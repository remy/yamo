package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/remy/yamo/internal/catalog"
	"github.com/remy/yamo/internal/tags"
)

// Changes is a sparse set of field updates, keyed by canonical field name.
//
// A missing key leaves the field alone; a key present with a null value clears
// it. That maps exactly onto JSON, so a client can express "set the artist"
// and "remove the comment" without a separate flag for each.
type Changes map[string]*string

// Extra keys accepted beyond the catalogue's own fields. Both halves of a
// "3/12" track number live in one tag, but only the first has a field of its
// own, and correcting the total is a real thing people need to do.
const (
	FieldTrackTotal = "tracktotal"
	FieldDiscTotal  = "disctotal"
)

// EditableFields lists every key Changes accepts.
func EditableFields() []string {
	out := []string{}
	for f := catalog.Field(0); f < catalog.Field(len(catalog.FieldNames)); f++ {
		if f.Editable() && catalog.FieldNames[f] != "" {
			out = append(out, catalog.FieldNames[f])
		}
	}
	out = append(out, FieldTrackTotal, FieldDiscTotal)
	sort.Strings(out)
	return out
}

// Validate rejects unknown or read-only fields before anything is written.
func (ch Changes) Validate() error {
	for k := range ch {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == FieldTrackTotal || key == FieldDiscTotal {
			continue
		}
		f, ok := catalog.LookupField(key)
		if !ok {
			return fmt.Errorf("library: unknown field %q", k)
		}
		if !f.Editable() {
			return fmt.Errorf("library: %q is derived from the file and cannot be set", k)
		}
	}
	return nil
}

// OpError records one file that could not be changed.
type OpError struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Error string `json:"error"`
}

// BatchResult reports the outcome of an operation over many tracks.
type BatchResult struct {
	Matched int  `json:"matched"`
	Changed int  `json:"changed"`
	Skipped int  `json:"skipped"` // already had the requested value
	Failed  int  `json:"failed"`
	DryRun  bool `json:"dryRun"`

	// BackupID names the journal this operation wrote, when it wrote one.
	// Pass it to /restore, or undo the job it belongs to; either way it is
	// how the change is taken back.
	BackupID string    `json:"backupId,omitempty"`
	Errors   []OpError `json:"errors,omitempty"`
}

// maxReportedErrors bounds the error list so a systematically broken run
// returns a usable response rather than fifty thousand identical messages.
const maxReportedErrors = 25

func (r *BatchResult) fail(id, path string, err error) {
	r.Failed++
	if len(r.Errors) < maxReportedErrors {
		r.Errors = append(r.Errors, OpError{ID: id, Path: path, Error: err.Error()})
	}
}

// Patch applies changes to one track and writes them to the file.
//
// When ifMatch is non-empty it must equal the track's current version, or the
// call fails with ErrConflict. That is what stops an edit made on a phone
// silently overwriting one made in the terminal a moment earlier.
func (s *Service) Patch(id string, ch Changes, ifMatch string) (Track, error) {
	if err := ch.Validate(); err != nil {
		return Track{}, err
	}
	changed, err := s.applyOne(id, ch, ifMatch, false, nil)
	if err != nil {
		return Track{}, err
	}
	if changed {
		s.markDirty()
		s.events.publish(Event{Type: EventTracksChanged, TrackIDs: []string{id}})
	}
	return s.Get(id)
}

// applyOne writes one track's changes. It reports whether anything changed.
//
// When jrn is non-nil the values about to be overwritten are recorded first,
// which is what makes the operation undoable. Only the fields that actually
// change are recorded: a batch that sets the artist on two thousand tracks
// where half already read that way journals the half it rewrites.
func (s *Service) applyOne(id string, ch Changes, ifMatch string, dryRun bool, jrn *journal) (bool, error) {
	// Take a copy under the read lock; the file write must not hold it.
	s.mu.RLock()
	i, ok := s.lookupLocked(id)
	if !ok {
		s.mu.RUnlock()
		return false, ErrNotFound
	}
	cur := s.cat.Tracks[i]
	s.mu.RUnlock()

	if ifMatch != "" && ifMatch != s.version(&cur) {
		return false, ErrConflict
	}
	if !cur.Format.Writable() {
		return false, fmt.Errorf("library: %s files cannot be written by this build", cur.Format)
	}

	edit, applied, before := editFromChanges(&cur, ch)
	if edit.Empty() {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	// Recorded before the write, not after: a write that fails halfway has
	// still changed the file, and a journal missing that record would offer an
	// undo that quietly skipped it.
	jrn.write(journalRecord{Path: cur.Path, Fields: before})

	// Serialise on the path: tag writing is a read-modify-write, and two
	// clients editing one track would otherwise lose an edit between them.
	err := s.locks.withPath(cur.Path, func() error { return tags.Write(cur.Path, edit) })
	if err != nil {
		return false, err
	}
	// The file has been rewritten, which a client holding its version needs to
	// see even when the padding absorbed the change and left the size and the
	// second identical.
	s.bumpRev(cur.Path)

	// The write changed the file's size and modification time, so the
	// in-memory record and its version have to catch up or the next If-Match
	// would spuriously conflict.
	size, modTime := cur.Size, cur.ModTime
	if fi, serr := os.Stat(cur.Path); serr == nil {
		size, modTime = fi.Size(), fi.ModTime().Unix()
	}

	s.mu.Lock()
	if j, ok := s.lookupLocked(id); ok {
		t := &s.cat.Tracks[j]
		applied(t)
		t.Size, t.ModTime = size, modTime
		t.Changed = 0 // written through; nothing is pending
		s.cat.Touch(int(j))
	}
	s.mu.Unlock()
	return true, nil
}

// editFromChanges builds the tag edit, a function that applies the same values
// to the in-memory record, and the values being overwritten.
//
// All three come from one pass because they have to agree: the applier keeps
// the catalogue in step with the file, and the previous values are what an
// undo writes back. Deriving any of them separately would be a second reading
// of the same rules, and the two would drift.
func editFromChanges(cur *catalog.Track, ch Changes) (*tags.Edit, func(*catalog.Track), map[string]*string) {
	e := &tags.Edit{}
	var appliers []func(*catalog.Track)
	before := map[string]*string{}

	// was records a field's current value for the journal. An empty field is
	// recorded as null rather than as "", because putting it back means
	// clearing it and those are different requests.
	was := func(name, value string) {
		if value == "" {
			before[name] = nil
			return
		}
		v := value
		before[name] = &v
	}

	for k, v := range ch {
		key := strings.ToLower(strings.TrimSpace(k))
		val := ""
		if v != nil {
			val = strings.TrimSpace(*v)
		}

		switch key {
		case FieldTrackTotal:
			if n := atoi32(val); n != cur.TrackTotal {
				e.SetInt("tracktotal", n)
				was(FieldTrackTotal, itoa32(cur.TrackTotal))
				appliers = append(appliers, func(t *catalog.Track) { t.TrackTotal = n })
			}
			continue
		case FieldDiscTotal:
			if n := atoi32(val); n != cur.DiscTotal {
				e.SetInt("disctotal", n)
				was(FieldDiscTotal, itoa32(cur.DiscTotal))
				appliers = append(appliers, func(t *catalog.Track) { t.DiscTotal = n })
			}
			continue
		}

		f, ok := catalog.LookupField(key)
		if !ok || !f.Editable() {
			continue
		}
		name := catalog.FieldNames[f]
		if f == catalog.FieldCompilation {
			// A flag is compared as a flag: "true" and "1" are the same
			// answer, and only a real change is worth rewriting a file for.
			want := catalog.IsTrue(val)
			if want == cur.Compilation {
				continue
			}
			e.SetBool(name, want)
			if cur.Compilation {
				was(name, "1")
			} else {
				was(name, "0")
			}
			appliers = append(appliers, func(t *catalog.Track) { t.Compilation = want })
			continue
		}
		if cur.String(f) == val {
			continue // already that value; do not rewrite the file for nothing
		}
		was(name, cur.String(f))
		switch f {
		case catalog.FieldYear, catalog.FieldTrackNo, catalog.FieldDisc:
			e.SetInt(name, atoi32(val))
		default:
			e.SetString(name, val)
		}
		field, value := f, val
		appliers = append(appliers, func(t *catalog.Track) { t.SetString(field, value) })
	}

	return e, func(t *catalog.Track) {
		for _, fn := range appliers {
			fn(t)
		}
	}, before
}

// itoa32 renders a number the way Changes carries one: as a string, and with
// zero written as empty, because a track total of zero is a field with nothing
// in it rather than the number nought.
func itoa32(n int32) string {
	if n == 0 {
		return ""
	}
	return strconv.FormatInt(int64(n), 10)
}

func atoi32(s string) int32 {
	var n int32
	neg := false
	for i, c := range s {
		if i == 0 && (c == '-' || c == '+') {
			neg = c == '-'
			continue
		}
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int32(c-'0')
		if n < 0 {
			return 0
		}
	}
	if neg {
		return -n
	}
	return n
}

// BatchSetRequest applies one set of changes across many tracks.
type BatchSetRequest struct {
	Selector Selector `json:"selector"`
	Set      Changes  `json:"set"`
	DryRun   bool     `json:"dryRun,omitempty"`

	// Backup records the values being overwritten so the job can be undone.
	// It defaults to true, which is the opposite of strip's default and for
	// the opposite reason: a strip is asked for deliberately and a batch edit
	// is the ordinary way to work here, so the recovery has to be there
	// without being asked for. It costs a line of text per changed file.
	Backup bool `json:"backup"`
}

// BatchSet starts a job that applies changes to every selected track.
//
// This is the operation the whole design exists for: "make the artist X on all
// of these", where "all of these" may be thousands of files chosen by a query
// rather than a list a phone had to upload.
func (s *Service) BatchSet(req BatchSetRequest) (*Job, error) {
	if err := req.Set.Validate(); err != nil {
		return nil, err
	}
	ids, err := s.Resolve(req.Selector)
	if err != nil {
		return nil, err
	}
	if len(req.Set) == 0 {
		return nil, errors.New("library: no changes given")
	}

	var jrn *journal
	if req.Backup && !req.DryRun {
		jrn = s.tryJournal(JournalEdit)
	}

	return s.jobs.StartWithJournal(JobEdit, jrn.ID(), func(ctx context.Context, j *Job) (any, error) {
		defer jrn.Close(j.ID)
		res := BatchResult{Matched: len(ids), DryRun: req.DryRun, BackupID: jrn.ID()}
		j.SetProgress(Progress{Total: int64(len(ids))})

		var touched []string
		for n, id := range ids {
			if ctx.Err() != nil {
				break
			}
			changed, err := s.applyOne(id, req.Set, "", req.DryRun, jrn)
			switch {
			case errors.Is(err, ErrNotFound):
				res.Skipped++
			case err != nil:
				path, _ := s.Path(id)
				res.fail(id, path, err)
			case changed:
				res.Changed++
				touched = append(touched, id)
			default:
				res.Skipped++
			}
			if n%64 == 0 || n == len(ids)-1 {
				j.SetProgress(Progress{Done: int64(n + 1), Total: int64(len(ids))})
			}
		}

		if len(touched) > 0 {
			s.markDirty()
			s.events.publish(Event{Type: EventTracksChanged, TrackIDs: touched})
		}
		return res, ctx.Err()
	}), nil
}

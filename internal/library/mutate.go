package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/remy/tag-manager/internal/catalog"
	"github.com/remy/tag-manager/internal/tags"
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
	Matched int       `json:"matched"`
	Changed int       `json:"changed"`
	Skipped int       `json:"skipped"` // already had the requested value
	Failed  int       `json:"failed"`
	DryRun  bool      `json:"dryRun"`
	Errors  []OpError `json:"errors,omitempty"`
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
	changed, err := s.applyOne(id, ch, ifMatch, false)
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
func (s *Service) applyOne(id string, ch Changes, ifMatch string, dryRun bool) (bool, error) {
	// Take a copy under the read lock; the file write must not hold it.
	s.mu.RLock()
	i, ok := s.lookupLocked(id)
	if !ok {
		s.mu.RUnlock()
		return false, ErrNotFound
	}
	cur := s.cat.Tracks[i]
	s.mu.RUnlock()

	if ifMatch != "" && ifMatch != TrackVersion(&cur) {
		return false, ErrConflict
	}
	if !cur.Format.Writable() {
		return false, fmt.Errorf("library: %s files cannot be written by this build", cur.Format)
	}

	edit, applied := editFromChanges(&cur, ch)
	if edit.Empty() {
		return false, nil
	}
	if dryRun {
		return true, nil
	}

	// Serialise on the path: tag writing is a read-modify-write, and two
	// clients editing one track would otherwise lose an edit between them.
	err := s.locks.withPath(cur.Path, func() error { return tags.Write(cur.Path, edit) })
	if err != nil {
		return false, err
	}

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

// editFromChanges builds the tag edit and a function that applies the same
// values to the in-memory record, so the two can never drift apart.
func editFromChanges(cur *catalog.Track, ch Changes) (*tags.Edit, func(*catalog.Track)) {
	e := &tags.Edit{}
	var appliers []func(*catalog.Track)

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
				appliers = append(appliers, func(t *catalog.Track) { t.TrackTotal = n })
			}
			continue
		case FieldDiscTotal:
			if n := atoi32(val); n != cur.DiscTotal {
				e.SetInt("disctotal", n)
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
			appliers = append(appliers, func(t *catalog.Track) { t.Compilation = want })
			continue
		}
		if cur.String(f) == val {
			continue // already that value; do not rewrite the file for nothing
		}
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
	}
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

	return s.jobs.Start(JobEdit, func(ctx context.Context, j *Job) (any, error) {
		res := BatchResult{Matched: len(ids), DryRun: req.DryRun}
		j.SetProgress(Progress{Total: int64(len(ids))})

		var touched []string
		for n, id := range ids {
			if ctx.Err() != nil {
				break
			}
			changed, err := s.applyOne(id, req.Set, "", req.DryRun)
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

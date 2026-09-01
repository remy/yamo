package library

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/remy/yamo/internal/catalog"
)

// Renaming files from the tags they carry.
//
// This is the other half of the split. A split reads a filename's worth of
// information out of a title; this writes the tags back out into the name, and
// it is what a library looks like once the tags are right: "01 Blue Suede
// Shoes.mp3" under "Elvis Presley/The Sun Sessions", rather than
// "track01.mp3" under "unsorted".
//
// It has to be a batch operation for the same reason a batch edit does. The
// per-track rename endpoint takes the destination as a string, so renaming two
// thousand files by it means the client computing two thousand names, and every
// client computing them differently. The template is computed here, once, from
// the same fields the search language names.

// RenameTracksRequest renames each selected file after its own tags.
type RenameTracksRequest struct {
	Selector Selector `json:"selector"`

	// Template is the destination path, naming fields with a leading $:
	// "$track $title", or "$albumartist/$album/$track $title" to file the
	// whole library. A literal dollar is written "$$".
	//
	// The extension is never part of it. A rename may not change a file's
	// container — the catalogue records the format, and a FLAC named .mp3 is
	// a lie every player believes — so the extension the file already has is
	// appended to whatever the template produces.
	Template string `json:"template"`

	// DryRun reports what would happen without moving anything. Worth doing
	// first, and more so than for an edit: a wrong template moves every
	// selected file somewhere wrong, and undoing that is a second pass over
	// the library rather than a rewrite of a field.
	DryRun bool `json:"dryRun,omitempty"`

	// Backup records where each file came from so the job can be undone.
	// Defaults to true.
	Backup bool `json:"backup"`
}

// RenameSample is one file and where the template would put it.
type RenameSample struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// RenameResult reports what a rename did or would do.
type RenameResult struct {
	BatchResult

	Template string `json:"template"`

	// Fields names what the template reads, in the order it names them.
	Fields []string `json:"fields"`

	// Incomplete counts tracks the template could not be filled in for,
	// because a field it names is empty on that file. They are not failures:
	// nothing is wrong with the file, it simply has no album to file itself
	// under, and this is the number that says whether the tags are ready for
	// the template.
	Incomplete int `json:"incomplete"`

	// Collisions counts destinations two selected tracks both wanted, or that
	// something already occupied. Reported separately from failures because
	// the answer is a better template — usually one carrying $disc or
	// $track — rather than a retry.
	Collisions int `json:"collisions"`

	// Samples shows a few worked examples, which is what makes a dry run
	// worth running: the count says how much would move, and these say
	// whether it would move anywhere sensible.
	Samples []RenameSample `json:"samples,omitempty"`
}

// maxRenameSamples bounds the worked examples a result carries.
const maxRenameSamples = 5

// renameTemplate is a compiled destination template.
type renameTemplate struct {
	parts  []renamePart
	fields []string
}

// renamePart is one literal or one field reference.
type renamePart struct {
	literal string
	field   catalog.Field
	name    string
	isField bool

	// pad is how many digits a numeric field is written to. Track and disc
	// numbers are padded to two, because "2 Blue Suede Shoes" sorts after
	// "10 Hound Dog" in every file browser there is, and the whole point of
	// putting the number first is to make the directory sort as the record
	// plays.
	pad int
}

// ErrBadTemplate is shared with the split template: both are a client's
// mistake in describing a shape, and both deserve to say which part is wrong
// rather than to fail as a server error.

// compileRename turns a destination template into a renderer.
func compileRename(tmpl string) (*renameTemplate, error) {
	tmpl = strings.TrimSpace(tmpl)
	if tmpl == "" {
		return nil, fmt.Errorf("%w: it is empty", ErrBadTemplate)
	}
	if filepath.IsAbs(tmpl) {
		return nil, fmt.Errorf("%w: it must be relative to the track's own directory, not an absolute path", ErrBadTemplate)
	}

	out := &renameTemplate{}
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			out.parts = append(out.parts, renamePart{literal: lit.String()})
			lit.Reset()
		}
	}

	for i := 0; i < len(tmpl); {
		if tmpl[i] != '$' {
			lit.WriteByte(tmpl[i])
			i++
			continue
		}
		if i+1 < len(tmpl) && tmpl[i+1] == '$' {
			lit.WriteByte('$')
			i += 2
			continue
		}
		j := i + 1
		for j < len(tmpl) && (tmpl[j] >= 'a' && tmpl[j] <= 'z') {
			j++
		}
		name := tmpl[i+1 : j]
		if name == "" {
			return nil, fmt.Errorf("%w: a $ with no field name after it", ErrBadTemplate)
		}
		f, ok := catalog.LookupField(name)
		if !ok {
			return nil, fmt.Errorf("%w: %q is not a field", ErrBadTemplate, name)
		}
		if f == catalog.FieldPath {
			return nil, fmt.Errorf("%w: $path is where the file already is, so a template naming it would not move anything", ErrBadTemplate)
		}
		flush()
		part := renamePart{field: f, name: catalog.FieldNames[f], isField: true}
		if f == catalog.FieldTrackNo || f == catalog.FieldDisc {
			part.pad = 2
		}
		out.parts = append(out.parts, part)
		out.fields = append(out.fields, part.name)
		i = j
	}
	flush()

	if len(out.fields) == 0 {
		return nil, fmt.Errorf("%w: it names no fields, so every file would be given the same name", ErrBadTemplate)
	}
	return out, nil
}

// render fills the template in for one track, returning the destination path
// relative to the library root and whether every field it names had a value.
func (rt *renameTemplate) render(t *catalog.Track) (string, bool) {
	var b strings.Builder
	complete := true
	for _, p := range rt.parts {
		if !p.isField {
			b.WriteString(p.literal)
			continue
		}
		v := t.String(p.field)
		// The album artist falls back to the artist, which is the same key
		// /albums, /artists and /folders group on. Filing a library by album
		// artist is exactly that grouping written to disk, so a template that
		// meant something narrower here would put a track in a different
		// place from the one the browse listings say it belongs to — and in a
		// library where most files never had an album artist written, that is
		// most tracks.
		if p.field == catalog.FieldAlbumArtist && strings.TrimSpace(v) == "" {
			v = t.String(catalog.FieldArtist)
		}
		if p.pad > 0 {
			// A number reads as "0" when the field is empty, which is not a
			// value; it is the absence of one.
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n <= 0 {
				complete = false
				continue
			}
			v = fmt.Sprintf("%0*d", p.pad, n)
		}
		if strings.TrimSpace(v) == "" {
			complete = false
			continue
		}
		b.WriteString(sanitiseSegment(v))
	}
	return tidyPath(b.String()), complete
}

// sanitiseSegment makes one field's value safe to use as part of a filename.
//
// A separator inside a value would silently create a directory: an artist of
// "AC/DC" under "$artist/$album" would file the album under "DC" inside a
// folder called "AC". The characters Windows refuses are replaced too, because
// a library on a NAS is read over SMB as often as not and a name it cannot
// represent is a file that does not appear.
func sanitiseSegment(v string) string {
	var b strings.Builder
	b.Grow(len(v))
	for _, r := range v {
		switch r {
		case '/', '\\':
			b.WriteByte('-')
		case ':', '*', '?', '"', '<', '>', '|':
			b.WriteByte('_')
		default:
			if r < 0x20 || r == 0x7f {
				continue // control characters have no business in a filename
			}
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// tidyPath cleans up what a template with an empty field left behind: a
// doubled separator, a leading or trailing one, and the spaces and dashes that
// were separating something from nothing.
func tidyPath(p string) string {
	segs := strings.Split(p, "/")
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		s = strings.TrimSpace(s)
		s = strings.Trim(s, " -_")
		s = strings.TrimSpace(s)
		// A trailing dot is stripped by Windows and confuses everything else.
		s = strings.TrimRight(s, ".")
		if s == "" || s == "." || s == ".." {
			continue
		}
		out = append(out, s)
	}
	return strings.Join(out, "/")
}

// RenameTracks starts a job that renames every selected file after its tags.
func (s *Service) RenameTracks(req RenameTracksRequest) (*Job, error) {
	rt, err := compileRename(req.Template)
	if err != nil {
		return nil, err
	}
	ids, err := s.Resolve(req.Selector)
	if err != nil {
		return nil, err
	}

	var jrn *journal
	if req.Backup && !req.DryRun {
		jrn = s.tryJournal(JournalRename)
	}

	return s.jobs.StartWithJournal(JobRename, jrn.ID(), func(ctx context.Context, j *Job) (any, error) {
		defer jrn.Close(j.ID)
		res := RenameResult{
			BatchResult: BatchResult{Matched: len(ids), DryRun: req.DryRun, BackupID: jrn.ID()},
			Template:    strings.TrimSpace(req.Template),
			Fields:      rt.fields,
		}
		j.SetProgress(Progress{Total: int64(len(ids))})

		// Two selected tracks rendering to the same name would have the second
		// land on the first — or, in a dry run, be reported as fine and then
		// fail for real. Claimed destinations are tracked so the collision is
		// reported either way.
		claimed := map[string]bool{}

		for n, id := range ids {
			if ctx.Err() != nil {
				break
			}
			from, dest, complete, err := s.renderFor(id, rt)
			switch {
			case err != nil:
				res.Skipped++
			case !complete:
				res.Incomplete++
			case dest == from:
				res.Skipped++ // already named that; nothing to do
			case claimed[dest]:
				res.Collisions++
				res.fail(id, from, fmt.Errorf("%w: two selected tracks both want %s", ErrExists, dest))
			default:
				claimed[dest] = true
				if len(res.Samples) < maxRenameSamples {
					res.Samples = append(res.Samples, RenameSample{From: from, To: dest})
				}
				if req.DryRun {
					res.Changed++
					break
				}
				if _, err := s.Rename(id, dest, ""); err != nil {
					if errors.Is(err, ErrExists) {
						res.Collisions++
					}
					res.fail(id, from, err)
				} else {
					// Recorded after the move rather than before it. A rename
					// is atomic, unlike a tag write, so there is no window
					// where the file is half-changed — and a record written
					// first would name a destination that a failed move never
					// created, offering an undo that could only fail.
					jrn.write(journalRecord{Path: dest, From: from})
					res.Changed++
				}
			}
			if n%32 == 0 || n == len(ids)-1 {
				j.SetProgress(Progress{Done: int64(n + 1), Total: int64(len(ids))})
			}
		}
		return res, ctx.Err()
	}), nil
}

// renderFor works out where one track should end up.
//
// The template names a path relative to the library root the track sits under,
// not to its own directory: a template that files by artist and album has to
// be able to move a track out of the wrong folder, and resolving against the
// folder it is wrongly in would nest the right one inside it. With no roots
// recorded, the track's own directory stands in, exactly as a single rename
// does.
func (s *Service) renderFor(id string, rt *renameTemplate) (from, dest string, complete bool, err error) {
	s.mu.RLock()
	i, ok := s.lookupLocked(id)
	if !ok {
		s.mu.RUnlock()
		return "", "", false, ErrNotFound
	}
	cur := s.cat.Tracks[i]
	roots := append([]string(nil), s.cat.Roots...)
	s.mu.RUnlock()

	rel, complete := rt.render(&cur)
	if !complete || rel == "" {
		return cur.Path, "", false, nil
	}

	base := rootFor(cur.Path, roots)
	return cur.Path, filepath.Join(base, filepath.FromSlash(rel)) + filepath.Ext(cur.Path), true, nil
}

// rootFor picks the library root a path sits under.
func rootFor(path string, roots []string) string {
	for _, r := range roots {
		if under(path, r) {
			return filepath.Clean(r)
		}
	}
	return filepath.Dir(path)
}

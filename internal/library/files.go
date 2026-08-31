package library

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Moving and removing the files themselves.
//
// Every other operation here changes what is inside a file. These two change
// which files there are, which makes them the only ones that can lose music
// rather than metadata — so both are deliberately narrow. A rename may not
// leave the library, may not land on something that already exists, and may
// not change the extension, because the catalogue records the format and a
// file renamed across containers would lie about what it is.
//
// A track's identity is derived from its path, so a rename changes the id. The
// new track is returned rather than left for the client to recompute, and the
// change event carries both ids so a cache keyed by the old one can drop it.

// ErrBadPath means the destination of a rename cannot be used.
var ErrBadPath = errors.New("library: the destination cannot be used")

// ErrExists means something is already at the destination. It is separate from
// ErrConflict because the client's answer is different: not "read it again and
// retry" but "choose another name".
var ErrExists = errors.New("library: something is already there")

// Rename moves one file and follows it in the catalogue.
//
// The destination may be a bare name, which renames in place; a relative path,
// which resolves against the track's own directory; or an absolute path.
// Missing parent directories are created, so moving a stray track into the
// album folder it belongs in is one request.
//
// When ifMatch is non-empty it must equal the track's current version, on the
// same reasoning as an edit: the file may have changed since the client read
// it, and moving something you have not seen is worse than editing it.
func (s *Service) Rename(id, dest, ifMatch string) (Track, error) {
	s.mu.RLock()
	i, ok := s.lookupLocked(id)
	if !ok {
		s.mu.RUnlock()
		return Track{}, ErrNotFound
	}
	cur := s.cat.Tracks[i]
	roots := append([]string(nil), s.cat.Roots...)
	s.mu.RUnlock()

	if ifMatch != "" && ifMatch != TrackVersion(&cur) {
		return Track{}, ErrConflict
	}

	target, err := resolveDest(cur.Path, dest, roots)
	if err != nil {
		return Track{}, err
	}
	if target == cur.Path {
		return s.Get(id) // already called that; not worth a failure
	}

	// Both ends are locked: the source because a tag write must not run
	// through a move, and the destination so two renames aiming at the same
	// name cannot both find it free. Nothing can be done about another program
	// creating the file between the check and the move, which is why the check
	// is not the only safeguard — the extension and containment rules bound
	// what a move can reach at all.
	err = s.locks.withPaths(cur.Path, target, func() error {
		if _, err := os.Lstat(target); err == nil {
			return fmt.Errorf("%w: %s already exists", ErrExists, target)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.Rename(cur.Path, target); err != nil {
			// Roots may sit on different mounts — two volumes on a NAS is the
			// normal case, not an exotic one — and rename cannot cross them.
			if errors.Is(err, syscall.EXDEV) {
				return moveAcrossDevices(cur.Path, target)
			}
			return err
		}
		return nil
	})
	if err != nil {
		return Track{}, err
	}

	// A copy across devices gives the file a new modification time, and the
	// version is derived from it, so the record has to catch up or the next
	// If-Match would spuriously conflict.
	size, modTime := cur.Size, cur.ModTime
	if fi, err := os.Stat(target); err == nil {
		size, modTime = fi.Size(), fi.ModTime().Unix()
	}
	newID := TrackID(target)

	s.mu.Lock()
	// The catalogue may have been replaced by a scan while the file moved. The
	// move still happened, so the track is described from the copy taken
	// above; the scan will find it where it now is.
	if j, ok := s.lookupLocked(id); ok && s.cat.Tracks[j].Path == cur.Path {
		t := &s.cat.Tracks[j]
		t.Path, t.Size, t.ModTime = target, size, modTime
		s.cat.Touch(int(j)) // the path is searchable, so its index row is stale
		delete(s.byID, id)
		s.byID[newID] = j
		cur = *t
	} else {
		cur.Path, cur.Size, cur.ModTime = target, size, modTime
	}
	s.mu.Unlock()

	s.markDirty()
	s.events.publish(Event{Type: EventTracksChanged, TrackIDs: []string{id, newID}})
	return toTrack(&cur), nil
}

// Delete removes one file from disk and from the catalogue.
//
// It is a real deletion, not a move to a holding area: the strip backup exists
// because tags removed from a file cannot be recovered from anywhere else,
// whereas a NAS running this has snapshots and a bin of its own for whole
// files. What the API owes a client is that the catalogue stops listing what
// is no longer there.
func (s *Service) Delete(id, ifMatch string) error {
	s.mu.RLock()
	i, ok := s.lookupLocked(id)
	if !ok {
		s.mu.RUnlock()
		return ErrNotFound
	}
	cur := s.cat.Tracks[i]
	s.mu.RUnlock()

	if ifMatch != "" && ifMatch != TrackVersion(&cur) {
		return ErrConflict
	}

	err := s.locks.withPath(cur.Path, func() error {
		// A file already gone is the outcome that was asked for. The catalogue
		// is a snapshot and can name files that were removed outside this
		// program; failing here would leave those entries impossible to clear
		// without a full rescan.
		if err := os.Remove(cur.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	if j, ok := s.lookupLocked(id); ok && s.cat.Tracks[j].Path == cur.Path {
		s.cat.Remove(int(j))
		// Removing shifts every index past it, so the identity map and the
		// search index are rebuilt rather than patched. Deletions are rare and
		// a rebuild is one pass.
		s.reindexLocked()
	}
	s.mu.Unlock()

	s.markDirty()
	s.events.publish(Event{Type: EventTracksChanged, TrackIDs: []string{id}})
	return nil
}

// resolveDest works out where a rename is asking for, and refuses the requests
// that should never be honoured.
func resolveDest(cur, dest string, roots []string) (string, error) {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return "", fmt.Errorf("%w: it is empty", ErrBadPath)
	}

	// A trailing separator says "into this directory", which this does not do:
	// the destination names the file, not where to put it. Saying so beats the
	// extension complaint the cleaned path would otherwise produce.
	if strings.HasSuffix(dest, "/") || strings.HasSuffix(dest, string(filepath.Separator)) {
		return "", fmt.Errorf("%w: it names a directory rather than a file", ErrBadPath)
	}

	target := dest
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(cur), target)
	}
	target = filepath.Clean(target)
	if filepath.Base(target) == "." || filepath.Base(target) == string(filepath.Separator) {
		return "", fmt.Errorf("%w: it names a directory rather than a file", ErrBadPath)
	}

	// The extension is how the catalogue knows what the file is, and every
	// player reads it the same way. Changing it renames a FLAC into a lie.
	if !strings.EqualFold(filepath.Ext(target), filepath.Ext(cur)) {
		return "", fmt.Errorf("%w: the extension must stay %s, or the file would claim a format it is not",
			ErrBadPath, filepath.Ext(cur))
	}

	// Containment is what stops this being an arbitrary write primitive: with
	// a token on a network-bound server, a move to any path at all would let a
	// client scatter files across the NAS. With no roots recorded — a
	// catalogue built by hand, or one loaded from an unreadable snapshot — the
	// track's own directory tree stands in for the library.
	bounds := roots
	if len(bounds) == 0 {
		bounds = []string{filepath.Dir(cur)}
	}
	for _, root := range bounds {
		if under(target, root) {
			return target, nil
		}
	}
	return "", fmt.Errorf("%w: %s is outside the library", ErrBadPath, target)
}

// under reports whether path sits inside dir. Both are cleaned first, and the
// separator matters: "/music-old/x.mp3" is not inside "/music".
func under(path, dir string) bool {
	dir = filepath.Clean(dir)
	if dir == "" || dir == "." {
		return false
	}
	if !strings.HasSuffix(dir, string(filepath.Separator)) {
		dir += string(filepath.Separator)
	}
	return strings.HasPrefix(filepath.Clean(path), dir)
}

// moveAcrossDevices copies a file to another mount and removes the original.
//
// It writes a temporary name beside the destination and renames it into place,
// so an interrupted copy leaves a stray temporary rather than a truncated
// track that looks playable. The source is removed only once the copy is
// safely on disk: a move that half fails should lose nothing.
func moveAcrossDevices(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	mode := fs.FileMode(0o644)
	if fi, err := in.Stat(); err == nil {
		mode = fi.Mode().Perm()
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".yamo-move-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // a no-op once the rename below has succeeded

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

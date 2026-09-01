package library

import (
	"strconv"

	"github.com/remy/yamo/internal/catalog"
)

// Making a version actually identify a version.
//
// A track's version is derived from its path, size and modification time, and
// it is what `If-Match` and every `ETag` here are built on. Two clients editing
// one library depend on it, and so does a browser deciding whether the cover it
// already holds is still current.
//
// The trouble is that size and modification time do not always change when the
// file does. Every tag format reserves padding so that a small edit does not
// have to rewrite the whole file, so replacing a value with one of a different
// length — or a 7KB cover with a 1.5KB one — routinely leaves the file exactly
// the same length. Modification time is recorded in whole seconds. Two writes
// inside one second that happen to leave the same length are therefore
// indistinguishable, and that is not a rare shape: it is what a client pasting
// artwork across an album does.
//
// What that costs is precisely the guarantee the version exists to give. An
// If-Match would pass against a file that had already been rewritten, and a
// conditional GET would answer 304 for a cover that had changed.
//
// So the version carries a revision counter as well: a count of the writes this
// server has made to that path since it started. It is not persisted and it does
// not need to be. What it has to catch is two clients writing through this
// server a moment apart, which is the case the concurrency story is about — an
// edit made on a phone and one made in the terminal both arrive here. A change
// made by another program on the machine is still only visible through size and
// modification time, but that was already true of the catalogue itself, and a
// scan is what reconciles it.

// bumpRev records that this server has written to a path.
//
// The map holds an entry per path written since startup, so it grows with the
// edits somebody makes rather than with the size of the library. It is not
// cleared by a scan: a version must never go backwards, or a client holding one
// from before the scan would find its If-Match passing again.
func (s *Service) bumpRev(path string) {
	s.revsMu.Lock()
	if s.revs == nil {
		s.revs = map[string]uint64{}
	}
	s.revs[path]++
	s.revsMu.Unlock()
}

// rev returns how many times this server has written to a path.
func (s *Service) rev(path string) uint64 {
	s.revsMu.Lock()
	defer s.revsMu.Unlock()
	return s.revs[path]
}

// version is the identifier a client sends back as If-Match or If-None-Match.
func (s *Service) version(t *catalog.Track) string {
	r := s.rev(t.Path)
	if r == 0 {
		// Untouched by this server, so the file's own state is the whole
		// story — and leaving the version unchanged here means a client that
		// cached one across a restart is not needlessly invalidated.
		return TrackVersion(t)
	}
	h := hash64(TrackVersion(t))
	h = hashMix(h, r)
	return strconv.FormatUint(h, 16)
}

// toTrack converts a catalogue entry, stamping the version this server would
// honour rather than the one the file alone implies.
func (s *Service) toTrack(t *catalog.Track) Track {
	out := toTrack(t)
	out.Version = s.version(t)
	return out
}

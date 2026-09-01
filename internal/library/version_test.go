package library

import (
	"testing"

	"github.com/remy/yamo/internal/tags"
)

// The case the revision counter exists for.
//
// Tag formats reserve padding, so replacing a value with one of a different
// length routinely leaves the file exactly as long as it was — and the
// modification time is recorded in whole seconds. Two writes inside one second
// that leave the same length are indistinguishable from size and time alone,
// which would let a stale If-Match through and answer 304 for a cover that had
// changed.
func TestVersionChangesOnEveryWrite(t *testing.T) {
	s, _ := realService(t, 1)
	id := s.matchIDs("")[0]

	before, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}

	value := "Something Else Entirely"
	if _, err := s.Patch(id, Changes{"comment": &value}, ""); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version == before.Version {
		t.Fatalf("the version is still %s after an edit; a stale If-Match would pass",
			after.Version)
	}

	// And the stale one is refused, which is the guarantee the version exists
	// to give.
	if _, err := s.Patch(id, Changes{"comment": &value}, before.Version); err != ErrConflict {
		t.Errorf("editing with the old version gave %v, want ErrConflict", err)
	}
	// While the current one is honoured.
	other := "Yet Another"
	if _, err := s.Patch(id, Changes{"comment": &other}, after.Version); err != nil {
		t.Errorf("editing with the current version failed: %v", err)
	}
}

// The same, for a cover: a small image replacing a large one inside the same
// second is the shape a client pasting artwork across an album produces.
func TestVersionChangesOnArtworkWrite(t *testing.T) {
	s, _ := realService(t, 1)
	id := s.matchIDs("")[0]

	large, err := tags.NewPicture(testJPEG(t, 400, 400))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetArtwork(id, large, ""); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(id)

	small, err := tags.NewPicture(testPNGSize(t, 400, 400))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetArtwork(id, small, ""); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Get(id)

	if after.Version == before.Version {
		t.Errorf("the version is still %s after the cover changed", after.Version)
	}
	if err := s.SetArtwork(id, large, before.Version); err != ErrConflict {
		t.Errorf("pasting with the old version gave %v, want ErrConflict", err)
	}
}

// A version must never go backwards, or a client holding a stale one would
// find its If-Match passing again later.
func TestVersionDoesNotGoBackwards(t *testing.T) {
	s, _ := realService(t, 1)
	id := s.matchIDs("")[0]

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		v := fmtVersion(t, s, id)
		if seen[v] {
			t.Fatalf("version %s came round again after %d edits", v, i)
		}
		seen[v] = true

		value := string(rune('a' + i))
		if _, err := s.Patch(id, Changes{"comment": &value}, ""); err != nil {
			t.Fatal(err)
		}
	}
}

func fmtVersion(t *testing.T, s *Service, id string) string {
	t.Helper()
	tr, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return tr.Version
}

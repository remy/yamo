package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/remy/tag-manager/internal/catalog"
	"github.com/remy/tag-manager/internal/tags"
)

// newFileService opens a service over a catalogue whose files really exist.
//
// Moving and deleting are the two operations that never open a file, so the
// contents need not be audio; what they need is to be there, because the whole
// point of the tests below is what happens on disk.
func newFileService(t *testing.T, names ...string) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	music := filepath.Join(dir, "music")

	c := catalog.New()
	for i, name := range names {
		path := filepath.Join(music, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Repeat("x", 64+i)), 0o644); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		c.Tracks = append(c.Tracks, catalog.Track{
			Path:    path,
			Title:   fmt.Sprintf("Song %d", i+1),
			Artist:  "Elvis Presley",
			Album:   "Sun Sessions",
			TrackNo: int32(i + 1),
			Format:  tags.FormatMP3,
			Size:    fi.Size(),
			ModTime: fi.ModTime().Unix(),
		})
	}
	return openService(t, dir, c), music
}

func TestRenameMovesTheFileAndTheIdentity(t *testing.T) {
	s, music := newFileService(t, "01 song.mp3", "02 song.mp3")

	old := filepath.Join(music, "01 song.mp3")
	oldID := TrackID(old)

	moved, err := s.Rename(oldID, "01 Blue Suede Shoes.mp3", "")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}

	want := filepath.Join(music, "01 Blue Suede Shoes.mp3")
	if moved.Path != want {
		t.Fatalf("renamed to %q, want %q", moved.Path, want)
	}
	if moved.ID == oldID {
		t.Error("the id did not change, but identity is derived from the path")
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("the file is not at its new path: %v", err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the file is still at its old path: %v", err)
	}

	// The old identity must stop resolving and the new one must work, or a
	// client would be able to address one file by two names.
	if _, err := s.Get(oldID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the old id still resolves: %v", err)
	}
	if got, err := s.Get(moved.ID); err != nil || got.Path != want {
		t.Errorf("the new id resolves to %q, %v", got.Path, err)
	}
	// The title is unchanged, so the metadata travelled with the file rather
	// than the record being rebuilt from the name.
	if moved.Title != "Song 1" {
		t.Errorf("the moved track's title is %q, want %q", moved.Title, "Song 1")
	}

	// The path is searchable, so the index row has to have been rebuilt.
	if ids := s.matchIDs("path:suede"); len(ids) != 1 || ids[0] != moved.ID {
		t.Errorf("searching for the new path found %v", ids)
	}
	if ids := s.matchIDs("path:\"01 song\""); len(ids) != 0 {
		t.Errorf("the old path is still in the index: %v", ids)
	}
}

func TestRenameCreatesTheDirectoriesItNeeds(t *testing.T) {
	s, music := newFileService(t, "stray.mp3")
	id := TrackID(filepath.Join(music, "stray.mp3"))

	moved, err := s.Rename(id, "Elvis Presley/Sun Sessions/01 song.mp3", "")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	want := filepath.Join(music, "Elvis Presley", "Sun Sessions", "01 song.mp3")
	if moved.Path != want {
		t.Fatalf("moved to %q, want %q", moved.Path, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("the album folder was not created: %v", err)
	}
}

func TestRenameRefusesWhatItShould(t *testing.T) {
	s, music := newFileService(t, "01 song.mp3", "02 song.mp3")
	id := TrackID(filepath.Join(music, "01 song.mp3"))

	cases := []struct {
		name string
		dest string
		want error
	}{
		{"empty", "  ", ErrBadPath},
		{"a different extension", "01 song.flac", ErrBadPath},
		{"outside the library", filepath.Join(filepath.Dir(music), "elsewhere.mp3"), ErrBadPath},
		{"escaping with ..", "../../elsewhere.mp3", ErrBadPath},
		{"a directory", "Sun Sessions/", ErrBadPath},
		{"a file that is already there", "02 song.mp3", ErrExists},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := s.Rename(id, c.dest, ""); !errors.Is(err, c.want) {
				t.Fatalf("renaming to %q gave %v, want %v", c.dest, err, c.want)
			}
			// Nothing may have moved: a refusal that half happened would be
			// worse than no check at all.
			if _, err := os.Stat(filepath.Join(music, "01 song.mp3")); err != nil {
				t.Fatalf("the file moved anyway: %v", err)
			}
		})
	}

	if _, err := s.Rename("nosuchtrack", "x.mp3", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("renaming an unknown track gave %v, want ErrNotFound", err)
	}
}

func TestRenameHonoursIfMatch(t *testing.T) {
	s, music := newFileService(t, "01 song.mp3")
	id := TrackID(filepath.Join(music, "01 song.mp3"))

	if _, err := s.Rename(id, "renamed.mp3", "not-the-version"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a stale version gave %v, want ErrConflict", err)
	}
	if _, err := os.Stat(filepath.Join(music, "01 song.mp3")); err != nil {
		t.Fatalf("the file moved despite the conflict: %v", err)
	}

	cur, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rename(id, "renamed.mp3", cur.Version); err != nil {
		t.Fatalf("the current version was refused: %v", err)
	}
}

// A rename to the name a file already has is a no-op rather than a conflict
// with itself, which is what makes a client that resends one safe.
func TestRenameToTheSameNameIsHarmless(t *testing.T) {
	s, music := newFileService(t, "01 song.mp3")
	id := TrackID(filepath.Join(music, "01 song.mp3"))

	got, err := s.Rename(id, "01 song.mp3", "")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got.ID != id {
		t.Errorf("the id changed for a rename that moved nothing")
	}
	if _, err := os.Stat(filepath.Join(music, "01 song.mp3")); err != nil {
		t.Errorf("the file is gone: %v", err)
	}
}

func TestRenameTellsBothIdentities(t *testing.T) {
	s, music := newFileService(t, "01 song.mp3")
	id := TrackID(filepath.Join(music, "01 song.mp3"))

	events, stop := s.Events().Subscribe()
	defer stop()

	moved, err := s.Rename(id, "renamed.mp3", "")
	if err != nil {
		t.Fatal(err)
	}

	select {
	case e := <-events:
		if e.Type != EventTracksChanged {
			t.Fatalf("published %q, want %q", e.Type, EventTracksChanged)
		}
		// Both, so a client caching by the old id knows to drop it and one
		// caching by the new id knows to fetch it.
		if len(e.TrackIDs) != 2 || e.TrackIDs[0] != id || e.TrackIDs[1] != moved.ID {
			t.Fatalf("the event named %v, want [%s %s]", e.TrackIDs, id, moved.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event was published for the rename")
	}
}

func TestDeleteRemovesTheFileAndTheEntry(t *testing.T) {
	s, music := newFileService(t, "01 song.mp3", "02 song.mp3", "03 song.mp3")
	path := filepath.Join(music, "02 song.mp3")
	id := TrackID(path)

	if err := s.Delete(id, ""); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the file is still there: %v", err)
	}
	if _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
		t.Errorf("the catalogue still lists it: %v", err)
	}

	// Removing shifts every entry past it, so the surviving tracks are the
	// check that the identity map and the search index were rebuilt rather
	// than left pointing at the wrong rows.
	all := s.List(ListParams{Limit: MaxLimit})
	if all.Total != 2 {
		t.Fatalf("the library holds %d tracks, want 2", all.Total)
	}
	for _, it := range all.Items {
		got, err := s.Get(it.ID)
		if err != nil {
			t.Fatalf("%s no longer resolves: %v", it.Path, err)
		}
		if got.Path != it.Path {
			t.Fatalf("%s resolves to %s", it.Path, got.Path)
		}
	}
	if ids := s.matchIDs("path:\"02 song\""); len(ids) != 0 {
		t.Errorf("the deleted track is still in the search index: %v", ids)
	}
}

// The catalogue is a snapshot, so it can name files something else removed. A
// delete has to be able to clear those entries, or the only way to tidy one
// away would be a full rescan.
func TestDeleteClearsAnEntryWhoseFileHasGone(t *testing.T) {
	s, music := newFileService(t, "01 song.mp3")
	path := filepath.Join(music, "01 song.mp3")
	id := TrackID(path)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(id, ""); err != nil {
		t.Fatalf("deleting an already-missing file: %v", err)
	}
	if _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
		t.Errorf("the entry survived: %v", err)
	}
}

func TestDeleteHonoursIfMatch(t *testing.T) {
	s, music := newFileService(t, "01 song.mp3")
	path := filepath.Join(music, "01 song.mp3")
	id := TrackID(path)

	if err := s.Delete(id, "not-the-version"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a stale version gave %v, want ErrConflict", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file was deleted despite the conflict: %v", err)
	}
	if err := s.Delete("nosuchtrack", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting an unknown track gave %v, want ErrNotFound", err)
	}

	cur, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(id, cur.Version); err != nil {
		t.Fatalf("the current version was refused: %v", err)
	}
}

// The snapshot has to survive both operations, or a restart would resurrect
// tracks that no longer exist and lose the ones that moved.
func TestRenameAndDeleteReachTheSnapshot(t *testing.T) {
	s, music := newFileService(t, "01 song.mp3", "02 song.mp3")
	moved, err := s.Rename(TrackID(filepath.Join(music, "01 song.mp3")), "renamed.mp3", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(TrackID(filepath.Join(music, "02 song.mp3")), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	c, err := catalog.Load(s.opts.CatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tracks) != 1 {
		t.Fatalf("the snapshot holds %d tracks, want 1", len(c.Tracks))
	}
	if c.Tracks[0].Path != moved.Path {
		t.Fatalf("the snapshot holds %q, want %q", c.Tracks[0].Path, moved.Path)
	}
}

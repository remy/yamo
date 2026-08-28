package catalog

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The sort fields have to survive the snapshot, or they would read correctly
// off disk and then vanish the moment the catalogue was saved and reloaded.
func TestCodecRoundTripsSortFields(t *testing.T) {
	orig := makeCatalog(20)
	for i := range orig.Tracks {
		orig.Tracks[i].TitleSort = "Song Number Sorted"
		orig.Tracks[i].ArtistSort = "Presley, Elvis"
		orig.Tracks[i].AlbumSort = "Sun Sessions, The"
		orig.Tracks[i].AlbumArtistSort = "Various Artists"
		orig.Tracks[i].ComposerSort = "Leiber, Jerry"
		orig.Tracks[i].Compilation = i%2 == 0
	}
	got, err := Decode(Encode(orig))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for i := range orig.Tracks {
		a, b := orig.Tracks[i], got.Tracks[i]
		if a.TitleSort != b.TitleSort || a.ArtistSort != b.ArtistSort ||
			a.AlbumSort != b.AlbumSort || a.AlbumArtistSort != b.AlbumArtistSort ||
			a.ComposerSort != b.ComposerSort || a.Compilation != b.Compilation {
			t.Fatalf("track %d did not round-trip:\n got %+v\nwant %+v", i, b, a)
		}
	}
}

// A snapshot from an older build must be refused rather than decoded with the
// new fields blank.
//
// This is the whole reason for the version bump. Scanning is incremental: a
// file whose size and modification time are unchanged is carried over from the
// previous catalogue without being opened, so a field left blank by an old
// snapshot stays blank through every rescan. Accepting the old file would mean
// the compilation flag and the sort tags were unreachable for an existing
// library no matter what the user did.
func TestDecodeRejectsOlderSnapshot(t *testing.T) {
	buf := Encode(makeCatalog(5))
	binary.LittleEndian.PutUint16(buf[8:], snapshotVersion-1)

	if _, err := Decode(buf); !errors.Is(err, ErrBadSnapshot) {
		t.Fatalf("Decode of an older snapshot returned %v, want ErrBadSnapshot", err)
	}
}

// The roots survive a rejected snapshot, because they are the one thing the
// caller cannot reconstruct. Without them the version bump would leave a
// server with an empty catalogue and nothing to scan, so the rescan that fixes
// everything could not be asked for.
func TestLoadRootsReadsARejectedSnapshot(t *testing.T) {
	c := makeCatalog(5)
	c.Roots = []string{"/music", "/volume1/more music"}

	path := filepath.Join(t.TempDir(), "catalog.db")
	buf := Encode(c)
	binary.LittleEndian.PutUint16(buf[8:], snapshotVersion-1)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); !errors.Is(err, ErrBadSnapshot) {
		t.Fatalf("Load returned %v, want ErrBadSnapshot", err)
	}
	got := LoadRoots(path)
	if len(got) != 2 || got[0] != "/music" || got[1] != "/volume1/more music" {
		t.Errorf("LoadRoots = %v, want the two roots", got)
	}
}

// LoadRoots reads a header it does not otherwise validate, so it has to be
// safe on anything.
func TestLoadRootsOnRubbish(t *testing.T) {
	dir := t.TempDir()
	for i, b := range [][]byte{
		nil,
		[]byte("nope"),
		[]byte("TAGMGRDB"),
		append([]byte("TAGMGRDB"), make([]byte, 12)...), // header, no roots
		append(Encode(makeCatalog(3))[:24], 0xff, 0xff), // a truncated root list
	} {
		path := filepath.Join(dir, string(rune('a'+i)))
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		LoadRoots(path) // must not panic; any answer is acceptable
	}
	if got := LoadRoots(filepath.Join(dir, "missing")); got != nil {
		t.Errorf("LoadRoots of a missing file = %v, want nil", got)
	}
}

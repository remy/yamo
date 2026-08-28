package tags

import "testing"

// sortEdit sets all five sort fields to distinguishable values, so a writer
// that puts one in another's frame is caught rather than passing because every
// field happened to hold the same string.
func sortEdit() *Edit {
	return &Edit{
		TitleSort:       str("Hound Dog, A"),
		ArtistSort:      str("Presley, Elvis"),
		AlbumSort:       str("Sun Sessions, The"),
		AlbumArtistSort: str("Various Artists"),
		ComposerSort:    str("Leiber, Jerry"),
	}
}

func checkSorts(t *testing.T, md Metadata) {
	t.Helper()
	for _, c := range []struct{ name, got, want string }{
		{"TitleSort", md.TitleSort, "Hound Dog, A"},
		{"ArtistSort", md.ArtistSort, "Presley, Elvis"},
		{"AlbumSort", md.AlbumSort, "Sun Sessions, The"},
		{"AlbumArtistSort", md.AlbumArtistSort, "Various Artists"},
		{"ComposerSort", md.ComposerSort, "Leiber, Jerry"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestSortRoundTrip covers the five sort fields through every container that
// can be written. Each format keeps them somewhere different — ID3 in TSOT and
// friends, MP4 in the four-letter atoms with no © prefix, Vorbis in
// TITLESORT and friends — so writing all five at once and reading them back
// is what proves none of them landed in a neighbour's slot.
func TestSortRoundTrip(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"song.mp3", []string{"-c:a", "libmp3lame"}},
		{"song.m4a", []string{"-c:a", "aac"}},
		{"song.flac", nil},
		{"song.ogg", []string{"-c:a", "libvorbis"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := genFile(t, dir, tc.name, tc.args...)
			r := NewReader()

			if err := Write(path, sortEdit()); err != nil {
				t.Fatalf("write: %v", err)
			}
			md, err := r.ReadFile(path)
			if err != nil && err != ErrNoTags {
				t.Fatalf("read: %v", err)
			}
			checkSorts(t, md)

			// The display fields are a separate set of tags and must be
			// untouched: a sort name is not a rename.
			if md.Title != "Original Title" || md.Artist != "Elvis Presley" {
				t.Errorf("writing sort fields changed the display fields: %+v", md)
			}
			decodes(t, path)

			// Every one of them must be removable, which for a text tag means
			// an empty string rather than a nil pointer.
			if err := Write(path, &Edit{ArtistSort: str("")}); err != nil {
				t.Fatalf("clear: %v", err)
			}
			md, _ = r.ReadFile(path)
			if md.ArtistSort != "" {
				t.Errorf("ArtistSort = %q after clearing", md.ArtistSort)
			}
			if md.AlbumArtistSort != "Various Artists" {
				t.Errorf("clearing one sort field disturbed another: %q", md.AlbumArtistSort)
			}
			decodes(t, path)
		})
	}
}

// An MP4 file that has never held a sort atom must gain one. The add path and
// the replace path are different code — a name missing from ilstOrder can be
// replaced but never added — and the file this all started from is exactly
// that case: an m4a with a sort tag and no album artist.
func TestSortAddedToUntaggedMP4(t *testing.T) {
	path := genFile(t, t.TempDir(), "fresh.m4a", "-c:a", "aac")
	if err := Write(path, &Edit{AlbumArtistSort: str("Various Artists")}); err != nil {
		t.Fatal(err)
	}
	md, _ := NewReader().ReadFile(path)
	if md.AlbumArtistSort != "Various Artists" {
		t.Errorf("AlbumArtistSort = %q; the atom was never added", md.AlbumArtistSort)
	}
	decodes(t, path)
}

// A sort field left nil must survive an edit to something else, the same way
// the compilation flag does. Correcting a genre across an album would
// otherwise wipe the sort names off every track in it.
func TestSortUntouchedByOtherEdits(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"keep.mp3", "keep.m4a", "keep.flac"} {
		t.Run(name, func(t *testing.T) {
			path := genFile(t, dir, name)
			if err := Write(path, sortEdit()); err != nil {
				t.Fatal(err)
			}
			if err := Write(path, &Edit{Genre: str("Rockabilly")}); err != nil {
				t.Fatal(err)
			}
			md, _ := NewReader().ReadFile(path)
			checkSorts(t, md)
			if md.Genre != "Rockabilly" {
				t.Errorf("genre = %q", md.Genre)
			}
		})
	}
}

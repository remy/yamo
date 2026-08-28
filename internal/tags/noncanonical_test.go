package tags

import (
	"sort"
	"strings"
	"testing"
)

func nonCanonicalNames(rep *StripReport) string {
	names := make([]string, 0, len(rep.NonCanonical))
	for _, t := range rep.NonCanonical {
		names = append(names, t.Name())
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// TestNonCanonicalID3v22 covers a real file from the owner's library: an
// iTunes-era tag where every frame is under its three-character name and the
// genre is the number 17 rather than the word.
func TestNonCanonicalID3v22(t *testing.T) {
	path := writeV22File(t, t.TempDir())
	rep, err := StripFile(path, NewKeepSet(DefaultKeepTags), false)
	if err != nil {
		t.Fatal(err)
	}
	got := nonCanonicalNames(rep)
	want := "album,artist,artwork,composer,date,genre,title,track"
	if got != want {
		t.Fatalf("non-canonical = %q, want %q", got, want)
	}
}

// A file ffmpeg wrote is already where this library would put things, so the
// same walk must report nothing — otherwise a clean-up would rewrite the whole
// library for no reason.
func TestNonCanonicalCleanFile(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"clean.mp3", "clean.flac", "clean.m4a"} {
		t.Run(name, func(t *testing.T) {
			path := genFile(t, dir, name)
			rep, err := StripFile(path, NewKeepSet(DefaultKeepTags), false)
			if err != nil {
				t.Fatal(err)
			}
			if n := nonCanonicalNames(rep); n != "" {
				t.Fatalf("reported %q as non-canonical in an encoder-written file", n)
			}
		})
	}
}

// The MP4 and Vorbis branches, driven directly: ffmpeg writes ©gen and ARTIST,
// so a file exhibiting the older spellings has to be built by hand, and the
// filtering functions are the smallest thing that can hold one.
func TestNonCanonicalNativeKeys(t *testing.T) {
	keep := NewKeepSet(DefaultKeepTags)

	rep := &StripReport{}
	filterMP4Items([]mp4Item{
		{Name: atomGenreID, Body: []byte("data")}, // a numeric genre
		{Name: atomTitle, Body: []byte("data")},   // already canonical
	}, keep, rep)
	if got := nonCanonicalNames(rep); got != "genre" {
		t.Errorf("mp4: non-canonical = %q, want %q", got, "genre")
	}

	rep = &StripReport{}
	vc := &vorbisComment{fields: []vorbisField{
		{key: "PERFORMER", value: "Elvis Presley"},
		{key: "YEAR", value: "1956"},
		{key: "ALBUM", value: "Sun Sessions"}, // already canonical
	}}
	stripVorbisFields(vc, keep, FormatFLAC, rep)
	if got := nonCanonicalNames(rep); got != "artist,date" {
		t.Errorf("vorbis: non-canonical = %q, want %q", got, "artist,date")
	}
	if len(vc.fields) != 3 {
		t.Errorf("an older spelling was removed rather than kept: %+v", vc.fields)
	}
}

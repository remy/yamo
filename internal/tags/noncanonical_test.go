package tags

import (
	"os"
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

// A file ffmpeg wrote is otherwise already where this library would put
// things, so nothing else may be reported — a clean-up that rewrote every file
// for no reason would be worse than none.
//
// The one exception is the ID3 date: ffmpeg writes a year frame and no TDRL,
// which is why an MP3 and an M4A of the same song disagree about the release
// year. Writing the field adds one.
func TestNonCanonicalCleanFile(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ name, want string }{
		{"clean.mp3", "date"},
		{"clean.flac", ""},
		{"clean.m4a", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := genFile(t, dir, tc.name)
			rep, err := StripFile(path, NewKeepSet(DefaultKeepTags), false)
			if err != nil {
				t.Fatal(err)
			}
			if n := nonCanonicalNames(rep); n != tc.want {
				t.Fatalf("non-canonical = %q, want %q", n, tc.want)
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

// TestDateIsWrittenForBothMeanings pins the frame pair that makes an MP3 and
// an M4A of the same song agree about the year.
//
// ID3 separates when a recording was made from when it was released; MP4 has
// one atom, ©day, and readers take it as both. A library that keeps the two
// apart — Navidrome maps releasedate from tdrl and ©day alike — therefore sees
// no release date at all on an MP3 carrying only a year frame.
func TestDateIsWrittenForBothMeanings(t *testing.T) {
	dir := t.TempDir()
	path := genFile(t, dir, "dated.mp3", "-c:a", "libmp3lame")

	e := &Edit{}
	e.SetInt("year", 1989)
	if err := Write(path, e); err != nil {
		t.Fatal(err)
	}

	ids := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	head := make([]byte, 1<<16)
	n, _ := f.ReadAt(head, 0)
	tag, err := parseID3v2(head[:n])
	if err != nil {
		t.Fatal(err)
	}
	for _, fr := range tag.frames {
		ids[fr.id] = frameText(fr.payload)
	}
	if ids["TDRL"] != "1989" {
		t.Errorf("TDRL = %q, want 1989 — a reader that separates release from recording will see no year", ids["TDRL"])
	}
	if ids["TYER"] != "1989" && ids["TDRC"] != "1989" {
		t.Errorf("no recording-year frame: TYER=%q TDRC=%q", ids["TYER"], ids["TDRC"])
	}

	// And it still reads back as one year, not two.
	md, err := NewReader().ReadFile(path)
	if err != nil || md.Year != 1989 {
		t.Fatalf("read back year=%d err=%v", md.Year, err)
	}
	decodes(t, path)
}

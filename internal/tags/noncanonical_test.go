package tags

import (
	"encoding/binary"
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
	want := "album,artist,artwork,composer,date,genre,soundcheck,title,track"
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

// A date holding more than a year, which is what an iTunes purchase leaves
// behind. It reads as 2011 whatever it says, so nothing but the clean-up can
// see that the file and the field disagree.
func TestNonCanonicalFullDate(t *testing.T) {
	dir := t.TempDir()
	path := genFile(t, dir, "dated.m4a", "-c:a", "aac")

	// ffmpeg writes a bare year, so the shape this is about has to be put
	// there: a purchased file carries the timestamp iTunes sold it with.
	if err := updateMP4(path, func(items []mp4Item, _ *Metadata) []mp4Item {
		for i := range items {
			if items[i].Name == atomDate {
				items[i].Body = mp4TextBody("2011-08-29T08:00:00Z")
			}
		}
		return items
	}); err != nil {
		t.Fatal(err)
	}

	rep, err := StripFile(path, NewKeepSet(DefaultKeepTags), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := nonCanonicalNames(rep); got != "date" {
		t.Fatalf("non-canonical = %q, want %q", got, "date")
	}

	// The year the clean-up will write back is the one the file already reads
	// as, so normalising it changes the form and not the value.
	var r Reader
	md, err := r.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if md.Year != 2011 {
		t.Errorf("year = %d, want 2011", md.Year)
	}
}

// The shapes a date field turns up in. A bare year is the form this library
// writes, and anything else that still holds a year is worth rewriting.
func TestDateBeyondYear(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"2011", false},
		{" 2011 ", false},
		{"0000", false}, // iTunes' empty original year, not a date at all
		{"2908", false}, // an ID3v2.3 TDAT, which is DDMM rather than a year
		{"", false},
		{"2011-08-29", true},
		{"2009-02-23T08:00:00Z", true},
		{"16/08/1977", true},
	} {
		if got := dateBeyondYear(tc.in); got != tc.want {
			t.Errorf("dateBeyondYear(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// The same date rule in the two branches a file cannot easily be built for.
func TestNonCanonicalDateNativeKeys(t *testing.T) {
	keep := NewKeepSet(DefaultKeepTags)

	rep := &StripReport{}
	filterMP4Items([]mp4Item{
		{Name: atomDate, Body: mp4TextBody("2011-08-29")},
		{Name: atomTitle, Body: mp4TextBody("Where Them Girls At")},
	}, keep, rep)
	if got := nonCanonicalNames(rep); got != "date" {
		t.Errorf("mp4: non-canonical = %q, want %q", got, "date")
	}

	// A bare year is what this writes, so it must not be reported: a clean-up
	// that rewrote every file for nothing would be worse than none.
	rep = &StripReport{}
	filterMP4Items([]mp4Item{{Name: atomDate, Body: mp4TextBody("2011")}}, keep, rep)
	if got := nonCanonicalNames(rep); got != "" {
		t.Errorf("mp4: non-canonical = %q for a bare year, want none", got)
	}

	rep = &StripReport{}
	vc := &vorbisComment{fields: []vorbisField{
		{key: "DATE", value: "2011-08-29"},
		{key: "ALBUM", value: "Nothing But The Beat"},
	}}
	stripVorbisFields(vc, keep, FormatFLAC, rep)
	if got := nonCanonicalNames(rep); got != "date" {
		t.Errorf("vorbis: non-canonical = %q, want %q", got, "date")
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

// TestITunesFieldsAreKept covers the whole of Apple's vocabulary reaching the
// same canonical tag, since it arrives by three different routes: a named
// atom, a freeform name, and a freeform namespace.
func TestITunesFieldsAreKept(t *testing.T) {
	freeform := func(mean, name string) []byte {
		atomOf := func(typ, payload string) []byte {
			b := make([]byte, 8, 12+len(payload))
			binary.BigEndian.PutUint32(b[0:4], uint32(12+len(payload)))
			copy(b[4:8], typ)
			b = append(b, 0, 0, 0, 0)
			return append(b, payload...)
		}
		return append(atomOf("mean", mean), atomOf("name", name)...)
	}
	cases := []struct {
		what string
		name string
		body []byte
		want Tag
	}{
		{"a named atom", "stik", nil, TagITunes},
		{"the account that bought it", "apID", nil, TagITunes},
		{"the advisory flag, which is not a star rating", "rtng", nil, TagITunes},
		{"a store identifier", "cnID", nil, TagITunes},
		{"the vendor id, whose name ends in a space", "xid ", nil, TagITunes},
		{"an iTun-prefixed freeform name", "----", freeform("com.apple.iTunes", "iTunMOVI"), TagITunes},
		{"a CDDB item", "----", freeform("com.apple.iTunes", "iTunes_CDDB_1"), TagITunes},
		{"an unknown name in Apple's namespace", "----", freeform("com.apple.iTunes", "Encoding Params"), TagITunes},

		// The two with their own places on the keep list must not be swallowed
		// by the iTun prefix rule, or they could no longer be dropped alone.
		{"gapless data", "----", freeform("com.apple.iTunes", "iTunSMPB"), TagGapless},
		{"volume normalisation", "----", freeform("com.apple.iTunes", "iTunNORM"), TagSoundCheck},

		// Picard writes MusicBrainz tags in Apple's namespace. Resolving by
		// namespace before name would file them all as iTunes fields.
		{"MusicBrainz in Apple's namespace", "----",
			freeform("com.apple.iTunes", "MusicBrainz Album Id"), TagMusicBrainz},
		{"ReplayGain in Apple's namespace", "----",
			freeform("com.apple.iTunes", "replaygain_track_gain"), TagReplayGain},
	}
	keep := NewKeepSet(DefaultKeepTags)
	for _, tc := range cases {
		got := tagForMP4Atom(tc.name, tc.body)
		if got != tc.want {
			t.Errorf("%s: resolved to %q, want %q", tc.what, got.Name(), tc.want.Name())
		}
	}
	for _, t2 := range []Tag{TagITunes, TagGapless, TagSoundCheck} {
		if !keep[t2] {
			t.Errorf("%s is not on the default keep list", t2.Name())
		}
	}
	if keep[TagMusicBrainz] || keep[TagReplayGain] {
		t.Error("keeping Apple's fields must not have kept everything else in that namespace")
	}
}

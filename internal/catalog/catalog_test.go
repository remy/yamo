package catalog

import (
	"fmt"
	"testing"
	"time"

	"github.com/remy/tag-manager/internal/tags"
)

// makeCatalog builds a synthetic library with the shape of a real one: a
// modest number of artists and albums, each repeated across many tracks.
func makeCatalog(n int) *Catalog {
	artists := []string{"Elvis Presley", "Björk", "Miles Davis", "Radiohead", "Beyoncé", "The Clash"}
	albums := []string{"Sun Sessions", "Homogénic", "Kind of Blue", "OK Computer", "Lemonade", "London Calling"}
	genres := []string{"Rock", "Electronic", "Jazz", "Alternative", "Soul", "Punk"}

	c := New()
	c.Tracks = make([]Track, n)
	for i := range c.Tracks {
		c.Tracks[i] = Track{
			Path:       fmt.Sprintf("/music/%s/%s/%02d track.mp3", artists[i%len(artists)], albums[i%len(albums)], i%12+1),
			Title:      fmt.Sprintf("Song Number %d", i),
			Artist:     artists[i%len(artists)],
			Album:      albums[i%len(albums)],
			Genre:      genres[i%len(genres)],
			Year:       int32(1960 + i%60),
			TrackNo:    int32(i%12 + 1),
			DurationMS: int32(120000 + i%180000),
			Size:       int64(4 << 20),
			ModTime:    time.Now().Unix(),
			Format:     tags.FormatMP3,
		}
	}
	c.Roots = []string{"/music"}
	c.ScannedAt = time.Now()
	return c
}

func TestCodecRoundTrip(t *testing.T) {
	orig := makeCatalog(500)
	got, err := Decode(Encode(orig))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Len() != orig.Len() {
		t.Fatalf("track count %d, want %d", got.Len(), orig.Len())
	}
	if len(got.Roots) != 1 || got.Roots[0] != "/music" {
		t.Errorf("roots = %v", got.Roots)
	}
	for i := range orig.Tracks {
		a, b := orig.Tracks[i], got.Tracks[i]
		if a.Path != b.Path || a.Title != b.Title || a.Artist != b.Artist ||
			a.Album != b.Album || a.Genre != b.Genre || a.Year != b.Year ||
			a.TrackNo != b.TrackNo || a.DurationMS != b.DurationMS ||
			a.Size != b.Size || a.ModTime != b.ModTime || a.Format != b.Format {
			t.Fatalf("track %d did not round-trip:\n got %+v\nwant %+v", i, b, a)
		}
	}
}

func TestCodecRejectsGarbage(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("nope"), []byte("TAGMGRDB")} {
		if _, err := Decode(b); err == nil {
			t.Errorf("Decode(%q) succeeded, want an error", b)
		}
	}
	// A valid header followed by truncated content must not panic.
	good := Encode(makeCatalog(20))
	for _, n := range []int{20, 40, 60, len(good) - 1} {
		if n > 0 && n < len(good) {
			if _, err := Decode(good[:n]); err == nil {
				t.Errorf("Decode of %d truncated bytes succeeded, want an error", n)
			}
		}
	}
}

func TestFold(t *testing.T) {
	cases := map[string]string{
		"Elvis":     "elvis",
		"Björk":     "bjork",
		"Beyoncé":   "beyonce",
		"MØ":        "mo",
		"Sigur Rós": "sigur ros",
		"lower":     "lower",
		"Ólafur":    "olafur",
		"Straße":    "strasse",
	}
	for in, want := range cases {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSearch(t *testing.T) {
	c := makeCatalog(600)
	ix := c.Index()

	count := func(q string) int { return len(ix.Search(ParseQuery(q))) }

	if n := count(""); n != 600 {
		t.Errorf("empty query matched %d, want all 600", n)
	}
	if n := count("elvis"); n != 100 {
		t.Errorf("bare term matched %d, want 100", n)
	}
	if count("artist:elvis") != count("elvis") {
		t.Error("field-scoped and bare search disagree for a unique artist")
	}
	// Folding must make the accented artists reachable from plain ASCII.
	if n := count("artist:bjork"); n != 100 {
		t.Errorf("artist:bjork matched %d, want 100", n)
	}
	if n := count("album:homogenic"); n != 100 {
		t.Errorf("album:homogenic matched %d, want 100", n)
	}
	// Negation.
	if n := count("-artist:elvis"); n != 500 {
		t.Errorf("-artist:elvis matched %d, want 500", n)
	}
	// Terms are ANDed.
	if n := count("artist:elvis artist:bjork"); n != 0 {
		t.Errorf("conflicting terms matched %d, want 0", n)
	}
	// Numeric comparisons.
	if count("year:1960") == 0 {
		t.Error("year:1960 matched nothing")
	}
	// The fixture spans 1960 to 2019.
	if count("year:>2019") != 0 {
		t.Error("year:>2019 should match nothing in this fixture")
	}
	if count("year:>2010") == 0 {
		t.Error("year:>2010 should match the 2011-2019 tracks")
	}
	lo, hi := count("year:1960-1969"), count("year:<1970")
	if lo != hi {
		t.Errorf("year:1960-1969 matched %d but year:<1970 matched %d", lo, hi)
	}
	// Quoted phrases keep their spaces.
	if n := count(`album:"kind of blue"`); n != 100 {
		t.Errorf("quoted album matched %d, want 100", n)
	}
	// A bare term must not match against the path, or every track under an
	// artist directory would match that artist's name twice over.
	if n := count("music"); n != 0 {
		t.Errorf("bare term matched %d paths, want 0", n)
	}
	if n := count("path:music"); n != 600 {
		t.Errorf("path:music matched %d, want 600", n)
	}
	// An empty field value finds gaps.
	if n := count("composer:"); n != 600 {
		t.Errorf("composer: matched %d, want all 600 (none have a composer)", n)
	}
}

func TestSearchReflectsEdits(t *testing.T) {
	c := makeCatalog(50)
	ix := c.Index()
	if len(ix.Search(ParseQuery("artist:kraftwerk"))) != 0 {
		t.Fatal("fixture already contains Kraftwerk")
	}
	c.Tracks[0].Artist = "Kraftwerk"
	c.Touch(0)
	if n := len(ix.Search(ParseQuery("artist:kraftwerk"))); n != 1 {
		t.Errorf("after edit, artist:kraftwerk matched %d, want 1", n)
	}
	if n := len(ix.Search(ParseQuery("artist:elvis"))); n != 8 {
		t.Errorf("the edited track still matches its old artist: got %d", n)
	}
}

func TestAutocomplete(t *testing.T) {
	c := makeCatalog(600)
	ix := c.Index()

	got := ix.Values(FieldArtist).Complete("el", 5)
	if len(got) == 0 || got[0].Value != "Elvis Presley" {
		t.Fatalf("Complete(\"el\") = %+v, want Elvis Presley first", got)
	}
	if got[0].Count != 100 {
		t.Errorf("count = %d, want 100", got[0].Count)
	}
	// Folded input must reach the accented value.
	if got := ix.Values(FieldArtist).Complete("bjo", 5); len(got) == 0 || got[0].Value != "Björk" {
		t.Errorf("Complete(\"bjo\") = %+v, want Björk", got)
	}
	// With no prefix, the most-used values come first.
	if got := ix.Values(FieldGenre).Complete("", 3); len(got) != 3 {
		t.Errorf("Complete(\"\") returned %d, want 3", len(got))
	}
	// A substring that is not a prefix should still be found.
	if got := ix.Values(FieldAlbum).Complete("session", 5); len(got) == 0 || got[0].Value != "Sun Sessions" {
		t.Errorf("Complete(\"session\") = %+v, want Sun Sessions", got)
	}
}

func TestParseQueryEdgeCases(t *testing.T) {
	// A colon inside a value must not be mistaken for a field prefix.
	q := ParseQuery("AC:DC")
	if len(q.terms) != 1 || q.terms[0].field != fieldAny || q.terms[0].value != "ac:dc" {
		t.Errorf("AC:DC parsed as %+v", q.terms)
	}
	if !ParseQuery("   ").Empty() {
		t.Error("whitespace-only query should be empty")
	}
	if !ParseQuery("").Empty() {
		t.Error("empty query should be empty")
	}
	// A half-typed field prefix should still search as text.
	if q := ParseQuery("artist:"); len(q.terms) != 1 || q.terms[0].field != FieldArtist {
		t.Errorf("artist: parsed as %+v", q.terms)
	}
}

func BenchmarkIndexBuild(b *testing.B) {
	c := makeCatalog(100000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.index = nil
		c.Index()
	}
}

func BenchmarkSearch(b *testing.B) {
	c := makeCatalog(100000)
	ix := c.Index()
	for _, q := range []string{
		"elvis", "artist:elvis", "hound", "year:>1990 genre:jazz",
		"artist:bjork -genre:rock",
		// The fuzzy forms, which cost far more per track and are the reason
		// they are opt-in. A bare fuzzy term is the worst case: it scores
		// every display field of every track.
		"artist:^elvis", "artist:~presly", "~presly", "~elvis presly",
	} {
		parsed := ParseQuery(q)
		b.Run(q, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				ix.Search(parsed)
			}
		})
	}
}

func BenchmarkEncodeDecode(b *testing.B) {
	c := makeCatalog(100000)
	buf := Encode(c)
	b.Run("encode", func(b *testing.B) {
		b.SetBytes(int64(len(buf)))
		for i := 0; i < b.N; i++ {
			Encode(c)
		}
	})
	b.Run("decode", func(b *testing.B) {
		b.SetBytes(int64(len(buf)))
		for i := 0; i < b.N; i++ {
			if _, err := Decode(buf); err != nil {
				b.Fatal(err)
			}
		}
	})
}

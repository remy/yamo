package tags

import (
	"reflect"
	"strings"
	"testing"
)

// addVorbisFields appends fields to a file's comment, which is how a file
// comes to repeat one: ffmpeg writes each key once, Picard writes a list as
// a field per value.
func addVorbisFields(t *testing.T, path string, kv ...string) {
	t.Helper()
	add := func(vc *vorbisComment) {
		for i := 0; i+1 < len(kv); i += 2 {
			vc.fields = append(vc.fields, vorbisField{key: kv[i], value: kv[i+1]})
		}
	}
	var err error
	if FormatForPath(path) == FormatFLAC {
		err = updateFLAC(path, func(vc *vorbisComment, other []flacBlock) ([]flacBlock, bool) {
			add(vc)
			return other, true
		})
	} else {
		err = updateOgg(path, func(vc *vorbisComment) bool { add(vc); return true })
	}
	if err != nil {
		t.Fatal(err)
	}
}

// vorbisValues lists every value a file holds under key, in file order.
func vorbisValues(t *testing.T, path, key string) []string {
	t.Helper()
	rep, err := StripFile(path, KeepSet{}, false)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rep.Removed {
		if r.Name == key {
			out = append(out, r.Text)
		}
	}
	return out
}

// A song with three genres reads as three genres, and an edit made from that
// reading writes back separate fields — so correcting one genre no longer
// deletes the other two, which is what it did when only the first was read
// and the writer replaced every GENRE with it.
func TestRepeatedVorbisFields(t *testing.T) {
	for _, tc := range []struct{ name, file, codec string }{
		{"flac", "a.flac", "flac"},
		{"ogg", "b.ogg", "libvorbis"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := genTagged(t, t.TempDir(), tc.file, "-c:a", tc.codec) // genre=Rock
			addVorbisFields(t, path, "GENRE", "Southern Hip Hop", "GENRE", "Trap",
				"ARTIST", "Kevin Gates", "TITLE", "A second title")

			md, err := NewReader().ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if md.Genre != "Rock; Southern Hip Hop; Trap" {
				t.Errorf("genre = %q, want all three joined", md.Genre)
			}
			if md.Artist != "Elvis Presley; Kevin Gates" {
				t.Errorf("artist = %q, want both joined", md.Artist)
			}
			if md.Title != "Hound Dog; A second title" {
				t.Errorf("title = %q, want both joined", md.Title)
			}

			// Dropping one genre and adding another keeps them separate.
			e := &Edit{}
			e.SetString("genre", "Rock;Trap ; Crunk")
			e.SetString("title", "Hound Dog; Live")
			if err := Write(path, e); err != nil {
				t.Fatal(err)
			}
			decodes(t, path)
			if got := vorbisValues(t, path, "GENRE"); !reflect.DeepEqual(got, []string{"Rock", "Trap", "Crunk"}) {
				t.Errorf("GENRE fields = %q, want one per genre", got)
			}
			// A title is one thing, semicolon or not.
			if got := vorbisValues(t, path, "TITLE"); !reflect.DeepEqual(got, []string{"Hound Dog; Live"}) {
				t.Errorf("TITLE fields = %q, want the one string", got)
			}
			// Untouched lists stay lists.
			if got := vorbisValues(t, path, "ARTIST"); len(got) != 2 {
				t.Errorf("ARTIST fields = %q; an edit to other fields disturbed them", got)
			}

			// A single value is still a single field.
			e = &Edit{}
			e.SetString("genre", "Rock")
			if err := Write(path, e); err != nil {
				t.Fatal(err)
			}
			if got := vorbisValues(t, path, "GENRE"); !reflect.DeepEqual(got, []string{"Rock"}) {
				t.Errorf("GENRE fields = %q, want just Rock", got)
			}
		})
	}
}

func TestSplitMultiValue(t *testing.T) {
	for in, want := range map[string][]string{
		"Hip Hop; Trap":  {"Hip Hop", "Trap"},
		"Hip Hop;Trap; ": {"Hip Hop", "Trap"},
		"  Rock  ":       {"Rock"},
		"":               nil,
		" ; ":            nil,
		"A; B; C":        {"A", "B", "C"},
	} {
		if got := splitMultiValue(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitMultiValue(%q) = %q, want %q", in, got, want)
		}
	}
	if j := strings.Join(splitMultiValue("Hip Hop;Trap"), multiValueSep); j != "Hip Hop; Trap" {
		t.Errorf("round trip = %q", j)
	}
}

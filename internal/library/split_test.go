package library

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/remy/yamo/internal/tags"
)

// The shapes a compilation's titles turn up in, and what the template makes of
// them. The Jay-Z case is the one that decides the design: the artist has a
// dash in it, so the separator has to be matched in full rather than treated as
// "a dash somewhere".
func TestSplitTemplates(t *testing.T) {
	for _, tc := range []struct {
		template, title string
		want            map[string]string
	}{
		{"$artist - $title", "Michael Jackson - Beat It",
			map[string]string{"artist": "Michael Jackson", "title": "Beat It"}},
		{"$artist - $title", "Jay-Z - 99 Problems",
			map[string]string{"artist": "Jay-Z", "title": "99 Problems"}},
		// The first separator wins and the rest of the line is the title, which
		// is what "Artist - Song - Remix" needs.
		{"$artist - $title", "Faithless - Insomnia - Monster Mix",
			map[string]string{"artist": "Faithless", "title": "Insomnia - Monster Mix"}},
		{"$artist - $title", "   Blondie   -   Atomic   ",
			map[string]string{"artist": "Blondie", "title": "Atomic"}},
		{"$title ($artist)", "Beat It (Michael Jackson)",
			map[string]string{"title": "Beat It", "artist": "Michael Jackson"}},
		{"$artist / $title", "Pixies / Debaser",
			map[string]string{"artist": "Pixies", "title": "Debaser"}},
		// A dollar in the title itself, which is why $$ exists.
		{"$$$artist - $title", "$Money Man - Cash",
			map[string]string{"artist": "Money Man", "title": "Cash"}},

		// No separator: the template does not describe this title, so it is
		// left alone rather than guessed at.
		{"$artist - $title", "Atomic", nil},
		// A separator with nothing before it captures nothing to write.
		{"$artist - $title", "- Atomic", nil},
	} {
		rule, err := compileSplit(tc.template)
		if err != nil {
			t.Fatalf("compiling %q: %v", tc.template, err)
		}
		got := rule.apply(tc.title)
		if len(got) != len(tc.want) {
			t.Errorf("%q on %q = %v, want %v", tc.template, tc.title, got, tc.want)
			continue
		}
		for k, v := range tc.want {
			if got[k] != v {
				t.Errorf("%q on %q: %s = %q, want %q", tc.template, tc.title, k, got[k], v)
			}
		}
	}
}

// A template that cannot work says why, and says it before anything is written.
func TestSplitTemplatesRejected(t *testing.T) {
	for _, tmpl := range []string{
		"",
		"   ",
		"artist - title",   // no fields named
		"$artist - $",      // a dollar with nothing after it
		"$nosuch - $title", // not a field
		"$path - $title",   // a field, but not one that can be written
		"$artist - $artist",
		"$title", // would copy the title onto itself
	} {
		if _, err := compileSplit(tmpl); !errors.Is(err, ErrBadTemplate) {
			t.Errorf("compileSplit(%q) error = %v, want ErrBadTemplate", tmpl, err)
		}
	}
}

// The whole operation over real files: a Various Artists album whose titles
// carry the performer, split into the fields they belong in.
func TestSplitWritesTheFields(t *testing.T) {
	ff := ffmpegOrSkip(t)
	root := t.TempDir()
	music := filepath.Join(root, "music")
	if err := os.MkdirAll(music, 0o755); err != nil {
		t.Fatal(err)
	}

	titles := []string{
		"Michael Jackson - Beat It",
		"Jay-Z - 99 Problems",
		"Atomic", // no separator: the template does not fit this one
	}
	paths := make([]string, len(titles))
	for i, title := range titles {
		paths[i] = filepath.Join(music, filepath.Base(itoaPad(i+1))+" track.mp3")
		cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:a", "libmp3lame",
			"-metadata", "title="+title,
			"-metadata", "artist=Various Artists",
			"-metadata", "album=Now That's What I Call Music",
			paths[i])
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v\n%s", err, b)
		}
	}

	s, err := Open(Options{CatalogPath: filepath.Join(root, "catalog.db"), SaveInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	scan, err := s.Scan(ScanRequest{Roots: []string{music}})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, s, scan.ID)

	split := func(dry bool) SplitResult {
		t.Helper()
		j, err := s.Split(SplitRequest{
			Selector: Selector{All: true}, Template: "$artist - $title", DryRun: dry,
		})
		if err != nil {
			t.Fatal(err)
		}
		return waitJob(t, s, j.ID).Result.(SplitResult)
	}

	dry := split(true)
	if dry.Changed != 2 || dry.Unmatched != 1 {
		t.Fatalf("dry run: changed=%d unmatched=%d, want 2 and 1", dry.Changed, dry.Unmatched)
	}
	// Worked examples, so the template can be checked before it is applied.
	if len(dry.Samples) != 2 {
		t.Fatalf("dry run returned %d samples, want the two that matched", len(dry.Samples))
	}
	r := tags.NewReader()
	if md, _ := r.ReadFile(paths[0]); md.Title != titles[0] {
		t.Fatal("the dry run wrote to the file")
	}

	if got := split(false); got.Changed != 2 || got.Unmatched != 1 {
		t.Fatalf("apply: changed=%d unmatched=%d, want 2 and 1", got.Changed, got.Unmatched)
	}

	for i, want := range []struct{ artist, title string }{
		{"Michael Jackson", "Beat It"},
		{"Jay-Z", "99 Problems"},
		{"Various Artists", "Atomic"}, // untouched, since nothing matched
	} {
		md, err := r.ReadFile(paths[i])
		if err != nil {
			t.Fatal(err)
		}
		if md.Artist != want.artist || md.Title != want.title {
			t.Errorf("%s: artist/title = %q/%q, want %q/%q",
				filepath.Base(paths[i]), md.Artist, md.Title, want.artist, want.title)
		}
		// The rest of the tags are left exactly as they were.
		if md.Album != "Now That's What I Call Music" {
			t.Errorf("%s: album = %q, want it untouched", filepath.Base(paths[i]), md.Album)
		}
	}

	// Running it again finds nothing left to do: the titles no longer carry a
	// separator, so a repeat is a no-op rather than a second bite.
	if again := split(false); again.Changed != 0 || again.Unmatched != 3 {
		t.Errorf("second run: changed=%d unmatched=%d, want 0 and 3", again.Changed, again.Unmatched)
	}
}

// itoaPad numbers the fixtures so they sort the way they were written.
func itoaPad(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

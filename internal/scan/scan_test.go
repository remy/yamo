package scan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/remy/yamo/internal/catalog"
	"github.com/remy/yamo/internal/tags"
)

// buildTree lays out a directory that looks like a real music share, including
// the junk that real ones accumulate.
func buildTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	files := []string{
		"Elvis Presley/Sun Sessions/01 Hound Dog.mp3",
		"Elvis Presley/Sun Sessions/02 Blue Moon.mp3",
		"Elvis Presley/Sun Sessions/cover.jpg",
		"Elvis Presley/Sun Sessions/folder.txt",
		"Björk/Homogénic/01 Jóga.flac",
		"Björk/Homogénic/02 Bachelorette.m4a",
		"Various/Live/track.ogg",
		"Various/Live/track.opus",
		"Various/Live/notes.md",
		// Junk that must be skipped.
		"@eaDir/thumb.mp3",
		"Elvis Presley/@eaDir/SYNOPHOTO_THUMB.mp3",
		".hidden/secret.mp3",
		"Elvis Presley/Sun Sessions/._01 Hound Dog.mp3",
		"lost+found/orphan.mp3",
		"Podcasts/episode.mp3",
	}
	for _, f := range files {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		// Content does not matter here; the scanner catalogues files whose
		// tags it cannot parse, which is the point.
		if err := os.WriteFile(p, []byte("not really audio, but long enough to open"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func scanTree(t *testing.T, opts Options) *catalog.Catalog {
	t.Helper()
	c, err := Scan(context.Background(), opts, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return c
}

func paths(c *catalog.Catalog, root string) []string {
	out := make([]string, 0, c.Len())
	for i := range c.Tracks {
		rel, _ := filepath.Rel(root, c.Tracks[i].Path)
		out = append(out, rel)
	}
	return out
}

func TestScanFindsAudioAndSkipsJunk(t *testing.T) {
	root := buildTree(t)
	c := scanTree(t, Options{Roots: []string{root}})

	got := map[string]bool{}
	for _, p := range paths(c, root) {
		got[p] = true
	}

	want := []string{
		"Elvis Presley/Sun Sessions/01 Hound Dog.mp3",
		"Elvis Presley/Sun Sessions/02 Blue Moon.mp3",
		"Björk/Homogénic/01 Jóga.flac",
		"Björk/Homogénic/02 Bachelorette.m4a",
		"Various/Live/track.ogg",
		"Various/Live/track.opus",
		"Podcasts/episode.mp3",
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing %q", w)
		}
	}
	notWanted := []string{
		"Elvis Presley/Sun Sessions/cover.jpg",
		"Elvis Presley/Sun Sessions/folder.txt",
		"Various/Live/notes.md",
		"@eaDir/thumb.mp3",
		"Elvis Presley/@eaDir/SYNOPHOTO_THUMB.mp3",
		".hidden/secret.mp3",
		"Elvis Presley/Sun Sessions/._01 Hound Dog.mp3",
		"lost+found/orphan.mp3",
	}
	for _, n := range notWanted {
		if got[n] {
			t.Errorf("should not have catalogued %q", n)
		}
	}
	if c.Len() != len(want) {
		t.Errorf("catalogued %d files, want %d: %v", c.Len(), len(want), paths(c, root))
	}
}

func TestScanExclude(t *testing.T) {
	root := buildTree(t)
	c := scanTree(t, Options{Roots: []string{root}, Exclude: []string{"Podcasts", "*.opus"}})
	for _, p := range paths(c, root) {
		if p == "Podcasts/episode.mp3" || filepath.Ext(p) == ".opus" {
			t.Errorf("exclusion did not skip %q", p)
		}
	}
	if c.Len() != 5 {
		t.Errorf("catalogued %d, want 5: %v", c.Len(), paths(c, root))
	}
}

func TestScanIncludeHidden(t *testing.T) {
	root := buildTree(t)
	c := scanTree(t, Options{Roots: []string{root}, IncludeHidden: true})
	found := false
	for _, p := range paths(c, root) {
		if p == ".hidden/secret.mp3" {
			found = true
		}
	}
	if !found {
		t.Error("-hidden did not pick up the dot-directory")
	}
}

// TestIncrementalReuse checks that a rescan reuses unchanged entries and
// re-reads the one file that moved.
func TestIncrementalReuse(t *testing.T) {
	root := buildTree(t)
	first := scanTree(t, Options{Roots: []string{root}})

	// Change one file so its size differs.
	changed := filepath.Join(root, "Podcasts/episode.mp3")
	if err := os.WriteFile(changed, []byte("different content entirely, longer than before"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stats Stats
	second, err := Scan(context.Background(), Options{Roots: []string{root}, Previous: first},
		func(s Stats) {
			if s.Finished {
				stats = s
			}
		})
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if second.Len() != first.Len() {
		t.Errorf("rescan found %d tracks, want %d", second.Len(), first.Len())
	}
	if stats.Reused != int64(first.Len()-1) {
		t.Errorf("reused %d entries, want %d", stats.Reused, first.Len()-1)
	}
	if stats.Parsed != 1 {
		t.Errorf("re-read %d files, want 1", stats.Parsed)
	}
}

// TestScanDeletedFiles covers pruning, which is how a rescan notices removals.
func TestScanDeletedFiles(t *testing.T) {
	root := buildTree(t)
	first := scanTree(t, Options{Roots: []string{root}})
	if err := os.Remove(filepath.Join(root, "Podcasts/episode.mp3")); err != nil {
		t.Fatal(err)
	}
	second := scanTree(t, Options{Roots: []string{root}, Previous: first})
	if second.Len() != first.Len()-1 {
		t.Errorf("after deleting one file the catalogue has %d tracks, want %d",
			second.Len(), first.Len()-1)
	}
}

// TestSymlinkLoop is the test that matters for a NAS: a directory that links
// back to an ancestor must not make the scan run forever.
func TestSymlinkLoop(t *testing.T) {
	root := buildTree(t)
	loop := filepath.Join(root, "Elvis Presley", "loop")
	if err := os.Symlink(root, loop); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		c, _ := Scan(ctx, Options{Roots: []string{root}, FollowSymlinks: true}, nil)
		done <- c.Len()
	}()

	select {
	case n := <-done:
		if n == 0 {
			t.Error("following symlinks found nothing")
		}
	case <-timeAfter(t):
		cancel()
		t.Fatal("scan did not terminate with a symlink loop present")
	}
}

// TestScanReadsRealTags runs the scanner over genuine encoder output.
func TestScanReadsRealTags(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "Elvis Presley", "Sun Sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "01 Hound Dog.mp3")
	cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:a", "libmp3lame",
		"-metadata", "artist=Elvis Presley", "-metadata", "album=Sun Sessions",
		"-metadata", "title=Hound Dog", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, b)
	}

	c := scanTree(t, Options{Roots: []string{root}})
	if c.Len() != 1 {
		t.Fatalf("catalogued %d tracks, want 1", c.Len())
	}
	got := c.Tracks[0]
	if got.Artist != "Elvis Presley" || got.Album != "Sun Sessions" || got.Title != "Hound Dog" {
		t.Errorf("tags = %+v", got)
	}
	if got.Format != tags.FormatMP3 {
		t.Errorf("format = %v, want mp3", got.Format)
	}
	if got.Size == 0 || got.ModTime == 0 {
		t.Error("size and modification time should be recorded")
	}
}

// timeAfter gives the symlink test a generous but finite deadline.
func timeAfter(t *testing.T) <-chan time.Time {
	t.Helper()
	return time.After(20 * time.Second)
}

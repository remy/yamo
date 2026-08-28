package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/remy/tag-manager/internal/artclip"
	"github.com/remy/tag-manager/internal/catalog"
	"github.com/remy/tag-manager/internal/tags"
	"github.com/remy/tag-manager/internal/ui"
)

const artSummary = `tagmgr art - inspect, copy and replace cover art

Usage:
  tagmgr art [flags] [query]

Cover art is moved around with a clipboard: copy one image, then paste it
onto however many tracks you like. The clipboard persists between runs, so
a cover copied here can be pasted in the browser and the other way round.

  tagmgr art                            what art the matching tracks have
  tagmgr art -copy TRACK_OR_IMAGE       put an image on the clipboard
  tagmgr art -paste QUERY -apply        write it to the matching tracks
  tagmgr art -export DIR QUERY          write covers out as files
  tagmgr art -remove QUERY -apply       take the art off

-copy accepts either an image file or an audio file to lift the cover from.
-paste and -remove are dry runs unless -apply is given.

Folder art:
  tagmgr art -from-folder QUERY -apply

  Looks beside each track for cover.jpg, folder.jpg, front.jpg or the like
  and embeds it. This is the usual way a downloaded library stores art, and
  the usual reason none of it shows up on a phone.

Note that embedding art rewrites the file: a cover is far larger than the
padding any format reserves, so unlike other edits the audio has to move.
Tracks whose art already matches are skipped.
`

// coverNames are the filenames art conventionally goes by, in preference order.
var coverNames = []string{
	"cover", "folder", "front", "album", "albumart", "artwork", "thumb",
}

var coverExts = []string{".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp"}

func cmdArt(args []string) error {
	fs := flag.NewFlagSet("art", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	copyFrom := fs.String("copy", "", "copy an image, or a track's cover, to the clipboard")
	paste := fs.Bool("paste", false, "write the clipboard image to the matching tracks")
	remove := fs.Bool("remove", false, "remove art from the matching tracks")
	export := fs.String("export", "", "write each distinct cover into this directory")
	fromFolder := fs.Bool("from-folder", false, "embed cover.jpg or folder.jpg found beside each track")
	clear := fs.Bool("clear", false, "empty the clipboard")
	apply := fs.Bool("apply", false, "actually write the files (default is a dry run)")
	workers := fs.Int("workers", 0, "concurrency (0 = auto)")
	if err := parseFlags(fs, args, artSummary, queryHelp); err != nil {
		return err
	}

	clip := artclip.New(artclip.DefaultDir(*catalogPath))
	if *clear {
		if err := clip.Clear(); err != nil {
			return err
		}
		fmt.Println("clipboard emptied")
		return nil
	}
	if *copyFrom != "" {
		return artCopy(clip, *copyFrom)
	}

	c, err := catalog.Load(*catalogPath)
	if err != nil {
		return fmt.Errorf("loading catalogue: %w (run: tagmgr scan <dir>)", err)
	}
	targets, skipped := selectWritable(c, strings.Join(fs.Args(), " "))
	if len(targets) == 0 && *export == "" {
		return errors.New("no writable files matched")
	}
	if *workers <= 0 {
		*workers = min(runtime.NumCPU(), 8)
	}

	switch {
	case *export != "":
		return artExport(c, targets, *export)
	case *remove:
		return artApply(c, targets, *workers, *apply, skipped, "remove art from", "removed art from",
			func(string) (*tags.Picture, error) { return nil, nil })
	case *paste:
		held, err := clip.Paste()
		if err != nil {
			if errors.Is(err, artclip.ErrEmpty) {
				return errors.New("the clipboard is empty (try: tagmgr art -copy FILE)")
			}
			return err
		}
		fmt.Fprintf(os.Stderr, "clipboard: %s from %s\n", held.Picture.Summary(), held.Source)
		return artApply(c, targets, *workers, *apply, skipped, "set art on", "set art on",
			func(string) (*tags.Picture, error) { return &held.Picture, nil })
	case *fromFolder:
		cache := newFolderCache()
		return artApply(c, targets, *workers, *apply, skipped, "set art on", "set art on", cache.find)
	default:
		return artReport(c, targets, skipped)
	}
}

// artCopy puts an image on the clipboard, from either an image file or the
// cover embedded in an audio file.
func artCopy(clip *artclip.Store, src string) error {
	if tags.IsAudioPath(src) {
		p, err := tags.ReadCover(src)
		if err != nil {
			return fmt.Errorf("%s: %w", src, err)
		}
		if err := clip.Copy(p, src); err != nil {
			return err
		}
		fmt.Printf("copied %s from %s\n", p.Summary(), filepath.Base(src))
		return nil
	}
	if err := clip.CopyFile(src); err != nil {
		return err
	}
	held, err := clip.Paste()
	if err != nil {
		return err
	}
	fmt.Printf("copied %s from %s\n", held.Picture.Summary(), filepath.Base(src))
	return nil
}

// artReport summarises the art across the matching tracks, grouping identical
// images so the duplication is visible.
func artReport(c *catalog.Catalog, targets []int32, skipped map[string]int) error {
	type group struct {
		summary string
		files   int
		bytes   int
		example string
	}
	groups := map[string]*group{}
	var without int

	for _, i := range targets {
		t := &c.Tracks[i]
		if !t.HasArt {
			without++
			continue
		}
		p, err := tags.ReadCover(t.Path)
		if err != nil {
			without++
			continue
		}
		key := fmt.Sprintf("%x", hashBytes(p.Data))
		g := groups[key]
		if g == nil {
			g = &group{summary: p.Summary(), example: filepath.Base(filepath.Dir(t.Path))}
			groups[key] = g
		}
		g.files++
		g.bytes += len(p.Data)
	}

	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return groups[keys[i]].files > groups[keys[j]].files })

	fmt.Printf("%8s  %-28s  %10s  %s\n", "tracks", "image", "embedded", "example album")
	total, unique := 0, 0
	for _, k := range keys {
		g := groups[k]
		total += g.bytes
		unique += g.bytes / g.files
		fmt.Printf("%8s  %-28s  %10s  %s\n", ui.FormatCount(g.files), g.summary,
			ui.FormatBytes(int64(g.bytes)), ui.Truncate(g.example, 40))
	}
	fmt.Printf("\n%s distinct images across %s tracks; %s embedded, %s of it unique\n",
		ui.FormatCount(len(groups)), ui.FormatCount(len(targets)-without),
		ui.FormatBytes(int64(total)), ui.FormatBytes(int64(unique)))
	if without > 0 {
		fmt.Printf("%s tracks have no art\n", ui.FormatCount(without))
	}
	reportSkipped(skipped)
	return nil
}

// artExport writes each distinct cover out once, named after its album.
func artExport(c *catalog.Catalog, targets []int32, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	seen := map[string]bool{}
	written := 0
	for _, i := range targets {
		t := &c.Tracks[i]
		if !t.HasArt {
			continue
		}
		p, err := tags.ReadCover(t.Path)
		if err != nil {
			continue
		}
		key := fmt.Sprintf("%x", hashBytes(p.Data))
		if seen[key] {
			continue
		}
		seen[key] = true

		name := t.Album
		if name == "" {
			name = filepath.Base(filepath.Dir(t.Path))
		}
		out := filepath.Join(dir, safeFilename(name)+p.Ext())
		if err := os.WriteFile(out, p.Data, 0o644); err != nil {
			return err
		}
		written++
	}
	fmt.Printf("exported %s images to %s\n", ui.FormatCount(written), dir)
	return nil
}

// pictureFor supplies the image a track should end up with, or nil to remove
// whatever it has.
type pictureFor func(path string) (*tags.Picture, error)

// artApply writes art across the matching tracks.
func artApply(c *catalog.Catalog, targets []int32, workers int, apply bool,
	skipped map[string]int, future, past string, pick pictureFor) error {

	var (
		mu                        sync.Mutex
		changed, same, none, fail int
		bytes                     int64
		firstErr                  error
	)
	start := time.Now()
	jobs := make(chan int32)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				t := &c.Tracks[idx]
				n, err := applyArtTo(t, pick, apply)
				mu.Lock()
				switch {
				case err != nil:
					fail++
					if firstErr == nil {
						firstErr = err
					}
				case n < 0:
					none++
				case n == 0:
					same++
				default:
					changed++
					bytes += int64(n)
				}
				mu.Unlock()
			}
		}()
	}
	for _, idx := range targets {
		jobs <- idx
	}
	close(jobs)
	wg.Wait()

	word := "would " + future
	if apply {
		word = past
	}
	fmt.Printf("%s %s tracks (%s of image data)\n", word, ui.FormatCount(changed), ui.FormatBytes(bytes))
	if same > 0 {
		fmt.Printf("%s tracks already had that image and were skipped\n", ui.FormatCount(same))
	}
	if none > 0 {
		fmt.Printf("%s tracks had no image to use\n", ui.FormatCount(none))
	}
	if apply {
		fmt.Printf("took %s\n", ui.FormatDuration(time.Since(start)))
	}
	reportSkipped(skipped)
	if !apply && changed > 0 {
		fmt.Println("\nthis was a dry run; add -apply to write")
	}
	if firstErr != nil {
		return fmt.Errorf("%d files failed; first: %w", fail, firstErr)
	}
	return nil
}

// applyArtTo returns the number of image bytes written, 0 when the track
// already carried that image, and -1 when there was no image to apply.
func applyArtTo(t *catalog.Track, pick pictureFor, apply bool) (int, error) {
	want, err := pick(t.Path)
	if err != nil {
		return -1, nil // nothing found for this track; not a failure
	}

	if want == nil { // removal
		if !t.HasArt {
			return 0, nil
		}
		if !apply {
			return 1, nil
		}
		e := &tags.Edit{}
		e.RemoveArtwork()
		return 1, tags.Write(t.Path, e)
	}

	// Re-embedding the same bytes would rewrite the file for nothing, which
	// on a library-wide run is the difference between minutes and hours.
	if t.HasArt {
		if cur, err := tags.ReadCover(t.Path); err == nil && bytesEqual(cur.Data, want.Data) {
			return 0, nil
		}
	}
	if !apply {
		return len(want.Data), nil
	}
	e := &tags.Edit{}
	e.SetArtwork([]tags.Picture{*want})
	if err := tags.Write(t.Path, e); err != nil {
		return 0, err
	}
	return len(want.Data), nil
}

// folderCache finds the cover image sitting beside a track, remembering the
// answer per directory so an album costs one scan rather than one per track.
type folderCache struct {
	mu   sync.Mutex
	seen map[string]*tags.Picture
}

func newFolderCache() *folderCache {
	return &folderCache{seen: map[string]*tags.Picture{}}
}

func (fc *folderCache) find(trackPath string) (*tags.Picture, error) {
	dir := filepath.Dir(trackPath)
	fc.mu.Lock()
	if p, ok := fc.seen[dir]; ok {
		fc.mu.Unlock()
		if p == nil {
			return nil, errors.New("no folder art")
		}
		return p, nil
	}
	fc.mu.Unlock()

	p := findFolderArt(dir)
	fc.mu.Lock()
	fc.seen[dir] = p
	fc.mu.Unlock()
	if p == nil {
		return nil, errors.New("no folder art")
	}
	return p, nil
}

// findFolderArt looks for a conventionally named image in a directory.
func findFolderArt(dir string) *tags.Picture {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	byName := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		ext := filepath.Ext(name)
		if !contains(coverExts, ext) {
			continue
		}
		byName[strings.TrimSuffix(name, ext)] = filepath.Join(dir, e.Name())
	}
	for _, want := range coverNames {
		if path, ok := byName[want]; ok {
			if data, err := os.ReadFile(path); err == nil {
				if p, err := tags.NewPicture(data); err == nil {
					return p
				}
			}
		}
	}
	return nil
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func reportSkipped(skipped map[string]int) {
	if len(skipped) == 0 {
		return
	}
	parts := make([]string, 0, len(skipped))
	for f, n := range skipped {
		parts = append(parts, fmt.Sprintf("%d %s", n, f))
	}
	sort.Strings(parts)
	fmt.Printf("skipped (this build cannot write them): %s\n", strings.Join(parts, ", "))
}

// safeFilename makes an album name usable as a filename.
func safeFilename(s string) string {
	out := []rune(s)
	for i, r := range out {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', 0:
			out[i] = '-'
		}
	}
	return strings.TrimSpace(string(out))
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// hashBytes identifies an image so identical covers group together. It is a
// non-cryptographic hash: the only requirement is that different images do not
// collide in a single library.
func hashBytes(b []byte) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for _, c := range b {
		h ^= uint64(c)
		h *= prime64
	}
	return h
}

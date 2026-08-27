package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/remy/tag-manager/internal/catalog"
	"github.com/remy/tag-manager/internal/tags"
	"github.com/remy/tag-manager/internal/ui"
)

const stripSummary = `tagmgr strip - remove every tag except a fixed set

Usage:
  tagmgr strip [flags] [query]

Removes every ID3v2 frame that is not on the keep list, leaving a uniform
tag across the library. With a query, only matching tracks are touched;
with none, the whole catalogue is.

This is a dry run unless -apply is given. The dry run reports exactly what
would go, grouped by frame, so the damage can be read before it is done.

ID3v2.2 tags are rewritten as ID3v2.3. The frames are translated first, so
a v2.2 "TP1" is recognised as an artist and kept.

Only MP3 files carry ID3v2 tags; other formats in the catalogue are counted
and skipped.

Examples:
  tagmgr strip                          what would be removed from everything
  tagmgr strip artist:elvis             ...from one artist
  tagmgr strip -list                    print the keep list and exit
  tagmgr strip -backup ~/strip.jsonl -apply
  tagmgr restore -backup ~/strip.jsonl -apply
`

func cmdStrip(args []string) error {
	fs := flag.NewFlagSet("strip", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	apply := fs.Bool("apply", false, "actually write the files (default is a dry run)")
	list := fs.Bool("list", false, "print the keep list and exit")
	keepFlag := fs.String("keep", "", "replace the keep list with this comma-separated set")
	alsoFlag := fs.String("also", "", "add these frames to the keep list, comma-separated")
	backup := fs.String("backup", "", "append removed frames to this file so restore can undo them")
	workers := fs.Int("workers", 0, "concurrency (0 = auto)")
	if err := parseFlags(fs, args, stripSummary, queryHelp); err != nil {
		return err
	}

	keep := tags.NewKeepSet(tags.DefaultKeepFrames)
	if *keepFlag != "" {
		keep = tags.NewKeepSet(strings.Split(*keepFlag, ","))
	}
	for _, id := range strings.Split(*alsoFlag, ",") {
		if id = strings.ToUpper(strings.TrimSpace(id)); id != "" {
			keep[id] = true
		}
	}
	if len(keep) == 0 {
		return fmt.Errorf("the keep list is empty; that would remove every tag")
	}
	if *list {
		printKeepList(keep)
		return nil
	}

	c, err := catalog.Load(*catalogPath)
	if err != nil {
		return fmt.Errorf("loading catalogue: %w (run: tagmgr scan <dir>)", err)
	}
	targets, skipped := selectMP3s(c, strings.Join(fs.Args(), " "))
	if len(targets) == 0 {
		return fmt.Errorf("no MP3 files matched")
	}

	var backupW *backupWriter
	if *backup != "" {
		if backupW, err = newBackupWriter(*backup); err != nil {
			return err
		}
		defer backupW.Close()
	}
	if *apply && backupW == nil {
		fmt.Fprint(os.Stderr,
			"warning: no -backup given, so this cannot be undone\n\n")
	}

	return runStrip(c, targets, skipped, keep, *apply, *workers, backupW)
}

// selectMP3s resolves a query to the MP3 tracks it matches, and counts the
// matches in other formats so they can be reported rather than silently
// ignored.
func selectMP3s(c *catalog.Catalog, query string) (targets []int32, skipped map[string]int) {
	skipped = map[string]int{}
	for _, i := range c.Index().Search(catalog.ParseQuery(query)) {
		if c.Tracks[i].Format == tags.FormatMP3 {
			targets = append(targets, i)
		} else {
			skipped[c.Tracks[i].Format.String()]++
		}
	}
	return targets, skipped
}

func printKeepList(keep tags.KeepSet) {
	fmt.Println("frames kept by a strip:")
	for _, id := range keep.Sorted() {
		fmt.Printf("  %-6s %s\n", id, tags.FrameMeaning(id))
	}
	fmt.Println("\neverything else is removed.")
}

// stripTally accumulates results across workers.
type stripTally struct {
	mu       sync.Mutex
	files    int
	changed  int
	noTag    int
	upgraded int
	bytes    int
	errs     []error
	byFrame  map[string]*frameTally
}

type frameTally struct {
	files   int
	bytes   int
	samples []string
}

func runStrip(c *catalog.Catalog, targets []int32, skipped map[string]int,
	keep tags.KeepSet, apply bool, workers int, backup *backupWriter) error {

	if workers <= 0 {
		workers = min(runtime.NumCPU(), 8)
	}
	t := &stripTally{byFrame: map[string]*frameTally{}}

	verb := "would remove"
	if apply {
		verb = "removed"
	}
	fmt.Fprintf(os.Stderr, "%s %s MP3s…\n", map[bool]string{true: "stripping", false: "examining"}[apply],
		ui.FormatCount(len(targets)))

	start := time.Now()
	jobs := make(chan int32)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				rep, err := tags.StripID3v2(c.Tracks[idx].Path, keep, apply)
				t.record(rep, err, backup)
			}
		}()
	}
	for _, idx := range targets {
		jobs <- idx
	}
	close(jobs)
	wg.Wait()

	t.print(verb, apply, skipped, time.Since(start))
	if len(t.errs) > 0 {
		return fmt.Errorf("%d files could not be processed; first: %w", len(t.errs), t.errs[0])
	}
	return nil
}

func (t *stripTally) record(rep *tags.StripReport, err error, backup *backupWriter) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		if len(t.errs) < 20 {
			t.errs = append(t.errs, err)
		}
		return
	}
	t.files++
	if rep.NoTag {
		t.noTag++
		return
	}
	if rep.Upgraded {
		t.upgraded++
	}
	if !rep.Changed {
		return
	}
	t.changed++
	t.bytes += rep.BytesRemoved()
	for _, f := range rep.Removed {
		ft := t.byFrame[f.ID]
		if ft == nil {
			ft = &frameTally{}
			t.byFrame[f.ID] = ft
		}
		ft.files++
		ft.bytes += f.Size()
		if len(ft.samples) < 3 {
			if s := tags.DescribeFrame(f.ID, f.Payload); s != "" {
				ft.samples = append(ft.samples, s)
			}
		}
	}
	if backup != nil {
		backup.write(rep)
	}
}

func (t *stripTally) print(verb string, apply bool, skipped map[string]int, elapsed time.Duration) {
	ids := make([]string, 0, len(t.byFrame))
	for id := range t.byFrame {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return t.byFrame[ids[i]].files > t.byFrame[ids[j]].files })

	fmt.Printf("\n%-6s %8s  %10s  %s\n", "frame", "files", "bytes", "meaning")
	for _, id := range ids {
		ft := t.byFrame[id]
		line := fmt.Sprintf("%-6s %8s  %10s  %s", id, ui.FormatCount(ft.files),
			ui.FormatBytes(int64(ft.bytes)), tags.FrameMeaning(id))
		if len(ft.samples) > 0 {
			line += "  ·  " + ui.Truncate(strings.Join(ft.samples, " / "), 46)
		}
		fmt.Println(line)
	}

	fmt.Printf("\n%s %s across %s of %s files (%s)\n", verb,
		ui.FormatBytes(int64(t.bytes)), ui.FormatCount(t.changed),
		ui.FormatCount(t.files), ui.FormatDuration(elapsed))
	if t.upgraded > 0 {
		fmt.Printf("%s files rewritten from ID3v2.2 to ID3v2.3\n", ui.FormatCount(t.upgraded))
	}
	if t.noTag > 0 {
		fmt.Printf("%s files had no ID3v2 tag\n", ui.FormatCount(t.noTag))
	}
	if len(skipped) > 0 {
		parts := make([]string, 0, len(skipped))
		for f, n := range skipped {
			parts = append(parts, fmt.Sprintf("%d %s", n, f))
		}
		sort.Strings(parts)
		fmt.Printf("skipped (not MP3): %s\n", strings.Join(parts, ", "))
	}
	if !apply && t.changed > 0 {
		fmt.Println("\nthis was a dry run; add -apply to write, and -backup FILE to keep an undo")
	}
}

// backupRecord is one file's removed frames, as written to the backup log.
type backupRecord struct {
	Path   string              `json:"path"`
	Frames []tags.RemovedFrame `json:"frames"`
}

// backupWriter appends one JSON object per stripped file. The format is a
// line per record so a partial run still leaves a usable log.
type backupWriter struct {
	mu sync.Mutex
	f  *os.File
	w  *bufio.Writer
}

func newBackupWriter(path string) (*backupWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &backupWriter{f: f, w: bufio.NewWriterSize(f, 1<<20)}, nil
}

func (b *backupWriter) write(rep *tags.StripReport) {
	b.mu.Lock()
	defer b.mu.Unlock()
	enc := json.NewEncoder(b.w)
	_ = enc.Encode(backupRecord{Path: rep.Path, Frames: rep.Removed})
}

func (b *backupWriter) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.w.Flush(); err != nil {
		b.f.Close()
		return err
	}
	if err := b.f.Sync(); err != nil {
		b.f.Close()
		return err
	}
	return b.f.Close()
}

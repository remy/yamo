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

Removes every tag that is not on the keep list, leaving a uniform set of
metadata across the library. With a query, only matching tracks are
touched; with none, the whole catalogue is.

The keep list is written in canonical names — title, albumartist, artwork —
not in the identifiers any one format uses, so the same list applies to
MP3, FLAC, MP4, Ogg Vorbis and Opus alike. An album artist is kept whether
the file spells it TPE2, ALBUMARTIST or aART. ID3 frame identifiers are
accepted as aliases.

This is a dry run unless -apply is given. The dry run reports exactly what
would go, grouped by format and key, so the damage can be read beforehand.

ID3v2.2 tags are rewritten as ID3v2.3, but only for files that actually
lose something. The frames are translated first, so a v2.2 "TP1" is
recognised as an artist and kept.

WMA, WAV and AIFF are read but not written, so they are counted and
skipped.

Examples:
  tagmgr strip                          what would be removed from everything
  tagmgr strip artist:elvis             ...from one artist
  tagmgr strip -list                    print the keep list and exit
  tagmgr strip -also gapless,musicbrainz -apply
  tagmgr strip -backup ~/strip.jsonl -apply
  tagmgr restore -backup ~/strip.jsonl -apply
`

func cmdStrip(args []string) error {
	fs := flag.NewFlagSet("strip", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	apply := fs.Bool("apply", false, "actually write the files (default is a dry run)")
	list := fs.Bool("list", false, "print the keep list and exit")
	keepFlag := fs.String("keep", "", "replace the keep list with this comma-separated set")
	alsoFlag := fs.String("also", "", "add these tags to the keep list, comma-separated")
	backup := fs.String("backup", "", "append removed frames to this file so restore can undo them")
	workers := fs.Int("workers", 0, "concurrency (0 = auto)")
	if err := parseFlags(fs, args, stripSummary, queryHelp); err != nil {
		return err
	}

	keep := tags.NewKeepSet(tags.DefaultKeepTags)
	if *keepFlag != "" {
		parsed, unknown := tags.ParseKeepSet(strings.Split(*keepFlag, ","))
		if len(unknown) > 0 {
			return fmt.Errorf("unknown tag %q (try: tagmgr strip -list)", unknown[0])
		}
		keep = parsed
	}
	if *alsoFlag != "" {
		extra, unknown := tags.ParseKeepSet(strings.Split(*alsoFlag, ","))
		if len(unknown) > 0 {
			return fmt.Errorf("unknown tag %q (try: tagmgr strip -list)", unknown[0])
		}
		for t := range extra {
			keep[t] = true
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
	targets, skipped := selectWritable(c, strings.Join(fs.Args(), " "))
	if len(targets) == 0 {
		return fmt.Errorf("no writable files matched")
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

// selectWritable resolves a query to the tracks whose format this build can
// write, counting the rest so they are reported rather than silently ignored.
func selectWritable(c *catalog.Catalog, query string) (targets []int32, skipped map[string]int) {
	skipped = map[string]int{}
	for _, i := range c.Index().Search(catalog.ParseQuery(query)) {
		if c.Tracks[i].Format.Writable() {
			targets = append(targets, i)
		} else {
			skipped[c.Tracks[i].Format.String()]++
		}
	}
	return targets, skipped
}

// printKeepList shows the keep list with the native keys each tag maps to, so
// it is clear what will actually be preserved in each kind of file.
func printKeepList(keep tags.KeepSet) {
	fmt.Printf("%-18s %-20s %-34s %s\n", "tag", "mp3", "flac / ogg / opus", "mp4")
	for _, t := range keep.Sorted() {
		fmt.Printf("%-18s %-20s %-34s %s\n", t.Name(),
			ui.Truncate(nativeKeys(t, tags.FormatMP3), 20),
			ui.Truncate(nativeKeys(t, tags.FormatFLAC), 34),
			nativeKeys(t, tags.FormatMP4))
	}
	fmt.Printf("\n%d tags kept; everything else is removed.\n", len(keep))
	fmt.Println("add tags with -also, or replace the list with -keep. available:")
	printAvailableTags(keep)
}

func nativeKeys(t tags.Tag, f tags.Format) string {
	k := t.NativeKeys(f)
	if len(k) == 0 {
		return "—"
	}
	return strings.Join(k, " ")
}

func printAvailableTags(keep tags.KeepSet) {
	var names []string
	for _, t := range tags.AllTags() {
		if !keep[t] {
			names = append(names, t.Name())
		}
	}
	line := "  "
	for _, n := range names {
		if len(line)+len(n)+2 > 78 {
			fmt.Println(line)
			line = "  "
		}
		line += n + "  "
	}
	if strings.TrimSpace(line) != "" {
		fmt.Println(line)
	}
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
	byFrame  map[key]*frameTally
}

// key identifies one kind of removed metadata for the report.
type key struct {
	format  string
	name    string
	meaning string
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
	t := &stripTally{byFrame: map[key]*frameTally{}}

	verb := "would remove"
	if apply {
		verb = "removed"
	}
	fmt.Fprintf(os.Stderr, "%s %s files…\n", map[bool]string{true: "stripping", false: "examining"}[apply],
		ui.FormatCount(len(targets)))

	start := time.Now()
	jobs := make(chan int32)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				rep, err := tags.StripFile(c.Tracks[idx].Path, keep, apply)
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
	for _, r := range rep.Removed {
		// Group by format and key together: the same idea has a different key
		// in each container, and merging them would hide which files are
		// affected.
		k := key{format: r.Format.String(), name: r.Display(), meaning: r.Meaning}
		ft := t.byFrame[k]
		if ft == nil {
			ft = &frameTally{}
			t.byFrame[k] = ft
		}
		ft.files++
		ft.bytes += r.Bytes
		if len(ft.samples) < 3 && r.Sample != "" {
			ft.samples = append(ft.samples, r.Sample)
		}
	}
	if backup != nil {
		backup.write(rep)
	}
}

func (t *stripTally) print(verb string, apply bool, skipped map[string]int, elapsed time.Duration) {
	keys := make([]key, 0, len(t.byFrame))
	for k := range t.byFrame {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := t.byFrame[keys[i]], t.byFrame[keys[j]]
		if a.files != b.files {
			return a.files > b.files
		}
		return keys[i].name < keys[j].name
	})

	fmt.Printf("\n%-5s %-22s %8s  %10s  %s\n", "fmt", "key", "files", "bytes", "meaning")
	for _, k := range keys {
		ft := t.byFrame[k]
		meaning := k.meaning
		if meaning == "" {
			meaning = "unrecognised"
		}
		line := fmt.Sprintf("%-5s %-22s %8s  %10s  %s", k.format, ui.Truncate(k.name, 22),
			ui.FormatCount(ft.files), ui.FormatBytes(int64(ft.bytes)), meaning)
		if len(ft.samples) > 0 {
			line += "  ·  " + ui.Truncate(strings.Join(ft.samples, " / "), 40)
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
		fmt.Printf("%s files carried no metadata at all\n", ui.FormatCount(t.noTag))
	}
	if len(skipped) > 0 {
		parts := make([]string, 0, len(skipped))
		for f, n := range skipped {
			parts = append(parts, fmt.Sprintf("%d %s", n, f))
		}
		sort.Strings(parts)
		fmt.Printf("skipped (this build cannot write them): %s\n", strings.Join(parts, ", "))
	}
	if !apply && t.changed > 0 {
		fmt.Println("\nthis was a dry run; add -apply to write, and -backup FILE to keep an undo")
	}
}

// backupRecord is one file's removed frames, as written to the backup log.
type backupRecord struct {
	Path   string            `json:"path"`
	Frames []tags.RemovedTag `json:"frames"`
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

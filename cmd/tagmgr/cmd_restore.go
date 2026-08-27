package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/remy/tag-manager/internal/tags"
	"github.com/remy/tag-manager/internal/ui"
)

const restoreSummary = `tagmgr restore - put stripped tags back from a backup

Usage:
  tagmgr restore [flags] -backup FILE

Reads the log written by "tagmgr strip -backup" and adds the removed frames
back to each file. Frames already present are left alone, so restoring twice
is harmless and a restore never overwrites an edit made since the strip.

This is a dry run unless -apply is given.

Examples:
  tagmgr restore -backup ~/strip.jsonl            what would come back
  tagmgr restore -backup ~/strip.jsonl -apply
`

func cmdRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	backup := fs.String("backup", "", "backup file written by: tagmgr strip -backup")
	apply := fs.Bool("apply", false, "actually write the files (default is a dry run)")
	workers := fs.Int("workers", 0, "concurrency (0 = auto)")
	if err := parseFlags(fs, args, restoreSummary, ""); err != nil {
		return err
	}
	if *backup == "" {
		return fmt.Errorf("-backup is required (try: tagmgr help restore)")
	}

	records, err := readBackup(*backup)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return fmt.Errorf("%s contains no records", *backup)
	}

	if !*apply {
		frames, missing := 0, 0
		for _, r := range records {
			frames += len(r.Frames)
			if _, err := os.Stat(r.Path); err != nil {
				missing++
			}
		}
		fmt.Printf("would restore %s frames across %s files\n",
			ui.FormatCount(frames), ui.FormatCount(len(records)))
		if missing > 0 {
			fmt.Printf("%s files in the backup no longer exist\n", ui.FormatCount(missing))
		}
		fmt.Println("\nthis was a dry run; add -apply to write")
		return nil
	}

	if *workers <= 0 {
		*workers = min(runtime.NumCPU(), 8)
	}
	var (
		mu             sync.Mutex
		added, touched int
		failed         []error
	)
	start := time.Now()
	jobs := make(chan backupRecord)
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range jobs {
				n, err := tags.RestoreID3v2(r.Path, r.Frames)
				mu.Lock()
				switch {
				case err != nil:
					if len(failed) < 20 {
						failed = append(failed, err)
					}
				case n > 0:
					added += n
					touched++
				}
				mu.Unlock()
			}
		}()
	}
	for _, r := range records {
		jobs <- r
	}
	close(jobs)
	wg.Wait()

	fmt.Printf("restored %s frames across %s files (%s)\n",
		ui.FormatCount(added), ui.FormatCount(touched), ui.FormatDuration(time.Since(start)))
	if len(failed) > 0 {
		return fmt.Errorf("%d files could not be restored; first: %w", len(failed), failed[0])
	}
	return nil
}

// readBackup loads the JSON-per-line log written during a strip.
func readBackup(path string) ([]backupRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []backupRecord
	sc := bufio.NewScanner(f)
	// Records hold whole frame payloads, which for cover art run to megabytes.
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for line := 1; sc.Scan(); line++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var r backupRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, line, err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

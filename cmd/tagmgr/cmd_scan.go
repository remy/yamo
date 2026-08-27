package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/remy/tag-manager/internal/catalog"
	"github.com/remy/tag-manager/internal/scan"
	"github.com/remy/tag-manager/internal/ui"
)

const scanSummary = `tagmgr scan - build or refresh the catalogue

Usage:
  tagmgr scan [flags] <dir>...

Walks each directory, extracts tags, and writes the catalogue. Files whose
size and modification time are unchanged since the last scan are reused
without being opened, so a refresh costs a stat per file rather than a read;
pass -full to ignore that and re-read everything.

Deleted files drop out of the catalogue on the next scan. Directories that
never hold music (@eaDir, #recycle, lost+found, dot-directories) and
AppleDouble "._" sidecars are skipped.

With no directories given, refreshes whatever the catalogue already covers.

Examples:
  tagmgr scan /volume1/music
  tagmgr scan                             refresh the existing roots
  tagmgr scan -full /volume1/music        re-read every file
  tagmgr scan -exclude Podcasts /volume1/music
`

func cmdScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	workers := fs.Int("workers", 0, "tag reader concurrency (0 = auto)")
	full := fs.Bool("full", false, "re-read every file")
	hidden := fs.Bool("hidden", false, "include dot-directories")
	follow := fs.Bool("follow", false, "follow directory symlinks")
	quiet := fs.Bool("quiet", false, "suppress progress output")
	var exclude stringList
	fs.Var(&exclude, "exclude", "skip paths matching a glob (repeatable)")
	if err := parseFlags(fs, args, scanSummary, ""); err != nil {
		return err
	}

	roots := fs.Args()
	if len(roots) == 0 {
		// With no roots given, refresh whatever the catalogue already covers.
		if prev, err := catalog.Load(*catalogPath); err == nil && len(prev.Roots) > 0 {
			roots = prev.Roots
			fmt.Fprintf(os.Stderr, "refreshing %s\n", strings.Join(roots, ", "))
		} else {
			return fmt.Errorf("no directories given and no catalogue at %s", *catalogPath)
		}
	}

	opts := scan.Options{
		Roots:          roots,
		Workers:        *workers,
		Exclude:        exclude,
		IncludeHidden:  *hidden,
		FollowSymlinks: *follow,
	}
	if !*full {
		if prev, err := catalog.Load(*catalogPath); err == nil {
			opts.Previous = prev
		}
	}

	ctx, cancel := notifyContext()
	defer cancel()

	var report func(scan.Stats)
	if !*quiet {
		report = progressPrinter()
	}

	start := time.Now()
	c, err := scan.Scan(ctx, opts, report)
	if err != nil && c == nil {
		return err
	}
	if err := catalog.Save(*catalogPath, c); err != nil {
		return fmt.Errorf("saving catalogue: %w", err)
	}

	fmt.Fprintf(os.Stderr, "\n%d tracks catalogued in %s -> %s\n",
		c.Len(), ui.FormatDuration(time.Since(start)), *catalogPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan interrupted; partial catalogue saved")
	}
	return nil
}

// progressPrinter returns a callback that redraws a single status line.
func progressPrinter() func(scan.Stats) {
	var lastLen int
	return func(st scan.Stats) {
		rate := float64(0)
		if secs := st.Elapsed.Seconds(); secs > 0 {
			rate = float64(st.Parsed+st.Reused) / secs
		}
		line := fmt.Sprintf("  %s  %d dirs  %d files  %d read  %d unchanged  %d errors  %.0f/s",
			ui.FormatDuration(st.Elapsed), st.Dirs, st.Found, st.Parsed, st.Reused, st.Errors, rate)
		if pad := lastLen - len(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		lastLen = len(line)
		fmt.Fprintf(os.Stderr, "\r%s", line)
		if st.Finished {
			fmt.Fprintln(os.Stderr)
		}
	}
}

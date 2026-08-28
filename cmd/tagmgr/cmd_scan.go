package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/remy/tag-manager/internal/client"
	"github.com/remy/tag-manager/internal/library"
	"github.com/remy/tag-manager/internal/ui"
)

const scanSummary = `tagmgr scan - bring the library up to date

Usage:
  tagmgr scan [flags] <dir>...

Asks the server to walk each directory and extract tags. Files whose size
and modification time are unchanged since the last scan are reused without
being opened, so a refresh costs a stat per file rather than a read; pass
-full to ignore that and re-read everything.

Deleted files drop out on the next scan. Directories that never hold music
(@eaDir, #recycle, lost+found, dot-directories) and AppleDouble "._"
sidecars are skipped.

With no directories given, refreshes whatever the library already covers.

The paths are resolved by the server, not by this command, so they must
make sense on the machine running it.

Examples:
  tagmgr scan /volume1/music
  tagmgr scan                             refresh the existing roots
  tagmgr scan -full /volume1/music        re-read every file
  tagmgr scan -exclude Podcasts /volume1/music
`

func cmdScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	server, token := serverFlags(fs)
	full := fs.Bool("full", false, "re-read every file")
	hidden := fs.Bool("hidden", false, "include dot-directories")
	follow := fs.Bool("follow", false, "follow directory symlinks")
	quiet := fs.Bool("quiet", false, "suppress progress output")
	workers := fs.Int("workers", 0, "tag reader concurrency (0 = auto)")
	var exclude stringList
	fs.Var(&exclude, "exclude", "skip paths matching a glob (repeatable)")
	if err := parseFlags(fs, args, scanSummary, ""); err != nil {
		return err
	}

	c, err := connect(*server, *token)
	if err != nil {
		return err
	}
	ctx, cancel := notifyContext()
	defer cancel()

	req := library.ScanRequest{
		Roots:          fs.Args(),
		Full:           *full,
		Exclude:        exclude,
		IncludeHidden:  *hidden,
		FollowSymlinks: *follow,
		Workers:        *workers,
	}
	job, err := c.Scan(ctx, req)
	if err != nil {
		return err
	}

	start := time.Now()
	done, err := waitForJobQuiet(ctx, c, job, "read", *quiet)
	if err != nil {
		return err
	}

	var res library.ScanResult
	if err := client.DecodeResult(done, &res); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "  %s tracks in %s: %s read, %s unchanged, %s errors\n",
		ui.FormatCount(res.Tracks), ui.FormatDuration(time.Since(start)),
		ui.FormatCount(int(res.Parsed)), ui.FormatCount(int(res.Reused)), ui.FormatCount(int(res.Errors)))
	if res.Removed > 0 {
		fmt.Fprintf(os.Stderr, "  %s tracks no longer exist and were dropped\n", ui.FormatCount(res.Removed))
	}
	if done.State == library.JobCancelled {
		fmt.Fprintln(os.Stderr, "  the scan was cancelled; what it found was kept")
	}
	return nil
}

// waitForJobQuiet follows a job, optionally without progress output.
func waitForJobQuiet(ctx context.Context, c *client.Client, job *library.Job, verb string, quiet bool) (*library.Job, error) {
	if quiet {
		return c.WaitJob(ctx, job.ID, nil)
	}
	return waitForJob(ctx, c, job, verb)
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

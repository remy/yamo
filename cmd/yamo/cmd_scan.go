package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/remy/yamo/internal/client"
	"github.com/remy/yamo/internal/library"
	"github.com/remy/yamo/internal/ui"
)

const scanSummary = `yamo scan - bring the library up to date

Usage:
  yamo scan [flags] <dir>...

Asks the server to walk each directory and extract tags. Files whose size
and modification time are unchanged since the last scan are reused without
being opened, so a refresh costs a stat per file rather than a read; pass
-full to ignore that and re-read everything.

Deleted files drop out on the next scan. Directories that never hold music
(@eaDir, #recycle, lost+found, dot-directories) and AppleDouble "._"
sidecars are skipped.

With no directories given, refreshes whatever the library already covers.

Only one scan runs at a time. Asking for another while one is under way
follows the running one rather than starting a second, since two would each
rebuild the whole catalogue and whichever finished last would win.

The paths are resolved by the server, not by this command, so they must
make sense on the machine running it.

Examples:
  yamo scan /volume1/music
  yamo scan -status                     is one running?
  yamo scan                             refresh the existing roots
  yamo scan -full /volume1/music        re-read every file
  yamo scan -exclude Podcasts /volume1/music
`

func cmdScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	server, token := serverFlags(fs)
	full := fs.Bool("full", false, "re-read every file")
	hidden := fs.Bool("hidden", false, "include dot-directories")
	follow := fs.Bool("follow", false, "follow directory symlinks")
	quiet := fs.Bool("quiet", false, "suppress progress output")
	status := fs.Bool("status", false, "report whether a scan is running, and exit")
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

	if *status {
		return printScanStatus(ctx, c)
	}

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
		// A scan already running is not a failure worth stopping for; follow
		// it rather than making the user work out what happened.
		id := client.RunningScanID(err)
		if id == "" {
			return err
		}
		fmt.Fprintln(os.Stderr, "  a scan is already running; following it")
		if job, err = c.Job(ctx, id); err != nil {
			return err
		}
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

// printScanStatus reports whether a scan is running and what the last one did.
func printScanStatus(ctx context.Context, c *client.Client) error {
	st, err := c.ScanStatus(ctx)
	if err != nil {
		return err
	}
	if st.Running && st.Job != nil {
		fmt.Printf("scanning now  job %s  %s of %s read\n", st.Job.ID,
			ui.FormatCount(int(st.Job.Progress.Done)), ui.FormatCount(int(st.Job.Progress.Total)))
	} else {
		fmt.Println("no scan running")
	}
	fmt.Printf("tracks        %s\n", ui.FormatCount(st.Tracks))
	if len(st.Roots) > 0 {
		fmt.Printf("roots         %s\n", strings.Join(st.Roots, "\n              "))
	}
	if st.ScannedAt != nil {
		fmt.Printf("last scanned  %s\n", st.ScannedAt.Format(time.RFC1123))
	}
	if st.Last != nil && st.Last.State != library.JobSucceeded {
		fmt.Printf("last job      %s: %s\n", st.Last.State, st.Last.Error)
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

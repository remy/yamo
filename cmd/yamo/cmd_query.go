package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/remy/yamo/internal/client"
	"github.com/remy/yamo/internal/library"
	"github.com/remy/yamo/internal/ui"
)

const findSummary = `yamo find - print matching tracks and exit

Usage:
  yamo find [flags] <query>

Searches the library and prints the results. The -format path output is
one filename per line, which is both a valid playlist and easy to pipe:

  yamo find -limit 0 -format path artist:elvis > elvis.m3u

Examples:
  yamo find artist:elvis
  yamo find artist:~presly
  yamo find -format tsv 'year:>1990 genre:jazz'
  yamo find -sort '-year,artist' 'genre:jazz'
  yamo find -- -genre:live artist:elvis
`

const infoSummary = `yamo info - show library statistics

Usage:
  yamo info [flags]

Prints what the library holds, and which fields are missing across it —
which is where the work is.
`

func cmdFind(args []string) error {
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	server, token := serverFlags(fs)
	limit := fs.Int("limit", 200, "maximum rows to print (0 for all)")
	sortBy := fs.String("sort", "", "sort order, e.g. artist,album,track or -year")
	format := fs.String("format", "table", "output format: table, path, tsv, json")
	if err := parseFlags(fs, args, findSummary, queryHelp); err != nil {
		return err
	}

	c, err := connect(*server, *token)
	if err != nil {
		return err
	}
	ctx, cancel := notifyContext()
	defer cancel()

	params := library.ListParams{Query: strings.Join(fs.Args(), " "), Sort: *sortBy, Limit: *limit}
	start := time.Now()

	var items []library.Track
	var total int
	if *limit <= 0 {
		if items, err = c.AllTracks(ctx, params); err != nil {
			return err
		}
		total = len(items)
	} else {
		page, err := c.ListTracks(ctx, params)
		if err != nil {
			return err
		}
		items, total = page.Items, page.Total
	}
	elapsed := time.Since(start)

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	// A fuzzy query ranks its results, so the table shows what it ranked them
	// on. The machine-readable formats are left alone: a column that comes and
	// goes would break the scripts they exist for, and -format json carries
	// the score already.
	scored := len(items) > 0 && items[0].Score > 0

	for i := range items {
		t := &items[i]
		switch *format {
		case "path":
			fmt.Fprintln(out, t.Path)
		case "tsv":
			fmt.Fprintf(out, "%s\t%s\t%s\t%d\t%s\t%s\n",
				t.Artist, t.Album, t.Title, t.TrackNo, t.Genre, t.Path)
		case "json":
			writeJSONLine(out, t)
		default:
			fmt.Fprintf(out, "%-28s %-28s %-32s %4s  %s",
				ui.Truncate(t.Artist, 28), ui.Truncate(t.Album, 28),
				ui.Truncate(t.Title, 32), ui.FormatTrackNo(t.TrackNo),
				ui.FormatMillis(t.DurationMS))
			if scored {
				fmt.Fprintf(out, "  %.2f", t.Score)
			}
			fmt.Fprintln(out)
		}
	}
	fmt.Fprintf(os.Stderr, "%d shown of %d matching in %s\n",
		len(items), total, elapsed.Round(time.Millisecond))
	return nil
}

func cmdInfo(args []string) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	server, token := serverFlags(fs)
	if err := parseFlags(fs, args, infoSummary, ""); err != nil {
		return err
	}
	c, err := connect(*server, *token)
	if err != nil {
		return err
	}
	ctx, cancel := notifyContext()
	defer cancel()

	st, err := c.Stats(ctx)
	if err != nil {
		return err
	}

	roots := "none yet — run: yamo scan <dir>"
	if len(st.Roots) > 0 {
		roots = strings.Join(st.Roots, "\n             ")
	}
	fmt.Printf("roots        %s\n", roots)
	if !st.ScannedAt.IsZero() {
		fmt.Printf("scanned      %s\n", st.ScannedAt.Format(time.RFC1123))
	}
	// Only worth a line when the server is on a timer. Without one the
	// "scanned" line above is the whole story, which is the point of saying so.
	if st.RescanEveryMS > 0 {
		every := time.Duration(st.RescanEveryMS) * time.Millisecond
		line := fmt.Sprintf("every %s", every)
		if st.NextRescanAt != nil {
			line += fmt.Sprintf(", next %s", st.NextRescanAt.Local().Format(time.RFC1123))
		}
		fmt.Printf("rescan       %s\n", line)
	}
	fmt.Printf("tracks       %s\n", ui.FormatCount(st.Tracks))
	fmt.Printf("audio        %s across %s\n",
		ui.FormatBytes(st.TotalBytes), ui.FormatDuration(time.Duration(st.TotalMS)*time.Millisecond))
	fmt.Printf("artists      %s\n", ui.FormatCount(st.Artists))
	fmt.Printf("albums       %s\n", ui.FormatCount(st.Albums))
	fmt.Printf("genres       %s\n", ui.FormatCount(st.Genres))
	fmt.Printf("artwork      %s tracks have some\n", ui.FormatCount(st.WithArt))
	fmt.Printf("formats      %s\n", countsLine(st.Formats))
	fmt.Printf("missing      %s\n", countsLine(st.Missing))
	return nil
}

// countsLine renders a map of counts in a stable order.
func countsLine(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s %s", k, ui.FormatCount(v)))
	}
	sort.Strings(parts)
	return strings.Join(parts, "  ")
}

// waitForJob follows a job to completion, printing progress to stderr so that
// stdout stays usable in a pipeline.
func waitForJob(ctx context.Context, c *client.Client, job *library.Job, verb string) (*library.Job, error) {
	var lastLen int
	done, err := c.WaitJob(ctx, job.ID, func(j *library.Job) {
		// Nothing useful to show until the job knows how much there is; a
		// fast operation would otherwise only ever print "0 of 0".
		if j.State != library.JobRunning || j.Progress.Total == 0 {
			return
		}
		line := fmt.Sprintf("  %s %s of %s", verb,
			ui.FormatCount(int(j.Progress.Done)), ui.FormatCount(int(j.Progress.Total)))
		if pad := lastLen - len(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		lastLen = len(line)
		fmt.Fprintf(os.Stderr, "\r%s", line)
	})
	if lastLen > 0 {
		fmt.Fprintf(os.Stderr, "\r%s\r", strings.Repeat(" ", lastLen))
	}
	if err != nil {
		return nil, err
	}
	if done.State == library.JobFailed {
		return done, fmt.Errorf("%s", done.Error)
	}
	return done, nil
}

func writeJSONLine(w *bufio.Writer, t *library.Track) {
	fmt.Fprintf(w, `{"id":%q,"artist":%q,"album":%q,"title":%q,"path":%q}`+"\n",
		t.ID, t.Artist, t.Album, t.Title, t.Path)
}

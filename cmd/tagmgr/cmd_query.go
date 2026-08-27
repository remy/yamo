package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/remy/tag-manager/internal/catalog"
	"github.com/remy/tag-manager/internal/ui"
)

const findSummary = `tagmgr find - print matching tracks and exit

Usage:
  tagmgr find [flags] <query>

Searches the catalogue and prints the results. The -format path output is
one filename per line, which is both a valid playlist and easy to pipe:

  tagmgr find -limit 0 -format path artist:elvis > elvis.m3u
  tagmgr find -limit 0 -format path album: | while read -r f; do echo "$f"; done

Examples:
  tagmgr find artist:elvis
  tagmgr find -format tsv 'year:>1990 genre:jazz'
  tagmgr find -- -genre:live artist:elvis
`

const infoSummary = `tagmgr info - show catalogue statistics

Usage:
  tagmgr info [flags]

Prints where the catalogue is, when it was built, what it covers, and which
fields are missing across the library.
`

func cmdFind(args []string) error {
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	limit := fs.Int("limit", 200, "maximum rows to print (0 for all)")
	format := fs.String("format", "table", "output format: table, path, tsv")
	if err := parseFlags(fs, args, findSummary, queryHelp); err != nil {
		return err
	}

	c, err := catalog.Load(*catalogPath)
	if err != nil {
		return fmt.Errorf("loading catalogue: %w (run: tagmgr scan <dir>)", err)
	}

	// Build the index before timing so the reported figure is the search
	// itself, not the one-off cost of folding the library.
	ix := c.Index()
	start := time.Now()
	q := catalog.ParseQuery(strings.Join(fs.Args(), " "))
	hits := ix.Search(q)
	elapsed := time.Since(start)

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	shown := hits
	if *limit > 0 && len(shown) > *limit {
		shown = shown[:*limit]
	}
	for _, i := range shown {
		t := &c.Tracks[i]
		switch *format {
		case "path":
			fmt.Fprintln(out, t.Path)
		case "tsv":
			fmt.Fprintf(out, "%s\t%s\t%s\t%d\t%s\t%s\n",
				t.Artist, t.Album, t.Title, t.TrackNo, t.Genre, t.Path)
		default:
			fmt.Fprintf(out, "%-28s %-28s %-32s %4s  %s\n",
				ui.Truncate(t.Artist, 28), ui.Truncate(t.Album, 28),
				ui.Truncate(t.Title, 32), ui.FormatTrackNo(t.TrackNo),
				ui.FormatMillis(t.DurationMS))
		}
	}
	fmt.Fprintf(os.Stderr, "%d/%d tracks in %s\n", len(hits), c.Len(), elapsed.Round(time.Microsecond))
	return nil
}

func cmdInfo(args []string) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	if err := parseFlags(fs, args, infoSummary, ""); err != nil {
		return err
	}

	start := time.Now()
	c, err := catalog.Load(*catalogPath)
	if err != nil {
		return fmt.Errorf("loading catalogue: %w (run: tagmgr scan <dir>)", err)
	}
	loadTime := time.Since(start)

	fi, _ := os.Stat(*catalogPath)
	var totalBytes, totalMS int64
	missing := map[string]int{}
	formats := map[string]int{}
	for i := range c.Tracks {
		t := &c.Tracks[i]
		totalBytes += t.Size
		totalMS += int64(t.DurationMS)
		formats[t.Format.String()]++
		if t.Artist == "" {
			missing["artist"]++
		}
		if t.Album == "" {
			missing["album"]++
		}
		if t.Title == "" {
			missing["title"]++
		}
		if t.Genre == "" {
			missing["genre"]++
		}
		if t.Year == 0 {
			missing["year"]++
		}
	}

	ix := c.Index()
	fmt.Printf("catalogue    %s\n", *catalogPath)
	if fi != nil {
		fmt.Printf("size         %s (loaded in %s)\n", ui.FormatBytes(fi.Size()), loadTime.Round(time.Millisecond))
	}
	fmt.Printf("scanned      %s\n", c.ScannedAt.Format(time.RFC1123))
	fmt.Printf("roots        %s\n", strings.Join(c.Roots, "\n             "))
	fmt.Printf("tracks       %d\n", c.Len())
	fmt.Printf("audio        %s across %s\n", ui.FormatBytes(totalBytes), ui.FormatDuration(time.Duration(totalMS)*time.Millisecond))
	fmt.Printf("artists      %d\n", len(ix.Values(catalog.FieldArtist).Values))
	fmt.Printf("albums       %d\n", len(ix.Values(catalog.FieldAlbum).Values))
	fmt.Printf("genres       %d\n", len(ix.Values(catalog.FieldGenre).Values))
	fmt.Printf("formats      %s\n", formatCounts(formats))
	fmt.Printf("missing      %s\n", formatCounts(missing))
	return nil
}

func formatCounts(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s %d", k, v))
	}
	// Stable output regardless of map iteration order.
	sortStrings(parts)
	return strings.Join(parts, "  ")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

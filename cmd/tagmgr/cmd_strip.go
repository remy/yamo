package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/remy/tag-manager/internal/client"
	"github.com/remy/tag-manager/internal/library"
	"github.com/remy/tag-manager/internal/tags"
	"github.com/remy/tag-manager/internal/ui"
)

const stripSummary = `tagmgr strip - remove every tag except a fixed set

Usage:
  tagmgr strip [flags] [query]

Removes every tag that is not on the keep list, leaving a uniform set of
metadata across the library. With a query, only matching tracks are
touched; with none, everything is.

The keep list is written in canonical names — title, albumartist, artwork —
not in the identifiers any one format uses, so the same list applies to
MP3, FLAC, MP4, Ogg Vorbis and Opus alike. An album artist is kept whether
the file spells it TPE2, ALBUMARTIST or aART. Names native to a format are
accepted as aliases.

This is a dry run unless -apply is given. The dry run reports exactly what
would go, grouped by format and key, so the damage can be read beforehand.

-normalize additionally moves kept fields that a file holds under an older
name into the one this library writes: an ID3v2.2 frame, a genre stored as
"(19)", an MP4 gnre atom, a Vorbis PERFORMER. The value does not change,
only where it is kept. A dry run counts them without writing.

WMA, WAV and AIFF are read but not written, so they are counted and skipped.

Examples:
  tagmgr strip                          what would be removed from everything
  tagmgr strip artist:elvis             ...from one artist
  tagmgr strip -list                    print the keep list and exit
  tagmgr strip -also gapless,musicbrainz -apply
  tagmgr strip -backup -apply
  tagmgr strip -normalize -backup -apply    tidy where values are stored too
  tagmgr restore -backup ID -apply
`

const restoreSummary = `tagmgr restore - put stripped tags back from a backup

Usage:
  tagmgr restore [flags] -backup ID

Backups live on the server and are addressed by id, so the client that
restores need not be the one that stripped. List them with -list.

Tags already present are left alone, so restoring twice is harmless and a
restore never overwrites an edit made since the strip.

This is a dry run unless -apply is given.
`

func cmdStrip(args []string) error {
	fs := flag.NewFlagSet("strip", flag.ContinueOnError)
	server, token := serverFlags(fs)
	apply := fs.Bool("apply", false, "actually write the files (default is a dry run)")
	list := fs.Bool("list", false, "print the keep list and exit")
	keepFlag := fs.String("keep", "", "replace the keep list with this comma-separated set")
	alsoFlag := fs.String("also", "", "add these tags to the keep list, comma-separated")
	backup := fs.Bool("backup", false, "record removed tags on the server so restore can undo them")
	normalize := fs.Bool("normalize", false, "also move kept fields stored under an older name into the standard one")
	if err := parseFlags(fs, args, stripSummary, queryHelp); err != nil {
		return err
	}

	if *list {
		return printKeepList(*keepFlag, *alsoFlag)
	}

	c, err := connect(*server, *token)
	if err != nil {
		return err
	}
	ctx, cancel := notifyContext()
	defer cancel()

	query := strings.Join(fs.Args(), " ")
	sel := library.Selector{Query: query}
	if query == "" {
		sel = library.Selector{All: true}
	}
	if *apply && !*backup {
		fmt.Fprint(os.Stderr, "warning: no -backup given, so this cannot be undone\n\n")
	}

	job, err := c.Strip(ctx, library.StripRequest{
		Selector:  sel,
		Keep:      splitList(*keepFlag),
		Also:      splitList(*alsoFlag),
		DryRun:    !*apply,
		Backup:    *backup,
		Normalize: *normalize,
	})
	if err != nil {
		return err
	}
	done, err := waitForJob(ctx, c, job, "examined")
	if err != nil {
		return err
	}

	var res library.StripResult
	if err := client.DecodeResult(done, &res); err != nil {
		return err
	}
	printStripResult(res, *apply)
	return nil
}

func printStripResult(res library.StripResult, apply bool) {
	fmt.Printf("\n%-5s %-22s %8s  %10s  %s\n", "fmt", "key", "tracks", "bytes", "meaning")
	for _, g := range res.Removed {
		line := fmt.Sprintf("%-5s %-22s %8s  %10s  %s", g.Format, ui.Truncate(g.Key, 22),
			ui.FormatCount(g.Tracks), ui.FormatBytes(g.Bytes), g.Meaning)
		if len(g.Samples) > 0 {
			line += "  ·  " + ui.Truncate(strings.Join(g.Samples, " / "), 40)
		}
		fmt.Println(line)
	}

	verb := "would remove"
	if apply {
		verb = "removed"
	}
	fmt.Printf("\n%s %s across %s of %s tracks\n", verb, ui.FormatBytes(res.Bytes),
		ui.FormatCount(res.Changed), ui.FormatCount(res.Matched))
	if res.Upgraded > 0 {
		fmt.Printf("%s files rewritten from ID3v2.2 to ID3v2.3\n", ui.FormatCount(res.Upgraded))
	}
	if res.Normalized > 0 {
		verb := "hold"
		if apply {
			verb = "held"
		}
		fmt.Printf("%s tracks %s %s under an older name\n", ui.FormatCount(res.Normalized),
			verb, strings.Join(res.NormalizeFields, ", "))
	}
	if len(res.Skipped) > 0 {
		fmt.Printf("skipped (this build cannot write them): %s\n", countsLine(res.Skipped))
	}
	if res.BackupID != "" {
		fmt.Printf("backup %s — undo with: tagmgr restore -backup %s -apply\n", res.BackupID, res.BackupID)
	}
	reportFailures(res.BatchResult)
	if !apply && res.Changed > 0 {
		fmt.Println("\nthis was a dry run; add -apply to write, and -backup to keep an undo")
	}
}

// printKeepList shows the keep list with the native keys each tag maps to.
//
// The list is resolved here rather than on the server: it is a property of the
// build's tag support, not of the library, and being able to see it without a
// server running is useful when deciding what to keep.
func printKeepList(keepFlag, alsoFlag string) error {
	keep := tags.NewKeepSet(tags.DefaultKeepTags)
	if keepFlag != "" {
		parsed, unknown := tags.ParseKeepSet(splitList(keepFlag))
		if len(unknown) > 0 {
			return fmt.Errorf("unknown tag %q (try: tagmgr strip -list)", unknown[0])
		}
		keep = parsed
	}
	if alsoFlag != "" {
		extra, unknown := tags.ParseKeepSet(splitList(alsoFlag))
		if len(unknown) > 0 {
			return fmt.Errorf("unknown tag %q (try: tagmgr strip -list)", unknown[0])
		}
		for t := range extra {
			keep[t] = true
		}
	}

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
	return nil
}

func nativeKeys(t tags.Tag, f tags.Format) string {
	k := t.NativeKeys(f)
	if len(k) == 0 {
		return "—"
	}
	return strings.Join(k, " ")
}

func printAvailableTags(keep tags.KeepSet) {
	line := "  "
	for _, t := range tags.AllTags() {
		if keep[t] {
			continue
		}
		if len(line)+len(t.Name())+2 > 78 {
			fmt.Println(line)
			line = "  "
		}
		line += t.Name() + "  "
	}
	if strings.TrimSpace(line) != "" {
		fmt.Println(line)
	}
}

func cmdRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	server, token := serverFlags(fs)
	backupID := fs.String("backup", "", "backup id, from a strip that used -backup")
	list := fs.Bool("list", false, "list the backups the server holds")
	apply := fs.Bool("apply", false, "actually write the files (default is a dry run)")
	if err := parseFlags(fs, args, restoreSummary, ""); err != nil {
		return err
	}

	c, err := connect(*server, *token)
	if err != nil {
		return err
	}
	ctx, cancel := notifyContext()
	defer cancel()

	if *list {
		backups, err := c.Backups(ctx)
		if err != nil {
			return err
		}
		if len(backups) == 0 {
			fmt.Println("no backups")
			return nil
		}
		for _, b := range backups {
			fmt.Printf("%s  %s  %s\n", b.ID, b.Created.Format("2006-01-02 15:04"), ui.FormatBytes(b.Bytes))
		}
		return nil
	}
	if *backupID == "" {
		return fmt.Errorf("-backup is required (try: tagmgr restore -list)")
	}

	job, err := c.Restore(ctx, library.RestoreRequest{BackupID: *backupID, DryRun: !*apply})
	if err != nil {
		return err
	}
	done, err := waitForJob(ctx, c, job, "restored")
	if err != nil {
		return err
	}
	var res library.BatchResult
	if err := client.DecodeResult(done, &res); err != nil {
		return err
	}
	verb := "would restore tags on"
	if *apply {
		verb = "restored tags on"
	}
	fmt.Printf("%s %s tracks\n", verb, ui.FormatCount(res.Changed))
	if res.Skipped > 0 {
		fmt.Printf("%s tracks already had them\n", ui.FormatCount(res.Skipped))
	}
	reportFailures(res)
	if !*apply && res.Changed > 0 {
		fmt.Println("\nthis was a dry run; add -apply to write")
	}
	return nil
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

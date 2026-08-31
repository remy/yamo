package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/remy/yamo/internal/client"
	"github.com/remy/yamo/internal/library"
	"github.com/remy/yamo/internal/ui"
)

const artSummary = `yamo art - inspect, copy and replace cover art

Usage:
  yamo art [flags] [query]

Cover art moves by clipboard: copy one image, then paste it onto however
many tracks you like. The clipboard lives on the server, so a cover copied
here can be pasted in the browser or from a phone, and the other way round.

  yamo art                            what art the matching tracks have
  yamo art -copy TRACK_OR_IMAGE       put an image on the clipboard
  yamo art -paste QUERY -apply        write it to the matching tracks
  yamo art -export DIR QUERY          write covers out as files
  yamo art -remove QUERY -apply       take the art off

-copy accepts an image file on this machine, which is uploaded, or a track
id to lift the cover from one already in the library.

-paste, -remove and -from-folder are dry runs unless -apply is given.

Folder art:
  yamo art -from-folder QUERY -apply

  Embeds the cover.jpg or folder.jpg sitting beside each track, which is
  how a downloaded library stores art and the usual reason none of it shows
  on a phone. The server reads each directory once, not once per track.

Embedding art rewrites the file: a cover is far larger than the padding any
format reserves, so unlike other edits the audio has to move. Tracks whose
art already matches are skipped.
`

func cmdArt(args []string) error {
	fs := flag.NewFlagSet("art", flag.ContinueOnError)
	server, token := serverFlags(fs)
	copyFrom := fs.String("copy", "", "copy an image file, or a track id's cover, to the clipboard")
	paste := fs.Bool("paste", false, "write the clipboard image to the matching tracks")
	remove := fs.Bool("remove", false, "remove art from the matching tracks")
	export := fs.String("export", "", "write each distinct cover into this directory")
	fromFolder := fs.Bool("from-folder", false, "embed cover.jpg or folder.jpg found beside each track")
	clear := fs.Bool("clear", false, "empty the clipboard")
	apply := fs.Bool("apply", false, "actually write the files (default is a dry run)")
	if err := parseFlags(fs, args, artSummary, queryHelp); err != nil {
		return err
	}

	c, err := connect(*server, *token)
	if err != nil {
		return err
	}
	ctx, cancel := notifyContext()
	defer cancel()
	query := strings.Join(fs.Args(), " ")

	switch {
	case *clear:
		if err := c.ClearClipboard(ctx); err != nil {
			return err
		}
		fmt.Println("clipboard emptied")
		return nil

	case *copyFrom != "":
		return artCopy(ctx, c, *copyFrom)

	case *export != "":
		return artExport(ctx, c, query, *export)

	case *remove:
		return artBatch(ctx, c, query, "remove", nil, *apply, "remove art from", "removed art from")

	case *paste:
		if _, _, err := c.Clipboard(ctx); err != nil {
			if client.IsNotFound(err) {
				return errors.New("the clipboard is empty (try: yamo art -copy FILE)")
			}
			return err
		}
		return artBatch(ctx, c, query, "clipboard", nil, *apply, "set art on", "set art on")

	case *fromFolder:
		return artBatch(ctx, c, query, "folder", nil, *apply, "set art on", "set art on")

	default:
		return artReport(ctx, c, query)
	}
}

// artCopy puts an image on the server's clipboard, from a local file or from a
// track the server already holds.
func artCopy(ctx context.Context, c *client.Client, src string) error {
	// A track id has no path separators and no extension; anything else is
	// treated as a file on this machine and uploaded.
	if !strings.ContainsAny(src, `/\.`) {
		info, err := c.CopyArtworkFromTrack(ctx, src)
		if err != nil {
			return err
		}
		fmt.Printf("copied %s from track %s\n", info.Summary, src)
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	info, err := c.PutClipboard(ctx, data)
	if err != nil {
		return err
	}
	fmt.Printf("copied %s from %s\n", info.Summary, filepath.Base(src))
	return nil
}

// artBatch runs one of the batch artwork operations.
func artBatch(ctx context.Context, c *client.Client, query, source string, image []byte, apply bool, future, past string) error {
	sel := library.Selector{Query: query}
	if query == "" {
		sel = library.Selector{All: true}
	}
	job, err := c.BatchArtwork(ctx, client.BatchArtworkRequest{
		Selector: sel, Source: source, Image: image, DryRun: !apply,
	})
	if err != nil {
		return err
	}
	done, err := waitForJob(ctx, c, job, "processed")
	if err != nil {
		return err
	}

	var res library.BatchResult
	if err := client.DecodeResult(done, &res); err != nil {
		return err
	}
	word := past
	if !apply {
		word = "would " + future
	}
	fmt.Printf("%s %s tracks\n", word, ui.FormatCount(res.Changed))
	if res.Skipped > 0 {
		fmt.Printf("%s tracks were skipped: already that image, or no image to use\n", ui.FormatCount(res.Skipped))
	}
	reportFailures(res)
	if !apply && res.Changed > 0 {
		fmt.Println("\nthis was a dry run; add -apply to write")
	}
	return nil
}

// artReport summarises the art across a selection, grouping identical covers.
func artReport(ctx context.Context, c *client.Client, query string) error {
	rep, err := c.ArtworkSummary(ctx, query)
	if err != nil {
		return err
	}
	fmt.Printf("%8s  %-28s  %10s  %s\n", "tracks", "image", "embedded", "example album")
	for _, g := range rep.Groups {
		fmt.Printf("%8s  %-28s  %10s  %s\n", ui.FormatCount(g.Tracks),
			ui.Truncate(g.Summary, 28), ui.FormatBytes(g.Bytes), ui.Truncate(g.ExampleDir, 40))
	}
	fmt.Printf("\n%s distinct images across %s tracks; %s embedded, %s of it unique\n",
		ui.FormatCount(len(rep.Groups)), ui.FormatCount(rep.Tracks-rep.WithoutArt),
		ui.FormatBytes(rep.TotalBytes), ui.FormatBytes(rep.UniqueBytes))
	if rep.WithoutArt > 0 {
		fmt.Printf("%s tracks have no art\n", ui.FormatCount(rep.WithoutArt))
	}
	return nil
}

// artExport downloads each distinct cover once, named after its album.
func artExport(ctx context.Context, c *client.Client, query, dir string) error {
	rep, err := c.ArtworkSummary(ctx, query)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	written := 0
	for _, g := range rep.Groups {
		data, _, err := c.Artwork(ctx, g.SampleID)
		if err != nil {
			continue
		}
		name := g.ExampleDir
		if name == "" {
			name = g.Hash
		}
		out := filepath.Join(dir, safeFilename(name)+extFor(g.MIME))
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return err
		}
		written++
	}
	fmt.Printf("exported %s images to %s\n", ui.FormatCount(written), dir)
	return nil
}

func extFor(mime string) string {
	switch {
	case strings.Contains(mime, "png"):
		return ".png"
	case strings.Contains(mime, "gif"):
		return ".gif"
	case strings.Contains(mime, "webp"):
		return ".webp"
	}
	return ".jpg"
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

// reportFailures prints the errors a batch collected, which the server caps.
func reportFailures(res library.BatchResult) {
	if res.Failed == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "%s tracks failed:\n", ui.FormatCount(res.Failed))
	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "  %s: %s\n", filepath.Base(e.Path), e.Error)
	}
	if res.Failed > len(res.Errors) {
		fmt.Fprintf(os.Stderr, "  ...and %s more\n", ui.FormatCount(res.Failed-len(res.Errors)))
	}
}

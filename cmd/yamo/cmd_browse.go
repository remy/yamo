package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/remy/yamo/internal/ui"
)

const browseSummary = `yamo browse - browse and edit the catalogue

Usage:
  yamo [flags]
  yamo browse [flags]

Opens the full-screen browser against a running server. This is what
yamo does with no command.

Press ? inside it for the full key list; the essentials are:

  /            search, updating as you type
  space v a    mark a track, a range, everything matching
  e            edit the marked tracks, or the one under the cursor
  tab enter    move between fields, edit the focused one
  ^s           write every change back to disk
  u  ^r        undo and redo
  y  p         copy and paste cover art
  R            refresh after edits have made the view stale
  q            quit

Edits are held here until ^s, which is where undo lives. Saving writes only
the fields you changed and leaves every other tag in the file as it was.

Marking everything with "a" selects by query rather than by listing tracks,
so it costs the same whether it matches ten or a hundred thousand.
`

func cmdBrowse(args []string) error {
	fs := flag.NewFlagSet("browse", flag.ContinueOnError)
	server, token := serverFlags(fs)
	if err := parseFlags(fs, args, browseSummary, queryHelp); err != nil {
		return err
	}

	c, err := connect(*server, *token)
	if err != nil {
		return err
	}

	// Fetch the roots up front: it doubles as the connection check, so a
	// missing server is reported plainly rather than as an empty screen.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := c.Stats(ctx)
	if err != nil {
		return err
	}
	if st.Tracks == 0 {
		fmt.Fprintln(os.Stderr, "the library is empty — run: yamo scan <music-dir>")
	}
	return ui.Run(c, st.Roots)
}

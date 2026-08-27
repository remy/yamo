package main

import (
	"flag"
	"fmt"

	"github.com/remy/tag-manager/internal/catalog"
	"github.com/remy/tag-manager/internal/ui"
)

const browseSummary = `tagmgr browse - browse and edit the catalogue

Usage:
  tagmgr [flags]
  tagmgr browse [flags]

Opens the full-screen browser. This is what tagmgr does with no command.
Press ? inside it for the full key list; the essentials are:

  /            search, updating as you type
  space v a    mark a track, a range, everything matching
  e            edit the marked tracks, or the one under the cursor
  tab enter    move between fields, edit the focused one
  ^s           write every change back to disk
  u  ^r        undo and redo
  q            quit

Nothing touches disk until ^s. Saving writes only the fields you changed and
leaves every other tag in the file as it was.
`

func cmdBrowse(args []string) error {
	fs := flag.NewFlagSet("browse", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	if err := parseFlags(fs, args, browseSummary, queryHelp); err != nil {
		return err
	}

	c, err := catalog.Load(*catalogPath)
	if err != nil {
		return fmt.Errorf("no catalogue at %s\n       run: tagmgr scan <music-dir>", *catalogPath)
	}
	// Build the search index up front so the first keystroke is not the thing
	// that pays for it.
	c.Index()
	return ui.Run(c, *catalogPath)
}

package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/remy/tag-manager/internal/catalog"
	"github.com/remy/tag-manager/internal/client"
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
	server, token := serverFlags(fs)
	if err := parseFlags(fs, args, browseSummary, queryHelp); err != nil {
		return err
	}

	// The browser still opens the catalogue itself; it has not been moved onto
	// the API yet. That is safe on its own and unsafe alongside a server, which
	// believes it is the only thing writing to these files — so refuse rather
	// than let two processes edit the same library from separate copies of it.
	if addr, running := serverIsRunning(*server, *token); running {
		return fmt.Errorf("a tagmgr server is running at %s\n"+
			"       the browser does not talk to it yet, and running both would have\n"+
			"       two processes editing the same files from separate copies of the\n"+
			"       catalogue. Stop the server, or use the API in the meantime:\n"+
			"         tagmgr find, tagmgr art, tagmgr strip", addr)
	}

	c, err := catalog.Load(*catalogPath)
	if err != nil {
		return fmt.Errorf("no catalogue at %s\n       run: tagmgr serve, then tagmgr scan <music-dir>", *catalogPath)
	}
	// Build the search index up front so the first keystroke is not the thing
	// that pays for it.
	c.Index()
	return ui.Run(c, *catalogPath)
}

// serverIsRunning reports whether a server answers at the configured address.
func serverIsRunning(server, token string) (string, bool) {
	c, err := client.FromEnv(server, token)
	if err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := c.Stats(ctx); err != nil {
		return "", false
	}
	addr := server
	if addr == "" {
		addr = client.DefaultServer
	}
	return addr, true
}

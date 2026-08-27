package main

import (
	"flag"
	"fmt"

	"github.com/remy/tag-manager/internal/catalog"
	"github.com/remy/tag-manager/internal/ui"
)

func cmdBrowse(args []string) error {
	fs := flag.NewFlagSet("browse", flag.ContinueOnError)
	catalogPath, _ := addCommonFlags(fs)
	help := fs.Bool("help", false, "show usage")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *help {
		printUsage()
		return nil
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

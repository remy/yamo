// Command tagmgr catalogues and edits music metadata from the terminal.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

const rootUsage = `tagmgr - terminal music metadata manager

Usage:
  tagmgr [flags]                 browse and edit the catalogue (default)
  tagmgr scan [flags] <dir>...   build or refresh the catalogue
  tagmgr find [flags] <query>    print matching tracks and exit
  tagmgr info [flags]            show catalogue statistics
  tagmgr help [command]          usage for a command

Run "tagmgr help <command>" for the flags a command accepts.

Getting started:
  tagmgr scan /volume1/music     catalogue a library
  tagmgr                         browse it; press ? for keys

The catalogue defaults to
  %s
Override it with -catalog or by setting TAGMGR_CATALOG.
`

// queryHelp documents the search language, which is identical in the search
// bar and on the command line.
const queryHelp = `Query syntax:
  elvis                     any text field contains "elvis"
  artist:elvis              restrict a term to one field
  artist:"elvis presley"    quoted values may contain spaces
  year:1977                 exact match on a numeric field
  year:>1980  year:<=1969   comparisons
  year:1970-1979            an inclusive range
  -genre:christmas          exclude matches
  album:                    the field is empty
  artist:elvis year:>1960   terms are combined with AND

Fields: title, artist, albumartist, album, genre, composer, comment,
year, track, disc, path. Most have short aliases (ar, al, g, y).

Matching ignores case and accents, so "bjork" finds "Björk".

A query beginning with "-" is otherwise read as a flag, so pass "--" first:
  tagmgr find -- -genre:live artist:elvis
`

// errHelpRequested unwinds a help request back to main. Asking how to use a
// program is not a usage error, so it must not produce a non-zero exit.
var errHelpRequested = errors.New("help requested")

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, errHelpRequested) {
			return
		}
		fmt.Fprintln(os.Stderr, "tagmgr:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	// A bare help flag with no command asks about the program, not about the
	// browser that happens to be the default command.
	if cmd == "" && len(args) > 0 && isHelpFlag(args[0]) {
		printRootUsage()
		return nil
	}

	switch cmd {
	case "scan":
		return cmdScan(args)
	case "find", "search":
		return cmdFind(args)
	case "info":
		return cmdInfo(args)
	case "", "browse", "tui":
		return cmdBrowse(args)
	case "help":
		return cmdHelp(args)
	default:
		return fmt.Errorf("unknown command %q (try: tagmgr help)", cmd)
	}
}

func isHelpFlag(s string) bool {
	switch s {
	case "-h", "-help", "--help", "-?":
		return true
	}
	return false
}

// cmdHelp prints usage for the program or for one command.
func cmdHelp(args []string) error {
	if len(args) == 0 {
		printRootUsage()
		return nil
	}
	// Re-dispatch with a help flag so each command owns its own usage text.
	switch args[0] {
	case "scan":
		return cmdScan([]string{"-h"})
	case "find", "search":
		return cmdFind([]string{"-h"})
	case "info":
		return cmdInfo([]string{"-h"})
	case "browse", "tui":
		return cmdBrowse([]string{"-h"})
	}
	return fmt.Errorf("unknown command %q (try: tagmgr help)", args[0])
}

func printRootUsage() {
	fmt.Printf(rootUsage, defaultCatalogPath())
}

// parseFlags wires a command's usage text to its flag set and parses args.
//
// The flag package's own reporting is suppressed for two reasons. Its default
// usage prints "Usage of <name>:" followed by bare flag defaults, which says
// nothing about what the command is for; and on a bad flag it writes the error
// itself, which main would then report a second time.
//
// Requested usage goes to stdout, because it is what the user asked for.
// Errors go to stderr via main.
func parseFlags(fs *flag.FlagSet, args []string, summary, footer string) error {
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			writeUsage(os.Stdout, fs, summary, footer)
			return errHelpRequested
		}
		return fmt.Errorf("%w (try: tagmgr help %s)", err, fs.Name())
	}
	return nil
}

// writeUsage prints a command's summary, its flags, and any trailing notes.
func writeUsage(w io.Writer, fs *flag.FlagSet, summary, footer string) {
	fmt.Fprint(w, summary)
	fmt.Fprint(w, "\nFlags:\n")
	fs.SetOutput(w)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
	if footer != "" {
		fmt.Fprint(w, "\n"+footer)
	}
}

// defaultCatalogPath puts the catalogue in the user cache directory, which is
// the right place for something wholly derived from files elsewhere.
func defaultCatalogPath() string {
	if p := os.Getenv("TAGMGR_CATALOG"); p != "" {
		return p
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".cache")
		} else {
			return "tagmgr-catalog.db"
		}
	}
	return filepath.Join(dir, "tagmgr", "catalog.db")
}

// notifyContext returns a context cancelled by SIGINT or SIGTERM, so a long
// scan can be stopped without losing what it has already found.
func notifyContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// catalogFlag registers the one flag every command shares.
func catalogFlag(fs *flag.FlagSet) *string {
	return fs.String("catalog", defaultCatalogPath(), "catalogue file path")
}

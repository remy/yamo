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
  tagmgr serve [flags]           run the backend that owns the library
  tagmgr [flags]                 browse and edit the catalogue (default)
  tagmgr scan [flags] <dir>...   build or refresh the catalogue
  tagmgr find [flags] <query>    print matching tracks and exit
  tagmgr art [flags] [query]     inspect, copy and replace cover art
  tagmgr strip [flags] [query]   remove every tag except a fixed set
  tagmgr restore [flags]         put stripped tags back from a backup
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
  artist:^elvis             the field begins with it
  artist:presley$           the field ends with it
  artist:"^elvis presley$"  the whole field, exactly
  artist:~presly            fuzzy: near misses count, and are scored
  year:1977                 exact match on a numeric field
  year:>1980  year:<=1969   comparisons
  year:1970-1979            an inclusive range
  -genre:christmas          exclude matches
  album:                    the field is empty
  compilation:1             the Various Artists flag is set
  albumartistsort:various   the sort fields, which a bare term skips
  artist:elvis year:>1960   terms are combined with AND

Fields: title, artist, albumartist, album, genre, composer, comment,
year, track, disc, compilation, path, and the sort forms titlesort,
artistsort, albumsort, albumartistsort, composersort. Most have short
aliases (ar, al, g, y, comp, aas).

A bare term searches the display fields only; the sort fields and path
are reachable by name.

Matching ignores case and accents, so "bjork" finds "Björk".

Fuzzy terms:
  ~ loosens one term. It matches the value literally, or spread through
  the field in order ("~elvpres" finds Elvis Presley), or within a few
  typos ("~presly", "~prelsey"). Every other term stays exact, so
  "artist:~presly year:>1960" is strict about the year and forgiving
  about the artist. Results come back best first, and -sort overrides
  that like any other order. ~ and the anchors combine: artist:~^presly.

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
	case "serve":
		return cmdServe(args)
	case "art", "cover":
		return cmdArt(args)
	case "strip":
		return cmdStrip(args)
	case "restore":
		return cmdRestore(args)
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
	case "serve":
		return cmdServe([]string{"-h"})
	case "art", "cover":
		return cmdArt([]string{"-h"})
	case "strip":
		return cmdStrip([]string{"-h"})
	case "restore":
		return cmdRestore([]string{"-h"})
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

	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			writeUsage(os.Stdout, fs, summary, footer)
			return errHelpRequested
		}
		return fmt.Errorf("%w (try: tagmgr help %s)", err, fs.Name())
	}
	return nil
}

// reorderArgs moves flags ahead of positional arguments.
//
// Go's flag package stops parsing at the first non-flag argument. Every
// command here takes a query, and writing the flag after it is the natural
// way to type one — but "tagmgr strip artist:elvis -apply" would then treat
// -apply as part of the query and silently do nothing. For a command that
// writes to a hundred thousand files, silently doing nothing is the good
// outcome; the bad one is a flag that was meant to make an operation safer
// being dropped just as quietly.
//
// The positionals are emitted after a "--" so that a query beginning with a
// dash, such as -genre:live, is not then mistaken for a flag in turn.
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			// A non-boolean flag written as "-name value" owns the argument
			// after it, which has to travel with it.
			if flagTakesValue(fs, a) && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	if len(positional) == 0 {
		return flags
	}
	return append(append(flags, "--"), positional...)
}

// flagTakesValue reports whether a flag consumes the following argument.
func flagTakesValue(fs *flag.FlagSet, arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if strings.ContainsRune(name, '=') {
		return false // the value is attached
	}
	f := fs.Lookup(name)
	if f == nil {
		return false // unknown; let Parse report it rather than guessing
	}
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return !ok || !bf.IsBoolFlag()
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
//
// It returns empty when there is nowhere obvious — a daemon started without
// HOME or XDG_CACHE_HOME, which is the normal case under systemd. Falling back
// to a relative path would put the catalogue in whatever the working directory
// happened to be, or fail to write and quietly rescan on every restart. The
// server refuses instead and asks for -catalog.
func defaultCatalogPath() string {
	if p := os.Getenv("TAGMGR_CATALOG"); p != "" {
		return p
	}
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "tagmgr", "catalog.db")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cache", "tagmgr", "catalog.db")
	}
	return ""
}

// notifyContext returns a context cancelled by SIGINT or SIGTERM, so a long
// scan can be stopped without losing what it has already found.
func notifyContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// catalogFlag registers the catalogue path, which only the server uses now:
// it alone opens the catalogue and the music files.
func catalogFlag(fs *flag.FlagSet) *string {
	return fs.String("catalog", defaultCatalogPath(), "catalogue file path (server only)")
}

// formatCount renders a large number with thousands separators.
func formatCount(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

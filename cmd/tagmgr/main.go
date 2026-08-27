// Command tagmgr catalogues and edits music metadata from the terminal.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

const usage = `tagmgr - terminal music metadata manager

Usage:
  tagmgr scan [flags] <dir>...   build or refresh the catalogue
  tagmgr [flags]                 open the browser (default)
  tagmgr find [flags] <query>    print matching tracks and exit
  tagmgr info                    show catalogue statistics

Common flags:
  -catalog <path>   catalogue file (default %s)
  -workers <n>      tag reader concurrency (default %d)
  -help             this message

Scan flags:
  -full             re-read every file instead of reusing unchanged entries
  -exclude <pat>    skip paths matching a glob; repeatable
  -hidden           include dot-directories
  -follow           follow directory symlinks

Query syntax:
  elvis                     any text field contains "elvis"
  artist:elvis              a specific field
  artist:"elvis presley"    quoted values may contain spaces
  year:1977  year:>1980  year:1970-1979
  -genre:christmas          exclude matches
  album:                    the field is empty
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tagmgr:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "scan":
		return cmdScan(args)
	case "find":
		return cmdFind(args)
	case "info":
		return cmdInfo(args)
	case "", "browse", "tui":
		return cmdBrowse(args)
	case "help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q (try: tagmgr help)", cmd)
	}
}

func printUsage() {
	fmt.Printf(usage, defaultCatalogPath(), defaultWorkers())
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

// addCommonFlags registers the flags every subcommand accepts.
func addCommonFlags(fs *flag.FlagSet) (catalogPath *string, workers *int) {
	catalogPath = fs.String("catalog", defaultCatalogPath(), "catalogue file path")
	workers = fs.Int("workers", 0, "tag reader concurrency (0 = auto)")
	return
}

package main

import (
	"flag"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

const versionSummary = `yamo version - print the version of this binary and exit

Usage:
  yamo version [flags]

Prints the release, the commit it was built from, the platform and the Go
toolchain. A pre-built binary from a release is stamped with its tag; a
binary you built yourself reports the commit, or "dev" when there is nothing
to go on.

The -short form is the one to parse:

  yamo version -short          # 1.0.0
`

// version and commit are stamped at link time by the Makefile and by the
// release workflow (-ldflags "-X main.version=..."). They are deliberately
// the only mutable build-time state in the program: everything else about a
// build is derivable from the source.
//
// They are left empty rather than defaulted so that buildVersion can tell an
// unstamped build from a stamped one and fall back to the module metadata the
// Go toolchain embeds on its own. That matters for "go install
// github.com/remy/yamo/cmd/yamo@v1.0.0", which never runs the Makefile and
// would otherwise report itself as a development build.
var (
	version = ""
	commit  = ""
)

func cmdVersion(args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	short := fs.Bool("short", false, "print only the version, with no commit or platform")
	if err := parseFlags(fs, args, versionSummary, ""); err != nil {
		return err
	}
	if *short {
		fmt.Println(buildVersion())
		return nil
	}
	fmt.Println(versionLine())
	return nil
}

// versionLine is the human-readable form: what it is, what it was built from,
// and what it runs on. The platform is worth printing because the archives
// are per-platform and an unpacked binary carries no clue which one it is.
func versionLine() string {
	s := "yamo " + buildVersion()
	if c := buildCommit(); c != "" {
		s += " (" + c + ")"
	}
	return fmt.Sprintf("%s %s/%s %s", s, runtime.GOOS, runtime.GOARCH, runtime.Version())
}

// buildVersion prefers the stamped tag, then the module version the toolchain
// records for a binary installed with "go install ...@version", and settles
// for "dev".
func buildVersion() string {
	if version != "" {
		return strings.TrimPrefix(version, "v")
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; isReleaseVersion(v) {
			return strings.TrimPrefix(v, "v")
		}
	}
	return "dev"
}

// isReleaseVersion reports whether a module version is a tag someone actually
// published, as opposed to something the toolchain invented.
//
// This exists because debug.ReadBuildInfo does not simply go quiet for a local
// build. It reports "(devel)" in some cases, and in others synthesises a
// pseudo-version from the commit — a plain "go build" in this checkout yields
// "0.0.0-20260922210735-a1a25e9e27de+dirty". Printing that would claim a
// release that does not exist, and it is less informative than "dev" plus the
// commit, which is what the fallback gives. So a version is trusted only when
// it is tag-shaped: a "v" prefix, no "+incompatible"-style build metadata, and
// none of the 14-digit timestamps that mark a pseudo-version.
func isReleaseVersion(v string) bool {
	if !strings.HasPrefix(v, "v") || strings.Contains(v, "+") {
		return false
	}
	for _, part := range strings.Split(v, "-") {
		if len(part) == 14 && strings.IndexFunc(part, func(r rune) bool {
			return r < '0' || r > '9'
		}) < 0 {
			return false
		}
	}
	return true
}

// buildCommit prefers the stamped commit and otherwise reads the revision the
// toolchain stamps into any build made inside a git checkout, which is how an
// unstamped "go build" still produces something traceable.
func buildCommit() string {
	if commit != "" {
		return shortCommit(commit)
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var rev, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev == "" {
		return ""
	}
	return shortCommit(rev) + dirty
}

// shortCommit abbreviates a full object name and leaves anything else alone,
// so a value that has already been shortened — or that carries a "-dirty"
// suffix — survives intact.
func shortCommit(c string) string {
	if len(c) == 40 && strings.IndexFunc(c, func(r rune) bool {
		return !strings.ContainsRune("0123456789abcdef", r)
	}) < 0 {
		return c[:7]
	}
	return c
}

func isVersionFlag(s string) bool {
	switch s {
	case "-version", "--version", "-V":
		return true
	}
	return false
}

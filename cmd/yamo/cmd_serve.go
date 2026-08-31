package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/remy/yamo/internal/api"
	"github.com/remy/yamo/internal/library"
)

const serveSummary = `yamo serve - run the API server

Usage:
  yamo serve [flags]

Starts the server that owns the catalogue and the music files. Every other
command, and the browser, talks to it over HTTP; nothing else opens a
music file directly.

The API is described by an OpenAPI schema the server serves itself, so a
client can be generated rather than written:

  curl -O http://127.0.0.1:8467/openapi.yaml

and browsed at /docs, which works with no outbound network access.

The catalogue is a single file the server rebuilds from a scan. It goes in
the user cache directory by default, since it is wholly derived from the
music; -catalog or YAMO_CATALOG puts it elsewhere. Under systemd there is
often no HOME, and then -catalog is required rather than guessed at.

Scanning on startup:
  -root points at a music directory to scan when the server comes up
  (repeatable, so several libraries can be given at once), or set YAMO_ROOT
  to a comma-separated list — the form a container's environment can hold.
  With no -root and an empty catalogue there is nothing to scan yet; ask for
  one over the API (POST /v1/scan) or with "yamo scan" once the server is up.

  The scan runs as a background job; the server starts accepting requests
  immediately rather than waiting for it. It is incremental like any other
  scan, so restarting a long-running container with -root set is cheap: a
  library with nothing changed costs a stat per file, not a re-read.

Keeping up with the files:
  Nothing watches the filesystem. A library changed by something other than
  this server — an album copied in over SMB, tags edited elsewhere — is only
  noticed when a scan is asked for.

  -rescan-every, or YAMO_RESCAN_EVERY, rescans the catalogue's roots on a
  timer: -rescan-every 1h, or 0 (the default) to leave it off. It is the same
  incremental scan, so the cost on an unchanged library is a stat per file;
  an hour is a reasonable starting point and a minute is the floor. A tick
  that lands while a scan is still running is skipped rather than queued.

  The interval and the time of the next one are reported by GET /v1/stats,
  and by "yamo info".

A web front end:
  Run serve from a directory containing index.html and it is served at the
  root alongside the API. Same origin, so the browser needs no CORS and
  therefore no token on loopback. -web points somewhere else, and -web ""
  turns it off.

Access:
  It binds to loopback by default, where no token is needed. Binding to
  anything else requires one: it is generated on first run, printed once,
  and kept in the config directory. Pass it back with -token or
  YAMO_TOKEN.

  Cross-origin browser requests are only allowed when a token is set. A
  server on loopback with no token and permissive headers could be driven
  by any web page you happened to visit, and this API rewrites music files.

Album art from Discogs:
  The browser's Get Info sheet can search Discogs for cover art. It needs no
  credentials: searching is open, and the server does the fetching because
  Discogs' image host sends no CORS header, so a page can display a cover but
  cannot read its bytes.

  The catch is the rate limit — 25 requests a minute per IP address, and a
  search costs one request plus one per candidate, because an unauthenticated
  search returns no images. Set -discogs-token or YAMO_DISCOGS_TOKEN and it
  becomes 60 a minute with covers in the search itself, one request a search.
  -no-discogs turns the lookup off, leaving the server making no outbound
  requests at all.

Examples:
  yamo serve                                just this machine
  yamo serve -root /volume1/music           ...and scan it on startup
  yamo serve -root /volume1/music -rescan-every 1h
                                            ...and keep it up to date
  yamo serve -web ./webapp                  ...and serve a front end from a directory
  yamo serve -listen 0.0.0.0:8467           reachable on the network
  yamo serve -listen unix:///tmp/yamo.sock
`

// DefaultListen is where the server binds unless told otherwise.
const DefaultListen = "127.0.0.1:8467"

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	listen := fs.String("listen", DefaultListen, "address to bind, or unix:///path/to.sock")
	token := fs.String("token", os.Getenv("YAMO_TOKEN"), "bearer token required for non-loopback binds")
	noAuth := fs.Bool("no-auth", false, "serve without a token even when not on loopback")
	saveEvery := fs.Duration("save-every", 5*time.Second, "how often to write the catalogue snapshot")
	web := fs.String("web", ".", "directory of a web front end to serve at / (ignored if it has no index.html)")
	discogsToken := fs.String("discogs-token", os.Getenv("YAMO_DISCOGS_TOKEN"), "optional Discogs token; raises the cover-lookup rate limit")
	noDiscogs := fs.Bool("no-discogs", false, "disable the Discogs cover lookup, so the server makes no outbound requests")
	rescanEvery := fs.Duration("rescan-every", 0, "rescan the roots on this interval (e.g. 1h); 0 never rescans")
	roots := stringList(splitEnvList("YAMO_ROOT"))
	fs.Var(&roots, "root", "directory to scan on startup (repeatable, or comma-separated in YAMO_ROOT)")
	if err := parseFlags(fs, args, serveSummary, ""); err != nil {
		return err
	}
	// The flag wins when given, so that a compose file's environment can be
	// overridden on the command line rather than edited.
	if !flagPassed(fs, "rescan-every") {
		d, err := envDuration("YAMO_RESCAN_EVERY")
		if err != nil {
			return err
		}
		*rescanEvery = d
	}
	// Below a minute the timer stops being a background chore and becomes the
	// server's main activity: a scan of a hundred thousand files is a stat per
	// file even when nothing has changed.
	if *rescanEvery > 0 && *rescanEvery < time.Minute {
		return fmt.Errorf("-rescan-every %s is too short; a minute is the shortest useful interval", *rescanEvery)
	}

	if *catalogPath == "" {
		return errors.New("could not work out where to keep the catalogue: " +
			"neither HOME nor XDG_CACHE_HOME is set, which is usual under systemd\n" +
			"       pass -catalog /path/to/catalog.db, or set YAMO_CATALOG")
	}
	// An absolute path so the startup line means something regardless of the
	// working directory the service was started from.
	if abs, err := filepath.Abs(*catalogPath); err == nil {
		*catalogPath = abs
	}

	svc, err := library.Open(library.Options{
		CatalogPath:    *catalogPath,
		SaveInterval:   *saveEvery,
		DiscogsToken:   *discogsToken,
		NoDiscogs:      *noDiscogs,
		RescanInterval: *rescanEvery,
	})
	if err != nil {
		return err
	}
	defer svc.Close()

	network, addr := parseListen(*listen)
	loopback := network == "unix" || isLoopback(addr)

	// A server anyone on the network can reach must be authenticated, because
	// this API can rewrite a hundred thousand files.
	if !loopback && *token == "" && !*noAuth {
		if *token, err = ensureToken(*catalogPath); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "\n  %s is not loopback, so a token is required.\n  token: %s\n\n", addr, *token)
	}
	if loopback && *token == "" {
		fmt.Fprintf(os.Stderr, "  bound to %s; no token needed from this machine\n", addr)
	}

	// Only serve a front end if the directory actually holds one, so running
	// the server from an arbitrary directory does not publish it.
	webRoot := ""
	if *web != "" {
		if abs, err := filepath.Abs(*web); err == nil {
			if _, err := os.Stat(filepath.Join(abs, "index.html")); err == nil {
				webRoot = abs
			}
		}
	}

	srv := api.New(svc, api.Options{
		Token: *token,
		// Only opened up when a token gates it; see the note above.
		AllowCrossOrigin: *token != "",
		WebRoot:          webRoot,
	})

	ln, err := listenOn(network, addr)
	if err != nil {
		return err
	}
	defer ln.Close()

	httpSrv := &http.Server{
		Handler: srv,
		// A scan of a hundred thousand files runs as a job, so no request
		// should ever be long — except the event streams, which is why there
		// is no write timeout.
		ReadHeaderTimeout: 10 * time.Second,
	}

	shown := addr
	if network == "unix" {
		shown = "unix:" + addr
	} else {
		shown = "http://" + addr
	}
	fmt.Fprintf(os.Stderr, "  catalogue: %s\n", *catalogPath)
	fmt.Fprintf(os.Stderr, "  yamo serving %s tracks on %s\n",
		formatCount(svc.Count("")), shown)
	if webRoot != "" {
		fmt.Fprintf(os.Stderr, "  web:  %s  (from %s)\n", shown, webRoot)
	}
	fmt.Fprintf(os.Stderr, "  docs: %s/docs\n", shown)

	// -root/YAMO_ROOT exists for unattended starts — a container has no one
	// around to run "yamo scan" by hand. The scan runs as a background job:
	// the server below accepts requests immediately rather than waiting for
	// it, the same as a scan asked for over the API. It is incremental, so
	// asking for it on every restart is cheap once the library is caught up.
	if len(roots) > 0 {
		job, err := svc.Scan(library.ScanRequest{Roots: roots})
		if err != nil {
			return fmt.Errorf("initial scan of %s: %w", strings.Join(roots, ", "), err)
		}
		fmt.Fprintf(os.Stderr, "  scanning: %s (job %s)\n", strings.Join(roots, ", "), job.ID)
	}
	if *rescanEvery > 0 {
		fmt.Fprintf(os.Stderr, "  rescan:   every %s\n", *rescanEvery)
	}

	ctx, stop := notifyContext()
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "\n  shutting down; writing the catalogue")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		return svc.Close()
	}
}

// parseListen splits a listen address into a network and an address.
func parseListen(s string) (network, addr string) {
	if rest, ok := strings.CutPrefix(s, "unix://"); ok {
		return "unix", rest
	}
	if rest, ok := strings.CutPrefix(s, "unix:"); ok {
		return "unix", rest
	}
	return "tcp", s
}

func listenOn(network, addr string) (net.Listener, error) {
	if network == "unix" {
		// A socket left behind by a killed process would otherwise make every
		// subsequent start fail with "address already in use".
		if err := removeStaleSocket(addr); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(addr), 0o755); err != nil {
			return nil, err
		}
	}
	return net.Listen(network, addr)
}

// removeStaleSocket deletes a socket file that nothing is listening on.
func removeStaleSocket(path string) error {
	if _, err := os.Stat(path); err != nil {
		return nil // nothing there
	}
	conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err == nil {
		conn.Close()
		return fmt.Errorf("something is already listening on %s", path)
	}
	return os.Remove(path)
}

// splitEnvList reads a comma-separated environment variable, which is the
// shape a container's environment can hold, unlike a repeated flag.
// Whitespace around each entry is trimmed and empty entries are dropped, so
// a trailing comma or stray space in compose YAML does not become a bogus
// root.
func splitEnvList(name string) []string {
	v := os.Getenv(name)
	if v == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// envDuration reads a duration from the environment. An unparseable value is
// an error rather than a silent zero: a typo in a compose file would otherwise
// turn the rescan timer off and look like the feature not working.
func envDuration(name string) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a duration (try 30m, 1h)", name, v)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s=%q is negative", name, v)
	}
	return d, nil
}

// flagPassed reports whether a flag was actually given on the command line,
// which is how an environment default can be applied without overwriting one.
func flagPassed(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// isLoopback reports whether a bind address only accepts local connections.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false // an empty host means every interface
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ensureToken loads the stored token, generating one on first use.
func ensureToken(catalogPath string) (string, error) {
	dir := filepath.Dir(catalogPath)
	if dir == "" || dir == "." {
		dir = "."
	}
	path := filepath.Join(dir, "token")

	if b, err := os.ReadFile(path); err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			return tok, nil
		}
	}
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw[:])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Readable only by the owner: it is the only thing standing between the
	// network and the library.
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

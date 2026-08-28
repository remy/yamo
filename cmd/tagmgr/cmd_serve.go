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

	"github.com/remy/tag-manager/internal/api"
	"github.com/remy/tag-manager/internal/library"
)

const serveSummary = `tagmgr serve - run the backend

Usage:
  tagmgr serve [flags]

Starts the server that owns the catalogue and the music files. Every other
command, and the browser, talks to it over HTTP; nothing else opens a
music file directly.

The API is described by an OpenAPI schema the server serves itself, so a
client can be generated rather than written:

  curl -O http://127.0.0.1:8467/openapi.yaml

and browsed at /docs, which works with no outbound network access.

Access:
  It binds to loopback by default, where no token is needed. Binding to
  anything else requires one: it is generated on first run, printed once,
  and kept in the config directory. Pass it back with -token or
  TAGMGR_TOKEN.

  Cross-origin browser requests are only allowed when a token is set. A
  server on loopback with no token and permissive headers could be driven
  by any web page you happened to visit, and this API rewrites music files.

Examples:
  tagmgr serve                                just this machine
  tagmgr serve -listen 0.0.0.0:8467           reachable on the network
  tagmgr serve -listen unix:///tmp/tagmgr.sock
`

// DefaultListen is where the server binds unless told otherwise.
const DefaultListen = "127.0.0.1:8467"

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	listen := fs.String("listen", DefaultListen, "address to bind, or unix:///path/to.sock")
	token := fs.String("token", os.Getenv("TAGMGR_TOKEN"), "bearer token required for non-loopback binds")
	noAuth := fs.Bool("no-auth", false, "serve without a token even when not on loopback")
	saveEvery := fs.Duration("save-every", 5*time.Second, "how often to write the catalogue snapshot")
	if err := parseFlags(fs, args, serveSummary, ""); err != nil {
		return err
	}

	svc, err := library.Open(library.Options{
		CatalogPath:  *catalogPath,
		SaveInterval: *saveEvery,
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

	srv := api.New(svc, api.Options{
		Token: *token,
		// Only opened up when a token gates it; see the note above.
		AllowCrossOrigin: *token != "",
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
	fmt.Fprintf(os.Stderr, "  tagmgr serving %s tracks on %s\n  docs: %s/docs\n",
		formatCount(svc.Count("")), shown, shown)

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

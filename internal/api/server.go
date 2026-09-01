// Package api serves the HTTP interface described by api/openapi.yaml.
//
// The handlers are written by hand rather than generated. OpenAPI 3.1 is what
// the clients this exists for want — a TypeScript or Swift generator handles it
// well — but the Go generators still trail the specification, and code
// generated from a dialect the generator half understands is worse than code
// written against a schema a test checks. api_test.go walks the YAML and
// asserts that every operation in it has a route and every route has an
// operation, which is the drift protection the generator would have provided.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/remy/yamo/internal/library"
	"github.com/remy/yamo/internal/tags"
)

// Options configures the HTTP server.
type Options struct {
	// Token, when set, must be presented as a bearer token on every /v1
	// request. The server requires one whenever it is not bound to loopback.
	Token string

	// AllowCrossOrigin permits browser requests from other origins.
	//
	// It is off unless a token is set, and that is deliberate. A server on
	// loopback with no token and permissive CORS could be driven by any web
	// page the user happens to visit, and this API rewrites music files.
	// Without the headers a browser refuses the cross-origin request itself.
	AllowCrossOrigin bool

	// WebRoot, when set, is a directory of static files served at the root.
	//
	// Serving the front end from the same origin as the API is not just a
	// convenience: same-origin requests need no CORS at all, so a browser
	// client works against a loopback server with no token, which is the
	// setup CORS is deliberately refused for.
	WebRoot string

	Logger *log.Logger
}

// Server is the HTTP handler for a library service.
type Server struct {
	svc  *library.Service
	opts Options
	mux  *http.ServeMux

	// patterns records every route registered, so a test can check it against
	// the schema. Go's ServeMux does not expose what it holds, and a route
	// that exists but is undocumented is as much a drift as the reverse.
	patterns []string
}

// New builds the server and registers every route.
func New(svc *library.Service, opts Options) *Server {
	s := &Server{svc: svc, opts: opts, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.opts.AllowCrossOrigin {
		s.setCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) setCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-Match, If-None-Match, Last-Event-ID")
	// A header a browser cannot read may as well not have been sent. The rate
	// limit ones are here so a page can pace its Discogs lookups the same way
	// a native client does, and Retry-After so it knows how long to wait.
	h.Set("Access-Control-Expose-Headers",
		"ETag, Location, Retry-After, RateLimit-Limit, RateLimit-Remaining, RateLimit-Reset")
}

// routes registers every operation. The paths mirror api/openapi.yaml exactly;
// the conformance test fails if they stop doing so.
func (s *Server) routes() {
	// What this build can do, and whether the caller is allowed to do it.
	// Capabilities is public: it describes the interface and holds nothing
	// about the library, and a client needs to know whether a token is
	// required before it can present one.
	s.handlePublic("GET /v1/capabilities", s.getCapabilities)
	s.handle("GET /v1/me", s.getMe)

	// Reading.
	s.handle("GET /v1/tracks", s.listTracks)
	s.handle("GET /v1/tracks/{id}", s.getTrack)
	s.handle("PATCH /v1/tracks/{id}", s.patchTrack)
	s.handle("GET /v1/tracks/{id}/tags", s.getRawTags)
	s.handle("GET /v1/albums", s.listAlbums)
	s.handle("GET /v1/artists", s.listArtists)
	s.handle("GET /v1/folders", s.listFolders)
	s.handle("GET /v1/duplicates", s.listDuplicates)
	s.handle("GET /v1/values/{field}", s.listValues)
	s.handle("GET /v1/stats", s.getStats)
	s.handle("GET /v1/tracks/{id}/audio", s.getAudio)

	// The files themselves. Everything else changes what is inside a file;
	// these two change which files there are.
	s.handle("DELETE /v1/tracks/{id}", s.deleteTrack)
	s.handle("POST /v1/tracks/{id}/rename", s.renameTrack)

	// Artwork.
	s.handle("GET /v1/tracks/{id}/artwork", s.getArtwork)
	s.handle("PUT /v1/tracks/{id}/artwork", s.putArtwork)
	s.handle("DELETE /v1/tracks/{id}/artwork", s.deleteArtwork)
	s.handle("GET /v1/artwork/summary", s.artworkSummary)
	s.handle("POST /v1/artwork/batch", s.batchArtwork)
	s.handle("POST /v1/artwork/export", s.exportArtwork)
	s.handle("GET /v1/clipboard/artwork", s.getClipboard)
	s.handle("PUT /v1/clipboard/artwork", s.putClipboard)
	s.handle("DELETE /v1/clipboard/artwork", s.deleteClipboard)
	s.handle("PUT /v1/clipboard/artwork/from-track/{id}", s.copyArtworkFromTrack)
	s.handle("PUT /v1/clipboard/artwork/from-url", s.copyArtworkFromURL)

	// Discogs cover lookup.
	s.handle("GET /v1/discogs/search", s.discogsSearch)
	s.handle("GET /v1/discogs/masters/{id}", s.discogsMaster)
	s.handle("GET /v1/discogs/album", s.discogsAlbum)

	// Batch and maintenance.
	s.handle("POST /v1/tracks/batch", s.batchEditTracks)
	s.handle("POST /v1/tracks/split", s.splitTracks)
	s.handle("POST /v1/tracks/rename", s.renameTracks)
	s.handle("POST /v1/strip", s.stripTags)
	s.handle("GET /v1/backups", s.listBackups)
	s.handle("GET /v1/backups/{id}", s.getBackup)
	s.handle("DELETE /v1/backups/{id}", s.deleteBackup)
	s.handle("POST /v1/restore", s.restoreBackup)
	s.handle("GET /v1/scans", s.getScanStatus)
	s.handle("POST /v1/scans", s.startScan)

	// Jobs and events.
	s.handle("GET /v1/jobs", s.listJobs)
	s.handle("GET /v1/jobs/{id}", s.getJob)
	s.handle("DELETE /v1/jobs/{id}", s.cancelJob)
	s.handle("POST /v1/jobs/{id}/undo", s.undoJob)
	s.handle("GET /v1/jobs/{id}/events", s.streamJobEvents)
	s.handle("GET /v1/events", s.streamEvents)

	// The schema and its viewer are served without a token: they describe the
	// interface and contain none of the library.
	s.mux.HandleFunc("GET /openapi.yaml", s.serveSpecYAML)
	s.mux.HandleFunc("GET /openapi.json", s.serveSpecJSON)
	s.mux.HandleFunc("GET /docs", s.serveDocs)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	// The front end, when there is one, is served unauthenticated: it has to
	// load before it can present a token, and it contains none of the library.
	if s.opts.WebRoot != "" {
		files := http.FileServer(http.Dir(s.opts.WebRoot))
		s.mux.Handle("GET /", files)
	} else {
		s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/docs", http.StatusFound)
		})
	}
}

// handle registers an authenticated route.
func (s *Server) handle(pattern string, fn http.HandlerFunc) {
	s.patterns = append(s.patterns, pattern)
	s.mux.HandleFunc(pattern, s.authenticate(fn))
}

// handlePublic registers a route served without a token.
//
// It is still recorded as a pattern, so the conformance test holds it to the
// schema exactly as it holds every other operation. The only thing that
// differs is the check, and it is only ever skipped for a response that says
// nothing about the library.
func (s *Server) handlePublic(pattern string, fn http.HandlerFunc) {
	s.patterns = append(s.patterns, pattern)
	s.mux.HandleFunc(pattern, fn)
}

// Routes returns the registered API patterns, for the conformance test.
func (s *Server) Routes() []string {
	out := make([]string, len(s.patterns))
	copy(out, s.patterns)
	return out
}

// authenticate enforces the bearer token when one is configured.
func (s *Server) authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.opts.Token == "" {
			next(w, r)
			return
		}
		const prefix = "Bearer "
		got := r.Header.Get("Authorization")
		if !strings.HasPrefix(got, prefix) || !constantTimeEqual(got[len(prefix):], s.opts.Token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="yamo"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "a bearer token is required")
			return
		}
		next(w, r)
	}
}

// constantTimeEqual compares without leaking length or position through timing.
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// --- responses ----------------------------------------------------------

type apiError struct {
	Error    string `json:"error"`
	Code     string `json:"code"`
	Expected *int   `json:"expected,omitempty"`
	Actual   *int   `json:"actual,omitempty"`
	JobID    string `json:"jobId,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: msg, Code: code})
}

// fail maps a service error onto the right status and code.
//
// The mapping is the API's contract for failure, so it lives in one place
// rather than being decided again in every handler.
func fail(w http.ResponseWriter, err error) {
	var mismatch *library.CountMismatchError
	var scanning *library.ScanRunningError
	switch {
	case errors.As(err, &scanning):
		writeJSON(w, http.StatusConflict, apiError{
			Error: scanning.Error(), Code: "scan_running", JobID: scanning.JobID,
		})
	case errors.Is(err, tags.ErrNoPicture):
		// Asking for artwork a track does not have is a missing resource, not
		// a server fault.
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, tags.ErrUnsupported), errors.Is(err, tags.ErrMalformed):
		// A file this library cannot write, or one that is not shaped like the
		// container it claims to be. Both are properties of the resource, so
		// neither is a 500: retrying will not help.
		writeError(w, http.StatusUnprocessableEntity, "unwritable", err.Error())
	case errors.Is(err, library.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, library.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, library.ErrExists):
		// Also a 409, but a distinct code: the client's answer is to choose
		// another name rather than to re-read and retry.
		writeError(w, http.StatusConflict, "exists", err.Error())
	case errors.Is(err, library.ErrBadPath), errors.Is(err, library.ErrBadRequest),
		errors.Is(err, library.ErrBadTemplate):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.As(err, &mismatch):
		writeJSON(w, http.StatusConflict, apiError{
			Error: mismatch.Error(), Code: "count_mismatch",
			Expected: &mismatch.Expected, Actual: &mismatch.Actual,
		})
	case strings.Contains(err.Error(), "cannot be written"):
		writeError(w, http.StatusUnprocessableEntity, "unwritable", err.Error())
	case errors.Is(err, library.ErrSelectorEmpty),
		strings.Contains(err.Error(), "unknown field"),
		strings.Contains(err.Error(), "unknown tag"),
		strings.Contains(err.Error(), "cannot be set"),
		strings.Contains(err.Error(), "no changes given"),
		strings.Contains(err.Error(), "keep list is empty"),
		strings.Contains(err.Error(), "no directories to scan"),
		strings.Contains(err.Error(), "unknown artwork source"):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// decodeJSON reads a request body, rejecting unknown fields so that a
// misspelled key fails loudly instead of being silently ignored.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad request body: %w", err)
	}
	return nil
}

func intParam(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func listParams(r *http.Request) library.ListParams {
	return library.ListParams{
		Query:  r.URL.Query().Get("q"),
		Sort:   r.URL.Query().Get("sort"),
		Limit:  intParam(r, "limit", library.DefaultLimit),
		Offset: intParam(r, "offset", 0),
	}
}

// selectorFromQuery builds a selector from the query string, for the reads
// that report on a selection rather than changing it.
//
// A GET cannot carry a body, so the same selection a POST expresses as JSON is
// expressed here as parameters. `expectCount` is deliberately not among them:
// it is a safety rail for a change, and refusing to answer a question because
// the count moved would be nothing but an obstacle.
func selectorFromQuery(r *http.Request) library.Selector {
	q := r.URL.Query()
	sel := library.Selector{
		Query:      q.Get("q"),
		IDs:        listParam(q["ids"]),
		ExcludeIDs: listParam(q["excludeIds"]),
		All:        q.Get("all") == "1" || q.Get("all") == "true",
	}
	// A read with nothing said about the selection means the whole library,
	// which is what /artwork/summary has always done with an empty q. The
	// explicit `all` a write requires exists so "everything" cannot be
	// reached by accident, and nothing here can be destroyed by accident.
	if len(sel.IDs) == 0 && sel.Query == "" && !sel.All {
		sel.All = true
	}
	return sel
}

// listParam accepts a repeated parameter or one comma-separated value, since
// a client building a URL by hand does one and a form does the other.
func listParam(values []string) []string {
	var out []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// notModified answers a conditional request when the client already holds the
// current version.
//
// Covers are the reason this exists: an album grid re-requests the same forty
// images every time it is opened, and each one is a few hundred kilobytes that
// have not changed since the last time. The ETag is the track's version, so a
// tag edit invalidates it and nothing else does.
func notModified(w http.ResponseWriter, r *http.Request, etag string) bool {
	if !matchesETag(r.Header.Get("If-None-Match"), etag) {
		return false
	}
	// The header goes out with the 304 as well: a cache is entitled to update
	// what it holds from it, and leaving it off would make the next request
	// unconditional again.
	w.Header().Set("ETag", strconv.Quote(etag))
	w.WriteHeader(http.StatusNotModified)
	return true
}

// matchesETag compares an If-None-Match header against a version.
//
// The header may hold several values, may quote them, and may be "*", which
// matches anything that exists. A weak validator prefix is accepted and
// ignored: this server issues only strong ones, but a proxy between here and
// the client may have weakened it in passing, and refusing to recognise our
// own tag coming back would defeat the caching entirely.
func matchesETag(header, etag string) bool {
	if header == "" || etag == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" {
			return true
		}
		part = strings.TrimPrefix(part, "W/")
		if unquote(part) == etag {
			return true
		}
	}
	return false
}

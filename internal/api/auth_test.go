package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These run against a server with no service behind it. Everything here is
// decided before a handler is reached — that is the whole point of it — and
// the one route exercised on the way through, /v1/me, answers from the request
// alone. It keeps the checks that are about credentials free of the ffmpeg
// fixture the rest of this package needs.
func authServer() *Server {
	return New(nil, Options{Token: "full-token", ReadOnlyToken: "read-token"})
}

func request(t *testing.T, s *Server, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func scopesOf(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var id identity
	if err := json.Unmarshal(rec.Body.Bytes(), &id); err != nil {
		t.Fatalf("/v1/me does not decode: %v (%s)", err, rec.Body.String())
	}
	return id.Scopes
}

// X-Api-Key is accepted because a good deal of software that would make a fine
// client can set an arbitrary header and cannot construct an Authorization one.
func TestTokenIsAcceptedFromEitherHeader(t *testing.T) {
	s := authServer()

	for _, h := range []map[string]string{
		{"Authorization": "Bearer full-token"},
		{"X-Api-Key": "full-token"},
	} {
		if rec := request(t, s, http.MethodGet, "/v1/me", h); rec.Code != http.StatusOK {
			t.Errorf("%v = %d, want 200\n%s", h, rec.Code, rec.Body.String())
		}
	}

	for _, h := range []map[string]string{
		nil,
		{"X-Api-Key": "wrong"},
		{"Authorization": "Bearer wrong"},
		{"Authorization": "full-token"}, // no scheme
	} {
		if rec := request(t, s, http.MethodGet, "/v1/me", h); rec.Code != http.StatusUnauthorized {
			t.Errorf("%v = %d, want 401", h, rec.Code)
		}
	}
}

// A header a proxy added must not quietly replace one the client chose.
func TestAuthorizationWinsOverApiKey(t *testing.T) {
	s := authServer()
	rec := request(t, s, http.MethodGet, "/v1/me", map[string]string{
		"Authorization": "Bearer full-token",
		"X-Api-Key":     "read-token",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := scopesOf(t, rec); len(got) != 2 {
		t.Errorf("scopes %v, expected the Authorization header to decide", got)
	}
}

// The scopes on /v1/me are how a client finds out what it is holding without
// attempting a write and reading the failure.
func TestMeReportsTheRole(t *testing.T) {
	s := authServer()

	full := scopesOf(t, request(t, s, http.MethodGet, "/v1/me", map[string]string{"X-Api-Key": "full-token"}))
	if strings.Join(full, ",") != "read,write" {
		t.Errorf("full token reports %v", full)
	}
	limited := scopesOf(t, request(t, s, http.MethodGet, "/v1/me", map[string]string{"X-Api-Key": "read-token"}))
	if strings.Join(limited, ",") != "read" {
		t.Errorf("read-only token reports %v", limited)
	}
}

// The read-only rule is "GET and nothing else", and it is only as good as this
// API keeping its writes out of GET. Walking the registered routes is what
// holds it: a write that slipped through would be reachable here.
func TestReadOnlyTokenIsRefusedOnEveryWrite(t *testing.T) {
	s := authServer()
	header := map[string]string{"X-Api-Key": "read-token"}

	writes := 0
	for _, pattern := range s.Routes() {
		method, path, ok := strings.Cut(pattern, " ")
		if !ok || method == http.MethodGet {
			continue
		}
		writes++
		// The handler is never reached, so a placeholder id is enough.
		path = strings.ReplaceAll(path, "{id}", "x")
		path = strings.ReplaceAll(path, "{field}", "artist")

		rec := request(t, s, method, path, header)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", pattern, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), "read_only") {
			t.Errorf("%s: no read_only code in %s", pattern, rec.Body.String())
		}
	}
	if writes == 0 {
		t.Fatal("no writing routes were found, so this checked nothing")
	}
}

// reaches reports whether the wrapper let a request through to its handler.
// It exercises the middleware rather than a route, because what is under test
// is what happens before a handler — and with no service behind this server,
// reaching one is a panic rather than a status.
func reaches(t *testing.T, s *Server, method, token string) bool {
	t.Helper()
	got := false
	h := s.authenticate(func(w http.ResponseWriter, r *http.Request) {
		got = true
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(method, "/v1/anything", nil)
	if token != "" {
		req.Header.Set("X-Api-Key", token)
	}
	h(httptest.NewRecorder(), req)
	return got
}

// Nothing changes for a server with one token, which is nearly every server.
func TestOneTokenStillGrantsEverything(t *testing.T) {
	s := New(nil, Options{Token: "only-token"})
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		if !reaches(t, s, method, "only-token") {
			t.Errorf("%s was refused; the only token grants everything", method)
		}
	}
	if reaches(t, s, http.MethodGet, "wrong") {
		t.Error("a wrong token was let through")
	}
}

// A read-only token has to actually read, or it is no use to anything.
func TestReadOnlyTokenStillReads(t *testing.T) {
	s := authServer()
	if !reaches(t, s, http.MethodGet, "read-token") {
		t.Error("a read-only token was refused a GET")
	}
	if reaches(t, s, http.MethodPost, "read-token") {
		t.Error("a read-only token was allowed a POST")
	}
}

// CORS has to name the header, or a browser will not send it.
func TestApiKeyHeaderIsAllowedCrossOrigin(t *testing.T) {
	s := New(nil, Options{Token: "t", AllowCrossOrigin: true})
	rec := request(t, s, http.MethodOptions, "/v1/tracks", nil)
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "X-Api-Key") {
		t.Errorf("Access-Control-Allow-Headers is %q", got)
	}
}

package discogs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testClient points a client at a local server, with the image allowlist
// widened to that server so the download path can be exercised at all.
//
// TLS rather than plain HTTP because FetchImage refuses anything but https,
// and a test that worked around that rule would not be testing the code that
// ships. httptest's client trusts the throwaway certificate.
func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)

	c := New("")
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.ImageHosts = map[string]bool{u.Hostname(): true}
	return c
}

// The search response this is parsed from is the real shape, empty image
// fields and all: that is what an unauthenticated Discogs search returns, and
// the whole two-step design exists because of it.
const searchJSON = `{"pagination":{"items":2},"results":[
 {"id":9,"master_id":265278,"type":"master","title":"Plan B (4) - The Defamation Of Strickland Banks",
  "year":"2010","country":"UK","format":["CD","Album","CD","Stereo"],
  "label":["679","Atlantic","679"],"thumb":"","cover_image":""},
 {"id":1838056,"master_id":0,"type":"master","title":"Something Else",
  "year":"2011","country":"DE","format":["Vinyl"],"label":[],"thumb":"","cover_image":""}
]}`

const masterJSON = `{"id":265278,"title":"The Defamation Of Strickland Banks","year":2010,
 "artists":[{"name":"Plan B"},{"name":"Someone Else"}],
 "images":[
  {"type":"secondary","uri":"https://HOST/back.jpg","uri150":"https://HOST/back150.jpg","width":600,"height":470},
  {"type":"primary","uri":"https://HOST/front.jpg","uri150":"https://HOST/front150.jpg","width":600,"height":600},
  {"type":"secondary","uri":"","uri150":""}
 ]}`

func TestSearchParsesMasters(t *testing.T) {
	var gotQuery url.Values
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		if ua := r.Header.Get("User-Agent"); ua == "" {
			t.Error("no User-Agent sent; Discogs answers 403 to that")
		}
		w.Write([]byte(searchJSON))
	})

	res, err := c.Search(context.Background(), "plan b", 8)
	if err != nil {
		t.Fatal(err)
	}
	// Masters, not releases: a release search returns the same sleeve once per
	// pressing, which is not a list anyone wants to choose from.
	if gotQuery.Get("type") != "master" {
		t.Errorf("type = %q, want master", gotQuery.Get("type"))
	}
	if gotQuery.Get("per_page") != "8" {
		t.Errorf("per_page = %q, want 8", gotQuery.Get("per_page"))
	}
	if len(res) != 2 {
		t.Fatalf("got %d results, want 2", len(res))
	}

	if res[0].MasterID != 265278 {
		t.Errorf("MasterID = %d, want 265278", res[0].MasterID)
	}
	// Discogs repeats labels and formats freely; a tile has room for neither.
	if got := strings.Join(res[0].Formats, ","); got != "CD,Album,Stereo" {
		t.Errorf("Formats = %q, want the duplicates dropped", got)
	}
	if res[0].Label != "679" {
		t.Errorf("Label = %q", res[0].Label)
	}
	// A hit with no master_id falls back to its own id, or it could never be
	// expanded.
	if res[1].MasterID != 1838056 {
		t.Errorf("fallback MasterID = %d, want the result's own id", res[1].MasterID)
	}
}

func TestMasterImagesAndPrimary(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(masterJSON))
	})

	m, err := c.MasterByID(context.Background(), 265278)
	if err != nil {
		t.Fatal(err)
	}
	// The image with no URI is useless as album art and must not become a
	// tile that fails to load.
	if len(m.Images) != 2 {
		t.Fatalf("got %d images, want the empty one dropped", len(m.Images))
	}
	if m.Artists != "Plan B, Someone Else" {
		t.Errorf("Artists = %q", m.Artists)
	}
	// Primary is not first in the payload, which is exactly why it is looked
	// up rather than indexed.
	p := m.Primary()
	if p == nil || !strings.Contains(p.URI, "front.jpg") {
		t.Errorf("Primary() = %+v, want the front cover", p)
	}
	if p.Thumb == "" {
		t.Error("the thumbnail was dropped; the grid lays out with it")
	}
}

// A master fetched twice must cost one request. The cache is a rate-limit
// measure: picking a cover means searching, expanding and applying, and
// without it that sequence pays for the same master three times over.
func TestMasterIsCached(t *testing.T) {
	var hits atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(masterJSON))
	})

	for i := 0; i < 3; i++ {
		if _, err := c.MasterByID(context.Background(), 265278); err != nil {
			t.Fatal(err)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("the server saw %d requests, want 1", n)
	}
	if _, rem := c.Budget(); rem != 24 {
		t.Errorf("budget remaining = %d, want 24 — the cache should spend nothing", rem)
	}
}

// The allowlist is the only thing standing between this endpoint and a
// request-forgery hole, since the URL comes from the client.
func TestFetchImageRejectsForeignHosts(t *testing.T) {
	c := New("")
	for _, raw := range []string{
		"http://169.254.169.254/latest/meta-data/",     // cloud metadata
		"http://127.0.0.1:8467/v1/tracks",              // the server itself
		"https://evil.example.com/x.jpg",               // plainly elsewhere
		"https://i.discogs.com.evil.example.com/x.jpg", // a lookalike suffix
		"https://notdiscogs.com/i.discogs.com/x.jpg",   // the host in the path
		"http://i.discogs.com/x.jpg",                   // right host, no TLS
		"file:///etc/passwd",
		"://nonsense",
	} {
		if _, _, err := c.FetchImage(context.Background(), raw); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("FetchImage(%q) error = %v, want ErrNotAllowed", raw, err)
		}
	}
}

func TestFetchImageReadsAndCaps(t *testing.T) {
	big := strings.Repeat("x", maxImageBytes+1)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte("\xff\xd8\xff\xe0 pretend jpeg"))
		case "/huge.jpg":
			w.Write([]byte(big))
		case "/empty.jpg":
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	data, mime, err := c.FetchImage(context.Background(), c.BaseURL+"/ok.jpg")
	if err != nil {
		t.Fatalf("ok.jpg: %v", err)
	}
	if len(data) == 0 || mime != "image/jpeg" {
		t.Errorf("got %d bytes, mime %q", len(data), mime)
	}

	// Oversized must fail rather than truncate: half an image written into
	// every track on an album is worse than no image.
	if _, _, err := c.FetchImage(context.Background(), c.BaseURL+"/huge.jpg"); err == nil {
		t.Error("an oversized image was accepted")
	}
	if _, _, err := c.FetchImage(context.Background(), c.BaseURL+"/empty.jpg"); err == nil {
		t.Error("an empty body was accepted")
	}
	if _, _, err := c.FetchImage(context.Background(), c.BaseURL+"/missing.jpg"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing image error = %v, want ErrNotFound", err)
	}
}

func TestNotFoundAndRateLimitStatuses(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "404") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
	})

	if _, err := c.MasterByID(context.Background(), 404); !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}

	var limited *RateLimitError
	err := errFrom(c.MasterByID(context.Background(), 7))
	if !errors.As(err, &limited) {
		t.Fatalf("error = %v, want a RateLimitError", err)
	}
	// The wait comes from the server, so the UI can say when rather than just
	// that something went wrong.
	if limited.RetryAfter != 17*time.Second {
		t.Errorf("RetryAfter = %v, want 17s", limited.RetryAfter)
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Error("a RateLimitError does not unwrap to ErrRateLimited")
	}
}

func errFrom(_ *Master, err error) error { return err }

// A token changes both the budget and whether search returns images, so both
// have to follow from it.
func TestTokenChangesBudgetAndAuth(t *testing.T) {
	plain, tokened := New(""), New("secret")
	if plain.Authenticated() {
		t.Error("an empty token reads as authenticated")
	}
	if !tokened.Authenticated() {
		t.Error("a token does not read as authenticated")
	}
	if l, _ := plain.Budget(); l != 25 {
		t.Errorf("unauthenticated limit = %d, want 25", l)
	}
	if l, _ := tokened.Budget(); l != 60 {
		t.Errorf("authenticated limit = %d, want 60", l)
	}

	var gotAuth string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(searchJSON))
	})
	c.token = "secret"
	if _, err := c.Search(context.Background(), "x", 5); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Discogs token=secret" {
		t.Errorf("Authorization = %q", gotAuth)
	}
}

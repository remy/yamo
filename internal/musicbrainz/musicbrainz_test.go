package musicbrainz

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const genesisJSON = `{"artists":[
 {"id":"8E3FCD7D-BDA1-4CA0-B987-B8528D2AD8FA","name":"Genesis","sort-name":"Genesis",
  "disambiguation":"English rock band","type":"Group","country":"GB","score":100,
  "area":{"name":"United Kingdom"},"life-span":{"begin":"1967","end":"2023"}},
 {"id":"a4fd2ba2-3a51-4b84-8b0d-e5d2a7c3ba3a","name":"Genesis","score":94,
  "disambiguation":"US hip hop artist"}
]}`

type fake struct {
	mu    sync.Mutex
	hits  []time.Time
	query []string
	agent []string
	code  int
}

func (f *fake) server(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits = append(f.hits, time.Now())
		f.query = append(f.query, r.URL.Query().Get("query"))
		f.agent = append(f.agent, r.Header.Get("User-Agent"))
		code := f.code
		f.mu.Unlock()
		if code != 0 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(genesisJSON))
	}))
	t.Cleanup(srv.Close)
	c := New()
	c.BaseURL = srv.URL
	return c
}

func TestSearchArtists(t *testing.T) {
	f := &fake{}
	c := f.server(t)

	got, err := c.SearchArtists(context.Background(), "Genesis", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d artists, want 2", len(got))
	}
	a := got[0]
	if a.ID != "8e3fcd7d-bda1-4ca0-b987-b8528d2ad8fa" {
		t.Errorf("id = %q; it should be lower-cased, the form the tags hold", a.ID)
	}
	if a.Disambiguation != "English rock band" || a.Area != "United Kingdom" ||
		a.Begin != "1967" || a.End != "2023" || a.Type != "Group" || a.Score != 100 {
		t.Errorf("first artist = %+v", a)
	}
	if f.agent[0] != UserAgent {
		t.Errorf("User-Agent = %q; MusicBrainz blocks clients that do not identify themselves", f.agent[0])
	}

	// The same search again is the cache's, not the network's.
	if _, err := c.SearchArtists(context.Background(), "genesis", 10); err != nil {
		t.Fatal(err)
	}
	if len(f.hits) != 1 {
		t.Errorf("a repeated search made %d requests, want 1", len(f.hits))
	}
}

// Lucene syntax in a name is escaped, or "AC/DC" is a malformed query.
func TestSearchEscapesTheName(t *testing.T) {
	f := &fake{}
	c := f.server(t)
	if _, err := c.SearchArtists(context.Background(), `AC/DC (live)`, 5); err != nil {
		t.Fatal(err)
	}
	if want := `AC\/DC \(live\)`; f.query[0] != want {
		t.Errorf("query = %q, want %q", f.query[0], want)
	}
}

// Two searches arriving together are sent a second apart, not as a pair.
func TestRequestsAreSpaced(t *testing.T) {
	f := &fake{}
	c := f.server(t)

	var wg sync.WaitGroup
	for _, name := range []string{"Genesis", "Yes"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			if _, err := c.SearchArtists(context.Background(), name, 5); err != nil {
				t.Error(err)
			}
		}(name)
	}
	wg.Wait()

	if len(f.hits) != 2 {
		t.Fatalf("made %d requests, want 2", len(f.hits))
	}
	gap := f.hits[1].Sub(f.hits[0])
	if gap < 0 {
		gap = -gap
	}
	if gap < time.Second {
		t.Errorf("requests were %s apart; MusicBrainz allows one a second", gap)
	}
}

// A 503 is the limit, and the client stops sending until the wait is over
// rather than collecting more of them.
func TestUnavailableIsTheRateLimit(t *testing.T) {
	f := &fake{code: http.StatusServiceUnavailable}
	c := f.server(t)

	_, err := c.SearchArtists(context.Background(), "Genesis", 5)
	var limited *RateLimitError
	if !errors.As(err, &limited) || !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want a RateLimitError", err)
	}
	if limited.RetryAfter != 2*time.Second {
		t.Errorf("RetryAfter = %s, want the server's 2s", limited.RetryAfter)
	}

	// The next slot is now two seconds out, which is within the wait, so the
	// next call is held rather than refused — and is not sent early.
	f.mu.Lock()
	f.code = 0
	f.mu.Unlock()
	start := time.Now()
	if _, err := c.SearchArtists(context.Background(), "Yes", 5); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < 1500*time.Millisecond {
		t.Errorf("the call after a 503 went out after %s; it should have waited", waited)
	}
}

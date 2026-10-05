// Package musicbrainz is a small read-only client for the MusicBrainz web
// service, used to find the id of an artist from its name.
//
// It is the Discogs client's smaller sibling, shaped by the same kind of
// constraints but different numbers.
//
// The rate limit is one request a second, averaged, per IP address, and going
// over it gets a 503 for every request rather than just the excess. That is
// strict enough that the requests are spaced here rather than merely counted:
// the client waits its turn for a short while, and past that says how long
// the wait would be rather than holding a browser request open.
//
// MusicBrainz asks for a User-Agent that names the application and a way to
// reach whoever runs it, and blocks clients that send a generic one. A browser
// cannot set that header, which is one of the reasons this goes through the
// server at all; the other is that the limit is per IP, so it can only be
// kept in one place.
//
// No account or key is needed for searching.
package musicbrainz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the web service root.
const DefaultBaseURL = "https://musicbrainz.org/ws/2"

// UserAgent identifies this client in the form MusicBrainz asks for:
// application, version, and a contact URL in parentheses.
const UserAgent = "yamo/1.0 ( https://github.com/remy/yamo )"

// interval is the gap kept between requests. A little over the documented
// second, because the server measures arrival and the network does not
// deliver two requests exactly as far apart as they were sent.
const interval = 1100 * time.Millisecond

// maxWait bounds how long a call queues for its turn. A picker opened on one
// field and then the other sends two searches together, and the second waiting
// a second is fine; a request held for ten would look broken.
const maxWait = 3 * time.Second

// searchTTL is how long a search result is reused. Opening the picker,
// closing it and opening it again on the same name is ordinary, and it should
// not cost a second request out of one a second.
const searchTTL = 10 * time.Minute

// Errors callers distinguish.
var (
	ErrRateLimited = errors.New("musicbrainz: rate limit reached")
	ErrNotFound    = errors.New("musicbrainz: not found")
)

// RateLimitError reports how long to wait.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("musicbrainz: rate limit reached; try again in %s", e.RetryAfter.Round(time.Second))
}
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// Artist is one search hit.
type Artist struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	SortName string `json:"sortName,omitempty"`

	// Disambiguation is MusicBrainz' own note on which of several artists
	// with one name this is — "English rock band", "US hip hop group". It is
	// the most useful thing in the response, and the reason the result is a
	// list to choose from rather than an answer.
	Disambiguation string `json:"disambiguation,omitempty"`

	Type    string `json:"type,omitempty"`    // Person, Group, Orchestra, Choir, Character, Other
	Country string `json:"country,omitempty"` // ISO 3166-1, where known
	Area    string `json:"area,omitempty"`
	Begin   string `json:"begin,omitempty"` // a date, or as much of one as is known
	End     string `json:"end,omitempty"`

	// Score is how well the name matched, out of 100. MusicBrainz orders the
	// results by it, and a client can use it to say "this is a guess".
	Score int `json:"score"`
}

// Client talks to MusicBrainz. It is safe for concurrent use.
type Client struct {
	BaseURL string
	HTTP    *http.Client

	mu   sync.Mutex
	next time.Time // the earliest the next request may be sent

	cacheMu sync.Mutex
	cache   map[string]cachedSearch
}

type cachedSearch struct {
	artists []Artist
	at      time.Time
}

// New returns a client.
func New() *Client {
	return &Client{
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: 20 * time.Second},
		cache:   map[string]cachedSearch{},
	}
}

// SearchArtists finds artists by name, best match first.
func (c *Client) SearchArtists(ctx context.Context, name string, limit int) ([]Artist, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("musicbrainz: empty name")
	}
	if limit <= 0 || limit > 25 {
		limit = 25
	}

	key := strconv.Itoa(limit) + "\x00" + strings.ToLower(name)
	if a, ok := c.cached(key); ok {
		return a, nil
	}

	// The name goes in escaped and unfielded. Unfielded, the search covers the
	// artist's name, its aliases and its sort name, so "Beatles" finds The
	// Beatles and a name typed surname-first still lands. Escaped, because the
	// query is Lucene and an artist called "AC/DC" or "!!!" is otherwise a
	// syntax error.
	q := url.Values{}
	q.Set("query", escapeLucene(name))
	q.Set("limit", strconv.Itoa(limit))
	q.Set("fmt", "json")

	var body struct {
		Artists []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			SortName       string `json:"sort-name"`
			Disambiguation string `json:"disambiguation"`
			Type           string `json:"type"`
			Country        string `json:"country"`
			Score          int    `json:"score"`
			Area           *struct {
				Name string `json:"name"`
			} `json:"area"`
			LifeSpan *struct {
				Begin string `json:"begin"`
				End   string `json:"end"`
			} `json:"life-span"`
		} `json:"artists"`
	}
	if err := c.getJSON(ctx, "/artist?"+q.Encode(), &body); err != nil {
		return nil, err
	}

	out := make([]Artist, 0, len(body.Artists))
	for _, a := range body.Artists {
		if a.ID == "" {
			continue
		}
		ar := Artist{
			ID: strings.ToLower(a.ID), Name: a.Name, SortName: a.SortName,
			Disambiguation: a.Disambiguation, Type: a.Type,
			Country: a.Country, Score: a.Score,
		}
		if a.Area != nil {
			ar.Area = a.Area.Name
		}
		if a.LifeSpan != nil {
			ar.Begin, ar.End = a.LifeSpan.Begin, a.LifeSpan.End
		}
		out = append(out, ar)
	}

	c.cacheMu.Lock()
	c.cache[key] = cachedSearch{artists: out, at: time.Now()}
	c.cacheMu.Unlock()
	return out, nil
}

func (c *Client) cached(key string) ([]Artist, bool) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	e, ok := c.cache[key]
	if !ok || time.Since(e.at) > searchTTL {
		delete(c.cache, key)
		return nil, false
	}
	return e.artists, true
}

// wait takes the next slot, sleeping until it comes round if that is soon
// enough. A slot is claimed before the request is sent, so two callers
// arriving together are spaced rather than sent as a pair.
func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	now := time.Now()
	at := c.next
	if at.Before(now) {
		at = now
	}
	if d := at.Sub(now); d > maxWait {
		c.mu.Unlock()
		return &RateLimitError{RetryAfter: d}
	}
	c.next = at.Add(interval)
	c.mu.Unlock()

	d := time.Until(at)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// backOff pushes the next slot out after MusicBrainz has said no. Its 503 is
// for the IP, not the request, so carrying on at the usual pace would only
// keep the refusals coming.
func (c *Client) backOff(d time.Duration) {
	c.mu.Lock()
	if at := time.Now().Add(d); at.After(c.next) {
		c.next = at
	}
	c.mu.Unlock()
}

// getJSON performs one paced request and decodes the body.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	if err := c.wait(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")

	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusServiceUnavailable, http.StatusTooManyRequests:
		// 503 is how MusicBrainz says "too fast". It is also how it says it is
		// down, and the two cannot be told apart from here, so both are
		// treated as the limit: waiting is the right response to either.
		d := retryAfter(res.Header)
		c.backOff(d)
		return &RateLimitError{RetryAfter: d}
	default:
		return fmt.Errorf("musicbrainz: %s", res.Status)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out)
}

func retryAfter(h http.Header) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 5 * time.Second
}

// escapeLucene backslash-escapes the characters the search syntax treats
// specially.
func escapeLucene(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		if strings.ContainsRune(`+-&|!(){}[]^"~*?:\/`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

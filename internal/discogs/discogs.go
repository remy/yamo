// Package discogs is a small read-only client for the Discogs database, used
// to find album art that a library is missing.
//
// Three facts about the public API shape everything here.
//
// The first is that searching does not return images. A search response has
// `thumb` and `cover_image` fields and they are empty strings unless the
// request is authenticated, so finding a cover means searching for masters and
// then fetching each master in turn. One search costs one request plus one per
// candidate.
//
// The second is the rate limit: 25 requests a minute without a token, 60 with
// one, counted per IP. That is low enough that the cost above is the binding
// constraint on the whole feature, which is why masters are cached and why the
// limiter reports what it has left rather than silently blocking.
//
// The third is that Discogs rejects a request with no User-Agent outright, so
// one is always sent.
package discogs

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

// DefaultBaseURL is the public API root.
const DefaultBaseURL = "https://api.discogs.com"

// UserAgent identifies this client. Discogs asks that clients say who they are
// and answers 403 to a request that does not.
const UserAgent = "yamo/1.0 +https://github.com/remy/yamo"

// maxImageBytes caps a cover download. Discogs serves covers at 600x600 and
// they run to a few hundred kilobytes; anything past this is not a cover, and
// the bytes are going into every track on an album.
const maxImageBytes = 12 << 20

// imageHosts is the allowlist for cover downloads.
//
// The server fetches a URL the client chose, which is a request-forgery hole
// unless the destination is constrained: without this, a client could point
// the endpoint at anything the server can reach and read the response. The
// allowlist is by host rather than by prefix so a lookalike path cannot slip
// through, and the scheme is checked separately.
var imageHosts = map[string]bool{
	"i.discogs.com":   true,
	"img.discogs.com": true,
	"s.discogs.com":   true,
}

// Errors callers distinguish.
var (
	// ErrRateLimited means the per-minute budget is spent. It carries how long
	// until the next slot, so a client can say when to try again rather than
	// just refusing.
	ErrRateLimited = errors.New("discogs: rate limit reached")

	// ErrNotAllowed means an image URL is not on a Discogs image host.
	ErrNotAllowed = errors.New("discogs: not a Discogs image URL")

	// ErrNotFound means Discogs has no such master.
	ErrNotFound = errors.New("discogs: not found")
)

// RateLimitError reports how long to wait.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("discogs: rate limit reached; try again in %s", e.RetryAfter.Round(time.Second))
}
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// Client talks to the Discogs API. It is safe for concurrent use.
type Client struct {
	BaseURL string
	HTTP    *http.Client

	// ImageHosts is the download allowlist, defaulted from imageHosts. It is a
	// field rather than a constant for the same reason BaseURL is: so a test
	// can point the client at a local server without the allowlist becoming
	// the thing that fails.
	ImageHosts map[string]bool

	token string
	lim   *limiter

	mu     sync.Mutex
	master map[int64]cachedMaster
}

type cachedMaster struct {
	m  *Master
	at time.Time
}

// masterTTL is how long a fetched master is reused.
//
// It exists for the rate limit rather than for speed: picking a cover means
// searching, looking at the results, expanding one, and applying it, and
// without a cache that sequence refetches the same masters two or three times
// out of a budget of 25 a minute.
const masterTTL = 10 * time.Minute

// New returns a client. An empty token is the normal case and means the lower
// rate limit and no images in search results.
func New(token string) *Client {
	return &Client{
		BaseURL:    DefaultBaseURL,
		ImageHosts: imageHosts,
		HTTP:       &http.Client{Timeout: 20 * time.Second},
		token:      strings.TrimSpace(token),
		lim:        newLimiter(limitFor(strings.TrimSpace(token))),
		master:     map[int64]cachedMaster{},
	}
}

// limitFor is the documented per-minute budget for the two auth states.
func limitFor(token string) int {
	if token != "" {
		return 60
	}
	return 25
}

// Authenticated reports whether a token is configured, which the UI shows
// because it changes both the budget and how many requests a search costs.
func (c *Client) Authenticated() bool { return c.token != "" }

// Budget reports the per-minute limit and how much of it is left.
func (c *Client) Budget() (limit, remaining int) { return c.lim.state() }

// Result is one search hit: a master release, without any image.
type Result struct {
	MasterID int64    `json:"masterId"`
	Title    string   `json:"title"`
	Year     string   `json:"year,omitempty"`
	Country  string   `json:"country,omitempty"`
	Formats  []string `json:"formats,omitempty"`
	Label    string   `json:"label,omitempty"`

	// Genres and Styles are Discogs' two levels of classification: a handful
	// of broad genres, and the narrower styles under them. A search returns
	// both without a token, which is what makes reading a genre off one cheap.
	Genres []string `json:"genres,omitempty"`
	Styles []string `json:"styles,omitempty"`

	// Thumb and Cover are only ever set for an authenticated search. They are
	// carried through so a tokened client can skip the per-master fetch.
	Thumb string `json:"thumb,omitempty"`
	Cover string `json:"cover,omitempty"`
}

// Image is one picture attached to a master.
type Image struct {
	URI    string `json:"uri"`
	Thumb  string `json:"thumb,omitempty"`
	Type   string `json:"type,omitempty"` // "primary" or "secondary"
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// Master is a master release and its images.
type Master struct {
	MasterID int64    `json:"masterId"`
	Title    string   `json:"title"`
	Year     int      `json:"year,omitempty"`
	Artists  string   `json:"artists,omitempty"`
	Genres   []string `json:"genres,omitempty"`
	Styles   []string `json:"styles,omitempty"`
	Images   []Image  `json:"images"`
}

// Primary returns the front cover, falling back to the first image. Discogs
// marks one image primary on a well-curated release and none on a thin one.
func (m *Master) Primary() *Image {
	for i := range m.Images {
		if m.Images[i].Type == "primary" {
			return &m.Images[i]
		}
	}
	if len(m.Images) > 0 {
		return &m.Images[0]
	}
	return nil
}

// Search finds master releases matching a free-text query.
//
// Masters rather than releases because a master is the album — one entry for
// every pressing, reissue and territory — and a release search returns the
// same sleeve twenty times over.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("discogs: empty query")
	}
	if limit <= 0 || limit > 50 {
		limit = 50
	}

	q := url.Values{}
	q.Set("q", query)
	q.Set("type", "master")
	q.Set("per_page", strconv.Itoa(limit))

	var body struct {
		Results []struct {
			ID       int64    `json:"id"`
			MasterID int64    `json:"master_id"`
			Title    string   `json:"title"`
			Year     string   `json:"year"`
			Country  string   `json:"country"`
			Format   []string `json:"format"`
			Label    []string `json:"label"`
			Genre    []string `json:"genre"`
			Style    []string `json:"style"`
			Thumb    string   `json:"thumb"`
			Cover    string   `json:"cover_image"`
		} `json:"results"`
	}
	if err := c.getJSON(ctx, "/database/search?"+q.Encode(), &body); err != nil {
		return nil, err
	}

	out := make([]Result, 0, len(body.Results))
	for _, r := range body.Results {
		id := r.MasterID
		if id == 0 {
			id = r.ID
		}
		if id == 0 {
			continue
		}
		out = append(out, Result{
			MasterID: id,
			Title:    r.Title,
			Year:     r.Year,
			Country:  r.Country,
			Formats:  dedupe(r.Format),
			Label:    first(r.Label),
			Genres:   dedupe(r.Genre),
			Styles:   dedupe(r.Style),
			Thumb:    r.Thumb,
			Cover:    r.Cover,
		})
	}
	return out, nil
}

// MasterByID fetches a master and its images, using the cache when it can.
func (c *Client) MasterByID(ctx context.Context, id int64) (*Master, error) {
	if m := c.cached(id); m != nil {
		return m, nil
	}

	var body struct {
		ID      int64  `json:"id"`
		Title   string `json:"title"`
		Year    int    `json:"year"`
		Artists []struct {
			Name string `json:"name"`
		} `json:"artists"`
		Genres []string `json:"genres"`
		Styles []string `json:"styles"`
		Images []struct {
			URI    string `json:"uri"`
			URI150 string `json:"uri150"`
			Type   string `json:"type"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"images"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("/masters/%d", id), &body); err != nil {
		return nil, err
	}

	m := &Master{
		MasterID: id, Title: body.Title, Year: body.Year,
		Genres: dedupe(body.Genres), Styles: dedupe(body.Styles),
	}
	names := make([]string, 0, len(body.Artists))
	for _, a := range body.Artists {
		names = append(names, a.Name)
	}
	m.Artists = strings.Join(names, ", ")
	for _, im := range body.Images {
		if im.URI == "" {
			continue
		}
		m.Images = append(m.Images, Image{
			URI: im.URI, Thumb: im.URI150, Type: im.Type,
			Width: im.Width, Height: im.Height,
		})
	}

	c.mu.Lock()
	c.master[id] = cachedMaster{m: m, at: time.Now()}
	c.mu.Unlock()
	return m, nil
}

func (c *Client) cached(id int64) *Master {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.master[id]
	if !ok || time.Since(e.at) > masterTTL {
		return nil
	}
	return e.m
}

// FetchImage downloads a cover.
//
// The URL must be on a Discogs image host: see imageHosts. Image bytes do not
// come from the API host and are not rate limited by it, so this does not take
// from the budget.
func (c *Client) FetchImage(ctx context.Context, raw string) ([]byte, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", ErrNotAllowed
	}
	hosts := c.ImageHosts
	if hosts == nil {
		hosts = imageHosts
	}
	if u.Scheme != "https" || !hosts[strings.ToLower(u.Hostname())] {
		return nil, "", ErrNotAllowed
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "image/*")

	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, "", ErrNotFound
	}
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("discogs: fetching the image returned %s", res.Status)
	}

	// Read one byte past the cap so an oversized image is refused rather than
	// silently truncated into a corrupt file.
	data, err := io.ReadAll(io.LimitReader(res.Body, maxImageBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxImageBytes {
		return nil, "", fmt.Errorf("discogs: the image is larger than %dMB", maxImageBytes>>20)
	}
	if len(data) == 0 {
		return nil, "", errors.New("discogs: the image was empty")
	}
	return data, res.Header.Get("Content-Type"), nil
}

// getJSON performs one rate-limited API call and decodes the body.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	if err := c.lim.take(ctx); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Discogs token="+c.token)
	}

	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	// Discogs reports the true state of the budget on every response. Trusting
	// it over the local count keeps the two from drifting apart, which they
	// otherwise do: the limit is per IP, so another process on this machine
	// spends from the same allowance.
	c.lim.observe(res.Header)

	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: retryAfter(res.Header)}
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("discogs: refused the request (%s); if a token is set it may be wrong", res.Status)
	default:
		return fmt.Errorf("discogs: %s", res.Status)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
}

func retryAfter(h http.Header) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return time.Minute
}

func first(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[0]
}

// dedupe keeps the order but drops repeats, which the format list is full of.
func dedupe(xs []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if x == "" || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	return out
}

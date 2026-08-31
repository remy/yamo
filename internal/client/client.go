// Package client talks to a yamo server.
//
// The types on the wire are the service's own, imported rather than restated.
// Two parallel sets of structs describing the same JSON would drift, and the
// compiler cannot notice when they do.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/remy/yamo/internal/library"
)

// DefaultServer is where the client looks unless told otherwise.
const DefaultServer = "http://127.0.0.1:8467"

// Client is a connection to a yamo server.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New builds a client for a server address.
//
// A unix:// address is dialled as a socket. The HTTP layer still needs a host
// in the URL, so a placeholder is used and the transport ignores it.
func New(server, token string) (*Client, error) {
	if server == "" {
		server = DefaultServer
	}
	c := &Client{token: token, http: &http.Client{Timeout: 0}}

	if socket, ok := cutUnixPrefix(server); ok {
		c.base = "http://yamo"
		c.http.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		}
		return c, nil
	}

	if !strings.Contains(server, "://") {
		server = "http://" + server
	}
	u, err := url.Parse(server)
	if err != nil {
		return nil, fmt.Errorf("bad server address %q: %w", server, err)
	}
	c.base = strings.TrimRight(u.String(), "/")
	return c, nil
}

func cutUnixPrefix(s string) (string, bool) {
	if rest, ok := strings.CutPrefix(s, "unix://"); ok {
		return rest, true
	}
	if rest, ok := strings.CutPrefix(s, "unix:"); ok {
		return rest, true
	}
	return "", false
}

// FromEnv builds a client from the usual flags and environment.
func FromEnv(server, token string) (*Client, error) {
	if server == "" {
		server = os.Getenv("YAMO_SERVER")
	}
	if token == "" {
		token = os.Getenv("YAMO_TOKEN")
	}
	return New(server, token)
}

// Error is a failure reported by the server.
type Error struct {
	Status   int
	Code     string
	Message  string
	Expected *int
	Actual   *int
	JobID    string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("server returned %d", e.Status)
}

// IsNotFound reports whether an error is a missing resource.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// IsConflict reports whether an error is a version or count conflict, which a
// client usually wants to handle by refreshing rather than retrying.
func IsConflict(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusConflict
}

// ErrServerUnreachable wraps a transport failure, so the command line can
// suggest starting a server rather than printing a dial error.
type ErrServerUnreachable struct {
	Base string
	Err  error
}

func (e *ErrServerUnreachable) Error() string {
	return fmt.Sprintf("cannot reach a yamo server at %s\n       start one with: yamo serve", e.Base)
}

func (e *ErrServerUnreachable) Unwrap() error { return e.Err }

// do performs a request and decodes the response into out.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out any, opts ...func(*http.Request)) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return &ErrServerUnreachable{Base: c.base, Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return decodeError(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// decodeError turns an error response into a typed error, falling back to the
// raw body when it is not the JSON shape the schema promises.
func decodeError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	e := &Error{Status: resp.StatusCode}

	var payload struct {
		Error    string `json:"error"`
		Code     string `json:"code"`
		Expected *int   `json:"expected"`
		Actual   *int   `json:"actual"`
		JobID    string `json:"jobId"`
	}
	if json.Unmarshal(data, &payload) == nil && payload.Error != "" {
		e.Message, e.Code = payload.Error, payload.Code
		e.Expected, e.Actual = payload.Expected, payload.Actual
		e.JobID = payload.JobID
		return e
	}
	e.Message = strings.TrimSpace(string(data))
	if e.Message == "" {
		e.Message = resp.Status
	}
	return e
}

func (c *Client) postJSON(ctx context.Context, path string, in, out any) error {
	buf, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, path, bytes.NewReader(buf), out)
}

// --- reading ------------------------------------------------------------

// ListTracks searches, sorts and pages the library.
func (c *Client) ListTracks(ctx context.Context, p library.ListParams) (*library.ListResult, error) {
	q := url.Values{}
	if p.Query != "" {
		q.Set("q", p.Query)
	}
	if p.Sort != "" {
		q.Set("sort", p.Sort)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Offset > 0 {
		q.Set("offset", strconv.Itoa(p.Offset))
	}
	var out library.ListResult
	return &out, c.do(ctx, http.MethodGet, "/v1/tracks?"+q.Encode(), nil, &out)
}

// AllTracks pages through an entire result set.
//
// It exists for the command line, which prints everything to a pipe. An
// interactive client should page instead: this pulls the whole set into memory
// one window at a time.
func (c *Client) AllTracks(ctx context.Context, p library.ListParams) ([]library.Track, error) {
	p.Limit = library.MaxLimit
	p.Offset = 0
	var all []library.Track
	for {
		page, err := c.ListTracks(ctx, p)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		p.Offset += len(page.Items)
		if len(page.Items) == 0 || p.Offset >= page.Total {
			return all, nil
		}
	}
}

func (c *Client) GetTrack(ctx context.Context, id string) (*library.Track, error) {
	var out library.Track
	return &out, c.do(ctx, http.MethodGet, "/v1/tracks/"+url.PathEscape(id), nil, &out)
}

// PatchTrack edits one track. When ifMatch is non-empty the server refuses the
// write if the file has changed since, returning a conflict.
func (c *Client) PatchTrack(ctx context.Context, id string, ch library.Changes, ifMatch string) (*library.Track, error) {
	buf, err := json.Marshal(ch)
	if err != nil {
		return nil, err
	}
	var out library.Track
	err = c.do(ctx, http.MethodPatch, "/v1/tracks/"+url.PathEscape(id), bytes.NewReader(buf), &out,
		func(r *http.Request) {
			if ifMatch != "" {
				r.Header.Set("If-Match", strconv.Quote(ifMatch))
			}
		})
	return &out, err
}

func (c *Client) Albums(ctx context.Context, p library.ListParams) (*library.AlbumsResult, error) {
	q := url.Values{}
	if p.Query != "" {
		q.Set("q", p.Query)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Offset > 0 {
		q.Set("offset", strconv.Itoa(p.Offset))
	}
	var out library.AlbumsResult
	return &out, c.do(ctx, http.MethodGet, "/v1/albums?"+q.Encode(), nil, &out)
}

// ValueCount is one distinct field value and how many tracks carry it.
type ValueCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

func (c *Client) Values(ctx context.Context, field, prefix string, limit int) ([]ValueCount, error) {
	q := url.Values{}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []ValueCount
	return out, c.do(ctx, http.MethodGet, "/v1/values/"+url.PathEscape(field)+"?"+q.Encode(), nil, &out)
}

func (c *Client) Stats(ctx context.Context) (*library.Stats, error) {
	var out library.Stats
	return &out, c.do(ctx, http.MethodGet, "/v1/stats", nil, &out)
}

// --- mutating -----------------------------------------------------------

func (c *Client) BatchSet(ctx context.Context, req library.BatchSetRequest) (*library.Job, error) {
	var out library.Job
	return &out, c.postJSON(ctx, "/v1/tracks/batch", req, &out)
}

// Scan starts a scan. If one is already running the server refuses, and the
// error carries that job's id so a caller can follow it instead.
func (c *Client) Scan(ctx context.Context, req library.ScanRequest) (*library.Job, error) {
	var out library.Job
	if err := c.postJSON(ctx, "/v1/scans", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ScanStatus reports whether a scan is running and what the last one did.
func (c *Client) ScanStatus(ctx context.Context) (*library.ScanStatus, error) {
	var out library.ScanStatus
	return &out, c.do(ctx, http.MethodGet, "/v1/scans", nil, &out)
}

// RunningScanID returns the scan already in progress, when an error says one
// is. Empty otherwise.
func RunningScanID(err error) string {
	var e *Error
	if errors.As(err, &e) && e.Code == "scan_running" {
		return e.JobID
	}
	return ""
}

func (c *Client) Strip(ctx context.Context, req library.StripRequest) (*library.Job, error) {
	var out library.Job
	return &out, c.postJSON(ctx, "/v1/strip", req, &out)
}

func (c *Client) Restore(ctx context.Context, req library.RestoreRequest) (*library.Job, error) {
	var out library.Job
	return &out, c.postJSON(ctx, "/v1/restore", req, &out)
}

func (c *Client) Backups(ctx context.Context) ([]library.Backup, error) {
	var out []library.Backup
	return out, c.do(ctx, http.MethodGet, "/v1/backups", nil, &out)
}

// --- artwork ------------------------------------------------------------

// Artwork returns a track's cover and its content type.
func (c *Client) Artwork(ctx context.Context, id string) ([]byte, string, error) {
	return c.fetchImage(ctx, "/v1/tracks/"+url.PathEscape(id)+"/artwork")
}

// Clipboard returns the image the server is holding.
func (c *Client) Clipboard(ctx context.Context) ([]byte, string, error) {
	return c.fetchImage(ctx, "/v1/clipboard/artwork")
}

func (c *Client) fetchImage(ctx context.Context, path string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, "", err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", &ErrServerUnreachable{Base: c.base, Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", decodeError(resp)
	}
	data, err := io.ReadAll(resp.Body)
	return data, resp.Header.Get("Content-Type"), err
}

// PictureInfo describes an image the server is holding.
type PictureInfo struct {
	MIME    string `json:"mime"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Bytes   int    `json:"bytes"`
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
	Source  string `json:"source"`
}

func (c *Client) PutArtwork(ctx context.Context, id string, image []byte) (*library.Track, error) {
	var out library.Track
	return &out, c.do(ctx, http.MethodPut, "/v1/tracks/"+url.PathEscape(id)+"/artwork",
		bytes.NewReader(image), &out, octetStream)
}

func (c *Client) DeleteArtwork(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/tracks/"+url.PathEscape(id)+"/artwork", nil, nil)
}

// PutClipboard uploads an image for later pasting, from any client.
func (c *Client) PutClipboard(ctx context.Context, image []byte) (*PictureInfo, error) {
	var out PictureInfo
	return &out, c.do(ctx, http.MethodPut, "/v1/clipboard/artwork", bytes.NewReader(image), &out, octetStream)
}

func (c *Client) CopyArtworkFromTrack(ctx context.Context, id string) (*PictureInfo, error) {
	var out PictureInfo
	return &out, c.do(ctx, http.MethodPut, "/v1/clipboard/artwork/from-track/"+url.PathEscape(id), nil, &out)
}

func (c *Client) ClearClipboard(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/v1/clipboard/artwork", nil, nil)
}

// octetStream lets the server sniff the format from the content, which is more
// reliable than trusting a header a client guessed from a file extension.
func octetStream(r *http.Request) { r.Header.Set("Content-Type", "application/octet-stream") }

// BatchArtworkRequest mirrors the schema; the image travels base64 encoded so
// the whole request stays one JSON document.
type BatchArtworkRequest struct {
	Selector library.Selector `json:"selector"`
	Source   string           `json:"source"`
	Image    []byte           `json:"image,omitempty"`
	DryRun   bool             `json:"dryRun,omitempty"`
}

func (c *Client) BatchArtwork(ctx context.Context, req BatchArtworkRequest) (*library.Job, error) {
	var out library.Job
	return &out, c.postJSON(ctx, "/v1/artwork/batch", req, &out)
}

func (c *Client) ArtworkSummary(ctx context.Context, query string) (*library.ArtworkReport, error) {
	q := url.Values{}
	if query != "" {
		q.Set("q", query)
	}
	var out library.ArtworkReport
	return &out, c.do(ctx, http.MethodGet, "/v1/artwork/summary?"+q.Encode(), nil, &out)
}

// --- jobs ---------------------------------------------------------------

func (c *Client) Job(ctx context.Context, id string) (*library.Job, error) {
	var out library.Job
	return &out, c.do(ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(id), nil, &out)
}

func (c *Client) Jobs(ctx context.Context) ([]*library.Job, error) {
	var out []*library.Job
	return out, c.do(ctx, http.MethodGet, "/v1/jobs", nil, &out)
}

func (c *Client) CancelJob(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/jobs/"+url.PathEscape(id), nil, nil)
}

// jobPoll is how often WaitJob asks for progress when the event stream is not
// available. Frequent enough to feel live, rare enough to be free.
const jobPoll = 150 * time.Millisecond

// WaitJob blocks until a job finishes, reporting progress as it goes.
func (c *Client) WaitJob(ctx context.Context, id string, onProgress func(*library.Job)) (*library.Job, error) {
	for {
		j, err := c.Job(ctx, id)
		if err != nil {
			return nil, err
		}
		if onProgress != nil {
			onProgress(j)
		}
		if j.State != library.JobRunning {
			return j, nil
		}
		select {
		case <-ctx.Done():
			// Leave the job running: the server owns it, and a cancelled wait
			// is not a cancelled operation.
			return j, ctx.Err()
		case <-time.After(jobPoll):
		}
	}
}

// DecodeResult unpacks a job's kind-specific result.
//
// It round-trips through JSON because the result arrives as a generic map; the
// alternative is a type switch in every caller over shapes the schema already
// describes.
func DecodeResult(j *library.Job, out any) error {
	if j == nil || j.Result == nil {
		return errors.New("the job has no result")
	}
	buf, err := json.Marshal(j.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(buf, out)
}

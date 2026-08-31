package client

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/remy/yamo/internal/api"
	"github.com/remy/yamo/internal/library"
)

// newTestClient starts a real server over a freshly scanned library and
// returns a client pointed at it.
//
// The server is real rather than a stub on purpose: a client tested against a
// fake only proves the fake and the client agree.
func newTestClient(t *testing.T, tracks int) (*Client, string) {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	dir := t.TempDir()
	music := filepath.Join(dir, "music", "Elvis Presley", "Sun Sessions")
	if err := os.MkdirAll(music, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= tracks; i++ {
		out := filepath.Join(music, fmt.Sprintf("%02d track.mp3", i))
		cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:a", "libmp3lame",
			"-metadata", fmt.Sprintf("title=Track %d", i),
			"-metadata", "artist=Elvis Presly",
			"-metadata", "album=Sun Sessions",
			"-metadata", fmt.Sprintf("track=%d", i), out)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v\n%s", err, b)
		}
	}

	svc, err := library.Open(library.Options{
		CatalogPath:  filepath.Join(dir, "catalog.db"),
		SaveInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(svc, api.Options{}))
	t.Cleanup(func() {
		srv.Close()
		svc.Close()
	})

	c, err := New(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	job, err := c.Scan(ctx, library.ScanRequest{Roots: []string{filepath.Join(dir, "music")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.WaitJob(ctx, job.ID, nil); err != nil {
		t.Fatal(err)
	}
	return c, dir
}

func TestClientReadAndEdit(t *testing.T) {
	c, _ := newTestClient(t, 5)
	ctx := context.Background()

	page, err := c.ListTracks(ctx, library.ListParams{Sort: "track"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 5 {
		t.Fatalf("listed %d tracks, want 5", page.Total)
	}

	// Paging through everything must return each track once.
	all, err := c.AllTracks(ctx, library.ListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Errorf("AllTracks returned %d, want 5", len(all))
	}

	first := page.Items[0]
	got, err := c.GetTrack(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != first.Path {
		t.Errorf("GetTrack returned %q, want %q", got.Path, first.Path)
	}

	// Editing, with the version the list handed back.
	updated, err := c.PatchTrack(ctx, first.ID, library.Changes{"genre": strptr("Rockabilly")}, first.Version)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if updated.Genre != "Rockabilly" {
		t.Errorf("genre = %q", updated.Genre)
	}

	// The old version is now stale, and the client must surface that as a
	// conflict rather than a generic failure.
	_, err = c.PatchTrack(ctx, first.ID, library.Changes{"genre": strptr("Blues")}, first.Version)
	if !IsConflict(err) {
		t.Errorf("a stale version gave %v, want a conflict", err)
	}

	if _, err := c.GetTrack(ctx, "nosuchid"); !IsNotFound(err) {
		t.Errorf("an unknown id gave %v, want not found", err)
	}
}

func strptr(s string) *string { return &s }

func TestClientBatchAndJobProgress(t *testing.T) {
	c, _ := newTestClient(t, 6)
	ctx := context.Background()

	job, err := c.BatchSet(ctx, library.BatchSetRequest{
		Selector: library.Selector{Query: "artist:presly"},
		Set:      library.Changes{"artist": strptr("Elvis Presley")},
	})
	if err != nil {
		t.Fatal(err)
	}

	var sawProgress bool
	done, err := c.WaitJob(ctx, job.ID, func(j *library.Job) {
		if j.Progress.Total > 0 {
			sawProgress = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if done.State != library.JobSucceeded {
		t.Fatalf("job %s: %s", done.State, done.Error)
	}
	if !sawProgress {
		t.Error("no progress was reported while waiting")
	}

	var res library.BatchResult
	if err := DecodeResult(done, &res); err != nil {
		t.Fatalf("decoding the result: %v", err)
	}
	if res.Changed != 6 {
		t.Errorf("batch changed %d, want 6", res.Changed)
	}

	page, _ := c.ListTracks(ctx, library.ListParams{Query: `artist:"elvis presley"`})
	if page.Total != 6 {
		t.Errorf("after the batch, %d tracks have the corrected artist", page.Total)
	}
}

// TestClientCountMismatchIsTyped checks the safety rail survives the wire: a
// client needs to tell "the selection moved" apart from any other failure.
func TestClientCountMismatchIsTyped(t *testing.T) {
	c, _ := newTestClient(t, 3)
	n := 99
	_, err := c.BatchSet(context.Background(), library.BatchSetRequest{
		Selector: library.Selector{Query: "artist:presly", ExpectCount: &n},
		Set:      library.Changes{"genre": strptr("Rock")},
	})
	if !IsConflict(err) {
		t.Fatalf("a stale count gave %v, want a conflict", err)
	}
	var e *Error
	if !asClientError(err, &e) {
		t.Fatalf("error was %T, want a typed client error", err)
	}
	if e.Code != "count_mismatch" {
		t.Errorf("code = %q, want count_mismatch", e.Code)
	}
	if e.Expected == nil || *e.Expected != 99 || e.Actual == nil || *e.Actual != 3 {
		t.Errorf("the counts did not survive the wire: %+v", e)
	}
}

func asClientError(err error, out **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*out = e
	}
	return ok
}

func TestClientArtworkAndClipboard(t *testing.T) {
	c, dir := newTestClient(t, 3)
	ctx := context.Background()
	ff, _ := exec.LookPath("ffmpeg")

	cover := filepath.Join(dir, "cover.jpg")
	if b, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=red:s=64x64", "-frames:v", "1", cover).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, b)
	}
	img, err := os.ReadFile(cover)
	if err != nil {
		t.Fatal(err)
	}

	info, err := c.PutClipboard(ctx, img)
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 64 || info.Height != 64 {
		t.Errorf("clipboard image is %dx%d, want 64x64", info.Width, info.Height)
	}

	job, err := c.BatchArtwork(ctx, BatchArtworkRequest{
		Selector: library.Selector{All: true},
		Source:   "clipboard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.WaitJob(ctx, job.ID, nil); err != nil {
		t.Fatal(err)
	}

	page, _ := c.ListTracks(ctx, library.ListParams{})
	for _, tr := range page.Items {
		if !tr.HasArt {
			t.Fatalf("%s has no art after the paste", tr.Path)
		}
		data, ct, err := c.Artwork(ctx, tr.ID)
		if err != nil {
			t.Fatal(err)
		}
		if ct != "image/jpeg" {
			t.Errorf("content type = %q", ct)
		}
		if len(data) != len(img) {
			t.Errorf("got %d bytes back, sent %d", len(data), len(img))
		}
	}

	summary, err := c.ArtworkSummary(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Groups) != 1 || summary.Groups[0].Tracks != 3 {
		t.Errorf("summary = %+v", summary.Groups)
	}
}

func TestClientEvents(t *testing.T) {
	c, _ := newTestClient(t, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	page, _ := c.ListTracks(ctx, library.ListParams{})
	id := page.Items[0].ID
	if _, err := c.PatchTrack(ctx, id, library.Changes{"genre": strptr("Rock")}, ""); err != nil {
		t.Fatal(err)
	}

	select {
	case e := <-events:
		if e.Type != library.EventTracksChanged {
			t.Errorf("first event was %q", e.Type)
		}
		if len(e.TrackIDs) != 1 || e.TrackIDs[0] != id {
			t.Errorf("event named %v, want %s", e.TrackIDs, id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event arrived")
	}
}

// TestClientUnreachableIsHelpful checks the message a person sees when no
// server is running, which is the most likely first experience.
func TestClientUnreachableIsHelpful(t *testing.T) {
	c, err := New("http://127.0.0.1:1", "") // nothing listens on port 1
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Stats(context.Background())
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !containsAll(err.Error(), "cannot reach", "yamo serve") {
		t.Errorf("the message does not say how to fix it: %v", err)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestClientUnixSocket(t *testing.T) {
	// A unix address must be dialled as a socket rather than parsed as a host.
	c, err := New("unix:///tmp/yamo-test.sock", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.base != "http://yamo" {
		t.Errorf("unix client base = %q", c.base)
	}
	if c.http.Transport == nil {
		t.Error("unix client has no custom transport")
	}
	// A bare host:port is treated as http.
	c2, err := New("nas:8467", "")
	if err != nil {
		t.Fatal(err)
	}
	if c2.base != "http://nas:8467" {
		t.Errorf("bare address became %q", c2.base)
	}
}

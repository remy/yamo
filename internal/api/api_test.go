package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/remy/yamo/internal/library"
)

// harness is a running server over a real, freshly scanned library.
type harness struct {
	t   *testing.T
	srv *httptest.Server
	svc *library.Service
	dir string

	// token, when set, is sent as the bearer token on every request. Tests
	// that check what happens without one clear it.
	token string
}

func newHarness(t *testing.T, tracks int) *harness {
	t.Helper()
	return newHarnessOpts(t, tracks, Options{})
}

func newHarnessOpts(t *testing.T, tracks int, opts Options) *harness {
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
			"-metadata", "comment=noise",
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
	h := &harness{t: t, svc: svc, dir: dir, token: opts.Token}
	h.srv = httptest.NewServer(New(svc, opts))
	t.Cleanup(func() {
		h.srv.Close()
		svc.Close()
	})

	job := h.post(t, "/v1/scans", map[string]any{"roots": []string{filepath.Join(dir, "music")}}, http.StatusAccepted)
	h.waitJob(t, job["id"].(string))
	return h
}

func (h *harness) do(t *testing.T, method, path string, body io.Reader, want int) (*http.Response, []byte) {
	t.Helper()
	return h.doWith(t, method, path, body, want, nil)
}

// doWith is do with extra request headers, for the conditional and concurrency
// checks that are entirely about them.
func (h *harness) doWith(t *testing.T, method, path string, body io.Reader, want int, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, h.srv.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if want != 0 && resp.StatusCode != want {
		t.Fatalf("%s %s = %d, want %d\n%s", method, path, resp.StatusCode, want, data)
	}
	return resp, data
}

func (h *harness) getJSON(t *testing.T, path string, want int) map[string]any {
	t.Helper()
	_, data := h.do(t, http.MethodGet, path, nil, want)
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("GET %s returned unparseable JSON: %v\n%s", path, err, data)
	}
	return out
}

func (h *harness) post(t *testing.T, path string, body any, want int) map[string]any {
	t.Helper()
	buf, _ := json.Marshal(body)
	_, data := h.do(t, http.MethodPost, path, bytes.NewReader(buf), want)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}

// waitJob polls until a job finishes.
func (h *harness) waitJob(t *testing.T, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		j := h.getJSON(t, "/v1/jobs/"+id, http.StatusOK)
		if j["state"] != "running" {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s never finished", id)
	return nil
}

func (h *harness) firstTrack(t *testing.T) map[string]any {
	t.Helper()
	page := h.getJSON(t, "/v1/tracks?sort=track&limit=1", http.StatusOK)
	items := page["items"].([]any)
	if len(items) == 0 {
		t.Fatal("the library is empty")
	}
	return items[0].(map[string]any)
}

func TestReadEndpoints(t *testing.T) {
	h := newHarness(t, 4)

	page := h.getJSON(t, "/v1/tracks", http.StatusOK)
	if page["total"].(float64) != 4 {
		t.Errorf("total = %v, want 4", page["total"])
	}
	// Paging metadata must be present even on a single page.
	if page["limit"].(float64) != float64(library.DefaultLimit) || page["offset"].(float64) != 0 {
		t.Errorf("paging metadata = limit %v offset %v", page["limit"], page["offset"])
	}

	filtered := h.getJSON(t, "/v1/tracks?q=artist:presly", http.StatusOK)
	if filtered["total"].(float64) != 4 {
		t.Errorf("query matched %v, want 4", filtered["total"])
	}
	none := h.getJSON(t, "/v1/tracks?q=artist:nobody", http.StatusOK)
	if none["total"].(float64) != 0 {
		t.Errorf("an impossible query matched %v", none["total"])
	}

	// A fuzzy term finds the misspelt artist from the correct spelling, and
	// the score it was ranked on comes back with it.
	fuzzy := h.getJSON(t, "/v1/tracks?q=artist:~presley", http.StatusOK)
	if fuzzy["total"].(float64) != 4 {
		t.Errorf("artist:~presley matched %v, want 4", fuzzy["total"])
	}
	near := fuzzy["items"].([]any)[0].(map[string]any)
	if score, ok := near["score"].(float64); !ok || score <= 0 || score >= 1 {
		t.Errorf("score = %v, want a number between 0 and 1", near["score"])
	}
	// An exact query carries none, so a client can tell a filter from a search.
	if _, ok := page["items"].([]any)[0].(map[string]any)["score"]; ok {
		t.Error("an exact query returned a score")
	}

	// One track, with an ETag a client can use as If-Match.
	first := h.firstTrack(t)
	resp, _ := h.do(t, http.MethodGet, "/v1/tracks/"+first["id"].(string), nil, http.StatusOK)
	if got := strings.Trim(resp.Header.Get("ETag"), `"`); got != first["version"] {
		t.Errorf("ETag %q does not match version %q", got, first["version"])
	}
	h.do(t, http.MethodGet, "/v1/tracks/nosuchid", nil, http.StatusNotFound)

	// Albums, artists, values and stats.
	albums := h.getJSON(t, "/v1/albums", http.StatusOK)
	if albums["total"].(float64) != 1 {
		t.Errorf("albums total = %v, want 1", albums["total"])
	}
	artists := h.getJSON(t, "/v1/artists", http.StatusOK)
	if artists["total"].(float64) != 1 {
		t.Errorf("artists total = %v, want 1", artists["total"])
	}
	one := artists["items"].([]any)[0].(map[string]any)
	if one["artist"] != "Elvis Presly" || one["tracks"].(float64) != 4 || one["albums"].(float64) != 1 {
		t.Errorf("artists = %v", artists)
	}
	// The query an artist carries must reselect exactly it over the API too.
	requery := h.getJSON(t, "/v1/tracks?q="+url.QueryEscape(one["query"].(string)), http.StatusOK)
	if requery["total"].(float64) != 4 {
		t.Errorf("the artist's query %q matched %v, want 4", one["query"], requery["total"])
	}
	_, vals := h.do(t, http.MethodGet, "/v1/values/artist?prefix=el", nil, http.StatusOK)
	if !strings.Contains(string(vals), "Elvis") {
		t.Errorf("autocomplete returned %s", vals)
	}
	h.do(t, http.MethodGet, "/v1/values/nosuchfield", nil, http.StatusNotFound)

	stats := h.getJSON(t, "/v1/stats", http.StatusOK)
	if stats["tracks"].(float64) != 4 {
		t.Errorf("stats tracks = %v", stats["tracks"])
	}
	if missing := stats["missing"].(map[string]any); missing["genre"].(float64) != 4 {
		t.Errorf("missing genre = %v, want 4", missing["genre"])
	}
}

func TestPatchAndConflict(t *testing.T) {
	h := newHarness(t, 3)
	track := h.firstTrack(t)
	id, version := track["id"].(string), track["version"].(string)

	// A patch with the current version succeeds and returns a new one.
	req, _ := http.NewRequest(http.MethodPatch, h.srv.URL+"/v1/tracks/"+id,
		strings.NewReader(`{"genre":"Rockabilly","year":"1954"}`))
	req.Header.Set("If-Match", `"`+version+`"`)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch = %d\n%s", resp.StatusCode, data)
	}
	var updated map[string]any
	_ = json.Unmarshal(data, &updated)
	if updated["genre"] != "Rockabilly" || updated["year"].(float64) != 1954 {
		t.Fatalf("patch returned %s", data)
	}
	if updated["version"] == version {
		t.Error("the version did not change after a write")
	}

	// The same version is now stale. This is the case that matters: a phone
	// and a terminal editing the same track a moment apart.
	req2, _ := http.NewRequest(http.MethodPatch, h.srv.URL+"/v1/tracks/"+id,
		strings.NewReader(`{"genre":"Blues"}`))
	req2.Header.Set("If-Match", `"`+version+`"`)
	req2.Header.Set("Content-Type", "application/json")
	resp2, _ := h.srv.Client().Do(req2)
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("a stale If-Match returned %d, want 409", resp2.StatusCode)
	}
	if !strings.Contains(string(body2), `"conflict"`) {
		t.Errorf("conflict body = %s", body2)
	}
	// The losing write must not have landed.
	after := h.getJSON(t, "/v1/tracks/"+id, http.StatusOK)
	if after["genre"] != "Rockabilly" {
		t.Errorf("the rejected write landed anyway: genre = %v", after["genre"])
	}

	// Clearing a field with an explicit null.
	h.do(t, http.MethodPatch, "/v1/tracks/"+id, strings.NewReader(`{"genre":null}`), http.StatusOK)
	cleared := h.getJSON(t, "/v1/tracks/"+id, http.StatusOK)
	if _, present := cleared["genre"]; present {
		t.Errorf("genre survived being cleared: %v", cleared["genre"])
	}
}

func TestPatchValidation(t *testing.T) {
	h := newHarness(t, 2)
	id := h.firstTrack(t)["id"].(string)

	for _, c := range []struct{ name, body string }{
		{"unknown field", `{"nonsense":"x"}`},
		{"read-only field", `{"path":"/etc/passwd"}`},
		{"malformed json", `{`},
	} {
		t.Run(c.name, func(t *testing.T) {
			h.do(t, http.MethodPatch, "/v1/tracks/"+id, strings.NewReader(c.body), http.StatusBadRequest)
		})
	}
	h.do(t, http.MethodPatch, "/v1/tracks/nosuchid", strings.NewReader(`{"genre":"x"}`), http.StatusNotFound)
}

func TestBatchEdit(t *testing.T) {
	h := newHarness(t, 5)

	job := h.post(t, "/v1/tracks/batch", map[string]any{
		"selector": map[string]any{"query": "artist:presly", "expectCount": 5},
		"set":      map[string]any{"artist": "Elvis Presley", "genre": "Rockabilly"},
	}, http.StatusAccepted)

	done := h.waitJob(t, job["id"].(string))
	if done["state"] != "succeeded" {
		t.Fatalf("batch %v: %v", done["state"], done["error"])
	}
	result := done["result"].(map[string]any)
	if result["changed"].(float64) != 5 {
		t.Fatalf("batch changed %v, want 5", result["changed"])
	}

	if got := h.getJSON(t, "/v1/tracks?q=artist:%22elvis+presley%22", http.StatusOK); got["total"].(float64) != 5 {
		t.Errorf("after the batch, the corrected artist matched %v", got["total"])
	}

	// A stale expectCount is refused rather than applied to a different set.
	resp := h.post(t, "/v1/tracks/batch", map[string]any{
		"selector": map[string]any{"query": "artist:elvis", "expectCount": 99},
		"set":      map[string]any{"comment": "no"},
	}, http.StatusConflict)
	if resp["code"] != "count_mismatch" {
		t.Errorf("expected count_mismatch, got %v", resp)
	}
	if resp["actual"].(float64) != 5 || resp["expected"].(float64) != 99 {
		t.Errorf("the mismatch did not report both counts: %v", resp)
	}

	// An empty selector must not mean everything.
	h.post(t, "/v1/tracks/batch", map[string]any{
		"selector": map[string]any{},
		"set":      map[string]any{"comment": "no"},
	}, http.StatusBadRequest)
}

// The audio endpoint serves the file itself, which is what lets the sheet
// settle whether a song is the one its tags describe.
func TestAudioEndpoint(t *testing.T) {
	h := newHarness(t, 1)
	track := h.firstTrack(t)
	id, path := track["id"].(string), track["path"].(string)

	res, body := h.do(t, http.MethodGet, "/v1/tracks/"+id+"/audio", nil, http.StatusOK)
	if ct := res.Header.Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("Content-Type = %q, want audio/mpeg — the format the catalogue recorded", ct)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, want) {
		t.Errorf("served %d bytes, want the file's %d", len(body), len(want))
	}

	// Range requests are what let a player seek without pulling the file
	// again, and they are the reason this serves content rather than copying.
	if res.Header.Get("Accept-Ranges") != "bytes" {
		t.Error("the response does not advertise ranges")
	}
	req, err := http.NewRequest(http.MethodGet, h.srv.URL+"/v1/tracks/"+id+"/audio", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=0-63")
	part, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer part.Body.Close()
	if part.StatusCode != http.StatusPartialContent {
		t.Fatalf("a range request returned %d, want 206", part.StatusCode)
	}
	chunk, _ := io.ReadAll(part.Body)
	if !bytes.Equal(chunk, want[:64]) {
		t.Errorf("the range returned %d bytes, want the first 64 of the file", len(chunk))
	}

	h.do(t, http.MethodGet, "/v1/tracks/nosuchtrack/audio", nil, http.StatusNotFound)

	// The catalogue is a snapshot, so a track it lists can have been moved
	// since. That is a missing resource rather than a server fault.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	h.do(t, http.MethodGet, "/v1/tracks/"+id+"/audio", nil, http.StatusNotFound)
}

func TestArtworkEndpoints(t *testing.T) {
	h := newHarness(t, 3)
	ff, _ := exec.LookPath("ffmpeg")

	cover := filepath.Join(h.dir, "cover.jpg")
	if b, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=red:s=90x90", "-frames:v", "1", cover).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg cover: %v\n%s", err, b)
	}
	img, err := os.ReadFile(cover)
	if err != nil {
		t.Fatal(err)
	}

	id := h.firstTrack(t)["id"].(string)
	h.do(t, http.MethodGet, "/v1/tracks/"+id+"/artwork", nil, http.StatusNotFound)

	// Upload one.
	req, _ := http.NewRequest(http.MethodPut, h.srv.URL+"/v1/tracks/"+id+"/artwork", bytes.NewReader(img))
	req.Header.Set("Content-Type", "image/jpeg")
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put artwork = %d", resp.StatusCode)
	}

	// Read it back as bytes, with the right content type.
	got, data := h.do(t, http.MethodGet, "/v1/tracks/"+id+"/artwork", nil, http.StatusOK)
	if ct := got.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content type = %q", ct)
	}
	if !bytes.Equal(data, img) {
		t.Errorf("the image came back as %d bytes, sent %d", len(data), len(img))
	}

	// The clipboard, copied from that track and pasted across the rest.
	h.do(t, http.MethodPut, "/v1/clipboard/artwork/from-track/"+id, nil, http.StatusOK)
	_, clip := h.do(t, http.MethodGet, "/v1/clipboard/artwork", nil, http.StatusOK)
	if !bytes.Equal(clip, img) {
		t.Error("the clipboard did not hold what was copied to it")
	}

	job := h.post(t, "/v1/artwork/batch", map[string]any{
		"selector": map[string]any{"all": true},
		"source":   "clipboard",
	}, http.StatusAccepted)
	done := h.waitJob(t, job["id"].(string))
	if done["state"] != "succeeded" {
		t.Fatalf("artwork batch %v: %v", done["state"], done["error"])
	}
	for _, it := range h.getJSON(t, "/v1/tracks", http.StatusOK)["items"].([]any) {
		if !it.(map[string]any)["hasArt"].(bool) {
			t.Errorf("%v has no art after the paste", it.(map[string]any)["path"])
		}
	}

	// The summary groups identical covers, which is the point of it.
	sum := h.getJSON(t, "/v1/artwork/summary", http.StatusOK)
	groups := sum["groups"].([]any)
	if len(groups) != 1 || groups[0].(map[string]any)["tracks"].(float64) != 3 {
		t.Errorf("summary = %v", sum)
	}

	// And removal.
	h.do(t, http.MethodDelete, "/v1/tracks/"+id+"/artwork", nil, http.StatusNoContent)
	if h.getJSON(t, "/v1/tracks/"+id, http.StatusOK)["hasArt"].(bool) {
		t.Error("artwork survived deletion")
	}
	h.do(t, http.MethodDelete, "/v1/clipboard/artwork", nil, http.StatusNoContent)
	h.do(t, http.MethodGet, "/v1/clipboard/artwork", nil, http.StatusNotFound)
}

func TestArtworkFromFolder(t *testing.T) {
	h := newHarness(t, 3)
	ff, _ := exec.LookPath("ffmpeg")

	// The cover sits beside the tracks, as a downloaded library stores it.
	music := filepath.Join(h.dir, "music", "Elvis Presley", "Sun Sessions")
	if b, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=blue:s=80x80", "-frames:v", "1",
		filepath.Join(music, "folder.jpg")).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, b)
	}

	job := h.post(t, "/v1/artwork/batch", map[string]any{
		"selector": map[string]any{"all": true},
		"source":   "folder",
	}, http.StatusAccepted)
	done := h.waitJob(t, job["id"].(string))
	if r := done["result"].(map[string]any); r["changed"].(float64) != 3 {
		t.Fatalf("folder art applied to %v tracks, want 3", r["changed"])
	}
	for _, it := range h.getJSON(t, "/v1/tracks", http.StatusOK)["items"].([]any) {
		if !it.(map[string]any)["hasArt"].(bool) {
			t.Error("a track did not get the folder art")
		}
	}
}

func TestStripAndRestore(t *testing.T) {
	h := newHarness(t, 3)

	// The default is a dry run, so a body without dryRun must not write.
	job := h.post(t, "/v1/strip", map[string]any{
		"selector": map[string]any{"all": true},
	}, http.StatusAccepted)
	done := h.waitJob(t, job["id"].(string))
	res := done["result"].(map[string]any)
	if res["dryRun"] != true {
		t.Fatal("strip defaulted to writing rather than to a dry run")
	}
	if h.getJSON(t, "/v1/tracks?q=comment:noise", http.StatusOK)["total"].(float64) != 3 {
		t.Fatal("the dry run modified files")
	}

	// For real, with a backup.
	job = h.post(t, "/v1/strip", map[string]any{
		"selector": map[string]any{"all": true},
		"dryRun":   false,
		"backup":   true,
	}, http.StatusAccepted)
	done = h.waitJob(t, job["id"].(string))
	res = done["result"].(map[string]any)
	if res["changed"].(float64) != 3 {
		t.Fatalf("strip changed %v, want 3", res["changed"])
	}
	backupID, _ := res["backupId"].(string)
	if backupID == "" {
		t.Fatal("no backup id was returned")
	}
	if h.getJSON(t, "/v1/tracks?q=comment:noise", http.StatusOK)["total"].(float64) != 0 {
		t.Error("the comment survived the strip")
	}
	// The keep list must have been honoured.
	if h.getJSON(t, "/v1/tracks?q=artist:presly", http.StatusOK)["total"].(float64) != 3 {
		t.Error("the strip removed the artist, which is on the keep list")
	}

	_, list := h.do(t, http.MethodGet, "/v1/backups", nil, http.StatusOK)
	if !strings.Contains(string(list), backupID) {
		t.Errorf("the backup is not listed: %s", list)
	}

	job = h.post(t, "/v1/restore", map[string]any{"backupId": backupID}, http.StatusAccepted)
	done = h.waitJob(t, job["id"].(string))
	if done["state"] != "succeeded" {
		t.Fatalf("restore %v: %v", done["state"], done["error"])
	}
	if h.getJSON(t, "/v1/tracks?q=comment:noise", http.StatusOK)["total"].(float64) != 3 {
		t.Error("the comment did not come back")
	}
	h.post(t, "/v1/restore", map[string]any{"backupId": "nosuchbackup"}, http.StatusNotFound)
}

func TestJobsEndpoints(t *testing.T) {
	h := newHarness(t, 2)

	job := h.post(t, "/v1/tracks/batch", map[string]any{
		"selector": map[string]any{"all": true},
		"set":      map[string]any{"genre": "Rock"},
	}, http.StatusAccepted)
	id := job["id"].(string)
	h.waitJob(t, id)

	_, list := h.do(t, http.MethodGet, "/v1/jobs", nil, http.StatusOK)
	if !strings.Contains(string(list), id) {
		t.Errorf("the job is not listed: %s", list)
	}
	h.do(t, http.MethodGet, "/v1/jobs/nosuchjob", nil, http.StatusNotFound)
	// Cancelling a finished job is a conflict, not a success.
	h.do(t, http.MethodDelete, "/v1/jobs/"+id, nil, http.StatusConflict)
	h.do(t, http.MethodDelete, "/v1/jobs/nosuchjob", nil, http.StatusNotFound)
}

// TestEventStream covers the mechanism that keeps several interfaces in step.
func TestEventStream(t *testing.T) {
	h := newHarness(t, 2)

	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/v1/events", nil)
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}

	seen := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "event: ") {
				select {
				case seen <- strings.TrimPrefix(sc.Text(), "event: "):
				default:
				}
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	id := h.firstTrack(t)["id"].(string)
	h.do(t, http.MethodPatch, "/v1/tracks/"+id, strings.NewReader(`{"genre":"Rock"}`), http.StatusOK)

	select {
	case e := <-seen:
		if e != "tracks.changed" {
			t.Errorf("first event was %q, want tracks.changed", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event arrived for an edit made on another connection")
	}
}

func TestAuthentication(t *testing.T) {
	h := newHarness(t, 1)
	// A second server over the same service, this one requiring a token.
	secured := httptest.NewServer(New(h.svc, Options{Token: "sekrit", AllowCrossOrigin: true}))
	defer secured.Close()

	get := func(path, token string) int {
		req, _ := http.NewRequest(http.MethodGet, secured.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := secured.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if got := get("/v1/stats", ""); got != http.StatusUnauthorized {
		t.Errorf("no token gave %d, want 401", got)
	}
	if got := get("/v1/stats", "wrong"); got != http.StatusUnauthorized {
		t.Errorf("a wrong token gave %d, want 401", got)
	}
	if got := get("/v1/stats", "sekrit"); got != http.StatusOK {
		t.Errorf("the right token gave %d, want 200", got)
	}
	// The schema and its viewer describe the interface, not the library, and
	// stay reachable so a client author can read them.
	if got := get("/openapi.yaml", ""); got != http.StatusOK {
		t.Errorf("the schema needs a token: %d", got)
	}
	if got := get("/docs", ""); got != http.StatusOK {
		t.Errorf("the docs need a token: %d", got)
	}
}

// TestCrossOriginIsOffWithoutAToken guards a real hazard: a loopback server
// with permissive headers could be driven by any web page the user visits, and
// this API rewrites music files.
func TestCrossOriginIsOffWithoutAToken(t *testing.T) {
	h := newHarness(t, 1)
	resp, _ := h.do(t, http.MethodGet, "/v1/stats", nil, http.StatusOK)
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("an unauthenticated server sent CORS headers: %q", got)
	}

	secured := httptest.NewServer(New(h.svc, Options{Token: "t", AllowCrossOrigin: true}))
	defer secured.Close()
	req, _ := http.NewRequest(http.MethodGet, secured.URL+"/v1/stats", nil)
	req.Header.Set("Authorization", "Bearer t")
	r2, err := secured.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.Header.Get("Access-Control-Allow-Origin") == "" {
		t.Error("an authenticated server did not send CORS headers")
	}
}

func TestSchemaIsServed(t *testing.T) {
	h := newHarness(t, 1)

	resp, yamlBody := h.do(t, http.MethodGet, "/openapi.yaml", nil, http.StatusOK)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/yaml") {
		t.Errorf("yaml content type = %q", resp.Header.Get("Content-Type"))
	}
	if !bytes.Contains(yamlBody, []byte("openapi: 3.1")) {
		t.Error("the served schema is not the one that was embedded")
	}

	_, jsonBody := h.do(t, http.MethodGet, "/openapi.json", nil, http.StatusOK)
	var doc map[string]any
	if err := json.Unmarshal(jsonBody, &doc); err != nil {
		t.Fatalf("the JSON schema does not parse: %v", err)
	}
	if len(doc["paths"].(map[string]any)) == 0 {
		t.Error("the JSON schema declares no paths")
	}

	_, docs := h.do(t, http.MethodGet, "/docs", nil, http.StatusOK)
	if !bytes.Contains(docs, []byte("openapi.json")) {
		t.Error("the docs page does not fetch the schema")
	}
	// The page must not depend on the network: a NAS may have none.
	for _, host := range []string{"http://", "https://"} {
		if bytes.Contains(docs, []byte(host)) {
			t.Errorf("the docs page references %s, so it would not work offline", host)
		}
	}
}

// TestScanIsNotConcurrent covers the case a client will hit by retrying: two
// scans would each rebuild the whole catalogue and whichever finished last
// would silently win.
func TestScanIsNotConcurrent(t *testing.T) {
	h := newHarness(t, 40)

	// Nothing running to begin with, but the catalogue remembers a scan.
	st := h.getJSON(t, "/v1/scans", http.StatusOK)
	if st["running"].(bool) {
		t.Fatal("a scan is running before one was asked for")
	}
	if st["tracks"].(float64) != 40 {
		t.Errorf("status reports %v tracks", st["tracks"])
	}
	if st["scannedAt"] == nil {
		t.Error("status has no scannedAt after a scan")
	}

	root := filepath.Join(h.dir, "music")
	first := h.post(t, "/v1/scans", map[string]any{"roots": []string{root}}, http.StatusAccepted)

	// A second POST while it runs is refused, and names the running job.
	second := h.post(t, "/v1/scans", map[string]any{"roots": []string{root}}, http.StatusConflict)
	if second["code"] != "scan_running" {
		t.Fatalf("second scan gave %v, want scan_running", second)
	}
	if second["jobId"] != first["id"] {
		t.Errorf("the error names job %v, want the running %v", second["jobId"], first["id"])
	}

	h.waitJob(t, first["id"].(string))

	// Exactly one scan job was created by this test beyond the harness's own.
	// The listing filters server-side, which is the point of the parameter.
	_, list := h.do(t, http.MethodGet, "/v1/jobs?kind=scan", nil, http.StatusOK)
	var page struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(list, &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 { // the harness's initial scan, plus this one
		t.Errorf("%d scan jobs exist, want 2", page.Total)
	}
	for _, j := range page.Items {
		if j["kind"] != "scan" {
			t.Errorf("?kind=scan returned a %v job", j["kind"])
		}
	}

	// And once finished, another is allowed again.
	h.post(t, "/v1/scans", map[string]any{"roots": []string{root}}, http.StatusAccepted)
}

func TestRenameAndDeleteTrack(t *testing.T) {
	h := newHarness(t, 3)
	track := h.firstTrack(t)
	id, version := track["id"].(string), track["version"].(string)

	// A rename returns the track under a new identity, because the id is
	// derived from the path. Location says where it now answers.
	resp, data := h.do(t, http.MethodPost, "/v1/tracks/"+id+"/rename",
		strings.NewReader(`{"path":"01 Blue Suede Shoes.mp3"}`), http.StatusOK)
	var moved map[string]any
	if err := json.Unmarshal(data, &moved); err != nil {
		t.Fatalf("rename returned unparseable JSON: %v\n%s", err, data)
	}
	newID := moved["id"].(string)
	if newID == id {
		t.Error("the id did not change, but the path did")
	}
	if got, want := resp.Header.Get("Location"), "/v1/tracks/"+newID; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	if !strings.HasSuffix(moved["path"].(string), "01 Blue Suede Shoes.mp3") {
		t.Errorf("the new path is %q", moved["path"])
	}
	h.do(t, http.MethodGet, "/v1/tracks/"+id, nil, http.StatusNotFound)
	h.do(t, http.MethodGet, "/v1/tracks/"+newID, nil, http.StatusOK)

	// A destination that is taken is a 409, but with its own code: the answer
	// is another name rather than another read.
	second := h.getJSON(t, "/v1/tracks?sort=track&limit=2", http.StatusOK)["items"].([]any)[1].(map[string]any)
	_, taken := h.do(t, http.MethodPost, "/v1/tracks/"+newID+"/rename",
		strings.NewReader(`{"path":"`+filepath.Base(second["path"].(string))+`"}`), http.StatusConflict)
	if !strings.Contains(string(taken), `"exists"`) {
		t.Errorf("a taken name returned %s", taken)
	}

	// The rules that bound what a move can reach are the caller's mistake.
	h.do(t, http.MethodPost, "/v1/tracks/"+newID+"/rename",
		strings.NewReader(`{"path":"01 Blue Suede Shoes.flac"}`), http.StatusBadRequest)
	h.do(t, http.MethodPost, "/v1/tracks/"+newID+"/rename",
		strings.NewReader(`{"path":"`+filepath.Join(h.dir, "escaped.mp3")+`"}`), http.StatusBadRequest)

	// Deleting with the version from before the rename must be refused: it is
	// the file's state the client has not seen.
	req, _ := http.NewRequest(http.MethodDelete, h.srv.URL+"/v1/tracks/"+newID, nil)
	req.Header.Set("If-Match", `"`+version+`"`)
	stale, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	stale.Body.Close()
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("a stale If-Match on a delete returned %d, want 409", stale.StatusCode)
	}

	path := moved["path"].(string)
	req2, _ := http.NewRequest(http.MethodDelete, h.srv.URL+"/v1/tracks/"+newID, nil)
	req2.Header.Set("If-Match", `"`+moved["version"].(string)+`"`)
	gone, err := h.srv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	gone.Body.Close()
	if gone.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", gone.StatusCode)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the file is still on disk: %v", err)
	}
	h.do(t, http.MethodGet, "/v1/tracks/"+newID, nil, http.StatusNotFound)
	if total := h.getJSON(t, "/v1/tracks", http.StatusOK)["total"].(float64); total != 2 {
		t.Errorf("the library holds %v tracks, want 2", total)
	}
	h.do(t, http.MethodDelete, "/v1/tracks/nosuchid", nil, http.StatusNotFound)
}

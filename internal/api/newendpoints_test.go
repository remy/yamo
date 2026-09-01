package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/remy/yamo/internal/library"
)

// newTokenHarness is a harness behind a bearer token, for the checks that are
// about what happens with one and without.
func newTokenHarness(t *testing.T, tracks int, token string) *harness {
	t.Helper()
	return newHarnessOpts(t, tracks, Options{Token: token})
}

// patch edits one track, optionally with an If-Match.
func (h *harness) patch(t *testing.T, path string, body any, ifMatch string, want int) map[string]any {
	t.Helper()
	buf, _ := json.Marshal(body)
	headers := map[string]string{}
	if ifMatch != "" {
		headers["If-Match"] = ifMatch
	}
	_, data := h.doWith(t, http.MethodPatch, path, bytes.NewReader(buf), want, headers)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}

// coverBytes is a JPEG large enough that scaling it down is visibly cheaper.
func coverBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0x30, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Capabilities is the one operation served without a token, because a client
// needs to know whether a token is required before it can present one.
func TestCapabilitiesNeedsNoToken(t *testing.T) {
	h := newTokenHarness(t, 2, "s3cret")
	h.token = "" // no Authorization header at all

	_, data := h.do(t, http.MethodGet, "/v1/capabilities", nil, http.StatusOK)
	var caps library.Capabilities
	if err := json.Unmarshal(data, &caps); err != nil {
		t.Fatal(err)
	}
	if !caps.AuthRequired {
		t.Error("authRequired is false on a server that requires a token")
	}
	if caps.Name != "yamo" {
		t.Errorf("name = %q", caps.Name)
	}
	if len(caps.Formats) == 0 {
		t.Fatal("no formats listed")
	}
	// The thing a client cannot learn any other way: which formats this build
	// writes.
	var sawWritable, sawReadOnly bool
	for _, f := range caps.Formats {
		if f.Format == "mp3" && f.Write {
			sawWritable = true
		}
		if f.Format == "wma" && !f.Write {
			sawReadOnly = true
		}
	}
	if !sawWritable || !sawReadOnly {
		t.Errorf("formats do not distinguish writable from read-only: %+v", caps.Formats)
	}
	if caps.Limits.MaxPageSize != library.MaxLimit {
		t.Errorf("maxPageSize = %d, want %d", caps.Limits.MaxPageSize, library.MaxLimit)
	}
	if len(caps.Fields) == 0 {
		t.Error("no editable fields, so a client cannot build a field picker")
	}

	// And nothing about the library leaks through it.
	if strings.Contains(string(data), h.dir) {
		t.Error("capabilities names a path on this machine")
	}
}

// Everything else still needs the token — the public endpoint must not have
// opened a hole.
func TestOtherEndpointsStillNeedToken(t *testing.T) {
	h := newTokenHarness(t, 2, "s3cret")
	h.token = ""
	for _, path := range []string{"/v1/tracks", "/v1/me", "/v1/stats", "/v1/duplicates", "/v1/folders"} {
		resp, _ := h.do(t, http.MethodGet, path, nil, http.StatusUnauthorized)
		if got := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
			t.Errorf("%s: WWW-Authenticate = %q, want a Bearer challenge", path, got)
		}
	}
}

// /me answers the question a client otherwise has to provoke a failure to ask.
func TestMe(t *testing.T) {
	h := newTokenHarness(t, 2, "s3cret")

	me := h.getJSON(t, "/v1/me", http.StatusOK)
	if me["authenticated"] != true {
		t.Errorf("authenticated = %v", me["authenticated"])
	}
	if me["tokenRequired"] != true {
		t.Errorf("tokenRequired = %v on a server that requires one", me["tokenRequired"])
	}

	h.token = "wrong"
	h.do(t, http.MethodGet, "/v1/me", nil, http.StatusUnauthorized)
}

// The pre-flight for a strip: what is in this file, rather than what the
// catalogue made of it.
func TestRawTags(t *testing.T) {
	h := newHarness(t, 1)
	track := h.firstTrack(t)

	raw := h.getJSON(t, "/v1/tracks/"+track["id"].(string)+"/tags", http.StatusOK)
	if raw["container"] != "ID3v2" {
		t.Errorf("container = %v, want ID3v2", raw["container"])
	}
	tags, _ := raw["tags"].([]any)
	if len(tags) == 0 {
		t.Fatal("no raw tags listed for a file ffmpeg wrote metadata into")
	}

	// Native keys, not canonical field names, and the keep flag that answers
	// the next question.
	var sawFrameID, sawKept bool
	for _, entry := range tags {
		m := entry.(map[string]any)
		key, _ := m["key"].(string)
		if strings.HasPrefix(key, "T") && len(key) == 4 {
			sawFrameID = true
		}
		if m["kept"] == true {
			sawKept = true
		}
	}
	if !sawFrameID {
		t.Errorf("no ID3 frame ids among %v", tags)
	}
	if !sawKept {
		t.Error("nothing is marked as kept, though the default list keeps the title")
	}

	h.do(t, http.MethodGet, "/v1/tracks/nosuchtrack/tags", nil, http.StatusNotFound)
}

// A grid asks for one cover per tile, and the full-size image is what makes
// that unaffordable.
func TestArtworkThumbnail(t *testing.T) {
	h := newHarness(t, 1)
	track := h.firstTrack(t)
	id := track["id"].(string)

	full := coverBytes(t, 1000, 1000)
	h.do(t, http.MethodPut, "/v1/tracks/"+id+"/artwork", bytes.NewReader(full), http.StatusOK)

	_, big := h.do(t, http.MethodGet, "/v1/tracks/"+id+"/artwork", nil, http.StatusOK)
	_, small := h.do(t, http.MethodGet, "/v1/tracks/"+id+"/artwork?size=128", nil, http.StatusOK)

	if len(small) >= len(big) {
		t.Errorf("the thumbnail is %d bytes and the original %d", len(small), len(big))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(small))
	if err != nil {
		t.Fatalf("the thumbnail does not decode: %v", err)
	}
	if cfg.Width != 128 || cfg.Height != 128 {
		t.Errorf("thumbnail is %d×%d, want 128×128", cfg.Width, cfg.Height)
	}

	// Out of bounds is the caller's mistake, not a silently clamped value.
	h.do(t, http.MethodGet, "/v1/tracks/"+id+"/artwork?size=9999", nil, http.StatusBadRequest)
}

// A cover re-requested every time the grid opens is a few hundred kilobytes
// that have not changed.
func TestConditionalGet(t *testing.T) {
	h := newHarness(t, 1)
	track := h.firstTrack(t)
	id := track["id"].(string)

	// The track record.
	resp, _ := h.do(t, http.MethodGet, "/v1/tracks/"+id, nil, http.StatusOK)
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on a track")
	}
	resp, body := h.doWith(t, http.MethodGet, "/v1/tracks/"+id, nil,
		http.StatusNotModified, map[string]string{"If-None-Match": etag})
	if len(body) != 0 {
		t.Errorf("a 304 carried %d bytes of body", len(body))
	}
	if resp.Header.Get("ETag") == "" {
		t.Error("the 304 dropped the ETag, so the next request would be unconditional again")
	}

	// The cover.
	h.do(t, http.MethodPut, "/v1/tracks/"+id+"/artwork",
		bytes.NewReader(coverBytes(t, 300, 300)), http.StatusOK)
	resp, _ = h.do(t, http.MethodGet, "/v1/tracks/"+id+"/artwork", nil, http.StatusOK)
	coverTag := resp.Header.Get("ETag")
	h.doWith(t, http.MethodGet, "/v1/tracks/"+id+"/artwork", nil,
		http.StatusNotModified, map[string]string{"If-None-Match": coverTag})

	// A thumbnail is a different resource, so it must not be answered by the
	// full-size image's validator.
	h.doWith(t, http.MethodGet, "/v1/tracks/"+id+"/artwork?size=64", nil,
		http.StatusOK, map[string]string{"If-None-Match": coverTag})

	// And editing the track invalidates it.
	h.patch(t, "/v1/tracks/"+id, map[string]any{"comment": "changed"}, "", http.StatusOK)
	h.doWith(t, http.MethodGet, "/v1/tracks/"+id, nil,
		http.StatusOK, map[string]string{"If-None-Match": etag})
}

// A cover is a write to the file like any other, and two clients pasting
// different art onto one album should not be settled by whoever finished last.
func TestArtworkIfMatch(t *testing.T) {
	h := newHarness(t, 1)
	track := h.firstTrack(t)
	id := track["id"].(string)
	stale := track["version"].(string)

	h.doWith(t, http.MethodPut, "/v1/tracks/"+id+"/artwork",
		bytes.NewReader(coverBytes(t, 200, 200)), http.StatusOK,
		map[string]string{"If-Match": stale})

	// The version has moved on, so the same one is now refused.
	h.doWith(t, http.MethodPut, "/v1/tracks/"+id+"/artwork",
		bytes.NewReader(coverBytes(t, 220, 220)), http.StatusConflict,
		map[string]string{"If-Match": stale})

	h.doWith(t, http.MethodDelete, "/v1/tracks/"+id+"/artwork", nil,
		http.StatusConflict, map[string]string{"If-Match": stale})

	// With the current one it goes through.
	current := h.getJSON(t, "/v1/tracks/"+id, http.StatusOK)["version"].(string)
	h.doWith(t, http.MethodDelete, "/v1/tracks/"+id+"/artwork", nil,
		http.StatusNoContent, map[string]string{"If-Match": current})
}

// Refused rather than truncated: half a JPEG embedded in every selected track
// would be a corrupt cover and no error anywhere.
func TestOversizedUpload(t *testing.T) {
	h := newHarness(t, 1)
	id := h.firstTrack(t)["id"].(string)

	huge := make([]byte, library.MaxImageBytes+1024)
	copy(huge, []byte{0xFF, 0xD8, 0xFF}) // looks like a JPEG
	h.do(t, http.MethodPut, "/v1/tracks/"+id+"/artwork",
		bytes.NewReader(huge), http.StatusRequestEntityTooLarge)
	h.do(t, http.MethodPut, "/v1/clipboard/artwork",
		bytes.NewReader(huge), http.StatusRequestEntityTooLarge)
}

// The operation the API exists for, made reversible.
func TestBatchEditUndoOverHTTP(t *testing.T) {
	h := newHarness(t, 3)

	job := h.post(t, "/v1/tracks/batch", map[string]any{
		"selector": map[string]any{"all": true},
		"set":      map[string]any{"artist": "Elvis Presley"},
	}, http.StatusAccepted)
	done := h.waitJob(t, job["id"].(string))

	// Journalled without being asked, which is the point.
	backupID, _ := done["backupId"].(string)
	if backupID == "" {
		t.Fatalf("a batch edit recorded no journal: %v", done)
	}
	if got := h.getJSON(t, "/v1/tracks?limit=1", http.StatusOK); got["items"].([]any)[0].(map[string]any)["artist"] != "Elvis Presley" {
		t.Fatal("the edit did not apply")
	}

	// The journal describes itself before anybody restores it.
	detail := h.getJSON(t, "/v1/backups/"+backupID, http.StatusOK)
	if detail["kind"] != "edit" {
		t.Errorf("kind = %v, want edit", detail["kind"])
	}
	if detail["jobId"] != done["id"] {
		t.Errorf("the journal names job %v, want %v", detail["jobId"], done["id"])
	}

	undo := h.post(t, "/v1/jobs/"+done["id"].(string)+"/undo", nil, http.StatusAccepted)
	if u := h.waitJob(t, undo["id"].(string)); u["state"] != "succeeded" {
		t.Fatalf("undo %v: %v", u["state"], u["error"])
	}
	// Back to the misspelling the fixtures carry.
	if got := h.getJSON(t, "/v1/tracks?limit=1", http.StatusOK); got["items"].([]any)[0].(map[string]any)["artist"] != "Elvis Presly" {
		t.Errorf("the undo did not restore the artist: %v", got["items"])
	}

	// And the journal can be discarded, since nothing expires them.
	h.do(t, http.MethodDelete, "/v1/backups/"+backupID, nil, http.StatusNoContent)
	h.do(t, http.MethodGet, "/v1/backups/"+backupID, nil, http.StatusNotFound)
}

// A job with no journal is not undoable, and says so distinguishably.
func TestUndoWithoutJournalOverHTTP(t *testing.T) {
	h := newHarness(t, 1)
	job := h.post(t, "/v1/tracks/batch", map[string]any{
		"selector": map[string]any{"all": true},
		"set":      map[string]any{"genre": "Rockabilly"},
		"backup":   false,
	}, http.StatusAccepted)
	done := h.waitJob(t, job["id"].(string))
	if done["backupId"] != nil {
		t.Fatalf("backup:false still journalled: %v", done["backupId"])
	}
	h.post(t, "/v1/jobs/"+done["id"].(string)+"/undo", nil, http.StatusNotFound)
	h.post(t, "/v1/jobs/nosuchjob/undo", nil, http.StatusNotFound)
}

// Renaming a whole selection after the tags each file carries.
func TestRenameTracksOverHTTP(t *testing.T) {
	h := newHarness(t, 3)

	dry := h.post(t, "/v1/tracks/rename", map[string]any{
		"selector": map[string]any{"all": true},
		"template": "$track $title",
		"dryRun":   true,
	}, http.StatusAccepted)
	res := h.waitJob(t, dry["id"].(string))["result"].(map[string]any)
	if res["changed"].(float64) != 3 {
		t.Errorf("dry run would change %v, want 3", res["changed"])
	}
	if len(res["samples"].([]any)) == 0 {
		t.Error("a dry run carried no samples")
	}

	real := h.post(t, "/v1/tracks/rename", map[string]any{
		"selector": map[string]any{"all": true},
		"template": "$track $title",
	}, http.StatusAccepted)
	done := h.waitJob(t, real["id"].(string))
	if done["kind"] != "rename" {
		t.Errorf("kind = %v, want rename", done["kind"])
	}
	if done["backupId"] == nil {
		t.Error("a rename recorded no journal")
	}

	page := h.getJSON(t, "/v1/tracks?sort=track", http.StatusOK)
	first := page["items"].([]any)[0].(map[string]any)["path"].(string)
	if !strings.HasSuffix(first, "01 Track 1.mp3") {
		t.Errorf("first track is %q, want it named after its tags", first)
	}

	// A template naming nothing is the caller's mistake, and says which part.
	h.post(t, "/v1/tracks/rename", map[string]any{
		"selector": map[string]any{"all": true},
		"template": "no fields here",
	}, http.StatusBadRequest)
}

// Browsing by folder, for a library whose tags are not good enough to browse
// by album.
func TestFoldersOverHTTP(t *testing.T) {
	h := newHarness(t, 3)

	roots := h.getJSON(t, "/v1/folders", http.StatusOK)
	items := roots["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("root listing has %d entries, want 1: %v", len(items), items)
	}
	root := items[0].(map[string]any)
	if root["descendants"].(float64) != 3 {
		t.Errorf("the root holds %v descendants, want 3", root["descendants"])
	}

	// One level down, then the album folder itself.
	next := h.getJSON(t, "/v1/folders?path="+root["path"].(string), http.StatusOK)
	artist := next["items"].([]any)[0].(map[string]any)
	if artist["name"] != "Elvis Presley" {
		t.Errorf("name = %v", artist["name"])
	}

	// The query reselects what the folder claims to hold.
	tracks := h.getJSON(t, "/v1/tracks?q="+queryEscape(artist["query"].(string)), http.StatusOK)
	if tracks["total"].(float64) != artist["descendants"].(float64) {
		t.Errorf("the folder's query selects %v, but it claims %v descendants",
			tracks["total"], artist["descendants"])
	}
}

// Duplicates are invisible from a search: the copies sort next to each other,
// look identical, and nothing counts them.
func TestDuplicatesOverHTTP(t *testing.T) {
	h := newHarness(t, 3)

	// The fixtures are three distinct titles, so nothing is a duplicate yet.
	page := h.getJSON(t, "/v1/duplicates", http.StatusOK)
	if page["total"].(float64) != 0 {
		t.Errorf("found %v duplicate groups in a library with none", page["total"])
	}

	// Give two tracks the same title, which is exactly the merged-library case.
	items := h.getJSON(t, "/v1/tracks?sort=track", http.StatusOK)["items"].([]any)
	for _, it := range items[:2] {
		m := it.(map[string]any)
		h.patch(t, "/v1/tracks/"+m["id"].(string), map[string]any{"title": "Blue Suede Shoes"}, "", http.StatusOK)
	}

	page = h.getJSON(t, "/v1/duplicates", http.StatusOK)
	if page["total"].(float64) != 1 {
		t.Fatalf("found %v groups, want 1: %v", page["total"], page["items"])
	}
	g := page["items"].([]any)[0].(map[string]any)
	if g["tracks"].(float64) != 2 {
		t.Errorf("group has %v tracks, want 2", g["tracks"])
	}
	if by := page["by"].([]any); len(by) != 2 || by[0] != "artist" || by[1] != "title" {
		t.Errorf("by = %v, want [artist title]", by)
	}

	// An explicit key is echoed back, so a client knows what was understood.
	other := h.getJSON(t, "/v1/duplicates?by=title,nonsense", http.StatusOK)
	if by := other["by"].([]any); len(by) != 1 || by[0] != "title" {
		t.Errorf("by = %v, want just [title] with the unknown name dropped", by)
	}
}

// The report read before deciding what to change has to describe the same set
// the change will act on.
func TestArtworkSummaryTakesASelector(t *testing.T) {
	h := newHarness(t, 3)
	items := h.getJSON(t, "/v1/tracks?sort=track", http.StatusOK)["items"].([]any)
	first := items[0].(map[string]any)["id"].(string)

	all := h.getJSON(t, "/v1/artwork/summary", http.StatusOK)
	if all["tracks"].(float64) != 3 {
		t.Errorf("summary covers %v tracks, want the whole library", all["tracks"])
	}

	one := h.getJSON(t, "/v1/artwork/summary?ids="+first, http.StatusOK)
	if one["tracks"].(float64) != 1 {
		t.Errorf("an ids selector covered %v tracks, want 1", one["tracks"])
	}
}

// Writing the covers back out is the direction source:folder does not go.
func TestExportArtworkOverHTTP(t *testing.T) {
	h := newHarness(t, 3)

	job := h.post(t, "/v1/artwork/batch", map[string]any{
		"selector": map[string]any{"all": true},
		"source":   "upload",
		"image":    base64Of(coverBytes(t, 300, 300)),
	}, http.StatusAccepted)
	h.waitJob(t, job["id"].(string))

	export := h.post(t, "/v1/artwork/export", map[string]any{
		"selector": map[string]any{"all": true},
	}, http.StatusAccepted)
	done := h.waitJob(t, export["id"].(string))
	if done["kind"] != "export" {
		t.Errorf("kind = %v, want export", done["kind"])
	}
	res := done["result"].(map[string]any)
	// Three tracks in one folder is one write, not three.
	if res["directories"].(float64) != 1 || res["changed"].(float64) != 1 {
		t.Errorf("wrote %v images across %v directories, want 1 and 1", res["changed"], res["directories"])
	}

	// A filename with a path in it would make this a way to write anywhere.
	h.post(t, "/v1/artwork/export", map[string]any{
		"selector": map[string]any{"all": true},
		"filename": "../../escape.jpg",
	}, http.StatusBadRequest)
}

// The backups listing is paged like every other listing.
func TestBackupsArePaged(t *testing.T) {
	h := newHarness(t, 2)
	for _, genre := range []string{"Rock", "Rockabilly", "Blues"} {
		job := h.post(t, "/v1/tracks/batch", map[string]any{
			"selector": map[string]any{"all": true},
			"set":      map[string]any{"genre": genre},
		}, http.StatusAccepted)
		h.waitJob(t, job["id"].(string))
	}

	page := h.getJSON(t, "/v1/backups?limit=2", http.StatusOK)
	if page["total"].(float64) != 3 {
		t.Errorf("total = %v, want 3", page["total"])
	}
	if len(page["items"].([]any)) != 2 {
		t.Errorf("a limit of 2 returned %d journals", len(page["items"].([]any)))
	}
}

// A split is not an edit: its result carries different fields, and a client
// polling the job list distinguishes them by kind.
func TestSplitReportsItsOwnKind(t *testing.T) {
	h := newHarness(t, 2)
	items := h.getJSON(t, "/v1/tracks?sort=track", http.StatusOK)["items"].([]any)
	for _, it := range items {
		m := it.(map[string]any)
		h.patch(t, "/v1/tracks/"+m["id"].(string),
			map[string]any{"title": "Carl Perkins - Blue Suede Shoes"}, "", http.StatusOK)
	}

	job := h.post(t, "/v1/tracks/split", map[string]any{
		"selector": map[string]any{"all": true},
		"template": "$artist - $title",
	}, http.StatusAccepted)
	done := h.waitJob(t, job["id"].(string))
	if done["kind"] != "split" {
		t.Errorf("kind = %v, want split", done["kind"])
	}
	res := done["result"].(map[string]any)
	if _, ok := res["template"]; !ok {
		t.Error("a split result carries no template, so it is indistinguishable from a batch edit")
	}
}

func queryEscape(s string) string { return url.QueryEscape(s) }

func base64Of(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

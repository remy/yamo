package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/remy/yamo/internal/auth"
	"github.com/remy/yamo/internal/library"
)

// newHarness starts the endpoint over an empty library. Empty is enough for
// everything here: what is under test is the protocol and the argument
// handling, and the operations themselves are already covered where they live.
func newHarness(t *testing.T) *Server {
	t.Helper()
	svc, err := library.Open(library.Options{
		CatalogPath: filepath.Join(t.TempDir(), "catalog.db"),
		NoDiscogs:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return New(svc, Options{})
}

// call posts one JSON-RPC message and returns the decoded response.
func call(t *testing.T, s *Server, method string, params any) map[string]any {
	t.Helper()
	return callAs(t, s, auth.Full, method, params)
}

// callAs posts one message as a caller holding a particular role, the way the
// authenticating wrapper in package api records it.
func callAs(t *testing.T, s *Server, role auth.Role, method string, params any) map[string]any {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	buf, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(buf))
	req = req.WithContext(auth.WithRole(req.Context(), role))
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d, body %s", method, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: response does not decode: %v", method, err)
	}
	return out
}

// callTool runs one tool and returns its text content along with whether the
// call was reported as an error.
func callTool(t *testing.T, s *Server, name string, args map[string]any) (string, bool) {
	t.Helper()
	return callToolAs(t, s, auth.Full, name, args)
}

func callToolAs(t *testing.T, s *Server, role auth.Role, name string, args map[string]any) (string, bool) {
	t.Helper()
	resp := callAs(t, s, role, "tools/call", map[string]any{"name": name, "arguments": args})
	if e, ok := resp["error"]; ok {
		t.Fatalf("%s: protocol error %v", name, e)
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s: no result in %v", name, resp)
	}
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("%s: expected one content block, got %v", name, result["content"])
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	isError, _ := result["isError"].(bool)
	return text, isError
}

func TestInitializeNegotiatesTheVersion(t *testing.T) {
	s := newHarness(t)

	resp := call(t, s, "initialize", map[string]any{"protocolVersion": "2025-03-26"})
	result := resp["result"].(map[string]any)
	if got := result["protocolVersion"]; got != "2025-03-26" {
		t.Errorf("a known version should be echoed back, got %v", got)
	}

	// An unknown one is answered with what this server actually speaks,
	// rather than agreeing to a revision it has never seen.
	resp = call(t, s, "initialize", map[string]any{"protocolVersion": "1999-01-01"})
	result = resp["result"].(map[string]any)
	if got := result["protocolVersion"]; got != preferredVersion {
		t.Errorf("an unknown version should fall back to %s, got %v", preferredVersion, got)
	}
	if _, ok := result["instructions"].(string); !ok {
		t.Error("initialize carries no instructions, which is where the query language is explained")
	}
}

// Every tool has to be usable by something that has only read this listing.
func TestEveryToolIsDescribed(t *testing.T) {
	s := newHarness(t)
	result := call(t, s, "tools/list", nil)["result"].(map[string]any)
	list, _ := result["tools"].([]any)
	if len(list) != len(tools()) {
		t.Fatalf("listed %d tools, the table holds %d", len(list), len(tools()))
	}

	seen := map[string]bool{}
	for _, item := range list {
		tool := item.(map[string]any)
		name, _ := tool["name"].(string)
		if name == "" {
			t.Fatalf("a tool has no name: %v", tool)
		}
		if seen[name] {
			t.Errorf("%s is listed twice", name)
		}
		seen[name] = true
		if d, _ := tool["description"].(string); len(d) < 40 {
			t.Errorf("%s has no useful description; a model picks tools by these", name)
		}
		sch, ok := tool["inputSchema"].(map[string]any)
		if !ok || sch["type"] != "object" {
			t.Errorf("%s has no object input schema", name)
		}
		if sch["additionalProperties"] != false {
			t.Errorf("%s accepts unknown arguments, so a misspelled one would be dropped", name)
		}
	}
}

// The writing tools are the ones worth a hint, and a wrong hint is worse than
// none: a client showing no confirmation for a tool that rewrites files is
// exactly the failure the annotation exists to prevent.
func TestWritingToolsAreMarkedDestructive(t *testing.T) {
	writes := map[string]bool{
		"edit_tracks": true, "split_titles": true, "rename_files": true,
		"strip_tags": true, "set_artwork": true, "undo_job": true,
		"restore_backup": true,
	}
	for _, tool := range tools() {
		if writes[tool.Name] {
			if tool.ReadOnly || !tool.Destructive {
				t.Errorf("%s writes to files but is not marked destructive", tool.Name)
			}
			continue
		}
		if tool.Destructive {
			t.Errorf("%s does not write but is marked destructive", tool.Name)
		}
	}
}

// toolNames returns what tools/list offers a caller with this role.
func toolNames(t *testing.T, s *Server, role auth.Role) []string {
	t.Helper()
	result := callAs(t, s, role, "tools/list", nil)["result"].(map[string]any)
	list, _ := result["tools"].([]any)
	var out []string
	for _, item := range list {
		name, _ := item.(map[string]any)["name"].(string)
		out = append(out, name)
	}
	return out
}

// A read-only caller must not be shown a tool it cannot call. Listing one and
// refusing it afterwards is how a model ends up planning around a step that
// fails at the end of the plan.
func TestReadOnlySeesOnlyReadingTools(t *testing.T) {
	s := newHarness(t)

	full := toolNames(t, s, auth.Full)
	if len(full) != len(tools()) {
		t.Fatalf("a full token was offered %d tools, the table holds %d", len(full), len(tools()))
	}

	readable := map[string]bool{}
	for _, tool := range tools() {
		if tool.ReadOnly {
			readable[tool.Name] = true
		}
	}
	got := toolNames(t, s, auth.ReadOnly)
	if len(got) != len(readable) {
		t.Errorf("a read-only token was offered %d tools, %d are marked read-only", len(got), len(readable))
	}
	for _, name := range got {
		if !readable[name] {
			t.Errorf("%s was offered to a read-only token but writes", name)
		}
	}
}

// A stale plan reaching for a writing tool has to be told why, in something a
// model can read and carry on from.
func TestReadOnlyCannotCallAWritingTool(t *testing.T) {
	s := newHarness(t)
	resp := callAs(t, s, auth.ReadOnly, "tools/call", map[string]any{
		"name":      "edit_tracks",
		"arguments": map[string]any{"all": true, "set": map[string]any{"genre": "Rock"}},
	})
	if resp["error"] != nil {
		t.Fatalf("expected a tool error a model can read, got a protocol error: %v", resp["error"])
	}
	result := resp["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatal("a read-only token was allowed to edit")
	}
	text, _ := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "read") {
		t.Errorf("the refusal should say why, got %q", text)
	}

	// Reading still works, or the token would be useless.
	if _, isErr := callToolAs(t, s, auth.ReadOnly, "search_tracks", map[string]any{}); isErr {
		t.Error("a read-only token could not search")
	}
}

// The handshake has to explain the missing tools, or the model casts about for
// something that was never offered.
func TestReadOnlyInstructionsSaySo(t *testing.T) {
	s := newHarness(t)
	full := call(t, s, "initialize", map[string]any{})["result"].(map[string]any)
	if strings.Contains(full["instructions"].(string), "may only read") {
		t.Error("a full token was told it may only read")
	}
	limited := callAs(t, s, auth.ReadOnly, "initialize", map[string]any{})["result"].(map[string]any)
	if !strings.Contains(limited["instructions"].(string), "may only read") {
		t.Error("a read-only token was not told what it is holding")
	}
}

func TestSearchReturnsAPage(t *testing.T) {
	s := newHarness(t)
	text, isErr := callTool(t, s, "search_tracks", map[string]any{"query": "elvis"})
	if isErr {
		t.Fatalf("search failed: %s", text)
	}
	var page library.ListResult
	if err := json.Unmarshal([]byte(text), &page); err != nil {
		t.Fatalf("the result is not a track page: %v (%s)", err, text)
	}
	if page.Limit != 20 {
		t.Errorf("limit defaulted to %d, expected the MCP default of 20", page.Limit)
	}
}

// An argument the tool does not know has to be reported rather than dropped.
// A model reaching for "dry_run" has said what it wants; quietly applying the
// default instead and answering as though the call was understood is how a
// write ends up meaning something nobody asked for.
//
// Case is not part of this: encoding/json matches field names case-insensitively,
// so "dryrun" is dryRun and is honoured. Only a genuinely different key is
// refused, which is the one that would otherwise be lost.
func TestUnknownArgumentsAreRefused(t *testing.T) {
	s := newHarness(t)
	text, isErr := callTool(t, s, "edit_tracks", map[string]any{
		"query": "artist:elvis", "set": map[string]any{"artist": "Elvis Presley"},
		"dry_run": false,
	})
	if !isErr {
		t.Fatalf("an unknown argument was accepted: %s", text)
	}
	if !strings.Contains(text, "dry_run") {
		t.Errorf("the error should name the offending key, got %q", text)
	}
}

// An empty selector must never be read as "everything".
func TestEmptySelectionIsRefused(t *testing.T) {
	s := newHarness(t)
	text, isErr := callTool(t, s, "edit_tracks", map[string]any{
		"set": map[string]any{"genre": "Rock"},
	})
	if !isErr {
		t.Fatalf("an empty selector was accepted: %s", text)
	}
}

// The default has to be the safe one, and it differs from the HTTP API's,
// so it is worth pinning.
func TestWritesAreDryRunsByDefault(t *testing.T) {
	s := newHarness(t)
	text, isErr := callTool(t, s, "edit_tracks", map[string]any{
		"all": true, "set": map[string]any{"genre": "Rock"},
	})
	if isErr {
		t.Fatalf("edit failed: %s", text)
	}
	var reply struct {
		State  library.JobState `json:"state"`
		Result struct {
			DryRun bool `json:"dryRun"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(text), &reply); err != nil {
		t.Fatalf("the result is not a job: %v (%s)", err, text)
	}
	if reply.State != library.JobSucceeded {
		t.Fatalf("the job did not finish within the wait: %s", text)
	}
	if !reply.Result.DryRun {
		t.Error("an edit with no dryRun argument wrote for real")
	}
}

func TestUnknownToolAndMethodAreProtocolErrors(t *testing.T) {
	s := newHarness(t)

	resp := call(t, s, "tools/call", map[string]any{"name": "nonesuch", "arguments": map[string]any{}})
	if resp["error"] == nil {
		t.Error("an unknown tool should be a protocol error, not a tool result")
	}
	resp = call(t, s, "resources/list", nil)
	if resp["error"] == nil {
		t.Error("an unimplemented method should be refused rather than answered")
	}
}

// A notification carries no id and is never answered.
func TestNotificationsAreAcknowledged(t *testing.T) {
	s := newHarness(t)
	body := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, expected 202", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a notification was answered with %s", rec.Body.String())
	}
}

func TestGetIsRefusedWithAnExplanation(t *testing.T) {
	s := newHarness(t)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, expected 405", rec.Code)
	}
	if rec.Header().Get("Allow") != "POST" {
		t.Errorf("no Allow header, got %q", rec.Header().Get("Allow"))
	}
}

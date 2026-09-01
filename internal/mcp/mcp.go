// Package mcp exposes the library over the Model Context Protocol, so that an
// assistant can search a library and correct it the same way the terminal
// browser does.
//
// It is a client of internal/library like every other front end here: it opens
// no music file, holds no catalogue of its own, and can reach nothing the HTTP
// API does not already offer. What it adds is a smaller surface — twenty tools
// rather than forty-five endpoints — and that is the whole design. A model
// choosing between forty-five near-identical operations chooses badly, and the
// parts of the API that move bytes (the audio stream, artwork images, the
// clipboard) are of no use to something that reads text. Those stay HTTP-only.
//
// The transport is the Streamable HTTP one, in its simplest legal form: a
// single JSON-RPC message per POST, answered with a single JSON response. No
// session id is issued, because nothing here is stateful between calls, and no
// server-initiated stream is offered, because no tool needs to push. Progress
// on a long operation is reported through the job it returns, which is the
// same answer the rest of this API gives.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/remy/yamo/internal/auth"
	"github.com/remy/yamo/internal/library"
)

// preferredVersion is the protocol revision this speaks. A client asking for
// one of knownVersions gets its own back, per the specification's negotiation
// rule; anything else is answered with this and left to decide whether it can
// live with it.
const preferredVersion = "2025-06-18"

var knownVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

// maxBody bounds a request. Every argument here is a query, a template or a
// handful of ids; a megabyte is already far more than any of them need, and
// the endpoint is authenticated with the same token that guards the rest.
const maxBody = 1 << 20

// Options tells the tools how the server around them is configured. The
// service does not know how it is being served, and library_stats reports it.
type Options struct {
	AuthRequired bool
	CrossOrigin  bool
}

// Server is the MCP endpoint for a library service.
type Server struct {
	svc    *library.Service
	opts   Options
	tools  []*Tool
	byName map[string]*Tool
}

// New builds the endpoint with the full tool set.
func New(svc *library.Service, opts Options) *Server {
	s := &Server{svc: svc, opts: opts, tools: tools(), byName: map[string]*Tool{}}
	for _, t := range s.tools {
		s.byName[t.Name] = t
	}
	return s
}

// --- JSON-RPC ------------------------------------------------------------

// JSON-RPC error codes. The first four are the specification's; a tool that
// fails does not use any of them, because a failed tool call is a result the
// model is meant to read and act on rather than a protocol fault.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ServeHTTP answers one JSON-RPC message.
//
// Only POST is served. The specification lets a server decline the GET stream
// it would otherwise use to push messages at a client, and this one has
// nothing to push: every tool answers in the response to its own call.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeRPC(w, http.StatusMethodNotAllowed, rpcResponse{
			JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{codeInvalidRequest,
				"this endpoint speaks MCP over POST only; it opens no server-initiated stream"},
		})
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeRPC(w, http.StatusBadRequest, errorResponse(nil, codeParse, "the request body could not be read"))
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		// A batch is an array, and batching was removed from the protocol in
		// 2025-06-18. Saying so is more use than "invalid JSON".
		msg := "the request is not a JSON-RPC message"
		if len(body) > 0 && body[0] == '[' {
			msg = "batched requests are not supported; send one message per POST"
		}
		writeRPC(w, http.StatusBadRequest, errorResponse(nil, codeParse, msg))
		return
	}

	// A message with no id is a notification: it is acknowledged and never
	// answered. "initialized" and "cancelled" both arrive this way.
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	result, rerr := s.dispatch(r.Context(), req.Method, req.Params)
	if rerr != nil {
		writeRPC(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rerr})
		return
	}
	writeRPC(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
}

func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
	// The role rides on the context, put there by whatever authenticated the
	// request. Nothing recorded means nothing to restrict — a server with no
	// token configured, which is the loopback default.
	role := auth.RoleOf(ctx)

	switch method {
	case "initialize":
		return s.initialize(params, role), nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return s.listTools(role), nil
	case "tools/call":
		return s.callTool(ctx, role, params)
	}
	return nil, &rpcError{codeMethodNotFound, fmt.Sprintf("unknown method %q", method)}
}

// initialize answers the handshake.
//
// The instructions are the most valuable thing in this response. A model that
// has not been told the query language will guess at it, and a model that has
// not been told writes are dry runs by default will report work it has not
// done — so both are said here, once, rather than repeated into twenty tool
// descriptions.
func (s *Server) initialize(params json.RawMessage, role auth.Role) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)

	version := preferredVersion
	if knownVersions[p.ProtocolVersion] {
		version = p.ProtocolVersion
	}

	guidance := instructions
	if !role.CanWrite() {
		guidance += readOnlyNote
	}

	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo": map[string]any{
			"name":    "yamo",
			"title":   "YAMO music library",
			"version": library.Version,
		},
		"instructions": guidance,
	}
}

const instructions = `YAMO catalogues a music library and edits the tags inside the files themselves.

Finding things. Every tool that takes a query uses one language:
  elvis                     any text field contains it
  artist:elvis              one field
  artist:"elvis presley"    quoted values may contain spaces
  artist:^elvis  presley$   anchored to the start or the end
  artist:~presly            fuzzy; near misses match and are scored
  year:1977  year:>1980  year:1970-1979
  -genre:christmas          exclude
  album:                    the field is empty
  compilation:1             the Various Artists flag
  artist:elvis year:>1960   terms are ANDed
Matching ignores case and accents. Fields: title, artist, albumartist, album,
genre, composer, comment, year, track, disc, compilation, path, and the sort
forms (titlesort, artistsort, albumsort, albumartistsort, composersort).

Choosing what to change. Writing tools take a selection, not a list of files:
give "query" (the same language), or "ids", or "all": true, which must be set
explicitly so that "everything" is never reached by accident. Send
"expectCount" with the number you told the user you were about to change; the
server refuses if the selection has moved since you counted it.

Writing. Every writing tool is a dry run unless you pass "dryRun": false. Run
it dry, read "matched" and "changed" and the samples, say what it would do,
and only then run it for real. Most writes record an undo journal, so
undo_job takes back what a job did and list_backups finds older ones.

Waiting. Anything that can touch more than one file returns a job. These tools
wait for it and return the finished job, including its result; if it is still
running when the wait is up they return the job id, and get_job polls it.

The library is only as current as its last scan — nothing watches the
filesystem. library_stats says when it was scanned; scan_library refreshes it.`

// readOnlyNote is appended for a caller holding the read-only token.
//
// The tool list it is given already omits everything it cannot call, so this
// is not a rule it has to remember — it is the explanation for why the tools
// it might expect are missing, so that it says "I can only read here" rather
// than casting about for a tool that was never offered.
const readOnlyNote = `

This token may only read. The writing tools — editing, splitting, renaming,
stripping, artwork, scanning and undo — are not offered to it and are absent
from the tool list. Report what you find and what you would change; somebody
holding the full token has to make the change.`

// listTools renders the tool table for the client.
//
// A read-only caller is shown the read-only tools and no others. Listing what
// it cannot call and refusing it afterwards would be worse than useless: a
// model plans with the list it was given, and half of that plan failing at the
// last step is how it ends up reporting work it never did.
func (s *Server) listTools(role auth.Role) map[string]any {
	out := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		if !t.ReadOnly && !role.CanWrite() {
			continue
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"title":       t.Title,
			"description": t.Description,
			"inputSchema": t.Input,
			"annotations": t.annotations(),
		})
	}
	return map[string]any{"tools": out}
}

// callTool runs one tool.
//
// A tool that fails comes back as a successful JSON-RPC response carrying
// isError, which is the protocol's own distinction and the right one: a bad
// query or a count that has moved is something the model should read and
// correct, not a transport fault it should retry blindly.
func (s *Server) callTool(ctx context.Context, role auth.Role, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{codeInvalidParams, "the call has no name and arguments"}
	}
	t, ok := s.byName[p.Name]
	if !ok {
		return nil, &rpcError{codeInvalidParams, fmt.Sprintf("unknown tool %q", p.Name)}
	}

	// A writing tool called by a read-only caller was never in the list it was
	// given, so this is a stale plan rather than a bad request. It comes back
	// as a tool error rather than a protocol one for that reason: a model can
	// read it, understand why, and carry on with what it can do, where a
	// transport failure is something it can only retry.
	if !t.ReadOnly && !role.CanWrite() {
		return toolResult("this token may only read, so "+t.Name+
			" is not available to it; the tools you can call are the ones tools/list returned", true), nil
	}

	result, err := t.Call(ctx, s.svc, s.opts, p.Arguments)
	if err != nil {
		return toolResult(err.Error(), true), nil
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, &rpcError{codeInternal, "the result could not be encoded: " + err.Error()}
	}
	return toolResult(string(encoded), false), nil
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": isError,
	}
}

func errorResponse(id json.RawMessage, code int, msg string) rpcResponse {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{code, msg}}
}

func writeRPC(w http.ResponseWriter, status int, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// --- tool plumbing -------------------------------------------------------

// Tool is one operation offered to a model.
type Tool struct {
	Name        string
	Title       string
	Description string

	// Input is the JSON Schema for the arguments. It is written by hand for
	// the same reason the HTTP handlers are: what a model reads to decide
	// which tool to reach for is prose, and a schema derived from a Go struct
	// carries none of it.
	Input map[string]any

	// ReadOnly, Destructive and Idempotent become the protocol's hints. They
	// are advisory — a client may show a confirmation for a destructive tool
	// — and are set honestly rather than defensively: a tool that rewrites a
	// hundred thousand files says so.
	ReadOnly    bool
	Destructive bool
	Idempotent  bool

	// OpenWorld marks a tool that reaches outside this machine, which here
	// means the Discogs lookup and nothing else.
	OpenWorld bool

	Call func(ctx context.Context, svc *library.Service, opts Options, args json.RawMessage) (any, error)
}

func (t *Tool) annotations() map[string]any {
	a := map[string]any{
		"title":          t.Title,
		"readOnlyHint":   t.ReadOnly,
		"openWorldHint":  t.OpenWorld,
		"idempotentHint": t.Idempotent,
	}
	// A read-only tool destroys nothing by definition, and the protocol only
	// reads destructiveHint when readOnlyHint is false.
	if !t.ReadOnly {
		a["destructiveHint"] = t.Destructive
	}
	return a
}

// decodeArgs reads a call's arguments, rejecting keys the tool does not know.
//
// Strictness is deliberate. An argument that is dropped rather than refused is
// answered as though it had been understood, and for a writing tool that means
// a model asking for one thing and being told it got it while the default
// happened instead. The error is the cheapest possible correction.
func decodeArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}

// selectorArgs is the flattened form of library.Selector.
//
// It is flattened because a nested object is one more thing for a model to get
// wrong, and the fields are few enough that they cost nothing at the top level.
type selectorArgs struct {
	Query       string   `json:"query,omitempty"`
	IDs         []string `json:"ids,omitempty"`
	ExcludeIDs  []string `json:"excludeIds,omitempty"`
	All         bool     `json:"all,omitempty"`
	ExpectCount *int     `json:"expectCount,omitempty"`
}

func (a selectorArgs) selector() library.Selector {
	return library.Selector{
		Query: a.Query, IDs: a.IDs, ExcludeIDs: a.ExcludeIDs,
		All: a.All, ExpectCount: a.ExpectCount,
	}
}

// yes reads a flag that defaults to true when the caller says nothing. Both
// flags that use it — dryRun and backup — are safe in that direction and
// unsafe in the other, so silence has to mean the safe one.
func yes(p *bool) bool { return p == nil || *p }

// no reads a flag that defaults to false.
func no(p *bool) bool { return p != nil && *p }

// errNoJob is the impossible case: an operation that returned neither a job
// nor an error. It is reported rather than dereferenced.
var errNoJob = errors.New("the operation returned no job")

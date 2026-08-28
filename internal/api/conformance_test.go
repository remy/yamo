package api

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	apispec "github.com/remy/tag-manager/api"
	"gopkg.in/yaml.v3"
)

// specDoc is the slice of the schema this test needs.
//
// Path items hold operations keyed by method alongside a shared parameters
// list, so the values are decoded lazily rather than typed as operations.
type specDoc struct {
	Paths map[string]map[string]yaml.Node `yaml:"paths"`
}

type specOp struct {
	OperationID string   `yaml:"operationId"`
	Summary     string   `yaml:"summary"`
	Tags        []string `yaml:"tags"`
}

// httpMethods are the path-item keys that are operations.
var httpMethods = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true, "delete": true,
}

// eachOperation calls fn for every operation in the schema.
func eachOperation(t *testing.T, doc specDoc, fn func(method, path string, op specOp)) {
	t.Helper()
	for path, item := range doc.Paths {
		for key, node := range item {
			if !httpMethods[key] {
				continue
			}
			var op specOp
			if err := node.Decode(&op); err != nil {
				t.Fatalf("%s %s does not decode: %v", key, path, err)
			}
			fn(key, path, op)
		}
	}
}

func loadSpec(t *testing.T) specDoc {
	t.Helper()
	var doc specDoc
	if err := yaml.Unmarshal(apispec.YAML(), &doc); err != nil {
		t.Fatalf("the embedded schema does not parse: %v", err)
	}
	return doc
}

// specRoutes returns the schema's operations as "METHOD /v1/path" patterns,
// in the form the router registers them.
func specRoutes(t *testing.T, doc specDoc) map[string]string {
	out := map[string]string{}
	eachOperation(t, doc, func(method, path string, op specOp) {
		out[strings.ToUpper(method)+" /v1"+path] = op.OperationID
	})
	return out
}

// TestRoutesMatchSchema is the reason the handlers can be written by hand.
//
// A generator would guarantee that the code and the contract agree. Without
// one, this does: every operation in the schema must have a route, and every
// route must be in the schema. A published contract that lies about what the
// server does is worse than no contract.
func TestRoutesMatchSchema(t *testing.T) {
	doc := loadSpec(t)
	want := specRoutes(t, doc)
	if len(want) == 0 {
		t.Fatal("the schema declares no operations")
	}

	// Build a real server so the list comes from the constructor rather than
	// from a second copy of it that could itself drift.
	srv := New(nil, Options{})
	got := map[string]bool{}
	for _, r := range srv.Routes() {
		got[r] = true
	}

	var missing, extra []string
	for route := range want {
		if !got[route] {
			missing = append(missing, route)
		}
	}
	for route := range got {
		if _, ok := want[route]; !ok {
			extra = append(extra, route)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	for _, r := range missing {
		t.Errorf("the schema declares %s (%s) but no route serves it", r, want[r])
	}
	for _, r := range extra {
		t.Errorf("the server serves %s but the schema does not declare it", r)
	}
}

// TestEveryOperationHasAnID checks the schema is usable by a generator, which
// names methods after operationId.
func TestEveryOperationHasAnID(t *testing.T) {
	doc := loadSpec(t)
	seen := map[string]string{}
	eachOperation(t, doc, func(method, path string, op specOp) {
		where := fmt.Sprintf("%s %s", strings.ToUpper(method), path)
		if op.OperationID == "" {
			t.Errorf("%s has no operationId, so a generated client would have no name for it", where)
			return
		}
		if prev, dup := seen[op.OperationID]; dup {
			t.Errorf("operationId %q is used by both %s and %s", op.OperationID, prev, where)
		}
		seen[op.OperationID] = where
		if op.Summary == "" {
			t.Errorf("%s has no summary", where)
		}
		if len(op.Tags) == 0 {
			t.Errorf("%s has no tags, so it would not be grouped in the documentation", where)
		}
	})
}

// TestSchemaJSONConverts checks the JSON form the browsable docs and most
// generators consume.
func TestSchemaJSONConverts(t *testing.T) {
	b, err := apispec.JSON()
	if err != nil {
		t.Fatalf("the schema does not convert to JSON: %v", err)
	}
	if len(b) < 1000 {
		t.Errorf("the JSON schema is only %d bytes, which cannot be right", len(b))
	}
	if !strings.Contains(string(b), `"openapi"`) {
		t.Error("the JSON schema has no openapi field")
	}
}

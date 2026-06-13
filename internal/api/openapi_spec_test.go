package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// fetchSpec returns the parsed live OpenAPI document.
func fetchSpec(t *testing.T) map[string]any {
	t.Helper()
	ta := bootAPI(t)
	rec := ta.raw("GET", "/api/openapi.json", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/openapi.json: %d", rec.Code)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	return doc
}

// operation returns the operation object for method+path, or fails.
func operation(t *testing.T, doc map[string]any, method, path string) map[string]any {
	t.Helper()
	paths, _ := doc["paths"].(map[string]any)
	p, ok := paths[path].(map[string]any)
	if !ok {
		t.Fatalf("path %q not in spec", path)
	}
	op, ok := p[method].(map[string]any)
	if !ok {
		t.Fatalf("%s %q not in spec", method, path)
	}
	return op
}

func queryNames(op map[string]any) map[string]bool {
	out := map[string]bool{}
	params, _ := op["parameters"].([]any)
	for _, raw := range params {
		p, _ := raw.(map[string]any)
		if p["in"] == "query" {
			out[p["name"].(string)] = true
		}
	}
	return out
}

// TestOpenAPIQueryParams verifies the spec now documents query parameters:
// cursor/limit on every list endpoint plus the per-endpoint filters that
// clients previously had to discover by reading source.
func TestOpenAPIQueryParams(t *testing.T) {
	doc := fetchSpec(t)

	// Pagination is auto-documented on list endpoints (listResponse shape).
	for _, path := range []string{"/api/v1/objects", "/api/v1/alerts", "/api/v1/events", "/api/v1/incidents"} {
		q := queryNames(operation(t, doc, "get", path))
		if !q["cursor"] || !q["limit"] {
			t.Errorf("%s missing cursor/limit query params: %v", path, q)
		}
	}

	// Explicit filters.
	if q := queryNames(operation(t, doc, "get", "/api/v1/objects")); !q["selector"] || !q["folder"] {
		t.Errorf("objects list missing selector/folder filters: %v", q)
	}
	if q := queryNames(operation(t, doc, "get", "/api/v1/alerts")); !q["status"] || !q["severity"] {
		t.Errorf("alerts list missing status/severity filters: %v", q)
	}
	if q := queryNames(operation(t, doc, "get", "/api/v1/events")); !q["from"] || !q["to"] {
		t.Errorf("events list missing from/to filters: %v", q)
	}
}

// TestOpenAPIStatusCodes verifies success codes are no longer guessed from a
// trailing "s": collection creates are 201, deletes 204, async actions 202.
func TestOpenAPIStatusCodes(t *testing.T) {
	doc := fetchSpec(t)
	cases := []struct {
		method, path, code string
	}{
		{"post", "/api/v1/hosts", "201"},                  // collection create
		{"post", "/api/v1/results", "202"},                // async submit
		{"post", "/api/v1/objects/{id}/check-now", "202"}, // async action (verb segment)
		{"delete", "/api/v1/heartbeats/{name}", "204"},    // no-content delete
	}
	for _, c := range cases {
		op := operation(t, doc, c.method, c.path)
		resp, _ := op["responses"].(map[string]any)
		if _, ok := resp[c.code]; !ok {
			t.Errorf("%s %s: missing %s response (have %v)", c.method, c.path, c.code, keysOf(resp))
		}
	}
}

func keysOf(m map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

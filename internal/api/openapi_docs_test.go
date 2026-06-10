package api

// The docs page is real Swagger UI vendored into the binary: the shell
// must mount against the live OpenAPI document and the assets must come
// out of the embedded FS — no CDN, no cache pinning (a stale shell must
// not outlive a binary upgrade).

import (
	"net/http"
	"strings"
	"testing"
)

func TestDocsServeSwaggerUI(t *testing.T) {
	ta := bootAPI(t)

	rec := ta.raw("GET", "/api/docs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/docs: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"SwaggerUIBundle", "/api/openapi.json",
		"/api/docs/swagger-ui-bundle.js", "/api/docs/swagger-ui.css", "withCredentials"} {
		if !strings.Contains(body, want) {
			t.Fatalf("docs shell missing %q:\n%s", want, body)
		}
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("docs shell Cache-Control = %q, want no-cache", cc)
	}

	rec = ta.raw("GET", "/api/docs/swagger-ui-bundle.js", nil)
	if rec.Code != http.StatusOK || rec.Body.Len() < 500_000 ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("bundle.js: %d, %d bytes, %s", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Type"))
	}
	rec = ta.raw("GET", "/api/docs/swagger-ui.css", nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("swagger-ui.css: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}

	// no path traversal / no surprise files out of the embed
	if rec := ta.raw("GET", "/api/docs/missing.js", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset: want 404, got %d", rec.Code)
	}

	// the OpenAPI document itself stays uncached at the client
	rec = ta.raw("GET", "/api/openapi.json", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("openapi.json: %d cc=%q", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

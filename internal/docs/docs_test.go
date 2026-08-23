package docs

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func gz(s string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(s))
	_ = zw.Close()
	return buf.Bytes()
}

func testSite() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                         {Data: []byte("<h1>home</h1>")},
		"404.html":                           {Data: []byte("<h1>lost</h1>")},
		"getting-started/install/index.html": {Data: []byte("<h1>install</h1>")},
		// pre-compressed page, as docs/compress.mjs stages it
		"alarming/overview/index.html.gz": {Data: gz("<h1>alarming</h1>")},
		"_astro/styles.deadbeef.css.gz":   {Data: gz("body{}")},
		"_astro/app.abc123.js":            {Data: []byte("console.log(1)")},
		"pagefind/pagefind.js":            {Data: []byte("export{}")},
		"openapi.json":                    {Data: []byte("{}")},
	}
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestHandlerRoutes(t *testing.T) {
	h := HandlerFS(testSite())

	cases := []struct {
		path       string
		code       int
		body       string
		cache      string
		location   string
		contentTyp string
	}{
		{path: "/docs/", code: 200, body: "home", cache: "no-cache", contentTyp: "text/html"},
		{path: "/docs/getting-started/install/", code: 200, body: "install", cache: "no-cache"},
		// Astro "directory" output: the bare path redirects to the slash form so
		// relative links inside the page resolve against the right base.
		{path: "/docs/getting-started/install", code: 308, location: "/docs/getting-started/install/"},
		{path: "/docs/alarming/overview", code: 308, location: "/docs/alarming/overview/"},
		// content-hashed assets are immutable
		{path: "/docs/_astro/app.abc123.js", code: 200, body: "console.log", cache: "public, max-age=31536000, immutable"},
		{path: "/docs/pagefind/pagefind.js", code: 200, cache: "no-cache"},
		{path: "/docs/openapi.json", code: 200, contentTyp: "application/json"},
		// unknown page → Starlight's own 404 document, with a real 404 status
		{path: "/docs/nope/", code: 404, body: "lost", contentTyp: "text/html"},
		{path: "/docs/nope.png", code: 404, body: "lost"},
		// traversal is neutralised by path.Clean, never escapes the tree
		{path: "/docs/../../etc/passwd", code: 404},
	}
	for _, tc := range cases {
		rec := get(t, h, tc.path)
		if rec.Code != tc.code {
			t.Errorf("%s: code %d, want %d", tc.path, rec.Code, tc.code)
		}
		if tc.body != "" && !strings.Contains(rec.Body.String(), tc.body) {
			t.Errorf("%s: body %q does not contain %q", tc.path, rec.Body.String(), tc.body)
		}
		if tc.cache != "" && rec.Header().Get("Cache-Control") != tc.cache {
			t.Errorf("%s: Cache-Control %q, want %q", tc.path, rec.Header().Get("Cache-Control"), tc.cache)
		}
		if tc.location != "" && rec.Header().Get("Location") != tc.location {
			t.Errorf("%s: Location %q, want %q", tc.path, rec.Header().Get("Location"), tc.location)
		}
		if tc.contentTyp != "" && !strings.HasPrefix(rec.Header().Get("Content-Type"), tc.contentTyp) {
			t.Errorf("%s: Content-Type %q, want prefix %q", tc.path, rec.Header().Get("Content-Type"), tc.contentTyp)
		}
		if rec.Code != 308 && rec.Header().Get("Content-Security-Policy") != CSP {
			t.Errorf("%s: docs CSP not applied (got %q)", tc.path, rec.Header().Get("Content-Security-Policy"))
		}
	}
}

// A request for the literal index.html is canonicalised to the directory form
// with an absolute Location — never the relative "./" a FileServer would emit,
// which the browser resolves against the original URL (an endless loop).
func TestHandlerIndexHTMLCanonical(t *testing.T) {
	h := HandlerFS(testSite())
	rec := get(t, h, "/docs/getting-started/install/index.html")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("index.html: code %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/docs/getting-started/install/" {
		t.Fatalf("index.html: Location %q", loc)
	}
}

// Pre-compressed pages: gzip-capable clients get the stored bytes with
// Content-Encoding, everyone else gets them inflated; both carry the right
// Content-Type, and a matching If-None-Match revalidates to 304.
func TestHandlerPrecompressed(t *testing.T) {
	h := HandlerFS(testSite())

	req := httptest.NewRequest(http.MethodGet, "/docs/alarming/overview/", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("gzip client: %d enc=%q vary=%q", rec.Code, rec.Header().Get("Content-Encoding"), rec.Header().Get("Vary"))
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("gzip client: Content-Type %q", rec.Header().Get("Content-Type"))
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	if !strings.Contains(string(body), "alarming") {
		t.Fatalf("gzip client: inflated body %q", body)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on a page")
	}

	// same page, client without gzip → inflated on the fly
	rec = get(t, h, "/docs/alarming/overview/")
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "" || !strings.Contains(rec.Body.String(), "<h1>alarming</h1>") {
		t.Fatalf("identity client: %d enc=%q body=%q", rec.Code, rec.Header().Get("Content-Encoding"), rec.Body.String())
	}

	// conditional request → 304
	req = httptest.NewRequest(http.MethodGet, "/docs/alarming/overview/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match: code %d, want 304", rec.Code)
	}

	// compressed hashed asset keeps the immutable cache policy + css type
	req = httptest.NewRequest(http.MethodGet, "/docs/_astro/styles.deadbeef.css", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d type=%q cache=%q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
	}
}

func TestHandlerRejectsWrites(t *testing.T) {
	h := HandlerFS(testSite())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/docs/", strings.NewReader("x")))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: code %d, want 405", rec.Code)
	}
}

// Without a staged build (plain `go build` sees only .gitkeep) the handler
// says so loudly instead of 404ing every page.
func TestHandlerWithoutBuild(t *testing.T) {
	h := HandlerFS(fstest.MapFS{".gitkeep": {Data: nil}})
	rec := get(t, h, "/docs/")
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "make docs") {
		t.Fatalf("no build: got %d %q", rec.Code, rec.Body.String())
	}
}

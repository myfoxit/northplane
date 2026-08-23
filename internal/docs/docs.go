// Package docs serves the product documentation — the Astro/Starlight site
// authored in /docs at the repository root — from the binary itself, mounted
// at /docs/. The static build is staged into ./dist by `make docs` (and by the
// Dockerfile / release pipeline) and embedded with go:embed, so every
// northplaned — binary, container, edge-proxied VM, on-prem box — ships the
// manual that matches its own version and works fully offline.
//
// The documentation is public (no login): it contains no instance data, and
// the same pages are what an operator reads *before* they can log in (first
// run, recovery). Only the handler's Content-Security-Policy differs from the
// SPA's — see CSP.
//
// Text assets are pre-compressed by the docs build (docs/compress.mjs): the
// staged tree holds `page.html.gz` instead of `page.html`, which keeps the
// embedded size small (the generated REST reference alone is ~36 MB raw).
// The handler serves the .gz bytes directly to gzip-capable clients and
// inflates them for the rare client that is not.
package docs

import (
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

// Prefix is the URL path the documentation is mounted under (with trailing
// slash, as a net/http subtree pattern).
const Prefix = "/docs/"

// CSP is the Content-Security-Policy served for documentation pages. Starlight
// relies on inline bootstrap scripts (theme before first paint, sidebar state)
// and its search (Pagefind) instantiates WebAssembly at runtime; the app's
// strict SPA policy would block both. The docs are static content with no user
// input, so 'unsafe-inline' for scripts is an acceptable trade here —
// everything else stays locked to the own origin.
const CSP = "default-src 'self'; " +
	"script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

//go:embed all:dist
var distFS embed.FS

// Embedded reports whether a real documentation build is present (as opposed
// to the bare .gitkeep placeholder a plain `go build` sees).
func Embedded() bool {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return false
	}
	return hasIndex(sub)
}

// Handler serves the embedded documentation under Prefix.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return notEmbedded()
	}
	return HandlerFS(sub)
}

// HandlerFS serves a Starlight build rooted at root under Prefix. Exported so
// tests (and tools) can drive the routing rules against an in-memory tree.
//
// Routing rules, matching Astro's default `build.format: "directory"` output:
//   - /docs/            → index.html
//   - /docs/a/b/        → a/b/index.html
//   - /docs/a/b         → 308 → /docs/a/b/  (when a/b/index.html exists)
//   - /docs/_astro/x.js → immutable, one-year cache (content-hashed names)
//   - anything else     → 404.html (Starlight's own not-found page) with 404
//
// Every file may exist either verbatim or pre-compressed as <name>.gz.
func HandlerFS(root fs.FS) http.Handler {
	if !hasIndex(root) {
		return notEmbedded()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rel := strings.TrimPrefix(r.URL.Path, Prefix)
		clean := path.Clean("/" + rel) // no traversal, no double slashes
		name := strings.TrimPrefix(clean, "/")
		if strings.HasSuffix(rel, "/") || name == "" {
			name = path.Join(name, "index.html")
		}
		if strings.HasSuffix(name, "/index.html") && !strings.HasSuffix(rel, "/") {
			// literal ".../index.html" → canonical directory form
			http.Redirect(w, r, Prefix+strings.TrimSuffix(name, "index.html"), http.StatusMovedPermanently)
			return
		}

		h := w.Header()
		h.Set("Content-Security-Policy", CSP)

		data, gz, ok := load(root, name)
		if !ok {
			if exists(root, name) || exists(root, path.Join(name, "index.html")) {
				// /docs/a/b → /docs/a/b/ so relative links inside the page resolve
				http.Redirect(w, r, Prefix+name+"/", http.StatusPermanentRedirect)
				return
			}
			h.Set("Cache-Control", "no-cache")
			if body, bgz, found := load(root, "404.html"); found {
				serve(w, r, "404.html", body, bgz, http.StatusNotFound)
				return
			}
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(name, "_astro/") {
			// content-hashed build assets
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		serve(w, r, name, data, gz, http.StatusOK)
	})
}

// load returns the bytes of name — verbatim, or the pre-compressed name.gz
// (gz=true) — and whether anything was found.
func load(root fs.FS, name string) (data []byte, gz bool, ok bool) {
	if b, err := fs.ReadFile(root, name); err == nil {
		return b, false, true
	}
	if b, err := fs.ReadFile(root, name+".gz"); err == nil {
		return b, true, true
	}
	return nil, false, false
}

func exists(root fs.FS, name string) bool {
	if _, err := fs.Stat(root, name); err == nil {
		return true
	}
	_, err := fs.Stat(root, name+".gz")
	return err == nil
}

func hasIndex(root fs.FS) bool { return exists(root, "index.html") }

func acceptsGzip(r *http.Request) bool {
	for _, enc := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		if strings.HasPrefix(strings.TrimSpace(enc), "gzip") {
			return true
		}
	}
	return false
}

func inflate(gz []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(zr)
	if cerr := zr.Close(); err == nil {
		err = cerr // a truncated stream surfaces at Close
	}
	return data, err
}

// serve writes data with the content type of name's extension, a strong
// content ETag (so no-cache pages revalidate to 304), range support and a
// custom status (404 pages carry a real 404). gz marks pre-compressed bytes:
// they go out as-is with Content-Encoding for gzip-capable clients and are
// inflated for everyone else.
func serve(w http.ResponseWriter, r *http.Request, name string, data []byte, gz bool, status int) {
	h := w.Header()
	if gz {
		if acceptsGzip(r) {
			h.Set("Content-Encoding", "gzip")
			h.Set("Vary", "Accept-Encoding")
		} else {
			var err error
			if data, err = inflate(data); err != nil {
				http.Error(w, "corrupt embedded asset", http.StatusInternalServerError)
				return
			}
		}
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		h.Set("Content-Type", ct)
	} else {
		h.Set("Content-Type", "application/octet-stream")
	}
	sum := fnv.New64a()
	_, _ = sum.Write(data)
	etag := fmt.Sprintf(`"%x"`, sum.Sum64())
	h.Set("ETag", etag)
	if status == http.StatusOK {
		// ServeContent handles If-None-Match / Range / HEAD against the ETag.
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
		return
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func notEmbedded() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "documentation not embedded in this build — run `make docs` before building", http.StatusNotImplemented)
	})
}

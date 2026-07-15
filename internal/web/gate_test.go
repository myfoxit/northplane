package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
)

// GateSPA must redirect a logged-out browser *navigation* (an HTML document
// request) to /login instead of serving the app shell — otherwise the SPA
// boots, renders the full UI and only then 401-redirects, which is the
// "flash before login" this gate exists to remove.
func TestGateSPARedirectsAnonymousDocument(t *testing.T) {
	p, _ := testPages(t)
	called := false
	gate := GateSPA(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), p.auth)

	req := httptest.NewRequest(http.MethodGet, "/objects/123", nil) // deep client-side route
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("anon document: got %d → %q, want 302 → /login", rec.Code, rec.Header().Get("Location"))
	}
	if called {
		t.Fatal("SPA shell was served to an anonymous document request")
	}
}

// A valid session cookie passes straight through to the SPA handler.
func TestGateSPAServesAuthenticatedDocument(t *testing.T) {
	p, store := testPages(t)
	ctx := context.Background()
	u, err := store.CreateLocalUser(ctx, "Admin", "a@example.net", auth.HashSecret("irrelevant"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := p.auth.NewSession(ctx, u.ID, model.DefaultTenant, []string{"admin"}, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	called := false
	gate := GateSPA(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}), p.auth)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")
	req.AddCookie(&http.Cookie{Name: "np_session", Value: sess})
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)

	if !called || rec.Code != http.StatusOK {
		t.Fatalf("authenticated document not served: called=%v code=%d", called, rec.Code)
	}
}

// Assets and non-navigation fetches are never gated (they carry no HTML
// document accept, or live under /assets/), so the login page's own assets,
// the favicon and XHR/fetch calls still resolve while logged out.
func TestGateSPAPassesAssetsAndNonDocuments(t *testing.T) {
	p, _ := testPages(t)
	cases := []struct{ name, path, accept string }{
		{"hashed asset", "/assets/app-abc123.js", "text/html"}, // /assets/ bypasses even with an html accept
		{"xhr/fetch", "/", "application/json"},                 // no text/html → not a navigation
		{"favicon", "/favicon.ico", "image/avif,image/webp,*/*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			gate := GateSPA(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), p.auth)
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Accept", tc.accept)
			rec := httptest.NewRecorder()
			gate.ServeHTTP(rec, req)
			if !called {
				t.Fatalf("%s: gate blocked a non-document request (code %d → %q)",
					tc.name, rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// Tests for the first-run /setup flow: gate condition, account creation,
// race safety, and the /login redirect chain.

func testPages(t *testing.T) (*Pages, *storage.Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); store.Close() })
	// fresh rate-limit buckets so tests do not throttle each other
	logins = &loginLimiter{buckets: map[string]*loginBucket{}}
	authn := &auth.Authenticator{Store: store}
	return NewPages(store, authn, nil, config.Defaults(), "test"), store
}

func setupForm(name, email, password, confirm string) *http.Request {
	form := url.Values{
		"name": {name}, "email": {email},
		"password": {password}, "confirm": {confirm},
	}
	r := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestSetupGateOpenRendersForm(t *testing.T) {
	p, _ := testPages(t)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /setup: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `action="/setup"`) || !strings.Contains(body, `name="confirm"`) {
		t.Fatalf("setup form missing fields: %s", body[:min(len(body), 200)])
	}
}

func TestLoginRedirectsToSetupWhenFresh(t *testing.T) {
	p, _ := testPages(t)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/setup" {
		t.Fatalf("GET /login fresh: %d → %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSetupCreatesAdminAndSession(t *testing.T) {
	p, store := testPages(t)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, setupForm("Admin", "admin@example.net", "korrekt-pferd-batterie", "korrekt-pferd-batterie"))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("POST /setup: %d → %q (%s)", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "np_session" {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value == "" {
		t.Fatal("no np_session cookie set")
	}

	users, err := store.ListUsers(context.Background())
	if err != nil || len(users) != 1 || !users[0].Local {
		t.Fatalf("want exactly one local user, got %d (err %v)", len(users), err)
	}
	if users[0].PassHash == "" || users[0].Email != "admin@example.net" {
		t.Fatalf("user not persisted correctly: %+v", users[0])
	}

	// the session must resolve to a principal with admin (*:*) rights
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	principal, err := p.auth.Authenticate(req)
	if err != nil || principal == nil {
		t.Fatalf("session did not authenticate: %v", err)
	}
	if !principal.Allow("admin:ai") {
		t.Fatal("setup admin lacks *:* permissions")
	}

	// audit trail records the bootstrap
	entries, err := store.QueryAudit(context.Background(), storage.AuditFilter{Action: "setup.admin"})
	if err != nil || len(entries) == 0 {
		t.Fatalf("setup.admin audit entry missing (err %v)", err)
	}
}

func TestSetupGateClosedAfterUser(t *testing.T) {
	p, _ := testPages(t)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, setupForm("Admin", "a@example.net", "korrekt-pferd-batterie", "korrekt-pferd-batterie"))
	if rec.Code != http.StatusFound {
		t.Fatalf("first POST: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("GET /setup after setup: %d → %q", rec.Code, rec.Header().Get("Location"))
	}

	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, setupForm("Eve", "eve@example.net", "boese-eve-passwort-123", "boese-eve-passwort-123"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST /setup after setup: want 409, got %d", rec.Code)
	}

	// /login renders normally again
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /login after setup: %d", rec.Code)
	}
}

func TestSetupGateClosedWhenTokenExists(t *testing.T) {
	p, store := testPages(t)
	_, tok := auth.MintToken(model.DefaultTenant, "bootstrap-admin",
		[]model.Permission{"*:*"}, nil)
	if err := store.CreateAPIToken(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("GET /setup with token: %d → %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSetupValidation(t *testing.T) {
	p, store := testPages(t)
	cases := []struct {
		name string
		req  *http.Request
	}{
		{"short password", setupForm("A", "a@example.net", "kurz", "kurz")},
		{"mismatch", setupForm("A", "a@example.net", "korrekt-pferd-batterie", "korrekt-pferd-batterix")},
		{"missing name", setupForm("", "a@example.net", "korrekt-pferd-batterie", "korrekt-pferd-batterie")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, tc.req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d", rec.Code)
			}
		})
	}
	users, _ := store.ListUsers(context.Background())
	if len(users) != 0 {
		t.Fatalf("invalid submissions must not create users, got %d", len(users))
	}
}

func TestSetupRaceDoublePOST(t *testing.T) {
	p, store := testPages(t)
	const n = 8
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, setupForm("Admin", "a@example.net", "korrekt-pferd-batterie", "korrekt-pferd-batterie"))
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()
	var created, conflict int
	for _, c := range codes {
		switch c {
		case http.StatusFound:
			created++
		case http.StatusConflict:
			conflict++
		}
	}
	if created != 1 {
		t.Fatalf("want exactly 1 created, got %d (codes %v)", created, codes)
	}
	users, _ := store.ListUsers(context.Background())
	if len(users) != 1 {
		t.Fatalf("race created %d users", len(users))
	}
}

func TestSetupRateLimited(t *testing.T) {
	p, _ := testPages(t)
	var last *httptest.ResponseRecorder
	// drain the per-IP bucket with invalid attempts (burst = 8)
	for i := 0; i < int(loginBurst)+1; i++ {
		last = httptest.NewRecorder()
		p.ServeHTTP(last, setupForm("A", "a@example.net", "kurz", "kurz"))
	}
	if last.Header().Get("Retry-After") == "" {
		t.Fatalf("expected throttling after %v attempts, got %d", loginBurst+1, last.Code)
	}
}


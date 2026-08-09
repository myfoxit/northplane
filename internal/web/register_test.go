package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/storage"
)

// Tests for the self-service /register flow: the allowSignup gate, the
// first-run deferral to /setup, viewer-only role assignment (never the
// legacy empty-roles→admin escalation), and duplicate handling.

// testSignupPages is testPages with signup enabled and the first-run gate
// already closed (an admin exists), i.e. the state a real install is in
// when /register becomes reachable.
func testSignupPages(t *testing.T) (*Pages, *storage.Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); store.Close() })
	logins = &loginLimiter{buckets: map[string]*loginBucket{}}
	authn := &auth.Authenticator{Store: store}
	cfg := config.Defaults()
	cfg.AllowSignup = true
	p := NewPages(store, authn, nil, nil, cfg, "test")

	// close the first-run gate the way a real install does
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, setupForm("Admin", "admin@example.net", "korrekt-pferd-batterie", "korrekt-pferd-batterie"))
	if rec.Code != http.StatusFound {
		t.Fatalf("setup bootstrap failed: %d", rec.Code)
	}
	return p, store
}

func registerForm(name, email, password, confirm string) *http.Request {
	form := url.Values{
		"name": {name}, "email": {email},
		"password": {password}, "confirm": {confirm},
	}
	r := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestRegisterHiddenWhenDisabled(t *testing.T) {
	p, _ := testPages(t) // Defaults(): AllowSignup=false
	for _, r := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/register", nil),
		registerForm("Eve", "eve@example.net", "korrekt-pferd-batterie", "korrekt-pferd-batterie"),
	} {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s /register with signup disabled: %d, want 404", r.Method, rec.Code)
		}
	}
}

func TestRegisterDefersToSetupWhileFirstRunOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); store.Close() })
	logins = &loginLimiter{buckets: map[string]*loginBucket{}}
	cfg := config.Defaults()
	cfg.AllowSignup = true
	p := NewPages(store, &auth.Authenticator{Store: store}, nil, nil, cfg, "test")

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, registerForm("Eve", "eve@example.net", "korrekt-pferd-batterie", "korrekt-pferd-batterie"))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/setup" {
		t.Fatalf("POST /register on fresh install: %d → %q, want 302 → /setup",
			rec.Code, rec.Header().Get("Location"))
	}
	users, _ := store.ListUsers(context.Background())
	if len(users) != 0 {
		t.Fatalf("fresh install must not create users via /register, got %d", len(users))
	}
}

func TestRegisterCreatesViewerAndSession(t *testing.T) {
	p, store := testSignupPages(t)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, registerForm("Visitor", "visitor@example.net", "korrekt-pferd-batterie", "korrekt-pferd-batterie"))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("POST /register: %d → %q (%s)", rec.Code, rec.Header().Get("Location"), rec.Body.String())
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

	var registered = false
	users, err := store.ListUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.Email != "visitor@example.net" {
			continue
		}
		registered = true
		if !u.Local || u.PassHash == "" {
			t.Fatalf("registered user not local with hash: %+v", u)
		}
		// THE regression guard: an empty Roles slice would be escalated to
		// admin by localLogin's legacy fallback. Registration must always
		// persist the explicit viewer role.
		if len(u.Roles) != 1 || u.Roles[0] != "viewer" {
			t.Fatalf("registered user roles = %v, want [viewer]", u.Roles)
		}
	}
	if !registered {
		t.Fatal("registered user not persisted")
	}

	// the session authenticates, with viewer rights only
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	principal, err := p.auth.Authenticate(req)
	if err != nil || principal == nil {
		t.Fatalf("session did not authenticate: %v", err)
	}
	if principal.Allow("admin:users") {
		t.Fatal("self-registered account must not hold admin permissions")
	}

	entries, err := store.QueryAudit(context.Background(), storage.AuditFilter{Action: "user.register"})
	if err != nil || len(entries) == 0 {
		t.Fatalf("user.register audit entry missing (err %v)", err)
	}
}

func TestRegisterValidation(t *testing.T) {
	p, _ := testSignupPages(t)
	cases := []struct {
		name string
		req  *http.Request
		want string
	}{
		{"mismatch", registerForm("V", "v@example.net", "korrekt-pferd-batterie", "anders-anders-anders"), "stimmen nicht überein"},
		{"short", registerForm("V", "v@example.net", "kurz", "kurz"), "mindestens 12 Zeichen"},
		{"missing", registerForm("", "", "korrekt-pferd-batterie", "korrekt-pferd-batterie"), "erforderlich"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, tc.req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s: %d, body lacks %q", tc.name, rec.Code, tc.want)
		}
	}
}

func TestRegisterDuplicateEmail(t *testing.T) {
	p, _ := testSignupPages(t)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, registerForm("V1", "dup@example.net", "korrekt-pferd-batterie", "korrekt-pferd-batterie"))
	if rec.Code != http.StatusFound {
		t.Fatalf("first register: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, registerForm("V2", "dup@example.net", "korrekt-pferd-batterie", "korrekt-pferd-batterie"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bereits registriert") {
		t.Fatalf("duplicate register: %d (%s)", rec.Code, rec.Body.String()[:min(len(rec.Body.String()), 200)])
	}
}

func TestLoginShowsSignupLinkOnlyWhenEnabled(t *testing.T) {
	// enabled + gate closed → link present
	p, _ := testSignupPages(t)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `href="/register"`) {
		t.Fatalf("login with signup enabled: %d, register link missing", rec.Code)
	}

	// disabled → no link (close the gate first so /login renders)
	p2, _ := testPages(t)
	rec = httptest.NewRecorder()
	p2.ServeHTTP(rec, setupForm("Admin", "a2@example.net", "korrekt-pferd-batterie", "korrekt-pferd-batterie"))
	if rec.Code != http.StatusFound {
		t.Fatalf("setup: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	p2.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `href="/register"`) {
		t.Fatalf("login with signup disabled: %d, register link unexpectedly present", rec.Code)
	}
}

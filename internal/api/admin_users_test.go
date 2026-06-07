package api

// HTTP-level coverage for the user-management endpoints (SPEC §11.2 /
// §13.2): create/get/update/set-password/self-change/delete plus the
// last-admin guard and the no-password-hash-leak invariant. A trimmed API
// (only registerUsers + the standard middleware) is driven over httptest so
// the test needs no event bus, TSDB or scheduler.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/metrics"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

type userAPI struct {
	h     http.Handler
	store *storage.Store
	authn *auth.Authenticator
	token string // np_… with admin:users
	t     *testing.T
}

func bootUserAPI(t *testing.T) *userAPI {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); store.Close() })

	authn := &auth.Authenticator{Store: store}
	a := &API{
		Store:   store,
		Auth:    authn,
		Metrics: metrics.NewRegistry(),
		Log:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
	a.mux = http.NewServeMux()
	a.registerUsers()
	h := a.withMiddleware(a.mux)

	clear, tok := auth.MintToken(model.DefaultTenant, "test-admin",
		[]model.Permission{"admin:users"}, nil)
	if err := store.CreateAPIToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	return &userAPI{h: h, store: store, authn: authn, token: clear, t: t}
}

// req issues an admin (token) request and returns status + raw body.
func (u *userAPI) req(method, path string, body any) (int, []byte) {
	u.t.Helper()
	return u.do(method, path, body, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+u.token)
	})
}

func (u *userAPI) do(method, path string, body any, mod func(*http.Request)) (int, []byte) {
	u.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, path, rd)
	if rd != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if mod != nil {
		mod(r)
	}
	rec := httptest.NewRecorder()
	u.h.ServeHTTP(rec, r)
	data, _ := io.ReadAll(rec.Body)
	return rec.Code, data
}

func (u *userAPI) id(body []byte) string {
	u.t.Helper()
	var v struct{ ID string }
	if err := json.Unmarshal(body, &v); err != nil || v.ID == "" {
		u.t.Fatalf("no id in response: %s", body)
	}
	return v.ID
}

func TestUsersAPICRUD(t *testing.T) {
	u := bootUserAPI(t)

	// create with a password and a role
	code, body := u.req("POST", "/api/v1/users", map[string]any{
		"name": "Ada", "email": "ada@example.net",
		"password": "korrekt-pferd-batterie", "roles": []string{"operator"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	if bytes.Contains(body, []byte("passHash")) || bytes.Contains(body, []byte("pass_hash")) ||
		bytes.Contains(body, []byte("korrekt-pferd")) {
		t.Fatalf("password material leaked in response: %s", body)
	}
	id := u.id(body)

	// get single — still no hash
	code, body = u.req("GET", "/api/v1/users/"+id, nil)
	if code != 200 || !bytes.Contains(body, []byte("ada@example.net")) {
		t.Fatalf("get: %d %s", code, body)
	}
	if bytes.Contains(body, []byte("pass")) {
		t.Fatalf("get leaked password field: %s", body)
	}

	// duplicate email → 409 np:users/email-in-use
	code, body = u.req("POST", "/api/v1/users", map[string]any{
		"name": "Eve", "email": "ada@example.net", "password": "korrekt-pferd-batterie"})
	if code != http.StatusConflict || !bytes.Contains(body, []byte("np:users/email-in-use")) {
		t.Fatalf("dup email: %d %s", code, body)
	}

	// short password → 422
	code, _ = u.req("POST", "/api/v1/users", map[string]any{
		"name": "Short", "email": "s@example.net", "password": "kurz"})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("short pw want 422, got %d", code)
	}

	// password is optional (OIDC-only account)
	code, body = u.req("POST", "/api/v1/users", map[string]any{
		"name": "NoPass", "email": "np@example.net", "roles": []string{"viewer"}})
	if code != http.StatusCreated {
		t.Fatalf("create no-pass: %d %s", code, body)
	}

	// update name + roles
	code, body = u.req("PUT", "/api/v1/users/"+id, map[string]any{
		"name": "Ada L.", "roles": []string{"viewer"}})
	if code != 200 || !bytes.Contains(body, []byte("Ada L.")) {
		t.Fatalf("update: %d %s", code, body)
	}

	// admin set-password (reset)
	code, _ = u.req("POST", "/api/v1/users/"+id+":set-password", map[string]any{
		"password": "ein-ganz-neues-passwort"})
	if code != http.StatusNoContent {
		t.Fatalf("set-password: %d", code)
	}

	// delete (non-admin user → no guard)
	code, _ = u.req("DELETE", "/api/v1/users/"+id, nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	code, _ = u.req("GET", "/api/v1/users/"+id, nil)
	if code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", code)
	}

	// audit trail recorded the mutations
	entries, _ := u.store.QueryAudit(context.Background(), storage.AuditFilter{Action: "user.create"})
	if len(entries) == 0 {
		t.Fatal("user.create audit entry missing")
	}
}

func TestUsersAPILastAdminGuard(t *testing.T) {
	u := bootUserAPI(t)
	ctx := context.Background()

	// two local admins
	a1, err := u.store.CreateLocalUser(ctx, "Admin1", "a1@example.net", auth.HashSecret("korrekt-pferd-batterie"))
	if err != nil {
		t.Fatal(err)
	}
	a2, err := u.store.CreateLocalUser(ctx, "Admin2", "a2@example.net", auth.HashSecret("korrekt-pferd-batterie"))
	if err != nil {
		t.Fatal(err)
	}

	// deleting one of two admins is fine
	if code, body := u.req("DELETE", "/api/v1/users/"+a1.ID, nil); code != http.StatusNoContent {
		t.Fatalf("delete one admin: %d %s", code, body)
	}

	// a2 is now the last enabled local admin — delete refused
	if code, body := u.req("DELETE", "/api/v1/users/"+a2.ID, nil); code != http.StatusConflict ||
		!bytes.Contains(body, []byte("np:users/last-admin")) {
		t.Fatalf("delete last admin: %d %s", code, body)
	}
	// disabling the last admin refused
	if code, body := u.req("PUT", "/api/v1/users/"+a2.ID, map[string]any{"disabled": true}); code != http.StatusConflict ||
		!bytes.Contains(body, []byte("np:users/last-admin")) {
		t.Fatalf("disable last admin: %d %s", code, body)
	}
	// stripping the admin role from the last admin refused
	if code, body := u.req("PUT", "/api/v1/users/"+a2.ID, map[string]any{"roles": []string{"viewer"}}); code != http.StatusConflict ||
		!bytes.Contains(body, []byte("np:users/last-admin")) {
		t.Fatalf("de-role last admin: %d %s", code, body)
	}
	// a2 must still be intact & enabled & admin
	if got, _ := u.store.GetUser(ctx, a2.ID); got == nil || got.Disabled || len(got.Roles) == 0 || got.Roles[0] != "admin" {
		t.Fatalf("last admin was mutated despite guard: %+v", got)
	}
}

func TestUsersAPIChangeOwnPassword(t *testing.T) {
	u := bootUserAPI(t)
	ctx := context.Background()

	const oldPw = "altes-passwort-1234"
	self, err := u.store.CreateUser(ctx, &model.User{
		Name: "Self", Email: "self@example.net", Local: true,
		PassHash: auth.HashSecret(oldPw), Roles: []string{"viewer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// a session cookie makes the request a user principal
	sess, err := u.authn.NewSession(ctx, self.ID, model.DefaultTenant, []string{"viewer"}, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	withSession := func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "np_session", Value: sess})
	}

	// wrong old password → 403
	if code, body := u.do("POST", "/api/v1/users/me:change-password", map[string]any{
		"oldPassword": "falsch", "newPassword": "ganz-neues-passwort-99"}, withSession); code != http.StatusForbidden ||
		!bytes.Contains(body, []byte("np:auth/bad-password")) {
		t.Fatalf("wrong old pw: %d %s", code, body)
	}
	// correct old password → 204 and the new password verifies
	if code, _ := u.do("POST", "/api/v1/users/me:change-password", map[string]any{
		"oldPassword": oldPw, "newPassword": "ganz-neues-passwort-99"}, withSession); code != http.StatusNoContent {
		t.Fatalf("change pw: %d", code)
	}
	got, _ := u.store.GetUser(ctx, self.ID)
	if !auth.VerifySecret("ganz-neues-passwort-99", got.PassHash) {
		t.Fatal("new password not stored")
	}

	// an unauthenticated (anonymous) caller is rejected
	if code, _ := u.do("POST", "/api/v1/users/me:change-password", map[string]any{
		"oldPassword": "x", "newPassword": "ganz-neues-passwort-99"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("anon change pw want 401, got %d", code)
	}
}

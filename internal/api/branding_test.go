package api

// Coverage for /api/v1/branding — the INSTANCE-wide console look.
//
// The behaviour these tests pin down is what makes branding different from
// every other config document: it is stored once per installation, so the
// X-Northplane-Tenant switch header must not move it. An operator managing
// many customers from one console changes customer constantly; if branding
// were tenant-scoped, the console would re-skin under them mid-shift.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
)

// brandingToken mints a token in the default tenant with the given grants.
func brandingToken(u *userAPI, name string, perms ...model.Permission) func(*http.Request) {
	u.t.Helper()
	clear, tok := auth.MintToken(model.DefaultTenant, name, perms, nil)
	if err := u.store.CreateAPIToken(u.t.Context(), tok); err != nil {
		u.t.Fatalf("create token %s: %v", name, err)
	}
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+clear) }
}

func brandingOf(u *userAPI, body []byte) model.Branding {
	u.t.Helper()
	var b model.Branding
	if err := json.Unmarshal(body, &b); err != nil {
		u.t.Fatalf("decode branding %s: %v", body, err)
	}
	return b
}

func TestBrandingRoundTrip(t *testing.T) {
	u := bootUserAPI(t)
	admin := brandingToken(u, "branding-admin", "config:write")

	// unset → empty document, so the client keeps its shipped default
	code, body := u.do("GET", "/api/v1/branding", nil, admin)
	if code != http.StatusOK {
		t.Fatalf("get: %d %s", code, body)
	}
	if b := brandingOf(u, body); b.Theme != "" || b.Mode != "" {
		t.Fatalf("want empty branding, got %s", body)
	}

	// set and read back
	if code, body = u.do("PUT", "/api/v1/branding",
		map[string]any{"theme": "obsidianFire", "mode": "dark"}, admin); code != http.StatusOK {
		t.Fatalf("put: %d %s", code, body)
	}
	code, body = u.do("GET", "/api/v1/branding", nil, admin)
	if code != http.StatusOK {
		t.Fatalf("get2: %d %s", code, body)
	}
	if b := brandingOf(u, body); b.Theme != "obsidianFire" || b.Mode != "dark" {
		t.Fatalf("round-trip mismatch: %s", body)
	}

	// mode is a closed set; the theme id deliberately is not (it is a
	// frontend registry, so a newer UI may ship ids this build never heard of)
	if code, body = u.do("PUT", "/api/v1/branding",
		map[string]any{"mode": "sideways"}, admin); code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 for a bogus mode, got %d %s", code, body)
	}
	if code, body = u.do("PUT", "/api/v1/branding",
		map[string]any{"theme": "some-future-theme"}, admin); code != http.StatusOK {
		t.Fatalf("an unknown theme id must be accepted, got %d %s", code, body)
	}
}

// TestBrandingIgnoresTenantSwitch is the point of the whole design: the same
// document is served no matter which customer the operator has switched to,
// and a write made while switched still lands on the instance document.
func TestBrandingIgnoresTenantSwitch(t *testing.T) {
	u := bootUserAPI(t)
	// admin:tenants is what makes tenantOf honour the header at all — without
	// it the switch is ignored anyway and the test would prove nothing.
	operator := brandingToken(u, "cmp-operator", "config:write", "admin:tenants")

	custB := model.NewID()
	if err := u.store.CreateTenant(u.t.Context(), custB, "Customer B", "cust-b"); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	switched := func(r *http.Request) {
		operator(r)
		r.Header.Set("X-Northplane-Tenant", custB)
	}

	// brand the instance from the home view
	if code, body := u.do("PUT", "/api/v1/branding",
		map[string]any{"theme": "arcticBlue", "mode": "light"}, operator); code != http.StatusOK {
		t.Fatalf("put home: %d %s", code, body)
	}

	// switching customer must NOT reveal a different (empty) branding
	code, body := u.do("GET", "/api/v1/branding", nil, switched)
	if code != http.StatusOK {
		t.Fatalf("get switched: %d %s", code, body)
	}
	if b := brandingOf(u, body); b.Theme != "arcticBlue" || b.Mode != "light" {
		t.Fatalf("tenant switch changed the branding: %s", body)
	}

	// …and a write made while switched updates the instance, not the customer
	if code, body = u.do("PUT", "/api/v1/branding",
		map[string]any{"theme": "neonMint", "mode": "dark"}, switched); code != http.StatusOK {
		t.Fatalf("put switched: %d %s", code, body)
	}
	code, body = u.do("GET", "/api/v1/branding", nil, operator) // back home
	if code != http.StatusOK {
		t.Fatalf("get home: %d %s", code, body)
	}
	if b := brandingOf(u, body); b.Theme != "neonMint" {
		t.Fatalf("write while switched did not reach the instance document: %s", body)
	}
}

func TestBrandingWriteNeedsConfigWrite(t *testing.T) {
	u := bootUserAPI(t)
	admin := brandingToken(u, "branding-admin2", "config:write")
	plain := brandingToken(u, "branding-plain", "objects:read")

	if code, body := u.do("PUT", "/api/v1/branding",
		map[string]any{"theme": "plumGold"}, admin); code != http.StatusOK {
		t.Fatalf("admin put: %d %s", code, body)
	}

	// any authenticated actor may READ it — every session needs it to paint
	code, body := u.do("GET", "/api/v1/branding", nil, plain)
	if code != http.StatusOK {
		t.Fatalf("plain get: %d %s", code, body)
	}
	if b := brandingOf(u, body); b.Theme != "plumGold" {
		t.Fatalf("plain reader got the wrong branding: %s", body)
	}

	// …but must not re-skin the console for everyone else
	if code, body = u.do("PUT", "/api/v1/branding",
		map[string]any{"theme": "carbonYellow"}, plain); code != http.StatusForbidden {
		t.Fatalf("plain put: want 403, got %d %s", code, body)
	}

	// unauthenticated → 401 on read, 401 on write
	if code, _ = u.do("GET", "/api/v1/branding", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("anon get: want 401, got %d", code)
	}
	if code, _ = u.do("PUT", "/api/v1/branding",
		map[string]any{"theme": "plumGold"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("anon put: want 401, got %d", code)
	}
}

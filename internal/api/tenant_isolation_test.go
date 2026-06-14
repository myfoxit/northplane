package api

// Tenant-isolation tests for the multi-customer story: a freshly created tenant
// must be immediately usable (built-in roles seeded by CreateTenant), and one
// tenant must never be able to read another tenant's objects through the public
// API. These are the two properties the "hundreds of self-service tenants"
// claim rests on.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// TestCreateTenantSeedsRoles proves CreateTenant seeds the built-in roles, so a
// principal bound to a new tenant resolves real permissions instead of nothing.
func TestCreateTenantSeedsRoles(t *testing.T) {
	ta := bootAPI(t)

	id := model.NewID()
	if err := ta.store.CreateTenant(ta.ctx, id, "Customer C", "cust-c"); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	for _, name := range []string{"admin", "operator", "viewer", "ai-agent"} {
		role, err := storage.LoadOne[model.Role](ta.ctx, ta.store, id, storage.KindRole, name)
		if err != nil {
			t.Fatalf("built-in role %q missing in new tenant: %v", name, err)
		}
		if len(role.Permissions) == 0 {
			t.Fatalf("role %q seeded with no permissions", name)
		}
	}

	// operator must be able to manage the tenant (objects:write), or the tenant
	// is read-only on arrival.
	op, err := storage.LoadOne[model.Role](ta.ctx, ta.store, id, storage.KindRole, "operator")
	if err != nil {
		t.Fatalf("load operator role: %v", err)
	}
	if !containsPerm(op.Permissions, "objects:write") {
		t.Fatalf("operator role missing objects:write, got %v", op.Permissions)
	}
}

// TestCrossTenantReadIsolation proves a token scoped to one tenant cannot read
// another tenant's data through the API, and (because the token draws its perms
// solely from the seeded operator role) doubles as an end-to-end check that role
// seeding works: a 403 here would mean the token resolved zero permissions.
func TestCrossTenantReadIsolation(t *testing.T) {
	ta := bootAPI(t)

	custB := model.NewID()
	if err := ta.store.CreateTenant(ta.ctx, custB, "Customer B", "cust-b"); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	// No raw scopes — permissions come ONLY from custB's seeded operator role.
	bClear, bTok := auth.MintToken(custB, "cust-b-operator", nil,
		&model.APIToken{RoleNames: []string{"operator"}})
	if err := ta.store.CreateAPIToken(ta.ctx, bTok); err != nil {
		t.Fatalf("create custB token: %v", err)
	}

	// A host in the operator's HOME tenant (DefaultTenant).
	homeHostID, _ := ta.createHost("home-host", "/")

	// custB's operator reading the home host: must be 404 (invisible across the
	// boundary), NOT 403 (which would mean role seeding gave it no perms at all).
	code, body := ta.do("GET", "/api/v1/objects/"+homeHostID, nil, bearer(bClear))
	switch code {
	case http.StatusForbidden:
		t.Fatalf("custB operator got 403 — role seeding regressed (token resolved no perms): %s", body)
	case http.StatusNotFound:
		// correct: authorized, but the object is invisible across the tenant boundary.
	default:
		t.Fatalf("cross-tenant GET should be 404, got %d: %s", code, body)
	}

	// …and the home host must not leak into custB's listing.
	code, list := ta.do("GET", "/api/v1/hosts", nil, bearer(bClear))
	if code != http.StatusOK {
		t.Fatalf("custB list hosts: %d %s", code, list)
	}
	if strings.Contains(string(list), homeHostID) {
		t.Fatalf("custB must not see the home tenant's host in its listing")
	}

	// custB's operator CAN create a host in its own tenant — proves the seeded
	// role actually grants objects:write.
	code, body = ta.do("POST", "/api/v1/hosts", map[string]any{
		"name": "b-host", "folder": "/",
		"spec": map[string]any{"checkCommand": "passive"},
	}, bearer(bClear))
	if code != http.StatusCreated {
		t.Fatalf("custB operator should create a host in its own tenant: %d %s", code, body)
	}
	bHostID := ta.id(body)

	// The home operator (no X-Northplane-Tenant switch) must not see custB's host.
	code, list = ta.do("GET", "/api/v1/hosts", nil, bearer(ta.adminToken))
	if code != http.StatusOK {
		t.Fatalf("home list hosts: %d %s", code, list)
	}
	if strings.Contains(string(list), bHostID) {
		t.Fatalf("home tenant must not see custB's host in its listing")
	}
	if c, _ := ta.do("GET", "/api/v1/objects/"+bHostID, nil, bearer(ta.adminToken)); c != http.StatusNotFound {
		t.Fatalf("home operator reading custB's host should 404, got %d", c)
	}
}

func containsPerm(perms []model.Permission, want model.Permission) bool {
	for _, p := range perms {
		if p == want {
			return true
		}
	}
	return false
}

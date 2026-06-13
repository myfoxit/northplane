package api

// Tests for the CMP multi-customer console's server-side contract: the
// X-Northplane-Tenant switch gate (tenantOf) and cross-tenant audit
// attribution. An admin:tenants operator manages many isolated customers from
// one console; these prove the switch is permission-gated and that actions on
// a customer land in that customer's tenant (objects) and audit log.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// tenantHeader sets the CMP active-customer switch header.
func tenantHeader(id string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("X-Northplane-Tenant", id) }
}

// TestTenantOfHeaderSwitch is the security gate the whole console rests on:
// only an admin:tenants operator may switch the active tenant via the header;
// everyone else stays pinned to their own tenant, so isolation holds even if a
// stale/forged header is sent.
func TestTenantOfHeaderSwitch(t *testing.T) {
	a := &API{}
	principal := func(tenant string, perms ...model.Permission) *auth.Principal {
		return &auth.Principal{TenantID: tenant, Perms: perms}
	}
	req := func(hdr string) *http.Request {
		r := httptest.NewRequest("GET", "/api/v1/hosts", nil)
		if hdr != "" {
			r.Header.Set("X-Northplane-Tenant", hdr)
		}
		return r
	}
	cases := []struct {
		name string
		hdr  string
		p    *auth.Principal
		want string
	}{
		{"no header → own tenant", "", principal("home", "admin:tenants"), "home"},
		{"admin:tenants switches", "cust-b", principal("home", "admin:tenants"), "cust-b"},
		{"wildcard implies the perm", "cust-b", principal("home", "*:*"), "cust-b"},
		{"unprivileged header ignored", "cust-b", principal("home", "objects:read"), "home"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.tenantOf(req(tc.hdr), tc.p); got != tc.want {
				t.Fatalf("tenantOf = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCrossTenantAuditAttribution proves an admin:tenants operator acting on
// another customer (via the header) creates the object in THAT customer's
// tenant and that the audit entry lands in the customer's log — not the
// operator's home tenant. This is what keeps each customer's audit trail
// faithful when managed centrally.
func TestCrossTenantAuditAttribution(t *testing.T) {
	ta := bootAPI(t)

	custB := model.NewID()
	if err := ta.store.CreateTenant(ta.ctx, custB, "Customer B", "cust-b"); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	code, body := ta.do("POST", "/api/v1/hosts", map[string]any{
		"name": "edge-host", "folder": "/",
		"spec": map[string]any{"checkCommand": "passive"},
	}, bearer(ta.adminToken), tenantHeader(custB))
	if code != http.StatusCreated {
		t.Fatalf("create host in cust-b: %d %s", code, body)
	}
	hostID := ta.id(body)

	// The object belongs to Customer B, not the operator's home (DefaultTenant).
	if _, err := ta.store.GetObject(ta.ctx, custB, hostID); err != nil {
		t.Fatalf("host should exist in cust-b: %v", err)
	}
	if obj, err := ta.store.GetObject(ta.ctx, model.DefaultTenant, hostID); err == nil && obj != nil {
		t.Fatal("host must NOT exist in the operator's home tenant")
	}

	// The audit entry is attributed to Customer B…
	inB, err := ta.store.QueryAudit(ta.ctx, storage.AuditFilter{TenantID: custB, Resource: hostID})
	if err != nil {
		t.Fatalf("query cust-b audit: %v", err)
	}
	if len(inB) == 0 {
		t.Fatal("expected an audit entry in cust-b for the created host")
	}
	if inB[0].ActorID == "" {
		t.Fatal("audit entry must still identify the operator who acted")
	}
	// …and NOT to the operator's home tenant.
	inHome, err := ta.store.QueryAudit(ta.ctx, storage.AuditFilter{TenantID: model.DefaultTenant, Resource: hostID})
	if err != nil {
		t.Fatalf("query home audit: %v", err)
	}
	if len(inHome) != 0 {
		t.Fatalf("audit must not land in the operator's home tenant, got %d entries", len(inHome))
	}
}

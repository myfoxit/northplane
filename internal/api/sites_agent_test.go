package api

// Tests for the federation main side (sites: CRUD, bundle validation,
// edge heartbeat/pull contract) and the agent check-config pull.

import (
	"net/http"
	"testing"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
)

const testBundle = `kind: Host
metadata:
  name: edge-db-01
spec:
  checkCommand: passive
---
kind: Service
metadata:
  name: postgres
  host: edge-db-01
spec:
  checkCommand: "agent:exec:check_pgsql"
`

// edgeToken mints the tenant-bound token an edge instance would use.
func (ta *testAPI) edgeToken() string {
	ta.t.Helper()
	clear, tok := auth.MintToken(model.DefaultTenant, "edge-customer-a",
		[]model.Permission{"sites:connect"}, nil)
	if err := ta.store.CreateAPIToken(ta.ctx, tok); err != nil {
		ta.t.Fatal(err)
	}
	return clear
}

func TestSitesLifecycle(t *testing.T) {
	ta := bootAPI(t)
	edge := ta.edgeToken()

	// invalid bundle is rejected on save (fails on main, not on the edge)
	code, body := ta.admin("POST", "/api/v1/sites", map[string]any{
		"name": "bad", "bundle": "kind: Nonsense\nmetadata:\n  name: x\n"})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid bundle: %d %s", code, body)
	}

	code, body = ta.admin("POST", "/api/v1/sites", map[string]any{
		"name": "customer-a", "description": "Kunde A", "bundle": testBundle})
	if code != http.StatusCreated {
		t.Fatalf("create site: %d %s", code, body)
	}

	// edge pulls its bundle: 200 + ETag, then 304 on the same content
	rec := ta.raw("GET", "/api/v1/sites/customer-a:pull", nil, bearer(edge))
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") == "" {
		t.Fatalf("pull: %d etag=%q", rec.Code, rec.Header().Get("ETag"))
	}
	if got := rec.Body.String(); got != testBundle {
		t.Fatalf("pull body mismatch:\n%s", got)
	}
	tag := rec.Header().Get("ETag")
	rec = ta.raw("GET", "/api/v1/sites/customer-a:pull", nil, bearer(edge),
		func(r *http.Request) { r.Header.Set("If-None-Match", tag) })
	if rec.Code != http.StatusNotModified {
		t.Fatalf("conditional pull: %d", rec.Code)
	}

	// edge heartbeats; the overview shows it as connected with stats
	code, body = ta.do("POST", "/api/v1/sites/customer-a:heartbeat", model.SiteHeartbeat{
		Version: "1.2.3", BundleETag: tag,
		Stats: map[string]int64{"hosts": 5, "alertsOpen": 1},
	}, bearer(edge))
	if code != http.StatusNoContent {
		t.Fatalf("heartbeat: %d %s", code, body)
	}
	code, body = ta.read("GET", "/api/v1/sites:overview", nil)
	if code != http.StatusOK {
		t.Fatalf("overview: %d %s", code, body)
	}
	var overview struct{ Items []model.SiteView }
	mustJSON(t, body, &overview)
	if len(overview.Items) != 1 {
		t.Fatalf("overview items: %s", body)
	}
	site := overview.Items[0]
	if !site.Connected || site.Status.Version != "1.2.3" ||
		site.Status.Stats["hosts"] != 5 || site.Status.LastSeenAt == nil {
		t.Fatalf("overview status: %+v", site)
	}

	// scopes: the read token may see the overview but not connect
	if code, _ := ta.read("POST", "/api/v1/sites/customer-a:heartbeat",
		model.SiteHeartbeat{}); code != http.StatusForbidden {
		t.Fatalf("read token heartbeat: want 403, got %d", code)
	}
	// the edge scope cannot touch site configuration
	if code, _ := ta.do("PUT", "/api/v1/sites/customer-a",
		map[string]any{"name": "customer-a"}, bearer(edge), ifMatch(1)); code != http.StatusForbidden {
		t.Fatalf("edge token site update: want 403, got %d", code)
	}

	// unknown site → 404; disabled site → 403 on both edge endpoints
	if code, _ := ta.do("POST", "/api/v1/sites/ghost:heartbeat",
		model.SiteHeartbeat{}, bearer(edge)); code != http.StatusNotFound {
		t.Fatalf("ghost heartbeat: want 404, got %d", code)
	}
	code, body = ta.admin("PUT", "/api/v1/sites/customer-a", map[string]any{
		"name": "customer-a", "bundle": testBundle, "disabled": true}, ifMatch(1))
	if code != http.StatusOK {
		t.Fatalf("disable site: %d %s", code, body)
	}
	if code, _ = ta.do("GET", "/api/v1/sites/customer-a:pull", nil, bearer(edge)); code != http.StatusForbidden {
		t.Fatalf("disabled pull: want 403, got %d", code)
	}
	if code, _ = ta.do("POST", "/api/v1/sites/customer-a:heartbeat",
		model.SiteHeartbeat{}, bearer(edge)); code != http.StatusForbidden {
		t.Fatalf("disabled heartbeat: want 403, got %d", code)
	}
}

func TestAgentChecksPull(t *testing.T) {
	ta := bootAPI(t)

	code, body := ta.admin("POST", "/api/v1/hosts", map[string]any{
		"name": "db-01",
		"spec": map[string]any{"checkCommand": "passive", "address": "10.1.2.3"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create host: %d %s", code, body)
	}
	code, body = ta.admin("POST", "/api/v1/services", map[string]any{
		"name": "disk", "host": "db-01",
		"spec": map[string]any{
			"checkCommand": "agent:exec:check_disk",
			"args":         []string{"-w", "80%", "-H", "$HOSTADDRESS$"},
			"interval":     "120s", "timeout": "10s",
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("create agent service: %d %s", code, body)
	}
	// a builtin service must NOT appear in the agent pull
	code, body = ta.admin("POST", "/api/v1/services", map[string]any{
		"name": "ping", "host": "db-01",
		"spec": map[string]any{"checkCommand": "builtin:icmp"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create builtin service: %d %s", code, body)
	}

	code, body = ta.read("GET", "/api/v1/agent/checks?host=db-01", nil)
	if code != http.StatusOK {
		t.Fatalf("agent checks: %d %s", code, body)
	}
	var resp AgentChecksResponse
	mustJSON(t, body, &resp)
	if resp.Host != "db-01" || len(resp.Checks) != 1 {
		t.Fatalf("agent checks payload: %s", body)
	}
	chk := resp.Checks[0]
	if chk.Service != "disk" || chk.Command != "check_disk" {
		t.Fatalf("check identity: %+v", chk)
	}
	// $HOSTADDRESS$ expanded server-side; literal args preserved
	if len(chk.Args) != 4 || chk.Args[3] != "10.1.2.3" || chk.Args[1] != "80%" {
		t.Fatalf("check args: %+v", chk.Args)
	}
	if chk.IntervalSeconds != 120 || chk.TimeoutSeconds != 10 {
		t.Fatalf("check timing: %+v", chk)
	}

	// unknown host → 404; missing parameter → 422
	if code, _ = ta.read("GET", "/api/v1/agent/checks?host=ghost", nil); code != http.StatusNotFound {
		t.Fatalf("ghost host: want 404, got %d", code)
	}
	if code, _ = ta.read("GET", "/api/v1/agent/checks", nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("missing host param: want 422, got %d", code)
	}
}

func TestDirectoryEndpointsUnconfigured(t *testing.T) {
	ta := bootAPI(t)
	code, body := ta.admin("GET", "/api/v1/directory/status", nil)
	if code != http.StatusOK {
		t.Fatalf("status: %d %s", code, body)
	}
	var st struct {
		Configured bool `json:"configured"`
	}
	mustJSON(t, body, &st)
	if st.Configured {
		t.Fatalf("unconfigured install must report configured=false: %s", body)
	}
	code, body = ta.admin("POST", "/api/v1/directory:sync", nil)
	if code != http.StatusNotImplemented {
		t.Fatalf("sync unconfigured: want 501, got %d %s", code, body)
	}
	// admin-only surface
	if code, _ := ta.read("GET", "/api/v1/directory/status", nil); code != http.StatusForbidden {
		t.Fatalf("status as reader: want 403, got %d", code)
	}
}

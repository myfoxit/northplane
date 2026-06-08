package api

// Coverage for the remaining registered surfaces:
//   - incidents CRUD + lifecycle (create / get / update with If-Match /
//     resolve / merge) and the alert→incident link,
//   - the generic resource-document CRUD (templates) which backs
//     templates / check-commands / time-periods (ETag + If-Match + 422),
//   - heartbeats (define / beat / list / delete),
//   - the problems view and the object effective-config / check-now
//     subroutes,
//   - alert severity filtering.

import (
	"net/http"
	"testing"

	"github.com/northplane/northplane/internal/model"
)

// --- incidents ---------------------------------------------------------------

func TestIncidentsLifecycle(t *testing.T) {
	ta := bootAPI(t)
	id, _ := ta.createHost("web01", "/")
	al := ta.seedAlert(id, "web01 down", model.SevCritical)

	// create with the alert attached.
	code, body := ta.admin("POST", "/api/v1/incidents", map[string]any{
		"title": "Outage", "severity": "critical", "alertIds": []string{al.ID}})
	if code != http.StatusCreated {
		t.Fatalf("create incident: %d %s", code, body)
	}
	var in model.Incident
	mustJSON(t, body, &in)
	if in.Status != model.IncidentOpen || in.Title != "Outage" {
		t.Fatalf("incident not opened: %+v", in)
	}

	// missing title → 422.
	if c, b := ta.admin("POST", "/api/v1/incidents", map[string]any{"severity": "warning"}); c != http.StatusUnprocessableEntity {
		t.Fatalf("incident no title: want 422, got %d %s", c, b)
	}

	// get returns the incident with its alert linked.
	code, body = ta.admin("GET", "/api/v1/incidents/"+in.ID, nil)
	if code != 200 {
		t.Fatalf("get incident: %d %s", code, body)
	}
	var got struct {
		Incident model.Incident `json:"incident"`
		Alerts   []model.Alert  `json:"alerts"`
	}
	mustJSON(t, body, &got)
	if len(got.Alerts) != 1 || got.Alerts[0].ID != al.ID {
		t.Fatalf("incident alert link missing: %s", body)
	}

	// update requires If-Match: without it → 428.
	if c, _ := ta.admin("PUT", "/api/v1/incidents/"+in.ID, map[string]any{"title": "x"}); c != http.StatusPreconditionRequired {
		t.Fatalf("incident update no If-Match: want 428, got %d", c)
	}
	// with correct version → 200.
	code, body = ta.admin("PUT", "/api/v1/incidents/"+in.ID,
		map[string]any{"title": "Outage (mitigating)", "impact": "checkout down"}, ifMatch(in.Version))
	if code != 200 {
		t.Fatalf("incident update: %d %s", code, body)
	}

	// list open incidents finds it.
	code, body = ta.admin("GET", "/api/v1/incidents?open=true", nil)
	if code != 200 {
		t.Fatalf("list incidents: %d %s", code, body)
	}
	var list struct{ Items []model.Incident }
	mustJSON(t, body, &list)
	if len(list.Items) != 1 {
		t.Fatalf("open incidents want 1, got %d: %s", len(list.Items), body)
	}

	// resolve → incident resolved AND its alert resolved.
	code, body = ta.admin("POST", "/api/v1/incidents/"+in.ID+":resolve", nil)
	if code != 200 {
		t.Fatalf("resolve incident: %d %s", code, body)
	}
	mustJSON(t, body, &in)
	if in.Status != model.IncidentResolved {
		t.Fatalf("incident not resolved: %+v", in)
	}
	got2, err := ta.store.GetAlert(ta.ctx, model.DefaultTenant, al.ID)
	if err != nil || got2.Status != model.AlertResolved {
		t.Fatalf("incident resolve did not close its alert: %+v %v", got2, err)
	}
}

func TestIncidentsMerge(t *testing.T) {
	ta := bootAPI(t)
	id, _ := ta.createHost("web01", "/")
	a1 := ta.seedAlert(id, "a1", model.SevWarning)
	a2 := ta.seedAlert(id, "a2", model.SevWarning)

	mk := func(title, alertID string) model.Incident {
		_, body := ta.admin("POST", "/api/v1/incidents", map[string]any{
			"title": title, "alertIds": []string{alertID}})
		var in model.Incident
		mustJSON(t, body, &in)
		return in
	}
	target := mk("primary", a1.ID)
	source := mk("secondary", a2.ID)

	code, body := ta.admin("POST", "/api/v1/incidents/"+target.ID+":merge",
		map[string]any{"sourceIds": []string{source.ID}})
	if code != 200 {
		t.Fatalf("merge: %d %s", code, body)
	}

	// the source's alert is now linked to the target.
	moved, _ := ta.store.GetAlert(ta.ctx, model.DefaultTenant, a2.ID)
	if moved.IncidentID != target.ID {
		t.Fatalf("merge did not reassign alert: %+v", moved)
	}
	// source incident is resolved.
	src, _ := ta.store.GetIncident(ta.ctx, model.DefaultTenant, source.ID)
	if src.Status != model.IncidentResolved {
		t.Fatalf("merged source not resolved: %+v", src)
	}
}

func TestIncidentsAuthz(t *testing.T) {
	ta := bootAPI(t)
	// reader has incidents:read but not incidents:write.
	if c, _ := ta.read("GET", "/api/v1/incidents", nil); c != 200 {
		t.Fatalf("reader list incidents: want 200, got %d", c)
	}
	if c, b := ta.read("POST", "/api/v1/incidents", map[string]any{"title": "x"}); c != http.StatusForbidden {
		t.Fatalf("reader create incident: want 403, got %d %s", c, b)
	}
}

// --- resource CRUD (templates) ----------------------------------------------

func TestResourceCRUDTemplates(t *testing.T) {
	ta := bootAPI(t)

	tpl := map[string]any{
		"name": "linux-base", "kind": "host",
		"spec": map[string]any{"checkCommand": "passive", "interval": "30s"},
	}

	// create → 201 + ETag.
	rec := ta.raw("POST", "/api/v1/templates", tpl, bearer(ta.adminToken))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create template: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatalf("create template missing ETag")
	}

	// missing name → 422.
	if c, b := ta.admin("POST", "/api/v1/templates", map[string]any{"kind": "host"}); c != http.StatusUnprocessableEntity {
		t.Fatalf("template no name: want 422, got %d %s", c, b)
	}

	// get → 200 + ETag.
	gr := ta.raw("GET", "/api/v1/templates/linux-base", nil, bearer(ta.adminToken))
	if gr.Code != 200 || gr.Header().Get("ETag") == "" {
		t.Fatalf("get template: %d etag=%q", gr.Code, gr.Header().Get("ETag"))
	}
	ver := ta.version(gr.Body.Bytes())

	// update without If-Match → 428.
	if c, _ := ta.admin("PUT", "/api/v1/templates/linux-base", tpl); c != http.StatusPreconditionRequired {
		t.Fatalf("template update no If-Match: want 428, got %d", c)
	}
	// update with correct version → 200.
	tpl["spec"] = map[string]any{"checkCommand": "passive", "interval": "60s"}
	if c, b := ta.admin("PUT", "/api/v1/templates/linux-base", tpl, ifMatch(ver)); c != 200 {
		t.Fatalf("template update: %d %s", c, b)
	}

	// list → one template.
	code, body := ta.admin("GET", "/api/v1/templates", nil)
	if code != 200 {
		t.Fatalf("list templates: %d %s", code, body)
	}
	var list struct{ Items []map[string]any }
	mustJSON(t, body, &list)
	if len(list.Items) != 1 {
		t.Fatalf("want 1 template, got %d: %s", len(list.Items), body)
	}

	// delete → 204, then get → 404.
	if c, _ := ta.admin("DELETE", "/api/v1/templates/linux-base", nil); c != http.StatusNoContent {
		t.Fatalf("delete template: %d", c)
	}
	if c, _ := ta.admin("GET", "/api/v1/templates/linux-base", nil); c != http.StatusNotFound {
		t.Fatalf("get deleted template: want 404, got %d", c)
	}
}

func TestResourceCRUDAuthz(t *testing.T) {
	ta := bootAPI(t)
	// templates use objects:read for reads but config:write for writes;
	// the reader token has objects:read only.
	if c, _ := ta.read("GET", "/api/v1/templates", nil); c != 200 {
		t.Fatalf("reader list templates: want 200, got %d", c)
	}
	if c, b := ta.read("POST", "/api/v1/templates", map[string]any{
		"name": "x", "kind": "host", "spec": map[string]any{}}); c != http.StatusForbidden {
		t.Fatalf("reader create template: want 403, got %d %s", c, b)
	}
}

// --- check-command console + builtins ---------------------------------------

func TestCheckCommandBuiltins(t *testing.T) {
	ta := bootAPI(t)
	code, body := ta.admin("GET", "/api/v1/check-commands:builtins", nil)
	if code != 200 {
		t.Fatalf("builtins: %d %s", code, body)
	}
	var names []string
	mustJSON(t, body, &names)
	if len(names) == 0 {
		t.Fatalf("expected at least one builtin check name")
	}
}

// --- heartbeats --------------------------------------------------------------

func TestHeartbeatsCRUDAndBeat(t *testing.T) {
	ta := bootAPI(t)

	// define → 201.
	code, body := ta.admin("POST", "/api/v1/heartbeats", map[string]any{
		"name": "nightly-backup", "expectEvery": "24h", "severity": "critical"})
	if code != http.StatusCreated {
		t.Fatalf("create heartbeat: %d %s", code, body)
	}

	// missing expectEvery → 422.
	if c, b := ta.admin("POST", "/api/v1/heartbeats", map[string]any{"name": "x"}); c != http.StatusUnprocessableEntity {
		t.Fatalf("heartbeat no interval: want 422, got %d %s", c, b)
	}

	// beat via GET (cron-friendly) → 200.
	if c, b := ta.admin("GET", "/api/v1/heartbeats/nightly-backup/beat", nil); c != 200 {
		t.Fatalf("beat: %d %s", c, b)
	}

	// list → one heartbeat with a recorded last beat.
	code, body = ta.admin("GET", "/api/v1/heartbeats", nil)
	if code != 200 {
		t.Fatalf("list heartbeats: %d %s", code, body)
	}
	var list struct{ Items []model.Heartbeat }
	mustJSON(t, body, &list)
	if len(list.Items) != 1 || list.Items[0].LastBeat == nil {
		t.Fatalf("heartbeat not recorded: %s", body)
	}

	// delete → 204.
	if c, _ := ta.admin("DELETE", "/api/v1/heartbeats/nightly-backup", nil); c != http.StatusNoContent {
		t.Fatalf("delete heartbeat: %d", c)
	}
}

// --- problems + object subroutes --------------------------------------------

func TestIncidentGet404(t *testing.T) {
	ta := bootAPI(t)
	if c, b := ta.admin("GET", "/api/v1/incidents/np-missing", nil); c != http.StatusNotFound {
		t.Fatalf("get unknown incident: want 404, got %d %s", c, b)
	}
	if c, _ := ta.admin("POST", "/api/v1/incidents/np-missing:resolve", nil); c != http.StatusNotFound {
		t.Fatalf("resolve unknown incident: want 404, got %d", c)
	}
}

func TestObjectsHostAndFolderFilter(t *testing.T) {
	ta := bootAPI(t)
	hostID, _ := ta.createHost("web01", "/prod")
	ta.createHost("db01", "/staging")
	// a service under web01.
	if c, b := ta.admin("POST", "/api/v1/services", map[string]any{
		"name": "http", "host": "web01",
		"spec": map[string]any{"checkCommand": "passive"}}); c != http.StatusCreated {
		t.Fatalf("seed service: %d %s", c, b)
	}

	// services filtered by hostId returns only that host's service.
	code, body := ta.admin("GET", "/api/v1/services?hostId="+hostID, nil)
	if code != 200 {
		t.Fatalf("services by host: %d %s", code, body)
	}
	var list struct{ Items []map[string]any }
	mustJSON(t, body, &list)
	if len(list.Items) != 1 {
		t.Fatalf("hostId filter want 1 service, got %d: %s", len(list.Items), body)
	}

	// folder filter on the host listing.
	code, body = ta.admin("GET", "/api/v1/hosts?folder=/staging", nil)
	if code != 200 {
		t.Fatalf("hosts by folder: %d %s", code, body)
	}
	mustJSON(t, body, &list)
	if len(list.Items) != 1 {
		t.Fatalf("folder filter want 1 host, got %d: %s", len(list.Items), body)
	}
}

func TestProblemsView(t *testing.T) {
	ta := bootAPI(t)
	// empty problems view returns an empty list, not an error.
	code, body := ta.admin("GET", "/api/v1/problems", nil)
	if code != 200 {
		t.Fatalf("problems: %d %s", code, body)
	}
	var list struct{ Items []any }
	mustJSON(t, body, &list)
	if len(list.Items) != 0 {
		t.Fatalf("expected no problems on a fresh store: %s", body)
	}
}

func TestObjectEffectiveConfigAndCheckNow(t *testing.T) {
	ta := bootAPI(t)
	id, _ := ta.createHost("web01", "/")

	// effective-config resolves through the catalog → 200 with a spec.
	code, body := ta.admin("GET", "/api/v1/objects/"+id+"/effective-config", nil)
	if code != 200 {
		t.Fatalf("effective-config: %d %s", code, body)
	}
	var eff struct {
		Spec struct {
			CheckCommand string `json:"checkCommand"`
		} `json:"spec"`
	}
	mustJSON(t, body, &eff)
	if eff.Spec.CheckCommand != "passive" {
		t.Fatalf("effective spec wrong: %s", body)
	}

	// unknown object effective-config → 404.
	if c, _ := ta.admin("GET", "/api/v1/objects/np-missing/effective-config", nil); c != http.StatusNotFound {
		t.Fatalf("effective-config unknown: want 404, got %d", c)
	}

	// check-now → 202 (scheduler enqueues a priority recheck).
	if c, b := ta.admin("POST", "/api/v1/objects/"+id+"/check-now", nil); c != http.StatusAccepted {
		t.Fatalf("check-now: want 202, got %d %s", c, b)
	}
	// check-now needs checks:run — reader is forbidden.
	if c, _ := ta.read("POST", "/api/v1/objects/"+id+"/check-now", nil); c != http.StatusForbidden {
		t.Fatalf("reader check-now: want 403, got %d", c)
	}
}

// --- resource list pagination ------------------------------------------------

func TestResourceListPaginationCursor(t *testing.T) {
	ta := bootAPI(t)
	// seed three templates; a limit of 2 must surface a nextCursor.
	for _, n := range []string{"t-a", "t-b", "t-c"} {
		if c, b := ta.admin("POST", "/api/v1/templates", map[string]any{
			"name": n, "kind": "host", "spec": map[string]any{"checkCommand": "passive"}}); c != http.StatusCreated {
			t.Fatalf("seed %s: %d %s", n, c, b)
		}
	}
	code, body := ta.admin("GET", "/api/v1/templates?limit=2", nil)
	if code != 200 {
		t.Fatalf("paginated list: %d %s", code, body)
	}
	var page struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"nextCursor"`
	}
	mustJSON(t, body, &page)
	if len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("want 2 items + cursor, got %d items cursor=%q: %s",
			len(page.Items), page.NextCursor, body)
	}
	// follow the cursor → the remaining template.
	code, body = ta.admin("GET", "/api/v1/templates?limit=2&cursor="+page.NextCursor, nil)
	if code != 200 {
		t.Fatalf("cursor page: %d %s", code, body)
	}
	mustJSON(t, body, &page)
	if len(page.Items) != 1 {
		t.Fatalf("cursor page want 1 item, got %d: %s", len(page.Items), body)
	}
}

// --- system overview + health ------------------------------------------------

func TestOverviewAndHealth(t *testing.T) {
	ta := bootAPI(t)
	id, _ := ta.createHost("web01", "/")
	ta.seedAlert(id, "down", model.SevCritical)

	// wallboard overview aggregates summary + open alerts + incidents.
	code, body := ta.admin("GET", "/api/v1/overview", nil)
	if code != 200 {
		t.Fatalf("overview: %d %s", code, body)
	}
	var ov struct {
		Summary    map[string]any   `json:"summary"`
		OpenAlerts map[string]int64 `json:"openAlerts"`
		Incidents  []map[string]any `json:"openIncidents"`
		Queues     map[string]any   `json:"queues"`
	}
	mustJSON(t, body, &ov)
	if ov.Summary == nil {
		t.Fatalf("overview missing summary: %s", body)
	}

	// /healthz and /readyz are unauthenticated liveness/readiness probes.
	if c, b := ta.do("GET", "/healthz", nil); c != 200 || string(b) != "ok" {
		t.Fatalf("healthz: %d %s", c, b)
	}
	if c, _ := ta.do("GET", "/readyz", nil); c != 200 {
		t.Fatalf("readyz: %d", c)
	}
}

// --- alert severity filter ---------------------------------------------------

func TestAlertSeverityFilter(t *testing.T) {
	ta := bootAPI(t)
	id, _ := ta.createHost("web01", "/")
	ta.seedAlert(id, "crit", model.SevCritical)
	ta.seedAlert(id, "warn", model.SevWarning)

	code, body := ta.admin("GET", "/api/v1/alerts?severity=critical", nil)
	if code != 200 {
		t.Fatalf("severity filter: %d %s", code, body)
	}
	var list struct{ Items []model.Alert }
	mustJSON(t, body, &list)
	if len(list.Items) != 1 || list.Items[0].Severity != model.SevCritical {
		t.Fatalf("severity=critical should return only the critical alert: %s", body)
	}
}

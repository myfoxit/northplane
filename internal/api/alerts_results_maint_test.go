package api

// HTTP coverage for the operational surfaces beyond plain CRUD:
//   - passive result ingestion (accept/reject accounting + an end-to-end
//     run through the real pipeline that reflects state on the object),
//   - the alert lifecycle (list/filter by status, :ack, :resolve, :snooze
//     and the 404/double-resolve paths),
//   - downtimes / silences (create + list + validation), and
//   - the Idempotency-Key replay on the downtime create path.

import (
	"net/http"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// --- results ingestion -------------------------------------------------------

func TestResultsAcceptAndReject(t *testing.T) {
	ta := bootAPI(t)
	ta.createHost("web01", "/")
	// service must exist in the catalog for a service result to land.
	if code, body := ta.admin("POST", "/api/v1/services", map[string]any{
		"name": "http", "host": "web01",
		"spec": map[string]any{"checkCommand": "passive"}}); code != http.StatusCreated {
		t.Fatalf("create service: %d %s", code, body)
	}

	code, body := ta.admin("POST", "/api/v1/results", map[string]any{
		"results": []map[string]any{
			{"host": "web01", "service": "http", "state": 2, "output": "CRITICAL | t=1s"},
			{"host": "web01", "state": "OK", "output": "host up"},
			{"host": "ghost", "state": 0, "output": "nope"},                   // unknown host
			{"host": "web01", "service": "missing", "state": 0, "output": ""}, // unknown service
			{"host": "web01", "state": "WAT", "output": ""},                   // bad state
		},
	})
	if code != http.StatusAccepted {
		t.Fatalf("results: want 202, got %d %s", code, body)
	}
	var res struct {
		Accepted int      `json:"accepted"`
		Rejected []string `json:"rejected"`
	}
	mustJSON(t, body, &res)
	if res.Accepted != 2 {
		t.Fatalf("accepted = %d, want 2: %s", res.Accepted, body)
	}
	if len(res.Rejected) != 3 {
		t.Fatalf("rejected = %d (%v), want 3", len(res.Rejected), res.Rejected)
	}
}

func TestResultsReflectStateOnObject(t *testing.T) {
	ta := bootAPI(t)
	ta.runPipeline() // start the result-processing loop
	ta.createHost("web01", "/")
	// A service is used (not a host) because the host state machine maps
	// CRITICAL→DOWN; on a service a state-2 result stays CRITICAL, which
	// is the cleaner assertion that "the result is reflected on the object".
	code, body := ta.admin("POST", "/api/v1/services", map[string]any{
		"name": "http", "host": "web01",
		"spec": map[string]any{"checkCommand": "passive"}})
	if code != http.StatusCreated {
		t.Fatalf("create service: %d %s", code, body)
	}
	svcID := ta.id(body)
	if e := ta.a.Catalog.Get(svcID); e == nil {
		t.Fatal("service not in catalog after create")
	}

	code, body = ta.admin("POST", "/api/v1/results", map[string]any{
		"results": []map[string]any{
			{"host": "web01", "service": "http", "state": 2,
				"output": "HTTP CRITICAL - connection refused"}}})
	if code != http.StatusAccepted {
		t.Fatalf("submit result: %d %s", code, body)
	}

	// the pipeline batches every 250ms — poll the object's live state.
	waitFor(t, "service state reflects CRITICAL", func() bool {
		cs, err := ta.store.GetCheckState(ta.ctx, svcID)
		return err == nil && cs.State == model.StateCritical
	})

	// and it is visible through the API decoration too.
	_, body = ta.admin("GET", "/api/v1/objects/"+svcID, nil)
	var view struct {
		State *struct {
			State  int    `json:"state"`
			Output string `json:"output"`
		} `json:"state"`
	}
	mustJSON(t, body, &view)
	if view.State == nil || view.State.State != int(model.StateCritical) {
		t.Fatalf("GET object did not surface CRITICAL state: %s", body)
	}
	if view.State.Output != "HTTP CRITICAL - connection refused" {
		t.Fatalf("output not reflected: %s", body)
	}
}

func TestResultsAuthz(t *testing.T) {
	ta := bootAPI(t)
	ta.createHost("web01", "/")
	// results need objects:write — the read-only token is rejected.
	code, body := ta.read("POST", "/api/v1/results", map[string]any{
		"results": []map[string]any{{"host": "web01", "state": 0, "output": "ok"}}})
	if code != http.StatusForbidden {
		t.Fatalf("reader submit results: want 403, got %d %s", code, body)
	}
}

// --- alert lifecycle ---------------------------------------------------------

func TestAlertsLifecycle(t *testing.T) {
	ta := bootAPI(t)
	id, _ := ta.createHost("web01", "/")
	al := ta.seedAlert(id, "web01 down", model.SevCritical)

	// list status=open returns the alert.
	code, body := ta.admin("GET", "/api/v1/alerts?status=open", nil)
	if code != 200 {
		t.Fatalf("list open: %d %s", code, body)
	}
	var list struct {
		Items []model.Alert
	}
	mustJSON(t, body, &list)
	if len(list.Items) != 1 || list.Items[0].ID != al.ID {
		t.Fatalf("open list want the seeded alert, got %s", body)
	}

	// filter by a non-matching status returns nothing.
	_, body = ta.admin("GET", "/api/v1/alerts?status=resolved", nil)
	mustJSON(t, body, &list)
	if len(list.Items) != 0 {
		t.Fatalf("resolved filter should be empty: %s", body)
	}

	// ack → 200, status acked, ackedBy recorded.
	code, body = ta.admin("POST", "/api/v1/alerts/"+al.ID+":ack",
		map[string]string{"comment": "on it"})
	if code != 200 {
		t.Fatalf("ack: %d %s", code, body)
	}
	var acked model.Alert
	mustJSON(t, body, &acked)
	if acked.Status != model.AlertAcked || acked.AckedBy == "" {
		t.Fatalf("ack did not transition: %+v", acked)
	}

	// acking again → 404 (no longer in 'open' state).
	if code, _ := ta.admin("POST", "/api/v1/alerts/"+al.ID+":ack", nil); code != http.StatusNotFound {
		t.Fatalf("double-ack: want 404, got %d", code)
	}

	// resolve → 200, status resolved.
	code, body = ta.admin("POST", "/api/v1/alerts/"+al.ID+":resolve", nil)
	if code != 200 {
		t.Fatalf("resolve: %d %s", code, body)
	}
	var resolved model.Alert
	mustJSON(t, body, &resolved)
	if resolved.Status != model.AlertResolved {
		t.Fatalf("resolve did not transition: %+v", resolved)
	}

	// resolving a resolved alert → 404.
	if code, _ := ta.admin("POST", "/api/v1/alerts/"+al.ID+":resolve", nil); code != http.StatusNotFound {
		t.Fatalf("double-resolve: want 404, got %d", code)
	}
}

func TestAlertsSnooze(t *testing.T) {
	ta := bootAPI(t)
	id, _ := ta.createHost("web01", "/")
	al := ta.seedAlert(id, "web01 down", model.SevWarning)

	// past 'until' → 422.
	code, body := ta.admin("POST", "/api/v1/alerts/"+al.ID+":snooze",
		map[string]any{"until": time.Now().Add(-time.Hour)})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("snooze past: want 422, got %d %s", code, body)
	}

	// future 'until' → 200 + acked.
	code, body = ta.admin("POST", "/api/v1/alerts/"+al.ID+":snooze",
		map[string]any{"until": time.Now().Add(time.Hour)})
	if code != 200 {
		t.Fatalf("snooze: %d %s", code, body)
	}
	var a model.Alert
	mustJSON(t, body, &a)
	if a.Status != model.AlertAcked {
		t.Fatalf("snooze should ack: %+v", a)
	}
}

func TestAlertsGet404AndAuthz(t *testing.T) {
	ta := bootAPI(t)

	if code, _ := ta.admin("GET", "/api/v1/alerts/np-missing", nil); code != http.StatusNotFound {
		t.Fatalf("get unknown alert: want 404, got %d", code)
	}

	id, _ := ta.createHost("web01", "/")
	al := ta.seedAlert(id, "x", model.SevWarning)

	// reader may list/get but not ack (needs alerts:ack).
	if code, _ := ta.read("GET", "/api/v1/alerts/"+al.ID, nil); code != 200 {
		t.Fatalf("reader get alert: want 200, got %d", code)
	}
	if code, body := ta.read("POST", "/api/v1/alerts/"+al.ID+":ack", nil); code != http.StatusForbidden {
		t.Fatalf("reader ack: want 403, got %d %s", code, body)
	}
}

// --- downtimes / silences ----------------------------------------------------

func TestDowntimeCreateListAndIdempotency(t *testing.T) {
	ta := bootAPI(t)
	id, _ := ta.createHost("web01", "/")

	body := map[string]any{
		"objectId": id, "type": "fixed",
		"start":   time.Now().UTC(),
		"end":     time.Now().UTC().Add(time.Hour),
		"comment": "planned maintenance",
	}
	idemKey := func(r *http.Request) { r.Header.Set("Idempotency-Key", "dt-1") }

	// first create → 201.
	code, b1 := ta.admin("POST", "/api/v1/downtimes", body, idemKey)
	if code != http.StatusCreated {
		t.Fatalf("downtime create: %d %s", code, b1)
	}

	// replay with the same key+body → identical body, replay header.
	rec := ta.raw("POST", "/api/v1/downtimes", body, bearer(ta.adminToken), idemKey)
	if rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("missing Idempotency-Replayed header: %v", rec.Header())
	}
	if rec.Body.String() != string(b1)+"\n" && rec.Body.String() != string(b1) {
		// idempotent() writes the stored marshalled body (no trailing
		// newline); the first response came through writeJSON via the
		// idempotent path too — compare ignoring a possible newline.
		if trimNL(rec.Body.String()) != trimNL(string(b1)) {
			t.Fatalf("replay body differs:\n got=%q\nwant=%q", rec.Body.String(), b1)
		}
	}

	// list → exactly one downtime.
	code, lb := ta.admin("GET", "/api/v1/downtimes", nil)
	if code != 200 {
		t.Fatalf("list downtimes: %d %s", code, lb)
	}
	var list struct{ Items []model.Downtime }
	mustJSON(t, lb, &list)
	if len(list.Items) != 1 {
		t.Fatalf("want 1 downtime (idempotent), got %d: %s", len(list.Items), lb)
	}
}

func TestDowntimeValidation(t *testing.T) {
	ta := bootAPI(t)
	cases := []struct {
		name string
		body map[string]any
		code string
	}{
		{"no target", map[string]any{
			"comment": "x", "start": time.Now(), "end": time.Now().Add(time.Hour)},
			"np:validation/target"},
		{"no comment", map[string]any{
			"objectId": "x", "start": time.Now(), "end": time.Now().Add(time.Hour)},
			"np:validation/comment"},
		{"end before start", map[string]any{
			"objectId": "x", "comment": "c",
			"start": time.Now().Add(time.Hour), "end": time.Now()},
			"np:validation/window"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body := ta.admin("POST", "/api/v1/downtimes", c.body)
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("want 422, got %d %s", code, body)
			}
			if p := decodeProblem(t, body); p.Code != c.code {
				t.Fatalf("problem code = %q, want %q: %s", p.Code, c.code, body)
			}
		})
	}
}

func TestSilenceCreateListAndValidation(t *testing.T) {
	ta := bootAPI(t)

	// missing TTL → 422.
	code, body := ta.admin("POST", "/api/v1/silences", map[string]any{
		"selector": "env=prod", "comment": "noise"})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("no TTL: want 422, got %d %s", code, body)
	}
	if p := decodeProblem(t, body); p.Code != "np:validation/expiresAt" {
		t.Fatalf("bad TTL problem: %s", body)
	}

	// no match (neither selector nor textRegex) → 422.
	code, body = ta.admin("POST", "/api/v1/silences", map[string]any{
		"comment": "x", "expiresAt": time.Now().Add(time.Hour)})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("no match: want 422, got %d %s", code, body)
	}
	if p := decodeProblem(t, body); p.Code != "np:validation/match" {
		t.Fatalf("bad match problem: %s", body)
	}

	// valid silence → 201.
	code, body = ta.admin("POST", "/api/v1/silences", map[string]any{
		"selector": "env=prod", "comment": "deploy window",
		"expiresAt": time.Now().Add(time.Hour)})
	if code != http.StatusCreated {
		t.Fatalf("create silence: %d %s", code, body)
	}

	// list → one silence.
	code, body = ta.admin("GET", "/api/v1/silences", nil)
	if code != 200 {
		t.Fatalf("list silences: %d %s", code, body)
	}
	var list struct{ Items []model.Silence }
	mustJSON(t, body, &list)
	if len(list.Items) != 1 {
		t.Fatalf("want 1 silence, got %d: %s", len(list.Items), body)
	}
}

func TestDowntimeActiveListCascadeAndDelete(t *testing.T) {
	ta := bootAPI(t)
	hostID, _ := ta.createHost("web01", "/")
	// a service child so the host downtime can cascade its depth.
	_, sb := ta.admin("POST", "/api/v1/services", map[string]any{
		"name": "http", "host": "web01",
		"spec": map[string]any{"checkCommand": "passive"}})
	svcID := ta.id(sb)

	// an active (now-window) fixed downtime on the host.
	code, body := ta.admin("POST", "/api/v1/downtimes", map[string]any{
		"objectId": hostID, "type": "fixed",
		"start":   time.Now().UTC().Add(-time.Minute),
		"end":     time.Now().UTC().Add(time.Hour),
		"comment": "active window",
	})
	if code != http.StatusCreated {
		t.Fatalf("create active downtime: %d %s", code, body)
	}
	dtID := ta.id(body)

	// active-only list returns it.
	code, body = ta.admin("GET", "/api/v1/downtimes?active=true", nil)
	if code != 200 {
		t.Fatalf("active downtimes: %d %s", code, body)
	}
	var list struct{ Items []model.Downtime }
	mustJSON(t, body, &list)
	if len(list.Items) != 1 {
		t.Fatalf("active downtimes want 1, got %d: %s", len(list.Items), body)
	}

	// the create path ran RefreshDowntimeDepths → host (and its service
	// child) carry downtime depth >= 1.
	if cs, err := ta.store.GetCheckState(ta.ctx, hostID); err != nil || cs.DowntimeDepth < 1 {
		t.Fatalf("host downtime depth not applied: %+v %v", cs, err)
	}
	if cs, err := ta.store.GetCheckState(ta.ctx, svcID); err != nil || cs.DowntimeDepth < 1 {
		t.Fatalf("service child downtime depth not cascaded: %+v %v", cs, err)
	}

	// delete → 204; depth recomputed back to 0.
	if c, _ := ta.admin("DELETE", "/api/v1/downtimes/"+dtID, nil); c != http.StatusNoContent {
		t.Fatalf("delete downtime: %d", c)
	}
	if cs, err := ta.store.GetCheckState(ta.ctx, hostID); err != nil || cs.DowntimeDepth != 0 {
		t.Fatalf("host downtime depth not cleared after delete: %+v %v", cs, err)
	}
}

func TestSilenceDeleteAndActiveList(t *testing.T) {
	ta := bootAPI(t)
	_, body := ta.admin("POST", "/api/v1/silences", map[string]any{
		"selector": "env=prod", "comment": "window",
		"expiresAt": time.Now().Add(time.Hour)})
	sid := ta.id(body)

	// active-only list returns the live silence.
	code, lb := ta.admin("GET", "/api/v1/silences?active=true", nil)
	if code != 200 {
		t.Fatalf("active silences: %d %s", code, lb)
	}
	var list struct{ Items []model.Silence }
	mustJSON(t, lb, &list)
	if len(list.Items) != 1 {
		t.Fatalf("active silences want 1, got %d: %s", len(list.Items), lb)
	}

	// expire early → 204.
	if c, _ := ta.admin("DELETE", "/api/v1/silences/"+sid, nil); c != http.StatusNoContent {
		t.Fatalf("delete silence: %d", c)
	}
}

func TestMaintenanceAuthz(t *testing.T) {
	ta := bootAPI(t)
	// downtimes:write and silences:write are not in the reader scope.
	if code, _ := ta.read("POST", "/api/v1/downtimes", map[string]any{
		"objectId": "x", "comment": "c",
		"start": time.Now(), "end": time.Now().Add(time.Hour)}); code != http.StatusForbidden {
		t.Fatalf("reader create downtime: want 403, got %d", code)
	}
	if code, _ := ta.read("POST", "/api/v1/silences", map[string]any{
		"selector": "env=prod", "comment": "c",
		"expiresAt": time.Now().Add(time.Hour)}); code != http.StatusForbidden {
		t.Fatalf("reader create silence: want 403, got %d", code)
	}
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

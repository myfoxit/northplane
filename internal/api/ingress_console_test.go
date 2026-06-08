package api

// Coverage for the webhook ingress adapter and the check-command test
// console:
//   - POST /api/v1/ingest/{source}: source resolution, auth modes
//     (none / token), disabled-source rejection, normal-form normalization,
//     and the Alertmanager-compatible receiver,
//   - POST /api/v1/check-commands:test (ad-hoc builtin run).

import (
	"net/http"
	"testing"
)

func seedSource(t *testing.T, ta *testAPI, name, authMode string, enabled bool) {
	t.Helper()
	code, body := ta.admin("POST", "/api/v1/event-sources", map[string]any{
		"name": name, "type": "webhook", "authMode": authMode, "enabled": enabled,
	})
	if code != http.StatusCreated {
		t.Fatalf("seed event-source %q: %d %s", name, code, body)
	}
}

func TestIngestNormalForm(t *testing.T) {
	ta := bootAPI(t)
	seedSource(t, ta, "ci", "none", true)

	// a normal-form payload through the no-auth source → 202.
	body := map[string]any{
		"summary":  "deploy failed",
		"severity": "critical",
		"dedupKey": "deploy-42",
		"labels":   map[string]string{"service": "checkout"},
	}
	code, resp := ta.do("POST", "/api/v1/ingest/ci", body)
	if code != http.StatusAccepted {
		t.Fatalf("ingest: want 202, got %d %s", code, resp)
	}

	// the event landed in the store and is searchable as an ingress event.
	code, ev := ta.admin("GET", "/api/v1/events?types=ingress", nil)
	if code != 200 {
		t.Fatalf("events: %d %s", code, ev)
	}
	var list struct{ Items []map[string]any }
	mustJSON(t, ev, &list)
	if len(list.Items) == 0 {
		t.Fatalf("ingress event not persisted: %s", ev)
	}
}

func TestIngestWithCELMapping(t *testing.T) {
	ta := bootAPI(t)
	// a source with a CEL mapping that lifts fields out of a foreign payload.
	code, body := ta.admin("POST", "/api/v1/event-sources", map[string]any{
		"name": "grafana", "type": "webhook", "authMode": "none", "enabled": true,
		"mapping": map[string]string{
			"summary":     "payload.title",
			"severity":    `"warning"`,
			"labels.team": "payload.team",
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("seed mapped source: %d %s", code, body)
	}

	code, resp := ta.do("POST", "/api/v1/ingest/grafana", map[string]any{
		"title": "Latency high", "team": "payments"})
	if code != http.StatusAccepted {
		t.Fatalf("mapped ingest: want 202, got %d %s", code, resp)
	}

	// a payload that the mapping can't evaluate (missing field used in a
	// non-defaulting expression is tolerated by CEL as it returns a value;
	// instead send invalid JSON to hit the mapping/parse error path).
	code, resp = ta.do("POST", "/api/v1/ingest/grafana", "{not json")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("bad json to mapped source: want 422, got %d %s", code, resp)
	}
}

func TestIngestUnknownSource404(t *testing.T) {
	ta := bootAPI(t)
	code, body := ta.do("POST", "/api/v1/ingest/ghost", map[string]any{"summary": "x"})
	if code != http.StatusNotFound {
		t.Fatalf("unknown source: want 404, got %d %s", code, body)
	}
	if p := decodeProblem(t, body); p.Code != "np:ingress/unknown-source" {
		t.Fatalf("bad problem: %s", body)
	}
}

func TestIngestDisabledSource403(t *testing.T) {
	ta := bootAPI(t)
	seedSource(t, ta, "off", "none", false)
	code, body := ta.do("POST", "/api/v1/ingest/off", map[string]any{"summary": "x"})
	if code != http.StatusForbidden {
		t.Fatalf("disabled source: want 403, got %d %s", code, body)
	}
	if p := decodeProblem(t, body); p.Code != "np:ingress/disabled" {
		t.Fatalf("bad problem: %s", body)
	}
}

func TestIngestTokenAuth(t *testing.T) {
	ta := bootAPI(t)
	// a token-auth source with no secret configured cannot authenticate
	// (the auth secret is empty), so any token is rejected → 401.
	seedSource(t, ta, "secured", "token", true)
	code, body := ta.do("POST", "/api/v1/ingest/secured", map[string]any{"summary": "x"},
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer some-token") })
	if code != http.StatusUnauthorized {
		t.Fatalf("token source without secret: want 401, got %d %s", code, body)
	}
	if p := decodeProblem(t, body); p.Code != "np:ingress/auth" {
		t.Fatalf("bad problem: %s", body)
	}
}

func TestIngestAlertmanager(t *testing.T) {
	ta := bootAPI(t)
	seedSource(t, ta, "prom", "none", true)

	body := map[string]any{
		"alerts": []map[string]any{
			{"status": "firing", "fingerprint": "abc",
				"labels":      map[string]string{"severity": "critical", "alertname": "HighCPU"},
				"annotations": map[string]string{"summary": "CPU > 90%"}},
			{"status": "resolved", "fingerprint": "def",
				"labels": map[string]string{"alertname": "DiskFull"}},
		},
	}
	code, resp := ta.do("POST", "/api/v1/ingest/prom/alertmanager", body)
	if code != http.StatusAccepted {
		t.Fatalf("alertmanager: want 202, got %d %s", code, resp)
	}
}

func TestCheckCommandTestConsole(t *testing.T) {
	ta := bootAPI(t)
	// run a builtin check ad hoc; we don't assert on the check outcome
	// (network-dependent) — only that the console returns a structured 200.
	names := func() []string {
		_, b := ta.admin("GET", "/api/v1/check-commands:builtins", nil)
		var n []string
		mustJSON(t, b, &n)
		return n
	}()
	if len(names) == 0 {
		t.Skip("no builtin checks registered")
	}
	code, body := ta.admin("POST", "/api/v1/check-commands:test", map[string]any{
		"builtin": names[0], "address": "127.0.0.1"})
	if code != 200 {
		t.Fatalf("check-command test: %d %s", code, body)
	}
	var res struct {
		State int    `json:"state"`
		Label string `json:"label"`
	}
	mustJSON(t, body, &res)
	if res.Label == "" {
		t.Fatalf("test console returned no state label: %s", body)
	}
}

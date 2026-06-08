package api

// Coverage for the rule / escalation / contact / event surfaces:
//   - resource validation branches in validateResourceDoc (alert-rule CEL
//     compile, escalation-policy step requirement, channel type),
//   - the escalation-policy simulator,
//   - the alert-rule test runner against demo events,
//   - contacts / channels resource CRUD,
//   - event search + filtering.

import (
	"net/http"
	"testing"
	"time"
)

// --- resource validation -----------------------------------------------------

func TestAlertRuleValidation(t *testing.T) {
	ta := bootAPI(t)

	// invalid CEL match → 422 (compile failure surfaced as validation).
	code, body := ta.admin("POST", "/api/v1/alert-rules", map[string]any{
		"name": "bad", "match": "this is not (valid CEL", "severity": "critical"})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("bad rule: want 422, got %d %s", code, body)
	}
	if p := decodeProblem(t, body); p.Status != 422 {
		t.Fatalf("bad rule problem (want 422 shape): %s", body)
	}

	// valid rule → 201.
	code, body = ta.admin("POST", "/api/v1/alert-rules", map[string]any{
		"name": "prod-crit", "severity": "critical",
		"match": `event.type == "state_change" && event.state == "CRITICAL"`,
	})
	if code != http.StatusCreated {
		t.Fatalf("good rule: %d %s", code, body)
	}
}

func TestEscalationPolicyValidation(t *testing.T) {
	ta := bootAPI(t)

	// no steps → 422.
	code, body := ta.admin("POST", "/api/v1/escalation-policies", map[string]any{
		"name": "empty", "steps": []any{}})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("empty policy: want 422, got %d %s", code, body)
	}

	// valid policy with one step → 201.
	code, body = ta.admin("POST", "/api/v1/escalation-policies", map[string]any{
		"name": "p1",
		"steps": []map[string]any{
			{"after": "5m", "notify": map[string]any{"contact": "alice"}}},
	})
	if code != http.StatusCreated {
		t.Fatalf("good policy: %d %s", code, body)
	}
}

func TestChannelValidation(t *testing.T) {
	ta := bootAPI(t)

	// missing type → 422.
	code, body := ta.admin("POST", "/api/v1/channels", map[string]any{"name": "c1"})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("channel no type: want 422, got %d %s", code, body)
	}

	// valid channel → 201.
	code, body = ta.admin("POST", "/api/v1/channels", map[string]any{
		"name": "smtp", "type": "email", "enabled": true,
		"config": map[string]string{"host": "smtp.example.net"}})
	if code != http.StatusCreated {
		t.Fatalf("good channel: %d %s", code, body)
	}
}

// --- escalation simulator ----------------------------------------------------

func TestEscalationSimulate(t *testing.T) {
	ta := bootAPI(t)
	// seed a contact and a 2-step policy.
	if c, b := ta.admin("POST", "/api/v1/contacts", map[string]any{
		"name": "alice", "email": "alice@example.net"}); c != http.StatusCreated {
		t.Fatalf("seed contact: %d %s", c, b)
	}
	if c, b := ta.admin("POST", "/api/v1/escalation-policies", map[string]any{
		"name": "p1",
		"steps": []map[string]any{
			{"after": "5m", "notify": map[string]any{"contact": "alice"}},
			{"after": "15m", "notify": map[string]any{"contact": "alice"}}},
	}); c != http.StatusCreated {
		t.Fatalf("seed policy: %d %s", c, b)
	}

	code, body := ta.admin("POST", "/api/v1/escalation-policies/p1:simulate", nil)
	if code != 200 {
		t.Fatalf("simulate: %d %s", code, body)
	}
	var res struct {
		Steps []map[string]any `json:"steps"`
	}
	mustJSON(t, body, &res)
	if len(res.Steps) != 2 {
		t.Fatalf("simulate want 2 steps, got %d: %s", len(res.Steps), body)
	}

	// simulating an unknown policy → 404.
	if c, _ := ta.admin("POST", "/api/v1/escalation-policies/nope:simulate", nil); c != http.StatusNotFound {
		t.Fatalf("simulate unknown: want 404, got %d", c)
	}
}

// --- rule test runner --------------------------------------------------------

func TestAlertRuleTestRunner(t *testing.T) {
	ta := bootAPI(t)

	// run a rule against an in-line demo event that matches.
	body := map[string]any{
		"rule": map[string]any{
			"name":     "crit",
			"severity": "critical",
			"match":    `event.severity == "critical"`,
		},
		"demoEvents": []map[string]any{
			{"id": "e1", "type": "ingress", "severity": "critical",
				"ts": time.Now().UTC().Format(time.RFC3339)},
		},
	}
	code, respBody := ta.admin("POST", "/api/v1/alert-rules:test", body)
	if code != 200 {
		t.Fatalf("rule test: %d %s", code, respBody)
	}

	// a malformed rule (bad CEL) surfaces a 422.
	bad := map[string]any{"rule": map[string]any{"name": "x", "match": "("}}
	if c, b := ta.admin("POST", "/api/v1/alert-rules:test", bad); c != http.StatusUnprocessableEntity {
		t.Fatalf("rule test bad CEL: want 422, got %d %s", c, b)
	}
}

// --- contacts CRUD -----------------------------------------------------------

func TestContactsCRUD(t *testing.T) {
	ta := bootAPI(t)

	code, body := ta.admin("POST", "/api/v1/contacts", map[string]any{
		"name": "alice", "email": "alice@example.net"})
	if code != http.StatusCreated {
		t.Fatalf("create contact: %d %s", code, body)
	}

	code, body = ta.admin("GET", "/api/v1/contacts", nil)
	if code != 200 {
		t.Fatalf("list contacts: %d %s", code, body)
	}
	var list struct{ Items []map[string]any }
	mustJSON(t, body, &list)
	if len(list.Items) != 1 {
		t.Fatalf("want 1 contact, got %d: %s", len(list.Items), body)
	}

	// contact group referencing the contact.
	if c, b := ta.admin("POST", "/api/v1/contact-groups", map[string]any{
		"name": "oncall", "members": []string{"alice"}}); c != http.StatusCreated {
		t.Fatalf("create contact-group: %d %s", c, b)
	}

	// update the contact (If-Match) then delete it.
	gr := ta.raw("GET", "/api/v1/contacts/alice", nil, bearer(ta.adminToken))
	if gr.Code != 200 {
		t.Fatalf("get contact: %d %s", gr.Code, gr.Body)
	}
	ver := ta.version(gr.Body.Bytes())
	if c, b := ta.admin("PUT", "/api/v1/contacts/alice", map[string]any{
		"name": "alice", "email": "alice@corp.example"}, ifMatch(ver)); c != 200 {
		t.Fatalf("update contact: %d %s", c, b)
	}
	if c, _ := ta.admin("DELETE", "/api/v1/contacts/alice", nil); c != http.StatusNoContent {
		t.Fatalf("delete contact: %d", c)
	}
	if c, _ := ta.admin("GET", "/api/v1/contacts/alice", nil); c != http.StatusNotFound {
		t.Fatalf("get deleted contact: want 404, got %d", c)
	}
}

func TestChannelTestSend(t *testing.T) {
	ta := bootAPI(t)
	// an email channel with no real SMTP host: TestSend will fail to
	// deliver, which the handler maps to 502 np:notify/test-failed.
	if c, b := ta.admin("POST", "/api/v1/channels", map[string]any{
		"name": "smtp", "type": "email", "enabled": true,
		"config": map[string]string{"host": "127.0.0.1", "port": "1"}}); c != http.StatusCreated {
		t.Fatalf("seed channel: %d %s", c, b)
	}
	code, body := ta.admin("POST", "/api/v1/channels/smtp:test-notification",
		map[string]any{"target": "ops@example.net"})
	// either a 200 (unexpected success) or the documented 502 failure — both
	// exercise the handler; assert it is one of those, never a 5xx panic.
	if code != http.StatusOK && code != http.StatusBadGateway {
		t.Fatalf("channel test-send: unexpected %d %s", code, body)
	}
	// test-send on an unknown channel → 404.
	if c, _ := ta.admin("POST", "/api/v1/channels/ghost:test-notification", nil); c != http.StatusNotFound {
		t.Fatalf("test-send unknown channel: want 404, got %d", c)
	}
}

// --- events ------------------------------------------------------------------

func TestEventsSearch(t *testing.T) {
	ta := bootAPI(t)

	// creating an object emits a config event via objectChanged → at least
	// one event exists; the search endpoint returns it.
	ta.createHost("web01", "/")

	code, body := ta.admin("GET", "/api/v1/events", nil)
	if code != 200 {
		t.Fatalf("events search: %d %s", code, body)
	}
	var list struct{ Items []map[string]any }
	mustJSON(t, body, &list)
	if len(list.Items) == 0 {
		t.Fatalf("expected at least one (config) event after object create: %s", body)
	}

	// type filter that matches nothing returns an empty list, not an error.
	code, body = ta.admin("GET", "/api/v1/events?types=does_not_exist", nil)
	if code != 200 {
		t.Fatalf("events filter: %d %s", code, body)
	}
	mustJSON(t, body, &list)
	if len(list.Items) != 0 {
		t.Fatalf("nonexistent type filter should be empty: %s", body)
	}

	// events:read is required — the metrics-only token is forbidden.
	if c, _ := ta.do("GET", "/api/v1/events", nil, bearer(ta.noneToken)); c != http.StatusForbidden {
		t.Fatalf("outsider events: want 403, got %d", c)
	}
}

func TestEventsNDJSONExport(t *testing.T) {
	ta := bootAPI(t)
	ta.createHost("web01", "/") // emits at least one config event

	code, body := ta.admin("GET", "/api/v1/events:export", nil)
	if code != 200 {
		t.Fatalf("export: %d %s", code, body)
	}
	// NDJSON: one JSON object per line; at least one line present.
	if len(body) == 0 {
		t.Fatalf("empty NDJSON export")
	}
}

func TestSavedAlertRuleTest(t *testing.T) {
	ta := bootAPI(t)
	// store a rule, then run the saved-rule test endpoint against history.
	if c, b := ta.admin("POST", "/api/v1/alert-rules", map[string]any{
		"name": "crit", "severity": "critical",
		"match": `event.severity == "critical"`}); c != http.StatusCreated {
		t.Fatalf("seed rule: %d %s", c, b)
	}
	code, body := ta.admin("POST", "/api/v1/alert-rules/crit:test", map[string]any{
		"demoEvents": []map[string]any{
			{"id": "e1", "type": "ingress", "severity": "critical",
				"ts": time.Now().UTC().Format(time.RFC3339)}},
	})
	if code != 200 {
		t.Fatalf("saved rule test: %d %s", code, body)
	}
	// testing an unknown saved rule → 404.
	if c, _ := ta.admin("POST", "/api/v1/alert-rules/nope:test", nil); c != http.StatusNotFound {
		t.Fatalf("saved rule test unknown: want 404, got %d", c)
	}
}

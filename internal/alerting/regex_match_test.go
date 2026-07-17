package alerting

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// Requirement: event matching must cover regex and e-mail-header cases.
// The mail ingress puts subject/from/body into the NormEvent payload, so
// rules match with CEL regex over those fields — verify the environment
// actually supports it.
func TestRuleRegexAndEmailHeaderMatching(t *testing.T) {
	cases := []struct {
		name, expr string
		want       bool
	}{
		{"regex on subject", `event.payload.subject.matches('(?i)feuer|brand')`, true},
		{"regex miss", `event.payload.subject.matches('^ESCALATION-[0-9]+$')`, false},
		{"from header", `event.payload.from == 'leitstelle@example.org'`, true},
		{"body contains", `event.payload.body.contains('Zone 12')`, true},
		{"label + severity combo", `event.labels.source == 'mail-line' && event.severity == 'critical'`, true},
		{"summary regex", `event.summary.matches('FEUER.*Halle')`, true},
	}

	payload, _ := json.Marshal(&model.NormEvent{
		Source: "src-1", Severity: model.SevCritical,
		Summary: "FEUER Alarm Halle 3",
		Labels:  model.Labels{"source": "mail-line"},
		Payload: json.RawMessage(`{"subject":"FEUER Alarm Halle 3","from":"leitstelle@example.org","body":"Rauchmelder Zone 12 hat ausgelöst"}`),
	})
	// The engine view flattens the NormEvent payload: rebuild the same
	// shape EventView produces for an ingress event.
	ev := &model.Event{
		ID: "e1", TenantID: model.DefaultTenant, TS: time.Now().UTC(),
		Type: model.EventIngress, SourceID: "src-1",
		Severity: model.SevCritical, Payload: payload,
	}
	view := EventView(ev)

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cr, err := CompileRule(&model.AlertRule{Name: "r", Match: c.expr})
			if err != nil {
				t.Fatalf("compile %q: %v", c.expr, err)
			}
			got, err := cr.Matches(view)
			if err != nil {
				t.Fatalf("eval %q: %v", c.expr, err)
			}
			if got != c.want {
				t.Fatalf("%q = %v, want %v", c.expr, got, c.want)
			}
		})
	}
}

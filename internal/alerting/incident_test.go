package alerting_test

import (
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// TestAutoIncidentLifecycle: a rule with incident:true opens an incident
// per alert and resolves it once all bundled alerts resolved (F-04.05).
func TestAutoIncidentLifecycle(t *testing.T) {
	e, ctx := setup(t)
	if _, err := e.store.PutResource(ctx, model.DefaultTenant, storage.KindAlertRule, "incident-rule",
		model.AlertRule{
			Match:    `event.type == "state_change" && event.labels.env == "incprod" && event.state == "CRITICAL"`,
			Severity: model.SevCritical,
			DedupKey: "incprod/db",
			Incident: true,
		}, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.eng.ReloadAll(ctx); err != nil {
		t.Fatal(err)
	}

	e.bus.PublishEvent(stateChangeEvent("CRITICAL", "incprod"))

	var alert *model.Alert
	waitFor(t, "alert with incident", 10*time.Second, func() bool {
		alerts, err := e.store.ListAlerts(ctx, storage.AlertFilter{
			TenantID: model.DefaultTenant, Status: []model.AlertStatus{model.AlertOpen}})
		if err != nil || len(alerts) != 1 || alerts[0].IncidentID == "" {
			return false
		}
		alert = alerts[0]
		return true
	})

	inc, err := e.store.GetIncident(ctx, model.DefaultTenant, alert.IncidentID)
	if err != nil {
		t.Fatal(err)
	}
	if inc.Status != model.IncidentOpen || inc.Severity != model.SevCritical {
		t.Fatalf("incident: %+v", inc)
	}
	if len(inc.CreatedBy) < 5 || inc.CreatedBy[:5] != "rule:" {
		t.Fatalf("createdBy: %q", inc.CreatedBy)
	}

	// resolution closes alert AND its rule-created incident
	e.bus.PublishEvent(stateChangeEvent("OK", "incprod"))
	waitFor(t, "incident resolved", 10*time.Second, func() bool {
		got, err := e.store.GetIncident(ctx, model.DefaultTenant, inc.ID)
		return err == nil && got.Status == model.IncidentResolved && got.ResolvedAt != nil
	})
}

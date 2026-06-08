package alerting_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/alerting"
	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/escalation"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/notify"
	"github.com/northplane/northplane/internal/storage"
)

type env struct {
	store    *storage.Store
	bus      *eventbus.Bus
	eng      *alerting.Engine
	esc      *escalation.Engine
	mgr      *notify.Manager
	sent     *atomic.Int64
	lastBody atomic.Value
}

func setup(t *testing.T) (*env, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	bus := eventbus.New()
	cat := catalog.New(store)
	if err := cat.LoadAll(ctx); err != nil {
		t.Fatal(err)
	}

	e := &env{store: store, bus: bus, sent: &atomic.Int64{}}
	e.esc = escalation.New(store, bus, nil)
	e.eng = alerting.NewEngine(store, cat, bus, e.esc, nil)
	e.mgr = notify.New(store, bus, nil)
	e.mgr.BaseURL = "https://np.test"
	e.mgr.AckSecret = []byte("test-secret")
	e.mgr.SendHook = func(ch *model.NotificationChannel, target, subject, body string) (string, error) {
		e.sent.Add(1)
		e.lastBody.Store(body)
		return "prov-1", nil
	}

	// fixtures: contact, channel, schedule, policy, rule
	mustPut := func(kind, name string, doc any) {
		t.Helper()
		if _, err := store.PutResource(ctx, model.DefaultTenant, kind, name, doc, 0); err != nil {
			t.Fatalf("fixture %s/%s: %v", kind, name, err)
		}
	}
	contact := model.Contact{Name: "murat", Email: "murat@example.net", Phone: "+4366012345"}
	mustPut(storage.KindContact, "murat", contact)
	mustPut(storage.KindChannel, "mail", model.NotificationChannel{
		Name: "mail", Type: model.ChannelEmail, Enabled: true,
		Config: map[string]string{"host": "localhost"}})
	mustPut(storage.KindSchedule, "netz-bereitschaft", model.Schedule{
		Name: "netz-bereitschaft", TimeZone: "UTC",
		Layers: []model.Rotation{{
			Participants: []string{"murat"}, Unit: model.RotateWeekly,
			Anchor: time.Now().UTC().Add(-24 * time.Hour),
		}},
	})
	mustPut(storage.KindEscalationPolicy, "prod-infra", model.EscalationPolicy{
		Name: "prod-infra",
		Steps: []model.EscalationStep{
			{After: 0, Notify: &model.EscalationTarget{Schedule: "netz-bereitschaft"},
				Channels: []model.ChannelType{model.ChannelEmail}},
			{After: model.Duration(time.Hour), UnlessAcked: true,
				Notify: &model.EscalationTarget{Schedule: "netz-bereitschaft", EscalateTo: "backup"}},
		},
	})
	mustPut(storage.KindAlertRule, "disk-critical-prod", model.AlertRule{
		Match:            `event.type == "state_change" && event.labels.env == "prod" && event.state == "CRITICAL"`,
		Severity:         model.SevCritical,
		DedupKey:         `{{ .event.object }}/disk`,
		EscalationPolicy: "prod-infra",
	})
	if err := e.eng.ReloadAll(ctx); err != nil {
		t.Fatal(err)
	}
	go e.eng.Run(ctx)
	go e.esc.Run(ctx)
	go e.mgr.Run(ctx)
	return e, ctx
}

func stateChangeEvent(state, env string) *model.Event {
	payload, _ := json.Marshal(model.StateChangePayload{
		ObjectName: "db-prod-01", Kind: model.KindService,
		ToLabel: state, StateType: model.StateHard, Attempt: 3,
		Output: "DISK " + state, Labels: model.Labels{"env": env},
	})
	sev := model.SevCritical
	if state == "OK" {
		sev = model.SevOK
	}
	return &model.Event{ID: model.NewID(), TenantID: model.DefaultTenant,
		TS: time.Now().UTC(), Type: model.EventStateChange,
		Severity: sev, Payload: payload}
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestRuleToNotificationFlow(t *testing.T) {
	e, ctx := setup(t)

	// non-matching event (env=test) → nothing
	e.bus.PublishEvent(stateChangeEvent("CRITICAL", "test"))
	// matching event → alert + escalation step 0 → email
	e.bus.PublishEvent(stateChangeEvent("CRITICAL", "prod"))

	var alert *model.Alert
	waitFor(t, "alert opened", 10*time.Second, func() bool {
		alerts, err := e.store.ListAlerts(ctx, storage.AlertFilter{
			TenantID: model.DefaultTenant, Status: []model.AlertStatus{model.AlertOpen}})
		if err != nil || len(alerts) != 1 {
			return false
		}
		alert = alerts[0]
		return true
	})
	if alert.DedupKey != "db-prod-01/disk" || alert.Severity != model.SevCritical {
		t.Fatalf("alert: %+v", alert)
	}

	waitFor(t, "notification sent", 15*time.Second, func() bool {
		return e.sent.Load() == 1
	})
	body, _ := e.lastBody.Load().(string)
	if body == "" || !contains(body, "/api/v1/ack/") {
		t.Fatalf("body must carry ack link: %q", body)
	}

	// duplicate event refreshes, does not duplicate
	e.bus.PublishEvent(stateChangeEvent("CRITICAL", "prod"))
	time.Sleep(500 * time.Millisecond)
	alerts, _ := e.store.ListAlerts(ctx, storage.AlertFilter{
		TenantID: model.DefaultTenant, Status: []model.AlertStatus{model.AlertOpen}})
	if len(alerts) != 1 {
		t.Fatalf("dedup failed: %d alerts", len(alerts))
	}

	// OK event resolves and stops the chain
	e.bus.PublishEvent(stateChangeEvent("OK", "prod"))
	waitFor(t, "alert resolved", 10*time.Second, func() bool {
		a, err := e.store.GetAlert(ctx, model.DefaultTenant, alert.ID)
		return err == nil && a.Status == model.AlertResolved
	})
	due, _ := e.store.DueEscalations(ctx, time.Now().Add(2*time.Hour), 10)
	if len(due) != 0 {
		t.Fatalf("chain not cancelled: %+v", due)
	}
}

func TestAckLinkRoundtrip(t *testing.T) {
	secret := []byte("s3cret")
	tok := notify.AckToken(secret, "alert-1", "contact-1", time.Hour)
	a, c, err := notify.VerifyAckToken(secret, tok)
	if err != nil || a != "alert-1" || c != "contact-1" {
		t.Fatalf("roundtrip: %v %s %s", err, a, c)
	}
	if _, _, err := notify.VerifyAckToken([]byte("wrong"), tok); err == nil {
		t.Fatal("wrong secret must fail")
	}
	expired := notify.AckToken(secret, "a", "c", -time.Minute)
	if _, _, err := notify.VerifyAckToken(secret, expired); err == nil {
		t.Fatal("expired must fail")
	}
}

func TestPendingFor(t *testing.T) {
	e, ctx := setup(t)
	// add a pending rule
	if _, err := e.store.PutResource(ctx, model.DefaultTenant, storage.KindAlertRule, "pending-rule",
		model.AlertRule{
			Match:      `event.type == "state_change" && event.labels.env == "staging"`,
			Severity:   model.SevWarning,
			DedupKey:   "staging-pending",
			PendingFor: model.Duration(2 * time.Second),
		}, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.eng.ReloadAll(ctx); err != nil {
		t.Fatal(err)
	}

	e.bus.PublishEvent(stateChangeEvent("CRITICAL", "staging"))
	time.Sleep(700 * time.Millisecond)
	alerts, _ := e.store.ListAlerts(ctx, storage.AlertFilter{
		TenantID: model.DefaultTenant, Status: []model.AlertStatus{model.AlertOpen}})
	if len(alerts) != 0 {
		t.Fatalf("alert opened before pendingFor elapsed")
	}
	waitFor(t, "pending alert fires", 15*time.Second, func() bool {
		alerts, _ := e.store.ListAlerts(ctx, storage.AlertFilter{
			TenantID: model.DefaultTenant, Status: []model.AlertStatus{model.AlertOpen}})
		return len(alerts) == 1 && alerts[0].DedupKey == "staging-pending"
	})
}

func TestRuleTestEndpointLogic(t *testing.T) {
	e, ctx := setup(t)
	rule := &model.AlertRule{Name: "t", Match: `event.state == "CRITICAL"`, Severity: model.SevCritical}
	demo := []*model.Event{stateChangeEvent("CRITICAL", "prod"), stateChangeEvent("OK", "prod")}
	res, err := e.eng.TestRule(ctx, model.DefaultTenant, rule, demo, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched != 1 || len(res.WouldOpen) != 1 {
		t.Fatalf("test result: %+v", res)
	}
}

func TestSilenceSuppressesChain(t *testing.T) {
	e, ctx := setup(t)
	// silence everything env=prod before the event
	if err := e.store.CreateSilence(ctx, &model.Silence{
		TenantID: model.DefaultTenant, Selector: "env=prod",
		Comment: "maintenance", CreatedBy: "test",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	e.bus.PublishEvent(stateChangeEvent("CRITICAL", "prod"))

	waitFor(t, "alert opened", 10*time.Second, func() bool {
		alerts, _ := e.store.ListAlerts(ctx, storage.AlertFilter{
			TenantID: model.DefaultTenant, Status: []model.AlertStatus{model.AlertOpen}})
		return len(alerts) == 1
	})
	// give escalation a moment — it must NOT fire
	time.Sleep(2 * time.Second)
	if e.sent.Load() != 0 {
		t.Fatalf("silenced alert must not notify, sent=%d", e.sent.Load())
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

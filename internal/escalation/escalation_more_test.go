package escalation

import (
	"context"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// --- resolveTargets: schedule (on-call) branch + contactByRef fallback -----

func TestFireRoutesToScheduleOnCall(t *testing.T) {
	eng, st, ctx := testEngine(t)
	// Contact stored under name "carol"; the override references it by that
	// same ref, so resolveTargets loads it (LoadOne resolves name-or-id) and,
	// if that miss, falls back through contactByRef.
	seedContact(t, st, ctx, "carol")

	now := time.Now().UTC()
	sched := &model.Schedule{Name: "primary", TimeZone: "UTC",
		Layers: []model.Rotation{{
			Name: "L1", Participants: []string{"carol"}, Unit: model.RotateDaily,
			Anchor: now.Add(-24 * time.Hour)}}}
	if _, err := st.PutResource(ctx, tenant, storage.KindSchedule, "primary", sched, 0); err != nil {
		t.Fatalf("put schedule: %v", err)
	}
	// An override makes on-call resolution deterministic regardless of the
	// rotation math: carol is explicitly on duty for the window covering now.
	ov := &model.Override{ScheduleID: "primary", ContactID: "carol",
		Start: now.Add(-time.Hour), End: now.Add(time.Hour)}
	if _, err := st.PutResource(ctx, tenant, storage.KindOverride, "ov1", ov, 0); err != nil {
		t.Fatalf("put override: %v", err)
	}

	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{
		{After: 0, Notify: &model.EscalationTarget{Schedule: "primary"}},
	})
	alert := seedAlert(t, st, ctx, now.Add(-time.Minute))

	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 0})
	jobs := notifications(t, st, ctx)
	if len(jobs) != 1 {
		t.Fatalf("schedule target should page the on-call contact: %+v", jobs)
	}
}

func TestFireScheduleMissingResolvesNobody(t *testing.T) {
	eng, st, ctx := testEngine(t)
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{
		{After: 0, Notify: &model.EscalationTarget{Schedule: "ghost-schedule"}},
	})
	alert := seedAlert(t, st, ctx, time.Now().UTC().Add(-time.Minute))

	// Missing schedule → no contacts → audit only, no notifications, but the
	// step is still processed (chain advances / completes without panic).
	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 0})
	if jobs := notifications(t, st, ctx); len(jobs) != 0 {
		t.Fatalf("missing schedule must not page: %+v", jobs)
	}
}

// --- notifyStep: action-only step and unresolved-contact step --------------

func TestFireActionOnlyStepEnqueuesAction(t *testing.T) {
	eng, st, ctx := testEngine(t)
	// Step with an Action and no Notify target: enqueues an "action" outbox
	// item and audits, but sends no notifications.
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{
		{After: 0, Action: &model.EscalationAction{Webhook: "pager-webhook"}},
	})
	alert := seedAlert(t, st, ctx, time.Now().UTC().Add(-time.Minute))

	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 0})

	if jobs := notifications(t, st, ctx); len(jobs) != 0 {
		t.Fatalf("action-only step must not page contacts: %+v", jobs)
	}
	items, err := st.DueOutbox(ctx, time.Now().Add(24*time.Hour), 100)
	if err != nil {
		t.Fatalf("due outbox: %v", err)
	}
	var actions int
	for _, it := range items {
		if it.Kind == "action" {
			actions++
		}
	}
	if actions != 1 {
		t.Fatalf("expected 1 action outbox item, got %d", actions)
	}
}

func TestFireStepWithUnresolvableContactAuditsOnly(t *testing.T) {
	eng, st, ctx := testEngine(t)
	// Notify a contact that does not exist → no resolvable contacts → audit
	// only, no notifications, chain still completes.
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{contactStep(0, "nobody")})
	alert := seedAlert(t, st, ctx, time.Now().UTC().Add(-time.Minute))

	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 0})
	if jobs := notifications(t, st, ctx); len(jobs) != 0 {
		t.Fatalf("unresolvable contact must not page: %+v", jobs)
	}
}

// --- notifyStep: per-contact channel preference via periodLookup -----------

func TestFireUsesContactPreferredChannels(t *testing.T) {
	eng, st, ctx := testEngine(t)
	// Contact whose default preference is SMS; the step sets no channels, so
	// notifyStep falls back to PreferredChannels (exercising periodLookup).
	c := &model.Contact{Name: "dave",
		Preferences: []model.ChannelPreference{
			{Profile: "default", Channels: []model.ChannelType{model.ChannelSMS}},
		}}
	if _, err := st.PutResource(ctx, tenant, storage.KindContact, "dave", c, 0); err != nil {
		t.Fatalf("put contact: %v", err)
	}
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{contactStep(0, "dave")})
	alert := seedAlert(t, st, ctx, time.Now().UTC().Add(-time.Minute))

	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 0})
	jobs := notifications(t, st, ctx)
	if len(jobs) != 1 || jobs[0].Channel != model.ChannelSMS {
		t.Fatalf("expected SMS from contact preference: %+v", jobs)
	}
}

func TestFireDefaultsToEmailWhenNoPreference(t *testing.T) {
	eng, st, ctx := testEngine(t)
	// Contact with no preferences at all and step with no channels → the
	// final fallback is email.
	c := &model.Contact{Name: "erin"}
	if _, err := st.PutResource(ctx, tenant, storage.KindContact, "erin", c, 0); err != nil {
		t.Fatalf("put contact: %v", err)
	}
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{contactStep(0, "erin")})
	alert := seedAlert(t, st, ctx, time.Now().UTC().Add(-time.Minute))

	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 0})
	jobs := notifications(t, st, ctx)
	if len(jobs) != 1 || jobs[0].Channel != model.ChannelEmail {
		t.Fatalf("expected email fallback: %+v", jobs)
	}
}

// --- Run poll loop ---------------------------------------------------------

// TestRunPollFiresDueTimer starts the poll loop and confirms it picks up a
// due timer and pages. The production ticker is 2s; we wait on the outbox
// with a bounded poll (no fixed long Sleep) and cancel as soon as we observe
// the effect, keeping the test fast and deterministic.
func TestRunPollFiresDueTimer(t *testing.T) {
	eng, st, ctx := testEngine(t)
	seedContact(t, st, ctx, "alice")
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{contactStep(0, "alice")})
	alert := seedAlert(t, st, ctx, time.Now().UTC().Add(-time.Minute))

	at := time.Now().UTC().Add(-time.Second) // already due
	if err := st.ScheduleEscalation(ctx, storage.EscalationTimer{
		AlertID: alert.ID, PolicyName: "pol", StepIndex: 0, NextAt: &at}); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { eng.Run(runCtx); close(done) }()

	// Poll for the notification effect for up to 6s (≥2 ticker periods).
	deadline := time.Now().Add(6 * time.Second)
	var paged bool
	for time.Now().Before(deadline) {
		if len(notifications(t, st, ctx)) > 0 {
			paged = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if !paged {
		t.Fatal("Run poll loop did not fire the due timer")
	}
}

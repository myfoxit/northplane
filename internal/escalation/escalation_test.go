package escalation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// testEngine spins up a real SQLite-backed store (the production code holds a
// concrete *storage.Store, not an interface, so a fake store is not an
// option) plus a fresh bus. The store is the cleanest seam for deterministic
// tests: alert OpenedAt and timer NextAt are controllable, and the
// time-sensitive selection happens in DueEscalations(now) which takes an
// explicit clock argument. We therefore never sleep — see TestStepTimingDue.
func testEngine(t *testing.T) (*Engine, *storage.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir(), RetentionMonths: 12})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, eventbus.New(), nil), st, ctx
}

const tenant = model.DefaultTenant

// seedContact stores a contact and returns its name (also usable as a ref).
func seedContact(t *testing.T, st *storage.Store, ctx context.Context, name string) {
	t.Helper()
	c := &model.Contact{Name: name, Email: name + "@example.com",
		Preferences: []model.ChannelPreference{{Profile: "default", Channels: []model.ChannelType{model.ChannelEmail}}}}
	if _, err := st.PutResource(ctx, tenant, storage.KindContact, name, c, 0); err != nil {
		t.Fatalf("put contact %q: %v", name, err)
	}
}

func seedPolicy(t *testing.T, st *storage.Store, ctx context.Context, name string, steps []model.EscalationStep) {
	t.Helper()
	p := &model.EscalationPolicy{Name: name, Steps: steps}
	if _, err := st.PutResource(ctx, tenant, storage.KindEscalationPolicy, name, p, 0); err != nil {
		t.Fatalf("put policy %q: %v", name, err)
	}
}

// seedAlert opens an alert with a controlled OpenedAt (so step timing is
// deterministic without waiting).
func seedAlert(t *testing.T, st *storage.Store, ctx context.Context, openedAt time.Time) *model.Alert {
	t.Helper()
	a := &model.Alert{TenantID: tenant, Title: "boom", Severity: model.SevCritical,
		ObjectID: "obj-1", OpenedAt: openedAt, DedupKey: model.NewID()}
	stored, _, err := st.UpsertAlert(ctx, a)
	if err != nil {
		t.Fatalf("upsert alert: %v", err)
	}
	return stored
}

// dueTimers returns the non-done escalation timers whose NextAt <= now.
// Passing a far-future "now" yields every still-armed timer (used to assert
// which step is armed next); passing a precise "now" asserts threshold timing.
func dueTimers(t *testing.T, st *storage.Store, ctx context.Context, now time.Time) []storage.EscalationTimer {
	t.Helper()
	timers, err := st.DueEscalations(ctx, now, 100)
	if err != nil {
		t.Fatalf("due escalations: %v", err)
	}
	return timers
}

// notifications decodes outbox notification jobs (kind=="notification") for
// the tenant. A far-future cutoff returns every enqueued item.
func notifications(t *testing.T, st *storage.Store, ctx context.Context) []notifyJob {
	t.Helper()
	items, err := st.DueOutbox(ctx, time.Now().Add(24*time.Hour), 1000)
	if err != nil {
		t.Fatalf("due outbox: %v", err)
	}
	var jobs []notifyJob
	for _, it := range items {
		if it.Kind != "notification" {
			continue
		}
		var j notifyJob
		if err := json.Unmarshal(it.Payload, &j); err != nil {
			t.Fatalf("decode notify job: %v", err)
		}
		jobs = append(jobs, j)
	}
	return jobs
}

func contactStep(after time.Duration, contact string, opts ...func(*model.EscalationStep)) model.EscalationStep {
	s := model.EscalationStep{After: model.Duration(after), Notify: &model.EscalationTarget{Contact: contact}}
	for _, o := range opts {
		o(&s)
	}
	return s
}

// --- StartChain ------------------------------------------------------------

func TestStartChainArmsStepZero(t *testing.T) {
	eng, st, ctx := testEngine(t)
	seedContact(t, st, ctx, "alice")
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{
		contactStep(5*time.Minute, "alice"),
	})
	opened := time.Now().UTC().Add(-time.Minute)
	alert := seedAlert(t, st, ctx, opened)

	if err := eng.StartChain(ctx, alert, "pol"); err != nil {
		t.Fatalf("StartChain: %v", err)
	}
	// Step 0 should be armed for OpenedAt+5m → not yet due now, but due at
	// opened+6m.
	if got := dueTimers(t, st, ctx, opened.Add(4*time.Minute)); len(got) != 0 {
		t.Fatalf("step 0 due too early: %v", got)
	}
	got := dueTimers(t, st, ctx, opened.Add(6*time.Minute))
	if len(got) != 1 || got[0].StepIndex != 0 {
		t.Fatalf("step 0 not armed at +6m: %+v", got)
	}
}

func TestStartChainEmptyPolicyIsNoop(t *testing.T) {
	eng, st, ctx := testEngine(t)
	seedPolicy(t, st, ctx, "empty", nil)
	alert := seedAlert(t, st, ctx, time.Now().UTC())

	if err := eng.StartChain(ctx, alert, "empty"); err != nil {
		t.Fatalf("StartChain empty: %v", err)
	}
	if got := dueTimers(t, st, ctx, time.Now().Add(time.Hour)); len(got) != 0 {
		t.Fatalf("empty policy armed a timer: %v", got)
	}
}

func TestStartChainMissingPolicyErrors(t *testing.T) {
	eng, st, ctx := testEngine(t)
	alert := seedAlert(t, st, ctx, time.Now().UTC())
	if err := eng.StartChain(ctx, alert, "ghost"); err == nil {
		t.Fatal("expected error for missing policy")
	}
}

// --- fire: routing / stepping ---------------------------------------------

func TestFireRoutesToContactAndArmsNextStep(t *testing.T) {
	eng, st, ctx := testEngine(t)
	seedContact(t, st, ctx, "alice")
	seedContact(t, st, ctx, "bob")
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{
		contactStep(0, "alice"),
		contactStep(10*time.Minute, "bob"),
	})
	opened := time.Now().UTC().Add(-time.Minute)
	alert := seedAlert(t, st, ctx, opened)

	// Fire step 0.
	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 0})

	jobs := notifications(t, st, ctx)
	if len(jobs) != 1 || jobs[0].ContactID == "" || jobs[0].StepIndex != 0 {
		t.Fatalf("step 0 should page exactly alice: %+v", jobs)
	}
	if jobs[0].Channel != model.ChannelEmail {
		t.Fatalf("channel = %v, want email (contact pref)", jobs[0].Channel)
	}

	// Step 0 is marked done; step 1 is armed for OpenedAt+10m.
	if got := dueTimers(t, st, ctx, opened.Add(11*time.Minute)); len(got) != 1 || got[0].StepIndex != 1 {
		t.Fatalf("step 1 not armed: %+v", got)
	}
	// Step 1 not yet due before its threshold.
	if got := dueTimers(t, st, ctx, opened.Add(9*time.Minute)); len(got) != 0 {
		t.Fatalf("step 1 due too early: %+v", got)
	}

	// Fire step 1 → pages bob, no further step armed (chain exhausted).
	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 1})
	jobs = notifications(t, st, ctx)
	if len(jobs) != 2 {
		t.Fatalf("expected 2 total notifications after step1, got %d", len(jobs))
	}
	if got := dueTimers(t, st, ctx, opened.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("no further step should be armed after the last step: %+v", got)
	}
}

func TestFireRoutesToContactGroup(t *testing.T) {
	eng, st, ctx := testEngine(t)
	seedContact(t, st, ctx, "alice")
	seedContact(t, st, ctx, "bob")
	// Resolve group members by ref (name). resolveTargets loads each member.
	group := &model.ContactGroup{Name: "oncall", Members: []string{"alice", "bob"}}
	if _, err := st.PutResource(ctx, tenant, storage.KindContactGroup, "oncall", group, 0); err != nil {
		t.Fatalf("put group: %v", err)
	}
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{
		{After: 0, Notify: &model.EscalationTarget{ContactGroup: "oncall"}},
	})
	alert := seedAlert(t, st, ctx, time.Now().UTC().Add(-time.Minute))

	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 0})
	jobs := notifications(t, st, ctx)
	if len(jobs) != 2 {
		t.Fatalf("contact group should page both members, got %d: %+v", len(jobs), jobs)
	}
}

func TestFireChannelOverride(t *testing.T) {
	eng, st, ctx := testEngine(t)
	seedContact(t, st, ctx, "alice")
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{
		contactStep(0, "alice", func(s *model.EscalationStep) {
			s.Channels = []model.ChannelType{model.ChannelSMS}
		}),
	})
	alert := seedAlert(t, st, ctx, time.Now().UTC().Add(-time.Minute))

	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 0})
	jobs := notifications(t, st, ctx)
	if len(jobs) != 1 || jobs[0].Channel != model.ChannelSMS {
		t.Fatalf("step channel override ignored: %+v", jobs)
	}
}

// --- fire: ack / resolve stop the chain -----------------------------------

func TestFireAckStopsChainAndSkipsUnlessAcked(t *testing.T) {
	eng, st, ctx := testEngine(t)
	seedContact(t, st, ctx, "alice")
	seedContact(t, st, ctx, "bob")
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{
		contactStep(0, "alice", func(s *model.EscalationStep) { s.UnlessAcked = true }),
		contactStep(10*time.Minute, "bob"),
	})
	opened := time.Now().UTC().Add(-time.Minute)
	alert := seedAlert(t, st, ctx, opened)

	// Acknowledge before the step fires.
	if _, err := st.AckAlert(ctx, tenant, alert.ID, "operator"); err != nil {
		t.Fatalf("ack: %v", err)
	}

	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 0})

	// UnlessAcked + acked → no notification sent for this step.
	if jobs := notifications(t, st, ctx); len(jobs) != 0 {
		t.Fatalf("acked UnlessAcked step still paged: %+v", jobs)
	}
	// Ack stops the chain: next step is never armed.
	if got := dueTimers(t, st, ctx, opened.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("ack should stop chain, but a timer is armed: %+v", got)
	}
}

func TestFireAckStillNotifiesWhenNotUnlessAcked(t *testing.T) {
	eng, st, ctx := testEngine(t)
	seedContact(t, st, ctx, "alice")
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{
		contactStep(0, "alice"),              // UnlessAcked = false
		contactStep(10*time.Minute, "alice"), // would-be next step
	})
	opened := time.Now().UTC().Add(-time.Minute)
	alert := seedAlert(t, st, ctx, opened)
	if _, err := st.AckAlert(ctx, tenant, alert.ID, "op"); err != nil {
		t.Fatalf("ack: %v", err)
	}

	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 0})

	// This already-due step still sends (UnlessAcked false)...
	if jobs := notifications(t, st, ctx); len(jobs) != 1 {
		t.Fatalf("acked step without UnlessAcked should still send once: %+v", jobs)
	}
	// ...but the chain stops: no next step armed once acked.
	if got := dueTimers(t, st, ctx, opened.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("ack should stop chain after a single send: %+v", got)
	}
}

func TestFireResolvedAlertIsSilentlyDropped(t *testing.T) {
	eng, st, ctx := testEngine(t)
	seedContact(t, st, ctx, "alice")
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{contactStep(0, "alice")})
	alert := seedAlert(t, st, ctx, time.Now().UTC().Add(-time.Minute))
	if _, err := st.ResolveAlert(ctx, tenant, alert.ID, model.AlertResolved); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 0})
	if jobs := notifications(t, st, ctx); len(jobs) != 0 {
		t.Fatalf("resolved alert must not page: %+v", jobs)
	}
	if got := dueTimers(t, st, ctx, time.Now().Add(time.Hour)); len(got) != 0 {
		t.Fatalf("resolved alert must not arm timers: %+v", got)
	}
}

func TestFireMissingAlertMarksDone(t *testing.T) {
	eng, st, ctx := testEngine(t)
	seedContact(t, st, ctx, "alice")
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{contactStep(0, "alice")})
	// Arm a timer for an alert id that does not exist.
	at := time.Now().UTC().Add(-time.Second)
	if err := st.ScheduleEscalation(ctx, storage.EscalationTimer{
		AlertID: "ghost-alert", PolicyName: "pol", StepIndex: 0, NextAt: &at}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	eng.fire(ctx, storage.EscalationTimer{AlertID: "ghost-alert", PolicyName: "pol", StepIndex: 0})

	if jobs := notifications(t, st, ctx); len(jobs) != 0 {
		t.Fatalf("missing alert must not page: %+v", jobs)
	}
	if got := dueTimers(t, st, ctx, time.Now().Add(time.Hour)); len(got) != 0 {
		t.Fatalf("missing alert timer should be marked done: %+v", got)
	}
}

// --- fire: exhausted steps -------------------------------------------------

func TestFireStepIndexBeyondPolicyMarksDone(t *testing.T) {
	eng, st, ctx := testEngine(t)
	seedContact(t, st, ctx, "alice")
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{contactStep(0, "alice")})
	alert := seedAlert(t, st, ctx, time.Now().UTC().Add(-time.Minute))

	// StepIndex 5 is out of range for a single-step policy.
	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 5})
	if jobs := notifications(t, st, ctx); len(jobs) != 0 {
		t.Fatalf("out-of-range step must not page: %+v", jobs)
	}
}

func TestFireSingleStepDoesNotArmNext(t *testing.T) {
	eng, st, ctx := testEngine(t)
	seedContact(t, st, ctx, "alice")
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{contactStep(0, "alice")})
	alert := seedAlert(t, st, ctx, time.Now().UTC().Add(-time.Minute))

	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 0})
	if jobs := notifications(t, st, ctx); len(jobs) != 1 {
		t.Fatalf("single step should page once: %+v", jobs)
	}
	if got := dueTimers(t, st, ctx, time.Now().Add(time.Hour)); len(got) != 0 {
		t.Fatalf("single step must not arm a next step: %+v", got)
	}
}

// --- fire: repeats ---------------------------------------------------------

func TestFireRepeatSchedulesNextRepeat(t *testing.T) {
	eng, st, ctx := testEngine(t)
	seedContact(t, st, ctx, "alice")
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{
		contactStep(0, "alice", func(s *model.EscalationStep) {
			s.RepeatEvery = model.Duration(5 * time.Minute)
			s.MaxRepeats = 2
		}),
	})
	alert := seedAlert(t, st, ctx, time.Now().UTC().Add(-time.Minute))

	// First fire (RepeatsDone == 0): pages and schedules repeat #1.
	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 0, RepeatsDone: 0})
	if jobs := notifications(t, st, ctx); len(jobs) != 1 {
		t.Fatalf("first repeat fire should page once: %+v", jobs)
	}
	got := dueTimers(t, st, ctx, time.Now().Add(time.Hour))
	if len(got) != 1 || got[0].StepIndex != 0 || got[0].RepeatsDone != 1 {
		t.Fatalf("repeat #1 not scheduled: %+v", got)
	}

	// Fire repeat #1 (RepeatsDone == 1): pages and schedules repeat #2 (== MaxRepeats).
	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 0, RepeatsDone: 1})
	if jobs := notifications(t, st, ctx); len(jobs) != 2 {
		t.Fatalf("second repeat fire should page again: %+v", jobs)
	}
	got = dueTimers(t, st, ctx, time.Now().Add(time.Hour))
	if len(got) != 1 || got[0].RepeatsDone != 2 {
		t.Fatalf("repeat #2 not scheduled: %+v", got)
	}

	// Fire repeat #2 (RepeatsDone == 2 == MaxRepeats): pages, then stops
	// (RepeatsDone is not < MaxRepeats), so the timer is marked done.
	eng.fire(ctx, storage.EscalationTimer{AlertID: alert.ID, PolicyName: "pol", StepIndex: 0, RepeatsDone: 2})
	if jobs := notifications(t, st, ctx); len(jobs) != 3 {
		t.Fatalf("third fire should page: %+v", jobs)
	}
	if got := dueTimers(t, st, ctx, time.Now().Add(time.Hour)); len(got) != 0 {
		t.Fatalf("repeats exhausted, no further timer expected: %+v", got)
	}
}

// --- StopChain -------------------------------------------------------------

func TestStopChainCancelsPendingTimers(t *testing.T) {
	eng, st, ctx := testEngine(t)
	seedContact(t, st, ctx, "alice")
	seedPolicy(t, st, ctx, "pol", []model.EscalationStep{
		contactStep(0, "alice"),
		contactStep(10*time.Minute, "alice"),
	})
	opened := time.Now().UTC().Add(-time.Minute)
	alert := seedAlert(t, st, ctx, opened)

	if err := eng.StartChain(ctx, alert, "pol"); err != nil {
		t.Fatalf("StartChain: %v", err)
	}
	// A timer exists.
	if got := dueTimers(t, st, ctx, opened.Add(time.Hour)); len(got) == 0 {
		t.Fatal("expected an armed timer before StopChain")
	}
	if err := eng.StopChain(ctx, alert.ID); err != nil {
		t.Fatalf("StopChain: %v", err)
	}
	if got := dueTimers(t, st, ctx, opened.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("StopChain should cancel all timers: %+v", got)
	}
}

// --- timing/threshold-to-advance is data-driven via NextAt ----------------

// TestStepTimingDue documents why no real clock is needed: DueEscalations
// takes an explicit `now`, so advancing "time" is just choosing the argument.
func TestStepTimingDue(t *testing.T) {
	_, st, ctx := testEngine(t)
	at := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := st.ScheduleEscalation(ctx, storage.EscalationTimer{
		AlertID: "a", PolicyName: "p", StepIndex: 0, NextAt: &at}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	tests := []struct {
		name string
		now  time.Time
		due  bool
	}{
		{"before threshold", at.Add(-time.Second), false},
		{"exactly at threshold", at, true},
		{"after threshold", at.Add(time.Second), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := dueTimers(t, st, ctx, tc.now)
			if (len(got) > 0) != tc.due {
				t.Fatalf("due=%v at now=%v, want %v", len(got) > 0, tc.now, tc.due)
			}
		})
	}
}

// Package escalation runs multi-step notification chains (SPEC §9.4):
// durable timers (table `escalations`) survive restarts, steps fire
// unlessAcked, repeats are bounded, acknowledgement stops the chain, and
// every step leaves an auditable notification event trail (F-05.03/09).
package escalation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// Engine schedules and fires steps.
type Engine struct {
	store *storage.Store
	bus   *eventbus.Bus
	log   *slog.Logger
}

// New builds the engine.
func New(store *storage.Store, bus *eventbus.Bus, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{store: store, bus: bus, log: log}
}

// StartChain arms step 0 of the policy for a fresh alert.
func (e *Engine) StartChain(ctx context.Context, alert *model.Alert, policyName string) error {
	policy, err := storage.LoadOne[model.EscalationPolicy](ctx, e.store, alert.TenantID,
		storage.KindEscalationPolicy, policyName)
	if err != nil {
		return fmt.Errorf("escalation policy %q: %w", policyName, err)
	}
	if len(policy.Steps) == 0 {
		return nil
	}
	at := alert.OpenedAt.Add(policy.Steps[0].After.D())
	return e.store.ScheduleEscalation(ctx, storage.EscalationTimer{
		AlertID: alert.ID, PolicyName: policyName, StepIndex: 0, NextAt: &at,
	})
}

// StopChain cancels all pending steps (ack/resolve — SPEC §9.4).
func (e *Engine) StopChain(ctx context.Context, alertID string) error {
	return e.store.CancelEscalations(ctx, alertID)
}

// Run polls due timers until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			due, err := e.store.DueEscalations(ctx, time.Now().UTC(), 200)
			if err != nil {
				e.log.Error("escalation: poll", "err", err)
				continue
			}
			for _, timer := range due {
				e.fire(ctx, timer)
			}
		}
	}
}

func (e *Engine) fire(ctx context.Context, timer storage.EscalationTimer) {
	markDone := func() {
		_ = e.store.ScheduleEscalation(ctx, storage.EscalationTimer{
			AlertID: timer.AlertID, PolicyName: timer.PolicyName,
			StepIndex: timer.StepIndex, RepeatsDone: timer.RepeatsDone, Done: true,
		})
	}

	// alert lookup needs the tenant — escalations are global, alerts are
	// tenant-scoped; resolve via direct query through all tenants is
	// wasteful, so alerts carry globally unique IDs: fetch via scan.
	alert, err := e.findAlert(ctx, timer.AlertID)
	if err != nil || alert == nil {
		markDone()
		return
	}
	if alert.Status == model.AlertResolved || alert.Status == model.AlertExpired {
		markDone()
		return
	}
	policy, err := storage.LoadOne[model.EscalationPolicy](ctx, e.store, alert.TenantID,
		storage.KindEscalationPolicy, timer.PolicyName)
	if err != nil || timer.StepIndex >= len(policy.Steps) {
		markDone()
		return
	}
	step := policy.Steps[timer.StepIndex]

	// unlessAcked: skip work but continue the schedule when acked.
	skipped := step.UnlessAcked && alert.Status == model.AlertAcked
	if !skipped {
		e.notifyStep(ctx, alert, policy, timer.StepIndex, step, timer.RepeatsDone)
	}

	// arm next step once (when this step fired its first time)
	if timer.RepeatsDone == 0 && timer.StepIndex+1 < len(policy.Steps) {
		next := policy.Steps[timer.StepIndex+1]
		at := alert.OpenedAt.Add(next.After.D())
		if at.Before(time.Now().UTC()) {
			at = time.Now().UTC().Add(5 * time.Second)
		}
		_ = e.store.ScheduleEscalation(ctx, storage.EscalationTimer{
			AlertID: alert.ID, PolicyName: timer.PolicyName,
			StepIndex: timer.StepIndex + 1, NextAt: &at,
		})
	}

	// repeats (repeatEvery / maxRepeats)
	if step.RepeatEvery > 0 && timer.RepeatsDone < step.MaxRepeats {
		at := time.Now().UTC().Add(step.RepeatEvery.D())
		_ = e.store.ScheduleEscalation(ctx, storage.EscalationTimer{
			AlertID: alert.ID, PolicyName: timer.PolicyName,
			StepIndex: timer.StepIndex, RepeatsDone: timer.RepeatsDone + 1, NextAt: &at,
		})
		return
	}
	markDone()
}

// findAlert scans tenants for the alert (IDs are UUIDv7-unique).
func (e *Engine) findAlert(ctx context.Context, alertID string) (*model.Alert, error) {
	tenants, err := e.store.Tenants(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range tenants {
		if a, err := e.store.GetAlert(ctx, t.ID, alertID); err == nil {
			return a, nil
		}
	}
	return nil, storage.ErrNotFound
}

// notifyStep resolves targets and enqueues deliveries to the outbox.
func (e *Engine) notifyStep(ctx context.Context, alert *model.Alert,
	policy *model.EscalationPolicy, stepIndex int, step model.EscalationStep, repeat int) {

	// external actions (ServiceNow/webhook) ride the outbox too
	if step.Action != nil {
		payload, _ := json.Marshal(map[string]any{
			"action": step.Action, "alert": alert, "policy": policy.Name, "step": stepIndex})
		_ = e.store.EnqueueOutbox(ctx, &storage.OutboxItem{
			TenantID: alert.TenantID, Kind: "action", Payload: payload})
	}
	if step.Notify == nil {
		e.auditStep(ctx, alert, stepIndex, repeat, nil, step.Channels)
		return
	}

	contacts := e.resolveTargets(ctx, alert.TenantID, step.Notify)
	if len(contacts) == 0 {
		e.log.Warn("escalation: step has no resolvable contacts",
			"alert", alert.ID, "policy", policy.Name, "step", stepIndex)
		e.auditStep(ctx, alert, stepIndex, repeat, nil, step.Channels)
		return
	}

	for _, contact := range contacts {
		channels := step.Channels
		if len(channels) == 0 {
			channels = preferredChannels(contact, alert.Severity, time.Now())
		}
		if len(channels) == 0 {
			channels = []model.ChannelType{model.ChannelEmail}
		}
		for _, ch := range channels {
			payload, _ := json.Marshal(notifyJob{
				AlertID: alert.ID, TenantID: alert.TenantID,
				ContactID: contact.ID, Channel: ch,
				StepIndex: stepIndex, Repeat: repeat, Policy: policy.Name,
			})
			item := &storage.OutboxItem{TenantID: alert.TenantID,
				Kind: "notification", Payload: payload}
			if err := e.store.EnqueueOutbox(ctx, item); err != nil {
				e.log.Error("escalation: enqueue", "err", err)
				continue
			}
			select { // wake the notifier
			case e.bus.Notifications <- item.ID:
			default:
			}
		}
	}
	e.auditStep(ctx, alert, stepIndex, repeat, contacts, step.Channels)
}

// notifyJob is the outbox payload consumed by the notifier.
type notifyJob struct {
	AlertID   string            `json:"alertId"`
	TenantID  string            `json:"tenantId"`
	ContactID string            `json:"contactId"`
	Channel   model.ChannelType `json:"channel"`
	StepIndex int               `json:"stepIndex"`
	Repeat    int               `json:"repeat"`
	Policy    string            `json:"policy"`
}

func (e *Engine) auditStep(ctx context.Context, alert *model.Alert, step, repeat int,
	contacts []*model.Contact, channels []model.ChannelType) {
	names := make([]string, 0, len(contacts))
	for _, c := range contacts {
		names = append(names, c.Name)
	}
	raw, _ := json.Marshal(map[string]any{
		"alertId": alert.ID, "step": step, "repeat": repeat,
		"contacts": names, "channels": channels})
	ev := &model.Event{ID: model.NewID(), TenantID: alert.TenantID,
		TS: time.Now().UTC(), Type: model.EventEscalation,
		ObjectID: alert.ObjectID, Severity: model.SevInfo, Payload: raw}
	_ = e.store.InsertEvents(ctx, []*model.Event{ev})
	e.bus.FanoutOnly(ev)
}

// resolveTargets expands a step target into contacts (SPEC §9.4):
// schedule (current on-call, optionally the backup), single contact or
// contact group.
func (e *Engine) resolveTargets(ctx context.Context, tenantID string,
	target *model.EscalationTarget) []*model.Contact {
	var ids []string
	switch {
	case target.Schedule != "":
		sched, err := storage.LoadOne[model.Schedule](ctx, e.store, tenantID,
			storage.KindSchedule, target.Schedule)
		if err != nil {
			e.log.Warn("escalation: schedule missing", "schedule", target.Schedule)
			return nil
		}
		overrides, _ := storage.LoadAll[model.Override](ctx, e.store, tenantID, storage.KindOverride)
		ovs := make([]model.Override, 0, len(overrides))
		for _, o := range overrides {
			if o.ScheduleID == sched.ID || o.ScheduleID == sched.Name {
				ovs = append(ovs, *o)
			}
		}
		offset := 0
		if target.EscalateTo == "backup" {
			offset = 1 // second person on the wheel (SPEC §9.4)
		}
		for _, shift := range model.ResolveOnCall(sched, ovs, time.Now().UTC(), offset) {
			ids = append(ids, shift.ContactID)
		}
	case target.ContactGroup != "":
		group, err := storage.LoadOne[model.ContactGroup](ctx, e.store, tenantID,
			storage.KindContactGroup, target.ContactGroup)
		if err != nil {
			return nil
		}
		ids = group.Members
	case target.Contact != "":
		ids = []string{target.Contact}
	}
	var out []*model.Contact
	for _, id := range ids {
		c, err := storage.LoadOne[model.Contact](ctx, e.store, tenantID, storage.KindContact, id)
		if err != nil {
			// id may be a name
			if c2, err2 := e.contactByRef(ctx, tenantID, id); err2 == nil {
				out = append(out, c2)
			}
			continue
		}
		out = append(out, c)
	}
	return out
}

func (e *Engine) contactByRef(ctx context.Context, tenantID, ref string) (*model.Contact, error) {
	return storage.LoadOne[model.Contact](ctx, e.store, tenantID, storage.KindContact, ref)
}

// preferredChannels picks the contact's channels for the active time
// profile (F-04.08), filtered by minimum severity.
func preferredChannels(c *model.Contact, sev model.Severity, now time.Time) []model.ChannelType {
	loc := time.UTC
	if c.TimeZone != "" {
		if l, err := time.LoadLocation(c.TimeZone); err == nil {
			loc = l
		}
	}
	local := now.In(loc)
	var fallback []model.ChannelType
	for _, pref := range c.Preferences {
		if pref.Severity != "" && sev.Rank() < pref.Severity.Rank() {
			continue
		}
		if pref.Period == "" {
			fallback = pref.Channels
			continue
		}
		tp := model.TimePeriod{Days: map[string][]string{}}
		_ = tp
		// Named-period preferences resolve through simple built-ins:
		// "worktime" = Mo–Fr 08:00–18:00, "night" = inverse.
		if matchProfile(pref.Period, local) {
			return pref.Channels
		}
	}
	return fallback
}

func matchProfile(period string, t time.Time) bool {
	hour := t.Hour()
	isWeekday := t.Weekday() >= time.Monday && t.Weekday() <= time.Friday
	switch period {
	case "worktime", "arbeitszeit":
		return isWeekday && hour >= 8 && hour < 18
	case "night", "nacht":
		return !isWeekday || hour < 8 || hour >= 18
	default:
		return false
	}
}

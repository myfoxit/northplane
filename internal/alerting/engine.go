package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/selector"
	"github.com/northplane/northplane/internal/storage"
)

// Escalator is implemented by the escalation engine; the alerting
// engine starts/stops chains through it (SPEC §9.1 pipeline order).
type Escalator interface {
	StartChain(ctx context.Context, alert *model.Alert, policyName string) error
	StopChain(ctx context.Context, alertID string) error
}

// Engine consumes bus.Events and maintains alerts.
type Engine struct {
	store *storage.Store
	cat   *catalog.Catalog
	bus   *eventbus.Bus
	esc   Escalator
	log   *slog.Logger

	mu       sync.RWMutex
	rules    map[string][]*CompiledRule // tenant →
	pending  map[string]*pendingAlert   // tenant/dedup →
	lastSeen map[string]time.Time       // tenant/source → heartbeat tracking
	// suppressedOpen tracks alerts that opened while suppressed (downtime/
	// silence/flap) so their escalation chain can be started once the
	// suppression lifts — otherwise a still-broken service that went CRIT
	// during a downtime would never page (SPEC §9.1).
	suppressedOpen map[string]suppressedRef // alertID →

	statMatched uint64
	statOpened  uint64
}

// suppressedRef remembers what to start once suppression lifts.
type suppressedRef struct {
	tenantID string
	policy   string
}

type pendingAlert struct {
	rule     *model.AlertRule
	tenantID string
	dedup    string
	firstAt  time.Time
	lastAt   time.Time
	draft    *model.Alert
}

// NewEngine builds the engine.
func NewEngine(store *storage.Store, cat *catalog.Catalog, bus *eventbus.Bus,
	esc Escalator, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		store: store, cat: cat, bus: bus, esc: esc, log: log,
		rules:          map[string][]*CompiledRule{},
		pending:        map[string]*pendingAlert{},
		lastSeen:       map[string]time.Time{},
		suppressedOpen: map[string]suppressedRef{},
	}
}

// ReloadRules recompiles a tenant's rules (config mutation hook).
func (en *Engine) ReloadRules(ctx context.Context, tenantID string) error {
	rules, err := storage.LoadAll[model.AlertRule](ctx, en.store, tenantID, storage.KindAlertRule)
	if err != nil {
		return err
	}
	var compiled []*CompiledRule
	for _, r := range rules {
		if r.Disabled {
			continue
		}
		cr, err := CompileRule(r)
		if err != nil {
			en.log.Warn("alerting: rule skipped", "rule", r.Name, "err", err)
			continue
		}
		compiled = append(compiled, cr)
	}
	en.mu.Lock()
	en.rules[tenantID] = compiled
	en.mu.Unlock()
	return nil
}

// ReloadAll loads every tenant's rules.
func (en *Engine) ReloadAll(ctx context.Context) error {
	tenants, err := en.store.Tenants(ctx)
	if err != nil {
		return err
	}
	for _, t := range tenants {
		if err := en.ReloadRules(ctx, t.ID); err != nil {
			return err
		}
	}
	return nil
}

// Run consumes events and ticks timers until ctx ends.
func (en *Engine) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-en.bus.Events:
			en.handleEvent(ctx, e)
		case <-ticker.C:
			en.firePending(ctx, time.Now().UTC())
			en.checkHeartbeats(ctx, time.Now().UTC())
			en.autoClose(ctx, time.Now().UTC())
			en.reEvaluateSuppressed(ctx)
			en.wakeSnoozed(ctx, time.Now().UTC())
		}
	}
}

func (en *Engine) handleEvent(ctx context.Context, e *model.Event) {
	// heartbeat bookkeeping
	if e.SourceID != "" {
		en.mu.Lock()
		en.lastSeen[e.TenantID+"/"+e.SourceID] = e.TS
		en.mu.Unlock()
	}

	// Direct object routing (Nagios contact_groups semantics): hard
	// state changes notify the object's contacts/contactGroups without
	// requiring an alert rule.
	en.notifyObjectContacts(ctx, e)

	view := EventView(e)
	en.mu.RLock()
	rules := en.rules[e.TenantID]
	en.mu.RUnlock()

	for _, cr := range rules {
		ok, err := cr.Matches(view)
		if err != nil {
			en.log.Warn("alerting: eval", "rule", cr.Rule.Name, "err", err)
			continue
		}
		if !ok {
			// resolve-on-OK: an OK state_change closes the open alert
			// sharing the dedup key.
			en.maybeResolve(ctx, cr.Rule, e, view)
			continue
		}
		en.mu.Lock()
		en.statMatched++
		en.mu.Unlock()
		en.matchedEvent(ctx, cr.Rule, e, view)
	}
}

// dedupKey renders the rule's dedup template (default: object/rule).
func dedupKey(r *model.AlertRule, e *model.Event, view map[string]any) string {
	if r.DedupKey == "" {
		if e.ObjectID != "" {
			return e.ObjectID + "/" + r.Name
		}
		if dk, ok := view["dedupKey"].(string); ok && dk != "" {
			return r.Name + "/" + dk
		}
		return r.Name + "/" + e.SourceID
	}
	tpl, err := template.New("dedup").Option("missingkey=zero").Parse(r.DedupKey)
	if err != nil {
		return r.Name + "/badtemplate"
	}
	var buf bytes.Buffer
	_ = tpl.Execute(&buf, map[string]any{
		"event":  view,
		"object": map[string]any{"id": e.ObjectID},
		"rule":   map[string]any{"name": r.Name},
	})
	return buf.String()
}

func title(r *model.AlertRule, view map[string]any) string {
	if r.Title != "" {
		tpl, err := template.New("title").Option("missingkey=zero").Parse(r.Title)
		if err == nil {
			var buf bytes.Buffer
			if tpl.Execute(&buf, map[string]any{"event": view}) == nil && buf.Len() > 0 {
				return buf.String()
			}
		}
	}
	if s, ok := view["summary"].(string); ok && s != "" {
		return s
	}
	if o, ok := view["object"].(string); ok && o != "" {
		state, _ := view["state"].(string)
		return fmt.Sprintf("%s is %s", o, state)
	}
	return r.Name
}

func (en *Engine) matchedEvent(ctx context.Context, r *model.AlertRule, e *model.Event, view map[string]any) {
	// Problem events with severity ok resolve instead of open.
	isClear := e.Severity == model.SevOK
	if st, ok := view["state"].(string); ok && (st == "OK" || st == "UP") {
		isClear = true
	}
	if resolve, ok := view["payload"].(map[string]any)["resolve"].(bool); ok && resolve {
		isClear = true
	}
	dk := dedupKey(r, e, view)
	if isClear {
		en.resolveByDedup(ctx, e.TenantID, dk, r)
		return
	}

	labels := model.Labels{}
	if lv, ok := view["labels"].(map[string]any); ok {
		for k, v := range lv {
			labels[k] = fmt.Sprint(v)
		}
	}
	labels = labels.Merge(r.SetLabels)

	draft := &model.Alert{
		TenantID: e.TenantID, RuleID: r.ID, ObjectID: e.ObjectID,
		Severity: r.Severity, Title: title(r, view), DedupKey: dk,
		Labels: labels, EventIDs: []string{e.ID}, Payload: e.Payload,
	}
	if draft.Severity == "" {
		draft.Severity = e.Severity
	}

	if r.PendingFor > 0 {
		key := e.TenantID + "/" + dk
		en.mu.Lock()
		p := en.pending[key]
		if p == nil {
			en.pending[key] = &pendingAlert{rule: r, tenantID: e.TenantID,
				dedup: dk, firstAt: e.TS, lastAt: e.TS, draft: draft}
		} else {
			p.lastAt = e.TS
			p.draft = draft // newest event wins for title/payload
		}
		en.mu.Unlock()
		return
	}
	en.open(ctx, r, draft)
}

// firePending opens alerts whose condition held for pendingFor.
func (en *Engine) firePending(ctx context.Context, now time.Time) {
	var due []*pendingAlert
	en.mu.Lock()
	for key, p := range en.pending {
		if now.Sub(p.firstAt) >= p.rule.PendingFor.D() {
			due = append(due, p)
			delete(en.pending, key)
		}
	}
	en.mu.Unlock()
	for _, p := range due {
		en.open(ctx, p.rule, p.draft)
	}
}

func (en *Engine) open(ctx context.Context, r *model.AlertRule, draft *model.Alert) {
	stored, created, err := en.store.UpsertAlert(ctx, draft)
	if err != nil {
		en.log.Error("alerting: upsert", "err", err)
		return
	}
	if !created {
		return // refreshed existing alert; chain already running
	}
	en.mu.Lock()
	en.statOpened++
	en.mu.Unlock()

	raw, _ := json.Marshal(map[string]any{
		"alertId": stored.ID, "title": stored.Title,
		"severity": stored.Severity, "rule": r.Name, "labels": stored.Labels})
	en.persistAndFanout(ctx, &model.Event{
		ID: model.NewID(), TenantID: stored.TenantID, TS: time.Now().UTC(),
		Type: model.EventAlertOpened, ObjectID: stored.ObjectID,
		Severity: stored.Severity, Payload: raw,
	})

	// rule-driven incident (F-04.05): every fresh alert opens its own
	// incident; resolution closes it once all bundled alerts resolved.
	if r.Incident {
		en.openIncident(ctx, r, stored)
	}

	// Suppression gate (SPEC §9.1): downtime, silence, flapping,
	// unreachable — alert exists, the chain does not start.
	suppressed, reason := en.Suppressed(ctx, stored)
	if suppressed {
		raw, _ := json.Marshal(model.NotificationRecord{
			AlertID: stored.ID, Status: model.NotifySuppress, Error: reason})
		en.persistAndFanout(ctx, &model.Event{
			ID: model.NewID(), TenantID: stored.TenantID, TS: time.Now().UTC(),
			Type: model.EventNotification, ObjectID: stored.ObjectID,
			Severity: model.SevInfo, Payload: raw,
		})
		// Remember so the chain can start once suppression lifts.
		if r.EscalationPolicy != "" && en.esc != nil {
			en.mu.Lock()
			en.suppressedOpen[stored.ID] = suppressedRef{tenantID: stored.TenantID, policy: r.EscalationPolicy}
			en.mu.Unlock()
		}
		return
	}
	if r.EscalationPolicy != "" && en.esc != nil {
		if err := en.esc.StartChain(ctx, stored, r.EscalationPolicy); err != nil {
			en.log.Error("alerting: escalation start", "alert", stored.ID, "err", err)
		}
	}
}

// reEvaluateSuppressed starts the escalation chain for any alert that
// opened while suppressed and is now (a) still open & unacked and (b) no
// longer suppressed — e.g. its downtime/silence window ended while the
// service stayed broken (SPEC §9.1). Resolved/acked/vanished alerts are
// simply forgotten.
func (en *Engine) reEvaluateSuppressed(ctx context.Context) {
	en.mu.RLock()
	pendingChecks := make(map[string]suppressedRef, len(en.suppressedOpen))
	for id, ref := range en.suppressedOpen {
		pendingChecks[id] = ref
	}
	en.mu.RUnlock()

	for id, ref := range pendingChecks {
		alert, err := en.store.GetAlert(ctx, ref.tenantID, id)
		if err != nil { // gone or transient — drop on not-found, retry otherwise
			if err == storage.ErrNotFound {
				en.forgetSuppressed(id)
			}
			continue
		}
		if alert.Status != model.AlertOpen { // acked/resolved/expired
			en.forgetSuppressed(id)
			continue
		}
		if suppressed, _ := en.Suppressed(ctx, alert); suppressed {
			continue // still suppressed; check again next tick
		}
		if err := en.esc.StartChain(ctx, alert, ref.policy); err != nil {
			en.log.Error("alerting: deferred escalation start", "alert", id, "err", err)
			continue
		}
		en.log.Info("alerting: escalation started after suppression lifted", "alert", id)
		en.forgetSuppressed(id)
	}
}

func (en *Engine) forgetSuppressed(alertID string) {
	en.mu.Lock()
	delete(en.suppressedOpen, alertID)
	en.mu.Unlock()
}

func (en *Engine) maybeResolve(ctx context.Context, r *model.AlertRule, e *model.Event, view map[string]any) {
	if r.ResolveOnOK != nil && !*r.ResolveOnOK {
		return
	}
	st, _ := view["state"].(string)
	resolveFlag, _ := view["payload"].(map[string]any)["resolve"].(bool)
	if st != "OK" && st != "UP" && e.Severity != model.SevOK && !resolveFlag {
		return
	}
	// Only events that would have matched the rule's object/source clear
	// it: recompute the dedup key and resolve.
	dk := dedupKey(r, e, view)
	en.resolveByDedup(ctx, e.TenantID, dk, r)
}

func (en *Engine) resolveByDedup(ctx context.Context, tenantID, dedup string, r *model.AlertRule) {
	// clear pending if armed
	en.mu.Lock()
	delete(en.pending, tenantID+"/"+dedup)
	en.mu.Unlock()

	alert, err := en.store.FindOpenAlertByDedup(ctx, tenantID, dedup)
	if err != nil {
		return
	}
	resolved, err := en.store.ResolveAlert(ctx, tenantID, alert.ID, model.AlertResolved)
	if err != nil {
		return
	}
	if en.esc != nil {
		_ = en.esc.StopChain(ctx, alert.ID)
	}
	raw, _ := json.Marshal(map[string]any{
		"alertId": resolved.ID, "title": resolved.Title, "rule": r.Name})
	en.persistAndFanout(ctx, &model.Event{
		ID: model.NewID(), TenantID: tenantID, TS: time.Now().UTC(),
		Type: model.EventAlertResolved, ObjectID: resolved.ObjectID,
		Severity: model.SevOK, Payload: raw,
	})
	en.MaybeResolveIncident(ctx, resolved)
}

// openIncident creates the rule-driven incident and bundles the alert.
func (en *Engine) openIncident(ctx context.Context, r *model.AlertRule, alert *model.Alert) {
	inc := &model.Incident{TenantID: alert.TenantID, Status: model.IncidentOpen,
		Severity: alert.Severity, Title: alert.Title,
		TicketURL: "", CreatedBy: "rule:" + r.Name, OpenedAt: alert.OpenedAt}
	if err := en.store.CreateIncident(ctx, inc); err != nil {
		en.log.Error("alerting: incident create", "alert", alert.ID, "err", err)
		return
	}
	if err := en.store.AssignAlertIncident(ctx, alert.TenantID, alert.ID, inc.ID); err != nil {
		en.log.Error("alerting: incident assign", "alert", alert.ID, "err", err)
		return
	}
	alert.IncidentID = inc.ID
	raw, _ := json.Marshal(map[string]any{
		"incidentId": inc.ID, "alertId": alert.ID, "title": inc.Title,
		"createdBy": inc.CreatedBy, "status": inc.Status})
	en.persistAndFanout(ctx, &model.Event{
		ID: model.NewID(), TenantID: alert.TenantID, TS: time.Now().UTC(),
		Type: model.EventIncidentUpdate, ObjectID: alert.ObjectID,
		Severity: alert.Severity, Payload: raw,
	})
}

// MaybeResolveIncident closes a rule-created incident once its last
// active alert resolved. Human/AI-created incidents stay open — closing
// those is an explicit decision (SPEC §6.4). Exported: the API's manual
// resolve path shares the gate.
func (en *Engine) MaybeResolveIncident(ctx context.Context, alert *model.Alert) {
	if alert == nil || alert.IncidentID == "" {
		return
	}
	inc, err := en.store.GetIncident(ctx, alert.TenantID, alert.IncidentID)
	if err != nil || inc.Status != model.IncidentOpen ||
		!strings.HasPrefix(inc.CreatedBy, "rule:") {
		return
	}
	n, err := en.store.CountActiveAlertsByIncident(ctx, alert.TenantID, inc.ID)
	if err != nil || n > 0 {
		return
	}
	now := time.Now().UTC()
	inc.Status, inc.ResolvedAt = model.IncidentResolved, &now
	if err := en.store.UpdateIncident(ctx, inc, 0); err != nil {
		en.log.Error("alerting: incident resolve", "incident", inc.ID, "err", err)
		return
	}
	raw, _ := json.Marshal(map[string]any{
		"incidentId": inc.ID, "title": inc.Title, "status": inc.Status})
	en.persistAndFanout(ctx, &model.Event{
		ID: model.NewID(), TenantID: alert.TenantID, TS: now,
		Type: model.EventIncidentUpdate, ObjectID: alert.ObjectID,
		Severity: model.SevOK, Payload: raw,
	})
}

// checkHeartbeats raises alerts for silent sources (rule-based,
// F-02.02) and for explicit heartbeat resources.
func (en *Engine) checkHeartbeats(ctx context.Context, now time.Time) {
	en.mu.RLock()
	type hbRule struct {
		rule     *model.AlertRule
		tenantID string
	}
	var hbs []hbRule
	for tenant, rules := range en.rules {
		for _, cr := range rules {
			if cr.Rule.Heartbeat != nil {
				hbs = append(hbs, hbRule{cr.Rule, tenant})
			}
		}
	}
	en.mu.RUnlock()

	for _, hb := range hbs {
		key := hb.tenantID + "/" + hb.rule.Heartbeat.Source
		en.mu.RLock()
		last, seen := en.lastSeen[key]
		en.mu.RUnlock()
		if !seen {
			continue // never seen: arms after first event
		}
		dk := "heartbeat/" + hb.rule.Name
		if now.Sub(last) > hb.rule.Heartbeat.ExpectEvery.D() {
			draft := &model.Alert{
				TenantID: hb.tenantID, RuleID: hb.rule.ID,
				Severity: hb.rule.Severity, DedupKey: dk,
				Title: fmt.Sprintf("No event from %q for %s (expected every %s)",
					hb.rule.Heartbeat.Source, now.Sub(last).Round(time.Minute),
					hb.rule.Heartbeat.ExpectEvery),
				Payload: json.RawMessage(`{}`),
			}
			en.open(ctx, hb.rule, draft)
		} else {
			en.resolveByDedup(ctx, hb.tenantID, dk, hb.rule)
		}
	}

	// explicit heartbeat resources (POST /heartbeats/{id})
	tenants, err := en.store.Tenants(ctx)
	if err != nil {
		return
	}
	for _, t := range tenants {
		beats, err := en.store.ListHeartbeats(ctx, t.ID)
		if err != nil {
			continue
		}
		for _, h := range beats {
			grace := h.Grace.D()
			overdue := h.LastBeat == nil ||
				now.Sub(*h.LastBeat) > h.ExpectEvery.D()+grace
			if overdue && !h.Missing {
				flipped, err := en.store.MarkHeartbeatMissing(ctx, h.ID)
				if err != nil || !flipped {
					continue
				}
				raw, _ := json.Marshal(map[string]any{
					"heartbeat": h.Name, "labels": h.Labels,
					"summary": fmt.Sprintf("Heartbeat %q missing", h.Name)})
				en.persistAndFanout(ctx, &model.Event{
					ID: model.NewID(), TenantID: t.ID, TS: now,
					Type: model.EventHeartbeatMiss, SourceID: h.ID,
					Severity: h.Severity, Payload: raw,
				})
				// feed back through rules so alert rules can match it
				ev := &model.Event{ID: model.NewID(), TenantID: t.ID, TS: now,
					Type: model.EventHeartbeatMiss, SourceID: h.ID,
					Severity: h.Severity, Payload: raw}
				en.handleEvent(ctx, ev)
			}
		}
	}
}

// autoClose expires alerts past their rule's autoCloseAfter.
func (en *Engine) autoClose(ctx context.Context, now time.Time) {
	en.mu.RLock()
	type ac struct {
		tenant string
		rule   *model.AlertRule
	}
	var acs []ac
	for tenant, rules := range en.rules {
		for _, cr := range rules {
			if cr.Rule.AutoCloseAfter > 0 {
				acs = append(acs, ac{tenant, cr.Rule})
			}
		}
	}
	en.mu.RUnlock()
	for _, a := range acs {
		cutoff := now.Add(-a.rule.AutoCloseAfter.D())
		n, err := en.store.ExpireStaleAlerts(ctx, a.tenant, a.rule.ID, cutoff)
		if err == nil && n > 0 {
			en.log.Info("alerting: auto-closed", "rule", a.rule.Name, "n", n)
		}
	}
}

// Suppressed checks downtimes, silences, flapping and reachability
// (SPEC §9.1 stage 4).
func (en *Engine) Suppressed(ctx context.Context, a *model.Alert) (bool, string) {
	// object-level conditions
	if a.ObjectID != "" {
		if cs, err := en.store.GetCheckState(ctx, a.ObjectID); err == nil {
			if cs.DowntimeDepth > 0 {
				return true, "object in downtime"
			}
			if cs.Flapping {
				return true, "object flapping"
			}
			if cs.State == model.HostUnreachable {
				if e := en.cat.Get(a.ObjectID); e != nil && e.Object.Kind == model.KindHost {
					return true, "host unreachable (parent down)"
				}
			}
		}
		if e := en.cat.Get(a.ObjectID); e != nil && e.Host != nil {
			if hs, err := en.store.GetCheckState(ctx, e.Host.Object.ID); err == nil {
				if hs.State != model.HostUp && hs.StateType == model.StateHard {
					return true, "host down"
				}
				if hs.DowntimeDepth > 0 {
					return true, "host in downtime"
				}
			}
		}
	}
	// selector downtimes
	dts, err := en.store.ListDowntimes(ctx, a.TenantID, true)
	if err == nil {
		now := time.Now().UTC()
		for _, d := range dts {
			if !d.ActiveAt(now) {
				continue
			}
			if d.ObjectID != "" && d.ObjectID == a.ObjectID {
				return true, "downtime " + d.ID
			}
			if d.Selector != "" {
				if sel, err := selector.Parse(d.Selector); err == nil && sel.Matches(a.Labels) {
					return true, "downtime " + d.ID
				}
			}
		}
	}
	// silences (selector + text regex, SPEC §9.2)
	sis, err := en.store.ListSilences(ctx, a.TenantID, true)
	if err == nil {
		now := time.Now().UTC()
		for _, si := range sis {
			if !si.ActiveAt(now) {
				continue
			}
			selOK := si.Selector == ""
			if !selOK {
				if sel, err := selector.Parse(si.Selector); err == nil {
					selOK = sel.Matches(a.Labels)
				}
			}
			if !selOK {
				continue
			}
			if si.TextRegex != "" {
				re, err := regexp.Compile(si.TextRegex)
				if err != nil || !re.MatchString(a.Title) {
					continue
				}
			}
			return true, "silence " + si.ID
		}
	}
	return false, ""
}

// persistAndFanout stores a pipeline-internal event and feeds SSE only
// (NOT the alerting queue — these are alerting outputs).
// wakeSnoozed re-opens alerts whose snooze deadline passed and restarts
// their escalation chain (SnoozeAlert sets the deadline; ack/resolve
// clear it). The policy comes from the alert's rule — or, for manual
// alarms, from the escalationPolicy recorded in the alert payload.
func (en *Engine) wakeSnoozed(ctx context.Context, now time.Time) {
	woken, err := en.store.WakeSnoozedAlerts(ctx, now)
	if err != nil {
		en.log.Error("alerting: snooze wake", "err", err)
		return
	}
	for _, alert := range woken {
		policy := en.policyForAlert(alert)
		raw, _ := json.Marshal(map[string]any{
			"alertId": alert.ID, "title": alert.Title,
			"comment": "snooze expired — alarm re-armed", "policy": policy})
		en.persistAndFanout(ctx, &model.Event{
			ID: model.NewID(), TenantID: alert.TenantID, TS: now,
			Type: model.EventEscalation, ObjectID: alert.ObjectID,
			Severity: alert.Severity, Payload: raw,
		})
		if alert.ObjectID != "" {
			_ = en.store.ClearAck(ctx, alert.ObjectID)
		}
		if policy != "" && en.esc != nil {
			// Chain restart counts from now, not from OpenedAt: rebase so
			// step 0 fires after its configured delay from the wake-up.
			rearmed := *alert
			rearmed.OpenedAt = now
			if err := en.esc.StartChain(ctx, &rearmed, policy); err != nil {
				en.log.Error("alerting: snooze re-arm", "alert", alert.ID, "err", err)
			}
		}
	}
}

// policyForAlert resolves the escalation policy driving an alert.
func (en *Engine) policyForAlert(alert *model.Alert) string {
	if alert.RuleID == "manual" || alert.RuleID == "" {
		var p struct {
			EscalationPolicy string `json:"escalationPolicy"`
		}
		_ = json.Unmarshal(alert.Payload, &p)
		return p.EscalationPolicy
	}
	en.mu.RLock()
	defer en.mu.RUnlock()
	for _, rules := range en.rules {
		for _, cr := range rules {
			if cr.Rule.ID == alert.RuleID {
				return cr.Rule.EscalationPolicy
			}
		}
	}
	return ""
}

func (en *Engine) persistAndFanout(ctx context.Context, e *model.Event) {
	if err := en.store.InsertEvents(ctx, []*model.Event{e}); err != nil {
		en.log.Error("alerting: event persist", "err", err)
	}
	en.bus.FanoutOnly(e)
}

// TestRule evaluates a rule against demo events or a historical range
// (SPEC §9.2 / F-05.04) without side effects.
type TestResult struct {
	Matched   int              `json:"matched"`
	WouldOpen []*model.Alert   `json:"wouldOpen"`
	Samples   []map[string]any `json:"sampleViews,omitempty"`
}

// TestRule runs the hypothetical evaluation.
func (en *Engine) TestRule(ctx context.Context, tenantID string, r *model.AlertRule,
	demo []*model.Event, from, to time.Time) (*TestResult, error) {
	cr, err := CompileRule(r)
	if err != nil {
		return nil, err
	}
	events := demo
	if len(events) == 0 && !from.IsZero() {
		events, err = en.store.QueryEvents(ctx, storage.EventFilter{
			TenantID: tenantID, From: from, To: to, Limit: 1000, Asc: true})
		if err != nil {
			return nil, err
		}
	}
	res := &TestResult{}
	seen := map[string]bool{}
	for _, e := range events {
		view := EventView(e)
		ok, _ := cr.Matches(view)
		if !ok {
			continue
		}
		res.Matched++
		if len(res.Samples) < 5 {
			res.Samples = append(res.Samples, view)
		}
		dk := dedupKey(r, e, view)
		if seen[dk] {
			continue
		}
		seen[dk] = true
		sev := r.Severity
		if sev == "" {
			sev = e.Severity
		}
		res.WouldOpen = append(res.WouldOpen, &model.Alert{
			TenantID: tenantID, RuleID: r.ID, ObjectID: e.ObjectID,
			Severity: sev, Title: title(r, view), DedupKey: dk,
		})
	}
	return res, nil
}

// Stats snapshot.
type Stats struct {
	Rules   int    `json:"rules"`
	Pending int    `json:"pending"`
	Matched uint64 `json:"matched"`
	Opened  uint64 `json:"opened"`
}

// Stats for self-metrics.
func (en *Engine) Stats() Stats {
	en.mu.RLock()
	defer en.mu.RUnlock()
	n := 0
	for _, rs := range en.rules {
		n += len(rs)
	}
	return Stats{Rules: n, Pending: len(en.pending), Matched: en.statMatched, Opened: en.statOpened}
}

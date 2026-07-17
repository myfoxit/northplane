package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/notify"
	"github.com/northplane/northplane/internal/storage"
)

func (a *API) registerAlerts() {
	a.handle("GET /api/v1/alerts", "List alerts", "alerts:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			q := r.URL.Query()
			f := storage.AlertFilter{
				TenantID: a.tenantOf(r, p), ObjectID: q.Get("objectId"),
				RuleID: q.Get("ruleId"), IncidentID: q.Get("incidentId"),
				Cursor: q.Get("cursor"), Limit: queryInt(r, "limit", 100),
			}
			for _, s := range strings.Split(q.Get("status"), ",") {
				if s = strings.TrimSpace(s); s != "" {
					f.Status = append(f.Status, model.AlertStatus(s))
				}
			}
			for _, s := range strings.Split(q.Get("severity"), ",") {
				if s = strings.TrimSpace(s); s != "" {
					f.Severity = append(f.Severity, model.Severity(s))
				}
			}
			if since := q.Get("since"); since != "" {
				if t, err := time.Parse(time.RFC3339, since); err == nil {
					f.Since = t
				}
			}
			alerts, err := a.Store.ListAlerts(r.Context(), f)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			next := ""
			if len(alerts) == f.Limit {
				next = alerts[len(alerts)-1].ID
			}
			a.writeList(w, alerts, next)
		}).Query(
		oaParam{Name: "objectId", Desc: "Filter by monitored object id"},
		oaParam{Name: "ruleId", Desc: "Filter by alert rule id"},
		oaParam{Name: "incidentId", Desc: "Filter by incident id"},
		oaParam{Name: "status", Desc: "Comma-separated alert statuses (e.g. firing,resolved)"},
		oaParam{Name: "severity", Desc: "Comma-separated severities (e.g. critical,warning)"},
		oaParam{Name: "since", Desc: "RFC 3339 lower bound on alert time"},
	)

	a.handle("GET /api/v1/alerts/{id}", "Get alert", "alerts:read", nil, model.Alert{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			alert, err := a.Store.GetAlert(r.Context(), a.tenantOf(r, p), param(r, "id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeJSON(w, http.StatusOK, alert)
		})

	// Manual alarm raise (alarming pipelines): the web dialog, the mobile
	// alarm app and scripts can open an alert directly — with a message,
	// severity, labels (np.sound/np.volume steer the alarm app) and an
	// escalation policy that starts ringing people immediately. Manual
	// alarms deliberately bypass the suppression gate: a human pressed
	// the button.
	type raiseAlertRequest struct {
		Title            string         `json:"title"`
		Message          string         `json:"message,omitempty"`
		Severity         model.Severity `json:"severity,omitempty"` // default critical
		Labels           model.Labels   `json:"labels,omitempty"`
		EscalationPolicy string         `json:"escalationPolicy,omitempty"`
		ObjectID         string         `json:"objectId,omitempty"`
		// DedupKey folds repeated triggers into one open alert; empty =
		// every trigger opens a fresh alert.
		DedupKey string `json:"dedupKey,omitempty"`
	}
	a.handle("POST /api/v1/alerts", "Raise an alarm manually (starts escalation)",
		"alerts:write", raiseAlertRequest{}, model.Alert{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req raiseAlertRequest
			if !a.decode(w, r, &req) {
				return
			}
			if strings.TrimSpace(req.Title) == "" {
				a.validationError(w, r, "title", "title required")
				return
			}
			if req.Severity == "" {
				req.Severity = model.SevCritical
			}
			if !req.Severity.Valid() {
				a.validationError(w, r, "severity", "unknown severity")
				return
			}
			tenant := a.tenantOf(r, p)
			if req.EscalationPolicy != "" {
				if _, err := storage.LoadOne[model.EscalationPolicy](r.Context(), a.Store,
					tenant, storage.KindEscalationPolicy, req.EscalationPolicy); err != nil {
					a.validationError(w, r, "escalationPolicy", "unknown escalation policy")
					return
				}
			}
			alert, created, err := a.RaiseAlert(r.Context(), tenant, RaiseParams{
				Title: req.Title, Message: req.Message, Severity: req.Severity,
				Labels: req.Labels, EscalationPolicy: req.EscalationPolicy,
				ObjectID: req.ObjectID, DedupKey: req.DedupKey,
				By: p.Name, Via: "api",
			})
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "alert.raise", alert.ID, nil, req)
			status := http.StatusCreated
			if !created {
				status = http.StatusOK // dedup folded into an existing alarm
			}
			a.writeJSON(w, status, alert)
		}).Status(http.StatusCreated)

	type ackRequest struct {
		Comment string `json:"comment,omitempty"`
	}
	a.handle("POST /api/v1/alerts/{id}:ack", "Acknowledge alert (stops escalation)",
		"alerts:ack", ackRequest{}, model.Alert{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req ackRequest
			_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req) // body optional, capped
			alert, err := a.ackAlert(r, p.TenantID, param(r, "id"), p.Name, req.Comment, p)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeJSON(w, http.StatusOK, alert)
		})

	a.handle("POST /api/v1/alerts/{id}:resolve", "Resolve alert manually", "alerts:ack", nil, model.Alert{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			alert, err := a.Store.ResolveAlert(r.Context(), tenant, param(r, "id"), model.AlertResolved)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			_ = a.Escal.StopChain(r.Context(), alert.ID)
			a.Alert.MaybeResolveIncident(r.Context(), alert)
			a.audit(r, p, "alert.resolve", alert.ID, nil, alert)
			a.alertLifecycleEvent(r, tenant, alert, model.EventAlertResolved)
			a.writeJSON(w, http.StatusOK, alert)
		})

	type snoozeRequest struct {
		Until time.Time `json:"until"`
	}
	a.handle("POST /api/v1/alerts/{id}:snooze", "Snooze: ack now, auto-unack at `until`",
		"alerts:ack", snoozeRequest{}, model.Alert{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req snoozeRequest
			if !a.decode(w, r, &req) {
				return
			}
			if req.Until.Before(time.Now()) {
				a.validationError(w, r, "until", "until must be in the future")
				return
			}
			tenant := a.tenantOf(r, p)
			alert, err := a.Store.SnoozeAlert(r.Context(), tenant, param(r, "id"), p.Name, req.Until)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			_ = a.Escal.StopChain(r.Context(), alert.ID)
			if alert.ObjectID != "" {
				_ = a.Store.SetAck(r.Context(), alert.ObjectID, p.Name,
					"snoozed until "+req.Until.Format(time.RFC3339))
			}
			a.audit(r, p, "alert.snooze", alert.ID, nil, map[string]string{
				"until": req.Until.Format(time.RFC3339)})
			raw, _ := json.Marshal(map[string]any{"alertId": alert.ID, "by": p.Name,
				"comment": "snoozed until " + req.Until.Format(time.RFC3339)})
			ev := &model.Event{ID: model.NewID(), TenantID: tenant, TS: time.Now().UTC(),
				Type: model.EventAck, ObjectID: alert.ObjectID, Severity: model.SevInfo, Payload: raw}
			a.insertEvents(r.Context(), ev)
			a.Bus.FanoutOnly(ev)
			a.writeJSON(w, http.StatusOK, alert)
		})

	// Signed ack links from notifications (SPEC §9.4: one-shot, no login).
	a.mux.HandleFunc("GET /api/v1/ack/{token}", func(w http.ResponseWriter, r *http.Request) {
		token := param(r, "token")
		alertID, contactID, err := notify.VerifyAckToken(a.Notify.AckSecret, token)
		if err != nil {
			http.Error(w, "Dieser Bestätigungs-Link ist ungültig oder abgelaufen.", http.StatusForbidden)
			return
		}
		tenants, err := a.Store.Tenants(r.Context())
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		for _, t := range tenants {
			if alert, err := a.Store.GetAlert(r.Context(), t.ID, alertID); err == nil {
				if alert.Status == model.AlertOpen {
					contact, _ := storage.LoadOne[model.Contact](r.Context(), a.Store, t.ID, storage.KindContact, contactID)
					by := contactID
					if contact != nil {
						by = contact.Name
					}
					if _, err := a.Store.AckAlert(r.Context(), t.ID, alertID, by); err == nil {
						_ = a.Escal.StopChain(r.Context(), alertID)
						_, _ = a.Store.AppendAudit(r.Context(), &model.AuditEntry{
							TenantID: t.ID, ActorType: model.ActorUser, ActorID: by,
							Action: "alert.ack", Resource: alertID,
							AfterJSON: `{"via":"ack-link"}`, SourceIP: remoteHost(r),
						})
						a.alertLifecycleEventTenant(r, t.ID, alertID)
					}
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(`<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><body style="font-family:system-ui;background:#0b1220;color:#e2e8f0;display:grid;place-items:center;height:100vh;margin:0"><div style="text-align:center"><h1 style="color:#34d399">✓ Quittiert</h1><p>Der Alarm wurde übernommen. Die Eskalationskette ist gestoppt.</p></div>`))
				return
			}
		}
		http.Error(w, "alert not found", http.StatusNotFound)
	})

	// Voice DTMF gather callback (SPEC §9.6): Twilio POSTs the pressed
	// digit; 4 acknowledges, 6 resolves. Same signed one-shot token as
	// the ack link.
	a.mux.HandleFunc("POST /api/v1/voice/gather/{token}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		say := func(msg string) {
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Response><Say>` +
				msg + `</Say></Response>`))
		}
		alertID, contactID, err := notify.VerifyAckToken(a.Notify.AckSecret, param(r, "token"))
		if err != nil {
			say("This acknowledgement link is invalid or expired. Goodbye.")
			return
		}
		digits := ""
		if err := r.ParseForm(); err == nil {
			digits = r.PostFormValue("Digits")
		}
		if digits != "4" && digits != "6" {
			say("Not acknowledged. Goodbye.")
			return
		}
		tenants, err := a.Store.Tenants(r.Context())
		if err != nil {
			say("Internal error. Goodbye.")
			return
		}
		for _, t := range tenants {
			alert, err := a.Store.GetAlert(r.Context(), t.ID, alertID)
			if err != nil {
				continue
			}
			contact, _ := storage.LoadOne[model.Contact](r.Context(), a.Store, t.ID, storage.KindContact, contactID)
			by := contactID
			if contact != nil {
				by = contact.Name
			}
			switch {
			case digits == "6" && (alert.Status == model.AlertOpen || alert.Status == model.AlertAcked):
				if resolved, err := a.Store.ResolveAlert(r.Context(), t.ID, alertID, model.AlertResolved); err == nil {
					_ = a.Escal.StopChain(r.Context(), alertID)
					a.Alert.MaybeResolveIncident(r.Context(), resolved)
					_, _ = a.Store.AppendAudit(r.Context(), &model.AuditEntry{
						TenantID: t.ID, ActorType: model.ActorUser, ActorID: by,
						Action: "alert.resolve", Resource: alertID,
						AfterJSON: `{"via":"voice-dtmf"}`, SourceIP: remoteHost(r),
					})
					a.alertLifecycleEvent(r, t.ID, resolved, model.EventAlertResolved)
				}
				say("Alert resolved. Goodbye.")
				return
			case alert.Status == model.AlertOpen:
				if _, err := a.Store.AckAlert(r.Context(), t.ID, alertID, by); err == nil {
					_ = a.Escal.StopChain(r.Context(), alertID)
					_, _ = a.Store.AppendAudit(r.Context(), &model.AuditEntry{
						TenantID: t.ID, ActorType: model.ActorUser, ActorID: by,
						Action: "alert.ack", Resource: alertID,
						AfterJSON: `{"via":"voice-dtmf"}`, SourceIP: remoteHost(r),
					})
					a.alertLifecycleEventTenant(r, t.ID, alertID)
				}
			}
			say("Alert acknowledged. The escalation chain is stopped. Goodbye.")
			return
		}
		say("Alert not found. Goodbye.")
	})

	// incidents
	a.handle("GET /api/v1/incidents", "List incidents", "incidents:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			incidents, err := a.Store.ListIncidents(r.Context(), a.tenantOf(r, p),
				r.URL.Query().Get("open") == "true", r.URL.Query().Get("cursor"),
				queryInt(r, "limit", 50))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			next := ""
			if len(incidents) == queryInt(r, "limit", 50) {
				next = incidents[len(incidents)-1].ID
			}
			a.writeList(w, incidents, next)
		}).Query(oaParam{Name: "open", Desc: "Only currently-open incidents when true", Type: "boolean"})

	type incidentBody struct {
		Title     string         `json:"title"`
		Severity  model.Severity `json:"severity,omitempty"`
		Summary   string         `json:"summary,omitempty"`
		Impact    string         `json:"impact,omitempty"`
		TicketURL string         `json:"ticketUrl,omitempty"`
		AlertIDs  []string       `json:"alertIds,omitempty"`
	}
	a.handle("POST /api/v1/incidents", "Create incident", "incidents:write", incidentBody{}, model.Incident{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var body incidentBody
			if !a.decode(w, r, &body) {
				return
			}
			if body.Title == "" {
				a.validationError(w, r, "title", "title required")
				return
			}
			in := &model.Incident{TenantID: a.tenantOf(r, p), Status: model.IncidentOpen,
				Severity: body.Severity, Title: body.Title, Summary: body.Summary,
				Impact: body.Impact, TicketURL: body.TicketURL, CreatedBy: p.Name}
			if in.Severity == "" {
				in.Severity = model.SevWarning
			}
			if err := a.Store.CreateIncident(r.Context(), in); err != nil {
				a.fail(w, r, err)
				return
			}
			for _, alertID := range body.AlertIDs {
				_ = a.Store.AssignAlertIncident(r.Context(), in.TenantID, alertID, in.ID)
			}
			a.audit(r, p, "incident.create", in.ID, nil, in)
			// Publish through the rule engine (not fanout-only): an
			// app/API-created incident is a first-class alarm trigger — a
			// rule like `event.type == "incident_update" &&
			// event.payload.action == "created"` turns it into an alert
			// with an escalation chain (the mobile alarm app creates
			// incidents). Engine-created incidents stay fanout-only, so
			// there is no feedback loop.
			raw, _ := json.Marshal(map[string]any{
				"incidentId": in.ID, "title": in.Title, "summary": in.Summary,
				"createdBy": in.CreatedBy, "status": in.Status, "action": "created",
				"labels": map[string]string{"createdBy": in.CreatedBy},
			})
			ev := &model.Event{ID: model.NewID(), TenantID: in.TenantID, TS: time.Now().UTC(),
				Type: model.EventIncidentUpdate, Severity: in.Severity, Payload: raw}
			a.insertEvents(r.Context(), ev)
			_ = a.Bus.PublishEventCtx(r.Context(), ev)
			a.writeJSON(w, http.StatusCreated, in)
		})

	a.handle("GET /api/v1/incidents/{id}", "Get incident with alerts + timeline",
		"incidents:read", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			in, err := a.Store.GetIncident(r.Context(), tenant, param(r, "id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			alerts, _ := a.Store.ListAlerts(r.Context(), storage.AlertFilter{
				TenantID: tenant, IncidentID: in.ID, Limit: 500})
			// timeline: events referencing the incident's alerts/objects
			a.writeJSON(w, http.StatusOK, map[string]any{
				"incident": in, "alerts": alerts})
		})

	a.handle("PUT /api/v1/incidents/{id}", "Update incident (If-Match)", "incidents:write",
		incidentBody{}, model.Incident{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			version, ok := a.requireIfMatch(w, r)
			if !ok {
				return
			}
			var body incidentBody
			if !a.decode(w, r, &body) {
				return
			}
			tenant := a.tenantOf(r, p)
			in, err := a.Store.GetIncident(r.Context(), tenant, param(r, "id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			before := *in
			if body.Title != "" {
				in.Title = body.Title
			}
			if body.Severity != "" {
				in.Severity = body.Severity
			}
			in.Summary, in.Impact, in.TicketURL = body.Summary, body.Impact, body.TicketURL
			if err := a.Store.UpdateIncident(r.Context(), in, version); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "incident.update", in.ID, before, in)
			etag(w, in.Version)
			a.writeJSON(w, http.StatusOK, in)
		})

	a.handle("POST /api/v1/incidents/{id}:resolve", "Resolve incident + its alerts",
		"incidents:write", nil, model.Incident{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			in, err := a.Store.GetIncident(r.Context(), tenant, param(r, "id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			now := time.Now().UTC()
			in.Status, in.ResolvedAt = model.IncidentResolved, &now
			if err := a.Store.UpdateIncident(r.Context(), in, 0); err != nil {
				a.fail(w, r, err)
				return
			}
			alerts, _ := a.Store.ListAlerts(r.Context(), storage.AlertFilter{
				TenantID: tenant, IncidentID: in.ID,
				Status: []model.AlertStatus{model.AlertOpen, model.AlertAcked}, Limit: 1000})
			for _, alert := range alerts {
				if _, err := a.Store.ResolveAlert(r.Context(), tenant, alert.ID, model.AlertResolved); err == nil {
					_ = a.Escal.StopChain(r.Context(), alert.ID)
				}
			}
			a.audit(r, p, "incident.resolve", in.ID, nil, in)
			a.writeJSON(w, http.StatusOK, in)
		})

	type mergeRequest struct {
		SourceIDs []string `json:"sourceIds"`
	}
	a.handle("POST /api/v1/incidents/{id}:merge", "Merge incidents", "incidents:write",
		mergeRequest{}, model.Incident{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req mergeRequest
			if !a.decode(w, r, &req) {
				return
			}
			tenant := a.tenantOf(r, p)
			target, err := a.Store.GetIncident(r.Context(), tenant, param(r, "id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			for _, srcID := range req.SourceIDs {
				src, err := a.Store.GetIncident(r.Context(), tenant, srcID)
				if err != nil {
					continue
				}
				alerts, _ := a.Store.ListAlerts(r.Context(), storage.AlertFilter{
					TenantID: tenant, IncidentID: src.ID, Limit: 1000})
				for _, alert := range alerts {
					_ = a.Store.AssignAlertIncident(r.Context(), tenant, alert.ID, target.ID)
				}
				now := time.Now().UTC()
				src.Status, src.ResolvedAt = model.IncidentResolved, &now
				src.Summary = "merged into " + target.ID
				_ = a.Store.UpdateIncident(r.Context(), src, 0)
			}
			a.audit(r, p, "incident.merge", target.ID, nil, req)
			a.writeJSON(w, http.StatusOK, target)
		})

	a.handle("POST /api/v1/incidents/{id}:summarize", "AI summary (propose)", "incidents:write", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if a.AI == nil || !a.AI.Enabled() {
				a.problem(w, r, http.StatusServiceUnavailable, "np:ai/disabled",
					"AI provider not configured", "set ai.provider in config.yaml")
				return
			}
			tenant := a.tenantOf(r, p)
			summary, err := a.AI.SummarizeIncident(r.Context(), tenant, param(r, "id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			in, err := a.Store.GetIncident(r.Context(), tenant, param(r, "id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			in.Summary = summary
			if err := a.Store.UpdateIncident(r.Context(), in, 0); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "incident.summarize", in.ID, nil, map[string]string{"summary": summary})
			a.writeJSON(w, http.StatusOK, in)
		})
}

// RaiseParams describes a manually raised alarm (web dialog, alarm app,
// inbound phone call / SMS).
type RaiseParams struct {
	Title            string
	Message          string
	Severity         model.Severity
	Labels           model.Labels
	EscalationPolicy string
	ObjectID         string
	DedupKey         string
	By               string // human-readable actor
	Via              string // api | web | voice | sms
}

// RaiseAlert opens (or dedup-refreshes) a manual alert, emits the
// alert_opened event and starts the escalation chain. Manual alarms skip
// the suppression gate on purpose.
func (a *API) RaiseAlert(ctx context.Context, tenantID string, p RaiseParams) (*model.Alert, bool, error) {
	summary := p.Message
	if summary == "" {
		summary = p.Title
	}
	payload, _ := json.Marshal(map[string]any{
		"summary": summary, "manual": true, "by": p.By, "via": p.Via,
		"escalationPolicy": p.EscalationPolicy,
	})
	draft := &model.Alert{
		TenantID: tenantID, RuleID: "manual", ObjectID: p.ObjectID,
		Severity: p.Severity, Title: p.Title, DedupKey: p.DedupKey,
		Labels: p.Labels, Payload: payload,
	}
	alert, created, err := a.Store.UpsertAlert(ctx, draft)
	if err != nil {
		return nil, false, err
	}
	if !created {
		return alert, false, nil // folded into the open alarm; chain already runs
	}
	raw, _ := json.Marshal(map[string]any{
		"alertId": alert.ID, "title": alert.Title, "severity": alert.Severity,
		"rule": "manual", "via": p.Via, "labels": alert.Labels})
	ev := &model.Event{ID: model.NewID(), TenantID: tenantID, TS: time.Now().UTC(),
		Type: model.EventAlertOpened, ObjectID: alert.ObjectID,
		Severity: alert.Severity, Payload: raw}
	a.insertEvents(ctx, ev)
	a.Bus.FanoutOnly(ev)
	if p.EscalationPolicy != "" {
		if err := a.Escal.StartChain(ctx, alert, p.EscalationPolicy); err != nil {
			a.Log.Error("alert raise: escalation start", "alert", alert.ID, "err", err)
		}
	}
	return alert, true, nil
}

// AckAlertVia acknowledges an alert for non-HTTP callers (FastAGI phone
// line): store transition, chain stop, sticky object ack, audit + event
// — the same effects as POST /alerts/{id}:ack.
func (a *API) AckAlertVia(ctx context.Context, tenantID, alertID, by, via string) error {
	alert, err := a.Store.AckAlert(ctx, tenantID, alertID, by)
	if err != nil {
		return err
	}
	_ = a.Escal.StopChain(ctx, alertID)
	if alert.ObjectID != "" {
		_ = a.Store.SetAck(ctx, alert.ObjectID, by, "acknowledged via "+via)
	}
	_, _ = a.Store.AppendAudit(ctx, &model.AuditEntry{
		TenantID: tenantID, ActorType: model.ActorUser, ActorID: by,
		Action: "alert.ack", Resource: alertID,
		AfterJSON: `{"via":` + jsonString(via) + `}`,
	})
	raw, _ := json.Marshal(map[string]any{"alertId": alertID, "by": by, "via": via})
	ev := &model.Event{ID: model.NewID(), TenantID: tenantID, TS: time.Now().UTC(),
		Type: model.EventAck, ObjectID: alert.ObjectID, Severity: model.SevInfo, Payload: raw}
	a.insertEvents(ctx, ev)
	a.Bus.FanoutOnly(ev)
	return nil
}

// ResolveAlertVia resolves an alert for non-HTTP callers, mirroring
// POST /alerts/{id}:resolve.
func (a *API) ResolveAlertVia(ctx context.Context, tenantID, alertID, by, via string) error {
	alert, err := a.Store.ResolveAlert(ctx, tenantID, alertID, model.AlertResolved)
	if err != nil {
		return err
	}
	_ = a.Escal.StopChain(ctx, alertID)
	if a.Alert != nil {
		a.Alert.MaybeResolveIncident(ctx, alert)
	}
	_, _ = a.Store.AppendAudit(ctx, &model.AuditEntry{
		TenantID: tenantID, ActorType: model.ActorUser, ActorID: by,
		Action: "alert.resolve", Resource: alertID,
		AfterJSON: `{"via":` + jsonString(via) + `}`,
	})
	raw, _ := json.Marshal(map[string]any{"alertId": alertID, "title": alert.Title, "by": by, "via": via})
	ev := &model.Event{ID: model.NewID(), TenantID: tenantID, TS: time.Now().UTC(),
		Type: model.EventAlertResolved, ObjectID: alert.ObjectID, Severity: model.SevOK, Payload: raw}
	a.insertEvents(ctx, ev)
	a.Bus.FanoutOnly(ev)
	return nil
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ackAlert is shared by :ack, :snooze and ack links.
func (a *API) ackAlert(r *http.Request, tenantID, alertID, by, comment string, p *auth.Principal) (*model.Alert, error) {
	alert, err := a.Store.AckAlert(r.Context(), tenantID, alertID, by)
	if err != nil {
		return nil, err
	}
	_ = a.Escal.StopChain(r.Context(), alert.ID)
	// mirror onto the object's check_state (sticky ack, SPEC §6.3) via a
	// column-scoped write so the pipeline's batched state flush can't
	// clobber it with a stale cached copy.
	if alert.ObjectID != "" {
		_ = a.Store.SetAck(r.Context(), alert.ObjectID, by, comment)
	}
	if p != nil {
		a.audit(r, p, "alert.ack", alert.ID, nil, map[string]string{"comment": comment})
	}
	raw, _ := json.Marshal(map[string]any{"alertId": alert.ID, "by": by, "comment": comment})
	ev := &model.Event{ID: model.NewID(), TenantID: tenantID, TS: time.Now().UTC(),
		Type: model.EventAck, ObjectID: alert.ObjectID, Severity: model.SevInfo, Payload: raw}
	a.insertEvents(r.Context(), ev)
	a.Bus.FanoutOnly(ev)
	return alert, nil
}

func (a *API) alertLifecycleEvent(r *http.Request, tenantID string, alert *model.Alert, typ model.EventType) {
	raw, _ := json.Marshal(map[string]any{"alertId": alert.ID, "title": alert.Title})
	ev := &model.Event{ID: model.NewID(), TenantID: tenantID, TS: time.Now().UTC(),
		Type: typ, ObjectID: alert.ObjectID, Severity: model.SevOK, Payload: raw}
	a.insertEvents(r.Context(), ev)
	a.Bus.FanoutOnly(ev)
}

func (a *API) alertLifecycleEventTenant(r *http.Request, tenantID, alertID string) {
	raw, _ := json.Marshal(map[string]any{"alertId": alertID, "via": "ack-link"})
	ev := &model.Event{ID: model.NewID(), TenantID: tenantID, TS: time.Now().UTC(),
		Type: model.EventAck, Severity: model.SevInfo, Payload: raw}
	a.insertEvents(r.Context(), ev)
	a.Bus.FanoutOnly(ev)
}

package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

func (a *API) registerRules() {
	a.resourceCRUD("alert-rules", storage.KindAlertRule, "config", model.AlertRule{})
	a.resourceCRUD("alert-groups", storage.KindAlertGroup, "config", model.AlertGroup{})
	a.resourceCRUD("escalation-policies", storage.KindEscalationPolicy, "config", model.EscalationPolicy{})

	// Rule test runner (SPEC §9.2 / F-05.04): demo events or historical
	// range, returns hypothetical alerts — a first-class feature here.
	type ruleTestRequest struct {
		Rule       model.AlertRule `json:"rule"`
		DemoEvents []*model.Event  `json:"demoEvents,omitempty"`
		From       time.Time       `json:"from,omitempty"`
		To         time.Time       `json:"to,omitempty"`
	}
	a.handle("POST /api/v1/alert-rules:test", "Evaluate a rule against demo or historical events",
		"alerts:read", ruleTestRequest{}, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req ruleTestRequest
			if !a.decode(w, r, &req) {
				return
			}
			if req.To.IsZero() {
				req.To = time.Now().UTC()
			}
			res, err := a.Alert.TestRule(r.Context(), a.tenantOf(r, p), &req.Rule,
				req.DemoEvents, req.From, req.To)
			if err != nil {
				a.validationError(w, r, "rule", err.Error())
				return
			}
			a.writeJSON(w, http.StatusOK, res)
		})

	// Saved test for an existing rule.
	a.handle("POST /api/v1/alert-rules/{name}:test", "Evaluate stored rule against history",
		"alerts:read", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			rule, err := storage.LoadOne[model.AlertRule](r.Context(), a.Store, tenant,
				storage.KindAlertRule, param(r, "name"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			var req struct {
				DemoEvents []*model.Event `json:"demoEvents,omitempty"`
				From       time.Time      `json:"from,omitempty"`
				To         time.Time      `json:"to,omitempty"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.To.IsZero() {
				req.To = time.Now().UTC()
			}
			if req.From.IsZero() && len(req.DemoEvents) == 0 {
				req.From = req.To.Add(-24 * time.Hour)
			}
			res, err := a.Alert.TestRule(r.Context(), tenant, rule, req.DemoEvents, req.From, req.To)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeJSON(w, http.StatusOK, res)
		})

	// Escalation simulator (SPEC §12.3: Policies mit Simulator).
	type simulateResponse struct {
		Steps []map[string]any `json:"steps"`
	}
	a.handle("POST /api/v1/escalation-policies/{name}:simulate",
		"Dry-run: who would be notified when", "alerts:read", nil, simulateResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			policy, err := storage.LoadOne[model.EscalationPolicy](r.Context(), a.Store, tenant,
				storage.KindEscalationPolicy, param(r, "name"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			now := time.Now().UTC()
			resp := simulateResponse{}
			for i, step := range policy.Steps {
				entry := map[string]any{
					"step": i + 1, "at": now.Add(step.After.D()).Format(time.RFC3339),
					"after": step.After.String(), "unlessAcked": step.UnlessAcked,
					"channels": step.Channels,
				}
				if step.Notify != nil {
					var who []string
					if step.Notify.Schedule != "" {
						sched, err := storage.LoadOne[model.Schedule](r.Context(), a.Store, tenant,
							storage.KindSchedule, step.Notify.Schedule)
						if err == nil {
							overrides, _ := storage.LoadAll[model.Override](r.Context(), a.Store, tenant, storage.KindOverride)
							ovs := filterOverrides(overrides, sched)
							offset := 0
							if step.Notify.EscalateTo == "backup" {
								offset = 1
							}
							for _, s := range model.ResolveOnCall(sched, ovs, now.Add(step.After.D()), offset) {
								who = append(who, a.contactName(r, tenant, s.ContactID))
							}
						}
						entry["schedule"] = step.Notify.Schedule
					}
					if step.Notify.Contact != "" {
						who = append(who, a.contactName(r, tenant, step.Notify.Contact))
					}
					if step.Notify.ContactGroup != "" {
						if g, err := storage.LoadOne[model.ContactGroup](r.Context(), a.Store, tenant,
							storage.KindContactGroup, step.Notify.ContactGroup); err == nil {
							for _, m := range g.Members {
								who = append(who, a.contactName(r, tenant, m))
							}
						}
					}
					entry["notify"] = who
				}
				if step.Action != nil {
					entry["action"] = step.Action
				}
				if step.RepeatEvery > 0 {
					entry["repeatEvery"] = step.RepeatEvery.String()
					entry["maxRepeats"] = step.MaxRepeats
				}
				resp.Steps = append(resp.Steps, entry)
			}
			a.writeJSON(w, http.StatusOK, resp)
		})
}

func filterOverrides(overrides []*model.Override, sched *model.Schedule) []model.Override {
	var out []model.Override
	for _, o := range overrides {
		if o.ScheduleID == sched.ID || o.ScheduleID == sched.Name {
			out = append(out, *o)
		}
	}
	return out
}

func (a *API) contactName(r *http.Request, tenantID, ref string) string {
	c, err := storage.LoadOne[model.Contact](r.Context(), a.Store, tenantID, storage.KindContact, ref)
	if err != nil {
		return ref
	}
	return c.Name
}

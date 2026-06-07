package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/selector"
)

func (a *API) registerMaintenance() {
	// --- downtimes (SPEC §6.3, §11.4) ---
	type downtimeBody struct {
		ObjectID string             `json:"objectId,omitempty"`
		Selector string             `json:"selector,omitempty"`
		Type     model.DowntimeType `json:"type,omitempty"`
		Start    time.Time          `json:"start"`
		End      time.Time          `json:"end"`
		Duration model.Duration     `json:"duration,omitempty"` // flexible
		RRule    string             `json:"rrule,omitempty"`
		Comment  string             `json:"comment"`
	}
	a.handle("POST /api/v1/downtimes", "Create downtime (Idempotency-Key honoured)",
		"downtimes:write", downtimeBody{}, model.Downtime{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			a.idempotent(w, r, p, func(body []byte) (int, any, error) {
				var req downtimeBody
				if err := json.Unmarshal(body, &req); err != nil {
					return 0, nil, errValidation("body", err.Error())
				}
				if req.ObjectID == "" && req.Selector == "" {
					return 0, nil, errValidation("target", "objectId or selector required")
				}
				if req.Selector != "" {
					if _, err := selector.Parse(req.Selector); err != nil {
						return 0, nil, errValidation("selector", err.Error())
					}
				}
				if req.Comment == "" {
					return 0, nil, errValidation("comment", "comment required")
				}
				if !req.End.After(req.Start) {
					return 0, nil, errValidation("window", "end must be after start")
				}
				d := &model.Downtime{
					TenantID: a.tenantOf(r, p), ObjectID: req.ObjectID, Selector: req.Selector,
					Type: req.Type, Start: req.Start.UTC(), End: req.End.UTC(),
					FlexDuration: req.Duration, RRule: req.RRule,
					Comment: req.Comment, CreatedBy: p.Name,
				}
				if err := a.Store.CreateDowntime(r.Context(), d); err != nil {
					return 0, nil, err
				}
				a.audit(r, p, "downtime.create", d.ID, nil, d)
				a.maintenanceEvent(r, d.TenantID, model.EventDowntime, map[string]any{
					"downtimeId": d.ID, "comment": d.Comment, "start": d.Start, "end": d.End})
				a.refreshDowntimeDepths(r, d.TenantID)
				return http.StatusCreated, d, nil
			})
		})

	a.handle("GET /api/v1/downtimes", "List downtimes", "objects:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			ds, err := a.Store.ListDowntimes(r.Context(), a.tenantOf(r, p),
				r.URL.Query().Get("active") == "true")
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeList(w, ds, "")
		})

	a.handle("DELETE /api/v1/downtimes/{id}", "Cancel downtime", "downtimes:write", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			d, _ := a.Store.GetDowntime(r.Context(), tenant, param(r, "id"))
			if err := a.Store.DeleteDowntime(r.Context(), tenant, param(r, "id")); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "downtime.delete", param(r, "id"), d, nil)
			a.refreshDowntimeDepths(r, tenant)
			w.WriteHeader(http.StatusNoContent)
		})

	// --- silences (TTL mandatory, SPEC §9.2) ---
	type silenceBody struct {
		Selector  string    `json:"selector,omitempty"`
		TextRegex string    `json:"textRegex,omitempty"`
		Comment   string    `json:"comment"`
		StartsAt  time.Time `json:"startsAt,omitempty"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	a.handle("POST /api/v1/silences", "Create silence (TTL mandatory)", "silences:write",
		silenceBody{}, model.Silence{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req silenceBody
			if !a.decode(w, r, &req) {
				return
			}
			if req.ExpiresAt.IsZero() {
				a.validationError(w, r, "expiresAt", "TTL is mandatory — no eternal silences")
				return
			}
			if req.ExpiresAt.After(time.Now().Add(90 * 24 * time.Hour)) {
				a.validationError(w, r, "expiresAt", "TTL exceeds the 90-day maximum")
				return
			}
			if req.Selector == "" && req.TextRegex == "" {
				a.validationError(w, r, "match", "selector or textRegex required")
				return
			}
			if req.Selector != "" {
				if _, err := selector.Parse(req.Selector); err != nil {
					a.validationError(w, r, "selector", err.Error())
					return
				}
			}
			if req.TextRegex != "" {
				if _, err := regexp.Compile(req.TextRegex); err != nil {
					a.validationError(w, r, "textRegex", err.Error())
					return
				}
			}
			si := &model.Silence{TenantID: a.tenantOf(r, p), Selector: req.Selector,
				TextRegex: req.TextRegex, Comment: req.Comment, CreatedBy: p.Name,
				StartsAt: req.StartsAt, ExpiresAt: req.ExpiresAt.UTC()}
			if err := a.Store.CreateSilence(r.Context(), si); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "silence.create", si.ID, nil, si)
			a.maintenanceEvent(r, si.TenantID, model.EventSilence, map[string]any{
				"silenceId": si.ID, "comment": si.Comment, "expiresAt": si.ExpiresAt})
			a.writeJSON(w, http.StatusCreated, si)
		})

	a.handle("GET /api/v1/silences", "List silences", "objects:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			sis, err := a.Store.ListSilences(r.Context(), a.tenantOf(r, p),
				r.URL.Query().Get("active") == "true")
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeList(w, sis, "")
		})

	a.handle("DELETE /api/v1/silences/{id}", "Expire silence early", "silences:write", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if err := a.Store.DeleteSilence(r.Context(), a.tenantOf(r, p), param(r, "id")); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "silence.delete", param(r, "id"), nil, nil)
			w.WriteHeader(http.StatusNoContent)
		})
}

// refreshDowntimeDepths recomputes check_state.downtime_depth for a
// tenant (called on downtime mutations and periodically by the janitor).
func (a *API) refreshDowntimeDepths(r *http.Request, tenantID string) {
	RefreshDowntimeDepths(r.Context(), a, tenantID)
}

func (a *API) maintenanceEvent(r *http.Request, tenantID string, typ model.EventType, payload map[string]any) {
	raw, _ := json.Marshal(payload)
	ev := &model.Event{ID: model.NewID(), TenantID: tenantID, TS: time.Now().UTC(),
		Type: typ, Severity: model.SevInfo, Payload: raw}
	_ = a.Store.InsertEvents(r.Context(), []*model.Event{ev})
	a.Bus.FanoutOnly(ev)
}

// errValidation lets idempotent() handlers signal 422s.
type validationErr struct{ code, detail string }

func (e validationErr) Error() string { return e.code + ": " + e.detail }

func errValidation(code, detail string) error { return validationErr{code, detail} }

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

func (a *API) registerOnCall() {
	a.resourceCRUD("schedules", storage.KindSchedule, "oncall", model.Schedule{})

	// who is on call now (SPEC §9.5: "Wer hat Dienst?"-Widget + API)
	type onCallNow struct {
		Schedule string              `json:"schedule"`
		Shifts   []model.OnCallShift `json:"shifts"`
		Contacts []*model.Contact    `json:"contacts"`
	}
	a.handle("GET /api/v1/oncall/now", "Who is on call", "oncall:read", nil, []onCallNow{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			want := r.URL.Query().Get("schedule")
			schedules, err := storage.LoadAll[model.Schedule](r.Context(), a.Store, tenant, storage.KindSchedule)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			overrides, _ := storage.LoadAll[model.Override](r.Context(), a.Store, tenant, storage.KindOverride)
			now := time.Now().UTC()
			var out []onCallNow
			for _, s := range schedules {
				if want != "" && s.Name != want {
					continue
				}
				shifts := model.ResolveOnCall(s, filterOverrides(overrides, s), now, 0)
				entry := onCallNow{Schedule: s.Name, Shifts: shifts}
				for _, sh := range shifts {
					if c, err := storage.LoadOne[model.Contact](r.Context(), a.Store, tenant,
						storage.KindContact, sh.ContactID); err == nil {
						entry.Contacts = append(entry.Contacts, c)
					}
				}
				out = append(out, entry)
			}
			a.writeJSON(w, http.StatusOK, out)
		})

	// timeline for calendar views (month/year, F-05.06)
	a.handle("GET /api/v1/schedules/{name}/timeline", "Resolved shifts for a range",
		"oncall:read", nil, []model.OnCallShift{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			s, err := storage.LoadOne[model.Schedule](r.Context(), a.Store, tenant,
				storage.KindSchedule, param(r, "name"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			from, to := rangeParams(r, 14*24*time.Hour)
			overrides, _ := storage.LoadAll[model.Override](r.Context(), a.Store, tenant, storage.KindOverride)
			tl := model.ScheduleTimeline(s, filterOverrides(overrides, s), from, to)
			a.writeJSON(w, http.StatusOK, tl)
		})

	// ICS calendar feed (SPEC §9.5) — token-authenticated like the API.
	a.handle("GET /api/v1/schedules/{name}/ics", "iCalendar feed", "oncall:read", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			s, err := storage.LoadOne[model.Schedule](r.Context(), a.Store, tenant,
				storage.KindSchedule, param(r, "name"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			from := time.Now().UTC().Add(-7 * 24 * time.Hour)
			to := time.Now().UTC().Add(60 * 24 * time.Hour)
			overrides, _ := storage.LoadAll[model.Override](r.Context(), a.Store, tenant, storage.KindOverride)
			tl := model.ScheduleTimeline(s, filterOverrides(overrides, s), from, to)
			ics := model.ICS(s, tl, func(id string) string { return a.contactName(r, tenant, id) })
			w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
			_, _ = w.Write([]byte(ics))
		})

	// overrides (vacation/swap, F-03.03)
	type overrideBody struct {
		Schedule  string    `json:"schedule"`
		ContactID string    `json:"contactId"`
		Start     time.Time `json:"start"`
		End       time.Time `json:"end"`
		Reason    string    `json:"reason,omitempty"`
	}
	a.handle("POST /api/v1/schedules/{name}/overrides", "Create override", "oncall:write",
		overrideBody{}, model.Override{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req overrideBody
			if !a.decode(w, r, &req) {
				return
			}
			if req.ContactID == "" || !req.End.After(req.Start) {
				a.validationError(w, r, "override", "contactId and a valid window required")
				return
			}
			tenant := a.tenantOf(r, p)
			schedName := param(r, "name")
			if _, err := storage.LoadOne[model.Schedule](r.Context(), a.Store, tenant,
				storage.KindSchedule, schedName); err != nil {
				a.fail(w, r, err)
				return
			}
			ov := model.Override{ID: model.NewID(), TenantID: tenant, ScheduleID: schedName,
				ContactID: req.ContactID, Start: req.Start.UTC(), End: req.End.UTC(),
				Reason: req.Reason, CreatedBy: p.Name, CreatedAt: time.Now().UTC()}
			if _, err := a.Store.PutResource(r.Context(), tenant, storage.KindOverride, ov.ID, ov, -1); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "override.create", ov.ID, nil, ov)
			a.writeJSON(w, http.StatusCreated, ov)
		})

	a.handle("DELETE /api/v1/schedules/{name}/overrides/{id}", "Delete override",
		"oncall:write", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			if err := a.Store.DeleteResource(r.Context(), tenant, storage.KindOverride, param(r, "id")); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "override.delete", param(r, "id"), nil, nil)
			w.WriteHeader(http.StatusNoContent)
		})

	// on-call statistics (F-03.08): hours per person incl. weekend share
	type statRow struct {
		ContactID string  `json:"contactId"`
		Contact   string  `json:"contact"`
		Hours     float64 `json:"hours"`
		Weekend   float64 `json:"weekendHours"`
		Overrides int     `json:"overrides"`
	}
	a.handle("GET /api/v1/schedules/{name}/stats", "On-call hours per person",
		"oncall:read", nil, []statRow{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			s, err := storage.LoadOne[model.Schedule](r.Context(), a.Store, tenant,
				storage.KindSchedule, param(r, "name"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			from, to := rangeParams(r, 30*24*time.Hour)
			overrides, _ := storage.LoadAll[model.Override](r.Context(), a.Store, tenant, storage.KindOverride)
			tl := model.ScheduleTimeline(s, filterOverrides(overrides, s), from, to)
			acc := map[string]*statRow{}
			for _, sh := range tl {
				row := acc[sh.ContactID]
				if row == nil {
					row = &statRow{ContactID: sh.ContactID,
						Contact: a.contactName(r, tenant, sh.ContactID)}
					acc[sh.ContactID] = row
				}
				row.Hours += sh.End.Sub(sh.Start).Hours()
				row.Weekend += weekendHours(sh.Start, sh.End)
				if sh.Override {
					row.Overrides++
				}
			}
			out := make([]statRow, 0, len(acc))
			for _, row := range acc {
				out = append(out, *row)
			}
			a.writeJSON(w, http.StatusOK, out)
		})
}

func weekendHours(start, end time.Time) float64 {
	var h float64
	for t := start; t.Before(end); t = t.Add(time.Hour) {
		wd := t.Weekday()
		if wd == time.Saturday || wd == time.Sunday {
			step := time.Hour
			if t.Add(step).After(end) {
				step = end.Sub(t)
			}
			h += step.Hours()
		}
	}
	return h
}

func rangeParams(r *http.Request, def time.Duration) (time.Time, time.Time) {
	to := time.Now().UTC()
	from := to.Add(-def)
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t.UTC()
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t.UTC()
		}
	}
	if v := r.URL.Query().Get("days"); v != "" {
		if n := queryInt(r, "days", 0); n > 0 {
			from = to.Add(-time.Duration(n) * 24 * time.Hour)
		}
	}
	return from, to
}

func (a *API) registerContacts() {
	a.resourceCRUD("contacts", storage.KindContact, "oncall", model.Contact{})
	a.resourceCRUD("contact-groups", storage.KindContactGroup, "oncall", model.ContactGroup{})
	a.resourceCRUD("channels", storage.KindChannel, "config", model.NotificationChannel{})

	// test-send (SPEC §12.3 admin: Kanäle Test-Senden)
	type testSendRequest struct {
		Target string `json:"target,omitempty"` // email/phone override
	}
	a.handle("POST /api/v1/channels/{name}:test-notification", "Send a test notification",
		"config:write", testSendRequest{}, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req testSendRequest
			_ = decodeOptional(r, &req)
			tenant := a.tenantOf(r, p)
			ch, err := storage.LoadOne[model.NotificationChannel](r.Context(), a.Store, tenant,
				storage.KindChannel, param(r, "name"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			result, err := a.Notify.TestSend(r.Context(), ch, req.Target, p.Name)
			if err != nil {
				a.problem(w, r, http.StatusBadGateway, "np:notify/test-failed",
					"test notification failed", err.Error())
				return
			}
			a.audit(r, p, "channel.test", ch.Name, nil, map[string]string{"result": result})
			a.writeJSON(w, http.StatusOK, map[string]string{"result": "sent", "detail": result})
		})
}

func decodeOptional(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	// Cap the body so a huge optional payload can't exhaust memory.
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(dst)
}

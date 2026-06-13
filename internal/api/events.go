package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/selector"
	"github.com/northplane/northplane/internal/storage"
	"github.com/northplane/northplane/internal/tsdb"
)

func (a *API) registerEvents() {
	eventFilters := []oaParam{
		{Name: "objectId", Desc: "Filter by monitored object id"},
		{Name: "sourceId", Desc: "Filter by ingress event-source id"},
		{Name: "severity", Desc: "Filter by severity (e.g. critical)"},
		{Name: "types", Desc: "Comma-separated event types"},
		{Name: "from", Desc: "RFC 3339 lower time bound"},
		{Name: "to", Desc: "RFC 3339 upper time bound"},
	}
	a.handle("GET /api/v1/events", "Search events", "events:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			f := a.eventFilter(r, p)
			events, err := a.Store.QueryEvents(r.Context(), f)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			next := ""
			if len(events) == f.Limit {
				next = events[len(events)-1].ID
			}
			a.writeList(w, events, next)
		}).Query(eventFilters...)

	// NDJSON export with cursor (SPEC §11.5: high-volume consumers).
	a.handle("GET /api/v1/events:export", "NDJSON event export", "events:read", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			f := a.eventFilter(r, p)
			f.Limit = 1000
			f.Asc = true
			w.Header().Set("Content-Type", "application/x-ndjson")
			enc := json.NewEncoder(w)
			count := 0
			for count < 100000 {
				events, err := a.Store.QueryEvents(r.Context(), f)
				if err != nil || len(events) == 0 {
					return
				}
				for _, e := range events {
					_ = enc.Encode(e)
					count++
				}
				last := events[len(events)-1]
				f.From = last.TS
				f.Cursor = "" // ascending export pages by time
				if len(events) < f.Limit {
					return
				}
			}
		}).Query(eventFilters...)

	// SSE stream (SPEC §7.6)
	a.handle("GET /api/v1/stream", "Server-Sent Events stream", "events:read", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			a.Hub.ServeHTTP(w, r, a.tenantOf(r, p))
		})
}

func (a *API) eventFilter(r *http.Request, p *auth.Principal) storage.EventFilter {
	q := r.URL.Query()
	f := storage.EventFilter{
		TenantID: a.tenantOf(r, p),
		ObjectID: q.Get("objectId"), SourceID: q.Get("sourceId"),
		Severity: q.Get("severity"), Cursor: q.Get("cursor"),
		Limit: queryInt(r, "limit", 200),
	}
	if t := q.Get("types"); t != "" {
		for _, x := range strings.Split(t, ",") {
			f.Types = append(f.Types, strings.TrimSpace(x))
		}
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = t
		}
	}
	return f
}

func (a *API) registerMetricsQuery() {
	// POST /metrics/query (SPEC §11.3): render-ready series.
	type metricsQuery struct {
		ObjectID  string    `json:"objectId,omitempty"`
		ObjectIDs []string  `json:"objectIds,omitempty"`
		Selector  string    `json:"selector,omitempty"`
		Metric    string    `json:"metric,omitempty"`
		From      time.Time `json:"from"`
		To        time.Time `json:"to"`
		StepSec   int       `json:"stepSeconds,omitempty"`
		MaxPoints int       `json:"maxPoints,omitempty"`
		Agg       string    `json:"agg,omitempty"`
	}
	a.handle("POST /api/v1/metrics/query", "Query time series (downsampled server-side)",
		"metrics:read", metricsQuery{}, []tsdb.Result{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var q metricsQuery
			if !a.decode(w, r, &q) {
				return
			}
			ids := q.ObjectIDs
			if q.ObjectID != "" {
				ids = append(ids, q.ObjectID)
			}
			if q.Selector != "" {
				sel, err := parseSelector(q.Selector)
				if err != nil {
					a.validationError(w, r, "selector", err.Error())
					return
				}
				for _, e := range a.Catalog.Select(a.tenantOf(r, p), sel) {
					ids = append(ids, e.Object.ID)
				}
			}
			if len(ids) == 0 {
				a.validationError(w, r, "target", "objectId(s) or selector required")
				return
			}
			if len(ids) > 100 {
				ids = ids[:100]
			}
			// tenant guard: every id must belong to the caller's tenant
			tenant := a.tenantOf(r, p)
			var owned []string
			for _, id := range ids {
				if e := a.Catalog.Get(id); e != nil && e.Object.TenantID == tenant {
					owned = append(owned, id)
				}
			}
			res, err := a.TSDB.Query(r.Context(), tsdb.Query{
				ObjectIDs: owned, Metric: q.Metric,
				From: q.From, To: q.To,
				Step:      time.Duration(q.StepSec) * time.Second,
				MaxPoints: q.MaxPoints, Agg: tsdb.AggFunc(q.Agg),
			})
			if err != nil {
				a.fail(w, r, err)
				return
			}
			if res == nil {
				res = []tsdb.Result{}
			}
			a.writeJSON(w, http.StatusOK, res)
		})

	// series metadata for chart pickers
	a.handle("GET /api/v1/objects/{id}/metrics", "List metric series of an object",
		"metrics:read", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			e := a.Catalog.Get(param(r, "id"))
			if e == nil || e.Object.TenantID != a.tenantOf(r, p) {
				a.problem(w, r, http.StatusNotFound, "np:not-found", "object not found", "")
				return
			}
			a.writeJSON(w, http.StatusOK, a.TSDB.SeriesForObject(e.Object.ID))
		})
}

func parseSelector(s string) (selector.Selector, error) { return selector.Parse(s) }

var _ = model.SevOK

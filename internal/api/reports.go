package api

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/selector"
	"github.com/northplane/northplane/internal/storage"
)

// Reports (SPEC §9.8): HTML-first, CSV always, PDF via the optional
// Chromium sidecar (ADR-11) — when configured, rendering posts the HTML
// to the sidecar; otherwise PDF requests get a clear 501.

func (a *API) registerReportsDashboards() {
	a.resourceCRUD("reports", storage.KindReport, "config", model.Report{})
	a.resourceCRUD("dashboards", storage.KindDashboard, "config", model.Dashboard{})
	a.resourceCRUD("saved-filters", storage.KindSavedFilter, "config", map[string]any{})

	a.handle("POST /api/v1/reports/{name}:render", "Render report (html|csv|json)",
		"reports:render", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			rep, err := storage.LoadOne[model.Report](r.Context(), a.Store, tenant,
				storage.KindReport, param(r, "name"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			format := r.URL.Query().Get("format")
			if format == "" {
				format = "html"
			}
			data, err := a.buildReport(r, tenant, rep)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "report.render", rep.Name, nil, map[string]string{"format": format})
			switch format {
			case "json":
				a.writeJSON(w, http.StatusOK, data)
			case "csv":
				w.Header().Set("Content-Type", "text/csv; charset=utf-8")
				w.Header().Set("Content-Disposition",
					`attachment; filename="`+rep.Name+`.csv"`)
				writeReportCSV(w, data)
			case "pdf":
				a.problem(w, r, http.StatusNotImplemented, "np:reports/pdf",
					"PDF rendering needs the optional Chromium sidecar (ADR-11)",
					"render html and print, or deploy the renderer container")
			default:
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				if err := reportHTML.Execute(w, data); err != nil {
					a.Log.Error("report render", "err", err)
				}
			}
		})

	// Scheduled-report archive (SPEC §9.8): list past renders and download
	// one. Listing is read-only (objects:read, the report-read baseline).
	a.handle("GET /api/v1/reports/{name}/archive", "List archived report renders",
		"objects:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			// validate the report exists so a typo'd name 404s, not [].
			if _, err := storage.LoadOne[model.Report](r.Context(), a.Store, tenant,
				storage.KindReport, param(r, "name")); err != nil {
				a.fail(w, r, err)
				return
			}
			entries, err := a.Store.ListReportArchive(r.Context(), tenant,
				param(r, "name"), queryInt(r, "limit", 100))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeList(w, entries, "")
		})

	a.handle("GET /api/v1/reports/{name}/archive/{id}", "Download an archived report render",
		"objects:read", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			e, err := a.Store.GetReportArchive(r.Context(), tenant, param(r, "id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			// guard cross-report id access: the path name must match.
			if e.ReportName != param(r, "name") {
				a.problem(w, r, http.StatusNotFound, "np:not-found", "resource not found", "")
				return
			}
			ct, ext := "application/octet-stream", "bin"
			switch e.Format {
			case "html":
				ct, ext = "text/html; charset=utf-8", "html"
			case "csv":
				ct, ext = "text/csv; charset=utf-8", "csv"
			case "json":
				ct, ext = "application/json; charset=utf-8", "json"
			}
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Content-Disposition",
				fmt.Sprintf(`attachment; filename=%q`, e.ReportName+"-"+e.Slot+"."+ext))
			_, _ = w.Write(e.Content)
		})

	// Force an immediate render+archive+send, independent of the schedule
	// (CMP "jetzt erzeugen"). A configuration action ⇒ config:write + audit.
	a.handle("POST /api/v1/reports/{name}:run", "Render, archive and e-mail a report now",
		"config:write", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			rep, err := storage.LoadOne[model.Report](r.Context(), a.Store, tenant,
				storage.KindReport, param(r, "name"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			now := time.Now()
			// a manual run is its own slot so it never collides with the
			// scheduled slot's dedup gate.
			slot := "manual-" + now.UTC().Format("2006-01-02T15:04:05")
			res, err := a.runReportOnce(r.Context(), tenant, rep, slot, now)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "report.run", rep.Name, nil, res)
			a.writeJSON(w, http.StatusOK, res)
		})
}

// ReportData is the render model for all report types.
type ReportData struct {
	Title       string            `json:"title"`
	Type        model.ReportType  `json:"type"`
	GeneratedAt time.Time         `json:"generatedAt"`
	WindowDays  int               `json:"windowDays"`
	Rows        []ReportRow       `json:"rows"`
	Totals      map[string]string `json:"totals,omitempty"`
}

// ReportRow is one line.
type ReportRow struct {
	Name   string            `json:"name"`
	Values map[string]string `json:"values"`
	Class  string            `json:"class,omitempty"` // ok|warn|crit for styling
}

type reportParams struct {
	Selector         string  `json:"selector,omitempty"`
	WindowDays       int     `json:"windowDays,omitempty"`
	Target           float64 `json:"target,omitempty"`
	IncludeDowntimes bool    `json:"includeDowntimes,omitempty"`
	// Channel optionally names the e-mail NotificationChannel scheduled
	// delivery uses (SPEC §9.8); empty = first enabled e-mail channel.
	Channel string `json:"channel,omitempty"`
}

// buildReport is the thin HTTP wrapper: it forwards the request context
// and query values to the request-independent core. Query overrides are a
// future extension point (CMP-Reports allow interactive parameter
// tweaks); scheduled runs call buildReportData with rep.Params only.
func (a *API) buildReport(r *http.Request, tenantID string, rep *model.Report) (*ReportData, error) {
	return a.buildReportData(r.Context(), tenantID, rep, r.URL.Query())
}

// RenderReportJSON implements the ai.ReportRenderer hook: the
// render_report MCP tool renders a stored report's data model on demand
// (SPEC §10.3) — structured JSON, the AI-friendly format.
func (a *API) RenderReportJSON(ctx context.Context, tenantID, name string) (any, error) {
	rep, err := storage.LoadOne[model.Report](ctx, a.Store, tenantID, storage.KindReport, name)
	if err != nil {
		return nil, err
	}
	return a.buildReportData(ctx, tenantID, rep, url.Values{})
}

// buildReportData renders a report's data model from its stored params.
// It takes no *http.Request so scheduled runs (SPEC §9.8) and the HTTP
// :render endpoint share one code path. q carries optional query overrides
// for the interactive endpoint; an empty url.Values yields the stored
// definition unchanged.
func (a *API) buildReportData(ctx context.Context, tenantID string, rep *model.Report, q url.Values) (*ReportData, error) {
	var params reportParams
	if len(rep.Params) > 0 {
		_ = json.Unmarshal(rep.Params, &params)
	}
	if params.WindowDays <= 0 {
		params.WindowDays = 30
	}
	data := &ReportData{Title: rep.Name, Type: rep.Type,
		GeneratedAt: time.Now().UTC(), WindowDays: params.WindowDays,
		Totals: map[string]string{}}
	from := time.Now().UTC().AddDate(0, 0, -params.WindowDays)
	to := time.Now().UTC()

	switch rep.Type {
	case model.ReportAvailability, model.ReportSLA:
		sel, err := selector.Parse(params.Selector)
		if err != nil {
			return nil, err
		}
		entries := a.Catalog.Select(tenantID, sel)
		if len(entries) > 500 {
			entries = entries[:500]
		}
		var sumAvail float64
		for _, e := range entries {
			down, err := ObjectDowntime(ctx, a.Store, tenantID, e.Object.ID, from, to)
			if err != nil {
				return nil, err
			}
			window := to.Sub(from)
			avail := 100 * (1 - float64(down)/float64(window))
			sumAvail += avail
			class := "ok"
			target := params.Target
			if target <= 0 {
				target = 99.9
			}
			if avail < target {
				class = "crit"
			} else if avail < target+(100-target)/2 {
				class = "warn"
			}
			data.Rows = append(data.Rows, ReportRow{
				Name: e.Object.Name, Class: class,
				Values: map[string]string{
					"availability": fmt.Sprintf("%.3f%%", avail),
					"downtime":     down.Round(time.Minute).String(),
				}})
		}
		if len(entries) > 0 {
			data.Totals["avgAvailability"] = fmt.Sprintf("%.3f%%", sumAvail/float64(len(entries)))
		}
	case model.ReportAlertStats:
		alerts, err := a.Store.ListAlerts(ctx, storage.AlertFilter{
			TenantID: tenantID, Since: from, Limit: 1000})
		if err != nil {
			return nil, err
		}
		var mtta, mttr time.Duration
		var ackCount, resolveCount int
		byTitle := map[string]int{}
		for _, alert := range alerts {
			byTitle[alert.Title]++
			if alert.AckedAt != nil {
				mtta += alert.AckedAt.Sub(alert.OpenedAt)
				ackCount++
			}
			if alert.ResolvedAt != nil {
				mttr += alert.ResolvedAt.Sub(alert.OpenedAt)
				resolveCount++
			}
		}
		data.Totals["alerts"] = strconv.Itoa(len(alerts))
		if ackCount > 0 {
			data.Totals["mtta"] = (mtta / time.Duration(ackCount)).Round(time.Second).String()
		}
		if resolveCount > 0 {
			data.Totals["mttr"] = (mttr / time.Duration(resolveCount)).Round(time.Second).String()
		}
		type kv struct {
			k string
			n int
		}
		var top []kv
		for k, n := range byTitle {
			top = append(top, kv{k, n})
		}
		sort.Slice(top, func(i, j int) bool { return top[i].n > top[j].n })
		for i, t := range top {
			if i >= 20 {
				break
			}
			data.Rows = append(data.Rows, ReportRow{Name: t.k,
				Values: map[string]string{"count": strconv.Itoa(t.n)}})
		}
	case model.ReportAudit:
		// permission report (A-15.07): roles and their members/tokens
		roles, err := storage.LoadAll[model.Role](ctx, a.Store, tenantID, storage.KindRole)
		if err != nil {
			return nil, err
		}
		for _, role := range roles {
			perms, _ := json.Marshal(role.Permissions)
			data.Rows = append(data.Rows, ReportRow{Name: role.Name,
				Values: map[string]string{
					"permissions": string(perms),
					"idpGroups":   fmt.Sprint(role.IdPGroups),
					"includes":    fmt.Sprint(role.Includes),
				}})
		}
	case model.ReportOnCall:
		schedules, err := storage.LoadAll[model.Schedule](ctx, a.Store, tenantID, storage.KindSchedule)
		if err != nil {
			return nil, err
		}
		overrides, _ := storage.LoadAll[model.Override](ctx, a.Store, tenantID, storage.KindOverride)
		for _, s := range schedules {
			tl := model.ScheduleTimeline(s, filterOverrides(overrides, s), from, to)
			hours := map[string]float64{}
			for _, sh := range tl {
				hours[sh.ContactID] += sh.End.Sub(sh.Start).Hours()
			}
			for contactID, h := range hours {
				data.Rows = append(data.Rows, ReportRow{
					Name:   s.Name + " / " + a.contactNameCtx(ctx, tenantID, contactID),
					Values: map[string]string{"hours": fmt.Sprintf("%.1f", h)},
				})
			}
		}
	default:
		return nil, fmt.Errorf("unknown report type %q", rep.Type)
	}
	return data, nil
}

// contactNameCtx resolves a contact's display name from a bare context
// (the request-independent report core, vs. the request-bound contactName
// used by interactive handlers).
func (a *API) contactNameCtx(ctx context.Context, tenantID, ref string) string {
	c, err := storage.LoadOne[model.Contact](ctx, a.Store, tenantID, storage.KindContact, ref)
	if err != nil {
		return ref
	}
	return c.Name
}

// renderReportHTML renders the report's print-optimised HTML to bytes.
func renderReportHTML(data *ReportData) ([]byte, error) {
	var buf bytes.Buffer
	if err := reportHTML.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// renderReportCSV renders the report's CSV to bytes.
func renderReportCSV(data *ReportData) []byte {
	var buf bytes.Buffer
	writeReportCSV(&buf, data)
	return buf.Bytes()
}

func writeReportCSV(w io.Writer, data *ReportData) {
	cw := csv.NewWriter(w)
	// stable column order
	cols := map[string]bool{}
	for _, row := range data.Rows {
		for k := range row.Values {
			cols[k] = true
		}
	}
	var header []string
	for k := range cols {
		header = append(header, k)
	}
	sort.Strings(header)
	_ = cw.Write(append([]string{"name"}, header...))
	for _, row := range data.Rows {
		rec := []string{row.Name}
		for _, k := range header {
			rec = append(rec, row.Values[k])
		}
		_ = cw.Write(rec)
	}
	cw.Flush()
}

// reportHTML is the print-optimised template (SPEC §9.8 HTML-first).
var reportHTML = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="de"><head><meta charset="utf-8">
<title>{{.Title}} — Northplane Report</title>
<style>
body{font-family:system-ui,-apple-system,sans-serif;margin:2rem auto;max-width:60rem;color:#111}
h1{font-size:1.4rem;border-bottom:2px solid #111;padding-bottom:.5rem}
table{border-collapse:collapse;width:100%;margin-top:1rem;font-size:.9rem}
th,td{text-align:left;padding:.4rem .6rem;border-bottom:1px solid #ddd}
th{background:#f5f5f5}
tr.crit td{background:#fee2e2}
tr.warn td{background:#fef3c7}
.meta{color:#666;font-size:.85rem}
.totals{margin-top:1rem;padding:.8rem;background:#f5f5f5;font-size:.9rem}
@media print{body{margin:0}}
</style></head><body>
<h1>{{.Title}}</h1>
<p class="meta">Typ: {{.Type}} · Zeitraum: {{.WindowDays}} Tage · Erstellt: {{.GeneratedAt.Format "2006-01-02 15:04 MST"}} · Northplane</p>
{{if .Rows}}<table><thead><tr><th>Name</th>
{{$first := index .Rows 0}}{{range $k, $v := $first.Values}}<th>{{$k}}</th>{{end}}
</tr></thead><tbody>
{{range .Rows}}<tr class="{{.Class}}"><td>{{.Name}}</td>{{range $k, $v := .Values}}<td>{{$v}}</td>{{end}}</tr>
{{end}}</tbody></table>{{else}}<p>Keine Daten im Zeitraum.</p>{{end}}
{{if .Totals}}<div class="totals">{{range $k, $v := .Totals}}<strong>{{$k}}</strong>: {{$v}} &nbsp; {{end}}</div>{{end}}
</body></html>`))

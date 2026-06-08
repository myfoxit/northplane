package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/selector"
	"github.com/northplane/northplane/internal/storage"
	"github.com/northplane/northplane/internal/tsdb"
)

// Tool surface (SPEC §10.3): the same set is exposed to the in-UI
// assistant and the MCP server. Read tools execute directly; mutating
// tools route through the approval gate (propose unless policy auto).

// Tool is a registered capability.
type Tool struct {
	Def      ToolDef
	Mutating bool
	AutoOK   bool // policy may set this to auto-execute (ack, short downtime)
	// Perm is the RBAC permission the caller must hold — the same scope
	// the equivalent REST route requires (SPEC §10.3: the AI/MCP session
	// is a privilegeless client that inherits exactly the token's scopes).
	Perm model.Permission
	// Run executes the tool against the platform with the principal's RBAC.
	Run func(ctx context.Context, s *Service, p *auth.Principal, input json.RawMessage) (any, error)
}

// --- tool input types ---
//
// Each tool's input is a typed struct: the single source of truth for both
// the advertised JSON Schema (via reflectSchema) and the args the Run
// closure unmarshals. `json` tags fix property names + required-ness,
// `desc` tags carry the per-property descriptions, and `enum` tags
// (pipe-separated) carry the allowed values the MCP/AI surface relies on.

type emptyInput struct{}

type searchObjectsInput struct {
	Selector string `json:"selector,omitempty" desc:"Label selector (e.g. env=prod,role!=db) to filter objects."`
	Query    string `json:"query,omitempty" desc:"Free-text match against object name/labels."`
	Kind     string `json:"kind,omitempty" desc:"Restrict to a single object kind." enum:"host|service"`
	Limit    int    `json:"limit,omitempty" desc:"Maximum number of objects to return (capped at 100, default 50)."`
}

type getObjectInput struct {
	ID string `json:"id" desc:"ID of the object to fetch."`
}

type queryMetricsInput struct {
	ObjectID     string  `json:"objectId" desc:"ID of the object whose metrics to query."`
	Metric       string  `json:"metric,omitempty" desc:"Metric name to query; empty returns all of the object's metrics."`
	FromHoursAgo float64 `json:"fromHoursAgo,omitempty" desc:"Look-back window in hours (default 24)."`
	Agg          string  `json:"agg,omitempty" desc:"Bucket aggregation function (default avg)." enum:"avg|min|max|sum|last|count"`
}

type getAlertsInput struct {
	Status string `json:"status,omitempty" desc:"Alert lifecycle filter; defaults to open+acked when omitted." enum:"open|acked|resolved|expired"`
	Limit  int    `json:"limit,omitempty" desc:"Maximum number of alerts to return."`
}

type analyzeMetricInput struct {
	ObjectID string  `json:"objectId" desc:"ID of the object whose metric to analyze."`
	Metric   string  `json:"metric,omitempty" desc:"Metric name on the object (e.g. cpu, mem, disk). Empty analyzes the object's only/first series."`
	Hours    float64 `json:"hours,omitempty" desc:"Look-back window in hours used to build the baseline (default 168 = 4 weeks of seasonality is approximated from this window)."`
}

type forecastCapacityInput struct {
	ObjectID     string  `json:"objectId" desc:"ID of the object whose metric to forecast."`
	Metric       string  `json:"metric,omitempty" desc:"Metric name on the object (e.g. disk, mem). Empty forecasts the object's only/first series."`
	Threshold    float64 `json:"threshold" desc:"Value whose crossing time to project (e.g. 100 for a percentage-full disk)."`
	HorizonHours float64 `json:"horizonHours,omitempty" desc:"Look-back window in hours used to fit the trend (default 168)."`
}

type suggestThresholdsInput struct {
	ObjectID string  `json:"objectId" desc:"ID of the object whose metric to analyze."`
	Metric   string  `json:"metric,omitempty" desc:"Metric name on the object. Empty uses the object's only/first series."`
	Hours    float64 `json:"hours,omitempty" desc:"Look-back window in hours over which to compute the distribution (default 168)."`
}

type getIncidentsInput struct {
	Open bool `json:"open,omitempty" desc:"When true, only open incidents are returned."`
}

type whoIsOncallInput struct {
	Schedule string `json:"schedule,omitempty" desc:"Restrict to a single schedule by name; empty returns all schedules."`
}

type explainAlertInput struct {
	AlertID string `json:"alertId" desc:"ID of the alert to explain."`
}

type runCheckNowInput struct {
	ObjectID string `json:"objectId" desc:"ID of the object to recheck immediately."`
}

type acknowledgeAlertInput struct {
	AlertID string `json:"alertId" desc:"ID of the alert to acknowledge."`
	Comment string `json:"comment,omitempty" desc:"Optional note recorded with the acknowledgement."`
}

type createDowntimeInput struct {
	ObjectID string  `json:"objectId,omitempty" desc:"ID of the object to put into downtime (use this or selector)."`
	Selector string  `json:"selector,omitempty" desc:"Label selector matching the objects to put into downtime (use this or objectId)."`
	Hours    float64 `json:"hours,omitempty" desc:"Downtime duration in hours (default 2; capped by the AI policy limit)."`
	Comment  string  `json:"comment" desc:"Reason for the downtime (required, recorded in the audit trail)."`
}

type createSilenceInput struct {
	Selector string  `json:"selector" desc:"Label selector matching the alerts to silence."`
	Hours    float64 `json:"hours,omitempty" desc:"Silence duration in hours (default 1)."`
	Comment  string  `json:"comment" desc:"Reason for the silence (required, recorded in the audit trail)."`
}

type proposeConfigChangeInput struct {
	BundleYaml string `json:"bundleYaml" desc:"YAML configuration bundle to validate and diff (dry-run only)."`
}

type applyConfigChangeInput struct {
	BundleYaml string `json:"bundleYaml" desc:"YAML configuration bundle to apply once approved."`
}

type renderReportInput struct {
	Name string `json:"name" desc:"Name of the stored report to render."`
}

// tools is the canonical registry.
func buildTools() []Tool {
	return []Tool{
		{Def: ToolDef{Name: "get_overview",
			Description: "State summary: problem counts, open incidents, on-call.",
			Schema:      reflectSchema(emptyInput{})},
			Perm: model.Permission("events:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				sum, err := s.store.Summary(ctx, p.TenantID)
				if err != nil {
					return nil, err
				}
				alerts, _ := s.store.OpenAlertStats(ctx, p.TenantID)
				incidents, _ := s.store.ListIncidents(ctx, p.TenantID, true, "", 10)
				return map[string]any{"summary": sum, "openAlerts": alerts, "openIncidents": incidents}, nil
			}},

		{Def: ToolDef{Name: "search_objects",
			Description: "Find hosts/services by label selector or text query.",
			Schema:      reflectSchema(searchObjectsInput{})},
			Perm: model.Permission("objects:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args searchObjectsInput
				_ = json.Unmarshal(in, &args)
				sel, err := selector.Parse(args.Selector)
				if err != nil {
					return nil, err
				}
				if args.Limit == 0 || args.Limit > 100 {
					args.Limit = 50
				}
				objs, err := s.store.ListObjects(ctx, storage.ObjectFilter{
					TenantID: p.TenantID, Kind: model.Kind(args.Kind), Selector: sel,
					Query: args.Query, Limit: args.Limit})
				if err != nil {
					return nil, err
				}
				out := make([]map[string]any, 0, len(objs))
				for _, o := range objs {
					cs, _ := s.store.GetCheckState(ctx, o.ID)
					state := "PENDING"
					if cs != nil {
						state = cs.State.Label(o.Kind)
					}
					out = append(out, map[string]any{"id": o.ID, "name": o.Name,
						"kind": o.Kind, "labels": o.Labels, "state": state})
				}
				return out, nil
			}},

		{Def: ToolDef{Name: "get_object",
			Description: "Object detail incl. effective config, state and metric series.",
			Schema:      reflectSchema(getObjectInput{})},
			Perm: model.Permission("objects:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args getObjectInput
				_ = json.Unmarshal(in, &args)
				obj, err := s.store.GetObject(ctx, p.TenantID, args.ID)
				if err != nil {
					return nil, err
				}
				cs, _ := s.store.GetCheckState(ctx, obj.ID)
				entry := s.cat.Get(obj.ID)
				resp := map[string]any{"object": obj, "state": cs}
				if entry != nil {
					resp["effectiveConfig"] = entry.Effective
					resp["templateChain"] = entry.Chain
				}
				resp["metrics"] = s.tsdb.SeriesForObject(obj.ID)
				return resp, nil
			}},

		{Def: ToolDef{Name: "query_metrics",
			Description: "Aggregated, downsampled time-series for an object/metric.",
			Schema:      reflectSchema(queryMetricsInput{})},
			Perm: model.Permission("metrics:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args queryMetricsInput
				_ = json.Unmarshal(in, &args)
				if args.FromHoursAgo == 0 {
					args.FromHoursAgo = 24
				}
				if e := s.cat.Get(args.ObjectID); e == nil || e.Object.TenantID != p.TenantID {
					return nil, fmt.Errorf("object not found")
				}
				return s.tsdb.Query(ctx, tsdb.Query{ObjectID: args.ObjectID, Metric: args.Metric,
					From: time.Now().Add(-time.Duration(args.FromHoursAgo * float64(time.Hour))),
					To:   time.Now(), Agg: tsdb.AggFunc(args.Agg), MaxPoints: 100})
			}},

		{Def: ToolDef{Name: "get_alerts",
			Description: "List alerts filtered by status/severity.",
			Schema:      reflectSchema(getAlertsInput{})},
			Perm: model.Permission("alerts:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args getAlertsInput
				_ = json.Unmarshal(in, &args)
				f := storage.AlertFilter{TenantID: p.TenantID, Limit: args.Limit}
				if args.Status != "" {
					f.Status = []model.AlertStatus{model.AlertStatus(args.Status)}
				} else {
					f.Status = []model.AlertStatus{model.AlertOpen, model.AlertAcked}
				}
				return s.store.ListAlerts(ctx, f)
			}},

		{Def: ToolDef{Name: "analyze_metric",
			Description: "Deterministic statistics (no LLM) for an object/metric: seasonal baseline plus MAD-based anomaly detection. Returns the current value, baseline mean/σ, whether the latest sample is anomalous, its deviation in σ, and the length of any ongoing anomalous run (SPEC §10.6).",
			Schema:      reflectSchema(analyzeMetricInput{})},
			Perm: model.Permission("metrics:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args analyzeMetricInput
				_ = json.Unmarshal(in, &args)
				if args.Hours <= 0 {
					args.Hours = 168
				}
				points, meta, err := s.fetchSeries(ctx, p, args.ObjectID, args.Metric, args.Hours)
				if err != nil {
					return nil, err
				}
				if len(points) == 0 {
					return nil, fmt.Errorf("no samples for object %q metric %q in the last %.0fh", args.ObjectID, args.Metric, args.Hours)
				}
				baseline := ComputeBaseline(points)
				anomalies := DetectAnomalies(points, baseline, 0, 0)
				last := points[len(points)-1]
				lastT := time.UnixMilli(last.T).UTC()
				expected := baseline.SeasonalExpected(lastT)
				mad := baseline.MAD
				if mad < 1e-9 {
					mad = baseline.StdDev
				}
				deviationSigma := 0.0
				if mad > 1e-9 {
					deviationSigma = (last.V - expected) / mad
				}
				// Trailing anomalous-run length: count consecutive samples from
				// the end of the window that deviate beyond the same k×MAD gate
				// the detector uses (k=5 default). 0 ⇒ the series is currently
				// back inside its baseline band.
				const k = 5.0
				runLen := 0
				for i := len(points) - 1; i >= 0 && mad > 1e-9; i-- {
					pt := points[i]
					exp := baseline.SeasonalExpected(time.UnixMilli(pt.T).UTC())
					if math.Abs(pt.V-exp)/mad > k {
						runLen++
					} else {
						break
					}
				}
				return map[string]any{
					"objectId":          args.ObjectID,
					"metric":            meta.Metric,
					"samples":           len(points),
					"currentValue":      last.V,
					"baselineMean":      baseline.Mean,
					"baselineStdDev":    baseline.StdDev,
					"baselineMad":       baseline.MAD,
					"seasonalExpected":  expected,
					"deviationSigma":    deviationSigma,
					"anomalous":         runLen > 0,
					"anomalousRunLen":   runLen,
					"totalAnomalyCount": len(anomalies),
				}, nil
			}},

		{Def: ToolDef{Name: "forecast_capacity",
			Description: "Deterministic capacity forecast (no LLM): fits a least-squares trend to an object/metric and projects when it reaches a threshold (e.g. \"disk full in ~9 days\"). Returns slope/hour, the projected current value, the projected exhaustion time and the fit confidence (R²) (SPEC §10.6).",
			Schema:      reflectSchema(forecastCapacityInput{})},
			Perm: model.Permission("metrics:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args forecastCapacityInput
				_ = json.Unmarshal(in, &args)
				if args.HorizonHours <= 0 {
					args.HorizonHours = 168
				}
				points, meta, err := s.fetchSeries(ctx, p, args.ObjectID, args.Metric, args.HorizonHours)
				if err != nil {
					return nil, err
				}
				if len(points) < 10 {
					return nil, fmt.Errorf("need at least 10 samples to forecast, have %d", len(points))
				}
				f := ComputeForecast(points, args.Threshold)
				out := map[string]any{
					"objectId":       args.ObjectID,
					"metric":         meta.Metric,
					"samples":        len(points),
					"threshold":      args.Threshold,
					"slopePerHour":   f.SlopePerHour,
					"projectedValue": f.Projected,
					"confidenceR2":   f.Confidence,
				}
				if f.HitsThreshold != nil {
					out["projectedExhaustionAt"] = f.HitsThreshold.UTC().Format(time.RFC3339)
					out["hoursToThreshold"] = time.Until(*f.HitsThreshold).Hours()
				} else {
					out["projectedExhaustionAt"] = nil
					out["note"] = "threshold not reached within the projection horizon (flat/declining trend or > 1y out)"
				}
				return out, nil
			}},

		{Def: ToolDef{Name: "suggest_thresholds",
			Description: "Deterministic threshold suggestion (no LLM): derives warn/crit from the observed distribution (P98/P99.5 quantiles) of an object/metric. Use to replace guessed thresholds with data-driven ones (SPEC §10.6).",
			Schema:      reflectSchema(suggestThresholdsInput{})},
			Perm: model.Permission("metrics:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args suggestThresholdsInput
				_ = json.Unmarshal(in, &args)
				if args.Hours <= 0 {
					args.Hours = 168
				}
				points, meta, err := s.fetchSeries(ctx, p, args.ObjectID, args.Metric, args.Hours)
				if err != nil {
					return nil, err
				}
				if len(points) < 20 {
					return nil, fmt.Errorf("need at least 20 samples to suggest thresholds, have %d", len(points))
				}
				th := ComputeAdaptiveThresholds(points, 0, 0)
				return map[string]any{
					"objectId":      args.ObjectID,
					"metric":        meta.Metric,
					"samples":       len(points),
					"suggestedWarn": th.Warn,
					"suggestedCrit": th.Crit,
					"basis":         "P98 (warn) / P99.5 (crit) of the observed distribution",
				}, nil
			}},

		{Def: ToolDef{Name: "get_incidents",
			Description: "List incidents with their alerts.",
			Schema:      reflectSchema(getIncidentsInput{})},
			Perm: model.Permission("incidents:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args getIncidentsInput
				_ = json.Unmarshal(in, &args)
				return s.store.ListIncidents(ctx, p.TenantID, args.Open, "", 50)
			}},

		{Def: ToolDef{Name: "who_is_oncall",
			Description: "Current on-call per schedule.",
			Schema:      reflectSchema(whoIsOncallInput{})},
			Perm: model.Permission("oncall:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args whoIsOncallInput
				_ = json.Unmarshal(in, &args)
				schedules, err := storage.LoadAll[model.Schedule](ctx, s.store, p.TenantID, storage.KindSchedule)
				if err != nil {
					return nil, err
				}
				overrides, _ := storage.LoadAll[model.Override](ctx, s.store, p.TenantID, storage.KindOverride)
				now := time.Now().UTC()
				out := map[string][]string{}
				for _, sched := range schedules {
					if args.Schedule != "" && sched.Name != args.Schedule {
						continue
					}
					var ovs []model.Override
					for _, o := range overrides {
						if o.ScheduleID == sched.ID || o.ScheduleID == sched.Name {
							ovs = append(ovs, *o)
						}
					}
					for _, sh := range model.ResolveOnCall(sched, ovs, now, 0) {
						name := sh.ContactID
						if c, err := storage.LoadOne[model.Contact](ctx, s.store, p.TenantID, storage.KindContact, sh.ContactID); err == nil {
							name = c.Name
						}
						out[sched.Name] = append(out[sched.Name], name)
					}
				}
				return out, nil
			}},

		{Def: ToolDef{Name: "explain_alert",
			Description: "Deterministic context for an alert: topology, recent config changes, similar past incidents. Structured data for grounding an explanation.",
			Schema:      reflectSchema(explainAlertInput{})},
			Perm: model.Permission("alerts:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args explainAlertInput
				_ = json.Unmarshal(in, &args)
				return s.explainAlert(ctx, p.TenantID, args.AlertID)
			}},

		{Def: ToolDef{Name: "run_check_now",
			Description: "Trigger an immediate recheck.",
			Schema:      reflectSchema(runCheckNowInput{})},
			Perm:     model.Permission("checks:run"),
			Mutating: true, AutoOK: true,
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args runCheckNowInput
				_ = json.Unmarshal(in, &args)
				if e := s.cat.Get(args.ObjectID); e == nil || e.Object.TenantID != p.TenantID {
					return nil, fmt.Errorf("object not found")
				}
				s.sched.CheckNow(args.ObjectID)
				return map[string]string{"status": "queued"}, nil
			}},

		{Def: ToolDef{Name: "acknowledge_alert",
			Description: "Acknowledge an alert, stopping its escalation.",
			Schema:      reflectSchema(acknowledgeAlertInput{})},
			Perm:     model.Permission("alerts:ack"),
			Mutating: true, AutoOK: true,
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args acknowledgeAlertInput
				_ = json.Unmarshal(in, &args)
				alert, err := s.store.AckAlert(ctx, p.TenantID, args.AlertID, "ai:"+p.Name)
				if err != nil {
					return nil, err
				}
				_ = s.escal.StopChain(ctx, alert.ID)
				return map[string]string{"status": "acked", "alert": alert.Title}, nil
			}},

		{Def: ToolDef{Name: "create_downtime",
			Description: "Schedule a downtime window (TTL-limited by policy).",
			Schema:      reflectSchema(createDowntimeInput{})},
			Perm:     model.Permission("downtimes:write"),
			Mutating: true,
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args createDowntimeInput
				_ = json.Unmarshal(in, &args)
				// Match the human REST path (maintenance.go): a downtime
				// must target something — reject an unscoped window.
				if args.ObjectID == "" && args.Selector == "" {
					return nil, fmt.Errorf("create_downtime requires objectId or selector")
				}
				if args.Hours <= 0 {
					args.Hours = 2
				}
				if args.Hours > s.maxDowntimeHours {
					return nil, fmt.Errorf("downtime exceeds the %.0fh AI limit", s.maxDowntimeHours)
				}
				d := &model.Downtime{TenantID: p.TenantID, ObjectID: args.ObjectID,
					Selector: args.Selector, Type: model.DowntimeFixed,
					Start: time.Now().UTC(), End: time.Now().UTC().Add(time.Duration(args.Hours * float64(time.Hour))),
					Comment: args.Comment, CreatedBy: "ai:" + p.Name}
				if err := s.store.CreateDowntime(ctx, d); err != nil {
					return nil, err
				}
				return map[string]string{"status": "scheduled", "id": d.ID}, nil
			}},

		{Def: ToolDef{Name: "create_silence",
			Description: "Silence matching alerts for a bounded TTL.",
			Schema:      reflectSchema(createSilenceInput{})},
			Perm:     model.Permission("silences:write"),
			Mutating: true,
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args createSilenceInput
				_ = json.Unmarshal(in, &args)
				if args.Hours <= 0 {
					args.Hours = 1
				}
				si := &model.Silence{TenantID: p.TenantID, Selector: args.Selector,
					Comment: args.Comment, CreatedBy: "ai:" + p.Name,
					StartsAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Duration(args.Hours * float64(time.Hour)))}
				if err := s.store.CreateSilence(ctx, si); err != nil {
					return nil, err
				}
				return map[string]string{"status": "silenced", "id": si.ID}, nil
			}},

		{Def: ToolDef{Name: "propose_config_change",
			Description: "Produce a validated bundle diff (dry-run). Always returns a plan for human approval — never applies.",
			Schema:      reflectSchema(proposeConfigChangeInput{})},
			Perm:     model.Permission("config:write"),
			Mutating: true,
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args proposeConfigChangeInput
				_ = json.Unmarshal(in, &args)
				return s.planBundle(ctx, p, args.BundleYaml)
			}},

		{Def: ToolDef{Name: "apply_config_change",
			Description: "Apply a configuration bundle. Queued for human approval; once approved the diff is applied atomically (SPEC §10.3).",
			Schema:      reflectSchema(applyConfigChangeInput{})},
			Perm:     model.Permission("config:write"),
			Mutating: true, // not AutoOK: rides the approval queue by design
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args applyConfigChangeInput
				_ = json.Unmarshal(in, &args)
				return s.applyBundle(ctx, p, args.BundleYaml)
			}},

		{Def: ToolDef{Name: "render_report",
			Description: "Render a stored report on demand (availability/SLA/top-N) as structured JSON.",
			Schema:      reflectSchema(renderReportInput{})},
			Perm: model.Permission("reports:render"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args renderReportInput
				_ = json.Unmarshal(in, &args)
				if args.Name == "" {
					return nil, fmt.Errorf("render_report requires name")
				}
				return s.renderReport(ctx, p, args.Name)
			}},
	}
}

// fetchSeries pulls a raw metric series for an object over the last
// `hours`, reusing the same fetch path as the query_metrics tool: tenant
// ownership is enforced via the catalog, then the series is read from the
// TSDB. Unlike query_metrics (which downsamples to ~100 pixels) the
// statistics tools want full resolution, so MaxPoints is set high. When
// `metric` is empty the object's first/only series is used. It returns
// the flattened samples and the resolved series metadata.
func (s *Service) fetchSeries(ctx context.Context, p *auth.Principal, objectID, metric string, hours float64) ([]tsdb.Sample, tsdb.SeriesMeta, error) {
	var meta tsdb.SeriesMeta
	if e := s.cat.Get(objectID); e == nil || e.Object.TenantID != p.TenantID {
		return nil, meta, fmt.Errorf("object not found")
	}
	results, err := s.tsdb.Query(ctx, tsdb.Query{
		ObjectID:  objectID,
		Metric:    metric,
		From:      time.Now().Add(-time.Duration(hours * float64(time.Hour))),
		To:        time.Now(),
		Agg:       tsdb.AggAvg,
		MaxPoints: 10000,
	})
	if err != nil {
		return nil, meta, err
	}
	if len(results) == 0 {
		return nil, meta, nil
	}
	// When the metric was unspecified the query may return several series;
	// the statistics operate on one. Use the first (deterministically the
	// lowest metric name, since Query sorts its output).
	r := results[0]
	return r.Points, r.Series, nil
}

package ai

import (
	"context"
	"encoding/json"
	"fmt"
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

func sch(s string) json.RawMessage { return json.RawMessage(s) }

// tools is the canonical registry.
func buildTools() []Tool {
	return []Tool{
		{Def: ToolDef{Name: "get_overview",
			Description: "State summary: problem counts, open incidents, on-call.",
			Schema:      sch(`{"type":"object","properties":{}}`)},
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
			Schema:      sch(`{"type":"object","properties":{"selector":{"type":"string"},"query":{"type":"string"},"kind":{"type":"string","enum":["host","service"]},"limit":{"type":"integer"}}}`)},
			Perm: model.Permission("objects:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args struct {
					Selector, Query, Kind string
					Limit                 int
				}
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
			Schema:      sch(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)},
			Perm: model.Permission("objects:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args struct{ ID string }
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
			Schema:      sch(`{"type":"object","properties":{"objectId":{"type":"string"},"metric":{"type":"string"},"fromHoursAgo":{"type":"number"},"agg":{"type":"string"}},"required":["objectId"]}`)},
			Perm: model.Permission("metrics:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args struct {
					ObjectID, Metric, Agg string
					FromHoursAgo          float64
				}
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
			Schema:      sch(`{"type":"object","properties":{"status":{"type":"string"},"limit":{"type":"integer"}}}`)},
			Perm: model.Permission("alerts:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args struct {
					Status string
					Limit  int
				}
				_ = json.Unmarshal(in, &args)
				f := storage.AlertFilter{TenantID: p.TenantID, Limit: args.Limit}
				if args.Status != "" {
					f.Status = []model.AlertStatus{model.AlertStatus(args.Status)}
				} else {
					f.Status = []model.AlertStatus{model.AlertOpen, model.AlertAcked}
				}
				return s.store.ListAlerts(ctx, f)
			}},

		{Def: ToolDef{Name: "get_incidents",
			Description: "List incidents with their alerts.",
			Schema:      sch(`{"type":"object","properties":{"open":{"type":"boolean"}}}`)},
			Perm: model.Permission("incidents:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args struct{ Open bool }
				_ = json.Unmarshal(in, &args)
				return s.store.ListIncidents(ctx, p.TenantID, args.Open, "", 50)
			}},

		{Def: ToolDef{Name: "who_is_oncall",
			Description: "Current on-call per schedule.",
			Schema:      sch(`{"type":"object","properties":{"schedule":{"type":"string"}}}`)},
			Perm: model.Permission("oncall:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args struct{ Schedule string }
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
			Schema:      sch(`{"type":"object","properties":{"alertId":{"type":"string"}},"required":["alertId"]}`)},
			Perm: model.Permission("alerts:read"),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args struct{ AlertID string }
				_ = json.Unmarshal(in, &args)
				return s.explainAlert(ctx, p.TenantID, args.AlertID)
			}},

		{Def: ToolDef{Name: "run_check_now",
			Description: "Trigger an immediate recheck.",
			Schema:      sch(`{"type":"object","properties":{"objectId":{"type":"string"}},"required":["objectId"]}`)},
			Perm:     model.Permission("checks:run"),
			Mutating: true, AutoOK: true,
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args struct{ ObjectID string }
				_ = json.Unmarshal(in, &args)
				if e := s.cat.Get(args.ObjectID); e == nil || e.Object.TenantID != p.TenantID {
					return nil, fmt.Errorf("object not found")
				}
				s.sched.CheckNow(args.ObjectID)
				return map[string]string{"status": "queued"}, nil
			}},

		{Def: ToolDef{Name: "acknowledge_alert",
			Description: "Acknowledge an alert, stopping its escalation.",
			Schema:      sch(`{"type":"object","properties":{"alertId":{"type":"string"},"comment":{"type":"string"}},"required":["alertId"]}`)},
			Perm:     model.Permission("alerts:ack"),
			Mutating: true, AutoOK: true,
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args struct{ AlertID, Comment string }
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
			Schema:      sch(`{"type":"object","properties":{"objectId":{"type":"string"},"selector":{"type":"string"},"hours":{"type":"number"},"comment":{"type":"string"}},"required":["comment"]}`)},
			Perm:     model.Permission("downtimes:write"),
			Mutating: true,
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args struct {
					ObjectID, Selector, Comment string
					Hours                       float64
				}
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
			Schema:      sch(`{"type":"object","properties":{"selector":{"type":"string"},"hours":{"type":"number"},"comment":{"type":"string"}},"required":["selector","comment"]}`)},
			Perm:     model.Permission("silences:write"),
			Mutating: true,
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args struct {
					Selector, Comment string
					Hours             float64
				}
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
			Schema:      sch(`{"type":"object","properties":{"bundleYaml":{"type":"string"}},"required":["bundleYaml"]}`)},
			Perm:     model.Permission("config:write"),
			Mutating: true,
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				var args struct{ BundleYaml string }
				_ = json.Unmarshal(in, &args)
				return s.planBundle(ctx, p, args.BundleYaml)
			}},
	}
}

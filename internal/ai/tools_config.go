package ai

// Generic config-resource tools (SPEC P1 parity): every configuration
// document the REST API manages via resourceCRUD is equally manageable
// over MCP/AI — list/get/upsert/delete with the *same* per-kind RBAC the
// REST routes enforce, the same validation (via ResourceAdmin → api),
// and the same audit trail. Writes are Mutating without AutoOK, so they
// ride the human-approval queue like apply_config_change.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

type kindPerms struct{ read, write model.Permission }

// configKindPerms mirrors the REST registrations (api.resourceCRUD call
// sites): permPrefix "config" reads with objects:read and writes with
// config:write; oncall/admin use their own pair. KindPreference matches
// the REST rule for foreign preferences (admin:users both ways — own
// preferences are a REST-session concern, not an agent surface).
// internal/api.TestConfigResourceToolPermParity pins this table against
// the live route registrations.
var configKindPerms = map[string]kindPerms{
	storage.KindTemplate:         {"objects:read", "config:write"},
	storage.KindCheckCommand:     {"objects:read", "config:write"},
	storage.KindTimePeriod:       {"objects:read", "config:write"},
	storage.KindAlertRule:        {"objects:read", "config:write"},
	storage.KindAlertGroup:       {"objects:read", "config:write"},
	storage.KindEscalationPolicy: {"objects:read", "config:write"},
	storage.KindChannel:          {"objects:read", "config:write"},
	storage.KindEventSource:      {"objects:read", "config:write"},
	storage.KindBusinessService:  {"objects:read", "config:write"},
	storage.KindDashboard:        {"objects:read", "config:write"},
	storage.KindReport:           {"objects:read", "config:write"},
	storage.KindSavedFilter:      {"objects:read", "config:write"},
	storage.KindWebhookSub:       {"objects:read", "config:write"},
	storage.KindStaticGroup:      {"objects:read", "config:write"},
	storage.KindSchedule:         {"oncall:read", "oncall:write"},
	storage.KindContact:          {"oncall:read", "oncall:write"},
	storage.KindContactGroup:     {"oncall:read", "oncall:write"},
	storage.KindRole:             {"admin:read", "admin:write"},
	storage.KindPreference:       {"admin:users", "admin:users"},
}

// configKindEnum is the sorted kind list for the tool schemas.
func configKindEnum() string {
	kinds := make([]string, 0, len(configKindPerms))
	for k := range configKindPerms {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return strings.Join(kinds, "|")
}

type listConfigResourcesInput struct {
	Kind  string `json:"kind" desc:"Resource kind to list."`
	Query string `json:"query,omitempty" desc:"Optional substring filter on the resource name."`
	Limit int    `json:"limit,omitempty" desc:"Maximum number of documents to return (default 100, capped at 500)."`
}

type getConfigResourceInput struct {
	Kind string `json:"kind" desc:"Resource kind."`
	Name string `json:"name" desc:"Resource name."`
}

type upsertConfigResourceInput struct {
	Kind string         `json:"kind" desc:"Resource kind."`
	Name string         `json:"name" desc:"Resource name (created if absent, updated otherwise)."`
	Doc  map[string]any `json:"doc" desc:"Full resource document — the same JSON the REST API accepts for this kind."`
	// ExpectedVersion mirrors REST's If-Match optimistic locking.
	ExpectedVersion int64 `json:"expectedVersion,omitempty" desc:"Optimistic-concurrency guard: fail unless the stored version matches. 0 = unconditional upsert."`
}

type deleteConfigResourceInput struct {
	Kind string `json:"kind" desc:"Resource kind."`
	Name string `json:"name" desc:"Resource name to delete."`
}

// configPermFor derives the RBAC permission from the input's kind. write
// selects the kind's write permission, otherwise read. Unknown kinds get
// an admin-tier fallback so a malformed call can never under-ask; Run
// then rejects the kind with a helpful error.
func configPermFor(write bool) func(json.RawMessage) model.Permission {
	return func(in json.RawMessage) model.Permission {
		var args struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal(in, &args)
		kp, ok := configKindPerms[args.Kind]
		if !ok {
			return "admin:*"
		}
		if write {
			return kp.write
		}
		return kp.read
	}
}

func checkConfigKind(kind string) error {
	if _, ok := configKindPerms[kind]; !ok {
		return fmt.Errorf("unsupported resource kind %q (one of: %s)", kind, configKindEnum())
	}
	return nil
}

func (s *Service) requireResources() (ResourceAdmin, error) {
	if s.resources == nil {
		return nil, fmt.Errorf("resource administration is not wired in this deployment")
	}
	return s.resources, nil
}

// configToolSchema reflects a config-tool input struct and injects the
// computed kind enum into its "kind" property (reflectSchema's enum tag
// is static, the kind list is derived from configKindPerms).
func configToolSchema(proto any) json.RawMessage {
	var m map[string]any
	_ = json.Unmarshal(reflectSchema(proto), &m)
	if props, ok := m["properties"].(map[string]any); ok {
		if kp, ok := props["kind"].(map[string]any); ok {
			vals := make([]any, 0)
			for _, v := range strings.Split(configKindEnum(), "|") {
				vals = append(vals, v)
			}
			kp["enum"] = vals
		}
	}
	out, _ := json.Marshal(m)
	return out
}

// buildConfigTools returns the generic resource CRUD tools, appended to
// the registry in buildTools.
func buildConfigTools() []Tool {
	return []Tool{
		{Def: ToolDef{Name: "list_config_resources",
			Description: "List configuration documents of a kind (templates, alert-rules, channels, schedules, contacts, dashboards, …) — the same documents the REST API manages.",
			Schema:      configToolSchema(listConfigResourcesInput{})},
			PermFor: configPermFor(false),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				ra, err := s.requireResources()
				if err != nil {
					return nil, err
				}
				var args listConfigResourcesInput
				_ = json.Unmarshal(in, &args)
				if err := checkConfigKind(args.Kind); err != nil {
					return nil, err
				}
				if args.Limit <= 0 || args.Limit > 500 {
					args.Limit = 100
				}
				docs, err := ra.ListResourceDocs(ctx, p.TenantID, args.Kind, args.Query, args.Limit)
				if err != nil {
					return nil, err
				}
				return map[string]any{"kind": args.Kind, "count": len(docs), "items": docs}, nil
			}},

		{Def: ToolDef{Name: "get_config_resource",
			Description: "Fetch one configuration document by kind and name, including its version for optimistic updates.",
			Schema:      configToolSchema(getConfigResourceInput{})},
			PermFor: configPermFor(false),
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				ra, err := s.requireResources()
				if err != nil {
					return nil, err
				}
				var args getConfigResourceInput
				_ = json.Unmarshal(in, &args)
				if err := checkConfigKind(args.Kind); err != nil {
					return nil, err
				}
				if args.Name == "" {
					return nil, fmt.Errorf("get_config_resource requires name")
				}
				return ra.GetResourceDoc(ctx, p.TenantID, args.Kind, args.Name)
			}},

		{Def: ToolDef{Name: "upsert_config_resource",
			Description: "Create or update a configuration document (validated like the REST API; honors expectedVersion). Queued for human approval unless policy allows automatic execution.",
			Schema:      configToolSchema(upsertConfigResourceInput{})},
			PermFor:  configPermFor(true),
			Mutating: true, // rides the approval queue (SPEC §10.3)
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				ra, err := s.requireResources()
				if err != nil {
					return nil, err
				}
				var args upsertConfigResourceInput
				if err := json.Unmarshal(in, &args); err != nil {
					return nil, fmt.Errorf("invalid input: %w", err)
				}
				if err := checkConfigKind(args.Kind); err != nil {
					return nil, err
				}
				if args.Name == "" || args.Doc == nil {
					return nil, fmt.Errorf("upsert_config_resource requires name and doc")
				}
				return ra.UpsertResourceDoc(ctx, p.TenantID, args.Kind, args.Name, args.Doc, args.ExpectedVersion)
			}},

		{Def: ToolDef{Name: "delete_config_resource",
			Description: "Delete a configuration document by kind and name. Queued for human approval unless policy allows automatic execution.",
			Schema:      configToolSchema(deleteConfigResourceInput{})},
			PermFor:  configPermFor(true),
			Mutating: true,
			Run: func(ctx context.Context, s *Service, p *auth.Principal, in json.RawMessage) (any, error) {
				ra, err := s.requireResources()
				if err != nil {
					return nil, err
				}
				var args deleteConfigResourceInput
				_ = json.Unmarshal(in, &args)
				if err := checkConfigKind(args.Kind); err != nil {
					return nil, err
				}
				if args.Name == "" {
					return nil, fmt.Errorf("delete_config_resource requires name")
				}
				if err := ra.DeleteResourceDoc(ctx, p.TenantID, args.Kind, args.Name); err != nil {
					return nil, err
				}
				return map[string]any{"deleted": args.Name, "kind": args.Kind}, nil
			}},
	}
}

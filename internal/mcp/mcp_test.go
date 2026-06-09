package mcp_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/northplane/northplane/internal/ai"
	"github.com/northplane/northplane/internal/api"
	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/escalation"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/mcp"
	"github.com/northplane/northplane/internal/metrics"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/scheduler"
	"github.com/northplane/northplane/internal/storage"
	"github.com/northplane/northplane/internal/tsdb"
)

// connect builds the real AI service + MCP server bound to a principal
// and returns a connected client session (in-memory transport — the
// same sdk.Server the stdio and Streamable-HTTP entrypoints serve).
func connect(t *testing.T, perms []model.Permission) (*sdk.ClientSession, *storage.Store, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cat := catalog.New(store)
	if err := cat.LoadAll(ctx); err != nil {
		t.Fatal(err)
	}
	bus := eventbus.New()
	ts, err := tsdb.Open(t.TempDir(), nil, tsdb.Retention{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ts.Close() })
	// the real API layer backs the generic config tools (validation +
	// cache invalidation), exactly as the server wires it.
	resAdmin := &api.API{Store: store, Catalog: cat, Sched: scheduler.New(cat, nil),
		Bus: bus, Metrics: metrics.NewRegistry(),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	svc := ai.New(ai.Deps{
		Cfg: config.AIConfig{}, Store: store, Catalog: cat,
		Sched: scheduler.New(cat, nil), Escal: escalation.New(store, bus, nil),
		Bus: bus, TSDB: ts, Resources: resAdmin,
	})
	principal := &auth.Principal{ActorType: model.ActorAI, ActorID: "tok-1",
		Name: "mcp-test", TenantID: model.DefaultTenant, Perms: perms}

	server := mcp.Build(svc, principal, "test")
	st, ct := sdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, store, ctx
}

// TestMCPToolSurface: the full SPEC §10.3 tool set is exposed over MCP.
func TestMCPToolSurface(t *testing.T) {
	session, _, ctx := connect(t, []model.Permission{"*:*"})
	res, err := session.ListTools(ctx, &sdk.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	want := []string{
		"get_overview", "search_objects", "get_object", "query_metrics",
		"get_alerts", "get_incidents", "who_is_oncall", "explain_alert",
		"run_check_now", "acknowledge_alert", "create_downtime",
		"create_silence", "propose_config_change", "apply_config_change",
		"render_report",
		"analyze_metric", "forecast_capacity", "suggest_thresholds",
		// generic config CRUD (P1 parity: full configuration over MCP)
		"list_config_resources", "get_config_resource",
		"upsert_config_resource", "delete_config_resource",
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("tool %q missing from MCP surface (have: %v)", name, len(res.Tools))
		}
	}
}

// TestMCPReadTool: a scoped token can execute read tools end-to-end.
func TestMCPReadTool(t *testing.T) {
	session, store, ctx := connect(t, []model.Permission{"events:read", "alerts:read", "incidents:read"})
	// seed one alert so the overview carries data
	if _, _, err := store.UpsertAlert(ctx, &model.Alert{
		TenantID: model.DefaultTenant, Severity: model.SevCritical,
		Title: "mcp sees me", DedupKey: "mcp/1", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "get_alerts",
		Arguments: map[string]any{"status": "open"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool errored: %+v", res.Content)
	}
	text := contentText(res)
	if !strings.Contains(text, "mcp sees me") {
		t.Fatalf("alert missing from result: %s", text)
	}
}

// TestMCPPermissionDenied: the MCP session is privilege-less — a token
// without the tool's scope is refused (and audited).
func TestMCPPermissionDenied(t *testing.T) {
	session, _, ctx := connect(t, []model.Permission{"events:read"}) // no alerts:read
	res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "get_alerts",
		Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected permission denial")
	}
	if text := contentText(res); !strings.Contains(text, "permission denied") {
		t.Fatalf("denial text: %s", text)
	}
}

// TestMCPMutationProposed: mutating tools without auto-ok land in the
// approval queue instead of executing (SPEC §10.1 approval gate).
func TestMCPMutationProposed(t *testing.T) {
	session, store, ctx := connect(t, []model.Permission{"*:*"})
	res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "apply_config_change",
		Arguments: map[string]any{"bundleYaml": "kind: Host\nmetadata:\n  name: h1\nspec: {}\n"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("propose errored: %s", contentText(res))
	}
	text := contentText(res)
	if !strings.Contains(text, `"status": "proposed"`) {
		t.Fatalf("mutation must be proposed, got: %s", text)
	}
	actions, err := store.ListAIActions(ctx, model.DefaultTenant, "proposed", 10)
	if err != nil || len(actions) != 1 || actions[0].Tool != "apply_config_change" {
		t.Fatalf("approval queue: %v %+v", err, actions)
	}
}

// TestMCPPrompts: shipped prompts resolve.
func TestMCPPrompts(t *testing.T) {
	session, _, ctx := connect(t, nil)
	prompts, err := session.ListPrompts(ctx, &sdk.ListPromptsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts.Prompts) < 3 {
		t.Fatalf("prompts: %d", len(prompts.Prompts))
	}
	p, err := session.GetPrompt(ctx, &sdk.GetPromptParams{Name: "morning-briefing"})
	if err != nil || len(p.Messages) == 0 {
		t.Fatalf("get prompt: %v", err)
	}
}

// TestMCPConfigResourceRead: configuration documents are readable over
// MCP via the generic tools (P1 parity), under per-kind RBAC.
func TestMCPConfigResourceRead(t *testing.T) {
	session, store, ctx := connect(t, []model.Permission{"objects:read"})
	if _, err := store.PutResource(ctx, model.DefaultTenant, storage.KindChannel, "ops-mail",
		&model.NotificationChannel{Name: "ops-mail", Type: model.ChannelEmail, Enabled: true,
			Config: map[string]string{"provider": "sendmail"}}, 0); err != nil {
		t.Fatal(err)
	}

	res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "list_config_resources",
		Arguments: map[string]any{"kind": "channel"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("list errored: %s", contentText(res))
	}
	if text := contentText(res); !strings.Contains(text, "ops-mail") {
		t.Fatalf("channel missing: %s", text)
	}

	// per-kind RBAC: schedules need oncall:read which this token lacks
	res, err = session.CallTool(ctx, &sdk.CallToolParams{Name: "list_config_resources",
		Arguments: map[string]any{"kind": "schedule"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(contentText(res), "oncall:read") {
		t.Fatalf("schedule list must be denied: %s", contentText(res))
	}
}

// TestMCPConfigResourceWriteProposed: a config write over MCP lands in
// the approval queue and applies through the same validated path as REST.
func TestMCPConfigResourceWriteProposed(t *testing.T) {
	session, store, ctx := connect(t, []model.Permission{"config:write"})
	res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "upsert_config_resource",
		Arguments: map[string]any{"kind": "channel", "name": "mcp-mail",
			"doc": map[string]any{"name": "mcp-mail", "type": "email", "enabled": true,
				"config": map[string]any{"provider": "resend", "apiKey": "$SECRET:resend$", "from": "np@example.com"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("upsert errored: %s", contentText(res))
	}
	if text := contentText(res); !strings.Contains(text, `"status": "proposed"`) {
		t.Fatalf("write must be proposed: %s", text)
	}
	actions, err := store.ListAIActions(ctx, model.DefaultTenant, "proposed", 10)
	if err != nil || len(actions) != 1 || actions[0].Tool != "upsert_config_resource" {
		t.Fatalf("queue: %v %+v", err, actions)
	}
}

func contentText(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

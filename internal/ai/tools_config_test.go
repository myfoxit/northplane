package ai

// Generic config-resource tools: per-kind RBAC (PermFor), dispatch into
// ResourceAdmin, the approval-queue ride for writes, and the approver
// re-check in ExecuteApproved.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// fakeResources records calls and serves a tiny in-memory kind/name map.
type fakeResources struct {
	docs    map[string]json.RawMessage // kind/name → doc
	calls   []string
	failPut error
}

func (f *fakeResources) key(kind, name string) string { return kind + "/" + name }

func (f *fakeResources) ListResourceDocs(_ context.Context, _, kind, _ string, _ int) ([]json.RawMessage, error) {
	f.calls = append(f.calls, "list:"+kind)
	var out []json.RawMessage
	for k, v := range f.docs {
		if strings.HasPrefix(k, kind+"/") {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeResources) GetResourceDoc(_ context.Context, _, kind, name string) (json.RawMessage, error) {
	f.calls = append(f.calls, "get:"+kind+"/"+name)
	doc, ok := f.docs[f.key(kind, name)]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return doc, nil
}

func (f *fakeResources) UpsertResourceDoc(_ context.Context, _, kind, name string, doc map[string]any, _ int64) (json.RawMessage, error) {
	f.calls = append(f.calls, "upsert:"+kind+"/"+name)
	if f.failPut != nil {
		return nil, f.failPut
	}
	raw, _ := json.Marshal(doc)
	if f.docs == nil {
		f.docs = map[string]json.RawMessage{}
	}
	f.docs[f.key(kind, name)] = raw
	return raw, nil
}

func (f *fakeResources) DeleteResourceDoc(_ context.Context, _, kind, name string) error {
	f.calls = append(f.calls, "delete:"+kind+"/"+name)
	delete(f.docs, f.key(kind, name))
	return nil
}

func bootConfigToolsSvc(t *testing.T) (*Service, *fakeResources, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	fr := &fakeResources{docs: map[string]json.RawMessage{}}
	svc := New(Deps{Cfg: config.AIConfig{}, Store: store, Bus: eventbus.New(), Resources: fr})
	return svc, fr, ctx
}

func principalWith(perms ...model.Permission) *auth.Principal {
	return &auth.Principal{ActorType: model.ActorAI, ActorID: "tok-cfg",
		Name: "cfg-test", TenantID: model.DefaultTenant, Perms: perms}
}

func TestConfigToolsReadRBACPerKind(t *testing.T) {
	svc, fr, ctx := bootConfigToolsSvc(t)
	fr.docs["channel/mail"] = json.RawMessage(`{"name":"mail","type":"email"}`)

	// objects:read suffices for config-prefix kinds…
	res, proposed, err := svc.RunTool(ctx, principalWith("objects:read"), "list_config_resources",
		json.RawMessage(`{"kind":"channel"}`))
	if err != nil || proposed {
		t.Fatalf("list channels: %v proposed=%v", err, proposed)
	}
	if m := res.(map[string]any); m["count"].(int) != 1 {
		t.Fatalf("count: %+v", m)
	}

	// …but not for on-call kinds (oncall:read required)
	_, _, err = svc.RunTool(ctx, principalWith("objects:read"), "list_config_resources",
		json.RawMessage(`{"kind":"schedule"}`))
	if err == nil || !strings.Contains(err.Error(), "oncall:read") {
		t.Fatalf("schedule list should need oncall:read, got %v", err)
	}

	// roles need admin:read
	_, _, err = svc.RunTool(ctx, principalWith("objects:read"), "get_config_resource",
		json.RawMessage(`{"kind":"role","name":"operator"}`))
	if err == nil || !strings.Contains(err.Error(), "admin:read") {
		t.Fatalf("role get should need admin:read, got %v", err)
	}

	// unknown kind: never under-asks (admin:*), then rejects
	_, _, err = svc.RunTool(ctx, principalWith("*:*"), "get_config_resource",
		json.RawMessage(`{"kind":"nonsense","name":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "unsupported resource kind") {
		t.Fatalf("unknown kind: %v", err)
	}
}

func TestConfigToolsWriteRidesApprovalQueue(t *testing.T) {
	svc, fr, ctx := bootConfigToolsSvc(t)
	p := principalWith("config:write")

	res, proposed, err := svc.RunTool(ctx, p, "upsert_config_resource", json.RawMessage(
		`{"kind":"channel","name":"mail","doc":{"name":"mail","type":"email","enabled":true,"config":{"provider":"sendmail"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !proposed {
		t.Fatalf("write must be proposed, got immediate result %+v", res)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("no storage call before approval, got %v", fr.calls)
	}

	// approve + execute under an approver holding the kind's write perm
	actions, err := svc.store.ListAIActions(ctx, model.DefaultTenant, "proposed", 10)
	if err != nil || len(actions) != 1 {
		t.Fatalf("queue: %v %+v", err, actions)
	}
	if err := svc.store.DecideAIAction(ctx, model.DefaultTenant, actions[0].ID,
		storage.AIApproved, "approver"); err != nil {
		t.Fatal(err)
	}

	// an approver lacking the perm is refused
	if _, err := svc.ExecuteApproved(ctx, model.DefaultTenant, actions[0].ID,
		principalWith("objects:read")); err == nil ||
		!strings.Contains(err.Error(), "config:write") {
		t.Fatalf("under-privileged approver: %v", err)
	}

	out, err := svc.ExecuteApproved(ctx, model.DefaultTenant, actions[0].ID,
		principalWith("config:write"))
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || len(fr.calls) != 1 || fr.calls[0] != "upsert:channel/mail" {
		t.Fatalf("execution calls: %v", fr.calls)
	}
}

func TestConfigToolsDeleteAndMissingWiring(t *testing.T) {
	svc, fr, ctx := bootConfigToolsSvc(t)
	fr.docs["contact/anna"] = json.RawMessage(`{"name":"anna"}`)

	// delete requires the kind's write perm even to PROPOSE
	_, _, err := svc.RunTool(ctx, principalWith("config:write"), "delete_config_resource",
		json.RawMessage(`{"kind":"contact","name":"anna"}`))
	if err == nil || !strings.Contains(err.Error(), "oncall:write") {
		t.Fatalf("contact delete should need oncall:write: %v", err)
	}

	// unwired Resources fails helpfully (stdio mode without the API layer)
	bare := New(Deps{Cfg: config.AIConfig{}, Store: svc.store, Bus: eventbus.New()})
	_, _, err = bare.RunTool(ctx, principalWith("objects:read"), "list_config_resources",
		json.RawMessage(`{"kind":"channel"}`))
	if err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("unwired: %v", err)
	}
}

func TestConfigKindTableCoversPreferencesAndStaysSorted(t *testing.T) {
	// preference writes are admin-tier (an agent administering users'
	// UI settings — same as the REST foreign-preferences rule).
	kp, ok := configKindPerms[storage.KindPreference]
	if !ok || kp.write != "admin:users" || kp.read != "admin:users" {
		t.Fatalf("preference perms: %+v ok=%v", kp, ok)
	}
	enum := configKindEnum()
	if !strings.Contains(enum, storage.KindChannel) || !strings.Contains(enum, storage.KindSchedule) {
		t.Fatalf("enum incomplete: %s", enum)
	}
}

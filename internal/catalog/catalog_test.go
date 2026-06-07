package catalog_test

import (
	"context"
	"testing"

	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

func setup(t *testing.T) (*storage.Store, *catalog.Catalog, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cat := catalog.New(store)
	if err := cat.LoadAll(ctx); err != nil {
		t.Fatal(err)
	}
	return store, cat, ctx
}

func mkObj(t *testing.T, store *storage.Store, ctx context.Context, kind model.Kind, name, hostID string, spec model.ObjectSpec) *model.Object {
	t.Helper()
	o := &model.Object{TenantID: model.DefaultTenant, Kind: kind, Name: name, HostID: hostID, Spec: spec}
	if err := store.CreateObject(ctx, o); err != nil {
		t.Fatal(err)
	}
	return o
}

// UpsertObject must index incrementally — entry, byName, host/children
// links and parent edges — without a full tenant reload.
func TestUpsertObjectIncremental(t *testing.T) {
	store, cat, ctx := setup(t)

	gw := mkObj(t, store, ctx, model.KindHost, "gw", "", model.ObjectSpec{
		Address: "10.0.0.1", CheckCommand: "builtin:icmp"})
	if err := cat.UpsertObject(gw); err != nil {
		t.Fatal(err)
	}

	web := mkObj(t, store, ctx, model.KindHost, "web", "", model.ObjectSpec{
		Address: "10.0.0.2", CheckCommand: "builtin:icmp", Parents: []string{"gw"}})
	if err := cat.UpsertObject(web); err != nil {
		t.Fatal(err)
	}
	if parents := cat.Parents(web.ID); len(parents) != 1 || parents[0] != gw.ID {
		t.Fatalf("parent edge missing after incremental upsert: %v", parents)
	}

	svc := mkObj(t, store, ctx, model.KindService, "https", web.ID, model.ObjectSpec{
		CheckCommand: "builtin:https", Args: []string{"-u", "https://example.org"}})
	if err := cat.UpsertObject(svc); err != nil {
		t.Fatal(err)
	}
	if children := cat.Children(web.ID); len(children) != 1 || children[0] != svc.ID {
		t.Fatalf("children edge missing: %v", children)
	}
	entry := cat.Get(svc.ID)
	if entry == nil || entry.Host == nil || entry.Host.Object.ID != web.ID {
		t.Fatal("service entry must link its host")
	}
	if cat.GetByName(model.DefaultTenant, model.KindService, web.ID, "https") == nil {
		t.Fatal("byName lookup missing after upsert")
	}

	// Spec update reindexes in place (interval visible in effective spec).
	svc.Spec.Interval = model.Duration(5_000_000_000) // 5s
	if err := store.UpdateObject(ctx, svc, 0); err != nil {
		t.Fatal(err)
	}
	if err := cat.UpsertObject(svc); err != nil {
		t.Fatal(err)
	}
	if cat.Get(svc.ID).Effective.Interval != model.Duration(5_000_000_000) {
		t.Fatal("update must reindex effective spec")
	}
}

// RemoveObject drops the object and its cascaded service children and
// returns every removed id (callers deschedule them).
func TestRemoveObjectCascades(t *testing.T) {
	store, cat, ctx := setup(t)

	host := mkObj(t, store, ctx, model.KindHost, "db", "", model.ObjectSpec{
		Address: "10.0.0.3", CheckCommand: "builtin:icmp"})
	if err := cat.UpsertObject(host); err != nil {
		t.Fatal(err)
	}
	svc := mkObj(t, store, ctx, model.KindService, "postgres", host.ID, model.ObjectSpec{
		CheckCommand: "passive"})
	if err := cat.UpsertObject(svc); err != nil {
		t.Fatal(err)
	}

	removed := cat.RemoveObject(model.DefaultTenant, host.ID)
	if len(removed) != 2 {
		t.Fatalf("expected host + service removed, got %v", removed)
	}
	if cat.Get(host.ID) != nil || cat.Get(svc.ID) != nil {
		t.Fatal("entries must be gone")
	}
	if cat.GetByName(model.DefaultTenant, model.KindHost, "", "db") != nil {
		t.Fatal("byName must be gone")
	}
	if len(cat.Children(host.ID)) != 0 {
		t.Fatal("children edges must be gone")
	}
}

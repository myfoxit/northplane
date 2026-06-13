package pipeline

import (
	"context"
	"testing"

	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/scheduler"
	"github.com/northplane/northplane/internal/storage"
)

// TestFlushRequeuesOnError is the data-loss regression: a transient DB error
// during a state flush must NOT drop the dirty markers. Previously they were
// cleared under lock before the (out-of-lock) write, so a failed write
// permanently lost check-state transitions — the core monitoring-state path.
// Now a failed write re-queues the markers for the next flush. (The pending
// events path re-queues identically; events live in separate segment DBs and
// can't be failed by closing the main store, so this exercises the state
// path that closing reliably breaks.)
func TestFlushRequeuesOnError(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cat := catalog.New(store)
	bus := eventbus.New()
	sched := scheduler.New(cat, nil)

	// A real host so SaveCheckStates' FK to objects is satisfied; that way a
	// failed flush can only come from the closed store, not a constraint.
	host := &model.Object{TenantID: model.DefaultTenant, Kind: model.KindHost, Name: "flush-host",
		Spec: model.ObjectSpec{Address: "127.0.0.1", CheckCommand: "builtin:tcp"}}
	if err := store.CreateObject(ctx, host); err != nil {
		t.Fatalf("create object: %v", err)
	}

	p := New(store, cat, bus, nil, sched, nil)
	p.states[host.ID] = &model.CheckState{ObjectID: host.ID, State: model.StateCritical,
		StateType: model.StateHard, Attempt: 1}
	p.dirty[host.ID] = true
	store.Close() // subsequent writes error

	p.flush(ctx)

	p.mu.Lock()
	gotDirty := p.dirty[host.ID]
	p.mu.Unlock()
	if !gotDirty {
		t.Error("dirty marker was dropped on flush error (state transition lost)")
	}

	// Sanity: a clean flush against a healthy store clears the marker.
	store2, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store2.Close()
	host2 := &model.Object{TenantID: model.DefaultTenant, Kind: model.KindHost, Name: "flush-host-2",
		Spec: model.ObjectSpec{Address: "127.0.0.1", CheckCommand: "builtin:tcp"}}
	if err := store2.CreateObject(ctx, host2); err != nil {
		t.Fatalf("create object 2: %v", err)
	}
	p2 := New(store2, catalog.New(store2), bus, nil, sched, nil)
	p2.states[host2.ID] = &model.CheckState{ObjectID: host2.ID, State: model.StateOK,
		StateType: model.StateHard, Attempt: 1}
	p2.dirty[host2.ID] = true
	p2.flush(ctx)
	p2.mu.Lock()
	stillDirty := p2.dirty[host2.ID]
	p2.mu.Unlock()
	if stillDirty {
		t.Error("dirty marker must clear after a successful flush")
	}
}

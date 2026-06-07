package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// ListCheckStates must return the same rows as per-object GetCheckState
// — it replaces the N+1 lookup on the list endpoints.
func TestListCheckStatesBatch(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var ids []string
	var states []*model.CheckState
	now := time.Now().UTC()
	for i := 0; i < 7; i++ {
		o := &model.Object{TenantID: model.DefaultTenant, Kind: model.KindHost,
			Name: fmt.Sprintf("h%d", i), Spec: model.ObjectSpec{CheckCommand: "passive"}}
		if err := store.CreateObject(ctx, o); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, o.ID)
		states = append(states, &model.CheckState{ObjectID: o.ID,
			State: model.State(i % 3), StateType: model.StateHard, Attempt: 1,
			Output: fmt.Sprintf("out-%d", i), LastCheck: &now})
	}
	if err := store.SaveCheckStates(ctx, states); err != nil {
		t.Fatal(err)
	}

	got, err := store.ListCheckStates(ctx, append(ids, "missing-id"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(ids) {
		t.Fatalf("want %d states, got %d", len(ids), len(got))
	}
	for i, id := range ids {
		cs, ok := got[id]
		if !ok {
			t.Fatalf("state for %s missing", id)
		}
		if cs.Output != fmt.Sprintf("out-%d", i) || cs.State != model.State(i%3) {
			t.Fatalf("state %d mismatch: %+v", i, cs)
		}
	}
	if empty, err := store.ListCheckStates(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty input: %v %v", empty, err)
	}
}

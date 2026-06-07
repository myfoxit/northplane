package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// Regression: the wheel must actually dispatch on its own (not just via
// the priority lane). A fractional splay once parked every entry for a
// full 24h revolution.
func TestWheelDispatchesWithinInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	obj := &model.Object{TenantID: model.DefaultTenant, Kind: model.KindHost, Name: "wheel-host",
		Spec: model.ObjectSpec{CheckCommand: "builtin:tcp", Args: []string{"-p", "1"},
			Interval: model.Duration(2 * time.Second), MaxCheckAttempts: 1}}
	if err := store.CreateObject(ctx, obj); err != nil {
		t.Fatal(err)
	}
	cat := catalog.New(store)
	if err := cat.LoadAll(ctx); err != nil {
		t.Fatal(err)
	}
	s := New(cat, nil)
	for _, e := range cat.All() {
		s.Upsert(e)
	}
	go s.Run(ctx)

	// within interval + wheel tick the job must appear on Out
	select {
	case job := <-s.Out:
		if job.ObjectID != obj.ID {
			t.Fatalf("wrong object: %+v", job)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("wheel never dispatched (stats: %+v)", s.Stats())
	}
	// and again (drift-free rescheduling)
	select {
	case <-s.Out:
	case <-time.After(5 * time.Second):
		t.Fatalf("wheel did not reschedule (stats: %+v)", s.Stats())
	}
}

func TestSplayIsDeterministicAndSpread(t *testing.T) {
	interval := 60 * time.Second
	a1 := splay("object-a", interval)
	a2 := splay("object-a", interval)
	if a1 != a2 {
		t.Fatal("splay must be deterministic")
	}
	// across many ids, offsets should cover the interval reasonably
	buckets := map[int64]int{}
	for i := 0; i < 1000; i++ {
		off := splay(model.NewID(), interval)
		if off < 0 || off >= interval {
			t.Fatalf("offset out of range: %v", off)
		}
		buckets[int64(off/(10*time.Second))]++
	}
	for b, n := range buckets {
		if n < 60 || n > 280 { // ~166 expected per 10s bucket
			t.Fatalf("splay skewed: bucket %d has %d", b, n)
		}
	}
}

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// TestCopyAllRoundtrip exercises the backend-migration copy path that was
// previously broken two ways: (1) the destination's seeded default tenant
// + builtin roles collided with the source rows (no ON CONFLICT), aborting
// on the first table; (2) event pagination advanced by +1ms and dropped
// same-millisecond events at a page boundary. Both are regression-guarded
// here (sqlite→sqlite; the int→bool conversion path is postgres-specific).
func TestCopyAllRoundtrip(t *testing.T) {
	ctx := context.Background()

	src, err := Open(ctx, Options{DataDir: t.TempDir(), RetentionMonths: 12})
	if err != nil {
		t.Fatalf("src open: %v", err)
	}
	defer src.Close()

	// An object + check state + a burst of same-millisecond events.
	host := &model.Object{TenantID: model.DefaultTenant, Kind: model.KindHost, Name: "h1"}
	if err := src.CreateObject(ctx, host); err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 1, 2, 3, 4, 5, 123_000_000, time.UTC)
	var evs []*model.Event
	for i := 0; i < 1500; i++ { // > one page, all within the same ms
		evs = append(evs, &model.Event{ID: model.NewID(), TenantID: model.DefaultTenant,
			TS: ts, Type: model.EventStateChange, ObjectID: host.ID, Severity: model.SevWarning})
	}
	if err := src.InsertEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}

	dst, err := Open(ctx, Options{DataDir: t.TempDir(), RetentionMonths: 12})
	if err != nil {
		t.Fatalf("dst open: %v", err)
	}
	defer dst.Close()

	// Previously this aborted on "table tenants: UNIQUE constraint failed".
	if _, err := CopyAll(ctx, src, dst); err != nil {
		t.Fatalf("CopyAll: %v", err)
	}

	// All events must have been copied (no same-ms boundary loss).
	got, err := dst.QueryEvents(ctx, EventFilter{TenantID: model.DefaultTenant, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	// page once more to count everything
	total := len(got)
	for len(got) == 1000 {
		got, _ = dst.QueryEvents(ctx, EventFilter{TenantID: model.DefaultTenant,
			Cursor: got[len(got)-1].ID, Limit: 1000})
		total += len(got)
	}
	if total != len(evs) {
		t.Fatalf("event count after migration = %d, want %d", total, len(evs))
	}

	// The object survived too.
	if _, err := dst.GetObject(ctx, model.DefaultTenant, host.ID); err != nil {
		t.Fatalf("object missing after migration: %v", err)
	}
}

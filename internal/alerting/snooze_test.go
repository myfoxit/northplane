package alerting

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// chainRecorder captures StartChain calls (fake Escalator).
type chainRecorder struct {
	mu     sync.Mutex
	starts []struct {
		alertID, policy string
		openedAt        time.Time
	}
}

func (c *chainRecorder) StartChain(_ context.Context, a *model.Alert, policy string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.starts = append(c.starts, struct {
		alertID, policy string
		openedAt        time.Time
	}{a.ID, policy, a.OpenedAt})
	return nil
}

func (c *chainRecorder) StopChain(context.Context, string) error { return nil }

// TestWakeSnoozedRestartsChain: a snoozed manual alarm re-opens after
// the deadline and its escalation chain restarts, rebased to the wake
// time (step 0 counts from now, not from the original open).
func TestWakeSnoozedRestartsChain(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	rec := &chainRecorder{}
	en := NewEngine(store, catalog.New(store), eventbus.New(), rec,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	payload, _ := json.Marshal(map[string]any{"escalationPolicy": "nachtdienst", "manual": true})
	al := &model.Alert{
		TenantID: model.DefaultTenant, RuleID: "manual",
		Severity: model.SevCritical, Title: "Manueller Alarm",
		OpenedAt: time.Now().Add(-time.Hour).UTC(), Payload: payload,
	}
	saved, _, err := store.UpsertAlert(ctx, al)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	until := time.Now().Add(-time.Minute).UTC() // already due
	if _, err := store.SnoozeAlert(ctx, saved.TenantID, saved.ID, "tester", until); err != nil {
		t.Fatalf("snooze: %v", err)
	}

	before := time.Now().UTC()
	en.wakeSnoozed(ctx, time.Now().UTC())

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.starts) != 1 {
		t.Fatalf("StartChain calls = %d, want 1", len(rec.starts))
	}
	if rec.starts[0].policy != "nachtdienst" || rec.starts[0].alertID != saved.ID {
		t.Fatalf("chain restart wrong: %+v", rec.starts[0])
	}
	if rec.starts[0].openedAt.Before(before.Add(-time.Second)) {
		t.Fatalf("chain not rebased to wake time: %v", rec.starts[0].openedAt)
	}

	got, err := store.GetAlert(ctx, saved.TenantID, saved.ID)
	if err != nil || got.Status != model.AlertOpen {
		t.Fatalf("alert not re-opened: %v %v", err, got)
	}

	// Second tick: nothing left to wake.
	en.wakeSnoozed(ctx, time.Now().UTC())
	if len(rec.starts) != 1 {
		t.Fatalf("double restart: %d", len(rec.starts))
	}
}

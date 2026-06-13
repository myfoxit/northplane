package server

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestSuperviseWorkerRecoversPanic is the core robustness guarantee: a
// panicking background worker must not crash the process — superviseWorker
// recovers and reports that a restart is warranted.
func TestSuperviseWorkerRecoversPanic(t *testing.T) {
	panicked := superviseWorker(context.Background(), quietLogger(), "boom",
		func(context.Context) { panic("kaboom") })
	if !panicked {
		t.Fatal("a panicking worker must report panicked=true (so it restarts)")
	}

	clean := superviseWorker(context.Background(), quietLogger(), "clean",
		func(context.Context) {})
	if clean {
		t.Fatal("a worker that returns normally must report panicked=false")
	}
}

// TestSuperviseWorkerRestartsUntilStable mirrors the spawn() loop: a worker
// that panics a few times then succeeds is restarted each time and the
// process survives.
func TestSuperviseWorkerRestartsUntilStable(t *testing.T) {
	var runs atomic.Int32
	fn := func(context.Context) {
		if runs.Add(1) < 3 {
			panic("transient")
		}
	}
	for superviseWorker(context.Background(), quietLogger(), "flaky", fn) {
		// loop: restart on panic, exactly as spawn() does
	}
	if got := runs.Load(); got != 3 {
		t.Fatalf("worker ran %d times, want 3 (2 panics + 1 clean)", got)
	}
}

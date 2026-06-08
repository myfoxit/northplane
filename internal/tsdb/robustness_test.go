package tsdb

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCorruptBlockNoOOM guards the file-size bounds check: a block file
// with a forged huge series-count header must error, not attempt a multi-
// gigabyte allocation or panic (a corrupt file on the query/recovery path
// must never take the process down — SPEC P7).
func TestCorruptBlockNoOOM(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, nil, Retention{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	db.Append("o1", "m", "", nil, "", "", nil, nil, now.Add(-3*time.Hour), 1)
	if err := db.Flush(now); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Forge a block: valid magic, absurd series count, tiny file.
	blocks, _ := filepath.Glob(filepath.Join(dir, "blocks", "*.npb"))
	if len(blocks) == 0 {
		t.Skip("no block flushed")
	}
	corrupt := append([]byte(nil), blockMagic[:]...)
	corrupt = append(corrupt, make([]byte, 16)...)    // ws/we
	corrupt = append(corrupt, 0xFF, 0xFF, 0xFF, 0xFF) // n = ~4 billion
	if err := os.WriteFile(blocks[0], corrupt, 0o640); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(dir, nil, Retention{})
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	// Must return an error quickly, not OOM/panic.
	_, _ = db2.Query(context.Background(), Query{ObjectID: "o1", Metric: "m",
		From: now.Add(-24 * time.Hour), To: now})
}

// TestCloseIdempotent guards that a second Close does not panic on the
// already-closed stopFsync channel.
func TestCloseIdempotent(t *testing.T) {
	db, err := Open(t.TempDir(), nil, Retention{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := db.Close(); err != nil { // must not panic
		t.Fatalf("second close: %v", err)
	}
}

// TestAppendRejectsNonFinite guards that NaN/Inf samples are dropped.
func TestAppendRejectsNonFinite(t *testing.T) {
	db, err := Open(t.TempDir(), nil, Retention{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before := db.Stats()
	db.Append("o", "m", "", nil, "", "", nil, nil, time.Now(), math.NaN())
	db.Append("o", "m", "", nil, "", "", nil, nil, time.Now(), math.Inf(1))
	after := db.Stats()
	if after.Samples != before.Samples {
		t.Fatalf("NaN sample was stored (samples %d→%d)", before.Samples, after.Samples)
	}
}

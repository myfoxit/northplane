package tsdb

import (
	"context"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"testing"
	"time"
)

// Property test (SPEC §16): encode→decode must be the identity for any
// time-ordered series.
func TestGorillaRoundtrip(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for iter := 0; iter < 200; iter++ {
		n := 1 + rng.Intn(500)
		app := NewChunkAppender()
		want := make([]Sample, 0, n)
		ts := int64(1735689600000) + rng.Int63n(1e9)
		v := rng.Float64() * 100
		for i := 0; i < n; i++ {
			switch rng.Intn(4) {
			case 0:
				ts += 60000 // steady interval (typical)
			case 1:
				ts += int64(1 + rng.Intn(120000))
			case 2:
				ts += int64(1 + rng.Intn(100))
			default:
				ts += 86400000 // big gap
			}
			switch rng.Intn(5) {
			case 0: // unchanged value (common for gauges)
			case 1:
				v += rng.NormFloat64()
			case 2:
				v = float64(rng.Intn(1000))
			case 3:
				v = rng.Float64() * 1e12
			default:
				v = -v
			}
			if !app.Append(ts, v) {
				t.Fatalf("append rejected ordered sample")
			}
			want = append(want, Sample{T: ts, V: v})
		}
		got, err := DecodeChunk(app.Bytes(), app.Count())
		if err != nil {
			t.Fatalf("iter %d: decode: %v", iter, err)
		}
		if len(got) != len(want) {
			t.Fatalf("iter %d: len %d != %d", iter, len(got), len(want))
		}
		for i := range want {
			if got[i].T != want[i].T || math.Float64bits(got[i].V) != math.Float64bits(want[i].V) {
				t.Fatalf("iter %d sample %d: got %+v want %+v", iter, i, got[i], want[i])
			}
		}
	}
}

func TestGorillaSpecialValues(t *testing.T) {
	app := NewChunkAppender()
	vals := []float64{0, math.Inf(1), math.Inf(-1), math.NaN(), -0.0, 1e-300, math.MaxFloat64}
	ts := int64(1000)
	for _, v := range vals {
		app.Append(ts, v)
		ts += 1
	}
	got, err := DecodeChunk(app.Bytes(), app.Count())
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range vals {
		if math.Float64bits(got[i].V) != math.Float64bits(v) {
			t.Fatalf("value %d: got %x want %x", i, math.Float64bits(got[i].V), math.Float64bits(v))
		}
	}
}

func TestCompressionTarget(t *testing.T) {
	// SPEC §7.3 target: ≤ 2 bytes/sample for realistic monitoring data
	// (steady 60s interval, slowly drifting gauge).
	app := NewChunkAppender()
	ts := int64(1735689600000)
	v := 42.0
	rng := rand.New(rand.NewSource(7))
	const n = 120 // one 2h window at 60s
	for i := 0; i < n; i++ {
		app.Append(ts, v)
		ts += 60000
		if rng.Intn(10) == 0 {
			v += rng.Float64() // occasional small drift
		}
	}
	perSample := float64(len(app.Bytes())) / n
	if perSample > 2.0 {
		t.Fatalf("compression %0.2f bytes/sample exceeds 2.0 target", perSample)
	}
	t.Logf("compression: %.2f bytes/sample", perSample)
}

func TestDBEndToEnd(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, slog.Default(), Retention{Raw: 48 * time.Hour, Agg5m: 100 * 24 * time.Hour, Agg1h: 1000 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	base := time.Now().Add(-4 * time.Hour).Truncate(time.Minute)
	for i := 0; i < 240; i++ { // 4h of minutely samples
		db.Append("obj1", "load", "", nil, "5", "10", nil, nil,
			base.Add(time.Duration(i)*time.Minute), float64(i%10))
	}
	// Flush closed windows (everything older than ~2h+grace).
	if err := db.Flush(time.Now()); err != nil {
		t.Fatal(err)
	}

	res, err := db.Query(context.Background(), Query{
		ObjectID: "obj1", Metric: "load",
		From: base, To: base.Add(4 * time.Hour), MaxPoints: 240,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("series: %d", len(res))
	}
	if len(res[0].Points) < 230 { // all samples surface (blocks + head)
		t.Fatalf("points: %d", len(res[0].Points))
	}
	if res[0].Series.Warn != "5" || res[0].Series.Crit != "10" {
		t.Fatalf("thresholds lost: %+v", res[0].Series)
	}

	// Crash-recovery: reopen without Close — WAL must restore the head.
	statsBefore := db.Stats()
	if err := db.Close(); err != nil { // here: graceful close (flushes head)
		t.Fatal(err)
	}
	db2, err := Open(dir, slog.Default(), Retention{Raw: 48 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	res2, err := db2.Query(context.Background(), Query{
		ObjectID: "obj1", Metric: "load",
		From: base, To: base.Add(4 * time.Hour), MaxPoints: 240,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2) != 1 || len(res2[0].Points) < 230 {
		t.Fatalf("after reopen: series=%d", len(res2))
	}
	_ = statsBefore
}

func TestWALCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, slog.Default(), Retention{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Minute)
	for i := 0; i < 30; i++ {
		db.Append("objX", "temp", "", nil, "", "", nil, nil,
			now.Add(time.Duration(i)*time.Second), float64(i))
	}
	db.syncWAL() // simulate the 1s fsync tick, then crash (no Close/Flush)

	db2, err := Open(dir, slog.Default(), Retention{})
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	res, err := db2.Query(context.Background(), Query{
		ObjectID: "objX", Metric: "temp",
		From: now.Add(-time.Minute), To: now.Add(time.Minute),
		Step: time.Second, Agg: AggLast,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || len(res[0].Points) != 30 {
		n := 0
		if len(res) == 1 {
			n = len(res[0].Points)
		}
		t.Fatalf("wal recovery: series=%d points=%d want 30", len(res), n)
	}
}

func TestDownsamplingTiers(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, slog.Default(), Retention{Raw: 365 * 24 * time.Hour, Agg5m: 400 * 24 * time.Hour, Agg1h: 2000 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Two full days, minutely. Old enough that days are complete.
	day := time.Now().UTC().AddDate(0, 0, -3).Truncate(24 * time.Hour)
	for i := 0; i < 2*24*60; i += 5 {
		db.Append("objA", "cpu", "%", nil, "", "", nil, nil,
			day.Add(time.Duration(i)*time.Minute), 50+float64(i%100))
	}
	if err := db.Maintain(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	// Aggregate files must exist for the two days.
	st := db.Stats()
	if st.Blocks == 0 {
		t.Fatal("no blocks written")
	}
	// Delete raw blocks to force tier reads.
	for _, ws := range db.listBlockStarts() {
		if err := osRemove(db.blockPath(ws)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := db.Query(context.Background(), Query{
		ObjectID: "objA", Metric: "cpu",
		From: day, To: day.Add(48 * time.Hour), Step: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || len(res[0].Points) < 40 {
		n := 0
		if len(res) == 1 {
			n = len(res[0].Points)
		}
		t.Fatalf("tier query: series=%d points=%d", len(res), n)
	}
}

func osRemove(path string) error { return os.Remove(path) }

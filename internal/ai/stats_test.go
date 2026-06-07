package ai

import (
	"math"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/tsdb"
)

func mkSeries(start time.Time, n int, fn func(i int) float64) []tsdb.Sample {
	out := make([]tsdb.Sample, n)
	for i := 0; i < n; i++ {
		out[i] = tsdb.Sample{T: start.Add(time.Duration(i) * time.Minute).UnixMilli(), V: fn(i)}
	}
	return out
}

func TestBaselineAndAnomalies(t *testing.T) {
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// Operational usage (SPEC §10.6): baseline from a 4-week history
	// window, detection on fresh data.
	history := mkSeries(start, 7*24*60, func(i int) float64 {
		return 50 + math.Sin(float64(i)/60)*2
	})
	b := ComputeBaseline(history)
	if b.Mean < 45 || b.Mean > 55 {
		t.Fatalf("mean: %f", b.Mean)
	}
	fresh := mkSeries(start.AddDate(0, 0, 7), 24*60, func(i int) float64 {
		v := 50 + math.Sin(float64(i)/60)*2
		if i >= 600 && i < 610 {
			v = 500 // anomaly burst (10 samples ≥ minRun)
		}
		return v
	})
	anomalies := DetectAnomalies(fresh, b, 5, 3)
	if len(anomalies) < 8 || len(anomalies) > 14 {
		t.Fatalf("anomalies: %d (want ~10)", len(anomalies))
	}
	// short blips below minRun are ignored (alert-fatigue guard)
	points2 := mkSeries(start, 500, func(i int) float64 {
		if i == 250 {
			return 500
		}
		return 50
	})
	b2 := ComputeBaseline(points2)
	if got := DetectAnomalies(points2, b2, 5, 3); len(got) != 0 {
		t.Fatalf("single blip must not alert: %d", len(got))
	}
}

func TestAdaptiveThresholds(t *testing.T) {
	start := time.Now().Add(-24 * time.Hour)
	points := mkSeries(start, 1000, func(i int) float64 { return float64(i % 100) })
	th := ComputeAdaptiveThresholds(points, 0, 0)
	if th.Warn < 90 || th.Warn > 99 || th.Crit < th.Warn {
		t.Fatalf("thresholds: %+v", th)
	}
	// clamps
	th2 := ComputeAdaptiveThresholds(points, 95, 98)
	if th2.Warn < 95 || th2.Crit > 98 {
		t.Fatalf("clamps: %+v", th2)
	}
}

func TestForecastDiskFull(t *testing.T) {
	start := time.Now().Add(-9 * 24 * time.Hour)
	// disk filling 1%/day from 50%, threshold 100% → ~41 days from start
	points := make([]tsdb.Sample, 0, 9*24)
	for h := 0; h < 9*24; h++ {
		points = append(points, tsdb.Sample{
			T: start.Add(time.Duration(h) * time.Hour).UnixMilli(),
			V: 50 + float64(h)/24.0,
		})
	}
	f := ComputeForecast(points, 100)
	if f.HitsThreshold == nil {
		t.Fatal("no threshold hit projected")
	}
	daysOut := time.Until(*f.HitsThreshold).Hours() / 24
	if daysOut < 35 || daysOut > 47 {
		t.Fatalf("projection off: %.1f days (want ~41)", daysOut)
	}
	if f.Confidence < 0.99 {
		t.Fatalf("linear data should fit: R²=%f", f.Confidence)
	}
}

func TestRedaction(t *testing.T) {
	r := NewRedactor(configRedaction(true))
	in := "login failed for admin@example.com from 10.1.2.3 using token np_a1b2c3d4e5f6a1b2c3d4 password=hunter2 on db-prod-01.example.net"
	out := r.Redact(in)
	for _, leak := range []string{"admin@example.com", "10.1.2.3", "np_a1b2c3d4e5f6a1b2c3d4", "hunter2", "db-prod-01.example.net"} {
		if contains(out, leak) {
			t.Fatalf("leak %q in %q", leak, out)
		}
	}
	// pseudonyms are stable
	out2 := r.Redact("db-prod-01.example.net again")
	h1 := extractHost(out)
	if h1 == "" || !contains(out2, h1) {
		t.Fatalf("pseudonym not stable: %q vs %q", out, out2)
	}
}

func configRedaction(pseudo bool) (c config.RedactionConfig) {
	if pseudo {
		c.Hostnames = "pseudonymize"
	}
	return c
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func extractHost(s string) string {
	i := indexOf(s, "host-")
	if i < 0 {
		return ""
	}
	return s[i : i+9]
}

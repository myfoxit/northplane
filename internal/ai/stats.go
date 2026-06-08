package ai

import (
	"math"
	"sort"
	"time"

	"github.com/northplane/northplane/internal/tsdb"
)

// Deterministic statistics (SPEC §10.6) — no LLM. EWMA + seasonality
// baselines, MAD-based anomaly detection, adaptive thresholds from
// quantiles, and linear+seasonal capacity forecasts. These run with
// provider=none (N6: statistics, not guessed thresholds).

// Baseline summarises a series' normal behaviour.
type Baseline struct {
	Mean   float64 `json:"mean"`
	MAD    float64 `json:"mad"` // median absolute deviation
	StdDev float64 `json:"stdDev"`
	// Seasonal[hour*7+weekday] = expected value (4-week window).
	Seasonal []float64 `json:"-"`
	N        int       `json:"samples"`
}

// ComputeBaseline derives a baseline from raw samples. The seasonal
// table is keyed by (hour-of-day × weekday) — SPEC §10.6.
func ComputeBaseline(points []tsdb.Sample) Baseline {
	if len(points) == 0 {
		return Baseline{}
	}
	values := make([]float64, len(points))
	for i, p := range points {
		values[i] = p.V
	}
	mean := meanOf(values)
	b := Baseline{Mean: mean, StdDev: stdDev(values, mean),
		MAD: medianAbsDev(values), N: len(values),
		Seasonal: make([]float64, 24*7)}

	// EWMA over the seasonal buckets
	counts := make([]int, 24*7)
	const alpha = 0.3
	for _, p := range points {
		t := time.UnixMilli(p.T).UTC()
		idx := t.Hour()*7 + int(t.Weekday())
		if counts[idx] == 0 {
			b.Seasonal[idx] = p.V
		} else {
			b.Seasonal[idx] = alpha*p.V + (1-alpha)*b.Seasonal[idx]
		}
		counts[idx]++
	}
	// fill empty buckets with the global mean
	for i := range b.Seasonal {
		if counts[i] == 0 {
			b.Seasonal[i] = mean
		}
	}
	return b
}

// SeasonalExpected returns the baseline value for a timestamp.
func (b Baseline) SeasonalExpected(t time.Time) float64 {
	if len(b.Seasonal) != 24*7 {
		return b.Mean
	}
	return b.Seasonal[t.UTC().Hour()*7+int(t.UTC().Weekday())]
}

// Anomaly is a detected deviation.
type Anomaly struct {
	At        time.Time `json:"at"`
	Value     float64   `json:"value"`
	Expected  float64   `json:"expected"`
	Deviation float64   `json:"deviationMad"` // |x-expected| / MAD
}

// DetectAnomalies flags points beyond k×MAD from the seasonal baseline
// for at least minDuration consecutive samples (conservative against
// alert fatigue, SPEC §10.6).
func DetectAnomalies(points []tsdb.Sample, b Baseline, k float64, minRun int) []Anomaly {
	if k <= 0 {
		k = 5
	}
	if minRun <= 0 {
		minRun = 3
	}
	mad := b.MAD
	if mad < 1e-9 {
		mad = b.StdDev // fall back to stddev for near-constant series
	}
	if mad < 1e-9 {
		return nil
	}
	var anomalies []Anomaly
	run := 0
	var runStart []Anomaly
	for _, p := range points {
		t := time.UnixMilli(p.T).UTC()
		expected := b.SeasonalExpected(t)
		dev := math.Abs(p.V-expected) / mad
		if dev > k {
			run++
			runStart = append(runStart, Anomaly{At: t, Value: p.V, Expected: expected, Deviation: dev})
		} else {
			if run >= minRun {
				anomalies = append(anomalies, runStart...)
			}
			run = 0
			runStart = nil
		}
	}
	if run >= minRun {
		anomalies = append(anomalies, runStart...)
	}
	return anomalies
}

// AdaptiveThresholds derives warn/crit from baseline quantiles
// (P98/P99.5) with min/max clamps (SPEC §10.6).
type AdaptiveThresholds struct {
	Warn float64 `json:"warn"`
	Crit float64 `json:"crit"`
}

// ComputeAdaptiveThresholds returns P98/P99.5 of the sample distribution.
func ComputeAdaptiveThresholds(points []tsdb.Sample, minWarn, maxCrit float64) AdaptiveThresholds {
	if len(points) < 20 {
		return AdaptiveThresholds{}
	}
	values := make([]float64, len(points))
	for i, p := range points {
		values[i] = p.V
	}
	sort.Float64s(values)
	warn := quantile(values, 0.98)
	crit := quantile(values, 0.995)
	if minWarn > 0 && warn < minWarn {
		warn = minWarn
	}
	if maxCrit > 0 && crit > maxCrit {
		crit = maxCrit
	}
	return AdaptiveThresholds{Warn: warn, Crit: crit}
}

// Forecast is a linear+seasonal capacity projection (SPEC §10.6).
type Forecast struct {
	SlopePerHour  float64    `json:"slopePerHour"`
	Projected     float64    `json:"projectedValue"`
	HitsThreshold *time.Time `json:"hitsThresholdAt,omitempty"`
	Confidence    float64    `json:"confidence"` // R²
}

// ComputeForecast fits a least-squares trend and projects when the
// series reaches `threshold` (e.g. "disk full in ~9 days").
func ComputeForecast(points []tsdb.Sample, threshold float64) Forecast {
	if len(points) < 10 {
		return Forecast{}
	}
	t0 := points[0].T
	var sumX, sumY, sumXY, sumXX, sumYY float64
	n := float64(len(points))
	for _, p := range points {
		x := float64(p.T-t0) / float64(time.Hour.Milliseconds())
		y := p.V
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
		sumYY += y * y
	}
	denom := n*sumXX - sumX*sumX
	if math.Abs(denom) < 1e-9 {
		return Forecast{}
	}
	slope := (n*sumXY - sumX*sumY) / denom
	intercept := (sumY - slope*sumX) / n

	// R²
	meanY := sumY / n
	var ssTot, ssRes float64
	for _, p := range points {
		x := float64(p.T-t0) / float64(time.Hour.Milliseconds())
		pred := intercept + slope*x
		ssRes += (p.V - pred) * (p.V - pred)
		ssTot += (p.V - meanY) * (p.V - meanY)
	}
	r2 := 0.0
	if ssTot > 1e-9 {
		r2 = 1 - ssRes/ssTot
	}

	f := Forecast{SlopePerHour: slope, Confidence: r2}
	lastX := float64(points[len(points)-1].T-t0) / float64(time.Hour.Milliseconds())
	f.Projected = intercept + slope*lastX

	if slope != 0 && threshold != 0 {
		hoursToHit := (threshold - f.Projected) / slope
		if hoursToHit > 0 && hoursToHit < 24*365 {
			hit := time.UnixMilli(points[len(points)-1].T).Add(
				time.Duration(hoursToHit * float64(time.Hour)))
			f.HitsThreshold = &hit
		}
	}
	return f
}

// --- helpers ---

func meanOf(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func stdDev(v []float64, mean float64) float64 {
	if len(v) < 2 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += (x - mean) * (x - mean)
	}
	return math.Sqrt(s / float64(len(v)-1))
}

func medianOf(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	n := len(c)
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

func medianAbsDev(v []float64) float64 {
	med := medianOf(v)
	dev := make([]float64, len(v))
	for i, x := range v {
		dev[i] = math.Abs(x - med)
	}
	return medianOf(dev) * 1.4826 // scale to ≈ stddev for normal data
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	frac := pos - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// Package metrics is the dependency-free self-metrics registry with
// OpenMetrics text exposition (SPEC §7.9: own registry, ~300 lines;
// §15.4: scheduler lag, queue depths, check rates, notification
// success, TSDB stats, SSE clients, AI tokens).
package metrics

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Counter is a monotonically increasing value.
type Counter struct{ v atomic.Uint64 }

func (c *Counter) Inc()          { c.v.Add(1) }
func (c *Counter) Add(n uint64)  { c.v.Add(n) }
func (c *Counter) Value() uint64 { return c.v.Load() }

// Gauge is a settable value.
type Gauge struct {
	mu sync.Mutex
	v  float64
}

func (g *Gauge) Set(v float64) { g.mu.Lock(); g.v = v; g.mu.Unlock() }
func (g *Gauge) Value() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.v
}

// DefaultBuckets are cumulative upper bounds (seconds) for latency
// histograms — the Prometheus client default ladder, fine for HTTP
// request durations.
var DefaultBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// Histogram is a cumulative latency distribution exported in the
// OpenMetrics histogram shape: per-bucket `_bucket{le="…"}`, plus
// `_sum` and `_count`. Buckets are fixed at construction; observations
// are lock-free (one counter per bucket + sum + count).
type Histogram struct {
	bounds  []float64
	buckets []atomic.Uint64 // len(bounds)+1; last is the +Inf overflow
	count   atomic.Uint64
	sumBits atomic.Uint64 // float64 sum via bit pattern (lock-free add)
}

func newHistogram(bounds []float64) *Histogram {
	b := append([]float64(nil), bounds...)
	sort.Float64s(b)
	return &Histogram{bounds: b, buckets: make([]atomic.Uint64, len(b)+1)}
}

// Observe records one sample (seconds).
func (h *Histogram) Observe(v float64) {
	// SearchFloat64s returns the smallest index i with bounds[i] >= v —
	// exactly the cumulative `le` bucket for v (v == bound stays in that
	// bucket). Index len(bounds) is the +Inf overflow bucket.
	i := sort.SearchFloat64s(h.bounds, v)
	h.buckets[i].Add(1)
	h.count.Add(1)
	for {
		old := h.sumBits.Load()
		nw := math.Float64bits(math.Float64frombits(old) + v)
		if h.sumBits.CompareAndSwap(old, nw) {
			break
		}
	}
}

// Registry holds named series; label sets are encoded in the name
// (`np_notifications_total{channel="email",result="sent"}`).
type Registry struct {
	mu         sync.RWMutex
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	histograms map[string]*Histogram
	help       map[string]string
	// callbacks pull gauge families at scrape time (subsystem Stats()).
	collectors []func(set func(name string, v float64))
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		counters:   map[string]*Counter{},
		gauges:     map[string]*Gauge{},
		histograms: map[string]*Histogram{},
		help:       map[string]string{},
	}
}

// Counter returns (creating) a counter.
func (r *Registry) Counter(name, help string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.counters[name]; ok {
		return c
	}
	c := &Counter{}
	r.counters[name] = c
	r.helpFor(name, help)
	return c
}

// Gauge returns (creating) a gauge.
func (r *Registry) Gauge(name, help string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.gauges[name]; ok {
		return g
	}
	g := &Gauge{}
	r.gauges[name] = g
	r.helpFor(name, help)
	return g
}

// Histogram returns (creating) a histogram with DefaultBuckets. The
// name carries any label set just like counters/gauges
// (`np_http_request_duration_seconds{method="GET"}`); the `le` bucket
// label is appended at render time.
func (r *Registry) Histogram(name, help string) *Histogram {
	return r.HistogramVec(name, help, DefaultBuckets)
}

// HistogramVec is Histogram with explicit bucket bounds.
func (r *Registry) HistogramVec(name, help string, buckets []float64) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.histograms[name]; ok {
		return h
	}
	h := newHistogram(buckets)
	r.histograms[name] = h
	r.helpFor(name, help)
	return h
}

func (r *Registry) helpFor(name, help string) {
	base := name
	if i := strings.IndexByte(base, '{'); i >= 0 {
		base = base[:i]
	}
	if help != "" {
		r.help[base] = help
	}
}

// Collect registers a scrape-time callback.
func (r *Registry) Collect(fn func(set func(name string, v float64))) {
	r.mu.Lock()
	r.collectors = append(r.collectors, fn)
	r.mu.Unlock()
}

// Render writes the OpenMetrics text format.
func (r *Registry) Render(w *strings.Builder) {
	r.mu.RLock()
	collectors := append([]func(func(string, float64)){}, r.collectors...)
	r.mu.RUnlock()

	// pull dynamic gauges
	dyn := map[string]float64{}
	var dynMu sync.Mutex
	for _, fn := range collectors {
		fn(func(name string, v float64) {
			dynMu.Lock()
			dyn[name] = v
			dynMu.Unlock()
		})
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	type row struct {
		name string
		val  string
		kind string
	}
	var rows []row
	for name, c := range r.counters {
		rows = append(rows, row{name, fmt.Sprint(c.Value()), "counter"})
	}
	for name, g := range r.gauges {
		rows = append(rows, row{name, trim(g.Value()), "gauge"})
	}
	for name, v := range dyn {
		rows = append(rows, row{name, trim(v), "gauge"})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

	seen := map[string]bool{}
	for _, rw := range rows {
		base := rw.name
		if i := strings.IndexByte(base, '{'); i >= 0 {
			base = base[:i]
		}
		if !seen[base] {
			seen[base] = true
			if h := r.help[base]; h != "" {
				fmt.Fprintf(w, "# HELP %s %s\n", base, h)
			}
			fmt.Fprintf(w, "# TYPE %s %s\n", base, rw.kind)
		}
		fmt.Fprintf(w, "%s %s\n", rw.name, rw.val)
	}

	r.renderHistograms(w, seen)
	w.WriteString("# EOF\n")
}

// renderHistograms emits the histogram families (`_bucket`/`_sum`/
// `_count`) in OpenMetrics shape. Called with r.mu held (RLock).
func (r *Registry) renderHistograms(w *strings.Builder, seen map[string]bool) {
	names := make([]string, 0, len(r.histograms))
	for name := range r.histograms {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		h := r.histograms[name]
		base, labels := name, ""
		if i := strings.IndexByte(name, '{'); i >= 0 {
			base, labels = name[:i], name[i:]
		}
		if !seen[base] {
			seen[base] = true
			if help := r.help[base]; help != "" {
				fmt.Fprintf(w, "# HELP %s %s\n", base, help)
			}
			fmt.Fprintf(w, "# TYPE %s histogram\n", base)
		}
		var cum uint64
		for i, bound := range h.bounds {
			cum += h.buckets[i].Load()
			fmt.Fprintf(w, "%s_bucket%s %d\n", base, withLE(labels, trim(bound)), cum)
		}
		cum += h.buckets[len(h.bounds)].Load() // +Inf overflow
		fmt.Fprintf(w, "%s_bucket%s %d\n", base, withLE(labels, "+Inf"), cum)
		fmt.Fprintf(w, "%s_sum%s %s\n", base, labels, trim(math.Float64frombits(h.sumBits.Load())))
		fmt.Fprintf(w, "%s_count%s %d\n", base, labels, h.count.Load())
	}
}

// withLE injects an le="…" label into an existing label set
// (`{method="GET"}` → `{method="GET",le="0.5"}`), or creates one.
func withLE(labels, le string) string {
	le = `le="` + le + `"`
	if labels == "" {
		return "{" + le + "}"
	}
	return labels[:len(labels)-1] + "," + le + "}"
}

func trim(v float64) string {
	s := fmt.Sprintf("%g", v)
	return s
}

// Handler serves the exposition endpoint.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		r.Render(&b)
		w.Header().Set("Content-Type", "application/openmetrics-text; version=1.0.0; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	})
}

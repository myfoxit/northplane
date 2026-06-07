// Package metrics is the dependency-free self-metrics registry with
// OpenMetrics text exposition (SPEC §7.9: own registry, ~300 lines;
// §15.4: scheduler lag, queue depths, check rates, notification
// success, TSDB stats, SSE clients, AI tokens).
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Counter is a monotonically increasing value.
type Counter struct{ v atomic.Uint64 }

func (c *Counter) Inc()             { c.v.Add(1) }
func (c *Counter) Add(n uint64)     { c.v.Add(n) }
func (c *Counter) Value() uint64    { return c.v.Load() }

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

// Registry holds named series; label sets are encoded in the name
// (`np_notifications_total{channel="email",result="sent"}`).
type Registry struct {
	mu       sync.RWMutex
	counters map[string]*Counter
	gauges   map[string]*Gauge
	help     map[string]string
	// callbacks pull gauge families at scrape time (subsystem Stats()).
	collectors []func(set func(name string, v float64))
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		counters: map[string]*Counter{},
		gauges:   map[string]*Gauge{},
		help:     map[string]string{},
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
	w.WriteString("# EOF\n")
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

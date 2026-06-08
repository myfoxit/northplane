package metrics

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// render is a helper that renders the registry to a string.
func render(r *Registry) string {
	var b strings.Builder
	r.Render(&b)
	return b.String()
}

// line reports whether s contains exact line `want` (bounded by newlines).
func hasLine(s, want string) bool {
	for _, l := range strings.Split(s, "\n") {
		if l == want {
			return true
		}
	}
	return false
}

// countOccurrences counts how many lines equal want.
func countLines(s, want string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if l == want {
			n++
		}
	}
	return n
}

// --- Counter / Gauge primitives -------------------------------------------

func TestCounter_IncAddValue(t *testing.T) {
	var c Counter
	if c.Value() != 0 {
		t.Fatalf("fresh counter = %d, want 0", c.Value())
	}
	c.Inc()
	c.Inc()
	c.Add(5)
	if got := c.Value(); got != 7 {
		t.Errorf("Value = %d, want 7", got)
	}
}

func TestGauge_SetValue(t *testing.T) {
	var g Gauge
	if g.Value() != 0 {
		t.Fatalf("fresh gauge = %v, want 0", g.Value())
	}
	g.Set(3.5)
	g.Set(-2.25)
	if got := g.Value(); got != -2.25 {
		t.Errorf("Value = %v, want -2.25 (last write wins)", got)
	}
}

// --- Registry create-or-get ------------------------------------------------

func TestRegistry_CounterCreateOrGet(t *testing.T) {
	r := NewRegistry()
	c1 := r.Counter("np_x_total", "help")
	c2 := r.Counter("np_x_total", "different help")
	if c1 != c2 {
		t.Fatal("Counter with same name must return the same instance")
	}
	c1.Inc()
	if c2.Value() != 1 {
		t.Errorf("shared instance Value = %d, want 1", c2.Value())
	}
}

func TestRegistry_GaugeCreateOrGet(t *testing.T) {
	r := NewRegistry()
	g1 := r.Gauge("np_y", "h")
	g2 := r.Gauge("np_y", "h")
	if g1 != g2 {
		t.Fatal("Gauge with same name must return the same instance")
	}
	g1.Set(42)
	if g2.Value() != 42 {
		t.Errorf("shared gauge Value = %v, want 42", g2.Value())
	}
}

func TestRegistry_HistogramCreateOrGet(t *testing.T) {
	r := NewRegistry()
	h1 := r.Histogram("np_dur_seconds", "h")
	h2 := r.Histogram("np_dur_seconds", "h")
	if h1 != h2 {
		t.Fatal("Histogram with same name must return the same instance")
	}
}

// --- Render: format & values ----------------------------------------------

func TestRender_CounterAndGaugeFormat(t *testing.T) {
	r := NewRegistry()
	r.Counter("np_requests_total", "total requests").Add(3)
	r.Gauge("np_temp_celsius", "temperature").Set(36.6)

	out := render(r)

	for _, want := range []string{
		"# HELP np_requests_total total requests",
		"# TYPE np_requests_total counter",
		"np_requests_total 3",
		"# HELP np_temp_celsius temperature",
		"# TYPE np_temp_celsius gauge",
		"np_temp_celsius 36.6",
	} {
		if !hasLine(out, want) {
			t.Errorf("missing line %q in:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "# EOF\n") {
		t.Errorf("exposition must end with # EOF, got:\n%s", out)
	}
}

func TestRender_SortedAndStableOrdering(t *testing.T) {
	r := NewRegistry()
	r.Counter("np_b_total", "b").Inc()
	r.Counter("np_a_total", "a").Inc()
	r.Gauge("np_c", "c").Set(1)

	out := render(r)
	ai := strings.Index(out, "np_a_total 1")
	bi := strings.Index(out, "np_b_total 1")
	ci := strings.Index(out, "np_c 1")
	if !(ai < bi && bi < ci) {
		t.Errorf("rows not sorted by name: a=%d b=%d c=%d\n%s", ai, bi, ci, out)
	}
}

func TestRender_NoHelpOmitsHelpLine(t *testing.T) {
	r := NewRegistry()
	r.Counter("np_nohelp_total", "").Inc()
	out := render(r)
	if strings.Contains(out, "# HELP np_nohelp_total") {
		t.Errorf("empty help must not emit a HELP line:\n%s", out)
	}
	if !hasLine(out, "# TYPE np_nohelp_total counter") {
		t.Errorf("TYPE line still required:\n%s", out)
	}
}

func TestRender_LabelsInName(t *testing.T) {
	r := NewRegistry()
	// Label set encoded in the name; base name drives the single TYPE line.
	r.Counter(`np_notifications_total{channel="email",result="sent"}`, "notifs").Add(2)
	r.Counter(`np_notifications_total{channel="sms",result="sent"}`, "notifs").Inc()

	out := render(r)

	if c := countLines(out, "# TYPE np_notifications_total counter"); c != 1 {
		t.Errorf("shared base name must render exactly one # TYPE line, got %d:\n%s", c, out)
	}
	if c := countLines(out, "# HELP np_notifications_total notifs"); c != 1 {
		t.Errorf("shared base name must render exactly one # HELP line, got %d:\n%s", c, out)
	}
	if !hasLine(out, `np_notifications_total{channel="email",result="sent"} 2`) {
		t.Errorf("missing labeled email series:\n%s", out)
	}
	if !hasLine(out, `np_notifications_total{channel="sms",result="sent"} 1`) {
		t.Errorf("missing labeled sms series:\n%s", out)
	}
}

func TestRender_GaugeValueFormatting(t *testing.T) {
	tests := []struct {
		set  float64
		want string
	}{
		{0, "np_v 0"},
		{1, "np_v 1"},
		{1.5, "np_v 1.5"},
		{-3.25, "np_v -3.25"},
		{1000000, "np_v 1e+06"}, // %g switches to exponent
	}
	for _, tc := range tests {
		r := NewRegistry()
		r.Gauge("np_v", "").Set(tc.set)
		out := render(r)
		if !hasLine(out, tc.want) {
			t.Errorf("Set(%v): want line %q in:\n%s", tc.set, tc.want, out)
		}
	}
}

// --- Collectors (dynamic gauges) -------------------------------------------

func TestRender_CollectorGauges(t *testing.T) {
	r := NewRegistry()
	r.Gauge("np_static", "s").Set(10) // gives np_static a TYPE/HELP base
	r.Collect(func(set func(string, float64)) {
		set("np_dynamic", 99)
	})
	out := render(r)
	if !hasLine(out, "np_dynamic 99") {
		t.Errorf("collector-provided gauge missing:\n%s", out)
	}
	if !hasLine(out, "# TYPE np_dynamic gauge") {
		t.Errorf("collector gauge needs a TYPE line:\n%s", out)
	}
}

// --- Histogram: bucketing & exposition -------------------------------------

func TestHistogram_BucketDistribution(t *testing.T) {
	r := NewRegistry()
	h := r.HistogramVec("np_lat_seconds", "latency", []float64{1, 2, 5})

	// Observations: which le-bucket each lands in (cumulative semantics):
	//   0.5 -> le=1 ; 1 -> le=1 (==bound stays) ; 1.5 -> le=2 ;
	//   2 -> le=2 ; 7 -> +Inf
	for _, v := range []float64{0.5, 1, 1.5, 2, 7} {
		h.Observe(v)
	}

	out := render(r)

	// Cumulative bucket counts:
	//   le=1   : 0.5, 1            -> 2
	//   le=2   : +1.5, +2          -> 4
	//   le=5   : (nothing new)     -> 4
	//   le=+Inf: +7                -> 5
	wantLines := []string{
		`np_lat_seconds_bucket{le="1"} 2`,
		`np_lat_seconds_bucket{le="2"} 4`,
		`np_lat_seconds_bucket{le="5"} 4`,
		`np_lat_seconds_bucket{le="+Inf"} 5`,
		`np_lat_seconds_count 5`,
		`np_lat_seconds_sum 12`, // 0.5+1+1.5+2+7
	}
	for _, w := range wantLines {
		if !hasLine(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
	if !hasLine(out, "# TYPE np_lat_seconds histogram") {
		t.Errorf("histogram needs a histogram TYPE line:\n%s", out)
	}
	if !hasLine(out, "# HELP np_lat_seconds latency") {
		t.Errorf("histogram HELP line missing:\n%s", out)
	}
}

func TestHistogram_BucketsAreCumulativeMonotonic(t *testing.T) {
	r := NewRegistry()
	h := r.HistogramVec("np_c_seconds", "", []float64{0.1, 0.5, 1})
	// All in the very first bucket.
	for i := 0; i < 4; i++ {
		h.Observe(0.05)
	}
	out := render(r)
	// Cumulative: every bucket including +Inf must be >= 4, and count 4.
	for _, w := range []string{
		`np_c_seconds_bucket{le="0.1"} 4`,
		`np_c_seconds_bucket{le="0.5"} 4`,
		`np_c_seconds_bucket{le="1"} 4`,
		`np_c_seconds_bucket{le="+Inf"} 4`,
		`np_c_seconds_count 4`,
	} {
		if !hasLine(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

func TestHistogram_EmptyHasInfAndZeroSum(t *testing.T) {
	r := NewRegistry()
	r.HistogramVec("np_empty_seconds", "", []float64{1, 2})
	out := render(r)
	for _, w := range []string{
		`np_empty_seconds_bucket{le="1"} 0`,
		`np_empty_seconds_bucket{le="2"} 0`,
		`np_empty_seconds_bucket{le="+Inf"} 0`,
		`np_empty_seconds_count 0`,
		`np_empty_seconds_sum 0`,
	} {
		if !hasLine(out, w) {
			t.Errorf("empty histogram missing %q in:\n%s", w, out)
		}
	}
}

func TestHistogram_UnsortedBoundsAreSorted(t *testing.T) {
	r := NewRegistry()
	// Bounds deliberately out of order; newHistogram must sort them so
	// SearchFloat64s and the rendered le order are correct.
	h := r.HistogramVec("np_sorted_seconds", "", []float64{5, 1, 2})
	h.Observe(1.5) // lands in le=2

	out := render(r)
	b1 := strings.Index(out, `np_sorted_seconds_bucket{le="1"}`)
	b2 := strings.Index(out, `np_sorted_seconds_bucket{le="2"}`)
	b5 := strings.Index(out, `np_sorted_seconds_bucket{le="5"}`)
	if !(b1 >= 0 && b1 < b2 && b2 < b5) {
		t.Errorf("buckets not rendered in ascending le order: 1=%d 2=%d 5=%d\n%s", b1, b2, b5, out)
	}
	if !hasLine(out, `np_sorted_seconds_bucket{le="2"} 1`) {
		t.Errorf("1.5 should fall into le=2:\n%s", out)
	}
}

func TestHistogram_DefaultBuckets(t *testing.T) {
	r := NewRegistry()
	h := r.Histogram("np_def_seconds", "")
	h.Observe(0.003) // below smallest default bound 0.005 -> le=0.005

	out := render(r)
	// Must contain the full default ladder + +Inf.
	for _, b := range DefaultBuckets {
		want := fmt.Sprintf(`np_def_seconds_bucket{le="%s"}`, trim(b))
		if !strings.Contains(out, want) {
			t.Errorf("default histogram missing bucket %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, `np_def_seconds_bucket{le="+Inf"} 1`) {
		t.Errorf("default histogram +Inf should be 1:\n%s", out)
	}
	if !hasLine(out, `np_def_seconds_bucket{le="0.005"} 1`) {
		t.Errorf("0.003 should fall into the le=0.005 bucket:\n%s", out)
	}
}

func TestHistogram_LabelLEInjection(t *testing.T) {
	r := NewRegistry()
	// Pre-existing label set: le must be appended as the last label.
	h := r.HistogramVec(`np_http_seconds{method="GET"}`, "http", []float64{1})
	h.Observe(0.5)

	out := render(r)

	if !hasLine(out, `np_http_seconds_bucket{method="GET",le="1"} 1`) {
		t.Errorf("le must be injected after existing labels:\n%s", out)
	}
	if !hasLine(out, `np_http_seconds_bucket{method="GET",le="+Inf"} 1`) {
		t.Errorf("missing labeled +Inf bucket:\n%s", out)
	}
	if !hasLine(out, `np_http_seconds_sum{method="GET"} 0.5`) {
		t.Errorf("_sum must keep the original label set (no le):\n%s", out)
	}
	if !hasLine(out, `np_http_seconds_count{method="GET"} 1`) {
		t.Errorf("_count must keep the original label set (no le):\n%s", out)
	}
	// Base TYPE line uses the bare base name, no labels.
	if !hasLine(out, "# TYPE np_http_seconds histogram") {
		t.Errorf("histogram TYPE line should use bare base name:\n%s", out)
	}
}

func TestWithLE(t *testing.T) {
	tests := []struct {
		labels string
		le     string
		want   string
	}{
		{"", "0.5", `{le="0.5"}`},
		{`{method="GET"}`, "1", `{method="GET",le="1"}`},
		{`{a="1",b="2"}`, "+Inf", `{a="1",b="2",le="+Inf"}`},
	}
	for _, tc := range tests {
		if got := withLE(tc.labels, tc.le); got != tc.want {
			t.Errorf("withLE(%q,%q) = %q, want %q", tc.labels, tc.le, got, tc.want)
		}
	}
}

// --- Histogram + counter sharing a base name -------------------------------

func TestRender_CounterAndHistogramShareBaseName_OneType(t *testing.T) {
	// A labeled counter family and a histogram share base name -> the
	// histogram's TYPE/HELP is suppressed because the base was already seen.
	r := NewRegistry()
	r.Counter(`np_shared{k="a"}`, "shared").Inc()
	r.HistogramVec(`np_shared{k="b"}`, "shared", []float64{1}).Observe(0.5)

	out := render(r)
	if c := countLines(out, "# TYPE np_shared counter") + countLines(out, "# TYPE np_shared histogram"); c != 1 {
		t.Errorf("shared base name must yield exactly one TYPE line total, got %d:\n%s", c, out)
	}
}

// --- Handler ---------------------------------------------------------------

func TestHandler_ServesExposition(t *testing.T) {
	r := NewRegistry()
	r.Counter("np_handler_total", "h").Inc()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	r.Handler().ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/openmetrics-text") {
		t.Errorf("Content-Type = %q, want openmetrics-text", ct)
	}
	body := rec.Body.String()
	if !hasLine(body, "np_handler_total 1") {
		t.Errorf("handler body missing series:\n%s", body)
	}
	if !strings.HasSuffix(body, "# EOF\n") {
		t.Errorf("handler body must end with # EOF:\n%s", body)
	}
}

// --- Concurrency (race detector) -------------------------------------------

func TestRegistry_ConcurrentCreateAndObserve(t *testing.T) {
	r := NewRegistry()
	const goroutines = 16
	const iters = 500

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				r.Counter("np_race_total", "c").Inc()
				r.Gauge("np_race_gauge", "g").Set(float64(i))
				r.HistogramVec("np_race_seconds", "h", []float64{1, 2}).Observe(0.5)
			}
		}()
	}
	wg.Wait()

	if got := r.Counter("np_race_total", "c").Value(); got != goroutines*iters {
		t.Errorf("counter under concurrency = %d, want %d", got, goroutines*iters)
	}

	out := render(r)
	wantCount := fmt.Sprintf("np_race_seconds_count %d", goroutines*iters)
	if !hasLine(out, wantCount) {
		t.Errorf("histogram count under concurrency wrong, want %q:\n%s", wantCount, out)
	}
}

func TestRender_ConcurrentRenderSafe(t *testing.T) {
	// Render must be safe while observations happen (RLock vs Observe).
	r := NewRegistry()
	h := r.HistogramVec("np_cr_seconds", "", []float64{1})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				h.Observe(0.5)
			}
		}
	}()
	for i := 0; i < 200; i++ {
		_ = render(r)
	}
	close(stop)
	wg.Wait()
}

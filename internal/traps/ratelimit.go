package traps

import (
	"sync"
	"sync/atomic"
	"time"
)

// Per-source token bucket, replicated locally from internal/api/ingress.go
// to honour the file-ownership boundary (the ingress bucket is unexported).
// Defaults match the ingress adapter so trap rate limits behave identically.
type bucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

var sourceBuckets sync.Map // sourceID → *bucket

// allowRate reports whether a trap from sourceID may pass, refilling the
// bucket at rate events/s up to burst. rate<=0 ⇒ 50/s, burst<=0 ⇒ 200.
func allowRate(sourceID string, rate float64, burst int) bool {
	if rate <= 0 {
		rate = 50 // default events/s per source (matches ingress.go)
	}
	if burst <= 0 {
		burst = 200
	}
	v, _ := sourceBuckets.LoadOrStore(sourceID, &bucket{tokens: float64(burst), last: time.Now()})
	b := v.(*bucket)
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * rate
	if b.tokens > float64(burst) {
		b.tokens = float64(burst)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// atomic64 is a tiny lifetime counter for self-metrics.
type atomic64 struct{ v atomic.Uint64 }

func (a *atomic64) add(n uint64) { a.v.Add(n) }
func (a *atomic64) load() uint64 { return a.v.Load() }

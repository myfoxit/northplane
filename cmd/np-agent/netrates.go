package main

import (
	"sort"
	"sync"
	"time"
)

// netCounter is one interface's cumulative byte counters (platform
// collectors return these; rates are derived between samples).
type netCounter struct {
	Name    string
	RxBytes uint64
	TxBytes uint64
}

// netRate is the derived per-interface throughput.
type netRate struct {
	Name  string  `json:"name"`
	RxBps float64 `json:"rxBytesPerSec"`
	TxBps float64 `json:"txBytesPerSec"`
}

// netTracker turns cumulative counters into rates between calls. One
// shared instance serves both the passive loop and the active listener;
// the first sample only primes the baseline.
type netTracker struct {
	mu   sync.Mutex
	last map[string]netCounter
	at   time.Time
}

var netRatesTracker netTracker

// rates samples counters and returns the throughput since the previous
// call. include filters by interface name (empty = all, capped at 8 by
// name to bound the payload).
func (t *netTracker) rates(include []string) ([]netRate, bool) {
	counters, ok := netCounters()
	if !ok {
		return nil, false
	}
	return t.ratesFrom(counters, time.Now(), include)
}

// ratesFrom is the pure core of rates (testable without OS counters).
func (t *netTracker) ratesFrom(counters []netCounter, now time.Time, include []string) ([]netRate, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	prev, prevAt := t.last, t.at
	t.last = map[string]netCounter{}
	for _, c := range counters {
		t.last[c.Name] = c
	}
	t.at = now
	if prev == nil {
		return nil, false // primed; rates from the next sample on
	}
	elapsed := now.Sub(prevAt).Seconds()
	if elapsed <= 0 {
		return nil, false
	}
	wanted := func(name string) bool {
		if len(include) == 0 {
			return true
		}
		for _, w := range include {
			if w == name {
				return true
			}
		}
		return false
	}
	var out []netRate
	for _, c := range counters {
		p, seen := prev[c.Name]
		if !seen || !wanted(c.Name) || c.RxBytes < p.RxBytes || c.TxBytes < p.TxBytes {
			continue // new interface or counter reset — skip this round
		}
		out = append(out, netRate{Name: c.Name,
			RxBps: float64(c.RxBytes-p.RxBytes) / elapsed,
			TxBps: float64(c.TxBytes-p.TxBytes) / elapsed})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(include) == 0 && len(out) > 8 {
		out = out[:8]
	}
	return out, len(out) > 0
}

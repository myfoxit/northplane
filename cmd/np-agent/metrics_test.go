package main

// New metric collectors: rate derivation, process counts, and their
// presence in the passive results + /v1/metrics payload.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNetTrackerRates(t *testing.T) {
	tr := &netTracker{}
	t0 := time.Now()
	// first sample primes
	if _, ok := tr.ratesFrom([]netCounter{{Name: "eth0", RxBytes: 1000, TxBytes: 500}}, t0, nil); ok {
		t.Fatal("first sample must prime, not report")
	}
	rates, ok := tr.ratesFrom([]netCounter{{Name: "eth0", RxBytes: 3000, TxBytes: 1500}}, t0.Add(2*time.Second), nil)
	if !ok || len(rates) != 1 {
		t.Fatalf("rates: ok=%v %+v", ok, rates)
	}
	if rates[0].RxBps != 1000 || rates[0].TxBps != 500 {
		t.Fatalf("rate math: %+v", rates[0])
	}

	// counter reset (reboot) skips the interface instead of underflowing
	if rates, _ := tr.ratesFrom([]netCounter{{Name: "eth0", RxBytes: 10, TxBytes: 10}},
		t0.Add(4*time.Second), nil); len(rates) != 0 {
		t.Fatalf("reset must skip: %+v", rates)
	}

	// include filter
	tr2 := &netTracker{}
	_, _ = tr2.ratesFrom([]netCounter{{Name: "a", RxBytes: 0, TxBytes: 0}, {Name: "b", RxBytes: 0, TxBytes: 0}}, t0, nil)
	rates, ok = tr2.ratesFrom([]netCounter{{Name: "a", RxBytes: 100, TxBytes: 0}, {Name: "b", RxBytes: 100, TxBytes: 0}},
		t0.Add(time.Second), []string{"b"})
	if !ok || len(rates) != 1 || rates[0].Name != "b" {
		t.Fatalf("include filter: %+v", rates)
	}
}

func TestProcCount(t *testing.T) {
	total, _, ok := procCount()
	if !ok || total < 2 {
		t.Fatalf("procCount on a live system: total=%d ok=%v", total, ok)
	}
}

func TestCollectIncludesNewServices(t *testing.T) {
	cfg := agentConfig{Hostname: "test-host", Disk: []string{"/"}}
	first := collect(context.Background(), cfg)
	time.Sleep(20 * time.Millisecond)
	second := collect(context.Background(), cfg)

	services := map[string]string{}
	for _, r := range second {
		services[r.Service] = r.Output
	}
	if _, ok := services["processes"]; !ok {
		t.Fatalf("processes service missing: %v", keys(services))
	}
	if !strings.Contains(services["processes"], "total=") {
		t.Fatalf("processes perfdata missing: %s", services["processes"])
	}
	// network appears from the second tick on (first primes the tracker)
	if _, ok := services["network"]; !ok {
		t.Logf("first collect services: %d, second: %d", len(first), len(second))
		t.Fatalf("network service missing on second tick: %v", keys(services))
	}
}

func TestCollectMetricsPayloadCarriesProcesses(t *testing.T) {
	m := collectMetrics(agentConfig{Hostname: "x", Disk: []string{"/"}})
	if m.Processes == nil || m.Processes.Total < 2 {
		t.Fatalf("payload processes: %+v", m.Processes)
	}
	if m.Load1 == nil {
		t.Fatalf("unix agents must report load1")
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

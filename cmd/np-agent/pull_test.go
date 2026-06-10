package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func pullServer(t *testing.T, checks []remoteCheck, fail *atomic.Bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/checks" {
			t.Errorf("path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("host") != "db-01" {
			t.Errorf("host param: %s", r.URL.Query().Get("host"))
		}
		if r.Header.Get("Authorization") != "Bearer np_test" {
			t.Errorf("auth: %s", r.Header.Get("Authorization"))
		}
		if fail != nil && fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"host": "db-01", "checks": checks})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testPuller(t *testing.T, srv *httptest.Server) *puller {
	t.Helper()
	cfg := agentConfig{Server: srv.URL, Token: "np_test", Hostname: "db-01",
		Interval: time.Minute}
	return newPuller(cfg, srv.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestPullerFetchAndCadence(t *testing.T) {
	checks := []remoteCheck{
		{Service: "disk", Command: "check_disk", Args: []string{"-w", "80%"}, IntervalSeconds: 120},
		{Service: "fast", Command: "check_fast"},
	}
	srv := pullServer(t, checks, nil)
	p := testPuller(t, srv)

	t0 := time.Now()
	p.fetch(context.Background(), t0)
	if len(p.checks) != 2 {
		t.Fatalf("fetched %d checks", len(p.checks))
	}
	if !p.overridden()["disk"] || p.overridden()["other"] {
		t.Fatalf("overridden set: %+v", p.overridden())
	}

	// tick 1: both due (first run)
	if due := p.due(t0, time.Minute); len(due) != 2 {
		t.Fatalf("first tick due: %d", len(due))
	}
	// tick 2 (+60s): only "fast" (agent-tick cadence) is due — "disk"
	// has its own 120s interval
	due := p.due(t0.Add(time.Minute), time.Minute)
	if len(due) != 1 || due[0].Service != "fast" {
		t.Fatalf("second tick due: %+v", due)
	}
	// tick 3 (+120s): both due again
	if due := p.due(t0.Add(2*time.Minute), time.Minute); len(due) != 2 {
		t.Fatalf("third tick due: %d", len(due))
	}
}

func TestPullerKeepsSetOnServerOutage(t *testing.T) {
	var fail atomic.Bool
	srv := pullServer(t, []remoteCheck{{Service: "disk", Command: "check_disk"}}, &fail)
	p := testPuller(t, srv)
	p.interval = 0 // refetch every call

	t0 := time.Now()
	p.fetch(context.Background(), t0)
	if len(p.checks) != 1 {
		t.Fatalf("initial fetch: %d", len(p.checks))
	}
	fail.Store(true)
	p.fetch(context.Background(), t0.Add(time.Hour))
	if len(p.checks) != 1 {
		t.Fatal("server outage must keep the previous check set")
	}

	// recovery with a changed set replaces it and prunes cadence state
	fail.Store(false)
	_ = p.due(t0.Add(time.Hour), time.Minute) // record lastRun for "disk"
	srvChecks := []remoteCheck{{Service: "swap", Command: "check_swap"}}
	srv2 := pullServer(t, srvChecks, nil)
	p.cfg.Server = srv2.URL
	p.client = srv2.Client()
	p.fetch(context.Background(), t0.Add(2*time.Hour))
	if len(p.checks) != 1 || p.checks[0].Service != "swap" {
		t.Fatalf("replacement set: %+v", p.checks)
	}
	_ = p.due(t0.Add(2*time.Hour), time.Minute)
	if _, stale := p.lastRun["disk"]; stale {
		t.Fatal("cadence state of removed checks must be pruned")
	}
}

func TestPullerRunExecutesPlugin(t *testing.T) {
	// /bin/sh is universally present; "exit 1" yields WARNING
	srv := pullServer(t, []remoteCheck{{
		Service: "always-warn", Command: "/bin/sh",
		Args: []string{"-c", "echo WARN - synthetic; exit 1"}, TimeoutSeconds: 5,
	}}, nil)
	p := testPuller(t, srv)
	p.fetch(context.Background(), time.Now())
	results := p.run(context.Background(), time.Now(), time.Minute)
	if len(results) != 1 {
		t.Fatalf("results: %+v", results)
	}
	r := results[0]
	if r.Host != "db-01" || r.Service != "always-warn" || r.State != 1 ||
		r.Output != "WARN - synthetic" {
		t.Fatalf("plugin result: %+v", r)
	}
}

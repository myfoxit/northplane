package main

// Central check config (SPEC §8.4): with pull enabled the agent fetches
// its agent-class checks (services with checkCommand "agent:exec:…")
// from the server and runs them as ordinary Nagios plugins — the daemon
// stays thin and is configured from the instance it reports to, in both
// deployment variants. Local agent.yaml checks keep working; a pulled
// check with the same service name takes precedence (central wins).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// remoteCheck mirrors the server's AgentCheck payload.
type remoteCheck struct {
	Service         string   `json:"service"`
	Command         string   `json:"command"`
	Args            []string `json:"args"`
	IntervalSeconds int      `json:"intervalSeconds"`
	TimeoutSeconds  int      `json:"timeoutSeconds"`
}

// puller owns the fetched check set and its per-check cadence.
type puller struct {
	cfg      agentConfig
	client   *http.Client
	log      *slog.Logger
	interval time.Duration // re-fetch cadence

	checks    []remoteCheck
	lastFetch time.Time
	lastRun   map[string]time.Time
}

func newPuller(cfg agentConfig, client *http.Client, log *slog.Logger) *puller {
	interval := cfg.PullInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &puller{cfg: cfg, client: client, log: log, interval: interval,
		lastRun: map[string]time.Time{}}
}

// fetch refreshes the check list when the re-fetch interval elapsed.
// Errors keep the previous set — a server outage must not stop checks.
func (p *puller) fetch(ctx context.Context, now time.Time) {
	if !p.lastFetch.IsZero() && now.Sub(p.lastFetch) < p.interval {
		return
	}
	checks, err := fetchChecks(ctx, p.client, p.cfg)
	if err != nil {
		p.log.Warn("pull: fetching checks failed, keeping previous set",
			"checks", len(p.checks), "err", err)
		return
	}
	p.lastFetch = now
	p.checks = checks
}

// fetchChecks calls GET /api/v1/agent/checks?host=… on the server.
func fetchChecks(ctx context.Context, client *http.Client, cfg agentConfig) ([]remoteCheck, error) {
	u := strings.TrimSuffix(cfg.Server, "/") + "/api/v1/agent/checks?host=" +
		url.QueryEscape(cfg.Hostname)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out struct {
		Checks []remoteCheck `json:"checks"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return out.Checks, nil
}

// overridden reports which local check services a pulled check replaces.
func (p *puller) overridden() map[string]bool {
	out := map[string]bool{}
	for _, c := range p.checks {
		out[c.Service] = true
	}
	return out
}

// due returns the pulled checks whose interval elapsed and marks them
// run. A check without an interval follows the agent's tick.
func (p *puller) due(now time.Time, tick time.Duration) []remoteCheck {
	var out []remoteCheck
	for _, c := range p.checks {
		interval := tick
		if c.IntervalSeconds > 0 {
			interval = time.Duration(c.IntervalSeconds) * time.Second
		}
		if last, ok := p.lastRun[c.Service]; ok && now.Sub(last) < interval {
			continue
		}
		p.lastRun[c.Service] = now
		out = append(out, c)
	}
	// forget cadence state of checks that no longer exist
	if len(p.lastRun) > len(p.checks) {
		known := p.overridden()
		for svc := range p.lastRun {
			if !known[svc] {
				delete(p.lastRun, svc)
			}
		}
	}
	return out
}

// run executes the due pulled checks as passive results.
func (p *puller) run(ctx context.Context, now time.Time, tick time.Duration) []passiveResult {
	var out []passiveResult
	for _, c := range p.due(now, tick) {
		chk := agentCheck{Service: c.Service, Command: c.Command, Args: c.Args,
			Timeout: time.Duration(c.TimeoutSeconds) * time.Second}
		state, output := runPluginCheck(ctx, chk)
		out = append(out, passiveResult{Host: p.cfg.Hostname, Service: c.Service,
			State: state, Output: output})
	}
	return out
}

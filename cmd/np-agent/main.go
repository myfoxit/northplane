// np-agent is the host agent (SPEC §7.1/§8.4): collects basic system
// metrics, runs local Nagios plugins on schedule and pushes everything
// as passive results over HTTPS — no inbound ports on the target.
//
// v1 scope note: transport is token-authenticated HTTPS against
// /api/v1/results; the mTLS join/CA flow is the satellite roadmap item
// (SPEC §7.7, M4).
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

var version = "1.0.0-dev"

// defaultAgentConfigPath mirrors config.DefaultConfigPath for the
// standalone agent (which avoids importing internal/config to stay a
// minimal binary): /etc/northplane/agent.yaml as root or when present,
// the per-user config dir otherwise.
func defaultAgentConfigPath() string {
	system := "/etc/northplane/agent.yaml"
	if os.Geteuid() == 0 {
		return system
	}
	if _, err := os.Stat(system); err == nil {
		return system
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "northplane", "agent.yaml")
	}
	return system
}

// agentConfig is /etc/northplane/agent.yaml.
type agentConfig struct {
	Server   string        `yaml:"server"`   // https://northplane.example.net
	Token    string        `yaml:"token"`    // np_… (objects:write scope)
	Hostname string        `yaml:"hostname"` // defaults to os.Hostname
	Insecure bool          `yaml:"insecure"`
	Interval time.Duration `yaml:"interval"` // default 60s

	// Checks: local plugin executions submitted as passive services.
	Checks []agentCheck `yaml:"checks"`
	// Pull: additionally fetch agent-class checks (checkCommand
	// "agent:exec:…") from the server, so the daemon is configured
	// centrally (SPEC §8.4). Pulled checks override same-named local
	// ones; the token then needs objects:read besides objects:write.
	Pull         bool          `yaml:"pull"`
	PullInterval time.Duration `yaml:"pullInterval"` // default 5m
	// Builtin metric collection toggles.
	Disk []string `yaml:"disk"` // mount points, default ["/"]
	// Net filters the network interfaces reported (empty = all non-
	// loopback, capped at 8).
	Net []string `yaml:"net"`

	// Active listener mode (NCPA-style, SPEC §8.4): the server queries the
	// agent over HTTPS instead of (or in addition to) passive pushes.
	// Empty = passive-only (default, no inbound ports).
	Listen      string `yaml:"listen"`      // e.g. ":5693"
	ListenToken string `yaml:"listenToken"` // bearer token, required with listen
	TLSCert     string `yaml:"tlsCert"`     // PEM path; empty = self-signed
	TLSKey      string `yaml:"tlsKey"`
}

type agentCheck struct {
	Service string        `yaml:"service"`
	Command string        `yaml:"command"`
	Args    []string      `yaml:"args"`
	Timeout time.Duration `yaml:"timeout"`
}

type passiveResult struct {
	Host    string `json:"host"`
	Service string `json:"service,omitempty"`
	State   int    `json:"state"`
	Output  string `json:"output"`
}

func main() {
	cfgPath := flag.String("config", defaultAgentConfigPath(), "agent config")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println("np-agent", version)
		return
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	cfg := agentConfig{Interval: 60 * time.Second, Disk: []string{"/"}}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		log.Error("config parse", "err", err)
		os.Exit(1)
	}
	if v := os.Getenv("NORTHPLANE_TOKEN"); v != "" {
		cfg.Token = v
	}
	if cfg.Server == "" || cfg.Token == "" {
		log.Error("config: server and token required")
		os.Exit(1)
	}
	if cfg.Hostname == "" {
		cfg.Hostname, _ = os.Hostname()
	}

	client := &http.Client{Timeout: 30 * time.Second}
	if cfg.Insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("np-agent: started", "host", cfg.Hostname, "server", cfg.Server,
		"interval", cfg.Interval, "checks", len(cfg.Checks), "pull", cfg.Pull)

	// active listener (optional): serve metrics/checks to the server
	if cfg.Listen != "" {
		if cfg.ListenToken == "" {
			log.Error("config: listenToken required with listen")
			os.Exit(1)
		}
		go func() {
			if err := serveListener(ctx, cfg, log); err != nil {
				log.Error("listener", "err", err)
				os.Exit(1)
			}
		}()
	}

	// central check config (optional)
	var pull *puller
	if cfg.Pull {
		pull = newPuller(cfg, client, log)
	}

	// store-and-forward buffer: results survive server outages in memory
	// (bounded; oldest dropped beyond 10k)
	var buffer []passiveResult

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	run := func() {
		now := time.Now()
		local := cfg
		var results []passiveResult
		if pull != nil {
			pull.fetch(ctx, now)
			// central definitions win over same-named local checks
			overridden := pull.overridden()
			local.Checks = nil
			for _, c := range cfg.Checks {
				if !overridden[c.Service] {
					local.Checks = append(local.Checks, c)
				}
			}
			results = append(results, pull.run(ctx, now, cfg.Interval)...)
		}
		results = append(results, collect(ctx, local)...)
		buffer = append(buffer, results...)
		if len(buffer) > 10000 {
			buffer = buffer[len(buffer)-10000:]
		}
		if err := submit(ctx, client, cfg, buffer); err != nil {
			log.Warn("submit failed, buffering", "buffered", len(buffer), "err", err)
			return
		}
		buffer = buffer[:0]
	}
	run()
	for {
		select {
		case <-ctx.Done():
			log.Info("np-agent: stopping")
			return
		case <-ticker.C:
			run()
		}
	}
}

func submit(ctx context.Context, client *http.Client, cfg agentConfig, results []passiveResult) error {
	if len(results) == 0 {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{"results": results})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(cfg.Server, "/")+"/api/v1/results", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func collect(ctx context.Context, cfg agentConfig) []passiveResult {
	var out []passiveResult
	host := cfg.Hostname

	// host alive heartbeat
	out = append(out, passiveResult{Host: host, State: 0,
		Output: fmt.Sprintf("np-agent %s alive on %s/%s | uptime=%ds;;;;",
			version, runtime.GOOS, runtime.GOARCH, int(time.Since(startTime).Seconds()))})

	// load average (unix)
	if load, ok := loadAvg(); ok {
		state := 0
		ncpu := float64(runtime.NumCPU())
		if load > ncpu*4 {
			state = 2
		} else if load > ncpu*2 {
			state = 1
		}
		out = append(out, passiveResult{Host: host, Service: "load", State: state,
			Output: fmt.Sprintf("load average %.2f (%d cpus) | load1=%.2f;%.0f;%.0f;0;",
				load, runtime.NumCPU(), load, ncpu*2, ncpu*4)})
	}

	// memory usage
	if usedPct, total, avail, ok := memUsage(); ok {
		state := 0
		if usedPct > 95 {
			state = 2
		} else if usedPct > 90 {
			state = 1
		}
		out = append(out, passiveResult{Host: host, Service: "memory", State: state,
			Output: fmt.Sprintf("RAM %.1f%% used, %.1f GB available of %.1f GB | used=%.1f%%;90;95;0;100 available=%dB;;;0;",
				usedPct, float64(avail)/(1<<30), float64(total)/(1<<30), usedPct, avail)})
	}

	// disk usage
	for _, mount := range cfg.Disk {
		if usedPct, freeBytes, ok := diskUsage(mount); ok {
			state := 0
			if usedPct > 95 {
				state = 2
			} else if usedPct > 85 {
				state = 1
			}
			out = append(out, passiveResult{Host: host, Service: "disk " + mount, State: state,
				Output: fmt.Sprintf("%s %.1f%% used, %.1f GB free | used=%.1f%%;85;95;0;100 free=%dB;;;0;",
					mount, usedPct, float64(freeBytes)/(1<<30), usedPct, freeBytes)})
		}
	}

	// cpu utilisation (platforms without a load average, i.e. Windows)
	if pct, ok := cpuPercent(); ok {
		state := 0
		if pct > 95 {
			state = 2
		} else if pct > 85 {
			state = 1
		}
		out = append(out, passiveResult{Host: host, Service: "cpu", State: state,
			Output: fmt.Sprintf("CPU %.1f%% busy (%d cpus) | cpu=%.1f%%;85;95;0;100",
				pct, runtime.NumCPU(), pct)})
	}

	// processes (informational; thresholds belong to alert rules)
	if total, running, ok := procCount(); ok {
		out = append(out, passiveResult{Host: host, Service: "processes", State: 0,
			Output: fmt.Sprintf("%d processes (%d running) | total=%d;;;0; running=%d;;;0;",
				total, running, total, running)})
	}

	// network throughput since the previous tick (first tick primes)
	if rates, ok := netRatesTracker.rates(cfg.Net); ok {
		var perf, summary strings.Builder
		for i, r := range rates {
			if i > 0 {
				summary.WriteString(", ")
			}
			fmt.Fprintf(&summary, "%s rx %.0f B/s tx %.0f B/s", r.Name, r.RxBps, r.TxBps)
			fmt.Fprintf(&perf, " rx_%s=%.0fB/s;;;0; tx_%s=%.0fB/s;;;0;", r.Name, r.RxBps, r.Name, r.TxBps)
		}
		out = append(out, passiveResult{Host: host, Service: "network", State: 0,
			Output: "throughput " + summary.String() + " |" + perf.String()})
	}

	// configured plugin checks
	for _, chk := range cfg.Checks {
		state, output := runPluginCheck(ctx, chk)
		out = append(out, passiveResult{Host: host, Service: chk.Service,
			State: state, Output: output})
	}
	return out
}

// runPluginCheck executes one configured plugin check — shared by the
// passive collection loop and the active listener's /v1/run endpoint.
func runPluginCheck(ctx context.Context, chk agentCheck) (state int, output string) {
	timeout := chk.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	plugPath := chk.Command
	// A bare command name is resolved against the agent's own PATH
	// (cmd.Env does NOT influence exec.LookPath), so resolve it
	// ourselves against the plugin search path before exec.
	if !strings.ContainsRune(plugPath, os.PathSeparator) {
		if resolved, lerr := lookPath(plugPath); lerr == nil {
			plugPath = resolved
		}
	}
	cmd := exec.CommandContext(cctx, plugPath, chk.Args...)
	cmd.Env = []string{"PATH=" + pluginPATH, "LC_ALL=C"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() >= 0 && ee.ExitCode() <= 3 {
			state = ee.ExitCode()
		} else {
			state = 3
		}
	}
	output = strings.TrimSpace(stdout.String())
	if output == "" {
		// Many plugins (and the not-found case) write the reason to
		// stderr; surface it instead of a useless "no output".
		output = strings.TrimSpace(stderr.String())
	}
	if output == "" {
		output = "UNKNOWN - no output"
	}
	if cctx.Err() == context.DeadlineExceeded {
		state, output = 3, "UNKNOWN - plugin timed out after "+timeout.String()
	}
	return state, output
}

// pluginPATH is the search path for bare plugin command names. It adds
// the common Nagios plugin dirs and Homebrew's bin so check_* binaries
// resolve under both Linux and macOS regardless of the agent's own env.
const pluginPATH = "/usr/local/bin:/usr/bin:/bin:/usr/lib/nagios/plugins:" +
	"/usr/lib64/nagios/plugins:/usr/local/libexec/nagios:/opt/homebrew/bin:/opt/homebrew/sbin"

// lookPath resolves a bare command name against pluginPATH (not the
// process environment, which exec.LookPath would use).
func lookPath(name string) (string, error) {
	for _, dir := range strings.Split(pluginPATH, ":") {
		cand := filepath.Join(dir, name)
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	return exec.LookPath(name) // fall back to the process PATH
}

var startTime = time.Now()

package checks

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/nagios"
)

func init() {
	register("agent", checkAgent)
}

// agentMetrics mirrors cmd/np-agent/listen.go metricsPayload.
type agentMetrics struct {
	Agent     string   `json:"agent"`
	Version   string   `json:"version"`
	Hostname  string   `json:"hostname"`
	UptimeSec int64    `json:"uptimeSeconds"`
	CPUs      int      `json:"cpus"`
	Load1     *float64 `json:"load1"`
	CPUPct    *float64 `json:"cpuPct"` // platforms without loadavg (Windows)
	Memory    *struct {
		UsedPct        float64 `json:"usedPct"`
		TotalBytes     uint64  `json:"totalBytes"`
		AvailableBytes uint64  `json:"availableBytes"`
	} `json:"memory"`
	Disks []struct {
		Mount     string  `json:"mount"`
		UsedPct   float64 `json:"usedPct"`
		FreeBytes uint64  `json:"freeBytes"`
	} `json:"disks"`
	Processes *struct {
		Total   int `json:"total"`
		Running int `json:"running"`
	} `json:"processes"`
	Network []struct {
		Name  string  `json:"name"`
		RxBps float64 `json:"rxBytesPerSec"`
		TxBps float64 `json:"txBytesPerSec"`
	} `json:"network"`
}

// checkAgent queries an np-agent active listener (SPEC §8.4, NCPA-style):
//
//	-H host, -p port (5693), --token <bearer> (use $SECRET:…$ macros),
//	--insecure (self-signed agent cert),
//	--metric load1|memory|disk:<mount>  graded via -w/-c, or
//	--check <service>                   run a remote agent.yaml check,
//	default: summary of all metrics with builtin thresholds.
func checkAgent(ctx context.Context, t Target, a Args) (model.State, nagios.Output) {
	host := a.Host(t)
	if host == "" {
		return unknownf("agent: no host (-H or object address)")
	}
	token := a.Get("token")
	if token == "" {
		return unknownf("agent: --token required (the agent's listenToken)")
	}
	port := a.Int(5693, "p", "port")
	timeout := a.Duration(10*time.Second, "t", "timeout")
	base := "https://" + net.JoinHostPort(host, fmt.Sprint(port))

	client := &http.Client{Timeout: timeout}
	if a.Bool("insecure") {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	get := func(path string, v any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, firstLineOf(string(body)))
		}
		return json.Unmarshal(body, v)
	}

	// remote check passthrough
	if name := a.Get("check"); name != "" {
		var res struct {
			State  int    `json:"state"`
			Output string `json:"output"`
		}
		if err := get("/v1/run/"+url.PathEscape(name), &res); err != nil {
			return criticalf("agent %s: %v", host, err)
		}
		return nagios.ExitState(res.State), nagios.ParseOutput(res.Output)
	}

	var m agentMetrics
	if err := get("/v1/metrics", &m); err != nil {
		return criticalf("agent %s: %v", host, err)
	}
	warn, crit := a.Get("w", "warning"), a.Get("c", "critical")

	// single-metric mode: grade one value via -w/-c
	if metric := a.Get("metric"); metric != "" {
		switch {
		case metric == "load1":
			if m.Load1 == nil {
				return unknownf("agent %s reports no load metric", host)
			}
			return evalPerf("load1", *m.Load1, "", warn, crit,
				fmt.Sprintf("%s load1 %.2f (%d cpus)", m.Hostname, *m.Load1, m.CPUs))
		case metric == "memory":
			if m.Memory == nil {
				return unknownf("agent %s reports no memory metric", host)
			}
			return evalPerf("memory", m.Memory.UsedPct, "%", warn, crit,
				fmt.Sprintf("%s RAM %.1f%% used, %.1f GB available", m.Hostname,
					m.Memory.UsedPct, float64(m.Memory.AvailableBytes)/(1<<30)))
		case strings.HasPrefix(metric, "disk:"):
			mount := strings.TrimPrefix(metric, "disk:")
			for _, d := range m.Disks {
				if d.Mount == mount {
					return evalPerf("disk "+mount, d.UsedPct, "%", warn, crit,
						fmt.Sprintf("%s %s %.1f%% used, %.1f GB free", m.Hostname,
							mount, d.UsedPct, float64(d.FreeBytes)/(1<<30)))
				}
			}
			return unknownf("agent %s monitors no mount %q", host, mount)
		case metric == "cpu":
			if m.CPUPct == nil {
				return unknownf("agent %s reports no cpu metric (unix agents expose load1)", host)
			}
			return evalPerf("cpu", *m.CPUPct, "%", warn, crit,
				fmt.Sprintf("%s CPU %.1f%% busy (%d cpus)", m.Hostname, *m.CPUPct, m.CPUs))
		case metric == "processes":
			if m.Processes == nil {
				return unknownf("agent %s reports no process metric", host)
			}
			return evalPerf("processes", float64(m.Processes.Total), "", warn, crit,
				fmt.Sprintf("%s %d processes (%d running)", m.Hostname,
					m.Processes.Total, m.Processes.Running))
		case strings.HasPrefix(metric, "net:"):
			ifname := strings.TrimPrefix(metric, "net:")
			for _, n := range m.Network {
				if n.Name != ifname {
					continue
				}
				// graded on rx (the usual saturation direction); tx rides
				// along as perfdata
				st := model.StateOK
				if warn != "" || crit != "" {
					code, err := nagios.Evaluate(n.RxBps, warn, crit)
					if err != nil {
						return unknownf("agent: bad threshold: %v", err)
					}
					st = model.State(code)
				}
				label := map[model.State]string{0: "OK", 1: "WARNING", 2: "CRITICAL", 3: "UNKNOWN"}[st]
				text := fmt.Sprintf("NET %s - %s %s rx %.0f B/s tx %.0f B/s | rx_%s=%.0fB/s;%s;%s;0; tx_%s=%.0fB/s;;;0;",
					label, m.Hostname, ifname, n.RxBps, n.TxBps, ifname, n.RxBps, warn, crit, ifname, n.TxBps)
				return st, nagios.ParseOutput(text)
			}
			return unknownf("agent %s reports no interface %q", host, ifname)
		default:
			return unknownf("agent: unknown --metric %q (load1 | cpu | memory | disk:<mount> | processes | net:<iface>)", metric)
		}
	}

	// summary mode: worst-of over builtin thresholds, full perfdata
	st := model.StateOK
	var parts, perf []string
	bump := func(s model.State) {
		if s > st {
			st = s
		}
	}
	if m.Load1 != nil {
		ncpu := float64(m.CPUs)
		switch {
		case *m.Load1 > ncpu*4:
			bump(model.StateCritical)
		case *m.Load1 > ncpu*2:
			bump(model.StateWarning)
		}
		parts = append(parts, fmt.Sprintf("load %.2f", *m.Load1))
		perf = append(perf, fmt.Sprintf("load1=%.2f;%.0f;%.0f;0;", *m.Load1, ncpu*2, ncpu*4))
	}
	if m.CPUPct != nil {
		switch {
		case *m.CPUPct > 95:
			bump(model.StateCritical)
		case *m.CPUPct > 85:
			bump(model.StateWarning)
		}
		parts = append(parts, fmt.Sprintf("cpu %.1f%%", *m.CPUPct))
		perf = append(perf, fmt.Sprintf("cpu=%.1f%%;85;95;0;100", *m.CPUPct))
	}
	if m.Memory != nil {
		switch {
		case m.Memory.UsedPct > 95:
			bump(model.StateCritical)
		case m.Memory.UsedPct > 90:
			bump(model.StateWarning)
		}
		parts = append(parts, fmt.Sprintf("mem %.1f%%", m.Memory.UsedPct))
		perf = append(perf, fmt.Sprintf("memory=%.1f%%;90;95;0;100", m.Memory.UsedPct))
	}
	for _, d := range m.Disks {
		switch {
		case d.UsedPct > 95:
			bump(model.StateCritical)
		case d.UsedPct > 85:
			bump(model.StateWarning)
		}
		parts = append(parts, fmt.Sprintf("disk %s %.1f%%", d.Mount, d.UsedPct))
		perf = append(perf, fmt.Sprintf("%s=%.1f%%;85;95;0;100", perfLabel("disk "+d.Mount), d.UsedPct))
	}
	if m.Processes != nil {
		parts = append(parts, fmt.Sprintf("%d procs", m.Processes.Total))
		perf = append(perf, fmt.Sprintf("processes=%d;;;0;", m.Processes.Total))
	}
	label := map[model.State]string{0: "OK", 1: "WARNING", 2: "CRITICAL", 3: "UNKNOWN"}[st]
	text := fmt.Sprintf("AGENT %s - %s v%s up %s: %s | %s", label, m.Hostname, m.Version,
		(time.Duration(m.UptimeSec) * time.Second).String(), strings.Join(parts, ", "),
		strings.Join(perf, " "))
	return st, nagios.ParseOutput(text)
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.TrimSpace(s)
}

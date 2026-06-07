package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/northplane/northplane/internal/model"
)

// fakeAgent mimics np-agent's active listener.
func fakeAgent(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer agent-tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/metrics":
			_, _ = w.Write([]byte(`{"agent":"np-agent","version":"1.0.0","hostname":"web-01",
				"uptimeSeconds":120,"cpus":4,"load1":1.5,
				"memory":{"usedPct":97.5,"totalBytes":17179869184,"availableBytes":429496729},
				"disks":[{"mount":"/","usedPct":42.0,"freeBytes":107374182400}]}`))
		case "/v1/run/check_users":
			_, _ = w.Write([]byte(`{"service":"check_users","state":1,"output":"USERS WARNING - 9 users | users=9;5;10;0"}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	u, _ := url.Parse(ts.URL)
	return ts, u.Hostname(), u.Port()
}

func TestAgentCheckSummary(t *testing.T) {
	_, host, port := fakeAgent(t)
	state, out := Run(context.Background(), "agent", Target{},
		[]string{"-H", host, "-p", port, "--token", "agent-tok", "--insecure", "true"})
	// memory 97.5% > 95 ⇒ CRITICAL via builtin thresholds
	if state != model.StateCritical {
		t.Fatalf("state=%v out=%s", state, out.Text)
	}
	if !strings.Contains(out.Text, "web-01") || !strings.Contains(out.Text, "mem 97.5%") {
		t.Fatalf("text: %s", out.Text)
	}
	if len(out.Metrics) < 3 {
		t.Fatalf("perfdata: %+v", out.Metrics)
	}
}

func TestAgentCheckSingleMetric(t *testing.T) {
	_, host, port := fakeAgent(t)
	state, out := Run(context.Background(), "agent", Target{},
		[]string{"-H", host, "-p", port, "--token", "agent-tok", "--insecure", "true",
			"--metric", "disk:/", "-w", "80", "-c", "90"})
	if state != model.StateOK {
		t.Fatalf("disk 42%% must be OK: %v %s", state, out.Text)
	}
	state, _ = Run(context.Background(), "agent", Target{},
		[]string{"-H", host, "-p", port, "--token", "agent-tok", "--insecure", "true",
			"--metric", "disk:/", "-w", "30", "-c", "90"})
	if state != model.StateWarning {
		t.Fatalf("disk 42%% with w=30 must WARN: %v", state)
	}
}

func TestAgentCheckRemoteRun(t *testing.T) {
	_, host, port := fakeAgent(t)
	state, out := Run(context.Background(), "agent", Target{},
		[]string{"-H", host, "-p", port, "--token", "agent-tok", "--insecure", "true",
			"--check", "check_users"})
	if state != model.StateWarning || !strings.Contains(out.Text, "USERS WARNING") {
		t.Fatalf("passthrough: %v %s", state, out.Text)
	}
}

func TestAgentCheckBadToken(t *testing.T) {
	_, host, port := fakeAgent(t)
	state, out := Run(context.Background(), "agent", Target{},
		[]string{"-H", host, "-p", port, "--token", "nope", "--insecure", "true"})
	if state != model.StateCritical {
		t.Fatalf("bad token: %v %s", state, out.Text)
	}
}

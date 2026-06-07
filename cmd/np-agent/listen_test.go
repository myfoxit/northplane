package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListenerAuthAndEndpoints(t *testing.T) {
	cfg := agentConfig{Hostname: "test-host", ListenToken: "agent-tok",
		Disk: []string{"/"},
		Checks: []agentCheck{{Service: "echo-ok", Command: "/bin/sh",
			Args: []string{"-c", "echo 'OK - fine | x=1;;;;'"}}}}
	ts := httptest.NewServer(listenerHandler(cfg))
	t.Cleanup(ts.Close)

	get := func(path, token string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var buf [4096]byte
		n, _ := resp.Body.Read(buf[:])
		return resp, buf[:n]
	}

	// no/wrong token → 401
	if resp, _ := get("/v1/metrics", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	if resp, _ := get("/v1/metrics", "wrong"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", resp.StatusCode)
	}

	// metrics shape
	resp, body := get("/v1/metrics", "agent-tok")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics: %d %s", resp.StatusCode, body)
	}
	var m metricsPayload
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("metrics json: %v %s", err, body)
	}
	if m.Agent != "np-agent" || m.Hostname != "test-host" || m.CPUs < 1 {
		t.Fatalf("metrics: %+v", m)
	}

	// allowlisted check runs; unknown 404s
	resp, body = get("/v1/run/echo-ok", "agent-tok")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run: %d %s", resp.StatusCode, body)
	}
	var r runPayload
	if err := json.Unmarshal(body, &r); err != nil || r.State != 0 {
		t.Fatalf("run result: %v %+v", err, r)
	}
	if resp, _ = get("/v1/run/not-configured", "agent-tok"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown check must 404: %d", resp.StatusCode)
	}
}

func TestListenerSelfSignedTLS(t *testing.T) {
	tlsCfg, err := listenerTLS(agentConfig{Hostname: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tlsCfg.Certificates) != 1 {
		t.Fatalf("certs: %d", len(tlsCfg.Certificates))
	}
}

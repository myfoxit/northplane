package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
	"github.com/northplane/northplane/internal/tsdb"
)

// Full-stack smoke test: real server process on a loopback port, real
// SQLite + TSDB, exercising the API surface end to end.

type testServer struct {
	base  string
	token string
	t     *testing.T
}

func bootServer(t *testing.T) *testServer {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())

	cfg := config.Defaults()
	cfg.Listen = "127.0.0.1:0"
	cfg.DataDir = dir
	cfg.TLS.Insecure = true
	cfg.LogFormat = "text"
	cfg.LogLevel = "warn"

	store, err := storage.Open(ctx, storage.Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ts, err := tsdb.Open(filepath.Join(dir, "tsdb"), nil, tsdb.Retention{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()                           // stop all subsystem goroutines first
		time.Sleep(400 * time.Millisecond) // let pipeline/janitor flushes settle
		ts.Close()
		store.Close()
	})

	clear, tok := auth.MintToken(model.DefaultTenant, "test-admin",
		[]model.Permission{"*:*"}, nil)
	if err := store.CreateAPIToken(ctx, tok); err != nil {
		t.Fatal(err)
	}

	srv, err := New(ctx, cfg, store, ts, testLogger(), "test")
	if err != nil {
		t.Fatal(err)
	}
	// run on a kernel-assigned port: grab it from the listener by
	// starting Run in a goroutine and polling /healthz via the bound addr
	addrCh := make(chan string, 1)
	srv.httpSrv.Addr = "127.0.0.1:0"
	go func() {
		// mirror Run() minimally to learn the port
		for _, e := range srv.cat.All() {
			srv.sched.Upsert(e)
		}
		go srv.sched.Run(ctx)
		go srv.exec.Run(ctx, srv.sched)
		go srv.pipe.Run(ctx)
		go srv.alert.Run(ctx)
		go srv.correl.Run(ctx)
		go srv.escal.Run(ctx)
		go srv.notify.Run(ctx)
		go srv.api.Janitor(ctx)
		go srv.api.WebhookDispatcher(ctx)
		ln, err := netListen("127.0.0.1:0")
		if err != nil {
			panic(err)
		}
		addrCh <- ln.Addr().String()
		_ = srv.httpSrv.Serve(ln)
	}()
	addr := <-addrCh
	tsrv := &testServer{base: "http://" + addr, token: "np_" + clearBody(clear), t: t}
	// wait ready
	for i := 0; i < 50; i++ {
		resp, err := http.Get(tsrv.base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return tsrv
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("server never became healthy")
	return nil
}

func clearBody(clear string) string { return strings.TrimPrefix(clear, "np_") }

func (ts *testServer) req(method, path string, body any) (int, []byte) {
	ts.t.Helper()
	var rd io.Reader
	contentType := "application/json"
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
		if strings.HasPrefix(strings.TrimSpace(b), "kind:") ||
			strings.Contains(b, "\nkind:") {
			contentType = "application/yaml"
		}
	default:
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, ts.base+path, rd)
	if err != nil {
		ts.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+ts.token)
	if rd != nil {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ts.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func TestAPIEndToEnd(t *testing.T) {
	ts := bootServer(t)

	// 1) whoami
	code, body := ts.req("GET", "/api/v1/whoami", nil)
	if code != 200 || !bytes.Contains(body, []byte("test-admin")) {
		t.Fatalf("whoami: %d %s", code, body)
	}

	// 2) create host (passive — no real checks in this test)
	code, body = ts.req("POST", "/api/v1/hosts", map[string]any{
		"name": "web01", "folder": "/prod",
		"labels": map[string]string{"env": "prod", "role": "web"},
		"spec":   map[string]any{"address": "127.0.0.1", "checkCommand": "passive"},
	})
	if code != 201 {
		t.Fatalf("create host: %d %s", code, body)
	}
	var host struct{ ID string }
	_ = json.Unmarshal(body, &host)

	// duplicate → 409
	code, _ = ts.req("POST", "/api/v1/hosts", map[string]any{
		"name": "web01", "spec": map[string]any{"checkCommand": "passive"}})
	if code != 409 {
		t.Fatalf("duplicate host: %d", code)
	}

	// 3) service via batch
	code, body = ts.req("POST", "/api/v1/objects:batch", map[string]any{
		"services": []map[string]any{{
			"name": "http", "host": "web01",
			"labels": map[string]string{"env": "prod"},
			"spec":   map[string]any{"checkCommand": "passive", "stalenessAfter": "1h"},
		}},
	})
	if code != 200 || !bytes.Contains(body, []byte(`"created":1`)) {
		t.Fatalf("batch: %d %s", code, body)
	}

	// 4) passive result with perfdata → state + problems + tsdb
	code, body = ts.req("POST", "/api/v1/results", map[string]any{
		"results": []map[string]any{{
			"host": "web01", "service": "http", "state": 2,
			"output": "HTTP CRITICAL - connect refused | time=5.0s;1;3;0;",
		}},
	})
	if code != 202 || !bytes.Contains(body, []byte(`"accepted":1`)) {
		t.Fatalf("passive: %d %s", code, body)
	}
	waitFor(t, "problem appears", func() bool {
		_, body := ts.req("GET", "/api/v1/problems", nil)
		return bytes.Contains(body, []byte("http"))
	})

	// 5) selector list
	code, body = ts.req("GET", "/api/v1/objects?selector=env%3Dprod", nil)
	if code != 200 || !bytes.Contains(body, []byte("web01")) {
		t.Fatalf("selector list: %d %s", code, body)
	}

	// 6) downtime with idempotency replay
	req1 := map[string]any{
		"objectId": host.ID, "type": "fixed",
		"start": time.Now().UTC(), "end": time.Now().UTC().Add(time.Hour),
		"comment": "maintenance",
	}
	r1, _ := http.NewRequest("POST", ts.base+"/api/v1/downtimes", jsonBody(req1))
	r1.Header.Set("Authorization", "Bearer "+ts.token)
	r1.Header.Set("Content-Type", "application/json")
	r1.Header.Set("Idempotency-Key", "maint-1")
	resp1, err := http.DefaultClient.Do(r1)
	if err != nil {
		t.Fatal(err)
	}
	d1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != 201 {
		t.Fatalf("downtime: %d %s", resp1.StatusCode, d1)
	}
	r2, _ := http.NewRequest("POST", ts.base+"/api/v1/downtimes", jsonBody(req1))
	r2.Header.Set("Authorization", "Bearer "+ts.token)
	r2.Header.Set("Content-Type", "application/json")
	r2.Header.Set("Idempotency-Key", "maint-1")
	resp2, err := http.DefaultClient.Do(r2)
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.Header.Get("Idempotency-Replayed") != "true" || !bytes.Equal(d1, d2) {
		t.Fatalf("idempotency replay failed: %s vs %s", d1, d2)
	}

	// 7) bundle plan + apply
	bundleYAML := `kind: Host
metadata:
  name: db01
  folder: /prod
  labels: {env: prod, role: db}
spec:
  checkCommand: passive
---
kind: AlertRule
metadata:
  name: prod-critical
spec:
  match: event.type == "state_change" && event.labels.env == "prod" && (event.state == "CRITICAL" || event.state == "DOWN")
  severity: critical
`
	code, body = ts.req("POST", "/api/v1/config/bundles:plan", bundleYAML)
	if code != 200 || !bytes.Contains(body, []byte(`"action":"create"`)) {
		t.Fatalf("plan: %d %s", code, body)
	}
	code, body = ts.req("POST", "/api/v1/config/bundles:apply", bundleYAML)
	if code != 200 {
		t.Fatalf("apply: %d %s", code, body)
	}
	// re-apply = no changes (idempotent round-trip)
	code, body = ts.req("POST", "/api/v1/config/bundles:apply", bundleYAML)
	var applyRes struct{ Plan []any }
	_ = json.Unmarshal(body, &applyRes)
	if code != 200 || len(applyRes.Plan) != 0 {
		t.Fatalf("re-apply should be empty: %d %s", code, body)
	}

	// 8) export round-trip contains both hosts
	code, body = ts.req("GET", "/api/v1/config/bundles:export", nil)
	if code != 200 || !bytes.Contains(body, []byte("web01")) || !bytes.Contains(body, []byte("db01")) {
		t.Fatalf("export: %d", code)
	}

	// 9) alert rule fires on the next CRITICAL passive result for db01
	ts.req("POST", "/api/v1/results", map[string]any{
		"results": []map[string]any{{
			"host": "db01", "state": 2, "output": "DOWN hard"}}})
	waitFor(t, "alert opens", func() bool {
		_, body := ts.req("GET", "/api/v1/alerts?status=open", nil)
		return bytes.Contains(body, []byte("db01"))
	})
	var alerts struct {
		Items []struct{ ID string }
	}
	_, body = ts.req("GET", "/api/v1/alerts?status=open", nil)
	_ = json.Unmarshal(body, &alerts)
	if len(alerts.Items) == 0 {
		t.Fatal("no alert")
	}
	code, body = ts.req("POST", "/api/v1/alerts/"+alerts.Items[0].ID+":ack",
		map[string]string{"comment": "on it"})
	if code != 200 {
		t.Fatalf("ack: %d %s", code, body)
	}

	// 10) audit chain intact and contains our mutations
	code, body = ts.req("POST", "/api/v1/audit:verify", nil)
	if code != 200 || !bytes.Contains(body, []byte(`"intact":true`)) {
		t.Fatalf("audit verify: %d %s", code, body)
	}
	_, body = ts.req("GET", "/api/v1/audit?action=host.create", nil)
	if !bytes.Contains(body, []byte("host.create")) {
		t.Fatal("audit trail missing host.create")
	}

	// 11) openapi + docs + metrics + status page
	code, body = ts.req("GET", "/api/openapi.json", nil)
	if code != 200 || !bytes.Contains(body, []byte(`"openapi": "3.1.0"`)) {
		t.Fatalf("openapi: %d", code)
	}
	if !bytes.Contains(body, []byte("/api/v1/alerts/{id}:ack")) {
		t.Fatal("openapi missing ack route")
	}
	resp, err := http.Get(ts.base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	mbody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(mbody, []byte("np_catalog_objects")) {
		t.Fatalf("metrics: %s", truncateB(mbody))
	}
	resp, err = http.Get(ts.base + "/status/default")
	if err != nil {
		t.Fatal(err)
	}
	sbody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(sbody, []byte("Service Status")) {
		t.Fatalf("status page: %d", resp.StatusCode)
	}

	// 12) unauthenticated requests are rejected
	resp, err = http.Get(ts.base + "/api/v1/hosts")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("unauthenticated: %d", resp.StatusCode)
	}

	// 13) events were recorded for the lifecycle
	_, body = ts.req("GET", "/api/v1/events?types=state_change", nil)
	if !bytes.Contains(body, []byte("state_change")) {
		t.Fatal("no state_change events")
	}
}

func TestSSEStream(t *testing.T) {
	ts := bootServer(t)
	// open the stream, then trigger an event, expect it on the wire
	req, _ := http.NewRequest("GET", ts.base+"/api/v1/stream?types=config", nil)
	req.Header.Set("Authorization", "Bearer "+ts.token)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream: %d", resp.StatusCode)
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		ts.req("POST", "/api/v1/hosts", map[string]any{
			"name": "sse-host", "spec": map[string]any{"checkCommand": "passive"}})
	}()
	buf := make([]byte, 4096)
	deadline := time.Now().Add(10 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
			if bytes.Contains(got, []byte("event: config")) {
				return // success
			}
		}
		if err != nil {
			break
		}
	}
	t.Fatalf("config event never arrived on SSE: %s", truncateB(got))
}

func jsonBody(v any) io.Reader {
	raw, _ := json.Marshal(v)
	return bytes.NewReader(raw)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func truncateB(b []byte) string {
	if len(b) > 300 {
		b = b[:300]
	}
	return string(b)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func netListen(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }

// TestTLSPolicy locks in the plaintext policy (A-15.10): loopback, explicit
// insecure, and trustProxy (TLS terminated upstream) serve plaintext; a
// non-loopback listener without any opt-in must refuse to start.
func TestTLSPolicy(t *testing.T) {
	cases := []struct {
		name       string
		listen     string
		insecure   bool
		trustProxy bool
		wantErr    bool
	}{
		{"loopback plaintext ok", "127.0.0.1:0", false, false, false},
		{"non-loopback refused", "0.0.0.0:0", false, false, true},
		{"non-loopback insecure ok", "0.0.0.0:0", true, false, false},
		{"non-loopback trustProxy ok", "0.0.0.0:0", false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", tc.listen)
			if err != nil {
				t.Skipf("bind %s: %v", tc.listen, err)
			}
			defer ln.Close()
			cfg := config.Defaults()
			cfg.TLS.Insecure = tc.insecure
			cfg.TrustProxy = tc.trustProxy
			s := &Server{Cfg: cfg, Log: testLogger()}
			tlsCfg, useTLS, err := s.tlsConfig(ln)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected refusal error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if useTLS || tlsCfg != nil {
				t.Fatal("plaintext case must not enable TLS")
			}
		})
	}
	// cert configured but unloadable → hard error, never plaintext fallback
	t.Run("bad cert refuses", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		cfg := config.Defaults()
		cfg.TLS.CertFile = "/nonexistent/cert.pem"
		cfg.TLS.KeyFile = "/nonexistent/key.pem"
		s := &Server{Cfg: cfg, Log: testLogger()}
		if _, _, err := s.tlsConfig(ln); err == nil {
			t.Fatal("expected cert load error, got nil")
		}
	})
}

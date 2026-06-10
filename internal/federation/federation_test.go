package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// fakeMain plays the main instance: a bundle behind :pull (with ETag /
// If-None-Match semantics) and a heartbeat sink.
type fakeMain struct {
	mu         sync.Mutex
	bundle     string
	etag       string
	pulls      int
	notModded  int
	heartbeats []model.SiteHeartbeat
	authSeen   string
}

func (f *fakeMain) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sites/customer-a:pull", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.authSeen = r.Header.Get("Authorization")
		f.pulls++
		if r.Header.Get("If-None-Match") == f.etag {
			f.notModded++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", f.etag)
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = io.WriteString(w, f.bundle)
	})
	mux.HandleFunc("/api/v1/sites/customer-a:heartbeat", func(w http.ResponseWriter, r *http.Request) {
		var hb model.SiteHeartbeat
		if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
			t.Errorf("heartbeat body: %v", err)
		}
		f.mu.Lock()
		f.heartbeats = append(f.heartbeats, hb)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func (f *fakeMain) lastHeartbeat(t *testing.T) model.SiteHeartbeat {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.heartbeats) == 0 {
		t.Fatal("no heartbeat received")
	}
	return f.heartbeats[len(f.heartbeats)-1]
}

// fakeApplier records applies and can be told to fail.
type fakeApplier struct {
	mu      sync.Mutex
	applied []string
	fail    bool
}

func (f *fakeApplier) ApplyBundleYAML(_ context.Context, tenantID, yamlText string) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, fmt.Errorf("unknown host \"edge-db-01\"")
	}
	if tenantID != model.DefaultTenant {
		return nil, fmt.Errorf("unexpected tenant %s", tenantID)
	}
	f.applied = append(f.applied, yamlText)
	return nil, nil
}

func (f *fakeApplier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.applied)
}

func testEdge(t *testing.T, mainURL string, applier BundleApplier) *Edge {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); store.Close() })
	cfg := config.FederationConfig{
		Mode: "edge", MainURL: mainURL, Token: "np_edgetoken", Site: "customer-a",
	}
	return NewEdge(cfg, store, applier, "9.9.9-test", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestEdgePullApplyHeartbeat(t *testing.T) {
	main := &fakeMain{bundle: "kind: Host\nmetadata:\n  name: edge-db-01\nspec:\n  checkCommand: passive\n", etag: `"rev1"`}
	srv := httptest.NewServer(main.handler(t))
	t.Cleanup(srv.Close)
	applier := &fakeApplier{}
	edge := testEdge(t, srv.URL, applier)
	ctx := context.Background()

	// first tick: pull + apply + heartbeat carrying the applied etag
	edge.tick(ctx)
	if applier.count() != 1 {
		t.Fatalf("bundle not applied: %d", applier.count())
	}
	if main.authSeen != "Bearer np_edgetoken" {
		t.Fatalf("auth header: %q", main.authSeen)
	}
	hb := main.lastHeartbeat(t)
	if hb.Version != "9.9.9-test" || hb.BundleETag != `"rev1"` || hb.ApplyError != "" {
		t.Fatalf("heartbeat: %+v", hb)
	}
	if hb.Stats["hosts"] != 0 || hb.Stats["services"] != 0 {
		t.Fatalf("stats: %+v", hb.Stats)
	}

	// second tick: conditional pull → 304, no re-apply
	edge.tick(ctx)
	if applier.count() != 1 {
		t.Fatalf("unchanged bundle re-applied: %d", applier.count())
	}
	if main.notModded != 1 {
		t.Fatalf("304 path not taken: pulls=%d notModified=%d", main.pulls, main.notModded)
	}

	// bundle changes but apply fails: etag must NOT advance, heartbeat
	// carries the error, and the next good tick re-applies
	main.mu.Lock()
	main.bundle, main.etag = main.bundle+"---\nkind: Host\nmetadata:\n  name: edge-web-01\nspec: {}\n", `"rev2"`
	main.mu.Unlock()
	applier.fail = true
	edge.tick(ctx)
	if hb = main.lastHeartbeat(t); hb.ApplyError == "" || hb.BundleETag != `"rev1"` {
		t.Fatalf("failed apply heartbeat: %+v", hb)
	}
	applier.fail = false
	edge.tick(ctx)
	if applier.count() != 2 {
		t.Fatalf("recovery apply missing: %d", applier.count())
	}
	if hb = main.lastHeartbeat(t); hb.BundleETag != `"rev2"` || hb.ApplyError != "" {
		t.Fatalf("recovered heartbeat: %+v", hb)
	}

	// the audit trail records the applies as system actor
	entries, err := edge.Store.QueryAudit(ctx, storage.AuditFilter{Action: "federation.apply"})
	if err != nil || len(entries) != 2 {
		t.Fatalf("federation.apply audit entries: %d (err %v)", len(entries), err)
	}
}

func TestEdgeEmptyBundleSkipsApply(t *testing.T) {
	main := &fakeMain{bundle: "", etag: `"empty"`}
	srv := httptest.NewServer(main.handler(t))
	t.Cleanup(srv.Close)
	applier := &fakeApplier{}
	edge := testEdge(t, srv.URL, applier)

	edge.tick(context.Background())
	if applier.count() != 0 {
		t.Fatal("empty bundle must not be applied")
	}
	if hb := main.lastHeartbeat(t); hb.BundleETag != `"empty"` {
		t.Fatalf("empty bundle etag not remembered: %+v", hb)
	}
}

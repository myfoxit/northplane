package api

// Shared httptest harness for the REST handler tests (objects, results,
// alerts, maintenance). It mirrors the bootUserAPI pattern in
// admin_users_test.go but wires the full dependency graph the object /
// alert / result / downtime handlers reach into (Catalog, Scheduler,
// Pipeline, Alerting, Escalation, eventbus, Notify) so the routes run
// without nil-panics. Everything is in-process over httptest; a real
// SQLite store on t.TempDir() backs it. No event loop is started unless
// a test explicitly opts in (runPipeline) — most handler assertions are
// synchronous.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/alerting"
	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/escalation"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/metrics"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/notify"
	"github.com/northplane/northplane/internal/pipeline"
	"github.com/northplane/northplane/internal/scheduler"
	"github.com/northplane/northplane/internal/storage"
	"github.com/northplane/northplane/internal/tts"
)

// testAPI is the wired-up system under test plus a couple of pre-minted
// tokens covering the common RBAC scenarios.
type testAPI struct {
	a     *API
	h     http.Handler
	store *storage.Store
	bus   *eventbus.Bus
	ctx   context.Context

	adminToken string // *:*  — full access
	readToken  string // objects:read, alerts:read, incidents:read — read only
	noneToken  string // a single unrelated scope (events:read) — denied elsewhere
	t          *testing.T
}

// bootAPI wires the full API with real (in-memory-backed) subsystems.
func bootAPI(t *testing.T) *testAPI {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { cancel(); store.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := eventbus.New()
	cat := catalog.New(store)
	sched := scheduler.New(cat, log)
	// pipeline tolerates a nil *tsdb.DB (no perfdata sink in tests).
	pipe := pipeline.New(store, cat, bus, nil, sched, log)
	escal := escalation.New(store, bus, log)
	alert := alerting.NewEngine(store, cat, bus, escal, log)
	mgr := notify.New(store, bus, log)
	authn := &auth.Authenticator{Store: store}
	ttsCache, err := tts.NewCache(t.TempDir(), 8, time.Hour)
	if err != nil {
		t.Fatalf("tts cache: %v", err)
	}
	speech := tts.New(store, ttsCache, nil, log)
	speech.BaseURL = "https://np.test"
	speech.SignKey = []byte("test-sign-key")
	mgr.TTS = speech

	a := &API{
		Store:   store,
		Catalog: cat,
		Bus:     bus,
		Sched:   sched,
		Pipe:    pipe,
		Alert:   alert,
		Escal:   escal,
		Notify:  mgr,
		TTS:     speech,
		Auth:    authn,
		Metrics: metrics.NewRegistry(),
		Log:     log,
	}
	a.mux = http.NewServeMux()
	a.registerObjects()
	a.registerAlerts()
	a.registerRules()
	a.registerMaintenance()
	a.registerContacts()
	a.registerEvents()
	a.registerIngress()
	a.registerTelephony()
	a.registerTTS()
	a.registerBundles()
	a.registerSystem()
	a.registerSites()
	a.registerAgentConfig()
	a.registerDirectory()
	a.registerOpenAPI()
	h := a.withMiddleware(a.mux)

	mk := func(name string, perms ...model.Permission) string {
		clear, tok := auth.MintToken(model.DefaultTenant, name, perms, nil)
		if err := store.CreateAPIToken(ctx, tok); err != nil {
			t.Fatalf("create token %s: %v", name, err)
		}
		return clear
	}

	return &testAPI{
		a:          a,
		h:          h,
		store:      store,
		bus:        bus,
		ctx:        ctx,
		adminToken: mk("admin", "*:*"),
		readToken: mk("reader",
			"objects:read", "alerts:read", "incidents:read", "events:read"),
		noneToken: mk("outsider", "metrics:read"),
		t:         t,
	}
}

// runPipeline starts the result-processing loop for the duration of the
// test (cancelled on cleanup). Only the results→state e2e test needs it.
func (ta *testAPI) runPipeline() {
	pctx, cancel := context.WithCancel(ta.ctx)
	ta.t.Cleanup(cancel)
	go ta.a.Pipe.Run(pctx)
}

// do issues a request with arbitrary header mods and returns status+body.
func (ta *testAPI) do(method, path string, body any, mods ...func(*http.Request)) (int, []byte) {
	ta.t.Helper()
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rd = bytes.NewReader([]byte(b))
		case []byte:
			rd = bytes.NewReader(b)
		default:
			raw, _ := json.Marshal(b)
			rd = bytes.NewReader(raw)
		}
	}
	r := httptest.NewRequest(method, path, rd)
	if rd != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for _, m := range mods {
		m(r)
	}
	rec := httptest.NewRecorder()
	ta.h.ServeHTTP(rec, r)
	data, _ := io.ReadAll(rec.Body)
	return rec.Code, data
}

// header captures the full response for header assertions (ETag etc.).
func (ta *testAPI) raw(method, path string, body any, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	ta.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, path, rd)
	if rd != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for _, m := range mods {
		m(r)
	}
	rec := httptest.NewRecorder()
	ta.h.ServeHTTP(rec, r)
	return rec
}

// bearer sets the Authorization header.
func bearer(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

// ifMatch sets the If-Match precondition.
func ifMatch(version int64) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("If-Match", `"`+itoa(version)+`"`)
	}
}

// admin / read issue token requests for the two common roles.
func (ta *testAPI) admin(method, path string, body any, mods ...func(*http.Request)) (int, []byte) {
	return ta.do(method, path, body, append([]func(*http.Request){bearer(ta.adminToken)}, mods...)...)
}

func (ta *testAPI) read(method, path string, body any, mods ...func(*http.Request)) (int, []byte) {
	return ta.do(method, path, body, append([]func(*http.Request){bearer(ta.readToken)}, mods...)...)
}

// id pulls the "id" field from a JSON object response.
func (ta *testAPI) id(body []byte) string {
	ta.t.Helper()
	var v struct{ ID string }
	if err := json.Unmarshal(body, &v); err != nil || v.ID == "" {
		ta.t.Fatalf("no id in response: %s", body)
	}
	return v.ID
}

// version pulls the "version" field from a JSON object response.
func (ta *testAPI) version(body []byte) int64 {
	ta.t.Helper()
	var v struct {
		Version int64 `json:"version"`
	}
	_ = json.Unmarshal(body, &v)
	return v.Version
}

// problem decodes an RFC 9457 problem+json body for field assertions.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func decodeProblem(t *testing.T, body []byte) problem {
	t.Helper()
	var p problem
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("body is not problem+json: %s (%v)", body, err)
	}
	return p
}

// mustJSON unmarshals or fails the test.
func mustJSON(t *testing.T, body []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(body, dst); err != nil {
		t.Fatalf("unmarshal: %v — body=%s", err, body)
	}
}

// createHost is a convenience that POSTs a minimal passive host and
// returns its id + the create response body.
func (ta *testAPI) createHost(name, folder string) (string, []byte) {
	ta.t.Helper()
	code, body := ta.admin("POST", "/api/v1/hosts", map[string]any{
		"name": name, "folder": folder,
		"spec": map[string]any{"checkCommand": "passive"},
	})
	if code != http.StatusCreated {
		ta.t.Fatalf("createHost %q: %d %s", name, code, body)
	}
	return ta.id(body), body
}

// seedAlert inserts an open alert directly so lifecycle handlers have
// something to act on without standing up the full alerting pipeline.
func (ta *testAPI) seedAlert(objectID, title string, sev model.Severity) *model.Alert {
	ta.t.Helper()
	al := &model.Alert{
		TenantID: model.DefaultTenant, ObjectID: objectID,
		Severity: sev, Title: title, OpenedAt: time.Now().UTC(),
	}
	saved, _, err := ta.store.UpsertAlert(ta.ctx, al)
	if err != nil {
		ta.t.Fatalf("seed alert: %v", err)
	}
	return saved
}

// waitFor polls cond until true or the deadline; fails otherwise. Used
// only by the async pipeline test — never a fixed sleep.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", what)
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

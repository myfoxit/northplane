// Package server wires the subsystems into a running northplaned
// process and owns HTTP delivery: the API, the SSE hub, the embedded
// SPA (go:embed) and the two server-rendered exceptions — login and the
// public status page (SPEC §12.1).
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/northplane/northplane/internal/agi"
	"github.com/northplane/northplane/internal/ai"
	"github.com/northplane/northplane/internal/alerting"
	"github.com/northplane/northplane/internal/api"
	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/escalation"
	"github.com/northplane/northplane/internal/espa"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/executor"
	"github.com/northplane/northplane/internal/federation"
	ldapsync "github.com/northplane/northplane/internal/ldap"
	"github.com/northplane/northplane/internal/mailin"
	mcpserver "github.com/northplane/northplane/internal/mcp"
	"github.com/northplane/northplane/internal/metrics"
	"github.com/northplane/northplane/internal/mqttin"
	"github.com/northplane/northplane/internal/notify"
	"github.com/northplane/northplane/internal/pipeline"
	"github.com/northplane/northplane/internal/scheduler"
	"github.com/northplane/northplane/internal/sse"
	"github.com/northplane/northplane/internal/storage"
	"github.com/northplane/northplane/internal/traps"
	"github.com/northplane/northplane/internal/tsdb"
	"github.com/northplane/northplane/internal/web"
)

// Server is the assembled process.
type Server struct {
	Cfg   config.Config
	Log   *slog.Logger
	Store *storage.Store
	TSDB  *tsdb.DB

	api     *api.API
	bus     *eventbus.Bus
	cat     *catalog.Catalog
	sched   *scheduler.Scheduler
	exec    *executor.Executor
	pipe    *pipeline.Pipeline
	alert   *alerting.Engine
	correl  *alerting.Correlator
	escal   *escalation.Engine
	notify  *notify.Manager
	traps   *traps.Manager
	mail    *mailin.Manager
	mqtt    *mqttin.Manager
	espa    *espa.Manager
	agi     *agi.Manager
	ldap    *ldapsync.Syncer
	edge    *federation.Edge
	hub     *sse.Hub
	metrics *metrics.Registry
	httpSrv *http.Server

	version string
}

// New builds (but does not start) the server from an open store + tsdb.
func New(ctx context.Context, cfg config.Config, store *storage.Store, ts *tsdb.DB,
	log *slog.Logger, version string) (*Server, error) {
	s := &Server{Cfg: cfg, Log: log, Store: store, TSDB: ts, version: version}

	s.bus = eventbus.New()
	s.cat = catalog.New(store)
	if err := s.cat.LoadAll(ctx); err != nil {
		return nil, err
	}
	s.metrics = metrics.NewRegistry()

	box := resolveSecretBox(cfg, log)
	secrets := auth.SecretsResolver(store, box)

	s.sched = scheduler.New(s.cat, log)
	s.exec = executor.New(executor.Options{
		ExecPoolSize: cfg.ExecPoolSize, PluginsDir: cfg.PluginsDir,
		PluginsAllow: cfg.PluginsAllow, DefaultTimeout: 30 * time.Second,
		ArtifactsDir: cfg.ArtifactsDir(), Secrets: secrets,
	}, s.cat, s.bus, log)
	s.pipe = pipeline.New(store, s.cat, s.bus, ts, s.sched, log)

	s.escal = escalation.New(store, s.bus, log)
	s.alert = alerting.NewEngine(store, s.cat, s.bus, s.escal, log)
	if err := s.alert.ReloadAll(ctx); err != nil {
		return nil, err
	}
	s.correl = alerting.NewCorrelator(store, s.bus, log)
	s.notify = notify.New(store, s.bus, log)
	s.notify.BaseURL = cfg.BaseURL
	s.notify.Secrets = secrets
	s.notify.AckSecret = ackSecret(ctx, store)
	s.initVAPID(ctx)

	// Ingress-Adapter beyond HTTP (SPEC §7.5): SNMP traps + IMAP poller.
	// Both reconcile against event-source definitions at runtime.
	secretFn := func(_ context.Context, tenantID, name string) (string, error) {
		v, ok := secrets(tenantID, name)
		if !ok {
			return "", fmt.Errorf("secret %q not resolvable (no secret store key?)", name)
		}
		return v, nil
	}
	s.traps = traps.New(store, s.bus, secretFn, log)
	s.mail = mailin.New(store, s.bus, secretFn, log)
	// Alarming ingress beyond monitoring (alarming pipelines): MQTT
	// subscriptions and ESPA / ESPA-X pager-protocol TCP listeners.
	s.mqtt = mqttin.New(store, s.bus, box, log)
	s.espa = espa.New(store, s.bus, log)

	// Permission-model upgrades are reconciled additively: system roles
	// gain newly introduced permissions, customised lists keep the rest.
	if err := store.EnsureSystemRolePermission(ctx, "operator", "alerts:write"); err != nil {
		log.Warn("server: role permission reconcile", "err", err)
	}

	s.hub = &sse.Hub{Bus: s.bus, Store: store}

	authn := &auth.Authenticator{Store: store}
	var oidc *auth.OIDC
	if cfg.OIDC.Issuer != "" {
		o, err := auth.NewOIDC(ctx, cfg.OIDC, cfg.BaseURL, store, authn, cfg.TrustProxy)
		if err != nil {
			log.Warn("server: OIDC disabled", "err", err)
		} else {
			oidc = o
		}
	}
	s.ldap = ldapsync.New(cfg.LDAP, store, log) // nil when unconfigured

	s.api = &api.API{
		Cfg: cfg, Store: store, Catalog: s.cat, Bus: s.bus, TSDB: ts,
		Sched: s.sched, Pipe: s.pipe, Alert: s.alert, Escal: s.escal,
		Notify: s.notify, Auth: authn, OIDC: oidc, LDAP: s.ldap, Box: box,
		Hub: s.hub, Metrics: s.metrics, Log: log, StartedAt: time.Now(), Version: version,
	}
	if cfg.Federation.EdgeEnabled() {
		s.edge = federation.NewEdge(cfg.Federation, store, s.api, version, log)
	}
	// AI subsystem (graceful when provider=none)
	s.api.AI = ai.New(ai.Deps{
		Cfg: cfg.AI, Store: store, Catalog: s.cat, Sched: s.sched,
		Escal: s.escal, Bus: s.bus, TSDB: ts, BaseURL: cfg.BaseURL,
		Box: box, Planner: s.api, Reports: s.api, Resources: s.api, Log: log,
	})

	apiHandler := api.New(s.api)
	// FastAGI listener: inbound alarm calls from an on-prem Asterisk/
	// FreePBX (the PBX terminates the SIP trunks; northplane drives the
	// IVR over AGI — the fully cloud-free phone path).
	s.agi = agi.New(s.api, log)

	s.httpSrv = &http.Server{
		Addr:    cfg.Listen,
		Handler: s.rootHandler(apiHandler, authn),
		// ReadHeaderTimeout bounds the slow-headers (slowloris) window;
		// ReadTimeout additionally caps the time to read the whole request
		// body so a slow/stalled uploader can't pin a connection forever.
		// No global WriteTimeout: it would abort the long-lived SSE/MCP
		// streams below — per-route response deadlines are applied via
		// withTimeouts() inside rootHandler instead.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB — reject oversized header floods
	}
	return s, nil
}

// resolveSecretBox makes the secret store self-provisioning so
// encrypted-at-rest features (secrets, AI provider keys) work out of
// the box instead of silently disabling:
//
//  1. an explicitly configured secretKeyFile is used when loadable; if
//     the file simply does not exist yet, the key is generated there
//     (first boot with a fresh config);
//  2. when the explicit path is unusable (read-only mount, a directory
//     left behind by a missing bind-mount source, bad contents) — or no
//     path is configured — the key lives at <dataDir>/secret.key, which
//     persists on the data volume across image pulls.
//
// The fallback is loud: sealing against a different key than the
// operator intended must be visible in the logs.
func resolveSecretBox(cfg config.Config, log *slog.Logger) *auth.SecretBox {
	tryPath := func(path string) *auth.SecretBox {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			if err := auth.GenerateMasterKey(path); err != nil {
				log.Warn("server: cannot create master key", "path", path, "err", err)
				return nil
			}
			log.Info("server: generated secret-store master key", "path", path)
		}
		box, err := auth.LoadMasterKey(path)
		if err != nil {
			log.Warn("server: master key unusable", "path", path, "err", err)
			return nil
		}
		return box
	}
	if cfg.SecretKeyFile != "" {
		if box := tryPath(cfg.SecretKeyFile); box != nil {
			return box
		}
		log.Warn("server: configured secretKeyFile unusable — falling back to the data directory",
			"configured", cfg.SecretKeyFile, "fallback", filepath.Join(cfg.DataDir, "secret.key"))
	}
	box := tryPath(filepath.Join(cfg.DataDir, "secret.key"))
	if box == nil {
		log.Warn("server: secret store disabled (no usable master key)")
	}
	return box
}

// rootHandler routes between API, SSE, server-rendered pages and the SPA.
func (s *Server) rootHandler(apiHandler http.Handler, authn *auth.Authenticator) http.Handler {
	// typed-nil guard: a nil *ldap.Syncer must stay a nil interface
	var directory web.DirectoryVerifier
	if s.ldap != nil {
		directory = s.ldap
	}
	pages := web.NewPages(s.Store, authn, s.api.OIDC, directory, s.Cfg, s.version)
	spa := web.SPAHandler()

	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	mux.Handle("/metrics", apiHandler)
	mux.Handle("/healthz", apiHandler)
	mux.Handle("/readyz", apiHandler)
	mux.Handle("/auth/", pages) // login, callback, logout
	mux.Handle("/status/", pages)
	mux.Handle("/login", pages)
	mux.Handle("/setup", pages)    // one-shot first-run admin setup
	mux.Handle("/register", pages) // self-service signup (viewer role, cfg.AllowSignup)
	// MCP Streamable HTTP (SPEC §10.3) — Northplane tokens authenticate
	if svc, ok := s.api.AI.(*ai.Service); ok {
		mux.Handle("/mcp", mcpserver.HTTPHandler(svc, authn, s.version))
	}
	// SPA + static assets. GateSPA redirects logged-out *document* navigations
	// to /login server-side so the app shell never renders before the client's
	// own 401 redirect (no full-app flash); assets and API auth are unchanged.
	mux.Handle("/", web.GateSPA(spa, authn))
	return securityHeaders(withTimeouts(mux), s.Cfg.TrustProxy)
}

// requestTimeout is the response deadline applied to ordinary (non-streaming)
// requests. Streaming routes (see isStreamingPath) are exempt because they are
// long-lived by design and http.TimeoutHandler's wrapper does not implement
// http.Flusher — wrapping an SSE handler would both abort the stream on the
// deadline and break flushing (the SSE hub 500s on a non-Flusher writer).
const requestTimeout = 30 * time.Second

// withTimeouts applies a per-request response deadline to normal routes while
// exempting the long-lived streaming endpoints (SSE, NDJSON export, MCP
// streamable-HTTP). It stands in for a global http.Server WriteTimeout, which
// would otherwise kill those streams mid-flight.
func withTimeouts(next http.Handler) http.Handler {
	timed := http.TimeoutHandler(next, requestTimeout, "request timeout")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isStreamingPath(r.URL.Path) {
			next.ServeHTTP(w, r) // no deadline, preserve http.Flusher
			return
		}
		timed.ServeHTTP(w, r)
	})
}

// isStreamingPath reports whether path serves a long-lived/flushing response
// that must not carry a write deadline: the SSE hub, the NDJSON event export,
// and the MCP streamable-HTTP endpoint.
func isStreamingPath(path string) bool {
	switch {
	case path == "/api/v1/stream":
		return true
	case path == "/api/v1/events:export":
		return true
	case path == "/api/v1/ai/chat":
		// Agent-chat SSE turn: long-lived while the model streams.
		return true
	case path == "/mcp" || strings.HasPrefix(path, "/mcp/"):
		return true
	default:
		return false
	}
}

// workerRestartBackoff delays restarting a background worker after it
// panics, so a deterministically-panicking worker degrades to a slow
// log loop rather than a hot CPU spin.
const workerRestartBackoff = time.Second

// superviseWorker runs fn, recovering from any panic so a single
// background worker cannot crash the whole process. It returns true if fn
// panicked (the caller may restart it) and false if fn returned normally
// — e.g. because ctx was cancelled.
func superviseWorker(ctx context.Context, log *slog.Logger, name string, fn func(context.Context)) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			log.Error("server: background worker panicked; restarting",
				"worker", name, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	fn(ctx)
	return false
}

// Run starts all subsystems and serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	for _, e := range s.cat.All() {
		s.sched.Upsert(e)
	}

	// Track every background worker so graceful shutdown can wait for them
	// to drain in-flight work (pipeline writes, notify/escalation delivery)
	// after the HTTP server stops accepting requests. Each Run honours ctx
	// cancellation; we only add the WaitGroup bookkeeping. spawn supervises
	// the worker: a panic is recovered (in Go an unrecovered goroutine panic
	// kills the whole process) and the worker is restarted with a short
	// backoff so one bad iteration can't take a subsystem permanently dark.
	var wg sync.WaitGroup
	spawn := func(name string, run func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for superviseWorker(ctx, s.Log, name, run) {
				// superviseWorker returned true → it panicked. Back off,
				// then restart unless we're shutting down.
				select {
				case <-ctx.Done():
					return
				case <-time.After(workerRestartBackoff):
				}
			}
		}()
	}
	spawn("scheduler", s.sched.Run)
	spawn("executor", func(ctx context.Context) { s.exec.Run(ctx, s.sched) })
	spawn("pipeline", s.pipe.Run)
	spawn("alerting", s.alert.Run)
	spawn("correlator", s.correl.Run)
	spawn("escalation", s.escal.Run)
	spawn("notify", s.notify.Run)
	spawn("traps", s.traps.Run)
	spawn("mailin", s.mail.Run)
	spawn("mqttin", s.mqtt.Run)
	spawn("espa", s.espa.Run)
	spawn("agi", s.agi.Run)
	spawn("api-janitor", s.api.Janitor)
	spawn("webhook-dispatcher", s.api.WebhookDispatcher)
	spawn("report-scheduler", s.api.ReportScheduler)
	spawn("dead-man", s.deadManLoop)
	if s.ldap != nil {
		spawn("ldap-sync", s.ldap.Run)
	}
	if s.edge != nil {
		spawn("federation-edge", s.edge.Run)
	}
	if svc, ok := s.api.AI.(interface{ Run(context.Context) }); ok {
		spawn("ai", svc.Run)
	}

	ln, err := net.Listen("tcp", s.Cfg.Listen)
	if err != nil {
		return err
	}
	tlsCfg, useTLS, err := s.tlsConfig(ln)
	if err != nil {
		_ = ln.Close()
		return err
	}
	scheme := "http"
	if useTLS {
		s.httpSrv.TLSConfig = tlsCfg
		ln = tls.NewListener(ln, tlsCfg)
		scheme = "https"
	}
	s.Log.Info("northplane: listening", "addr", s.Cfg.Listen, "scheme", scheme,
		"storage", s.Store.Dialect().Name(), "objects", s.cat.Size(),
		"ai", s.api.AI.Enabled())
	if web.FirstRunOpen(ctx, s.Store) {
		s.Log.Warn("first run: open " + s.setupURL(scheme) + " to create your admin account")
	}

	errCh := make(chan error, 1)
	go func() { errCh <- s.httpSrv.Serve(ln) }()

	select {
	case <-ctx.Done():
		s.Log.Info("northplane: shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := s.httpSrv.Shutdown(shutCtx)
		// ctx is already cancelled (that's why we're here), so every worker
		// is unwinding. Wait for them to finish draining in-flight work,
		// bounded by the same shutdown budget so a stuck worker can't hang
		// the process indefinitely.
		s.waitWorkers(shutCtx, &wg)
		return err
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// waitWorkers blocks until all background workers have returned or the
// shutdown deadline (ctx) elapses, whichever comes first.
func (s *Server) waitWorkers(ctx context.Context, wg *sync.WaitGroup) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		s.Log.Info("northplane: background workers drained")
	case <-ctx.Done():
		s.Log.Warn("northplane: shutdown budget elapsed, workers still running")
	}
}

// tlsConfig resolves the TLS setup; plaintext is refused on non-loopback
// listeners unless TLS is terminated upstream (trustProxy) or explicitly
// insecure (A-15.10). A non-nil error means the server must not start.
func (s *Server) tlsConfig(ln net.Listener) (*tls.Config, bool, error) {
	if s.Cfg.TLS.CertFile != "" && s.Cfg.TLS.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(s.Cfg.TLS.CertFile, s.Cfg.TLS.KeyFile)
		if err != nil {
			return nil, false, fmt.Errorf("TLS cert load failed, refusing to start insecure: %w", err)
		}
		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}, true, nil
	}
	if s.Cfg.TLS.Insecure || s.Cfg.TrustProxy || isLoopback(ln.Addr()) {
		s.Log.Warn("server: serving plaintext HTTP (loopback/dev or behind a TLS-terminating proxy — A-15.10 requires TLS in production)")
		return nil, false, nil
	}
	return nil, false, errors.New("no TLS configured on a non-loopback listener — set tls.certFile/keyFile, or trustProxy behind a TLS-terminating proxy, or tls.insecure for dev")
}

// setupURL derives a human-pasteable URL for the first-run hint: the
// configured external BaseURL when set, otherwise scheme + listen address
// with a wildcard/empty host rewritten to 127.0.0.1.
func (s *Server) setupURL(scheme string) string {
	if s.Cfg.BaseURL != "" {
		return strings.TrimRight(s.Cfg.BaseURL, "/") + "/setup"
	}
	host, port, err := net.SplitHostPort(s.Cfg.Listen)
	if err != nil {
		return scheme + "://" + s.Cfg.Listen + "/setup"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return scheme + "://" + net.JoinHostPort(host, port) + "/setup"
}

func isLoopback(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// securityHeaders applies HSTS and hardening (SPEC §13.2).
func securityHeaders(next http.Handler, trustProxy bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if auth.RequestIsHTTPS(r, trustProxy) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			// steptOrigin carries the embedded Stept assistant (chat widget +
			// product tours): its loader script, the messenger iframe and the
			// widget's own API/WebSocket calls all come from that one origin.
			const steptOrigin = "https://app.stepped.ai"
			h.Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data: "+steptOrigin+"; "+
					"style-src 'self' 'unsafe-inline'; "+
					"script-src 'self' "+steptOrigin+"; "+
					"connect-src 'self' "+steptOrigin+" wss://app.stepped.ai; "+
					"frame-src 'self' "+steptOrigin+"; "+
					"frame-ancestors 'none'; base-uri 'self'")
		}
		next.ServeHTTP(w, r)
	})
}

// ackSecret loads (or generates+persists) the ack-link HMAC key.
func ackSecret(ctx context.Context, store *storage.Store) []byte {
	var keyHex string
	if err := store.KVGet(ctx, "ack_secret", &keyHex); err == nil && keyHex != "" {
		return []byte(keyHex)
	}
	secret := modelNewSecret(32)
	_ = store.KVPut(ctx, "ack_secret", secret)
	return []byte(secret)
}

// deadManLoop pings the external heartbeat (SPEC §14.2 / P7).
func (s *Server) deadManLoop(ctx context.Context) {
	if s.Cfg.DeadManURL == "" {
		return
	}
	interval := s.Cfg.DeadManInterval
	if interval <= 0 {
		interval = time.Minute
	}
	client := &http.Client{Timeout: 10 * time.Second}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// only ping while healthy (check processing not stalled)
			if s.bus.Stats().ResultsDepth > 7000 {
				s.Log.Warn("deadman: skipping ping, results queue saturated")
				continue
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.Cfg.DeadManURL, nil)
			if resp, err := client.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	}
}

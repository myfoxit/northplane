// Package server wires the subsystems into a running northplaned
// process and owns HTTP delivery: the API, the SSE hub, the embedded
// SPA (go:embed) and the two server-rendered exceptions — login and the
// public status page (SPEC §12.1).
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/ai"
	"github.com/northplane/northplane/internal/alerting"
	"github.com/northplane/northplane/internal/api"
	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/escalation"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/executor"
	mcpserver "github.com/northplane/northplane/internal/mcp"
	"github.com/northplane/northplane/internal/metrics"
	"github.com/northplane/northplane/internal/notify"
	"github.com/northplane/northplane/internal/pipeline"
	"github.com/northplane/northplane/internal/scheduler"
	"github.com/northplane/northplane/internal/sse"
	"github.com/northplane/northplane/internal/storage"
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

	// secret box (optional)
	var box *auth.SecretBox
	if cfg.SecretKeyFile != "" {
		b, err := auth.LoadMasterKey(cfg.SecretKeyFile)
		if err != nil {
			log.Warn("server: secret store disabled", "err", err)
		} else {
			box = b
		}
	}
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

	s.api = &api.API{
		Cfg: cfg, Store: store, Catalog: s.cat, Bus: s.bus, TSDB: ts,
		Sched: s.sched, Pipe: s.pipe, Alert: s.alert, Escal: s.escal,
		Notify: s.notify, Auth: authn, OIDC: oidc, Box: box, Hub: s.hub,
		Metrics: s.metrics, Log: log, StartedAt: time.Now(), Version: version,
	}
	// AI subsystem (graceful when provider=none)
	s.api.AI = ai.New(ai.Deps{
		Cfg: cfg.AI, Store: store, Catalog: s.cat, Sched: s.sched,
		Escal: s.escal, Bus: s.bus, TSDB: ts, BaseURL: cfg.BaseURL,
		Planner: s.api, Log: log,
	})

	apiHandler := api.New(s.api)
	s.httpSrv = &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.rootHandler(apiHandler, authn),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s, nil
}

// rootHandler routes between API, SSE, server-rendered pages and the SPA.
func (s *Server) rootHandler(apiHandler http.Handler, authn *auth.Authenticator) http.Handler {
	pages := web.NewPages(s.Store, authn, s.api.OIDC, s.Cfg, s.version)
	spa := web.SPAHandler()

	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	mux.Handle("/metrics", apiHandler)
	mux.Handle("/healthz", apiHandler)
	mux.Handle("/readyz", apiHandler)
	mux.Handle("/auth/", pages) // login, callback, logout
	mux.Handle("/status/", pages)
	mux.Handle("/login", pages)
	// MCP Streamable HTTP (SPEC §10.3) — Northplane tokens authenticate
	if svc, ok := s.api.AI.(*ai.Service); ok {
		mux.Handle("/mcp", mcpserver.HTTPHandler(svc, authn, s.version))
	}
	mux.Handle("/", spa) // SPA + static assets (auth enforced client-side + API)
	return securityHeaders(mux, s.Cfg.TrustProxy)
}

// Run starts all subsystems and serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	for _, e := range s.cat.All() {
		s.sched.Upsert(e)
	}
	go s.sched.Run(ctx)
	go s.exec.Run(ctx, s.sched)
	go s.pipe.Run(ctx)
	go s.alert.Run(ctx)
	go s.correl.Run(ctx)
	go s.escal.Run(ctx)
	go s.notify.Run(ctx)
	go s.api.Janitor(ctx)
	go s.api.WebhookDispatcher(ctx)
	go s.deadManLoop(ctx)
	if svc, ok := s.api.AI.(interface{ Run(context.Context) }); ok {
		go svc.Run(ctx)
	}

	ln, err := net.Listen("tcp", s.Cfg.Listen)
	if err != nil {
		return err
	}
	tlsCfg, useTLS := s.tlsConfig(ln)
	scheme := "http"
	if useTLS {
		s.httpSrv.TLSConfig = tlsCfg
		ln = tls.NewListener(ln, tlsCfg)
		scheme = "https"
	}
	s.Log.Info("northplane: listening", "addr", s.Cfg.Listen, "scheme", scheme,
		"storage", s.Store.Dialect().Name(), "objects", s.cat.Size(),
		"ai", s.api.AI.Enabled())

	errCh := make(chan error, 1)
	go func() { errCh <- s.httpSrv.Serve(ln) }()

	select {
	case <-ctx.Done():
		s.Log.Info("northplane: shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return s.httpSrv.Shutdown(shutCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// tlsConfig resolves the TLS setup; plaintext is refused on non-loopback
// listeners unless explicitly insecure (A-15.10).
func (s *Server) tlsConfig(ln net.Listener) (*tls.Config, bool) {
	if s.Cfg.TLS.CertFile != "" && s.Cfg.TLS.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(s.Cfg.TLS.CertFile, s.Cfg.TLS.KeyFile)
		if err != nil {
			s.Log.Error("server: TLS cert load failed, refusing to start insecure", "err", err)
			return nil, false
		}
		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}, true
	}
	if s.Cfg.TLS.Insecure || isLoopback(ln.Addr()) {
		s.Log.Warn("server: serving plaintext HTTP (dev/loopback only — A-15.10 requires TLS in production)")
		return nil, false
	}
	s.Log.Error("server: no TLS configured on a non-loopback listener — set tls.certFile/keyFile or tls.insecure for dev")
	return nil, false
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
			h.Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
					"connect-src 'self'; frame-ancestors 'none'; base-uri 'self'")
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

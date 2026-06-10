// Package federation implements the edge half of main↔edge federation
// (SPEC §7.7 deployment variant B): a customer-site northplaned that
// dials out to a main instance, reports status heartbeats and pulls its
// declarative config bundle — so the customer site is configured from
// the main instance without any inbound connectivity.
//
// The edge stays a full instance: scheduling, Nagios plugin execution,
// agents, notification delivery all run locally and keep working when
// the uplink is down. Federation only adds "configured from above" and
// "visible from above".
package federation

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// BundleApplier applies a config bundle locally — implemented by
// *api.API (the same path `np apply` and the AI config layer use, so
// validation and catalog reload behave identically).
type BundleApplier interface {
	ApplyBundleYAML(ctx context.Context, tenantID, yamlText string) (any, error)
}

// Edge is the uplink worker.
type Edge struct {
	Cfg     config.FederationConfig
	Store   *storage.Store
	Applier BundleApplier
	Version string
	Log     *slog.Logger

	client *http.Client

	mu         sync.Mutex
	bundleETag string // ETag of the last successfully applied bundle
	applyError string
}

// NewEdge builds the uplink worker (call only when EdgeEnabled).
func NewEdge(cfg config.FederationConfig, store *storage.Store, applier BundleApplier,
	version string, log *slog.Logger) *Edge {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg.InsecureSkipVerify {
		client.Transport = insecureTransport()
	}
	return &Edge{Cfg: cfg, Store: store, Applier: applier, Version: version,
		Log: log, client: client}
}

// Run ticks until ctx is cancelled. Order per tick: pull/apply first so
// the heartbeat reports the post-apply state.
func (e *Edge) Run(ctx context.Context) {
	interval := e.Cfg.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	e.Log.Info("federation: edge mode", "main", e.Cfg.MainURL, "site", e.Cfg.Site,
		"interval", interval, "applyConfig", e.Cfg.ApplyConfigEnabled())
	e.tick(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.tick(ctx)
		}
	}
}

func (e *Edge) tick(ctx context.Context) {
	if e.Cfg.ApplyConfigEnabled() {
		if err := e.pullAndApply(ctx); err != nil {
			e.Log.Warn("federation: bundle pull/apply failed", "err", err)
		}
	}
	if err := e.heartbeat(ctx); err != nil {
		e.Log.Warn("federation: heartbeat failed", "err", err)
	}
}

// pullAndApply does a conditional GET of the site bundle and applies it
// when it changed. The ETag advances only after a successful apply, so
// a failed apply retries every tick until it succeeds or is replaced.
func (e *Edge) pullAndApply(ctx context.Context) error {
	req, err := e.request(ctx, http.MethodGet, ":pull", nil)
	if err != nil {
		return err
	}
	e.mu.Lock()
	etag := e.bundleETag
	e.mu.Unlock()
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pull: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	newTag := resp.Header.Get("ETag")
	if len(strings.TrimSpace(string(body))) == 0 {
		// empty bundle = nothing managed centrally yet; remember the tag
		e.setApplied(newTag, "")
		return nil
	}
	if _, err := e.Applier.ApplyBundleYAML(ctx, model.DefaultTenant, string(body)); err != nil {
		e.setApplied(etag, err.Error()) // keep old tag → retry next tick
		return fmt.Errorf("apply: %w", err)
	}
	e.setApplied(newTag, "")
	_, _ = e.Store.AppendAudit(ctx, &model.AuditEntry{
		TenantID: model.DefaultTenant, ActorType: model.ActorSystem,
		ActorID: "federation", Action: "federation.apply",
		Resource: e.Cfg.Site, AfterJSON: fmt.Sprintf(`{"etag":%q}`, newTag),
	})
	e.Log.Info("federation: bundle applied", "etag", newTag)
	return nil
}

func (e *Edge) setApplied(etag, applyErr string) {
	e.mu.Lock()
	e.bundleETag = etag
	e.applyError = applyErr
	e.mu.Unlock()
}

// heartbeat posts the edge status (version, applied bundle, counters).
func (e *Edge) heartbeat(ctx context.Context) error {
	hosts, _ := e.Store.CountObjects(ctx, model.DefaultTenant, model.KindHost)
	services, _ := e.Store.CountObjects(ctx, model.DefaultTenant, model.KindService)
	var alertsOpen int64
	if sevs, err := e.Store.OpenAlertStats(ctx, model.DefaultTenant); err == nil {
		for _, n := range sevs {
			alertsOpen += n
		}
	}
	e.mu.Lock()
	hb := model.SiteHeartbeat{
		Version: e.Version, BundleETag: e.bundleETag, ApplyError: e.applyError,
		Stats: map[string]int64{"hosts": hosts, "services": services, "alertsOpen": alertsOpen},
	}
	e.mu.Unlock()
	req, err := e.request(ctx, http.MethodPost, ":heartbeat", hb)
	if err != nil {
		return err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("heartbeat: HTTP %d", resp.StatusCode)
	}
	return nil
}

// request builds an authenticated call to the main instance's site
// action endpoint (":pull" / ":heartbeat").
func (e *Edge) request(ctx context.Context, method, action string, jsonBody any) (*http.Request, error) {
	u := strings.TrimSuffix(e.Cfg.MainURL, "/") + "/api/v1/sites/" +
		url.PathEscape(e.Cfg.Site) + action
	var body io.Reader
	if jsonBody != nil {
		raw, err := json.Marshal(jsonBody)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+e.Cfg.Token)
	if jsonBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func insecureTransport() *http.Transport {
	return &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // operator opt-in for self-signed main instances
	}
}

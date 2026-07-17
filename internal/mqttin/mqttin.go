// Package mqttin is the MQTT ingress adapter (SPEC §7.5): it subscribes
// to broker topics and turns every received message into a normalised
// monitoring event, fed into the very same event pipeline as the
// HTTP/webhook ingress so alert rules behave identically (mirrors
// internal/mailin and internal/api/ingress.go).
//
// One Manager reconciles every enabled EventSource of type "mqtt" into
// exactly one broker connection; connections auto-reconnect, resubscribe
// their topic filters on reconnect, and publish events rate-limited per
// source.
package mqttin

import (
	"context"
	"encoding/json"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

const (
	// reconcileInterval is how often the Manager re-reads sources and
	// starts/stops/restarts broker connections.
	reconcileInterval = 30 * time.Second
	// connectRetryInterval spaces initial-connect retries; reconnects
	// after a lost connection back off automatically (paho default).
	connectRetryInterval = 30 * time.Second
	// keepAlive is the MQTT keep-alive interval sent to the broker.
	keepAlive = 60 * time.Second
	// subscribeTimeout bounds waiting for a SUBACK per topic filter.
	subscribeTimeout = 10 * time.Second
	// disconnectQuiesce is how long Disconnect waits for in-flight
	// work, in milliseconds (paho API takes a plain uint).
	disconnectQuiesce = 250
	// secretTimeout bounds one secret-store lookup during connect.
	secretTimeout = 10 * time.Second
)

// Manager owns the set of MQTT broker connections (SPEC §7.5).
type Manager struct {
	store *storage.Store
	bus   *eventbus.Bus
	box   *auth.SecretBox
	log   *slog.Logger

	mu      sync.Mutex
	running map[string]*runner // key: source ID (unique across tenants)

	received atomic.Uint64 // messages delivered by brokers
	dropped  atomic.Uint64 // messages discarded (rate limit, mapping error, publish failure)
}

// New builds a Manager. A nil logger falls back to slog.Default(); a nil
// SecretBox means only inline Config["password"] is usable.
func New(store *storage.Store, bus *eventbus.Bus, box *auth.SecretBox, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		store:   store,
		bus:     bus,
		box:     box,
		log:     log,
		running: map[string]*runner{},
	}
}

// Run reconciles broker connections until ctx is cancelled. Each enabled
// mqtt source gets one connection goroutine; a connection is restarted
// when its config fingerprint changes and stopped when the source
// disappears or is disabled. On shutdown every connection is closed.
func (m *Manager) Run(ctx context.Context) {
	m.reconcile(ctx)
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-ticker.C:
			m.reconcile(ctx)
		}
	}
}

// Stats reports the number of running source connections and the message
// counters: received counts every message delivered by a broker, dropped
// counts messages discarded due to rate limiting, mapping errors or
// publish failures.
func (m *Manager) Stats() (sources int, received uint64, dropped uint64) {
	m.mu.Lock()
	sources = len(m.running)
	m.mu.Unlock()
	return sources, m.received.Load(), m.dropped.Load()
}

// reconcile diffs desired (enabled mqtt sources) against running
// connections and applies start/stop/restart.
func (m *Manager) reconcile(ctx context.Context) {
	desired := m.desiredSources(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop runners whose source vanished, was disabled, or changed config.
	for id, r := range m.running {
		want, ok := desired[id]
		if !ok || want.fingerprint != r.fingerprint {
			r.stop()
			delete(m.running, id)
		}
	}
	// Start runners for newly-desired sources.
	for id, want := range desired {
		if _, ok := m.running[id]; ok {
			continue
		}
		rctx, cancel := context.WithCancel(ctx)
		r := &runner{
			mgr:         m,
			tenantID:    want.tenantID,
			src:         want.src,
			cfg:         want.cfg,
			fingerprint: want.fingerprint,
			limiter:     newBucket(want.src.RateLimit, want.src.Burst),
			cancel:      cancel,
		}
		r.wg.Add(1)
		go r.run(rctx)
		m.running[id] = r
	}
}

type desired struct {
	tenantID    string
	src         *model.EventSource
	cfg         mqttConfig
	fingerprint string
}

// desiredSources lists every enabled mqtt source across all tenants.
func (m *Manager) desiredSources(ctx context.Context) map[string]desired {
	out := map[string]desired{}
	tenants, err := m.store.Tenants(ctx)
	if err != nil {
		m.log.Warn("mqttin: list tenants failed", "err", err)
		return out
	}
	for _, t := range tenants {
		srcs, err := storage.LoadAll[model.EventSource](ctx, m.store, t.ID, storage.KindEventSource)
		if err != nil {
			m.log.Warn("mqttin: list event-sources failed", "tenant", t.ID, "err", err)
			continue
		}
		for _, src := range srcs {
			if !src.Enabled || !strings.EqualFold(src.Type, "mqtt") {
				continue
			}
			cfg, err := parseConfig(src)
			if err != nil {
				m.log.Warn("mqttin: invalid mqtt config", "source", src.Name, "err", err)
				continue
			}
			out[src.ID] = desired{
				tenantID:    t.ID,
				src:         src,
				cfg:         cfg,
				fingerprint: cfg.fingerprint(src),
			}
		}
	}
	return out
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	rs := make([]*runner, 0, len(m.running))
	for id, r := range m.running {
		rs = append(rs, r)
		delete(m.running, id)
	}
	m.mu.Unlock()
	for _, r := range rs {
		r.stop()
	}
}

// runner drives one MQTT source: a single auto-reconnecting broker
// connection subscribed to the source's topic filters.
type runner struct {
	mgr         *Manager
	tenantID    string
	src         *model.EventSource
	cfg         mqttConfig
	fingerprint string
	limiter     *tokenBucket

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (r *runner) stop() {
	r.cancel()
	r.wg.Wait()
}

// run connects to the broker and blocks until ctx is cancelled. paho owns
// reconnection (AutoReconnect + ConnectRetry); the OnConnect handler
// resubscribes the topic filters after every (re)connect.
func (r *runner) run(ctx context.Context) {
	defer r.wg.Done()
	log := r.mgr.log.With("source", r.src.Name, "tenant", r.tenantID, "url", r.cfg.url)
	log.Info("mqttin: connector started", "topics", r.cfg.topics, "qos", r.cfg.qos)
	defer log.Info("mqttin: connector stopped")

	client := mqtt.NewClient(r.clientOptions(ctx, log))
	tok := client.Connect()
	// ConnectRetry keeps retrying in the background; surface an early
	// failure for the operator without blocking shutdown. WaitTimeout
	// bounds the watcher goroutine even if the token never completes.
	go func() {
		if !tok.WaitTimeout(time.Minute) {
			return // still retrying; OnConnect / OnConnectionLost log later
		}
		if err := tok.Error(); err != nil && ctx.Err() == nil {
			log.Warn("mqttin: connect failed (retrying)", "err", err)
		}
	}()

	<-ctx.Done()
	client.Disconnect(disconnectQuiesce)
}

// clientOptions assembles the full paho options for this runner: the pure
// transport options from the config plus credentials and handlers.
func (r *runner) clientOptions(ctx context.Context, log *slog.Logger) *mqtt.ClientOptions {
	opts := clientOptions(r.cfg)
	if r.cfg.username != "" || r.cfg.passwordLit != "" || r.cfg.passwordRef != "" {
		// CredentialsProvider runs on every connect attempt, so a rotated
		// secret is picked up on the next reconnect.
		opts.SetCredentialsProvider(func() (string, string) {
			return r.cfg.username, r.password(ctx, log)
		})
	}
	handler := func(_ mqtt.Client, msg mqtt.Message) {
		r.handleMessage(ctx, log, msg.Topic(), msg.Payload())
	}
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		log.Info("mqttin: connected")
		// Resubscribe on every (re)connect: CleanSession(true) means the
		// broker forgets subscriptions between sessions.
		for _, topic := range r.cfg.topics {
			t := c.Subscribe(topic, r.cfg.qos, handler)
			if !t.WaitTimeout(subscribeTimeout) {
				log.Warn("mqttin: subscribe timed out", "topic", topic)
				continue
			}
			if err := t.Error(); err != nil {
				log.Warn("mqttin: subscribe failed", "topic", topic, "err", err)
			}
		}
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		log.Warn("mqttin: connection lost (auto-reconnecting)", "err", err)
	})
	return opts
}

// password resolves the source credential: secret reference first
// (store.GetSecret + SecretBox.Open, like mailin), inline
// Config["password"] as fallback.
func (r *runner) password(ctx context.Context, log *slog.Logger) string {
	if r.cfg.passwordRef == "" || r.mgr.box == nil {
		return r.cfg.passwordLit
	}
	sctx, cancel := context.WithTimeout(ctx, secretTimeout)
	defer cancel()
	blob, err := r.mgr.store.GetSecret(sctx, r.tenantID, r.cfg.passwordRef)
	if err == nil {
		v, oerr := r.mgr.box.Open(blob)
		if oerr == nil {
			return v
		}
		err = oerr
	}
	log.Warn("mqttin: secret resolution failed", "ref", r.cfg.passwordRef, "err", err)
	return r.cfg.passwordLit
}

// handleMessage converts one MQTT message into an event and publishes it.
// It recovers from panics: the CEL mapping runs over untrusted payloads
// and paho would propagate a handler panic to the runtime (same rationale
// as mailin's safePoll).
func (r *runner) handleMessage(ctx context.Context, log *slog.Logger, topic string, payload []byte) {
	defer func() {
		if rec := recover(); rec != nil {
			r.mgr.dropped.Add(1)
			log.Error("mqttin: message handler panic recovered",
				"topic", topic, "panic", rec, "stack", string(debug.Stack()))
		}
	}()
	r.mgr.received.Add(1)
	now := time.Now()
	if !r.limiter.allow(now) {
		r.mgr.dropped.Add(1)
		log.Debug("mqttin: rate limit exceeded, message dropped", "topic", topic)
		return
	}
	if len(r.src.Mapping) > 0 && !json.Valid(payload) {
		log.Debug("mqttin: payload is not JSON, mapping skipped (plain-text fallback)", "topic", topic)
	}
	norm, err := buildEvent(r.src, topic, payload, now)
	if err != nil {
		r.mgr.dropped.Add(1)
		log.Warn("mqttin: message rejected", "topic", topic, "err", err)
		return
	}
	if err := r.publish(ctx, norm); err != nil {
		r.mgr.dropped.Add(1)
		log.Warn("mqttin: publish failed", "topic", topic, "err", err)
	}
}

// publish inserts the event and fans it out on the bus, mirroring the
// HTTP ingress path (internal/api/ingress.go publishNorm) and mailin.
func (r *runner) publish(ctx context.Context, norm *model.NormEvent) error {
	raw, err := json.Marshal(norm)
	if err != nil {
		return err
	}
	ev := &model.Event{
		ID:       model.NewID(),
		TenantID: r.tenantID,
		TS:       norm.ReceivedAt,
		Type:     model.EventIngress,
		SourceID: r.src.ID,
		Severity: norm.Severity,
		Payload:  raw,
	}
	if err := r.mgr.store.InsertEvents(ctx, []*model.Event{ev}); err != nil {
		return err
	}
	return r.mgr.bus.PublishEventCtx(ctx, ev)
}

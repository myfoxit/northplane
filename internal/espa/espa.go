// Package espa is the pager-protocol ingress adapter (SPEC §7.5): it
// accepts TCP connections carrying ESPA 4.4.4 (the classic serial paging
// protocol as bridged onto raw TCP by serial-device servers) and ESPA-X
// 2.0 (the XML-over-TCP successor used by nurse-call/DECT/fire-panel
// systems), and turns every paging call into the same normalised
// model.Event the webhook ingress emits, so the identical alert rules
// fire.
//
// It mirrors internal/traps: one Manager reconciles every enabled
// EventSource of type "espa" or "espa-x" across all tenants every 30s
// into exactly one TCP listener per distinct listen address. Listeners
// are hot-managed — sources added/removed/re-pointed take effect within
// the poll interval — and a panicking connection handler can never take
// a listener down.
package espa

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// reconcileInterval is how often the source set is re-read and listeners
// are reconciled (SPEC §7.5: config changes take effect without restart).
const reconcileInterval = 30 * time.Second

// Source types handled by this adapter.
const (
	typeESPA  = "espa"   // ESPA 4.4.4 over TCP (serial bridge)
	typeESPAX = "espa-x" // ESPA-X 2.0, XML over TCP
)

// Default listen addresses per source type. ESPA 4.4.4 has no IANA port
// (it is a serial protocol); ESPA-X conventionally uses 8123.
const (
	defaultListenESPA  = "tcp://:2023"
	defaultListenESPAX = "tcp://:8123"
)

// maxLabelValue truncates oversized field values used as label values.
const maxLabelValue = 200

// maxSummaryChars caps the event summary derived from the display text.
const maxSummaryChars = 512

// Manager owns the ESPA listeners and the reconcile loop.
type Manager struct {
	Store *storage.Store
	Bus   *eventbus.Bus
	Log   *slog.Logger

	// Interval overrides reconcileInterval (tests use a short value).
	Interval time.Duration

	mu        sync.Mutex
	listeners map[string]*listener // listen addr → running listener
	runCtx    context.Context      // Run's ctx; publishes derive from it

	// counters for self-metrics / debugging (no external dependency).
	received atomic64
	dropped  atomic64
	emitted  atomic64
}

// New builds a Manager. log may be nil (defaults to slog.Default()).
func New(store *storage.Store, bus *eventbus.Bus, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		Store:     store,
		Bus:       bus,
		Log:       log,
		listeners: map[string]*listener{},
	}
}

// Run drives the reconcile loop until ctx is cancelled, then tears down
// every listener cleanly (SPEC §7.2 clean shutdown).
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	m.runCtx = ctx
	m.mu.Unlock()

	interval := m.Interval
	if interval <= 0 {
		interval = reconcileInterval
	}
	m.reconcile(ctx) // once immediately so listeners are up without waiting
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.shutdown()
			return
		case <-ticker.C:
			m.reconcile(ctx)
		}
	}
}

// baseCtx is the context event publishing uses: the Manager's Run ctx
// (never a per-connection one, so a slow peer cannot pin a publish).
func (m *Manager) baseCtx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runCtx != nil {
		return m.runCtx
	}
	return context.Background()
}

// shutdown stops all listeners (called on ctx cancel).
func (m *Manager) shutdown() {
	m.mu.Lock()
	ls := m.listeners
	m.listeners = map[string]*listener{}
	m.mu.Unlock()
	for addr, l := range ls {
		l.stop()
		m.Log.Info("espa: listener stopped", "listen", addr)
	}
}

// boundSource pairs an EventSource with its tenant.
type boundSource struct {
	tenantID string
	src      *model.EventSource
}

// reconcile loads the desired source set and makes the live listeners
// match it: start new listen addresses, stop vanished ones, and hot-swap
// the source on the ones that persist (add/remove/port change without a
// restart, mirroring traps).
func (m *Manager) reconcile(ctx context.Context) {
	desired, err := m.loadSources(ctx)
	if err != nil {
		m.Log.Error("espa: reconcile load failed", "err", err)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop listeners whose address no longer has a source.
	for addr, l := range m.listeners {
		if _, ok := desired[addr]; !ok {
			l.stop()
			delete(m.listeners, addr)
			m.Log.Info("espa: listener stopped", "listen", addr)
		}
	}

	// Start/refresh listeners for every desired address. An existing
	// listener keeps its socket; only the bound source is swapped (new
	// connections pick up the new source/protocol, established ones
	// finish under the old one).
	for addr, bs := range desired {
		if l, ok := m.listeners[addr]; ok {
			l.setSource(bs)
			continue
		}
		l := newListener(m.Log, addr, hostPort(addr), m.emit)
		l.setSource(bs)
		if err := l.start(); err != nil {
			m.Log.Error("espa: listener start failed", "listen", addr, "err", err)
			continue
		}
		m.listeners[addr] = l
		m.Log.Info("espa: listening for pager calls",
			"listen", addr, "type", bs.src.Type, "source", bs.src.Name)
	}
}

// loadSources returns enabled espa/espa-x sources keyed by listen address.
func (m *Manager) loadSources(ctx context.Context) (map[string]boundSource, error) {
	tenants, err := m.Store.Tenants(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	var all []boundSource
	for _, t := range tenants {
		srcs, err := storage.LoadAll[model.EventSource](ctx, m.Store, t.ID, storage.KindEventSource)
		if err != nil {
			return nil, fmt.Errorf("load event-sources for tenant %s: %w", t.ID, err)
		}
		for _, s := range srcs {
			if (s.Type != typeESPA && s.Type != typeESPAX) || !s.Enabled {
				continue
			}
			all = append(all, boundSource{tenantID: t.ID, src: s})
		}
	}
	return groupByListen(all, m.Log), nil
}

// groupByListen maps sources to their listen address. Unlike SNMP traps
// (where several communities share one socket) a raw pager stream has no
// in-band source discriminator, so exactly one source owns an address:
// the first one wins and duplicates are logged and ignored.
func groupByListen(sources []boundSource, log *slog.Logger) map[string]boundSource {
	out := make(map[string]boundSource, len(sources))
	for _, bs := range sources {
		addr := listenAddrFor(bs.src)
		if prev, ok := out[addr]; ok {
			log.Warn("espa: duplicate listen address; source ignored",
				"listen", addr, "kept", prev.src.Name, "ignored", bs.src.Name)
			continue
		}
		out[addr] = bs
	}
	return out
}

// listenAddrFor resolves the configured listen address of a source,
// defaulting per protocol type.
func listenAddrFor(s *model.EventSource) string {
	if v := strings.TrimSpace(s.Config["listen"]); v != "" {
		return v
	}
	if s.Type == typeESPAX {
		return defaultListenESPAX
	}
	return defaultListenESPA
}

// hostPort strips the tcp:// scheme off a listen address, yielding the
// host:port string net.Listen wants. Bare "host:port" is accepted too.
func hostPort(addr string) string {
	return strings.TrimPrefix(strings.TrimSpace(addr), "tcp://")
}

// configSeverity reads Config["severity"] (the severity of calls that
// carry no or a routine priority), defaulting to info: a routine pager
// call is informational, unlike an unknown SNMP trap.
func configSeverity(src *model.EventSource) model.Severity {
	sev := model.Severity(strings.TrimSpace(src.Config["severity"]))
	if sev.Valid() {
		return sev
	}
	return model.SevInfo
}

// emit finalises a protocol-level NormEvent for a source and publishes
// it exactly like the other ingress adapters (rate limit → normal form →
// InsertEvents → bus fan-out). It is the listener's emit callback; the
// protocol handlers themselves only see an emitFunc so tests can capture
// events without a store or bus.
func (m *Manager) emit(bs boundSource, norm *model.NormEvent) {
	m.received.add(1)
	src := bs.src

	if !allowRate(src.ID, src.RateLimit, src.Burst) {
		m.dropped.add(1)
		m.Log.Debug("espa: rate limited", "source", src.Name)
		return
	}

	now := time.Now().UTC()
	norm.Source = src.ID
	norm.ReceivedAt = now
	labels := model.Labels{"source": src.Name}.Merge(norm.Labels)
	norm.Labels = src.Labels.Merge(labels) // protocol fields win over source labels

	raw, err := json.Marshal(norm)
	if err != nil {
		m.dropped.add(1)
		m.Log.Error("espa: marshal normevent failed", "source", src.Name, "err", err)
		return
	}
	ev := &model.Event{
		ID:       model.NewID(),
		TenantID: bs.tenantID,
		TS:       now,
		Type:     model.EventIngress,
		SourceID: src.ID,
		Severity: norm.Severity,
		Payload:  raw,
	}

	ctx := m.baseCtx()
	if err := m.Store.InsertEvents(ctx, []*model.Event{ev}); err != nil {
		m.Log.Error("espa: insert event failed", "source", src.Name, "err", err)
		return
	}
	if err := m.Bus.PublishEventCtx(ctx, ev); err != nil {
		m.Log.Error("espa: publish abandoned", "source", src.Name, "err", err)
		return
	}
	m.emitted.add(1)
	m.Log.Debug("espa: event published",
		"source", src.Name, "severity", norm.Severity, "summary", norm.Summary)
}

// Stats reports lifetime counters (received/dropped/emitted) and the
// number of live listeners — for self-metrics wiring.
type Stats struct {
	Listeners int    `json:"listeners"`
	Received  uint64 `json:"received"`
	Dropped   uint64 `json:"dropped"`
	Emitted   uint64 `json:"emitted"`
}

// Stats snapshots the counters.
func (m *Manager) Stats() Stats {
	m.mu.Lock()
	n := len(m.listeners)
	m.mu.Unlock()
	return Stats{
		Listeners: n,
		Received:  m.received.load(),
		Dropped:   m.dropped.load(),
		Emitted:   m.emitted.load(),
	}
}

// truncate caps s at n bytes with an ellipsis marker (label hygiene).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

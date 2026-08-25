// Package traps is the SNMP trap receiver ingress adapter (SPEC §7.5,
// F-01.02). It is a self-contained subsystem: a reconcile loop watches
// every enabled EventSource of type "snmp-trap" across all tenants,
// owns one gosnmp TrapListener per distinct listen address, and on each
// incoming trap matches the source (community for v1/v2c, USM user for
// v3), normalises it into the same model.Event normal form the webhook
// ingress emits (internal/api/ingress.go), and publishes it on the bus
// so the identical alerting rules fire.
//
// Listeners are hot-managed: sources added/removed/re-pointed at another
// port reconcile within the poll interval without dropping unrelated
// listeners. A panicking handler can never take a listener down.
package traps

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// reconcileInterval is how often the source set is re-read and listeners
// are reconciled (SPEC §7.5: config changes take effect without restart).
const reconcileInterval = 30 * time.Second

// Well-known OIDs used to classify standard traps (RFC 1907 / RFC 3418).
const (
	oidSnmpTrapOID = "1.3.6.1.6.3.1.1.4.1.0" // snmpTrapOID.0 varbind in every v2c/v3 trap
	oidColdStart   = "1.3.6.1.6.3.1.1.5.1"
	oidWarmStart   = "1.3.6.1.6.3.1.1.5.2"
	oidLinkDown    = "1.3.6.1.6.3.1.1.5.3"
	oidLinkUp      = "1.3.6.1.6.3.1.1.5.4"
	oidAuthFailure = "1.3.6.1.6.3.1.1.5.5"
	oidEgpNeighbor = "1.3.6.1.6.3.1.1.5.6"
)

// maxVarbindLabels caps how many varbinds are promoted to event labels.
const maxVarbindLabels = 20

// maxLabelValue truncates oversized varbind values used as label values.
const maxLabelValue = 200

// maxPayloadBytes caps the archived varbind JSON payload (~16KB).
const maxPayloadBytes = 16 * 1024

// SecretFunc resolves a secret reference to its plaintext for a tenant.
// It mirrors auth.SecretsResolver but returns an error so callers can
// distinguish "no secret box wired" from "secret missing"; nil is a
// valid value (v3 falls back to inline passphrases from Config).
type SecretFunc func(ctx context.Context, tenantID, name string) (string, error)

// Manager owns the trap listeners and the reconcile loop.
type Manager struct {
	Store  *storage.Store
	Bus    *eventbus.Bus
	Secret SecretFunc
	Log    *slog.Logger

	// Interval overrides reconcileInterval (tests use a short value).
	Interval time.Duration

	mu        sync.Mutex
	listeners map[string]*listener // listen addr → running listener

	// counters for self-metrics / debugging (no external dependency).
	received atomic64
	dropped  atomic64
	emitted  atomic64
}

// New builds a Manager. log may be nil (defaults to slog.Default()).
func New(store *storage.Store, bus *eventbus.Bus, secret SecretFunc, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		Store:     store,
		Bus:       bus,
		Secret:    secret,
		Log:       log,
		listeners: map[string]*listener{},
	}
}

// Run drives the reconcile loop until ctx is cancelled, then tears down
// every listener cleanly (SPEC §7.2 clean shutdown).
func (m *Manager) Run(ctx context.Context) {
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

// shutdown stops all listeners (called on ctx cancel).
func (m *Manager) shutdown() {
	m.mu.Lock()
	ls := m.listeners
	m.listeners = map[string]*listener{}
	m.mu.Unlock()
	for addr, l := range ls {
		l.stop()
		m.Log.Info("traps: listener stopped", "listen", addr)
	}
}

// boundSource pairs an EventSource with its tenant for matching.
type boundSource struct {
	tenantID string
	src      *model.EventSource
}

// reconcile loads the desired source set and makes the live listeners
// match it: start new listen addresses, stop vanished ones, and refresh
// the source list on the ones that persist (hot add/remove/port change).
func (m *Manager) reconcile(ctx context.Context) {
	desired, err := m.loadSources(ctx)
	if err != nil {
		m.Log.Error("traps: reconcile load failed", "err", err)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop listeners whose address no longer has any source.
	for addr, l := range m.listeners {
		if _, ok := desired[addr]; !ok {
			l.stop()
			delete(m.listeners, addr)
			m.Log.Info("traps: listener stopped", "listen", addr)
		}
	}

	// Start/refresh listeners for every desired address.
	for addr, sources := range desired {
		sig := v3Signature(sources)
		if l, ok := m.listeners[addr]; ok {
			if l.v3sig == sig {
				l.setSources(sources) // hot-swap the source set, same socket
				continue
			}
			// The v3 USM table is built once at start(); a changed v3 user
			// set needs a fresh socket. v2c-only changes don't move the
			// signature and stay hot-swapped above (no dropped traps).
			l.stop()
			delete(m.listeners, addr)
			m.Log.Info("traps: rebuilding listener for v3 credential change", "listen", addr)
		}
		l := newListener(m, addr)
		l.setSources(sources)
		l.v3sig = sig
		if err := l.start(); err != nil {
			m.Log.Error("traps: listener start failed", "listen", addr, "err", err)
			continue
		}
		m.listeners[addr] = l
		m.Log.Info("traps: listening for SNMP traps", "listen", addr, "sources", len(sources))
	}
}

// loadSources returns enabled snmp-trap sources grouped by listen address.
func (m *Manager) loadSources(ctx context.Context) (map[string][]boundSource, error) {
	tenants, err := m.Store.Tenants(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	out := map[string][]boundSource{}
	for _, t := range tenants {
		srcs, err := storage.LoadAll[model.EventSource](ctx, m.Store, t.ID, storage.KindEventSource)
		if err != nil {
			return nil, fmt.Errorf("load event-sources for tenant %s: %w", t.ID, err)
		}
		for _, s := range srcs {
			if s.Type != "snmp-trap" || !s.Enabled {
				continue
			}
			s = m.resolveV3Secrets(ctx, t.ID, s)
			addr := listenAddr(s)
			out[addr] = append(out[addr], boundSource{tenantID: t.ID, src: s})
		}
	}
	return out, nil
}

// resolveV3Secrets materialises v3AuthSecretRef/v3PrivSecretRef into the
// inline v3AuthPass/v3PrivPass keys the USM table is built from. The
// event-source dialog stores passphrases as secret references; an inline
// passphrase (More settings) wins so configs that used it as a workaround
// keep working. The returned source is a copy — the store's Config map is
// never mutated. Resolution happens here (not in usmFromConfig) so
// v3Signature also covers the resolved values and a rotated secret
// triggers the listener rebuild.
func (m *Manager) resolveV3Secrets(ctx context.Context, tenantID string, s *model.EventSource) *model.EventSource {
	authRef := strings.TrimSpace(s.Config["v3AuthSecretRef"])
	privRef := strings.TrimSpace(s.Config["v3PrivSecretRef"])
	if m.Secret == nil || strings.TrimSpace(s.Config["v3User"]) == "" || (authRef == "" && privRef == "") {
		return s
	}
	resolve := func(inlineKey, ref string) string {
		if ref == "" || s.Config[inlineKey] != "" { // inline passphrase wins
			return ""
		}
		v, err := m.Secret(ctx, tenantID, ref)
		if err != nil {
			m.Log.Warn("traps: v3 secret resolution failed; passphrase unset",
				"source", s.Name, "ref", ref, "err", err)
			return ""
		}
		return v
	}
	authPass := resolve("v3AuthPass", authRef)
	privPass := resolve("v3PrivPass", privRef)
	if authPass == "" && privPass == "" {
		return s
	}
	cp := *s
	cp.Config = make(map[string]string, len(s.Config)+2)
	for k, v := range s.Config {
		cp.Config[k] = v
	}
	if authPass != "" {
		cp.Config["v3AuthPass"] = authPass
	}
	if privPass != "" {
		cp.Config["v3PrivPass"] = privPass
	}
	return &cp
}

// listenAddr resolves the gosnmp listen string for a source.
func listenAddr(s *model.EventSource) string {
	if v := strings.TrimSpace(s.Config["listen"]); v != "" {
		return v
	}
	return "udp://:9162"
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

// classifyTrap maps the snmpTrapOID varbind to (title-stem, severity).
// Enterprise/unknown traps return ("", "") so the caller falls back to
// the source-configured default severity (SPEC §7.5 severity mapping).
//
// Severity vocabulary is the model.Severity set: critical | warning |
// info | ok. linkDown is critical; linkUp/coldStart/warmStart are ok
// (recovery/benign); authenticationFailure and egpNeighborLoss are
// warning.
func classifyTrap(trapOID string) (name string, sev model.Severity) {
	switch trapOID {
	case oidColdStart:
		return "coldStart", model.SevOK
	case oidWarmStart:
		return "warmStart", model.SevOK
	case oidLinkDown:
		return "linkDown", model.SevCritical
	case oidLinkUp:
		return "linkUp", model.SevOK
	case oidAuthFailure:
		return "authenticationFailure", model.SevWarning
	case oidEgpNeighbor:
		return "egpNeighborLoss", model.SevWarning
	default:
		return "", ""
	}
}

// trapOIDFromPacket extracts the snmpTrapOID.0 varbind value from a v2c/v3
// packet, or synthesises the OID for a v1 trap from its enterprise +
// generic/specific fields (RFC 2576 §3.1 coexistence mapping).
func trapOIDFromPacket(pkt *gosnmp.SnmpPacket) string {
	if pkt.Version == gosnmp.Version1 {
		return v1TrapOID(pkt)
	}
	for _, vb := range pkt.Variables {
		if normOID(vb.Name) == oidSnmpTrapOID {
			if s, ok := vb.Value.(string); ok {
				return normOID(s)
			}
			return normOID(fmt.Sprint(vb.Value))
		}
	}
	return ""
}

// v1TrapOID maps a v1 trap header to the equivalent v2c trap OID.
func v1TrapOID(pkt *gosnmp.SnmpPacket) string {
	switch pkt.GenericTrap {
	case 0:
		return oidColdStart
	case 1:
		return oidWarmStart
	case 2:
		return oidLinkDown
	case 3:
		return oidLinkUp
	case 4:
		return oidAuthFailure
	case 5:
		return oidEgpNeighbor
	default: // 6 = enterpriseSpecific: enterprise + 0 + specific
		ent := normOID(pkt.Enterprise)
		return ent + ".0." + fmt.Sprint(pkt.SpecificTrap)
	}
}

// normOID strips a single leading dot so OID comparisons are stable
// regardless of the leading-dot convention.
func normOID(oid string) string {
	return strings.TrimPrefix(oid, ".")
}

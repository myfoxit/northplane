// Package agi makes northplane a first-class citizen of an Asterisk/
// FreePBX deployment for INBOUND alarm calls (the outbound direction is
// the notify voice channel's asterisk provider, AMI Originate).
//
// The PBX terminates the SIP trunks (A1, Twilio Elastic, any carrier)
// and its dialplan hands answered calls to northplane over FastAGI —
// one line, no dialplan logic:
//
//	[alarm-line]
//	exten => s,1,AGI(agi://northplane-host:4573/<source-id-or-name>)
//	 same => n,Hangup()
//
// Northplane then drives the same configurable IVR menus (resource kind
// ivr-menu) as the cloud-webhook path: PIN gate, raise alarm (with
// voicemail), list open alarms, acknowledge, resolve. Fully on-prem —
// no cloud in the call path.
//
// Sources are EventSources of Type "asterisk-inbound". Config keys:
//
//	listen           FastAGI bind address (default "tcp://:4573")
//	menu             IVR menu name; empty = built-in (1 raise, 2 list, 3 ack)
//	language         phrase language for TTS mode ("de-DE"…, default en)
//	ttsApp           Asterisk application that speaks its argument
//	                 (e.g. "Flite", "ESpeak", or a Gosub wrapper around
//	                 piper). Empty = prompt-file mode: the operator
//	                 records/generates the np-* sound files listed in
//	                 docs/ALARMING.md and dynamic text is skipped.
//	escalationPolicy default policy for phone-raised alarms
//	severity         default severity (default critical)
//	allowFrom        comma-separated caller-id prefixes; empty = all
//	recordDir        PBX-side directory for voicemail recordings
//	                 (default /var/spool/asterisk/recording)
package agi

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/northplane/northplane/internal/api"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

const (
	defaultListen  = "tcp://:4573"
	reconcileEvery = 30 * time.Second
	maxConns       = 32
	sessionTimeout = 10 * time.Minute
)

// Manager reconciles one FastAGI listener per distinct listen address
// across all enabled asterisk-inbound event sources (mirrors the
// traps/espa managers).
type Manager struct {
	api *api.API
	log *slog.Logger

	mu        sync.Mutex
	listeners map[string]*listener // listen addr → listener
}

// New builds the manager. The API reference supplies alarm operations
// (raise/ack/resolve) so phone flows behave exactly like the HTTP ones.
func New(a *api.API, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{api: a, log: log, listeners: map[string]*listener{}}
}

// Run reconciles until ctx ends, then tears every listener down.
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(reconcileEvery)
	defer ticker.Stop()
	m.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for _, l := range m.listeners {
				l.close()
			}
			m.listeners = map[string]*listener{}
			m.mu.Unlock()
			return
		case <-ticker.C:
			m.reconcile(ctx)
		}
	}
}

func (m *Manager) reconcile(ctx context.Context) {
	tenants, err := m.api.Store.Tenants(ctx)
	if err != nil {
		m.log.Error("agi: tenants", "err", err)
		return
	}
	want := map[string][]*model.EventSource{} // listen addr → sources
	for _, t := range tenants {
		sources, err := storage.LoadAll[model.EventSource](ctx, m.api.Store, t.ID, storage.KindEventSource)
		if err != nil {
			continue
		}
		for _, src := range sources {
			if src.Type != "asterisk-inbound" || !src.Enabled {
				continue
			}
			addr := listenAddr(src.Config["listen"])
			want[addr] = append(want[addr], src)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for addr, l := range m.listeners {
		if _, ok := want[addr]; !ok {
			l.close()
			delete(m.listeners, addr)
			m.log.Info("agi: listener stopped", "addr", addr)
		}
	}
	for addr, sources := range want {
		if l, ok := m.listeners[addr]; ok {
			l.setSources(sources)
			continue
		}
		l, err := newListener(ctx, addr, sources, m.api, m.log)
		if err != nil {
			m.log.Error("agi: listen", "addr", addr, "err", err)
			continue
		}
		m.listeners[addr] = l
		m.log.Info("agi: listener started", "addr", addr, "sources", len(sources))
	}
}

func listenAddr(v string) string {
	if v == "" {
		v = defaultListen
	}
	return strings.TrimPrefix(v, "tcp://")
}

// listener accepts FastAGI connections for a set of sources.
type listener struct {
	ln  net.Listener
	api *api.API
	log *slog.Logger

	mu      sync.RWMutex
	sources []*model.EventSource

	conns  chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
}

func newListener(parent context.Context, addr string, sources []*model.EventSource,
	a *api.API, log *slog.Logger) (*listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	l := &listener{ln: ln, api: a, log: log, sources: sources,
		conns: make(chan struct{}, maxConns), ctx: ctx, cancel: cancel}
	go l.acceptLoop()
	return l, nil
}

func (l *listener) setSources(sources []*model.EventSource) {
	l.mu.Lock()
	l.sources = sources
	l.mu.Unlock()
}

func (l *listener) close() {
	l.cancel()
	_ = l.ln.Close()
}

func (l *listener) acceptLoop() {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			return // closed
		}
		select {
		case l.conns <- struct{}{}:
		default:
			_ = conn.Close() // connection cap reached
			continue
		}
		go func() {
			defer func() {
				<-l.conns
				_ = conn.Close()
				if r := recover(); r != nil {
					l.log.Error("agi: session panic", "panic", r)
				}
			}()
			_ = conn.SetDeadline(time.Now().Add(sessionTimeout))
			l.serve(conn)
		}()
	}
}

// serve runs one call: parse the AGI environment, route to the source,
// drive the IVR conversation.
func (l *listener) serve(conn net.Conn) {
	s, err := newSession(conn)
	if err != nil {
		l.log.Warn("agi: bad handshake", "err", err)
		return
	}
	src := l.route(s.env["agi_network_script"])
	if src == nil {
		l.log.Warn("agi: no source for script", "script", s.env["agi_network_script"])
		_ = s.verbose("northplane: unknown alarm line")
		return
	}
	caller := s.env["agi_callerid"]
	if !allowedCaller(src.Config["allowFrom"], caller) {
		l.log.Info("agi: caller denied", "caller", caller, "source", src.Name)
		_ = s.verbose("northplane: caller not allowed")
		return
	}
	conv := &conversation{
		s:    s,
		src:  src,
		acts: &apiActions{api: l.api},
		log:  l.log.With("source", src.Name, "caller", caller),
	}
	conv.run(l.ctx)
}

// route picks the source addressed by the FastAGI URL path
// (agi://host:port/<id-or-name>); a single-source listener accepts any.
func (l *listener) route(script string) *model.EventSource {
	l.mu.RLock()
	defer l.mu.RUnlock()
	script = strings.Trim(script, "/")
	for _, src := range l.sources {
		if script == src.ID || script == src.Name {
			return src
		}
	}
	if script == "" && len(l.sources) == 1 {
		return l.sources[0]
	}
	return nil
}

// allowedCaller mirrors the webhook path's prefix allowlist.
func allowedCaller(allow, from string) bool {
	allow = strings.TrimSpace(allow)
	if allow == "" {
		return true
	}
	from = normalizePhone(from)
	for _, p := range strings.Split(allow, ",") {
		if p = normalizePhone(p); p != "" && strings.HasPrefix(from, p) {
			return true
		}
	}
	return false
}

func normalizePhone(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '+' || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// actions is the alarm surface the conversation needs; api.API satisfies
// it via apiActions, tests use fakes.
type actions interface {
	Raise(ctx context.Context, tenant string, p api.RaiseParams) (*model.Alert, bool, error)
	Ack(ctx context.Context, tenant, alertID, by string) error
	Resolve(ctx context.Context, tenant, alertID, by string) error
	OpenAlerts(ctx context.Context, tenant string, limit int) []*model.Alert
	ContactNameByPhone(ctx context.Context, tenant, phone string) string
	Menu(ctx context.Context, tenant, name string) *model.IVRMenu
	AttachLabels(ctx context.Context, tenant, alertID string, labels model.Labels)
}

type apiActions struct{ api *api.API }

func (x *apiActions) Raise(ctx context.Context, tenant string, p api.RaiseParams) (*model.Alert, bool, error) {
	return x.api.RaiseAlert(ctx, tenant, p)
}

func (x *apiActions) Ack(ctx context.Context, tenant, alertID, by string) error {
	return x.api.AckAlertVia(ctx, tenant, alertID, by, "asterisk-agi")
}

func (x *apiActions) Resolve(ctx context.Context, tenant, alertID, by string) error {
	return x.api.ResolveAlertVia(ctx, tenant, alertID, by, "asterisk-agi")
}

func (x *apiActions) OpenAlerts(ctx context.Context, tenant string, limit int) []*model.Alert {
	alerts, err := x.api.Store.ListAlerts(ctx, storage.AlertFilter{
		TenantID: tenant, Status: []model.AlertStatus{model.AlertOpen}, Limit: limit})
	if err != nil {
		return nil
	}
	return alerts
}

func (x *apiActions) ContactNameByPhone(ctx context.Context, tenant, phone string) string {
	phone = normalizePhone(phone)
	if phone == "" {
		return ""
	}
	contacts, err := storage.LoadAll[model.Contact](ctx, x.api.Store, tenant, storage.KindContact)
	if err != nil {
		return ""
	}
	for _, c := range contacts {
		if normalizePhone(c.Phone) == phone {
			return c.Name
		}
	}
	return ""
}

func (x *apiActions) Menu(ctx context.Context, tenant, name string) *model.IVRMenu {
	if name != "" {
		if m, err := storage.LoadOne[model.IVRMenu](ctx, x.api.Store, tenant, storage.KindIVRMenu, name); err == nil {
			return m
		}
	}
	return model.DefaultIVRMenu()
}

func (x *apiActions) AttachLabels(ctx context.Context, tenant, alertID string, labels model.Labels) {
	_, _ = x.api.Store.MergeAlertLabels(ctx, tenant, alertID, labels)
}

// Stats reports listener/connection gauges for self-monitoring.
func (m *Manager) Stats() (listeners int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.listeners)
}

var _ = fmt.Sprintf // keep fmt for session.go helpers

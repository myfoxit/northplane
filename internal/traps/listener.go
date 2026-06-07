package traps

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/northplane/northplane/internal/model"
)

// listener wraps one gosnmp TrapListener bound to a single address. Its
// source set is hot-swappable so a reconcile can add/remove sources
// without touching the socket.
type listener struct {
	mgr  *Manager
	addr string

	tl   *gosnmp.TrapListener
	done chan struct{} // closed when the Listen goroutine returns

	mu      sync.RWMutex
	sources []boundSource // candidate sources sharing this address
}

func newListener(mgr *Manager, addr string) *listener {
	return &listener{mgr: mgr, addr: addr, done: make(chan struct{})}
}

// setSources atomically replaces the candidate source set.
func (l *listener) setSources(s []boundSource) {
	l.mu.Lock()
	l.sources = s
	l.mu.Unlock()
}

func (l *listener) snapshot() []boundSource {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]boundSource, len(l.sources))
	copy(out, l.sources)
	return out
}

// start opens the UDP socket and blocks (in a goroutine) in Listen until
// Close. It waits for the listener to signal readiness so reconcile sees
// a usable socket (and a bind error surfaces synchronously).
func (l *listener) start() error {
	tl := gosnmp.NewTrapListener()
	// A v3-capable Params with USM enabled: per-trap unmarshalling needs
	// matching SecurityParameters, which the OnNewTrap layer can't supply
	// after the fact for authPriv. We keep v2c the default and let the
	// handler reject mismatched community/user. Logger is the zero value
	// so gosnmp stays silent (its Print/Printf no-op on a nil logger).
	tl.Params = &gosnmp.GoSNMP{
		Version:   gosnmp.Version2c,
		Community: "", // community is validated per-source in the handler
		Logger:    gosnmp.Logger{},
	}
	tl.OnNewTrap = l.handle

	errCh := make(chan error, 1)
	go func() {
		defer close(l.done)
		if err := tl.Listen(l.addr); err != nil {
			errCh <- err
		}
	}()

	// Wait until listening or a bind error (bounded so a wedged socket
	// can't hang reconcile forever).
	select {
	case <-tl.Listening():
		l.tl = tl
		return nil
	case err := <-errCh:
		return fmt.Errorf("listen %s: %w", l.addr, err)
	case <-time.After(5 * time.Second):
		tl.Close()
		return fmt.Errorf("listen %s: timed out waiting for socket", l.addr)
	}
}

// stop closes the socket and waits for the Listen goroutine to exit.
func (l *listener) stop() {
	if l.tl != nil {
		l.tl.Close()
	}
	select {
	case <-l.done:
	case <-time.After(5 * time.Second): // never block shutdown indefinitely
	}
}

// handle is the gosnmp OnNewTrap callback. A panic here must not take the
// listener down (SPEC robustness): recover and log, never propagate.
func (l *listener) handle(pkt *gosnmp.SnmpPacket, u *net.UDPAddr) {
	defer func() {
		if r := recover(); r != nil {
			l.mgr.Log.Error("traps: handler panic recovered", "listen", l.addr, "panic", r)
		}
	}()
	if pkt == nil {
		return
	}
	l.mgr.received.add(1)

	agentIP := ""
	if u != nil {
		agentIP = u.IP.String()
	}

	bs, ok := l.match(pkt)
	if !ok {
		l.mgr.dropped.add(1)
		l.mgr.Log.Debug("traps: no matching source",
			"listen", l.addr, "agent", agentIP, "version", pkt.Version.String())
		return
	}
	src := bs.src

	if !allowRate(src.ID, src.RateLimit, src.Burst) {
		l.mgr.dropped.add(1)
		l.mgr.Log.Debug("traps: rate limited", "source", src.Name, "agent", agentIP)
		return
	}

	ev, err := l.mgr.normalize(bs, pkt, agentIP)
	if err != nil {
		l.mgr.dropped.add(1)
		l.mgr.Log.Debug("traps: normalise failed", "source", src.Name, "agent", agentIP, "err", err)
		return
	}

	if err := l.mgr.publish(bs.tenantID, ev); err != nil {
		l.mgr.Log.Error("traps: publish failed", "source", src.Name, "err", err)
		return
	}
	l.mgr.emitted.add(1)
	l.mgr.Log.Debug("traps: event published",
		"source", src.Name, "agent", agentIP, "severity", ev.Severity)
}

// match selects the source on this listener whose auth matches the trap.
// v1/v2c: community equals Config["community"] (default "public").
// v3: USM user equals Config["v3User"]. (gosnmp validates the v3 auth/priv
// cryptography during UnmarshalTrap before this point when Params carry the
// USM credentials; without a per-source USM table we match on user name and
// treat a successfully-unmarshalled v3 packet as authenticated.)
func (l *listener) match(pkt *gosnmp.SnmpPacket) (boundSource, bool) {
	sources := l.snapshot()
	switch pkt.Version {
	case gosnmp.Version3:
		user := usmUser(pkt)
		for _, bs := range sources {
			want := bs.src.Config["v3User"]
			if want != "" && want == user {
				return bs, true
			}
		}
	default: // Version1, Version2c
		for _, bs := range sources {
			community := bs.src.Config["community"]
			if community == "" {
				community = "public"
			}
			if pkt.Community == community {
				return bs, true
			}
		}
	}
	return boundSource{}, false
}

// usmUser extracts the USM user name from a v3 packet's security params.
func usmUser(pkt *gosnmp.SnmpPacket) string {
	if sp, ok := pkt.SecurityParameters.(*gosnmp.UsmSecurityParameters); ok && sp != nil {
		return sp.UserName
	}
	return ""
}

// publish inserts the event and fans it onto the bus, exactly as the
// webhook ingress path does (insert for the audit log, then PublishEvent
// for alerting + subscribers).
func (m *Manager) publish(tenantID string, ev *model.Event) error {
	ctx := context.Background()
	if err := m.Store.InsertEvents(ctx, []*model.Event{ev}); err != nil {
		return err
	}
	m.Bus.PublishEvent(ev)
	return nil
}

// normalize renders a trap into the canonical Event/NormEvent shape used
// by every ingress adapter (SPEC §7.5, mirrors api.publishNorm): a
// NormEvent JSON payload wrapped in a model.Event with Type=ingress.
func (m *Manager) normalize(bs boundSource, pkt *gosnmp.SnmpPacket, agentIP string) (*model.Event, error) {
	src := bs.src
	now := time.Now().UTC()

	trapOID := trapOIDFromPacket(pkt)
	name, sev := classifyTrap(trapOID)
	if sev == "" { // enterprise/unknown trap → source default
		sev = configSeverity(src)
	}

	// Title: human label including the trap name (or OID) and agent.
	title := trapLabel(name, trapOID)
	if agentIP != "" {
		title += " from " + agentIP
	}

	// Labels: source labels + source/agent/trapOid + up to N varbinds.
	labels := model.Labels{
		"source":  src.Name,
		"agent":   agentIP,
		"trapOid": trapOID,
	}
	added := 0
	for _, vb := range pkt.Variables {
		if added >= maxVarbindLabels {
			break
		}
		key := normOID(vb.Name)
		labels[key] = truncate(renderValue(vb), maxLabelValue)
		added++
	}
	labels = src.Labels.Merge(labels) // explicit trap fields win over source labels

	dedupKey := src.Name + "/" + agentIP + "/" + trapOID

	norm := model.NormEvent{
		Source:     src.ID,
		ReceivedAt: now,
		DedupKey:   dedupKey,
		Severity:   sev,
		Summary:    title,
		Labels:     labels,
	}
	norm.Payload = varbindPayload(pkt)

	raw, err := json.Marshal(&norm)
	if err != nil {
		return nil, fmt.Errorf("marshal normevent: %w", err)
	}
	return &model.Event{
		ID:       model.NewID(),
		TenantID: bs.tenantID,
		TS:       now,
		Type:     model.EventIngress,
		SourceID: src.ID,
		Severity: sev,
		Payload:  raw,
	}, nil
}

// trapLabel renders the human stem of the title: the trap name if known,
// else the OID, else a generic fallback.
func trapLabel(name, oid string) string {
	switch {
	case name != "":
		return "SNMP " + name
	case oid != "":
		return "SNMP trap " + oid
	default:
		return "SNMP trap"
	}
}

// configSeverity reads Config["severity"] (default "warning"), validating
// against the model severity vocabulary.
func configSeverity(src *model.EventSource) model.Severity {
	sev := model.Severity(strings.TrimSpace(src.Config["severity"]))
	if sev.Valid() {
		return sev
	}
	return model.SevWarning
}

// varbindPayload archives every varbind as JSON (OID → {type, value}),
// capped at maxPayloadBytes (SPEC §7.5: raw payload archived).
func varbindPayload(pkt *gosnmp.SnmpPacket) json.RawMessage {
	type vbJSON struct {
		OID   string `json:"oid"`
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	out := struct {
		Version   string   `json:"version"`
		TrapOID   string   `json:"trapOid"`
		Community string   `json:"community,omitempty"`
		Varbinds  []vbJSON `json:"varbinds"`
	}{
		Version:   pkt.Version.String(),
		TrapOID:   trapOIDFromPacket(pkt),
		Community: pkt.Community,
	}
	for _, vb := range pkt.Variables {
		out.Varbinds = append(out.Varbinds, vbJSON{
			OID:   normOID(vb.Name),
			Type:  vb.Type.String(),
			Value: renderValue(vb),
		})
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	if len(raw) > maxPayloadBytes {
		// Drop trailing varbinds until it fits rather than truncate JSON.
		for len(out.Varbinds) > 0 && len(raw) > maxPayloadBytes {
			out.Varbinds = out.Varbinds[:len(out.Varbinds)-1]
			raw, _ = json.Marshal(out)
		}
	}
	return raw
}

// renderValue stringifies an SNMP varbind value (OctetString is []byte;
// OIDs and most scalars stringify directly).
func renderValue(vb gosnmp.SnmpPDU) string {
	switch v := vb.Value.(type) {
	case nil:
		return ""
	case []byte:
		return string(v)
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

// truncate caps s at n runes-ish bytes with an ellipsis marker.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

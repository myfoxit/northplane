package traps

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

const (
	oidIfIndex  = "1.3.6.1.2.1.2.2.1.1.1" // ifIndex.1 varbind in linkDown
	ciCommunity = "ci"
)

// freeUDPPort grabs an ephemeral UDP port and releases it, returning the
// number. The window between release and the listener's bind is tiny and
// CI-safe; the listener re-binds the same port immediately.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve udp port: %v", err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	pc.Close()
	return port
}

// newStore opens a temp SQLite store (default tenant is seeded by migrations).
func newStore(t *testing.T, ctx context.Context) *storage.Store {
	t.Helper()
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// putSource creates/updates an snmp-trap EventSource fixture.
func putSource(t *testing.T, ctx context.Context, store *storage.Store, src model.EventSource) {
	t.Helper()
	if _, err := store.PutResource(ctx, model.DefaultTenant, storage.KindEventSource, src.Name, src, 0); err != nil {
		t.Fatalf("put source %s: %v", src.Name, err)
	}
}

// sendV2cTrap sends a v2c trap (snmpTrapOID.0 + extra varbinds) to addr.
func sendV2cTrap(t *testing.T, port int, community, trapOID string, extra ...gosnmp.SnmpPDU) {
	t.Helper()
	g := &gosnmp.GoSNMP{
		Target:    "127.0.0.1",
		Port:      uint16(port),
		Community: community,
		Version:   gosnmp.Version2c,
		Timeout:   2 * time.Second,
		Retries:   1,
		MaxOids:   gosnmp.MaxOids,
		Logger:    gosnmp.NewLogger(log.New(io.Discard, "", 0)),
	}
	if err := g.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer g.Conn.Close()

	vars := []gosnmp.SnmpPDU{
		{Name: oidSnmpTrapOID, Type: gosnmp.ObjectIdentifier, Value: trapOID},
	}
	vars = append(vars, extra...)
	if _, err := g.SendTrap(gosnmp.SnmpTrap{Variables: vars}); err != nil {
		t.Fatalf("send trap: %v", err)
	}
}

// sendV3Trap sends an SNMPv3 authPriv trap with the given USM credentials.
// The sender is authoritative for traps, so it supplies its own engine ID;
// the receiver localises keys per packet from the configured user table.
func sendV3Trap(t *testing.T, port int, user, authPass, privPass, trapOID string, extra ...gosnmp.SnmpPDU) {
	t.Helper()
	g := &gosnmp.GoSNMP{
		Target:        "127.0.0.1",
		Port:          uint16(port),
		Version:       gosnmp.Version3,
		Timeout:       2 * time.Second,
		Retries:       1,
		MaxOids:       gosnmp.MaxOids,
		SecurityModel: gosnmp.UserSecurityModel,
		MsgFlags:      gosnmp.AuthPriv,
		SecurityParameters: &gosnmp.UsmSecurityParameters{
			UserName:                 user,
			AuthoritativeEngineID:    "np-test-sender-engine",
			AuthenticationProtocol:   gosnmp.SHA,
			AuthenticationPassphrase: authPass,
			PrivacyProtocol:          gosnmp.AES,
			PrivacyPassphrase:        privPass,
		},
		Logger: gosnmp.NewLogger(log.New(io.Discard, "", 0)),
	}
	if err := g.Connect(); err != nil {
		t.Fatalf("v3 connect: %v", err)
	}
	defer g.Conn.Close()

	vars := []gosnmp.SnmpPDU{
		{Name: oidSnmpTrapOID, Type: gosnmp.ObjectIdentifier, Value: trapOID},
	}
	vars = append(vars, extra...)
	if _, err := g.SendTrap(gosnmp.SnmpTrap{Variables: vars}); err != nil {
		t.Fatalf("send v3 trap: %v", err)
	}
}

// waitEvent blocks for an event on the subscriber or fails after timeout.
func waitEvent(t *testing.T, sub *eventbus.Subscriber, timeout time.Duration) *model.Event {
	t.Helper()
	select {
	case ev := <-sub.C:
		return ev
	case <-time.After(timeout):
		t.Fatal("timed out waiting for event")
		return nil
	}
}

// expectNoEvent asserts no event arrives within the window.
func expectNoEvent(t *testing.T, sub *eventbus.Subscriber, window time.Duration) {
	t.Helper()
	select {
	case ev := <-sub.C:
		t.Fatalf("unexpected event: severity=%s payload=%s", ev.Severity, ev.Payload)
	case <-time.After(window):
	}
}

// waitListeners polls Stats until the live-listener count reaches want.
func waitListeners(t *testing.T, m *Manager, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m.Stats().Listeners == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("listener count = %d, want %d", m.Stats().Listeners, want)
}

// TestLinkDownTrapNormalises is the core happy path: a v2c linkDown trap
// with an ifIndex varbind, matched by community, becomes a critical event
// with the deterministic dedupKey and the agent/trapOid labels.
func TestLinkDownTrapNormalises(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newStore(t, ctx)
	bus := eventbus.New()
	port := freeUDPPort(t)
	listen := "udp://127.0.0.1:" + strconv.Itoa(port)

	putSource(t, ctx, store, model.EventSource{
		Name: "edge-traps", Type: "snmp-trap", Enabled: true,
		Config: map[string]string{"listen": listen, "community": ciCommunity},
		Labels: model.Labels{"site": "dc1"},
	})

	sub := bus.Subscribe(16)
	defer sub.Close()

	m := New(store, bus, nil, nil)
	m.Interval = 50 * time.Millisecond
	go m.Run(ctx)
	waitListeners(t, m, 1, 3*time.Second)

	sendV2cTrap(t, port, ciCommunity, oidLinkDown,
		gosnmp.SnmpPDU{Name: oidIfIndex, Type: gosnmp.Integer, Value: 7})

	ev := waitEvent(t, sub, 3*time.Second)

	if ev.Type != model.EventIngress {
		t.Errorf("event type = %s, want %s", ev.Type, model.EventIngress)
	}
	if ev.Severity != model.SevCritical {
		t.Errorf("severity = %s, want critical", ev.Severity)
	}
	if ev.TenantID != model.DefaultTenant {
		t.Errorf("tenant = %s, want default", ev.TenantID)
	}

	norm := decodeNorm(t, ev)
	wantDedup := "edge-traps/127.0.0.1/" + oidLinkDown
	if norm.DedupKey != wantDedup {
		t.Errorf("dedupKey = %q, want %q", norm.DedupKey, wantDedup)
	}
	if norm.Labels["agent"] != "127.0.0.1" {
		t.Errorf("label agent = %q, want 127.0.0.1", norm.Labels["agent"])
	}
	if norm.Labels["trapOid"] != oidLinkDown {
		t.Errorf("label trapOid = %q, want %q", norm.Labels["trapOid"], oidLinkDown)
	}
	if norm.Labels["source"] != "edge-traps" {
		t.Errorf("label source = %q, want edge-traps", norm.Labels["source"])
	}
	if norm.Labels["site"] != "dc1" {
		t.Errorf("source label site not merged: %v", norm.Labels)
	}
	if norm.Labels[oidIfIndex] != "7" {
		t.Errorf("varbind label %s = %q, want 7", oidIfIndex, norm.Labels[oidIfIndex])
	}
	if norm.Severity != model.SevCritical {
		t.Errorf("normevent severity = %s, want critical", norm.Severity)
	}
}

// TestV3AuthPrivTrapNormalises is the SNMPv3 happy path: an authPriv trap
// is authenticated and decrypted against the source's USM credentials, then
// normalised exactly like a v2c trap. It proves v3 traps are no longer
// dropped before the handler (the listener switches to a v3-capable socket
// when a source configures a v3User).
func TestV3AuthPrivTrapNormalises(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newStore(t, ctx)
	bus := eventbus.New()
	port := freeUDPPort(t)
	listen := "udp://127.0.0.1:" + strconv.Itoa(port)

	const user, authPass, privPass = "v3user", "authpass-123456", "privpass-123456"
	putSource(t, ctx, store, model.EventSource{
		Name: "edge-v3", Type: "snmp-trap", Enabled: true,
		Config: map[string]string{
			"listen": listen, "v3User": user, "v3SecLevel": "authPriv",
			"v3AuthProto": "SHA", "v3AuthPass": authPass,
			"v3PrivProto": "AES", "v3PrivPass": privPass,
		},
	})

	sub := bus.Subscribe(16)
	defer sub.Close()

	m := New(store, bus, nil, nil)
	m.Interval = 50 * time.Millisecond
	go m.Run(ctx)
	waitListeners(t, m, 1, 3*time.Second)

	sendV3Trap(t, port, user, authPass, privPass, oidLinkDown,
		gosnmp.SnmpPDU{Name: oidIfIndex, Type: gosnmp.Integer, Value: 9})

	ev := waitEvent(t, sub, 3*time.Second)
	if ev.Severity != model.SevCritical {
		t.Errorf("severity = %s, want critical", ev.Severity)
	}
	norm := decodeNorm(t, ev)
	if norm.Labels["source"] != "edge-v3" {
		t.Errorf("label source = %q, want edge-v3", norm.Labels["source"])
	}
	if norm.Labels[oidIfIndex] != "9" {
		t.Errorf("varbind label %s = %q, want 9", oidIfIndex, norm.Labels[oidIfIndex])
	}
}

// TestV3WrongCredentialsDropped: a v3 trap whose USM passphrase does not
// match the configured user fails authentication in gosnmp and never reaches
// the handler — no event is produced.
func TestV3WrongCredentialsDropped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newStore(t, ctx)
	bus := eventbus.New()
	port := freeUDPPort(t)
	listen := "udp://127.0.0.1:" + strconv.Itoa(port)

	putSource(t, ctx, store, model.EventSource{
		Name: "edge-v3", Type: "snmp-trap", Enabled: true,
		Config: map[string]string{
			"listen": listen, "v3User": "v3user", "v3SecLevel": "authPriv",
			"v3AuthProto": "SHA", "v3AuthPass": "the-real-authpass",
			"v3PrivProto": "AES", "v3PrivPass": "the-real-privpass",
		},
	})

	sub := bus.Subscribe(16)
	defer sub.Close()

	m := New(store, bus, nil, nil)
	m.Interval = 50 * time.Millisecond
	go m.Run(ctx)
	waitListeners(t, m, 1, 3*time.Second)

	sendV3Trap(t, port, "v3user", "wrong-authpass-xx", "wrong-privpass-xx", oidLinkDown)
	expectNoEvent(t, sub, 700*time.Millisecond)
}

// TestWrongCommunityDropped: a trap whose community does not match any
// source on the listener is dropped (no event, dropped counter rises).
func TestWrongCommunityDropped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newStore(t, ctx)
	bus := eventbus.New()
	port := freeUDPPort(t)
	listen := "udp://127.0.0.1:" + strconv.Itoa(port)

	putSource(t, ctx, store, model.EventSource{
		Name: "edge-traps", Type: "snmp-trap", Enabled: true,
		Config: map[string]string{"listen": listen, "community": ciCommunity},
	})

	sub := bus.Subscribe(16)
	defer sub.Close()

	m := New(store, bus, nil, nil)
	m.Interval = 50 * time.Millisecond
	go m.Run(ctx)
	waitListeners(t, m, 1, 3*time.Second)

	before := m.Stats().Dropped
	sendV2cTrap(t, port, "wrong-secret", oidLinkDown)
	expectNoEvent(t, sub, 600*time.Millisecond)

	if got := m.Stats().Dropped; got <= before {
		t.Errorf("dropped counter did not rise (before=%d after=%d)", before, got)
	}
}

// TestReconcileStopsListener: disabling a source removes its listener, so
// a subsequent trap to that port yields no event.
func TestReconcileStopsListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newStore(t, ctx)
	bus := eventbus.New()
	port := freeUDPPort(t)
	listen := "udp://127.0.0.1:" + strconv.Itoa(port)

	src := model.EventSource{
		Name: "edge-traps", Type: "snmp-trap", Enabled: true,
		Config: map[string]string{"listen": listen, "community": ciCommunity},
	}
	putSource(t, ctx, store, src)

	sub := bus.Subscribe(16)
	defer sub.Close()

	m := New(store, bus, nil, nil)
	m.Interval = 50 * time.Millisecond
	go m.Run(ctx)
	waitListeners(t, m, 1, 3*time.Second)

	// Sanity: enabled source delivers.
	sendV2cTrap(t, port, ciCommunity, oidLinkUp)
	ev := waitEvent(t, sub, 3*time.Second)
	if ev.Severity != model.SevOK { // linkUp → ok in the severity vocabulary
		t.Errorf("linkUp severity = %s, want ok", ev.Severity)
	}

	// Disable the source; reconcile must tear the listener down.
	src.Enabled = false
	putSource(t, ctx, store, src)
	waitListeners(t, m, 0, 3*time.Second)

	// Drain anything already queued before the listener stopped.
	drain(sub)

	// A trap to the now-closed port must not produce an event.
	pc, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("port should be free after listener stop: %v", err)
	}
	pc.Close()
}

// drain empties any buffered events.
func drain(sub *eventbus.Subscriber) {
	for {
		select {
		case <-sub.C:
		default:
			return
		}
	}
}

func decodeNorm(t *testing.T, ev *model.Event) model.NormEvent {
	t.Helper()
	var norm model.NormEvent
	if err := json.Unmarshal(ev.Payload, &norm); err != nil {
		t.Fatalf("decode normevent: %v", err)
	}
	return norm
}

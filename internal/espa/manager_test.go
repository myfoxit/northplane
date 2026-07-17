package espa

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
)

func TestListenAddrForDefaults(t *testing.T) {
	espaSrc := &model.EventSource{Type: typeESPA}
	if got := listenAddrFor(espaSrc); got != "tcp://:2023" {
		t.Errorf("espa default = %q", got)
	}
	espaxSrc := &model.EventSource{Type: typeESPAX}
	if got := listenAddrFor(espaxSrc); got != "tcp://:8123" {
		t.Errorf("espa-x default = %q", got)
	}
	custom := &model.EventSource{Type: typeESPA, Config: map[string]string{"listen": "tcp://:9999"}}
	if got := listenAddrFor(custom); got != "tcp://:9999" {
		t.Errorf("custom = %q", got)
	}
}

func TestHostPort(t *testing.T) {
	cases := map[string]string{
		"tcp://:2023":          ":2023",
		" tcp://0.0.0.0:8123 ": "0.0.0.0:8123",
		"127.0.0.1:7":          "127.0.0.1:7", // bare host:port accepted
	}
	for in, want := range cases {
		if got := hostPort(in); got != want {
			t.Errorf("hostPort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGroupByListenFirstWins(t *testing.T) {
	a := boundSource{tenantID: "t1", src: &model.EventSource{ID: "a", Name: "first", Type: typeESPA,
		Config: map[string]string{"listen": "tcp://:7000"}}}
	b := boundSource{tenantID: "t2", src: &model.EventSource{ID: "b", Name: "second", Type: typeESPAX,
		Config: map[string]string{"listen": "tcp://:7000"}}}
	c := boundSource{tenantID: "t1", src: &model.EventSource{ID: "c", Name: "other", Type: typeESPAX}}

	got := groupByListen([]boundSource{a, b, c}, testLogger())
	if len(got) != 2 {
		t.Fatalf("groups = %d, want 2", len(got))
	}
	if got["tcp://:7000"].src.ID != "a" {
		t.Errorf("duplicate address kept %q, want first source a", got["tcp://:7000"].src.ID)
	}
	if got["tcp://:8123"].src.ID != "c" {
		t.Errorf("default espa-x address missing: %+v", got)
	}
}

func TestConfigSeverity(t *testing.T) {
	src := &model.EventSource{}
	if got := configSeverity(src); got != model.SevInfo {
		t.Errorf("default = %q, want info", got)
	}
	src.Config = map[string]string{"severity": "critical"}
	if got := configSeverity(src); got != model.SevCritical {
		t.Errorf("configured = %q, want critical", got)
	}
	src.Config["severity"] = "bogus"
	if got := configSeverity(src); got != model.SevInfo {
		t.Errorf("invalid config = %q, want info fallback", got)
	}
}

func TestAllowRateBurstExhaustion(t *testing.T) {
	// Tiny refill so the burst dominates within the test's lifetime.
	id := "test-rate-" + time.Now().Format("150405.000000000")
	if !allowRate(id, 0.001, 2) || !allowRate(id, 0.001, 2) {
		t.Fatal("burst of 2 should admit two calls")
	}
	if allowRate(id, 0.001, 2) {
		t.Fatal("third call should be rate limited")
	}
}

type capture struct {
	bs   boundSource
	norm *model.NormEvent
}

// TestListenerEndToEndTCP drives a real localhost listener: ESPA 4.4.4
// poll + call frame, then a hot-swap to an ESPA-X source on the same
// socket, then shutdown.
func TestListenerEndToEndTCP(t *testing.T) {
	events := make(chan capture, 8)
	l := newListener(testLogger(), "tcp://127.0.0.1:0", "127.0.0.1:0",
		func(bs boundSource, n *model.NormEvent) { events <- capture{bs, n} })
	l.setSource(boundSource{tenantID: "t1", src: &model.EventSource{
		ID: "src-espa", Name: "pager-bridge", Type: typeESPA, Enabled: true,
	}})
	if err := l.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer l.stop()
	addr := l.ln.Addr().String()

	// --- ESPA 4.4.4 session over real TCP ---
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	mustWrite(t, c, []byte{enq})
	if b := readByte(t, c); b != ack {
		t.Fatalf("ENQ reply = %#x, want ACK", b)
	}
	mustWrite(t, c, buildESPAFrame("1", [][2]string{{"1", "0815"}, {"2", "Feuer"}, {"6", "1"}}))
	if b := readByte(t, c); b != ack {
		t.Fatalf("frame reply = %#x, want ACK", b)
	}
	var got capture
	select {
	case got = <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for captured event")
	}
	if got.bs.src.ID != "src-espa" || got.bs.tenantID != "t1" {
		t.Errorf("event bound to %s/%s, want t1/src-espa", got.bs.tenantID, got.bs.src.ID)
	}
	if got.norm.Summary != "Feuer" || got.norm.Labels["espa.address"] != "0815" {
		t.Errorf("event = %+v", got.norm)
	}
	_ = c.Close()

	// --- hot-swap the same socket to an ESPA-X source ---
	l.setSource(boundSource{tenantID: "t2", src: &model.EventSource{
		ID: "src-espax", Name: "nurse-call", Type: typeESPAX, Enabled: true,
	}})
	c2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial after swap: %v", err)
	}
	call := `<ESPA-X version="2.0" timestamp="2026-07-17T10:00:00Z">` +
		`<REQ.P-CALL><CP-CALL><CALL-ID>1</CALL-ID><ADDRESS>7</ADDRESS></CP-CALL></REQ.P-CALL></ESPA-X>`
	mustWrite(t, c2, frameDoc(call))
	_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	rep, framed, err := readESPAXDoc(bufio.NewReader(c2))
	if err != nil {
		t.Fatalf("read espa-x reply: %v", err)
	}
	if !framed || !strings.Contains(string(rep), `<REP.P-CALL state="ok">`) {
		t.Errorf("espa-x reply = %q framed=%v", rep, framed)
	}
	select {
	case got = <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for espa-x event")
	}
	if got.bs.src.ID != "src-espax" || got.norm.DedupKey != "espa-x/1" {
		t.Errorf("swapped event = %s / %+v", got.bs.src.ID, got.norm)
	}
	_ = c2.Close()

	// --- shutdown closes the socket ---
	l.stop()
	if _, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		t.Error("dial succeeded after stop; listener socket still open")
	}
}

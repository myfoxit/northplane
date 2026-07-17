package espa

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// buildESPAFrame assembles SOH <header> STX <records> ETX <BCC> with the
// records joined by RS and split key/value by US.
func buildESPAFrame(header string, records [][2]string) []byte {
	body := []byte(header)
	body = append(body, stx)
	for i, r := range records {
		if i > 0 {
			body = append(body, rs)
		}
		body = append(body, r[0]...)
		body = append(body, us)
		body = append(body, r[1]...)
	}
	body = append(body, etx)
	frame := append([]byte{soh}, body...)
	return append(frame, xorBCC(body))
}

// espaSession runs serveESPA on one end of a pipe and hands the test the
// other end plus the captured events.
type espaSession struct {
	client net.Conn
	events chan *model.NormEvent
}

func startESPASession(t *testing.T, defSev model.Severity) *espaSession {
	t.Helper()
	client, server := net.Pipe()
	s := &espaSession{client: client, events: make(chan *model.NormEvent, 8)}
	go func() {
		defer server.Close()
		_ = serveESPA(server, defSev, func(n *model.NormEvent) { s.events <- n }, testLogger())
	}()
	t.Cleanup(func() { _ = client.Close() })
	return s
}

func mustWrite(t *testing.T, c net.Conn, b []byte) {
	t.Helper()
	_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write(b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readByte(t *testing.T, c net.Conn) byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	var b [1]byte
	if _, err := io.ReadFull(c, b[:]); err != nil {
		t.Fatalf("read control byte: %v", err)
	}
	return b[0]
}

func waitEvent(t *testing.T, ch chan *model.NormEvent) *model.NormEvent {
	t.Helper()
	select {
	case n := <-ch:
		return n
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return nil
	}
}

func noEvent(t *testing.T, ch chan *model.NormEvent) {
	t.Helper()
	select {
	case n := <-ch:
		t.Fatalf("unexpected event emitted: %+v", n)
	default:
	}
}

// TestESPA444BCCHandComputed pins the block check character to a frame
// computed by hand: SOH '1' STX '1' US 'A' ETX with
// BCC = 0x31^0x02^0x31^0x1F^0x41^0x03 = 0x5F.
func TestESPA444BCCHandComputed(t *testing.T) {
	frame := buildESPAFrame("1", [][2]string{{"1", "A"}})
	want := []byte{0x01, 0x31, 0x02, 0x31, 0x1F, 0x41, 0x03, 0x5F}
	if string(frame) != string(want) {
		t.Fatalf("frame = % X, want % X", frame, want)
	}
	if got := xorBCC(frame[1 : len(frame)-1]); got != 0x5F {
		t.Fatalf("xorBCC = %#x, want 0x5f", got)
	}
}

func TestESPA444CallFrame(t *testing.T) {
	s := startESPASession(t, model.SevInfo)
	frame := buildESPAFrame("1", [][2]string{
		{"1", "0815"},
		{"2", "Feuer Zimmer 12"},
		{"3", "2"}, // beep coding
		{"4", "3"}, // call type standard
		{"5", "1"}, // transmissions
		{"6", "1"}, // priority alarm
	})
	mustWrite(t, s.client, frame)
	if b := readByte(t, s.client); b != ack {
		t.Fatalf("reply = %#x, want ACK", b)
	}

	n := waitEvent(t, s.events)
	if n.Summary != "Feuer Zimmer 12" {
		t.Errorf("summary = %q", n.Summary)
	}
	if n.Severity != model.SevCritical {
		t.Errorf("severity = %q, want critical", n.Severity)
	}
	if n.DedupKey != "" {
		t.Errorf("dedupKey = %q, want empty (every call is a fresh event)", n.DedupKey)
	}
	wantLabels := map[string]string{
		"espa.address": "0815", "espa.beep": "2", "espa.callType": "3", "espa.priority": "1",
	}
	for k, v := range wantLabels {
		if n.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, n.Labels[k], v)
		}
	}
	var payload struct {
		Function string            `json:"function"`
		Records  map[string]string `json:"records"`
	}
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.Function != "1" || payload.Records["5"] != "1" {
		t.Errorf("payload = %+v", payload)
	}
}

func TestESPA444CorruptBCCGetsNAK(t *testing.T) {
	s := startESPASession(t, model.SevInfo)
	frame := buildESPAFrame("1", [][2]string{{"1", "0815"}, {"2", "Feuer"}})
	frame[len(frame)-1] ^= 0xFF // corrupt the BCC
	mustWrite(t, s.client, frame)
	if b := readByte(t, s.client); b != nak {
		t.Fatalf("reply = %#x, want NAK", b)
	}
	noEvent(t, s.events)
}

func TestESPA444ENQPolling(t *testing.T) {
	s := startESPASession(t, model.SevInfo)

	// Bare ENQ poll → ACK (ready).
	mustWrite(t, s.client, []byte{enq})
	if b := readByte(t, s.client); b != ack {
		t.Fatalf("bare ENQ reply = %#x, want ACK", b)
	}

	// Addressed selection '1' ENQ → ACK as well (address char ignored).
	mustWrite(t, s.client, []byte{'1', enq})
	if b := readByte(t, s.client); b != ack {
		t.Fatalf("'1' ENQ reply = %#x, want ACK", b)
	}
	noEvent(t, s.events)
}

func TestESPA444TwoFramesOneConnection(t *testing.T) {
	s := startESPASession(t, model.SevInfo)

	mustWrite(t, s.client, buildESPAFrame("1", [][2]string{{"1", "0815"}, {"6", "1"}}))
	if b := readByte(t, s.client); b != ack {
		t.Fatalf("frame 1 reply = %#x, want ACK", b)
	}
	first := waitEvent(t, s.events)

	mustWrite(t, s.client, []byte{eot}) // transaction end between frames

	mustWrite(t, s.client, buildESPAFrame("1", [][2]string{{"1", "4711"}, {"2", "Test"}, {"6", "2"}}))
	if b := readByte(t, s.client); b != ack {
		t.Fatalf("frame 2 reply = %#x, want ACK", b)
	}
	second := waitEvent(t, s.events)

	if first.Labels["espa.address"] != "0815" || first.Severity != model.SevCritical {
		t.Errorf("first event = %+v", first)
	}
	if first.Summary != "ESPA call 0815" {
		t.Errorf("first summary = %q, want default", first.Summary)
	}
	if second.Labels["espa.address"] != "4711" || second.Severity != model.SevWarning || second.Summary != "Test" {
		t.Errorf("second event = %+v", second)
	}
}

// TestESPA444NonCallFunctionACKedNotEmitted: a checksum-valid status
// block (function '2') is acknowledged but produces no event; the
// following call frame still does.
func TestESPA444NonCallFunctionACKedNotEmitted(t *testing.T) {
	s := startESPASession(t, model.SevInfo)

	mustWrite(t, s.client, buildESPAFrame("2", [][2]string{{"1", "0815"}}))
	if b := readByte(t, s.client); b != ack {
		t.Fatalf("status block reply = %#x, want ACK", b)
	}

	mustWrite(t, s.client, buildESPAFrame("1", [][2]string{{"1", "0815"}}))
	if b := readByte(t, s.client); b != ack {
		t.Fatalf("call block reply = %#x, want ACK", b)
	}
	n := waitEvent(t, s.events)
	if n.Labels["espa.address"] != "0815" {
		t.Errorf("event = %+v", n)
	}
	noEvent(t, s.events) // exactly one event: the status block emitted none
}

// TestESPA444NormalPriorityUsesConfigDefault: priority 3 (normal) falls
// back to the source-configured severity.
func TestESPA444NormalPriorityUsesConfigDefault(t *testing.T) {
	s := startESPASession(t, model.SevWarning)
	mustWrite(t, s.client, buildESPAFrame("1", [][2]string{{"1", "7"}, {"6", "3"}}))
	if b := readByte(t, s.client); b != ack {
		t.Fatalf("reply = %#x, want ACK", b)
	}
	if n := waitEvent(t, s.events); n.Severity != model.SevWarning {
		t.Errorf("severity = %q, want configured default warning", n.Severity)
	}
}

// TestESPA444Latin1 checks the ISO 8859-1 → UTF-8 conversion: byte 0xE4
// is "ä".
func TestESPA444Latin1(t *testing.T) {
	if got := latin1String([]byte{0xE4}); got != "ä" {
		t.Fatalf("latin1String(0xE4) = %q, want ä", got)
	}

	s := startESPASession(t, model.SevInfo)
	mustWrite(t, s.client, buildESPAFrame("1", [][2]string{{"1", "1"}, {"2", "B\xe4r in Zimmer 3"}}))
	if b := readByte(t, s.client); b != ack {
		t.Fatalf("reply = %#x, want ACK", b)
	}
	if n := waitEvent(t, s.events); n.Summary != "Bär in Zimmer 3" {
		t.Errorf("summary = %q, want %q", n.Summary, "Bär in Zimmer 3")
	}
}

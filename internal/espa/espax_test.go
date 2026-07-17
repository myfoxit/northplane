package espa

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// espaxSession runs serveESPAX on one end of a pipe.
type espaxSession struct {
	client net.Conn
	reader *bufio.Reader // persistent: replies may arrive in one stream
	events chan *model.NormEvent
}

func startESPAXSession(t *testing.T, defSev model.Severity) *espaxSession {
	t.Helper()
	client, server := net.Pipe()
	s := &espaxSession{
		client: client,
		reader: bufio.NewReader(client),
		events: make(chan *model.NormEvent, 8),
	}
	go func() {
		defer server.Close()
		_ = serveESPAX(server, defSev, func(n *model.NormEvent) { s.events <- n }, testLogger())
	}()
	t.Cleanup(func() { _ = client.Close() })
	return s
}

// readReply scans the next reply document off the wire using the same
// scanner the server uses (STX/ETX framed or bare up to </ESPA-X>).
func (s *espaxSession) readReply(t *testing.T) (doc string, framed bool) {
	t.Helper()
	_ = s.client.SetReadDeadline(time.Now().Add(2 * time.Second))
	raw, framed, err := readESPAXDoc(s.reader)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	return string(raw), framed
}

func frameDoc(doc string) []byte {
	return append(append([]byte{stx}, doc...), etx)
}

func TestESPAXLoginAndHeartbeat(t *testing.T) {
	s := startESPAXSession(t, model.SevInfo)

	login := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<ESPA-X version="2.0" timestamp="2026-07-17T10:00:00Z">` +
		`<REQ.LOGIN><LOGIN-USER>nurse-call</LOGIN-USER><LOGIN-PASSWORD>x</LOGIN-PASSWORD></REQ.LOGIN>` +
		`</ESPA-X>`
	mustWrite(t, s.client, frameDoc(login))
	rep, framed := s.readReply(t)
	if !framed {
		t.Error("login reply not STX/ETX framed although request was")
	}
	if !strings.Contains(rep, `<REP.LOGIN state="ok"/>`) {
		t.Errorf("login reply = %q, want REP.LOGIN state ok", rep)
	}
	if !strings.Contains(rep, `<ESPA-X version="2.0" timestamp="`) {
		t.Errorf("login reply missing ESPA-X envelope: %q", rep)
	}

	hb := `<ESPA-X version="2.0" timestamp="2026-07-17T10:00:30Z"><REQ.HEARTBEAT/></ESPA-X>`
	mustWrite(t, s.client, frameDoc(hb))
	rep, _ = s.readReply(t)
	if !strings.Contains(rep, `<REP.HEARTBEAT state="ok"/>`) {
		t.Errorf("heartbeat reply = %q, want REP.HEARTBEAT state ok", rep)
	}
	noEvent(t, s.events) // neither login nor heartbeat emits
}

func TestESPAXPCall(t *testing.T) {
	s := startESPAXSession(t, model.SevInfo)

	call := `<ESPA-X version="2.0" timestamp="2026-07-17T10:01:00Z">` +
		`<REQ.P-CALL seq="7"><CP-CALL>` +
		`<CALL-ID>4711</CALL-ID>` +
		`<CALL-ADDRESS>123</CALL-ADDRESS>` +
		`<DISPLAY-MSG>Sturzalarm Zimmer 7</DISPLAY-MSG>` +
		`<CALL-PRIO>alarm</CALL-PRIO>` +
		`<SIGNAL-TYP>standard</SIGNAL-TYP>` +
		`</CP-CALL></REQ.P-CALL></ESPA-X>`
	mustWrite(t, s.client, frameDoc(call))

	rep, framed := s.readReply(t)
	if !framed {
		t.Error("P-CALL reply not framed although request was")
	}
	if !strings.Contains(rep, `<REP.P-CALL state="ok">`) || !strings.Contains(rep, `<CALL-ID>4711</CALL-ID>`) {
		t.Errorf("P-CALL reply = %q, want state ok echoing CALL-ID", rep)
	}

	n := waitEvent(t, s.events)
	if n.Summary != "Sturzalarm Zimmer 7" {
		t.Errorf("summary = %q", n.Summary)
	}
	if n.DedupKey != "espa-x/4711" {
		t.Errorf("dedupKey = %q, want espa-x/4711", n.DedupKey)
	}
	if n.Severity != model.SevCritical {
		t.Errorf("severity = %q, want critical", n.Severity)
	}
	wantLabels := map[string]string{
		"espa.callId": "4711", "espa.address": "123", "espa.prio": "alarm", "espa.signal": "standard",
	}
	for k, v := range wantLabels {
		if n.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, n.Labels[k], v)
		}
	}
}

// TestESPAXBareXMLAndTagVariants: unframed document, ADDRESS/TEXT-MSG
// vendor variant, numeric priority "2" → warning; the reply comes back
// bare as well and without a CALL-ID echo.
func TestESPAXBareXMLAndTagVariants(t *testing.T) {
	s := startESPAXSession(t, model.SevInfo)

	call := `<ESPA-X version="2.0" timestamp="2026-07-17T10:02:00Z">` +
		`<REQ.P-CALL><CP-CALL>` +
		`<ADDRESS>99</ADDRESS>` +
		`<TEXT-MSG>Rufanforderung</TEXT-MSG>` +
		`<CALL-PRIO>2</CALL-PRIO>` +
		`</CP-CALL></REQ.P-CALL></ESPA-X>`
	mustWrite(t, s.client, []byte(call)) // bare, no STX/ETX

	rep, framed := s.readReply(t)
	if framed {
		t.Error("reply framed although request was bare")
	}
	if !strings.Contains(rep, `<REP.P-CALL state="ok"/>`) {
		t.Errorf("reply = %q, want self-closed REP.P-CALL without CALL-ID", rep)
	}

	n := waitEvent(t, s.events)
	if n.Summary != "Rufanforderung" {
		t.Errorf("summary = %q", n.Summary)
	}
	if n.Labels["espa.address"] != "99" {
		t.Errorf("address label = %q", n.Labels["espa.address"])
	}
	if n.Severity != model.SevWarning {
		t.Errorf("severity = %q, want warning", n.Severity)
	}
	if n.DedupKey != "" {
		t.Errorf("dedupKey = %q, want empty without CALL-ID", n.DedupKey)
	}
}

// TestESPAXCallWithoutMessageUsesDefaultSummary also covers the V="…"
// value-attribute dialect.
func TestESPAXCallWithoutMessageUsesDefaultSummary(t *testing.T) {
	s := startESPAXSession(t, model.SevInfo)

	call := `<ESPA-X version="2.0" timestamp="2026-07-17T10:03:00Z">` +
		`<REQ.P-CALL><CP-CALL><CALL-ADDRESS V="42"/></CP-CALL></REQ.P-CALL></ESPA-X>`
	mustWrite(t, s.client, frameDoc(call))
	s.readReply(t)

	n := waitEvent(t, s.events)
	if n.Summary != "ESPA-X call 42" {
		t.Errorf("summary = %q, want default with address", n.Summary)
	}
	if n.Severity != model.SevInfo {
		t.Errorf("severity = %q, want default info", n.Severity)
	}
}

func TestESPAXSeverityMapping(t *testing.T) {
	def := model.SevInfo
	cases := []struct {
		prio string
		want model.Severity
	}{
		{"alarm", model.SevCritical},
		{"prio-1", model.SevCritical},
		{"1", model.SevCritical},
		{"high", model.SevWarning},
		{"2", model.SevWarning},
		{"normal", def},
		{"", def},
	}
	for _, c := range cases {
		if got := espaxSeverity(c.prio, def); got != c.want {
			t.Errorf("espaxSeverity(%q) = %q, want %q", c.prio, got, c.want)
		}
	}
}

package notify

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// fakeAMI is a minimal Asterisk Manager Interface endpoint: banner,
// Login, Originate (recorded), Logoff. It interleaves an unrelated
// event before the Originate response to exercise the skip path.
type fakeAMI struct {
	addr          string
	failLogin     bool
	failOriginate bool

	mu        sync.Mutex
	login     []string
	originate []string
}

func startFakeAMI(t *testing.T, failLogin, failOriginate bool) *fakeAMI {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	f := &fakeAMI{addr: ln.Addr().String(), failLogin: failLogin, failOriginate: failOriginate}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = fmt.Fprintf(conn, "Asterisk Call Manager/9.0.0\r\n")
		r := bufio.NewReader(conn)
		for {
			var pkt []string
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\r\n")
				if line == "" {
					break
				}
				pkt = append(pkt, line)
			}
			action, actionID := "", ""
			for _, l := range pkt {
				if v, ok := strings.CutPrefix(l, "Action: "); ok {
					action = v
				}
				if v, ok := strings.CutPrefix(l, "ActionID: "); ok {
					actionID = v
				}
			}
			switch action {
			case "Login":
				f.mu.Lock()
				f.login = pkt
				f.mu.Unlock()
				if f.failLogin {
					_, _ = fmt.Fprintf(conn, "Response: Error\r\nActionID: %s\r\nMessage: Authentication failed\r\n\r\n", actionID)
					return
				}
				_, _ = fmt.Fprintf(conn, "Response: Success\r\nActionID: %s\r\nMessage: Authentication accepted\r\n\r\n", actionID)
			case "Originate":
				f.mu.Lock()
				f.originate = pkt
				f.mu.Unlock()
				// unrelated event between request and response
				_, _ = fmt.Fprintf(conn, "Event: FullyBooted\r\nPrivilege: system,all\r\n\r\n")
				if f.failOriginate {
					_, _ = fmt.Fprintf(conn, "Response: Error\r\nActionID: %s\r\nMessage: Extension does not exist\r\n\r\n", actionID)
				} else {
					_, _ = fmt.Fprintf(conn, "Response: Success\r\nActionID: %s\r\nMessage: Originate successfully queued\r\n\r\n", actionID)
				}
			case "Logoff":
				_, _ = fmt.Fprintf(conn, "Response: Goodbye\r\n\r\n")
				return
			}
		}
	}()
	return f
}

func (f *fakeAMI) originateLines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.originate...)
}

func amiHas(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func asteriskChannel(t *testing.T, store *storage.Store, ctx context.Context, addr string, extra map[string]string) {
	t.Helper()
	cfg := map[string]string{
		"provider": "asterisk", "host": "127.0.0.1",
		"username": "northplane", "secret": "amisecret",
		"channel": "PJSIP/{to}@trunk",
	}
	if _, port, err := net.SplitHostPort(addr); err == nil {
		cfg["port"] = port
	}
	for k, v := range extra {
		cfg[k] = v
	}
	putChannel(t, store, ctx, model.NotificationChannel{
		Name: "asterisk", Type: model.ChannelVoice, Config: cfg})
}

func TestAsteriskVoiceOriginate(t *testing.T) {
	m, store, ctx := setupMgr(t)
	f := startFakeAMI(t, false, false)
	asteriskChannel(t, store, ctx, f.addr, map[string]string{"callerId": "Northplane <8000>"})

	alert := openAlert(t, store, ctx, "asterisk case")
	rc := rcFor(m, alert)
	ch := mustChannel(t, m, ctx, "asterisk")
	_, body, err := m.render(ch, rc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.send(ctx, ch, "+4915012345", "", body, rc); err != nil {
		t.Fatalf("originate: %v", err)
	}

	lines := f.originateLines()
	if !amiHas(lines, "Channel: PJSIP/+4915012345@trunk") {
		t.Fatalf("channel template not applied: %v", lines)
	}
	if !amiHas(lines, "Context: northplane-alert") || !amiHas(lines, "Exten: s") ||
		!amiHas(lines, "Priority: 1") || !amiHas(lines, "Async: true") {
		t.Fatalf("dialplan defaults missing: %v", lines)
	}
	if !amiHas(lines, "CallerID: Northplane <8000>") {
		t.Fatalf("caller id missing: %v", lines)
	}
	var hasText, hasAck bool
	for _, l := range lines {
		if strings.HasPrefix(l, "Variable: NP_TEXT=") && strings.Contains(l, "asterisk case") {
			hasText = true
		}
		if strings.HasPrefix(l, "Variable: NP_ACK_URL=") && strings.Contains(l, "/api/v1/voice/gather/") {
			hasAck = true
		}
	}
	if !hasText || !hasAck {
		t.Fatalf("channel variables missing (text=%v ack=%v): %v", hasText, hasAck, lines)
	}
}

func TestAsteriskVoiceApplicationMode(t *testing.T) {
	m, store, ctx := setupMgr(t)
	f := startFakeAMI(t, false, false)
	asteriskChannel(t, store, ctx, f.addr, map[string]string{
		"application": "Playback", "appData": "alert-sound"})

	ch := mustChannel(t, m, ctx, "asterisk")
	if _, err := m.send(ctx, ch, "100", "", "Alarm", &RenderContext{Severity: "CRITICAL"}); err != nil {
		t.Fatal(err)
	}
	lines := f.originateLines()
	if !amiHas(lines, "Application: Playback") || !amiHas(lines, "Data: alert-sound") {
		t.Fatalf("application mode: %v", lines)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "Context:") {
			t.Fatalf("context must not be set in application mode: %v", lines)
		}
	}
	if !amiHas(lines, "Variable: NP_SEVERITY=CRITICAL") {
		t.Fatalf("severity variable: %v", lines)
	}
}

func TestAsteriskVoiceErrors(t *testing.T) {
	m, store, ctx := setupMgr(t)

	f := startFakeAMI(t, true, false)
	asteriskChannel(t, store, ctx, f.addr, nil)
	ch := mustChannel(t, m, ctx, "asterisk")
	if _, err := m.send(ctx, ch, "100", "", "x", &RenderContext{}); err == nil ||
		!strings.Contains(err.Error(), "login") {
		t.Fatalf("want login error, got %v", err)
	}

	f2 := startFakeAMI(t, false, true)
	putChannel(t, store, ctx, model.NotificationChannel{
		Name: "asterisk", Type: model.ChannelVoice, Config: map[string]string{
			"provider": "asterisk", "host": "127.0.0.1", "port": portOf(t, f2.addr),
			"username": "northplane", "secret": "amisecret", "channel": "Local/{to}@out"}})
	ch = mustChannel(t, m, ctx, "asterisk")
	if _, err := m.send(ctx, ch, "100", "", "x", &RenderContext{}); err == nil ||
		!strings.Contains(err.Error(), "Extension does not exist") {
		t.Fatalf("want originate error, got %v", err)
	}

	// missing config
	putChannel(t, store, ctx, model.NotificationChannel{
		Name: "asterisk", Type: model.ChannelVoice,
		Config: map[string]string{"provider": "asterisk"}})
	ch = mustChannel(t, m, ctx, "asterisk")
	if _, err := m.send(ctx, ch, "100", "", "x", &RenderContext{}); err == nil ||
		!strings.Contains(err.Error(), "required") {
		t.Fatalf("want config error, got %v", err)
	}
}

func TestAMISanitize(t *testing.T) {
	in := "line1\r\nAction: Logoff\n\nline2"
	out := amiSanitize(in)
	if strings.ContainsAny(out, "\r\n") {
		t.Fatalf("CRLF must be stripped: %q", out)
	}
}

func portOf(t *testing.T, addr string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

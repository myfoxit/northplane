package mailin

// In-process fake IMAP server drives a full poll cycle over plain TCP
// (tls=off). It scripts a minimal RFC 3501 conversation serving one
// multipart message with a quoted-printable text/plain part and an
// RFC 2047-encoded subject, then asserts:
//   - an event is published with the decoded subject, derived severity and
//     "<source>/<Message-ID>" dedup key;
//   - the server received STORE +FLAGS (\Seen);
//   - a second poll (SEARCH UNSEEN now empty) publishes nothing.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// The raw message: RFC2047-encoded subject containing "CRITICAL", a
// multipart/alternative body whose text/plain part is quoted-printable.
const rawMessage = "" +
	"From: Alarm Robot <alarms@example.net>\r\n" +
	"To: noc@example.net\r\n" +
	"Subject: =?utf-8?q?=5BCRITICAL=5D_Festpl=C3=A4tte_voll?=\r\n" +
	"Message-ID: <abc123@example.net>\r\n" +
	"Date: Mon, 02 Jan 2006 15:04:05 +0000\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/alternative; boundary=\"BOUND42\"\r\n" +
	"\r\n" +
	"--BOUND42\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: quoted-printable\r\n" +
	"\r\n" +
	"Disk =C3=BCber 90=25 voll auf host-01.=\r\n" +
	"\r\nBitte pr=C3=BCfen.\r\n" +
	"--BOUND42\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: 7bit\r\n" +
	"\r\n" +
	"<p>ignored html</p>\r\n" +
	"--BOUND42--\r\n"

// fakeIMAP is a scripted single-connection IMAP server.
type fakeIMAP struct {
	t  *testing.T
	ln net.Listener

	mu       sync.Mutex
	sawStore bool   // STORE +FLAGS (\Seen) received
	unseen   string // current UNSEEN search result ("1" then "")
	conns    int
}

func newFakeIMAP(t *testing.T) *fakeIMAP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeIMAP{t: t, ln: ln, unseen: "1"}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeIMAP) addr() string { return f.ln.Addr().String() }

func (f *fakeIMAP) port() string {
	_, p, _ := net.SplitHostPort(f.addr())
	return p
}

func (f *fakeIMAP) storeSeen() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sawStore
}

func (f *fakeIMAP) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns++
		f.mu.Unlock()
		go f.handle(conn)
	}
}

func (f *fakeIMAP) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	w := func(s string) {
		bw.WriteString(s)
		bw.Flush()
	}
	// Untagged greeting.
	w("* OK [CAPABILITY IMAP4rev1] fake ready\r\n")

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		fields := strings.SplitN(line, " ", 3)
		if len(fields) < 2 {
			continue
		}
		tag, cmd := fields[0], strings.ToUpper(fields[1])
		switch cmd {
		case "LOGIN":
			w(tag + " OK LOGIN completed\r\n")
		case "SELECT":
			// Interleave realistic untagged status lines before completion.
			w("* 1 EXISTS\r\n")
			w("* 0 RECENT\r\n")
			w("* FLAGS (\\Seen \\Deleted)\r\n")
			w(tag + " OK [READ-WRITE] SELECT completed\r\n")
		case "SEARCH":
			f.mu.Lock()
			u := f.unseen
			f.mu.Unlock()
			if u == "" {
				w("* SEARCH\r\n")
			} else {
				w("* SEARCH " + u + "\r\n")
			}
			w(tag + " OK SEARCH completed\r\n")
		case "FETCH":
			// Serve the message as an IMAP literal {n}CRLF<bytes>.
			body := rawMessage
			w(fmt.Sprintf("* 1 FETCH (RFC822 {%d}\r\n", len(body)))
			w(body)
			w(")\r\n")
			w(tag + " OK FETCH completed\r\n")
		case "STORE":
			if strings.Contains(strings.ToUpper(line), "\\SEEN") {
				f.mu.Lock()
				f.sawStore = true
				f.unseen = "" // message is now seen → next SEARCH empty
				f.mu.Unlock()
			}
			w("* 1 FETCH (FLAGS (\\Seen))\r\n")
			w(tag + " OK STORE completed\r\n")
		case "LOGOUT":
			w("* BYE logging out\r\n")
			w(tag + " OK LOGOUT completed\r\n")
			return
		default:
			w(tag + " OK done\r\n")
		}
	}
}

// collectEvents subscribes to the bus and accumulates published events.
func collectEvents(bus *eventbus.Bus) (*sync.Mutex, *[]*model.Event, func()) {
	sub := bus.Subscribe(64)
	var mu sync.Mutex
	var got []*model.Event
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case ev := <-sub.C:
				mu.Lock()
				got = append(got, ev)
				mu.Unlock()
			}
		}
	}()
	return &mu, &got, func() { close(done); sub.Close() }
}

func TestPollPublishesEventAndMarksSeen(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir(), RetentionMonths: 12})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	srv := newFakeIMAP(t)

	// Seed an enabled imap EventSource pointing at the fake server (tls off).
	src := &model.EventSource{
		Name:    "mailbox-noc",
		Type:    "imap",
		Enabled: true,
		Labels:  model.Labels{"team": "noc"},
		Config: map[string]string{
			"host":         "127.0.0.1",
			"port":         srv.port(),
			"tls":          "off",
			"username":     "noc@example.net",
			"password":     "s3cret",
			"folder":       "INBOX",
			"pollInterval": "15s",
			"markSeen":     "true",
		},
	}
	if _, err := store.PutResource(ctx, model.DefaultTenant, storage.KindEventSource, src.Name, src, -1); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	bus := eventbus.New()
	mu, got, stop := collectEvents(bus)
	defer stop()

	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelWarn}))
	secret := func(_ context.Context, _, _ string) (string, error) {
		return "", fmt.Errorf("no secret store in test")
	}
	m := New(store, bus, secret, log)

	// Build the poller directly (rather than via reconcile, whose goroutine
	// would poll concurrently) so the cycle is driven deterministically.
	p := m.testPoller(t, ctx)

	n, err := p.poll(ctx, log)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if n != 1 {
		t.Fatalf("first poll published %d events, want 1", n)
	}

	// Allow the fanout goroutine to record the event.
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(*got) == 1
	}, "event to be published")

	mu.Lock()
	ev := (*got)[0]
	mu.Unlock()

	// Event shape mirrors the HTTP ingress (Type ingress, NormEvent payload).
	if ev.Type != model.EventIngress {
		t.Errorf("event type = %q, want %q", ev.Type, model.EventIngress)
	}
	if ev.TenantID != model.DefaultTenant {
		t.Errorf("tenant = %q, want default", ev.TenantID)
	}
	if ev.Severity != model.SevCritical {
		t.Errorf("severity = %q, want critical (subject has CRITICAL)", ev.Severity)
	}

	var norm model.NormEvent
	if err := json.Unmarshal(ev.Payload, &norm); err != nil {
		t.Fatalf("payload not a NormEvent: %v", err)
	}
	wantSubject := "[CRITICAL] Festplätte voll"
	if norm.Summary != wantSubject {
		t.Errorf("summary = %q, want decoded subject %q", norm.Summary, wantSubject)
	}
	if norm.Severity != model.SevCritical {
		t.Errorf("norm severity = %q, want critical", norm.Severity)
	}
	if norm.DedupKey != "mailbox-noc/<abc123@example.net>" {
		t.Errorf("dedupKey = %q, want mailbox-noc/<abc123@example.net>", norm.DedupKey)
	}
	if norm.Labels["from"] != "alarms@example.net" {
		t.Errorf("from label = %q, want alarms@example.net", norm.Labels["from"])
	}
	if norm.Labels["source"] != "mailbox-noc" {
		t.Errorf("source label = %q, want mailbox-noc", norm.Labels["source"])
	}
	if norm.Labels["team"] != "noc" {
		t.Errorf("team label = %q, want noc (source label)", norm.Labels["team"])
	}

	// The inner payload JSON carries subject/from/date/messageId.
	var inner struct {
		Subject, From, Date, MessageID string
	}
	if err := json.Unmarshal(norm.Payload, &inner); err != nil {
		t.Fatalf("inner payload: %v", err)
	}
	if inner.Subject != wantSubject || inner.From != "alarms@example.net" ||
		inner.MessageID != "<abc123@example.net>" || inner.Date == "" {
		t.Errorf("inner payload = %+v", inner)
	}

	// STORE \Seen must have reached the server.
	if !srv.storeSeen() {
		t.Error("server did not receive STORE +FLAGS (\\Seen)")
	}

	// Second poll: SEARCH UNSEEN is now empty → nothing new published.
	n2, err := p.poll(ctx, log)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second poll published %d events, want 0", n2)
	}
	// Give any (unexpected) fanout a moment, then assert still exactly one.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	total := len(*got)
	mu.Unlock()
	if total != 1 {
		t.Errorf("total events = %d, want 1 after empty second poll", total)
	}
}

// TestRunLifecycle drives the real Manager.Run reconcile path: it starts a
// poller goroutine for the seeded source, observes the published event, then
// cancels and asserts the manager shuts down its goroutines cleanly (no
// leak) — pollers must stop on ctx cancellation.
func TestRunLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir(), RetentionMonths: 12})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	srv := newFakeIMAP(t)
	src := &model.EventSource{
		Name: "lifecycle-box", Type: "email", Enabled: true,
		Config: map[string]string{
			"host": "127.0.0.1", "port": srv.port(), "tls": "off",
			"username": "u", "password": "p", "pollInterval": "15s", "markSeen": "true",
		},
	}
	if _, err := store.PutResource(ctx, model.DefaultTenant, storage.KindEventSource, src.Name, src, -1); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	bus := eventbus.New()
	mu, got, stop := collectEvents(bus)
	defer stop()
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelWarn}))
	m := New(store, bus, nil, log)

	// Baseline captured after fake server + collector goroutines exist, so the
	// only goroutines Run adds (reconcile loop + poller) must drain on cancel.
	runtime.Gosched()
	baseGoroutines := runtime.NumGoroutine()

	runDone := make(chan struct{})
	go func() { m.Run(ctx); close(runDone) }()

	// The poller polls once immediately on start → one event.
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(*got) >= 1
	}, "lifecycle event")

	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Manager.Run did not return after ctx cancel")
	}

	// After shutdown the goroutine count must settle back near baseline
	// (poller + reconcile goroutines stopped). Allow brief scheduler slack.
	waitFor(t, func() bool {
		return runtime.NumGoroutine() <= baseGoroutines+2
	}, "goroutines to drain after shutdown")
}

// TestBodyDecoding checks the quoted-printable text/plain extraction in
// isolation (multipart walk + QP decode + soft line-break joining).
func TestBodyDecoding(t *testing.T) {
	ct := "multipart/alternative; boundary=\"BOUND42\""
	body := rawMessage[strings.Index(rawMessage, "\r\n\r\n")+4:]
	got := extractTextPlain(ct, strings.NewReader(body))
	if !strings.Contains(got, "Disk über 90% voll auf host-01.") {
		t.Errorf("decoded body = %q, missing QP-decoded first line", got)
	}
	if !strings.Contains(got, "Bitte prüfen.") {
		t.Errorf("decoded body = %q, missing second line", got)
	}
	if strings.Contains(got, "ignored html") {
		t.Errorf("decoded body leaked html part: %q", got)
	}
}

// TestSeverityFromSubject covers the subject-token severity mapping.
func TestSeverityFromSubject(t *testing.T) {
	cases := []struct {
		subject string
		dflt    model.Severity
		want    model.Severity
	}{
		{"[CRITICAL] disk full", model.SevWarning, model.SevCritical},
		{"Service WARNING latency", model.SevInfo, model.SevWarning},
		{"All systems OK now", model.SevWarning, model.SevOK},
		{"Issue resolved", model.SevWarning, model.SevOK},
		{"nothing notable", model.SevWarning, model.SevWarning},
		{"nothing notable", model.SevInfo, model.SevInfo},
		{"stocking levels low", model.SevInfo, model.SevInfo}, // "ok" must not match inside "stocking"
	}
	for _, c := range cases {
		if got := severityFor(c.subject, c.dflt); got != c.want {
			t.Errorf("severityFor(%q, %q) = %q, want %q", c.subject, c.dflt, got, c.want)
		}
	}
}

// TestDedupKeyFallback covers the no-Message-ID hashing branch and the
// normal "<source>/<Message-ID>" form.
func TestDedupKeyFallback(t *testing.T) {
	if k := dedupKey("src", "<id@h>", "subj", "date"); k != "src/<id@h>" {
		t.Errorf("with message-id: got %q", k)
	}
	a := dedupKey("src", "", "Disk full", "Mon, 02 Jan 2006")
	b := dedupKey("src", "", "Disk full", "Mon, 02 Jan 2006")
	c := dedupKey("src", "", "Disk OK", "Mon, 02 Jan 2006")
	if !strings.HasPrefix(a, "src/") {
		t.Errorf("fallback missing source prefix: %q", a)
	}
	if a != b {
		t.Errorf("fallback not stable: %q vs %q", a, b)
	}
	if a == c {
		t.Errorf("fallback collided for different subjects: %q", a)
	}
}

// TestQuoteIMAP covers RFC 3501 quoted-string escaping of credentials.
func TestQuoteIMAP(t *testing.T) {
	cases := map[string]string{
		"user":       `"user"`,
		`a"b`:        `"a\"b"`,
		`a\b`:        `"a\\b"`,
		"with space": `"with space"`,
	}
	for in, want := range cases {
		if got := quoteIMAP(in); got != want {
			t.Errorf("quoteIMAP(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestParseConfigDefaults verifies SPEC §7.5 imap defaults and bounds.
func TestParseConfigDefaults(t *testing.T) {
	c, err := parseConfig(map[string]string{"host": "mail.example.net"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !c.useTLS || c.port != "993" || c.folder != "INBOX" ||
		c.pollInterval != 60*time.Second || !c.markSeen || c.severity != model.SevWarning {
		t.Errorf("defaults wrong: %+v", c)
	}
	c2, _ := parseConfig(map[string]string{"host": "h", "pollInterval": "1s"})
	if c2.pollInterval != 15*time.Second {
		t.Errorf("min pollInterval not enforced: %v", c2.pollInterval)
	}
	c3, _ := parseConfig(map[string]string{"host": "h", "tls": "off"})
	if c3.useTLS || c3.port != "143" {
		t.Errorf("tls=off handling wrong: %+v", c3)
	}
	if _, err := parseConfig(map[string]string{}); err == nil {
		t.Error("expected error for missing host")
	}
}

// testPoller builds exactly one poller from the seeded sources via the real
// desiredSources path (exercising config parsing), without starting the
// background reconcile/poll goroutine, so poll() can be driven by hand.
func (m *Manager) testPoller(t *testing.T, ctx context.Context) *poller {
	t.Helper()
	desired := m.desiredSources(ctx)
	if len(desired) != 1 {
		t.Fatalf("expected 1 desired source, got %d", len(desired))
	}
	for _, d := range desired {
		return &poller{
			mgr: m, tenantID: d.tenantID, src: d.src,
			cfg: d.cfg, fingerprint: d.fingerprint, cancel: func() {},
		}
	}
	return nil
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// testWriter adapts *testing.T to io.Writer for slog.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// Package mailin is the e-mail ingress adapter (SPEC §7.5, "E-Mail |
// IMAP-Poller"; F-01.03; "Alarmserver-Erbe"): it polls IMAP mailboxes for
// unread messages and turns each into a normalised monitoring event, fed
// into the very same event pipeline as the HTTP/webhook ingress so alert
// rules behave identically (mirrors internal/api/ingress.go).
//
// One Manager reconciles every enabled EventSource of type "imap"/"email"
// into exactly one poller goroutine; pollers tick at the source's
// pollInterval, connect over implicit TLS (or plain TCP for tests), fetch
// UNSEEN mail, publish events, and mark messages \Seen.
package mailin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

const (
	// reconcileInterval is how often the Manager re-reads sources and
	// starts/stops/restarts pollers.
	reconcileInterval = 30 * time.Second
	// defaultPollInterval / minPollInterval bound a source's tick.
	defaultPollInterval = 60 * time.Second
	minPollInterval     = 15 * time.Second
	// defaultFolder is the mailbox polled when none is configured.
	defaultFolder = "INBOX"
	// implicit TLS default port (RFC 8314 / IMAPS).
	defaultTLSPort = "993"
	// maxMessageChars caps the event Message body.
	maxMessageChars = 4000
	// maxPerPoll bounds how many UNSEEN messages one tick processes, so a
	// flooded mailbox cannot monopolise a poller.
	maxPerPoll = 200
)

// SecretFunc resolves a secret reference to its plaintext value for a
// tenant. Injected so the package stays decoupled from the crypto box.
type SecretFunc func(ctx context.Context, tenantID, name string) (string, error)

// Manager owns the set of IMAP pollers (SPEC §7.5).
type Manager struct {
	Store  *storage.Store
	Bus    *eventbus.Bus
	Secret SecretFunc
	Log    *slog.Logger

	mu      sync.Mutex
	pollers map[string]*poller // key: tenantID/sourceID
}

// New builds a Manager. A nil logger falls back to slog.Default(); a nil
// Secret resolver means only inline Config["password"] is usable.
func New(store *storage.Store, bus *eventbus.Bus, secret SecretFunc, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		Store:   store,
		Bus:     bus,
		Secret:  secret,
		Log:     log,
		pollers: map[string]*poller{},
	}
}

// Run reconciles pollers until ctx is cancelled. Each enabled imap/email
// source gets one poller goroutine; a poller is restarted when its config
// fingerprint changes and stopped when the source disappears or is
// disabled. On shutdown every poller goroutine is stopped (no leaks).
func (m *Manager) Run(ctx context.Context) {
	m.reconcile(ctx)
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-ticker.C:
			m.reconcile(ctx)
		}
	}
}

// reconcile diffs desired (enabled imap/email sources) against running
// pollers and applies start/stop/restart.
func (m *Manager) reconcile(ctx context.Context) {
	desired := m.desiredSources(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop pollers whose source vanished, was disabled, or changed config.
	for key, p := range m.pollers {
		want, ok := desired[key]
		if !ok || want.fingerprint != p.fingerprint {
			p.stop()
			delete(m.pollers, key)
		}
	}
	// Start pollers for newly-desired sources.
	for key, want := range desired {
		if _, ok := m.pollers[key]; ok {
			continue
		}
		pctx, cancel := context.WithCancel(ctx)
		p := &poller{
			mgr:         m,
			tenantID:    want.tenantID,
			src:         want.src,
			cfg:         want.cfg,
			fingerprint: want.fingerprint,
			cancel:      cancel,
		}
		p.wg.Add(1)
		go p.run(pctx)
		m.pollers[key] = p
	}
}

type desired struct {
	tenantID    string
	src         *model.EventSource
	cfg         imapConfig
	fingerprint string
}

// desiredSources lists every enabled imap/email source across all tenants.
func (m *Manager) desiredSources(ctx context.Context) map[string]desired {
	out := map[string]desired{}
	tenants, err := m.Store.Tenants(ctx)
	if err != nil {
		m.Log.Warn("mailin: list tenants failed", "err", err)
		return out
	}
	for _, t := range tenants {
		srcs, err := storage.LoadAll[model.EventSource](ctx, m.Store, t.ID, storage.KindEventSource)
		if err != nil {
			m.Log.Warn("mailin: list event-sources failed", "tenant", t.ID, "err", err)
			continue
		}
		for _, src := range srcs {
			if !src.Enabled || !isIMAPType(src.Type) {
				continue
			}
			cfg, err := parseConfig(src.Config)
			if err != nil {
				m.Log.Warn("mailin: invalid imap config", "source", src.Name, "err", err)
				continue
			}
			key := t.ID + "/" + src.ID
			out[key] = desired{
				tenantID:    t.ID,
				src:         src,
				cfg:         cfg,
				fingerprint: cfg.fingerprint(src),
			}
		}
	}
	return out
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	ps := make([]*poller, 0, len(m.pollers))
	for key, p := range m.pollers {
		ps = append(ps, p)
		delete(m.pollers, key)
	}
	m.mu.Unlock()
	for _, p := range ps {
		p.stop()
	}
}

func isIMAPType(t string) bool {
	switch strings.ToLower(t) {
	case "imap", "email":
		return true
	}
	return false
}

// imapConfig is the parsed, validated transport config for one source.
type imapConfig struct {
	host         string
	port         string
	useTLS       bool // implicit TLS (default true)
	username     string
	passwordRef  string // secret reference (preferred)
	passwordLit  string // inline fallback Config["password"]
	folder       string
	pollInterval time.Duration
	markSeen     bool
	severity     model.Severity
}

// parseConfig reads the source Config map (SPEC §7.5 imap fields). tls:
// "on"/"" (implicit TLS, default, port 993) or "off" (plain, tests).
func parseConfig(cfg map[string]string) (imapConfig, error) {
	c := imapConfig{
		useTLS:       true,
		folder:       defaultFolder,
		pollInterval: defaultPollInterval,
		markSeen:     true,
		severity:     model.SevWarning,
	}
	c.host = strings.TrimSpace(cfg["host"])
	if c.host == "" {
		return c, fmt.Errorf("host required")
	}
	switch strings.ToLower(strings.TrimSpace(cfg["tls"])) {
	case "", "on", "implicit", "true":
		c.useTLS = true
	case "off", "none", "false", "plain":
		c.useTLS = false
	case "starttls":
		// STARTTLS upgrade is not implemented in this minimal client; the
		// supported modes are implicit TLS ("on") and plain ("off").
		return c, fmt.Errorf("tls=starttls not supported (use on/off)")
	default:
		return c, fmt.Errorf("invalid tls %q", cfg["tls"])
	}
	c.port = strings.TrimSpace(cfg["port"])
	if c.port == "" {
		if c.useTLS {
			c.port = defaultTLSPort
		} else {
			c.port = "143"
		}
	}
	c.username = cfg["username"]
	c.passwordRef = strings.TrimSpace(cfg["passwordSecretRef"])
	c.passwordLit = cfg["password"]
	if f := strings.TrimSpace(cfg["folder"]); f != "" {
		c.folder = f
	}
	if iv := strings.TrimSpace(cfg["pollInterval"]); iv != "" {
		d, err := time.ParseDuration(iv)
		if err != nil {
			return c, fmt.Errorf("invalid pollInterval %q: %w", iv, err)
		}
		c.pollInterval = d
	}
	if c.pollInterval < minPollInterval {
		c.pollInterval = minPollInterval
	}
	if ms := strings.TrimSpace(cfg["markSeen"]); ms != "" {
		c.markSeen = !strings.EqualFold(ms, "false")
	}
	if sev := model.Severity(strings.TrimSpace(cfg["severity"])); sev.Valid() {
		c.severity = sev
	}
	return c, nil
}

// fingerprint identifies a config so the Manager can detect changes that
// warrant a poller restart. The secret value is not read here; we include
// its reference and the inline literal so credential changes restart too.
func (c imapConfig) fingerprint(src *model.EventSource) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%t|%s|%s|%s|%s|%s|%t|%s|%v\n",
		c.host, c.port, c.useTLS, c.username, c.passwordRef, c.passwordLit,
		c.folder, c.pollInterval, c.markSeen, c.severity, src.Labels.String())
	return hex.EncodeToString(h.Sum(nil))
}

// poller drives one IMAP source on its pollInterval.
type poller struct {
	mgr         *Manager
	tenantID    string
	src         *model.EventSource
	cfg         imapConfig
	fingerprint string

	cancel context.CancelFunc
	wg     sync.WaitGroup

	failures uint64 // consecutive connect/poll failures (backoff via slog)
}

func (p *poller) stop() {
	p.cancel()
	p.wg.Wait()
}

// run ticks at pollInterval. A failed poll logs a warning and simply waits
// for the next tick (per-source backoff); it never blocks other sources.
func (p *poller) run(ctx context.Context) {
	defer p.wg.Done()
	log := p.mgr.Log.With("source", p.src.Name, "tenant", p.tenantID, "host", p.cfg.host)
	log.Info("mailin: poller started", "folder", p.cfg.folder, "interval", p.cfg.pollInterval)
	defer log.Info("mailin: poller stopped")

	// Poll once immediately, then on each tick.
	p.pollOnce(ctx, log)
	ticker := time.NewTicker(p.cfg.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollOnce(ctx, log)
		}
	}
}

// pollOnce runs a full IMAP cycle: connect, login, select, search unseen,
// fetch+publish each, mark \Seen, logout.
func (p *poller) pollOnce(ctx context.Context, log *slog.Logger) {
	if err := ctx.Err(); err != nil {
		return
	}
	n, err := p.poll(ctx, log)
	if err != nil {
		p.failures++
		log.Warn("mailin: poll failed", "err", err, "consecutiveFailures", p.failures)
		return
	}
	if p.failures > 0 {
		log.Info("mailin: poll recovered", "afterFailures", p.failures)
	}
	p.failures = 0
	if n > 0 {
		log.Info("mailin: ingested mail", "events", n)
	}
}

func (p *poller) poll(ctx context.Context, log *slog.Logger) (int, error) {
	pass, err := p.password(ctx)
	if err != nil {
		return 0, fmt.Errorf("resolve password: %w", err)
	}
	addr := net.JoinHostPort(p.cfg.host, p.cfg.port)
	cl, err := dialIMAP(addr, p.cfg.useTLS, p.cfg.host)
	if err != nil {
		return 0, fmt.Errorf("connect %s: %w", addr, err)
	}
	// Always close the socket; LOGOUT is best-effort before that.
	defer func() { _ = cl.Close() }()

	if err := cl.Login(p.cfg.username, pass); err != nil {
		return 0, err
	}
	if err := cl.Select(p.cfg.folder); err != nil {
		return 0, err
	}
	ids, err := cl.SearchUnseen()
	if err != nil {
		return 0, err
	}
	published := 0
	for i, id := range ids {
		if i >= maxPerPoll {
			log.Warn("mailin: more unseen mail than maxPerPoll; deferring rest", "limit", maxPerPoll)
			break
		}
		if err := ctx.Err(); err != nil {
			return published, err
		}
		raw, err := cl.Fetch(id)
		if err != nil {
			log.Warn("mailin: fetch failed", "id", id, "err", err)
			continue
		}
		ev, err := p.buildEvent(raw)
		if err != nil {
			log.Warn("mailin: parse failed", "id", id, "err", err)
			continue
		}
		if err := p.publish(ctx, ev); err != nil {
			// Publishing failed (ctx cancelled / store error): do NOT mark
			// the message seen, so it is retried next poll.
			return published, err
		}
		published++
		if p.cfg.markSeen {
			if err := cl.MarkSeen(id); err != nil {
				log.Warn("mailin: mark \\Seen failed", "id", id, "err", err)
			}
		}
	}
	_ = cl.Logout()
	return published, nil
}

// password resolves the source credential: secret reference first, inline
// Config["password"] as fallback.
func (p *poller) password(ctx context.Context) (string, error) {
	if p.cfg.passwordRef != "" && p.mgr.Secret != nil {
		v, err := p.mgr.Secret(ctx, p.tenantID, p.cfg.passwordRef)
		if err == nil {
			return v, nil
		}
		// Fall through to inline literal if the ref cannot be resolved.
		if p.cfg.passwordLit == "" {
			return "", err
		}
	}
	return p.cfg.passwordLit, nil
}

// publish inserts the event and fans it out on the bus, mirroring the
// HTTP ingress path (internal/api/ingress.go publishNorm).
func (p *poller) publish(ctx context.Context, ev *model.Event) error {
	if err := p.mgr.Store.InsertEvents(ctx, []*model.Event{ev}); err != nil {
		return err
	}
	return p.mgr.Bus.PublishEventCtx(ctx, ev)
}

// buildEvent parses a raw RFC822 message into a normalised model.Event,
// constructed identically to the webhook ingress (Type=EventIngress,
// payload = marshalled NormEvent, same Severity vocabulary).
func (p *poller) buildEvent(raw []byte) (*model.Event, error) {
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	hdr := msg.Header

	subject := decodeHeader(hdr.Get("Subject"))
	fromAddr := parseFromAddr(hdr.Get("From"))
	messageID := strings.TrimSpace(hdr.Get("Message-ID"))
	dateStr := hdr.Get("Date")

	body := extractTextPlain(hdr.Get("Content-Type"), msg.Body)
	body = capRunes(body, maxMessageChars)

	summary := subject
	if summary == "" {
		summary = "(no subject)"
	}

	sev := severityFor(subject, p.cfg.severity)

	// Labels: source labels overlaid with the mail's source/from
	// (mirrors ingress: norm.Labels.Merge(src.Labels) — source labels win
	// over per-event ones, matching the HTTP path's precedence).
	labels := model.Labels{
		"source": p.src.Name,
		"from":   fromAddr,
	}.Merge(p.src.Labels)

	dedup := dedupKey(p.src.Name, messageID, subject, dateStr)

	payload, _ := json.Marshal(struct {
		Subject   string `json:"subject"`
		From      string `json:"from"`
		Date      string `json:"date"`
		MessageID string `json:"messageId"`
		Body      string `json:"body,omitempty"`
	}{Subject: subject, From: fromAddr, Date: dateStr, MessageID: messageID, Body: body})

	norm := &model.NormEvent{
		Source:     p.src.ID,
		ReceivedAt: time.Now().UTC(),
		DedupKey:   dedup,
		Severity:   sev,
		Summary:    summary,
		Labels:     labels,
		Resolve:    sev == model.SevOK,
		Payload:    payload,
	}
	rawNorm, _ := json.Marshal(norm)

	return &model.Event{
		ID:       model.NewID(),
		TenantID: p.tenantID,
		TS:       norm.ReceivedAt,
		Type:     model.EventIngress,
		SourceID: p.src.ID,
		Severity: sev,
		Payload:  rawNorm,
	}, nil
}

// severityFor derives severity from the subject tokens, falling back to
// the configured default. Vocabulary matches model.Severity:
// critical / warning / info / ok (SPEC §6.5). "resolved" maps to ok.
func severityFor(subject string, dflt model.Severity) model.Severity {
	s := strings.ToLower(subject)
	switch {
	case containsToken(s, "critical"):
		return model.SevCritical
	case containsToken(s, "ok"), containsToken(s, "resolved"):
		return model.SevOK
	case containsToken(s, "warning"):
		return model.SevWarning
	}
	if dflt.Valid() {
		return dflt
	}
	return model.SevWarning
}

// containsToken reports whether word appears as a whole, case-insensitive
// token in s (s is already lower-cased), so "ok" does not match "stocking".
func containsToken(s, word string) bool {
	for i := 0; i+len(word) <= len(s); i++ {
		if s[i:i+len(word)] != word {
			continue
		}
		if i > 0 && isWordByte(s[i-1]) {
			continue
		}
		if i+len(word) < len(s) && isWordByte(s[i+len(word)]) {
			continue
		}
		return true
	}
	return false
}

func isWordByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

// dedupKey is "<source>/<Message-ID>", or a hash of subject+date when the
// message carries no Message-ID.
func dedupKey(source, messageID, subject, date string) string {
	if messageID != "" {
		return source + "/" + messageID
	}
	h := sha256.Sum256([]byte(subject + "\x00" + date))
	return source + "/" + hex.EncodeToString(h[:8])
}

// decodeHeader decodes RFC 2047 encoded-words ("=?utf-8?...?=") in a
// header value, tolerating non-UTF-8 charsets by passing bytes through.
func decodeHeader(v string) string {
	if v == "" {
		return ""
	}
	dec := &mime.WordDecoder{CharsetReader: charsetPassthrough}
	out, err := dec.DecodeHeader(v)
	if err != nil {
		return v
	}
	return out
}

// parseFromAddr extracts the bare e-mail address from a From header,
// falling back to the decoded raw header when it does not parse.
func parseFromAddr(v string) string {
	if v == "" {
		return ""
	}
	parser := &mail.AddressParser{WordDecoder: &mime.WordDecoder{CharsetReader: charsetPassthrough}}
	if addr, err := parser.Parse(v); err == nil {
		return addr.Address
	}
	return strings.TrimSpace(decodeHeader(v))
}

// extractTextPlain walks the message body and returns the first text/plain
// part, decoding quoted-printable / base64 transfer encodings. Charsets
// other than UTF-8/ASCII are passed through as-is (assume UTF-8/ASCII).
func extractTextPlain(contentType string, body io.Reader) string {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType == "" {
		// No/!parseable Content-Type → treat as plain text.
		b, _ := io.ReadAll(body)
		return strings.TrimSpace(string(b))
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		if txt, ok := walkMultipart(body, params["boundary"]); ok {
			return strings.TrimSpace(txt)
		}
		return ""
	}
	// Single part: only surface text/plain (and unlabelled text/*).
	if mediaType == "text/plain" || strings.HasPrefix(mediaType, "text/") {
		b, _ := io.ReadAll(body)
		return strings.TrimSpace(string(b))
	}
	return ""
}

// walkMultipart returns the first text/plain part found (recursing into
// nested multiparts), already transfer-decoded.
func walkMultipart(body io.Reader, boundary string) (string, bool) {
	if boundary == "" {
		return "", false
	}
	mr := multipart.NewReader(body, boundary)
	for {
		part, err := mr.NextPart()
		if err != nil {
			return "", false
		}
		mediaType, params, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if strings.HasPrefix(mediaType, "multipart/") {
			if txt, ok := walkMultipart(part, params["boundary"]); ok {
				return txt, true
			}
			continue
		}
		if mediaType == "text/plain" || mediaType == "" {
			data := decodeTransfer(part.Header.Get("Content-Transfer-Encoding"), part)
			return data, true
		}
	}
}

// decodeTransfer applies the Content-Transfer-Encoding to a part body.
func decodeTransfer(enc string, r io.Reader) string {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "quoted-printable":
		b, _ := io.ReadAll(quotedprintable.NewReader(r))
		return string(b)
	case "base64":
		b, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, newBase64Sanitizer(r)))
		return string(b)
	default: // 7bit / 8bit / binary / none
		b, _ := io.ReadAll(r)
		return string(b)
	}
}

// charsetPassthrough lets the WordDecoder/AddressParser accept any declared
// charset by handing the bytes back unchanged (we assume UTF-8/ASCII).
func charsetPassthrough(_ string, input io.Reader) (io.Reader, error) {
	return input, nil
}

// capRunes truncates s to at most n runes (UTF-8 safe).
func capRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// newBase64Sanitizer strips CR/LF/spaces from a base64 stream so the
// std decoder (which rejects embedded newlines) handles MIME-wrapped data.
func newBase64Sanitizer(r io.Reader) io.Reader {
	b, _ := io.ReadAll(r)
	clean := strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', ' ', '\t':
			return -1
		}
		return r
	}, string(b))
	return strings.NewReader(clean)
}

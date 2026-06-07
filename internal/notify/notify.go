// Package notify delivers notifications (SPEC §9.6): channel senders
// (e-mail, webhook, Teams, Slack, ntfy, Web Push, SMS), Go-template
// messages with a FuncMap whitelist, retry with exponential backoff +
// jitter up to 24 h, and a dead-letter queue with UI surfacing
// (F-04.04). Delivery attempts are recorded as immutable notification
// events (F-05.09).
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// Manager consumes the outbox.
type Manager struct {
	store *storage.Store
	bus   *eventbus.Bus
	log   *slog.Logger

	BaseURL string
	// AckSecret signs one-shot ack links (SPEC §9.4).
	AckSecret []byte
	// Secrets resolves $SECRET:name$ in channel configs.
	Secrets func(tenantID, name string) (string, bool)
	// SendHook overrides actual transport in tests.
	SendHook func(channel *model.NotificationChannel, target string, subject, body string) (string, error)

	statSent, statFailed, statDead atomic.Uint64
}

// outboxLease is how long a claimed delivery is hidden from other ticks
// while it is being sent — must exceed the longest transport timeout.
const outboxLease = 2 * time.Minute

// New builds the manager.
func New(store *storage.Store, bus *eventbus.Bus, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{store: store, bus: bus, log: log}
}

// Run polls due outbox items (plus wake-ups via bus) until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.bus.Notifications:
		case <-ticker.C:
		}
		items, err := m.store.ClaimOutbox(ctx, time.Now().UTC(), outboxLease, 100)
		if err != nil {
			m.log.Error("notify: poll", "err", err)
			continue
		}
		for _, item := range items {
			m.deliver(ctx, item)
		}
	}
}

// Backoff: 30s · 2^n with ±20 % jitter, capped at 1 h, dead after 24 h
// cumulative (SPEC §9.6).
func backoff(attempt int) time.Duration {
	d := 30 * time.Second * (1 << min(attempt, 7))
	if d > time.Hour {
		d = time.Hour
	}
	jitter := time.Duration(rand.Int63n(int64(d) / 5))
	return d - time.Duration(int64(d)/10) + jitter
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

const maxAttempts = 30 // ≈ 26 h of retries → DLQ

func (m *Manager) deliver(ctx context.Context, item *storage.OutboxItem) {
	var err error
	var providerID string
	switch item.Kind {
	case "notification":
		providerID, err = m.deliverNotification(ctx, item)
	case "webhook-sub":
		providerID, err = m.deliverWebhookSub(ctx, item)
	case "action":
		err = fmt.Errorf("external actions (ServiceNow) need a configured integration — v2 backlog")
	default:
		err = fmt.Errorf("unknown outbox kind %q", item.Kind)
	}
	if err == nil {
		m.statSent.Add(1)
		_ = m.store.OutboxDone(ctx, item.ID)
		_ = providerID
		return
	}
	m.statFailed.Add(1)
	attempts := item.Attempts + 1
	dead := attempts >= maxAttempts
	if dead {
		m.statDead.Add(1)
		m.log.Error("notify: moved to DLQ", "item", item.ID, "err", err)
	}
	_ = m.store.OutboxRetry(ctx, item.ID, attempts,
		time.Now().UTC().Add(backoff(attempts)), dead, err.Error())
}

// notifyJob mirrors escalation.notifyJob (kept in sync via JSON shape).
type notifyJob struct {
	AlertID   string            `json:"alertId"`
	TenantID  string            `json:"tenantId"`
	ContactID string            `json:"contactId"`
	Channel   model.ChannelType `json:"channel"`
	StepIndex int               `json:"stepIndex"`
	Repeat    int               `json:"repeat"`
	Policy    string            `json:"policy"`
}

func (m *Manager) deliverNotification(ctx context.Context, item *storage.OutboxItem) (string, error) {
	var job notifyJob
	if err := json.Unmarshal(item.Payload, &job); err != nil {
		return "", fmt.Errorf("bad payload: %w", err)
	}
	alert, err := m.store.GetAlert(ctx, job.TenantID, job.AlertID)
	if err == storage.ErrNotFound {
		return "", nil // alert genuinely gone: drop silently (delivered=true)
	}
	if err != nil {
		return "", err // transient (DB blip): retry rather than lose the page
	}
	if alert.Status == model.AlertResolved || alert.Status == model.AlertExpired {
		return "", nil // raced resolution: nothing to send
	}
	contact, err := storage.LoadOne[model.Contact](ctx, m.store, job.TenantID,
		storage.KindContact, job.ContactID)
	if err != nil {
		return "", fmt.Errorf("contact %s: %w", job.ContactID, err)
	}
	channel, err := m.channelFor(ctx, job.TenantID, job.Channel)
	if err != nil {
		return "", err
	}

	rctx := m.renderContext(alert, contact, job)
	subject, body, err := m.render(channel, rctx)
	if err != nil {
		return "", fmt.Errorf("template: %w", err)
	}
	target := targetFor(channel.Type, contact, channel)
	if target == "" {
		return "", fmt.Errorf("contact %q has no %s target", contact.Name, channel.Type)
	}

	start := time.Now()
	var providerID string
	if m.SendHook != nil {
		providerID, err = m.SendHook(channel, target, subject, body)
	} else {
		providerID, err = m.send(ctx, channel, target, subject, body, rctx)
	}
	status := model.NotifySent
	errText := ""
	if err != nil {
		status, errText = model.NotifyFailed, err.Error()
	}
	rec := model.NotificationRecord{
		AlertID: alert.ID, StepIndex: job.StepIndex, Repeat: job.Repeat,
		ContactID: contact.ID, Contact: contact.Name,
		Channel: channel.Type, ChannelID: channel.ID,
		Target: mask(target), Status: status, Attempt: item.Attempts + 1,
		Error: errText, ProviderID: providerID,
		LatencyMS: time.Since(start).Milliseconds(),
	}
	raw, _ := json.Marshal(rec)
	ev := &model.Event{ID: model.NewID(), TenantID: job.TenantID, TS: time.Now().UTC(),
		Type: model.EventNotification, ObjectID: alert.ObjectID,
		Severity: model.SevInfo, Payload: raw}
	_ = m.store.InsertEvents(ctx, []*model.Event{ev})
	m.bus.FanoutOnly(ev)
	return providerID, err
}

// channelFor picks the tenant's configured channel instance of a type.
func (m *Manager) channelFor(ctx context.Context, tenantID string, typ model.ChannelType) (*model.NotificationChannel, error) {
	channels, err := storage.LoadAll[model.NotificationChannel](ctx, m.store, tenantID, storage.KindChannel)
	if err != nil {
		return nil, err
	}
	for _, c := range channels {
		if c.Type == typ && c.Enabled {
			return c, nil
		}
	}
	return nil, fmt.Errorf("no enabled %s channel configured", typ)
}

// RenderContext is the template context (documented for F-04.09
// per-channel templates).
type RenderContext struct {
	Alert    *model.Alert      `json:"alert"`
	Contact  *model.Contact    `json:"contact"`
	Severity string            `json:"severity"`
	Title    string            `json:"title"`
	Labels   map[string]string `json:"labels"`
	Step     int               `json:"step"`
	Repeat   int               `json:"repeat"`
	Policy   string            `json:"policy"`
	BaseURL  string            `json:"baseUrl"`
	AlertURL string            `json:"alertUrl"`
	AckURL   string            `json:"ackUrl"`
	Now      string            `json:"now"`
}

func (m *Manager) renderContext(alert *model.Alert, contact *model.Contact, job notifyJob) *RenderContext {
	rc := &RenderContext{
		Alert: alert, Contact: contact,
		Severity: strings.ToUpper(string(alert.Severity)),
		Title:    alert.Title, Labels: alert.Labels,
		Step: job.StepIndex + 1, Repeat: job.Repeat, Policy: job.Policy,
		BaseURL: m.BaseURL, Now: time.Now().UTC().Format(time.RFC3339),
	}
	if m.BaseURL != "" {
		rc.AlertURL = m.BaseURL + "/alerts/" + alert.ID
		rc.AckURL = m.BaseURL + "/api/v1/ack/" + AckToken(m.AckSecret, alert.ID, contact.ID, 24*time.Hour)
	}
	return rc
}

// AckToken builds a signed, expiring one-shot ack link token
// (SPEC §9.4: works without login).
func AckToken(secret []byte, alertID, contactID string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	msg := fmt.Sprintf("%s|%s|%d", alertID, contactID, exp)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(msg))
	return fmt.Sprintf("%s.%s.%d.%s", alertID, contactID, exp, hex.EncodeToString(mac.Sum(nil)[:16]))
}

// VerifyAckToken parses and validates an ack token.
func VerifyAckToken(secret []byte, token string) (alertID, contactID string, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 4 {
		return "", "", fmt.Errorf("malformed token")
	}
	var exp int64
	if _, err := fmt.Sscanf(parts[2], "%d", &exp); err != nil {
		return "", "", fmt.Errorf("malformed expiry")
	}
	if time.Now().Unix() > exp {
		return "", "", fmt.Errorf("token expired")
	}
	msg := fmt.Sprintf("%s|%s|%d", parts[0], parts[1], exp)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(msg))
	want := hex.EncodeToString(mac.Sum(nil)[:16])
	if !hmac.Equal([]byte(want), []byte(parts[3])) {
		return "", "", fmt.Errorf("bad signature")
	}
	return parts[0], parts[1], nil
}

// templateFuncs is the deliberately small FuncMap (SPEC §13.1: no os
// access, whitelist only).
var templateFuncs = template.FuncMap{
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
	"trunc": func(n int, s string) string {
		if len(s) <= n {
			return s
		}
		return s[:n] + "…"
	},
	"json": func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	},
}

// defaultTemplates per channel type (F-04.09); override per channel.
var defaultTemplates = map[model.ChannelType]string{
	model.ChannelEmail: `Subject: [{{.Severity}}] {{.Title}}
{{.Severity}}: {{.Title}}

Alert:    {{.AlertURL}}
Opened:   {{.Alert.OpenedAt}}
Labels:   {{range $k,$v := .Labels}}{{$k}}={{$v}} {{end}}
Step:     {{.Step}} (policy {{.Policy}})

Acknowledge: {{.AckURL}}`,
	model.ChannelSMS:  `[{{.Severity}}] {{trunc 100 .Title}} ack: {{.AckURL}}`,
	model.ChannelNtfy: `{{.Title}}`,
	model.ChannelPush: `{{.Title}}`,
	model.ChannelSlack: `{"text":"[{{.Severity}}] {{.Title}}","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"*[{{.Severity}}] <{{.AlertURL}}|{{.Title}}>*\nStep {{.Step}} · Policy {{.Policy}}"}},{"type":"actions","elements":[{"type":"button","text":{"type":"plain_text","text":"Acknowledge"},"url":"{{.AckURL}}","style":"primary"}]}]}`,
	model.ChannelTeams: `{"type":"message","attachments":[{"contentType":"application/vnd.microsoft.card.adaptive","content":{"type":"AdaptiveCard","$schema":"http://adaptivecards.io/schemas/adaptive-card.json","version":"1.4","body":[{"type":"TextBlock","size":"Medium","weight":"Bolder","text":"[{{.Severity}}] {{.Title}}"},{"type":"TextBlock","text":"Step {{.Step}} · Policy {{.Policy}}","wrap":true}],"actions":[{"type":"Action.OpenUrl","title":"Acknowledge","url":"{{.AckURL}}"},{"type":"Action.OpenUrl","title":"Open","url":"{{.AlertURL}}"}]}}]}`,
	model.ChannelWebhook: `{"version":1,"alert":{{json .Alert}},"severity":"{{.Severity}}","title":{{json .Title}},"labels":{{json .Labels}},"step":{{.Step}},"policy":{{json .Policy}},"ackUrl":{{json .AckURL}},"alertUrl":{{json .AlertURL}}}`,
	model.ChannelVoice: `Northplane alert. Severity {{.Severity}}. {{.Title}}. Press 4 to acknowledge.`,
}

// render produces subject (e-mail) and body. The first line of the
// e-mail template carries "Subject: …".
func (m *Manager) render(ch *model.NotificationChannel, rc *RenderContext) (subject, body string, err error) {
	tplText := ch.Template
	if tplText == "" {
		tplText = defaultTemplates[ch.Type]
	}
	if tplText == "" {
		tplText = defaultTemplates[model.ChannelWebhook]
	}
	tpl, err := template.New("msg").Funcs(templateFuncs).Option("missingkey=zero").Parse(tplText)
	if err != nil {
		return "", "", err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, rc); err != nil {
		return "", "", err
	}
	out := buf.String()
	if ch.Type == model.ChannelEmail {
		if rest, ok := strings.CutPrefix(out, "Subject: "); ok {
			if i := strings.IndexByte(rest, '\n'); i >= 0 {
				return strings.TrimSpace(rest[:i]), strings.TrimLeft(rest[i+1:], "\n"), nil
			}
		}
		return "[" + rc.Severity + "] " + rc.Title, out, nil
	}
	return "", out, nil
}

func targetFor(typ model.ChannelType, contact *model.Contact, ch *model.NotificationChannel) string {
	switch typ {
	case model.ChannelEmail:
		return contact.Email
	case model.ChannelSMS, model.ChannelVoice:
		return contact.Phone
	case model.ChannelPush:
		return contact.UserID // subscriptions resolved per user
	default:
		return ch.Config["url"] // team channels: fixed endpoint
	}
}

// mask redacts the middle of delivery targets for the audit trail.
func mask(s string) string {
	if len(s) <= 6 {
		return "***"
	}
	return s[:3] + "…" + s[len(s)-3:]
}

// resolveSecret expands $SECRET:name$ in channel config values.
func (m *Manager) resolveSecret(tenantID, v string) string {
	if name, ok := strings.CutPrefix(v, "$SECRET:"); ok && strings.HasSuffix(name, "$") {
		if m.Secrets != nil {
			if val, ok := m.Secrets(tenantID, strings.TrimSuffix(name, "$")); ok {
				return val
			}
		}
		return ""
	}
	return v
}

// HMACSign computes the X-Northplane-Signature webhook header
// (SPEC §9.6: HMAC-SHA256).
func HMACSign(secret []byte, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// TestSend pushes a synthetic notification through a channel
// (admin test console, SPEC §12.3). target overrides the destination
// for personal channel types.
func (m *Manager) TestSend(ctx context.Context, ch *model.NotificationChannel, target, by string) (string, error) {
	alert := &model.Alert{ID: "test", TenantID: ch.TenantID,
		Severity: model.SevInfo, Status: model.AlertOpen,
		Title:    "Test notification from Northplane (" + by + ")",
		OpenedAt: time.Now().UTC()}
	contact := &model.Contact{Name: by, Email: target, Phone: target, UserID: target}
	rc := m.renderContext(alert, contact, notifyJob{AlertID: alert.ID, TenantID: ch.TenantID})
	subject, body, err := m.render(ch, rc)
	if err != nil {
		return "", err
	}
	dst := targetFor(ch.Type, contact, ch)
	if dst == "" {
		return "", fmt.Errorf("no target for %s (pass one in the request)", ch.Type)
	}
	if m.SendHook != nil {
		return m.SendHook(ch, dst, subject, body)
	}
	return m.send(ctx, ch, dst, subject, body, rc)
}

// Stats snapshot.
type Stats struct {
	Sent   uint64 `json:"sent"`
	Failed uint64 `json:"failed"`
	Dead   uint64 `json:"dead"`
}

// Stats for self-metrics (notification success/error per channel lives
// in events; this is the coarse counter).
func (m *Manager) Stats() Stats {
	return Stats{Sent: m.statSent.Load(), Failed: m.statFailed.Load(), Dead: m.statDead.Load()}
}

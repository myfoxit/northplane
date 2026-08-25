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
	"strconv"
	"strings"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
	"github.com/northplane/northplane/internal/tts"
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
	// TTS synthesises spoken alarm text for voice channels (nil = the
	// telephony provider's own speech only).
	TTS *tts.Service

	statSent, statFailed, statDead atomic.Uint64
	// statDropped counts immutable notification events that failed to
	// persist. Recording them is best-effort (delivery already happened),
	// but a silent drop hides an audit-trail gap — surface it.
	statDropped atomic.Uint64
}

// recordEvent persists a notification event best-effort. The send has
// already occurred, so a failed insert must not fail the delivery; but
// a lost event is an audit-trail gap (F-05.09) and must be observable —
// log a warning and bump the dropped-events counter (exposed as
// np_events_dropped_total{source="notify"} via Stats()) instead of
// discarding the error silently.
func (m *Manager) recordEvent(ctx context.Context, ev *model.Event) {
	if err := m.store.InsertEvents(ctx, []*model.Event{ev}); err != nil {
		m.statDropped.Add(1)
		m.log.Warn("notify: event insert dropped", "err", err,
			"type", ev.Type, "object", ev.ObjectID)
	}
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

// retryPolicy shapes the outbox retry loop. Defaults per SPEC §9.6:
// 30s · 2^n with ±10 % jitter, capped at 1 h, DLQ after 30 attempts
// (≈ 26 h). Channels override via config retryMaxAttempts /
// retryBackoffSeconds / retryBackoffCapSeconds — an alarm SMS that
// should retry every 30 s and give up after 5 tries is a config choice,
// not a code change.
type retryPolicy struct {
	maxAttempts int
	base        time.Duration
	cap         time.Duration
}

var defaultRetry = retryPolicy{maxAttempts: 30, base: 30 * time.Second, cap: time.Hour}

func (p retryPolicy) backoff(attempt int) time.Duration {
	d := p.base * (1 << min(attempt, 12))
	if d > p.cap || d <= 0 { // <=0 guards shift overflow
		d = p.cap
	}
	jitter := time.Duration(rand.Int63n(int64(d)/5 + 1))
	return d - time.Duration(int64(d)/10) + jitter
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// retryPolicyFor resolves the channel-specific retry overrides for a
// failed delivery. Only "notification" items carry a channel type; all
// other kinds use the defaults. Lookup errors fall back to defaults —
// the retry loop must never get stuck on a config read.
func (m *Manager) retryPolicyFor(ctx context.Context, item *storage.OutboxItem) retryPolicy {
	pol := defaultRetry
	if item.Kind != "notification" {
		return pol
	}
	var job notifyJob
	if err := json.Unmarshal(item.Payload, &job); err != nil || job.Channel == "" {
		return pol
	}
	ch, err := m.channelFor(ctx, job.TenantID, job.Channel)
	if err != nil {
		return pol
	}
	if v, err := strconv.Atoi(ch.Config["retryMaxAttempts"]); err == nil && v > 0 && v <= 100 {
		pol.maxAttempts = v
	}
	if v, err := strconv.Atoi(ch.Config["retryBackoffSeconds"]); err == nil && v > 0 {
		pol.base = time.Duration(v) * time.Second
	}
	if v, err := strconv.Atoi(ch.Config["retryBackoffCapSeconds"]); err == nil && v > 0 {
		pol.cap = time.Duration(v) * time.Second
	}
	if pol.cap < pol.base {
		pol.cap = pol.base
	}
	return pol
}

func (m *Manager) deliver(ctx context.Context, item *storage.OutboxItem) {
	var err error
	var providerID string
	switch item.Kind {
	case "notification":
		providerID, err = m.deliverNotification(ctx, item)
	case "object-notification":
		providerID, err = m.deliverObjectNotification(ctx, item)
	case "webhook-sub":
		providerID, err = m.deliverWebhookSub(ctx, item)
	case "action":
		providerID, err = m.deliverAction(ctx, item)
	case "ticket-close":
		providerID, err = m.deliverTicketClose(ctx, item)
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
	pol := m.retryPolicyFor(ctx, item)
	attempts := item.Attempts + 1
	dead := attempts >= pol.maxAttempts
	if dead {
		m.statDead.Add(1)
		m.log.Error("notify: moved to DLQ", "item", item.ID, "err", err)
	}
	_ = m.store.OutboxRetry(ctx, item.ID, attempts,
		time.Now().UTC().Add(pol.backoff(attempts)), dead, err.Error(), item.ChannelID)
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
	item.ChannelID = channel.ID // surfaces the failing instance in DLQ rows

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
	m.recordEvent(ctx, ev)
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

// channelByName resolves a channel by its configured name (escalation
// actions reference channels by name, not type).
func (m *Manager) channelByName(ctx context.Context, tenantID, name string) (*model.NotificationChannel, error) {
	ch, err := storage.LoadOne[model.NotificationChannel](ctx, m.store, tenantID, storage.KindChannel, name)
	if err != nil {
		return nil, fmt.Errorf("channel %q: %w", name, err)
	}
	if !ch.Enabled {
		return nil, fmt.Errorf("channel %q is disabled", name)
	}
	return ch, nil
}

// deliverAction executes escalation-step side effects (F-04.05): ticket
// creation (ServiceNow/Zendesk/Jira/generic) and action webhooks. Rides
// the outbox so retries/backoff/DLQ apply like any delivery.
func (m *Manager) deliverAction(ctx context.Context, item *storage.OutboxItem) (string, error) {
	var job struct {
		Action *model.EscalationAction `json:"action"`
		Alert  *model.Alert            `json:"alert"`
		Policy string                  `json:"policy"`
		Step   int                     `json:"step"`
	}
	if err := json.Unmarshal(item.Payload, &job); err != nil {
		return "", fmt.Errorf("bad payload: %w", err)
	}
	if job.Action == nil || job.Alert == nil {
		return "", nil
	}
	// Skip side effects for alerts that resolved while queued; a closed
	// alert must not open a fresh ticket.
	if current, err := m.store.GetAlert(ctx, job.Alert.TenantID, job.Alert.ID); err == nil {
		if current.Status == model.AlertResolved || current.Status == model.AlertExpired {
			return "", nil
		}
		if current.Ticket != nil && current.Ticket.Ref != "" {
			return current.Ticket.Ref, nil // already ticketed (repeat step)
		}
		job.Alert = current
	}

	// normalise: the legacy servicenow shorthand is a ticket action against
	// the tenant's servicenow channel.
	ticketAct := job.Action.Ticket
	if ticketAct == nil && job.Action.ServiceNow != nil {
		ch, err := m.channelFor(ctx, job.Alert.TenantID, model.ChannelServiceNow)
		if err != nil {
			return "", err
		}
		ticketAct = &model.TicketAction{Channel: ch.Name,
			AutoClose: job.Action.ServiceNow.AutoClose,
			Params:    map[string]string{"assignmentGroup": job.Action.ServiceNow.AssignmentGroup}}
	}

	var providerID string
	if ticketAct != nil {
		ch, err := m.channelByName(ctx, job.Alert.TenantID, ticketAct.Channel)
		if err != nil {
			return "", err
		}
		rc := m.renderContext(job.Alert, &model.Contact{}, notifyJob{
			AlertID: job.Alert.ID, TenantID: job.Alert.TenantID,
			StepIndex: job.Step, Policy: job.Policy})
		subject, body, err := m.render(ch, rc)
		if err != nil {
			return "", fmt.Errorf("template: %w", err)
		}
		if m.SendHook != nil { // tests intercept transports
			providerID, err = m.SendHook(ch, "ticket", subject, body)
			if err == nil {
				m.attachTicket(ctx, rc, &model.TicketRef{Channel: ch.Name,
					Type: string(ch.Type), Ref: providerID, AutoClose: ticketAct.AutoClose})
			}
		} else {
			providerID, err = m.sendTicket(ctx, ch, subject, body, rc, ticketAct)
		}
		if err != nil {
			return "", err
		}
		m.recordActionEvent(ctx, job.Alert, ch, providerID, job.Step)
	}
	if job.Action.Webhook != "" {
		ch, err := m.channelByName(ctx, job.Alert.TenantID, job.Action.Webhook)
		if err != nil {
			return providerID, err
		}
		rc := m.renderContext(job.Alert, &model.Contact{}, notifyJob{
			AlertID: job.Alert.ID, TenantID: job.Alert.TenantID,
			StepIndex: job.Step, Policy: job.Policy})
		_, body, err := m.render(ch, rc)
		if err != nil {
			return providerID, fmt.Errorf("template: %w", err)
		}
		if m.SendHook != nil {
			_, err = m.SendHook(ch, ch.Config["url"], "", body)
		} else {
			_, err = m.sendWebhook(ctx, ch, body)
		}
		if err != nil {
			return providerID, err
		}
	}
	return providerID, nil
}

// recordActionEvent leaves the immutable trail for executed actions.
func (m *Manager) recordActionEvent(ctx context.Context, alert *model.Alert,
	ch *model.NotificationChannel, providerID string, step int) {
	rec := model.NotificationRecord{AlertID: alert.ID, StepIndex: step,
		Channel: ch.Type, ChannelID: ch.ID, Status: model.NotifySent,
		ProviderID: providerID}
	raw, _ := json.Marshal(rec)
	ev := &model.Event{ID: model.NewID(), TenantID: alert.TenantID, TS: time.Now().UTC(),
		Type: model.EventNotification, ObjectID: alert.ObjectID,
		Severity: model.SevInfo, Payload: raw}
	m.recordEvent(ctx, ev)
	m.bus.FanoutOnly(ev)
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
	model.ChannelSMS:     `[{{.Severity}}] {{trunc 100 .Title}} ack: {{.AckURL}}`,
	model.ChannelNtfy:    `{{.Title}}`,
	model.ChannelPush:    `{{.Title}}`,
	model.ChannelSlack:   `{"text":"[{{.Severity}}] {{.Title}}","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"*[{{.Severity}}] <{{.AlertURL}}|{{.Title}}>*\nStep {{.Step}} · Policy {{.Policy}}"}},{"type":"actions","elements":[{"type":"button","text":{"type":"plain_text","text":"Acknowledge"},"url":"{{.AckURL}}","style":"primary"}]}]}`,
	model.ChannelTeams:   `{"type":"message","attachments":[{"contentType":"application/vnd.microsoft.card.adaptive","content":{"type":"AdaptiveCard","$schema":"http://adaptivecards.io/schemas/adaptive-card.json","version":"1.4","body":[{"type":"TextBlock","size":"Medium","weight":"Bolder","text":"[{{.Severity}}] {{.Title}}"},{"type":"TextBlock","text":"Step {{.Step}} · Policy {{.Policy}}","wrap":true}],"actions":[{"type":"Action.OpenUrl","title":"Acknowledge","url":"{{.AckURL}}"},{"type":"Action.OpenUrl","title":"Open","url":"{{.AlertURL}}"}]}}]}`,
	model.ChannelWebhook: `{"version":1,"alert":{{json .Alert}},"severity":"{{.Severity}}","title":{{json .Title}},"labels":{{json .Labels}},"step":{{.Step}},"policy":{{json .Policy}},"ackUrl":{{json .AckURL}},"alertUrl":{{json .AlertURL}}}`,
	model.ChannelVoice:   `Northplane alert. Severity {{.Severity}}. {{.Title}}. Press 4 to acknowledge, 6 to resolve.`,
	model.ChannelMQTT:    `{"version":1,"alert":{{json .Alert}},"severity":"{{.Severity}}","title":{{json .Title}},"labels":{{json .Labels}},"ackUrl":{{json .AckURL}},"alertUrl":{{json .AlertURL}}}`,
	// Ticket descriptions (ServiceNow/Zendesk/Jira share the text shape;
	// the generic gateway posts JSON like a webhook).
	model.ChannelServiceNow: ticketDescriptionTemplate,
	model.ChannelZendesk:    ticketDescriptionTemplate,
	model.ChannelJira:       ticketDescriptionTemplate,
	model.ChannelTicket:     `{"subject":{{json .Title}},"severity":"{{.Severity}}","body":{{json .Title}},"labels":{{json .Labels}},"alertUrl":{{json .AlertURL}},"alertId":{{json .Alert.ID}}}`,
}

const ticketDescriptionTemplate = `{{.Severity}}: {{.Title}}

Alert:  {{.AlertURL}}
Opened: {{.Alert.OpenedAt}}
Labels: {{range $k,$v := .Labels}}{{$k}}={{$v}} {{end}}

Acknowledge: {{.AckURL}}`

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
	case model.ChannelNtfy:
		// sendNtfy resolves the server itself (url defaults to ntfy.sh);
		// requiring config.url here failed deliveries whose dialog marks
		// the field optional (NTF-4). The topic is the real target.
		return ch.Config["topic"]
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
	Sent    uint64 `json:"sent"`
	Failed  uint64 `json:"failed"`
	Dead    uint64 `json:"dead"`
	Dropped uint64 `json:"dropped"` // notification events that failed to persist
}

// Stats for self-metrics (notification success/error per channel lives
// in events; this is the coarse counter).
func (m *Manager) Stats() Stats {
	return Stats{Sent: m.statSent.Load(), Failed: m.statFailed.Load(),
		Dead: m.statDead.Load(), Dropped: m.statDropped.Load()}
}

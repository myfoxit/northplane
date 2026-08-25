package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// SendDirect dispatches a single message through a channel outside the
// alert/outbox path (SPEC §9.8 scheduled-report e-mail; any "send this
// exact body now" use). It reuses the private transport dispatch, so
// behaviour is identical to a notification delivery — only the trigger
// differs. An HTML body (starts with "<!doctype" or "<html") is sent as
// text/html by the e-mail transport; everything else as text/plain.
func (m *Manager) SendDirect(ctx context.Context, ch *model.NotificationChannel,
	target, subject, body string) (string, error) {
	if m.SendHook != nil {
		return m.SendHook(ch, target, subject, body)
	}
	return m.send(ctx, ch, target, subject, body, &RenderContext{Title: subject})
}

// SenderFunc is one transport implementation. Register new channel
// types via RegisterSender — the dispatch below is a plain registry so
// transports stay pluggable (SPEC §9.6).
type SenderFunc func(m *Manager, ctx context.Context, ch *model.NotificationChannel,
	target, subject, body string, rc *RenderContext) (string, error)

// senders is the transport registry, keyed by channel type.
var senders = map[model.ChannelType]SenderFunc{
	model.ChannelEmail: func(m *Manager, ctx context.Context, ch *model.NotificationChannel, target, subject, body string, _ *RenderContext) (string, error) {
		return m.sendEmail(ctx, ch, target, subject, body)
	},
	model.ChannelWebhook: func(m *Manager, ctx context.Context, ch *model.NotificationChannel, _, _, body string, _ *RenderContext) (string, error) {
		return m.sendWebhook(ctx, ch, body)
	},
	model.ChannelSlack: func(m *Manager, ctx context.Context, ch *model.NotificationChannel, _, _, body string, _ *RenderContext) (string, error) {
		return m.sendJSONHook(ctx, ch, body)
	},
	model.ChannelTeams: func(m *Manager, ctx context.Context, ch *model.NotificationChannel, _, _, body string, _ *RenderContext) (string, error) {
		return m.sendJSONHook(ctx, ch, body)
	},
	model.ChannelNtfy: func(m *Manager, ctx context.Context, ch *model.NotificationChannel, _, subject, body string, rc *RenderContext) (string, error) {
		return m.sendNtfy(ctx, ch, subject, body, rc)
	},
	model.ChannelSMS: func(m *Manager, ctx context.Context, ch *model.NotificationChannel, target, _, body string, _ *RenderContext) (string, error) {
		return m.sendSMS(ctx, ch, target, body)
	},
	model.ChannelPush: func(m *Manager, ctx context.Context, ch *model.NotificationChannel, target, _, body string, rc *RenderContext) (string, error) {
		return m.sendPush(ctx, ch, target, body, rc)
	},
	model.ChannelVoice: func(m *Manager, ctx context.Context, ch *model.NotificationChannel, target, _, body string, rc *RenderContext) (string, error) {
		return m.sendVoice(ctx, ch, target, body, rc)
	},
	model.ChannelServiceNow: func(m *Manager, ctx context.Context, ch *model.NotificationChannel, _, subject, body string, rc *RenderContext) (string, error) {
		return m.sendTicket(ctx, ch, subject, body, rc, nil)
	},
	model.ChannelZendesk: func(m *Manager, ctx context.Context, ch *model.NotificationChannel, _, subject, body string, rc *RenderContext) (string, error) {
		return m.sendTicket(ctx, ch, subject, body, rc, nil)
	},
	model.ChannelJira: func(m *Manager, ctx context.Context, ch *model.NotificationChannel, _, subject, body string, rc *RenderContext) (string, error) {
		return m.sendTicket(ctx, ch, subject, body, rc, nil)
	},
	model.ChannelTicket: func(m *Manager, ctx context.Context, ch *model.NotificationChannel, _, subject, body string, rc *RenderContext) (string, error) {
		return m.sendTicket(ctx, ch, subject, body, rc, nil)
	},
	model.ChannelMQTT: func(m *Manager, ctx context.Context, ch *model.NotificationChannel, _, _, body string, rc *RenderContext) (string, error) {
		return m.sendMQTT(ctx, ch, body, rc)
	},
}

// RegisterSender adds (or replaces) a transport for a channel type.
func RegisterSender(t model.ChannelType, fn SenderFunc) { senders[t] = fn }

// send dispatches to the concrete transport (SPEC §9.6).
func (m *Manager) send(ctx context.Context, ch *model.NotificationChannel,
	target, subject, body string, rc *RenderContext) (string, error) {
	fn, ok := senders[ch.Type]
	if !ok {
		return "", fmt.Errorf("unsupported channel type %q", ch.Type)
	}
	return fn(m, ctx, ch, target, subject, body, rc)
}

// --- webhook: templated payload + HMAC + retry (handled by outbox) ---

// hookClient is the shared client for all outbound HTTP delivery: webhooks,
// Slack/Teams chat hooks, web-push, voice (Twilio/Asterisk REST) and the
// e-mail provider APIs (Resend/SES). Its dialer refuses link-local and
// cloud-metadata addresses (169.254.0.0/16 incl. 169.254.169.254, and IPv6
// link-local) so a hook target — which can be influenced by lower-privileged
// channel config or message templates — cannot be turned into an SSRF probe
// against the instance metadata service or other link-local endpoints
// (SPEC §13.1). Private RFC1918 ranges stay reachable on purpose: on-prem
// webhooks to internal services are a legitimate, common deployment.
var hookClient = &http.Client{
	Timeout:   20 * time.Second,
	Transport: ssrfGuardedTransport(),
}

func ssrfGuardedTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, _, err := net.SplitHostPort(addr); err == nil {
			if ip := net.ParseIP(host); ip != nil && blockedHookIP(ip) {
				return nil, fmt.Errorf("destination %s blocked (link-local/metadata)", ip)
			}
		}
		conn, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		// DNS-rebinding guard: re-check the resolved peer.
		if ra, ok := conn.RemoteAddr().(*net.TCPAddr); ok && blockedHookIP(ra.IP) {
			_ = conn.Close()
			return nil, fmt.Errorf("resolved destination %s blocked (link-local/metadata)", ra.IP)
		}
		return conn, nil
	}
	return tr
}

// blockedHookIP rejects link-local and cloud-metadata ranges (mirrors the
// HTTP-check SSRF guard in internal/checks; private/internal ranges are
// intentionally still allowed).
func blockedHookIP(ip net.IP) bool {
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 169 && v4[1] == 254 {
		return true // includes 169.254.169.254 metadata
	}
	return false
}

func (m *Manager) sendWebhook(ctx context.Context, ch *model.NotificationChannel, body string) (string, error) {
	u := ch.Config["url"]
	if u == "" {
		return "", fmt.Errorf("webhook channel: config.url required")
	}
	// config.method (the dialog offers it): POST default, small allow-list
	// so a typo cannot turn into an arbitrary verb. GET sends no body.
	method := http.MethodPost
	switch strings.ToUpper(strings.TrimSpace(ch.Config["method"])) {
	case "", "POST":
	case "PUT":
		method = http.MethodPut
	case "PATCH":
		method = http.MethodPatch
	case "GET":
		method = http.MethodGet
	default:
		return "", fmt.Errorf("webhook channel: unsupported method %q (POST, PUT, PATCH or GET)", ch.Config["method"])
	}
	var bodyReader io.Reader
	if method != http.MethodGet {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Northplane-Webhook/1.0")
	// auth modes: basic | token | hmac (F-04.04)
	if user := ch.Config["username"]; user != "" {
		req.SetBasicAuth(user, m.resolveSecret(ch.TenantID, ch.Config["password"]))
	}
	if tok := m.resolveSecret(ch.TenantID, ch.Config["token"]); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if secret := m.resolveSecret(ch.TenantID, ch.Config["secret"]); secret != "" {
		req.Header.Set("X-Northplane-Signature", HMACSign([]byte(secret), []byte(body)))
	}
	resp, err := hookClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) // drain for keep-alive
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("webhook: HTTP %d", resp.StatusCode)
	}
	return resp.Header.Get("X-Request-Id"), nil
}

// sendJSONHook posts pre-rendered JSON (Slack Block Kit / Teams
// Adaptive Cards, SPEC §9.6).
func (m *Manager) sendJSONHook(ctx context.Context, ch *model.NotificationChannel, body string) (string, error) {
	u := ch.Config["url"]
	if u == "" {
		return "", fmt.Errorf("%s channel: config.url required", ch.Type)
	}
	if !json.Valid([]byte(body)) {
		return "", fmt.Errorf("%s template must render valid JSON", ch.Type)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hookClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s: HTTP %d: %s", ch.Type, resp.StatusCode, firstLine(string(respBody)))
	}
	return "", nil
}

// --- ntfy (SPEC §9.6 v1) ---

func (m *Manager) sendNtfy(ctx context.Context, ch *model.NotificationChannel,
	subject, body string, rc *RenderContext) (string, error) {
	server := ch.Config["url"]
	if server == "" {
		server = "https://ntfy.sh"
	}
	topic := ch.Config["topic"]
	if topic == "" {
		return "", fmt.Errorf("ntfy channel: config.topic required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(server, "/")+"/"+url.PathEscape(topic), strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Title", "["+rc.Severity+"] Northplane")
	switch rc.Severity {
	case "CRITICAL":
		req.Header.Set("Priority", "urgent")
		req.Header.Set("Tags", "rotating_light")
	case "WARNING":
		req.Header.Set("Priority", "high")
		req.Header.Set("Tags", "warning")
	}
	if rc.AckURL != "" {
		req.Header.Set("Actions", "view, Acknowledge, "+rc.AckURL)
	}
	if tok := m.resolveSecret(ch.TenantID, ch.Config["token"]); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := hookClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("ntfy: HTTP %d", resp.StatusCode)
	}
	return "", nil
}

// --- SMS: provider abstraction (SPEC §9.6 — one provider in v1 plus a
// generic HTTP gateway covering SMSEagle/Teltonika-style devices) ---

func (m *Manager) sendSMS(ctx context.Context, ch *model.NotificationChannel, to, text string) (string, error) {
	switch ch.Config["provider"] {
	case "twilio":
		return m.sendTwilioSMS(ctx, ch, to, text)
	case "generic-http", "":
		return m.sendGenericSMS(ctx, ch, to, text)
	default:
		return "", fmt.Errorf("sms provider %q not supported (twilio | generic-http)", ch.Config["provider"])
	}
}

func (m *Manager) sendTwilioSMS(ctx context.Context, ch *model.NotificationChannel, to, text string) (string, error) {
	sid := ch.Config["accountSid"]
	from := ch.Config["from"]
	user, pass := m.twilioCreds(ch)
	if sid == "" || user == "" || pass == "" || from == "" {
		return "", fmt.Errorf("twilio: accountSid, authToken (or apiKeySid+apiKeySecret), from required")
	}
	form := url.Values{"To": {to}, "From": {from}, "Body": {text}}
	apiBase := strings.TrimSuffix(ch.Config["apiBase"], "/")
	if apiBase == "" {
		apiBase = "https://api.twilio.com"
	}
	endpoint := apiBase + "/2010-04-01/Accounts/" + sid + "/Messages.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(user, pass)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hookClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("twilio: HTTP %d: %s", resp.StatusCode, firstLine(string(body)))
	}
	var out struct {
		Sid string `json:"sid"`
	}
	_ = json.Unmarshal(body, &out)
	return out.Sid, nil
}

// sendGenericSMS drives HTTP gateways: config.url with {to}/{text}
// placeholders (GET) or config.jsonBody template (POST).
func (m *Manager) sendGenericSMS(ctx context.Context, ch *model.NotificationChannel, to, text string) (string, error) {
	u := ch.Config["url"]
	if u == "" {
		return "", fmt.Errorf("generic-http sms: config.url required")
	}
	method := http.MethodGet
	var bodyReader io.Reader
	if tpl := ch.Config["jsonBody"]; tpl != "" {
		method = http.MethodPost
		body := strings.NewReplacer("{to}", to, "{text}", text).Replace(tpl)
		bodyReader = strings.NewReader(body)
	} else {
		u = strings.NewReplacer("{to}", url.QueryEscape(to),
			"{text}", url.QueryEscape(text)).Replace(u)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return "", err
	}
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if user := ch.Config["username"]; user != "" {
		req.SetBasicAuth(user, m.resolveSecret(ch.TenantID, ch.Config["password"]))
	}
	resp, err := hookClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) // drain for keep-alive
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("sms gateway: HTTP %d", resp.StatusCode)
	}
	return "", nil
}

// --- outgoing webhook subscriptions (SPEC §11.5) ---

func (m *Manager) deliverWebhookSub(ctx context.Context, item *storage.OutboxItem) (string, error) {
	var payload struct {
		URL    string          `json:"url"`
		Secret string          `json:"secret"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, payload.URL,
		bytes.NewReader(payload.Body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if payload.Secret != "" {
		req.Header.Set("X-Northplane-Signature",
			HMACSign([]byte(m.resolveSecret(item.TenantID, payload.Secret)), payload.Body))
	}
	resp, err := hookClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) // drain for keep-alive
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("subscription webhook: HTTP %d", resp.StatusCode)
	}
	return "", nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.TrimSpace(s)
}

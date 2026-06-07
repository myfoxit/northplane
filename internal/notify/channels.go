package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
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

// send dispatches to the concrete transport (SPEC §9.6).
func (m *Manager) send(ctx context.Context, ch *model.NotificationChannel,
	target, subject, body string, rc *RenderContext) (string, error) {
	switch ch.Type {
	case model.ChannelEmail:
		return m.sendEmail(ctx, ch, target, subject, body)
	case model.ChannelWebhook:
		return m.sendWebhook(ctx, ch, body)
	case model.ChannelSlack, model.ChannelTeams:
		return m.sendJSONHook(ctx, ch, body)
	case model.ChannelNtfy:
		return m.sendNtfy(ctx, ch, subject, body, rc)
	case model.ChannelSMS:
		return m.sendSMS(ctx, ch, target, body)
	case model.ChannelPush:
		return m.sendPush(ctx, ch, target, body, rc)
	case model.ChannelVoice:
		return "", fmt.Errorf("voice provider integration is v2 (SPEC §9.6) — configure SMS/Push for now")
	default:
		return "", fmt.Errorf("unsupported channel type %q", ch.Type)
	}
}

// --- e-mail: native SMTP client (STARTTLS/implicit, SPEC §9.6) ---

func (m *Manager) sendEmail(ctx context.Context, ch *model.NotificationChannel,
	to, subject, body string) (string, error) {
	host := ch.Config["host"]
	if host == "" {
		return "", fmt.Errorf("email channel: config.host required")
	}
	port := ch.Config["port"]
	if port == "" {
		port = "587"
	}
	from := ch.Config["from"]
	if from == "" {
		from = "northplane@" + host
	}
	user := ch.Config["username"]
	pass := m.resolveSecret(ch.TenantID, ch.Config["password"])

	// HTML bodies (scheduled-report mail, §9.8) are detected by their
	// leading tag and sent as text/html; alert bodies stay text/plain.
	contentType := "text/plain; charset=utf-8"
	if isHTMLBody(body) {
		contentType = "text/html; charset=utf-8"
	}

	msg := &bytes.Buffer{}
	fmt.Fprintf(msg, "From: %s\r\n", from)
	fmt.Fprintf(msg, "To: %s\r\n", to)
	fmt.Fprintf(msg, "Subject: %s\r\n", mimeHeader(subject))
	fmt.Fprintf(msg, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(msg, "Message-ID: <%d.northplane@%s>\r\n", time.Now().UnixNano(), host)
	fmt.Fprintf(msg, "MIME-Version: 1.0\r\nContent-Type: %s\r\n\r\n", contentType)
	msg.WriteString(body)

	addr := net.JoinHostPort(host, port)
	implicit := port == "465" || ch.Config["tls"] == "implicit"

	dial := func() (*smtp.Client, error) {
		d := net.Dialer{Timeout: 15 * time.Second}
		if implicit {
			conn, err := tls.DialWithDialer(&d, "tcp", addr, &tls.Config{ServerName: host})
			if err != nil {
				return nil, err
			}
			return smtp.NewClient(conn, host)
		}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			return nil, err
		}
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
				c.Close()
				return nil, err
			}
		} else if ch.Config["allowPlaintext"] != "true" {
			c.Close()
			return nil, fmt.Errorf("server offers no STARTTLS (set allowPlaintext=true to override)")
		}
		return c, nil
	}
	c, err := dial()
	if err != nil {
		return "", fmt.Errorf("smtp connect: %w", err)
	}
	defer c.Close()
	if user != "" {
		if err := c.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
			return "", fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return "", err
	}
	if err := c.Rcpt(to); err != nil {
		return "", err
	}
	w, err := c.Data()
	if err != nil {
		return "", err
	}
	if _, err := w.Write(msg.Bytes()); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return "", c.Quit()
}

func mimeHeader(s string) string {
	if isASCII(s) {
		return s
	}
	return "=?UTF-8?B?" + base64Std(s) + "?="
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

// isHTMLBody reports whether a body should be sent as text/html, by its
// leading tag (case-insensitive, after whitespace).
func isHTMLBody(body string) bool {
	t := strings.TrimSpace(body)
	if len(t) > 16 {
		t = t[:16]
	}
	t = strings.ToLower(t)
	return strings.HasPrefix(t, "<!doctype html") || strings.HasPrefix(t, "<html")
}

// --- webhook: templated payload + HMAC + retry (handled by outbox) ---

var hookClient = &http.Client{Timeout: 20 * time.Second}

func (m *Manager) sendWebhook(ctx context.Context, ch *model.NotificationChannel, body string) (string, error) {
	u := ch.Config["url"]
	if u == "" {
		return "", fmt.Errorf("webhook channel: config.url required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(body))
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
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
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
	token := m.resolveSecret(ch.TenantID, ch.Config["authToken"])
	from := ch.Config["from"]
	if sid == "" || token == "" || from == "" {
		return "", fmt.Errorf("twilio: accountSid, authToken, from required")
	}
	form := url.Values{"To": {to}, "From": {from}, "Body": {text}}
	endpoint := "https://api.twilio.com/2010-04-01/Accounts/" + sid + "/Messages.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(sid, token)
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
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
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
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
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

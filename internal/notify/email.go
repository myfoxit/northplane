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
	"net/mail"
	"net/smtp"
	"os/exec"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/awssig"
	"github.com/northplane/northplane/internal/model"
)

// E-mail transport with pluggable providers (SPEC §9.6). config.provider
// selects the delivery backend; "smtp" stays the default so existing
// channels keep working unchanged:
//
//	smtp     — native SMTP client, STARTTLS/implicit TLS (default). Covers
//	           any relay incl. Postfix, Exim, AWS SES SMTP, Mailgun SMTP.
//	sendmail — pipe through the local MTA binary (Postfix/Exim/msmtp
//	           sendmail(8) interface); no network config needed.
//	resend   — Resend HTTP API (https://resend.com).
//	ses      — AWS SES v2 HTTP API with SigV4 request signing.
func (m *Manager) sendEmail(ctx context.Context, ch *model.NotificationChannel,
	to, subject, body string) (string, error) {
	switch ch.Config["provider"] {
	case "", "smtp":
		return m.sendEmailSMTP(ctx, ch, to, subject, body)
	case "sendmail":
		return m.sendEmailSendmail(ctx, ch, to, subject, body)
	case "resend":
		return m.sendEmailResend(ctx, ch, to, subject, body)
	case "ses":
		return m.sendEmailSES(ctx, ch, to, subject, body)
	default:
		return "", fmt.Errorf("email provider %q not supported (smtp | sendmail | resend | ses)", ch.Config["provider"])
	}
}

// buildEmailMessage renders the RFC 5322 message used by the smtp and
// sendmail providers. HTML bodies (scheduled-report mail, §9.8) are
// detected by their leading tag and sent as text/html; alert bodies stay
// text/plain.
func buildEmailMessage(from, to, subject, body, idDomain string) []byte {
	contentType := "text/plain; charset=utf-8"
	if isHTMLBody(body) {
		contentType = "text/html; charset=utf-8"
	}
	// Subject/recipient/sender are derived from check output and templates
	// (untrusted): strip CR/LF so an attacker cannot inject extra headers or
	// a forged body via the Subject line (CWE-93 header injection). mimeHeader
	// then RFC 2047-encodes any non-ASCII subject.
	from = sanitizeHeader(from)
	to = sanitizeHeader(to)
	subject = sanitizeHeader(subject)
	idDomain = sanitizeHeader(idDomain)
	msg := &bytes.Buffer{}
	fmt.Fprintf(msg, "From: %s\r\n", from)
	fmt.Fprintf(msg, "To: %s\r\n", to)
	fmt.Fprintf(msg, "Subject: %s\r\n", mimeHeader(subject))
	fmt.Fprintf(msg, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(msg, "Message-ID: <%d.northplane@%s>\r\n", time.Now().UnixNano(), idDomain)
	fmt.Fprintf(msg, "MIME-Version: 1.0\r\nContent-Type: %s\r\n\r\n", contentType)
	msg.WriteString(body)
	return msg.Bytes()
}

// sanitizeHeader removes CR, LF and other C0 control bytes (except TAB) from
// an e-mail header value so untrusted input cannot smuggle additional headers
// or split the message (CWE-93). Folding of legitimately long values and
// non-ASCII encoding are handled by mimeHeader.
func sanitizeHeader(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return -1
		}
		if r < 0x20 && r != '\t' {
			return -1
		}
		return r
	}, s)
}

// --- smtp: native SMTP client (STARTTLS/implicit, SPEC §9.6) ---

// heloName is the EHLO identity for outbound SMTP: config.helo wins,
// otherwise the domain of the from address. Go's client would announce
// "localhost", which strict receivers (direct-to-MX delivery) answer
// with 550 or a capability list without STARTTLS.
func heloName(cfg map[string]string, from string) string {
	if h := cfg["helo"]; h != "" {
		return h
	}
	if a, err := mail.ParseAddress(from); err == nil {
		if i := strings.LastIndexByte(a.Address, '@'); i >= 0 {
			return a.Address[i+1:]
		}
	}
	return "localhost"
}

// envelopeAddr reduces "Display Name <box@dom>" to box@dom — MAIL
// FROM/RCPT TO take the bare address, the display form stays in the
// message headers.
func envelopeAddr(s string) string {
	if a, err := mail.ParseAddress(s); err == nil {
		return a.Address
	}
	return s
}

func (m *Manager) sendEmailSMTP(ctx context.Context, ch *model.NotificationChannel,
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

	msg := buildEmailMessage(from, to, subject, body, host)

	addr := net.JoinHostPort(host, port)
	implicit := port == "465" || ch.Config["tls"] == "implicit"
	helo := heloName(ch.Config, from)

	dial := func() (*smtp.Client, error) {
		d := net.Dialer{Timeout: 15 * time.Second}
		if implicit {
			conn, err := tls.DialWithDialer(&d, "tcp", addr, &tls.Config{ServerName: host})
			if err != nil {
				return nil, err
			}
			c, err := smtp.NewClient(conn, host)
			if err != nil {
				return nil, err
			}
			if err := c.Hello(helo); err != nil {
				_ = c.Close()
				return nil, err
			}
			return c, nil
		}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			return nil, err
		}
		if err := c.Hello(helo); err != nil {
			_ = c.Close()
			return nil, err
		}
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
				_ = c.Close()
				return nil, err
			}
		} else if ch.Config["allowPlaintext"] != "true" {
			_ = c.Close()
			return nil, fmt.Errorf("server offers no STARTTLS (set allowPlaintext=true to override)")
		}
		return c, nil
	}
	c, err := dial()
	if err != nil {
		return "", fmt.Errorf("smtp connect: %w", err)
	}
	defer func() { _ = c.Close() }()
	if user != "" {
		if err := c.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
			return "", fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(envelopeAddr(from)); err != nil {
		return "", err
	}
	if err := c.Rcpt(envelopeAddr(to)); err != nil {
		return "", err
	}
	w, err := c.Data()
	if err != nil {
		return "", err
	}
	if _, err := w.Write(msg); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return "", c.Quit()
}

// --- sendmail: local MTA via the sendmail(8) interface ---

// sendmailSearchPath lists the conventional sendmail locations; PATH is
// consulted first so msmtp-style wrappers win when installed.
var sendmailSearchPath = []string{"/usr/sbin/sendmail", "/usr/lib/sendmail", "/usr/bin/sendmail"}

func (m *Manager) sendEmailSendmail(ctx context.Context, ch *model.NotificationChannel,
	to, subject, body string) (string, error) {
	path := ch.Config["sendmailPath"]
	if path == "" {
		if p, err := exec.LookPath("sendmail"); err == nil {
			path = p
		} else {
			for _, cand := range sendmailSearchPath {
				if _, err := exec.LookPath(cand); err == nil {
					path = cand
					break
				}
			}
		}
		if path == "" {
			return "", fmt.Errorf("sendmail binary not found (set config.sendmailPath)")
		}
	}
	from := ch.Config["from"]
	if from == "" {
		from = "northplane@localhost"
	}
	// -i: don't end on a lone "." line; -f: envelope sender; "--" guards
	// against recipient values being parsed as flags.
	args := []string{"-i", "-f", from, "--", to}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = bytes.NewReader(buildEmailMessage(from, to, subject, body, "localhost"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("sendmail: %w: %s", err, firstLine(string(out)))
	}
	return "", nil
}

// --- resend: https://resend.com HTTP API ---

func (m *Manager) sendEmailResend(ctx context.Context, ch *model.NotificationChannel,
	to, subject, body string) (string, error) {
	apiKey := m.resolveSecret(ch.TenantID, ch.Config["apiKey"])
	if apiKey == "" {
		return "", fmt.Errorf("resend: config.apiKey required")
	}
	from := ch.Config["from"]
	if from == "" {
		return "", fmt.Errorf("resend: config.from required (verified sender)")
	}
	payload := map[string]any{
		"from":    from,
		"to":      []string{to},
		"subject": subject,
	}
	if isHTMLBody(body) {
		payload["html"] = body
	} else {
		payload["text"] = body
	}
	apiBase := strings.TrimSuffix(ch.Config["apiBase"], "/")
	if apiBase == "" {
		apiBase = "https://api.resend.com"
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/emails", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := hookClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("resend: HTTP %d: %s", resp.StatusCode, firstLine(string(respBody)))
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(respBody, &out)
	return out.ID, nil
}

// --- ses: AWS SES v2 HTTP API with SigV4 signing ---

func (m *Manager) sendEmailSES(ctx context.Context, ch *model.NotificationChannel,
	to, subject, body string) (string, error) {
	region := ch.Config["region"]
	accessKey := ch.Config["accessKeyId"]
	secretKey := m.resolveSecret(ch.TenantID, ch.Config["secretAccessKey"])
	from := ch.Config["from"]
	if region == "" || accessKey == "" || secretKey == "" || from == "" {
		return "", fmt.Errorf("ses: region, accessKeyId, secretAccessKey, from required")
	}
	bodyKey := "Text"
	if isHTMLBody(body) {
		bodyKey = "Html"
	}
	payload := map[string]any{
		"FromEmailAddress": from,
		"Destination":      map[string]any{"ToAddresses": []string{to}},
		"Content": map[string]any{
			"Simple": map[string]any{
				"Subject": map[string]any{"Data": subject, "Charset": "UTF-8"},
				"Body": map[string]any{
					bodyKey: map[string]any{"Data": body, "Charset": "UTF-8"},
				},
			},
		},
	}
	endpoint := strings.TrimSuffix(ch.Config["endpoint"], "/")
	if endpoint == "" {
		endpoint = "https://email." + region + ".amazonaws.com"
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		endpoint+"/v2/email/outbound-emails", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	signAWSV4(req, raw, awsCredentials{
		AccessKey:    accessKey,
		SecretKey:    secretKey,
		SessionToken: m.resolveSecret(ch.TenantID, ch.Config["sessionToken"]),
		Region:       region,
		Service:      "ses",
	}, time.Now().UTC())
	resp, err := hookClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("ses: HTTP %d: %s", resp.StatusCode, firstLine(string(respBody)))
	}
	var out struct {
		MessageID string `json:"MessageId"`
	}
	_ = json.Unmarshal(respBody, &out)
	return out.MessageID, nil
}

// awsCredentials is the SigV4 credential set (shared signer in
// internal/awssig; kept as an alias so call sites and tests read naturally).
type awsCredentials = awssig.Credentials

// signAWSV4 adds an AWS Signature Version 4 Authorization header.
func signAWSV4(req *http.Request, payload []byte, cred awsCredentials, now time.Time) {
	awssig.Sign(req, payload, cred, now)
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

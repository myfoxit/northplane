package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"time"

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

type awsCredentials struct {
	AccessKey    string
	SecretKey    string
	SessionToken string
	Region       string
	Service      string
}

// signAWSV4 adds an AWS Signature Version 4 Authorization header
// (https://docs.aws.amazon.com/IAM/latest/UserGuide/create-signed-request.html).
// Hand-rolled to keep the dependency tree free of the AWS SDK; SES is the
// only AWS surface and needs exactly one signed POST.
func signAWSV4(req *http.Request, payload []byte, cred awsCredentials, now time.Time) {
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := hexSHA256(payload)

	req.Header.Set("X-Amz-Date", amzDate)
	if cred.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", cred.SessionToken)
	}

	headers := map[string]string{
		"host":       req.Host,
		"x-amz-date": amzDate,
	}
	if req.Host == "" {
		headers["host"] = req.URL.Host
	}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		headers["content-type"] = ct
	}
	if cred.SessionToken != "" {
		headers["x-amz-security-token"] = cred.SessionToken
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + strings.TrimSpace(headers[k]) + "\n")
	}
	signedHeaders := strings.Join(names, ";")

	canonURI := req.URL.EscapedPath()
	if canonURI == "" {
		canonURI = "/"
	}
	canonQuery := canonicalQuery(req.URL.Query())
	canonRequest := strings.Join([]string{
		req.Method, canonURI, canonQuery, canonHeaders.String(), signedHeaders, payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, cred.Region, cred.Service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hexSHA256([]byte(canonRequest)),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+cred.SecretKey), dateStamp)
	kRegion := hmacSHA256(kDate, cred.Region)
	kService := hmacSHA256(kRegion, cred.Service)
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		cred.AccessKey, scope, signedHeaders, signature))
}

// canonicalQuery renders the query string per SigV4: keys sorted, values
// URI-encoded with %20 for space.
func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, awsURIEncode(k)+"="+awsURIEncode(v))
		}
	}
	return strings.Join(parts, "&")
}

func awsURIEncode(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

func hexSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
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

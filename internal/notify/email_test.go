package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
)

func emailChannel(provider string, cfg map[string]string) *model.NotificationChannel {
	if cfg == nil {
		cfg = map[string]string{}
	}
	if provider != "" {
		cfg["provider"] = provider
	}
	return &model.NotificationChannel{
		TenantID: model.DefaultTenant, Name: "mail", Type: model.ChannelEmail,
		Enabled: true, Config: cfg,
	}
}

func TestSendEmailUnknownProvider(t *testing.T) {
	m := &Manager{}
	_, err := m.sendEmail(context.Background(), emailChannel("pigeon", nil), "a@b.c", "s", "b")
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("want unsupported-provider error, got %v", err)
	}
}

// TestSigV4KnownVector pins the signer to the AWS-documented example
// (IAM ListUsers, AKIDEXAMPLE credentials, 2015-08-30T12:36:00Z).
func TestSigV4KnownVector(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet,
		"https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	at, _ := time.Parse(time.RFC3339, "2015-08-30T12:36:00Z")
	signAWSV4(req, nil, awsCredentials{
		AccessKey: "AKIDEXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
		Region:    "us-east-1",
		Service:   "iam",
	}, at)
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, " +
		"SignedHeaders=content-type;host;x-amz-date, " +
		"Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("sigv4 mismatch:\n got %s\nwant %s", got, want)
	}
}

func TestSendEmailResend(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emails" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "re_123"})
	}))
	defer srv.Close()

	m := &Manager{}
	id, err := m.sendEmail(context.Background(), emailChannel("resend", map[string]string{
		"apiKey": "re_test_key", "from": "alerts@example.com", "apiBase": srv.URL,
	}), "ops@example.com", "CPU high", "<html><b>alert</b></html>")
	if err != nil {
		t.Fatal(err)
	}
	if id != "re_123" {
		t.Fatalf("provider id = %q, want re_123", id)
	}
	if gotAuth != "Bearer re_test_key" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotBody["from"] != "alerts@example.com" || gotBody["subject"] != "CPU high" {
		t.Fatalf("payload = %v", gotBody)
	}
	if _, ok := gotBody["html"]; !ok {
		t.Fatalf("HTML body should be sent as html field, got %v", gotBody)
	}
	to, _ := gotBody["to"].([]any)
	if len(to) != 1 || to[0] != "ops@example.com" {
		t.Fatalf("to = %v", gotBody["to"])
	}
}

func TestSendEmailResendMissingConfig(t *testing.T) {
	m := &Manager{}
	if _, err := m.sendEmail(context.Background(),
		emailChannel("resend", map[string]string{"from": "a@b.c"}), "x@y.z", "s", "b"); err == nil {
		t.Fatal("want apiKey-required error")
	}
	if _, err := m.sendEmail(context.Background(),
		emailChannel("resend", map[string]string{"apiKey": "k"}), "x@y.z", "s", "b"); err == nil {
		t.Fatal("want from-required error")
	}
}

func TestSendEmailSES(t *testing.T) {
	var gotAuth, gotDate, gotToken string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/email/outbound-emails" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotDate = r.Header.Get("X-Amz-Date")
		gotToken = r.Header.Get("X-Amz-Security-Token")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]string{"MessageId": "ses-msg-1"})
	}))
	defer srv.Close()

	m := &Manager{Secrets: func(_, name string) (string, bool) {
		if name == "ses-secret" {
			return "resolved-secret-key", true
		}
		return "", false
	}}
	id, err := m.sendEmail(context.Background(), emailChannel("ses", map[string]string{
		"region": "eu-central-1", "accessKeyId": "AKIDTEST",
		"secretAccessKey": "$SECRET:ses-secret$", "sessionToken": "tok-123",
		"from": "alerts@example.com", "endpoint": srv.URL,
	}), "ops@example.com", "Disk full", "plain text body")
	if err != nil {
		t.Fatal(err)
	}
	if id != "ses-msg-1" {
		t.Fatalf("provider id = %q", id)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AKIDTEST/") ||
		!strings.Contains(gotAuth, "/eu-central-1/ses/aws4_request") {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if !strings.Contains(gotAuth, "SignedHeaders=content-type;host;x-amz-date;x-amz-security-token") {
		t.Fatalf("signed headers missing: %q", gotAuth)
	}
	if gotDate == "" || gotToken != "tok-123" {
		t.Fatalf("date=%q token=%q", gotDate, gotToken)
	}
	if gotBody["FromEmailAddress"] != "alerts@example.com" {
		t.Fatalf("payload = %v", gotBody)
	}
	content := gotBody["Content"].(map[string]any)["Simple"].(map[string]any)
	if content["Subject"].(map[string]any)["Data"] != "Disk full" {
		t.Fatalf("subject payload = %v", content)
	}
	if _, ok := content["Body"].(map[string]any)["Text"]; !ok {
		t.Fatalf("plain body should be Text, got %v", content["Body"])
	}
}

func TestSendEmailSESMissingConfig(t *testing.T) {
	m := &Manager{}
	if _, err := m.sendEmail(context.Background(),
		emailChannel("ses", map[string]string{"region": "eu-central-1"}), "x@y.z", "s", "b"); err == nil {
		t.Fatal("want missing-config error")
	}
}

func TestSendEmailSendmail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake sendmail")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "capture")
	script := filepath.Join(dir, "sendmail")
	if err := os.WriteFile(script, []byte(
		"#!/bin/sh\necho \"$@\" > \"$NP_TEST_OUT.args\"\ncat > \"$NP_TEST_OUT.msg\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NP_TEST_OUT", out)

	m := &Manager{}
	_, err := m.sendEmail(context.Background(), emailChannel("sendmail", map[string]string{
		"sendmailPath": script, "from": "np@example.com",
	}), "ops@example.com", "Hello", "body line")
	if err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(out + ".args")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(args)); got != "-i -f np@example.com -- ops@example.com" {
		t.Fatalf("sendmail args = %q", got)
	}
	msg, err := os.ReadFile(out + ".msg")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"From: np@example.com", "To: ops@example.com", "Subject: Hello", "body line"} {
		if !strings.Contains(string(msg), want) {
			t.Fatalf("message missing %q:\n%s", want, msg)
		}
	}
}

func TestSendEmailSendmailFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake sendmail")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "sendmail")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'queue full' >&2\nexit 75\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := &Manager{}
	_, err := m.sendEmail(context.Background(), emailChannel("sendmail",
		map[string]string{"sendmailPath": script}), "ops@example.com", "s", "b")
	if err == nil || !strings.Contains(err.Error(), "queue full") {
		t.Fatalf("want sendmail failure with stderr, got %v", err)
	}
}

// fakeSMTP runs a single-connection plaintext SMTP server and returns
// its address plus a channel delivering the received DATA payload.
func fakeSMTP(t *testing.T) (string, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		write := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		write("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if inData {
				if line == "." {
					inData = false
					got <- data.String()
					write("250 ok queued")
					continue
				}
				data.WriteString(line + "\n")
				continue
			}
			switch {
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				write("250-fake")
				write("250 SIZE 1048576")
			case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
				write("250 ok")
			case strings.HasPrefix(line, "DATA"):
				inData = true
				write("354 go ahead")
			case strings.HasPrefix(line, "QUIT"):
				write("221 bye")
				return
			default:
				write("250 ok")
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestSendEmailSMTPPlaintext(t *testing.T) {
	addr, got := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)

	m := &Manager{}
	_, err := m.sendEmail(context.Background(), emailChannel("", map[string]string{
		"host": host, "port": port, "from": "np@example.com", "allowPlaintext": "true",
	}), "ops@example.com", "Müller äöü", "smtp body")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-got:
		if !strings.Contains(msg, "To: ops@example.com") || !strings.Contains(msg, "smtp body") {
			t.Fatalf("unexpected message:\n%s", msg)
		}
		// non-ASCII subject must be MIME-encoded
		if !strings.Contains(msg, "Subject: =?UTF-8?B?") {
			t.Fatalf("subject not MIME-encoded:\n%s", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no message received")
	}
}

func TestSendEmailSMTPRefusesPlaintextByDefault(t *testing.T) {
	addr, _ := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	m := &Manager{}
	_, err := m.sendEmail(context.Background(), emailChannel("smtp", map[string]string{
		"host": host, "port": port,
	}), "ops@example.com", "s", "b")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("want STARTTLS refusal, got %v", err)
	}
}

package notify

import (
	"net"
	"strings"
	"testing"
)

// TestEmailHeaderInjection ensures untrusted subject/recipient text cannot
// smuggle extra headers or a forged body via CR/LF (CWE-93). The malicious
// payload must end up on a single header line, not split the message.
func TestEmailHeaderInjection(t *testing.T) {
	evilSubject := "Disk full\r\nBcc: attacker@evil.example\r\n\r\nInjected body"
	evilTo := "ops@example.com\r\nCc: leak@evil.example"
	msg := string(buildEmailMessage("np@example.com", evilTo, evilSubject,
		"legit body", "example.com"))

	headers, body, _ := strings.Cut(msg, "\r\n\r\n")
	// The attacker's "Bcc:"/"Cc:" text may survive as literal Subject text,
	// but must never become a real header line (start of a line).
	for _, line := range strings.Split(headers, "\r\n") {
		if strings.HasPrefix(line, "Bcc:") || strings.HasPrefix(line, "Cc:") {
			t.Fatalf("header injection not sanitized — got header line %q", line)
		}
	}
	// The body must be only the legitimate content, not the injected text.
	if strings.TrimSpace(body) != "legit body" {
		t.Fatalf("body splitting not prevented; body=%q", body)
	}
	// The Subject line keeps its visible text (minus the CR/LF).
	if !strings.Contains(headers, "Subject: Disk full") {
		t.Fatalf("subject text lost: %s", headers)
	}
}

// TestBlockedHookIP pins the notification-webhook SSRF policy: link-local and
// cloud-metadata targets are refused, private/public stay reachable (on-prem
// webhooks to internal services are legitimate).
func TestBlockedHookIP(t *testing.T) {
	for _, s := range []string{"169.254.169.254", "169.254.1.1", "fe80::1"} {
		if !blockedHookIP(net.ParseIP(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range []string{"10.1.2.3", "192.168.0.5", "203.0.113.10"} {
		if blockedHookIP(net.ParseIP(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

// TestHookClientRefusesMetadata proves the shared client's dialer blocks the
// metadata IP before any connection is attempted.
func TestHookClientRefusesMetadata(t *testing.T) {
	_, err := hookClient.Get("http://169.254.169.254/latest/meta-data/")
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected SSRF block error, got %v", err)
	}
}

package checks

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/northplane/northplane/internal/model"
)

// hostPort splits an httptest/listener address into host and numeric port.
func hostPort(t *testing.T, raw string) (string, string) {
	t.Helper()
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		raw = u.Host
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		t.Fatalf("split %q: %v", raw, err)
	}
	return host, port
}

func TestCheckHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/boom" {
			http.Error(w, "kaboom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("hello northplane world"))
	}))
	defer srv.Close()
	host, port := hostPort(t, srv.URL)
	tgt := Target{Address: host}
	ctx := context.Background()

	cases := []struct {
		name string
		args []string
		want model.State
	}{
		{"200 ok", []string{"-p", port, "-u", "/"}, model.StateOK},
		{"500 critical", []string{"-p", port, "-u", "/boom"}, model.StateCritical},
		{"expect 200 matches", []string{"-p", port, "-u", "/", "-e", "200"}, model.StateOK},
		{"expect 404 mismatch", []string{"-p", port, "-u", "/", "-e", "404"}, model.StateCritical},
		{"body string found", []string{"-p", port, "-u", "/", "-s", "northplane"}, model.StateOK},
		{"body string missing", []string{"-p", port, "-u", "/", "-s", "absent-token"}, model.StateCritical},
		{"body regex found", []string{"-p", port, "-u", "/", "-r", "hello .* world"}, model.StateOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, out := Run(ctx, "http", tgt, c.args)
			if st != c.want {
				t.Fatalf("state = %d (%s), want %d", st, out.Text, c.want)
			}
		})
	}
}

func TestCheckHTTPConnRefused(t *testing.T) {
	// Bind then immediately release a port so nothing is listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	ln.Close()

	st, out := Run(context.Background(), "http", Target{Address: "127.0.0.1"},
		[]string{"-p", port, "-u", "/"})
	if st != model.StateCritical {
		t.Fatalf("connection refused should be CRITICAL, got %d (%s)", st, out.Text)
	}
}

func TestCheckTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, port := hostPort(t, ln.Addr().String())

	st, out := Run(context.Background(), "tcp", Target{Address: "127.0.0.1"}, []string{"-p", port})
	if st != model.StateOK {
		t.Fatalf("open port should be OK, got %d (%s)", st, out.Text)
	}

	// A port with nothing listening is CRITICAL.
	ln2, _ := net.Listen("tcp", "127.0.0.1:0")
	closedPort := strconv.Itoa(ln2.Addr().(*net.TCPAddr).Port)
	ln2.Close()
	st, _ = Run(context.Background(), "tcp", Target{Address: "127.0.0.1"}, []string{"-p", closedPort})
	if st != model.StateCritical {
		t.Fatalf("closed port should be CRITICAL, got %d", st)
	}

	// Missing -p is UNKNOWN (operator error, not a down service).
	if st, _ := Run(context.Background(), "tcp", Target{Address: "127.0.0.1"}, nil); st != model.StateUnknown {
		t.Fatalf("missing port should be UNKNOWN, got %d", st)
	}
}

// TestBlockedIP pins the SSRF policy: link-local + cloud-metadata are
// blocked, ordinary public and private (RFC1918) addresses are allowed so
// on-prem monitoring keeps working.
func TestBlockedIP(t *testing.T) {
	blocked := []string{"169.254.169.254", "169.254.0.1", "fe80::1"}
	for _, s := range blocked {
		if !blockedIP(net.ParseIP(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	allowed := []string{"10.0.0.1", "192.168.1.1", "172.16.5.5", "8.8.8.8", "127.0.0.1"}
	for _, s := range allowed {
		if blockedIP(net.ParseIP(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

package notify

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// Asterisk voice provider (SPEC §9.6): originates a call over AMI
// (Asterisk Manager Interface) into a dialplan context the operator
// controls — the on-prem-first option for voice alerting. The dialplan
// owns TTS/playback and may acknowledge the alert by hitting the signed
// NP_ACK_URL (curl from the dialplan after a DTMF confirm), exactly like
// the Twilio gather flow but without any cloud dependency.
//
// Channel config:
//
//	host       AMI host (required)
//	port       AMI port, default 5038
//	username   AMI manager user (required)
//	secret     AMI manager secret, $SECRET ref supported (required)
//	channel    originate channel template with {to}, e.g. "PJSIP/{to}@trunk" (required)
//	context    dialplan context, default "northplane-alert"
//	exten      dialplan extension, default "s"
//	priority   dialplan priority, default "1"
//	application/appData
//	           alternative to context/exten: run one application
//	           (e.g. application=Playback, appData=alert-sound)
//	callerId   optional caller id, e.g. "Northplane <8000>"
//	timeoutMs  ring timeout passed to Originate, default 30000
//	tls        "on" = AMI over TLS (Asterisk tlsenable, port 5039)
//	insecure   "true" = skip TLS certificate verification
//
// The rendered alert text and ack URL travel as channel variables
// (NP_TEXT, NP_SEVERITY, NP_ACK_URL) so the dialplan can speak and act
// on them.
func (m *Manager) sendAsteriskVoice(ctx context.Context, ch *model.NotificationChannel,
	to, text string, rc *RenderContext) (string, error) {
	host := ch.Config["host"]
	user := ch.Config["username"]
	secret := m.resolveSecret(ch.TenantID, ch.Config["secret"])
	channelTpl := ch.Config["channel"]
	if host == "" || user == "" || secret == "" || channelTpl == "" {
		return "", fmt.Errorf("asterisk voice: host, username, secret, channel required")
	}
	port := ch.Config["port"]
	if port == "" {
		port = "5038"
	}

	conn, err := dialAMI(ctx, ch, net.JoinHostPort(host, port))
	if err != nil {
		return "", fmt.Errorf("asterisk voice: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	}
	r := bufio.NewReader(conn)

	// banner: "Asterisk Call Manager/x.y"
	if _, err := r.ReadString('\n'); err != nil {
		return "", fmt.Errorf("asterisk voice: banner: %w", err)
	}

	// Login with events off: this connection only originates one call, so
	// the event stream would just be noise between request and response.
	if err := amiAction(conn, r, "login", map[string]string{
		"Action": "Login", "Username": user, "Secret": secret, "Events": "off",
	}); err != nil {
		return "", fmt.Errorf("asterisk voice: login: %w", err)
	}

	fields := map[string]string{
		"Action":  "Originate",
		"Channel": strings.ReplaceAll(channelTpl, "{to}", to),
		"Timeout": orDefault(ch.Config["timeoutMs"], "30000"),
		"Async":   "true",
	}
	if app := ch.Config["application"]; app != "" {
		fields["Application"] = app
		if data := ch.Config["appData"]; data != "" {
			fields["Data"] = data
		}
	} else {
		fields["Context"] = orDefault(ch.Config["context"], "northplane-alert")
		fields["Exten"] = orDefault(ch.Config["exten"], "s")
		fields["Priority"] = orDefault(ch.Config["priority"], "1")
	}
	if cid := ch.Config["callerId"]; cid != "" {
		fields["CallerID"] = cid
	}
	severity := ""
	if rc != nil {
		severity = rc.Severity
	}
	variables := []string{
		"NP_TEXT=" + amiSanitize(text),
		"NP_SEVERITY=" + amiSanitize(severity),
	}
	if ack := m.gatherURL(rc); ack != "" {
		variables = append(variables, "NP_ACK_URL="+amiSanitize(ack))
	}
	if err := amiActionVars(conn, r, "originate", fields, variables); err != nil {
		return "", fmt.Errorf("asterisk voice: %w", err)
	}
	// best-effort logoff; the call is already queued
	_, _ = fmt.Fprintf(conn, "Action: Logoff\r\n\r\n")
	return "", nil
}

// dialAMI opens the manager connection, optionally over TLS.
func dialAMI(ctx context.Context, ch *model.NotificationChannel, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 10 * time.Second}
	if ch.Config["tls"] != "on" {
		return d.DialContext(ctx, "tcp", addr)
	}
	td := &tls.Dialer{NetDialer: d, Config: &tls.Config{
		InsecureSkipVerify: ch.Config["insecure"] == "true", //nolint:gosec // operator opt-in for self-signed AMI certs
		MinVersion:         tls.VersionTLS12,
	}}
	return td.DialContext(ctx, "tcp", addr)
}

// amiAction sends one action and consumes packets until its response.
func amiAction(conn net.Conn, r *bufio.Reader, id string, fields map[string]string) error {
	return amiActionVars(conn, r, id, fields, nil)
}

// amiActionVars writes an AMI action (ordered: Action first, one
// Variable: line per entry) and waits for the packet answering its
// ActionID, skipping interleaved event packets.
func amiActionVars(conn net.Conn, r *bufio.Reader, id string, fields map[string]string, variables []string) error {
	var b strings.Builder
	b.WriteString("Action: " + fields["Action"] + "\r\n")
	b.WriteString("ActionID: " + id + "\r\n")
	for k, v := range fields {
		if k == "Action" {
			continue
		}
		b.WriteString(k + ": " + v + "\r\n")
	}
	for _, v := range variables {
		b.WriteString("Variable: " + v + "\r\n")
	}
	b.WriteString("\r\n")
	if _, err := conn.Write([]byte(b.String())); err != nil {
		return err
	}
	for {
		packet, err := amiReadPacket(r)
		if err != nil {
			return err
		}
		if packet["actionid"] != id {
			continue // unrelated event traffic
		}
		if !strings.EqualFold(packet["response"], "Success") {
			msg := packet["message"]
			if msg == "" {
				msg = packet["response"]
			}
			return fmt.Errorf("%s rejected: %s", strings.ToLower(fields["Action"]), msg)
		}
		return nil
	}
}

// amiReadPacket reads one "Key: Value" block terminated by a blank line.
func amiReadPacket(r *bufio.Reader) (map[string]string, error) {
	packet := map[string]string{}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(packet) == 0 {
				continue // stray blank line
			}
			return packet, nil
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			packet[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
}

// amiSanitize strips protocol-significant characters from variable
// values (CR/LF would terminate the line and inject AMI fields).
func amiSanitize(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	if len(s) > 1024 {
		s = s[:1024]
	}
	return s
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

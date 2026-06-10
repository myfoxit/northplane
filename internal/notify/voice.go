package notify

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// Voice transport (SPEC §9.6): on-prem-first (cloud is never the only
// option for a channel — SPEC §9.6 ordering), no own SIP stack.
//
// Providers, in recommended order:
//   - asterisk:      AMI Originate into a local Asterisk/FreePBX
//     dialplan (see voice_asterisk.go) — fully on-prem voice.
//   - generic-http:  config url with {to}/{text} placeholders (GET) or
//     jsonBody template (POST) — covers sipgate/46elks-style gateways
//     and on-prem voice boxes.
//   - twilio (cloud): config accountSid, authToken ($SECRET ref), from.
//     Places a call whose TwiML <Say>s the rendered message and
//     <Gather>s one digit; pressing 4 hits the signed gather callback
//     (POST /api/v1/voice/gather/{token}) and acknowledges the alert.
func (m *Manager) sendVoice(ctx context.Context, ch *model.NotificationChannel,
	to, text string, rc *RenderContext) (string, error) {
	switch ch.Config["provider"] {
	case "asterisk":
		return m.sendAsteriskVoice(ctx, ch, to, text, rc)
	case "twilio", "":
		if ch.Config["accountSid"] == "" && ch.Config["provider"] == "" {
			return m.sendGenericVoice(ctx, ch, to, text)
		}
		return m.sendTwilioVoice(ctx, ch, to, text, rc)
	case "generic-http":
		return m.sendGenericVoice(ctx, ch, to, text)
	default:
		return "", fmt.Errorf("voice provider %q not supported (asterisk | generic-http | twilio)", ch.Config["provider"])
	}
}

func (m *Manager) sendTwilioVoice(ctx context.Context, ch *model.NotificationChannel,
	to, text string, rc *RenderContext) (string, error) {
	sid := ch.Config["accountSid"]
	token := m.resolveSecret(ch.TenantID, ch.Config["authToken"])
	from := ch.Config["from"]
	if sid == "" || token == "" || from == "" {
		return "", fmt.Errorf("twilio voice: accountSid, authToken, from required")
	}
	lang := ch.Config["language"]
	if lang == "" {
		lang = "en-US"
	}
	form := url.Values{"To": {to}, "From": {from}, "Twiml": {voiceTwiML(text, lang, m.gatherURL(rc))}}
	apiBase := strings.TrimSuffix(ch.Config["apiBase"], "/")
	if apiBase == "" {
		apiBase = "https://api.twilio.com"
	}
	endpoint := apiBase + "/2010-04-01/Accounts/" + sid + "/Calls.json"
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
		return "", fmt.Errorf("twilio voice: HTTP %d: %s", resp.StatusCode, firstLine(string(body)))
	}
	var out struct {
		Sid string `json:"sid"`
	}
	_ = json.Unmarshal(body, &out)
	return out.Sid, nil
}

// gatherURL builds the signed DTMF-ack callback for a real alert call;
// empty when there is nothing to acknowledge (test sends) or no public
// base URL is configured.
func (m *Manager) gatherURL(rc *RenderContext) string {
	if rc == nil || rc.Alert == nil || rc.Alert.ID == "" || rc.Alert.ID == "test" ||
		m.BaseURL == "" || len(m.AckSecret) == 0 {
		return ""
	}
	contactID := ""
	if rc.Contact != nil {
		contactID = rc.Contact.ID
	}
	return m.BaseURL + "/api/v1/voice/gather/" +
		AckToken(m.AckSecret, rc.Alert.ID, contactID, 24*time.Hour)
}

// voiceTwiML renders the call flow: announce twice, gather one digit.
func voiceTwiML(text, lang, gatherURL string) string {
	say := `<Say language="` + lang + `" loop="2">` + xmlEscape(text) + `</Say>`
	if gatherURL == "" {
		return `<Response>` + say + `</Response>`
	}
	return `<Response><Gather numDigits="1" timeout="10" action="` + xmlEscape(gatherURL) +
		`" method="POST">` + say + `</Gather>` +
		`<Say language="` + lang + `">No input received. Goodbye.</Say></Response>`
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// sendGenericVoice drives HTTP voice gateways: config.url with
// {to}/{text} placeholders (GET) or config.jsonBody template (POST).
func (m *Manager) sendGenericVoice(ctx context.Context, ch *model.NotificationChannel, to, text string) (string, error) {
	u := ch.Config["url"]
	if u == "" {
		return "", fmt.Errorf("generic-http voice: config.url required")
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
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) // drain for keep-alive
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("voice gateway: HTTP %d", resp.StatusCode)
	}
	return "", nil
}

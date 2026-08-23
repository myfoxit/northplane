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
	"github.com/northplane/northplane/internal/tts"
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
//
// Speech: with a TTS profile (channel config ttsProfile, the alert
// label np.ttsProfile, or a profile named "default") the text is
// synthesized by Northplane — language detection, pronunciation rules,
// the chosen engine/voice — and the providers play the resulting clip
// (Twilio <Play>, Asterisk NP_AUDIO_URL / NP_AUDIO_FILE, generic
// {audioUrl}). Without a profile, or when every engine in the chain
// fails, the provider's own speech is used exactly as before.
func (m *Manager) sendVoice(ctx context.Context, ch *model.NotificationChannel,
	to, text string, rc *RenderContext) (string, error) {
	// Per-alarm spoken text: a rule (or manual trigger) that sets the
	// np.tts label overrides the channel template for this call.
	if rc != nil && rc.Alert != nil {
		if tts := rc.Alert.Labels["np.tts"]; tts != "" {
			text = tts
		}
	}
	spoken := m.speak(ctx, ch, text, rc)
	switch ch.Config["provider"] {
	case "asterisk":
		return m.sendAsteriskVoice(ctx, ch, to, text, rc, spoken)
	case "twilio", "":
		if ch.Config["accountSid"] == "" && ch.Config["provider"] == "" {
			return m.sendGenericVoice(ctx, ch, to, text, spoken)
		}
		return m.sendTwilioVoice(ctx, ch, to, text, rc, spoken)
	case "generic-http":
		return m.sendGenericVoice(ctx, ch, to, text, spoken)
	default:
		return "", fmt.Errorf("voice provider %q not supported (asterisk | generic-http | twilio)", ch.Config["provider"])
	}
}

// spokenAudio is a synthesized announcement ready for a provider.
type spokenAudio struct {
	res *tts.Result
	url string // signed public URL (empty without baseUrl)
}

// speak synthesizes the announcement through the channel's TTS profile;
// nil when no profile applies or synthesis failed (the caller falls back
// to provider speech). Per-alert labels np.ttsProfile / np.ttsLang /
// np.ttsVoice steer profile, language and voice.
func (m *Manager) speak(ctx context.Context, ch *model.NotificationChannel, text string, rc *RenderContext) *spokenAudio {
	if m.TTS == nil || text == "" {
		return nil
	}
	var labels model.Labels
	if rc != nil && rc.Alert != nil {
		labels = rc.Alert.Labels
	}
	profile := m.TTS.Pick(ctx, ch.TenantID, ch.Config["ttsProfile"], labels)
	if profile == nil {
		return nil
	}
	opts := tts.SpeakOptions{Preroll: true, Lang: labels["np.ttsLang"], Voice: labels["np.ttsVoice"]}
	res, err := m.TTS.Speak(ctx, ch.TenantID, profile, text, opts)
	if err != nil {
		m.log.Warn("voice: tts failed, using provider speech", "channel", ch.Name, "profile", profile.Name, "err", err)
		return nil
	}
	return &spokenAudio{res: res, url: m.TTS.AudioURL(res, 24*time.Hour)}
}

func (m *Manager) sendTwilioVoice(ctx context.Context, ch *model.NotificationChannel,
	to, text string, rc *RenderContext, spoken *spokenAudio) (string, error) {
	sid := ch.Config["accountSid"]
	from := ch.Config["from"]
	user, pass := m.twilioCreds(ch)
	if sid == "" || user == "" || pass == "" || from == "" {
		return "", fmt.Errorf("twilio voice: accountSid, authToken (or apiKeySid+apiKeySecret), from required")
	}
	lang := ch.Config["language"]
	if lang == "" {
		lang = "en-US"
	}
	audioURL := ""
	if spoken != nil && spoken.url != "" {
		audioURL = spoken.url
		if spoken.res.Lang != "" {
			lang = spoken.res.Lang // the trailing <Say> follows the detected language
		}
	}
	form := url.Values{"To": {to}, "From": {from}, "Twiml": {voiceTwiML(text, lang, m.gatherURL(rc), audioURL)}}
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
	req.SetBasicAuth(user, pass)
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

// twilioCreds picks the REST credentials: a standalone API key
// (apiKeySid/apiKeySecret — recommended, revocable per SIP/number
// setup) when configured, otherwise the account SID + auth token.
// Multiple numbers/SIP trunks = multiple channels, each with its own
// credentials and caller ID.
func (m *Manager) twilioCreds(ch *model.NotificationChannel) (user, pass string) {
	if keySid := ch.Config["apiKeySid"]; keySid != "" {
		return keySid, m.resolveSecret(ch.TenantID, ch.Config["apiKeySecret"])
	}
	return ch.Config["accountSid"], m.resolveSecret(ch.TenantID, ch.Config["authToken"])
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
// With audioURL (a Northplane-synthesized clip) the announcement is
// <Play>ed instead of <Say>d.
func voiceTwiML(text, lang, gatherURL, audioURL string) string {
	say := `<Say language="` + lang + `" loop="2">` + xmlEscape(text) + `</Say>`
	if audioURL != "" {
		say = `<Play loop="2">` + xmlEscape(audioURL) + `</Play>`
	}
	if gatherURL == "" {
		return `<Response>` + say + `</Response>`
	}
	return `<Response><Gather numDigits="1" timeout="10" action="` + xmlEscape(gatherURL) +
		`" method="POST">` + say + `</Gather>` +
		`<Say language="` + lang + `">` + noInputPhrase(lang) + `</Say></Response>`
}

// noInputPhrase closes the call in the announcement language.
func noInputPhrase(lang string) string {
	switch strings.ToLower(lang) {
	case "de", "de-de", "de-at", "de-ch":
		return "Keine Eingabe erhalten. Auf Wiederhören."
	case "fr", "fr-fr", "fr-ch", "fr-be", "fr-ca":
		return "Aucune saisie reçue. Au revoir."
	case "es", "es-es", "es-mx", "es-us":
		return "No se recibió ninguna entrada. Adiós."
	case "it", "it-it":
		return "Nessun input ricevuto. Arrivederci."
	case "nl", "nl-nl", "nl-be":
		return "Geen invoer ontvangen. Tot ziens."
	}
	return "No input received. Goodbye."
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// sendGenericVoice drives HTTP voice gateways: config.url with
// {to}/{text}/{audioUrl} placeholders (GET) or config.jsonBody template
// (POST). {audioUrl} is the synthesized clip (empty without a TTS
// profile) for gateways that play a URL instead of speaking text.
func (m *Manager) sendGenericVoice(ctx context.Context, ch *model.NotificationChannel, to, text string, spoken *spokenAudio) (string, error) {
	u := ch.Config["url"]
	if u == "" {
		return "", fmt.Errorf("generic-http voice: config.url required")
	}
	audioURL := ""
	if spoken != nil {
		audioURL = spoken.url
		if spoken.res != nil && spoken.res.Text != "" {
			text = spoken.res.Text // normalised text for gateways that speak themselves
		}
	}
	method := http.MethodGet
	var bodyReader io.Reader
	if tpl := ch.Config["jsonBody"]; tpl != "" {
		method = http.MethodPost
		body := strings.NewReplacer("{to}", to, "{text}", text, "{audioUrl}", audioURL).Replace(tpl)
		bodyReader = strings.NewReader(body)
	} else {
		u = strings.NewReplacer("{to}", url.QueryEscape(to),
			"{text}", url.QueryEscape(text), "{audioUrl}", url.QueryEscape(audioURL)).Replace(u)
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

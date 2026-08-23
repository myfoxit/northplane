package tts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// edgeEngine speaks through the Microsoft Edge "Read aloud" service: the
// same neural voices as Azure AI Speech, free and without an account,
// but unofficial — Microsoft can change the handshake at any time (it
// did in late 2024, when the Sec-MS-GEC token became mandatory). Use it
// for labs and small installations and always configure a fallback
// profile for production alarm lines; paying customers move to the
// azure engine, which is the same voice catalogue with an SLA.
//
// Protocol (as implemented by the edge-tts project): a websocket to the
// read-aloud endpoint, a speech.config message selecting the MP3 output
// format, one SSML message per request, and binary frames carrying the
// audio until Path:turn.end.
type edgeEngine struct {
	voice    string
	pitch    string
	volume   string
	proxy    *url.URL
	version  string
	endpoint string // test override (ws://)
	voiceURL string
}

const (
	edgeTrustedClientToken = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"
	edgeWSSBase            = "wss://speech.platform.bing.com/consumer/speech/synthesize/readaloud/edge/v1"
	edgeVoicesBase         = "https://speech.platform.bing.com/consumer/speech/synthesize/readaloud/voices/list"
	edgeChromiumVersion    = "130.0.2849.68"
	edgeOutputFormat       = "audio-24khz-48kbitrate-mono-mp3"
)

func newEdgeEngine(cfg map[string]string, get func(string) string) (Engine, error) {
	e := &edgeEngine{voice: get("voice"), pitch: get("pitch"), volume: get("volume"),
		version: get("chromiumVersion"), endpoint: get("endpoint"), voiceURL: get("voicesUrl")}
	if e.pitch == "" {
		e.pitch = "+0Hz"
	}
	if e.volume == "" {
		e.volume = "+0%"
	}
	if e.version == "" {
		e.version = edgeChromiumVersion
	}
	if p := get("proxy"); p != "" {
		u, err := url.Parse(p)
		if err != nil {
			return nil, fmt.Errorf("edge: proxy: %w", err)
		}
		e.proxy = u
	}
	return e, nil
}

// edgeSecMSGEC derives the anti-abuse token: SHA-256 of the current
// Windows file time rounded down to 5 minutes, concatenated with the
// trusted client token, upper-case hex.
func edgeSecMSGEC(now time.Time) string {
	ticks := now.Unix() + 11644473600 // seconds since 1601-01-01
	ticks -= ticks % 300
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d%s", ticks*10_000_000, edgeTrustedClientToken)))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func (e *edgeEngine) headers() http.Header {
	major := e.version
	if i := strings.Index(major, "."); i > 0 {
		major = major[:i]
	}
	h := http.Header{}
	h.Set("Pragma", "no-cache")
	h.Set("Cache-Control", "no-cache")
	h.Set("Origin", "chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold")
	h.Set("Accept-Encoding", "gzip, deflate, br")
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/"+
		major+".0.0.0 Safari/537.36 Edg/"+major+".0.0.0")
	return h
}

func (e *edgeEngine) wsURL(now time.Time) string {
	base := e.endpoint
	if base == "" {
		base = edgeWSSBase
	}
	q := url.Values{}
	q.Set("TrustedClientToken", edgeTrustedClientToken)
	q.Set("Sec-MS-GEC", edgeSecMSGEC(now))
	q.Set("Sec-MS-GEC-Version", "1-"+e.version)
	q.Set("ConnectionId", randomHex(16))
	return base + "?" + q.Encode()
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func edgeTimestamp(now time.Time) string {
	return now.UTC().Format("Mon Jan 02 2006 15:04:05") + " GMT+0000 (Coordinated Universal Time)"
}

// edgeRate converts a multiplier to the "+NN%" form.
func edgeRate(r float64) string {
	if r <= 0 {
		return "+0%"
	}
	pct := int(math.Round((r - 1) * 100))
	if pct >= 0 {
		return fmt.Sprintf("+%d%%", pct)
	}
	return fmt.Sprintf("%d%%", pct)
}

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// edgeSSML builds the single-voice SSML document.
func edgeSSML(lang, voice, rate, pitch, volume, text string) string {
	return `<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='` + xmlText(lang) + `'>` +
		`<voice name='` + xmlText(voice) + `'><prosody pitch='` + xmlText(pitch) + `' rate='` + xmlText(rate) +
		`' volume='` + xmlText(volume) + `'>` + xmlText(text) + `</prosody></voice></speak>`
}

func (e *edgeEngine) Synthesize(ctx context.Context, req Request) (*Audio, error) {
	voice := req.Voice
	if voice == "" {
		voice = e.voice
	}
	if voice == "" {
		voice = defaultNeuralVoice(req.Lang)
	}
	lang := req.Lang
	if lang == "" {
		lang = localeOfVoice(voice)
	}
	now := time.Now()
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	if e.proxy != nil {
		dialer.Proxy = http.ProxyURL(e.proxy)
	}
	conn, resp, err := dialer.DialContext(ctx, e.wsURL(now), e.headers()) //nolint:bodyclose // closed below on both paths
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close() // the handshake response body is never streamed
	}
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("edge: websocket handshake: %w (HTTP %d)", err, resp.StatusCode)
		}
		return nil, fmt.Errorf("edge: websocket: %w", err)
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(45 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetWriteDeadline(deadline)
	_ = conn.SetReadDeadline(deadline)

	config := "X-Timestamp:" + edgeTimestamp(now) + "\r\nContent-Type:application/json; charset=utf-8\r\nPath:speech.config\r\n\r\n" +
		`{"context":{"synthesis":{"audio":{"metadataoptions":{"sentenceBoundaryEnabled":"false","wordBoundaryEnabled":"false"},"outputFormat":"` +
		edgeOutputFormat + `"}}}}` + "\r\n"
	if err := conn.WriteMessage(websocket.TextMessage, []byte(config)); err != nil {
		return nil, fmt.Errorf("edge: send config: %w", err)
	}
	reqID := randomHex(16)
	ssml := "X-RequestId:" + reqID + "\r\nContent-Type:application/ssml+xml\r\nX-Timestamp:" + edgeTimestamp(now) +
		"Z\r\nPath:ssml\r\n\r\n" + edgeSSML(lang, voice, edgeRate(req.Rate), e.pitch, e.volume, req.Text)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(ssml)); err != nil {
		return nil, fmt.Errorf("edge: send ssml: %w", err)
	}

	var audio []byte
	for done := false; !done; {
		typ, data, err := conn.ReadMessage()
		if err != nil {
			if len(audio) > 0 {
				break // server closed after the last frame
			}
			return nil, fmt.Errorf("edge: read: %w", err)
		}
		switch typ {
		case websocket.TextMessage:
			headers, _ := splitEdgeFrame(data)
			done = strings.Contains(headers, "Path:turn.end")
		case websocket.BinaryMessage:
			if len(data) < 2 {
				continue
			}
			hl := int(binary.BigEndian.Uint16(data[:2]))
			if 2+hl > len(data) {
				continue
			}
			if strings.Contains(string(data[2:2+hl]), "Path:audio") {
				audio = append(audio, data[2+hl:]...)
				if len(audio) > maxAudioBytes {
					return nil, fmt.Errorf("edge: audio too large")
				}
			}
		}
	}
	if len(audio) == 0 {
		return nil, fmt.Errorf("edge: no audio received (voice %q)", voice)
	}
	return Decode(audio, "audio/mpeg", "")
}

func splitEdgeFrame(data []byte) (headers, body string) {
	s := string(data)
	if i := strings.Index(s, "\r\n\r\n"); i >= 0 {
		return s[:i], s[i+4:]
	}
	return s, ""
}

// Voices lists the read-aloud catalogue (optionally filtered by locale
// prefix).
func (e *edgeEngine) Voices(ctx context.Context, lang string) ([]Voice, error) {
	base := e.voiceURL
	if base == "" {
		base = edgeVoicesBase
	}
	q := url.Values{}
	q.Set("trustedclienttoken", edgeTrustedClientToken)
	q.Set("Sec-MS-GEC", edgeSecMSGEC(time.Now()))
	q.Set("Sec-MS-GEC-Version", "1-"+e.version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header = e.headers()
	req.Header.Del("Accept-Encoding") // let Go handle gzip transparently
	body, _, err := doAudio(req)
	if err != nil {
		return nil, fmt.Errorf("edge voices: %w", err)
	}
	var raw []struct {
		ShortName    string `json:"ShortName"`
		FriendlyName string `json:"FriendlyName"`
		Gender       string `json:"Gender"`
		Locale       string `json:"Locale"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("edge voices: %w", err)
	}
	prefix := strings.ToLower(langPrefix(lang))
	var out []Voice
	for _, v := range raw {
		if prefix != "" && !strings.HasPrefix(strings.ToLower(v.Locale), prefix) {
			continue
		}
		out = append(out, Voice{ID: v.ShortName, Name: v.FriendlyName, Lang: v.Locale, Gender: v.Gender})
	}
	return out, nil
}

// neuralDefaults maps locales/languages to a good default Microsoft
// neural voice (shared by the edge and azure engines).
var neuralDefaults = map[string]string{
	"de-DE": "de-DE-KatjaNeural", "de-AT": "de-AT-IngridNeural", "de-CH": "de-CH-LeniNeural",
	"en-US": "en-US-AriaNeural", "en-GB": "en-GB-SoniaNeural", "en-AU": "en-AU-NatashaNeural", "en-IE": "en-IE-EmilyNeural",
	"fr-FR": "fr-FR-DeniseNeural", "fr-CH": "fr-CH-ArianeNeural", "es-ES": "es-ES-ElviraNeural", "es-MX": "es-MX-DaliaNeural",
	"it-IT": "it-IT-ElsaNeural", "nl-NL": "nl-NL-ColetteNeural", "nl-BE": "nl-BE-DenaNeural",
	"pt-PT": "pt-PT-RaquelNeural", "pt-BR": "pt-BR-FranciscaNeural", "pl-PL": "pl-PL-ZofiaNeural",
	"cs-CZ": "cs-CZ-VlastaNeural", "sk-SK": "sk-SK-ViktoriaNeural", "sv-SE": "sv-SE-SofieNeural",
	"da-DK": "da-DK-ChristelNeural", "nb-NO": "nb-NO-PernilleNeural", "fi-FI": "fi-FI-NooraNeural",
	"tr-TR": "tr-TR-EmelNeural", "hu-HU": "hu-HU-NoemiNeural", "ro-RO": "ro-RO-AlinaNeural",
	"ru-RU": "ru-RU-SvetlanaNeural", "uk-UA": "uk-UA-PolinaNeural", "el-GR": "el-GR-AthinaNeural",
	"bg-BG": "bg-BG-KalinaNeural", "hr-HR": "hr-HR-GabrijelaNeural", "sl-SI": "sl-SI-PetraNeural",
	"ja-JP": "ja-JP-NanamiNeural", "zh-CN": "zh-CN-XiaoxiaoNeural", "ko-KR": "ko-KR-SunHiNeural",
	"ar-SA": "ar-SA-ZariyahNeural", "he-IL": "he-IL-HilaNeural", "hi-IN": "hi-IN-SwaraNeural",
}

func defaultNeuralVoice(lang string) string {
	tag := strings.Replace(lang, "_", "-", 1)
	if v, ok := neuralDefaults[tag]; ok {
		return v
	}
	if full, ok := defaultRegion[langPrefix(lang)]; ok {
		if v, ok := neuralDefaults[full]; ok {
			return v
		}
	}
	return "en-US-AriaNeural"
}

// localeOfVoice extracts "de-DE" from "de-DE-KatjaNeural".
func localeOfVoice(voice string) string {
	parts := strings.SplitN(voice, "-", 3)
	if len(parts) >= 2 && len(parts[0]) == 2 {
		return parts[0] + "-" + parts[1]
	}
	return "en-US"
}

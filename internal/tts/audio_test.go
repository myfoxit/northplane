package tts

import (
	"context"
	"encoding/binary"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWAVRoundTrip(t *testing.T) {
	a := Tone(16000, 440, 200*time.Millisecond, 0.5)
	b, err := DecodeWAV(a.WAV())
	if err != nil {
		t.Fatal(err)
	}
	if b.Rate != 16000 || len(b.PCM) != len(a.PCM) || b.PCM[100] != a.PCM[100] {
		t.Fatalf("roundtrip mismatch")
	}
	// µ-law container decodes back to roughly the same signal
	u, err := DecodeWAV(a.Resample(8000).WAVMulaw())
	if err != nil {
		t.Fatal(err)
	}
	if u.Rate != 8000 || u.Peak() < 12000 || u.Peak() > 18000 {
		t.Fatalf("ulaw roundtrip: rate=%d peak=%d", u.Rate, u.Peak())
	}
	// raw hints
	if d, err := Decode(a.SLN(), "", "pcm16:16000"); err != nil || d.Rate != 16000 || len(d.PCM) != len(a.PCM) {
		t.Fatalf("pcm16 hint: %v", err)
	}
	if d, err := Decode(a.Resample(8000).Mulaw(), "", "ulaw:8000"); err != nil || d.Rate != 8000 {
		t.Fatalf("ulaw hint: %v", err)
	}
	if d, err := Decode(a.Resample(8000).Alaw(), "audio/x-alaw", "alaw"); err != nil || d.Rate != 8000 || d.Peak() < 12000 {
		t.Fatalf("alaw hint: %v", err)
	}
	if _, err := Decode([]byte("definitely not audio"), "text/plain", ""); err == nil {
		t.Fatal("garbage accepted")
	}
	if _, err := Decode(nil, "", ""); err == nil {
		t.Fatal("empty accepted")
	}
}

func TestG711Symmetry(t *testing.T) {
	for _, v := range []int16{0, 1, -1, 100, -100, 1000, -1000, 8000, -8000, 32000, -32000, 32767, -32768} {
		u := ulaw2linear(linear2ulaw(v))
		a := alaw2linear(linear2alaw(v))
		tol := int(math.Abs(float64(v))/16) + 40
		if d := int(u) - int(v); d > tol || d < -tol {
			t.Errorf("ulaw %d → %d", v, u)
		}
		if d := int(a) - int(v); d > tol || d < -tol {
			t.Errorf("alaw %d → %d", v, a)
		}
	}
	// known vectors (ITU G.711 reference): silence and peak
	if linear2ulaw(0) != 0xFF || linear2alaw(0) != 0xD5 {
		t.Fatalf("silence codes: ulaw %x alaw %x", linear2ulaw(0), linear2alaw(0))
	}
}

func TestResampleKeepsToneAndRemovesAliases(t *testing.T) {
	// 440 Hz survives 24k → 8k; a 6 kHz component (above the 8 kHz Nyquist)
	// must be attenuated heavily instead of folding down.
	low := Tone(24000, 440, 500*time.Millisecond, 0.5)
	high := Tone(24000, 6000, 500*time.Millisecond, 0.5)
	rl := low.Resample(8000)
	rh := high.Resample(8000)
	if rl.Rate != 8000 || len(rl.PCM) != 4000 {
		t.Fatalf("resampled length %d", len(rl.PCM))
	}
	if rl.Peak() < 14000 {
		t.Fatalf("440 Hz attenuated: peak %d", rl.Peak())
	}
	if rh.Peak() > 2500 {
		t.Fatalf("6 kHz not filtered: peak %d", rh.Peak())
	}
	// upsampling
	up := rl.Resample(16000)
	if up.Rate != 16000 || len(up.PCM) != 8000 || up.Peak() < 13000 {
		t.Fatalf("upsample: %d %d", len(up.PCM), up.Peak())
	}
}

func TestAudioFinishing(t *testing.T) {
	a := Concat(Silence(8000, 500*time.Millisecond), Tone(8000, 300, 200*time.Millisecond, 0.1), Silence(8000, 500*time.Millisecond))
	a.TrimSilence(-45)
	if d := a.Duration(); d < 200*time.Millisecond || d > 260*time.Millisecond {
		t.Fatalf("trim: %s", d)
	}
	a.NormalizePeak(-3)
	if a.Peak() < 23000 || a.Peak() > 23400 {
		t.Fatalf("normalize peak: %d", a.Peak())
	}
	a.Gain(-6)
	if a.Peak() < 11400 || a.Peak() > 11800 {
		t.Fatalf("gain: %d", a.Peak())
	}
	a.Pad(300*time.Millisecond, 200*time.Millisecond)
	if d := a.Duration(); d < 700*time.Millisecond {
		t.Fatalf("pad: %s", d)
	}
	for _, name := range PrerollNames {
		p := Preroll(name, 8000)
		if name == "none" && p != nil {
			t.Fatal("none must be nil")
		}
		if name != "none" && (p == nil || p.Duration() < 300*time.Millisecond) {
			t.Fatalf("preroll %s", name)
		}
	}
	if Preroll("bogus", 8000) != nil {
		t.Fatal("unknown preroll")
	}
}

// TestEdgeProtocol drives the edge engine against a fake read-aloud
// websocket: checks the DRM query parameters, the config/SSML messages
// and the binary audio framing.
func TestEdgeProtocol(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var gotSSML, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for i := 0; i < 2; i++ { // config + ssml
			_, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			if strings.Contains(string(msg), "Path:ssml") {
				gotSSML = string(msg)
			}
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte("X-RequestId:x\r\nPath:turn.start\r\n\r\n{}"))
		// binary frame: 2-byte header length + headers + audio (WAV here; the
		// real service sends MP3 — Decode sniffs either)
		headers := "X-RequestId:x\r\nContent-Type:audio/mpeg\r\nPath:audio\r\n"
		audio := Tone(24000, 440, 100*time.Millisecond, 0.5).WAV()
		frame := make([]byte, 2+len(headers)+len(audio))
		binary.BigEndian.PutUint16(frame, uint16(len(headers)))
		copy(frame[2:], headers)
		copy(frame[2+len(headers):], audio)
		_ = c.WriteMessage(websocket.BinaryMessage, frame)
		_ = c.WriteMessage(websocket.TextMessage, []byte("X-RequestId:x\r\nPath:turn.end\r\n\r\n{}"))
	}))
	t.Cleanup(srv.Close)

	eng, err := NewEngine("edge", map[string]string{"endpoint": "ws" + strings.TrimPrefix(srv.URL, "http")}, EngineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := eng.Synthesize(context.Background(), Request{Text: "Alarm & Test", Lang: "de-DE", Rate: 1.1})
	if err != nil {
		t.Fatal(err)
	}
	if a.Rate != 24000 || len(a.PCM) == 0 {
		t.Fatalf("audio: %+v", a)
	}
	if !strings.Contains(gotQuery, "Sec-MS-GEC=") || !strings.Contains(gotQuery, "Sec-MS-GEC-Version=1-") || !strings.Contains(gotQuery, "ConnectionId=") {
		t.Fatalf("query: %s", gotQuery)
	}
	if !strings.Contains(gotSSML, "de-DE-KatjaNeural") || !strings.Contains(gotSSML, "Alarm &amp; Test") ||
		!strings.Contains(gotSSML, "rate='+10%'") || !strings.Contains(gotSSML, "xml:lang='de-DE'") {
		t.Fatalf("ssml: %s", gotSSML)
	}
	// GEC is stable within a 5-minute window and upper-case hex
	now := time.Date(2026, 8, 23, 10, 2, 30, 0, time.UTC)
	g1, g2 := edgeSecMSGEC(now), edgeSecMSGEC(now.Add(2*time.Minute))
	if g1 != g2 || len(g1) != 64 || strings.ToUpper(g1) != g1 {
		t.Fatalf("gec: %s %s", g1, g2)
	}
	if edgeSecMSGEC(now.Add(5*time.Minute)) == g1 {
		t.Fatal("gec must roll over")
	}
}

func TestEdgeRateAndVoices(t *testing.T) {
	if edgeRate(0) != "+0%" || edgeRate(1.25) != "+25%" || edgeRate(0.8) != "-20%" {
		t.Fatalf("rates: %s %s %s", edgeRate(0), edgeRate(1.25), edgeRate(0.8))
	}
	if defaultNeuralVoice("de") != "de-DE-KatjaNeural" || defaultNeuralVoice("en-GB") != "en-GB-SoniaNeural" ||
		defaultNeuralVoice("xx") != "en-US-AriaNeural" {
		t.Fatal("default voices")
	}
	if localeOfVoice("fr-FR-DeniseNeural") != "fr-FR" || localeOfVoice("weird") != "en-US" {
		t.Fatal("locale of voice")
	}
}

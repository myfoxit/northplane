package api

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/tts"
)

// fakeSpeechServer is an HTTP TTS endpoint (the "http" engine) returning
// a short tone as WAV; it records the texts it was asked to speak.
func fakeSpeechServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var texts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		texts = append(texts, r.URL.Query().Get("text"))
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(tts.Tone(16000, 500, 150*time.Millisecond, 0.4).WAV())
	}))
	t.Cleanup(srv.Close)
	return srv, &texts
}

func (ta *testAPI) createProfile(name string, doc map[string]any) []byte {
	ta.t.Helper()
	doc["name"] = name
	code, body := ta.admin("POST", "/api/v1/tts-profiles", doc)
	if code != http.StatusCreated {
		ta.t.Fatalf("create profile: %d %s", code, body)
	}
	return body
}

func TestTTSProfilesCRUDAndValidation(t *testing.T) {
	ta := bootAPI(t)

	body := ta.createProfile("default", map[string]any{
		"engine": "edge", "language": "de-DE",
		"voices": map[string]string{"de": "de-DE-KatjaNeural", "en": "en-US-AriaNeural"},
		"detect": map[string]any{"mode": "segments"},
		"normalize": map[string]any{
			"lexicon": []map[string]any{{"from": "np-01", "to": "Server eins"}},
			"regex":   []map[string]any{{"pattern": `srv(\d+)`, "replace": "Server $1"}},
		},
	})
	if !strings.Contains(string(body), `"engine":"edge"`) {
		t.Fatalf("created doc: %s", body)
	}
	code, list := ta.read("GET", "/api/v1/tts-profiles", nil)
	if code != http.StatusOK || !strings.Contains(string(list), `"name":"default"`) {
		t.Fatalf("list: %d %s", code, list)
	}
	// invalid engine / regex / enum → 422
	for _, bad := range []map[string]any{
		{"name": "bad1", "engine": "nope"},
		{"name": "bad2", "engine": "edge", "normalize": map[string]any{"regex": []map[string]any{{"pattern": "(", "replace": ""}}}},
		{"name": "bad3", "engine": "edge", "detect": map[string]any{"mode": "always"}},
		{"name": "bad4", "engine": "edge", "audio": map[string]any{"format": "ulaw", "sampleRate": 16000}},
	} {
		if code, body := ta.admin("POST", "/api/v1/tts-profiles", bad); code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422 for %v, got %d %s", bad["name"], code, body)
		}
	}
	// read-only token cannot create
	if code, _ := ta.read("POST", "/api/v1/tts-profiles", map[string]any{"name": "x", "engine": "edge"}); code != http.StatusForbidden {
		t.Fatalf("reader create: %d", code)
	}
	// engines catalogue
	code, eng := ta.read("GET", "/api/v1/tts/engines", nil)
	if code != http.StatusOK || !strings.Contains(string(eng), `"elevenlabs"`) || !strings.Contains(string(eng), `"apiKey"`) {
		t.Fatalf("engines: %d %s", code, eng)
	}
	// bundle kind round trip
	bundle := "kind: TTSProfile\nmetadata: { name: bundled }\nspec:\n  engine: edge\n  language: en-GB\n"
	code, res := ta.do("POST", "/api/v1/config/bundles:apply", bundle, bearer(ta.adminToken), func(r *http.Request) {
		r.Header.Set("Content-Type", "application/yaml")
	})
	if code >= 300 {
		t.Fatalf("bundle apply: %d %s", code, res)
	}
	if code, got := ta.read("GET", "/api/v1/tts-profiles/bundled", nil); code != http.StatusOK || !strings.Contains(string(got), "en-GB") {
		t.Fatalf("bundled profile: %d %s", code, got)
	}
}

func TestTTSNormalizeAndPreview(t *testing.T) {
	ta := bootAPI(t)
	srv, texts := fakeSpeechServer(t)
	ta.createProfile("default", map[string]any{
		"engine": "http", "language": "de-DE",
		"config": map[string]string{"url": srv.URL + "/?text={text}&lang={lang}"},
		"voices": map[string]string{"de": "thorsten", "en": "amy"},
		"detect": map[string]any{"mode": "segments"},
		"normalize": map[string]any{
			"lexicon": []map[string]any{{"from": "np-01", "to": "Server eins"}},
		},
	})

	// dry run: language + normalised text, no engine call, readable by readers
	code, body := ta.read("POST", "/api/v1/tts:normalize", map[string]any{
		"text": "Festplatte /var auf np-01 zu 95% voll. CPU load is very high on the web server.",
	})
	if code != http.StatusOK {
		t.Fatalf("normalize: %d %s", code, body)
	}
	var plan tts.Plan
	mustJSON(t, body, &plan)
	if plan.Lang != "de-DE" || len(plan.Segments) != 2 || plan.Segments[1].Lang != "en-US" {
		t.Fatalf("plan: %+v", plan)
	}
	if !strings.Contains(plan.Segments[0].Text, "Server eins") || !strings.Contains(plan.Segments[0].Text, "95 Prozent") {
		t.Fatalf("german segment: %q", plan.Segments[0].Text)
	}
	if !strings.Contains(plan.Segments[1].Text, "C P U load") {
		t.Fatalf("english segment: %q", plan.Segments[1].Text)
	}
	if len(*texts) != 0 {
		t.Fatal("normalize must not call the engine")
	}

	// preview: synthesizes and returns a base64 WAV
	code, body = ta.admin("POST", "/api/v1/tts:preview", map[string]any{"text": "Festplatte voll auf np-01", "preroll": true})
	if code != http.StatusOK {
		t.Fatalf("preview: %d %s", code, body)
	}
	var prev TTSPreviewResponse
	mustJSON(t, body, &prev)
	wav, err := base64.StdEncoding.DecodeString(prev.Audio)
	if err != nil || len(wav) < 100 || string(wav[:4]) != "RIFF" {
		t.Fatalf("preview audio: %v %d", err, len(wav))
	}
	if prev.Lang != "de-DE" || !strings.Contains(prev.Text, "Server eins") || prev.DurationMS < 500 || prev.Engine != "default/http" {
		t.Fatalf("preview meta: %+v", prev)
	}
	if len(*texts) != 1 || !strings.Contains((*texts)[0], "Server eins") {
		t.Fatalf("engine texts: %v", *texts)
	}
	// readers may not spend provider credits
	if code, _ := ta.read("POST", "/api/v1/tts:preview", map[string]any{"text": "x"}); code != http.StatusForbidden {
		t.Fatalf("reader preview: %d", code)
	}
	// inline (unsaved) profile preview, bypassing the cache
	code, body = ta.admin("POST", "/api/v1/tts:preview", map[string]any{
		"text": "Hello world", "profile": map[string]any{
			"engine": "http", "language": "en-US", "config": map[string]string{"url": srv.URL + "/?text={text}"},
			"normalize": map[string]any{"spellOut": []string{"world"}},
		},
	})
	if code != http.StatusOK || !strings.Contains(string(body), "W O R L D") {
		t.Fatalf("inline preview: %d %s", code, body)
	}
	// invalid inline profile → 422
	code, _ = ta.admin("POST", "/api/v1/tts:preview", map[string]any{"text": "x", "profile": map[string]any{"engine": "ghost"}})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("inline invalid: %d", code)
	}
	// unknown profile name → 404
	code, _ = ta.admin("POST", "/api/v1/tts:preview", map[string]any{"text": "x", "profileName": "missing"})
	if code != http.StatusNotFound {
		t.Fatalf("missing profile: %d", code)
	}
	// voices: the http engine has no catalogue → empty list
	code, body = ta.admin("POST", "/api/v1/tts:voices", map[string]any{"profileName": "default"})
	if code != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("voices: %d %s", code, body)
	}
}

func TestTTSAudioRouteAndTwilioPlay(t *testing.T) {
	ta := bootAPI(t)
	ta.a.Cfg.BaseURL = "https://np.test"
	srv, texts := fakeSpeechServer(t)
	ta.createProfile("default", map[string]any{
		"engine": "http", "language": "en-US",
		"config": map[string]string{"url": srv.URL + "/?text={text}"},
	})
	srcID := ta.createTelSource("voice-inbound", map[string]string{"language": "en-US"})

	form := url.Values{"From": {"+4915112345678"}, "To": {"+49301234567"}, "CallSid": {"CAplay1"}}
	code, xml := ta.call("/api/v1/voice/inbound/"+srcID, form)
	if code != http.StatusOK {
		t.Fatalf("inbound: %d %s", code, xml)
	}
	if !strings.Contains(xml, "<Play>https://np.test/api/v1/tts/audio/") || strings.Contains(xml, "<Say") {
		t.Fatalf("expected synthesized prompts, got %s", xml)
	}
	if len(*texts) < 2 { // greeting + options
		t.Fatalf("engine calls: %v", *texts)
	}
	// the signed URL in the TwiML is served publicly (no auth)
	start := strings.Index(xml, "https://np.test/api/v1/tts/audio/")
	end := strings.Index(xml[start:], "</Play>")
	clipURL := xml[start : start+end]
	path := strings.TrimPrefix(clipURL, "https://np.test")
	rec := ta.raw("GET", path, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "audio/wav" || rec.Body.Len() < 100 {
		t.Fatalf("audio route: %d %s %d", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	// tampered token → 404
	rec = ta.raw("GET", strings.Replace(path, ".wav", "x.wav", 1), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("tampered: %d", rec.Code)
	}
	// the same prompt is cached: a second call does not hit the engine again
	n := len(*texts)
	code, xml = ta.call("/api/v1/voice/inbound/"+srcID, form)
	if code != http.StatusOK || len(*texts) != n {
		t.Fatalf("cache: %d calls %d→%d %s", code, n, len(*texts), xml)
	}
	// source without a usable profile → plain <Say>
	srcSay := ta.createTelSource("sms-inbound", nil) // unrelated type to keep names unique
	_ = srcSay
	code, body := ta.admin("DELETE", "/api/v1/tts-profiles/default", nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete profile: %d %s", code, body)
	}
	code, xml = ta.call("/api/v1/voice/inbound/"+srcID, form)
	if code != http.StatusOK || !strings.Contains(xml, "<Say") || strings.Contains(xml, "<Play>") {
		t.Fatalf("fallback to Say: %d %s", code, xml)
	}
}

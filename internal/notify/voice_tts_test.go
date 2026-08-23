package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
	"github.com/northplane/northplane/internal/tts"
)

// withTTS attaches a TTS service (cache in a temp dir) and stores a
// profile that speaks through a fake HTTP engine.
func withTTS(t *testing.T, m *Manager, store *storage.Store, ctx context.Context, profile model.TTSProfile) *[]string {
	t.Helper()
	var texts []string
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		texts = append(texts, r.URL.Query().Get("text"))
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(tts.Tone(22050, 440, 120*time.Millisecond, 0.4).WAV())
	}))
	t.Cleanup(engine.Close)
	cache, err := tts.NewCache(t.TempDir(), 4, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	svc := tts.New(store, cache, nil, nil)
	svc.BaseURL = m.BaseURL
	svc.SignKey = m.AckSecret
	m.TTS = svc
	if profile.Engine == "" {
		profile.Engine = model.TTSEngineHTTP
		profile.Config = map[string]string{"url": engine.URL + "/?text={text}&lang={lang}"}
	}
	if _, err := store.PutResource(ctx, model.DefaultTenant, storage.KindTTSProfile, profile.Name, profile, 0); err != nil {
		t.Fatal(err)
	}
	return &texts
}

func TestTwilioVoicePlaysSynthesizedClip(t *testing.T) {
	m, store, ctx := setupMgr(t)
	texts := withTTS(t, m, store, ctx, model.TTSProfile{Name: "default", Language: "de-DE",
		Detect: model.TTSDetect{Languages: []string{"de-DE", "en-US"}}})

	var twiml atomic.Value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		twiml.Store(r.PostFormValue("Twiml"))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"CA1"}`))
	}))
	t.Cleanup(ts.Close)
	putChannel(t, store, ctx, model.NotificationChannel{
		Name: "voice", Type: model.ChannelVoice,
		Config: map[string]string{"provider": "twilio", "accountSid": "AC1", "authToken": "tok",
			"from": "+15550100", "apiBase": ts.URL}})

	alert := openAlert(t, store, ctx, "Festplatte voll auf srv-01")
	alert.Labels = model.Labels{"np.tts": "Achtung. Festplatte voll auf srv-01. Drücken Sie die 4 zum Quittieren."}
	rc := rcFor(m, alert)
	ch := mustChannel(t, m, ctx, "voice")
	if _, err := m.send(ctx, ch, "+15550123", "", "ignored template text", rc); err != nil {
		t.Fatal(err)
	}
	tw, _ := twiml.Load().(string)
	if !strings.Contains(tw, `<Play loop="2">https://np.test/api/v1/tts/audio/`) || strings.Contains(tw, `<Say language="de-DE" loop`) {
		t.Fatalf("expected <Play> of the synthesized clip: %s", tw)
	}
	// language follows the detected German → German closing phrase
	if !strings.Contains(tw, "Keine Eingabe erhalten") || !strings.Contains(tw, "/api/v1/voice/gather/") {
		t.Fatalf("closing phrase / gather: %s", tw)
	}
	if len(*texts) != 1 || !strings.Contains((*texts)[0], "Server 0 1") {
		t.Fatalf("normalised text sent to the engine: %v", *texts)
	}
	// the clip is published and verifiable
	u := tw[strings.Index(tw, "https://np.test/api/v1/tts/audio/"):]
	u = u[:strings.Index(u, "</Play>")]
	token := strings.TrimPrefix(u, "https://np.test/api/v1/tts/audio/")
	if _, err := m.TTS.VerifyAudioToken(token); err != nil {
		t.Fatalf("token: %v", err)
	}
}

func TestTwilioVoiceFallsBackToSayWhenEngineFails(t *testing.T) {
	m, store, ctx := setupMgr(t)
	// profile whose engine points at a dead endpoint
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(dead.Close)
	withTTS(t, m, store, ctx, model.TTSProfile{Name: "default", Engine: model.TTSEngineHTTP,
		Config: map[string]string{"url": dead.URL + "/?text={text}"}, Language: "en-US"})

	var twiml atomic.Value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		twiml.Store(r.PostFormValue("Twiml"))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"CA2"}`))
	}))
	t.Cleanup(ts.Close)
	putChannel(t, store, ctx, model.NotificationChannel{
		Name: "voice", Type: model.ChannelVoice,
		Config: map[string]string{"provider": "twilio", "accountSid": "AC1", "authToken": "tok",
			"from": "+15550100", "apiBase": ts.URL}})
	alert := openAlert(t, store, ctx, "engine down")
	ch := mustChannel(t, m, ctx, "voice")
	if _, err := m.send(ctx, ch, "+15550123", "", "Northplane alert. engine down.", rcFor(m, alert)); err != nil {
		t.Fatal(err)
	}
	tw, _ := twiml.Load().(string)
	if !strings.Contains(tw, `<Say language="en-US" loop="2">Northplane alert. engine down.</Say>`) || strings.Contains(tw, "<Play") {
		t.Fatalf("expected <Say> fallback: %s", tw)
	}
}

func TestAsteriskVoiceAudioVariables(t *testing.T) {
	m, store, ctx := setupMgr(t)
	withTTS(t, m, store, ctx, model.TTSProfile{Name: "pbx", Language: "en-US"})
	dir := filepath.Join(t.TempDir(), "sounds")

	ch := &model.NotificationChannel{TenantID: model.DefaultTenant, Name: "pbx-voice", Type: model.ChannelVoice,
		Config: map[string]string{"provider": "asterisk", "ttsProfile": "pbx", "ttsDir": dir, "ttsDirPBX": "/var/lib/asterisk/sounds/northplane"}}
	spoken := m.speak(ctx, ch, "Disk full on web01", nil)
	if spoken == nil || spoken.url == "" {
		t.Fatalf("no synthesized clip: %+v", spoken)
	}
	vars := asteriskAudioVars(ch, spoken, nil)
	joined := strings.Join(vars, "\n")
	if !strings.Contains(joined, "NP_AUDIO_URL=https://np.test/api/v1/tts/audio/") ||
		!strings.Contains(joined, "NP_AUDIO_FILE=/var/lib/asterisk/sounds/northplane/"+spoken.res.ID) ||
		!strings.Contains(joined, "NP_LANG=en-US") ||
		!strings.Contains(joined, "NP_TEXT_SPOKEN=Disk full on web 0 1.") {
		t.Fatalf("vars: %s", joined)
	}
	for _, v := range vars {
		if strings.HasPrefix(v, "NP_AUDIO_FILE=") && strings.HasSuffix(v, ".wav") {
			t.Fatalf("NP_AUDIO_FILE must not carry the extension: %s", v)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, spoken.res.ID+".wav")); err != nil {
		t.Fatalf("clip not written to ttsDir: %v", err)
	}
	// unknown ttsProfile on the channel → no speech, provider fallback
	ch.Config["ttsProfile"] = "nope"
	if sp := m.speak(ctx, ch, "x", nil); sp != nil {
		t.Fatalf("unknown profile must yield no clip")
	}
}

func TestGenericVoiceAudioURLPlaceholder(t *testing.T) {
	m, store, ctx := setupMgr(t)
	withTTS(t, m, store, ctx, model.TTSProfile{Name: "default", Language: "en-US"})
	var hit atomic.Value
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(r.URL.String())
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(gw.Close)
	putChannel(t, store, ctx, model.NotificationChannel{
		Name: "gw", Type: model.ChannelVoice,
		Config: map[string]string{"provider": "generic-http", "url": gw.URL + "/call?to={to}&audio={audioUrl}&text={text}"}})
	ch := mustChannel(t, m, ctx, "gw")
	if _, err := m.send(ctx, ch, "+4912345", "", "CPU high on np-01", &RenderContext{}); err != nil {
		t.Fatal(err)
	}
	got, _ := hit.Load().(string)
	if !strings.Contains(got, "audio=https%3A%2F%2Fnp.test%2Fapi%2Fv1%2Ftts%2Faudio%2F") || !strings.Contains(got, "text=C+P+U+high+on+N+P+0+1.") {
		t.Fatalf("gateway url: %s", got)
	}
}

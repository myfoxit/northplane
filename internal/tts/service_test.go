package tts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// fakeTTS is an HTTP synthesis endpoint returning a 1-second 440 Hz tone
// as 22.05 kHz WAV and recording what it was asked.
type fakeTTS struct {
	mu    sync.Mutex
	calls []map[string]string
	fail  bool
}

func (f *fakeTTS) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, map[string]string{
		"text": r.URL.Query().Get("text"), "lang": r.URL.Query().Get("lang"), "voice": r.URL.Query().Get("voice"),
	})
	fail := f.fail
	f.mu.Unlock()
	if fail {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	_, _ = w.Write(Tone(22050, 440, time.Second, 0.3).WAV())
}

func (f *fakeTTS) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func httpProfile(name, url string) *model.TTSProfile {
	return &model.TTSProfile{
		Name: name, Engine: model.TTSEngineHTTP,
		Config:   map[string]string{"url": url + "/tts?text={text}&lang={lang}&voice={voice}"},
		Language: "de-DE",
		Voices:   map[string]string{"de": "thorsten", "en": "amy"},
		Detect:   model.TTSDetect{Mode: model.TTSDetectSegments},
	}
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	cache, err := NewCache(filepath.Join(t.TempDir(), "cache"), 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s := New(nil, cache, nil, nil)
	s.BaseURL = "https://np.test"
	s.SignKey = []byte("test-key")
	return s
}

func TestSpeakSegmentsCacheAndURL(t *testing.T) {
	f := &fakeTTS{}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	s := newTestService(t)
	p := httpProfile("main", srv.URL)
	p.Audio.Preroll = "chime"

	text := "Northplane Alarm. Schweregrad kritisch. CPU load is very high on the web server. Drücken Sie die 4 zum Quittieren."
	res, err := s.Speak(context.Background(), "t1", p, text, SpeakOptions{Preroll: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cached || res.Path == "" || len(res.Data) == 0 {
		t.Fatalf("first speak: cached=%v path=%q data=%d", res.Cached, res.Path, len(res.Data))
	}
	if len(res.Segments) != 3 {
		t.Fatalf("segments: %+v", res.Segments)
	}
	if res.Segments[0].Lang != "de-DE" || res.Segments[0].Voice != "thorsten" ||
		res.Segments[1].Lang != "en-US" || res.Segments[1].Voice != "amy" {
		t.Fatalf("segment voices: %+v", res.Segments)
	}
	if f.count() != 3 {
		t.Fatalf("engine calls: %d", f.count())
	}
	if f.calls[1]["lang"] != "en-US" || f.calls[1]["voice"] != "amy" {
		t.Fatalf("english call: %+v", f.calls[1])
	}
	// normalised text reached the engine (C P U spelled)
	if !strings.Contains(f.calls[1]["text"], "C P U load") {
		t.Fatalf("normalised text: %q", f.calls[1]["text"])
	}
	// 8 kHz WAV output with pre-roll: > 3 s of audio (3×1 s + chime + pauses)
	a, err := DecodeWAV(res.Data)
	if err != nil {
		t.Fatal(err)
	}
	if a.Rate != 8000 || a.Duration() < 3500*time.Millisecond {
		t.Fatalf("output: rate=%d dur=%s", a.Rate, a.Duration())
	}
	if a.Peak() < 20000 { // normalised to -3 dBFS ≈ 23197
		t.Fatalf("not normalised: peak %d", a.Peak())
	}

	// second call: served from cache, no engine call
	res2, err := s.Speak(context.Background(), "t1", p, text, SpeakOptions{Preroll: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Cached || res2.ID != res.ID || f.count() != 3 {
		t.Fatalf("cache: cached=%v id=%s calls=%d", res2.Cached, res2.ID, f.count())
	}
	// without pre-roll → different clip
	res3, err := s.Speak(context.Background(), "t1", p, text, SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res3.ID == res.ID || res3.Cached {
		t.Fatalf("preroll must change the cache id")
	}

	// signed URL round trip
	u := s.AudioURL(res, time.Hour)
	if !strings.HasPrefix(u, "https://np.test/api/v1/tts/audio/") || !strings.HasSuffix(u, ".wav") {
		t.Fatalf("url: %s", u)
	}
	token := strings.TrimPrefix(u, "https://np.test/api/v1/tts/audio/")
	id, err := s.VerifyAudioToken(token)
	if err != nil || id != res.ID {
		t.Fatalf("verify: %v %s", err, id)
	}
	if _, err := s.VerifyAudioToken(strings.Replace(token, res.ID[:4], "0000", 1)); err == nil {
		t.Fatal("tampered token accepted")
	}
	rec := httptest.NewRecorder()
	s.ServeAudio(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tts/audio/"+token, nil), token)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "audio/wav" || rec.Body.Len() != len(res.Data) {
		t.Fatalf("serve: %d %s %d", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	rec = httptest.NewRecorder()
	s.ServeAudio(rec, httptest.NewRequest(http.MethodGet, "/x", nil), "bogus")
	if rec.Code != 404 {
		t.Fatalf("bogus token: %d", rec.Code)
	}
}

func TestSpeakFallbackChain(t *testing.T) {
	bad := &fakeTTS{fail: true}
	good := &fakeTTS{}
	badSrv := httptest.NewServer(http.HandlerFunc(bad.handler))
	goodSrv := httptest.NewServer(http.HandlerFunc(good.handler))
	t.Cleanup(badSrv.Close)
	t.Cleanup(goodSrv.Close)

	s := newTestService(t)
	// no store: fallbacks are resolved through a stub loader below
	primary := httpProfile("primary", badSrv.URL)
	primary.Fallback = "backup"
	backup := httpProfile("backup", goodSrv.URL)
	backup.Voices = map[string]string{"de": "piper-de", "en": "piper-en"}

	// Without a store the chain cannot load "backup" → error mentions both.
	_, err := s.Speak(context.Background(), "t1", primary, "Festplatte voll", SpeakOptions{})
	if err == nil || !strings.Contains(err.Error(), "primary") {
		t.Fatalf("expected primary failure, got %v", err)
	}
	// Direct synthesis with the backup works and maps its own voices.
	res, err := s.Speak(context.Background(), "t1", backup, "Festplatte voll", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Segments[0].Voice != "piper-de" || good.calls[0]["voice"] != "piper-de" {
		t.Fatalf("voice mapping: %+v %+v", res.Segments, good.calls)
	}
}

func TestSpeakForcedLanguageAndVoice(t *testing.T) {
	f := &fakeTTS{}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	s := newTestService(t)
	p := httpProfile("main", srv.URL)
	res, err := s.Speak(context.Background(), "t1", p, "CPU load high on web01", SpeakOptions{Lang: "fr-FR", Voice: "custom", NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 1 || res.Segments[0].Lang != "fr-FR" || res.Segments[0].Voice != "custom" {
		t.Fatalf("forced: %+v", res.Segments)
	}
	if !strings.Contains(f.calls[0]["text"], "C P U") || res.Path != "" {
		t.Fatalf("nocache/normalise: %+v path=%q", f.calls[0], res.Path)
	}
}

func TestPlanSingleLanguageProfile(t *testing.T) {
	s := newTestService(t)
	p := &model.TTSProfile{Name: "x", Engine: model.TTSEngineEdge, Language: "de-DE"}
	// one configured language → detection off even for clearly English text
	plan, err := s.Plan(p, "The database server is unreachable since ten minutes", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Lang != "de-DE" || len(plan.Segments) != 1 {
		t.Fatalf("plan: %+v", plan)
	}
	// explicit candidates → detection on
	p.Detect.Languages = []string{"de-DE", "en-GB"}
	plan, err = s.Plan(p, "The database server is unreachable since ten minutes", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Lang != "en-GB" {
		t.Fatalf("plan en: %+v", plan)
	}
	if _, err := s.Plan(p, "   ", SpeakOptions{}); err == nil {
		t.Fatal("empty text must fail")
	}
}

func TestCommandEngine(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "fixture.wav")
	if err := os.WriteFile(fixture, Tone(16000, 500, 300*time.Millisecond, 0.5).WAV(), 0o600); err != nil {
		t.Fatal(err)
	}
	// {out} mode (bare names resolve through PATH)
	eng, err := NewEngine(model.TTSEngineCommand, map[string]string{"command": "cp " + fixture + " {out}"},
		EngineOptions{AllowCommands: []string{"cp", "cat"}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := eng.Synthesize(context.Background(), Request{Text: "hello", Lang: "en-US"})
	if err != nil || a.Rate != 16000 || len(a.PCM) == 0 {
		t.Fatalf("cp engine: %v %+v", err, a)
	}
	// stdout mode
	eng, err = NewEngine(model.TTSEngineCommand, map[string]string{"command": "cat " + fixture},
		EngineOptions{AllowCommands: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	if a, err = eng.Synthesize(context.Background(), Request{Text: "hello"}); err != nil || a.Rate != 16000 {
		t.Fatalf("cat engine: %v", err)
	}
	// allowlist
	if _, err := NewEngine(model.TTSEngineCommand, map[string]string{"command": "/bin/sh -c 'echo x'"}, EngineOptions{}); err == nil {
		t.Fatal("sh must be rejected by the default allowlist")
	}
	if _, err := NewEngine(model.TTSEngineCommand, map[string]string{"command": "piper --model x"}, EngineOptions{}); err != nil {
		t.Fatalf("piper must be allowed by default: %v", err)
	}
	// a bare allowlist entry does not permit an arbitrary file of that name
	if _, err := NewEngine(model.TTSEngineCommand, map[string]string{"command": "/tmp/piper --model x"}, EngineOptions{}); err == nil {
		t.Fatal("/tmp/piper must be rejected (basename match only for bare names)")
	}
	if _, err := NewEngine(model.TTSEngineCommand, map[string]string{"command": "/opt/piper/piper --model x"},
		EngineOptions{AllowCommands: []string{"/opt/piper/piper"}}); err != nil {
		t.Fatalf("exact absolute path must be allowed: %v", err)
	}
	// loader-hooking environment is refused
	for _, env := range []string{"LD_PRELOAD=/tmp/x.so", "DYLD_INSERT_LIBRARIES=/tmp/x", "PATH=/tmp", "PYTHONPATH=/tmp", "bad name=1"} {
		if _, err := NewEngine(model.TTSEngineCommand, map[string]string{"command": "piper", "env": env}, EngineOptions{}); err == nil {
			t.Fatalf("env %q must be rejected", env)
		}
	}
	if _, err := NewEngine(model.TTSEngineCommand, map[string]string{"command": "piper", "env": "PIPER_VOICES=/opt/voices;OMP_NUM_THREADS=2"}, EngineOptions{}); err != nil {
		t.Fatalf("harmless env must be allowed: %v", err)
	}
	// failure surfaces stderr
	eng, _ = NewEngine(model.TTSEngineCommand, map[string]string{"command": "cat /nonexistent/file.wav"}, EngineOptions{AllowCommands: []string{"*"}})
	if _, err := eng.Synthesize(context.Background(), Request{Text: "x"}); err == nil {
		t.Fatal("expected failure")
	}
}

func TestSplitCommand(t *testing.T) {
	argv, err := splitCommand(`piper --model "/opt/my voices/{voice}.onnx" -o {out} --flag\ x 'single q'`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"piper", "--model", "/opt/my voices/{voice}.onnx", "-o", "{out}", "--flag x", "single q"}
	if len(argv) != len(want) {
		t.Fatalf("argv %q", argv)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Errorf("%d: %q != %q", i, argv[i], want[i])
		}
	}
	if _, err := splitCommand(`piper "unterminated`); err == nil {
		t.Fatal("unbalanced quote accepted")
	}
}

func TestValidateProfile(t *testing.T) {
	ok := &model.TTSProfile{Name: "a", Engine: "edge", Detect: model.TTSDetect{Mode: "segments"},
		Normalize: model.TTSNormalize{Numbers: "words", Regex: []model.TTSRegexRule{{Pattern: `a(\d)`, Replace: "b$1"}}},
		Audio:     model.TTSAudio{SampleRate: 8000, Format: "ulaw", Preroll: "chime"}}
	if err := Validate(ok); err != nil {
		t.Fatal(err)
	}
	bad := []*model.TTSProfile{
		{Name: "a", Engine: "nope"},
		{Name: "a", Engine: "edge", Detect: model.TTSDetect{Mode: "sometimes"}},
		{Name: "a", Engine: "edge", Normalize: model.TTSNormalize{Numbers: "roman"}},
		{Name: "a", Engine: "edge", Normalize: model.TTSNormalize{Regex: []model.TTSRegexRule{{Pattern: "("}}}},
		{Name: "a", Engine: "edge", Audio: model.TTSAudio{SampleRate: 12345}},
		{Name: "a", Engine: "edge", Audio: model.TTSAudio{Format: "ulaw", SampleRate: 16000}},
		{Name: "a", Engine: "edge", Rate: 5},
		{Name: "a", Engine: "edge", Fallback: "a"},
		{Name: "a", Engine: "command", Config: map[string]string{"command": `piper "x`}},
	}
	for i, p := range bad {
		if err := Validate(p); err == nil {
			t.Errorf("case %d should fail", i)
		}
	}
}

func TestCacheEviction(t *testing.T) {
	dir := t.TempDir()
	c, err := NewCache(dir, 1, time.Hour) // 1 MB
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 300<<10) // 300 KB
	ids := []string{}
	for i := 0; i < 5; i++ {
		id := ID("clip", string(rune('a'+i)))
		ids = append(ids, id)
		if _, err := c.Put(id, data); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		// older mtimes for earlier files so LRU order is deterministic
		old := time.Now().Add(-time.Duration(10-i) * time.Minute)
		_ = os.Chtimes(c.Path(id), old, old)
	}
	c.Sweep()
	if c.Size() > 1<<20 {
		t.Fatalf("size after sweep: %d", c.Size())
	}
	if _, ok := c.Get(ids[0]); ok {
		t.Fatal("oldest clip should be evicted")
	}
	if _, ok := c.Get(ids[4]); !ok {
		t.Fatal("newest clip should survive")
	}
	// TTL sweep
	c2, _ := NewCache(filepath.Join(dir, "ttl"), 10, time.Minute)
	id := ID("x")
	_, _ = c2.Put(id, data)
	past := time.Now().Add(-2 * time.Minute)
	_ = os.Chtimes(c2.Path(id), past, past)
	c2.Sweep()
	if _, ok := c2.Get(id); ok {
		t.Fatal("expired clip should be gone")
	}
}

func TestEngineConfigKeysJSON(t *testing.T) {
	// the UI consumes this table — it must marshal and cover every engine
	raw, err := json.Marshal(ConfigKeys)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range model.TTSEngines {
		if _, ok := ConfigKeys[e]; !ok {
			t.Errorf("no config keys documented for %s", e)
		}
	}
	if len(raw) < 100 {
		t.Fatal("empty table")
	}
}

package agi

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/tts"
)

// TestAGISynthesizedPrompts: with a TTS profile every prompt is rendered
// by Northplane and streamed from ttsDir (STREAM FILE <dir>/<id>, the PIN
// prompt inside GET DATA); ttsApp is not used.
func TestAGISynthesizedPrompts(t *testing.T) {
	var spoken []string
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spoken = append(spoken, r.URL.Query().Get("text"))
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(tts.Tone(16000, 440, 100*time.Millisecond, 0.4).WAV())
	}))
	t.Cleanup(engine.Close)
	cache, err := tts.NewCache(t.TempDir(), 4, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	svc := tts.New(nil, cache, nil, nil)
	svc.BaseURL = "https://np.test"
	svc.SignKey = []byte("k")
	profile := &model.TTSProfile{Name: "pbx", Engine: model.TTSEngineHTTP,
		Config: map[string]string{"url": engine.URL + "/?text={text}&lang={lang}"}, Language: "de-DE"}
	dir := filepath.Join(t.TempDir(), "sounds")

	menu := &model.IVRMenu{Name: "m", Language: "de-DE", PIN: "4711",
		Options: []model.IVROption{{Digit: "3", Action: model.IVRAckAlert}, {Digit: "5", Action: model.IVRSay, Text: "Database server is down"}}}
	src := &model.EventSource{ID: "src-tts", TenantID: model.DefaultTenant, Name: "tts-line", Type: "asterisk-inbound",
		Config: map[string]string{"ttsApp": "Flite", "ttsDir": dir, "ttsDirPBX": "/var/lib/asterisk/sounds/northplane"}}
	acts := &fakeActs{menu: menu, open: []*model.Alert{{ID: "al-1", Title: "CPU load high on web01", Severity: model.SevCritical}}}
	env := map[string]string{"agi_network_script": "tts-line", "agi_callerid": "+4915100000000", "agi_uniqueid": "1.1"}

	client, server := net.Pipe()
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	_ = server.SetDeadline(time.Now().Add(10 * time.Second))
	fake := runFakeAsterisk(t, client, env, []string{"4711", "5", "3"})
	s, err := newSession(server)
	if err != nil {
		t.Fatal(err)
	}
	conv := &conversation{s: s, src: src, acts: acts, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		tts: svc, profile: profile}
	conv.run(context.Background())
	_ = server.Close()
	<-fake.done

	cmds := strings.Join(fake.commands(), "\n")
	if !strings.Contains(cmds, `GET DATA "/var/lib/asterisk/sounds/northplane/`) {
		t.Fatalf("PIN prompt should be the synthesized clip: %s", cmds)
	}
	if !strings.Contains(cmds, `STREAM FILE "/var/lib/asterisk/sounds/northplane/`) {
		t.Fatalf("prompts should be streamed from ttsDir: %s", cmds)
	}
	if strings.Contains(cmds, "EXEC Flite") {
		t.Fatalf("ttsApp must not be used when the profile works: %s", cmds)
	}
	if len(acts.acked) != 1 {
		t.Fatalf("ack: %v", acts.acked)
	}
	// the clip files exist in ttsDir, without extension in the AGI reference
	entries, _ := os.ReadDir(dir)
	if len(entries) < 3 {
		t.Fatalf("clips in ttsDir: %d", len(entries))
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".wav") {
			t.Fatalf("unexpected file %s", e.Name())
		}
	}
	// fixed phrases were sent normalised and in German; the say text and
	// the alert title went through detection (English → "C P U")
	joined := strings.Join(spoken, "\n")
	if !strings.Contains(joined, "Bitte geben Sie Ihre PIN ein.") || !strings.Contains(joined, "Database server is down.") {
		t.Fatalf("spoken texts: %v", spoken)
	}
}

// TestAGISynthesisFailureFallsBack: an engine failure leaves the call on
// the classic ttsApp path.
func TestAGISynthesisFailureFallsBack(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(dead.Close)
	svc := tts.New(nil, nil, nil, nil)
	profile := &model.TTSProfile{Name: "pbx", Engine: model.TTSEngineHTTP,
		Config: map[string]string{"url": dead.URL + "/?text={text}"}, Language: "en-US"}
	src := &model.EventSource{ID: "src-fb", TenantID: model.DefaultTenant, Name: "fb", Type: "asterisk-inbound",
		Config: map[string]string{"ttsApp": "Flite"}}
	acts := &fakeActs{}
	env := map[string]string{"agi_network_script": "fb", "agi_callerid": "+491", "agi_uniqueid": "2.2"}

	client, server := net.Pipe()
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	_ = server.SetDeadline(time.Now().Add(10 * time.Second))
	fake := runFakeAsterisk(t, client, env, []string{""})
	s, err := newSession(server)
	if err != nil {
		t.Fatal(err)
	}
	conv := &conversation{s: s, src: src, acts: acts, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		tts: svc, profile: profile}
	conv.run(context.Background())
	_ = server.Close()
	<-fake.done
	if !fake.has("EXEC Flite") {
		t.Fatalf("expected ttsApp fallback, got %v", fake.commands())
	}
}

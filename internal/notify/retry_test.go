package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

func TestRetryPolicyChannelOverrides(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	m := New(store, eventbus.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	if _, err := store.PutResource(ctx, model.DefaultTenant, storage.KindChannel, "alarm-sms",
		&model.NotificationChannel{
			Name: "alarm-sms", Type: model.ChannelSMS, Enabled: true,
			Config: map[string]string{
				"provider":            "twilio",
				"retryMaxAttempts":    "5",
				"retryBackoffSeconds": "10",
			},
		}, 0); err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	payload, _ := json.Marshal(notifyJob{TenantID: model.DefaultTenant, Channel: model.ChannelSMS})
	pol := m.retryPolicyFor(ctx, &storage.OutboxItem{Kind: "notification", Payload: payload})
	if pol.maxAttempts != 5 || pol.base != 10*time.Second {
		t.Fatalf("override not applied: %+v", pol)
	}

	// Other kinds and unknown channels keep the defaults.
	def := m.retryPolicyFor(ctx, &storage.OutboxItem{Kind: "webhook-sub"})
	if def != defaultRetry {
		t.Fatalf("webhook-sub policy = %+v, want default", def)
	}
	payload, _ = json.Marshal(notifyJob{TenantID: model.DefaultTenant, Channel: model.ChannelEmail})
	def = m.retryPolicyFor(ctx, &storage.OutboxItem{Kind: "notification", Payload: payload})
	if def != defaultRetry {
		t.Fatalf("unconfigured channel policy = %+v, want default", def)
	}
}

func TestRetryBackoffShape(t *testing.T) {
	p := retryPolicy{maxAttempts: 5, base: 10 * time.Second, cap: time.Minute}
	for attempt := 0; attempt < 50; attempt++ {
		d := p.backoff(attempt)
		if d <= 0 || d > time.Minute+time.Minute/5 {
			t.Fatalf("attempt %d: backoff %v out of bounds", attempt, d)
		}
	}
	// Huge attempt counts must not overflow into negatives.
	if d := defaultRetry.backoff(1000); d <= 0 || d > time.Hour+time.Hour/5 {
		t.Fatalf("large-attempt backoff out of bounds: %v", d)
	}
}

// The np.tts label overrides the spoken text for voice calls.
func TestVoiceTTSLabelOverride(t *testing.T) {
	var gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotText = r.URL.Query().Get("text")
	}))
	defer srv.Close()

	rc := &RenderContext{Alert: &model.Alert{
		ID: "a1", Labels: model.Labels{"np.tts": "Feuer im Serverraum drei"},
	}}
	m := &Manager{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ch := &model.NotificationChannel{Type: model.ChannelVoice, Config: map[string]string{
		"provider": "generic-http", "url": srv.URL + "/call?text={text}",
	}}
	if _, err := m.sendVoice(context.Background(), ch, "+491511234", "template text", rc); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotText != "Feuer im Serverraum drei" {
		t.Fatalf("voice gateway got %q, want the np.tts label", gotText)
	}
}

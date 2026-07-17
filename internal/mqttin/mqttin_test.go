package mqttin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/northplane/northplane/internal/model"
)

// testSource returns a minimal valid mqtt source; mut customises it.
func testSource(mut func(*model.EventSource)) *model.EventSource {
	src := &model.EventSource{
		ID:       "0198a7b2-1111-7333-8444-000000000001",
		TenantID: "t1",
		Name:     "factory-broker",
		Type:     "mqtt",
		Enabled:  true,
		Config: map[string]string{
			"url":    "tcp://broker.example:1883",
			"topics": "sensors/#, alarms/+/state",
		},
	}
	if mut != nil {
		mut(src)
	}
	return src
}

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig(testSource(nil))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.url != "tcp://broker.example:1883" {
		t.Errorf("url = %q", cfg.url)
	}
	if len(cfg.topics) != 2 || cfg.topics[0] != "sensors/#" || cfg.topics[1] != "alarms/+/state" {
		t.Errorf("topics = %v", cfg.topics)
	}
	if cfg.qos != 1 {
		t.Errorf("qos = %d, want default 1", cfg.qos)
	}
	if want := "northplane-0198a7b2"; cfg.clientID != want {
		t.Errorf("clientID = %q, want %q", cfg.clientID, want)
	}
	if cfg.severity != model.SevInfo {
		t.Errorf("severity = %q, want info", cfg.severity)
	}
	if cfg.tlsInsecure {
		t.Error("tlsInsecure = true, want false")
	}
}

func TestParseConfigValues(t *testing.T) {
	src := testSource(func(s *model.EventSource) {
		s.Config = map[string]string{
			"url":               " ssl://broker.example:8883 ",
			"topics":            "a/b",
			"qos":               "2",
			"clientId":          "custom-id",
			"username":          "u1",
			"password":          "p1",
			"passwordSecretRef": "broker-pass",
			"tlsInsecure":       "TRUE",
			"severity":          "critical",
		}
	})
	cfg, err := parseConfig(src)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.url != "ssl://broker.example:8883" {
		t.Errorf("url = %q (should be trimmed)", cfg.url)
	}
	if cfg.qos != 2 {
		t.Errorf("qos = %d", cfg.qos)
	}
	if cfg.clientID != "custom-id" {
		t.Errorf("clientID = %q", cfg.clientID)
	}
	if cfg.username != "u1" || cfg.passwordLit != "p1" || cfg.passwordRef != "broker-pass" {
		t.Errorf("credentials = %q/%q/%q", cfg.username, cfg.passwordLit, cfg.passwordRef)
	}
	if !cfg.tlsInsecure {
		t.Error("tlsInsecure = false, want true (case-insensitive)")
	}
	if cfg.severity != model.SevCritical {
		t.Errorf("severity = %q", cfg.severity)
	}
	if !cfg.secure() {
		t.Error("secure() = false for ssl://")
	}
}

func TestParseConfigErrors(t *testing.T) {
	cases := []struct {
		name string
		mut  func(map[string]string)
	}{
		{"missing url", func(c map[string]string) { delete(c, "url") }},
		{"bad scheme", func(c map[string]string) { c["url"] = "http://broker.example" }},
		{"no host", func(c map[string]string) { c["url"] = "tcp://" }},
		{"missing topics", func(c map[string]string) { delete(c, "topics") }},
		{"blank topics", func(c map[string]string) { c["topics"] = " , " }},
		{"qos too high", func(c map[string]string) { c["qos"] = "3" }},
		{"qos not a number", func(c map[string]string) { c["qos"] = "abc" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := testSource(nil)
			tc.mut(src.Config)
			if _, err := parseConfig(src); err == nil {
				t.Fatal("parseConfig succeeded, want error")
			}
		})
	}
}

func TestParseConfigInvalidSeverityFallsBack(t *testing.T) {
	src := testSource(func(s *model.EventSource) { s.Config["severity"] = "bogus" })
	cfg, err := parseConfig(src)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.severity != model.SevInfo {
		t.Errorf("severity = %q, want info fallback", cfg.severity)
	}
}

func TestClientOptionsAssembly(t *testing.T) {
	cfg, err := parseConfig(testSource(nil))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	opts := clientOptions(cfg)
	if len(opts.Servers) != 1 || opts.Servers[0].String() != "tcp://broker.example:1883" {
		t.Errorf("Servers = %v", opts.Servers)
	}
	if opts.ClientID != "northplane-0198a7b2" {
		t.Errorf("ClientID = %q", opts.ClientID)
	}
	if !opts.CleanSession || !opts.AutoReconnect || !opts.ConnectRetry {
		t.Errorf("CleanSession/AutoReconnect/ConnectRetry = %v/%v/%v",
			opts.CleanSession, opts.AutoReconnect, opts.ConnectRetry)
	}
	if opts.ConnectRetryInterval != 30*time.Second {
		t.Errorf("ConnectRetryInterval = %v", opts.ConnectRetryInterval)
	}
	if opts.KeepAlive != 60 { // paho stores seconds
		t.Errorf("KeepAlive = %d, want 60", opts.KeepAlive)
	}
	if opts.TLSConfig != nil {
		t.Error("TLSConfig set for tcp://, want nil")
	}
}

func TestClientOptionsTLSInsecure(t *testing.T) {
	src := testSource(func(s *model.EventSource) {
		s.Config["url"] = "ssl://broker.example:8883"
		s.Config["tlsInsecure"] = "true"
	})
	cfg, err := parseConfig(src)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	opts := clientOptions(cfg)
	if opts.TLSConfig == nil || !opts.TLSConfig.InsecureSkipVerify {
		t.Errorf("TLSConfig = %+v, want InsecureSkipVerify", opts.TLSConfig)
	}

	// tlsInsecure on a plain tcp:// URL must not build a TLS config.
	src2 := testSource(func(s *model.EventSource) { s.Config["tlsInsecure"] = "true" })
	cfg2, err := parseConfig(src2)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if opts2 := clientOptions(cfg2); opts2.TLSConfig != nil {
		t.Error("TLSConfig set for tcp:// despite tlsInsecure")
	}
}

func TestFingerprintChangesOnMapping(t *testing.T) {
	src := testSource(nil)
	cfg, err := parseConfig(src)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	base := cfg.fingerprint(src)
	if base != cfg.fingerprint(testSource(nil)) {
		t.Error("fingerprint not stable for identical config")
	}
	mapped := testSource(func(s *model.EventSource) {
		s.Mapping = map[string]string{"summary": "payload.msg"}
	})
	if base == cfg.fingerprint(mapped) {
		t.Error("fingerprint unchanged after mapping change")
	}
}

func TestBuildEventPlainText(t *testing.T) {
	src := testSource(func(s *model.EventSource) {
		s.Config["severity"] = "warning"
		s.Labels = model.Labels{"site": "hq"}
	})
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.FixedZone("CET", 3600))
	norm, err := buildEvent(src, "sensors/1", []byte("pump 3 offline"), now)
	if err != nil {
		t.Fatalf("buildEvent: %v", err)
	}
	if norm.Summary != "pump 3 offline" {
		t.Errorf("Summary = %q", norm.Summary)
	}
	if norm.Severity != model.SevWarning {
		t.Errorf("Severity = %q, want config default warning", norm.Severity)
	}
	if norm.Source != src.ID {
		t.Errorf("Source = %q, want %q", norm.Source, src.ID)
	}
	if !norm.ReceivedAt.Equal(now.UTC()) || norm.ReceivedAt.Location() != time.UTC {
		t.Errorf("ReceivedAt = %v, want %v", norm.ReceivedAt, now.UTC())
	}
	if norm.Labels["topic"] != "sensors/1" || norm.Labels["site"] != "hq" {
		t.Errorf("Labels = %v", norm.Labels)
	}
	if norm.DedupKey != "" {
		t.Errorf("DedupKey = %q, want empty", norm.DedupKey)
	}
	if string(norm.Payload) != `"pump 3 offline"` {
		t.Errorf("Payload = %s, want quoted JSON string", norm.Payload)
	}
	if _, err := json.Marshal(norm); err != nil {
		t.Errorf("NormEvent does not marshal: %v", err)
	}
}

func TestBuildEventPlainTextEmpty(t *testing.T) {
	norm, err := buildEvent(testSource(nil), "t", nil, time.Now())
	if err != nil {
		t.Fatalf("buildEvent: %v", err)
	}
	if norm.Summary != "(empty message)" {
		t.Errorf("Summary = %q", norm.Summary)
	}
	if norm.Severity != model.SevInfo {
		t.Errorf("Severity = %q, want info", norm.Severity)
	}
}

func TestBuildEventPlainTextTruncation(t *testing.T) {
	payload := strings.Repeat("ü", 300) // multi-byte: cap must be rune-safe
	norm, err := buildEvent(testSource(nil), "t", []byte(payload), time.Now())
	if err != nil {
		t.Fatalf("buildEvent: %v", err)
	}
	if got := utf8.RuneCountInString(norm.Summary); got != maxSummaryRunes {
		t.Errorf("summary rune count = %d, want %d", got, maxSummaryRunes)
	}
	if !utf8.ValidString(norm.Summary) {
		t.Error("summary is not valid UTF-8")
	}
}

func TestBuildEventNormalFormPassthrough(t *testing.T) {
	src := testSource(func(s *model.EventSource) {
		s.Labels = model.Labels{"team": "ops"}
	})
	payload := []byte(`{"summary":"disk full","severity":"critical","dedupKey":"disk-1","resolve":true,"labels":{"env":"prod","team":"dev"}}`)
	norm, err := buildEvent(src, "alarms/1/state", payload, time.Now())
	if err != nil {
		t.Fatalf("buildEvent: %v", err)
	}
	if norm.Summary != "disk full" || norm.Severity != model.SevCritical {
		t.Errorf("Summary/Severity = %q/%q", norm.Summary, norm.Severity)
	}
	if norm.DedupKey != "disk-1" || !norm.Resolve {
		t.Errorf("DedupKey/Resolve = %q/%v", norm.DedupKey, norm.Resolve)
	}
	// Source labels win over per-event ones (matches HTTP path precedence).
	if norm.Labels["team"] != "ops" || norm.Labels["env"] != "prod" {
		t.Errorf("Labels = %v", norm.Labels)
	}
	if norm.Labels["topic"] != "alarms/1/state" {
		t.Errorf("topic label = %q", norm.Labels["topic"])
	}
	if string(norm.Payload) != string(payload) {
		t.Errorf("Payload not kept verbatim: %s", norm.Payload)
	}
}

func TestBuildEventNormalFormDefaultSummary(t *testing.T) {
	norm, err := buildEvent(testSource(nil), "t", []byte(`{"severity":"warning"}`), time.Now())
	if err != nil {
		t.Fatalf("buildEvent: %v", err)
	}
	if want := "event from factory-broker"; norm.Summary != want {
		t.Errorf("Summary = %q, want %q", norm.Summary, want)
	}
	if norm.Severity != model.SevWarning {
		t.Errorf("Severity = %q", norm.Severity)
	}
}

func TestBuildEventJSONNotNormalForm(t *testing.T) {
	// Valid JSON without summary/severity is not normal form → plain text.
	for _, payload := range []string{`{"foo":1}`, `[1,2,3]`, `42`} {
		norm, err := buildEvent(testSource(nil), "t", []byte(payload), time.Now())
		if err != nil {
			t.Fatalf("buildEvent(%s): %v", payload, err)
		}
		if norm.Summary != payload {
			t.Errorf("Summary = %q, want raw text %q", norm.Summary, payload)
		}
	}
}

func TestBuildEventMapping(t *testing.T) {
	src := testSource(func(s *model.EventSource) {
		s.Labels = model.Labels{"site": "hq"}
		s.Mapping = map[string]string{
			"summary":    "payload.msg",
			"severity":   "payload.sev",
			"dedupKey":   "'k-' + payload.id",
			"resolve":    "payload.state == 'ok'",
			"labels.foo": "payload.foo",
		}
	})
	payload := []byte(`{"msg":"pump stopped","sev":"critical","id":"m-7","state":"ok","foo":"bar"}`)
	norm, err := buildEvent(src, "sensors/9", payload, time.Now())
	if err != nil {
		t.Fatalf("buildEvent: %v", err)
	}
	if norm.Summary != "pump stopped" {
		t.Errorf("Summary = %q", norm.Summary)
	}
	if norm.Severity != model.SevCritical {
		t.Errorf("Severity = %q", norm.Severity)
	}
	if norm.DedupKey != "k-m-7" {
		t.Errorf("DedupKey = %q", norm.DedupKey)
	}
	if !norm.Resolve {
		t.Error("Resolve = false, want true")
	}
	if norm.Labels["foo"] != "bar" || norm.Labels["site"] != "hq" || norm.Labels["topic"] != "sensors/9" {
		t.Errorf("Labels = %v", norm.Labels)
	}
	if string(norm.Payload) != string(payload) {
		t.Errorf("Payload not kept verbatim: %s", norm.Payload)
	}
}

func TestBuildEventMappingEvalError(t *testing.T) {
	src := testSource(func(s *model.EventSource) {
		s.Mapping = map[string]string{"summary": "payload.missing.deep"}
	})
	if _, err := buildEvent(src, "t", []byte(`{"a":1}`), time.Now()); err == nil {
		t.Fatal("buildEvent succeeded, want mapping error")
	}
}

func TestBuildEventMappingNonJSONFallsBack(t *testing.T) {
	src := testSource(func(s *model.EventSource) {
		s.Config["severity"] = "critical"
		s.Mapping = map[string]string{"summary": "payload.msg"}
	})
	norm, err := buildEvent(src, "t", []byte("plain text alarm"), time.Now())
	if err != nil {
		t.Fatalf("buildEvent: %v", err)
	}
	if norm.Summary != "plain text alarm" {
		t.Errorf("Summary = %q, want plain-text fallback", norm.Summary)
	}
	if norm.Severity != model.SevCritical {
		t.Errorf("Severity = %q, want config default", norm.Severity)
	}
}

func TestBuildEventSeverityFallback(t *testing.T) {
	src := testSource(func(s *model.EventSource) {
		s.Config["severity"] = "critical"
		s.Mapping = map[string]string{"summary": "'s'", "severity": "'bogus'"}
	})
	norm, err := buildEvent(src, "t", []byte(`{}`), time.Now())
	if err != nil {
		t.Fatalf("buildEvent: %v", err)
	}
	if norm.Severity != model.SevCritical {
		t.Errorf("Severity = %q, want config fallback critical", norm.Severity)
	}
}

func TestBuildEventTopicLabelWins(t *testing.T) {
	src := testSource(func(s *model.EventSource) {
		s.Labels = model.Labels{"topic": "static"}
	})
	norm, err := buildEvent(src, "alarms/2", []byte("x"), time.Now())
	if err != nil {
		t.Fatalf("buildEvent: %v", err)
	}
	if norm.Labels["topic"] != "alarms/2" {
		t.Errorf("topic label = %q, want message topic", norm.Labels["topic"])
	}
	if src.Labels["topic"] != "static" {
		t.Error("source labels were mutated")
	}
}

func TestBuildEventPayloadCap(t *testing.T) {
	// Non-JSON oversized payload → capped, stored as a JSON string.
	big := strings.Repeat("a", maxPayloadBytes+4096)
	norm, err := buildEvent(testSource(nil), "t", []byte(big), time.Now())
	if err != nil {
		t.Fatalf("buildEvent: %v", err)
	}
	var s string
	if err := json.Unmarshal(norm.Payload, &s); err != nil {
		t.Fatalf("Payload is not a JSON string: %v", err)
	}
	if len(s) != maxPayloadBytes {
		t.Errorf("payload length = %d, want %d", len(s), maxPayloadBytes)
	}
	if _, err := json.Marshal(norm); err != nil {
		t.Errorf("NormEvent does not marshal: %v", err)
	}

	// Valid JSON broken by the cap must still yield a marshal-safe event.
	bigJSON := []byte(`{"pad":"` + strings.Repeat("b", maxPayloadBytes+4096) + `"}`)
	norm2, err := buildEvent(testSource(nil), "t", bigJSON, time.Now())
	if err != nil {
		t.Fatalf("buildEvent: %v", err)
	}
	if !json.Valid(norm2.Payload) {
		t.Error("capped payload is not valid JSON")
	}
	if _, err := json.Marshal(norm2); err != nil {
		t.Errorf("NormEvent does not marshal: %v", err)
	}
}

func TestTokenBucket(t *testing.T) {
	b := newBucket(1, 2)
	base := time.Now()
	b.last = base
	if !b.allow(base) || !b.allow(base) {
		t.Fatal("burst of 2 not allowed")
	}
	if b.allow(base) {
		t.Fatal("third immediate message allowed, want deny")
	}
	if !b.allow(base.Add(1500 * time.Millisecond)) {
		t.Fatal("refilled token denied")
	}
	if b.allow(base.Add(1500 * time.Millisecond)) {
		t.Fatal("allowed beyond refill")
	}
	// Refill is capped at burst.
	later := base.Add(time.Minute)
	if !b.allow(later) || !b.allow(later) {
		t.Fatal("burst after idle denied")
	}
	if b.allow(later) {
		t.Fatal("burst cap exceeded")
	}
}

func TestTokenBucketDefaults(t *testing.T) {
	b := newBucket(0, 0)
	if b.rate != defaultRate || b.burst != defaultBurst {
		t.Errorf("defaults = %g/%g, want %g/%d", b.rate, b.burst, defaultRate, defaultBurst)
	}
}

func TestNewManagerStats(t *testing.T) {
	m := New(nil, nil, nil, nil)
	if m.log == nil {
		t.Fatal("nil logger not defaulted")
	}
	sources, received, dropped := m.Stats()
	if sources != 0 || received != 0 || dropped != 0 {
		t.Errorf("Stats = %d/%d/%d, want zeros", sources, received, dropped)
	}
}

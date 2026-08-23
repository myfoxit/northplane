package tts

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// Request is one synthesis job for an engine: already-normalised text in
// one language with the voice chosen for that language.
type Request struct {
	Text  string
	Lang  string  // BCP-47, e.g. de-DE
	Voice string  // engine voice id/name; "" = engine default
	Rate  float64 // speaking-rate multiplier; 0 = engine default
}

// Engine turns text into decoded audio. Implementations are stateless
// apart from their configuration and safe for concurrent use.
type Engine interface {
	// Synthesize renders req; the returned Audio is mono 16-bit PCM at
	// whatever rate the engine produced (callers resample).
	Synthesize(ctx context.Context, req Request) (*Audio, error)
}

// VoiceLister is implemented by engines that can enumerate voices.
type VoiceLister interface {
	Voices(ctx context.Context, lang string) ([]Voice, error)
}

// Voice describes one selectable voice.
type Voice struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Lang   string `json:"lang,omitempty"`
	Gender string `json:"gender,omitempty"`
}

// EngineOptions carries the pieces engines need beyond their own config.
type EngineOptions struct {
	// Resolve expands $SECRET:name$ references (nil = literal values).
	Resolve func(v string) string
	// AllowCommands is the allowlist for the command engine (basenames or
	// absolute paths; "*" = any executable). Empty = built-in list of
	// known TTS binaries.
	AllowCommands []string
	// CommandTimeout bounds one local synthesis (0 = 30 s).
	CommandTimeout time.Duration
}

func (o EngineOptions) resolve(v string) string {
	if o.Resolve == nil {
		return v
	}
	return o.Resolve(v)
}

// NewEngine builds the engine named by kind from a profile config.
func NewEngine(kind string, cfg map[string]string, opts EngineOptions) (Engine, error) {
	get := func(k string) string { return strings.TrimSpace(opts.resolve(cfg[k])) }
	switch kind {
	case model.TTSEngineCommand:
		return newCommandEngine(cfg, get, opts)
	case model.TTSEngineEdge:
		return newEdgeEngine(cfg, get)
	case model.TTSEngineOpenAI:
		return newOpenAIEngine(cfg, get)
	case model.TTSEngineElevenLabs:
		return newElevenLabsEngine(cfg, get)
	case model.TTSEngineAzure:
		return newAzureEngine(cfg, get)
	case model.TTSEngineGoogle:
		return newGoogleEngine(cfg, get)
	case model.TTSEnginePolly:
		return newPollyEngine(cfg, get)
	case model.TTSEngineHTTP:
		return newHTTPEngine(cfg, get)
	case "":
		return nil, fmt.Errorf("tts: engine not set")
	}
	return nil, fmt.Errorf("tts: unknown engine %q (%s)", kind, strings.Join(model.TTSEngines, " | "))
}

// ConfigKeys documents the per-engine config keys (name → hint) for the
// UI and the OpenAPI description; secret-bearing keys are listed in
// SecretKeys.
var ConfigKeys = map[string][]ConfigKey{
	model.TTSEngineCommand: {
		{Key: "command", Hint: "executable + args, placeholders {text} {lang} {voice} {rate} {out}; text is also on stdin", Required: true},
		{Key: "format", Hint: "output hint when headerless: wav (default, sniffed) | pcm16:22050 | mp3 | ulaw:8000"},
		{Key: "outExt", Hint: "extension of the temp file for {out} (default wav)"},
		{Key: "env", Hint: "extra environment, KEY=VALUE;KEY2=VALUE2"},
		{Key: "workDir", Hint: "working directory"},
		{Key: "timeoutSeconds", Hint: "default 30"},
	},
	model.TTSEngineEdge: {
		{Key: "voice", Hint: "e.g. de-DE-KatjaNeural (default per language)"},
		{Key: "pitch", Hint: "e.g. +0Hz, -5Hz"},
		{Key: "volume", Hint: "e.g. +0%"},
		{Key: "proxy", Hint: "http(s):// proxy URL for the websocket"},
	},
	model.TTSEngineOpenAI: {
		{Key: "apiKey", Hint: "$SECRET:name$", Secret: true, Required: true},
		{Key: "baseUrl", Hint: "default https://api.openai.com/v1 — any OpenAI-compatible server (Kokoro, LocalAI, openedai-speech)"},
		{Key: "model", Hint: "default gpt-4o-mini-tts (tts-1, tts-1-hd, kokoro …)"},
		{Key: "voice", Hint: "alloy | ash | ballad | coral | echo | fable | onyx | nova | sage | shimmer | verse"},
		{Key: "instructions", Hint: "speaking style for gpt-4o-mini-tts, e.g. 'urgent, clear, like an emergency dispatcher'"},
		{Key: "responseFormat", Hint: "default wav (pcm, mp3 accepted)"},
	},
	model.TTSEngineElevenLabs: {
		{Key: "apiKey", Hint: "$SECRET:name$", Secret: true, Required: true},
		{Key: "voice", Hint: "voice id (default Rachel)"},
		{Key: "model", Hint: "default eleven_multilingual_v2 (eleven_flash_v2_5 = lowest latency)"},
		{Key: "outputFormat", Hint: "default mp3_22050_32; pcm_16000 / ulaw_8000 avoid decoding"},
		{Key: "stability", Hint: "0–1"},
		{Key: "similarityBoost", Hint: "0–1"},
		{Key: "style", Hint: "0–1"},
		{Key: "baseUrl", Hint: "default https://api.elevenlabs.io"},
	},
	model.TTSEngineAzure: {
		{Key: "key", Hint: "$SECRET:name$", Secret: true, Required: true},
		{Key: "region", Hint: "e.g. westeurope (or endpoint)", Required: true},
		{Key: "endpoint", Hint: "full TTS endpoint URL override"},
		{Key: "voice", Hint: "e.g. de-DE-KatjaNeural"},
		{Key: "style", Hint: "e.g. serious, urgent (voice-dependent)"},
		{Key: "pitch", Hint: "e.g. +0Hz"},
		{Key: "outputFormat", Hint: "default riff-16khz-16bit-mono-pcm"},
	},
	model.TTSEngineGoogle: {
		{Key: "apiKey", Hint: "$SECRET:name$ (API key)", Secret: true, Required: true},
		{Key: "voice", Hint: "e.g. de-DE-Neural2-F"},
		{Key: "pitch", Hint: "semitones, e.g. -2.0"},
		{Key: "baseUrl", Hint: "default https://texttospeech.googleapis.com"},
	},
	model.TTSEnginePolly: {
		{Key: "accessKeyId", Hint: "AWS access key", Required: true},
		{Key: "secretAccessKey", Hint: "$SECRET:name$", Secret: true, Required: true},
		{Key: "sessionToken", Hint: "$SECRET:name$ (STS)", Secret: true},
		{Key: "region", Hint: "e.g. eu-central-1", Required: true},
		{Key: "voice", Hint: "e.g. Vicki, Joanna"},
		{Key: "engine", Hint: "neural (default) | standard | long-form | generative"},
		{Key: "endpoint", Hint: "override"},
	},
	model.TTSEngineHTTP: {
		{Key: "url", Hint: "placeholders {text} {lang} {voice} {rate} (URL-encoded)", Required: true},
		{Key: "method", Hint: "GET (default) | POST"},
		{Key: "body", Hint: "POST body template with placeholders; {text} is JSON-escaped when contentType is application/json"},
		{Key: "contentType", Hint: "POST content type (default application/json)"},
		{Key: "headers", Hint: "Name: value; Name2: value2 ($SECRET refs allowed)"},
		{Key: "responseField", Hint: "JSON field with base64 audio (dot path), empty = raw body"},
		{Key: "format", Hint: "hint when headerless: pcm16:22050 | ulaw:8000 | mp3 | wav"},
	},
}

// ConfigKey documents one engine config key.
type ConfigKey struct {
	Key      string `json:"key"`
	Hint     string `json:"hint,omitempty"`
	Secret   bool   `json:"secret,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// EngineKinds returns the engine names sorted.
func EngineKinds() []string {
	out := append([]string(nil), model.TTSEngines...)
	sort.Strings(out)
	return out
}

// --- shared HTTP plumbing ------------------------------------------------------

// httpClient is used for every cloud engine. Like the notify hook
// client it refuses link-local / cloud-metadata destinations so a TTS
// endpoint set by a config writer cannot probe 169.254.169.254.
var httpClient = &http.Client{
	Timeout:   60 * time.Second,
	Transport: guardedTransport(),
}

func guardedTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, _, err := net.SplitHostPort(addr); err == nil {
			if ip := net.ParseIP(host); ip != nil && blockedIP(ip) {
				return nil, fmt.Errorf("destination %s blocked (link-local/metadata)", ip)
			}
		}
		conn, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if ta, ok := conn.RemoteAddr().(*net.TCPAddr); ok && blockedIP(ta.IP) {
			_ = conn.Close()
			return nil, fmt.Errorf("destination %s blocked (link-local/metadata)", ta.IP)
		}
		return conn, nil
	}
	return tr
}

func blockedIP(ip net.IP) bool {
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

// doAudio performs an HTTP request and returns the body with its
// content type; non-2xx is an error carrying the first line of the body.
func doAudio(req *http.Request) ([]byte, string, error) {
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioBytes+1))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, firstLine(string(body)))
	}
	if len(body) > maxAudioBytes {
		return nil, "", fmt.Errorf("audio response too large")
	}
	return body, resp.Header.Get("Content-Type"), nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func atof(s string, def float64) float64 {
	if s == "" {
		return def
	}
	var v float64
	if _, err := fmt.Sscanf(s, "%g", &v); err != nil {
		return def
	}
	return v
}

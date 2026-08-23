// Package tts turns alarm text into speech for the voice channels
// (SPEC §9.6 evolution): pluggable engines (local command, Edge, OpenAI,
// ElevenLabs, Azure, Google, Polly, generic HTTP), automatic language
// detection with per-sentence voice switching, an operator-extensible
// pronunciation/normalisation layer, telephony-grade audio finishing and
// a disk cache. The telephony integrations (Twilio <Play>, Asterisk AMI
// channel variables, FastAGI STREAM FILE) consume the Service; the
// provider's own speech stays the fallback when no profile is set or
// every engine in the chain fails — an alarm call must never be silent.
package tts

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// Service is the synthesis front door. Zero value is unusable; build
// with New.
type Service struct {
	store   *storage.Store
	cache   *Cache
	log     *slog.Logger
	secrets func(tenantID, name string) (string, bool)

	// BaseURL is the public origin used for signed audio URLs.
	BaseURL string
	// SignKey signs audio URLs (the notify ack secret is reused).
	SignKey []byte
	// Commands is the command-engine allowlist (config tts.commands).
	Commands []string
}

// New wires the service. cache may be nil (no caching).
func New(store *storage.Store, cache *Cache, secrets func(tenantID, name string) (string, bool), log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: store, cache: cache, secrets: secrets, log: log}
}

// Cache exposes the clip cache (nil when disabled).
func (s *Service) Cache() *Cache { return s.cache }

// DefaultProfileName is used when a channel/source names no profile.
const DefaultProfileName = "default"

// Profile loads a tenant's profile by name. An empty name resolves to
// the "default" profile; (nil, nil) when no profile applies, so callers
// fall back to provider-native speech.
func (s *Service) Profile(ctx context.Context, tenantID, name string) (*model.TTSProfile, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	explicit := name != ""
	if name == "" {
		name = DefaultProfileName
	}
	p, err := storage.LoadOne[model.TTSProfile](ctx, s.store, tenantID, storage.KindTTSProfile, name)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			if explicit {
				return nil, fmt.Errorf("tts profile %q not found", name)
			}
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// Pick resolves the profile for a delivery: the np.ttsProfile alert
// label wins, then the channel/source config value, then "default".
func (s *Service) Pick(ctx context.Context, tenantID, configured string, labels model.Labels) *model.TTSProfile {
	if s == nil {
		return nil
	}
	if labels != nil {
		if n := labels["np.ttsProfile"]; n != "" {
			if p, err := s.Profile(ctx, tenantID, n); err == nil && p != nil {
				return p
			}
			s.log.Warn("tts: label np.ttsProfile names unknown profile", "profile", n)
		}
	}
	p, err := s.Profile(ctx, tenantID, configured)
	if err != nil {
		s.log.Warn("tts: profile", "profile", configured, "err", err)
		return nil
	}
	return p
}

// SpeakOptions tune one synthesis.
type SpeakOptions struct {
	// Lang forces the language (np.ttsLang); detection is skipped.
	Lang string
	// Voice forces the voice (np.ttsVoice).
	Voice string
	// Preroll prepends the profile's attention signal (outbound alarm
	// announcements; not for IVR prompts).
	Preroll bool
	// NoCache bypasses the clip cache (preview of edited settings).
	NoCache bool
	// Rate overrides the profile's speaking rate (0 = profile).
	Rate float64
}

// SpokenSegment is one synthesised run.
type SpokenSegment struct {
	Text  string `json:"text"`
	Lang  string `json:"lang"`
	Voice string `json:"voice,omitempty"`
}

// Result describes synthesised audio.
type Result struct {
	ID       string          `json:"id"`
	Path     string          `json:"path,omitempty"`
	Data     []byte          `json:"-"`
	Lang     string          `json:"lang"`
	Text     string          `json:"text"`
	Segments []SpokenSegment `json:"segments"`
	Profile  string          `json:"profile"`
	Engine   string          `json:"engine"`
	Cached   bool            `json:"cached"`
	Duration time.Duration   `json:"-"`
	Format   string          `json:"format"`
}

// DurationMS for JSON consumers.
func (r *Result) DurationMS() int64 { return r.Duration.Milliseconds() }

// Plan is the dry run of Speak: detection + normalisation, no engine.
type Plan struct {
	Lang     string          `json:"lang"`
	Text     string          `json:"text"`
	Segments []SpokenSegment `json:"segments"`
}

// Plan computes what Speak would send to the engine.
func (s *Service) Plan(p *model.TTSProfile, text string, opts SpeakOptions) (*Plan, error) {
	if p == nil {
		return nil, fmt.Errorf("tts: no profile")
	}
	segs, err := s.segments(p, text, opts)
	if err != nil {
		return nil, err
	}
	out := &Plan{Segments: segs}
	var texts []string
	for _, sg := range segs {
		texts = append(texts, sg.Text)
	}
	out.Text = strings.Join(texts, " ")
	if len(segs) > 0 {
		out.Lang = dominantLang(segs)
	}
	return out, nil
}

func dominantLang(segs []SpokenSegment) string {
	chars := map[string]int{}
	for _, sg := range segs {
		chars[sg.Lang] += len(sg.Text)
	}
	best, n := "", -1
	for l, c := range chars {
		if c > n || (c == n && l < best) {
			best, n = l, c
		}
	}
	return best
}

// segments runs detection + normalisation.
func (s *Service) segments(p *model.TTSProfile, text string, opts SpeakOptions) ([]SpokenSegment, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("tts: empty text")
	}
	if len(text) > 4000 {
		text = text[:4000]
	}
	norm, err := NewNormalizer(p.Normalize)
	if err != nil {
		return nil, err
	}
	defLang := p.Language
	if defLang == "" {
		defLang = "en-US"
	}
	det := &Detector{Candidates: p.Detect.Languages, MinConfidence: p.Detect.MinConfidence, Default: defLang}
	mode := p.Detect.Mode
	if mode == "" {
		mode = model.TTSDetectMessage
	}
	if len(det.Candidates) == 0 {
		// implicit candidates: the default language plus every language a
		// voice is configured for — detection only ever chooses among the
		// languages the operator prepared voices for.
		seen := map[string]bool{}
		add := func(l string) {
			if l != "" && !seen[langPrefix(l)] {
				seen[langPrefix(l)] = true
				det.Candidates = append(det.Candidates, l)
			}
		}
		add(defLang)
		keys := make([]string, 0, len(p.Voices))
		for k := range p.Voices {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			add(k)
		}
		if len(det.Candidates) < 2 {
			mode = model.TTSDetectOff // one language: nothing to detect
		}
	}
	if opts.Lang != "" {
		mode = model.TTSDetectOff
		defLang = opts.Lang
	}
	var raw []Segment
	switch mode {
	case model.TTSDetectOff:
		raw = []Segment{{Text: text, Lang: det.FullTag(defLang)}}
	case model.TTSDetectSegments:
		raw = det.Segments(text)
	default:
		lang, _ := det.Detect(text)
		raw = []Segment{{Text: text, Lang: lang}}
	}
	var out []SpokenSegment
	for _, r := range raw {
		t := norm.Apply(r.Text, r.Lang)
		if t == "" {
			continue
		}
		out = append(out, SpokenSegment{Text: t, Lang: r.Lang})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("tts: nothing to speak")
	}
	return out, nil
}

// multilingualEngines have voices that speak any language; the others
// need a per-language voice (or their own default for the language).
var multilingualEngines = map[string]bool{model.TTSEngineOpenAI: true, model.TTSEngineElevenLabs: true}

// voiceFor chooses the voice for a segment language.
func voiceFor(p *model.TTSProfile, lang, override string) string {
	if override != "" {
		return override
	}
	tag := strings.Replace(lang, "_", "-", 1)
	if v := p.Voices[tag]; v != "" {
		return v
	}
	if v := p.Voices[langPrefix(tag)]; v != "" {
		return v
	}
	for k, v := range p.Voices { // case-insensitive match on full tag
		if strings.EqualFold(k, tag) {
			return v
		}
	}
	if p.Voice != "" && (multilingualEngines[p.Engine] || p.Language == "" || langPrefix(p.Language) == langPrefix(tag)) {
		return p.Voice
	}
	return "" // engine default for the language
}

// Speak synthesises text with the profile (and its fallback chain),
// finishes the audio for the phone line and caches it. The returned
// Result carries the WAV bytes and, when a cache is configured, the file
// path to serve.
func (s *Service) Speak(ctx context.Context, tenantID string, p *model.TTSProfile, text string, opts SpeakOptions) (*Result, error) {
	if s == nil || p == nil {
		return nil, fmt.Errorf("tts: no profile")
	}
	segs, err := s.segments(p, text, opts)
	if err != nil {
		return nil, err
	}
	rate := p.Rate
	if opts.Rate > 0 {
		rate = opts.Rate
	}
	for i := range segs {
		segs[i].Voice = voiceFor(p, segs[i].Lang, opts.Voice)
	}
	outRate := p.Audio.SampleRate
	if outRate <= 0 {
		outRate = 8000
	}
	format := p.Audio.Format
	if format == "" || outRate != 8000 {
		format = "wav"
	}

	// cache lookup
	id := s.cacheID(p, segs, rate, opts, outRate, format)
	res := &Result{ID: id, Profile: p.Name, Engine: p.Engine, Lang: dominantLang(segs), Segments: segs, Format: format}
	var texts []string
	for _, sg := range segs {
		texts = append(texts, sg.Text)
	}
	res.Text = strings.Join(texts, " ")
	useCache := s.cache != nil && !p.CacheDisabled && !opts.NoCache
	if useCache {
		if path, ok := s.cache.Get(id); ok {
			if data, err := os.ReadFile(path); err == nil {
				res.Path, res.Data, res.Cached = path, data, true
				if a, err := DecodeWAV(data); err == nil {
					res.Duration = a.Duration()
				}
				return res, nil
			}
		}
	}

	// synthesis with fallback chain
	clips, engineName, err := s.synthesizeChain(ctx, tenantID, p, segs, rate, opts.Voice)
	if err != nil {
		return nil, err
	}
	res.Engine = engineName

	// finishing
	var parts []*Audio
	if opts.Preroll && p.Audio.Preroll != "" && p.Audio.Preroll != "none" {
		if pre := Preroll(p.Audio.Preroll, outRate); pre != nil {
			parts = append(parts, pre, Silence(outRate, 300*time.Millisecond))
		}
	}
	for i, c := range clips {
		if i > 0 {
			parts = append(parts, Silence(outRate, 200*time.Millisecond))
		}
		c = c.Resample(outRate)
		if !p.Audio.KeepSilence {
			c.TrimSilence(-45)
		}
		parts = append(parts, c)
	}
	audio := Concat(parts...)
	if !p.Audio.NoNormalize {
		audio.NormalizePeak(-3)
	}
	if p.Audio.GainDB != 0 {
		audio.Gain(p.Audio.GainDB)
	}
	lead, trail := p.Audio.LeadSilenceMs, p.Audio.TrailSilenceMs
	if lead == 0 {
		lead = 300
	}
	if trail == 0 {
		trail = 200
	}
	audio.Pad(time.Duration(lead)*time.Millisecond, time.Duration(trail)*time.Millisecond)
	res.Duration = audio.Duration()
	if format == "ulaw" {
		res.Data = audio.WAVMulaw()
	} else {
		res.Data = audio.WAV()
	}
	if useCache {
		if path, err := s.cache.Put(id, res.Data); err == nil {
			res.Path = path
		} else {
			s.log.Warn("tts: cache write", "err", err)
		}
	}
	return res, nil
}

// synthesizeChain tries the profile, then its fallbacks.
func (s *Service) synthesizeChain(ctx context.Context, tenantID string, p *model.TTSProfile,
	segs []SpokenSegment, rate float64, voiceOverride string) ([]*Audio, string, error) {
	var errs []string
	seen := map[string]bool{}
	cur := p
	for depth := 0; cur != nil && depth < 4 && !seen[cur.Name]; depth++ {
		seen[cur.Name] = true
		override := voiceOverride
		if depth > 0 && cur.Engine != p.Engine {
			override = "" // a forced voice id belongs to the primary engine
		}
		clips, err := s.synthesizeWith(ctx, tenantID, cur, segs, rate, override)
		if err == nil {
			return clips, cur.Name + "/" + cur.Engine, nil
		}
		errs = append(errs, cur.Name+": "+err.Error())
		s.log.Warn("tts: engine failed", "profile", cur.Name, "engine", cur.Engine, "err", err, "fallback", cur.Fallback)
		if cur.Fallback == "" || s.store == nil {
			break
		}
		next, perr := storage.LoadOne[model.TTSProfile](ctx, s.store, tenantID, storage.KindTTSProfile, cur.Fallback)
		if perr != nil {
			errs = append(errs, "fallback "+cur.Fallback+": "+perr.Error())
			break
		}
		cur = next
	}
	return nil, "", fmt.Errorf("tts: %s", strings.Join(errs, "; "))
}

func (s *Service) synthesizeWith(ctx context.Context, tenantID string, p *model.TTSProfile,
	segs []SpokenSegment, rate float64, voiceOverride string) ([]*Audio, error) {
	eng, err := NewEngine(p.Engine, p.Config, EngineOptions{
		Resolve:       func(v string) string { return s.resolveSecret(tenantID, v) },
		AllowCommands: s.Commands,
	})
	if err != nil {
		return nil, err
	}
	clips := make([]*Audio, 0, len(segs))
	for _, sg := range segs {
		// every profile in the chain maps voices for itself
		voice := voiceFor(p, sg.Lang, voiceOverride)
		a, err := eng.Synthesize(ctx, Request{Text: sg.Text, Lang: sg.Lang, Voice: voice, Rate: rate})
		if err != nil {
			return nil, err
		}
		if a == nil || len(a.PCM) == 0 {
			return nil, fmt.Errorf("engine returned no audio")
		}
		clips = append(clips, a)
	}
	return clips, nil
}

// resolveSecret expands $SECRET:name$ like channel configs.
func (s *Service) resolveSecret(tenantID, v string) string {
	if name, ok := strings.CutPrefix(v, "$SECRET:"); ok && strings.HasSuffix(name, "$") {
		if s.secrets != nil {
			if val, ok := s.secrets(tenantID, strings.TrimSuffix(name, "$")); ok {
				return val
			}
		}
		return ""
	}
	return v
}

// cacheID hashes everything that shapes the clip.
func (s *Service) cacheID(p *model.TTSProfile, segs []SpokenSegment, rate float64, opts SpeakOptions, outRate int, format string) string {
	parts := []string{"v1", p.Engine}
	keys := make([]string, 0, len(p.Config))
	for k := range p.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+"="+p.Config[k])
	}
	parts = append(parts, fmt.Sprintf("rate=%g", rate), fmt.Sprintf("out=%d", outRate), "fmt="+format,
		fmt.Sprintf("audio=%v/%g/%v/%d/%d", p.Audio.NoNormalize, p.Audio.GainDB, p.Audio.KeepSilence, p.Audio.LeadSilenceMs, p.Audio.TrailSilenceMs))
	if opts.Preroll {
		parts = append(parts, "preroll="+p.Audio.Preroll)
	}
	for _, sg := range segs {
		parts = append(parts, sg.Lang+"|"+sg.Voice+"|"+sg.Text)
	}
	return ID(parts...)
}

// --- voices ----------------------------------------------------------------------------

// Voices lists the engine's voices for a profile (optionally filtered
// by language); engines without a catalogue return an empty list.
func (s *Service) Voices(ctx context.Context, tenantID string, p *model.TTSProfile, lang string) ([]Voice, error) {
	eng, err := NewEngine(p.Engine, p.Config, EngineOptions{
		Resolve:       func(v string) string { return s.resolveSecret(tenantID, v) },
		AllowCommands: s.Commands,
	})
	if err != nil {
		return nil, err
	}
	if vl, ok := eng.(VoiceLister); ok {
		return vl.Voices(ctx, lang)
	}
	return []Voice{}, nil
}

// --- signed audio URLs -----------------------------------------------------------------

// AudioURL returns the public URL for a cached clip, valid for ttl.
// Empty when no base URL / key is configured or the clip is not cached.
func (s *Service) AudioURL(res *Result, ttl time.Duration) string {
	if s == nil || res == nil || res.Path == "" || s.BaseURL == "" || len(s.SignKey) == 0 {
		return ""
	}
	return strings.TrimSuffix(s.BaseURL, "/") + "/api/v1/tts/audio/" + s.AudioToken(res.ID, ttl) + ".wav"
}

// AudioToken signs a clip id: <id>.<exp>.<hmac16>.
func (s *Service) AudioToken(id string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	return id + "." + strconv.FormatInt(exp, 10) + "." + s.audioMAC(id, exp)
}

func (s *Service) audioMAC(id string, exp int64) string {
	mac := hmac.New(sha256.New, s.SignKey)
	mac.Write([]byte("tts|" + id + "|" + strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

// VerifyAudioToken returns the clip id for a valid token.
func (s *Service) VerifyAudioToken(token string) (string, error) {
	token = strings.TrimSuffix(token, ".wav")
	parts := strings.Split(token, ".")
	if len(parts) != 3 || !validID(parts[0]) {
		return "", fmt.Errorf("malformed token")
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", fmt.Errorf("malformed expiry")
	}
	if time.Now().Unix() > exp {
		return "", fmt.Errorf("token expired")
	}
	if len(s.SignKey) == 0 || !hmac.Equal([]byte(s.audioMAC(parts[0], exp)), []byte(parts[2])) {
		return "", fmt.Errorf("bad signature")
	}
	return parts[0], nil
}

// ServeAudio answers GET /api/v1/tts/audio/{token}: the clip as
// audio/wav with range support (Twilio and Asterisk's media cache both
// fetch with plain GET; browsers may seek).
func (s *Service) ServeAudio(w http.ResponseWriter, r *http.Request, token string) {
	if s == nil || s.cache == nil {
		http.Error(w, "tts cache disabled", http.StatusNotFound)
		return
	}
	id, err := s.VerifyAudioToken(token)
	if err != nil {
		http.Error(w, "invalid or expired audio link", http.StatusNotFound)
		return
	}
	path, ok := s.cache.Get(id)
	if !ok {
		http.Error(w, "audio no longer available", http.StatusNotFound)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "audio no longer available", http.StatusNotFound)
		return
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "audio no longer available", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, id+".wav", st.ModTime(), f)
}

// Validate checks a profile document before it is stored: known engine,
// compilable rules, sane enumerations.
func Validate(p *model.TTSProfile) error {
	known := false
	for _, e := range model.TTSEngines {
		if p.Engine == e {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("engine must be one of %s", strings.Join(model.TTSEngines, ", "))
	}
	if _, err := NewNormalizer(p.Normalize); err != nil {
		return err
	}
	oneOf := func(field, v string, allowed ...string) error {
		if v == "" {
			return nil
		}
		for _, a := range allowed {
			if v == a {
				return nil
			}
		}
		return fmt.Errorf("%s must be one of %s", field, strings.Join(allowed, ", "))
	}
	checks := []error{
		oneOf("detect.mode", p.Detect.Mode, model.TTSDetectOff, model.TTSDetectMessage, model.TTSDetectSegments),
		oneOf("normalize.numbers", p.Normalize.Numbers, "auto", "digits", "words", "native"),
		oneOf("normalize.acronyms", p.Normalize.Acronyms, "auto", "off"),
		oneOf("normalize.ipAddresses", p.Normalize.IPAddresses, "dot", "native"),
		oneOf("normalize.identifiers", p.Normalize.Identifiers, "split", "keep"),
		oneOf("normalize.units", p.Normalize.Units, "expand", "native"),
		oneOf("normalize.symbols", p.Normalize.Symbols, "expand", "native"),
		oneOf("normalize.urls", p.Normalize.URLs, "host", "drop", "keep"),
		oneOf("audio.preroll", p.Audio.Preroll, PrerollNames...),
		oneOf("audio.format", p.Audio.Format, "wav", "ulaw"),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	switch p.Audio.SampleRate {
	case 0, 8000, 16000, 22050, 24000, 44100, 48000:
	default:
		return fmt.Errorf("audio.sampleRate must be 8000, 16000, 22050, 24000, 44100 or 48000")
	}
	if p.Audio.Format == "ulaw" && p.Audio.SampleRate != 0 && p.Audio.SampleRate != 8000 {
		return fmt.Errorf("audio.format ulaw requires sampleRate 8000")
	}
	if p.Rate != 0 && (p.Rate < 0.5 || p.Rate > 2) {
		return fmt.Errorf("rate must be between 0.5 and 2")
	}
	if p.Detect.MinConfidence < 0 || p.Detect.MinConfidence > 1 {
		return fmt.Errorf("detect.minConfidence must be between 0 and 1")
	}
	if p.Fallback == p.Name && p.Name != "" {
		return fmt.Errorf("fallback must not reference the profile itself")
	}
	if p.Engine == model.TTSEngineCommand {
		if _, err := splitCommand(p.Config["command"]); err != nil {
			return fmt.Errorf("config.command: %w", err)
		}
	}
	return nil
}

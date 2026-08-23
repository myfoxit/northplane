package model

import "time"

// TTSProfile configures how Northplane turns alarm text into speech
// (SPEC §9.6 evolution — "voice" is only as good as its TTS). A profile
// names an engine with its credentials and voice(s), how the language of
// a message is determined, how the text is normalised before it is
// spoken (pronunciation lexicon, regex rewrites, number/acronym/unit
// handling) and how the audio is finished for the phone line.
//
// Profiles are referenced by name from voice channels (config
// ttsProfile), voice-inbound / asterisk-inbound event sources (config
// ttsProfile) and per alert through the label np.ttsProfile. Without a
// reference the profile named "default" is used when it exists; with no
// profile at all the telephony provider's own speech (Twilio <Say>,
// the Asterisk ttsApp / prompt files) is used exactly as before.
type TTSProfile struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenantId"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	// Engine is one of the TTSEngine* constants.
	Engine string `json:"engine"`
	// Config carries engine-specific keys; secret-bearing values use
	// $SECRET:name$ references like channel configs.
	Config map[string]string `json:"config,omitempty"`
	// Fallback names another profile that is tried when this engine
	// fails (and so on down the chain, loop-safe). After the chain the
	// telephony provider's native speech is the last resort.
	Fallback string `json:"fallback,omitempty"`

	// Language is the default BCP-47 tag (e.g. de-DE) — used when
	// detection is off, undecided, or for the fixed IVR phrases.
	Language string `json:"language,omitempty"`
	// Voice is the engine's default voice id / name.
	Voice string `json:"voice,omitempty"`
	// Voices maps a language (tag or 2-letter prefix) to a voice, so a
	// detected English sentence in a German message is read by an
	// English voice: {"de": "de-DE-KatjaNeural", "en": "en-US-AriaNeural"}.
	Voices map[string]string `json:"voices,omitempty"`
	// Rate is the speaking-rate multiplier (1.0 = engine default; 0 =
	// unset). Engines without a rate parameter ignore it.
	Rate float64 `json:"rate,omitempty"`

	Detect    TTSDetect    `json:"detect"`
	Normalize TTSNormalize `json:"normalize"`
	Audio     TTSAudio     `json:"audio"`
	// CacheDisabled turns off the synthesized-audio cache for this
	// profile (default: cached by text+voice+settings, so IVR prompts and
	// repeated announcements are rendered once).
	CacheDisabled bool `json:"cacheDisabled,omitempty"`

	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Engine identifiers.
const (
	TTSEngineCommand    = "command"    // local executable (piper, espeak-ng, flite, say, …)
	TTSEngineEdge       = "edge"       // Microsoft Edge read-aloud neural voices (free, unofficial)
	TTSEngineOpenAI     = "openai"     // OpenAI audio/speech API and compatible servers
	TTSEngineElevenLabs = "elevenlabs" // ElevenLabs text-to-speech
	TTSEngineAzure      = "azure"      // Azure AI Speech (Cognitive Services)
	TTSEngineGoogle     = "google"     // Google Cloud Text-to-Speech
	TTSEnginePolly      = "polly"      // Amazon Polly
	TTSEngineHTTP       = "http"       // generic HTTP synthesis endpoint (Piper server, MaryTTS, Coqui, …)
)

// TTSEngines lists the selectable engines.
var TTSEngines = []string{
	TTSEngineCommand, TTSEngineEdge, TTSEngineOpenAI, TTSEngineElevenLabs,
	TTSEngineAzure, TTSEngineGoogle, TTSEnginePolly, TTSEngineHTTP,
}

// TTSDetect controls automatic language detection.
type TTSDetect struct {
	// Mode: "off" (always Language), "message" (detect once per
	// message — default), "segments" (detect per sentence; runs of one
	// language are synthesised with that language's voice and stitched
	// together, so a German announcement quoting an English alarm title
	// is read correctly in both languages).
	Mode string `json:"mode,omitempty"`
	// Languages restricts detection to these candidates (tags or
	// prefixes, e.g. ["de-DE", "en-US"]); a short list makes detection
	// of short alarm texts far more reliable. Empty = Language plus
	// every language a voice is configured for, else unrestricted.
	Languages []string `json:"languages,omitempty"`
	// MinConfidence (0–1) below which the default Language is kept.
	MinConfidence float64 `json:"minConfidence,omitempty"`
}

// Detection modes.
const (
	TTSDetectOff      = "off"
	TTSDetectMessage  = "message"
	TTSDetectSegments = "segments"
)

// TTSNormalize shapes the text before synthesis. All steps run in the
// order of the fields below; the operator's lexicon and regex rules come
// first so they can pre-empt every built-in rule.
type TTSNormalize struct {
	// Disabled skips normalisation entirely (text is spoken verbatim).
	Disabled bool `json:"disabled,omitempty"`
	// Lexicon: literal replacements (whole-word, case-insensitive unless
	// flagged) — "np-01" → "Server eins", "k8s" → "Kubernetes".
	Lexicon []TTSLexiconEntry `json:"lexicon,omitempty"`
	// Regex: RE2 rewrites with $1-style group references, e.g.
	// {pattern: "srv(\\d+)", replace: "Server $1"}.
	Regex []TTSRegexRule `json:"regex,omitempty"`
	// NoBuiltinLexicon disables the shipped IT-operations lexicon
	// (CRIT → critical, k8s → Kubernetes, HTTP → H T T P, …).
	NoBuiltinLexicon bool `json:"noBuiltinLexicon,omitempty"`
	// SpellOut lists tokens that are always spelled letter by letter.
	SpellOut []string `json:"spellOut,omitempty"`
	// Acronyms: "auto" (default; ALL-CAPS tokens and vowel-less
	// identifiers are spelled unless known to be pronounceable) | "off".
	Acronyms string `json:"acronyms,omitempty"`
	// Numbers: "auto" (default; long or leading-zero numbers digit by
	// digit, the rest left to the engine) | "digits" (every integer
	// digit by digit) | "words" (integers written out — English and
	// German; other languages fall back to auto) | "native".
	Numbers string `json:"numbers,omitempty"`
	// DigitsFrom is the length from which "auto" reads integers digit by
	// digit (default 5: 4711 is a number, 47110 is "4 7 1 1 0").
	DigitsFrom int `json:"digitsFrom,omitempty"`
	// IPAddresses: "dot" (default; 10.0.0.1 → "10 dot 0 dot 0 dot 1" in
	// the message language) | "native".
	IPAddresses string `json:"ipAddresses,omitempty"`
	// Identifiers: "split" (default; web01 → "web 0 1", np-02 → "N P 0 2")
	// | "keep".
	Identifiers string `json:"identifiers,omitempty"`
	// Units: "expand" (default; 20ms → 20 milliseconds, 512MB → 512
	// megabytes) | "native".
	Units string `json:"units,omitempty"`
	// Symbols: "expand" (default; % → percent, & → and, = → equals, _ →
	// space …) | "native".
	Symbols string `json:"symbols,omitempty"`
	// URLs: "host" (default; https://grafana.example.net/d/x → "grafana
	// dot example dot net") | "drop" | "keep".
	URLs string `json:"urls,omitempty"`
}

// TTSLexiconEntry is one literal pronunciation replacement.
type TTSLexiconEntry struct {
	From string `json:"from"`
	To   string `json:"to"`
	// MatchCase makes the match case-sensitive (default insensitive).
	MatchCase bool `json:"matchCase,omitempty"`
	// Substring matches inside words too (default whole words only).
	Substring bool `json:"substring,omitempty"`
}

// TTSRegexRule is one regular-expression rewrite.
type TTSRegexRule struct {
	Pattern string `json:"pattern"`
	Replace string `json:"replace"`
}

// TTSAudio finishes the synthesized audio for the phone line.
type TTSAudio struct {
	// SampleRate of the delivered audio: 8000 (default, telephony),
	// 16000, 22050 or 24000.
	SampleRate int `json:"sampleRate,omitempty"`
	// NoNormalize keeps the engine's loudness (default: peak-normalised
	// to -3 dBFS so every voice is equally loud on the line).
	NoNormalize bool `json:"noNormalize,omitempty"`
	// GainDB is an additional gain after normalisation.
	GainDB float64 `json:"gainDb,omitempty"`
	// KeepSilence skips trimming of leading/trailing silence.
	KeepSilence bool `json:"keepSilence,omitempty"`
	// LeadSilenceMs / TrailSilenceMs pad the clip (default 300 / 200 ms)
	// so the first word is not swallowed while the far end answers.
	LeadSilenceMs  int `json:"leadSilenceMs,omitempty"`
	TrailSilenceMs int `json:"trailSilenceMs,omitempty"`
	// Preroll is an attention signal before outbound announcements:
	// "none" (default), "chime", "alert", "gong".
	Preroll string `json:"preroll,omitempty"`
	// Format of served audio: "wav" (16-bit PCM, default) | "ulaw"
	// (G.711 µ-law WAV, 8 kHz only).
	Format string `json:"format,omitempty"`
}

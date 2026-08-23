package tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// --- OpenAI (and compatible servers) -----------------------------------------

// openAIEngine calls POST {baseUrl}/audio/speech. Besides api.openai.com
// this covers every OpenAI-compatible local server (Kokoro-FastAPI,
// LocalAI, openedai-speech, Speaches) — a popular self-hosted path.
type openAIEngine struct {
	apiKey, baseURL, model, voice, instructions, format string
}

func newOpenAIEngine(cfg map[string]string, get func(string) string) (Engine, error) {
	e := &openAIEngine{apiKey: get("apiKey"), baseURL: strings.TrimSuffix(get("baseUrl"), "/"),
		model: get("model"), voice: get("voice"), instructions: get("instructions"), format: get("responseFormat")}
	if e.baseURL == "" {
		e.baseURL = "https://api.openai.com/v1"
	}
	if e.apiKey == "" && strings.Contains(e.baseURL, "api.openai.com") {
		return nil, fmt.Errorf("openai: config.apiKey required")
	}
	if e.model == "" {
		e.model = "gpt-4o-mini-tts"
	}
	if e.voice == "" {
		e.voice = "alloy"
	}
	if e.format == "" {
		e.format = "wav"
	}
	return e, nil
}

func (e *openAIEngine) Synthesize(ctx context.Context, req Request) (*Audio, error) {
	voice := req.Voice
	if voice == "" {
		voice = e.voice
	}
	payload := map[string]any{
		"model": e.model, "input": req.Text, "voice": voice, "response_format": e.format,
	}
	if req.Rate > 0 && req.Rate != 1 {
		payload["speed"] = req.Rate
	}
	if e.instructions != "" {
		payload["instructions"] = e.instructions
	}
	raw, _ := json.Marshal(payload)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/audio/speech", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		hr.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	body, ct, err := doAudio(hr)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	hint := ""
	switch e.format {
	case "pcm":
		hint = "pcm16:24000"
	case "mp3", "wav":
		hint = e.format
	}
	a, err := Decode(body, ct, hint)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	return a, nil
}

// Voices returns the fixed OpenAI voice list (compatible servers may
// accept others; the field is free text in the UI).
func (e *openAIEngine) Voices(_ context.Context, _ string) ([]Voice, error) {
	var out []Voice
	for _, v := range []string{"alloy", "ash", "ballad", "coral", "echo", "fable", "onyx", "nova", "sage", "shimmer", "verse"} {
		out = append(out, Voice{ID: v, Name: v})
	}
	return out, nil
}

// --- ElevenLabs --------------------------------------------------------------------

type elevenEngine struct {
	apiKey, baseURL, voice, model, format string
	stability, similarity, style          string
}

func newElevenLabsEngine(cfg map[string]string, get func(string) string) (Engine, error) {
	e := &elevenEngine{apiKey: get("apiKey"), baseURL: strings.TrimSuffix(get("baseUrl"), "/"),
		voice: get("voice"), model: get("model"), format: get("outputFormat"),
		stability: get("stability"), similarity: get("similarityBoost"), style: get("style")}
	if e.apiKey == "" {
		return nil, fmt.Errorf("elevenlabs: config.apiKey required")
	}
	if e.baseURL == "" {
		e.baseURL = "https://api.elevenlabs.io"
	}
	if e.voice == "" {
		e.voice = "21m00Tcm4TlvDq8ikWAM" // Rachel
	}
	if e.model == "" {
		e.model = "eleven_multilingual_v2"
	}
	if e.format == "" {
		e.format = "mp3_22050_32"
	}
	return e, nil
}

func (e *elevenEngine) Synthesize(ctx context.Context, req Request) (*Audio, error) {
	voice := req.Voice
	if voice == "" {
		voice = e.voice
	}
	payload := map[string]any{"text": req.Text, "model_id": e.model}
	// language enforcement exists for the v2.5 turbo/flash models only
	if strings.Contains(e.model, "v2_5") && req.Lang != "" {
		payload["language_code"] = langPrefix(req.Lang)
	}
	settings := map[string]any{}
	if e.stability != "" {
		settings["stability"] = atof(e.stability, 0.5)
	}
	if e.similarity != "" {
		settings["similarity_boost"] = atof(e.similarity, 0.75)
	}
	if e.style != "" {
		settings["style"] = atof(e.style, 0)
	}
	if req.Rate > 0 && req.Rate != 1 {
		settings["speed"] = req.Rate
	}
	if len(settings) > 0 {
		payload["voice_settings"] = settings
	}
	raw, _ := json.Marshal(payload)
	u := e.baseURL + "/v1/text-to-speech/" + url.PathEscape(voice) + "?output_format=" + url.QueryEscape(e.format)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("xi-api-key", e.apiKey)
	hr.Header.Set("Accept", "*/*")
	body, ct, err := doAudio(hr)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: %w", err)
	}
	hint := ""
	switch {
	case strings.HasPrefix(e.format, "pcm_"):
		hint = "pcm16:" + strings.TrimPrefix(e.format, "pcm_")
	case strings.HasPrefix(e.format, "ulaw_"):
		hint = "ulaw:" + strings.TrimPrefix(e.format, "ulaw_")
	case strings.HasPrefix(e.format, "alaw_"):
		hint = "alaw:" + strings.TrimPrefix(e.format, "alaw_")
	case strings.HasPrefix(e.format, "mp3"):
		hint = "mp3"
	}
	a, err := Decode(body, ct, hint)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: %w", err)
	}
	return a, nil
}

func (e *elevenEngine) Voices(ctx context.Context, _ string) ([]Voice, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, e.baseURL+"/v1/voices", nil)
	if err != nil {
		return nil, err
	}
	hr.Header.Set("xi-api-key", e.apiKey)
	body, _, err := doAudio(hr)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs voices: %w", err)
	}
	var raw struct {
		Voices []struct {
			ID     string            `json:"voice_id"`
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"voices"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("elevenlabs voices: %w", err)
	}
	var out []Voice
	for _, v := range raw.Voices {
		out = append(out, Voice{ID: v.ID, Name: v.Name, Gender: v.Labels["gender"], Lang: v.Labels["language"]})
	}
	return out, nil
}

// --- Azure AI Speech ------------------------------------------------------------------

type azureEngine struct {
	key, endpoint, voicesURL, voice, style, pitch, format string
}

func newAzureEngine(cfg map[string]string, get func(string) string) (Engine, error) {
	e := &azureEngine{key: get("key"), endpoint: strings.TrimSuffix(get("endpoint"), "/"), voice: get("voice"),
		style: get("style"), pitch: get("pitch"), format: get("outputFormat")}
	if e.key == "" {
		return nil, fmt.Errorf("azure: config.key required")
	}
	region := get("region")
	if e.endpoint == "" {
		if region == "" {
			return nil, fmt.Errorf("azure: config.region (or endpoint) required")
		}
		e.endpoint = "https://" + region + ".tts.speech.microsoft.com/cognitiveservices/v1"
	}
	e.voicesURL = get("voicesUrl")
	if e.voicesURL == "" && region != "" {
		e.voicesURL = "https://" + region + ".tts.speech.microsoft.com/cognitiveservices/voices/list"
	}
	if e.format == "" {
		e.format = "riff-16khz-16bit-mono-pcm"
	}
	if e.pitch == "" {
		e.pitch = "+0Hz"
	}
	return e, nil
}

func (e *azureEngine) Synthesize(ctx context.Context, req Request) (*Audio, error) {
	voice := req.Voice
	if voice == "" {
		voice = e.voice
	}
	if voice == "" {
		voice = defaultNeuralVoice(req.Lang)
	}
	lang := req.Lang
	if lang == "" {
		lang = localeOfVoice(voice)
	}
	inner := `<prosody rate='` + xmlText(edgeRate(req.Rate)) + `' pitch='` + xmlText(e.pitch) + `'>` + xmlText(req.Text) + `</prosody>`
	if e.style != "" {
		inner = `<mstts:express-as style='` + xmlText(e.style) + `'>` + inner + `</mstts:express-as>`
	}
	ssml := `<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xmlns:mstts='https://www.w3.org/2001/mstts' xml:lang='` +
		xmlText(lang) + `'><voice name='` + xmlText(voice) + `'>` + inner + `</voice></speak>`
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, strings.NewReader(ssml))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/ssml+xml")
	hr.Header.Set("Ocp-Apim-Subscription-Key", e.key)
	hr.Header.Set("X-Microsoft-OutputFormat", e.format)
	hr.Header.Set("User-Agent", "northplane")
	body, ct, err := doAudio(hr)
	if err != nil {
		return nil, fmt.Errorf("azure: %w", err)
	}
	hint := ""
	switch {
	case strings.HasPrefix(e.format, "raw-") && strings.Contains(e.format, "pcm"):
		hint = "pcm16:" + azureRate(e.format)
	case strings.Contains(e.format, "mulaw"):
		hint = "ulaw:" + azureRate(e.format)
	case strings.Contains(e.format, "alaw"):
		hint = "alaw:" + azureRate(e.format)
	case strings.Contains(e.format, "mp3"):
		hint = "mp3"
	}
	a, err := Decode(body, ct, hint)
	if err != nil {
		return nil, fmt.Errorf("azure: %w", err)
	}
	return a, nil
}

// azureRate extracts "16000" from "raw-16khz-16bit-mono-pcm".
func azureRate(format string) string {
	for _, p := range strings.Split(format, "-") {
		if strings.HasSuffix(p, "khz") {
			return strings.TrimSuffix(p, "khz") + "000"
		}
	}
	return "16000"
}

func (e *azureEngine) Voices(ctx context.Context, lang string) ([]Voice, error) {
	if e.voicesURL == "" {
		return nil, fmt.Errorf("azure voices: region required")
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, e.voicesURL, nil)
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Ocp-Apim-Subscription-Key", e.key)
	body, _, err := doAudio(hr)
	if err != nil {
		return nil, fmt.Errorf("azure voices: %w", err)
	}
	var raw []struct {
		ShortName string `json:"ShortName"`
		LocalName string `json:"LocalName"`
		Gender    string `json:"Gender"`
		Locale    string `json:"Locale"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("azure voices: %w", err)
	}
	prefix := strings.ToLower(langPrefix(lang))
	var out []Voice
	for _, v := range raw {
		if prefix != "" && !strings.HasPrefix(strings.ToLower(v.Locale), prefix) {
			continue
		}
		out = append(out, Voice{ID: v.ShortName, Name: v.LocalName, Lang: v.Locale, Gender: v.Gender})
	}
	return out, nil
}

// --- Google Cloud Text-to-Speech ---------------------------------------------------------

type googleEngine struct {
	apiKey, baseURL, voice, pitch string
}

func newGoogleEngine(cfg map[string]string, get func(string) string) (Engine, error) {
	e := &googleEngine{apiKey: get("apiKey"), baseURL: strings.TrimSuffix(get("baseUrl"), "/"),
		voice: get("voice"), pitch: get("pitch")}
	if e.apiKey == "" {
		return nil, fmt.Errorf("google: config.apiKey required")
	}
	if e.baseURL == "" {
		e.baseURL = "https://texttospeech.googleapis.com"
	}
	return e, nil
}

func (e *googleEngine) Synthesize(ctx context.Context, req Request) (*Audio, error) {
	voice := req.Voice
	if voice == "" {
		voice = e.voice
	}
	lang := req.Lang
	if lang == "" && voice != "" {
		lang = localeOfVoice(voice)
	}
	if lang == "" {
		lang = "en-US"
	}
	v := map[string]any{"languageCode": lang}
	if voice != "" {
		v["name"] = voice
	}
	audio := map[string]any{"audioEncoding": "LINEAR16", "sampleRateHertz": 16000}
	if req.Rate > 0 && req.Rate != 1 {
		audio["speakingRate"] = req.Rate
	}
	if e.pitch != "" {
		audio["pitch"] = atof(e.pitch, 0)
	}
	payload := map[string]any{"input": map[string]string{"text": req.Text}, "voice": v, "audioConfig": audio}
	raw, _ := json.Marshal(payload)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/v1/text:synthesize", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("X-Goog-Api-Key", e.apiKey)
	body, _, err := doAudio(hr)
	if err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	var out struct {
		AudioContent string `json:"audioContent"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AudioContent == "" {
		return nil, fmt.Errorf("google: unexpected response: %s", firstLine(string(body)))
	}
	data, err := base64.StdEncoding.DecodeString(out.AudioContent)
	if err != nil {
		return nil, fmt.Errorf("google: audio decode: %w", err)
	}
	a, err := Decode(data, "audio/wav", "wav")
	if err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	return a, nil
}

func (e *googleEngine) Voices(ctx context.Context, lang string) ([]Voice, error) {
	u := e.baseURL + "/v1/voices"
	if lang != "" {
		u += "?languageCode=" + url.QueryEscape(lang)
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	hr.Header.Set("X-Goog-Api-Key", e.apiKey)
	body, _, err := doAudio(hr)
	if err != nil {
		return nil, fmt.Errorf("google voices: %w", err)
	}
	var raw struct {
		Voices []struct {
			Name          string   `json:"name"`
			LanguageCodes []string `json:"languageCodes"`
			Gender        string   `json:"ssmlGender"`
		} `json:"voices"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("google voices: %w", err)
	}
	var out []Voice
	for _, v := range raw.Voices {
		l := ""
		if len(v.LanguageCodes) > 0 {
			l = v.LanguageCodes[0]
		}
		out = append(out, Voice{ID: v.Name, Name: v.Name, Lang: l, Gender: v.Gender})
	}
	return out, nil
}

// --- generic HTTP -----------------------------------------------------------------------------

// httpEngine drives any synthesis endpoint that takes text and returns
// audio: a Piper HTTP server, MaryTTS (/process?INPUT_TEXT=…), Coqui
// (/api/tts?text=…), a company-internal TTS gateway. Placeholders in the
// URL are query-escaped; in the body they are inserted raw except {text},
// which is JSON-escaped for application/json bodies.
type httpEngine struct {
	url, method, body, contentType, responseField, format string
	headers                                               map[string]string
}

func newHTTPEngine(cfg map[string]string, get func(string) string) (Engine, error) {
	e := &httpEngine{url: get("url"), method: strings.ToUpper(get("method")), body: cfg["body"],
		contentType: get("contentType"), responseField: get("responseField"), format: get("format"),
		headers: map[string]string{}}
	if e.url == "" {
		return nil, fmt.Errorf("http: config.url required")
	}
	if e.method == "" {
		if e.body != "" {
			e.method = http.MethodPost
		} else {
			e.method = http.MethodGet
		}
	}
	if e.contentType == "" {
		e.contentType = "application/json"
	}
	for _, h := range strings.Split(get("headers"), ";") {
		if k, v, ok := strings.Cut(h, ":"); ok {
			e.headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return e, nil
}

func (e *httpEngine) Synthesize(ctx context.Context, req Request) (*Audio, error) {
	rate := "1"
	if req.Rate > 0 {
		rate = fmt.Sprintf("%g", req.Rate)
	}
	u := strings.NewReplacer("{text}", url.QueryEscape(req.Text), "{lang}", url.QueryEscape(req.Lang),
		"{voice}", url.QueryEscape(req.Voice), "{rate}", rate).Replace(e.url)
	var bodyReader *strings.Reader
	if e.method != http.MethodGet && e.body != "" {
		text := req.Text
		if strings.Contains(e.contentType, "json") {
			q, _ := json.Marshal(req.Text)
			text = strings.Trim(string(q), `"`)
		}
		body := strings.NewReplacer("{text}", text, "{lang}", req.Lang, "{voice}", req.Voice, "{rate}", rate).Replace(e.body)
		bodyReader = strings.NewReader(body)
	}
	var hr *http.Request
	var err error
	if bodyReader != nil {
		hr, err = http.NewRequestWithContext(ctx, e.method, u, bodyReader)
	} else {
		hr, err = http.NewRequestWithContext(ctx, e.method, u, nil)
	}
	if err != nil {
		return nil, err
	}
	if bodyReader != nil {
		hr.Header.Set("Content-Type", e.contentType)
	}
	for k, v := range e.headers {
		hr.Header.Set(k, v)
	}
	data, ct, err := doAudio(hr)
	if err != nil {
		return nil, fmt.Errorf("http tts: %w", err)
	}
	if e.responseField != "" {
		var doc any
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("http tts: JSON response expected: %w", err)
		}
		cur := doc
		for _, part := range strings.Split(e.responseField, ".") {
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("http tts: field %q not found", e.responseField)
			}
			cur = m[part]
		}
		s, ok := cur.(string)
		if !ok {
			return nil, fmt.Errorf("http tts: field %q is not a string", e.responseField)
		}
		if data, err = base64.StdEncoding.DecodeString(s); err != nil {
			return nil, fmt.Errorf("http tts: base64: %w", err)
		}
		ct = ""
	}
	a, err := Decode(data, ct, e.format)
	if err != nil {
		return nil, fmt.Errorf("http tts: %w", err)
	}
	return a, nil
}

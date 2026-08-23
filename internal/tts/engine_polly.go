package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/awssig"
)

// pollyEngine calls Amazon Polly (POST /v1/speech, SigV4-signed). Polly
// returns raw 16-bit PCM at 16 kHz (or 8 kHz) — no decoding needed.
type pollyEngine struct {
	cred     awssig.Credentials
	endpoint string
	voice    string
	engine   string
}

func newPollyEngine(cfg map[string]string, get func(string) string) (Engine, error) {
	e := &pollyEngine{
		cred: awssig.Credentials{AccessKey: get("accessKeyId"), SecretKey: get("secretAccessKey"),
			SessionToken: get("sessionToken"), Region: get("region"), Service: "polly"},
		endpoint: strings.TrimSuffix(get("endpoint"), "/"), voice: get("voice"), engine: get("engine"),
	}
	if e.cred.AccessKey == "" || e.cred.SecretKey == "" || e.cred.Region == "" {
		return nil, fmt.Errorf("polly: config.accessKeyId, secretAccessKey, region required")
	}
	if e.endpoint == "" {
		e.endpoint = "https://polly." + e.cred.Region + ".amazonaws.com"
	}
	if e.engine == "" {
		e.engine = "neural"
	}
	return e, nil
}

// pollyDefaults picks a neural voice per locale.
var pollyDefaults = map[string]string{
	"de-DE": "Vicki", "de-AT": "Hannah", "en-US": "Joanna", "en-GB": "Amy", "en-AU": "Olivia", "en-IN": "Kajal",
	"fr-FR": "Lea", "fr-CA": "Gabrielle", "es-ES": "Lucia", "es-MX": "Mia", "es-US": "Lupe", "it-IT": "Bianca",
	"nl-NL": "Laura", "nl-BE": "Lisa", "pt-PT": "Ines", "pt-BR": "Camila", "pl-PL": "Ola", "sv-SE": "Elin",
	"da-DK": "Sofie", "nb-NO": "Ida", "fi-FI": "Suvi", "tr-TR": "Burcu", "ja-JP": "Kazuha", "ko-KR": "Seoyeon",
	"zh-CN": "Zhiyu", "ar-AE": "Hala", "ca-ES": "Arlet", "cs-CZ": "Jitka", "cy-GB": "Gwyneth", "ru-RU": "Tatyana",
	"is-IS": "Dora", "ro-RO": "Carmen", "hi-IN": "Kajal",
}

func (e *pollyEngine) Synthesize(ctx context.Context, req Request) (*Audio, error) {
	voice := req.Voice
	if voice == "" {
		voice = e.voice
	}
	lang := req.Lang
	if voice == "" {
		tag := lang
		if tag == "" {
			tag = "en-US"
		}
		if v, ok := pollyDefaults[tag]; ok {
			voice = v
		} else if full, ok := defaultRegion[langPrefix(tag)]; ok && pollyDefaults[full] != "" {
			voice = pollyDefaults[full]
		} else {
			voice = "Joanna"
		}
	}
	text := req.Text
	textType := "text"
	if req.Rate > 0 && req.Rate != 1 {
		text = `<speak><prosody rate="` + edgeRate(req.Rate) + `">` + xmlText(req.Text) + `</prosody></speak>`
		textType = "ssml"
	}
	payload := map[string]any{
		"Text": text, "TextType": textType, "VoiceId": voice, "OutputFormat": "pcm",
		"SampleRate": "16000", "Engine": e.engine,
	}
	if lang != "" {
		payload["LanguageCode"] = lang
	}
	raw, _ := json.Marshal(payload)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint+"/v1/speech", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	awssig.Sign(hr, raw, e.cred, time.Now().UTC())
	body, _, err := doAudio(hr)
	if err != nil {
		return nil, fmt.Errorf("polly: %w", err)
	}
	return DecodePCM16(body, 16000, 1), nil
}

func (e *pollyEngine) Voices(ctx context.Context, lang string) ([]Voice, error) {
	u := e.endpoint + "/v1/voices"
	if lang != "" {
		u += "?LanguageCode=" + lang
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	awssig.Sign(hr, nil, e.cred, time.Now().UTC())
	body, _, err := doAudio(hr)
	if err != nil {
		return nil, fmt.Errorf("polly voices: %w", err)
	}
	var raw struct {
		Voices []struct {
			ID           string `json:"Id"`
			Name         string `json:"Name"`
			Gender       string `json:"Gender"`
			LanguageCode string `json:"LanguageCode"`
		} `json:"Voices"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("polly voices: %w", err)
	}
	var out []Voice
	for _, v := range raw.Voices {
		out = append(out, Voice{ID: v.ID, Name: v.Name, Lang: v.LanguageCode, Gender: v.Gender})
	}
	return out, nil
}

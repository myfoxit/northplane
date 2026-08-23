package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
	"github.com/northplane/northplane/internal/tts"
)

// Text-to-speech (voice alarms): tts-profile resources plus the tools
// the profile editor needs — a dry run of the normalisation, an audible
// preview, the engine's voice catalogue, the engine/config-key table —
// and the public, signed audio route the telephony providers fetch.

// TTSPreviewRequest previews a profile (saved by name, or an unsaved
// document) on a text.
type TTSPreviewRequest struct {
	Text string `json:"text"`
	// ProfileName selects a stored profile; Profile is an inline
	// document (takes precedence) so unsaved edits can be heard.
	ProfileName string            `json:"profileName,omitempty"`
	Profile     *model.TTSProfile `json:"profile,omitempty"`
	// Language / Voice force the language / voice (like np.ttsLang /
	// np.ttsVoice); Preroll adds the attention signal.
	Language string `json:"language,omitempty"`
	Voice    string `json:"voice,omitempty"`
	Preroll  bool   `json:"preroll,omitempty"`
}

// TTSPreviewResponse carries the clip and what was spoken.
type TTSPreviewResponse struct {
	// Audio is the WAV clip, base64.
	Audio      string              `json:"audio"`
	Format     string              `json:"format"`
	Lang       string              `json:"lang"`
	Text       string              `json:"text"`
	Segments   []tts.SpokenSegment `json:"segments"`
	Engine     string              `json:"engine"`
	Cached     bool                `json:"cached"`
	DurationMS int64               `json:"durationMs"`
}

// TTSVoicesRequest asks for the voices of a profile's engine.
type TTSVoicesRequest struct {
	ProfileName string            `json:"profileName,omitempty"`
	Profile     *model.TTSProfile `json:"profile,omitempty"`
	Language    string            `json:"language,omitempty"`
}

// TTSEnginesResponse documents engines and their config keys.
type TTSEnginesResponse struct {
	Engines    []string                   `json:"engines"`
	ConfigKeys map[string][]tts.ConfigKey `json:"configKeys"`
	Prerolls   []string                   `json:"prerolls"`
}

func (a *API) registerTTS() {
	a.resourceCRUD("tts-profiles", storage.KindTTSProfile, "config", model.TTSProfile{})

	a.handle("GET /api/v1/tts/engines", "List TTS engines and their config keys", "objects:read", nil, TTSEnginesResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			a.writeJSON(w, http.StatusOK, TTSEnginesResponse{
				Engines: tts.EngineKinds(), ConfigKeys: tts.ConfigKeys, Prerolls: tts.PrerollNames,
			})
		})

	a.handle("POST /api/v1/tts:normalize", "Dry run: detected language and normalised text for a profile", "objects:read",
		TTSPreviewRequest{}, tts.Plan{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req TTSPreviewRequest
			if !a.decode(w, r, &req) {
				return
			}
			profile, ok := a.ttsProfileFor(w, r, p, req.ProfileName, req.Profile)
			if !ok {
				return
			}
			plan, err := a.TTS.Plan(profile, req.Text, tts.SpeakOptions{Lang: req.Language})
			if err != nil {
				a.validationError(w, r, "tts", err.Error())
				return
			}
			a.writeJSON(w, http.StatusOK, plan)
		})

	a.handle("POST /api/v1/tts:preview", "Synthesize a preview clip (WAV, base64)", "config:write",
		TTSPreviewRequest{}, TTSPreviewResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req TTSPreviewRequest
			if !a.decode(w, r, &req) {
				return
			}
			if strings.TrimSpace(req.Text) == "" {
				req.Text = "Northplane test. Severity critical. CPU load high on np-01. Press 4 to acknowledge."
			}
			profile, ok := a.ttsProfileFor(w, r, p, req.ProfileName, req.Profile)
			if !ok {
				return
			}
			res, err := a.TTS.Speak(r.Context(), a.tenantOf(r, p), profile, req.Text,
				tts.SpeakOptions{Lang: req.Language, Voice: req.Voice, Preroll: req.Preroll, NoCache: req.Profile != nil})
			if err != nil {
				a.problem(w, r, http.StatusBadGateway, "np:tts/synthesis", "speech synthesis failed", err.Error())
				return
			}
			a.writeJSON(w, http.StatusOK, TTSPreviewResponse{
				Audio: base64.StdEncoding.EncodeToString(res.Data), Format: "audio/wav",
				Lang: res.Lang, Text: res.Text, Segments: res.Segments, Engine: res.Engine,
				Cached: res.Cached, DurationMS: res.DurationMS(),
			})
		})

	a.handle("POST /api/v1/tts:voices", "List the voices of a profile's engine", "config:write",
		TTSVoicesRequest{}, []tts.Voice{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req TTSVoicesRequest
			if !a.decode(w, r, &req) {
				return
			}
			profile, ok := a.ttsProfileFor(w, r, p, req.ProfileName, req.Profile)
			if !ok {
				return
			}
			voices, err := a.TTS.Voices(r.Context(), a.tenantOf(r, p), profile, req.Language)
			if err != nil {
				a.problem(w, r, http.StatusBadGateway, "np:tts/voices", "voice list failed", err.Error())
				return
			}
			if voices == nil {
				voices = []tts.Voice{}
			}
			a.writeJSON(w, http.StatusOK, voices)
		})

	// Public: signed, expiring clip URLs handed to Twilio <Play> and the
	// Asterisk media cache (no session — the token is the credential).
	a.mux.HandleFunc("GET /api/v1/tts/audio/{token}", func(w http.ResponseWriter, r *http.Request) {
		if a.TTS == nil {
			http.NotFound(w, r)
			return
		}
		a.TTS.ServeAudio(w, r, param(r, "token"))
	})
}

// ttsProfileFor resolves the profile for the tool endpoints: an inline
// document (validated) wins, else the named stored profile (empty name
// = "default"). Writes the error response and returns ok=false on
// failure.
func (a *API) ttsProfileFor(w http.ResponseWriter, r *http.Request, p *auth.Principal,
	name string, inline *model.TTSProfile) (*model.TTSProfile, bool) {
	if a.TTS == nil {
		a.problem(w, r, http.StatusServiceUnavailable, "np:tts/disabled", "text-to-speech not available", "")
		return nil, false
	}
	if inline != nil {
		if inline.Name == "" {
			inline.Name = "preview"
		}
		if err := tts.Validate(inline); err != nil {
			a.validationError(w, r, "tts-profile", err.Error())
			return nil, false
		}
		return inline, true
	}
	profile, err := a.TTS.Profile(r.Context(), a.tenantOf(r, p), name)
	if err != nil {
		a.problem(w, r, http.StatusNotFound, "np:tts/profile", "tts profile not found", err.Error())
		return nil, false
	}
	if profile == nil {
		a.problem(w, r, http.StatusNotFound, "np:tts/profile", "no tts profile",
			"name a profile (profileName) or create one called \"default\"")
		return nil, false
	}
	return profile, true
}

// ttsAudioURLTTL is how long a clip URL stays valid — outbox retries of
// a voice call may span a day.
const ttsAudioURLTTL = 24 * time.Hour

// validateTTSProfileDoc is the resource-save hook.
func validateTTSProfileDoc(raw []byte) error {
	var p model.TTSProfile
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	return tts.Validate(&p)
}

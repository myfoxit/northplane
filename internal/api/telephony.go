package api

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
	"github.com/northplane/northplane/internal/tts"
)

// Inbound telephony (alarming pipelines): calls and SMS to a Twilio
// number (or SIP domain) hit these webhooks and raise, acknowledge or
// resolve alarms through configurable IVR menus. Sources are
// EventSources of type "voice-inbound" / "sms-inbound", so auth, rate
// limits, labels and multi-number setups ride the existing ingress
// machinery — one EventSource per phone number / SIP domain.
//
// voice-inbound config keys:
//
//	menu             IVR menu name (resource kind ivr-menu); empty = built-in
//	                 (1 = raise alarm + record message, 2 = list, 3 = ack)
//	language         TTS language when the menu has none (default en-US)
//	voice            provider TTS voice (optional, e.g. "Polly.Vicki")
//	ttsProfile       TTS profile: prompts are synthesized by Northplane and
//	                 <Play>ed (default: the profile named "default", if any;
//	                 otherwise Twilio <Say>)
//	allowFrom        comma-separated E.164 prefixes; empty = all callers
//	escalationPolicy default policy for alarms the menu raises
//	severity         default severity (default critical)
//	twilioAuthToken  $SECRET ref; when set, X-Twilio-Signature is verified
//
// sms-inbound config keys:
//
//	action           "event" (default: normalised ingress event through
//	                 alert rules) | "alert" (raise directly)
//	escalationPolicy policy for action=alert
//	severity         default severity (default warning)
//	allowFrom, twilioAuthToken as above
//	ackKeyword       reply keyword that acknowledges the newest open
//	                 alarm (default "ACK"; sender must match a contact)
func (a *API) registerTelephony() {
	a.resourceCRUD("ivr-menus", storage.KindIVRMenu, "config", model.IVRMenu{})

	a.mux.HandleFunc("POST /api/v1/voice/inbound/{source}", func(w http.ResponseWriter, r *http.Request) {
		a.handleVoiceInbound(w, r)
	})
	a.mux.HandleFunc("POST /api/v1/voice/inbound/{source}/menu", func(w http.ResponseWriter, r *http.Request) {
		a.handleVoiceMenu(w, r)
	})
	a.mux.HandleFunc("POST /api/v1/voice/inbound/{source}/transcription", func(w http.ResponseWriter, r *http.Request) {
		a.handleVoiceTranscription(w, r)
	})
	a.mux.HandleFunc("POST /api/v1/sms/inbound/{source}", func(w http.ResponseWriter, r *http.Request) {
		a.handleSMSInbound(w, r)
	})
}

// telCall carries one parsed inbound telephony webhook.
type telCall struct {
	src    *model.EventSource
	tenant string
	form   url.Values
	menu   *model.IVRMenu
	lang   string
	voice  string
	tts    *model.TTSProfile // nil = Twilio <Say>
}

func (c *telCall) from() string    { return c.form.Get("From") }
func (c *telCall) to() string      { return c.form.Get("To") }
func (c *telCall) callSid() string { return c.form.Get("CallSid") }
func (c *telCall) digits() string  { return c.form.Get("Digits") }

// telAuth authenticates a telephony webhook: EventSource auth mode
// (token/basic/hmac/none) plus the optional Twilio signature check.
// Returns the parsed form on success.
func (a *API) telAuth(w http.ResponseWriter, r *http.Request, kind string) *telCall {
	src, tenantID, err := a.findEventSource(r, param(r, "source"))
	if err != nil || src.Type != kind {
		a.problem(w, r, http.StatusNotFound, "np:ingress/unknown-source", "unknown event source", "")
		return nil
	}
	if !src.Enabled {
		a.problem(w, r, http.StatusForbidden, "np:ingress/disabled", "event source disabled", "")
		return nil
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		a.problem(w, r, http.StatusRequestEntityTooLarge, "np:ingress/size", "payload too large", "")
		return nil
	}
	if !a.ingressAuth(r, src, body) {
		a.problem(w, r, http.StatusUnauthorized, "np:ingress/auth", "ingress authentication failed", "")
		return nil
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		a.problem(w, r, http.StatusBadRequest, "np:ingress/format", "expected form-encoded webhook", "")
		return nil
	}
	if tok := a.telSecret(r, src, "twilioAuthToken"); tok != "" {
		if !validTwilioSignature(tok, a.publicURL(r), form, r.Header.Get("X-Twilio-Signature")) {
			a.problem(w, r, http.StatusUnauthorized, "np:ingress/auth", "twilio signature invalid", "")
			return nil
		}
	}
	if !allowedCaller(src.Config["allowFrom"], form.Get("From")) {
		a.problem(w, r, http.StatusForbidden, "np:ingress/caller", "caller not allowed", "")
		return nil
	}
	if !allowRate(src.ID, src.RateLimit, src.Burst) {
		a.problem(w, r, http.StatusTooManyRequests, "np:ingress/rate", "rate limit exceeded", "")
		return nil
	}

	c := &telCall{src: src, tenant: tenantID, form: form}
	c.menu = a.loadIVRMenu(r, tenantID, src.Config["menu"])
	c.lang = firstNonEmpty(c.menu.Language, src.Config["language"], "en-US")
	c.voice = firstNonEmpty(c.menu.Voice, src.Config["voice"])
	if a.TTS != nil && a.Cfg.BaseURL != "" { // <Play> needs a public URL
		c.tts = a.TTS.Pick(r.Context(), tenantID, src.Config["ttsProfile"], nil)
	}
	return c
}

// ttsPlay returns a speak hook for TwiML: prompts synthesized through the
// call's TTS profile are <Play>ed; "" falls back to <Say>.
func (a *API) ttsPlay(r *http.Request, c *telCall) func(string) string {
	if c.tts == nil {
		return nil
	}
	return func(text string) string {
		res, err := a.TTS.Speak(r.Context(), c.tenant, c.tts, text, tts.SpeakOptions{Lang: c.ttsLang()})
		if err != nil {
			a.Log.Warn("telephony: tts failed, using <Say>", "profile", c.tts.Name, "err", err)
			return ""
		}
		return a.TTS.AudioURL(res, ttsAudioURLTTL)
	}
}

// ttsLang pins IVR prompts to the menu/source language when one is
// configured (the fixed phrases are German or English by that setting),
// leaving detection to the profile only when nothing is configured.
func (c *telCall) ttsLang() string {
	if c.menu.Language != "" {
		return c.menu.Language
	}
	return c.src.Config["language"]
}

// telSecret resolves a possibly $SECRET-referenced config value.
func (a *API) telSecret(r *http.Request, src *model.EventSource, key string) string {
	v := src.Config[key]
	if name, ok := strings.CutPrefix(v, "$SECRET:"); ok && strings.HasSuffix(name, "$") && a.Box != nil {
		blob, err := a.Store.GetSecret(r.Context(), src.TenantID, strings.TrimSuffix(name, "$"))
		if err != nil {
			return ""
		}
		val, _ := a.Box.Open(blob)
		return val
	}
	return v
}

func (a *API) loadIVRMenu(r *http.Request, tenantID, name string) *model.IVRMenu {
	if name != "" {
		if m, err := storage.LoadOne[model.IVRMenu](r.Context(), a.Store, tenantID,
			storage.KindIVRMenu, name); err == nil {
			return m
		}
		a.Log.Warn("telephony: configured ivr menu missing, using builtin", "menu", name)
	}
	return model.DefaultIVRMenu()
}

// publicURL reconstructs the exact URL Twilio signed (base URL + path +
// query, SPEC §13: behind the trusted proxy the config baseUrl wins).
func (a *API) publicURL(r *http.Request) string {
	base := strings.TrimSuffix(a.Cfg.BaseURL, "/")
	if base == "" {
		scheme := "https"
		if r.TLS == nil {
			scheme = "http"
		}
		base = scheme + "://" + r.Host
	}
	u := base + r.URL.Path
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	return u
}

// validTwilioSignature implements Twilio's webhook signing: HMAC-SHA1
// over URL + form keys/values sorted by key, base64-encoded.
func validTwilioSignature(authToken, fullURL string, form url.Values, header string) bool {
	if header == "" {
		return false
	}
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	msg := fullURL
	for _, k := range keys {
		for _, v := range form[k] {
			msg += k + v
			break // Twilio signs the first value only
		}
	}
	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(msg))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(header))
}

// allowedCaller checks the caller against comma-separated E.164 prefixes.
func allowedCaller(allow, from string) bool {
	allow = strings.TrimSpace(allow)
	if allow == "" {
		return true
	}
	from = normalizePhone(from)
	for _, p := range strings.Split(allow, ",") {
		if p = normalizePhone(p); p != "" && strings.HasPrefix(from, p) {
			return true
		}
	}
	return false
}

func normalizePhone(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '+' || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// --- prompt texts (German when the TTS language is de-*) -------------

type telPrompts struct {
	greeting, pin, pinBad, invalid, bye                string
	alarmRaised, recordNow, recorded                   string
	noAlerts, ackConfirm, resolveConfirm, listIntro    string
	optTrigger, optList, optAck, optResolve, chooseAck string
}

func promptsFor(lang string) telPrompts {
	if strings.HasPrefix(strings.ToLower(lang), "de") {
		return telPrompts{
			greeting:       "Willkommen bei der Northplane Alarmzentrale.",
			pin:            "Bitte geben Sie Ihre PIN ein.",
			pinBad:         "Falsche PIN.",
			invalid:        "Ungültige Eingabe.",
			bye:            "Auf Wiederhören.",
			alarmRaised:    "Der Alarm wurde ausgelöst. Die Alarmierung läuft.",
			recordNow:      "Sprechen Sie Ihre Meldung nach dem Ton und beenden Sie mit der Raute-Taste.",
			recorded:       "Ihre Meldung wurde aufgezeichnet.",
			noAlerts:       "Es gibt keine offenen Alarme.",
			ackConfirm:     "Der Alarm wurde quittiert. Die Eskalationskette ist gestoppt.",
			resolveConfirm: "Der Alarm wurde gelöst.",
			listIntro:      "Offene Alarme:",
			optTrigger:     "Um einen Alarm auszulösen, drücken Sie die %s.",
			optList:        "Um offene Alarme zu hören, drücken Sie die %s.",
			optAck:         "Um einen Alarm zu quittieren, drücken Sie die %s.",
			optResolve:     "Um einen Alarm zu lösen, drücken Sie die %s.",
			chooseAck:      "Wählen Sie den Alarm mit den Zifferntasten.",
		}
	}
	return telPrompts{
		greeting:       "Welcome to the Northplane alarm line.",
		pin:            "Please enter your PIN.",
		pinBad:         "Wrong PIN.",
		invalid:        "Invalid input.",
		bye:            "Goodbye.",
		alarmRaised:    "The alarm has been raised. Notifications are on their way.",
		recordNow:      "Speak your message after the tone, finish with the pound key.",
		recorded:       "Your message has been recorded.",
		noAlerts:       "There are no open alarms.",
		ackConfirm:     "The alarm is acknowledged. The escalation chain is stopped.",
		resolveConfirm: "The alarm is resolved.",
		listIntro:      "Open alarms:",
		optTrigger:     "To raise an alarm, press %s.",
		optList:        "To hear open alarms, press %s.",
		optAck:         "To acknowledge an alarm, press %s.",
		optResolve:     "To resolve an alarm, press %s.",
		chooseAck:      "Choose the alarm with the digit keys.",
	}
}

// --- TwiML rendering --------------------------------------------------

type twiml struct {
	b     strings.Builder
	lang  string
	voice string
	// play, when set, returns a clip URL for a text (Northplane TTS);
	// "" means fall back to <Say>.
	play func(text string) string
}

func newTwiML(lang, voice string) *twiml {
	t := &twiml{lang: lang, voice: voice}
	t.b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><Response>`)
	return t
}

// withPlay attaches the synthesized-speech hook.
func (t *twiml) withPlay(play func(string) string) *twiml {
	t.play = play
	return t
}

func (t *twiml) esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func (t *twiml) sayAttrs() string {
	attrs := ` language="` + t.esc(t.lang) + `"`
	if t.voice != "" {
		attrs += ` voice="` + t.esc(t.voice) + `"`
	}
	return attrs
}

func (t *twiml) Say(text string) *twiml {
	if t.play != nil && strings.TrimSpace(text) != "" {
		if u := t.play(text); u != "" {
			t.b.WriteString(`<Play>` + t.esc(u) + `</Play>`)
			return t
		}
	}
	t.b.WriteString(`<Say` + t.sayAttrs() + `>` + t.esc(text) + `</Say>`)
	return t
}

// Gather wraps the given say-texts in a DTMF gather posting to action.
func (t *twiml) Gather(action string, numDigits int, texts ...string) *twiml {
	fmt.Fprintf(&t.b, `<Gather input="dtmf" numDigits="%d" timeout="10" action="%s" method="POST">`,
		numDigits, t.esc(action))
	for _, s := range texts {
		t.Say(s)
	}
	t.b.WriteString(`</Gather>`)
	return t
}

func (t *twiml) Record(action, transcribeCallback string, maxSeconds int) *twiml {
	attrs := fmt.Sprintf(` maxLength="%d" finishOnKey="#" playBeep="true" action="%s" method="POST"`,
		maxSeconds, t.esc(action))
	if transcribeCallback != "" {
		attrs += ` transcribe="true" transcribeCallback="` + t.esc(transcribeCallback) + `"`
	}
	t.b.WriteString(`<Record` + attrs + `/>`)
	return t
}

func (t *twiml) Hangup() *twiml {
	t.b.WriteString(`<Hangup/>`)
	return t
}

func (t *twiml) write(w http.ResponseWriter) {
	t.b.WriteString(`</Response>`)
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = w.Write([]byte(t.b.String()))
}

// action builds the menu-callback URL for a call, carrying the webhook
// token (Twilio re-posts to the action URL verbatim) and flow state.
func (a *API) telAction(r *http.Request, src *model.EventSource, sub string, params url.Values) string {
	base := strings.TrimSuffix(a.Cfg.BaseURL, "/")
	if base == "" {
		base = "https://" + r.Host
	}
	if params == nil {
		params = url.Values{}
	}
	if tok := r.URL.Query().Get("token"); tok != "" {
		params.Set("token", tok)
	}
	u := base + "/api/v1/voice/inbound/" + url.PathEscape(src.ID) + sub
	if enc := params.Encode(); enc != "" {
		u += "?" + enc
	}
	return u
}

// --- inbound voice flow ------------------------------------------------

// handleVoiceInbound answers a fresh call: PIN gate or main menu.
func (a *API) handleVoiceInbound(w http.ResponseWriter, r *http.Request) {
	c := a.telAuth(w, r, "voice-inbound")
	if c == nil {
		return
	}
	p := promptsFor(c.lang)
	t := newTwiML(c.lang, c.voice).withPlay(a.ttsPlay(r, c))
	greeting := firstNonEmpty(c.menu.Greeting, p.greeting)

	if c.menu.PIN != "" && (!c.menu.TrustCallerID || a.contactByPhone(r, c.tenant, c.from()) == nil) {
		t.Gather(a.telAction(r, c.src, "/menu", url.Values{"state": {"pin"}}),
			len(c.menu.PIN), greeting, p.pin).Say(p.bye).write(w)
		return
	}
	a.voiceMainMenu(w, r, c, t, greeting)
}

// voiceMainMenu speaks the option list and gathers one digit.
func (a *API) voiceMainMenu(w http.ResponseWriter, r *http.Request, c *telCall, t *twiml, intro string) {
	p := promptsFor(c.lang)
	texts := []string{}
	if intro != "" {
		texts = append(texts, intro)
	}
	for _, opt := range c.menu.Options {
		label := opt.Label
		if label == "" {
			switch opt.Action {
			case model.IVRTriggerAlarm:
				label = fmt.Sprintf(p.optTrigger, opt.Digit)
			case model.IVRListAlerts:
				label = fmt.Sprintf(p.optList, opt.Digit)
			case model.IVRAckAlert:
				label = fmt.Sprintf(p.optAck, opt.Digit)
			case model.IVRResolveAlert:
				label = fmt.Sprintf(p.optResolve, opt.Digit)
			default:
				continue
			}
		} else {
			label = fmt.Sprintf("%s: %s", label, opt.Digit)
		}
		texts = append(texts, label)
	}
	t.Gather(a.telAction(r, c.src, "/menu", url.Values{"state": {"menu"}}), 1, texts...).
		Say(p.bye).write(w)
}

// handleVoiceMenu dispatches DTMF input per flow state.
func (a *API) handleVoiceMenu(w http.ResponseWriter, r *http.Request) {
	c := a.telAuth(w, r, "voice-inbound")
	if c == nil {
		return
	}
	p := promptsFor(c.lang)
	t := newTwiML(c.lang, c.voice).withPlay(a.ttsPlay(r, c))
	q := r.URL.Query()

	switch q.Get("state") {
	case "pin":
		if c.digits() != c.menu.PIN || c.menu.PIN == "" {
			t.Say(p.pinBad).Say(p.bye).Hangup().write(w)
			return
		}
		a.voiceMainMenu(w, r, c, t, "")

	case "menu":
		opt := c.menu.FindOption(c.digits())
		if opt == nil {
			a.voiceMainMenu(w, r, c, t, p.invalid)
			return
		}
		a.voiceDispatch(w, r, c, t, opt)

	case "record-done":
		// <Record action>: attach the voicemail to the alert raised before.
		alertID := q.Get("alert")
		if rec := c.form.Get("RecordingUrl"); rec != "" && alertID != "" {
			_, _ = a.Store.MergeAlertLabels(r.Context(), c.tenant, alertID,
				model.Labels{"recordingUrl": rec + ".mp3"})
		}
		t.Say(p.recorded).Say(p.bye).Hangup().write(w)

	case "ack", "resolve":
		ids := strings.Split(q.Get("ids"), ",")
		d := c.digits()
		if len(d) != 1 || d[0] < '1' || d[0] > '9' {
			t.Say(p.invalid).Say(p.bye).Hangup().write(w)
			return
		}
		idx := int(d[0]-'0') - 1
		if idx >= len(ids) || ids[idx] == "" {
			t.Say(p.invalid).Say(p.bye).Hangup().write(w)
			return
		}
		a.voiceAckResolve(w, r, c, t, q.Get("state"), ids[idx])

	default:
		a.voiceMainMenu(w, r, c, t, "")
	}
}

// voiceDispatch executes a chosen menu option.
func (a *API) voiceDispatch(w http.ResponseWriter, r *http.Request, c *telCall, t *twiml, opt *model.IVROption) {
	p := promptsFor(c.lang)
	switch opt.Action {
	case model.IVRTriggerAlarm:
		sev := opt.Severity
		if sev == "" {
			sev = model.Severity(firstNonEmpty(c.src.Config["severity"], string(model.SevCritical)))
		}
		title := firstNonEmpty(opt.Title, "Phone alarm from {caller}")
		title = strings.NewReplacer("{caller}", c.from(), "{called}", c.to()).Replace(title)
		labels := model.Labels{"caller": c.from(), "called": c.to(), "callSid": c.callSid()}.
			Merge(c.src.Labels).Merge(opt.Labels)
		policy := firstNonEmpty(opt.EscalationPolicy, c.src.Config["escalationPolicy"])
		by := c.from()
		if contact := a.contactByPhone(r, c.tenant, c.from()); contact != nil {
			by = contact.Name
		}
		alert, _, err := a.RaiseAlert(r.Context(), c.tenant, RaiseParams{
			Title: title, Severity: sev, Labels: labels,
			EscalationPolicy: policy, DedupKey: "call/" + c.callSid(),
			By: by, Via: "voice",
		})
		if err != nil {
			a.Log.Error("telephony: raise", "err", err)
			t.Say(p.invalid).Hangup().write(w)
			return
		}
		_, _ = a.Store.AppendAudit(r.Context(), &model.AuditEntry{
			TenantID: c.tenant, ActorType: model.ActorUser, ActorID: by,
			Action: "alert.raise", Resource: alert.ID,
			AfterJSON: `{"via":"voice-inbound","caller":` + strconvQuote(c.from()) + `}`,
			SourceIP:  remoteHost(r),
		})
		if opt.Record {
			t.Say(p.alarmRaised).Say(p.recordNow).
				Record(a.telAction(r, c.src, "/menu", url.Values{"state": {"record-done"}, "alert": {alert.ID}}),
					a.telAction(r, c.src, "/transcription", url.Values{"alert": {alert.ID}}), 120).
				Say(p.bye).write(w)
			return
		}
		t.Say(p.alarmRaised).Say(p.bye).Hangup().write(w)

	case model.IVRListAlerts:
		open := a.openAlerts(r, c.tenant, 5)
		if len(open) == 0 {
			t.Say(p.noAlerts)
			a.voiceMainMenu(w, r, c, t, "")
			return
		}
		texts := []string{p.listIntro}
		for i, al := range open {
			texts = append(texts, fmt.Sprintf("%d: %s. %s.", i+1, al.Severity, al.Title))
		}
		t.Say(strings.Join(texts, " "))
		a.voiceMainMenu(w, r, c, t, "")

	case model.IVRAckAlert, model.IVRResolveAlert:
		state := "ack"
		if opt.Action == model.IVRResolveAlert {
			state = "resolve"
		}
		open := a.openAlerts(r, c.tenant, 5)
		if len(open) == 0 {
			t.Say(p.noAlerts).Say(p.bye).Hangup().write(w)
			return
		}
		if len(open) == 1 {
			a.voiceAckResolve(w, r, c, t, state, open[0].ID)
			return
		}
		ids := make([]string, 0, len(open))
		texts := []string{p.chooseAck}
		for i, al := range open {
			ids = append(ids, al.ID)
			texts = append(texts, fmt.Sprintf("%d: %s.", i+1, al.Title))
		}
		t.Gather(a.telAction(r, c.src, "/menu",
			url.Values{"state": {state}, "ids": {strings.Join(ids, ",")}}), 1, texts...).
			Say(p.bye).write(w)

	case model.IVRSay:
		t.Say(opt.Text)
		a.voiceMainMenu(w, r, c, t, "")

	default:
		t.Say(p.invalid).Hangup().write(w)
	}
}

// voiceAckResolve acks/resolves one alert from the phone.
func (a *API) voiceAckResolve(w http.ResponseWriter, r *http.Request, c *telCall, t *twiml, state, alertID string) {
	p := promptsFor(c.lang)
	by := c.from()
	if contact := a.contactByPhone(r, c.tenant, c.from()); contact != nil {
		by = contact.Name
	}
	if state == "resolve" {
		alert, err := a.Store.ResolveAlert(r.Context(), c.tenant, alertID, model.AlertResolved)
		if err != nil {
			t.Say(p.invalid).Hangup().write(w)
			return
		}
		_ = a.Escal.StopChain(r.Context(), alert.ID)
		a.Alert.MaybeResolveIncident(r.Context(), alert)
		a.alertLifecycleEvent(r, c.tenant, alert, model.EventAlertResolved)
		t.Say(p.resolveConfirm).Say(p.bye).Hangup().write(w)
	} else {
		if _, err := a.Store.AckAlert(r.Context(), c.tenant, alertID, by); err != nil {
			t.Say(p.invalid).Hangup().write(w)
			return
		}
		_ = a.Escal.StopChain(r.Context(), alertID)
		a.alertLifecycleEventTenant(r, c.tenant, alertID, "voice-inbound")
		t.Say(p.ackConfirm).Say(p.bye).Hangup().write(w)
	}
	_, _ = a.Store.AppendAudit(r.Context(), &model.AuditEntry{
		TenantID: c.tenant, ActorType: model.ActorUser, ActorID: by,
		Action: "alert." + state, Resource: alertID,
		AfterJSON: `{"via":"voice-inbound"}`, SourceIP: remoteHost(r),
	})
}

// handleVoiceTranscription attaches the provider transcript to the alert.
func (a *API) handleVoiceTranscription(w http.ResponseWriter, r *http.Request) {
	c := a.telAuth(w, r, "voice-inbound")
	if c == nil {
		return
	}
	alertID := r.URL.Query().Get("alert")
	text := c.form.Get("TranscriptionText")
	if alertID != "" && text != "" {
		if len(text) > 500 {
			text = text[:500] + "…"
		}
		_, _ = a.Store.MergeAlertLabels(r.Context(), c.tenant, alertID,
			model.Labels{"transcript": text})
	}
	w.WriteHeader(http.StatusNoContent)
}

// openAlerts lists the newest open alerts for voice menus.
func (a *API) openAlerts(r *http.Request, tenant string, limit int) []*model.Alert {
	alerts, err := a.Store.ListAlerts(r.Context(), storage.AlertFilter{
		TenantID: tenant, Status: []model.AlertStatus{model.AlertOpen}, Limit: limit})
	if err != nil {
		return nil
	}
	return alerts
}

// contactByPhone finds the contact owning a phone number.
func (a *API) contactByPhone(r *http.Request, tenant, phone string) *model.Contact {
	phone = normalizePhone(phone)
	if phone == "" {
		return nil
	}
	contacts, err := storage.LoadAll[model.Contact](r.Context(), a.Store, tenant, storage.KindContact)
	if err != nil {
		return nil
	}
	for _, c := range contacts {
		if normalizePhone(c.Phone) == phone {
			return c
		}
	}
	return nil
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// --- inbound SMS -------------------------------------------------------

// handleSMSInbound turns an inbound SMS into an alarm (or an ack): the
// ack keyword from a known contact acknowledges the newest open alarm;
// anything else raises one (config action=alert) or flows through the
// alert rules as a normal ingress event (default).
func (a *API) handleSMSInbound(w http.ResponseWriter, r *http.Request) {
	c := a.telAuth(w, r, "sms-inbound")
	if c == nil {
		return
	}
	body := strings.TrimSpace(c.form.Get("Body"))
	from := c.from()
	reply := func(msg string) {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(msg))
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Response><Message>` +
			b.String() + `</Message></Response>`))
	}
	german := strings.HasPrefix(strings.ToLower(firstNonEmpty(c.src.Config["language"], "en")), "de")

	// Ack keyword from a known contact stops the newest alarm.
	keyword := strings.ToUpper(firstNonEmpty(c.src.Config["ackKeyword"], "ACK"))
	if strings.HasPrefix(strings.ToUpper(body), keyword) {
		contact := a.contactByPhone(r, c.tenant, from)
		if contact == nil {
			if german {
				reply("Unbekannte Nummer — nicht quittiert.")
			} else {
				reply("Unknown number — not acknowledged.")
			}
			return
		}
		open := a.openAlerts(r, c.tenant, 1)
		if len(open) == 0 {
			if german {
				reply("Keine offenen Alarme.")
			} else {
				reply("No open alarms.")
			}
			return
		}
		alert := open[0]
		if _, err := a.Store.AckAlert(r.Context(), c.tenant, alert.ID, contact.Name); err == nil {
			_ = a.Escal.StopChain(r.Context(), alert.ID)
			a.alertLifecycleEventTenant(r, c.tenant, alert.ID, "sms")
			_, _ = a.Store.AppendAudit(r.Context(), &model.AuditEntry{
				TenantID: c.tenant, ActorType: model.ActorUser, ActorID: contact.Name,
				Action: "alert.ack", Resource: alert.ID,
				AfterJSON: `{"via":"sms"}`, SourceIP: remoteHost(r),
			})
		}
		if german {
			reply("Quittiert: " + alert.Title)
		} else {
			reply("Acknowledged: " + alert.Title)
		}
		return
	}

	summary := body
	if summary == "" {
		summary = "SMS alarm from " + from
	}
	if len(summary) > 200 {
		summary = summary[:200] + "…"
	}
	sev := model.Severity(firstNonEmpty(c.src.Config["severity"], string(model.SevWarning)))
	if !sev.Valid() {
		sev = model.SevWarning
	}

	if c.src.Config["action"] == "alert" {
		by := from
		if contact := a.contactByPhone(r, c.tenant, from); contact != nil {
			by = contact.Name
		}
		_, _, err := a.RaiseAlert(r.Context(), c.tenant, RaiseParams{
			Title: summary, Severity: sev,
			Labels:           model.Labels{"from": from, "to": c.to()}.Merge(c.src.Labels),
			EscalationPolicy: c.src.Config["escalationPolicy"],
			DedupKey:         "sms/" + c.form.Get("MessageSid"),
			By:               by, Via: "sms",
		})
		if err != nil {
			a.Log.Error("telephony: sms raise", "err", err)
		}
	} else {
		norm := &model.NormEvent{
			Source: c.src.ID, ReceivedAt: time.Now().UTC(),
			Severity: sev, Summary: summary,
			Labels: model.Labels{"from": from, "to": c.to()}.Merge(c.src.Labels),
		}
		norm.Payload, _ = json.Marshal(map[string]string{"body": body, "from": from, "to": c.to()})
		a.publishNorm(r, c.tenant, c.src, norm)
	}
	a.Metrics.Counter(`np_ingress_events_total{type="sms"}`, "Ingress events").Inc()
	if german {
		reply("Alarm angenommen.")
	} else {
		reply("Alarm received.")
	}
}

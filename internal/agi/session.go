package agi

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/api"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/tts"
)

// session speaks the AGI wire protocol: Asterisk sends an environment
// block (agi_* headers, blank-line terminated), then we issue commands
// and parse "200 result=<n> [extra]" replies.
type session struct {
	r   *bufio.Reader
	w   io.Writer
	env map[string]string
}

func newSession(rw io.ReadWriter) (*session, error) {
	s := &session{r: bufio.NewReader(rw), w: rw, env: map[string]string{}}
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("agi env: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ": "); ok && strings.HasPrefix(k, "agi_") {
			s.env[k] = v
		}
	}
	return s, nil
}

// cmd sends one AGI command and returns the numeric result plus the
// remainder of the status line.
func (s *session) cmd(format string, args ...any) (result int, extra string, err error) {
	if _, err := fmt.Fprintf(s.w, format+"\n", args...); err != nil {
		return 0, "", err
	}
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			return 0, "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "200 ") {
			rest := strings.TrimPrefix(line, "200 ")
			rest = strings.TrimPrefix(rest, "result=")
			val, extra, _ := strings.Cut(rest, " ")
			n, _ := strconv.Atoi(val)
			return n, extra, nil
		}
		if strings.HasPrefix(line, "5") { // 510/511/520: unusable channel/command
			return 0, "", fmt.Errorf("agi: %s", line)
		}
		// 520-style multi-line usage text: keep draining until status
	}
}

// getData plays a prompt file and collects up to maxDigits DTMF digits.
func (s *session) getData(promptFile string, timeoutMs, maxDigits int) (string, error) {
	if _, err := fmt.Fprintf(s.w, "GET DATA %s %d %d\n", quoteArg(promptFile), timeoutMs, maxDigits); err != nil {
		return "", err
	}
	line, err := s.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "200 ") {
		return "", fmt.Errorf("agi: %s", line)
	}
	// GET DATA result is the digit string itself (may be empty/-1).
	rest := strings.TrimPrefix(line, "200 result=")
	digits, _, _ := strings.Cut(rest, " ")
	if digits == "-1" {
		return "", fmt.Errorf("agi: channel failure during GET DATA")
	}
	return digits, nil
}

// streamFile plays a sound file, allowing DTMF barge-in; returns the
// pressed digit (0 = none).
func (s *session) streamFile(name, escapeDigits string) (byte, error) {
	res, _, err := s.cmd("STREAM FILE %s %s", quoteArg(name), quoteArg(escapeDigits))
	if err != nil {
		return 0, err
	}
	if res > 0 {
		return byte(res), nil
	}
	return 0, nil
}

// waitForDigit blocks up to timeoutMs; returns 0 on timeout.
func (s *session) waitForDigit(timeoutMs int) (byte, error) {
	res, _, err := s.cmd("WAIT FOR DIGIT %d", timeoutMs)
	if err != nil {
		return 0, err
	}
	if res > 0 {
		return byte(res), nil
	}
	return 0, nil
}

// execApp runs a dialplan application (TTS apps like Flite/ESpeak).
func (s *session) execApp(app, arg string) error {
	_, _, err := s.cmd("EXEC %s %s", app, quoteArg(arg))
	return err
}

func (s *session) sayNumber(n int) error {
	_, _, err := s.cmd(`SAY NUMBER %d ""`, n)
	return err
}

// recordFile records caller audio (wav, beep, finish on #).
func (s *session) recordFile(path string, maxSeconds int) error {
	_, _, err := s.cmd(`RECORD FILE %s wav "#" %d BEEP`, quoteArg(path), maxSeconds*1000)
	return err
}

func (s *session) answer() error {
	_, _, err := s.cmd("ANSWER")
	return err
}

func (s *session) hangup() {
	_, _, _ = s.cmd("HANGUP")
}

func (s *session) verbose(msg string) error {
	_, _, err := s.cmd(`VERBOSE %s 1`, quoteArg(msg))
	return err
}

// quoteArg wraps an AGI argument in double quotes; embedded quotes and
// newlines are stripped (AGI is a line protocol — they cannot be
// represented and must not break framing).
func quoteArg(v string) string {
	v = strings.NewReplacer("\"", "", "\n", " ", "\r", " ").Replace(v)
	return `"` + v + `"`
}

// --- phrase sets (TTS mode) + prompt file names (file mode) -----------

type phrases struct {
	greeting, pin, pinBad, invalid, bye             string
	alarmRaised, recordNow, recorded                string
	noAlerts, ackConfirm, resolveConfirm, listIntro string
	optTrigger, optList, optAck, optResolve, choose string
	sevCritical, sevWarning, sevInfo                string
}

func phrasesFor(lang string) phrases {
	if strings.HasPrefix(strings.ToLower(lang), "de") {
		return phrases{
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
			choose:         "Wählen Sie den Alarm mit den Zifferntasten.",
			sevCritical:    "kritisch", sevWarning: "Warnung", sevInfo: "Info",
		}
	}
	return phrases{
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
		choose:         "Choose the alarm with the digit keys.",
		sevCritical:    "critical", sevWarning: "warning", sevInfo: "info",
	}
}

// prompt file names for TTS-less deployments (record once, e.g. with
// piper; see docs/ALARMING.md).
const (
	pGreeting   = "np-greeting"
	pPin        = "np-pin"
	pPinBad     = "np-pin-bad"
	pInvalid    = "np-invalid"
	pBye        = "np-bye"
	pRaised     = "np-alarm-raised"
	pRecordNow  = "np-record-now"
	pRecorded   = "np-recorded"
	pNoAlerts   = "np-no-alerts"
	pAckOK      = "np-ack-confirm"
	pResolveOK  = "np-resolve-confirm"
	pListIntro  = "np-list-intro"
	pOptTrigger = "np-opt-trigger" // "…press" — digit appended via SAY NUMBER
	pOptList    = "np-opt-list"
	pOptAck     = "np-opt-ack"
	pOptResolve = "np-opt-resolve"
	pChoose     = "np-choose"
	pSevCrit    = "np-sev-critical"
	pSevWarn    = "np-sev-warning"
	pSevInfo    = "np-sev-info"
)

const escapeDigits = "0123456789*#"

// conversation drives one call through the menu.
type conversation struct {
	s    *session
	src  *model.EventSource
	acts actions
	log  interface {
		Info(string, ...any)
		Warn(string, ...any)
	}

	menu    *model.IVRMenu
	ph      phrases
	lang    string
	ttsApp  string
	pending byte // digit pressed during a prompt (barge-in)

	// Northplane-side speech: with a profile every prompt is synthesized
	// and streamed (ttsDir file or signed URL); ttsApp / prompt files are
	// the fallback.
	tts     *tts.Service
	profile *model.TTSProfile
	ctx     context.Context
}

// dynamic reports whether free text can be spoken (Northplane TTS or
// an Asterisk TTS app) as opposed to prompt-file-only mode.
func (c *conversation) dynamic() bool { return c.profile != nil || c.ttsApp != "" }

// synth renders text through the profile and returns the STREAM FILE
// reference: the clip path without extension (ttsDir) or its signed
// URL. "" = unavailable (caller falls back). Fixed phrases are pinned to
// the menu/source language; free text (alert titles, say options) goes
// through the profile's own language detection.
func (c *conversation) synth(text string, pinLang bool) string {
	if c.profile == nil || c.tts == nil || text == "" {
		return ""
	}
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	opts := tts.SpeakOptions{}
	if pinLang {
		opts.Lang = c.lang
	}
	res, err := c.tts.Speak(ctx, c.src.TenantID, c.profile, text, opts)
	if err != nil {
		c.log.Warn("agi: tts failed, using fallback speech", "err", err)
		return ""
	}
	if dir := c.src.Config["ttsDir"]; dir != "" {
		if path, err := writeClip(dir, res.ID, res.Data); err == nil {
			return strings.TrimSuffix(filepath.Join(firstNonEmpty(c.src.Config["ttsDirPBX"], dir), filepath.Base(path)), ".wav")
		} else {
			c.log.Warn("agi: ttsDir write failed", "dir", dir, "err", err)
		}
	}
	if u := c.tts.AudioURL(res, 24*time.Hour); u != "" {
		return u
	}
	c.log.Warn("agi: tts clip has no playable reference (set ttsDir or baseUrl)")
	return ""
}

// writeClip stores <id>.wav in dir once and returns its path.
func writeClip(dir, id string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	path := filepath.Join(dir, id+".wav")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil { //nolint:gosec // the PBX must read the clip
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

func (c *conversation) run(ctx context.Context) {
	c.ctx = ctx
	c.menu = c.acts.Menu(ctx, c.src.TenantID, c.src.Config["menu"])
	c.lang = firstNonEmpty(c.menu.Language, c.src.Config["language"])
	c.ph = phrasesFor(c.lang)
	c.ttsApp = c.src.Config["ttsApp"]

	if err := c.s.answer(); err != nil {
		return
	}
	caller := c.s.env["agi_callerid"]
	contact := c.acts.ContactNameByPhone(ctx, c.src.TenantID, caller)

	// PIN gate (trusted caller-ids skip it, same as the webhook path).
	if c.menu.PIN != "" && (!c.menu.TrustCallerID || contact == "") {
		if !c.pinGate() {
			c.speak(c.ph.bye, pBye)
			c.s.hangup()
			return
		}
	}

	c.speak(firstNonEmpty(c.menu.Greeting, c.ph.greeting), pGreeting)
	for round := 0; round < 5; round++ {
		digit := c.menuPrompt()
		if digit == 0 {
			break // silence — hang up politely below
		}
		opt := c.menu.FindOption(string(digit))
		if opt == nil {
			c.speak(c.ph.invalid, pInvalid)
			continue
		}
		if done := c.dispatch(ctx, opt, caller, contact); done {
			return
		}
	}
	c.speak(c.ph.bye, pBye)
	c.s.hangup()
}

// pinGate collects the PIN, three tries.
func (c *conversation) pinGate() bool {
	for try := 0; try < 3; try++ {
		prompt := pPin
		if ref := c.synth(c.ph.pin, true); ref != "" {
			prompt = ref // synthesized prompt plays inside GET DATA
		} else if c.ttsApp != "" {
			c.speak(c.ph.pin, "")
		}
		digits, err := c.s.getData(prompt, 8000, len(c.menu.PIN))
		if err != nil {
			return false
		}
		if digits == c.menu.PIN {
			return true
		}
		c.speak(c.ph.pinBad, pPinBad)
	}
	return false
}

// menuPrompt speaks the options (barge-in aware) and returns the digit.
func (c *conversation) menuPrompt() byte {
	c.pending = 0
	for i := range c.menu.Options {
		if c.pending != 0 {
			break
		}
		opt := &c.menu.Options[i]
		switch opt.Action {
		case model.IVRTriggerAlarm:
			c.speakOption(c.ph.optTrigger, pOptTrigger, opt)
		case model.IVRListAlerts:
			c.speakOption(c.ph.optList, pOptList, opt)
		case model.IVRAckAlert:
			c.speakOption(c.ph.optAck, pOptAck, opt)
		case model.IVRResolveAlert:
			c.speakOption(c.ph.optResolve, pOptResolve, opt)
		}
	}
	if c.pending != 0 {
		d := c.pending
		c.pending = 0
		return d
	}
	d, err := c.s.waitForDigit(7000)
	if err != nil {
		return 0
	}
	return d
}

// speakOption announces one menu option: TTS speaks the full phrase
// (digit interpolated); prompt mode plays the phrase file then says the
// digit as a number.
func (c *conversation) speakOption(phraseFmt, promptFile string, opt *model.IVROption) {
	if opt.Label != "" && c.dynamic() {
		c.speak(fmt.Sprintf("%s: %s.", opt.Label, opt.Digit), "")
		return
	}
	if c.dynamic() {
		c.speak(fmt.Sprintf(phraseFmt, opt.Digit), "")
		return
	}
	if d, err := c.s.streamFile(promptFile, escapeDigits); err == nil && d != 0 {
		c.pending = d
		return
	}
	if n, err := strconv.Atoi(opt.Digit); err == nil {
		_ = c.s.sayNumber(n)
	}
}

// speak voices a fixed phrase: a Northplane-synthesized clip (barge-in
// aware), else the Asterisk TTS app, else the prompt file.
func (c *conversation) speak(text, promptFile string) {
	c.speakAs(text, promptFile, true)
}

// speakFree voices free text (alert titles, say options) — the TTS
// profile detects its language.
func (c *conversation) speakFree(text string) {
	c.speakAs(text, "", false)
}

func (c *conversation) speakAs(text, promptFile string, pinLang bool) {
	if ref := c.synth(text, pinLang); ref != "" {
		if d, err := c.s.streamFile(ref, escapeDigits); err == nil && d != 0 {
			c.pending = d
		}
		return
	}
	if c.ttsApp != "" && text != "" {
		_ = c.s.execApp(c.ttsApp, text)
		return
	}
	if promptFile != "" {
		if d, err := c.s.streamFile(promptFile, escapeDigits); err == nil && d != 0 {
			c.pending = d
		}
	}
}

// dispatch executes a menu option; returns true when the call is done.
func (c *conversation) dispatch(ctx context.Context, opt *model.IVROption, caller, contact string) bool {
	switch opt.Action {
	case model.IVRTriggerAlarm:
		sev := opt.Severity
		if sev == "" {
			sev = model.Severity(firstNonEmpty(c.src.Config["severity"], string(model.SevCritical)))
		}
		title := firstNonEmpty(opt.Title, "Phone alarm from {caller}")
		title = strings.NewReplacer("{caller}", caller, "{called}", c.s.env["agi_dnid"]).Replace(title)
		by := firstNonEmpty(contact, caller)
		labels := model.Labels{"caller": caller, "via": "asterisk"}.
			Merge(c.src.Labels).Merge(opt.Labels)
		alert, _, err := c.acts.Raise(ctx, c.src.TenantID, api.RaiseParams{
			Title: title, Severity: sev, Labels: labels,
			EscalationPolicy: firstNonEmpty(opt.EscalationPolicy, c.src.Config["escalationPolicy"]),
			DedupKey:         "agi/" + c.s.env["agi_uniqueid"],
			By:               by, Via: "asterisk-agi",
		})
		if err != nil {
			c.speak(c.ph.invalid, pInvalid)
			return true
		}
		c.speak(c.ph.alarmRaised, pRaised)
		if opt.Record && alert != nil {
			c.speak(c.ph.recordNow, pRecordNow)
			dir := strings.TrimSuffix(firstNonEmpty(c.src.Config["recordDir"], "/var/spool/asterisk/recording"), "/")
			path := dir + "/np-" + alert.ID
			if err := c.s.recordFile(path, 120); err == nil {
				c.acts.AttachLabels(ctx, c.src.TenantID, alert.ID,
					model.Labels{"recordingFile": path + ".wav"})
				c.speak(c.ph.recorded, pRecorded)
			}
		}
		c.speak(c.ph.bye, pBye)
		c.s.hangup()
		return true

	case model.IVRListAlerts:
		open := c.acts.OpenAlerts(ctx, c.src.TenantID, 5)
		if len(open) == 0 {
			c.speak(c.ph.noAlerts, pNoAlerts)
			return false
		}
		c.speak(c.ph.listIntro, pListIntro)
		c.speakAlertList(open)
		return false

	case model.IVRAckAlert, model.IVRResolveAlert:
		return c.ackResolve(ctx, opt.Action, contact, caller)

	case model.IVRSay:
		if c.dynamic() {
			c.speakFree(opt.Text)
		} else if opt.Text != "" {
			// prompt mode: Text names a sound file
			_, _ = c.s.streamFile(opt.Text, escapeDigits)
		}
		return false
	}
	return false
}

// speakAlertList reads out numbered alerts (titles only with TTS).
func (c *conversation) speakAlertList(open []*model.Alert) {
	for i, al := range open {
		_ = c.s.sayNumber(i + 1)
		switch al.Severity {
		case model.SevCritical:
			c.speak(c.ph.sevCritical, pSevCrit)
		case model.SevWarning:
			c.speak(c.ph.sevWarning, pSevWarn)
		default:
			c.speak(c.ph.sevInfo, pSevInfo)
		}
		if c.dynamic() {
			c.speakFree(al.Title)
		}
	}
}

// ackResolve lets the caller pick an open alert and ack/resolve it.
func (c *conversation) ackResolve(ctx context.Context, action, contact, caller string) bool {
	open := c.acts.OpenAlerts(ctx, c.src.TenantID, 5)
	if len(open) == 0 {
		c.speak(c.ph.noAlerts, pNoAlerts)
		return false
	}
	target := open[0]
	if len(open) > 1 {
		c.speak(c.ph.choose, pChoose)
		c.speakAlertList(open)
		d, err := c.s.waitForDigit(8000)
		if err != nil || d < '1' || int(d-'0') > len(open) {
			c.speak(c.ph.invalid, pInvalid)
			return false
		}
		target = open[d-'1']
	}
	by := firstNonEmpty(contact, caller)
	var err error
	if action == model.IVRResolveAlert {
		err = c.acts.Resolve(ctx, c.src.TenantID, target.ID, by)
	} else {
		err = c.acts.Ack(ctx, c.src.TenantID, target.ID, by)
	}
	if err != nil {
		c.speak(c.ph.invalid, pInvalid)
		return false
	}
	if action == model.IVRResolveAlert {
		c.speak(c.ph.resolveConfirm, pResolveOK)
	} else {
		c.speak(c.ph.ackConfirm, pAckOK)
	}
	c.speak(c.ph.bye, pBye)
	c.s.hangup()
	return true
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

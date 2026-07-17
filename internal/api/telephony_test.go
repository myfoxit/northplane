package api

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// createVoiceSource seeds a voice-inbound event source (open auth for
// test brevity; production uses token/signature) and returns its id.
func (ta *testAPI) createTelSource(typ string, config map[string]string) string {
	ta.t.Helper()
	code, body := ta.admin("POST", "/api/v1/event-sources", map[string]any{
		"name": typ + "-line", "type": typ, "enabled": true,
		"authMode": "none", "config": config,
	})
	if code != http.StatusCreated {
		ta.t.Fatalf("create %s source: %d %s", typ, code, body)
	}
	return ta.id(body)
}

// call posts a form-encoded telephony webhook (as Twilio does).
func (ta *testAPI) call(path string, form url.Values) (int, string) {
	ta.t.Helper()
	code, body := ta.do("POST", path, form.Encode(), func(r *http.Request) {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	})
	return code, string(body)
}

func TestVoiceInboundTriggersAlarm(t *testing.T) {
	ta := bootAPI(t)
	srcID := ta.createTelSource("voice-inbound", nil)

	// Fresh call → builtin menu offered via <Gather>.
	form := url.Values{"From": {"+4915112345678"}, "To": {"+49301234567"}, "CallSid": {"CAtest123"}}
	code, xml := ta.call("/api/v1/voice/inbound/"+srcID, form)
	if code != http.StatusOK {
		t.Fatalf("inbound: %d %s", code, xml)
	}
	if !strings.Contains(xml, "<Gather") || !strings.Contains(xml, "state=menu") {
		t.Fatalf("expected gather menu, got %s", xml)
	}
	if !strings.Contains(xml, "To raise an alarm, press 1") {
		t.Fatalf("expected builtin option prompt, got %s", xml)
	}

	// Caller presses 1 → alarm raised + voicemail recording offered.
	form.Set("Digits", "1")
	code, xml = ta.call("/api/v1/voice/inbound/"+srcID+"/menu?state=menu", form)
	if code != http.StatusOK || !strings.Contains(xml, "<Record") {
		t.Fatalf("menu digit 1: %d %s", code, xml)
	}

	code, body := ta.admin("GET", "/api/v1/alerts?status=open", nil)
	if code != http.StatusOK {
		t.Fatalf("list alerts: %d", code)
	}
	var list struct{ Items []*model.Alert }
	mustJSON(t, body, &list)
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 open alert, got %d", len(list.Items))
	}
	al := list.Items[0]
	if !strings.Contains(al.Title, "+4915112345678") {
		t.Fatalf("caller missing from title: %q", al.Title)
	}
	if al.Labels["caller"] != "+4915112345678" || al.Labels["callSid"] != "CAtest123" {
		t.Fatalf("labels wrong: %v", al.Labels)
	}
	if al.Severity != model.SevCritical {
		t.Fatalf("severity = %s, want critical", al.Severity)
	}

	// Recording callback attaches the voicemail URL.
	rec := url.Values{"From": {"+4915112345678"}, "CallSid": {"CAtest123"},
		"RecordingUrl": {"https://api.twilio.com/rec/RE1"}}
	code, xml = ta.call("/api/v1/voice/inbound/"+srcID+"/menu?state=record-done&alert="+al.ID, rec)
	if code != http.StatusOK || !strings.Contains(xml, "recorded") {
		t.Fatalf("record-done: %d %s", code, xml)
	}
	code, body = ta.admin("GET", "/api/v1/alerts/"+al.ID, nil)
	if code != http.StatusOK {
		t.Fatalf("get alert: %d", code)
	}
	var got model.Alert
	mustJSON(t, body, &got)
	if got.Labels["recordingUrl"] != "https://api.twilio.com/rec/RE1.mp3" {
		t.Fatalf("recordingUrl label missing: %v", got.Labels)
	}

	// Same CallSid dedups: pressing 1 again folds into the open alert.
	code, _ = ta.call("/api/v1/voice/inbound/"+srcID+"/menu?state=menu", form)
	if code != http.StatusOK {
		t.Fatalf("re-trigger: %d", code)
	}
	_, body = ta.admin("GET", "/api/v1/alerts?status=open", nil)
	mustJSON(t, body, &list)
	if len(list.Items) != 1 {
		t.Fatalf("dedup failed: %d open alerts", len(list.Items))
	}
}

func TestVoiceInboundAckAndPin(t *testing.T) {
	ta := bootAPI(t)

	// German menu with PIN + explicit ack option.
	code, body := ta.admin("POST", "/api/v1/ivr-menus", map[string]any{
		"name": "nachtwache", "language": "de-DE", "pin": "4711",
		"options": []map[string]any{
			{"digit": "3", "action": "ack-alert"},
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("create menu: %d %s", code, body)
	}
	srcID := ta.createTelSource("voice-inbound", map[string]string{"menu": "nachtwache"})
	seeded := ta.seedAlert("", "Kühlraum Temperatur", model.SevCritical)

	form := url.Values{"From": {"+4915100000001"}, "CallSid": {"CApin1"}}
	code, xml := ta.call("/api/v1/voice/inbound/"+srcID, form)
	if code != http.StatusOK || !strings.Contains(xml, "state=pin") {
		t.Fatalf("expected PIN gate: %d %s", code, xml)
	}
	if !strings.Contains(xml, "PIN") {
		t.Fatalf("expected PIN prompt, got %s", xml)
	}

	// Wrong PIN hangs up.
	form.Set("Digits", "9999")
	_, xml = ta.call("/api/v1/voice/inbound/"+srcID+"/menu?state=pin", form)
	if !strings.Contains(xml, "Falsche PIN") || !strings.Contains(xml, "<Hangup/>") {
		t.Fatalf("expected wrong-PIN hangup, got %s", xml)
	}

	// Correct PIN → German menu.
	form.Set("Digits", "4711")
	_, xml = ta.call("/api/v1/voice/inbound/"+srcID+"/menu?state=pin", form)
	if !strings.Contains(xml, "quittieren") {
		t.Fatalf("expected German ack option, got %s", xml)
	}

	// Digit 3 acks the single open alert directly.
	form.Set("Digits", "3")
	_, xml = ta.call("/api/v1/voice/inbound/"+srcID+"/menu?state=menu", form)
	if !strings.Contains(xml, "quittiert") {
		t.Fatalf("expected ack confirmation, got %s", xml)
	}
	_, body = ta.admin("GET", "/api/v1/alerts/"+seeded.ID, nil)
	var got model.Alert
	mustJSON(t, body, &got)
	if got.Status != model.AlertAcked {
		t.Fatalf("alert status = %s, want acked", got.Status)
	}
}

func TestSMSInboundEventAndAck(t *testing.T) {
	ta := bootAPI(t)
	srcID := ta.createTelSource("sms-inbound", map[string]string{"language": "de"})

	// Plain SMS → ingress event + German confirmation.
	form := url.Values{"From": {"+4915177700001"}, "To": {"+493099999"},
		"Body": {"Wasser im Keller"}, "MessageSid": {"SMtest1"}}
	code, xml := ta.call("/api/v1/sms/inbound/"+srcID, form)
	if code != http.StatusOK || !strings.Contains(xml, "Alarm angenommen") {
		t.Fatalf("sms inbound: %d %s", code, xml)
	}

	// ACK from a known contact acknowledges the newest open alert.
	code, body := ta.admin("POST", "/api/v1/contacts", map[string]any{
		"name": "Bereitschaft", "phone": "+49 151 777 00001"})
	if code != http.StatusCreated {
		t.Fatalf("create contact: %d %s", code, body)
	}
	seeded := ta.seedAlert("", "Pumpe ausgefallen", model.SevCritical)
	form.Set("Body", "ACK")
	_, xml = ta.call("/api/v1/sms/inbound/"+srcID, form)
	if !strings.Contains(xml, "Quittiert: Pumpe ausgefallen") {
		t.Fatalf("expected ack reply, got %s", xml)
	}
	_, body = ta.admin("GET", "/api/v1/alerts/"+seeded.ID, nil)
	var got model.Alert
	mustJSON(t, body, &got)
	if got.Status != model.AlertAcked || got.AckedBy != "Bereitschaft" {
		t.Fatalf("ack failed: status=%s by=%s", got.Status, got.AckedBy)
	}

	// Unknown sender may not ack.
	ta.seedAlert("", "Zweiter Alarm", model.SevWarning)
	form.Set("From", "+4900000000")
	_, xml = ta.call("/api/v1/sms/inbound/"+srcID, form)
	if !strings.Contains(xml, "Unbekannte Nummer") {
		t.Fatalf("expected unknown-number reply, got %s", xml)
	}
}

func TestManualAlertRaise(t *testing.T) {
	ta := bootAPI(t)

	// Reader tokens may not raise alarms.
	code, _ := ta.read("POST", "/api/v1/alerts", map[string]any{"title": "nope"})
	if code != http.StatusForbidden {
		t.Fatalf("read raise: %d, want 403", code)
	}

	// Unknown policy is rejected.
	code, _ = ta.admin("POST", "/api/v1/alerts", map[string]any{
		"title": "x", "escalationPolicy": "missing"})
	if code != http.StatusUnprocessableEntity && code != http.StatusBadRequest {
		t.Fatalf("unknown policy: %d, want validation error", code)
	}

	code, body := ta.admin("POST", "/api/v1/alerts", map[string]any{
		"title": "Feueralarm Halle 3", "message": "Rauchmelder Zone 12",
		"labels": map[string]string{"np.sound": "np_klaxon", "np.volume": "1.0"},
	})
	if code != http.StatusCreated {
		t.Fatalf("raise: %d %s", code, body)
	}
	var al model.Alert
	mustJSON(t, body, &al)
	if al.Severity != model.SevCritical || al.Status != model.AlertOpen {
		t.Fatalf("raise defaults wrong: %s/%s", al.Severity, al.Status)
	}
	if al.Labels["np.sound"] != "np_klaxon" {
		t.Fatalf("labels missing: %v", al.Labels)
	}

	// The alert_opened event carries the labels (alarm-app contract).
	code, body = ta.admin("GET", "/api/v1/events?types=alert_opened&limit=10", nil)
	if code != http.StatusOK {
		t.Fatalf("events: %d", code)
	}
	if !strings.Contains(string(body), "np_klaxon") {
		t.Fatalf("alert_opened payload lacks np.sound: %s", body)
	}
}

func TestSnoozeSetsWakeDeadlineAndWakes(t *testing.T) {
	ta := bootAPI(t)
	al := ta.seedAlert("", "Disk voll", model.SevWarning)

	until := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	code, body := ta.admin("POST", "/api/v1/alerts/"+al.ID+":snooze",
		map[string]any{"until": until.Format(time.RFC3339)})
	if code != http.StatusOK {
		t.Fatalf("snooze: %d %s", code, body)
	}
	var snoozed model.Alert
	mustJSON(t, body, &snoozed)
	if snoozed.Status != model.AlertAcked || snoozed.SnoozedUntil == nil {
		t.Fatalf("snooze state wrong: %s %v", snoozed.Status, snoozed.SnoozedUntil)
	}

	// Not due yet.
	woken, err := ta.store.WakeSnoozedAlerts(ta.ctx, time.Now().UTC())
	if err != nil || len(woken) != 0 {
		t.Fatalf("early wake: %v %d", err, len(woken))
	}
	// Due → re-opened exactly once.
	woken, err = ta.store.WakeSnoozedAlerts(ta.ctx, until.Add(time.Minute))
	if err != nil || len(woken) != 1 {
		t.Fatalf("wake: %v %d", err, len(woken))
	}
	if woken[0].Status != model.AlertOpen || woken[0].SnoozedUntil != nil {
		t.Fatalf("woken state wrong: %+v", woken[0])
	}
	again, _ := ta.store.WakeSnoozedAlerts(ta.ctx, until.Add(2*time.Minute))
	if len(again) != 0 {
		t.Fatalf("double wake: %d", len(again))
	}

	// A plain ack afterwards clears any wake deadline.
	code, _ = ta.admin("POST", "/api/v1/alerts/"+al.ID+":ack", nil)
	if code != http.StatusOK {
		t.Fatalf("ack: %d", code)
	}
	got, err := ta.store.GetAlert(ta.ctx, model.DefaultTenant, al.ID)
	if err != nil || got.SnoozedUntil != nil {
		t.Fatalf("ack should clear snooze: %v %v", err, got.SnoozedUntil)
	}
}

// Twilio's documented signature example must verify (docs: "Test the
// validity of your webhook signature", auth token 12345).
func TestTwilioSignature(t *testing.T) {
	form := url.Values{
		"CallSid": {"CA1234567890ABCDE"},
		"Caller":  {"+14158675309"},
		"Digits":  {"1234"},
		"From":    {"+14158675309"},
		"To":      {"+18005551212"},
	}
	u := "https://mycompany.com/myapp.php?foo=1&bar=2"
	want := "RSOYDt4T1cUTdK1PDd93/VVr8B8="
	if !validTwilioSignature("12345", u, form, want) {
		t.Fatal("documented Twilio signature vector failed to verify")
	}
	if validTwilioSignature("12345", u, form, "bogus=") {
		t.Fatal("bogus signature verified")
	}
	if validTwilioSignature("12345", u, form, "") {
		t.Fatal("empty signature verified")
	}
}

func TestAllowedCaller(t *testing.T) {
	cases := []struct {
		allow, from string
		want        bool
	}{
		{"", "+4915112345678", true},
		{"+49151", "+49 151 123 456 78", true},
		{"+49151,+43", "+431234", true},
		{"+49151", "+49301234", false},
		{"+1", "+4915112345678", false},
	}
	for _, c := range cases {
		if got := allowedCaller(c.allow, c.from); got != c.want {
			t.Errorf("allowedCaller(%q,%q) = %v, want %v", c.allow, c.from, got, c.want)
		}
	}
}

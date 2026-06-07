package notify

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/northplane/northplane/internal/model"
)

func TestVoiceTwiML(t *testing.T) {
	xml := voiceTwiML(`Severity CRITICAL. db & web <down>.`, "en-US", "")
	if !strings.Contains(xml, "db &amp; web &lt;down&gt;") {
		t.Fatalf("unescaped TwiML: %s", xml)
	}
	if strings.Contains(xml, "<Gather") {
		t.Fatalf("no gather without callback URL: %s", xml)
	}
	xml = voiceTwiML("Press 4", "de-DE", "https://np.test/api/v1/voice/gather/tok")
	if !strings.Contains(xml, `<Gather numDigits="1"`) ||
		!strings.Contains(xml, "voice/gather/tok") ||
		!strings.Contains(xml, `language="de-DE"`) {
		t.Fatalf("gather TwiML: %s", xml)
	}
}

func TestTwilioVoiceCall(t *testing.T) {
	m, store, ctx := setupMgr(t)

	var to, from, twiml atomic.Value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/2010-04-01/Accounts/AC123/Calls.json" {
			t.Errorf("path: %s", r.URL.Path)
		}
		if user, pass, _ := r.BasicAuth(); user != "AC123" || pass != "tok" {
			t.Errorf("auth: %s %s", user, pass)
		}
		_ = r.ParseForm()
		to.Store(r.PostFormValue("To"))
		from.Store(r.PostFormValue("From"))
		twiml.Store(r.PostFormValue("Twiml"))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"CA987"}`))
	}))
	t.Cleanup(ts.Close)

	putChannel(t, store, ctx, model.NotificationChannel{
		Name: "voice", Type: model.ChannelVoice,
		Config: map[string]string{"provider": "twilio", "accountSid": "AC123",
			"authToken": "tok", "from": "+15550100", "apiBase": ts.URL}})

	alert := openAlert(t, store, ctx, "voice case")
	rc := rcFor(m, alert)
	ch := mustChannel(t, m, ctx, "voice")
	_, body, err := m.render(ch, rc)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := m.send(ctx, ch, "+15550123", "", body, rc)
	if err != nil || sid != "CA987" {
		t.Fatalf("call: %v sid=%q", err, sid)
	}
	if to.Load() != "+15550123" || from.Load() != "+15550100" {
		t.Fatalf("to/from: %v %v", to.Load(), from.Load())
	}
	tw, _ := twiml.Load().(string)
	// real alert + BaseURL + AckSecret ⇒ DTMF gather with signed token
	if !strings.Contains(tw, "/api/v1/voice/gather/") || !strings.Contains(tw, "Press 4") {
		t.Fatalf("twiml: %s", tw)
	}
}

func TestGenericVoiceGateway(t *testing.T) {
	m, store, ctx := setupMgr(t)

	var hit atomic.Value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(r.URL.String())
	}))
	t.Cleanup(ts.Close)

	putChannel(t, store, ctx, model.NotificationChannel{
		Name: "voicebox", Type: model.ChannelVoice,
		Config: map[string]string{"provider": "generic-http",
			"url": ts.URL + "/call?to={to}&text={text}"}})

	ch := mustChannel(t, m, ctx, "voicebox")
	if _, err := m.send(ctx, ch, "+43123", "", "Alarm", &RenderContext{}); err != nil {
		t.Fatal(err)
	}
	got, _ := hit.Load().(string)
	if !strings.Contains(got, "to=%2B43123") || !strings.Contains(got, "text=Alarm") {
		t.Fatalf("gateway url: %s", got)
	}
}

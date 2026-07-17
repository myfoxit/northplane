package agi

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/api"
	"github.com/northplane/northplane/internal/model"
)

// fakeAsterisk speaks the PBX side of FastAGI: it sends the env block,
// answers every command per the digit script, and records the commands.
type fakeAsterisk struct {
	conn net.Conn
	env  map[string]string
	// digits returned for successive GET DATA / WAIT FOR DIGIT calls.
	inputs []string

	mu   sync.Mutex
	cmds []string
	done chan struct{}
}

func runFakeAsterisk(t *testing.T, conn net.Conn, env map[string]string, inputs []string) *fakeAsterisk {
	t.Helper()
	f := &fakeAsterisk{conn: conn, env: env, inputs: inputs, done: make(chan struct{})}
	go f.loop()
	return f
}

func (f *fakeAsterisk) loop() {
	defer close(f.done)
	var b strings.Builder
	for k, v := range f.env {
		fmt.Fprintf(&b, "%s: %s\n", k, v)
	}
	b.WriteString("\n")
	if _, err := io.WriteString(f.conn, b.String()); err != nil {
		return
	}
	r := bufio.NewReader(f.conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.mu.Lock()
		f.cmds = append(f.cmds, line)
		f.mu.Unlock()

		reply := "200 result=0\n"
		switch {
		case strings.HasPrefix(line, "GET DATA"), strings.HasPrefix(line, "WAIT FOR DIGIT"):
			d := ""
			if len(f.inputs) > 0 {
				d = f.inputs[0]
				f.inputs = f.inputs[1:]
			}
			if strings.HasPrefix(line, "WAIT FOR DIGIT") {
				code := 0
				if d != "" {
					code = int(d[0])
				}
				reply = fmt.Sprintf("200 result=%d\n", code)
			} else {
				reply = fmt.Sprintf("200 result=%s\n", d)
			}
		case strings.HasPrefix(line, "HANGUP"):
			reply = "200 result=1\n"
		}
		if _, err := io.WriteString(f.conn, reply); err != nil {
			return
		}
	}
}

func (f *fakeAsterisk) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

func (f *fakeAsterisk) has(prefix string) bool {
	for _, c := range f.commands() {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// fakeActs records alarm operations.
type fakeActs struct {
	mu       sync.Mutex
	raised   []api.RaiseParams
	acked    []string
	resolved []string
	labels   map[string]model.Labels
	open     []*model.Alert
	menu     *model.IVRMenu
	contacts map[string]string // phone → name
}

func (f *fakeActs) Raise(_ context.Context, _ string, p api.RaiseParams) (*model.Alert, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.raised = append(f.raised, p)
	return &model.Alert{ID: "al-1", Title: p.Title, Severity: p.Severity}, true, nil
}

func (f *fakeActs) Ack(_ context.Context, _, alertID, by string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acked = append(f.acked, alertID+"/"+by)
	return nil
}

func (f *fakeActs) Resolve(_ context.Context, _, alertID, by string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolved = append(f.resolved, alertID+"/"+by)
	return nil
}

func (f *fakeActs) OpenAlerts(context.Context, string, int) []*model.Alert { return f.open }

func (f *fakeActs) ContactNameByPhone(_ context.Context, _, phone string) string {
	return f.contacts[normalizePhone(phone)]
}

func (f *fakeActs) Menu(context.Context, string, string) *model.IVRMenu {
	if f.menu != nil {
		return f.menu
	}
	return model.DefaultIVRMenu()
}

func (f *fakeActs) AttachLabels(_ context.Context, _, alertID string, l model.Labels) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.labels == nil {
		f.labels = map[string]model.Labels{}
	}
	f.labels[alertID] = l
}

func runConversation(t *testing.T, src *model.EventSource, acts *fakeActs,
	env map[string]string, inputs []string) *fakeAsterisk {
	t.Helper()
	client, server := net.Pipe()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	_ = server.SetDeadline(time.Now().Add(5 * time.Second))
	fake := runFakeAsterisk(t, client, env, inputs)

	s, err := newSession(server)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	conv := &conversation{s: s, src: src, acts: acts,
		log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	conv.run(context.Background())
	_ = server.Close()
	<-fake.done
	return fake
}

func TestAGITriggerAlarmWithVoicemail(t *testing.T) {
	src := &model.EventSource{ID: "src-agi", TenantID: model.DefaultTenant,
		Name: "alarmline", Type: "asterisk-inbound",
		Config: map[string]string{"ttsApp": "Flite", "escalationPolicy": "nachtdienst"},
		Labels: model.Labels{"line": "haupt"},
	}
	acts := &fakeActs{}
	env := map[string]string{
		"agi_network_script": "alarmline",
		"agi_callerid":       "+4915112345678",
		"agi_dnid":           "911",
		"agi_uniqueid":       "1700000000.42",
	}
	// menu digit 1 → trigger (builtin menu records voicemail)
	fake := runConversation(t, src, acts, env, []string{"1"})

	if len(acts.raised) != 1 {
		t.Fatalf("raised = %d, want 1", len(acts.raised))
	}
	p := acts.raised[0]
	if p.Title != "Phone alarm from +4915112345678" || p.Severity != model.SevCritical {
		t.Fatalf("raise params wrong: %+v", p)
	}
	if p.EscalationPolicy != "nachtdienst" || p.DedupKey != "agi/1700000000.42" {
		t.Fatalf("policy/dedup wrong: %+v", p)
	}
	if p.Labels["caller"] != "+4915112345678" || p.Labels["line"] != "haupt" {
		t.Fatalf("labels wrong: %v", p.Labels)
	}
	if !fake.has("ANSWER") || !fake.has("RECORD FILE") {
		t.Fatalf("expected ANSWER + RECORD FILE, got %v", fake.commands())
	}
	if acts.labels["al-1"]["recordingFile"] != "/var/spool/asterisk/recording/np-al-1.wav" {
		t.Fatalf("recording label wrong: %v", acts.labels)
	}
	// TTS mode speaks via EXEC Flite
	if !fake.has(`EXEC Flite`) {
		t.Fatalf("expected EXEC Flite prompts, got %v", fake.commands())
	}
}

func TestAGIPinGateAndAck(t *testing.T) {
	menu := &model.IVRMenu{Name: "m", Language: "de-DE", PIN: "4711",
		Options: []model.IVROption{{Digit: "3", Action: model.IVRAckAlert}}}
	src := &model.EventSource{ID: "src2", TenantID: model.DefaultTenant,
		Name: "nacht", Type: "asterisk-inbound",
		Config: map[string]string{"ttsApp": "Flite"}}
	acts := &fakeActs{menu: menu,
		open:     []*model.Alert{{ID: "al-9", Title: "Pumpe", Severity: model.SevCritical}},
		contacts: map[string]string{"+4915177700001": "Bereitschaft"}}
	env := map[string]string{
		"agi_network_script": "src2",
		"agi_callerid":       "+4915177700001",
		"agi_uniqueid":       "1700000001.1",
	}
	// wrong PIN, right PIN, then digit 3 → ack (single alert, no chooser)
	fake := runConversation(t, src, acts, env, []string{"9999", "4711", "3"})

	if len(acts.acked) != 1 || acts.acked[0] != "al-9/Bereitschaft" {
		t.Fatalf("ack = %v, want al-9 by contact name", acts.acked)
	}
	if len(acts.raised) != 0 {
		t.Fatalf("unexpected raise: %v", acts.raised)
	}
	if !fake.has("GET DATA") {
		t.Fatalf("expected PIN GET DATA, got %v", fake.commands())
	}
}

func TestAGIResolveChooser(t *testing.T) {
	menu := &model.IVRMenu{Name: "m",
		Options: []model.IVROption{{Digit: "4", Action: model.IVRResolveAlert}}}
	src := &model.EventSource{ID: "src3", TenantID: model.DefaultTenant,
		Name: "line3", Type: "asterisk-inbound",
		Config: map[string]string{"ttsApp": "Flite"}}
	acts := &fakeActs{menu: menu, open: []*model.Alert{
		{ID: "al-1", Title: "Erster", Severity: model.SevWarning},
		{ID: "al-2", Title: "Zweiter", Severity: model.SevCritical},
	}}
	env := map[string]string{"agi_network_script": "line3",
		"agi_callerid": "+43123", "agi_uniqueid": "1700000002.7"}
	// menu digit 4 → chooser → pick 2 → resolve al-2
	_ = runConversation(t, src, acts, env, []string{"4", "2"})

	if len(acts.resolved) != 1 || !strings.HasPrefix(acts.resolved[0], "al-2/") {
		t.Fatalf("resolved = %v, want al-2", acts.resolved)
	}
}

func TestAGIRouteAndCallerAllow(t *testing.T) {
	l := &listener{sources: []*model.EventSource{
		{ID: "id-1", Name: "haupt"},
		{ID: "id-2", Name: "neben"},
	}}
	if src := l.route("haupt"); src == nil || src.ID != "id-1" {
		t.Fatalf("route by name failed")
	}
	if src := l.route("id-2"); src == nil || src.ID != "id-2" {
		t.Fatalf("route by id failed")
	}
	if src := l.route("unknown"); src != nil {
		t.Fatalf("unknown script must not route")
	}
	single := &listener{sources: []*model.EventSource{{ID: "only"}}}
	if src := single.route(""); src == nil {
		t.Fatalf("single-source listener must accept empty script")
	}
	if !allowedCaller("", "+491511") || !allowedCaller("+49151,+43", "+43 660 1") ||
		allowedCaller("+49151", "+43123") {
		t.Fatal("allowedCaller prefix logic wrong")
	}
}

package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

func setupMgr(t *testing.T) (*Manager, *storage.Store, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	m := New(store, eventbus.New(), nil)
	m.BaseURL = "https://np.test"
	m.AckSecret = []byte("test-secret")
	return m, store, ctx
}

func putChannel(t *testing.T, store *storage.Store, ctx context.Context, ch model.NotificationChannel) {
	t.Helper()
	ch.Enabled = true
	if _, err := store.PutResource(ctx, model.DefaultTenant, storage.KindChannel, ch.Name, ch, 0); err != nil {
		t.Fatal(err)
	}
}

func openAlert(t *testing.T, store *storage.Store, ctx context.Context, title string) *model.Alert {
	t.Helper()
	a := &model.Alert{TenantID: model.DefaultTenant, Severity: model.SevCritical,
		Title: title, DedupKey: "t/" + title, Payload: json.RawMessage(`{}`)}
	stored, created, err := store.UpsertAlert(ctx, a)
	if err != nil || !created {
		t.Fatalf("open alert: %v created=%v", err, created)
	}
	return stored
}

func rcFor(m *Manager, a *model.Alert) *RenderContext {
	return m.renderContext(a, &model.Contact{ID: "c1", Name: "tester"},
		notifyJob{AlertID: a.ID, TenantID: a.TenantID})
}

func TestServiceNowCreateAndAutoClose(t *testing.T) {
	m, store, ctx := setupMgr(t)

	var created, closed atomic.Int64
	var gotAuth, gotShort, gotGroup, closeState atomic.Value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/now/table/incident":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotShort.Store(body["short_description"])
			gotGroup.Store(body["assignment_group"])
			user, pass, _ := r.BasicAuth()
			gotAuth.Store(user + ":" + pass)
			created.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"result":{"sys_id":"abc123","number":"INC0010001"}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/api/now/table/incident/abc123":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			closeState.Store(body["state"])
			closed.Add(1)
			_, _ = w.Write([]byte(`{"result":{}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)

	putChannel(t, store, ctx, model.NotificationChannel{
		Name: "sn", Type: model.ChannelServiceNow,
		Config: map[string]string{"url": ts.URL, "username": "np", "password": "pw"}})

	alert := openAlert(t, store, ctx, "db down")
	rc := rcFor(m, alert)
	ref, err := m.sendTicket(ctx, mustChannel(t, m, ctx, "sn"), "", "DB is down", rc,
		&model.TicketAction{Channel: "sn", AutoClose: true,
			Params: map[string]string{"assignmentGroup": "dba-team"}})
	if err != nil {
		t.Fatal(err)
	}
	if ref != "abc123" || created.Load() != 1 {
		t.Fatalf("ref=%q created=%d", ref, created.Load())
	}
	if gotAuth.Load() != "np:pw" {
		t.Fatalf("basic auth: %v", gotAuth.Load())
	}
	if gotGroup.Load() != "dba-team" {
		t.Fatalf("assignment_group: %v", gotGroup.Load())
	}
	if gotShort.Load() != "[CRITICAL] db down" {
		t.Fatalf("short_description: %v", gotShort.Load())
	}

	// ticket persisted on the alert
	stored, err := store.GetAlert(ctx, model.DefaultTenant, alert.ID)
	if err != nil || stored.Ticket == nil {
		t.Fatalf("ticket not linked: %v %+v", err, stored)
	}
	if stored.Ticket.Ref != "abc123" || !stored.Ticket.AutoClose || stored.Ticket.Channel != "sn" {
		t.Fatalf("ticket ref: %+v", stored.Ticket)
	}
	if stored.Ticket.URL == "" {
		t.Fatal("ticket needs a human URL")
	}

	// resolving the alert enqueues + delivers the auto-close
	if _, err := store.ResolveAlert(ctx, model.DefaultTenant, alert.ID, model.AlertResolved); err != nil {
		t.Fatal(err)
	}
	items, err := store.ClaimOutbox(ctx, time.Now().UTC().Add(time.Second), time.Minute, 10)
	if err != nil || len(items) != 1 || items[0].Kind != "ticket-close" {
		t.Fatalf("outbox: %v %+v", err, items)
	}
	if _, err := m.deliverTicketClose(ctx, items[0]); err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 1 || closeState.Load() != "6" {
		t.Fatalf("close: n=%d state=%v", closed.Load(), closeState.Load())
	}
}

func TestZendeskCreateAndClose(t *testing.T) {
	m, store, ctx := setupMgr(t)

	var auth, closeStatus atomic.Value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		auth.Store(user + ":" + pass)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/tickets.json":
			var body struct {
				Ticket map[string]any `json:"ticket"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Ticket["priority"] != "urgent" {
				t.Errorf("priority: %v", body.Ticket["priority"])
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"ticket":{"id":42}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v2/tickets/42.json":
			var body struct {
				Ticket map[string]any `json:"ticket"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			closeStatus.Store(body.Ticket["status"])
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)

	putChannel(t, store, ctx, model.NotificationChannel{
		Name: "zd", Type: model.ChannelZendesk,
		Config: map[string]string{"url": ts.URL, "email": "ops@example.net", "apiToken": "ztok"}})

	alert := openAlert(t, store, ctx, "web down")
	ref, err := m.sendTicket(ctx, mustChannel(t, m, ctx, "zd"), "", "WEB is down",
		rcFor(m, alert), &model.TicketAction{Channel: "zd", AutoClose: true})
	if err != nil || ref != "42" {
		t.Fatalf("create: %v ref=%q", err, ref)
	}
	if auth.Load() != "ops@example.net/token:ztok" {
		t.Fatalf("zendesk token auth: %v", auth.Load())
	}
	stored, _ := store.GetAlert(ctx, model.DefaultTenant, alert.ID)
	if stored.Ticket == nil || stored.Ticket.Type != "zendesk" {
		t.Fatalf("ticket: %+v", stored.Ticket)
	}
	if err := m.closeTicket(ctx, model.DefaultTenant, stored.Ticket, "resolved"); err != nil {
		t.Fatal(err)
	}
	if closeStatus.Load() != "solved" {
		t.Fatalf("close status: %v", closeStatus.Load())
	}
}

func TestJiraCreateAndClose(t *testing.T) {
	m, store, ctx := setupMgr(t)

	var transitioned, commented atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/2/issue":
			var body struct {
				Fields map[string]any `json:"fields"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if p, _ := body.Fields["project"].(map[string]any); p["key"] != "OPS" {
				t.Errorf("project: %v", body.Fields["project"])
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"10001","key":"OPS-7"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/2/issue/OPS-7/transitions":
			transitioned.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/2/issue/OPS-7/comment":
			commented.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)

	putChannel(t, store, ctx, model.NotificationChannel{
		Name: "jira", Type: model.ChannelJira,
		Config: map[string]string{"url": ts.URL, "project": "OPS",
			"username": "bot", "password": "jtok", "closeTransitionId": "31"}})

	alert := openAlert(t, store, ctx, "queue stuck")
	ref, err := m.sendTicket(ctx, mustChannel(t, m, ctx, "jira"), "", "Queue stuck",
		rcFor(m, alert), nil)
	if err != nil || ref != "OPS-7" {
		t.Fatalf("create: %v ref=%q", err, ref)
	}
	stored, _ := store.GetAlert(ctx, model.DefaultTenant, alert.ID)
	if stored.Ticket == nil || stored.Ticket.URL != ts.URL+"/browse/OPS-7" {
		t.Fatalf("ticket url: %+v", stored.Ticket)
	}
	if err := m.closeTicket(ctx, model.DefaultTenant, stored.Ticket, "done"); err != nil {
		t.Fatal(err)
	}
	if transitioned.Load() != 1 || commented.Load() != 1 {
		t.Fatalf("close: transitions=%d comments=%d", transitioned.Load(), commented.Load())
	}
}

func TestGenericTicketGateway(t *testing.T) {
	m, store, ctx := setupMgr(t)

	var closeHits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/create":
			if r.Header.Get("Authorization") != "Bearer gw-token" {
				t.Errorf("auth: %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"data":{"ticketId":"T-9"}}`))
		case "/close/T-9":
			closeHits.Add(1)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)

	putChannel(t, store, ctx, model.NotificationChannel{
		Name: "gw", Type: model.ChannelTicket,
		Config: map[string]string{"url": ts.URL + "/create", "token": "gw-token",
			"refField": "data.ticketId", "ticketUrlTemplate": "https://tickets.example.net/{ref}",
			"closeUrl": ts.URL + "/close/{ref}", "autoClose": "true"}})

	alert := openAlert(t, store, ctx, "generic case")
	ch := mustChannel(t, m, ctx, "gw")
	rc := rcFor(m, alert)
	_, body, err := m.render(ch, rc)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := m.sendTicket(ctx, ch, "", body, rc, nil)
	if err != nil || ref != "T-9" {
		t.Fatalf("create: %v ref=%q", err, ref)
	}
	stored, _ := store.GetAlert(ctx, model.DefaultTenant, alert.ID)
	if stored.Ticket == nil || stored.Ticket.URL != "https://tickets.example.net/T-9" {
		t.Fatalf("ticket: %+v", stored.Ticket)
	}
	if !stored.Ticket.AutoClose {
		t.Fatal("channel autoClose=true must propagate")
	}
	if err := m.closeTicket(ctx, model.DefaultTenant, stored.Ticket, "done"); err != nil {
		t.Fatal(err)
	}
	if closeHits.Load() != 1 {
		t.Fatalf("close hits: %d", closeHits.Load())
	}
}

func TestDeliverActionLegacyServiceNowAndIncidentMirror(t *testing.T) {
	m, store, ctx := setupMgr(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"sys_id":"sn-1","number":"INC1"}}`))
	}))
	t.Cleanup(ts.Close)
	putChannel(t, store, ctx, model.NotificationChannel{
		Name: "sn", Type: model.ChannelServiceNow,
		Config: map[string]string{"url": ts.URL}})

	// alert bundled into an incident: ticket URL must mirror
	inc := &model.Incident{TenantID: model.DefaultTenant, Status: model.IncidentOpen,
		Severity: model.SevCritical, Title: "incident", CreatedBy: "rule:r"}
	if err := store.CreateIncident(ctx, inc); err != nil {
		t.Fatal(err)
	}
	alert := openAlert(t, store, ctx, "sn case")
	if err := store.AssignAlertIncident(ctx, model.DefaultTenant, alert.ID, inc.ID); err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(map[string]any{
		"action": &model.EscalationAction{ServiceNow: &model.ServiceNowAction{
			AssignmentGroup: "noc", AutoClose: true}},
		"alert": alert, "policy": "p", "step": 0})
	if _, err := m.deliverAction(ctx, &storage.OutboxItem{
		TenantID: model.DefaultTenant, Kind: "action", Payload: payload}); err != nil {
		t.Fatal(err)
	}

	stored, _ := store.GetAlert(ctx, model.DefaultTenant, alert.ID)
	if stored.Ticket == nil || stored.Ticket.Ref != "sn-1" || !stored.Ticket.AutoClose {
		t.Fatalf("legacy servicenow action: %+v", stored.Ticket)
	}
	got, _ := store.GetIncident(ctx, model.DefaultTenant, inc.ID)
	if got.TicketURL == "" {
		t.Fatal("incident ticketUrl must mirror the created ticket")
	}

	// repeated action must not create a second ticket
	if _, err := m.deliverAction(ctx, &storage.OutboxItem{
		TenantID: model.DefaultTenant, Kind: "action", Payload: payload}); err != nil {
		t.Fatal(err)
	}
}

func mustChannel(t *testing.T, m *Manager, ctx context.Context, name string) *model.NotificationChannel {
	t.Helper()
	ch, err := m.channelByName(ctx, model.DefaultTenant, name)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

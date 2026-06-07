package alerting_test

// Object-level notification routing (Nagios contact_groups semantics):
// hard state changes on objects with contacts/contactGroups notify those
// contacts directly — no alert rule involved.

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/alerting"
	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/notify"
	"github.com/northplane/northplane/internal/storage"
)

type objEnv struct {
	store *storage.Store
	bus   *eventbus.Bus
	cat   *catalog.Catalog
	sent  atomic.Int64
	last  atomic.Value // last body
}

func setupObjNotify(t *testing.T) (*objEnv, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	bus := eventbus.New()
	cat := catalog.New(store)
	e := &objEnv{store: store, bus: bus, cat: cat}

	mustPut := func(kind, name string, doc any) {
		t.Helper()
		if _, err := store.PutResource(ctx, model.DefaultTenant, kind, name, doc, 0); err != nil {
			t.Fatalf("fixture %s/%s: %v", kind, name, err)
		}
	}
	anna := model.Contact{ID: model.NewID(), Name: "anna", Email: "anna@example.net",
		Preferences: []model.ChannelPreference{{Profile: "default",
			Channels: []model.ChannelType{model.ChannelEmail}}}}
	mustPut(storage.KindContact, "anna", anna)
	mustPut(storage.KindContactGroup, "ops", model.ContactGroup{
		Name: "ops", Members: []string{"anna"}})
	mustPut(storage.KindChannel, "mail", model.NotificationChannel{
		Name: "mail", Type: model.ChannelEmail, Enabled: true,
		Config: map[string]string{"host": "localhost"}})

	eng := alerting.NewEngine(store, cat, bus, nil, nil)
	mgr := notify.New(store, bus, nil)
	mgr.SendHook = func(ch *model.NotificationChannel, target, subject, body string) (string, error) {
		e.sent.Add(1)
		e.last.Store(subject + "\n" + body)
		return "prov-1", nil
	}
	go eng.Run(ctx)
	go mgr.Run(ctx)
	return e, ctx
}

func (e *objEnv) createObject(t *testing.T, ctx context.Context, kind model.Kind, name string, spec model.ObjectSpec) *model.Object {
	t.Helper()
	obj := &model.Object{TenantID: model.DefaultTenant, Kind: kind, Name: name, Spec: spec}
	if err := e.store.CreateObject(ctx, obj); err != nil {
		t.Fatal(err)
	}
	if err := e.cat.UpsertObject(obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func hardStateChange(obj *model.Object, from, to model.State) *model.Event {
	payload, _ := json.Marshal(model.StateChangePayload{
		ObjectName: obj.Name, Kind: obj.Kind,
		From: from, To: to,
		FromLabel: from.Label(obj.Kind), ToLabel: to.Label(obj.Kind),
		StateType: model.StateHard, Attempt: 2,
		Output: "probe says " + to.Label(obj.Kind), Labels: obj.Labels,
	})
	return &model.Event{ID: model.NewID(), TenantID: obj.TenantID,
		TS: time.Now().UTC(), Type: model.EventStateChange, ObjectID: obj.ID,
		Severity: model.SeverityFromState(to, obj.Kind), Payload: payload}
}

func TestObjectContactGroupNotification(t *testing.T) {
	e, ctx := setupObjNotify(t)
	host := e.createObject(t, ctx, model.KindHost, "web-1", model.ObjectSpec{
		Address: "127.0.0.1", CheckCommand: "builtin:icmp",
		ContactGroups: []string{"ops"},
	})

	// Hard DOWN → the ops group (anna) gets mailed, no alert rule needed.
	e.bus.PublishEvent(hardStateChange(host, model.HostUp, model.HostDown))
	waitFor(t, "object notification sent", 10*time.Second, func() bool {
		return e.sent.Load() == 1
	})
	body, _ := e.last.Load().(string)
	if !strings.Contains(body, "web-1") || !strings.Contains(body, "DOWN") {
		t.Fatalf("notification body should name the object and state: %q", body)
	}

	// Hard recovery → recovery notification (default notifyOn includes it).
	e.bus.PublishEvent(hardStateChange(host, model.HostDown, model.HostUp))
	waitFor(t, "recovery sent", 10*time.Second, func() bool {
		return e.sent.Load() == 2
	})
	body, _ = e.last.Load().(string)
	if !strings.Contains(body, "recovered") {
		t.Fatalf("recovery body: %q", body)
	}
}

func TestObjectNotifyOnFilterAndDisable(t *testing.T) {
	e, ctx := setupObjNotify(t)
	off := false

	// notifyOn=[critical]: a hard WARNING must stay silent.
	svc := e.createObject(t, ctx, model.KindService, "db-load", model.ObjectSpec{
		CheckCommand: "passive", ContactGroups: []string{"ops"},
		NotifyOn: []string{"critical"},
	})
	// enableNotifications=false: even a CRITICAL stays silent.
	muted := e.createObject(t, ctx, model.KindService, "muted", model.ObjectSpec{
		CheckCommand: "passive", ContactGroups: []string{"ops"},
		EnableNotifications: &off,
	})
	// Soft transitions never notify.
	softy := e.createObject(t, ctx, model.KindService, "softy", model.ObjectSpec{
		CheckCommand: "passive", ContactGroups: []string{"ops"},
	})

	e.bus.PublishEvent(hardStateChange(svc, model.StateOK, model.StateWarning))
	e.bus.PublishEvent(hardStateChange(muted, model.StateOK, model.StateCritical))
	soft := hardStateChange(softy, model.StateOK, model.StateCritical)
	var p model.StateChangePayload
	_ = json.Unmarshal(soft.Payload, &p)
	p.StateType = model.StateSoft
	soft.Payload, _ = json.Marshal(p)
	e.bus.PublishEvent(soft)

	// The matching case proves the pipeline is live; everything before it
	// must have been dropped.
	e.bus.PublishEvent(hardStateChange(svc, model.StateWarning, model.StateCritical))
	waitFor(t, "critical notification", 10*time.Second, func() bool {
		return e.sent.Load() >= 1
	})
	time.Sleep(500 * time.Millisecond) // absorb any stragglers
	if got := e.sent.Load(); got != 1 {
		t.Fatalf("exactly the notifyOn-matching transition must notify, got %d", got)
	}
}

func TestObjectNotificationPeriodGate(t *testing.T) {
	e, ctx := setupObjNotify(t)
	// A period that is never open (no days) gates everything off.
	if _, err := e.store.PutResource(ctx, model.DefaultTenant, storage.KindTimePeriod,
		"never", model.TimePeriod{Name: "never", Days: map[string][]string{"monday": {"00:00-00:00"}}}, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.cat.ReloadTenant(ctx, model.DefaultTenant); err != nil {
		t.Fatal(err)
	}
	gated := e.createObject(t, ctx, model.KindHost, "gated", model.ObjectSpec{
		Address: "127.0.0.1", CheckCommand: "builtin:icmp",
		ContactGroups: []string{"ops"}, NotificationPeriod: "never",
	})
	open := e.createObject(t, ctx, model.KindHost, "open", model.ObjectSpec{
		Address: "127.0.0.1", CheckCommand: "builtin:icmp",
		ContactGroups: []string{"ops"},
	})

	e.bus.PublishEvent(hardStateChange(gated, model.HostUp, model.HostDown))
	e.bus.PublishEvent(hardStateChange(open, model.HostUp, model.HostDown))
	waitFor(t, "ungated notification", 10*time.Second, func() bool {
		return e.sent.Load() >= 1
	})
	time.Sleep(500 * time.Millisecond)
	if got := e.sent.Load(); got != 1 {
		t.Fatalf("notificationPeriod must gate the dispatch, got %d sends", got)
	}
	body, _ := e.last.Load().(string)
	if !strings.Contains(body, "open") {
		t.Fatalf("the ungated object must be the one delivered: %q", body)
	}
}

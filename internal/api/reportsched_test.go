package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/notify"
	"github.com/northplane/northplane/internal/storage"
)

// --- parseSchedule -----------------------------------------------------------

func TestParseSchedule(t *testing.T) {
	cases := []struct {
		spec    string
		kind    schedKind
		weekday time.Weekday
		day     int
		hour    int
		minute  int
		wantErr bool
	}{
		{spec: "daily", kind: schedDaily, hour: 7, minute: 0},
		{spec: "daily@09:30", kind: schedDaily, hour: 9, minute: 30},
		{spec: "DAILY@23:59", kind: schedDaily, hour: 23, minute: 59},
		{spec: "weekly:monday", kind: schedWeekly, weekday: time.Monday, hour: 7},
		{spec: "weekly:mon@06:15", kind: schedWeekly, weekday: time.Monday, hour: 6, minute: 15},
		{spec: "weekly:sunday", kind: schedWeekly, weekday: time.Sunday, hour: 7},
		{spec: "monthly", kind: schedMonthly, day: 1, hour: 7},
		{spec: "monthly:15", kind: schedMonthly, day: 15, hour: 7},
		{spec: "monthly:28@08:00", kind: schedMonthly, day: 28, hour: 8},
		{spec: " monthly:1 ", kind: schedMonthly, day: 1, hour: 7},
		// invalid
		{spec: "", wantErr: true},
		{spec: "hourly", wantErr: true},
		{spec: "daily:5", wantErr: true},
		{spec: "weekly", wantErr: true},
		{spec: "weekly:funday", wantErr: true},
		{spec: "monthly:0", wantErr: true},
		{spec: "monthly:32", wantErr: true},
		{spec: "daily@25:00", wantErr: true},
		{spec: "daily@09:60", wantErr: true},
		{spec: "daily@9", wantErr: true},
	}
	for _, c := range cases {
		got, err := parseSchedule(c.spec)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseSchedule(%q): want error, got %+v", c.spec, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSchedule(%q): %v", c.spec, err)
			continue
		}
		if got.Kind != c.kind || got.Weekday != c.weekday || got.Day != c.day ||
			got.Hour != c.hour || got.Minute != c.minute {
			t.Errorf("parseSchedule(%q) = %+v, want kind=%v wd=%v day=%d %02d:%02d",
				c.spec, got, c.kind, c.weekday, c.day, c.hour, c.minute)
		}
	}
}

// --- slotFor -----------------------------------------------------------------

func TestSlotFor(t *testing.T) {
	loc := time.UTC
	// Wednesday 2026-06-10 10:00.
	now := time.Date(2026, 6, 10, 10, 0, 0, 0, loc)

	t.Run("daily due", func(t *testing.T) {
		s, _ := parseSchedule("daily@07:00")
		key, at := slotFor(s, now)
		if key != "2026-06-10" {
			t.Fatalf("daily key = %q", key)
		}
		if !now.After(at) {
			t.Fatalf("daily should be due: now=%v at=%v", now, at)
		}
	})

	t.Run("daily not yet due", func(t *testing.T) {
		s, _ := parseSchedule("daily@23:00")
		_, at := slotFor(s, now)
		if !now.Before(at) {
			t.Fatalf("daily@23:00 should not be due at 10:00: at=%v", at)
		}
	})

	t.Run("weekly iso week + weekday anchor", func(t *testing.T) {
		// 2026-06-10 is ISO week 24; Monday of that week is 2026-06-08.
		s, _ := parseSchedule("weekly:monday@07:00")
		key, at := slotFor(s, now)
		if key != "2026-W24" {
			t.Fatalf("weekly key = %q, want 2026-W24", key)
		}
		if at.Weekday() != time.Monday || at.Day() != 8 || at.Hour() != 7 {
			t.Fatalf("weekly at = %v, want Mon 2026-06-08 07:00", at)
		}
		if !now.After(at) { // Monday already passed by Wednesday → due
			t.Fatalf("weekly:monday should be due on Wednesday")
		}
	})

	t.Run("weekly future weekday same week not due", func(t *testing.T) {
		// Friday is still ahead of Wednesday in the same ISO week.
		s, _ := parseSchedule("weekly:friday@07:00")
		_, at := slotFor(s, now)
		if !now.Before(at) {
			t.Fatalf("weekly:friday should not be due on Wednesday: at=%v", at)
		}
	})

	t.Run("monthly key + day anchor", func(t *testing.T) {
		s, _ := parseSchedule("monthly:1@07:00")
		key, at := slotFor(s, now)
		if key != "2026-06" {
			t.Fatalf("monthly key = %q", key)
		}
		if at.Day() != 1 || at.Hour() != 7 {
			t.Fatalf("monthly at = %v, want day 1 07:00", at)
		}
		if !now.After(at) {
			t.Fatalf("monthly:1 should be due on the 10th")
		}
	})

	t.Run("monthly day clamps to month length", func(t *testing.T) {
		feb := time.Date(2026, 2, 20, 12, 0, 0, 0, loc) // 2026 Feb has 28 days
		s, _ := parseSchedule("monthly:31")
		_, at := slotFor(s, feb)
		if at.Month() != time.February || at.Day() != 28 {
			t.Fatalf("monthly:31 in Feb 2026 should clamp to the 28th, got %v", at)
		}
	})
}

// --- runDue (due-logic with injected now) ------------------------------------

type captured struct {
	mu    sync.Mutex
	mails []string // "to|subject|isHTML"
}

func newSchedTestAPI(t *testing.T) (*API, *captured, *storage.Store) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir(), RetentionMonths: 12})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := eventbus.New()
	cap := &captured{}
	mgr := notify.New(store, bus, log)
	mgr.SendHook = func(ch *model.NotificationChannel, target, subject, body string) (string, error) {
		cap.mu.Lock()
		defer cap.mu.Unlock()
		isHTML := len(body) > 0 && (body[0] == '<')
		cap.mails = append(cap.mails, target+"|"+subject+"|"+boolStr(isHTML))
		return "test-id", nil
	}
	a := &API{
		Store:   store,
		Catalog: catalog.New(store),
		Bus:     bus,
		Notify:  mgr,
		Log:     log,
	}
	return a, cap, store
}

func boolStr(b bool) string {
	if b {
		return "html"
	}
	return "text"
}

func putReport(t *testing.T, store *storage.Store, rep model.Report) {
	t.Helper()
	doc := map[string]any{
		"name":     rep.Name,
		"type":     string(rep.Type),
		"schedule": rep.Schedule,
		"email":    rep.Email,
		"keep":     rep.Keep,
	}
	if rep.Params != nil {
		var p any
		_ = json.Unmarshal(rep.Params, &p)
		doc["params"] = p
	}
	if _, err := store.PutResource(context.Background(), model.DefaultTenant,
		storage.KindReport, rep.Name, doc, -1); err != nil {
		t.Fatalf("put report %q: %v", rep.Name, err)
	}
}

func putEmailChannel(t *testing.T, store *storage.Store, name string, enabled bool) {
	t.Helper()
	doc := map[string]any{
		"name":    name,
		"type":    string(model.ChannelEmail),
		"enabled": enabled,
		"config":  map[string]string{"host": "smtp.example.net"},
	}
	if _, err := store.PutResource(context.Background(), model.DefaultTenant,
		storage.KindChannel, name, doc, -1); err != nil {
		t.Fatalf("put channel %q: %v", name, err)
	}
}

func TestRunDueArchivesAndMails(t *testing.T) {
	a, cap, store := newSchedTestAPI(t)
	ctx := context.Background()

	// audit report needs no catalog/selector; reads seeded roles.
	putReport(t, store, model.Report{
		Name: "perm-monthly", Type: model.ReportAudit,
		Schedule: "monthly:1@07:00",
		Email:    []string{"a@example.net", "b@example.net"},
		Keep:     6,
	})
	putEmailChannel(t, store, "smtp", true)

	// On the 10th, the monthly:1 slot is due and unarchived → fires.
	now := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	a.runDue(ctx, now)

	list, err := store.ListReportArchive(ctx, model.DefaultTenant, "perm-monthly", 10)
	if err != nil {
		t.Fatalf("list archive: %v", err)
	}
	// HTML + CSV archived for slot 2026-06.
	if len(list) != 2 {
		t.Fatalf("want 2 archived rows, got %d", len(list))
	}
	gotFormats := map[string]bool{}
	for _, e := range list {
		gotFormats[e.Format] = true
		if e.Slot != "2026-06" {
			t.Fatalf("unexpected slot %q", e.Slot)
		}
	}
	if !gotFormats["html"] || !gotFormats["csv"] {
		t.Fatalf("missing formats: %+v", gotFormats)
	}

	cap.mu.Lock()
	nMails := len(cap.mails)
	mails := append([]string(nil), cap.mails...)
	cap.mu.Unlock()
	if nMails != 2 {
		t.Fatalf("want 2 mails (one per recipient), got %d: %v", nMails, mails)
	}
	for _, m := range mails {
		if !contains(m, "[Northplane] Report perm-monthly 2026-06") {
			t.Fatalf("subject wrong: %q", m)
		}
		if !contains(m, "|html") {
			t.Fatalf("report mail should be HTML: %q", m)
		}
	}

	// Idempotency: a second sweep in the same slot must not re-archive or
	// re-mail (the archive row is the dedup gate).
	a.runDue(ctx, now.Add(time.Minute))
	list2, _ := store.ListReportArchive(ctx, model.DefaultTenant, "perm-monthly", 10)
	if len(list2) != 2 {
		t.Fatalf("dedup failed: archive grew to %d rows", len(list2))
	}
	cap.mu.Lock()
	again := len(cap.mails)
	cap.mu.Unlock()
	if again != 2 {
		t.Fatalf("dedup failed: mails grew to %d", again)
	}
}

func TestRunDueSkipsUnscheduledAndFuture(t *testing.T) {
	a, cap, store := newSchedTestAPI(t)
	ctx := context.Background()

	// no schedule ⇒ never runs.
	putReport(t, store, model.Report{Name: "ondemand", Type: model.ReportAudit})
	// scheduled but not due yet at 10:00 (fires at 23:00).
	putReport(t, store, model.Report{Name: "evening", Type: model.ReportAudit, Schedule: "daily@23:00"})

	now := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	a.runDue(ctx, now)

	for _, name := range []string{"ondemand", "evening"} {
		list, _ := store.ListReportArchive(ctx, model.DefaultTenant, name, 10)
		if len(list) != 0 {
			t.Fatalf("%s should not have run, got %d rows", name, len(list))
		}
	}
	cap.mu.Lock()
	n := len(cap.mails)
	cap.mu.Unlock()
	if n != 0 {
		t.Fatalf("no mail expected, got %d", n)
	}
}

func TestRunReportOnceArchivesEvenWhenMailFails(t *testing.T) {
	a, _, store := newSchedTestAPI(t)
	ctx := context.Background()
	// override the hook to fail every send.
	a.Notify.SendHook = func(ch *model.NotificationChannel, target, subject, body string) (string, error) {
		return "", errTestSend
	}

	putReport(t, store, model.Report{
		Name: "daily-rep", Type: model.ReportAudit, Schedule: "daily@07:00",
		Email: []string{"x@example.net"},
	})
	putEmailChannel(t, store, "smtp", true)

	now := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	res, err := a.runReportOnce(ctx, model.DefaultTenant, mustReport(t, store, "daily-rep"), "2026-06-10", now)
	if err != nil {
		t.Fatalf("runReportOnce returned error despite mail-only failure: %v", err)
	}
	if !res.Archived {
		t.Fatal("expected Archived=true")
	}
	if res.MailError == "" {
		t.Fatal("expected MailError to be recorded")
	}
	if res.Sent != 0 {
		t.Fatalf("expected 0 sent, got %d", res.Sent)
	}
	// slot must be marked done so it is not retried in a loop.
	if has, _ := store.HasReportArchiveSlot(ctx, model.DefaultTenant, "daily-rep", "2026-06-10"); !has {
		t.Fatal("slot should be archived even though mail failed")
	}
}

func TestReportEmailChannelSelection(t *testing.T) {
	a, _, store := newSchedTestAPI(t)
	ctx := context.Background()

	putEmailChannel(t, store, "primary", true)
	putEmailChannel(t, store, "secondary", true)

	// named channel honoured
	repNamed := model.Report{Name: "r1", Type: model.ReportAudit,
		Params: json.RawMessage(`{"channel":"secondary"}`)}
	putReport(t, store, repNamed)
	ch, err := a.reportEmailChannel(ctx, model.DefaultTenant, mustReport(t, store, "r1"))
	if err != nil || ch.Name != "secondary" {
		t.Fatalf("named channel: got %+v err=%v", ch, err)
	}

	// no name ⇒ first enabled email channel (name-ordered: "primary").
	repDefault := model.Report{Name: "r2", Type: model.ReportAudit}
	putReport(t, store, repDefault)
	ch, err = a.reportEmailChannel(ctx, model.DefaultTenant, mustReport(t, store, "r2"))
	if err != nil || ch.Name != "primary" {
		t.Fatalf("default channel: got %+v err=%v", ch, err)
	}

	// named-but-missing ⇒ error
	repBad := model.Report{Name: "r3", Type: model.ReportAudit,
		Params: json.RawMessage(`{"channel":"nope"}`)}
	putReport(t, store, repBad)
	if _, err := a.reportEmailChannel(ctx, model.DefaultTenant, mustReport(t, store, "r3")); err == nil {
		t.Fatal("missing named channel should error")
	}
}

func mustReport(t *testing.T, store *storage.Store, name string) *model.Report {
	t.Helper()
	rep, err := storage.LoadOne[model.Report](context.Background(), store, model.DefaultTenant,
		storage.KindReport, name)
	if err != nil {
		t.Fatalf("load report %q: %v", name, err)
	}
	return rep
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

var errTestSend = errTest("smtp boom")

type errTest string

func (e errTest) Error() string { return string(e) }

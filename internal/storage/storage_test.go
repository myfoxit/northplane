package storage

// Storage matrix (SPEC §16): every test runs against SQLite always and
// against PostgreSQL when NORTHPLANE_TEST_PG_DSN is set — identical test
// cases, both backends first-class from M0.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/selector"
)

func testStores(t *testing.T) map[string]*Store {
	t.Helper()
	ctx := context.Background()
	stores := map[string]*Store{}

	dir := t.TempDir()
	sq, err := Open(ctx, Options{DataDir: dir, RetentionMonths: 12})
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	t.Cleanup(func() { sq.Close() })
	stores["sqlite"] = sq

	if dsn := os.Getenv("NORTHPLANE_TEST_PG_DSN"); dsn != "" {
		resetPostgres(t, dsn)
		pg, err := Open(ctx, Options{DSN: dsn, RetentionMonths: 12})
		if err != nil {
			t.Fatalf("postgres open: %v", err)
		}
		t.Cleanup(func() { pg.Close() })
		stores["postgres"] = pg
	}
	return stores
}

// resetPostgres drops and recreates the public schema so every test starts
// against a pristine PostgreSQL database, mirroring the fresh t.TempDir() the
// SQLite store gets. The PG DSN points at one shared database; without this
// reset its tables accumulate rows across tests (audit chains keep growing,
// object identities collide on objects_ident), which is why the matrix flaked
// only on PostgreSQL. Open() re-runs migrations on the clean schema.
func resetPostgres(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("postgres reset: open: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(),
		`DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("postgres reset: %v", err)
	}
}

func matrix(t *testing.T, fn func(t *testing.T, s *Store)) {
	for name, s := range testStores(t) {
		t.Run(name, func(t *testing.T) { fn(t, s) })
	}
}

func TestObjectCRUDAndSelectors(t *testing.T) {
	matrix(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		host := &model.Object{
			TenantID: model.DefaultTenant, Kind: model.KindHost,
			Name: "db-prod-01.example.net", Folder: "/prod/wien",
			Labels: model.Labels{"env": "prod", "role": "postgres", "site": "wien"},
			Spec:   model.ObjectSpec{Address: "10.20.1.15", CheckCommand: "builtin:icmp"},
		}
		if err := s.CreateObject(ctx, host); err != nil {
			t.Fatalf("create host: %v", err)
		}
		svc := &model.Object{
			TenantID: model.DefaultTenant, Kind: model.KindService,
			Name: "postgres-connections", HostID: host.ID,
			Labels: model.Labels{"tier": "database", "env": "prod"},
			Spec:   model.ObjectSpec{CheckCommand: "exec:check_postgres", Interval: model.Duration(120 * time.Second)},
		}
		if err := s.CreateObject(ctx, svc); err != nil {
			t.Fatalf("create service: %v", err)
		}
		// duplicate name on same host must conflict
		if err := s.CreateObject(ctx, &model.Object{
			TenantID: model.DefaultTenant, Kind: model.KindService,
			Name: "postgres-connections", HostID: host.ID, Spec: model.ObjectSpec{},
		}); err == nil {
			t.Fatal("expected duplicate error")
		}

		got, err := s.GetObjectByName(ctx, model.DefaultTenant, model.KindHost, "", "db-prod-01.example.net")
		if err != nil || got.ID != host.ID {
			t.Fatalf("get by name: %v %+v", err, got)
		}

		sel := selector.MustParse("env=prod,role in (postgres,mysql)")
		objs, err := s.ListObjects(ctx, ObjectFilter{TenantID: model.DefaultTenant, Selector: sel})
		if err != nil || len(objs) != 1 || objs[0].ID != host.ID {
			t.Fatalf("selector list: %v, n=%d", err, len(objs))
		}
		// negation handled in Go
		sel2 := selector.MustParse("env=prod,role!=postgres")
		objs2, err := s.ListObjects(ctx, ObjectFilter{TenantID: model.DefaultTenant, Selector: sel2})
		if err != nil || len(objs2) != 1 || objs2[0].ID != svc.ID {
			t.Fatalf("neq list: %v, n=%d", err, len(objs2))
		}

		// optimistic locking
		host.Labels["owner"] = "team-data"
		if err := s.UpdateObject(ctx, host, 1); err != nil {
			t.Fatalf("update v1: %v", err)
		}
		stale := *host
		if err := s.UpdateObject(ctx, &stale, 1); err == nil {
			t.Fatal("expected version conflict")
		}

		// folder subtree filter
		objs3, err := s.ListObjects(ctx, ObjectFilter{TenantID: model.DefaultTenant, Folder: "/prod"})
		if err != nil || len(objs3) != 1 { // service has folder "/"
			t.Fatalf("folder list: %v n=%d", err, len(objs3))
		}

		// delete cascades to service
		if err := s.DeleteObject(ctx, model.DefaultTenant, host.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := s.GetObject(ctx, model.DefaultTenant, svc.ID); err != ErrNotFound {
			t.Fatalf("service should cascade, got %v", err)
		}
	})
}

func TestEventsPartitionedQueries(t *testing.T) {
	matrix(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		now := time.Now().UTC()
		lastMonth := now.AddDate(0, -1, 0)
		evs := []*model.Event{
			{ID: model.NewIDAt(lastMonth), TenantID: model.DefaultTenant, TS: lastMonth,
				Type: model.EventStateChange, Severity: model.SevCritical, Payload: json.RawMessage(`{"n":1}`)},
			{ID: model.NewIDAt(now.Add(-time.Hour)), TenantID: model.DefaultTenant, TS: now.Add(-time.Hour),
				Type: model.EventIngress, Severity: model.SevWarning, Payload: json.RawMessage(`{"n":2}`)},
			{ID: model.NewIDAt(now), TenantID: model.DefaultTenant, TS: now,
				Type: model.EventStateChange, Severity: model.SevOK, Payload: json.RawMessage(`{"n":3}`)},
		}
		if err := s.InsertEvents(ctx, evs); err != nil {
			t.Fatalf("insert: %v", err)
		}
		// newest-first across partitions
		got, err := s.QueryEvents(ctx, EventFilter{TenantID: model.DefaultTenant, Limit: 10})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(got) != 3 || got[0].TS.Before(got[1].TS) {
			t.Fatalf("want 3 desc, got %d", len(got))
		}
		// type filter
		got, err = s.QueryEvents(ctx, EventFilter{TenantID: model.DefaultTenant,
			Types: []string{string(model.EventIngress)}, Limit: 10})
		if err != nil || len(got) != 1 {
			t.Fatalf("type filter: %v n=%d", err, len(got))
		}
		// time range hitting only the old partition
		got, err = s.QueryEvents(ctx, EventFilter{TenantID: model.DefaultTenant,
			From: lastMonth.Add(-time.Hour), To: lastMonth.Add(time.Hour), Limit: 10})
		if err != nil || len(got) != 1 {
			t.Fatalf("range filter: %v n=%d", err, len(got))
		}
		n, err := s.CountEvents(ctx, EventFilter{TenantID: model.DefaultTenant})
		if err != nil || n != 3 {
			t.Fatalf("count: %v n=%d", err, n)
		}
	})
}

func TestAlertDedupAndLifecycle(t *testing.T) {
	matrix(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		a1 := &model.Alert{TenantID: model.DefaultTenant, Severity: model.SevWarning,
			Title: "disk warn", DedupKey: "obj1/disk", Payload: json.RawMessage(`{}`)}
		stored, created, err := s.UpsertAlert(ctx, a1)
		if err != nil || !created {
			t.Fatalf("first upsert: %v created=%v", err, created)
		}
		// same dedup key refreshes, severity escalates
		a2 := &model.Alert{TenantID: model.DefaultTenant, Severity: model.SevCritical,
			Title: "disk crit", DedupKey: "obj1/disk", Payload: json.RawMessage(`{}`)}
		refreshed, created2, err := s.UpsertAlert(ctx, a2)
		if err != nil || created2 {
			t.Fatalf("second upsert: %v created=%v", err, created2)
		}
		if refreshed.ID != stored.ID || refreshed.Severity != model.SevCritical {
			t.Fatalf("dedup merge wrong: %+v", refreshed)
		}

		if _, err := s.AckAlert(ctx, model.DefaultTenant, stored.ID, "sandra"); err != nil {
			t.Fatalf("ack: %v", err)
		}
		if _, err := s.ResolveAlert(ctx, model.DefaultTenant, stored.ID, model.AlertResolved); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		// after resolve, same dedup key opens a fresh alert
		_, created3, err := s.UpsertAlert(ctx, &model.Alert{TenantID: model.DefaultTenant,
			Severity: model.SevWarning, Title: "again", DedupKey: "obj1/disk", Payload: json.RawMessage(`{}`)})
		if err != nil || !created3 {
			t.Fatalf("reopen: %v created=%v", err, created3)
		}
	})
}

func TestAuditChain(t *testing.T) {
	matrix(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		for i := 0; i < 5; i++ {
			_, err := s.AppendAudit(ctx, &model.AuditEntry{
				TenantID: model.DefaultTenant, ActorType: model.ActorUser, ActorID: "u1",
				Action: fmt.Sprintf("test.%d", i), AfterJSON: `{"i":` + fmt.Sprint(i) + `}`,
			})
			if err != nil {
				t.Fatalf("append %d: %v", i, err)
			}
		}
		n, err := s.VerifyAudit(ctx)
		if err != nil || n != 5 {
			t.Fatalf("verify: n=%d err=%v", n, err)
		}
		// tamper → chain must break
		_, err = s.db.ExecContext(ctx, s.Q(`UPDATE audit_log SET action = 'forged' WHERE action = 'test.2'`))
		if err != nil {
			t.Fatalf("tamper: %v", err)
		}
		if _, err := s.VerifyAudit(ctx); err == nil {
			t.Fatal("verify must detect tampering")
		}
	})
}

func TestResourcesAndGenerics(t *testing.T) {
	matrix(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		rule := model.AlertRule{Match: `event.type == "state_change"`, Severity: model.SevCritical}
		env, err := s.PutResource(ctx, model.DefaultTenant, KindAlertRule, "r1", rule, -1)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if env.Version != 1 {
			t.Fatalf("version: %d", env.Version)
		}
		if _, err := s.PutResource(ctx, model.DefaultTenant, KindAlertRule, "r1", rule, -1); err == nil {
			t.Fatal("expected duplicate")
		}
		rule.Severity = model.SevWarning
		env2, err := s.PutResource(ctx, model.DefaultTenant, KindAlertRule, "r1", rule, 1)
		if err != nil || env2.Version != 2 {
			t.Fatalf("update: %v v=%d", err, env2.Version)
		}
		if _, err := s.PutResource(ctx, model.DefaultTenant, KindAlertRule, "r1", rule, 1); err == nil {
			t.Fatal("expected conflict")
		}
		rules, err := LoadAll[model.AlertRule](ctx, s, model.DefaultTenant, KindAlertRule)
		if err != nil || len(rules) != 1 || rules[0].Severity != model.SevWarning {
			t.Fatalf("loadall: %v", err)
		}
		one, err := LoadOne[model.AlertRule](ctx, s, model.DefaultTenant, KindAlertRule, "r1")
		if err != nil || one.Name != "r1" || one.ID == "" {
			t.Fatalf("loadone roundtrip: %v %+v", err, one)
		}
	})
}

func TestCheckStateBatchAndProblems(t *testing.T) {
	matrix(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		host := &model.Object{TenantID: model.DefaultTenant, Kind: model.KindHost,
			Name: "h-problems", Labels: model.Labels{"env": "test"}, Spec: model.ObjectSpec{}}
		if err := s.CreateObject(ctx, host); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		cs := &model.CheckState{ObjectID: host.ID, State: model.HostDown,
			StateType: model.StateHard, Attempt: 3, Output: "CRITICAL - down",
			LastCheck: &now, LastHardChange: &now}
		if err := s.SaveCheckStates(ctx, []*model.CheckState{cs}); err != nil {
			t.Fatalf("save: %v", err)
		}
		probs, err := s.ListProblems(ctx, model.DefaultTenant, false, 10)
		if err != nil || len(probs) != 1 || probs[0].Object.ID != host.ID {
			t.Fatalf("problems: %v n=%d", err, len(probs))
		}
		// acked problems disappear from unhandled view. Ack is written via
		// the column-scoped SetAck (SaveCheckStates no longer owns ack
		// columns, so the pipeline batch can't clobber an API ack).
		if err := s.SetAck(ctx, host.ID, "murat", " on it"); err != nil {
			t.Fatal(err)
		}
		// a subsequent pipeline-style batch save must NOT wipe the ack
		cs.Output = "CRITICAL - still down"
		if err := s.SaveCheckStates(ctx, []*model.CheckState{cs}); err != nil {
			t.Fatal(err)
		}
		probs, _ = s.ListProblems(ctx, model.DefaultTenant, false, 10)
		if len(probs) != 0 {
			t.Fatalf("acked should be handled, n=%d", len(probs))
		}
		sum, err := s.Summary(ctx, model.DefaultTenant)
		if err != nil || sum.HostsDown != 1 || sum.Acked != 1 {
			t.Fatalf("summary: %+v err=%v", sum, err)
		}
	})
}

func TestIdempotency(t *testing.T) {
	matrix(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		body := []byte(`{"a":1}`)
		_, _, found, err := s.IdempotencyCheck(ctx, model.DefaultTenant, "k1", body)
		if err != nil || found {
			t.Fatalf("first check: %v found=%v", err, found)
		}
		if err := s.IdempotencyStore(ctx, model.DefaultTenant, "k1", body, 201, []byte(`{"id":"x"}`)); err != nil {
			t.Fatal(err)
		}
		status, resp, found, err := s.IdempotencyCheck(ctx, model.DefaultTenant, "k1", body)
		if err != nil || !found || status != 201 || string(resp) != `{"id":"x"}` {
			t.Fatalf("replay: %v %d %s", err, status, resp)
		}
		// same key, different body = error
		if _, _, _, err := s.IdempotencyCheck(ctx, model.DefaultTenant, "k1", []byte(`{"a":2}`)); err == nil {
			t.Fatal("expected mismatch error")
		}
	})
}

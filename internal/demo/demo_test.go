package demo

import (
	"context"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// newStore opens a throwaway SQLite store (mirrors storage_test.go's
// SQLite path; the demo seeder is dialect-agnostic).
func newStore(t *testing.T) *storage.Store {
	t.Helper()
	ctx := context.Background()
	s, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir(), RetentionMonths: 12})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// countObjects returns the number of objects whose name has the demo
// prefix (hosts + services), the canonical duplicate probe.
func countObjects(t *testing.T, s *storage.Store) int {
	t.Helper()
	objs, err := s.ListObjects(context.Background(), storage.ObjectFilter{
		TenantID: model.DefaultTenant, Limit: 5000})
	if err != nil {
		t.Fatalf("list objects: %v", err)
	}
	n := 0
	for _, o := range objs {
		if hasPrefix(o.Name, "demo-") {
			n++
		}
	}
	return n
}

func countResources(t *testing.T, s *storage.Store, kind string) int {
	t.Helper()
	envs, err := s.ListResources(context.Background(), model.DefaultTenant, kind, "", "", 2000)
	if err != nil {
		t.Fatalf("list %s: %v", kind, err)
	}
	return len(envs)
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

func TestSeedCreatesEnvironment(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	var createdUsers []string
	opts := Options{
		CreateUser: func(_ context.Context, name, _, _ string, _ []string) error {
			createdUsers = append(createdUsers, name)
			return nil
		},
	}
	sum, err := Seed(ctx, s, opts)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// --- object spec fields land correctly ---
	gw, err := s.GetObjectByName(ctx, model.DefaultTenant, model.KindHost, "", "demo-gateway")
	if err != nil {
		t.Fatalf("get demo-gateway: %v", err)
	}
	if gw.Spec.Interval != model.Duration(15*time.Second) {
		t.Errorf("demo-gateway interval = %s, want 15s", gw.Spec.Interval)
	}
	if gw.Spec.CheckCommand != "builtin:icmp" {
		t.Errorf("demo-gateway checkCommand = %q, want builtin:icmp", gw.Spec.CheckCommand)
	}

	// --- parents set on web/dns/snmp ---
	for _, name := range []string{"demo-web", "demo-dns", "demo-snmp-device"} {
		o, err := s.GetObjectByName(ctx, model.DefaultTenant, model.KindHost, "", name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if len(o.Spec.Parents) != 1 || o.Spec.Parents[0] != "demo-gateway" {
			t.Errorf("%s parents = %v, want [demo-gateway]", name, o.Spec.Parents)
		}
	}

	// --- template intervals ---
	tmpl, err := storage.LoadOne[model.Template](ctx, s, model.DefaultTenant,
		storage.KindTemplate, "demo-host-base")
	if err != nil {
		t.Fatalf("load demo-host-base: %v", err)
	}
	if tmpl.Spec.MaxCheckAttempts != 2 || tmpl.Spec.RetryInterval != model.Duration(10*time.Second) {
		t.Errorf("demo-host-base spec = %+v, want maxAttempts 2 / retry 10s", tmpl.Spec)
	}

	// --- passive service + heartbeat ---
	svc, err := s.GetObjectByName(ctx, model.DefaultTenant, model.KindService, gw.ID, "demo-batchjob")
	if err != nil {
		t.Fatalf("get demo-batchjob: %v", err)
	}
	if svc.Spec.CheckCommand != "passive" || svc.Spec.StalenessAfter != model.Duration(10*time.Minute) {
		t.Errorf("demo-batchjob spec = %+v, want passive / staleness 10m", svc.Spec)
	}
	if _, err := s.GetHeartbeat(ctx, model.DefaultTenant, "demo-cron"); err != nil {
		t.Fatalf("heartbeat demo-cron missing: %v", err)
	}

	// --- alert rule compiles to the expected CEL & links escalation ---
	rule, err := storage.LoadOne[model.AlertRule](ctx, s, model.DefaultTenant,
		storage.KindAlertRule, "demo-critical")
	if err != nil {
		t.Fatalf("load demo-critical: %v", err)
	}
	if rule.EscalationPolicy != "demo-escalation" || rule.GroupID != "demo-storm" {
		t.Errorf("demo-critical links = policy %q group %q", rule.EscalationPolicy, rule.GroupID)
	}
	if rule.Match == "" {
		t.Errorf("demo-critical has no match expression")
	}

	// --- escalation policy has its two steps ---
	pol, err := storage.LoadOne[model.EscalationPolicy](ctx, s, model.DefaultTenant,
		storage.KindEscalationPolicy, "demo-escalation")
	if err != nil {
		t.Fatalf("load demo-escalation: %v", err)
	}
	if len(pol.Steps) != 2 {
		t.Errorf("demo-escalation steps = %d, want 2", len(pol.Steps))
	}

	// --- contact group references both contacts by their stable IDs ---
	grp, err := storage.LoadOne[model.ContactGroup](ctx, s, model.DefaultTenant,
		storage.KindContactGroup, "demo-ops")
	if err != nil {
		t.Fatalf("load demo-ops: %v", err)
	}
	if len(grp.Members) != 2 {
		t.Errorf("demo-ops members = %d, want 2", len(grp.Members))
	}
	alice, err := storage.LoadOne[model.Contact](ctx, s, model.DefaultTenant,
		storage.KindContact, "demo-alice")
	if err != nil {
		t.Fatalf("load demo-alice: %v", err)
	}
	if grp.Members[0] != alice.ID && grp.Members[1] != alice.ID {
		t.Errorf("demo-ops does not reference demo-alice's id %s: %v", alice.ID, grp.Members)
	}

	// --- business-service tree: 3 leaves point at the root by ID ---
	root, err := storage.LoadOne[model.BusinessService](ctx, s, model.DefaultTenant,
		storage.KindBusinessService, "demo-webshop")
	if err != nil {
		t.Fatalf("load demo-webshop: %v", err)
	}
	if root.SLATarget != 99.9 || root.SLAWindow != "month" {
		t.Errorf("demo-webshop SLA = %v/%q, want 99.9/month", root.SLATarget, root.SLAWindow)
	}
	for _, leaf := range []string{"demo-webshop-web", "demo-webshop-dns", "demo-webshop-gateway"} {
		bs, err := storage.LoadOne[model.BusinessService](ctx, s, model.DefaultTenant,
			storage.KindBusinessService, leaf)
		if err != nil {
			t.Fatalf("load %s: %v", leaf, err)
		}
		if bs.ParentID != root.ID {
			t.Errorf("%s parentId = %q, want root id %q", leaf, bs.ParentID, root.ID)
		}
	}

	// --- schedule rotation participants are the contact IDs ---
	sch, err := storage.LoadOne[model.Schedule](ctx, s, model.DefaultTenant,
		storage.KindSchedule, "demo-oncall")
	if err != nil {
		t.Fatalf("load demo-oncall: %v", err)
	}
	if len(sch.Layers) != 1 || len(sch.Layers[0].Participants) != 2 {
		t.Fatalf("demo-oncall rotation malformed: %+v", sch.Layers)
	}
	if sch.TimeZone != "Europe/Vienna" {
		t.Errorf("demo-oncall tz = %q, want Europe/Vienna", sch.TimeZone)
	}

	// --- event sources: traps enabled, imap disabled ---
	traps, err := storage.LoadOne[model.EventSource](ctx, s, model.DefaultTenant,
		storage.KindEventSource, "demo-traps")
	if err != nil {
		t.Fatalf("load demo-traps: %v", err)
	}
	if !traps.Enabled || traps.Config["listen"] == "" {
		t.Errorf("demo-traps should be enabled with a listen addr: %+v", traps)
	}
	imap, err := storage.LoadOne[model.EventSource](ctx, s, model.DefaultTenant,
		storage.KindEventSource, "demo-imap")
	if err != nil {
		t.Fatalf("load demo-imap: %v", err)
	}
	if imap.Enabled {
		t.Errorf("demo-imap should be disabled")
	}

	// --- recurring downtime on demo-batchjob ---
	dts, err := s.ListDowntimes(ctx, model.DefaultTenant, false)
	if err != nil {
		t.Fatalf("list downtimes: %v", err)
	}
	if len(dts) != 1 || dts[0].RRule == "" || dts[0].ObjectID != svc.ID {
		t.Errorf("downtime = %+v, want 1 recurring on demo-batchjob", dts)
	}

	// --- users created via callback ---
	if len(createdUsers) != 2 {
		t.Errorf("CreateUser called %d times, want 2", len(createdUsers))
	}
	if len(sum.Users) != 2 {
		t.Errorf("summary users = %d, want 2", len(sum.Users))
	}

	// --- summary counts are populated ---
	for _, kind := range []string{"host", "service", storage.KindTemplate,
		storage.KindContact, storage.KindChannel, storage.KindAlertRule,
		storage.KindEscalationPolicy, storage.KindSchedule,
		storage.KindBusinessService, storage.KindDashboard, storage.KindReport,
		storage.KindEventSource, "heartbeat", "downtime", "user"} {
		if sum.Counts[kind] == 0 {
			t.Errorf("summary count for %q is 0", kind)
		}
	}
}

func TestSeedIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	var userCalls int
	opts := Options{
		CreateUser: func(_ context.Context, _, _, _ string, _ []string) error {
			userCalls++
			// Simulate the real store: second creation returns duplicate.
			if userCalls > 2 {
				return storage.ErrDuplicate
			}
			return nil
		},
	}

	if _, err := Seed(ctx, s, opts); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	objs1 := countObjects(t, s)
	rules1 := countResources(t, s, storage.KindAlertRule)
	bs1 := countResources(t, s, storage.KindBusinessService)
	es1 := countResources(t, s, storage.KindEventSource)

	// capture a version to prove update-in-place (version increments).
	envBefore, err := s.GetResource(ctx, model.DefaultTenant, storage.KindAlertRule, "demo-critical")
	if err != nil {
		t.Fatalf("get demo-critical: %v", err)
	}

	if _, err := Seed(ctx, s, opts); err != nil {
		t.Fatalf("second seed: %v", err)
	}

	if got := countObjects(t, s); got != objs1 {
		t.Errorf("object count after re-seed = %d, want %d (no dupes)", got, objs1)
	}
	if got := countResources(t, s, storage.KindAlertRule); got != rules1 {
		t.Errorf("alert-rule count after re-seed = %d, want %d", got, rules1)
	}
	if got := countResources(t, s, storage.KindBusinessService); got != bs1 {
		t.Errorf("business-service count after re-seed = %d, want %d", got, bs1)
	}
	if got := countResources(t, s, storage.KindEventSource); got != es1 {
		t.Errorf("event-source count after re-seed = %d, want %d", got, es1)
	}

	// downtime must not duplicate either.
	dts, err := s.ListDowntimes(ctx, model.DefaultTenant, false)
	if err != nil {
		t.Fatalf("list downtimes: %v", err)
	}
	if len(dts) != 1 {
		t.Errorf("downtime count after re-seed = %d, want 1", len(dts))
	}

	// update-in-place: the resource version advanced (upsert), id stable.
	envAfter, err := s.GetResource(ctx, model.DefaultTenant, storage.KindAlertRule, "demo-critical")
	if err != nil {
		t.Fatalf("get demo-critical (2): %v", err)
	}
	if envAfter.ID != envBefore.ID {
		t.Errorf("demo-critical id changed across re-seed: %s -> %s", envBefore.ID, envAfter.ID)
	}
	if envAfter.Version <= envBefore.Version {
		t.Errorf("demo-critical version did not advance: %d -> %d", envBefore.Version, envAfter.Version)
	}

	// idempotent users: duplicate on second run is swallowed, no error.
	if userCalls != 4 {
		t.Errorf("CreateUser total calls = %d, want 4 (2 per seed)", userCalls)
	}
}

func TestSeedWithoutUserCallback(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sum, err := Seed(ctx, s, Options{}) // no CreateUser
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(sum.Users) != 0 || sum.Counts["user"] != 0 {
		t.Errorf("expected no users seeded without callback, got %+v", sum.Users)
	}
	// the rest of the environment must still be present.
	if _, err := s.GetObjectByName(ctx, model.DefaultTenant, model.KindHost, "", "demo-gateway"); err != nil {
		t.Errorf("demo-gateway not seeded: %v", err)
	}
}

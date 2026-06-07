// Package demo seeds a self-contained demonstration environment that
// showcases the whole product with REAL working checks: a mix of builtin
// ICMP/HTTPS/DNS/SNMP/TLS probes, a passive job with a heartbeat, the full
// notification/escalation/on-call stack, a BPI tree with an SLA, a
// dashboard, a scheduled report, ingress event-sources, a recurring
// downtime, and (optionally) two local users.
//
// Seed is idempotent: every artefact is named with the "demo-" prefix and
// carries the label demo=true, and re-running updates in place instead of
// creating duplicates. It seeds against the default tenant
// (model.DefaultTenant). It NEVER probes the network — it only writes
// configuration; the scheduler/executor run the checks once live.
//
// The CLI flag (`northplaned serve --demo`), the CreateUser callback and
// the mock SMTP/webhook sinks are wired by the orchestrator; this package
// only builds the seeder.
package demo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// tenant is the bootstrap tenant every demo artefact lands in.
const tenant = model.DefaultTenant

// demoLabel marks every seeded object/resource so a future teardown (or a
// selector-scoped prune) can find exactly what the demo created.
var demoLabel = model.Labels{"demo": "true"}

// Options configures the seeder. Zero value is valid: the defaults below
// target a loopback SNMP agent, a loopback trap listener, and a public
// HTTPS endpoint, so the demo works on a single host with no external
// dependencies (checks that can't reach their target simply report
// UNKNOWN/CRITICAL once — see Summary.Hints).
type Options struct {
	// SNMPTarget is the host:port of an SNMP agent for the snmp/snmp-walk
	// demo checks. Default "127.0.0.1:161", community "public".
	SNMPTarget string
	// TrapListen is the listen URL for the snmp-trap event-source.
	// Default "udp://:9162".
	TrapListen string
	// HTTPTarget is the URL the demo-web host probes via builtin:https.
	// Default "https://example.org".
	HTTPTarget string

	// CreateUser, when non-nil, provisions the two demo users. The
	// orchestrator wires this to a storage+auth-backed implementation
	// (storage.CreateUser + auth.HashSecret); when nil, user seeding is
	// skipped and the returned credentials list is empty. Signature:
	// (ctx, name, email, password string, roles []string) error.
	CreateUser func(ctx context.Context, name, email, password string, roles []string) error

	// Log receives progress; defaults to slog.Default().
	Log *slog.Logger
}

func (o *Options) defaults() {
	if o.SNMPTarget == "" {
		o.SNMPTarget = "127.0.0.1:161"
	}
	if o.TrapListen == "" {
		o.TrapListen = "udp://:9162"
	}
	if o.HTTPTarget == "" {
		o.HTTPTarget = "https://example.org"
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
}

// Credential is a created demo login (returned so the operator can print
// them after `serve --demo`).
type Credential struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// Summary reports what Seed did: a count per kind, the credentials of any
// users it created, and operational hints (e.g. unreachable SNMP target).
type Summary struct {
	// Counts is keyed by artefact kind ("host", "service", "template",
	// "contact", "channel", "alert-rule", "schedule", "business-service",
	// "dashboard", "report", "event-source", "heartbeat", "downtime",
	// "contact-group", "escalation-policy", "alert-group", "user").
	Counts map[string]int `json:"counts"`
	// Users holds the credentials of users created this run (empty when
	// Options.CreateUser is nil or the users already existed).
	Users []Credential `json:"users,omitempty"`
	// Hints are non-fatal operator notes.
	Hints []string `json:"hints,omitempty"`
}

func (s *Summary) bump(kind string) { s.Counts[kind]++ }

// stableID derives a deterministic UUID-shaped id from a name so that
// re-running Seed keeps the same surrogate id (idempotent cross-references
// such as BusinessService.ParentID stay valid). It is NOT a UUIDv7 but is
// accepted everywhere a stored doc id is read back verbatim.
func stableID(name string) string {
	h := sha256.Sum256([]byte("northplane-demo:" + name))
	b := h[:16]
	b[6] = (b[6] & 0x0f) | 0x70 // version 7 nibble (cosmetic; round-trips as-is)
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	var dst [36]byte
	hex.Encode(dst[:8], b[:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:], b[10:])
	return string(dst[:])
}

// Seed writes (idempotently) the full demo environment into store under the
// default tenant. It returns a Summary and the first error encountered.
func Seed(ctx context.Context, store *storage.Store, opts Options) (Summary, error) {
	opts.defaults()
	sum := Summary{Counts: map[string]int{}}
	s := &seeder{store: store, opts: opts, sum: &sum}

	steps := []func(context.Context) error{
		s.templates,
		s.hosts, // hosts + their services (objects)
		s.passiveAndHeartbeat,
		s.contacts,
		s.channels,
		s.alerting, // alert-groups, alert-rules, escalation policies
		s.schedule,
		s.businessServices,
		s.dashboard,
		s.report,
		s.eventSources,
		s.downtime,
		s.users,
	}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			return sum, err
		}
	}
	opts.Log.Info("demo seed complete", "counts", sum.Counts, "users", len(sum.Users))
	return sum, nil
}

// seeder carries the shared state through the seed steps.
type seeder struct {
	store *storage.Store
	opts  Options
	sum   *Summary
}

// putResource upserts a resource document unconditionally (idempotent:
// second run updates in place, no duplicates) and bumps the kind counter.
func (s *seeder) putResource(ctx context.Context, kind, name string, doc any) error {
	if _, err := s.store.PutResource(ctx, tenant, kind, name, doc, 0); err != nil {
		return fmt.Errorf("demo: put %s %q: %w", kind, name, err)
	}
	s.sum.bump(kind)
	return nil
}

// putObject creates or updates a host/service. Idempotent by (kind,
// hostID, name): an existing object is updated in place (spec/labels/
// folder), a missing one is created. Returns the resulting object id.
func (s *seeder) putObject(ctx context.Context, o *model.Object) (string, error) {
	o.TenantID = tenant
	if o.Labels == nil {
		o.Labels = model.Labels{}
	}
	for k, v := range demoLabel {
		o.Labels[k] = v
	}
	existing, err := s.store.GetObjectByName(ctx, tenant, o.Kind, o.HostID, o.Name)
	switch err {
	case nil:
		existing.Folder = o.Folder
		existing.Labels = o.Labels
		existing.Spec = o.Spec
		if err := s.store.UpdateObject(ctx, existing, 0); err != nil {
			return "", fmt.Errorf("demo: update %s %q: %w", o.Kind, o.Name, err)
		}
		s.sum.bump(string(o.Kind))
		return existing.ID, nil
	case storage.ErrNotFound:
		if err := s.store.CreateObject(ctx, o); err != nil {
			return "", fmt.Errorf("demo: create %s %q: %w", o.Kind, o.Name, err)
		}
		s.sum.bump(string(o.Kind))
		return o.ID, nil
	default:
		return "", err
	}
}

// --- templates -----------------------------------------------------------

func (s *seeder) templates(ctx context.Context) error {
	// demo-host-base: brisk interval, fast retry, only 2 attempts before
	// hard — demonstrates check-attempt/interval management.
	base := model.Template{
		Kind:   model.TemplateHost,
		Name:   "demo-host-base",
		Labels: demoLabel.Clone(),
		Spec: model.ObjectSpec{
			Interval:         model.Duration(30 * time.Second),
			RetryInterval:    model.Duration(10 * time.Second),
			MaxCheckAttempts: 2,
			Timeout:          model.Duration(10 * time.Second),
		},
	}
	if err := s.putResource(ctx, storage.KindTemplate, base.Name, &base); err != nil {
		return err
	}
	// demo-web-service: slower interval, generous timeout for web probes.
	web := model.Template{
		Kind:   model.TemplateService,
		Name:   "demo-web-service",
		Labels: demoLabel.Clone(),
		Spec: model.ObjectSpec{
			Interval: model.Duration(60 * time.Second),
			Timeout:  model.Duration(10 * time.Second),
		},
	}
	return s.putResource(ctx, storage.KindTemplate, web.Name, &web)
}

// --- hosts & services ----------------------------------------------------

func (s *seeder) hosts(ctx context.Context) error {
	// demo-gateway: the reachability root. builtin:icmp against loopback,
	// brisk 15s interval.
	gwID, err := s.putObject(ctx, &model.Object{
		Kind:   model.KindHost,
		Name:   "demo-gateway",
		Folder: "/demo",
		Labels: model.Labels{"role": "gateway", "site": "demo"},
		Spec: model.ObjectSpec{
			Address:      "127.0.0.1",
			CheckCommand: "builtin:icmp",
			Interval:     model.Duration(15 * time.Second),
		},
	})
	if err != nil {
		return err
	}

	// demo-web: HTTPS via the web template; parent = gateway (reachability).
	webID, err := s.putObject(ctx, &model.Object{
		Kind:   model.KindHost,
		Name:   "demo-web",
		Folder: "/demo",
		Labels: model.Labels{"role": "web", "site": "demo"},
		Spec: model.ObjectSpec{
			Address:      s.opts.HTTPTarget,
			Templates:    []string{"demo-host-base"},
			Parents:      []string{"demo-gateway"},
			CheckCommand: "builtin:https",
			Args:         []string{"-u", s.opts.HTTPTarget},
		},
	})
	if err != nil {
		return err
	}

	// demo-dns: resolve example.org; parent = gateway.
	if _, err := s.putObject(ctx, &model.Object{
		Kind:   model.KindHost,
		Name:   "demo-dns",
		Folder: "/demo",
		Labels: model.Labels{"role": "dns", "site": "demo"},
		Spec: model.ObjectSpec{
			Address:      "127.0.0.1",
			Parents:      []string{"demo-gateway"},
			CheckCommand: "builtin:dns",
			Args:         []string{"-H", "example.org"},
		},
	}); err != nil {
		return err
	}

	// demo-snmp-device: sysUpTime via builtin:snmp; parent = gateway; 30s.
	snmpID, err := s.putObject(ctx, &model.Object{
		Kind:   model.KindHost,
		Name:   "demo-snmp-device",
		Folder: "/demo",
		Labels: model.Labels{"role": "snmp", "site": "demo"},
		Spec: model.ObjectSpec{
			Address:      s.opts.SNMPTarget,
			Parents:      []string{"demo-gateway"},
			CheckCommand: "builtin:snmp",
			Args:         []string{"-o", "1.3.6.1.2.1.1.3.0", "-C", "public"}, // sysUpTime.0
			Interval:     model.Duration(30 * time.Second),
		},
	})
	if err != nil {
		return err
	}
	_ = webID

	// Services on demo-snmp-device.
	if _, err := s.putObject(ctx, &model.Object{
		Kind:   model.KindService,
		Name:   "demo-snmp-ifwalk",
		HostID: snmpID,
		Folder: "/demo",
		Labels: model.Labels{"kind": "snmp-walk"},
		Spec: model.ObjectSpec{
			CheckCommand: "builtin:snmp-walk",
			Args:         []string{"-o", "1.3.6.1.2.1.2.2.1.8", "-C", "public"}, // ifOperStatus
			Interval:     model.Duration(60 * time.Second),
		},
	}); err != nil {
		return err
	}
	if _, err := s.putObject(ctx, &model.Object{
		Kind:   model.KindService,
		Name:   "demo-tls",
		HostID: snmpID,
		Folder: "/demo",
		Labels: model.Labels{"kind": "tls-cert"},
		Spec: model.ObjectSpec{
			CheckCommand: "builtin:tls-cert",
			Args:         []string{"-H", "example.org", "-p", "443", "-w", "21", "-c", "7"},
			Interval:     model.Duration(6 * time.Hour),
		},
	}); err != nil {
		return err
	}

	// demo-web-latency service on demo-web: HTTPS with perfdata (time=…s).
	if _, err := s.putObject(ctx, &model.Object{
		Kind:   model.KindService,
		Name:   "demo-web-latency",
		HostID: webID,
		Folder: "/demo",
		Labels: model.Labels{"kind": "https"},
		Spec: model.ObjectSpec{
			Templates:    []string{"demo-web-service"},
			CheckCommand: "builtin:https",
			Args:         []string{"-u", s.opts.HTTPTarget, "-w", "1.0", "-c", "3.0"},
			Interval:     model.Duration(30 * time.Second),
		},
	}); err != nil {
		return err
	}

	_ = gwID
	return nil
}

// passiveAndHeartbeat seeds the passive batch-job service and its
// dead-man heartbeat.
func (s *seeder) passiveAndHeartbeat(ctx context.Context) error {
	// demo-batchjob: passive service on demo-gateway, marked stale after
	// 10m without a submitted result. Lives under the gateway host.
	gw, err := s.store.GetObjectByName(ctx, tenant, model.KindHost, "", "demo-gateway")
	if err != nil {
		return fmt.Errorf("demo: locate demo-gateway: %w", err)
	}
	if _, err := s.putObject(ctx, &model.Object{
		Kind:   model.KindService,
		Name:   "demo-batchjob",
		HostID: gw.ID,
		Folder: "/demo",
		Labels: model.Labels{"kind": "passive"},
		Spec: model.ObjectSpec{
			CheckCommand:   "passive",
			StalenessAfter: model.Duration(10 * time.Minute),
			StalenessText:  "no batch-job result submitted in 10m",
			// Direct object routing showcase (Nagios contact_groups):
			// hard CRITICAL/recovery notifies demo-ops without any rule.
			ContactGroups: []string{"demo-ops"},
			NotifyOn:      []string{"critical", "recovery"},
		},
	}); err != nil {
		return err
	}

	// demo-cron heartbeat: a beat is expected every 5m (dead-man source).
	hb := &model.Heartbeat{
		ID:          stableID("hb:demo-cron"),
		TenantID:    tenant,
		Name:        "demo-cron",
		ExpectEvery: model.Duration(5 * time.Minute),
		Grace:       model.Duration(1 * time.Minute),
		Severity:    model.SevWarning,
		Labels:      demoLabel.Clone(),
	}
	if err := s.store.PutHeartbeat(ctx, hb); err != nil {
		return fmt.Errorf("demo: put heartbeat demo-cron: %w", err)
	}
	s.sum.bump("heartbeat")
	s.sum.Hints = append(s.sum.Hints,
		"passive service demo-batchjob & heartbeat demo-cron have no live feeder — "+
			"submit a result to /checks/results and POST /event-sources/demo-hook-in or beat demo-cron to see them go OK")
	return nil
}

// --- contacts & groups ---------------------------------------------------

func (s *seeder) contacts(ctx context.Context) error {
	alice := model.Contact{
		ID:       stableID("contact:demo-alice"),
		Name:     "demo-alice",
		Email:    "alice@demo.local",
		TimeZone: "Europe/Vienna",
		Preferences: []model.ChannelPreference{{
			Profile:  "default",
			Channels: []model.ChannelType{model.ChannelEmail},
		}},
	}
	bob := model.Contact{
		ID:       stableID("contact:demo-bob"),
		Name:     "demo-bob",
		Email:    "bob@demo.local",
		TimeZone: "Europe/Vienna",
		Preferences: []model.ChannelPreference{{
			Profile:  "default",
			Channels: []model.ChannelType{model.ChannelWebhook, model.ChannelEmail},
		}},
	}
	if err := s.putResource(ctx, storage.KindContact, alice.Name, &alice); err != nil {
		return err
	}
	if err := s.putResource(ctx, storage.KindContact, bob.Name, &bob); err != nil {
		return err
	}
	// demo-ops group: both contacts, referenced by stable contact IDs.
	grp := model.ContactGroup{
		ID:      stableID("group:demo-ops"),
		Name:    "demo-ops",
		Members: []string{alice.ID, bob.ID},
	}
	return s.putResource(ctx, storage.KindContactGroup, grp.Name, &grp)
}

// --- channels ------------------------------------------------------------

func (s *seeder) channels(ctx context.Context) error {
	email := model.NotificationChannel{
		ID:      stableID("channel:demo-email"),
		Name:    "demo-email",
		Type:    model.ChannelEmail,
		Enabled: true,
		Config: map[string]string{
			"host":           "127.0.0.1",
			"port":           "2525",
			"from":           "northplane@demo.local",
			"allowPlaintext": "true", // mock sink offers no STARTTLS
		},
	}
	hook := model.NotificationChannel{
		ID:      stableID("channel:demo-hook"),
		Name:    "demo-hook",
		Type:    model.ChannelWebhook,
		Enabled: true,
		Config: map[string]string{
			"url": "http://127.0.0.1:18081/hook",
		},
	}
	if err := s.putResource(ctx, storage.KindChannel, email.Name, &email); err != nil {
		return err
	}
	if err := s.putResource(ctx, storage.KindChannel, hook.Name, &hook); err != nil {
		return err
	}
	s.sum.Hints = append(s.sum.Hints,
		"channel demo-email points at a mock SMTP sink on 127.0.0.1:2525 and demo-hook at "+
			"http://127.0.0.1:18081/hook — start those sinks (orchestrator) to see deliveries")
	return nil
}

// --- alert groups, rules & escalation ------------------------------------

func (s *seeder) alerting(ctx context.Context) error {
	// demo-storm: group bursts of alerts by host.
	grp := model.AlertGroup{
		ID:       stableID("alertgroup:demo-storm"),
		Name:     "demo-storm",
		GroupBy:  []string{"host"},
		Window:   model.Duration(5 * time.Minute),
		MinCount: 3,
	}
	if err := s.putResource(ctx, storage.KindAlertGroup, grp.Name, &grp); err != nil {
		return err
	}

	// demo-escalation: step 0 → demo-ops via email; after 15m unless acked
	// → demo-bob via webhook.
	pol := model.EscalationPolicy{
		ID:   stableID("escpolicy:demo-escalation"),
		Name: "demo-escalation",
		Steps: []model.EscalationStep{
			{
				After:    0,
				Notify:   &model.EscalationTarget{ContactGroup: "demo-ops"},
				Channels: []model.ChannelType{model.ChannelEmail},
			},
			{
				After:       model.Duration(15 * time.Minute),
				UnlessAcked: true,
				Notify:      &model.EscalationTarget{Contact: "demo-bob"},
				Channels:    []model.ChannelType{model.ChannelWebhook},
			},
		},
	}
	if err := s.putResource(ctx, storage.KindEscalationPolicy, pol.Name, &pol); err != nil {
		return err
	}

	// demo-critical: fire on hard state_change into CRITICAL/DOWN, route
	// through demo-escalation, group under demo-storm.
	rule := model.AlertRule{
		ID:   stableID("alertrule:demo-critical"),
		Name: "demo-critical",
		Match: `event.type == "state_change" && event.stateType == "hard" && ` +
			`(event.state == "CRITICAL" || event.state == "DOWN")`,
		Severity:         model.SevCritical,
		Title:            `demo: {{ .event.object }} is {{ .event.state }}`,
		PendingFor:       0,
		EscalationPolicy: "demo-escalation",
		GroupID:          "demo-storm",
		SetLabels:        model.Labels{"demo": "true"},
	}
	if err := s.putResource(ctx, storage.KindAlertRule, rule.Name, &rule); err != nil {
		return err
	}

	// demo-heartbeat-rule: heartbeat-style rule mirroring the demo-cron
	// heartbeat — alerts when the "demo-cron" source is silent > 5m.
	hbRule := model.AlertRule{
		ID:   stableID("alertrule:demo-heartbeat-rule"),
		Name: "demo-heartbeat-rule",
		Heartbeat: &model.RuleHeartbeat{
			Source:      "demo-cron",
			ExpectEvery: model.Duration(5 * time.Minute),
		},
		Severity: model.SevWarning,
		Title:    "demo: cron heartbeat missing",
	}
	return s.putResource(ctx, storage.KindAlertRule, hbRule.Name, &hbRule)
}

// --- on-call schedule ----------------------------------------------------

func (s *seeder) schedule(ctx context.Context) error {
	// Anchor at a fixed historic Monday 08:00 Vienna so the rotation is
	// deterministic across runs (idempotent doc).
	loc, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		loc = time.UTC
	}
	anchor := time.Date(2026, 1, 5, 8, 0, 0, 0, loc) // Mon 2026-01-05 08:00
	sch := model.Schedule{
		ID:       stableID("schedule:demo-oncall"),
		Name:     "demo-oncall",
		TimeZone: "Europe/Vienna",
		Layers: []model.Rotation{{
			Name:         "primary",
			Participants: []string{stableID("contact:demo-alice"), stableID("contact:demo-bob")},
			Unit:         model.RotateWeekly,
			Anchor:       anchor,
		}},
	}
	return s.putResource(ctx, storage.KindSchedule, sch.Name, &sch)
}

// --- business service (BPI tree + SLA) -----------------------------------

func (s *seeder) businessServices(ctx context.Context) error {
	// Root: demo-webshop, worst-of its children, SLA 99.9% monthly.
	rootID := stableID("bs:demo-webshop")
	root := model.BusinessService{
		ID:        rootID,
		Name:      "demo-webshop",
		Rule:      model.BSWorst,
		SLATarget: 99.9,
		SLAWindow: "month",
	}
	if err := s.putResource(ctx, storage.KindBusinessService, root.Name, &root); err != nil {
		return err
	}
	// Leaves bind to the demo hosts by label selector (web AND dns AND
	// gateway). Each is a child of demo-webshop, so the root is healthy
	// only when all three are.
	leaves := []struct {
		name, selector string
	}{
		{"demo-webshop-web", "role=web,demo=true"},
		{"demo-webshop-dns", "role=dns,demo=true"},
		{"demo-webshop-gateway", "role=gateway,demo=true"},
	}
	for _, l := range leaves {
		leaf := model.BusinessService{
			ID:       stableID("bs:" + l.name),
			Name:     l.name,
			ParentID: rootID,
			Selector: l.selector,
		}
		if err := s.putResource(ctx, storage.KindBusinessService, leaf.Name, &leaf); err != nil {
			return err
		}
	}
	return nil
}

// --- dashboard -----------------------------------------------------------

// dashboardSpec is the free-form widget document for demo-overview. The
// KindDashboard CRUD is schema-light (the layout JSON is owned by the
// frontend), so this defines a concrete, documented widget vocabulary the
// UI team builds against — see the package final message / README for the
// schema.
func dashboardSpec(httpLatencyMetric string) map[string]any {
	return map[string]any{
		"schemaVersion": 1,
		"title":         "Demo Overview",
		"widgets": []map[string]any{
			{"type": "counters", "title": "State summary"},
			{"type": "problems", "title": "Open problems", "limit": 10},
			{
				"type":   "metric",
				"title":  "Web latency",
				"object": "demo-web-latency",
				"metric": httpLatencyMetric, // builtin:https emits "time" (seconds)
				"range":  "3h",
			},
			{"type": "bpi", "title": "Webshop health", "service": "demo-webshop"},
			{"type": "table", "title": "Demo-Inventar", "selector": "demo=true", "limit": 15, "w": 12, "h": 2},
		},
	}
}

func (s *seeder) dashboard(ctx context.Context) error {
	dash := model.Dashboard{
		ID:     stableID("dashboard:demo-overview"),
		Name:   "demo-overview",
		Shared: true,
	}
	// Dashboard.Spec is json.RawMessage; embed the widget doc.
	doc := map[string]any{
		"id":     dash.ID,
		"name":   dash.Name,
		"shared": true,
		"spec":   dashboardSpec("time"),
		"labels": demoLabel,
	}
	return s.putResource(ctx, storage.KindDashboard, dash.Name, doc)
}

// --- report --------------------------------------------------------------

func (s *seeder) report(ctx context.Context) error {
	// Availability report over the demo hosts, daily at 07:00, mailed to
	// alice, keep last 7.
	params := map[string]any{
		"selector": "demo=true",
		"window":   "30d",
		"folder":   "/demo",
	}
	doc := map[string]any{
		"id":       stableID("report:demo-availability"),
		"name":     "demo-availability",
		"type":     string(model.ReportAvailability),
		"params":   params,
		"schedule": "daily@07:00",
		"email":    []string{"alice@demo.local"},
		"keep":     7,
		"labels":   demoLabel,
	}
	return s.putResource(ctx, storage.KindReport, "demo-availability", doc)
}

// --- event sources -------------------------------------------------------

func (s *seeder) eventSources(ctx context.Context) error {
	// demo-hook-in: inbound webhook, token auth. The token itself is a
	// secret the orchestrator must create (secretRef below) — we only
	// reference it; no secret is invented here.
	hookIn := model.EventSource{
		ID:        stableID("eventsource:demo-hook-in"),
		Name:      "demo-hook-in",
		Type:      "webhook",
		Enabled:   true,
		AuthMode:  "token",
		SecretRef: "demo-hook-in-token",
		Labels:    demoLabel.Clone(),
	}
	if err := s.putResource(ctx, storage.KindEventSource, hookIn.Name, &hookIn); err != nil {
		return err
	}
	s.sum.Hints = append(s.sum.Hints,
		"event-source demo-hook-in uses authMode=token with secretRef \"demo-hook-in-token\" — "+
			"the orchestrator must store that secret (PutSecret) for inbound auth to pass")

	// demo-traps: SNMP-trap listener, enabled.
	traps := model.EventSource{
		ID:      stableID("eventsource:demo-traps"),
		Name:    "demo-traps",
		Type:    "snmp-trap",
		Enabled: true,
		Config: map[string]string{
			"listen":    s.opts.TrapListen,
			"community": "public",
			"severity":  "warning",
		},
		Labels: demoLabel.Clone(),
	}
	if err := s.putResource(ctx, storage.KindEventSource, traps.Name, &traps); err != nil {
		return err
	}

	// demo-imap: IMAP poller, DISABLED (so it doesn't try to connect),
	// references a password secret the orchestrator may create.
	imap := model.EventSource{
		ID:      stableID("eventsource:demo-imap"),
		Name:    "demo-imap",
		Type:    "imap",
		Enabled: false,
		Config: map[string]string{
			"host":              "127.0.0.1",
			"port":              "3143",
			"tls":               "off",
			"username":          "demo",
			"passwordSecretRef": "demo-imap-pass",
			"folder":            "INBOX",
			"pollInterval":      "30s",
		},
		Labels: demoLabel.Clone(),
	}
	return s.putResource(ctx, storage.KindEventSource, imap.Name, &imap)
}

// --- downtime ------------------------------------------------------------

func (s *seeder) downtime(ctx context.Context) error {
	// One recurring downtime on demo-batchjob: daily 03:00–04:00. The
	// Downtime model supports RRULE (FREQ=DAILY) — express the recurrence
	// there; Start/End anchor the first window.
	gw, err := s.store.GetObjectByName(ctx, tenant, model.KindHost, "", "demo-gateway")
	if err != nil {
		return fmt.Errorf("demo: locate demo-gateway for downtime: %w", err)
	}
	job, err := s.store.GetObjectByName(ctx, tenant, model.KindService, gw.ID, "demo-batchjob")
	if err != nil {
		return fmt.Errorf("demo: locate demo-batchjob for downtime: %w", err)
	}
	id := stableID("downtime:demo-batchjob-nightly")
	// Idempotent: only insert if this stable id isn't present yet
	// (CreateDowntime always inserts a fresh row otherwise).
	if _, err := s.store.GetDowntime(ctx, tenant, id); err == nil {
		s.sum.bump("downtime")
		return nil
	} else if err != storage.ErrNotFound {
		return err
	}
	loc, lerr := time.LoadLocation("Europe/Vienna")
	if lerr != nil {
		loc = time.UTC
	}
	// Anchor the first window at the next 03:00 local; recurrence repeats it.
	now := time.Now().In(loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, loc)
	if !start.After(now) {
		start = start.AddDate(0, 0, 1)
	}
	dt := &model.Downtime{
		ID:        id,
		TenantID:  tenant,
		ObjectID:  job.ID,
		Type:      model.DowntimeFixed,
		Start:     start.UTC(),
		End:       start.Add(time.Hour).UTC(),
		RRule:     "FREQ=DAILY;BYHOUR=3;BYMINUTE=0",
		Comment:   "demo: nightly batch-job maintenance window",
		CreatedBy: "demo-seed",
	}
	if err := s.store.CreateDowntime(ctx, dt); err != nil {
		return fmt.Errorf("demo: create downtime: %w", err)
	}
	s.sum.bump("downtime")
	return nil
}

// --- users ---------------------------------------------------------------

func (s *seeder) users(ctx context.Context) error {
	if s.opts.CreateUser == nil {
		s.sum.Hints = append(s.sum.Hints,
			"no CreateUser callback wired — demo users (demo-operator, demo-viewer) were NOT created")
		return nil
	}
	creds := []Credential{
		{Name: "demo-operator", Email: "operator@demo.local", Password: "operator-demo-2026!", Role: "operator"},
		{Name: "demo-viewer", Email: "viewer@demo.local", Password: "viewer-demo-2026!", Role: "viewer"},
	}
	for _, c := range creds {
		err := s.opts.CreateUser(ctx, c.Name, c.Email, c.Password, []string{c.Role})
		switch {
		case err == nil:
			s.sum.bump("user")
			s.sum.Users = append(s.sum.Users, c)
		case isDuplicate(err):
			// Idempotent: a previous run already created this user. Report
			// the credentials so the operator can still log in.
			s.sum.Users = append(s.sum.Users, c)
		default:
			return fmt.Errorf("demo: create user %q: %w", c.Name, err)
		}
	}
	return nil
}

// isDuplicate reports whether err signals an already-existing user, so a
// re-seed treats it as success (idempotency). It matches the storage
// duplicate sentinel by its error text to avoid a hard dependency on the
// callback's concrete error type.
func isDuplicate(err error) bool {
	return err == storage.ErrDuplicate ||
		(err != nil && containsAny(err.Error(), "duplicate", "already exists", "UNIQUE"))
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

// indexOf is a tiny strings.Contains helper kept local to avoid pulling
// strings just for one call site.
func indexOf(s, sub string) int {
	n, m := len(s), len(sub)
	for i := 0; i+m <= n; i++ {
		if s[i:i+m] == sub {
			return i
		}
	}
	return -1
}

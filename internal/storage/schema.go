package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// migration is one forward-only step. SQL uses dialect placeholders
// ({{TIMESTAMP}}, {{JSON}}, …) expanded per backend — one logical schema,
// generated DDL (SPEC §6.5).
type migration struct {
	version int
	name    string
	sql     []string
}

var migrations = []migration{
	{1, "core", []string{
		`CREATE TABLE tenants (
			id         TEXT PRIMARY KEY,
			name       TEXT NOT NULL,
			slug       TEXT NOT NULL UNIQUE,
			disabled   {{BOOL}} NOT NULL DEFAULT false,
			version    {{BIGINT}} NOT NULL DEFAULT 1,
			created_at {{TIMESTAMP}} NOT NULL,
			updated_at {{TIMESTAMP}} NOT NULL
		)`,
		`CREATE TABLE users (
			id           TEXT PRIMARY KEY,
			name         TEXT NOT NULL,
			email        TEXT NOT NULL DEFAULT '',
			subject      TEXT,
			local        {{BOOL}} NOT NULL DEFAULT false,
			pass_hash    TEXT NOT NULL DEFAULT '',
			disabled     {{BOOL}} NOT NULL DEFAULT false,
			last_seen_at {{TIMESTAMP}},
			version      {{BIGINT}} NOT NULL DEFAULT 1,
			created_at   {{TIMESTAMP}} NOT NULL,
			updated_at   {{TIMESTAMP}} NOT NULL
		)`,
		`CREATE UNIQUE INDEX users_subject ON users (subject) WHERE subject IS NOT NULL`,
		// Hosts & services unified (SPEC §6.5).
		`CREATE TABLE objects (
			id         TEXT PRIMARY KEY,
			tenant_id  TEXT NOT NULL,
			kind       TEXT NOT NULL CHECK (kind IN ('host','service')),
			name       TEXT NOT NULL,
			host_id    TEXT REFERENCES objects(id) ON DELETE CASCADE,
			folder     TEXT NOT NULL DEFAULT '/',
			labels     {{JSON}} NOT NULL DEFAULT '{}',
			spec       {{JSON}} NOT NULL,
			version    {{BIGINT}} NOT NULL DEFAULT 1,
			created_at {{TIMESTAMP}} NOT NULL,
			updated_at {{TIMESTAMP}} NOT NULL
		)`,
		`CREATE UNIQUE INDEX objects_ident ON objects (tenant_id, kind, COALESCE(host_id,''), name)`,
		`CREATE INDEX objects_host ON objects (host_id) WHERE host_id IS NOT NULL`,
		`CREATE INDEX objects_folder ON objects (tenant_id, folder)`,
		// Selector index (SPEC §6.5).
		`CREATE TABLE object_labels (
			object_id TEXT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
			k TEXT NOT NULL, v TEXT NOT NULL,
			PRIMARY KEY (k, v, object_id)
		)`,
		`CREATE INDEX object_labels_obj ON object_labels (object_id)`,
		// Hot current state (SPEC §6.5).
		`CREATE TABLE check_state (
			object_id      TEXT PRIMARY KEY REFERENCES objects(id) ON DELETE CASCADE,
			state          {{BIGINT}} NOT NULL DEFAULT 0,
			state_type     TEXT NOT NULL DEFAULT 'hard',
			attempt        {{BIGINT}} NOT NULL DEFAULT 1,
			output         TEXT NOT NULL DEFAULT '',
			long_output    TEXT NOT NULL DEFAULT '',
			perfdata       TEXT NOT NULL DEFAULT '',
			latency_ms     {{BIGINT}} NOT NULL DEFAULT 0,
			exec_ms        {{BIGINT}} NOT NULL DEFAULT 0,
			last_check     {{TIMESTAMP}},
			next_check     {{TIMESTAMP}},
			last_hard_change {{TIMESTAMP}},
			last_ok        {{TIMESTAMP}},
			flapping       {{BOOL}} NOT NULL DEFAULT false,
			flap_pct       REAL NOT NULL DEFAULT 0,
			flap_history   {{BIGINT}} NOT NULL DEFAULT 0,
			acked_by       TEXT NOT NULL DEFAULT '',
			ack_comment    TEXT NOT NULL DEFAULT '',
			downtime_depth {{BIGINT}} NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE alerts (
			id          TEXT PRIMARY KEY,
			tenant_id   TEXT NOT NULL,
			rule_id     TEXT,
			object_id   TEXT,
			incident_id TEXT,
			status      TEXT NOT NULL,
			severity    TEXT NOT NULL,
			title       TEXT NOT NULL,
			dedup_key   TEXT,
			labels      {{JSON}} NOT NULL DEFAULT '{}',
			event_ids   {{JSON}} NOT NULL DEFAULT '[]',
			opened_at   {{TIMESTAMP}} NOT NULL,
			acked_at    {{TIMESTAMP}},
			acked_by    TEXT NOT NULL DEFAULT '',
			resolved_at {{TIMESTAMP}},
			payload     {{JSON}} NOT NULL DEFAULT '{}'
		)`,
		`CREATE UNIQUE INDEX alerts_dedup ON alerts (tenant_id, dedup_key)
			WHERE status IN ('open','acked') AND dedup_key IS NOT NULL`,
		`CREATE INDEX alerts_status ON alerts (tenant_id, status, opened_at)`,
		`CREATE INDEX alerts_incident ON alerts (incident_id) WHERE incident_id IS NOT NULL`,
		`CREATE TABLE incidents (
			id          TEXT PRIMARY KEY,
			tenant_id   TEXT NOT NULL,
			status      TEXT NOT NULL,
			severity    TEXT NOT NULL,
			title       TEXT NOT NULL,
			summary     TEXT NOT NULL DEFAULT '',
			impact      TEXT NOT NULL DEFAULT '',
			ticket_url  TEXT NOT NULL DEFAULT '',
			created_by  TEXT NOT NULL,
			opened_at   {{TIMESTAMP}} NOT NULL,
			resolved_at {{TIMESTAMP}},
			version     {{BIGINT}} NOT NULL DEFAULT 1
		)`,
		`CREATE INDEX incidents_status ON incidents (tenant_id, status, opened_at)`,
		// Generic config documents: templates, commands, time periods,
		// rules, groups, policies, schedules, channels, contacts,
		// contact groups, event sources, business services, dashboards,
		// reports, roles, webhook subscriptions (SPEC §6.1 — concrete
		// hot-path tables stay dedicated).
		`CREATE TABLE resources (
			id         TEXT PRIMARY KEY,
			tenant_id  TEXT NOT NULL,
			kind       TEXT NOT NULL,
			name       TEXT NOT NULL,
			doc        {{JSON}} NOT NULL,
			version    {{BIGINT}} NOT NULL DEFAULT 1,
			created_at {{TIMESTAMP}} NOT NULL,
			updated_at {{TIMESTAMP}} NOT NULL,
			UNIQUE (tenant_id, kind, name)
		)`,
		`CREATE INDEX resources_kind ON resources (tenant_id, kind)`,
		`CREATE TABLE downtimes (
			id            TEXT PRIMARY KEY,
			tenant_id     TEXT NOT NULL,
			object_id     TEXT,
			selector      TEXT NOT NULL DEFAULT '',
			dt_type       TEXT NOT NULL DEFAULT 'fixed',
			start_at      {{TIMESTAMP}} NOT NULL,
			end_at        {{TIMESTAMP}} NOT NULL,
			flex_duration {{BIGINT}} NOT NULL DEFAULT 0,
			triggered_by  TEXT NOT NULL DEFAULT '',
			rrule         TEXT NOT NULL DEFAULT '',
			comment       TEXT NOT NULL DEFAULT '',
			created_by    TEXT NOT NULL DEFAULT '',
			started_at    {{TIMESTAMP}},
			version       {{BIGINT}} NOT NULL DEFAULT 1,
			created_at    {{TIMESTAMP}} NOT NULL
		)`,
		`CREATE INDEX downtimes_window ON downtimes (tenant_id, end_at)`,
		`CREATE TABLE silences (
			id         TEXT PRIMARY KEY,
			tenant_id  TEXT NOT NULL,
			selector   TEXT NOT NULL DEFAULT '',
			text_regex TEXT NOT NULL DEFAULT '',
			comment    TEXT NOT NULL DEFAULT '',
			created_by TEXT NOT NULL DEFAULT '',
			starts_at  {{TIMESTAMP}} NOT NULL,
			expires_at {{TIMESTAMP}} NOT NULL,
			version    {{BIGINT}} NOT NULL DEFAULT 1,
			created_at {{TIMESTAMP}} NOT NULL
		)`,
		`CREATE INDEX silences_window ON silences (tenant_id, expires_at)`,
		`CREATE TABLE heartbeats (
			id           TEXT PRIMARY KEY,
			tenant_id    TEXT NOT NULL,
			name         TEXT NOT NULL,
			expect_every {{BIGINT}} NOT NULL,
			grace        {{BIGINT}} NOT NULL DEFAULT 0,
			severity     TEXT NOT NULL DEFAULT 'warning',
			labels       {{JSON}} NOT NULL DEFAULT '{}',
			last_beat    {{TIMESTAMP}},
			missing      {{BOOL}} NOT NULL DEFAULT false,
			version      {{BIGINT}} NOT NULL DEFAULT 1,
			created_at   {{TIMESTAMP}} NOT NULL,
			updated_at   {{TIMESTAMP}} NOT NULL,
			UNIQUE (tenant_id, name)
		)`,
		`CREATE TABLE api_tokens (
			id         TEXT PRIMARY KEY,
			tenant_id  TEXT NOT NULL,
			name       TEXT NOT NULL,
			prefix     TEXT NOT NULL,
			hash       TEXT NOT NULL,
			scopes     {{JSON}} NOT NULL DEFAULT '[]',
			roles      {{JSON}} NOT NULL DEFAULT '[]',
			ip_bind    {{JSON}} NOT NULL DEFAULT '[]',
			ai_agent   {{BOOL}} NOT NULL DEFAULT false,
			expires_at {{TIMESTAMP}},
			last_used  {{TIMESTAMP}},
			created_by TEXT NOT NULL DEFAULT '',
			version    {{BIGINT}} NOT NULL DEFAULT 1,
			created_at {{TIMESTAMP}} NOT NULL
		)`,
		`CREATE INDEX api_tokens_prefix ON api_tokens (prefix)`,
		`CREATE TABLE sessions (
			id         TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL,
			tenant_id  TEXT NOT NULL,
			data       {{JSON}} NOT NULL DEFAULT '{}',
			created_at {{TIMESTAMP}} NOT NULL,
			expires_at {{TIMESTAMP}} NOT NULL
		)`,
		`CREATE INDEX sessions_expiry ON sessions (expires_at)`,
		// Hash-chained revision log (SPEC §13.5).
		`CREATE TABLE audit_log (
			seq         {{PK_AUTO}},
			ts          {{TIMESTAMP}} NOT NULL,
			tenant_id   TEXT,
			actor_type  TEXT NOT NULL,
			actor_id    TEXT NOT NULL,
			action      TEXT NOT NULL,
			resource    TEXT,
			source_ip   TEXT NOT NULL DEFAULT '',
			request_id  TEXT NOT NULL DEFAULT '',
			before_json {{JSON}},
			after_json  {{JSON}},
			prev_hash   TEXT NOT NULL,
			hash        TEXT NOT NULL
		)`,
		`CREATE INDEX audit_ts ON audit_log (ts)`,
		// AES-256-GCM encrypted secret store ($SECRET:name$, SPEC §8.2/§13.2).
		`CREATE TABLE secrets (
			tenant_id  TEXT NOT NULL,
			name       TEXT NOT NULL,
			ciphertext {{BLOB}} NOT NULL,
			updated_by TEXT NOT NULL DEFAULT '',
			updated_at {{TIMESTAMP}} NOT NULL,
			PRIMARY KEY (tenant_id, name)
		)`,
		`CREATE TABLE idempotency (
			tenant_id  TEXT NOT NULL,
			idem_key   TEXT NOT NULL,
			req_hash   TEXT NOT NULL,
			status     {{BIGINT}} NOT NULL,
			body       {{BLOB}},
			created_at {{TIMESTAMP}} NOT NULL,
			PRIMARY KEY (tenant_id, idem_key)
		)`,
		// Escalation timers: durable so chains survive restarts (SPEC §9.4).
		`CREATE TABLE escalations (
			alert_id     TEXT NOT NULL,
			policy_name  TEXT NOT NULL,
			step_index   {{BIGINT}} NOT NULL,
			repeats_done {{BIGINT}} NOT NULL DEFAULT 0,
			next_at      {{TIMESTAMP}},
			done         {{BOOL}} NOT NULL DEFAULT false,
			PRIMARY KEY (alert_id, step_index)
		)`,
		`CREATE INDEX escalations_due ON escalations (done, next_at)`,
		// Outbound retry queue + dead letters (SPEC §9.6 / §11.5).
		`CREATE TABLE outbox (
			id          TEXT PRIMARY KEY,
			tenant_id   TEXT NOT NULL,
			channel_id  TEXT NOT NULL DEFAULT '',
			kind        TEXT NOT NULL,
			payload     {{JSON}} NOT NULL,
			attempts    {{BIGINT}} NOT NULL DEFAULT 0,
			next_try    {{TIMESTAMP}} NOT NULL,
			dead        {{BOOL}} NOT NULL DEFAULT false,
			last_error  TEXT NOT NULL DEFAULT '',
			created_at  {{TIMESTAMP}} NOT NULL
		)`,
		`CREATE INDEX outbox_due ON outbox (dead, next_try)`,
		// AI approval queue + conversations + usage (SPEC §10).
		`CREATE TABLE ai_actions (
			id              TEXT PRIMARY KEY,
			tenant_id       TEXT NOT NULL,
			conversation_id TEXT NOT NULL DEFAULT '',
			tool            TEXT NOT NULL,
			args            {{JSON}} NOT NULL,
			summary         TEXT NOT NULL DEFAULT '',
			status          TEXT NOT NULL DEFAULT 'proposed',
			actor           TEXT NOT NULL DEFAULT '',
			result          {{JSON}},
			decided_by      TEXT NOT NULL DEFAULT '',
			decided_at      {{TIMESTAMP}},
			created_at      {{TIMESTAMP}} NOT NULL
		)`,
		`CREATE INDEX ai_actions_status ON ai_actions (tenant_id, status, created_at)`,
		`CREATE TABLE ai_conversations (
			id         TEXT PRIMARY KEY,
			tenant_id  TEXT NOT NULL,
			user_id    TEXT NOT NULL DEFAULT '',
			title      TEXT NOT NULL DEFAULT '',
			messages   {{JSON}} NOT NULL DEFAULT '[]',
			version    {{BIGINT}} NOT NULL DEFAULT 1,
			created_at {{TIMESTAMP}} NOT NULL,
			updated_at {{TIMESTAMP}} NOT NULL
		)`,
		`CREATE TABLE ai_usage (
			month      TEXT PRIMARY KEY,
			tokens_in  {{BIGINT}} NOT NULL DEFAULT 0,
			tokens_out {{BIGINT}} NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE push_subscriptions (
			id         TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL,
			endpoint   TEXT NOT NULL,
			keys       {{JSON}} NOT NULL,
			created_at {{TIMESTAMP}} NOT NULL
		)`,
		// Scheduled-report archive (SPEC §9.8, CMP-Reports parity): each
		// rendered slot is stored as a BLOB. slot is the schedule slot key
		// ("2026-06-07" | "2026-W23" | "2026-06") and dedups one render per
		// (report, slot, format); Keep-based retention prunes the oldest.
		`CREATE TABLE report_archive (
			id          TEXT PRIMARY KEY,
			tenant_id   TEXT NOT NULL,
			report_name TEXT NOT NULL,
			slot        TEXT NOT NULL DEFAULT '',
			format      TEXT NOT NULL,
			content     {{BLOB}} NOT NULL,
			created_at  {{TIMESTAMP}} NOT NULL
		)`,
		`CREATE INDEX report_archive_rep ON report_archive (tenant_id, report_name, created_at DESC)`,
		// Misc durable state (adaptive baselines, satellite leases, …).
		`CREATE TABLE kv (
			k TEXT PRIMARY KEY,
			v {{JSON}} NOT NULL,
			updated_at {{TIMESTAMP}} NOT NULL
		)`,
	}},
	{2, "seed", nil}, // handled in code: default tenant + builtin roles
	// User-bound roles (SPEC §11.2). Sessions still carry the effective
	// role set, but local (break-glass) users now persist their roles so an
	// admin can manage them via the API and so the last-admin guard has a
	// durable source of truth. JSON array, NOT NULL DEFAULT '[]' on both
	// dialects (ALTER ... ADD COLUMN is in the shared subset).
	{3, "user_roles", []string{
		`ALTER TABLE users ADD COLUMN roles {{JSON}} NOT NULL DEFAULT '[]'`,
	}},
	// Scheduled reports: the v1 report_archive shape (report_id/rendered_at,
	// no slot column) was never written to. Recreate it with the slot-keyed
	// dedup shape the scheduler needs (SPEC §9.8). DROP+CREATE is in the
	// shared subset and safe because no rows ever existed.
	{4, "report_archive_slot", []string{
		`DROP TABLE IF EXISTS report_archive`,
		`CREATE TABLE report_archive (
			id          TEXT PRIMARY KEY,
			tenant_id   TEXT NOT NULL,
			report_name TEXT NOT NULL,
			slot        TEXT NOT NULL DEFAULT '',
			format      TEXT NOT NULL,
			content     {{BLOB}} NOT NULL,
			created_at  {{TIMESTAMP}} NOT NULL
		)`,
		`CREATE INDEX report_archive_rep ON report_archive (tenant_id, report_name, created_at DESC)`,
	}},
	// External ticket linkage (F-04.05): a ticket created for an alert is
	// remembered on the alert so resolution can auto-close it. ticket_meta
	// is a model.TicketRef JSON ('{}' = no ticket). ALTER … ADD COLUMN with
	// constant default is in the shared SQLite/Postgres subset.
	{5, "alert_ticket", []string{
		`ALTER TABLE alerts ADD COLUMN ticket_url TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE alerts ADD COLUMN ticket_meta {{JSON}} NOT NULL DEFAULT '{}'`,
	}},
	// Hot-path covering indices (storage audit). Three queries full-scanned
	// their tables before this: ListProblems (check_state), ListAlerts and
	// ExpireStaleAlerts (alerts). The DDL below is in the shared subset —
	// CREATE INDEX IF NOT EXISTS and partial (WHERE) indices are supported by
	// both SQLite and PostgreSQL.
	{6, "hotpath_indices", []string{
		// ListProblems (objects.go) filters check_state on `state != 0`
		// (and state_type='hard'). A partial index over the non-OK rows keeps
		// the index tiny (most states are OK=0) and lets the Problems view
		// skip the full table scan.
		`CREATE INDEX IF NOT EXISTS check_state_problem ON check_state (state, state_type)
			WHERE state != 0`,
		// ListAlerts (alerts.go) filters by tenant + object_id; ExpireStaleAlerts
		// filters by tenant + rule_id + status ordered/bounded by opened_at.
		`CREATE INDEX IF NOT EXISTS alerts_object ON alerts (tenant_id, object_id)`,
		`CREATE INDEX IF NOT EXISTS alerts_rule ON alerts (tenant_id, rule_id, status, opened_at)`,
	}},
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, s.dialect.DDL(
		`CREATE TABLE IF NOT EXISTS schema_version (
			version {{BIGINT}} PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at {{TIMESTAMP}} NOT NULL
		)`)); err != nil {
		return fmt.Errorf("storage: schema_version: %w", err)
	}
	var current int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&current); err != nil {
		return err
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		s.log.Info("storage: applying migration", "version", m.version, "name", m.name)
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range m.sql {
			if _, err := tx.ExecContext(ctx, s.dialect.DDL(stmt)); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("storage: migration %d %q: %w\n%s", m.version, m.name, err, stmt)
			}
		}
		if m.version == 2 {
			if err := seed(ctx, tx, s.dialect); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO schema_version (version, name, applied_at) VALUES (?,?,?)`),
			m.version, m.name, s.T(time.Now())); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// seed creates the default tenant and built-in roles.
func seed(ctx context.Context, tx *sql.Tx, d Dialect) error {
	now := d.TimeValue(time.Now())
	q := d.Rebind(`INSERT INTO tenants (id, name, slug, version, created_at, updated_at)
		VALUES (?,?,?,1,?,?) ON CONFLICT (id) DO NOTHING`)
	if _, err := tx.ExecContext(ctx, q, model.DefaultTenant, "Default", "default", now, now); err != nil {
		return err
	}
	for _, r := range model.BuiltinRoles {
		doc, err := jsonMarshal(model.Role{
			ID: model.NewID(), TenantID: model.DefaultTenant, Name: r.Name,
			Permissions: r.Permissions, System: true, Version: 1,
		})
		if err != nil {
			return err
		}
		q := d.Rebind(`INSERT INTO resources (id, tenant_id, kind, name, doc, version, created_at, updated_at)
			VALUES (?,?,?,?,?,1,?,?) ON CONFLICT (tenant_id, kind, name) DO NOTHING`)
		if _, err := tx.ExecContext(ctx, q,
			model.NewID(), model.DefaultTenant, "role", r.Name, doc, now, now); err != nil {
			return err
		}
	}
	return nil
}

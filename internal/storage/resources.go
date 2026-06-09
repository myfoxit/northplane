package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/model"
)

func newID() string { return model.NewID() }

// Resource kinds stored in the generic config-document table. Hot-path
// entities (objects, alerts, events, …) have dedicated tables; these are
// configuration documents read at evaluation time and cached in memory.
const (
	KindTemplate         = "template"
	KindCheckCommand     = "check-command"
	KindTimePeriod       = "time-period"
	KindAlertRule        = "alert-rule"
	KindAlertGroup       = "alert-group"
	KindEscalationPolicy = "escalation-policy"
	KindSchedule         = "schedule"
	KindOverride         = "override"
	KindContact          = "contact"
	KindContactGroup     = "contact-group"
	KindChannel          = "channel"
	KindEventSource      = "event-source"
	KindBusinessService  = "business-service"
	KindDashboard        = "dashboard"
	KindReport           = "report"
	KindRole             = "role"
	KindWebhookSub       = "webhook-subscription"
	KindSavedFilter      = "saved-filter"
	KindStaticGroup      = "static-group" // Nagios import fidelity (SPEC §6.2)
	KindPreference       = "preference"   // per-actor UI settings (name = actor ID)
)

// ResourceEnvelope wraps a stored document.
type ResourceEnvelope struct {
	ID        string          `json:"id"`
	TenantID  string          `json:"tenantId"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Doc       json.RawMessage `json:"doc"`
	Version   int64           `json:"version"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// PutResource creates or updates (by tenant+kind+name). expectVersion:
// -1 = must not exist (create), 0 = upsert unconditional, >0 = optimistic.
// The stored doc gets id/tenantId/name/version/timestamps injected so
// documents round-trip completely.
func (s *Store) PutResource(ctx context.Context, tenantID, kind, name string, doc any, expectVersion int64) (*ResourceEnvelope, error) {
	now := time.Now().UTC()
	existing, err := s.GetResource(ctx, tenantID, kind, name)
	if err != nil && err != ErrNotFound {
		return nil, err
	}
	switch {
	case existing == nil && expectVersion > 0:
		return nil, ErrNotFound
	case existing != nil && expectVersion == -1:
		return nil, fmt.Errorf("%w: %s %q", ErrDuplicate, kind, name)
	case existing != nil && expectVersion > 0 && existing.Version != expectVersion:
		return nil, fmt.Errorf("%w: have %d, expected %d", ErrConflict, existing.Version, expectVersion)
	}

	env := &ResourceEnvelope{TenantID: tenantID, Kind: kind, Name: name, CreatedAt: now, UpdatedAt: now}
	if existing != nil {
		env.ID, env.Version, env.CreatedAt = existing.ID, existing.Version+1, existing.CreatedAt
	} else {
		env.ID, env.Version = newDocID(doc), 1
	}

	raw, err := normalizeDoc(doc, env)
	if err != nil {
		return nil, err
	}
	env.Doc = raw

	// The version/existence checks above are advisory (read before the
	// write); enforce them atomically inside the transaction so two
	// concurrent writers with the same If-Match cannot both succeed and
	// lose an update (a plain upsert has no version guard).
	vals := []any{env.ID, tenantID, kind, name, string(raw), env.Version, s.T(env.CreatedAt), s.T(now)}
	base := `INSERT INTO resources (id, tenant_id, kind, name, doc, version, created_at, updated_at)
			 VALUES (?,?,?,?,?,?,?,?)`
	err = s.Write(ctx, func(tx *sql.Tx) error {
		var q string
		args := vals
		switch {
		case expectVersion == -1: // create-only
			q = base + ` ON CONFLICT (tenant_id, kind, name) DO NOTHING`
		case expectVersion > 0: // guarded update (optimistic concurrency)
			q = base + ` ON CONFLICT (tenant_id, kind, name) DO UPDATE SET
				 doc = excluded.doc, version = excluded.version, updated_at = excluded.updated_at
				 WHERE resources.version = ?`
			args = append(append([]any{}, vals...), expectVersion)
		default: // unconditional upsert (expectVersion == 0)
			q = base + ` ON CONFLICT (tenant_id, kind, name) DO UPDATE SET
				 doc = excluded.doc, version = excluded.version, updated_at = excluded.updated_at`
		}
		res, err := tx.ExecContext(ctx, s.Q(q), args...)
		if err != nil {
			return err
		}
		if expectVersion == -1 || expectVersion > 0 {
			if n, _ := res.RowsAffected(); n == 0 {
				if expectVersion == -1 {
					return fmt.Errorf("%w: %s %q", ErrDuplicate, kind, name)
				}
				return fmt.Errorf("%w: %s %q changed concurrently", ErrConflict, kind, name)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return env, nil
}

// newDocID honours a caller-set "id" field, else mints one.
func newDocID(doc any) string {
	b, _ := json.Marshal(doc)
	var probe struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(b, &probe)
	if probe.ID != "" {
		return probe.ID
	}
	return newID()
}

// normalizeDoc injects envelope fields into the document JSON.
func normalizeDoc(doc any, env *ResourceEnvelope) (json.RawMessage, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("resource doc must be an object: %w", err)
	}
	m["id"] = env.ID
	m["tenantId"] = env.TenantID
	m["name"] = env.Name
	m["version"] = env.Version
	m["createdAt"] = env.CreatedAt.Format(time.RFC3339Nano)
	m["updatedAt"] = env.UpdatedAt.Format(time.RFC3339Nano)
	return json.Marshal(m)
}

// GetResource by natural key.
func (s *Store) GetResource(ctx context.Context, tenantID, kind, name string) (*ResourceEnvelope, error) {
	row := s.db.QueryRowContext(ctx, s.Q(
		`SELECT id, tenant_id, kind, name, doc, version, created_at, updated_at
		 FROM resources WHERE tenant_id = ? AND kind = ? AND name = ?`), tenantID, kind, name)
	return scanResource(row)
}

// GetResourceByID by surrogate key.
func (s *Store) GetResourceByID(ctx context.Context, tenantID, kind, id string) (*ResourceEnvelope, error) {
	row := s.db.QueryRowContext(ctx, s.Q(
		`SELECT id, tenant_id, kind, name, doc, version, created_at, updated_at
		 FROM resources WHERE tenant_id = ? AND kind = ? AND id = ?`), tenantID, kind, id)
	return scanResource(row)
}

// ResolveResource accepts either an ID or a name.
func (s *Store) ResolveResource(ctx context.Context, tenantID, kind, ref string) (*ResourceEnvelope, error) {
	if env, err := s.GetResource(ctx, tenantID, kind, ref); err == nil {
		return env, nil
	} else if err != ErrNotFound {
		return nil, err
	}
	return s.GetResourceByID(ctx, tenantID, kind, ref)
}

func scanResource(sc interface{ Scan(...any) error }) (*ResourceEnvelope, error) {
	var env ResourceEnvelope
	var doc string
	var created, updated ScanTime
	if err := sc.Scan(&env.ID, &env.TenantID, &env.Kind, &env.Name, &doc,
		&env.Version, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	env.Doc = json.RawMessage(doc)
	env.CreatedAt, env.UpdatedAt = created.T, updated.T
	return &env, nil
}

// ListResources of a kind, name-ordered, optional name filter.
func (s *Store) ListResources(ctx context.Context, tenantID, kind, query, cursor string, limit int) ([]*ResourceEnvelope, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	conds := []string{"tenant_id = ?", "kind = ?"}
	args := []any{tenantID, kind}
	if query != "" {
		conds = append(conds, "(name LIKE ? OR doc LIKE ?)")
		like := "%" + query + "%"
		args = append(args, like, like)
	}
	if cursor != "" {
		conds = append(conds, "name > ?")
		args = append(args, cursor)
	}
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT id, tenant_id, kind, name, doc, version, created_at, updated_at
		 FROM resources WHERE `+strings.Join(conds, " AND ")+
			` ORDER BY name LIMIT `+fmt.Sprint(limit)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ResourceEnvelope
	for rows.Next() {
		env, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, env)
	}
	return out, rows.Err()
}

// DeleteResource by natural key.
func (s *Store) DeleteResource(ctx context.Context, tenantID, kind, name string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`DELETE FROM resources WHERE tenant_id = ? AND kind = ? AND name = ?`),
			tenantID, kind, name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// LoadAll unmarshals every document of a kind into dst (pointer to slice)
// — the evaluation caches (rules, policies, schedules…) refresh through
// this.
func LoadAll[T any](ctx context.Context, s *Store, tenantID, kind string) ([]*T, error) {
	envs, err := s.ListResources(ctx, tenantID, kind, "", "", 2000)
	if err != nil {
		return nil, err
	}
	out := make([]*T, 0, len(envs))
	for _, env := range envs {
		var v T
		if err := json.Unmarshal(env.Doc, &v); err != nil {
			return nil, fmt.Errorf("%s %q: %w", kind, env.Name, err)
		}
		out = append(out, &v)
	}
	return out, nil
}

// LoadOne unmarshals a single document resolved by name or ID.
func LoadOne[T any](ctx context.Context, s *Store, tenantID, kind, ref string) (*T, error) {
	env, err := s.ResolveResource(ctx, tenantID, kind, ref)
	if err != nil {
		return nil, err
	}
	var v T
	if err := json.Unmarshal(env.Doc, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// Tenants returns all tenant rows.
func (s *Store) Tenants(ctx context.Context) ([]TenantRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, slug, disabled, version, created_at, updated_at FROM tenants ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TenantRow
	for rows.Next() {
		var t TenantRow
		var created, updated ScanTime
		if err := rows.Scan(&t.ID, &t.Name, &t.Slug, &t.Disabled, &t.Version, &created, &updated); err != nil {
			return nil, err
		}
		t.CreatedAt, t.UpdatedAt = created.T, updated.T
		out = append(out, t)
	}
	return out, rows.Err()
}

// TenantRow mirrors model.Tenant at the storage boundary.
type TenantRow struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Disabled  bool      `json:"disabled"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CreateTenant inserts a tenant.
func (s *Store) CreateTenant(ctx context.Context, id, name, slug string) error {
	now := time.Now().UTC()
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO tenants (id, name, slug, version, created_at, updated_at) VALUES (?,?,?,1,?,?)`),
			id, name, slug, s.T(now), s.T(now))
		if s.dialect.IsDuplicate(err) {
			return fmt.Errorf("%w: tenant %q", ErrDuplicate, slug)
		}
		return err
	})
}

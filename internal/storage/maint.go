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

// --- downtimes ---

const downtimeCols = `id, tenant_id, object_id, selector, dt_type, start_at, end_at,
	flex_duration, triggered_by, rrule, comment, created_by, started_at, version, created_at`

func scanDowntime(sc interface{ Scan(...any) error }) (*model.Downtime, error) {
	var d model.Downtime
	var objectID NullStr
	var flexNS int64
	var start, end, started, created ScanTime
	if err := sc.Scan(&d.ID, &d.TenantID, &objectID, &d.Selector, (*string)(&d.Type),
		&start, &end, &flexNS, &d.TriggeredBy, &d.RRule, &d.Comment, &d.CreatedBy,
		&started, &d.Version, &created); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d.ObjectID = string(objectID)
	d.Start, d.End = start.T, end.T
	d.FlexDuration = model.Duration(flexNS)
	d.StartedAt = started.Ptr()
	d.CreatedAt = created.T
	return &d, nil
}

// CreateDowntime inserts a downtime window.
func (s *Store) CreateDowntime(ctx context.Context, d *model.Downtime) error {
	if d.ID == "" {
		d.ID = model.NewID()
	}
	d.CreatedAt = time.Now().UTC()
	d.Version = 1
	if d.Type == "" {
		d.Type = model.DowntimeFixed
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO downtimes (`+downtimeCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
			d.ID, d.TenantID, S(d.ObjectID), d.Selector, string(d.Type),
			s.T(d.Start), s.T(d.End), int64(d.FlexDuration), d.TriggeredBy, d.RRule,
			d.Comment, d.CreatedBy, s.TP(d.StartedAt), d.Version, s.T(d.CreatedAt))
		return err
	})
}

// DeleteDowntime cancels a downtime.
func (s *Store) DeleteDowntime(ctx context.Context, tenantID, id string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`DELETE FROM downtimes WHERE tenant_id = ? AND id = ?`), tenantID, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ListDowntimes; activeOnly filters to windows overlapping now.
func (s *Store) ListDowntimes(ctx context.Context, tenantID string, activeOnly bool) ([]*model.Downtime, error) {
	conds := []string{"tenant_id = ?"}
	args := []any{tenantID}
	if activeOnly {
		conds = append(conds, "end_at > ?")
		args = append(args, s.T(time.Now().UTC()))
	}
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT `+downtimeCols+` FROM downtimes WHERE `+strings.Join(conds, " AND ")+
			` ORDER BY start_at DESC LIMIT 1000`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Downtime
	for rows.Next() {
		d, err := scanDowntime(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDowntime by ID.
func (s *Store) GetDowntime(ctx context.Context, tenantID, id string) (*model.Downtime, error) {
	row := s.db.QueryRowContext(ctx, s.Q(
		`SELECT `+downtimeCols+` FROM downtimes WHERE tenant_id = ? AND id = ?`), tenantID, id)
	return scanDowntime(row)
}

// MarkFlexDowntimeStarted records the trigger time of a flexible downtime.
func (s *Store) MarkFlexDowntimeStarted(ctx context.Context, id string, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`UPDATE downtimes SET started_at = ? WHERE id = ? AND started_at IS NULL`), s.T(at), id)
		return err
	})
}

// --- silences ---

const silenceCols = `id, tenant_id, selector, text_regex, comment, created_by,
	starts_at, expires_at, version, created_at`

func scanSilence(sc interface{ Scan(...any) error }) (*model.Silence, error) {
	var si model.Silence
	var starts, expires, created ScanTime
	if err := sc.Scan(&si.ID, &si.TenantID, &si.Selector, &si.TextRegex, &si.Comment,
		&si.CreatedBy, &starts, &expires, &si.Version, &created); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	si.StartsAt, si.ExpiresAt, si.CreatedAt = starts.T, expires.T, created.T
	return &si, nil
}

// CreateSilence enforces the mandatory TTL (SPEC §9.2).
func (s *Store) CreateSilence(ctx context.Context, si *model.Silence) error {
	if si.ExpiresAt.IsZero() {
		return fmt.Errorf("silence requires expiresAt (TTL mandatory)")
	}
	if si.ID == "" {
		si.ID = model.NewID()
	}
	if si.StartsAt.IsZero() {
		si.StartsAt = time.Now().UTC()
	}
	si.CreatedAt = time.Now().UTC()
	si.Version = 1
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO silences (`+silenceCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`),
			si.ID, si.TenantID, si.Selector, si.TextRegex, si.Comment, si.CreatedBy,
			s.T(si.StartsAt), s.T(si.ExpiresAt), si.Version, s.T(si.CreatedAt))
		return err
	})
}

// DeleteSilence removes a silence early.
func (s *Store) DeleteSilence(ctx context.Context, tenantID, id string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`DELETE FROM silences WHERE tenant_id = ? AND id = ?`), tenantID, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ListSilences; activeOnly = not yet expired.
func (s *Store) ListSilences(ctx context.Context, tenantID string, activeOnly bool) ([]*model.Silence, error) {
	conds := []string{"tenant_id = ?"}
	args := []any{tenantID}
	if activeOnly {
		conds = append(conds, "expires_at > ?")
		args = append(args, s.T(time.Now().UTC()))
	}
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT `+silenceCols+` FROM silences WHERE `+strings.Join(conds, " AND ")+
			` ORDER BY expires_at DESC LIMIT 1000`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Silence
	for rows.Next() {
		si, err := scanSilence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, si)
	}
	return out, rows.Err()
}

// --- heartbeats ---

const heartbeatCols = `id, tenant_id, name, expect_every, grace, severity, labels,
	last_beat, missing, version, created_at, updated_at`

func scanHeartbeat(sc interface{ Scan(...any) error }) (*model.Heartbeat, error) {
	var h model.Heartbeat
	var expectNS, graceNS int64
	var labels string
	var lastBeat, created, updated ScanTime
	if err := sc.Scan(&h.ID, &h.TenantID, &h.Name, &expectNS, &graceNS,
		(*string)(&h.Severity), &labels, &lastBeat, &h.Missing,
		&h.Version, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	h.ExpectEvery, h.Grace = model.Duration(expectNS), model.Duration(graceNS)
	_ = json.Unmarshal([]byte(labels), &h.Labels)
	h.LastBeat = lastBeat.Ptr()
	h.CreatedAt, h.UpdatedAt = created.T, updated.T
	return &h, nil
}

// PutHeartbeat upserts the definition by (tenant, name).
func (s *Store) PutHeartbeat(ctx context.Context, h *model.Heartbeat) error {
	now := time.Now().UTC()
	if h.ID == "" {
		h.ID = model.NewID()
	}
	if h.Severity == "" {
		h.Severity = model.SevWarning
	}
	h.UpdatedAt = now
	labels, _ := jsonMarshal(orLabels(h.Labels))
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO heartbeats (`+heartbeatCols+`) VALUES (?,?,?,?,?,?,?,?,?,1,?,?)
			 ON CONFLICT (tenant_id, name) DO UPDATE SET
			 expect_every = excluded.expect_every, grace = excluded.grace,
			 severity = excluded.severity, labels = excluded.labels,
			 version = heartbeats.version + 1, updated_at = excluded.updated_at`),
			h.ID, h.TenantID, h.Name, int64(h.ExpectEvery), int64(h.Grace),
			string(h.Severity), labels, s.TP(h.LastBeat), h.Missing, s.T(now), s.T(now))
		return err
	})
}

// Beat records a heartbeat ping; returns the heartbeat (ErrNotFound when
// undefined and autocreate is off).
func (s *Store) Beat(ctx context.Context, tenantID, name string, at time.Time) (*model.Heartbeat, bool, error) {
	var recovered bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var missing bool
		err := tx.QueryRowContext(ctx, s.Q(
			`SELECT missing FROM heartbeats WHERE tenant_id = ? AND name = ?`),
			tenantID, name).Scan(&missing)
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		recovered = missing
		_, err = tx.ExecContext(ctx, s.Q(
			`UPDATE heartbeats SET last_beat = ?, missing = false, updated_at = ? WHERE tenant_id = ? AND name = ?`),
			s.T(at), s.T(at), tenantID, name)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	h, err := s.GetHeartbeat(ctx, tenantID, name)
	return h, recovered, err
}

// GetHeartbeat by name.
func (s *Store) GetHeartbeat(ctx context.Context, tenantID, name string) (*model.Heartbeat, error) {
	row := s.db.QueryRowContext(ctx, s.Q(
		`SELECT `+heartbeatCols+` FROM heartbeats WHERE tenant_id = ? AND name = ?`), tenantID, name)
	return scanHeartbeat(row)
}

// ListHeartbeats all definitions of a tenant.
func (s *Store) ListHeartbeats(ctx context.Context, tenantID string) ([]*model.Heartbeat, error) {
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT `+heartbeatCols+` FROM heartbeats WHERE tenant_id = ? ORDER BY name`), tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Heartbeat
	for rows.Next() {
		h, err := scanHeartbeat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// DeleteHeartbeat removes a definition.
func (s *Store) DeleteHeartbeat(ctx context.Context, tenantID, name string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`DELETE FROM heartbeats WHERE tenant_id = ? AND name = ?`), tenantID, name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// MarkHeartbeatMissing flips a heartbeat into missing state (returns
// false when it already was).
func (s *Store) MarkHeartbeatMissing(ctx context.Context, id string) (bool, error) {
	var flipped bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`UPDATE heartbeats SET missing = true, updated_at = ? WHERE id = ? AND missing = false`),
			s.T(time.Now().UTC()), id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		flipped = n > 0
		return nil
	})
	return flipped, err
}

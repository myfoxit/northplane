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

const alertCols = `id, tenant_id, rule_id, object_id, incident_id, status, severity, title,
	dedup_key, labels, event_ids, opened_at, acked_at, acked_by, resolved_at, payload,
	ticket_url, ticket_meta`

func scanAlert(sc interface{ Scan(...any) error }) (*model.Alert, error) {
	var a model.Alert
	var ruleID, objectID, incidentID, dedup NullStr
	var labels, eventIDs, payload, ticketURL, ticketMeta string
	var opened, acked, resolved ScanTime
	if err := sc.Scan(&a.ID, &a.TenantID, &ruleID, &objectID, &incidentID,
		(*string)(&a.Status), (*string)(&a.Severity), &a.Title, &dedup,
		&labels, &eventIDs, &opened, &acked, &a.AckedBy, &resolved, &payload,
		&ticketURL, &ticketMeta); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.RuleID, a.ObjectID, a.IncidentID, a.DedupKey = string(ruleID), string(objectID), string(incidentID), string(dedup)
	a.OpenedAt = opened.T
	a.AckedAt, a.ResolvedAt = acked.Ptr(), resolved.Ptr()
	_ = json.Unmarshal([]byte(labels), &a.Labels)
	_ = json.Unmarshal([]byte(eventIDs), &a.EventIDs)
	a.Payload = json.RawMessage(payload)
	if ticketMeta != "" && ticketMeta != "{}" {
		var t model.TicketRef
		if json.Unmarshal([]byte(ticketMeta), &t) == nil && t.Ref != "" {
			t.URL = ticketURL
			a.Ticket = &t
		}
	}
	return &a, nil
}

// UpsertAlert opens an alert or refreshes the open one with the same
// dedup key (unique partial index alerts_dedup, SPEC §6.5). Returns the
// stored alert and whether it was newly opened.
func (s *Store) UpsertAlert(ctx context.Context, a *model.Alert) (*model.Alert, bool, error) {
	if a.ID == "" {
		a.ID = model.NewID()
	}
	if a.OpenedAt.IsZero() {
		a.OpenedAt = time.Now().UTC()
	}
	labels, _ := jsonMarshal(orLabels(a.Labels))
	eventIDs, _ := jsonMarshal(orSlice(a.EventIDs))

	if a.DedupKey != "" {
		existing, err := s.FindOpenAlertByDedup(ctx, a.TenantID, a.DedupKey)
		if err != nil && err != ErrNotFound {
			return nil, false, err
		}
		if existing != nil {
			// Refresh: severity may rise, events accumulate.
			merged := append(existing.EventIDs, a.EventIDs...)
			if len(merged) > 50 {
				merged = merged[len(merged)-50:]
			}
			mergedJSON, _ := jsonMarshal(merged)
			sev := existing.Severity
			if a.Severity.Rank() > sev.Rank() {
				sev = a.Severity
			}
			err := s.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, s.Q(
					`UPDATE alerts SET severity = ?, title = ?, payload = ?, event_ids = ? WHERE id = ?`),
					string(sev), a.Title, string(orEmptyJSON(a.Payload)), mergedJSON, existing.ID)
				return err
			})
			if err != nil {
				return nil, false, err
			}
			existing.Severity, existing.Title, existing.EventIDs = sev, a.Title, merged
			return existing, false, nil
		}
	}
	a.Status = model.AlertOpen
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO alerts (`+alertCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
			a.ID, a.TenantID, S(a.RuleID), S(a.ObjectID), S(a.IncidentID),
			string(a.Status), string(a.Severity), a.Title, S(a.DedupKey),
			labels, eventIDs, s.T(a.OpenedAt), nil, "", nil, string(orEmptyJSON(a.Payload)),
			"", "{}")
		return err
	})
	if err != nil {
		if s.dialect.IsDuplicate(err) {
			// Lost race on dedup key: re-read and merge.
			existing, err2 := s.FindOpenAlertByDedup(ctx, a.TenantID, a.DedupKey)
			if err2 == nil && existing != nil {
				return existing, false, nil
			}
		}
		return nil, false, err
	}
	return a, true, nil
}

func orSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// FindOpenAlertByDedup locates the open/acked alert holding a dedup key.
func (s *Store) FindOpenAlertByDedup(ctx context.Context, tenantID, dedupKey string) (*model.Alert, error) {
	row := s.db.QueryRowContext(ctx, s.Q(
		`SELECT `+alertCols+` FROM alerts
		 WHERE tenant_id = ? AND dedup_key = ? AND status IN ('open','acked')`), tenantID, dedupKey)
	return scanAlert(row)
}

// GetAlert by ID.
func (s *Store) GetAlert(ctx context.Context, tenantID, id string) (*model.Alert, error) {
	row := s.db.QueryRowContext(ctx, s.Q(
		`SELECT `+alertCols+` FROM alerts WHERE tenant_id = ? AND id = ?`), tenantID, id)
	return scanAlert(row)
}

// AlertFilter narrows listing.
type AlertFilter struct {
	TenantID   string
	Status     []model.AlertStatus
	Severity   []model.Severity
	ObjectID   string
	RuleID     string
	IncidentID string
	Since      time.Time
	Cursor     string
	Limit      int
}

// ListAlerts newest-first with cursor pagination.
func (s *Store) ListAlerts(ctx context.Context, f AlertFilter) ([]*model.Alert, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	conds := []string{"tenant_id = ?"}
	args := []any{f.TenantID}
	if len(f.Status) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(f.Status)), ",")
		conds = append(conds, "status IN ("+ph+")")
		for _, st := range f.Status {
			args = append(args, string(st))
		}
	}
	if len(f.Severity) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(f.Severity)), ",")
		conds = append(conds, "severity IN ("+ph+")")
		for _, sv := range f.Severity {
			args = append(args, string(sv))
		}
	}
	if f.ObjectID != "" {
		conds = append(conds, "object_id = ?")
		args = append(args, f.ObjectID)
	}
	if f.RuleID != "" {
		conds = append(conds, "rule_id = ?")
		args = append(args, f.RuleID)
	}
	if f.IncidentID != "" {
		conds = append(conds, "incident_id = ?")
		args = append(args, f.IncidentID)
	}
	if !f.Since.IsZero() {
		conds = append(conds, "opened_at >= ?")
		args = append(args, s.T(f.Since))
	}
	if f.Cursor != "" {
		conds = append(conds, "id < ?")
		args = append(args, f.Cursor)
	}
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT `+alertCols+` FROM alerts WHERE `+strings.Join(conds, " AND ")+
			` ORDER BY id DESC LIMIT `+fmt.Sprint(f.Limit)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Alert
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AckAlert transitions open → acked.
func (s *Store) AckAlert(ctx context.Context, tenantID, id, by string) (*model.Alert, error) {
	now := time.Now().UTC()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`UPDATE alerts SET status = 'acked', acked_at = ?, acked_by = ?
			 WHERE tenant_id = ? AND id = ? AND status = 'open'`), s.T(now), by, tenantID, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetAlert(ctx, tenantID, id)
}

// ResolveAlert closes the alert (status resolved|expired). When the
// alert carries an auto-close ticket (F-04.05), a durable ticket-close
// job is enqueued — central here so every resolve path (engine, API,
// AI tool, ack-link) closes the external ticket.
func (s *Store) ResolveAlert(ctx context.Context, tenantID, id string, status model.AlertStatus) (*model.Alert, error) {
	now := time.Now().UTC()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`UPDATE alerts SET status = ?, resolved_at = ?
			 WHERE tenant_id = ? AND id = ? AND status IN ('open','acked')`),
			string(status), s.T(now), tenantID, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	alert, err := s.GetAlert(ctx, tenantID, id)
	if err == nil && alert.Ticket != nil && alert.Ticket.AutoClose {
		s.enqueueTicketClose(ctx, alert)
	}
	return alert, err
}

// enqueueTicketClose schedules the external ticket close in the outbox
// (retry semantics identical to notifications).
func (s *Store) enqueueTicketClose(ctx context.Context, a *model.Alert) {
	payload, _ := json.Marshal(map[string]any{
		"tenantId": a.TenantID, "alertId": a.ID, "title": a.Title, "ticket": a.Ticket})
	_ = s.EnqueueOutbox(ctx, &OutboxItem{
		TenantID: a.TenantID, Kind: "ticket-close", Payload: payload})
}

// SetAlertTicket records the external ticket created for an alert.
func (s *Store) SetAlertTicket(ctx context.Context, tenantID, alertID string, t *model.TicketRef) error {
	meta, _ := json.Marshal(t)
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`UPDATE alerts SET ticket_url = ?, ticket_meta = ? WHERE tenant_id = ? AND id = ?`),
			t.URL, string(meta), tenantID, alertID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// CountActiveAlertsByIncident counts open/acked alerts in an incident
// (auto-resolve gate for rule-created incidents).
func (s *Store) CountActiveAlertsByIncident(ctx context.Context, tenantID, incidentID string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, s.Q(
		`SELECT COUNT(*) FROM alerts
		 WHERE tenant_id = ? AND incident_id = ? AND status IN ('open','acked')`),
		tenantID, incidentID).Scan(&n)
	return n, err
}

// AssignAlertIncident links/unlinks an alert to an incident.
func (s *Store) AssignAlertIncident(ctx context.Context, tenantID, alertID, incidentID string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`UPDATE alerts SET incident_id = ? WHERE tenant_id = ? AND id = ?`),
			S(incidentID), tenantID, alertID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ExpireStaleAlerts closes open alerts past their auto-close horizon —
// called by the janitor with rule-resolved cutoffs. Ticketed alerts with
// autoClose get their external ticket closed too.
func (s *Store) ExpireStaleAlerts(ctx context.Context, tenantID, ruleID string, before time.Time) (int64, error) {
	// collect auto-close tickets before the bulk flip
	ticketed, _ := s.ListAlerts(ctx, AlertFilter{TenantID: tenantID, RuleID: ruleID,
		Status: []model.AlertStatus{model.AlertOpen, model.AlertAcked}, Limit: 1000})
	var n int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`UPDATE alerts SET status = 'expired', resolved_at = ?
			 WHERE tenant_id = ? AND rule_id = ? AND status IN ('open','acked') AND opened_at < ?`),
			s.T(time.Now().UTC()), tenantID, ruleID, s.T(before))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	if err == nil && n > 0 {
		for _, a := range ticketed {
			if a.Ticket != nil && a.Ticket.AutoClose && a.OpenedAt.Before(before) {
				s.enqueueTicketClose(ctx, a)
			}
		}
	}
	return n, err
}

// OpenAlertStats: counts per severity (overview).
func (s *Store) OpenAlertStats(ctx context.Context, tenantID string) (map[model.Severity]int64, error) {
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT severity, COUNT(*) FROM alerts
		 WHERE tenant_id = ? AND status IN ('open','acked') GROUP BY severity`), tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[model.Severity]int64{}
	for rows.Next() {
		var sev string
		var n int64
		if err := rows.Scan(&sev, &n); err != nil {
			return nil, err
		}
		out[model.Severity(sev)] = n
	}
	return out, rows.Err()
}

// --- incidents ---

const incidentCols = `id, tenant_id, status, severity, title, summary, impact, ticket_url,
	created_by, opened_at, resolved_at, version`

func scanIncident(sc interface{ Scan(...any) error }) (*model.Incident, error) {
	var in model.Incident
	var opened, resolved ScanTime
	if err := sc.Scan(&in.ID, &in.TenantID, (*string)(&in.Status), (*string)(&in.Severity),
		&in.Title, &in.Summary, &in.Impact, &in.TicketURL, &in.CreatedBy,
		&opened, &resolved, &in.Version); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	in.OpenedAt, in.ResolvedAt = opened.T, resolved.Ptr()
	return &in, nil
}

// CreateIncident inserts a new incident.
func (s *Store) CreateIncident(ctx context.Context, in *model.Incident) error {
	if in.ID == "" {
		in.ID = model.NewID()
	}
	if in.OpenedAt.IsZero() {
		in.OpenedAt = time.Now().UTC()
	}
	in.Version = 1
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO incidents (`+incidentCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`),
			in.ID, in.TenantID, string(in.Status), string(in.Severity), in.Title,
			in.Summary, in.Impact, in.TicketURL, in.CreatedBy,
			s.T(in.OpenedAt), s.TP(in.ResolvedAt), in.Version)
		return err
	})
}

// UpdateIncident with optimistic locking.
func (s *Store) UpdateIncident(ctx context.Context, in *model.Incident, expectVersion int64) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		cond := `tenant_id = ? AND id = ?`
		args := []any{string(in.Status), string(in.Severity), in.Title, in.Summary,
			in.Impact, in.TicketURL, s.TP(in.ResolvedAt), in.TenantID, in.ID}
		if expectVersion > 0 {
			cond += ` AND version = ?`
			args = append(args, expectVersion)
		}
		res, err := tx.ExecContext(ctx, s.Q(
			`UPDATE incidents SET status = ?, severity = ?, title = ?, summary = ?,
			 impact = ?, ticket_url = ?, resolved_at = ?, version = version + 1
			 WHERE `+cond), args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrConflict
		}
		in.Version++
		return nil
	})
}

// GetIncident by ID.
func (s *Store) GetIncident(ctx context.Context, tenantID, id string) (*model.Incident, error) {
	row := s.db.QueryRowContext(ctx, s.Q(
		`SELECT `+incidentCols+` FROM incidents WHERE tenant_id = ? AND id = ?`), tenantID, id)
	return scanIncident(row)
}

// ListIncidents newest-first.
func (s *Store) ListIncidents(ctx context.Context, tenantID string, openOnly bool, cursor string, limit int) ([]*model.Incident, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	conds := []string{"tenant_id = ?"}
	args := []any{tenantID}
	if openOnly {
		conds = append(conds, "status = 'open'")
	}
	if cursor != "" {
		conds = append(conds, "id < ?")
		args = append(args, cursor)
	}
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT `+incidentCols+` FROM incidents WHERE `+strings.Join(conds, " AND ")+
			` ORDER BY id DESC LIMIT `+fmt.Sprint(limit)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Incident
	for rows.Next() {
		in, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

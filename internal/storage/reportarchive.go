package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// ReportArchive subsystem (SPEC §9.8, CMP-Reports parity): the scheduler
// renders a report once per due slot and stores the bytes here so they can
// be downloaded later. Retention is a per-report count (Report.Keep,
// default DefaultReportKeep) of distinct slots — the oldest beyond the
// budget are pruned on insert.

// DefaultReportKeep is the archive retention when Report.Keep is unset.
const DefaultReportKeep = 12

// ReportArchiveEntry is one archived render. Content is nil in list
// responses (List omits the BLOB); Get returns it populated.
type ReportArchiveEntry struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId"`
	ReportName string    `json:"reportName"`
	Slot       string    `json:"slot"`
	Format     string    `json:"format"`
	Content    []byte    `json:"-"`
	CreatedAt  time.Time `json:"createdAt"`
}

// PutReportArchive inserts one rendered report and prunes the report's
// archive down to keep distinct slots (oldest first). keep <= 0 falls back
// to DefaultReportKeep. The insert + prune run in one write transaction so
// a concurrent reader never sees the table mid-prune.
func (s *Store) PutReportArchive(ctx context.Context, e *ReportArchiveEntry, keep int) error {
	if e.ID == "" {
		e.ID = model.NewID()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	if keep <= 0 {
		keep = DefaultReportKeep
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO report_archive (id, tenant_id, report_name, slot, format, content, created_at)
			 VALUES (?,?,?,?,?,?,?)`),
			e.ID, e.TenantID, e.ReportName, e.Slot, e.Format, e.Content, s.T(e.CreatedAt)); err != nil {
			return err
		}
		return pruneReportArchive(ctx, tx, s, e.TenantID, e.ReportName, keep)
	})
}

// pruneReportArchive deletes everything older than the keep-th most recent
// slot. Retention counts distinct slots (a daily HTML+CSV pair is one
// slot), so all formats of a kept slot survive together.
func pruneReportArchive(ctx context.Context, tx *sql.Tx, s *Store, tenantID, reportName string, keep int) error {
	// newest distinct slots, capped at keep+1 so we learn the cutoff
	rows, err := tx.QueryContext(ctx, s.Q(
		`SELECT slot, MAX(created_at) AS m FROM report_archive
		 WHERE tenant_id = ? AND report_name = ?
		 GROUP BY slot ORDER BY m DESC LIMIT ?`),
		tenantID, reportName, keep+1)
	if err != nil {
		return err
	}
	slots := make([]string, 0, keep+1)
	for rows.Next() {
		var slot string
		var m ScanTime
		if err := rows.Scan(&slot, &m); err != nil {
			rows.Close()
			return err
		}
		slots = append(slots, slot)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(slots) <= keep {
		return nil // within budget
	}
	// the slot at index keep is the newest one to drop; delete it and any
	// older — by created_at of that slot's newest row as the cutoff.
	cutoff := slots[keep]
	_, err = tx.ExecContext(ctx, s.Q(
		`DELETE FROM report_archive
		 WHERE tenant_id = ? AND report_name = ?
		 AND created_at <= (
			SELECT MAX(created_at) FROM report_archive
			WHERE tenant_id = ? AND report_name = ? AND slot = ?)`),
		tenantID, reportName, tenantID, reportName, cutoff)
	return err
}

// HasReportArchiveSlot reports whether the report already has a render for
// the given slot (the scheduler's dedup gate — one render per slot).
func (s *Store) HasReportArchiveSlot(ctx context.Context, tenantID, reportName, slot string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, s.Q(
		`SELECT 1 FROM report_archive
		 WHERE tenant_id = ? AND report_name = ? AND slot = ? LIMIT 1`),
		tenantID, reportName, slot).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ListReportArchive returns metadata (no content) newest-first.
func (s *Store) ListReportArchive(ctx context.Context, tenantID, reportName string, limit int) ([]*ReportArchiveEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT id, tenant_id, report_name, slot, format, created_at
		 FROM report_archive WHERE tenant_id = ? AND report_name = ?
		 ORDER BY created_at DESC LIMIT `+fmt.Sprint(limit)),
		tenantID, reportName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ReportArchiveEntry
	for rows.Next() {
		var e ReportArchiveEntry
		var created ScanTime
		if err := rows.Scan(&e.ID, &e.TenantID, &e.ReportName, &e.Slot, &e.Format, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = created.T
		out = append(out, &e)
	}
	return out, rows.Err()
}

// GetReportArchive fetches one entry with its content (download path).
func (s *Store) GetReportArchive(ctx context.Context, tenantID, id string) (*ReportArchiveEntry, error) {
	var e ReportArchiveEntry
	var created ScanTime
	err := s.db.QueryRowContext(ctx, s.Q(
		`SELECT id, tenant_id, report_name, slot, format, content, created_at
		 FROM report_archive WHERE tenant_id = ? AND id = ?`),
		tenantID, id).Scan(&e.ID, &e.TenantID, &e.ReportName, &e.Slot, &e.Format, &e.Content, &created)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	e.CreatedAt = created.T
	return &e, nil
}

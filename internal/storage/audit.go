package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// Audit hash chain (SPEC §13.5): hash = SHA256(prev_hash ‖ canonical row).
// The chain head is cached in memory; appends are serialised by auditMu
// so the chain stays linear even with concurrent writers.

const auditGenesis = "0000000000000000000000000000000000000000000000000000000000000000"

func (s *Store) loadAuditHead(ctx context.Context) error {
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT hash FROM audit_log ORDER BY seq DESC LIMIT 1`).Scan(&hash)
	switch err {
	case nil:
		s.auditLastHash = hash
	case sql.ErrNoRows:
		s.auditLastHash = auditGenesis
	default:
		return err
	}
	return nil
}

func auditRowHash(prev string, e *model.AuditEntry) string {
	h := sha256.New()
	h.Write([]byte(prev))
	// Canonical serialisation: fixed field order, unit separator.
	fields := []string{
		e.TS.UTC().Format(time.RFC3339Nano), e.TenantID, string(e.ActorType), e.ActorID,
		e.Action, e.Resource, e.SourceIP, e.RequestID, e.BeforeJSON, e.AfterJSON,
	}
	for _, f := range fields {
		h.Write([]byte(f))
		h.Write([]byte{0x1f})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// AppendAudit links a new entry into the chain and returns it with
// seq/hash filled.
func (s *Store) AppendAudit(ctx context.Context, e *model.AuditEntry) (*model.AuditEntry, error) {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	e.PrevHash = s.auditLastHash
	e.Hash = auditRowHash(e.PrevHash, e)

	err := s.Write(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, s.Q(
			`INSERT INTO audit_log (ts, tenant_id, actor_type, actor_id, action, resource,
			 source_ip, request_id, before_json, after_json, prev_hash, hash)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?) RETURNING seq`),
			s.T(e.TS), S(e.TenantID), string(e.ActorType), e.ActorID, e.Action, S(e.Resource),
			e.SourceIP, e.RequestID, S(e.BeforeJSON), S(e.AfterJSON), e.PrevHash, e.Hash)
		return row.Scan(&e.Seq)
	})
	if err != nil {
		return nil, err
	}
	s.auditLastHash = e.Hash
	return e, nil
}

const auditCols = `seq, ts, tenant_id, actor_type, actor_id, action, resource,
	source_ip, request_id, before_json, after_json, prev_hash, hash`

func scanAudit(sc interface{ Scan(...any) error }) (*model.AuditEntry, error) {
	var e model.AuditEntry
	var ts ScanTime
	var tenant, resource, before, after NullStr
	if err := sc.Scan(&e.Seq, &ts, &tenant, (*string)(&e.ActorType), &e.ActorID,
		&e.Action, &resource, &e.SourceIP, &e.RequestID, &before, &after,
		&e.PrevHash, &e.Hash); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	e.TS = ts.T
	e.TenantID, e.Resource = string(tenant), string(resource)
	e.BeforeJSON, e.AfterJSON = string(before), string(after)
	return &e, nil
}

// AuditFilter narrows the audit browser query.
type AuditFilter struct {
	TenantID  string // empty = all (system scope)
	ActorID   string
	ActorType string
	Action    string // prefix match ("host." …)
	Resource  string
	From, To  time.Time
	AfterSeq  int64
	Limit     int
	Asc       bool
}

// QueryAudit lists entries (default newest first).
func (s *Store) QueryAudit(ctx context.Context, f AuditFilter) ([]*model.AuditEntry, error) {
	if f.Limit <= 0 || f.Limit > 5000 {
		f.Limit = 200
	}
	var conds []string
	var args []any
	if f.TenantID != "" {
		conds = append(conds, "tenant_id = ?")
		args = append(args, f.TenantID)
	}
	if f.ActorID != "" {
		conds = append(conds, "actor_id = ?")
		args = append(args, f.ActorID)
	}
	if f.ActorType != "" {
		conds = append(conds, "actor_type = ?")
		args = append(args, f.ActorType)
	}
	if f.Action != "" {
		conds = append(conds, "action LIKE ?")
		args = append(args, f.Action+"%")
	}
	if f.Resource != "" {
		conds = append(conds, "resource = ?")
		args = append(args, f.Resource)
	}
	if !f.From.IsZero() {
		conds = append(conds, "ts >= ?")
		args = append(args, s.T(f.From))
	}
	if !f.To.IsZero() {
		conds = append(conds, "ts < ?")
		args = append(args, s.T(f.To))
	}
	if f.AfterSeq > 0 {
		if f.Asc {
			conds = append(conds, "seq > ?")
		} else {
			conds = append(conds, "seq < ?")
		}
		args = append(args, f.AfterSeq)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	order := "DESC"
	if f.Asc {
		order = "ASC"
	}
	rows, err := s.db.QueryContext(ctx, s.Q(fmt.Sprintf(
		`SELECT %s FROM audit_log %s ORDER BY seq %s LIMIT %d`, auditCols, where, order, f.Limit)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.AuditEntry
	for rows.Next() {
		e, err := scanAudit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// VerifyAudit walks the chain and recomputes hashes (np audit verify).
// Returns the number of verified entries; err describes the first break.
func (s *Store) VerifyAudit(ctx context.Context) (int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+auditCols+` FROM audit_log ORDER BY seq ASC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	prev := auditGenesis
	var n int64
	for rows.Next() {
		e, err := scanAudit(rows)
		if err != nil {
			return n, err
		}
		if e.PrevHash != prev {
			return n, fmt.Errorf("audit chain broken at seq %d: prev_hash mismatch", e.Seq)
		}
		if want := auditRowHash(prev, e); want != e.Hash {
			return n, fmt.Errorf("audit chain broken at seq %d: row hash mismatch", e.Seq)
		}
		prev = e.Hash
		n++
	}
	return n, rows.Err()
}

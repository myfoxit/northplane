package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/selector"
)

func jsonMarshal(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// CreateObject inserts a host/service plus its label index rows.
func (s *Store) CreateObject(ctx context.Context, o *model.Object) error {
	if o.ID == "" {
		o.ID = model.NewID()
	}
	now := time.Now().UTC()
	o.CreatedAt, o.UpdatedAt, o.Version = now, now, 1
	labels, _ := jsonMarshal(orLabels(o.Labels))
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO objects (id, tenant_id, kind, name, host_id, folder, labels, spec, version, created_at, updated_at)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?)`),
			o.ID, o.TenantID, string(o.Kind), o.Name, S(o.HostID), orSlash(o.Folder),
			labels, model.MarshalSpec(o.Spec), o.Version, s.T(now), s.T(now))
		if err != nil {
			if s.dialect.IsDuplicate(err) {
				return fmt.Errorf("%w: %s %q", ErrDuplicate, o.Kind, o.Name)
			}
			return err
		}
		if err := writeLabels(ctx, tx, s, o.ID, o.Labels); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, s.Q(
			`INSERT INTO check_state (object_id, state, state_type, attempt) VALUES (?,3,'hard',1)`), o.ID)
		return err
	})
}

func orSlash(f string) string {
	if f == "" {
		return "/"
	}
	return f
}

func orLabels(l model.Labels) model.Labels {
	if l == nil {
		return model.Labels{}
	}
	return l
}

func writeLabels(ctx context.Context, tx *sql.Tx, s *Store, objectID string, labels model.Labels) error {
	if _, err := tx.ExecContext(ctx, s.Q(`DELETE FROM object_labels WHERE object_id = ?`), objectID); err != nil {
		return err
	}
	for k, v := range labels {
		if _, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO object_labels (object_id, k, v) VALUES (?,?,?)`), objectID, k, v); err != nil {
			return err
		}
	}
	return nil
}

// UpdateObject applies optimistic locking: expectVersion 0 skips the check.
func (s *Store) UpdateObject(ctx context.Context, o *model.Object, expectVersion int64) error {
	now := time.Now().UTC()
	labels, _ := jsonMarshal(orLabels(o.Labels))
	return s.Write(ctx, func(tx *sql.Tx) error {
		cond, args := "id = ? AND tenant_id = ?", []any{}
		args = append(args, labels, model.MarshalSpec(o.Spec), orSlash(o.Folder), s.T(now), o.ID, o.TenantID)
		if expectVersion > 0 {
			cond += " AND version = ?"
			args = append(args, expectVersion)
		}
		res, err := tx.ExecContext(ctx, s.Q(
			`UPDATE objects SET labels = ?, spec = ?, folder = ?, version = version + 1, updated_at = ? WHERE `+cond), args...)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			// Distinguish missing from conflict for correct HTTP codes.
			var v int64
			err := tx.QueryRowContext(ctx, s.Q(`SELECT version FROM objects WHERE id = ?`), o.ID).Scan(&v)
			if err == sql.ErrNoRows {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: have %d, expected %d", ErrConflict, v, expectVersion)
		}
		o.Version = expectVersion + 1
		o.UpdatedAt = now
		return writeLabels(ctx, tx, s, o.ID, o.Labels)
	})
}

// DeleteObject removes the object (cascades to labels, state, services).
func (s *Store) DeleteObject(ctx context.Context, tenantID, id string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		// Manual cascade for service children on backends without FK
		// enforcement surprises; FKs also handle it, this is belt+braces.
		if _, err := tx.ExecContext(ctx, s.Q(
			`DELETE FROM objects WHERE tenant_id = ? AND host_id = ?`), tenantID, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, s.Q(
			`DELETE FROM objects WHERE tenant_id = ? AND id = ?`), tenantID, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

const objectCols = `id, tenant_id, kind, name, host_id, folder, labels, spec, version, created_at, updated_at`

func scanObject(sc interface{ Scan(...any) error }) (*model.Object, error) {
	var o model.Object
	var hostID NullStr
	var labels, spec string
	var created, updated ScanTime
	if err := sc.Scan(&o.ID, &o.TenantID, (*string)(&o.Kind), &o.Name, &hostID,
		&o.Folder, &labels, &spec, &o.Version, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	o.HostID = string(hostID)
	o.CreatedAt, o.UpdatedAt = created.T, updated.T
	_ = json.Unmarshal([]byte(labels), &o.Labels)
	var err error
	o.Spec, err = model.UnmarshalSpec(spec)
	return &o, err
}

// GetObject by ID.
func (s *Store) GetObject(ctx context.Context, tenantID, id string) (*model.Object, error) {
	row := s.db.QueryRowContext(ctx, s.Q(
		`SELECT `+objectCols+` FROM objects WHERE tenant_id = ? AND id = ?`), tenantID, id)
	return scanObject(row)
}

// GetObjectByName resolves hosts by name; services by (hostID, name).
func (s *Store) GetObjectByName(ctx context.Context, tenantID string, kind model.Kind, hostID, name string) (*model.Object, error) {
	row := s.db.QueryRowContext(ctx, s.Q(
		`SELECT `+objectCols+` FROM objects
		 WHERE tenant_id = ? AND kind = ? AND COALESCE(host_id,'') = ? AND name = ?`),
		tenantID, string(kind), hostID, name)
	return scanObject(row)
}

// ObjectFilter narrows listing.
type ObjectFilter struct {
	TenantID string
	Kind     model.Kind // "" = both
	HostID   string
	Folder   string // subtree prefix
	Selector selector.Selector
	Query    string // substring on name/address
	Cursor   string
	Limit    int
}

// ListObjects applies filters; selector terms are pushed into SQL via
// the object_labels index (SPEC §6.5), the rest matches in Go.
func (s *Store) ListObjects(ctx context.Context, f ObjectFilter) ([]*model.Object, error) {
	if f.Limit <= 0 || f.Limit > 5000 {
		f.Limit = 500
	}
	var conds []string
	var args []any
	conds = append(conds, "tenant_id = ?")
	args = append(args, f.TenantID)
	if f.Kind != "" {
		conds = append(conds, "kind = ?")
		args = append(args, string(f.Kind))
	}
	if f.HostID != "" {
		conds = append(conds, "host_id = ?")
		args = append(args, f.HostID)
	}
	if f.Folder != "" && f.Folder != "/" {
		conds = append(conds, "(folder = ? OR folder LIKE ?)")
		args = append(args, f.Folder, strings.TrimSuffix(f.Folder, "/")+"/%")
	}
	if f.Query != "" {
		conds = append(conds, "(name LIKE ? OR spec LIKE ?)")
		like := "%" + f.Query + "%"
		args = append(args, like, like)
	}
	if f.Cursor != "" {
		conds = append(conds, "id > ?")
		args = append(args, f.Cursor)
	}
	// Push equality/exists selector terms into the label index.
	for i, r := range f.Selector.Requirements() {
		alias := fmt.Sprintf("l%d", i)
		switch r.Op {
		case selector.OpEq:
			conds = append(conds, fmt.Sprintf(
				`EXISTS (SELECT 1 FROM object_labels %s WHERE %s.object_id = objects.id AND %s.k = ? AND %s.v = ?)`,
				alias, alias, alias, alias))
			args = append(args, r.Key, r.Values[0])
		case selector.OpIn:
			ph := strings.TrimSuffix(strings.Repeat("?,", len(r.Values)), ",")
			conds = append(conds, fmt.Sprintf(
				`EXISTS (SELECT 1 FROM object_labels %s WHERE %s.object_id = objects.id AND %s.k = ? AND %s.v IN (%s))`,
				alias, alias, alias, alias, ph))
			args = append(args, r.Key)
			for _, v := range r.Values {
				args = append(args, v)
			}
		case selector.OpExists:
			conds = append(conds, fmt.Sprintf(
				`EXISTS (SELECT 1 FROM object_labels %s WHERE %s.object_id = objects.id AND %s.k = ?)`,
				alias, alias, alias))
			args = append(args, r.Key)
		case selector.OpNotExists:
			conds = append(conds, fmt.Sprintf(
				`NOT EXISTS (SELECT 1 FROM object_labels %s WHERE %s.object_id = objects.id AND %s.k = ?)`,
				alias, alias, alias))
			args = append(args, r.Key)
		case selector.OpNeq, selector.OpNotIn:
			// negations with absent-key semantics filter in Go below
		}
	}
	query := `SELECT ` + objectCols + ` FROM objects WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY id LIMIT ` + fmt.Sprint(f.Limit+1)
	rows, err := s.db.QueryContext(ctx, s.Q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Object
	for rows.Next() {
		o, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		if !f.Selector.Matches(o.Labels) {
			continue // residual terms (!=, notin)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// CountObjects for overview tiles.
func (s *Store) CountObjects(ctx context.Context, tenantID string, kind model.Kind) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, s.Q(
		`SELECT COUNT(*) FROM objects WHERE tenant_id = ? AND kind = ?`),
		tenantID, string(kind)).Scan(&n)
	return n, err
}

// --- check_state ---

const stateCols = `object_id, state, state_type, attempt, output, long_output, perfdata,
	latency_ms, exec_ms, last_check, next_check, last_hard_change, last_ok,
	flapping, flap_pct, flap_history, acked_by, ack_comment, downtime_depth`

func scanState(sc interface{ Scan(...any) error }) (*model.CheckState, error) {
	var cs model.CheckState
	var lastCheck, nextCheck, lastHard, lastOK ScanTime
	var flapHistory int64
	if err := sc.Scan(&cs.ObjectID, &cs.State, (*string)(&cs.StateType), &cs.Attempt,
		&cs.Output, &cs.LongOutput, &cs.Perfdata, &cs.LatencyMS, &cs.ExecMS,
		&lastCheck, &nextCheck, &lastHard, &lastOK,
		&cs.Flapping, &cs.FlapPct, &flapHistory, &cs.AckedBy, &cs.AckComment,
		&cs.DowntimeDepth); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	cs.LastCheck, cs.NextCheck = lastCheck.Ptr(), nextCheck.Ptr()
	cs.LastHardChange, cs.LastOK = lastHard.Ptr(), lastOK.Ptr()
	cs.FlapHistory = uint32(flapHistory)
	return &cs, nil
}

// GetCheckState returns the hot state row.
func (s *Store) GetCheckState(ctx context.Context, objectID string) (*model.CheckState, error) {
	row := s.db.QueryRowContext(ctx, s.Q(
		`SELECT `+stateCols+` FROM check_state WHERE object_id = ?`), objectID)
	return scanState(row)
}

// SaveCheckStates upserts a batch in one transaction (pipeline batch
// commits, SPEC §7.4).
func (s *Store) SaveCheckStates(ctx context.Context, states []*model.CheckState) error {
	if len(states) == 0 {
		return nil
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		q := s.Q(`INSERT INTO check_state (object_id, state, state_type, attempt, output, long_output,
			perfdata, latency_ms, exec_ms, last_check, next_check, last_hard_change, last_ok,
			flapping, flap_pct, flap_history, acked_by, ack_comment, downtime_depth)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT (object_id) DO UPDATE SET
			state = excluded.state, state_type = excluded.state_type, attempt = excluded.attempt,
			output = excluded.output, long_output = excluded.long_output, perfdata = excluded.perfdata,
			latency_ms = excluded.latency_ms, exec_ms = excluded.exec_ms,
			last_check = excluded.last_check, next_check = excluded.next_check,
			last_hard_change = excluded.last_hard_change, last_ok = excluded.last_ok,
			flapping = excluded.flapping, flap_pct = excluded.flap_pct, flap_history = excluded.flap_history,
			acked_by = excluded.acked_by, ack_comment = excluded.ack_comment,
			downtime_depth = excluded.downtime_depth`)
		for _, cs := range states {
			if _, err := tx.ExecContext(ctx, q, cs.ObjectID, int(cs.State), string(cs.StateType),
				cs.Attempt, cs.Output, cs.LongOutput, cs.Perfdata, cs.LatencyMS, cs.ExecMS,
				s.TP(cs.LastCheck), s.TP(cs.NextCheck), s.TP(cs.LastHardChange), s.TP(cs.LastOK),
				cs.Flapping, cs.FlapPct, int64(cs.FlapHistory), cs.AckedBy, cs.AckComment,
				cs.DowntimeDepth); err != nil {
				return err
			}
		}
		return nil
	})
}

// ProblemRow joins object + state for the Problems view.
type ProblemRow struct {
	Object *model.Object     `json:"object"`
	State  *model.CheckState `json:"state"`
}

// ListProblems returns hard non-OK states with their objects, worst &
// oldest first (SPEC §12.3 Problems view), unhandled before handled.
func (s *Store) ListProblems(ctx context.Context, tenantID string, includeHandled bool, limit int) ([]*ProblemRow, error) {
	if limit <= 0 {
		limit = 500
	}
	cond := ""
	if !includeHandled {
		cond = ` AND cs.acked_by = '' AND cs.downtime_depth = 0`
	}
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT `+prefixCols("o", objectCols)+`, `+prefixCols("cs", stateCols)+`
		 FROM check_state cs JOIN objects o ON o.id = cs.object_id
		 WHERE o.tenant_id = ? AND cs.state != 0 AND cs.state_type = 'hard'`+cond+`
		 ORDER BY cs.state DESC, cs.last_hard_change ASC LIMIT `+fmt.Sprint(limit)), tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ProblemRow
	for rows.Next() {
		o := &model.Object{}
		cs := &model.CheckState{}
		var hostID NullStr
		var labels, spec string
		var created, updated, lastCheck, nextCheck, lastHard, lastOK ScanTime
		var flapHistory int64
		if err := rows.Scan(&o.ID, &o.TenantID, (*string)(&o.Kind), &o.Name, &hostID,
			&o.Folder, &labels, &spec, &o.Version, &created, &updated,
			&cs.ObjectID, &cs.State, (*string)(&cs.StateType), &cs.Attempt, &cs.Output,
			&cs.LongOutput, &cs.Perfdata, &cs.LatencyMS, &cs.ExecMS,
			&lastCheck, &nextCheck, &lastHard, &lastOK,
			&cs.Flapping, &cs.FlapPct, &flapHistory, &cs.AckedBy, &cs.AckComment,
			&cs.DowntimeDepth); err != nil {
			return nil, err
		}
		o.HostID = string(hostID)
		o.CreatedAt, o.UpdatedAt = created.T, updated.T
		_ = json.Unmarshal([]byte(labels), &o.Labels)
		o.Spec, _ = model.UnmarshalSpec(spec)
		cs.LastCheck, cs.NextCheck = lastCheck.Ptr(), nextCheck.Ptr()
		cs.LastHardChange, cs.LastOK = lastHard.Ptr(), lastOK.Ptr()
		cs.FlapHistory = uint32(flapHistory)
		out = append(out, &ProblemRow{Object: o, State: cs})
	}
	return out, rows.Err()
}

func prefixCols(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// StateSummary aggregates counts for overview tiles.
type StateSummary struct {
	HostsUp          int64 `json:"hostsUp"`
	HostsDown        int64 `json:"hostsDown"`
	HostsUnreachable int64 `json:"hostsUnreachable"`
	ServicesOK       int64 `json:"servicesOk"`
	ServicesWarning  int64 `json:"servicesWarning"`
	ServicesCritical int64 `json:"servicesCritical"`
	ServicesUnknown  int64 `json:"servicesUnknown"`
	Acked            int64 `json:"acked"`
	InDowntime       int64 `json:"inDowntime"`
	Flapping         int64 `json:"flapping"`
}

// Summary computes the wallboard counters in one pass.
func (s *Store) Summary(ctx context.Context, tenantID string) (*StateSummary, error) {
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT o.kind, cs.state,
		        SUM(CASE WHEN cs.acked_by != '' THEN 1 ELSE 0 END),
		        SUM(CASE WHEN cs.downtime_depth > 0 THEN 1 ELSE 0 END),
		        SUM(CASE WHEN cs.flapping THEN 1 ELSE 0 END),
		        COUNT(*)
		 FROM check_state cs JOIN objects o ON o.id = cs.object_id
		 WHERE o.tenant_id = ? GROUP BY o.kind, cs.state`), tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sum := &StateSummary{}
	for rows.Next() {
		var kind string
		var state int
		var acked, down, flap, count int64
		if err := rows.Scan(&kind, &state, &acked, &down, &flap, &count); err != nil {
			return nil, err
		}
		sum.Acked += acked
		sum.InDowntime += down
		sum.Flapping += flap
		if kind == "host" {
			switch model.State(state) {
			case model.HostUp:
				sum.HostsUp += count
			case model.HostDown:
				sum.HostsDown += count
			default:
				sum.HostsUnreachable += count
			}
		} else {
			switch model.State(state) {
			case model.StateOK:
				sum.ServicesOK += count
			case model.StateWarning:
				sum.ServicesWarning += count
			case model.StateCritical:
				sum.ServicesCritical += count
			default:
				sum.ServicesUnknown += count
			}
		}
	}
	return sum, rows.Err()
}

// DueChecks returns objects whose next_check is due (scheduler recovery
// path after restart; live scheduling keeps its own wheel).
func (s *Store) DueChecks(ctx context.Context, before time.Time, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT object_id FROM check_state WHERE next_check IS NOT NULL AND next_check <= ?
		 ORDER BY next_check LIMIT `+fmt.Sprint(limit)), s.T(before))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

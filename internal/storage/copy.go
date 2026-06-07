package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CopyAll migrates the relational dataset between backends (SPEC §7.3:
// `northplaned storage migrate --to <dsn>` — offline, downtime = copy
// time; the NP-TSDB is backend-independent and untouched). Both stores
// must be migrated to the same schema version (Open guarantees that).
func CopyAll(ctx context.Context, src, dst *Store) (int64, error) {
	// generic tables: identical logical columns in both dialects
	tables := []string{
		"tenants", "users", "objects", "object_labels", "check_state",
		"alerts", "incidents", "resources", "downtimes", "silences",
		"heartbeats", "api_tokens", "sessions", "secrets", "idempotency",
		"escalations", "outbox", "ai_actions", "ai_conversations",
		"ai_usage", "push_subscriptions", "report_archive", "kv",
	}
	var total int64
	for _, table := range tables {
		n, err := copyTable(ctx, src, dst, table)
		if err != nil {
			return total, fmt.Errorf("table %s: %w", table, err)
		}
		total += n
	}
	// audit_log: preserve seq + chain exactly (hash chain stays valid)
	n, err := copyAudit(ctx, src, dst)
	if err != nil {
		return total, fmt.Errorf("audit_log: %w", err)
	}
	total += n
	// events: read across partitions ascending, insert into target
	n, err = copyEvents(ctx, src, dst)
	if err != nil {
		return total, fmt.Errorf("events: %w", err)
	}
	total += n
	return total, nil
}

// copyTable streams all rows of one table.
func copyTable(ctx context.Context, src, dst *Store, table string) (int64, error) {
	rows, err := src.db.QueryContext(ctx, `SELECT * FROM `+table)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return 0, err
	}
	// Destination column types: needed to coerce SQLite's INTEGER-backed
	// booleans into real bools for PostgreSQL (pgx rejects int→boolean).
	dstBool, err := boolColumns(ctx, dst, table)
	if err != nil {
		return 0, err
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	// ON CONFLICT DO NOTHING: the destination Open() already seeded the
	// default tenant and builtin roles, so a plain INSERT of the source's
	// identical rows would hit a UNIQUE violation on the first table.
	insert := dst.Q(`INSERT INTO ` + table + ` (` + strings.Join(cols, ",") +
		`) VALUES (` + placeholders + `) ON CONFLICT DO NOTHING`)

	var n int64
	err = dst.Write(ctx, func(tx *sql.Tx) error {
		for rows.Next() {
			raw := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range raw {
				ptrs[i] = &raw[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			vals := make([]any, len(cols))
			for i, v := range raw {
				vals[i] = convertValue(v, types[i].DatabaseTypeName(), dstBool[cols[i]], dst.dialect)
			}
			if _, err := tx.ExecContext(ctx, insert, vals...); err != nil {
				return err
			}
			n++
		}
		return rows.Err()
	})
	return n, err
}

// boolColumns reports which columns of a destination table are boolean,
// keyed by name, by inspecting an empty result set's column types.
func boolColumns(ctx context.Context, dst *Store, table string) (map[string]bool, error) {
	rows, err := dst.db.QueryContext(ctx, `SELECT * FROM `+table+` WHERE 1=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(cols))
	for i, c := range cols {
		t := strings.ToUpper(types[i].DatabaseTypeName())
		out[c] = strings.HasPrefix(t, "BOOL")
	}
	return out, nil
}

// convertValue bridges dialect representations (RFC3339 text ↔
// timestamptz, int ↔ bool).
func convertValue(v any, typeName string, dstIsBool bool, dst Dialect) any {
	if dstIsBool {
		// SQLite stores booleans as INTEGER 0/1 → real bool for the dest.
		switch x := v.(type) {
		case int64:
			return x != 0
		case bool:
			return x
		case nil:
			return nil
		}
	}
	switch x := v.(type) {
	case []byte:
		if looksTimestamp(typeName) {
			if t, err := time.Parse(time.RFC3339Nano, string(x)); err == nil {
				return dst.TimeValue(t)
			}
		}
		return x
	case string:
		if looksTimestamp(typeName) {
			if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
				return dst.TimeValue(t)
			}
		}
		return x
	case time.Time:
		return dst.TimeValue(x)
	case bool:
		// Destination is integer-backed (SQLite): store 0/1.
		if dst.Name() != "postgres" {
			if x {
				return int64(1)
			}
			return int64(0)
		}
		return x
	default:
		return v
	}
}

func looksTimestamp(typeName string) bool {
	t := strings.ToUpper(typeName)
	return strings.Contains(t, "TIMESTAMP")
}

// copyAudit preserves sequence numbers (identity/autoincrement columns
// need explicit handling per dialect).
func copyAudit(ctx context.Context, src, dst *Store) (int64, error) {
	rows, err := src.db.QueryContext(ctx, `SELECT `+auditCols+` FROM audit_log ORDER BY seq`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	insert := `INSERT INTO audit_log (seq, ts, tenant_id, actor_type, actor_id, action,
		resource, source_ip, request_id, before_json, after_json, prev_hash, hash)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`
	if dst.dialect.Name() == "postgres" {
		insert = `INSERT INTO audit_log (seq, ts, tenant_id, actor_type, actor_id, action,
		resource, source_ip, request_id, before_json, after_json, prev_hash, hash)
		OVERRIDING SYSTEM VALUE VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`
	}
	insert = dst.Q(insert)
	var n int64
	err = dst.Write(ctx, func(tx *sql.Tx) error {
		for rows.Next() {
			e, err := scanAudit(rows)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, insert, e.Seq, dst.T(e.TS), S(e.TenantID),
				string(e.ActorType), e.ActorID, e.Action, S(e.Resource), e.SourceIP,
				e.RequestID, S(e.BeforeJSON), S(e.AfterJSON), e.PrevHash, e.Hash); err != nil {
				return err
			}
			n++
		}
		return rows.Err()
	})
	if err != nil {
		return n, err
	}
	// resync the identity sequence on PG
	if dst.dialect.Name() == "postgres" && n > 0 {
		_, err = dst.db.ExecContext(ctx,
			`SELECT setval(pg_get_serial_sequence('audit_log','seq'), (SELECT MAX(seq) FROM audit_log))`)
	}
	return n, err
}

// copyEvents pages each tenant's events oldest-first into the target's
// partitioning scheme.
func copyEvents(ctx context.Context, src, dst *Store) (int64, error) {
	var total int64
	tenants, err := src.Tenants(ctx)
	if err != nil {
		return 0, err
	}
	for _, t := range tenants {
		cursor := ""
		for {
			// Page by UUIDv7 id cursor, not by advancing the timestamp:
			// the old +1ms advance skipped any events sharing the boundary
			// millisecond beyond the page size.
			events, err := src.QueryEvents(ctx, EventFilter{
				TenantID: t.ID, Cursor: cursor, Limit: 1000, Asc: true})
			if err != nil {
				return total, err
			}
			if len(events) == 0 {
				break
			}
			if err := dst.InsertEvents(ctx, events); err != nil {
				return total, err
			}
			total += int64(len(events))
			cursor = events[len(events)-1].ID
			if len(events) < 1000 {
				break
			}
		}
	}
	return total, nil
}

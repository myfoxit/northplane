package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// eventStore implements time-partitioned event persistence (ADR-13).
//
// SQLite: one segment *file* per month (events-YYYYMM.db), each with its
// own handle and single-writer discipline. Cross-month queries fan out
// per segment and merge in Go (events are always consumed ordered by ts
// with a limit, so a K-way merge over pre-sorted streams is exact).
// Retention = close + delete file: O(1), no DELETE/VACUUM churn, and
// report reads on cold segments never touch the hot file's WAL.
//
// PostgreSQL: native monthly range partitions on the parent table;
// retention = DROP TABLE on the child partition.
type eventStore struct {
	store   *Store
	dataDir string
	keep    int // months, 0 = forever

	mu       sync.Mutex
	segments map[string]*sql.DB // "200601" → handle (sqlite only)
	ensured  map[string]bool    // partitions known to exist (pg)
}

const eventDDL = `CREATE TABLE IF NOT EXISTS events (
	id        TEXT PRIMARY KEY,
	tenant_id TEXT NOT NULL,
	ts        {{TIMESTAMP}} NOT NULL,
	type      TEXT NOT NULL,
	object_id TEXT,
	source_id TEXT,
	severity  TEXT,
	payload   {{JSON}} NOT NULL
)`

func newEventStore(ctx context.Context, s *Store, dataDir string, keep int) (*eventStore, error) {
	es := &eventStore{
		store: s, dataDir: dataDir, keep: keep,
		segments: map[string]*sql.DB{}, ensured: map[string]bool{},
	}
	if s.dialect.Name() == "postgres" {
		if _, err := s.db.ExecContext(ctx, s.dialect.DDL(
			`CREATE TABLE IF NOT EXISTS events (
				id        TEXT NOT NULL,
				tenant_id TEXT NOT NULL,
				ts        {{TIMESTAMP}} NOT NULL,
				type      TEXT NOT NULL,
				object_id TEXT,
				source_id TEXT,
				severity  TEXT,
				payload   {{JSON}} NOT NULL,
				PRIMARY KEY (id, ts)
			) PARTITION BY RANGE (ts)`)); err != nil {
			return nil, fmt.Errorf("storage: events parent: %w", err)
		}
		if err := es.ensurePartition(ctx, time.Now().UTC()); err != nil {
			return nil, err
		}
		return es, nil
	}
	// SQLite: discover existing segments.
	if dataDir != "" {
		matches, _ := filepath.Glob(filepath.Join(dataDir, "events-*.db"))
		for _, m := range matches {
			key := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "events-"), ".db")
			if len(key) == 6 {
				if _, err := es.segment(key, false); err != nil {
					return nil, err
				}
			}
		}
	}
	if _, err := es.segment(monthKey(time.Now().UTC()), true); err != nil {
		return nil, err
	}
	return es, nil
}

func monthKey(t time.Time) string { return t.UTC().Format("200601") }

func (es *eventStore) segmentPath(key string) string {
	return filepath.Join(es.dataDir, "events-"+key+".db")
}

// segment opens (or returns) the handle for a month. create=false only
// opens existing files.
func (es *eventStore) segment(key string, create bool) (*sql.DB, error) {
	es.mu.Lock()
	defer es.mu.Unlock()
	if db, ok := es.segments[key]; ok {
		return db, nil
	}
	path := es.segmentPath(key)
	if !create {
		if _, err := os.Stat(path); err != nil {
			return nil, nil // absent month
		}
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	d := sqliteDialect{}
	if _, err := db.Exec(d.DDL(eventDDL)); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS events_ts ON events (tenant_id, ts)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS events_obj ON events (object_id, ts)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	es.segments[key] = db
	return db, nil
}

func (es *eventStore) ensurePartition(ctx context.Context, t time.Time) error {
	key := monthKey(t)
	es.mu.Lock()
	done := es.ensured[key]
	es.mu.Unlock()
	if done {
		return nil
	}
	from := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	stmt := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS events_%s PARTITION OF events
		 FOR VALUES FROM ('%s') TO ('%s')`,
		key, from.Format(time.RFC3339), to.Format(time.RFC3339))
	if _, err := es.store.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("storage: partition %s: %w", key, err)
	}
	idx := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS events_%s_ts ON events_%s (tenant_id, ts)`, key, key)
	if _, err := es.store.db.ExecContext(ctx, idx); err != nil {
		return err
	}
	// Parity with the SQLite segments (events_obj above): per-object lookups
	// (QueryEvents with ObjectID) otherwise scan the whole partition on PG.
	objIdx := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS events_%s_obj ON events_%s (object_id, ts)`, key, key)
	if _, err := es.store.db.ExecContext(ctx, objIdx); err != nil {
		return err
	}
	es.mu.Lock()
	es.ensured[key] = true
	es.mu.Unlock()
	return nil
}

func (es *eventStore) close() {
	es.mu.Lock()
	defer es.mu.Unlock()
	for _, db := range es.segments {
		_ = db.Close() // shutdown teardown; nothing actionable on error
	}
	es.segments = map[string]*sql.DB{}
}

// InsertEvents appends a batch (single transaction per segment).
func (s *Store) InsertEvents(ctx context.Context, events []*model.Event) error {
	if len(events) == 0 {
		return nil
	}
	if s.dialect.Name() == "postgres" {
		for _, e := range events {
			if err := s.events.ensurePartition(ctx, e.TS); err != nil {
				return err
			}
		}
		return s.Write(ctx, func(tx *sql.Tx) error {
			q := s.Q(`INSERT INTO events (id, tenant_id, ts, type, object_id, source_id, severity, payload)
				VALUES (?,?,?,?,?,?,?,?)`)
			for _, e := range events {
				if _, err := tx.ExecContext(ctx, q, e.ID, e.TenantID, s.T(e.TS),
					string(e.Type), S(e.ObjectID), S(e.SourceID), S(string(e.Severity)),
					string(orEmptyJSON(e.Payload))); err != nil {
					return err
				}
			}
			return nil
		})
	}
	// SQLite: group per month segment.
	byMonth := map[string][]*model.Event{}
	for _, e := range events {
		k := monthKey(e.TS)
		byMonth[k] = append(byMonth[k], e)
	}
	for key, evs := range byMonth {
		db, err := s.events.segment(key, true)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		stmt := `INSERT INTO events (id, tenant_id, ts, type, object_id, source_id, severity, payload)
			VALUES (?,?,?,?,?,?,?,?)`
		for _, e := range evs {
			if _, err := tx.ExecContext(ctx, stmt, e.ID, e.TenantID,
				sqliteDialect{}.TimeValue(e.TS), string(e.Type),
				S(e.ObjectID), S(e.SourceID), S(string(e.Severity)),
				string(orEmptyJSON(e.Payload))); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func orEmptyJSON(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage(`{}`)
	}
	return r
}

// EventFilter narrows event queries.
type EventFilter struct {
	TenantID string
	Types    []string
	ObjectID string
	SourceID string
	Severity string
	From, To time.Time // zero = unbounded
	Cursor   string    // last event ID (descending pagination)
	Limit    int
	Asc      bool
}

// QueryEvents returns events newest-first (or oldest-first when Asc),
// fanning out over partitions/segments and merging.
func (s *Store) QueryEvents(ctx context.Context, f EventFilter) ([]*model.Event, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}
	where, args := eventWhere(s.dialect, f)
	order := "DESC"
	if f.Asc {
		order = "ASC"
	}
	query := fmt.Sprintf(`SELECT id, tenant_id, ts, type, object_id, source_id, severity, payload
		FROM events %s ORDER BY ts %s, id %s LIMIT %d`, where, order, order, f.Limit)

	if s.dialect.Name() == "postgres" {
		rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(query), args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanEvents(rows)
	}

	// SQLite: query relevant month segments and merge.
	keys := s.events.monthsFor(f.From, f.To)
	var all []*model.Event
	for _, key := range keys {
		db, err := s.events.segment(key, false)
		if err != nil {
			return nil, err
		}
		if db == nil {
			continue
		}
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		evs, err := scanEvents(rows)
		rows.Close()
		if err != nil {
			return nil, err
		}
		all = append(all, evs...)
	}
	sort.Slice(all, func(i, j int) bool {
		// Break ts ties by id in the same direction as the SQL ORDER BY,
		// so cursor pagination is stable across same-timestamp events.
		if all[i].TS.Equal(all[j].TS) {
			if f.Asc {
				return all[i].ID < all[j].ID
			}
			return all[i].ID > all[j].ID
		}
		if f.Asc {
			return all[i].TS.Before(all[j].TS)
		}
		return all[i].TS.After(all[j].TS)
	})
	if len(all) > f.Limit {
		all = all[:f.Limit]
	}
	return all, nil
}

// monthsFor lists candidate segment keys for a time range, newest first.
func (es *eventStore) monthsFor(from, to time.Time) []string {
	es.mu.Lock()
	defer es.mu.Unlock()
	var keys []string
	for k := range es.segments {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	var out []string
	for _, k := range keys {
		t, err := time.Parse("200601", k)
		if err != nil {
			continue
		}
		end := t.AddDate(0, 1, 0)
		if !to.IsZero() && t.After(to) {
			continue
		}
		if !from.IsZero() && end.Before(from) {
			continue
		}
		out = append(out, k)
	}
	return out
}

func eventWhere(d Dialect, f EventFilter) (string, []any) {
	var conds []string
	var args []any
	conds = append(conds, "tenant_id = ?")
	args = append(args, f.TenantID)
	if len(f.Types) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(f.Types)), ",")
		conds = append(conds, "type IN ("+ph+")")
		for _, t := range f.Types {
			args = append(args, t)
		}
	}
	if f.ObjectID != "" {
		conds = append(conds, "object_id = ?")
		args = append(args, f.ObjectID)
	}
	if f.SourceID != "" {
		conds = append(conds, "source_id = ?")
		args = append(args, f.SourceID)
	}
	if f.Severity != "" {
		conds = append(conds, "severity = ?")
		args = append(args, f.Severity)
	}
	if !f.From.IsZero() {
		conds = append(conds, "ts >= ?")
		args = append(args, d.TimeValue(f.From))
	}
	if !f.To.IsZero() {
		conds = append(conds, "ts < ?")
		args = append(args, d.TimeValue(f.To))
	}
	if f.Cursor != "" {
		// UUIDv7 IDs are time-ordered: the ID itself is the cursor. The
		// direction must match the sort order, else ascending pagination
		// (used by backend migration) re-reads or skips at page edges.
		if f.Asc {
			conds = append(conds, "id > ?")
		} else {
			conds = append(conds, "id < ?")
		}
		args = append(args, f.Cursor)
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

func scanEvents(rows *sql.Rows) ([]*model.Event, error) {
	var out []*model.Event
	for rows.Next() {
		var e model.Event
		var ts ScanTime
		var obj, src, sev NullStr
		var payload string
		if err := rows.Scan(&e.ID, &e.TenantID, &ts, (*string)(&e.Type), &obj, &src, &sev, &payload); err != nil {
			return nil, err
		}
		e.TS = ts.T
		e.ObjectID, e.SourceID, e.Severity = string(obj), string(src), model.Severity(sev)
		e.Payload = json.RawMessage(payload)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// EnforceEventRetention drops segments/partitions older than the
// configured horizon (ADR-13: O(1), no DELETE).
func (s *Store) EnforceEventRetention(ctx context.Context) (dropped []string, err error) {
	keep := s.events.keep
	if keep <= 0 {
		return nil, nil
	}
	cutoff := time.Now().UTC().AddDate(0, -keep, 0)
	cutKey := monthKey(cutoff)
	if s.dialect.Name() == "postgres" {
		rows, err := s.db.QueryContext(ctx,
			`SELECT tablename FROM pg_tables WHERE tablename LIKE 'events_2%'`)
		if err != nil {
			return nil, err
		}
		var tables []string
		for rows.Next() {
			var t string
			if err := rows.Scan(&t); err != nil {
				rows.Close()
				return nil, err
			}
			tables = append(tables, t)
		}
		rows.Close()
		for _, t := range tables {
			key := strings.TrimPrefix(t, "events_")
			if key < cutKey {
				if _, err := s.db.ExecContext(ctx, `DROP TABLE IF EXISTS `+t); err != nil {
					return dropped, err
				}
				dropped = append(dropped, key)
			}
		}
		return dropped, nil
	}
	es := s.events
	es.mu.Lock()
	var victims []string
	for k := range es.segments {
		if k < cutKey {
			victims = append(victims, k)
		}
	}
	es.mu.Unlock()
	for _, k := range victims {
		es.mu.Lock()
		if db := es.segments[k]; db != nil {
			_ = db.Close() // closing before deleting the segment files
			delete(es.segments, k)
		}
		es.mu.Unlock()
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(es.segmentPath(k) + suffix)
		}
		dropped = append(dropped, k)
	}
	return dropped, nil
}

// PurgeEventPayloads blanks payloads of a type older than a horizon
// in-place (GDPR retention classes, SPEC §13.4) while keeping the row
// skeleton for statistics until the segment drops.
func (s *Store) PurgeEventPayloads(ctx context.Context, tenantID, eventType string, before time.Time) (int64, error) {
	stmt := `UPDATE events SET payload = '{}' WHERE tenant_id = ? AND type = ? AND ts < ?`
	if s.dialect.Name() == "postgres" {
		res, err := s.db.ExecContext(ctx, s.Q(stmt), tenantID, eventType, s.T(before))
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	}
	var total int64
	for _, key := range s.events.monthsFor(time.Time{}, before) {
		db, err := s.events.segment(key, false)
		if err != nil || db == nil {
			continue
		}
		res, err := db.ExecContext(ctx, stmt, tenantID, eventType,
			sqliteDialect{}.TimeValue(before))
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// CountEvents is used by stats endpoints.
func (s *Store) CountEvents(ctx context.Context, f EventFilter) (int64, error) {
	where, args := eventWhere(s.dialect, f)
	query := `SELECT COUNT(*) FROM events ` + where
	if s.dialect.Name() == "postgres" {
		var n int64
		err := s.db.QueryRowContext(ctx, s.dialect.Rebind(query), args...).Scan(&n)
		return n, err
	}
	var total int64
	for _, key := range s.events.monthsFor(f.From, f.To) {
		db, err := s.events.segment(key, false)
		if err != nil || db == nil {
			continue
		}
		var n int64
		if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

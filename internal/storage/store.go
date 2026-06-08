// Package storage implements the relational layer behind a narrow
// database/sql-based interface with two first-class backends (SPEC §7.3,
// ADR-02): embedded SQLite (modernc, pure Go) and PostgreSQL ≥ 15 (pgx).
//
// Dialect policy (SPEC §6.5): one logical schema, dialect-generated DDL,
// DML strictly in the shared subset (ON CONFLICT, partial indexes, CTEs,
// RETURNING). Time is persisted as RFC 3339 UTC text on SQLite and
// timestamptz on PostgreSQL — value conversion is the only divergence,
// query text is identical (placeholders are rebound for pg).
//
// Events are time-partitioned (ADR-13): monthly segment *files* on
// SQLite (independent handles, merged in Go — same operational property
// as ATTACH: O(1) retention by deleting a file, cold reads never touch
// the hot core file) and native monthly range partitions on PostgreSQL.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// ErrNotFound is returned for missing rows.
var ErrNotFound = errors.New("not found")

// ErrConflict signals an optimistic-locking version mismatch (HTTP 409).
var ErrConflict = errors.New("version conflict")

// ErrDuplicate signals a unique-constraint violation (HTTP 409).
var ErrDuplicate = errors.New("duplicate")

// Store is the storage handle shared by all subsystems.
type Store struct {
	db      *sql.DB
	dialect Dialect
	log     *slog.Logger

	// SQLite: one write serialisation per file (SPEC §7.2). PostgreSQL
	// handles concurrency itself; writeMu stays unused there.
	writeMu   sync.Mutex
	serialise bool

	events *eventStore

	auditMu       sync.Mutex
	auditLastHash string

	// touchMu throttles best-effort api_tokens.last_used writes so a busy
	// token client doesn't fire a DB write (and take writeMu) per request.
	touchMu   sync.Mutex
	lastTouch map[string]time.Time
}

// Options for Open.
type Options struct {
	// DSN: empty or file path ⇒ SQLite; postgres://… ⇒ PostgreSQL.
	DSN     string
	DataDir string // SQLite mode: directory for core.db + event segments
	Log     *slog.Logger
	// RetentionMonths: event segments/partitions kept (0 = keep all).
	RetentionMonths int
}

// Open connects, migrates and prepares partitioning.
func Open(ctx context.Context, o Options) (*Store, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	s := &Store{log: o.Log}

	switch {
	case strings.HasPrefix(o.DSN, "postgres://") || strings.HasPrefix(o.DSN, "postgresql://"):
		db, err := sql.Open("pgx", o.DSN)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(16)
		s.db, s.dialect = db, pgDialect{}
	default:
		path := o.DSN
		if path == "" {
			if o.DataDir == "" {
				return nil, fmt.Errorf("storage: neither dsn nor data dir given")
			}
			path = filepath.Join(o.DataDir, "core.db")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, err
		}
		db, err := sql.Open("sqlite", sqliteDSN(path))
		if err != nil {
			return nil, err
		}
		// Connection pool (SPEC §7.3). WAL lets readers run concurrently
		// with the single writer, so keep a healthy number of connections —
		// but, crucially, keep them all idle-warm and non-expiring: modernc
		// re-runs the DSN _pragma list on every newly-opened connection, so a
		// connection opened mid-request used to re-execute journal_mode(WAL),
		// which takes the write lock and could wait out busy_timeout behind
		// the 250ms pipeline flush. Matching idle to open and disabling
		// idle/lifetime expiry means the pool is opened once and reused, not
		// re-opened (and re-pragma'd) on every request burst.
		db.SetMaxOpenConns(16)
		db.SetMaxIdleConns(16)
		db.SetConnMaxIdleTime(0)
		db.SetConnMaxLifetime(0)
		// Set WAL once at the database level (persistent in the file header)
		// rather than per-connection, so freshly-opened pool connections
		// never take the write lock just to confirm the journal mode.
		if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("storage: enable WAL: %w", err)
		}
		s.db, s.dialect = db, sqliteDialect{}
		s.serialise = true
	}

	if err := s.db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("storage: connect: %w", err)
	}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}

	es, err := newEventStore(ctx, s, o.DataDir, o.RetentionMonths)
	if err != nil {
		return nil, err
	}
	s.events = es

	if err := s.loadAuditHead(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func sqliteDSN(path string) string {
	// modernc applies these _pragma options per connection. journal_mode is
	// deliberately NOT here — it's set once on the *sql.DB (persistent in the
	// file header); per-connection it would take the write lock on every new
	// connection and stall reads behind the writer up to busy_timeout. The
	// rest are connection-local and cheap. busy_timeout is the worst-case
	// wait when a write genuinely contends (lowered from 10s).
	return "file:" + path +
		"?_pragma=synchronous(NORMAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_time_format=sqlite"
}

// Close shuts down all handles.
func (s *Store) Close() error {
	if s.events != nil {
		s.events.close()
	}
	return s.db.Close()
}

// Dialect exposes backend specifics to the few callers that need them.
func (s *Store) Dialect() Dialect { return s.dialect }

// DB exposes the raw handle (read paths, tests).
func (s *Store) DB() *sql.DB { return s.db }

// Write serialises a write transaction. fn runs inside a BEGIN…COMMIT;
// on SQLite all writes funnel through one mutex (one write goroutine per
// file, SPEC §7.3).
func (s *Store) Write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if s.serialise {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Read runs fn with the pooled handle (no transaction).
func (s *Store) Read() *sql.DB { return s.db }

// Q rebinds ?-placeholders for the active dialect.
func (s *Store) Q(query string) string { return s.dialect.Rebind(query) }

// T converts a time for parameter binding.
func (s *Store) T(t time.Time) any { return s.dialect.TimeValue(t) }

// TP converts an optional time.
func (s *Store) TP(t *time.Time) any {
	if t == nil {
		return nil
	}
	return s.dialect.TimeValue(*t)
}

// Dialect abstracts the two backends (kept deliberately tiny).
type Dialect interface {
	Name() string
	// Rebind converts ?-placeholders to the native form.
	Rebind(q string) string
	// TimeValue converts a time.Time for parameter binding.
	TimeValue(t time.Time) any
	// DDL expands schema placeholders.
	DDL(template string) string
	// IsDuplicate detects unique-violation errors.
	IsDuplicate(err error) bool
}

type sqliteDialect struct{}

func (sqliteDialect) Name() string            { return "sqlite" }
func (sqliteDialect) Rebind(q string) string  { return q }
func (sqliteDialect) TimeValue(t time.Time) any {
	return t.UTC().Format(time.RFC3339Nano)
}
func (sqliteDialect) DDL(tpl string) string {
	r := strings.NewReplacer(
		"{{PK_AUTO}}", "INTEGER PRIMARY KEY AUTOINCREMENT",
		"{{TIMESTAMP}}", "TEXT",
		"{{JSON}}", "TEXT",
		"{{BOOL}}", "INTEGER",
		"{{BLOB}}", "BLOB",
		"{{BIGINT}}", "INTEGER",
	)
	return r.Replace(tpl)
}
func (sqliteDialect) IsDuplicate(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

type pgDialect struct{}

func (pgDialect) Name() string { return "postgres" }
func (pgDialect) Rebind(q string) string {
	var b strings.Builder
	b.Grow(len(q) + 8)
	n := 0
	inStr := false
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c == '\'' {
			inStr = !inStr
		}
		if c == '?' && !inStr {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
func (pgDialect) TimeValue(t time.Time) any { return t.UTC() }
func (pgDialect) DDL(tpl string) string {
	r := strings.NewReplacer(
		"{{PK_AUTO}}", "BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY",
		"{{TIMESTAMP}}", "timestamptz",
		"{{JSON}}", "jsonb",
		"{{BOOL}}", "boolean",
		"{{BLOB}}", "bytea",
		"{{BIGINT}}", "bigint",
	)
	return r.Replace(tpl)
}
func (pgDialect) IsDuplicate(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23505")
}

// ScanTime handles both backends' time representations.
type ScanTime struct {
	T     time.Time
	Valid bool
}

func (st *ScanTime) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		st.Valid = false
		return nil
	case time.Time:
		st.T, st.Valid = x.UTC(), true
		return nil
	case string:
		return st.parse(x)
	case []byte:
		return st.parse(string(x))
	}
	return fmt.Errorf("storage: cannot scan %T as time", v)
}

func (st *ScanTime) parse(s string) error {
	if s == "" {
		st.Valid = false
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		// tolerate space-separated SQLite datetime()
		t, err = time.Parse("2006-01-02 15:04:05", s)
		if err != nil {
			return err
		}
	}
	st.T, st.Valid = t.UTC(), true
	return nil
}

// Ptr returns the optional time as *time.Time.
func (st ScanTime) Ptr() *time.Time {
	if !st.Valid {
		return nil
	}
	t := st.T
	return &t
}

// NullStr maps NULL ↔ "".
type NullStr string

func (n *NullStr) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		*n = ""
	case string:
		*n = NullStr(x)
	case []byte:
		*n = NullStr(x)
	default:
		return fmt.Errorf("storage: cannot scan %T as string", v)
	}
	return nil
}

// S converts "" → NULL for binding.
func S(s string) any {
	if s == "" {
		return nil
	}
	return s
}

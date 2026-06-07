package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// --- users ---

const userCols = `id, name, email, subject, local, pass_hash, disabled, last_seen_at,
	version, created_at, updated_at`

func scanUser(sc interface{ Scan(...any) error }) (*model.User, error) {
	var u model.User
	var subject NullStr
	var lastSeen, created, updated ScanTime
	if err := sc.Scan(&u.ID, &u.Name, &u.Email, &subject, &u.Local, &u.PassHash,
		&u.Disabled, &lastSeen, &u.Version, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Subject = string(subject)
	u.LastSeenAt = lastSeen.Ptr()
	u.CreatedAt, u.UpdatedAt = created.T, updated.T
	return &u, nil
}

// UpsertUserBySubject provisions SSO users on first login (SPEC §11.2).
func (s *Store) UpsertUserBySubject(ctx context.Context, subject, name, email string) (*model.User, error) {
	now := time.Now().UTC()
	existing, err := s.GetUserBySubject(ctx, subject)
	if err != nil && err != ErrNotFound {
		return nil, err
	}
	if existing != nil {
		err := s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, s.Q(
				`UPDATE users SET name = ?, email = ?, last_seen_at = ?, updated_at = ? WHERE id = ?`),
				name, email, s.T(now), s.T(now), existing.ID)
			return err
		})
		if err != nil {
			return nil, err
		}
		existing.Name, existing.Email = name, email
		return existing, nil
	}
	u := &model.User{ID: model.NewID(), Name: name, Email: email, Subject: subject,
		Version: 1, CreatedAt: now, UpdatedAt: now}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO users (id, name, email, subject, local, version, created_at, updated_at)
			 VALUES (?,?,?,?,false,1,?,?)`),
			u.ID, u.Name, u.Email, u.Subject, s.T(now), s.T(now))
		return err
	})
	return u, err
}

// CreateLocalUser for break-glass accounts.
func (s *Store) CreateLocalUser(ctx context.Context, name, email, passHash string) (*model.User, error) {
	now := time.Now().UTC()
	u := &model.User{ID: model.NewID(), Name: name, Email: email, Local: true,
		PassHash: passHash, Version: 1, CreatedAt: now, UpdatedAt: now}
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO users (id, name, email, local, pass_hash, version, created_at, updated_at)
			 VALUES (?,?,?,true,?,1,?,?)`),
			u.ID, u.Name, u.Email, u.PassHash, s.T(now), s.T(now))
		return err
	})
	return u, err
}

// GetUserBySubject for OIDC logins.
func (s *Store) GetUserBySubject(ctx context.Context, subject string) (*model.User, error) {
	row := s.db.QueryRowContext(ctx, s.Q(
		`SELECT `+userCols+` FROM users WHERE subject = ?`), subject)
	return scanUser(row)
}

// GetUserByEmail for local login.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	row := s.db.QueryRowContext(ctx, s.Q(
		`SELECT `+userCols+` FROM users WHERE email = ? AND disabled = false`), email)
	return scanUser(row)
}

// GetUser by ID.
func (s *Store) GetUser(ctx context.Context, id string) (*model.User, error) {
	row := s.db.QueryRowContext(ctx, s.Q(`SELECT `+userCols+` FROM users WHERE id = ?`), id)
	return scanUser(row)
}

// ListUsers (admin view).
func (s *Store) ListUsers(ctx context.Context) ([]*model.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// --- API tokens ---

const tokenCols = `id, tenant_id, name, prefix, hash, scopes, roles, ip_bind, ai_agent,
	expires_at, last_used, created_by, version, created_at`

func scanToken(sc interface{ Scan(...any) error }) (*model.APIToken, error) {
	var t model.APIToken
	var scopes, roles, ipBind string
	var expires, lastUsed, created ScanTime
	if err := sc.Scan(&t.ID, &t.TenantID, &t.Name, &t.Prefix, &t.Hash, &scopes, &roles,
		&ipBind, &t.AIAgent, &expires, &lastUsed, &t.CreatedBy, &t.Version, &created); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(scopes), &t.Scopes)
	_ = json.Unmarshal([]byte(roles), &t.RoleNames)
	_ = json.Unmarshal([]byte(ipBind), &t.IPBind)
	t.ExpiresAt, t.LastUsed = expires.Ptr(), lastUsed.Ptr()
	t.CreatedAt = created.T
	return &t, nil
}

// CreateAPIToken stores the hashed token.
func (s *Store) CreateAPIToken(ctx context.Context, t *model.APIToken) error {
	if t.ID == "" {
		t.ID = model.NewID()
	}
	t.CreatedAt = time.Now().UTC()
	t.Version = 1
	scopes, _ := jsonMarshal(t.Scopes)
	roles, _ := jsonMarshal(orSlice(t.RoleNames))
	ipBind, _ := jsonMarshal(orSlice(t.IPBind))
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO api_tokens (`+tokenCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
			t.ID, t.TenantID, t.Name, t.Prefix, t.Hash, scopes, roles, ipBind, t.AIAgent,
			s.TP(t.ExpiresAt), nil, t.CreatedBy, t.Version, s.T(t.CreatedAt))
		return err
	})
}

// TokensByPrefix returns candidates for verification (prefix collision
// is possible, the argon2 check decides).
func (s *Store) TokensByPrefix(ctx context.Context, prefix string) ([]*model.APIToken, error) {
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT `+tokenCols+` FROM api_tokens WHERE prefix = ?`), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.APIToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListAPITokens of a tenant.
func (s *Store) ListAPITokens(ctx context.Context, tenantID string) ([]*model.APIToken, error) {
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT `+tokenCols+` FROM api_tokens WHERE tenant_id = ? ORDER BY created_at DESC`), tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.APIToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAPIToken revokes a token.
func (s *Store) DeleteAPIToken(ctx context.Context, tenantID, id string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`DELETE FROM api_tokens WHERE tenant_id = ? AND id = ?`), tenantID, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// TouchAPIToken updates last_used (best-effort, no error propagation).
func (s *Store) TouchAPIToken(ctx context.Context, id string) {
	_ = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`UPDATE api_tokens SET last_used = ? WHERE id = ?`), s.T(time.Now().UTC()), id)
		return err
	})
}

// --- sessions ---

// SessionData carried by UI cookies.
type SessionData struct {
	Roles  []string `json:"roles,omitempty"`
	Groups []string `json:"groups,omitempty"`
}

// CreateSession persists a UI session.
func (s *Store) CreateSession(ctx context.Context, id, userID, tenantID string, data SessionData, ttl time.Duration) error {
	now := time.Now().UTC()
	raw, _ := jsonMarshal(data)
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO sessions (id, user_id, tenant_id, data, created_at, expires_at) VALUES (?,?,?,?,?,?)`),
			id, userID, tenantID, raw, s.T(now), s.T(now.Add(ttl)))
		return err
	})
}

// GetSession returns user/tenant/data for a live session.
func (s *Store) GetSession(ctx context.Context, id string) (userID, tenantID string, data SessionData, err error) {
	var raw string
	var expires ScanTime
	err = s.db.QueryRowContext(ctx, s.Q(
		`SELECT user_id, tenant_id, data, expires_at FROM sessions WHERE id = ?`), id).
		Scan(&userID, &tenantID, &raw, &expires)
	if err == sql.ErrNoRows {
		return "", "", data, ErrNotFound
	}
	if err != nil {
		return "", "", data, err
	}
	if expires.Valid && expires.T.Before(time.Now()) {
		return "", "", data, ErrNotFound
	}
	_ = json.Unmarshal([]byte(raw), &data)
	return userID, tenantID, data, nil
}

// DeleteSession (logout).
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(`DELETE FROM sessions WHERE id = ?`), id)
		return err
	})
}

// CleanupExpired removes dead sessions and idempotency rows (janitor).
func (s *Store) CleanupExpired(ctx context.Context) error {
	now := time.Now().UTC()
	return s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, s.Q(
			`DELETE FROM sessions WHERE expires_at < ?`), s.T(now)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, s.Q(
			`DELETE FROM idempotency WHERE created_at < ?`), s.T(now.Add(-24*time.Hour)))
		return err
	})
}

// --- secrets (AES-256-GCM blobs are produced by the secrets package) ---

// PutSecret stores an encrypted secret value.
func (s *Store) PutSecret(ctx context.Context, tenantID, name string, ciphertext []byte, by string) error {
	now := time.Now().UTC()
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO secrets (tenant_id, name, ciphertext, updated_by, updated_at)
			 VALUES (?,?,?,?,?)
			 ON CONFLICT (tenant_id, name) DO UPDATE SET
			 ciphertext = excluded.ciphertext, updated_by = excluded.updated_by, updated_at = excluded.updated_at`),
			tenantID, name, ciphertext, by, s.T(now))
		return err
	})
}

// GetSecret returns the ciphertext.
func (s *Store) GetSecret(ctx context.Context, tenantID, name string) ([]byte, error) {
	var ct []byte
	err := s.db.QueryRowContext(ctx, s.Q(
		`SELECT ciphertext FROM secrets WHERE tenant_id = ? AND name = ?`), tenantID, name).Scan(&ct)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return ct, err
}

// ListSecretNames (values never leave the store unencrypted via list).
func (s *Store) ListSecretNames(ctx context.Context, tenantID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT name FROM secrets WHERE tenant_id = ? ORDER BY name`), tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DeleteSecret removes a secret.
func (s *Store) DeleteSecret(ctx context.Context, tenantID, name string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`DELETE FROM secrets WHERE tenant_id = ? AND name = ?`), tenantID, name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// --- idempotency (SPEC §11.1) ---

// IdempotencyCheck returns a stored response for (tenant, key) when the
// request hash matches; stores nothing. found=false ⇒ caller executes
// and records via IdempotencyStore.
func (s *Store) IdempotencyCheck(ctx context.Context, tenantID, key string, reqBody []byte) (status int, body []byte, found bool, err error) {
	h := sha256.Sum256(reqBody)
	var storedHash string
	err = s.db.QueryRowContext(ctx, s.Q(
		`SELECT req_hash, status, body FROM idempotency WHERE tenant_id = ? AND idem_key = ?`),
		tenantID, key).Scan(&storedHash, &status, &body)
	if err == sql.ErrNoRows {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	if storedHash != hex.EncodeToString(h[:]) {
		return 0, nil, false, fmt.Errorf("idempotency key reused with different request body")
	}
	return status, body, true, nil
}

// IdempotencyStore records the response for replay.
func (s *Store) IdempotencyStore(ctx context.Context, tenantID, key string, reqBody []byte, status int, respBody []byte) error {
	h := sha256.Sum256(reqBody)
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO idempotency (tenant_id, idem_key, req_hash, status, body, created_at)
			 VALUES (?,?,?,?,?,?) ON CONFLICT (tenant_id, idem_key) DO NOTHING`),
			tenantID, key, hex.EncodeToString(h[:]), status, respBody, s.T(time.Now().UTC()))
		return err
	})
}

// --- kv ---

// KVGet unmarshals a state document.
func (s *Store) KVGet(ctx context.Context, key string, dst any) error {
	var raw string
	err := s.db.QueryRowContext(ctx, s.Q(`SELECT v FROM kv WHERE k = ?`), key).Scan(&raw)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), dst)
}

// KVPut stores a state document.
func (s *Store) KVPut(ctx context.Context, key string, v any) error {
	raw, err := jsonMarshal(v)
	if err != nil {
		return err
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO kv (k, v, updated_at) VALUES (?,?,?)
			 ON CONFLICT (k) DO UPDATE SET v = excluded.v, updated_at = excluded.updated_at`),
			key, raw, s.T(time.Now().UTC()))
		return err
	})
}

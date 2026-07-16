package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// Multi-provider agent chat persistence (SPEC §10.4 evolution): provider
// connections carry SecretBox-sealed API keys and are scoped to a user
// (user_id = '' marks a tenant-wide connection an admin manages); chats
// and messages are strictly per-user — every query filters on both
// tenant_id and user_id so one user can never read another's transcript.

// AIProviderConnection is a stored LLM provider account.
type AIProviderConnection struct {
	ID           string            `json:"id"`
	TenantID     string            `json:"-"`
	UserID       string            `json:"-"`
	Shared       bool              `json:"shared"` // tenant-wide (admin-managed)
	Name         string            `json:"name"`
	Provider     string            `json:"provider"`
	Endpoint     string            `json:"endpoint,omitempty"`
	APIKeySealed []byte            `json:"-"` // AES-GCM blob, never serialised
	KeyHint      string            `json:"keyHint,omitempty"`
	HasKey       bool              `json:"hasKey"`
	DefaultModel string            `json:"defaultModel,omitempty"`
	Extra        map[string]string `json:"extra,omitempty"`
	Disabled     bool              `json:"disabled,omitempty"`
	Version      int64             `json:"version"`
	CreatedAt    time.Time         `json:"createdAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
}

// AIChat is one agent conversation (metadata; messages live separately).
type AIChat struct {
	ID           string          `json:"id"`
	TenantID     string          `json:"-"`
	UserID       string          `json:"-"`
	Title        string          `json:"title"`
	ConnectionID string          `json:"connectionId,omitempty"`
	Model        string          `json:"model,omitempty"`
	Settings     json.RawMessage `json:"settings,omitempty"`
	Version      int64           `json:"version"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

// AIChatMessage is one turn; parts is the UI-message parts array
// (text / reasoning / tool invocations with results).
type AIChatMessage struct {
	ID        string          `json:"id"`
	ChatID    string          `json:"chatId"`
	TenantID  string          `json:"-"`
	Role      string          `json:"role"` // user | assistant
	Parts     json.RawMessage `json:"parts"`
	Model     string          `json:"model,omitempty"`
	Usage     json.RawMessage `json:"usage,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

// --- provider connections ---

// ListAIConnections returns the user's own connections plus tenant-wide
// ones (user_id = ”).
func (s *Store) ListAIConnections(ctx context.Context, tenantID, userID string) ([]*AIProviderConnection, error) {
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT id, tenant_id, user_id, name, provider, endpoint, api_key, key_hint,
		        default_model, extra, disabled, version, created_at, updated_at
		 FROM ai_provider_connections
		 WHERE tenant_id = ? AND (user_id = ? OR user_id = '')
		 ORDER BY user_id DESC, name`), tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*AIProviderConnection
	for rows.Next() {
		c, err := scanAIConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetAIConnection fetches one connection the user may use: their own or
// a tenant-wide one.
func (s *Store) GetAIConnection(ctx context.Context, tenantID, userID, id string) (*AIProviderConnection, error) {
	row := s.db.QueryRowContext(ctx, s.Q(
		`SELECT id, tenant_id, user_id, name, provider, endpoint, api_key, key_hint,
		        default_model, extra, disabled, version, created_at, updated_at
		 FROM ai_provider_connections
		 WHERE tenant_id = ? AND id = ? AND (user_id = ? OR user_id = '')`), tenantID, id, userID)
	c, err := scanAIConnection(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return c, err
}

type rowScanner interface{ Scan(dst ...any) error }

func scanAIConnection(row rowScanner) (*AIProviderConnection, error) {
	c := &AIProviderConnection{}
	var extra string
	var created, updated ScanTime
	err := row.Scan(&c.ID, &c.TenantID, &c.UserID, &c.Name, &c.Provider, &c.Endpoint,
		&c.APIKeySealed, &c.KeyHint, &c.DefaultModel, &extra, &c.Disabled,
		&c.Version, &created, &updated)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(extra), &c.Extra)
	c.CreatedAt, c.UpdatedAt = created.T, updated.T
	c.Shared = c.UserID == ""
	c.HasKey = len(c.APIKeySealed) > 0
	return c, nil
}

// CreateAIConnection inserts a connection (ID assigned when empty).
func (s *Store) CreateAIConnection(ctx context.Context, c *AIProviderConnection) error {
	if c.ID == "" {
		c.ID = model.NewID()
	}
	now := time.Now().UTC()
	c.CreatedAt, c.UpdatedAt, c.Version = now, now, 1
	extra, _ := jsonMarshal(c.Extra)
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO ai_provider_connections
			 (id, tenant_id, user_id, name, provider, endpoint, api_key, key_hint,
			  default_model, extra, disabled, version, created_at, updated_at)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
			c.ID, c.TenantID, c.UserID, c.Name, c.Provider, c.Endpoint, c.APIKeySealed,
			c.KeyHint, c.DefaultModel, extra, c.Disabled, c.Version, s.T(now), s.T(now))
		if s.dialect.IsDuplicate(err) {
			return fmt.Errorf("%w: connection %q already exists", ErrDuplicate, c.Name)
		}
		return err
	})
}

// UpdateAIConnection rewrites mutable fields. A nil newKey keeps the
// stored key; a non-nil empty slice clears it.
func (s *Store) UpdateAIConnection(ctx context.Context, c *AIProviderConnection, newKey []byte) error {
	now := time.Now().UTC()
	extra, _ := jsonMarshal(c.Extra)
	return s.Write(ctx, func(tx *sql.Tx) error {
		var res sql.Result
		var err error
		if newKey != nil {
			sealed := newKey
			if len(sealed) == 0 {
				sealed = nil
			}
			res, err = tx.ExecContext(ctx, s.Q(
				`UPDATE ai_provider_connections SET name = ?, endpoint = ?, api_key = ?,
				 key_hint = ?, default_model = ?, extra = ?, disabled = ?,
				 version = version + 1, updated_at = ?
				 WHERE tenant_id = ? AND id = ? AND user_id = ?`),
				c.Name, c.Endpoint, sealed, c.KeyHint, c.DefaultModel, extra, c.Disabled,
				s.T(now), c.TenantID, c.ID, c.UserID)
		} else {
			res, err = tx.ExecContext(ctx, s.Q(
				`UPDATE ai_provider_connections SET name = ?, endpoint = ?,
				 default_model = ?, extra = ?, disabled = ?,
				 version = version + 1, updated_at = ?
				 WHERE tenant_id = ? AND id = ? AND user_id = ?`),
				c.Name, c.Endpoint, c.DefaultModel, extra, c.Disabled,
				s.T(now), c.TenantID, c.ID, c.UserID)
		}
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// DeleteAIConnection removes a connection owned by userID (” = shared).
func (s *Store) DeleteAIConnection(ctx context.Context, tenantID, userID, id string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`DELETE FROM ai_provider_connections WHERE tenant_id = ? AND id = ? AND user_id = ?`),
			tenantID, id, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// --- chats ---

// ListAIChats returns the user's chats, most recently active first.
func (s *Store) ListAIChats(ctx context.Context, tenantID, userID string, limit int) ([]*AIChat, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT id, tenant_id, user_id, title, connection_id, model, settings, version, created_at, updated_at
		 FROM ai_chats WHERE tenant_id = ? AND user_id = ?
		 ORDER BY updated_at DESC LIMIT `+fmt.Sprint(limit)), tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*AIChat
	for rows.Next() {
		c, err := scanAIChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetAIChat fetches one chat, enforcing ownership.
func (s *Store) GetAIChat(ctx context.Context, tenantID, userID, id string) (*AIChat, error) {
	row := s.db.QueryRowContext(ctx, s.Q(
		`SELECT id, tenant_id, user_id, title, connection_id, model, settings, version, created_at, updated_at
		 FROM ai_chats WHERE tenant_id = ? AND user_id = ? AND id = ?`), tenantID, userID, id)
	c, err := scanAIChat(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return c, err
}

func scanAIChat(row rowScanner) (*AIChat, error) {
	c := &AIChat{}
	var settings string
	var created, updated ScanTime
	err := row.Scan(&c.ID, &c.TenantID, &c.UserID, &c.Title, &c.ConnectionID, &c.Model,
		&settings, &c.Version, &created, &updated)
	if err != nil {
		return nil, err
	}
	if settings != "" {
		c.Settings = json.RawMessage(settings)
	}
	c.CreatedAt, c.UpdatedAt = created.T, updated.T
	return c, nil
}

// CreateAIChat inserts a chat (ID assigned when empty).
func (s *Store) CreateAIChat(ctx context.Context, c *AIChat) error {
	if c.ID == "" {
		c.ID = model.NewID()
	}
	if len(c.Settings) == 0 {
		c.Settings = json.RawMessage(`{}`)
	}
	now := time.Now().UTC()
	c.CreatedAt, c.UpdatedAt, c.Version = now, now, 1
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO ai_chats (id, tenant_id, user_id, title, connection_id, model, settings, version, created_at, updated_at)
			 VALUES (?,?,?,?,?,?,?,?,?,?)`),
			c.ID, c.TenantID, c.UserID, c.Title, c.ConnectionID, c.Model,
			string(c.Settings), c.Version, s.T(now), s.T(now))
		return err
	})
}

// UpdateAIChat rewrites chat metadata (title, connection, model, settings).
func (s *Store) UpdateAIChat(ctx context.Context, c *AIChat) error {
	now := time.Now().UTC()
	if len(c.Settings) == 0 {
		c.Settings = json.RawMessage(`{}`)
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`UPDATE ai_chats SET title = ?, connection_id = ?, model = ?, settings = ?,
			 version = version + 1, updated_at = ?
			 WHERE tenant_id = ? AND user_id = ? AND id = ?`),
			c.Title, c.ConnectionID, c.Model, string(c.Settings), s.T(now),
			c.TenantID, c.UserID, c.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// TouchAIChat bumps updated_at (called after a message lands).
func (s *Store) TouchAIChat(ctx context.Context, tenantID, id string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`UPDATE ai_chats SET updated_at = ? WHERE tenant_id = ? AND id = ?`),
			s.T(time.Now().UTC()), tenantID, id)
		return err
	})
}

// DeleteAIChat removes a chat and (via FK cascade) its messages.
func (s *Store) DeleteAIChat(ctx context.Context, tenantID, userID, id string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		// Explicit message delete first: SQLite enforces the cascade only
		// with foreign_keys(ON) — belt and braces keeps both dialects clean.
		if _, err := tx.ExecContext(ctx, s.Q(
			`DELETE FROM ai_chat_messages WHERE chat_id = ? AND tenant_id = ?`), id, tenantID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, s.Q(
			`DELETE FROM ai_chats WHERE tenant_id = ? AND user_id = ? AND id = ?`),
			tenantID, userID, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// --- messages ---

// ListAIChatMessages returns a chat's messages in insertion order
// (UUIDv7 ids are time-ordered).
func (s *Store) ListAIChatMessages(ctx context.Context, tenantID, chatID string) ([]*AIChatMessage, error) {
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT id, chat_id, tenant_id, role, parts, model, usage, created_at
		 FROM ai_chat_messages WHERE tenant_id = ? AND chat_id = ? ORDER BY id`), tenantID, chatID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*AIChatMessage
	for rows.Next() {
		m := &AIChatMessage{}
		var parts, usage string
		var created ScanTime
		if err := rows.Scan(&m.ID, &m.ChatID, &m.TenantID, &m.Role, &parts, &m.Model, &usage, &created); err != nil {
			return nil, err
		}
		m.Parts, m.Usage, m.CreatedAt = json.RawMessage(parts), json.RawMessage(usage), created.T
		out = append(out, m)
	}
	return out, rows.Err()
}

// AppendAIChatMessage inserts one message (ID assigned when empty).
func (s *Store) AppendAIChatMessage(ctx context.Context, m *AIChatMessage) error {
	if m.ID == "" {
		m.ID = model.NewID()
	}
	if len(m.Parts) == 0 {
		m.Parts = json.RawMessage(`[]`)
	}
	if len(m.Usage) == 0 {
		m.Usage = json.RawMessage(`{}`)
	}
	m.CreatedAt = time.Now().UTC()
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO ai_chat_messages (id, chat_id, tenant_id, role, parts, model, usage, created_at)
			 VALUES (?,?,?,?,?,?,?,?)`),
			m.ID, m.ChatID, m.TenantID, m.Role, string(m.Parts), m.Model, string(m.Usage), s.T(m.CreatedAt))
		return err
	})
}

// DeleteAIChatMessage removes a single message from a chat.
func (s *Store) DeleteAIChatMessage(ctx context.Context, tenantID, chatID, msgID string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`DELETE FROM ai_chat_messages WHERE tenant_id = ? AND chat_id = ? AND id = ?`),
			tenantID, chatID, msgID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// DeleteAIChatMessagesFrom removes msgID and everything after it —
// the regenerate/edit-from-here primitive (UUIDv7 order).
func (s *Store) DeleteAIChatMessagesFrom(ctx context.Context, tenantID, chatID, msgID string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`DELETE FROM ai_chat_messages WHERE tenant_id = ? AND chat_id = ? AND id >= ?`),
			tenantID, chatID, msgID)
		return err
	})
}

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// --- escalation timers (SPEC §9.4: durable across restarts) ---

// EscalationTimer is one pending step of an alert's chain.
type EscalationTimer struct {
	AlertID     string
	PolicyName  string
	StepIndex   int
	RepeatsDone int
	NextAt      *time.Time
	Done        bool
}

// ScheduleEscalation upserts a step timer.
func (s *Store) ScheduleEscalation(ctx context.Context, t EscalationTimer) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO escalations (alert_id, policy_name, step_index, repeats_done, next_at, done)
			 VALUES (?,?,?,?,?,?)
			 ON CONFLICT (alert_id, step_index) DO UPDATE SET
			 repeats_done = excluded.repeats_done, next_at = excluded.next_at, done = excluded.done`),
			t.AlertID, t.PolicyName, t.StepIndex, t.RepeatsDone, s.TP(t.NextAt), t.Done)
		return err
	})
}

// DueEscalations returns timers ready to fire.
func (s *Store) DueEscalations(ctx context.Context, now time.Time, limit int) ([]EscalationTimer, error) {
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT alert_id, policy_name, step_index, repeats_done, next_at, done
		 FROM escalations WHERE done = false AND next_at IS NOT NULL AND next_at <= ?
		 ORDER BY next_at LIMIT `+fmt.Sprint(limit)), s.T(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EscalationTimer
	for rows.Next() {
		var t EscalationTimer
		var next ScanTime
		if err := rows.Scan(&t.AlertID, &t.PolicyName, &t.StepIndex, &t.RepeatsDone, &next, &t.Done); err != nil {
			return nil, err
		}
		t.NextAt = next.Ptr()
		out = append(out, t)
	}
	return out, rows.Err()
}

// CancelEscalations marks all timers of an alert done (ack/resolve stop
// the chain, SPEC §9.4).
func (s *Store) CancelEscalations(ctx context.Context, alertID string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`UPDATE escalations SET done = true WHERE alert_id = ?`), alertID)
		return err
	})
}

// --- outbox (retry queue with DLQ, SPEC §9.6/§11.5) ---

// OutboxItem is a pending outbound delivery.
type OutboxItem struct {
	ID        string          `json:"id"`
	TenantID  string          `json:"tenantId"`
	ChannelID string          `json:"channelId,omitempty"`
	Kind      string          `json:"kind"` // notification|webhook-sub|servicenow
	Payload   json.RawMessage `json:"payload"`
	Attempts  int             `json:"attempts"`
	NextTry   time.Time       `json:"nextTry"`
	Dead      bool            `json:"dead"`
	LastError string          `json:"lastError,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

// EnqueueOutbox inserts a delivery.
func (s *Store) EnqueueOutbox(ctx context.Context, item *OutboxItem) error {
	if item.ID == "" {
		item.ID = model.NewID()
	}
	now := time.Now().UTC()
	item.CreatedAt = now
	if item.NextTry.IsZero() {
		item.NextTry = now
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO outbox (id, tenant_id, channel_id, kind, payload, attempts, next_try, dead, last_error, created_at)
			 VALUES (?,?,?,?,?,?,?,false,'',?)`),
			item.ID, item.TenantID, item.ChannelID, item.Kind, string(item.Payload),
			item.Attempts, s.T(item.NextTry), s.T(now))
		return err
	})
}

// DueOutbox returns deliveries ready for (re)try.
func (s *Store) DueOutbox(ctx context.Context, now time.Time, limit int) ([]*OutboxItem, error) {
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT id, tenant_id, channel_id, kind, payload, attempts, next_try, dead, last_error, created_at
		 FROM outbox WHERE dead = false AND next_try <= ? ORDER BY next_try LIMIT `+fmt.Sprint(limit)),
		s.T(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOutbox(rows)
}

// ClaimOutbox atomically leases due deliveries: each returned item has
// had its next_try pushed out by lease, so a concurrent notifier tick or
// a second HA node will not pick it up again. A crash mid-send simply
// lets the lease expire and the item becomes due again. This prevents the
// double-send that a plain DueOutbox+send has under overlapping ticks.
func (s *Store) ClaimOutbox(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]*OutboxItem, error) {
	candidates, err := s.DueOutbox(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	leaseUntil := s.T(now.Add(lease))
	var claimed []*OutboxItem
	err = s.Write(ctx, func(tx *sql.Tx) error {
		for _, it := range candidates {
			res, err := tx.ExecContext(ctx, s.Q(
				`UPDATE outbox SET next_try = ? WHERE id = ? AND next_try <= ? AND dead = false`),
				leaseUntil, it.ID, s.T(now))
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 1 {
				claimed = append(claimed, it)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

func scanOutbox(rows *sql.Rows) ([]*OutboxItem, error) {
	var out []*OutboxItem
	for rows.Next() {
		var it OutboxItem
		var payload string
		var nextTry, created ScanTime
		if err := rows.Scan(&it.ID, &it.TenantID, &it.ChannelID, &it.Kind, &payload,
			&it.Attempts, &nextTry, &it.Dead, &it.LastError, &created); err != nil {
			return nil, err
		}
		it.Payload = json.RawMessage(payload)
		it.NextTry, it.CreatedAt = nextTry.T, created.T
		out = append(out, &it)
	}
	return out, rows.Err()
}

// OutboxRetry reschedules after a failure; dead=true moves to the DLQ.
func (s *Store) OutboxRetry(ctx context.Context, id string, attempts int, nextTry time.Time, dead bool, lastError string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`UPDATE outbox SET attempts = ?, next_try = ?, dead = ?, last_error = ? WHERE id = ?`),
			attempts, s.T(nextTry), dead, lastError, id)
		return err
	})
}

// OutboxDone removes a delivered item.
func (s *Store) OutboxDone(ctx context.Context, id string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(`DELETE FROM outbox WHERE id = ?`), id)
		return err
	})
}

// DeadLetters lists the DLQ (UI + alarm, F-04.04).
func (s *Store) DeadLetters(ctx context.Context, tenantID string, limit int) ([]*OutboxItem, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT id, tenant_id, channel_id, kind, payload, attempts, next_try, dead, last_error, created_at
		 FROM outbox WHERE dead = true AND tenant_id = ? ORDER BY created_at DESC LIMIT `+fmt.Sprint(limit)),
		tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOutbox(rows)
}

// ReplayDeadLetter requeues a DLQ item (POST /webhooks/{id}:replay).
func (s *Store) ReplayDeadLetter(ctx context.Context, tenantID, id string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`UPDATE outbox SET dead = false, attempts = 0, next_try = ?, last_error = ''
			 WHERE tenant_id = ? AND id = ? AND dead = true`),
			s.T(time.Now().UTC()), tenantID, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// --- AI actions (approval queue, SPEC §10.1) ---

// AIActionStatus lifecycle.
type AIActionStatus string

const (
	AIProposed AIActionStatus = "proposed"
	AIApproved AIActionStatus = "approved"
	AIDenied   AIActionStatus = "denied"
	AIExecuted AIActionStatus = "executed"
	AIFailed   AIActionStatus = "failed"
)

// AIAction is a proposed/executed mutation by an AI agent.
type AIAction struct {
	ID             string          `json:"id"`
	TenantID       string          `json:"tenantId"`
	ConversationID string          `json:"conversationId,omitempty"`
	Tool           string          `json:"tool"`
	Args           json.RawMessage `json:"args"`
	Summary        string          `json:"summary,omitempty"`
	Status         AIActionStatus  `json:"status"`
	Actor          string          `json:"actor"`
	Result         json.RawMessage `json:"result,omitempty"`
	DecidedBy      string          `json:"decidedBy,omitempty"`
	DecidedAt      *time.Time      `json:"decidedAt,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// CreateAIAction enqueues a proposal (or an auto-approved action).
func (s *Store) CreateAIAction(ctx context.Context, a *AIAction) error {
	if a.ID == "" {
		a.ID = model.NewID()
	}
	a.CreatedAt = time.Now().UTC()
	if a.Status == "" {
		a.Status = AIProposed
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO ai_actions (id, tenant_id, conversation_id, tool, args, summary, status, actor, created_at)
			 VALUES (?,?,?,?,?,?,?,?,?)`),
			a.ID, a.TenantID, a.ConversationID, a.Tool, string(orEmptyJSON(a.Args)),
			a.Summary, string(a.Status), a.Actor, s.T(a.CreatedAt))
		return err
	})
}

// DecideAIAction approves/denies a proposal.
func (s *Store) DecideAIAction(ctx context.Context, tenantID, id string, status AIActionStatus, by string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.Q(
			`UPDATE ai_actions SET status = ?, decided_by = ?, decided_at = ?
			 WHERE tenant_id = ? AND id = ? AND status = 'proposed'`),
			string(status), by, s.T(time.Now().UTC()), tenantID, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// FinishAIAction records execution outcome.
func (s *Store) FinishAIAction(ctx context.Context, id string, status AIActionStatus, result json.RawMessage) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`UPDATE ai_actions SET status = ?, result = ? WHERE id = ?`),
			string(status), string(orEmptyJSON(result)), id)
		return err
	})
}

// GetAIAction by id.
func (s *Store) GetAIAction(ctx context.Context, tenantID, id string) (*AIAction, error) {
	row := s.db.QueryRowContext(ctx, s.Q(
		`SELECT id, tenant_id, conversation_id, tool, args, summary, status, actor, result, decided_by, decided_at, created_at
		 FROM ai_actions WHERE tenant_id = ? AND id = ?`), tenantID, id)
	return scanAIAction(row)
}

func scanAIAction(sc interface{ Scan(...any) error }) (*AIAction, error) {
	var a AIAction
	var args, result NullStr
	var decided, created ScanTime
	if err := sc.Scan(&a.ID, &a.TenantID, &a.ConversationID, &a.Tool, &args, &a.Summary,
		(*string)(&a.Status), &a.Actor, &result, &a.DecidedBy, &decided, &created); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.Args = json.RawMessage(args)
	if result != "" {
		a.Result = json.RawMessage(result)
	}
	a.DecidedAt, a.CreatedAt = decided.Ptr(), created.T
	return &a, nil
}

// ListAIActions filtered by status.
func (s *Store) ListAIActions(ctx context.Context, tenantID string, status AIActionStatus, limit int) ([]*AIAction, error) {
	if limit <= 0 {
		limit = 100
	}
	conds, args := "tenant_id = ?", []any{tenantID}
	if status != "" {
		conds += " AND status = ?"
		args = append(args, string(status))
	}
	rows, err := s.db.QueryContext(ctx, s.Q(
		`SELECT id, tenant_id, conversation_id, tool, args, summary, status, actor, result, decided_by, decided_at, created_at
		 FROM ai_actions WHERE `+conds+` ORDER BY created_at DESC LIMIT `+fmt.Sprint(limit)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AIAction
	for rows.Next() {
		a, err := scanAIAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AddAIUsage accumulates token spend for the budget gate (SPEC §10.2).
func (s *Store) AddAIUsage(ctx context.Context, month string, tokensIn, tokensOut int64) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.Q(
			`INSERT INTO ai_usage (month, tokens_in, tokens_out) VALUES (?,?,?)
			 ON CONFLICT (month) DO UPDATE SET
			 tokens_in = ai_usage.tokens_in + excluded.tokens_in,
			 tokens_out = ai_usage.tokens_out + excluded.tokens_out`),
			month, tokensIn, tokensOut)
		return err
	})
}

// AIUsage returns the month's spend.
func (s *Store) AIUsage(ctx context.Context, month string) (in, out int64, err error) {
	err = s.db.QueryRowContext(ctx, s.Q(
		`SELECT tokens_in, tokens_out FROM ai_usage WHERE month = ?`), month).Scan(&in, &out)
	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	return in, out, err
}

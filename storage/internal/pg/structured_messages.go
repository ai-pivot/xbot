package pg

// structured_messages.go — PostgreSQL port of storage/sqlite/structured_messages.go.
//
// One structured message is one session_messages row: kind / correlation_id /
// reply_to / idempotency_key / structured_json / direction / answered /
// cancelled, on top of the regular role/content/sender_* columns. Visibility is
// unchanged (record_type stays 'message'), so the existing display projection and
// the LLM replay pick these rows up exactly like SQLite does.
//
// Backend differences, same semantics:
//   - idempotency is guaranteed by the partial unique index
//     ux_sm_chat_idempotency (chat_id, idempotency_key) WHERE idempotency_key <> ''
//     instead of SQLite's per-tenant write lock: a concurrent duplicate surfaces
//     as SQLSTATE 23505 and is resolved by reading the existing row back;
//   - INSERT ... RETURNING id replaces LastInsertId;
//   - answered/cancelled are native BOOLEAN, created_at is TIMESTAMPTZ;
//   - the CAS steps (AppendDecision / AnswerAskUser) run in one transaction with
//     SELECT ... FOR UPDATE on the target row, which reproduces SQLite's
//     "one immediate-write transaction" serialization.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	common "xbot/storage/common"
)

// structuredMessageColumns is the shared projection. Nullable TEXT columns are
// COALESCEd (older rows predate the ALTERs), booleans default to false.
const structuredMessageColumns = `id, COALESCE(chat_id, ''), COALESCE(kind, ''),
	COALESCE(correlation_id, ''), COALESCE(reply_to, ''), COALESCE(idempotency_key, ''),
	COALESCE(structured_json, ''), COALESCE(direction, ''), COALESCE(answered, false),
	COALESCE(cancelled, false), role, content, COALESCE(sender_type, ''),
	COALESCE(sender_id, ''), COALESCE(sender_name, ''), created_at`

// structuredScanner is satisfied by both pgx.Row and pgx.Rows.
type structuredScanner interface {
	Scan(dest ...any) error
}

func scanPGStructuredRow(sc structuredScanner) (common.StructuredMessage, error) {
	var m common.StructuredMessage
	var createdAt time.Time
	if err := sc.Scan(&m.ID, &m.ChatID, &m.Kind, &m.CorrelationID, &m.ReplyTo,
		&m.IdempotencyKey, &m.StructuredJSON, &m.Direction, &m.Answered, &m.Cancelled,
		&m.Role, &m.Content, &m.SenderType, &m.SenderID, &m.SenderName, &createdAt); err != nil {
		return common.StructuredMessage{}, err
	}
	m.CreatedAt = createdAt
	return m, nil
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (SQLSTATE 23505). The idempotency index is the only unique index
// these inserts can hit.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// lookupStructuredByKey reads the row already holding (tenant_id, chat_id,
// idempotency_key). It reports pgx.ErrNoRows when the key is unused.
func (s *sessionStore) lookupStructuredByKey(ctx context.Context, q structuredQueryer, tenantID int64, chatID, key string) (int64, error) {
	var id int64
	err := q.QueryRow(ctx,
		`SELECT id FROM session_messages
		  WHERE tenant_id = $1 AND chat_id = $2 AND idempotency_key = $3
		  LIMIT 1`, tenantID, sanitizeText(chatID), sanitizeText(key)).Scan(&id)
	return id, err
}

// structuredQueryer is satisfied by *pgxpool.Pool and pgx.Tx.
type structuredQueryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// AppendStructuredMessage idempotently writes one structured message.
//
// Idempotency: the same (chat_id, idempotency_key) returns the existing row id
// with created=false; an empty key never participates and always inserts.
func (s *sessionStore) AppendStructuredMessage(tenantID int64, m common.StructuredMessage) (id int64, created bool, err error) {
	if m.Kind == "" {
		m.Kind = common.StructuredKindText
	}
	if !common.StructuredMessageKinds[m.Kind] {
		return 0, false, fmt.Errorf("invalid structured message kind %q", m.Kind)
	}
	if m.Direction == "" {
		m.Direction = "in"
	}
	if m.Direction != "in" && m.Direction != "out" {
		return 0, false, fmt.Errorf("invalid direction %q", m.Direction)
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	ctx := context.Background()

	if m.IdempotencyKey != "" {
		existing, qerr := s.lookupStructuredByKey(ctx, s.pool, tenantID, m.ChatID, m.IdempotencyKey)
		if qerr == nil {
			return existing, false, nil
		}
		if !errors.Is(qerr, pgx.ErrNoRows) {
			return 0, false, fmt.Errorf("lookup idempotency key: %w", qerr)
		}
	}

	err = s.pool.QueryRow(ctx, `
		INSERT INTO session_messages
		(tenant_id, role, content, display_only, record_type, created_at, turn_id,
		 chat_id, kind, correlation_id, reply_to, idempotency_key, structured_json,
		 direction, answered, cancelled, sender_type, sender_id, sender_name)
		VALUES ($1, $2, $3, false, 'message', $4, 0,
		        $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING id`,
		tenantID, sanitizeText(common.StructuredRole(m)), sanitizeText(m.Content), m.CreatedAt,
		sanitizeText(m.ChatID), m.Kind, sanitizeText(m.CorrelationID), sanitizeText(m.ReplyTo),
		sanitizeText(m.IdempotencyKey), sanitizeText(m.StructuredJSON), m.Direction,
		m.Answered, m.Cancelled, sanitizeText(m.SenderType), sanitizeText(m.SenderID),
		sanitizeText(m.SenderName)).Scan(&id)
	if err != nil {
		// Concurrent duplicate: the unique index rejected the second writer.
		// Read the winner back so the call stays idempotent.
		if m.IdempotencyKey != "" && isUniqueViolation(err) {
			if existing, qerr := s.lookupStructuredByKey(ctx, s.pool, tenantID, m.ChatID, m.IdempotencyKey); qerr == nil {
				return existing, false, nil
			}
		}
		return 0, false, fmt.Errorf("insert structured message: %w", err)
	}
	return id, true, nil
}

// AppendDecision atomically writes a decision (kind=decision) and marks its
// target row answered. Same CAS closed loop as AnswerAskUser:
//   - missing target (m.ReplyTo) -> ErrStructuredNotFound
//   - target already answered -> ErrAskUserAnswered (also with a different key)
//   - target already cancelled -> ErrAskUserCancelled
//   - target kind not report/ask_user -> ErrDecisionTargetNotDecidable
//   - target submitted by the deciding agent -> ErrDecisionSelfApproval
//   - same (chat_id, idempotency_key) -> existing row, created=false
//
// "check idempotency before CAS" is deliberate: a double click with the same key
// must return the original decision row (200), not fail with 409 because the
// target was already answered by that very decision.
func (s *sessionStore) AppendDecision(tenantID int64, m common.StructuredMessage) (id int64, created bool, err error) {
	if m.Kind == "" {
		m.Kind = common.StructuredKindDecision
	}
	if m.Kind != common.StructuredKindDecision {
		return 0, false, fmt.Errorf("invalid decision kind %q", m.Kind)
	}
	targetID, perr := strconv.ParseInt(strings.TrimSpace(m.ReplyTo), 10, 64)
	if perr != nil || targetID <= 0 {
		return 0, false, fmt.Errorf("invalid decision target %q", m.ReplyTo)
	}
	if m.Direction == "" {
		m.Direction = "in"
	}
	if m.Direction != "in" && m.Direction != "out" {
		return 0, false, fmt.Errorf("invalid direction %q", m.Direction)
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}

	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Idempotency: a repeated key returns the existing row and touches nothing.
	if m.IdempotencyKey != "" {
		existing, qerr := s.lookupStructuredByKey(ctx, tx, tenantID, m.ChatID, m.IdempotencyKey)
		if qerr == nil {
			if cerr := tx.Commit(ctx); cerr != nil {
				return 0, false, cerr
			}
			return existing, false, nil
		}
		if !errors.Is(qerr, pgx.ErrNoRows) {
			return 0, false, fmt.Errorf("lookup decision idempotency key: %w", qerr)
		}
	}

	// 2. The target must exist and still be open. FOR UPDATE serializes
	//    concurrent decisions on the same target (SQLite's write lock).
	//
	// chat_id is read as well: the row id is only unique inside one session DB,
	// so keying on (tenant_id, id) alone would let a decision in session A close
	// a same-numbered report in session B.
	var answered, cancelled bool
	var targetKind, targetSender, targetChat string
	qerr := tx.QueryRow(ctx, `
		SELECT COALESCE(answered, false), COALESCE(cancelled, false), COALESCE(kind, ''),
		       COALESCE(sender_id, ''), COALESCE(chat_id, '')
		  FROM session_messages WHERE tenant_id = $1 AND id = $2
		  FOR UPDATE`, tenantID, targetID).
		Scan(&answered, &cancelled, &targetKind, &targetSender, &targetChat)
	if errors.Is(qerr, pgx.ErrNoRows) {
		return 0, false, common.ErrStructuredNotFound
	}
	if qerr != nil {
		return 0, false, fmt.Errorf("lookup decision target: %w", qerr)
	}
	if targetKind != common.StructuredKindReport && targetKind != common.StructuredKindAskUser {
		return 0, false, fmt.Errorf("%w (target %d has kind=%q)", common.ErrDecisionTargetNotDecidable, targetID, targetKind)
	}
	// Cross-session guard: when both sides carry a chat_id they must match.
	// Historical rows written before 2026-10-22 have an empty chat_id and stay
	// decidable for backward compatibility.
	if targetChat != "" && m.ChatID != "" && targetChat != m.ChatID {
		return 0, false, common.ErrStructuredNotFound
	}
	// Self-approval ban: an agent must not approve its own report. Only enforced
	// when the sender is identifiable ("agent:" prefix); rows written before
	// 2026-10-21 are recorded as "web-4" and cannot distinguish a sub-agent
	// self-approval from a main-session proxy approval.
	if strings.HasPrefix(targetSender, "agent:") && targetSender == m.SenderID {
		return 0, false, fmt.Errorf("%w (target %d was submitted by %q)", common.ErrDecisionSelfApproval, targetID, targetSender)
	}
	if cancelled {
		return 0, false, common.ErrAskUserCancelled
	}
	if answered {
		return 0, false, common.ErrAskUserAnswered
	}

	// 3. CAS: only an unanswered, uncancelled report/ask_user can advance
	//    (concurrent decisions: exactly one wins).
	tag, uerr := tx.Exec(ctx, `
		UPDATE session_messages SET answered = true
		 WHERE tenant_id = $1 AND id = $2 AND (chat_id = $3 OR chat_id = '')
		   AND answered = false AND cancelled = false AND kind IN ($4, $5)`,
		tenantID, targetID, sanitizeText(m.ChatID), common.StructuredKindReport, common.StructuredKindAskUser)
	if uerr != nil {
		return 0, false, fmt.Errorf("mark decision target answered: %w", uerr)
	}
	if tag.RowsAffected() == 0 {
		return 0, false, common.ErrAskUserAnswered
	}

	// 4. Write the decision row (reply_to points at the approved target).
	var newID int64
	ierr := tx.QueryRow(ctx, `
		INSERT INTO session_messages
		(tenant_id, role, content, display_only, record_type, created_at, turn_id,
		 chat_id, kind, correlation_id, reply_to, idempotency_key, structured_json,
		 direction, answered, cancelled, sender_type, sender_id, sender_name)
		VALUES ($1, $2, $3, false, 'message', $4, 0,
		        $5, 'decision', $6, $7, $8, $9, $10, false, false, $11, $12, $13)
		RETURNING id`,
		tenantID, sanitizeText(common.StructuredRole(m)), sanitizeText(m.Content), m.CreatedAt,
		sanitizeText(m.ChatID), sanitizeText(m.CorrelationID), sanitizeText(m.ReplyTo),
		sanitizeText(m.IdempotencyKey), sanitizeText(m.StructuredJSON), m.Direction,
		sanitizeText(m.SenderType), sanitizeText(m.SenderID), sanitizeText(m.SenderName)).Scan(&newID)
	if ierr != nil {
		// Concurrent duplicate key: the transaction is already aborted, so read
		// the winner back through the pool after rolling back.
		if m.IdempotencyKey != "" && isUniqueViolation(ierr) {
			_ = tx.Rollback(ctx)
			if existing, qerr := s.lookupStructuredByKey(ctx, s.pool, tenantID, m.ChatID, m.IdempotencyKey); qerr == nil {
				return existing, false, nil
			}
		}
		return 0, false, fmt.Errorf("insert decision: %w", ierr)
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return 0, false, cerr
	}
	return newID, true, nil
}

// AnswerAskUser atomically marks an ask_user answered/cancelled and writes a
// kind=answer row. Returns (answerID, questionID, created, error).
//
// Idempotency (a double click only takes effect once):
//   - an answer row already exists for the correlation:
//     · same idempotency_key -> return it (created=false);
//     · different/empty key  -> ErrAskUserAnswered.
//   - already cancelled -> ErrAskUserCancelled (the cancel request itself is
//     idempotent: the same key returns the existing row).
func (s *sessionStore) AnswerAskUser(tenantID int64, chatID, correlationID, answerText, structuredJSON, idempotencyKey, senderID, senderName string, cancelled bool) (answerID, questionID int64, created bool, err error) {
	if strings.TrimSpace(correlationID) == "" {
		return 0, 0, false, common.ErrStructuredNotFound
	}

	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Locate the question row (a correlation may be delivered more than once;
	//    the newest row wins).
	var answered, cancelledFlag bool
	var qChat string
	qerr := tx.QueryRow(ctx, `
		SELECT id, COALESCE(chat_id, ''), COALESCE(answered, false), COALESCE(cancelled, false)
		  FROM session_messages
		 WHERE tenant_id = $1 AND kind = 'ask_user' AND correlation_id = $2
		 ORDER BY id DESC LIMIT 1
		 FOR UPDATE`, tenantID, sanitizeText(correlationID)).
		Scan(&questionID, &qChat, &answered, &cancelledFlag)
	if errors.Is(qerr, pgx.ErrNoRows) {
		return 0, 0, false, common.ErrStructuredNotFound
	}
	if qerr != nil {
		return 0, 0, false, fmt.Errorf("lookup ask_user: %w", qerr)
	}
	if chatID == "" {
		chatID = qChat
	}

	// 2. Existing answer row -> idempotent return or rejection.
	var existingAnswerID int64
	var existingKey string
	var existingCancelled bool
	aerr := tx.QueryRow(ctx, `
		SELECT id, COALESCE(idempotency_key, ''), COALESCE(cancelled, false)
		  FROM session_messages
		 WHERE tenant_id = $1 AND kind = 'answer' AND correlation_id = $2
		 ORDER BY id DESC LIMIT 1`, tenantID, sanitizeText(correlationID)).
		Scan(&existingAnswerID, &existingKey, &existingCancelled)
	if aerr == nil {
		if idempotencyKey != "" && existingKey == idempotencyKey {
			if cerr := tx.Commit(ctx); cerr != nil {
				return 0, 0, false, cerr
			}
			return existingAnswerID, questionID, false, nil
		}
		if existingCancelled {
			return 0, 0, false, common.ErrAskUserCancelled
		}
		return 0, 0, false, common.ErrAskUserAnswered
	}
	if !errors.Is(aerr, pgx.ErrNoRows) {
		return 0, 0, false, fmt.Errorf("lookup answer: %w", aerr)
	}

	// 3. Already cancelled -> reject; a cancel request is idempotent (returns the
	//    original question row).
	if cancelledFlag {
		if cancelled {
			if cerr := tx.Commit(ctx); cerr != nil {
				return 0, 0, false, cerr
			}
			return questionID, questionID, false, nil
		}
		return 0, 0, false, common.ErrAskUserCancelled
	}

	// 4. CAS: only an unanswered, uncancelled question advances (concurrent
	//    double clicks: exactly one wins).
	var tag pgconn.CommandTag
	var uerr error
	if cancelled {
		tag, uerr = tx.Exec(ctx, `
			UPDATE session_messages SET cancelled = true
			 WHERE tenant_id = $1 AND id = $2 AND answered = false AND cancelled = false`,
			tenantID, questionID)
	} else {
		tag, uerr = tx.Exec(ctx, `
			UPDATE session_messages SET answered = true
			 WHERE tenant_id = $1 AND id = $2 AND answered = false AND cancelled = false`,
			tenantID, questionID)
	}
	if uerr != nil {
		flag := "answered"
		if cancelled {
			flag = "cancelled"
		}
		return 0, 0, false, fmt.Errorf("mark ask_user %s: %w", flag, uerr)
	}
	if tag.RowsAffected() == 0 {
		return 0, 0, false, common.ErrAskUserAnswered
	}

	// 5. Write the answer row.
	if senderName == "" {
		senderName = senderID
	}
	var newID int64
	ierr := tx.QueryRow(ctx, `
		INSERT INTO session_messages
		(tenant_id, role, content, display_only, record_type, created_at, turn_id,
		 chat_id, kind, correlation_id, reply_to, idempotency_key, structured_json,
		 direction, answered, cancelled, sender_type, sender_id, sender_name)
		VALUES ($1, 'user', $2, false, 'message', $3, 0,
		        $4, 'answer', $5, $6, $7, $8, 'in', false, $9, 'user', $10, $11)
		RETURNING id`,
		tenantID, sanitizeText(answerText), time.Now(),
		sanitizeText(chatID), sanitizeText(correlationID), strconv.FormatInt(questionID, 10),
		sanitizeText(idempotencyKey), sanitizeText(structuredJSON), cancelled,
		sanitizeText(senderID), sanitizeText(senderName)).Scan(&newID)
	if ierr != nil {
		// Concurrent duplicate key: aborted transaction -> read the winner back
		// through the pool.
		if idempotencyKey != "" && isUniqueViolation(ierr) {
			_ = tx.Rollback(ctx)
			if existing, qerr := s.lookupStructuredByKey(ctx, s.pool, tenantID, chatID, idempotencyKey); qerr == nil {
				return existing, questionID, false, nil
			}
		}
		return 0, 0, false, fmt.Errorf("insert answer: %w", ierr)
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return 0, 0, false, cerr
	}
	return newID, questionID, true, nil
}

// MarkAskUserCancelled cancels an unanswered ask_user (writes kind=answer,
// cancelled=true).
func (s *sessionStore) MarkAskUserCancelled(tenantID int64, chatID, correlationID, reason, idempotencyKey, senderID string) (int64, int64, bool, error) {
	payload, _ := json.Marshal(map[string]any{
		"answers":     []any{},
		"cancelled":   true,
		"answered_by": senderID,
		"reason":      reason,
	})
	return s.AnswerAskUser(tenantID, chatID, correlationID, "已取消", string(payload), idempotencyKey, senderID, senderID, true)
}

// ListStructuredMessages pages structured messages by kind ("" = all), newest
// first. cursor is the last id of the previous page.
func (s *sessionStore) ListStructuredMessages(tenantID int64, kind string, cursor int64, limit int) ([]common.StructuredMessage, bool, int64, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	q := `SELECT ` + structuredMessageColumns + `
	        FROM session_messages
	       WHERE tenant_id = $1 AND kind <> ''`
	args := []any{tenantID}
	if kind != "" {
		args = append(args, kind)
		q += fmt.Sprintf(` AND kind = $%d`, len(args))
	}
	if cursor > 0 {
		args = append(args, cursor)
		q += fmt.Sprintf(` AND id < $%d`, len(args))
	}
	args = append(args, limit+1)
	q += fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, len(args))

	out, err := s.queryStructuredMessages(q, args, limit)
	if err != nil {
		return nil, false, 0, fmt.Errorf("query structured messages: %w", err)
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	next := int64(0)
	if len(out) > 0 {
		next = out[len(out)-1].ID
	}
	return out, hasMore, next, nil
}

// ListStructuredMessagesAfter returns id > afterID in ascending order. Used by
// the SSE reconnect catch-up: only pulling the newest page would silently drop
// answer/decision rows from deep history, and derived state (answered/cancelled)
// would regress to "pending" after a reconnect.
func (s *sessionStore) ListStructuredMessagesAfter(tenantID int64, kind string, afterID int64, limit int) ([]common.StructuredMessage, bool, int64, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 200 {
		limit = 200
	}
	q := `SELECT ` + structuredMessageColumns + `
	        FROM session_messages
	       WHERE tenant_id = $1 AND kind <> '' AND id > $2`
	args := []any{tenantID, afterID}
	if kind != "" {
		args = append(args, kind)
		q += fmt.Sprintf(` AND kind = $%d`, len(args))
	}
	args = append(args, limit+1)
	q += fmt.Sprintf(` ORDER BY id ASC LIMIT $%d`, len(args))

	out, err := s.queryStructuredMessages(q, args, limit)
	if err != nil {
		return nil, false, 0, fmt.Errorf("query structured messages after %d: %w", afterID, err)
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	next := afterID
	if len(out) > 0 {
		next = out[len(out)-1].ID
	}
	return out, hasMore, next, nil
}

// ListInbox returns the structured messages awaiting a human: ask_user and
// report, both leaving the inbox via answered/cancelled. Newest first, cursor =
// last id of the previous page.
func (s *sessionStore) ListInbox(tenantID int64, cursor int64, limit int) ([]common.StructuredMessage, bool, int64, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	q := `SELECT ` + structuredMessageColumns + `
	        FROM session_messages
	       WHERE tenant_id = $1
	         AND kind IN ('ask_user', 'report')
	         AND answered = false AND cancelled = false`
	args := []any{tenantID}
	if cursor > 0 {
		args = append(args, cursor)
		q += fmt.Sprintf(` AND id < $%d`, len(args))
	}
	args = append(args, limit+1)
	q += fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, len(args))

	out, err := s.queryStructuredMessages(q, args, limit)
	if err != nil {
		return nil, false, 0, fmt.Errorf("query inbox: %w", err)
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	next := int64(0)
	if len(out) > 0 {
		next = out[len(out)-1].ID
	}
	return out, hasMore, next, nil
}

// queryStructuredMessages runs a structured-message projection query.
func (s *sessionStore) queryStructuredMessages(q string, args []any, capacity int) ([]common.StructuredMessage, error) {
	rows, err := s.pool.Query(context.Background(), q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]common.StructuredMessage, 0, capacity)
	for rows.Next() {
		m, err := scanPGStructuredRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// CountInbox returns the number of pending structured messages (badge count).
func (s *sessionStore) CountInbox(tenantID int64) (int, error) {
	var n int64
	if err := s.pool.QueryRow(context.Background(), `
		SELECT COUNT(1) FROM session_messages
		 WHERE tenant_id = $1
		   AND kind IN ('ask_user', 'report')
		   AND answered = false AND cancelled = false`,
		tenantID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count inbox: %w", err)
	}
	return int(n), nil
}

// GetStructuredMessageByID reads one structured message row.
func (s *sessionStore) GetStructuredMessageByID(tenantID, id int64) (*common.StructuredMessage, error) {
	m, err := scanPGStructuredRow(s.pool.QueryRow(context.Background(),
		`SELECT `+structuredMessageColumns+`
		   FROM session_messages WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, common.ErrStructuredNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

package sqlite

import (
	"database/sql"
	"fmt"
	"strings"

	"xbot/llm"
	log "xbot/logger"
)

// SessionService handles session message operations
type SessionService struct {
	db *DB
}

// NewSessionService creates a new session service
func NewSessionService(db *DB) *SessionService {
	return &SessionService{db: db}
}

// conn returns the underlying database connection.
// Returns an error if the database has been closed (nil connection).
func (s *SessionService) conn() (*sql.DB, error) {
	c := s.db.Conn()
	if c == nil {
		return nil, fmt.Errorf("database connection is closed")
	}
	return c, nil
}

// AddMessage adds a message to a tenant's session
func (s *SessionService) AddMessage(tenantID int64, msg llm.ChatMessage) error {
	_, err := s.AddMessageWithID(tenantID, msg)
	return err
}

// AddMessageWithID adds a message and returns the auto-increment id from the DB.
// Delegates to AppendMessage which acquires the striped historyLock, ensuring
// append-only mutations are serialized per tenant.
func (s *SessionService) AddMessageWithID(tenantID int64, msg llm.ChatMessage) (int64, error) {
	return s.AppendMessage(tenantID, msg)
}

// ReplaceToolMessage updates the most recent matching tool-role message.
//
// Parameters:
//   - toolName:    filter by tool_name. Empty string = match any (wildcard).
//   - toolCallID:  filter by tool_call_id. Empty string = match any (wildcard).
//   - content:     new content to write.
//
// Returns sql.ErrNoRows if no matching message exists.
func (s *SessionService) ReplaceToolMessage(tenantID int64, toolName, toolCallID, content string) error {
	lock := s.db.historyLock(tenantID)
	lock.Lock()
	defer lock.Unlock()
	return s.withImmediateHistoryWrite(func(store historyQueryExecer) error {
		if toolName == "AskUser" {
			_, err := validateAndAppendAskAnswerWith(store, tenantID, content)
			return err
		}
		replay, err := replayWith(store, tenantID)
		if err != nil {
			return err
		}
		for i := len(replay.Messages) - 1; i >= 0; i-- {
			msg := replay.Messages[i]
			if msg.Role == "tool" && (toolName == "" || msg.ToolName == toolName) && (toolCallID == "" || msg.ToolCallID == toolCallID) {
				msg.Content = content
				occurrence := 0
				for j := 0; j < i; j++ {
					if replay.Messages[j].ID == msg.ID {
						occurrence++
					}
				}
				_, err := appendControlWith(store, tenantID, HistoryRecordContextEdit, msg.ID, MessageMutations{Mutations: []MessageMutation{{TargetHistoryID: msg.ID, TargetOccurrence: occurrence, Message: msg}}})
				return err
			}
		}
		return sql.ErrNoRows
	})
}

// GetHistory retrieves the most recent messages for a tenant.
// limit specifies the minimum number of user/assistant messages to return.
// Tool messages between them are included to maintain context continuity.
// display_only messages (e.g. cron results) are excluded from LLM context.
// GetHistory returns the last `limit` messages (rows). If beforeID > 0,
// returns messages with id < beforeID only.
func (s *SessionService) GetHistory(tenantID int64, limit int) ([]llm.ChatMessage, error) {
	return s.GetHistoryBefore(tenantID, 0, limit)
}

// GetHistoryBefore returns up to `limit` raw history messages (rows) that
// occur before beforeID. If beforeID <= 0, returns the most recent `limit`
// messages. The result is chronologically ordered (oldest first).
//
// The limit counts MESSAGES, not user turns. Counting turns let a single
// turn with many tool/assistant rows balloon the returned window to millions
// of tokens (one turn commonly holds 10-50 rows). Bounding by message count
// keeps the payload proportional to what the client can render.
func (s *SessionService) GetHistoryBefore(tenantID int64, beforeID int64, limit int) ([]llm.ChatMessage, error) {
	replay, err := s.Replay(tenantID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	msgs := replay.Messages
	// If beforeID specified, slice to only messages with id < beforeID.
	if beforeID > 0 {
		cut := len(msgs)
		for i, m := range msgs {
			if m.ID >= beforeID {
				cut = i
				break
			}
		}
		msgs = msgs[:cut]
		if len(msgs) == 0 {
			return nil, nil
		}
	}
	// Walk backwards counting messages; start is the first message of the
	// window (oldest). Messages are already in chronological order.
	start := 0
	if len(msgs) > limit {
		start = len(msgs) - limit
	}
	return append([]llm.ChatMessage(nil), msgs[start:]...), nil
}

// GetHistoryBeforeForDisplay returns up to `limit` messages (including
// pre-compression) before beforeID, plus the total count of messages
// before beforeID (for has_more pagination).
// Unlike GetHistoryBefore which uses Replay() (replacing old messages with
// the compress summary), this uses ReplayForDisplay() which preserves all
// messages from the append-only session_messages table.
//
// total is the count of messages with id < beforeID — not the full session
// count — so has_more = (total > len(returned)) converges to false at the
// earliest page instead of staying true forever.
func (s *SessionService) GetHistoryBeforeForDisplay(tenantID int64, beforeID int64, limit int) ([]llm.ChatMessage, int, error) {
	if limit <= 0 {
		return nil, 0, nil
	}
	lock := s.db.historyLock(tenantID)
	lock.Lock()
	defer lock.Unlock()
	conn, err := s.conn()
	if err != nil {
		return nil, 0, err
	}
	replay, total, err := replayForDisplayWindow(conn, tenantID, beforeID, int64(limit))
	if err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}
	return replay.Messages, total, nil
}

// GetAllMessages retrieves all non-display-only messages for a tenant.
// Used by memory consolidation and context building.
//
// Design decision: display_only messages (e.g. cron task results) are intentionally
// excluded because they are produced by an independent agent loop with no shared
// conversation context. Including them in consolidation would inject unrelated content
// into the user's long-term memory summary. If future features need to retrieve cron
// execution history, a dedicated query (without the display_only filter) should be added.
func (s *SessionService) GetAllMessages(tenantID int64) ([]llm.ChatMessage, error) {
	replay, err := s.Replay(tenantID)
	if err != nil {
		return nil, err
	}
	return replay.Messages, nil
}

// GetMessagesCount returns the number of active messages for a tenant.
// Uses a checkpoint-aware SQL count instead of full Replay() to avoid
// deserializing all control records just to count messages.
func (s *SessionService) GetMessagesCount(tenantID int64) (int, error) {
	return s.countActiveMessages(tenantID, false)
}

// GetUserMessageCount returns the number of user-role messages for a tenant.
// Used by consolidation logic to count conversation turns, not raw message rows
// (which include tool calls, assistant iterations, etc.).
// Excludes display_only messages (cron results).
func (s *SessionService) GetUserMessageCount(tenantID int64) (int, error) {
	return s.countActiveMessages(tenantID, true)
}

// Clear removes all messages for a tenant
func (s *SessionService) Clear(tenantID int64) error {
	lock := s.db.historyLock(tenantID)
	lock.Lock()
	defer lock.Unlock()
	conn, err := s.conn()
	if err != nil {
		return err
	}
	// Delete iteration_history first (no FK cascade when foreign_keys=OFF).
	_, _ = conn.Exec("DELETE FROM iteration_history WHERE tenant_id = ?", tenantID)
	result, err := conn.Exec("DELETE FROM session_messages WHERE tenant_id = ?", tenantID)
	if err != nil {
		return fmt.Errorf("clear session messages: %w", err)
	}
	rows, _ := result.RowsAffected()
	log.WithFields(log.Fields{
		"tenant_id": tenantID,
		"messages":  rows,
	}).Debug("Session messages cleared")
	return nil
}

// UpdateMessageContent updates the content of the Nth message (0-indexed) for a tenant.
// Used by observation masking to persist masked content back to session.
func (s *SessionService) UpdateMessageContent(tenantID int64, messageIndex int, content string) error {
	return s.UpdateMessageContentNonDisplayOnly(tenantID, messageIndex, content)
}

// UpdateMessageContentNonDisplayOnly updates the content of the Nth non-display-only message (0-indexed) for a tenant.
// The index corresponds to the ordering used by GetAllMessages (which excludes display_only messages).
// Used by context_edit persistence to sync in-memory edits back to the database.
func (s *SessionService) UpdateMessageContentNonDisplayOnly(tenantID int64, messageIndex int, content string) error {
	lock := s.db.historyLock(tenantID)
	lock.Lock()
	defer lock.Unlock()
	return s.withImmediateHistoryWrite(func(store historyQueryExecer) error {
		replay, err := replayWith(store, tenantID)
		if err != nil {
			return err
		}
		if messageIndex < 0 || messageIndex >= len(replay.Messages) {
			return fmt.Errorf("no non-display-only message found at index %d for tenant %d", messageIndex, tenantID)
		}
		msg := replay.Messages[messageIndex]
		msg.Content = content
		occurrence := 0
		for i := 0; i < messageIndex; i++ {
			if replay.Messages[i].ID == msg.ID {
				occurrence++
			}
		}
		_, err = appendControlWith(store, tenantID, HistoryRecordContextEdit, msg.ID, MessageMutations{Mutations: []MessageMutation{{TargetHistoryID: msg.ID, TargetOccurrence: occurrence, Message: msg}}})
		return err
	})
}

// UpdateUserMessageContextTokens sets the context_tokens field on the most recent
// user-role message for a tenant. This records the exact API prompt_tokens at the
// time that user message was sent, enabling precise token accounting for rewind.
func (s *SessionService) UpdateUserMessageContextTokens(tenantID int64, promptTokens int64) error {
	lock := s.db.historyLock(tenantID)
	lock.Lock()
	defer lock.Unlock()
	conn, err := s.conn()
	if err != nil {
		return err
	}
	result, err := conn.Exec(`
UPDATE session_messages SET context_tokens = ?
WHERE id = (
SELECT id FROM session_messages
WHERE tenant_id = ? AND role = 'user' AND COALESCE(display_only, 0) = 0
ORDER BY id DESC LIMIT 1
)
`, promptTokens, tenantID)
	if err != nil {
		return fmt.Errorf("update user message context_tokens: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetLastUserMessageContextTokens returns the context_tokens of the most recent
// non-display-only user message for a tenant. Used by rewind to restore accurate
// token state. Returns (0, nil) if no user message or context_tokens is 0.
func (s *SessionService) GetLastUserMessageContextTokens(tenantID int64) (int64, error) {
	lock := s.db.historyLock(tenantID)
	lock.Lock()
	defer lock.Unlock()
	conn, err := s.conn()
	if err != nil {
		return 0, err
	}
	var tokens sql.NullInt64
	err = conn.QueryRow(`
SELECT context_tokens FROM session_messages
WHERE tenant_id = ? AND role = 'user' AND COALESCE(display_only, 0) = 0
ORDER BY id DESC LIMIT 1
`, tenantID).Scan(&tokens)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get last user message context_tokens: %w", err)
	}
	if tokens.Valid {
		return tokens.Int64, nil
	}
	return 0, nil
}

// GetMaxTurnID returns the highest turn_id for a tenant across BOTH
// session_messages and iteration_history. Used by chatProcessLoop to restore
// the per-session turn ID counter after a server restart, ensuring turn_id
// remains globally monotonic.
//
// It MUST include iteration_history: rewind truncates session_messages but (before
// the v56 fix) left orphaned iteration_history rows behind; restoring from
// session_messages alone would reuse those orphaned turn_ids and mix stale
// iterations into new turns. Taking the max over both tables keeps the counter
// past any orphaned turn_id.
// Returns 0 if neither table has a turn_id (new or legacy sessions).
func (s *SessionService) GetMaxTurnID(tenantID int64) (uint64, error) {
	conn, err := s.conn()
	if err != nil {
		return 0, err
	}
	var msgMax sql.NullInt64
	if err := conn.QueryRow(
		"SELECT MAX(turn_id) FROM session_messages WHERE tenant_id = ?", tenantID,
	).Scan(&msgMax); err != nil {
		return 0, fmt.Errorf("get max turn_id: %w", err)
	}
	var iterMax sql.NullInt64
	if err := conn.QueryRow(
		"SELECT MAX(turn_id) FROM iteration_history WHERE tenant_id = ?", tenantID,
	).Scan(&iterMax); err != nil {
		return 0, fmt.Errorf("get max iteration turn_id: %w", err)
	}
	max := uint64(0)
	if msgMax.Valid {
		max = uint64(msgMax.Int64)
	}
	if iterMax.Valid && uint64(iterMax.Int64) > max {
		max = uint64(iterMax.Int64)
	}
	return max, nil
}

// GetLastUserTurnID returns the turn_id of the LAST non-display-only user
// message in the session. A restart-resumed Run (InjectInboundResume) reuses
// this turn id so the interrupted work and the resumed work belong to ONE turn
// — the frontend renders a single assistant block instead of one per restart.
// Returns 0 when the session has no user message or the last one predates
// turn_id stamping (legacy rows) — the caller then falls back to allocating a
// fresh turn id.
func (s *SessionService) GetLastUserTurnID(tenantID int64) (uint64, error) {
	conn, err := s.conn()
	if err != nil {
		return 0, err
	}
	var turnID sql.NullInt64
	if err := conn.QueryRow(`
		SELECT turn_id FROM session_messages
		WHERE tenant_id = ? AND role = 'user' AND COALESCE(display_only, 0) = 0
		ORDER BY id DESC LIMIT 1
	`, tenantID).Scan(&turnID); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("get last user turn_id: %w", err)
	}
	if !turnID.Valid || turnID.Int64 <= 0 {
		return 0, nil
	}
	return uint64(turnID.Int64), nil
}

// GetMaxIterationForTurn returns the highest iteration number recorded for a
// turn in iteration_history. A restart-resumed Run uses it to CONTINUE the
// interrupted turn's iteration numbering (IterationStart offset): iteration
// numbers are turn-scoped ((turn_id, iteration) uniqueness), so restarting at 1
// would collide with the interrupted Run's records and break the frontend's
// per-turn iteration advance/merge checks. Returns 0 when the turn has no
// records (fresh turn — a resume of a turn interrupted before its first
// iteration snapshot).
func (s *SessionService) GetMaxIterationForTurn(tenantID int64, turnID uint64) (int, error) {
	conn, err := s.conn()
	if err != nil {
		return 0, err
	}
	var maxIter sql.NullInt64
	if err := conn.QueryRow(
		"SELECT MAX(iteration) FROM iteration_history WHERE tenant_id = ? AND turn_id = ?",
		tenantID, turnID,
	).Scan(&maxIter); err != nil {
		return 0, fmt.Errorf("get max iteration for turn: %w", err)
	}
	if !maxIter.Valid || maxIter.Int64 < 0 {
		return 0, nil
	}
	return int(maxIter.Int64), nil
}

// SetTenantCWD persists a session's current working directory in the tenants
// table (the single authoritative store; file-based session_cwd is retired).
func (s *SessionService) SetTenantCWD(tenantID int64, cwd string) error {
	conn, err := s.conn()
	if err != nil {
		return err
	}
	if _, err := conn.Exec("UPDATE tenants SET cwd = ? WHERE id = ?", cwd, tenantID); err != nil {
		return fmt.Errorf("update tenants.cwd: %w", err)
	}
	return nil
}

// GetTenantCWD reads a session's persisted CWD from the tenants table.
// Returns "" when the session has no persisted CWD (fresh session).
func (s *SessionService) GetTenantCWD(tenantID int64) (string, error) {
	conn, err := s.conn()
	if err != nil {
		return "", err
	}
	var cwd sql.NullString
	err = conn.QueryRow("SELECT cwd FROM tenants WHERE id = ?", tenantID).Scan(&cwd)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("get tenants.cwd: %w", err)
	}
	if cwd.Valid {
		return cwd.String, nil
	}
	return "", nil
}

// IterationRecord is a structured iteration history entry (v54+).
// Replaces the Detail JSON blob — every intermediate assistant message now
// has its iteration data in a dedicated table row, not just the final message.
type IterationRecord struct {
	MessageID int64  `json:"message_id"`
	TurnID    uint64 `json:"turn_id"`
	Iteration int    `json:"iteration"`
	Content   string `json:"content"`
	Reasoning string `json:"reasoning"`
	Tools     string `json:"tools"` // JSON array of tool snapshots
	// Tokens is the per-iteration completion-token count (not cumulative).
	Tokens int64 `json:"tokens"`
	// TTFTMs is the time-to-first-token for this iteration's LLM stream.
	TTFTMs int64 `json:"ttft_ms"`
	// TokensPerSec is the average generation speed for this iteration.
	TokensPerSec int64 `json:"tokens_per_sec"`
	// TotalMs is the total stream duration for this iteration.
	TotalMs int64 `json:"total_ms"`
	// TPOTMs is the time-per-output-token (ms) for this iteration's LLM stream.
	TPOTMs int64 `json:"tpot_ms"`
	// InputTokens is the prompt tokens of this iteration's LLM call(s) (v59).
	InputTokens int64 `json:"input_tokens"`
	// CachedTokens is the prompt-cache hit tokens of this iteration's LLM call(s) (v59).
	CachedTokens int64 `json:"cached_tokens"`
	// Model is the LLM model used for this iteration (v59).
	Model string `json:"model"`
}

// AppendIterationHistory inserts a single iteration record linked to a message.
func (s *SessionService) AppendIterationHistory(tenantID int64, msgID int64, turnID uint64, rec IterationRecord) error {
	conn, err := s.conn()
	if err != nil {
		return err
	}
	_, err = conn.Exec(`
		INSERT INTO iteration_history (message_id, tenant_id, turn_id, iteration, content, reasoning, tools, tokens, ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens, model)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, msgID, tenantID, turnID, rec.Iteration, rec.Content, rec.Reasoning, rec.Tools, rec.Tokens, rec.TTFTMs, rec.TokensPerSec, rec.TotalMs, rec.TPOTMs, rec.InputTokens, rec.CachedTokens, rec.Model)
	if err != nil {
		return fmt.Errorf("append iteration_history: %w", err)
	}
	return nil
}

// GetIterationHistoryByTurn returns all iteration records for a given
// (tenant_id, turn_id) pair, ordered by iteration number. This is the
// ONLY query method used by ConvertMessagesToHistoryWithIterations.
func (s *SessionService) GetIterationHistoryByTurn(tenantID int64, turnID uint64) ([]IterationRecord, error) {
	conn, err := s.conn()
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(`
		SELECT message_id, turn_id, iteration, content, reasoning, tools, tokens, ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens, model
		FROM iteration_history
		WHERE tenant_id = ? AND turn_id = ?
		ORDER BY iteration ASC
	`, tenantID, turnID)
	if err != nil {
		return nil, fmt.Errorf("get iteration_history by turn: %w", err)
	}
	defer rows.Close()
	return scanIterationRecords(rows)
}

// GetIterationHistoryByTurns 批量查询多个 turn 的 iteration_history —— 一次
// IN 查询替代循环单查。history 接口原来对每个 turn 单查一次 DB（100 条消息
// 可能 10-30 个 turn → 10-30 次 SQLite 查询），是接口慢的主要根源。
func (s *SessionService) GetIterationHistoryByTurns(tenantID int64, turnIDs []uint64) (map[uint64][]IterationRecord, error) {
	result := make(map[uint64][]IterationRecord)
	if len(turnIDs) == 0 {
		return result, nil
	}
	conn, err := s.conn()
	if err != nil {
		return nil, err
	}
	placeholders := make([]string, len(turnIDs))
	args := make([]any, 0, len(turnIDs)+1)
	args = append(args, tenantID)
	for i, id := range turnIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := fmt.Sprintf(`
		SELECT message_id, turn_id, iteration, content, reasoning, tools, tokens, ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens, model
		FROM iteration_history
		WHERE tenant_id = ? AND turn_id IN (%s)
		ORDER BY turn_id ASC, iteration ASC
	`, strings.Join(placeholders, ","))
	rows, err := conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get iteration_history by turns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rec IterationRecord
		if err := rows.Scan(&rec.MessageID, &rec.TurnID, &rec.Iteration, &rec.Content, &rec.Reasoning, &rec.Tools, &rec.Tokens, &rec.TTFTMs, &rec.TokensPerSec, &rec.TotalMs, &rec.TPOTMs, &rec.InputTokens, &rec.CachedTokens, &rec.Model); err != nil {
			continue
		}
		result[rec.TurnID] = append(result[rec.TurnID], rec)
	}
	return result, nil
}

func scanIterationRecords(rows *sql.Rows) ([]IterationRecord, error) {
	var records []IterationRecord
	for rows.Next() {
		var rec IterationRecord
		if err := rows.Scan(&rec.MessageID, &rec.TurnID, &rec.Iteration, &rec.Content, &rec.Reasoning, &rec.Tools, &rec.Tokens, &rec.TTFTMs, &rec.TokensPerSec, &rec.TotalMs, &rec.TPOTMs, &rec.InputTokens, &rec.CachedTokens, &rec.Model); err != nil {
			continue
		}
		records = append(records, rec)
	}
	return records, nil
}

// GetAllIterationHistory returns ALL iteration records for a tenant, ordered by
// (turn_id, iteration). Used by session export to include per-iteration
// TTFT/TPOT/tokens/timing for every completed iteration.
func (s *SessionService) GetAllIterationHistory(tenantID int64) ([]IterationRecord, error) {
	conn, err := s.conn()
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(`
		SELECT message_id, turn_id, iteration, content, reasoning, tools, tokens, ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens, model
		FROM iteration_history
		WHERE tenant_id = ?
		ORDER BY turn_id ASC, iteration ASC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get all iteration_history: %w", err)
	}
	defer rows.Close()
	return scanIterationRecords(rows)
}

// ── Tenant usage aggregation (v59) ─────────────────────────────────────────
//
// iteration_history now carries input_tokens / cached_tokens / model per
// iteration, making it the single source for usage & perf aggregation. The
// helpers below answer "what did this session consume / how did it perform"
// without a separate session-level ledger.

// UsageModelRow is a per-model usage breakdown (GROUP BY model).
type UsageModelRow struct {
	Model        string  `json:"model"`
	Iterations   int64   `json:"iterations"`
	Turns        int64   `json:"turns"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CachedTokens int64   `json:"cached_tokens"`
	AvgTTFTMs    float64 `json:"avg_ttft_ms"`
	AvgTPOTMs    float64 `json:"avg_tpot_ms"`
}

// UsageIterationRow is a single recent iteration's usage/perf record.
type UsageIterationRow struct {
	TurnID       uint64 `json:"turn_id"`
	Iteration    int    `json:"iteration"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CachedTokens int64  `json:"cached_tokens"`
	TTFTMs       int64  `json:"ttft_ms"`
	TPOTMs       int64  `json:"tpot_ms"`
	TokensPerSec int64  `json:"tokens_per_sec"`
	TotalMs      int64  `json:"total_ms"`
	Model        string `json:"model"`
	CreatedAt    string `json:"created_at"`
}

// TenantUsageStats aggregates a session's usage & performance from
// iteration_history (plus tenant watermark / metadata).
type TenantUsageStats struct {
	// iteration_history aggregates (pre-v59 rows have input_tokens=0 /
	// cached_tokens=0 / model='' — they still count toward iterations/turns).
	IterationCount  int64   `json:"iteration_count"`
	TurnCount       int64   `json:"turn_count"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	LLMTotalMs      int64   `json:"llm_total_ms"`
	AvgTTFTMs       float64 `json:"avg_ttft_ms"`
	AvgTPOTMs       float64 `json:"avg_tpot_ms"`
	AvgTokensPerSec float64 `json:"avg_tokens_per_sec"`
	// iteration_time_range
	FirstIterationAt string `json:"first_iteration_at"`
	LastIterationAt  string `json:"last_iteration_at"`
	// tenant_state watermark (current context level)
	LastPromptTokens     int64 `json:"last_prompt_tokens"`
	LastCompletionTokens int64 `json:"last_completion_tokens"`
	// tenants metadata
	CurrentModel      string `json:"current_model"`
	SessionCreatedAt  string `json:"session_created_at"`
	SessionLastActive string `json:"session_last_active"`
	// Breakdowns
	ByModel          []UsageModelRow     `json:"by_model"`
	RecentIterations []UsageIterationRow `json:"recent_iterations"`
}

// GetTenantUsageStats aggregates usage & perf stats for a tenant from
// iteration_history, tenant_state and tenants. recentLimit caps the
// RecentIterations detail rows (0 = default 20, negative = skip).
func (s *SessionService) GetTenantUsageStats(tenantID int64, recentLimit int) (*TenantUsageStats, error) {
	conn, err := s.conn()
	if err != nil {
		return nil, err
	}
	stats := &TenantUsageStats{}

	// Main aggregate. NULLIF excludes unrecorded zeros (pre-v59 rows /
	// non-streaming iterations) from the averages so they don't skew means.
	err = conn.QueryRow(`
		SELECT COUNT(*), COUNT(DISTINCT turn_id),
		       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(tokens), 0), COALESCE(SUM(cached_tokens), 0),
		       COALESCE(SUM(total_ms), 0),
		       COALESCE(AVG(NULLIF(ttft_ms, 0)), 0), COALESCE(AVG(NULLIF(tpot_ms, 0)), 0), COALESCE(AVG(NULLIF(tokens_per_sec, 0)), 0),
		       COALESCE(MIN(created_at), ''), COALESCE(MAX(created_at), '')
		FROM iteration_history WHERE tenant_id = ?
	`, tenantID).Scan(
		&stats.IterationCount, &stats.TurnCount,
		&stats.InputTokens, &stats.OutputTokens, &stats.CachedTokens,
		&stats.LLMTotalMs,
		&stats.AvgTTFTMs, &stats.AvgTPOTMs, &stats.AvgTokensPerSec,
		&stats.FirstIterationAt, &stats.LastIterationAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get tenant usage stats: %w", err)
	}

	// Per-model breakdown. model='' rows are pre-v59 history (before the
	// model column existed) — they have no model attribution and no input/
	// cached data, showing as a nameless "in=0 all-out" entry that drowns the
	// real models. Exclude them from the per-model split (they still count
	// toward the main aggregate above).
	rows, err := conn.Query(`
		SELECT COALESCE(model, ''), COUNT(*), COUNT(DISTINCT turn_id),
		       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(tokens), 0), COALESCE(SUM(cached_tokens), 0),
		       COALESCE(AVG(NULLIF(ttft_ms, 0)), 0), COALESCE(AVG(NULLIF(tpot_ms, 0)), 0)
		FROM iteration_history WHERE tenant_id = ? AND model != ''
		GROUP BY model ORDER BY SUM(input_tokens) + SUM(tokens) DESC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get tenant usage by model: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r UsageModelRow
		if err := rows.Scan(&r.Model, &r.Iterations, &r.Turns, &r.InputTokens, &r.OutputTokens, &r.CachedTokens, &r.AvgTTFTMs, &r.AvgTPOTMs); err != nil {
			continue
		}
		stats.ByModel = append(stats.ByModel, r)
	}
	rows.Close()

	// Recent iterations (newest first, then chronological for display).
	if recentLimit != 0 {
		if recentLimit < 0 || recentLimit > 500 {
			recentLimit = 500
		} else if recentLimit < 1 {
			recentLimit = 20
		}
		rows, err = conn.Query(`
			SELECT turn_id, iteration, input_tokens, tokens, cached_tokens, ttft_ms, tpot_ms, tokens_per_sec, total_ms, COALESCE(model, ''), COALESCE(created_at, '')
			FROM iteration_history WHERE tenant_id = ?
			ORDER BY id DESC LIMIT ?
		`, tenantID, recentLimit)
		if err != nil {
			return nil, fmt.Errorf("get recent iterations: %w", err)
		}
		for rows.Next() {
			var r UsageIterationRow
			if err := rows.Scan(&r.TurnID, &r.Iteration, &r.InputTokens, &r.OutputTokens, &r.CachedTokens, &r.TTFTMs, &r.TPOTMs, &r.TokensPerSec, &r.TotalMs, &r.Model, &r.CreatedAt); err != nil {
				continue
			}
			stats.RecentIterations = append(stats.RecentIterations, r)
		}
		rows.Close()
		// Reverse to chronological order (oldest → newest).
		for i, j := 0, len(stats.RecentIterations)-1; i < j; i, j = i+1, j-1 {
			stats.RecentIterations[i], stats.RecentIterations[j] = stats.RecentIterations[j], stats.RecentIterations[i]
		}
	}

	// tenant_state watermark (current context level).
	_ = conn.QueryRow(`SELECT COALESCE(last_prompt_tokens, 0), COALESCE(last_completion_tokens, 0) FROM tenant_state WHERE tenant_id = ?`, tenantID).
		Scan(&stats.LastPromptTokens, &stats.LastCompletionTokens)

	// tenants metadata.
	_ = conn.QueryRow(`SELECT COALESCE(model, ''), COALESCE(created_at, ''), COALESCE(last_active_at, '') FROM tenants WHERE id = ?`, tenantID).
		Scan(&stats.CurrentModel, &stats.SessionCreatedAt, &stats.SessionLastActive)

	return stats, nil
}

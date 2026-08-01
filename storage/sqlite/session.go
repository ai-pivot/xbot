package sqlite

import (
	"database/sql"
	"fmt"

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

// PurgeOldMessages is retained for compatibility. Compression is append-only and
// must never physically delete its source messages.
func (s *SessionService) PurgeOldMessages(tenantID int64, keepCount int) (int64, error) {
	// Append-only history: compression writes a snapshot record instead of
	// physically deleting source messages. Retained as a no-op for API compat.
	return 0, nil
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

// GetMaxTurnID returns the highest turn_id for a tenant's messages.
// Used by chatProcessLoop to restore the per-session turn ID counter
// after a server restart, ensuring turn_id remains globally monotonic.
// Returns 0 if no messages have a turn_id (new or legacy sessions).
func (s *SessionService) GetMaxTurnID(tenantID int64) (uint64, error) {
	conn, err := s.conn()
	if err != nil {
		return 0, err
	}
	var maxTurnID sql.NullInt64
	err = conn.QueryRow(
		"SELECT MAX(turn_id) FROM session_messages WHERE tenant_id = ?", tenantID,
	).Scan(&maxTurnID)
	if err != nil {
		return 0, fmt.Errorf("get max turn_id: %w", err)
	}
	if maxTurnID.Valid {
		return uint64(maxTurnID.Int64), nil
	}
	return 0, nil
}

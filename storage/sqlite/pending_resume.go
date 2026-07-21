package sqlite

import (
	"database/sql"
	"fmt"
	"time"
)

// PendingResume represents a session whose agent loop was interrupted by
// graceful shutdown and should be resumed on next startup.
type PendingResume struct {
	Channel   string
	ChatID    string
	SenderID  string
	Content   string // last user message content to re-inject
	CreatedAt string
}

// AddPendingResume records a session for resumption on next startup.
func (db *DB) AddPendingResume(channel, chatID, senderID, content string) error {
	conn := db.Conn()
	_, err := conn.Exec(`
		INSERT OR REPLACE INTO pending_resumes (channel, chat_id, sender_id, content, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, channel, chatID, senderID, content, time.Now().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("add pending resume: %w", err)
	}
	return nil
}

// GetLastUserMessage retrieves the last non-display-only user message
// (content + sender_id) for a given channel:chatID session.
// Uses a correlated subquery to deterministically pick the most recent
// sender_id (avoids non-deterministic LEFT JOIN with multiple senders).
func (db *DB) GetLastUserMessage(channel, chatID string) (content, senderID string, err error) {
	conn := db.Conn()
	err = conn.QueryRow(`
		SELECT sm.content, COALESCE((
			SELECT uc.sender_id FROM user_chats uc
			WHERE uc.channel = t.channel AND uc.chat_id = t.chat_id
			ORDER BY uc.created_at DESC LIMIT 1
		), '')
		FROM session_messages sm
		JOIN tenants t ON sm.tenant_id = t.id
		WHERE t.channel = ? AND t.chat_id = ? AND sm.role = 'user' AND COALESCE(sm.display_only, 0) = 0
		ORDER BY sm.id DESC LIMIT 1
	`, channel, chatID).Scan(&content, &senderID)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("get last user message: %w", err)
	}
	return content, senderID, nil
}

// HasAssistantReplyAfterLastUser checks whether the last user message in
// the session already has a subsequent assistant reply. Used by resume to
// detect turns that completed naturally between shutdown collection and
// cancel() — if so, the resume should be skipped.
//
// Note: The (tool_calls IS NULL OR tool_calls = ”) filter matches only
// final text replies (no tool calls). If a provider returns content +
// tool_calls in the same message as the final reply, this check would
// miss it — but that's safe: the re-injected turn is idempotent (the
// user message is already in DB, processMessage detects resume_turn and
// skips eager-save). The worst case is a duplicate turn, not data loss.
func (db *DB) HasAssistantReplyAfterLastUser(channel, chatID string) (bool, error) {
	conn := db.Conn()
	var count int
	err := conn.QueryRow(`
		SELECT COUNT(*) FROM session_messages sm
		JOIN tenants t ON sm.tenant_id = t.id
		WHERE t.channel = ? AND t.chat_id = ?
		  AND sm.role = 'assistant'
		  AND COALESCE(sm.display_only, 0) = 0
		  AND (sm.tool_calls IS NULL OR sm.tool_calls = '')
		  AND sm.id > (
		    SELECT sm2.id FROM session_messages sm2
		    JOIN tenants t2 ON sm2.tenant_id = t2.id
		    WHERE t2.channel = ? AND t2.chat_id = ?
		      AND sm2.role = 'user' AND COALESCE(sm2.display_only, 0) = 0
		    ORDER BY sm2.id DESC LIMIT 1
		  )
	`, channel, chatID, channel, chatID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("has assistant reply after last user: %w", err)
	}
	return count > 0, nil
}

// ListPendingResumes returns all sessions marked for resumption.
func (db *DB) ListPendingResumes() ([]PendingResume, error) {
	conn := db.Conn()
	rows, err := conn.Query(`SELECT channel, chat_id, sender_id, content, created_at FROM pending_resumes`)
	if err != nil {
		return nil, fmt.Errorf("list pending resumes: %w", err)
	}
	defer rows.Close()

	var result []PendingResume
	for rows.Next() {
		var pr PendingResume
		if err := rows.Scan(&pr.Channel, &pr.ChatID, &pr.SenderID, &pr.Content, &pr.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan pending resume: %w", err)
		}
		result = append(result, pr)
	}
	return result, rows.Err()
}

// ClearPendingResume removes a single pending resume record.
func (db *DB) ClearPendingResume(channel, chatID string) error {
	conn := db.Conn()
	_, err := conn.Exec(`DELETE FROM pending_resumes WHERE channel = ? AND chat_id = ?`, channel, chatID)
	if err != nil {
		return fmt.Errorf("clear pending resume: %w", err)
	}
	return nil
}

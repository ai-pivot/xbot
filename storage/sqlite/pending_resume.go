package sqlite

import (
	"fmt"
	"time"
)

// PendingResume represents a session whose agent loop was interrupted by
// graceful shutdown and should be resumed on next startup.
type PendingResume struct {
	Channel   string
	ChatID    string
	SenderID  string
	CreatedAt string
}

// AddPendingResume records a session for resumption on next startup.
func (db *DB) AddPendingResume(channel, chatID, senderID string) error {
	conn := db.Conn()
	_, err := conn.Exec(`
		INSERT OR REPLACE INTO pending_resumes (channel, chat_id, sender_id, content, created_at)
		VALUES (?, ?, ?, '', ?)
	`, channel, chatID, senderID, time.Now().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("add pending resume: %w", err)
	}
	return nil
}

// GetSessionSenderID returns the most recent sender_id recorded in the
// user_chats registry (main DB) for a channel:chatID session.
//
// Since v71 (one session, one DB), message content lives in the per-session
// DB — resume flows read it via TenantSession.GetLastUserMessageContent.
// The sender identity remains a main-DB registry concern, so it's looked up
// here. Returns "" when the session has no user_chats row.
func (db *DB) GetSessionSenderID(channel, chatID string) (string, error) {
	conn := db.Conn()
	var senderID string
	err := conn.QueryRow(`
		SELECT COALESCE((
			SELECT uc.sender_id FROM user_chats uc
			WHERE uc.channel = ? AND uc.chat_id = ?
			ORDER BY uc.created_at DESC LIMIT 1
		), '')
	`, channel, chatID).Scan(&senderID)
	if err != nil {
		return "", fmt.Errorf("get session sender id: %w", err)
	}
	return senderID, nil
}

// ListPendingResumes returns all sessions marked for resumption.
func (db *DB) ListPendingResumes() ([]PendingResume, error) {
	conn := db.Conn()
	rows, err := conn.Query(`SELECT channel, chat_id, sender_id, created_at FROM pending_resumes`)
	if err != nil {
		return nil, fmt.Errorf("list pending resumes: %w", err)
	}
	defer rows.Close()

	var result []PendingResume
	for rows.Next() {
		var pr PendingResume
		if err := rows.Scan(&pr.Channel, &pr.ChatID, &pr.SenderID, &pr.CreatedAt); err != nil {
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

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
func (db *DB) GetLastUserMessage(channel, chatID string) (content, senderID string, err error) {
	conn := db.Conn()
	err = conn.QueryRow(`
		SELECT sm.content, uc.sender_id
		FROM session_messages sm
		JOIN tenants t ON sm.tenant_id = t.id
		LEFT JOIN user_chats uc ON uc.channel = t.channel AND uc.chat_id = t.chat_id
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

// ClearPendingResumes removes all pending resume records.
func (db *DB) ClearPendingResumes() error {
	conn := db.Conn()
	_, err := conn.Exec(`DELETE FROM pending_resumes`)
	if err != nil {
		return fmt.Errorf("clear pending resumes: %w", err)
	}
	return nil
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

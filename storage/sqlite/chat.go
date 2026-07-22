package sqlite

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	log "xbot/logger"
)

var ErrChatNotFound = errors.New("chat not found")

// UserChat represents a chatroom owned by a user.
type UserChat struct {
	ID        int64
	Channel   string
	SenderID  string
	ChatID    string
	Label     string
	CreatedAt time.Time
}

// UserChatWithPreview extends UserChat with tenant metadata (last active, message preview).
type UserChatWithPreview struct {
	ChatID     string    `json:"chat_id"`
	Label      string    `json:"label"`
	LastActive time.Time `json:"last_active"`
	Preview    string    `json:"preview"`
	IsCurrent  bool      `json:"is_current"`
}

// ChatService manages user chatrooms (multi-chat support).
type ChatService struct {
	db *DB
}

// NewChatService creates a new ChatService.
func NewChatService(db *DB) *ChatService {
	return &ChatService{db: db}
}

// ListUserChats returns all chatrooms for a user in a given channel.
// Includes the default chat (chatID=senderID) even if not in user_chats table.
// If currentChatID is non-empty, marks that chat as current.
// Uses a single SQL query with LEFT JOIN to avoid N+1 per-chatID queries.
func (s *ChatService) ListUserChats(channel, senderID, currentChatID string) ([]UserChatWithPreview, error) {
	conn := s.db.Conn()

	// Collect all chat IDs for this user:
	// 1. Default chat (chat_id = senderID)
	// 2. User-created chats from user_chats table
	chatIDs := []string{senderID}

	rows, err := conn.Query(
		"SELECT chat_id, label FROM user_chats WHERE channel = ? AND sender_id = ?",
		channel, senderID,
	)
	if err != nil {
		return nil, fmt.Errorf("list user chats: %w", err)
	}
	defer rows.Close()

	labelMap := map[string]string{}
	for rows.Next() {
		var cid, label string
		if err := rows.Scan(&cid, &label); err != nil {
			continue
		}
		chatIDs = append(chatIDs, cid)
		labelMap[cid] = label
	}

	if len(chatIDs) == 0 {
		return nil, nil
	}

	// Single query: LEFT JOIN tenants + latest session message preview for all chatIDs.
	placeholders := make([]string, len(chatIDs))
	args := make([]any, 0, len(chatIDs)+1)
	args = append(args, channel)
	for i, cid := range chatIDs {
		placeholders[i] = "?"
		args = append(args, cid)
	}

	query := fmt.Sprintf(`
		SELECT t.chat_id, t.last_active_at,
			(SELECT sm.content FROM session_messages sm
			 WHERE sm.tenant_id = t.id AND sm.role IN ('user', 'assistant')
			 ORDER BY sm.id DESC LIMIT 1) AS preview
		FROM tenants t
		WHERE t.channel = ? AND t.chat_id IN (%s)
	`, strings.Join(placeholders, ","))

	rows2, err := conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query tenant metadata: %w", err)
	}
	defer rows2.Close()

	tenantInfo := map[string]struct {
		lastActive time.Time
		preview    string
	}{}
	for rows2.Next() {
		var cid string
		var lastActive time.Time
		var preview sql.NullString
		if err := rows2.Scan(&cid, &lastActive, &preview); err != nil {
			continue
		}
		tenantInfo[cid] = struct {
			lastActive time.Time
			preview    string
		}{lastActive: lastActive, preview: preview.String}
	}

	// Build result preserving chatIDs order
	var result []UserChatWithPreview
	for _, cid := range chatIDs {
		info, hasTenant := tenantInfo[cid]
		lastActive := time.Time{}
		preview := ""
		if hasTenant {
			lastActive = info.lastActive
			preview = info.preview
		}

		label := labelMap[cid]
		if label == "" && cid == senderID {
			label = "默认会话"
		}

		result = append(result, UserChatWithPreview{
			ChatID:     cid,
			Label:      label,
			LastActive: lastActive,
			Preview:    truncate(preview, 80),
			IsCurrent:  cid == currentChatID,
		})
	}

	return result, nil
}

// CreateChat creates a new chatroom for a user. Returns the new chatID.
func (s *ChatService) CreateChat(channel, senderID, label string) (string, error) {
	conn := s.db.Conn()
	userID, err := canonicalUserID(conn, channel, senderID)
	if err != nil {
		return "", fmt.Errorf("resolve chat owner: %w", err)
	}
	return s.createChat(channel, senderID, label, userID)
}

// CreateChatOwned creates a chat using the canonical identity resolved at the
// authenticated channel boundary. This is required when a linked identity uses
// Web transport with a non-Web sender ID.
func (s *ChatService) CreateChatOwned(channel, senderID, label string, canonicalUserID int64) (string, error) {
	if canonicalUserID <= 0 {
		return s.CreateChat(channel, senderID, label)
	}
	return s.createChat(channel, senderID, label, canonicalUserID)
}

func (s *ChatService) createChat(channel, senderID, label string, userID int64) (string, error) {
	conn := s.db.Conn()

	// Generate a unique chat ID
	var chatID string
	for i := 0; i < 10; i++ {
		var hex string
		err := conn.QueryRow("SELECT hex(randomblob(6))").Scan(&hex)
		if err != nil {
			return "", fmt.Errorf("generate chat id: %w", err)
		}
		chatID = "chat_" + hex

		// Check uniqueness
		var count int
		err = conn.QueryRow(
			"SELECT COUNT(*) FROM user_chats WHERE channel = ? AND sender_id = ? AND chat_id = ?",
			channel, senderID, chatID,
		).Scan(&count)
		if err == nil && count == 0 {
			break
		}
		chatID = ""
	}
	if chatID == "" {
		return "", fmt.Errorf("failed to generate unique chat id")
	}

	if label == "" {
		autoName, err := generateChatLabel()
		if err != nil {
			label = "新会话"
		} else {
			label = autoName
		}
	}

	_, err := conn.Exec(
		"INSERT INTO user_chats (channel, sender_id, chat_id, label, user_id) VALUES (?, ?, ?, ?, ?)",
		channel, senderID, chatID, label, userID,
	)
	if err != nil {
		return "", fmt.Errorf("create chat: %w", err)
	}

	log.WithFields(log.Fields{
		"channel": channel, "sender": senderID, "chat_id": chatID, "label": label,
	}).Info("Chat created")
	return chatID, nil
}

// DeleteChat removes a chatroom. Deletes the tenant and all associated data (cascading).
func (s *ChatService) DeleteChat(channel, senderID, chatID string) error {

	conn := s.db.Conn()

	// Verify ownership via user_chats table
	var count int
	err := conn.QueryRow(
		"SELECT COUNT(*) FROM user_chats WHERE channel = ? AND sender_id = ? AND chat_id = ?",
		channel, senderID, chatID,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("check chat ownership: %w", err)
	}

	// A session can acquire labels from more than one authenticated surface
	// (for example cli_user plus an admin Web identity). Delete them together
	// so a later session with the same key cannot inherit stale metadata.
	_, err = conn.Exec(
		"DELETE FROM user_chats WHERE channel = ? AND chat_id = ?",
		channel, chatID,
	)
	if err != nil {
		return fmt.Errorf("delete chat record: %w", err)
	}

	// Delete tenant (cascades to session_messages, memory, etc.) regardless of user_chats.
	// CLI sessions may not have a user_chats entry but still have tenant data.
	result, err := conn.Exec(
		"DELETE FROM tenants WHERE channel = ? AND chat_id = ?",
		channel, chatID,
	)
	if err != nil {
		return fmt.Errorf("delete tenant: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 && count == 0 {
		return ErrChatNotFound
	}

	log.WithFields(log.Fields{
		"channel": channel, "sender": senderID, "chat_id": chatID,
	}).Info("Chat deleted")
	return nil
}

// RenameChat updates the label of a chatroom.
func (s *ChatService) RenameChat(channel, senderID, chatID, label string) error {
	// Upsert: try UPDATE first (any channel), then INSERT if no row exists.
	// This handles renaming CLI/feishu sessions from the Web UI, where the
	// caller passes channel="web" but the session lives under a different channel.
	conn := s.db.Conn()
	_, err := conn.Exec(`
		INSERT INTO user_chats (channel, sender_id, chat_id, label)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(channel, sender_id, chat_id) DO UPDATE SET label = ?`,
		channel, senderID, chatID, label, label,
	)
	return err
}

// generateChatLabel creates a random session label like "Agent-brave-fox".
// Mirrors channel.GenerateSessionName to avoid import cycles.
func generateChatLabel() (string, error) {
	adjs := []string{
		"brave", "calm", "swift", "keen", "warm", "witty", "sage", "brisk",
		"cool", "bold", "sharp", "lucid", "sunny", "frank", "deft", "astute",
	}
	nouns := []string{
		"fox", "hawk", "lynx", "dove", "panda", "otter", "falcon", "heron",
		"stone", "flame", "brook", "cedar", "comet", "coral", "ember", "zephyr",
	}
	adjIdx, err := rand.Int(rand.Reader, big.NewInt(int64(len(adjs))))
	if err != nil {
		return "", err
	}
	nounIdx, err := rand.Int(rand.Reader, big.NewInt(int64(len(nouns))))
	if err != nil {
		return "", err
	}
	return "Agent-" + adjs[adjIdx.Int64()] + "-" + nouns[nounIdx.Int64()], nil
}

func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes-3]) + "..."
}

// GetSenderForChat looks up the sender_id (user) that owns a given chat session.
// Returns ("", nil) if no owner found (session not in DB).
func (s *ChatService) GetSenderForChat(channel, chatID string) (string, error) {
	var senderID string
	err := s.db.Conn().QueryRow(
		"SELECT sender_id FROM user_chats WHERE channel = ? AND chat_id = ? LIMIT 1",
		channel, chatID,
	).Scan(&senderID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("get sender for chat: %w", err)
	}
	return senderID, nil
}

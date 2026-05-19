package channel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	log "xbot/logger"
)

// PendingAskUser is a pending AskUser question persisted to disk.
type PendingAskUser struct {
	ChatID    string `json:"chat_id"`    // the session this question belongs to
	Questions string `json:"questions"`  // JSON string of ask_questions
	RequestID string `json:"request_id"` // optional, from server
	SavedAt   int64  `json:"saved_at"`   // unix timestamp
}

func pendingAskUserDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".xbot", "pending_askuser")
}

func pendingAskUserKey(channelName, chatID string) string {
	h := sha256.Sum256([]byte(qualifyChatID(channelName, chatID)))
	return hex.EncodeToString(h[:])
}

func pendingAskUserPath(channelName, chatID string) string {
	return filepath.Join(pendingAskUserDir(), pendingAskUserKey(channelName, chatID)+".json")
}

// savePendingAskUser saves a pending AskUser question to disk.
func (m *cliModel) savePendingAskUser(chatID string, metadata map[string]string) {
	if metadata == nil {
		return
	}
	qJSON := metadata["ask_questions"]
	if qJSON == "" {
		return
	}

	pu := &PendingAskUser{
		ChatID:    chatID,
		Questions: qJSON,
		RequestID: metadata["request_id"],
		SavedAt:   nowUnix(),
	}

	dir := pendingAskUserDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.WithError(err).Warn("Failed to create pending_askuser dir")
		return
	}

	path := pendingAskUserPath(m.channelName, chatID)
	data, err := json.Marshal(pu)
	if err != nil {
		log.WithError(err).WithField("chat_id", chatID).Warn("Failed to marshal pending ask_user")
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.WithError(err).WithField("chat_id", chatID).Warn("Failed to write pending ask_user")
		return
	}
	log.WithField("chat_id", chatID).Info("Saved pending ask_user to disk")
}

// loadPendingAskUser loads a pending AskUser question from disk.
// Returns nil if no pending question exists.
func (m *cliModel) loadPendingAskUser(chatID string) *PendingAskUser {
	path := pendingAskUserPath(m.channelName, chatID)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.WithError(err).WithField("chat_id", chatID).Warn("Failed to read pending ask_user")
		}
		return nil
	}
	var pu PendingAskUser
	if err := json.Unmarshal(data, &pu); err != nil {
		log.WithError(err).WithField("chat_id", chatID).Warn("Failed to unmarshal pending ask_user, removing corrupt file")
		os.Remove(path)
		return nil
	}
	return &pu
}

// deletePendingAskUser removes a pending AskUser question from disk.
func (m *cliModel) deletePendingAskUser(chatID string) {
	path := pendingAskUserPath(m.channelName, chatID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.WithError(err).WithField("chat_id", chatID).Warn("Failed to delete pending ask_user")
		return
	}
	log.WithField("chat_id", chatID).Info("Deleted pending ask_user from disk")
}

// nowUnix returns current unix timestamp.
var nowUnix = func() int64 {
	return time.Now().Unix()
}

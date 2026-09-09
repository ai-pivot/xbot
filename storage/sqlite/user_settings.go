package sqlite

import (
	"fmt"
	"time"
)

// UserSettingsService manages per-user settings stored in the user_settings table.
type UserSettingsService struct {
	db *DB
}

// NewUserSettingsService creates a new UserSettingsService.
func NewUserSettingsService(db *DB) *UserSettingsService {
	return &UserSettingsService{db: db}
}

// Get retrieves all settings for a given channel.
//
// SINGLE OPERATOR (post-v63): user_settings rows carry the operator identity;
// the v63 migration deleted every non-operator sender row. The senderID
// parameter is retained for call-site compatibility but is NOT used as a
// filter — filtering by a stale pre-v63 sender id returned an empty map, which
// silently reset settings (same bug class as GetUserDefaultModel). The channel
// filter is kept: settings are scoped to the canonical channel (e.g. "cli" for
// global tier/thinking settings).
func (s *UserSettingsService) Get(channel, senderID string) (map[string]string, error) {
	_ = senderID // single operator
	if s.db == nil {
		return nil, fmt.Errorf("user settings store: database not initialized")
	}
	conn := s.db.Conn()
	if conn == nil {
		return nil, fmt.Errorf("user settings store: database connection closed")
	}
	rows, err := conn.Query(
		"SELECT key, value FROM user_settings WHERE channel = ?",
		channel,
	)
	if err != nil {
		return nil, fmt.Errorf("get user settings: %w", err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, rows.Err()
}

// Set creates or updates a single setting.
func (s *UserSettingsService) Set(channel, senderID, key, value string) error {
	if s.db == nil {
		return fmt.Errorf("user settings store: database not initialized")
	}
	conn := s.db.Conn()
	if conn == nil {
		return fmt.Errorf("user settings store: database connection closed")
	}
	now := time.Now().UnixMilli()
	_, err := conn.Exec(
		`INSERT INTO user_settings (channel, sender_id, key, value, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(channel, sender_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		channel, senderID, key, value, now,
	)
	if err != nil {
		return fmt.Errorf("set user setting: %w", err)
	}
	return nil
}

// Delete removes a single setting.
func (s *UserSettingsService) Delete(channel, senderID, key string) error {
	if s.db == nil {
		return fmt.Errorf("user settings store: database not initialized")
	}
	_, err := s.db.Conn().Exec(
		"DELETE FROM user_settings WHERE channel = ? AND sender_id = ? AND key = ?",
		channel, senderID, key,
	)
	if err != nil {
		return fmt.Errorf("delete user setting: %w", err)
	}
	return nil
}

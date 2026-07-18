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

// Get retrieves all settings for a given channel and sender.
func (s *UserSettingsService) Get(channel, senderID string) (map[string]string, error) {
	if s.db == nil {
		return nil, fmt.Errorf("user settings store: database not initialized")
	}
	conn := s.db.Conn()
	if conn == nil {
		return nil, fmt.Errorf("user settings store: database connection closed")
	}
	rows, err := conn.Query(
		"SELECT key, value FROM user_settings WHERE channel = ? AND sender_id = ?",
		channel, senderID,
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

// GetByUserID retrieves all settings for a canonical user (by user_id).
// Channel is still needed because settings are namespaced per channel.
func (s *UserSettingsService) GetByUserID(channel string, userID int64) (map[string]string, error) {
	if s.db == nil {
		return nil, fmt.Errorf("user settings store: database not initialized")
	}
	conn := s.db.Conn()
	if conn == nil {
		return nil, fmt.Errorf("user settings store: database connection closed")
	}
	rows, err := conn.Query(
		"SELECT key, value FROM user_settings WHERE channel = ? AND user_id = ?",
		channel, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("get user settings by user_id: %w", err)
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

// SetByUserID creates or updates a setting for a canonical user (by user_id).
// The sender_id column is set to the user_id string for compatibility with
// existing queries that may still read by sender_id.
func (s *UserSettingsService) SetByUserID(channel string, userID int64, key, value string) error {
	if s.db == nil {
		return fmt.Errorf("user settings store: database not initialized")
	}
	conn := s.db.Conn()
	if conn == nil {
		return fmt.Errorf("user settings store: database connection closed")
	}
	now := time.Now().UnixMilli()
	senderID := fmt.Sprintf("user-%d", userID)
	// Delete any existing rows for this (channel, user_id, key) first to
	// avoid UNIQUE(channel, sender_id, key) conflicts from stale sender_ids.
	conn.Exec(`DELETE FROM user_settings WHERE channel = ? AND user_id = ? AND key = ?`, channel, userID, key)
	_, err := conn.Exec(
		`INSERT INTO user_settings (channel, sender_id, user_id, key, value, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(channel, sender_id, key) DO UPDATE SET value = excluded.value, user_id = excluded.user_id, updated_at = excluded.updated_at`,
		channel, senderID, userID, key, value, now,
	)
	if err != nil {
		return fmt.Errorf("set user setting by user_id: %w", err)
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

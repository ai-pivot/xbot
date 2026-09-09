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

// singleOperatorSender is the canonical sender id under which every
// single-operator row is stored (post-v63 multi-user removal).
//
// ⚠️ Writers normalise to it so the sender-agnostic readers (Get,
// GetUserDefaultModel, GetProfile) always observe the latest write. Without
// this the two write paths diverge — e.g. the Feishu settings card writes
// ("cli", <open_id>, "thinking_mode") while the CLI/Web RPC writes
// ("cli", "cli_user", "thinking_mode") — producing TWO rows for the same
// (channel, key); the reader's map-overwrite then depends on rowid order, so
// the user's change was silently ignored (CR: 读取已忽略 sender_id，但写入端
// 仍按 sender_id 落库 → 同一 (channel,key) 出现多行，设置改动被静默忽略).
const singleOperatorSender = "cli_user"

// Set creates or updates a single setting.
func (s *UserSettingsService) Set(channel, senderID, key, value string) error {
	_ = senderID // single operator: writes normalise to the operator id
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
		channel, singleOperatorSender, key, value, now,
	)
	if err != nil {
		return fmt.Errorf("set user setting: %w", err)
	}
	return nil
}

// Delete removes a single setting.
//
// Single operator: delete by (channel, key) so a legacy row stored under a
// different sender id is removed too (CR: 写入/清除端仍按 sender_id，两边不对称).
func (s *UserSettingsService) Delete(channel, senderID, key string) error {
	_ = senderID // single operator
	if s.db == nil {
		return fmt.Errorf("user settings store: database not initialized")
	}
	_, err := s.db.Conn().Exec(
		"DELETE FROM user_settings WHERE channel = ? AND key = ?",
		channel, key,
	)
	if err != nil {
		return fmt.Errorf("delete user setting: %w", err)
	}
	return nil
}

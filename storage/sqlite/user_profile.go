package sqlite

import (
	"database/sql"
	"fmt"

	log "xbot/logger"
)

// UserProfileService handles per-user profile CRUD (keyed by sender_id, independent of tenants)
type UserProfileService struct {
	db *DB
}

// NewUserProfileService creates a new user profile service
func NewUserProfileService(db *DB) *UserProfileService {
	return &UserProfileService{db: db}
}

// GetProfile retrieves the operator's name and profile.
//
// SINGLE OPERATOR (post-v63): the profile table belongs to the one operator.
// The senderID parameter is retained for call-site compatibility but is NOT
// used as a filter — a stale pre-v63 sender id missed the row and silently
// returned an empty profile (same bug class as GetUserDefaultModel).
func (s *UserProfileService) GetProfile(senderID string) (name, profile string, err error) {
	_ = senderID // single operator
	conn := s.db.Conn()
	err = conn.QueryRow(
		"SELECT name, profile FROM user_profiles ORDER BY updated_at DESC LIMIT 1",
	).Scan(&name, &profile)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("get user profile: %w", err)
	}
	return name, profile, nil
}

// SaveProfile upserts the operator profile.
//
// SINGLE OPERATOR: normalise to the canonical operator id and purge legacy
// rows so the sender-agnostic GetProfile always reads what was just written
// (CR: GetProfile 已改成只读一行，但写入端仍按 sender 落库 → 写入后读到的可能
// 仍是旧行).
func (s *UserProfileService) SaveProfile(senderID, name, profile string) error {
	_ = senderID // single operator
	conn := s.db.Conn()
	if _, err := conn.Exec(
		`DELETE FROM user_profiles WHERE sender_id != ?`, singleOperatorSender,
	); err != nil {
		return fmt.Errorf("save user profile (purge legacy rows): %w", err)
	}
	_, err := conn.Exec(`
		INSERT INTO user_profiles (sender_id, name, profile) VALUES (?, ?, ?)
		ON CONFLICT(sender_id) DO UPDATE SET
			name = CASE WHEN excluded.name != '' THEN excluded.name ELSE user_profiles.name END,
			profile = excluded.profile,
			updated_at = CURRENT_TIMESTAMP
	`, singleOperatorSender, name, profile)
	if err != nil {
		return fmt.Errorf("save user profile: %w", err)
	}
	log.WithField("sender_id", singleOperatorSender).Debug("User profile updated")
	return nil
}

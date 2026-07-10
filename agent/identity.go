package agent

import (
	"database/sql"
	"fmt"

	log "xbot/logger"
)

// IdentityResolver resolves channel-specific senderID to a canonical user_id.
// It is called at every system entry point (WS connect, HTTP request, CLI startup)
// to translate channel identities (cli_user, web-4, ou_xxx) into a unified user_id
// that is used for all internal operations (subscriptions, runners, settings, sessions).
//
// Design (roundtable-reviewed):
//   - No in-memory cache — DB is authoritative, consistent with ResolveLLM philosophy
//   - Race-safe: uses INSERT OR IGNORE + re-SELECT to avoid orphan rows
//   - Resolve is called at channel entry layer, not in agent loop
type IdentityResolver struct {
	db *sql.DB
}

// NewIdentityResolver creates an IdentityResolver backed by the given database.
func NewIdentityResolver(db *sql.DB) *IdentityResolver {
	return &IdentityResolver{db: db}
}

// Resolve looks up or auto-creates a canonical user for the given channel identity.
// Returns (userID, role, error).
//
// Race-safe pattern: two concurrent calls for the same new identity will both
// INSERT INTO users (generating two rows), but the second INSERT INTO user_identities
// will hit the UNIQUE(channel, channel_user_id) constraint and be ignored.
// The re-SELECT returns the canonical user_id regardless of which INSERT won.
func (r *IdentityResolver) Resolve(channel, channelUserID string) (int64, string, error) {
	if r == nil || r.db == nil {
		return 0, "admin", nil // fallback: standalone mode, treat as admin
	}

	// 1. Fast path: check if already linked
	var userID int64
	err := r.db.QueryRow(
		`SELECT user_id FROM user_identities WHERE channel = ? AND channel_user_id = ?`,
		channel, channelUserID,
	).Scan(&userID)
	if err == nil {
		role := r.getRole(userID)
		return userID, role, nil
	}

	// 2. Not linked — auto-create a new user
	result, err := r.db.Exec(`INSERT INTO users (role) VALUES ('user')`)
	if err != nil {
		return 0, "", fmt.Errorf("identity resolve: create user: %w", err)
	}
	newID, _ := result.LastInsertId()

	// 3. Link identity (ON CONFLICT handles race: if another goroutine already inserted)
	r.db.Exec(
		`INSERT INTO user_identities (user_id, channel, channel_user_id)
		 VALUES (?, ?, ?)
		 ON CONFLICT(channel, channel_user_id) DO NOTHING`,
		newID, channel, channelUserID,
	)

	// 4. Re-SELECT to get the canonical user_id (may differ if race lost)
	err = r.db.QueryRow(
		`SELECT user_id FROM user_identities WHERE channel = ? AND channel_user_id = ?`,
		channel, channelUserID,
	).Scan(&userID)
	if err != nil {
		return 0, "", fmt.Errorf("identity resolve: re-select: %w", err)
	}

	// 5. If our auto-created user was orphaned (race lost), clean it up
	if userID != newID {
		r.db.Exec(`DELETE FROM users WHERE id = ? AND id NOT IN (SELECT user_id FROM user_identities)`, newID)
	}

	role := r.getRole(userID)
	return userID, role, nil
}

// getRole fetches the role for a canonical user_id.
func (r *IdentityResolver) getRole(userID int64) string {
	var role string
	err := r.db.QueryRow(`SELECT role FROM users WHERE id = ?`, userID).Scan(&role)
	if err != nil {
		return "user"
	}
	return role
}

// IsAdmin checks if the canonical user has admin role.
func (r *IdentityResolver) IsAdmin(userID int64) bool {
	if r == nil || r.db == nil {
		return true // fallback: standalone mode
	}
	return r.getRole(userID) == "admin"
}

// SetRole updates a user's role.
func (r *IdentityResolver) SetRole(userID int64, role string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("identity resolver not initialized")
	}
	_, err := r.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, role, userID)
	if err != nil {
		return fmt.Errorf("set role: %w", err)
	}
	log.WithFields(log.Fields{
		"user_id": userID,
		"role":    role,
	}).Info("IdentityResolver: user role updated")
	return nil
}

// ListIdentities returns all channel identities linked to a canonical user.
func (r *IdentityResolver) ListIdentities(userID int64) ([]IdentityEntry, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	rows, err := r.db.Query(
		`SELECT id, channel, channel_user_id, linked_at FROM user_identities WHERE user_id = ? ORDER BY linked_at`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list identities: %w", err)
	}
	defer rows.Close()

	var entries []IdentityEntry
	for rows.Next() {
		var e IdentityEntry
		if err := rows.Scan(&e.ID, &e.Channel, &e.ChannelUserID, &e.LinkedAt); err != nil {
			return nil, fmt.Errorf("scan identity: %w", err)
		}
		e.UserID = userID
		entries = append(entries, e)
	}
	return entries, nil
}

// IdentityEntry represents a single channel identity linked to a canonical user.
type IdentityEntry struct {
	ID            int64  `json:"id"`
	UserID        int64  `json:"user_id"`
	Channel       string `json:"channel"`
	ChannelUserID string `json:"channel_user_id"`
	LinkedAt      string `json:"linked_at"`
}

// ListAllUsers returns all canonical users (admin only).
func (r *IdentityResolver) ListAllUsers() ([]UserInfo, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	rows, err := r.db.Query(
		`SELECT id, display_name, role, created_at FROM users ORDER BY id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []UserInfo
	for rows.Next() {
		var u UserInfo
		if err := rows.Scan(&u.ID, &u.DisplayName, &u.Role, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, nil
}

// UserInfo represents a canonical user's metadata.
type UserInfo struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	CreatedAt   string `json:"created_at"`
}

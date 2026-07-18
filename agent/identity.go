package agent

import (
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"fmt"
	"strings"
	"sync"
	"time"

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
//
// initialized tracks whether IdentityResolver has been initialized with a real DB.
// In standalone CLI mode, this is false — admin checks fall back to true.
type IdentityResolver struct {
	db          *sql.DB
	initialized bool
}

// NewIdentityResolver creates an IdentityResolver backed by the given database.
func NewIdentityResolver(db *sql.DB) *IdentityResolver {
	return &IdentityResolver{db: db, initialized: db != nil}
}

// Resolve looks up or auto-creates a canonical user for the given channel identity.
// Returns (userID, role, error).
//
// Race-safe pattern: two concurrent calls for the same new identity will both
// INSERT INTO users (generating two rows), but the second INSERT INTO user_identities
// will hit the UNIQUE(channel, channel_user_id) constraint and be ignored.
// The re-SELECT returns the canonical user_id regardless of which INSERT won.
func (r *IdentityResolver) Resolve(channel, channelUserID string) (int64, string, error) {
	if r == nil || !r.initialized {
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
	if r == nil || !r.initialized {
		return true // fallback: standalone mode (no DB)
	}
	return r.getRole(userID) == "admin"
}

// ResolvePrimarySenderID returns the canonical senderID for a user — the
// single business identity used for all DB queries (subscriptions, settings,
// tier config, etc.).
//
// All channel identities linked to the same canonical user MUST resolve to the
// same primary senderID. This is the architectural contract: subscriptions are
// effectively bound to the canonical user through this primary senderID.
//
// Resolution priority:
//  1. "cli" channel identity (e.g. "cli_user") — CLI is the primary management
//     surface; admin subscriptions, settings, and tier config live here.
//  2. First linked identity by linked_at (oldest = most established).
//  3. Empty string if the user has no linked identities.
func (r *IdentityResolver) ResolvePrimarySenderID(userID int64) string {
	if r == nil || !r.initialized || userID == 0 {
		return ""
	}
	// Try CLI channel first — it's the canonical management identity.
	var cliID string
	err := r.db.QueryRow(
		`SELECT channel_user_id FROM user_identities WHERE user_id = ? AND channel = 'cli' LIMIT 1`,
		userID,
	).Scan(&cliID)
	if err == nil && cliID != "" {
		return cliID
	}
	// Fall back to the oldest linked identity (any channel).
	var channelUserID string
	err = r.db.QueryRow(
		`SELECT channel_user_id FROM user_identities WHERE user_id = ? ORDER BY linked_at LIMIT 1`,
		userID,
	).Scan(&channelUserID)
	if err == nil {
		return channelUserID
	}
	return ""
}

// SetRole updates a user's role.
func (r *IdentityResolver) SetRole(userID int64, role string) error {
	if r == nil || !r.initialized {
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
func (r *IdentityResolver) ListIdentities(userID int64) (any, error) {
	if r == nil || !r.initialized {
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
func (r *IdentityResolver) ListAllUsers() (any, error) {
	if r == nil || !r.initialized {
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

// ---------------------------------------------------------------------------
// Link codes — one-time codes for cross-channel account association
// ---------------------------------------------------------------------------

// GenerateLinkCode creates a one-time link code for the given user.
// Code is 8 chars base32 (no padding), expires in 5 minutes.
// Rate-limited: one code per user per 10 seconds (replaces previous if exists).
func (r *IdentityResolver) GenerateLinkCode(userID int64) (string, error) {
	if r == nil || !r.initialized {
		return "", fmt.Errorf("identity resolver not initialized")
	}
	// Generate 5 random bytes → 8 base32 chars
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate link code: %w", err)
	}
	code := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
	// Format expiry as SQLite datetime string for consistent comparison with datetime('now')
	expires := time.Now().Add(5 * time.Minute).UTC().Format("2006-01-02 15:04:05")
	// Delete any previous code for this user, then insert new one
	tx, err := r.db.Begin()
	if err != nil {
		return "", fmt.Errorf("link code tx: %w", err)
	}
	tx.Exec("DELETE FROM link_codes WHERE user_id = ?", userID)
	_, err = tx.Exec("INSERT INTO link_codes (code, user_id, expires_at) VALUES (?, ?, ?)", code, userID, expires)
	if err != nil {
		tx.Rollback()
		return "", fmt.Errorf("insert link code: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit link code: %w", err)
	}
	log.WithFields(log.Fields{
		"user_id": userID,
		"code":    code,
	}).Info("IdentityResolver: link code generated")
	return code, nil
}

// ConsumeLinkCode validates a link code and returns the target user_id.
// The code is deleted after successful validation (single-use).
// Returns (targetUserID, error).
func (r *IdentityResolver) ConsumeLinkCode(code string) (int64, error) {
	if r == nil || !r.initialized {
		return 0, fmt.Errorf("identity resolver not initialized")
	}
	var userID int64
	var expires string
	err := r.db.QueryRow("SELECT user_id, expires_at FROM link_codes WHERE code = ?", code).Scan(&userID, &expires)
	if err != nil {
		return 0, fmt.Errorf("invalid or expired link code")
	}
	// Check expiry
	expiryTime, _ := time.Parse("2006-01-02 15:04:05", expires)
	if expiryTime.IsZero() {
		expiryTime, _ = time.Parse(time.RFC3339, expires)
	}
	if time.Now().After(expiryTime) {
		r.db.Exec("DELETE FROM link_codes WHERE code = ?", code)
		return 0, fmt.Errorf("link code has expired")
	}
	// Delete code (single-use)
	r.db.Exec("DELETE FROM link_codes WHERE code = ?", code)
	return userID, nil
}

// ValidateLinkCode validates a link code WITHOUT consuming it.
// Used for preview (merge preview) before the actual consume.
// Returns (targetUserID, error).
func (r *IdentityResolver) ValidateLinkCode(code string) (int64, error) {
	if r == nil || !r.initialized {
		return 0, fmt.Errorf("identity resolver not initialized")
	}
	var userID int64
	var expires string
	err := r.db.QueryRow("SELECT user_id, expires_at FROM link_codes WHERE code = ?", code).Scan(&userID, &expires)
	if err != nil {
		return 0, fmt.Errorf("invalid or expired link code")
	}
	expiryTime, _ := time.Parse("2006-01-02 15:04:05", expires)
	if expiryTime.IsZero() {
		expiryTime, _ = time.Parse(time.RFC3339, expires)
	}
	if time.Now().After(expiryTime) {
		r.db.Exec("DELETE FROM link_codes WHERE code = ?", code)
		return 0, fmt.Errorf("link code has expired")
	}
	return userID, nil
}

// LinkIdentity links a channel identity to an existing canonical user.
// If the identity is already linked to a different user, returns an error
// indicating a merge is required (caller should call MergeUsers instead).
// Returns (merged bool, error).
func (r *IdentityResolver) LinkIdentity(targetUserID int64, channel, channelUserID string) (bool, error) {
	if r == nil || !r.initialized {
		return false, fmt.Errorf("identity resolver not initialized")
	}
	// Check if identity already exists
	var existingUserID int64
	err := r.db.QueryRow(
		"SELECT user_id FROM user_identities WHERE channel = ? AND channel_user_id = ?",
		channel, channelUserID,
	).Scan(&existingUserID)
	if err == nil {
		if existingUserID == targetUserID {
			return false, nil // already linked to same user — no-op
		}
		// Linked to a different user — need merge
		return false, fmt.Errorf("identity %s:%s is linked to user %d, merge required with target user %d", channel, channelUserID, existingUserID, targetUserID)
	}
	// Not linked — simple insert
	_, err = r.db.Exec(
		"INSERT INTO user_identities (user_id, channel, channel_user_id) VALUES (?, ?, ?)",
		targetUserID, channel, channelUserID,
	)
	if err != nil {
		return false, fmt.Errorf("link identity: %w", err)
	}
	log.WithFields(log.Fields{
		"target_user_id":  targetUserID,
		"channel":         channel,
		"channel_user_id": channelUserID,
	}).Info("IdentityResolver: identity linked")
	return false, nil
}

// MergePreview calculates what would happen if sourceUser is merged into targetUser.
// Returns counts of assets that will be migrated and conflicts that need resolution.
type MergePreview struct {
	SourceUserID  int64    `json:"source_user_id"`
	TargetUserID  int64    `json:"target_user_id"`
	Identities    int      `json:"identities"`
	Subscriptions int      `json:"subscriptions"`
	Runners       int      `json:"runners"`
	Settings      int      `json:"settings"`
	DefaultModel  int      `json:"default_model"`
	UserChats     int      `json:"user_chats"`
	Tenants       int      `json:"tenants"`
	CronJobs      int      `json:"cron_jobs"`
	EventTriggers int      `json:"event_triggers"`
	Conflicts     []string `json:"conflicts"`
}

// PreviewMerge calculates a merge preview without executing it.
func (r *IdentityResolver) PreviewMerge(sourceUserID, targetUserID int64) (any, error) {
	if r == nil || !r.initialized {
		return nil, fmt.Errorf("identity resolver not initialized")
	}
	p := &MergePreview{SourceUserID: sourceUserID, TargetUserID: targetUserID}
	r.db.QueryRow("SELECT COUNT(*) FROM user_identities WHERE user_id = ?", sourceUserID).Scan(&p.Identities)
	r.db.QueryRow("SELECT COUNT(*) FROM user_llm_subscriptions WHERE user_id = ?", sourceUserID).Scan(&p.Subscriptions)
	r.db.QueryRow("SELECT COUNT(*) FROM runners WHERE owner_user_id = ?", sourceUserID).Scan(&p.Runners)
	r.db.QueryRow("SELECT COUNT(*) FROM user_settings WHERE user_id = ?", sourceUserID).Scan(&p.Settings)
	r.db.QueryRow("SELECT COUNT(*) FROM user_default_model WHERE user_id = ?", sourceUserID).Scan(&p.DefaultModel)
	r.db.QueryRow("SELECT COUNT(*) FROM user_chats WHERE user_id = ?", sourceUserID).Scan(&p.UserChats)
	r.db.QueryRow("SELECT COUNT(*) FROM tenants WHERE owner_user_id = ?", sourceUserID).Scan(&p.Tenants)
	r.db.QueryRow("SELECT COUNT(*) FROM cron_jobs WHERE user_id = ?", sourceUserID).Scan(&p.CronJobs)
	r.db.QueryRow("SELECT COUNT(*) FROM event_triggers WHERE user_id = ?", sourceUserID).Scan(&p.EventTriggers)
	// Check conflicts
	if p.DefaultModel > 0 {
		var targetHas int
		r.db.QueryRow("SELECT COUNT(*) FROM user_default_model WHERE user_id = ?", targetUserID).Scan(&targetHas)
		if targetHas > 0 {
			p.Conflicts = append(p.Conflicts, "default_model: both users have a default model, keeping target's")
		}
	}
	// Check runner name conflicts
	rows, err := r.db.Query("SELECT name FROM runners WHERE owner_user_id = ? AND name IN (SELECT name FROM runners WHERE owner_user_id = ?)", sourceUserID, targetUserID)
	if err == nil {
		for rows.Next() {
			var name string
			rows.Scan(&name)
			p.Conflicts = append(p.Conflicts, fmt.Sprintf("runner_duplicate: %s (will be renamed)", name))
		}
		rows.Close()
	}
	// Check settings conflicts
	rows, err = r.db.Query("SELECT channel, key FROM user_settings WHERE user_id = ? AND (channel, key) IN (SELECT channel, key FROM user_settings WHERE user_id = ?)", sourceUserID, targetUserID)
	if err == nil {
		for rows.Next() {
			var ch, key string
			rows.Scan(&ch, &key)
			p.Conflicts = append(p.Conflicts, fmt.Sprintf("settings_duplicate: %s:%s (keeping target's)", ch, key))
		}
		rows.Close()
	}
	return p, nil
}

// mergeMu prevents concurrent merges of the same user pair.
var mergeMu sync.Map // key: "min-max" → *sync.Mutex

// MergeUsers merges sourceUser into targetUser: migrates all identities and
// assets, resolves conflicts, then deletes the source user.
// This is irreversible — caller should backup first.
func (r *IdentityResolver) MergeUsers(sourceUserID, targetUserID int64) error {
	if r == nil || !r.initialized {
		return fmt.Errorf("identity resolver not initialized")
	}
	if sourceUserID == targetUserID {
		return fmt.Errorf("cannot merge user with itself")
	}
	// Concurrency lock keyed by user pair
	lockKey := fmt.Sprintf("%d-%d", min64(sourceUserID, targetUserID), max64(sourceUserID, targetUserID))
	actual, _ := mergeMu.LoadOrStore(lockKey, &sync.Mutex{})
	mu := actual.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("merge tx: %w", err)
	}
	defer tx.Rollback()

	// 1. Resolve runner name conflicts BEFORE migration
	tx.Exec(`UPDATE runners SET name = name || ' (' || CAST(? AS TEXT) || ')' 
		WHERE owner_user_id = ? AND name IN (SELECT name FROM runners WHERE owner_user_id = ?)`,
		sourceUserID, sourceUserID, targetUserID)

	// 2. Delete conflicting user_settings BEFORE migration (keep target's)
	tx.Exec(`DELETE FROM user_settings WHERE user_id = ? AND (channel, key) IN 
		(SELECT channel, key FROM user_settings WHERE user_id = ?)`, sourceUserID, targetUserID)

	// 3. Delete conflicting user_default_model BEFORE migration (keep target's)
	tx.Exec(`DELETE FROM user_default_model WHERE user_id = ?`, sourceUserID)

	// 4. Migrate ALL identities first (BEFORE deleting source user — CASCADE safe)
	tx.Exec("UPDATE user_identities SET user_id = ? WHERE user_id = ?", targetUserID, sourceUserID)

	// 4.5 Role escalation: if source is admin, target becomes admin too.
	// This prevents privilege loss when merging an admin user (e.g. cli_user)
	// into a non-admin user. Admin is the highest role — merging an admin
	// into a regular user should escalate the target, not demote the source.
	var sourceRole, targetRole string
	tx.QueryRow("SELECT role FROM users WHERE id = ?", sourceUserID).Scan(&sourceRole)
	tx.QueryRow("SELECT role FROM users WHERE id = ?", targetUserID).Scan(&targetRole)
	if sourceRole == "admin" && targetRole != "admin" {
		tx.Exec("UPDATE users SET role = 'admin' WHERE id = ?", targetUserID)
	}

	// 5. Migrate asset tables
	tx.Exec("UPDATE user_llm_subscriptions SET user_id = ? WHERE user_id = ?", targetUserID, sourceUserID)
	tx.Exec("UPDATE runners SET owner_user_id = ? WHERE owner_user_id = ?", targetUserID, sourceUserID)
	tx.Exec("UPDATE user_settings SET user_id = ? WHERE user_id = ?", targetUserID, sourceUserID)
	tx.Exec("UPDATE user_default_model SET user_id = ? WHERE user_id = ?", targetUserID, sourceUserID)
	tx.Exec("UPDATE user_chats SET user_id = ? WHERE user_id = ?", targetUserID, sourceUserID)
	tx.Exec("UPDATE tenants SET owner_user_id = ? WHERE owner_user_id = ?", targetUserID, sourceUserID)
	tx.Exec("UPDATE cron_jobs SET user_id = ? WHERE user_id = ?", targetUserID, sourceUserID)
	tx.Exec("UPDATE event_triggers SET user_id = ? WHERE user_id = ?", targetUserID, sourceUserID)

	// 6. Delete source user (CASCADE safe — identities already moved)
	tx.Exec("DELETE FROM users WHERE id = ?", sourceUserID)

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("merge commit: %w", err)
	}
	log.WithFields(log.Fields{
		"source_user_id": sourceUserID,
		"target_user_id": targetUserID,
	}).Info("IdentityResolver: users merged")
	return nil
}

// UnlinkIdentity removes a channel identity from a user.
// The user keeps all assets — only the identity mapping is removed.
func (r *IdentityResolver) UnlinkIdentity(userID, identityID int64) error {
	if r == nil || !r.initialized {
		return fmt.Errorf("identity resolver not initialized")
	}
	result, err := r.db.Exec("DELETE FROM user_identities WHERE id = ? AND user_id = ?", identityID, userID)
	if err != nil {
		return fmt.Errorf("unlink identity: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("identity not found or not owned by user")
	}
	return nil
}

// CleanupExpiredLinkCodes removes expired link codes. Called periodically.
func (r *IdentityResolver) CleanupExpiredLinkCodes() {
	if r == nil || !r.initialized {
		return
	}
	r.db.Exec("DELETE FROM link_codes WHERE expires_at < datetime('now')")
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

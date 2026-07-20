package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	log "xbot/logger"
)

// TenantService handles tenant CRUD operations
type TenantService struct {
	db *DB
}

var ErrTenantOwnerConflict = errors.New("tenant belongs to another user")

type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func canonicalUserID(q rowQuerier, channel, senderID string) (int64, error) {
	if channel == "" || senderID == "" {
		return 0, nil
	}
	var userID int64
	err := q.QueryRow(
		`SELECT user_id FROM user_identities WHERE channel = ? AND channel_user_id = ?`,
		channel, senderID,
	).Scan(&userID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("resolve canonical user: %w", err)
	}
	return userID, nil
}

func backfillSessionOwnership(tx *sql.Tx, channel, chatID string) error {
	if _, err := tx.Exec(`
UPDATE user_chats
SET user_id = COALESCE((
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel = user_chats.channel
      AND ui.channel_user_id = user_chats.sender_id
), 0)
WHERE channel = ? AND chat_id = ? AND COALESCE(user_id, 0) = 0
`, channel, chatID); err != nil {
		return fmt.Errorf("backfill chat owner: %w", err)
	}
	if _, err := tx.Exec(`
UPDATE tenants
SET owner_user_id = COALESCE(
    (SELECT NULLIF(uc.user_id, 0) FROM user_chats uc
     WHERE uc.channel = tenants.channel AND uc.chat_id = tenants.chat_id
     ORDER BY uc.id LIMIT 1),
    (SELECT ui.user_id FROM user_identities ui
     WHERE ui.channel = tenants.channel AND ui.channel_user_id = tenants.chat_id),
    0
)
WHERE channel = ? AND chat_id = ? AND COALESCE(owner_user_id, 0) = 0
`, channel, chatID); err != nil {
		return fmt.Errorf("backfill tenant owner: %w", err)
	}
	return nil
}

// NewTenantService creates a new tenant service
func NewTenantService(db *DB) *TenantService {
	return &TenantService{db: db}
}

// ClaimOrVerifyTenantOwner atomically creates or claims a tenant for a
// canonical user, and rejects tenants already owned by another user.
func ClaimOrVerifyTenantOwner(conn *sql.DB, channel, chatID string, canonicalUserID int64) (int64, error) {
	if conn == nil {
		return 0, fmt.Errorf("database not initialized")
	}
	if channel == "" || chatID == "" || canonicalUserID <= 0 {
		return 0, fmt.Errorf("channel, chat ID, and canonical user ID are required")
	}

	tx, err := conn.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tenant owner claim: %w", err)
	}
	defer tx.Rollback()

	now := time.Now()
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO tenants (channel, chat_id, owner_user_id, created_at, last_active_at) VALUES (?, ?, ?, ?, ?)`,
		channel, chatID, canonicalUserID, now, now,
	); err != nil {
		return 0, fmt.Errorf("create tenant owner claim: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE tenants SET owner_user_id = ? WHERE channel = ? AND chat_id = ? AND COALESCE(owner_user_id, 0) = 0`,
		canonicalUserID, channel, chatID,
	); err != nil {
		return 0, fmt.Errorf("claim tenant owner: %w", err)
	}

	var tenantID, ownerUserID int64
	if err := tx.QueryRow(
		`SELECT id, COALESCE(owner_user_id, 0) FROM tenants WHERE channel = ? AND chat_id = ?`,
		channel, chatID,
	).Scan(&tenantID, &ownerUserID); err != nil {
		return 0, fmt.Errorf("verify tenant owner: %w", err)
	}
	if ownerUserID != canonicalUserID {
		return 0, ErrTenantOwnerConflict
	}
	if _, err := tx.Exec(`UPDATE tenants SET last_active_at = ? WHERE id = ?`, now, tenantID); err != nil {
		return 0, fmt.Errorf("update tenant activity: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit tenant owner claim: %w", err)
	}
	return tenantID, nil
}

func (s *TenantService) ClaimOrVerifyTenantOwner(channel, chatID string, canonicalUserID int64) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("tenant service not initialized")
	}
	return ClaimOrVerifyTenantOwner(s.db.Conn(), channel, chatID, canonicalUserID)
}

// GetOrCreateTenantIDWithOwner is the owned-session creation path. A positive
// canonical user ID is claimed atomically; standalone callers can keep using
// GetOrCreateTenantID without ownership.
func (s *TenantService) GetOrCreateTenantIDWithOwner(channel, chatID string, canonicalUserID int64) (int64, error) {
	if canonicalUserID <= 0 {
		return s.GetOrCreateTenantID(channel, chatID)
	}
	return s.ClaimOrVerifyTenantOwner(channel, chatID, canonicalUserID)
}

// GetOrCreateTenantID retrieves a tenant ID by (channel, chat_id), creating it if it doesn't exist.
// Uses INSERT OR IGNORE within a transaction to avoid TOCTOU race conditions.
// The UNIQUE(channel, chat_id) constraint on the tenants table guarantees uniqueness.
func (s *TenantService) GetOrCreateTenantID(channel, chatID string) (int64, error) {
	conn := s.db.Conn()

	tx, err := conn.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now()

	// INSERT OR IGNORE: if the row already exists (UNIQUE constraint), it is silently skipped.
	_, err = tx.Exec(
		"INSERT OR IGNORE INTO tenants (channel, chat_id, created_at, last_active_at) VALUES (?, ?, ?, ?)",
		channel, chatID, now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("insert or ignore tenant: %w", err)
	}

	// SELECT the tenant ID (works for both newly inserted and pre-existing rows).
	var tenantID int64
	err = tx.QueryRow(
		"SELECT id FROM tenants WHERE channel = ? AND chat_id = ?",
		channel, chatID,
	).Scan(&tenantID)
	if err != nil {
		return 0, fmt.Errorf("select tenant: %w", err)
	}
	if err := backfillSessionOwnership(tx, channel, chatID); err != nil {
		return 0, err
	}

	// Always update last_active_at to reflect current usage.
	if _, err := tx.Exec(
		"UPDATE tenants SET last_active_at = ? WHERE id = ?",
		now, tenantID,
	); err != nil {
		log.WithError(err).Warn("Failed to update tenant last_active_at")
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}

	return tenantID, nil
}

// GetTenantInfo retrieves tenant information by ID
func (s *TenantService) GetTenantInfo(tenantID int64) (channel, chatID string, err error) {
	conn := s.db.Conn()
	err = conn.QueryRow(
		"SELECT channel, chat_id FROM tenants WHERE id = ?",
		tenantID,
	).Scan(&channel, &chatID)
	if err != nil {
		return "", "", fmt.Errorf("query tenant info: %w", err)
	}
	return channel, chatID, nil
}

// DeleteTenant removes a tenant and all associated data (cascade)
func (s *TenantService) DeleteTenant(tenantID int64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("tenant service not initialized")
	}
	conn := s.db.Conn()
	result, err := conn.Exec("DELETE FROM tenants WHERE id = ?", tenantID)
	if err != nil {
		return fmt.Errorf("delete tenant: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("tenant not found: %d", tenantID)
	}
	log.WithField("tenant_id", tenantID).Info("Tenant deleted")
	return nil
}

// GetTenantIDByChannelChatID looks up the tenant ID for (channel, chatID) without creating one.
// Returns (0, nil) if not found.
func (s *TenantService) GetTenantIDByChannelChatID(channel, chatID string) (int64, error) {
	conn := s.db.Conn()
	var tenantID int64
	err := conn.QueryRow(
		"SELECT id FROM tenants WHERE channel = ? AND chat_id = ?",
		channel, chatID,
	).Scan(&tenantID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get tenant by channel/chat: %w", err)
	}
	return tenantID, nil
}

// ListTenants returns all tenants with optional label from user_chats.
func (s *TenantService) ListTenants() ([]TenantInfo, error) {
	conn := s.db.Conn()
	rows, err := conn.Query(
		`SELECT t.id, t.channel, t.chat_id, COALESCE(c.label, '') as label,
										COALESCE(t.subscription_id, '') as sub_id, COALESCE(t.model, '') as model,
										t.created_at, t.last_active_at
		FROM tenants t
		LEFT JOIN user_chats c ON c.channel = t.channel AND c.chat_id = t.chat_id
		WHERE t.channel != '_shared'
		ORDER BY t.last_active_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()

	var tenants []TenantInfo
	for rows.Next() {
		var t TenantInfo
		var createdAt, lastActiveAt string
		if err := rows.Scan(&t.ID, &t.Channel, &t.ChatID, &t.Label, &t.SubscriptionID, &t.Model, &createdAt, &lastActiveAt); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		t.CreatedAt = parseSQLiteTime(createdAt)
		t.LastActiveAt = parseSQLiteTime(lastActiveAt)
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenants: %w", err)
	}
	return tenants, nil
}

// TenantInfo contains tenant information
type TenantInfo struct {
	ID             int64
	Channel        string
	ChatID         string
	Label          string `json:"label,omitempty"`
	SubscriptionID string `json:"subscription_id,omitempty"`
	Model          string `json:"model,omitempty"`
	CreatedAt      time.Time
	LastActiveAt   time.Time
}

// SetTenantSubscription persists the session→subscription mapping to the tenants table.
// This is the backend source of truth for which subscription a session uses.
// If the tenant row doesn't exist (e.g. CLI session created locally, never written to DB),
// it is auto-created via INSERT OR IGNORE first.
func (s *TenantService) SetTenantSubscription(channel, chatID, subscriptionID, model string) error {
	conn := s.db.Conn()
	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("begin set tenant subscription: %w", err)
	}
	defer tx.Rollback()

	// Ensure tenant row exists (no-op if already present).
	if _, err := tx.Exec(
		"INSERT OR IGNORE INTO tenants (channel, chat_id) VALUES (?, ?)",
		channel, chatID,
	); err != nil {
		return fmt.Errorf("ensure tenant for subscription: %w", err)
	}

	var tenantID int64
	var oldSubscriptionID, oldModel string
	if err := tx.QueryRow(
		"SELECT id, COALESCE(subscription_id, ''), COALESCE(model, '') FROM tenants WHERE channel = ? AND chat_id = ?",
		channel, chatID,
	).Scan(&tenantID, &oldSubscriptionID, &oldModel); err != nil {
		return fmt.Errorf("read tenant subscription: %w", err)
	}
	if err := backfillSessionOwnership(tx, channel, chatID); err != nil {
		return err
	}

	var modelID string
	if subscriptionID != "" && model != "" {
		if err := tx.QueryRow(
			"SELECT id FROM subscription_models WHERE subscription_id = ? AND model = ?",
			subscriptionID, model,
		).Scan(&modelID); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("resolve tenant model id: %w", err)
		}
	}
	result, err := tx.Exec(
		"UPDATE tenants SET subscription_id = ?, model = ?, model_id = ? WHERE id = ?",
		subscriptionID, model, modelID, tenantID,
	)
	if err != nil {
		return fmt.Errorf("set tenant subscription: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("set tenant subscription: tenant %s/%s not found after insert", channel, chatID)
	}
	if oldSubscriptionID != subscriptionID || oldModel != model {
		if _, err := tx.Exec(
			"UPDATE tenant_state SET last_prompt_tokens = 0, last_completion_tokens = 0 WHERE tenant_id = ?",
			tenantID,
		); err != nil {
			return fmt.Errorf("clear tenant token state after model change: %w", err)
		}
		if _, err := tx.Exec(`
UPDATE session_messages SET context_tokens = 0
WHERE id = (
SELECT id FROM session_messages
WHERE tenant_id = ? AND role = 'user' AND COALESCE(display_only, 0) = 0
ORDER BY id DESC LIMIT 1
)
`, tenantID); err != nil {
			return fmt.Errorf("clear latest user context tokens after model change: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set tenant subscription: %w", err)
	}
	return nil
}

// GetTenantSubscription reads the session→subscription mapping from the tenants table.
// Returns empty strings if no mapping exists.
func (s *TenantService) GetTenantSubscription(channel, chatID string) (subscriptionID, model string, err error) {
	conn := s.db.Conn()
	err = conn.QueryRow(
		"SELECT subscription_id, model FROM tenants WHERE channel = ? AND chat_id = ?",
		channel, chatID,
	).Scan(&subscriptionID, &model)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("get tenant subscription: %w", err)
	}
	return subscriptionID, model, nil
}

// SetTenantRunner persists the session→runner binding to the tenants table.
func (s *TenantService) SetTenantRunner(channel, chatID, runnerID string) error {
	conn := s.db.Conn()
	_, _ = conn.Exec(
		"INSERT OR IGNORE INTO tenants (channel, chat_id) VALUES (?, ?)",
		channel, chatID,
	)
	result, err := conn.Exec(
		"UPDATE tenants SET runner_id = ? WHERE channel = ? AND chat_id = ?",
		runnerID, channel, chatID,
	)
	if err != nil {
		return fmt.Errorf("set tenant runner: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("set tenant runner: tenant %s/%s not found after insert", channel, chatID)
	}
	return nil
}

// GetTenantRunner reads the session→runner binding from the tenants table.
// Returns empty string if no binding exists.
func (s *TenantService) GetTenantRunner(channel, chatID string) (string, error) {
	conn := s.db.Conn()
	var runnerID string
	err := conn.QueryRow(
		"SELECT runner_id FROM tenants WHERE channel = ? AND chat_id = ?",
		channel, chatID,
	).Scan(&runnerID)
	if err != nil {
		return "", nil // not found is not an error
	}
	return runnerID, nil
}

// ClearSubscriptionFromTenants resets subscription_id and model for all tenant
// rows currently pointing to the given subscription ID. Called when a subscription
// is deleted — prevents stale references that would cause ResolveLLM to waste
// cycles looking up a non-existent subscription before falling back to default.
func (s *TenantService) ClearSubscriptionFromTenants(subID string) error {
	if subID == "" {
		return nil
	}
	conn := s.db.Conn()
	_, err := conn.Exec(
		"UPDATE tenants SET subscription_id = '', model = '' WHERE subscription_id = ?",
		subID,
	)
	if err != nil {
		return fmt.Errorf("clear tenant subscription: %w", err)
	}
	return nil
}

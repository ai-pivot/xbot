package sqlite

import (
	"database/sql"
	"fmt"
	"time"

	log "xbot/logger"
)

// TenantService handles tenant CRUD operations
type TenantService struct {
	db *DB
}

// NewTenantService creates a new tenant service
func NewTenantService(db *DB) *TenantService {
	return &TenantService{db: db}
}

// GetOrCreateTenantID retrieves a tenant ID by (channel, chat_id), creating it if it doesn't exist.
// Uses INSERT OR IGNORE within a transaction to avoid TOCTOU race conditions.
// The UNIQUE(channel, chat_id) constraint on the tenants table guarantees uniqueness.
// GetOrCreateTenantID returns the tenant ID for (channel, chatID), creating it
// if absent. It is a pure lookup for pre-existing rows — it does NOT refresh
// last_active_at for existing tenants (see TouchTenantID for that). Only the
// INSERT path sets last_active_at (creation time). This keeps read-only paths
// (session-tree pagination, widget CWD lookup) free of write side effects —
// previously every "get" bumped last_active_at, which scrambled last-active
// ordering during pagination and made sidebar "update time" jump to today.
func (s *TenantService) GetOrCreateTenantID(channel, chatID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("tenant service not initialized")
	}
	// Process-wide write gate — see db.writeMu. SQLite allows one writer at a
	// time and the modernc driver can bypass busy_timeout on the write-lock
	// acquisition path; serializing in-process removes the collision at source.
	s.db.writeMu.Lock()
	defer s.db.writeMu.Unlock()

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

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}

	return tenantID, nil
}

// TouchTenantID bumps last_active_at for an existing tenant (or creates the row
// first). Use this ONLY for genuine user activity (opening a session, sending a
// message) — NOT for read-only lookups like session-tree pagination.
func (s *TenantService) TouchTenantID(channel, chatID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("tenant service not initialized")
	}
	tenantID, err := s.GetOrCreateTenantID(channel, chatID)
	if err != nil {
		return err
	}
	_, err = s.db.Conn().Exec(
		"UPDATE tenants SET last_active_at = ? WHERE id = ?",
		time.Now(), tenantID,
	)
	if err != nil {
		return fmt.Errorf("touch tenant last_active_at: %w", err)
	}
	return nil
}

// GetTenantInfo retrieves tenant information by ID
func (s *TenantService) GetTenantInfo(tenantID int64) (channel, chatID string, err error) {
	if s == nil || s.db == nil {
		return "", "", fmt.Errorf("tenant service not initialized")
	}
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
	// Delete iteration_history first (no FK cascade when foreign_keys=OFF).
	_, _ = conn.Exec("DELETE FROM iteration_history WHERE tenant_id = ?", tenantID)
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
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("tenant service not initialized")
	}
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
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("tenant service not initialized")
	}
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
		// v71（每会话一个 DB）：session_messages.context_tokens 的清零（模型切换
		// 重置 token 基线）已移出本方法 —— session_messages 在会话库，而本方法只
		// 操作主库。清零由 LLMFactory 的 sessionTokenResetter 钩子完成（模型变更
		// 时经 MultiTenantSession.ResetSessionContextTokens 清会话库），见
		// llm_factory.go 的 SetSessionLLM / SelectModel。
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set tenant subscription: %w", err)
	}
	return nil
}

// GetTenantSubscription reads the session→subscription mapping from the tenants table.
// Returns empty strings if no mapping exists.
func (s *TenantService) GetTenantSubscription(channel, chatID string) (subscriptionID, model string, err error) {
	if s == nil || s.db == nil {
		return "", "", fmt.Errorf("tenant service not initialized")
	}
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
	if s == nil || s.db == nil {
		return "", fmt.Errorf("tenant service not initialized")
	}
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

// ── 每会话一个 DB（one session, one DB）：tenants 注册表方法（v71）──────────────
//
// tenants 表是会话库的注册表：db_path（会话库相对路径）+ migrated（数据是否已
// 惰性迁移到会话库）+ preview（跨会话列表的最新消息预览 —— 拆库后主库没有
// session_messages，预览由写入路径维护到这一列）。这些方法全部操作主库。

// TenantDBInfo 是会话库注册信息（tenants 行的会话库三列）。
type TenantDBInfo struct {
	DBPath   string // 会话库相对路径（相对主库目录；空 = 尚未分配）
	Migrated bool   // 会话数据是否已迁移到会话库（false = 主库 session_messages 仍是权威）
}

// GetTenantDBInfo reads the per-session-DB registry columns for a tenant.
func (s *TenantService) GetTenantDBInfo(tenantID int64) (TenantDBInfo, error) {
	if s == nil || s.db == nil {
		return TenantDBInfo{}, fmt.Errorf("tenant service not initialized")
	}
	var info TenantDBInfo
	var migrated int
	err := s.db.Conn().QueryRow(
		"SELECT COALESCE(db_path, ''), COALESCE(migrated, 0) FROM tenants WHERE id = ?",
		tenantID,
	).Scan(&info.DBPath, &migrated)
	if err != nil {
		return TenantDBInfo{}, fmt.Errorf("get tenant db info: %w", err)
	}
	info.Migrated = migrated != 0
	return info, nil
}

// SetTenantDBPath persists the session DB's relative path (registry is the
// single authority — path derivation happens once, then this column is read).
func (s *TenantService) SetTenantDBPath(tenantID int64, dbPath string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("tenant service not initialized")
	}
	if _, err := s.db.Conn().Exec(
		"UPDATE tenants SET db_path = ? WHERE id = ?", dbPath, tenantID,
	); err != nil {
		return fmt.Errorf("set tenant db_path: %w", err)
	}
	return nil
}

// SetTenantMigrated marks a tenant's session data as migrated to its session DB.
// Called AFTER the copy commits — the flag is what stops a re-copy from wiping
// post-migration writes (the copy is DELETE+INSERT, idempotent only while
// migrated=0).
//
// ⛔ v72 ordering contract (data-loss guard): after this call, callers should
// delete the tenant's legacy main-DB rows (DeleteTenantHistory) — NEVER the
// reverse order. See CopyTenantDataFromMainDB for the full contract.
func (s *TenantService) SetTenantMigrated(tenantID int64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("tenant service not initialized")
	}
	if _, err := s.db.Conn().Exec(
		"UPDATE tenants SET migrated = 1 WHERE id = ?", tenantID,
	); err != nil {
		return fmt.Errorf("set tenant migrated: %w", err)
	}
	return nil
}

// DeleteTenantHistory deletes a tenant's rows from the MAIN DB's legacy
// session_messages/iteration_history (v72 invariant: the main DB only holds
// data for migrated=0 stragglers; once a session is migrated, its data lives
// exclusively in its session DB). No-op when the rows are already gone.
//
// ⛔ Ordering contract (data-loss guard): callers MUST have set
// tenants.migrated=1 BEFORE calling this — the inverse order with a crash in
// between leaves the tenant unmigrated with EMPTY main-DB rows, and the next
// migration re-copies from EMPTY = permanent data loss. Flag-first + crash in
// between leaves only harmless stale main-DB rows. See
// CopyTenantDataFromMainDB's re-run contract.
func (s *TenantService) DeleteTenantHistory(tenantID int64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("tenant service not initialized")
	}
	conn := s.db.Conn()
	if _, err := conn.Exec("DELETE FROM session_messages WHERE tenant_id = ?", tenantID); err != nil {
		return fmt.Errorf("delete main-db session_messages: %w", err)
	}
	if _, err := conn.Exec("DELETE FROM iteration_history WHERE tenant_id = ?", tenantID); err != nil {
		return fmt.Errorf("delete main-db iteration_history: %w", err)
	}
	return nil
}

// SetTenantPreview updates the cross-session list preview (the latest
// user/assistant message, truncated). The write path calls this after each
// eligible append; the read path (ListUserChats / listTenantsByChannel) reads
// this column instead of joining session_messages (which lives in per-session
// DBs after the split). substr(?, 1, 256) matches the old subquery's bound.
func (s *TenantService) SetTenantPreview(tenantID int64, content string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("tenant service not initialized")
	}
	if _, err := s.db.Conn().Exec(
		"UPDATE tenants SET preview = substr(?, 1, 256) WHERE id = ?", content, tenantID,
	); err != nil {
		return fmt.Errorf("set tenant preview: %w", err)
	}
	return nil
}

// SetTenantCWD persists a session's current working directory in the tenants
// table (the single authoritative store; file-based session_cwd is retired).
// Moved from SessionService (v71 per-session DB split): tenants is a main-DB
// table, and SessionService is now bound to the per-session DB.
func (s *TenantService) SetTenantCWD(tenantID int64, cwd string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("tenant service not initialized")
	}
	if _, err := s.db.Conn().Exec("UPDATE tenants SET cwd = ? WHERE id = ?", cwd, tenantID); err != nil {
		return fmt.Errorf("update tenants.cwd: %w", err)
	}
	return nil
}

// GetTenantCWD reads a session's persisted CWD from the tenants table.
// Returns "" when the session has no persisted CWD (fresh session).
// Moved from SessionService (v71 per-session DB split): tenants is a main-DB
// table, and SessionService is now bound to the per-session DB.
func (s *TenantService) GetTenantCWD(tenantID int64) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("tenant service not initialized")
	}
	var cwd sql.NullString
	err := s.db.Conn().QueryRow("SELECT cwd FROM tenants WHERE id = ?", tenantID).Scan(&cwd)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("get tenants.cwd: %w", err)
	}
	if cwd.Valid {
		return cwd.String, nil
	}
	return "", nil
}

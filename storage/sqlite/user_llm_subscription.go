package sqlite

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"xbot/crypto"
	log "xbot/logger"
	"xbot/protocol"
)

// PerModelConfig stores per-model token overrides within a subscription.
// Alias to protocol.PerModelConfig — the canonical definition used across all packages.
type PerModelConfig = protocol.PerModelConfig

// SystemSenderID is the reserved sender_id for the shared system subscription.
// The system subscription is reconciled from config/env at boot, visible to all
// users, read-only, and acts as the lowest-priority default/fallback LLM.
const SystemSenderID = "__system__"

// SystemSubscriptionName is the display name of the system subscription.
const SystemSubscriptionName = "system"

// LLMSubscription represents a user's LLM provider subscription.
type LLMSubscription struct {
	ID              string                    // unique subscription ID
	SenderID        string                    // user ID
	Name            string                    // display name (e.g. "OpenAI GPT-4", "DeepSeek")
	Provider        string                    // LLM provider: "openai", "deepseek", "anthropic", etc.
	BaseURL         string                    // API base URL
	APIKey          string                    // API key (plaintext in struct, encrypted in DB)
	Model           string                    // default model for this subscription
	MaxContext      int                       // max context token limit (0 = use default)
	MaxOutputTokens int                       // max output token limit (0 = use default 8192)
	ThinkingMode    string                    // thinking mode: "" (auto), "enabled", "disabled"
	APIType         string                    // API type: "" (default=chat_completions), "responses"
	IsDefault       bool                      // whether this is the active subscription
	Enabled         bool                      // whether this subscription contributes models to the picker (v40); default true
	IsSystem        bool                      // whether this is the shared system subscription (v44); read-only, fallback default
	CachedModels    []string                  // cached model list from API (JSON in DB)
	PerModelConfigs map[string]PerModelConfig // per-model token overrides (JSON in DB)
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SubscriptionModel stores per-model configuration for a subscription.
// Introduced in v35 to replace the JSON-blob PerModelConfigs and subscription-level
// model fields. One subscription → many models.
type SubscriptionModel struct {
	ID              string // unique model row ID
	SubscriptionID  string // FK → user_llm_subscriptions.id
	Model           string // model name (e.g. "deepseek-v4-pro")
	MaxContext      int    // max context window tokens
	MaxOutputTokens int    // max output tokens
	ThinkingMode    string // thinking mode override
	APIType         string // API type override: "" (use subscription default), "responses"
	Enabled         bool   // whether this model is selectable (v38); default true
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// LLMSubscriptionService manages user LLM subscriptions.
type LLMSubscriptionService struct {
	db *DB
}

// NewLLMSubscriptionService creates a new LLMSubscriptionService.
func NewLLMSubscriptionService(db *DB) *LLMSubscriptionService {
	return &LLMSubscriptionService{db: db}
}

// scanSubscription scans a single subscription row from the given scanner.
// SQLite stores created_at/updated_at as TEXT, so we scan into string and parse.
// IsDefault is NOT populated here — it's a read-side projection derived from
// user_default_model (GetDefault/List mark it). The is_default column was dropped in v43.
func scanSubscription(scanner interface{ Scan(...any) error }, sub *LLMSubscription) (string, error) {
	var encryptedAPIKey string
	var enabled int
	var isSystem int
	var createdAt, updatedAt string
	var cachedModelsJSON string
	err := scanner.Scan(&sub.ID, &sub.SenderID, &sub.Name, &sub.Provider, &sub.BaseURL,
		&encryptedAPIKey, &sub.Model, &enabled, &sub.MaxContext, &sub.MaxOutputTokens, &sub.ThinkingMode, &sub.APIType,
		&cachedModelsJSON, &createdAt, &updatedAt, &isSystem)
	if err != nil {
		return "", err
	}
	sub.Enabled = enabled == 1
	sub.IsSystem = isSystem == 1
	sub.CreatedAt = parseSQLiteTime(createdAt)
	sub.UpdatedAt = parseSQLiteTime(updatedAt)
	if cachedModelsJSON != "" {
		_ = json.Unmarshal([]byte(cachedModelsJSON), &sub.CachedModels)
	}
	return encryptedAPIKey, nil
}

// loadPerModelConfigs populates sub.PerModelConfigs from the subscription_models
// table (the sole source since v42). Called after scanSubscription.
func (s *LLMSubscriptionService) loadPerModelConfigs(sub *LLMSubscription) {
	if sub == nil {
		return
	}
	models, err := s.GetModels(sub.ID)
	if err != nil {
		log.WithError(err).WithField("sub_id", sub.ID).Warn("failed to load subscription_models")
		return
	}
	sub.PerModelConfigs = make(map[string]PerModelConfig, len(models))
	for _, m := range models {
		sub.PerModelConfigs[m.Model] = PerModelConfig{
			MaxOutputTokens: m.MaxOutputTokens,
			MaxContext:      m.MaxContext,
			APIType:         m.APIType,
			Enabled:         m.Enabled,
		}
	}
}

// decryptAPIKey decrypts the subscription's API key in place.
func decryptAPIKey(sub *LLMSubscription, encryptedAPIKey string) {
	if encryptedAPIKey != "" {
		decrypted, err := crypto.Decrypt(encryptedAPIKey)
		if err != nil {
			log.WithError(err).WithField("sub_id", sub.ID).Warn("failed to decrypt API key")
			sub.APIKey = "(decryption failed)"
		} else {
			sub.APIKey = decrypted
		}
	}
}

// ListAll returns all subscriptions across all users, ordered by creation time.
func (s *LLMSubscriptionService) ListAll() ([]*LLMSubscription, error) {
	conn := s.db.Conn()
	rows, err := conn.Query(`
		SELECT id, sender_id, name, provider, base_url, api_key, model, enabled, max_context, max_output_tokens, thinking_mode, api_type, cached_models, created_at, updated_at, is_system
			FROM user_llm_subscriptions
			ORDER BY is_system DESC, created_at ASC
		`)
	if err != nil {
		return nil, fmt.Errorf("list all subscriptions: %w", err)
	}
	defer rows.Close()

	var subs []*LLMSubscription
	for rows.Next() {
		sub := &LLMSubscription{}
		encryptedAPIKey, err := scanSubscription(rows, sub)
		if err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		decryptAPIKey(sub, encryptedAPIKey)
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	// Populate PerModelConfigs AFTER closing the rows cursor — the SQLite pool is
	// single-connection, so a nested query inside the rows loop would deadlock.
	for _, sub := range subs {
		s.loadPerModelConfigs(sub)
	}
	s.markDefaultsAll(subs)
	return subs, nil
}

// List returns all subscriptions for a user, ordered by creation time.
func (s *LLMSubscriptionService) List(senderID string) ([]*LLMSubscription, error) {
	conn := s.db.Conn()
	rows, err := conn.Query(`
			SELECT id, sender_id, name, provider, base_url, api_key, model, enabled, max_context, max_output_tokens, thinking_mode, api_type, cached_models, created_at, updated_at, is_system
				FROM user_llm_subscriptions
				WHERE sender_id = ? OR is_system = 1
				ORDER BY is_system DESC, created_at ASC
			`, senderID)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	defer rows.Close()

	var subs []*LLMSubscription
	for rows.Next() {
		sub := &LLMSubscription{}
		encryptedAPIKey, err := scanSubscription(rows, sub)
		if err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		decryptAPIKey(sub, encryptedAPIKey)
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for _, sub := range subs {
		s.loadPerModelConfigs(sub)
	}
	s.markDefaultsFor(subs, senderID)
	return subs, nil
}

// markDefaultsFor is a no-op. IsDefault/Active projection has been retired —
// subscriptions no longer have a "default" concept. The user_default_model
// table is repurposed as "last used model" storage, not a default marker.
// Kept as no-op to avoid touching all List/GetDefault call sites.
func (s *LLMSubscriptionService) markDefaultsFor(subs []*LLMSubscription, senderID string) {
	// IsDefault is always false — no "default subscription" concept.
}

// markDefaultsAll is a no-op (see markDefaultsFor).
func (s *LLMSubscriptionService) markDefaultsAll(subs []*LLMSubscription) {
	// IsDefault is always false — no "default subscription" concept.
}

// GetDefault returns the user's last-used subscription (from user_default_model,
// repurposed as "last used model" storage). If none set, falls back to the shared
// system subscription (v44), which is always present after boot reconcile.
// NOTE: The name "GetDefault" is retained for compatibility but the semantics
// are now "last used", not "default".
func (s *LLMSubscriptionService) GetDefault(senderID string) (*LLMSubscription, error) {
	udm, err := s.GetUserDefaultModel(senderID)
	if err != nil {
		return nil, fmt.Errorf("get default subscription: %w", err)
	}
	if udm == nil {
		sys, err := s.GetSystemSubscription()
		if err != nil {
			return nil, fmt.Errorf("get default subscription (system fallback): %w", err)
		}
		if sys != nil {
			sys.IsDefault = true
		}
		return sys, nil
	}
	sub, err := s.Get(udm.SubscriptionID)
	if err != nil {
		return nil, fmt.Errorf("get default subscription: %w", err)
	}
	// IsDefault is always false — no "default subscription" concept.
	return sub, nil
}

// isSystemSubscription reports whether the given subscription ID is the shared
// system subscription (is_system=1). Used to guard mutation entry points.
func (s *LLMSubscriptionService) isSystemSubscription(id string) bool {
	if id == "" {
		return false
	}
	conn := s.db.Conn()
	var isSystem int
	err := conn.QueryRow("SELECT is_system FROM user_llm_subscriptions WHERE id = ?", id).Scan(&isSystem)
	if err != nil {
		return false
	}
	return isSystem == 1
}

// GetSystemSubscription returns the shared system subscription, or nil if absent.
func (s *LLMSubscriptionService) GetSystemSubscription() (*LLMSubscription, error) {
	conn := s.db.Conn()
	row := conn.QueryRow(`
		SELECT id, sender_id, name, provider, base_url, api_key, model, enabled, max_context, max_output_tokens, thinking_mode, api_type, cached_models, created_at, updated_at, is_system
			FROM user_llm_subscriptions
			WHERE is_system = 1 LIMIT 1
		`)
	sub := &LLMSubscription{}
	encryptedAPIKey, err := scanSubscription(row, sub)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get system subscription: %w", err)
	}
	decryptAPIKey(sub, encryptedAPIKey)
	s.loadPerModelConfigs(sub)
	return sub, nil
}

// UpsertSystemSubscription creates or reconciles the shared system subscription
// from config/env at boot. Every boot overwrites credentials/config fields
// (reconcile policy); cached_models and per-model configs are preserved.
// caller provides the desired subscription fields (typically derived from cfg.LLM).
func (s *LLMSubscriptionService) UpsertSystemSubscription(sub *LLMSubscription) error {
	if sub == nil {
		return fmt.Errorf("nil system subscription")
	}
	sub.SenderID = SystemSenderID
	sub.IsSystem = true
	sub.Name = SystemSubscriptionName
	if sub.ID == "" {
		sub.ID = "system"
	}
	encryptedAPIKey := sub.APIKey
	if sub.APIKey != "" {
		encrypted, err := crypto.Encrypt(sub.APIKey)
		if err != nil {
			return fmt.Errorf("encrypt API key: %w", err)
		}
		encryptedAPIKey = encrypted
	}
	conn := s.db.Conn()
	now := time.Now()
	_, err := conn.Exec(`
		INSERT INTO user_llm_subscriptions (id, sender_id, name, provider, base_url, api_key, model, enabled, max_context, max_output_tokens, thinking_mode, api_type, is_system, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			sender_id = excluded.sender_id,
			name = excluded.name,
			provider = excluded.provider,
			base_url = excluded.base_url,
			api_key = excluded.api_key,
			model = excluded.model,
			max_context = excluded.max_context,
			max_output_tokens = excluded.max_output_tokens,
			thinking_mode = excluded.thinking_mode,
			api_type = excluded.api_type,
			is_system = 1,
			updated_at = excluded.updated_at
	`, sub.ID, sub.SenderID, sub.Name, sub.Provider, sub.BaseURL, encryptedAPIKey, sub.Model, sub.MaxContext, sub.MaxOutputTokens, sub.ThinkingMode, sub.APIType, now, now)
	if err != nil {
		return fmt.Errorf("upsert system subscription: %w", err)
	}
	return nil
}

// Get returns a subscription by ID.
func (s *LLMSubscriptionService) Get(id string) (*LLMSubscription, error) {
	conn := s.db.Conn()
	row := conn.QueryRow(`
		SELECT id, sender_id, name, provider, base_url, api_key, model, enabled, max_context, max_output_tokens, thinking_mode, api_type, cached_models, created_at, updated_at, is_system
			FROM user_llm_subscriptions
			WHERE id = ?
		`, id)

	sub := &LLMSubscription{}
	encryptedAPIKey, err := scanSubscription(row, sub)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get subscription: %w", err)
	}
	decryptAPIKey(sub, encryptedAPIKey)
	s.loadPerModelConfigs(sub)
	return sub, nil
}

// Add creates a new subscription. If isDefault is true, other subscriptions are unset as default.
func (s *LLMSubscriptionService) Add(sub *LLMSubscription) error {
	conn := s.db.Conn()

	encryptedAPIKey := sub.APIKey
	if sub.APIKey != "" {
		encrypted, err := crypto.Encrypt(sub.APIKey)
		if err != nil {
			return fmt.Errorf("encrypt API key: %w", err)
		}
		encryptedAPIKey = encrypted
	}

	if sub.ID == "" {
		sub.ID = fmt.Sprintf("sub_%s", newULID())
	}
	now := time.Now()
	sub.CreatedAt = now
	sub.UpdatedAt = now

	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	isSystem := 0
	if sub.IsSystem {
		isSystem = 1
	}
	_, err = tx.Exec(`
		INSERT INTO user_llm_subscriptions (id, sender_id, name, provider, base_url, api_key, model, enabled, max_context, max_output_tokens, thinking_mode, api_type, is_system, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?)
	`, sub.ID, sub.SenderID, sub.Name, sub.Provider, sub.BaseURL, encryptedAPIKey, sub.Model, sub.MaxContext, sub.MaxOutputTokens, sub.ThinkingMode, sub.APIType, isSystem, now, now)
	if err != nil {
		return fmt.Errorf("insert subscription: %w", err)
	}

	// Persist any per-model overrides to the subscription_models table (sole source since v42).
	for model, cfg := range sub.PerModelConfigs {
		if err := s.upsertModelTx(tx, sub.ID, model, cfg.MaxContext, cfg.MaxOutputTokens, "", cfg.APIType); err != nil {
			return fmt.Errorf("upsert per-model %s: %w", model, err)
		}
	}

	// No "default subscription" seeding — user_default_model is repurposed as
	// "last used model", written by SelectModel when the user picks a model.
	// Adding a subscription does NOT set a default.

	return tx.Commit()
}

// It ensures the subscription's active model is always included.
func (s *LLMSubscriptionService) UpdateCachedModels(subID string, models []string) error {
	sub, err := s.Get(subID)
	if err != nil || sub == nil {
		return fmt.Errorf("subscription %s not found: %w", subID, err)
	}
	models = ensureModel(models, sub.Model)
	data, err := json.Marshal(models)
	if err != nil {
		return fmt.Errorf("marshal cached models: %w", err)
	}
	_, err = s.db.Conn().Exec("UPDATE user_llm_subscriptions SET cached_models = ?, updated_at = datetime('now') WHERE id = ?",
		string(data), subID)
	return err
}

// ensureModel adds model to the list if not already present.
func ensureModel(models []string, model string) []string {
	if model == "" {
		return models
	}
	for _, m := range models {
		if m == model {
			return models
		}
	}
	return append(models, model)
}

// Update updates an existing subscription.
func (s *LLMSubscriptionService) Update(sub *LLMSubscription) error {
	if s.isSystemSubscription(sub.ID) {
		return fmt.Errorf("system subscription is read-only")
	}
	conn := s.db.Conn()

	encryptedAPIKey := sub.APIKey
	if sub.APIKey != "" {
		encrypted, err := crypto.Encrypt(sub.APIKey)
		if err != nil {
			return fmt.Errorf("encrypt API key: %w", err)
		}
		encryptedAPIKey = encrypted
	}

	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	now := time.Now()
	_, err = tx.Exec(`
		UPDATE user_llm_subscriptions SET
		name = ?, provider = ?, base_url = ?, api_key = ?, model = ?,
		max_context = ?, max_output_tokens = ?, thinking_mode = ?, api_type = ?,
		updated_at = ?
		WHERE id = ? AND sender_id = ?
	`, sub.Name, sub.Provider, sub.BaseURL, encryptedAPIKey, sub.Model, sub.MaxContext, sub.MaxOutputTokens, sub.ThinkingMode, sub.APIType, now, sub.ID, sub.SenderID)
	if err != nil {
		return fmt.Errorf("update subscription: %w", err)
	}

	// No "default subscription" sync — user_default_model is repurposed as
	// "last used model", written only by SelectModel.

	return tx.Commit()
}

// Remove deletes a subscription by ID.
func (s *LLMSubscriptionService) Remove(id string) error {
	if s.isSystemSubscription(id) {
		return fmt.Errorf("system subscription is read-only")
	}
	conn := s.db.Conn()
	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Get sender_id before deleting (for user_default_model cleanup).
	var senderID string
	_ = tx.QueryRow("SELECT sender_id FROM user_llm_subscriptions WHERE id = ?", id).Scan(&senderID)

	if _, err := tx.Exec("DELETE FROM user_llm_subscriptions WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete subscription: %w", err)
	}
	// Cascade: delete per-model config rows for this subscription.
	if _, err := tx.Exec("DELETE FROM subscription_models WHERE subscription_id = ?", id); err != nil {
		return fmt.Errorf("delete subscription_models: %w", err)
	}
	// Cascade: clear user_default_model if it points to the deleted subscription.
	if senderID != "" {
		if _, err := tx.Exec(
			"DELETE FROM user_default_model WHERE sender_id = ? AND subscription_id = ?",
			senderID, id,
		); err != nil {
			return fmt.Errorf("clear user_default_model: %w", err)
		}
	}
	return tx.Commit()
}

// SetDefault sets a subscription as the default for its user.
// Derived from user_default_model (is_default column dropped in v43).
func (s *LLMSubscriptionService) SetDefault(id string) error {
	conn := s.db.Conn()

	var senderID, model string
	err := conn.QueryRow("SELECT sender_id, model FROM user_llm_subscriptions WHERE id = ?", id).Scan(&senderID, &model)
	if err != nil {
		return fmt.Errorf("find subscription: %w", err)
	}

	_, err = conn.Exec(`
		INSERT INTO user_default_model (sender_id, subscription_id, model, updated_at)
		VALUES (?, ?, ?, datetime('now'))
		ON CONFLICT(sender_id) DO UPDATE SET subscription_id = excluded.subscription_id, model = excluded.model, updated_at = excluded.updated_at
	`, senderID, id, model)
	if err != nil {
		return fmt.Errorf("set default: %w", err)
	}
	return nil
}

// SetModel updates the model for a subscription.
func (s *LLMSubscriptionService) SetModel(id, model string) error {
	conn := s.db.Conn()
	_, err := conn.Exec("UPDATE user_llm_subscriptions SET model = ?, updated_at = datetime('now') WHERE id = ?", model, id)
	if err != nil {
		return fmt.Errorf("update subscription model: %w", err)
	}
	return nil
}

func (s *LLMSubscriptionService) Rename(id, name string) error {
	if s.isSystemSubscription(id) {
		return fmt.Errorf("system subscription is read-only")
	}
	conn := s.db.Conn()
	_, err := conn.Exec("UPDATE user_llm_subscriptions SET name = ?, updated_at = datetime('now') WHERE id = ?", name, id)
	if err != nil {
		return fmt.Errorf("rename subscription: %w", err)
	}
	return nil
}

// UpdatePerModelConfigs replaces all per-model token overrides for a subscription.
// Since v42 the subscription_models table is the sole source; this deletes existing
// rows and re-inserts the provided map. configs replaces existing rows entirely.
func (s *LLMSubscriptionService) UpdatePerModelConfigs(id string, configs map[string]PerModelConfig) error {
	conn := s.db.Conn()
	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM subscription_models WHERE subscription_id = ?", id); err != nil {
		return fmt.Errorf("clear subscription_models: %w", err)
	}
	for model, cfg := range configs {
		if err := s.upsertModelTx(tx, id, model, cfg.MaxContext, cfg.MaxOutputTokens, "", cfg.APIType); err != nil {
			return fmt.Errorf("upsert model %s: %w", model, err)
		}
	}
	return tx.Commit()
}

// GetPerModelMaxTokens returns the per-model max_output_tokens override for the given subscription and model.
// Returns 0 if no override is configured (caller should fall back to subscription-level default).
func (sub *LLMSubscription) GetPerModelMaxTokens(model string) int {
	if sub.PerModelConfigs == nil || model == "" {
		return 0
	}
	if cfg, ok := sub.PerModelConfigs[model]; ok {
		return cfg.MaxOutputTokens
	}
	return 0
}

// GetPerModelMaxContext returns the per-model max_context override for the given subscription and model.
// Returns 0 if no override is configured (caller should fall back to subscription-level default).
func (sub *LLMSubscription) GetPerModelMaxContext(model string) int {
	if sub.PerModelConfigs == nil || model == "" {
		return 0
	}
	if cfg, ok := sub.PerModelConfigs[model]; ok {
		return cfg.MaxContext
	}
	return 0
}

// GetPerModelAPIType returns the per-model API type override.
// Returns "" if no override is set (use subscription default).
func (sub *LLMSubscription) GetPerModelAPIType(model string) string {
	if sub.PerModelConfigs == nil || model == "" {
		return ""
	}
	if cfg, ok := sub.PerModelConfigs[model]; ok {
		return cfg.APIType
	}
	return ""
}

// ─── SubscriptionModel CRUD ─────────────────────────────

// scanSubscriptionModel scans a subscription_models row into a SubscriptionModel.
func scanSubscriptionModel(scanner interface{ Scan(...any) error }, m *SubscriptionModel) error {
	var createdAt, updatedAt string
	var enabled int
	err := scanner.Scan(&m.ID, &m.SubscriptionID, &m.Model, &m.MaxContext,
		&m.MaxOutputTokens, &m.ThinkingMode, &m.APIType, &createdAt, &updatedAt, &enabled)
	if err != nil {
		return err
	}
	m.Enabled = enabled == 1
	m.CreatedAt = parseSQLiteTime(createdAt)
	m.UpdatedAt = parseSQLiteTime(updatedAt)
	return nil
}

// GetModels returns all models for a subscription.
func (s *LLMSubscriptionService) GetModels(subID string) ([]*SubscriptionModel, error) {
	conn := s.db.Conn()
	rows, err := conn.Query(`
		SELECT id, subscription_id, model, max_context, max_output_tokens, thinking_mode, api_type, created_at, updated_at, enabled
		FROM subscription_models WHERE subscription_id = ? ORDER BY created_at ASC
	`, subID)
	if err != nil {
		return nil, fmt.Errorf("get models: %w", err)
	}
	defer rows.Close()
	var models []*SubscriptionModel
	for rows.Next() {
		m := &SubscriptionModel{}
		if err := scanSubscriptionModel(rows, m); err != nil {
			return nil, fmt.Errorf("scan model: %w", err)
		}
		models = append(models, m)
	}
	return models, rows.Err()
}

// GetModel returns a model row by subscription ID and model name.
func (s *LLMSubscriptionService) GetModel(subID, model string) (*SubscriptionModel, error) {
	conn := s.db.Conn()
	m := &SubscriptionModel{}
	err := scanSubscriptionModel(
		conn.QueryRow(`
			SELECT id, subscription_id, model, max_context, max_output_tokens, thinking_mode, api_type, created_at, updated_at, enabled
			FROM subscription_models WHERE subscription_id = ? AND model = ?
		`, subID, model),
		m,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get model: %w", err)
	}
	return m, nil
}

// UpsertModel inserts or updates a model row in subscription_models.
func (s *LLMSubscriptionService) UpsertModel(subID, model string, maxCtx, maxOut int, thinking, apiType string) error {
	conn := s.db.Conn()
	return s.upsertModelTx(conn, subID, model, maxCtx, maxOut, thinking, apiType)
}

// upsertModelTx is the tx-aware core of UpsertModel.
func (s *LLMSubscriptionService) upsertModelTx(tx interface {
	Exec(query string, args ...any) (sql.Result, error)
}, subID, model string, maxCtx, maxOut int, thinking, apiType string) error {
	_, err := tx.Exec(`
		INSERT INTO subscription_models (id, subscription_id, model, max_context, max_output_tokens, thinking_mode, api_type)
		VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?)
		ON CONFLICT(subscription_id, model) DO UPDATE SET
			max_context = excluded.max_context,
			max_output_tokens = excluded.max_output_tokens,
			thinking_mode = excluded.thinking_mode,
			api_type = excluded.api_type,
			updated_at = datetime('now')
	`, subID, model, maxCtx, maxOut, thinking, apiType)
	if err != nil {
		return fmt.Errorf("upsert model: %w", err)
	}
	return nil
}

// SetSubscriptionEnabled toggles a subscription's enabled flag (v40). A disabled
// subscription stops contributing models to the picker (ListAllModelsForUser and
// ResolveSubscriptionForModel skip it) without deleting its credentials/models.
func (s *LLMSubscriptionService) SetSubscriptionEnabled(subID string, enabled bool) error {
	if s.isSystemSubscription(subID) {
		return fmt.Errorf("system subscription is read-only")
	}
	conn := s.db.Conn()
	v := 0
	if enabled {
		v = 1
	}
	res, err := conn.Exec(`
		UPDATE user_llm_subscriptions SET enabled = ?, updated_at = datetime('now')
		WHERE id = ?
	`, v, subID)
	if err != nil {
		return fmt.Errorf("set subscription enabled: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set subscription enabled: no subscription %s", subID)
	}
	return nil
}

// SetModelEnabled toggles a model's enabled flag (v38). Disabling a model removes
// it from the selectable catalog without deleting its per-model config.
func (s *LLMSubscriptionService) SetModelEnabled(subID, model string, enabled bool) error {
	conn := s.db.Conn()
	v := 0
	if enabled {
		v = 1
	}
	res, err := conn.Exec(`
		UPDATE subscription_models SET enabled = ?, updated_at = datetime('now')
		WHERE subscription_id = ? AND model = ?
	`, v, subID, model)
	if err != nil {
		return fmt.Errorf("set model enabled: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set model enabled: no row for subscription %s model %s", subID, model)
	}
	return nil
}

// ─── user_default_model (v38) ──────────────────────────

// UserDefaultModel holds a user's default (subscription, model) used to resolve
// the LLM for new sessions. Replaces the implicit "current model" semantics of
// user_llm_subscriptions.model.
type UserDefaultModel struct {
	SenderID       string
	SubscriptionID string
	Model          string
	UpdatedAt      time.Time
}

// GetUserDefaultModel returns the user's default model selection, or nil if unset.
func (s *LLMSubscriptionService) GetUserDefaultModel(senderID string) (*UserDefaultModel, error) {
	conn := s.db.Conn()
	m := &UserDefaultModel{}
	var updatedAt string
	err := conn.QueryRow(`
		SELECT sender_id, subscription_id, model, updated_at
		FROM user_default_model WHERE sender_id = ?
	`, senderID).Scan(&m.SenderID, &m.SubscriptionID, &m.Model, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user default model: %w", err)
	}
	m.UpdatedAt = parseSQLiteTime(updatedAt)
	return m, nil
}

// SetUserDefaultModel sets the user's default (subscription, model). An empty
// model means "use the subscription's default model" and is allowed only when the
// caller intends to defer model selection.
func (s *LLMSubscriptionService) SetUserDefaultModel(senderID, subID, model string) error {
	conn := s.db.Conn()
	_, err := conn.Exec(`
		INSERT INTO user_default_model (sender_id, subscription_id, model, updated_at)
		VALUES (?, ?, ?, datetime('now'))
		ON CONFLICT(sender_id) DO UPDATE SET
			subscription_id = excluded.subscription_id,
			model = excluded.model,
			updated_at = datetime('now')
	`, senderID, subID, model)
	if err != nil {
		return fmt.Errorf("set user default model: %w", err)
	}
	return nil
}

// ClearUserDefaultModel removes the user's default model selection.
func (s *LLMSubscriptionService) ClearUserDefaultModel(senderID string) error {
	conn := s.db.Conn()
	_, err := conn.Exec(`DELETE FROM user_default_model WHERE sender_id = ?`, senderID)
	if err != nil {
		return fmt.Errorf("clear user default model: %w", err)
	}
	return nil
}

// newULID generates a new ULID string.
func newULID() string {
	b := make([]byte, 16)
	// time component (6 bytes, ms since epoch)
	now := time.Now()
	ms := uint64(now.UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	// random component (10 bytes) — cryptographically secure
	if _, err := rand.Read(b[6:16]); err != nil {
		// This should never happen with /dev/urandom, but fallback to timestamp-based
		for i := 6; i < 16; i++ {
			b[i] = byte(now.UnixNano() >> (i * 7))
		}
	}
	return fmt.Sprintf("%x", b)
}

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
func scanSubscription(scanner interface{ Scan(...any) error }, sub *LLMSubscription) (string, int, error) {
	var encryptedAPIKey string
	var isDefault int
	var createdAt, updatedAt string
	var cachedModelsJSON string
	var perModelConfigsJSON string
	err := scanner.Scan(&sub.ID, &sub.SenderID, &sub.Name, &sub.Provider, &sub.BaseURL,
		&encryptedAPIKey, &sub.Model, &isDefault, &sub.MaxContext, &sub.MaxOutputTokens, &sub.ThinkingMode, &sub.APIType,
		&cachedModelsJSON, &perModelConfigsJSON, &createdAt, &updatedAt)
	if err != nil {
		return "", 0, err
	}
	sub.IsDefault = isDefault == 1
	sub.CreatedAt = parseSQLiteTime(createdAt)
	sub.UpdatedAt = parseSQLiteTime(updatedAt)
	if cachedModelsJSON != "" {
		_ = json.Unmarshal([]byte(cachedModelsJSON), &sub.CachedModels)
	}
	if perModelConfigsJSON != "" && perModelConfigsJSON != "{}" {
		_ = json.Unmarshal([]byte(perModelConfigsJSON), &sub.PerModelConfigs)
	}
	return encryptedAPIKey, isDefault, nil
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
		SELECT id, sender_id, name, provider, base_url, api_key, model, is_default, max_context, max_output_tokens, thinking_mode, api_type, cached_models, per_model_configs, created_at, updated_at
			FROM user_llm_subscriptions
			ORDER BY created_at ASC
		`)
	if err != nil {
		return nil, fmt.Errorf("list all subscriptions: %w", err)
	}
	defer rows.Close()

	var subs []*LLMSubscription
	for rows.Next() {
		sub := &LLMSubscription{}
		encryptedAPIKey, _, err := scanSubscription(rows, sub)
		if err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		decryptAPIKey(sub, encryptedAPIKey)
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

// List returns all subscriptions for a user, ordered by creation time.
func (s *LLMSubscriptionService) List(senderID string) ([]*LLMSubscription, error) {
	conn := s.db.Conn()
	rows, err := conn.Query(`
			SELECT id, sender_id, name, provider, base_url, api_key, model, is_default, max_context, max_output_tokens, thinking_mode, api_type, cached_models, per_model_configs, created_at, updated_at
				FROM user_llm_subscriptions
				WHERE sender_id = ?
				ORDER BY created_at ASC
			`, senderID)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	defer rows.Close()

	var subs []*LLMSubscription
	for rows.Next() {
		sub := &LLMSubscription{}
		encryptedAPIKey, _, err := scanSubscription(rows, sub)
		if err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		decryptAPIKey(sub, encryptedAPIKey)
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

// GetDefault returns the default (active) subscription for a user.
func (s *LLMSubscriptionService) GetDefault(senderID string) (*LLMSubscription, error) {
	conn := s.db.Conn()
	row := conn.QueryRow(`
		SELECT id, sender_id, name, provider, base_url, api_key, model, is_default, max_context, max_output_tokens, thinking_mode, api_type, cached_models, per_model_configs, created_at, updated_at
			FROM user_llm_subscriptions
			WHERE sender_id = ? AND is_default = 1
			LIMIT 1
		`, senderID)

	sub := &LLMSubscription{}
	encryptedAPIKey, _, err := scanSubscription(row, sub)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get default subscription: %w", err)
	}
	decryptAPIKey(sub, encryptedAPIKey)
	return sub, nil
}

// Get returns a subscription by ID.
func (s *LLMSubscriptionService) Get(id string) (*LLMSubscription, error) {
	conn := s.db.Conn()
	row := conn.QueryRow(`
		SELECT id, sender_id, name, provider, base_url, api_key, model, is_default, max_context, max_output_tokens, thinking_mode, api_type, cached_models, per_model_configs, created_at, updated_at
			FROM user_llm_subscriptions
			WHERE id = ?
		`, id)

	sub := &LLMSubscription{}
	encryptedAPIKey, _, err := scanSubscription(row, sub)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get subscription: %w", err)
	}
	decryptAPIKey(sub, encryptedAPIKey)
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

	if sub.IsDefault {
		if _, err := tx.Exec("UPDATE user_llm_subscriptions SET is_default = 0 WHERE sender_id = ?", sub.SenderID); err != nil {
			return fmt.Errorf("clear default: %w", err)
		}
	}

	isDefault := 0
	if sub.IsDefault {
		isDefault = 1
	}
	perModelConfigsJSON := "{}"
	if len(sub.PerModelConfigs) > 0 {
		if data, err := json.Marshal(sub.PerModelConfigs); err == nil {
			perModelConfigsJSON = string(data)
		}
	}
	_, err = tx.Exec(`
		INSERT INTO user_llm_subscriptions (id, sender_id, name, provider, base_url, api_key, model, is_default, max_context, max_output_tokens, thinking_mode, api_type, per_model_configs, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, sub.ID, sub.SenderID, sub.Name, sub.Provider, sub.BaseURL, encryptedAPIKey, sub.Model, isDefault, sub.MaxContext, sub.MaxOutputTokens, sub.ThinkingMode, sub.APIType, perModelConfigsJSON, now, now)
	if err != nil {
		return fmt.Errorf("insert subscription: %w", err)
	}

	return tx.Commit()
}

// UpdateCachedModels persists the model list cache for a subscription.
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

	if sub.IsDefault {
		if _, err := tx.Exec("UPDATE user_llm_subscriptions SET is_default = 0 WHERE sender_id = ? AND id != ?", sub.SenderID, sub.ID); err != nil {
			return fmt.Errorf("clear default: %w", err)
		}
	}

	now := time.Now()
	isDefault := 0
	if sub.IsDefault {
		isDefault = 1
	}
	perModelConfigsJSON := "{}"
	if len(sub.PerModelConfigs) > 0 {
		if data, err := json.Marshal(sub.PerModelConfigs); err == nil {
			perModelConfigsJSON = string(data)
		}
	}
	_, err = tx.Exec(`
		UPDATE user_llm_subscriptions SET
		name = ?, provider = ?, base_url = ?, api_key = ?, model = ?,
		max_context = ?, max_output_tokens = ?, thinking_mode = ?, api_type = ?,
		per_model_configs = ?,
		is_default = ?, updated_at = ?
		WHERE id = ? AND sender_id = ?
	`, sub.Name, sub.Provider, sub.BaseURL, encryptedAPIKey, sub.Model, sub.MaxContext, sub.MaxOutputTokens, sub.ThinkingMode, sub.APIType, perModelConfigsJSON, isDefault, now, sub.ID, sub.SenderID)
	if err != nil {
		return fmt.Errorf("update subscription: %w", err)
	}

	return tx.Commit()
}

// Remove deletes a subscription by ID.
func (s *LLMSubscriptionService) Remove(id string) error {
	conn := s.db.Conn()
	_, err := conn.Exec("DELETE FROM user_llm_subscriptions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete subscription: %w", err)
	}
	return nil
}

// SetDefault sets a subscription as the default for its user.
func (s *LLMSubscriptionService) SetDefault(id string) error {
	conn := s.db.Conn()

	// First find the sender_id
	var senderID string
	err := conn.QueryRow("SELECT sender_id FROM user_llm_subscriptions WHERE id = ?", id).Scan(&senderID)
	if err != nil {
		return fmt.Errorf("find subscription: %w", err)
	}

	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec("UPDATE user_llm_subscriptions SET is_default = 0 WHERE sender_id = ?", senderID); err != nil {
		return fmt.Errorf("clear default: %w", err)
	}
	if _, err := tx.Exec("UPDATE user_llm_subscriptions SET is_default = 1 WHERE id = ?", id); err != nil {
		return fmt.Errorf("set default: %w", err)
	}

	return tx.Commit()
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
	conn := s.db.Conn()
	_, err := conn.Exec("UPDATE user_llm_subscriptions SET name = ?, updated_at = datetime('now') WHERE id = ?", name, id)
	if err != nil {
		return fmt.Errorf("rename subscription: %w", err)
	}
	return nil
}

// UpdatePerModelConfigs updates the per-model token overrides for a subscription.
// configs is the full map to persist (replaces existing entirely).
func (s *LLMSubscriptionService) UpdatePerModelConfigs(id string, configs map[string]PerModelConfig) error {
	configsJSON := "{}"
	if len(configs) > 0 {
		data, err := json.Marshal(configs)
		if err != nil {
			return fmt.Errorf("marshal per_model_configs: %w", err)
		}
		configsJSON = string(data)
	}
	conn := s.db.Conn()
	_, err := conn.Exec("UPDATE user_llm_subscriptions SET per_model_configs = ?, updated_at = datetime('now') WHERE id = ?", configsJSON, id)
	if err != nil {
		return fmt.Errorf("update per_model_configs: %w", err)
	}
	return nil
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
	err := scanner.Scan(&m.ID, &m.SubscriptionID, &m.Model, &m.MaxContext,
		&m.MaxOutputTokens, &m.ThinkingMode, &m.APIType, &createdAt, &updatedAt)
	if err != nil {
		return err
	}
	m.CreatedAt = parseSQLiteTime(createdAt)
	m.UpdatedAt = parseSQLiteTime(updatedAt)
	return nil
}

// GetModels returns all models for a subscription.
func (s *LLMSubscriptionService) GetModels(subID string) ([]*SubscriptionModel, error) {
	conn := s.db.Conn()
	rows, err := conn.Query(`
		SELECT id, subscription_id, model, max_context, max_output_tokens, thinking_mode, api_type, created_at, updated_at
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
			SELECT id, subscription_id, model, max_context, max_output_tokens, thinking_mode, api_type, created_at, updated_at
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
	_, err := conn.Exec(`
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

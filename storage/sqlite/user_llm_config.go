package sqlite

import (
	"database/sql"
	"fmt"
	"time"

	"xbot/crypto"
	log "xbot/logger"
)

// UserLLMConfig holds per-user LLM configuration
type UserLLMConfig struct {
	ID              string         // subscription ID (for precise UPDATE targeting)
	SenderID        string         // user ID
	Provider        string         // LLM provider: "openai", "deepseek", "anthropic", etc.
	BaseURL         string         // API Base URL
	APIKey          string         // API Key
	Model           string         // default model
	MaxContext      int            // max context tokens (0 = unlimited)
	MaxOutputTokens int            // max output tokens (0 = default 8192)
	ThinkingMode    string         // thinking mode: "" (auto), "enabled", "disabled"
	OnModelsLoaded  func([]string) // callback after models loaded from API
	CreatedAt       time.Time      // creation time
	UpdatedAt       time.Time      // last update time
}

// UserLLMConfigService manages per-user LLM configurations
type UserLLMConfigService struct {
	db *DB
}

// NewUserLLMConfigService creates a new UserLLMConfigService
func NewUserLLMConfigService(db *DB) *UserLLMConfigService {
	return &UserLLMConfigService{db: db}
}

// GetConfig retrieves the user's LLM configuration
func (s *UserLLMConfigService) GetConfig(senderID string) (*UserLLMConfig, error) {
	conn := s.db.Conn()

	var cfg UserLLMConfig
	var createdAt, updatedAt string
	err := conn.QueryRow(`
				SELECT id, sender_id, provider, base_url, api_key, model, max_context, max_output_tokens, thinking_mode, created_at, updated_at
				FROM user_llm_subscriptions
				WHERE sender_id = ? AND is_default = 1
				LIMIT 1
			`, senderID).Scan(
		&cfg.ID, &cfg.SenderID, &cfg.Provider, &cfg.BaseURL, &cfg.APIKey, &cfg.Model,
		&cfg.MaxContext, &cfg.MaxOutputTokens, &cfg.ThinkingMode,
		&createdAt, &updatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query user llm config: %w", err)
	}

	cfg.CreatedAt = parseSQLiteTime(createdAt)
	cfg.UpdatedAt = parseSQLiteTime(updatedAt)

	// Decrypt API key
	if cfg.APIKey != "" {
		decrypted, err := crypto.Decrypt(cfg.APIKey)
		if err != nil {
			log.WithError(err).WithField("sender_id", cfg.SenderID).Error("failed to decrypt API key")
			return nil, fmt.Errorf("decrypt API key: %w", err)
		}
		cfg.APIKey = decrypted
	}

	return &cfg, nil
}

// SetConfig sets the user's LLM configuration (writes to user_llm_subscriptions)
func (s *UserLLMConfigService) SetConfig(cfg *UserLLMConfig) error {
	conn := s.db.Conn()

	// Encrypt API key before storage
	encryptedAPIKey := cfg.APIKey
	if cfg.APIKey != "" {
		encrypted, err := crypto.Encrypt(cfg.APIKey)
		if err != nil {
			log.WithError(err).WithField("sender_id", cfg.SenderID).Error("failed to encrypt API key")
			return fmt.Errorf("encrypt API key: %w", err)
		}
		encryptedAPIKey = encrypted
	}

	now := time.Now()

	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Clear default flag for this user
	tx.Exec("UPDATE user_llm_subscriptions SET is_default = 0 WHERE sender_id = ?", cfg.SenderID)

	if cfg.ID != "" {
		// Update existing subscription by ID (precise match, avoids overwriting
		// same-provider subscriptions when user has multiple with the same provider).
		// Preserve existing name — do NOT derive it from provider (was the source of
		// bug where "cjw" got overwritten to "openai" on every startup).
		_, err = tx.Exec(`
		UPDATE user_llm_subscriptions SET
		provider = ?, base_url = ?, api_key = ?, model = ?,
		max_context = ?, max_output_tokens = ?, thinking_mode = ?,
		is_default = 1, updated_at = ?
		WHERE id = ? AND sender_id = ?
		`, cfg.Provider, cfg.BaseURL, encryptedAPIKey, cfg.Model,
			cfg.MaxContext, cfg.MaxOutputTokens, cfg.ThinkingMode,
			now, cfg.ID, cfg.SenderID)
		if err != nil {
			return fmt.Errorf("update subscription by id: %w", err)
		}
	} else {
		// Legacy path: no ID available (e.g. /set-llm command), match by sender+provider.
		// Preserve existing name for UPDATE; for INSERT derive from provider.
		result, err := tx.Exec(`
		UPDATE user_llm_subscriptions SET
		provider = ?, base_url = ?, api_key = ?, model = ?,
		max_context = ?, max_output_tokens = ?, thinking_mode = ?,
		is_default = 1, updated_at = ?
		WHERE sender_id = ? AND provider = ?
		`, cfg.Provider, cfg.BaseURL, encryptedAPIKey, cfg.Model,
			cfg.MaxContext, cfg.MaxOutputTokens, cfg.ThinkingMode,
			now, cfg.SenderID, cfg.Provider)
		if err != nil {
			return fmt.Errorf("update subscription: %w", err)
		}

		rowsAffected, _ := result.RowsAffected()
		if rowsAffected == 0 {
			subID := fmt.Sprintf("sub_%x", now.UnixNano())
			// New subscription: derive name from provider (only for INSERT, not UPDATE)
			subName := cfg.Provider
			if subName == "" {
				subName = "openai"
			}
			_, err = tx.Exec(`
			INSERT INTO user_llm_subscriptions (id, sender_id, name, provider, base_url, api_key, model, is_default, max_context, max_output_tokens, thinking_mode, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)
		`, subID, cfg.SenderID, subName, cfg.Provider, cfg.BaseURL, encryptedAPIKey, cfg.Model, cfg.MaxContext, cfg.MaxOutputTokens, cfg.ThinkingMode, now, now)
			if err != nil {
				return fmt.Errorf("insert subscription: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	log.WithFields(log.Fields{
		"sender_id": cfg.SenderID,
		"provider":  cfg.Provider,
		"model":     cfg.Model,
	}).Debug("User LLM config saved")

	return nil
}

// DeleteConfig deletes the user's LLM configuration
func (s *UserLLMConfigService) DeleteConfig(senderID string) error {
	conn := s.db.Conn()
	_, err := conn.Exec("DELETE FROM user_llm_subscriptions WHERE sender_id = ?", senderID)
	if err != nil {
		return fmt.Errorf("delete user llm config: %w", err)
	}
	log.WithField("sender_id", senderID).Info("User LLM config deleted")
	return nil
}

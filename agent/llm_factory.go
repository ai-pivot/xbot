package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"xbot/config"
	"xbot/llm"
	log "xbot/logger"
	"xbot/storage/sqlite"
)

// LLMFactory manages creation and caching of user-custom LLM clients
type LLMFactory struct {
	configSvc           *sqlite.UserLLMConfigService
	subscriptionSvc     *sqlite.LLMSubscriptionService     // Multi-subscription management (DB-backed)
	configSubsFn        func() []config.SubscriptionConfig // CLI config.json subscriptions (non-DB)
	settingsSvc         *SettingsService                   // For reading/writing user concurrency configuration
	defaultLLM          llm.LLM
	defaultModel        string
	defaultThinkingMode string
	tierModels          config.LLMConfig
	retryConfig         llm.RetryConfig // For wrapping bare clients created by createClient

	// LLMSemaphoreManager manages per-tenant LLM concurrency semaphore
	llmSemManager *llm.LLMSemaphoreManager

	// Cache user's LLM clients
	mu              sync.RWMutex
	clients         map[string]llm.LLM // senderID -> LLM client
	models          map[string]string  // senderID -> model name
	maxContexts     map[string]int     // senderID -> max_context tokens
	maxOutputTokens map[string]int     // senderID -> max_output_tokens
	thinkingModes   map[string]string  // senderID -> thinking_mode

	// hasCustomLLMCache Cache whether user has custom LLM configuration (avoid frequent DB queries)
	// Uses sync.Map to ensure concurrency safety
	hasCustomLLMCache sync.Map
}

// NewLLMFactory creates LLM factory
func NewLLMFactory(configSvc *sqlite.UserLLMConfigService, defaultLLM llm.LLM, defaultModel string) *LLMFactory {
	return &LLMFactory{
		configSvc:       configSvc,
		defaultLLM:      defaultLLM,
		defaultModel:    defaultModel,
		clients:         make(map[string]llm.LLM),
		models:          make(map[string]string),
		maxContexts:     make(map[string]int),
		maxOutputTokens: make(map[string]int),
		thinkingModes:   make(map[string]string),
		// hasCustomLLMCache uses zero-value sync.Map, no initialization needed
	}
}

// SetModelTiers updates the configured tier-to-model mappings used by SubAgent model resolution.
func (f *LLMFactory) SetModelTiers(cfg config.LLMConfig) {
	f.mu.Lock()
	f.tierModels = cfg
	f.mu.Unlock()
}

// SetRetryConfig sets the retry configuration used to wrap LLM clients.
// It wraps both the defaultLLM and all future createClient results.
func (f *LLMFactory) SetRetryConfig(cfg llm.RetryConfig) {
	f.mu.Lock()
	f.retryConfig = cfg
	// Wrap defaultLLM if not already wrapped (ensures users without
	// custom subscriptions still get 429/5xx retry).
	if cfg.Attempts > 0 {
		if _, ok := f.defaultLLM.(*llm.RetryLLM); !ok {
			f.defaultLLM = llm.NewRetryLLM(f.defaultLLM, cfg)
		}
	}
	f.mu.Unlock()
}

// GetLLM gets user's LLM client, returns default client if no custom configuration
// Returns: (LLM client, model name, maxContext, thinkingMode)
//
// Lookup priority:
// GetLLM returns the LLM client for the given user. Lookup order:
//  1. In-memory cache (from a previous GetLLM/SwitchSubscription call)
//  2. subscriptionSvc (user_llm_subscriptions table, default subscription)
//  3. Global default LLM (from config/startup)
func (f *LLMFactory) GetLLM(senderID string) (llm.LLM, string, int, string) {
	// Check cache first
	f.mu.RLock()
	if client, ok := f.clients[senderID]; ok {
		model := f.models[senderID]
		maxCtx := f.maxContexts[senderID]
		thinkingMode := f.thinkingModes[senderID]
		f.mu.RUnlock()
		return client, model, maxCtx, thinkingMode
	}
	f.mu.RUnlock()

	// Load from subscription service (single source of truth for per-user LLM config)
	if f.subscriptionSvc != nil {
		sub, err := f.subscriptionSvc.GetDefault(senderID)
		if err == nil && sub != nil && sub.BaseURL != "" && sub.APIKey != "" {
			// Diagnostic: detect masked keys that would cause API auth failures
			if strings.HasSuffix(sub.APIKey, "****") && len(sub.APIKey) <= 20 {
				log.WithFields(log.Fields{
					"sender_id": senderID,
					"sub_id":    sub.ID,
					"base_url":  sub.BaseURL,
					"api_key":   sub.APIKey,
					"provider":  sub.Provider,
				}).Error("[LLMFactory] GetLLM: subscription has masked API key — real key was lost!")
			}
			client := f.createClientFromSub(sub, sub.Model)
			if client != nil {
				model := sub.Model
				if model == "" {
					model = f.defaultModel
				}
				f.mu.Lock()
				f.clients[senderID] = client
				f.models[senderID] = model
				f.maxContexts[senderID] = sub.MaxContext
				f.maxOutputTokens[senderID] = sub.MaxOutputTokens
				f.thinkingModes[senderID] = sub.ThinkingMode
				f.mu.Unlock()
				return client, model, sub.MaxContext, sub.ThinkingMode
			}
		}
	}

	// Fallback: global default LLM
	return f.defaultLLM, f.defaultModel, 0, f.defaultThinkingMode
}

// chatKey returns the per-chat cache key used to isolate LLM clients between
// different CLI windows (each with a unique chatID/working-directory).
func chatKey(senderID, chatID string) string {
	return senderID + ":" + chatID
}

// GetLLMForChat returns the LLM client for a specific chat session.
// It first checks the per-chat cache (keyed by senderID:chatID), then falls
// back to GetLLM(senderID) which checks the user-level cache and DB.
// This ensures each CLI window can switch subscriptions independently.
func (f *LLMFactory) GetLLMForChat(senderID, chatID string) (llm.LLM, string, int, string) {
	if chatID == "" {
		return f.GetLLM(senderID)
	}
	key := chatKey(senderID, chatID)
	f.mu.RLock()
	if client, ok := f.clients[key]; ok {
		model := f.models[key]
		maxCtx := f.maxContexts[key]
		thinkingMode := f.thinkingModes[key]
		f.mu.RUnlock()
		return client, model, maxCtx, thinkingMode
	}
	f.mu.RUnlock()
	// No per-chat override — fall back to user-level resolution
	return f.GetLLM(senderID)
}

// HasCustomLLM checks if user has custom LLM configuration
func (f *LLMFactory) HasCustomLLM(senderID string) bool {
	// Check cache first
	if val, ok := f.hasCustomLLMCache.Load(senderID); ok {
		if b, ok := val.(bool); ok {
			return b
		}
		return false
	}

	// Then check client cache
	f.mu.RLock()
	if _, ok := f.clients[senderID]; ok {
		f.mu.RUnlock()
		f.hasCustomLLMCache.Store(senderID, true)
		return true
	}
	f.mu.RUnlock()

	// Check old single configuration from database
	if f.configSvc != nil {
		cfg, err := f.configSvc.GetConfig(senderID)
		if err == nil && cfg != nil {
			hasCustom := cfg.BaseURL != "" && cfg.APIKey != ""
			if hasCustom {
				f.hasCustomLLMCache.Store(senderID, true)
				return true
			}
		}
	}
	// Then check multi-subscription system
	if f.subscriptionSvc != nil {
		sub, err := f.subscriptionSvc.GetDefault(senderID)
		if err == nil && sub != nil && sub.BaseURL != "" && sub.APIKey != "" {
			f.hasCustomLLMCache.Store(senderID, true)
			return true
		}
	}
	f.hasCustomLLMCache.Store(senderID, false)
	return false
}

// InvalidateCustomLLMCache invalidates the specified user's custom LLM cache
func (f *LLMFactory) InvalidateCustomLLMCache(senderID string) {
	f.hasCustomLLMCache.Delete(senderID)
}

// SetSubscriptionSvc sets the subscription service (optional, for multi-subscription support).
func (f *LLMFactory) SetSubscriptionSvc(svc *sqlite.LLMSubscriptionService) {
	f.subscriptionSvc = svc
}

// SetConfigSubs sets a function that returns CLI config.json subscriptions (used when DB subscriptions are empty).
// Using a function instead of a slice ensures we always read the latest subscriptions after Add/Remove/Update.
func (f *LLMFactory) SetConfigSubs(fn func() []config.SubscriptionConfig) {
	f.mu.Lock()
	f.configSubsFn = fn
	f.mu.Unlock()
}

// GetSubscriptionSvc returns the subscription service.
func (f *LLMFactory) GetSubscriptionSvc() *sqlite.LLMSubscriptionService {
	return f.subscriptionSvc
}

// GetDefaultModel returns the default model name.
func (f *LLMFactory) GetDefaultModel() string {
	return f.defaultModel
}

// SwitchSubscription switches a user's active LLM to the specified subscription.
// It creates a new LLM client from the subscription config and caches it under
// both the user-level key (senderID) and the per-chat key (senderID:chatID).
// The per-chat key ensures other CLI windows keep their own LLM client.
func (f *LLMFactory) SwitchSubscription(senderID string, sub *sqlite.LLMSubscription, chatID string) error {
	cfg := &sqlite.UserLLMConfig{
		Provider:        sub.Provider,
		BaseURL:         sub.BaseURL,
		APIKey:          sub.APIKey,
		Model:           sub.Model,
		MaxContext:      sub.MaxContext,
		MaxOutputTokens: sub.MaxOutputTokens,
		ThinkingMode:    sub.ThinkingMode,
	}
	client, model := f.createClient(cfg)
	if client == nil {
		log.WithFields(log.Fields{
			"sender_id": senderID,
			"sub_id":    sub.ID,
			"provider":  sub.Provider,
			"base_url":  sub.BaseURL,
			"api_key":   sub.APIKey != "",
		}).Error("[LLM] SwitchSubscription: failed to create client")
		return fmt.Errorf("failed to create LLM client for subscription %s", sub.ID)
	}

	f.mu.Lock()
	// Always update user-level cache so GetLLM(senderID) picks it up
	f.clients[senderID] = client
	f.models[senderID] = model
	f.maxContexts[senderID] = sub.MaxContext
	f.maxOutputTokens[senderID] = sub.MaxOutputTokens
	f.thinkingModes[senderID] = sub.ThinkingMode
	// If chatID provided, also cache under per-chat key for chat isolation
	if chatID != "" {
		chatK := chatKey(senderID, chatID)
		f.clients[chatK] = client
		f.models[chatK] = model
		f.maxContexts[chatK] = sub.MaxContext
		f.maxOutputTokens[chatK] = sub.MaxOutputTokens
		f.thinkingModes[chatK] = sub.ThinkingMode
	}
	// For the CLI identity, also update defaultLLM so that GetLLM fallback
	// (when cache miss and no DB default) returns the currently active
	// subscription's client, not the stale startup client.
	if senderID == "cli_user" {
		f.defaultLLM = client
		f.defaultModel = model
	}
	f.mu.Unlock()

	log.WithFields(log.Fields{
		"sender_id":         senderID,
		"chat_id":           chatID,
		"sub_id":            sub.ID,
		"sub_name":          sub.Name,
		"model":             model,
		"max_output_tokens": sub.MaxOutputTokens,
		"thinking_mode":     sub.ThinkingMode,
	}).Debug("[LLM] SwitchSubscription: client created and cached")

	f.hasCustomLLMCache.Store(senderID, true)
	return nil
}

// SwitchModel switches a user's active model without changing the subscription/LLM client.
// Persists to DB subscription via the RPC handler. This method updates in-memory cache
// and clears per-chat caches so GetLLMForChat returns the new model.
func (f *LLMFactory) SwitchModel(senderID, model string) {
	f.mu.Lock()
	// Clear per-chat caches so GetLLMForChat falls back to user-level cache
	prefix := senderID + ":"
	for k := range f.clients {
		if strings.HasPrefix(k, prefix) {
			delete(f.clients, k)
			delete(f.models, k)
			delete(f.maxContexts, k)
			delete(f.maxOutputTokens, k)
			delete(f.thinkingModes, k)
		}
	}
	f.models[senderID] = model
	f.mu.Unlock()
}

// SetUserMaxOutputTokens updates the max_output_tokens cache for a user.
// This is a lightweight update that doesn't require LLMConfig.
func (f *LLMFactory) SetUserMaxOutputTokens(senderID string, n int) {
	f.mu.Lock()
	f.maxOutputTokens[senderID] = n
	f.mu.Unlock()
}

// SetUserThinkingMode updates the thinking_mode cache for a user.
func (f *LLMFactory) SetUserThinkingMode(senderID, mode string) {
	f.mu.Lock()
	f.thinkingModes[senderID] = mode
	f.mu.Unlock()
}

// SetDefaults updates the default LLM client and model name.
// For immediate effect after modifying global LLM configuration via setup/settings panel.
// Wraps the new defaultLLM with RetryLLM if retryConfig is set.
func (f *LLMFactory) SetDefaults(newLLM llm.LLM, newModel string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.retryConfig.Attempts > 0 {
		if _, ok := newLLM.(*llm.RetryLLM); !ok {
			newLLM = llm.NewRetryLLM(newLLM, f.retryConfig)
		}
	}
	f.defaultLLM = newLLM
	f.defaultModel = newModel
	// Clear all user caches, let subsequent GetLLM recreate clients
	f.clients = make(map[string]llm.LLM)
	f.models = make(map[string]string)
	f.maxContexts = make(map[string]int)
	f.maxOutputTokens = make(map[string]int)
	f.thinkingModes = make(map[string]string)
}

// SetDefaultThinkingMode sets the default thinking mode for users without custom config.
// Used by CLI mode where there's no DB-backed configSvc.
func (f *LLMFactory) SetDefaultThinkingMode(mode string) {
	f.mu.Lock()
	f.defaultThinkingMode = mode
	// Clear cached thinkingModes so GetLLM picks up the new default
	f.thinkingModes = make(map[string]string)
	f.mu.Unlock()
}

// SetChatLLM caches an LLM client for a specific chat session without affecting
// other chats or the global default. Used by Ctrl+N subscription switching to
// ensure each CLI window's model change is isolated.
func (f *LLMFactory) SetChatLLM(senderID, chatID string, client llm.LLM, model string) {
	if chatID == "" {
		// No chat isolation — update user-level cache only
		f.mu.Lock()
		f.clients[senderID] = client
		f.models[senderID] = model
		f.mu.Unlock()
		return
	}
	key := chatKey(senderID, chatID)
	f.mu.Lock()
	f.clients[key] = client
	f.models[key] = model
	f.mu.Unlock()
}

// SetProxyLLM sets a ProxyLLM for a user (used when their active runner has local LLM).
// This overrides any per-user LLM config for this sender.
func (f *LLMFactory) SetProxyLLM(senderID string, proxy *llm.ProxyLLM, model string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clients[senderID] = proxy
	f.models[senderID] = model
	f.maxContexts[senderID] = 0
	f.maxOutputTokens[senderID] = 0
	f.thinkingModes[senderID] = ""
}

// ClearProxyLLM removes a ProxyLLM for a user (runner disconnected or local LLM disabled).
func (f *LLMFactory) ClearProxyLLM(senderID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.clients, senderID)
	delete(f.models, senderID)
	delete(f.maxContexts, senderID)
	delete(f.thinkingModes, senderID)
}

// createClient creates LLM client based on configuration, returns nil if configuration is invalid.
// Bare clients created are wrapped by RetryLLM, ensuring SubAgent and subscription clients
// also enjoy 429/5xx exponential backoff retry capability.
func (f *LLMFactory) createClient(cfg *sqlite.UserLLMConfig) (llm.LLM, string) {
	// Check required fields
	if cfg.BaseURL == "" || cfg.APIKey == "" {
		return nil, ""
	}

	model := cfg.Model
	if model == "" {
		model = f.defaultModel
	}

	var client llm.LLM
	switch cfg.Provider {
	case "anthropic":
		client = llm.NewAnthropicLLM(llm.AnthropicConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: model,
			MaxTokens:    cfg.MaxOutputTokens,
		})

	default:
		// All other providers (openai, deepseek, siliconflow, etc.) use OpenAI-compatible API
		client = llm.NewOpenAILLM(llm.OpenAIConfig{
			BaseURL:        cfg.BaseURL,
			APIKey:         cfg.APIKey,
			DefaultModel:   model,
			MaxTokens:      cfg.MaxOutputTokens,
			OnModelsLoaded: cfg.OnModelsLoaded,
			SubscriptionID: cfg.ID,
		})
	}

	// Wrap with RetryLLM: ensure all clients created via LLMFactory have retry capability
	f.mu.RLock()
	retryCfg := f.retryConfig
	f.mu.RUnlock()
	if retryCfg.Attempts > 0 {
		client = llm.NewRetryLLM(client, retryCfg)
	}

	return client, model
}

// Invalidate invalidates user's LLM client cache (called after configuration update).
// Also clears user-level key (senderID) and all per-chat keys (senderID:chatID),
// ensuring GetLLMForChat won't return stale per-chat cache.
func (f *LLMFactory) Invalidate(senderID string) {
	f.mu.Lock()
	prefix := senderID + ":"
	for k := range f.clients {
		if k == senderID || strings.HasPrefix(k, prefix) {
			delete(f.clients, k)
			delete(f.models, k)
			delete(f.maxContexts, k)
			delete(f.maxOutputTokens, k)
			delete(f.thinkingModes, k)
		}
	}
	f.mu.Unlock()
}

// InvalidateAll invalidates all caches
func (f *LLMFactory) InvalidateAll() {
	f.mu.Lock()
	f.clients = make(map[string]llm.LLM)
	f.models = make(map[string]string)
	f.maxContexts = make(map[string]int)
	f.maxOutputTokens = make(map[string]int)
	f.thinkingModes = make(map[string]string)
	f.mu.Unlock()
}

// SetSettingsService injects SettingsService (For reading/writing user concurrency configuration）。
// Must be called after Agent initialization, because SettingsService creation depends on Agent.
func (f *LLMFactory) SetSettingsService(svc *SettingsService) {
	f.settingsSvc = svc
}

// SetLLMSemaphoreManager injects LLMSemaphoreManager.
func (f *LLMFactory) SetLLMSemaphoreManager(mgr *llm.LLMSemaphoreManager) {
	f.llmSemManager = mgr
}

// LLMSemaphoreManager returns the LLMSemaphoreManager instance.
func (f *LLMFactory) LLMSemaphoreManager() *llm.LLMSemaphoreManager {
	return f.llmSemManager
}

// ListModels returns available model names from the default LLM client.
func (f *LLMFactory) ListModels() []string {
	return f.defaultLLM.ListModels()
}

// ListAllModelsForUser returns model names from the default LLM plus all subscription
// Model fields for a given user. Used for global tier settings where the user should
// see models across all their subscriptions.
func (f *LLMFactory) ListAllModelsForUser(senderID string) []string {
	seen := make(map[string]bool)
	var result []string

	// Default LLM models
	for _, m := range f.defaultLLM.ListModels() {
		if !seen[m] {
			seen[m] = true
			result = append(result, m)
		}
	}

	// All subscription model fields (no API calls, just DB records).
	// When senderID is empty, collect models from ALL subscriptions
	// (used by settings card tier selectors which need a global model list).
	if f.subscriptionSvc != nil {
		var subs []*sqlite.LLMSubscription
		var err error
		if senderID != "" {
			subs, err = f.subscriptionSvc.List(senderID)
		} else {
			subs, err = f.subscriptionSvc.ListAll()
		}
		if err == nil {
			for _, sub := range subs {
				if sub.Model != "" && !seen[sub.Model] {
					seen[sub.Model] = true
					result = append(result, sub.Model)
				}
			}
		}
	}

	return result
}

// GetLLMConcurrency reads the user's personal LLM concurrency limit from configuration.
// Uses default DefaultLLMConcurrencyPersonal when not configured.
func (f *LLMFactory) GetLLMConcurrency(senderID string) int {
	if f.settingsSvc == nil {
		return llm.DefaultLLMConcurrencyPersonal
	}
	settings, err := f.settingsSvc.GetSettings("feishu", senderID)
	if err != nil || settings == nil {
		return llm.DefaultLLMConcurrencyPersonal
	}
	return parseOrDefault(settings["llm_max_concurrent_personal"], llm.DefaultLLMConcurrencyPersonal)
}

// SetLLMConcurrency sets the user's personal LLM concurrency limit configuration.
func (f *LLMFactory) SetLLMConcurrency(senderID string, personal int) error {
	if f.settingsSvc == nil {
		return fmt.Errorf("settings service not available")
	}
	return f.settingsSvc.SetSetting("feishu", senderID, "llm_max_concurrent_personal", fmt.Sprintf("%d", personal))
}

// parseOrDefault parses string as int, returns default on failure.
func parseOrDefault(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	var v int
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil || v <= 0 {
		return defaultVal
	}
	return v
}

// LLMSemAcquireForUser returns an LLMSemAcquire callback for the given user.
// It determines whether the user uses a personal or global LLM and reads
// the corresponding concurrency setting.
// Returns nil if no semaphore manager is configured.
func (f *LLMFactory) LLMSemAcquireForUser(senderID string) func(context.Context) func() {
	if f.llmSemManager == nil {
		return nil
	}
	llmKey := "global"
	if f.HasCustomLLM(senderID) {
		llmKey = "personal"
	}
	return func(ctx context.Context) func() {
		personalCap := f.GetLLMConcurrency(senderID)
		cap := llm.DefaultLLMConcurrency
		if llmKey == "personal" {
			cap = personalCap
		}
		return f.llmSemManager.Acquire(ctx, senderID, llmKey, func() int { return cap })
	}
}

// SubAgentSemAcquireForUser returns a SubAgentSem callback for the given user.
// SubAgent concurrency is bounded by a separate semaphore (llmKey="subagent").
// Returns nil if no semaphore manager is configured.
func (f *LLMFactory) SubAgentSemAcquireForUser(senderID string) func(context.Context) func() {
	if f.llmSemManager == nil {
		return nil
	}
	return func(ctx context.Context) func() {
		// Default max concurrent SubAgents: 3
		cap := parseOrDefault(f.getSetting(senderID, "subagent_max_concurrent"), 3)
		return f.llmSemManager.Acquire(ctx, senderID, "subagent", func() int { return cap })
	}
}

// getSetting reads a single user setting. Returns "" on any error.
func (f *LLMFactory) getSetting(senderID, key string) string {
	if f.settingsSvc == nil {
		return ""
	}
	settings, err := f.settingsSvc.GetSettings("feishu", senderID)
	if err != nil || settings == nil {
		return ""
	}
	return settings[key]
}

// GetMaxOutputTokens returns the user's configured max_output_tokens (0 = default).
// Uses the per-user cache populated by GetLLM(); no DB hit.
func (f *LLMFactory) GetMaxOutputTokens(senderID string) int {
	f.mu.RLock()
	if v, ok := f.maxOutputTokens[senderID]; ok {
		f.mu.RUnlock()
		return v
	}
	f.mu.RUnlock()
	// User has no cached config (using default client) — return 0 (use default)
	return 0
}

// GetLLMForModel gets LLM client for a specified model, for SubAgent using a different model than main Agent.
//
// Lookup priority:
//  1. Find subscription with Model field exactly matching targetModel among all user subscriptions
//  2. Use current active subscription's credentials + targetModel
//  3. Use any subscription's credentials + targetModel (prefer Provider match)
//  4. Fallback to main Agent's current LLM (ignore targetModel)
//
// Returns: (LLM client, actual model name, maxContext, thinkingMode, whether non-default model was used)
func (f *LLMFactory) GetLLMForModel(senderID, targetModel string) (llm.LLM, string, int, string, bool) {
	resolvedModel, _ := f.resolveTierModel(targetModel)
	if resolvedModel == "" {
		client, model, maxCtx, tm := f.GetLLM(senderID)
		return client, model, maxCtx, tm, false
	}

	// Step 1: look up from cached model lists in DB — O(1), no API calls
	modelMap := f.buildModelSubscriptionMap(senderID)
	if sub, ok := modelMap[resolvedModel]; ok {
		log.WithFields(log.Fields{"model": resolvedModel, "sub": sub.Name, "step": 1}).Info("[LLM] GetLLMForModel: cache hit")
		client := f.createClientFromSub(sub, resolvedModel)
		if client != nil {
			return client, resolvedModel, sub.MaxContext, sub.ThinkingMode, true
		}
	} else {
		log.WithField("model", resolvedModel).Info("[LLM] GetLLMForModel: cache miss, trying subscriptions")
	}

	// Step 2: cache miss — try each subscription.
	// First try config.json subscriptions (CLI mode), then DB subscriptions.
	// Config subs match on Model field only (no CachedModels).
	f.mu.RLock()
	getConfigSubs := f.configSubsFn
	f.mu.RUnlock()
	var configSubs []config.SubscriptionConfig
	if getConfigSubs != nil {
		configSubs = getConfigSubs()
	}
	// Config subs don't have CachedModels. Search all subs by priority:
	// 1. Exact Model field match → correct endpoint guaranteed
	// 2. Provider guess match (e.g. "gpt-5-mini" → openai, "claude-*" → anthropic)
	// NOT: "any valid sub" — using a random endpoint with a model that doesn't
	// belong there causes 400 "model not supported" errors.
	guessedProvider := guessProvider(resolvedModel)
	var providerMatchSub *config.SubscriptionConfig
	for i := range configSubs {
		cs := &configSubs[i]
		if cs.BaseURL == "" || cs.APIKey == "" {
			continue
		}
		// Priority 1: exact Model match
		if cs.Model == resolvedModel {
			sub := configSubToLLMSubscription(*cs)
			client := f.createClientFromSub(sub, resolvedModel)
			if client != nil {
				log.WithFields(log.Fields{"model": resolvedModel, "sub": cs.Name, "step": 2, "source": "config-exact"}).Info("[LLM] GetLLMForModel: found in config sub (exact)")
				return client, resolvedModel, sub.MaxContext, sub.ThinkingMode, true
			}
		}
		// Priority 2: provider guess
		if providerMatchSub == nil && guessedProvider != "" && strings.Contains(strings.ToLower(cs.Provider), guessedProvider) {
			providerMatchSub = cs
		}
	}
	// Try provider-matched sub
	if providerMatchSub != nil {
		sub := configSubToLLMSubscription(*providerMatchSub)
		client := f.createClientFromSub(sub, resolvedModel)
		if client != nil {
			log.WithFields(log.Fields{"model": resolvedModel, "sub": providerMatchSub.Name, "step": 2, "source": "config-provider"}).Info("[LLM] GetLLMForModel: found via provider guess")
			return client, resolvedModel, sub.MaxContext, sub.ThinkingMode, true
		}
	}

	// DB subscriptions: search by CachedModels/Model match, then provider guess, then any valid sub.
	if f.subscriptionSvc != nil && senderID != "" {
		subs, err := f.subscriptionSvc.List(senderID)
		if err == nil && len(subs) > 0 {
			var dbProviderSub *sqlite.LLMSubscription
			for _, sub := range subs {
				if sub.BaseURL == "" || sub.APIKey == "" {
					continue
				}
				// Priority 1: model in CachedModels or exact Model field
				found := sub.Model == resolvedModel
				if !found {
					for _, m := range sub.CachedModels {
						if m == resolvedModel {
							found = true
							break
						}
					}
				}
				if found {
					client := f.createClientFromSub(sub, resolvedModel)
					if client != nil {
						log.WithFields(log.Fields{"model": resolvedModel, "sub": sub.Name, "step": 2}).Info("[LLM] GetLLMForModel: found in sub cache")
						return client, resolvedModel, sub.MaxContext, sub.ThinkingMode, true
					}
				}
				// No cache — try loading from API (first-run for this subscription)
				if len(sub.CachedModels) == 0 {
					client := f.createClientFromSub(sub, resolvedModel)
					if client == nil {
						continue
					}
					// llmTestTimeout is the timeout for testing LLM connectivity.
					const llmTestTimeout = 15 * time.Second
					ctx, cancel := context.WithTimeout(context.Background(), llmTestTimeout)
					if loader, ok := client.(llm.ModelLoader); ok {
						_ = loader.LoadModelsFromAPI(ctx)
					}
					cancel()
					// OnModelsLoaded callback updated DB — re-read sub to get fresh cache
					updatedSubs, err2 := f.subscriptionSvc.List(senderID)
					if err2 == nil {
						for _, us := range updatedSubs {
							if us.ID == sub.ID {
								for _, m := range us.CachedModels {
									if m == resolvedModel {
										log.WithFields(log.Fields{"model": resolvedModel, "sub": sub.Name, "step": 2}).Info("[LLM] GetLLMForModel: found after API load")
										return client, resolvedModel, sub.MaxContext, sub.ThinkingMode, true
									}
								}
							}
						}
					}
				}
				// Collect fallbacks: provider guess only (no "any sub" — wrong endpoint risk)
				if dbProviderSub == nil && guessedProvider != "" && strings.Contains(strings.ToLower(sub.Provider), guessedProvider) {
					dbProviderSub = sub
				}
			}
			// Priority 2: provider guess
			if dbProviderSub != nil {
				client := f.createClientFromSub(dbProviderSub, resolvedModel)
				if client != nil {
					log.WithFields(log.Fields{"model": resolvedModel, "sub": dbProviderSub.Name, "step": 2, "source": "db-provider"}).Info("[LLM] GetLLMForModel: found via provider guess (DB)")
					return client, resolvedModel, dbProviderSub.MaxContext, dbProviderSub.ThinkingMode, true
				}
			}
		}
	}

	// Fallback: model not found in any subscription — use default client with its OWN model
	// (not resolvedModel, to avoid sending wrong model to wrong endpoint).
	log.WithFields(log.Fields{"model": resolvedModel, "fallback": true}).Warn("[LLM] GetLLMForModel: model not found in any subscription, using default")
	client, defaultModel, maxCtx, tm := f.GetLLM(senderID)
	return client, defaultModel, maxCtx, tm, false
}

// buildModelSubscriptionMap builds a model_name → subscription lookup table from
// cached model lists in DB and config.json subscriptions. No API calls.
// Each subscription's active model (sub.Model) is always included.
// Config subs are checked first (CLI mode), then DB subs (server mode).
func (f *LLMFactory) buildModelSubscriptionMap(senderID string) map[string]*sqlite.LLMSubscription {
	m := make(map[string]*sqlite.LLMSubscription)

	// First: config.json subscriptions (CLI mode)
	f.mu.RLock()
	getConfigSubs := f.configSubsFn
	f.mu.RUnlock()
	var configSubs []config.SubscriptionConfig
	if getConfigSubs != nil {
		configSubs = getConfigSubs()
	}
	for _, cs := range configSubs {
		if cs.BaseURL == "" || cs.APIKey == "" {
			continue
		}
		sub := configSubToLLMSubscription(cs)
		if sub.Model != "" {
			if _, exists := m[sub.Model]; !exists {
				m[sub.Model] = sub
			}
		}
		// Config subs don't have CachedModels — only Model field is available
	}

	// Second: DB subscriptions (server mode)
	if f.subscriptionSvc != nil && senderID != "" {
		subs, err := f.subscriptionSvc.List(senderID)
		if err == nil && len(subs) > 0 {
			for _, sub := range subs {
				if sub.BaseURL == "" || sub.APIKey == "" {
					continue
				}
				for _, modelName := range sub.CachedModels {
					if _, exists := m[modelName]; !exists {
						m[modelName] = sub
					}
				}
				if sub.Model != "" {
					if _, exists := m[sub.Model]; !exists {
						m[sub.Model] = sub
					}
				}
			}
		}
	}
	return m
}

// configSubToLLMSubscription converts a config.SubscriptionConfig to sqlite.LLMSubscription
// for use in buildModelSubscriptionMap.
func configSubToLLMSubscription(cs config.SubscriptionConfig) *sqlite.LLMSubscription {
	return &sqlite.LLMSubscription{
		ID:              cs.ID,
		Name:            cs.Name,
		Provider:        cs.Provider,
		BaseURL:         cs.BaseURL,
		APIKey:          cs.APIKey,
		Model:           cs.Model,
		MaxOutputTokens: cs.MaxOutputTokens,
		ThinkingMode:    cs.ThinkingMode,
	}
}

// createClientFromSub creates LLM client from subscription, using specified model name (not subscription's default model)
func (f *LLMFactory) createClientFromSub(sub *sqlite.LLMSubscription, model string) llm.LLM {
	if sub.BaseURL == "" || sub.APIKey == "" {
		return nil
	}
	cfg := &sqlite.UserLLMConfig{
		Provider:        sub.Provider,
		BaseURL:         sub.BaseURL,
		APIKey:          sub.APIKey,
		Model:           model,
		MaxOutputTokens: sub.MaxOutputTokens,
		OnModelsLoaded: func(models []string) {
			if f.subscriptionSvc != nil && sub.ID != "" {
				if err := f.subscriptionSvc.UpdateCachedModels(sub.ID, models); err != nil {
					log.WithError(err).WithField("sub_id", sub.ID).Debug("failed to cache subscription models (may be config-only sub)")
				}
			}
		},
	}
	client, _ := f.createClient(cfg)
	return client
}

func normalizeModelTier(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "vanguard", "strong":
		return "vanguard"
	case "balance", "medium":
		return "balance"
	case "swift", "weak":
		return "swift"
	default:
		return ""
	}
}

func (f *LLMFactory) resolveTierModel(value string) (string, bool) {
	tier := normalizeModelTier(value)
	if tier == "" {
		return value, false
	}

	f.mu.RLock()
	tiers := f.tierModels
	f.mu.RUnlock()

	// Try requested tier first
	model := f.tierModel(tiers, tier)
	if model != "" {
		return model, true
	}
	// Fallback chain: swift/vanguard → balance → vanguard/swift
	fallback := ""
	switch tier {
	case "swift", "vanguard":
		fallback = "balance"
	case "balance":
		fallback = "vanguard"
	}
	if fallback != "" {
		if model = f.tierModel(tiers, fallback); model != "" {
			return model, true
		}
	}
	// All tiers unconfigured — let caller fall through to default LLM
	return "", true
}

// tierModel returns the trimmed model name for a tier, or "" if unconfigured.
func (f *LLMFactory) tierModel(tiers config.LLMConfig, tier string) string {
	switch tier {
	case "vanguard":
		return strings.TrimSpace(tiers.VanguardModel)
	case "balance":
		return strings.TrimSpace(tiers.BalanceModel)
	case "swift":
		return strings.TrimSpace(tiers.SwiftModel)
	}
	return ""
}

// guessProvider guesses provider based on model name.
// Returns empty string if unable to guess.
func guessProvider(model string) string {
	switch {
	case strings.Contains(model, "claude"):
		return "anthropic"
	case strings.Contains(model, "gpt") || strings.Contains(model, "o1") || strings.Contains(model, "o3") || strings.Contains(model, "chatgpt"):
		return "openai"
	case strings.Contains(model, "deepseek"):
		return "deepseek"
	case strings.Contains(model, "gemini"):
		return "google"
	case strings.Contains(model, "qwen"):
		return "qwen"
	default:
		return ""
	}
}

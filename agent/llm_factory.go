package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"xbot/channel"
	"xbot/config"
	"xbot/llm"
	log "xbot/logger"
	"xbot/protocol"
	"xbot/storage/sqlite"
)

// LLMFactory 管理用户自定义 LLM 客户端的创建和缓存。
//
// 设计原则：
//   - LLM 客户端按 (subscription, apiType) 缓存在 clientCache 中（HTTP 连接池复用）
//   - per-session 解析通过 ResolveLLM 从 DB (tenants + user_default_model) 读取，无内存缓存
//   - per-model 配置（max_context, max_output_tokens, thinking_mode）从 subscription_models 表读取
type LLMFactory struct {
	subscriptionSvc *sqlite.LLMSubscriptionService
	tenantSvc       *sqlite.TenantService // for per-session model restoration from DB
	configSubsFn    func() []config.SubscriptionConfig
	settingsSvc     *SettingsService

	// Global defaults (no per-user override)
	defaultLLM          llm.LLM
	defaultModel        string
	defaultThinkingMode string
	retryConfig         llm.RetryConfig
	globalMaxTokens     int

	// model name → max context tokens (from config model_contexts, not per-user)
	modelContexts map[string]int

	// ─── Model-first resolution (v38+) ───────────────────────
	// clientCache shares one LLM client per (subscription, apiType). A client is
	// reusable across models of the same subscription — the model is supplied
	// per request — so we cache at the subscription level, not the model level.
	clientCache map[clientCacheKey]llm.LLM

	// proxyLLMs stores runtime-injected ProxyLLMs by senderID. These override
	// DB-based resolution — when a runner has local LLM configured, the proxy
	// takes priority over any cloud subscription. Not persisted: tied to the
	// runner connection lifecycle (SetProxyLLM on connect, ClearProxyLLM on
	// disconnect).
	proxyLLMs sync.Map

	mu            sync.RWMutex
	llmSemManager *llm.LLMSemaphoreManager
}

// proxyEntry stores a runtime-injected ProxyLLM override.
type proxyEntry struct {
	client llm.LLM
	model  string
}

// clientCacheKey identifies a shared LLM client by subscription + API type.
type clientCacheKey struct {
	subID   string
	apiType string
}

// NewLLMFactory 创建 LLM 工厂
func NewLLMFactory(defaultLLM llm.LLM, defaultModel string) *LLMFactory {
	return &LLMFactory{
		defaultLLM:    defaultLLM,
		defaultModel:  defaultModel,
		modelContexts: make(map[string]int),
		clientCache:   make(map[clientCacheKey]llm.LLM),
	}
}

// ─── Getters ─────────────────────────────────────────────

// GetDefaultModel returns the default model name.
func (f *LLMFactory) GetDefaultModel() string { return f.defaultModel }

// GetSubscriptionSvc returns the subscription service.
func (f *LLMFactory) GetSubscriptionSvc() *sqlite.LLMSubscriptionService {
	return f.subscriptionSvc
}

// ─── Configuration setters ───────────────────────────────

func (f *LLMFactory) SetRetryConfig(cfg llm.RetryConfig) {
	f.mu.Lock()
	f.retryConfig = cfg
	if cfg.Attempts > 0 {
		if _, ok := f.defaultLLM.(*llm.RetryLLM); !ok {
			f.defaultLLM = llm.NewRetryLLM(f.defaultLLM, cfg)
		}
	}
	f.mu.Unlock()
}

func (f *LLMFactory) SetModelContexts(m map[string]int) {
	f.mu.Lock()
	f.modelContexts = m
	f.mu.Unlock()
}

func (f *LLMFactory) SetGlobalMaxTokens(n int) {
	f.mu.Lock()
	f.globalMaxTokens = n
	f.mu.Unlock()
}

func (f *LLMFactory) SetSubscriptionSvc(svc *sqlite.LLMSubscriptionService) {
	f.subscriptionSvc = svc
}

// SetTenantSvc injects the TenantService for per-session model restoration.
// Used by GetLLMForChat to recover per-session subscription+model from the
// tenants table when the in-memory cache is empty (e.g. after server restart).
func (f *LLMFactory) SetTenantSvc(svc *sqlite.TenantService) {
	f.tenantSvc = svc
}

// GetTenantSvc returns the TenantService used for per-session model restoration.
func (f *LLMFactory) GetTenantSvc() *sqlite.TenantService {
	return f.tenantSvc
}

func (f *LLMFactory) SetConfigSubs(fn func() []config.SubscriptionConfig) {
	f.mu.Lock()
	f.configSubsFn = fn
	f.mu.Unlock()
}

func (f *LLMFactory) SetSettingsService(svc *SettingsService) { f.settingsSvc = svc }

func (f *LLMFactory) SetLLMSemaphoreManager(mgr *llm.LLMSemaphoreManager) {
	f.llmSemManager = mgr
}

func (f *LLMFactory) LLMSemaphoreManager() *llm.LLMSemaphoreManager { return f.llmSemManager }

// ─── Context resolution ──────────────────────────────────

func (f *LLMFactory) resolveModelContext(model string) int {
	if model == "" {
		return 0
	}
	f.mu.RLock()
	ctx := f.modelContexts[model]
	f.mu.RUnlock()
	return ctx
}

// lookupSub fetches a subscription by ID from the subscription service.
// Returns nil if the service is unavailable or the subscription doesn't exist.
func (f *LLMFactory) lookupSub(subID string) *sqlite.LLMSubscription {
	if f.subscriptionSvc == nil || subID == "" {
		return nil
	}
	sub, err := f.subscriptionSvc.Get(subID)
	if err != nil {
		return nil
	}
	return sub
}

// resolveEffectiveContext resolves max context for (model, subID):
// per-model subscription config → global model_contexts → 0
func (f *LLMFactory) resolveEffectiveContext(model string, subID string) int {
	if sub := f.lookupSub(subID); sub != nil {
		if v := sub.GetPerModelMaxContext(model); v > 0 {
			return v
		}
	}
	return f.resolveModelContext(model)
}

// GetEffectiveMaxContext is the single source of truth for "what max context should the UI show?".
// Resolves via GetLLMForChat (per-session) when chatID is provided; GetLLM (global) otherwise.
func (f *LLMFactory) GetEffectiveMaxContext(senderID, chatID string) int {
	_, _, mc, _, _ := f.GetLLMForChat(senderID, chatID)
	return mc
}

// ─── Primary LLM resolution ──────────────────────────────

func chatKey(senderID, chatID string) string { return senderID + ":" + chatID }

// GetLLM returns (client, model, maxContext, thinkingMode, maxOutputTokens).
// ProxyLLM (runner local LLM) takes priority over DB subscriptions.
// Falls back to the global default LLM when no subscription resolves.
func (f *LLMFactory) GetLLM(senderID string) (llm.LLM, string, int, string, int) {
	// ProxyLLM override (runner local LLM)
	if v, ok := f.proxyLLMs.Load(senderID); ok {
		if pe, ok := v.(proxyEntry); ok && pe.client != nil {
			return pe.client, pe.model, f.resolveModelContext(pe.model), "", 0
		}
	}
	if f.subscriptionSvc != nil {
		sub, err := f.subscriptionSvc.GetDefault(senderID)
		if err == nil && sub != nil && sub.BaseURL != "" && sub.APIKey != "" {
			if strings.HasSuffix(sub.APIKey, "****") && len(sub.APIKey) <= 20 {
				log.WithFields(log.Fields{
					"sender_id": senderID, "sub_id": sub.ID,
					"base_url": sub.BaseURL, "provider": sub.Provider,
				}).Error("[LLMFactory] GetLLM: subscription has masked API key")
			}
			model := ""
			if udm, err := f.subscriptionSvc.GetUserDefaultModel(senderID); err == nil && udm != nil {
				model = udm.Model
			}
			if model == "" {
				model = f.defaultModel
			}
			client := f.getOrCreateClient(sub, model)
			if client != nil {
				pmc := f.resolveModelConfig(sub.ID, model)
				maxOut := pmc.maxOutputTokens
				if maxOut == 0 {
					maxOut = sub.MaxOutputTokens
				}
				thinking := pmc.thinkingMode
				if thinking == "" {
					thinking = f.userThinkingMode(senderID)
				}
				return client, model, f.resolveSubContextFor(sub.ID, model), thinking, maxOut
			}
		}
	}

	return f.defaultLLM, f.defaultModel, 0, f.defaultThinkingMode, 0
}

// GetLLMForChat returns (client, model, maxContext, thinkingMode, maxOutputTokens).
// If chatID is non-empty, reads per-session (subID, model) from the tenants table;
// falls back to GetLLM (global default) when no per-session override exists.
func (f *LLMFactory) GetLLMForChat(senderID, chatID string) (llm.LLM, string, int, string, int) {
	// ProxyLLM override (runner local LLM) — highest priority.
	if v, ok := f.proxyLLMs.Load(senderID); ok {
		if pe, ok := v.(proxyEntry); ok && pe.client != nil {
			return pe.client, pe.model, f.resolveModelContext(pe.model), "", 0
		}
	}
	if chatID != "" && f.tenantSvc != nil {
		// Try per-session override from tenants table.
		// Channel may be "" or "cli" depending on write path.
		subID, model, _ := f.tenantSvc.GetTenantSubscription("", chatID)
		if subID == "" {
			subID, model, _ = f.tenantSvc.GetTenantSubscription("cli", chatID)
		}
		if subID != "" {
			sub, err := f.subscriptionSvc.Get(subID)
			if err == nil && sub != nil && sub.BaseURL != "" {
				if model == "" {
					model = sub.Model
				}
				client := f.getOrCreateClient(sub, model)
				if client != nil {
					pmc := f.resolveModelConfig(sub.ID, model)
					maxOut := pmc.maxOutputTokens
					if maxOut == 0 {
						maxOut = sub.MaxOutputTokens
					}
					thinking := pmc.thinkingMode
					if thinking == "" {
						thinking = f.userThinkingMode(senderID)
					}
					return client, model, f.resolveSubContextFor(sub.ID, model), thinking, maxOut
				}
			}
		}
	}
	return f.GetLLM(senderID)
}

// GetMaxOutputTokens returns the max_output_tokens for the user's active
// subscription, resolved via GetLLM. Prefer using GetLLM which returns all
// subscription-derived values in one call.
func (f *LLMFactory) GetMaxOutputTokens(senderID string, chatID ...string) int {
	_, _, _, _, maxOut := f.GetLLM(senderID)
	return maxOut
}

// HasCustomLLM checks if a user has custom LLM config: either a ProxyLLM
// (runner local LLM) or a personal (non-system) DB subscription with credentials.
func (f *LLMFactory) HasCustomLLM(senderID string) bool {
	if _, ok := f.proxyLLMs.Load(senderID); ok {
		return true
	}
	if f.subscriptionSvc != nil {
		subs, err := f.subscriptionSvc.List(senderID)
		if err == nil {
			for _, sub := range subs {
				if !sub.IsSystem && sub.BaseURL != "" && sub.APIKey != "" {
					return true
				}
			}
		}
	}
	return false
}

// SwitchSubscription switches a user's active LLM to the specified subscription.
// With the entries cache removed, this only updates the global default LLM/model
// for cli_user (so SubAgent fallback, ListModels(), and GetLLM follow the user's
// last choice). Per-session state lives in DB (tenants table).
func (f *LLMFactory) SwitchSubscription(senderID string, sub *sqlite.LLMSubscription, chatID string) error {
	if sub == nil {
		return fmt.Errorf("SwitchSubscription: sub is required")
	}
	if senderID == "cli_user" {
		// Model is user-level — resolve from user_default_model, not sub.Model.
		model := ""
		if f.subscriptionSvc != nil {
			if udm, err := f.subscriptionSvc.GetUserDefaultModel(senderID); err == nil && udm != nil {
				model = udm.Model
			}
		}
		client := f.createClientFromSub(sub, model)
		f.mu.Lock()
		if client != nil {
			f.defaultLLM = client
		}
		f.defaultModel = model
		f.mu.Unlock()
	}
	return nil
}

// SetSessionLLM persists the per-session subscription mapping to the tenants
// table. This is the write counterpart of GetLLMForChat's read path.
func (f *LLMFactory) SetSessionLLM(senderID, chatID string, sub *sqlite.LLMSubscription) error {
	if f.tenantSvc != nil && chatID != "" && sub != nil {
		// Model is user-level — resolve from user_default_model, not sub.Model.
		model := ""
		if f.subscriptionSvc != nil {
			if udm, err := f.subscriptionSvc.GetUserDefaultModel(senderID); err == nil && udm != nil {
				model = udm.Model
			}
		}
		return f.tenantSvc.SetTenantSubscription("cli", chatID, sub.ID, model)
	}
	return nil
}

// SwitchModel switches the active model without changing subscription.
// Per-session switch (with chatID): no-op — persistence is handled by SelectModel.
// User-level switch (no chatID): persists the model change to the default
// subscription in DB so new sessions and GetLLM pick it up.
func (f *LLMFactory) SwitchModel(senderID, model string, chatID ...string) {
	effectiveChatID := ""
	if len(chatID) > 0 {
		effectiveChatID = chatID[0]
	}
	if effectiveChatID != "" {
		return
	}
	if f.subscriptionSvc != nil && senderID != "" {
		if sub, err := f.subscriptionSvc.GetDefault(senderID); err == nil && sub != nil && sub.Model != model && sub.ID != "" {
			_ = f.subscriptionSvc.SetModel(sub.ID, model)
		}
	}
}

// SetChatLLM is a no-op: per-session (subscription, model) lives in the tenants
// table, not in an in-memory cache.
func (f *LLMFactory) SetChatLLM(senderID, chatID, subID, model string) error {
	return nil
}

// SetUserMaxOutputTokens is a no-op: the DB path via UpdatePerModelConfig
// (Agent.SetUserMaxOutputTokens) is authoritative.
func (f *LLMFactory) SetUserMaxOutputTokens(senderID string, n int) {
}

// SetUserThinkingMode is a no-op: thinking mode is a global user setting stored
// in user_settings DB via Agent.SetUserThinkingMode.
func (f *LLMFactory) SetUserThinkingMode(senderID, mode string) {
}

// SetDefaults updates the global default LLM and model.
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
}

// SetSystemLLM sets the factory's fallback LLM from the shared system
// subscription (reconciled from config/env at boot). Unlike SetDefaults it does
// NOT clear per-user caches — it only updates the lowest-priority fallback used
// when no per-user/per-chat entry and no DB default subscription resolve.
func (f *LLMFactory) SetSystemLLM(newLLM llm.LLM, newModel string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.retryConfig.Attempts > 0 {
		if _, ok := newLLM.(*llm.RetryLLM); !ok {
			newLLM = llm.NewRetryLLM(newLLM, f.retryConfig)
		}
	}
	f.defaultLLM = newLLM
	f.defaultModel = newModel
}

func (f *LLMFactory) SetDefaultThinkingMode(mode string) {
	f.mu.Lock()
	f.defaultThinkingMode = mode
	f.mu.Unlock()
}

// SetProxyLLM injects a ProxyLLM (runner local LLM) for a user. This overrides
// all DB-based resolution — when present, ResolveLLM/GetLLM/GetLLMForChat return
// the proxy without consulting subscriptions or tenants table.
// Not persisted: cleared on runner disconnect via ClearProxyLLM.
func (f *LLMFactory) SetProxyLLM(senderID string, proxy llm.LLM, model string) {
	f.proxyLLMs.Store(senderID, proxyEntry{client: proxy, model: model})
}

// ClearProxyLLM removes a ProxyLLM override. After this, resolution falls back
// to DB subscriptions (tenants table → user_default_model → system default).
func (f *LLMFactory) ClearProxyLLM(senderID string) {
	f.proxyLLMs.Delete(senderID)
}

// Invalidate is a no-op: no in-memory entries to clear. Client cache
// invalidation is handled per-subscription via InvalidateSubscription.
func (f *LLMFactory) Invalidate(senderID string) {
}

// InvalidateSender is a no-op: no in-memory entries or session memos to clear.
func (f *LLMFactory) InvalidateSender(senderID string) {
}

// InvalidateSession is a no-op: no in-memory entries to clear.
func (f *LLMFactory) InvalidateSession(senderID, chatID string) {
}

// InvalidateAll clears the client cache.
func (f *LLMFactory) InvalidateAll() {
	f.mu.Lock()
	f.clientCache = make(map[clientCacheKey]llm.LLM)
	f.mu.Unlock()
}

// ─── Client creation ─────────────────────────────────────

func (f *LLMFactory) createClient(cfg *sqlite.UserLLMConfig) (llm.LLM, string) {
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
			BaseURL: cfg.BaseURL, APIKey: cfg.APIKey,
			DefaultModel: model, MaxTokens: cfg.MaxOutputTokens,
		})
	default:
		client = llm.NewOpenAILLM(llm.OpenAIConfig{
			BaseURL: cfg.BaseURL, APIKey: cfg.APIKey,
			DefaultModel: model, MaxTokens: cfg.MaxOutputTokens, APIType: cfg.APIType,
			OnModelsLoaded: cfg.OnModelsLoaded, SubscriptionID: cfg.ID,
		})
	}

	f.mu.RLock()
	retryCfg := f.retryConfig
	f.mu.RUnlock()
	if retryCfg.Attempts > 0 {
		client = llm.NewRetryLLM(client, retryCfg)
	}
	return client, model
}

func (f *LLMFactory) createClientFromSub(sub *sqlite.LLMSubscription, model string) llm.LLM {
	if sub == nil || sub.BaseURL == "" || sub.APIKey == "" {
		return nil
	}
	maxTokens := sub.MaxOutputTokens
	if pm := sub.GetPerModelMaxTokens(model); pm > 0 {
		maxTokens = pm
	}
	f.mu.RLock()
	if f.globalMaxTokens > 0 {
		maxTokens = f.globalMaxTokens
	}
	f.mu.RUnlock()
	// Resolve per-model APIType override, fallback to subscription-level
	apiType := sub.APIType
	if pm := sub.GetPerModelAPIType(model); pm != "" {
		apiType = pm
	}
	cfg := &sqlite.UserLLMConfig{
		Provider: sub.Provider, BaseURL: sub.BaseURL, APIKey: sub.APIKey,
		Model: model, MaxOutputTokens: maxTokens, APIType: apiType,
		ID:             sub.ID,
		OnModelsLoaded: f.makeOnModelsLoaded(sub.ID),
	}
	client, _ := f.createClient(cfg)
	return client
}

// makeOnModelsLoaded returns a callback that persists a subscription's
// API-discovered model list to CachedModels. Runs in NewOpenAILLM's async
// goroutine, so it must be concurrency-safe and nil-guard the subscription
// (UpdateCachedModels nil-derefs if the sub no longer exists in DB).
func (f *LLMFactory) makeOnModelsLoaded(subID string) func([]string) {
	if f.subscriptionSvc == nil || subID == "" {
		return nil
	}
	return func(models []string) {
		// /models API returned a fresh model list — upsert each into
		// subscription_models so they show up in the picker. No more
		// CachedModels column writes.
		for _, m := range models {
			if m != "" {
				_ = f.subscriptionSvc.UpsertModel(subID, m, 0, 0, "", "")
			}
		}
	}
}

// ─── Model-first resolution (v38+) ───────────────────────
//
// This is the new authoritative resolution path. Per-session state lives in DB
// (tenants.subscription_id + model for per-session; user_default_model for the
// user-level default). Resolution reads DB on every call — no in-memory memo.
//
// LLM clients are cached per (subscription_id, apiType): one client serves all
// models of a subscription, with the model supplied per request. This is the
// correct granularity because credentials/base_url live on the subscription,
// not the model.
//
// These methods are additive alongside the legacy Switch*/Set*/Invalidate*
// matrix. The legacy matrix is removed once RPC + CLI migrate (later chunk).

// modelPerModelConfig holds the per-model overrides read from subscription_models.
type modelPerModelConfig struct {
	maxContext      int
	maxOutputTokens int
	thinkingMode    string
	apiType         string
	enabled         bool
	present         bool
}

// resolveModelConfig reads per-model config from the subscription_models table.
// Returns present=false when no row exists for (subID, model).
func (f *LLMFactory) resolveModelConfig(subID, model string) modelPerModelConfig {
	var c modelPerModelConfig
	if f.subscriptionSvc == nil || subID == "" || model == "" {
		return c
	}
	sm, err := f.subscriptionSvc.GetModel(subID, model)
	if err != nil || sm == nil {
		return c
	}
	c.present = true
	c.maxContext = sm.MaxContext
	c.maxOutputTokens = sm.MaxOutputTokens
	c.thinkingMode = sm.ThinkingMode
	c.apiType = sm.APIType
	c.enabled = sm.Enabled
	return c
}

// resolveSubContextFor is the (subID, model) variant of resolveSubContext,
// used by the new path which does not carry an llmEntry.
// Priority: subscription_models table → sub.PerModelConfigs (backward compat) → modelContexts.
func (f *LLMFactory) resolveSubContextFor(subID, model string) int {
	if sub := f.lookupSub(subID); sub != nil {
		if f.subscriptionSvc != nil && subID != "" {
			if sm, err := f.subscriptionSvc.GetModel(subID, model); err == nil && sm != nil && sm.MaxContext > 0 {
				return sm.MaxContext
			}
		}
		if v := sub.GetPerModelMaxContext(model); v > 0 {
			return v
		}
	}
	return f.resolveModelContext(model)
}

// getOrCreateClient returns a cached client for (subID, apiType) or builds one.
// The model is supplied per request, so the cache key excludes the model.
func (f *LLMFactory) getOrCreateClient(sub *sqlite.LLMSubscription, model string) llm.LLM {
	if sub == nil || sub.BaseURL == "" || sub.APIKey == "" {
		return nil
	}
	pmc := f.resolveModelConfig(sub.ID, model)
	apiType := pmc.apiType
	if apiType == "" {
		apiType = sub.APIType
	}
	key := clientCacheKey{subID: sub.ID, apiType: apiType}
	f.mu.RLock()
	if c, ok := f.clientCache[key]; ok && c != nil {
		f.mu.RUnlock()
		return c
	}
	f.mu.RUnlock()

	maxTokens := sub.MaxOutputTokens
	if pmc.maxOutputTokens > 0 {
		maxTokens = pmc.maxOutputTokens
	}
	f.mu.RLock()
	if f.globalMaxTokens > 0 {
		maxTokens = f.globalMaxTokens
	}
	f.mu.RUnlock()
	cfg := &sqlite.UserLLMConfig{
		ID: sub.ID, Provider: sub.Provider, BaseURL: sub.BaseURL, APIKey: sub.APIKey,
		Model: model, MaxOutputTokens: maxTokens, APIType: apiType,
		OnModelsLoaded: f.makeOnModelsLoaded(sub.ID),
	}
	client, _ := f.createClient(cfg)
	if client == nil {
		return nil
	}
	f.mu.Lock()
	// Another goroutine may have raced; keep the first cached.
	if existing, ok := f.clientCache[key]; ok && existing != nil {
		f.mu.Unlock()
		return existing
	}
	f.clientCache[key] = client
	f.mu.Unlock()
	return client
}

// ResolveLLM is the single authoritative resolution path for the agent loop.
// Returns (client, model, maxContext, thinkingMode, maxOutputTokens).
//
// Resolution order:
//  1. Per-session (channel, chatID) from tenants table
//  2. User-level default from user_default_model
//  3. System default (defaultLLM via GetLLM)
func (f *LLMFactory) ResolveLLM(senderID, chatID, channel string) (llm.LLM, string, int, string, int) {
	// ProxyLLM override (runner local LLM) — highest priority.
	if v, ok := f.proxyLLMs.Load(senderID); ok {
		if pe, ok := v.(proxyEntry); ok && pe.client != nil {
			return pe.client, pe.model, f.resolveModelContext(pe.model), "", 0
		}
	}
	subID, model := "", ""
	if chatID != "" && f.tenantSvc != nil {
		subID, model, _ = f.tenantSvc.GetTenantSubscription(channel, chatID)
	}
	if subID == "" && f.subscriptionSvc != nil {
		if udm, err := f.subscriptionSvc.GetUserDefaultModel(senderID); err == nil && udm != nil {
			subID = udm.SubscriptionID
			model = udm.Model
		}
	}
	if subID == "" {
		// Fall back to the legacy user-level default subscription, then system default.
		return f.GetLLM(senderID)
	}
	sub := f.lookupSub(subID)
	if sub == nil {
		return f.GetLLM(senderID)
	}
	if model == "" {
		return f.GetLLM(senderID)
	}
	client := f.getOrCreateClient(sub, model)
	if client == nil {
		return f.GetLLM(senderID)
	}
	pmc := f.resolveModelConfig(sub.ID, model)
	maxOut := pmc.maxOutputTokens
	if maxOut == 0 {
		maxOut = sub.MaxOutputTokens
	}
	// Thinking mode: per-model override (hidden, programmatic) → global user
	// setting (ScopeUser, the Ctrl+M toggle / /settings Select) → "" (auto).
	// sub.ThinkingMode is no longer read — thinking is global, not per-subscription.
	// The global setting is stored under a canonical channel (see
	// userThinkingMode) so one value applies regardless of which channel the
	// LLM call comes from.
	thinking := pmc.thinkingMode
	if thinking == "" {
		thinking = f.userThinkingMode(senderID)
	}
	return client, model, f.resolveSubContextFor(sub.ID, model), thinking, maxOut
}

// ResolveActiveSubModel returns the subscription and model the given session is
// currently using, mirroring ResolveLLM's resolution chain (tenants table →
// user_default_model → legacy GetDefault fallback) but WITHOUT
// materializing or caching an LLM client. It is the model-first way to identify
// a model — by the (subscription, model) pair — and replaces the legacy
// "default subscription" (GetDefault) lookup used by per-model config setters
// (max_context / max_output_tokens). Per-model config is model-scoped (stored
// under (subID, model)), so it must target the (sub, model) the user is
// actually interacting with in this session, not the user's default
// subscription's default model. When chatID is empty, resolution falls to user
// level (user_default_model, then legacy default subscription) — still
// model-first via the user_default_model table rather than subscriptions'
// is_default projection.
func (f *LLMFactory) ResolveActiveSubModel(senderID, chatID, channel string) (*sqlite.LLMSubscription, string, error) {
	if f.subscriptionSvc == nil {
		return nil, "", fmt.Errorf("ResolveActiveSubModel: subscription service unavailable")
	}
	subID, model := "", ""
	if chatID != "" && f.tenantSvc != nil {
		subID, model, _ = f.tenantSvc.GetTenantSubscription(channel, chatID)
	}
	if subID == "" {
		if udm, err := f.subscriptionSvc.GetUserDefaultModel(senderID); err == nil && udm != nil {
			subID = udm.SubscriptionID
			model = udm.Model
		}
	}
	// Legacy fallback when no session/user-level (sub,model) is bound: the
	// user's default subscription. This is the only path that still consults
	// GetDefault, kept for boot/first-run before any model is chosen.
	if subID == "" {
		sub, err := f.subscriptionSvc.GetDefault(senderID)
		if err != nil {
			return nil, "", fmt.Errorf("ResolveActiveSubModel: get default subscription: %w", err)
		}
		if sub == nil {
			return nil, "", fmt.Errorf("ResolveActiveSubModel: no active subscription for user %s", senderID)
		}
		return sub, model, nil
	}
	sub := f.lookupSub(subID)
	if sub == nil {
		return nil, "", fmt.Errorf("ResolveActiveSubModel: subscription %s not found", subID)
	}
	if model == "" {
		return sub, "", nil // model will be resolved by caller via user_default_model
	}
	return sub, model, nil
}

// SelectModel sets the per-session (subscription, model) for a chat and
// persists it to the tenants table. Validates the model is enabled when a
// subscription_models row exists for it. Invalidates the session memo so the
// next ResolveLLM re-reads DB.
func (f *LLMFactory) SelectModel(senderID, chatID, channel, subID, model string) error {
	if f.subscriptionSvc == nil {
		return fmt.Errorf("SelectModel: subscription service unavailable")
	}
	if subID == "" || model == "" {
		return fmt.Errorf("SelectModel: subID and model are required")
	}
	sub, err := f.subscriptionSvc.Get(subID)
	if err != nil || sub == nil {
		return fmt.Errorf("SelectModel: subscription %s not found", subID)
	}
	if !sub.Enabled {
		return fmt.Errorf("SelectModel: subscription %s is disabled", subID)
	}
	if sm, gerr := f.subscriptionSvc.GetModel(subID, model); gerr == nil && sm != nil && !sm.Enabled {
		return fmt.Errorf("SelectModel: model %q is disabled for subscription %s", model, subID)
	}
	if f.tenantSvc != nil && chatID != "" {
		if err := f.tenantSvc.SetTenantSubscription(channel, chatID, subID, model); err != nil {
			return fmt.Errorf("SelectModel: persist tenant: %w", err)
		}
	}
	// Update "last used model" (user_default_model repurposed) so new sessions
	// inherit this (sub, model) pair. This is NOT "setting a default subscription" —
	// the table now serves as last-used-model storage for session inheritance.
	if err := f.subscriptionSvc.SetUserDefaultModel(senderID, subID, model); err != nil {
		// Non-fatal: log and continue. New sessions will fall back to system subscription.
		log.WithError(err).Warn("SelectModel: failed to update last-used model")
	}
	return nil
}

// ResolveSubscriptionForModel finds the subscription that provides the given
// model for a user. This is the model-first inverse of "which models does this
// subscription serve": given a model name picked from the unified model list,
// return the subscription whose endpoint actually serves it, so the agent pairs
// the right credentials with the model name.
//
// Search order (first match wins, system subscription preferred when tied):
//  1. subscription_models rows with Enabled=true for each subscription.
//  2. Each subscription's CachedModels (API-discovered list) and sub.Model.
//
// Disabled subscription_models rows are skipped in pass 1. Pass 2 does not
// consult subscription_models because CachedModels/sub.Model predate the
// enable flag and are only a fallback for models not yet registered as rows.
// Returns an error if no subscription provides the model.
func (f *LLMFactory) ResolveSubscriptionForModel(senderID, model string) (*sqlite.LLMSubscription, error) {
	if f.subscriptionSvc == nil {
		return nil, fmt.Errorf("ResolveSubscriptionForModel: subscription service unavailable")
	}
	if model == "" {
		return nil, fmt.Errorf("ResolveSubscriptionForModel: model is required")
	}
	subs, err := f.subscriptionSvc.List(senderID)
	if err != nil {
		return nil, fmt.Errorf("ResolveSubscriptionForModel: list: %w", err)
	}
	if len(subs) == 0 {
		return nil, fmt.Errorf("ResolveSubscriptionForModel: no subscriptions for %s", senderID)
	}
	// find returns the subscription for the model, preferring the system subscription.
	// matchFn reports whether a subscription provides the model.
	find := func(matchFn func(*sqlite.LLMSubscription) bool) *sqlite.LLMSubscription {
		var fallback *sqlite.LLMSubscription
		for i := range subs {
			sub := subs[i]
			if !sub.Enabled {
				continue // disabled subscription cannot own a selectable model
			}
			if !matchFn(sub) {
				continue
			}
			// No "default subscription" concept — prefer system subscription,
			// then first-enabled by creation order.
			if sub.IsSystem {
				return sub
			}
			if fallback == nil {
				fallback = sub
			}
		}
		return fallback
	}
	// Pass 1: enabled subscription_models rows.
	if owner := find(func(sub *sqlite.LLMSubscription) bool {
		models, gerr := f.subscriptionSvc.GetModels(sub.ID)
		if gerr != nil || len(models) == 0 {
			return false
		}
		for _, sm := range models {
			if sm.Model == model && sm.Enabled {
				return true
			}
		}
		return false
	}); owner != nil {
		return owner, nil
	}
	return nil, fmt.Errorf("ResolveSubscriptionForModel: no subscription provides model %q", model)
}

// SetUserDefaultModel sets the user-level default (subscription, model) used for
// new sessions. Persists to user_default_model.
func (f *LLMFactory) SetUserDefaultModel(senderID, subID, model string) error {
	if f.subscriptionSvc == nil {
		return fmt.Errorf("SetUserDefaultModel: subscription service unavailable")
	}
	if subID == "" {
		return fmt.Errorf("SetUserDefaultModel: subID is required")
	}
	sub, err := f.subscriptionSvc.Get(subID)
	if err != nil || sub == nil {
		return fmt.Errorf("SetUserDefaultModel: subscription %s not found", subID)
	}
	if model != "" {
		if sm, gerr := f.subscriptionSvc.GetModel(subID, model); gerr == nil && sm != nil && !sm.Enabled {
			return fmt.Errorf("SetUserDefaultModel: model %q is disabled", model)
		}
	}
	if err := f.subscriptionSvc.SetUserDefaultModel(senderID, subID, model); err != nil {
		return fmt.Errorf("SetUserDefaultModel: persist: %w", err)
	}
	return nil
}

// SetModelEnabled toggles a model's enabled flag and invalidates any cached
// state for its subscription so resolution picks up the change.
func (f *LLMFactory) SetModelEnabled(subID, model string, enabled bool) error {
	if f.subscriptionSvc == nil {
		return fmt.Errorf("SetModelEnabled: subscription service unavailable")
	}
	if err := f.subscriptionSvc.SetModelEnabled(subID, model, enabled); err != nil {
		return err
	}
	f.InvalidateSubscription(subID)
	return nil
}

// SetSubscriptionEnabled toggles a subscription's enabled flag (v40). Disabling a
// subscription removes all its models from the picker and prevents it from being
// resolved as a model's owner; the credentials and per-model config are preserved
// so re-enabling is lossless. Invalidates the client cache and session memos.
func (f *LLMFactory) SetSubscriptionEnabled(subID string, enabled bool) error {
	if f.subscriptionSvc == nil {
		return fmt.Errorf("SetSubscriptionEnabled: subscription service unavailable")
	}
	if err := f.subscriptionSvc.SetSubscriptionEnabled(subID, enabled); err != nil {
		return err
	}
	f.InvalidateSubscription(subID)
	return nil
}

// InvalidateSubscription drops the client cache entries referencing a
// subscription. Called when a subscription's credentials/config change or one
// of its models is toggled.
func (f *LLMFactory) InvalidateSubscription(subID string) {
	if subID == "" {
		return
	}
	f.mu.Lock()
	for k := range f.clientCache {
		if k.subID == subID {
			delete(f.clientCache, k)
		}
	}
	f.mu.Unlock()
}

// ─── Model listing & SubAgent resolution ─────────────────

func (f *LLMFactory) ListModels() []string { return f.defaultLLM.ListModels() }

func (f *LLMFactory) ListAllModelsForUser(senderID string) []string {
	entries := f.listModelEntriesCore(senderID, false)
	result := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if seen[e.Model] {
			continue
		}
		seen[e.Model] = true
		result = append(result, e.Model)
	}
	return result
}

// ListAllModelEntriesForUser returns every (subscription, model) pair the picker
// should show for a user, paired with availability Status. The list is DB-driven
// (CachedModels ∪ sub.Model ∪ subscription_models rows) and INCLUDES
// disabled/offline models so the picker can render them with their status tag.
// The same model name served by multiple subscriptions is listed once per
// subscription (e.g. "system · deepseek-v4-pro" and "deepseek ·
// deepseek-v4-pro" both appear) so the user can pick the exact subscription;
// within a single subscription a model name is emitted at most once.
func (f *LLMFactory) ListAllModelEntriesForUser(senderID string) []protocol.ModelEntry {
	return f.listModelEntriesCore(senderID, true)
}

// listModelEntriesCore is the shared DB-driven list builder.
//   - includeDisabled=true: emit every (subscription, model) pair (normal/offline/disabled)
//     for the picker — same model name served by different subscriptions is listed
//     once per subscription, NOT deduped by model name;
//   - includeDisabled=false: emit only selectable models (normal + offline; skip
//     disabled) — used by tier selectors and other selectable-model callers.
//
// Within a single subscription, a model name is emitted at most once (CachedModels,
// sub.Model, and subscription_models rows are merged). Disabled subscriptions
// contribute nothing. Emitted in subscription order (stable), system subscription
// first (it is injected at the head of the list by the storage layer).
func (f *LLMFactory) listModelEntriesCore(senderID string, includeDisabled bool) []protocol.ModelEntry {
	seen := make(map[string]bool)
	var result []protocol.ModelEntry
	add := func(subID, subName, model, status string) {
		if model == "" {
			return
		}
		key := subID + "\x00" + model
		if seen[key] {
			return
		}
		seen[key] = true
		result = append(result, protocol.ModelEntry{SubID: subID, SubName: subName, Model: model, Status: status})
	}
	const systemModelLabel = "system"
	if f.subscriptionSvc == nil {
		for _, m := range f.defaultLLM.ListModels() {
			add("", systemModelLabel, m, "normal")
		}
		return result
	}
	var subs []*sqlite.LLMSubscription
	var err error
	if senderID != "" {
		subs, err = f.subscriptionSvc.List(senderID)
	} else {
		subs, err = f.subscriptionSvc.ListAll()
	}
	if err != nil || len(subs) == 0 {
		return result
	}

	// Only enabled subscriptions contribute. Models come from subscription_models
	// table + sub.Model. No CachedModels — all models treated equally.
	type subInfo struct {
		sub   *sqlite.LLMSubscription
		rows  []*sqlite.SubscriptionModel
		rowEn map[string]bool // model → enabled flag from row (absent ⇒ true)
	}
	infos := make([]subInfo, 0, len(subs))
	for _, sub := range subs {
		if !sub.Enabled {
			continue
		}
		rows, _ := f.subscriptionSvc.GetModels(sub.ID)
		rowEn := make(map[string]bool, len(rows))
		for _, r := range rows {
			rowEn[r.Model] = r.Enabled
		}
		infos = append(infos, subInfo{sub: sub, rows: rows, rowEn: rowEn})
	}
	// statusOf: only "normal" or "disabled". No "offline".
	statusOf := func(i int, m string) string {
		info := infos[i]
		if en, ok := info.rowEn[m]; ok && !en {
			return "disabled"
		}
		return "normal"
	}
	candidates := func(i int) []string {
		s := infos[i]
		set := make(map[string]bool)
		for _, r := range s.rows {
			if r.Model != "" {
				set[r.Model] = true
			}
		}
		cs := make([]string, 0, len(set))
		for m := range set {
			cs = append(cs, m)
		}
		sort.Strings(cs)
		return cs
	}
	for i := range infos {
		for _, m := range candidates(i) {
			if m == "" {
				continue
			}
			st := statusOf(i, m)
			if !includeDisabled && st == "disabled" {
				continue
			}
			add(infos[i].sub.ID, infos[i].sub.Name, m, st)
		}
	}
	return result
}

// RefreshResult records the outcome of refreshing one subscription's model list.
// Surfaced to the user via /models so they can see WHY a subscription's models
// are missing — previously the failure was Warn-level log only, invisible in chat.
type RefreshResult struct {
	SubName    string // display name (may be empty for system sub)
	SubID      string
	IsSystem   bool
	Status     string // "ok" | "fail" | "skipped" | "noloader" | "noclient"
	ModelCount int    // models fetched (ok) or already cached (skipped)
	Error      string // short failure cause (fail only)
}

// RefreshModelEntriesForUser live-fetches /models for every enabled subscription
// (parallel, capped concurrency, per-sub timeout), persists results to
// CachedModels via the OnModelsLoaded callback, then returns the fresh entry
// list. Subscriptions that error keep their existing CachedModels (soft fail).
// Used by the model picker so the list reflects each provider's true available
// models, not just the persisted snapshot.
//
// Returns only the entries for backward compatibility with the RPC handler
// and TUI picker. Callers that need per-subscription refresh status (e.g.
// /models command output) should call RefreshModelEntriesForUserWithResults.
func (f *LLMFactory) RefreshModelEntriesForUser(senderID string) []protocol.ModelEntry {
	entries, _ := f.RefreshModelEntriesForUserWithResults(senderID)
	return entries
}

// RefreshModelEntriesForUserWithResults is the extended variant that also
// returns per-subscription refresh outcomes. Used by /models so the user can
// see which subscriptions refreshed successfully and which failed (and why),
// rather than silently missing models with no explanation.
func (f *LLMFactory) RefreshModelEntriesForUserWithResults(senderID string) ([]protocol.ModelEntry, []RefreshResult) {
	if f.subscriptionSvc == nil {
		return f.ListAllModelEntriesForUser(senderID), nil
	}
	var subs []*sqlite.LLMSubscription
	var err error
	if senderID != "" {
		subs, err = f.subscriptionSvc.List(senderID)
	} else {
		subs, err = f.subscriptionSvc.ListAll()
	}
	if err != nil {
		return f.ListAllModelEntriesForUser(senderID), nil
	}

	// Collect results keyed by sub ID so the summary is stable regardless of
	// goroutine completion order. Preserves subscription list order.
	results := make([]RefreshResult, 0, len(subs))
	resultByID := make(map[string]*RefreshResult, len(subs))
	for _, sub := range subs {
		r := RefreshResult{SubName: sub.Name, SubID: sub.ID, IsSystem: sub.IsSystem}
		switch {
		case !sub.Enabled:
			r.Status = "skipped"
		case sub.BaseURL == "" || sub.APIKey == "":
			r.Status = "skipped"
			r.Error = "missing base_url or api_key"
			log.WithFields(log.Fields{"sub": sub.Name, "has_baseurl": sub.BaseURL != "", "has_apikey": sub.APIKey != ""}).Debug("[LLM] RefreshModelEntries: skipping sub (missing base_url or api_key)")
		default:
			// placeholder; filled in by the goroutine below.
			r.Status = "pending"
		}
		results = append(results, r)
		resultByID[sub.ID] = &results[len(results)-1]
	}

	const concurrency = 8
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, sub := range subs {
		if !sub.Enabled || sub.BaseURL == "" || sub.APIKey == "" {
			continue
		}
		wg.Add(1)
		go func(s *sqlite.LLMSubscription) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := resultByID[s.ID]
			// /models fetch only needs credentials — model name is irrelevant.
			client := f.createClientFromSub(s, "")
			if client == nil {
				r.Status = "noclient"
				r.Error = "无法创建 LLM 客户端（检查 provider/base_url）"
				return
			}
			loader, ok := client.(llm.ModelLoader)
			if !ok {
				r.Status = "noloader"
				// Anthropic etc. don't expose /models; not an error, just unsupported.
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			if err := loader.LoadModelsFromAPI(ctx); err != nil {
				r.Status = "fail"
				r.Error = truncateErrMsg(err.Error())
				log.WithFields(log.Fields{"sub": s.Name, "base_url": s.BaseURL, "has_apikey": s.APIKey != "", "err": err.Error()}).Warn("[LLM] RefreshModelEntries: /models fetch failed")
				return
			}
			// Re-read to count subscription_models rows (OnModelsLoaded upserts them).
			r.Status = "ok"
			if models, gerr := f.subscriptionSvc.GetModels(s.ID); gerr == nil {
				r.ModelCount = len(models)
			}
		}(sub)
	}
	wg.Wait()
	return f.ListAllModelEntriesForUser(senderID), results
}

// truncateErrMsg shortens an error message for user-facing display. Long SDK
// errors (HTTP body dumps, stack traces) would flood the chat output.
func truncateErrMsg(msg string) string {
	const max = 120
	if len(msg) <= max {
		return msg
	}
	return msg[:max] + "..."
}

// GetLLMForModel returns (client, model, maxContext, thinkingMode, maxOutputTokens, usedCustom).
// All subscription-derived values come from a single subscription, guaranteeing consistency.
// Used by SubAgent when a role specifies a model (or tier name like vanguard/balance/swift).
func (f *LLMFactory) GetLLMForModel(senderID, targetModel string) (llm.LLM, string, int, string, int, bool) {
	subID, resolvedModel, fromTier := f.resolveTierModel(senderID, targetModel)
	if resolvedModel == "" {
		client, model, maxCtx, tm, maxOut := f.GetLLM(senderID)
		return client, model, maxCtx, tm, maxOut, false
	}

	// When tier config provides an explicit subID (new "subID|model" format),
	// look up the subscription directly — no model→subscription resolution.
	if fromTier && subID != "" {
		if f.subscriptionSvc != nil {
			sub, err := f.subscriptionSvc.Get(subID)
			if err == nil && sub != nil && sub.Enabled {
				client := f.createClientFromSub(sub, resolvedModel)
				if client != nil {
					log.WithFields(log.Fields{"model": resolvedModel, "sub": sub.Name, "source": "tier-subid"}).Info("[LLM] GetLLMForModel: exact match via subID")
					return client, resolvedModel, f.resolveEffectiveContext(resolvedModel, sub.ID), sub.ThinkingMode, sub.MaxOutputTokens, true
				}
			}
		}
		// subID provided but subscription not found/unavailable — log and
		// fall through to model-name lookup as a safety net.
		log.WithFields(log.Fields{"subID": subID, "model": resolvedModel}).Warn("[LLM] GetLLMForModel: subID not found, falling back to model-name lookup")
	}

	modelMap := f.buildModelSubscriptionMap(senderID)
	if sub, ok := modelMap[resolvedModel]; ok {
		client := f.createClientFromSub(sub, resolvedModel)
		if client != nil {
			source := "direct"
			if fromTier {
				source = "tier-exact"
			}
			log.WithFields(log.Fields{"model": resolvedModel, "sub": sub.Name, "source": source}).Info("[LLM] GetLLMForModel: exact match")
			return client, resolvedModel, f.resolveEffectiveContext(resolvedModel, sub.ID), sub.ThinkingMode, sub.MaxOutputTokens, true
		}
	}

	f.mu.RLock()
	getConfigSubs := f.configSubsFn
	f.mu.RUnlock()
	if getConfigSubs != nil {
		for _, cs := range getConfigSubs() {
			if cs.BaseURL == "" || cs.APIKey == "" || cs.Model != resolvedModel {
				continue
			}
			sub := configSubToLLMSubscription(cs)
			client := f.createClientFromSub(sub, resolvedModel)
			if client != nil {
				log.WithFields(log.Fields{"model": resolvedModel, "sub": cs.Name, "source": "config-exact"}).Info("[LLM] GetLLMForModel: config sub exact match")
				return client, resolvedModel, f.resolveEffectiveContext(resolvedModel, sub.ID), sub.ThinkingMode, sub.MaxOutputTokens, true
			}
		}
	}

	if f.subscriptionSvc != nil && senderID != "" {
		subs, err := f.subscriptionSvc.List(senderID)
		if err == nil {
			for _, sub := range subs {
				if sub.BaseURL == "" || sub.APIKey == "" {
					continue
				}
				// Check if subscription already has models registered.
				models, _ := f.subscriptionSvc.GetModels(sub.ID)
				if len(models) > 0 {
					continue
				}
				client := f.createClientFromSub(sub, resolvedModel)
				if client == nil {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				if loader, ok := client.(llm.ModelLoader); ok {
					_ = loader.LoadModelsFromAPI(ctx)
				}
				cancel()
				// After API load, OnModelsLoaded upserts into subscription_models.
				// Check if the target model is now registered.
				updatedModels, _ := f.subscriptionSvc.GetModels(sub.ID)
				for _, sm := range updatedModels {
					if sm.Model == resolvedModel {
						log.WithFields(log.Fields{"model": resolvedModel, "sub": sub.Name, "source": "api-load"}).Info("[LLM] GetLLMForModel: found after API load")
						return client, resolvedModel, f.resolveEffectiveContext(resolvedModel, sub.ID), sub.ThinkingMode, sub.MaxOutputTokens, true
					}
				}
			}
		}
	}

	// No subscription for the resolved model. Try using any available
	// subscription with the resolved model as the requested model name.
	// OpenAI-compatible endpoints can serve arbitrary model names even if
	// they're not in cached_models. This prevents the tier system from
	// silently falling back to the parent's model and confusing the user.
	f.mu.RLock()
	getConfigSubs2 := f.configSubsFn
	f.mu.RUnlock()
	if getConfigSubs2 != nil {
		for _, cs := range getConfigSubs2() {
			if cs.BaseURL == "" || cs.APIKey == "" {
				continue
			}
			sub := configSubToLLMSubscription(cs)
			client := f.createClientFromSub(sub, resolvedModel)
			if client != nil {
				log.WithFields(log.Fields{"model": resolvedModel, "sub": cs.Name, "source": "tier-fallback-config"}).Info("[LLM] GetLLMForModel: using config subscription with resolved model")
				return client, resolvedModel, f.resolveEffectiveContext(resolvedModel, sub.ID), sub.ThinkingMode, sub.MaxOutputTokens, true
			}
		}
	}
	if f.subscriptionSvc != nil && senderID != "" {
		subs, err := f.subscriptionSvc.List(senderID)
		if err == nil {
			for _, sub := range subs {
				if sub.BaseURL == "" || sub.APIKey == "" {
					continue
				}
				client := f.createClientFromSub(sub, resolvedModel)
				if client != nil {
					log.WithFields(log.Fields{"model": resolvedModel, "sub": sub.Name, "source": "tier-fallback-sub"}).Info("[LLM] GetLLMForModel: using subscription with resolved model")
					return client, resolvedModel, f.resolveEffectiveContext(resolvedModel, sub.ID), sub.ThinkingMode, sub.MaxOutputTokens, true
				}
			}
		}
	}

	// Last resort: use parent LLM but keep the resolved model name so the
	// TUI status bar shows what was requested, not the fallback model.
	log.WithFields(log.Fields{"model": resolvedModel, "tier": fromTier}).Warn("[LLM] GetLLMForModel: not found, using parent LLM with resolved model name")
	client, _, maxCtx, tm, maxOut := f.GetLLM(senderID)
	return client, resolvedModel, maxCtx, tm, maxOut, false
}

func (f *LLMFactory) buildModelSubscriptionMap(senderID string) map[string]*sqlite.LLMSubscription {
	m := make(map[string]*sqlite.LLMSubscription)

	f.mu.RLock()
	getConfigSubs := f.configSubsFn
	f.mu.RUnlock()
	if getConfigSubs != nil {
		for _, cs := range getConfigSubs() {
			if cs.BaseURL == "" || cs.APIKey == "" {
				continue
			}
			sub := configSubToLLMSubscription(cs)
			if sub.Model != "" {
				if _, exists := m[sub.Model]; !exists {
					m[sub.Model] = sub
				}
			}
		}
	}

	if f.subscriptionSvc != nil && senderID != "" {
		subs, err := f.subscriptionSvc.List(senderID)
		if err == nil && len(subs) > 0 {
			for _, sub := range subs {
				if sub.BaseURL == "" || sub.APIKey == "" {
					continue
				}
				models, _ := f.subscriptionSvc.GetModels(sub.ID)
				for _, sm := range models {
					if _, exists := m[sm.Model]; !exists {
						m[sm.Model] = sub
					}
				}
			}
		}
	}
	return m
}

func configSubToLLMSubscription(cs config.SubscriptionConfig) *sqlite.LLMSubscription {
	sub := &sqlite.LLMSubscription{
		ID: cs.ID, Name: cs.Name, Provider: cs.Provider,
		BaseURL: cs.BaseURL, APIKey: cs.APIKey, Model: cs.Model,
		MaxOutputTokens: cs.MaxOutputTokens, ThinkingMode: cs.ThinkingMode,
	}
	sub.PerModelConfigs = cs.PerModelConfigs
	return sub
}

// ─── Tier resolution ─────────────────────────────────────

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

func (f *LLMFactory) resolveTierModel(senderID, value string) (subID, model string, fromTier bool) {
	tier := normalizeModelTier(value)
	if tier == "" {
		return "", value, false
	}
	// Tier config is per-user, stored in user_settings DB (same pattern as
	// thinking_mode). Key: "tier_vanguard" / "tier_balance" / "tier_swift".
	// Value: "subID|model" or legacy plain "model".
	raw := f.userTierModel(senderID, tier)
	if raw != "" {
		s, m := parseTierValue(raw)
		return s, m, true
	}
	fallback := ""
	switch tier {
	case "swift", "vanguard":
		fallback = "balance"
	case "balance":
		fallback = "vanguard"
	}
	if fallback != "" {
		raw = f.userTierModel(senderID, fallback)
		if raw != "" {
			s, m := parseTierValue(raw)
			return s, m, true
		}
	}
	return "", "", true
}

// userTierModel returns the per-user tier model setting from user_settings DB.
// Value is "subID|model" or legacy plain "model". Returns "" when unset.
func (f *LLMFactory) userTierModel(senderID, tier string) string {
	return f.getSetting(senderID, thinkingModeChannel, "tier_"+tier)
}

// parseTierValue splits a tier config value into (subID, model).
// Supports both "subID|model" (new format) and plain "model" (legacy).
func parseTierValue(s string) (subID, model string) {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "|"); idx >= 0 {
		return s[:idx], s[idx+1:]
	}
	return "", s
}

// guessProvider 根据模型名猜测 provider。
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

// ─── Concurrency settings ────────────────────────────────

// Setting keys used by LLMFactory for concurrency control.
// Must match keys stored in user_settings DB (written by settings panel).
const (
	settingMaxConcurrency           = "max_concurrency" // channel.SettingMaxConcurrency
	settingSubAgentMaxConcurrency   = "subagent_max_concurrency"
	settingLLMMaxConcurrentPersonal = "llm_max_concurrent_personal"
)

func (f *LLMFactory) GetLLMConcurrency(senderID string) int {
	if f.settingsSvc == nil {
		return llm.DefaultLLMConcurrencyPersonal
	}
	settings, err := f.settingsSvc.GetSettings("feishu", senderID)
	if err != nil || settings == nil {
		return llm.DefaultLLMConcurrencyPersonal
	}
	return parseOrDefault(settings[settingLLMMaxConcurrentPersonal], llm.DefaultLLMConcurrencyPersonal)
}

func (f *LLMFactory) SetLLMConcurrency(senderID string, personal int) error {
	if f.settingsSvc == nil {
		return ErrSettingsUnavailable
	}
	return f.settingsSvc.SetSetting("feishu", senderID, settingLLMMaxConcurrentPersonal, fmt.Sprintf("%d", personal))
}

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

func (f *LLMFactory) LLMSemAcquireForUser(senderID, channel string) func(context.Context) func() {
	if f.llmSemManager == nil {
		return nil
	}
	llmKey := "global"
	if f.HasCustomLLM(senderID) {
		llmKey = "personal"
	}
	return func(ctx context.Context) func() {
		personalCap := f.GetLLMConcurrency(senderID)
		// Resolution order: user DB max_concurrent (applies to both
		// global and personal keys) → personal-specific → hardcoded default.
		// This ensures the user's single max_concurrency knob controls
		// ALL LLM calls regardless of whether they use shared or personal LLM.
		cap := parseOrDefault(f.getSetting(senderID, channel, settingMaxConcurrency), -1)
		if cap <= 0 {
			if llmKey == "personal" {
				cap = personalCap
			} else {
				cap = llm.DefaultLLMConcurrency
			}
		}
		log.WithFields(log.Fields{
			"sender":  senderID,
			"channel": channel,
			"llmKey":  llmKey,
			"cap":     cap,
			"dbVal":   f.getSetting(senderID, channel, settingMaxConcurrency),
		}).Debug("LLMSemAcquireForUser: resolved capacity")
		return f.llmSemManager.Acquire(ctx, senderID, llmKey, func() int { return cap })
	}
}

func (f *LLMFactory) SubAgentSemAcquireForUser(senderID, channel string) func(context.Context) func() {
	if f.llmSemManager == nil {
		return nil
	}
	return func(ctx context.Context) func() {
		cap := parseOrDefault(f.getSetting(senderID, channel, settingSubAgentMaxConcurrency), -1)
		if cap < 0 {
			cap = parseOrDefault(f.getSetting(senderID, channel, settingMaxConcurrency), llm.DefaultLLMConcurrency)
		}
		return f.llmSemManager.Acquire(ctx, senderID, "subagent", func() int { return cap })
	}
}

func (f *LLMFactory) getSetting(senderID, channel, key string) string {
	if f.settingsSvc == nil {
		return ""
	}
	settings, err := f.settingsSvc.GetSettings(channel, senderID)
	if err != nil || settings == nil {
		return ""
	}
	return settings[key]
}

// thinkingModeChannel is the canonical channel under which the global
// thinking_mode user setting is stored. The CLI settings panel and Ctrl+M
// toggle both write here, and ResolveLLM reads here regardless of the actual
// call channel — making thinking a single per-user value.
//
// The constant itself lives in the channel package (channel.ThinkingModeChannel)
// to avoid an import cycle: channel/cli and agent both need it, and agent
// already imports channel while channel/cli cannot import agent.
const thinkingModeChannel = channel.ThinkingModeChannel

// userThinkingMode returns the global thinking_mode user setting for a sender
// (the Ctrl+M toggle / /settings Select), stored under the canonical channel.
// Returns "" (auto) when unset or the settings service is unavailable.
// Per-model overrides still win above this; sub.ThinkingMode is no longer
// consulted.
func (f *LLMFactory) userThinkingMode(senderID string) string {
	return f.getSetting(senderID, thinkingModeChannel, "thinking_mode")
}

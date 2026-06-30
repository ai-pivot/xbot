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

// llmEntry bundles ALL per-key LLM state into a single struct.
// This eliminates the "partial write" class of bugs where methods updated
// some maps but forgot others (e.g. SetChatLLM not writing subscriptions).
//
// Every write method must create a complete llmEntry — it is impossible
// to have an entry with a client but no subscription ID.
//
// subID is the authoritative subscription identifier. resolveSub(entry) reads
// subscription data from DB via subID, falling back to the sub cache when DB
// is unavailable (tests, startup races). This prevents model-from-sub-A +
// config-from-sub-B cross-contamination: SwitchModel copies subID + sub
// from the user entry, but resolveSub always queries DB first by subID.
type llmEntry struct {
	client          llm.LLM
	model           string
	subID           string                  // authoritative subscription identity
	sub             *sqlite.LLMSubscription // cache (fallback when DB unavailable)
	maxOutputTokens int
	thinkingMode    string
}

// LLMFactory 管理用户自定义 LLM 客户端的创建和缓存。
//
// 设计原则：
//   - 所有 per-user/per-chat 的 LLM 状态存储在单个 `entries` map 中
//   - 每次写入必须提供完整的 llmEntry（client + model + sub + tokens + thinking）
//   - 读取通过 getEntry() 获取完整 entry，从中派生所有值
//   - per-chat max_context override 存在独立的 perChatMaxCtx 中（key=chatID）
type LLMFactory struct {
	subscriptionSvc *sqlite.LLMSubscriptionService
	tenantSvc       *sqlite.TenantService // for per-session model restoration from DB
	configSubsFn    func() []config.SubscriptionConfig
	settingsSvc     *SettingsService

	// Global defaults (no per-user override)
	defaultLLM          llm.LLM
	defaultModel        string
	defaultThinkingMode string
	tierModels          config.LLMConfig
	retryConfig         llm.RetryConfig
	globalMaxTokens     int

	// model name → max context tokens (from config model_contexts, not per-user)
	modelContexts map[string]int

	// Single source of truth for per-user and per-chat LLM state.
	// Key = senderID (user-level) or chatKey(senderID, chatID) (per-chat).
	entries map[string]*llmEntry

	// Per-chat max_context override (user explicitly set in /settings).
	// Key = chatID only (not senderID:chatID), because max_context is
	// session-scoped, not subscription-scoped.
	perChatMaxCtx map[string]int

	// ─── Model-first resolution (v38+) ───────────────────────
	// clientCache shares one LLM client per (subscription, apiType). A client is
	// reusable across models of the same subscription — the model is supplied
	// per request — so we cache at the subscription level, not the model level.
	clientCache map[clientCacheKey]llm.LLM
	// sessionMemo caches the per-session resolution (subID, model) read from DB
	// (tenants + user_default_model). It is a pure memo of DB state: every write
	// (SelectModel / SetUserDefaultModel / InvalidateSubscription) invalidates
	// the affected keys, so the memo can never diverge from DB.
	sessionMemo map[string]*resolvedEntry

	mu                sync.RWMutex
	llmSemManager     *llm.LLMSemaphoreManager
	hasCustomLLMCache sync.Map
}

// clientCacheKey identifies a shared LLM client by subscription + API type.
type clientCacheKey struct {
	subID   string
	apiType string
}

// resolvedEntry is the memoized per-session resolution: which subscription +
// model this session uses, plus the materialized client and derived config.
type resolvedEntry struct {
	subID           string
	model           string
	maxOutputTokens int
	thinkingMode    string
	client          llm.LLM
}

// NewLLMFactory 创建 LLM 工厂
func NewLLMFactory(defaultLLM llm.LLM, defaultModel string) *LLMFactory {
	return &LLMFactory{
		defaultLLM:    defaultLLM,
		defaultModel:  defaultModel,
		entries:       make(map[string]*llmEntry),
		modelContexts: make(map[string]int),
		perChatMaxCtx: make(map[string]int),
		clientCache:   make(map[clientCacheKey]llm.LLM),
		sessionMemo:   make(map[string]*resolvedEntry),
	}
}

// ─── Getters ─────────────────────────────────────────────

func (f *LLMFactory) getEntry(key string) *llmEntry {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.entries[key]
}

func (f *LLMFactory) setEntry(key string, e *llmEntry) {
	f.mu.Lock()
	f.entries[key] = e
	f.mu.Unlock()
}

// GetDefaultModel returns the default model name.
func (f *LLMFactory) GetDefaultModel() string { return f.defaultModel }

// GetSubscriptionSvc returns the subscription service.
func (f *LLMFactory) GetSubscriptionSvc() *sqlite.LLMSubscriptionService {
	return f.subscriptionSvc
}

// ─── Configuration setters ───────────────────────────────

func (f *LLMFactory) SetModelTiers(cfg config.LLMConfig) {
	f.mu.Lock()
	f.tierModels = cfg
	f.mu.Unlock()
}

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

// resolveSub returns the subscription for an entry. DB (by subID) takes priority;
// falls back to the cached sub pointer when the subscription service is unavailable.
func (f *LLMFactory) resolveSub(e *llmEntry) *sqlite.LLMSubscription {
	if sub := f.lookupSub(e.subID); sub != nil {
		return sub
	}
	return e.sub
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

// resolveSubContext resolves max context using an llmEntry's subscription.
// Priority: subscription_models table (v35+) → sub.PerModelConfigs (backward compat) → modelContexts.
func (f *LLMFactory) resolveSubContext(model string, e *llmEntry) int {
	if sub := f.resolveSub(e); sub != nil {
		// 1. Check subscription_models (authoritative for v35+)
		if f.subscriptionSvc != nil && e.subID != "" {
			if sm, err := f.subscriptionSvc.GetModel(e.subID, model); err == nil && sm != nil && sm.MaxContext > 0 {
				return sm.MaxContext
			}
		}
		// 2. Fall back to PerModelConfigs (backward compat, pre-v35)
		if v := sub.GetPerModelMaxContext(model); v > 0 {
			return v
		}
	}
	return f.resolveModelContext(model)
}

// GetEffectiveMaxContext is the single source of truth for "what max context should the UI show?".
// Priority: per-chat override → per-model sub config → global model_contexts → 0.
func (f *LLMFactory) GetEffectiveMaxContext(senderID, chatID string) int {
	if chatID != "" {
		f.mu.RLock()
		if v, ok := f.perChatMaxCtx[chatID]; ok && v > 0 {
			f.mu.RUnlock()
			return v
		}
		f.mu.RUnlock()
	}
	key := senderID
	if chatID != "" {
		key = chatKey(senderID, chatID)
	}
	if e := f.getEntry(key); e != nil {
		if mc := f.resolveSubContext(e.model, e); mc > 0 {
			return mc
		}
	}
	return 0
}

// ─── Per-chat max context ────────────────────────────────

func (f *LLMFactory) SetPerChatMaxContext(chatID string, maxCtx int) {
	f.mu.Lock()
	if maxCtx > 0 {
		f.perChatMaxCtx[chatID] = maxCtx
	} else {
		delete(f.perChatMaxCtx, chatID)
	}
	f.mu.Unlock()
}

func (f *LLMFactory) GetPerChatMaxContext(chatID string) int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.perChatMaxCtx[chatID]
}

func (f *LLMFactory) ClearPerChatMaxContext(chatID string) {
	f.mu.Lock()
	delete(f.perChatMaxCtx, chatID)
	f.mu.Unlock()
}

// ─── Primary LLM resolution ──────────────────────────────

func chatKey(senderID, chatID string) string { return senderID + ":" + chatID }

// GetLLM returns (client, model, maxContext, thinkingMode, maxOutputTokens).
// All subscription-derived values come from a single llmEntry, guaranteeing
// consistency (same subscription for model, context, thinking, output).
// Lookup order:
//  1. In-memory cache (entries map)
//  2. subscriptionSvc (DB default subscription)
//  3. Global default LLM
func (f *LLMFactory) GetLLM(senderID string) (llm.LLM, string, int, string, int) {
	if e := f.getEntry(senderID); e != nil && e.client != nil {
		return e.client, e.model, f.resolveSubContext(e.model, e), e.thinkingMode, e.maxOutputTokens
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
			e := f.createEntryFromSub(sub, sub.Model)
			if e != nil {
				f.setEntry(senderID, e)
				f.hasCustomLLMCache.Store(senderID, true)
				return e.client, e.model, f.resolveSubContext(e.model, e), e.thinkingMode, e.maxOutputTokens
			}
		}
	}

	return f.defaultLLM, f.defaultModel, 0, f.defaultThinkingMode, 0
}

// GetLLMForChat returns (client, model, maxContext, thinkingMode, maxOutputTokens).
// All subscription-derived values come from a single llmEntry, guaranteeing
// consistency. Priority: per-chat entry → user-level entry (with per-chat maxCtx override).
func (f *LLMFactory) GetLLMForChat(senderID, chatID string) (llm.LLM, string, int, string, int) {
	if chatID == "" {
		return f.GetLLM(senderID)
	}
	key := chatKey(senderID, chatID)

	// Per-chat cache hit
	if e := f.getEntry(key); e != nil {
		maxCtx := f.resolveSubContext(e.model, e)
		f.mu.RLock()
		if pcCtx, ok := f.perChatMaxCtx[chatID]; ok && pcCtx > 0 {
			maxCtx = pcCtx
		}
		f.mu.RUnlock()
		// Lazy client recreation (SwitchModel clears client)
		if e.client == nil && e.subID != "" {
			e = f.createEntryFromSubID(e.subID, e.model)
			if e != nil {
				f.setEntry(key, e)
			}
		}
		if e != nil && e.client != nil {
			return e.client, e.model, maxCtx, e.thinkingMode, e.maxOutputTokens
		}
	}

	// Per-chat max_context override without per-chat subscription
	f.mu.RLock()
	if pcCtx, ok := f.perChatMaxCtx[chatID]; ok && pcCtx > 0 {
		f.mu.RUnlock()
		client, model, _, thinkingMode, maxOut := f.GetLLM(senderID)
		return client, model, pcCtx, thinkingMode, maxOut
	}
	f.mu.RUnlock()

	return f.GetLLM(senderID)
}

// GetMaxOutputTokens returns the cached max_output_tokens.
// When chatID is provided, checks the per-chat entry first (same source as GetLLMForChat).
// Prefer using GetLLMForChat which returns all subscription-derived values in one call.
func (f *LLMFactory) GetMaxOutputTokens(senderID string, chatID ...string) int {
	if len(chatID) > 0 && chatID[0] != "" {
		if e := f.getEntry(chatKey(senderID, chatID[0])); e != nil {
			return e.maxOutputTokens
		}
	}
	if e := f.getEntry(senderID); e != nil {
		return e.maxOutputTokens
	}
	return 0
}

// HasCustomLLM checks if a user has custom LLM config.
func (f *LLMFactory) HasCustomLLM(senderID string) bool {
	if val, ok := f.hasCustomLLMCache.Load(senderID); ok {
		b, _ := val.(bool)
		return b
	}
	if e := f.getEntry(senderID); e != nil && e.client != nil {
		f.hasCustomLLMCache.Store(senderID, true)
		return true
	}
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

func (f *LLMFactory) InvalidateCustomLLMCache(senderID string) {
	f.hasCustomLLMCache.Delete(senderID)
}

// ─── Write methods (all produce complete llmEntry) ───────

// createEntryFromSub creates a complete llmEntry from a subscription.
// Returns nil if the subscription config is invalid.
func (f *LLMFactory) createEntryFromSub(sub *sqlite.LLMSubscription, model string) *llmEntry {
	if sub == nil || sub.BaseURL == "" || sub.APIKey == "" {
		return nil
	}
	if model == "" {
		model = sub.Model
	}
	if model == "" {
		// Do NOT fall straight back to f.defaultModel — that contaminates this
		// entry (and f.defaultModel) with a model name from an unrelated
		// subscription when the current subscription has no default model
		// (the 404 bug). Pick a concrete model from this subscription first.
		model = f.PickDefaultModelForSub(sub)
	}
	if model == "" {
		model = f.defaultModel
	}
	// Resolve per-model APIType override, fallback to subscription-level
	apiType := sub.APIType
	if pm := sub.GetPerModelAPIType(model); pm != "" {
		apiType = pm
	}
	cfg := &sqlite.UserLLMConfig{
		Provider: sub.Provider, BaseURL: sub.BaseURL, APIKey: sub.APIKey,
		Model: model, MaxOutputTokens: sub.MaxOutputTokens, ThinkingMode: sub.ThinkingMode, APIType: apiType,
		ID:             sub.ID,
		OnModelsLoaded: f.makeOnModelsLoaded(sub.ID),
	}
	client, _ := f.createClient(cfg)
	if client == nil {
		return nil
	}
	return &llmEntry{
		client: client, model: model, subID: sub.ID, sub: sub,
		maxOutputTokens: sub.MaxOutputTokens, thinkingMode: sub.ThinkingMode,
	}
}

// createEntryFromSubID looks up a subscription by ID then creates an entry.
// This is the ID-based variant of createEntryFromSub — used when only the
// subscription ID is available (e.g. lazy rebuild in GetLLMForChat).
func (f *LLMFactory) createEntryFromSubID(subID, model string) *llmEntry {
	if f.subscriptionSvc == nil || subID == "" {
		return nil
	}
	sub, err := f.subscriptionSvc.Get(subID)
	if err != nil || sub == nil {
		return nil
	}
	return f.createEntryFromSub(sub, model)
}

// RefreshSessionEntry re-fetches the subscription for a per-session entry from DB
// and rebuilds the entry. This must be called at the start of every Run so that
// stale cached data from a previous Run never survives across message boundaries.
//
// When the per-chat entry does not exist (e.g. after server restart), it is
// restored from the tenants table using (channel, chatID) — this ensures
// per-session model choices survive restarts.
func (f *LLMFactory) RefreshSessionEntry(senderID, chatID, channel string) {
	if f.subscriptionSvc == nil || chatID == "" {
		return
	}
	key := chatKey(senderID, chatID)
	e := f.getEntry(key)
	if e == nil || e.subID == "" {
		// Cache miss: try restoring from tenants table before giving up.
		// Without this, sessions that had per-session model switches lose
		// their model on restart and fall back to the subscription default.
		f.restoreEntryFromDB(senderID, chatID, channel)
		return
	}
	sub, err := f.subscriptionSvc.Get(e.subID)
	if err != nil || sub == nil {
		return
	}
	// Preserve the per-session model across the refresh. The authoritative
	// source is the tenants table (written by the per-session switch RPC);
	// fall back to the cached entry's model, then the subscription default.
	// Rebuilding with sub.Model unconditionally discards per-session model
	// switches — and when sub.Model is empty, createEntryFromSub falls back to
	// f.defaultModel, which may still hold a model name from a PREVIOUS
	// subscription, producing a request to (new base_url, old model) → 404.
	model := e.model
	if f.tenantSvc != nil {
		if dbSubID, dbModel, gerr := f.tenantSvc.GetTenantSubscription(channel, chatID); gerr == nil && dbSubID == sub.ID && dbModel != "" {
			model = dbModel
		}
	}
	if model == "" {
		model = sub.Model
	}
	// Build new entry BEFORE acquiring the lock. createEntryFromSub makes HTTP
	// calls (model list loading) that can take 5-30s. Holding f.mu during that
	// call blocks every other goroutine that needs f.mu.RLock() — including
	// getEntry, GetLLMForChat, and GetLLMForModel — freezing the entire agent loop.
	newEntry := f.createEntryFromSub(sub, model)

	f.mu.Lock()
	current := f.entries[key]
	if current == nil || current.subID == "" || current.subID != sub.ID {
		f.mu.Unlock()
		return
	}
	if newEntry != nil {
		f.entries[key] = newEntry
	}
	f.mu.Unlock()
}

// restoreEntryFromDB recovers per-session subscription+model from the tenants
// table and populates the in-memory cache. Called by RefreshSessionEntry when
// the per-chat entry is empty (typically after server restart).
func (f *LLMFactory) restoreEntryFromDB(senderID, chatID, channel string) {
	if f.tenantSvc == nil || f.subscriptionSvc == nil {
		return
	}
	subID, model, err := f.tenantSvc.GetTenantSubscription(channel, chatID)
	if err != nil {
		log.WithError(err).WithField("chat_id", chatID).Debug("restoreEntryFromDB: GetTenantSubscription failed")
		return
	}
	if subID == "" {
		return // no per-session mapping in DB
	}
	sub, err := f.subscriptionSvc.Get(subID)
	if err != nil || sub == nil {
		log.WithError(err).WithFields(log.Fields{
			"chat_id": chatID, "sub_id": subID,
		}).Debug("restoreEntryFromDB: subscription lookup failed")
		return
	}
	effectiveModel := model
	if effectiveModel == "" {
		effectiveModel = sub.Model
	}
	e := f.createEntryFromSub(sub, effectiveModel)
	if e != nil {
		f.setEntry(chatKey(senderID, chatID), e)
		log.WithFields(log.Fields{
			"chat_id": chatID,
			"channel": channel,
			"sub_id":  subID,
			"model":   effectiveModel,
		}).Info("Restored per-session LLM from DB")
	}
}

// SwitchSubscription switches a user's active LLM to the specified subscription.
// Updates BOTH user-level (senderID) and per-chat (senderID:chatID) caches atomically.
func (f *LLMFactory) SwitchSubscription(senderID string, sub *sqlite.LLMSubscription, chatID string) error {
	e := f.createEntryFromSub(sub, sub.Model)
	if e == nil {
		log.WithFields(log.Fields{
			"sender_id": senderID, "sub_id": sub.ID,
			"provider": sub.Provider, "base_url": sub.BaseURL,
		}).Error("[LLM] SwitchSubscription: failed to create client")
		return fmt.Errorf("failed to create LLM client for subscription %s", sub.ID)
	}

	f.mu.Lock()
	f.entries[senderID] = e
	if chatID != "" {
		f.entries[chatKey(senderID, chatID)] = &llmEntry{
			client: e.client, model: e.model, subID: e.subID, sub: e.sub,
			maxOutputTokens: e.maxOutputTokens, thinkingMode: e.thinkingMode,
		}
	}
	// Update user-level default LLM so that SubAgent fallback, ListModels(),
	// and GetLLM for sessions without per-session subscriptions all follow
	// the user's last choice. In CLI mode, all sessions share senderID "cli_user",
	// so this correctly reflects the user's global LLM preference.
	if senderID == "cli_user" {
		f.defaultLLM = e.client
		f.defaultModel = e.model
	}
	f.mu.Unlock()

	log.WithFields(log.Fields{
		"sender_id": senderID, "chat_id": chatID, "sub_id": sub.ID,
		"sub_name": sub.Name, "model": e.model,
		"max_output_tokens": e.maxOutputTokens, "thinking_mode": e.thinkingMode,
	}).Debug("[LLM] SwitchSubscription: client created and cached")

	f.hasCustomLLMCache.Store(senderID, true)
	return nil
}

// SetSessionLLM sets the LLM for a specific session ONLY (no user-level update).
func (f *LLMFactory) SetSessionLLM(senderID, chatID string, sub *sqlite.LLMSubscription) error {
	if chatID == "" || sub == nil {
		return fmt.Errorf("SetSessionLLM: chatID and sub are required")
	}
	e := f.createEntryFromSub(sub, sub.Model)
	if e == nil {
		return fmt.Errorf("failed to create LLM client for session %s", chatID)
	}
	f.setEntry(chatKey(senderID, chatID), e)
	return nil
}

// SwitchModel switches the active model without changing subscription.
// When chatID is provided, only the per-chat entry is updated (session-scoped).
// When chatID is empty, the user-level entry is updated and per-chat caches are cleared.
func (f *LLMFactory) SwitchModel(senderID, model string, chatID ...string) {
	effectiveChatID := ""
	if len(chatID) > 0 {
		effectiveChatID = chatID[0]
	}

	f.mu.Lock()
	if effectiveChatID != "" {
		key := chatKey(senderID, effectiveChatID)
		if userEntry := f.entries[senderID]; userEntry != nil {
			// Copy user-level subID and sub cache. resolveSubContext will query
			// DB by subID first; the sub cache is only a fallback when DB is
			// unavailable (tests, startup). This prevents stale sub pointers
			// from causing model-from-sub-A + config-from-sub-B contamination.
			f.entries[key] = &llmEntry{
				subID: userEntry.subID, sub: userEntry.sub, model: model,
				maxOutputTokens: userEntry.maxOutputTokens,
				thinkingMode:    userEntry.thinkingMode,
			}
		} else {
			f.entries[key] = &llmEntry{model: model}
		}
	} else {
		prefix := senderID + ":"
		for k := range f.entries {
			if strings.HasPrefix(k, prefix) {
				delete(f.entries, k)
			}
		}
		if e := f.entries[senderID]; e != nil {
			e.model = model
		} else {
			f.entries[senderID] = &llmEntry{model: model}
		}
	}
	svc := f.subscriptionSvc
	f.mu.Unlock()

	// Only persist model change to the default subscription for user-level
	// switches (no chatID). Per-session model switches (with chatID) must NOT
	// modify the subscription — otherwise switching model in session A
	// contaminates all sessions sharing the same subscription.
	if effectiveChatID == "" && svc != nil && senderID != "" {
		if sub, err := svc.GetDefault(senderID); err == nil && sub != nil && sub.Model != model && sub.ID != "" {
			_ = svc.SetModel(sub.ID, model)
		}
	}
}

// SetChatLLM caches an LLM client for a specific chat session.
// IMPORTANT: Inherits subscription from user-level entry so that per-model
// config lookups (MaxContext, MaxOutputTokens) still work correctly.
// This fixes the root cause of "subscription switch loses max_context" bugs.
func (f *LLMFactory) SetChatLLM(senderID, chatID string, client llm.LLM, model string) {
	entry := &llmEntry{client: client, model: model}
	// Inherit subscription metadata from user-level entry
	f.mu.Lock()
	if existing := f.entries[senderID]; existing != nil {
		entry.subID = existing.subID
		entry.sub = existing.sub
		entry.maxOutputTokens = existing.maxOutputTokens
		entry.thinkingMode = existing.thinkingMode
	}
	if chatID == "" {
		f.entries[senderID] = entry
	} else {
		f.entries[chatKey(senderID, chatID)] = entry
	}
	f.mu.Unlock()
}

// SetUserMaxOutputTokens updates the max_output_tokens for a user's entry.
func (f *LLMFactory) SetUserMaxOutputTokens(senderID string, n int) {
	f.mu.Lock()
	if e := f.entries[senderID]; e != nil {
		e.maxOutputTokens = n
	}
	f.mu.Unlock()
}

// SetUserThinkingMode updates the thinking_mode for a user's entry.
func (f *LLMFactory) SetUserThinkingMode(senderID, mode string) {
	f.mu.Lock()
	if e := f.entries[senderID]; e != nil {
		e.thinkingMode = mode
	}
	f.mu.Unlock()
}

// SetDefaults updates the global default LLM and clears ALL per-user caches.
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
	f.entries = make(map[string]*llmEntry)
	f.perChatMaxCtx = make(map[string]int)
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

// SetProxyLLM sets a ProxyLLM for a user, overriding per-user config.
func (f *LLMFactory) SetProxyLLM(senderID string, proxy *llm.ProxyLLM, model string) {
	f.mu.Lock()
	f.entries[senderID] = &llmEntry{
		client: proxy, model: model,
		maxOutputTokens: 0, thinkingMode: "",
	}
	f.mu.Unlock()
}

// ClearProxyLLM removes a ProxyLLM and ALL associated state.
func (f *LLMFactory) ClearProxyLLM(senderID string) {
	f.mu.Lock()
	delete(f.entries, senderID)
	f.mu.Unlock()
}

// Invalidate clears user-level and all per-chat caches for a sender.
// Invalidate removes ALL cached entries for a sender — both user-level and
// per-session (sender:chatID). Use with caution: this wipes every session's
// LLM override and forces every session to fall back to the default subscription.
// Prefer InvalidateSender (user-level only) or InvalidateSession (one session).
func (f *LLMFactory) Invalidate(senderID string) {
	f.mu.Lock()
	prefix := senderID + ":"
	for k := range f.entries {
		if k == senderID || strings.HasPrefix(k, prefix) {
			delete(f.entries, k)
		}
	}
	f.mu.Unlock()
}

// InvalidateSender removes only the user-level entry (senderID key),
// preserving all per-session entries (senderID:chatID keys).
// Safe to call from subscription field updates — per-session overrides survive.
func (f *LLMFactory) InvalidateSender(senderID string) {
	f.mu.Lock()
	delete(f.entries, senderID)
	f.mu.Unlock()
}

// InvalidateSession removes the per-session entry for a specific chat.
func (f *LLMFactory) InvalidateSession(senderID, chatID string) {
	f.mu.Lock()
	delete(f.entries, chatKey(senderID, chatID))
	f.mu.Unlock()
}

// InvalidateAll clears ALL caches.
func (f *LLMFactory) InvalidateAll() {
	f.mu.Lock()
	f.entries = make(map[string]*llmEntry)
	f.perChatMaxCtx = make(map[string]int)
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
		if sub, err := f.subscriptionSvc.Get(subID); err == nil && sub != nil {
			_ = f.subscriptionSvc.UpdateCachedModels(subID, models)
		}
	}
}

// ─── Model-first resolution (v38+) ───────────────────────
//
// This is the new authoritative resolution path. Per-session state lives in DB
// (tenants.subscription_id + model for per-session; user_default_model for the
// user-level default). The in-memory sessionMemo is a pure memo of that DB
// state — every write invalidates the affected keys, so it can never diverge.
//
// LLM clients are cached per (subscription_id, apiType): one client serves all
// models of a subscription, with the model supplied per request. This is the
// correct granularity because credentials/base_url live on the subscription,
// not the model.
//
// These methods are additive alongside the legacy Switch*/Set*/Invalidate*
// matrix. The legacy matrix is removed once RPC + CLI migrate (later chunk).

// PickDefaultModelForSub returns a sensible default model for a subscription
// when neither the caller nor the subscription specifies one. Prefers the first
// enabled subscription_models row, then the first cached API model. Returns ""
// if none is found (caller falls back to the global default).
func (f *LLMFactory) PickDefaultModelForSub(sub *sqlite.LLMSubscription) string {
	if sub == nil {
		return ""
	}
	if f.subscriptionSvc != nil && sub.ID != "" {
		if models, err := f.subscriptionSvc.GetModels(sub.ID); err == nil {
			for _, m := range models {
				if m.Enabled {
					return m.Model
				}
			}
		}
	}
	if len(sub.CachedModels) > 0 {
		return sub.CachedModels[0]
	}
	return ""
}

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
//  3. System default (defaultLLM)
//
// The sessionMemo caches the result; SelectModel / SetUserDefaultModel /
// InvalidateSubscription invalidate the affected keys so the memo tracks DB.
func (f *LLMFactory) ResolveLLM(senderID, chatID, channel string) (llm.LLM, string, int, string, int) {
	key := senderID
	if chatID != "" {
		key = chatKey(senderID, chatID)
	}

	f.mu.RLock()
	memo := f.sessionMemo[key]
	f.mu.RUnlock()
	if memo != nil && memo.client != nil {
		return memo.client, memo.model, f.resolveSubContextFor(memo.subID, memo.model), memo.thinkingMode, memo.maxOutputTokens
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
		client, m, mc, tm, mo := f.GetLLM(senderID)
		return client, m, mc, tm, mo
	}
	sub := f.lookupSub(subID)
	if sub == nil {
		client, m, mc, tm, mo := f.GetLLM(senderID)
		return client, m, mc, tm, mo
	}
	if model == "" {
		model = sub.Model
	}
	if model == "" {
		model = f.PickDefaultModelForSub(sub)
	}
	if model == "" {
		client, m, mc, tm, mo := f.GetLLM(senderID)
		return client, m, mc, tm, mo
	}
	client := f.getOrCreateClient(sub, model)
	if client == nil {
		client, m, mc, tm, mo := f.GetLLM(senderID)
		return client, m, mc, tm, mo
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
	entry := &resolvedEntry{
		subID: sub.ID, model: model, client: client,
		maxOutputTokens: maxOut, thinkingMode: thinking,
	}
	f.mu.Lock()
	f.sessionMemo[key] = entry
	f.mu.Unlock()
	return client, model, f.resolveSubContextFor(sub.ID, model), thinking, maxOut
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
	// Invalidate the session memo (and any user-level memo) so ResolveLLM re-reads.
	f.invalidateMemo(senderID, chatID)
	f.hasCustomLLMCache.Store(senderID, true)
	return nil
}

// ResolveSubscriptionForModel finds the subscription that provides the given
// model for a user. This is the model-first inverse of "which models does this
// subscription serve": given a model name picked from the unified model list,
// return the subscription whose endpoint actually serves it, so the agent pairs
// the right credentials with the model name.
//
// Search order (first match wins, default subscription preferred when tied):
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
	// find returns the subscription for the model, preferring the default sub.
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
			if sub.IsDefault {
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
	// Pass 2: CachedModels and sub.Model (models not registered as rows).
	if owner := find(func(sub *sqlite.LLMSubscription) bool {
		if sub.Model == model {
			return true
		}
		for _, m := range sub.CachedModels {
			if m == model {
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
// new sessions. Persists to user_default_model and invalidates the user's memos.
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
	f.invalidateUserMemos(senderID)
	f.hasCustomLLMCache.Store(senderID, true)
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

// InvalidateSubscription drops the client cache + all session memos referencing
// a subscription. Called when a subscription's credentials/config change or one
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
	for k, e := range f.sessionMemo {
		if e.subID == subID {
			delete(f.sessionMemo, k)
		}
	}
	f.mu.Unlock()
}

// invalidateMemo drops the memo for one session (and the user-level key).
func (f *LLMFactory) invalidateMemo(senderID, chatID string) {
	f.mu.Lock()
	delete(f.sessionMemo, senderID)
	if chatID != "" {
		delete(f.sessionMemo, chatKey(senderID, chatID))
	}
	f.mu.Unlock()
}

// invalidateUserMemos drops all memos for a user (user-level + every session).
func (f *LLMFactory) invalidateUserMemos(senderID string) {
	prefix := senderID + ":"
	f.mu.Lock()
	for k := range f.sessionMemo {
		if k == senderID || strings.HasPrefix(k, prefix) {
			delete(f.sessionMemo, k)
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
	// seen keyed by (subID, model) so the same model from different subscriptions
	// is listed once per subscription, while a single subscription never emits the
	// same model twice (e.g. a model present in both CachedModels and sub.Model).
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
	// systemModelLabel tags models that come from the global default LLM (no
	// owning subscription). Shown as "system · model" in the picker so they're
	// not bare among subscription-owned entries.
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

	// Only enabled subscriptions contribute. Cache their subscription_models rows
	// (the "record") and their fetched CachedModels set.
	type subInfo struct {
		sub    *sqlite.LLMSubscription
		rows   []*sqlite.SubscriptionModel
		fetch  map[string]bool
		rowEn  map[string]bool // model → enabled flag from row (absent ⇒ true)
		rowHas map[string]bool // model → has a subscription_models record
	}
	infos := make([]subInfo, 0, len(subs))
	for _, sub := range subs {
		if !sub.Enabled {
			continue
		}
		rows, _ := f.subscriptionSvc.GetModels(sub.ID)
		fetch := make(map[string]bool, len(sub.CachedModels))
		for _, m := range sub.CachedModels {
			if m != "" {
				fetch[m] = true
			}
		}
		rowEn := make(map[string]bool, len(rows))
		rowHas := make(map[string]bool, len(rows))
		for _, r := range rows {
			rowHas[r.Model] = true
			rowEn[r.Model] = r.Enabled
		}
		infos = append(infos, subInfo{sub: sub, rows: rows, fetch: fetch, rowEn: rowEn, rowHas: rowHas})
	}
	// statusOf computes the per-(sub,model) status.
	//   disabled: row.enabled == false
	//   normal:   fetched (in CachedModels) OR it's sub.Model, and not disabled
	//   offline:  has a record (row) but not fetched, and not disabled
	statusOf := func(i int, m string) string {
		info := infos[i]
		if info.rowHas[m] && !info.rowEn[m] {
			return "disabled"
		}
		if info.fetch[m] || m == info.sub.Model {
			return "normal"
		}
		return "offline"
	}
	// candidates(i) = every model subscription i knows about (fetched + default
	// + records), deduped by name and SORTED for deterministic emission order.
	// Map iteration (s.fetch / s.rows) is randomized in Go, so without sorting
	// the picker display order — and tests comparing two separate calls — would
	// be nondeterministic.
	candidates := func(i int) []string {
		s := infos[i]
		set := make(map[string]bool)
		for m := range s.fetch {
			if m != "" {
				set[m] = true
			}
		}
		if s.sub.Model != "" {
			set[s.sub.Model] = true
		}
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
	// Emit every (subscription, model) pair in subscription order. The picker
	// shows each pair as its own row so a user can pick the specific subscription
	// that serves a model (e.g. "system · deepseek-v4-pro" vs "deepseek ·
	// deepseek-v4-pro"). `add` dedups within a subscription by model name.
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

// RefreshModelEntriesForUser live-fetches /models for every enabled subscription
// (parallel, capped concurrency, per-sub timeout), persists results to
// CachedModels via the OnModelsLoaded callback, then returns the fresh entry
// list. Subscriptions that error keep their existing CachedModels (soft fail).
// Used by the model picker so the list reflects each provider's true available
// models, not just the persisted snapshot.
func (f *LLMFactory) RefreshModelEntriesForUser(senderID string) []protocol.ModelEntry {
	if f.subscriptionSvc == nil {
		return f.ListAllModelEntriesForUser(senderID)
	}
	var subs []*sqlite.LLMSubscription
	var err error
	if senderID != "" {
		subs, err = f.subscriptionSvc.List(senderID)
	} else {
		subs, err = f.subscriptionSvc.ListAll()
	}
	if err != nil {
		return f.ListAllModelEntriesForUser(senderID)
	}
	const concurrency = 8
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, sub := range subs {
		if !sub.Enabled {
			continue
		}
		if sub.BaseURL == "" || sub.APIKey == "" {
			log.WithFields(log.Fields{"sub": sub.Name, "has_baseurl": sub.BaseURL != "", "has_apikey": sub.APIKey != ""}).Debug("[LLM] RefreshModelEntries: skipping sub (missing base_url or api_key)")
			continue
		}
		wg.Add(1)
		go func(s *sqlite.LLMSubscription) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// Model is only the client's default; /models fetch is independent of it.
			client := f.createClientFromSub(s, s.Model)
			if client == nil {
				return
			}
			loader, ok := client.(llm.ModelLoader)
			if !ok {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			if err := loader.LoadModelsFromAPI(ctx); err != nil {
				log.WithFields(log.Fields{"sub": s.Name, "base_url": s.BaseURL, "has_apikey": s.APIKey != ""}).Warn("[LLM] RefreshModelEntries: /models fetch failed")
			}
		}(sub)
	}
	wg.Wait()
	return f.ListAllModelEntriesForUser(senderID)
}

// GetLLMForModel returns (client, model, maxContext, thinkingMode, maxOutputTokens, usedCustom).
// All subscription-derived values come from a single subscription, guaranteeing consistency.
// Used by SubAgent when a role specifies a model (or tier name like vanguard/balance/swift).
func (f *LLMFactory) GetLLMForModel(senderID, targetModel string) (llm.LLM, string, int, string, int, bool) {
	resolvedModel, fromTier := f.resolveTierModel(targetModel)
	if resolvedModel == "" {
		client, model, maxCtx, tm, maxOut := f.GetLLM(senderID)
		return client, model, maxCtx, tm, maxOut, false
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
				if sub.BaseURL == "" || sub.APIKey == "" || len(sub.CachedModels) > 0 {
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
				updatedSubs, err2 := f.subscriptionSvc.List(senderID)
				if err2 == nil {
					for _, us := range updatedSubs {
						if us.ID == sub.ID {
							for _, m := range us.CachedModels {
								if m == resolvedModel {
									log.WithFields(log.Fields{"model": resolvedModel, "sub": sub.Name, "source": "api-load"}).Info("[LLM] GetLLMForModel: found after API load")
									return client, resolvedModel, f.resolveEffectiveContext(resolvedModel, sub.ID), sub.ThinkingMode, sub.MaxOutputTokens, true
								}
							}
						}
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

func (f *LLMFactory) resolveTierModel(value string) (string, bool) {
	tier := normalizeModelTier(value)
	if tier == "" {
		return value, false
	}
	f.mu.RLock()
	tiers := f.tierModels
	f.mu.RUnlock()
	model := f.tierModel(tiers, tier)
	if model != "" {
		return model, true
	}
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
	return "", true
}

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

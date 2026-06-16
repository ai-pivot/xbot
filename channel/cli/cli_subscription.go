package cli

import (
	"fmt"
	ch "xbot/channel"

	tea "charm.land/bubbletea/v2"
)

// cycleModel switches to the next model across all subscriptions.
// Uses ListAllModels() so models from ALL subscriptions are visible (not just the
// current default LLM). Cycles through the model names displayed in the status bar.
// Note: this only changes the cached model name — the actual subscription switch
// happens when a new LLM call is made (or via quick switch panel).
func (m *cliModel) cycleModel() {
	if m.channel == nil {
		return
	}

	// Ensure models are loaded synchronously before cycling.
	// Without this, the first Ctrl+N sees only the single fallback model
	// (the async fetch hasn't completed yet).
	m.channel.modelLister.EnsureModelsLoaded()

	// Use ListModels (current subscription only) instead of ListAllModels.
	// Ctrl+N should cycle through the current subscription's models only.
	models := m.channel.modelLister.ListModels()
	if len(models) < 2 {
		m.showTempStatus("Only one model available")
		return
	}

	current := m.cachedModelName
	nextIdx := 0
	for i, name := range models {
		if name == current {
			nextIdx = (i + 1) % len(models)
			break
		}
	}
	nextModel := models[nextIdx]

	m.cachedModelName = nextModel
	m.showTempStatus(fmt.Sprintf("Model: %s", nextModel))

	// Switch model on the current subscription (no need to change subscription
	// since we're already cycling within the current subscription's models).
	if m.llmSubscriber != nil {
		m.llmSubscriber.SwitchModel(m.senderID, nextModel, m.chatID)
	}
	// Persist per-session model choice
	existing := LoadSessionLLMState(m.workDir, m.chatID)
	existing.SubscriptionID = m.activeSubID
	existing.Model = nextModel
	SaveSessionLLMState(m.workDir, m.chatID, existing, m.remoteMode)
	// Re-resolve context/output token limits for the new model so the
	// context usage bar reflects the correct window size immediately.
	m.cachedMaxContextTokens = ResolveEffectiveMaxContext(existing, m.subscriptionMgr)
	m.cachedMaxOutputTokens = int64(ResolveEffectiveMaxOutputTokens(existing, m.subscriptionMgr))
	m.updateQuickSwitchModels(nextModel)
}

// tickerTickMsg 是 ticker 定时 tick 消息

// debugCaptureMsg triggers a UI capture (dump View() to file).
// cliTokenRefreshMsg refreshes the context bar after compression.
// Pushed through asyncCh by refreshTokenStateAfterReload.
type cliTokenRefreshMsg struct {
	channelName     string
	chatID          string
	tokenPrompt     int64
	tokenCompletion int64
}

// cliToastItem 单条 Toast 通知数据
// SetSubscriptionMgr sets the subscription manager for quick switch.
func (m *cliModel) SetSubscriptionMgr(mgr SubscriptionManager) {
	m.subscriptionMgr = mgr
}

// SetLLMSubscriber sets the LLM subscriber for quick switch.
// SetLLMSubscriber sets the LLM subscriber for quick switch.
func (m *cliModel) SetLLMSubscriber(sub LLMSubscriber) {
	m.llmSubscriber = sub
}

// ---------------------------------------------------------------------------
// Bubble Tea Messages (内部消息类型)
// ---------------------------------------------------------------------------

// cliOutboundMsg 从 agent 收到的消息
// cliSwitchLLMDoneMsg is sent when an async subscription switch completes.
// resolveSubMaxContext returns the effective max_context from a subscription.
// Priority: per-model config → subscription-level MaxContext → 0 (let global config decide).
func resolveSubMaxContext(sub *ch.Subscription) int {
	if sub.Model != "" {
		if pmc, ok := sub.PerModelConfigs[sub.Model]; ok && pmc.MaxContext > 0 {
			return pmc.MaxContext
		}
	}
	// Fallback to subscription-level MaxContext (previously invisible to TUI,
	// causing 1M-context subscriptions to show 200k in context bar).
	if sub.MaxContext > 0 {
		return sub.MaxContext
	}
	return 0
}

// resolveSubMaxOutputTokens returns the per-model max_output_tokens from a subscription.
// resolveSubMaxOutputTokens returns the per-model max_output_tokens from a subscription.
func resolveSubMaxOutputTokens(sub *ch.Subscription) int {
	if sub.Model != "" {
		if pmc, ok := sub.PerModelConfigs[sub.Model]; ok && pmc.MaxOutputTokens > 0 {
			return pmc.MaxOutputTokens
		}
	}
	return sub.MaxOutputTokens
}

// hasNoSubscription returns true when there is no usable subscription configured.
// Used to show a friendly setup prompt instead of a cryptic LLM error.
func (m *cliModel) hasNoSubscription() bool {
	if m.hasNoSubCacheValid {
		return m.hasNoSubCache
	}
	result := m.computeHasNoSubscription()
	m.hasNoSubCache = result
	m.hasNoSubCacheValid = true
	return result
}

// computeHasNoSubscription performs the actual subscription check.
// computeHasNoSubscription performs the actual subscription check.
func (m *cliModel) computeHasNoSubscription() bool {
	if m.channel == nil || m.channel.subscriptionMgr == nil {
		return true
	}
	subs, err := m.channel.subscriptionMgr.List(m.senderID)
	if err != nil || len(subs) == 0 {
		return true
	}
	// Check if any subscription has an API key
	for _, sub := range subs {
		if sub.APIKey != "" {
			return false
		}
	}
	return true
}

// invalidateSubCache forces hasNoSubscription to re-query on next call.
// invalidateSubCache forces hasNoSubscription to re-query on next call.
func (m *cliModel) invalidateSubCache() {
	m.hasNoSubCacheValid = false
}

// refreshCachedModelName caches the current model name to avoid repeated lookups in View().
// Should be called after channel init, config changes, and settings saves.
// Prefers per-session override (from disk or in-memory state) over global default.
// refreshCachedModelName caches the current model name to avoid repeated lookups in View().
// Should be called after channel init, config changes, and settings saves.
// Prefers per-session override (from disk or in-memory state) over global default.
func (m *cliModel) refreshCachedModelName() {
	if m.channel == nil {
		return
	}
	// ── Remote mode: backend is the source of truth ──────────────────
	// Query the backend for the session→subscription mapping first.
	// The backend persists this in the tenants table (via SetSessionLLM).
	// Local JSON is NOT authoritative for subscription fields in remote mode.
	if m.remoteMode && m.channel.subscriptionMgr != nil {
		if subID, model, err := m.channel.subscriptionMgr.GetSessionSubscription(m.senderID, m.chatID); err == nil && subID != "" {
			m.cachedModelName = model
			m.activeSubID = subID
			return
		}
		// Backend returned empty (server restart, first-time session, etc.).
		// Fall through to local JSON as cache.
	}
	// ── Local mode / fallback: per-session model from disk ──────────
	if state := LoadSessionLLMState(m.workDir, m.chatID); state.Model != "" {
		m.cachedModelName = state.Model
		if m.activeSubID == "" && state.SubscriptionID != "" {
			m.activeSubID = state.SubscriptionID
		}
		return
	}
	// Fallback: in-memory saved state (for sessions that were saved but not yet persisted)
	if saved, ok := m.savedSessions[m.sessionKey()]; ok && saved.activeModel != "" {
		m.cachedModelName = saved.activeModel
		if saved.activeSubscriptionID != "" {
			m.activeSubID = saved.activeSubscriptionID
		}
		return
	}
	// Fallback: only use global default when no per-session override exists
	if m.cachedModelName == "" && m.channel.subscriptionMgr != nil {
		if sub, err := m.channel.subscriptionMgr.GetDefault(m.senderID); err == nil && sub != nil {
			m.cachedModelName = sub.Model
			if m.activeSubID == "" {
				m.activeSubID = sub.ID
			}
		}
	}
	// Cache model count for View() (avoids ListAllModels RPC per frame)
	if m.channel.modelLister != nil {
		m.modelCount = len(m.channel.modelLister.ListAllModels())
	}
}

// scheduleSessionLLMRestore triggers an async SwitchLLM + SetDefault RPC when
// a per-session subscription was restored from Session JSON during startup.
// This ensures the backend (server or local agent) uses the correct LLM,
// not just the frontend display.
// scheduleSessionLLMRestore triggers an async SwitchLLM + SetDefault RPC when
// a per-session subscription was restored from Session JSON during startup.
// This ensures the backend (server or local agent) uses the correct LLM,
// not just the frontend display.
func (m *cliModel) scheduleSessionLLMRestore() {
	if m.activeSubID == "" || m.channel == nil || m.channel.subscriptionMgr == nil {
		return
	}
	if m.channel.config.SwitchLLM == nil {
		return
	}
	subs, err := m.channel.subscriptionMgr.List("")
	if err != nil {
		return
	}
	for i := range subs {
		if subs[i].ID == m.activeSubID {
			switchFn := m.channel.config.SwitchLLM
			target := subs[i]
			// Use per-session model if available (restored by refreshCachedModelName
			// from the backend tenants table). This ensures the restored LLM uses the
			// model the user switched to in this session, not the subscription's default.
			model := target.Model
			if m.cachedModelName != "" {
				model = m.cachedModelName
			}
			m.pendingCmds = append(m.pendingCmds, func() tea.Msg {
				err := switchFn(target.Provider, target.BaseURL, target.APIKey, model)
				return cliSwitchLLMDoneMsg{
					err:       err,
					subID:     target.ID,
					subName:   target.Name,
					subModel:  model,
					maxCtx:    resolveSubMaxContext(&target),
					maxOutTok: resolveSubMaxOutputTokens(&target),
					mgr:       m.subscriptionMgr,
				}
			})
			break
		}
	}
}

// Init 初始化。全局 ticker goroutine 已在 NewCLIChannel 中启动，
// 不需要 Init 启动任何 tick chain。

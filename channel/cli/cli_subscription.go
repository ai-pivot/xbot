package cli

import (
	"fmt"
	"github.com/sirupsen/logrus"
	ch "xbot/channel"

	tea "charm.land/bubbletea/v2"
)

// applyModelSwitch switches the session to the given model and re-syncs client
// state. Because the model may belong to a different subscription than the
// current one, the owning subscription is re-read from the backend
// (GetSessionSubscription, authoritative in both local and remote modes) after
// the switch RPC completes, so activeSubID/cachedModelName/context limits
// reflect the new owner rather than the stale previous subscription.
//
// When subID is non-empty, the picker row carried an owning subscription, so
// SelectModel pins that exact subscription (the same model name may be served
// by several subscriptions; SelectModel disambiguates). When subID is empty
// (no owning subscription, e.g. a bare system-default entry from the
// subscriptionSvc==nil path), it falls back to SwitchModel, which resolves the
// owner server-side by model name.
func (m *cliModel) applyModelSwitch(nextModel, subID string) {
	if nextModel == "" {
		return
	}
	if m.llmSubscriber != nil && subID != "" {
		if err := m.llmSubscriber.SelectModel(m.senderID, subID, nextModel, m.chatID); err != nil {
			logrus.WithError(err).WithField("sub_id", subID).Warn("applyModelSwitch: SelectModel failed")
		}
	}
	m.cachedModelName = nextModel
	// Re-resolve the owning subscription from the backend. switch_model persists
	// (ownerSubID, model) to tenants; GetSessionSubscription reads it back. This
	// works in local mode too (same RPC path through ChannelTransport → ServerCore).
	if m.channel != nil && m.channel.subscriptionMgr != nil {
		if subID, _, err := m.channel.subscriptionMgr.GetSessionSubscription(m.senderID, m.chatID); err == nil && subID != "" {
			m.activeSubID = subID
		}
	}
	m.refreshCachedSubName()
	state := SessionLLMState{
		SubscriptionID: m.activeSubID,
		Model:          nextModel,
	}
	m.cachedMaxContextTokens = ResolveEffectiveMaxContext(state, m.subscriptionMgr)
	m.cachedMaxOutputTokens = int64(ResolveEffectiveMaxOutputTokens(state, m.subscriptionMgr))
	SaveSessionLLMState(m.workDir, m.chatID, SessionLLMState{
		SubscriptionID: m.activeSubID,
		Model:          nextModel,
	}, m.remoteMode)
	m.updateQuickSwitchModels(nextModel)
	m.showTempStatus(fmt.Sprintf("Model: %s", nextModel))
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

// refreshCachedSubName caches the owning subscription's display name for the
// status bar ("订阅名 · 模型名"). Called whenever activeSubID changes. Reads the
// subscription list (one RPC) — NOT per-frame; View() reads the cached field.
func (m *cliModel) refreshCachedSubName() {
	m.cachedSubName = ""
	if m.activeSubID == "" || m.channel == nil || m.channel.subscriptionMgr == nil {
		return
	}
	if subs, err := m.channel.subscriptionMgr.List(""); err == nil {
		for _, s := range subs {
			if s.ID == m.activeSubID {
				m.cachedSubName = s.Name
				return
			}
		}
	}
}

// refreshCachedModelName caches the current model name to avoid repeated lookups in View().
// Should be called after channel init, config changes, settings saves, and session switches.
// Prefers per-session override (from DB tenants table or disk) over global default.
func (m *cliModel) refreshCachedModelName() {
	defer m.refreshCachedSubName() // keep status-bar "订阅名 · 模型名" in sync with activeSubID
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
	// Local JSON is the source of truth for this fallback path. Both
	// cachedModelName AND activeSubID must come from the same record —
	// updating only the model while keeping a stale activeSubID from a
	// previous session creates impossible (model, subscription) pairs
	// like "glm-5.2(dpsk)".
	if state := LoadSessionLLMState(m.workDir, m.chatID); state.Model != "" {
		m.cachedModelName = state.Model
		if state.SubscriptionID != "" {
			m.activeSubID = state.SubscriptionID
		}
		return
	}
	// Fallback: in-memory saved state (for sessions that were saved but not yet persisted)
	if saved, ok := m.savedSessions[m.sessionKey()]; ok && saved.activeModel != "" {
		// Both must come from the same record to avoid impossible (model, sub) pairs.
		m.cachedModelName = saved.activeModel
		m.activeSubID = saved.activeSubscriptionID
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

// refreshCachedThinkingMode loads the global thinking_mode user setting from
// the settings service into m.cachedThinkingMode for status-bar display. Called
// on startup, session restore, and after the Ctrl+M toggle. Thinking is a
// global per-user value stored under the canonical channel (agent.ThinkingModeChannel).
func (m *cliModel) refreshCachedThinkingMode() {
	if m.channel == nil || m.channel.settingsSvc == nil {
		return
	}
	vals, err := m.channel.settingsSvc.GetSettings(ch.ThinkingModeChannel, m.senderID)
	if err != nil || vals == nil {
		return
	}
	m.cachedThinkingMode = vals["thinking_mode"]
}

// thinkingModeLabel renders the status-bar indicator for the current global
// thinking mode. "" = auto (provider default), "enabled" = on, "disabled" = off.
// Compact ASCII format for clean terminal rendering.
func (m *cliModel) thinkingModeLabel() string {
	switch m.cachedThinkingMode {
	case "enabled":
		return m.styles.Accent.Render("think+")
	case "disabled":
		return m.styles.TextMutedSt.Render("think-")
	default:
		return m.styles.TextMutedSt.Render("think")
	}
}

// toggleThinkingMode cycles the global thinking_mode user setting
// (auto → enabled → disabled → auto), persists it, applies the runtime effect
// (InvalidateSender via the SettingHandlerRegistry), and refreshes the cache.
func (m *cliModel) toggleThinkingMode() {
	if m.channel == nil || m.channel.settingsSvc == nil {
		return
	}
	next := ""
	switch m.cachedThinkingMode {
	case "":
		next = "enabled"
	case "enabled":
		next = "disabled"
	case "disabled":
		next = ""
	}
	if err := m.channel.settingsSvc.SetSetting(ch.ThinkingModeChannel, m.senderID, "thinking_mode", next); err != nil {
		m.showTempStatus(fmt.Sprintf("Failed to set thinking mode: %v", err))
		return
	}
	// Apply runtime effect (the thinking_mode handler calls InvalidateSender so
	// the next LLM call re-reads the new value).
	if m.channel.config.ApplySettings != nil {
		m.channel.config.ApplySettings(map[string]string{"thinking_mode": next}, m.chatID)
	}
	m.cachedThinkingMode = next
	m.showTempStatus("Thinking mode: " + m.thinkingModeLabel())
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

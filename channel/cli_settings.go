package channel

import (
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
)

// cli_settings.go — settings panel read/write logic (≤300 lines, NO cache).
//
// Data model (single source of truth per scope):
//
//	ScopeSubscription → subscriptionMgr (config.json Subscriptions[].PerModelConfigs)
//	ScopeUser         → settingsSvc    (user_settings DB / config.json)
//
// readSettings:  merges all scopes → map[string]string for the settings panel.
// saveSettings:  dispatches each key to its scope's writer.
// maxContext:    resolves from subscription.PerModelConfigs[model].MaxContext.

// ── read ─────────────────────────────────────────────────────────────

// readSettings returns the current settings values for the /settings panel.
// Order (later wins): schema defaults → config values → DB overrides → subscription fields.
func (m *cliModel) readSettings() map[string]string {
	values := make(map[string]string)
	if m.channel == nil {
		return values
	}

	// 1. Base values from config (theme, language, tiers, agent defaults)
	if m.channel.config.GetCurrentValues != nil {
		for k, v := range m.channel.config.GetCurrentValues() {
			if v != "" {
				values[k] = v
			}
		}
	}

	// 2. User-scoped DB overrides (max_iterations, language overrides, etc.)
	if m.channel.settingsSvc != nil {
		if vals, err := m.channel.settingsSvc.GetSettings(m.channelName, m.senderID); err == nil {
			for k, v := range vals {
				if v != "" && IsUserScopedSettingKey(k) {
					values[k] = v
				}
			}
		}
	}

	// 3. Subscription-scoped fields (provider, key, model, max_output, thinking_mode, max_context)
	sub := m.activeSubscription()
	if sub != nil {
		values["llm_provider"] = sub.Provider
		values["llm_api_key"] = sub.APIKey
		values["llm_base_url"] = sub.BaseURL
		values["llm_model"] = sub.Model
		values["max_output_tokens"] = strconv.Itoa(sub.MaxOutputTokens)
		values["thinking_mode"] = sub.ThinkingMode
		// max_context_tokens: from PerModelConfigs[model].MaxContext
		if pmc, ok := sub.PerModelConfigs[sub.Model]; ok && pmc.MaxContext > 0 {
			values["max_context_tokens"] = strconv.Itoa(pmc.MaxContext)
		}
	}

	return values
}

// ── write ────────────────────────────────────────────────────────────

// saveSettings persists changed values to their correct scope.
// Subscription keys → subscriptionMgr.Update
// User keys         → settingsSvc.SetSetting
// Then ApplySettings for runtime effect.
func (m *cliModel) saveSettings(values map[string]string) {
	if m.channel == nil {
		return
	}

	// --- Subscription-scoped writes (batch into one Update) ---
	subChanged := false
	sub := m.activeSubscription()
	if sub != nil && m.subscriptionMgr != nil {
		// Direct subscription fields
		for _, k := range []string{"llm_provider", "llm_api_key", "llm_base_url", "llm_model", "max_output_tokens", "thinking_mode"} {
			if v, ok := values[k]; ok {
				subChanged = true
				switch k {
				case "llm_provider":
					sub.Provider = v
				case "llm_api_key":
					if !strings.HasSuffix(v, "****") {
						sub.APIKey = v
					}
				case "llm_base_url":
					sub.BaseURL = v
				case "llm_model":
					sub.Model = v
				case "max_output_tokens":
					sub.MaxOutputTokens, _ = strconv.Atoi(v)
				case "thinking_mode":
					sub.ThinkingMode = v
				}
			}
		}
		// max_context_tokens → PerModelConfigs[model].MaxContext
		if v, ok := values["max_context_tokens"]; ok {
			model := sub.Model
			if model == "" {
				model = m.cachedModelName
			}
			if model != "" {
				n, _ := strconv.Atoi(strings.TrimSpace(v))
				if sub.PerModelConfigs == nil {
					sub.PerModelConfigs = make(map[string]PerModelConfig)
				}
				pmc := sub.PerModelConfigs[model]
				pmc.MaxContext = n
				sub.PerModelConfigs[model] = pmc
				subChanged = true
			}
		}
		if subChanged {
			if err := m.subscriptionMgr.Update(sub.ID, sub); err != nil {
				logrus.WithFields(logrus.Fields{"err": err, "sub": sub.ID}).Warn("saveSettings: subscription update failed")
			}
		}
	}

	// --- User-scoped DB writes ---
	if m.channel.settingsSvc != nil {
		for k, v := range values {
			if v != "" && IsUserScopedSettingKey(k) && !IsSubscriptionScopedSettingKey(k) {
				if err := m.channel.settingsSvc.SetSetting(m.channelName, m.senderID, k, v); err != nil {
					logrus.WithFields(logrus.Fields{"key": k, "err": err}).Warn("saveSettings: SetSetting failed")
				}
			}
		}
	}

	// --- Apply to runtime ---
	if m.channel.config.ApplySettings != nil {
		m.channel.config.ApplySettings(values, m.chatID)
	}
}

// ── resolve helpers ──────────────────────────────────────────────────

// activeSubscription returns a mutable copy of the active subscription, or nil.
func (m *cliModel) activeSubscription() *Subscription {
	if m.subscriptionMgr == nil || m.activeSubID == "" {
		// Fallback: try GetDefault
		if m.subscriptionMgr != nil {
			if sub, err := m.subscriptionMgr.GetDefault(m.senderID); err == nil && sub != nil {
				return sub
			}
		}
		return nil
	}
	subs, err := m.subscriptionMgr.List("")
	if err != nil {
		return nil
	}
	for _, s := range subs {
		if s.ID == m.activeSubID {
			cp := s
			return &cp
		}
	}
	return nil
}

// resolveMaxContext reads max_context from subscription.PerModelConfigs[model].
// Returns 0 if not set (caller uses schema default 200000).
func (m *cliModel) resolveMaxContext() int {
	sub := m.activeSubscription()
	if sub == nil {
		return 0
	}
	model := m.cachedModelName
	if model == "" {
		model = sub.Model
	}
	if model == "" {
		return 0
	}
	if pmc, ok := sub.PerModelConfigs[model]; ok && pmc.MaxContext > 0 {
		return pmc.MaxContext
	}
	return 0
}

// IsPerSessionSettingKey returns true if the key is a per-session setting.
// Currently empty — all settings are either subscription-scoped or user-scoped.
func IsPerSessionSettingKey(key string) bool { return false }

package cli

import (
	"strconv"
	"strings"
	ch "xbot/channel"

	"xbot/config"

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

// isTierSettingKey returns true if the key is one of the tier_* settings.
// Tier settings are registered in AllSettingDefs with ScopeUser but are
// persisted to DB only — no runtime handler needed. They are stripped from
// runtimeValues in saveSettings to avoid redundant DB writes in ApplySettings.
func isTierSettingKey(key string) bool {
	switch key {
	case "tier_vanguard", "tier_balance", "tier_swift":
		return true
	}
	return false
}

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
				if v != "" && ch.IsUserScopedSettingKey(k) {
					values[k] = v
				}
			}
		}
	}

	// 3. ch.Subscription-scoped fields (provider, key, model, max_output, max_context)
	// thinking_mode is NOT subscription-scoped anymore — it's a global user
	// setting (read in step 2 above via IsUserScopedSettingKey).
	sub := m.activeSubscription()
	if sub != nil {
		values["llm_provider"] = sub.Provider
		values["llm_api_key"] = sub.APIKey
		values["llm_base_url"] = sub.BaseURL
		values["llm_model"] = sub.Model
		values["max_output_tokens"] = strconv.Itoa(sub.MaxOutputTokens)
		// api_type: per-model config → subscription-level
		// Use the SESSION's active model for per-model override, matching
		// max_context_tokens behavior.
		apiModel := m.cachedModelName
		if apiModel == "" {
			apiModel = sub.Model
		}
		if apiModel != "" {
			if pmc, ok := sub.PerModelConfigs[apiModel]; ok && pmc.APIType != "" {
				values["api_type"] = pmc.APIType
			} else {
				values["api_type"] = sub.APIType
			}
		} else {
			values["api_type"] = sub.APIType
		}
		// max_context_tokens: per-model config → subscription-level → empty
		// Use the SESSION's active model (m.cachedModelName), NOT sub.Model.
		// Without this, the settings panel shows the subscription default model's
		// max_context instead of the currently selected model's value.
		model := m.cachedModelName
		if model == "" {
			model = sub.Model
		}
		if model != "" {
			if pmc, ok := sub.PerModelConfigs[model]; ok && pmc.MaxContext > 0 {
				values["max_context_tokens"] = strconv.Itoa(pmc.MaxContext)
			} else if sub.MaxContext > 0 {
				values["max_context_tokens"] = strconv.Itoa(sub.MaxContext)
			}
		}
	}

	return values
}

// ── write ────────────────────────────────────────────────────────────

// saveSettings persists changed values to their correct scope.
//
// CRITICAL: Each subscription-scoped key writes to the ACTIVE subscription (m.activeSubID),
// NEVER to GetDefault(). The settings panel reads from activeSubscription(), so writes
// MUST target the same subscription. Writing to the wrong subscription is a data-corruption
// bug (the old code always wrote to GetDefault(), destroying other subscriptions' configs).
//
// Write order:
//  1. ch.Subscription-scoped fields (provider, key, model, base_url, max_output, thinking)
//     → subscriptionMgr.Update(activeSubID) — one subscription, one call
//  2. PerModelConfigs (max_context_tokens) → subscriptionMgr.UpdatePerModelConfig(activeSubID)
//  3. User-scoped keys → settingsSvc
//  4. Runtime effects → ApplySettings (NO subscription keys — all handled above)
func (m *cliModel) saveSettings(values map[string]string) {
	if m.channel == nil {
		return
	}

	// --- ch.Subscription-scoped writes ---
	// Write directly to the ACTIVE subscription, not the default.
	// Only ONE subscription is updated per save — never batch.
	// CRITICAL: Only call Update when values have ACTUALLY changed.
	// Update triggers server-side Invalidate(senderID) which wipes ALL
	// per-session LLM cache entries, causing the agent to fall back to
	// the default subscription on the next message (wrong MaxContext → compression).
	if m.subscriptionMgr != nil {
		hasSubValues := false
		for _, k := range []string{"llm_provider", "llm_api_key", "llm_model", "llm_base_url"} {
			if v, ok := values[k]; ok && strings.TrimSpace(v) != "" {
				hasSubValues = true
				break
			}
		}

		if m.activeSubID != "" {
			activeSub := m.activeSubscription()
			if activeSub != nil {
				updated := *activeSub // copy existing to preserve unmasked fields
				changed := false

				if v, ok := values["llm_provider"]; ok && strings.TrimSpace(v) != "" && !strings.Contains(v, "****") {
					if updated.Provider != strings.TrimSpace(v) {
						updated.Provider = strings.TrimSpace(v)
						changed = true
					}
				}
				if v, ok := values["llm_api_key"]; ok && strings.TrimSpace(v) != "" {
					key := strings.TrimSpace(v)
					if !isMaskedAPIKey(key) && updated.APIKey != key {
						updated.APIKey = key
						changed = true
					}
				}
				if v, ok := values["llm_base_url"]; ok && strings.TrimSpace(v) != "" && !strings.Contains(v, "****") {
					if updated.BaseURL != strings.TrimSpace(v) {
						updated.BaseURL = strings.TrimSpace(v)
						changed = true
					}
				}
				if v, ok := values["max_output_tokens"]; ok {
					if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
						if updated.MaxOutputTokens != n {
							updated.MaxOutputTokens = n
							changed = true
						}
					}
				}
				// thinking_mode is no longer subscription-scoped; it's a global user
				// setting persisted below via settingsSvc.SetSetting.
				if v, ok := values["api_type"]; ok {
					if updated.APIType != v {
						updated.APIType = v
						changed = true
					}
				}

				if changed {
					if err := m.subscriptionMgr.Update(m.activeSubID, &updated); err != nil {
						logrus.WithFields(logrus.Fields{
							"err": err, "sub": m.activeSubID,
						}).Warn("saveSettings: subscription update failed")
					}
				}
			}
		} else if hasSubValues {
			// No active subscription — create one (first-run / wizard setup).
			newSub := &ch.Subscription{
				Name:   "default",
				Active: true,
			}
			if v, ok := values["llm_provider"]; ok {
				newSub.Provider = strings.TrimSpace(v)
			}
			if v, ok := values["llm_api_key"]; ok {
				newSub.APIKey = strings.TrimSpace(v)
			}
			if v, ok := values["llm_model"]; ok && strings.TrimSpace(v) != "" {
				// Model is user-level — stored in newSub.Model temporarily,
				// upserted to subscription_models after Add.
				newSub.Model = strings.TrimSpace(v)
			}
			if v, ok := values["llm_base_url"]; ok {
				newSub.BaseURL = strings.TrimSpace(v)
			}
			if newSub.Provider != "" && newSub.APIKey != "" {
				if err := m.subscriptionMgr.Add(newSub); err != nil {
					logrus.WithFields(logrus.Fields{"err": err}).Warn("saveSettings: subscription create failed")
				} else {
					// Activate the new subscription as the GLOBAL default.
					// Must pass chatID="" so the RPC handler takes the global
					// path (svc.SetDefault + SwitchSubscription), NOT the
					// per-session path which skips both.
					_ = m.subscriptionMgr.SetDefault(newSub.ID, "")
					// Also bind to the current session so GetSessionSubscription
					// finds it immediately and the LLM factory has a per-chat entry.
					_ = m.subscriptionMgr.SetDefault(newSub.ID, m.chatID)
					// Sync TUI caches immediately — applySessionLLMState is the
					// single source of truth for cachedModelName/activeSubID.
					// Without this, refreshCachedModelName may fire before
					// GetDefault reflects the new subscription (RPC timing),
					// letting auto-discover pick a wrong model from ListModels().
					m.activeSubID = newSub.ID
					m.applySessionLLMState(SessionLLMState{
						SubscriptionID: newSub.ID,
						Model:          "", // model is user-level, resolved from user_default_model
					})
				}
			}
		}
	}

	// --- PerModelConfigs update (max_context_tokens only) ---
	// Only call UpdatePerModelConfig when the value has ACTUALLY changed.
	// Unnecessary calls trigger DB write + potential side effects.
	if v, ok := values["max_context_tokens"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			if m.subscriptionMgr != nil && m.activeSubID != "" {
				model := m.cachedModelName
				if model == "" {
					if sub := m.activeSubscription(); sub != nil {
						model = sub.Model
					}
				}
				if model != "" {
					// Only write if the value actually differs from current config
					currentCtx := 0
					currentMaxOut := 0
					if sub := m.activeSubscription(); sub != nil {
						if pmc, ok := sub.PerModelConfigs[model]; ok {
							currentCtx = pmc.MaxContext
							currentMaxOut = pmc.MaxOutputTokens
						}
					}
					if n != currentCtx {
						// Preserve existing MaxOutputTokens when updating MaxContext.
						// PerModelConfig{MaxContext: n} alone would zero out MaxOutputTokens.
						pmc := ch.PerModelConfig{MaxContext: n, MaxOutputTokens: currentMaxOut}
						if err := m.subscriptionMgr.UpdatePerModelConfig(m.activeSubID, model, pmc); err != nil {
							logrus.WithFields(logrus.Fields{"err": err, "sub": m.activeSubID, "model": model}).Warn("saveSettings: UpdatePerModelConfig failed")
						}
						// Update local cache so the context bar reflects the new value immediately.
						m.cachedMaxContextTokens = n
						// DB is the single source of truth — no local JSON persistence.
						// SaveSessionLLMState only persists SubscriptionID + Model.
						existing := LoadSessionLLMState(m.workDir, m.chatID)
						SaveSessionLLMState(m.workDir, m.chatID, existing, m.remoteMode)
					}
				}
			}
		}
	}

	// --- User-scoped DB writes ---
	if m.channel.settingsSvc != nil {
		// thinking_mode is a global user toggle; "" (auto) is a valid, intentional
		// value, so persist it even when empty (the generic loop below skips "" to
		// avoid overwriting other keys with defaults). Without this, toggling back
		// to "auto" wouldn't clear a previously-stored "enabled".
		if v, ok := values["thinking_mode"]; ok && ch.IsUserScopedSettingKey("thinking_mode") {
			if err := m.channel.settingsSvc.SetSetting(m.channelName, m.senderID, "thinking_mode", v); err != nil {
				logrus.WithFields(logrus.Fields{"key": "thinking_mode", "err": err}).Warn("saveSettings: SetSetting failed")
			}
		}
		for k, v := range values {
			if k == "thinking_mode" {
				continue // handled above
			}
			if v != "" && ch.IsUserScopedSettingKey(k) && !ch.IsSubscriptionScopedSettingKey(k) {
				if err := m.channel.settingsSvc.SetSetting(m.channelName, m.senderID, k, v); err != nil {
					logrus.WithFields(logrus.Fields{"key": k, "err": err}).Warn("saveSettings: SetSetting failed")
				}
			}
		}
	}

	// --- Apply to runtime ---
	// Strip subscription-scoped keys (provider, key, model, etc.) — already handled above.
	// max_context_tokens is NOT passed to ApplySettings anymore. The PerModelConfig
	// write above already persists the value and calls Invalidate() on the server,
	// so the next GetLLMForChat re-reads from DB with the correct per-model value.
	// Passing it to ApplySettings would call ag.SetMaxContextTokens(n) without chatID,
	// setting the GLOBAL contextManagerConfig.MaxContextTokens — which contaminates
	// all models that don't have their own per-model override.
	if m.channel.config.ApplySettings != nil {
		runtimeValues := make(map[string]string, len(values))
		// Track whether LLM credentials were saved in this call.
		// saveSettings already persisted them via subscriptionMgr above,
		// but those subscription-scoped keys are stripped from runtimeValues.
		// Without signaling, ApplySettings can't detect that LLM config changed,
		// so CLISetupCompleted never gets set → setup panel re-appears on restart.
		llmCredsSaved := false
		for k, v := range values {
			if isSubscriptionScopedSettingKey(k) {
				if k == "llm_api_key" && strings.TrimSpace(v) != "" {
					llmCredsSaved = true
				}
				continue
			}
			// max_context_tokens is per-model (PerModelConfigs), already handled
			// above via UpdatePerModelConfig. Never pass it to ApplySettings —
			// that would call SetMaxContextTokens globally and contaminate all models.
			if k == "max_context_tokens" {
				continue
			}
			// Tier settings already persisted above via settingsSvc. Strip them
			// to avoid redundant DB write in main.go's ApplySettings loop.
			if isTierSettingKey(k) {
				continue
			}
			runtimeValues[k] = v
		}
		if llmCredsSaved {
			runtimeValues["__llm_creds_saved"] = "1"
		}
		m.channel.config.ApplySettings(runtimeValues, m.chatID)
	}
	// Refresh values cache with the ACTIVE subscription ID, not GetDefault().
	// ApplySettings internally calls refreshRemoteValuesCache("") which falls back
	// to GetDefaultSubscription(). Override with the correct session subscription.
	if m.channel != nil && m.channel.config.RefreshValuesCache != nil && m.activeSubID != "" {
		m.channel.config.RefreshValuesCache(m.activeSubID)
	}
}

// ── resolve helpers ──────────────────────────────────────────────────

// activeSubscription returns the subscription identified by m.activeSubID.
// Returns nil when activeSubID is empty — callers MUST handle the nil case.
// NEVER falls back to GetDefault() — the settings panel must show the ACTIVE
// subscription, not silently substitute the default.
func (m *cliModel) activeSubscription() *ch.Subscription {
	if m.subscriptionMgr == nil || m.activeSubID == "" {
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
	// Check user settings first — config tool writes max_context_tokens
	// to SettingsSvc (user_settings DB), not to subscription PerModelConfigs.
	// Same pattern as resolveMaxOutputTokens and resolveCompressRatio.
	if m.channel != nil && m.channel.config.GetCurrentValues != nil {
		if v, ok := m.channel.config.GetCurrentValues()["max_context_tokens"]; ok && v != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				return n
			}
		}
	}
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
	return config.DefaultMaxContextTokens
}

// IsPerSessionSettingKey returns true if the key is a per-session setting.
// Currently empty — all settings are either subscription-scoped or user-scoped.
func IsPerSessionSettingKey(key string) bool { return false }

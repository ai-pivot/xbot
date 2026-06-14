package cli

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// handleSwitchLLMDoneMsg processes async subscription switch completion.
// Returns (model, cmd, handled).
func (m *cliModel) handleSwitchLLMDoneMsg(done cliSwitchLLMDoneMsg) (tea.Model, tea.Cmd, bool) {
	returnToSettings := m.quickSwitchReturnToPanel
	m.quickSwitchReturnToPanel = false
	if done.err != nil {
		m.showTempStatus(fmt.Sprintf("Failed to switch LLM: %v", done.err))
		return m, nil, true
	}
	if done.mgr != nil {
		if err := done.mgr.SetDefault(done.subID, m.chatID); err != nil {
			m.showTempStatus(fmt.Sprintf("LLM switched but failed to save: %v", err))
		} else {
			m.showTempStatus(fmt.Sprintf("Switched to: %s (%s)", done.subName, done.subModel))
		}
		// Also update the global default subscription (is_default flag in DB)
		// so that new sessions inherit the last-used subscription.
		// The per-session call above (with chatID) only updates the LLM client
		// for this session; it does NOT touch is_default.
		_ = done.mgr.SetDefault(done.subID, "")
		// ALWAYS update per-session LLM state on successful switch, even if
		// SetDefault (global DB write) fails. The session must track its own
		// subscription regardless of global default persistence success.
		// Failure to update activeSubID is the root cause of the settings panel
		// showing the wrong subscription after a switch.
		// Determine the effective model: if this is a session restore and the
		// session has its own model choice, preserve it instead of using the
		// subscription's default model. Also sync the per-session model to the
		// server so GetLLMForChat returns the correct model for this session.
		effectiveModel := done.subModel
		if done.restoreModel != "" && done.restoreModel != done.subModel {
			effectiveModel = done.restoreModel
			// Sync per-session model to server (creates per-chat LLM entry)
			if m.llmSubscriber != nil {
				m.llmSubscriber.SwitchModel(m.senderID, effectiveModel, m.chatID)
			}
		}
		// Do NOT pass done.maxCtx/done.maxOutTok as MaxContextTokens/MaxOutputTokens.
		// Those values come from resolveSubMaxContext which uses the subscription's
		// DEFAULT model, not the session's model. Setting them here would poison
		// state.MaxContextTokens, and ResolveEffectiveMaxContext priority-1 returns
		// it directly, bypassing per-model lookup. Instead, let
		// applySessionLLMState → ResolveEffectiveMaxContext resolve from
		// PerModelConfigs (which now includes subscription_models data).
		state := SessionLLMState{
			SubscriptionID: done.subID,
			Model:          effectiveModel,
		}
		SaveSessionLLMState(m.workDir, m.chatID, state, m.remoteMode)
		m.applySessionLLMState(state)
		// Refresh values cache so GetCurrentValues() reflects the new subscription.
		if m.channel != nil && m.channel.config.RefreshValuesCache != nil {
			m.channel.config.RefreshValuesCache(done.subID)
		}
	}
	// If we came from the settings panel, re-open it so the user can continue editing
	if returnToSettings {
		m.openSettingsFromQuickSwitch()
	}
	// Drain pendingCmds (e.g. showTempStatus timer) — must not return nil cmds
	var cmd tea.Cmd
	if len(m.pendingCmds) > 0 {
		cmd = tea.Batch(m.pendingCmds...)
		m.pendingCmds = nil
	}
	return m, cmd, true
}

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
		_ = done.mgr.SetDefault(done.subID, "")
		// Restore per-session model: SetDefault(subID, chatID) creates the per-chat
		// entry using the subscription's default model (via SetSessionLLM). If the
		// session had a different model choice (e.g. user switched via Ctrl+N),
		// SwitchModel overrides the per-chat entry's model field AND persists the
		// correct model back to the tenants table. This is idempotent when
		// done.subModel equals the subscription default.
		if done.subModel != "" && m.llmSubscriber != nil {
			m.llmSubscriber.SwitchModel(m.senderID, done.subModel, m.chatID)
		}
		// Update per-session LLM state.
		state := SessionLLMState{
			SubscriptionID: done.subID,
			Model:          done.subModel,
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

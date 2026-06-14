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
		// Update per-session LLM state. The subscription's model is authoritative —
		// per-session model overrides (Ctrl+N) are handled by cycleModel which
		// saves directly to session JSON + calls SwitchModel RPC.
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

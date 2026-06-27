package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	ch "xbot/channel"

	tea "charm.land/bubbletea/v2"

	"xbot/config"
)

// openQuickSwitch opens the quick switch overlay for subscription or model selection.
func (m *cliModel) openQuickSwitch(mode string) {
	if m.subscriptionMgr == nil {
		return
	}
	subs, err := m.subscriptionMgr.List("")
	if err != nil || len(subs) == 0 {
		// Even with no subscriptions, allow adding one
		subs = nil
	}

	m.quickSwitchMode = mode
	m.quickSwitchList = subs
	m.quickSwitchCursor = 0

	// Append "Add subscription" entry for subscription mode
	if mode == "subscription" {
		m.quickSwitchList = append(m.quickSwitchList, ch.Subscription{
			ID:   "__add__",
			Name: "➕ Add subscription",
		})
	}

	// Pre-select the active subscription (per-session, not DB default)
	if m.activeSubID != "" {
		for i, s := range subs {
			if s.ID == m.activeSubID {
				m.quickSwitchCursor = i
				break
			}
		}
	} else {
		for i, s := range subs {
			if s.Active {
				m.quickSwitchCursor = i
				break
			}
		}
	}
}

// applyQuickSwitch applies the selected item from the quick switch overlay.
// For subscription switches, the LLM creation (which may hit the network) runs
// asynchronously so the UI never freezes.
func (m *cliModel) applyQuickSwitch() {
	if m.quickSwitchCursor >= len(m.quickSwitchList) {
		m.quickSwitchMode = ""
		return
	}
	selected := m.quickSwitchList[m.quickSwitchCursor]

	// "Add subscription" entry — open a mini settings panel
	if selected.ID == "__add__" {
		m.quickSwitchMode = ""
		addSchema := []ch.SettingDefinition{
			{Key: "sub_name", Label: "Name", Description: "Display name for this subscription", Type: ch.SettingTypeText, DefaultValue: ""},
			{Key: "sub_provider", Label: "Provider", Description: "LLM provider (openai, anthropic, deepseek, etc.)", Type: ch.SettingTypeText, DefaultValue: "openai"},
			{Key: "sub_model", Label: "Model", Description: "Model name", Type: ch.SettingTypeText, DefaultValue: ""},
			{Key: "sub_base_url", Label: "Base URL", Description: "API base URL (leave empty for provider default)", Type: ch.SettingTypeText, DefaultValue: ""},
			{Key: "sub_api_key", Label: "API Key", Description: "API key (leave empty to use global key)", Type: ch.SettingTypePassword, DefaultValue: ""},
			{Key: "sub_max_output_tokens", Label: "Max Output Tokens", Description: fmt.Sprintf("Default max output tokens (0 = use %d)", config.DefaultMaxOutputTokens), Type: ch.SettingTypeNumber, DefaultValue: "0"},
			{Key: "sub_thinking_mode", Label: "Thinking Mode", Description: "Thinking/reasoning mode", Type: ch.SettingTypeSelect, DefaultValue: "", Options: []ch.SettingOption{
				{Label: "Auto (default)", Value: ""},
				{Label: "Enabled", Value: "enabled"},
				{Label: "Disabled", Value: "disabled"},
			}},
		}
		// Inject model list into combo for model field
		if m.channel.modelLister != nil {
			models := m.channel.modelLister.ListModels()
			if len(models) > 0 {
				opts := make([]ch.SettingOption, len(models))
				for j, mdl := range models {
					opts[j] = ch.SettingOption{Label: mdl, Value: mdl}
				}
				addSchema[2].Options = opts
			}
		}
		m.openSettingsPanel(addSchema, map[string]string{}, func(values map[string]string) {
			name := values["sub_name"]
			if name == "" {
				name = values["sub_provider"]
			}
			if name == "" {
				name = "unnamed"
			}
			maxOut, _ := strconv.Atoi(values["sub_max_output_tokens"])
			sub := &ch.Subscription{
				ID:              fmt.Sprintf("sub_%d", time.Now().UnixNano()),
				Name:            name,
				Provider:        values["sub_provider"],
				BaseURL:         values["sub_base_url"],
				APIKey:          values["sub_api_key"],
				Model:           values["sub_model"],
				MaxOutputTokens: maxOut,
				ThinkingMode:    values["sub_thinking_mode"],
				Active:          false,
			}
			if err := m.subscriptionMgr.Add(sub); err != nil {
				m.showTempStatus(fmt.Sprintf("Failed to add subscription: %v", err))
			} else {
				m.showTempStatus(fmt.Sprintf("Added subscription: %s (%s)", sub.Name, sub.Model))
			}
		})
		return
	}

	switch m.quickSwitchMode {
	case "subscription":
		if m.subscriptionMgr == nil {
			break
		}
		// Find the full subscription config first
		var target *ch.Subscription
		if subs, err := m.subscriptionMgr.List(""); err == nil {
			for i := range subs {
				if subs[i].ID == selected.ID {
					target = &subs[i]
					break
				}
			}
		}
		if target == nil {
			m.showTempStatus("ch.Subscription not found")
			break
		}
		if m.channel == nil || m.channel.config.SwitchLLM == nil {
			break
		}
		// ── IMMEDIATE frontend state update ──────────────────────
		// Must happen synchronously before the async SwitchLLM call.
		// Without this, activeSubID and cachedModelName stay stale between
		// Enter press and handleSwitchLLMDoneMsg processing. The settings
		// panel and context bar read from these fields, so they would show
		// the OLD subscription until the async callback completes.
		m.activeSubID = selected.ID
		m.cachedModelName = selected.Model
		m.subGeneration++ // subscription actually changed
		// Persist immediately so refreshCachedModelName (called on settings save)
		// loads the correct state from disk instead of stale old data.
		state := SessionLLMState{
			SubscriptionID:   selected.ID,
			Model:            selected.Model,
			MaxContextTokens: resolveSubMaxContext(target),
			MaxOutputTokens:  resolveSubMaxOutputTokens(target),
		}
		SaveSessionLLMState(m.workDir, m.chatID, state, m.remoteMode)
		m.applySessionLLMState(state)
		// ── Async backend switch ─────────────────────────────────
		m.showTempStatus(fmt.Sprintf("Switching to: %s …", selected.Name))
		switchFn := m.channel.config.SwitchLLM
		subID := selected.ID
		subName := selected.Name
		subModel := selected.Model
		mgr := m.subscriptionMgr
		chatID := m.chatID // capture before goroutine
		m.pendingCmds = append(m.pendingCmds, func() tea.Msg {
			// Set per-chat cache entry FIRST, before creating the LLM client.
			// Without this, a user message arriving before SetDefault completes
			// hits GetLLMForChat with no per-chat entry, falls back to the
			// user-level entry (still pointing to the OLD subscription), and
			// maybeCompress uses the old MaxContext.
			if mgr != nil {
				_ = mgr.SetDefault(subID, chatID)
			}
			err := switchFn(target.Provider, target.BaseURL, target.APIKey, target.Model)
			return cliSwitchLLMDoneMsg{
				err:       err,
				subID:     subID,
				subName:   subName,
				subModel:  subModel,
				maxCtx:    resolveSubMaxContext(target),
				maxOutTok: resolveSubMaxOutputTokens(target),
				mgr:       mgr,
			}
		})
	case "model":
		if m.llmSubscriber != nil {
			m.llmSubscriber.SwitchModel(m.senderID, selected.Model, m.chatID)
			m.cachedModelName = selected.Model
			m.subGeneration++ // model switch also changes effective subscription state
			// Re-resolve context/output token limits for the new model.
			newState := SessionLLMState{
				SubscriptionID: m.activeSubID,
				Model:          selected.Model,
			}
			m.cachedMaxContextTokens = ResolveEffectiveMaxContext(newState, m.subscriptionMgr)
			m.cachedMaxOutputTokens = int64(ResolveEffectiveMaxOutputTokens(newState, m.subscriptionMgr))
			// Update quickSwitchList so the panel reflects the new model
			m.updateQuickSwitchModels(selected.Model)
			// Persist per-session model choice so it survives restarts.
			// Use resolved values (not stale cached ones) so the saved state
			// reflects the new model's effective limits.
			SaveSessionLLMState(m.workDir, m.chatID, SessionLLMState{
				SubscriptionID:   m.activeSubID,
				Model:            selected.Model,
				MaxContextTokens: m.cachedMaxContextTokens,
				MaxOutputTokens:  int(m.cachedMaxOutputTokens),
			}, m.remoteMode)
			m.showTempStatus(fmt.Sprintf("Model switched to: %s", selected.Model))
		}
	}

	m.quickSwitchMode = ""
}

// editQuickSwitchEntry opens a mini panel to edit all fields of the selected subscription.
func (m *cliModel) editQuickSwitchEntry() {
	if m.quickSwitchCursor >= len(m.quickSwitchList) {
		return
	}
	selected := m.quickSwitchList[m.quickSwitchCursor]
	if selected.ID == "__add__" {
		return
	}
	// Find the full subscription config (including APIKey) from the manager
	var target *ch.Subscription
	if m.subscriptionMgr != nil {
		if subs, err := m.subscriptionMgr.List(""); err == nil {
			for i := range subs {
				if subs[i].ID == selected.ID {
					target = &subs[i]
					break
				}
			}
		}
	}
	if target == nil {
		m.showTempStatus("ch.Subscription not found")
		return
	}

	editSchema := []ch.SettingDefinition{
		{Key: "sub_name", Label: "Name", Description: "Display name for this subscription", Type: ch.SettingTypeText, DefaultValue: target.Name},
		{Key: "sub_provider", Label: "Provider", Description: "LLM provider (openai, anthropic, deepseek, etc.)", Type: ch.SettingTypeText, DefaultValue: target.Provider},
		{Key: "sub_model", Label: "Model", Description: "Model name", Type: ch.SettingTypeCombo, DefaultValue: target.Model},
		{Key: "sub_base_url", Label: "Base URL", Description: "API base URL (leave empty for provider default)", Type: ch.SettingTypeText, DefaultValue: target.BaseURL},
		{Key: "sub_api_key", Label: "API Key", Description: "API key (leave empty to use global key)", Type: ch.SettingTypePassword, DefaultValue: target.APIKey},
		{Key: "sub_max_output_tokens", Label: "Max Output Tokens", Description: fmt.Sprintf("Default max output tokens (0 = use %d)", config.DefaultMaxOutputTokens), Type: ch.SettingTypeNumber, DefaultValue: strconv.Itoa(target.MaxOutputTokens)},
		{Key: "sub_thinking_mode", Label: "Thinking Mode", Description: "Thinking/reasoning mode", Type: ch.SettingTypeSelect, DefaultValue: target.ThinkingMode, Options: []ch.SettingOption{
			{Label: "Auto (default)", Value: ""},
			{Label: "Enabled", Value: "enabled"},
			{Label: "Disabled", Value: "disabled"},
		}},
		{Key: "__pm_header__", Label: "─── Model-Specific Overrides ───", Description: "Override max tokens and context per model. Set 0 to use subscription default.", Type: ch.SettingTypeText, DefaultValue: ""},
	}
	// Build per-model override rows: only models that belong to THIS subscription.
	// Use target.Model + keys from existing PerModelConfigs (not ListAllModels which
	// returns models from ALL subscriptions).
	subModels := make(map[string]bool)
	if target.Model != "" {
		subModels[target.Model] = true
	}
	for mdl := range target.PerModelConfigs {
		subModels[mdl] = true
	}
	for mdl := range subModels {
		pmOut := 0
		pmCtx := 0
		pmEnabled := true // default enabled for models without an explicit row
		if target.PerModelConfigs != nil {
			if cfg, ok := target.PerModelConfigs[mdl]; ok {
				pmOut = cfg.MaxOutputTokens
				pmCtx = cfg.MaxContext
				pmEnabled = cfg.Enabled
			}
		}
		editSchema = append(editSchema, ch.SettingDefinition{
			Key: "pm_" + mdl + "_max_output", Label: mdl + " Max Tokens",
			Description: "Max output tokens for " + mdl + " (0 = use default)",
			Type:        ch.SettingTypeNumber, DefaultValue: strconv.Itoa(pmOut),
		})
		editSchema = append(editSchema, ch.SettingDefinition{
			Key: "pm_" + mdl + "_max_context", Label: mdl + " Max Context",
			Description: "Max context tokens for " + mdl + " (0 = use default)",
			Type:        ch.SettingTypeNumber, DefaultValue: strconv.Itoa(pmCtx),
		})
		enabledDef := "enabled"
		if !pmEnabled {
			enabledDef = "disabled"
		}
		editSchema = append(editSchema, ch.SettingDefinition{
			Key: "pm_" + mdl + "_enabled", Label: mdl + " Enabled",
			Description: "Disabled models are hidden from cycling and rejected on switch",
			Type:        ch.SettingTypeSelect, DefaultValue: enabledDef,
			Options: []ch.SettingOption{
				{Label: "Enabled", Value: "enabled"},
				{Label: "Disabled", Value: "disabled"},
			},
		})
	}
	editValues := map[string]string{
		"sub_name":              target.Name,
		"sub_provider":          target.Provider,
		"sub_model":             target.Model,
		"sub_base_url":          target.BaseURL,
		"sub_api_key":           target.APIKey,
		"sub_max_output_tokens": strconv.Itoa(target.MaxOutputTokens),
		"sub_thinking_mode":     target.ThinkingMode,
	}
	m.quickSwitchMode = "" // close overlay while editing
	m.openSettingsPanel(editSchema, editValues, func(values map[string]string) {
		if m.subscriptionMgr == nil {
			return
		}
		apiKey := values["sub_api_key"]
		// Never write back a masked API key — it would destroy the real key in storage.
		if isMaskedAPIKey(apiKey) {
			apiKey = target.APIKey
		}
		maxOut, _ := strconv.Atoi(values["sub_max_output_tokens"])
		// Collect per-model overrides: only models belonging to THIS subscription
		perModelConfigs := make(map[string]ch.PerModelConfig)
		for mdl := range target.PerModelConfigs {
			pmOut, _ := strconv.Atoi(values["pm_"+mdl+"_max_output"])
			pmCtx, _ := strconv.Atoi(values["pm_"+mdl+"_max_context"])
			if pmOut > 0 || pmCtx > 0 {
				perModelConfigs[mdl] = ch.PerModelConfig{MaxOutputTokens: pmOut, MaxContext: pmCtx}
			}
		}
		// Also check the current model (may have been newly added)
		if modelFromCombo := values["sub_model"]; modelFromCombo != "" {
			pmOut, _ := strconv.Atoi(values["pm_"+modelFromCombo+"_max_output"])
			pmCtx, _ := strconv.Atoi(values["pm_"+modelFromCombo+"_max_context"])
			if pmOut > 0 || pmCtx > 0 {
				perModelConfigs[modelFromCombo] = ch.PerModelConfig{MaxOutputTokens: pmOut, MaxContext: pmCtx}
			}
		}
		updated := &ch.Subscription{
			ID:              target.ID,
			Name:            values["sub_name"],
			Provider:        values["sub_provider"],
			Model:           values["sub_model"],
			BaseURL:         values["sub_base_url"],
			APIKey:          apiKey,
			MaxOutputTokens: maxOut,
			ThinkingMode:    values["sub_thinking_mode"],
			PerModelConfigs: perModelConfigs,
			Active:          target.Active,
		}
		if err := m.subscriptionMgr.Update(target.ID, updated); err != nil {
			m.showTempStatus(fmt.Sprintf("Failed to update: %v", err))
		} else {
			m.showTempStatus(fmt.Sprintf("Updated: %s", updated.Name))
		}
		// Apply per-model enable/disable toggles. SetModelEnabled is independent of
		// Update (which writes token overrides), so a failed Update still won't lose
		// the enabled state and vice versa.
		for mdl := range subModels {
			wantEnabled := values["pm_"+mdl+"_enabled"] != "disabled"
			origEnabled := true
			if cfg, ok := target.PerModelConfigs[mdl]; ok {
				origEnabled = cfg.Enabled
			}
			if wantEnabled != origEnabled {
				if err := m.subscriptionMgr.SetModelEnabled(target.ID, mdl, wantEnabled); err != nil {
					m.showTempStatus(fmt.Sprintf("Failed to %s %s: %v", map[bool]string{true: "enable", false: "disable"}[wantEnabled], mdl, err))
				}
			}
		}
	})
}

// deleteQuickSwitchEntry deletes the selected subscription (with confirmation if it's active).
func (m *cliModel) deleteQuickSwitchEntry() {
	if m.quickSwitchCursor >= len(m.quickSwitchList) {
		return
	}
	selected := m.quickSwitchList[m.quickSwitchCursor]
	if selected.ID == "__add__" {
		return
	}
	if m.subscriptionMgr == nil {
		return
	}
	subs, err := m.subscriptionMgr.List("")
	if err != nil || len(subs) <= 1 {
		m.showTempStatus("Cannot delete the last subscription")
		return
	}
	// Don't allow deleting the per-session active subscription without a fallback.
	// Check m.activeSubID first (per-session), then fall back to Active flag (global default).
	activeID := m.activeSubID
	if activeID == "" {
		for _, s := range subs {
			if s.Active {
				activeID = s.ID
				break
			}
		}
	}
	if selected.ID == activeID {
		m.showTempStatus("Cannot delete active subscription — switch to another first")
		return
	}
	if err := m.subscriptionMgr.Remove(selected.ID); err != nil {
		m.showTempStatus(fmt.Sprintf("Failed to delete: %v", err))
		return
	}
	m.showTempStatus(fmt.Sprintf("Deleted: %s", selected.Name))
	// Refresh the list
	m.openQuickSwitch(m.quickSwitchMode)
}

// updateQuickSwitchModels updates the model field in quickSwitchList for the active subscription.
func (m *cliModel) updateQuickSwitchModels(newModel string) {
	if len(m.quickSwitchList) == 0 {
		return
	}
	// Use m.activeSubID (per-session) to find the current subscription,
	// NOT Active flag (global default). The quick-switch list must reflect
	// the session's active subscription.
	if m.activeSubID != "" {
		for i := range m.quickSwitchList {
			if m.quickSwitchList[i].ID == m.activeSubID {
				m.quickSwitchList[i].Model = newModel
				return
			}
		}
	}
	// Fallback: if no per-session subscription, use global default.
	for i := range m.quickSwitchList {
		if m.quickSwitchList[i].Active {
			m.quickSwitchList[i].Model = newModel
			return
		}
	}
}

func (m *cliModel) viewQuickSwitch(width, height int) string {
	if m.quickSwitchMode == "" || len(m.quickSwitchList) == 0 {
		return ""
	}

	title := "Switch ch.Subscription"
	if m.quickSwitchMode == "model" {
		title = "Switch Model"
	}

	var lines []string

	// Header
	lines = append(lines, m.styles.PanelHeader.Render(title))
	lines = append(lines, "") // spacer

	// Items
	for i, s := range m.quickSwitchList {
		// Separator before "Add" entry
		if s.ID == "__add__" && i > 0 {
			lines = append(lines, m.styles.TextMutedSt.Render(" ─────────────────────────────────"))
		}
		cursor := " "
		style := m.styles.TextMutedSt
		if i == m.quickSwitchCursor {
			cursor = "▸"
			style = m.styles.Accent
		}
		active := ""
		if m.activeSubID != "" {
			if s.ID == m.activeSubID {
				active = " ✓"
			}
		} else if s.Active {
			active = " ✓"
		}
		name := s.Name
		if name == "" {
			name = s.ID
		}
		line := style.Render(fmt.Sprintf(" %s %-30s %-16s%s", cursor, name, s.Model, active))
		lines = append(lines, line)
	}

	// Build panel with border
	panelContent := strings.Join(lines, "\n")
	box := m.styles.PanelBox.Render(panelContent)

	// Hint line below the box
	hint := m.styles.PanelHint.Render(" ↑↓ Navigate  Enter Select  E Edit  D Delete  Esc Close")

	// Center vertically
	sepLines := 0
	for _, s := range m.quickSwitchList {
		if s.ID == "__add__" {
			sepLines = 1
			break
		}
	}
	listH := len(m.quickSwitchList) + 3 + sepLines // header + spacer + items + separator + borders(~2)
	blankLines := max(0, (height-listH)/2)
	var b strings.Builder
	for i := 0; i < blankLines; i++ {
		b.WriteString("\n")
	}
	b.WriteString(box)
	b.WriteString("\n")
	b.WriteString(hint)

	return b.String()
}

// handleQuickSwitchKey handles key events for the quick switch overlay.
// Returns (handled, cmd). Called from Update() BEFORE panelMode check
// so quick switch has higher priority than panels.
func (m *cliModel) handleQuickSwitchKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if m.quickSwitchMode == "" {
		return false, nil
	}
	switch msg.Code {
	case tea.KeyEsc:
		returnToSettings := m.quickSwitchReturnToPanel
		m.quickSwitchReturnToPanel = false
		m.quickSwitchMode = ""
		if returnToSettings {
			m.openSettingsFromQuickSwitch()
		}
		return true, nil
	case tea.KeyUp:
		if m.quickSwitchCursor > 0 {
			m.quickSwitchCursor--
		}
		return true, nil
	case tea.KeyDown:
		if m.quickSwitchCursor < len(m.quickSwitchList)-1 {
			m.quickSwitchCursor++
		}
		return true, nil
	case tea.KeyEnter:
		m.applyQuickSwitch()
		if len(m.pendingCmds) > 0 {
			pending := m.pendingCmds
			m.pendingCmds = nil
			return true, tea.Batch(pending...)
		}
		return true, nil
	}
	// E: edit selected subscription
	if msg.String() == "e" {
		m.editQuickSwitchEntry()
		return true, nil
	}
	// D: delete selected subscription
	if msg.String() == "d" {
		m.deleteQuickSwitchEntry()
		return true, nil
	}
	return true, nil // block all other keys
}

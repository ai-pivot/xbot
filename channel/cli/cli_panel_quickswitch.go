package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	ch "xbot/channel"
	"xbot/protocol"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"xbot/config"
)

// openQuickSwitch opens the quick switch overlay for subscription or model selection.
//
// Model-first redesign: "model" mode is the primary selection mechanism and lists
// every selectable model across ALL enabled subscriptions (ListAllModels). Picking
// a model implicitly selects its owning subscription (resolved by the backend).
// "subscription" mode is management-only: add / disable / delete — there is no
// "switch subscription" action; subscriptions are credential sources, not the
// thing you run.
func (m *cliModel) openQuickSwitch(mode string) {
	if m.subscriptionMgr == nil {
		return
	}
	m.quickSwitchMode = mode
	m.quickSwitchList = nil
	m.quickSwitchCursor = 0

	if mode == "model" {
		// Cross-subscription model picker. Each entry pairs a model with its owning
		// subscription so the UI can show "订阅名 · 模型名". Render immediately from the
		// DB snapshot, then kick off an async /models refresh so the list reflects
		// each provider's true available models (not just the persisted cache).
		var entries []protocol.ModelEntry
		if m.channel != nil && m.channel.modelLister != nil {
			m.channel.modelLister.EnsureModelsLoaded()
			entries = m.channel.modelLister.ListAllModelEntries()
		}
		m.quickSwitchModelEntries = entries
		if len(entries) == 0 {
			m.quickSwitchMode = ""
			m.showTempStatus("No models available — add a subscription first")
			return
		}
		ti := textinput.New()
		ti.Placeholder = "Filter models…"
		ti.Prompt = " > "
		ti.CharLimit = 80
		ti.SetWidth(40)
		ti.Focus()
		m.quickSwitchFilterInput = ti
		m.applyQuickSwitchFilter()
		m.cursorToActiveModel()
		// Background refresh: fetch /models for every enabled subscription,
		// persist to CachedModels, then push the fresh entries back. The UI
		// shows a spinner until the refresh reply arrives.
		m.quickSwitchRefreshing = true
		m.pendingCmds = append(m.pendingCmds, m.refreshModelEntriesCmd())
		return
	}

	// subscription mode: list subscriptions + an "Add" entry.
	subs, err := m.subscriptionMgr.List("")
	if err != nil || len(subs) == 0 {
		subs = nil
	}
	m.quickSwitchList = subs
	m.quickSwitchList = append(m.quickSwitchList, ch.Subscription{
		ID:   "__add__",
		Name: "➕ Add subscription",
	})
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

// applyQuickSwitchFilter rebuilds quickSwitchModelFiltered from
// quickSwitchModelEntries using the current filter input (case-insensitive
// substring match on subscription name or model name). It preserves the cursor
// when still valid, else clamps — so a background refresh doesn't yank the
// cursor. Callers set the cursor explicitly (open → active model, type → top).
func (m *cliModel) applyQuickSwitchFilter() {
	q := strings.ToLower(strings.TrimSpace(m.quickSwitchFilterInput.Value()))
	m.quickSwitchModelFiltered = m.quickSwitchModelFiltered[:0]
	for _, e := range m.quickSwitchModelEntries {
		if q == "" || strings.Contains(strings.ToLower(e.SubName), q) || strings.Contains(strings.ToLower(e.Model), q) {
			m.quickSwitchModelFiltered = append(m.quickSwitchModelFiltered, e)
		}
	}
	if m.quickSwitchCursor >= len(m.quickSwitchModelFiltered) {
		m.quickSwitchCursor = max(0, len(m.quickSwitchModelFiltered)-1)
	}
}

// cursorToActiveModel parks the cursor on the currently active model (used on
// open, when no filter is typed).
func (m *cliModel) cursorToActiveModel() {
	for i, e := range m.quickSwitchModelFiltered {
		if e.Model == m.cachedModelName {
			m.quickSwitchCursor = i
			return
		}
	}
	m.quickSwitchCursor = 0
}

// applyQuickSwitch applies the selected item from the quick switch overlay.
// For subscription switches, the LLM creation (which may hit the network) runs
// asynchronously so the UI never freezes.
func (m *cliModel) applyQuickSwitch() {
	// Model picker: select from the filtered entry list. The backend resolves the
	// owning subscription and persists (ownerSubID, model) to tenants; applyModelSwitch
	// re-reads the owner so activeSubID/context limits stay correct.
	if m.quickSwitchMode == "model" {
		if m.quickSwitchCursor < 0 || m.quickSwitchCursor >= len(m.quickSwitchModelFiltered) {
			m.quickSwitchMode = ""
			return
		}
		entry := m.quickSwitchModelFiltered[m.quickSwitchCursor]
		m.quickSwitchMode = ""
		m.applyModelSwitch(entry.Model)
		return
	}
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
		// Find the full subscription config (carries Enabled).
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
		// Management-only action: toggle enabled. A disabled subscription stops
		// contributing models to the picker; credentials are preserved so
		// re-enabling is lossless. There is no "switch subscription" action.
		wantEnabled := !target.Enabled
		if err := m.subscriptionMgr.SetSubscriptionEnabled(target.ID, wantEnabled); err != nil {
			m.showTempStatus(fmt.Sprintf("Failed to toggle: %v", err))
			break
		}
		verb := "Disabled"
		if wantEnabled {
			verb = "Enabled"
		}
		warning := ""
		if !wantEnabled && (target.ID == m.activeSubID || (m.activeSubID == "" && target.Active)) {
			warning = " — active session's models hidden; switch model to continue"
		}
		m.showTempStatus(fmt.Sprintf("%s: %s%s", verb, target.Name, warning))
		// Refresh the list so the enabled/disabled marker updates, and keep the
		// panel open so the user can manage more subscriptions.
		m.openQuickSwitch(m.quickSwitchMode)
		return
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
		m.showTempStatus("Cannot delete the active subscription — switch model first")
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
	if m.quickSwitchMode == "" {
		return ""
	}
	modelMode := m.quickSwitchMode == "model"
	if modelMode && len(m.quickSwitchModelEntries) == 0 {
		return ""
	}
	if !modelMode && len(m.quickSwitchList) == 0 {
		return ""
	}

	title := "Manage Subscriptions"
	if modelMode {
		title = "Switch Model"
	}

	var lines []string

	// Header
	lines = append(lines, m.styles.PanelHeader.Render(title))
	lines = append(lines, "") // spacer

	if modelMode {
		// Filter input
		lines = append(lines, m.quickSwitchFilterInput.View())
		if m.quickSwitchRefreshing {
			lines = append(lines, m.styles.TextMutedSt.Render("  ↻ 刷新模型列表…"))
		} else {
			lines = append(lines, "") // spacer (keeps layout stable across refresh states)
		}
		filtered := m.quickSwitchModelFiltered
		if len(filtered) == 0 {
			lines = append(lines, m.styles.TextMutedSt.Render("  No matching models"))
		}
		for i, e := range filtered {
			cursor := " "
			style := m.styles.TextMutedSt
			if i == m.quickSwitchCursor {
				cursor = "▸"
				style = m.styles.Accent
			}
			label := e.Model
			if e.SubName != "" {
				label = e.SubName + " · " + e.Model
			}
			mark := ""
			if e.Model == m.cachedModelName {
				mark = " ✓"
			}
			lines = append(lines, style.Render(fmt.Sprintf(" %s %s%s", cursor, label, mark)))
		}
	} else {
		// subscription mode items
		for i, s := range m.quickSwitchList {
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
			disabledTag := ""
			if !s.Enabled {
				disabledTag = " (disabled)"
			}
			lines = append(lines, style.Render(fmt.Sprintf(" %s %-30s %-16s%s%s", cursor, name, s.Model, disabledTag, active)))
		}
	}

	// Build panel with border
	panelContent := strings.Join(lines, "\n")
	box := m.styles.PanelBox.Render(panelContent)

	// Hint line below the box
	hint := m.styles.PanelHint.Render(" ↑↓ Navigate  Enter Select  Esc Close")
	if modelMode {
		hint = m.styles.PanelHint.Render(" Type to filter  ↑↓ Navigate  Enter Select  Esc Close")
	} else {
		hint = m.styles.PanelHint.Render(" ↑↓ Navigate  Enter Enable/Disable  E Edit  D Delete  Esc Close")
	}

	// Center vertically
	itemCount := len(m.quickSwitchList)
	extra := 0
	if modelMode {
		itemCount = len(m.quickSwitchModelFiltered)
		extra = 2 // filter input + spacer
		if itemCount == 0 {
			itemCount = 1 // "No matching models" line
		}
	}
	sepLines := 0
	if !modelMode {
		for _, s := range m.quickSwitchList {
			if s.ID == "__add__" {
				sepLines = 1
				break
			}
		}
	}
	listH := itemCount + 3 + extra + sepLines // header + spacer + items + extra + separator + borders(~2)
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
	// Model picker: filterable list. Esc/Up/Down/Enter navigate; all other keys
	// (printable, backspace) feed the filter input and re-apply the filter.
	if m.quickSwitchMode == "model" {
		switch msg.Code {
		case tea.KeyEsc:
			m.quickSwitchMode = ""
			return true, nil
		case tea.KeyUp:
			if m.quickSwitchCursor > 0 {
				m.quickSwitchCursor--
			}
			return true, nil
		case tea.KeyDown:
			if m.quickSwitchCursor < len(m.quickSwitchModelFiltered)-1 {
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
		var cmd tea.Cmd
		m.quickSwitchFilterInput, cmd = m.quickSwitchFilterInput.Update(msg)
		m.applyQuickSwitchFilter()
		// Jump to the top match while typing (standard filter UX).
		if strings.TrimSpace(m.quickSwitchFilterInput.Value()) != "" {
			m.quickSwitchCursor = 0
		}
		return true, cmd
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
	// E: edit selected subscription (subscription mode only — models are edited
	// inside their owning subscription's edit panel).
	if msg.String() == "e" && m.quickSwitchMode == "subscription" {
		m.editQuickSwitchEntry()
		return true, nil
	}
	// D: delete selected subscription (subscription mode only).
	if msg.String() == "d" && m.quickSwitchMode == "subscription" {
		m.deleteQuickSwitchEntry()
		return true, nil
	}
	return true, nil // block all other keys
}

// cliModelEntriesRefreshedMsg carries the fresh model entry list after a
// background /models refresh of every enabled subscription.
type cliModelEntriesRefreshedMsg struct {
	entries []protocol.ModelEntry
}

// refreshModelEntriesCmd issues the backend refresh (blocking RPC) in a
// goroutine and emits cliModelEntriesRefreshedMsg with the fresh entries.
func (m *cliModel) refreshModelEntriesCmd() tea.Cmd {
	return func() tea.Msg {
		var entries []protocol.ModelEntry
		if m.channel != nil && m.channel.modelLister != nil {
			entries = m.channel.modelLister.RefreshModelEntries()
		}
		return cliModelEntriesRefreshedMsg{entries: entries}
	}
}

// handleModelEntriesRefreshed applies the fresh entry list to the picker if it
// is still open in model mode. Preserves the current filter text and cursor
// position (clamped if the filtered set shrank).
func (m *cliModel) handleModelEntriesRefreshed(msg cliModelEntriesRefreshedMsg) {
	m.quickSwitchRefreshing = false
	if m.quickSwitchMode != "model" || len(msg.entries) == 0 {
		return
	}
	m.quickSwitchModelEntries = msg.entries
	m.applyQuickSwitchFilter()
}

package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
	ch "xbot/channel"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"xbot/internal/textarea"
)

var dangerConfirmStrings = map[string]string{
	"session":       "DELETE-SESSION",
	"core_persona":  "DELETE-PERSONA",
	"core_human":    "DELETE-HUMAN",
	"core_working":  "DELETE-WORKING",
	"core_all":      "DELETE-CORE-MEMORY",
	"long_term":     "DELETE-LONG-TERM",
	"event_history": "DELETE-HISTORY",
	"archival":      "DELETE-ARCHIVAL",
	"reset_all":     "RESET-ALL-MEMORY",
}

type dangerItem struct {
	Action string // "session", "core_persona", etc.
	Label  string // display label
	Stat   string // current stat (e.g. "128 msgs")
}

// openSettingsPanel activates the settings panel overlay.
func (m *cliModel) openSettingsPanel(schema []ch.SettingDefinition, values map[string]string, onSubmit func(map[string]string)) {
	m.panelState.mode = "settings"
	m.relayoutViewport() // 缩小 viewport 为 panel 腾出空间
	m.panelState.cursor = 0
	m.panelState.editing = false
	m.panelState.scrollY = 0
	m.panelState.subGeneration = m.subGeneration // capture current subscription generation
	// Store full schema and pre-process defaults / options on the full copy.
	m.panelState.schemaFull = make([]ch.SettingDefinition, len(schema))
	copy(m.panelState.schemaFull, schema)
	m.panelState.values = make(map[string]string, len(values))
	for k, v := range values {
		m.panelState.values[k] = v
	}
	// Fill defaults and mark global-scoped settings as read-only (admin-only).
	for i := range m.panelState.schemaFull {
		def := &m.panelState.schemaFull[i]
		cur, ok := m.panelState.values[def.Key]
		needsDefault := !ok || cur == ""
		// For number fields, also treat "0" as needing default when the
		// default value is non-zero (handles stale DB entries from scope migrations).
		if !needsDefault && def.Type == ch.SettingTypeNumber && def.DefaultValue != "" && def.DefaultValue != "0" {
			if cur == "0" {
				needsDefault = true
			}
		}
		if needsDefault && def.DefaultValue != "" {
			m.panelState.values[def.Key] = def.DefaultValue
		}
		// Inject cross-subscription model list for tier model selectors.
		// tierModelOptions labels each entry "订阅名 · 模型名" for disambiguation;
		// Value stays the raw model name (resolved cross-sub at Run).
		if (def.Key == "vanguard_model" || def.Key == "balance_model" || def.Key == "swift_model") && m.channel.modelLister != nil && len(def.Options) == 0 {
			if opts := m.tierModelOptions(); len(opts) > 0 {
				def.Options = opts
				def.Type = ch.SettingTypeCombo
			}
		}
		// Global-scoped settings require admin access — mark read-only for non-admin users.
		if !def.ReadOnly && ch.IsGlobalScopedSettingKey(def.Key) && (m.isAdminFn == nil || !m.isAdminFn()) {
			def.ReadOnly = true
		}
	}
	// Auto-fill base_url on panel open if provider has a known default
	// and base_url is currently empty (typical for setup wizard).
	if provider := m.panelState.values["llm_provider"]; provider != "" {
		if m.panelState.values["llm_base_url"] == "" {
			if url, ok := ch.ProviderDefaultURLs[provider]; ok {
				m.panelState.values["llm_base_url"] = url
			}
		}
		if m.panelState.values["llm_model"] == "" {
			if model, ok := ch.ProviderRecommendedModels[provider]; ok {
				m.panelState.values["llm_model"] = model
			}
		}
		m.panelState.prevProvider = provider
		// Show provider-specific API key guidance on initial open.
		m.updateAPIKeyHint(provider)
	}
	// Build visible schema from full schema (filters DependsOn fields).
	m.rebuildVisibleSchema()
	m.panelState.onSubmit = onSubmit
	m.panelState.onCancel = nil
	// Pre-create textarea for editing
	ta := textarea.New()
	ta.Placeholder = m.locale.PanelEditPlaceholder
	ta.SetWidth(m.panelWidth(60))
	ta.SetHeight(1)
	ta.CharLimit = 200
	m.panelState.editTA = ta
}

// rebuildVisibleSchema rebuilds panelSchema from panelSchemaFull,
// filtering out fields whose DependsOn conditions are not met by current panelValues.
func (m *cliModel) rebuildVisibleSchema() {
	m.panelState.schema = make([]ch.SettingDefinition, 0, len(m.panelState.schemaFull))
	for _, def := range m.panelState.schemaFull {
		if ch.IsFieldVisible(def, m.panelState.values) {
			m.panelState.schema = append(m.panelState.schema, def)
		}
	}
	// Clamp cursor
	if m.panelState.cursor >= len(m.panelState.schema) {
		m.panelState.cursor = max(0, len(m.panelState.schema)-1)
	}
}

// autoFillBaseURL sets llm_base_url to the provider's default URL when the
// current base_url is empty or matches a known provider default (i.e., was
// previously auto-filled). Never overwrites a user's custom URL.
// Also auto-fills llm_model with the recommended model for the provider.
func (m *cliModel) autoFillBaseURL(provider string) {
	defaultURL, ok := ch.ProviderDefaultURLs[provider]
	if !ok {
		// Provider has no known default (azure, custom) — clear base_url only
		// if it currently holds a previous provider's auto-filled URL.
		cur := m.panelState.values["llm_base_url"]
		if cur != "" && ch.IsProviderDefaultURL(cur) {
			m.panelState.values["llm_base_url"] = ""
		}
	} else {
		cur := m.panelState.values["llm_base_url"]
		if cur == "" || ch.IsProviderDefaultURL(cur) {
			m.panelState.values["llm_base_url"] = defaultURL
		}
	}
	// Auto-fill recommended model when model is empty or matches a previous provider default.
	if model, ok := ch.ProviderRecommendedModels[provider]; ok {
		cur := m.panelState.values["llm_model"]
		if cur == "" || isProviderRecommendedModel(cur) {
			m.panelState.values["llm_model"] = model
		}
	}
	// Dynamically update the API Key field description with provider-specific
	// guidance and a clickable link to the key management page.
	m.updateAPIKeyHint(provider)
	// Rebuild visible schema (DependsOn fields may appear/disappear).
	m.rebuildVisibleSchema()
}

// updateAPIKeyHint updates the llm_api_key field's description in panelSchemaFull
// to show provider-specific guidance with a clickable OSC 8 hyperlink.
func (m *cliModel) updateAPIKeyHint(provider string) {
	hint := ch.FormatProviderHint(provider, m.locale)
	if hint == "" {
		return
	}
	for i := range m.panelState.schemaFull {
		if m.panelState.schemaFull[i].Key == "llm_api_key" {
			m.panelState.schemaFull[i].Description = hint
			break
		}
	}
	// Also update in the visible schema if the field is there.
	for i := range m.panelState.schema {
		if m.panelState.schema[i].Key == "llm_api_key" {
			m.panelState.schema[i].Description = hint
			break
		}
	}
}

// isProviderRecommendedModel checks if a model name matches any provider's recommended model.
func isProviderRecommendedModel(model string) bool {
	for _, m := range ch.ProviderRecommendedModels {
		if m == model {
			return true
		}
	}
	return false
}

// openSetupPanel opens the step-by-step setup wizard.
// Uses a multi-step state machine (language → provider → apikey → done)
// instead of a single-panel form, so users only make one choice per page.
func (m *cliModel) openSetupPanel() {
	m.openWizardPanel()
}

// viewDangerPanel renders the danger zone panel.
func (m *cliModel) viewDangerPanel() string {
	s := &m.styles
	var sb strings.Builder

	sb.WriteString(s.PanelHeader.Render(m.locale.DangerTitle))
	sb.WriteString("\n")

	if m.panelState.dangerConfirm && m.panelState.dangerCursor < len(m.panelState.dangerItems) {
		// Confirmation sub-mode
		item := m.panelState.dangerItems[m.panelState.dangerCursor]
		confirmStr := dangerConfirmStrings[item.Action]
		fmt.Fprintf(&sb, "  %s\n", fmt.Sprintf(m.locale.DangerConfirmClear, s.WarningSt.Render(item.Label)))
		sb.WriteString(s.PanelDesc.Render("  " + m.locale.DangerIrreversible))
		sb.WriteString("\n\n")
		fmt.Fprintf(&sb, "  %s\n", fmt.Sprintf(m.locale.DangerTypeConfirm, s.ProgressError.Render(confirmStr)))
		sb.WriteString("  ")
		sb.WriteString(m.panelState.dangerInput.View())
		sb.WriteString("\n")
		sb.WriteString(s.PanelHint.Render("  " + m.locale.DangerNavHint))
	} else {
		// Item selection mode
		for i, item := range m.panelState.dangerItems {
			var prefix string
			statText := ""
			if item.Stat != "" {
				statText = fmt.Sprintf("  %s", s.InfoSt.Render(item.Stat))
			}
			if i == m.panelState.dangerCursor {
				prefix = s.PanelCursor.Render("▸")
			} else {
				prefix = "  "
			}
			line := fmt.Sprintf("%s %s%s", prefix, item.Label, statText)
			if i == m.panelState.dangerCursor {
				line = m.renderSelLine(line, m.panelWidth(60)-4)
			}
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
		sb.WriteString(s.PanelHint.Render("  " + m.locale.DangerNavHint))
	}

	return sb.String()
}

// updateDangerPanel handles key events in the danger zone panel.
func (m *cliModel) updateDangerPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	if m.panelState.dangerConfirm {
		// Confirmation input mode
		switch msg.Code {
		case tea.KeyEsc:
			m.panelState.dangerConfirm = false
			m.panelState.dangerInput.SetValue("")
			return true, m, nil
		case tea.KeyEnter:
			if m.panelState.dangerOnExec == nil || m.panelState.dangerCursor >= len(m.panelState.dangerItems) {
				return true, m, nil
			}
			item := m.panelState.dangerItems[m.panelState.dangerCursor]
			confirmStr := dangerConfirmStrings[item.Action]
			if m.panelState.dangerInput.Value() != confirmStr {
				m.showSystemMsg(m.locale.DangerMismatch, feedbackWarning)
				return true, m, nil
			}
			// Execute the clear action
			if err := m.panelState.dangerOnExec(item.Action); err != nil {
				m.showSystemMsg(fmt.Sprintf(m.locale.DangerClearFailed, err), feedbackWarning)
			} else {
				m.showSystemMsg(fmt.Sprintf(m.locale.DangerCleared, item.Label), feedbackInfo)
			}
			m.closePanel()
			return true, m, nil
		default:
			var cmd tea.Cmd
			m.panelState.dangerInput, cmd = m.panelState.dangerInput.Update(msg)
			return true, m, cmd
		}
	}

	// Item selection mode
	switch {
	case msg.Code == tea.KeyEsc:
		// ESC: pop back to parent panel via navigation stack
		if !m.popPanel() {
			m.closePanel()
		}
		return true, m, nil

	case msg.String() == "ctrl+c":
		return m.closePanelAndResume()

	case msg.Code == tea.KeyUp:
		if m.panelState.dangerCursor > 0 {
			m.panelState.dangerCursor--
		}

	case msg.Code == tea.KeyDown:
		if m.panelState.dangerCursor < len(m.panelState.dangerItems)-1 {
			m.panelState.dangerCursor++
		}

	case msg.Code == tea.KeyEnter:
		if m.panelState.dangerCursor < len(m.panelState.dangerItems) {
			m.panelState.dangerConfirm = true
			m.panelState.dangerInput.SetValue("")
			m.panelState.dangerInput.Focus()
			return true, m, m.panelState.dangerInput.Focus()
		}
	}

	return true, m, nil
}

// openDangerPanelFromSettings builds danger items with stats and opens the danger zone panel.
func (m *cliModel) openDangerPanelFromSettings() {
	stats := map[string]string{}
	if m.channel != nil && m.channel.config.GetMemoryStats != nil {
		stats = m.channel.config.GetMemoryStats()
	}

	items := []dangerItem{
		{"session", m.locale.DangerSessionHistory, stats["session"]},
		{"core_persona", "Core Memory: persona", stats["persona"]},
		{"core_human", "Core Memory: human", stats["human"]},
		{"core_working", "Core Memory: working_context", stats["working_context"]},
		{"core_all", m.locale.DangerCoreAll, ""},
		{"long_term", m.locale.DangerLongTerm, stats["long_term"]},
		{"event_history", m.locale.DangerEventHistory, stats["event_history"]},
		{"archival", m.locale.DangerArchival, stats["archival"]},
	}

	m.panelState.mode = "danger"
	m.panelState.scrollY = 0
	m.relayoutViewport()
	m.panelState.dangerItems = items
	m.panelState.dangerCursor = 0
	m.panelState.dangerConfirm = false
	m.panelState.dangerOnExec = func(targetType string) error {
		if m.channel != nil && m.channel.config.ClearMemory != nil {
			return m.channel.config.ClearMemory(targetType)
		}
		return fmt.Errorf("clear memory not configured")
	}
	// Pre-create text input for confirmation
	ti := textinput.New()
	ti.Placeholder = m.locale.DangerConfirmPlaceholder
	ti.CharLimit = 50
	ti.SetWidth(m.panelWidth(40))
	tiStyles := ti.Styles()
	tiStyles.Focused.Prompt = m.styles.TIPrompt
	tiStyles.Focused.Text = m.styles.TIText
	tiStyles.Focused.Placeholder = m.styles.TIPlaceholder
	tiStyles.Cursor.Color = m.styles.TICursor.GetForeground()
	ti.SetStyles(tiStyles)
	m.panelState.dangerInput = ti
}

func (m *cliModel) updateSettingsPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	if m.panelState.editing {
		// Editing mode
		switch msg.Code {
		case tea.KeyEnter:
			// Save value
			newVal := strings.TrimSpace(m.panelState.editTA.Value())
			if m.panelState.cursor < len(m.panelState.schema) {
				key := m.panelState.schema[m.panelState.cursor].Key
				m.panelState.values[key] = newVal
			}
			m.panelState.editing = false
			return true, m, nil
		case tea.KeyEsc:
			m.panelState.editing = false
			return true, m, nil
		default:
			// Delegate to textarea
			var cmd tea.Cmd
			m.panelState.editTA, cmd = m.panelState.editTA.Update(msg)
			return true, m, cmd
		}
	}

	// Combo dropdown mode
	if m.panelState.combo {
		if m.panelState.cursor < len(m.panelState.schema) {
			def := m.panelState.schema[m.panelState.cursor]
			opts := def.Options
			switch msg.Code {
			case tea.KeyEsc:
				m.panelState.combo = false
				return true, m, nil
			case tea.KeyUp:
				if m.panelState.comboIdx > 0 {
					m.panelState.comboIdx--
				}
				return true, m, nil
			case tea.KeyDown:
				if m.panelState.comboIdx < len(opts)-1 {
					m.panelState.comboIdx++
				}
				return true, m, nil
			case tea.KeyEnter:
				if m.panelState.comboIdx < len(opts) {
					m.panelState.values[def.Key] = opts[m.panelState.comboIdx].Value
				}
				m.panelState.combo = false
				// Auto-fill llm_base_url when llm_provider changes via combo
				if def.Key == "llm_provider" && m.panelState.values["llm_provider"] != m.panelState.prevProvider {
					m.autoFillBaseURL(m.panelState.values["llm_provider"])
					m.panelState.prevProvider = m.panelState.values["llm_provider"]
				}
				return true, m, nil
			case tea.KeySpace:
				m.panelState.combo = false
				// Switch to edit mode for custom input
				m.panelState.editing = true
				ta := m.newPanelTextArea(m.panelState.values[def.Key], 50, 1)
				var cmd tea.Cmd
				m.panelState.editTA, cmd = ta.Update(msg)
				return true, m, cmd
			default:
				// Any printable key: auto-switch to edit mode for custom input
				if len(msg.Text) > 0 {
					m.panelState.combo = false
					m.panelState.editing = true
					ta := m.newPanelTextArea(m.panelState.values[def.Key], 50, 1)
					var cmd tea.Cmd
					m.panelState.editTA, cmd = ta.Update(msg)
					return true, m, cmd
				}
			}
		}
		return true, m, nil
	}

	// Navigation mode
	switch {
	case msg.Code == tea.KeyEsc:
		if !m.popPanel() {
			m.closePanel()
		}
		return true, m, nil
	case msg.String() == "ctrl+s":
		// If currently editing a field, commit the edit before saving.
		if m.panelState.editing && m.panelState.cursor < len(m.panelState.schema) {
			def := m.panelState.schema[m.panelState.cursor]
			if !def.ReadOnly {
				key := def.Key
				m.panelState.values[key] = strings.TrimSpace(m.panelState.editTA.Value())
			}
			m.panelState.editing = false
		}
		// Submit all settings — async to avoid blocking the UI.
		// Close panel immediately to restore responsiveness, then run
		// the save callback in a goroutine and send back results.
		onSubmit := m.panelState.onSubmit
		panelVals := m.panelState.values
		m.closePanel()
		if onSubmit != nil && panelVals != nil {
			m.panelState.settingsSaving = true // block input until cliSettingsSavedMsg arrives
			return true, m, m.doSaveSettings(onSubmit, panelVals)
		}
		return true, m, nil
	case msg.Code == tea.KeyUp || msg.String() == "shift+tab":
		if m.panelState.cursor > 0 {
			m.panelState.cursor--
			m.ensureSettingsCursorVisible(0)
		}
		return true, m, nil
	case msg.Code == tea.KeyDown || msg.Code == tea.KeyTab:
		if m.panelState.cursor < len(m.panelState.schema)-1 {
			m.panelState.cursor++
			m.ensureSettingsCursorVisible(0)
		}
		return true, m, nil
	case msg.Code == tea.KeyEnter:
		if m.panelState.cursor < len(m.panelState.schema) {
			def := m.panelState.schema[m.panelState.cursor]
			// Read-only items: skip editing (display-only)
			if def.ReadOnly {
				return true, m, nil
			}
			// Runner panel entry — push settings state before opening child panel
			if def.Key == "runner_panel" {
				m.pushPanel()
				m.openRunnerPanel()
				return true, m, nil
			}
			// Danger zone entry — push settings state before opening child panel
			if def.Key == "danger_zone" {
				m.pushPanel()
				m.openDangerPanelFromSettings()
				return true, m, nil
			}
			// ch.Subscription management entry — save panel state, open quick switch
			if def.Key == "subscription_manage" {
				// Backup current panel state so we can restore after quick switch
				m.panelState.valuesBackup = make(map[string]string, len(m.panelState.values))
				for k, v := range m.panelState.values {
					m.panelState.valuesBackup[k] = v
				}
				m.panelState.cursorBackup = m.panelState.cursor
				m.panelState.onSubmitBackup = m.panelState.onSubmit
				m.panelState.mode = ""
				m.relayoutViewport()
				m.quickSwitchReturnToPanel = true
				m.openQuickSwitch("")
				return true, m, nil
			}
			switch def.Type {
			case ch.SettingTypeToggle:
				// Toggle on Enter
				cur := m.panelState.values[def.Key]
				if cur == "true" {
					m.panelState.values[def.Key] = "false"
				} else {
					m.panelState.values[def.Key] = "true"
				}
				return true, m, nil
			case ch.SettingTypeSelect:
				// Cycle through options
				cur := m.panelState.values[def.Key]
				if cur == "" && def.DefaultValue != "" {
					cur = def.DefaultValue
				}
				idx := slices.IndexFunc(def.Options, func(opt ch.SettingOption) bool {
					return opt.Value == cur
				})
				if idx >= 0 && idx < len(def.Options)-1 {
					m.panelState.values[def.Key] = def.Options[idx+1].Value
				} else if len(def.Options) > 0 {
					m.panelState.values[def.Key] = def.Options[0].Value
				}
				return true, m, nil
			case ch.SettingTypeCombo:
				// Open combo dropdown if options available, otherwise edit
				if len(def.Options) > 0 {
					m.panelState.combo = true
					m.panelState.comboIdx = 0
					// Pre-select current value if it matches an option
					cur := m.panelState.values[def.Key]
					for i, opt := range def.Options {
						if opt.Value == cur {
							m.panelState.comboIdx = i
							break
						}
					}
					return true, m, nil
				}
				// No options: fall through to default edit mode
				fallthrough
			default:
				// Enter edit mode for text/number/textarea/combo(fallback)
				m.panelState.editing = true
				m.panelState.editTA = m.newPanelTextArea(m.panelState.values[def.Key], 50, 1)
				return true, m, nil
			}
		}
		return true, m, nil
	}
	return true, m, nil
}

func (m *cliModel) viewSettingsPanel() string {

	// §20 使用缓存样式
	s := &m.styles
	valueStyle := s.InfoSt
	cursorStyle := s.PanelCursor
	descStyle := s.PanelDesc
	hintStyle := s.PanelHint

	var sb strings.Builder
	sb.WriteString(s.PanelHeader.Render(m.locale.PanelSettingsTitle))
	sb.WriteString("\n")
	// 表头下方精致分割线，区分标题与内容
	sb.WriteString(s.SettingsDivider.Render("┈" + strings.Repeat("┈", 30)))
	sb.WriteString("\n")

	// Group by category
	lastCat := ""
	ln := 0 // 当前渲染行号
	for i, def := range m.panelState.schema {
		if def.Category != lastCat {
			lastCat = def.Category
			sb.WriteString("\n")
			sb.WriteString(s.SettingsCat.Render("▸ " + lastCat))
			sb.WriteString("\n")
			ln += 2
		}

		cur := m.panelState.values[def.Key]
		// If value is empty, fall back to DefaultValue for display
		if cur == "" && def.DefaultValue != "" {
			cur = def.DefaultValue
		}
		var prefix string
		if i == m.panelState.cursor && !m.panelState.editing {
			prefix = cursorStyle.Render("▸")
		} else {
			prefix = "  "
		}

		// Read-only items: show lock icon, dim label, skip value interaction
		labelSt := lipgloss.NewStyle()
		valueSt := valueStyle
		if def.ReadOnly {
			prefix = s.PanelDesc.Render("🔒")
			labelSt = s.PanelDesc
			valueSt = s.PanelDesc
		}

		// Runner panel entry: render with connection status
		if def.Key == "runner_panel" {
			statusHint := ""
			if m.runnerBridge != nil {
				switch m.runnerBridge.Status() {
				case RunnerConnected:
					statusHint = " " + s.ProgressDone.Render("● "+m.locale.RunnerStatusConnected)
				case RunnerConnecting:
					statusHint = " " + s.ProgressRunning.Render("● "+m.locale.RunnerConnecting)
				}
			}
			line := fmt.Sprintf("%s %s%s", prefix, s.ProgressDone.Render(def.Label), statusHint)
			if i == m.panelState.cursor && !m.panelState.editing {
				line = m.renderSelLine(line, m.width-6)
			}
			sb.WriteString(line)
			sb.WriteString("\n")
			ln++
			continue
		}

		// Danger zone entry: render with warning style
		if def.Key == "danger_zone" {
			line := fmt.Sprintf("%s %s", prefix, s.WarningSt.Render(def.Label))
			if i == m.panelState.cursor && !m.panelState.editing {
				line = m.renderSelLine(line, m.width-6)
			}
			sb.WriteString(line)
			sb.WriteString("\n")
			ln++
			continue
		}

		// ch.Subscription management entry: show count + CURRENT session's subscription
		if def.Key == "subscription_manage" {
			subHint := ""
			if m.subscriptionMgr != nil {
				if subs, err := m.subscriptionMgr.List(""); err == nil && len(subs) > 0 {
					var activeName string
					// Use m.activeSubID (per-session) to find the current subscription,
					// NOT sub.Active (global default). The settings panel must reflect
					// the session's active subscription, not the global default.
					if m.activeSubID != "" {
						for _, sub := range subs {
							if sub.ID == m.activeSubID {
								activeName = sub.Name
								break
							}
						}
					}
					// Fallback: if no per-session subscription is set, no hint is shown.
					// (Previously fell back to sub.Active — "default subscription" concept retired.)
					if activeName != "" {
						subHint = " " + s.ProgressDone.Render("● "+activeName)
					}
					subHint += descStyle.Render(fmt.Sprintf(" (%d)", len(subs)))
				}
			}
			line := fmt.Sprintf("%s %s%s", prefix, s.ProgressDone.Render(def.Label), subHint)
			if i == m.panelState.cursor && !m.panelState.editing {
				line = m.renderSelLine(line, m.width-6)
			}
			sb.WriteString(line)
			sb.WriteString("\n")
			ln++
			continue
		}

		// Format value display
		var displayVal string
		switch def.Type {
		case ch.SettingTypeToggle:
			if cur == "true" {
				displayVal = valueSt.Render(m.locale.PanelToggleOn)
			} else {
				displayVal = valueSt.Render(m.locale.PanelToggleOff)
			}
		case ch.SettingTypeSelect:
			// Find label for current value
			displayVal = cur
			for _, opt := range def.Options {
				if opt.Value == cur {
					displayVal = valueSt.Render(opt.Label)
					break
				}
			}
		case ch.SettingTypeCombo:
			// Show current value with dropdown hint
			if cur == "" {
				displayVal = descStyle.Render(m.locale.PanelNotSet)
			} else {
				displayVal = valueSt.Render(cur)
			}
			if !def.ReadOnly && len(def.Options) > 0 {
				displayVal += descStyle.Render(" ▾")
			}
		case ch.SettingTypePassword:
			if cur == "" {
				displayVal = descStyle.Render(m.locale.PanelNotSet)
			} else {
				displayVal = valueSt.Render("••••••")
			}
		default:
			if cur == "" {
				displayVal = descStyle.Render(m.locale.PanelNotSet)
			} else {
				displayVal = valueSt.Render(cur)
			}
		}

		line := fmt.Sprintf("%s %s: %s", prefix, labelSt.Render(def.Label), displayVal)
		if i == m.panelState.cursor && !m.panelState.editing && !m.panelState.combo {
			line = m.renderSelLine(line, m.width-6)
		}
		sb.WriteString(line)
		sb.WriteString("\n")
		ln++

		// Show field description when cursor is on this field (not in edit/combo mode).
		if i == m.panelState.cursor && !m.panelState.editing && !m.panelState.combo && def.Description != "" {
			descLines := strings.Split(def.Description, "\n")
			for _, dl := range descLines {
				if strings.Contains(dl, "\x1b]8;;") {
					// OSC 8 hyperlink line — write directly, don't let lipgloss touch it.
					sb.WriteString("    ")
					sb.WriteString(dl)
				} else {
					sb.WriteString(descStyle.Render("    " + dl))
				}
				sb.WriteString("\n")
				ln++
			}
		}

		// API Key field: always show "获取密钥" button below the field.
		// Clickable via mouse zone (panelOpenURL) — opens browser directly.
		// Also wrapped in OSC 8 hyperlink for terminals that support it.
		if def.Key == "llm_api_key" && m.panelState.values["llm_provider"] != "" {
			guide, hasGuide := ch.ProviderSetupGuides[m.panelState.values["llm_provider"]]
			if hasGuide && guide.URL != "" {
				btnLabel := "  " + m.locale.PanelBtnGetKey + "  "
				oscLink := fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", guide.URL, btnLabel)
				sb.WriteString("    ")
				sb.WriteString(oscLink)
				sb.WriteString("\n")
				ln++
			} else if hasGuide && guide.URL == "" {
				// Ollama: no key needed, show info text from locale
				hint := ""
				if m.locale.ProviderHints != nil {
					hint = m.locale.ProviderHints[guide.HintKey]
				}
				if hint != "" {
					sb.WriteString(descStyle.Render("    " + hint))
					sb.WriteString("\n")
					ln++
				}
			}
		}

		// ── Inline edit/combo overlay (Crush-style: render right below the item) ──
		if i == m.panelState.cursor {
			if m.panelState.editing {
				sb.WriteString("  ")
				sb.WriteString(cursorStyle.Render("✎ "))
				sb.WriteString(m.panelState.editTA.View())
				sb.WriteString("\n")
				sb.WriteString(descStyle.Render("    " + m.locale.PanelEditHint))
				sb.WriteString("\n")
				ln += 3
			} else if m.panelState.combo {
				maxShow := 8
				start := 0
				if m.panelState.comboIdx >= maxShow {
					start = m.panelState.comboIdx - maxShow + 1
				}
				end := start + maxShow
				if end > len(def.Options) {
					end = len(def.Options)
				}
				for j := start; j < end; j++ {
					opt := def.Options[j]
					label := opt.Label
					runes := []rune(label)
					if len(runes) > 40 {
						label = string(runes[:37]) + "..."
					}
					if j == m.panelState.comboIdx {
						sb.WriteString(cursorStyle.Render("    ▸ " + label))
					} else {
						sb.WriteString("      ")
						sb.WriteString(label)
					}
					// Show option description on the selected combo item.
					if j == m.panelState.comboIdx && opt.Description != "" {
						sb.WriteString("\n")
						sb.WriteString(descStyle.Render("        " + opt.Description))
					}
					sb.WriteString("\n")
					ln++
					if j == m.panelState.comboIdx && opt.Description != "" {
						ln++
					}
				}
				sb.WriteString(descStyle.Render("    " + m.locale.PanelComboHint))
				sb.WriteString("\n")
				ln++
			}
		}
	}

	// Bottom buttons: always show Save and Cancel buttons.
	// These are clickable mouse zones for users who don't know keyboard shortcuts.
	if !m.panelState.editing && !m.panelState.combo {
		sb.WriteString("\n")
		// Save button — styled prominently
		saveBtn := "  " + m.locale.PanelBtnSave + "  "
		saveOsc := fmt.Sprintf("\x1b]8;;xbot://panel-save\x1b\\%s\x1b]8;;\x1b\\", saveBtn)
		sb.WriteString("  ")
		sb.WriteString(saveOsc)
		// Cancel button
		cancelBtn := "  " + m.locale.PanelBtnCancel + "  "
		cancelOsc := fmt.Sprintf("\x1b]8;;xbot://panel-cancel\x1b\\%s\x1b]8;;\x1b\\", cancelBtn)
		sb.WriteString("    ")
		sb.WriteString(cancelOsc)
		sb.WriteString("\n")
		// Keyboard hint below buttons (secondary, for discoverability)
		sb.WriteString(hintStyle.Render("  " + m.locale.PanelNavHint))
		sb.WriteString("\n")
	}

	return sb.String()
}

// SettingsSchema returns the settings definitions for CLI ch.
func (c *CLIChannel) SettingsSchema() []ch.SettingDefinition {
	loc := ch.GetLocale(ch.CurrentLocaleLang())
	return loc.SettingsSchema
}

// HandleSettingSubmit processes a setting value submission from the CLI ch.
func (c *CLIChannel) HandleSettingSubmit(ctx context.Context, rawInput string) (map[string]string, error) {
	// CLI uses interactive panel, this is for programmatic access
	return nil, fmt.Errorf("CLI uses interactive settings panel")
}

// SetSettingsService injects the settings service for the interactive panel.
func (c *CLIChannel) SetSettingsService(svc SettingsService) {
	c.settingsSvc = svc
}

// SetModelLister injects the model lister for combo settings.
func (c *CLIChannel) SetModelLister(lister ModelLister) {
	c.modelLister = lister
}

// SetSubscriptionManager sets the subscription manager for multi-subscription support.
func (c *CLIChannel) SetSubscriptionManager(mgr SubscriptionManager) {
	c.subscriptionMgr = mgr
	if c.model != nil {
		c.model.SetSubscriptionMgr(mgr)
	}
}

// SetLLMSubscriber sets the LLM subscriber for quick switch actions.
// Stores on channel for propagation to model in Start().
func (c *CLIChannel) SetLLMSubscriber(sub LLMSubscriber) {
	c.llmSubscriber = sub
	if c.model != nil {
		c.model.SetLLMSubscriber(sub)
	}
}

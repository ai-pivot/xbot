package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
	ch "xbot/channel"
	"xbot/protocol"

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
	m.panelState.settings.editing = false
	m.panelState.scrollY = 0
	m.panelState.settings.subGeneration = m.subGeneration // capture current subscription generation
	// Store full schema and pre-process defaults / options on the full copy.
	m.panelState.settings.schemaFull = make([]ch.SettingDefinition, len(schema))
	copy(m.panelState.settings.schemaFull, schema)
	m.panelState.settings.values = make(map[string]string, len(values))
	for k, v := range values {
		m.panelState.settings.values[k] = v
	}
	// Fill defaults and mark global-scoped settings as read-only (admin-only).
	for i := range m.panelState.settings.schemaFull {
		def := &m.panelState.settings.schemaFull[i]
		cur, ok := m.panelState.settings.values[def.Key]
		needsDefault := !ok || cur == ""
		// For number fields, also treat "0" as needing default when the
		// default value is non-zero (handles stale DB entries from scope migrations).
		if !needsDefault && def.Type == ch.SettingTypeNumber && def.DefaultValue != "" && def.DefaultValue != "0" {
			if cur == "0" {
				needsDefault = true
			}
		}
		if needsDefault && def.DefaultValue != "" {
			m.panelState.settings.values[def.Key] = def.DefaultValue
		}
		// Inject cross-subscription model list for tier model selectors.
		// Global-scoped settings require admin access — mark read-only for non-admin users.
		if !def.ReadOnly && ch.IsGlobalScopedSettingKey(def.Key) && (m.isAdminFn == nil || !m.isAdminFn()) {
			def.ReadOnly = true
		}
	}

	// Inject cross-subscription model options for tier selectors.
	// Tier values are stored as "subID|model" — options must match this format
	// so the combo dropdown can pre-select the current value.
	if m.channel != nil && m.channel.modelLister != nil {
		entries := m.channel.modelLister.ListAllModelEntries()
		tierKeys := map[string]bool{"tier_vanguard": true, "tier_balance": true, "tier_swift": true}
		for i := range m.panelState.settings.schemaFull {
			def := &m.panelState.settings.schemaFull[i]
			if !tierKeys[def.Key] {
				continue
			}
			def.Options = tierModelOptions(entries)
		}
	}
	// Auto-fill base_url on panel open if provider has a known default
	// and base_url is currently empty (typical for setup wizard).
	if provider := m.panelState.settings.values["llm_provider"]; provider != "" {
		if m.panelState.settings.values["llm_base_url"] == "" {
			if url, ok := ch.ProviderDefaultURLs[provider]; ok {
				m.panelState.settings.values["llm_base_url"] = url
			}
		}
		if m.panelState.settings.values["llm_model"] == "" {
			if model, ok := ch.ProviderRecommendedModels[provider]; ok {
				m.panelState.settings.values["llm_model"] = model
			}
		}
		m.panelState.settings.prevProvider = provider
		// Show provider-specific API key guidance on initial open.
		m.updateAPIKeyHint(provider)
	}
	// Build visible schema from full schema (filters DependsOn fields).
	m.rebuildVisibleSchema()
	m.panelState.settings.onSubmit = onSubmit
	m.panelState.askUser.onCancel = nil
	// Pre-create textarea for editing
	ta := textarea.New()
	ta.Placeholder = m.locale.PanelEditPlaceholder
	ta.SetWidth(m.panelWidth(60))
	ta.SetHeight(1)
	ta.CharLimit = 200
	m.panelState.settings.editTA = ta
}

// tierModelOptions builds SettingOption list for tier selectors.
// Values are encoded as "subID|model" (or plain "model" when SubID is empty)
// to match the tier value format stored in DB.
func tierModelOptions(entries []protocol.ModelEntry) []ch.SettingOption {
	opts := make([]ch.SettingOption, 0, len(entries))
	for _, e := range entries {
		val := e.Model
		if e.SubID != "" {
			val = e.SubID + "|" + e.Model
		}
		label := e.Model
		if e.SubName != "" {
			label = e.Model + " (" + e.SubName + ")"
		}
		opts = append(opts, ch.SettingOption{Label: label, Value: val})
	}
	return opts
}

// rebuildVisibleSchema rebuilds panelSchema from panelSchemaFull,
// filtering out fields whose DependsOn conditions are not met by current panelValues.
func (m *cliModel) rebuildVisibleSchema() {
	m.panelState.settings.schema = make([]ch.SettingDefinition, 0, len(m.panelState.settings.schemaFull))
	for _, def := range m.panelState.settings.schemaFull {
		if ch.IsFieldVisible(def, m.panelState.settings.values) {
			m.panelState.settings.schema = append(m.panelState.settings.schema, def)
		}
	}
	// Clamp cursor
	if m.panelState.cursor >= len(m.panelState.settings.schema) {
		m.panelState.cursor = max(0, len(m.panelState.settings.schema)-1)
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
		cur := m.panelState.settings.values["llm_base_url"]
		if cur != "" && ch.IsProviderDefaultURL(cur) {
			m.panelState.settings.values["llm_base_url"] = ""
		}
	} else {
		cur := m.panelState.settings.values["llm_base_url"]
		if cur == "" || ch.IsProviderDefaultURL(cur) {
			m.panelState.settings.values["llm_base_url"] = defaultURL
		}
	}
	// Auto-fill recommended model when model is empty or matches a previous provider default.
	if model, ok := ch.ProviderRecommendedModels[provider]; ok {
		cur := m.panelState.settings.values["llm_model"]
		if cur == "" || isProviderRecommendedModel(cur) {
			m.panelState.settings.values["llm_model"] = model
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
	for i := range m.panelState.settings.schemaFull {
		if m.panelState.settings.schemaFull[i].Key == "llm_api_key" {
			m.panelState.settings.schemaFull[i].Description = hint
			break
		}
	}
	// Also update in the visible schema if the field is there.
	for i := range m.panelState.settings.schema {
		if m.panelState.settings.schema[i].Key == "llm_api_key" {
			m.panelState.settings.schema[i].Description = hint
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

	if m.panelState.misc.dangerConfirm && m.panelState.misc.dangerCursor < len(m.panelState.misc.dangerItems) {
		// Confirmation sub-mode
		item := m.panelState.misc.dangerItems[m.panelState.misc.dangerCursor]
		confirmStr := dangerConfirmStrings[item.Action]
		fmt.Fprintf(&sb, "  %s\n", fmt.Sprintf(m.locale.DangerConfirmClear, s.WarningSt.Render(item.Label)))
		sb.WriteString(s.PanelDesc.Render("  " + m.locale.DangerIrreversible))
		sb.WriteString("\n\n")
		fmt.Fprintf(&sb, "  %s\n", fmt.Sprintf(m.locale.DangerTypeConfirm, s.ProgressError.Render(confirmStr)))
		sb.WriteString("  ")
		sb.WriteString(m.panelState.misc.dangerInput.View())
		sb.WriteString("\n")
		sb.WriteString(s.PanelHint.Render("  " + m.locale.DangerNavHint))
	} else {
		// Item selection mode
		for i, item := range m.panelState.misc.dangerItems {
			var prefix string
			statText := ""
			if item.Stat != "" {
				statText = fmt.Sprintf("  %s", s.InfoSt.Render(item.Stat))
			}
			if i == m.panelState.misc.dangerCursor {
				prefix = s.PanelCursor.Render("▸")
			} else {
				prefix = "  "
			}
			line := fmt.Sprintf("%s %s%s", prefix, item.Label, statText)
			if i == m.panelState.misc.dangerCursor {
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
	if m.panelState.misc.dangerConfirm {
		// Confirmation input mode
		switch msg.Code {
		case tea.KeyEsc:
			m.panelState.misc.dangerConfirm = false
			m.panelState.misc.dangerInput.SetValue("")
			return true, m, nil
		case tea.KeyEnter:
			if m.panelState.misc.dangerOnExec == nil || m.panelState.misc.dangerCursor >= len(m.panelState.misc.dangerItems) {
				return true, m, nil
			}
			item := m.panelState.misc.dangerItems[m.panelState.misc.dangerCursor]
			confirmStr := dangerConfirmStrings[item.Action]
			if m.panelState.misc.dangerInput.Value() != confirmStr {
				m.showSystemMsg(m.locale.DangerMismatch, feedbackWarning)
				return true, m, nil
			}
			// Execute the clear action
			if err := m.panelState.misc.dangerOnExec(item.Action); err != nil {
				m.showSystemMsg(fmt.Sprintf(m.locale.DangerClearFailed, err), feedbackWarning)
			} else {
				m.showSystemMsg(fmt.Sprintf(m.locale.DangerCleared, item.Label), feedbackInfo)
			}
			m.closePanel()
			return true, m, nil
		default:
			var cmd tea.Cmd
			m.panelState.misc.dangerInput, cmd = m.panelState.misc.dangerInput.Update(msg)
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
		if m.panelState.misc.dangerCursor > 0 {
			m.panelState.misc.dangerCursor--
		}

	case msg.Code == tea.KeyDown:
		if m.panelState.misc.dangerCursor < len(m.panelState.misc.dangerItems)-1 {
			m.panelState.misc.dangerCursor++
		}

	case msg.Code == tea.KeyEnter:
		if m.panelState.misc.dangerCursor < len(m.panelState.misc.dangerItems) {
			m.panelState.misc.dangerConfirm = true
			m.panelState.misc.dangerInput.SetValue("")
			m.panelState.misc.dangerInput.Focus()
			return true, m, m.panelState.misc.dangerInput.Focus()
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
	m.panelState.misc.dangerItems = items
	m.panelState.misc.dangerCursor = 0
	m.panelState.misc.dangerConfirm = false
	m.panelState.misc.dangerOnExec = func(targetType string) error {
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
	m.panelState.misc.dangerInput = ti
}

func (m *cliModel) updateSettingsPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	if m.panelState.settings.editing {
		// Editing mode
		switch msg.Code {
		case tea.KeyEnter:
			// Save value
			newVal := strings.TrimSpace(m.panelState.settings.editTA.Value())
			if m.panelState.cursor < len(m.panelState.settings.schema) {
				key := m.panelState.settings.schema[m.panelState.cursor].Key
				m.panelState.settings.values[key] = newVal
			}
			m.panelState.settings.editing = false
			return true, m, nil
		case tea.KeyEsc:
			m.panelState.settings.editing = false
			return true, m, nil
		default:
			// Delegate to textarea
			var cmd tea.Cmd
			m.panelState.settings.editTA, cmd = m.panelState.settings.editTA.Update(msg)
			return true, m, cmd
		}
	}

	// Combo dropdown mode
	if m.panelState.settings.combo {
		if m.panelState.cursor < len(m.panelState.settings.schema) {
			def := m.panelState.settings.schema[m.panelState.cursor]
			opts := def.Options
			switch msg.Code {
			case tea.KeyEsc:
				m.panelState.settings.combo = false
				return true, m, nil
			case tea.KeyUp:
				if m.panelState.settings.comboIdx > 0 {
					m.panelState.settings.comboIdx--
				}
				return true, m, nil
			case tea.KeyDown:
				if m.panelState.settings.comboIdx < len(opts)-1 {
					m.panelState.settings.comboIdx++
				}
				return true, m, nil
			case tea.KeyEnter:
				if m.panelState.settings.comboIdx < len(opts) {
					m.panelState.settings.values[def.Key] = opts[m.panelState.settings.comboIdx].Value
				}
				m.panelState.settings.combo = false
				// Auto-fill llm_base_url when llm_provider changes via combo
				if def.Key == "llm_provider" && m.panelState.settings.values["llm_provider"] != m.panelState.settings.prevProvider {
					m.autoFillBaseURL(m.panelState.settings.values["llm_provider"])
					m.panelState.settings.prevProvider = m.panelState.settings.values["llm_provider"]
				}
				return true, m, nil
			case tea.KeySpace:
				m.panelState.settings.combo = false
				// Switch to edit mode for custom input
				m.panelState.settings.editing = true
				ta := m.newPanelTextArea(m.panelState.settings.values[def.Key], 50, 1)
				var cmd tea.Cmd
				m.panelState.settings.editTA, cmd = ta.Update(msg)
				return true, m, cmd
			default:
				// Any printable key: auto-switch to edit mode for custom input
				if len(msg.Text) > 0 {
					m.panelState.settings.combo = false
					m.panelState.settings.editing = true
					ta := m.newPanelTextArea(m.panelState.settings.values[def.Key], 50, 1)
					var cmd tea.Cmd
					m.panelState.settings.editTA, cmd = ta.Update(msg)
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
		if m.panelState.settings.editing && m.panelState.cursor < len(m.panelState.settings.schema) {
			def := m.panelState.settings.schema[m.panelState.cursor]
			if !def.ReadOnly {
				key := def.Key
				m.panelState.settings.values[key] = strings.TrimSpace(m.panelState.settings.editTA.Value())
			}
			m.panelState.settings.editing = false
		}
		// Submit all settings — async to avoid blocking the UI.
		// Close panel immediately to restore responsiveness, then run
		// the save callback in a goroutine and send back results.
		onSubmit := m.panelState.settings.onSubmit
		panelVals := m.panelState.settings.values
		m.closePanel()
		if onSubmit != nil && panelVals != nil {
			m.panelState.settings.settingsSaving = true // block input until cliSettingsSavedMsg arrives
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
		if m.panelState.cursor < len(m.panelState.settings.schema)-1 {
			m.panelState.cursor++
			m.ensureSettingsCursorVisible(0)
		}
		return true, m, nil
	case msg.Code == tea.KeyEnter:
		if m.panelState.cursor < len(m.panelState.settings.schema) {
			def := m.panelState.settings.schema[m.panelState.cursor]
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
				m.pushPanel()
				m.panelState.mode = ""
				m.relayoutViewport()
				m.openQuickSwitch("")
				return true, m, nil
			}
			switch def.Type {
			case ch.SettingTypeToggle:
				// Toggle on Enter
				cur := m.panelState.settings.values[def.Key]
				if cur == "true" {
					m.panelState.settings.values[def.Key] = "false"
				} else {
					m.panelState.settings.values[def.Key] = "true"
				}
				return true, m, nil
			case ch.SettingTypeSelect:
				// Cycle through options
				cur := m.panelState.settings.values[def.Key]
				if cur == "" && def.DefaultValue != "" {
					cur = def.DefaultValue
				}
				idx := slices.IndexFunc(def.Options, func(opt ch.SettingOption) bool {
					return opt.Value == cur
				})
				if idx >= 0 && idx < len(def.Options)-1 {
					m.panelState.settings.values[def.Key] = def.Options[idx+1].Value
				} else if len(def.Options) > 0 {
					m.panelState.settings.values[def.Key] = def.Options[0].Value
				}
				return true, m, nil
			case ch.SettingTypeCombo:
				// Open combo dropdown if options available, otherwise edit
				if len(def.Options) > 0 {
					m.panelState.settings.combo = true
					m.panelState.settings.comboIdx = 0
					// Pre-select current value if it matches an option
					cur := m.panelState.settings.values[def.Key]
					for i, opt := range def.Options {
						if opt.Value == cur {
							m.panelState.settings.comboIdx = i
							break
						}
					}
					return true, m, nil
				}
				// No options: fall through to default edit mode
				fallthrough
			default:
				// Enter edit mode for text/number/textarea/combo(fallback)
				m.panelState.settings.editing = true
				m.panelState.settings.editTA = m.newPanelTextArea(m.panelState.settings.values[def.Key], 50, 1)
				return true, m, nil
			}
		}
		return true, m, nil
	}
	return true, m, nil
}

// settingsLayoutRow represents a single rendered line in the settings panel.
// It carries both the display text (for viewSettingsPanel) and the zone
// metadata (for trackSettingsZones), ensuring both paths stay in sync.
type settingsLayoutRow struct {
	text    string // styled line content (without trailing newline)
	zoneID  string // zone identifier; "" means non-interactive (skip)
	zoneIdx int    // index parameter: schema index for items, option index for combo items
}

// buildSettingsLayout produces the complete list of rendered lines and zone
// metadata for the settings panel. Both viewSettingsPanel and trackSettingsZones
// call this single function to guarantee identical line ordering. The function
// does NOT apply scroll offset — callers handle that independently.
func (m *cliModel) buildSettingsLayout() []settingsLayoutRow {
	// §20 使用缓存样式
	s := &m.styles
	valueStyle := s.InfoSt
	cursorStyle := s.PanelCursor
	descStyle := s.PanelDesc
	hintStyle := s.PanelHint

	rows := make([]settingsLayoutRow, 0, 64)

	// Header
	rows = append(rows, settingsLayoutRow{
		text: s.PanelHeader.Render(m.locale.PanelSettingsTitle),
	})
	// 表头下方精致分割线，区分标题与内容
	rows = append(rows, settingsLayoutRow{
		text: s.SettingsDivider.Render("┈" + strings.Repeat("┈", 30)),
	})

	// Group by category
	lastCat := ""
	for i, def := range m.panelState.settings.schema {
		if def.Category != lastCat {
			lastCat = def.Category
			rows = append(rows, settingsLayoutRow{}) // blank line
			rows = append(rows, settingsLayoutRow{
				text: s.SettingsCat.Render("▸ " + lastCat),
			})
		}

		cur := m.panelState.settings.values[def.Key]
		// If value is empty, fall back to DefaultValue for display
		if cur == "" && def.DefaultValue != "" {
			cur = def.DefaultValue
		}
		var prefix string
		if i == m.panelState.cursor && !m.panelState.settings.editing {
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

		// Determine zone ID from field type (item/toggle/combo)
		zoneID := "panelItem"
		switch def.Type {
		case ch.SettingTypeToggle:
			zoneID = "panelToggle"
		case ch.SettingTypeCombo:
			zoneID = "panelCombo"
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
			if i == m.panelState.cursor && !m.panelState.settings.editing {
				line = m.renderSelLine(line, m.width-6)
			}
			rows = append(rows, settingsLayoutRow{text: line, zoneID: zoneID, zoneIdx: i})
			continue
		}

		// Danger zone entry: render with warning style
		if def.Key == "danger_zone" {
			line := fmt.Sprintf("%s %s", prefix, s.WarningSt.Render(def.Label))
			if i == m.panelState.cursor && !m.panelState.settings.editing {
				line = m.renderSelLine(line, m.width-6)
			}
			rows = append(rows, settingsLayoutRow{text: line, zoneID: zoneID, zoneIdx: i})
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
			if i == m.panelState.cursor && !m.panelState.settings.editing {
				line = m.renderSelLine(line, m.width-6)
			}
			rows = append(rows, settingsLayoutRow{text: line, zoneID: zoneID, zoneIdx: i})
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
				// Look up label from options for nicer display (e.g. tier selectors
				// store "subID|model" but should show "model (subname)")
				displayVal = valueSt.Render(cur)
				for _, opt := range def.Options {
					if opt.Value == cur {
						displayVal = valueSt.Render(opt.Label)
						break
					}
				}
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
		if i == m.panelState.cursor && !m.panelState.settings.editing && !m.panelState.settings.combo {
			line = m.renderSelLine(line, m.width-6)
		}
		rows = append(rows, settingsLayoutRow{text: line, zoneID: zoneID, zoneIdx: i})

		// Show field description when cursor is on this field (not in edit/combo mode).
		if i == m.panelState.cursor && !m.panelState.settings.editing && !m.panelState.settings.combo && def.Description != "" {
			descLines := strings.Split(def.Description, "\n")
			for _, dl := range descLines {
				if strings.Contains(dl, "\x1b]8;;") {
					// OSC 8 hyperlink line — write directly, don't let lipgloss touch it.
					rows = append(rows, settingsLayoutRow{text: "    " + dl})
				} else {
					rows = append(rows, settingsLayoutRow{text: descStyle.Render("    " + dl)})
				}
			}
		}

		// API Key field: always show "获取密钥" button below the field.
		// Clickable via mouse zone (panelOpenURL) — opens browser directly.
		// Also wrapped in OSC 8 hyperlink for terminals that support it.
		if def.Key == "llm_api_key" && m.panelState.settings.values["llm_provider"] != "" {
			guide, hasGuide := ch.ProviderSetupGuides[m.panelState.settings.values["llm_provider"]]
			if hasGuide && guide.URL != "" {
				btnLabel := "  " + m.locale.PanelBtnGetKey + "  "
				oscLink := fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", guide.URL, btnLabel)
				rows = append(rows, settingsLayoutRow{
					text:    "    " + oscLink,
					zoneID:  "panelOpenURL",
					zoneIdx: 0,
				})
			} else if hasGuide && guide.URL == "" {
				// Ollama: no key needed, show info text from locale
				hint := ""
				if m.locale.ProviderHints != nil {
					hint = m.locale.ProviderHints[guide.HintKey]
				}
				if hint != "" {
					rows = append(rows, settingsLayoutRow{
						text: descStyle.Render("    " + hint),
					})
				}
			}
		}

		// ── Inline edit/combo overlay (Crush-style: render right below the item) ──
		if i == m.panelState.cursor {
			if m.panelState.settings.editing {
				// Edit input line
				rows = append(rows, settingsLayoutRow{
					text: "  " + cursorStyle.Render("✎ ") + m.panelState.settings.editTA.View(),
				})
				// Edit hint line
				rows = append(rows, settingsLayoutRow{
					text: descStyle.Render("    " + m.locale.PanelEditHint),
				})
			} else if m.panelState.settings.combo {
				maxShow := 8
				start := 0
				if m.panelState.settings.comboIdx >= maxShow {
					start = m.panelState.settings.comboIdx - maxShow + 1
				}
				end := min(start+maxShow, len(def.Options))
				for j := start; j < end; j++ {
					opt := def.Options[j]
					label := opt.Label
					runes := []rune(label)
					if len(runes) > 40 {
						label = string(runes[:37]) + "..."
					}
					var comboLine string
					if j == m.panelState.settings.comboIdx {
						comboLine = cursorStyle.Render("    ▸ " + label)
					} else {
						comboLine = "      " + label
					}
					rows = append(rows, settingsLayoutRow{
						text:    comboLine,
						zoneID:  "panelComboItem",
						zoneIdx: j,
					})
					// Show option description on the selected combo item.
					if j == m.panelState.settings.comboIdx && opt.Description != "" {
						rows = append(rows, settingsLayoutRow{
							text: descStyle.Render("        " + opt.Description),
						})
					}
				}
				// Combo hint
				rows = append(rows, settingsLayoutRow{
					text: descStyle.Render("    " + m.locale.PanelComboHint),
				})
			}
		}
	}

	// Bottom buttons: always show Save and Cancel buttons.
	// These are clickable mouse zones for users who don't know keyboard shortcuts.
	if !m.panelState.settings.editing && !m.panelState.settings.combo {
		// blank line
		rows = append(rows, settingsLayoutRow{})
		// Save button — styled prominently + Cancel button (single line, two inline zones)
		saveBtn := "  " + m.locale.PanelBtnSave + "  "
		saveOsc := fmt.Sprintf("\x1b]8;;xbot://panel-save\x1b\\%s\x1b]8;;\x1b\\", saveBtn)
		cancelBtn := "  " + m.locale.PanelBtnCancel + "  "
		cancelOsc := fmt.Sprintf("\x1b]8;;xbot://panel-cancel\x1b\\%s\x1b]8;;\x1b\\", cancelBtn)
		rows = append(rows, settingsLayoutRow{
			text:    "  " + saveOsc + "    " + cancelOsc,
			zoneID:  "panelSaveCancel",
			zoneIdx: 0,
		})
		// Keyboard hint below buttons (secondary, for discoverability)
		rows = append(rows, settingsLayoutRow{
			text: hintStyle.Render("  " + m.locale.PanelNavHint),
		})
	}

	return rows
}

// settingsOverlayExtra computes the number of lines below the cursor item that
// must remain visible (API-key button, edit/combo overlay). Derived from
// buildSettingsLayout so it stays in sync with rendering. Returns 0 when not
// in edit/combo mode (cursor navigation only needs the item line itself).
func (m *cliModel) settingsOverlayExtra() int {
	if !m.panelState.settings.editing && !m.panelState.settings.combo {
		return 0
	}
	rows := m.buildSettingsLayout()
	cursorLine := m.settingsCursorLine()
	extra := 0
	for j := cursorLine + 1; j < len(rows); j++ {
		row := rows[j]
		// Stop at the next schema item or bottom-button section.
		if row.zoneID == "panelItem" || row.zoneID == "panelToggle" ||
			row.zoneID == "panelCombo" || row.zoneID == "panelSaveCancel" {
			break
		}
		// Stop at a blank separator line (category/bottom separator).
		if row.text == "" && row.zoneID == "" {
			break
		}
		extra++
	}
	return extra
}

func (m *cliModel) viewSettingsPanel() string {
	rows := m.buildSettingsLayout()
	var sb strings.Builder
	for _, row := range rows {
		sb.WriteString(row.text)
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

package cli

import (
	"fmt"
	"strings"
	ch "xbot/channel"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Wizard step constants.
const (
	wizardLang     = 0
	wizardProvider = 1
	wizardAPIKey   = 2
	wizardDone     = 3
)

// wizardLangOptions returns the language choices for the wizard.
var wizardLangOptions = []struct {
	Label string
	Code  string
}{
	{"🇨🇳  中文", "zh"},
	{"🇺🇸  English", "en"},
	{"🇯🇵  日本語", "ja"},
}

// wizardProviderList returns provider options from locale.
func (m *cliModel) wizardProviderList() []ch.SettingOption {
	for _, def := range m.locale.SetupSchema {
		if def.Key == "llm_provider" {
			return def.Options
		}
	}
	return nil
}

// --- Rendering ---

func (m *cliModel) renderWizard() string {
	switch m.panelState.wizardStep {
	case wizardLang:
		return m.renderWizardLang()
	case wizardProvider:
		return m.renderWizardProvider()
	case wizardAPIKey:
		return m.renderWizardAPIKey()
	case wizardDone:
		return m.renderWizardDone()
	}
	return ""
}

func (m *cliModel) renderWizardLang() string {
	w := m.width - 4
	if w > 60 {
		w = 60
	}
	sb := strings.Builder{}
	sb.WriteString(m.wizardTitle("🌍  选择你的语言  /  Choose your language"))
	sb.WriteString("\n\n")

	for i, opt := range wizardLangOptions {
		if i == m.panelState.wizardLangSel {
			sb.WriteString(m.wizardSelLine(opt.Label, w))
		} else {
			sb.WriteString(m.wizardUnselLine(opt.Label, w))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	// Back button — closes panel since this is the first step
	sb.WriteString("    ← " + m.locale.WizardBackBtn)
	sb.WriteString("\n\n")
	sb.WriteString(m.wizardHint("↑↓ / 点击选择 · Enter 确认 · Esc 关闭"))
	return sb.String()
}

func (m *cliModel) renderWizardProvider() string {
	w := m.width - 4
	if w > 80 {
		w = 80
	}
	sb := strings.Builder{}
	sb.WriteString(m.wizardTitle(m.locale.WizardProviderTitle))
	sb.WriteString("\n\n")

	opts := m.wizardProviderList()
	for i, opt := range opts {
		if i == m.panelState.wizardProvSel {
			sb.WriteString(m.wizardSelLine(opt.Label, w))
			if opt.Description != "" {
				sb.WriteString("\n")
				sb.WriteString(m.styles.TextSecondarySt.Render("      " + opt.Description))
			}
		} else {
			sb.WriteString(m.wizardUnselLine(opt.Label, w))
			if opt.Description != "" {
				sb.WriteString("\n")
				sb.WriteString(m.styles.TextMutedSt.Render("      " + opt.Description))
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	// Back button
	sb.WriteString("    ← " + m.locale.WizardBackBtn)
	sb.WriteString("\n\n")
	sb.WriteString(m.wizardHint(m.locale.WizardNavHint))
	return sb.String()
}

func (m *cliModel) renderWizardAPIKey() string {
	provider := m.panelState.values["llm_provider"]
	providerLabel := provider
	for _, opt := range m.wizardProviderList() {
		if opt.Value == provider {
			providerLabel = opt.Label
			break
		}
	}

	sb := strings.Builder{}
	sb.WriteString(m.wizardTitle(fmt.Sprintf(m.locale.WizardKeyTitle, providerLabel)))
	sb.WriteString("\n\n")

	// "Get key" button — clickable, opens browser
	guide, hasGuide := ch.ProviderSetupGuides[provider]
	if hasGuide && guide.URL != "" {
		btnLabel := "  " + m.locale.PanelBtnGetKey + "  "
		oscLink := fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", guide.URL, btnLabel)
		sb.WriteString("  ")
		sb.WriteString(oscLink)
		sb.WriteString("\n\n")
	} else if hasGuide && guide.URL == "" {
		hint := ""
		if m.locale.ProviderHints != nil {
			hint = m.locale.ProviderHints[guide.HintKey]
		}
		if hint != "" {
			sb.WriteString("  " + hint)
			sb.WriteString("\n\n")
		}
	}

	// Single input for API key
	sb.WriteString("  " + m.locale.WizardKeyLabel)
	sb.WriteString("\n  ")
	sb.WriteString(m.panelState.wizardKeyTI.View())
	sb.WriteString("\n\n")

	// Save + Back buttons
	sb.WriteString("    ✅ " + m.locale.PanelBtnSave)
	sb.WriteString("        ← " + m.locale.WizardBackBtn)
	sb.WriteString("\n\n")

	sb.WriteString(m.wizardHint(m.locale.WizardNavHint))
	return sb.String()
}

func (m *cliModel) renderWizardDone() string {
	w := m.width - 4
	if w > 80 {
		w = 80
	}
	sb := strings.Builder{}
	sb.WriteString(m.wizardTitle(m.locale.WizardDoneTitle))
	sb.WriteString("\n\n")

	if m.locale.SetupWelcome != "" {
		for _, l := range strings.Split(m.locale.SetupWelcome, "\n") {
			line := "  " + l
			// Truncate to panel content width to prevent PanelBox from wrapping.
			// Wrapping adds extra rendered lines, making zone Y-coordinates wrong.
			for lipgloss.Width(line) > w {
				runes := []rune(line)
				if len(runes) <= 1 {
					break
				}
				line = string(runes[:len(runes)-1])
			}
			sb.WriteString(line + "\n")
		}
	}

	sb.WriteString("\n\n")
	sb.WriteString("    🚀 " + m.locale.WizardStartBtn)
	sb.WriteString("        ← " + m.locale.WizardBackBtn)
	sb.WriteString("\n")

	return sb.String()
}

// --- Style helpers ---

func (m *cliModel) wizardTitle(text string) string {
	w := m.width - 4
	if w > 80 {
		w = 80
	}
	stepLabels := []string{"1/3", "2/3", "3/3", "✓"}
	step := stepLabels[min(m.panelState.wizardStep, 3)]
	return m.styles.PanelHeader.Width(w).Render(fmt.Sprintf(" %s  %s", step, text))
}

func (m *cliModel) wizardSelLine(text string, width int) string {
	// Use Accent style for selected items — gives bold colored text + pointer
	return m.styles.Accent.Width(width).Render("▸ " + text)
}

func (m *cliModel) wizardUnselLine(text string, width int) string {
	return m.styles.TextMutedSt.Width(width).Render("  " + text)
}

func (m *cliModel) wizardHint(text string) string {
	return m.styles.PanelHint.Render("  " + text)
}

// --- Keyboard ---

func (m *cliModel) handleWizardKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch m.panelState.wizardStep {
	case wizardLang:
		return m.wizardLangKey(msg)
	case wizardProvider:
		return m.wizardProvKey(msg)
	case wizardAPIKey:
		return m.wizardKeyInput(msg)
	case wizardDone:
		return m.wizardDoneKey(msg)
	}
	return false, nil
}

func (m *cliModel) wizardLangKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.panelState.wizardLangSel > 0 {
			m.panelState.wizardLangSel--
		}
	case "down", "j":
		if m.panelState.wizardLangSel < len(wizardLangOptions)-1 {
			m.panelState.wizardLangSel++
		}
	case "enter":
		m.wizardConfirmLang(m.panelState.wizardLangSel)
	case "esc":
		m.closePanel()
	}
	return true, nil
}

func (m *cliModel) wizardProvKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	opts := m.wizardProviderList()
	switch msg.String() {
	case "up", "k":
		if m.panelState.wizardProvSel > 0 {
			m.panelState.wizardProvSel--
		}
	case "down", "j":
		if m.panelState.wizardProvSel < len(opts)-1 {
			m.panelState.wizardProvSel++
		}
	case "enter":
		m.wizardConfirmProvider(m.panelState.wizardProvSel)
	case "esc":
		m.panelState.wizardStep = wizardLang
	}
	return true, nil
}

func (m *cliModel) wizardKeyInput(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "ctrl+s", "enter":
		m.panelState.values["llm_api_key"] = m.panelState.wizardKeyTI.Value()
		m.panelState.wizardStep = wizardDone
		return true, nil
	case "esc":
		m.panelState.wizardStep = wizardProvider
		return true, nil
	default:
		var cmd tea.Cmd
		m.panelState.wizardKeyTI, cmd = m.panelState.wizardKeyTI.Update(msg)
		return true, cmd
	}
}

func (m *cliModel) wizardDoneKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "enter":
		return true, m.wizardFinish()
	case "esc":
		provider := m.panelState.values["llm_provider"]
		if provider == "ollama" {
			m.panelState.wizardStep = wizardProvider
		} else {
			m.panelState.wizardStep = wizardAPIKey
			m.panelState.wizardKeyTI.SetValue(m.panelState.values["llm_api_key"])
			m.panelState.wizardKeyTI.Focus()
		}
	}
	return true, nil
}

// --- Confirm helpers (shared by keyboard + mouse) ---

func (m *cliModel) wizardConfirmLang(idx int) {
	if idx < 0 || idx >= len(wizardLangOptions) {
		return
	}
	code := wizardLangOptions[idx].Code
	m.locale = ch.GetLocale(code)
	m.panelState.values["language"] = code
	m.panelState.wizardStep = wizardProvider
	m.panelState.wizardProvSel = 0
}

func (m *cliModel) wizardConfirmProvider(idx int) {
	opts := m.wizardProviderList()
	if idx < 0 || idx >= len(opts) {
		return
	}
	provider := opts[idx].Value
	m.panelState.values["llm_provider"] = provider
	if url, ok := ch.ProviderDefaultURLs[provider]; ok {
		m.panelState.values["llm_base_url"] = url
	}
	if model, ok := ch.ProviderRecommendedModels[provider]; ok {
		m.panelState.values["llm_model"] = model
	}
	m.updateAPIKeyHint(provider)
	m.rebuildVisibleSchema()

	if provider == "ollama" {
		m.panelState.wizardStep = wizardDone
	} else {
		m.panelState.wizardStep = wizardAPIKey
		m.panelState.wizardKeyTI.SetValue("")
		m.panelState.wizardKeyTI.Focus()
	}
}

func (m *cliModel) wizardFinish() tea.Cmd {
	onSubmit := m.panelState.onSubmit
	panelVals := m.panelState.values
	m.closePanel()
	if onSubmit != nil && panelVals != nil {
		m.panelState.settingsSaving = true
		return m.doSaveSettings(onSubmit, panelVals)
	}
	return nil
}

// --- Mouse zone tracking ---

func (m *cliModel) trackWizardZones(zb *mouseZoneBuilder, contentStartY, visibleH int) {
	scrollY := m.panelState.scrollY

	type wLine struct {
		zoneID  string
		zoneIdx int
	}
	var lines []wLine

	switch m.panelState.wizardStep {
	case wizardLang:
		lines = append(lines, wLine{}) // title
		lines = append(lines, wLine{}) // blank
		for i := range wizardLangOptions {
			lines = append(lines, wLine{zoneID: "wizardLang", zoneIdx: i})
		}
		lines = append(lines, wLine{})                     // blank
		lines = append(lines, wLine{zoneID: "wizardBack"}) // back button
		lines = append(lines, wLine{})                     // blank
		lines = append(lines, wLine{})                     // hint

	case wizardProvider:
		lines = append(lines, wLine{}) // title
		lines = append(lines, wLine{}) // blank
		opts := m.wizardProviderList()
		for i, opt := range opts {
			lines = append(lines, wLine{zoneID: "wizardProv", zoneIdx: i})
			if opt.Description != "" {
				lines = append(lines, wLine{})
			}
		}
		lines = append(lines, wLine{})                     // blank
		lines = append(lines, wLine{zoneID: "wizardBack"}) // back button
		lines = append(lines, wLine{})                     // blank
		lines = append(lines, wLine{})                     // hint

	case wizardAPIKey:
		lines = append(lines, wLine{}) // title
		lines = append(lines, wLine{}) // blank
		provider := m.panelState.values["llm_provider"]
		guide, hasGuide := ch.ProviderSetupGuides[provider]
		if hasGuide && guide.URL != "" {
			lines = append(lines, wLine{zoneID: "panelOpenURL"}) // button
			lines = append(lines, wLine{})                       // blank
		} else if hasGuide {
			lines = append(lines, wLine{}) // info text
			lines = append(lines, wLine{}) // blank
		}
		lines = append(lines, wLine{})                         // key label
		lines = append(lines, wLine{})                         // input
		lines = append(lines, wLine{})                         // blank
		lines = append(lines, wLine{zoneID: "wizardSaveLine"}) // save+back buttons
		lines = append(lines, wLine{})                         // blank
		lines = append(lines, wLine{})                         // hint

	case wizardDone:
		lines = append(lines, wLine{}) // title
		lines = append(lines, wLine{}) // blank
		if m.locale.SetupWelcome != "" {
			for range strings.Split(m.locale.SetupWelcome, "\n") {
				lines = append(lines, wLine{})
			}
		}
		lines = append(lines, wLine{})                          // blank
		lines = append(lines, wLine{})                          // blank
		lines = append(lines, wLine{zoneID: "wizardStartBack"}) // start + back buttons on same line
	}

	for ln := scrollY; ln < len(lines) && zb.y < contentStartY+visibleH; ln++ {
		info := lines[ln]
		switch info.zoneID {
		case "wizardLang", "wizardProv":
			zb.add(1, info.zoneID, info.zoneIdx)
		case "panelOpenURL":
			zb.add(1, "panelOpenURL", 0)
		case "wizardBack":
			zb.add(1, "wizardBack", 0)
		case "wizardSaveLine":
			zb.addX(0, 0, 20, "wizardSave", 0)
			zb.addX(0, 24, 40, "wizardBack", 0)
			zb.skip(1)
		case "wizardStartBack":
			zb.addX(0, 2, 22, "wizardStart", 0)
			zb.addX(0, 26, 46, "wizardBack", 0)
			zb.skip(1)
		default:
			zb.skip(1)
		}
	}
}

// handleWizardClick dispatches wizard mouse clicks.
func (m *cliModel) handleWizardClick(zone mouseZone) (bool, tea.Model, tea.Cmd) {
	switch zone.ID {
	case "wizardLang":
		m.wizardConfirmLang(zone.Index)
		return true, m, nil
	case "wizardProv":
		m.wizardConfirmProvider(zone.Index)
		return true, m, nil
	case "wizardSave":
		m.panelState.values["llm_api_key"] = m.panelState.wizardKeyTI.Value()
		m.panelState.wizardStep = wizardDone
		return true, m, nil
	case "wizardBack":
		switch m.panelState.wizardStep {
		case wizardLang:
			m.closePanel()
		case wizardProvider:
			m.panelState.wizardStep = wizardLang
		case wizardAPIKey:
			m.panelState.wizardStep = wizardProvider
		case wizardDone:
			provider := m.panelState.values["llm_provider"]
			if provider == "ollama" {
				m.panelState.wizardStep = wizardProvider
			} else {
				m.panelState.wizardStep = wizardAPIKey
				m.panelState.wizardKeyTI.SetValue(m.panelState.values["llm_api_key"])
				m.panelState.wizardKeyTI.Focus()
			}
		}
		return true, m, nil
	case "wizardStart":
		return true, m, m.wizardFinish()
	}
	return false, m, nil
}

// updateWizardPanel handles keyboard events in wizard mode.
func (m *cliModel) updateWizardPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	handled, cmd := m.handleWizardKey(msg)
	return handled, m, cmd
}

// openWizardPanel opens the step-by-step setup wizard.
func (m *cliModel) openWizardPanel() {
	m.panelState.mode = "wizard"
	m.relayoutViewport()
	m.panelState.wizardStep = wizardLang
	m.panelState.wizardLangSel = 0
	m.panelState.wizardProvSel = 0
	m.panelState.values = make(map[string]string)
	m.panelState.schemaFull = make([]ch.SettingDefinition, len(m.locale.SetupSchema))
	copy(m.panelState.schemaFull, m.locale.SetupSchema)
	for _, def := range m.panelState.schemaFull {
		if def.DefaultValue != "" {
			m.panelState.values[def.Key] = def.DefaultValue
		}
	}
	m.panelState.onSubmit = func(vals map[string]string) {
		m.persistCLISettingsValues(vals)
	}
	m.panelState.isSetup = true
	ti := textinput.New()
	ti.Placeholder = "sk-..."
	ti.Prompt = "  "
	ti.CharLimit = 200
	ti.SetWidth(max(min(m.width-10, 60), 20))
	ti.Focus()
	tiStyles := ti.Styles()
	tiStyles.Focused.Prompt = m.styles.TIPrompt
	tiStyles.Focused.Text = m.styles.TIText
	tiStyles.Focused.Placeholder = m.styles.TIPlaceholder
	tiStyles.Cursor.Color = m.styles.TICursor.GetForeground()
	ti.SetStyles(tiStyles)
	m.panelState.wizardKeyTI = ti
}

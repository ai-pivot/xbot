package cli

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	ch "xbot/channel"
	"xbot/tools"
)

type panelAgentEntry struct {
	Role         string // role name (e.g. "explore")
	Instance     string // instance ID
	Running      bool   // true = currently executing
	Background   bool   // true = background mode
	Task         string // one-shot subagent task description
	Preview      string // latest progress/last reply summary
	ParentChatID string // parent session chatID for session isolation
}

type panelStackEntry struct {
	mode        string // panelMode value ("settings", etc.)
	cursor      int    // panelCursor
	scrollY     int    // panelScrollY
	values      map[string]string
	schema      []ch.SettingDefinition
	onSubmit    func(values map[string]string)
	fromPalette bool // true = ESC should reopen command palette instead of restoring mode
}

// pushPanel saves the current panel state onto the navigation stack.
// The caller should set the new panelMode afterwards (via openXxxPanel).
// Used when navigating from a parent panel (e.g. Settings) to a child panel.
func (m *cliModel) pushPanel() {
	m.panelStack = append(m.panelStack, panelStackEntry{
		mode:     m.panelMode,
		cursor:   m.panelCursor,
		scrollY:  m.panelScrollY,
		values:   m.panelValues,
		schema:   m.panelSchema,
		onSubmit: m.panelOnSubmit,
	})
}

// pushPanelFromPalette saves a marker so that popPanel reopens the palette
// instead of restoring a previous panel. Called when a palette command opens a panel.
func (m *cliModel) pushPanelFromPalette() {
	m.panelStack = append(m.panelStack, panelStackEntry{fromPalette: true})
}

// popPanel restores the parent panel state from the navigation stack.
// Returns true if a parent panel was restored, false if the stack is empty
// (meaning we should close the panel entirely).
func (m *cliModel) popPanel() bool {
	if len(m.panelStack) == 0 {
		return false
	}
	// Pop the last entry
	entry := m.panelStack[len(m.panelStack)-1]
	m.panelStack = m.panelStack[:len(m.panelStack)-1]

	if entry.fromPalette {
		// Clean up current panel state entirely, then reopen palette
		m.closePanel()
		m.openCommandPalette()
		return true
	}

	// Restore parent panel state
	m.panelMode = entry.mode
	m.panelCursor = entry.cursor
	m.panelScrollY = entry.scrollY
	m.panelValues = entry.values
	m.panelSchema = entry.schema
	m.panelOnSubmit = entry.onSubmit
	m.panelEdit = false
	m.panelCombo = false
	m.relayoutViewport()
	return true
}

// renderSelLine renders a settings panel selected row left-aligned to the given width.
// lipgloss v2 Width() defaults to centering; this helper avoids that by manual padding.
func (m *cliModel) renderSelLine(line string, w int) string {
	// Use w-2 to leave room for scrollbar (1 char) + spacing (1 char).
	// When scrollbar is not shown, applyScrollbar won't be called, so
	// the shorter padding is fine (panel box clips the content anyway).
	padW := w - 2
	if padW < 10 {
		padW = 10
	}
	vw := lipgloss.Width(line)
	if vw < padW {
		line += strings.Repeat(" ", padW-vw)
	}
	return m.styles.SettingsSelBg.Render(line)
}

// closePanel deactivates any active panel.
func (m *cliModel) closePanel() {
	m.panelMode = ""
	m.panelStack = nil
	m.panelEdit = false
	m.panelCombo = false
	m.panelSchema = nil
	m.panelValues = nil
	m.panelPrevProvider = ""
	m.panelOnSubmit = nil
	m.panelItems = nil
	m.panelTab = 0
	m.panelOptSel = nil
	m.panelOptCursor = nil
	// Bg tasks/agents panel cleanup
	m.cleanupCompletedBgTasks()
	m.panelBgTasks = nil
	m.panelBgAgents = nil
	m.panelBgViewing = false
	m.panelScrollY = 0
	m.panelBgLogLines = nil
	m.panelBgLogFollow = false
	// Danger zone cleanup
	m.panelDangerItems = nil
	m.panelDangerCursor = 0
	m.panelDangerConfirm = false
	m.panelDangerOnExec = nil
	// Runner panel cleanup
	m.panelRunnerServerTI = textinput.Model{}
	m.panelRunnerTokenTI = textinput.Model{}
	m.panelRunnerWorkspace = textinput.Model{}
	m.panelRunnerEditField = 0
	// 恢复 viewport 到正常模式高度
	m.panelScrollY = 0
	m.relayoutViewport()
}

// sanitizeOutputLine sanitizes a single output line for safe TUI rendering.
// Delegates to the shared tools.SanitizeOutputLine which handles \r
// carriage-return overwrites (progress bars like tqdm) and ANSI escape
// sequences (color codes, cursor movement).
func sanitizeOutputLine(line string) string {
	return tools.SanitizeOutputLine(line)
}

// sanitizeOutputLines splits raw process output into sanitized display lines.
// Delegates to tools.SanitizeOutputLines for \r stripping, ANSI removal, and
// empty-line filtering.
func sanitizeOutputLines(raw string) []string {
	return tools.SanitizeOutputLines(raw)
}

// splitLines splits a string into lines, preserving trailing empty line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// updatePanel handles key events when a panel is active.
// Returns (handled, newModel, cmd).
func (m *cliModel) updatePanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	if m.panelMode == "" {
		return false, m, nil
	}

	handled, newModel, cmd := func() (bool, tea.Model, tea.Cmd) {
		switch m.panelMode {
		case "settings":
			return m.updateSettingsPanel(msg)
		case "wizard":
			return m.updateWizardPanel(msg)
		case "askuser":
			return m.updateAskUserPanel(msg)
		case "bgtasks":
			return m.updateBgTasksPanel(msg)
		case "sessions":
			return m.updateSessionsPanel(msg)
		case "danger":
			return m.updateDangerPanel(msg)
		case "runner":
			return m.updateRunnerPanel(msg)
		case "approval":
			return m.updateApprovalPanel(msg)
		case "channel":
			return m.updateChannelPanel(msg)
		}
		return false, m, nil
	}()

	// 对有 cursor 导航的 panel：cursor 超出可见区域时自动滚动
	if handled {
		switch m.panelMode {
		case "settings":
			m.ensurePanelCursorVisible()
		case "askuser":
			m.ensureAskUserVisible()
		}
	}

	return handled, newModel, cmd
}

// viewPanel renders the active panel as a string.
func (m *cliModel) viewPanel() string {
	var raw string
	switch m.panelMode {
	case "settings":
		raw = m.viewSettingsPanel()
	case "wizard":
		raw = m.renderWizard()
	case "askuser":
		raw = m.viewAskUserPanel()
	case "bgtasks":
		raw = m.viewBgTasksPanel()
	case "sessions":
		raw = m.viewSessionsPanel()
	case "danger":
		raw = m.viewDangerPanel()
	case "runner":
		raw = m.viewRunnerPanel()
	case "approval":
		raw = m.viewApprovalPanel()
	case "channel":
		raw = m.viewChannelPanel()
	default:
		return ""
	}
	return raw
}

// newPanelTextInput 创建一个配置好的 textinput 用于面板输入
func (m *cliModel) newPanelTextInput(value, placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Prompt = ""
	ti.CharLimit = 200
	ti.SetWidth(m.panelWidth(50))
	ti.SetValue(value)
	if value != "" {
		ti.CursorEnd()
	}
	tiStyles := ti.Styles()
	tiStyles.Focused.Prompt = m.styles.TIPrompt
	tiStyles.Focused.Text = m.styles.TIText
	tiStyles.Focused.Placeholder = m.styles.TIPlaceholder
	tiStyles.Cursor.Color = m.styles.TICursor.GetForeground()
	ti.SetStyles(tiStyles)
	ti.Focus()
	return ti
}

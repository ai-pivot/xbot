package cli

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sahilm/fuzzy"
)

// ---------------------------------------------------------------------------
// §23 Command Palette (Ctrl+K)
// ---------------------------------------------------------------------------

// paletteActionKind defines the type of action a palette item performs.
type paletteActionKind int

const (
	paletteActionNone            paletteActionKind = iota
	paletteActionOpenPanel                         // opens a panel by mode name
	paletteActionOpenQuickSwitch                   // opens quick switch overlay
	paletteActionInsertText                        // inserts text into textarea
	paletteActionSendText                          // sends text as user message (slash command)
	paletteActionToggle                            // toggles a UI feature
	paletteActionQuit                              // quits the application
)

// PaletteCategory groups commands into tabs in the command palette.
type PaletteCategory string

const (
	PaletteCategorySystem  PaletteCategory = "System"
	PaletteCategoryUser    PaletteCategory = "User"
	PaletteCategoryPlugins PaletteCategory = "Plugins"
	PaletteCategorySkills  PaletteCategory = "Skills"
	PaletteCategoryAgents  PaletteCategory = "Agents"
)

// paletteCategories is the ordered list of visible categories.
var paletteCategories = []PaletteCategory{
	PaletteCategorySystem,
	PaletteCategoryUser,
	PaletteCategoryPlugins,
	PaletteCategorySkills,
	PaletteCategoryAgents,
}

// PaletteExternalCommand represents a command contributed by an external source
// (plugin, skill, user custom command).
type PaletteExternalCommand struct {
	Title       string
	Description string
	Category    PaletteCategory
	Content     string // content to insert or send
	Send        bool   // true = send immediately, false = insert into textarea
}

// PaletteContributor provides external commands for the command palette.
// Injected by the application layer to supply plugin/skill/agent/custom commands.
type PaletteContributor func() []PaletteExternalCommand

// paletteCommand represents a single item in the command palette.
type paletteCommand struct {
	ID          string
	Title       string
	Description string
	Shortcut    string
	Category    PaletteCategory
	ActionKind  paletteActionKind
	ActionData  string
}

// paletteFilterable adapts []paletteCommand for fuzzy matching.
type paletteFilterable []paletteCommand

func (p paletteFilterable) Len() int            { return len(p) }
func (p paletteFilterable) String(i int) string { return p[i].Title + " " + p[i].Description }

const paletteMaxVisible = 12

// buildPaletteCommands returns all available commands for the palette.
// Commands are grouped by category (tab-switchable) and presented as a flat searchable list.
func (m *cliModel) buildPaletteCommands() []paletteCommand {
	var cmds []paletteCommand

	// --- System ---
	cmds = append(cmds, paletteCommand{
		ID: "sessions", Title: "Open Sessions", Description: "switch between chat sessions",
		Shortcut: "Ctrl+T", Category: PaletteCategorySystem, ActionKind: paletteActionOpenPanel, ActionData: "sessions",
	})
	cmds = append(cmds, paletteCommand{
		ID: "switch_model", Title: "Models & Subscriptions", Description: "switch model / manage subscriptions (add, edit, disable, delete)",
		Shortcut: "Ctrl+N", Category: PaletteCategorySystem, ActionKind: paletteActionOpenQuickSwitch, ActionData: "model",
	})
	cmds = append(cmds, paletteCommand{
		ID: "bgtasks", Title: "Background Tasks", Description: "view running background tasks and agents",
		Shortcut: "^", Category: PaletteCategorySystem, ActionKind: paletteActionOpenPanel, ActionData: "bgtasks",
	})
	cmds = append(cmds, paletteCommand{
		ID: "runner", Title: "Runner Panel", Description: "connect to remote runner",
		Category: PaletteCategorySystem, ActionKind: paletteActionOpenPanel, ActionData: "runner",
	})
	cmds = append(cmds, paletteCommand{
		ID: "channel", Title: "ch.Channel Config", Description: "configure web/feishu/QQ channels",
		Category: PaletteCategorySystem, ActionKind: paletteActionOpenPanel, ActionData: "channel",
	})
	cmds = append(cmds, paletteCommand{
		ID: "clear", Title: "Clear Chat", Description: "start a fresh conversation",
		Shortcut: "/clear", Category: PaletteCategorySystem, ActionKind: paletteActionSendText, ActionData: "/clear",
	})
	cmds = append(cmds, paletteCommand{
		ID: "compress", Title: "Compress Context", Description: "compress conversation history",
		Shortcut: "/compress", Category: PaletteCategorySystem, ActionKind: paletteActionSendText, ActionData: "/compress",
	})
	cmds = append(cmds, paletteCommand{
		ID: "search", Title: "Search Messages", Description: "find text in conversation history",
		Category: PaletteCategorySystem, ActionKind: paletteActionInsertText, ActionData: "/search ",
	})
	cmds = append(cmds, paletteCommand{
		ID: "tool_detail", Title: "Toggle Tool Details", Description: "expand or collapse tool output",
		Shortcut: "Ctrl+O", Category: PaletteCategorySystem, ActionKind: paletteActionToggle, ActionData: "tool_summary",
	})
	cmds = append(cmds, paletteCommand{
		ID: "msg_fold", Title: "Toggle Message Fold", Description: "expand or collapse long messages",
		Shortcut: "Ctrl+E", Category: PaletteCategorySystem, ActionKind: paletteActionToggle, ActionData: "msg_fold",
	})
	cmds = append(cmds, paletteCommand{
		ID: "settings", Title: "Open Settings", Description: "configure LLM, sandbox, memory, etc.",
		Category: PaletteCategorySystem, ActionKind: paletteActionSendText, ActionData: "/settings",
	})
	cmds = append(cmds, paletteCommand{
		ID: "danger", Title: "Danger Zone", Description: "clear session, memory, history",
		Category: PaletteCategorySystem, ActionKind: paletteActionOpenPanel, ActionData: "danger",
	})
	cmds = append(cmds, paletteCommand{
		ID: "reload_plugins", Title: "Reload All Plugins", Description: "reload all plugins",
		Category: PaletteCategorySystem, ActionKind: paletteActionSendText, ActionData: "/plugin reload-all",
	})
	cmds = append(cmds, paletteCommand{
		ID: "install_plugin", Title: "Install Plugin", Description: "install a plugin from URL or path",
		Category: PaletteCategorySystem, ActionKind: paletteActionInsertText, ActionData: "/plugin install ",
	})
	cmds = append(cmds, paletteCommand{
		ID: "help", Title: "Help", Description: "show available slash commands and shortcuts",
		Shortcut: "/help", Category: PaletteCategorySystem, ActionKind: paletteActionSendText, ActionData: "/help",
	})
	cmds = append(cmds, paletteCommand{
		ID: "update", Title: "Check Update", Description: "check for new xbot versions",
		Shortcut: "/update", Category: PaletteCategorySystem, ActionKind: paletteActionSendText, ActionData: "/update",
	})
	cmds = append(cmds, paletteCommand{
		ID: "context", Title: "Context Info", Description: "show token usage and context info",
		Shortcut: "/context", Category: PaletteCategorySystem, ActionKind: paletteActionSendText, ActionData: "/context",
	})
	cmds = append(cmds, paletteCommand{
		ID: "setup", Title: "Setup Wizard", Description: "re-run initial setup wizard",
		Shortcut: "/setup", Category: PaletteCategorySystem, ActionKind: paletteActionSendText, ActionData: "/setup",
	})
	cmds = append(cmds, paletteCommand{
		ID: "models", Title: "List Models", Description: "show available LLM models",
		Shortcut: "/models", Category: PaletteCategorySystem, ActionKind: paletteActionSendText, ActionData: "/models",
	})
	cmds = append(cmds, paletteCommand{
		ID: "new", Title: "New Session", Description: "start a fresh chat session",
		Shortcut: "/new", Category: PaletteCategorySystem, ActionKind: paletteActionSendText, ActionData: "/new",
	})
	cmds = append(cmds, paletteCommand{
		ID: "rewind", Title: "Rewind", Description: "undo last conversation turn",
		Shortcut: "/rewind", Category: PaletteCategorySystem, ActionKind: paletteActionSendText, ActionData: "/rewind",
	})
	cmds = append(cmds, paletteCommand{
		ID: "cancel", Title: "Cancel", Description: "cancel current operation",
		Shortcut: "/cancel", Category: PaletteCategorySystem, ActionKind: paletteActionSendText, ActionData: "/cancel",
	})
	cmds = append(cmds, paletteCommand{
		ID: "quit", Title: "Quit", Description: "exit xbot",
		Shortcut: "Ctrl+Z", Category: PaletteCategorySystem, ActionKind: paletteActionQuit,
	})

	// --- External contributions (plugins, skills, agents, custom commands) ---
	if m.paletteContributor != nil {
		m.pluginCmdNames = nil // reset
		for _, ext := range m.paletteContributor() {
			kind := paletteActionInsertText
			if ext.Send {
				kind = paletteActionSendText
			}
			cat := ext.Category
			if cat == "" {
				cat = PaletteCategoryPlugins
			}
			cmds = append(cmds, paletteCommand{
				ID:          "ext:" + ext.Title,
				Title:       ext.Title,
				Description: ext.Description,
				Category:    cat,
				ActionKind:  kind,
				ActionData:  ext.Content,
			})
			// Collect slash command names for Tab completion
			if strings.HasPrefix(ext.Content, "/") {
				name := strings.SplitN(ext.Content, " ", 2)[0]
				// Avoid duplicates
				found := false
				for _, existing := range m.pluginCmdNames {
					if existing == name {
						found = true
						break
					}
				}
				if !found {
					m.pluginCmdNames = append(m.pluginCmdNames, name)
				}
			}
		}
	}

	return cmds
}

// openCommandPalette activates the command palette overlay.
func (m *cliModel) openCommandPalette() {
	if m.paletteOpen {
		m.closeCommandPalette()
		return
	}
	// Sync external contributor from channel (may change at runtime)
	if m.channel != nil && m.channel.PaletteContributor != nil {
		m.paletteContributor = m.channel.PaletteContributor
	}
	m.paletteOpen = true
	m.paletteItems = m.buildPaletteCommands()
	m.paletteFiltered = m.paletteItems
	m.paletteCursor = 0
	m.paletteScrollY = 0
	m.paletteActiveCategory = "" // show all categories by default

	ti := textinput.New()
	ti.Placeholder = "Type to filter commands…"
	ti.Prompt = " > "
	ti.CharLimit = 100
	ti.SetWidth(46)
	// Apply theme-matched styles
	tiStyles := ti.Styles()
	tiStyles.Focused.Prompt = m.styles.TIPrompt
	tiStyles.Focused.Text = m.styles.TIText
	tiStyles.Focused.Placeholder = m.styles.TIPlaceholder
	tiStyles.Cursor.Color = m.styles.TICursor.GetForeground()
	ti.SetStyles(tiStyles)
	ti.Focus()
	m.paletteInput = ti
}

// closeCommandPalette deactivates the command palette overlay.
func (m *cliModel) closeCommandPalette() {
	m.paletteOpen = false
	m.paletteInput.Blur()
}

// filterPaletteCommands filters commands by the current input query using fuzzy matching.
// Respects the active category tab — only shows commands in the current category.
func (m *cliModel) filterPaletteCommands() {
	query := m.paletteInput.Value()
	active := m.paletteActiveCategory

	source := m.paletteItems
	// Filter by active category (unless "all")
	if active != "" {
		filtered := make([]paletteCommand, 0, len(source))
		for _, cmd := range source {
			if cmd.Category == active {
				filtered = append(filtered, cmd)
			}
		}
		source = filtered
	}

	if query == "" {
		m.paletteFiltered = source
		return
	}
	fuzzySource := paletteFilterable(source)
	matches := fuzzy.FindFrom(query, fuzzySource)
	m.paletteFiltered = make([]paletteCommand, 0, len(matches))
	for _, match := range matches {
		m.paletteFiltered = append(m.paletteFiltered, source[match.Index])
	}
}

// applyPaletteCommand executes the selected command and closes the palette.
func (m *cliModel) applyPaletteCommand() {
	if m.paletteCursor >= len(m.paletteFiltered) {
		m.closeCommandPalette()
		return
	}
	cmd := m.paletteFiltered[m.paletteCursor]
	m.closeCommandPalette()

	switch cmd.ActionKind {
	case paletteActionOpenPanel:
		// Push palette marker so ESC returns to palette
		m.pushPanelFromPalette()
		switch cmd.ActionData {
		case "sessions":
			m.openSessionsPanel()
		case "bgtasks":
			m.openBgTasksPanel()
		case "runner":
			m.openRunnerPanel()
		case "channel":
			m.openChannelPanel()
		case "danger":
			m.openDangerPanelFromSettings()
		}
	case paletteActionOpenQuickSwitch:
		if m.subscriptionMgr != nil {
			m.openQuickSwitch(cmd.ActionData)
		}
	case paletteActionInsertText:
		m.textarea.SetValue(cmd.ActionData)
		m.autoExpandInput()
	case paletteActionSendText:
		// Slash commands that open panels: push palette marker for ESC-back-to-palette
		if cmd.ActionData == "/settings" || cmd.ActionData == "/channel" {
			m.pushPanelFromPalette()
		}
		if fn := m.sendMessage(cmd.ActionData); fn != nil {
			m.pendingCmds = append(m.pendingCmds, fn)
		}
	case paletteActionToggle:
		switch cmd.ActionData {
		case "tool_summary":
			m.toggleToolSummary()
		case "msg_fold":
			m.toggleMessageFold()
		}
	case paletteActionQuit:
		m.shouldQuit = true
	}
}

// cyclePaletteCategory moves to the next/previous category tab.
func (m *cliModel) cyclePaletteCategory(dir int) {
	// Build list of non-empty categories
	nonEmpty := make(map[PaletteCategory]bool)
	for _, cmd := range m.paletteItems {
		nonEmpty[cmd.Category] = true
	}
	var visible []PaletteCategory
	for _, cat := range paletteCategories {
		if nonEmpty[cat] {
			visible = append(visible, cat)
		}
	}
	if len(visible) == 0 {
		return
	}

	// Find current index
	curIdx := -1
	for i, cat := range visible {
		if cat == m.paletteActiveCategory {
			curIdx = i
			break
		}
	}

	// Move to next/prev (wrap around)
	nextIdx := curIdx + dir
	if nextIdx < 0 {
		nextIdx = len(visible) - 1
	} else if nextIdx >= len(visible) {
		nextIdx = 0
	}
	m.paletteActiveCategory = visible[nextIdx]
	m.paletteCursor = 0
	m.paletteScrollY = 0
	m.filterPaletteCommands()
}

// handlePaletteKey handles key events when the command palette is open.
// Returns (handled, cmd). When open, always returns handled=true to block all keys.
func (m *cliModel) handlePaletteKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if !m.paletteOpen {
		return false, nil
	}

	switch msg.Code {
	case tea.KeyEsc:
		m.closeCommandPalette()
		return true, nil
	case tea.KeyUp:
		if m.paletteCursor > 0 {
			m.paletteCursor--
			m.clampPaletteScroll()
		}
		return true, nil
	case tea.KeyDown:
		if m.paletteCursor < len(m.paletteFiltered)-1 {
			m.paletteCursor++
			m.clampPaletteScroll()
		}
		return true, nil
	case tea.KeyEnter:
		m.applyPaletteCommand()
		if len(m.pendingCmds) > 0 {
			pending := m.pendingCmds
			m.pendingCmds = nil
			return true, tea.Batch(pending...)
		}
		return true, nil
	}
	// Non-Code key checks (Tab, Shift+Tab, Enter fallback, etc.)
	switch msg.String() {
	case "enter", "ctrl+m":
		// Fallback Enter handling: some terminals/key protocols may encode Enter
		// as ctrl+m instead of tea.KeyEnter (e.g. flagCtrlM legacy encoding).
		m.applyPaletteCommand()
		if len(m.pendingCmds) > 0 {
			pending := m.pendingCmds
			m.pendingCmds = nil
			return true, tea.Batch(pending...)
		}
		return true, nil
	case "tab":
		// Cycle to next category
		m.cyclePaletteCategory(1)
		return true, nil
	case "shift+tab":
		// Cycle to previous category
		m.cyclePaletteCategory(-1)
		return true, nil
	}

	// Forward printable keys to textinput for filtering
	var cmd tea.Cmd
	m.paletteInput, cmd = m.paletteInput.Update(msg)
	prevFiltered := m.paletteFiltered
	m.filterPaletteCommands()
	// Reset cursor when filter results change (even if cursor is still in bounds,
	// the item at the old cursor position has likely changed).
	if len(m.paletteFiltered) != len(prevFiltered) || m.paletteCursor >= len(m.paletteFiltered) {
		m.paletteCursor = 0
		m.paletteScrollY = 0
	}
	return true, cmd
}

// clampPaletteScroll ensures the selected item stays visible in the list viewport.
func (m *cliModel) clampPaletteScroll() {
	if m.paletteCursor < m.paletteScrollY {
		m.paletteScrollY = m.paletteCursor
	}
	if m.paletteCursor >= m.paletteScrollY+paletteMaxVisible {
		m.paletteScrollY = m.paletteCursor - paletteMaxVisible + 1
	}
}

// viewCommandPalette renders the command palette overlay.
// Follows the same centering pattern as viewQuickSwitch.
func (m *cliModel) viewCommandPalette(width, height int) string {
	if !m.paletteOpen {
		return ""
	}

	paletteW := min(64, width-4)
	if paletteW < 30 {
		paletteW = 30
	}

	var lines []string

	// Title
	lines = append(lines, m.styles.PanelHeader.Render(" Command Palette"))

	// Category tabs
	nonEmpty := make(map[PaletteCategory]bool)
	for _, cmd := range m.paletteItems {
		nonEmpty[cmd.Category] = true
	}
	var tabParts []string
	for _, cat := range paletteCategories {
		if !nonEmpty[cat] {
			continue
		}
		label := string(cat)
		if cat == m.paletteActiveCategory {
			label = m.styles.Accent.Bold(true).Render(label)
		} else {
			label = m.styles.TextSecondarySt.Render(label)
		}
		tabParts = append(tabParts, label)
	}
	if len(tabParts) > 1 {
		tabLine := " " + strings.Join(tabParts, "  ")
		lines = append(lines, tabLine)
	}

	// Search input
	lines = append(lines, m.paletteInput.View())

	// Separator
	lines = append(lines, m.styles.TextSecondarySt.Render(strings.Repeat("─", paletteW-2)))

	// Command list (with scroll)
	total := len(m.paletteFiltered)
	start := m.paletteScrollY
	end := min(start+paletteMaxVisible, total)

	for i := start; i < end; i++ {
		cmd := m.paletteFiltered[i]
		selected := i == m.paletteCursor

		// Cursor indicator
		cursor := "  "
		titleStyle := m.styles.TextSecondarySt
		if selected {
			cursor = m.styles.Accent.Render("▸ ")
			titleStyle = m.styles.Accent
		}

		// Title (left) + shortcut (right-aligned)
		titleText := cmd.Title
		maxTitleLen := paletteW - 16
		if maxTitleLen < 10 {
			maxTitleLen = 10
		}
		if len(titleText) > maxTitleLen {
			titleText = titleText[:maxTitleLen-1] + "…"
		}

		shortcutText := ""
		if cmd.Shortcut != "" {
			shortcutText = m.styles.TextMutedSt.Render(cmd.Shortcut)
		}

		titleStyled := titleStyle.Render(titleText)
		titleVisualW := lipgloss.Width(titleStyled)
		shortcutVisualW := lipgloss.Width(shortcutText)
		gap := paletteW - 4 - titleVisualW - shortcutVisualW // -4 for cursor(2) + padding(2)
		if gap < 1 {
			gap = 1
		}

		line := cursor + titleStyled + strings.Repeat(" ", gap) + shortcutText
		if selected {
			line = m.styles.SettingsSelBg.Render(line)
		}
		lines = append(lines, line)
	}

	if total == 0 {
		lines = append(lines, m.styles.TextMutedSt.Render("  No matching commands"))
	}

	// Scroll indicator
	if total > paletteMaxVisible {
		pct := fmt.Sprintf(" %d/%d ", min(end, total), total)
		lines = append(lines, m.styles.TextMutedSt.Render(pct))
	}

	// Footer hints
	lines = append(lines, m.styles.TextSecondarySt.Render(" ↑↓ Navigate · Enter Select · Tab Category · Esc Close"))

	// Build bordered panel
	content := strings.Join(lines, "\n")
	box := m.styles.PanelBox.Render(content)

	// Center vertically on screen
	totalH := len(lines) + 2 // +2 for box border
	blankLines := max(0, (height-totalH)/2)
	var b strings.Builder
	for range blankLines {
		b.WriteString("\n")
	}
	b.WriteString(box)

	return b.String()
}

package channel

import (
	"fmt"
	"image/color"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
	"xbot/version"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// appendStatusHint appends a styled hint to the status line, with proper spacing.
func appendStatusHint(status, hint string) string {
	if hint == "" {
		return status
	}
	if status == "" {
		return hint
	}
	return status + "  " + hint
}

// isCompact returns true when terminal width < 80 — compact layout for narrow windows.
func (m *cliModel) isCompact() bool { return m.width < 80 }

// isNarrow returns true when terminal width < 60 — minimal layout.
func (m *cliModel) isNarrow() bool { return m.width < 60 }

// isWide returns true when terminal width >= 120 — wide layout with extra info.
func (m *cliModel) isWide() bool { return m.width >= 120 }

// chatWidth returns the effective width for the chat viewport, accounting for sidebar.
func (m *cliModel) chatWidth() int {
	if m.isWide() && m.sidebarEnabled && m.sidebarVisible {
		// sidebar: RoundedBorder(2) + Padding(0,1)(2) + content(sidebarWidth) = sidebarWidth+4
		w := m.width - m.sidebarWidth - 4
		if w < 20 {
			w = 20
		}
		return w
	}
	return m.width
}

// cliFormatTokenCount formats a token count with K/M/B suffixes for display.
func cliFormatTokenCount(n int64) string {
	if n >= 1_000_000_000 {
		return fmt.Sprintf("%.1fB", float64(n)/1_000_000_000)
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// renderTitleBar builds the top title bar with gradient wordmark, diagonal fill,
// mode label, hints, runner status, and user identity indicator.
// In compact mode (<80 cols), extras (runner, user) are hidden.
func (m *cliModel) renderTitleBar() string {
	titleLeft := m.titleText()
	titleRight := m.locale.TitleHint
	// Askuser panel: override titleRight with panel-specific hints (always visible)
	if m.panelMode == "askuser" {
		titleRight = m.askUserTitleHints()
	} else if m.updateNotice != nil && m.updateNotice.HasUpdate {
		titleRight = fmt.Sprintf("%s→%s · /update · /help", m.updateNotice.Current, m.updateNotice.Latest)
	}
	// Runner status + identity indicator — hidden in compact mode
	if !m.isCompact() {
		if m.runnerBridge != nil {
			switch m.runnerBridge.Status() {
			case RunnerConnected:
				titleRight = IconRunnerOn + " Runner · " + titleRight
			case RunnerConnecting:
				titleRight = IconRunnerWait + " Runner · " + titleRight
			}
		}
		if m.senderID != "cli_user" {
			titleRight = IconUser + " " + m.senderID + " · " + titleRight
		}
	}

	// Narrow: hide /help hint to save space
	if m.isNarrow() {
		titleRight = ""
	}
	titlePad := m.width - lipgloss.Width(titleLeft) - lipgloss.Width(titleRight)
	if titlePad < 1 {
		titlePad = 1
	}
	return m.styles.TitleBar.Render(titleLeft + strings.Repeat(" ", titlePad) + titleRight)
}

// renderInputArea renders the textarea input box with dynamic border color
// and manual placeholder overlay (avoids textarea's built-in placeholder
// which triggers CJK rendering bugs on Windows Terminal).
func (m *cliModel) renderInputArea(borderColor color.Color) string {
	// Use chatWidth so input box fits when sidebar is open
	w := m.chatWidth()
	inputBoxStyle := m.styles.InputBox.BorderForeground(borderColor).Width(w - 4)
	inputArea := m.textarea.View()

	// Render placeholder manually when textarea is empty.
	if m.textarea.Value() == "" && m.placeholderText != "" {
		taHeight := minTaHeight
		if h := m.textarea.Height(); h > 0 {
			taHeight = h
		}
		ph := m.placeholderText
		if tw := m.textarea.Width(); tw > 0 {
			ph = truncateToWidth(ph, tw)
		}
		phRunes := []rune(ph)
		if len(phRunes) > 0 {
			first := string(phRunes[0])
			rest := string(phRunes[1:])
			cursorColor := m.styles.TACursor.GetForeground()
			cursor := lipgloss.NewStyle().Foreground(cursorColor).Reverse(true).Render(first)
			phRendered := cursor + m.styles.PlaceholderSt.Render(rest)
			lines := make([]string, taHeight)
			lines[0] = phRendered
			for i := 1; i < taHeight; i++ {
				lines[i] = ""
			}
			inputArea = strings.Join(lines, "\n")
		}
	}

	inputRendered := inputBoxStyle.Render(inputArea)

	// Replace top border with context usage progress bar
	if newTop := m.renderContextTopBorder(borderColor, inputRendered); newTop != "" {
		_, rest, found := strings.Cut(inputRendered, "\n")
		if found {
			return newTop + "\n" + rest
		}
	}

	return inputRendered
}

// renderReadyStatus builds the "Ready" status bar line with message count,
// model name, agent session indicator, and right-aligned context usage bar.
func (m *cliModel) renderReadyStatus() string {
	readyParts := []string{m.locale.StatusReady}
	// Session indicator (for agent sessions)
	if m.channelName == "agent" {
		parts := strings.SplitN(m.chatID, "/", 2)
		if len(parts) == 2 {
			readyParts = append(readyParts, fmt.Sprintf("%s %s", IconRobot, parts[1]))
		} else {
			readyParts = append(readyParts, fmt.Sprintf("%s %s", IconRobot, m.chatID))
		}
	}
	// Message count
	msgCount := len(m.messages)
	if msgCount > 0 {
		s := ""
		if msgCount > 1 {
			s = "s"
		}
		readyParts = append(readyParts, fmt.Sprintf("%d msg%s", msgCount, s))
	}
	// Model name (cached, avoids per-frame lookup)
	if m.cachedModelName != "" {
		modelHint := m.cachedModelName
		if m.modelCount > 1 && !m.isCompact() {
			modelHint += " [Ctrl+N]"
		}
		readyParts = append(readyParts, modelHint)
	}
	// Narrow screen: drop msg count to save space
	if m.isNarrow() && len(readyParts) > 2 {
		readyParts = readyParts[:2]
	}
	leftParts := strings.Join(readyParts, " · ")

	// Wide screen: append token usage
	if m.isWide() && m.lastTokenUsage != nil {
		total := m.lastTokenUsage.PromptTokens + m.lastTokenUsage.CompletionTokens
		if total > 0 {
			leftParts += fmt.Sprintf("  ·  tokens: %s", cliFormatTokenCount(m.lastTokenUsage.PromptTokens))
			if m.lastTokenUsage.CompletionTokens > 0 {
				leftParts += fmt.Sprintf(" + %s", cliFormatTokenCount(m.lastTokenUsage.CompletionTokens))
			}
		}
	}

	return m.styles.ReadyStatus.Render(leftParts)
}

// layoutSearch renders the search-mode layout: title bar, viewport, search bar,
// and input box.
func (m *cliModel) layoutSearch(titleBar, input string) string {
	var searchBar string
	if m.searchEditing {
		searchBar = m.styles.SearchBar.Render(m.searchTI.View())
	} else {
		searchBar = m.styles.SearchBar.Render(
			fmt.Sprintf(m.locale.SearchNavFormat, m.searchQuery, m.searchIdx+1, len(m.searchResults)))
	}
	return fmt.Sprintf("%s\n%s\n%s\n%s",
		titleBar, m.viewport.View(), searchBar, input)
}

// layoutAskUser renders the askuser panel layout: title bar, viewport,
// scrollable ask panel with progress indicator and optional scrollbar.
func (m *cliModel) layoutAskUser(titleBar string) string {
	askRaw := m.viewAskUserPanel()
	m.clampAskUserPanelScroll(askRaw)
	askLines := strings.Split(askRaw, "\n")
	fixedLines := 2 // titleBar (no toast)
	panelBorder := 2
	viewportH := m.layoutViewportHeight()
	askVisibleH := m.height - fixedLines - viewportH - panelBorder
	if askVisibleH < 3 {
		askVisibleH = 3
	}
	totalAskLines := len(askLines)
	if m.askPanelScrollY+askVisibleH > totalAskLines {
		m.askPanelScrollY = max(0, totalAskLines-askVisibleH)
	}
	end := m.askPanelScrollY + askVisibleH
	if end > totalAskLines {
		end = totalAskLines
	}
	visibleAsk := askLines[m.askPanelScrollY:end]
	askContent := strings.Join(visibleAsk, "\n")
	// Append scrollbar when content overflows
	if totalAskLines > askVisibleH && askVisibleH > 0 {
		contentWidth := m.width - 4 - 2 // PanelBox border(2) + padding(2) - scrollbar(2)
		if contentWidth < 10 {
			contentWidth = 10
		}
		askContent = m.applyScrollbar(askContent, contentWidth, totalAskLines, m.askPanelScrollY)
	}
	boxedAsk := m.styles.PanelBox.Render(askContent)
	// Scroll indicator — mouse wheel or ↑↓ at edges scrolls content
	scrollHint := ""
	if totalAskLines > askVisibleH {
		pct := (m.askPanelScrollY + askVisibleH) * 100 / totalAskLines
		scrollHint = m.styles.PanelDesc.Render(fmt.Sprintf(" [%d%%] ↕ scroll", pct))
	}
	return fmt.Sprintf("%s\n%s\n%s%s",
		titleBar, m.viewport.View(), boxedAsk, scrollHint)
}

// layoutPanel renders the generic panel-mode layout: title bar, scrollable
// panel content in a bordered box with optional scrollbar, and panel footer.
func (m *cliModel) layoutPanel(titleBar string) string {
	panelFooter := m.renderFooter()
	rawContent := m.viewPanel()
	m.clampPanelScroll(rawContent)
	rawLines := strings.Split(rawContent, "\n")
	visibleH := m.panelVisibleHeight()
	totalLines := len(rawLines)
	if m.panelScrollY+visibleH > totalLines {
		m.panelScrollY = max(0, totalLines-visibleH)
	}
	end := m.panelScrollY + visibleH
	if end > totalLines {
		end = totalLines
	}
	visible := rawLines[m.panelScrollY:end]
	panelContent := strings.Join(visible, "\n")
	// Append scrollbar when content overflows
	if totalLines > visibleH && visibleH > 0 {
		// contentWidth: PanelBox inner width minus border(2) minus padding(2)
		contentWidth := m.width - 4 - 2 // -2 for scrollbar + spacing
		if contentWidth < 10 {
			contentWidth = 10
		}
		panelContent = m.applyScrollbar(panelContent, contentWidth, totalLines, m.panelScrollY)
	}
	boxedContent := m.styles.PanelBox.Render(panelContent)
	return fmt.Sprintf("%s\n%s%s",
		titleBar, boxedContent, panelFooter)
}

// layoutMain renders the primary chat layout: title bar, viewport, status bar
// (with hints for temp status, new content), optional todo bar, footer (shortcuts),
// input box, and info bar below input.
func (m *cliModel) layoutMain(titleBar, input, completionsHint string) string {
	// Render status bar
	var status string
	if m.typing || m.progress != nil {
		thinkingStatusStyle := m.styles.ThinkingSt
		progressStyle := m.styles.Progress
		toolStyle := m.styles.Tool
		status = thinkingStatusStyle.Render(m.renderProgressStatus(progressStyle, toolStyle))
	} else if m.checkingUpdate {
		status = m.styles.ThinkingSt.Render(m.locale.CheckingUpdates)
	} else if completionsHint != "" {
		status = completionsHint
	} else {
		status = m.renderReadyStatus()
	}

	// Accumulate status hints
	var hints []string
	if m.tempStatus != "" {
		hints = append(hints, m.styles.WarningSt.Render(m.tempStatus))
	}
	if m.newContentHint {
		hints = append(hints, m.styles.InfoSt.Render(m.locale.NewContentHint))
	}
	if len(hints) > 0 {
		status = appendStatusHint(status, strings.Join(hints, "  "))
	}

	// Inject widget content into bars
	titleBar = m.augmentTitleBar(titleBar)
	status = m.augmentStatusBar(status)
	footer := m.renderFooter()
	footer = m.augmentFooter(footer)
	infoBar := m.renderInfoBar()
	infoBar = m.augmentInfoBar(infoBar)

	// Layout assembly — build progressively so empty sections don't add blank lines.
	showSidebar := m.isWide() && m.sidebarEnabled && m.sidebarVisible

	// Title bar is always full width
	var topLines []string
	topLines = append(topLines, titleBar)

	// Middle section: viewport + status + todo + footer + input + infoBar
	// When sidebar is visible, this whole section is squeezed to chatWidth
	var middleLines []string
	middleLines = append(middleLines, m.viewport.View())
	if status != "" {
		middleLines = append(middleLines, status)
	}
	todoBar := m.renderTodoBar()
	if todoBar != "" {
		middleLines = append(middleLines, todoBar)
	}
	if footer != "" {
		middleLines = append(middleLines, footer)
	}
	middleLines = append(middleLines, input)
	if infoBar != "" {
		middleLines = append(middleLines, infoBar)
	}
	middleBlock := strings.Join(middleLines, "\n")

	// Sidebar: spans the full height of the middle section (viewport → infoBar)
	if showSidebar {
		sidebar := m.renderSidebarForBlock(middleBlock)
		if m.sidebarPosition == "right" {
			return strings.Join(topLines, "\n") + "\n" +
				lipgloss.JoinHorizontal(lipgloss.Top, middleBlock, sidebar)
		}
		return strings.Join(topLines, "\n") + "\n" +
			lipgloss.JoinHorizontal(lipgloss.Top, sidebar, middleBlock)
	}

	return strings.Join(topLines, "\n") + "\n" + middleBlock
}

// renderSidebarForBlock renders the sidebar that spans the full height of the
// middle content block (viewport + status + footer + input).
// The block string is used only to measure height via line counting.
func (m *cliModel) renderSidebarForBlock(block string) string {
	sw := m.sidebarWidth
	if sw < 12 {
		sw = 12
	}

	// Measure middle block height
	h := strings.Count(block, "\n") + 1
	if h < 5 {
		h = 5
	}

	contentW := sw // content width = sidebarWidth; border + padding from SidebarBg style

	// Only render sections that have real content
	var blocks []string

	// --- Sessions (always shown, clickable) ---
	blocks = append(blocks, m.renderSidebarSessions(contentW))

	// --- Active tasks (only when something is running) ---
	if m.bgTaskCount > 0 || m.agentCount > 0 {
		blocks = append(blocks, m.renderSidebarActive())
	}

	content := strings.Join(blocks, "\n\n")

	return m.styles.SidebarBg.
		Width(sw).
		Height(h).
		Render(content)
}

func (m *cliModel) renderSidebarSessions(w int) string {
	// Reset tracking
	sidebarSessionLines = nil
	sidebarNewSessionY = -1

	entries := m.sidebarSessionEntries()
	currentIdx := m.sidebarCurrentIdx()

	var b strings.Builder
	b.WriteString(m.styles.SidebarHeader.Render("Sessions"))
	sidebarSessionLines = append(sidebarSessionLines, -1) // header line

	if len(entries) == 0 {
		b.WriteByte('\n')
		b.WriteString(m.styles.TextMutedSt.Render("  (empty)"))
		sidebarSessionLines = append(sidebarSessionLines, -1)
	} else {
		for i, s := range entries {
			b.WriteByte('\n')
			label := s.Label
			if label == "" {
				label = s.ID
			}
			maxLen := w - 4
			if len(label) > maxLen {
				label = label[:maxLen-1] + "…"
			}
			if i == currentIdx {
				b.WriteString(m.styles.SidebarActive.Render(" ● " + label))
			} else {
				b.WriteString(m.styles.SidebarItem.Render(" ○ " + label))
			}
			sidebarSessionLines = append(sidebarSessionLines, i) // session index
		}
	}

	// "+ New" button
	b.WriteByte('\n')
	b.WriteByte('\n')
	sidebarNewSessionY = len(sidebarSessionLines) + 1 // blank line + new button line
	b.WriteString(m.styles.Accent.Bold(true).Render("  + New"))

	return b.String()
}

// sidebarSessionEntries returns the full session entries (not just names).
func (m *cliModel) sidebarSessionEntries() []SessionPanelEntry {
	if m.sessionsListFn == nil {
		return nil
	}
	return m.sessionsListFn()
}

func (m *cliModel) renderSidebarActive() string {
	var b strings.Builder
	b.WriteString(m.styles.SidebarHeader.Render("Active"))
	b.WriteByte('\n')
	if m.bgTaskCount > 0 {
		b.WriteString(m.styles.SidebarItem.Render(fmt.Sprintf(" ● bg tasks: %d", m.bgTaskCount)))
	}
	if m.agentCount > 0 {
		if m.bgTaskCount > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(m.styles.SidebarItem.Render(fmt.Sprintf(" ● agents: %d", m.agentCount)))
	}
	return b.String()
}

// sidebarSessionList returns session names for sidebar display.
// sidebarCurrentIdx returns the index of the currently active session.
func (m *cliModel) sidebarCurrentIdx() int {
	if m.sessionsListFn == nil {
		return -1
	}
	entries := m.sessionsListFn()
	for i, e := range entries {
		if e.Active {
			return i
		}
	}
	return -1
}

// augmentTitleBar prepends titleBarLeft widgets and appends titleBarRight widgets.
func (m *cliModel) augmentTitleBar(titleBar string) string {
	left, right := m.resolveWidgetZone("titleBarLeft"), m.resolveWidgetZone("titleBarRight")
	if left == "" && right == "" {
		return titleBar
	}
	if left != "" {
		titleBar = left + " " + titleBar
	}
	if right != "" {
		titleBar = titleBar + " " + right
	}
	return titleBar
}

// augmentStatusBar prepends statusBarLeft and appends statusBarRight widgets.
func (m *cliModel) augmentStatusBar(statusBar string) string {
	left, right := m.resolveWidgetZone("statusBarLeft"), m.resolveWidgetZone("statusBarRight")
	if left == "" && right == "" {
		return statusBar
	}
	if left != "" {
		statusBar = left + "  " + statusBar
	}
	if right != "" {
		statusBar = statusBar + "  " + right
	}
	return statusBar
}

// augmentFooter appends footer widget content below the shortcut-hint bar.
func (m *cliModel) augmentFooter(footer string) string {
	content := m.resolveWidgetZone("footer")
	if content == "" {
		return footer
	}
	widgetLine := m.styles.TextMutedSt.Render(content)
	if footer == "" {
		return widgetLine
	}
	return footer + "  " + widgetLine
}

// augmentInfoBar appends infoBar widget content to the base info bar.
// Widget content is appended left-aligned after the bg task info (if present).
// The widget's own styling (from buildWidgetRenderFn) is preserved as-is.
func (m *cliModel) augmentInfoBar(infoBar string) string {
	content := m.resolveWidgetZone("infoBar")
	if content == "" {
		return infoBar
	}
	if infoBar == "" {
		return content
	}
	return infoBar + "  " + content
}

// resolveWidgetZone returns widget content for a zone, checking local WidgetRegistry
// first (using on-the-fly rendering to avoid stale slot cache), then falling back
// to remote plugin cache in remote mode.
func (m *cliModel) resolveWidgetZone(zone string) string {
	if m.widgetRegistry != nil {
		// Use RenderZoneForContext which calls provider.Render() directly
		// instead of reading from the global slot cache. The slot cache is
		// only written by RefreshWidget/RefreshAllWidgets and may be stale
		// after script plugin updates that use NotifyUpdated instead.
		return m.widgetRegistry.RenderZoneForContext(zone)
	}
	if m.remotePluginCache != nil {
		v := m.remotePluginCache.WidgetZone(zone)
		return v
	}
	return ""
}

// View renders the CLI interface.
func (m *cliModel) View() tea.View {
	// Reset mouse zones for this frame
	m.mouseZones.reset()

	// Splash screen
	if !m.splashDone {
		v := tea.NewView(m.renderSplash())
		v.AltScreen = true
		return v
	}
	if !m.ready {
		v := tea.NewView("\n  " + m.locale.SplashLoading)
		v.AltScreen = true
		return v
	}

	// Easter egg overlay
	if m.easterEgg != easterEggNone {
		v := tea.NewView(m.renderEasterEggOverlay())
		v.AltScreen = true
		return v
	}

	// /su loading
	if m.suLoading {
		v := tea.NewView(m.renderSuLoading())
		v.AltScreen = true
		return v
	}

	// Build shared components
	titleBar := m.renderTitleBar()
	borderColor, completionsHint := m.renderCompletionsHint(m.textarea.Value())
	input := m.renderInputArea(borderColor)

	// Layout selection + zone tracking
	var content string
	switch {
	case m.searchMode:
		content = m.layoutSearch(titleBar, input)
		m.trackMainLayoutZones(&m.mouseZones)
	case m.panelMode == "askuser":
		content = m.layoutAskUser(titleBar)
		m.trackAskUserZones(&m.mouseZones)
	case m.panelMode != "":
		content = m.layoutPanel(titleBar)
		m.trackPanelZones(&m.mouseZones)
	default:
		content = m.layoutMain(titleBar, input, completionsHint)
		m.trackMainLayoutZones(&m.mouseZones)
	}

	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion

	// Command palette overlay (highest priority — hides everything)
	if m.paletteOpen {
		if overlay := m.viewCommandPalette(m.width, m.height); overlay != "" {
			v.Content = overlay
		}
		// Re-track zones for overlay
		m.mouseZones.reset()
		m.trackOverlayZones(&m.mouseZones)
		return v
	}

	// Quick switch overlay
	if m.quickSwitchMode != "" {
		if overlay := m.viewQuickSwitch(m.width, m.height); overlay != "" {
			v.Content = overlay
		}
		m.mouseZones.reset()
		m.trackOverlayZones(&m.mouseZones)
	}

	// Rewind overlay
	if m.rewindMode {
		if overlay := m.viewRewindPanel(m.width, m.height); overlay != "" {
			v.Content = overlay
		}
		m.mouseZones.reset()
		m.trackOverlayZones(&m.mouseZones)
	}

	return v
}

// allTodosDone returns true when todos exist and every item is marked done.
func (m *cliModel) allTodosDone() bool {
	if len(m.todos) == 0 {
		return false
	}
	for _, t := range m.todos {
		if !t.Done {
			return false
		}
	}
	return true
}

// renderInfoBar renders a sleek bottom status line below the input box
// showing background tasks, active subagents, and pending queue —
// inspired by lazygit's and Warp's status panels.
// Only renders when there are active items (no "empty" state noise).
func (m *cliModel) renderInfoBar() string {
	hasTasks := m.bgTaskCount > 0
	hasAgents := m.agentCount > 0
	hasQueue := len(m.messageQueue) > 0

	if !hasTasks && !hasAgents && !hasQueue {
		return ""
	}

	var parts []string

	if hasTasks {
		icon := m.styles.WarningSt.Render("⚡")
		count := m.styles.WarningSt.Render(fmt.Sprintf("%d", m.bgTaskCount))
		label := m.styles.Accent.Bold(true).Render(m.locale.InfoBarTasks)
		parts = append(parts, fmt.Sprintf("%s%s %s", icon, count, label))
	}
	if hasAgents {
		icon := m.styles.WarningSt.Render("🧠")
		count := m.styles.WarningSt.Render(fmt.Sprintf("%d", m.agentCount))
		label := m.styles.Accent.Bold(true).Render(m.locale.InfoBarAgents)
		parts = append(parts, fmt.Sprintf("%s%s %s", icon, count, label))
	}
	if hasQueue {
		icon := m.styles.InfoSt.Render("📬")
		count := m.styles.InfoSt.Render(fmt.Sprintf("%d", len(m.messageQueue)))
		parts = append(parts, fmt.Sprintf("%s%s", icon, count))
	}

	// Join sections with muted separators
	separator := m.styles.TextMutedSt.Render(" · ")
	content := strings.Join(parts, separator)

	// Left padding of 2 (matching InputBox visual)
	return lipgloss.NewStyle().
		PaddingLeft(2).
		Render(content)
}

// renderTodoBar renders a compact TODO progress bar between status and input.
// Returns empty string when no todos are active.
func (m *cliModel) renderTodoBar() string {
	if len(m.todos) == 0 {
		return ""
	}

	done := 0
	total := len(m.todos)
	for _, item := range m.todos {
		if item.Done {
			done++
		}
	}

	// All done — still show bar (cleared on next user message)
	// if done == total { return "" }

	// Progress bar: filled portion
	barWidth := 20
	filled := 0
	if total > 0 {
		filled = done * barWidth / total
	}

	barFilled := strings.Repeat("█", filled)
	barEmpty := strings.Repeat("░", barWidth-filled)

	// §20
	s := &m.styles
	todoLabelSt := s.TodoLabel
	todoBarFilledSt := s.TodoFilled
	todoBarEmptySt := s.TodoEmpty
	todoDoneSt := s.TodoDone
	todoPendingSt := s.TodoPending

	var sb strings.Builder
	// Header: TODO label + count + progress bar
	sb.WriteString(todoLabelSt.Render(" TODO "))
	fmt.Fprintf(&sb, "%d/%d ", done, total)
	sb.WriteString(todoBarFilledSt.Render(barFilled))
	sb.WriteString(todoBarEmptySt.Render(barEmpty))
	sb.WriteString("\n")
	// Items
	for i, item := range m.todos {
		text := item.Text
		if utf8.RuneCountInString(text) > 60 {
			text = string([]rune(text)[:59]) + "…"
		}
		if item.Done {
			sb.WriteString("  ")
			sb.WriteString(todoDoneSt.Render("✓"))
			sb.WriteString(" ")
			sb.WriteString(todoPendingSt.Render(text))
		} else {
			sb.WriteString("  ")
			sb.WriteString(todoLabelSt.Render("○"))
			sb.WriteString(" ")
			sb.WriteString(todoPendingSt.Render(text))
		}
		if i < len(m.todos)-1 {
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// titleText 生成标题栏文字。
func (m *cliModel) titleText() string {
	modeLabel := "xbot"
	if m.remoteMode {
		host := m.remoteServerURL
		// Strip scheme for display: "ws://host:port" → "host:port"
		if u, err := url.Parse(host); err == nil && u.Host != "" {
			host = u.Host
		}
		// Connection state via plain Unicode symbol (no ANSI — colors break titleBar background)
		var cloud string
		switch m.connState {
		case "connected":
			cloud = IconCloudOn
		case "disconnected":
			cloud = IconCloudOff
		case "reconnecting":
			cloud = IconCloudWait
		default:
			cloud = IconCloudOn
		}
		if host != "" {
			modeLabel = fmt.Sprintf("%s xbot %s", cloud, host)
		} else {
			modeLabel = fmt.Sprintf("%s xbot remote", cloud)
		}
	}
	prefix := IconDiamond + " "
	if m.workDir != "" {
		abs, err := filepath.Abs(m.workDir)
		if err == nil {
			return prefix + fmt.Sprintf("%s [%s]", modeLabel, filepath.Base(abs))
		}
		return prefix + fmt.Sprintf("%s [%s]", modeLabel, filepath.Base(m.workDir))
	}
	return prefix + modeLabel
}

// ---------------------------------------------------------------------------
// §14 Dynamic title bar hints
// ---------------------------------------------------------------------------

// askUserTitleHints returns the minimal control hints for the askuser panel,
// displayed in the header bar so they're always visible regardless of scroll.
// Keep it short — header width is limited and line wrap looks terrible.
func (m *cliModel) askUserTitleHints() string {
	hints := []string{"↑↓ select", "Space check", "Enter submit", "Esc cancel"}
	if len(m.panelItems) > 1 {
		hints = append([]string{"←→ switch"}, hints...)
	}
	return strings.Join(hints, " · ")
}

// ---------------------------------------------------------------------------
// §14 启动画面 (Splash Screen)
// ---------------------------------------------------------------------------

// xbotLogo — "XBOT" ASCII art（slant 字体，figlet 生成）
var xbotLogo = []string{
	"   _  __    ____    ____    ______",
	"  | |/ /   / __ )  / __ \\  /_  __/",
	"  |   /   / __  | / / / /   / /",
	" /   |   / /_/ / / /_/ /   / /",
	"/_/|_|  /_____/  \\____/   /_/",
}

// renderSplash 渲染启动画面 — 品牌 logo + 版本号 + 加载动画
func (m *cliModel) renderSplash() string {
	// 中心化计算
	screenW := m.width
	if screenW < 40 {
		screenW = 40
	}
	screenH := m.height
	if screenH < 10 {
		screenH = 10
	}

	// §20 使用缓存样式
	versionStyle := m.styles.VersionSt
	descStyle := m.styles.TextMutedSt
	loadingStyle := m.styles.WarningSt

	// 组装 splash 内容 — ASCII logo 逐行渐变（Accent → Gradient）
	var lines []string
	maxLogoW := 0
	renderedLogo := make([]string, len(xbotLogo))
	fromR, fromG, fromB := hexToRGB(currentTheme.Accent)
	toR, toG, toB := hexToRGB(currentTheme.Gradient)
	n := len(xbotLogo)
	for i, line := range xbotLogo {
		t := float64(i) / float64(max(n-1, 1))
		r := uint8(float64(fromR) + (float64(toR)-float64(fromR))*t)
		g := uint8(float64(fromG) + (float64(toG)-float64(fromG))*t)
		b := uint8(float64(fromB) + (float64(toB)-float64(fromB))*t)
		lineColor := lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", r, g, b))
		renderedLogo[i] = lipgloss.NewStyle().Foreground(lineColor).Bold(true).Render(line)
		if w := lipgloss.Width(renderedLogo[i]); w > maxLogoW {
			maxLogoW = w
		}
	}
	logoPad := (screenW - maxLogoW) / 2
	if logoPad < 0 {
		logoPad = 0
	}
	for _, line := range renderedLogo {
		lines = append(lines, strings.Repeat(" ", logoPad)+line)
	}

	// 空行
	lines = append(lines, "")

	// 版本号居中
	versionText := versionStyle.Render(fmt.Sprintf("xbot %s · %s", version.Version, version.Commit))
	vW := lipgloss.Width(versionText)
	vPad := (screenW - vW) / 2
	if vPad < 0 {
		vPad = 0
	}
	lines = append(lines, strings.Repeat(" ", vPad)+versionText)

	// 描述居中（节日版彩蛋）
	splashDesc := m.locale.SplashDesc
	if holidayDesc := getHolidaySplashDesc(); holidayDesc != "" {
		splashDesc = holidayDesc
	}
	descText := descStyle.Render(splashDesc)
	dW := lipgloss.Width(descText)
	dPad := (screenW - dW) / 2
	if dPad < 0 {
		dPad = 0
	}
	lines = append(lines, strings.Repeat(" ", dPad)+descText)

	// 空行
	lines = append(lines, "")

	// 加载动画
	frame := splashFrames[m.splashFrame%len(splashFrames)]
	loadingText := loadingStyle.Render(fmt.Sprintf(m.locale.SplashLoading, frame))
	lW := lipgloss.Width(loadingText)
	lPad := (screenW - lW) / 2
	if lPad < 0 {
		lPad = 0
	}
	lines = append(lines, strings.Repeat(" ", lPad)+loadingText)

	// 垂直居中
	emptyLinesBefore := (screenH - len(lines)) / 2
	if emptyLinesBefore < 2 {
		emptyLinesBefore = 2
	}

	var sb strings.Builder
	for i := 0; i < emptyLinesBefore; i++ {
		sb.WriteString("\n")
	}
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	return sb.String()
}

// renderSuLoading 渲染 /su 切换用户后的历史加载画面（复用 splash 动画帧）
func (m *cliModel) renderSuLoading() string {
	screenW := m.width
	if screenW < 40 {
		screenW = 40
	}
	screenH := m.height
	if screenH < 10 {
		screenH = 10
	}

	loadingStyle := m.styles.WarningSt
	descStyle := m.styles.TextMutedSt

	// 居中内容
	var lines []string
	frame := splashFrames[m.splashFrame%len(splashFrames)]

	// 切换目标提示
	suText := descStyle.Render(fmt.Sprintf(m.locale.SuSwitching, m.senderID))
	suW := lipgloss.Width(suText)
	suPad := (screenW - suW) / 2
	if suPad < 0 {
		suPad = 0
	}
	lines = append(lines, strings.Repeat(" ", suPad)+suText)

	// 空行
	lines = append(lines, "")

	// 加载动画
	loadingText := loadingStyle.Render(fmt.Sprintf(m.locale.SuLoadingHistory, frame))
	lW := lipgloss.Width(loadingText)
	lPad := (screenW - lW) / 2
	if lPad < 0 {
		lPad = 0
	}
	lines = append(lines, strings.Repeat(" ", lPad)+loadingText)

	// 垂直居中
	emptyLinesBefore := (screenH - len(lines)) / 2
	if emptyLinesBefore < 3 {
		emptyLinesBefore = 3
	}

	var sb strings.Builder
	for i := 0; i < emptyLinesBefore; i++ {
		sb.WriteString("\n")
	}
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	return sb.String()
}

// ---------------------------------------------------------------------------
// §15 底部快捷键提示条 (Footer Bar)
// ---------------------------------------------------------------------------

// footerHint represents a clickable hint in the footer bar.
type footerHint struct {
	xStart int    // rendered X start position (0-based)
	xEnd   int    // rendered X end position (exclusive)
	action string // action to trigger on click
	key    string // display key (e.g. "Ctrl+k")
	desc   string // display description (e.g. "命令面板")
}

// footerHints stores the current frame's footer hint positions for mouse click handling.
// Populated during renderFooter(), consumed during trackMainLayoutZones().
var footerHints []footerHint

// sidebarSessionLines tracks Y-offsets of each session item row in the sidebar.
// Populated during renderSidebarSessions, consumed by trackMainLayoutZones.
// -1 means "no item" (header, blank line, etc).
var sidebarSessionLines []int

// sidebarNewSessionY tracks the Y-offset of the "+ New" button in the sidebar.
// -1 means not rendered.
var sidebarNewSessionY int

// renderFooter 渲染底部快捷键提示条。
// 根据当前状态动态显示最相关的快捷键，避免信息过载。
func (m *cliModel) renderFooter() string {
	var hints []footerHint

	if m.panelMode != "" {
		// 面板打开时：显示面板相关快捷键
		escLabel := m.locale.FooterClose
		if len(m.panelStack) > 0 {
			escLabel = m.locale.FooterBack
		}
		switch m.panelMode {
		case "bgtasks":
			if m.panelBgViewing {
				hints = append(hints,
					m.footerHintItem("PgUp/PgDn", m.locale.FooterScroll, "scroll"),
					m.footerHintItem("Esc", m.locale.FooterBack, "esc"),
				)
			} else {
				hints = append(hints,
					m.footerHintItem("↑↓", m.locale.FooterNavigate, "navigate"),
					m.footerHintItem("Enter", m.locale.FooterLog, "enter"),
					m.footerHintItem("Del", m.locale.FooterKill, "delete"),
					m.footerHintItem("Esc", m.locale.FooterClose, "esc"),
				)
			}
		case "approval":
			hints = append(hints,
				m.footerHintItem("←→", m.locale.FooterNavigate, "navigate"),
				m.footerHintItem("y/n", "Quick", "quick"),
				m.footerHintItem("Enter", m.locale.FooterSelect, "enter"),
				m.footerHintItem("Esc", "Deny", "esc"),
			)
		case "settings":
			hints = append(hints,
				m.footerHintItem("↑↓", m.locale.FooterNavigate, "navigate"),
				m.footerHintItem("Ctrl+s", "Save", "ctrl+s"),
				m.footerHintItem("Esc", escLabel, "esc"),
			)
		case "askuser":
			hints = append(hints,
				m.footerHintItem("↑↓", m.locale.FooterNavigate, "navigate"),
				m.footerHintItem("Space", "Check", "space"),
				m.footerHintItem("Enter", m.locale.FooterSelect, "enter"),
				m.footerHintItem("Esc", m.locale.FooterClose, "esc"),
			)
		case "danger":
			hints = append(hints,
				m.footerHintItem("↑↓", m.locale.FooterNavigate, "navigate"),
				m.footerHintItem("Enter", "Confirm", "enter"),
				m.footerHintItem("Esc", escLabel, "esc"),
			)
		case "runner":
			hints = append(hints,
				m.footerHintItem("↑↓", "Field", "navigate"),
				m.footerHintItem("Enter", "Connect", "enter"),
				m.footerHintItem("Esc", escLabel, "esc"),
			)
		default:
			hints = append(hints,
				m.footerHintItem("↑↓", m.locale.FooterNavigate, "navigate"),
				m.footerHintItem("Enter", m.locale.FooterSelect, "enter"),
				m.footerHintItem("Esc", escLabel, "esc"),
			)
		}
	} else if m.typing {
		hints = append(hints, m.footerHintItem("Ctrl+c", m.locale.FooterCancel, "ctrl+c"))
	} else {
		if m.textarea.Value() == "" {
			hints = append(hints, m.footerHintItem("Ctrl+k", m.locale.FooterPalette, "ctrl+k"))
			if !m.isNarrow() {
				hints = append(hints, m.footerHintItem("tab", m.locale.FooterComplete, "tab"))
			}
			if !m.isCompact() {
				hints = append(hints, m.footerHintItem("Ctrl+e", m.locale.FooterFold, "ctrl+e"))
			}
			if m.subscriptionMgr != nil && !m.isNarrow() {
				hints = append(hints, m.footerHintItem("Ctrl+p", "Subs", "ctrl+p"))
			}
			if !m.isNarrow() {
				hints = append(hints, m.footerHintItem("Ctrl+t", "Sessions", "ctrl+t"))
			}
			if m.bgTaskCount > 0 && !m.isCompact() {
				hints = append(hints, m.footerHintItem("^", m.locale.FooterBgTasks, "^"))
			}
		} else {
			hints = append(hints, m.footerHintItem("Ctrl+j", m.locale.FooterNewline, "ctrl+j"))
			if !m.isNarrow() {
				hints = append(hints, m.footerHintItem("tab", m.locale.FooterComplete, "tab"))
			}
			hints = append(hints, m.footerHintItem("Ctrl+k", m.locale.FooterPalette, "ctrl+k"))
		}
	}

	if len(hints) == 0 {
		footerHints = nil
		return ""
	}

	helpHint := m.styles.TextMutedSt.Render("/help")
	ellipsis := m.styles.TextMutedSt.Render("…")
	ellipsisW := lipgloss.Width(ellipsis)

	// Progressively drop hints from the end until the footer fits.
	for len(hints) > 0 {
		footerText, xPositions := m.renderHintsText(hints)
		footerText = padBetween(footerText, helpHint, m.chatWidth())
		if lipgloss.Width(footerText) <= m.chatWidth() {
			// Store X positions for mouse zone tracking
			for i := range hints {
				if i < len(xPositions) {
					hints[i].xStart = xPositions[i]
					hints[i].xEnd = xPositions[i+1]
				}
			}
			footerHints = hints
			return m.styles.Footer.Width(m.chatWidth()).Render(footerText)
		}
		hints = hints[:len(hints)-1]
	}

	footerHints = nil
	return m.styles.Footer.Width(m.chatWidth()).Render(
		padBetween(ellipsis, helpHint, max(ellipsisW+lipgloss.Width(helpHint)+1, m.chatWidth())))
}

// footerHintItem creates a footerHint with display text and action.
func (m *cliModel) footerHintItem(key, desc, action string) footerHint {
	return footerHint{key: key, desc: desc, action: action}
}

// renderHintsText renders all hints into a single string and tracks X positions.
func (m *cliModel) renderHintsText(hints []footerHint) (string, []int) {
	var sb strings.Builder
	positions := make([]int, 0, len(hints)+1)
	positions = append(positions, 0) // start at X=0

	for i, h := range hints {
		rendered := m.styles.FooterHintLabel.Render(h.key) + " " + m.styles.KeyDescSt.Render(h.desc)
		if i > 0 {
			sb.WriteString("  ")
		}
		startX := lipgloss.Width(sb.String())
		positions = append(positions, startX+lipgloss.Width(rendered))
		sb.WriteString(rendered)
	}

	return sb.String(), positions
}

// padBetween 在左右文本之间填充空格，使总宽度达到 width
func padBetween(left, right string, width int) string {
	w := lipgloss.Width(left) + lipgloss.Width(right)
	if w >= width {
		return left + " " + right
	}
	return left + strings.Repeat(" ", width-w) + right
}

// renderProgressStatus renders a compact one-line status for the status bar.
func (m *cliModel) renderProgressStatus(progressStyle, toolStyle lipgloss.Style) string {
	s := &m.styles // §20
	var sb strings.Builder
	sb.WriteString(s.Progress.Render(m.ticker.view()))
	sb.WriteString(" ")

	if m.progress != nil {
		fmt.Fprintf(&sb, "#%d", m.progress.Iteration)

		// Phase hint
		switch m.progress.Phase {
		case "thinking":
			sb.WriteString(" · " + m.pickVerb(m.ticker.ticks))
		case "compressing":
			sb.WriteString(" · " + m.locale.StatusCompressing)
		case "retrying":
			sb.WriteString(" · " + m.locale.StatusRetrying)
		default:
			if len(m.progress.CompletedTools) > 0 {
				sb.WriteString(" · " + m.locale.StatusDone)
			}
		}
	} else {
		sb.WriteString(m.pickVerb(m.ticker.ticks) + "...")
	}

	// Total elapsed
	if !m.typingStartTime.IsZero() {
		elapsed := time.Since(m.typingStartTime).Milliseconds()
		sb.WriteString(" · ")
		sb.WriteString(formatElapsed(elapsed))
	}

	return sb.String()
}

// ctxBarStyles holds theme-derived styles for the context usage progress bar.
// Rebuilt on each renderContextTopBorder call so theme switches take effect immediately.
type ctxBarStyles struct {
	fillGreen  lipgloss.Style
	fillYellow lipgloss.Style
	fillRed    lipgloss.Style
	dim        lipgloss.Style
	empty      lipgloss.Style
	threshold  lipgloss.Style
}

func newCtxBarStyles() ctxBarStyles {
	c := func(s string) color.Color { return lipgloss.Color(s) }
	t := currentTheme
	return ctxBarStyles{
		fillGreen:  lipgloss.NewStyle().Foreground(c(t.Success)),
		fillYellow: lipgloss.NewStyle().Foreground(c(t.Warning)),
		fillRed:    lipgloss.NewStyle().Foreground(c(t.Error)),
		dim:        lipgloss.NewStyle().Foreground(c(t.FGMostSubtle)).Faint(true),
		empty:      lipgloss.NewStyle().Foreground(c(t.BarEmpty)),
		threshold:  lipgloss.NewStyle().Foreground(c(t.Error)).Bold(true),
	}
}

// renderContextTopBorder replaces the input box top border with a context
// usage progress bar. The border corners (╭╮) stay in the original border color,
// while the inner line becomes a segmented progress bar using thin line characters:
//
//	─ filled (color-coded) · ─ free (dim) · ┊ threshold (red bold) · ╌ output reservation (dashed dim)
//
// Returns empty string when no token data is available (keep original border).
func (m *cliModel) renderContextTopBorder(borderColor color.Color, renderedBox string) string {
	if m.lastTokenUsage == nil || m.cachedMaxContextTokens <= 0 {
		return ""
	}
	promptTokens := m.lastTokenUsage.PromptTokens
	maxTokens := int64(m.cachedMaxContextTokens)
	if promptTokens <= 0 || maxTokens <= 0 {
		return ""
	}

	firstLine, _, found := strings.Cut(renderedBox, "\n")
	if !found {
		return ""
	}
	totalW := lipgloss.Width(firstLine)
	innerW := totalW - 2 // minus ╭ and ╮
	if innerW < 6 {
		return "" // too narrow, keep default
	}

	pct := float64(promptTokens) / float64(maxTokens)
	if pct > 1 {
		pct = 1
	}

	maxOutputTokens := m.cachedMaxOutputTokens
	if maxOutputTokens <= 0 {
		maxOutputTokens = 8192
	}
	promptBudget := maxTokens - maxOutputTokens
	if promptBudget <= 0 {
		promptBudget = maxTokens / 2
	}

	compressRatio := m.cachedCompressRatio
	if compressRatio <= 0 {
		compressRatio = 0.9
	}
	compressThreshold := int64(float64(promptBudget) * compressRatio)

	// Cell counts
	filledCells := int(float64(innerW) * float64(promptTokens) / float64(maxTokens))
	if filledCells > innerW {
		filledCells = innerW
	}

	outputCells := int(float64(innerW) * float64(maxOutputTokens) / float64(maxTokens))
	if outputCells < 1 {
		outputCells = 1
	}
	if outputCells > innerW-1 {
		outputCells = innerW - 1
	}

	compressPos := int(float64(innerW) * float64(compressThreshold) / float64(maxTokens))
	if compressPos < 1 {
		compressPos = 1
	}
	if compressPos >= innerW {
		compressPos = innerW - 1
	}

	// Color selection
	bs := newCtxBarStyles()
	var fillSty lipgloss.Style
	switch {
	case pct > 0.8:
		fillSty = bs.fillRed
	case pct > 0.5:
		fillSty = bs.fillYellow
	default:
		fillSty = bs.fillGreen
	}

	cornerSty := lipgloss.NewStyle().Foreground(borderColor)

	// Build top border
	var sb strings.Builder
	sb.WriteString(cornerSty.Render("╭"))

	outputStart := innerW - outputCells
	if outputStart < filledCells {
		outputStart = filledCells
	}

	// 1. Filled segment — thin line matching border style
	if filledCells > 0 {
		sb.WriteString(fillSty.Render(strings.Repeat("─", filledCells)))
	}

	// 2. Empty segment (may contain threshold marker)
	emptyStart := filledCells
	emptyEnd := outputStart
	if emptyEnd > emptyStart {
		if compressPos >= emptyStart && compressPos < emptyEnd {
			before := compressPos - emptyStart
			after := emptyEnd - compressPos - 1
			if before > 0 {
				sb.WriteString(bs.empty.Render(strings.Repeat("─", before)))
			}
			sb.WriteString(bs.threshold.Render("┊"))
			if after > 0 {
				sb.WriteString(bs.empty.Render(strings.Repeat("─", after)))
			}
		} else {
			sb.WriteString(bs.empty.Render(strings.Repeat("─", emptyEnd-emptyStart)))
		}
	}

	// 3. Output reservation — dashed thin line
	if innerW-outputStart > 0 {
		sb.WriteString(bs.dim.Render(strings.Repeat("╌", innerW-outputStart)))
	}

	sb.WriteString(cornerSty.Render("╮"))
	return sb.String()
}

// ---------------------------------------------------------------------------

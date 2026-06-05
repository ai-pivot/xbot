package channel

import (
	"fmt"
	"image/color"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
	"xbot/config"
	"xbot/version"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"xbot/clipanic"
)

// computeInputCursorScreenPos calculates the absolute screen (X, Y) of the
// textarea cursor for IME candidate window positioning.
// Only valid when the main layout is active (no panels/palettes).
//
// It uses textarea.Cursor() for CJK-correct internal coordinates (CharOffset
// uses uniseg.StringWidth), then adds the screen layout offsets.
func (m *cliModel) computeInputCursorScreenPos() (x, y int, ok bool) {
	// Only compute for main layout where textarea is visible
	if m.panelMode != "" || m.paletteOpen || m.quickSwitchMode != "" || m.rewindMode || m.searchMode {
		return 0, 0, false
	}
	// Textarea is not rendered when settings are being saved
	if m.settingsSaving {
		return 0, 0, false
	}

	// textarea.Cursor() returns nil when virtual cursor is enabled.
	// We call SetVirtualCursor(false) elsewhere so this returns a real cursor.
	taCur := m.textarea.Cursor()
	if taCur == nil {
		return 0, 0, false
	}

	// --- Compute Y (screen row) ---
	// Layout: titleBar(1) + viewport(vpH) + status(1) + [todoBar(N)] + [footer(0/1)] + inputBox
	y = 1                         // titleBar
	y += m.layoutViewportHeight() // viewport
	y++                           // status bar

	// todo bar (only when sidebar is NOT visible)
	if !m.sidebarShown() {
		if todoBar := m.renderTodoBar(); todoBar != "" {
			y += strings.Count(todoBar, "\n") + 1
		}
	}
	// footer
	if footer := m.augmentFooter(m.renderFooter()); footer != "" {
		y++
	}

	// InputBox: top border → textarea content
	// InputBox uses RoundedBorder + Padding(0,1).
	// The textarea's Cursor().Y is relative to textarea top-left (0-based).
	// InputBox top border adds 1 line before textarea content.
	inputBox := m.styles.InputBox
	y += inputBox.GetBorderTopSize() + inputBox.GetPaddingTop()
	y += taCur.Y

	// --- Compute X (screen column) ---
	// textarea.Cursor().X already includes: CharOffset + prompt + lineNumber + base border/padding.
	// We need to add: xShift (sidebar) + InputBox border-left + InputBox padding-left.
	x = m.xShift
	x += inputBox.GetBorderLeftSize() + inputBox.GetPaddingLeft()
	x += taCur.X

	return x, y, true
}

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

// sidebarShown returns true when the sidebar is currently rendered on screen.
func (m *cliModel) sidebarShown() bool { return m.isWide() && m.sidebarEnabled && m.sidebarVisible }

// invalidateLayoutCache resets cached sidebar/chat width metrics.
// Must be called on resize, sidebar toggle, sidebarWidth change, or theme change.
func (m *cliModel) invalidateLayoutCache() {
	m.cachedSidebarRenderedWidth = 0
	m.cachedSidebarWidthKey = 0
	m.cachedChatWidth = 0
	m.cachedChatWidthKey = 0
}

// sidebarRenderedWidth returns the actual visual width of the sidebar after rendering.
// This depends on character widths (e.g. RUNEWIDTH_EASTASIAN makes │ width=2),
// so we measure it dynamically — but cache the result until layout changes.
func (m *cliModel) sidebarRenderedWidth() int {
	sw := m.sidebarWidth
	if sw < 12 {
		sw = 12
	}
	if m.cachedSidebarRenderedWidth > 0 && m.cachedSidebarWidthKey == sw {
		return m.cachedSidebarRenderedWidth
	}
	rendered := m.styles.SidebarBg.Width(sw).Height(1).Render("")
	line := strings.Split(rendered, "\n")[0]
	w := lipgloss.Width(line)
	m.cachedSidebarRenderedWidth = w
	m.cachedSidebarWidthKey = sw
	return w
}

// chatWidth returns the effective width for the chat viewport, accounting for sidebar.
// Result is cached until invalidateLayoutCache() is called.
func (m *cliModel) chatWidth() int {
	if m.cachedChatWidth > 0 && m.cachedChatWidthKey == m.width {
		return m.cachedChatWidth
	}
	var w int
	if m.sidebarShown() {
		w = m.width - m.sidebarRenderedWidth()
		if w < 20 {
			w = 20
		}
	} else {
		w = m.width
	}
	m.cachedChatWidth = w
	m.cachedChatWidthKey = m.width
	return w
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

	// Shift-select hint: shown when user clicks/drags without Shift (likely trying to select text)
	if !m.shiftHintUntil.IsZero() && time.Now().Before(m.shiftHintUntil) && m.locale.ShiftSelectHint != "" {
		titleRight = m.locale.ShiftSelectHint + " · " + titleRight
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
	// Show saving overlay instead of textarea while settings are being saved
	if m.settingsSaving {
		w := m.chatWidth()
		inputBoxStyle := m.styles.InputBox.BorderForeground(borderColor).Width(w - 4)
		return inputBoxStyle.Render(lipgloss.NewStyle().Faint(true).Render("  ⏳ Saving settings..."))
	}
	// Use chatWidth so input box fits when sidebar is open
	w := m.chatWidth()
	inputBoxStyle := m.styles.InputBox.BorderForeground(borderColor).Width(w - 4)
	inputArea := m.textarea.View()

	// Render placeholder manually when textarea is empty.
	// No cursor block is rendered here — BubbleTea's real terminal cursor
	// is positioned at this location via v.Cursor in View().
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
			phRendered := m.styles.PlaceholderSt.Render(string(phRunes))
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
	m.modelNameZoneXStart = -1
	m.modelNameZoneXEnd = -1
	if m.cachedModelName != "" {
		modelHint := m.cachedModelName
		// Track X position of the model name part for click detection.
		// The model name is: prefixBeforeModel + modelHint
		// where prefixBeforeModel = join(readyParts without modelHint) + " · "
		modelHintIdx := len(readyParts) // index where model hint will be appended
		prefixBeforeModel := ""
		if modelHintIdx > 0 {
			prefixBeforeModel = strings.Join(readyParts, " · ") + " · "
		}
		if m.modelCount > 1 && !m.isCompact() {
			modelHint += " [Ctrl+N]"
		}
		m.modelNameZoneXStart = lipgloss.Width(prefixBeforeModel)
		m.modelNameZoneXEnd = m.modelNameZoneXStart + lipgloss.Width(modelHint)
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
		// Use chatWidth (not m.width) so panel width respects sidebar
		panelW := m.chatWidth()
		contentWidth := panelW - 4 - 2 // PanelBox border(2) + padding(2) - scrollbar(2)
		if contentWidth < 10 {
			contentWidth = 10
		}
		askContent = m.applyScrollbar(askContent, contentWidth, totalAskLines, m.askPanelScrollY)
	}
	// Use chatWidth-aware PanelBox so the askuser panel fits alongside sidebar
	cw := m.chatWidth()
	boxedAsk := m.styles.PanelBox.Width(cw - 4).Render(askContent)
	// Scroll indicator — mouse wheel or ↑↓ at edges scrolls content
	scrollHint := ""
	if totalAskLines > askVisibleH {
		pct := (m.askPanelScrollY + askVisibleH) * 100 / totalAskLines
		scrollHint = m.styles.PanelDesc.Render(fmt.Sprintf(" [%d%%] ↕ scroll", pct))
	}

	// Middle block: viewport + askPanel + scrollHint (same structure as layoutMain
	// middle section, so sidebar can be joined horizontally)
	middleBlock := fmt.Sprintf("%s\n%s%s", m.viewport.View(), boxedAsk, scrollHint)

	// Sidebar support — askuser is a split layout (viewport + panel), sidebar
	// should remain visible just like in normal mode. Without this, opening the
	// askuser panel causes the sidebar to disappear unconditionally.
	showSidebar := m.sidebarShown()
	if showSidebar {
		availableH := m.height - 1 // minus titleBar
		sidebar := m.renderSidebarForBlock(middleBlock, availableH)
		if m.sidebarPosition == "right" {
			return titleBar + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, middleBlock, sidebar)
		}
		return titleBar + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, sidebar, middleBlock)
	}

	return fmt.Sprintf("%s\n%s", titleBar, middleBlock)
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
		status = thinkingStatusStyle.Render(m.renderProgressStatus())
	} else if m.checkingUpdate {
		status = m.styles.ThinkingSt.Render(m.locale.CheckingUpdates)
	} else if completionsHint != "" {
		status = completionsHint
	} else {
		status = m.renderReadyStatus()
	}

	// Accumulate status hints
	var hints []string
	var hintsBeforeNewContent string // accumulated string before newContentHint
	if m.tempStatus != "" {
		rendered := m.styles.WarningSt.Render(m.tempStatus)
		hints = append(hints, rendered)
		hintsBeforeNewContent = rendered
	}
	if m.newContentHint {
		rendered := m.styles.InfoSt.Render(m.locale.NewContentHint)
		m.newContentHintRendered = rendered
		hints = append(hints, rendered)
		// Calculate X position: status + "  " + hintsBeforeNewContent + "  "
		prefix := status
		if hintsBeforeNewContent != "" {
			prefix = appendStatusHint(status, hintsBeforeNewContent)
		}
		m.newContentHintXStart = lipgloss.Width(prefix)
	} else {
		m.newContentHintRendered = ""
		m.newContentHintXStart = 0
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
	showSidebar := m.sidebarShown()

	// Title bar is always full width
	var topLines []string
	topLines = append(topLines, titleBar)

	// Middle section: viewport + status + todo + footer + input + infoBar
	// When sidebar is visible, this whole section is squeezed to chatWidth
	// and the todo bar moves to the sidebar instead.
	var middleLines []string
	middleLines = append(middleLines, m.viewport.View())
	if status != "" {
		middleLines = append(middleLines, status)
	}
	if !showSidebar {
		todoBar := m.renderTodoBar()
		if todoBar != "" {
			middleLines = append(middleLines, todoBar)
		}
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
		sidebar := m.renderSidebarForBlock(middleBlock, m.height-len(topLines))
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
func (m *cliModel) renderSidebarForBlock(block string, availableH int) string {
	sw := m.sidebarWidth
	if sw < 12 {
		sw = 12
	}

	// Measure middle block height, capped to actual screen area available
	h := strings.Count(block, "\n") + 1
	if h > availableH {
		h = availableH
	}
	if h < 5 {
		h = 5
	}

	contentW := sw - m.styles.SidebarBg.GetHorizontalFrameSize() // Width(sw) includes border+padding; content = sw - frame

	// Reset section header tracking for click-to-collapse
	sidebarSectionHeaders = make(map[string]int)

	// Only render sections that have real content
	var blocks []string

	// --- Sessions (always shown, clickable) ---
	sidebarSectionHeaders["sessions"] = 0
	if m.sidebarCollapsedSections["sessions"] {
		// Must reset tracking vars even when collapsed, otherwise stale
		// zone data from the previous frame causes wrong click targets.
		sidebarSessionLines = nil
		sidebarDeleteXStart = nil
		sidebarDeleteXEnd = nil
		sidebarNewSessionY = -1
		m.sidebarHasBusySessions = false
		blocks = append(blocks, m.renderSidebarSectionHeader("Sessions", true))
	} else {
		blocks = append(blocks, m.renderSidebarSessions(contentW))
	}

	// --- Todo (when sidebar is visible, todo moves here from main view) ---
	if len(m.todos) > 0 {
		if m.sidebarCollapsedSections["todo"] {
			sidebarSectionHeaders["todo"] = nextBlockOffset(blocks)
			blocks = append(blocks, m.renderSidebarSectionHeader("Todo", true))
		} else {
			if st := m.renderSidebarTodo(contentW); st != "" {
				sidebarSectionHeaders["todo"] = nextBlockOffset(blocks)
				blocks = append(blocks, st)
			}
		}
	}

	// --- Active tasks (only when something is running) ---
	if m.bgTaskCount > 0 || m.agentCount > 0 {
		if m.sidebarCollapsedSections["tasks"] {
			sidebarSectionHeaders["tasks"] = nextBlockOffset(blocks)
			blocks = append(blocks, m.renderSidebarSectionHeader("Tasks", true))
			sidebarActiveSectionOffset = -1
			sidebarBgTaskLines = nil // clear stale zone data
		} else {
			sidebarActiveSectionOffset = nextBlockOffset(blocks)
			sidebarSectionHeaders["tasks"] = sidebarActiveSectionOffset
			blocks = append(blocks, m.renderSidebarActive(contentW))
		}
	} else {
		sidebarActiveSectionOffset = -1
	}

	content := strings.Join(blocks, "\n\n")

	return m.styles.SidebarBg.
		Width(sw).
		Height(h).
		Render(content)
}

// renderSidebarSectionHeader renders a collapsed section header with a ▸ indicator.
// Clicking it will expand the section.
func (m *cliModel) renderSidebarSectionHeader(label string, collapsed bool) string {
	indicator := "▾" // expanded
	if collapsed {
		indicator = "▸"
	}
	return m.styles.SidebarHeader.Render(indicator + " " + label)
}

// countBlockLines returns the total number of visual lines consumed by the blocks so far,
// accounting for "\n\n" separators between blocks.
func countBlockLines(blocks []string) int {
	n := 0
	for i, blk := range blocks {
		if i > 0 {
			n++ // separator "\n\n" adds 1 line
		}
		n += strings.Count(blk, "\n") + 1
	}
	return n
}

// nextBlockOffset returns the Y-offset where the NEXT block would start
// if appended via strings.Join(append(blocks, ...), "\n\n").
// It accounts for the extra "\n\n" separator that precedes the new block.
func nextBlockOffset(blocks []string) int {
	if len(blocks) == 0 {
		return 0
	}
	return countBlockLines(blocks) + 1 // +1 for the separator before the next block
}

func (m *cliModel) renderSidebarSessions(w int) string {
	// Reset tracking
	m.sidebarHasBusySessions = false
	sidebarSessionLines = nil
	sidebarDeleteXStart = nil
	sidebarDeleteXEnd = nil
	sidebarNewSessionY = -1

	entries := m.sidebarSessionEntries()
	currentIdx := m.sidebarCurrentIdx()

	var b strings.Builder
	b.WriteString(m.renderSidebarSectionHeader("Sessions", false))
	sidebarSessionLines = append(sidebarSessionLines, -1) // header line
	sidebarDeleteXStart = append(sidebarDeleteXStart, -1)
	sidebarDeleteXEnd = append(sidebarDeleteXEnd, -1)

	if len(entries) == 0 {
		b.WriteByte('\n')
		b.WriteString(m.styles.TextMutedSt.Render("  (empty)"))
		sidebarSessionLines = append(sidebarSessionLines, -1)
		sidebarDeleteXStart = append(sidebarDeleteXStart, -1)
		sidebarDeleteXEnd = append(sidebarDeleteXEnd, -1)
	} else {
		for i, s := range entries {
			b.WriteByte('\n')
			label := s.Label
			if label == "" {
				label = s.ID
			}
			// SubAgent entries get a 2-space indent to show parent-child hierarchy.
			indent := ""
			if s.Type == "agent" {
				indent = "  "
			}
			// Layout: "[indent] ○ label" + padding + " ×" = w columns total.
			// ALL sessions reserve space for " ×" so that switching active/inactive
			// never changes the label width (avoids re-truncation and wrapping).
			deletePart := " ×"
			deleteVisW := lipgloss.Width(deletePart)
			indentW := lipgloss.Width(indent)
			// " ○ " visual width varies with EASTASIAN (○ is width 2 in CJK locales).
			// Both ○ and ● have the same width, so we use ○ for measurement.
			iconSepW := lipgloss.Width(" ○ ")
			maxLabelW := w - indentW - iconSepW - 1 - deleteVisW // indent + " ○ " + label + padding(1) + " ×"
			if maxLabelW < 1 {
				maxLabelW = 1
			}
			if lipgloss.Width(label) > maxLabelW {
				label = truncateToWidth(label, maxLabelW)
			}

			isActive := i == currentIdx
			// Determine busy state: for current session use m.typing,
			// for agents use Running, for other main sessions use Busy.
			// Event-driven liveSessionStates (from SessionEvent push) provide
			// instant updates (sub-100ms) before the safety-net poll catches up.
			isBusy := false
			if isActive {
				isBusy = m.typing
			} else if s.Type == "agent" {
				isBusy = s.Running
				// Check live state for agent sessions
				if ls, ok := m.liveSessionStates[s.ID]; ok {
					isBusy = ls.busy
				}
			} else {
				isBusy = s.Busy
				// Live state takes priority for non-active main sessions
				if ls, ok := m.liveSessionStates[s.ID]; ok {
					isBusy = ls.busy
				}
			}

			icon := "○"
			itemStyle := m.styles.SidebarItem
			if isActive {
				// Active: always ● — user can see what's happening.
				icon = "●"
				itemStyle = m.styles.SidebarActive
				// Clear unread flag when user is viewing this session.
				delete(m.unreadSessions, s.ID)
			} else if isBusy {
				// Non-active but busy: animated spinner.
				m.sidebarHasBusySessions = true
				icon = m.ticker.viewFrames(sidebarSpinnerFrames, 3)
				itemStyle = m.styles.SidebarBusy
				// Clear unread when busy — a running session shows a spinner,
				// not the ✦ unread icon. Without this, a stale sessions list
				// that briefly returns Running=false can set unread=true via
				// the busy→idle transition below, and the flag persists even
				// after the sessions list catches up and shows Running=true again.
				delete(m.unreadSessions, s.ID)
			} else if m.unreadSessions[s.ID] {
				// Non-active, idle, but has unread results.
				icon = "✦"
				itemStyle = m.styles.SidebarBusy
			}
			// Track busy→idle transitions to mark unread.
			wasBusy := m.lastBusyStates[s.ID]
			if wasBusy && !isBusy && !isActive {
				m.unreadSessions[s.ID] = true
			}
			m.lastBusyStates[s.ID] = isBusy

			labelPart := indent + " " + icon + " " + label
			labelVisW := lipgloss.Width(labelPart)
			padding := w - labelVisW - deleteVisW
			if padding < 1 {
				padding = 1
			}

			// × position (visual X within sidebar content area)
			deleteX := labelVisW + padding

			b.WriteString(itemStyle.Render(labelPart))
			b.WriteString(strings.Repeat(" ", padding))
			if !isActive {
				b.WriteString(m.styles.TextMutedSt.Render(deletePart))
				sidebarDeleteXStart = append(sidebarDeleteXStart, deleteX)
				sidebarDeleteXEnd = append(sidebarDeleteXEnd, deleteX+deleteVisW)
			} else {
				// Active: same layout but no × rendered or clickable
				sidebarDeleteXStart = append(sidebarDeleteXStart, -1)
				sidebarDeleteXEnd = append(sidebarDeleteXEnd, -1)
			}
			sidebarSessionLines = append(sidebarSessionLines, i)
		}
	}

	// "+ New" button
	b.WriteByte('\n')
	b.WriteByte('\n')
	sidebarNewSessionY = len(sidebarSessionLines) + 1
	b.WriteString(m.styles.Accent.Bold(true).Render("  + New"))

	return b.String()
}

// sidebarSessionEntries returns all session entries.
// When sessionsListFn is set, it handles everything (main + local dir + subagents).
// Otherwise, fall back to local dir sessions only.
func (m *cliModel) sidebarSessionEntries() []SessionPanelEntry {
	if m.sessionsListFn != nil {
		return m.sessionsListFn()
	}
	return m.listLocalDirSessions()
}

func (m *cliModel) renderSidebarActive(w int) string {
	// Reset tracking
	sidebarBgTaskLines = nil

	var b strings.Builder
	b.WriteString(m.renderSidebarSectionHeader("Tasks", false))

	if m.bgTaskCount > 0 {
		// List individual bg tasks so user can click to view log
		tasks := m.listBgTasks()
		if len(tasks) == 0 {
			// Fallback to summary if list is unavailable
			b.WriteByte('\n')
			b.WriteString(m.styles.SidebarItem.Render(fmt.Sprintf("  bg tasks: %d", m.bgTaskCount)))
			sidebarBgTaskLines = append(sidebarBgTaskLines, -1)
		} else {
			s := &m.styles
			for i, task := range tasks {
				b.WriteByte('\n')
				// Status icon: ● running, ✓ done, ✗ error/killed
				icon, iconStyle := "●", s.ProgressRunning
				switch task.Status {
				case BgTaskDone:
					if task.Error != "" || task.ExitCode != 0 {
						icon, iconStyle = "✗", s.ProgressError
					} else {
						icon, iconStyle = "✓", s.ProgressDone
					}
				case BgTaskKilled:
					icon, iconStyle = "✗", s.ProgressError
				case BgTaskError:
					icon, iconStyle = "✗", s.ProgressError
				}
				// Layout: " <icon> <command>" padded to width w
				iconRendered := iconStyle.Render(icon)
				iconW := lipgloss.Width(iconRendered)
				maxCmdW := w - 2 - iconW - 1 // 2 for leading spaces, 1 for space after icon
				if maxCmdW < 5 {
					maxCmdW = 5
				}
				cmd := task.Command
				if lipgloss.Width(cmd) > maxCmdW {
					cmd = truncateToWidth(cmd, maxCmdW)
				}
				line := "  " + iconRendered + " " + cmd
				lineVisW := lipgloss.Width(line)
				padding := w - lineVisW
				if padding < 0 {
					padding = 0
				}
				b.WriteString(m.styles.SidebarItem.Render(line + strings.Repeat(" ", padding)))
				sidebarBgTaskLines = append(sidebarBgTaskLines, i)
			}
		}
	} else {
		sidebarBgTaskLines = nil
	}

	if m.agentCount > 0 {
		b.WriteByte('\n')
		b.WriteString(m.styles.SidebarItem.Render(fmt.Sprintf("  agents: %d", m.agentCount)))
		// Agent lines are not individually clickable (use agents panel instead)
	}

	return b.String()
}

// renderSidebarTodo renders the TODO list inside the sidebar as a compact section.
// It adapts the renderTodoBar format for the narrower sidebar content width.
func (m *cliModel) renderSidebarTodo(w int) string {
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

	s := &m.styles

	var sb strings.Builder
	// Header: "▾ Todo N/M" + progress bar, padded to full width
	headerLabel := m.renderSidebarSectionHeader("Todo", false)
	fmt.Fprintf(&sb, "%s %d/%d", headerLabel, done, total)
	sb.WriteString(" ")
	barWidth := 10
	filled := 0
	if total > 0 {
		filled = done * barWidth / total
	}
	sb.WriteString(s.TodoFilled.Render(strings.Repeat("█", filled)))
	sb.WriteString(s.TodoEmpty.Render(strings.Repeat("░", barWidth-filled)))

	// Items — one per line, single style per line to avoid ANSI boundary
	// wrapping artifacts in the narrow sidebar. Pattern mirrors
	// renderSidebarSessions: truncate, pad to width, one style.
	for _, item := range m.todos {
		sb.WriteByte('\n')
		icon := "○"
		var style lipgloss.Style
		if item.Done {
			icon = "✓"
			style = s.TodoDone
		} else {
			style = s.TodoPending
		}

		prefix := "  " + icon + " "
		prefixW := lipgloss.Width(prefix)
		maxTextW := w - prefixW
		if maxTextW < 2 {
			maxTextW = 2
		}

		text := item.Text
		if lipgloss.Width(text) > maxTextW {
			text = truncateToWidth(text, maxTextW)
		}

		line := prefix + text
		lineW := lipgloss.Width(line)
		linePadding := w - lineW
		if linePadding < 0 {
			linePadding = 0
		}

		sb.WriteString(style.Render(line))
		sb.WriteString(strings.Repeat(" ", linePadding))
	}

	return sb.String()
}

// sidebarCurrentIdx returns the index of the currently active session.
func (m *cliModel) sidebarCurrentIdx() int {
	entries := m.sidebarSessionEntries()
	// Match by chatID — never fall back to Active flag because it can
	// be stale (e.g. SessionsList callback hardcodes Active=true for
	// the main session, which mislabels it as active after switching
	// to a different session).
	for i, e := range entries {
		if e.ID == m.chatID {
			return i
		}
		// For agent sessions, entry ID uses format "agent:role/instance" but
		// chatID uses format "channel:parentID/role:instance". Match by
		// constructing the chatID from entry fields (same as panel code).
		if e.Type == "agent" {
			agentChatID := e.Channel + ":" + e.ParentID + "/" + e.Role
			if e.Instance != "" {
				agentChatID += ":" + e.Instance
			}
			if agentChatID == m.chatID {
				return i
			}
		}
	}
	return -1
}

// padLineToWidth pads a styled line with trailing spaces so it fills the full
// terminal width. This prevents stale content from previous frames lingering
// at the right edge when widget zone content shrinks (BubbleTea's diff renderer
// only updates changed positions and does not clear the line tail).
func (m *cliModel) padLineToWidth(line string) string {
	lineW := lipgloss.Width(line)
	// Use chatWidth (accounts for sidebar) so lines don't overflow when
	// horizontally joined with the sidebar via lipgloss.JoinHorizontal.
	targetW := m.chatWidth()
	if lineW >= targetW {
		return line
	}
	return line + strings.Repeat(" ", targetW-lineW)
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
// The result is padded to full terminal width so that stale content from a
// previous (wider) frame is overwritten.
func (m *cliModel) augmentStatusBar(statusBar string) string {
	left, right := m.resolveWidgetZone("statusBarLeft"), m.resolveWidgetZone("statusBarRight")
	if left == "" && right == "" {
		return m.padLineToWidth(statusBar)
	}
	if left != "" {
		statusBar = left + "  " + statusBar
	}
	if right != "" {
		statusBar = statusBar + "  " + right
	}
	return m.padLineToWidth(statusBar)
}

// augmentFooter appends footer widget content below the shortcut-hint bar.
// The result is padded to full terminal width so that stale content from a
// previous (wider) frame is overwritten.
func (m *cliModel) augmentFooter(footer string) string {
	content := m.resolveWidgetZone("footer")
	if content == "" {
		return m.padLineToWidth(footer)
	}
	widgetLine := m.styles.TextMutedSt.Render(content)
	if footer == "" {
		return m.padLineToWidth(widgetLine)
	}
	return m.padLineToWidth(footer + "  " + widgetLine)
}

// augmentInfoBar appends infoBar widget content to the base info bar.
// Widget content is appended left-aligned after the bg task info (if present).
// The widget's own styling (from buildWidgetRenderFn) is preserved as-is.
// The result is padded to full terminal width so that stale content from a
// previous (wider) frame is overwritten.
func (m *cliModel) augmentInfoBar(infoBar string) string {
	content := m.resolveWidgetZone("infoBar")
	if content == "" {
		return m.padLineToWidth(infoBar)
	}
	if infoBar == "" {
		return m.padLineToWidth(content)
	}
	return m.padLineToWidth(infoBar + "  " + content)
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
func (m *cliModel) View() (v tea.View) {
	defer clipanic.Recover("channel.cliModel.View", nil, true)
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

	// Remote reconnect overlay — show spinner when WS is disconnected.
	// Placed after suLoading so that session-switch reconnect doesn't conflict.
	if m.remoteMode && m.connState != "connected" && m.connState != "" {
		if overlay := m.renderReconnectOverlay(); overlay != "" {
			v := tea.NewView(overlay)
			v.AltScreen = true
			return v
		}
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

	v = tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion

	// Set real cursor position to match textarea cursor.
	// This is essential for IME candidate window positioning:
	// without it, the terminal's real cursor drifts during streaming
	// output, causing the IME candidate popup to jump around.
	if cx, cy, ok := m.computeInputCursorScreenPos(); ok {
		v.Cursor = tea.NewCursor(cx, cy)
		v.Cursor.Shape = tea.CursorBar // bar cursor for text input
		v.Cursor.Blink = true
		v.Cursor.Color = m.styles.TACursor.GetForeground()
	}

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
// renderInfoBar builds the info bar below the input box.
// Always produces output (at minimum the workspace indicator) so the user
// can see which workspace/session they're in. layoutViewportHeight reserves
// a matching line via infoBarLines=1.
func (m *cliModel) renderInfoBar() string {
	hasTasks := m.bgTaskCount > 0
	hasAgents := m.agentCount > 0
	hasQueue := len(m.messageQueue) > 0

	// Always show workspace indicator (pinned to left).
	wsIndicator := m.renderWorkspaceIndicator()

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
	pinnedLeft := wsIndicator
	content := strings.Join(parts, separator)
	if pinnedLeft != "" {
		if content != "" {
			content = pinnedLeft + separator + content
		} else {
			content = pinnedLeft
		}
	}

	// Left padding of 2 (matching InputBox visual)
	return lipgloss.NewStyle().
		PaddingLeft(2).
		Render(content)
}

// renderWorkspaceIndicator returns a workspace status string.
// "🏠 main workspace" for main workspace, "🌿 <name>" for worktree sessions.
func (m *cliModel) renderWorkspaceIndicator() string {
	cwd := ""
	if m.progress != nil {
		cwd = m.progress.CWD
	}

	if cwd != "" && strings.Contains(cwd, ".xbot-worktrees") {
		dirName := filepath.Base(cwd)
		shortName := shortenWorktreeName(dirName)
		return fmt.Sprintf("🌿 %s", m.styles.Accent.Render(shortName))
	}

	// No progress yet — derive from chatID. Named sessions (chatID
	// has a session name after ':') are likely worktree sessions.
	// Default session (chatID == workDir) is always primary.
	if m.chatID != "" && m.chatID != m.workDir {
		sessionName := m.chatID
		if idx := strings.LastIndex(m.chatID, ":"); idx > 0 {
			sessionName = m.chatID[idx+1:]
		}
		return fmt.Sprintf("🌿 %s", m.styles.Accent.Render(sessionName))
	}

	return fmt.Sprintf("🏠 %s", m.styles.TextMutedSt.Render("main workspace"))
}

// shortenWorktreeName shortens a worktree directory name for display.
func shortenWorktreeName(dirName string) string {
	// dirName format: {role}-{sessionKey_shortened}-{timestamp}
	// e.g. peer-cli--home-user-src-xbot-worktree-20260509-180133
	// Show just the role part + short timestamp
	parts := strings.Split(dirName, "-")
	if len(parts) > 2 {
		// Last parts are timestamp: YYYYMMDD-HHMMSS
		if len(parts) >= 4 {
			datePart := parts[len(parts)-2] + "-" + parts[len(parts)-1]
			if len(datePart) == 13 { // YYYYMMDD-HHMMSS
				return parts[0] + " " + datePart[4:6] + "/" + datePart[6:8] + " " + datePart[9:11] + ":" + datePart[11:13]
			}
		}
	}
	if len(dirName) > 25 {
		dirName = dirName[:25] + "…"
	}
	return dirName
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
	screenW := m.chatWidth()
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
	versionText := versionStyle.Render(fmt.Sprintf("xbot %s · %s · %s", version.Version, version.Channel, version.Commit))
	vW := lipgloss.Width(versionText)
	vPad := (screenW - vW) / 2
	if vPad < 0 {
		vPad = 0
	}
	lines = append(lines, strings.Repeat(" ", vPad)+versionText)

	// 描述居中（节日版彩蛋 + 首次运行欢迎）
	splashDesc := m.locale.SplashDesc
	if holidayDesc := getHolidaySplashDesc(); holidayDesc != "" {
		splashDesc = holidayDesc
	}
	if m.channel != nil && m.channel.config.IsFirstRun && m.locale.SplashFirstRun != "" {
		splashDesc = m.locale.SplashFirstRun
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
	screenW := m.chatWidth()
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

// renderReconnectOverlay renders a full-screen reconnect spinner overlay
// when the remote WS connection is lost. Blocks all interaction except quit.
func (m *cliModel) renderReconnectOverlay() string {
	screenW := m.chatWidth()
	if screenW < 40 {
		screenW = 40
	}
	screenH := m.height
	if screenH < 10 {
		screenH = 10
	}

	warningStyle := m.styles.WarningSt
	errorStyle := m.styles.ErrorMsg
	mutedStyle := m.styles.TextMutedSt

	frame := splashFrames[m.reconnectFrame%len(splashFrames)]

	var lines []string

	// Title — connection lost
	titleText := errorStyle.Render(m.locale.ReconnectTitle)
	tW := lipgloss.Width(titleText)
	tPad := (screenW - tW) / 2
	if tPad < 0 {
		tPad = 0
	}
	lines = append(lines, strings.Repeat(" ", tPad)+titleText)

	// Blank line
	lines = append(lines, "")

	// Server URL
	host := m.remoteServerURL
	if u, err := url.Parse(host); err == nil && u.Host != "" {
		host = u.Host
	}
	serverText := mutedStyle.Render("  " + host)
	sW := lipgloss.Width(serverText)
	sPad := (screenW - sW) / 2
	if sPad < 0 {
		sPad = 0
	}
	lines = append(lines, strings.Repeat(" ", sPad)+serverText)

	// Blank line
	lines = append(lines, "")

	// Spinner + reconnecting message
	var spinnerMsg string
	switch m.connState {
	case "disconnected":
		spinnerMsg = fmt.Sprintf(m.locale.ReconnectingMsg, frame)
	default: // "reconnecting" or any other non-connected state
		spinnerMsg = fmt.Sprintf(m.locale.ReconnectingMsg, frame)
	}
	loadingText := warningStyle.Render(spinnerMsg)
	lW := lipgloss.Width(loadingText)
	lPad := (screenW - lW) / 2
	if lPad < 0 {
		lPad = 0
	}
	lines = append(lines, strings.Repeat(" ", lPad)+loadingText)

	// Blank line
	lines = append(lines, "")

	// Quit hint
	hintText := mutedStyle.Render(m.locale.ReconnectHint)
	hW := lipgloss.Width(hintText)
	hPad := (screenW - hW) / 2
	if hPad < 0 {
		hPad = 0
	}
	lines = append(lines, strings.Repeat(" ", hPad)+hintText)

	// Vertical center
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

// sidebarDeleteXStart tracks the X position of the "×" delete button for each
// session line. Indexed same as sidebarSessionLines. -1 means no delete button.
var sidebarDeleteXStart []int

// sidebarDeleteXEnd is the X end of each "×" delete button.
var sidebarDeleteXEnd []int

// sidebarBgTaskLines tracks Y-offsets of each bg task row in the sidebar's "Active" section.
// Populated during renderSidebarActive, consumed by trackMainLayoutZones.
// -1 means "no task" (header, blank line, etc).
var sidebarBgTaskLines []int

// sidebarActiveSectionOffset tracks the Y-offset of the "Active" section header
// within the sidebar content. Used by trackMainLayoutZones to register bg task zones.
// -1 means not rendered.
var sidebarActiveSectionOffset int

// sidebarSectionHeaders tracks Y-offsets of each section header for click-to-collapse.
// Key = section name ("sessions", "todo", "tasks"), Value = Y line offset within sidebar content.
var sidebarSectionHeaders map[string]int

func init() {
	sidebarSectionHeaders = make(map[string]int)
}

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
		case "wizard":
			switch m.wizardStep {
			case wizardAPIKey:
				hints = append(hints,
					m.footerHintItem("Enter/Ctrl+s", m.locale.WizardNextBtn, "enter"),
					m.footerHintItem("Esc", m.locale.WizardBackBtn, "esc"),
				)
			case wizardDone:
				hints = append(hints,
					m.footerHintItem("Enter", m.locale.WizardStartBtn, "enter"),
					m.footerHintItem("Esc", m.locale.WizardBackBtn, "esc"),
				)
			default:
				hints = append(hints,
					m.footerHintItem("↑↓/jk", m.locale.FooterNavigate, "navigate"),
					m.footerHintItem("Enter", m.locale.WizardNextBtn, "enter"),
					m.footerHintItem("Esc", escLabel, "esc"),
				)
			}
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
		footerText = padBetween(footerText, helpHint, m.chatWidth()-2)
		if lipgloss.Width(footerText) <= m.chatWidth()-2 {
			// Store X positions for mouse zone tracking
			for i := range hints {
				if i < len(xPositions) {
					hints[i].xStart = xPositions[i]
					hints[i].xEnd = xPositions[i+1]
				}
			}
			footerHints = hints
			return m.styles.Footer.Width(m.chatWidth() - 2).Render(footerText)
		}
		hints = hints[:len(hints)-1]
	}

	footerHints = nil
	return m.styles.Footer.Width(m.chatWidth() - 2).Render(
		padBetween(ellipsis, helpHint, max(ellipsisW+lipgloss.Width(helpHint)+1, m.chatWidth()-2)))
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
func (m *cliModel) renderProgressStatus() string {
	var sb strings.Builder

	// Model name (show during iteration)
	m.modelNameZoneXStart = -1
	m.modelNameZoneXEnd = -1
	if m.cachedModelName != "" {
		m.modelNameZoneXStart = 0
		sb.WriteString(m.cachedModelName)
		m.modelNameZoneXEnd = lipgloss.Width(sb.String())
		sb.WriteString(" · ")
	}

	if m.progress != nil {
		fmt.Fprintf(&sb, "#%d", m.progress.Iteration)

		// Phase hint
		switch m.progress.Phase {
		case "thinking":
			sb.WriteString(" · " + m.pickVerb(m.ticker.ticks))
		case "compressing":
			sb.WriteString(" · " + m.locale.StatusCompressing)
		case "newing":
			sb.WriteString(" · " + m.locale.StatusNewing)
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
// Returns empty string only when cachedMaxContextTokens is unavailable (<=0),
// meaning the token budget cannot be determined. Once the budget is known,
// the bar ALWAYS renders — as a filled bar when token data is available,
// or as an empty bar when lastTokenUsage is nil (e.g. before first LLM call).
// This prevents the jarring "bar disappears" flash that happened when
// lastTokenUsage was temporarily nil due to progressCh coalescing.
func (m *cliModel) renderContextTopBorder(borderColor color.Color, renderedBox string) string {
	maxTokens := int64(m.cachedMaxContextTokens)
	if maxTokens <= 0 {
		return ""
	}
	var promptTokens int64
	if m.lastTokenUsage != nil {
		promptTokens = m.lastTokenUsage.PromptTokens
	}
	// Don't bail on promptTokens==0 — show an empty bar instead of flashing
	// back to the plain border. lastTokenUsage is only cleared by explicit
	// delete-record RPCs (/clear, /cancel, session reset); during normal
	// operation a zero prompt count just means no LLM call has completed yet.
	if promptTokens < 0 {
		promptTokens = 0
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
		maxOutputTokens = config.DefaultMaxOutputTokens
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

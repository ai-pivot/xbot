package channel

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
	"xbot/version"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// appendHint appends a styled hint to the status string, with separator spacing.
func appendHint(status, hint string) string {
	if status != "" {
		return status + "  " + hint
	}
	return hint
}

// View renders the full TUI. It acts as a thin coordinator that delegates
// to focused render sub-methods based on the current UI mode.
func (m *cliModel) View() tea.View {
	// §14 Splash screen: brand animation (auto-dismisses after ~2.4 seconds)
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

	// 🥚 Easter egg overlay: render fullscreen overlay when an easter egg is active
	if m.easterEgg != easterEggNone {
		v := tea.NewView(m.renderEasterEggOverlay())
		v.AltScreen = true
		return v
	}

	// Loading screen during history loading after /su user switch
	if m.suLoading {
		v := tea.NewView(m.renderSuLoading())
		v.AltScreen = true
		return v
	}

	// Build shared UI sections
	titleBar := m.renderTitleBar()
	input := m.renderInputBox()
	toastStr := m.renderToast()

	// Select layout based on current mode
	var content string
	switch {
	case m.searchMode:
		content = m.renderSearchLayout(titleBar, input, toastStr)
	case m.panelMode == panelModeAskUser:
		content = m.renderAskUserLayout(titleBar, toastStr)
	case m.panelMode != "":
		content = m.renderPanelLayout(titleBar, toastStr)
	default:
		content = m.renderMainLayout(titleBar, input, toastStr)
	}

	v := tea.NewView(content)
	v.AltScreen = true

	// §15 Quick switch overlay (subscription/model picker)
	if m.quickSwitchMode != "" {
		if overlay := m.viewQuickSwitch(m.width, m.height); overlay != "" {
			v.Content = overlay
		}
	}

	// §9 Rewind overlay (/rewind command)
	if m.rewindMode {
		if overlay := m.viewRewindPanel(m.width, m.height); overlay != "" {
			v.Content = overlay
		}
	}

	return v
}

// ---------------------------------------------------------------------------
// View sub-methods: each renders one focused section of the TUI.
// ---------------------------------------------------------------------------

// renderTitleBar builds the top title bar showing the application label,
// working directory, runner status, identity, and shortcut hints.
func (m *cliModel) renderTitleBar() string {
	titleLeft := m.titleText()
	titleRight := m.locale.TitleHint

	// Askuser panel: override titleRight with panel-specific hints (always visible)
	if m.panelMode == panelModeAskUser {
		titleRight = m.askUserTitleHints()
	} else if m.updateNotice != nil && m.updateNotice.HasUpdate {
		titleRight = fmt.Sprintf("%s→%s · /update · /help", m.updateNotice.Current, m.updateNotice.Latest)
	}

	// Runner status + identity indicator in title bar
	if m.runnerBridge != nil {
		switch m.runnerBridge.Status() {
		case RunnerConnected:
			titleRight = "🟢 Runner · " + titleRight
		case RunnerConnecting:
			titleRight = "🟡 Runner · " + titleRight
		}
	}
	if m.senderID != "cli_user" {
		titleRight = "👤 " + m.senderID + " · " + titleRight
	}

	titlePad := m.width - lipgloss.Width(titleLeft) - lipgloss.Width(titleRight)
	if titlePad < 1 {
		titlePad = 1
	}
	return m.styles.TitleBar.Render(titleLeft + strings.Repeat(" ", titlePad) + titleRight)
}

// renderInputBox renders the textarea input area with dynamic border color
// based on input content (! → error, / → success, @ → info, default → accent).
// When the textarea is empty, a custom placeholder is rendered to avoid
// textarea's built-in placeholder that triggers CJK rendering bugs.
func (m *cliModel) renderInputBox() string {
	inputValue := m.textarea.Value()
	borderColor, _ := m.renderCompletionsHint(inputValue)

	inputBoxStyle := m.styles.InputBox.BorderForeground(borderColor)
	inputArea := m.textarea.View()

	// §23 Render placeholder manually when textarea is empty.
	// This avoids textarea's built-in placeholder which causes a view-mode
	// switch that triggers CJK rendering bugs on Windows Terminal.
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

	return inputBoxStyle.Render(inputArea)
}

// renderStatusBar builds the status bar shown in the default (non-panel) layout.
// It displays thinking progress, completion hints, or the ready state with
// contextual indicators (session info, message count, model, temp status, etc.).
func (m *cliModel) renderStatusBar() string {
	// §20 Use cached styles
	thinkingStatusStyle := m.styles.ThinkingSt
	progressStyle := m.styles.Progress
	toolStyle := m.styles.Tool
	readyStatusStyle := m.styles.ReadyStatus

	var status string
	if m.typing || m.progress != nil {
		// Show spinner + progress info
		status = thinkingStatusStyle.Render(m.renderProgressStatus(progressStyle, toolStyle))
	} else if m.checkingUpdate {
		status = thinkingStatusStyle.Render(m.locale.CheckingUpdates)
	} else if _, completionsHint := m.renderCompletionsHint(m.textarea.Value()); completionsHint != "" {
		// Show completion candidate hint
		status = completionsHint
	} else {
		// Ready state: show message count + current model (if overridden)
		readyParts := []string{m.locale.StatusReady}
		// Session indicator (for agent sessions)
		if m.channelName == "agent" {
			// Extract role/instance from chatID format: "channel:chatID/role:instance"
			parts := strings.SplitN(m.chatID, "/", 2)
			if len(parts) == 2 {
				readyParts = append(readyParts, fmt.Sprintf("🤖 %s", parts[1]))
			} else {
				readyParts = append(readyParts, fmt.Sprintf("🤖 %s", m.chatID))
			}
		}
		// Message count
		msgCount := len(m.messages)
		if msgCount > 0 {
			readyParts = append(readyParts, fmt.Sprintf("%d msg%s", msgCount, func() string {
				if msgCount > 1 {
					return "s"
				}
				return ""
			}()))
		}
		// Model name (use cache, avoid repeated lookup on each View())
		if m.cachedModelName != "" {
			modelHint := m.cachedModelName
			if m.modelCount > 1 {
				modelHint += " [Ctrl+N]"
			}
			readyParts = append(readyParts, modelHint)
		}
		status = readyStatusStyle.Render(strings.Join(readyParts, " · "))
	}

	// Temporary status hint (auto-expires)
	if m.tempStatus != "" {
		status = appendHint(status, m.styles.WarningSt.Render(m.tempStatus))
	}
	// New message hint: show when user has scrolled up and there's new content
	if m.newContentHint {
		status = appendHint(status, m.styles.InfoSt.Render(m.locale.NewContentHint))
	}
	// Background task indicator
	if m.bgTaskCount > 0 {
		status = appendHint(status, m.styles.WarningSt.Render(
			fmt.Sprintf(m.locale.BgTaskRunning, m.bgTaskCount)))
	}
	// Agent indicator
	if m.agentCount > 0 {
		status = appendHint(status, m.styles.WarningSt.Render(
			fmt.Sprintf(m.locale.AgentRunning, m.agentCount)))
	}
	// Message queue indicator (persistent, not temp status)
	if len(m.messageQueue) > 0 {
		status = appendHint(status, m.styles.InfoSt.Render(
			fmt.Sprintf(m.locale.QueuePending, len(m.messageQueue))))
	}

	return status
}

// renderSearchLayout assembles the full-screen content for search mode,
// overlaying a search bar between the viewport and input area.
func (m *cliModel) renderSearchLayout(titleBar, input, toastStr string) string {
	var searchBar string
	if m.searchEditing {
		searchBar = m.styles.SearchBar.Render(m.searchTI.View())
	} else {
		searchBar = m.styles.SearchBar.Render(
			fmt.Sprintf(m.locale.SearchNavFormat, m.searchQuery, m.searchIdx+1, len(m.searchResults)))
	}
	return fmt.Sprintf(
		"%s\n%s\n%s\n%s%s",
		titleBar,
		m.viewport.View(),
		searchBar,
		input,
		toastStr,
	)
}

// renderAskUserLayout assembles the split layout for the ask-user panel:
// viewport visible above, scrollable ask-user panel at the bottom.
func (m *cliModel) renderAskUserLayout(titleBar, toastStr string) string {
	// §12b AskUser split layout: viewport visible above, panel at bottom
	// Note: no panelFooter here — hints are inside the panel (viewAskUserPanel)
	askRaw := m.viewAskUserPanel()
	m.clampAskUserPanelScroll(askRaw)
	askLines := strings.Split(askRaw, "\n")
	// Calculate available height for the ask panel
	fixedLines := 2 // titleBar + toast (no separate footer — hints are in-panel)
	panelBorder := 2
	viewportH := m.layoutViewportHeight()
	askVisibleH := m.height - fixedLines - viewportH - panelBorder
	if askVisibleH < 3 {
		askVisibleH = 3
	}
	if m.askPanelScrollY+askVisibleH > len(askLines) {
		m.askPanelScrollY = max(0, len(askLines)-askVisibleH)
	}
	end := m.askPanelScrollY + askVisibleH
	if end > len(askLines) {
		end = len(askLines)
	}
	visibleAsk := askLines[m.askPanelScrollY:end]
	askContent := strings.Join(visibleAsk, "\n")
	boxedAsk := m.styles.PanelBox.Render(askContent)
	// Scroll indicator
	totalAskLines := len(askLines)
	scrollHint := ""
	if totalAskLines > askVisibleH {
		pct := (m.askPanelScrollY + askVisibleH) * 100 / totalAskLines
		scrollHint = m.styles.PanelDesc.Render(fmt.Sprintf(" [%d%%] Ctrl+↑↓/PgUp/PgDn", pct))
	}
	return fmt.Sprintf(
		"%s\n%s\n%s%s%s",
		titleBar,
		m.viewport.View(),
		boxedAsk,
		scrollHint,
		toastStr,
	)
}

// renderPanelLayout assembles the full-screen content for general panel modes
// (settings, bgtasks, approval, etc.) with manual line slicing and PanelBox wrapping.
func (m *cliModel) renderPanelLayout(titleBar, toastStr string) string {
	// §12 Panel mode: manual slicing + PanelBox wrapping (border always within screen)
	panelFooter := m.renderFooter()
	rawContent := m.viewPanel() // Raw content, no PanelBox
	m.clampPanelScroll(rawContent)
	rawLines := strings.Split(rawContent, "\n")
	visibleH := m.panelVisibleHeight()
	// Slice visible lines
	if m.panelScrollY+visibleH > len(rawLines) {
		m.panelScrollY = max(0, len(rawLines)-visibleH)
	}
	end := m.panelScrollY + visibleH
	if end > len(rawLines) {
		end = len(rawLines)
	}
	visible := rawLines[m.panelScrollY:end]
	panelContent := strings.Join(visible, "\n")
	// PanelBox wrapping (border after slicing, ensures complete display)
	boxedContent := m.styles.PanelBox.Render(panelContent)
	return fmt.Sprintf(
		"%s\n%s%s%s",
		titleBar,
		boxedContent,
		panelFooter,
		toastStr,
	)
}

// renderMainLayout assembles the default (non-panel, non-search) layout:
// title bar, viewport, status bar, optional todo/footer bars, input, and toast.
func (m *cliModel) renderMainLayout(titleBar, input, toastStr string) string {
	status := m.renderStatusBar()

	todoBar := m.renderTodoBar()
	footer := m.renderFooter()

	switch {
	case todoBar != "":
		return fmt.Sprintf(
			"%s\n%s\n%s\n%s\n%s%s",
			titleBar,
			m.viewport.View(),
			status,
			todoBar,
			input,
			toastStr,
		)
	case footer != "":
		return fmt.Sprintf(
			"%s\n%s\n%s\n%s\n%s%s",
			titleBar,
			m.viewport.View(),
			status,
			footer,
			input,
			toastStr,
		)
	default:
		return fmt.Sprintf(
			"%s\n%s\n%s\n%s%s",
			titleBar,
			m.viewport.View(),
			status,
			input,
			toastStr,
		)
	}
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

// titleText Generate title bar text.
func (m *cliModel) titleText() string {
	modeLabel := "⌂ xbot"
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
			cloud = "☁"
		case "disconnected":
			cloud = "⊘"
		case "reconnecting":
			cloud = "◌"
		default:
			cloud = "☁"
		}
		if host != "" {
			modeLabel = fmt.Sprintf("%s xbot %s", cloud, host)
		} else {
			modeLabel = fmt.Sprintf("%s xbot remote", cloud)
		}
	}
	if m.workDir != "" {
		// Resolve to absolute path so "." → actual directory name
		abs, err := filepath.Abs(m.workDir)
		if err == nil {
			return fmt.Sprintf(" %s [%s]", modeLabel, filepath.Base(abs))
		}
		return fmt.Sprintf(" %s [%s]", modeLabel, filepath.Base(m.workDir))
	}
	return " " + modeLabel
}

// ---------------------------------------------------------------------------
// §14 Dynamic title bar hints
// ---------------------------------------------------------------------------

// askUserTitleHints returns the minimal control hints for the askuser panel,
// displayed in the header bar so they're always visible regardless of scroll.
// Keep it short — header width is limited and line wrap looks terrible.
func (m *cliModel) askUserTitleHints() string {
	hints := []string{"Shift+↑↓ history", "Ctrl+↑↓ question", "Enter submit", "Esc cancel"}
	if len(m.panelItems) > 1 {
		hints = append([]string{"←→/Tab switch"}, hints...)
	}
	return strings.Join(hints, " · ")
}

// ---------------------------------------------------------------------------
// §14 Splash Screen
// ---------------------------------------------------------------------------

// xbotLogo — "XBOT" ASCII art (slant font, generated by figlet)
var xbotLogo = []string{
	"   _  __    ____    ____    ______",
	"  | |/ /   / __ )  / __ \\  /_  __/",
	"  |   /   / __  | / / / /   / /",
	" /   |   / /_/ / / /_/ /   / /",
	"/_/|_|  /_____/  \\____/   /_/",
}

// centerLine centers a styled text line within the given screen width.
func centerLine(screenW int, text string, style lipgloss.Style) string {
	styled := style.Render(text)
	w := lipgloss.Width(styled)
	pad := (screenW - w) / 2
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad) + styled
}

// renderSplash Render splash screen — brand logo + version number + loading animation
func (m *cliModel) renderSplash() string {
	// Centering calculation
	screenW := m.width
	if screenW < 40 {
		screenW = 40
	}
	screenH := m.height
	if screenH < 10 {
		screenH = 10
	}

	// §20 Use cached styles
	logoStyle := m.styles.Accent.Bold(true)
	versionStyle := m.styles.VersionSt
	descStyle := m.styles.TextMutedSt
	loadingStyle := m.styles.WarningSt

	// Assemble splash content (logo centered by widest line, preserving internal letter alignment)
	var lines []string
	maxLogoW := 0
	renderedLogo := make([]string, len(xbotLogo))
	for i, line := range xbotLogo {
		renderedLogo[i] = logoStyle.Render(line)
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

	// Blank line
	lines = append(lines, "")

	// Version number centered
	lines = append(lines, centerLine(screenW, fmt.Sprintf("xbot %s · %s", version.Version, version.Commit), versionStyle))

	// Description centered (holiday edition easter egg)
	splashDesc := m.locale.SplashDesc
	if holidayDesc := getHolidaySplashDesc(); holidayDesc != "" {
		splashDesc = holidayDesc
	}
	lines = append(lines, centerLine(screenW, splashDesc, descStyle))

	// Blank line
	lines = append(lines, "")

	// Loading animation
	frame := splashFrames[m.splashFrame%len(splashFrames)]
	lines = append(lines, centerLine(screenW, fmt.Sprintf(m.locale.SplashLoading, frame), loadingStyle))

	// Vertical centering
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

// renderSuLoading Render history loading screen after /su user switch (reuses splash animation frames)
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

	// Center content
	var lines []string
	frame := splashFrames[m.splashFrame%len(splashFrames)]

	// Switch target hint
	lines = append(lines, centerLine(screenW, fmt.Sprintf(m.locale.SuSwitching, m.senderID), descStyle))

	// Blank line
	lines = append(lines, "")

	// Loading animation
	lines = append(lines, centerLine(screenW, fmt.Sprintf(m.locale.SuLoadingHistory, frame), loadingStyle))

	// Vertical centering
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
// §15 Bottom shortcut hint bar (Footer Bar)
// ---------------------------------------------------------------------------

// renderFooter Render bottom shortcut hint bar.
// Dynamically show most relevant shortcuts based on current state, avoiding information overload.
func (m *cliModel) renderFooter() string {
	// Collect most relevant shortcut hints for current context
	var hints []string

	if m.panelMode != "" {
		// When panel is open: show panel-related shortcuts
		switch m.panelMode {
		case "bgtasks":
			if m.panelBgViewing {
				hints = append(hints, m.keyHint("PgUp/PgDn", m.locale.FooterScroll), m.keyHint("Esc", m.locale.FooterBack))
			} else {
				hints = append(hints, m.keyHint("↑↓", m.locale.FooterNavigate), m.keyHint("Enter", m.locale.FooterLog), m.keyHint("Del", m.locale.FooterKill), m.keyHint("Esc", m.locale.FooterClose))
			}
		case "approval":
			hints = append(hints, m.keyHint("←→", m.locale.FooterNavigate), m.keyHint("y/n", "Quick"), m.keyHint("Enter", m.locale.FooterSelect), m.keyHint("Esc", "Deny"))
		default:
			hints = append(hints, m.keyHint("↑↓", m.locale.FooterNavigate), m.keyHint("Enter", m.locale.FooterSelect), m.keyHint("Esc", m.locale.FooterClose))
		}
	} else if m.typing {
		// During processing: show cancel shortcut
		hints = append(hints, m.ctrlKey("c", m.locale.FooterCancel))
	} else {
		// Ready state: show core shortcuts
		if m.textarea.Value() == "" {
			hints = append(hints, m.ctrlKey("k", m.locale.FooterDelete), m.keyHint("/", m.locale.FooterCommands), m.keyHint("tab", m.locale.FooterComplete), m.ctrlKey("e", m.locale.FooterFold))
			if m.subscriptionMgr != nil {
				hints = append(hints, m.ctrlKey("p", "Subs"))
			}
			hints = append(hints, m.ctrlKey("t", "Sessions"))
			if m.bgTaskCount > 0 {
				hints = append(hints, m.keyHint("^", m.locale.FooterBgTasks))
			}
		} else {
			hints = append(hints, m.ctrlKey("j", m.locale.FooterNewline), m.keyHint("tab", m.locale.FooterComplete), m.ctrlKey("k", m.locale.FooterDelete))
		}
	}

	if len(hints) == 0 {
		return ""
	}

	// §20 Use cached styles
	helpHint := m.styles.TextMutedSt.Render("/help")
	ellipsis := m.styles.TextMutedSt.Render("…")
	ellipsisW := lipgloss.Width(ellipsis)
	// Progressively drop hints from the end until the footer fits.
	// The rightmost "/help" is always preserved; extra hints are trimmed
	// and replaced with "…" when the terminal is too narrow.
	for len(hints) > 0 {
		footerText := strings.Join(hints, "  ")
		footerText = padBetween(footerText, helpHint, m.width)
		if lipgloss.Width(footerText) <= m.width {
			return m.styles.Footer.Width(m.width).Render(footerText)
		}
		hints = hints[:len(hints)-1]
	}
	// Even a single hint overflows — show just "… /help"
	return m.styles.Footer.Width(m.width).Render(
		padBetween(ellipsis, helpHint, max(ellipsisW+lipgloss.Width(helpHint)+1, m.width)))
}

// ctrlKey Render Ctrl+X shortcut label (gray keycap + colored description)
func (m *cliModel) ctrlKey(key string, desc string) string {
	k := m.styles.KeyLabelSt.Render("Ctrl+" + key)
	d := m.styles.KeyDescSt.Render(desc)
	return k + " " + d
}

// keyHint Render normal key label
func (m *cliModel) keyHint(key, desc string) string {
	k := m.styles.KeyLabelSt.Render(key)
	d := m.styles.KeyDescSt.Render(desc)
	return k + " " + d
}

// padBetween Pad spaces between left and right text to reach total width
func padBetween(left, right string, width int) string {
	w := lipgloss.Width(left) + lipgloss.Width(right)
	if w >= width {
		return left + " " + right
	}
	return left + strings.Repeat(" ", width-w) + right
}

// renderToast Render bottom Toast notification stack (§16).
// Supports multiple toasts queued, max 3 rendered simultaneously, 3-second rotation.
// Floats at the very bottom of the UI, uses Surface background consistent with theme.
func (m *cliModel) renderToast() string {
	if len(m.toasts) == 0 {
		return ""
	}

	// Max 3 displayed
	showCount := len(m.toasts)
	if showCount > 3 {
		showCount = 3
	}

	var lines []string
	for i := 0; i < showCount; i++ {
		item := m.toasts[i]

		iconSty := m.styles.ToastIcon
		switch item.icon {
		case "✗", "⚠":
			iconSty = iconSty.Foreground(lipgloss.Color(currentTheme.Error))
		case "ℹ":
			iconSty = iconSty.Foreground(lipgloss.Color(currentTheme.Info))
		}

		// More transparent for later ones (creating depth)
		faintFactor := i // 0=newest/brightest, 1=slightly dimmer, 2=dimmest
		if faintFactor > 0 {
			iconSty = iconSty.Faint(true)
		}
		textSty := m.styles.ToastText
		if faintFactor > 0 {
			textSty = textSty.Faint(true)
		}

		toastContent := iconSty.Render(" "+item.icon+" ") + " " + textSty.Render(item.text)
		lines = append(lines, m.styles.ToastBg.Render(toastContent))
	}

	return "\n" + strings.Join(lines, "\n")
}

// renderProgressStatus renders a compact one-line status for the status bar.
func (m *cliModel) renderProgressStatus(progressStyle, toolStyle lipgloss.Style) string {
	s := &m.styles // §20
	var sb strings.Builder
	sb.WriteString(s.Progress.Render(m.ticker.view()))
	sb.WriteString(" ")

	if m.progress != nil {
		fmt.Fprintf(&sb, "#%d", m.progress.Iteration)

		// Phase hint (active tool is shown in progress block, skip here to avoid duplication)
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

	// §18 Token usage display
	if m.progress != nil && m.progress.TokenUsage != nil && m.progress.TokenUsage.TotalTokens > 0 {
		tu := m.progress.TokenUsage
		// §20 tokenStyle → s.TokenUsage
		sb.WriteString(" · ")
		sb.WriteString(s.TokenUsage.Render(formatTokenCount(tu)))
	}

	return sb.String()
}

// formatTokenCount Format token usage as compact string
func formatTokenCount(tu *CLITokenUsage) string {
	if tu.TotalTokens < 1000 {
		return fmt.Sprintf("tokens: %d", tu.TotalTokens)
	}
	parts := []string{}
	if tu.PromptTokens > 0 {
		parts = append(parts, fmt.Sprintf("in:%d", tu.PromptTokens))
	}
	if tu.CompletionTokens > 0 {
		parts = append(parts, fmt.Sprintf("out:%d", tu.CompletionTokens))
	}
	if len(parts) > 0 {
		return "tokens: " + strings.Join(parts, " ") + fmt.Sprintf(" = %d", tu.TotalTokens)
	}
	return fmt.Sprintf("tokens: %d", tu.TotalTokens)
}

// ---------------------------------------------------------------------------

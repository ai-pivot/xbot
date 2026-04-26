package channel

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"fmt"
	"image/color"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"xbot/clipanic"
)

// Update Handle message
func (m *cliModel) Update(msg tea.Msg) (model tea.Model, retCmd tea.Cmd) {
	defer clipanic.Recover("channel.cliModel.Update", msg, true)
	var cmds []tea.Cmd
	prevText := m.textarea.Value()
	wasTyping := m.typing

	// Pre-switch: async notifications + side effects + key checks
	if handled, mdl, c, extraCmds := m.handlePreSwitchMessages(msg); handled {
		return mdl, c
	} else if len(extraCmds) > 0 {
		cmds = append(cmds, extraCmds...)
	}

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		model, keyCmds, handled := m.handleKeyPress(msg, wasTyping)
		if handled {
			if cm, ok := model.(*cliModel); ok && !wasTyping && cm.typing && !cm.fastTickActive {
				keyCmds = append(keyCmds, tickCmd())
			}
			return model, tea.Batch(keyCmds...)
		}
	case tea.WindowSizeMsg:
		m.handleResize(msg.Width, msg.Height)
	case cliOutboundMsg:
		m.handleAgentMessage(msg.msg)
	case cliProgressMsg:
		m.handleProgressMsgCase(msg, &cmds)
	case cliProcessingMsg:
		m.handleProcessingMsgCase(msg)
	case cliConnStateMsg:
		m.connState = msg.state
	case cliTickMsg:
		cmds = append(cmds, m.handleTickMsg()...)
	case idleTickMsg:
		cmds = append(cmds, m.handleIdleTickMsg()...)
	case cliTempStatusClearMsg:
		m.tempStatus = ""
	case cliInjectedUserMsg:
		cmds = append(cmds, m.handleInjectedUserMsg(msg)...)
	case cliUpdateCheckMsg:
		m.handleUpdateCheck(msg)
	case tickerTickMsg:
		// Legacy: ticker is now driven by cliTickMsg. Drop stale messages.
	case typewriterTickMsg:
		cmds = append(cmds, m.handleTypewriterTickMsg()...)
	case splashTickMsg:
		return m.handleSplashTick(msg)
	case debugCaptureMsg:
		m.debugCaptureUI()
		cmds = append(cmds, m.debugCaptureTick())
	case splashDoneMsg:
		cmds = append(cmds, m.handleSplashDoneMsg()...)
	case suHistoryLoadMsg:
		cmds = append(cmds, m.handleSuHistoryLoad(msg)...)
	case cliToastMsg:
		cmds = append(cmds, m.handleToastMsg(msg)...)
	case cliHistoryLoadMsg:
		m.handleHistoryLoadMsgCase(msg)
	case cliHistoryReloadMsg:
		m.handleHistoryReload(msg)
	case cliToastClearMsg:
		cmds = append(cmds, m.handleToastClear(msg)...)
	case easterEggDoneMsg:
		m.handleEasterEggDoneMsgCase()
		return m, nil
	case easterEggMatrixTickMsg:
		return m, m.handleEasterEggMatrixTickCase(&cmds)
	case approvalRequestMsg:
		m.handleApprovalRequestMsg(msg)
		return m, nil
	}

	return m.handlePostSwitch(msg, prevText, wasTyping, cmds)
}

// autoExpandInput adjusts the viewport height to compensate for textarea height changes.
// With DynamicHeight enabled on the textarea, it manages its own height based on
// visual lines (including soft wraps from CJK characters). We just need to keep the
// viewport in sync.

func (m *cliModel) autoExpandInput() {
	// Bubble Tea textarea owns its own height when DynamicHeight is enabled.
	// Do NOT force SetHeight here: once the textarea reaches MaxHeight it switches
	// from grow mode to internal scrolling, and external SetHeight calls can break
	// newline insertion / cursor behavior exactly at that boundary.
	// We only keep the outer viewport in sync with the textarea's current height.
	expectedVP := m.layoutViewportHeight()
	currentVP := m.viewport.Height()
	if currentVP != expectedVP {
		wasAtBottom := m.viewport.AtBottom()
		m.viewport.SetHeight(expectedVP)
		if wasAtBottom {
			m.viewport.GotoBottom()
		}
	}
}

// layoutViewportHeight Calculate viewport's intended height, considering panel mode.
// Normal mode: titleBar(1) + status(1) + footer(1) + inputBox(taHeight+border)
// Panel mode: titleBar(1) + panel(border) + panelFooter(1) + toast(~1)
func (m *cliModel) layoutViewportHeight() int {
	height := m.height
	fixedLines := 3 // titleBar + status + footer

	if m.panelMode != "" {
		if m.panelMode == panelModeAskUser {
			// AskUser split layout: viewport stays visible above the panel.
			// Calculate panel content height, cap it, let viewport take the rest.
			askContent := m.viewAskUserPanel()
			askLines := countLines(askContent)
			panelBorder := 2                // PanelBox top + bottom border
			fixedLines := 2                 // titleBar + toast (no separate footer — hints are in-panel)
			maxPanelH := (m.height / 2) + 2 // panel gets at most ~half the screen
			minPanelH := askLines + panelBorder
			if minPanelH < 8 {
				minPanelH = 8
			}
			if minPanelH > maxPanelH {
				minPanelH = maxPanelH
			}
			viewportH := m.height - fixedLines - minPanelH
			if viewportH < 5 {
				viewportH = 5
				_ = m.height - fixedLines - viewportH // panel gets the rest
			}
			return viewportH
		}
		// Other panels: viewport shrinks to minimum, panel takes all space
		return 3
	}

	// Normal mode
	taBorder := 2 // top + bottom border
	// Calculate lines occupied by todoBar: title line(1) + one line per todo item
	todoLines := 0
	if len(m.todos) > 0 {
		todoLines = 1 + len(m.todos)
	}
	reservedLines := fixedLines + taBorder + m.textarea.Height() + todoLines
	// §20b Small terminal adaptation: dynamically reduce layout for very small windows
	if height < 12 {
		reservedLines = fixedLines + taBorder + 2 // min textarea
	}
	if height < 8 {
		reservedLines = 4 // ultra-compact: title + status + 1-line viewport + border
	}
	viewportHeight := height - reservedLines
	if viewportHeight < 3 {
		viewportHeight = 3
	}
	return viewportHeight
}

// relayoutViewport Recalculate and set viewport height (without rebuilding style cache).
// For dynamically adjusting layout when panel opens/closes, todos change.
// If user was at bottom before, continue following bottom after adjustment.
func (m *cliModel) relayoutViewport() {
	if m.width == 0 || m.height == 0 {
		return
	}
	wasAtBottom := m.viewport.AtBottom()
	m.viewport.SetHeight(m.layoutViewportHeight())
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
}

// handleResize Handle window size change
func (m *cliModel) handleResize(width, height int) {
	// Deduplicate: skip if size hasn't actually changed.
	// During resize drags, terminals (especially foot) may fire many
	// SIGWINCH signals with the same dimensions — each one triggers a
	// full O(N) rebuild of the message history.
	if width == m.width && height == m.height && m.ready {
		return
	}

	m.width = width
	m.height = height

	// §20 Rebuild style cache
	m.styles = buildStyles(width)

	m.viewport.SetWidth(width)
	m.viewport.SetHeight(m.layoutViewportHeight())

	// InputBox lipgloss style: Width(width-4) includes border(2) + padding(2).
	// Content area = width-4-2-2 = width-8. Textarea must match this.
	iw := width - 8
	if iw < 10 {
		iw = 10
	}
	iw = iw &^ 1 // round down to even for CJK
	m.textarea.SetWidth(iw)

	// Glamour word-wrap must match viewport width so that lines
	// don't get re-wrapped by lipgloss (which would lose the margin).
	if width > 4 {
		m.renderer = newGlamourRenderer(width - 4)
	}

	if !m.ready {
		m.ready = true
	}

	// §1 Incremental rendering: all caches invalidated after resize
	m.renderCacheValid = false
	m.lastViewportContent = "" // force setViewportContent to re-wrap
	for i := range m.messages {
		m.messages[i].dirty = true
	}

	// Update content (preserve user scroll position)
	wasAtBottom := m.viewport.AtBottom()
	m.updateViewportContent()
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
}

// panelWidth returns a width suitable for panel textareas,
// adapting to the current terminal width (with sensible bounds).
func (m *cliModel) panelWidth(want int) int {
	maxW := m.width - 8 // room for panel border + padding
	if want > maxW {
		return maxW
	}
	if want < 30 {
		return 30
	}
	return want
}

// truncateCompHint truncates a styled completion hint string to fit within
// maxW columns. It uses lipgloss.Width for ANSI-aware measurement and
// removes trailing items (from the last " · " separator backwards) until
// it fits, appending "…" to indicate truncation.
func truncateCompHint(hint string, maxW int) string {
	if maxW <= 0 || lipgloss.Width(hint) <= maxW {
		return hint
	}
	sep := " · "
	for {
		idx := strings.LastIndex(hint, sep)
		if idx < 0 {
			break
		}
		candidate := hint[:idx]
		if lipgloss.Width(candidate+"…") <= maxW {
			return candidate + "…"
		}
		hint = candidate
	}
	// Fallback: return as-is (should rarely happen; each item is short).
	return hint
}

// renderCompletionsHint returns the dynamic border color and completions hint string
// based on the current input content (slash commands, @ file references, etc.).
func (m *cliModel) renderCompletionsHint(inputValue string) (borderColor color.Color, hint string) {
	borderColor = lipgloss.Color(currentTheme.Accent)

	if strings.HasPrefix(inputValue, "!") {
		borderColor = lipgloss.Color(currentTheme.Error)
		return
	}

	if strings.HasPrefix(inputValue, "/") {
		borderColor = lipgloss.Color(currentTheme.Success)
		if len(m.completions) > 0 {
			parts := make([]string, len(m.completions))
			for i, c := range m.completions {
				if i == m.compIdx {
					parts[i] = m.styles.CompSelected.Render(c)
				} else {
					parts[i] = m.styles.CompItem.Render(c)
				}
			}
			hint = truncateCompHint(m.styles.CompHint.Render(strings.Join(parts, " · ")), m.width)
		} else {
			var matches []string
			for _, cmd := range cliCommands {
				if strings.HasPrefix(cmd, inputValue) {
					matches = append(matches, cmd)
				}
			}
			if len(matches) > 0 {
				hint = truncateCompHint(m.styles.CompHintBorder.Render("[Tab] "+strings.Join(matches, " · ")), m.width)
			}
		}
		return
	}

	// §20c @ file reference completion (with directory/file icon distinction + truncation)
	rawInput := m.textarea.Value()
	if ok, _ := detectAtPrefix(rawInput); ok {
		borderColor = lipgloss.Color(currentTheme.Info)
		if len(m.fileCompletions) > 0 {
			parts := make([]string, len(m.fileCompletions))
			for i, c := range m.fileCompletions {
				base := filepath.Base(c)
				dir := isDir(c)
				if dir {
					base += "/"
				}
				// Truncate long filenames
				if utf8.RuneCountInString(base) > fileCompMaxNameRunes {
					runes := []rune(base)
					base = string(runes[:fileCompTruncateAt]) + "…"
				}
				icon := "📄 "
				if dir {
					icon = "📁 "
				}
				display := icon + base
				if i == m.fileCompIdx {
					parts[i] = m.styles.FileCompSel.Render(display)
				} else {
					parts[i] = m.styles.FileCompFile.Render(display)
				}
			}
			hint = m.styles.TextMutedSt.Padding(0, 1).
				Render("[Tab] " + strings.Join(parts, " · "))
		} else {
			hint = m.styles.TextMutedSt.Padding(0, 1).
				Render(m.locale.TabNoMatch)
		}
		return
	}
	return
}

// handleRunnerStatusMsg Handle runner connection status change
func (m *cliModel) handleRunnerStatusMsg(msg runnerStatusMsg) tea.Cmd {
	if msg.err != nil {
		m.showTempStatus(fmt.Sprintf("%s: %v", m.locale.RunnerConnectFailed, msg.err))
		return m.clearTempStatusCmd()
	}
	if msg.status == RunnerConnected {
		m.showTempStatus(m.locale.RunnerConnectSuccess)
		return m.clearTempStatusCmd()
	}
	return nil
}

// handleSwitchLLMDone processes the result of an async LLM subscription switch.
func (m *cliModel) handleSwitchLLMDone(done cliSwitchLLMDoneMsg) tea.Cmd {
	returnToSettings := m.quickSwitchReturnToPanel
	m.quickSwitchReturnToPanel = false
	if done.err != nil {
		m.showTempStatus(fmt.Sprintf("Failed to switch LLM: %v", done.err))
	} else if done.mgr != nil {
		if err := done.mgr.SetDefault(done.subID, m.chatID); err != nil {
			m.showTempStatus(fmt.Sprintf("LLM switched but failed to save: %v", err))
		} else {
			m.subGeneration++ // subscription actually changed
			m.showTempStatus(fmt.Sprintf("Switched to: %s (%s)", done.subName, done.subModel))
			// Refresh values cache so GetCurrentValues() reflects the new subscription.
			if m.channel != nil && m.channel.config.RefreshValuesCache != nil {
				m.channel.config.RefreshValuesCache()
			}
		}
		// Update cached model name directly from the switch result
		// (same pattern as model-switch case — avoids stale config/RPC reads)
		if done.subModel != "" {
			m.cachedModelName = done.subModel
			// Always refresh modelCount after subscription switch
			// so status bar shows correct count and [Ctrl+N] hint.
			if m.channel.modelLister != nil {
				m.modelCount = len(m.channel.modelLister.ListModels())
			}
		} else {
			// Subscription has no model configured — clear stale model name.
			m.cachedModelName = ""
		}
	}
	// If we came from the settings panel, re-open it so the user can continue editing
	if returnToSettings {
		m.openSettingsFromQuickSwitch()
	}
	// Drain pendingCmds (e.g. showTempStatus timer) — must not return nil cmds
	if len(m.pendingCmds) > 0 {
		cmd := tea.Batch(m.pendingCmds...)
		m.pendingCmds = nil
		return cmd
	}
	return nil
}

// handleCtrlC implements unified Ctrl+C handling for all states.
func (m *cliModel) handleCtrlC() {
	// 1. Close all overlay/panel
	if m.quickSwitchMode != "" {
		m.quickSwitchMode = ""
	}
	if m.rewindMode {
		m.closeRewindPanel()
	}
	if m.panelMode != "" {
		m.closePanel()
	}
	if m.searchMode {
		m.exitSearch()
	}
	// 2. Cancel editing of queued message
	if m.queueEditing {
		m.queueEditing = false
		m.queueEditBuf = ""
		m.textarea.SetValue("")
	}
	// 3. If agent is processing:
	//    - Has queued messages: only clear queue, don't send cancel (need another Ctrl+C to cancel)
	//    - No queued messages: send cancel
	if m.typing {
		queueLen := len(m.messageQueue)
		if queueLen > 0 {
			m.messageQueue = nil
			m.showSystemMsg(fmt.Sprintf(m.locale.QueueCleared, queueLen), feedbackInfo)
		} else {
			m.sendCancel()
		}
		return
	}
	// 4. Idle state: clear input
	if m.textarea.Value() != "" {
		m.textarea.Reset()
		m.inputHistoryIdx = -1
		m.inputDraft = ""
		m.autoExpandInput()
	}
}

// handleSearchKey handles key events when search mode is active.
// Returns (cmd, true) if the key was handled, (nil, false) otherwise.
func (m *cliModel) handleSearchKey(key tea.KeyPressMsg) (tea.Cmd, bool) {
	switch {
	case m.searchEditing:
		switch key.String() {
		case "enter":
			m.executeSearch()
			return nil, true
		case "esc":
			m.exitSearch()
			return nil, true
		}
		var cmd tea.Cmd
		m.searchTI, cmd = m.searchTI.Update(key)
		return cmd, true
	default:
		switch key.String() {
		case "n":
			if len(m.searchResults) > 0 {
				next := m.searchIdx + 1
				if next >= len(m.searchResults) {
					next = 0
				}
				m.jumpToSearchResult(next)
				m.renderCacheValid = false
				m.updateViewportContent()
			}
			return nil, true
		case "N":
			if len(m.searchResults) > 0 {
				prev := m.searchIdx - 1
				if prev < 0 {
					prev = len(m.searchResults) - 1
				}
				m.jumpToSearchResult(prev)
				m.renderCacheValid = false
				m.updateViewportContent()
			}
			return nil, true
		case "esc":
			m.exitSearch()
			return nil, true
		}
		return nil, true
	}
}

// handleTickMsg processes periodic tick events for progress updates, queue flushing,
// and tick chain management.
func (m *cliModel) handleTickMsg() []tea.Cmd {
	var cmds []tea.Cmd
	// Always refresh bg task count on tick so status bar updates immediately
	// when a bg task completes (even when no progress event is coming)
	if m.bgTaskCountFn != nil {
		prev := m.bgTaskCount
		m.bgTaskCount = m.bgTaskCountFn()
		// Force re-render when count changes (e.g. task killed in panel)
		if m.bgTaskCount != prev {
			m.renderCacheValid = false
		}
	}
	// Refresh agent count on tick
	if m.agentCountFn != nil {
		prev := m.agentCount
		m.agentCount = m.agentCountFn()
		if m.agentCount != prev {
			m.renderCacheValid = false
		}
	}
	// Schedule next tick when agent is active or bg tasks are running.
	// IMPORTANT: only emit ONE tickCmd to prevent exponential message growth
	// (two tickCmd() would double the message count every 100ms → CPU explosion).
	busy := m.typing || m.progress != nil
	if (m.bgTaskCountFn != nil && m.bgTaskCount > 0) || (m.agentCountFn != nil && m.agentCount > 0) || busy {
		m.fastTickActive = true
		cmds = append(cmds, tickCmd())
	} else if m.needFlushQueue && len(m.messageQueue) > 0 {
		m.fastTickActive = true
		// Pending queue flush — use fast tick so the queued message
		// is sent promptly (not waiting 3s for idleTickCmd).
		cmds = append(cmds, tickCmd())
	} else {
		// Transition to idle: start low-frequency tick for placeholder rotation
		m.fastTickActive = false
		cmds = append(cmds, idleTickCmd())
	}
	if busy {
		// Advance spinner frame on every tick so the animation stays in sync
		// with elapsed time display. Previously driven by a separate tickerTickMsg
		// chain that could break when m.progress briefly went nil.
		m.ticker.tick()
		// Typewriter is now driven by its own typewriterTickMsg chain (50ms).
		// Start the typewriter chain if there's stream or reasoning content to reveal.
		hasStreamContent := m.progress != nil && m.progress.StreamContent != "" && m.twVisible < len([]rune(m.progress.StreamContent))
		hasReasoningContent := m.progress != nil && m.progress.ReasoningStreamContent != "" && m.rwVisible < len([]rune(m.progress.ReasoningStreamContent))
		if hasStreamContent || hasReasoningContent {
			if !m.typewriterTickActive {
				m.typewriterTickActive = true
				cmds = append(cmds, typewriterTickCmd())
			}
		}
		m.updateViewportContent()
	} else {
		// Not busy: stop typewriter chain
		m.typewriterTickActive = false
		// Still refresh viewport if messages were added/changed (e.g. assistant
		// reply arrived via handleAgentMessage after PhaseDone cleared progress).
		if !m.renderCacheValid {
			m.updateViewportContent()
		}
	}

	// §Q Flush message queue on tick (not in cliProgressMsg/cliOutboundMsg).
	// This ensures the previous reply is already appended to m.messages before
	// the queued message gets sent, producing correct order: msg1, reply1, msg2.
	// Guard: only flush when NOT typing (previous turn fully complete).
	if m.needFlushQueue && !m.typing && len(m.messageQueue) > 0 {
		m.needFlushQueue = false
		m.flushMessageQueue()
		// Always break after flush so the tickCmd queued by startAgentTurn()
		// (inside sendMessageFromQueue → sendMessage) gets picked up in cmds.
		// Note: caller must not append more cmds after this
		return cmds
	}
	return cmds
}

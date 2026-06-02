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
	log "xbot/logger"
	"xbot/protocol"
)

// Update 处理消息
func (m *cliModel) Update(msg tea.Msg) (model tea.Model, retCmd tea.Cmd) {
	defer clipanic.Recover("channel.cliModel.Update", msg, true)
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	// §8 Tab 补全：记录输入内容变化以重置补全状态
	prevText := m.textarea.Value()

	wasTyping := m.typing

	// Async settings save completed — apply theme/locale/viewport changes
	if saved, ok := msg.(cliSettingsSavedMsg); ok {
		cmd := m.handleSettingsSavedMsg(saved)
		return m, cmd
	}

	// Async subscription switch completed
	if done, ok := msg.(cliSwitchLLMDoneMsg); ok {
		model, cmd, handled := m.handleSwitchLLMDoneMsg(done)
		if handled {
			return model, cmd
		}
	}

	// Runner status change notification
	if rsm, ok := msg.(runnerStatusMsg); ok {
		cmd := m.handleRunnerStatusMsg(rsm)
		return m, cmd
	}

	// AI-triggered TUI session control (from tui_control tool via program.Send — same path as mouse clicks)
	if sc, ok := msg.(cliSessionControlMsg); ok {
		retCmd := m.handleSessionControlMsg(sc)
		return m, retCmd
	}

	// 主题变更通知：重建样式缓存 + glamour 渲染器
	select {
	case <-themeChangeCh:
		m.applyThemeAndRebuild(currentThemeName)
		m.updateViewportContent()
	default:
	}

	// Terminal color profile detected by BubbleTea.
	// Cache it and rebuild styles so muted/dim colors stay visible on
	// low-color terminals (e.g. Linux console with ANSI 16-color).
	if cpMsg, ok := msg.(tea.ColorProfileMsg); ok {
		prev := terminalProfile
		terminalProfile = cpMsg.Profile
		if terminalProfile != prev {
			m.styles = buildStyles(m.width)
			m.updateViewportContent()
		}
		return m, nil
	}

	// Model list load error notification from LLM goroutines
	select {
	case err := <-modelsLoadErrorCh:
		m.showTempStatus(fmt.Sprintf("Model list load failed: %v", err))
		_ = m.clearTempStatusCmd()
	default:
	}

	// Drain pending cmds queued by helpers (e.g. showTempStatus).
	// Append to cmds so they get batched with any cmds produced by the
	// switch cases below — do NOT return early here, or the tick chain
	// breaks (e.g. a pending tempStatus clear would prevent cliTickMsg
	// from emitting the next tickCmd).
	if len(m.pendingCmds) > 0 {
		cmds = append(cmds, m.pendingCmds...)
		m.pendingCmds = nil
	}

	// i18n: locale 变更通知
	select {
	case <-localeChangeCh:
		m.locale = GetLocale(currentLocaleLang)
		m.renderCacheValid = false
		for i := range m.messages {
			m.messages[i].dirty = true
		}
		m.updatePlaceholder()
		m.updateViewportContent()
	default:
	}

	// Ctrl+Z: 紧急退出（无论什么状态，包括 panel/typing/idle）
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+z" {
		m.showSystemMsg(m.locale.EmergencyQuitHint, feedbackWarning)
		return m, tea.Quit
	}

	// DEBUG: log all KeyPressMsg to trace ctrl+c handling
	if key, ok := msg.(tea.KeyPressMsg); ok {
		log.WithFields(log.Fields{"str": key.String(), "code": key.Code, "mod": key.Mod}).Debug("DEBUG keypress")
	}

	// Ctrl+C: 统一处理，位于所有其他 key handler 之前。
	// 这是唯一的 Ctrl+C 处理点——任何其他地方不得再拦截 Ctrl+C。
	// 保证无论什么状态（typing/idle/panel/queue/editing），Ctrl+C 始终有效。
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+c" {
		model, cmd, handled := m.handleCtrlC()
		if handled {
			return model, cmd
		}
	}

	// Remote reconnect overlay: block all interaction when disconnected.
	// Only quit keys (Ctrl+C, Ctrl+Z) pass through — they're handled above.
	if m.remoteMode && m.connState != "connected" && m.connState != "" {
		// Swallow all key presses; user can only Ctrl+C (handled above) or Ctrl+Z.
		return m, nil
	}

	// §23 Command palette overlay: highest priority (above quick switch).
	// When palette is open it intercepts all keys.
	if key, ok := msg.(tea.KeyPressMsg); ok {
		if handled, cmd := m.handlePaletteKey(key); handled {
			return m, cmd
		}
	}

	// §15 Quick switch overlay: highest priority (above panelMode).
	// This ensures ESC in quick switch closes the overlay, not the panel behind it.
	if key, ok := msg.(tea.KeyPressMsg); ok {
		if handled, cmd := m.handleQuickSwitchKey(key); handled {
			return m, cmd
		}
		// §9 Rewind overlay: same priority as quick switch.
		if handled, cmd := m.handleRewindKey(key); handled {
			return m, cmd
		}
	}

	// §12 Panel mode: intercept all key events when panel is active
	// NOTE: Ctrl+C is handled at the top of Update() — never intercept it here.
	if key, ok := msg.(tea.KeyPressMsg); ok && m.panelMode != "" {
		handled, newModel, cmd := m.updatePanel(key)
		if handled {
			return newModel, cmd
		}
	}
	// §12b Panel mode: intercept paste events — PasteMsg is not KeyPressMsg,
	// so it bypasses the above panel interceptor and would be captured by the
	// main textarea below. Forward it to the panel's internal textarea instead.
	if paste, ok := msg.(tea.PasteMsg); ok && m.panelMode != "" {
		var cmd tea.Cmd
		switch m.panelMode {
		case "askuser":
			// Check if current tab has options (use textinput) or free input (use textarea)
			if m.panelTab >= 0 && m.panelTab < len(m.panelItems) && len(m.panelItems[m.panelTab].Options) > 0 {
				m.panelOtherTI, cmd = m.panelOtherTI.Update(paste)
			} else {
				m.autoExpandAskTA()
				m.panelAnswerTA, cmd = m.panelAnswerTA.Update(paste)
			}
		case "settings":
			if m.panelEdit {
				m.panelEditTA, cmd = m.panelEditTA.Update(paste)
			}
		case "wizard":
			if m.wizardStep == wizardAPIKey {
				m.wizardKeyTI, cmd = m.wizardKeyTI.Update(paste)
			}
		}
		return m, cmd
	}
	// §21 搜索模式拦截
	if key, ok := msg.(tea.KeyPressMsg); ok && m.searchMode {
		model, cmd, handled := m.handleSearchKey(key)
		if handled {
			return model, cmd
		}
	}

	// Home/End 跳顶部/底部
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "home":
			m.viewport.GotoTop()
			return m, nil
		case "end":
			m.viewport.GotoBottom()
			m.newContentHint = false
			m.userScrolledUp = false
			return m, nil
		}
	}

	// Ctrl+Enter 换行（终端发送的 raw sequence 不统一，需手动检测）
	if isCtrlEnter(msg) {
		m.textarea.InsertString("\n")
		m.autoExpandInput()
		return m, nil
	}
	// Ctrl+J 换行 — 直接 InsertString 绕过 textarea 内部 atContentLimit 检查，
	// 否则到达 MaxHeight 后 textarea 的 InsertNewline keymap 会静默丢弃换行。
	if isCtrlJ(msg) {
		m.textarea.InsertString("\n")
		m.autoExpandInput()
		return m, nil
	}
	// Ctrl+O 切换 tool summary 展开/折叠（CSI u 协议兼容层，kitty/Ghostty 等）
	if isCtrlO(msg) {
		m.toggleToolSummary()
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.MouseMsg:
		// Block mouse events when remote connection is lost.
		if m.remoteMode && m.connState != "connected" && m.connState != "" {
			return m, nil
		}
		handled, newModel, cmd := m.handleMouseMsg(msg)
		if handled {
			if _, ok := newModel.(*cliModel); ok {
				m.autoExpandInput()
			}
			return newModel, cmd
		}
		// Unhandled mouse events (e.g., wheel in viewport area)
		// will be forwarded to viewport/textarea at the end of Update().

	case tea.KeyPressMsg:
		if m.settingsSaving {
			break // block input while settings are being saved
		}
		model, keyCmds, handled := m.handleKeyPress(msg, wasTyping)
		if handled {
			// wasTyping guard: ensure tick chain starts on idle→typing transition.
			// handleKeyPress may call sendMessage→startAgentTurn which sets typing=true,
			// but the early return below skips the wasTyping guard at the end of Update.
			return model, tea.Batch(keyCmds...)
		}
		// Unhandled key: fall through to post-switch processing

	case tea.WindowSizeMsg:
		// 窗口大小变化 - 动态调整布局
		m.handleResize(msg.Width, msg.Height)

	case cliOutboundMsg:
		// 收到 agent 回复
		m.handleAgentMessage(msg.msg)
		// Queue flush is handled in cliTickMsg to ensure correct message ordering
		// (reply must be appended before queued message is sent).

	case cliProgressMsg:
		m.handleProgressMsg(msg)

	case cliSessionStateMsg:
		m.handleSessionStateMsg(msg)

	case cliProcessingMsg:
		if msg.processing && !m.typing {
			m.startAgentTurn()
		} else if !msg.processing && m.typing {
			m.endAgentTurn(m.agentTurnID)
		}
		// NOTE: do NOT flush queue here even if needFlushQueue is true!
		// PhaseDone can arrive before cliOutboundMsg (the reply text). If we

	case cliConnStateMsg:
		m.connState = msg.state
		if msg.state == "connected" {
			m.reconnectFrame = 0
		}
	// flush here, the queued message gets appended BEFORE the reply,
	// producing wrong order: msg1, msg2, reply1 instead of msg1, reply1, msg2.
	// Flush is handled in cliTickMsg instead (next tick after typing=false).

	case cliTickMsg:
		cmds = append(cmds, m.handleTickMsg()...)

	case cliTempStatusClearMsg:
		m.tempStatus = ""

	case cliInjectedUserMsg:
		// Agent injected a user message (e.g. bg task completion notification).
		cmds = append(cmds, m.handleInjectedUserMsg(msg)...)
	case cliUpdateCheckMsg:
		m.handleUpdateCheck(msg)

	case cliPluginReloadResultMsg:
		m.pluginReloading = false
		if msg.err != nil {
			m.showSystemMsg(fmt.Sprintf("❌ Failed to reload plugin %s: %v", msg.pluginID, msg.err), feedbackError)
		} else {
			m.showSystemMsg(fmt.Sprintf("✅ Plugin %s reloaded successfully", msg.pluginID), feedbackInfo)
		}
		m.updateViewportContent()

	case cliPluginReloadAllResultMsg:
		m.pluginReloading = false
		if msg.err != nil {
			m.showSystemMsg(fmt.Sprintf("❌ Failed to reload all plugins: %v", msg.err), feedbackError)
		} else {
			m.showSystemMsg("✅ All plugins reloaded successfully", feedbackInfo)
		}
		m.updateViewportContent()

	case cliPluginHealthResultMsg:
		results := msg.results
		if len(results) == 0 {
			m.showSystemMsg("No active plugins to check.", feedbackInfo)
		} else {
			var sb strings.Builder
			sb.WriteString(m.styles.ToolHeader.Render("🔍 Plugin Health"))
			sb.WriteString("\n\n")

			// Show errors first, then healthy
			for id, err := range results {
				if err != nil {
					icon := pluginStateIcon("error")
					line := fmt.Sprintf("  %-20s %s %s\n", id, icon,
						m.styles.PluginError.Render(err.Error()))
					sb.WriteString(line)
				}
			}
			for id, err := range results {
				if err == nil {
					icon := pluginStateIcon("active")
					line := fmt.Sprintf("  %-20s %s %s\n", id, icon,
						m.styles.PluginActive.Render("healthy"))
					sb.WriteString(line)
				}
			}
			m.appendSystemStyled(sb.String())
		}
		m.updateViewportContent()

	case cliPluginInstallResultMsg:
		m.pluginReloading = false
		if msg.err != nil {
			m.showSystemMsg(fmt.Sprintf("❌ Failed to install plugin: %v", msg.err), feedbackError)
		} else {
			m.showSystemMsg(fmt.Sprintf("✅ Plugin %s installed successfully at %s", msg.pluginID, msg.pluginDir), feedbackInfo)
		}
		m.updateViewportContent()

	case cliPluginUninstallResultMsg:
		m.pluginReloading = false
		if msg.err != nil {
			m.showSystemMsg(fmt.Sprintf("❌ Failed to uninstall plugin %s: %v", msg.pluginID, msg.err), feedbackError)
		} else {
			m.showSystemMsg(fmt.Sprintf("✅ Plugin %s uninstalled successfully", msg.pluginID), feedbackInfo)
		}
		m.updateViewportContent()

	case typewriterTickMsg:
		cmds = append(cmds, m.handleTypewriterTick()...)

	case debugCaptureMsg:
		// --debug: dump current TUI view to file every second
		m.debugCaptureUI()
		cmds = append(cmds, m.debugCaptureTick())

	case splashDoneMsg:
		cmds = append(cmds, m.handleSplashDone()...)

	case suHistoryLoadMsg:
		cmds = append(cmds, m.handleSuHistoryLoad(msg)...)

	case cliToastMsg:
		cmds = append(cmds, m.handleToastMsg(msg)...)

	case cliHistoryLoadMsg:
		m.handleHistoryLoad(msg)

	case cliHistoryReloadMsg:
		m.handleHistoryReload(msg)

	case cliTokenRefreshMsg:
		// Session guard: reject stale refresh from a different session.
		// After compression, the old session's async goroutine may push a
		// cliTokenRefreshMsg that arrives after the user has switched to
		// another session. Without this guard, the stale data overwrites
		// the current session's token usage, causing the context bar to
		// "jump back" to the old compressed count.
		if msg.channelName != m.channelName || msg.chatID != m.chatID {
			break
		}
		if msg.tokenPrompt > 0 || msg.tokenCompletion > 0 {
			// Only accept the DB value if we don't have a more recent value
			// from engine progress events. After compression, the async
			// refreshTokenStateAfterReload goroutine may read a stale value
			// from DB (the compressed token count) while engine has already
			// pushed the real post-LLM-call value via progress events.
			// Accept when: nil (no data yet) or DB value is HIGHER (genuinely newer).
			if m.lastTokenUsage == nil || msg.tokenPrompt > m.lastTokenUsage.PromptTokens {
				m.lastTokenUsage = &protocol.TokenUsage{
					PromptTokens:     msg.tokenPrompt,
					CompletionTokens: msg.tokenCompletion,
					TotalTokens:      msg.tokenPrompt + msg.tokenCompletion,
				}
			}
		}

	case cliToastClearMsg:
		cmds = append(cmds, m.handleToastClear(msg)...)

	case cliWidgetUpdateMsg:
		// Widget content changed — invalidate render cache and relayout viewport.
		// Info bar appearing/disappearing changes the number of reserved lines.
		m.renderCacheValid = false
		m.relayoutViewport()

	case cliModelDiscoverMsg:
		if cmd := m.handleModelDiscoverMsg(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}

	case easterEggDoneMsg:
		// 🥚 彩蛋关闭（按任意键触发）
		m.dismissEasterEgg()
		m.renderCacheValid = false
		m.updateViewportContent()
		return m, nil

	case easterEggMatrixTickMsg:
		// 🥚 Matrix 代码雨动画帧推进
		if m.easterEgg == easterEggMatrix {
			m.tickMatrix()
			cmds = append(cmds, matrixTickCmd())
		}
		return m, tea.Batch(cmds...)

	case approvalRequestMsg:
		return m.handleApprovalRequest(msg)
	}

	// Idle→typing transition guard: if typing just started (e.g. from
	// handleInjectedUserMsg or cliProcessingMsg), ensure the tick chain is running.
	// This is the universal safety net — callers that can return cmds do so
	// directly, but this catches any missed transitions.

	// Track viewport scroll position before update to detect user-initiated scrolling.
	preViewportYOffset := m.viewport.YOffset()

	// 更新 viewport
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	// Detect user scroll intent from viewport.Update (mouse wheel, keyboard
	// scroll, PageUp/Down, etc.). This is the ONLY place userScrolledUp should
	// be set to true — never from programmatic scroll adjustments.
	postViewportYOffset := m.viewport.YOffset()
	if postViewportYOffset != preViewportYOffset {
		if m.viewport.AtBottom() {
			// User scrolled to bottom (via any means) → clear intent flag
			m.userScrolledUp = false
			m.newContentHint = false
		} else if postViewportYOffset < preViewportYOffset {
			// User scrolled up → set intent flag
			m.userScrolledUp = true
		}
		// Scrolling down but not yet at bottom: keep userScrolledUp as-is
	}

	// 更新 textarea
	// Skip WindowSizeMsg: handleResize already calls SetWidth() which
	// triggers recalculateHeight(). Forwarding the resize message to
	// textarea.Update() would redundantly recalculate + render view().
	if _, ok := msg.(tea.WindowSizeMsg); !ok {
		m.textarea, cmd = m.textarea.Update(msg)
		cmds = append(cmds, cmd)
	}

	// §8 Tab 补全：输入内容变化时重置补全状态
	newVal := m.textarea.Value()
	if newVal != prevText {
		m.completions = nil
		m.compIdx = 0
		m.fileCompActive = false
		// 用户手动输入：根据当前 @ prefix 重新 glob
		// 但如果 fileCompActive（Tab 循环中），不重新 glob
		if !m.fileCompActive {
			if ok, prefix := detectAtPrefix(newVal); ok {
				m.populateFileCompletions(prefix)
			} else {
				m.fileCompletions = nil
				m.fileCompIdx = 0
			}
		}
	}

	// 检查是否需要退出
	if m.shouldQuit {
		return m, tea.Quit
	}

	m.autoExpandInput()

	return m, tea.Batch(cmds...)
}

// autoExpandInput adjusts the viewport height to compensate for textarea height changes.
// With DynamicHeight enabled on the textarea, it manages its own height based on
// visual lines (including soft wraps from CJK characters). We just need to keep the
// viewport in sync.
const (
	minTaHeight = 1
	maxTaHeight = 10
)

func (m *cliModel) autoExpandInput() {
	// Bubble Tea textarea owns its own height when DynamicHeight is enabled.
	// Do NOT force SetHeight here: once the textarea reaches MaxHeight it switches
	// from grow mode to internal scrolling, and external SetHeight calls can break
	// newline insertion / cursor behavior exactly at that boundary.
	// We only keep the outer viewport in sync with the textarea's current height.
	expectedVP := m.layoutViewportHeight()
	currentVP := m.viewport.Height()
	if currentVP != expectedVP {
		shouldFollowBottom := !m.userScrolledUp
		oldYOffset := m.viewport.YOffset()
		m.viewport.SetHeight(expectedVP)
		if shouldFollowBottom {
			m.viewport.GotoBottom()
		} else {
			// Height changed while user was scrolled up. Clamp yOffset to
			// the new maxYOffset so the next setViewportContent doesn't
			// detect "at bottom" (via yOffset >= maxYOffset) and force-scroll.
			m.viewport.SetYOffset(oldYOffset)
		}
	}
}

// layoutViewportHeight 计算 viewport 应有的高度，考虑 panel 模式。
// 正常模式：titleBar(1) + status(1) + footer(1) + inputBox(taHeight+border)
// Panel 模式：titleBar(1) + panel(border) + panelFooter(1) + toast(~1)
func (m *cliModel) layoutViewportHeight() int {
	height := m.height
	fixedLines := 3 // titleBar + status + footer

	if m.panelMode != "" {
		if m.panelMode == "askuser" {
			// AskUser split layout: viewport stays visible above the panel.
			// Calculate panel content height, cap it, let viewport take the rest.
			askContent := m.viewAskUserPanel()
			askLines := strings.Count(askContent, "\n") + 1
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

	// 正常模式
	taBorder := 2 // top + bottom border
	// 计算 todoBar 占用的行数：标题行(1) + 每个 todo item 一行
	// 当 sidebar 展开时，todo 在 sidebar 中渲染，不占用主视图空间
	todoLines := 0
	if len(m.todos) > 0 && !m.sidebarShown() {
		todoLines = 1 + len(m.todos)
	}
	// Info bar: always reserve 1 line. renderInfoBar() always produces
	// output (at minimum the workspace indicator), so the viewport must
	// account for it.
	infoBarLines := 1
	reservedLines := fixedLines + taBorder + m.textarea.Height() + todoLines + infoBarLines
	// §20b 小终端适配：极小窗口下动态缩减布局
	if height < 12 {
		reservedLines = fixedLines + taBorder + 2 // min textarea
	}
	if height < 8 {
		reservedLines = 4
	}
	if height < 5 {
		reservedLines = 4
	}
	viewportHeight := height - reservedLines
	if viewportHeight < 3 {
		viewportHeight = 3
	}
	return viewportHeight
}

// relayoutViewport 重新计算并设置 viewport 宽高、textarea 和 glamour。
// 用于 panel 打开/关闭、todo 增减、sidebar toggle 时动态调整布局。
// 如果用户之前在底部，调整后继续保持跟随底部。
//
// Optimized: only invalidates render caches when viewport width actually
// changes. Height-only changes (e.g. todo bar appearing/disappearing,
// panel open/close) just resize the viewport without rebuilding all messages.
// This avoids O(N) fullRebuild on every endAgentTurn / handleProgressDone.
func (m *cliModel) relayoutViewport() {
	if m.width == 0 || m.height == 0 {
		return
	}

	cw := m.chatWidth()
	oldWidth := m.viewport.Width()
	oldHeight := m.viewport.Height()
	oldYOffset := m.viewport.YOffset()

	m.viewport.SetWidth(cw)
	m.viewport.SetHeight(m.layoutViewportHeight())

	// Textarea width matches input box content area
	iw := cw - 8
	if iw < 10 {
		iw = 10
	}
	iw = iw &^ 1
	m.textarea.SetWidth(iw)

	widthChanged := cw != oldWidth
	heightChanged := m.viewport.Height() != oldHeight

	// Only invalidate render caches when width changes.
	// Height-only changes don't affect message rendering — just viewport scrolling.
	if widthChanged {
		// Invalidate render caches so content re-wraps at new width
		m.renderCacheValid = false
		m.lastViewportContent = ""

		// Glamour word-wrap matches viewport
		if cw > 4 {
			m.renderer = newGlamourRenderer(cw - 4)
		}
		m.cachedWrappedHistory = ""
		m.cachedWrappedHistoryRaw = ""
		m.cachedWrappedHistoryWidth = 0
		m.cachedHistoryMaxWidth = 0
		m.cachedHistoryLines = nil
		for i := range m.messages {
			m.messages[i].dirty = true
		}
	}

	// Use userScrolledUp instead of AtBottom() to avoid false-positive
	// when height changes cause maxYOffset to decrease below yOffset.
	shouldFollowBottom := !m.userScrolledUp
	m.updateViewportContent()
	if shouldFollowBottom {
		m.viewport.GotoBottom()
	} else if heightChanged {
		// Height changed while user was scrolled up. Restore relative
		// scroll position to prevent jarring jumps.
		m.viewport.SetYOffset(oldYOffset)
	}
}

// handleResize 处理窗口大小变化
func (m *cliModel) handleResize(width, height int) {
	// Deduplicate: skip if size hasn't actually changed.
	if width == m.width && height == m.height && m.ready {
		return
	}

	// Mark ready BEFORE any rendering operations. During the first resize,
	// View() may be called while this function is still processing; if ready
	// is false, View() shows a blank loading screen. Setting ready early
	// ensures the first render always shows the proper UI, not a blank flash.
	if !m.ready {
		m.ready = true
	}

	m.width = width
	m.height = height
	m.invalidateLayoutCache()

	// §20 重建样式缓存
	m.styles = buildStyles(width)
	// Invalidate again after style rebuild (sidebar styles may have changed)
	m.cachedSidebarRenderedWidth = 0

	// Refresh widget render function with new styles and re-render all widgets
	if m.widgetRegistry != nil {
		m.widgetRegistry.SetDefaultRenderFn(buildWidgetRenderFn(m.styles))
		m.widgetRegistry.RefreshAllWidgets(width, nil)
	}

	m.relayoutViewport()
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
			hint = truncateCompHint(m.styles.CompHint.Render(strings.Join(parts, " · ")), m.chatWidth())
		} else {
			var matches []string
			for _, cmd := range cliCommands {
				if strings.HasPrefix(cmd, inputValue) {
					matches = append(matches, cmd)
				}
			}
			if len(matches) > 0 {
				hint = truncateCompHint(m.styles.CompHintBorder.Render("[Tab] "+strings.Join(matches, " · ")), m.chatWidth())
			}
		}
		return
	}

	// §20c @ 文件引用补全（带目录/文件图标区分 + 截断）
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
				// 截断过长文件名
				if utf8.RuneCountInString(base) > 20 {
					runes := []rune(base)
					base = string(runes[:18]) + "…"
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

// handleRunnerStatusMsg 处理 runner 连接状态变化
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

package cli

import (
	"fmt"
	"strings"
	"time"

	log "xbot/logger"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// handleKeyPress processes key press events in the main update loop.
// Returns (model, cmds, handled). If handled is true, the caller should return
// immediately; otherwise, post-switch processing (viewport/textarea update) should continue.
func (m *cliModel) handleKeyPress(msg tea.KeyPressMsg, wasTyping bool) (tea.Model, []tea.Cmd, bool) {

	// 🥚 彩蛋覆盖层激活时，按任意键退出（Ctrl+C 除外，已在上面处理）
	if m.easterEgg != easterEggNone {
		return m, []tea.Cmd{func() tea.Msg { return easterEggDoneMsg{} }}, true
	}

	// 🥚 Konami Code 彩蛋：监听方向键和字母键
	if m.easterEgg == easterEggNone {
		konamiKey := ""
		switch msg.Code {
		case tea.KeyUp:
			konamiKey = "up"
		case tea.KeyDown:
			konamiKey = "down"
		case tea.KeyLeft:
			konamiKey = "left"
		case tea.KeyRight:
			konamiKey = "right"
		}
		// 检测字母键 B 和 A
		if len(msg.Text) == 1 {
			switch msg.Text[0] {
			case 'b', 'B':
				konamiKey = "b"
			case 'a', 'A':
				konamiKey = "a"
			}
		}
		if konamiKey != "" && m.checkKonami(konamiKey) {
			// Konami Code 完整序列匹配！
			cmd := m.activateEasterEgg(easterEggKonami)
			return m, []tea.Cmd{cmd}, true
		}
	}

	// NOTE: Ctrl+C is handled at the top of Update() — never handle it here.
	// This case only remains to prevent Ctrl+C from falling through to the
	// textarea (which would insert a ^C character).
	switch {
	case msg.String() == "ctrl+c":
		return m, nil, true

	case msg.Code == tea.KeyEsc:
		// Esc：非处理状态清空输入；处理中时取消排队编辑或清空输入
		if m.queueEditing {
			m.queueEditing = false
			m.queueEditBuf = ""
			m.textarea.SetValue("")
			return m, nil, true
		}
		if !m.typing {
			if m.textarea.Value() != "" {
				m.textarea.Reset()
				m.inputHistoryIdx = -1
				m.inputDraft = ""
				m.autoExpandInput()
			}
		}
		return m, nil, true

	case msg.String() == "ctrl+k":
		// §23 Ctrl+K: Command Palette — always available, even in panels
		if !m.paletteOpen {
			m.openCommandPalette()
			return m, nil, true
		}

	case msg.String() == "ctrl+p":
		// Ctrl+P: Quick switch subscription
		if m.panelMode == "" && m.subscriptionMgr != nil && !m.typing {
			m.openQuickSwitch("subscription")
			return m, nil, true
		}

	case msg.String() == "ctrl+t":
		// Ctrl+T: Open Sessions panel (T = Tabs/Sessions)
		if m.panelMode == "" {
			m.openSessionsPanel()
			return m, nil, true
		}

	case msg.String() == "ctrl+b":
		// Ctrl+B: Toggle sidebar (only in wide mode)
		if m.panelMode == "" && m.isWide() && m.sidebarEnabled {
			m.sidebarVisible = !m.sidebarVisible
			m.invalidateLayoutCache()
			m.relayoutViewport()
			return m, nil, true
		}

	case msg.String() == "ctrl+n":
		// Cycle model (next in list)
		// Uses Ctrl+N instead of Ctrl+M because Ctrl+M is indistinguishable
		// from Enter on Windows VT Input Mode (Char=\r in both cases).
		if m.panelMode == "" && !m.typing && m.channel != nil {
			m.cycleModel()
			// Drain pending cmds (e.g. showTempStatus timer) immediately
			// to avoid an extra Update→View cycle on the next frame.
			if len(m.pendingCmds) > 0 {
				pending := m.pendingCmds
				m.pendingCmds = nil
				return m, []tea.Cmd{tea.Batch(pending...)}, true
			}
			return m, nil, true
		}

	case msg.Text == "^":
		// ^ opens bg tasks panel only when input is empty AND there are running tasks.
		// Gate prevents intercepting the ^ character during normal typing.
		if m.panelMode == "" && m.inputHistoryIdx == -1 && m.bgTaskCount > 0 {
			m.openBgTasksPanel()
			return m, nil, true
		}

	case msg.Code == tea.KeyUp && msg.Mod == tea.ModShift:
		model, cmd, handled := m.handleShiftUp()
		if handled {
			return model, cmd, true
		}

	case msg.Code == tea.KeyUp:
		// Plain ArrowUp: only viewport scroll (no queue recall / history).
		// If textarea has content, let textarea own multiline vertical cursor movement.
		if m.panelMode == "" && m.textarea.Value() != "" {
			break
		}
		// Viewport 不在底部时，方向键滚动 viewport
		if !m.viewport.AtBottom() {
			m.viewport.ScrollUp(1)
			m.userScrolledUp = true
			return m, nil, true
		}

	case msg.Code == tea.KeyDown && msg.Mod == tea.ModShift:
		model, cmd, handled := m.handleShiftDown()
		if handled {
			return model, cmd, true
		}

	case msg.Code == tea.KeyDown:
		// Plain ArrowDown: only viewport scroll.
		if m.panelMode == "" && m.textarea.Value() != "" {
			break
		}
		if !m.viewport.AtBottom() {
			m.viewport.ScrollDown(1)
			if m.viewport.AtBottom() {
				m.userScrolledUp = false
				m.newContentHint = false
			}
			return m, nil, true
		}

	case msg.Code == tea.KeyEnter:
		model, enterCmds, handled := m.handleEnterKey()
		if handled {
			return model, enterCmds, true
		}

	case msg.Code == tea.KeyTab:
		// §8 Tab 命令补全
		m.handleTabComplete()
		return m, nil, true

	case msg.String() == "ctrl+o":
		// §11 Ctrl+O 切换 tool summary 展开/折叠（兼容非 CSI-u 终端）
		m.toggleToolSummary()
		return m, nil, true

	case msg.String() == "ctrl+e":
		// §19 Ctrl+E 切换长消息折叠（搜索导航模式下拦截）
		if m.searchMode && !m.searchEditing {
			return m, nil, true
		}
		if !m.typing && !m.searchMode && len(m.messages) > 0 {
			m.toggleMessageFold()
		}
		return m, nil, true

	} // end switch

	// Unhandled key — let post-switch processing handle it (viewport/textarea update)
	return m, nil, false
}

// handleInjectedUserMsg processes user messages injected by the agent (e.g. bg task completion).
func (m *cliModel) handleInjectedUserMsg(msg cliInjectedUserMsg) []tea.Cmd {
	// suLoading guard: during session switch in remote mode, discard injected messages.
	// They belong to the previous session's context; the RPC will handle state.
	if m.suLoading {
		log.WithFields(log.Fields{"msg_chat_id": msg.chatID}).Debug("handleInjectedUserMsg: suLoading, discarding (session switch in progress)")
		return nil
	}
	// Filter by session: if chatID is set, only apply to matching session.
	// Legacy messages (chatID="") are always applied for backward compat.
	if msg.chatID != "" {
		currentKey := qualifyChatID(m.channelName, m.chatID)
		if msg.chatID != currentKey {
			log.WithFields(log.Fields{"msg_chat_id": msg.chatID, "current_key": currentKey}).Debug("handleInjectedUserMsg: session filter mismatch, discarding")
			return nil
		}
	}
	m.messages = append(m.messages, cliMessage{
		role:      "user",
		content:   msg.content,
		timestamp: time.Now(),
		dirty:     true,
	})
	// Only start a new turn if the agent is idle.
	// If already typing, the agent is processing this message (injectInbound was
	// already called). Starting a new turn here would increment agentTurnID,
	// causing the current turn's endAgentTurn to become a no-op (stale turnID).
	// This produces two user messages without an assistant reply between them.
	if !m.typing {
		m.startAgentTurn()
	}
	// Refresh bg task count on injection
	if m.bgTaskCountFn != nil {
		m.bgTaskCount = m.bgTaskCountFn()
	}
	// Refresh agent count on injection
	if m.agentCountFn != nil {
		m.agentCount = m.agentCountFn()
	}
	m.rc.valid = false
	// NOTE: do NOT return tickCmd() here. The wasTyping guard at the bottom of
	// Update() detects idle->typing and starts the tick chain.
	// Returning tickCmd() here creates a duplicate chain (2x spinner speed).
	// §16 触发 toast 通知（后台任务完成提示）
	// 提取首行作为 toast 文本，避免内容过长
	firstLine := msg.content
	if idx := strings.Index(msg.content, "\n"); idx >= 0 {
		firstLine = msg.content[:idx]
	}
	if len([]rune(firstLine)) > 50 {
		firstLine = string([]rune(firstLine)[:47]) + "..."
	}
	// 检测是否为完成或失败消息
	icon := "ℹ"
	lower := strings.ToLower(firstLine)
	if strings.Contains(lower, "done") || strings.Contains(lower, "completed") || strings.Contains(lower, "完成") {
		icon = "✓"
	} else if strings.Contains(lower, "error") || strings.Contains(lower, "failed") {
		icon = "✗"
	}
	return []tea.Cmd{m.enqueueToast(firstLine, icon)}
}

// handleUpdateCheck processes update check results.
func (m *cliModel) handleUpdateCheck(msg cliUpdateCheckMsg) {
	m.checkingUpdate = false
	if msg.info == nil {
		m.showSystemMsg(m.locale.UpdateFailed, feedbackError)
		return
	}
	// Dev builds and non-stable channels skip the check — don't show any message.
	if msg.info.Skipped {
		return
	}
	m.updateNotice = msg.info
	// Suppress update notice when an agent turn is active (progress running).
	// The notice would corrupt the progress panel layout and distract from
	// the active iteration history the user needs to see.
	// The notice is still stored in m.updateNotice for manual /update check.
	if m.typing || (m.progress != nil && m.progress.Phase != "done" && m.progress.Phase != "") {
		return
	}
	if msg.info.HasUpdate {
		content := fmt.Sprintf(m.locale.UpdateFound, msg.info.Current, msg.info.Latest, msg.info.URL)
		m.showSystemMsg(content, feedbackInfo)
	} else {
		ch := msg.info.Channel
		if ch == "" {
			ch = "dev"
		}
		content := fmt.Sprintf(m.locale.UpdateCurrent, msg.info.Current, ch)
		m.showSystemMsg(content, feedbackInfo)
	}
}

// handleToastMsg enqueues a toast notification.
func (m *cliModel) handleToastMsg(msg cliToastMsg) []tea.Cmd {
	// §16 Toast 通知入队（最多保留 5 条，显示前 3 条）
	if len(m.toasts) >= 5 {
		m.toasts = m.toasts[len(m.toasts)-4:]
	}
	m.toasts = append(m.toasts, cliToastItem(msg))
	if !m.toastTimer {
		m.toastTimer = true
		return []tea.Cmd{tea.Tick(3*time.Second, func(time.Time) tea.Msg {
			return cliToastClearMsg{}
		})}
	}
	return nil
}

// handleToastClear removes the oldest toast notification.
func (m *cliModel) handleToastClear(msg cliToastClearMsg) []tea.Cmd {
	if len(m.toasts) > 0 {
		m.toasts = m.toasts[1:]
	}
	if len(m.toasts) > 0 {
		return []tea.Cmd{tea.Tick(3*time.Second, func(time.Time) tea.Msg {
			return cliToastClearMsg{}
		})}
	}
	m.toastTimer = false
	return nil
}

// handleCtrlC handles the unified Ctrl+C keypress.
// Returns (model, cmd, handled). If handled is true, Update() returns immediately.
func (m *cliModel) handleCtrlC() (tea.Model, tea.Cmd, bool) {
	// 1. 关闭所有 overlay/panel
	if m.paletteOpen {
		m.closeCommandPalette()
	}
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
	// 2. 取消正在编辑的排队消息
	if m.queueEditing {
		m.queueEditing = false
		m.queueEditBuf = ""
		m.textarea.SetValue("")
	}
	// 3. 如果 agent 正在处理：
	//    - 有排队消息：先删除最后一条（再按清空全部，再按 cancel agent）
	//    - 无排队消息：发送 cancel
	if m.typing {
		queueLen := len(m.messageQueue)
		if queueLen > 0 {
			if m.queueEditing {
				// 正在编辑排队消息 → 取消编辑并删除该消息
				removed := m.messageQueue[len(m.messageQueue)-1].content
				m.messageQueue = m.messageQueue[:len(m.messageQueue)-1]
				m.queueEditing = false
				m.queueEditBuf = ""
				m.textarea.SetValue("")
				m.showSystemMsg(fmt.Sprintf(m.locale.QueueItemRemoved, removed), feedbackInfo)
			} else if queueLen > 1 {
				// 多条排队 → 删除最后一条
				removed := m.messageQueue[len(m.messageQueue)-1].content
				m.messageQueue = m.messageQueue[:len(m.messageQueue)-1]
				m.showSystemMsg(fmt.Sprintf(m.locale.QueueItemRemoved+". "+m.locale.QueueCleared, removed, len(m.messageQueue)), feedbackInfo)
			} else {
				// 只剩一条 → 清空全部
				m.messageQueue = nil
				m.showSystemMsg(fmt.Sprintf(m.locale.QueueCleared, queueLen), feedbackInfo)
			}
		} else {
			m.sendCancel()
			m.turnCancelled = true // prevent stale progress from auto-starting after cancel
		}
		return m, nil, true
	}
	// 4. 空闲状态：清空输入
	if m.textarea.Value() != "" {
		m.textarea.Reset()
		m.inputHistoryIdx = -1
		m.inputDraft = ""
		m.autoExpandInput()
	}
	return m, nil, true
}

// handleTickMsg processes the global 100ms tick from the goroutine in
// NewCLIChannel. It handles ALL timed UI updates: splash animation,
// spinner/progress, queue flush, and placeholder rotation.
// Returns cmds only for typewriter (separate chain) and queue flush.
// NEVER returns tickCmd — the global goroutine is the single tick source.
func (m *cliModel) handleTickMsg() []tea.Cmd {
	var cmds []tea.Cmd

	// Splash / suLoading animation — data-ready driven, no artificial delay.
	if !m.splashDone || m.suLoading {
		m.splashFrame++
		// End splash as soon as model is ready and RPC loading is done.
		if !m.suLoading && m.ready {
			m.splashDone = true
		}
		// Hard limit: ~3s (30 frames × 100ms) UNCONDITIONAL — safety net
		// if RPC hangs. User sees the UI instead of staring at splash forever.
		if m.splashFrame >= 30 {
			m.splashDone = true
		}
	}

	// Reconnect overlay spinner animation — advances every tick (100ms)
	// when WS connection is lost, providing visual feedback.
	if m.remoteMode && m.connState != "connected" && m.connState != "" {
		m.reconnectFrame++
	}

	// Spinner / progress update
	sessionActive := m.progress != nil && m.progress.Phase != "done"
	busy := m.typing || sessionActive
	needsSpinnerTick := busy || m.sidebarHasBusySessions

	// Refresh bg task / agent counts every tick so the infobar and sidebar
	// stay accurate even when the agent is idle (no progress messages flowing).
	prevBg := m.bgTaskCount
	prevAgent := m.agentCount
	if m.bgTaskCountFn != nil {
		m.bgTaskCount = m.bgTaskCountFn()
	}
	if m.agentCountFn != nil {
		m.agentCount = m.agentCountFn()
	}
	countsChanged := m.bgTaskCount != prevBg || m.agentCount != prevAgent

	if (m.bgTaskCount > 0) || (m.agentCount > 0) || needsSpinnerTick {
		m.ticker.tick()
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
		m.typewriterTickActive = false
		if !m.rc.valid || countsChanged {
			m.updateViewportContent()
		}
	}

	// Queue flush
	if m.needFlushQueue && !m.typing && !m.suLoading && len(m.messageQueue) > 0 {
		prevTurnID := m.agentTurnID
		canFlush := m.isTurnReplyReceived(prevTurnID)
		if !canFlush && m.isTurnDoneProcessed(prevTurnID) && m.turnCancelled {
			canFlush = true
		}
		if !canFlush && m.isTurnDoneProcessed(prevTurnID) {
			prevFlag := m.getTurnFlag(prevTurnID)
			if prevFlag != nil && !prevFlag.doneTime.IsZero() && time.Since(prevFlag.doneTime) > 2*time.Second {
				log.WithField("turnID", prevTurnID).Warn("Queue flush timeout: forcing flush after 2s without reply")
				canFlush = true
			}
		}

		if canFlush {
			m.needFlushQueue = false
			m.flushMessageQueue()
			return cmds
		}
	}

	// Idle: placeholder rotation (every 30 ticks = ~3s)
	if !busy && !needsSpinnerTick && m.splashDone {
		m.idleTickCounter++
		if m.idleTickCounter >= 30 {
			m.idleTickCounter = 0
			if m.cachedModelName == "" && m.remoteMode {
				m.refreshCachedModelName()
			}
			m.updatePlaceholder()
		}
	} else {
		m.idleTickCounter = 0
	}

	return cmds
}

func (m *cliModel) handleTypewriterTick() []tea.Cmd {
	var cmds []tea.Cmd
	// Advance typewriter by 1 rune on its own 50ms cadence.
	m.advanceTypewriter()
	m.updateViewportContent()
	// Continue chain if still behind on either stream or reasoning content
	streamBehind := m.progress != nil && m.progress.StreamContent != "" && m.twVisible < len([]rune(m.progress.StreamContent))
	reasoningBehind := m.progress != nil && m.progress.ReasoningStreamContent != "" && m.rwVisible < len([]rune(m.progress.ReasoningStreamContent))
	if m.typewriterTickActive && (streamBehind || reasoningBehind) {
		cmds = append(cmds, typewriterTickCmd())
	} else {
		m.typewriterTickActive = false
	}
	return cmds
}

// handleSplashDone processes the splash screen completion.
func (m *cliModel) handleSplashDone() []tea.Cmd {
	var cmds []tea.Cmd
	// §14 启动画面结束确认
	m.splashDone = true
	// Remote mode: retry model name fetch — the initial call in cli.go:76
	// may have failed if the WS RPC wasn't fully ready yet.
	if m.cachedModelName == "" && m.remoteMode {
		m.refreshCachedModelName()
	}
	_ = m.progress // sessionActive computed for future use
	return cmds
}

// handleApprovalRequest shows the approval dialog for a permission request.
func (m *cliModel) handleApprovalRequest(msg approvalRequestMsg) (tea.Model, tea.Cmd) {
	// Permission control: show approval dialog
	m.approvalRequest = &msg.request
	m.approvalResultCh = msg.resultCh
	m.approvalCursor = 0 // default to Approve
	m.approvalEnteringDeny = false
	m.approvalDenyInput = textinput.New()
	m.approvalDenyInput.Placeholder = "Optional deny reason for LLM"
	m.approvalDenyInput.CharLimit = 200
	m.approvalDenyInput.SetWidth(60)
	m.panelMode = "approval"
	m.rc.valid = false
	return m, nil
}

// handleSearchKey processes key events when search mode is active.
// Returns (model, cmd, handled). If handled is true, Update() returns immediately.
func (m *cliModel) handleSearchKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case m.searchEditing:
		switch key.String() {
		case "enter":
			m.executeSearch()
			return m, nil, true
		case "esc":
			m.exitSearch()
			return m, nil, true
		}
		var cmd tea.Cmd
		m.searchTI, cmd = m.searchTI.Update(key)
		return m, cmd, true
	default:
		switch key.String() {
		case "n":
			if len(m.searchResults) > 0 {
				next := m.searchIdx + 1
				if next >= len(m.searchResults) {
					next = 0
				}
				m.jumpToSearchResult(next)
				m.rc.valid = false
				m.updateViewportContent()
			}
			return m, nil, true
		case "N":
			if len(m.searchResults) > 0 {
				prev := m.searchIdx - 1
				if prev < 0 {
					prev = len(m.searchResults) - 1
				}
				m.jumpToSearchResult(prev)
				m.rc.valid = false
				m.updateViewportContent()
			}
			return m, nil, true
		case "esc":
			m.exitSearch()
			return m, nil, true
		}
		return m, nil, true
	}
}

// handleEnterKey processes the Enter keypress for sending messages, queue management,
// and file completion. Returns (model, cmds, handled).
func (m *cliModel) handleEnterKey() (tea.Model, []tea.Cmd, bool) {
	var cmds []tea.Cmd

	// Plain Enter sends. Modified/newline-intent variants should fall through to
	// the textarea so its native multiline/internal-scroll behavior works,
	// especially once the input reaches MaxHeight.
	// Note: ctrl+j is handled earlier in Update() via isCtrlJ() → InsertString("\n").
	// Note: cycleModel uses Ctrl+N (not Ctrl+M), so no need to intercept here.
	// Enter 发送消息
	if !m.inputReady {
		// §Q 消息队列：typing 期间允许排队消息
		if m.queueEditing {
			// 正在编辑排队消息 → 保存编辑结果
			m.messageQueue[len(m.messageQueue)-1].content = m.textarea.Value()
			m.queueEditing = false
			m.queueEditBuf = ""
			m.textarea.SetValue("")
			return m, nil, true
		}
		if m.textarea.Value() != "" {
			m.messageQueue = append(m.messageQueue, queuedMsg{content: m.textarea.Value(), chatID: m.chatID})
			m.textarea.SetValue("")
			// 显示队列提示
			if len(m.messageQueue) == 1 {
				m.showTempStatus(fmt.Sprintf(m.locale.MessageQueuedUp, len(m.messageQueue)))
			} else {
				m.showTempStatus(fmt.Sprintf(m.locale.MessageQueued, len(m.messageQueue)))
			}
			return m, nil, true
		}
		return m, nil, true
	}
	// §8b @ 模式：Enter 进入目录或确认文件
	// Check fileCompletions even without Tab (fileCompActive=false):
	// typing @path auto-populates completions via input change handler.
	if len(m.fileCompletions) > 0 {
		input := m.textarea.Value()
		if ok, prefix := detectAtPrefix(input); ok {
			selected := m.fileCompletions[m.fileCompIdx]
			atStart := len(input) - len(prefix) - 1
			if isDir(selected) {
				newInput := input[:atStart] + "@" + selected + "/"
				m.textarea.SetValue(newInput)
				m.fileCompActive = false
				m.populateFileCompletions(selected + "/")
			} else {
				newInput := input[:atStart] + "@" + selected + " "
				m.textarea.SetValue(newInput)
				m.fileCompActive = false
				m.fileCompletions = nil
				m.fileCompIdx = 0
			}
			return m, nil, true
		}
	}
	content := strings.TrimSpace(m.textarea.Value())
	if content != "" {
		// §22 输入历史：保存发送的内容（去重，不保存 / 命令和空输入）
		if !strings.HasPrefix(content, "/") {
			if len(m.inputHistory) == 0 || m.inputHistory[0] != content {
				m.inputHistory = append([]string{content}, m.inputHistory...)
				if len(m.inputHistory) > 100 {
					m.inputHistory = m.inputHistory[:100]
				}
			}
		}
		m.inputHistoryIdx = -1
		m.inputDraft = ""
		if m.allTodosDone() {
			m.todos = nil
			m.todosDoneCleared = true
			m.relayoutViewport() // recalculate viewport height after clearing todo bar
		}
		// 发送消息（彩蛋可能返回动画 cmd）
		if cmd := m.sendMessage(content); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.textarea.Reset()
		m.autoExpandInput()
		m.viewport.GotoBottom()
		m.newContentHint = false
		m.userScrolledUp = false
	}
	// NOTE: tick chain is started by startAgentTurn() inside sendMessage().
	// No need to emit tickCmd() here — doing so would create duplicate chains.
	return m, cmds, true
}

// handleShiftUp handles Shift+Up for queue recall and input history browsing.
func (m *cliModel) handleShiftUp() (tea.Model, []tea.Cmd, bool) {
	// Shift+Up: recall queued message for editing / browse input history.
	// When actively browsing history (inputHistoryIdx >= 0), allow continued
	// scrolling even though textarea has content (from the previous history entry).
	if m.panelMode == "" && m.textarea.Value() != "" && m.inputHistoryIdx < 0 {
		return m, nil, true
	}
	if !m.viewport.AtBottom() {
		return m, nil, true
	}
	// §Q 消息队列：typing 时 Shift+↑ 追回最后一条排队消息编辑
	if m.panelMode == "" && m.typing && !m.inputReady && len(m.messageQueue) > 0 {
		if !m.queueEditing && m.textarea.Value() == "" {
			// 追回最后一条排队消息
			m.queueEditing = true
			m.queueEditBuf = m.messageQueue[len(m.messageQueue)-1].content
			m.textarea.SetValue(m.queueEditBuf)
			m.autoExpandInput()
			return m, nil, true
		}
	}
	if m.panelMode == "" && !m.typing {
		// 空输入时浏览历史
		if (m.textarea.Value() == "" || m.inputHistoryIdx >= 0) && len(m.inputHistory) > 0 {
			if m.inputHistoryIdx == -1 {
				m.inputDraft = m.textarea.Value() // 保存当前草稿
				m.inputHistoryIdx = 0
			} else if m.inputHistoryIdx < len(m.inputHistory)-1 {
				m.inputHistoryIdx++
			}
			m.textarea.SetValue(m.inputHistory[m.inputHistoryIdx])
			m.autoExpandInput()
			return m, nil, true
		}
	}
	return m, nil, false
}

// handleShiftDown handles Shift+Down for reverse input history browsing.
func (m *cliModel) handleShiftDown() (tea.Model, []tea.Cmd, bool) {
	// Shift+Down: browse input history backwards.
	// Only block when NOT in history browsing mode AND textarea has content.
	if m.panelMode == "" && m.textarea.Value() != "" && m.inputHistoryIdx < 0 {
		return m, nil, true
	}
	if !m.viewport.AtBottom() {
		return m, nil, true
	}
	if m.panelMode == "" && !m.typing && m.inputHistoryIdx >= 0 {
		if m.inputHistoryIdx > 0 {
			m.inputHistoryIdx--
			m.textarea.SetValue(m.inputHistory[m.inputHistoryIdx])
		} else {
			m.inputHistoryIdx = -1
			m.textarea.SetValue(m.inputDraft)
		}
		m.autoExpandInput()
		return m, nil, true
	}
	return m, nil, false
}

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
	if m.easterEggState.mode != easterEggNone {
		return m, []tea.Cmd{func() tea.Msg { return easterEggDoneMsg{} }}, true
	}

	// 🥚 Konami Code 彩蛋：监听方向键和字母键
	if m.easterEggState.mode == easterEggNone {
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

	case msg.String() == "ctrl+t":
		// Ctrl+T: Open Sessions panel (T = Tabs/Sessions)
		if m.panelState.mode == "" {
			m.openSessionsPanel()
			return m, nil, true
		}

	case msg.String() == "ctrl+b":
		// Ctrl+B: Toggle sidebar (only in wide mode)
		if m.panelState.mode == "" && m.isWide() && m.layoutConfig.sidebarEnabled {
			m.layoutConfig.sidebarVisible = !m.layoutConfig.sidebarVisible
			m.invalidateLayoutCache()
			m.relayoutViewport()
			return m, nil, true
		}

	case msg.String() == "ctrl+n":
		// Open the unified LLM panel (subscriptions + models).
		if m.panelState.mode == "" && !m.typing && m.subscriptionMgr != nil {
			m.openQuickSwitch("")
			// Drain the background /models refresh cmd immediately.
			if len(m.pendingCmds) > 0 {
				pending := m.pendingCmds
				m.pendingCmds = nil
				return m, []tea.Cmd{tea.Batch(pending...)}, true
			}
			return m, nil, true
		}

	case msg.String() == "ctrl+m":
		// Toggle the global thinking_mode user setting (auto → enabled → disabled → auto).
		// Surfaced on the main chat status bar; works in normal chat mode (not while typing
		// in an input/panel).
		if m.panelState.mode == "" && !m.typing {
			m.toggleThinkingMode()
			return m, nil, true
		}

	case msg.Text == "^":
		// ^ opens bg tasks panel only when input is empty AND there are running tasks.
		// Gate prevents intercepting the ^ character during normal typing.
		if m.panelState.mode == "" && m.inputHistoryIdx == -1 && m.bgTaskCount > 0 {
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
		if m.panelState.mode == "" && m.textarea.Value() != "" {
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
		if m.panelState.mode == "" && m.textarea.Value() != "" {
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
		if m.searchState.mode && !m.searchState.editing {
			return m, nil, true
		}
		if !m.typing && !m.searchState.mode && len(m.messages) > 0 {
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
	if m.splashState.suLoading {
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

	// ── Race guard: queue if the current turn hasn't received its reply yet ──
	// The agent's chatProcessLoop calls sendMessage (async via bus) then immediately
	// calls drainAndProcessNotifications → injectCLIUserMessage (direct write to
	// asyncCh). The injected message can arrive in asyncCh BEFORE the turn's
	// outbound reply. Without this guard, handleInjectedUserMsg starts a new turn
	// (incrementing agentTurnID), causing the pending reply to be treated as
	// stale and dropped — losing ALL iterations from the completed turn.
	//
	// Three cases to guard:
	//   1. m.typing == true: turn in progress, reply hasn't arrived yet
	//   2. !m.replyProcessed && !m.typing: PhaseDone arrived (set typing=false),
	//      but the reply hasn't been processed by handleAgentMessage yet.
	//   3. m.turnCancelled == true: Ctrl+C PhaseDone arrived (typing=false)
	//      but cancel ack hasn't been processed yet.
	shouldQueue := m.typing || m.turnCancelled || !m.replyProcessed

	// Race fix: if typing=true was set by progress auto-start (not by a real
	// user message), the injected notification IS the user message for this
	// turn. Claim it by inserting the user message before the streaming message
	// instead of queuing — queuing would produce a second assistant when flushed.
	if shouldQueue && m.typing && m.turnAutoStarted {
		m.turnAutoStarted = false
		m.insertUserMessageBeforeStreaming(msg.content)
		if m.bgTaskCountFn != nil {
			m.bgTaskCount = m.bgTaskCountFn()
		}
		if m.agentCountFn != nil {
			m.agentCount = m.agentCountFn()
		}
		m.rc.valid = false
		return nil
	}

	if shouldQueue {
		log.Debug("handleInjectedUserMsg: queuing — current turn reply not yet received")
		m.messageQueue = append(m.messageQueue, queuedMsg{content: msg.content, chatID: m.chatID})
		if m.bgTaskCountFn != nil {
			m.bgTaskCount = m.bgTaskCountFn()
		}
		if m.agentCountFn != nil {
			m.agentCount = m.agentCountFn()
		}
		m.rc.valid = false
		return nil
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
	if m.typing {
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
	if len(m.toastState.queue) >= 5 {
		m.toastState.queue = m.toastState.queue[len(m.toastState.queue)-4:]
	}
	m.toastState.queue = append(m.toastState.queue, cliToastItem(msg))
	if !m.toastState.timerActive {
		m.toastState.timerActive = true
		return []tea.Cmd{tea.Tick(3*time.Second, func(time.Time) tea.Msg {
			return cliToastClearMsg{}
		})}
	}
	return nil
}

// handleToastClear removes the oldest toast notification.
func (m *cliModel) handleToastClear(msg cliToastClearMsg) []tea.Cmd {
	if len(m.toastState.queue) > 0 {
		m.toastState.queue = m.toastState.queue[1:]
	}
	if len(m.toastState.queue) > 0 {
		return []tea.Cmd{tea.Tick(3*time.Second, func(time.Time) tea.Msg {
			return cliToastClearMsg{}
		})}
	}
	m.toastState.timerActive = false
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
	if m.panelState.mode != "" {
		m.closePanel()
	}
	if m.searchState.mode {
		m.exitSearch()
	}
	// 2. 取消正在编辑的排队消息
	if m.queueEditing {
		m.queueEditing = false
		m.queueEditBuf = ""
		m.textarea.SetValue("")
	}
	// 3. 如果 agent 正在处理：始终立即发送 cancel，同时清空排队消息。
	//    不再要求用户按多次 Ctrl+C 来逐条删除排队消息 — 那段时间里 agent 继续在跑，
	//    用户体验是 "Ctrl+C 没反应"。
	if m.typing {
		if len(m.messageQueue) > 0 {
			m.messageQueue = nil
			m.showSystemMsg("已清空排队消息", feedbackInfo)
		}
		m.sendCancel()
		m.turnCancelled = true // prevent stale progress from auto-starting after cancel
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
	if !m.splashState.done || m.splashState.suLoading {
		m.splashState.frame++
		// End splash as soon as model is ready and RPC loading is done.
		if !m.splashState.suLoading && m.ready {
			m.splashState.done = true
		}
		// Hard limit: ~3s (30 frames × 100ms) UNCONDITIONAL — safety net
		// if RPC hangs. User sees the UI instead of staring at splash forever.
		if m.splashState.frame >= 30 {
			m.splashState.done = true
		}
	}

	// Reconnect overlay spinner animation — advances every tick (100ms)
	// when WS connection is lost, providing visual feedback.
	if m.remoteMode && m.connState != "connected" && m.connState != "" {
		m.reconnectFrame++
	}

	// Spinner / progress update
	// m.typing is the authoritative busy state flag (set by startAgentTurn,
	// cleared by endAgentTurn). progressState.current is rendering data,
	// NOT a state flag — it's preserved after endAgentTurn for flicker-free
	// rendering between PhaseDone and handleAgentMessage.
	busy := m.typing
	needsSpinnerTick := busy || m.progressState.busySessions

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

	// Gap-triggered snapshot pull.
	//
	// Push events (best-effort, like Raft AppendEntries) carry a monotonic Seq.
	// When the client detects a Seq jump (e.g. 2→5, meaning events 3,4 were
	// dropped by Hub sendCh-full or WS disconnect), it sets gapDetected=true.
	// On the next tick (100ms), we immediately pull the authoritative snapshot
	// from the server (like Raft InstallSnapshot), which includes the complete
	// iteration history filtered by watermark.
	//
	// Normal flow: Seq increments by 1, no gap, ZERO pulls. Push carries everything.
	// Recovery flow: gap → pull once → gapDetected cleared → back to push-only.
	// Reconnect flow: WS reconnect triggers /su path (full DB restore + fromIter=-1).
	//
	// Watermark: max iteration from local iterations list, or -1 when empty
	// (so server returns ALL iterations including iteration 0).
	if m.typing && m.channel != nil && m.channel.config.GetActiveProgressFn != nil {
		if m.progressState.gapDetected {
			m.progressState.gapDetected = false
			watermark := -1
			for _, it := range m.progressState.iterations {
				if it.Iteration > watermark {
					watermark = it.Iteration
				}
			}
			if snapshot := m.channel.config.GetActiveProgressFn(m.channelName, m.chatID, watermark); snapshot != nil {
				m.applyProgressSnapshot(snapshot)
			}
		}
	}

	if (m.bgTaskCount > 0) || (m.agentCount > 0) || needsSpinnerTick {
		m.ticker.tick()
		hasStreamContent := m.progressState.current != nil && m.progressState.current.StreamContent != "" && m.progressState.twVisible < len([]rune(m.progressState.current.StreamContent))
		hasReasoningContent := m.progressState.current != nil && m.progressState.current.ReasoningStreamContent != "" && m.progressState.rwVisible < len([]rune(m.progressState.current.ReasoningStreamContent))
		if hasStreamContent || hasReasoningContent {
			if !m.progressState.twActive {
				m.progressState.twActive = true
				cmds = append(cmds, typewriterTickCmd())
			}
		}
		m.updateViewportContent()
	} else {
		m.progressState.twActive = false
		if !m.rc.valid || countsChanged {
			m.updateViewportContent()
		}
	}

	// Queue flush — only flush when reply has been fully processed.
	// applyProgressSnapshot may set needFlushQueue before the reply arrives;
	// the tick handler must wait until replyProcessed is true (set by
	// handleAgentMessage or handleCancelAck) to prevent premature flush
	// that would start a new turn before the current turn's reply is
	// attached to the assistant message.
	//
	// LINEAR CONSISTENCY: A one-tick delay (flushDelay) is inserted between
	// needFlushQueue being armed and the actual flush. This gives pending
	// cliInjectedUserMsg messages (e.g. bg task notifications drained after
	// the reply was sent) time to arrive from asyncCh and be appended to the
	// message list BEFORE the queued user message is flushed. Without this
	// delay, the tick can fire between handleAgentMessage and the arrival of
	// the injected notification, causing the user message to appear before
	// the notification in the TUI — even though the backend processed the
	// notification first.
	if m.needFlushQueue && !m.typing && m.replyProcessed && !m.splashState.suLoading && len(m.messageQueue) > 0 {
		if m.flushDelay > 0 {
			m.flushDelay--
		} else {
			m.needFlushQueue = false
			m.flushDelay = 0
			m.flushMessageQueue()
			return cmds
		}
	}

	// Idle: placeholder rotation (every 30 ticks = ~3s)
	if !busy && !needsSpinnerTick && m.splashState.done {
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

	// NEVER returns tickCmd — the global goroutine is the single tick source.
	return cmds
}

func (m *cliModel) handleTypewriterTick() []tea.Cmd {
	var cmds []tea.Cmd
	// Advance typewriter by 1 rune on its own 50ms cadence.
	m.advanceTypewriter()
	m.updateViewportContent()
	// Continue chain if still behind on either stream or reasoning content
	streamBehind := m.progressState.current != nil && m.progressState.current.StreamContent != "" && m.progressState.twVisible < len([]rune(m.progressState.current.StreamContent))
	reasoningBehind := m.progressState.current != nil && m.progressState.current.ReasoningStreamContent != "" && m.progressState.rwVisible < len([]rune(m.progressState.current.ReasoningStreamContent))
	if m.progressState.twActive && (streamBehind || reasoningBehind) {
		cmds = append(cmds, typewriterTickCmd())
	} else {
		m.progressState.twActive = false
	}
	return cmds
}

// handleSplashDone processes the splash screen completion.
func (m *cliModel) handleSplashDone() []tea.Cmd {
	var cmds []tea.Cmd
	// §14 启动画面结束确认
	m.splashState.done = true
	// Remote mode: retry model name fetch — the initial call in cli.go:76
	// may have failed if the WS RPC wasn't fully ready yet.
	if m.cachedModelName == "" && m.remoteMode {
		m.refreshCachedModelName()
	}
	_ = m.progressState.current // sessionActive computed for future use
	return cmds
}

// handleApprovalRequest shows the approval dialog for a permission request.
func (m *cliModel) handleApprovalRequest(msg approvalRequestMsg) (tea.Model, tea.Cmd) {
	// Permission control: show approval dialog
	m.panelState.misc.approvalReq = &msg.request
	m.panelState.misc.approvalCh = msg.resultCh
	m.panelState.misc.approvalCursor = 0 // default to Approve
	m.panelState.misc.approvalDenyMode = false
	m.panelState.misc.approvalDenyTA = textinput.New()
	m.panelState.misc.approvalDenyTA.Placeholder = "Optional deny reason for LLM"
	m.panelState.misc.approvalDenyTA.CharLimit = 200
	m.panelState.misc.approvalDenyTA.SetWidth(60)
	m.panelState.mode = "approval"
	m.rc.valid = false
	return m, nil
}

// handleSearchKey processes key events when search mode is active.
// Returns (model, cmd, handled). If handled is true, Update() returns immediately.
func (m *cliModel) handleSearchKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case m.searchState.editing:
		switch key.String() {
		case "enter":
			m.executeSearch()
			return m, nil, true
		case "esc":
			m.exitSearch()
			return m, nil, true
		}
		var cmd tea.Cmd
		m.searchState.ti, cmd = m.searchState.ti.Update(key)
		return m, cmd, true
	default:
		switch key.String() {
		case "n":
			if len(m.searchState.results) > 0 {
				next := m.searchState.idx + 1
				if next >= len(m.searchState.results) {
					next = 0
				}
				m.jumpToSearchResult(next)
				m.rc.valid = false
				m.updateViewportContent()
			}
			return m, nil, true
		case "N":
			if len(m.searchState.results) > 0 {
				prev := m.searchState.idx - 1
				if prev < 0 {
					prev = len(m.searchState.results) - 1
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
	if m.panelState.mode == "" && m.textarea.Value() != "" && m.inputHistoryIdx < 0 {
		return m, nil, true
	}
	if !m.viewport.AtBottom() {
		return m, nil, true
	}
	// §Q 消息队列：typing 时 Shift+↑ 追回最后一条排队消息编辑
	if m.panelState.mode == "" && m.typing && !m.inputReady && len(m.messageQueue) > 0 {
		if !m.queueEditing && m.textarea.Value() == "" {
			// 追回最后一条排队消息
			m.queueEditing = true
			m.queueEditBuf = m.messageQueue[len(m.messageQueue)-1].content
			m.textarea.SetValue(m.queueEditBuf)
			m.autoExpandInput()
			return m, nil, true
		}
	}
	if m.panelState.mode == "" && !m.typing {
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
	if m.panelState.mode == "" && m.textarea.Value() != "" && m.inputHistoryIdx < 0 {
		return m, nil, true
	}
	if !m.viewport.AtBottom() {
		return m, nil, true
	}
	if m.panelState.mode == "" && !m.typing && m.inputHistoryIdx >= 0 {
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

package channel

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"xbot/protocol"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	log "xbot/logger"
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

// restoreIterationHistory converts IterationHistory from a reconnect snapshot
// into local iteration history, bootstrapping tool StartedAt timestamps.
func (m *cliModel) restoreIterationHistory(payload *protocol.ProgressEvent) {
	if payload == nil || len(payload.IterationHistory) == 0 || len(m.iterationHistory) > 0 {
		return
	}
	for _, ih := range payload.IterationHistory {
		snap := cliIterationSnapshot{
			Iteration: ih.Iteration,
			Thinking:  ih.Thinking,
			Reasoning: ih.Reasoning,
			Tools:     ih.CompletedTools,
		}
		for i := range snap.Tools {
			t := &snap.Tools[i]
			if t.StartedAt.IsZero() && t.Elapsed > 0 {
				t.StartedAt = time.Now().Add(-time.Duration(t.Elapsed) * time.Millisecond)
			}
		}
		m.iterationHistory = append(m.iterationHistory, snap)
	}
	if len(m.iterationHistory) > 0 {
		lastIter := m.iterationHistory[len(m.iterationHistory)-1].Iteration
		if lastIter > m.lastSeenIteration {
			m.lastSeenIteration = lastIter
		}
	}
	m.removeLastToolSummary()
}

// carryForwardProgressState preserves transient state across progress updates
// (StartedAt timers, CompletedTools, Reasoning/Thinking content, SubAgent trees).
func (m *cliModel) carryForwardProgressState(prev *protocol.ProgressEvent) {
	if m.progress == nil {
		return
	}

	// Preserve StartedAt across progress updates so live timers don't reset.
	startedAtMap := make(map[string]time.Time)
	if prev != nil {
		for _, t := range prev.ActiveTools {
			if !t.StartedAt.IsZero() {
				startedAtMap[t.Name] = t.StartedAt
			}
		}
	}
	for i := range m.progress.ActiveTools {
		t := &m.progress.ActiveTools[i]
		if sa, ok := startedAtMap[t.Name]; ok {
			t.StartedAt = sa
		} else if t.StartedAt.IsZero() {
			if t.Elapsed > 0 {
				t.StartedAt = time.Now().Add(-time.Duration(t.Elapsed) * time.Millisecond)
			} else {
				t.StartedAt = time.Now()
			}
		}
	}

	if prev == nil {
		return
	}
	sameIter := m.progress.Iteration == prev.Iteration || m.progress.Iteration == 0

	// Carry forward CompletedTools from previous progress within the same iteration.
	if len(m.progress.CompletedTools) == 0 && len(prev.CompletedTools) > 0 && sameIter {
		m.progress.CompletedTools = prev.CompletedTools
	}

	// Carry forward Reasoning/Thinking content.
	if m.progress.Reasoning == "" && prev.Reasoning != "" && sameIter {
		m.progress.Reasoning = prev.Reasoning
	}
	if m.progress.Thinking == "" && prev.Thinking != "" && sameIter {
		m.progress.Thinking = prev.Thinking
	}

	// Carry forward StreamContent within the same iteration.
	// Structured payloads from progressFinalizer/GetActiveProgress do not carry
	// StreamContent; losing it mid-stream causes visible "frozen" display after
	// session switch recovery.
	// Skip when Thinking is already set — it contains the same finalized content
	// and carrying StreamContent forward would cause duplicate rendering
	// (renderCurrentIteration renders both fields separately).
	if prev.StreamContent != "" && m.progress.StreamContent == "" && sameIter {
		if m.progress.Thinking == "" {
			m.progress.StreamContent = prev.StreamContent
		}
	}

	// Carry forward ReasoningStreamContent.
	// Guard: only when StreamContent is also empty — reasoning stream is the
	// LLM's internal thinking; once the actual text response (StreamContent)
	// starts, reasoning stream from the previous progress shouldn't reappear.
	if m.progress.ReasoningStreamContent == "" && prev.ReasoningStreamContent != "" && sameIter {
		if m.progress.StreamContent == "" {
			m.progress.ReasoningStreamContent = prev.ReasoningStreamContent
		}
	}

	// SubAgent tree preservation: merge new data into previous tree instead of
	// blindly copying the old tree. This prevents stale/zombie SubAgent entries
	// from persisting after they've completed.
	//
	// The server sends SubAgent data only when SubAgent progress changes.
	// Between updates, SubAgents is empty — we must carry forward the tree
	// so it remains visible. BUT we must merge (not replace) so that:
	//   - Completed SubAgents stay completed (don't revert to "running")
	//   - New SubAgents get added
	//   - SubAgents no longer in the server's tree get removed
	iterationChanged := m.progress.Iteration != prev.Iteration && m.progress.Iteration > 0
	if iterationChanged {
		m.progress.SubAgents = nil
	} else if len(m.progress.SubAgents) > 0 {
		// New progress has SubAgent data — merge into previous tree to preserve
		// completion status for agents no longer reported by the server.
		m.progress.SubAgents = mergeSubAgentTrees(prev.SubAgents, m.progress.SubAgents)
	} else if len(prev.SubAgents) > 0 {
		// No new SubAgent data — carry forward previous tree, but filter out
		// agents that were already done in prev. Done agents have already been
		// displayed to the user and should not linger across subsequent updates.
		m.progress.SubAgents = pruneDoneSubAgents(prev.SubAgents)
	}
}

// handleProgressMsg processes progress update events from the agent.
func (m *cliModel) handleProgressMsg(msg cliProgressMsg) {
	// Filter by session: only process progress for the currently viewed session.
	// payload.ChatID is set by ProgressEventHandler as "channel:chatID".
	// Fatal guard: ChatID must never be empty — it identifies which session
	// this progress belongs to. Empty ChatID means the progress bypassed
	// session filtering and would leak into the wrong view.
	if msg.payload != nil && msg.payload.ChatID == "" {
		log.WithFields(log.Fields{
			"phase":     msg.payload.Phase,
			"iteration": msg.payload.Iteration,
		}).Error("handleProgressMsg: received progress with empty ChatID — this is a programming error")
		return
	}

	if msg.payload != nil && msg.payload.ChatID != "" {
		currentKey := m.channelName + ":" + m.chatID
		if msg.payload.ChatID != currentKey {
			return
		}
	}

	turnID := m.agentTurnID // capture before any mutation
	prev := m.progress

	// Seq monotonic check: discard out-of-order or duplicate progress events.
	// Placed after ChatID filtering, before any state mutation.
	if msg.payload != nil && msg.payload.Seq > 0 {
		if msg.payload.Seq <= m.lastProgressSeq {
			return
		}
		m.lastProgressSeq = msg.payload.Seq
	}

	// New turn's first non-PhaseDone progress clears the cancel flag.
	// This allows the new turn (started by bg notification injection or queue flush)
	// to receive progress events, while still blocking stale progress from the
	// cancelled turn. Guard: only clear when typing (turn is active).
	if m.turnCancelled && msg.payload != nil && msg.payload.Phase != "done" && msg.payload.Phase != "" && m.typing {
		m.turnCancelled = false
	}

	// Guard: ignore progress after explicit Ctrl+C cancel.
	// PhaseDone is allowed through: it's idempotent (endAgentTurn checks turnID).
	// When switching to a running session with no saved state (first switch),
	// turnCancelled is false and m.typing is false — auto-start below handles it.
	if m.turnCancelled && msg.payload != nil && msg.payload.Phase != "done" {
		return
	}

	// Auto-start turn: when receiving progress for current session but not typing,
	// start the turn. This handles first-switch to a running SubAgent session.
	// Guard: !suLoading — during session switch in remote mode, progress events
	// from the old session may arrive before the RPC reconciles state. Starting
	// a turn here would create an inconsistent state with no message history loaded.
	// Guard: panelMode != "askuser" — AskUser panel sets m.typing=false but the
	// turn is paused (not ended). Late progress events from the engine must not
	// trigger startAgentTurn → resetProgressState, which clears iterationHistory
	// and makes all previous iterations disappear.
	if !m.typing && !m.suLoading && msg.payload != nil && msg.payload.Phase != "done" && m.panelMode != "askuser" {
		log.WithFields(log.Fields{
			"phase":     msg.payload.Phase,
			"iteration": msg.payload.Iteration,
			"active":    len(msg.payload.ActiveTools),
			"chatID":    msg.payload.ChatID,
		}).Info("handleProgressMsg: auto-start turn")
		m.startAgentTurn()
	}

	// suLoading guard: during session switch in remote mode, discard all
	// non-PhaseDone progress events. Only PhaseDone is allowed through
	// (to clear stale turn state). All other events are stale — the RPC
	// (handleSuHistoryLoad) will reconcile with authoritative server data.
	if m.suLoading && msg.payload != nil && msg.payload.Phase != "done" {
		return
	}
	// suLoading + PhaseDone: server confirmed the turn is done.
	// Record this so handleSuHistoryLoad won't restore stale progress
	// as active (which would create a stuck spinner — typing=true with
	// no more progress events coming from the idle server).
	if m.suLoading && msg.payload != nil && msg.payload.Phase == "done" {
		m.suPhaseDoneConfirmed = true
	}

	// Stream-only payloads (from StreamContentFunc/StreamReasoningFunc) only carry
	// stream content fields. Merge into existing progress instead of replacing to
	// preserve tool/iteration state.
	isStreamOnly := msg.payload != nil &&
		msg.payload.Phase == "" && msg.payload.Iteration == 0 &&
		(msg.payload.StreamContent != "" || msg.payload.ReasoningStreamContent != "")
	if isStreamOnly {
		if m.progress != nil {
			if msg.payload.StreamContent != "" {
				m.progress.StreamContent = msg.payload.StreamContent
			}
			if msg.payload.ReasoningStreamContent != "" {
				m.progress.ReasoningStreamContent = msg.payload.ReasoningStreamContent
			}
			// Refresh lastTokenUsage from current progress so the context bar
			// stays visible even when structured events are lost to progressCh
			// coalescing (stream-only events evicting structured events).
			m.cacheTokenUsage(m.progress.TokenUsage)
		} else if m.typing {
			// Turn started but no structured progress yet — create minimal payload
			if msg.payload.CWD == "" && m.progress != nil {
				msg.payload.CWD = m.progress.CWD
			}
			// Preserve CWD from previous progress if new payload doesn't have it.
			if msg.payload.CWD == "" && m.progress != nil {
				msg.payload.CWD = m.progress.CWD
			}
			// Preserve CWD from previous progress if new payload doesn't have it.
			if msg.payload.CWD == "" && m.progress != nil {
				msg.payload.CWD = m.progress.CWD
			}
			m.progress = msg.payload
		}
		return
	}
	// Structured (non-stream-only) payload: replace m.progress.
	// Carrying forward stream content (same-iteration only) is handled by
	// carryForwardProgressState below — the single source of truth for all
	// carry-forward logic.
	if msg.payload == nil {
		m.progress = nil
		return
	}
	// Preserve CWD from previous progress if new payload doesn't have it.
	if msg.payload.CWD == "" && m.progress != nil {
		msg.payload.CWD = m.progress.CWD
	}
	m.progress = msg.payload

	if m.cachedMaxContextTokens == 0 {
		m.cachedMaxContextTokens = m.resolveMaxContextTokens()
	}
	if m.cachedCompressRatio == 0 {
		m.cachedCompressRatio = m.resolveCompressRatio()
	}
	if m.cachedMaxOutputTokens == 0 {
		m.cachedMaxOutputTokens = m.resolveMaxOutputTokens()
	}

	// Restore iteration history from reconnect/GetActiveProgress snapshot.
	m.restoreIterationHistory(m.progress)

	m.carryForwardProgressState(prev)

	// Update bg task count from callback
	if m.bgTaskCountFn != nil {
		m.bgTaskCount = m.bgTaskCountFn()
	}
	// Update agent count from callback
	if m.agentCountFn != nil {
		m.agentCount = m.agentCountFn()
	}

	// HistoryCompacted: context compression replaced the engine's message list.
	// Clear stale messages immediately so the user doesn't see outdated content
	// during the async reload, then rebuild from session storage.
	// Also clear the token usage bar — compressed context has far fewer tokens.
	if msg.payload != nil && msg.payload.HistoryCompacted {
		m.lastTokenUsage = nil
		m.messages = make([]cliMessage, 0, cliMsgBufSize)
		m.streamingMsgIdx = -1
		m.invalidateAllCache(true)
		m.viewport.GotoBottom()
		m.reloadMessagesFromSession()
	}

	// Cache token usage for context bar display — every progress event
	// carries fresh token counts from the agent's updateTokenUsage().
	// Must run after HistoryCompacted so the compressed estimate overwrites
	// the nil set above, rather than being cleared by it.
	if m.progress != nil {
		m.cacheTokenUsage(m.progress.TokenUsage)
	}

	if msg.payload != nil {
		// Sync todo items from progress event
		m.syncProgressTodos(msg.payload)
		// Detect iteration change and snapshot previous iteration
		m.snapshotIterationChange(msg.payload, prev)

		// Record per-iteration reasoning from structured progress.
		if m.progress != nil && m.progress.Reasoning != "" && m.progress.Iteration >= 0 {
			if m.reasoningByIter == nil {
				m.reasoningByIter = make(map[int]string)
			}
			m.reasoningByIter[m.progress.Iteration] = m.progress.Reasoning
		}

		// §2 工具可视化：快照 CompletedTools 到独立字段
		// Accept all completed tools regardless of their Iteration field — they
		// represent work that finished and should be displayed.
		if len(msg.payload.CompletedTools) > 0 {
			m.lastCompletedTools = make([]protocol.ToolProgress, len(msg.payload.CompletedTools))
			copy(m.lastCompletedTools, msg.payload.CompletedTools)
		}
		if msg.payload.Phase == "done" {
			m.handleProgressDone(msg, prev, turnID)
		}
	}
	m.updateViewportContent()
}

// handleSessionStateMsg processes server-pushed session state change events.
// Runs inside BubbleTea Update() — no goroutines, no RPC, no locks.
func (m *cliModel) handleSessionStateMsg(msg cliSessionStateMsg) {
	ev := msg.event
	log.WithFields(log.Fields{
		"action":  ev.Action,
		"chat_id": ev.ChatID,
		"channel": ev.Channel,
		"role":    ev.Role,
	}).Debug("handleSessionStateMsg: received session event")
	switch ev.Action {
	case "busy":
		// Main session started processing.
		m.liveSessionStates[ev.ChatID] = &liveSessionState{busy: true}
	case "idle":
		// Main session finished processing.
		// Explicitly mark as idle (not delete) — the 30s safety-net poll
		// may return stale Busy=true from cache, so we need the override.
		m.liveSessionStates[ev.ChatID] = &liveSessionState{busy: false}
	case "subagent_started":
		// SubAgent interactive session created.
		key := "agent:" + ev.Role + "/" + ev.Instance
		m.liveSessionStates[key] = &liveSessionState{
			busy:     true,
			role:     ev.Role,
			instance: ev.Instance,
			parentID: ev.ParentID,
		}
		// New session appeared — trigger async cache refresh so sidebar shows it.
		m.scheduleSessionsRefresh()
	case "subagent_stopped":
		// SubAgent interactive session destroyed.
		key := "agent:" + ev.Role + "/" + ev.Instance
		// Explicitly mark as idle (not delete) — same reason as main session idle.
		m.liveSessionStates[key] = &liveSessionState{
			busy:     false,
			role:     ev.Role,
			instance: ev.Instance,
			parentID: ev.ParentID,
		}
		// Session disappeared — trigger async cache refresh so sidebar updates.
		m.scheduleSessionsRefresh()
	case "renamed":
		// Session renamed via config tool or API — trigger cache refresh so sidebar updates immediately.
		m.scheduleSessionsRefresh()
	}
}

// scheduleSessionsRefresh triggers an immediate session list cache refresh.
// Called when sessions are created/destroyed via server push events.
func (m *cliModel) scheduleSessionsRefresh() {
	if m.channel != nil && m.channel.config.SessionsListRefresh != nil {
		m.channel.config.SessionsListRefresh()
	}
}

// syncProgressTodos syncs todo items from the progress payload.
func (m *cliModel) syncProgressTodos(payload *protocol.ProgressEvent) {
	if payload == nil {
		return
	}
	if len(payload.Todos) > 0 {
		allDone := true
		for _, t := range payload.Todos {
			if !t.Done {
				allDone = false
				break
			}
		}
		if m.todosDoneCleared && allDone {
			// Already cleared by user input; don't re-accept stale all-done list
		} else {
			m.todos = make([]protocol.TodoItem, len(payload.Todos))
			copy(m.todos, payload.Todos)
			m.todosDoneCleared = false
			m.relayoutViewport()
			// Persist to TodoManager so todos survive turn end and session switches.
			m.persistTodosToManager()
		}
	}
	// When payload.Todos is empty, do NOT clear m.todos.
	// An empty Todos field only means "this progress event carries no todo data"
	// (e.g. early thinking phase before todo_write executes), not "todos were deleted".
	// TODOs are cleared only by: user sending a new message (todosDoneCleared),
	// turn ending with all done (endAgentTurn), or explicit todo_write([]).
}

// persistTodosToManager writes m.todos to the CLI-side todoManager
// for cross-turn and cross-session persistence.
func (m *cliModel) persistTodosToManager() {
	if m.todoManager == nil {
		return
	}
	key := m.sessionKey()
	if key == "" {
		return
	}
	if len(m.todos) == 0 {
		m.todoManager.SetTodos(key, nil)
		return
	}
	// m.todos is already []protocol.TodoItem, pass directly.
	items := make([]protocol.TodoItem, len(m.todos))
	copy(items, m.todos)
	m.todoManager.SetTodos(key, items)
}

// snapshotIterationChange detects iteration changes and snapshots the previous
// iteration's tools/reasoning into iteration history.
func (m *cliModel) snapshotIterationChange(payload *protocol.ProgressEvent, prev *protocol.ProgressEvent) {
	if payload == nil {
		return
	}
	if payload.Iteration > m.lastSeenIteration && m.lastSeenIteration >= 0 && prev != nil {
		// Guard: only create snapshot if prev actually belongs to lastSeenIteration.
		// After session switch, resetProgressState sets lastSeenIteration=0 but
		// the restored m.progress has Iteration=N. When the next live progress
		// arrives, prev (which came from the restore) has Iteration=N, not 0.
		// Snapshoting "iteration 0" with iteration N's data would cause #0 and #1
		// to display the same reasoning content.
		if prev.Iteration != m.lastSeenIteration {
			// Data mismatch: prev belongs to a different iteration than what
			// lastSeenIteration claims. Skip the snapshot to avoid corruption,
			// but update the counter to prevent repeated misfires.
			m.lastSeenIteration = payload.Iteration
			m.iterationStartTime = time.Now()
			return
		}
		prevIterTools := prev.CompletedTools
		// Also include ActiveTools that completed (status=done/error) but
		// haven't been moved to CompletedTools yet by progressFinalizer.
		for _, t := range prev.ActiveTools {
			if t.Status == "done" || t.Status == "error" {
				prevIterTools = append(prevIterTools, t)
			}
		}
		prevReasoning := prev.Reasoning
		if prevReasoning == "" {
			prevReasoning = m.reasoningByIter[m.lastSeenIteration]
		}
		if prevReasoning == "" {
			prevReasoning = prev.ReasoningStreamContent
		}
		if len(prevIterTools) > 0 || prev.Thinking != "" || prevReasoning != "" {
			snap := cliIterationSnapshot{
				Iteration:   m.lastSeenIteration,
				Thinking:    prev.Thinking,
				Reasoning:   prevReasoning,
				Tools:       prevIterTools,
				ElapsedWall: time.Since(m.iterationStartTime).Milliseconds(),
			}
			m.iterationHistory = append(m.iterationHistory, snap)
		}
		m.lastCompletedTools = m.lastCompletedTools[:0]
		m.lastSeenIteration = payload.Iteration
		m.iterationStartTime = time.Now()
	}
}

// handleProgressDone handles the Phase "done" case: snapshots the final iteration,
// generates tool summary, resets iteration tracking state, and synthesizes agent messages.
func (m *cliModel) handleProgressDone(msg cliProgressMsg, prev *protocol.ProgressEvent, turnID uint64) {
	// When turn was cancelled (Ctrl+C), still generate tool_summary from
	// accumulated iterationHistory so tool records survive rewind operations.
	// Without this, cancelled turns lose their tool records because iterationHistory
	// is cleared by endAgentTurn, and no tool_summary message exists in m.messages.
	// (Restarting the client restores them via ConvertMessagesToHistory from DB,
	// proving the data is valid — we just need to persist it in-memory too.)
	if m.turnCancelled {
		// Snapshot the final iteration (same logic as the non-cancelled path below).
		// This is needed because snapshotIterationChange only fires on iteration
		// *changes* (N→N+1), so a single-iteration turn won't have any snapshots yet.
		if m.lastSeenIteration >= 0 {
			alreadySnapped := slices.ContainsFunc(m.iterationHistory, func(s cliIterationSnapshot) bool {
				return s.Iteration == m.lastSeenIteration
			})
			if !alreadySnapped {
				var finalTools []protocol.ToolProgress
				finalTools = append(finalTools, msg.payload.CompletedTools...)
				for _, t := range msg.payload.ActiveTools {
					if t.Status == "done" || t.Status == "error" {
						if !slices.ContainsFunc(finalTools, func(existing protocol.ToolProgress) bool {
							return existing.Name == t.Name && existing.Label == t.Label
						}) {
							finalTools = append(finalTools, t)
						}
					}
				}
				for _, t := range m.lastCompletedTools {
					if !slices.ContainsFunc(finalTools, func(existing protocol.ToolProgress) bool {
						return existing.Name == t.Name && existing.Label == t.Label
					}) {
						finalTools = append(finalTools, t)
					}
				}
				snap := cliIterationSnapshot{
					Iteration:   m.lastSeenIteration,
					Thinking:    msg.payload.Thinking,
					Tools:       finalTools,
					ElapsedWall: time.Since(m.iterationStartTime).Milliseconds(),
				}
				if m.reasoningByIter != nil {
					snap.Reasoning = m.reasoningByIter[m.lastSeenIteration]
				}
				if snap.Reasoning == "" {
					snap.Reasoning = m.lastReasoning
				}
				if snap.Reasoning == "" {
					snap.Reasoning = msg.payload.Reasoning
				}
				if len(finalTools) > 0 || snap.Thinking != "" || snap.Reasoning != "" {
					m.iterationHistory = append(m.iterationHistory, snap)
				}
			}
		}
		if len(m.iterationHistory) > 0 {
			toolSummaryMsg := cliMessage{
				role:       "tool_summary",
				content:    "",
				timestamp:  time.Now(),
				iterations: append([]cliIterationSnapshot{}, m.iterationHistory...),
				dirty:      true,
			}
			m.upsertMessageByTurn(turnID, "tool_summary", toolSummaryMsg)
			m.renderCacheValid = false
		}
		m.setTurnDoneProcessed(turnID)
		m.endAgentTurn(turnID)
		if turnID == m.agentTurnID {
			m.inputReady = true
			if len(m.messageQueue) > 0 {
				m.needFlushQueue = true
			}
		}
		return
	}
	// Snapshot the final iteration before clearing progress.
	// This handles the case where PhaseDone arrives before
	// handleAgentMessage (e.g. agent error/cancel).
	// Skip if handleAgentMessage already processed (m.typing == false
	// means the reply arrived and cleaned up iteration state).
	if m.typing && m.lastSeenIteration >= 0 {
		alreadySnapped := slices.ContainsFunc(m.iterationHistory, func(s cliIterationSnapshot) bool {
			return s.Iteration == m.lastSeenIteration
		})
		if !alreadySnapped {
			var finalTools []protocol.ToolProgress
			// Check progress.CompletedTools first (set by progressFinalizer)
			finalTools = append(finalTools, msg.payload.CompletedTools...)
			// Also include ActiveTools(done) not yet moved by progressFinalizer
			for _, t := range msg.payload.ActiveTools {
				if t.Status == "done" || t.Status == "error" {
					if !slices.ContainsFunc(finalTools, func(existing protocol.ToolProgress) bool {
						return existing.Name == t.Name && existing.Label == t.Label
					}) {
						finalTools = append(finalTools, t)
					}
				}
			}
			// Also include any from lastCompletedTools (race safety)
			for _, t := range m.lastCompletedTools {
				if !slices.ContainsFunc(finalTools, func(existing protocol.ToolProgress) bool {
					return existing.Name == t.Name && existing.Label == t.Label
				}) {
					finalTools = append(finalTools, t)
				}
			}
			snap := cliIterationSnapshot{
				Iteration:   m.lastSeenIteration,
				Thinking:    msg.payload.Thinking,
				Tools:       finalTools,
				ElapsedWall: time.Since(m.iterationStartTime).Milliseconds(),
			}
			// Carry over reasoning: priority is reasoningByIter (per-iteration, authoritative)
			// > lastReasoning (captured before progress clear)
			// > prev progress Reasoning (server-authoritative, from ReasoningContent)
			// > PhaseDone payload Reasoning
			reasoning := m.reasoningByIter[m.lastSeenIteration]
			if reasoning == "" {
				reasoning = m.lastReasoning
			}
			if reasoning == "" && prev != nil {
				reasoning = prev.Reasoning
			}
			if reasoning == "" {
				reasoning = msg.payload.Reasoning
			}
			snap.Reasoning = reasoning
			if len(finalTools) > 0 || snap.Thinking != "" || snap.Reasoning != "" {
				m.iterationHistory = append(m.iterationHistory, snap)
			}
		}
		// Generate tool_summary if we have iteration history.
		// Use upsert to avoid duplicates when PhaseDone fires multiple times
		// (e.g. cancel + late tool completion).
		if len(m.iterationHistory) > 0 {
			toolSummaryMsg := cliMessage{
				role:       "tool_summary",
				content:    "",
				timestamp:  time.Now(),
				iterations: append([]cliIterationSnapshot{}, m.iterationHistory...),
				dirty:      true,
			}
			m.upsertMessageByTurn(turnID, "tool_summary", toolSummaryMsg)
			m.pendingToolSummary = nil // upsert replaces the slot; no need for separate pending
			m.renderCacheValid = false
		}
	}
	// Mark this turn as done-processed (tool_summary created, turn ending).
	m.setTurnDoneProcessed(turnID)

	// Reset all iteration tracking state (always, even if handleAgentMessage ran first)
	m.endAgentTurn(turnID) // also clears todos and does relayoutViewport
	if turnID == m.agentTurnID {
		m.inputReady = true
		if len(m.messageQueue) > 0 {
			m.needFlushQueue = true
		}
	}

	// For agent sessions (interactive SubAgent viewer), the outbound
	// message goes back to the parent agent's channel/chatID — it never
	// arrives as a cliOutboundMsg for this session view. So we must
	// synthesize the assistant message from the progress payload's final
	// content (Thinking field carries the last clean assistant text).
	// For main sessions, handleAgentMessage handles this and will
	// relocate the tool_summary before the assistant reply.
	if m.channelName == "agent" && !m.typing {
		assistantContent := msg.payload.Thinking
		if assistantContent == "" {
			assistantContent = msg.payload.StreamContent
		}
		if assistantContent != "" {
			m.upsertMessageByTurn(turnID, "assistant", cliMessage{
				role:      "assistant",
				content:   assistantContent,
				timestamp: time.Now(),
				dirty:     true,
			})
			m.setTurnReplyReceived(turnID)
			m.renderCacheValid = false
		}
	}

	m.relayoutViewport()
}

// handleInjectedUserMsg processes user messages injected by the agent (e.g. bg task completion).
func (m *cliModel) handleInjectedUserMsg(msg cliInjectedUserMsg) []tea.Cmd {
	// suLoading guard: during session switch in remote mode, discard injected messages.
	// They belong to the previous session's context; the RPC will handle state.
	if m.suLoading {
		return nil
	}
	// Filter by session: if chatID is set, only apply to matching session.
	// Legacy messages (chatID="") are always applied for backward compat.
	if msg.chatID != "" {
		currentKey := m.channelName + ":" + m.chatID
		if msg.chatID != currentKey {
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
	m.renderCacheValid = false
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

// handleSuHistoryLoad processes /su user switch history load results.
// Returns tea.Cmds to start the tick chain when active progress is restored.
func (m *cliModel) handleSuHistoryLoad(msg suHistoryLoadMsg) []tea.Cmd {
	// Stale result guard: if user switched away from the target session
	// while the async load was in-flight, discard the result entirely.
	// Do NOT clear suLoading on stale callbacks — the new session's loading
	// guard is set by its own postRestoreSessionSetup call.
	if msg.channelName != m.channelName || msg.chatID != m.chatID {
		return nil
	}

	// Only clear suLoading for the matching session.
	m.suLoading = false

	if msg.err != nil {
		m.showSystemMsg(fmt.Sprintf(m.locale.SuLoadFailed, msg.err), feedbackWarning)
		// Clear pendingUserMsg even on error. If we leave it set, the stale
		// reference gets saved in sessionState and restored on every subsequent
		// switch, potentially creating duplicate user messages when history
		// eventually loads successfully.
		m.pendingUserMsg = nil
		// RPC failed — no authoritative data. Enable input so the user can retry.
		// Also force typing=false: restored state was a hint, but without server
		// confirmation we cannot know the real turn state. Assuming idle is the
		// safe fallback (prevents perpetual spinner from stuck typing=true).
		m.typing = false
		m.progress = nil
		m.inputReady = true
	} else {
		// Build a dedup set from existing messages.
		// Key uses role + timestamp to handle sequences of identical-role
		// messages (e.g. multiple tool_summary with empty content).
		existingKeys := make(map[string]bool, len(m.messages))
		for _, cm := range m.messages {
			existingKeys[cm.role+"|"+cm.timestamp.Format(time.RFC3339Nano)] = true
		}
		for _, hm := range msg.history {
			key := hm.Role + "|" + hm.Timestamp.Format(time.RFC3339Nano)
			if existingKeys[key] {
				continue // already in messages, skip duplicate
			}
			existingKeys[key] = true
			cm := cliMessage{
				role:      hm.Role,
				content:   hm.Content,
				timestamp: hm.Timestamp,
				isPartial: false,
				dirty:     true,
			}
			if len(hm.Iterations) > 0 {
				cm.iterations = make([]cliIterationSnapshot, len(hm.Iterations))
				for i, hi := range hm.Iterations {
					cm.iterations[i] = cliIterationSnapshot(hi)
				}
			}
			m.messages = append(m.messages, cm)
		}
		// Restore pending user message if it was sent but not yet persisted to DB.
		// This handles the race where the user sends a message and quickly switches
		// sessions before the agent's eager-save completes.
		if m.pendingUserMsg != nil {
			found := false
			for _, existing := range m.messages {
				if existing.role == "user" && existing.content == m.pendingUserMsg.content {
					found = true
					break
				}
			}
			if !found {
				m.pendingUserMsg.dirty = true
				m.messages = append(m.messages, *m.pendingUserMsg)
			}
			m.pendingUserMsg = nil
		}
		// SuSwitchedHistory提示已移除 — 切换session静默完成
	}
	m.invalidateAllCache(false)
	m.viewport.GotoBottom()

	// Restore active progress for seamless session switch.
	// msg.activeProgress (from GetActiveProgress RPC) is the authoritative source:
	// if the server says the turn is done or gone, any saved state from
	// restoreSession() is stale and must be discarded.
	// suPhaseDoneConfirmed: PhaseDone arrived during suLoading (before this
	// RPC completed). The server confirmed the turn is done — the RPC snapshot
	// is stale. Skip acceptProgress to avoid restoring a stuck spinner.
	var cmds []tea.Cmd
	var acceptProgress bool
	if !m.suPhaseDoneConfirmed && msg.activeProgress != nil && msg.activeProgress.Phase != "done" {
		acceptProgress = true
		// Cross-session guard: activeProgress from GetActiveProgress RPC
		// should match the current session. If ChatID is set and doesn't
		// match, treat as no active progress (fall through to default).
		if msg.activeProgress.ChatID != "" {
			currentKey := m.channelName + ":" + m.chatID
			if msg.activeProgress.ChatID != currentKey {
				acceptProgress = false
			}
		}
	}
	switch {
	case acceptProgress:
		// Turn is still active on the server. Use the server snapshot regardless
		// of whether restoreSession() also restored state — the server snapshot
		// has the freshest progress data.
		if !m.typing {
			m.startAgentTurn()
		}
		// startAgentTurn calls resetProgressState which sets lastSeenIteration=0.
		// Restore it from the server snapshot to prevent snapshotIterationChange
		// from creating a spurious "iteration 0" snapshot on the next live
		// progress event (symptom: #0 and #1 both show the same reasoning).
		if msg.activeProgress.Iteration > 0 {
			m.lastSeenIteration = msg.activeProgress.Iteration
		}
		m.progress = msg.activeProgress

		// Sync todos from server snapshot so the todo bar shows them
		// immediately without waiting for the next live progress event.
		m.syncProgressTodos(msg.activeProgress)

		// Restore token usage from server snapshot so the context bar
		// doesn't disappear on session switch. Without this, lastTokenUsage
		// stays nil (cleared by session switch paths) and the context bar
		// only reappears with the next live progress event.
		m.cacheTokenUsage(msg.activeProgress.TokenUsage)
		// Resolve cached context settings from current session's config.
		if m.cachedMaxContextTokens == 0 {
			m.cachedMaxContextTokens = m.resolveMaxContextTokens()
		}
		if m.cachedCompressRatio == 0 {
			m.cachedCompressRatio = m.resolveCompressRatio()
		}
		if m.cachedMaxOutputTokens == 0 {
			m.cachedMaxOutputTokens = m.resolveMaxOutputTokens()
		}

		// Restore StartedAt for active tools so live elapsed timers work.
		for i := range m.progress.ActiveTools {
			t := &m.progress.ActiveTools[i]
			if t.StartedAt.IsZero() && t.Elapsed > 0 {
				t.StartedAt = time.Now().Add(-time.Duration(t.Elapsed) * time.Millisecond)
			}
		}

		// Rebuild iteration history from server snapshot (authoritative).
		m.iterationHistory = nil
		m.invalidateProgressHistoryCache()
		if len(msg.activeProgress.IterationHistory) > 0 {
			for _, ih := range msg.activeProgress.IterationHistory {
				snap := cliIterationSnapshot{
					Iteration: ih.Iteration,
					Thinking:  ih.Thinking,
					Reasoning: ih.Reasoning,
					Tools:     ih.CompletedTools,
				}
				for i := range snap.Tools {
					t := &snap.Tools[i]
					if t.StartedAt.IsZero() && t.Elapsed > 0 {
						t.StartedAt = time.Now().Add(-time.Duration(t.Elapsed) * time.Millisecond)
					}
				}
				m.iterationHistory = append(m.iterationHistory, snap)
			}
			if len(m.iterationHistory) > 0 {
				lastIter := m.iterationHistory[len(m.iterationHistory)-1].Iteration
				if lastIter > m.lastSeenIteration {
					m.lastSeenIteration = lastIter
				}
			}
		}
		// Remove the LAST tool_summary from loaded history. The active
		// progress block owns the current turn's iteration display — the
		// static tool_summary from the active turn would duplicate content
		// and its iteration numbers (globally cumulative from DB) don't
		// match the progress block's per-turn numbers.
		// Only the last tool_summary is removed. Previous turns' tool_summaries
		// (including interrupted turns without assistant replies) are preserved
		// — they have no live progress panel to replace them.
		m.removeLastToolSummary()

		// Fallback: if server returned Iteration=0 but iteration history
		// has entries, derive the current iteration from history max.
		// This handles a server-side quirk where activeProgress.Iteration
		// is 0 but IterationHistory is populated during SubAgent session
		// switches (symptom: progress shows #0 while history shows
		// correct #1, #2, ...).
		if m.progress != nil && m.progress.Iteration <= 0 && len(m.iterationHistory) > 0 {
			m.progress.Iteration = m.iterationHistory[len(m.iterationHistory)-1].Iteration
		}

		// Emit a tickCmd to guarantee the fast tick chain is running.
		// Emit a tickCmd to kick the tick chain after restoring.
		// If the restored progress has stream or reasoning content, start the
		// typewriter tick immediately. Without this, the cursor won't blink and
		// streaming content won't animate until the next handleTickMsg cycle.
		hasStream := m.progress != nil && m.progress.StreamContent != "" && m.twVisible < len([]rune(m.progress.StreamContent))
		hasReasoning := m.progress != nil && m.progress.ReasoningStreamContent != "" && m.rwVisible < len([]rune(m.progress.ReasoningStreamContent))
		if !m.typewriterTickActive && (hasStream || hasReasoning) {
			m.typewriterTickActive = true
			cmds = append(cmds, typewriterTickCmd())
		}

	default:
		// Turn is not active (nil or PhaseDone). If restoreSession() restored
		// a stale typing=true state, clear it — the server snapshot is authoritative.
		if m.typing {
			m.endAgentTurn(m.agentTurnID)
		}
		// Independent guard: clear stale progress that restoreSession() may have
		// restored from a previous visit. The session switch handler sets typing=false
		// before this async handler runs, so endAgentTurn's typing guard above may
		// not fire. But progress can still be non-nil → renderProgressBlock would
		// show a phantom progress block.
		if m.progress != nil {
			m.progress = nil
			m.renderCacheValid = false
		}
		// Server says session is idle — enable input.
		m.inputReady = true

		// Apply server-side todos from the RPC response, overwriting
		// the local TodoManager cache. This ensures the first session
		// switch after TUI startup shows fresh data from the server.
		// nil means "RPC unavailable" (keep local cache).
		// empty slice means "server has no todos" (clear local cache).
		if msg.todos != nil {
			if len(msg.todos) > 0 {
				m.todos = make([]protocol.TodoItem, len(msg.todos))
				copy(m.todos, msg.todos)
				m.todosDoneCleared = false
				m.persistTodosToManager()
			} else {
				m.todos = nil
				if m.todoManager != nil {
					m.todoManager.SetTodos(m.sessionKey(), nil)
				}
			}
			m.relayoutViewport()
		}
		// If the restored session has queued messages, schedule a flush.
		// postRestoreSessionSetup clears needFlushQueue for safety; this is the
		// authoritative re-enable point after the RPC confirms the session is idle.
		if len(m.messageQueue) > 0 {
			m.needFlushQueue = true
		}
		// Start a tick chain even when idle, so handleTickMsg can evaluate
		// sidebarHasBusySessions and animate sidebar spinners for non-active
		// busy sessions.
		// Reload history to pick up messages that arrived while we were viewing
		// another session (e.g. the assistant's final reply was filtered out by
		// ChatID check during the agent session view).
		if loader := m.channel.config.DynamicHistoryLoader; loader != nil {
			ch, cid := m.channelName, m.chatID
			cmds = append(cmds, func() tea.Msg {
				history, err := loader(ch, cid)
				if err != nil {
					return cliHistoryReloadMsg{channelName: ch, chatID: cid, err: err}
				}
				return cliHistoryReloadMsg{channelName: ch, chatID: cid, history: history}
			})
		}
	}
	// Fallback: restore lastTokenUsage from persisted token state when
	// active progress didn't provide it (e.g. idle session, first switch
	// after startup). Without this, the context bar shows 0 until the
	// first live progress event of the new session.
	if m.lastTokenUsage == nil && (msg.tokenPrompt > 0 || msg.tokenCompletion > 0) {
		m.lastTokenUsage = &protocol.TokenUsage{
			PromptTokens:     msg.tokenPrompt,
			CompletionTokens: msg.tokenCompletion,
			TotalTokens:      msg.tokenPrompt + msg.tokenCompletion,
		}
	}
	// Always check for pending AskUser questions after history load.
	// This covers both active turns (agent paused waiting for user) and
	// idle sessions (pending from a previous session that was never answered).
	cmds = append(cmds, m.checkAndRestorePendingAskUser())
	return cmds
}

// handleHistoryReload rebuilds m.messages from session storage after context compression.
// Unlike /su which appends, this REPLACES the entire message list because compression
// may have replaced many old messages with a single [Compacted context] summary.
func (m *cliModel) handleHistoryReload(msg cliHistoryReloadMsg) {
	// Stale guard: discard results from a different session.
	if msg.channelName != m.channelName || msg.chatID != m.chatID {
		return
	}
	if msg.err != nil {
		log.WithError(msg.err).Warn("Failed to reload history after compression")
		return
	}
	var newMessages []cliMessage
	for _, hm := range msg.history {
		cm := cliMessage{
			role:      hm.Role,
			content:   hm.Content,
			timestamp: hm.Timestamp,
			isPartial: false,
			dirty:     true,
		}
		if len(hm.Iterations) > 0 {
			cm.iterations = make([]cliIterationSnapshot, len(hm.Iterations))
			for i, hi := range hm.Iterations {
				cm.iterations[i] = cliIterationSnapshot(hi)
			}
		}
		newMessages = append(newMessages, cm)
	}
	// Restore pending user message if missing (same race as handleSuHistoryLoad)
	if m.pendingUserMsg != nil {
		found := false
		for _, existing := range newMessages {
			if existing.role == "user" && existing.content == m.pendingUserMsg.content {
				found = true
				break
			}
		}
		if !found {
			m.pendingUserMsg.dirty = true
			newMessages = append(newMessages, *m.pendingUserMsg)
		}
		m.pendingUserMsg = nil
	}
	m.messages = newMessages
	m.streamingMsgIdx = -1
	m.invalidateAllCache(false)
	m.updateViewportContent()
	m.viewport.GotoBottom()
	log.WithField("count", len(newMessages)).Info("History reloaded after compression")

	// Refresh token state so the context bar updates immediately after compression.
	// Without this, the bar stays at the pre-compression (high) value or nil
	// until the next progress event arrives.
	m.refreshTokenStateAfterReload()
}

// handleSplashTick processes splash animation frames.

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

// maxTreeDepth returns the maximum depth of the SubAgent tree (1 for top-level nodes).
// mergeSubAgentTrees merges new SubAgent data into the previous tree.
// Agents present in both trees are updated with new data (status, tools, description).
// Agents only in prev are kept as-is (they may have completed between server updates).
// Agents only in new are added.
//
// Uniqueness key: Role + ":" + Instance. When Instance is empty, Role alone is used.
// This prevents same-role different-instance agents from being merged into one.
//
// Key rule: if an agent in prev is NOT in new, it means the server stopped reporting
// it. This is normal — the server only reports actively-running agents. We mark
// stale running/pending agents as "done" so they don't linger in the progress panel
// (Issue #29: zombie agents that completed but were never marked done by the server).
func mergeSubAgentTrees(prev, new []protocol.SubAgentInfo) []protocol.SubAgentInfo {
	if len(prev) == 0 {
		return new
	}
	if len(new) == 0 {
		// Server stopped reporting all agents — they completed.
		// Return empty slice (no zombies). Previous carry-forward
		// in carryForwardProgressState will handle pruning.
		return nil
	}

	// Build lookup from new by unique key (Role + Instance)
	newByKey := make(map[string]int, len(new))
	for i, a := range new {
		key := subAgentKey(a.Role, a.Instance)
		newByKey[key] = i
	}

	result := make([]protocol.SubAgentInfo, 0, len(prev)+len(new))

	// Start with all prev entries, updating those that have new data
	for _, p := range prev {
		key := subAgentKey(p.Role, p.Instance)
		if idx, ok := newByKey[key]; ok {
			// Agent exists in both — merge: use new data but preserve
			// previous Desc when new is empty (SubAgent progress may
			// report an empty Desc between activity bursts).
			n := new[idx]
			merged := n
			if merged.Desc == "" && p.Desc != "" {
				merged.Desc = p.Desc
			}
			merged.Children = mergeSubAgentTrees(p.Children, n.Children)
			result = append(result, merged)
			delete(newByKey, key)
		} else {
			// Agent only in prev — server stopped reporting it.
			// If already done/error, skip it (zombie cleanup — prevents
			// completed agents from accumulating in the tree forever).
			// If still running/pending, mark as done (it completed between
			// updates) but also skip — the user already saw it finish.
			_ = markDoneIfRunning(p) // mark children recursively
		}
	}

	// Add agents only in new
	for key := range newByKey {
		result = append(result, new[newByKey[key]])
	}

	return result
}

// subAgentKey builds a unique key for a SubAgent from Role and Instance.
func subAgentKey(role, instance string) string {
	if instance == "" {
		return role
	}
	return role + ":" + instance
}

// markDoneIfRunning marks a SubAgent and its children as done if they are
// still in running/pending state. This handles the case where the server
// stops reporting a completed SubAgent — without this, the agent would
// linger as "running" forever (Issue #29).
func markDoneIfRunning(sa protocol.SubAgentInfo) protocol.SubAgentInfo {
	if sa.Status == "running" || sa.Status == "pending" {
		sa.Status = "done"
	}
	for i := range sa.Children {
		sa.Children[i] = markDoneIfRunning(sa.Children[i])
	}
	return sa
}

// pruneDoneSubAgents removes agents (and their children) that are already
// marked "done". This prevents zombie entries from accumulating across
// iteration boundaries when no new SubAgent data arrives.
// Agents still "running" or "pending" are kept (they may complete soon).
func pruneDoneSubAgents(agents []protocol.SubAgentInfo) []protocol.SubAgentInfo {
	var kept []protocol.SubAgentInfo
	for _, a := range agents {
		a.Children = pruneDoneSubAgents(a.Children)
		if a.Status != "done" && a.Status != "error" {
			kept = append(kept, a)
		}
	}
	return kept
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

// handleSwitchLLMDoneMsg processes async subscription switch completion.
// Returns (model, cmd, handled).
func (m *cliModel) handleSwitchLLMDoneMsg(done cliSwitchLLMDoneMsg) (tea.Model, tea.Cmd, bool) {
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
			// Build complete session LLM state and persist atomically.
			// maxContext comes from the new subscription's per-model config.
			// Do NOT fallback to the old session JSON value — that belongs
			// to the previous subscription and would show a stale context bar.
			state := SessionLLMState{
				SubscriptionID:   done.subID,
				Model:            done.subModel,
				MaxContextTokens: done.maxCtx,
				MaxOutputTokens:  done.maxOutTok,
			}
			SaveSessionLLMState(m.workDir, m.chatID, state)
			m.applySessionLLMState(state)
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
	var cmd tea.Cmd
	if len(m.pendingCmds) > 0 {
		cmd = tea.Batch(m.pendingCmds...)
		m.pendingCmds = nil
	}
	return m, cmd, true
}

// handleTickMsg processes the fast tick (100ms) message.
// Returns tea.Cmds to batch with other commands.

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

	// Spinner / progress update
	sessionActive := m.progress != nil && m.progress.Phase != "done"
	busy := m.typing || sessionActive
	needsSpinnerTick := busy || m.sidebarHasBusySessions

	if (m.bgTaskCountFn != nil && m.bgTaskCount > 0) || (m.agentCountFn != nil && m.agentCount > 0) || needsSpinnerTick {
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
		if !m.renderCacheValid {
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

// handleHistoryLoad loads pre-converted history messages into the model.
func (m *cliModel) handleHistoryLoad(msg cliHistoryLoadMsg) {
	// Stale guard: discard results from a different session.
	if msg.channelName != "" && (msg.channelName != m.channelName || msg.chatID != m.chatID) {
		return
	}
	if len(msg.history) > 0 {
		m.messages = append(m.messages, msg.history...)
		m.invalidateAllCache(false)
		m.updateViewportContent()
		if m.streamingMsgIdx < 0 {
			m.viewport.GotoBottom()
		}
		log.WithFields(log.Fields{"count": len(msg.history)}).Info("Applied history load in Update loop")
	}
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
	m.renderCacheValid = false
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
				m.renderCacheValid = false
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
				m.renderCacheValid = false
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
	}
	// NOTE: tick chain is started by startAgentTurn() inside sendMessage().
	// No need to emit tickCmd() here — doing so would create duplicate chains.
	return m, cmds, true
}

// handleShiftUp handles Shift+Up for queue recall and input history browsing.
func (m *cliModel) handleShiftUp() (tea.Model, []tea.Cmd, bool) {
	// Shift+Up: recall queued message for editing / browse input history.
	if m.panelMode == "" && m.textarea.Value() != "" {
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
		if m.textarea.Value() == "" && len(m.inputHistory) > 0 {
			if m.inputHistoryIdx == -1 {
				m.inputDraft = "" // 保存空草稿
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
	if m.panelMode == "" && m.textarea.Value() != "" {
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

// refreshTokenStateAfterReload fetches the latest token state via RPC
// after a history reload (compression). Pushes the result through asyncCh
// so the context bar updates immediately without waiting for the next
// progress event.
func (m *cliModel) refreshTokenStateAfterReload() {
	tokenFn := m.channel.config.GetTokenStateFn
	if tokenFn == nil {
		return
	}
	ch := m.channel
	chatID := m.chatID
	channelName := m.channelName
	go func() {
		prompt, completion := tokenFn(channelName, chatID)
		if prompt <= 0 && completion <= 0 {
			return
		}
		msg := cliTokenRefreshMsg{
			channelName:     channelName,
			chatID:          chatID,
			tokenPrompt:     prompt,
			tokenCompletion: completion,
		}
		select {
		case ch.asyncCh <- msg:
		default:
		}
	}()
}

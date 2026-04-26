package channel

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"

	tea "charm.land/bubbletea/v2"
	log "xbot/logger"
)

// handleKeyPress processes key press events in the main update loop.
// Returns (model, cmds, handled). If handled is true, the caller should return
// immediately; otherwise, post-switch processing (viewport/textarea update) should continue.
func (m *cliModel) handleKeyPress(msg tea.KeyPressMsg, wasTyping bool) (tea.Model, []tea.Cmd, bool) {

	// 🥚 When easter egg overlay is active, any key dismisses it (except Ctrl+C, already handled above)
	if m.easterEgg != easterEggNone {
		return m, []tea.Cmd{func() tea.Msg { return easterEggDoneMsg{} }}, true
	}

	// 🥚 Konami Code easter egg detection
	if handled, konamiCmd := m.handleKonamiCode(msg); handled {
		return m, konamiCmd, true
	}

	// NOTE: Ctrl+C is handled at the top of Update() — never handle it here.
	// This case only remains to prevent Ctrl+C from falling through to the
	// textarea (which would insert a ^C character).
	switch {
	case msg.String() == "ctrl+c":
		return m, nil, true

	case msg.Code == tea.KeyEsc:
		return m.handleEscKey()

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
		return m.handleShiftUp()

	case msg.Code == tea.KeyUp:
		// Plain ArrowUp: only viewport scroll (no queue recall / history).
		// If textarea has content, let textarea own multiline vertical cursor movement.
		if m.panelMode == "" && m.textarea.Value() != "" {
			break
		}
		// When viewport is not at bottom, arrow keys scroll viewport
		if !m.viewport.AtBottom() {
			m.viewport.ScrollUp(1)
			return m, nil, true
		}

	case msg.Code == tea.KeyDown && msg.Mod == tea.ModShift:
		return m.handleShiftDown()

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
		return m.handleEnterKey()

	case msg.Code == tea.KeyTab:
		// §8 Tab command completion
		m.handleTabComplete()
		return m, nil, true

	case msg.String() == "ctrl+o":
		// §11 Ctrl+O toggle tool summary expand/collapse (compatible with non-CSI-u terminals)
		m.toggleToolSummary()
		return m, nil, true

	case msg.String() == "ctrl+e":
		// §19 Ctrl+E toggle long message folding (intercepted in search navigation mode)
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

// handleKonamiCode checks for Konami Code easter egg key sequence.
// Returns (handled, cmds). If handled is true, the key event is consumed.
func (m *cliModel) handleKonamiCode(msg tea.KeyPressMsg) (bool, []tea.Cmd) {
	if m.easterEgg != easterEggNone {
		return false, nil
	}
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
	// Detect letter keys B and A
	if len(msg.Text) == 1 {
		switch msg.Text[0] {
		case 'b', 'B':
			konamiKey = "b"
		case 'a', 'A':
			konamiKey = "a"
		}
	}
	if konamiKey != "" && m.checkKonami(konamiKey) {
		// Konami Code full sequence match!
		cmd := m.activateEasterEgg(easterEggKonami)
		return true, []tea.Cmd{cmd}
	}
	return false, nil
}

// handleEscKey handles the Escape key: clears input when idle,
// cancels queue editing, or clears input during processing.
func (m *cliModel) handleEscKey() (tea.Model, []tea.Cmd, bool) {
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
}

// handleShiftUp handles Shift+Up: recalls queued messages for editing
// and browses input history when idle.
func (m *cliModel) handleShiftUp() (tea.Model, []tea.Cmd, bool) {
	if m.panelMode == "" && m.textarea.Value() != "" {
		return m, nil, true
	}
	if !m.viewport.AtBottom() {
		return m, nil, true
	}
	// §Q Message queue: Shift+Up during typing to recall last queued message for editing
	if m.panelMode == "" && m.typing && !m.inputReady && len(m.messageQueue) > 0 {
		if !m.queueEditing && m.textarea.Value() == "" {
			// Recall last queued message
			m.queueEditing = true
			m.queueEditBuf = m.messageQueue[len(m.messageQueue)-1]
			m.textarea.SetValue(m.queueEditBuf)
			m.autoExpandInput()
			return m, nil, true
		}
	}
	if m.panelMode == "" && !m.typing {
		// Browse history when input is empty
		if m.textarea.Value() == "" && len(m.inputHistory) > 0 {
			if m.inputHistoryIdx == -1 {
				m.inputDraft = "" // Save empty draft
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

// handleShiftDown handles Shift+Down: browses input history backwards.
func (m *cliModel) handleShiftDown() (tea.Model, []tea.Cmd, bool) {
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

// handleEnterKey handles the Enter key: message queuing during processing,
// @ file completion, and message sending.
func (m *cliModel) handleEnterKey() (tea.Model, []tea.Cmd, bool) {
	var cmds []tea.Cmd

	// Plain Enter sends. Modified/newline-intent variants should fall through to
	// the textarea so its native multiline/internal-scroll behavior works,
	// especially once the input reaches MaxHeight.
	// Note: ctrl+j is handled earlier in Update() via isCtrlJ() → InsertString("\n").
	// Note: cycleModel uses Ctrl+N (not Ctrl+M), so no need to intercept here.
	// Enter sends message
	if !m.inputReady {
		// §Q Message queue: allow queuing messages during typing
		if m.queueEditing {
			// Editing queued message → save edit result
			m.messageQueue[len(m.messageQueue)-1] = m.textarea.Value()
			m.queueEditing = false
			m.queueEditBuf = ""
			m.textarea.SetValue("")
			return m, nil, true
		}
		if m.textarea.Value() != "" {
			m.messageQueue = append(m.messageQueue, m.textarea.Value())
			m.textarea.SetValue("")
			// Show queue hint
			if len(m.messageQueue) == 1 {
				m.showTempStatus(fmt.Sprintf(m.locale.MessageQueuedUp, len(m.messageQueue)))
			} else {
				m.showTempStatus(fmt.Sprintf(m.locale.MessageQueued, len(m.messageQueue)))
			}
			return m, nil, true
		}
		return m, nil, true
	}
	// §8b @ mode: Enter enters directory or confirms file
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
		// §22 Input history: save sent content (deduplicated, don't save / commands and empty input)
		if !strings.HasPrefix(content, "/") {
			if len(m.inputHistory) == 0 || m.inputHistory[0] != content {
				m.inputHistory = append([]string{content}, m.inputHistory...)
				if len(m.inputHistory) > inputHistoryMax {
					m.inputHistory = m.inputHistory[:inputHistoryMax]
				}
			}
		}
		m.inputHistoryIdx = -1
		m.inputDraft = ""
		if m.allTodosDone() {
			m.todos = nil
			m.todosDoneCleared = true
			m.relayoutViewport() // TODO 清除，恢复 viewport 高度
		}
		// Send message (easter egg may return animation cmd)
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

// handleProgressMsg processes progress update events from the agent.
// It is the main coordinator for progress state management, delegating logical
// blocks to focused sub-methods for readability.
func (m *cliModel) handleProgressMsg(msg cliProgressMsg) {
	// Step 1: Filter by session and guard against invalid/cancelled progress.
	if m.filterProgressSession(msg) {
		return
	}

	// Step 2: Handle stream-only payloads (content/reasoning merges).
	if m.mergeStreamOnlyPayload(msg) {
		return
	}

	// Step 3: Assign new progress and restore iteration history from reconnect.
	prev := m.progress
	m.progress = msg.payload
	m.restoreIterationHistory()

	// Step 4: Preserve tool timings and carry forward state from previous progress.
	m.preserveProgressState(prev)

	// Step 5: Preserve SubAgent tree across progress updates.
	m.preserveSubAgentTree(prev)

	// Step 6: Update background callbacks and handle history compaction.
	m.updateProgressCallbacks(msg.payload)

	// Step 7: Sync todo items from progress payload.
	m.syncProgressTodos(msg.payload)

	// Steps 8-10 require payload; skip if nil.
	if msg.payload == nil {
		m.updateViewportContent()
		return
	}

	// Step 8: Detect iteration change and snapshot previous iteration.
	m.snapshotIterationChange(msg.payload, prev)

	// Step 9: Snapshot completed tools for visualization.
	m.snapshotCompletedTools(msg.payload)

	// Step 10: Handle phase "done" — final snapshot, tool summary, state reset.
	if msg.payload.Phase == "done" {
		turnID := m.agentTurnID // capture before endAgentTurn increments it
		m.handlePhaseDone(msg.payload, prev, turnID)
	}

	m.updateViewportContent()
}

// filterProgressSession validates session routing and checks turn cancellation.
// Returns true if the progress event should be dropped entirely.
func (m *cliModel) filterProgressSession(msg cliProgressMsg) bool {
	// Fatal guard: ChatID must never be empty — it identifies which session
	// this progress belongs to. Empty ChatID means the progress bypassed
	// session filtering and would leak into the wrong view.
	if msg.payload != nil && msg.payload.ChatID == "" {
		log.WithFields(log.Fields{
			"phase":     msg.payload.Phase,
			"iteration": msg.payload.Iteration,
		}).Error("handleProgressMsg: received progress with empty ChatID — this is a programming error")
		return true
	}
	if msg.payload != nil && msg.payload.ChatID != "" {
		currentKey := m.channelName + ":" + m.chatID
		if msg.payload.ChatID != currentKey {
			return true
		}
	}

	// Guard: ignore progress after explicit Ctrl+C cancel.
	// PhaseDone is allowed through: it's idempotent (endAgentTurn checks turnID).
	if m.turnCancelled && msg.payload != nil && msg.payload.Phase != "done" {
		return true
	}

	// Auto-start turn: when receiving progress for current session but not typing,
	// start the turn. This handles first-switch to a running SubAgent session.
	if !m.typing && msg.payload != nil && msg.payload.Phase != "done" {
		log.WithFields(log.Fields{
			"phase":     msg.payload.Phase,
			"iteration": msg.payload.Iteration,
			"active":    len(msg.payload.ActiveTools),
			"chatID":    msg.payload.ChatID,
		}).Info("handleProgressMsg: auto-start turn")
		m.startAgentTurn()
	}

	return false
}

// mergeStreamOnlyPayload handles stream-only payloads that only carry content
// or reasoning stream text. These are merged into existing progress rather than
// replacing it, to preserve tool/iteration state.
// Returns true if the payload was stream-only (caller should return immediately).
func (m *cliModel) mergeStreamOnlyPayload(msg cliProgressMsg) bool {
	isStreamOnly := msg.payload != nil &&
		msg.payload.Phase == "" && msg.payload.Iteration == 0 &&
		(msg.payload.StreamContent != "" || msg.payload.ReasoningStreamContent != "")
	if !isStreamOnly {
		return false
	}

	if m.progress != nil {
		if msg.payload.StreamContent != "" {
			m.progress.StreamContent = msg.payload.StreamContent
		}
		if msg.payload.ReasoningStreamContent != "" {
			m.progress.ReasoningStreamContent = msg.payload.ReasoningStreamContent
		}
	} else if m.typing {
		// Turn started but no structured progress yet — create minimal payload
		m.progress = msg.payload
	}
	return true
}

// restoreIterationHistory rebuilds iteration history from a reconnect/GetActiveProgress
// snapshot. When a CLI reconnects mid-turn, the server sends completed iterations
// in IterationHistory — these are converted to cliIterationSnapshot for rendering.
func (m *cliModel) restoreIterationHistory() {
	if m.progress == nil || len(m.progress.IterationHistory) == 0 || len(m.iterationHistory) > 0 {
		return
	}

	for _, ih := range m.progress.IterationHistory {
		snap := cliIterationSnapshot{
			Iteration: ih.Iteration,
			Thinking:  ih.Thinking,
			Reasoning: ih.Reasoning,
			Tools:     ih.CompletedTools,
		}
		restoreToolStartedAt(snap.Tools)
		m.iterationHistory = append(m.iterationHistory, snap)
	}

	// Set lastSeenIteration to the latest restored iteration so we don't
	// re-snapshot it when the next progress event arrives.
	if len(m.iterationHistory) > 0 {
		lastIter := m.iterationHistory[len(m.iterationHistory)-1].Iteration
		if lastIter > m.lastSeenIteration {
			m.lastSeenIteration = lastIter
		}
	}

	// Deduplicate: remove ALL tool_summary messages. When progress is
	// active, the progress block owns iteration display — any static
	// tool_summary would duplicate content with mismatched iteration numbers.
	m.removeAllToolSummaries()
}

// preserveProgressState carries forward tool timings, completed tools, reasoning,
// and thinking from the previous progress event. Each structured progress event
// replaces fields entirely, so we must restore transient state by matching.
func (m *cliModel) preserveProgressState(prev *CLIProgressPayload) {
	if m.progress == nil {
		return
	}

	// Build a map of previous tool StartedAt values for restoration.
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
		if prevStartedAt, ok := startedAtMap[t.Name]; ok {
			t.StartedAt = prevStartedAt
		} else if t.StartedAt.IsZero() {
			// First appearance: bootstrap from Elapsed or now
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
	// Progress events may arrive without CompletedTools (e.g. a thinking-phase event
	// after tool completion), which would cause completed tools to flicker/disappear.
	if len(m.progress.CompletedTools) == 0 && len(prev.CompletedTools) > 0 && sameIter {
		m.progress.CompletedTools = prev.CompletedTools
	}

	// Carry forward Reasoning/Thinking from previous progress within the same iteration.
	if m.progress.Reasoning == "" && prev.Reasoning != "" && sameIter {
		m.progress.Reasoning = prev.Reasoning
	}
	if m.progress.Thinking == "" && prev.Thinking != "" && sameIter {
		m.progress.Thinking = prev.Thinking
	}
	// ReasoningStreamContent: carry forward if new payload doesn't have it
	// and we're still in reasoning streaming phase (no StreamContent yet).
	if m.progress.ReasoningStreamContent == "" && prev.ReasoningStreamContent != "" && sameIter {
		if m.progress.StreamContent == "" {
			m.progress.ReasoningStreamContent = prev.ReasoningStreamContent
		}
	}
}

// preserveSubAgentTree preserves the SubAgent tree across progress updates within
// the same iteration. Progress events may arrive with incomplete subagent data
// (missing deep nodes) or no subagent data at all. We preserve the deepest tree
// seen during the current turn to prevent the TUI from losing deep agent nodes.
// PhaseDone is the exception — it intentionally clears the tree.
func (m *cliModel) preserveSubAgentTree(prev *CLIProgressPayload) {
	if m.progress == nil || m.progress.Phase == "done" || prev == nil {
		return
	}

	iterationChanged := m.progress.Iteration != prev.Iteration && m.progress.Iteration > 0
	if iterationChanged {
		// New iteration started — clear stale SubAgent tree
		m.progress.SubAgents = nil
		return
	}

	newDepth := maxTreeDepth(m.progress.SubAgents)
	prevDepth := maxTreeDepth(prev.SubAgents)
	if len(m.progress.SubAgents) == 0 && len(prev.SubAgents) > 0 {
		// New payload has no tree — carry forward old tree
		m.progress.SubAgents = prev.SubAgents
	} else if newDepth < prevDepth && newDepth > 0 {
		// New tree is shallower than old — carry forward old tree
		// (deeper nodes are still running even if this event didn't include them)
		m.progress.SubAgents = prev.SubAgents
	}
}

// updateProgressCallbacks updates background task/agent counts from callbacks
// and handles history compaction events.
func (m *cliModel) updateProgressCallbacks(payload *CLIProgressPayload) {
	if m.bgTaskCountFn != nil {
		m.bgTaskCount = m.bgTaskCountFn()
	}
	if m.agentCountFn != nil {
		m.agentCount = m.agentCountFn()
	}

	if payload != nil && payload.HistoryCompacted {
		m.reloadMessagesFromSession()
	}
}

// syncProgressTodos synchronizes the CLI todo list from the progress payload.
// It respects user dismissal of all-done todos to prevent stale re-acceptance.
func (m *cliModel) syncProgressTodos(payload *CLIProgressPayload) {
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
			m.todos = make([]CLITodoItem, len(payload.Todos))
			copy(m.todos, payload.Todos)
			m.todosDoneCleared = false
			m.relayoutViewport() // TODO 行数可能变化，重新计算 viewport 高度
		}
	} else {
		prevTodoCount := len(m.todos)
		m.todos = nil
		if prevTodoCount > 0 {
			m.relayoutViewport() // TODO 清除，恢复 viewport 高度
		}
	}
}

// snapshotIterationChange detects when the iteration number increments and
// snapshots the previous iteration's completed tools, thinking, and reasoning
// into iteration history for rendering.
func (m *cliModel) snapshotIterationChange(payload *CLIProgressPayload, prev *CLIProgressPayload) {
	if payload.Iteration <= m.lastSeenIteration || m.lastSeenIteration < 0 || prev == nil {
		return
	}

	// Snapshot all completed tools from prev — they belong to iterations
	// that finished before this new iteration started. Don't filter by
	// Iteration field because tools from earlier iterations may have been
	// carried forward via the CompletedTools carry-forward logic.
	prevIterTools := prev.CompletedTools
	prevReasoning := prev.Reasoning
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

	// Clear lastCompletedTools to prevent stale tools from being
	// re-snapshotted when the final iteration is snapshotted in handleAgentMessage.
	m.lastCompletedTools = m.lastCompletedTools[:0]
	m.lastSeenIteration = payload.Iteration
	m.iterationStartTime = time.Now()
}

// snapshotCompletedTools stores the latest CompletedTools for tool visualization,
// accepting all completed tools regardless of their Iteration field.
func (m *cliModel) snapshotCompletedTools(payload *CLIProgressPayload) {
	if len(payload.CompletedTools) > 0 {
		m.lastCompletedTools = make([]CLIToolProgress, len(payload.CompletedTools))
		copy(m.lastCompletedTools, payload.CompletedTools)
	}
}

// handlePhaseDone processes the "done" phase: snapshots the final iteration,
// generates tool summary, resets turn state, and synthesizes the assistant
// message for agent sessions.
func (m *cliModel) handlePhaseDone(payload *CLIProgressPayload, prev *CLIProgressPayload, turnID uint64) {
	// Snapshot the final iteration before clearing progress.
	// This handles the case where PhaseDone arrives before
	// handleAgentMessage (e.g. agent error/cancel).
	if m.typing && m.lastSeenIteration >= 0 {
		m.snapshotFinalIteration(payload, prev)
		m.emitToolSummary()
	}

	// Reset all iteration tracking state (always, even if handleAgentMessage ran first)
	m.todos = nil
	m.todosDoneCleared = false
	m.endAgentTurn(turnID)
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
		assistantContent := payload.Thinking
		if assistantContent == "" {
			assistantContent = payload.StreamContent
		}
		if assistantContent != "" {
			m.messages = append(m.messages, cliMessage{
				role:      roleAssistant,
				content:   assistantContent,
				timestamp: time.Now(),
				dirty:     true,
			})
			m.renderCacheValid = false
		}
	}

	m.relayoutViewport()
}

// snapshotFinalIteration captures the last iteration into history when PhaseDone
// arrives. It merges CompletedTools from both the done payload and any
// lastCompletedTools held from prior events, and carries forward reasoning
// from multiple sources with priority ordering.
func (m *cliModel) snapshotFinalIteration(payload *CLIProgressPayload, prev *CLIProgressPayload) {
	alreadySnapped := false
	for _, s := range m.iterationHistory {
		if s.Iteration == m.lastSeenIteration {
			alreadySnapped = true
			break
		}
	}
	if alreadySnapped {
		return
	}

	var finalTools []CLIToolProgress
	// Check progress.CompletedTools first (set by progressFinalizer)
	finalTools = append(finalTools, payload.CompletedTools...)
	// Also include any from lastCompletedTools (race safety)
	for _, t := range m.lastCompletedTools {
		dup := false
		for _, existing := range finalTools {
			if existing.Name == t.Name && existing.Label == t.Label {
				dup = true
				break
			}
		}
		if !dup {
			finalTools = append(finalTools, t)
		}
	}

	snap := cliIterationSnapshot{
		Iteration:   m.lastSeenIteration,
		Thinking:    payload.Thinking,
		Tools:       finalTools,
		ElapsedWall: time.Since(m.iterationStartTime).Milliseconds(),
	}

	// Carry over reasoning: priority is lastReasoning (captured before progress clear)
	// > prev progress Reasoning > prev ReasoningStreamContent
	// > PhaseDone payload Reasoning
	if m.lastReasoning != "" {
		snap.Reasoning = m.lastReasoning
	} else if prev != nil && prev.Reasoning != "" {
		snap.Reasoning = prev.Reasoning
	} else if prev != nil && prev.ReasoningStreamContent != "" {
		snap.Reasoning = prev.ReasoningStreamContent
	} else if payload.Reasoning != "" {
		snap.Reasoning = payload.Reasoning
	}

	if len(finalTools) > 0 || snap.Thinking != "" || snap.Reasoning != "" {
		m.iterationHistory = append(m.iterationHistory, snap)
	}
}

// emitToolSummary generates a tool_summary message from the accumulated
// iteration history and appends it to the message list. This ensures
// cancel/error cases (which bypass handleAgentMessage) still display a summary.
func (m *cliModel) emitToolSummary() {
	if len(m.iterationHistory) == 0 {
		return
	}
	m.pendingToolSummary = &cliMessage{
		role:       roleToolSummary,
		content:    "",
		timestamp:  time.Now(),
		iterations: append([]cliIterationSnapshot{}, m.iterationHistory...),
		dirty:      true,
	}
	m.messages = append(m.messages, *m.pendingToolSummary)
	m.renderCacheValid = false
}

// handleInjectedUserMsg processes user messages injected by the agent (e.g. bg task completion).
func (m *cliModel) handleInjectedUserMsg(msg cliInjectedUserMsg) []tea.Cmd {
	m.messages = append(m.messages, cliMessage{
		role:      roleUser,
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
	// §16 Trigger toast notification (background task completion hint)
	// Extract first line as toast text to avoid excessive content length
	firstLine := msg.content
	if idx := strings.Index(msg.content, "\n"); idx >= 0 {
		firstLine = msg.content[:idx]
	}
	if len([]rune(firstLine)) > toastMaxRunes {
		firstLine = string([]rune(firstLine)[:toastTruncateRunes]) + "..."
	}
	// Detect if it's a completion or failure message
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
	if msg.info != nil {
		m.updateNotice = msg.info
		if msg.info.HasUpdate {
			content := fmt.Sprintf(m.locale.UpdateFound, msg.info.Current, msg.info.Latest, msg.info.URL)
			m.showSystemMsg(content, feedbackInfo)
		} else {
			content := fmt.Sprintf(m.locale.UpdateCurrent, msg.info.Current)
			m.showSystemMsg(content, feedbackInfo)
		}
	} else {
		m.showSystemMsg(m.locale.UpdateFailed, feedbackError)
	}
}

// handleSuHistoryLoad processes /su user switch history load results.
// Returns tea.Cmds to start the tick chain when active progress is restored.
func (m *cliModel) handleSuHistoryLoad(msg suHistoryLoadMsg) []tea.Cmd {
	m.suLoading = false

	// Stale result guard: if user switched away from the target session
	// while the async load was in-flight, discard the result.
	if msg.channelName != m.channelName || msg.chatID != m.chatID {
		return nil
	}

	if msg.err != nil {
		m.showSystemMsg(fmt.Sprintf(m.locale.SuLoadFailed, msg.err), feedbackWarning)
	} else {
		for _, hm := range msg.history {
			m.messages = append(m.messages, historyMessageToCLI(hm))
		}
		m.showSystemMsg(fmt.Sprintf(m.locale.SuSwitchedHistory, m.senderID, len(msg.history)), feedbackInfo)
	}
	m.invalidateAllCache(false)
	m.viewport.GotoBottom()

	// Restore active progress for seamless session switch.
	// msg.activeProgress (from GetActiveProgress RPC) is the authoritative source:
	// if the server says the turn is done or gone, any saved state from
	// restoreSession() is stale and must be discarded.
	var cmds []tea.Cmd
	switch {
	case msg.activeProgress != nil && msg.activeProgress.Phase != "done":
		// Turn is still active on the server. Use the server snapshot regardless
		// of whether restoreSession() also restored state — the server snapshot
		// has the freshest progress data.
		if !m.typing {
			m.startAgentTurn()
		}
		m.progress = msg.activeProgress

		// Restore StartedAt for active tools so live elapsed timers work.
		restoreToolStartedAt(m.progress.ActiveTools)

		// Rebuild iteration history from server snapshot (authoritative).
		m.iterationHistory = nil
		if len(msg.activeProgress.IterationHistory) > 0 {
			for _, ih := range msg.activeProgress.IterationHistory {
				snap := cliIterationSnapshot{
					Iteration: ih.Iteration,
					Thinking:  ih.Thinking,
					Reasoning: ih.Reasoning,
					Tools:     ih.CompletedTools,
				}
				restoreToolStartedAt(snap.Tools)
				m.iterationHistory = append(m.iterationHistory, snap)
			}
			if len(m.iterationHistory) > 0 {
				lastIter := m.iterationHistory[len(m.iterationHistory)-1].Iteration
				if lastIter > m.lastSeenIteration {
					m.lastSeenIteration = lastIter
				}
			}
		}
		// When turn is still active, remove ALL tool_summary messages from
		// loaded history. ConvertMessagesToHistory produces tool_summary from
		// intermediate DB messages with globally-cumulative iteration numbers
		// that don't match the progress block's per-turn iteration numbers.
		// The active progress block owns iteration display entirely — any
		// static tool_summary would duplicate content with mismatched numbers.
		m.removeAllToolSummaries()

		// Emit a tickCmd to guarantee the fast tick chain is running,
		// but only if it's not already active (avoid duplicate chains).
		// See handleSplashTick for the other half of this guard.
		if !m.fastTickActive {
			m.fastTickActive = true
			cmds = append(cmds, tickCmd())
		}

	default:
		// Turn is not active (nil or PhaseDone). If restoreSession() restored
		// a stale typing=true state, clear it — the server snapshot is authoritative.
		if m.typing {
			m.endAgentTurn(m.agentTurnID)
		}
		// Reload history to pick up messages that arrived while we were viewing
		// another session (e.g. the assistant's final reply was filtered out by
		// ChatID check during the agent session view).
		if loader := m.channel.config.DynamicHistoryLoader; loader != nil {
			ch, cid := m.channelName, m.chatID
			cmds = append(cmds, func() tea.Msg {
				history, err := loader(ch, cid)
				if err != nil {
					return cliHistoryReloadMsg{err: err}
				}
				return cliHistoryReloadMsg{history: history}
			})
		}
	}
	return cmds
}

// handleHistoryReload rebuilds m.messages from session storage after context compression.
// Unlike /su which appends, this REPLACES the entire message list because compression
// may have replaced many old messages with a single [Compacted context] summary.
func (m *cliModel) handleHistoryReload(msg cliHistoryReloadMsg) {
	if msg.err != nil {
		log.WithError(msg.err).Warn("Failed to reload history after compression")
		return
	}
	var newMessages []cliMessage
	for _, hm := range msg.history {
		newMessages = append(newMessages, historyMessageToCLI(hm))
	}
	m.messages = newMessages
	m.streamingMsgIdx = -1
	m.invalidateAllCache(false)
	m.updateViewportContent()
	m.viewport.GotoBottom()
	log.WithField("count", len(newMessages)).Info("History reloaded after compression")
}

// handleSplashTick processes splash animation frames.
func (m *cliModel) handleSplashTick(msg splashTickMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	m.splashFrame = msg.frame
	if m.suLoading {
		// /su history loading, continue animation
		cmds = append(cmds, m.splashTick(msg.frame))
		return m, tea.Batch(cmds...)
	}
	if m.ready && msg.frame >= splashMinFrames {
		// Initialization complete and displayed for at least 1 second (20 frames × 50ms)
		m.splashDone = true
		if m.typing && m.progress != nil && !m.fastTickActive {
			m.fastTickActive = true
			cmds = append(cmds, tickCmd())
		} else if !m.typing || m.progress == nil {
			cmds = append(cmds, idleTickCmd())
		}
		return m, tea.Batch(cmds...)
	}
	// Failsafe limit: ~2 seconds (40 frames)
	if msg.frame >= splashMaxFrames {
		m.splashDone = true
		if m.typing && m.progress != nil && !m.fastTickActive {
			m.fastTickActive = true
			cmds = append(cmds, tickCmd())
		} else if !m.typing || m.progress == nil {
			cmds = append(cmds, idleTickCmd())
		}
		return m, tea.Batch(cmds...)
	}
	cmds = append(cmds, m.splashTick(msg.frame))
	return m, tea.Batch(cmds...)
}

// handleToastMsg enqueues a toast notification.
func (m *cliModel) handleToastMsg(msg cliToastMsg) []tea.Cmd {
	// §16 Toast notification enqueue (keep max 5, display first 3)
	if len(m.toasts) >= toastMaxQueue {
		m.toasts = m.toasts[len(m.toasts)-toastTrimTo:]
	}
	m.toasts = append(m.toasts, cliToastItem(msg))
	if !m.toastTimer {
		m.toastTimer = true
		return []tea.Cmd{tea.Tick(toastDisplaySec*time.Second, func(time.Time) tea.Msg {
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
		return []tea.Cmd{tea.Tick(toastDisplaySec*time.Second, func(time.Time) tea.Msg {
			return cliToastClearMsg{}
		})}
	}
	m.toastTimer = false
	return nil
}

// maxTreeDepth returns the maximum depth of the SubAgent tree (1 for top-level nodes).
func maxTreeDepth(agents []CLISubAgent) int {
	if len(agents) == 0 {
		return 0
	}
	max := 1
	for _, a := range agents {
		if d := maxTreeDepth(a.Children); d+1 > max {
			max = d + 1
		}
	}
	return max
}

// handlePreSwitchMessages processes all pre-switch message checks in Update.
// Returns (handled=true, model, cmd, nil) if the message was fully consumed.
// Returns (false, nil, nil, extraCmds) if processing should continue.
func (m *cliModel) handlePreSwitchMessages(msg tea.Msg) (bool, tea.Model, tea.Cmd, []tea.Cmd) {
	// Phase 1: Async notifications (early return before side effects)
	if handled, mdl, cmd := m.handleAsyncNotifications(msg); handled {
		return true, mdl, cmd, nil
	}
	// Phase 2: Apply side effects (theme, locale, models error, pending cmds)
	extraCmds := m.applyPreSwitchSideEffects(msg)
	// Phase 3: Key-based early-return checks (pending cmds intentionally dropped for early returns)
	if handled, mdl, cmd := m.handlePreSwitchKeyChecks(msg); handled {
		return true, mdl, cmd, nil
	}
	return false, nil, nil, extraCmds
}

// handleAsyncNotifications handles async notification messages that return early.
func (m *cliModel) handleAsyncNotifications(msg tea.Msg) (bool, tea.Model, tea.Cmd) {
	// Async settings save completed — apply theme/locale/viewport changes
	if saved, ok := msg.(cliSettingsSavedMsg); ok {
		cmd := m.handleSettingsSavedMsg(saved)
		return true, m, cmd
	}
	// Async subscription switch completed
	if done, ok := msg.(cliSwitchLLMDoneMsg); ok {
		return true, m, m.handleSwitchLLMDone(done)
	}
	// Runner status change notification
	if rsm, ok := msg.(runnerStatusMsg); ok {
		cmd := m.handleRunnerStatusMsg(rsm)
		return true, m, cmd
	}
	return false, nil, nil
}

// applyPreSwitchSideEffects applies non-returning side effects:
// theme rebuild, models load error, pending cmds drain, locale refresh.
func (m *cliModel) applyPreSwitchSideEffects(msg tea.Msg) []tea.Cmd {
	var cmds []tea.Cmd
	// Theme change notification: rebuild style cache + glamour renderer
	select {
	case <-themeChangeCh:
		m.applyThemeAndRebuild(currentThemeName)
		m.updateViewportContent()
	default:
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
	// i18n: Locale change notification
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
	return cmds
}

// handlePreSwitchKeyChecks handles all key-based early-return checks
// that must fire before the main type switch in Update.
func (m *cliModel) handlePreSwitchKeyChecks(msg tea.Msg) (bool, tea.Model, tea.Cmd) {
	// Ctrl+Z: emergency quit (regardless of state, including panel/typing/idle)
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+z" {
		m.showSystemMsg(m.locale.EmergencyQuitHint, feedbackWarning)
		return true, m, tea.Quit
	}
	// DEBUG: log all KeyPressMsg to trace ctrl+c handling
	if key, ok := msg.(tea.KeyPressMsg); ok {
		log.WithFields(log.Fields{"str": key.String(), "code": key.Code, "mod": key.Mod}).Debug("DEBUG keypress")
	}
	// Ctrl+C: unified handling, placed before all other key handlers.
	// This is the only Ctrl+C handling point — no other place should intercept Ctrl+C.
	// Ensure Ctrl+C always works regardless of state (typing/idle/panel/queue/editing).
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+c" {
		m.handleCtrlC()
		return true, m, nil
	}
	// §15 Quick switch overlay: highest priority (above panelMode).
	// This ensures ESC in quick switch closes the overlay, not the panel behind it.
	if key, ok := msg.(tea.KeyPressMsg); ok {
		if handled, cmd := m.handleQuickSwitchKey(key); handled {
			return true, m, cmd
		}
		// §9 Rewind overlay: same priority as quick switch.
		if handled, cmd := m.handleRewindKey(key); handled {
			return true, m, cmd
		}
	}
	// §12 Panel mode: intercept all key events when panel is active
	// NOTE: Ctrl+C is handled above — never intercept it here.
	if key, ok := msg.(tea.KeyPressMsg); ok && m.panelMode != "" {
		handled, newModel, cmd := m.updatePanel(key)
		if handled {
			return true, newModel, cmd
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
		}
		return true, m, cmd
	}
	// §21 Search mode interception
	if key, ok := msg.(tea.KeyPressMsg); ok && m.searchMode {
		if cmd, handled := m.handleSearchKey(key); handled {
			return true, m, cmd
		}
	}
	// Home/End jump to top/bottom
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "home":
			m.viewport.GotoTop()
			return true, m, nil
		case "end":
			m.viewport.GotoBottom()
			m.newContentHint = false
			return true, m, nil
		}
	}
	// Ctrl+Enter newline (terminal raw sequences are inconsistent, need manual detection)
	if isCtrlEnter(msg) {
		m.textarea.InsertString("\n")
		m.autoExpandInput()
		return true, m, nil
	}
	// Ctrl+J newline — directly InsertString bypassing textarea's internal atContentLimit check,
	// otherwise textarea's InsertNewline keymap silently drops newlines after reaching MaxHeight.
	if isCtrlJ(msg) {
		m.textarea.InsertString("\n")
		m.autoExpandInput()
		return true, m, nil
	}
	// Ctrl+O toggle tool summary expand/collapse (CSI u protocol compatibility layer, kitty/Ghostty, etc.)
	if isCtrlO(msg) {
		m.toggleToolSummary()
		return true, m, nil
	}
	return false, nil, nil
}

// handleIdleTickMsg handles the low-frequency idle tick: rotate placeholder, keep alive,
// and self-heal broken tick chains.
func (m *cliModel) handleIdleTickMsg() []tea.Cmd {
	var cmds []tea.Cmd
	// Remote mode: keep retrying model name fetch until we get one.
	if m.cachedModelName == "" && m.remoteMode {
		m.refreshCachedModelName()
	}
	if !m.typing && m.progress == nil {
		m.updatePlaceholder()
		cmds = append(cmds, idleTickCmd())
	} else if !m.fastTickActive {
		// Self-healing: if fast tick chain broke but we're still busy
		// (typing or progress active), re-arm fast tick.
		m.fastTickActive = true
		cmds = append(cmds, tickCmd())
	}
	return cmds
}

// handleSplashDoneMsg handles the splash screen end confirmation.
func (m *cliModel) handleSplashDoneMsg() []tea.Cmd {
	var cmds []tea.Cmd
	m.splashDone = true
	// Remote mode: retry model name fetch — the initial call in cli.go:76
	// may have failed if the WS RPC wasn't fully ready yet.
	if m.cachedModelName == "" && m.remoteMode {
		m.refreshCachedModelName()
	}
	if m.typing && m.progress != nil && !m.fastTickActive {
		m.fastTickActive = true
		cmds = append(cmds, tickCmd())
	} else if !m.typing || m.progress == nil {
		cmds = append(cmds, idleTickCmd())
	}
	return cmds
}

// handleTypewriterTickMsg advances the typewriter animation by one rune
// and continues the tick chain if still behind.
func (m *cliModel) handleTypewriterTickMsg() []tea.Cmd {
	m.advanceTypewriter()
	m.updateViewportContent()
	// Continue chain if still behind on either stream or reasoning content
	streamBehind := m.progress != nil && m.progress.StreamContent != "" && m.twVisible < len([]rune(m.progress.StreamContent))
	reasoningBehind := m.progress != nil && m.progress.ReasoningStreamContent != "" && m.rwVisible < len([]rune(m.progress.ReasoningStreamContent))
	if m.typewriterTickActive && (streamBehind || reasoningBehind) {
		return []tea.Cmd{typewriterTickCmd()}
	}
	m.typewriterTickActive = false
	return nil
}

// handleHistoryLoadMsgCase applies a batch of history messages to the view.
func (m *cliModel) handleHistoryLoadMsgCase(msg cliHistoryLoadMsg) {
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

// handleProcessingMsgCase updates agent turn state based on processing flag.
// NOTE: do NOT flush queue here even if needFlushQueue is true!
// PhaseDone can arrive before cliOutboundMsg (the reply text). If we
// flush here, the queued message gets appended BEFORE the reply,
// producing wrong order: msg1, msg2, reply1 instead of msg1, reply1, msg2.
// Flush is handled in cliTickMsg instead (next tick after typing=false).
func (m *cliModel) handleProcessingMsgCase(msg cliProcessingMsg) {
	if msg.processing && !m.typing {
		m.startAgentTurn()
	} else if !msg.processing && m.typing {
		m.endAgentTurn(m.agentTurnID)
	}
}

// handleApprovalRequestMsg sets up the approval dialog state.
func (m *cliModel) handleApprovalRequestMsg(msg approvalRequestMsg) {
	m.approvalRequest = &msg.request
	m.approvalResultCh = msg.resultCh
	m.approvalCursor = 0 // default to Approve
	m.approvalEnteringDeny = false
	m.approvalDenyInput = textinput.New()
	m.approvalDenyInput.Placeholder = "Optional deny reason for LLM"
	m.approvalDenyInput.CharLimit = 200
	m.approvalDenyInput.SetWidth(60)
	m.panelMode = panelModeApproval
	m.renderCacheValid = false
}

// handleProgressMsgCase processes a progress message and ensures the fast tick
// chain is active when restoring progress (reconnect/switch).
func (m *cliModel) handleProgressMsgCase(msg cliProgressMsg, cmds *[]tea.Cmd) {
	m.handleProgressMsg(msg)
	// Normal progress events don't need this (tick already running), but restored
	// snapshots arrive before the idle tick self-heal fires (3s delay).
	if m.typing && !m.fastTickActive {
		m.fastTickActive = true
		*cmds = append(*cmds, tickCmd())
	}
}

// handleEasterEggDoneMsgCase dismisses the easter egg overlay.
func (m *cliModel) handleEasterEggDoneMsgCase() {
	m.dismissEasterEgg()
	m.renderCacheValid = false
	m.updateViewportContent()
}

// handleEasterEggMatrixTickCase advances the matrix rain animation.
func (m *cliModel) handleEasterEggMatrixTickCase(cmds *[]tea.Cmd) tea.Cmd {
	if m.easterEgg == easterEggMatrix {
		m.tickMatrix()
		*cmds = append(*cmds, matrixTickCmd())
	}
	return tea.Batch(*cmds...)
}

// handlePostSwitch performs post-switch processing: idle→typing guard,
// viewport/textarea update, tab completion reset, and quit check.
func (m *cliModel) handlePostSwitch(msg tea.Msg, prevText string, wasTyping bool, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	// Idle→typing transition guard: if typing just started (e.g. from
	// handleInjectedUserMsg or cliProcessingMsg), ensure the tick chain is running.
	if !wasTyping && m.typing && !m.fastTickActive {
		cmds = append(cmds, tickCmd())
	}
	// Update viewport
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	// Update textarea (skip WindowSizeMsg: handleResize already calls SetWidth)
	if _, ok := msg.(tea.WindowSizeMsg); !ok {
		m.textarea, cmd = m.textarea.Update(msg)
		cmds = append(cmds, cmd)
	}
	// §8 Tab completion：Reset completion state on input content change
	newVal := m.textarea.Value()
	if newVal != prevText {
		m.completions = nil
		m.compIdx = 0
		m.fileCompActive = false
		if !m.fileCompActive {
			if ok, prefix := detectAtPrefix(newVal); ok {
				m.populateFileCompletions(prefix)
			} else {
				m.fileCompletions = nil
				m.fileCompIdx = 0
			}
		}
	}
	if m.shouldQuit {
		return m, tea.Quit
	}
	m.autoExpandInput()
	return m, tea.Batch(cmds...)
}

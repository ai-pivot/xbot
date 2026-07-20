package cli

import (
	"fmt"
	"slices"
	"time"
	"xbot/protocol"

	log "xbot/logger"

	tea "charm.land/bubbletea/v2"
)

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
		m.progressState.liveStates[ev.ChatID] = &liveSessionState{busy: true}
	case "idle":
		// Main session finished processing.
		// Explicitly mark as idle (not delete) — the 30s safety-net poll
		// may return stale Busy=true from cache, so we need the override.
		m.progressState.liveStates[ev.ChatID] = &liveSessionState{busy: false}
	case "subagent_started":
		// SubAgent interactive session created.
		key := "agent:" + ev.Role + "/" + ev.Instance
		m.progressState.liveStates[key] = &liveSessionState{
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
		m.progressState.liveStates[key] = &liveSessionState{
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
	case "history_rewound":
		if ev.Channel != m.channelName || ev.ChatID != m.chatID {
			return
		}
		var rewindGeneration uint64
		if m.rewindSync.generation != 0 && m.rewindSync.targetHistoryID == ev.TargetHistoryID {
			rewindGeneration = m.rewindSync.generation
			m.rewindPending = true
			m.rewindPendingGen = rewindGeneration
		} else {
			m.rewindGeneration++
			rewindGeneration = m.rewindGeneration
			m.rewindPending = true
			m.rewindPendingGen = rewindGeneration
			m.rewindSync = rewindWarningSync{
				generation:      rewindGeneration,
				targetHistoryID: ev.TargetHistoryID,
			}
		}
		if !m.rewindSync.resetSeen {
			m.beginRewindReload(ev.TargetHistoryID, rewindGeneration)
		}
	case "resync_required":
		if ev.Channel != m.channelName || ev.ChatID != m.chatID {
			return
		}
		m.prepareAuthoritativeSessionReload()
		if m.channel != nil && m.channel.config != nil {
			m.pendingCmds = append(m.pendingCmds, m.authoritativeSessionReloadCmd())
		}
	}
}

func (m *cliModel) beginRewindReload(targetHistoryID int64, generation uint64) {
	if generation == 0 || m.rewindSync.generation != generation || m.rewindSync.resetSeen {
		return
	}
	m.rewindSync.targetHistoryID = targetHistoryID
	m.rewindSync.resetSeen = true
	m.clearPendingAskUserUI(m.chatID)
	m.closeRewindPanel()
	m.pendingUserMsg = nil
	m.messages = make([]cliMessage, 0, cliMsgBufSize)
	m.streamingMsgIdx = -1
	m.progressState.current = nil
	m.progressState.iterations = nil
	m.progressState.lastIter = 0
	m.progressState.lastSeq = 0
	m.progressState.lastAppliedSeq = 0
	m.progressState.lastStreamSeq = 0
	m.progressState.lastReceivedSeq = 0
	m.progressState.gapDetected = false
	m.progressState.twActive = false
	m.progressState.twVisible = 0
	m.progressState.rwVisible = 0
	m.progressState.rwCjkSkip = false
	m.progressState.twCjkSkip = false
	m.typing = false
	m.typingStartTime = time.Time{}
	m.replyProcessed = true
	m.inputReady = false
	m.lastTokenUsage = nil
	m.historyMutationGeneration++
	m.splashState.compReloading = true
	m.invalidateAllCache(true)
	if m.channel == nil || m.channel.config == nil || m.channel.config.DynamicHistoryLoader == nil {
		m.splashState.compReloading = false
		m.showSystemMsg("History rewound, but reload service is unavailable", feedbackError)
		m.unlockRewind(generation)
		m.rewindSync = rewindWarningSync{}
		return
	}
	m.reloadMessagesFromSessionForRewind(true, generation)
}

func (m *cliModel) prepareAuthoritativeSessionReload() {
	m.clearPendingAskUserUI(m.chatID)
	m.closeRewindPanel()
	if !m.rewindPending {
		m.rewindSync = rewindWarningSync{}
		m.rewindResult = nil
	}
	m.messages = make([]cliMessage, 0, cliMsgBufSize)
	m.streamingMsgIdx = -1
	m.progressState.current = nil
	m.progressState.iterations = nil
	m.progressState.lastIter = 0
	m.progressState.lastSeq = 0
	m.progressState.lastAppliedSeq = 0
	m.progressState.lastStreamSeq = 0
	m.progressState.lastReceivedSeq = 0
	m.progressState.gapDetected = false
	m.progressState.twActive = false
	m.progressState.twVisible = 0
	m.progressState.rwVisible = 0
	m.progressState.rwCjkSkip = false
	m.progressState.twCjkSkip = false
	m.typing = false
	m.typingStartTime = time.Time{}
	m.replyProcessed = true
	m.needFlushQueue = false
	m.turnCancelled = false
	m.inputReady = false
	m.lastTokenUsage = nil
	m.lastContent = ""
	m.todos = nil
	if m.todoManager != nil {
		m.todoManager.SetTodos(m.sessionKey(), nil)
	}
	m.splashState.suLoading = true
	m.splashState.suPhaseConfirmed = false
	m.invalidateAllCache(true)
	m.relayoutViewport()
}

// scheduleSessionsRefresh triggers an immediate session list cache refresh.
// Called when sessions are created/destroyed via server push events.
func (m *cliModel) scheduleSessionsRefresh() {
	if m.channel != nil && m.channel.config.SessionsListRefresh != nil {
		m.channel.config.SessionsListRefresh()
	}
}

// msgIdentity uses the stable DB node when available and retains the legacy
// role+timestamp fallback for optimistic/in-memory messages.
type msgIdentity struct {
	historyID int64
	role      string
	timestamp time.Time
}

func cliMessageIdentity(msg cliMessage) msgIdentity {
	if msg.historyID != 0 {
		return msgIdentity{historyID: msg.historyID}
	}
	return msgIdentity{role: msg.role, timestamp: msg.timestamp}
}

// toCLIMessage converts a protocol.HistoryMessage into a cliMessage
// suitable for appending to m.messages. Shared by handleSuHistoryLoad,
// handleHistoryLoad, and handleHistoryReload to avoid copy-paste drift.
func toCLIMessage(hm protocol.HistoryMessage) cliMessage {
	cm := cliMessage{
		historyID:   hm.HistoryID,
		recordType:  hm.RecordType,
		compactedBy: hm.CompactedBy,
		displayOnly: hm.DisplayOnly,
		hidden:      hm.RecordType != "" && hm.RecordType != "message" && hm.RecordType != "compress",
		role:        hm.Role,
		content:     hm.Content,
		reasoning:   hm.ReasoningContent,
		timestamp:   hm.Timestamp,
		isPartial:   false,
		dirty:       true,
	}
	for _, call := range hm.ToolCalls {
		label := call.Name
		if call.Arguments != "" {
			label += " " + call.Arguments
		}
		cm.tools = append(cm.tools, protocol.ToolProgress{
			Name: call.Name, Label: label, Status: "history", Args: call.Arguments, Detail: call.ID,
		})
	}
	if hm.Role == "tool" && (hm.ToolName != "" || hm.ToolCallID != "") {
		cm.tools = append(cm.tools, protocol.ToolProgress{
			Name: hm.ToolName, Label: hm.ToolName, Args: hm.ToolArguments, Detail: hm.ToolCallID,
		})
	}
	if len(hm.Iterations) > 0 {
		cm.iterations = make([]cliIterationSnapshot, len(hm.Iterations))
		for i, hi := range hm.Iterations {
			cm.iterations[i] = cliIterationSnapshot(hi)
		}
	}
	// Empty assistant shells carry no visible history information. In particular,
	// do not hide display-only messages that still contain reasoning, tools, or
	// iteration snapshots: those are original append-only records too.
	if hm.Role == "assistant" && hm.Content == "" && hm.ReasoningContent == "" && len(cm.tools) == 0 && len(cm.iterations) == 0 {
		cm.hidden = true
	}
	return cm
}

// trailingContinuableAssistantIndex returns the assistant at the history tail.
// A streaming reply may only continue that exact row; crossing any later row
// would move the reply before an append-only history boundary.
func trailingContinuableAssistantIndex(messages []cliMessage, end int) int {
	if end <= 0 || end > len(messages) {
		return -1
	}
	idx := end - 1
	msg := messages[idx]
	if msg.role != "assistant" || msg.hidden || msg.displayOnly || msg.content == "" || len(msg.tools) > 0 ||
		(msg.recordType != "" && msg.recordType != "message") {
		return -1
	}
	return idx
}

// dedupAppend appends incoming messages to existing, skipping any whose
// identity already appears in existing.
func dedupAppend(existing []cliMessage, incoming []cliMessage) []cliMessage {
	seen := make(map[msgIdentity]bool, len(existing))
	for _, m := range existing {
		seen[cliMessageIdentity(m)] = true
	}
	for _, cm := range incoming {
		id := cliMessageIdentity(cm)
		if !seen[id] {
			existing = append(existing, cm)
			seen[id] = true
		}
	}
	return existing
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
	if msg.snapshotGeneration != 0 && msg.snapshotGeneration != m.sessionSnapshotGeneration {
		return nil
	}
	if msg.mutationGuard && msg.mutationGeneration != m.historyMutationGeneration {
		// A live event arrived after this DB snapshot started. That event is
		// durable before publication, so retrying yields one coherent timeline.
		return []tea.Cmd{m.loadSessionSnapshotCmd(msg.authoritative)}
	}

	// Only clear suLoading for the matching session.
	m.splashState.suLoading = false

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
		m.progressState.current = nil
		m.inputReady = true
	} else {
		// Dedup-append history: skip messages already present (role + timestamp).
		// Handles sequences of identical-role messages (e.g. multiple
		// tool_summary with empty content) — timestamp disambiguates them.
		incoming := make([]cliMessage, 0, len(msg.history))
		for _, hm := range msg.history {
			incoming = append(incoming, toCLIMessage(hm))
		}
		if msg.authoritative {
			m.messages = incoming
		} else {
			m.messages = dedupAppend(m.messages, incoming)
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
			} else {
				m.pendingUserMsg = nil
			}
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
	if !m.splashState.suPhaseConfirmed && msg.activeProgress != nil && msg.activeProgress.Phase != "done" {
		acceptProgress = true
		// Cross-session guard: activeProgress from GetActiveProgress RPC
		// should match the current session. If ChatID is set and doesn't
		// match, treat as no active progress (fall through to default).
		if msg.activeProgress.ChatID != "" {
			currentKey := qualifyChatID(m.channelName, m.chatID)
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
		// Restore it from the server snapshot to prevent applyProgressSnapshot
		// from creating a spurious "iteration 0" snapshot on the next live
		// progress event (symptom: #0 and #1 both show the same reasoning).
		if msg.activeProgress.Iteration > 0 {
			m.progressState.lastIter = msg.activeProgress.Iteration
		}
		m.progressState.current = msg.activeProgress

		// When an active turn is restored from the server snapshot, the
		// last assistant message in DB history corresponds to the in-flight
		// streaming message. We must ensure there is exactly ONE assistant
		// message serving as the streaming slot — either reuse the history
		// assistant or keep startAgentTurn's empty one (when no history assistant).
		//
		// startAgentTurn() always creates a new empty streaming assistant message.
		// If history already has an assistant message, we must replace
		// startAgentTurn's empty message with the history one to avoid showing
		// two assistant messages (the history version + the empty streaming version).
		//
		// EXCEPTION: Phase=="compressing" — compression is a transient operation.
		// The last assistant message in DB is NOT the in-flight streaming message;
		// merging its content into the streaming slot causes the compression
		// indicator to render inside the previous assistant's content.
		if msg.activeProgress.Phase != "compressing" {
			streamIdx := m.streamingMsgIdx
			historyAssistantIdx := trailingContinuableAssistantIndex(m.messages, streamIdx)
			if historyAssistantIdx >= 0 {
				// Replace startAgentTurn's empty shell with the full persisted node so
				// history identity, reasoning, and relation metadata stay intact.
				historyAssistant := m.messages[historyAssistantIdx]
				historyAssistant.isPartial = true
				historyAssistant.turnID = m.agentTurnID
				historyAssistant.dirty = true
				m.messages[streamIdx] = historyAssistant
				// Remove the duplicate history assistant message.
				m.messages = slices.Delete(m.messages, historyAssistantIdx, historyAssistantIdx+1)
				// Adjust streamingMsgIdx after deletion.
				if historyAssistantIdx < streamIdx {
					m.streamingMsgIdx--
				}
			} else if m.streamingMsgIdx < 0 {
				// No startAgentTurn was called (m.typing was true from restoreSession).
				// Only the actual history tail can be continued. Compression markers
				// and other rows are ordering boundaries in append-only history.
				if i := trailingContinuableAssistantIndex(m.messages, len(m.messages)); i >= 0 {
					m.messages[i].isPartial = true
					m.messages[i].dirty = true
					m.messages[i].turnID = m.agentTurnID
					m.streamingMsgIdx = i
				} else {
					m.messages = append(m.messages, cliMessage{
						role:      "assistant",
						content:   "",
						timestamp: time.Now(),
						isPartial: true,
						dirty:     true,
						turnID:    m.agentTurnID,
					})
					m.streamingMsgIdx = len(m.messages) - 1
				}
			}
		} else if m.streamingMsgIdx < 0 {
			// Phase=="compressing" with no streaming slot: create a fresh empty
			// streaming message so the compression indicator renders standalone.
			// Do NOT reuse the last history assistant — that would merge the
			// compression indicator into the previous message's content.
			m.messages = append(m.messages, cliMessage{
				role:      "assistant",
				content:   "",
				timestamp: time.Now(),
				isPartial: true,
				dirty:     true,
				turnID:    m.agentTurnID,
			})
			m.streamingMsgIdx = len(m.messages) - 1
		}

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
		for i := range m.progressState.current.ActiveTools {
			t := &m.progressState.current.ActiveTools[i]
			if t.StartedAt.IsZero() && t.Elapsed > 0 {
				t.StartedAt = time.Now().Add(-time.Duration(t.Elapsed) * time.Millisecond)
			}
		}

		// Rebuild iteration history from server snapshot (authoritative).
		m.progressState.iterations = nil
		m.rc.invalidateProgress()
		if len(msg.activeProgress.IterationHistory) > 0 {
			for _, ih := range msg.activeProgress.IterationHistory {
				snap := cliIterationSnapshot{
					Iteration: ih.Iteration,
					Content:   ih.Content,
					Reasoning: ih.Reasoning,
					Tools:     ih.CompletedTools,
				}
				for i := range snap.Tools {
					t := &snap.Tools[i]
					if t.StartedAt.IsZero() && t.Elapsed > 0 {
						t.StartedAt = time.Now().Add(-time.Duration(t.Elapsed) * time.Millisecond)
					}
				}
				m.progressState.iterations = append(m.progressState.iterations, snap)
			}
			if len(m.progressState.iterations) > 0 {
				lastIter := m.progressState.iterations[len(m.progressState.iterations)-1].Iteration
				if lastIter > m.progressState.lastIter {
					m.progressState.lastIter = lastIter
				}
			}
		}
		// Fallback: if server returned Iteration=0 but iteration history
		// has entries, derive the current iteration from history max.
		// This handles a server-side quirk where activeProgress.Iteration
		// is 0 but IterationHistory is populated during SubAgent session
		// switches (symptom: progress shows #0 while history shows
		// correct #1, #2, ...).
		if m.progressState.current != nil && m.progressState.current.Iteration <= 0 && len(m.progressState.iterations) > 0 {
			m.progressState.current.Iteration = m.progressState.iterations[len(m.progressState.iterations)-1].Iteration
		}

		// Emit a tickCmd to guarantee the fast tick chain is running.
		// Emit a tickCmd to kick the tick chain after restoring.
		// If the restored progress has stream or reasoning content, start the
		// typewriter tick immediately. Without this, the cursor won't blink and
		// streaming content won't animate until the next handleTickMsg cycle.
		hasStream := m.progressState.current != nil && m.progressState.current.StreamContent != "" && m.progressState.twVisible < len([]rune(m.progressState.current.StreamContent))
		hasReasoning := m.progressState.current != nil && m.progressState.current.ReasoningStreamContent != "" && m.progressState.rwVisible < len([]rune(m.progressState.current.ReasoningStreamContent))
		if !m.progressState.twActive && (hasStream || hasReasoning) {
			m.progressState.twActive = true
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
		if m.progressState.current != nil {
			m.progressState.current = nil
			m.rc.valid = false
		}
		// Server says session is idle — enable input.
		m.inputReady = true

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
		// Only reload history if it wasn't already provided in the message.
		// When called from ReconnectRestore, msg.history is pre-populated
		// by the async goroutine — no need for a second blocking RPC.
		if loader := m.channel.config.DynamicHistoryLoader; loader != nil && len(msg.history) == 0 && !msg.authoritative {
			ch, cid := m.channelName, m.chatID
			m.historyReloadGeneration++
			reloadGeneration := m.historyReloadGeneration
			mutationGeneration := m.historyMutationGeneration
			cmds = append(cmds, func() tea.Msg {
				history, err := loader(ch, cid)
				if err != nil {
					return cliHistoryReloadMsg{
						channelName: ch, chatID: cid, err: err,
						mutationGuard: true, mutationGeneration: mutationGeneration, reloadGeneration: reloadGeneration,
					}
				}
				return cliHistoryReloadMsg{
					channelName: ch, chatID: cid, history: history,
					mutationGuard: true, mutationGeneration: mutationGeneration, reloadGeneration: reloadGeneration,
				}
			})
		}
	}
	// A provided TODO callback is authoritative even when it returns nil/empty.
	// Apply it after progress restoration so it also wins for active turns.
	if msg.todosKnown || msg.todos != nil {
		if len(msg.todos) > 0 {
			m.todos = append([]protocol.TodoItem(nil), msg.todos...)
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
	// Restore LLM state for TUI status bar (model name, context limits, etc.)
	// For SubAgent sessions, these come from AgentSessionDump. Both cachedModelName
	// AND activeSubID must come from the same dump to avoid impossible (model, sub)
	// pairs. The parent session's values are restored on switch-back via
	// restoreSession → refreshCachedModelName.
	if msg.modelName != "" {
		m.cachedModelName = msg.modelName
		m.activeSubID = msg.subscriptionID
	}
	if msg.maxContextTokens > 0 {
		m.cachedMaxContextTokens = int(msg.maxContextTokens)
	}
	if msg.maxOutputTokens > 0 {
		m.cachedMaxOutputTokens = msg.maxOutputTokens
	}
	if msg.compressRatio > 0 {
		m.cachedCompressRatio = msg.compressRatio
	}
	if msg.pendingAskUserKnown {
		m.applyPendingAskUserSnapshot(msg.pendingAskUser)
	} else {
		// Local-only legacy clients have no server pending RPC. Keep the disk
		// fallback for them; remote snapshots never trust the local cache.
		cmds = append(cmds, m.checkAndRestorePendingAskUser())
	}
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
	if msg.reloadGeneration != 0 && msg.reloadGeneration != m.historyReloadGeneration {
		return
	}
	if msg.mutationGuard && msg.mutationGeneration != m.historyMutationGeneration {
		m.reloadMessagesFromSessionForRewind(msg.forceFullRebuild, msg.rewindGeneration)
		return
	}
	if msg.err != nil {
		log.WithError(msg.err).Warn("Failed to reload history after compression")
		if msg.rewindGeneration != 0 && m.rewindSync.generation == msg.rewindGeneration {
			m.showSystemMsg(fmt.Sprintf("History rewound, but reload failed: %v", msg.err), feedbackError)
			m.unlockRewind(msg.rewindGeneration)
			m.rewindSync = rewindWarningSync{}
		}
		m.splashState.compReloading = false
		return
	}
	var newMessages []cliMessage
	for _, hm := range msg.history {
		newMessages = append(newMessages, toCLIMessage(hm))
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
		} else {
			m.pendingUserMsg = nil
		}
	}
	restoredStreamingIdx := -1
	if msg.forceFullRebuild {
		// Compression path: HistoryCompacted cleared all messages and did NOT
		// create a streaming message (by design — prevents duplicates).
		// Reuse the current turn's persisted assistant only when it is the history
		// tail. An append-only compression marker (or any other later row) is an
		// ordering boundary, so the live reply must start after it.
		if m.typing {
			if i := trailingContinuableAssistantIndex(newMessages, len(newMessages)); i >= 0 {
				newMessages[i].isPartial = true
				newMessages[i].turnID = m.agentTurnID
				newMessages[i].dirty = true
				restoredStreamingIdx = i
			}
			// No continuable assistant at the history tail: create a distinct live
			// slot after the boundary instead of overwriting an older assistant.
			if restoredStreamingIdx < 0 {
				newMessages = append(newMessages, cliMessage{
					role:      "assistant",
					content:   "",
					timestamp: time.Now(),
					isPartial: true,
					dirty:     true,
					turnID:    m.agentTurnID,
				})
				restoredStreamingIdx = len(newMessages) - 1
			}
		}
	} else {
		// Normal reload path: streaming message was created by startAgentTurn
		// and is still in m.messages. Preserve it across the smart merge.
		// No duplication risk — startAgentTurn creates exactly one.
		if m.typing && m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			streamingMsg := m.messages[m.streamingMsgIdx]
			if streamingMsg.role == "assistant" && streamingMsg.isPartial {
				restoredStreamingIdx = len(newMessages)
				newMessages = append(newMessages, streamingMsg)
			}
		}
	}
	// Smart merge: reuse rendered cache from existing messages to avoid
	// O(N) glamour re-rendering of ALL messages. Only truly new or changed
	// messages need re-rendering. This is critical for sessions with hundreds
	// of iterations where full rebuild would take seconds.
	m.streamingMsgIdx = restoredStreamingIdx
	// Compression reload complete — allow auto-start turn again.
	m.splashState.compReloading = false
	if msg.forceFullRebuild {
		m.messages = newMessages
		if msg.rewindGeneration != 0 && m.rewindSync.generation == msg.rewindGeneration {
			m.rewindSync.reloadSeen = true
			m.finishRewindAfterReload()
		}
		m.invalidateAllCache(false)
		// If engine is still running (typing=true), ensure a streaming message
		// exists — compression may have cleared it. If typing=false, the turn
		// ended during the reload (PhaseDone/endAgentTurn ran) — do NOT auto-start.
		if m.typing && m.streamingMsgIdx < 0 {
			// Compression happened mid-turn: HistoryCompacted cleared messages
			// and streamingMsgIdx, but typing is still true. Without recreating
			// the streaming message, subsequent progress/streaming events have
			// nowhere to render — TUI freezes until restart.
			m.messages = append(m.messages, cliMessage{
				role:      "assistant",
				content:   "",
				timestamp: time.Now(),
				isPartial: true,
				dirty:     true,
				turnID:    m.agentTurnID,
			})
			m.streamingMsgIdx = len(m.messages) - 1
			m.rc.valid = false
		}
		m.updateViewportContent()
		log.WithField("count", len(m.messages)).Info("History reloaded after compression with full rebuild")
		return
	}
	prevMsgCount := len(m.messages)
	allMatched := m.mergeMessagesPreservingCache(newMessages)
	// If ALL messages matched (same content, same count), skip fullRebuild.
	// MUST check count: rewind deletes messages — remaining ones match old
	// cache, but cachedHistoryLines still contains deleted messages' lines.
	if allMatched && m.rc.valid && len(m.messages) == prevMsgCount {
		m.viewport.GotoBottom()
		log.WithField("count", len(m.messages)).Debug("History reloaded (all cached, skipped rebuild)")
		return
	}
	// Some messages are new/dirty or count changed — need rebuild, but only
	// those will be re-rendered. Invalidate the flag so fullRebuild runs.
	m.rc.valid = false
	m.updateViewportContent()
	m.viewport.GotoBottom()
	log.WithField("count", len(m.messages)).Info("History reloaded after compression")

	// NOTE: do NOT call refreshTokenStateAfterReload() here.
	// The HistoryCompacted handler in handleProgressMsg already calls
	// cacheTokenUsage(m.progressState.current.TokenUsage) synchronously, which sets
	// the compressed token count from the engine's progress event.
	// refreshTokenStateAfterReload was an async DB read that could race:
	// it reads the compressed value (e.g. 20k) from DB and sends
	// cliTokenRefreshMsg, which can arrive after post-compression LLM
	// iterations have pushed the count back up (e.g. 50k). The guard
	// (msg.tokenPrompt > m.lastTokenUsage.PromptTokens) should reject
	// lower values, but the async nature introduces unnecessary risk
	// with no benefit — the synchronous cacheTokenUsage already handles it.
}

// handleHistoryLoad loads pre-converted history messages into the model.
func (m *cliModel) handleHistoryLoad(msg cliHistoryLoadMsg) {
	// Stale guard: discard results from a different session.
	if msg.channelName != "" && (msg.channelName != m.channelName || msg.chatID != m.chatID) {
		return
	}
	if len(msg.history) > 0 {
		// Dedup-append using unified identity key (role + timestamp).
		before := len(m.messages)
		m.messages = dedupAppend(m.messages, msg.history)
		added := len(m.messages) - before
		if added > 0 {
			m.invalidateAllCache(false)
			m.updateViewportContent()
			if m.streamingMsgIdx < 0 {
				m.viewport.GotoBottom()
			}
		}
		log.WithFields(log.Fields{"total": len(msg.history), "added": added}).Info("Applied history load in Update loop")
	}
}

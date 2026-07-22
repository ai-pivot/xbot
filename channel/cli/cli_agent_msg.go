package cli

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	ch "xbot/channel"

	log "xbot/logger"
	"xbot/protocol"
)

func (m *cliModel) handleAgentMessage(msg ch.OutboundMsg) {
	// Persist pending AskUser questions BEFORE session filter, so they survive
	// session switches and restarts. Only persist if metadata has ask_questions.
	if msg.WaitingUser && msg.Metadata != nil && msg.Metadata["ask_questions"] != "" && msg.ChatID != "" {
		m.savePendingAskUser(msg.ChatID, msg.Metadata)
	}

	matchesCurrentSession := msg.Channel == "" || msg.ChatID == "" ||
		(msg.Channel == m.channelName && msg.ChatID == m.chatID)

	// suLoading guard: during session switch in remote mode, the history
	// RPC is in-flight. handleSuHistoryLoad will load all messages from DB
	// (including this reply). Without this guard, handleAgentMessage appends
	// the live message (with turnID > 0) and handleSuHistoryLoad then appends
	// the DB version (with turnID = 0) — the dedup key role|timestamp differs
	// because time.Now() ≠ DB timestamp, producing duplicate messages in
	// m.messages that survive fullRebuild (symptom: entire chat block repeated).
	if m.splashState.suLoading {
		if matchesCurrentSession {
			m.historyMutationGeneration++
		}
		log.WithFields(log.Fields{
			"msg_chatid":   msg.ChatID,
			"waiting_user": msg.WaitingUser,
		}).Debug("handleAgentMessage: suLoading, discarding (session switch in progress)")
		return
	}

	// Filter by session: only process outbound for the currently viewed session.
	if msg.Channel != "" && msg.ChatID != "" {
		if msg.Channel != m.channelName || msg.ChatID != m.chatID {
			log.WithFields(log.Fields{
				"msg_channel":    msg.Channel,
				"msg_chatid":     msg.ChatID,
				"my_channelName": m.channelName,
				"my_chatid":      m.chatID,
				"waiting_user":   msg.WaitingUser,
			}).Warn("handleAgentMessage: session filter rejected outbound message")
			return
		}
	} else {
		// ChatID empty: this is a defensive warning. Messages without proper
		// session identity risk cross-session contamination. Log at error level
		// to make it visible.
		log.WithFields(log.Fields{
			"msg_channel":    msg.Channel,
			"msg_chatid":     msg.ChatID,
			"my_channelName": m.channelName,
			"my_chatid":      m.chatID,
			"waiting_user":   msg.WaitingUser,
			"content_len":    len(msg.Content),
		}).Error("handleAgentMessage: ChatID empty — filter bypassed, risk of cross-session contamination")
	}
	m.historyMutationGeneration++

	turnID := m.agentTurnID // capture at entry for stale-signal guard
	content := msg.Content

	// Cancel ack handling: when a Run is cancelled, the agent sends outbound
	// messages with metadata cancelled=true. These belong to the cancelled turn,
	// not the current turn.
	isCancelledAck := msg.Metadata != nil && msg.Metadata["cancelled"] == "true"
	if isCancelledAck {
		m.handleCancelAck(msg, turnID)
		return
	}

	// 处理 __FEISHU_CARD__ 协议（简化显示）
	if strings.HasPrefix(content, "__FEISHU_CARD__") {
		content = ch.ConvertFeishuCard(content)
	}

	// Empty content with no waiting user: end turn and flush queue,
	// but don't append a blank message.
	// Guard: when AskUser panel is open, the turn is paused (not ended).
	// A late-arriving empty-content outbound (e.g. from engine cleanup) must
	// not trigger endAgentTurn, which clears iterationHistory and makes all
	// previous iterations disappear from the viewport.
	if content == "" && !msg.WaitingUser && len(msg.ToolsUsed) == 0 && m.panelState.mode != "askuser" {
		// Persist token usage before clearing progress
		if m.progressState.current != nil {
			m.cacheTokenUsage(m.progressState.current.TokenUsage)
		}
		// Finalize the streaming message: even though the reply is empty,
		// the streaming message (created by startAgentTurn) must be marked
		// as completed. Without this, isPartial stays true and the message
		// renders as a streaming "..." block forever. When the next turn
		// starts (e.g. bg task notification), a new streaming message is
		// created alongside the old one → two Assistant blocks appear.
		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) &&
			m.messages[m.streamingMsgIdx].turnID == turnID {
			m.messages[m.streamingMsgIdx].isPartial = false
			m.messages[m.streamingMsgIdx].dirty = true
		} else if existingIdx := m.findMessageByTurn(turnID, "assistant"); existingIdx >= 0 {
			m.messages[existingIdx].isPartial = false
			m.messages[existingIdx].dirty = true
		} else {
			log.WithField("turnID", turnID).Warn("handleAgentMessage: streaming message not found to finalize")
		}
		m.streamingMsgIdx = -1
		m.progressState.current = nil
		m.endAgentTurn(turnID)
		m.replyProcessed = true
		if turnID == m.agentTurnID {
			m.inputReady = true
			m.tryFlushMessageQueue()
		}
		return
	}

	if msg.IsPartial {
		// Update existing streaming message (created by startAgentTurn) or reuse one.
		// NEVER create a new assistant message if the last message is already an
		// assistant — this would produce two consecutive Assistant blocks.
		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) &&
			m.messages[m.streamingMsgIdx].turnID == turnID {
			// Update existing streaming message
			m.messages[m.streamingMsgIdx].content = content
			m.messages[m.streamingMsgIdx].dirty = true
		} else if existingIdx := m.findMessageByTurn(turnID, "assistant"); existingIdx >= 0 {
			// Reuse existing message for this turn (prevents duplicate streaming messages
			// when streamingMsgIdx was stale or cleared by endAgentTurn).
			m.streamingMsgIdx = existingIdx
			m.messages[existingIdx].content = content
			m.messages[existingIdx].isPartial = true
			m.messages[existingIdx].dirty = true
		} else if len(m.messages) > 0 && m.messages[len(m.messages)-1].role == "assistant" &&
			m.messages[len(m.messages)-1].isPartial && m.messages[len(m.messages)-1].content == "" {
			// Last message is an EMPTY streaming placeholder — reuse it instead of
			// creating a new one. This is the hard guard against two consecutive
			// Assistant blocks. Only applies to empty placeholders, NOT to
			// completed assistants from previous turns (which have content and
			// should be preserved as separate messages).
			m.streamingMsgIdx = len(m.messages) - 1
			m.messages[m.streamingMsgIdx].content = content
			m.messages[m.streamingMsgIdx].isPartial = true
			m.messages[m.streamingMsgIdx].dirty = true
			m.messages[m.streamingMsgIdx].turnID = turnID
		} else {
			// Create new streaming message (only when last message is NOT an assistant)
			m.streamingMsgIdx = len(m.messages)
			m.messages = append(m.messages, cliMessage{
				role:      "assistant",
				content:   content,
				timestamp: time.Now(),
				isPartial: true,
				dirty:     true,
				turnID:    turnID,
			})
		}
	} else {
		// 完整消息 — save the message index for later thinking capture
		var completedMsgIdx int

		// Compute iterations to bake into the assistant message.
		// Use progressState.iterations (PhaseDone does NOT clear it anymore).
		// Dedup by iteration number: applyProgressSnapshot creates per-iteration
		// snapshots before PhaseDone may create a conflated one with ALL completed tools.
		// Fallback: preserve existing iterations from the streaming message
		// (e.g. saved by cancel ack before this response arrived).
		var bakeIterations []cliIterationSnapshot
		if len(m.progressState.iterations) > 0 {
			seen := make(map[int]bool)
			for _, it := range m.progressState.iterations {
				if !seen[it.Iteration] {
					seen[it.Iteration] = true
					bakeIterations = append(bakeIterations, it)
				}
			}
		}
		if len(bakeIterations) == 0 && m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			bakeIterations = m.messages[m.streamingMsgIdx].iterations
		}
		// Append the last iteration from m.progressState.current if it wasn't already
		// captured in iterationHistory. The last iteration's data (tools,
		// reasoning) is typically in m.progressState.current but hasn't been snapshotted
		// by applyProgressSnapshot (which only fires on iteration N→N+1
		// transitions). Without this, AskUser messages lose the last
		// iteration's tools from the viewport.
		// Guard: use progressState.current.CompletedTools (maintained in-place
		// by applyProgressSnapshot) and only run when the iteration is genuinely missing.
		if m.progressState.current != nil && m.progressState.lastIter >= 0 {
			iterNum := m.progressState.lastIter
			if m.progressState.current.Iteration > 0 {
				iterNum = m.progressState.current.Iteration
			}
			alreadyBaked := false
			for _, it := range bakeIterations {
				if it.Iteration == iterNum {
					alreadyBaked = true
					break
				}
			}
			if !alreadyBaked {
				var finalTools []protocol.ToolProgress
				for _, t := range m.progressState.current.CompletedTools {
					if t.Iteration == 0 || t.Iteration == iterNum {
						finalTools = append(finalTools, t)
					}
				}
				// Include ALL active tools — turn is ending, so running/pending
				// tools have completed. Mark them as done (done event may have
				// been lost in progressSlot coalescing).
				for _, t := range m.progressState.current.ActiveTools {
					if t.Status == "running" || t.Status == "pending" || t.Status == "" {
						t.Status = "done"
					}
					if (t.Iteration == 0 || t.Iteration == iterNum) && !containsToolProgress(finalTools, t) {
						finalTools = append(finalTools, t)
					}
				}
				reasoning := m.progressState.current.Reasoning
				if reasoning == "" {
					reasoning = m.progressState.current.ReasoningStreamContent
				}
				snap := cliIterationSnapshot{
					Iteration:   iterNum,
					Content:     m.progressState.current.Content,
					Reasoning:   reasoning,
					Tools:       finalTools,
					ElapsedWall: time.Since(m.progressState.iterStart).Milliseconds(),
				}
				if len(snap.Tools) > 0 || snap.Content != "" || snap.Reasoning != "" {
					bakeIterations = append(bakeIterations, snap)
				}
			}
		}

		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) &&
			m.messages[m.streamingMsgIdx].turnID == turnID {
			// 更新流式消息为完整消息 (turnID 校验：防止跨 turn 覆盖)
			// MERGE iterations: finalizeTurnFromSnapshot may have already baked
			// iterations that progressState.iterations lost (due to progressSlot
			// coalescing or Hub storeStateless overwrite). Take the union of
			// existing and new iterations, dedup by iteration number.
			m.messages[m.streamingMsgIdx].content = content
			m.messages[m.streamingMsgIdx].isPartial = false
			m.messages[m.streamingMsgIdx].dirty = true
			m.messages[m.streamingMsgIdx].turnID = turnID
			m.messages[m.streamingMsgIdx].iterations = mergeIterations(m.messages[m.streamingMsgIdx].iterations, bakeIterations)
			completedMsgIdx = m.streamingMsgIdx
		} else {
			// Find existing assistant message for this turn, or append new.
			existingIdx := m.findMessageByTurn(turnID, "assistant")
			if existingIdx >= 0 {
				m.messages[existingIdx] = cliMessage{
					role:       "assistant",
					content:    content,
					timestamp:  time.Now(),
					isPartial:  false,
					dirty:      true,
					turnID:     turnID,
					iterations: mergeIterations(m.messages[existingIdx].iterations, bakeIterations),
				}
				completedMsgIdx = existingIdx
			} else {
				// No existing assistant for this turn — check if last message
				// is an EMPTY placeholder assistant. Only reuse if it's empty;
				// a completed assistant from a previous turn must be preserved.
				if len(m.messages) > 0 && m.messages[len(m.messages)-1].role == "assistant" &&
					m.messages[len(m.messages)-1].isPartial && m.messages[len(m.messages)-1].content == "" {
					m.messages[len(m.messages)-1] = cliMessage{
						role:       "assistant",
						content:    content,
						timestamp:  time.Now(),
						isPartial:  false,
						dirty:      true,
						turnID:     turnID,
						iterations: mergeIterations(m.messages[len(m.messages)-1].iterations, bakeIterations),
					}
					completedMsgIdx = len(m.messages) - 1
				} else {
					m.messages = append(m.messages, cliMessage{
						role:       "assistant",
						content:    content,
						timestamp:  time.Now(),
						isPartial:  false,
						dirty:      true,
						turnID:     turnID,
						iterations: bakeIterations,
					})
					completedMsgIdx = len(m.messages) - 1
				}
			}
		}
		// 重置流式状态
		m.streamingMsgIdx = -1
		// Capture reasoning from progress before it might be cleared.
		// Do NOT clear m.progressState.current here — progress is only cleared by endAgentTurn.
		// Intermediate text messages (e.g. thinking content) arrive while the agent
		// is still running; clearing progress here would hide the progress panel
		// and make it look like the turn ended prematurely.
		// IMPORTANT: Do NOT fallback to m.progressState.current.ReasoningStreamContent.
		// ReasoningStreamContent is a streaming accumulator with no per-iteration
		// boundary. When handleAgentMessage arrives after the next structured
		// progress has advanced m.progressState.current.Iteration, ReasoningStreamContent may
		// still contain the previous iteration's content — causing the previous
		// iteration's reasoning to be misattributed.
		if turnID == m.agentTurnID && m.progressState.current != nil {
			if m.progressState.current.Content != "" {
				m.lastContent = m.progressState.current.Content
			}
		}
		// Store captured thinking on the completed message for Thinking Box rendering.
		if completedMsgIdx >= 0 && completedMsgIdx < len(m.messages) {
			thinking := ""
			if m.progressState.current != nil {
				thinking = m.progressState.current.Reasoning
			}
			if thinking == "" {
				thinking = m.lastContent
			}
			if thinking != "" {
				m.messages[completedMsgIdx].reasoning = thinking
			}
		}
		// Targeted re-render: the message was already cached by
		// endAgentTurn→relayoutViewport→appendNewMessagesToCache with incomplete
		// streaming content. Now that the final reply arrived, re-render JUST
		// this one message (O(1)) instead of invalidating the entire cache
		// (O(N) fullRebuild → flicker).
		m.rerenderCachedMessage(completedMsgIdx)

		// §11.5 Session reset: clear messages and token usage bar after /new
		if msg.Metadata != nil && msg.Metadata["session_reset"] == "true" {
			m.lastTokenUsage = nil
			m.cachedMaxContextTokens = 0 // reset context budget — solid line until next progress
			m.messages = make([]cliMessage, 0, cliMsgBufSize)
			m.streamingMsgIdx = -1
			m.rc.history = ""
			m.rc.wrapHistory = ""
			m.rc.wrapRaw = ""
			m.rc.wrapWidth = 0
			m.rc.histMaxW = 0
			m.rc.histLines = nil
			m.rc.bumpHistGen()
			// PhaseDone from emitBuiltinProgressDone should arrive before this outbound,
			// so endAgentTurn is usually a no-op (turn already ended). Kept as safety net.
			m.endAgentTurn(m.agentTurnID)
			m.invalidateAllCache(true)
			m.viewport.GotoBottom()
		}

		// §12 AskUser panel: detect WaitingUser and open interactive panel
		if msg.WaitingUser {
			var items []askItem
			if msg.Metadata != nil {
				if qJSON := msg.Metadata["ask_questions"]; qJSON != "" {
					// Multi-question mode: parse questions array
					var qs []askQItem
					if json.Unmarshal([]byte(qJSON), &qs) == nil {
						for _, q := range qs {
							items = append(items, askItem{Question: q.Question, Options: q.Options})
						}
					}
				}
			}
			// Fallback: search message history for ❓ (legacy single-question format)
			if len(items) == 0 {
				for i := len(m.messages) - 1; i >= 0; i-- {
					if strings.HasPrefix(m.messages[i].content, "❓") {
						question := strings.TrimSpace(strings.TrimPrefix(m.messages[i].content, "❓"))
						m.messages = append(m.messages[:i], m.messages[i+1:]...)
						if question != "" {
							items = append(items, askItem{Question: question})
						}
						break
					}
				}
			}
			if len(items) > 0 {
				m.updateViewportContent()
				m.askUserSession = m.chatID // bind AskUser to current session
				m.openAskUserPanel(items, func(answers map[string]string) {
					// Clean up persisted pending question now that user answered.
					m.deletePendingAskUser(m.askUserSession)
					// Format answers as tool-call style message
					var parts []string
					for i, item := range items {
						key := fmt.Sprintf("q%d", i)
						ans := answers[key]
						parts = append(parts, fmt.Sprintf("Q: %s\nA: %s", item.Question, ans))
					}
					content := strings.Join(parts, "\n\n")
					// Send to agent as tool result replacement (not a new user message).
					// Use blocking send with timeout — ask_user answers are critical:
					// if dropped, the agent hangs indefinitely waiting for a response.
					if !m.sendInboundWait(m.newInbound(content, map[string]string{"ask_user_answered": "true"}), 5*time.Second) {
						m.showSystemMsg("Failed to deliver answer to agent, please try again", feedbackError)
					}
					// Show answers as a system message (was previously a tool_summary)
					m.messages = append(m.messages, cliMessage{
						role:      "system",
						content:   "AskUser: " + fmt.Sprintf("answered %d question(s)", len(items)),
						timestamp: time.Now(),
						dirty:     true,
					})
					// Show answers as system message
					var answerParts []string
					for i, item := range items {
						key := fmt.Sprintf("q%d", i)
						ans := answers[key]
						answerParts = append(answerParts, fmt.Sprintf("  %s → %s", item.Question, ans))
					}
					m.showSystemMsg(strings.Join(answerParts, "\n"), feedbackInfo)
					m.startAgentTurn()
					m.updateViewportContent()
				}, func() {
					// Clean up persisted pending question on cancel.
					m.deletePendingAskUser(m.askUserSession)
					m.showSystemMsg(m.locale.AskCancelled, feedbackInfo)
					m.typing = false
					m.updatePlaceholder()
					m.inputReady = true
					m.resetProgressState()
					m.updateViewportContent()
				})
				return
			}
		}

		// Snapshot the final iteration before clearing
		if m.progressState.current != nil && m.progressState.lastIter >= 0 && (len(m.progressState.current.CompletedTools) > 0 || m.lastContent != "") {
			alreadySnapped := false
			for _, s := range m.progressState.iterations {
				if s.Iteration == m.progressState.lastIter {
					alreadySnapped = true
					break
				}
			}
			if !alreadySnapped {
				var finalTools []protocol.ToolProgress
				// Filter to only include tools from the current iteration.
				// Without filtering, stale tools from previous iterations
				// (carried in CompletedTools by applyProgressSnapshot) would
				// pollute this iteration's snapshot.
				for _, t := range m.progressState.current.CompletedTools {
					if t.Iteration == m.progressState.lastIter {
						finalTools = append(finalTools, t)
					}
				}
				for _, t := range m.progressState.current.ActiveTools {
					if (t.Status == "done" || t.Status == "error") && t.Iteration == m.progressState.lastIter {
						finalTools = append(finalTools, t)
					}
				}
				reasoning := m.progressState.current.Reasoning
				snap := cliIterationSnapshot{
					Iteration:   m.progressState.lastIter,
					Reasoning:   reasoning,
					Content:     m.lastContent,
					Tools:       finalTools,
					ElapsedWall: time.Since(m.progressState.iterStart).Milliseconds(),
				}
				if len(finalTools) > 0 || reasoning != "" || m.lastContent != "" {
					m.progressState.iterations = append(m.progressState.iterations, snap)
				}
			}
		}

		// Update assistant message iterations if we have richer local data
		// that wasn't captured at assistant message creation time (step above).
		// The assistant message already has iterations from progressState.iterations
		// (if PhaseDone arrived first) or from iterationHistory (if not).
		// The final snapshot just above may have added more iterations.
		if len(m.progressState.iterations) > 0 {
			asstIdx := m.findMessageByTurn(turnID, "assistant")
			if asstIdx >= 0 {
				existing := m.messages[asstIdx]
				existingIters := make(map[int]bool)
				for _, it := range existing.iterations {
					existingIters[it.Iteration] = true
				}
				for _, it := range m.progressState.iterations {
					if !existingIters[it.Iteration] {
						existing.iterations = append(existing.iterations, it)
					}
				}
				existing.dirty = true
				m.messages[asstIdx] = existing
				m.rc.valid = false
			}
		}

		// Mark reply as received and reset iteration tracking state.
		// When WaitingUser is true (AskUser), the turn is paused not ended —
		// endAgentTurn would clear iterationHistory and progress, causing all
		// previous iterations to disappear. The turn will be ended later when
		// the agent completes after receiving the user's answer.
		if !msg.WaitingUser {
			m.endAgentTurn(turnID)
			m.replyProcessed = true
			if turnID == m.agentTurnID {
				m.inputReady = true
				m.tryFlushMessageQueue()
			}
		}

	}

	m.updateViewportContent()
}

// handleCancelAck processes the cancel acknowledgement from the agent.
// When a Run is cancelled, the agent sends outbound messages with metadata
// cancelled=true. These belong to the cancelled turn, not the current turn.
// This method finalizes or removes the cancelled turn's streaming message,
// cleans up progress state, and restores user-ready state.
func (m *cliModel) handleCancelAck(msg ch.OutboundMsg, turnID uint64) {
	// Find the streaming message that belongs to the cancelled turn.
	// When a new turn starts (via startAgentTurn) before the cancel ack
	// arrives, m.streamingMsgIdx points to the NEW turn's message.
	// Using cancelTargetTurnID ensures we only finalize the correct message.
	cancelledIdx := -1
	if m.cancelTargetTurnID != 0 {
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].turnID == m.cancelTargetTurnID && m.messages[i].isPartial {
				cancelledIdx = i
				break
			}
		}
	}
	// Fallback: if cancelTargetTurnID is not set (e.g. cancel from external
	// source), use the current streaming message index — but only if its
	// turnID matches m.agentTurnID AND no cancel ack has already been
	// processed for this session (prevents stale second cancel ack from
	// async goroutine race from matching the wrong streaming message).
	if cancelledIdx < 0 && m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
		if !m.cancelAckProcessed && m.messages[m.streamingMsgIdx].turnID == m.agentTurnID {
			cancelledIdx = m.streamingMsgIdx
		}
	}

	if cancelledIdx >= 0 {
		streamingMsg := &m.messages[cancelledIdx]

		// ALWAYS capture the live iteration from progressState. The old
		// branching logic conditionally skipped cancelledTurnIterations()
		// when streamingMsg.iterations was non-empty (from AskUser/partial
		// reply), losing the latest live iteration. PhaseDone may not arrive
		// (ctx wrapper drops it on cancel), so this fallback is the ONLY
		// way to capture the live iteration from m.progressState.current.
		iters := m.cancelledTurnIterations()
		if len(iters) > 0 {
			streamingMsg.iterations = iters
		}
		streamingMsg.isPartial = false
		streamingMsg.dirty = true

		// If the streaming message has NO content and NO iterations,
		// there's nothing to display — remove it.
		if strings.TrimSpace(streamingMsg.content) == "" && len(streamingMsg.iterations) == 0 {
			m.messages = append(m.messages[:cancelledIdx], m.messages[cancelledIdx+1:]...)
			if m.streamingMsgIdx == cancelledIdx {
				m.streamingMsgIdx = -1
			} else if m.streamingMsgIdx > cancelledIdx {
				m.streamingMsgIdx--
			}
			if cancelledIdx >= len(m.messages) {
				m.rc.valid = false
			}
		}
	}
	// Clean up progress/streaming state for the cancelled turn.
	if m.progressState.current != nil {
		m.cacheTokenUsage(m.progressState.current.TokenUsage)
	}
	// Clear pendingUserMsg on cancel. processMessage eager-saves the user message
	// to DB BEFORE Run() starts, so by the time cancel happens the message is
	// already persisted. Keeping pendingUserMsg set causes a duplicate on the
	// next history reload: the pendingUserMsg content (e.g. "/goal objective")
	// won't match the DB-saved content (e.g. "objective" without /goal prefix
	// because processMessage strips the command prefix before eager-save), so
	// handleHistoryReload treats it as "not yet persisted" and appends it again.
	m.pendingUserMsg = nil
	if m.streamingMsgIdx >= 0 {
		m.streamingMsgIdx = -1
	}
	m.progressState.current = nil
	m.typing = false
	m.turnCancelled = false
	m.replyProcessed = true
	m.cancelTargetTurnID = 0
	m.cancelAckProcessed = true
	m.inputReady = true
	m.tryFlushMessageQueue()
	m.rerenderCachedMessage(cancelledIdx)
	// Push the updated cache to the viewport immediately so View() renders
	// the correct content in this frame. Without this, the viewport shows
	// stale streaming content until the next 100ms tick — the user perceives
	// this as "history disappears then re-renders" because the streaming view
	// (inline iterations/tools) is suddenly replaced by the idle view.
	m.updateViewportContent()
}

// tryFlushMessageQueue arms the tick handler to drain queued messages
// when the message queue has pending items. Sets a one-tick delay to
// allow pending injected messages (bg notifications) to arrive first.
func (m *cliModel) tryFlushMessageQueue() {
	if len(m.messageQueue) > 0 {
		m.needFlushQueue = true
		m.flushDelay = 1 // one tick (~100ms) for pending asyncCh messages
	}
}

func (m *cliModel) cancelledTurnIterations() []cliIterationSnapshot {
	var iterations []cliIterationSnapshot
	if len(m.progressState.iterations) > 0 {
		iterations = append(iterations, m.progressState.iterations...)
	}
	if m.progressState.current == nil {
		return append([]cliIterationSnapshot{}, iterations...)
	}

	iterNum := m.progressState.current.Iteration
	if iterNum == 0 && m.progressState.lastIter != 0 {
		iterNum = m.progressState.lastIter
	}
	for _, it := range iterations {
		if it.Iteration == iterNum {
			return append([]cliIterationSnapshot{}, iterations...)
		}
	}

	tools := append([]protocol.ToolProgress{}, m.progressState.current.CompletedTools...)
	for _, tool := range m.progressState.current.ActiveTools {
		if !containsToolProgress(tools, tool) {
			tools = append(tools, tool)
		}
	}
	reasoning := m.progressState.current.Reasoning
	// Capture streamed reasoning as fallback (LLM was still streaming when
	// Ctrl+C interrupted). m.progressState.current is the live progress the
	// user was watching — ReasoningStreamContent is what they saw on screen.
	if reasoning == "" && m.progressState.current.ReasoningStreamContent != "" {
		reasoning = m.progressState.current.ReasoningStreamContent
	}
	// Capture streamed content as fallback when structured Thinking is empty.
	// This preserves partial LLM output that was streamed but not yet finalized
	// by recordAssistantMsg when Ctrl+C interrupted.
	content := m.progressState.current.Content
	if content == "" && m.progressState.current.StreamContent != "" {
		content = m.progressState.current.StreamContent
	}
	snap := cliIterationSnapshot{
		Iteration:   iterNum,
		Content:     content,
		Reasoning:   reasoning,
		Tools:       tools,
		ElapsedWall: time.Since(m.progressState.iterStart).Milliseconds(),
	}
	if snap.Content != "" || snap.Reasoning != "" || len(snap.Tools) > 0 {
		iterations = append(iterations, snap)
	}
	return append([]cliIterationSnapshot{}, iterations...)
}

func containsToolProgress(tools []protocol.ToolProgress, needle protocol.ToolProgress) bool {
	for _, tool := range tools {
		if tool.Name == needle.Name && tool.Label == needle.Label && tool.Iteration == needle.Iteration {
			return true
		}
	}
	return false
}

// mergeIterations takes the union of two iteration snapshots, deduplicating
// by iteration number. When both slices have an entry for the same iteration,
// the one from `newer` wins (it typically has more complete data from
// progressState). This prevents handleAgentMessage from overwriting iterations
// that were already baked by finalizeTurnFromSnapshot — the exact bug that
// caused iteration 0 to permanently disappear when PhaseDone arrived before
// the outbound reply.
func mergeIterations(existing, newer []cliIterationSnapshot) []cliIterationSnapshot {
	if len(existing) == 0 {
		return newer
	}
	if len(newer) == 0 {
		return existing
	}
	merged := make([]cliIterationSnapshot, 0, len(existing)+len(newer))
	seen := make(map[int]bool, len(existing)+len(newer))
	// Start with existing (may have iterations from finalizeTurnFromSnapshot)
	for _, it := range existing {
		if !seen[it.Iteration] {
			seen[it.Iteration] = true
			merged = append(merged, it)
		}
	}
	// Add new iterations from bakeIterations (newer data wins for dupes)
	mergedIdx := make(map[int]int)
	for i, it := range merged {
		mergedIdx[it.Iteration] = i
	}
	for _, it := range newer {
		if idx, ok := mergedIdx[it.Iteration]; ok {
			merged[idx] = it // newer wins
		} else {
			merged = append(merged, it)
			mergedIdx[it.Iteration] = len(merged) - 1
		}
	}
	// Sort by iteration number for stable rendering
	slices.SortFunc(merged, func(a, b cliIterationSnapshot) int {
		return a.Iteration - b.Iteration
	})
	return merged
}

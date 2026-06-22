package cli

import (
	"encoding/json"
	"fmt"
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

	// suLoading guard: during session switch in remote mode, the history
	// RPC is in-flight. handleSuHistoryLoad will load all messages from DB
	// (including this reply). Without this guard, handleAgentMessage appends
	// the live message (with turnID > 0) and handleSuHistoryLoad then appends
	// the DB version (with turnID = 0) — the dedup key role|timestamp differs
	// because time.Now() ≠ DB timestamp, producing duplicate messages in
	// m.messages that survive fullRebuild (symptom: entire chat block repeated).
	if m.splashState.suLoading {
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
		// session identity risk cross-session contamination. The dedupMessagesGuard
		// will catch any resulting duplicates, but the root cause should be fixed
		// at the sender. Log at error level to make it visible.
		log.WithFields(log.Fields{
			"msg_channel":    msg.Channel,
			"msg_chatid":     msg.ChatID,
			"my_channelName": m.channelName,
			"my_chatid":      m.chatID,
			"waiting_user":   msg.WaitingUser,
			"content_len":    len(msg.Content),
		}).Error("handleAgentMessage: ChatID empty — filter bypassed, risk of cross-session contamination")
	}

	turnID := m.agentTurnID // capture at entry for stale-signal guard
	content := msg.Content

	// Cancel ack handling: when a Run is cancelled, the agent sends outbound
	// messages with metadata cancelled=true. These belong to the cancelled turn,
	// not the current turn. If a new turn has already started (bg notification
	// injection arrived first via cliInjectedUserMsg), these cancel acks would
	// match the new turn's ID and incorrectly endAgentTurn. Skip turn-ending
	// logic for cancel acks to preserve the new turn's state.
	isCancelledAck := msg.Metadata != nil && msg.Metadata["cancelled"] == "true"
	if isCancelledAck {
		// Find the streaming message that belongs to the cancelled turn.
		// When a new turn starts (via startAgentTurn) before the cancel ack
		// arrives, m.streamingMsgIdx points to the NEW turn's message and
		// m.pendingToolSummary may still hold OLD turn's iteration data.
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
		// turnID matches m.agentTurnID (i.e. no new turn has started).
		if cancelledIdx < 0 && m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			if m.messages[m.streamingMsgIdx].turnID == m.agentTurnID {
				cancelledIdx = m.streamingMsgIdx
			}
		}

		if cancelledIdx >= 0 {
			streamingMsg := &m.messages[cancelledIdx]
			if strings.TrimSpace(streamingMsg.content) != "" {
				// Streaming message accumulated real content (e.g. partial LLM text).
				// Finalize it as a completed message so the user keeps what was streamed.
				streamingMsg.isPartial = false
				streamingMsg.dirty = true
				// Only set iterations from cancelledTurnIterations() if the message
				// doesn't already have baked iterations. handleProgressDone's cancel
				// path bakes iterations BEFORE endAgentTurn clears iterationHistory.
				// By the time this cancel ack arrives, iterationHistory is already
				// empty, so cancelledTurnIterations() would return nothing and
				// OVERWRITE the baked iterations — causing the exact bug where
				// "Ctrl+C loses iterations but restart brings them back".
				if len(streamingMsg.iterations) == 0 {
					streamingMsg.iterations = m.cancelledTurnIterations()
				}
			} else if len(streamingMsg.iterations) > 0 {
				// Empty content but has iteration data (tool tags, reasoning).
				// The user saw these rendered inline during the cancelled turn.
				// Finalize instead of removing so the progress is preserved.
				// (iterations already baked by handleProgressDone cancel path)
				streamingMsg.isPartial = false
				streamingMsg.dirty = true
			} else if iters := m.cancelledTurnIterations(); len(iters) > 0 {
				// Empty content but iterations exist in iterationHistory/pendingToolSummary
				// (cancel ack arrived before PhaseDone). Finalize and bake iterations.
				streamingMsg.isPartial = false
				streamingMsg.dirty = true
				streamingMsg.iterations = iters
			} else {
				// Empty streaming message (shell created by startAgentTurn)
				// with no iteration data. Remove it — keeping an empty assistant
				// creates a phantom message that doesn't match DB history.
				m.messages = append(m.messages[:cancelledIdx], m.messages[cancelledIdx+1:]...)
				if m.streamingMsgIdx == cancelledIdx {
					m.streamingMsgIdx = -1
				} else if m.streamingMsgIdx > cancelledIdx {
					m.streamingMsgIdx--
				}
			}
		}
		// Still clean up progress/streaming state for the cancelled turn.
		// Do NOT endAgentTurn — the current turn (if any) must remain active.
		if m.progressState.current != nil {
			m.cacheTokenUsage(m.progressState.current.TokenUsage)
		}
		// Restore pendingUserMsg: startAgentTurn cleared it, but if the engine
		// hasn't persisted the user message to DB yet (immediate Ctrl+C), a
		// subsequent reloadMessagesFromSession would lose it. Re-save the last
		// user message from m.messages so handleHistoryReload can restore it.
		if m.pendingUserMsg == nil {
			for i := len(m.messages) - 1; i >= 0; i-- {
				if m.messages[i].role == "user" {
					cp := m.messages[i]
					m.pendingUserMsg = &cp
					break
				}
			}
		}
		if m.streamingMsgIdx >= 0 {
			m.streamingMsgIdx = -1
		}
		m.pendingToolSummary = nil // clear stale iteration data from cancelled turn
		m.progressState.current = nil
		m.typing = false        // clear typing indicator immediately after cancel
		m.turnCancelled = false // cancel complete, allow future turns
		m.cancelTargetTurnID = 0
		// Match every other turn-end path: set inputReady so the user can send
		// directly (status bar already shows "就绪" because typing=false), and
		// arm needFlushQueue so the tick handler drains queued messages.
		// Without this, Ctrl+C leaves inputReady=false: new messages silently
		// queue (📬N) but never flush — the state bar says "ready" but the
		// queue is stuck.
		m.inputReady = true
		if len(m.messageQueue) > 0 {
			m.needFlushQueue = true
		}
		// Keep rc.valid = true — same rationale as the normal completion path.
		// The cancelled message's isPartial=false / dirty=true changes are
		// picked up lazily by appendNewMessagesToCache (if not yet cached) or
		// on the next fullRebuild triggered by a real layout change.
		// Avoids fullRebuild flicker after Ctrl+C.
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
		m.streamingMsgIdx = -1
		m.pendingToolSummary = nil // prevent cross-turn leak (same class as U1 A1 U2 A1 bug)
		m.progressState.current = nil
		m.setTurnReplyReceived(turnID)
		m.endAgentTurn(turnID)
		if turnID == m.agentTurnID {
			m.inputReady = true
			if len(m.messageQueue) > 0 {
				m.needFlushQueue = true
			}
		}
		return
	}

	if msg.IsPartial {
		// Update existing streaming message (created by startAgentTurn) or create new one.
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
		} else {
			// Create new streaming message (fallback)
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
		// If PhaseDone already processed this turn, use iterations stored in pendingToolSummary.
		// Otherwise (PhaseDone hasn't arrived yet), use local iterationHistory.
		// Fallback: preserve existing iterations from the streaming message
		// (e.g. saved by cancel ack before this response arrived).
		var bakeIterations []cliIterationSnapshot
		if m.isTurnDoneProcessed(turnID) && m.pendingToolSummary != nil {
			bakeIterations = m.pendingToolSummary.iterations
		} else if len(m.progressState.iterations) > 0 {
			bakeIterations = append([]cliIterationSnapshot{}, m.progressState.iterations...)
		}
		if len(bakeIterations) == 0 && m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			bakeIterations = m.messages[m.streamingMsgIdx].iterations
		}
		// Append the last iteration from m.progressState.current if it wasn't already
		// captured in iterationHistory. The last iteration's data (tools,
		// reasoning) is typically in m.progressState.current but hasn't been snapshotted
		// by snapshotIterationChange (which only fires on iteration N→N+1
		// transitions). Without this, AskUser messages lose the last
		// iteration's tools from the viewport.
		// Guard: use lastCompletedTools (filtered per-iteration) instead of
		// m.progressState.current.CompletedTools (may contain stale tools from earlier
		// iterations), and only run when the iteration is genuinely missing.
		if m.progressState.current != nil && m.progressState.lastIter >= 0 && msg.WaitingUser {
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
				// Use lastCompletedTools — these are per-iteration filtered
				// (cleared by snapshotIterationChange on N→N+1), unlike
				// m.progressState.current.CompletedTools which accumulates stale tools.
				var finalTools []protocol.ToolProgress
				finalTools = append(finalTools, m.lastCompletedTools...)
				for _, t := range m.progressState.current.ActiveTools {
					if t.Status == "done" || t.Status == "error" {
						if !containsToolProgress(finalTools, t) {
							finalTools = append(finalTools, t)
						}
					}
				}
				reasoning := m.progressState.current.Reasoning
				if reasoning == "" && m.reasoningByIter != nil {
					reasoning = m.reasoningByIter[iterNum]
				}
				if reasoning == "" {
					reasoning = m.lastReasoning
				}
				snap := cliIterationSnapshot{
					Iteration:   iterNum,
					Thinking:    m.progressState.current.Thinking,
					Reasoning:   reasoning,
					Tools:       finalTools,
					ElapsedWall: time.Since(m.progressState.iterStart).Milliseconds(),
				}
				if len(snap.Tools) > 0 || snap.Thinking != "" || snap.Reasoning != "" {
					bakeIterations = append(bakeIterations, snap)
				}
			}
		}

		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) &&
			m.messages[m.streamingMsgIdx].turnID == turnID {
			// 更新流式消息为完整消息 (turnID 校验：防止跨 turn 覆盖)
			m.messages[m.streamingMsgIdx].content = content
			m.messages[m.streamingMsgIdx].isPartial = false
			m.messages[m.streamingMsgIdx].dirty = true
			m.messages[m.streamingMsgIdx].turnID = turnID
			m.messages[m.streamingMsgIdx].iterations = bakeIterations
			completedMsgIdx = m.streamingMsgIdx
		} else {
			// 新增完整的 assistant 消息 — use upsert to prevent duplicates
			assistantMsg := cliMessage{
				role:       "assistant",
				content:    content,
				timestamp:  time.Now(),
				isPartial:  false,
				dirty:      true,
				turnID:     turnID,
				iterations: bakeIterations,
			}
			completedMsgIdx = m.upsertMessageByTurn(turnID, "assistant", assistantMsg)
		}
		// Clear pendingToolSummary after consuming — prevents stale iteration
		// data from Turn N leaking into Turn N+1's bakeIterations.
		// Exception: when WaitingUser=true, the turn is paused (not ended).
		// pendingToolSummary must survive so iterations remain available when
		// the user answers and the turn resumes.
		if !msg.WaitingUser {
			m.pendingToolSummary = nil
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
		// iteration's reasoning to be misattributed to m.reasoningByIter[newIter].
		if turnID == m.agentTurnID && m.progressState.current != nil {
			reasoning := m.progressState.current.Reasoning
			if reasoning != "" {
				m.lastReasoning = reasoning
				if m.reasoningByIter == nil {
					m.reasoningByIter = make(map[int]string)
				}
				iter := m.progressState.current.Iteration
				if iter >= 0 {
					m.reasoningByIter[iter] = reasoning
				}
			}
			if m.progressState.current.Thinking != "" {
				m.lastThinking = m.progressState.current.Thinking
			}
		}
		// Store captured thinking on the completed message for Thinking Box rendering.
		if completedMsgIdx >= 0 && completedMsgIdx < len(m.messages) {
			thinking := m.lastReasoning
			if thinking == "" {
				thinking = m.lastThinking
			}
			if thinking != "" {
				m.messages[completedMsgIdx].thinking = thinking
			}
		}
		// Keep rc.valid = true to avoid fullRebuild flicker on turn completion.
		// During streaming, rc.msgCount was set to streamingMsgIdx (the streaming
		// message was excluded from cache). Now that streamingMsgIdx = -1, the
		// former streaming message appears as a "new" message to the cache.
		// The next updateViewportContent (if called) uses appendNewMessagesToCache
		// which renders ONLY this one message — not a full O(N) rebuild.
		// Even better: the tick handler's idle branch checks !rc.valid and skips
		// updateViewportContent entirely when valid, so there's zero rendering
		// overhead. The isPartial=false guide dimming is picked up lazily on the
		// next real viewport update (new turn, user input, etc.).

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
					// Persist pre-AskUser iteration history AFTER startAgentTurn.
					// startAgentTurn clears pendingToolSummary (to prevent stale
					// cross-turn data), so we must save iterationHistory AFTER
					// the clear, not before.
					savedIterHistory := append([]cliIterationSnapshot{}, m.progressState.iterations...)
					m.startAgentTurn()
					if len(savedIterHistory) > 0 {
						if m.pendingToolSummary == nil {
							m.pendingToolSummary = &cliMessage{}
						}
						m.pendingToolSummary.iterations = savedIterHistory
					}
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
		if m.progressState.lastIter >= 0 && (len(m.lastCompletedTools) > 0 || m.lastReasoning != "" || m.lastThinking != "") {
			alreadySnapped := false
			for _, s := range m.progressState.iterations {
				if s.Iteration == m.progressState.lastIter {
					alreadySnapped = true
					break
				}
			}
			if !alreadySnapped {
				// Filter tools by Iteration field to ensure correct attribution
				var finalTools []protocol.ToolProgress
				for _, t := range m.lastCompletedTools {
					if t.Iteration == m.progressState.lastIter {
						finalTools = append(finalTools, t)
					}
				}
				reasoning := m.lastReasoning
				if reasoning == "" && m.reasoningByIter != nil {
					reasoning = m.reasoningByIter[m.progressState.lastIter]
				}
				snap := cliIterationSnapshot{
					Iteration:   m.progressState.lastIter,
					Reasoning:   reasoning,
					Thinking:    m.lastThinking,
					Tools:       finalTools,
					ElapsedWall: time.Since(m.progressState.iterStart).Milliseconds(),
				}
				if len(finalTools) > 0 || reasoning != "" || m.lastThinking != "" {
					m.progressState.iterations = append(m.progressState.iterations, snap)
				}
			}
		}

		// Update assistant message iterations if we have richer local data
		// that wasn't captured at assistant message creation time (step above).
		// The assistant message already has iterations from pendingToolSummary
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
			m.setTurnReplyReceived(turnID)
			m.endAgentTurn(turnID)
			if turnID == m.agentTurnID {
				m.inputReady = true
				// §Q 标记需要刷新消息队列（由 Update 循环检查）
				if len(m.messageQueue) > 0 {
					m.needFlushQueue = true
				}
			}
		}

	}

	m.updateViewportContent()
}

func (m *cliModel) cancelledTurnIterations() []cliIterationSnapshot {
	var iterations []cliIterationSnapshot
	if m.pendingToolSummary != nil && len(m.pendingToolSummary.iterations) > 0 {
		iterations = append(iterations, m.pendingToolSummary.iterations...)
	} else if len(m.progressState.iterations) > 0 {
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
	if reasoning == "" && m.reasoningByIter != nil {
		reasoning = m.reasoningByIter[iterNum]
	}
	if reasoning == "" {
		reasoning = m.lastReasoning
	}
	// Capture streamed reasoning as fallback (LLM was still streaming when
	// Ctrl+C interrupted). m.progressState.current is the live progress the
	// user was watching — ReasoningStreamContent is what they saw on screen.
	if reasoning == "" && m.progressState.current.ReasoningStreamContent != "" {
		reasoning = m.progressState.current.ReasoningStreamContent
	}
	// Capture streamed content as fallback when structured Thinking is empty.
	// This preserves partial LLM output that was streamed but not yet finalized
	// by recordAssistantMsg when Ctrl+C interrupted.
	content := m.progressState.current.Thinking
	if content == "" && m.progressState.current.StreamContent != "" {
		content = m.progressState.current.StreamContent
	}
	snap := cliIterationSnapshot{
		Iteration:   iterNum,
		Thinking:    content,
		Reasoning:   reasoning,
		Tools:       tools,
		ElapsedWall: time.Since(m.progressState.iterStart).Milliseconds(),
	}
	if snap.Thinking != "" || snap.Reasoning != "" || len(snap.Tools) > 0 {
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

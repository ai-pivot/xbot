package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	log "xbot/logger"
	"xbot/protocol"
)

func (m *cliModel) handleAgentMessage(msg OutboundMsg) {
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
	if m.suLoading {
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
		log.WithFields(log.Fields{
			"msg_channel":    msg.Channel,
			"msg_chatid":     msg.ChatID,
			"my_channelName": m.channelName,
			"my_chatid":      m.chatID,
			"waiting_user":   msg.WaitingUser,
			"content_len":    len(msg.Content),
		}).Warn("handleAgentMessage: ChatID empty — filter bypassed, applying to current session")
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
		if cancelledIdx < 0 && m.streamingMsgIdx >= 0 {
			if m.messages[m.streamingMsgIdx].turnID == m.agentTurnID {
				cancelledIdx = m.streamingMsgIdx
			}
		}

		if cancelledIdx >= 0 {
			streamingMsg := &m.messages[cancelledIdx]
			streamingMsg.isPartial = false
			streamingMsg.dirty = true
			// Preserve any accumulated content from IsPartial updates.
			// The cancel outbound has empty Content, but the streaming message
			// may have accumulated text via IsPartial before the cancel.
			// Do NOT overwrite existing content — keep what was streamed.
			if m.pendingToolSummary != nil && len(m.pendingToolSummary.iterations) > 0 {
				streamingMsg.iterations = m.pendingToolSummary.iterations
			} else if len(m.iterationHistory) > 0 {
				streamingMsg.iterations = append([]cliIterationSnapshot{}, m.iterationHistory...)
			}
		}
		// Still clean up progress/streaming state for the cancelled turn.
		// Do NOT endAgentTurn — the current turn (if any) must remain active.
		if m.progress != nil {
			m.cacheTokenUsage(m.progress.TokenUsage)
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
		m.streamingMsgIdx = -1
		m.progress = nil
		m.typing = false // clear typing indicator immediately after cancel
		m.cancelTargetTurnID = 0
		m.rc.valid = false
		m.updateViewportContent()
		return
	}

	// 处理 __FEISHU_CARD__ 协议（简化显示）
	if strings.HasPrefix(content, "__FEISHU_CARD__") {
		content = ConvertFeishuCard(content)
	}

	// Empty content with no waiting user: end turn and flush queue,
	// but don't append a blank message.
	// Guard: when AskUser panel is open, the turn is paused (not ended).
	// A late-arriving empty-content outbound (e.g. from engine cleanup) must
	// not trigger endAgentTurn, which clears iterationHistory and makes all
	// previous iterations disappear from the viewport.
	if content == "" && !msg.WaitingUser && len(msg.ToolsUsed) == 0 && m.panelMode != "askuser" {
		// Persist token usage before clearing progress
		if m.progress != nil {
			m.cacheTokenUsage(m.progress.TokenUsage)
		}
		m.streamingMsgIdx = -1
		m.progress = nil
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
		} else if len(m.iterationHistory) > 0 {
			bakeIterations = append([]cliIterationSnapshot{}, m.iterationHistory...)
		}
		if len(bakeIterations) == 0 && m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			bakeIterations = m.messages[m.streamingMsgIdx].iterations
		}

		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			// 更新流式消息为完整消息
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
		// 重置流式状态
		m.streamingMsgIdx = -1
		// Capture reasoning from progress before it might be cleared.
		// Do NOT clear m.progress here — progress is only cleared by endAgentTurn.
		// Intermediate text messages (e.g. thinking content) arrive while the agent
		// is still running; clearing progress here would hide the progress panel
		// and make it look like the turn ended prematurely.
		// IMPORTANT: Do NOT fallback to m.progress.ReasoningStreamContent.
		// ReasoningStreamContent is a streaming accumulator with no per-iteration
		// boundary. When handleAgentMessage arrives after the next structured
		// progress has advanced m.progress.Iteration, ReasoningStreamContent may
		// still contain the previous iteration's content — causing the previous
		// iteration's reasoning to be misattributed to m.reasoningByIter[newIter].
		if turnID == m.agentTurnID && m.progress != nil {
			reasoning := m.progress.Reasoning
			if reasoning != "" {
				m.lastReasoning = reasoning
				if m.reasoningByIter == nil {
					m.reasoningByIter = make(map[int]string)
				}
				iter := m.progress.Iteration
				if iter >= 0 {
					m.reasoningByIter[iter] = reasoning
				}
			}
			if m.progress.Thinking != "" {
				m.lastThinking = m.progress.Thinking
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
		m.rc.valid = false
		m.updateViewportContent()

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
					// Persist pre-AskUser iteration history before startAgentTurn clears it.
					// Without this, iterations 1..N from the first run disappear when
					// resetProgressState sets m.iterationHistory = nil.
					if len(m.iterationHistory) > 0 {
						// Store iterations in pendingToolSummary (same pattern as handleProgressDone)
						if m.pendingToolSummary == nil {
							m.pendingToolSummary = &cliMessage{}
						}
						m.pendingToolSummary.iterations = append([]cliIterationSnapshot{}, m.iterationHistory...)
					}
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
		if m.lastSeenIteration >= 0 && (len(m.lastCompletedTools) > 0 || m.lastReasoning != "" || m.lastThinking != "") {
			alreadySnapped := false
			for _, s := range m.iterationHistory {
				if s.Iteration == m.lastSeenIteration {
					alreadySnapped = true
					break
				}
			}
			if !alreadySnapped {
				// Filter tools by Iteration field to ensure correct attribution
				var finalTools []protocol.ToolProgress
				for _, t := range m.lastCompletedTools {
					if t.Iteration == m.lastSeenIteration {
						finalTools = append(finalTools, t)
					}
				}
				reasoning := m.lastReasoning
				if reasoning == "" && m.reasoningByIter != nil {
					reasoning = m.reasoningByIter[m.lastSeenIteration]
				}
				snap := cliIterationSnapshot{
					Iteration:   m.lastSeenIteration,
					Reasoning:   reasoning,
					Thinking:    m.lastThinking,
					Tools:       finalTools,
					ElapsedWall: time.Since(m.iterationStartTime).Milliseconds(),
				}
				if len(finalTools) > 0 || reasoning != "" || m.lastThinking != "" {
					m.iterationHistory = append(m.iterationHistory, snap)
				}
			}
		}

		// Update assistant message iterations if we have richer local data
		// that wasn't captured at assistant message creation time (step above).
		// The assistant message already has iterations from pendingToolSummary
		// (if PhaseDone arrived first) or from iterationHistory (if not).
		// The final snapshot just above may have added more iterations.
		if len(m.iterationHistory) > 0 {
			asstIdx := m.findMessageByTurn(turnID, "assistant")
			if asstIdx >= 0 {
				existing := m.messages[asstIdx]
				existingIters := make(map[int]bool)
				for _, it := range existing.iterations {
					existingIters[it.Iteration] = true
				}
				for _, it := range m.iterationHistory {
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

// renderProgressBlock renders the iteration progress panel for the viewport.
// The output is cached via fingerprint — the expensive blockStyle.Render()
// (lipgloss border wrapping with ANSI width calculation on every character)
// renderHistoryRange renders a slice of iteration snapshots into buf.
// Extracted from renderProgressBlock to enable incremental rebuild — when only

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"xbot/bus"
	"xbot/channel"
	"xbot/llm"
	log "xbot/logger"
	"xbot/protocol"
	"xbot/session"
	"xbot/tools"
)

// injectSystemNotes appends runtime state notes (background tasks, interactive
// agents, active groups) to the last user message in the slice. The message
// struct is copied to avoid mutating session data.
func (a *Agent) injectSystemNotes(messages []llm.ChatMessage, channel, chatID string) []llm.ChatMessage {
	var systemNotes []string

	// Background tasks
	if a.bgTaskMgr != nil {
		sessionKey := qualifyChatID(channel, chatID)
		running := a.bgTaskMgr.ListRunning(sessionKey)
		if len(running) > 0 {
			var ids []string
			for _, t := range running {
				ids = append(ids, t.ID)
			}
			systemNotes = append(systemNotes, fmt.Sprintf("Running background tasks: %s", strings.Join(ids, ", ")))
		}
	}

	// Interactive agent sessions
	sessions := a.ListInteractiveSessions(channel, chatID)
	if len(sessions) > 0 {
		var agentParts []string
		for _, s := range sessions {
			status := "idle"
			if s.Running {
				status = "running"
			}
			mode := "fg"
			if s.Background {
				mode = "bg"
			}
			agentParts = append(agentParts, fmt.Sprintf("%s/%s(%s,%s)", s.Role, s.Instance, mode, status))
		}
		systemNotes = append(systemNotes, fmt.Sprintf("Active interactive agents: %s", strings.Join(agentParts, ", ")))
	}

	// Active group chats
	groups := tools.ListGroups()
	if len(groups) > 0 {
		var groupParts []string
		for _, g := range groups {
			status := "open"
			if g.Closed {
				status = "closed"
			}
			members := strings.Join(g.Members, ",")
			groupParts = append(groupParts, fmt.Sprintf("%s(%s, %d members: %s)", g.Name, status, len(g.Members), members))
		}
		systemNotes = append(systemNotes, fmt.Sprintf("Groups: %s", strings.Join(groupParts, "; ")))
	}

	if len(systemNotes) > 0 {
		info := "\n[System] " + strings.Join(systemNotes, " | ")
		// Append to a copy of the last user message to avoid mutating session data
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				m := messages[i] // shallow copy
				m.Content += info
				messages[i] = m
				break
			}
		}
	}
	return messages
}

// wireBgNotificationDrain creates a DrainBgNotifications callback for Run()
// that returns only notifications matching the given session key, putting
// others back into the pending list to prevent cross-session contamination.
func (a *Agent) wireBgNotificationDrain(sessionKey string) func() []tools.BgNotification {
	return func() []tools.BgNotification {
		a.bgRunPendingMu.Lock()
		pending := a.bgRunPending
		a.bgRunPending = nil
		a.bgRunPendingMu.Unlock()

		var mine []tools.BgNotification
		var others []tools.BgNotification
		for _, n := range pending {
			if n.SessionKey() == sessionKey {
				mine = append(mine, n)
			} else {
				others = append(others, n)
			}
		}
		// Put other sessions' notifications back
		if len(others) > 0 {
			a.bgRunPendingMu.Lock()
			a.bgRunPending = append(a.bgRunPending, others...)
			a.bgRunPendingMu.Unlock()
		}
		// Track drained notifications so cancel can discard them explicitly.
		// If the Run is cancelled after draining, these notifications were
		// consumed from bgRunPending and should not be delivered as a fresh
		// user message after Ctrl+C.
		if len(mine) > 0 {
			if state, ok := a.bgSessionStates.Load(sessionKey); ok {
				ss := state.(*bgSessionState)
				ss.drainedThisRunMu.Lock()
				ss.drainedThisRun = append(ss.drainedThisRun, mine...)
				ss.drainedThisRunMu.Unlock()
			}
		}
		return mine
	}
}

// drainAndProcessNotifications drains bg notifications for the given session
// from bgRunPending and processes them via processBgNotification/processSubAgentBgNotification.
// Called by chatProcessLoop after each turn completes (response sent), and by
// chatWorker when idle. Safe for concurrent use — bgRunPendingMu serializes access.
//
// Batching: ALL drained notifications are merged into a SINGLE user message
// (joined by separators). This avoids spamming the TUI with N separate messages
// and triggering N separate agent turns when multiple bg tasks complete at once.
func (a *Agent) drainAndProcessNotifications(sessionKey string) {
	a.bgRunPendingMu.Lock()
	pending := a.bgRunPending
	a.bgRunPending = nil
	a.bgRunPendingMu.Unlock()

	var mine, others []tools.BgNotification
	for _, n := range pending {
		if n.SessionKey() == sessionKey {
			mine = append(mine, n)
		} else {
			others = append(others, n)
		}
	}
	// Put other sessions' notifications back
	if len(others) > 0 {
		a.bgRunPendingMu.Lock()
		a.bgRunPending = append(a.bgRunPending, others...)
		a.bgRunPendingMu.Unlock()
	}
	if len(mine) == 0 {
		return
	}

	parts := strings.SplitN(sessionKey, ":", 2)
	if len(parts) != 2 {
		log.WithField("session_key", sessionKey).Warn("drainAndProcessNotifications: invalid session key")
		return
	}
	channelName, chatID := parts[0], parts[1]

	// Format all notifications into content strings, collect senderID
	var contents []string
	senderID := ""
	for _, notif := range mine {
		var content string
		switch n := notif.(type) {
		case *tools.BackgroundTask:
			// Offload large output per-task
			outputOverride := ""
			if a.offloadStore != nil && n.Output != "" {
				offloadCtx := context.Background()
				if offloaded, ok := a.offloadStore.MaybeOffload(offloadCtx, sessionKey,
					"background_task_result", n.Command, n.Output,
					"", "", ""); ok {
					outputOverride = offloaded.Summary
				}
			}
			content = tools.FormatBgTaskCompletion(n, outputOverride)
			if senderID == "" {
				senderID = n.SenderID()
			}
		case *tools.SubAgentBgNotify:
			if n.Type != tools.SubAgentBgNotifyCompleted {
				continue // drop progress during idle
			}
			content = tools.FormatSubAgentBgNotify(n)
			if senderID == "" {
				senderID = n.SenderID()
			}
		case *tools.CronFired:
			content = fmt.Sprintf("⏰ [定时任务触发] %s", n.Message)
			if senderID == "" {
				senderID = n.SenderID()
			}
		case *tools.AsyncMessageNotification:
			content = n.Content
			if senderID == "" {
				senderID = n.SenderID()
			}
		default:
			continue
		}
		if content != "" {
			contents = append(contents, content)
		}
	}

	if len(contents) == 0 {
		return
	}

	// Merge into a single message
	combined := strings.Join(contents, "\n\n---\n\n")

	log.WithFields(log.Fields{
		"channel":     channelName,
		"chat_id":     chatID,
		"notif_count": len(contents),
	}).Info("Bg notifications: injecting as batched user message")

	a.injectBgUserMessage(channelName, chatID, senderID, combined)
}

func (a *Agent) discardPendingBgNotifications(sessionKey string) int {
	a.bgRunPendingMu.Lock()
	defer a.bgRunPendingMu.Unlock()

	pending := a.bgRunPending
	a.bgRunPending = nil
	discarded := 0
	for _, n := range pending {
		if n.SessionKey() == sessionKey {
			discarded++
			continue
		}
		a.bgRunPending = append(a.bgRunPending, n)
	}
	return discarded
}

// handleCancelledRun persists un-saved engine messages and iteration history
// when a Run is cancelled, then returns a minimal OutboundMessage so the
// channel knows processing ended.
func (a *Agent) handleCancelledRun(ctx context.Context, msg bus.InboundMessage, out *RunOutput, tenantSession *session.TenantSession) (*channel.OutboundMsg, error) {
	// Discard notifications for this session when a Run is cancelled.
	// Ctrl+C means stop this work, so neither notifications already injected
	// into the cancelled Run nor notifications still pending for this session
	// should start a fresh turn after the cancel ack.
	sessionKey := qualifyChatID(msg.Channel, msg.ChatID)
	discardedPending := a.discardPendingBgNotifications(sessionKey)
	discardedDrained := 0
	if state, ok := a.bgSessionStates.Load(sessionKey); ok {
		ss := state.(*bgSessionState)
		ss.drainedThisRunMu.Lock()
		discardedDrained = len(ss.drainedThisRun)
		ss.drainedThisRun = nil
		ss.drainedThisRunMu.Unlock()
	}
	if discardedPending+discardedDrained > 0 {
		log.Ctx(ctx).WithFields(log.Fields{
			"pending": discardedPending,
			"drained": discardedDrained,
		}).Info("Discarded background notifications after cancel (user requested stop)")
	}

	// Save any un-persisted engine messages from the interrupted iteration.
	for _, em := range out.EngineMessages {
		if err := assertNoSystemPersist(em); err != nil {
			continue
		}
		if err := tenantSession.AddMessage(em); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to save engine message on cancel")
		}
	}
	if len(out.EngineMessages) > 0 {
		log.Ctx(ctx).Infof("Cancelled: persisted %d un-persisted engine messages", len(out.EngineMessages))
	}
	// Save iteration history as an assistant message with detail,
	// so web UI can restore it on page refresh without showing "loading".
	// Serialize iteration history once and reuse to avoid duplicate JSON marshal
	var iterationHistoryJSON string
	iterHistory := out.IterationHistory

	// Fallback: when Run() returned before creating iteration snapshots
	// (e.g. ctx cancelled mid-tool-call), use in-memory iteration histories
	// accumulated by recordIterationSnapshot during progress handling.
	if len(iterHistory) == 0 {
		key := qualifyChatID(msg.Channel, msg.ChatID)
		if histPtr, ok := a.iterationHistories.Load(key); ok {
			hist := histPtr.(*[]protocol.ProgressEvent)
			if len(*hist) > 0 {
				iterHistory = make([]IterationSnapshot, len(*hist))
				for i, ev := range *hist {
					iterHistory[i] = IterationSnapshot{
						Iteration: ev.Iteration,
						Content:   ev.Content,
						Reasoning: ev.Reasoning,
					}
					for _, t := range ev.CompletedTools {
						iterHistory[i].Tools = append(iterHistory[i].Tools, IterationToolSnapshot{
							Name:      t.Name,
							Label:     t.Label,
							Status:    t.Status,
							ElapsedMS: t.Elapsed,
							Summary:   t.Summary,
						})
					}
					for _, t := range ev.ActiveTools {
						iterHistory[i].Tools = append(iterHistory[i].Tools, IterationToolSnapshot{
							Name:      t.Name,
							Label:     t.Label,
							Status:    t.Status,
							ElapsedMS: t.Elapsed,
							Summary:   t.Summary,
						})
					}
				}
			}
		}
	}

	if len(iterHistory) > 0 {
		if jsonBytes, err := json.Marshal(iterHistory); err == nil {
			iterationHistoryJSON = string(jsonBytes)
		}
	}
	if len(iterHistory) > 0 {
		cancelMsg := llm.NewAssistantMessage("[interrupted]")
		cancelMsg.DisplayOnly = true
		if iterationHistoryJSON != "" {
			cancelMsg.Detail = iterationHistoryJSON
		}
		if err := tenantSession.AddMessage(cancelMsg); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to save cancelled iteration history")
		}
	}
	// Send a minimal outbound so the web channel knows processing ended.
	meta := map[string]string{"cancelled": "true"}
	if len(iterHistory) > 0 {
		if iterationHistoryJSON != "" {
			meta["progress_history"] = iterationHistoryJSON
		}
	}
	return &channel.OutboundMsg{
		Channel:  msg.Channel,
		ChatID:   msg.ChatID,
		Content:  "",
		Metadata: meta,
	}, nil
}

// handleRunOutput processes the successful result of a Run() call:
// - WaitingUser: send WaitingUser outbound
// - Empty content with mandatory reply: send warning
// buildWaitingUserOutbound constructs the WaitingUser OutboundMsg from a RunOutput.
// Shared by handleRunOutput (main message path) and card_handler.go (card action path).
func buildWaitingUserOutbound(ctx context.Context, msg bus.InboundMessage, out *RunOutput, tenantSession *session.TenantSession) *channel.OutboundMsg {
	log.Ctx(ctx).Info("Tool is waiting for user response, sending WaitingUser outbound")
	meta := map[string]string{}
	for k, v := range out.Metadata {
		meta[k] = v
	}
	// Persist iteration history to session so it survives restarts.
	if len(out.IterationHistory) > 0 {
		if jsonBytes, err := json.Marshal(out.IterationHistory); err == nil {
			histMsg := llm.NewAssistantMessage("")
			histMsg.DisplayOnly = true
			histMsg.Detail = string(jsonBytes)
			if err := tenantSession.AddMessage(histMsg); err != nil {
				log.Ctx(ctx).WithError(err).Warn("Failed to save waitingUser iteration history")
			}
			meta["progress_history"] = string(jsonBytes)
		}
	}
	return &channel.OutboundMsg{
		Channel:     msg.Channel,
		ChatID:      msg.ChatID,
		Content:     out.Content,
		WaitingUser: true,
		Metadata:    meta,
	}
}

// - Empty content with optional reply: clear progress state
// - Normal: persist assistant message, send, add reaction
func (a *Agent) handleRunOutput(ctx context.Context, msg bus.InboundMessage, out *RunOutput, tenantSession *session.TenantSession, replyPolicy string) (*channel.OutboundMsg, error) {
	finalContent := out.Content
	waitingUser := out.WaitingUser

	// If a tool is waiting for user response, send WaitingUser outbound
	if waitingUser {
		return buildWaitingUserOutbound(ctx, msg, out, tenantSession), nil
	}

	// Empty content without waiting for user and not optional reply
	if finalContent == "" && replyPolicy != bus.ReplyPolicyOptional {
		log.Ctx(ctx).Warn("Run produced empty content without waiting for user input")
		if err := a.sendMessage(msg.Channel, msg.ChatID, "⚠️ 处理完成，但未生成回复内容。请尝试重新描述您的需求。"); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to send empty content notification")
		}
		return nil, nil
	}

	if finalContent == "" && replyPolicy == bus.ReplyPolicyOptional {
		log.Ctx(ctx).WithFields(log.Fields{
			"channel":      msg.Channel,
			"chat_id":      msg.ChatID,
			"reply_policy": replyPolicy,
		}).Info("Optional reply policy: no final response generated, skipping outbound")
		// Send an empty outbound to clear TUI progress state.
		if ch, ok := a.channelFinder(msg.Channel); ok {
			ch.Send(channel.OutboundMsg{
				Channel: msg.Channel,
				ChatID:  msg.ChatID,
				Content: "",
			})
		}
		return nil, nil
	}

	// Persist the final assistant reply
	assistantMsg := llm.NewAssistantMessage(finalContent)
	assistantMsg.ReasoningContent = out.ReasoningContent
	if len(out.IterationHistory) > 0 {
		if jsonBytes, err := json.Marshal(out.IterationHistory); err == nil {
			assistantMsg.Detail = string(jsonBytes)
		}
	}
	if err := tenantSession.AddMessage(assistantMsg); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to save assistant message")
	}

	// Send via sendMessage (reuses session message tracking)
	sendMeta := map[string]string{}
	if assistantMsg.Detail != "" {
		sendMeta["progress_history"] = assistantMsg.Detail
	}
	if err := a.sendMessage(msg.Channel, msg.ChatID, finalContent, sendMeta); err != nil {
		log.Ctx(ctx).WithError(err).Error("Failed to send final response via sendMessage")
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: finalContent,
		}, nil
	}

	// Add reaction to user's original message
	a.addReaction(msg)

	return nil, nil
}

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

	"github.com/google/uuid"
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

func (a *Agent) enqueueBgNotification(notif tools.BgNotification) {
	sessionKey := notif.SessionKey()
	a.bgRunPendingMu.Lock()
	defer a.bgRunPendingMu.Unlock()
	if a.bgRunPending == nil {
		a.bgRunPending = make(map[string][]tools.BgNotification)
	}
	a.bgRunPending[sessionKey] = append(a.bgRunPending[sessionKey], notif)
}

func (a *Agent) enqueueBgNotifications(notifs []tools.BgNotification) {
	if len(notifs) == 0 {
		return
	}
	a.bgRunPendingMu.Lock()
	defer a.bgRunPendingMu.Unlock()
	if a.bgRunPending == nil {
		a.bgRunPending = make(map[string][]tools.BgNotification)
	}
	for _, notif := range notifs {
		sessionKey := notif.SessionKey()
		a.bgRunPending[sessionKey] = append(a.bgRunPending[sessionKey], notif)
	}
}

func (a *Agent) takePendingBgNotifications(sessionKey string) []tools.BgNotification {
	a.bgRunPendingMu.Lock()
	defer a.bgRunPendingMu.Unlock()
	if len(a.bgRunPending) == 0 {
		return nil
	}
	pending := a.bgRunPending[sessionKey]
	delete(a.bgRunPending, sessionKey)
	return pending
}

func (a *Agent) pendingBgNotifications(sessionKey string) []tools.BgNotification {
	a.bgRunPendingMu.Lock()
	defer a.bgRunPendingMu.Unlock()
	pending := a.bgRunPending[sessionKey]
	if len(pending) == 0 {
		return nil
	}
	out := make([]tools.BgNotification, len(pending))
	copy(out, pending)
	return out
}

func (a *Agent) acknowledgePendingBgNotifications(sessionKey string, count int) {
	if count <= 0 {
		return
	}
	a.bgRunPendingMu.Lock()
	defer a.bgRunPendingMu.Unlock()
	pending := a.bgRunPending[sessionKey]
	if count >= len(pending) {
		delete(a.bgRunPending, sessionKey)
		return
	}
	a.bgRunPending[sessionKey] = append([]tools.BgNotification(nil), pending[count:]...)
}

func backgroundNotificationSyntheticTool(notif tools.BgNotification, seq int) (llm.ChatMessage, llm.ChatMessage, IterationToolSnapshot, bool) {
	toolName := ""
	toolID := ""
	assistantContent := ""
	toolContent := ""
	label := ""
	var elapsedMS int64

	switch n := notif.(type) {
	case *tools.BackgroundTask:
		toolName = "background_task_result"
		toolID = "bg_" + n.ID
		assistantContent = "A background task completed while this run was being cancelled. I will record the result."
		toolContent = tools.FormatBgTaskCompletion(n, "")
		label = fmt.Sprintf("bg:%s", n.ID)
		if n.FinishedAt != nil {
			elapsedMS = n.FinishedAt.Sub(n.StartedAt).Milliseconds()
		}
	case *tools.SubAgentBgNotify:
		if n.Type != tools.SubAgentBgNotifyCompleted {
			return llm.ChatMessage{}, llm.ChatMessage{}, IterationToolSnapshot{}, false
		}
		toolName = "bg_subagent_" + string(n.Type)
		toolID = fmt.Sprintf("bgsub_%s_%s_%d", n.Role, n.Instance, seq)
		assistantContent = fmt.Sprintf("Background subagent %s completed while this run was being cancelled. I will record the result.", n.Role)
		toolContent = tools.FormatSubAgentBgNotify(n)
		label = fmt.Sprintf("bgsub:%s/%s", n.Role, n.Instance)
	case *tools.CronFired:
		toolName = "cron_fired"
		toolID = fmt.Sprintf("cron_cancel_%d", seq)
		assistantContent = "A scheduled cron job fired while this run was being cancelled. I will record it."
		toolContent = fmt.Sprintf("A scheduled cron job fired.\n\nMessage: %s", n.Message)
		label = "cron"
	case *tools.AsyncMessageNotification:
		toolName = "async_message"
		toolID = fmt.Sprintf("async_cancel_%d", seq)
		assistantContent = "An asynchronous message arrived while this run was being cancelled. I will record it."
		toolContent = n.Content
		label = "async_message"
	default:
		return llm.ChatMessage{}, llm.ChatMessage{}, IterationToolSnapshot{}, false
	}

	assistantMsg := llm.NewAssistantMessage(assistantContent)
	assistantMsg.ToolCalls = []llm.ToolCall{{
		ID:        toolID,
		Name:      toolName,
		Arguments: "{}",
	}}
	toolMsg := llm.NewToolMessage(toolName, toolID, "{}", toolContent)
	snapshot := IterationToolSnapshot{
		Name:      toolName,
		Label:     label,
		Status:    string(ToolDone),
		ElapsedMS: elapsedMS,
		Summary:   toolContent,
	}
	return assistantMsg, toolMsg, snapshot, true
}

func userCancelledSyntheticTool() (llm.ChatMessage, llm.ChatMessage, IterationToolSnapshot) {
	const toolName = "user_cancelled"
	const toolID = "user_cancelled"
	const content = "User cancelled this run with Ctrl+C. Treat the previous turn as interrupted. Do not continue unfinished actions unless the user asks to resume."

	assistantMsg := llm.NewAssistantMessage("The user cancelled this run. I will record the interruption.")
	assistantMsg.ToolCalls = []llm.ToolCall{{
		ID:        toolID,
		Name:      toolName,
		Arguments: "{}",
	}}
	toolMsg := llm.NewToolMessage(toolName, toolID, "{}", content)
	snapshot := IterationToolSnapshot{
		Name:    toolName,
		Label:   "cancelled by user",
		Status:  string(ToolDone),
		Summary: content,
	}
	return assistantMsg, toolMsg, snapshot
}

// wireBgNotificationDrain creates a DrainBgNotifications callback for Run()
// that returns only notifications matching the given session key.
func (a *Agent) wireBgNotificationDrain(sessionKey string) func() []tools.BgNotification {
	return func() []tools.BgNotification {
		mine := a.takePendingBgNotifications(sessionKey)
		// Track drained notifications so cancel can persist them explicitly. If the
		// Run is cancelled after draining, these notifications were consumed from
		// bgRunPending and must be recorded in the interrupted turn instead of
		// delivered as a fresh user message after Ctrl+C.
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

func (a *Agent) wireBgNotificationAcknowledge(sessionKey string) func(int) {
	return func(count int) {
		if state, ok := a.bgSessionStates.Load(sessionKey); ok {
			state.(*bgSessionState).acknowledgeDrainedThisRun(count)
		}
	}
}

func (a *Agent) requeueDrainedBgNotifications(sessionKey string) {
	state, ok := a.bgSessionStates.Load(sessionKey)
	if !ok {
		return
	}
	drained := state.(*bgSessionState).takeDrainedThisRun()
	if len(drained) == 0 {
		return
	}

	a.bgRunPendingMu.Lock()
	a.bgRunPending[sessionKey] = append(drained, a.bgRunPending[sessionKey]...)
	a.bgRunPendingMu.Unlock()
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
	mine := a.takePendingBgNotifications(sessionKey)
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

// handleCancelledRun persists un-saved engine messages and iteration history
// when a Run is cancelled, then returns a minimal OutboundMessage so the
// channel knows processing ended.
func (a *Agent) handleCancelledRun(ctx context.Context, msg bus.InboundMessage, out *RunOutput, tenantSession *session.TenantSession) (*channel.OutboundMsg, error) {
	// Persist pending notifications for this session into the interrupted turn.
	// Ctrl+C should not start a fresh bg-notification turn, but completed work
	// should remain visible to the next model call as tool observations.
	sessionKey := qualifyChatID(msg.Channel, msg.ChatID)
	pendingNotifications := a.pendingBgNotifications(sessionKey)
	var drainedNotifications []tools.BgNotification
	var sessionState *bgSessionState
	if state, ok := a.bgSessionStates.Load(sessionKey); ok {
		sessionState = state.(*bgSessionState)
		drainedNotifications = sessionState.snapshotDrainedThisRun()
	}
	if len(pendingNotifications)+len(drainedNotifications) > 0 {
		log.Ctx(ctx).WithFields(log.Fields{
			"pending": len(pendingNotifications),
			"drained": len(drainedNotifications),
		}).Info("Recording background notifications in cancelled turn")
	}

	notifications := make([]tools.BgNotification, 0, len(drainedNotifications)+len(pendingNotifications))
	notifications = append(notifications, drainedNotifications...)
	notifications = append(notifications, pendingNotifications...)
	batch := make([]llm.ChatMessage, 0, len(out.EngineMessages)+2*len(notifications)+3)
	// Save any un-persisted engine messages from the interrupted iteration.
	for _, em := range out.EngineMessages {
		if err := assertNoSystemPersist(em); err != nil {
			continue
		}
		batch = append(batch, em)
	}
	if len(out.EngineMessages) > 0 {
		log.Ctx(ctx).Infof("Cancelled: prepared %d un-persisted engine messages", len(out.EngineMessages))
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

	appendCancelToolSnapshot := func(snapshot IterationToolSnapshot) {
		if len(iterHistory) == 0 {
			iterHistory = []IterationSnapshot{{Iteration: 1}}
		}
		idx := len(iterHistory) - 1
		if iterHistory[idx].Iteration == 0 {
			iterHistory[idx].Iteration = idx + 1
		}
		iterHistory[idx].Tools = append(iterHistory[idx].Tools, snapshot)
	}
	appendCancelTool := func(assistantMsg, toolMsg llm.ChatMessage, snapshot IterationToolSnapshot) {
		batch = append(batch, assistantMsg, toolMsg)
		appendCancelToolSnapshot(snapshot)
	}

	for i, notif := range notifications {
		assistantMsg, toolMsg, snapshot, ok := backgroundNotificationSyntheticTool(notif, i+1)
		if !ok {
			continue
		}
		appendCancelTool(assistantMsg, toolMsg, snapshot)
	}
	cancelAssistantMsg, cancelToolMsg, cancelSnapshot := userCancelledSyntheticTool()
	appendCancelTool(cancelAssistantMsg, cancelToolMsg, cancelSnapshot)

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
		batch = append(batch, cancelMsg)
	}
	if tenantSession != nil {
		if _, err := tenantSession.AppendMessages(batch); err != nil {
			// Drained notifications are no longer reachable through bgRunPending.
			// Restore them before returning so a later turn can retry the batch.
			a.requeueDrainedBgNotifications(sessionKey)
			return nil, fmt.Errorf("append cancelled run batch: %w", err)
		}
	}
	// Pending notifications and the per-run drained ledger are acknowledgements:
	// clear them only after the interrupted turn is durably committed.
	a.acknowledgePendingBgNotifications(sessionKey, len(pendingNotifications))
	if sessionState != nil {
		sessionState.clearDrainedThisRun()
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
	if meta["request_id"] == "" {
		meta["request_id"] = uuid.NewString()
	}
	// Persist iteration history to session so it survives restarts.
	// NOT DisplayOnly — GetAllMessages filters display_only messages, which
	// would cause the iteration history to be invisible to ConvertMessagesToHistory
	// on history reload (both web and TUI). A regular empty-content assistant
	// message with Detail is handled correctly by ConvertMessagesToHistory
	// (it merges with the next content-bearing assistant message).
	if len(out.IterationHistory) > 0 {
		if jsonBytes, err := json.Marshal(out.IterationHistory); err == nil {
			histMsg := llm.NewAssistantMessage("")
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

func (a *Agent) storePendingAskUserOutbound(msg bus.InboundMessage, outbound *channel.OutboundMsg) {
	if a == nil || outbound == nil {
		return
	}
	askPayload := &protocol.ProgressEvent{}
	if outbound.Metadata != nil {
		askPayload.RequestID = outbound.Metadata["request_id"]
		if qJSON := outbound.Metadata["ask_questions"]; qJSON != "" {
			var questions []protocol.AskUserQuestion
			if json.Unmarshal([]byte(qJSON), &questions) == nil {
				askPayload.Questions = questions
			}
		}
	}
	a.setPendingAskUser(msg.Channel, msg.ChatID, askPayload)
}

// - Empty content with optional reply: clear progress state
// - Normal: persist assistant message, send, add reaction
func (a *Agent) handleRunOutput(ctx context.Context, msg bus.InboundMessage, out *RunOutput, tenantSession *session.TenantSession, replyPolicy string) (*channel.OutboundMsg, error) {
	finalContent := out.Content
	waitingUser := out.WaitingUser

	// If a tool is waiting for user response, send WaitingUser outbound
	if waitingUser {
		outbound := buildWaitingUserOutbound(ctx, msg, out, tenantSession)
		// Store the pending AskUser payload so reconnect can resend it.
		a.storePendingAskUserOutbound(msg, outbound)
		return outbound, nil
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
		return nil, fmt.Errorf("append assistant message: %w", err)
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

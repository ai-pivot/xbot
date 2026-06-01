package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"xbot/agent/hooks"
	"xbot/channel"
	"xbot/llm"
	log "xbot/logger"

	"xbot/tools"
)

// toolCallEntry tracks a single tool call within an iteration.
type toolCallEntry struct {
	iteration int // Agent loop iteration number (for debug tracing)
	index     int // Index within the LLM response's tool calls
	tc        llm.ToolCall
}

// toolExecResult holds the result of executing a single tool call.
type toolExecResult struct {
	content    string
	llmContent string
	result     *tools.ToolResult
	err        error
	elapsed    time.Duration
}

// runState holds the mutable state for a single Run() execution.
// It bundles all loop-local variables so extracted methods can share state
// without passing dozens of parameters.
type runState struct {
	// Configuration (read-only after init)
	cfg                      RunConfig
	maxIter                  int
	sessionKey               string
	offloadSessionKey        string
	toolExecutor             func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error)
	toolTimeout              time.Duration
	autoNotify               bool
	batchProgressByIteration bool
	dynamicInjector          *DynamicContextInjector

	// Messages
	messages        []llm.ChatMessage
	initialMsgCount int
	persistence     *PersistenceBridge

	// Token tracking
	tokenTracker *TokenTracker

	// Loop state
	toolsUsed          []string
	waitingUser        bool
	waitingQuestion    string
	waitingMetadata    map[string]string
	lastContent        string
	compressRetryCount int
	compressAttempts   int
	lastCompressIter   int

	// Metrics (local counters for this Run)
	localIterCount    int
	localToolCalls    int
	localLLMCalls     int
	localInputTokens  int
	localOutputTokens int
	localCachedTokens int

	// Progress
	progressLines      []string
	progressMu         sync.Mutex
	structuredProgress *StructuredProgress
	subAgentNodes      []SubAgentNode // structured SubAgent tree (updated alongside progressLines)
	iterationSnapshots []IterationSnapshot
	progressFinalizer  func()
}

// newRunState creates and initializes a runState from the given RunConfig.
func newRunState(cfg RunConfig) *runState {
	maxIter := cfg.MaxIterations
	if maxIter == 0 {
		maxIter = DefaultMaxIterations
	}

	sessionKey := cfg.SessionKey
	if sessionKey == "" && cfg.Channel != "" {
		sessionKey = cfg.Channel + ":" + cfg.ChatID
	}

	offloadSessionKey := sessionKey
	if cfg.RootSessionKey != "" {
		offloadSessionKey = cfg.RootSessionKey
	}

	toolExecutor := cfg.ToolExecutor
	if toolExecutor == nil {
		toolExecutor = defaultToolExecutor(&cfg)
	}

	// toolTimeout is kept for API compat but no longer used to wrap tool contexts.
	// Individual tools manage their own timeouts; engine only passes through the
	// parent context (which carries user cancellation via Ctrl+C).
	toolTimeout := cfg.ToolTimeout

	messages := copyMessages(cfg.Messages)
	for i := range messages {
		if messages[i].Role != "system" && strings.Contains(messages[i].Content, "<system-reminder>") {
			messages[i].Content = stripSystemReminder(messages[i].Content)
		}
	}

	// autoNotify gates progress dispatch (notifyProgress, progressLines updates).
	// Either ProgressNotifier (legacy text-based) or ProgressEventHandler (structured)
	// is sufficient to enable progress events. This decouples the two dispatch paths:
	// ProgressNotifier sends text lines to parent agent; ProgressEventHandler sends
	// structured events to channels. Neither should gate the other.
	autoNotify := cfg.ProgressNotifier != nil || cfg.ProgressEventHandler != nil
	batchProgressByIteration := cfg.Channel == "web"

	return &runState{
		cfg:                      cfg,
		maxIter:                  maxIter,
		sessionKey:               sessionKey,
		offloadSessionKey:        offloadSessionKey,
		toolExecutor:             toolExecutor,
		toolTimeout:              toolTimeout,
		autoNotify:               autoNotify,
		batchProgressByIteration: batchProgressByIteration,
		messages:                 messages,
		initialMsgCount:          len(messages),
		persistence:              NewPersistenceBridge(cfg.Session, len(messages)),
		tokenTracker:             NewTokenTracker(cfg.LastPromptTokens, cfg.LastCompletionTokens),
	}
}

// initProgress sets up structured progress tracking and the progress finalizer.
func (s *runState) initProgress() {
	if s.cfg.ProgressEventHandler != nil || s.cfg.OnIterationSnapshot != nil {
		s.structuredProgress = &StructuredProgress{
			Phase:          PhaseThinking,
			Iteration:      0,
			ActiveTools:    nil,
			CompletedTools: nil,
			CWD:            s.cfg.InitialCWD,
		}
		// Seed token usage from DB-restored values so the first progress
		// event carries real data instead of nil. Without this, the CLI
		// context bar shows nothing (or estimated values from maybeCompress)
		// until the first callLLM completes.
		if s.tokenTracker.PromptTokens() > 0 {
			s.structuredProgress.TokenUsage = &TokenUsageSnapshot{
				PromptTokens:     s.tokenTracker.PromptTokens(),
				CompletionTokens: s.tokenTracker.CompletionTokens(),
				// TotalTokens represents the current context fill (input tokens only).
				// Do NOT add CompletionTokens — those are output tokens from the
				// previous Run's API response, NOT part of the current prompt.
				TotalTokens:     s.tokenTracker.PromptTokens(),
				MaxOutputTokens: int64(s.cfg.MaxOutputTokens),
			}
		}
	}

	copyLines := func(lines []string) []string {
		cp := make([]string, len(lines))
		copy(cp, lines)
		return cp
	}

	if s.structuredProgress != nil {
		s.progressFinalizer = func() {
			if len(s.structuredProgress.ActiveTools) > 0 {
				for _, t := range s.structuredProgress.ActiveTools {
					if t.Status == ToolDone || t.Status == ToolError {
						s.structuredProgress.CompletedTools = append(s.structuredProgress.CompletedTools, t)
					}
				}
				s.structuredProgress.ActiveTools = nil
			}
			// When WaitingUser is true, the agent turn is paused (not ended).
			// Do NOT send PhaseDone — the CLI would clear all progress state
			// (iterationHistory, progress, lastCompletedTools), causing all
			// previous iterations' tools to disappear from the progress panel.
			// The tool states have been finalized above (Active→Completed),
			// which is sufficient. The next notifyProgress from a subsequent
			// callLLM will carry the correct state.
			if s.waitingUser {
				return
			}
			s.structuredProgress.Phase = PhaseDone
			if s.cfg.ProgressSeq != nil {
				s.structuredProgress.Seq = s.cfg.ProgressSeq.Add(1)
			}
			if s.autoNotify && s.cfg.ProgressEventHandler != nil {
				s.structuredProgress.SubAgents = s.subAgentNodes
				s.cfg.ProgressEventHandler(&ProgressEvent{
					Lines:      copyLines(s.progressLines),
					Structured: s.structuredProgress,
					Timestamp:  time.Now(),
				})
			}
		}
	}
}

// initDynamicInjector sets up the dynamic context injector for CWD change detection.
func (s *runState) initDynamicInjector() {
	s.dynamicInjector = NewDynamicContextInjectorWithPeers(
		func() string {
			if s.cfg.Session != nil {
				if dir := s.cfg.Session.GetCurrentDir(); dir != "" {
					return dir
				}
			}
			return s.cfg.InitialCWD
		},
		func() string {
			return buildPeerContextXML(s.cfg.WorkspaceRoot, s.sessionKey)
		},
	)
}

// tickSession advances the round counter for tool activation cleanup.
func (s *runState) tickSession() {
	if s.sessionKey != "" {
		s.cfg.Tools.TickSession(s.sessionKey)
	}
}

// cleanupTodos clears completed TODOs. Called via defer from Run().
func (s *runState) cleanupTodos() {
	if s.cfg.TodoManager != nil && s.sessionKey != "" {
		items := s.cfg.TodoManager.GetTodoItems(s.sessionKey)
		if len(items) > 0 {
			allDone := true
			for _, item := range items {
				if !item.Done {
					allDone = false
					break
				}
			}
			if allDone {
				s.cfg.TodoManager.ClearTodos(s.sessionKey)
			}
		}
	}
}

// cleanupBgTasks removes completed background tasks for the session.
// Called via defer from Run() to prevent stale completed tasks from
// accumulating indefinitely across multiple agent turns.
func (s *runState) cleanupBgTasks() {
	if s.cfg.BgTaskManager != nil && s.sessionKey != "" {
		s.cfg.BgTaskManager.RemoveCompletedTasks(s.sessionKey)
	}
}

// recordMetrics records conversation metrics. Called via defer from Run().
func (s *runState) recordMetrics() {
	GlobalMetrics.RecordConversation(s.localIterCount, s.localToolCalls, s.localLLMCalls, s.localInputTokens, s.localOutputTokens)
	if s.cfg.RecordUserTokenUsage != nil && s.cfg.SenderID != "" {
		s.cfg.RecordUserTokenUsage(s.cfg.SenderID, s.cfg.Model, s.localInputTokens, s.localOutputTokens, s.localCachedTokens, 1, s.localLLMCalls)
	}
	GlobalMetrics.ClearRecallTracking()
}

// accumulateCompressUsage adds compression LLM token usage to the local counters
// so they are included in recordMetrics (and thus /usage).
func (s *runState) accumulateCompressUsage(result *CompressResult) {
	if result == nil {
		return
	}
	s.localLLMCalls += result.LLMCalls
	s.localInputTokens += int(result.InputTokens)
	s.localOutputTokens += int(result.OutputTokens)
	s.localCachedTokens += int(result.CachedTokens)
}

// syncMessages syncs the ContextEditor reference when messages are reassigned.
func (s *runState) syncMessages(newMessages []llm.ChatMessage) []llm.ChatMessage {
	if s.cfg.ContextEditor != nil {
		s.cfg.ContextEditor.SetMessages(newMessages)
	}
	return newMessages
}

// notifyProgress sends progress notification to the configured callback.
func (s *runState) notifyProgress(extra string) {
	if !s.autoNotify {
		return
	}
	// Increment seq and assign to structuredProgress (unified entry point).
	if s.structuredProgress != nil && s.cfg.ProgressSeq != nil {
		s.structuredProgress.Seq = s.cfg.ProgressSeq.Add(1)
	}
	s.progressMu.Lock()
	lines := make([]string, len(s.progressLines))
	copy(lines, s.progressLines)
	s.progressMu.Unlock()
	if extra != "" {
		lines = append(lines, extra)
	}
	var flatLines []string
	for _, line := range lines {
		flatLines = append(flatLines, strings.Split(line, "\n")...)
	}
	var buf strings.Builder
	for i, line := range flatLines {
		if i > 0 {
			prev := flatLines[i-1]
			prevIsQuote := strings.HasPrefix(prev, "> ")
			currIsQuote := strings.HasPrefix(line, "> ")
			if prevIsQuote != currIsQuote {
				buf.WriteByte('\n')
			}
		}
		buf.WriteString(line)
		if i < len(flatLines)-1 {
			buf.WriteByte('\n')
		}
	}
	thinking := ""
	if s.structuredProgress != nil {
		thinking = s.structuredProgress.ThinkingContent
	}
	if s.cfg.ProgressNotifier != nil {
		s.cfg.ProgressNotifier([]string{buf.String()}, thinking)
	}
	if s.cfg.ProgressEventHandler != nil && s.structuredProgress != nil {
		s.progressMu.Lock()
		snapshot := make([]string, len(s.progressLines))
		copy(snapshot, s.progressLines)
		// Attach structured SubAgent tree (if any) directly to the event,
		// so consumers don't need to parse text lines.
		s.structuredProgress.SubAgents = s.subAgentNodes
		s.progressMu.Unlock()
		s.cfg.ProgressEventHandler(&ProgressEvent{
			Lines:      snapshot,
			Structured: s.structuredProgress,
			Timestamp:  time.Now(),
		})
	}
}

// setupRetryNotify returns a context wrapped with LLM retry notification.
func (s *runState) setupRetryNotify(ctx context.Context) context.Context {
	return llm.WithRetryNotify(ctx, func(attempt, max uint, err error) {
		if !s.autoNotify {
			return
		}
		reason := summarizeRetryError(err)
		s.progressLines = append(s.progressLines,
			fmt.Sprintf("> ⚠️ LLM 请求失败 (%s)，重试中 %d/%d ...", reason, attempt, max))
		s.notifyProgress("")
	})
}

// buildOutput creates a RunOutput from an OutboundMessage.
func (s *runState) buildOutput(ob *channel.OutboundMsg) *RunOutput {
	out := &RunOutput{OutboundMsg: ob}
	if s.cfg.Memory != nil {
		out.Messages = s.messages
	}
	if engineMsgs := s.persistence.ComputeEngineMessages(s.messages); engineMsgs != nil {
		out.EngineMessages = engineMsgs
	}
	if len(s.iterationSnapshots) > 0 {
		out.IterationHistory = s.iterationSnapshots
	}
	out.LastPromptTokens = s.tokenTracker.PromptTokens()
	out.LastCompletionTokens = s.tokenTracker.CompletionTokens()
	s.tokenTracker.SaveState(s.cfg.SaveTokenState)
	return out
}

// beginIteration updates state at the start of each loop iteration.
func (s *runState) beginIteration(i int) {
	s.localIterCount++
	s.subAgentNodes = nil
	if s.structuredProgress != nil {
		s.structuredProgress.Iteration = i
		s.structuredProgress.Phase = PhaseThinking
		s.structuredProgress.ActiveTools = nil
		s.structuredProgress.CompletedTools = nil
		s.structuredProgress.SubAgents = nil
		s.structuredProgress.ThinkingContent = ""
		s.structuredProgress.ReasoningContent = ""
	}
	if s.structuredProgress != nil && s.cfg.TodoManager != nil && s.sessionKey != "" {
		todos := s.cfg.TodoManager.GetTodoItems(s.sessionKey)
		if len(todos) > 0 {
			s.structuredProgress.Todos = make([]TodoProgressItem, len(todos))
			copy(s.structuredProgress.Todos, todos)
		} else {
			s.structuredProgress.Todos = nil
		}
	}
}

// notifyThinking sends the thinking progress notification.
func (s *runState) notifyThinking(iteration int) {
	if s.autoNotify {
		if iteration == 0 {
			s.notifyProgress("💭")
		} else {
			s.notifyProgress("> 💭 思考中...")
		}
	}
}

// assertSystemMessages checks that messages have exactly one system message.
// Returns a RunOutput error if the assertion fails, nil otherwise.
func (s *runState) assertSystemMessages(ctx context.Context) *RunOutput {
	var systemCount int
	for _, m := range s.messages {
		if m.Role == "system" {
			systemCount++
		}
	}
	if systemCount != 1 {
		log.Ctx(ctx).WithField("system_count", systemCount).Error("assert: LLM messages must have exactly one system message")
		return s.buildOutput(&channel.OutboundMsg{
			Channel: s.cfg.Channel,
			ChatID:  s.cfg.ChatID,
			Content: "内部错误：system 消息数量异常",
			Error:   fmt.Errorf("assert: LLM messages must have exactly one system message; got %d", systemCount),
		})
	}
	return nil
}

// updateTokenUsage syncs the current tokenTracker state into
// structuredProgress.TokenUsage so that progress events carry accurate token counts.
func (s *runState) updateTokenUsage() {
	if s.structuredProgress == nil {
		return
	}
	s.structuredProgress.TokenUsage = &TokenUsageSnapshot{
		PromptTokens:     s.tokenTracker.PromptTokens(),
		CompletionTokens: s.tokenTracker.CompletionTokens(),
		// TotalTokens = current context fill (prompt tokens only).
		// CompletionTokens are output tokens, not part of context.
		TotalTokens:     s.tokenTracker.PromptTokens(),
		CacheHitTokens:  int64(s.localCachedTokens),
		MaxOutputTokens: int64(s.cfg.MaxOutputTokens),
	}
}

// setTokenUsageAfterCompress updates TokenUsage with the post-compress token count
// (from the compression LLM call's API-returned prompt_tokens). After
// ResetAfterCompress(), the tracker has zero values — this ensures the CLI
// context bar shows the reduced token count immediately.
//
// It also updates the token tracker so maybeCompress can still evaluate
// the compressed count on the next iteration. Without this, ResetAfterCompress
// zeros the tracker, GetPromptTokens returns "no_data", and maybeCompress
// skips entirely — even when the compressed count is still above the threshold.
// The CLI bar shows the compressed count past the threshold marker, but
// compression never re-triggers until the next real LLM call provides data.
//
// hadLLMCall stays false so SaveState skips (buildOutput uses the tracker
// value for LastPromptTokens, but SaveState's guard prevents overwriting
// the DB with a non-API-round value). The engine already calls
// SaveTokenState(compressedCount, 0) directly after compression.
func (s *runState) setTokenUsageAfterCompress(tokenCount int64) {
	// Update token tracker so maybeCompress can evaluate post-compress state.
	// Do NOT set hadLLMCall — SaveState must skip so buildOutput's SaveState
	// doesn't overwrite the DB with a non-API-round value. The engine already
	// calls SaveTokenState(compressedCount, 0) directly after compression,
	// so the DB is already correct.
	if tokenCount > 0 && s.tokenTracker != nil {
		s.tokenTracker.SetAfterCompress(tokenCount)
	}
	if s.structuredProgress == nil {
		return
	}
	s.structuredProgress.TokenUsage = &TokenUsageSnapshot{
		PromptTokens:     tokenCount,
		CompletionTokens: 0,
		TotalTokens:      tokenCount,
		CacheHitTokens:   0,
		MaxOutputTokens:  int64(s.cfg.MaxOutputTokens),
	}
}

// callLLM invokes the LLM with the current messages, handling per-tenant
// concurrency semaphore and input-too-long errors with forced compression.
func (s *runState) callLLM(ctx context.Context, retryNotifyCtx context.Context) (*llm.LLMResponse, error) {
	toolDefs := visibleToolDefs(s.cfg.Tools.AsDefinitionsForSession(s.sessionKey, s.cfg.TenantID), s.cfg.SettingsSvc, s.cfg.Channel, s.cfg.OriginUserID)

	var releaseLLMSem func()
	if s.cfg.LLMSemAcquire != nil {
		releaseLLMSem = s.cfg.LLMSemAcquire(ctx)
	}

	response, err := generateResponse(retryNotifyCtx, s.cfg.LLMClient, s.cfg.Model, s.messages, toolDefs, s.cfg.ThinkingMode, s.cfg.Stream, s.cfg.StreamContentFunc, s.cfg.StreamReasoningFunc)

	s.localLLMCalls++
	if response != nil {
		s.tokenTracker.RecordLLMCall(response.Usage.PromptTokens, response.Usage.CompletionTokens)
		s.localInputTokens += int(response.Usage.PromptTokens)
		s.localOutputTokens += int(response.Usage.CompletionTokens)
		s.localCachedTokens += int(response.Usage.CacheHitTokens)
		s.updateTokenUsage()
		// Save exact API prompt_tokens to the most recent user message
		// so rewind can restore accurate token state from DB.
		if s.cfg.SaveContextTokens != nil {
			s.cfg.SaveContextTokens(response.Usage.PromptTokens)
		}
		// Push updated token usage to CLI immediately so the context
		// bar reflects the latest prompt token count on each iteration.
		s.notifyProgress("")
		s.validateInvariantsAt(ctx, "post_llm_call")
	}

	if err != nil && llm.IsInputTooLongError(err) && len(s.messages) > 3 {
		response, err = s.handleInputTooLong(ctx, retryNotifyCtx, toolDefs)
	}

	if releaseLLMSem != nil {
		releaseLLMSem()
	}

	return response, err
}

// handleInputTooLong forces context compression when input exceeds model limits,
// then retries the LLM call.
func (s *runState) handleInputTooLong(ctx context.Context, retryNotifyCtx context.Context, toolDefs []llm.ToolDefinition) (*llm.LLMResponse, error) {
	log.Ctx(ctx).WithError(fmt.Errorf("input too long")).Warn("Input too long for LLM, forcing context compression and retrying")
	if s.autoNotify {
		s.progressLines = append(s.progressLines, "> ⚠️ 输入超限，正在强制压缩上下文...")
		s.notifyProgress("")
	}

	cm := s.cfg.ContextManager
	if cm == nil {
		return nil, fmt.Errorf("input too long")
	}

	pipelineResult, compressErr := ApplyCompress(ctx, CompressPipelineParams{
		CM:                cm,
		Messages:          s.messages,
		LLMClient:         s.cfg.LLMClient,
		Model:             s.cfg.Model,
		UseManual:         true,
		TokenTracker:      s.tokenTracker,
		Persistence:       s.persistence,
		OffloadStore:      s.cfg.OffloadStore,
		OffloadSessionKey: s.offloadSessionKey,
		MaskStore:         s.cfg.MaskStore,
		AccumulateUsage:   s.accumulateCompressUsage,
		SyncMessages:      s.syncMessages,
	})
	if compressErr != nil {
		log.Ctx(ctx).WithError(compressErr).Warn("Forced context compression after input-too-long failed")
		return nil, compressErr
	}
	s.messages = pipelineResult.NewMessages
	// Update token estimate so CLI shows reduced context immediately
	s.setTokenUsageAfterCompress(pipelineResult.NewTokenCount)
	// Persist API-returned token count in case retry fails and Run ends.
	if s.cfg.SaveContextTokens != nil && pipelineResult.NewTokenCount > 0 {
		s.cfg.SaveContextTokens(pipelineResult.NewTokenCount)
	}
	if s.cfg.SaveTokenState != nil && pipelineResult.NewTokenCount > 0 {
		s.cfg.SaveTokenState(pipelineResult.NewTokenCount, 0)
	}
	if s.autoNotify {
		s.progressLines = append(s.progressLines, fmt.Sprintf("> ✅ 强制压缩完成 → %d tokens", pipelineResult.NewTokenCount))
		s.notifyProgress("")
	}

	response, err := generateResponse(retryNotifyCtx, s.cfg.LLMClient, s.cfg.Model, s.messages, toolDefs, s.cfg.ThinkingMode, s.cfg.Stream, s.cfg.StreamContentFunc, s.cfg.StreamReasoningFunc)
	s.localLLMCalls++
	if response != nil {
		s.tokenTracker.RecordLLMCall(response.Usage.PromptTokens, response.Usage.CompletionTokens)
		s.localInputTokens += int(response.Usage.PromptTokens)
		s.localOutputTokens += int(response.Usage.CompletionTokens)
		s.localCachedTokens += int(response.Usage.CacheHitTokens)
		s.updateTokenUsage()
		// Save exact API prompt_tokens (after compress retry, still the same user message)
		if s.cfg.SaveContextTokens != nil {
			s.cfg.SaveContextTokens(response.Usage.PromptTokens)
		}
		s.validateInvariantsAt(ctx, "post_llm_call_input_too_long")
	}
	return response, err
}

// handleLLMError handles errors from LLM calls. Returns a RunOutput if the
// error should terminate the loop, nil if no error.
// partialResp may contain content accumulated before the stream error.
func (s *runState) handleLLMError(ctx context.Context, err error, partialResp *llm.LLMResponse, iteration int) *RunOutput {
	if err == nil {
		return nil
	}
	if ctx.Err() == nil && !llm.IsInputTooLongError(err) {
		GlobalMetrics.TotalLLMErrors.Add(1)
	}
	// Emit AgentError event (notification, non-blocking)
	if s.cfg.HookManager != nil {
		s.cfg.HookManager.Emit(ctx, &hooks.AgentErrorEvent{
			BasePayload: hooks.BasePayload{
				SessionID: s.cfg.ChatID, Channel: s.cfg.Channel,
				SenderID: s.cfg.OriginUserID, ChatID: s.cfg.ChatID,
			},
			ErrorType:    "llm_error",
			ErrorMessage: err.Error(),
		})
	}
	if ctx.Err() != nil {
		return s.buildOutput(&channel.OutboundMsg{
			Channel:   s.cfg.Channel,
			ChatID:    s.cfg.ChatID,
			Content:   "Agent was cancelled.",
			Error:     ctx.Err(),
			ToolsUsed: s.toolsUsed,
		})
	}
	// Use partial response content if available (stream error with partial output),
	// otherwise fall back to lastContent from previous successful iteration.
	partialContent := ""
	if partialResp != nil {
		partialContent = llm.StripThinkBlocks(partialResp.Content)
	}
	if partialContent == "" {
		partialContent = s.lastContent
	}
	if partialContent != "" {
		log.Ctx(ctx).WithFields(log.Fields{
			"agent_id":  s.cfg.AgentID,
			"iteration": iteration + 1,
		}).Warnf("LLM failed, returning partial result: %v", err)
		return s.buildOutput(&channel.OutboundMsg{
			Channel:   s.cfg.Channel,
			ChatID:    s.cfg.ChatID,
			Content:   partialContent + "\n\n> ⚠️ LLM 调用失败 (" + summarizeRetryError(err) + ")，以上为部分结果。",
			ToolsUsed: s.toolsUsed,
		})
	}
	userErrMsg := fmt.Sprintf("❌ LLM 服务调用失败 (%s)，请稍后重试。", summarizeRetryError(err))
	return s.buildOutput(&channel.OutboundMsg{
		Channel:   s.cfg.Channel,
		ChatID:    s.cfg.ChatID,
		Content:   userErrMsg,
		Error:     fmt.Errorf("%w: %w", ErrLLMGenerate, err),
		ToolsUsed: s.toolsUsed,
	})
}

// handleFinalResponse processes LLM responses.
// Returns (output, retry): output is non-nil when Run should return it;
// retry is true when context was compressed and the loop should continue.
//
// Finish reason handling:
//   - "stop" + no tool_calls → final text response, return
//   - "tool_calls" (or HasToolCalls) → execute tools, continue loop
//   - "length" → model hit max_tokens, output is truncated. Auto-retry once
//     by appending partial content and asking model to continue. If model
//     still can't finish, return with truncation warning.
//   - "content_filter" → filtered by safety, return with warning
//   - "" (empty/unknown) → abnormal stream termination, warn and return
//   - "model_context_window_exceeded" → compress and retry
func (s *runState) handleFinalResponse(ctx context.Context, response *llm.LLMResponse) (output *RunOutput, retry bool) {
	cleanContent := llm.StripThinkBlocks(response.Content)

	if !response.HasToolCalls() {
		// context_window_exceeded: force compress and retry
		if response.FinishReason == llm.FinishReasonContextWindowExceeded {
			const maxCompressRetries = 3
			log.Ctx(ctx).WithFields(log.Fields{
				"msg_count":          len(s.messages),
				"last_prompt_tokens": s.tokenTracker.PromptTokens(),
				"finish_reason":      response.FinishReason,
				"compress_retry":     s.compressRetryCount,
			}).Warn("Model context window exceeded, forcing compression and retry")

			// Phase 1: Try LLM-based compression (up to maxCompressRetries times)
			cm := s.cfg.ContextManager
			if cm != nil && s.compressRetryCount < maxCompressRetries {
				s.compressRetryCount++
				if s.cfg.MemoryToolDefs != nil && s.cfg.MemoryToolExec != nil {
					cm.SetMemoryTools(s.cfg.MemoryToolDefs, s.cfg.MemoryToolExec)
				}
				pipelineResult, compressErr := ApplyCompress(ctx, CompressPipelineParams{
					CM:              cm,
					Messages:        s.messages,
					LLMClient:       s.cfg.LLMClient,
					Model:           s.cfg.Model,
					TokenTracker:    s.tokenTracker,
					Persistence:     s.persistence,
					AccumulateUsage: s.accumulateCompressUsage,
					SyncMessages:    s.syncMessages,
				})
				if compressErr != nil {
					log.Ctx(ctx).WithError(compressErr).Warn("Compression failed after context_window_exceeded, trying aggressive truncation")
				} else {
					s.messages = pipelineResult.NewMessages
					s.validateInvariantsAt(ctx, "post_compress_window_exceeded")
					// Update token estimate so CLI shows reduced context immediately
					s.setTokenUsageAfterCompress(pipelineResult.NewTokenCount)
					// Persist API-returned token count in case the retry also fails.
					if s.cfg.SaveContextTokens != nil && pipelineResult.NewTokenCount > 0 {
						s.cfg.SaveContextTokens(pipelineResult.NewTokenCount)
					}
					if s.cfg.SaveTokenState != nil && pipelineResult.NewTokenCount > 0 {
						s.cfg.SaveTokenState(pipelineResult.NewTokenCount, 0)
					}
					log.Ctx(ctx).WithFields(log.Fields{
						"new_msg_count": len(s.messages),
						"retry":         s.compressRetryCount,
					}).Info("Compression completed after context_window_exceeded, retrying")
					return nil, true // retry loop iteration
				}
			}

			// Phase 2: Aggressive truncation — keep system messages + last N messages
			if truncated := s.aggressiveTruncate(ctx); truncated {
				log.Ctx(ctx).WithFields(log.Fields{
					"new_msg_count": len(s.messages),
				}).Warn("Applied aggressive truncation after compression retries exhausted, retrying")
				return nil, true // retry loop iteration
			}

			// Phase 3: All recovery attempts failed
			out := s.buildOutput(&channel.OutboundMsg{
				Channel:   s.cfg.Channel,
				ChatID:    s.cfg.ChatID,
				Content:   "⚠️ Context window exceeded. Use /new to start a new conversation.",
				ToolsUsed: s.toolsUsed,
			})
			out.ReasoningContent = response.ReasoningContent
			return out, false
		}

		// length: output truncated due to max_tokens limit
		output := cleanContent
		if response.FinishReason == llm.FinishReasonLength {
			output += "\n\n⚠️ Output was truncated (reached max output token limit). Use /set-llm max_output_tokens=<n> to increase."
		}
		// content_filter: model output was filtered by safety system
		if response.FinishReason == llm.FinishReasonContentFilter {
			log.Ctx(ctx).WithFields(log.Fields{
				"finish_reason": response.FinishReason,
				"content_len":   len(cleanContent),
			}).Warn("Model response filtered by content filter")
			if output == "" {
				output = "⚠️ Response was filtered by content safety system."
			} else {
				output += "\n\n⚠️ Response was partially filtered by content safety system."
			}
		}

		// Update ThinkingContent and ReasoningContent so PhaseDone progress
		// carries the final reply and thinking. recordAssistantMsg is not called
		// for final text responses (handleFinalResponse returns directly), so
		// both fields must be set here for SubAgent session viewers and CLI
		// tool_summary rendering.
		if s.structuredProgress != nil {
			if cleanContent != "" {
				s.structuredProgress.ThinkingContent = cleanContent
			} else if response.ReasoningContent != "" {
				// Model returned only tool calls (no text) but has reasoning.
				// Use reasoning as ThinkingContent so SubAgent progress tree
				// shows what the model is thinking rather than "💭 思考中...".
				rc := response.ReasoningContent
				if r := []rune(rc); len(r) > 200 {
					rc = string(r[:200]) + "…"
				}
				s.structuredProgress.ThinkingContent = rc
			}
			if response.ReasoningContent != "" {
				s.structuredProgress.ReasoningContent = response.ReasoningContent
			}
		}

		out := s.buildOutput(&channel.OutboundMsg{
			Channel:     s.cfg.Channel,
			ChatID:      s.cfg.ChatID,
			Content:     output,
			ToolsUsed:   s.toolsUsed,
			WaitingUser: s.waitingUser,
		})
		out.ReasoningContent = response.ReasoningContent
		return out, false
	}
	return nil, false
}

// recordAssistantMsg records intermediate content and the assistant message.
func (s *runState) recordAssistantMsg(ctx context.Context, response *llm.LLMResponse) {
	cleanContent := llm.StripThinkBlocks(response.Content)

	if cleanContent != "" {
		s.lastContent = cleanContent
	}

	if s.autoNotify && cleanContent != "" {
		s.progressLines = append(s.progressLines, cleanContent)
	}
	if s.structuredProgress != nil && cleanContent != "" {
		s.structuredProgress.ThinkingContent = cleanContent
	}
	// Wire the model's reasoning chain (reasoning_content) to progress
	// so the CLI can display the thinking process to the user.
	if s.structuredProgress != nil && response.ReasoningContent != "" {
		s.structuredProgress.ReasoningContent = response.ReasoningContent
	}

	// Push progress so CLI can display reasoning immediately after LLM completes,
	// rather than waiting for the next notifyProgress call (e.g. executeToolCalls).
	if s.autoNotify {
		s.notifyProgress("")
	}

	assistantMsg := llm.ChatMessage{
		Role:             "assistant",
		Content:          strings.TrimRight(response.Content, " \t"),
		ReasoningContent: response.ReasoningContent,
		ToolCalls:        response.ToolCalls,
	}
	s.messages = s.syncMessages(append(s.messages, assistantMsg))
}

// stripRenameHints removes the auto-naming rename hint from all user messages.
// Called after detecting a config set session_name tool call — once the agent
// has renamed the session, the hint is stale and should not influence later iterations.
func (s *runState) stripRenameHints() {
	const marker = "⚠️ 当前会话名"
	for i := range s.messages {
		if s.messages[i].Role != "user" {
			continue
		}
		if !strings.Contains(s.messages[i].Content, marker) {
			continue
		}
		s.messages[i].Content = stripRenameHint(s.messages[i].Content)
	}
}

// stripRenameHint removes the rename hint block from a user message content.
// The hint is appended by UserMessageMiddleware and looks like:
//
//	\n⚠️ 当前会话名 "Agent-xxx" 是自动生成的。你必须先...
//	config(action="set", key="session_name", value="xxx")\n
func stripRenameHint(content string) string {
	const marker = "\n⚠️ 当前会话名"
	idx := strings.Index(content, marker)
	if idx < 0 {
		return content
	}
	return strings.TrimSpace(content[:idx])
}

// maybeCompress checks if context compression or observation masking is needed.
func (s *runState) maybeCompress(ctx context.Context) {
	s.compressAttempts++
	cm := s.cfg.ContextManager
	if cm == nil || len(s.messages) <= 3 {
		return
	}

	maxTokens := 0
	if s.cfg.ContextManagerConfig != nil {
		maxTokens = s.cfg.ContextManagerConfig.MaxContextTokens
	}
	if maxTokens <= 0 {
		log.Ctx(ctx).WithFields(log.Fields{
			"last_prompt_tokens": s.tokenTracker.PromptTokens(),
			"msg_count":          len(s.messages),
		}).Info("maybeCompress skipped: maxTokens=0")
		return
	}

	// Reserve headroom for max_output_tokens: the API budget is shared
	// between prompt (input) and completion (output). If we don't subtract
	// maxOutputTokens, we risk exceeding the context window when the model
	// generates a long response.
	maxOutputTokens := s.cfg.MaxOutputTokens
	if maxOutputTokens <= 0 {
		maxOutputTokens = 32_768 // sync with config.DefaultMaxOutputTokens
	}
	promptBudget := maxTokens - maxOutputTokens
	if promptBudget <= 0 {
		promptBudget = maxTokens / 2 // fallback: reserve half for output
	}

	// Use the exact API-returned prompt_tokens — no local estimation.
	// Offload handles large tool results; a single iteration cannot add enough
	// tokens to justify delta estimation.
	totalTokens, tokenSource := s.tokenTracker.GetPromptTokens()
	if tokenSource == "no_data" {
		// No API token data means we do not know the actual context pressure.
		// Do not compact or mask based on local guesses.
		return
	}

	compressThreshold := 0.9
	if s.cfg.ContextManagerConfig != nil && s.cfg.ContextManagerConfig.CompressionThreshold > 0 {
		compressThreshold = s.cfg.ContextManagerConfig.CompressionThreshold
	}
	needCompress := len(s.messages) > 3 && shouldCompact(int(totalTokens), promptBudget, compressThreshold) && (s.lastCompressIter == 0 || s.compressAttempts-s.lastCompressIter >= 5)

	log.Ctx(ctx).WithFields(log.Fields{
		"total_tokens":       totalTokens,
		"max_context":        maxTokens,
		"max_output_tokens":  maxOutputTokens,
		"prompt_budget":      promptBudget,
		"threshold":          int(float64(promptBudget) * compressThreshold),
		"msg_count":          len(s.messages),
		"need":               needCompress,
		"base_prompt_tokens": s.tokenTracker.PromptTokens(),
		"completion_tokens":  s.tokenTracker.CompletionTokens(),
		"source":             tokenSource,
	}).Info("maybeCompress check")

	if needCompress {
		// Respect ContextManager mode: mode=none means auto-compression is disabled.
		// Without this guard, maybeCompress bypasses ShouldCompress() and attempts
		// compression even when the ContextManager is a noopManager, which returns
		// "auto compression is disabled (mode=none)" error.
		if cm.Mode() == ContextModeNone {
			log.Ctx(ctx).Debug("maybeCompress: auto-compression skipped (mode=none)")
		} else {
			s.runCompression(ctx, cm, int(totalTokens), maxTokens)
		}
		return
	}

	// Observation masking (lightweight, no LLM call).
	s.maybeMaskObservations(ctx, totalTokens, maxTokens)
}

// runCompression performs the actual context compression.
func (s *runState) runCompression(ctx context.Context, cm ContextManager, totalTokens, maxTokens int) {
	if s.structuredProgress != nil {
		s.structuredProgress.Phase = PhaseCompressing
	}
	if s.autoNotify {
		s.progressLines = append(s.progressLines, fmt.Sprintf("> 📦 上下文过大 (%d tokens)，正在压缩 + 记忆整理...", totalTokens))
		s.notifyProgress("")
	}

	log.Ctx(ctx).Info("Auto context compaction triggered")

	// Emit PreCompact event (notification, non-blocking)
	if s.cfg.HookManager != nil {
		s.cfg.HookManager.Emit(ctx, &hooks.PreCompactEvent{
			BasePayload: hooks.BasePayload{
				SessionID: s.cfg.ChatID, Channel: s.cfg.Channel,
				SenderID: s.cfg.OriginUserID, ChatID: s.cfg.ChatID,
			},
			Trigger:               "token_limit",
			MessageCount:          len(s.messages),
			EstimatedTokensBefore: int64(totalTokens),
		})
	}

	if s.cfg.MemoryToolDefs != nil && s.cfg.MemoryToolExec != nil {
		cm.SetMemoryTools(s.cfg.MemoryToolDefs, s.cfg.MemoryToolExec)
	}

	pipelineResult, compressErr := ApplyCompress(ctx, CompressPipelineParams{
		CM:                cm,
		Messages:          s.messages,
		LLMClient:         s.cfg.LLMClient,
		Model:             s.cfg.Model,
		TokenTracker:      s.tokenTracker,
		Persistence:       s.persistence,
		OffloadStore:      s.cfg.OffloadStore,
		OffloadSessionKey: s.offloadSessionKey,
		MaskStore:         s.cfg.MaskStore,
		AccumulateUsage:   s.accumulateCompressUsage,
		SyncMessages:      s.syncMessages,
	})
	if compressErr != nil {
		log.Ctx(ctx).WithError(compressErr).Warn("Auto context compaction failed")
		if s.structuredProgress != nil {
			s.structuredProgress.Phase = PhaseThinking
		}
		return
	}
	s.messages = pipelineResult.NewMessages
	s.lastCompressIter = s.compressAttempts
	s.validateInvariantsAt(ctx, "post_compress")
	// Update token usage estimate so progress events show reduced tokens
	// immediately instead of showing 0 (from ResetAfterCompress) or stale
	// pre-compress values until the next LLM API call.
	s.setTokenUsageAfterCompress(pipelineResult.NewTokenCount)

	// Persist the API-returned token count so that after a restart, the next Run
	// restores an accurate value instead of the pre-compress count. Without this,
	// ResetAfterCompress zeros the tracker, SaveState skips (no LLM call), and
	// the DB still holds the old large value → immediate re-compression on restart.
	if s.cfg.SaveContextTokens != nil && pipelineResult.NewTokenCount > 0 {
		s.cfg.SaveContextTokens(pipelineResult.NewTokenCount)
	}
	if s.cfg.SaveTokenState != nil && pipelineResult.NewTokenCount > 0 {
		s.cfg.SaveTokenState(pipelineResult.NewTokenCount, 0)
	}

	oldTokenCount := totalTokens

	// Emit PostCompact event (notification, non-blocking)
	if s.cfg.HookManager != nil {
		s.cfg.HookManager.Emit(ctx, &hooks.PostCompactEvent{
			BasePayload: hooks.BasePayload{
				SessionID: s.cfg.ChatID, Channel: s.cfg.Channel,
				SenderID: s.cfg.OriginUserID, ChatID: s.cfg.ChatID,
			},
			Trigger:              "token_limit",
			EstimatedTokensAfter: pipelineResult.NewTokenCount,
		})
	}

	if s.structuredProgress != nil {
		s.structuredProgress.Phase = PhaseThinking
		s.structuredProgress.HistoryCompacted = true
	}
	if s.autoNotify {
		for i := len(s.progressLines) - 1; i >= 0; i-- {
			if strings.Contains(s.progressLines[i], "正在压缩") {
				s.progressLines[i] = fmt.Sprintf("> ✅ 压缩完成: %d → %d tokens", oldTokenCount, pipelineResult.NewTokenCount)
				break
			}
		}
		s.notifyProgress("")
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"new_tokens": pipelineResult.NewTokenCount,
	}).Info("Auto context compaction completed")

	GlobalMetrics.CompressEvents.Add(1)
	GlobalMetrics.CompressTokensIn.Add(int64(oldTokenCount))
	GlobalMetrics.CompressTokensOut.Add(pipelineResult.NewTokenCount)

	if oldTokenCount > 0 {
		reductionRate := 1.0 - float64(pipelineResult.NewTokenCount)/float64(oldTokenCount)
		if reductionRate < 0.10 {
			log.Ctx(ctx).WithFields(log.Fields{
				"old_tokens": oldTokenCount,
				"new_tokens": pipelineResult.NewTokenCount,
				"reduction":  fmt.Sprintf("%.1f%%", reductionRate*100),
			}).Warn("Compaction ineffective (reduction < 10%)")
		}
	}

	if hook := cm.SessionHook(); hook != nil {
		hook.AfterPersist(ctx, s.cfg.Session, pipelineResult.CompressOutput)
	}
}

// aggressiveTruncate performs emergency context truncation when all compression
// retries have failed. It keeps system messages + the last few conversation
// turns, discarding everything in between. Returns true if truncation was applied.
func (s *runState) aggressiveTruncate(ctx context.Context) bool {
	const keepTailMessages = 6 // keep last 6 messages (~3 turns)
	msgs := s.messages
	if len(msgs) <= keepTailMessages+1 {
		// Already too few messages to truncate further
		return false
	}

	// Separate system messages from conversation
	var systemMsgs []llm.ChatMessage
	var conversationMsgs []llm.ChatMessage
	for _, m := range msgs {
		if m.Role == "system" {
			systemMsgs = append(systemMsgs, m)
		} else {
			conversationMsgs = append(conversationMsgs, m)
		}
	}

	if len(conversationMsgs) <= keepTailMessages {
		return false // nothing to truncate
	}

	// Keep: system messages + last keepTailMessages conversation messages
	tailMsgs := conversationMsgs[len(conversationMsgs)-keepTailMessages:]
	newMessages := make([]llm.ChatMessage, 0, len(systemMsgs)+1+len(tailMsgs))
	newMessages = append(newMessages, systemMsgs...)

	// Insert a notice about the truncation so the model knows context was lost
	newMessages = append(newMessages, llm.ChatMessage{
		Role: "system",
		Content: "[System notice: Earlier conversation history was truncated due to context " +
			"window limits. Some earlier context may be lost. Continue from the " +
			"remaining conversation below.]",
	})
	newMessages = append(newMessages, tailMsgs...)

	oldCount := len(msgs)
	s.messages = s.syncMessages(newMessages)

	// Reset token tracker so the next iteration gets fresh data from the API
	if s.tokenTracker != nil {
		s.tokenTracker.ResetAfterCompress()
	}

	// Persist the truncated history
	if s.persistence != nil {
		s.persistence.RewriteAfterCompress(
			newMessages, len(newMessages),
		)
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"old_msg_count": oldCount,
		"new_msg_count": len(s.messages),
		"kept_tail":     keepTailMessages,
	}).Warn("Aggressive truncation applied")

	return true
}

// executeToolCalls runs all tool calls from the LLM response.
func (s *runState) executeToolCalls(ctx context.Context, response *llm.LLMResponse, iteration int) []toolExecResult {
	batch := s.initToolProgress(response, iteration)
	s.dispatchToolCalls(ctx, iteration, response.ToolCalls, batch)
	s.snapshotCompletedIteration(iteration)
	return batch.results
}

// processToolResults handles offload, OAuth, waiting user, and stale invalidation
// for tool execution results.
func (s *runState) processToolResults(ctx context.Context, response *llm.LLMResponse, execResults []toolExecResult) {
	// Count recall tool calls for metrics
	for idx2, tc := range response.ToolCalls {
		r := execResults[idx2]
		if r.err != nil {
			continue
		}
		switch tc.Name {
		case "offload_recall":
			var args struct {
				ID string `json:"id"`
			}
			if json.Unmarshal([]byte(tc.Arguments), &args) == nil && args.ID != "" {
				GlobalMetrics.RecordOffloadRecall(args.ID)
			} else {
				GlobalMetrics.OffloadedRecalls.Add(1)
			}
		case "recall_masked":
			var args struct {
				ID string `json:"id"`
			}
			if json.Unmarshal([]byte(tc.Arguments), &args) == nil && args.ID != "" {
				GlobalMetrics.RecordMaskedRecall(args.ID)
			} else {
				GlobalMetrics.MaskedRecalls.Add(1)
			}
		case "context_edit":
			GlobalMetrics.ContextEditEvents.Add(1)
		case "memory_write":
			GlobalMetrics.MemoryWrites.Add(1)
		case "Read":
			// Track reads of docs/agent/ knowledge files
			var args struct {
				Path string `json:"path"`
			}
			if json.Unmarshal([]byte(tc.Arguments), &args) == nil {
				if strings.HasPrefix(args.Path, "docs/agent/") || strings.Contains(args.Path, "/docs/agent/") {
					GlobalMetrics.DocsAgentReads.Add(1)
				}
			}
		}
	}

	// Process results in original order
	for idx, tc := range response.ToolCalls {
		r := execResults[idx]
		content := r.llmContent

		// Layer 1 Offload
		skipOffload := tc.Name == "offload_recall"
		if tc.Name == "Read" && readArgsHasOffsetOrLimit(tc.Arguments) {
			skipOffload = true
		}
		if s.cfg.OffloadStore != nil && r.err == nil && !skipOffload {
			offloadContent := content
			if r.result != nil && r.result.Summary != "" {
				offloadContent = r.result.Summary
			}
			offloaded, wasOffloaded := s.cfg.OffloadStore.MaybeOffload(ctx, s.offloadSessionKey, tc.Name, tc.Arguments, offloadContent, s.cfg.WorkspaceRoot, "", s.cfg.OriginUserID)
			if wasOffloaded {
				content = offloaded.Summary
				GlobalMetrics.OffloadEvents.Add(1)
				GlobalMetrics.OffloadedItems.Add(1)
				log.Ctx(ctx).WithFields(log.Fields{
					"tool":         tc.Name,
					"offload_id":   offloaded.ID,
					"tokens_saved": offloaded.TokenSize,
				}).Info("Tool result offloaded")
			}
		}

		// OAuth auto-trigger
		if r.err != nil && s.cfg.OAuthHandler != nil {
			if oauthContent, handled := s.cfg.OAuthHandler(ctx, tc, r.err); handled {
				content = oauthContent
				s.autoNotify = false
				if r.result != nil && r.result.WaitingUser {
					s.setWaitingUser(r.result.Summary, r.result.Metadata)
				}
			}
		}

		// Check sessionFinalSent
		if s.cfg.SessionFinalSentCallback != nil && s.cfg.SessionFinalSentCallback() {
			s.autoNotify = false
			s.progressLines = nil
		}

		if r.result != nil && r.result.WaitingUser {
			s.setWaitingUser(r.result.Summary, r.result.Metadata)
		}

		toolMsg := llm.NewToolMessage(tc.Name, tc.ID, tc.Arguments, content)
		if r.result != nil && r.result.Detail != "" {
			toolMsg.Detail = r.result.Detail
		}
		s.messages = s.syncMessages(append(s.messages, toolMsg))
	}

	// Invalidate stale Read offloads after any tool execution
	if s.cfg.OffloadStore != nil {
		staleIDs := s.cfg.OffloadStore.InvalidateStaleReads(ctx, s.offloadSessionKey, s.cfg.WorkspaceRoot, "", s.cfg.OriginUserID)
		if len(staleIDs) > 0 {
			log.Ctx(ctx).WithFields(log.Fields{
				"stale_count": len(staleIDs),
				"stale_ids":   staleIDs,
			}).Info("Stale offloads detected and invalidated")
			s.messages = s.syncMessages(s.cfg.OffloadStore.PurgeStaleMessages(s.offloadSessionKey, s.messages))
		}
	}

	// Detect session_name config change and strip rename hints from user messages.
	// The rename hint (⚠️ 当前会话名) is injected by UserMessageMiddleware on the
	// first user message. Once the agent renames the session, this hint becomes
	// stale and should be removed to prevent repeated rename attempts in later iterations.
	for _, tc := range response.ToolCalls {
		if tc.Name == "config" {
			var args struct {
				Action string `json:"action"`
				Key    string `json:"key"`
			}
			if json.Unmarshal([]byte(tc.Arguments), &args) == nil && args.Action == "set" && args.Key == "session_name" {
				s.stripRenameHints()
				break
			}
		}
	}
}

// postToolProcessing handles dynamic context injection, system reminder,
// session persistence, background task draining, and waiting user check.
// Returns a RunOutput if the loop should terminate, nil otherwise.
func (s *runState) postToolProcessing(ctx context.Context, response *llm.LLMResponse, iteration int) *RunOutput {
	// --- Dynamic Context injection (CWD change detection) ---
	s.dynamicInjector.InjectIfNeeded(s.messages)

	// --- Sync CWD to progress (may change via Cd or worktree cleanup) ---
	if s.structuredProgress != nil && s.cfg.Session != nil {
		s.structuredProgress.CWD = s.cfg.Session.GetCurrentDir()
	}

	// --- System Reminder injection ---
	if len(response.ToolCalls) > 0 {
		// Strip ALL previous reminders from earlier messages to ensure only
		// the latest one survives. Must not break on first non-reminder —
		// intervening assistant/user messages can split reminder-bearing
		// tool messages, leaving older reminders alive past the break point.
		for idx := len(s.messages) - 2; idx >= 0; idx-- {
			if strings.Contains(s.messages[idx].Content, "<system-reminder>") {
				s.messages[idx].Content = stripSystemReminder(s.messages[idx].Content)
			}
		}

		var todoSummary string
		if s.cfg.TodoManager != nil && s.sessionKey != "" {
			todoSummary = s.cfg.TodoManager.GetTodoSummary(s.sessionKey)
		}

		// Get current CWD for system reminder
		var cwd string
		if s.cfg.Session != nil {
			cwd = s.cfg.Session.GetCurrentDir()
		}

		// Derive sessionName from chatID (format: workDir or workDir:name).
		// s.cfg.SessionName is set once at engine creation and becomes stale
		// when the user switches sessions; chatID always reflects the active one.
		sessionName := "default"
		if idx := strings.LastIndex(s.cfg.ChatID, ":"); idx > 0 {
			workDirPart := s.cfg.ChatID[:idx]
			if strings.HasPrefix(workDirPart, "/") || strings.HasPrefix(workDirPart, ".") || strings.HasPrefix(workDirPart, "~") {
				sessionName = s.cfg.ChatID[idx+1:]
			}
		}
		reminder := BuildSystemReminder(s.messages, response.ToolCalls, todoSummary, s.cfg.AgentID, cwd, s.sessionKey, sessionName)
		if reminder != "" && len(s.messages) > 0 {
			lastIdx := len(s.messages) - 1
			s.messages[lastIdx].Content += "\n\n" + reminder
		}
	}

	// --- Incremental session persistence ---
	s.persistence.IncrementalPersist(s.messages)
	s.validateInvariantsAt(ctx, "post_persist")

	// --- Background notification draining (bg tasks + bg subagents) ---
	if s.cfg.DrainBgNotifications != nil {
		pending := s.cfg.DrainBgNotifications()
		for _, notif := range pending {
			switch n := notif.(type) {
			case *tools.BackgroundTask:
				s.injectBgTaskNotification(ctx, iteration, n)
			case *tools.SubAgentBgNotify:
				s.injectSubAgentBgNotification(ctx, iteration, n)
			case *tools.CronFired:
				s.injectCronFiredNotification(ctx, iteration, n)
			case *tools.QueuedUserMessage:
				s.injectQueuedUserMessage(ctx, iteration, n)
			}
		}
	}

	// Check if any tool marked as waiting for user response
	if s.waitingUser {
		log.Ctx(ctx).Info("Tool is waiting for user response, ending loop without additional reply")
		outMsg := &channel.OutboundMsg{
			Channel:     s.cfg.Channel,
			ChatID:      s.cfg.ChatID,
			ToolsUsed:   s.toolsUsed,
			WaitingUser: true,
		}
		if s.waitingQuestion != "" || len(s.waitingMetadata) > 0 || s.cfg.SenderID != "" {
			outMsg.Metadata = make(map[string]string)
			if s.cfg.SenderID != "" {
				outMsg.Metadata["sender_id"] = s.cfg.SenderID
			}
			if s.waitingQuestion != "" {
				outMsg.Metadata["ask_question"] = s.waitingQuestion
			}
			for k, v := range s.waitingMetadata {
				outMsg.Metadata[k] = v
			}
		}
		return s.buildOutput(outMsg)
	}

	return nil
}

// injectBgTaskNotification injects a bg task completion as a synthetic tool call/result pair.
func (s *runState) injectBgTaskNotification(ctx context.Context, iteration int, bgTask *tools.BackgroundTask) {
	bgContent := tools.FormatBgTaskCompletion(bgTask, "")
	bgAssistantMsg := llm.ChatMessage{
		Role:    "assistant",
		Content: "A background task has completed. Let me check the result.",
		ToolCalls: []llm.ToolCall{{
			ID:   "bg_" + bgTask.ID,
			Name: "background_task_result",
		}},
	}
	if s.cfg.OffloadStore != nil {
		if offloaded, ok := s.cfg.OffloadStore.MaybeOffload(ctx, s.offloadSessionKey, "background_task_result", "", bgContent, s.cfg.WorkspaceRoot, "", s.cfg.OriginUserID); ok {
			bgContent = offloaded.Summary
			GlobalMetrics.OffloadEvents.Add(1)
			GlobalMetrics.OffloadedItems.Add(1)
		}
	}
	bgToolMsg := llm.NewToolMessage("background_task_result", "bg_"+bgTask.ID, "", bgContent)
	s.messages = s.syncMessages(append(s.messages, bgAssistantMsg, bgToolMsg))
	log.Ctx(ctx).WithField("task_id", bgTask.ID).Info("Injected bg task completion into Run loop")

	if s.cfg.Session != nil {
		_ = s.cfg.Session.AddMessage(bgAssistantMsg)
		_ = s.cfg.Session.AddMessage(bgToolMsg)
		s.persistence.MarkAllPersisted(len(s.messages))
	}

	if s.structuredProgress != nil {
		var elapsed time.Duration
		if bgTask.FinishedAt != nil {
			elapsed = bgTask.FinishedAt.Sub(bgTask.StartedAt)
		}
		s.structuredProgress.CompletedTools = append(s.structuredProgress.CompletedTools, ToolProgress{
			Name:      "background_task_result",
			Label:     fmt.Sprintf("bg:%s", bgTask.ID),
			Status:    ToolDone,
			Elapsed:   elapsed,
			Iteration: iteration,
		})
		if s.autoNotify {
			s.notifyProgress("")
		}
	}
}

// injectSubAgentBgNotification injects a bg subagent notification as a synthetic tool call/result pair.
// Progress notifications are dropped entirely — they would pollute the parent's TUI and waste LLM tokens.
// Only completed notifications are injected (as tool messages) and shown in the TUI progress block.
func (s *runState) injectSubAgentBgNotification(ctx context.Context, iteration int, n *tools.SubAgentBgNotify) {
	// Drop progress notifications — only completion matters for the parent agent
	if n.Type == tools.SubAgentBgNotifyProgress {
		log.Ctx(ctx).WithFields(log.Fields{
			"role":     n.Role,
			"instance": n.Instance,
		}).Debug("Dropping bg subagent progress notification in Run loop")
		return
	}
	bgContent := tools.FormatSubAgentBgNotify(n)
	toolName := "bg_subagent_" + string(n.Type)
	toolID := fmt.Sprintf("bgsub_%s_%s", n.Role, n.Instance)
	assistantMsg := llm.ChatMessage{
		Role:    "assistant",
		Content: fmt.Sprintf("Background subagent %s has a %s update.", n.Role, n.Type),
		ToolCalls: []llm.ToolCall{{
			ID:   toolID,
			Name: toolName,
		}},
	}
	if s.cfg.OffloadStore != nil {
		if offloaded, ok := s.cfg.OffloadStore.MaybeOffload(ctx, s.offloadSessionKey, toolName, "", bgContent, s.cfg.WorkspaceRoot, "", s.cfg.OriginUserID); ok {
			bgContent = offloaded.Summary
			GlobalMetrics.OffloadEvents.Add(1)
			GlobalMetrics.OffloadedItems.Add(1)
		}
	}
	toolMsg := llm.NewToolMessage(toolName, toolID, "", bgContent)
	s.messages = s.syncMessages(append(s.messages, assistantMsg, toolMsg))
	log.Ctx(ctx).WithFields(log.Fields{
		"role":     n.Role,
		"instance": n.Instance,
		"type":     n.Type,
	}).Info("Injected bg subagent notification into Run loop")

	if s.cfg.Session != nil {
		_ = s.cfg.Session.AddMessage(assistantMsg)
		_ = s.cfg.Session.AddMessage(toolMsg)
		s.persistence.MarkAllPersisted(len(s.messages))
	}

	// Show completion in TUI progress block
	if s.structuredProgress != nil {
		s.structuredProgress.CompletedTools = append(s.structuredProgress.CompletedTools, ToolProgress{
			Name:      toolName,
			Label:     fmt.Sprintf("bgsub:%s/%s", n.Role, n.Instance),
			Status:    ToolDone,
			Iteration: iteration,
		})
		if s.autoNotify {
			s.notifyProgress("")
		}
	}
}

// injectCronFiredNotification injects a cron fired notification as a synthetic tool call/result pair.
// Cron messages are injected as tool results so the LLM can act on them during the current Run.
func (s *runState) injectCronFiredNotification(ctx context.Context, iteration int, c *tools.CronFired) {
	bgContent := fmt.Sprintf("⏰ A scheduled cron job has fired.\n\nMessage: %s", c.Message)
	toolName := "cron_fired"
	toolID := "cron_" + c.SessionKey()
	assistantMsg := llm.ChatMessage{
		Role:    "assistant",
		Content: "A scheduled cron job has fired. Let me process it.",
		ToolCalls: []llm.ToolCall{{
			ID:   toolID,
			Name: toolName,
		}},
	}
	if s.cfg.OffloadStore != nil {
		if offloaded, ok := s.cfg.OffloadStore.MaybeOffload(ctx, s.offloadSessionKey, toolName, "", bgContent, s.cfg.WorkspaceRoot, "", s.cfg.OriginUserID); ok {
			bgContent = offloaded.Summary
			GlobalMetrics.OffloadEvents.Add(1)
			GlobalMetrics.OffloadedItems.Add(1)
		}
	}
	toolMsg := llm.NewToolMessage(toolName, toolID, "", bgContent)
	s.messages = s.syncMessages(append(s.messages, assistantMsg, toolMsg))
	log.Ctx(ctx).WithField("session_key", c.SessionKey()).Info("Injected cron fired notification into Run loop")

	if s.cfg.Session != nil {
		_ = s.cfg.Session.AddMessage(assistantMsg)
		_ = s.cfg.Session.AddMessage(toolMsg)
		s.persistence.MarkAllPersisted(len(s.messages))
	}

	// Show in TUI progress block
	if s.structuredProgress != nil {
		s.structuredProgress.CompletedTools = append(s.structuredProgress.CompletedTools, ToolProgress{
			Name:      toolName,
			Label:     "cron",
			Status:    ToolDone,
			Iteration: iteration,
		})
		if s.autoNotify {
			s.notifyProgress("")
		}
	}
}

// injectQueuedUserMessage injects a user message that was delivered while the SubAgent
// was running. The message is injected as a synthetic tool call/result pair so the
// SubAgent sees it between iterations. ReplyFn is called after injection to notify
// the sender of success. The message content explicitly tells the SubAgent the delivery
// was successful.
func (s *runState) injectQueuedUserMessage(ctx context.Context, iteration int, m *tools.QueuedUserMessage) {
	toolName := "delivered_message"
	toolID := fmt.Sprintf("delivered_%d_%d", iteration, time.Now().UnixNano())

	bgContent := fmt.Sprintf("📬 [消息已送达确认] 你收到了一条来自主 agent 的消息：\n\n%s\n\n✅ 此消息已成功送达，无需回复确认。", m.Content)

	assistantMsg := llm.ChatMessage{
		Role:    "assistant",
		Content: "A message from the parent agent was delivered while I was working.",
		ToolCalls: []llm.ToolCall{{
			ID:   toolID,
			Name: toolName,
		}},
	}

	if s.cfg.OffloadStore != nil {
		if offloaded, ok := s.cfg.OffloadStore.MaybeOffload(ctx, s.offloadSessionKey, toolName, "", bgContent, s.cfg.WorkspaceRoot, "", s.cfg.OriginUserID); ok {
			bgContent = offloaded.Summary
			GlobalMetrics.OffloadEvents.Add(1)
			GlobalMetrics.OffloadedItems.Add(1)
		}
	}

	toolMsg := llm.NewToolMessage(toolName, toolID, "", bgContent)
	s.messages = s.syncMessages(append(s.messages, assistantMsg, toolMsg))

	log.Ctx(ctx).WithFields(log.Fields{
		"content_len": len(m.Content),
		"iteration":   iteration,
	}).Info("Injected queued user message into SubAgent Run loop")

	if s.cfg.Session != nil {
		_ = s.cfg.Session.AddMessage(assistantMsg)
		_ = s.cfg.Session.AddMessage(toolMsg)
		s.persistence.MarkAllPersisted(len(s.messages))
	}

	// Notify sender of successful delivery
	if m.ReplyFn != nil {
		m.ReplyFn(nil)
	}

	// Show in TUI progress block
	if s.structuredProgress != nil {
		s.structuredProgress.CompletedTools = append(s.structuredProgress.CompletedTools, ToolProgress{
			Name:      toolName,
			Label:     "delivered_message",
			Status:    ToolDone,
			Iteration: iteration,
		})
		if s.autoNotify {
			s.notifyProgress("")
		}
	}
}

// buildMaxIterOutput creates the output for when max iterations is reached.
func (s *runState) buildMaxIterOutput() *RunOutput {
	return s.buildOutput(&channel.OutboundMsg{
		Channel:   s.cfg.Channel,
		ChatID:    s.cfg.ChatID,
		Content:   "已达到最大迭代次数，请重新描述你的需求。",
		ToolsUsed: s.toolsUsed,
	})
}

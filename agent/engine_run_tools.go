package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"xbot/clipanic"
	"xbot/llm"
	log "xbot/logger"

	"xbot/tools"
)

// toolExecBatch holds state shared across executing a batch of tool calls.
type toolExecBatch struct {
	results          []toolExecResult
	progressStartIdx int
}

// initToolProgress sets up progress placeholders and structured progress for all
// tool calls in the LLM response.
func (s *runState) initToolProgress(response *llm.LLMResponse, iteration int) *toolExecBatch {
	progressStartIdx := len(s.progressLines)
	for _, tc := range response.ToolCalls {
		s.toolsUsed = append(s.toolsUsed, tc.Name)
		s.localToolCalls++
		toolLabel := formatToolProgress(tc.Name, tc.Arguments)
		if s.autoNotify {
			s.progressLines = append(s.progressLines, fmt.Sprintf("> ⏳ %s ...", toolLabel))
		}
	}
	batch := &toolExecBatch{
		results:          make([]toolExecResult, len(response.ToolCalls)),
		progressStartIdx: progressStartIdx,
	}
	if s.structuredProgress != nil {
		s.structuredProgress.Phase = PhaseToolExec
		s.structuredProgress.ActiveTools = make([]ToolProgress, len(response.ToolCalls))
		// NOTE: do NOT clear CompletedTools here — they represent tools that finished
		// earlier in this iteration (e.g. first batch of WebSearch done, second batch started).
		// Clearing them causes completed tools to flicker/disappear in the CLI progress panel.
		for j, tc := range response.ToolCalls {
			s.structuredProgress.ActiveTools[j] = ToolProgress{
				Name:      tc.Name,
				Label:     formatToolProgress(tc.Name, tc.Arguments),
				Status:    ToolPending,
				Iteration: iteration,
				Args:      tc.Arguments,
			}
		}
	}
	if s.autoNotify {
		s.notifyProgress("")
	}
	return batch
}

// execOneTool executes a single tool call and records the result in the batch.
func (s *runState) execOneTool(ctx context.Context, entry toolCallEntry, batch *toolExecBatch) {
	tc := entry.tc
	argPreview := tc.Arguments
	if r := []rune(argPreview); len(r) > 200 {
		argPreview = string(r[:200]) + "..."
	}
	log.Ctx(ctx).WithFields(log.Fields{
		"tool":      tc.Name,
		"id":        tc.ID,
		"iteration": entry.iteration,
		"call_idx":  entry.index,
	}).Debugf("Tool call: %s(%s)", tc.Name, argPreview)

	// Context setup
	var execCtx context.Context
	var cancel context.CancelFunc
	if tc.Name == "SubAgent" {
		execCtx, cancel = ctx, func() {}
		if s.autoNotify {
			pi := batch.progressStartIdx + entry.index
			if pi < len(s.progressLines) {
				execCtx = WithSubAgentProgress(execCtx, func(detail SubAgentProgressDetail) {
					s.progressMu.Lock()
					s.progressLines[pi] = formatSubAgentProgress(detail)
					// Merge structured SubAgent node — don't replace.
					// Multiple SubAgents may be running concurrently; each
					// callback only has one node but we must preserve others.
					newNode := extractSubAgentNodesFromDetail(detail)
					s.subAgentNodes = mergeSubAgentNodeList(s.subAgentNodes, newNode)
					s.progressMu.Unlock()
					s.notifyProgress("")
				})
			}
		}
	} else {
		execCtx, cancel = ctx, func() {}
	}

	start := time.Now()
	if s.structuredProgress != nil && entry.index < len(s.structuredProgress.ActiveTools) {
		s.structuredProgress.ActiveTools[entry.index].Status = ToolRunning
	}
	// Notify CLI immediately so the running animation is visible
	// before the tool blocks on execution.
	if s.autoNotify {
		s.notifyProgress("")
	}
	result, execErr := s.toolExecutor(execCtx, tc)
	elapsed := time.Since(start)
	cancel()

	batch.results[entry.index] = toolExecResult{err: execErr, result: result, elapsed: elapsed}
	s.updateToolResultProgress(ctx, entry, batch, result, execErr, elapsed)
	s.updateToolResultLine(ctx, entry, batch, tc, result, execErr, elapsed)
}

// updateToolResultProgress updates the structured progress entry for a completed tool.
func (s *runState) updateToolResultProgress(ctx context.Context, entry toolCallEntry, batch *toolExecBatch, result *tools.ToolResult, execErr error, elapsed time.Duration) {
	if s.structuredProgress == nil || entry.index >= len(s.structuredProgress.ActiveTools) {
		return
	}
	status := ToolDone
	if execErr != nil || (result != nil && result.IsError) {
		status = ToolError
	}
	s.structuredProgress.ActiveTools[entry.index].Status = status
	s.structuredProgress.ActiveTools[entry.index].Elapsed = elapsed

	summary := ""
	if result != nil && result.Summary != "" {
		summary = strings.TrimSpace(result.Summary)
		if idx := strings.Index(summary, "\n"); idx >= 0 {
			summary = summary[:idx]
		}
		if r := []rune(summary); len(r) > 100 {
			summary = string(r[:100]) + "..."
		}
	} else if execErr != nil {
		summary = execErr.Error()
		if r := []rune(summary); len(r) > 100 {
			summary = string(r[:100]) + "..."
		}
	}
	s.structuredProgress.ActiveTools[entry.index].Summary = summary

	// Store full untruncated result for per-tool body rendering (Read, Shell, etc.)
	if result != nil {
		detail := ""
		if result.Detail != "" {
			detail = result.Detail
		} else if result.Summary != "" {
			detail = result.Summary
		}
		// Cap at 4000 runes to avoid bloating progress payloads
		if r := []rune(detail); len(r) > 4000 {
			detail = string(r[:4000]) + "\n... (truncated)"
		}
		s.structuredProgress.ActiveTools[entry.index].Detail = detail
	}

	// Read plugin toolHints (e.g. file-diff markdown) after PostToolUse hook
	// has fired synchronously. The hint plugin ran inside executeWithHooks.
	if s.cfg.PluginManager != nil {
		if hint := s.cfg.PluginManager.GetToolHints(); hint != "" {
			s.structuredProgress.ActiveTools[entry.index].ToolHints = hint
		}
	}

	// Built-in diff: if no plugin hints, generate diff from tool result metadata.
	// This makes diff work without the external file-diff.sh plugin.
	if s.structuredProgress.ActiveTools[entry.index].ToolHints == "" && result != nil && result.Metadata != nil {
		if diff, ok := result.Metadata["diff"]; ok && diff != "" {
			s.structuredProgress.ActiveTools[entry.index].ToolHints = "```diff\n" + diff + "\n```"
		}
	}
}

// updateToolResultLine updates the progress line for a completed tool (success or failure).
func (s *runState) updateToolResultLine(ctx context.Context, entry toolCallEntry, batch *toolExecBatch, tc llm.ToolCall, result *tools.ToolResult, execErr error, elapsed time.Duration) {
	toolLabel := formatToolProgress(tc.Name, tc.Arguments)
	pi := batch.progressStartIdx + entry.index

	if execErr != nil {
		GlobalMetrics.TotalToolErrors.Add(1)
		log.Ctx(ctx).WithFields(log.Fields{
			"tool":    tc.Name,
			"elapsed": elapsed.Round(time.Millisecond),
		}).WithError(execErr).Debug("Tool failed (hook also logged)")
		batch.results[entry.index].content = fmt.Sprintf("Error: %v\n\nPlease fix the issue and try again with corrected parameters.", execErr)
		batch.results[entry.index].llmContent = batch.results[entry.index].content
		if s.autoNotify {
			if tc.Name == "SubAgent" {
				line := s.progressLines[pi]
				line = strings.ReplaceAll(line, "⏳", "❌")
				line = strings.ReplaceAll(line, "🔄", "❌")
				s.progressLines[pi] = line
			} else {
				s.progressLines[pi] = fmt.Sprintf("> ❌ %s (%s)", toolLabel, elapsed.Round(time.Millisecond))
			}
		}
	} else {
		batch.results[entry.index].content = result.Summary
		batch.results[entry.index].llmContent = buildToolMessageContent(result)
		if result.IsError {
			GlobalMetrics.TotalToolErrors.Add(1)
			batch.results[entry.index].llmContent = fmt.Sprintf("Error: %s\n\nDo NOT retry the same command. Analyze the error, fix the root cause, then try a different approach.", batch.results[entry.index].llmContent)
		}
		resultPreview := result.Summary
		if r := []rune(resultPreview); len(r) > 200 {
			resultPreview = string(r[:200]) + "..."
		}
		log.Ctx(ctx).WithFields(log.Fields{
			"tool":    tc.Name,
			"elapsed": elapsed.Round(time.Millisecond),
		}).Debugf("Tool done: %s", resultPreview)
		if s.autoNotify {
			if tc.Name == "SubAgent" {
				line := s.progressLines[pi]
				// Replace both possible prefixes: ⏳ (initial placeholder) and 🔄 (progress-updated)
				line = strings.ReplaceAll(line, "⏳", "✅")
				line = strings.ReplaceAll(line, "🔄", "✅")
				s.progressLines[pi] = line
			} else {
				icon := "✅"
				if result.IsError {
					icon = "❌"
				}
				s.progressLines[pi] = fmt.Sprintf("> %s %s (%s)", icon, toolLabel, elapsed.Round(time.Millisecond))
			}
		}
	}
}

// dispatchToolCalls dispatches tool calls using the appropriate execution strategy.
func (s *runState) dispatchToolCalls(ctx context.Context, iteration int, toolCalls []llm.ToolCall, batch *toolExecBatch) {
	execFn := func(entry toolCallEntry) {
		s.execOneTool(ctx, entry, batch)
	}

	if s.cfg.EnableReadWriteSplit {
		s.dispatchReadWriteSplit(ctx, iteration, toolCalls, execFn)
	} else if s.cfg.EnableConcurrentSubAgents {
		s.dispatchConcurrentSubAgents(ctx, iteration, toolCalls, execFn)
	} else {
		s.dispatchSequential(ctx, iteration, toolCalls, execFn)
	}
}

// dispatchSequential runs all tool calls one by one, respecting context cancellation.
func (s *runState) dispatchSequential(ctx context.Context, iteration int, toolCalls []llm.ToolCall, execFn func(toolCallEntry)) {
	for idx, tc := range toolCalls {
		// 检查是否已取消：跳过后续工具调用
		if ctx.Err() != nil {
			return
		}
		execFn(toolCallEntry{iteration: iteration, index: idx, tc: tc})
		if s.autoNotify && !s.batchProgressByIteration {
			s.notifyProgress("")
		}
	}
}

// dispatchConcurrentSubAgents runs SubAgent calls concurrently and other calls sequentially.
func (s *runState) dispatchConcurrentSubAgents(ctx context.Context, iteration int, toolCalls []llm.ToolCall, execFn func(toolCallEntry)) {
	var subAgentOps, otherOps []toolCallEntry
	for idx, tc := range toolCalls {
		entry := toolCallEntry{iteration: iteration, index: idx, tc: tc}
		if tc.Name == "SubAgent" {
			subAgentOps = append(subAgentOps, entry)
		} else {
			otherOps = append(otherOps, entry)
		}
	}
	if len(subAgentOps) > 0 {
		s.executeSubAgentOps(ctx, subAgentOps, execFn)
	}
	for _, entry := range otherOps {
		execFn(entry)
		if s.autoNotify && !s.batchProgressByIteration {
			s.notifyProgress("")
		}
	}
}

// dispatchReadWriteSplit categorizes tool calls into read/write/SubAgent and
// runs them with appropriate concurrency.
func (s *runState) dispatchReadWriteSplit(ctx context.Context, iteration int, toolCalls []llm.ToolCall, execFn func(toolCallEntry)) {
	var readOps, writeOps, subAgentOps []toolCallEntry
	for idx, tc := range toolCalls {
		entry := toolCallEntry{iteration: iteration, index: idx, tc: tc}
		if tc.Name == "SubAgent" && s.cfg.EnableConcurrentSubAgents {
			subAgentOps = append(subAgentOps, entry)
		} else if readOnlyTools[tc.Name] {
			readOps = append(readOps, entry)
		} else {
			writeOps = append(writeOps, entry)
		}
	}
	if len(subAgentOps) > 0 {
		s.executeSubAgentOps(ctx, subAgentOps, execFn)
	}
	if len(readOps) > 0 {
		const maxParallel = 8
		sem := make(chan struct{}, maxParallel)
		var wg sync.WaitGroup
		for _, entry := range readOps {
			wg.Add(1)
			sem <- struct{}{}
			go func(e toolCallEntry) {
				defer clipanic.Recover("agent.executeToolCalls.readOp", e.tc, false)
				defer wg.Done()
				defer func() { <-sem }()
				execFn(e)
			}(entry)
		}
		wg.Wait()
		if s.autoNotify && !s.batchProgressByIteration {
			s.notifyProgress("")
		}
	}
	for _, entry := range writeOps {
		execFn(entry)
		if s.autoNotify && !s.batchProgressByIteration {
			s.notifyProgress("")
		}
	}
}

// executeSubAgentOps runs SubAgent tool calls concurrently with semaphore control.
func (s *runState) executeSubAgentOps(ctx context.Context, ops []toolCallEntry, execFn func(toolCallEntry)) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, entry := range ops {
		wg.Add(1)
		go func(e toolCallEntry) {
			defer clipanic.Recover("agent.executeToolCalls.subAgentOp", e.tc, false)
			defer wg.Done()
			var release func()
			if s.cfg.SubAgentSem != nil {
				release = s.cfg.SubAgentSem(ctx)
				defer release()
			}
			execFn(e)
			if s.autoNotify && !s.batchProgressByIteration {
				mu.Lock()
				s.notifyProgress("")
				mu.Unlock()
			}
		}(entry)
	}
	wg.Wait()
}

// snapshotCompletedIteration records the completed iteration snapshot for structured progress.
func (s *runState) snapshotCompletedIteration(iteration int) {
	if s.structuredProgress != nil {
		s.structuredProgress.CompletedTools = append(s.structuredProgress.CompletedTools, s.structuredProgress.ActiveTools...)
		s.structuredProgress.ActiveTools = nil
	}
	if s.autoNotify && !s.batchProgressByIteration && s.structuredProgress != nil {
		s.notifyProgress("")
	}
	if s.structuredProgress != nil && len(s.structuredProgress.CompletedTools) > 0 {
		snap := IterationSnapshot{
			Iteration: iteration,
			Thinking:  s.structuredProgress.ThinkingContent,
			Reasoning: s.structuredProgress.ReasoningContent,
			Tools:     make([]IterationToolSnapshot, len(s.structuredProgress.CompletedTools)),
		}
		for j, t := range s.structuredProgress.CompletedTools {
			snap.Tools[j] = IterationToolSnapshot{
				Name:      t.Name,
				Label:     t.Label,
				Status:    string(t.Status),
				ElapsedMS: t.Elapsed.Milliseconds(),
				Summary:   t.Summary,
			}
		}
		s.iterationSnapshots = append(s.iterationSnapshots, snap)
		if s.cfg.OnIterationSnapshot != nil {
			s.cfg.OnIterationSnapshot(snap)
		}
	}
	if s.autoNotify && s.batchProgressByIteration {
		s.notifyProgress("")
	}
}

// maybeMaskObservations applies lightweight observation masking when context
// exceeds 60% of max tokens but hasn't reached compression threshold.
func (s *runState) maybeMaskObservations(ctx context.Context, totalTokens int64, maxTokens int) {
	if s.cfg.MaskStore == nil {
		return
	}
	if totalTokens <= 0 {
		return
	}
	maskingThreshold := float64(maxTokens) * 0.6
	if float64(totalTokens) <= maskingThreshold {
		return
	}
	keepGroups := calculateKeepGroups(int(totalTokens), maxTokens)
	masked, count, maskedEntries := MaskOldToolResults(s.messages, s.cfg.MaskStore, keepGroups)
	if count == 0 {
		return
	}
	// Diagnostic: log which groups were masked and which were kept
	{
		var groups []int
		for i := range s.messages {
			if s.messages[i].Role == "assistant" && len(s.messages[i].ToolCalls) > 0 {
				groups = append(groups, i)
			}
		}
		firstMasked := -1
		if len(maskedEntries) > 0 {
			firstMasked = maskedEntries[0].MessageIndex
		}
		log.Ctx(ctx).WithFields(log.Fields{
			"keep_groups":  keepGroups,
			"total_groups": len(groups),
			"masked_count": count,
			"first_masked": firstMasked,
			"msg_count":    len(s.messages),
			"token_ratio":  fmt.Sprintf("%.2f", float64(totalTokens)/float64(maxTokens)),
		}).Info("Observation masking applied")
	}
	s.messages = s.syncMessages(masked)
	GlobalMetrics.MaskingEvents.Add(1)
	GlobalMetrics.MaskedItems.Add(int64(count))

	// Persist masked content to session so the next Run() loads masked messages.
	// CRITICAL: s.messages indices include system messages (not in DB).
	// The session DB indices skip system messages. Calculate the offset so
	// masked content lands on the correct DB rows.
	dbOffset := 0
	for _, msg := range s.messages {
		if msg.Role == "system" {
			dbOffset++
		} else {
			break
		}
	}
	if s.cfg.Session != nil {
		persistedMasked := 0
		for _, entry := range maskedEntries {
			dbIdx := entry.MessageIndex - dbOffset
			if dbIdx < 0 {
				continue // system message, not in DB
			}
			if s.persistence.IsPersisted(entry.MessageIndex) {
				// Use NonDisplayOnly variant to align with GetAllMessages
				// (both filter WHERE COALESCE(display_only, 0) = 0).
				if err := s.cfg.Session.UpdateMessageContentNonDisplayOnly(dbIdx, entry.Content); err != nil {
					log.Ctx(ctx).WithError(err).WithField("idx", dbIdx).WithField("raw_idx", entry.MessageIndex).Warn("Failed to persist masked message to session")
				} else {
					persistedMasked++
				}
			}
		}
		if persistedMasked > 0 {
			log.Ctx(ctx).WithField("persisted_masked", persistedMasked).WithField("db_offset", dbOffset).Info("Persisted masked messages to session")
		}
	}

	if s.autoNotify {
		s.progressLines = append(s.progressLines, fmt.Sprintf("> 🎭 上下文较大 (%d tokens)，已遮蔽 %d 条旧工具结果", totalTokens, count))
		s.notifyProgress("")
	}
	log.Ctx(ctx).WithField("masked_count", count).Info("Observation masking triggered")
}

// setWaitingUser sets the waiting user state from a tool result.
func (s *runState) setWaitingUser(summary string, metadata map[string]string) {
	s.waitingUser = true
	if s.waitingQuestion == "" && summary != "" {
		s.waitingQuestion = summary
	}
	if len(metadata) > 0 && s.waitingMetadata == nil {
		s.waitingMetadata = make(map[string]string)
		for k, v := range metadata {
			s.waitingMetadata[k] = v
		}
	}
}

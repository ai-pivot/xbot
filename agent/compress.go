package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"xbot/bus"
	"xbot/channel"
	"xbot/llm"
	log "xbot/logger"
	"xbot/protocol"
	"xbot/session"
	"xbot/tools"
)

// CompressResult holds the compaction output.
type CompressResult struct {
	LLMView     []llm.ChatMessage // Full messages for continuing the current Run()
	SessionView []llm.ChatMessage // User/assistant only, persisted to session

	// CompressedTokens is the estimated token count of LLMView after compression.
	// This is the authoritative "how big is the context now" value.
	CompressedTokens int

	// Token usage from compression LLM calls (for tracking in /usage).
	InputTokens  int64
	OutputTokens int64
	CachedTokens int64
	LLMCalls     int
}

// compactionPrompt is the structured contract for LLM-based context compaction.
// V2: PRESERVES mask/offload references (instead of stripping them) so the
// compressed context can recall original data on demand. This is a key
// advantage over Codex and Claude Code — their summaries lose access to
// original tool outputs permanently.
const compactionPrompt = `You are performing a CONTEXT COMPACTION. Create a structured working state
that allows another LLM to continue this task without re-asking any questions.

## CRITICAL: Recency Priority

The conversation history below is ordered oldest → newest. Messages near the END
are the most recent work and MUST be preserved in maximum detail. Older messages
that are unrelated to the current topic may be aggressively compressed or omitted
entirely to save space for recent work. NEVER sacrifice recent context for old history.

## Required Sections

### Historical Context (from previous compactions)
If this conversation contains a summary from a previous compaction (marked with
"[Compacted context]"), extract only what remains relevant to the CURRENT topic.
If older context is unrelated to recent work, summarize it in 1-2 sentences or omit it.
Do NOT bloat this section — relevance to the current task is the filter.

### Task Summary
What the user asked for and current overall progress (1-3 sentences).

### Key Decisions
Decisions made during this session and WHY they were made (so they are not
re-litigated). Include rejected approaches and the reasoning.

### Active Files
Files currently being worked on (full paths). Include key function signatures
if relevant.

### Errors & Fixes
Errors encountered and how they were resolved. Preserve error messages verbatim.

### Recent Work (HIGHEST PRIORITY)
What was being worked on most recently — preserve in detail:
- The last few user requests and what was done for each
- Files modified, code changes made, commands run
- Current state of in-progress work
- Any pending items or incomplete steps
This section gets the most space in your output. Omitting recent work is NOT acceptable.

### Next Steps
What should happen next to continue from where we left off.

## Constraints
- Preserve ALL file paths from active operations
- Preserve ALL error messages verbatim
- PRESERVE offload markers (📂 [offload:ol_xxx]) and masked markers (📂 [masked:mk_xxx])
  in their original form — these markers allow recalling the full original data when needed.
  Do NOT strip or remove offload IDs (ol_...) or mask IDs (mk_...) from your output.
  If a marker's summary text is important, include BOTH the marker and its summary.
- Be concise — focus on facts, not narrative
- Allocate the majority of your output budget to "Recent Work" — this is the most important section`

// continuationMessage is injected after compaction to tell the LLM to resume work.
const continuationMessage = `This conversation was compacted from a longer session. The "Recent Work" section above is the most critical context — it reflects what was happening immediately before compaction. Continue from where you left off without re-asking the user any questions.`

// extractDialogueFromTail extracts a pure user/assistant view from a tail
// that may contain tool messages. Tool group summaries are folded into
// assistant messages.
func extractDialogueFromTail(tail []llm.ChatMessage) []llm.ChatMessage {
	var result []llm.ChatMessage
	var pendingToolSummary strings.Builder

	for _, msg := range tail {
		switch {
		case msg.Role == "user":
			flushPending(&result, &pendingToolSummary)
			result = append(result, llm.NewUserMessage(msg.Content))

		case msg.Role == "assistant" && len(msg.ToolCalls) > 0:
			if msg.Content != "" {
				// assistant thinking text (non-empty content alongside tool calls)
				pendingToolSummary.WriteString(msg.Content + "\n")
			}
			for _, tc := range msg.ToolCalls {
				summary := summarizeToolCall(tc.Name, tc.Arguments)
				pendingToolSummary.WriteString(summary + "\n")
			}

		case msg.Role == "assistant":
			flushPending(&result, &pendingToolSummary)
			result = append(result, llm.NewAssistantMessage(msg.Content))

		case msg.Role == "tool":
			stripped := stripOffloadMaskPrefix(msg.Content)
			if stripped != "" {
				pendingToolSummary.WriteString("  → " + truncateRunes(stripped, 200) + "\n")
			}
		}
	}
	flushPending(&result, &pendingToolSummary)
	return result
}

// summarizeToolCall converts a raw tool call into a human-readable one-liner.
// e.g. Shell({"command":"gh pr view 396"}) → "Shell: gh pr view 396"
func summarizeToolCall(name, args string) string {
	switch name {
	case "Shell":
		cmd := ExtractJSONString(args, "command")
		if cmd == "" {
			return fmt.Sprintf("- **%s**: ...", name)
		}
		// Strip common prefixes for brevity
		if len(cmd) > 80 {
			cmd = cmd[:80] + "..."
		}
		return fmt.Sprintf("- **%s**: `%s`", name, cmd)
	case "Read":
		path := ExtractJSONString(args, "path")
		if path == "" {
			return fmt.Sprintf("- **%s**: ...", name)
		}
		return fmt.Sprintf("- **%s**: `%s`", name, path)
	case "Grep":
		pattern := ExtractJSONString(args, "pattern")
		if pattern == "" {
			return fmt.Sprintf("- **%s**: ...", name)
		}
		include := ExtractJSONString(args, "include")
		if include != "" {
			return fmt.Sprintf("- **%s**: `%s` in `%s`", name, pattern, include)
		}
		return fmt.Sprintf("- **%s**: `%s`", name, pattern)
	case "Glob":
		pat := ExtractJSONString(args, "pattern")
		if pat == "" {
			return fmt.Sprintf("- **%s**: ...", name)
		}
		return fmt.Sprintf("- **%s**: `%s`", name, pat)
	case "FileReplace", "FileCreate":
		path := ExtractJSONString(args, "path")
		if path == "" {
			return fmt.Sprintf("- **%s**: ...", name)
		}
		return fmt.Sprintf("- **%s**: `%s`", name, path)
	default:
		// Generic: show name + truncated args
		truncated := truncateArgs(args, 60)
		return fmt.Sprintf("- **%s**: %s", name, truncated)
	}
}

// stripOffloadMaskPrefix removes 📂 [offload:...] / 📂 [masked:...] prefix from tool content.
func stripOffloadMaskPrefix(content string) string {
	if strings.HasPrefix(content, "📂 [offload:") || strings.HasPrefix(content, "📂 [masked:") {
		if idx := strings.Index(content, "] "); idx >= 0 {
			return content[idx+2:]
		}
	}
	return content
}

func flushPending(result *[]llm.ChatMessage, builder *strings.Builder) {
	if builder.Len() == 0 {
		return
	}
	*result = append(*result, llm.NewAssistantMessage(builder.String()))
	builder.Reset()
}

func truncateArgs(args string, maxLen int) string {
	runes := []rune(args)
	if len(runes) <= maxLen {
		return args
	}
	return string(runes[:maxLen]) + "..."
}

// handleCompress handles the /compress command: manually trigger context compaction.
func (a *Agent) handleCompress(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*channel.OutboundMsg, error) {
	a.emitBuiltinProgress(msg.Channel, msg.ChatID, PhaseCompressing)
	// Capture the post-compress token count so the deferred PhaseDone
	// can carry it in the progress event and update the TUI context bar.
	var compressTokenUsage *protocol.TokenUsage
	defer func() {
		a.emitBuiltinProgressDone(msg.Channel, msg.ChatID, compressTokenUsage, true)
	}()

	messages, err := a.buildPrompt(ctx, msg, tenantSession)
	if err != nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("构建上下文失败: %v", err),
		}, nil
	}

	if len(messages) == 0 {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "当前没有消息需要压缩。",
		}, nil
	}

	tokenCount := len(messages) * 200 // rough estimate for display only

	// Always allow manual /compress regardless of threshold — user explicitly requested it.

	cm := a.GetContextManager()

	// Inject memory tools so manual compaction can archive important context
	// (same as the auto-compress path in engine.Run).
	if defs, exec := a.buildMemoryToolSetup(msg.Channel, msg.ChatID); defs != nil {
		cm.SetMemoryTools(defs, exec)
	}

	userCtx := UserContextFromContext(ctx)
	result, err := cm.ManualCompress(ctx, messages, userCtx.LLMClient, userCtx.Model)
	if err != nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("上下文压缩失败: %v", err),
		}, nil
	}

	// Record compress token usage to /usage stats.
	if result.LLMCalls > 0 && a.multiSession != nil {
		if recordErr := a.multiSession.RecordUserTokenUsage(
			msg.SenderID, userCtx.Model,
			int(result.InputTokens), int(result.OutputTokens), int(result.CachedTokens),
			0, result.LLMCalls,
		); recordErr != nil {
			log.Ctx(ctx).WithError(recordErr).Warn("Failed to record compress token usage")
		}
	}

	if err := tenantSession.Clear(); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to clear session for compression")
		newTokenCount := len(result.LLMView) * 200
		compressTokenUsage = &protocol.TokenUsage{PromptTokens: int64(newTokenCount)}
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("上下文压缩完成 (内存): %d 条 → %d 条 (LLM %d 条, Session %d 条)", tokenCount/200, newTokenCount/200, len(result.LLMView), len(result.SessionView)),
		}, nil
	}
	allOk := true
	for _, msg := range result.SessionView {
		if err := assertNoSystemPersist(msg); err != nil {
			continue
		}
		if err := tenantSession.AddMessage(msg); err != nil {
			log.Ctx(ctx).WithError(err).Error("Partial write during compression, session may be corrupted")
			allOk = false
			break
		}
	}

	// Use CompressedTokens for accuracy (same as auto-compress path).
	newTokenCount := int64(result.CompressedTokens)
	// Fallback to rough estimate if CompressedTokens is 0 (shouldn't happen).
	if newTokenCount <= 0 {
		newTokenCount = int64(len(result.LLMView) * 200)
	}

	// Persist the compressed token count so the next Run() doesn't immediately
	// trigger re-compression. Without this, GetLastContextTokens() returns the
	// pre-compress high value, and maybeCompress triggers again on the next message.
	if saveErr := tenantSession.SaveContextTokens(newTokenCount); saveErr != nil {
		log.Ctx(ctx).WithError(saveErr).Warn("Failed to save context tokens after manual compress")
	}
	// Also update the token_state table (fallback used by GetTokenState when
	// context_tokens is unavailable).
	if memSvc := tenantSession.MemoryService(); memSvc != nil {
		if saveErr := memSvc.SetTokenState(ctx, tenantSession.TenantID(), newTokenCount, 0); saveErr != nil {
			log.Ctx(ctx).WithError(saveErr).Warn("Failed to save token state after manual compress")
		}
	}

	// Set the token usage for the deferred PhaseDone so the CLI context bar updates.
	compressTokenUsage = &protocol.TokenUsage{PromptTokens: newTokenCount}

	if allOk {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("上下文压缩完成: %d 条 → %d 条 (LLM %d 条, Session %d 条)", tokenCount/200, newTokenCount/200, len(result.LLMView), len(result.SessionView)),
		}, nil
	}
	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("上下文压缩完成 (内存): %d 条 → %d 条 (LLM %d 条, Session %d 条)", tokenCount/200, newTokenCount/200, len(result.LLMView), len(result.SessionView)),
	}, nil
}

// formatCompactLine formats a single message for the compaction history text.
func formatCompactLine(msg llm.ChatMessage) string {
	role := strings.ToUpper(msg.Role)
	content := msg.Content
	if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
		var toolNames []string
		for _, tc := range msg.ToolCalls {
			toolNames = append(toolNames, tc.Name)
		}
		content += fmt.Sprintf(" [called tools: %s]", strings.Join(toolNames, ", "))
	}
	runes := []rune(content)
	if len(runes) > 2000 {
		content = string(runes[:2000]) + "..."
	}
	return fmt.Sprintf("[%s] %s\n\n", role, content)
}

// truncateRunes truncates a string to maxLen runes (multi-byte safe).
func truncateRunes(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "...[truncated]"
}

// findTailCutPoint 从后向前扫描消息列表，找到 tail 的起始位置。
// tail 定义为从最后一个用户消息或纯文本 assistant 消息开始到末尾的所有消息。
// 返回 tailStart（tail 在 messages 中的起始索引）和 originalUserMsg（如果找到用户消息）。
func findTailCutPoint(messages []llm.ChatMessage) (tailStart int, originalUserMsg *llm.ChatMessage) {
	tailStart = len(messages)
	for i := len(messages) - 1; i >= 1; i-- {
		msg := messages[i]
		if msg.Role == "user" {
			tailStart = i
			msgCopy := msg
			originalUserMsg = &msgCopy
			break
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) == 0 {
			tailStart = i
			break
		}
		if i == 1 {
			tailStart = 1
		}
	}
	return
}

// capTailLength 根据 maxContextTokens 限制 tail 长度。
// tail 最多保留 maxContextTokens 的 15%（按 ~200 tokens/message 估算），
// 硬上限 300 条，软下限 50 条。返回调整后的 tailStart。
func capTailLength(messages []llm.ChatMessage, tailStart int, maxContextTokens int) int {
	const maxTailContextFraction = 0.15
	const tokensPerMessage = 200
	dynamicTailLimit := int(float64(maxContextTokens) * maxTailContextFraction / tokensPerMessage)
	maxTailMessages := dynamicTailLimit
	if maxTailMessages > 300 {
		maxTailMessages = 300
	}
	if maxTailMessages < 50 {
		maxTailMessages = 50
	}
	tailLen := len(messages) - tailStart
	if tailLen > maxTailMessages {
		return len(messages) - maxTailMessages
	}
	return tailStart
}

// mergeCompressedResult 将压缩结果与 system 消息和 tail 合并为 LLM view 和 session view。
func mergeCompressedResult(compressed string, systemMsgs []llm.ChatMessage, tail []llm.ChatMessage, originalUserMsg *llm.ChatMessage, originalTailStart int, tailStart int) (llmView, sessionView []llm.ChatMessage) {
	summaryMsg := llm.NewUserMessage("[Compacted context]\n\n" + compressed)
	continuationMsg := llm.NewUserMessage(continuationMessage)

	needInjectUserMsg := originalUserMsg != nil && tailStart > originalTailStart

	extraCap := 0
	if needInjectUserMsg {
		extraCap = 1
	}
	llmView = make([]llm.ChatMessage, 0, len(systemMsgs)+2+extraCap+len(tail))
	llmView = append(llmView, systemMsgs...)
	llmView = append(llmView, summaryMsg)
	llmView = append(llmView, continuationMsg)
	if needInjectUserMsg {
		llmView = append(llmView, *originalUserMsg)
	}
	llmView = append(llmView, tail...)

	tailDialogue := extractDialogueFromTail(tail)
	sessionView = make([]llm.ChatMessage, 0, 1+extraCap+len(tailDialogue))
	sessionView = append(sessionView, summaryMsg)
	if needInjectUserMsg {
		sessionView = append(sessionView, *originalUserMsg)
	}
	sessionView = append(sessionView, tailDialogue...)
	return
}

// compactMessages performs a structured compaction of conversation history.
//
// V2 Design — reuses the agent loop (engine.Run) instead of a custom LLM call loop:
//  1. Find a safe cut point (last user message or plain assistant message)
//  2. Cap tail length so only the most recent iterations are kept verbatim
//  3. Separate system messages from the history before the cut point
//  4. Build history text within token budget
//  5. Call engine.Run() with a compression-focused RunConfig (reuses streaming, retry, sanitization)
//  6. Build result: [system] + [compaction summary] + [continuation] + [tail messages]
func compactMessages(
	ctx context.Context,
	messages []llm.ChatMessage,
	client llm.LLM,
	model string,
	maxContextTokens int,
) (*CompressResult, error) {
	// Step 1: find tail cut point — keep the last user message and everything after it.
	tailStart, originalUserMsg := findTailCutPoint(messages)
	originalTailStart := tailStart

	// Step 2: cap tail length.
	newTailStart := capTailLength(messages, tailStart, maxContextTokens)
	if newTailStart != tailStart {
		log.Ctx(ctx).WithFields(log.Fields{
			"old_tail_start": tailStart,
			"new_tail_start": newTailStart,
			"tail_capped":    (len(messages) - tailStart) - (len(messages) - newTailStart),
		}).Info("Capping tail length for compaction")
		tailStart = newTailStart
	}

	// Step 3: separate system messages from content to compress
	var systemMsgs []llm.ChatMessage
	var toCompress []llm.ChatMessage

	for i, msg := range messages {
		if i >= tailStart {
			break
		}
		if msg.Role == "system" {
			systemMsgs = append(systemMsgs, msg)
		} else {
			toCompress = append(toCompress, msg)
		}
	}

	tail := messages[tailStart:]

	if len(toCompress) == 0 {
		// Nothing to compress — all non-system messages are in tail.
		// Return as-is (the caller's InputTooLong handler will retry if needed).
		llmView := make([]llm.ChatMessage, 0, len(systemMsgs)+len(tail))
		llmView = append(llmView, systemMsgs...)
		llmView = append(llmView, tail...)

		sessionView := extractDialogueFromTail(tail)
		return &CompressResult{
			LLMView:     llmView,
			SessionView: sessionView,
		}, nil
	}

	// Step 4: build the history text for the compaction prompt.
	var historyText strings.Builder

	perMsgTokens := make([]int, len(toCompress))
	totalCompressTokens := 0
	for i, msg := range toCompress {
		charEstimate := len([]rune(msg.Content)) * 2 / 3
		perMsgTokens[i] = charEstimate
		totalCompressTokens += charEstimate
	}

	const compactionOverhead = 1500
	historyBudget := maxContextTokens - compactionOverhead
	if historyBudget < 1000 {
		historyBudget = 1000
	}

	fitCount := 0
	usedTokens := 0
	for i := len(toCompress) - 1; i >= 0; i-- {
		if usedTokens+perMsgTokens[i] > historyBudget {
			break
		}
		usedTokens += perMsgTokens[i]
		fitCount++
	}

	omittedCount := len(toCompress) - fitCount
	if omittedCount > 0 {
		fmt.Fprintf(&historyText, "[Note: %d older messages omitted from compaction]\n\n", omittedCount)
	}
	fitting := toCompress[omittedCount:]
	recentStart := len(fitting) * 3 / 5
	for i, msg := range fitting {
		if i == recentStart && recentStart > 0 && recentStart < len(fitting) {
			historyText.WriteString("--- RECENT WORK BEGINS (messages below are highest priority) ---\n\n")
		}
		historyText.WriteString(formatCompactLine(msg))
	}

	// Step 5: calculate token-budget-aware target output length.
	// Unlike the old fixed 5000-char cap, we compute based on the available context budget:
	// target = min(30% of original tokens, 50% of available budget after tail + overhead)
	totalChars := 0
	for _, msg := range messages {
		totalChars += len([]rune(msg.Content))
	}
	originalTokens := totalChars / 3

	tailEstTokens := len(tail) * 200 // rough estimate
	availableBudget := maxContextTokens - tailEstTokens - compactionOverhead
	if availableBudget < 1000 {
		availableBudget = 1000
	}
	targetTokens := int(float64(originalTokens) * 0.3)
	maxTarget := availableBudget / 2 // leave room for tail + system prompt
	if targetTokens > maxTarget {
		targetTokens = maxTarget
	}
	if targetTokens < 500 {
		targetTokens = 500
	}
	targetRunes := int(float64(targetTokens) * 1.5) // tokens → chars (rough)

	prompt := compactionPrompt + fmt.Sprintf(`

## Output Length
Your output MUST be at most %d characters. Be concise — facts over narrative.

## Conversation History
`, targetRunes) + historyText.String() + `

Output the structured working state directly.`

	// Step 6: call engine.Run() — REUSE THE AGENT LOOP.
	// Instead of a custom client.Generate() loop, we build a minimal RunConfig
	// and let engine.Run() handle LLM calling, retry, streaming, and sanitization.
	// This eliminates the duplicated LLM call logic that the old compactMessages had.
	compactionMsgs := []llm.ChatMessage{
		llm.NewSystemMessage("You are a context compaction expert. Create a structured working state for task continuation. Stay under the specified length limit."),
		llm.NewUserMessage(prompt),
	}

	compressCfg := RunConfig{
		LLMClient:     client,
		Model:         model,
		Messages:      compactionMsgs,
		Tools:         tools.NewRegistry(), // empty — no tools for compression
		MaxIterations: 1,                   // single LLM call, no tool execution needed
		Stream:        false,
		ThinkingMode:  "",
		AgentID:       "compressor",
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"original_tokens":  originalTokens,
		"target_runes":     targetRunes,
		"to_compress":      len(toCompress),
		"tail_messages":    len(tail),
		"available_budget": availableBudget,
	}).Info("Context compaction: calling engine.Run() for LLM compression")

	output := Run(ctx, compressCfg)
	if output.Error != nil {
		return nil, fmt.Errorf("compaction engine.Run failed: %w", output.Error)
	}

	compressed := llm.StripThinkBlocks(output.Content)
	if compressed == "" {
		return nil, fmt.Errorf("compaction LLM produced no output")
	}

	// Step 7: build compacted message structure
	if len(systemMsgs) > 1 {
		log.Ctx(ctx).WithField("system_count", len(systemMsgs)).Error("assert: at most one system message in compact input")
		return nil, fmt.Errorf("compact: expected at most one system message, got %d", len(systemMsgs))
	}

	needInjectUserMsg := originalUserMsg != nil && tailStart > originalTailStart
	if needInjectUserMsg {
		log.Ctx(ctx).WithFields(log.Fields{
			"original_tail": originalTailStart,
			"capped_tail":   tailStart,
			"user_msg_idx":  originalTailStart,
		}).Info("Injecting original user message after tail capping")
	}

	llmView, sessionView := mergeCompressedResult(compressed, systemMsgs, tail, originalUserMsg, originalTailStart, tailStart)

	newTokens := len([]rune(compressed)) * 2 / 3
	log.Ctx(ctx).WithFields(map[string]any{
		"original_tokens":   originalTokens,
		"new_tokens":        newTokens,
		"tail_messages":     len(tail),
		"llm_prompt_tokens": output.LastPromptTokens,
		"llm_output_tokens": output.LastCompletionTokens,
	}).Info("Context compaction completed")

	return &CompressResult{
		LLMView:          llmView,
		SessionView:      sessionView,
		CompressedTokens: newTokens,
		InputTokens:      output.LastPromptTokens,
		OutputTokens:     output.LastCompletionTokens,
		LLMCalls:         1,
	}, nil
}

// offloadIDRe matches offload IDs (ol_xxxxxxxx) in message content markers.
var offloadIDRe = regexp.MustCompile(`offload:(ol_[a-f0-9]+)`)

// maskIDRe matches mask IDs (mk_xxxxxxxx) in message content markers.
var maskIDRe = regexp.MustCompile(`masked:(mk_[a-f0-9]+)`)

// bareOffloadIDRe matches bare offload IDs in tool call JSON arguments.
var bareOffloadIDRe = regexp.MustCompile(`"(ol_[a-f0-9]+)"`)

// bareMaskIDRe matches bare mask IDs in tool call JSON arguments.
var bareMaskIDRe = regexp.MustCompile(`"(mk_[a-f0-9]+)"`)

// extractMaskOffloadIDs scans messages for mask/offload ID references.
// Returns a set of referenced IDs that must NOT be cleaned during compression.
// This is the key mechanism that ensures compressed views can still recall
// original data — unlike Codex and Claude Code which permanently lose access
// to old tool outputs after compaction.
func extractMaskOffloadIDs(messages []llm.ChatMessage) map[string]bool {
	ids := make(map[string]bool)
	for _, msg := range messages {
		// Check for full markers (📂 [offload:ol_xxx]) in content
		for _, m := range offloadIDRe.FindAllStringSubmatch(msg.Content, -1) {
			ids[m[1]] = true
		}
		for _, m := range maskIDRe.FindAllStringSubmatch(msg.Content, -1) {
			ids[m[1]] = true
		}
		// Check for bare IDs in tool call arguments (e.g. {"id":"ol_xxx"})
		for _, tc := range msg.ToolCalls {
			for _, m := range bareOffloadIDRe.FindAllStringSubmatch(tc.Arguments, -1) {
				ids[m[1]] = true
			}
			for _, m := range bareMaskIDRe.FindAllStringSubmatch(tc.Arguments, -1) {
				ids[m[1]] = true
			}
		}
	}
	return ids
}

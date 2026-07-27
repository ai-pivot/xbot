package agent

import (
	"strings"

	"xbot/llm"
)

// reconstructIterationsFromMessages rebuilds IterationSnapshot[] from DB messages.
// Used by handleCancelledRun when out.IterationHistory is empty (e.g., after a
// server restart where the resumed Run hasn't completed any iterations).
//
// The pre-restart iterations are in the DB as assistant(tool_calls) + tool
// message pairs (from IncrementalPersist). This function converts them into
// IterationSnapshot format so the [interrupted] message's Detail has the full
// iteration history, not just the user_cancelled tool.
func reconstructIterationsFromMessages(msgs []llm.ChatMessage) []IterationSnapshot {
	if len(msgs) == 0 {
		return nil
	}

	// Find the start of the current turn (after the last user message).
	turnStart := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			turnStart = i + 1
			break
		}
	}

	// Build tool result map (tool_call_id → content) for status detection.
	toolResults := make(map[string]string)
	for _, m := range msgs[turnStart:] {
		if m.Role == "tool" && m.ToolCallID != "" {
			toolResults[m.ToolCallID] = m.Content
		}
	}

	var iters []IterationSnapshot
	iterIdx := 0
	var curTools []IterationToolSnapshot
	var curContent, curReasoning string

	flushIter := func() {
		if len(curTools) > 0 || curContent != "" || curReasoning != "" {
			iterIdx++
			iters = append(iters, IterationSnapshot{
				Iteration: iterIdx,
				Content:   curContent,
				Reasoning: curReasoning,
				Tools:     curTools,
			})
		}
		curTools = nil
		curContent = ""
		curReasoning = ""
	}

	for _, m := range msgs[turnStart:] {
		switch m.Role {
		case "tool":
			continue
		case "assistant":
			if len(m.ToolCalls) > 0 {
				flushIter()
				curContent = llm.StripThinkBlocks(m.Content)
				curReasoning = m.ReasoningContent
				for _, tc := range m.ToolCalls {
					status := "done"
					if content, ok := toolResults[tc.ID]; ok && strings.HasPrefix(content, "Error:") {
						status = "error"
					}
					curTools = append(curTools, IterationToolSnapshot{
						Name:   tc.Name,
						Label:  formatToolProgress(tc.Name, tc.Arguments),
						Status: status,
						Args:   tc.Arguments,
					})
				}
			} else if m.Content != "" {
				// Final reply (no tool_calls) — flush previous iteration, skip this message.
				flushIter()
			}
		}
	}
	flushIter()

	return iters
}

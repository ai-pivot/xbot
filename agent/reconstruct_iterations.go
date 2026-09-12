package agent

import (
	"strings"

	"xbot/llm"
	"xbot/tools"
)

// maxReconstructedToolPreview bounds the tool-result text carried into a
// reconstructed iteration snapshot for INJECTED notification tools (their
// result IS user-facing content: the notification/interjection text). Rune-safe
// truncation via tools.TruncateHeadPreview.
const maxReconstructedToolPreview = 2000

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

	// Find the start of the current turn. We key on TurnID rather than the
	// last user message because a resume turn (InjectInboundResume) and a
	// notification turn have NO user message of their own. The old
	// "scan back to the last user message" heuristic then pointed into the
	// PREVIOUS turn and rebuilt the wrong iterations (cross-turn content
	// bleed into iteration_history/Detail).
	var curTurnID uint64
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].TurnID > 0 {
			curTurnID = msgs[i].TurnID
			break
		}
	}
	if curTurnID == 0 {
		return nil
	}
	turnStart := 0
	for i := 0; i < len(msgs); i++ {
		if msgs[i].TurnID == curTurnID {
			turnStart = i
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
					result, hasResult := toolResults[tc.ID]
					if hasResult && strings.HasPrefix(result, "Error:") {
						status = "error"
					}
					snap := IterationToolSnapshot{
						Name:   tc.Name,
						Label:  formatToolProgress(tc.Name, tc.Arguments),
						Status: status,
						Args:   tc.Arguments,
					}
					// Injected notification tools: their tool RESULT is USER-FACING
					// content (the notification/interjection text, markdown). Other
					// tools carry their output in the unified `Detail` field; do the
					// same here so the web card can render it (as markdown) instead of
					// showing "no structured data". Without it the text sat in
					// session_messages but never reached the UI.
					if hasResult && tools.IsSyntheticToolName(tc.Name) {
						snap.Detail = tools.TruncateHeadPreview(result, maxReconstructedToolPreview)
					}
					curTools = append(curTools, snap)
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

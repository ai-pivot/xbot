package agent

import (
	"fmt"
	"strings"

	"xbot/llm"
	"xbot/tools"
)

// syntheticToolPrefixes are the tool names used by injectSyntheticToolPair
// (notifications injected INTO the LLM context as fake tool-call pairs).
// The LLM sees these names in its history and may MIMIC them — calling
// "background_task_result" as if it were a real tool. The executor must
// recognize these and return a friendly result instead of "unknown tool".
//
// Root cause: injectSyntheticToolPair writes an assistant message with
// ToolCalls: [{Name: "background_task_result"}] into the conversation.
// The model treats the history as examples of its own behavior and copies
// the tool name on subsequent turns. The executor then fails lookup because
// the tool was never registered — producing the user-visible
// "unknown tool: background_task_result" error.
var syntheticToolPrefixes = []string{
	"background_task_result", // bg task completion notification
	"bg_subagent_",           // subagent notification (bg_subagent_completed etc.)
	"cron_fired",             // cron trigger notification
	"delivered_message",      // queued user message delivery confirmation
	"pre_turn_end",           // PreTurnEnd hook injection
	"user_cancelled",         // cancel marker
	"loop_detected",          // loop breaker fake tool result
	"ask_user",               // AskUser fake tool result
}

// isSyntheticToolName reports whether the given tool name is one of the
// fake names the system injects as notification tool-call pairs.
func isSyntheticToolName(name string) bool {
	for _, p := range syntheticToolPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// syntheticToolResult returns a friendly tool result for a mimic-call of a
// synthetic notification tool. Instead of an "unknown tool" error (which the
// model cannot act on), this result tells the model exactly what happened
// and what to do — keeping the loop running.
func syntheticToolResult(tc llm.ToolCall) (*tools.ToolResult, error) {
	msg := fmt.Sprintf(
		"This is a system notification channel (%s), not a callable tool. "+
			"It appears in conversation history when the system injects background "+
			"task results, cron triggers, or other asynchronous notifications. "+
			"Do NOT call this tool — continue with your actual task using the real tools available to you.",
		tc.Name,
	)
	return &tools.ToolResult{
		Summary: msg,
		IsError: false,
	}, nil
}

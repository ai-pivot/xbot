package agent

import (
	"fmt"

	"xbot/llm"
	"xbot/tools"
)

// The canonical name list lives in tools.SyntheticToolPrefixes (tools/synthetic_hints.go)
// and is shared with the web-facing history reconstruction.
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
// isSyntheticToolName reports whether the given tool name is one of the
// fake names the system injects as notification tool-call pairs.
// Thin wrapper over tools.IsSyntheticToolName — SINGLE SOURCE OF TRUTH lives in
// tools/synthetic_hints.go (shared with the web-facing history reconstruction).
func isSyntheticToolName(name string) bool {
	return tools.IsSyntheticToolName(name)
}

// syntheticToolResult returns a friendly tool result for a mimic-call of a
// synthetic notification tool. Instead of an "unknown tool" error (which the
// model cannot act on), this result tells the model exactly what happened —
// keeping the loop running.
//
// ⚠️ 2026-10-14 用户定向：「去掉多余的关于不要调用的提示」。旧文案结尾带一条命令式指令
// （"Do NOT call this tool — continue with your actual task using the real tools available to
// you."）—— 陈述清楚"这是系统自动注入的通知通道、不是可调用工具"之后，命令是多余的：
// 它挤占指令位，还强化"我调错工具了"这个错误关注点。⇒ 只保留事实（含 AUTO-INJECTS 强调）。
func syntheticToolResult(tc llm.ToolCall) (*tools.ToolResult, error) {
	msg := fmt.Sprintf(
		"This is a system notification channel (%s), not a callable tool: the xbot runtime "+
			"AUTO-INJECTS it into the conversation history to carry background task results, "+
			"cron triggers, and other asynchronous notifications. Treat it as history, not as "+
			"something you invoked.",
		tc.Name,
	)
	return &tools.ToolResult{
		Summary: msg,
		IsError: false,
	}, nil
}

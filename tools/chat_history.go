package tools

import (
	"fmt"
	"strings"
	"time"

	"xbot/llm"
)

// ---- ChatHistoryTool: 让 LLM 回看当前会话的近期消息 ----
//
// 数据源 = session_messages（DB，会话历史的**唯一权威**），经 SessionService.Replay
// 读出"当前视图"（mask/压缩/回溯都已生效）。本工具**不持有任何副本**：回溯
// （RewindToHistoryID）或清空（Clear）截断 DB 之后，这里立刻读不到被截断的消息。
//
// ⚠️ 曾经的实现维护了一份进程内 ring（每条入站消息 Add 一份），回溯/清空都不清它 ——
// 用户回溯后模型仍能用本工具读到回溯前的消息（用户实测复现）。会话内容的第二份
// 副本 = 必漏，故删除，改由 DB 单一权威承担。

// ChatHistoryTool 会话历史查询工具（无状态）。
type ChatHistoryTool struct{}

func NewChatHistoryTool() *ChatHistoryTool {
	return &ChatHistoryTool{}
}

func (t *ChatHistoryTool) Name() string {
	return "ChatHistory"
}

func (t *ChatHistoryTool) Description() string {
	return `Query the recent message history of the current conversation.

Use this tool SPARINGLY — only when you genuinely cannot recall what was recently discussed and it is NOT available in the conversation context (e.g., after a page refresh with no compacted summary). It reads the same authoritative session history the conversation itself uses, so anything already rewound or cleared is likewise gone here. Do NOT use this tool after /compress — the compacted context summary already contains the relevant history. Do NOT use this tool as a routine first step before responding.

Parameters (JSON):
  - limit: integer, optional, number of recent messages to retrieve (defaults to 10, max 50)
Example: {"limit": 10}`
}

func (t *ChatHistoryTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "limit", Type: "integer", Description: "Number of recent messages to retrieve (defaults to 10, max 50)", Required: false},
	}
}

type chatHistoryParams struct {
	Limit int `json:"limit"`
}

func (t *ChatHistoryTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[chatHistoryParams](input)
	if err != nil {
		return nil, err
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	if ctx.SessionSvc == nil || ctx.TenantID == 0 {
		return NewResult("No active conversation context."), nil
	}

	// Replay 读出当前权威视图（mask/压缩/回溯已生效）——与前端/引擎看到的是同一份。
	replay, err := ctx.SessionSvc.Replay(ctx.TenantID)
	if err != nil {
		return nil, fmt.Errorf("read session history: %w", err)
	}

	lines := make([]string, 0, limit)
	for _, m := range replay.Messages {
		// 只回对话消息：tool/system 行是过程噪声，模型的上下文里本来也有。
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		lines = append(lines, formatChatHistoryLine(m))
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	if len(lines) == 0 {
		return NewResult("No recent message history found."), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Recent %d messages in this conversation:\n\n", len(lines))
	for i, line := range lines {
		fmt.Fprintf(&sb, "[%d] %s\n", i+1, line)
	}
	return NewResult(sb.String()), nil
}

// formatChatHistoryLine 渲染一行 `HH:MM <role>: content`（超过 24h 带日期）。
func formatChatHistoryLine(m llm.ChatMessage) string {
	timeStr := ""
	if !m.Timestamp.IsZero() {
		timeStr = m.Timestamp.Format("15:04")
		if time.Since(m.Timestamp) > 24*time.Hour {
			timeStr = m.Timestamp.Format("01/02 15:04")
		}
	}
	if timeStr == "" {
		return fmt.Sprintf("<%s>: %s", m.Role, m.Content)
	}
	return fmt.Sprintf("%s <%s>: %s", timeStr, m.Role, m.Content)
}

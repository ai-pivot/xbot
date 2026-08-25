package agent

import (
	"context"
	"testing"

	"xbot/llm"
	"xbot/tools"
)

// TestRunAskUserAfterToolIteration 复现线上错误：
//
//	append session history: persist AskUser question: no pending messages
//
// 根因：system reminder 以 fake tool pair 的形式被注入到 s.messages 尾部，
// 但持久化时 pendingMessages 会跳过它。这破坏了 lastPersistedCount 这个
// 「已持久化消息数量」watermark 与「messages 数组索引」之间的对应关系：
// commitPending 用 len(messages) 推进 watermark，把被跳过的 reminder 也
// 算进了「已持久化」，导致 watermark 虚高。下一轮 removeSystemReminderTool
// 删除 reminder 后真实消息索引前移，落到虚高 watermark 之下，被
// pendingMessages 当作「已持久化」跳过 —— 于是 AskUser 的 assistant+tool
// 消息根本没进 pending，报「no pending messages」。
//
// 复现路径：第一轮 Shell（触发 reminder 注入 + IncrementalPersist），
// 第二轮 AskUser（触发 waitingUser + IncrementalPersistAndAskQuestion）。
func TestRunAskUserAfterToolIteration(t *testing.T) {
	_, sess := newAgentHistorySession(t)
	userID, err := sess.AppendMessage(llm.NewUserMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockLLM{responses: []llm.LLMResponse{
		// 迭代 1：普通工具，触发 reminder 注入 + IncrementalPersist
		{
			FinishReason: llm.FinishReasonToolCalls,
			ToolCalls:    []llm.ToolCall{{ID: "shell-1", Name: "Shell", Arguments: `{}`}},
		},
		// 迭代 2：AskUser，触发 waitingUser + IncrementalPersistAndAskQuestion
		{
			FinishReason: llm.FinishReasonToolCalls,
			ToolCalls:    []llm.ToolCall{{ID: "ask-1", Name: "AskUser", Arguments: `{}`}},
		},
	}}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock, Model: "test", Session: sess, AgentID: "main",
		Tools: newTestRegistry(&mockTool{name: "AskUser"}),
		Messages: []llm.ChatMessage{
			llm.NewSystemMessage("system"), {ID: userID, Role: "user", Content: "hello"},
		},
		ToolExecutor: func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
			if tc.Name == "AskUser" {
				return &tools.ToolResult{Summary: "waiting", WaitingUser: true, Metadata: map[string]string{"request_id": "r1"}}, nil
			}
			return &tools.ToolResult{Summary: "shell done"}, nil
		},
	})

	if out.Error != nil {
		t.Fatalf("Run error: %v", out.Error)
	}
	if !out.WaitingUser {
		t.Fatalf("expected WaitingUser, got %+v", out)
	}

	// AskUser 的 assistant + tool 消息必须已持久化。
	active, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	var askToolPersisted bool
	for _, m := range active {
		if m.Role == "tool" && m.ToolName == "AskUser" {
			askToolPersisted = true
		}
	}
	if !askToolPersisted {
		t.Fatalf("AskUser tool message was not persisted: %+v", active)
	}
}

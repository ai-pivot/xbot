package agent

// 复现 + 回归：压缩失败（自动触发 / 显式 compact_context）绝不能终止用户的 turn。
//
// 现状（RED）——压缩链路上的任何 error 都被外层循环当成致命错误：
//
//	agent/engine.go:718        maybeCompress 返回 err
//	                           → out.Error = "persist context compression: ..." → Run 直接 return（turn 死）
//	agent/engine_run.go:1086   context_window_exceeded 的 Phase 1 压缩失败 → 同样 return（turn 死）
//	agent/engine_run.go:868    handleInputTooLong 的压缩失败 → 冒泡成 LLM 错误 → turn 死
//
// 而这些 error 的真实来源（agent/compress.go）都是**可恢复的瞬时故障**：
//
//	804: "compaction engine.Run failed: %w"  ← 压缩用的那次 LLM 调用失败（网关 5xx / 超时）
//	379: "append compression history: %w"    ← 持久化失败（fail-closed 只需不安装压缩视图）
//
// 正确语义：压缩失败 = 降级（丢弃压缩结果、保留原上下文继续跑，并给出可见告警），
// 而不是"用户的 turn 到此结束"。上下文真超限时后面还有 aggressiveTruncate /
// handleInputTooLong 两条兜底路径。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"xbot/llm"
	"xbot/tools"
)

// compressFailCM 返回一个"压缩必失败"的 ContextManager，错误文本与
// compress.go:804 的真实形态一致（压缩本体的内层 engine.Run 失败）。
func compressFailCM(calls *int) *mockContextManager {
	return &mockContextManager{
		compressFn: func(_ context.Context, _ []llm.ChatMessage, _ llm.LLM, _ string, _ int64) (*CompressResult, error) {
			*calls++
			return nil, errors.New("compaction engine.Run failed: transient gateway 502")
		},
		manualCompressFn: func(_ context.Context, _ []llm.ChatMessage, _ llm.LLM, _ string, _ int64) (*CompressResult, error) {
			*calls++
			return nil, errors.New("compaction engine.Run failed: transient gateway 502")
		},
	}
}

// compressTriggerCfg 构造"下一迭代必触发自动压缩"的配置：
// promptBudget = 1000-100 = 900，阈值 0.9 → 810；mock LLM 报告 950 prompt tokens。
func compressTriggerCfg(mock *mockLLM, cm ContextManager, msgs []llm.ChatMessage, tools ...*mockTool) RunConfig {
	return RunConfig{
		LLMClient:     mock,
		Model:         "test-model",
		Tools:         newTestRegistry(tools...),
		Messages:      msgs,
		AgentID:       "main",
		Channel:       "test",
		ChatID:        "chat-compress-fail",
		MaxIterations: 6,

		ContextManager:       cm,
		ContextManagerConfig: &ContextManagerConfig{MaxContextTokens: 1000, CompressionThreshold: 0.9},
		MaxOutputTokens:      100,

		SaveTokenState:    func(_, _ int64) {},
		SaveContextTokens: func(int64) {},
	}
}

func compressScenarioMessages() []llm.ChatMessage {
	return []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
		llm.NewUserMessage("u1"),
		llm.NewAssistantMessage("a1"),
		llm.NewUserMessage("u2"),
	}
}

// bigMessages 构造 n 条（含 system）普通消息，用于绕过 maybeCompress 的
// len(messages) <= 3 早退。
func bigMessages(n int) []llm.ChatMessage {
	msgs := []llm.ChatMessage{llm.NewSystemMessage("You are a test agent.")}
	for i := 1; i < n; i++ {
		if i%2 == 1 {
			msgs = append(msgs, llm.NewUserMessage("u"))
		} else {
			msgs = append(msgs, llm.NewAssistantMessage("a"))
		}
	}
	return msgs
}

// Test 1（单元）：maybeCompress 遇到压缩失败必须**降级返回 nil**（循环继续），
// 并且给出可见告警；绝不能把错误抛给调用方去终止 turn。
func TestMaybeCompress_FailureDegradesInsteadOfAborting(t *testing.T) {
	calls := 0
	s := newCompressLoopState(t, compressFailCM(&calls), bigMessages(8), 195_000)

	err := s.maybeCompress(context.Background())
	if err != nil {
		t.Fatalf("maybeCompress returned error %v — 压缩失败被当成致命错误（会终止 turn）", err)
	}
	if calls == 0 {
		t.Fatal("precondition failed: compression was never attempted (test does not exercise the path)")
	}
	if !strings.Contains(s.compressWarning, "压缩") {
		t.Fatalf("compressWarning = %q, want a visible warning telling the user compression failed", s.compressWarning)
	}
}

// Test 2（端到端）：自动压缩失败时，用户的 turn 必须继续跑完并给出最终回答。
func TestRun_AutoCompressFailure_DoesNotAbortTurn(t *testing.T) {
	calls := 0
	probe := &mockTool{name: "probe", result: &tools.ToolResult{Summary: "probe ok"}}
	mock := &mockLLM{responses: []llm.LLMResponse{
		// 迭代 1：真实 prompt_tokens 950 > 阈值 810；带工具调用 → 循环继续
		{
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: "probe", Arguments: "{}"}},
			Usage:     llm.TokenUsage{PromptTokens: 950, CompletionTokens: 5},
		},
		// 迭代 2：maybeCompress 触发自动压缩（失败）→ 必须继续到这条最终回答
		{Content: "task done", Usage: llm.TokenUsage{PromptTokens: 950, CompletionTokens: 5}},
	}}

	out := Run(context.Background(), compressTriggerCfg(mock, compressFailCM(&calls), compressScenarioMessages(), probe))

	if out.Error != nil {
		t.Fatalf("压缩失败终止了 turn: %v（期望：降级继续，返回最终回答）", out.Error)
	}
	if out.Content != "task done" {
		t.Fatalf("content = %q, want %q", out.Content, "task done")
	}
	if calls == 0 {
		t.Fatal("precondition failed: auto compression never triggered")
	}
}

// Test 3（端到端）：模型显式调用 compact_context（CompactRequested）后压缩失败，
// 同样必须降级继续，而不是终止 turn。
func TestRun_ExplicitCompactFailure_DoesNotAbortTurn(t *testing.T) {
	calls := 0
	compactTool := &mockTool{
		name:   "compact_context",
		result: &tools.ToolResult{Summary: "compaction requested", CompactRequested: true},
	}
	mock := &mockLLM{responses: []llm.LLMResponse{
		{
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: "compact_context", Arguments: "{}"}},
			Usage:     llm.TokenUsage{PromptTokens: 500, CompletionTokens: 5}, // 未超阈值 → 只因显式请求而压缩
		},
		{Content: "kept working", Usage: llm.TokenUsage{PromptTokens: 500, CompletionTokens: 5}},
	}}

	out := Run(context.Background(), compressTriggerCfg(mock, compressFailCM(&calls), compressScenarioMessages(), compactTool))

	if out.Error != nil {
		t.Fatalf("显式压缩失败终止了 turn: %v（期望：降级继续）", out.Error)
	}
	if out.Content != "kept working" {
		t.Fatalf("content = %q, want %q", out.Content, "kept working")
	}
	if calls == 0 {
		t.Fatal("precondition failed: explicit compaction never triggered")
	}
}

// Test 4（端到端）：context_window_exceeded 时压缩失败 → 必须落到
// aggressiveTruncate 兜底并继续，而不是把 turn 打死。
func TestRun_ContextWindowExceeded_CompressFailureFallsBackToTruncate(t *testing.T) {
	calls := 0
	msgs := []llm.ChatMessage{llm.NewSystemMessage("sys")}
	for i := 0; i < 8; i++ {
		msgs = append(msgs, llm.NewUserMessage("u"), llm.NewAssistantMessage("a"))
	}
	mock := &mockLLM{responses: []llm.LLMResponse{
		// 模型返回"上下文超限"→ Phase 1 压缩（失败）→ Phase 2 截断 → 重试
		{FinishReason: llm.FinishReasonContextWindowExceeded, Usage: llm.TokenUsage{PromptTokens: 990}},
		// 截断后的重试：正常最终回答
		{Content: "recovered", Usage: llm.TokenUsage{PromptTokens: 300, CompletionTokens: 5}},
	}}

	out := Run(context.Background(), compressTriggerCfg(mock, compressFailCM(&calls), msgs))

	if out.Error != nil {
		t.Fatalf("context_window_exceeded 下的压缩失败终止了 turn: %v（期望：截断兜底后继续）", out.Error)
	}
	if out.Content != "recovered" {
		t.Fatalf("content = %q, want %q", out.Content, "recovered")
	}
	if calls == 0 {
		t.Fatal("precondition failed: forced compression never triggered")
	}
}

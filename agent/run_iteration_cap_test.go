package agent

// run_iteration_cap_test —— P1-2 的判据：**内部一次性 Run 必须保证终止**。
//
// 病根（本 PR 之前删除 max_iterations 时一并删掉了压缩侧的 `MaxIterations: 1`）：
// 上下文压缩（agent/compress.go 的 compactMessages）用 **空工具注册表**跑一次摘要
// 调用。若模型幻觉出 tool_calls（历史里全是工具对的会话很常见），引擎的
// defaultToolExecutor 会把 "unknown tool: X" 当成**工具结果**回喂给模型并继续下一轮
// ⇒ 没有上限就永不返回 ⇒ 压缩把整个 turn 拖到 ctx deadline（用户看到"卡在压缩"）。
//
// 判据分两层：
//  1. 行为：RunConfig.MaxIterations=N 必须**恰好**放行 N 次 LLM 调用，第 N+1 次之前
//     返回明确错误（不是空转、不是静默吞掉）；且只吐 tool_calls 的模型不会把内容
//     冒充成摘要（lastAssistantContent 为空 ⇒ 压缩侧判"没有产出"）。
//  2. wire：`compressRunConfig(...)` 的返回值必须 **MaxIterations == 1**
//     （纯函数直接断言；不再扫源码字符串 —— 那在重命名/格式化后会假红假绿）。

import (
	"context"
	"strings"
	"testing"

	"xbot/llm"
	"xbot/tools"
)

// toolCallOnlyLLM 永远只回 tool_calls（模拟幻觉），可记录调用次数。
type toolCallOnlyLLM struct {
	calls int
}

func (m *toolCallOnlyLLM) Generate(_ context.Context, _ string, _ []llm.ChatMessage, _ []llm.ToolDefinition, _ string) (*llm.LLMResponse, error) {
	m.calls++
	return &llm.LLMResponse{
		FinishReason: llm.FinishReasonToolCalls,
		ToolCalls:    []llm.ToolCall{{ID: "tc", Name: "NotRegistered", Arguments: `{}`}},
	}, nil
}

func (m *toolCallOnlyLLM) ListModels() []string { return []string{"test-model"} }

// 一次性上限：cap=1 ⇒ 只允许 1 次 LLM 调用，随后返回明确错误（绝不空转到 ctx 超时）。
func TestRun_InternalIterationCapTerminatesRunawayToolLoop(t *testing.T) {
	mock := &toolCallOnlyLLM{}
	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test-model",
		Tools:     tools.NewRegistry(), // 压缩姿态：空工具注册表
		Messages:  baseMessages(),
		AgentID:   "compressor",
		Channel:   "test",
		ChatID:    "chat-cap-1",
		// 无上限时本测试会挂死（引擎无限循环）—— 这正是修复前的生产形态。
		MaxIterations: 1,
	})
	if out == nil {
		t.Fatal("Run returned nil")
	}
	if mock.calls != 1 {
		t.Fatalf("cap=1 必须恰好放行 1 次 LLM 调用，got %d（>1 = 上限没挡在调用之前）", mock.calls)
	}
	if out.Error == nil || !strings.Contains(out.Error.Error(), "iteration cap") {
		t.Fatalf("触顶必须返回明确错误（压缩侧据此走非致命降级），got %v", out.Error)
	}
	// ⛔ 内容不得是"上一轮的旧 assistant 文本"：只有 tool_calls 时摘要为空。
	if out.Content != "" {
		t.Fatalf("只有 tool_calls 的一次性 Run 不得产出内容（否则会被当成摘要写进上下文），got %q", out.Content)
	}
}

// 上限语义是"允许 N 次迭代"：cap=2 ⇒ 恰好 2 次调用，第 3 次之前收尾。
func TestRun_InternalIterationCapAllowsExactlyN(t *testing.T) {
	responses := []llm.LLMResponse{
		{FinishReason: llm.FinishReasonToolCalls, ToolCalls: []llm.ToolCall{{ID: "t1", Name: "NotRegistered", Arguments: `{}`}}},
		{FinishReason: llm.FinishReasonToolCalls, ToolCalls: []llm.ToolCall{{ID: "t2", Name: "NotRegistered", Arguments: `{}`}}},
		{Content: "should never be requested", FinishReason: llm.FinishReasonStop},
	}
	mock := &mockLLM{responses: responses}
	out := Run(context.Background(), RunConfig{
		LLMClient:     mock,
		Model:         "test-model",
		Tools:         tools.NewRegistry(),
		Messages:      baseMessages(),
		AgentID:       "compressor",
		Channel:       "test",
		ChatID:        "chat-cap-2",
		MaxIterations: 2,
	})
	if mock.callCount != 2 {
		t.Fatalf("cap=2 必须恰好 2 次 LLM 调用，got %d", mock.callCount)
	}
	if out.Error == nil {
		t.Fatal("触顶必须返回错误")
	}
}

// 主对话（cap=0）不受影响：正常回答照常返回、无错误 —— 与 TestRun_NoIterationCap
// 同族，但这里钉的是"0 = 不限"这个默认值本身（防止有人把 0 解释成"0 次迭代"）。
func TestRun_ZeroCapMeansUnlimited(t *testing.T) {
	mock := &mockLLM{responses: []llm.LLMResponse{
		{FinishReason: llm.FinishReasonToolCalls, ToolCalls: []llm.ToolCall{{ID: "tc1", Name: "Shell", Arguments: `{}`}}},
		{Content: "done", FinishReason: llm.FinishReasonStop},
	}}
	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test-model",
		Tools:     newTestRegistry(),
		Messages:  baseMessages(),
		AgentID:   "main",
		Channel:   "test",
		ChatID:    "chat-cap-0",
		ToolExecutor: func(_ context.Context, _ llm.ToolCall) (*tools.ToolResult, error) {
			return tools.NewResult("ok"), nil
		},
	})
	if out.Error != nil {
		t.Fatalf("cap=0（默认）不得触发任何上限：%v", out.Error)
	}
	if out.Content != "done" || mock.callCount != 2 {
		t.Fatalf("cap=0 必须跑到底：content=%q calls=%d", out.Content, mock.callCount)
	}
}

// ── wire：压缩路径必须显式带上一次性上限 ─────────────────────────────────────
//
// 行为判据只证明"引擎支持上限"；压缩是否**用了**它，只有调用点能证明。
// 判别力自证见 TestCompressCfgBounded_RejectsMutatedLiteral。

// TestCompressRunIsExplicitlyBounded 钉住压缩 Run 的轮次上限。
//
// 2026-10-28 重构（C1，门下省审计）：原实现扫 compress.go 源码里的
// `compressCfg := RunConfig{` 字面量文本 —— 源码字符串判据在重命名、格式化、
// 挪动调用点后会假红/假绿，不是有效守卫。现在压缩配置由
// `compressRunConfig(...)` 纯函数产出，这里**直接断言返回值**。
func TestCompressRunIsExplicitlyBounded(t *testing.T) {
	cfg := compressRunConfig(
		&mockLLM{}, "test-model",
		[]llm.ChatMessage{llm.NewSystemMessage("sys"), llm.NewUserMessage("summarize")},
		1024)
	if cfg.MaxIterations != 1 {
		t.Fatalf("⛔ 压缩 Run 的 MaxIterations = %d, want 1 —— 空工具集 + 幻觉 tool_calls 会无限循环至 ctx deadline",
			cfg.MaxIterations)
	}
	if len(cfg.Tools.AsDefinitionsForSession("", 0)) != 0 {
		t.Fatalf("压缩 Run 必须使用空工具注册表（got %d 个工具定义）", len(cfg.Tools.AsDefinitionsForSession("", 0)))
	}
}

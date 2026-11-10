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
//  2. wire：agent/compress.go 的 compressCfg 字面量必须**显式**传 MaxIterations: 1
//     （删掉这行 ⇒ 上面那条行为判据就再也守不住压缩路径；故单独钉调用点）。

import (
	"context"
	"os"
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

// compressCfgLiteral 截取 compress.go 里 compressCfg 的 RunConfig 字面量。
func compressCfgLiteral(t *testing.T, src string) string {
	t.Helper()
	const anchor = "compressCfg := RunConfig{"
	i := strings.Index(src, anchor)
	if i < 0 {
		t.Fatalf("wire: compress.go 里找不到 `%s`（压缩路径被重构？请同步本判据）", anchor)
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n\t}"); j >= 0 {
		return rest[:j]
	}
	t.Fatalf("wire: 无法定位 compressCfg 字面量结尾")
	return ""
}

func checkCompressCfgBounded(literal string) error {
	if !strings.Contains(literal, "MaxIterations: 1") {
		return errCompressUnbounded
	}
	return nil
}

type errCompressUnboundedType struct{}

func (errCompressUnboundedType) Error() string {
	return "压缩 Run 未显式设置 MaxIterations: 1 —— 空工具集 + 幻觉 tool_calls 会无限循环至 ctx deadline"
}

var errCompressUnbounded = errCompressUnboundedType{}

func TestCompressRunIsExplicitlyBounded(t *testing.T) {
	src, err := os.ReadFile("compress.go")
	if err != nil {
		t.Fatalf("read compress.go: %v", err)
	}
	if err := checkCompressCfgBounded(compressCfgLiteral(t, string(src))); err != nil {
		t.Fatalf("⛔ %v", err)
	}
}

func TestCompressCfgBounded_RejectsMutatedLiteral(t *testing.T) {
	src, err := os.ReadFile("compress.go")
	if err != nil {
		t.Fatalf("read compress.go: %v", err)
	}
	literal := compressCfgLiteral(t, string(src))
	if err := checkCompressCfgBounded(literal); err != nil {
		t.Fatalf("前置：当前压缩字面量必须已合规：%v", err)
	}
	mutated := strings.Replace(literal, "MaxIterations: 1,", "", 1)
	if mutated == literal {
		t.Fatal("变异未生效（锚点串变了？请同步更新本测试）")
	}
	if err := checkCompressCfgBounded(mutated); err == nil {
		t.Fatal("⛔ 删掉 MaxIterations: 1 后判据仍然通过 —— 本判据没有判别力")
	}
}

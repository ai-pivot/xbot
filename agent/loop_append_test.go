package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"xbot/llm"
	"xbot/tools"
)

// TestLoopIterationAppendIntegrity 复现 turn 13 LOOP 事故：验证 LOOP 拦截路径
// append 的消息对（assistant + toolMsg）是否完整进入 s.messages。
//
// 事故数据链（用户日志分析）：
//
//	iter19 请求 743 条/301215 tokens → LOOP 拦截 →
//	iter20 请求 745 条/300880 tokens（-335：新增 2 条但 0 token 贡献）
//	→ LOOP 对是零 token 空壳 → 模型看不到任何新迭代信息 → 无限重复
func TestLoopIterationAppendIntegrity(t *testing.T) {
	cfg := RunConfig{
		Channel: "web",
		ChatID:  "chat-test",
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "do the edit"},
		},
		ToolExecutor: func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
			return &tools.ToolResult{Summary: "Successfully replaced 1 occurrence(s) in /a.ts"}, nil
		},
		TurnID: 13,
	}
	s := newRunState(cfg)
	s.initDynamicInjector() // postToolProcessing 依赖（InjectIfNeeded）
	ctx := context.Background()

	frCall := func(id string) []llm.ToolCall {
		return []llm.ToolCall{{
			ID:        id,
			Name:      "FileReplace",
			Arguments: `{"path":"/a.ts","old_string":"export type TabType = 'agent'","new_string":"export type TabType = 'agent' | 'diff'"}`,
		}}
	}

	// ---- 迭代 A：正常执行（成功 FileReplace）+ postToolProcessing（reminder 注入）----
	respA := &llm.LLMResponse{Content: "", ToolCalls: frCall("call_a")}
	s.recordAssistantMsg(ctx, respA)
	resA := s.executeToolCalls(ctx, respA, 1)
	s.processToolResults(ctx, respA, resA)
	s.postToolProcessing(ctx, respA, 1)

	base := len(removeSystemReminderTool(s.messages))
	// A 对完整性（先剥离 transient reminder，只校验真实 tool 对）
	cleanA := removeSystemReminderTool(s.messages)
	if cleanA[base-2].Role != "assistant" || len(cleanA[base-2].ToolCalls) != 1 {
		t.Fatalf("iter A assistant 不完整: role=%v toolCalls=%d", cleanA[base-2].Role, len(cleanA[base-2].ToolCalls))
	}
	if cleanA[base-1].Role != "tool" || !strings.Contains(cleanA[base-1].Content, "Successfully replaced") {
		t.Fatalf("iter A toolMsg 不完整: %q", cleanA[base-1].Content[:min(60, len(cleanA[base-1].Content))])
	}
	// reminder 现在以独立 fake tool pair 存在，不再混入真实 tool content
	if len(s.messages) != base+2 {
		t.Fatalf("iter A 未追加 transient reminder pair：len=%d（期望干净消息 %d + 2）", len(s.messages), base)
	}
	t.Logf("iter A: 真实消息=%d，含 reminder pair=%d", base, len(s.messages)-base)

	// ---- 迭代 B：完全相同的调用 → LOOP 拦截 + postToolProcessing ----
	respB := &llm.LLMResponse{Content: "", ToolCalls: frCall("call_b")}
	s.recordAssistantMsg(ctx, respB) // 尾部调 detectIterationLoop → loopDetected=true
	if !s.loopDetected {
		t.Fatalf("预期 detectIterationLoop 触发（签名相同 + 上轮无 error）")
	}
	resB := s.executeToolCalls(ctx, respB, 2) // → fakeLoopToolResults
	s.processToolResults(ctx, respB, resB)
	s.postToolProcessing(ctx, respB, 2) // reminder strip/inject

	// ---- 核心断言：LOOP 对（B）必须完整进入 s.messages ----
	// reminder 现在是独立 fake tool pair（末尾 2 条），剥离后再定位真实 LOOP 对。
	clean := removeSystemReminderTool(s.messages)
	if len(clean) != base+2 {
		t.Fatalf("LOOP 对未 append：干净消息 len=%d（期望 %d）", len(clean), base+2)
	}
	loopAssistant := clean[len(clean)-2]
	loopTool := clean[len(clean)-1]

	if loopAssistant.Role != "assistant" {
		t.Fatalf("LOOP assistant role=%v", loopAssistant.Role)
	}
	// 空壳检测①：assistant 的 ToolCalls 必须非空（FileReplace 调用）
	if len(loopAssistant.ToolCalls) == 0 {
		t.Fatalf("🔴 LOOP assistant 的 ToolCalls 为空——零 token 空壳！模型看不到自己的调用")
	}
	if loopAssistant.ToolCalls[0].Name != "FileReplace" {
		t.Fatalf("LOOP assistant tool name=%v", loopAssistant.ToolCalls[0].Name)
	}
	// 空壳检测②：toolMsg 必须含 LOOP 警告
	if loopTool.Role != "tool" {
		t.Fatalf("LOOP toolMsg role=%v", loopTool.Role)
	}
	if !strings.Contains(loopTool.Content, "LOOP DETECTED") {
		t.Fatalf("🔴 LOOP toolMsg 内容不含警告（len=%d）——空壳！模型看不到拦截反馈", len(loopTool.Content))
	}
	// tool_call_id 配对（SanitizeMessages 不剔的前提）
	if loopTool.ToolCallID != "call_b" {
		t.Fatalf("LOOP toolMsg id=%v", loopTool.ToolCallID)
	}
	paired := false
	for _, tc := range loopAssistant.ToolCalls {
		if tc.ID == loopTool.ToolCallID {
			paired = true
		}
	}
	if !paired {
		t.Fatalf("LOOP 对 id 配对断裂：tool=%v 不在 assistant 的 tool_calls 里", loopTool.ToolCallID)
	}

	// ---- 再走一轮 SanitizeMessages（callLLM 第一行）确认不剔除 ----
	// reminder pair 也必须合法（assistant tool_call + 匹配 tool），不能被 Sanitize 意外打断
	sanitized := llm.SanitizeMessages(s.messages)
	if len(sanitized) != len(s.messages) {
		t.Fatalf("SanitizeMessages 剔除了 LOOP/reminder 消息（in=%d out=%d）", len(s.messages), len(sanitized))
	}

	// ---- 连续 LOOP 到上限 → loopFatal 强制终止（turn-13 事故：19 次空转）----
	// 迭代 B 已拦截 1 次（loopBreakCount=1），再跑 4 次达到 maxLoopBreaks=5
	for i := 3; i <= maxLoopBreaks+1; i++ {
		resp := &llm.LLMResponse{Content: "", ToolCalls: frCall(fmt.Sprintf("call_%d", i))}
		s.recordAssistantMsg(ctx, resp)
		res := s.executeToolCalls(ctx, resp, i)
		s.processToolResults(ctx, resp, res)
	}
	if !s.loopFatal {
		t.Fatalf("连续 %d 次 LOOP 拦截后 loopFatal 未置位——Run 不会终止（turn-13 同款死锁）", maxLoopBreaks)
	}
	if s.loopBreakCount != maxLoopBreaks {
		t.Fatalf("loopBreakCount=%d（期望 %d）", s.loopBreakCount, maxLoopBreaks)
	}

	// ---- 正常执行一次 → 计数重置 ----
	respOK := &llm.LLMResponse{Content: "done", ToolCalls: frCall("call_ok")}
	s.recordAssistantMsg(ctx, respOK)
	resOK := s.executeToolCalls(ctx, respOK, maxLoopBreaks+1)
	s.processToolResults(ctx, respOK, resOK)
	if s.loopBreakCount != 0 {
		t.Fatalf("正常执行后 loopBreakCount 未重置（=%d）", s.loopBreakCount)
	}
}

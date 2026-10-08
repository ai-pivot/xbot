package channel

// 2026-10-02 P0（用户报告）：「subagent done / bgtask done 的内容，前端渲染显示
// 历史消息不含结构化详情，刷新后详情消失」。
//
// 根因：agent 侧 IterationToolSnapshot.ToolHints 以 `tool_hints` 键落库（Fix A 回写
// 完整快照），但 channel 侧 tools JSON 的两个解析结构（region_view.go 的
// regionToolSnap —— MapIterationRecord/parseRegionTools 唯一解析入口，覆盖 REST 三条
// 路径；subscription.go 的 iterToolSnap —— legacy Detail JSON 路径）都没有该字段 ⇒
// encoding/json 静默丢弃 ⇒ 历史载荷 tool_hints 恒空 ⇒ 前端 SyntheticToolCard 的
// 结构化卡片（subagent role/instance/task、bg_task task/output 全靠 SyntheticToolHints
// JSON 载荷）退化为纯文本 fallback。live 渲染走 SSE 直推（protocol.ToolProgress 不经
// DB JSON 往返）⇒ hints 在 —— 所以「live 时详情正常、刷新后不含结构化详情」。
//
// 本文件钉死 tool_hints 在三条读取路径上的透传：
//   T1 MapIterationRecord（REST 三路径共用的唯一解析入口，fold=false 全量形态）
//   T2 View 主路径端到端（ConvertMessagesToHistoryWithIterationsView）
//   T3 legacy Detail JSON 路径（rawMessageIterations，v55- 旧数据）
// 折叠轻字段语义（fold=true 置空 ToolHints）是既有契约，一并钉死防回归。
//
// Mutation 自证：删掉 regionToolSnap/iterToolSnap 的 ToolHints 字段或装配处的
// tp.ToolHints = t.ToolHints ⇒ T1/T2/T3 必红。

import (
	"encoding/json"
	"testing"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// fixASyntheticToolJSON 是 Fix A（persistSyntheticToolToHistory）落库的
// bg_subagent_completed 快照形状 —— 与 agent.IterationToolSnapshot 的 json tag
// 逐一对应（channel 不能 import agent：agent → channel 已有依赖，反向会成环）。
func fixASyntheticToolJSON(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal([]map[string]any{{
		"name":       "bg_subagent_completed",
		"label":      "bgsub:explore/mem-1",
		"status":     "done",
		"elapsed_ms": 4200,
		"summary":    "子代理 explore/mem-1 已完成",
		"detail":     "# 子代理完成汇报\n\n修复已完成，测试全绿。",
		"tool_hints": `{"kind":"subagent","role":"explore","instance":"mem-1","status":"done","task":"修复登录页"}`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestMapIterationRecord_ToolHintsPassthrough（T1，修复前红）：
// tools JSON 里持久化的 tool_hints 必须流入 protocol.ToolProgress.ToolHints ——
// 这是 /api/iteration_detail「完整覆盖轻字段」与 CLI/RPC 全量装配的共同来源。
func TestMapIterationRecord_ToolHintsPassthrough(t *testing.T) {
	rec := sqlite.IterationRecord{
		TurnID:    19,
		Iteration: 2,
		Content:   "最终回复",
		Tools:     fixASyntheticToolJSON(t),
	}

	full := MapIterationRecord(rec, false)
	if len(full.Tools) != 1 {
		t.Fatalf("tools = %v (len %d), want 1", full.Tools, len(full.Tools))
	}
	tp := full.Tools[0]
	if tp.Name != "bg_subagent_completed" {
		t.Fatalf("name = %q", tp.Name)
	}
	if tp.ToolHints == "" {
		t.Fatalf("ToolHints 被丢弃（历史合成工具结构化详情消失的根因）：%+v", tp)
	}
	if !json.Valid([]byte(tp.ToolHints)) || !contains(tp.ToolHints, `"kind":"subagent"`) {
		t.Fatalf("ToolHints 载荷不完整: %q", tp.ToolHints)
	}
	if tp.Summary == "" || tp.Detail == "" {
		t.Fatalf("fold=false 时 summary/detail 必须透传：%+v", tp)
	}

	// 折叠轻字段语义（既有契约）：fold=true 置空详情大字段，pill 轻字段完整。
	folded := MapIterationRecord(rec, true)
	if len(folded.Tools) != 1 {
		t.Fatalf("folded tools len = %d", len(folded.Tools))
	}
	ft := folded.Tools[0]
	if ft.ToolHints != "" || ft.Detail != "" || ft.Summary != "" {
		t.Fatalf("fold=true 必须省略详情大字段: %+v", ft)
	}
	if ft.Name == "" || ft.Label == "" || ft.Status != "done" {
		t.Fatalf("fold=true 时 pill 轻字段必须完整: %+v", ft)
	}
	if !folded.ToolsFolded {
		t.Fatal("fold=true 必须打 ToolsFolded 标记")
	}
}

// TestView_SyntheticToolHintsEndToEnd（T2，修复前红）：
// REST 历史主路径 —— Fix A 落库的合成工具带 tool_hints，View 装配后必须透传。
func TestView_SyntheticToolHintsEndToEnd(t *testing.T) {
	msgs, turnIterMap, _ := hintsViewFixture(t)
	history := ConvertMessagesToHistoryWithIterationsView(msgs, turnIterMap, false)

	assistant := findTurnAssistant(t, history, 19)
	if len(assistant.Iterations) != 1 {
		t.Fatalf("iterations = %d, want 1", len(assistant.Iterations))
	}
	tools := assistant.Iterations[0].Tools
	if len(tools) != 1 {
		t.Fatalf("tools = %v (len %d), want 1", tools, len(tools))
	}
	if tools[0].ToolHints == "" {
		t.Fatalf("View 输出丢失 tool_hints（前端历史渲染合成卡片无结构化详情）：%+v", tools[0])
	}
	if tools[0].Name != "bg_subagent_completed" {
		t.Fatalf("name = %q", tools[0].Name)
	}
}

// TestRawMessageIterations_ToolHintsPassthrough（T3，修复前红）：
// legacy Detail JSON（v55- 旧数据的 message.Detail）里带 tool_hints 的工具
// 经 rawMessageIterations 装配必须透传。
func TestRawMessageIterations_ToolHintsPassthrough(t *testing.T) {
	detailJSON := `{"id":9,"role":"assistant","content":"done","detail":"[{\"iteration\":1,\"tools\":[{\"name\":\"background_task_result\",\"status\":\"done\",\"summary\":\"后台任务完成\",\"detail\":\"build ok\",\"tool_hints\":\"{\\\"kind\\\":\\\"bg_task\\\",\\\"task\\\":\\\"npm run build\\\",\\\"status\\\":\\\"done\\\",\\\"output\\\":\\\"build ok\\\"}\"}]}]"}`
	_ = detailJSON // detail 走 llm.ChatMessage.Detail 字段
	msg := llm.ChatMessage{ID: 9, Role: "assistant", Content: "done", TurnID: 19,
		Detail: `[{"iteration":1,"content":"done","tools":[{"name":"background_task_result","status":"done","summary":"后台任务完成","detail":"build ok","tool_hints":"{\"kind\":\"bg_task\",\"task\":\"npm run build\",\"status\":\"done\",\"output\":\"build ok\"}"}]}]`}
	iters := rawMessageIterations(msg, nil)
	if len(iters) != 1 {
		t.Fatalf("iterations = %d, want 1", len(iters))
	}
	if len(iters[0].Tools) != 1 {
		t.Fatalf("tools = %v (len %d), want 1", iters[0].Tools, len(iters[0].Tools))
	}
	if iters[0].Tools[0].ToolHints == "" {
		t.Fatalf("legacy Detail JSON 装配丢失 tool_hints：%+v", iters[0].Tools[0])
	}
	if !contains(iters[0].Tools[0].ToolHints, `"kind":"bg_task"`) {
		t.Fatalf("ToolHints 载荷不完整: %q", iters[0].Tools[0].ToolHints)
	}
}

func hintsViewFixture(t *testing.T) ([]llm.ChatMessage, map[uint64][]sqlite.IterationRecord, string) {
	t.Helper()
	msgs := []llm.ChatMessage{
		{ID: 1, Role: "user", Content: "go", TurnID: 19},
		{ID: 2, Role: "assistant", Content: "done", TurnID: 19},
	}
	turnIterMap := map[uint64][]sqlite.IterationRecord{19: {
		{TurnID: 19, Iteration: 1, Content: "done", Tools: fixASyntheticToolJSON(t)},
	}}
	return msgs, turnIterMap, fixASyntheticToolJSON(t)
}

package channel

import (
	"strings"
	"testing"
	"time"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// 合成工具对（synthetic tool pair）历史回放合并的契约测试（2026-09-30 事故）：
//
// 事故形态：pre_turn_end / bg task / cron 等【注入型】工具对落进了
// session_messages（assistant 行带恰好一个合成工具调用 + tool 结果行），但
// 在旧代码下 iteration_history 的 tools JSON **永远**缺这个工具（注入发生在
// 迭代快照落库之后，内存 CompletedTools 马上被 beginIteration 清空）⇒ 投影层
// ConvertMessagesToHistoryWithIterationsView 对有结构化数据的 turn 直接丢弃
// pendingIters ⇒ 刷新/切会话后前端再也渲染不出这个工具（chat_D3D0 turn 19
// pre_turn_end 实证）。
//
// 本文件守护两层：
//   - MergeSyntheticToolPairs：投影前把 session_messages 的工具对合并回
//     turnIterMap（旧数据修复；Fix A 之后的新数据由 Detail 去重防双写）；
//   - ConvertMessagesToHistoryWithIterationsView 的装配路径：合并后的记录
//     必须真的流到 HistoryIteration.Tools（foldView 两种模式）。
//
// Mutation 自证：删掉 View 里的 MergeSyntheticToolPairs 调用 ⇒ 前两个用例必红。

// legacyPairFixture = turn-19 事故的最小化形状：2 个迭代（Shell + content-only
// 收尾），pre_turn_end 工具对在最终快照【之后】注入 —— session_messages 有、
// 旧数据 iteration_history 没有。
func legacyPairFixture() (msgs []llm.ChatMessage, turnIterMap map[uint64][]sqlite.IterationRecord) {
	t1 := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(2 * time.Minute)
	t3 := t2.Add(5 * time.Second)
	pairContent := "所有 PreTurnEnd 钩子已处理完毕，请完成最终回复。"
	msgs = []llm.ChatMessage{
		{ID: 1, Role: "user", Content: "go", TurnID: 19, Timestamp: t1},
		{ID: 2, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Shell", Arguments: "{}"}}, TurnID: 19, Timestamp: t1.Add(10 * time.Second)},
		{Role: "tool", ToolCallID: "c1", ToolName: "Shell", Content: "ok", TurnID: 19, Timestamp: t1.Add(20 * time.Second)},
		{ID: 4, Role: "assistant", Content: "最终回复", TurnID: 19, Timestamp: t2},
		// pre_turn_end 合成工具对：注入发生在最终迭代快照之后。
		{ID: 5, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "pte_1", Name: "pre_turn_end", Arguments: "{}"}}, TurnID: 19, Timestamp: t3},
		{Role: "tool", ToolCallID: "pte_1", ToolName: "pre_turn_end", Content: pairContent, TurnID: 19, Timestamp: t3.Add(time.Second)},
	}
	turnIterMap = map[uint64][]sqlite.IterationRecord{19: {
		{TurnID: 19, Iteration: 1, Tools: `[{"name":"Shell","status":"done"}]`, CreatedAt: t1.Add(30 * time.Second)},
		{TurnID: 19, Iteration: 2, Content: "最终回复", Tools: "[]", CreatedAt: t2},
	}}
	return msgs, turnIterMap
}

func findTurnAssistant(t *testing.T, history []HistoryMessage, turnID uint64) HistoryMessage {
	t.Helper()
	for _, h := range history {
		if h.Role == "assistant" && h.TurnID == turnID {
			return h
		}
	}
	t.Fatalf("no assistant HistoryMessage for turn %d in %v", turnID, history)
	return HistoryMessage{}
}

// TestView_RestoresLegacySyntheticPair（复现用例，修复前红）：
// 旧数据的工具对必须并回它所属的迭代 —— pre_turn_end 注入发生在最终快照
// 之后 ⇒ 按时间戳锚定到【最后一个】迭代（与 live 渲染同一迭代）。
func TestView_RestoresLegacySyntheticPair(t *testing.T) {
	msgs, turnIterMap := legacyPairFixture()
	history := ConvertMessagesToHistoryWithIterationsView(msgs, turnIterMap, false)

	assistant := findTurnAssistant(t, history, 19)
	if len(assistant.Iterations) != 2 {
		t.Fatalf("expected 2 iterations, got %d", len(assistant.Iterations))
	}
	last := assistant.Iterations[1]
	if last.Content != "最终回复" {
		t.Errorf("iter 2 content = %q, want %q", last.Content, "最终回复")
	}
	if len(last.Tools) != 1 {
		t.Fatalf("iter 2 tools = %v (len %d), want exactly 1 restored pre_turn_end tool", last.Tools, len(last.Tools))
	}
	if last.Tools[0].Name != "pre_turn_end" {
		t.Errorf("iter 2 tool name = %q, want pre_turn_end", last.Tools[0].Name)
	}
	if last.Tools[0].Status != "done" {
		t.Errorf("iter 2 tool status = %q, want done", last.Tools[0].Status)
	}
	if last.Tools[0].Detail != "所有 PreTurnEnd 钩子已处理完毕，请完成最终回复。" {
		t.Errorf("iter 2 tool detail = %q, want pair tool result content", last.Tools[0].Detail)
	}
	// 锚定迭代号必须与宿主迭代一致（前端 pill 归属）。
	if last.Tools[0].Iteration != 2 {
		t.Errorf("iter 2 tool iteration = %d, want 2", last.Tools[0].Iteration)
	}
	// 迭代 1 的既有工具不受影响。
	if len(assistant.Iterations[0].Tools) != 1 || assistant.Iterations[0].Tools[0].Name != "Shell" {
		t.Errorf("iter 1 tools = %v, want original Shell only", assistant.Iterations[0].Tools)
	}
}

// TestView_RestoresLegacySyntheticPair_Folded：foldView=true（Web REST 历史路径）
// 同样必须恢复该工具 —— 轻字段（Name/Label/Status）保留、详情折叠，由
// /api/iteration_detail 取回（todo：detail 端点的合并由 serverapp 侧覆盖）。
func TestView_RestoresLegacySyntheticPair_Folded(t *testing.T) {
	msgs, turnIterMap := legacyPairFixture()
	history := ConvertMessagesToHistoryWithIterationsView(msgs, turnIterMap, true)

	assistant := findTurnAssistant(t, history, 19)
	if len(assistant.Iterations) != 2 {
		t.Fatalf("expected 2 iterations, got %d", len(assistant.Iterations))
	}
	last := assistant.Iterations[1]
	if len(last.Tools) != 1 || last.Tools[0].Name != "pre_turn_end" {
		t.Fatalf("folded view: iter 2 tools = %v, want restored pre_turn_end", last.Tools)
	}
	if last.Tools[0].Detail != "" {
		t.Errorf("folded view: iter 2 tool detail should be light-field (empty), got %q", last.Tools[0].Detail)
	}
	if !last.ToolsFolded {
		t.Errorf("folded view: iter 2 ToolsFolded should be true for non-GenUI merged tool")
	}
}

// TestMergeSyntheticToolPairs_DedupAgainstPersisted：Fix A 之后的新数据
// iteration_history 已带该工具（AppendIterationTool 回写）⇒ 绝不能双写。
// 去重键 = (name, detail)：工具对的结果内容与 tools JSON 的 detail 同源。
func TestMergeSyntheticToolPairs_DedupAgainstPersisted(t *testing.T) {
	msgs, turnIterMap := legacyPairFixture()
	persisted := `[{"name":"pre_turn_end","status":"done","label":"pre_turn_end","detail":"所有 PreTurnEnd 钩子已处理完毕，请完成最终回复。","tool_hints":"{\"kind\":\"pre_turn_end\"}"}]`
	turnIterMap[19][1].Tools = persisted

	history := ConvertMessagesToHistoryWithIterationsView(msgs, turnIterMap, false)
	assistant := findTurnAssistant(t, history, 19)
	if len(assistant.Iterations[1].Tools) != 1 {
		t.Fatalf("iter 2 tools = %v (len %d), dedup failed — already-persisted tool must not duplicate", assistant.Iterations[1].Tools, len(assistant.Iterations[1].Tools))
	}
}

// TestMergeSyntheticToolPairs_MidRunAnchor：迭代中途注入（bg task 完成于
// iter1 快照之后、iter2 快照之前）⇒ 按时间戳锚定到 iter1。
func TestMergeSyntheticToolPairs_MidRunAnchor(t *testing.T) {
	msgs, turnIterMap := legacyPairFixture()
	// 把 pre_turn_end 对改成 bg task 对，时间戳落在 iter1 记录与 iter2 记录之间。
	t1 := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	mid := t1.Add(90 * time.Second) // iter1.CreatedAt = t1+30s, iter2.CreatedAt = t1+120s
	msgs[4] = llm.ChatMessage{ID: 5, Role: "assistant",
		ToolCalls: []llm.ToolCall{{ID: "bg_1", Name: "background_task_result", Arguments: "{}"}},
		TurnID:    19, Timestamp: mid}
	msgs[5] = llm.ChatMessage{Role: "tool", ToolCallID: "bg_1", ToolName: "background_task_result",
		Content: "背景任务 3f8f 已完成（exit 0）", TurnID: 19, Timestamp: mid.Add(time.Second)}

	history := ConvertMessagesToHistoryWithIterationsView(msgs, turnIterMap, false)
	assistant := findTurnAssistant(t, history, 19)
	iter1 := assistant.Iterations[0]
	if len(iter1.Tools) != 2 || iter1.Tools[1].Name != "background_task_result" {
		t.Fatalf("iter 1 tools = %v, want [Shell, background_task_result] (mid-run anchor)", iter1.Tools)
	}
	if len(assistant.Iterations[1].Tools) != 0 {
		t.Errorf("iter 2 tools = %v, want none (pair belongs to iter 1)", assistant.Iterations[1].Tools)
	}
}

// TestMergeSyntheticToolPairs_NeverDestroysCorruptTools：锚定记录的 tools JSON
// 损坏时（既非合法数组也不是空串/[]）跳过合并，绝不覆盖既有数据。
func TestMergeSyntheticToolPairs_NeverDestroysCorruptTools(t *testing.T) {
	msgs, turnIterMap := legacyPairFixture()
	corrupt := `{oops`
	turnIterMap[19][1].Tools = corrupt

	history := ConvertMessagesToHistoryWithIterationsView(msgs, turnIterMap, false)
	assistant := findTurnAssistant(t, history, 19)
	// 输出里该迭代没有任何工具（解析失败 → 无工具，不追加、不覆盖）。
	if len(assistant.Iterations[1].Tools) != 0 {
		t.Errorf("iter 2 tools = %v, want none (corrupt tools JSON must be left untouched)", assistant.Iterations[1].Tools)
	}
	// 调用方的原记录未被破坏。
	if turnIterMap[19][1].Tools != corrupt {
		t.Errorf("caller's record tools mutated: %q", turnIterMap[19][1].Tools)
	}
}

// TestMergeSyntheticToolPairs_SkipsPairWithoutContent：分页边界把消息对劈开
// （assistant 行在窗口里、tool 结果行不在）⇒ 无内容可去重也无内容可渲染 ⇒
// 跳过，绝不造空 pill、也绝不对已持久化（Fix A）的条目产生重复。
func TestMergeSyntheticToolPairs_SkipsPairWithoutContent(t *testing.T) {
	msgs, turnIterMap := legacyPairFixture()
	msgs = msgs[:5] // 掐掉 tool 结果行

	history := ConvertMessagesToHistoryWithIterationsView(msgs, turnIterMap, false)
	assistant := findTurnAssistant(t, history, 19)
	if len(assistant.Iterations[1].Tools) != 0 {
		t.Errorf("iter 2 tools = %v, want none (pair without content must be skipped)", assistant.Iterations[1].Tools)
	}
}

// TestMergeSyntheticToolPairs_CallerImmutability：无合成对时返回原 map
// （同一引用，零开销）；合并只发生在副本上，调用方数据永不被改。
func TestMergeSyntheticToolPairs_CallerImmutability(t *testing.T) {
	toolResults := map[string]string{"c1": "ok"}
	// 无合成对 ⇒ 原引用。
	plainMsgs := []llm.ChatMessage{
		{Role: "user", Content: "go", TurnID: 7},
		{ID: 2, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Shell", Arguments: "{}"}}, TurnID: 7},
		{Role: "tool", ToolCallID: "c1", Content: "ok", TurnID: 7},
		{ID: 4, Role: "assistant", Content: "done", TurnID: 7},
	}
	turnIterMap := map[uint64][]sqlite.IterationRecord{7: {
		{TurnID: 7, Iteration: 1, Tools: `[{"name":"Shell","status":"done"}]`},
	}}
	// 无合成对 ⇒ 原引用（零拷贝）：返回 map 必须可用且内容未被改动。
	if got := MergeSyntheticToolPairs(plainMsgs, toolResults, turnIterMap); len(got) != 1 {
		t.Fatalf("MergeSyntheticToolPairs returned %v, want original map untouched", got)
	} else if got[7][0].Tools != `[{"name":"Shell","status":"done"}]` {
		t.Errorf("no-pair path mutated records: %q", got[7][0].Tools)
	}

	// 合并路径：调用方记录不被改。
	msgs, richMap := legacyPairFixture()
	_ = ConvertMessagesToHistoryWithIterationsView(msgs, richMap, false)
	if richMap[19][1].Tools != "[]" {
		t.Errorf("caller's iter-2 record tools mutated: %q", richMap[19][1].Tools)
	}
}

// TestMergeSyntheticToolPairs_NoStructuredRecords：turnIterMap 里没有该 turn
// （无结构化迭代数据）⇒ 不合并 —— 该 turn 走 legacy pendingIters 路径，工具对
// 本来就会渲染。
func TestMergeSyntheticToolPairs_NoStructuredRecords(t *testing.T) {
	msgs, _ := legacyPairFixture()
	emptyMap := map[uint64][]sqlite.IterationRecord{}
	if got := MergeSyntheticToolPairs(msgs, map[string]string{"pte_1": "x"}, emptyMap); len(got) != 0 {
		t.Errorf("expected empty map untouched, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// 按需取回端点（/api/regions、/api/iteration_detail）的合并原语
// ---------------------------------------------------------------------------

// anchorFixture 构造完整 turn 锚点（4 个迭代，时间递增）+ 一对注入于
// iter2 落盘后、iter3 落盘前的 bg task 工具对（mid-run 注入形态）。
func anchorFixture() (anchors []sqlite.IterationRecord, pair SyntheticToolPair) {
	t1 := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	anchors = []sqlite.IterationRecord{
		{Iteration: 1, CreatedAt: t1},
		{Iteration: 2, CreatedAt: t1.Add(time.Minute)},
		{Iteration: 3, CreatedAt: t1.Add(2 * time.Minute)},
		{Iteration: 4, CreatedAt: t1.Add(3 * time.Minute)},
	}
	pair = SyntheticToolPair{
		TurnID: 88, TS: t1.Add(90 * time.Second),
		Name: "background_task_result", Args: "{}", CallID: "bg_1",
		Content: "任务已完成（exit 0）",
	}
	return anchors, pair
}

// TestMergeSyntheticPairsIntoSegment：段合并 —— 锚定到段【内】迭代的对并入；
// 锚定到段外的对跳过（不归本段渲染，前端翻到含该迭代的段时那一次请求补上）。
func TestMergeSyntheticPairsIntoSegment(t *testing.T) {
	anchors, pair := anchorFixture()
	// 段 = iter 2..3（含锚定目标 iter2）。
	segment := []sqlite.IterationRecord{
		{TurnID: 88, Iteration: 2, Content: "iter2", Tools: "[]", CreatedAt: anchors[1].CreatedAt},
		{TurnID: 88, Iteration: 3, Content: "iter3", Tools: "[]", CreatedAt: anchors[2].CreatedAt},
	}
	out := MergeSyntheticPairsIntoSegment(segment, []SyntheticToolPair{pair}, anchors)
	if len(out) != 2 {
		t.Fatalf("segment length changed: %d", len(out))
	}
	if !containsTool(out[0].Tools, "background_task_result") {
		t.Fatalf("iter2 tools=%q, want merged background_task_result (anchor target)", out[0].Tools)
	}
	if containsTool(out[1].Tools, "background_task_result") {
		t.Fatalf("iter3 tools=%q, must not contain the pair (wrong anchor)", out[1].Tools)
	}
	// 段不含锚定迭代（iter1..1，锚定目标是 iter2）⇒ 跳过，返回原切片（零合并）。
	tail := []sqlite.IterationRecord{
		{TurnID: 88, Iteration: 1, Content: "iter1", Tools: "[]", CreatedAt: anchors[0].CreatedAt},
	}
	if out := MergeSyntheticPairsIntoSegment(tail, []SyntheticToolPair{pair}, anchors); containsTool(out[0].Tools, "background_task_result") {
		t.Fatalf("out-of-segment pair leaked into iter1: %q", out[0].Tools)
	}
	// 无对时原引用返回（copy-on-write 零开销）。
	if got := MergeSyntheticPairsIntoSegment(tail, nil, anchors); len(got) != 1 || got[0].Tools != "[]" {
		t.Fatalf("no-pairs path mutated: %+v", got)
	}
}

// TestMergeSyntheticPairsIntoRecord：单条合并 —— 只有锚定到该迭代号的对才并入
// （/api/iteration_detail 的语义：请求迭代 K 的详情 ⇒ 只补 K 的工具）。
func TestMergeSyntheticPairsIntoRecord(t *testing.T) {
	anchors, pair := anchorFixture()
	// iter2 = 锚定目标 ⇒ 并入。
	rec2 := sqlite.IterationRecord{TurnID: 88, Iteration: 2, Content: "iter2", Tools: "[]", CreatedAt: anchors[1].CreatedAt}
	out := MergeSyntheticPairsIntoRecord(rec2, []SyntheticToolPair{pair}, anchors)
	if !containsTool(out.Tools, "background_task_result") {
		t.Fatalf("iter2 detail=%q, want merged tool", out.Tools)
	}
	if out.Content != "iter2" || out.Iteration != 2 {
		t.Fatalf("record fields lost in merge: %+v", out)
	}
	// iter3 ≠ 锚定目标 ⇒ 原样返回（值相等即可；语义是「不并入」）。
	rec3 := sqlite.IterationRecord{TurnID: 88, Iteration: 3, Content: "iter3", Tools: "[]", CreatedAt: anchors[2].CreatedAt}
	if out := MergeSyntheticPairsIntoRecord(rec3, []SyntheticToolPair{pair}, anchors); containsTool(out.Tools, "background_task_result") {
		t.Fatalf("iter3 detail=%q, pair must not merge into non-anchor iteration", out.Tools)
	}
	// 已持久化（Fix A 新数据）⇒ 去重短路，绝不双写。
	persisted := sqlite.IterationRecord{TurnID: 88, Iteration: 2, Content: "iter2",
		Tools: `[{"name":"background_task_result","status":"done","detail":"任务已完成（exit 0）"}]`, CreatedAt: anchors[1].CreatedAt}
	out = MergeSyntheticPairsIntoRecord(persisted, []SyntheticToolPair{pair}, anchors)
	if countOccurrences(out.Tools, `"name":"background_task_result"`) != 1 {
		t.Fatalf("dedup failed (double write): %q", out.Tools)
	}
	// anchors 为空（无结构化迭代）⇒ 跳过（fallback 无处可锚）。
	if out := MergeSyntheticPairsIntoRecord(rec2, []SyntheticToolPair{pair}, nil); containsTool(out.Tools, "background_task_result") {
		t.Fatalf("empty anchors must not merge: %q", out.Tools)
	}
}

// containsTool 检查 tools JSON 是否含指定工具名的条目。
func containsTool(toolsJSON, name string) bool {
	return strings.Contains(toolsJSON, `"name":"`+name+`"`)
}

func countOccurrences(s, sub string) int {
	return strings.Count(s, sub)
}

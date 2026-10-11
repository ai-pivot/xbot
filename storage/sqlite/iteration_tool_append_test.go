package sqlite

import (
	"strings"
	"testing"
	"time"

	"xbot/llm"
)

// TestAppendIterationTool 守护 2026-09-30 pre_turn_end 事故引入的
// SessionService.AppendIterationTool（详见方法注释）。四条契约：
//  1. 追加进已落盘记录的 tools JSON **尾部**（顺序保留，老工具在前）；
//  2. 记录不存在 → (false, nil)，绝不 Insert（互斥契约：未落盘的工具由
//     下一次 snapshotCompletedIteration 从 CompletedTools 正常写入）；
//  3. 非法 toolJSON → 报错，不落库；
//  4. 已有 tools 不是 JSON 数组 → 报错且**不覆盖**（fail-closed，不破坏存量数据）。
func TestAppendIterationTool(t *testing.T) {
	db, svc, tenantID := newHistoryTestService(t)
	_ = db
	const turnID = uint64(100)
	if err := svc.AppendIterationHistory(tenantID, 0, turnID, IterationRecord{
		MessageID: 0, TurnID: turnID, Iteration: 1, Content: "final reply",
		Tools: `[{"name":"Shell","status":"done"},{"name":"Read","status":"done"}]`,
	}); err != nil {
		t.Fatal(err)
	}

	// 1. 追加到尾部。
	found, err := svc.AppendIterationTool(tenantID, turnID, 1,
		`{"name":"pre_turn_end","status":"done","detail":"continue"}`)
	if err != nil || !found {
		t.Fatalf("append found=%v err=%v", found, err)
	}
	recs, err := svc.GetIterationHistoryByTurn(tenantID, turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("records=%d want 1", len(recs))
	}
	if !strings.Contains(recs[0].Tools, `"name":"Shell"`) ||
		!strings.Contains(recs[0].Tools, `"name":"Read"`) ||
		!strings.Contains(recs[0].Tools, `"name":"pre_turn_end"`) {
		t.Fatalf("merged tools JSON lost entries: %s", recs[0].Tools)
	}
	// 尾部断言：pre_turn_end 必须在 Shell 之后（老数据在前）。
	if strings.Index(recs[0].Tools, `"name":"pre_turn_end"`) < strings.Index(recs[0].Tools, `"name":"Shell"`) {
		t.Fatalf("appended tool is not at the array tail: %s", recs[0].Tools)
	}
	if !strings.HasPrefix(strings.TrimSpace(recs[0].Tools), "[") {
		t.Fatalf("merged tools is not a JSON array: %s", recs[0].Tools)
	}

	// 2. 记录不存在 → (false, nil)，且绝不 Insert。
	found, err = svc.AppendIterationTool(tenantID, turnID, 9, `{"name":"x"}`)
	if err != nil || found {
		t.Fatalf("missing record: found=%v err=%v (want false,nil)", found, err)
	}
	recs, err = svc.GetIterationHistoryByTurn(tenantID, turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Iteration != 1 {
		t.Fatalf("missing-record append must not insert: %+v", recs)
	}

	// 3. 非法 toolJSON → 报错。
	if _, err := svc.AppendIterationTool(tenantID, turnID, 1, `not-json`); err == nil {
		t.Fatal("invalid toolJSON accepted")
	}

	// 4. 已有 tools 不是 JSON 数组 → 报错且不覆盖。
	if _, err := db.Conn().Exec(`
		UPDATE iteration_history SET tools = '{"unexpected":"object"}'
		WHERE tenant_id = ? AND turn_id = ? AND iteration = 1
	`, tenantID, turnID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendIterationTool(tenantID, turnID, 1, `{"name":"y"}`); err == nil {
		t.Fatal("non-array existing tools accepted")
	}
	var tools string
	if err := db.Conn().QueryRow(`
		SELECT tools FROM iteration_history WHERE tenant_id = ? AND turn_id = ? AND iteration = 1
	`, tenantID, turnID).Scan(&tools); err != nil {
		t.Fatal(err)
	}
	if tools != `{"unexpected":"object"}` {
		t.Fatalf("corrupt existing tools was overwritten: %s", tools)
	}
}

// TestAppendIterationToolEmptyTools 守护边界：已有 tools 为空串时追加，结果
// 必须是含一个元素的合法数组（而不是字符串拼接）。NULL 不可测：schema 的
// tools 列带 NOT NULL 约束，NULL 行不可能存在（AppendIterationTool 的
// NullString 分支只是防御性读取）。
func TestAppendIterationToolEmptyTools(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	const turnID = uint64(201)
	if err := svc.AppendIterationHistory(tenantID, 0, turnID, IterationRecord{
		TurnID: turnID, Iteration: 1, Content: "a", Tools: "",
	}); err != nil {
		t.Fatal(err)
	}
	found, err := svc.AppendIterationTool(tenantID, turnID, 1, `{"name":"cron_fired"}`)
	if err != nil || !found {
		t.Fatalf("append: found=%v err=%v", found, err)
	}
	recs, err := svc.GetIterationHistoryByTurn(tenantID, turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("records=%d want 1", len(recs))
	}
	if recs[0].Tools != `[{"name":"cron_fired"}]` {
		t.Fatalf("tools=%q, want single-element array", recs[0].Tools)
	}
}

// pairTestAppend 是工具对查询测试的最小写入 helper（失败即 Fatal）。
func pairTestAppend(t *testing.T, svc *SessionService, tenantID int64, msg llm.ChatMessage) {
	t.Helper()
	if _, err := svc.AppendMessage(tenantID, msg); err != nil {
		t.Fatal(err)
	}
}

// TestGetSyntheticPairRowsByTurn 守护 /api/regions、/api/iteration_detail 旧数据
// 修复的取数查询（GetSyntheticPairRowsByTurn，2026-09-30 事故）：只返回该 turn 的
// 工具对候选行（带 tool_calls 的 assistant 行 + tool 结果行），噪声行全部排除 ——
// display_only（cron 展示行）/ internal_only（多模态载体）/ 纯文本行 / 其他 turn。
// 合成名单不在 SQL 里判（channel 层职责），普通工具对同样是候选行。
func TestGetSyntheticPairRowsByTurn(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	const turnID = uint64(300)
	base := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	mk := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }
	synCall := []llm.ToolCall{{ID: "pte_1", Name: "pre_turn_end", Arguments: "{}"}}
	realCall := []llm.ToolCall{{ID: "sh_1", Name: "Shell", Arguments: "{}"}}

	// 候选行（按时间序 append ⇒ id ASC = 时间序）。
	pairTestAppend(t, svc, tenantID, llm.ChatMessage{Role: "assistant", ToolCalls: realCall, TurnID: turnID, Timestamp: mk(10)})
	pairTestAppend(t, svc, tenantID, llm.ChatMessage{Role: "tool", ToolCallID: "sh_1", ToolName: "Shell", Content: "ok", TurnID: turnID, Timestamp: mk(11)})
	pairTestAppend(t, svc, tenantID, llm.ChatMessage{Role: "assistant", ToolCalls: synCall, TurnID: turnID, Timestamp: mk(60)})
	pairTestAppend(t, svc, tenantID, llm.ChatMessage{Role: "tool", ToolCallID: "pte_1", ToolName: "pre_turn_end", Content: "done", TurnID: turnID, Timestamp: mk(61)})
	// 噪声行（绝不返回）。
	pairTestAppend(t, svc, tenantID, llm.ChatMessage{Role: "user", Content: "go", TurnID: turnID, Timestamp: mk(1)})
	pairTestAppend(t, svc, tenantID, llm.ChatMessage{Role: "assistant", Content: "final", TurnID: turnID, Timestamp: mk(70)})
	pairTestAppend(t, svc, tenantID, llm.ChatMessage{Role: "assistant", ToolCalls: realCall, TurnID: turnID, DisplayOnly: true, Timestamp: mk(80)})
	pairTestAppend(t, svc, tenantID, llm.ChatMessage{Role: "assistant", ToolCalls: realCall, TurnID: turnID, Internal: true, Timestamp: mk(81)})
	// 其他 turn（turn_id 过滤）。
	pairTestAppend(t, svc, tenantID, llm.ChatMessage{Role: "assistant", ToolCalls: synCall, TurnID: turnID + 1, Timestamp: mk(90)})

	rows, err := svc.GetSyntheticPairRowsByTurn(tenantID, turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows=%d want 4 (pair candidates only): %+v", len(rows), rows)
	}
	// id ASC 断言（append 顺序）。
	if rows[0].Role != "assistant" || len(rows[0].ToolCalls) != 1 || rows[0].ToolCalls[0].Name != "Shell" {
		t.Fatalf("rows[0] = %+v, want Shell call row", rows[0])
	}
	if !rows[0].Timestamp.Equal(mk(10)) {
		t.Fatalf("rows[0].Timestamp=%v want %v (created_at round-trip)", rows[0].Timestamp, mk(10))
	}
	if rows[1].Role != "tool" || rows[1].ToolCallID != "sh_1" || rows[1].Content != "ok" {
		t.Fatalf("rows[1] = %+v, want Shell tool result row", rows[1])
	}
	if rows[2].Role != "assistant" || len(rows[2].ToolCalls) != 1 || rows[2].ToolCalls[0].Name != "pre_turn_end" || rows[2].ToolCalls[0].ID != "pte_1" {
		t.Fatalf("rows[2] = %+v, want pre_turn_end call row (tool_calls JSON round-trip)", rows[2])
	}
	if !rows[2].Timestamp.Equal(mk(60)) {
		t.Fatalf("rows[2].Timestamp=%v want %v", rows[2].Timestamp, mk(60))
	}
	if rows[3].Role != "tool" || rows[3].ToolCallID != "pte_1" || rows[3].Content != "done" {
		t.Fatalf("rows[3] = %+v, want pre_turn_end tool result row", rows[3])
	}

	// 不存在的 turn → 空结果，非错误。
	if rows, err := svc.GetSyntheticPairRowsByTurn(tenantID, 404); err != nil || len(rows) != 0 {
		t.Fatalf("missing turn: rows=%d err=%v, want 0,nil", len(rows), err)
	}
}

// TestGetIterationAnchorsByTurn 守护轻量锚点查询（GetIterationAnchorsByTurn）：
// 只取 (iteration, created_at) 两列、按迭代升序 —— detail/regions 的旧数据
// 修复据此把注入时刻映射到迭代号，绝不拉 content/tools 大字段（那正是折叠
// 视图要省的）。
func TestGetIterationAnchorsByTurn(t *testing.T) {
	db, svc, tenantID := newHistoryTestService(t)
	const turnID = uint64(301)
	for it := 1; it <= 3; it++ {
		if err := svc.AppendIterationHistory(tenantID, 0, turnID, IterationRecord{
			TurnID: turnID, Iteration: it, Content: "iter content that must NOT be loaded", Tools: "[]",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// AppendIterationHistory 不写 created_at —— 与 Fix A 测试同款直接 UPDATE 受控时间戳。
	base := time.Date(2026, 9, 30, 11, 0, 0, 0, time.UTC)
	for it := 1; it <= 3; it++ {
		if _, err := db.Conn().Exec(
			`UPDATE iteration_history SET created_at = ? WHERE tenant_id = ? AND turn_id = ? AND iteration = ?`,
			base.Add(time.Duration(it)*time.Minute).Format(time.RFC3339), tenantID, turnID, it,
		); err != nil {
			t.Fatal(err)
		}
	}

	anchors, err := svc.GetIterationAnchorsByTurn(tenantID, turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(anchors) != 3 {
		t.Fatalf("anchors=%d want 3", len(anchors))
	}
	for i, a := range anchors {
		if a.Iteration != i+1 {
			t.Fatalf("anchor[%d].Iteration=%d want %d (iteration ASC)", i, a.Iteration, i+1)
		}
		want := base.Add(time.Duration(i+1) * time.Minute)
		if !a.CreatedAt.Equal(want) {
			t.Fatalf("anchor[%d].CreatedAt=%v want %v", i, a.CreatedAt, want)
		}
		if a.Content != "" || a.Tools != "" {
			t.Fatalf("anchor must be light (Iteration/CreatedAt only): %+v", a)
		}
	}
}

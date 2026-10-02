package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestSyntheticToolPairAfterSnapshotIsAppendedToIterationHistory 是 2026-09-30
// pre_turn_end 合成工具丢失事故的核心守护（红→绿）：
//
//	handleFinalResponse → snapshotCompletedIteration 先把迭代记录写库
//	（此时 tools 数组里还没有合成工具），然后 maybeContinueTurn →
//	injectSyntheticToolPair 才把工具塞进内存 CompletedTools —— 而该内存态
//	马上被 beginIteration 清空，快照永远看不到它。
//
// 修复（persistSyntheticToolToHistory）：注入时把工具快照 UPDATE 追加进
// 【已落盘】记录的 tools JSON 尾部。本测试按真实顺序调用：
// 先 snapshotCompletedIteration(1)，再 injectSyntheticToolPair(1, ...)。
//
// Mutation 自证：删掉 injectSyntheticToolPair 里的 persistSyntheticToolToHistory
// 调用（或把 appendFn 换成 no-op）→ 本测试必红。
func TestSyntheticToolPairAfterSnapshotIsAppendedToIterationHistory(t *testing.T) {
	_, sess := newAgentHistorySession(t)
	const turnID = 42
	state := &runState{
		cfg:                RunConfig{Session: sess},
		persistence:        NewPersistenceBridge(sess, 0),
		structuredProgress: &StructuredProgress{TurnID: turnID, Iteration: 1},
	}
	// 迭代 1 先完成快照（模拟 handleFinalResponse 的落库顺序）。
	state.snapshotCompletedIteration(1)
	// 快照之后注入 pre_turn_end 合成工具（模拟 maybeContinueTurn）。
	if err := state.injectSyntheticToolPair(context.Background(), 1,
		"pre_turn_end", "pre_turn_end_1",
		"goal unfinished, continue", "pre_turn_end",
		"PreTurnEnd hook requested another turn",
		"", 0); err != nil {
		t.Fatal(err)
	}

	records, err := sess.GetIterationHistoryByTurns([]uint64{turnID})
	if err != nil {
		t.Fatal(err)
	}
	recs := records[turnID]
	if len(recs) != 1 {
		t.Fatalf("iteration records=%d, want 1", len(recs))
	}
	var tools []IterationToolSnapshot
	if strings.TrimSpace(recs[0].Tools) != "" {
		if err := json.Unmarshal([]byte(recs[0].Tools), &tools); err != nil {
			t.Fatalf("tools JSON is corrupt: %v (%s)", err, recs[0].Tools)
		}
	}
	if len(tools) != 1 {
		t.Fatalf("tools in iteration_history=%d, want 1 (synthetic tool must be appended after snapshot): %+v", len(tools), tools)
	}
	if tools[0].Name != "pre_turn_end" || tools[0].Status != string(ToolDone) || tools[0].Detail != "goal unfinished, continue" {
		t.Fatalf("appended tool snapshot mismatch: %+v", tools[0])
	}
}

// TestSyntheticToolPairBeforeSnapshotIsWrittenOnce 守护互斥契约的另一半：
// 迭代中途注入（记录尚未落盘）时 persistSyntheticToolToHistory found=false
// 静默跳过，工具随后由下一次 snapshotCompletedIteration 从 CompletedTools
// 正常写入 —— 恰好一次，绝不双写。
func TestSyntheticToolPairBeforeSnapshotIsWrittenOnce(t *testing.T) {
	_, sess := newAgentHistorySession(t)
	const turnID = 7
	state := &runState{
		cfg:                RunConfig{Session: sess},
		persistence:        NewPersistenceBridge(sess, 0),
		structuredProgress: &StructuredProgress{TurnID: turnID, Iteration: 1},
	}
	// 快照之前注入（记录不存在 → found=false 跳过，不得 Insert）。
	if err := state.injectSyntheticToolPair(context.Background(), 1,
		"background_task_result", "bg_task-1",
		"cargo check done", "bg:task-1",
		"背景任务 task-1 · done", "", 0); err != nil {
		t.Fatal(err)
	}
	if len(state.messages) != 2 {
		t.Fatalf("injected messages=%d, want 2 (assistant+tool pair)", len(state.messages))
	}
	// 随后的快照把 CompletedTools（含注入工具）正常写入。
	state.snapshotCompletedIteration(1)

	records, err := sess.GetIterationHistoryByTurns([]uint64{turnID})
	if err != nil {
		t.Fatal(err)
	}
	recs := records[turnID]
	if len(recs) != 1 {
		t.Fatalf("iteration records=%d, want 1", len(recs))
	}
	var tools []IterationToolSnapshot
	if err := json.Unmarshal([]byte(recs[0].Tools), &tools); err != nil {
		t.Fatalf("tools JSON is corrupt: %v (%s)", err, recs[0].Tools)
	}
	if len(tools) != 1 {
		t.Fatalf("tools=%d, want exactly 1 (no double-write across the two paths): %+v", len(tools), tools)
	}
	if tools[0].Name != "background_task_result" {
		t.Fatalf("tool name=%q, want background_task_result", tools[0].Name)
	}
}

// TestSyntheticToolPairWithoutStructuredProgressSkipsHistory 非结构化进度
// （structuredProgress=nil）的 Run 不写 iteration_history —— 补写点不得
// panic 或越权 Insert（互斥契约的守卫分支）。
func TestSyntheticToolPairWithoutStructuredProgressSkipsHistory(t *testing.T) {
	_, sess := newAgentHistorySession(t)
	state := &runState{
		cfg:         RunConfig{Session: sess},
		persistence: NewPersistenceBridge(sess, 0),
	}
	if err := state.injectSyntheticToolPair(context.Background(), 1,
		"cron_fired", "cron_1", "tick", "cron", "定时任务已触发", "", 0); err != nil {
		t.Fatal(err)
	}
	records, err := sess.GetIterationHistoryByTurns([]uint64{0})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) > 0 {
		t.Fatalf("iteration_history written without structuredProgress: %+v", records)
	}
	// 消息对本身必须照常落库（LLM 上下文可见性不受影响）。
	msgs, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].ToolCalls[0].Name != "cron_fired" || msgs[1].ToolName != "cron_fired" {
		t.Fatalf("synthetic pair not persisted: %+v", msgs)
	}
}

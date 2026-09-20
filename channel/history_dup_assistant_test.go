package channel

import (
	"testing"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// 2026-09-17 实测（真实会话 chat_07B68B101679）：turn 101 有 **6 个空壳 assistant 占位行**
// （v55 起回复文本存在 iteration_history，session_messages 只留空壳；重启/续跑会各写一条）
// ⇒ 旧实现为【每一条】都追加一个 HistoryMessage（每条都带完整 turnIterMap 迭代 ≈272KB）
// ⇒ /api/history 里同一 turn 的 assistant 行重复 6 次、每份都重复带同样 60 个迭代
// （响应 12.9MB 的 1.4MB 部分；浏览器要解析/渲染 6 份）。
//
// 契约：**一个 turn 只能有一条 assistant HistoryMessage**（迭代只附一次）。
func TestConvert_WithIterations_DuplicateEmptyShellAssistantsCollapse(t *testing.T) {
	msgs := []llm.ChatMessage{{Role: "user", Content: "go", TurnID: 101}}
	for i := 0; i < 6; i++ {
		msgs = append(msgs, llm.ChatMessage{ID: int64(1000 + i), Role: "assistant", Content: "", TurnID: 101})
	}
	turnIterMap := map[uint64][]sqlite.IterationRecord{
		101: {
			{TurnID: 101, Iteration: 1, Tools: `[{"name":"Shell","status":"done"}]`},
			{TurnID: 101, Iteration: 2, Content: "final answer", Tools: "[]"},
		},
	}

	history := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)

	var assistants []HistoryMessage
	for _, h := range history {
		if h.Role == "assistant" {
			assistants = append(assistants, h)
		}
	}
	if len(assistants) != 1 {
		t.Fatalf("REPRO: 同一 turn 的 assistant 行应为 1 条，实际 %d 条（每份都重复带同样迭代 ⇒ 响应膨胀、前端重复渲染）",
			len(assistants))
	}
	if len(assistants[0].Iterations) != 2 {
		t.Errorf("迭代必须完整保留：got %d, want 2", len(assistants[0].Iterations))
	}
}

// 空壳占位行之后紧跟真实最终回复（非空 content）时：合并成**同一条**，且保留真实回复文本。
func TestConvert_WithIterations_EmptyShellThenFinalContentKeepsContent(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "go", TurnID: 7},
		{ID: 10, Role: "assistant", Content: "", TurnID: 7},      // v55 空壳占位
		{ID: 11, Role: "assistant", Content: "done!", TurnID: 7}, // 真实最终回复
	}
	turnIterMap := map[uint64][]sqlite.IterationRecord{
		7: {{TurnID: 7, Iteration: 1, Content: "done", Tools: "[]"}},
	}

	history := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)

	var assistants []HistoryMessage
	for _, h := range history {
		if h.Role == "assistant" {
			assistants = append(assistants, h)
		}
	}
	if len(assistants) != 1 {
		t.Fatalf("assistant 行应为 1 条，实际 %d", len(assistants))
	}
	if assistants[0].Content != "done!" {
		t.Errorf("最终回复文本丢失：content=%q, want %q", assistants[0].Content, "done!")
	}
	if len(assistants[0].Iterations) != 1 {
		t.Errorf("迭代应保留，got %d", len(assistants[0].Iterations))
	}
}

// 多 turn 场景不得互相影响：每个 turn 各自一条，且顺序保持（turn 2 在 turn 3 之前）。
func TestConvert_WithIterations_MultipleTurnsStayOneRowEach(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "a", TurnID: 2},
		{ID: 20, Role: "assistant", Content: "", TurnID: 2},
		{ID: 21, Role: "assistant", Content: "", TurnID: 2},
		{Role: "user", Content: "b", TurnID: 3},
		{ID: 30, Role: "assistant", Content: "", TurnID: 3},
		{ID: 31, Role: "assistant", Content: "", TurnID: 3},
		{ID: 32, Role: "assistant", Content: "", TurnID: 3},
	}
	turnIterMap := map[uint64][]sqlite.IterationRecord{
		2: {{TurnID: 2, Iteration: 1, Content: "two", Tools: "[]"}},
		3: {{TurnID: 3, Iteration: 1, Content: "three", Tools: "[]"}},
	}

	history := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)

	var turnIDs []uint64
	for _, h := range history {
		if h.Role == "assistant" {
			turnIDs = append(turnIDs, h.TurnID)
		}
	}
	if len(turnIDs) != 2 || turnIDs[0] != 2 || turnIDs[1] != 3 {
		t.Fatalf("每个 turn 各一条且顺序保持：got %v, want [2 3]", turnIDs)
	}
}

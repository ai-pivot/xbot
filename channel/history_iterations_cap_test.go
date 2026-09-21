package channel

import (
	"testing"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// ⛔ P0 不变量（用户 2026-09-21 定稿）：「不能有任何 gap，任何 gap 都是破坏线性一致性」。
//
// 历史载荷必须**完整**携带每个 turn 的迭代（1..N）。曾经用 BoundHistoryIterations 把每个
// turn 截到最近 60 个（2026-09-15 为压 payload 体积）—— 那正是 gap 的制造者：客户端手里
// 「上一次的窗口」与之后下发的窗口不相邻 ⇒ 合并出 gap ⇒ 渲染层只能在 gap 处截断 ⇒ 用户
// 看到「历史停在旧位置 / 中间迭代不见 / 新迭代出现即消失」，而且取不回来（当时没有分页
// 通路）。体积/渲染性能归渲染层（TurnBody 迭代级窗口化），不得丢数据。
//
// 判别力：在 get_history / web 快照路径上重新加回任何尾部截断 ⇒ 本例必红。
func TestConvertMessagesToHistoryWithIterations_CarriesAllIterations(t *testing.T) {
	const total = 120
	recs := make([]sqlite.IterationRecord, 0, total)
	for i := 1; i <= total; i++ {
		recs = append(recs, sqlite.IterationRecord{
			TurnID:    1,
			Iteration: i,
			Content:   "iter-content",
		})
	}
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "do it", TurnID: 1},
		{Role: "assistant", TurnID: 1},
	}
	got := ConvertMessagesToHistoryWithIterations(msgs, map[uint64][]sqlite.IterationRecord{1: recs})

	var assistant *HistoryMessage
	for i := range got {
		if got[i].TurnID == 1 && got[i].Role == "assistant" {
			assistant = &got[i]
			break
		}
	}
	if assistant == nil {
		t.Fatalf("no assistant history message for turn 1: %+v", got)
	}
	if assistant.IterationsTruncated != 0 {
		t.Errorf("IterationsTruncated = %d, want 0（禁止任何截断）", assistant.IterationsTruncated)
	}
	if len(assistant.Iterations) != total {
		t.Fatalf("iterations = %d, want %d —— 历史载荷必须完整下发", len(assistant.Iterations), total)
	}
	if first := assistant.Iterations[0].Iteration; first != 1 {
		t.Errorf("首个迭代 = %d, want 1（必须从 1 开始，否则渲染起点就断）", first)
	}
	if last := assistant.Iterations[len(assistant.Iterations)-1].Iteration; last != total {
		t.Errorf("末个迭代 = %d, want %d", last, total)
	}
	for i := 1; i < len(assistant.Iterations); i++ {
		if assistant.Iterations[i].Iteration != assistant.Iterations[i-1].Iteration+1 {
			t.Fatalf("历史载荷出现 gap：iter %d 之后是 %d（任何 gap 都破坏线性一致性）",
				assistant.Iterations[i-1].Iteration, assistant.Iterations[i].Iteration)
		}
	}
}

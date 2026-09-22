package channel

import (
	"testing"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// ⛔ 不变量（用户 2026-09-21 定稿）：「**不能有任何 gap，任何 gap 都是破坏线性一致性**」。
//
// 历史载荷必须**完整**携带每个 turn 的迭代 1..N —— 这里**禁止**任何尾部截断。
// 历史教训：2026-09-15 的 `1b1f41e9` 用 BoundHistoryIterations 把每个 turn 截到最近 60 个
// （为压 payload 体积），用户实测 `turn-1-c` 的 `iter-range=60-119`、**1..59 永久缺失**
// （当时也没有任何取回通路）。体积/渲染性能归渲染层（TurnBody 迭代级窗口化），不得丢数据。
//
// 判别力：在 get_history / web 快照路径上加回任何尾部截断 ⇒ 本例必红。
func TestConvertMessagesToHistoryWithIterations_CarriesAllIterations(t *testing.T) {
	const total = 120
	recs := make([]sqlite.IterationRecord, 0, total)
	for i := 1; i <= total; i++ {
		recs = append(recs, sqlite.IterationRecord{TurnID: 1, Iteration: i, Content: "iter-content"})
	}
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "do it", TurnID: 1},
		{Role: "assistant", TurnID: 1},
	}
	got := ConvertMessagesToHistoryWithIterations(msgs, map[uint64][]sqlite.IterationRecord{1: recs})
	for i := range got {
		if got[i].TurnID != 1 || got[i].Role != "assistant" {
			continue
		}
		if n := len(got[i].Iterations); n != total {
			t.Fatalf("iterations = %d, want %d —— 历史载荷必须完整下发（禁止任何尾部截断）", n, total)
		}
		if got[i].IterationsTruncated != 0 {
			t.Errorf("IterationsTruncated = %d, want 0", got[i].IterationsTruncated)
		}
		first := got[i].Iterations[0].Iteration
		last := got[i].Iterations[total-1].Iteration
		if first != 1 || last != total {
			t.Fatalf("iteration range = %d..%d, want 1..%d（必须从 1 开始，上一条下一条才接得上）", first, last, total)
		}
		for k := 1; k < total; k++ {
			if got[i].Iterations[k].Iteration != got[i].Iterations[k-1].Iteration+1 {
				t.Fatalf("载荷出现 gap：%d 之后是 %d", got[i].Iterations[k-1].Iteration, got[i].Iterations[k].Iteration)
			}
		}
		return
	}
	t.Fatalf("no assistant history message for turn 1: %+v", got)
}

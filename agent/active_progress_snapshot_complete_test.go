package agent

import (
	"fmt"
	"testing"

	"xbot/protocol"
)

// ⛔ 不变量（用户 2026-09-21）：「不能有任何 gap」—— active_progress 快照的
// iteration_history **必须完整**（1..N 全给）。历史教训：2026-09-17 的 `5b43c212` 为压体积
// 把 FetchAll 截到最近 60 个迭代（当时现场 1964 个迭代 ⇒ 11.2MB 快照），代价是**客户端
// 手里的窗口与权威窗口不相邻** ⇒ 拼接出 gap ⇒ 渲染只能在 gap 处截断（用户看到「历史停在
// 旧位置 / 中间迭代不见」）。体积/渲染性能归渲染层（TurnBody 迭代级窗口化），不得丢数据。
//
// 判别力：把任何尾部截断加回 GetActiveProgress ⇒ 本例必红。
func TestGetActiveProgress_FetchAllIsComplete(t *testing.T) {
	a := NewTestAgent()
	key := "web:chat-big"
	iters := make([]protocol.ProgressEvent, 0, 500)
	for i := 1; i <= 500; i++ {
		iters = append(iters, protocol.ProgressEvent{Iteration: i, Phase: "tool_exec", Content: fmt.Sprintf("iter-%d", i)})
	}
	a.iterationHistories.Store(key, &iters)
	a.lastProgressSnapshot.Store(key, &protocol.ProgressEvent{ChatID: key, Phase: "tool_exec", TurnID: 3})

	res := a.GetActiveProgress("web", "chat-big", protocol.FetchAll())
	if res == nil {
		t.Fatal("GetActiveProgress returned nil")
	}
	if len(res.IterationHistory) != 500 {
		t.Fatalf("IterationHistory len = %d, want 500 —— 快照必须完整（任何尾部截断都会让客户端窗口与权威窗口不相邻 ⇒ gap）", len(res.IterationHistory))
	}
	if first, last := res.IterationHistory[0].Iteration, res.IterationHistory[499].Iteration; first != 1 || last != 500 {
		t.Errorf("iteration range = %d..%d, want 1..500", first, last)
	}
	for i := 1; i < len(res.IterationHistory); i++ {
		if res.IterationHistory[i].Iteration != res.IterationHistory[i-1].Iteration+1 {
			t.Fatalf("快照出现 gap：%d 之后是 %d", res.IterationHistory[i-1].Iteration, res.IterationHistory[i].Iteration)
		}
	}
}

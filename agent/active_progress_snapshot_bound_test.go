package agent

import (
	"fmt"
	"testing"

	"xbot/protocol"
)

// ⛔ P0 不变量（用户 2026-09-21 定稿）：「不能有任何 gap，任何 gap 都是破坏线性一致性」。
//
// active_progress 快照是「切过去时把进行中 turn 的画面撑起来」的权威来源之一 —— 它
// **必须完整**（该 turn 的迭代 1..N 全给）。曾经为压体积把 FetchAll 截到最近 60 个迭代
// （2026-09-17），代价是客户端手里的窗口与之后下发的窗口**不相邻** ⇒ 合并出 gap ⇒
// 渲染层只能在 gap 处截断 ⇒ 用户看到「历史停在旧位置 / 中间很多迭代不见 / 新迭代出现即
// 消失」，而且**取不回来**（当时没有分页通路）。体积与渲染性能归渲染层（TurnBody 的迭代
// 级窗口化：只挂载视口附近的块），绝不以丢数据换体积。
//
// 判别力：把任何尾部截断加回 GetActiveProgress ⇒ 本例必红。
func TestGetActiveProgress_FetchAllIsComplete(t *testing.T) {
	a := NewTestAgent()
	key := "web:chat-big"
	iters := make([]protocol.ProgressEvent, 0, 500)
	for i := 1; i <= 500; i++ {
		iters = append(iters, protocol.ProgressEvent{
			Iteration: i,
			Phase:     "tool_exec",
			Content:   fmt.Sprintf("iter-%d", i),
		})
	}
	a.iterationHistories.Store(key, &iters)
	// GetActiveProgress 的入口要求存在 lastProgressSnapshot（否则返回 nil）
	a.lastProgressSnapshot.Store(key, &protocol.ProgressEvent{ChatID: key, Phase: "tool_exec", TurnID: 3})

	res := a.GetActiveProgress("web", "chat-big", protocol.FetchAll())
	if res == nil {
		t.Fatal("GetActiveProgress returned nil")
	}
	if len(res.IterationHistory) != 500 {
		t.Fatalf("IterationHistory len = %d, want 500 —— 快照必须完整（任何尾部截断都会让客户端窗口与权威窗口不相邻 ⇒ gap）",
			len(res.IterationHistory))
	}
	if first := res.IterationHistory[0].Iteration; first != 1 {
		t.Errorf("首个迭代 = %d, want 1（必须从 1 开始，否则渲染起点就断）", first)
	}
	if last := res.IterationHistory[len(res.IterationHistory)-1].Iteration; last != 500 {
		t.Errorf("末个迭代 = %d, want 500", last)
	}
	for i := 1; i < len(res.IterationHistory); i++ {
		if res.IterationHistory[i].Iteration != res.IterationHistory[i-1].Iteration+1 {
			t.Fatalf("快照出现 gap：iter %d 之后是 %d（任何 gap 都破坏线性一致性）",
				res.IterationHistory[i-1].Iteration, res.IterationHistory[i].Iteration)
		}
	}
}

// 短历史同样必须完整（正常会话行为不变）。
func TestGetActiveProgress_FetchAllKeepsShortHistory(t *testing.T) {
	a := NewTestAgent()
	key := "web:chat-small"
	iters := make([]protocol.ProgressEvent, 0, 5)
	for i := 1; i <= 5; i++ {
		iters = append(iters, protocol.ProgressEvent{Iteration: i, Phase: "tool_exec"})
	}
	a.iterationHistories.Store(key, &iters)
	a.lastProgressSnapshot.Store(key, &protocol.ProgressEvent{ChatID: key, Phase: "tool_exec", TurnID: 3})

	res := a.GetActiveProgress("web", "chat-small", protocol.FetchAll())
	if res == nil {
		t.Fatal("GetActiveProgress returned nil")
	}
	if len(res.IterationHistory) != 5 {
		t.Errorf("IterationHistory len = %d, want 5（短历史不得被截断）", len(res.IterationHistory))
	}
}

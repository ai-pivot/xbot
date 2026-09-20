package agent

import (
	"fmt"
	"testing"

	"xbot/protocol"
)

// 2026-09-17 我自己的 E2E 实测根因：chat_07B68B101679 的 turn 101 有 1964 个迭代，
// FetchAll 快照就带 **11.2MB** iteration_history（/api/history 总量 12.9MB）⇒ 前端物化
// 1964 个迭代块 / 50,633 DOM 节点 ⇒ 切会话 7175ms、长任务 2997ms。
// 契约：FetchAll 也必须**有界**（只给尾部最近 N 个），且必须保留**最新**的迭代号
// （进行中 turn 的 live 视图靠尾部撑起来）。
func TestGetActiveProgress_FetchAllIsBounded(t *testing.T) {
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
	if len(res.IterationHistory) != maxActiveSnapshotIterations {
		t.Errorf("IterationHistory len = %d, want %d — FetchAll 快照必须有界（否则切会话一次 11MB）",
			len(res.IterationHistory), maxActiveSnapshotIterations)
	}
	if n := len(res.IterationHistory); n > 0 {
		if last := res.IterationHistory[n-1].Iteration; last != 500 {
			t.Errorf("尾部必须保留最新迭代：last.Iteration = %d, want 500", last)
		}
		first := res.IterationHistory[0].Iteration
		if want := 500 - maxActiveSnapshotIterations + 1; first != want {
			t.Errorf("尾部起点 = %d, want %d", first, want)
		}
	}
}

// 迭代数不超过上限时**不得**截断（正常会话行为不变）。
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

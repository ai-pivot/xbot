package agent

import (
	"fmt"
	"testing"

	"xbot/channel"
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

// P1 语义演进（docs/plan-history-fold-windowing.md §3.5 D5 + §0 铁律演进）：
// **折叠视图**（Agent.GetActiveProgressFolded，REST 历史路径的 active_progress）允许
// 只下发尾部 K 个展示区域 + 省略工具详情，但代价是**必须显式声明**未下发区域数
// （IterationRegionsBefore）——「未下发」由计数声明且有确定取回通路（POST /api/regions）
// ⇒ 不构成静默缺失（旧的「尾部截断且无通路」才被禁）。原方法（上面的 FetchAllIsComplete）
// 仍然必须完整。
//
// 判别力（mutation）：① 变体漏设 IterationRegionsBefore ⇒ 本例必红；
//
//	② 窗口把工具组劈开（迭代号不连续 / 工具组成员缺失）⇒ 必红。
func TestGetActiveProgressFolded_DeclaresUnsentRegionsWithoutGap(t *testing.T) {
	a := NewTestAgent()
	key := "web:chat-fold-big"
	// 150 个「无工具文本」迭代 ⇒ 150 个展示区域（每个独立成块）。
	total := 150
	iters := make([]protocol.ProgressEvent, 0, total)
	for i := 1; i <= total; i++ {
		iters = append(iters, protocol.ProgressEvent{
			Iteration: i,
			Phase:     "tool_exec",
			Content:   fmt.Sprintf("iter-%d", i),
		})
	}
	a.iterationHistories.Store(key, &iters)
	a.lastProgressSnapshot.Store(key, &protocol.ProgressEvent{ChatID: key, Phase: "tool_exec", TurnID: 3})

	// 原方法：完整（不因变体存在而改变）。
	full := a.GetActiveProgress("web", "chat-fold-big", protocol.FetchAll())
	if len(full.IterationHistory) != total || full.IterationRegionsBefore != 0 {
		t.Fatalf("原方法必须完整：len=%d regions_before=%d", len(full.IterationHistory), full.IterationRegionsBefore)
	}

	folded := a.GetActiveProgressFolded("web", "chat-fold-big", protocol.FetchAll())
	if folded == nil {
		t.Fatal("GetActiveProgressFolded returned nil")
	}
	want := channel.HistoryRegionWindow
	if folded.IterationRegionsBefore != total-want {
		t.Fatalf("IterationRegionsBefore = %d, want %d —— 未下发区域必须显式声明（= 区域总数 − 窗口）",
			folded.IterationRegionsBefore, total-want)
	}
	if len(folded.IterationHistory) != want {
		t.Fatalf("窗口 len = %d, want %d", len(folded.IterationHistory), want)
	}
	// 窗口是**连续**迭代号区间（不是「抽头」）：越界声明 + 连续 ⇒ 可完整取回、无洞。
	first := folded.IterationHistory[0].Iteration
	if first != total-want+1 || folded.IterationHistory[len(folded.IterationHistory)-1].Iteration != total {
		t.Fatalf("窗口区间 = %d..%d, want %d..%d",
			first, folded.IterationHistory[len(folded.IterationHistory)-1].Iteration, total-want+1, total)
	}
	for i := 1; i < len(folded.IterationHistory); i++ {
		if folded.IterationHistory[i].Iteration != folded.IterationHistory[i-1].Iteration+1 {
			t.Fatalf("窗口内出现 gap：%d 之后是 %d（区域/窗口必须原子对齐，绝不劈开）",
				folded.IterationHistory[i-1].Iteration, folded.IterationHistory[i].Iteration)
		}
	}
	// 窗口内区域数恰为 K（窗口边界 ≡ 区域边界）。
	if got := len(channel.RegionRuns(activeProgressRecords(folded.IterationHistory))); got != want {
		t.Errorf("窗口内区域数 = %d, want %d", got, want)
	}
}

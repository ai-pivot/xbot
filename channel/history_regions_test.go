package channel

import (
	"fmt"
	"testing"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// =============================================================================
// T5：区域窗口边界（channel 层）—— 区域原子 + RegionsBefore 计数 + 拼接连续
//
// 契约来源：docs/plan-history-fold-windowing.md §3.1/§3.2/§4-3：
//   - 区域 = 前端渲染块（折叠的工具组算 1 个）；
//   - 窗口/段边界**永远对齐区域边界**，工具组永不劈开；
//   - 任何响应窗口内迭代号连续；regions_before 恒与实际未下发区域数一致。
//
// 本文件只钉 channel 层（RegionWindow + 装配）。区域段端点在 serverapp 层，
// 其切分测试见 serverapp/callbacks_history_regions_test.go。
// =============================================================================

// regionShapeFixture 造 R 个展示区域的记录序列：每第 2 个区域是「带文本 head +
// 纯工具成员」的工具组（2 迭代 / 1 区域），其余是单迭代纯文本区域。
func regionShapeFixture(regions int) []sqlite.IterationRecord {
	recs := make([]sqlite.IterationRecord, 0, regions*2)
	it := 0
	for r := 0; r < regions; r++ {
		it++
		if r%3 == 1 {
			recs = append(recs, regionRec(it, fmt.Sprintf("head-%d", r), plainTool(fmt.Sprintf("H%d", r))))
			it++
			recs = append(recs, regionRec(it, "", plainTool(fmt.Sprintf("F%d", r))))
			continue
		}
		recs = append(recs, regionRec(it, fmt.Sprintf("text-%d", r)))
	}
	return recs
}

// T5：尾部 HistoryRegionWindow 个区域的窗口起点必须是**区域 head**，且
// RegionsBefore 与实际未下发区域数一致；区域数 ≤ 窗口 ⇒ 全量 + RegionsBefore=0。
//
// mutation 判别力：把窗口按「迭代号减 100」之类的方式切（而不是按区域边界切），
// 或漏数 regionsBefore，本条必红。
func TestRegionWindow_HeadBoundaryAndRegionsBefore(t *testing.T) {
	for _, regions := range []int{0, 1, 2, 99, 100, 101, 120, 205} {
		regions := regions
		t.Run(fmt.Sprintf("regions=%d", regions), func(t *testing.T) {
			recs := regionShapeFixture(regions)
			runs := RegionRuns(recs)
			if len(runs) != regions {
				t.Fatalf("夹具区域数 = %d, want %d", len(runs), regions)
			}
			window, before := RegionWindow(recs, HistoryRegionWindow)

			wantBefore := regions - HistoryRegionWindow
			if wantBefore < 0 {
				wantBefore = 0
			}
			if before != wantBefore {
				t.Fatalf("regionsBefore = %d, want %d（0 = 已完整下发）", before, wantBefore)
			}
			if len(runs) <= HistoryRegionWindow {
				if len(window) != len(recs) {
					t.Fatalf("区域数 %d ≤ 窗口 %d ⇒ 必须全量下发：got %d 迭代, want %d",
						len(runs), HistoryRegionWindow, len(window), len(recs))
				}
				return
			}
			if len(window) == 0 {
				t.Fatal("窗口为空但仍有更早区域 —— 取回通路断裂")
			}
			head := runs[regions-HistoryRegionWindow]
			if window[0].Iteration != head.FirstIteration {
				t.Fatalf("窗口首迭代 = %d, want 区域 head = %d —— 边界必须对齐区域（工具组永不劈开）",
					window[0].Iteration, head.FirstIteration)
			}
			if head.IterationCount > 1 && window[0].Content == "" && len(runs) > 0 {
				// 工具的后续成员被当成了窗口起点 ⇒ 工具组被劈开（head 必是第一个成员）。
				t.Fatalf("窗口起点落在工具组内部：%d 是纯工具成员，区域 head 应为 %d",
					window[0].Iteration, head.FirstIteration)
			}
			last := recs[len(recs)-1].Iteration
			if window[len(window)-1].Iteration != last {
				t.Fatalf("窗口末迭代 = %d, want turn 末迭代 = %d（必须是尾部窗口）",
					window[len(window)-1].Iteration, last)
			}
			for i := 1; i < len(window); i++ {
				if window[i].Iteration != window[i-1].Iteration+1 {
					t.Fatalf("窗口内出现 gap：%d 之后是 %d", window[i-1].Iteration, window[i].Iteration)
				}
			}
			// 被裁掉的前 wantBefore 个区域的迭代，一个都不许出现在窗口里。
			cut := make(map[int]bool)
			for _, run := range runs[:regions-HistoryRegionWindow] {
				for n := run.FirstIteration; n <= run.LastIteration; n++ {
					cut[n] = true
				}
			}
			if len(cut) != window[0].Iteration-1 {
				t.Fatalf("被裁迭代数 = %d, want %d（迭代号连续 ⇒ 前 N-1 个即被裁）", len(cut), window[0].Iteration-1)
			}
			for _, rec := range window {
				if cut[rec.Iteration] {
					t.Fatalf("被裁区域的迭代 %d 出现在窗口里", rec.Iteration)
				}
			}
		})
	}
}

// T5：RegionWindow 的退化与边界入参（k<=0 ⇒ 空窗口 + 全部算作更早区域；空输入）。
func TestRegionWindow_DegenerateInputs(t *testing.T) {
	recs := regionShapeFixture(5)
	if window, before := RegionWindow(nil, HistoryRegionWindow); window != nil || before != 0 {
		t.Fatalf("空输入 ⇒ (nil, 0)，got (%v, %d)", window, before)
	}
	if window, before := RegionWindow(recs, 0); len(window) != 0 || before != 5 {
		t.Fatalf("k=0 ⇒ 空窗口 + regionsBefore=区域总数，got (%d 迭代, %d)", len(window), before)
	}
}

// T1（装配层）：RegionsBefore 是**每个 turn 各自**的声明 —— 小 turn 全量(0)、
// 巨型 turn 声明被裁区域数；窗口不跨 turn 串味。
func TestConvert_ViewTrue_PerTurnRegionsBefore(t *testing.T) {
	small := regionShapeFixture(3)
	large := regionShapeFixture(130)
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "small", TurnID: 1},
		{ID: 11, Role: "assistant", Content: "s", TurnID: 1},
		{Role: "user", Content: "large", TurnID: 2},
		{ID: 21, Role: "assistant", Content: "l", TurnID: 2},
	}
	turnIterMap := map[uint64][]sqlite.IterationRecord{1: small, 2: large}

	got := ConvertMessagesToHistoryWithIterationsView(msgs, turnIterMap, true)
	smallRow := assistantRowOf(t, got, 1)
	largeRow := assistantRowOf(t, got, 2)

	if smallRow.RegionsBefore != 0 {
		t.Errorf("小 turn（3 区域 ≤ 窗口 %d）RegionsBefore = %d, want 0", HistoryRegionWindow, smallRow.RegionsBefore)
	}
	if len(smallRow.Iterations) != len(small) {
		t.Errorf("小 turn 必须全量下发：got %d, want %d", len(smallRow.Iterations), len(small))
	}
	if want := 130 - HistoryRegionWindow; largeRow.RegionsBefore != want {
		t.Errorf("巨型 turn RegionsBefore = %d, want %d", largeRow.RegionsBefore, want)
	}
	// 小 turn 的行不得被大 turn 的窗口影响（各自独立窗口）。
	if smallRow.Iterations[0].Iteration != small[0].Iteration {
		t.Errorf("小 turn 迭代起点漂移：%d, want %d", smallRow.Iterations[0].Iteration, small[0].Iteration)
	}
}

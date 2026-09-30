package serverapp

import (
	"bytes"
	"fmt"
	"os"
	"testing"

	"xbot/channel"
	"xbot/storage/sqlite"
)

// =============================================================================
// POST /api/regions 的段切分（serverapp 层）
//
// 契约：docs/plan-history-fold-windowing.md §3.2 D2。
//   - 输入 = GetIterationHistoryBeforeRange 的输出（严格早于 beforeIter、升序）；
//   - 输出 = 尾部 regionLimit 个**展示区域**组成的段（区域原子 ⇒ 段是连续迭代号区间，
//     绝不劈开折叠工具组）+ 段外更早区域数（0 = 已到该 turn 头部）；
//   - regionLimit <= 0 ⇒ 默认；> MaxHistoryRegionRequest ⇒ 钳到硬上限（R3）。
// =============================================================================

// segRegionFixture 造 R 个展示区域的记录序列：每第 2 个区域是「带文本 head +
// 纯工具成员」的工具组（2 迭代 / 1 区域），其余是单迭代纯文本区域。
func segRegionFixture(regions int) []sqlite.IterationRecord {
	recs := make([]sqlite.IterationRecord, 0, regions*2)
	it := 0
	for r := 0; r < regions; r++ {
		it++
		if r%2 == 1 {
			recs = append(recs, sqlite.IterationRecord{
				TurnID: 1, Iteration: it, Content: fmt.Sprintf("head-%d", r),
				Tools: fmt.Sprintf(`[{"name":"H%d","status":"done","elapsed_ms":3,"summary":"s","args":"{}","detail":"d"}]`, r),
			})
			it++
			recs = append(recs, sqlite.IterationRecord{
				TurnID: 1, Iteration: it, Content: "",
				Tools: fmt.Sprintf(`[{"name":"F%d","status":"done","elapsed_ms":4,"summary":"s","args":"{}","detail":"d"}]`, r),
			})
			continue
		}
		recs = append(recs, sqlite.IterationRecord{
			TurnID: 1, Iteration: it, Content: fmt.Sprintf("text-%d", r), Tools: "[]",
		})
	}
	return recs
}

// 基本段切分：尾部 regionLimit 个区域；区域数 ≤ regionLimit ⇒ 全量段 + regionsBefore=0。
//
// mutation 判别力：把段起点按迭代号硬算（如「last - regionLimit」）而不是按区域边界累加，
// 本条的区域头断言与迭代数断言必红。
func TestHistoryRegionSegment_TailLimitAndFullSegment(t *testing.T) {
	recs := segRegionFixture(15) // 22 迭代 / 15 区域
	runs := channel.RegionRuns(recs)
	if len(runs) != 15 || len(recs) != 22 {
		t.Fatalf("夹具 = %d 区域 / %d 迭代, want 15 / 22", len(runs), len(recs))
	}

	// 尾部 3 区域：区域 13/14/15 = 迭代 19..22（区域 13 = 单文本迭代 19）。
	seg, before := historyRegionSegment(recs, 3)
	if before != 12 {
		t.Fatalf("regionsBefore = %d, want 12（15 - 3）", before)
	}
	if len(seg) != 4 {
		t.Fatalf("段内迭代数 = %d, want 4（迭代 19..22）", len(seg))
	}
	if seg[0].Iteration != 19 || seg[0].Iteration != runs[12].FirstIteration {
		t.Fatalf("段起点 = %d（区域 13 的 head = %d）, want 19 —— 段边界必须对齐区域", seg[0].Iteration, runs[12].FirstIteration)
	}
	if seg[len(seg)-1].Iteration != 22 {
		t.Fatalf("段末迭代 = %d, want 22（段必须紧贴 beforeIter 的下一条）", seg[len(seg)-1].Iteration)
	}
	for i := 1; i < len(seg); i++ {
		if seg[i].Iteration != seg[i-1].Iteration+1 {
			t.Fatalf("段内出现 gap：%d 之后是 %d", seg[i-1].Iteration, seg[i].Iteration)
		}
	}

	// regionLimit ≥ 区域总数 ⇒ 全量段 + regionsBefore=0（该 turn 到顶）。
	all, before := historyRegionSegment(recs, 15)
	if before != 0 || len(all) != 22 {
		t.Fatalf("全量段 = (%d 迭代, regionsBefore=%d), want (22, 0)", len(all), before)
	}
	// regionLimit <= 0 ⇒ 服务端默认（= 100）⇒ 本夹具同样全量。
	all, before = historyRegionSegment(recs, 0)
	if before != 0 || len(all) != 22 {
		t.Fatalf("regionLimit=0 ⇒ 默认段 = (%d 迭代, regionsBefore=%d), want (22, 0)", len(all), before)
	}
	// 空输入（该 turn 没有更早迭代）⇒ (nil, 0)，绝不 panic。
	if seg, before := historyRegionSegment(nil, 5); seg != nil || before != 0 {
		t.Fatalf("空输入 ⇒ (nil, 0)，got (%v, %d)", seg, before)
	}
}

// 服务端硬上限：regionLimit > MaxHistoryRegionRequest ⇒ 钳到上限。
//
// mutation 判别力：去掉钳制（或钳成别的值）⇒ 本条必红。
func TestHistoryRegionSegment_ClampsRegionLimit(t *testing.T) {
	recs := segRegionFixture(150)
	runs := channel.RegionRuns(recs)
	if len(runs) != 150 {
		t.Fatalf("夹具区域数 = %d, want 150", len(runs))
	}
	seg, before := historyRegionSegment(recs, MaxHistoryRegionRequest+900)

	wantBefore := 150 - MaxHistoryRegionRequest
	if before != wantBefore {
		t.Fatalf("超大 regionLimit ⇒ 仍被钳到 %d：regionsBefore = %d, want %d", MaxHistoryRegionRequest, before, wantBefore)
	}
	if len(seg) >= len(recs) {
		t.Fatalf("超大 regionLimit 未被钳制：返回了 %d 迭代（全量 %d）", len(seg), len(recs))
	}
	wantStart := runs[wantBefore].FirstIteration
	if seg[0].Iteration != wantStart {
		t.Fatalf("段起点 = %d, want %d（尾部 100 区域的第一个 head）", seg[0].Iteration, wantStart)
	}
	if seg[len(seg)-1].Iteration != recs[len(recs)-1].Iteration {
		t.Fatalf("段末迭代 = %d, want turn 末迭代 %d", seg[len(seg)-1].Iteration, recs[len(recs)-1].Iteration)
	}
}

// 段内迭代是**轻字段形态**（ToolsFolded 标记 + 详情省略，GenUI 例外），
// 且段的区域原子性与窗口一致（起点必是区域 head）。
//
// mutation 判别力：historyRegionSegment 里误传 MapIterationRecord(rec, false)
// ⇒ tools_folded/详情省略断言必红。
func TestHistoryRegionSegment_LightShape(t *testing.T) {
	recs := segRegionFixture(15)
	seg, _ := historyRegionSegment(recs, 15) // 全量段
	byIter := make(map[int]channel.HistoryIteration, len(seg))
	for _, it := range seg {
		byIter[it.Iteration] = it
	}
	// 迭代 2 = 工具组 head（带文本）⇒ 内容保留、工具折叠、打标。
	head := byIter[2]
	if head.Content != "head-1" {
		t.Errorf("段内 head 迭代文本必须完整：%+v", head)
	}
	if !head.ToolsFolded || len(head.Tools) != 1 {
		t.Fatalf("段内工具迭代必须打 ToolsFolded 标记：%+v", head)
	}
	if head.Tools[0].Summary != "" || head.Tools[0].Args != "" || head.Tools[0].Detail != "" {
		t.Errorf("段内工具详情必须省略（轻字段形态）：%+v", head.Tools[0])
	}
	// pill 轻字段保留。
	if head.Tools[0].Name != "H1" || head.Tools[0].Status != "done" || head.Tools[0].Iteration != 2 {
		t.Errorf("pill 轻字段必须保留：%+v", head.Tools[0])
	}
	// 纯文本迭代：无工具、不打标。
	if it := byIter[1]; it.ToolsFolded || len(it.Tools) != 0 || it.Content != "text-0" {
		t.Errorf("纯文本迭代形态异常：%+v", it)
	}
}

// T1「可完整取回」：初始窗口（尾部 100 区域）+ 反复向旧方向取段 ⇒ 拼回 1..N 全量、
// 无重复、无洞，regionsBefore 单调收敛到 0。
//
// mutation 判别力：段边界落到区域中间（劈开工具组）或 regionsBefore 计数错 ⇒
// 覆盖/重复/不收敛断言必红。
func TestHistoryRegionSegment_ReconstructFullTurn(t *testing.T) {
	recs := segRegionFixture(150)
	total := recs[len(recs)-1].Iteration

	seen := map[int]bool{}
	window, before := channel.RegionWindow(recs, channel.HistoryRegionWindow)
	if len(window) == 0 {
		t.Fatal("初始窗口为空")
	}
	for _, rec := range window {
		seen[rec.Iteration] = true
	}
	if want := total - len(window); before != 150-channel.HistoryRegionWindow || want < 0 {
		t.Fatalf("初始 RegionsBefore = %d, want %d", before, 150-channel.HistoryRegionWindow)
	}
	beforeIter := window[0].Iteration

	for steps := 0; before > 0; steps++ {
		if steps > 50 {
			t.Fatalf("取回不收敛（regionsBefore 停在 %d）", before)
		}
		// 模拟 GetIterationHistoryBeforeRange：严格更早、升序。
		older := make([]sqlite.IterationRecord, 0, len(recs))
		for _, rec := range recs {
			if rec.Iteration < beforeIter {
				older = append(older, rec)
			}
		}
		seg, prev := historyRegionSegment(older, DefaultHistoryRegionRequest)
		if len(seg) == 0 {
			t.Fatalf("段为空但 regionsBefore = %d —— 取回通路断裂（迭代会永久不可见）", before)
		}
		for _, it := range seg {
			if seen[it.Iteration] {
				t.Fatalf("迭代 %d 被重复下发（段重叠 ⇒ 区域边界漂移）", it.Iteration)
			}
			seen[it.Iteration] = true
		}
		beforeIter = seg[0].Iteration
		if prev >= before {
			t.Fatalf("regionsBefore 未收敛：%d → %d", before, prev)
		}
		before = prev
	}

	if len(seen) != total {
		t.Fatalf("拼回后迭代数 = %d, want %d（全量）", len(seen), total)
	}
	for n := 1; n <= total; n++ {
		if !seen[n] {
			t.Fatalf("拼回后缺迭代 %d —— 完整性与线性一致被破坏", n)
		}
	}
}

// =============================================================================
// 守护：Web SSE/WS 推送快照（callbacks.GetActiveProgress 回调）必须走折叠视图
//
// 该回调的全部消费方是 Web SSE/WS 推送（web_sse.go:266 SSE fallback / :756 心跳快照
// 入 ring（断线重连重放）/ web.go:1477 重连 replay 补发）——若退回 FetchAll 全量，
// busy 大 turn 的全部已完成迭代（1,661 迭代 ≈ MB 级）会经 SSE 推给浏览器，绕过整个
// 折叠视图架构（用户 2026-09-30：「确保 Web 端以后都不会拉全量了吧」）。
//
// 行为级测试需要 agent 私有字段（lastProgressSnapshot/iterationHistories）注入快照
// ——无公开 API —— 故用源码断言钉死装配行（项目先例：前端 noLegacyFoldFormat.test.tsx
// 的 FORBIDDEN_CODE 源码扫描）。mutation 判别力：把回调体改回 ag.GetActiveProgress(...)
// ⇒ 本条必红。
func TestCallbacksGetActiveProgressUsesFoldedVariant(t *testing.T) {
	src, err := os.ReadFile("callbacks.go")
	if err != nil {
		t.Fatal(err)
	}
	idx := bytes.Index(src, []byte("callbacks.GetActiveProgress = func"))
	if idx < 0 {
		t.Fatal("callbacks.GetActiveProgress 回调注册不存在（被删/改名 ⇒ 请同步本守护与 web_sse 消费方）")
	}
	window := src[idx : idx+700]
	if bytes.Contains(window, []byte("ag.GetActiveProgressFolded(")) {
		return // 装配仍走折叠视图
	}
	if bytes.Contains(window, []byte("ag.GetActiveProgress(")) {
		t.Fatalf("callbacks.GetActiveProgress 回调退回了全量 GetActiveProgress —— Web SSE/WS 推送快照不再走折叠视图（大 turn MB 级载荷回归）。窗口内容:\n%s", window)
	}
	t.Fatalf("callbacks.GetActiveProgress 回调体内未发现 GetActiveProgressFolded 调用 —— 请确认装配仍走折叠视图。窗口内容:\n%s", window)
}

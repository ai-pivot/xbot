package channel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
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

// =============================================================================
// 折叠视图（foldView=true）演进：区域窗口（D2）+ 工具轻字段化（D3）
//
// 铁律演进（方案 docs/plan-history-fold-windowing.md §0）：迭代**存在性**仍必须
// 完整且线性一致 —— 任何响应窗口内迭代号连续、被裁区域由 RegionsBefore 显式声明
// （≠ gap），且可经 POST /api/regions 完整取回；迭代的**工具详情载荷**允许默认省略，
// 条件是：① 带 tools_folded 标记；② 有 (turn_id, iteration) 详情取回端点。
// =============================================================================

// assistantRowOf 取 turn 的唯一 assistant 行（一个 turn 只能有一条，见
// history_dup_assistant_test.go）。
func assistantRowOf(t *testing.T, history []HistoryMessage, turnID uint64) HistoryMessage {
	t.Helper()
	var found []HistoryMessage
	for _, h := range history {
		if h.Role == "assistant" && h.TurnID == turnID {
			found = append(found, h)
		}
	}
	if len(found) != 1 {
		t.Fatalf("turn %d 的 assistant 行应为 1 条，实际 %d 条：%+v", turnID, len(found), history)
	}
	return found[0]
}

// foldTurnFixture 造一个 140 迭代 / **120 个展示区域**的 turn（HistoryRegionWindow=100
// ⇒ 恰好被裁掉前 20 个区域）：
//
//	迭代 1..100   ：纯文本（各自 1 个区域）                      → 区域 1..100
//	迭代 101..138 ：19 个「带文本 head + 纯工具成员」的工具组（每组 2 迭代 / 1 区域）；
//	                迭代 101 的 head 同时带**普通工具 + GenUI 工具**（折叠时 GenUI 豁免）
//	迭代 139..140 ：GenUI-only 工具组（139 带文本 head，140 为纯工具成员）→ 1 个区域
//
// ⇒ 区域 120 个；尾部 100 区域 = 迭代 21..140；RegionsBefore = 20。
func foldTurnFixture() []sqlite.IterationRecord {
	recs := make([]sqlite.IterationRecord, 0, 140)
	for i := 1; i <= 100; i++ {
		recs = append(recs, regionRec(i, fmt.Sprintf("text-%d", i)))
	}
	for r := 0; r < 19; r++ {
		it := 101 + 2*r
		if r == 0 {
			recs = append(recs, regionRec(it, "head-0", plainTool("H0"), genuiTool("GH0")))
		} else {
			recs = append(recs, regionRec(it, fmt.Sprintf("head-%d", r), plainTool(fmt.Sprintf("H%d", r))))
		}
		recs = append(recs, regionRec(it+1, "", plainTool(fmt.Sprintf("F%d", r))))
	}
	recs = append(recs, regionRec(139, "genui-head", genuiTool("G0")))
	recs = append(recs, regionRec(140, "", genuiTool("G1")))
	return recs
}

// T1（新增用例）：折叠视图下「尾部 HistoryRegionWindow 个区域 + RegionsBefore」正确，
// 被裁区域的迭代**不在**响应里，窗口首迭代必是区域 head（区域原子）。
//
// mutation 判别力：
//   - View(true) 漏挂 RegionsBefore ⇒ 本条必红；
//   - 窗口起点落到区域中间（如按迭代号硬减 100）⇒ 首迭代/连续性断言必红；
//   - 窗口内工具不做轻字段化（漏传 foldTools=true）⇒ tools_folded 断言必红。
func TestConvert_ViewTrue_WindowTailAndRegionsBefore(t *testing.T) {
	recs := foldTurnFixture()
	runs := RegionRuns(recs)
	if len(runs) != 120 {
		t.Fatalf("fixture 区域数 = %d, want 120（夹具自校验）", len(runs))
	}
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "do it", TurnID: 7},
		{ID: 99, Role: "assistant", Content: "final answer", TurnID: 7},
	}
	got := ConvertMessagesToHistoryWithIterationsView(msgs, map[uint64][]sqlite.IterationRecord{7: recs}, true)
	row := assistantRowOf(t, got, 7)

	if row.Content != "final answer" {
		t.Errorf("最终回复文本必须并入唯一 assistant 行：content=%q", row.Content)
	}
	if row.RegionsBefore != 20 {
		t.Fatalf("RegionsBefore = %d, want 20（120 区域 - 窗口 100；更早区域必须被显式声明）", row.RegionsBefore)
	}
	if n := len(row.Iterations); n != 120 {
		t.Fatalf("窗口内迭代数 = %d, want 120（迭代 21..140）", n)
	}
	if row.Iterations[0].Iteration != 21 || row.Iterations[0].Iteration != runs[20].FirstIteration {
		t.Fatalf("窗口首迭代 = %d（区域 21 的 head = %d），want 21 —— 窗口边界必须对齐区域（区域原子）",
			row.Iterations[0].Iteration, runs[20].FirstIteration)
	}
	if last := row.Iterations[len(row.Iterations)-1].Iteration; last != 140 {
		t.Fatalf("窗口末迭代 = %d, want 140（尾部窗口必须覆盖 turn 最后迭代）", last)
	}
	for i := 1; i < len(row.Iterations); i++ {
		if row.Iterations[i].Iteration != row.Iterations[i-1].Iteration+1 {
			t.Fatalf("窗口内出现 gap：%d 之后是 %d —— 窗口必须是连续迭代号区间",
				row.Iterations[i-1].Iteration, row.Iterations[i].Iteration)
		}
		if row.Iterations[i].Iteration <= 20 {
			t.Fatalf("被裁区域的迭代 %d 仍在响应里 —— 未下发必须由 RegionsBefore 声明，不得夹带",
				row.Iterations[i].Iteration)
		}
	}
	if row.IterationsTruncated != 0 {
		t.Errorf("IterationsTruncated = %d, want 0（该老钩子不由窗口视图驱动）", row.IterationsTruncated)
	}
}

// T1（新增用例）：窗口内工具 = pill 轻字段；GenUI 豁免；纯文本迭代无标记。
func TestConvert_ViewTrue_FoldsToolDetails(t *testing.T) {
	recs := foldTurnFixture()
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "do it", TurnID: 7},
		{ID: 99, Role: "assistant", Content: "", TurnID: 7},
	}
	got := ConvertMessagesToHistoryWithIterationsView(msgs, map[uint64][]sqlite.IterationRecord{7: recs}, true)
	row := assistantRowOf(t, got, 7)
	byIter := make(map[int]HistoryIteration, len(row.Iterations))
	for _, it := range row.Iterations {
		byIter[it.Iteration] = it
	}

	// 纯文本迭代：无工具、不打标。
	if it := byIter[21]; it.Content != "text-21" || len(it.Tools) != 0 || it.ToolsFolded {
		t.Fatalf("纯文本迭代 21 形态异常：%+v", it)
	}
	// 工具组 head（带文本）：内容保留、工具轻字段化、GenUI 工具豁免。
	head := byIter[101]
	if head.Content != "head-0" {
		t.Errorf("head 迭代文本必须完整下发：content=%q", head.Content)
	}
	if !head.ToolsFolded {
		t.Errorf("head 迭代 101 含普通工具 ⇒ ToolsFolded 必须为 true")
	}
	if len(head.Tools) != 2 {
		t.Fatalf("迭代 101 工具数 = %d, want 2（普通 + GenUI）", len(head.Tools))
	}
	plain, genui := head.Tools[0], head.Tools[1]
	if plain.Name != "H0" || plain.Label != "H0(arg)" || plain.Status != "done" || plain.Elapsed != 12 || plain.Iteration != 101 {
		t.Errorf("pill 轻字段必须完整保留：%+v", plain)
	}
	if plain.Summary != "" || plain.Args != "" || plain.Detail != "" {
		t.Errorf("普通工具详情必须被省略（summary/args/detail）：%+v", plain)
	}
	if genui.Name != "GH0" {
		t.Fatalf("工具顺序漂移：%+v", head.Tools)
	}
	if genui.Summary == "" || genui.Args == "" || genui.Detail == "" {
		t.Errorf("GenUI 工具必须豁免折叠（详情保留）：%+v", genui)
	}
	// 纯工具成员：同样轻字段化。
	member := byIter[102]
	if member.Content != "" || !member.ToolsFolded {
		t.Errorf("纯工具成员 102 必须被折叠标记：%+v", member)
	}
	if len(member.Tools) != 1 || member.Tools[0].Summary != "" || member.Tools[0].Detail != "" {
		t.Errorf("纯工具成员详情必须省略：%+v", member.Tools)
	}
	// GenUI-only 迭代：不瘦身、不打标（无任何字段被省略）。
	for _, n := range []int{139, 140} {
		it := byIter[n]
		if it.ToolsFolded {
			t.Errorf("GenUI-only 迭代 %d 不得打 ToolsFolded 标记：%+v", n, it)
		}
		if len(it.Tools) != 1 || it.Tools[0].Summary == "" || it.Tools[0].Args == "" || it.Tools[0].Detail == "" {
			t.Errorf("GenUI-only 迭代 %d 详情必须完整：%+v", n, it.Tools)
		}
	}
}

// View(false) 必须与「原函数」逐字节一致（R5：CLI/RPC 零影响）。
//
// 两条断言：
//
//	① JSON 字节对照 View(false) == ConvertMessagesToHistoryWithIterations（wrapper 契约）；
//	② 显式钉死历史行为 —— 全量迭代、无 RegionsBefore、无 ToolsFolded，且**保留两个装配
//	   点的历史差异**：主装配路径不填迭代级指标、flushPending（取消/续跑，无最终回复行）
//	   路径填指标。判据：若把 fullTurnIterations 的 keepMetrics 统一成 true（顺手「修正」
//	   差异），②必红 —— 那会让 CLI/RPC 载荷漂移。
func TestConvert_ViewFalse_ByteIdenticalToLegacy(t *testing.T) {
	recs := []sqlite.IterationRecord{
		{TurnID: 3, Iteration: 1, Content: "think", Reasoning: "r", Tools: `[{"name":"Shell","status":"done","summary":"s","args":"{}","detail":"d"}]`, Tokens: 11, TTFTMs: 22, TokensPerSec: 33, TotalMs: 44},
		{TurnID: 3, Iteration: 2, Content: "answer", Tools: "[]", Tokens: 55, TTFTMs: 66, TokensPerSec: 77, TotalMs: 88},
	}
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "go", TurnID: 3},
		{ID: 41, Role: "assistant", Content: "done", TurnID: 3},
	}
	turnIterMap := map[uint64][]sqlite.IterationRecord{3: recs}

	legacy := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)
	view := ConvertMessagesToHistoryWithIterationsView(msgs, turnIterMap, false)
	lb, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	vb, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(lb, vb) {
		t.Fatalf("View(false) 与原函数不逐字节一致：\n legacy=%s\n view  =%s", lb, vb)
	}

	row := assistantRowOf(t, view, 3)
	if len(row.Iterations) != 2 || row.RegionsBefore != 0 {
		t.Fatalf("View(false) 必须全量且无区域声明：iters=%d regionsBefore=%d", len(row.Iterations), row.RegionsBefore)
	}
	for _, it := range row.Iterations {
		if it.ToolsFolded {
			t.Errorf("View(false) 不得出现折叠标记：%+v", it)
		}
	}
	// 主装配路径（有最终回复行）历史行为：不填迭代级指标。
	if got := row.Iterations[0]; got.Tokens != 0 || got.TTFTMs != 0 || got.TokensPerSec != 0 || got.TotalMs != 0 {
		t.Errorf("主装配路径的迭代级指标必须保持历史行为（不填）：%+v", got)
	}
	if got := row.Iterations[0].Tools[0]; got.Summary == "" || got.Args == "" || got.Detail == "" {
		t.Errorf("View(false) 工具详情必须完整：%+v", got)
	}

	// flushPending 路径（turn 无最终回复行 —— 取消/续跑）：历史行为是**填**指标。
	cancelMsgs := []llm.ChatMessage{
		{Role: "user", Content: "go", TurnID: 4},
		{Role: "assistant", TurnID: 4, ToolCalls: []llm.ToolCall{{ID: "t1", Name: "Shell"}}},
	}
	cancelRecs := map[uint64][]sqlite.IterationRecord{
		4: {{TurnID: 4, Iteration: 1, Content: "x", Tools: "[]", Tokens: 7, TTFTMs: 8, TokensPerSec: 9, TotalMs: 10}},
	}
	cancelView := ConvertMessagesToHistoryWithIterationsView(cancelMsgs, cancelRecs, false)
	cancelRow := assistantRowOf(t, cancelView, 4)
	if got := cancelRow.Iterations[0]; got.Tokens != 7 || got.TTFTMs != 8 || got.TokensPerSec != 9 || got.TotalMs != 10 {
		t.Errorf("flushPending 路径的迭代级指标必须保持历史行为（填）：%+v", got)
	}
	cancelLegacy, _ := json.Marshal(ConvertMessagesToHistoryWithIterations(cancelMsgs, cancelRecs))
	cancelVB, _ := json.Marshal(cancelView)
	if !bytes.Equal(cancelLegacy, cancelVB) {
		t.Fatalf("flushPending 路径 View(false) 与原函数不逐字节一致：\n legacy=%s\n view  =%s", cancelLegacy, cancelVB)
	}
}

// 无结构化迭代数据的 turn（turnIterMap 查不到）走既有 fallback：**不窗口化、不折叠**
// —— 与现状一致（Detail 回落路径只在老数据上触发）。
func TestConvert_ViewTrue_NoStructuredDataNotWindowed(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "old", TurnID: 5},
		{ID: 51, Role: "assistant", Content: "old answer", TurnID: 5},
	}
	got := ConvertMessagesToHistoryWithIterationsView(msgs, map[uint64][]sqlite.IterationRecord{}, true)
	if len(got) != 2 {
		t.Fatalf("fallback 路径消息数 = %d, want 2：%+v", len(got), got)
	}
	for _, h := range got {
		if h.RegionsBefore != 0 || len(h.Iterations) != 0 {
			t.Fatalf("无结构化数据的 turn 不得窗口化/折叠：%+v", h)
		}
	}
}

// =============================================================================
// T12（方案 §6-T12）：折叠视图的**体积预算守护**。
//
// fixture 与生产取证同构（方案 §1.1：单 turn 1,661 迭代 ≈ 3.6MB——迭代详情是
// payload 绝对大头）：每迭代 1 工具（summary ~120B / args ~180B / detail ~900B），
// 每 7 条迭代带 ~300B 文本。
//
// 断言：View(true) 序列化体积 < View(false) 的 **10%**（2026-09-30 实测 6.2%，
// 阈值留余量防抖动）。体积回潮（轻字段化/窗口化被弄丢、HistoryRegionWindow 被
// 调大、工具详情字段又随历史载荷下发）⇒ 本条必红。
//
// mutation 判别力：把 View(true) 装配误用 MapIterationRecord(rec,false)（不折叠）⇒
// folded ≈ full ⇒ 体积断言必红。
func TestConvert_ViewTrue_PayloadBudget(t *testing.T) {
	const total = 1661
	summary := strings.Repeat("s", 120)
	args := strings.Repeat("a", 180)
	detail := strings.Repeat("d", 900)
	recs := make([]sqlite.IterationRecord, 0, total)
	for i := 1; i <= total; i++ {
		content := ""
		if i%7 == 0 {
			content = strings.Repeat("x", 300)
		}
		recs = append(recs, sqlite.IterationRecord{
			TurnID: 1, Iteration: i, Content: content,
			Tools: fmt.Sprintf(`[{"name":"Shell","label":"Shell: npm test","status":"done","summary":%q,"args":%q,"detail":%q}]`, summary, args, detail),
		})
	}
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "do it", TurnID: 1},
		{Role: "assistant", TurnID: 1},
	}
	turnIterMap := map[uint64][]sqlite.IterationRecord{1: recs}

	fullJSON, err := json.Marshal(ConvertMessagesToHistoryWithIterationsView(msgs, turnIterMap, false))
	if err != nil {
		t.Fatal(err)
	}
	foldedJSON, err := json.Marshal(ConvertMessagesToHistoryWithIterationsView(msgs, turnIterMap, true))
	if err != nil {
		t.Fatal(err)
	}
	if lim := len(fullJSON) / 10; len(foldedJSON) >= lim {
		t.Fatalf("折叠视图体积 = %d B（≥ 全量 %d B 的 10%% = %d B）—— 体积预算回潮（实测校准 6.2%%）",
			len(foldedJSON), len(fullJSON), lim)
	}
	t.Logf("体积实测：full=%d B folded=%d B（%.1f%%）", len(fullJSON), len(foldedJSON), float64(len(foldedJSON))*100/float64(len(fullJSON)))

	// 形状正确性（体积小不能以丢数据为代价）：
	foldedView := ConvertMessagesToHistoryWithIterationsView(msgs, turnIterMap, true)
	for i := range foldedView {
		m := foldedView[i]
		if m.TurnID != 1 || m.Role != "assistant" {
			continue
		}
		if m.RegionsBefore <= 0 {
			t.Fatalf("RegionsBefore = %d, want > 0（1,661 迭代的 turn 必有未下发区域）", m.RegionsBefore)
		}
		if n := len(m.Iterations); n == 0 {
			t.Fatal("窗口内迭代为空")
		}
		for k := 1; k < len(m.Iterations); k++ {
			if m.Iterations[k].Iteration != m.Iterations[k-1].Iteration+1 {
				t.Fatalf("窗口内迭代号 gap：%d 之后是 %d", m.Iterations[k-1].Iteration, m.Iterations[k].Iteration)
			}
		}
		sawPlain, sawFolded := false, false
		for _, it := range m.Iterations {
			if !it.ToolsFolded {
				continue
			}
			sawFolded = true
			for _, tool := range it.Tools {
				if tool.UIMode == "" {
					sawPlain = true
					if tool.Summary != "" || tool.Args != "" || tool.Detail != "" {
						t.Fatal("折叠视图内普通工具的详情必须省略（体积收益的来源）")
					}
				}
			}
		}
		if !sawFolded || !sawPlain {
			t.Fatalf("窗口内必须有被折叠标记的迭代与普通工具（got folded=%v plain=%v）", sawFolded, sawPlain)
		}
		return
	}
	t.Fatal("no assistant message")
}

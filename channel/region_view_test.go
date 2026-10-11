package channel

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"xbot/storage/sqlite"
)

// =============================================================================
// 展示区域算法契约测试（F1-F11）
//
// 唯一规范来源：docs/plan-history-fold-windowing.md §2.1
//               = web/src/components/agent/TurnBody.tsx:941-956（mergeToolRuns）
//
// fixture 编号 F1-F11 与前端侧单测**共用同一组**（方案 §6-T4）：同一输入、两侧
// 区域边界必须逐条一致。本文件额外内嵌一份 mergeToolRuns 的 **TS 逐行移植**
// （tsMergeToolRuns）——它与 RegionRuns 走完全独立的代码路径，对同一组 fixture
// 做对照，任何一侧漂移立即变红（R1「两端区域判定漂移」的守护）。
// =============================================================================

// ---------------------------------------------------------------------------
// fixture 构造
// ---------------------------------------------------------------------------

// regionTestTool 是落库 tools JSON 的元素形状（= IterationRecord.Tools 的内容）。
type regionTestTool struct {
	Name      string   `json:"name"`
	Label     string   `json:"label,omitempty"`
	Status    string   `json:"status"`
	ElapsedMS int64    `json:"elapsed_ms"`
	Summary   string   `json:"summary,omitempty"`
	Args      string   `json:"args,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	UIMode    string   `json:"ui_mode,omitempty"`
	UILibs    []string `json:"ui_libs,omitempty"`
}

func regionToolsJSON(tools ...regionTestTool) string {
	b, err := json.Marshal(tools)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// regionRec 造一条 DB 迭代记录：无工具时 Tools="[]"（与落库形态一致，
// 见 storage/sqlite/sessiondb.go 的 iteration_history.tools DEFAULT '[]'）。
func regionRec(iteration int, content string, tools ...regionTestTool) sqlite.IterationRecord {
	rec := sqlite.IterationRecord{Iteration: iteration, Content: content, Tools: "[]"}
	if len(tools) > 0 {
		rec.Tools = regionToolsJSON(tools...)
	}
	return rec
}

// regionRecR 带 reasoning 的迭代（reasoning 与 content 同为「非纯工具」依据）。
func regionRecR(iteration int, content, reasoning string, tools ...regionTestTool) sqlite.IterationRecord {
	rec := regionRec(iteration, content, tools...)
	rec.Reasoning = reasoning
	return rec
}

// plainTool 普通工具（四项详情字段齐全 —— 折叠时应被省略）。
func plainTool(name string) regionTestTool {
	return regionTestTool{
		Name: name, Label: name + "(arg)", Status: "done", ElapsedMS: 12,
		Summary: name + ":summary", Args: `{"x":1}`, Detail: "detail-" + name,
	}
}

// genuiTool GenUI 工具（ui_mode 非空 —— 折叠时必须豁免，四项字段保留）。
func genuiTool(name string) regionTestTool {
	return regionTestTool{
		Name: name, Status: "done", ElapsedMS: 34,
		Summary: name + ":summary", Args: `{"x":2}`, Detail: "detail-" + name,
		UIMode: "genui", UILibs: []string{"echarts"},
	}
}

// ---------------------------------------------------------------------------
// 共享 fixture F1-F11
// ---------------------------------------------------------------------------

type regionFixture struct {
	id   string
	desc string
	recs []sqlite.IterationRecord
	want []RegionRun
}

func regionFixtures() []regionFixture {
	// F9：12 个区域、k=6 的窗口用例（前 6 个区域是纯文本迭代，后 6 个是
	// 「带文本 head + 纯工具成员」的折叠工具组 —— 每个区域 2 个迭代）
	var f9Recs []sqlite.IterationRecord
	for i := 1; i <= 6; i++ {
		f9Recs = append(f9Recs, regionRec(i, fmt.Sprintf("text-%d", i)))
	}
	it := 7
	for r := 0; r < 6; r++ {
		f9Recs = append(f9Recs, regionRec(it, fmt.Sprintf("head-%d", r), plainTool(fmt.Sprintf("H%d", r))))
		it++
		f9Recs = append(f9Recs, regionRec(it, "", plainTool(fmt.Sprintf("F%d", r))))
		it++
	}
	// ⇒ 迭代 1..18，区域 12 个：R1..R6 = {1}..{6}；R7=(7..8) R8=(9..10) …
	//   R12=(17..18)。区域 7 的起点 = 迭代 7（尾部 6 区域 = 迭代 7..18）。
	f9Want := []RegionRun{
		{HeadIteration: 1, FirstIteration: 1, LastIteration: 1, IterationCount: 1, ToolCount: 0},
		{HeadIteration: 2, FirstIteration: 2, LastIteration: 2, IterationCount: 1, ToolCount: 0},
		{HeadIteration: 3, FirstIteration: 3, LastIteration: 3, IterationCount: 1, ToolCount: 0},
		{HeadIteration: 4, FirstIteration: 4, LastIteration: 4, IterationCount: 1, ToolCount: 0},
		{HeadIteration: 5, FirstIteration: 5, LastIteration: 5, IterationCount: 1, ToolCount: 0},
		{HeadIteration: 6, FirstIteration: 6, LastIteration: 6, IterationCount: 1, ToolCount: 0},
		{HeadIteration: 7, FirstIteration: 7, LastIteration: 8, IterationCount: 2, ToolCount: 2},
		{HeadIteration: 9, FirstIteration: 9, LastIteration: 10, IterationCount: 2, ToolCount: 2},
		{HeadIteration: 11, FirstIteration: 11, LastIteration: 12, IterationCount: 2, ToolCount: 2},
		{HeadIteration: 13, FirstIteration: 13, LastIteration: 14, IterationCount: 2, ToolCount: 2},
		{HeadIteration: 15, FirstIteration: 15, LastIteration: 16, IterationCount: 2, ToolCount: 2},
		{HeadIteration: 17, FirstIteration: 17, LastIteration: 18, IterationCount: 2, ToolCount: 2},
	}

	return []regionFixture{
		{
			// mutation: 若 RegionRuns 把无工具迭代误并入相邻块（或反之把文本块拆开），
			// 本条区域数/边界必红。
			id: "F1_text_only", desc: "[text]：纯文本迭代独立成块",
			recs: []sqlite.IterationRecord{regionRec(1, "hello")},
			want: []RegionRun{{HeadIteration: 1, FirstIteration: 1, LastIteration: 1, IterationCount: 1, ToolCount: 0}},
		},
		{
			// ★ 用户点名的坑（§2.1/§1.2-4）：head **允许带 content/reasoning**，它的文本照常
			// 独立渲染、它的工具作为折叠组头部，后续纯工具迭代被吸收。
			// mutation: 若 RegionRuns 不允许「带文本的 head」（把它的工具另起一块），
			// 本条区域数会从 1 变 3（[text] + [tool] + [tool]），必红。
			id: "F2_text_head_absorbs", desc: "[text+tools, tool-only, tool-only]：1 区域，head=iter1 带文本",
			recs: []sqlite.IterationRecord{
				regionRec(1, "assistant text", plainTool("A")),
				regionRec(2, "", plainTool("B")),
				regionRec(3, "", plainTool("C")),
			},
			want: []RegionRun{{HeadIteration: 1, FirstIteration: 1, LastIteration: 3, IterationCount: 3, ToolCount: 3}},
		},
		{
			// mutation: 若 RegionRuns 把「纯工具迭代」排除在 head 之外（只允许非纯工具
			// 迭代做 head），iter1 会失去 head 身份 ⇒ 区域数从 2 变 3，必红。
			id: "F3_tool_head", desc: "[tool-only, tool-only, text]：2 区域，head 是纯工具迭代",
			recs: []sqlite.IterationRecord{
				regionRec(1, "", plainTool("A")),
				regionRec(2, "", plainTool("B")),
				regionRec(3, "final answer"),
			},
			want: []RegionRun{
				{HeadIteration: 1, FirstIteration: 1, LastIteration: 2, IterationCount: 2, ToolCount: 2},
				{HeadIteration: 3, FirstIteration: 3, LastIteration: 3, IterationCount: 1, ToolCount: 0},
			},
		},
		{
			id: "F4_text_breaks_run", desc: "[text+tools, tool-only, text, tool-only]：3 区域（run 被文本断开）",
			recs: []sqlite.IterationRecord{
				regionRec(1, "t1", plainTool("A")),
				regionRec(2, "", plainTool("B")),
				regionRec(3, "t2"),
				regionRec(4, "", plainTool("C")),
			},
			want: []RegionRun{
				{HeadIteration: 1, FirstIteration: 1, LastIteration: 2, IterationCount: 2, ToolCount: 2},
				{HeadIteration: 3, FirstIteration: 3, LastIteration: 3, IterationCount: 1, ToolCount: 0},
				// 末尾的纯工具迭代也是 head（前面是文本块，无法被吸收）。
				{HeadIteration: 4, FirstIteration: 4, LastIteration: 4, IterationCount: 1, ToolCount: 1},
			},
		},
		{
			// GenUI 豁免：MapIterationRecord(foldTools=true) 对这一条**不打标、不瘦身**。
			// mutation: 若折叠时 GenUI 工具被一并瘦身，TestMapIterationRecord_F5F6 必红。
			id: "F5_genui_only", desc: "[genui-only]：1 区域 + GenUI 豁免",
			recs: []sqlite.IterationRecord{regionRec(1, "", genuiTool("display_html"))},
			want: []RegionRun{{HeadIteration: 1, FirstIteration: 1, LastIteration: 1, IterationCount: 1, ToolCount: 1}},
		},
		{
			id: "F6_mixed_genui_and_plain", desc: "[text+tools(1 普通+1 GenUI), tool-only(2 普通)]：1 区域",
			recs: []sqlite.IterationRecord{
				regionRec(1, "hi", plainTool("Read"), genuiTool("display_html")),
				regionRec(2, "", plainTool("Shell"), plainTool("Grep")),
			},
			want: []RegionRun{{HeadIteration: 1, FirstIteration: 1, LastIteration: 2, IterationCount: 2, ToolCount: 4}},
		},
		{
			// 全纯工具 turn：head = iter1（纯工具迭代本身即 head），后续全部吸收。
			// mutation: 同 F3（纯工具迭代失去 head 身份 ⇒ 本条区域数 1→3），必红。
			id: "F7_all_pure_tool_turn", desc: "[tool-only ×3]：1 区域，head=iter1",
			recs: []sqlite.IterationRecord{
				regionRec(1, "", plainTool("A")),
				regionRec(2, "", plainTool("B")),
				regionRec(3, "", plainTool("C")),
			},
			want: []RegionRun{{HeadIteration: 1, FirstIteration: 1, LastIteration: 3, IterationCount: 3, ToolCount: 3}},
		},
		{
			id: "F8_empty", desc: "空序列：0 区域",
			recs: nil,
			want: nil,
		},
		{
			// mutation: 若 RegionWindow 按「尾部 k 个迭代」而非「尾部 k 个区域」切分，
			// 本条窗口起点会变成迭代 13（而非区域 7 的起点 7）、长度 6（而非 12），必红。
			id: "F9_window_12regions_k6", desc: "12 区域的序列、k=6：窗口=尾部 6 区域",
			recs: f9Recs,
			want: f9Want,
		},
		{
			id: "F10_k_ge_regions", desc: "k ≥ 区域总数：全量 + regionsBefore=0",
			recs: []sqlite.IterationRecord{
				regionRec(1, "assistant text", plainTool("A")),
				regionRec(2, "", plainTool("B")),
				regionRec(3, "", plainTool("C")),
			},
			want: []RegionRun{{HeadIteration: 1, FirstIteration: 1, LastIteration: 3, IterationCount: 3, ToolCount: 3}},
		},
		{
			id: "F11_single_iteration_turn", desc: "单迭代 turn（text+tools）：1 区域，k=1 全量",
			recs: []sqlite.IterationRecord{regionRec(1, "only", plainTool("A"))},
			want: []RegionRun{{HeadIteration: 1, FirstIteration: 1, LastIteration: 1, IterationCount: 1, ToolCount: 1}},
		},
	}
}

func fixtureByID(t *testing.T, id string) regionFixture {
	t.Helper()
	for _, f := range regionFixtures() {
		if f.id == id {
			return f
		}
	}
	t.Fatalf("fixture %s 不存在（fixture 表被改坏了）", id)
	return regionFixture{}
}

// ---------------------------------------------------------------------------
// TS 逐行移植（TurnBody.tsx:941-956）—— 独立代码路径，用于同构对照
// ---------------------------------------------------------------------------

// tsIter = WebIteration 的判定等价物（只保留 mergeToolRuns 读取的字段）。
type tsIter struct {
	iteration int
	content   string
	reasoning string
	toolCount int
}

// tsMergeToolRuns 是 mergeToolRuns 的**逐行移植**（唯一规范 §2.1）：
//
//	hasTools(it) = it.tools.length > 0
//	absorbs(it)  = hasTools(it) && !it.content && !it.reasoning
//	head = 首个 hasTools 迭代（允许带 content/reasoning）
//	贪心吸收 head 之后连续 absorbs 成员；块保留 head 迭代号
//
// ⚠️ 与 RegionRuns 无任何共享代码路径（刻意为之）：两侧若漂移，fixture 对照必红。
func tsMergeToolRuns(iters []tsIter) []RegionRun {
	hasTools := func(it tsIter) bool { return it.toolCount > 0 }
	absorbs := func(it tsIter) bool { return hasTools(it) && it.content == "" && it.reasoning == "" }
	var out []RegionRun
	for i := 0; i < len(iters); i++ {
		head := iters[i]
		if !hasTools(head) {
			out = append(out, RegionRun{
				HeadIteration: head.iteration, FirstIteration: head.iteration,
				LastIteration: head.iteration, IterationCount: 1, ToolCount: 0,
			})
			continue
		}
		j := i
		tools := head.toolCount
		for j+1 < len(iters) && absorbs(iters[j+1]) {
			j++
			tools += iters[j].toolCount
		}
		out = append(out, RegionRun{
			HeadIteration: head.iteration, FirstIteration: head.iteration,
			LastIteration: iters[j].iteration, IterationCount: j - i + 1, ToolCount: tools,
		})
		i = j
	}
	return out
}

func toTSIters(recs []sqlite.IterationRecord) []tsIter {
	out := make([]tsIter, 0, len(recs))
	for _, rec := range recs {
		out = append(out, tsIter{
			iteration: rec.Iteration,
			content:   rec.Content,
			reasoning: rec.Reasoning,
			toolCount: len(parseRegionTools(rec)),
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// RegionRuns：F1-F11 + 同构对照
// ---------------------------------------------------------------------------

func TestRegionRuns_FixturesF1ToF11(t *testing.T) {
	for _, f := range regionFixtures() {
		f := f
		t.Run(f.id, func(t *testing.T) {
			got := RegionRuns(f.recs)
			if !reflect.DeepEqual(got, f.want) {
				t.Fatalf("%s\n got=%+v\nwant=%+v", f.desc, got, f.want)
			}
			// 区域内部不变量：块起点即 head；块覆盖的是连续迭代号区间。
			for i, r := range got {
				if r.HeadIteration != r.FirstIteration {
					t.Errorf("区域[%d]：HeadIteration(%d) != FirstIteration(%d) —— 块起点必须是 head（前端保留 head 迭代号）",
						i, r.HeadIteration, r.FirstIteration)
				}
				if r.FirstIteration > r.LastIteration {
					t.Errorf("区域[%d]：First(%d) > Last(%d)", i, r.FirstIteration, r.LastIteration)
				}
				if r.IterationCount != r.LastIteration-r.FirstIteration+1 {
					t.Errorf("区域[%d]：IterationCount(%d) 与区间 [%d..%d] 不符（区域必须覆盖连续迭代号）",
						i, r.IterationCount, r.FirstIteration, r.LastIteration)
				}
			}
			// 区域划分必须覆盖全部迭代且不重不漏（区域 = 迭代序列的一个划分）。
			total := 0
			for _, r := range got {
				total += r.IterationCount
			}
			if total != len(f.recs) {
				t.Errorf("区域覆盖迭代数 %d != 输入迭代数 %d（划分有漏/重）", total, len(f.recs))
			}
			// 同构对照：Go 实现 vs mergeToolRuns 逐行移植。
			tsRuns := tsMergeToolRuns(toTSIters(f.recs))
			if !reflect.DeepEqual(got, tsRuns) {
				t.Fatalf("与前端 mergeToolRuns 漂移（同构契约 T4 破裂）\n   go=%+v\n  tswant=%+v", got, tsRuns)
			}
		})
	}
}

// TestRegionRuns_ExtraReasoningCases 覆盖 fixture 之外的 reasoning 边界
// （非共享 fixture，仅钉 Go 侧判定；前端同判定：!it.reasoning）。
func TestRegionRuns_ExtraReasoningCases(t *testing.T) {
	// 带 reasoning + 工具：可作为 head，但不满足 absorbs ⇒ 只能做 head。
	recs := []sqlite.IterationRecord{
		regionRecR(1, "", "thinking...", plainTool("A")),
		regionRec(2, "", plainTool("B")),
	}
	got := RegionRuns(recs)
	want := []RegionRun{{HeadIteration: 1, FirstIteration: 1, LastIteration: 2, IterationCount: 2, ToolCount: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reasoning+tools 的 head：got=%+v want=%+v", got, want)
	}

	// 纯 reasoning（无工具）⇒ 独立成块，且**不吸收**后续纯工具迭代（它无工具）。
	recs2 := []sqlite.IterationRecord{
		regionRecR(1, "", "thinking..."),
		regionRec(2, "", plainTool("A")),
	}
	got2 := RegionRuns(recs2)
	want2 := []RegionRun{
		{HeadIteration: 1, FirstIteration: 1, LastIteration: 1, IterationCount: 1, ToolCount: 0},
		{HeadIteration: 2, FirstIteration: 2, LastIteration: 2, IterationCount: 1, ToolCount: 1},
	}
	if !reflect.DeepEqual(got2, want2) {
		t.Fatalf("纯 reasoning 块：got=%+v want=%+v", got2, want2)
	}

	// 带文本的迭代夹在工具组中间 ⇒ 断开 run（工具组永不跨文本合并）。
	recs3 := []sqlite.IterationRecord{
		regionRec(1, "", plainTool("A")),
		regionRec(2, "interrupting text"),
		regionRec(3, "", plainTool("B")),
	}
	got3 := RegionRuns(recs3)
	want3 := []RegionRun{
		{HeadIteration: 1, FirstIteration: 1, LastIteration: 1, IterationCount: 1, ToolCount: 1},
		{HeadIteration: 2, FirstIteration: 2, LastIteration: 2, IterationCount: 1, ToolCount: 0},
		{HeadIteration: 3, FirstIteration: 3, LastIteration: 3, IterationCount: 1, ToolCount: 1},
	}
	if !reflect.DeepEqual(got3, want3) {
		t.Fatalf("文本断开 run：got=%+v want=%+v", got3, want3)
	}
}

// TestIsPureToolIteration_NoTrimSpace 钉死「不做 TrimSpace」的判定基准
// （前端 `!it.content` 同样不 trim：空白文本迭代仍然是「带文本」迭代，不可被吸收）。
func TestIsPureToolIteration_NoTrimSpace(t *testing.T) {
	blank := regionRec(2, "   ", plainTool("B"))
	if IsPureToolIteration(blank) {
		t.Fatal("空白 content 迭代不得被判为纯工具迭代（判定不做 TrimSpace，与前端同构）")
	}
	// 关键判别：空白文本迭代**夹在工具组中间**时必须断开 run（它就是「带文本迭代」）。
	// mutation: 若判定加了 TrimSpace（空白视为空串），iter2 会被 iter1 吸收 ⇒ 区域数 2→1，本条必红。
	recs := []sqlite.IterationRecord{
		regionRec(1, "", plainTool("A")),
		blank,
		regionRec(3, "", plainTool("C")),
	}
	got := RegionRuns(recs)
	want := []RegionRun{
		{HeadIteration: 1, FirstIteration: 1, LastIteration: 1, IterationCount: 1, ToolCount: 1},
		{HeadIteration: 2, FirstIteration: 2, LastIteration: 3, IterationCount: 2, ToolCount: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("空白文本必须断开工具组（无 TrimSpace）：got=%+v want=%+v", got, want)
	}
	if !reflect.DeepEqual(got, tsMergeToolRuns(toTSIters(recs))) {
		t.Fatal("空白文本判定与前端移植漂移")
	}
	// 空串才算纯工具迭代。
	if !IsPureToolIteration(regionRec(1, "", plainTool("A"))) {
		t.Fatal("content/reasoning 均为空串且有工具的迭代必须是纯工具迭代")
	}
	// 无工具 ⇒ 不是纯工具迭代。
	if IsPureToolIteration(regionRec(1, "")) {
		t.Fatal("无工具迭代不得被判为纯工具迭代")
	}
}

// TestHasGenuiTool 钉死 GenUI 判定（ui_mode 非空）。
func TestHasGenuiTool(t *testing.T) {
	if !HasGenuiTool(regionRec(1, "", genuiTool("display_html"))) {
		t.Fatal("ui_mode 非空 ⇒ HasGenuiTool 必须为 true")
	}
	if HasGenuiTool(regionRec(1, "", plainTool("Read"))) {
		t.Fatal("普通工具 ⇒ HasGenuiTool 必须为 false")
	}
	if !HasGenuiTool(regionRec(1, "", plainTool("Read"), genuiTool("display_html"))) {
		t.Fatal("混合工具里有一个 GenUI ⇒ true")
	}
	if HasGenuiTool(regionRec(1, "")) {
		t.Fatal("无工具 ⇒ false")
	}
}

// TestRegionRuns_GenuiDoesNotBlockAbsorption 钉死「GenUI 不阻碍吸收」
// （前端 absorbs 不看 uiMode；两侧必须一致，否则窗口边界漂移）。
func TestRegionRuns_GenuiDoesNotBlockAbsorption(t *testing.T) {
	recs := []sqlite.IterationRecord{
		regionRec(1, "", plainTool("A")),
		regionRec(2, "", genuiTool("display_html")), // GenUI 纯工具迭代 —— 照常被吸收
		regionRec(3, "", plainTool("B")),
	}
	got := RegionRuns(recs)
	want := []RegionRun{{HeadIteration: 1, FirstIteration: 1, LastIteration: 3, IterationCount: 3, ToolCount: 3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GenUI 迭代必须照常被吸收（与前端同构）：got=%+v want=%+v", got, want)
	}
	if !reflect.DeepEqual(got, tsMergeToolRuns(toTSIters(recs))) {
		t.Fatal("GenUI 吸收语义与前端移植漂移")
	}
}

// ---------------------------------------------------------------------------
// RegionWindow
// ---------------------------------------------------------------------------

type regionWindowCase struct {
	id         string
	recs       []sqlite.IterationRecord
	k          int
	wantLen    int
	wantFirst  int // 空窗口为 0
	wantBefore int
	wantFull   bool // 窗口 == 全量（含 0 区域时的空全量）
}

func TestRegionWindow_Cases(t *testing.T) {
	f2 := fixtureByID(t, "F2_text_head_absorbs")
	f4 := fixtureByID(t, "F4_text_breaks_run")
	f9 := fixtureByID(t, "F9_window_12regions_k6")

	cases := []regionWindowCase{
		{id: "F8_empty_k6", recs: nil, k: 6, wantLen: 0, wantFirst: 0, wantBefore: 0, wantFull: true},
		{id: "F1_text_k1", recs: fixtureByID(t, "F1_text_only").recs, k: 1, wantLen: 1, wantFirst: 1, wantBefore: 0, wantFull: true},
		{id: "F2_k1_regions1_full", recs: f2.recs, k: 1, wantLen: 3, wantFirst: 1, wantBefore: 0, wantFull: true},
		{id: "F2_k100_full", recs: f2.recs, k: 100, wantLen: 3, wantFirst: 1, wantBefore: 0, wantFull: true},
		{id: "F4_k2_tail2regions", recs: f4.recs, k: 2, wantLen: 2, wantFirst: 3, wantBefore: 1},
		{id: "F4_k3_full", recs: f4.recs, k: 3, wantLen: 4, wantFirst: 1, wantBefore: 0, wantFull: true},
		{id: "F7_k3_full", recs: fixtureByID(t, "F7_all_pure_tool_turn").recs, k: 3, wantLen: 3, wantFirst: 1, wantBefore: 0, wantFull: true},
		// ★ 区域原子：尾部 6 区域 = 迭代 7..18（12 个迭代），绝不是「尾部 6 个迭代」。
		{id: "F9_k6_tail6regions", recs: f9.recs, k: 6, wantLen: 12, wantFirst: 7, wantBefore: 6},
		// 退化：k <= 0 ⇒ 空窗口 + 全部区域计入 regionsBefore（不 panic）。
		{id: "F4_k0_degenerate", recs: f4.recs, k: 0, wantLen: 0, wantFirst: 0, wantBefore: 3},
	}

	for _, c := range cases {
		c := c
		t.Run(c.id, func(t *testing.T) {
			runs := RegionRuns(c.recs)
			window, before := RegionWindow(c.recs, c.k)

			if before != c.wantBefore {
				t.Errorf("regionsBefore = %d, want %d", before, c.wantBefore)
			}
			if len(window) != c.wantLen {
				t.Fatalf("窗口长度 = %d, want %d（window=%v）", len(window), c.wantLen, iterationsOf(window))
			}
			if len(runs) > 0 {
				// regionsBefore 必须与实际未下发区域数一致（§4-4 不变量）。
				wantBefore := len(runs) - c.k
				if wantBefore < 0 {
					wantBefore = 0
				}
				if before != wantBefore {
					t.Errorf("regionsBefore=%d 与「区域总数(%d) - k(%d)」不符", before, len(runs), c.k)
				}
			}
			if c.wantLen > 0 {
				if window[0].Iteration != c.wantFirst {
					t.Errorf("窗口起点迭代 = %d, want %d", window[0].Iteration, c.wantFirst)
				}
				// 窗口是尾部：必须包含输入的最后一个迭代。
				if got := window[len(window)-1].Iteration; got != c.recs[len(c.recs)-1].Iteration {
					t.Errorf("窗口末尾迭代 = %d, want %d（窗口必须贴住尾部）", got, c.recs[len(c.recs)-1].Iteration)
				}
				// 窗口内迭代号连续（无内部洞）。
				for i := 1; i < len(window); i++ {
					if window[i].Iteration != window[i-1].Iteration+1 {
						t.Fatalf("窗口内有洞：iter %d → %d", window[i-1].Iteration, window[i].Iteration)
					}
				}
				// 区域原子：窗口起点必是某个区域的起点（绝不劈开工具组）。
				headOK := false
				for _, r := range runs {
					if r.FirstIteration == window[0].Iteration {
						headOK = true
						break
					}
				}
				if !headOK {
					t.Errorf("窗口起点迭代 %d 不是任何区域的起点 —— 劈开了区域（用户「工具组整体处理」被违反）", window[0].Iteration)
				}
				// 未下发的区域必须正好是 runs[:len(runs)-k]。
				if !c.wantFull && len(runs) > c.k {
					if runs[len(runs)-c.k].FirstIteration != window[0].Iteration {
						t.Errorf("窗口起点 %d != 尾部第 %d 个区域的起点 %d",
							window[0].Iteration, c.k, runs[len(runs)-c.k].FirstIteration)
					}
				}
			}
			if c.wantFull {
				if len(window) != len(c.recs) {
					t.Errorf("全量窗口长度 = %d, want %d", len(window), len(c.recs))
				}
				for i := range window {
					if window[i].Iteration != c.recs[i].Iteration {
						t.Fatalf("全量窗口第 %d 项 = %d, want %d", i, window[i].Iteration, c.recs[i].Iteration)
					}
				}
			}
			// 未下发区域 + 窗口 必须拼回全量（完整性：不丢任何迭代，区域划分无漏无重）。
			undelivered := 0
			if c.k <= 0 {
				for _, r := range runs {
					undelivered += r.IterationCount
				}
			} else if len(runs) > c.k {
				for _, r := range runs[:len(runs)-c.k] {
					undelivered += r.IterationCount
				}
			}
			if undelivered+len(window) != len(c.recs) {
				t.Errorf("未下发区域覆盖 %d 个迭代 + 窗口 %d 个 = %d, want 总迭代数 %d（有迭代丢失）",
					undelivered, len(window), undelivered+len(window), len(c.recs))
			}
		})
	}
}

func iterationsOf(recs []sqlite.IterationRecord) []int {
	out := make([]int, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Iteration)
	}
	return out
}

// ---------------------------------------------------------------------------
// MapIterationRecord：现状行为保真 + D3 折叠
// ---------------------------------------------------------------------------

// TestMapIterationRecord_PreservesCurrentMapping 钉死「从 subscription.go:345-378
// 抽取」后的字段逐一保真（foldTools=false 时必须与现状装配完全一致）。
func TestMapIterationRecord_PreservesCurrentMapping(t *testing.T) {
	rec := sqlite.IterationRecord{
		Iteration:    7,
		Content:      "answer",
		Reasoning:    "think",
		Tools:        regionToolsJSON(regionTestTool{Name: "Read", Status: "done", ElapsedMS: 42, Summary: "S", Args: "A", Detail: "D"}),
		Tokens:       99,
		TTFTMs:       123,
		TokensPerSec: 45,
		TotalMs:      678,
	}
	got := MapIterationRecord(rec, false)

	if got.Iteration != 7 || got.Content != "answer" || got.Reasoning != "think" {
		t.Fatalf("迭代级字段丢失：%+v", got)
	}
	if got.Tokens != 99 || got.TTFTMs != 123 || got.TokensPerSec != 45 || got.TotalMs != 678 {
		t.Fatalf("迭代指标未透传：%+v", got)
	}
	if got.ToolsFolded {
		t.Fatal("foldTools=false 时 ToolsFolded 必须为 false")
	}
	if len(got.Tools) != 1 {
		t.Fatalf("工具数 = %d, want 1", len(got.Tools))
	}
	tp := got.Tools[0]
	if tp.Name != "Read" || tp.Label != "Read" || tp.Status != "done" || tp.Elapsed != 42 ||
		tp.Summary != "S" || tp.Args != "A" || tp.Detail != "D" {
		t.Fatalf("工具字段映射与现状不符：%+v", tp)
	}
	if tp.Iteration != 7 {
		t.Fatalf("每条工具必须盖所属迭代号：got %d", tp.Iteration)
	}

	// label 非空时保留原 label（不回落 name）。
	rec2 := sqlite.IterationRecord{Iteration: 1, Tools: regionToolsJSON(regionTestTool{Name: "Read", Label: "Read(/etc/hosts)", Status: "done"})}
	if lbl := MapIterationRecord(rec2, false).Tools[0].Label; lbl != "Read(/etc/hosts)" {
		t.Fatalf("label 非空必须原样保留：got %q", lbl)
	}

	// 无工具 / "[]" / 解析失败 ⇒ tools 为 nil（与现状一致：字段被 omitempty 省略）。
	for _, tools := range []string{"", "[]", "{not json", `{"not":"array"}`} {
		r := sqlite.IterationRecord{Iteration: 3, Tools: tools}
		m := MapIterationRecord(r, true)
		if m.Tools != nil {
			t.Errorf("Tools=%q ⇒ 应映射为 nil，实际 %+v", tools, m.Tools)
		}
		if m.ToolsFolded {
			t.Errorf("Tools=%q ⇒ 无工具可折叠，ToolsFolded 必须为 false", tools)
		}
	}
}

// TestMapIterationRecord_F5_GenuiExempt 钉死 F5：GenUI-only 迭代**不瘦身、不打标**。
// mutation: 若折叠时不做 ui_mode 豁免（或无差别地把所有工具都瘦身），本条必红。
func TestMapIterationRecord_F5_GenuiExempt(t *testing.T) {
	rec := fixtureByID(t, "F5_genui_only").recs[0]
	for _, fold := range []bool{true, false} {
		got := MapIterationRecord(rec, fold)
		if got.ToolsFolded {
			t.Fatalf("fold=%v：GenUI-only 迭代不得打 ToolsFolded（没有任何字段被省略）", fold)
		}
		tp := got.Tools[0]
		if tp.Summary == "" || tp.Args == "" || tp.Detail == "" {
			t.Fatalf("fold=%v：GenUI 工具字段被瘦身：%+v", fold, tp)
		}
		if tp.UIMode != "genui" || len(tp.UILibs) != 1 || tp.UILibs[0] != "echarts" {
			t.Fatalf("fold=%v：GenUI 声明字段丢失：%+v", fold, tp)
		}
		if tp.Label != "display_html" {
			t.Fatalf("fold=%v：label 空时应回落 name，got %q", fold, tp.Label)
		}
	}
}

// TestMapIterationRecord_F6_MixedFold 钉死 F6：普通工具瘦身、GenUI 完整、ToolsFolded=true
// mutation: 若折叠把 GenUI 工具一起瘦身（或整个迭代因含 GenUI 而豁免打标），本条必红。
func TestMapIterationRecord_F6_MixedFold(t *testing.T) {
	recs := fixtureByID(t, "F6_mixed_genui_and_plain").recs

	// head 迭代：1 普通 + 1 GenUI。
	got := MapIterationRecord(recs[0], true)
	if !got.ToolsFolded {
		t.Fatal("含非 GenUI 工具 ⇒ ToolsFolded 必须为 true（GenUI 豁免不是整迭代豁免）")
	}
	if got.Content != "hi" {
		t.Fatalf("head 的文本必须完整保留（§1.2-4）：got %q", got.Content)
	}
	plain, genui := got.Tools[0], got.Tools[1]
	// 轻字段全保留 —— pill 渲染与现状像素级一致。
	if plain.Name != "Read" || plain.Label != "Read(arg)" || plain.Status != "done" || plain.Elapsed != 12 || plain.Iteration != 1 {
		t.Fatalf("普通工具轻字段被破坏：%+v", plain)
	}
	if plain.Summary != "" || plain.Args != "" || plain.Detail != "" {
		t.Fatalf("普通工具详情大字段必须被省略：%+v", plain)
	}
	if genui.UIMode != "genui" || genui.Summary == "" || genui.Args == "" || genui.Detail == "" {
		t.Fatalf("GenUI 工具必须豁免瘦身：%+v", genui)
	}

	// 被吸收的纯工具迭代：全普通工具 ⇒ 全部瘦身 + 打标。
	absorbed := MapIterationRecord(recs[1], true)
	if !absorbed.ToolsFolded {
		t.Fatal("纯普通工具迭代必须打标")
	}
	for _, tp := range absorbed.Tools {
		if tp.Summary != "" || tp.Args != "" || tp.Detail != "" {
			t.Fatalf("普通工具未被瘦身：%+v", tp)
		}
		if tp.Name == "" || tp.Status == "" || tp.Iteration != 2 {
			t.Fatalf("轻字段丢失：%+v", tp)
		}
	}

	// 同一输入 foldTools=false ⇒ 全量（CLI/RPC 路径零影响）。
	full := MapIterationRecord(recs[0], false)
	if full.ToolsFolded {
		t.Fatal("foldTools=false 必须不打标")
	}
	for i, tp := range full.Tools {
		if tp.Summary == "" || tp.Args == "" || tp.Detail == "" {
			t.Fatalf("foldTools=false 必须全量下发：tool[%d]=%+v", i, tp)
		}
	}
	// 折叠只动详情字段：轻字段两态必须逐字段一致（无感验收的前提）。
	for i := range got.Tools {
		g, f := got.Tools[i], full.Tools[i]
		if g.Name != f.Name || g.Label != f.Label || g.Status != f.Status || g.Elapsed != f.Elapsed ||
			g.Iteration != f.Iteration || g.UIMode != f.UIMode || !reflect.DeepEqual(g.UILibs, f.UILibs) ||
			!reflect.DeepEqual(g.UISurface, f.UISurface) {
			t.Fatalf("折叠改变了 pill 轻字段：tool[%d]\n folded=%+v\n   full=%+v", i, g, f)
		}
	}
}

// TestMapIterationRecord_UISurfacePassthrough 钉死 ui_surface 透传
// （刷新后 panel 消失的既有回归点：UIMode/UILibs/UISurface 必须原样带到历史）。
func TestMapIterationRecord_UISurfacePassthrough(t *testing.T) {
	toolsJSON := `[{"name":"display_html","status":"done","elapsed_ms":5,"summary":"s","args":"{}","detail":"<div/>","ui_mode":"genui","ui_libs":["echarts"],"ui_surface":{"kind":"panel","title":"T","collapsible":true,"fullscreen":false,"default_open":true}}]`
	rec := sqlite.IterationRecord{Iteration: 2, Tools: toolsJSON}
	tp := MapIterationRecord(rec, true).Tools[0]
	if tp.UISurface == nil || tp.UISurface.Kind != "panel" || tp.UISurface.Title != "T" ||
		!tp.UISurface.Collapsible || tp.UISurface.Fullscreen || !tp.UISurface.DefaultOpen {
		t.Fatalf("ui_surface 未透传：%+v", tp.UISurface)
	}
	if tp.UIMode != "genui" {
		t.Fatalf("ui_mode 未透传：%q", tp.UIMode)
	}
}

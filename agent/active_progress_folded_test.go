package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"xbot/channel"
	"xbot/protocol"
)

// ⛔ P1（docs/plan-history-fold-windowing.md §3.5 D5）守护测试：active_progress 的
// **折叠视图变体**（Agent.GetActiveProgressFolded，REST 历史路径专用）。
//
// 不变量（与 §4 实施检查表一致）：
//  1. 原方法 GetActiveProgress 的**输出与行为零变化**——变体只在投影层工作，
//     且绝不原地改共享的迭代历史（否则原方法随后返回的快照会缺详情）；
//  2. FetchAll：尾部 channel.HistoryRegionWindow 个**区域**（区域原子 ⇒ 永不劈开
//     工具组）+ 窗口内轻字段化（GenUI 豁免）+ IterationRegionsBefore 显式声明
//     更早未下发区域数（**不是 gap**：窗口内迭代号连续）；
//  3. 增量路径（FetchSinceWatermark）**不折叠**（窗口延伸语义逐字节保持）；
//  4. live 进行中状态（activeTools/content/reasoning/phase/seq/iteration/turnID…）
//     一个字节不动。

// ── fixtures ────────────────────────────────────────────────────────────────

// foldTool 造一个「普通工具」（UIMode 空 ⇒ 参与轻字段化），详情大字段齐全。
func foldTool(name string) protocol.ToolProgress {
	return protocol.ToolProgress{
		Name:      name,
		Label:     name + " label",
		Status:    "done",
		Elapsed:   12,
		Summary:   name + " summary",
		Args:      `{"path":"` + name + `"}`,
		Detail:    name + " detail",
		ToolHints: "hint-" + name,
		CallID:    "call-" + name,
	}
}

// foldGenuiTool 造一个 GenUI 工具（UIMode 非空 ⇒ 豁免，详情必须全量保留）。
func foldGenuiTool(name string) protocol.ToolProgress {
	t := foldTool(name)
	t.UIMode = "genui"
	t.UILibs = []string{"echarts"}
	t.UISurface = &protocol.UISurface{Kind: "panel", Title: name, DefaultOpen: true}
	return t
}

// foldIter 造一条快照迭代元素（progressSnapshotWithoutHistory 落进
// iterationHistories 的形态）：文本字段决定区域划分是否吸收它。
func foldIter(n int, content, reasoning string, tools ...protocol.ToolProgress) protocol.ProgressEvent {
	tt := append([]protocol.ToolProgress(nil), tools...)
	for i := range tt {
		tt[i].Iteration = n
	}
	return protocol.ProgressEvent{
		Iteration:      n,
		Phase:          "tool_exec",
		Content:        content,
		Reasoning:      reasoning,
		CompletedTools: tt,
	}
}

func foldSeed(a *Agent, key string, snap *protocol.ProgressEvent, iters []protocol.ProgressEvent) {
	a.lastProgressSnapshot.Store(key, snap)
	a.iterationHistories.Store(key, &iters)
}

func foldMustJSON(t *testing.T, ev *protocol.ProgressEvent) string {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal progress event: %v", err)
	}
	return string(b)
}

// foldAssertLight 断言窗口元素的工具已轻字段化（详情为空、pill 轻字段与 CallID 仍在）。
func foldAssertLight(t *testing.T, ev protocol.ProgressEvent, wantName string) {
	t.Helper()
	if !ev.ToolsFolded {
		t.Fatalf("iter %d: 折叠视图必须打 tools_folded 标记（前端才知道要按 (turn_id, iteration) 拉详情）", ev.Iteration)
	}
	if len(ev.CompletedTools) != 1 {
		t.Fatalf("iter %d: tools len = %d, want 1", ev.Iteration, len(ev.CompletedTools))
	}
	tp := ev.CompletedTools[0]
	if tp.Summary != "" || tp.Args != "" || tp.Detail != "" || tp.ToolHints != "" {
		t.Errorf("iter %d: 非 GenUI 工具的详情大字段必须省略，got summary=%q args=%q detail=%q hints=%q",
			ev.Iteration, tp.Summary, tp.Args, tp.Detail, tp.ToolHints)
	}
	if tp.Name != wantName || tp.Status != "done" || tp.Elapsed != 12 {
		t.Errorf("iter %d: pill 轻字段必须完整保留，got %+v", ev.Iteration, tp)
	}
	if tp.CallID != "call-"+wantName {
		t.Errorf("iter %d: CallID 必须保留（CoT 配对 / promote 目标靠它），got %q", ev.Iteration, tp.CallID)
	}
}

// ── 1. ≤ K 区域：全量下发 + regions_before=0（普通 turn 完全不受影响）──────────

func TestGetActiveProgressFolded_FetchAllWithinWindowKeepsEverything(t *testing.T) {
	a := NewTestAgent()
	key := "web:fold-small"
	iters := []protocol.ProgressEvent{
		foldIter(1, "hello", "", foldTool("Shell")), // 带文本 + 工具 ⇒ head
		foldIter(2, "", "", foldTool("Grep")),       // 纯工具 ⇒ 被 iter1 吸收（1 个区域）
		foldIter(3, "final answer", ""),             // 无工具 ⇒ 独立区域
	}
	foldSeed(a, key, &protocol.ProgressEvent{ChatID: key, Phase: "tool_exec", TurnID: 9}, iters)

	res := a.GetActiveProgressFolded("web", "fold-small", protocol.FetchAll())
	if res == nil {
		t.Fatal("GetActiveProgressFolded returned nil")
	}
	if len(res.IterationHistory) != 3 {
		t.Fatalf("IterationHistory len = %d, want 3（区域数 ≤ K ⇒ 全量下发）", len(res.IterationHistory))
	}
	if res.IterationRegionsBefore != 0 {
		t.Errorf("IterationRegionsBefore = %d, want 0（无更早未下发区域）", res.IterationRegionsBefore)
	}
	for i, want := range []int{1, 2, 3} {
		if got := res.IterationHistory[i].Iteration; got != want {
			t.Errorf("iteration[%d] = %d, want %d（迭代号必须连续无洞）", i, got, want)
		}
	}
	foldAssertLight(t, res.IterationHistory[0], "Shell")
	foldAssertLight(t, res.IterationHistory[1], "Grep")
	if res.IterationHistory[2].ToolsFolded {
		t.Error("无工具的迭代不该被打标")
	}
	if res.IterationHistory[2].Content != "final answer" {
		t.Errorf("文本迭代的 content 必须原样保留，got %q", res.IterationHistory[2].Content)
	}
}

// ── 2. > K 区域：尾部区域窗口 + 区域原子（永不劈开工具组）──────────────────────

func TestGetActiveProgressFolded_WindowsLargeTurnOnRegionBoundary(t *testing.T) {
	a := NewTestAgent()
	key := "web:fold-large"

	// 区域划分：iter 1..100 各是无工具文本 ⇒ 100 个独立区域；
	//           iter 101 带文本 + 工具 = head，吸收 102..110 的纯工具迭代 ⇒ 第 101 个区域。
	// 合计 101 个区域 > K=100 ⇒ 只下发最后 100 个区域 = iter 2..110。
	iters := make([]protocol.ProgressEvent, 0, 110)
	for i := 1; i <= 100; i++ {
		iters = append(iters, foldIter(i, fmt.Sprintf("text-%d", i), ""))
	}
	iters = append(iters, foldIter(101, "head text", "", foldTool("Shell"), foldTool("Read")))
	for i := 102; i <= 110; i++ {
		iters = append(iters, foldIter(i, "", "", foldTool("Grep")))
	}
	foldSeed(a, key, &protocol.ProgressEvent{ChatID: key, Phase: "tool_exec", TurnID: 11}, iters)

	res := a.GetActiveProgressFolded("web", "fold-large", protocol.FetchAll())
	if res == nil {
		t.Fatal("GetActiveProgressFolded returned nil")
	}
	if res.IterationRegionsBefore != 1 {
		t.Errorf("IterationRegionsBefore = %d, want 1（101 个区域只下发尾部 100 个）", res.IterationRegionsBefore)
	}
	if len(res.IterationHistory) != 109 {
		t.Fatalf("window len = %d, want 109（iter 2..110）", len(res.IterationHistory))
	}
	if first := res.IterationHistory[0].Iteration; first != 2 {
		t.Errorf("窗口首个迭代 = %d, want 2（区域原子：窗口起点必须是某个区域的头部，绝不从工具组中间切开）", first)
	}
	for i := 1; i < len(res.IterationHistory); i++ {
		if res.IterationHistory[i].Iteration != res.IterationHistory[i-1].Iteration+1 {
			t.Fatalf("窗口出现 gap：%d 之后是 %d", res.IterationHistory[i-1].Iteration, res.IterationHistory[i].Iteration)
		}
	}
	last := res.IterationHistory[len(res.IterationHistory)-1].Iteration
	if last != 110 {
		t.Errorf("窗口末迭代 = %d, want 110", last)
	}
	// 工具组 101..110 必须整段在窗口内（被吸收的成员一个不少）。
	seen := map[int]bool{}
	for _, it := range res.IterationHistory {
		seen[it.Iteration] = true
	}
	for i := 101; i <= 110; i++ {
		if !seen[i] {
			t.Errorf("工具组成员 iter %d 被窗口切掉了（区域原子被破坏）", i)
		}
	}
	// 窗口自身恰为 K=100 个区域（窗口边界 ≡ 区域边界）。
	if got := len(channel.RegionRuns(activeProgressRecords(res.IterationHistory))); got != channel.HistoryRegionWindow {
		t.Errorf("窗口内区域数 = %d, want %d", got, channel.HistoryRegionWindow)
	}
	// 窗口内全部轻字段化（含被吸收的纯工具迭代）。
	for _, it := range res.IterationHistory {
		for _, tp := range it.CompletedTools {
			if tp.Summary != "" || tp.Args != "" || tp.Detail != "" {
				t.Fatalf("iter %d 的工具未轻字段化：%+v", it.Iteration, tp)
			}
		}
	}
}

// ── 3. live 进行中状态完整不动 ─────────────────────────────────────────────

func TestGetActiveProgressFolded_KeepsLiveStateIntact(t *testing.T) {
	a := NewTestAgent()
	key := "web:fold-live"
	iters := []protocol.ProgressEvent{foldIter(1, "done iter", "", foldTool("Shell"))}
	snap := &protocol.ProgressEvent{
		ChatID:         key,
		Phase:          "tool_exec",
		Seq:            7,
		Iteration:      2,
		TurnID:         42,
		Content:        "live content",
		Reasoning:      "live reasoning",
		StreamContent:  "live stream",
		ActiveTools:    []protocol.ToolProgress{{Name: "Bash", Status: "running", CallID: "live-call", Iteration: 2}},
		StreamingTools: []protocol.ToolProgress{{Name: "Write", Status: "generating", GenChars: 33}},
		Todos:          []protocol.TodoItem{{Text: "todo-1", Status: "pending"}},
		TokenUsage:     &protocol.TokenUsage{PromptTokens: 11, CompletionTokens: 22, TotalTokens: 33},
		StreamStats:    &protocol.StreamStats{TTFTMs: 100, TokensPerSec: 50, TotalMs: 900},
		CWD:            "/tmp/live",
	}
	foldSeed(a, key, snap, iters)

	orig := a.GetActiveProgress("web", "fold-live", protocol.FetchAll())
	folded := a.GetActiveProgressFolded("web", "fold-live", protocol.FetchAll())
	if orig == nil || folded == nil {
		t.Fatal("nil progress event")
	}

	// 逐字段对照：除 IterationHistory / IterationRegionsBefore 外必须完全一致。
	o, f := *orig, *folded
	o.IterationHistory, f.IterationHistory = nil, nil
	o.IterationRegionsBefore, f.IterationRegionsBefore = 0, 0
	if !reflect.DeepEqual(o, f) {
		t.Fatalf("折叠视图改动了非投影字段（live 状态必须一个字节不动）\norig:   %+v\nfolded: %+v", o, f)
	}
	// 显式点名（防 DeepEqual 被未来字段悄悄放宽）：live 权威字段。
	if folded.Phase != "tool_exec" || folded.Seq != 7 || folded.Iteration != 2 || folded.TurnID != 42 {
		t.Errorf("live 元信息被改动：phase=%q seq=%d iter=%d turn=%d", folded.Phase, folded.Seq, folded.Iteration, folded.TurnID)
	}
	if folded.Content != "live content" || folded.Reasoning != "live reasoning" || folded.StreamContent != "live stream" {
		t.Errorf("live 文本被改动：content=%q reasoning=%q stream=%q", folded.Content, folded.Reasoning, folded.StreamContent)
	}
	if len(folded.ActiveTools) != 1 || folded.ActiveTools[0].CallID != "live-call" {
		t.Errorf("activeTools 被改动：%+v", folded.ActiveTools)
	}
	if len(folded.StreamingTools) != 1 || folded.StreamingTools[0].GenChars != 33 {
		t.Errorf("streamingTools 被改动：%+v", folded.StreamingTools)
	}
	if folded.TokenUsage == nil || folded.TokenUsage.TotalTokens != 33 || folded.StreamStats == nil || folded.StreamStats.TTFTMs != 100 {
		t.Errorf("用量/流统计被改动：%+v / %+v", folded.TokenUsage, folded.StreamStats)
	}
	if len(folded.Todos) != 1 || folded.Todos[0].Text != "todo-1" {
		t.Errorf("todos 被改动：%+v", folded.Todos)
	}
	// 历史侧：原方法完整、变体轻字段（同输入两方法对比）。
	if orig.IterationHistory[0].CompletedTools[0].Summary == "" {
		t.Error("原方法的迭代历史应当保持完整（详情在）")
	}
	foldAssertLight(t, folded.IterationHistory[0], "Shell")
}

// ── 4. GenUI 豁免（快照内 GenUI 迭代详情保留 + GenUI-only 不打标）─────────────

func TestGetActiveProgressFolded_GenuiExempt(t *testing.T) {
	a := NewTestAgent()
	key := "web:fold-genui"
	iters := []protocol.ProgressEvent{
		foldIter(1, "mixed", "", foldTool("Shell"), foldGenuiTool("Chart")),
		foldIter(2, "genui only", "", foldGenuiTool("Graph")),
	}
	foldSeed(a, key, &protocol.ProgressEvent{ChatID: key, Phase: "tool_exec"}, iters)

	res := a.GetActiveProgressFolded("web", "fold-genui", protocol.FetchAll())
	if res == nil {
		t.Fatal("GetActiveProgressFolded returned nil")
	}
	mixed := res.IterationHistory[0]
	if !mixed.ToolsFolded {
		t.Error("含非 GenUI 工具的迭代必须打标")
	}
	if got := mixed.CompletedTools[0]; got.Summary != "" || got.Args != "" || got.Detail != "" {
		t.Errorf("非 GenUI 工具必须轻字段化，got %+v", got)
	}
	genui := mixed.CompletedTools[1]
	if genui.Summary == "" || genui.Args == "" || genui.Detail == "" {
		t.Errorf("GenUI 工具必须豁免（详情全量保留），got %+v", genui)
	}
	if genui.UIMode != "genui" || genui.UISurface == nil || genui.UISurface.Title != "Chart" || len(genui.UILibs) != 1 {
		t.Errorf("GenUI 元数据必须原样保留，got %+v", genui)
	}

	only := res.IterationHistory[1]
	if only.ToolsFolded {
		t.Error("GenUI-only 迭代没有任何字段被省略 ⇒ 不得打标（否则前端会去拉并不缺的详情）")
	}
	if only.CompletedTools[0].Summary == "" {
		t.Errorf("GenUI-only 迭代详情必须完整，got %+v", only.CompletedTools[0])
	}
}

// ── 5. 增量路径不折叠（含 resync_required 语义不变）─────────────────────────

func TestGetActiveProgressFolded_IncrementalNotFolded(t *testing.T) {
	a := NewTestAgent()
	key := "web:fold-inc"
	iters := make([]protocol.ProgressEvent, 0, 5)
	for i := 1; i <= 5; i++ {
		iters = append(iters, foldIter(i, "", "", foldTool("Shell")))
	}
	foldSeed(a, key, &protocol.ProgressEvent{ChatID: key, Phase: "tool_exec", Iteration: 5}, iters)

	res := a.GetActiveProgressFolded("web", "fold-inc", protocol.FetchSinceWatermark(2))
	if res == nil {
		t.Fatal("GetActiveProgressFolded returned nil")
	}
	if len(res.IterationHistory) != 3 {
		t.Fatalf("增量 len = %d, want 3（iter 3..5）", len(res.IterationHistory))
	}
	if res.IterationRegionsBefore != 0 {
		t.Errorf("增量路径不得填 IterationRegionsBefore，got %d", res.IterationRegionsBefore)
	}
	for _, it := range res.IterationHistory {
		if it.ToolsFolded {
			t.Errorf("增量路径元素不得打 tools_folded（客户端已建立窗口，增量是窗口延伸）: iter %d", it.Iteration)
		}
		if it.CompletedTools[0].Summary == "" {
			t.Errorf("增量路径必须保持完整详情: iter %d got %+v", it.Iteration, it.CompletedTools[0])
		}
	}
}

func TestGetActiveProgressFolded_ResyncSemanticsUnchanged(t *testing.T) {
	a := NewTestAgent()
	key := "web:fold-resync"
	iters := make([]protocol.ProgressEvent, 0, maxIncrementalIterations+10)
	for i := 1; i <= maxIncrementalIterations+10; i++ {
		iters = append(iters, foldIter(i, "", "", foldTool("Shell")))
	}
	foldSeed(a, key, &protocol.ProgressEvent{ChatID: key, Phase: "tool_exec"}, iters)

	orig := a.GetActiveProgress("web", "fold-resync", protocol.FetchSinceWatermark(0))
	folded := a.GetActiveProgressFolded("web", "fold-resync", protocol.FetchSinceWatermark(0))
	if !orig.ResyncRequired || !folded.ResyncRequired {
		t.Fatalf("resync_required 语义必须不变：orig=%v folded=%v", orig.ResyncRequired, folded.ResyncRequired)
	}
	if folded.IterationHistory != nil || folded.IterationRegionsBefore != 0 {
		t.Errorf("resync 分支不得折叠/窗口化：hist=%d regions_before=%d",
			len(folded.IterationHistory), folded.IterationRegionsBefore)
	}
	if foldMustJSON(t, orig) != foldMustJSON(t, folded) {
		t.Error("resync 分支的折叠视图必须与原方法逐字节一致")
	}
}

// ── 6. 原方法回归：输出零变化 + 共享迭代历史不被污染 ────────────────────────

// 判别力（mutation）：把 foldProgressIteration 的「新建切片」去掉（原地清字段）⇒
// 本测试的 after != before 必红；把变体的迭代历史折叠逻辑写回原方法 ⇒ 同样必红。
func TestGetActiveProgress_UnchangedByFoldedVariant(t *testing.T) {
	a := NewTestAgent()
	key := "web:fold-regress"
	iters := []protocol.ProgressEvent{
		foldIter(1, "text", "", foldTool("Shell"), foldTool("Read")),
		foldIter(2, "", "", foldTool("Grep")),
	}
	foldSeed(a, key, &protocol.ProgressEvent{ChatID: key, Phase: "tool_exec", TurnID: 3}, iters)

	before := foldMustJSON(t, a.GetActiveProgress("web", "fold-regress", protocol.FetchAll()))
	folded := a.GetActiveProgressFolded("web", "fold-regress", protocol.FetchAll())
	after := foldMustJSON(t, a.GetActiveProgress("web", "fold-regress", protocol.FetchAll()))

	if before != after {
		t.Fatalf("折叠变体污染了原方法的输出（共享迭代历史被原地修改）\nbefore: %s\nafter:  %s", before, after)
	}
	// 原方法视图：完整详情、无窗口声明、无折叠标记。
	if !strings.Contains(before, `"summary":"Shell summary"`) {
		t.Errorf("原方法必须原样保留工具详情，got %s", before)
	}
	if strings.Contains(before, "tools_folded") || strings.Contains(before, "iteration_regions_before") {
		t.Errorf("原方法不得出现折叠视图字段，got %s", before)
	}
	// 折叠视图：轻字段 + 标记；区域数 ≤ K ⇒ regions_before 因 omitempty 不出现（旧客户端零感知）。
	foldedJSON := foldMustJSON(t, folded)
	if !strings.Contains(foldedJSON, `"tools_folded":true`) {
		t.Errorf("折叠视图必须带 tools_folded 标记，got %s", foldedJSON)
	}
	if strings.Contains(foldedJSON, "Shell summary") || strings.Contains(foldedJSON, "Shell detail") {
		t.Errorf("折叠视图必须省略非 GenUI 工具详情，got %s", foldedJSON)
	}
	if strings.Contains(foldedJSON, "iteration_regions_before") {
		t.Errorf("regions_before=0 时必须 omitempty（普通 turn 载荷逐字节不变），got %s", foldedJSON)
	}
}

// ── 7. isomorphism 守护：内联轻字段规则 ≡ region_view 规范实现 ───────────────

// 判别力（mutation）：把 foldProgressIteration 的 UIMode 判定改掉（例如无条件省略、
// 或把 GenUI 也折叠）⇒ 本测试与 GenUI 测试同时必红。这条把「内联判定」与波 0 的
// 唯一规范 channel.MapIterationRecord(rec, true) 钉在一起（§7-R1 双实现漂移）。
func TestFoldProgressIteration_MatchesCanonicalMapping(t *testing.T) {
	cases := []struct {
		name string
		el   protocol.ProgressEvent
	}{
		{"plain tools", foldIter(1, "", "", foldTool("Shell"), foldTool("Read"))},
		{"genui only", foldIter(2, "text", "", foldGenuiTool("Chart"))},
		{"mixed", foldIter(3, "text", "", foldTool("Shell"), foldGenuiTool("Chart"))},
		{"no tools", foldIter(4, "text", "")},
		{"gnui-looking ui mode", foldIter(5, "", "", func() protocol.ToolProgress {
			tp := foldTool("Panel")
			tp.UIMode = "html" // 非 genui 但 ui_mode 非空 ⇒ 与 genui 同等地豁免（判定只看非空）
			return tp
		}())},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := activeProgressRecord(tc.el)
			light := foldProgressIteration(tc.el, rec)
			mapped := channel.MapIterationRecord(rec, true)

			if light.ToolsFolded != mapped.ToolsFolded {
				t.Fatalf("折叠判定漂移：inline=%v canonical=%v", light.ToolsFolded, mapped.ToolsFolded)
			}
			if len(light.CompletedTools) != len(mapped.Tools) {
				t.Fatalf("工具数漂移：inline=%d canonical=%d", len(light.CompletedTools), len(mapped.Tools))
			}
			for j := range mapped.Tools {
				m, got := mapped.Tools[j], light.CompletedTools[j]
				if m.Name != got.Name || m.UIMode != got.UIMode {
					t.Errorf("tool[%d] 身份漂移：inline=%+v canonical=%+v", j, got, m)
				}
				if m.Summary != got.Summary || m.Args != got.Args || m.Detail != got.Detail {
					t.Errorf("tool[%d] 轻字段漂移：inline={%q,%q,%q} canonical={%q,%q,%q}",
						j, got.Summary, got.Args, got.Detail, m.Summary, m.Args, m.Detail)
				}
				// ToolHints 只在**被折叠**的象限可比：规范形状 regionToolSnap 没有
				// tool_hints 字段（region_view.go:30-42，DB 路径同样从不填充它），
				// 所以豁免象限的 canonical 恒空是「映射缺失」而非「规则要求省略」；
				// 元素侧保留 tool_hints 更贴近 live 契约（protocol.ToolProgress 有该字段）。
				if got.UIMode == "" && got.ToolHints != "" {
					t.Errorf("tool[%d] 折叠象限必须省略 tool_hints，got %q", j, got.ToolHints)
				}
			}
		})
	}
}

// ── 8. 适配层：快照元素 → 区域判定输入（与 DB 形状同构）─────────────────────

func TestActiveProgressRecord_ShapesMatchDBClassification(t *testing.T) {
	// 纯工具迭代（无文本 + 有工具）必须被识别成「可被吸收」——与 DB 路径同一判定。
	pure := activeProgressRecord(foldIter(7, "", "", foldTool("Shell")))
	if !channel.IsPureToolIteration(pure) {
		t.Errorf("快照的纯工具迭代必须与 DB 路径同样可被吸收，rec=%+v", pure)
	}
	// 无工具 ⇒ Tools 空（与 DB 侧 ""/"[]" 同义）。
	empty := activeProgressRecord(foldIter(8, "text", ""))
	if empty.Tools != "" {
		t.Errorf("无工具迭代的 Tools 必须是空串（与 DB 形状同义），got %q", empty.Tools)
	}
	// GenUI 工具在区域判定里**不阻碍吸收**（与前端 absorbs 同构）。
	genui := activeProgressRecord(foldIter(9, "", "", foldGenuiTool("Chart")))
	if !channel.IsPureToolIteration(genui) {
		t.Errorf("GenUI 不影响区域划分（否则两端区域边界漂移），rec=%+v", genui)
	}
	if !channel.HasGenuiTool(genui) {
		t.Errorf("GenUI 判定必须与 region_view 同构，rec=%+v", genui)
	}
}

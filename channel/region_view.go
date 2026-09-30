package channel

import (
	"encoding/json"

	"xbot/protocol"
	"xbot/storage/sqlite"
)

// =============================================================================
// 展示区域（region）—— 历史分页的计量单位（方案 docs/plan-history-fold-windowing.md
// §2.1/§3.1/§3.2/§3.3）
//
// 本文件是**纯函数库**（无 I/O、无全局状态），导出给装配层（subscription.go）与
// 契约测试使用。核心不变量：
//
//	① 「区域」= 前端渲染块 = `mergeToolRuns`（web/src/components/agent/TurnBody.tsx:941）
//	   的输出块。**折叠的工具组算 1 个区域**。两侧必须逐条件同构（唯一规范 §2.1），
//	   由 channel/region_view_test.go 的共享 fixture（F1-F11）钉死。
//	② 区域是**原子**的：窗口/段的边界永远对齐区域边界，**永不劈开工具组**。
//	③ 判定基准：DB 空串 vs 前端空串同构 —— `Content == "" && Reasoning == ""`，
//	   **不做 TrimSpace**（前端 `!it.content` 同样不做）。
// =============================================================================

// regionToolSnap mirrors the persisted tool snapshot JSON entry (同一形状，
// 见 subscription.go 的 iterToolSnap 与 agent 侧 IterationSnapshot)。
//
// 这里**故意**不复用 subscription.go 的 iterToolSnap：本任务要求 region_view.go
// 自包含（不产生跨文件/跨改动面的依赖），字段与解析行为与 iterToolSnap 逐字段一致。
type regionToolSnap struct {
	Name      string `json:"name"`
	Label     string `json:"label,omitempty"`
	Status    string `json:"status"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Summary   string `json:"summary,omitempty"`
	Args      string `json:"args,omitempty"`
	Detail    string `json:"detail,omitempty"`
	// UIMode/UILibs/UISurface —— GenUI 能力声明（刷新后 panel 不丢的关键字段）。
	UIMode    string              `json:"ui_mode,omitempty"`
	UILibs    []string            `json:"ui_libs,omitempty"`
	UISurface *protocol.UISurface `json:"ui_surface,omitempty"`
}

// parseRegionTools 解析一条迭代记录的 tools JSON。
//
// 与 subscription.go:349 / :484 的取数条件逐字相同：空串与 "[]" 视为「无工具」，
// 解析失败同样回落为 nil（绝不 panic、绝不半成品）。返回 nil 表示「无工具」。
//
// ⚠️ 纯函数、无缓存：同一 rec 在 RegionRuns/HasGenuiTool/MapIterationRecord 里会被
// 重复解析。本层不是性能瓶颈（正确性与同构优先），调用方若在巨型 turn 上做热路径
// 优化，可在**调用方**缓存解析结果（不要在此处塞全局 map —— 会引入共享可变状态）。
func parseRegionTools(rec sqlite.IterationRecord) []regionToolSnap {
	if rec.Tools == "" || rec.Tools == "[]" {
		return nil
	}
	var snaps []regionToolSnap
	if err := json.Unmarshal([]byte(rec.Tools), &snaps); err != nil {
		return nil
	}
	return snaps
}

// hasRegionTools 与前端 `hasTools(it) = it.tools.length > 0` 同构。
func hasRegionTools(rec sqlite.IterationRecord) bool {
	return len(parseRegionTools(rec)) > 0
}

// IsPureToolIteration 报告该迭代是否是「纯工具迭代」——即前端
// `absorbs(it) = hasTools(it) && !it.content && !it.reasoning` 的 Go 同构。
//
// 只有纯工具迭代才能被**前置 head 吸收**（head 本身允许带 content/reasoning，
// 见 §2.1 用户点名的坑）。
func IsPureToolIteration(rec sqlite.IterationRecord) bool {
	return rec.Content == "" && rec.Reasoning == "" && hasRegionTools(rec)
}

// HasGenuiTool 报告该迭代的工具里是否存在 GenUI 工具（ui_mode 非空）。
// 用于 D3 的 GenUI 豁免：含 GenUI 工具的迭代不参与详情瘦身（顶层卡片默认渲染
// 可能消费详情，保守全量）。
func HasGenuiTool(rec sqlite.IterationRecord) bool {
	for _, t := range parseRegionTools(rec) {
		if t.UIMode != "" {
			return true
		}
	}
	return false
}

// RegionRun 描述一个展示区域（= 一个前端渲染块）。
//
// HeadIteration = 该块的**头部迭代号**（前端 mergeToolRuns 保留 head 迭代号：
// 高度缓存 / 窗口 key 稳定；文本与工具都取 head 那一份 + 后续成员的工具）。
// FirstIteration = 该块覆盖的**首个迭代号**。由于 head 本身就是块的第一个成员，
// 二者恒等（由测试钉死该不变量，供装配层/取回端点无歧义地取窗口起点）。
// LastIteration = 该块覆盖的最后一个迭代号（被吸收的尾部成员）。
// IterationCount = 块覆盖的迭代个数（被吸收的成员一并计入；独立迭代块恒为 1）。
// ToolCount = 块内工具总数（折叠工具组的「工具数」，非 GenUI 与 GenUI 都算）。
type RegionRun struct {
	HeadIteration  int
	FirstIteration int
	LastIteration  int
	IterationCount int
	ToolCount      int
}

// RegionRuns 把按 iteration 升序排列的迭代记录划分为展示区域。
//
// 算法与前端 mergeToolRuns（TurnBody.tsx:941-956）**逐条件同构**：
//
//	hasTools(rec) = 解析后工具数 > 0
//	absorbs(rec)  = IsPureToolIteration(rec)            // 纯工具迭代
//	head          = 首个 hasTools 迭代（**允许带 content/reasoning**）
//	贪心吸收 head 之后连续的 absorbs 成员；块保留 head 迭代号
//	带文本 / 无工具的迭代各自独立成块
//
// ⚠️ GenUI **不阻碍吸收**：前端 absorbs 不看 uiMode，Go 侧同样不看（一致性优先；
// 差异会直接导致两侧区域边界漂移 ⇒ 窗口边界错 ⇒ 前端拼接断号）。
//
// 输入必须是 iteration 升序（调用方保证：GetIterationHistoryByTurns 的 ORDER BY）。
func RegionRuns(recs []sqlite.IterationRecord) []RegionRun {
	if len(recs) == 0 {
		return nil
	}
	out := make([]RegionRun, 0, len(recs))
	for i := 0; i < len(recs); i++ {
		head := recs[i]
		headTools := len(parseRegionTools(head))
		if headTools == 0 {
			// 无工具迭代（纯文本 / 纯思考 / 空迭代）—— 独立成块。
			out = append(out, RegionRun{
				HeadIteration:  head.Iteration,
				FirstIteration: head.Iteration,
				LastIteration:  head.Iteration,
				IterationCount: 1,
				ToolCount:      0,
			})
			continue
		}
		// head 带工具：贪心吸收其后的连续纯工具迭代。
		j := i
		toolCount := headTools
		for j+1 < len(recs) && IsPureToolIteration(recs[j+1]) {
			j++
			toolCount += len(parseRegionTools(recs[j]))
		}
		out = append(out, RegionRun{
			HeadIteration:  head.Iteration,
			FirstIteration: head.Iteration,
			LastIteration:  recs[j].Iteration,
			IterationCount: j - i + 1,
			ToolCount:      toolCount,
		})
		i = j
	}
	return out
}

// RegionWindow 取「尾部 k 个区域」作为下发窗口（D2 内层分页）。
//
// 返回：
//   - window：窗口内的原始记录（**连续迭代号区间**：区域原子 ⇒ 起点必是某个区域的
//     头部，绝不从工具组中间切开）；
//   - regionsBefore：更早未下发的区域数（0 = 该 turn 已完整下发）。
//
// 语义：区域总数 ≤ k ⇒ 全量返回 + regionsBefore=0（普通 turn 完全不受影响）。
// k <= 0 ⇒ 空窗口 + regionsBefore=区域总数（合法退化，不是 panic）。
//
// ⚠️ 「窗口内迭代号连续」依赖输入不变量：同一 turn 的 iteration 号连续
// （DB 侧 1 起、续跑续接）。本函数只按**区域边界**切分，不缝补输入空洞。
func RegionWindow(recs []sqlite.IterationRecord, k int) ([]sqlite.IterationRecord, int) {
	runs := RegionRuns(recs)
	if len(runs) == 0 {
		return nil, 0
	}
	if k <= 0 {
		return nil, len(runs)
	}
	if len(runs) <= k {
		return recs, 0
	}
	// 起点 = 前 (总数-k) 个区域的迭代个数之和 —— 用下标切分而非按迭代号查找，
	// 保证任何输入下都严格落在区域边界上（区域原子）。
	start := 0
	for _, r := range runs[:len(runs)-k] {
		start += r.IterationCount
	}
	return recs[start:], len(runs) - k
}

// MapIterationRecord 把一条 DB 迭代记录映射为协议形态的 HistoryIteration
// （从 subscription.go:345-378 / :481-513 的装配处抽取，字段逐一保留现状行为）：
//
//   - tools JSON → []protocol.ToolProgress；label 为空则回落 name；
//     Elapsed←elapsed_ms、Iteration←rec.Iteration（每条工具都盖本迭代号）、
//     ui_mode/ui_libs/ui_surface 原样透传；
//   - 迭代级指标 Tokens/TTFTMs/TokensPerSec/TotalMs 透传；
//   - Tools 为空串/"[]"/解析失败 ⇒ tools 为 nil（与现状一致：字段被 omitempty 省略）。
//
// foldTools=true（D3 折叠视图，REST 路径）时：
//   - **非 GenUI 工具**（UIMode == ""）省略 Summary/Args/Detail/ToolHints（置空）——
//     pill 轻字段（name/label/status/elapsed_ms/iteration/ui_*）完整保留 ⇒ 默认渲染
//     与现状像素级一致；
//   - **GenUI 工具**（UIMode != ""）保留全部字段（豁免）；
//   - 只要存在至少一个非 GenUI 工具 ⇒ ToolsFolded=true。**GenUI-only 迭代不打标**
//     （没有任何字段被省略）。
//
// foldTools=false（CLI/RPC 路径）时等价于抽取前的现状装配，ToolsFolded 恒 false。
func MapIterationRecord(rec sqlite.IterationRecord, foldTools bool) HistoryIteration {
	snaps := parseRegionTools(rec)
	var tools []protocol.ToolProgress
	folded := false
	if len(snaps) > 0 {
		tools = make([]protocol.ToolProgress, len(snaps))
		for i, t := range snaps {
			label := t.Label
			if label == "" {
				label = t.Name
			}
			tp := protocol.ToolProgress{
				Name:      t.Name,
				Label:     label,
				Status:    t.Status,
				Elapsed:   t.ElapsedMS,
				Iteration: rec.Iteration,
				Summary:   t.Summary,
				Args:      t.Args,
				Detail:    t.Detail,
				UIMode:    t.UIMode,
				UILibs:    t.UILibs,
				UISurface: t.UISurface,
			}
			if foldTools && t.UIMode == "" {
				// 折叠：省略详情大字段。ToolHints 现状映射从未填充（恒空），
				// 显式置空是为了把「折叠语义」固化在此处，防止后续新增映射时漏掉。
				tp.Summary = ""
				tp.Args = ""
				tp.Detail = ""
				tp.ToolHints = ""
				folded = true
			}
			tools[i] = tp
		}
	}
	return HistoryIteration{
		Iteration:    rec.Iteration,
		Content:      rec.Content,
		Reasoning:    rec.Reasoning,
		Tools:        tools,
		Tokens:       rec.Tokens,
		TTFTMs:       rec.TTFTMs,
		TokensPerSec: rec.TokensPerSec,
		TotalMs:      rec.TotalMs,
		ToolsFolded:  folded,
	}
}

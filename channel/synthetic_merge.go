package channel

import (
	"encoding/json"
	"time"

	"xbot/llm"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// SyntheticToolPair 是 session_messages 里一个【注入型】合成工具对的投影态：
// assistant 行（恰好一个合成工具调用）+ 紧随的 tool 结果行。
//
// 2026-09-30 事故（chat_D3D0 turn 19）：pre_turn_end / bg task / cron 这类
// 注入型工具的执行结果只活在会话内存（CompletedTools），注入又发生在迭代
// 快照落库【之后】（见 agent 引擎的 Fix A 注释）⇒ 旧数据的 iteration_history
// tools JSON 永远缺这个工具。历史回放对有结构化数据的 turn 直接丢弃
// pendingIters ⇒ 刷新/切会话后前端永远渲染不出该工具。
//
// 修复模型（三个读取路径共用同一锚定语义）：
//   - View 主路径（ConvertMessagesToHistoryWithIterationsView）：完整 turn 的
//     迭代记录自锚定 —— MergeSyntheticToolPairs；
//   - POST /api/regions（段回放）：recs=段，anchors=完整 turn 锚点 ——
//     MergeSyntheticPairsIntoSegment；
//   - POST /api/iteration_detail（单条详情）：recs=单条，anchors=完整 turn
//     锚点 —— MergeSyntheticPairsIntoRecord。
//
// 铁律（同 storage.AppendIterationTool）：
//   - 合并只发生在副本上（copy-on-write），调用方的记录永不被改；
//   - 已有条目按 json.RawMessage 原样透传 —— 任何 regionToolSnap 不认识的
//     字段（如 Fix A 的 tool_hints）绝不丢失；
//   - 损坏的 tools JSON 一律跳过，绝不覆盖既有数据；
//   - 新数据（Fix A 持久层回写）防双写：按 (name, detail) 去重 ——
//     detail 比较用【全量】content（Fix A 持久化的是全量 Detail），写入用
//     截断版（与既有渲染路径 subscription.go:1171/:1392 同口径）。
type SyntheticToolPair struct {
	TurnID uint64
	TS     time.Time // assistant 注入行的时间戳 = 工具对的注入时刻
	Name   string
	Args   string
	CallID string
	// Content 是去重键的一部分 + pill 详情 —— 取自配对的 tool 结果行。
	// 工具结果行不在窗口内（分页边界劈开消息对）时该对被跳过：无内容可去重
	// 也无内容可渲染，宁可少一个 pill 也不造空 pill / 双写。
	Content string
}

// BuildToolResults 预扫描消息行，建立 ToolCallID → 工具结果内容的映射。
// View 主路径与按需端点共用（同一实现的唯一来源）。
func BuildToolResults(msgs []llm.ChatMessage) map[string]string {
	toolResults := make(map[string]string)
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID != "" {
			toolResults[m.ToolCallID] = m.Content
		}
	}
	return toolResults
}

// CollectSyntheticToolPairs 从消息行收集合成工具对（唯一实现）。
//
// 识别条件：assistant 行 + 恰好一个工具调用 + 合成工具名 + 配对的 tool
// 结果行在窗口内（Content 非空）。调用方负责「该 turn 有结构化迭代数据」的
// 前提 —— View 主路径经 turnIterMap 判定，按需端点天然成立（它们服务的
// 就是 iteration_history 里的 turn）。
func CollectSyntheticToolPairs(msgs []llm.ChatMessage, toolResults map[string]string) []SyntheticToolPair {
	var pairs []SyntheticToolPair
	for i := range msgs {
		m := &msgs[i]
		if m.Role != "assistant" || len(m.ToolCalls) != 1 || m.TurnID == 0 {
			continue
		}
		tc := &m.ToolCalls[0]
		if !tools.IsSyntheticToolName(tc.Name) {
			continue
		}
		content, hasContent := toolResults[tc.ID]
		if !hasContent || content == "" {
			continue
		}
		pairs = append(pairs, SyntheticToolPair{
			TurnID: m.TurnID, TS: m.Timestamp,
			Name: tc.Name, Args: tc.Arguments, CallID: tc.ID, Content: content,
		})
	}
	return pairs
}

// MergeSyntheticToolPairs 把 msgs 里的合成工具对合并进 turnIterMap 的副本。
// 返回值与入参 map 不同引用（仅当至少合并成功一次时才拷贝受影响的 turn）；
// 无工具对可合并时返回原 map（只读共享）。
//
// 必须在 ConvertMessagesToHistoryWithIterationsView 组装 HistoryIteration
// 【之前】调用 —— MapIterationRecord 是 tools JSON 的唯一解析入口，合并后的
// 记录经它自然获得工具，无需第二条装配路径。
func MergeSyntheticToolPairs(msgs []llm.ChatMessage, toolResults map[string]string, turnIterMap map[uint64][]sqlite.IterationRecord) map[uint64][]sqlite.IterationRecord {
	if len(turnIterMap) == 0 {
		return turnIterMap
	}
	pairs := CollectSyntheticToolPairs(msgs, toolResults)
	if len(pairs) == 0 {
		return turnIterMap
	}
	byTurn := make(map[uint64][]SyntheticToolPair, len(pairs))
	for _, p := range pairs {
		// 该 turn 没有结构化迭代记录 ⇒ 走 legacy pendingIters 路径，工具对
		// 本来就会渲染，不需要（也不能）合并。
		if _, hasTurn := turnIterMap[p.TurnID]; !hasTurn {
			continue
		}
		byTurn[p.TurnID] = append(byTurn[p.TurnID], p)
	}
	if len(byTurn) == 0 {
		return turnIterMap
	}
	merged := turnIterMap
	mapCopied := false
	for turnID, turnPairs := range byTurn {
		// 完整 turn 记录：锚点即记录本身（自锚定）。
		recs, changed := mergePairsWithAnchors(turnIterMap[turnID], turnPairs, turnIterMap[turnID])
		if !changed {
			continue
		}
		if !mapCopied {
			// 首次改动才拷贝整个 map（copy-on-write）；未受影响的 turn 共享原切片。
			clone := make(map[uint64][]sqlite.IterationRecord, len(turnIterMap))
			for id, rs := range turnIterMap {
				clone[id] = rs
			}
			merged = clone
			mapCopied = true
		}
		merged[turnID] = recs
	}
	return merged
}

// MergeSyntheticPairsIntoSegment 把合成工具对合并进【段】迭代记录（
// POST /api/regions 的旧数据修复）。anchors 必须是该 turn 的完整迭代锚点
// 列表（GetIterationAnchorsByTurn 的轻量查询 —— 锚定语义与 View 主路径
// 完全一致）；锚定到段外迭代的对被跳过（不归本段渲染 —— 前端翻到含该
// 迭代的段时那一次请求会补上）。
//
// copy-on-write：无命中时返回原切片（零分配）。
func MergeSyntheticPairsIntoSegment(recs []sqlite.IterationRecord, pairs []SyntheticToolPair, anchors []sqlite.IterationRecord) []sqlite.IterationRecord {
	out, _ := mergePairsWithAnchors(recs, pairs, anchors)
	return out
}

// MergeSyntheticPairsIntoRecord 把合成工具对合并进【单条】迭代记录（
// POST /api/iteration_detail 的旧数据修复）。仅锚定到该迭代号的对会并入；
// anchors 与 MergeSyntheticPairsIntoSegment 同一来源。返回值可能是原值
// （无命中时零分配）。
func MergeSyntheticPairsIntoRecord(rec sqlite.IterationRecord, pairs []SyntheticToolPair, anchors []sqlite.IterationRecord) sqlite.IterationRecord {
	out, _ := mergePairsWithAnchors([]sqlite.IterationRecord{rec}, pairs, anchors)
	return out[0]
}

// mergePairsWithAnchors 统一合并核心（三个读取路径共用）。
//
// 锚定模型：注入发生在【最后一个已落盘快照】之后的时刻 ⇒ 在 anchors 上取
// CreatedAt 严格早于注入时刻的最后一条（与 live 渲染同一迭代 —— 注入时它
// 就是"当前迭代"）；时间不可用（老数据无 created_at）回落到 anchors 最后
// 一条。锚定得到的是【迭代号】，再在 recs 里按迭代号定位记录：
//   - View 主路径（recs == anchors，完整 turn）必命中；
//   - 段/单条视图未覆盖锚定迭代 ⇒ 跳过（该对不归本段渲染）。
//
// recs 只读；返回 (可能新的切片, 是否有改动)。未改动时返回原切片。
func mergePairsWithAnchors(recs []sqlite.IterationRecord, pairs []SyntheticToolPair, anchors []sqlite.IterationRecord) ([]sqlite.IterationRecord, bool) {
	if len(recs) == 0 || len(pairs) == 0 {
		return recs, false
	}
	out := recs
	copied := false
	ensureCopy := func() {
		if !copied {
			out = append([]sqlite.IterationRecord(nil), recs...)
			copied = true
		}
	}
	changed := false
	for _, p := range pairs {
		anchor, ok := anchorPairRecord(anchors, p.TS)
		if !ok {
			continue
		}
		targetIter := anchors[anchor].Iteration
		idx := -1
		for i := range recs {
			if recs[i].Iteration == targetIter {
				idx = i
				break
			}
		}
		if idx < 0 {
			continue // 锚定到本视图之外的迭代 —— 跳过
		}
		// RawMessage 透传：已有条目原样保留（不丢 tool_hints 等新字段）。
		var raws []json.RawMessage
		if cur := out[idx].Tools; cur != "" && cur != "[]" {
			if err := json.Unmarshal([]byte(cur), &raws); err != nil {
				continue // 损坏的 tools JSON：跳过，绝不覆盖既有数据
			}
		}
		dup := false
		for _, raw := range raws {
			var sn regionToolSnap
			if json.Unmarshal(raw, &sn) == nil && sn.Name == p.Name && sn.Detail == p.Content {
				dup = true
				break
			}
		}
		if dup {
			continue // Fix A 已持久化该工具 —— 防双写
		}
		snaps := append(raws, mustMarshalRegionToolSnap(p))
		b, err := json.Marshal(snaps)
		if err != nil {
			continue
		}
		ensureCopy()
		out[idx].Tools = string(b)
		changed = true
	}
	return out, changed
}

// anchorPairRecord 按注入时间戳在（完整 turn 的）迭代记录列表上定位锚点。
//
// 锚定规则：取 CreatedAt 严格早于注入时刻的最后一条记录；时间不可用（老数据
// 无 created_at / 注入行无时间戳）时回落到最后一条 —— 注入发生在最后一个已
// 落盘快照之后的兜底语义（与 live 渲染一致）。
func anchorPairRecord(recs []sqlite.IterationRecord, ts time.Time) (int, bool) {
	if len(recs) == 0 {
		return 0, false
	}
	anchor := -1
	if !ts.IsZero() {
		for i := range recs {
			ri := &recs[i]
			if ri.CreatedAt.IsZero() || !ri.CreatedAt.Before(ts) {
				continue
			}
			anchor = i
		}
	}
	if anchor < 0 {
		anchor = len(recs) - 1
	}
	return anchor, true
}

func mustMarshalRegionToolSnap(p SyntheticToolPair) json.RawMessage {
	sn := regionToolSnap{
		Name:   p.Name,
		Label:  formatToolLabel(p.Name, p.Args),
		Status: "done",
		Args:   p.Args,
		Detail: tools.TruncateHeadPreview(p.Content, maxHistoryToolPreview),
	}
	b, err := json.Marshal(sn)
	if err != nil {
		// regionToolSnap 是纯 POD，序列化不可能失败；防御只会掩盖 bug。
		panic(err)
	}
	return b
}

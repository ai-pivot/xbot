package channel

import (
	"strings"
	"testing"
	"time"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// compressMarker 构造一个压缩标记行 —— 落库/回放形态：role=user、turn_id=0、
// content 前缀 "[Compacted context]"（见 storage.replayDisplayRecords 与
// agent/compress.go 的 llm.NewUserMessage("[Compacted context]\n\n"+summary)）。
func compressMarker(summary string, ts time.Time) llm.ChatMessage {
	return llm.ChatMessage{Role: "user", Content: "[Compacted context]\n\n" + summary, Timestamp: ts}
}

func findTurnRow(t *testing.T, out []HistoryMessage, turnID uint64) *HistoryMessage {
	t.Helper()
	for i := range out {
		if out[i].Role == "assistant" && out[i].TurnID == turnID {
			return &out[i]
		}
	}
	return nil
}

func hasStandaloneCompactMarker(out []HistoryMessage) bool {
	for _, h := range out {
		if h.Standalone && strings.HasPrefix(strings.TrimSpace(h.Content), "[Compacted context]") {
			return true
		}
	}
	return false
}

// 重新设计（2026-09-26，用户：「如果真的是在一个 turn 中间压缩的，应该插在 Iter
// 中间，跟 Iter 同级渲染，类似 Cursor 的 context summarized」）：
//
// 压缩由 agent.maybeCompress 在 LLM 请求前触发 ⇒ 恒在**迭代边界** ⇒ 归属「它所在的
// 那个 turn」，渲染在**迭代之间**。后端把它挂到该 turn 的 assistant 行
// （`Compactions` + `AfterIteration`），而不是独立的 standalone 行。
//
// 定位 = 该 turn 中 created_at 早于压缩时刻的最大迭代号（此处迭代 1 已在压缩前、
// 迭代 2 在压缩后 ⇒ AfterIteration=1）。
func TestCompactMarker_AttachedToTurnAsInlineCompaction(t *testing.T) {
	base := time.Date(2026, 9, 26, 5, 0, 0, 0, time.UTC)
	markerTS := base.Add(41 * time.Minute) // 05:41（示例 tenant 的真实压缩时刻）
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "turn3 的用户消息", TurnID: 3},
		{Role: "assistant", Content: "turn3 迭代", TurnID: 3},
		compressMarker("context compacted summary", markerTS),
		{Role: "assistant", Content: "turn3 继续迭代", TurnID: 3},
		{Role: "user", Content: "继续优化到 7ms，你的上下文无限", TurnID: 4},
		{Role: "assistant", Content: "turn4 迭代", TurnID: 4},
	}
	turnIterMap := map[uint64][]sqlite.IterationRecord{
		3: {
			{Iteration: 1, Content: "turn3 迭代", CreatedAt: base.Add(10 * time.Minute)},
			{Iteration: 2, Content: "turn3 继续迭代", CreatedAt: base.Add(50 * time.Minute)},
		},
		4: {{Iteration: 1, Content: "turn4 迭代", CreatedAt: base.Add(60 * time.Minute)}},
	}

	out := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)

	// ① 绝无独立的压缩标记行（它现在内联在 turn 里）。
	if hasStandaloneCompactMarker(out) {
		t.Fatalf("压缩标记不应是独立行（应内联在 turn 的 Compactions 上）: %+v", out)
	}
	// ② 归属正确的 turn（3）—— 绝不是下一个 turn（4，用户消息所在 turn）。
	turn3 := findTurnRow(t, out, 3)
	if turn3 == nil {
		t.Fatalf("turn 3 的 assistant 行必须存在: %+v", out)
	}
	if len(turn3.Compactions) != 1 {
		t.Fatalf("turn 3 必须带 1 个内联压缩点，got %d: %+v", len(turn3.Compactions), turn3.Compactions)
	}
	c := turn3.Compactions[0]
	if c.AfterIteration != 1 {
		t.Fatalf("AfterIteration 应为 1（迭代 1 在压缩前、迭代 2 在压缩后），got %d", c.AfterIteration)
	}
	if !strings.Contains(c.Content, "context compacted summary") {
		t.Fatalf("内联压缩点必须带摘要正文，got %q", c.Content)
	}
	// ③ 用户消息所在的 turn 4 不得被污染。
	turn4 := findTurnRow(t, out, 4)
	if turn4 != nil && len(turn4.Compactions) != 0 {
		t.Fatalf("turn 4 不得带压缩点（用户消息所在 turn）: %+v", turn4.Compactions)
	}
}

// 老数据（无结构化迭代 ⇒ 无法定位迭代位置）回落为独立的 standalone 标记行 ——
// 前端仍支持该形态（基本兼容），信息不丢。
func TestCompactMarker_LegacyFallbackStandaloneWhenNoIterations(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "turn3 的用户消息", TurnID: 3},
		{Role: "assistant", Content: "turn3 迭代", TurnID: 3},
		compressMarker("summary", time.Now()),
		{Role: "user", Content: "继续优化到 7ms", TurnID: 4},
		{Role: "assistant", Content: "turn4 迭代", TurnID: 4},
	}
	// 无结构化迭代（turnIterMap 缺失 ⇒ 走 legacy 转换路径）。
	out := ConvertMessagesToHistory(msgs)

	if !hasStandaloneCompactMarker(out) {
		t.Fatalf("无结构化迭代时压缩标记必须回落为 standalone 行: %+v", out)
	}
	for _, h := range out {
		if h.Standalone && strings.HasPrefix(strings.TrimSpace(h.Content), "[Compacted context]") {
			if h.TurnID != 0 {
				t.Fatalf("standalone 标记 turnID 必须保持 0，got %d", h.TurnID)
			}
			if h.AnchorTurnID != 3 {
				t.Fatalf("standalone 标记锚点应为 3（发生在 turn 3 中间），got %d", h.AnchorTurnID)
			}
		}
	}
	// 用户消息绝不被顶掉（turn 4 的 user 行仍在）。
	var turn4User *HistoryMessage
	for i := range out {
		if out[i].Role == "user" && out[i].TurnID == 4 {
			turn4User = &out[i]
		}
	}
	if turn4User == nil || !strings.Contains(turn4User.Content, "继续优化到 7ms") {
		t.Fatalf("用户消息必须保留（P0 不得回归）: %+v", out)
	}
}

// 迭代记录缺 created_at（老数据）时同样回落 standalone —— 绝不把压缩点错放到
// 无法验证的位置。
func TestCompactionIteration_NoTimestampFallsBack(t *testing.T) {
	recs := map[uint64][]sqlite.IterationRecord{
		3: {{Iteration: 1}, {Iteration: 2}}, // 无 CreatedAt
	}
	if _, ok := compactionIteration(recs, 3, time.Now()); ok {
		t.Fatal("迭代无 created_at 时必须回落（ok=false）")
	}
	if _, ok := compactionIteration(recs, 0, time.Now()); ok {
		t.Fatal("无 turn 时必须回落（ok=false）")
	}
	if _, ok := compactionIteration(recs, 3, time.Time{}); ok {
		t.Fatal("压缩时刻缺失时必须回落（ok=false）")
	}
}

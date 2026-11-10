package channel

import (
	"strings"
	"testing"
	"time"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// ── 压缩点归属（2026-10-14 用户亲报「一串『上下文已压缩』堆在一起」的根因）──────────────
//
// 症状（真机/真数据实测）：同一 turn 的**全部**压缩点 `after_iteration` 完全相同
//（turn128 的 15 条全是 2000、turn129 的 16 条全是 2000…），前端按 after_iteration
// 把压缩点渲在迭代之间 ⇒ N 条挤在同一个边界 = 用户截图里那一串。
//
// 根因：归属用的是「标记在消息流里的位置所属的 turn」（cmdAnchor），而压缩标记在回放里
// 落在**被替换区间的起点** ⇒ 归属落到**被压缩内容的最后一个 turn**，而压缩实际发生在
// **更晚**的 turn 里 ⇒ 时间戳晚于该 turn 全部迭代 ⇒ after_iteration 退化成"该 turn 最大
// 迭代号" ⇒ 全部压缩点拿到同一锚点。实测 144 条压缩点**没有一条**落在归属 turn 的迭代
// 区间内。
//
// 修法：`compactionAnchor` 用**全局迭代时间线**把压缩点归到 ts 时刻真正在跑的 turn。
// 下面 ① 是端到端（走 ConvertMessagesToHistoryWithIterations，钉调用点），
// ②③④⑤ 直接钉 compactionAnchor 的语义边界。

// TestCompactionAnchor_AttributedToRunningTurnNotStreamPosition 是**本 bug 的回归钉**：
// 标记在消息流里的位置紧跟在 turn 10 之后（cmdAnchor=10），但它的时间戳落在 turn 11 的
// 迭代区间内 ⇒ 必须归到 turn 11，且锚点 = turn 11 内在它之前已完成的迭代号。
// ⛔ 旧实现（cmdAnchor + 该 turn 最大迭代）会把它归到 turn 10 且锚点=2 ⇒ 本条必红。
func TestCompactionAnchor_AttributedToRunningTurnNotStreamPosition(t *testing.T) {
	base := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	// turn 10 的迭代在 01:00–01:20 结束；turn 11 的迭代在 02:00–03:00。
	// 压缩发生在 02:40 —— 那会儿 turn 11 正在跑。
	markerTS := base.Add(100 * time.Minute)
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "turn10 用户", TurnID: 10},
		{Role: "assistant", Content: "turn10 完成", TurnID: 10},
		compressMarker("mid-turn11 compaction", markerTS),
		{Role: "assistant", Content: "turn11 迭代A", TurnID: 11},
		{Role: "user", Content: "turn12 用户", TurnID: 12},
		{Role: "assistant", Content: "turn12 完成", TurnID: 12},
	}
	turnIterMap := map[uint64][]sqlite.IterationRecord{
		10: {
			{Iteration: 1, CreatedAt: base.Add(5 * time.Minute)},
			{Iteration: 2, CreatedAt: base.Add(20 * time.Minute)},
		},
		11: {
			{Iteration: 1, CreatedAt: base.Add(60 * time.Minute)},
			{Iteration: 2, CreatedAt: base.Add(80 * time.Minute)},
			{Iteration: 3, CreatedAt: base.Add(120 * time.Minute)},
		},
		12: {{Iteration: 1, CreatedAt: base.Add(180 * time.Minute)}},
	}

	out := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)

	if hasStandaloneCompactMarker(out) {
		t.Fatalf("压缩标记必须内联在 turn 上，不该回落 standalone: %+v", out)
	}
	turn10 := findTurnRow(t, out, 10)
	if turn10 != nil && len(turn10.Compactions) != 0 {
		t.Fatalf("⛔ 压缩点不得归到 turn10（它早已结束）：%+v", turn10.Compactions)
	}
	turn11 := findTurnRow(t, out, 11)
	if turn11 == nil {
		t.Fatalf("turn11 的 assistant 行必须存在")
	}
	if len(turn11.Compactions) != 1 {
		t.Fatalf("压缩点必须归到 turn11（压缩发生时它在跑），got %d: %+v", len(turn11.Compactions), turn11.Compactions)
	}
	// turn11 内在 02:40 之前完成的迭代 = 1（02:00）与 2（02:20）⇒ 锚点 2。
	if got := turn11.Compactions[0].AfterIteration; got != 2 {
		t.Fatalf("AfterIteration 必须按 turn11 自己的迭代算（应为 2），got %d", got)
	}
	if !strings.Contains(turn11.Compactions[0].Content, "mid-turn11 compaction") {
		t.Fatalf("压缩点必须带摘要正文")
	}
}

// TestCompactionAnchor_DistinctAnchorsForMultipleCompactions 钉**用户症状本身**：
// 同一个 turn 内不同时刻的多次压缩必须拿到**互不相同**的锚点（否则前端堆叠）。
func TestCompactionAnchor_DistinctAnchorsForMultipleCompactions(t *testing.T) {
	base := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	turnIterMap := map[uint64][]sqlite.IterationRecord{
		7: {
			{Iteration: 1, CreatedAt: base.Add(10 * time.Minute)},
			{Iteration: 2, CreatedAt: base.Add(20 * time.Minute)},
			{Iteration: 3, CreatedAt: base.Add(30 * time.Minute)},
		},
	}
	spans := buildTurnSpans(turnIterMap)
	got := []int{}
	for _, min := range []int{15, 25, 35} {
		_, after, ok := compactionAnchor(turnIterMap, spans, 7, base.Add(time.Duration(min)*time.Minute))
		if !ok {
			t.Fatalf("ts=+%dm 必须能定位（turn7 有迭代时间戳）", min)
		}
		got = append(got, after)
	}
	// 期望 1、2、3 —— 三次压缩分别落在迭代 1/2/3 之后。
	want := []int{1, 2, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("⛔ 同 turn 多次压缩的锚点必须互不相同（旧实现全退化成一个值）: got %v want %v", got, want)
		}
	}
	if got[0] == got[1] || got[1] == got[2] {
		t.Fatalf("锚点重复 = 前端会堆叠: %v", got)
	}
}

// TestCompactionAnchor_GapAfterLastIterationKeepsOwner：压缩发生在某 turn 末迭代之后、
// 下一个 turn 开始之前（turn 之间的空隙）⇒ 归属仍是**该** turn（不能前移/后移到别的 turn）。
func TestCompactionAnchor_GapAfterLastIterationKeepsOwner(t *testing.T) {
	base := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	turnIterMap := map[uint64][]sqlite.IterationRecord{
		3: {{Iteration: 1, CreatedAt: base.Add(1 * time.Minute)}},
		4: {{Iteration: 1, CreatedAt: base.Add(10 * time.Minute)}},
	}
	spans := buildTurnSpans(turnIterMap)
	owner, after, ok := compactionAnchor(turnIterMap, spans, 99, base.Add(5*time.Minute))
	if !ok || owner != 3 || after != 1 {
		t.Fatalf("空隙内的压缩应归 turn3 且锚点=1（该 turn 末迭代），got owner=%d after=%d ok=%v", owner, after, ok)
	}
}

// TestCompactionAnchor_FallsBackWhenNoTimeline：时间线为空（老数据/迭代无时间戳）⇒
// `ok=false` —— 与**旧行为逐字一致**：调用方回落为独立的 standalone 标记行（信息绝不丢）。
// 另一种回落：时间线存在但 ts **早于所有** turn 的首迭代 ⇒ 用 fallbackTurn（= 旧 cmdAnchor）。
func TestCompactionAnchor_FallsBackWhenNoTimeline(t *testing.T) {
	base := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	turnIterMap := map[uint64][]sqlite.IterationRecord{
		5: {{Iteration: 1}, {Iteration: 2}}, // 无 CreatedAt ⇒ 不进时间线
	}
	spans := buildTurnSpans(turnIterMap)
	if len(spans) != 0 {
		t.Fatalf("无时间戳的 turn 不应进时间线，got %+v", spans)
	}
	// ① 完全无法定位 ⇒ ok=false（调用方回落 standalone；⛔ 不许"猜一个锚点"）。
	if owner, after, ok := compactionAnchor(turnIterMap, spans, 5, base); ok {
		t.Fatalf("无任何迭代时间戳时不得假装定位成功：owner=%d after=%d", owner, after)
	}
	// ② 零时间戳同理（旧行为也是 compactionIteration 直接 false）。
	if _, _, ok := compactionAnchor(turnIterMap, spans, 5, time.Time{}); ok {
		t.Fatalf("零时间戳不得定位成功")
	}
	// ③ fallback=0 且无时间线 ⇒ 同样 ok=false（绝无 turn0 这种归属）。
	if _, _, ok := compactionAnchor(turnIterMap, spans, 0, time.Time{}); ok {
		t.Fatalf("无 turn 可归属时必须 ok=false")
	}
}

// TestCompactionAnchor_EarlierThanAllTurnsUsesFallback：ts 早于所有 turn 的首迭代
// （标记出现在结构化迭代数据之前的老段）⇒ 用 fallbackTurn（旧 cmdAnchor 语义）。
func TestCompactionAnchor_EarlierThanAllTurnsUsesFallback(t *testing.T) {
	base := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	turnIterMap := map[uint64][]sqlite.IterationRecord{
		6: {{Iteration: 1, CreatedAt: base.Add(30 * time.Minute)}, {Iteration: 2, CreatedAt: base.Add(40 * time.Minute)}},
	}
	spans := buildTurnSpans(turnIterMap)
	owner, after, ok := compactionAnchor(turnIterMap, spans, 6, base.Add(10*time.Minute))
	if !ok || owner != 6 || after != 0 {
		t.Fatalf("早于所有迭代的压缩应回落 fallbackTurn=6 且锚点=0（该 turn 首迭代之前），got owner=%d after=%d ok=%v", owner, after, ok)
	}
}

// TestCompactionAnchor_IntegrationKeepsStandaloneOnNoTimestamps：端到端确认老数据形态
// 不变 —— 无迭代时间戳 ⇒ 压缩标记仍是 standalone 行（前端支持，信息不丢）。
func TestCompactionAnchor_IntegrationKeepsStandaloneOnNoTimestamps(t *testing.T) {
	base := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "u", TurnID: 2},
		{Role: "assistant", Content: "a", TurnID: 2},
		compressMarker("legacy no-ts", base.Add(5*time.Minute)),
	}
	turnIterMap := map[uint64][]sqlite.IterationRecord{
		2: {{Iteration: 1}, {Iteration: 2}}, // 无 CreatedAt（老数据）
	}
	out := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)
	if !hasStandaloneCompactMarker(out) {
		t.Fatalf("无时间戳的老数据必须回落 standalone 标记行（信息不丢）: %+v", out)
	}
}

// TestCompactionAnchor_AfterAllIterationsUsesLastTurn：压缩时间戳晚于所有迭代
// （会话末尾/长时间空闲后的压缩）⇒ 归属最后一个已开始的 turn，锚点=它的最大迭代号。
// 这条**故意保留旧语义**：无处可归时"落在最后一个 turn 末尾"是最不坏的近似。
func TestCompactionAnchor_AfterAllIterationsUsesLastTurn(t *testing.T) {
	base := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	turnIterMap := map[uint64][]sqlite.IterationRecord{
		8:  {{Iteration: 1, CreatedAt: base.Add(1 * time.Minute)}},
		9:  {{Iteration: 1, CreatedAt: base.Add(10 * time.Minute)}, {Iteration: 2, CreatedAt: base.Add(20 * time.Minute)}},
		10: {{Iteration: 1, CreatedAt: base.Add(30 * time.Minute)}},
	}
	spans := buildTurnSpans(turnIterMap)
	owner, after, ok := compactionAnchor(turnIterMap, spans, 0, base.Add(90*time.Minute))
	if !ok || owner != 10 || after != 1 {
		t.Fatalf("晚于所有迭代的压缩应归最后一个 turn（10）且锚点=1，got owner=%d after=%d ok=%v", owner, after, ok)
	}
}

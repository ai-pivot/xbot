package channel

import (
	"testing"
	"time"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// ── 压缩分隔条堆叠事故的**生产形态**端到端回归（2026-10-14）─────────────────────
//
// 与 compaction_anchor_test.go 的纯 struct 用例不同：本文件走**真实 SQLite**
// 落库→扫描（iteration_history.created_at = CURRENT_TIMESTAMP 写出的 UTC 裸串，
// 逐字节复刻 web:chat_026B326DE4C4 的生产数据）+ 压缩标记时间 = Go 写侧形态
// （RFC3339 带本地偏移，+08:00）—— 正是这两套时间表示混存，才让纯 struct 测试
// 全绿、线上却把同窗全部压缩点退化成「最后一个 turn + 其最大迭代号」
// （live API 实测 5 条全是 after_iteration=30；用户 DOM 实测 turn-154 后连摞 4 条
// 「上下文已压缩」，且这些压缩都发生在 very early 迭代上）。
//
// 修复链：AppendIterationHistory 显式写 canonical created_at（不再依赖 DDL 默认）
// + 读侧 parseIterationTime 把 CURRENT_TIMESTAMP 裸串按 **UTC** 解析。
//
// ⚠️ 审计时必须以两种宿主时区各跑一遍：
//
//	TZ=Asia/Shanghai go test ./channel/ -run TestCompactionAnchor_LegacyUTC -count=1
//	TZ=UTC           go test ./channel/ -run TestCompactionAnchor_LegacyUTC -count=1

func seedLegacyUTCIteration(t *testing.T, db *sqlite.DB, tenantID int64, turnID uint64, iteration int, rawUTC string) {
	t.Helper()
	if _, err := db.Conn().Exec(
		`INSERT INTO iteration_history (message_id, tenant_id, turn_id, iteration, content, created_at)
		 VALUES (0, ?, ?, ?, 'iter', ?)`,
		tenantID, turnID, iteration, rawUTC); err != nil {
		t.Fatalf("seed legacy iteration_history: %v", err)
	}
}

// TestCompactionAnchor_LegacyUTCBareSpansZonedMarker：生产数据形态 —— 裸 UTC
// 迭代时间线 + 带偏移的压缩标记 —— 标记必须归到**真实在跑的 turn** 的**真实迭代
// 边界**（很早的迭代上），而不是堆到最后一个 turn 的末尾。
//
// 生产实测样本（本地 +08:00）：
//
//	turn 152 迭代 1..4：17:10:31 / 17:10:45 / 17:11:10 / 17:12:00
//	  ⇒ CURRENT_TIMESTAMP 落库= UTC 裸 "2026-10-14 09:10:31" …
//	压缩标记：17:11:20+08:00（= 09:11:20Z，落在 turn152 迭代 3 之后、迭代 4 之前）
//	turn 155（更晚、最后一个 turn）：19:24:53 / 19:25:10 本地 —— 绝不是标记的归属。
//
// ⛔ 修复前（宿主 TZ=+08:00）：迭代被 -8h 解析 ⇒ 标记"晚于全部 turn 区间" ⇒
// owner=155（时间线最后一个 turn）、after=155 的最大迭代号（2）⇒ 与用户 DOM 完全一致
// 的堆叠形态 ⇒ 本条必红。
func TestCompactionAnchor_LegacyUTCBareSpansZonedMarker(t *testing.T) {
	db, err := sqlite.Open(t.TempDir() + "/tz-funnel.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	tenantSvc := sqlite.NewTenantService(db)
	sessionSvc := sqlite.NewSessionService(db)
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat-tz-funnel")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	// turn 152（真实：17:10:31 起跑的长 turn）—— CURRENT_TIMESTAMP 裸 UTC。
	seedLegacyUTCIteration(t, db, tenantID, 152, 1, "2026-10-14 09:10:31")
	seedLegacyUTCIteration(t, db, tenantID, 152, 2, "2026-10-14 09:10:45")
	seedLegacyUTCIteration(t, db, tenantID, 152, 3, "2026-10-14 09:11:10")
	seedLegacyUTCIteration(t, db, tenantID, 152, 4, "2026-10-14 09:12:00")
	// turn 155（真实：更晚的小 turn —— 修复前会被误当"归属"）
	seedLegacyUTCIteration(t, db, tenantID, 155, 1, "2026-10-14 11:24:53")
	seedLegacyUTCIteration(t, db, tenantID, 155, 2, "2026-10-14 11:25:10")

	turnIterMap, err := sessionSvc.GetIterationHistoryByTurns(tenantID, []uint64{152, 155})
	if err != nil {
		t.Fatalf("GetIterationHistoryByTurns: %v", err)
	}
	if len(turnIterMap[152]) != 4 || len(turnIterMap[155]) != 2 {
		t.Fatalf("seed shape broken: %+v", turnIterMap)
	}

	// 压缩标记：Go 写侧形态（RFC3339 +08:00），17:11:20 本地 —— turn 152 迭代 3 之后。
	//（生产路径：storage/internal.ParseTimestamp case 1 = time.Parse(RFC3339)，语义等价。）
	markerTS, err := time.Parse(time.RFC3339, "2026-10-14T17:11:20+08:00")
	if err != nil {
		t.Fatalf("marker ts: %v", err)
	}
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "turn152 用户消息", TurnID: 152},
		{Role: "assistant", Content: "turn152 迭代", TurnID: 152},
		compressMarker("mid-turn compaction at iter3", markerTS),
		{Role: "assistant", Content: "turn152 压缩后继续", TurnID: 152},
		{Role: "user", Content: "turn155 用户消息", TurnID: 155},
		{Role: "assistant", Content: "turn155 迭代", TurnID: 155},
	}

	out := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)

	if hasStandaloneCompactMarker(out) {
		t.Fatalf("时间戳可用时压缩标记必须内联在 turn 上（该走 standalone 说明解析失败）: %+v", out)
	}
	// 绝不落到"时间线最后一个 turn"（155）—— 那是用户看到的堆叠位置。
	turn155Row := findTurnRow(t, out, 155)
	if turn155Row != nil && len(turn155Row.Compactions) != 0 {
		t.Fatalf("⛔ 压缩点不得归到 turn155（它 19:24 才开始跑，压缩 17:11 就发生了）: %+v", turn155Row.Compactions)
	}
	// 必须归到真实在跑的 turn 152、且锚点是**很早的迭代 3**。
	turn152Row := findTurnRow(t, out, 152)
	if turn152Row == nil {
		t.Fatalf("turn 152 的 assistant 行必须存在")
	}
	if len(turn152Row.Compactions) != 1 {
		t.Fatalf("压缩点必须归到 turn152 且只有 1 条, got %d: %+v", len(turn152Row.Compactions), turn152Row.Compactions)
	}
	if got := turn152Row.Compactions[0].AfterIteration; got != 3 {
		t.Fatalf("AfterIteration 必须是压缩真实发生处的迭代边界（3），got %d —— 得到 turn 级最大迭代/末尾形态 = 堆叠根因仍在", got)
	}
}

// TestCompactionAnchor_SpansFromDBDontDependOnHostTZ：同一批生产数据在**任意
// 宿主时区**下必须给出同一批 instant —— 时区只许影响墙钟显示，绝不许影响
// 「压缩归到哪个 turn 的哪个迭代」这类**时序事实**判定。
// （双跑判据见文件头：TZ=Asia/Shanghai 与 TZ=UTC 的 go test 各跑一遍。）
func TestCompactionAnchor_SpansFromDBDontDependOnHostTZ(t *testing.T) {
	db, err := sqlite.Open(t.TempDir() + "/tz-invariant.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	tenantSvc := sqlite.NewTenantService(db)
	sessionSvc := sqlite.NewSessionService(db)
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat-tz-invariant")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	seedLegacyUTCIteration(t, db, tenantID, 9, 1, "2026-10-14 08:00:00")
	seedLegacyUTCIteration(t, db, tenantID, 9, 2, "2026-10-14 08:10:00")
	seedLegacyUTCIteration(t, db, tenantID, 9, 3, "2026-10-14 08:20:00")

	turnIterMap, err := sessionSvc.GetIterationHistoryByTurns(tenantID, []uint64{9})
	if err != nil {
		t.Fatalf("GetIterationHistoryByTurns: %v", err)
	}
	recs := turnIterMap[9]
	if len(recs) != 3 {
		t.Fatalf("want 3 records, got %d", len(recs))
	}
	// 期望值全部用 UTC instant 构造 —— 与运行该测试的宿主时区无关。
	want := []time.Time{
		time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 14, 8, 10, 0, 0, time.UTC),
		time.Date(2026, 10, 14, 8, 20, 0, 0, time.UTC),
	}
	for i := range want {
		if !recs[i].CreatedAt.Equal(want[i]) {
			t.Fatalf("⛔ 迭代[%d] instant 随宿主时区漂移: got %v want %v（时间表示解析时区依赖 = 事故根因形态）", i, recs[i].CreatedAt, want[i])
		}
	}
	// 时间线锚（compactionIteration）在这种数据上必须稳定给出同一批结果：
	// 08:15Z 的压缩 ⇒ 锚点 = 迭代 2。
	if _, after, ok := compactionAnchor(
		turnIterMap,
		buildTurnSpans(turnIterMap),
		9,
		time.Date(2026, 10, 14, 8, 15, 0, 0, time.UTC),
	); !ok || after != 2 {
		t.Fatalf("compactionAnchor 必须与宿主时区无关地得到锚点 2, got after=%d ok=%v", after, ok)
	}
}

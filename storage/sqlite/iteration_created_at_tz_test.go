package sqlite

import (
	"strings"
	"testing"
	"time"
)

// ── 时区铁律回归（2026-10-14 用户亲报「压缩分隔条堆一摞」的真根因）──────────────
//
// 生产形态（实测 web:chat_026B326DE4C4 会话 DB）：
//   - iteration_history.created_at 由 DDL 默认 `CURRENT_TIMESTAMP` 写出 =
//     **UTC 裸字符串**（"2026-10-14 09:10:31"）—— AppendIterationHistory 的
//     INSERT 从不写这一列；
//   - session_messages.created_at（压缩标记行所在表）由 Go 写
//     `time.RFC3339(Nano)` = **带本地偏移**（"2026-10-14T17:22:20.601938+08:00"）。
// 两套形态混存时，把裸串按 time.Local(+08:00) 解析 ⇒ 迭代时刻整体早 8 小时 ⇒
// 压缩标记的时间戳"晚于"全部 turn 时间线 ⇒ compactionAnchor 退化（owner=最后一个
// turn、afterIteration=其最大迭代号）⇒ 同窗全部「上下文已压缩」分隔条堆叠
// （live API 实测 5 条全是 after_iteration=30；用户 DOM 实测 turn-154 后连摞 4 条）。
//
// 本文件钉死三件事：
//  1. 迭代时刻必须按真实 instant 读回（CURRENT_TIMESTAMP 裸串按 **UTC**）—— 与宿主时区无关；
//  2. AppendIterationHistory 必须显式写带偏移的 canonical created_at
//     （统一时间表示：从此不依赖 CURRENT_TIMESTAMP 默认）；
//  3. 无法识别的时间格式 fail-safe 为零值（下游回落，绝不猜错）
//
// ⚠️ 审计时必须以两种宿主时区各跑一遍（同一条数据必须给出同一批 instant）：
//
//	TZ=Asia/Shanghai go test ./storage/sqlite/ -run TestIterationCreatedAt -count=1
//	TZ=UTC           go test ./storage/sqlite/ -run TestIterationCreatedAt -count=1

// seedIterationRawAt 生产形态落库：created_at 由调用方给**原样字符串**
//（legacy = CURRENT_TIMESTAMP 的 UTC 裸串；canonical = Go 写的 RFC3339 带偏移）。
func seedIterationRawAtTZ(t *testing.T, db *DB, tenantID int64, turnID uint64, iteration int, rawCreatedAt string) {
	t.Helper()
	if _, err := db.Conn().Exec(
		`INSERT INTO iteration_history (message_id, tenant_id, turn_id, iteration, content, created_at)
		 VALUES (0, ?, ?, ?, 'iter-content', ?)`,
		tenantID, turnID, iteration, rawCreatedAt); err != nil {
		t.Fatalf("seed iteration_history (raw): %v", err)
	}
}

// TestIterationCreatedAt_LegacyBareIsUTC：iteration_history 的老裸串由
// `CURRENT_TIMESTAMP` 写出，**就是 UTC**。按 local(+08:00) 猜 = 早 8 小时 ⇒
// 压缩时间线整体错位。三条读路径（ByTurns/ByTurn/Anchors）必须完全一致。
// 本断言与宿主时区无关（期望值全用 UTC instant 构造）。
func TestIterationCreatedAt_LegacyBareIsUTC(t *testing.T) {
	db, svc, tenantID := newUsageTestDB(t, "chat-iter-tz-legacy")
	// 生产实测：turn 152 本地 17:10:31 = UTC 裸 "2026-10-14 09:10:31"。
	seedIterationRawAtTZ(t, db, tenantID, 152, 1, "2026-10-14 09:10:31")
	// 驱动字符串扫描可能改写成 "T"+"Z" 形态 —— 同一只读语义（UTC）。
	seedIterationRawAtTZ(t, db, tenantID, 152, 2, "2026-10-14T09:20:00Z")

	want1 := time.Date(2026, 10, 14, 9, 10, 31, 0, time.UTC)
	want2 := time.Date(2026, 10, 14, 9, 20, 0, 0, time.UTC)

	byTurns, err := svc.GetIterationHistoryByTurns(tenantID, []uint64{152})
	if err != nil {
		t.Fatalf("GetIterationHistoryByTurns: %v", err)
	}
	recs := byTurns[152]
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}
	if !recs[0].CreatedAt.Equal(want1) {
		t.Fatalf("⛔ 裸 UTC 串被错误解析（旧实现按 time.Local 早 N 小时）: got %v want %v", recs[0].CreatedAt, want1)
	}
	if !recs[1].CreatedAt.Equal(want2) {
		t.Fatalf("⛔ Z 形态 UTC 串被错误解析: got %v want %v", recs[1].CreatedAt, want2)
	}

	byTurn, err := svc.GetIterationHistoryByTurn(tenantID, 152)
	if err != nil {
		t.Fatalf("GetIterationHistoryByTurn: %v", err)
	}
	if len(byTurn) != 2 || !byTurn[0].CreatedAt.Equal(want1) || !byTurn[1].CreatedAt.Equal(want2) {
		t.Fatalf("ByTurn 与 ByTurns 必须 byte 级一致: %+v", byTurn)
	}

	anchors, err := svc.GetIterationAnchorsByTurn(tenantID, 152)
	if err != nil {
		t.Fatalf("GetIterationAnchorsByTurn: %v", err)
	}
	if len(anchors) != 2 || !anchors[0].CreatedAt.Equal(want1) || !anchors[1].CreatedAt.Equal(want2) {
		t.Fatalf("Anchors 与 ByTurns 必须 byte 级一致: %+v", anchors)
	}

	byNumber, ok, err := svc.GetIterationHistoryByNumber(tenantID, 152, 1)
	if err != nil || !ok {
		t.Fatalf("GetIterationHistoryByNumber: ok=%v err=%v", ok, err)
	}
	if !byNumber.CreatedAt.Equal(want1) {
		t.Fatalf("ByNumber 与 ByTurns 必须 byte 级一致: got %v want %v", byNumber.CreatedAt, want1)
	}
}

// TestIterationCreatedAt_CanonicalWriteAndRoundTrip：写侧必须显式写带偏移的
// canonical created_at（RFC3339 系，含 'T' 与显式时区）—— 不再依赖
// CURRENT_TIMESTAMP 默认。回读 instant 必须就是落库时的真实时刻。
// 首条断言（stored 串的形态）在任何宿主时区下都能抓出"写侧仍依赖 DDL 默认"的回归。
func TestIterationCreatedAt_CanonicalWriteAndRoundTrip(t *testing.T) {
	db, svc, tenantID := newUsageTestDB(t, "chat-iter-tz-write")
	nowA := time.Now()
	if err := svc.AppendIterationHistory(tenantID, 0, 152, IterationRecord{
		TurnID: 152, Iteration: 1, Content: "c1",
	}); err != nil {
		t.Fatalf("AppendIterationHistory: %v", err)
	}
	nowB := time.Now()

	var stored string
	if err := db.Conn().QueryRow(
		`SELECT created_at FROM iteration_history WHERE tenant_id = ? AND turn_id = 152 AND iteration = 1`,
		tenantID).Scan(&stored); err != nil {
		t.Fatalf("read stored created_at: %v", err)
	}
	if !strings.Contains(stored, "T") || !(strings.HasSuffix(stored, "Z") || strings.Contains(stored, "+")) {
		t.Fatalf("⛔ 写侧必须显式写带时区偏移的 canonical created_at（不得依赖 CURRENT_TIMESTAMP 默认）: got %q", stored)
	}

	recs, err := svc.GetIterationHistoryByTurn(tenantID, 152)
	if err != nil || len(recs) != 1 {
		t.Fatalf("GetIterationHistoryByTurn: len=%d err=%v", len(recs), err)
	}
	got := recs[0].CreatedAt
	if got.Before(nowA.Add(-2*time.Minute)) || got.After(nowB.Add(2*time.Minute)) {
		t.Fatalf("⛔ 回读 instant 必须等于落库真实时刻（旧形态按 local 猜会偏 N 小时）: got %v, wrote between %v..%v", got, nowA, nowB)
	}
}

// TestIterationCreatedAt_GarbageFailsSafe：无法识别的时间格式绝不 panic、
// 绝不猜 —— 零值处理（下游 buildTurnSpans 跳过 / compactionIteration 回落
// standalone 标记行，信息不丢）。同 DB 的合法行不受牵连。
func TestIterationCreatedAt_GarbageFailsSafe(t *testing.T) {
	db, svc, tenantID := newUsageTestDB(t, "chat-iter-tz-garbage")
	seedIterationRawAtTZ(t, db, tenantID, 7, 1, "not-a-timestamp")
	seedIterationRawAtTZ(t, db, tenantID, 7, 2, "2026-10-14 09:10:31") // 合法的 legacy 行

	recs, err := svc.GetIterationHistoryByTurn(tenantID, 7)
	if err != nil || len(recs) != 2 {
		t.Fatalf("GetIterationHistoryByTurn: len=%d err=%v", len(recs), err)
	}
	if !recs[0].CreatedAt.IsZero() {
		t.Fatalf("⛔ 无法识别的时间必须 fail-safe 为零值（绝不猜）: got %v", recs[0].CreatedAt)
	}
	if !recs[1].CreatedAt.Equal(time.Date(2026, 10, 14, 9, 10, 31, 0, time.UTC)) {
		t.Fatalf("合法的 legacy 行必须照常解析: got %v", recs[1].CreatedAt)
	}
}

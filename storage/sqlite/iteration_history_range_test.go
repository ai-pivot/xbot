package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件守护 v2 会话库新增的两条读路径 + 复合索引迁移：
//   - GetIterationHistoryBeforeRange：区域段取回（`iteration < ?` 升序）
//   - GetIterationHistoryByNumber：详情单查（(turn_id, iteration) 定位）
//   - idx_iter_history_turn_iter：新库建表 + 既有库打开（幂等重放 DDL）两条路径
//
// 测试库一律走 openSessionDBForTest（真实会话库 schema —— 每会话一 DB 铁律：
// 查询必须经会话库连接，绝不碰主库的旧 session_messages/iteration_history）。

// iterationIndexName 复合索引名（唯一权威常量，测试与 DDL 用同一个字符串）。
const iterationIndexName = "idx_iter_history_turn_iter"

// openSessionDBForTest 打开一个临时**会话库**并返回 (db, svc)。
func openSessionDBForTest(t *testing.T) (*DB, *SessionService) {
	t.Helper()
	db, err := OpenSessionDB(filepath.Join(t.TempDir(), "session.db"))
	if err != nil {
		t.Fatalf("open session db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, NewSessionService(db)
}

// iterationFixture 构造一条**全字段非零**的迭代记录。字段值全部由 iteration
// 派生 —— 「字段完整往返」用例据此逐字段比对（任何一列在 SELECT/scan 里被漏掉
// 或错位都会立刻显形）。
//
// MessageID：AppendIterationHistory 忽略 rec.MessageID、用形参 msgID 写
// message_id 列（agent/engine_run_tools.go:589 传 0 是生产实况）——
// 这里让 fixture 的 MessageID 与 seed 传入的 msgID 一致，往返才有判别力。
func iterationFixture(turnID uint64, iteration int) IterationRecord {
	return IterationRecord{
		MessageID:      int64(1000 + iteration),
		TurnID:         turnID,
		Iteration:      iteration,
		Content:        fmt.Sprintf("content-%d", iteration),
		Reasoning:      fmt.Sprintf("reasoning-%d", iteration),
		Tools:          fmt.Sprintf(`[{"name":"Shell","call_id":"c-%d"}]`, iteration),
		Tokens:         int64(10 + iteration),
		TTFTMs:         int64(200 + iteration),
		TokensPerSec:   int64(30 + iteration),
		TotalMs:        int64(400 + iteration),
		TPOTMs:         int64(50 + iteration),
		InputTokens:    int64(60 + iteration),
		CachedTokens:   int64(70 + iteration),
		Model:          fmt.Sprintf("model-%d", iteration),
		SubscriptionID: fmt.Sprintf("sub-%d", iteration),
	}
}

// seedIteration 写入一条 fixture 记录。
func seedIteration(t *testing.T, svc *SessionService, tenantID int64, turnID uint64, iteration int) IterationRecord {
	t.Helper()
	rec := iterationFixture(turnID, iteration)
	if err := svc.AppendIterationHistory(tenantID, rec.MessageID, turnID, rec); err != nil {
		t.Fatalf("seed iteration (tenant=%d turn=%d iter=%d): %v", tenantID, turnID, iteration, err)
	}
	return rec
}

// iterationNumbers 抽取迭代号序列（顺带断言「升序」这一存储层契约）。
func iterationNumbers(t *testing.T, recs []IterationRecord) []int {
	t.Helper()
	out := make([]int, 0, len(recs))
	for i, r := range recs {
		if i > 0 && r.Iteration <= recs[i-1].Iteration {
			t.Fatalf("GetIterationHistoryBeforeRange must return ascending iterations, got %d after %d",
				r.Iteration, recs[i-1].Iteration)
		}
		out = append(out, r.Iteration)
	}
	return out
}

func rangeInts(from, to int) []int {
	out := make([]int, 0, to-from+1)
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestGetIterationHistoryBeforeRange 表驱动：范围语义 + 边界排他 + 升序 +
// tenant/turn 隔离。
//
// 判别力（mutation 自证）：
//   - 若把 `iteration < ?` 写成 `iteration <= ?`（或 `<=` 边界回归），
//     "boundaryExclusive" 与 "partialRange" 用例立刻红（多出一个 beforeIter）。
//   - 若 WHERE 漏掉 `turn_id = ?`，decoys（同 tenant 的 turn=8）会混进结果，
//     "partialRange" 的精确序列比对立刻红。
//   - 若 WHERE 漏掉 `tenant_id = ?`，另一 tenant 同 turn_id 的 1..5 会重复混入，
//     所有用例都红（长度/序列不符）。
//   - 若 ORDER BY iteration ASC 被去掉或反向，iterationNumbers 的升序断言红。
func TestGetIterationHistoryBeforeRange(t *testing.T) {
	_, svc := openSessionDBForTest(t)

	const (
		tenantMain  = int64(1)
		tenantOther = int64(2)
		turnMain    = uint64(7)
		turnOther   = uint64(8)
	)
	for i := 1; i <= 66; i++ {
		seedIteration(t, svc, tenantMain, turnMain, i)
	}
	// Decoys — 隔离用例的判别力来源（同 tenant 的另一 turn / 另一 tenant 的同号 turn）。
	for i := 1; i <= 5; i++ {
		seedIteration(t, svc, tenantMain, turnOther, i)
		seedIteration(t, svc, tenantOther, turnMain, i)
	}

	cases := []struct {
		name       string
		tenantID   int64
		turnID     uint64
		beforeIter int
		want       []int
		wantEmpty  bool
	}{
		{
			// 无更早迭代：beforeIter=1（turn 的第一个迭代）⇒ 空结果、非错误。
			name: "emptyWhenNoEarlierIterations", tenantID: tenantMain, turnID: turnMain,
			beforeIter: 1, wantEmpty: true,
		},
		{
			name: "singleEarlierIteration", tenantID: tenantMain, turnID: turnMain,
			beforeIter: 2, want: []int{1},
		},
		{
			// 部分区间：区域窗口最小号 = 52 ⇒ 取回 1..51（不含 52 自己）。
			name: "partialRangeExcludesBoundary", tenantID: tenantMain, turnID: turnMain,
			beforeIter: 52, want: rangeInts(1, 51),
		},
		{
			// 边界排他性（末迭代）：beforeIter=66 ⇒ 1..65，66 自身绝不下发。
			name: "boundaryExclusiveAtLastIteration", tenantID: tenantMain, turnID: turnMain,
			beforeIter: 66, want: rangeInts(1, 65),
		},
		{
			// beforeIter 超出该 turn 的最大迭代号 ⇒ 全量（仍升序、无重复）。
			name: "beyondMaxReturnsAll", tenantID: tenantMain, turnID: turnMain,
			beforeIter: 100, want: rangeInts(1, 66),
		},
		{
			// 隔离专项（另一 tenant 的同号 turn，只有 1..5）：
			// 漏 tenant_id 过滤会看到 6 行（1,1,2,2,3,3...）⇒ 必红。
			name: "tenantIsolation", tenantID: tenantOther, turnID: turnMain,
			beforeIter: 4, want: []int{1, 2, 3},
		},
		{
			// 隔离专项（同 tenant 的另一 turn，只有 1..5）：
			// 漏 turn_id 过滤会看到 5..66 混合 ⇒ 必红。
			name: "turnIsolation", tenantID: tenantMain, turnID: turnOther,
			beforeIter: 100, want: []int{1, 2, 3, 4, 5},
		},
		{
			// 完全不存在的 (tenant, turn) ⇒ 空结果、非错误。
			name: "unknownTurnIsEmpty", tenantID: tenantMain, turnID: uint64(999),
			beforeIter: 100, wantEmpty: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.GetIterationHistoryBeforeRange(tc.tenantID, tc.turnID, tc.beforeIter)
			if err != nil {
				t.Fatalf("GetIterationHistoryBeforeRange: %v", err)
			}
			nums := iterationNumbers(t, got)
			if tc.wantEmpty {
				if len(nums) != 0 {
					t.Fatalf("want empty, got %v", nums)
				}
				return
			}
			if !equalInts(nums, tc.want) {
				t.Fatalf("iterations mismatch\n got: %v\nwant: %v", nums, tc.want)
			}
			// 边界排他性显式断言（独立于 want 列表，防止 want 被误改掩盖回归）。
			for _, n := range nums {
				if n == tc.beforeIter {
					t.Fatalf("beforeIter=%d must be EXCLUSIVE but appeared in result %v", tc.beforeIter, nums)
				}
				if n > tc.beforeIter {
					t.Fatalf("iteration %d >= beforeIter %d leaked into result %v", n, tc.beforeIter, nums)
				}
			}
		})
	}
}

// TestGetIterationHistoryByNumber 单条定位：命中（全字段往返）/ 未命中
// （bool=false 且 err=nil —— 「不存在」不是错误，上层据此转 404）。
//
// 判别力（mutation 自证）：
//   - 若未命中路径返回 error 而非 (zero, false, nil)，"missingDoesNotError" 必红。
//   - 若 WHERE 漏 tenant_id 或 turn_id（只按 iteration 查），隔离用例必红
//     （会命中另一 tenant/turn 的同号迭代）。
//   - 若 SELECT 列清单漏列/错位（如漏 COALESCE(created_at, ”)），
//     fullFieldRoundTrip 的字段比对或 Scan 必红。
func TestGetIterationHistoryByNumber(t *testing.T) {
	_, svc := openSessionDBForTest(t)

	const (
		tenantA = int64(11)
		tenantB = int64(22)
		turnA   = uint64(5)
		turnB   = uint64(6)
	)
	for i := 1; i <= 3; i++ {
		seedIteration(t, svc, tenantA, turnA, i)
		seedIteration(t, svc, tenantA, turnB, i)
		seedIteration(t, svc, tenantB, turnA, i)
	}

	t.Run("hitFullFieldRoundTrip", func(t *testing.T) {
		before := time.Now().Add(-time.Minute)
		got, ok, err := svc.GetIterationHistoryByNumber(tenantA, turnA, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatal("expected ok=true for a seeded (tenant, turn, iteration)")
		}
		want := iterationFixture(turnA, 2)
		if got.MessageID != want.MessageID {
			t.Errorf("MessageID: got %d want %d", got.MessageID, want.MessageID)
		}
		if got.TurnID != want.TurnID {
			t.Errorf("TurnID: got %d want %d", got.TurnID, want.TurnID)
		}
		if got.Iteration != want.Iteration {
			t.Errorf("Iteration: got %d want %d", got.Iteration, want.Iteration)
		}
		if got.Content != want.Content {
			t.Errorf("Content: got %q want %q", got.Content, want.Content)
		}
		if got.Reasoning != want.Reasoning {
			t.Errorf("Reasoning: got %q want %q", got.Reasoning, want.Reasoning)
		}
		if got.Tools != want.Tools {
			t.Errorf("Tools: got %q want %q", got.Tools, want.Tools)
		}
		if got.Tokens != want.Tokens {
			t.Errorf("Tokens: got %d want %d", got.Tokens, want.Tokens)
		}
		if got.TTFTMs != want.TTFTMs {
			t.Errorf("TTFTMs: got %d want %d", got.TTFTMs, want.TTFTMs)
		}
		if got.TokensPerSec != want.TokensPerSec {
			t.Errorf("TokensPerSec: got %d want %d", got.TokensPerSec, want.TokensPerSec)
		}
		if got.TotalMs != want.TotalMs {
			t.Errorf("TotalMs: got %d want %d", got.TotalMs, want.TotalMs)
		}
		if got.TPOTMs != want.TPOTMs {
			t.Errorf("TPOTMs: got %d want %d", got.TPOTMs, want.TPOTMs)
		}
		if got.InputTokens != want.InputTokens {
			t.Errorf("InputTokens: got %d want %d", got.InputTokens, want.InputTokens)
		}
		if got.CachedTokens != want.CachedTokens {
			t.Errorf("CachedTokens: got %d want %d", got.CachedTokens, want.CachedTokens)
		}
		if got.Model != want.Model {
			t.Errorf("Model: got %q want %q", got.Model, want.Model)
		}
		if got.SubscriptionID != want.SubscriptionID {
			t.Errorf("SubscriptionID: got %q want %q", got.SubscriptionID, want.SubscriptionID)
		}
		// created_at：由 DB CURRENT_TIMESTAMP 填充（本地 wall-clock），必须是
		// 真实落库时间而非零值 —— 漏 COALESCE(created_at,'') 时这里也会连带显形。
		if got.CreatedAt.IsZero() {
			t.Error("CreatedAt is zero — created_at was not round-tripped")
		}
		if got.CreatedAt.Before(before) || got.CreatedAt.After(time.Now().Add(time.Minute)) {
			t.Errorf("CreatedAt %v outside the seeding window (>= %v)", got.CreatedAt, before)
		}
	})

	misses := []struct {
		name          string
		tenantID      int64
		turnID        uint64
		iteration     int
		whatIsMissing string
	}{
		{"missingIteration", tenantA, turnA, 99, "iteration never written"},
		{"missingTurn", tenantA, uint64(999), 2, "turn never written for this tenant"},
		{"missingTenant", int64(999), turnA, 2, "tenant never written"},
		{"tenantIsolation", tenantB, turnB, 2, "iteration 2 exists for (tenantA,turnA) and (tenantB,turnA) and (tenantA,turnB) but NOT (tenantB,turnB)"},
	}
	for _, tc := range misses {
		t.Run("miss_"+tc.name, func(t *testing.T) {
			got, ok, err := svc.GetIterationHistoryByNumber(tc.tenantID, tc.turnID, tc.iteration)
			// 未命中必须是「正常结果」：false + nil error（上层转 404）。
			if err != nil {
				t.Fatalf("miss (%s) must not return an error, got %v", tc.whatIsMissing, err)
			}
			if ok {
				t.Fatalf("miss (%s) must return ok=false, got true with %+v", tc.whatIsMissing, got)
			}
			if got != (IterationRecord{}) {
				t.Fatalf("miss (%s) must return the zero record, got %+v", tc.whatIsMissing, got)
			}
		})
	}
}

// legacySessionSchemaNoTurnIterIndex 是「v2 之前」的会话库 DDL（真实老库形态）：
// 两张表 + 旧索引 + schema_version=1，**没有** idx_iter_history_turn_iter。
const legacySessionSchemaNoTurnIterIndex = `
CREATE TABLE IF NOT EXISTS session_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    tool_call_id TEXT,
    tool_name TEXT,
    tool_arguments TEXT,
    tool_calls TEXT,
    detail TEXT,
    reasoning_content TEXT DEFAULT '',
    reasoning_items TEXT DEFAULT '',
    display_only INTEGER DEFAULT 0,
    internal_only INTEGER DEFAULT 0,
    context_tokens INTEGER DEFAULT 0,
    turn_id INTEGER DEFAULT 0,
    record_type TEXT NOT NULL DEFAULT 'message',
    target_history_id INTEGER,
    record_data TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_session_messages_tenant_created ON session_messages(tenant_id, created_at);
CREATE INDEX IF NOT EXISTS idx_session_messages_tenant_history ON session_messages(tenant_id, id);
CREATE TABLE IF NOT EXISTS iteration_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id INTEGER NOT NULL DEFAULT 0,
    tenant_id INTEGER NOT NULL,
    turn_id INTEGER NOT NULL DEFAULT 0,
    iteration INTEGER NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    reasoning TEXT NOT NULL DEFAULT '',
    tools TEXT NOT NULL DEFAULT '[]',
    tokens INTEGER NOT NULL DEFAULT 0,
    ttft_ms INTEGER NOT NULL DEFAULT 0,
    tokens_per_sec INTEGER NOT NULL DEFAULT 0,
    total_ms INTEGER NOT NULL DEFAULT 0,
    tpot_ms INTEGER NOT NULL DEFAULT 0,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    model TEXT NOT NULL DEFAULT '',
    subscription_id TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_iter_history_msg ON iteration_history(message_id);
CREATE INDEX IF NOT EXISTS idx_iter_history_turn ON iteration_history(tenant_id, turn_id);
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY
);
INSERT INTO schema_version (version) VALUES (1);
`

// TestIterationHistoryTurnIterIndexFreshAndLegacy 索引迁移的两条路径：
//
//	路径 1（新库）：OpenSessionDB 首次建库，DDL 直接带索引。
//	路径 2（既有库）：用 v2 之前的 DDL 造一个老库（无索引、version=1），
//	  再经 OpenSessionDB 打开 —— initSessionSchema 重放整份幂等 DDL
//	  （CREATE INDEX IF NOT EXISTS）把索引补上、版本推进到 sessionSchemaVersion。
//
// 判别力（mutation 自证）：
//   - 若索引只加在「新建分支」而 OpenSessionDB 对既有库不重放 DDL（或漏写
//     IF NOT EXISTS 导致第二次打开报错），legacy 用例必红。
//   - 若 sessionSchemaVersion 没随 DDL 变更推进，版本断言必红。
//   - 若版本表回退成 INSERT OR REPLACE（升级后留下 {1,2} 两行），
//     "version is a single authoritative row" 断言必红。
func TestIterationHistoryTurnIterIndexFreshAndLegacy(t *testing.T) {
	assertIndex := func(t *testing.T, q interface {
		QueryRow(query string, args ...any) *sql.Row
	}, stage string) {
		t.Helper()
		var ddl string
		if err := q.QueryRow(
			"SELECT sql FROM sqlite_master WHERE type='index' AND name=?", iterationIndexName,
		).Scan(&ddl); err != nil {
			t.Fatalf("%s: index %s missing: %v", stage, iterationIndexName, err)
		}
		// 列序即契约（复合索引的等值列在前、范围列在后）。
		if !strings.Contains(ddl, "iteration_history(tenant_id, turn_id, iteration)") {
			t.Errorf("%s: %s DDL unexpected: %s", stage, iterationIndexName, ddl)
		}
	}
	assertIndexAbsent := func(t *testing.T, q interface {
		QueryRow(query string, args ...any) *sql.Row
	}, stage string) {
		t.Helper()
		var name string
		err := q.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='index' AND name=?", iterationIndexName,
		).Scan(&name)
		if err != sql.ErrNoRows {
			t.Fatalf("%s: expected %s to be absent in the legacy fixture, err=%v", stage, iterationIndexName, err)
		}
	}

	t.Run("freshSessionDB", func(t *testing.T) {
		db, _ := openSessionDBForTest(t)
		assertIndex(t, db.Conn(), "fresh schema")
		assertSingleSchemaVersion(t, db, "fresh schema")
	})

	t.Run("legacySessionDBMigratesOnOpen", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "legacy-session.db")
		raw, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatalf("open raw legacy db: %v", err)
		}
		if _, err := raw.Exec(legacySessionSchemaNoTurnIterIndex); err != nil {
			raw.Close()
			t.Fatalf("seed legacy session schema: %v", err)
		}
		// 前置断言：老库确实没有索引（否则本用例无判别力）。
		assertIndexAbsent(t, raw, "legacy fixture")
		var v0 int
		if err := raw.QueryRow("SELECT COUNT(*) FROM schema_version WHERE version = 1").Scan(&v0); err != nil || v0 != 1 {
			raw.Close()
			t.Fatalf("legacy fixture must pin version=1 (got count=%d err=%v)", v0, err)
		}
		_ = raw.Close()

		// 既有库打开（= 生产里 TenantSession 首次/下次打开会话库的收口点）。
		db, err := OpenSessionDB(path)
		if err != nil {
			t.Fatalf("OpenSessionDB on legacy file: %v", err)
		}
		assertIndex(t, db.Conn(), "legacy migration (open #1)")
		assertSingleSchemaVersion(t, db, "legacy migration (open #1)")
		_ = db.Close()

		// 幂等：再开一次不报错、索引仍恰好一份。
		db2, err := OpenSessionDB(path)
		if err != nil {
			t.Fatalf("OpenSessionDB reopen: %v", err)
		}
		defer db2.Close()
		assertIndex(t, db2.Conn(), "reopen #2 (idempotency)")
		assertSingleSchemaVersion(t, db2, "reopen #2 (idempotency)")
		var n int
		if err := db2.Conn().QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", iterationIndexName,
		).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("index must exist exactly once after repeated opens, got %d", n)
		}
	})
}

// assertSingleSchemaVersion 断言会话库版本表是「单行 = sessionSchemaVersion」。
func assertSingleSchemaVersion(t *testing.T, db *DB, stage string) {
	t.Helper()
	var rows, version int
	if err := db.Conn().QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&rows); err != nil {
		t.Fatalf("%s: count schema_version rows: %v", stage, err)
	}
	if rows != 1 {
		t.Fatalf("%s: schema_version must be a single authoritative row, got %d rows", stage, rows)
	}
	if err := db.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("%s: read schema version: %v", stage, err)
	}
	if version != sessionSchemaVersion {
		t.Fatalf("%s: session schema version = %d, want %d", stage, version, sessionSchemaVersion)
	}
}

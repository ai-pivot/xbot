package sqlite

// sessiondb_lookup_indexes_test.go — 会话库补齐 v80 三条历史流回溯索引的守护测试。
//
// 背景（G-12 未翻绿的真正原因）：v71/v72 拆库后主库 session_messages 基本是
// 0 行，真实消息全在各会话库（sessions/<channel>/<bucket>/<name>.db）。v80 只在
// 主库建 idx_sm_markers / idx_sm_display_msg / idx_sm_turn_user，等于没建 ——
// /api/history 的回溯查询仍在会话库上全表扫（实测 p95 550ms；会话库补建后
// 240ms，反向 DROP 回到 550ms）。
//
// 覆盖三个不变量：
//  1. 新建会话库：PRAGMA index_list(session_messages) 含三条索引；
//  2. 幂等：再次 OpenSessionDB 不报错，三条索引仍各恰好一份；
//  3. 既有库回填：v5 形态（无这三条索引、已有数据）的会话库打开即补齐，
//     且原有数据不丢 —— 这是生产升级真正命中的路径。
//
// 判别力（mutation 自证）：把 initSessionSchema 里
// `ensureSessionMessagesLookupIndexes(db.Conn())` 那一行注释掉，三条索引不再
// 由会话库 DDL 创建（sessionSchema 里没有它们），本文件两个用例必红。

import (
	"path/filepath"
	"testing"
)

// sessionDBLookupIndexNames 是 v80 三条会话库回溯索引的名单。
var sessionDBLookupIndexNames = []string{
	"idx_sm_markers",
	"idx_sm_display_msg",
	"idx_sm_turn_user",
}

// assertSessionDBLookupIndexes 断言三条索引存在且各恰好一份。
func assertSessionDBLookupIndexes(t *testing.T, db *DB, stage string) {
	t.Helper()
	names := sessionMessagesIndexNames(t, db.Conn())
	for _, name := range sessionDBLookupIndexNames {
		if !names[name] {
			t.Fatalf("%s: index %s missing from session db (index_list=%v)", stage, name, names)
		}
		var n int
		if err := db.Conn().QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", name,
		).Scan(&n); err != nil {
			t.Fatalf("%s: count index %s: %v", stage, name, err)
		}
		if n != 1 {
			t.Fatalf("%s: index %s must exist exactly once, got %d", stage, name, n)
		}
	}
}

// TestSessionDBLookupIndexesFreshAndIdempotent 新建库即带索引，重复打开幂等。
func TestSessionDBLookupIndexesFreshAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.db")

	db, err := OpenSessionDB(path)
	if err != nil {
		t.Fatalf("OpenSessionDB: %v", err)
	}
	assertSessionDBLookupIndexes(t, db, "fresh open #1")
	assertSingleSchemaVersion(t, db, "fresh open #1")
	if err := db.Close(); err != nil {
		t.Fatalf("close #1: %v", err)
	}

	// 幂等：再开一次不报错、索引仍在且不重复。
	db2, err := OpenSessionDB(path)
	if err != nil {
		t.Fatalf("OpenSessionDB reopen: %v", err)
	}
	defer db2.Close()
	assertSessionDBLookupIndexes(t, db2, "reopen #2 (idempotency)")
	assertSingleSchemaVersion(t, db2, "reopen #2 (idempotency)")
}

// TestSessionDBLookupIndexesBackfilledOnExistingDB 既有会话库（生产升级形态）：
// 缺三条索引、已有数据 —— 打开即补齐，数据不丢。
func TestSessionDBLookupIndexesBackfilledOnExistingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-session.db")

	db, err := OpenSessionDB(path)
	if err != nil {
		t.Fatalf("OpenSessionDB: %v", err)
	}
	if _, err := db.Conn().Exec(
		"INSERT INTO session_messages (tenant_id, role, content) VALUES (7, 'user', 'legacy row')",
	); err != nil {
		db.Close()
		t.Fatalf("seed legacy row: %v", err)
	}
	// 退化成 v5 形态：三条索引不存在（模拟升级前的会话库）。
	for _, name := range sessionDBLookupIndexNames {
		if _, err := db.Conn().Exec("DROP INDEX IF EXISTS " + name); err != nil {
			db.Close()
			t.Fatalf("drop %s: %v", name, err)
		}
	}
	// 前置断言：fixture 确实缺索引，否则本用例无判别力。
	present := sessionMessagesIndexNames(t, db.Conn())
	for _, name := range sessionDBLookupIndexNames {
		if present[name] {
			db.Close()
			t.Fatalf("fixture must not have %s before reopen", name)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}

	db2, err := OpenSessionDB(path)
	if err != nil {
		t.Fatalf("OpenSessionDB on legacy session db: %v", err)
	}
	defer db2.Close()
	assertSessionDBLookupIndexes(t, db2, "legacy backfill on open")
	var content string
	if err := db2.Conn().QueryRow(
		"SELECT content FROM session_messages WHERE tenant_id = 7 LIMIT 1",
	).Scan(&content); err != nil {
		t.Fatalf("legacy row lost after backfill: %v", err)
	}
	if content != "legacy row" {
		t.Fatalf("legacy row corrupted: %q", content)
	}
}

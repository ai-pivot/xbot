package sqlite

// migration_v80_test.go — v79→v80 三条 session_messages 部分索引的守护测试。
//
// 覆盖三个不变量：
//  1. 建得出来：迁移后 PRAGMA index_list(session_messages) 含 idx_sm_markers /
//     idx_sm_display_msg / idx_sm_turn_user，且版本置 80；
//  2. 可重入：同一个迁移重跑一次（以及那次崩溃后由 ensureAdditiveTables 重试）
//     exit=0，不报错、不重复建；
//  3. 不阻断启动（P0 口径，见 cdc7ff8e）：索引 DDL 失败（索引名被占）时，
//     迁移**只告警**、返回 nil、版本照常推进；索引之后由兜底路径补建。

import (
	"database/sql"
	"testing"
)

// sessionMessagesIndexNames 从 PRAGMA index_list(session_messages) 读出全部索引名。
func sessionMessagesIndexNames(t *testing.T, conn *sql.DB) map[string]bool {
	t.Helper()
	rows, err := conn.Query("PRAGMA index_list(session_messages)")
	if err != nil {
		t.Fatalf("PRAGMA index_list: %v", err)
	}
	defer rows.Close()

	// index_list 列：seq | name | unique | origin | partial（不同 SQLite 版本列数
	// 可能多出字段，所以按列名取值而不是按下标硬读）。
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("PRAGMA index_list columns: %v", err)
	}
	names := make(map[string]bool)
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("PRAGMA index_list scan: %v", err)
		}
		for i, c := range cols {
			if c != "name" {
				continue
			}
			switch v := vals[i].(type) {
			case string:
				names[v] = true
			case []byte:
				names[string(v)] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("PRAGMA index_list rows: %v", err)
	}
	return names
}

func requireV80Indexes(t *testing.T, conn *sql.DB) {
	t.Helper()
	names := sessionMessagesIndexNames(t, conn)
	for _, want := range []string{"idx_sm_markers", "idx_sm_display_msg", "idx_sm_turn_user"} {
		if !names[want] {
			t.Fatalf("index %q missing from PRAGMA index_list(session_messages); got %v", want, names)
		}
	}
}

func schemaVersionOf(t *testing.T, conn *sql.DB) int {
	t.Helper()
	var v int
	if err := conn.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&v); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	return v
}

// TestMigrateV79ToV80CreatesLookupIndexes 验证「迁移后 index_list 含三名 + 版本 80」。
func TestMigrateV79ToV80CreatesLookupIndexes(t *testing.T) {
	db := openTestDB(t)
	conn := db.Conn()

	// 新库由 createSchema 直接建在 v80（schema.go 已声明三条索引）——先删掉，
	// 逼出真实的 v79→v80 迁移路径。
	for _, idx := range []string{"idx_sm_markers", "idx_sm_display_msg", "idx_sm_turn_user"} {
		if _, err := conn.Exec("DROP INDEX IF EXISTS " + idx); err != nil {
			t.Fatalf("drop %s: %v", idx, err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 79"); err != nil {
		t.Fatal(err)
	}

	if err := migrateV79ToV80(db); err != nil {
		t.Fatalf("migrateV79ToV80: %v", err)
	}
	requireV80Indexes(t, conn)
	if v := schemaVersionOf(t, conn); v != 80 {
		t.Fatalf("schema_version = %d, want 80", v)
	}
}

// TestMigrateV79ToV80Idempotent 验证重跑幂等（崩溃重启 / 手工重跑都必须 exit=0）。
func TestMigrateV79ToV80Idempotent(t *testing.T) {
	db := openTestDB(t)
	conn := db.Conn()

	if err := migrateV79ToV80(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := migrateV79ToV80(db); err != nil {
		t.Fatalf("second run (must be idempotent): %v", err)
	}
	requireV80Indexes(t, conn)
	if v := schemaVersionOf(t, conn); v != 80 {
		t.Fatalf("schema_version = %d, want 80", v)
	}
	// 兜底路径（每次 Open 都跑）同样必须幂等。
	if err := ensureSessionMessagesLookupIndexes(conn); err != nil {
		t.Fatalf("ensureSessionMessagesLookupIndexes after migration: %v", err)
	}
}

// TestMigrateV79ToV80IndexFailureDoesNotBlock 验证 P0 口径：索引建不出来也不能让
// 迁移（进而让 Open）失败 —— 只告警、版本照常推进，索引留给兜底重试。
func TestMigrateV79ToV80IndexFailureDoesNotBlock(t *testing.T) {
	db := openTestDB(t)
	conn := db.Conn()

	for _, idx := range []string{"idx_sm_markers", "idx_sm_display_msg", "idx_sm_turn_user"} {
		if _, err := conn.Exec("DROP INDEX IF EXISTS " + idx); err != nil {
			t.Fatalf("drop %s: %v", idx, err)
		}
	}
	// 用同名索引占住 idx_sm_markers（挂在另一张表上）⇒ CREATE INDEX IF NOT EXISTS
	// 会报 "index idx_sm_markers already exists"，模拟「索引名被占用 / 只读库」。
	if _, err := conn.Exec("CREATE INDEX idx_sm_markers ON tenants(id)"); err != nil {
		t.Fatalf("occupy index name: %v", err)
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 79"); err != nil {
		t.Fatal(err)
	}

	if err := migrateV79ToV80(db); err != nil {
		t.Fatalf("migrateV79ToV80 must not fail on index DDL error, got: %v", err)
	}
	if v := schemaVersionOf(t, conn); v != 80 {
		t.Fatalf("schema_version = %d, want 80 even when indexes failed", v)
	}

	// 占位索引释放后，兜底路径必须能把三条索引补回来（可恢复）。
	if _, err := conn.Exec("DROP INDEX idx_sm_markers"); err != nil {
		t.Fatalf("drop occupying index: %v", err)
	}
	if err := ensureSessionMessagesLookupIndexes(conn); err != nil {
		t.Fatalf("ensureSessionMessagesLookupIndexes retry: %v", err)
	}
	requireV80Indexes(t, conn)
}

// TestFreshDatabaseHasV80Indexes 新库（createSchema 直建）也必须带三条索引。
func TestFreshDatabaseHasV80Indexes(t *testing.T) {
	db := openTestDB(t)
	requireV80Indexes(t, db.Conn())
	if v := schemaVersionOf(t, db.Conn()); v != 80 {
		t.Fatalf("schema_version = %d, want 80", v)
	}
}

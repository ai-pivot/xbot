package sqlite

// migration_v72_test.go — 删除迁移（deletion migration v72）的守护测试。
//
// 覆盖四类不变量：
//  1. 批量对账：残留者（migrated=0）补齐复制 + 已迁移者/孤儿的主库行删除 +
//     preview 回填 + version=72；
//  2. 幂等：重跑不产生重复、不丢数据；
//  3. 韧性：单个残留者失败只 WARN + 跳过（数据留主库、不阻塞启动、其他租户照常）；
//  4. VACUUM 回收：删除后文件体积收缩 + freelist 清零。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xbot/llm"
)

// setupV71State builds a v71-shaped main DB: version pinned to 71, one
// already-migrated tenant (A), one straggler (B), and legacy main-DB rows for
// both plus an orphan row (tenant destroyed while foreign_keys was OFF).
func setupV71State(t *testing.T) (*DB, int64, int64) {
	t.Helper()
	db := openTestDB(t)
	conn := db.Conn()
	// Pin version to 71 — migrateV71ToV72 is tested directly (same pattern as
	// TestMigrateV69ToV70RepairsMissingReasoningItems).
	if _, err := conn.Exec("UPDATE schema_version SET version = 71"); err != nil {
		t.Fatal(err)
	}
	ts := NewTenantService(db)
	tenantA, err := ts.GetOrCreateTenantID("test", "already-migrated")
	if err != nil {
		t.Fatal(err)
	}
	tenantB, err := ts.GetOrCreateTenantID("test", "straggler")
	if err != nil {
		t.Fatal(err)
	}
	// A is already migrated (the v71 lazy path ran for it) — its main rows are a
	// redundant copy whose authority lives in its session DB. F2 删除守卫要求
	// migrated=1 ⇒ 会话库文件真实存在（真实世界：v71 惰性路径派生 db_path +
	// 复制 + 置标记，三件事总在一起）—— fixture 必须同样把三件事做完。
	dbPathA := SessionDBRelPath("test", "already-migrated")
	if err := ts.SetTenantDBPath(tenantA, dbPathA); err != nil {
		t.Fatal(err)
	}
	if sdb, oerr := OpenSessionDB(filepath.Join(filepath.Dir(db.path), dbPathA)); oerr != nil {
		t.Fatal(oerr)
	} else {
		_ = sdb.Close()
	}
	if err := ts.SetTenantMigrated(tenantA); err != nil {
		t.Fatal(err)
	}
	// Legacy main-DB rows for both + one orphan row. Orphans came from the era
	// when PRAGMA foreign_keys was OFF (pre-v33): simulate it on a pinned conn
	// (FK is per-connection; the pool would otherwise route the INSERT to a
	// different FK-ON connection).
	ss := NewSessionService(db)
	for _, id := range []int64{tenantA, tenantB} {
		if _, err := ss.AppendMessage(id, llm.NewUserMessage("legacy message")); err != nil {
			t.Fatal(err)
		}
	}
	pinned, err := conn.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pinned.ExecContext(context.Background(), "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := pinned.ExecContext(context.Background(),
		"INSERT INTO session_messages (tenant_id, role, content) VALUES (99999, 'user', 'orphan row')"); err != nil {
		t.Fatal(err)
	}
	_, _ = pinned.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
	_ = pinned.Close()
	return db, tenantA, tenantB
}

// TestMigrateV71ToV72BulkCompletesAndDeletes — 批量对账：straggler 复制到其
// 会话库（行数/内容一致 + preview 回填）+ migrated 置位；主库中已迁移者、
// 新补齐者、孤儿的行全部删除；version=72。
func TestMigrateV71ToV72BulkCompletesAndDeletes(t *testing.T) {
	db, tenantA, tenantB := setupV71State(t)
	conn := db.Conn()

	if err := migrateV71ToV72(db); err != nil {
		t.Fatalf("migrateV71ToV72: %v", err)
	}

	// version = 72。
	var version int
	if err := conn.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 72 {
		t.Fatalf("version = %d, want 72", version)
	}

	// Straggler B：migrated=1 + db_path 已分配。
	ts := NewTenantService(db)
	info, err := ts.GetTenantDBInfo(tenantB)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Migrated {
		t.Fatal("straggler not marked migrated")
	}
	if info.DBPath == "" {
		t.Fatal("straggler db_path not assigned")
	}

	// B 的数据已复制到其会话库（行数/内容对账）。
	sdb, err := OpenSessionDB(filepath.Join(filepath.Dir(db.path), info.DBPath))
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	var count int
	var content string
	if err := sdb.Conn().QueryRow(
		"SELECT COUNT(*), COALESCE(MAX(content), '') FROM session_messages WHERE tenant_id = ?", tenantB,
	).Scan(&count, &content); err != nil {
		t.Fatal(err)
	}
	if count != 1 || content != "legacy message" {
		t.Fatalf("straggler session DB = %d rows, content %q — want 1 row 'legacy message'", count, content)
	}

	// preview 回填（跨会话列表读主库 tenants.preview 这一列）。
	var preview string
	if err := conn.QueryRow("SELECT COALESCE(preview, '') FROM tenants WHERE id = ?", tenantB).Scan(&preview); err != nil {
		t.Fatal(err)
	}
	if preview != "legacy message" {
		t.Fatalf("preview = %q, want %q (backfilled from the session DB)", preview, "legacy message")
	}

	// 主库：session_messages / iteration_history 全空（A 已迁移、B 已补齐、孤儿清理）。
	for _, table := range []string{"session_messages", "iteration_history"} {
		var n int
		if err := conn.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("main DB %s still has %d rows after v72 (want 0)", table, n)
		}
	}
	_ = tenantA
}

// TestMigrateV71ToV72IsIdempotent — 重跑安全：无残留者（全部 migrated=1）时
// 第二次运行是 no-op，不产生重复、不丢数据。
func TestMigrateV71ToV72IsIdempotent(t *testing.T) {
	db, _, tenantB := setupV71State(t)
	ts := NewTenantService(db)

	if err := migrateV71ToV72(db); err != nil {
		t.Fatal(err)
	}
	info, err := ts.GetTenantDBInfo(tenantB)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateV71ToV72(db); err != nil {
		t.Fatalf("re-run must be a no-op: %v", err)
	}

	// B 的会话库行数稳定（重跑不重复复制）。
	sdb, err := OpenSessionDB(filepath.Join(filepath.Dir(db.path), info.DBPath))
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	var count int
	if err := sdb.Conn().QueryRow("SELECT COUNT(*) FROM session_messages WHERE tenant_id = ?", tenantB).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("session DB rows after re-run = %d, want 1 (no duplicates)", count)
	}
	// 主库仍空 + version 仍 72。
	var version int
	db.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version)
	if version != 72 {
		t.Fatalf("version = %d, want 72", version)
	}
}

// TestMigrateV72SkipsFailingStragglerWithoutBlocking — 韧性：单个残留者的
// 会话库无法创建（路径被目录占用）时，迁移**不报错**（启动永不阻塞），
// 该租户保持 migrated=0 + 数据完整留在主库（惰性路径下次打开接管）；
// 其余租户照常处理（已迁移者的行照常删除）。
func TestMigrateV72SkipsFailingStragglerWithoutBlocking(t *testing.T) {
	db, tenantA, tenantB := setupV71State(t)
	conn := db.Conn()
	ts := NewTenantService(db)

	// 预占 straggler 的会话库路径：在该 .db 文件位置放一个目录 →
	// OpenSessionDB 建库失败（file is a directory）。路径派生是确定性的。
	abs := filepath.Join(filepath.Dir(db.path), SessionDBRelPath("test", "straggler"))
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatal(err)
	}

	// 迁移必须成功（跳过失败者，绝不阻塞启动）。
	if err := migrateV71ToV72(db); err != nil {
		t.Fatalf("v72 must not block startup on a failing straggler: %v", err)
	}

	// B：保持 migrated=0 + 数据完整留在主库。
	info, err := ts.GetTenantDBInfo(tenantB)
	if err != nil {
		t.Fatal(err)
	}
	if info.Migrated {
		t.Fatal("failed straggler must stay migrated=0 (its data has no session-DB copy yet)")
	}
	var countB int
	if err := conn.QueryRow("SELECT COUNT(*) FROM session_messages WHERE tenant_id = ?", tenantB).Scan(&countB); err != nil {
		t.Fatal(err)
	}
	if countB != 1 {
		t.Fatalf("failed straggler's data must be preserved in the main DB (count=%d, want 1)", countB)
	}

	// A（已迁移者）：行照常删除 —— 单点失败不影响其余租户。
	var countA int
	if err := conn.QueryRow("SELECT COUNT(*) FROM session_messages WHERE tenant_id = ?", tenantA).Scan(&countA); err != nil {
		t.Fatal(err)
	}
	if countA != 0 {
		t.Fatalf("already-migrated tenant's rows were not deleted (countA=%d)", countA)
	}

	// version = 72（迁移整体完成 —— 失败者留给惰性路径，不卡版本推进）。
	var version int
	conn.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version)
	if version != 72 {
		t.Fatalf("version = %d, want 72 (the skip must not stall the chain)", version)
	}
}

// TestMigrateV72VacuumReclaimsSpace — VACUUM 回收：删除 ~3MB 遗留行后主库
// 文件体积显著收缩 + freelist 清零（空间真的还给了文件系统）。
func TestMigrateV72VacuumReclaimsSpace(t *testing.T) {
	db := openTestDB(t)
	conn := db.Conn()
	if _, err := conn.Exec("UPDATE schema_version SET version = 71"); err != nil {
		t.Fatal(err)
	}
	ts := NewTenantService(db)
	tenantA, err := ts.GetOrCreateTenantID("test", "big-tenant")
	if err != nil {
		t.Fatal(err)
	}
	// F2 删除守卫：migrated=1 ⇒ 会话库文件必须真实存在（同 setupV71State 的三件套）。
	dbPathA := SessionDBRelPath("test", "big-tenant")
	if err := ts.SetTenantDBPath(tenantA, dbPathA); err != nil {
		t.Fatal(err)
	}
	if sdb, oerr := OpenSessionDB(filepath.Join(filepath.Dir(db.path), dbPathA)); oerr != nil {
		t.Fatal(oerr)
	} else {
		_ = sdb.Close()
	}
	if err := ts.SetTenantMigrated(tenantA); err != nil {
		t.Fatal(err)
	}
	// ~3MB 遗留行。
	payload := strings.Repeat("x", 10*1024)
	for i := 0; i < 300; i++ {
		if _, err := conn.Exec(
			"INSERT INTO session_messages (tenant_id, role, content) VALUES (?, 'user', ?)", tenantA, payload,
		); err != nil {
			t.Fatal(err)
		}
	}
	// checkpoint WAL 进主文件，让体积对比有意义。
	if _, err := conn.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	sizeBefore := fileSize(t, db.path)

	if err := migrateV71ToV72(db); err != nil {
		t.Fatal(err)
	}

	sizeAfter := fileSize(t, db.path)
	if sizeAfter >= sizeBefore/2 {
		t.Fatalf("VACUUM did not reclaim: before=%d after=%d (want after < before/2)", sizeBefore, sizeAfter)
	}
	var freelist int
	if err := conn.QueryRow("PRAGMA freelist_count").Scan(&freelist); err != nil {
		t.Fatal(err)
	}
	if freelist != 0 {
		t.Fatalf("freelist_count = %d after VACUUM, want 0", freelist)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// TestHasLegacyRows — 迁移决策探针（2026-09-30 生产事故修复的判别基础）：
// 全新租户两表皆空 ⇒ false（不迁移）；任一表有行 ⇒ true（必须复制）。
func TestHasLegacyRows(t *testing.T) {
	db := openTestDB(t)
	ts := NewTenantService(db)
	ss := NewSessionService(db)
	tenantID, err := ts.GetOrCreateTenantID("test", "probe-rows")
	if err != nil {
		t.Fatal(err)
	}

	// 全新租户：两表皆空 ⇒ false。
	got, err := ss.HasLegacyRows(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("brand-new tenant must report no legacy rows (no migration needed)")
	}

	// 仅 session_messages 有行 ⇒ true。
	if _, err := ss.AppendMessage(tenantID, llm.NewUserMessage("legacy")); err != nil {
		t.Fatal(err)
	}
	if got, err = ss.HasLegacyRows(tenantID); err != nil || !got {
		t.Fatalf("legacy session_messages row: got (%v, %v), want (true, nil)", got, err)
	}

	// 隔离到另一个只有 iteration_history 的租户 ⇒ 也必须 true。
	tenantIH, err := ts.GetOrCreateTenantID("test", "probe-iter-only")
	if err != nil {
		t.Fatal(err)
	}
	if err := ss.AppendIterationHistory(tenantIH, 0, 7, IterationRecord{TurnID: 7, Iteration: 1, Content: "x"}); err != nil {
		t.Fatal(err)
	}
	if got, err = ss.HasLegacyRows(tenantIH); err != nil || !got {
		t.Fatalf("legacy iteration_history row: got (%v, %v), want (true, nil)", got, err)
	}
}

package sqlite

import (
	"path/filepath"
	"testing"
)

// 已有库（版本号已是当前版本）缺一张「后来才加入 schema」的表时，打开库必须自动补上。
//
// 真实事故：oauth_tokens 曾由 oauth 包惰性建表 —— 从未启用过 OAuth 的库就没有这张表。
// 改成后端无关的 token 存储后没人建它，写入直接报 `no such table: oauth_tokens`。
// createSchema 只在建库时跑、迁移链只在版本变化时跑，两者都覆盖不到这个组合，
// 所以 ensureAdditiveTables 必须在两条路径的收尾处都执行。
//
// 判别力：去掉 ensureAdditiveTables 的调用（或只保留 createSchema 里那一份），
// 本测试在「重建后表存在」处必红。
func TestOpen_ExistingDBGainsAdditiveTables(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "xbot.db")

	// 1. 建库（createSchema 会带出 oauth_tokens）。
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// 2. 删掉它，模拟「这个库当年从未启用过 OAuth」的形态。
	if _, err := db.Conn().Exec("DROP TABLE IF EXISTS oauth_tokens"); err != nil {
		t.Fatalf("DROP oauth_tokens: %v", err)
	}
	if ok, err := tableExists(db.Conn(), "oauth_tokens"); err != nil || ok {
		t.Fatalf("setup failed: oauth_tokens still exists after DROP (err=%v)", err)
	}
	// 版本号保持当前值 —— 关键前提：这不是「需要跑迁移链」的库。
	var version int
	if err := db.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("setup failed: schema version = %d, want the current %d (this test is about same-version DBs)",
			version, schemaVersion)
	}
	db.Close()

	// 3. 重新打开：走的是「已有库 + 版本相同」分支。
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()

	if ok, err := tableExists(db2.Conn(), "oauth_tokens"); err != nil {
		t.Fatalf("check oauth_tokens: %v", err)
	} else if !ok {
		t.Fatal("reopening a same-version database did not recreate the additive table oauth_tokens — " +
			"ensureAdditiveTables is not wired into the same-version path")
	}
	// 表必须真的可用，不只是存在于 sqlite_master。
	if _, err := db2.Conn().Exec(
		`INSERT INTO oauth_tokens (provider, channel, chat_id, access_token, expires_at, updated_at)
		 VALUES ('p', 'c', 'x', 'a', 0, 0)`); err != nil {
		t.Fatalf("additive table exists but is not usable: %v", err)
	}
}

// 迁移链走完后也必须补齐（老库从低版本升上来，同样可能缺这张表）。
// 这里直接验证 migrateSchema 的收尾：把版本改低再打开，走迁移链。
func TestOpen_MigrationPathAlsoAddsAdditiveTables(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "xbot.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.Conn().Exec("DROP TABLE IF EXISTS oauth_tokens"); err != nil {
		t.Fatalf("DROP oauth_tokens: %v", err)
	}
	// 把版本号改低，强制下一次 Open 走迁移链（v72 → v73 是最后一步）。
	if _, err := db.Conn().Exec("UPDATE schema_version SET version = 72"); err != nil {
		t.Fatalf("lower schema_version: %v", err)
	}
	db.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen (migration path): %v", err)
	}
	defer db2.Close()

	var version int
	if err := db2.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("migration did not reach the current version: %d", version)
	}
	if ok, err := tableExists(db2.Conn(), "oauth_tokens"); err != nil {
		t.Fatalf("check oauth_tokens: %v", err)
	} else if !ok {
		t.Fatal("the migration path did not add the additive table — " +
			"ensureAdditiveTables is not wired into migrateSchema's tail")
	}
}

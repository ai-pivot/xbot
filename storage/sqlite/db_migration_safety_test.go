package sqlite

import (
	"os"
	"path/filepath"
	"testing"
)

// 2026-09-16 用户要求：「我主要是想防止清除已有数据」，同时 **「一定不能让正常情况无法启动」**。
//
// 契约：
//
//	① 需要迁移（version < schemaVersion）时，**尽力**先留整库备份
//	   <db>.pre-v<from>.bak（失败只告警，照常迁移、照常启动）；
//	② 备份按 from-version **确定性命名 + 已存在即跳过**：迁移重试 / 崩溃重启只有一份，
//	   不会反复复制整库（用户 2026-09-16：「你不能迁移一次备份一次呀…太浪费空间了」）；
//	③ DB 版本比二进制新 ⇒ **不阻塞启动**，且不跑任何迁移（既有数据原样不动）。
func TestOpen_BacksUpExistingDBBeforeMigration(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "xbot.db")

	db, err := Open(p)
	if err != nil {
		t.Fatalf("fresh open: %v", err)
	}
	// 模拟「既有旧库需要迁移」：把记录版本改小一格（v66 < v67 ⇒ 必然触发一次迁移）
	if _, err := db.Conn().Exec("UPDATE schema_version SET version = ?", schemaVersion-1); err != nil {
		t.Fatalf("set old version: %v", err)
	}
	db.Close()

	db2, err := Open(p) // 迁移路径：应先留备份，再迁移
	if err != nil {
		t.Fatalf("需要迁移的既有库必须能正常启动: %v", err)
	}
	db2.Close()

	matches, _ := filepath.Glob(p + ".pre-v*.bak")
	if len(matches) == 0 {
		t.Fatal("迁移前必须尽力留下整库备份（用户要求：防止清除已有数据）")
	}
	fi, statErr := os.Stat(matches[0])
	if statErr != nil || fi.Size() == 0 {
		t.Fatalf("备份必须存在且非空: err=%v", statErr)
	}
}

// 空间契约（用户 2026-09-16）：「你不能迁移一次备份一次呀，你如果这么做的话大太多了
// 太浪费空间了」——备份必须确定性命名，重复迁移不得产生第二份整库副本。
func TestOpen_MigrationBackupIsCreatedAtMostOnce(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "xbot.db")

	// 反复制造「需要迁移」的状态并重开：模拟迁移重试 / 崩溃重启
	for round := 0; round < 3; round++ {
		db, err := Open(p)
		if err != nil {
			t.Fatalf("round %d open: %v", round, err)
		}
		if _, err := db.Conn().Exec("UPDATE schema_version SET version = ?", schemaVersion-1); err != nil {
			t.Fatalf("round %d set old version: %v", round, err)
		}
		db.Close()
	}

	matches, _ := filepath.Glob(p + ".pre-v*.bak")
	if len(matches) != 1 {
		t.Fatalf("每个 from-version 只能有一份迁移备份，否则 2.5GB 的库会反复复制占满磁盘；实际 %d 份: %v", len(matches), matches)
	}
}

func TestOpen_NewerSchemaDoesNotBlockStartup(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "xbot.db")

	db, err := Open(p)
	if err != nil {
		t.Fatalf("fresh open: %v", err)
	}
	newer := schemaVersion + 5
	if _, err := db.Conn().Exec("UPDATE schema_version SET version = ?", newer); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	db.Close()

	db2, err := Open(p)
	if err != nil {
		t.Fatalf("旧二进制配新库也必须能启动（只告警，绝不崩溃循环）: %v", err)
	}
	defer db2.Close()

	var v int
	if err := db2.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&v); err != nil {
		t.Fatalf("query version: %v", err)
	}
	if v != newer {
		t.Fatalf("不得跑任何迁移去改动既有数据: want version=%d got=%d", newer, v)
	}
}

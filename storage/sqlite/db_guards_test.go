package sqlite

import (
	"os"
	"path/filepath"
	"testing"
)

// 2026-09-16 用户要求：数据安全「防止清除已有 db 数据」+ **「一定不能让正常情况无法启动」**。
//
// 设计铁律：xbot-server 由 supervisor 托管，Open() 一旦返回错误就是「启动失败 → 自动重启
// → 再失败」的崩溃循环。所以守卫只做**告警与取证**，绝不阻塞启动。本文件钉死该契约：
//
//	① 既有 0 字节文件（SQLite 首次写入前的正常形态，也是测试里建临时库的常见做法）
//	   ⇒ 照常启动并正常建表；
//	② 既有非 SQLite 文件 ⇒ 绝不被改写/截断（是否报错由驱动决定）；
//	③ 库文件缺失但同级有备份 ⇒ 照常启动（不自动恢复、不阻塞），备份原样保留；
//	④ 0 字节 + 同级有备份 ⇒ 照常启动（只告警）。
func TestOpen_ZeroByteExistingFileStartsNormally(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "xbot.db")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := Open(p)
	if err != nil {
		t.Fatalf("0 字节既有文件是「新库」的正常形态，必须能正常启动: %v", err)
	}
	defer db.Close()

	var v int
	if err := db.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&v); err != nil {
		t.Fatalf("应已建好 schema: %v", err)
	}
	if fi, statErr := os.Stat(p); statErr != nil || fi.Size() == 0 {
		t.Fatalf("打开后库文件应已写入 SQLite 头: err=%v", statErr)
	}
}

func TestOpen_NonSQLiteFileIsNeverModified(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "not-a-db.db")
	content := []byte("this file is definitely not a sqlite database and it must survive intact")
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatal(err)
	}

	if db, err := Open(p); err == nil {
		db.Close() // 能否打开交给驱动；关键在下面的字节比对
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("文件必须仍然存在: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("非 SQLite 文件绝不能被改写/截断\nwant=%q\ngot =%q", content, got)
	}
}

func TestOpen_MissingDBWithBackupStillStarts(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "xbot.db")
	backup := p + ".bak-20260101-000000"
	backupContent := []byte("pretend this is an old database")
	if err := os.WriteFile(backup, backupContent, 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := Open(p) // 有备份也只告警，绝不阻塞启动
	if err != nil {
		t.Fatalf("有备份也不能阻塞启动（supervisor 会崩溃循环）: %v", err)
	}
	db.Close()

	got, err := os.ReadFile(backup)
	if err != nil || string(got) != string(backupContent) {
		t.Fatalf("备份文件必须原样保留: err=%v", err)
	}
}

func TestSiblingDBBackups_DetectsBackups(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "xbot.db")
	for _, name := range []string{
		"xbot.db.bak-20260101",
		"xbot.db.pre-v66-20260101.bak",
		"xbot.db.recover-1",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := siblingDBBackups(p); len(got) != 3 {
		t.Fatalf("应识别出 3 个同级备份，实际 %d: %v", len(got), got)
	}
	if got := siblingDBBackups(filepath.Join(dir, "other.db")); len(got) != 0 {
		t.Fatalf("其他库名的备份不得被误判: %v", got)
	}
}

package sqlite

// user_chats 查询索引的回归测试（2026-10-30，G-22）。
//
// 背景：生产库（v79）一直有 idx_user_chats_channel_chat，仓库内的建表语句
// 却从未声明过它 —— 新库/重建库缺索引，/api/session-tree 的 label/sort_order
// 查询退化成逐租户行扫表。本测试钉住两件事：
//
//	① 全新库（createSchema 路径）必须带这条索引；
//	② 已存在的库若缺这条索引（同版本漂移），下一次 Open 自动补回，且重复
//	   Open 不报错。

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

const userChatsChannelChatIndex = "idx_user_chats_channel_chat"

func hasUserChatsChannelChatIndex(t *testing.T, conn *sql.DB) bool {
	t.Helper()
	var name string
	err := conn.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, userChatsChannelChatIndex,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("lookup index: %v", err)
	}
	return true
}

func TestUserChatsChannelChatIndexOnFreshSchema(t *testing.T) {
	db := openTestDB(t)
	if !hasUserChatsChannelChatIndex(t, db.Conn()) {
		t.Fatalf("fresh schema lacks %s", userChatsChannelChatIndex)
	}
}

func TestUserChatsChannelChatIndexRepairedOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drift.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	// 模拟生产式漂移：库已存在，索引缺失。
	if _, err := db.Conn().Exec(`DROP INDEX ` + userChatsChannelChatIndex); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 第二次 Open：走 ensureAdditiveTables 的同版本补齐路径。
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open (repair): %v", err)
	}
	if !hasUserChatsChannelChatIndex(t, db2.Conn()) {
		t.Fatalf("%s not repaired on reopen", userChatsChannelChatIndex)
	}
	if err := db2.Close(); err != nil {
		t.Fatalf("close2: %v", err)
	}

	// 第三次 Open：索引已在，重复执行幂等、不报错。
	db3, err := Open(path)
	if err != nil {
		t.Fatalf("third Open (idempotent): %v", err)
	}
	defer db3.Close()
	if !hasUserChatsChannelChatIndex(t, db3.Conn()) {
		t.Fatalf("%s disappeared on third Open", userChatsChannelChatIndex)
	}
}

// TestUserChatsChannelChatIndexReadOnlyDBStillOpens 钉住 P0 回归（2026-10-30，
// 2070457b）：索引只是优化，**建索引失败绝不许阻断 Open**。
//
// 场景＝只读库（文件 0444）且缺 idx_user_chats_channel_chat：CREATE INDEX 报
// "attempt to write a readonly database"，回归版本把该错误从 ensureAdditiveTables
// 上抛 ⇒ 「能打开库」这个前提被打掉。修复后要求：Open 成功、句柄可读、索引缺席
// 只是降级（label 子查询变慢），与 schema.go:530-551 的索引兜底同约定。
func TestUserChatsChannelChatIndexReadOnlyDBStillOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readonly.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	// 模拟「只读副本 + 缺索引」：生产式漂移在只读库上无法被自动修复。
	if _, err := db.Conn().Exec(`DROP INDEX ` + userChatsChannelChatIndex); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}
	// Close 已 checkpoint；清掉 WAL 残留再切只读，确保只读连接读到的是落盘内容。
	for _, sfx := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + sfx); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s: %v", path+sfx, err)
		}
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("chmod read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	roDB, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a read-only database must not fail — index creation is an optimization, not an availability precondition: %v", err)
	}
	defer roDB.Close()

	// 句柄可用：能真正读到表数据。
	var tables int
	if err := roDB.Conn().QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='user_chats'`,
	).Scan(&tables); err != nil {
		t.Fatalf("read-only handle is unusable: %v", err)
	}
	if tables != 1 {
		t.Fatalf("user_chats table missing on read-only database (count=%d)", tables)
	}

	// 索引缺席是允许的降级结果 —— 关键是「不报错」。
	rows, err := roDB.Conn().Query(`PRAGMA index_list(user_chats)`)
	if err != nil {
		t.Fatalf("PRAGMA index_list on read-only handle: %v", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("index_list columns: %v", err)
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan index_list: %v", err)
		}
		for _, v := range vals {
			if b, ok := v.([]byte); ok && string(b) == userChatsChannelChatIndex {
				t.Fatalf("%s must not exist on a read-only database; the failed CREATE INDEX must degrade to a warning", userChatsChannelChatIndex)
			}
			if s, ok := v.(string); ok && s == userChatsChannelChatIndex {
				t.Fatalf("%s must not exist on a read-only database; the failed CREATE INDEX must degrade to a warning", userChatsChannelChatIndex)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("index_list rows: %v", err)
	}
}

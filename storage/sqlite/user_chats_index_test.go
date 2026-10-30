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

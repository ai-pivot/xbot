package sqlite

import (
	"strings"
	"testing"
	"time"
)

// seedChatWithPreview inserts a tenant + user_chats label + a tenants.preview
// value for the ListUserChats preview tests.
//
// v71（每会话一个 DB）：preview 不再由 ListUserChats 的 session_messages 子查询
// 计算（拆库后主库没有消息数据 —— 消息在每会话独立库里，跨库 JOIN 不可能），
// 而是读 tenants.preview 列（写入路径维护：TenantSession 的 append 钩子 /
// 迁移回填 / rewind/clear 重算）。本 helper 直接种 preview 列 —— 模拟写入路径
// 的产物；display_only 过滤 / substr(256) / latest-wins 语义由 session 层的
// 写入路径测试守护（session/sessiondb_test.go），这里只测读路径。
func seedChatWithPreview(t *testing.T, db *DB, channel, senderID, chatID, label, preview string) int64 {
	t.Helper()
	conn := db.Conn()
	var tenantID int64
	if err := conn.QueryRow(
		"INSERT INTO tenants (channel, chat_id, preview) VALUES (?, ?, ?) RETURNING id",
		channel, chatID, preview,
	).Scan(&tenantID); err != nil {
		t.Fatalf("seed tenant %s: %v", chatID, err)
	}
	if _, err := conn.Exec(
		"INSERT INTO user_chats (channel, sender_id, chat_id, label, created_at) VALUES (?, ?, ?, ?, ?)",
		channel, senderID, chatID, label, time.Now().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seed user_chats %s: %v", chatID, err)
	}
	return tenantID
}

// TestListUserChatsPreviewReadsTenantsColumn (m1) verifies ListUserChats reads
// the tenants.preview column (v71: the write path maintains it — the old
// session_messages subquery is gone because messages live in per-session DBs).
func TestListUserChatsPreviewReadsTenantsColumn(t *testing.T) {
	db := openTestDB(t)
	svc := NewChatService(db)

	seedChatWithPreview(t, db, "web", "u1", "/w/preview-1", "Chat1", "real user message")

	chats, _, err := svc.ListUserChats("web", "u1", "cur", 0, 50)
	if err != nil {
		t.Fatalf("ListUserChats: %v", err)
	}
	if len(chats) != 2 {
		t.Fatalf("expected 2 chats (default + /w/preview-1), got %d", len(chats))
	}
	var preview1 string
	for _, c := range chats {
		if c.ChatID == "/w/preview-1" {
			preview1 = c.Preview
		}
	}
	if preview1 != "real user message" {
		t.Errorf("preview = %q, want %q (tenants.preview column)", preview1, "real user message")
	}
}

// TestListUserChatsPreviewTruncatedTo80Runes (m2) verifies the Go-side 80-rune
// clip still applies to the preview column value (the write path stores up to
// 256 bytes; the read path clips to 80 runes for the sidebar).
func TestListUserChatsPreviewTruncatedTo80Runes(t *testing.T) {
	db := openTestDB(t)
	svc := NewChatService(db)

	// 10,000 chars (way past both the 256-byte write bound and the 80-rune
	// read clip) — simulating what the write path would have stored.
	long := strings.Repeat("a", 10000)
	seedChatWithPreview(t, db, "web", "u2", "/w/preview-2", "Chat2", long)

	chats, _, err := svc.ListUserChats("web", "u2", "cur", 0, 50)
	if err != nil {
		t.Fatalf("ListUserChats: %v", err)
	}
	if len(chats) != 2 {
		t.Fatalf("expected 2 chats (default + /w/preview-2), got %d", len(chats))
	}
	var preview2 string
	for _, c := range chats {
		if c.ChatID == "/w/preview-2" {
			preview2 = c.Preview
		}
	}
	// The Go-side clip is 80 runes; the stored value must be bounded on read.
	if len([]rune(preview2)) != 80 {
		t.Errorf("preview rune length = %d, want 80", len([]rune(preview2)))
	}
}

// TestListUserChatsPreviewEmptyForFreshSession verifies a session with no
// preview (fresh, no messages yet) renders an empty preview, not an error.
func TestListUserChatsPreviewEmptyForFreshSession(t *testing.T) {
	db := openTestDB(t)
	svc := NewChatService(db)

	seedChatWithPreview(t, db, "web", "u3", "/w/preview-3", "Chat3", "")

	chats, _, err := svc.ListUserChats("web", "u3", "cur", 0, 50)
	if err != nil {
		t.Fatalf("ListUserChats: %v", err)
	}
	if len(chats) != 2 {
		t.Fatalf("expected 2 chats (default + /w/preview-3), got %d", len(chats))
	}
	var preview3 string
	for _, c := range chats {
		if c.ChatID == "/w/preview-3" {
			preview3 = c.Preview
		}
	}
	if preview3 != "" {
		t.Errorf("preview = %q, want empty for a fresh session", preview3)
	}
}

// TestSetTenantPreviewTruncatesTo256Bytes verifies the write-path bound: the
// preview column stores at most 256 bytes (substr in SQL — same bound the old
// ListUserChats subquery applied). The read path clips to 80 runes on top.
func TestSetTenantPreviewTruncatesTo256Bytes(t *testing.T) {
	db := openTestDB(t)
	ts := NewTenantService(db)
	tenantID, err := ts.GetOrCreateTenantID("web", "/w/preview-write")
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("b", 10000)
	if err := ts.SetTenantPreview(tenantID, long); err != nil {
		t.Fatalf("SetTenantPreview: %v", err)
	}
	var stored string
	if err := db.Conn().QueryRow("SELECT preview FROM tenants WHERE id = ?", tenantID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 256 {
		t.Errorf("stored preview length = %d, want 256 (SQL substr bound)", len(stored))
	}
}

// TestTruncateSmallMaxRunes (m7) verifies truncate never panics on
// maxRunes < 4 — the old `runes[:maxRunes-3]` sliced a negative index.
func TestTruncateSmallMaxRunes(t *testing.T) {
	cases := []int{0, 1, 2, 3}
	for _, n := range cases {
		got := truncate("hello world", n)
		if got != "hello world" {
			t.Errorf("truncate(_, %d) = %q, want the input unchanged (no negative-index panic, nothing to clip)", n, got)
		}
	}
}

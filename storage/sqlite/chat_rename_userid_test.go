package sqlite

import (
	"testing"
)

// seedChatOwnerIdentity creates users(88) + user_identities('web','u-m5'→88).
func seedChatOwnerIdentity(t *testing.T, db *DB) {
	t.Helper()
	if _, err := db.Conn().Exec(
		"INSERT INTO users (id, display_name, role) VALUES (88, 'm5owner', 'user')",
	); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := db.Conn().Exec(
		"INSERT INTO user_identities (user_id, channel, channel_user_id) VALUES (88, 'web', 'u-m5')",
	); err != nil {
		t.Fatalf("seed user_identities: %v", err)
	}
}

// TestRenameChatWritesUserID (m5) verifies the RenameChat upsert stamps
// user_chats.user_id: the INSERT path (rename of a chat with no user_chats
// row — CLI/feishu sessions renamed from Web) must carry the canonical user,
// and the UPDATE path over a legacy user_id=0 row must backfill it.
// user_id=0 rows are invisible to user-scoped queries (ListByUserID etc.).
func TestRenameChatWritesUserID(t *testing.T) {
	db := openTestDB(t)
	svc := NewChatService(db)
	seedChatOwnerIdentity(t, db)

	// INSERT path: no user_chats row exists for this chat yet.
	if err := svc.RenameChat("web", "u-m5", "/w/ren-1", "New Label"); err != nil {
		t.Fatalf("RenameChat (insert path): %v", err)
	}
	var uid int64
	var label string
	if err := db.Conn().QueryRow(
		"SELECT user_id, label FROM user_chats WHERE channel = ? AND sender_id = ? AND chat_id = ?",
		"web", "u-m5", "/w/ren-1",
	).Scan(&uid, &label); err != nil {
		t.Fatalf("read back user_chats (insert path): %v", err)
	}
	if label != "New Label" {
		t.Errorf("label = %q, want %q", label, "New Label")
	}
	if uid != 88 {
		t.Errorf("INSERT path user_id = %d, want 88 (canonical user) — the upsert must stamp user_id", uid)
	}

	// UPDATE path over a legacy user_id=0 row (pre-canonical backfill target).
	if _, err := db.Conn().Exec(
		"INSERT INTO user_chats (channel, sender_id, chat_id, label, user_id) VALUES (?, ?, ?, ?, 0)",
		"web", "u-m5", "/w/ren-2", "Old",
	); err != nil {
		t.Fatalf("seed legacy user_id=0 row: %v", err)
	}
	if err := svc.RenameChat("web", "u-m5", "/w/ren-2", "Renamed"); err != nil {
		t.Fatalf("RenameChat (update path): %v", err)
	}
	if err := db.Conn().QueryRow(
		"SELECT user_id, label FROM user_chats WHERE channel = ? AND sender_id = ? AND chat_id = ?",
		"web", "u-m5", "/w/ren-2",
	).Scan(&uid, &label); err != nil {
		t.Fatalf("read back user_chats (update path): %v", err)
	}
	if label != "Renamed" {
		t.Errorf("label = %q, want %q", label, "Renamed")
	}
	if uid != 88 {
		t.Errorf("UPDATE path user_id = %d, want 88 (legacy user_id=0 row must be backfilled)", uid)
	}
}

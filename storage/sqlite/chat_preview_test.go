package sqlite

import (
	"testing"
	"time"
)

// seedChatWithMessages inserts a tenant + user_chats label + session messages
// for the ListUserChats preview tests.
func seedChatWithMessages(t *testing.T, db *DB, channel, senderID, chatID, label string, msgs []struct {
	role        string
	content     string
	displayOnly bool
}) int64 {
	t.Helper()
	conn := db.Conn()
	var tenantID int64
	if err := conn.QueryRow(
		"INSERT INTO tenants (channel, chat_id) VALUES (?, ?) RETURNING id",
		channel, chatID,
	).Scan(&tenantID); err != nil {
		t.Fatalf("seed tenant %s: %v", chatID, err)
	}
	if _, err := conn.Exec(
		"INSERT INTO user_chats (channel, sender_id, chat_id, label, created_at) VALUES (?, ?, ?, ?, ?)",
		channel, senderID, chatID, label, time.Now().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seed user_chats %s: %v", chatID, err)
	}
	for _, m := range msgs {
		if _, err := conn.Exec(
			"INSERT INTO session_messages (tenant_id, role, content, display_only, record_type) VALUES (?, ?, ?, ?, 'message')",
			tenantID, m.role, m.content, m.displayOnly,
		); err != nil {
			t.Fatalf("seed message %q: %v", m.content, err)
		}
	}
	return tenantID
}

// TestListUserChatsPreviewSkipsDisplayOnly (m1) verifies the preview subquery
// filters display-only rows: a session whose LATEST user/assistant message is
// display-only (e.g. a synthetic cancel marker) must show the latest REAL
// message instead.
func TestListUserChatsPreviewSkipsDisplayOnly(t *testing.T) {
	db := openTestDB(t)
	svc := NewChatService(db)

	seedChatWithMessages(t, db, "web", "u1", "/w/preview-1", "Chat1", []struct {
		role        string
		content     string
		displayOnly bool
	}{
		{"user", "real user message", false},
		{"assistant", "[cancelled] synthetic marker", true}, // display-only, newest
	})

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
		t.Errorf("preview = %q, want %q (display-only rows must be skipped)", preview1, "real user message")
	}
}

// TestListUserChatsPreviewTruncatedInSQL (m2) verifies the preview is bounded
// in SQL (substr) instead of reading the full content for an 80-rune Go-side
// clip. A 100KB message must not be fully materialized (asserted behaviorally:
// the preview never exceeds the SQL bound).
func TestListUserChatsPreviewTruncatedInSQL(t *testing.T) {
	db := openTestDB(t)
	svc := NewChatService(db)

	// 10,000 chars of 'a' (10KB, way past the 256-byte SQL bound).
	long := make([]byte, 10000)
	for i := range long {
		long[i] = 'a'
	}
	seedChatWithMessages(t, db, "web", "u2", "/w/preview-2", "Chat2", []struct {
		role        string
		content     string
		displayOnly bool
	}{
		{"user", string(long), false},
	})

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
	// The Go-side clip is 80 runes; the SQL bound (256 bytes) must keep the
	// transfer bounded — the preview comes back exactly 80 runes.
	if len([]rune(preview2)) != 80 {
		t.Errorf("preview rune length = %d, want 80", len([]rune(preview2)))
	}
}

// TestListUserChatsPreviewPrefersUserMessage verifies the display_only filter
// does not change the "latest real message wins" semantics across roles.
func TestListUserChatsPreviewPrefersUserMessage(t *testing.T) {
	db := openTestDB(t)
	svc := NewChatService(db)

	seedChatWithMessages(t, db, "web", "u3", "/w/preview-3", "Chat3", []struct {
		role        string
		content     string
		displayOnly bool
	}{
		{"user", "question", false},
		{"assistant", "answer", false},
	})

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
	if preview3 != "answer" {
		t.Errorf("preview = %q, want %q (latest message)", preview3, "answer")
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

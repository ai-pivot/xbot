package sqlite

import (
	"testing"
	"time"

	"xbot/event"
)

// TestDeleteChatCleansAssociatedData verifies that DeleteChat removes rows that
// have no FK cascade from the tenant (M2): iteration_history, cron_jobs,
// event_triggers, pending_resumes. Previously only user_chats + tenants were
// deleted — a deleted chat's cron job kept firing (GetOrCreateSession
// resurrected the tenant) and its iteration history leaked.
func TestDeleteChatCleansAssociatedData(t *testing.T) {
	db := openTestDB(t)
	conn := db.Conn()

	const (
		channel  = "web"
		chatID   = "/w/chat-del-test"
		senderID = "web-1"
	)

	// Seed: tenant + user_chats label + one session message + one iteration
	// history row + one cron job + one event trigger + one pending resume.
	var tenantID int64
	if err := conn.QueryRow(
		"INSERT INTO tenants (channel, chat_id) VALUES (?, ?) RETURNING id",
		channel, chatID,
	).Scan(&tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if _, err := conn.Exec(
		"INSERT INTO user_chats (channel, sender_id, chat_id, label) VALUES (?, ?, ?, 'l')",
		channel, senderID, chatID,
	); err != nil {
		t.Fatalf("seed user_chats: %v", err)
	}
	if _, err := conn.Exec(
		"INSERT INTO session_messages (tenant_id, role, content, record_type) VALUES (?, 'user', 'm', 'message')",
		tenantID,
	); err != nil {
		t.Fatalf("seed session_messages: %v", err)
	}
	if _, err := conn.Exec(
		"INSERT INTO iteration_history (message_id, tenant_id, turn_id, iteration, content) VALUES (0, ?, 1, 1, 'c')",
		tenantID,
	); err != nil {
		t.Fatalf("seed iteration_history: %v", err)
	}

	cronSvc := NewCronService(db)
	now := time.Now()
	if err := cronSvc.AddJob(&CronJob{
		ID: "job_del_test", Message: "m", Channel: channel, ChatID: chatID,
		SenderID: senderID, EverySeconds: 60, CreatedAt: now, NextRun: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("seed cron job: %v", err)
	}
	trigSvc := NewTriggerService(db)
	if err := trigSvc.AddTrigger(&event.Trigger{
		ID: "trg_del_test", EventType: "webhook", Channel: channel, ChatID: chatID,
		SenderID: senderID, MessageTpl: "tpl", Enabled: true, CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed event trigger: %v", err)
	}
	if err := db.AddPendingResume(channel, chatID, senderID); err != nil {
		t.Fatalf("seed pending resume: %v", err)
	}

	chatSvc := NewChatService(db)
	if err := chatSvc.DeleteChat(channel, senderID, chatID); err != nil {
		t.Fatalf("DeleteChat: %v", err)
	}

	assertZero := func(label, query string, args ...any) {
		t.Helper()
		var n int
		if err := conn.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", label, err)
		}
		if n != 0 {
			t.Errorf("%s not cleaned by DeleteChat: %d row(s) remain", label, n)
		}
	}
	assertZero("iteration_history", "SELECT COUNT(*) FROM iteration_history WHERE tenant_id = ?", tenantID)
	assertZero("cron_jobs", "SELECT COUNT(*) FROM cron_jobs WHERE channel = ? AND chat_id = ?", channel, chatID)
	assertZero("event_triggers", "SELECT COUNT(*) FROM event_triggers WHERE channel = ? AND chat_id = ?", channel, chatID)
	assertZero("pending_resumes", "SELECT COUNT(*) FROM pending_resumes WHERE channel = ? AND chat_id = ?", channel, chatID)
	assertZero("tenants", "SELECT COUNT(*) FROM tenants WHERE channel = ? AND chat_id = ?", channel, chatID)
	assertZero("user_chats", "SELECT COUNT(*) FROM user_chats WHERE channel = ? AND chat_id = ?", channel, chatID)

	// FK cascade check: session_messages follow the tenant.
	assertZero("session_messages", "SELECT COUNT(*) FROM session_messages WHERE tenant_id = ?", tenantID)
}

// TestDeleteChatMissingChatNotFound verifies DeleteChat keeps returning
// ErrChatNotFound when neither user_chats nor tenants has the row.
func TestDeleteChatMissingChatNotFound(t *testing.T) {
	db := openTestDB(t)
	svc := NewChatService(db)
	if err := svc.DeleteChat("web", "u", "/nope"); err == nil {
		t.Fatal("expected ErrChatNotFound for missing chat")
	}
}

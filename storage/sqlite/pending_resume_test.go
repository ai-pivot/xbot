package sqlite

import (
	"testing"
	"time"

	"xbot/llm"
)

func TestPendingResume_CRUD(t *testing.T) {
	db := openTestDB(t)

	// Initially empty
	list, err := db.ListPendingResumes()
	if err != nil {
		t.Fatalf("ListPendingResumes: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d", len(list))
	}

	// Add a pending resume
	if err := db.AddPendingResume("web", "chat-1", "web-1", "hello world"); err != nil {
		t.Fatalf("AddPendingResume: %v", err)
	}

	// List should have 1 entry
	list, err = db.ListPendingResumes()
	if err != nil {
		t.Fatalf("ListPendingResumes: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(list))
	}
	if list[0].Channel != "web" || list[0].ChatID != "chat-1" || list[0].Content != "hello world" || list[0].SenderID != "web-1" {
		t.Fatalf("unexpected entry: %+v", list[0])
	}

	// Upsert (same key) replaces
	if err := db.AddPendingResume("web", "chat-1", "web-1", "updated content"); err != nil {
		t.Fatalf("AddPendingResume upsert: %v", err)
	}
	list, err = db.ListPendingResumes()
	if err != nil {
		t.Fatalf("ListPendingResumes: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 entry after upsert, got %d", len(list))
	}
	if list[0].Content != "updated content" {
		t.Fatalf("expected updated content, got %q", list[0].Content)
	}

	// Clear single
	if err := db.ClearPendingResume("web", "chat-1"); err != nil {
		t.Fatalf("ClearPendingResume: %v", err)
	}
	list, err = db.ListPendingResumes()
	if err != nil {
		t.Fatalf("ListPendingResumes after clear: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty after clear, got %d", len(list))
	}

	// Clear multiple entries individually
	db.AddPendingResume("web", "chat-1", "web-1", "msg1")
	db.AddPendingResume("feishu", "chat-2", "ou_xxx", "msg2")
	if err := db.ClearPendingResume("web", "chat-1"); err != nil {
		t.Fatalf("ClearPendingResume web: %v", err)
	}
	if err := db.ClearPendingResume("feishu", "chat-2"); err != nil {
		t.Fatalf("ClearPendingResume feishu: %v", err)
	}
	list, err = db.ListPendingResumes()
	if err != nil {
		t.Fatalf("ListPendingResumes after clear all: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty after clear all, got %d", len(list))
	}
}

func TestGetLastUserMessage(t *testing.T) {
	db := openTestDB(t)

	// No messages yet
	content, senderID, err := db.GetLastUserMessage("web", "chat-1")
	if err != nil {
		t.Fatalf("GetLastUserMessage on empty: %v", err)
	}
	if content != "" || senderID != "" {
		t.Fatalf("expected empty, got content=%q senderID=%q", content, senderID)
	}

	// Create tenant + user chat + messages
	tenantSvc := NewTenantService(db)
	tenantID, err := tenantSvc.GetOrCreateTenantID("web", "chat-1")
	if err != nil {
		t.Fatalf("GetOrCreateTenantID: %v", err)
	}

	// Add user chat for sender resolution
	conn := db.Conn()
	_, err = conn.Exec(`INSERT OR IGNORE INTO user_chats (channel, sender_id, chat_id) VALUES (?, ?, ?)`,
		"web", "web-1", "chat-1")
	if err != nil {
		t.Fatalf("insert user_chats: %v", err)
	}

	// Insert messages: user, assistant, user (last user is the one we want)
	sessionSvc := NewSessionService(db)
	sessionSvc.AddMessage(tenantID, llm.ChatMessage{Role: "user", Content: "first question", Timestamp: time.Now()})
	sessionSvc.AddMessage(tenantID, llm.ChatMessage{Role: "assistant", Content: "first answer", Timestamp: time.Now()})
	sessionSvc.AddMessage(tenantID, llm.ChatMessage{Role: "user", Content: "second question", Timestamp: time.Now()})

	content, senderID, err = db.GetLastUserMessage("web", "chat-1")
	if err != nil {
		t.Fatalf("GetLastUserMessage: %v", err)
	}
	if content != "second question" {
		t.Fatalf("expected 'second question', got %q", content)
	}
	if senderID != "web-1" {
		t.Fatalf("expected 'web-1', got %q", senderID)
	}
}

func TestHasAssistantReplyAfterLastUser(t *testing.T) {
	db := openTestDB(t)
	tenantSvc := NewTenantService(db)
	tenantID, err := tenantSvc.GetOrCreateTenantID("web", "chat-1")
	if err != nil {
		t.Fatalf("GetOrCreateTenantID: %v", err)
	}
	sessionSvc := NewSessionService(db)

	// No messages: no reply
	hasReply, err := db.HasAssistantReplyAfterLastUser("web", "chat-1")
	if err != nil {
		t.Fatalf("HasAssistantReplyAfterLastUser empty: %v", err)
	}
	if hasReply {
		t.Fatal("expected false on empty")
	}

	// user only, no assistant reply yet
	sessionSvc.AddMessage(tenantID, llm.ChatMessage{Role: "user", Content: "hello", Timestamp: time.Now()})
	hasReply, err = db.HasAssistantReplyAfterLastUser("web", "chat-1")
	if err != nil {
		t.Fatalf("HasAssistantReplyAfterLastUser after user: %v", err)
	}
	if hasReply {
		t.Fatal("expected false: no assistant reply after last user")
	}

	// assistant reply added → should be true
	sessionSvc.AddMessage(tenantID, llm.ChatMessage{Role: "assistant", Content: "hi there", Timestamp: time.Now()})
	hasReply, err = db.HasAssistantReplyAfterLastUser("web", "chat-1")
	if err != nil {
		t.Fatalf("HasAssistantReplyAfterLastUser after reply: %v", err)
	}
	if !hasReply {
		t.Fatal("expected true: assistant reply exists after last user")
	}

	// new user message (turn 2), no reply yet → should be false
	sessionSvc.AddMessage(tenantID, llm.ChatMessage{Role: "user", Content: "again", Timestamp: time.Now()})
	hasReply, err = db.HasAssistantReplyAfterLastUser("web", "chat-1")
	if err != nil {
		t.Fatalf("HasAssistantReplyAfterLastUser turn2: %v", err)
	}
	if hasReply {
		t.Fatal("expected false: turn 2 has no assistant reply yet")
	}
}

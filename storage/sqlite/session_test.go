package sqlite

import (
	"testing"

	"xbot/llm"
)

func TestSessionService_AddMessage(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)

	// Create tenant
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Add messages
	msg1 := llm.NewUserMessage("Hello")
	msg2 := llm.NewAssistantMessage("Hi there")
	msg3 := llm.NewToolMessage("test_tool", "call123", "{}", "Result")

	if err := sessionSvc.AddMessage(tenantID, msg1); err != nil {
		t.Fatalf("Failed to add message 1: %v", err)
	}
	if err := sessionSvc.AddMessage(tenantID, msg2); err != nil {
		t.Fatalf("Failed to add message 2: %v", err)
	}
	if err := sessionSvc.AddMessage(tenantID, msg3); err != nil {
		t.Fatalf("Failed to add message 3: %v", err)
	}

	// Verify count
	count, err := sessionSvc.GetMessagesCount(tenantID)
	if err != nil {
		t.Fatalf("Failed to get messages count: %v", err)
	}
	if count != 3 {
		t.Errorf("Expected 3 messages, got %d", count)
	}
}

func TestSessionService_GetHistory(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)

	// Create tenant
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Add messages
	for i := 0; i < 10; i++ {
		msg := llm.NewUserMessage("Message " + string(rune('0'+i)))
		if err := sessionSvc.AddMessage(tenantID, msg); err != nil {
			t.Fatalf("Failed to add message: %v", err)
		}
	}

	// Get last 5 messages
	history, err := sessionSvc.GetHistory(tenantID, 5)
	if err != nil {
		t.Fatalf("Failed to get history: %v", err)
	}
	if len(history) != 5 {
		t.Errorf("Expected 5 messages in history, got %d", len(history))
	}

	// Get all messages
	allHistory, err := sessionSvc.GetHistory(tenantID, 100)
	if err != nil {
		t.Fatalf("Failed to get all history: %v", err)
	}
	if len(allHistory) != 10 {
		t.Errorf("Expected 10 messages in all history, got %d", len(allHistory))
	}
}

// TestSessionService_GetHistoryBefore_MessageLimit verifies that the limit
// counts raw MESSAGES, not user turns. A single turn commonly holds many
// assistant/tool rows; bounding by message count keeps the paginated payload
// proportional to what the client can render (a turn-count bound allowed a
// single turn with dozens of tool rows to balloon the window to millions of
// tokens).
func TestSessionService_GetHistoryBefore_MessageLimit(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Two turns: turn 1 = 1 user + 2 assistant (3 rows); turn 2 = 1 user + 1
	// assistant (2 rows). Total 5 rows.
	add := func(m llm.ChatMessage) {
		if err := sessionSvc.AddMessage(tenantID, m); err != nil {
			t.Fatalf("Failed to add message: %v", err)
		}
	}
	add(llm.NewUserMessage("u1"))
	add(llm.NewAssistantMessage("a1-1"))
	add(llm.NewAssistantMessage("a1-2"))
	add(llm.NewUserMessage("u2"))
	add(llm.NewAssistantMessage("a2"))

	// limit=3 bounds by message count → the window is the last 3 rows
	// (u2, a2 + the immediately preceding a1-2), NOT the last 3 turns.
	history, err := sessionSvc.GetHistoryBefore(tenantID, 0, 3)
	if err != nil {
		t.Fatalf("Failed to get history: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(history))
	}
	if history[0].Content != "a1-2" || history[1].Content != "u2" || history[2].Content != "a2" {
		t.Fatalf("unexpected window contents: %v", history)
	}

	// beforeID pagination: everything before u2 (exclusive) = u1, a1-1, a1-2.
	all, err := sessionSvc.GetAllMessages(tenantID)
	if err != nil {
		t.Fatalf("Failed to get all messages: %v", err)
	}
	var u2ID int64
	for _, m := range all {
		if m.Content == "u2" {
			u2ID = m.ID
			break
		}
	}
	if u2ID == 0 {
		t.Fatal("u2 message not found")
	}
	older, err := sessionSvc.GetHistoryBefore(tenantID, u2ID, 100)
	if err != nil {
		t.Fatalf("Failed to get older history: %v", err)
	}
	if len(older) != 3 {
		t.Fatalf("expected 3 older messages, got %d", len(older))
	}
	if older[0].Content != "u1" || older[1].Content != "a1-1" || older[2].Content != "a1-2" {
		t.Fatalf("unexpected older window contents: %v", older)
	}
}

func TestSessionService_GetAllMessages(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)

	// Create two tenants
	tenantID1, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant 1: %v", err)
	}
	tenantID2, err := tenantSvc.GetOrCreateTenantID("test", "chat2")
	if err != nil {
		t.Fatalf("Failed to create tenant 2: %v", err)
	}

	// Add messages to tenant 1
	msg1 := llm.NewUserMessage("Tenant 1 message")
	if err := sessionSvc.AddMessage(tenantID1, msg1); err != nil {
		t.Fatalf("Failed to add message to tenant 1: %v", err)
	}

	// Add messages to tenant 2
	msg2 := llm.NewUserMessage("Tenant 2 message")
	if err := sessionSvc.AddMessage(tenantID2, msg2); err != nil {
		t.Fatalf("Failed to add message to tenant 2: %v", err)
	}

	// Get messages for tenant 1
	msgs1, err := sessionSvc.GetAllMessages(tenantID1)
	if err != nil {
		t.Fatalf("Failed to get messages for tenant 1: %v", err)
	}
	if len(msgs1) != 1 {
		t.Errorf("Expected 1 message for tenant 1, got %d", len(msgs1))
	}
	if msgs1[0].Content != "Tenant 1 message" {
		t.Errorf("Expected 'Tenant 1 message', got '%s'", msgs1[0].Content)
	}

	// Get messages for tenant 2
	msgs2, err := sessionSvc.GetAllMessages(tenantID2)
	if err != nil {
		t.Fatalf("Failed to get messages for tenant 2: %v", err)
	}
	if len(msgs2) != 1 {
		t.Errorf("Expected 1 message for tenant 2, got %d", len(msgs2))
	}
	if msgs2[0].Content != "Tenant 2 message" {
		t.Errorf("Expected 'Tenant 2 message', got '%s'", msgs2[0].Content)
	}
}

func TestSessionService_Clear(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)

	// Create tenant
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Add messages
	for i := 0; i < 5; i++ {
		msg := llm.NewUserMessage("Message")
		if err := sessionSvc.AddMessage(tenantID, msg); err != nil {
			t.Fatalf("Failed to add message: %v", err)
		}
	}

	// Verify count
	count, err := sessionSvc.GetMessagesCount(tenantID)
	if err != nil {
		t.Fatalf("Failed to get messages count: %v", err)
	}
	if count != 5 {
		t.Errorf("Expected 5 messages before clear, got %d", count)
	}

	// Clear messages
	if err := sessionSvc.Clear(tenantID); err != nil {
		t.Fatalf("Failed to clear messages: %v", err)
	}

	// Verify count after clear
	count, err = sessionSvc.GetMessagesCount(tenantID)
	if err != nil {
		t.Fatalf("Failed to get messages count after clear: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 messages after clear, got %d", count)
	}
}

func TestSessionService_ToolCalls(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)

	// Create tenant
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Add message with tool calls
	msg := llm.ChatMessage{
		Role:    "assistant",
		Content: "Let me help you",
		ToolCalls: []llm.ToolCall{
			{ID: "call1", Name: "tool1", Arguments: "{\"arg\":\"value\"}"},
			{ID: "call2", Name: "tool2", Arguments: "{}"},
		},
	}

	if err := sessionSvc.AddMessage(tenantID, msg); err != nil {
		t.Fatalf("Failed to add message with tool calls: %v", err)
	}

	// Retrieve and verify
	messages, err := sessionSvc.GetAllMessages(tenantID)
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}
	if len(messages[0].ToolCalls) != 2 {
		t.Errorf("Expected 2 tool calls, got %d", len(messages[0].ToolCalls))
	}
	if messages[0].ToolCalls[0].Name != "tool1" {
		t.Errorf("Expected tool name 'tool1', got '%s'", messages[0].ToolCalls[0].Name)
	}
}

func TestSessionService_ClosedDB_NoPanic(t *testing.T) {
	// Regression test: SessionService methods must return errors (not panic)
	// when the underlying DB connection has been closed.
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)

	// Create tenant while DB is still open
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat_closed")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Close the DB — simulates shutdown while an agent is still running
	db.Close()

	// All write methods must return an error, not panic
	if err := sessionSvc.AddMessage(tenantID, llm.NewUserMessage("hello")); err == nil {
		t.Error("expected error from AddMessage after DB close")
	}
	if err := sessionSvc.ReplaceToolMessage(tenantID, "tool", "id", "content"); err == nil {
		t.Error("expected error from ReplaceToolMessage after DB close")
	}
	if err := sessionSvc.Clear(tenantID); err == nil {
		t.Error("expected error from Clear after DB close")
	}
	if err := sessionSvc.UpdateMessageContent(tenantID, 0, "x"); err == nil {
		t.Error("expected error from UpdateMessageContent after DB close")
	}
	if err := sessionSvc.UpdateMessageContentNonDisplayOnly(tenantID, 0, "x"); err == nil {
		t.Error("expected error from UpdateMessageContentNonDisplayOnly after DB close")
	}
	if err := sessionSvc.UpdateUserMessageContextTokens(tenantID, 100); err == nil {
		t.Error("expected error from UpdateUserMessageContextTokens after DB close")
	}

	// Read methods must also return errors
	if _, err := sessionSvc.GetHistory(tenantID, 10); err == nil {
		t.Error("expected error from GetHistory after DB close")
	}
	if _, err := sessionSvc.GetAllMessages(tenantID); err == nil {
		t.Error("expected error from GetAllMessages after DB close")
	}
	if _, err := sessionSvc.GetMessagesCount(tenantID); err == nil {
		t.Error("expected error from GetMessagesCount after DB close")
	}
	if _, err := sessionSvc.GetUserMessageCount(tenantID); err == nil {
		t.Error("expected error from GetUserMessageCount after DB close")
	}
	if _, err := sessionSvc.GetLastUserMessageContextTokens(tenantID); err == nil {
		t.Error("expected error from GetLastUserMessageContextTokens after DB close")
	}
}

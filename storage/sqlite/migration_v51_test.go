package sqlite

import (
	"encoding/json"
	"testing"

	"xbot/llm"
)

func TestSessionService_GetMaxTurnID(t *testing.T) {
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

	// Empty session → max = 0
	maxID, err := sessionSvc.GetMaxTurnID(tenantID)
	if err != nil {
		t.Fatalf("GetMaxTurnID on empty: %v", err)
	}
	if maxID != 0 {
		t.Errorf("expected 0, got %d", maxID)
	}

	// Add messages with turn_ids: 1, 3, 2
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "hello", TurnID: 1},
		{Role: "assistant", Content: "hi", TurnID: 1},
		{Role: "user", Content: "again", TurnID: 3},
		{Role: "assistant", Content: "response", TurnID: 3},
		{Role: "user", Content: "legacy", TurnID: 0}, // legacy turn
	}
	for _, m := range msgs {
		if err := sessionSvc.AddMessage(tenantID, m); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}

	maxID, err = sessionSvc.GetMaxTurnID(tenantID)
	if err != nil {
		t.Fatalf("GetMaxTurnID: %v", err)
	}
	if maxID != 3 {
		t.Errorf("expected max turn_id=3, got %d", maxID)
	}
}

func TestMigrateV50ToV51_DetailIterationUpgrade(t *testing.T) {
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

	// Insert a message with 0-based Detail JSON (old format)
	oldDetail := `[{"iteration":0,"content":"think0","tools":[{"name":"Read","status":"done"}]},{"iteration":1,"content":"think1","tools":[{"name":"Grep","status":"done"}]}]`
	msg := llm.ChatMessage{
		Role:    "assistant",
		Content: "done",
		Detail:  oldDetail,
		TurnID:  1,
	}
	if err := sessionSvc.AddMessage(tenantID, msg); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// Run the migration
	if err := migrateV50ToV51(db.Conn()); err != nil {
		t.Fatalf("migrateV50ToV51: %v", err)
	}

	// Read back and verify
	messages, err := sessionSvc.GetAllMessages(tenantID)
	if err != nil {
		t.Fatalf("GetAllMessages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}

	var snaps []struct {
		Iteration int `json:"iteration"`
	}
	if err := json.Unmarshal([]byte(messages[0].Detail), &snaps); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("expected 2 iterations, got %d", len(snaps))
	}

	// After migration: iteration should be 1, 2 (1-based)
	if snaps[0].Iteration != 1 {
		t.Errorf("expected iteration 1 (1-based), got %d", snaps[0].Iteration)
	}
	if snaps[1].Iteration != 2 {
		t.Errorf("expected iteration 2 (1-based), got %d", snaps[1].Iteration)
	}
}

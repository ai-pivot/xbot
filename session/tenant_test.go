package session

import (
	"os"
	"path/filepath"
	"testing"

	"xbot/config"
	"xbot/llm"
)

func TestTenantSession_AddMessage(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath)
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	sess, err := mt.GetOrCreateSession("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add various types of messages
	msgs := []llm.ChatMessage{
		llm.NewUserMessage("Hello"),
		llm.NewAssistantMessage("Hi there"),
		llm.NewToolMessage("test_tool", "call123", "{}", "Result"),
		{
			Role:    "assistant",
			Content: "I'll help",
			ToolCalls: []llm.ToolCall{
				{ID: "call1", Name: "tool1", Arguments: "{}"},
			},
		},
	}

	for _, msg := range msgs {
		if err := sess.AddMessage(msg); err != nil {
			t.Errorf("Failed to add message: %v", err)
		}
	}

	// Verify count
	length, err := sess.Len()
	if err != nil {
		t.Fatalf("Failed to get session length: %v", err)
	}
	if length != 4 {
		t.Errorf("Expected 4 messages, got %d", length)
	}
}

func TestTenantSession_GetHistory(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath)
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	sess, err := mt.GetOrCreateSession("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add 10 messages
	for i := 0; i < 10; i++ {
		msg := llm.NewUserMessage("Message")
		if err := sess.AddMessage(msg); err != nil {
			t.Fatalf("Failed to add message: %v", err)
		}
	}

	// Get last 5
	history, err := sess.GetHistory(5)
	if err != nil {
		t.Fatalf("Failed to get history: %v", err)
	}
	if len(history) != 5 {
		t.Errorf("Expected 5 messages, got %d", len(history))
	}

	// Get more than available
	history, err = sess.GetHistory(100)
	if err != nil {
		t.Fatalf("Failed to get all history: %v", err)
	}
	if len(history) != 10 {
		t.Errorf("Expected 10 messages, got %d", len(history))
	}
}

func TestTenantSession_Clear(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath)
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	sess, err := mt.GetOrCreateSession("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add messages
	for i := 0; i < 5; i++ {
		msg := llm.NewUserMessage("Message")
		if err := sess.AddMessage(msg); err != nil {
			t.Fatalf("Failed to add message: %v", err)
		}
	}

	// Clear session
	if err := sess.Clear(); err != nil {
		t.Fatalf("Failed to clear session: %v", err)
	}

	// Verify empty
	length, err := sess.Len()
	if err != nil {
		t.Fatalf("Failed to get session length: %v", err)
	}
	if length != 0 {
		t.Errorf("Expected 0 messages after clear, got %d", length)
	}
}

func TestTenantSession_LastConsolidated(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath)
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	sess, err := mt.GetOrCreateSession("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Initially should be 0
	if lc := sess.LastConsolidated(); lc != 0 {
		t.Errorf("Expected initial lastConsolidated 0, got %d", lc)
	}

	// Set value
	if err := sess.SetLastConsolidated(42); err != nil {
		t.Fatalf("Failed to set last consolidated: %v", err)
	}

	// Verify
	if lc := sess.LastConsolidated(); lc != 42 {
		t.Errorf("Expected lastConsolidated 42, got %d", lc)
	}

	// Update
	if err := sess.SetLastConsolidated(100); err != nil {
		t.Fatalf("Failed to update last consolidated: %v", err)
	}

	if lc := sess.LastConsolidated(); lc != 100 {
		t.Errorf("Expected lastConsolidated 100, got %d", lc)
	}
}

func TestTenantSession_String(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath)
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	sess, err := mt.GetOrCreateSession("feishu", "chat123")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	str := sess.String()
	if str == "" {
		t.Error("String() returned empty string")
	}
	// Should contain channel, chat_id, and tenant_id
	expected := "feishu:chat123"
	if len(str) < len(expected) {
		t.Errorf("String output too short: %s", str)
	}
}

func TestLoadPersistedCWD_RejectsWorktreePath(t *testing.T) {
	cwdDir := filepath.Join(config.XbotHome(), "session_cwd")
	if err := os.MkdirAll(cwdDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Clean up after test
	defer os.RemoveAll(filepath.Join(config.XbotHome(), "session_cwd"))

	// Write a persisted CWD that points to a worktree
	worktreePath := "/some/repo/.xbot-worktrees/session-123/some-dir"
	cwdFile := filepath.Join(cwdDir, sessionCwdFileName("cli", "test-reject-worktree"))
	if err := os.WriteFile(cwdFile, []byte(worktreePath), 0600); err != nil {
		t.Fatal(err)
	}

	// loadPersistedCWD should reject the worktree path and return ""
	result := loadPersistedCWD("cli", "test-reject-worktree")
	if result != "" {
		t.Errorf("expected empty CWD for worktree path, got %q", result)
	}

	// The persisted file should have been deleted
	if _, err := os.Stat(cwdFile); !os.IsNotExist(err) {
		t.Error("expected persisted CWD file to be deleted after worktree rejection")
	}
}

func TestLoadPersistedCWD_RejectsNonExistentDir(t *testing.T) {
	cwdDir := filepath.Join(config.XbotHome(), "session_cwd")
	if err := os.MkdirAll(cwdDir, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Join(config.XbotHome(), "session_cwd"))

	nonExistPath := "/this/absolutely/does/not/exist"
	cwdFile := filepath.Join(cwdDir, sessionCwdFileName("cli", "test-reject-noexist"))
	if err := os.WriteFile(cwdFile, []byte(nonExistPath), 0600); err != nil {
		t.Fatal(err)
	}

	result := loadPersistedCWD("cli", "test-reject-noexist")
	if result != "" {
		t.Errorf("expected empty CWD for non-existent dir, got %q", result)
	}

	if _, err := os.Stat(cwdFile); !os.IsNotExist(err) {
		t.Error("expected persisted CWD file to be deleted after non-existent rejection")
	}
}

func TestLoadPersistedCWD_AcceptsValidDir(t *testing.T) {
	cwdDir := filepath.Join(config.XbotHome(), "session_cwd")
	if err := os.MkdirAll(cwdDir, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Join(config.XbotHome(), "session_cwd"))

	// Use a real directory
	validPath := t.TempDir()
	cwdFile := filepath.Join(cwdDir, sessionCwdFileName("cli", "test-valid-dir"))
	if err := os.WriteFile(cwdFile, []byte(validPath), 0600); err != nil {
		t.Fatal(err)
	}

	result := loadPersistedCWD("cli", "test-valid-dir")
	if result != validPath {
		t.Errorf("expected %q, got %q", validPath, result)
	}
}

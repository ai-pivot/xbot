package sqlite

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func createCanonicalIdentity(t *testing.T, db *DB, channel, senderID string) int64 {
	t.Helper()
	result, err := db.Conn().Exec(
		"INSERT INTO users (display_name, role) VALUES (?, 'user')",
		fmt.Sprintf("%s-%s", channel, senderID),
	)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("get user id: %v", err)
	}
	if _, err := db.Conn().Exec(
		"INSERT INTO user_identities (user_id, channel, channel_user_id) VALUES (?, ?, ?)",
		userID, channel, senderID,
	); err != nil {
		t.Fatalf("insert identity: %v", err)
	}
	return userID
}

func TestChatAndTenantOwnership_BackfillsCanonicalUser(t *testing.T) {
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	const senderID = "web-owner"
	userID := createCanonicalIdentity(t, db, "web", senderID)
	chatSvc := NewChatService(db)
	chatID, err := chatSvc.CreateChat("web", senderID, "owned chat")
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}

	var chatUserID int64
	if err := db.Conn().QueryRow(
		"SELECT COALESCE(user_id, 0) FROM user_chats WHERE channel = 'web' AND chat_id = ?",
		chatID,
	).Scan(&chatUserID); err != nil {
		t.Fatalf("read chat owner: %v", err)
	}
	if chatUserID != userID {
		t.Fatalf("chat user_id=%d, want %d", chatUserID, userID)
	}

	// Simulate a pre-canonical row and verify normal session loading repairs it.
	if _, err := db.Conn().Exec(
		"UPDATE user_chats SET user_id = 0 WHERE channel = 'web' AND chat_id = ?",
		chatID,
	); err != nil {
		t.Fatalf("clear legacy chat owner: %v", err)
	}
	tenantID, err := NewTenantService(db).GetOrCreateTenantID("web", chatID)
	if err != nil {
		t.Fatalf("get or create tenant: %v", err)
	}

	var ownerUserID int64
	if err := db.Conn().QueryRow(
		"SELECT COALESCE(owner_user_id, 0) FROM tenants WHERE id = ?",
		tenantID,
	).Scan(&ownerUserID); err != nil {
		t.Fatalf("read tenant owner: %v", err)
	}
	if ownerUserID != userID {
		t.Fatalf("tenant owner_user_id=%d, want %d", ownerUserID, userID)
	}
	if err := db.Conn().QueryRow(
		"SELECT COALESCE(user_id, 0) FROM user_chats WHERE channel = 'web' AND chat_id = ?",
		chatID,
	).Scan(&chatUserID); err != nil {
		t.Fatalf("read repaired chat owner: %v", err)
	}
	if chatUserID != userID {
		t.Fatalf("repaired chat user_id=%d, want %d", chatUserID, userID)
	}
}

func TestLinkedIdentityChatAndTenantUseExplicitCanonicalOwner(t *testing.T) {
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	ownerUserID := createCanonicalIdentity(t, db, "feishu", "ou_linked")
	chatID, err := NewChatService(db).CreateChatOwned("web", "ou_linked", "linked chat", ownerUserID)
	if err != nil {
		t.Fatalf("create linked chat: %v", err)
	}
	tenantID, err := NewTenantService(db).GetOrCreateTenantID("web", chatID)
	if err != nil {
		t.Fatalf("create linked tenant: %v", err)
	}

	var chatOwner, tenantOwner int64
	if err := db.Conn().QueryRow(`SELECT COALESCE(user_id, 0) FROM user_chats WHERE channel = 'web' AND chat_id = ?`, chatID).Scan(&chatOwner); err != nil {
		t.Fatalf("read linked chat owner: %v", err)
	}
	if err := db.Conn().QueryRow(`SELECT COALESCE(owner_user_id, 0) FROM tenants WHERE id = ?`, tenantID).Scan(&tenantOwner); err != nil {
		t.Fatalf("read linked tenant owner: %v", err)
	}
	if chatOwner != ownerUserID || tenantOwner != ownerUserID {
		t.Fatalf("linked ownership = chat %d tenant %d, want %d", chatOwner, tenantOwner, ownerUserID)
	}
}

func TestTenantService_GetOrCreateTenantID(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewTenantService(db)

	// Create first tenant
	id1, err := svc.GetOrCreateTenantID("feishu", "chat123")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}
	if id1 == 0 {
		t.Error("Expected non-zero tenant ID")
	}

	// Get same tenant - should return same ID
	id2, err := svc.GetOrCreateTenantID("feishu", "chat123")
	if err != nil {
		t.Fatalf("Failed to get tenant: %v", err)
	}
	if id2 != id1 {
		t.Errorf("Expected same tenant ID %d, got %d", id1, id2)
	}

	// Create different tenant - should return different ID
	id3, err := svc.GetOrCreateTenantID("feishu", "chat456")
	if err != nil {
		t.Fatalf("Failed to create second tenant: %v", err)
	}
	if id3 == id1 {
		t.Error("Expected different tenant ID for different chat")
	}

	// Create tenant with different channel
	id4, err := svc.GetOrCreateTenantID("slack", "chat123")
	if err != nil {
		t.Fatalf("Failed to create tenant with different channel: %v", err)
	}
	if id4 == id1 || id4 == id3 {
		t.Error("Expected different tenant ID for different channel")
	}
}

func TestTenantService_ClaimOrVerifyTenantOwner(t *testing.T) {
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	svc := NewTenantService(db)
	tenantID, err := svc.ClaimOrVerifyTenantOwner("cli", "claimed", 42)
	if err != nil {
		t.Fatalf("initial claim: %v", err)
	}
	verifiedID, err := svc.ClaimOrVerifyTenantOwner("cli", "claimed", 42)
	if err != nil || verifiedID != tenantID {
		t.Fatalf("same-owner verification = (%d, %v), want (%d, nil)", verifiedID, err, tenantID)
	}
	if _, err := svc.ClaimOrVerifyTenantOwner("cli", "claimed", 99); !errors.Is(err, ErrTenantOwnerConflict) {
		t.Fatalf("foreign claim error = %v, want ErrTenantOwnerConflict", err)
	}

	if _, err := svc.GetOrCreateTenantID("cli", "legacy"); err != nil {
		t.Fatalf("create legacy tenant: %v", err)
	}
	if _, err := svc.ClaimOrVerifyTenantOwner("cli", "legacy", 99); err != nil {
		t.Fatalf("claim legacy tenant: %v", err)
	}
	var ownerUserID int64
	if err := db.Conn().QueryRow(
		`SELECT COALESCE(owner_user_id, 0) FROM tenants WHERE channel = 'cli' AND chat_id = 'legacy'`,
	).Scan(&ownerUserID); err != nil {
		t.Fatalf("read legacy owner: %v", err)
	}
	if ownerUserID != 99 {
		t.Fatalf("legacy owner = %d, want 99", ownerUserID)
	}
}

func TestTenantService_GetTenantInfo(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewTenantService(db)

	// Create tenant
	tenantID, err := svc.GetOrCreateTenantID("feishu", "test_chat")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Get tenant info
	channel, chatID, err := svc.GetTenantInfo(tenantID)
	if err != nil {
		t.Fatalf("Failed to get tenant info: %v", err)
	}

	if channel != "feishu" {
		t.Errorf("Expected channel 'feishu', got '%s'", channel)
	}
	if chatID != "test_chat" {
		t.Errorf("Expected chatID 'test_chat', got '%s'", chatID)
	}

	// Try to get non-existent tenant
	_, _, err = svc.GetTenantInfo(99999)
	if err == nil {
		t.Error("Expected error for non-existent tenant")
	}
}

func TestTenantService_DeleteTenant(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewTenantService(db)

	// Create tenant
	tenantID, err := svc.GetOrCreateTenantID("feishu", "to_delete")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Delete tenant
	err = svc.DeleteTenant(tenantID)
	if err != nil {
		t.Fatalf("Failed to delete tenant: %v", err)
	}

	// Try to get deleted tenant
	_, _, err = svc.GetTenantInfo(tenantID)
	if err == nil {
		t.Error("Expected error for deleted tenant")
	}

	// Try to delete non-existent tenant
	err = svc.DeleteTenant(99999)
	if err == nil {
		t.Error("Expected error when deleting non-existent tenant")
	}
}

func TestTenantService_ListTenants(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewTenantService(db)

	// Create multiple tenants
	ids := []int64{}
	for i := 0; i < 3; i++ {
		id, err := svc.GetOrCreateTenantID("feishu", "chat"+string(rune('0'+i)))
		if err != nil {
			t.Fatalf("Failed to create tenant: %v", err)
		}
		ids = append(ids, id)
	}

	// List tenants
	tenants, err := svc.ListTenants()
	if err != nil {
		t.Fatalf("Failed to list tenants: %v", err)
	}

	if len(tenants) != 3 {
		t.Errorf("Expected 3 tenants, got %d", len(tenants))
	}

	// Verify tenant IDs
	idMap := make(map[int64]bool)
	for _, tenant := range tenants {
		idMap[tenant.ID] = true
		if tenant.Channel != "feishu" {
			t.Errorf("Expected channel 'feishu', got '%s'", tenant.Channel)
		}
	}
	for _, id := range ids {
		if !idMap[id] {
			t.Errorf("Tenant ID %d not found in list", id)
		}
	}
}

func TestTenantService_SetAndGetSubscription(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewTenantService(db)

	// Create tenant first
	_, err = svc.GetOrCreateTenantID("cli", "/home/user/project")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Set subscription mapping
	err = svc.SetTenantSubscription("cli", "/home/user/project", "sub-123", "gpt-4o")
	if err != nil {
		t.Fatalf("Failed to set subscription: %v", err)
	}

	// Read it back
	subID, model, err := svc.GetTenantSubscription("cli", "/home/user/project")
	if err != nil {
		t.Fatalf("Failed to get subscription: %v", err)
	}
	if subID != "sub-123" {
		t.Errorf("Expected subID 'sub-123', got %q", subID)
	}
	if model != "gpt-4o" {
		t.Errorf("Expected model 'gpt-4o', got %q", model)
	}

	// Update with different values
	err = svc.SetTenantSubscription("cli", "/home/user/project", "sub-456", "claude-3")
	if err != nil {
		t.Fatalf("Failed to update subscription: %v", err)
	}
	subID, model, _ = svc.GetTenantSubscription("cli", "/home/user/project")
	if subID != "sub-456" || model != "claude-3" {
		t.Errorf("Expected updated values, got %q / %q", subID, model)
	}
}

func TestTenantService_GetSubscription_NotFound(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewTenantService(db)

	// Non-existent tenant returns empty strings, no error
	subID, model, err := svc.GetTenantSubscription("cli", "/nonexistent")
	if err != nil {
		t.Fatalf("Expected no error for non-existent, got %v", err)
	}
	if subID != "" || model != "" {
		t.Errorf("Expected empty strings, got %q / %q", subID, model)
	}
}

func TestTenantService_GetOrCreate_DoesNotOverwriteSubscription(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewTenantService(db)

	// Create tenant and set subscription
	_, _ = svc.GetOrCreateTenantID("cli", "/test")
	svc.SetTenantSubscription("cli", "/test", "sub-abc", "deepseek")

	// GetOrCreateTenantID again — should NOT overwrite subscription
	_, err = svc.GetOrCreateTenantID("cli", "/test")
	if err != nil {
		t.Fatalf("Failed: %v", err)
	}
	subID, model, _ := svc.GetTenantSubscription("cli", "/test")
	if subID != "sub-abc" || model != "deepseek" {
		t.Errorf("Subscription was overwritten by GetOrCreate: got %q/%q", subID, model)
	}
}

func TestTenantService_ListTenants_IncludesSubscription(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewTenantService(db)
	_, _ = svc.GetOrCreateTenantID("cli", "/chat-a")
	_, _ = svc.GetOrCreateTenantID("cli", "/chat-b")
	svc.SetTenantSubscription("cli", "/chat-a", "sub-1", "gpt-4o")
	svc.SetTenantSubscription("cli", "/chat-b", "sub-2", "claude-3")

	tenants, err := svc.ListTenants()
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}

	subs := make(map[string]TenantInfo)
	for _, t := range tenants {
		subs[t.ChatID] = t
	}
	if a, ok := subs["/chat-a"]; !ok || a.SubscriptionID != "sub-1" || a.Model != "gpt-4o" {
		t.Errorf("/chat-a: got sub=%q model=%q", a.SubscriptionID, a.Model)
	}
	if b, ok := subs["/chat-b"]; !ok || b.SubscriptionID != "sub-2" || b.Model != "claude-3" {
		t.Errorf("/chat-b: got sub=%q model=%q", b.SubscriptionID, b.Model)
	}
}

func TestTenantService_SetTenantSubscription_ClearsTokenSnapshotOnlyOnChange(t *testing.T) {
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	if err := tenantSvc.SetTenantSubscription("web", "chat-1", "sub-1", "model-1"); err != nil {
		t.Fatalf("set initial subscription: %v", err)
	}
	tenantID, err := tenantSvc.GetTenantIDByChannelChatID("web", "chat-1")
	if err != nil || tenantID == 0 {
		t.Fatalf("get tenant: id=%d err=%v", tenantID, err)
	}
	if _, err := db.Conn().Exec(
		"INSERT INTO session_messages (tenant_id, role, content, context_tokens) VALUES (?, 'user', 'hello', 12345)",
		tenantID,
	); err != nil {
		t.Fatalf("insert user message: %v", err)
	}
	memSvc := NewMemoryService(db)
	if err := memSvc.SetTokenState(context.Background(), tenantID, 12345, 678); err != nil {
		t.Fatalf("set token state: %v", err)
	}

	if err := tenantSvc.SetTenantSubscription("web", "chat-1", "sub-1", "model-1"); err != nil {
		t.Fatalf("repeat same subscription: %v", err)
	}
	assertTenantTokenSnapshot(t, db, tenantID, 12345, 678, 12345)

	if err := tenantSvc.SetTenantSubscription("web", "chat-1", "sub-1", "model-2"); err != nil {
		t.Fatalf("switch model: %v", err)
	}
	assertTenantTokenSnapshot(t, db, tenantID, 0, 0, 0)
}

func assertTenantTokenSnapshot(t *testing.T, db *DB, tenantID, wantPrompt, wantCompletion, wantContext int64) {
	t.Helper()
	prompt, completion, err := NewMemoryService(db).GetTokenState(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("get token state: %v", err)
	}
	contextTokens, err := NewSessionService(db).GetLastUserMessageContextTokens(tenantID)
	if err != nil {
		t.Fatalf("get user context tokens: %v", err)
	}
	if prompt != wantPrompt || completion != wantCompletion || contextTokens != wantContext {
		t.Fatalf("snapshot=(%d,%d,%d), want (%d,%d,%d)", prompt, completion, contextTokens, wantPrompt, wantCompletion, wantContext)
	}
}

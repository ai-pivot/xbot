package sqlite

import (
	"context"
	"testing"
)

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

// TestTenantService_GetOrCreateDoesNotTouchLastActive guards against the
// pagination-scrambling regression: GetOrCreateTenantID is a pure lookup for
// pre-existing rows and must NOT refresh last_active_at. Previously every "get"
// bumped last_active_at to now, so flipping pages (which touches many tenants
// via read-only RPCs) made every session's update time jump to today and
// scrambled last-active ordering during offset pagination.
func TestTenantService_GetOrCreateDoesNotTouchLastActive(t *testing.T) {
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewTenantService(db)

	// Create the tenant, then pin last_active_at to an old timestamp.
	if _, err := svc.GetOrCreateTenantID("web", "page1"); err != nil {
		t.Fatalf("create: %v", err)
	}
	var tenantID int64
	if err := db.Conn().QueryRow(`SELECT id FROM tenants WHERE channel='web' AND chat_id='page1'`).Scan(&tenantID); err != nil {
		t.Fatalf("select: %v", err)
	}
	old := "2026-01-01T00:00:00Z"
	if _, err := db.Conn().Exec(`UPDATE tenants SET last_active_at=? WHERE id=?`, old, tenantID); err != nil {
		t.Fatalf("pin last_active: %v", err)
	}

	// Getting the same tenant again must NOT bump last_active_at.
	if _, err := svc.GetOrCreateTenantID("web", "page1"); err != nil {
		t.Fatalf("get: %v", err)
	}
	var lastActive string
	if err := db.Conn().QueryRow(`SELECT last_active_at FROM tenants WHERE id=?`, tenantID).Scan(&lastActive); err != nil {
		t.Fatalf("re-select: %v", err)
	}
	if lastActive != old {
		t.Fatalf("GetOrCreateTenantID touched last_active_at: want %q, got %q", old, lastActive)
	}

	// TouchTenantID is the explicit path that DOES bump it.
	if err := svc.TouchTenantID("web", "page1"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	if err := db.Conn().QueryRow(`SELECT last_active_at FROM tenants WHERE id=?`, tenantID).Scan(&lastActive); err != nil {
		t.Fatalf("re-select after touch: %v", err)
	}
	if lastActive == old {
		t.Fatal("TouchTenantID did not bump last_active_at")
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

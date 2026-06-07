package sqlite

import (
	"testing"
)

// TestV35Migration_SubscriptionModelsTable verifies the v35 migration creates
// subscription_models table and the CRUD operations work correctly.
func TestV35Migration_SubscriptionModelsTable(t *testing.T) {
	db := openTestDB(t)
	conn := db.Conn()

	// Verify subscription_models table exists
	var count int
	err := conn.QueryRow("SELECT COUNT(*) FROM subscription_models").Scan(&count)
	if err != nil {
		t.Fatalf("subscription_models table should exist: %v", err)
	}

	// Verify tenants has model_id column
	_, err = conn.Exec("INSERT OR IGNORE INTO tenants (channel, chat_id) VALUES ('cli-mig', '/test-mig')")
	if err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	_, err = conn.Exec("UPDATE tenants SET model_id = 'test-model-id' WHERE channel = 'cli-mig' AND chat_id = '/test-mig'")
	if err != nil {
		t.Fatalf("tenants.model_id column should exist: %v", err)
	}

	// Verify schema version is 35
	var version int
	conn.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version)
	if version != 35 {
		t.Errorf("schema version = %d, want 35", version)
	}

	// Verify migration is idempotent
	if err := migrateV34ToV35(db); err != nil {
		t.Errorf("migration should be idempotent: %v", err)
	}
}

// TestSubscriptionModelCRUD verifies the full lifecycle of SubscriptionModel:
// GetModels, GetModel, UpsertModel (create + update).
func TestSubscriptionModelCRUD(t *testing.T) {
	db := openTestDB(t)
	svc := NewLLMSubscriptionService(db)

	// Add a subscription first
	sub := &LLMSubscription{ID: "crud-sub", SenderID: "cli_user", Name: "Test", Provider: "openai", BaseURL: "http://api", APIKey: "sk-test"}
	if err := svc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// GetModels on empty subscription: should return empty
	models, err := svc.GetModels("crud-sub")
	if err != nil {
		t.Fatalf("GetModels empty: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("expected 0 models, got %d", len(models))
	}

	// GetModel on non-existent: should return nil
	m, err := svc.GetModel("crud-sub", "nonexistent")
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	if m != nil {
		t.Errorf("expected nil for non-existent model, got %+v", m)
	}

	// UpsertModel: create
	if err := svc.UpsertModel("crud-sub", "gpt-4", 200000, 8192, "enabled"); err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}

	// GetModel: verify
	m, err = svc.GetModel("crud-sub", "gpt-4")
	if err != nil || m == nil {
		t.Fatalf("GetModel after create: err=%v m=%v", err, m)
	}
	if m.MaxContext != 200000 || m.MaxOutputTokens != 8192 || m.ThinkingMode != "enabled" {
		t.Errorf("model data: MaxContext=%d MaxOutput=%d Thinking=%q", m.MaxContext, m.MaxOutputTokens, m.ThinkingMode)
	}
	oldID := m.ID

	// UpsertModel: update
	svc.UpsertModel("crud-sub", "gpt-4", 500000, 16384, "")
	m, _ = svc.GetModel("crud-sub", "gpt-4")
	if m == nil || m.MaxContext != 500000 || m.MaxOutputTokens != 16384 {
		t.Errorf("after update: MaxContext=%d MaxOutput=%d", m.MaxContext, m.MaxOutputTokens)
	}
	if m.ID != oldID {
		t.Errorf("ID changed after update: %q -> %q", oldID, m.ID)
	}

	// Add second model + verify count
	svc.UpsertModel("crud-sub", "gpt-3.5", 16000, 4096, "")
	models, _ = svc.GetModels("crud-sub")
	if len(models) != 2 {
		t.Errorf("expected 2 models, got %d", len(models))
	}

	// Verify unique constraint: re-upsert same model is update, not insert
	svc.UpsertModel("crud-sub", "gpt-4", 999, 999, "")
	models, _ = svc.GetModels("crud-sub")
	if len(models) != 2 {
		t.Errorf("expected still 2 models after re-upsert, got %d", len(models))
	}
}

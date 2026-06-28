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

	// Verify schema version is 44
	var version int
	conn.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version)
	if version != 44 {
		t.Errorf("schema version = %d, want 44", version)
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
	if err := svc.UpsertModel("crud-sub", "gpt-4", 200000, 8192, "enabled", ""); err != nil {
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
	svc.UpsertModel("crud-sub", "gpt-4", 500000, 16384, "", "")
	m, _ = svc.GetModel("crud-sub", "gpt-4")
	if m == nil || m.MaxContext != 500000 || m.MaxOutputTokens != 16384 {
		t.Errorf("after update: MaxContext=%d MaxOutput=%d", m.MaxContext, m.MaxOutputTokens)
	}
	if m.ID != oldID {
		t.Errorf("ID changed after update: %q -> %q", oldID, m.ID)
	}

	// Add second model + verify count
	svc.UpsertModel("crud-sub", "gpt-3.5", 16000, 4096, "", "")
	models, _ = svc.GetModels("crud-sub")
	if len(models) != 2 {
		t.Errorf("expected 2 models, got %d", len(models))
	}

	// Verify unique constraint: re-upsert same model is update, not insert
	svc.UpsertModel("crud-sub", "gpt-4", 999, 999, "", "")
	models, _ = svc.GetModels("crud-sub")
	if len(models) != 2 {
		t.Errorf("expected still 2 models after re-upsert, got %d", len(models))
	}
}

// TestV39Migration_ModelFirstFoundation verifies the v39 migration adds the
// enabled column, the user_default_model table, and backfills concrete model
// rows for tenants-referenced (sub, model) pairs.
func TestV39Migration_ModelFirstFoundation(t *testing.T) {
	db := openTestDB(t)
	conn := db.Conn()
	svc := NewLLMSubscriptionService(db)

	// enabled column present on subscription_models.
	var ec int
	if err := conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('subscription_models') WHERE name='enabled'").Scan(&ec); err != nil || ec != 1 {
		t.Fatalf("subscription_models.enabled missing: err=%v count=%d", err, ec)
	}

	// user_default_model table present.
	var uc int
	if err := conn.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='user_default_model'").Scan(&uc); err != nil || uc != 1 {
		t.Fatalf("user_default_model table missing: err=%v count=%d", err, uc)
	}

	// Freshly upserted models default to enabled.
	sub := &LLMSubscription{ID: "v38-sub", SenderID: "cli_user", Name: "V38", Provider: "openai", BaseURL: "http://api", APIKey: "sk-test"}
	if err := svc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.UpsertModel("v38-sub", "m1", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}
	m, err := svc.GetModel("v38-sub", "m1")
	if err != nil || m == nil {
		t.Fatalf("GetModel: err=%v m=%v", err, m)
	}
	if !m.Enabled {
		t.Errorf("newly upserted model should be enabled by default, got enabled=false")
	}

	// SetModelEnabled toggles and persists.
	if err := svc.SetModelEnabled("v38-sub", "m1", false); err != nil {
		t.Fatalf("SetModelEnabled(false): %v", err)
	}
	m, _ = svc.GetModel("v38-sub", "m1")
	if m == nil || m.Enabled {
		t.Errorf("after disable, model.Enabled should be false")
	}
	if err := svc.SetModelEnabled("v38-sub", "m1", true); err != nil {
		t.Fatalf("SetModelEnabled(true): %v", err)
	}
	if err := svc.SetModelEnabled("v38-sub", "nope", true); err == nil {
		t.Errorf("SetModelEnabled on missing row should error")
	}

	// Backfill: a tenant referencing a (sub, model) with no row gets a concrete row.
	tenantSubID := "v38-sub"
	_, err = conn.Exec("INSERT OR IGNORE INTO tenants (channel, chat_id, subscription_id, model) VALUES ('cli', '/v38-backfill', ?, 'backfilled-model')", tenantSubID)
	if err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	// Re-run v39 to exercise the backfill path against the just-added tenant.
	if err := migrateV38ToV39(conn); err != nil {
		t.Fatalf("re-run migrateV38ToV39: %v", err)
	}
	m, err = svc.GetModel("v38-sub", "backfilled-model")
	if err != nil || m == nil {
		t.Fatalf("backfilled model row missing: err=%v m=%v", err, m)
	}
	if m.MaxContext != 0 || !m.Enabled {
		t.Errorf("backfilled row should be defaults (max_ctx=0, enabled=true): %+v", m)
	}

	// UserDefaultModel get/set/clear.
	if got, err := svc.GetUserDefaultModel("nobody"); err != nil || got != nil {
		t.Errorf("GetUserDefaultModel unset: err=%v got=%v", err, got)
	}
	if err := svc.SetUserDefaultModel("cli_user", "v38-sub", "m1"); err != nil {
		t.Fatalf("SetUserDefaultModel: %v", err)
	}
	udm, err := svc.GetUserDefaultModel("cli_user")
	if err != nil || udm == nil || udm.SubscriptionID != "v38-sub" || udm.Model != "m1" {
		t.Errorf("GetUserDefaultModel after set: err=%v udm=%+v", err, udm)
	}
	if err := svc.SetUserDefaultModel("cli_user", "v38-sub", "m2"); err != nil {
		t.Fatalf("SetUserDefaultModel update: %v", err)
	}
	udm, _ = svc.GetUserDefaultModel("cli_user")
	if udm == nil || udm.Model != "m2" {
		t.Errorf("GetUserDefaultModel after update: %+v", udm)
	}
	if err := svc.ClearUserDefaultModel("cli_user"); err != nil {
		t.Fatalf("ClearUserDefaultModel: %v", err)
	}
	udm, _ = svc.GetUserDefaultModel("cli_user")
	if udm != nil {
		t.Errorf("GetUserDefaultModel after clear: %+v", udm)
	}
}

// TestSystemSubscription verifies the shared system subscription lifecycle:
// UpsertSystemSubscription reconciles fields on each boot, GetSystemSubscription
// returns it, List injects it for every user, mutation guards refuse it, and
// GetDefault falls back to it when a user has no default.
func TestSystemSubscription(t *testing.T) {
	db := openTestDB(t)
	svc := NewLLMSubscriptionService(db)

	// No system subscription initially.
	if sys, err := svc.GetSystemSubscription(); err != nil || sys != nil {
		t.Fatalf("GetSystemSubscription before upsert: err=%v sys=%v", err, sys)
	}

	// Reconcile from "config/env" values.
	first := &LLMSubscription{Provider: "openai", BaseURL: "http://api", APIKey: "sk-first", Model: "gpt-4o"}
	if err := svc.UpsertSystemSubscription(first); err != nil {
		t.Fatalf("UpsertSystemSubscription: %v", err)
	}
	sys, err := svc.GetSystemSubscription()
	if err != nil || sys == nil {
		t.Fatalf("GetSystemSubscription after upsert: err=%v sys=%v", err, sys)
	}
	if sys.ID != "system" || sys.SenderID != SystemSenderID || sys.Name != SystemSubscriptionName || !sys.IsSystem {
		t.Errorf("system sub identity: id=%q sender=%q name=%q isSystem=%v", sys.ID, sys.SenderID, sys.Name, sys.IsSystem)
	}
	if sys.Provider != "openai" || sys.Model != "gpt-4o" || sys.APIKey != "sk-first" {
		t.Errorf("system sub fields: provider=%q model=%q key=%q", sys.Provider, sys.Model, sys.APIKey)
	}

	// Reconcile again with different values — fields overwrite, ID stable.
	second := &LLMSubscription{Provider: "deepseek", BaseURL: "http://ds", APIKey: "sk-second", Model: "deepseek-v4"}
	if err := svc.UpsertSystemSubscription(second); err != nil {
		t.Fatalf("UpsertSystemSubscription reconcile: %v", err)
	}
	sys, _ = svc.GetSystemSubscription()
	if sys == nil || sys.Provider != "deepseek" || sys.Model != "deepseek-v4" || sys.APIKey != "sk-second" {
		t.Errorf("reconciled fields wrong: %+v", sys)
	}

	// List injects the system sub for an arbitrary user.
	subs, err := svc.List("any-user")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, s := range subs {
		if s.IsSystem {
			found = true
		}
	}
	if !found {
		t.Errorf("List should include system subscription for any user, got %d subs", len(subs))
	}

	// Mutation guards refuse the system subscription.
	if err := svc.SetSubscriptionEnabled("system", false); err == nil {
		t.Errorf("SetSubscriptionEnabled on system should error")
	}
	if err := svc.Rename("system", "x"); err == nil {
		t.Errorf("Rename on system should error")
	}
	if err := svc.Remove("system"); err == nil {
		t.Errorf("Remove on system should error")
	}
	if err := svc.Update(&LLMSubscription{ID: "system", SenderID: SystemSenderID, Name: "x"}); err == nil {
		t.Errorf("Update on system should error")
	}

	// GetDefault falls back to system when user has no default.
	def, err := svc.GetDefault("user-without-default")
	if err != nil || def == nil || !def.IsSystem {
		t.Errorf("GetDefault fallback to system: err=%v def=%+v", err, def)
	}

	// GetDefault prefers the user's explicit default when set.
	own := &LLMSubscription{ID: "own-sub", SenderID: "u1", Name: "Own", Provider: "openai", BaseURL: "http://api", APIKey: "sk"}
	if err := svc.Add(own); err != nil {
		t.Fatalf("Add own: %v", err)
	}
	if err := svc.SetUserDefaultModel("u1", "own-sub", "gpt-4o"); err != nil {
		t.Fatalf("SetUserDefaultModel: %v", err)
	}
	def, _ = svc.GetDefault("u1")
	if def == nil || def.ID != "own-sub" {
		t.Errorf("GetDefault should prefer user default, got %+v", def)
	}
}

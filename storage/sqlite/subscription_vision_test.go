package sqlite

// Per-model vision (multimodal image input) configuration — v64.
//
// Vision is a purely MANUAL per-model switch (NO built-in model-name whitelist):
// the operator enables it per model in the model editor. The switch must
// survive every unrelated per-model write (token configs, thinking mode,
// api_type) — token-config upserts never reset it (SetModelVisionConfig is
// the sole write path for the vision columns).

import (
	"testing"
)

func TestSetModelVisionConfig_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	svc := NewLLMSubscriptionService(db)

	sub := &LLMSubscription{ID: "vis-sub", SenderID: "cli_user", Name: "Vision", Provider: "openai", BaseURL: "http://api", APIKey: "sk-test"}
	if err := svc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.UpsertModel("vis-sub", "glm-4.6v", 128000, 8192, "", ""); err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}

	// Default: vision off.
	m, err := svc.GetModel("vis-sub", "glm-4.6v")
	if err != nil || m == nil {
		t.Fatalf("GetModel: err=%v m=%v", err, m)
	}
	if m.Vision {
		t.Errorf("vision should default to false, got true")
	}

	// SetModelVisionConfig round-trips vision + vision_detail.
	if err := svc.SetModelVisionConfig("vis-sub", "glm-4.6v", true, "low"); err != nil {
		t.Fatalf("SetModelVisionConfig: %v", err)
	}
	m, _ = svc.GetModel("vis-sub", "glm-4.6v")
	if m == nil || !m.Vision || m.VisionDetail != "low" {
		t.Fatalf("after SetModelVisionConfig: Vision=%v VisionDetail=%q (want true, low)", m.Vision, m.VisionDetail)
	}

	// UpsertModel (token config write) must NOT reset vision.
	if err := svc.UpsertModel("vis-sub", "glm-4.6v", 200000, 16384, "enabled", "responses"); err != nil {
		t.Fatalf("UpsertModel after vision set: %v", err)
	}
	m, _ = svc.GetModel("vis-sub", "glm-4.6v")
	if m == nil || !m.Vision || m.VisionDetail != "low" {
		t.Fatalf("UpsertModel clobbered vision: Vision=%v VisionDetail=%q (want true, low)", m.Vision, m.VisionDetail)
	}
	if m.MaxContext != 200000 || m.APIType != "responses" {
		t.Errorf("UpsertModel token fields: MaxContext=%d APIType=%q", m.MaxContext, m.APIType)
	}

	// SetModelMaxContext / SetModelMaxOutput single-column writes keep vision.
	if err := svc.SetModelMaxContext("vis-sub", "glm-4.6v", 256000); err != nil {
		t.Fatalf("SetModelMaxContext: %v", err)
	}
	m, _ = svc.GetModel("vis-sub", "glm-4.6v")
	if m == nil || !m.Vision {
		t.Fatalf("SetModelMaxContext clobbered vision: %+v", m)
	}

	// Disable again.
	if err := svc.SetModelVisionConfig("vis-sub", "glm-4.6v", false, ""); err != nil {
		t.Fatalf("SetModelVisionConfig(false): %v", err)
	}
	m, _ = svc.GetModel("vis-sub", "glm-4.6v")
	if m == nil || m.Vision || m.VisionDetail != "" {
		t.Fatalf("after disable: Vision=%v VisionDetail=%q", m.Vision, m.VisionDetail)
	}

	// PerModelConfigs projection carries Vision/VisionDetail (loadPerModelConfigs path).
	if err := svc.SetModelVisionConfig("vis-sub", "glm-4.6v", true, "high"); err != nil {
		t.Fatalf("SetModelVisionConfig(high): %v", err)
	}
	got, err := svc.Get("vis-sub")
	if err != nil || got == nil {
		t.Fatalf("Get: err=%v sub=%v", err, got)
	}
	cfg, ok := got.PerModelConfigs["glm-4.6v"]
	if !ok {
		t.Fatalf("PerModelConfigs missing glm-4.6v: %+v", got.PerModelConfigs)
	}
	if !cfg.Vision || cfg.VisionDetail != "high" {
		t.Errorf("PerModelConfigs projection: Vision=%v VisionDetail=%q (want true, high)", cfg.Vision, cfg.VisionDetail)
	}
}

// TestSetModelVisionConfig_UpdatesPerModelConfigsMap verifies the
// update_per_model_config RPC semantics: UpsertModel (token path) followed by
// SetModelVisionConfig produces the same end state as writing the full
// PerModelConfig map through UpdatePerModelConfigs.
func TestSetModelVisionConfig_UpdatesPerModelConfigsMap(t *testing.T) {
	db := openTestDB(t)
	svc := NewLLMSubscriptionService(db)

	sub := &LLMSubscription{ID: "vis-map", SenderID: "cli_user", Name: "Map", Provider: "openai", BaseURL: "http://api", APIKey: "sk"}
	if err := svc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.UpsertModel("vis-map", "m1", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}
	if err := svc.SetModelVisionConfig("vis-map", "m1", true, ""); err != nil {
		t.Fatalf("SetModelVisionConfig: %v", err)
	}

	// UpdatePerModelConfigs (delete + reinsert) carries Vision through the map.
	cfgs := map[string]PerModelConfig{
		"m1": {MaxContext: 1000, MaxOutputTokens: 100, Vision: true, VisionDetail: "high"},
	}
	if err := svc.UpdatePerModelConfigs("vis-map", cfgs); err != nil {
		t.Fatalf("UpdatePerModelConfigs: %v", err)
	}
	m, _ := svc.GetModel("vis-map", "m1")
	if m == nil || !m.Vision || m.VisionDetail != "high" {
		t.Fatalf("UpdatePerModelConfigs lost vision: %+v", m)
	}

	// Add() with a PerModelConfigs map containing Vision — new rows honor it.
	sub2 := &LLMSubscription{
		ID: "vis-add", SenderID: "cli_user", Name: "Add", Provider: "openai", BaseURL: "http://api", APIKey: "sk",
		PerModelConfigs: map[string]PerModelConfig{
			"vm": {Vision: true, VisionDetail: "low"},
		},
	}
	if err := svc.Add(sub2); err != nil {
		t.Fatalf("Add with vision map: %v", err)
	}
	m2, _ := svc.GetModel("vis-add", "vm")
	if m2 == nil || !m2.Vision || m2.VisionDetail != "low" {
		t.Fatalf("Add() PerModelConfigs vision: %+v", m2)
	}
}

// TestV64Migration_VisionColumns verifies migrateV63ToV64 is idempotent and
// the vision columns exist after the fresh-schema path (createSchema includes
// them for new DBs).
func TestV64Migration_VisionColumns(t *testing.T) {
	db := openTestDB(t)
	conn := db.Conn()

	// Fresh schema includes the columns.
	for _, col := range []string{"vision", "vision_detail"} {
		var n int
		if err := conn.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info('subscription_models') WHERE name = ?", col,
		).Scan(&n); err != nil || n != 1 {
			t.Fatalf("subscription_models.%s missing in fresh schema: err=%v count=%d", col, err, n)
		}
	}

	// Re-running the migration is a no-op (columnExists guards).
	if err := migrateV63ToV64(db); err != nil {
		t.Fatalf("migrateV63ToV64 should be idempotent: %v", err)
	}
}

package serverapp

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"xbot/agent"
	"xbot/config"
	"xbot/llm"
	"xbot/storage/sqlite"
)

// TestImportSubscriptionsDupCheckSnapshotsExistingNames verifies import_subscriptions
// duplicate-name semantics after hoisting the dup-check snapshot out of the loop
// (Fix 7 N+1): existing-name skip, same-batch duplicate skip, and overwrite
// bypass all keep their original behavior.
func TestImportSubscriptionsDupCheckSnapshotsExistingNames(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(filepath.Join(dir, "xbot.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	factory.SetTenantSvc(sqlite.NewTenantService(db))

	// Pre-existing subscription named "dup" owned by the canonical user.
	if err := subSvc.Add(&sqlite.LLMSubscription{ID: "sub-dup", SenderID: "cli_user", Name: "dup", Provider: "openai", BaseURL: "https://api.example/v1", APIKey: "sk-x", Model: "m1"}); err != nil {
		t.Fatalf("add pre-existing: %v", err)
	}
	if err := subSvc.SetSubscriptionUserID("sub-dup", 1); err != nil {
		t.Fatalf("bind pre-existing to user: %v", err)
	}

	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(&config.Config{}, ag, nil, nil, nil)

	mkParams := func(overwrite bool, names ...string) []byte {
		subs := make([]map[string]any, len(names))
		for i, n := range names {
			subs[i] = map[string]any{"name": n, "provider": "openai", "base_url": "https://api.example/v1", "api_key": "sk-n", "model": "m"}
		}
		params, _ := json.Marshal(map[string]any{"subs": subs, "overwrite": overwrite})
		return params
	}

	// Batch 1 (no overwrite): "dup" (exists) + "new-a" (fresh) + "new-a" again
	// (same-batch duplicate must also be skipped — the old per-iteration
	// re-query saw the freshly imported one, so the hoisted set must too).
	raw, err := HandleCLIRPC(table, "import_subscriptions", mkParams(false, "dup", "new-a", "new-a"), "admin")
	if err != nil {
		t.Fatalf("import batch 1: %v", err)
	}
	var res struct {
		Imported int `json:"imported"`
		Skipped  int `json:"skipped"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Imported != 1 || res.Skipped != 2 {
		t.Fatalf("batch 1 imported=%d skipped=%d, want 1/2 (pre-existing dup + same-batch dup skipped)", res.Imported, res.Skipped)
	}

	// Batch 2 (overwrite=true): duplicates are imported regardless.
	raw, err = HandleCLIRPC(table, "import_subscriptions", mkParams(true, "dup", "new-a"), "admin")
	if err != nil {
		t.Fatalf("import batch 2: %v", err)
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Imported != 2 || res.Skipped != 0 {
		t.Fatalf("batch 2 imported=%d skipped=%d, want 2/0 (overwrite bypasses dup check)", res.Imported, res.Skipped)
	}
}

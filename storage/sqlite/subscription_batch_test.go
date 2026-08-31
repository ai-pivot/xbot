package sqlite

import "testing"

// TestList_BatchedPerModelConfigs verifies ListAll/List populate
// PerModelConfigs via the single batched subscription_models query
// (loadPerModelConfigsBatch), across MULTIPLE subscriptions each carrying
// multiple per-model rows. Regression guard for the N+1 → batched-IN fix:
// every sub's models must land in the right PerModelConfigs map with the
// right fields, and subs with no models keep an empty (non-nil) map.
// (Post-v63: subscriptions are global — the ListByUserID user dimension
// was removed with the multi-user system.)
func TestList_BatchedPerModelConfigs(t *testing.T) {
	db := openTestDB(t)
	svc := NewLLMSubscriptionService(db)

	owned1 := &LLMSubscription{ID: "batch-a", SenderID: "cli_user", Name: "a", Provider: "openai", BaseURL: "http://a", APIKey: "sk"}
	owned2 := &LLMSubscription{ID: "batch-b", SenderID: "cli_user", Name: "b", Provider: "openai", BaseURL: "http://b", APIKey: "sk"}
	other := &LLMSubscription{ID: "batch-c", SenderID: "cli_user", Name: "c", Provider: "openai", BaseURL: "http://c", APIKey: "sk"}
	for _, sub := range []*LLMSubscription{owned1, owned2, other} {
		if err := svc.Add(sub); err != nil {
			t.Fatalf("Add %s: %v", sub.ID, err)
		}
	}

	// Per-model rows: two on a, one on b, none on c (model-less sub must
	// still get an empty non-nil PerModelConfigs map).
	if err := svc.UpsertModel("batch-a", "glm-5.2", 200000, 8192, "", "responses"); err != nil {
		t.Fatalf("UpsertModel a glm-5.2: %v", err)
	}
	if err := svc.UpsertModel("batch-a", "glm-5.2-air", 128000, 4096, "enabled", ""); err != nil {
		t.Fatalf("UpsertModel a glm-5.2-air: %v", err)
	}
	if err := svc.UpsertModel("batch-b", "deepseek-v4-pro", 131072, 16384, "", ""); err != nil {
		t.Fatalf("UpsertModel b deepseek: %v", err)
	}

	checkConfigs := func(subs []*LLMSubscription, wantIDs ...string) {
		t.Helper()
		if len(subs) != len(wantIDs) {
			t.Fatalf("got %d subs, want %d", len(subs), len(wantIDs))
		}
		byID := make(map[string]*LLMSubscription, len(subs))
		for _, s := range subs {
			byID[s.ID] = s
		}
		for _, id := range wantIDs {
			if byID[id] == nil {
				t.Fatalf("sub %s missing from result", id)
			}
		}
		if a := byID["batch-a"]; a != nil {
			if a.PerModelConfigs == nil {
				t.Fatalf("a PerModelConfigs is nil — batch loader must init the map")
			}
			if len(a.PerModelConfigs) != 2 {
				t.Errorf("a: got %d per-model configs, want 2 (%+v)", len(a.PerModelConfigs), a.PerModelConfigs)
			}
			if pmc, ok := a.PerModelConfigs["glm-5.2"]; !ok {
				t.Errorf("a: glm-5.2 missing from PerModelConfigs")
			} else if pmc.MaxContext != 200000 || pmc.MaxOutputTokens != 8192 || pmc.APIType != "responses" || !pmc.Enabled {
				t.Errorf("a glm-5.2 fields wrong: %+v", pmc)
			}
			if pmc, ok := a.PerModelConfigs["glm-5.2-air"]; !ok {
				t.Errorf("a: glm-5.2-air missing from PerModelConfigs")
			} else if pmc.MaxContext != 128000 || pmc.MaxOutputTokens != 4096 {
				t.Errorf("a glm-5.2-air fields wrong: %+v", pmc)
			}
		}
		if b := byID["batch-b"]; b != nil {
			if pmc, ok := b.PerModelConfigs["deepseek-v4-pro"]; !ok {
				t.Errorf("b: deepseek-v4-pro missing from PerModelConfigs")
			} else if pmc.MaxContext != 131072 || pmc.MaxOutputTokens != 16384 {
				t.Errorf("b deepseek fields wrong: %+v", pmc)
			}
			// Cross-contamination guard: a's models must NOT leak into b.
			if _, ok := b.PerModelConfigs["glm-5.2"]; ok {
				t.Errorf("b: glm-5.2 leaked from a (batch grouping broken)")
			}
		}
	}

	subsBySender, err := svc.List("cli_user")
	if err != nil {
		t.Fatalf("List(cli_user): %v", err)
	}
	checkConfigs(subsBySender, "batch-a", "batch-b", "batch-c")

	all, err := svc.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	checkConfigs(all, "batch-a", "batch-b", "batch-c")

	// c's sub has no models — PerModelConfigs must be empty map, not nil,
	// matching the per-sub loader's behavior for a model-less subscription.
	for _, s := range all {
		if s.ID == "batch-c" && s.PerModelConfigs == nil {
			t.Errorf("c model-less sub: PerModelConfigs nil, want empty map")
		}
	}
}

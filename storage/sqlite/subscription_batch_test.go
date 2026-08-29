package sqlite

import "testing"

// TestListByUserID_BatchedPerModelConfigs verifies List/ListByUserID/ListAll
// populate PerModelConfigs via the single batched subscription_models query
// (loadPerModelConfigsBatch), across MULTIPLE subscriptions each carrying
// multiple per-model rows. Regression guard for the N+1 → batched-IN fix:
// every sub's models must land in the right PerModelConfigs map with the
// right fields, and subs with no models keep an empty (non-nil) map.
func TestListByUserID_BatchedPerModelConfigs(t *testing.T) {
	db := openTestDB(t)
	svc := NewLLMSubscriptionService(db)

	// Two subs owned by canonical user 7, one by user 8, plus one system sub.
	owned1 := &LLMSubscription{ID: "batch-u7a", SenderID: "web-7a", Name: "u7a", Provider: "openai", BaseURL: "http://a", APIKey: "sk"}
	owned2 := &LLMSubscription{ID: "batch-u7b", SenderID: "web-7b", Name: "u7b", Provider: "openai", BaseURL: "http://b", APIKey: "sk"}
	other := &LLMSubscription{ID: "batch-u8", SenderID: "web-8", Name: "u8", Provider: "openai", BaseURL: "http://c", APIKey: "sk"}
	for _, sub := range []*LLMSubscription{owned1, owned2, other} {
		if err := svc.Add(sub); err != nil {
			t.Fatalf("Add %s: %v", sub.ID, err)
		}
	}
	if err := svc.SetSubscriptionUserID("batch-u7a", 7); err != nil {
		t.Fatalf("SetSubscriptionUserID u7a: %v", err)
	}
	if err := svc.SetSubscriptionUserID("batch-u7b", 7); err != nil {
		t.Fatalf("SetSubscriptionUserID u7b: %v", err)
	}
	if err := svc.SetSubscriptionUserID("batch-u8", 8); err != nil {
		t.Fatalf("SetSubscriptionUserID u8: %v", err)
	}

	// Per-model rows: two on u7a, one on u7b, none on u8 (model-less sub must
	// still get an empty non-nil PerModelConfigs map).
	if err := svc.UpsertModel("batch-u7a", "glm-5.2", 200000, 8192, "", "responses"); err != nil {
		t.Fatalf("UpsertModel u7a glm-5.2: %v", err)
	}
	if err := svc.UpsertModel("batch-u7a", "glm-5.2-air", 128000, 4096, "enabled", ""); err != nil {
		t.Fatalf("UpsertModel u7a glm-5.2-air: %v", err)
	}
	if err := svc.UpsertModel("batch-u7b", "deepseek-v4-pro", 131072, 16384, "", ""); err != nil {
		t.Fatalf("UpsertModel u7b deepseek: %v", err)
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
		if u7a := byID["batch-u7a"]; u7a != nil {
			if u7a.PerModelConfigs == nil {
				t.Fatalf("u7a PerModelConfigs is nil — batch loader must init the map")
			}
			if len(u7a.PerModelConfigs) != 2 {
				t.Errorf("u7a: got %d per-model configs, want 2 (%+v)", len(u7a.PerModelConfigs), u7a.PerModelConfigs)
			}
			if pmc, ok := u7a.PerModelConfigs["glm-5.2"]; !ok {
				t.Errorf("u7a: glm-5.2 missing from PerModelConfigs")
			} else if pmc.MaxContext != 200000 || pmc.MaxOutputTokens != 8192 || pmc.APIType != "responses" || !pmc.Enabled {
				t.Errorf("u7a glm-5.2 fields wrong: %+v", pmc)
			}
			if pmc, ok := u7a.PerModelConfigs["glm-5.2-air"]; !ok {
				t.Errorf("u7a: glm-5.2-air missing from PerModelConfigs")
			} else if pmc.MaxContext != 128000 || pmc.MaxOutputTokens != 4096 {
				t.Errorf("u7a glm-5.2-air fields wrong: %+v", pmc)
			}
		}
		if u7b := byID["batch-u7b"]; u7b != nil {
			if pmc, ok := u7b.PerModelConfigs["deepseek-v4-pro"]; !ok {
				t.Errorf("u7b: deepseek-v4-pro missing from PerModelConfigs")
			} else if pmc.MaxContext != 131072 || pmc.MaxOutputTokens != 16384 {
				t.Errorf("u7b deepseek fields wrong: %+v", pmc)
			}
			// Cross-contamination guard: u7a's models must NOT leak into u7b.
			if _, ok := u7b.PerModelConfigs["glm-5.2"]; ok {
				t.Errorf("u7b: glm-5.2 leaked from u7a (batch grouping broken)")
			}
		}
	}

	subsByUser, err := svc.ListByUserID(7)
	if err != nil {
		t.Fatalf("ListByUserID(7): %v", err)
	}
	// No system subscription upserted in this test — only user_id=7 subs.
	checkConfigs(subsByUser, "batch-u7a", "batch-u7b")

	subsBySender, err := svc.List("web-7a")
	if err != nil {
		t.Fatalf("List(web-7a): %v", err)
	}
	checkConfigs(subsBySender, "batch-u7a")

	all, err := svc.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	checkConfigs(all, "batch-u7a", "batch-u7b", "batch-u8")

	// u8's sub has no models — PerModelConfigs must be empty map, not nil,
	// matching the per-sub loader's behavior for a model-less subscription.
	for _, s := range all {
		if s.ID == "batch-u8" && s.PerModelConfigs == nil {
			t.Errorf("u8 model-less sub: PerModelConfigs nil, want empty map")
		}
	}
}

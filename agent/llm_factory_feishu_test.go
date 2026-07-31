package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"xbot/storage/sqlite"
)

// TestListAllModelEntries_EmptyModel_WithCachedModels verifies that when a
// subscription has Model="" (like feishu /set-llm without model= param) but
// CachedModels is populated (from a /models API fetch), the model correctly
// appears in ListAllModelEntriesForUser.
//
// This is the feishu scenario: user does /set-llm glm-h20 base_url=... api_key=...
// without specifying model=, leaving sub.Model="". After /models refresh
// populates CachedModels, the model should appear.
func TestListAllModelEntries_EmptyModel_WithCachedModels(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)
	sub := &sqlite.LLMSubscription{
		ID:       "sub-feishu",
		SenderID: "ou_feishu_user",
		Name:     "glm-h20",
		Provider: "openai",
		BaseURL:  "http://10.0.0.1:8000/v1/",
		APIKey:   "sk-glm52-h20",
		Model:    "", // empty — like feishu /set-llm without model=
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Simulate what OnModelsLoaded would do after a successful /models fetch:
	// upsert into subscription_models (no more CachedModels column writes).
	fetchedModels := []string{"/data/models/skyai/GLM-5.2-W4AFP8"}
	for _, m := range fetchedModels {
		if err := subSvc.UpsertModel(sub.ID, m, 0, 0, "", ""); err != nil {
			t.Fatalf("UpsertModel: %v", err)
		}
	}

	// Verify it persisted to DB via subscription_models
	rows, _ := subSvc.GetModels(sub.ID)
	if len(rows) != 1 || rows[0].Model != "/data/models/skyai/GLM-5.2-W4AFP8" {
		t.Fatalf("subscription_models = %v, want [/data/models/skyai/GLM-5.2-W4AFP8]", rows)
	}

	// Now check ListAllModelEntriesForUser shows the model
	entries := f.ListAllModelEntriesForUser("ou_feishu_user")
	found := false
	for _, e := range entries {
		if e.Model == "/data/models/skyai/GLM-5.2-W4AFP8" && e.SubName == "glm-h20" {
			found = true
			if e.Status != "normal" {
				t.Errorf("status = %q, want normal", e.Status)
			}
		}
	}
	if !found {
		t.Errorf("model /data/models/skyai/GLM-5.2-W4AFP8 not found in entries: %+v", entries)
	}
}

// TestRefreshModelEntries_PopulatesCachedModels verifies the full refresh flow
// with a real HTTP server: RefreshModelEntriesForUserWithResults should fetch
// /models, persist via OnModelsLoaded → UpdateCachedModels, and the model
// should appear in the returned entries.
func TestRefreshModelEntries_PopulatesCachedModels(t *testing.T) {
	// Start a mock /models endpoint
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := map[string]any{
			"object": "list",
			"data": []map[string]any{
				{
					"id":       "/data/models/skyai/GLM-5.2-W4AFP8",
					"object":   "model",
					"owned_by": "sglang",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	f, subSvc, _ := newModelFirstTestFactory(t)
	sub := &sqlite.LLMSubscription{
		ID:       "sub-refresh",
		SenderID: "ou_feishu_user",
		Name:     "glm-h20",
		Provider: "openai",
		BaseURL:  server.URL + "/v1",
		APIKey:   "sk-test",
		Model:    "", // empty — the feishu scenario
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Before refresh: no entries
	before := f.ListAllModelEntriesForUser("ou_feishu_user")
	if len(before) != 0 {
		t.Fatalf("before refresh, expected 0 entries, got %d: %+v", len(before), before)
	}

	// Refresh
	entries, results := f.RefreshModelEntriesForUserWithResults("ou_feishu_user")
	if len(results) == 0 {
		t.Fatal("no refresh results")
	}
	for _, r := range results {
		if r.SubName == "glm-h20" && r.Status != "ok" {
			t.Errorf("refresh status for glm-h20 = %q (err=%s), want ok", r.Status, r.Error)
		}
	}

	// After refresh: model should appear in entries
	found := false
	for _, e := range entries {
		if e.Model == "/data/models/skyai/GLM-5.2-W4AFP8" {
			found = true
			t.Logf("found entry: SubName=%s Model=%s Status=%s", e.SubName, e.Model, e.Status)
		}
	}
	if !found {
		t.Errorf("model not found in entries after refresh: %+v", entries)
	}

	// Verify models persisted to DB via subscription_models (UpsertModel in OnModelsLoaded)
	rows, _ := subSvc.GetModels(sub.ID)
	if len(rows) == 0 {
		t.Error("subscription_models still empty in DB after refresh")
	} else {
		t.Logf("subscription_models in DB: %d rows", len(rows))
	}
}

// TestRefreshModelEntries_EmptyModel_NoCachedModels verifies that when a
// subscription has Model="" and no CachedModels (never refreshed), it produces
// NO entries. This confirms the model is truly invisible until refresh succeeds.
func TestRefreshModelEntries_EmptyModel_NoCachedModels(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)
	sub := &sqlite.LLMSubscription{
		ID:       "sub-empty",
		SenderID: "ou_feishu_user",
		Name:     "glm-h20",
		Provider: "openai",
		BaseURL:  "http://127.0.0.1:1/v1", // unreachable
		APIKey:   "sk-test",
		Model:    "", // empty
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}

	entries := f.ListAllModelEntriesForUser("ou_feishu_user")
	for _, e := range entries {
		if e.SubID == sub.ID {
			t.Errorf("subscription with empty Model and no CachedModels should produce no entries, got %+v", e)
		}
	}
}

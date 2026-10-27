package storage

import (
	"context"
	"testing"

	"xbot/storage/pg"
	"xbot/storage/sqlite"
)

// TestPGSubscriptionCachedModels is the regression test for
//
//	ERROR: null value in column "cached_models" of relation
//	"user_llm_subscriptions" violates not-null constraint (SQLSTATE 23502)
//
// Two defects combined to break every "edit subscription" request on PG:
//
//  1. GetSubscription never decoded cached_models, so the struct a caller loads
//     and writes back carried a nil []string.
//  2. UpdateSubscription passed that []string straight into the TEXT column.
//     pgx encodes a nil slice as SQL NULL (→ 23502) and a non-nil one as an
//     array (→ "unable to encode []string into text"), so no value worked.
//
// The fix stops Update from touching the column at all, matching SQLite, where
// LLMSubscriptionService.Update never writes it either — editing a name must not
// wipe the API-discovered model cache.
func TestPGSubscriptionCachedModels(t *testing.T) {
	pool := newPGTestPool(t)
	ctx := context.Background()

	store, err := pg.NewPGStorageWithPool(pool)
	if err != nil {
		t.Fatalf("NewPGStorageWithPool: %v", err)
	}
	cs := store.Config()

	const subID = "sub-cached-models"
	// Seed through raw SQL: the public interface cannot write cached_models (by
	// design — it is API-discovered state), so the test sets it the way the
	// runtime discovery path does.
	if _, err := pool.Exec(ctx, `INSERT INTO user_llm_subscriptions
		(id, sender_id, name, provider, base_url, api_key, model, max_context,
		 max_output_tokens, thinking_mode, cached_models, api_type, enabled)
		VALUES ($1,'sender-1','Sub','openai','https://api.example.com','sk-x','gpt-4o',
		 128000, 4096, '', '["gpt-4o","o3"]', '', true)`, subID); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	// (1) The read path must decode the column.
	sub, err := cs.Get(subID)
	if err != nil {
		t.Fatalf("Get(%q): %v", subID, err)
	}
	if sub == nil {
		t.Fatalf("Get(%q) = nil", subID)
	}
	if len(sub.CachedModels) != 2 || sub.CachedModels[0] != "gpt-4o" || sub.CachedModels[1] != "o3" {
		t.Fatalf("Get.CachedModels = %v, want [gpt-4o o3]", sub.CachedModels)
	}

	// (2) Editing through the RPC path: load, rename, write back. This is the
	// exact sequence that failed with 23502.
	sub.Name = "Renamed"
	if err := cs.Update(sub); err != nil {
		t.Fatalf("Update after edit: %v", err)
	}

	sub2, err := cs.Get(subID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if sub2.Name != "Renamed" {
		t.Errorf("Name = %q, want Renamed", sub2.Name)
	}
	// (3) The cache must survive an edit — parity with SQLite.
	if len(sub2.CachedModels) != 2 {
		t.Errorf("CachedModels after Update = %v, want the seeded list preserved", sub2.CachedModels)
	}

	// (4) A caller that hands Update a struct with no cache loaded must still
	// succeed (this is what nil []string used to do: SQL NULL → 23502).
	bare := &sqlite.LLMSubscription{
		ID: subID, Name: "Bare Edit", Provider: "openai",
		BaseURL: "https://api.example.com", APIKey: "sk-x", Model: "gpt-4o",
		MaxContext: 128000, MaxOutputTokens: 4096, Enabled: true,
	}
	if err := cs.Update(bare); err != nil {
		t.Fatalf("Update with nil CachedModels: %v", err)
	}
	sub3, err := cs.Get(subID)
	if err != nil {
		t.Fatalf("Get after bare Update: %v", err)
	}
	if sub3.Name != "Bare Edit" {
		t.Errorf("Name = %q, want Bare Edit", sub3.Name)
	}
	if len(sub3.CachedModels) != 2 {
		t.Errorf("CachedModels after bare Update = %v, want preserved", sub3.CachedModels)
	}

	// (5) AddSubscription must write a valid (non-NULL) value even when the
	// caller supplies no cache.
	if err := cs.Add(&sqlite.LLMSubscription{
		ID: "sub-no-cache", SenderID: "sender-1", Name: "NoCache", Provider: "openai",
		BaseURL: "https://api.example.com", APIKey: "sk-y", Model: "gpt-4o", Enabled: true,
	}); err != nil {
		t.Fatalf("Add without CachedModels: %v", err)
	}
	var cached string
	if err := pool.QueryRow(ctx, `SELECT cached_models FROM user_llm_subscriptions WHERE id='sub-no-cache'`).Scan(&cached); err != nil {
		t.Fatalf("read cached_models: %v", err)
	}
	if cached != "" {
		t.Errorf("Add stored cached_models=%q, want empty string (not NULL, not \"null\")", cached)
	}
}

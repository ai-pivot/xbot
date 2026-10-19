package storage

import (
	"context"
	"fmt"
	"testing"

	"xbot/storage/agentmem"
)

// testAgentMemoryStoreContract exercises the official xbot provider's storage
// seam (AgentMemoryStore) against every backend. It pins the semantics the
// provider relies on, and — crucially — that SEARCH RANKING means the same thing
// on both backends even though the underlying scores differ in sign and scale
// (SQLite bm25 is negative/lower-is-better, PG ts_rank is positive/higher-is-better).
//
// Isolation: rows are scoped by user_id, and each subtest uses a unique user id,
// so no cleanup is needed and no subtest can observe another's rows.
func testAgentMemoryStoreContract(t *testing.T, factory StorageFactory) {
	s := factory(t)
	defer s.Close()
	ctx := context.Background()
	mem := s.Memory()

	// The provider calls EnsureAgentMemorySchema when it is constructed, so the
	// tables exist before any read/write. The contract starts from the same state
	// (and this doubles as the idempotency check on a fresh database).
	if err := mem.EnsureAgentMemorySchema(ctx); err != nil {
		t.Fatalf("EnsureAgentMemorySchema on a fresh database: %v", err)
	}

	// uniqueUser gives each subtest its own isolation scope.
	uniqueUser := func(t *testing.T) int64 {
		var n int64
		for _, r := range t.Name() {
			n = n*31 + int64(r)
		}
		if n < 0 {
			n = -n
		}
		return n%1_000_000 + 1
	}

	t.Run("LongTerm_AddSearchRoundTrip", func(t *testing.T) {
		user := uniqueUser(t)
		id, err := mem.AddAgentLongTermMemory(ctx, agentmem.LongTermMemory{
			UserID: user, Type: "fact", Content: "the cluster runs on B300 GPUs",
			Keywords: "cluster gpu", Importance: 0.8, SearchText: "the cluster runs on B300 GPUs cluster gpu",
		})
		if err != nil {
			t.Fatalf("AddAgentLongTermMemory: %v", err)
		}
		if id <= 0 {
			t.Fatalf("AddAgentLongTermMemory returned id %d, want > 0", id)
		}
		got, err := mem.GetAgentLongTermMemory(ctx, user, 0, id)
		if err != nil {
			t.Fatalf("GetAgentLongTermMemory: %v", err)
		}
		if got.Content != "the cluster runs on B300 GPUs" || got.Type != "fact" {
			t.Errorf("round-trip mismatch: %+v", got)
		}
		if got.Scope != "global" {
			t.Errorf("scope = %q, want the default \"global\"", got.Scope)
		}
	})

	t.Run("LongTerm_SearchRanksBestMatchFirst", func(t *testing.T) {
		user := uniqueUser(t)
		// Two rows; only the first contains the query term strongly.
		if _, err := mem.AddAgentLongTermMemory(ctx, agentmem.LongTermMemory{
			UserID: user, Type: "fact", Content: "frpc tunnel exposes port 9000",
			Keywords: "frpc tunnel", SearchText: "frpc tunnel exposes port 9000",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := mem.AddAgentLongTermMemory(ctx, agentmem.LongTermMemory{
			UserID: user, Type: "fact", Content: "unrelated note about coffee",
			Keywords: "coffee", SearchText: "unrelated note about coffee",
		}); err != nil {
			t.Fatal(err)
		}
		rows, err := mem.SearchAgentLongTermMemories(ctx, agentmem.Query{
			UserID: user, Terms: []string{"frpc"}, Join: agentmem.JoinAny,
			Limit: 5, Order: agentmem.OrderRelevance,
		})
		if err != nil {
			t.Fatalf("SearchAgentLongTermMemories: %v", err)
		}
		if len(rows) == 0 {
			t.Fatal("relevance search returned no rows for a term that exists")
		}
		// The ordering contract: best match first, on BOTH backends, even though
		// bm25 is negative (lower better) and ts_rank positive (higher better).
		if rows[0].Content != "frpc tunnel exposes port 9000" {
			t.Errorf("best match not first: got %q (all: %v)", rows[0].Content, contents(rows))
		}
	})

	t.Run("LongTerm_SupersedeHidesOldFacts", func(t *testing.T) {
		user := uniqueUser(t)
		seedNoiseCorpus(t, mem, user, 40)
		oldID, err := mem.AddAgentLongTermMemory(ctx, agentmem.LongTermMemory{
			UserID: user, Type: "fact", Content: "cluster is at 8.222.11.182",
			Keywords: "cluster ip", SearchText: "cluster is at 8.222.11.182",
		})
		if err != nil {
			t.Fatal(err)
		}
		newID, superseded, err := mem.AddAgentLongTermMemoryWithSupersede(ctx, agentmem.LongTermMemory{
			UserID: user, Type: "fact", Content: "cluster moved to 10.0.0.5",
			Keywords: "cluster ip", SearchText: "cluster moved to 10.0.0.5", Importance: 0.9,
		}, []string{"cluster", "ip"})
		if err != nil {
			t.Fatalf("AddAgentLongTermMemoryWithSupersede: %v", err)
		}
		if len(superseded) == 0 {
			t.Fatalf("expected the older contradicting fact to be superseded (new=%d old=%d)", newID, oldID)
		}
		// The old row must disappear from ACTIVE views...
		active, err := mem.ListAgentLongTermMemories(ctx, agentmem.Query{UserID: user, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range active {
			if r.ID == oldID {
				t.Errorf("superseded row %d still listed as active", oldID)
			}
		}
		// ...but stay in the table for rollback.
		if got, err := mem.GetAgentLongTermMemory(ctx, user, 0, oldID); err != nil || got.SupersededBy != newID {
			t.Errorf("superseded row must be preserved with superseded_by=%d, got %+v err=%v", newID, got, err)
		}
	})

	t.Run("LongTerm_UserIsolationNeverTenantFallback", func(t *testing.T) {
		userA, userB := uniqueUser(t), uniqueUser(t)+7
		if _, err := mem.AddAgentLongTermMemory(ctx, agentmem.LongTermMemory{
			UserID: userA, Type: "fact", Content: "owner A secret", SearchText: "owner a secret",
		}); err != nil {
			t.Fatal(err)
		}
		rows, err := mem.ListAgentLongTermMemories(ctx, agentmem.Query{UserID: userB, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.Content == "owner A secret" {
				t.Fatal("user B can see user A's memory — isolation must be by user_id")
			}
		}
		// user_id=0 must match ONLY ownerless rows, never everything.
		ownerless, err := mem.ListAgentLongTermMemories(ctx, agentmem.Query{UserID: 0, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range ownerless {
			if r.UserID != 0 {
				t.Fatalf("user_id=0 query returned a row owned by %d — tenant fallback is forbidden", r.UserID)
			}
		}
	})

	t.Run("LongTerm_ScopeFiltering", func(t *testing.T) {
		user := uniqueUser(t)
		if _, err := mem.AddAgentLongTermMemory(ctx, agentmem.LongTermMemory{
			UserID: user, Type: "fact", Content: "global fact", Scope: "global", SearchText: "global fact",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := mem.AddAgentLongTermMemory(ctx, agentmem.LongTermMemory{
			UserID: user, Type: "task", Content: "session fact", Scope: "session",
			SourceSession: "sess-1", SearchText: "session fact",
		}); err != nil {
			t.Fatal(err)
		}
		global, err := mem.SearchAgentLongTermMemories(ctx, agentmem.Query{
			UserID: user, Scope: "global", Limit: 10, Order: agentmem.OrderRecency,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range global {
			if r.Scope != "global" {
				t.Errorf("scope=global query returned scope=%q", r.Scope)
			}
		}
		scoped, err := mem.SearchAgentLongTermMemories(ctx, agentmem.Query{
			UserID: user, Scope: "session", SourceSession: "sess-1", Limit: 10, Order: agentmem.OrderRecency,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(scoped) != 1 || scoped[0].Content != "session fact" {
			t.Errorf("session-scope filter returned %v, want exactly the session fact", contents(scoped))
		}
	})

	t.Run("LongTerm_DedupFindsSimilar", func(t *testing.T) {
		user := uniqueUser(t)
		seedNoiseCorpus(t, mem, user, 40)
		if _, err := mem.AddAgentLongTermMemory(ctx, agentmem.LongTermMemory{
			UserID: user, Type: "fact", Content: "deploys with GLM model",
			Keywords: "glm model", SearchText: "deploys with GLM model glm model",
		}); err != nil {
			t.Fatal(err)
		}
		dup, err := mem.FindAgentDuplicateLongTerm(ctx, agentmem.Query{
			UserID: user, Terms: []string{"glm", "model"}, Join: agentmem.JoinAll, SourceSession: "",
		})
		if err != nil {
			t.Fatalf("FindAgentDuplicateLongTerm: %v", err)
		}
		if dup == 0 {
			t.Error("strongly matching terms must be detected as a duplicate")
		}
		// A non-matching term set must NOT be reported as duplicate.
		none, err := mem.FindAgentDuplicateLongTerm(ctx, agentmem.Query{
			UserID: user, Terms: []string{"kubernetes", "ingress"}, Join: agentmem.JoinAll,
		})
		if err != nil {
			t.Fatal(err)
		}
		if none != 0 {
			t.Errorf("unrelated terms reported duplicate id %d", none)
		}
	})

	t.Run("LongTerm_UpdateDeleteTouch", func(t *testing.T) {
		user := uniqueUser(t)
		id, err := mem.AddAgentLongTermMemory(ctx, agentmem.LongTermMemory{
			UserID: user, Type: "fact", Content: "before", SearchText: "before",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := mem.UpdateAgentLongTermMemory(ctx, agentmem.LongTermMemory{
			ID: id, UserID: user, Type: "fact", Content: "after", Keywords: "k", SearchText: "after k",
		}); err != nil {
			t.Fatalf("UpdateAgentLongTermMemory: %v", err)
		}
		got, err := mem.GetAgentLongTermMemory(ctx, user, 0, id)
		if err != nil || got.Content != "after" {
			t.Errorf("update not applied: %+v err=%v", got, err)
		}
		if err := mem.TouchAgentLongTermMemory(ctx, id); err != nil {
			t.Fatalf("TouchAgentLongTermMemory: %v", err)
		}
		got, _ = mem.GetAgentLongTermMemory(ctx, user, 0, id)
		if got.AccessCount != 1 {
			t.Errorf("access_count = %d, want 1 after one touch", got.AccessCount)
		}
		if err := mem.DeleteAgentLongTermMemory(ctx, user, 0, id); err != nil {
			t.Fatalf("DeleteAgentLongTermMemory: %v", err)
		}
		if _, err := mem.GetAgentLongTermMemory(ctx, user, 0, id); err == nil {
			t.Error("deleted memory must not be readable")
		}
	})

	t.Run("LongTerm_PruneKeepsHighestHeat", func(t *testing.T) {
		user := uniqueUser(t)
		var ids []int64
		for i, imp := range []float64{0.1, 0.9, 0.5} {
			id, err := mem.AddAgentLongTermMemory(ctx, agentmem.LongTermMemory{
				UserID: user, Type: "fact", Content: fmt.Sprintf("prune-candidate-%d", i),
				Importance: imp, SearchText: fmt.Sprintf("prune candidate %d", i),
			})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		if err := mem.PruneAgentLongTerm(ctx, user, 0, 1); err != nil {
			t.Fatalf("PruneAgentLongTerm: %v", err)
		}
		left, err := mem.ListAgentLongTermMemories(ctx, agentmem.Query{UserID: user, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(left) != 1 {
			t.Fatalf("prune left %d rows, want 1 (kept: %v)", len(left), contents(left))
		}
		// heat_score is 1.0 for every fresh row, so the tie is broken by
		// importance: the 0.9 row must survive.
		if left[0].Content != "prune-candidate-1" {
			t.Errorf("prune kept %q, want the highest-importance row", left[0].Content)
		}
		_ = ids
	})

	t.Run("ShortTerm_AddSearchEvict", func(t *testing.T) {
		user := uniqueUser(t)
		for i := 0; i < 3; i++ {
			if _, err := mem.AddAgentShortTermMemory(ctx, agentmem.ShortTermMemory{
				UserID: user, SessionID: fmt.Sprintf("sess-%d", i),
				Summary:   fmt.Sprintf("summary %d about frpc tunnels", i),
				KeyTopics: "frpc tunnel",
			}); err != nil {
				t.Fatalf("AddAgentShortTermMemory: %v", err)
			}
		}
		rows, err := mem.SearchAgentShortTermMemories(ctx, agentmem.Query{UserID: user, Limit: 10})
		if err != nil {
			t.Fatalf("SearchAgentShortTermMemories: %v", err)
		}
		if len(rows) != 3 {
			t.Fatalf("short-term rows = %d, want 3", len(rows))
		}
		if err := mem.EvictAgentShortTermMemories(ctx, user, 0, 1); err != nil {
			t.Fatalf("EvictAgentShortTermMemories: %v", err)
		}
		rows, err = mem.SearchAgentShortTermMemories(ctx, agentmem.Query{UserID: user, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Errorf("after evict(keep=1) rows = %d, want 1", len(rows))
		}
	})

	t.Run("Schema_IsIdempotent", func(t *testing.T) {
		// EnsureAgentMemorySchema runs on every provider construction, so a second
		// call must be a no-op on both backends (PG: tables come from initPGSchema).
		if err := mem.EnsureAgentMemorySchema(ctx); err != nil {
			t.Fatalf("EnsureAgentMemorySchema (1st): %v", err)
		}
		if err := mem.EnsureAgentMemorySchema(ctx); err != nil {
			t.Fatalf("EnsureAgentMemorySchema (2nd, must be idempotent): %v", err)
		}
	})

	t.Run("SearchText_BackfillDriver", func(t *testing.T) {
		user := uniqueUser(t)
		id, err := mem.AddAgentLongTermMemory(ctx, agentmem.LongTermMemory{
			UserID: user, Type: "fact", Content: "legacy row", SearchText: "",
		})
		if err != nil {
			t.Fatal(err)
		}
		missing, err := mem.ListAgentLongTermMissingSearchText(ctx, user, 0, 10)
		if err != nil {
			t.Fatalf("ListAgentLongTermMissingSearchText: %v", err)
		}
		found := false
		for _, r := range missing {
			if r.ID == id {
				found = true
			}
		}
		if !found {
			t.Errorf("row with empty search_text not reported for backfill (got %d rows)", len(missing))
		}
		if err := mem.SetAgentLongTermSearchText(ctx, id, "legacy row backfilled"); err != nil {
			t.Fatalf("SetAgentLongTermSearchText: %v", err)
		}
		missing, err = mem.ListAgentLongTermMissingSearchText(ctx, user, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range missing {
			if r.ID == id {
				t.Error("row still reported after search_text was set")
			}
		}
	})
}

// seedNoiseCorpus adds unrelated rows so a text-similarity threshold that depends
// on corpus statistics behaves realistically.
//
// Measured on SQLite FTS5 (bm25 is corpus-scaled): a perfect match scores ~0 with
// 2 rows, -5.99 with 20 rows, -7.53 with 40 rows — so the provider's dedup
// threshold (-6.0, i.e. "strong keyword overlap") only becomes reachable once the
// corpus is realistically sized. Asserting dedup against a 2-row corpus would
// test an artefact of corpus size rather than the contract. PG ts_rank is
// per-document normalized, so it does not need this, but the same corpus keeps
// both backends comparable.
func seedNoiseCorpus(t *testing.T, mem AgentMemoryStore, user int64, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := mem.AddAgentLongTermMemory(context.Background(), agentmem.LongTermMemory{
			UserID:     user,
			Type:       "noise",
			Content:    fmt.Sprintf("noise entry %d about unrelated matters %d", i, i),
			Keywords:   fmt.Sprintf("noise%d", i),
			SearchText: fmt.Sprintf("noise entry %d about unrelated matters %d", i, i),
		}); err != nil {
			t.Fatalf("seed noise row %d: %v", i, err)
		}
	}
}

func contents(rows []agentmem.LongTermMemory) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Content
	}
	return out
}

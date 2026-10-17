package storage

import (
	"context"
	"strings"
	"testing"

	"xbot/storage/sqlite"
)

// testEdgeCasesContract covers the cross-cutting edge cases of the Storage
// contract that do not belong to a single store: non-ASCII content
// round-trips, the "a missing row yields (nil, nil), never an error"
// convention shared by every lookup surface, and cascade deletion of a tenant
// across every table.
//
// It runs against every backend (see sqlite_contract_test.go and
// pg_contract_test.go) so these semantics stay identical across implementations.
//
// Isolation: every subtest mints its own tenant via uniqueChatID, so the shared
// Storage instance never lets one subtest observe another's rows (the cascade
// subtest deletes a tenant outright, which would otherwise wipe siblings' data).
func testEdgeCasesContract(t *testing.T, factory StorageFactory) {
	s := factory(t)
	defer s.Close()

	t.Run("CJK_Content_RoundTrip", func(t *testing.T) {
		tenantID := createTestTenant(t, s, uniqueChatID(t))
		ss := getSessionStore(t, s, tenantID)

		// Mixed scripts: Chinese, a mathematical bold script (astral plane,
		// each rune a UTF-16 surrogate pair), and Japanese. A backend that
		// truncates or mis-encodes on the way through storage corrupts one of
		// the encodings below.
		const content = "你好世界 𝓕𝓲𝓸𝓲𝓵 日语テスト"

		if err := ss.AddMessage(tenantID, dummyMsg("user", content)); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}

		msgs, err := ss.GetHistory(tenantID, 10)
		if err != nil {
			t.Fatalf("GetHistory: %v", err)
		}
		if len(msgs) == 0 {
			t.Fatalf("GetHistory returned no messages after AddMessage")
		}
		if got := msgs[0].Content; got != content {
			t.Errorf("content round-trip = %q, want %q", got, content)
		}
	})

	t.Run("EmptyResult_GetSubscription_NonExistent", func(t *testing.T) {
		// Not-found is a normal outcome, not an error: callers branch on a nil
		// pointer, so a backend that returns an error here would break them.
		sub, err := s.Config().GetSubscription("nonexistent-id")
		if err != nil {
			t.Fatalf("GetSubscription(nonexistent) = (nil, %v), want (nil, nil)", err)
		}
		if sub != nil {
			t.Errorf("GetSubscription(nonexistent) = %+v, want nil", sub)
		}
	})

	t.Run("EmptyResult_GetCronJob_NonExistent", func(t *testing.T) {
		job, err := s.Config().GetCronJob("nonexistent-id")
		if err != nil {
			t.Fatalf("GetCronJob(nonexistent) = (nil, %v), want (nil, nil)", err)
		}
		if job != nil {
			t.Errorf("GetCronJob(nonexistent) = %+v, want nil", job)
		}
	})

	t.Run("EmptyResult_GetSharedArtifact_Revoked", func(t *testing.T) {
		cfg := s.Config()
		art := &sqlite.SharedArtifact{
			PluginID:    "test.edge",
			ContentType: "test/edge",
			Payload:     `{"edge":true}`,
			Title:       "edge case",
			CreatedBy:   424242,
		}
		if err := cfg.CreateSharedArtifact(art); err != nil {
			t.Fatalf("CreateSharedArtifact: %v", err)
		}
		if art.Token == "" {
			t.Fatalf("CreateSharedArtifact did not mint a token")
		}
		if err := cfg.RevokeSharedArtifact(art.Token, art.CreatedBy); err != nil {
			t.Fatalf("RevokeSharedArtifact: %v", err)
		}

		// A revoked token must resolve to no artifact. The GET handler treats
		// (nil, nil) exactly like (nil, err) — both become 404 — but the storage
		// contract is the stricter one: a missing/revoked row yields (nil, nil),
		// matching subscription and cron lookups above.
		got, err := cfg.GetSharedArtifact(art.Token)
		if err != nil {
			t.Fatalf("GetSharedArtifact(revoked) = (nil, %v), want (nil, nil)", err)
		}
		if got != nil {
			t.Errorf("GetSharedArtifact(revoked) = %+v, want nil", got)
		}
	})

	t.Run("DeleteTenant_CascadeAllTables", func(t *testing.T) {
		ctx := context.Background()
		tenantID := createTestTenant(t, s, uniqueChatID(t))
		ss := getSessionStore(t, s, tenantID)
		mem := s.Memory()

		// Seed every table that is keyed by tenant_id across all four stores.
		if err := mem.InitBlocks(tenantID, ""); err != nil {
			t.Fatalf("InitBlocks: %v", err)
		}
		if err := mem.SetBlock(tenantID, "working_context", "cascade sentinel", ""); err != nil {
			t.Fatalf("SetBlock(working_context): %v", err)
		}
		if err := mem.AppendHistory(ctx, tenantID, "edge entry"); err != nil {
			t.Fatalf("AppendHistory: %v", err)
		}
		if err := mem.WriteLongTerm(ctx, tenantID, "long term sentinel"); err != nil {
			t.Fatalf("WriteLongTerm: %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "cascade message")); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}

		// Preconditions: the rows really exist, so a zero *after* the delete
		// proves the cascade ran rather than that nothing was ever written.
		if blocks, err := mem.GetAllBlocks(tenantID, ""); err != nil {
			t.Fatalf("GetAllBlocks (pre-delete): %v", err)
		} else if len(blocks) == 0 {
			t.Fatalf("precondition failed: GetAllBlocks is empty before DeleteTenant")
		}
		if count, err := mem.GetHistoryCount(ctx, tenantID); err != nil {
			t.Fatalf("GetHistoryCount (pre-delete): %v", err)
		} else if count == 0 {
			t.Fatalf("precondition failed: GetHistoryCount == 0 before DeleteTenant")
		}
		if count, err := ss.GetMessagesCount(tenantID); err != nil {
			t.Fatalf("GetMessagesCount (pre-delete): %v", err)
		} else if count == 0 {
			t.Fatalf("precondition failed: GetMessagesCount == 0 before DeleteTenant")
		}
		if lt, err := mem.ReadLongTerm(ctx, tenantID); err != nil {
			t.Fatalf("ReadLongTerm (pre-delete): %v", err)
		} else if lt == "" {
			t.Fatalf("precondition failed: ReadLongTerm is empty before DeleteTenant")
		}

		if err := s.Tenant().DeleteTenant(tenantID); err != nil {
			t.Fatalf("DeleteTenant: %v", err)
		}

		blocks, err := mem.GetAllBlocks(tenantID, "")
		if err != nil {
			t.Fatalf("GetAllBlocks (post-delete): %v", err)
		}
		if len(blocks) != 0 {
			t.Errorf("GetAllBlocks after DeleteTenant = %d blocks (%v), want 0", len(blocks), blocks)
		}

		lt, err := mem.ReadLongTerm(ctx, tenantID)
		if err != nil {
			t.Fatalf("ReadLongTerm (post-delete): %v", err)
		}
		if lt != "" {
			t.Errorf("ReadLongTerm after DeleteTenant = %q, want empty", lt)
		}

		hcount, err := mem.GetHistoryCount(ctx, tenantID)
		if err != nil {
			t.Fatalf("GetHistoryCount (post-delete): %v", err)
		}
		if hcount != 0 {
			t.Errorf("GetHistoryCount after DeleteTenant = %d, want 0", hcount)
		}

		mcount, err := ss.GetMessagesCount(tenantID)
		if err != nil {
			t.Fatalf("GetMessagesCount (post-delete): %v", err)
		}
		if mcount != 0 {
			t.Errorf("GetMessagesCount after DeleteTenant = %d, want 0", mcount)
		}
	})

	t.Run("LongContent_RoundTrip", func(t *testing.T) {
		tenantID := createTestTenant(t, s, uniqueChatID(t))
		ss := getSessionStore(t, s, tenantID)

		content := strings.Repeat("A", 10*1024) // 10 KiB, larger than a page
		if err := ss.AddMessage(tenantID, dummyMsg("user", content)); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}

		msgs, err := ss.GetHistory(tenantID, 10)
		if err != nil {
			t.Fatalf("GetHistory: %v", err)
		}
		if len(msgs) == 0 {
			t.Fatalf("GetHistory returned no messages after AddMessage")
		}
		if got := msgs[0].Content; got != content {
			t.Errorf("long content round-trip mismatch: got %d bytes, want %d", len(got), len(content))
		}
	})
}

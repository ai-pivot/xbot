package storage

import (
	"context"
	"fmt"
	"testing"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// contract_cross_test.go — Cross-store contract tests
// Verify that operations across different stores are consistent
// (e.g., DeleteTenant cascades to session_messages + memory tables).

func testCrossStoreContract(t *testing.T, factory StorageFactory) {
	t.Run("CreateChat_ListUserChats_RoundTrip", func(t *testing.T) {
		s := factory(t)
		defer s.Close()

		chatID, err := s.Tenant().CreateChat("web", "cross-user", "Cross Test")
		if err != nil {
			t.Fatalf("CreateChat: %v", err)
		}

		chats, _, err := s.Tenant().ListUserChats("web", "cross-user", chatID, 0, 100)
		if err != nil {
			t.Fatalf("ListUserChats: %v", err)
		}
		found := false
		for _, c := range chats {
			if c.ChatID == chatID {
				found = true
				if c.Label != "Cross Test" {
					t.Errorf("label: got %q, want %q", c.Label, "Cross Test")
				}
			}
		}
		if !found {
			t.Fatal("CreateChat → ListUserChats: created chat not found")
		}
	})

	t.Run("DeleteTenant_CascadesAllTables", func(t *testing.T) {
		s := factory(t)
		defer s.Close()

		chatID := fmt.Sprintf("chat_cascade_%s", t.Name())
		tid, _ := s.Tenant().GetOrCreateTenantID("web", chatID)

		// Seed data across stores
		ss, _ := s.Session(tid)
		ss.AddMessage(tid, llm.ChatMessage{Role: "user", Content: "cascade test"})
		s.Memory().InitBlocks(tid, "cascade-user")
		s.Memory().SetBlock(tid, "persona", "p", "cascade-user")
		s.Memory().WriteLongTerm(context.Background(), tid, "lt content")
		s.Memory().AppendHistory(context.Background(), tid, `{"e":"h"}`)

		// Verify data exists
		count, _ := ss.GetMessagesCount(tid)
		if count == 0 {
			t.Fatal("expected session_messages before delete")
		}
		blocks, _ := s.Memory().GetAllBlocks(tid, "cascade-user")
		if blocks["persona"] != "p" {
			t.Fatal("expected core_memory_blocks before delete")
		}
		lt, _ := s.Memory().ReadLongTerm(context.Background(), tid)
		if lt != "lt content" {
			t.Fatal("expected long_term_memory before delete")
		}

		// Delete tenant — should cascade to ALL child tables
		if err := s.Tenant().DeleteTenant(tid); err != nil {
			t.Fatalf("DeleteTenant: %v", err)
		}

		// Verify cascade: session_messages gone
		count2, _ := ss.GetMessagesCount(tid)
		if count2 != 0 {
			t.Errorf("after DeleteTenant cascade: session_messages=%d, want 0", count2)
		}

		// Verify cascade: core_memory_blocks gone
		blocks2, _ := s.Memory().GetAllBlocks(tid, "cascade-user")
		if blocks2["persona"] != "" {
			t.Error("after DeleteTenant cascade: core_memory_blocks still has content")
		}

		// Verify cascade: long_term_memory gone
		lt2, _ := s.Memory().ReadLongTerm(context.Background(), tid)
		if lt2 != "" {
			t.Errorf("after DeleteTenant cascade: long_term_memory=%q, want empty", lt2)
		}

		// Verify cascade: event_history gone
		histCount, _ := s.Memory().GetHistoryCount(context.Background(), tid)
		if histCount != 0 {
			t.Errorf("after DeleteTenant cascade: event_history count=%d, want 0", histCount)
		}
	})

	t.Run("GetOrCreateTenantID_TouchTenantID", func(t *testing.T) {
		s := factory(t)
		defer s.Close()

		chatID := fmt.Sprintf("chat_touch_%s", t.Name())
		tid, _ := s.Tenant().GetOrCreateTenantID("web", chatID)

		if err := s.Tenant().TouchTenantID("web", chatID); err != nil {
			t.Errorf("TouchTenantID: %v", err)
		}

		found, _ := s.Tenant().GetTenantIDByChannelChatID("web", chatID)
		if found != tid {
			t.Errorf("after touch: found=%d, want %d", found, tid)
		}
	})

	t.Run("GetTenantInfo_AfterCreate", func(t *testing.T) {
		s := factory(t)
		defer s.Close()

		chatID := fmt.Sprintf("chat_info_%s", t.Name())
		tid, _ := s.Tenant().GetOrCreateTenantID("web", chatID)

		ch, cid, err := s.Tenant().GetTenantInfo(tid)
		if err != nil {
			t.Fatalf("GetTenantInfo: %v", err)
		}
		if ch != "web" {
			t.Errorf("channel: got %q, want %q", ch, "web")
		}
		if cid != chatID {
			t.Errorf("chatID: got %q, want %q", cid, chatID)
		}
	})
}

// testCopySessionFromContract covers the session-fork copy primitive.
//
// Fork is a data copy, not a transformation, so the contract is: everything the
// source holds arrives in order, control bookkeeping is flattened exactly like
// the old read-then-write path, and the source is untouched. A backend may
// implement it with `INSERT … SELECT` (no rows leave the database) — the
// contract is deliberately phrased over observable rows so both shapes pass.
func testCopySessionFromContract(t *testing.T, factory StorageFactory) {
	t.Run("CopySessionFrom", func(t *testing.T) {
		s := factory(t)
		defer s.Close()

		srcID := createTestTenant(t, s, uniqueChatID(t))
		dstID := createTestTenant(t, s, uniqueChatID(t))
		src := getSessionStore(t, s, srcID)
		dst := getSessionStore(t, s, dstID)

		// Source: a normal exchange, an empty assistant shell (must be skipped —
		// it is the one sanitize rule that fires in production), and iteration
		// history for one turn.
		msgs := []llm.ChatMessage{
			{Role: "user", Content: "问题一", TurnID: 1},
			{Role: "assistant", Content: "回答一", TurnID: 1},
			{Role: "assistant", Content: "", TurnID: 1}, // empty shell → skipped
			{Role: "user", Content: "问题二", TurnID: 2},
		}
		if _, err := src.AppendMessages(srcID, msgs); err != nil {
			t.Fatalf("seed source messages: %v", err)
		}
		recs := []sqlite.IterationRecord{
			{TurnID: 1, Iteration: 1, Content: "iter-1", Tokens: 5, Model: "m1"},
			{TurnID: 2, Iteration: 1, Content: "iter-2", Tokens: 6, Model: "m2"},
		}
		if err := src.AppendIterationHistories(srcID, recs); err != nil {
			t.Fatalf("seed source iterations: %v", err)
		}

		n, err := s.CopySessionFrom(dstID, srcID)
		if err != nil {
			t.Fatalf("CopySessionFrom: %v", err)
		}
		if n != 3 {
			t.Errorf("CopySessionFrom returned %d messages, want 3 (empty assistant shell skipped)", n)
		}

		got, err := dst.GetAllMessages(dstID)
		if err != nil {
			t.Fatalf("GetAllMessages(dst): %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("dst has %d messages, want 3: %+v", len(got), got)
		}
		for i, want := range []string{"问题一", "回答一", "问题二"} {
			if got[i].Content != want {
				t.Errorf("dst[%d].Content = %q, want %q (order must follow source id)", i, got[i].Content, want)
			}
		}
		// Turn ids are preserved so the forked turns keep their grouping.
		if got[2].TurnID != 2 {
			t.Errorf("dst[2].TurnID = %d, want 2", got[2].TurnID)
		}

		iters, err := dst.GetIterationHistoryByTurn(dstID, 1)
		if err != nil {
			t.Fatalf("GetIterationHistoryByTurn(dst): %v", err)
		}
		if len(iters) != 1 || iters[0].Content != "iter-1" {
			t.Errorf("dst turn 1 iterations = %+v, want the copied iter-1", iters)
		}

		// The source must be untouched (fork must not consume it).
		srcMsgs, err := src.GetAllMessages(srcID)
		if err != nil {
			t.Fatalf("GetAllMessages(src): %v", err)
		}
		if len(srcMsgs) != 4 {
			t.Errorf("source now has %d messages, want 4 (unchanged)", len(srcMsgs))
		}

		// Copying an empty session is a no-op, not an error.
		emptyID := createTestTenant(t, s, uniqueChatID(t))
		if n, err := s.CopySessionFrom(dstID, emptyID); err != nil || n != 0 {
			t.Errorf("CopySessionFrom(empty) = (%d, %v), want (0, nil)", n, err)
		}
	})
}

package storage

import (
	"context"
	"fmt"
	"testing"

	"xbot/llm"
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

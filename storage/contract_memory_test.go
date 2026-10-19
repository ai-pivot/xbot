package storage

import (
	"context"
	"testing"
	"time"
)

// testMemoryStoreContract exercises the MemoryStore half of the Storage
// contract: core memory blocks, long-term memory, consolidation state, token
// state and event history. It runs against every backend (see
// sqlite_contract_test.go and pg_contract_test.go) so the semantics of these
// domains stay identical across implementations.
//
// Isolation: one tenant is created for the whole suite and the subtests run
// sequentially (Go executes non-parallel subtests in declaration order). The
// block subtests deliberately share the default "test-user" user id because
// the cross-tenant human block is keyed by (tenant_id=0, user_id) — a fresh
// factory per suite (fresh DB) is what keeps that shared row from leaking
// between suites.
func testMemoryStoreContract(t *testing.T, factory StorageFactory) {
	s := factory(t)
	defer s.Close()
	tenantID := createTestTenant(t, s, uniqueChatID(t))
	ctx := context.Background()
	mem := s.Memory()

	// ── P0 ──

	t.Run("InitBlocks_GetBlock_SetBlock", func(t *testing.T) {
		if err := mem.InitBlocks(tenantID, "test-user"); err != nil {
			t.Fatalf("InitBlocks: %v", err)
		}

		content, charLimit, err := mem.GetBlock(tenantID, "persona", "test-user")
		if err != nil {
			t.Fatalf("GetBlock(persona) after InitBlocks: %v", err)
		}
		if content != "" {
			t.Errorf("GetBlock(persona) content = %q, want empty", content)
		}
		if charLimit != 2000 {
			t.Errorf("GetBlock(persona) charLimit = %d, want 2000", charLimit)
		}

		if err := mem.SetBlock(tenantID, "persona", "I am test", "test-user"); err != nil {
			t.Fatalf("SetBlock(persona): %v", err)
		}

		content, charLimit, err = mem.GetBlock(tenantID, "persona", "test-user")
		if err != nil {
			t.Fatalf("GetBlock(persona) after SetBlock: %v", err)
		}
		if content != "I am test" {
			t.Errorf("GetBlock(persona) content = %q, want %q", content, "I am test")
		}
		if charLimit != 2000 {
			t.Errorf("GetBlock(persona) charLimit = %d, want 2000", charLimit)
		}
	})

	t.Run("GetAllBlocks_ClearBlock_ClearAllBlocks", func(t *testing.T) {
		if err := mem.InitBlocks(tenantID, "test-user"); err != nil {
			t.Fatalf("InitBlocks: %v", err)
		}
		if err := mem.SetBlock(tenantID, "persona", "persona text", "test-user"); err != nil {
			t.Fatalf("SetBlock(persona): %v", err)
		}
		if err := mem.SetBlock(tenantID, "human", "human text", "test-user"); err != nil {
			t.Fatalf("SetBlock(human): %v", err)
		}

		blocks, err := mem.GetAllBlocks(tenantID, "test-user")
		if err != nil {
			t.Fatalf("GetAllBlocks: %v", err)
		}
		if len(blocks) < 2 {
			t.Fatalf("GetAllBlocks returned %d blocks, want >= 2", len(blocks))
		}
		if blocks["persona"] != "persona text" {
			t.Errorf("blocks[persona] = %q, want %q", blocks["persona"], "persona text")
		}
		if blocks["human"] != "human text" {
			t.Errorf("blocks[human] = %q, want %q", blocks["human"], "human text")
		}

		if err := mem.ClearBlock(tenantID, "persona", "test-user"); err != nil {
			t.Fatalf("ClearBlock(persona): %v", err)
		}
		blocks, err = mem.GetAllBlocks(tenantID, "test-user")
		if err != nil {
			t.Fatalf("GetAllBlocks after ClearBlock: %v", err)
		}
		if got, ok := blocks["persona"]; !ok || got != "" {
			t.Errorf("blocks[persona] after ClearBlock = %q (present=%v), want %q", got, ok, "")
		}
		if blocks["human"] != "human text" {
			t.Errorf("blocks[human] after ClearBlock(persona) = %q, want %q (untouched)", blocks["human"], "human text")
		}

		if err := mem.ClearAllBlocks(tenantID, "test-user"); err != nil {
			t.Fatalf("ClearAllBlocks: %v", err)
		}
		blocks, err = mem.GetAllBlocks(tenantID, "test-user")
		if err != nil {
			t.Fatalf("GetAllBlocks after ClearAllBlocks: %v", err)
		}
		for name, content := range blocks {
			if content != "" {
				t.Errorf("blocks[%s] after ClearAllBlocks = %q, want empty", name, content)
			}
		}
	})

	t.Run("LongTerm_ReadWriteClear", func(t *testing.T) {
		if err := mem.WriteLongTerm(ctx, tenantID, "long term content"); err != nil {
			t.Fatalf("WriteLongTerm: %v", err)
		}

		got, err := mem.ReadLongTerm(ctx, tenantID)
		if err != nil {
			t.Fatalf("ReadLongTerm: %v", err)
		}
		if got != "long term content" {
			t.Errorf("ReadLongTerm = %q, want %q", got, "long term content")
		}

		if err := mem.ClearLongTerm(ctx, tenantID); err != nil {
			t.Fatalf("ClearLongTerm: %v", err)
		}
		got, err = mem.ReadLongTerm(ctx, tenantID)
		if err != nil {
			t.Fatalf("ReadLongTerm after ClearLongTerm: %v", err)
		}
		if got != "" {
			t.Errorf("ReadLongTerm after ClearLongTerm = %q, want empty", got)
		}
	})

	t.Run("State_GetSet", func(t *testing.T) {
		if err := mem.SetState(ctx, tenantID, 42); err != nil {
			t.Fatalf("SetState: %v", err)
		}

		got, err := mem.GetState(ctx, tenantID)
		if err != nil {
			t.Fatalf("GetState: %v", err)
		}
		if got != 42 {
			t.Errorf("GetState = %d, want 42", got)
		}
	})

	t.Run("EventHistory_AppendGetClearCount", func(t *testing.T) {
		if err := mem.AppendHistory(ctx, tenantID, "entry one"); err != nil {
			t.Fatalf("AppendHistory(one): %v", err)
		}
		if err := mem.AppendHistory(ctx, tenantID, "entry two"); err != nil {
			t.Fatalf("AppendHistory(two): %v", err)
		}

		entries, err := mem.GetHistoryEntries(ctx, tenantID, 10)
		if err != nil {
			t.Fatalf("GetHistoryEntries: %v", err)
		}
		if len(entries) < 2 {
			t.Fatalf("GetHistoryEntries returned %d entries, want >= 2", len(entries))
		}

		count, err := mem.GetHistoryCount(ctx, tenantID)
		if err != nil {
			t.Fatalf("GetHistoryCount: %v", err)
		}
		if count < 2 {
			t.Errorf("GetHistoryCount = %d, want >= 2", count)
		}

		if err := mem.ClearHistory(ctx, tenantID); err != nil {
			t.Fatalf("ClearHistory: %v", err)
		}
		count, err = mem.GetHistoryCount(ctx, tenantID)
		if err != nil {
			t.Fatalf("GetHistoryCount after ClearHistory: %v", err)
		}
		if count != 0 {
			t.Errorf("GetHistoryCount after ClearHistory = %d, want 0", count)
		}
	})

	// ── P1 ──

	t.Run("ClearState", func(t *testing.T) {
		if err := mem.SetState(ctx, tenantID, 42); err != nil {
			t.Fatalf("SetState: %v", err)
		}
		if err := mem.ClearState(ctx, tenantID); err != nil {
			t.Fatalf("ClearState: %v", err)
		}

		got, err := mem.GetState(ctx, tenantID)
		if err != nil {
			t.Fatalf("GetState after ClearState: %v", err)
		}
		if got != 0 {
			t.Errorf("GetState after ClearState = %d, want 0", got)
		}
	})

	t.Run("SetTokenState_GetTokenState", func(t *testing.T) {
		if err := mem.SetTokenState(ctx, tenantID, 100, 50); err != nil {
			t.Fatalf("SetTokenState: %v", err)
		}

		promptTokens, completionTokens, err := mem.GetTokenState(ctx, tenantID)
		if err != nil {
			t.Fatalf("GetTokenState: %v", err)
		}
		if promptTokens != 100 {
			t.Errorf("GetTokenState promptTokens = %d, want 100", promptTokens)
		}
		if completionTokens != 50 {
			t.Errorf("GetTokenState completionTokens = %d, want 50", completionTokens)
		}
	})

	t.Run("SetPersonaIsolation", func(t *testing.T) {
		// SetPersonaIsolation returns nothing (no error path): the contract is
		// only that both toggles are safe to call on any backend. On PG it is a
		// documented no-op (persona isolation is a SQLite-only concept).
		mem.SetPersonaIsolation(true)
		mem.SetPersonaIsolation(false)
	})

	// GetHistoryEntriesInRange replaced the SQLite-only RecallTimeRangeFunc: it is
	// the recall time-window query the letta provider depends on, so its ordering,
	// defaulting and tenant isolation must hold on every backend.
	t.Run("GetHistoryEntriesInRange", func(t *testing.T) {
		rangeTenant := createTestTenant(t, s, uniqueChatID(t))
		if err := mem.ClearHistory(ctx, rangeTenant); err != nil {
			t.Fatalf("ClearHistory: %v", err)
		}
		for _, entry := range []string{"first", "second", "third"} {
			if err := mem.AppendHistory(ctx, rangeTenant, entry); err != nil {
				t.Fatalf("AppendHistory(%s): %v", entry, err)
			}
		}

		// A wide window returns all three, NEWEST FIRST.
		start := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		end := time.Now().Add(24 * time.Hour)
		got, err := mem.GetHistoryEntriesInRange(ctx, rangeTenant, start, end, 10)
		if err != nil {
			t.Fatalf("GetHistoryEntriesInRange: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("returned %d entries, want 3", len(got))
		}
		if got[0].Entry != "third" {
			t.Errorf("first row = %q, want the newest (\"third\") — ordering is newest-first", got[0].Entry)
		}
		if got[2].Entry != "first" {
			t.Errorf("last row = %q, want the oldest (\"first\")", got[2].Entry)
		}
		if got[0].CreatedAt.IsZero() {
			t.Error("CreatedAt must be populated (recall needs timestamps)")
		}

		// limit <= 0 defaults to 20 (never "unlimited"), so a small limit still
		// truncates and stays newest-first.
		limited, err := mem.GetHistoryEntriesInRange(ctx, rangeTenant, start, end, 2)
		if err != nil {
			t.Fatalf("GetHistoryEntriesInRange(limit=2): %v", err)
		}
		if len(limited) != 2 || limited[0].Entry != "third" {
			t.Errorf("limit=2 returned %v, want the two newest newest-first", entryStrings(limited))
		}
		if def, err := mem.GetHistoryEntriesInRange(ctx, rangeTenant, start, end, 0); err != nil || len(def) != 3 {
			t.Errorf("limit=0 returned %d entries (err=%v), want the 20-row default (all 3 here)", len(def), err)
		}

		// An empty window must return nothing — not the whole table.
		future := time.Now().Add(48 * time.Hour)
		none, err := mem.GetHistoryEntriesInRange(ctx, rangeTenant, future, future.Add(time.Hour), 10)
		if err != nil {
			t.Fatalf("GetHistoryEntriesInRange(empty window): %v", err)
		}
		if len(none) != 0 {
			t.Errorf("empty window returned %d entries, want 0", len(none))
		}

		// Tenant isolation: another tenant's rows never leak in.
		otherTenant := createTestTenant(t, s, uniqueChatID(t))
		if err := mem.AppendHistory(ctx, otherTenant, "other-tenant-entry"); err != nil {
			t.Fatalf("AppendHistory(other): %v", err)
		}
		isolated, err := mem.GetHistoryEntriesInRange(ctx, rangeTenant, start, end, 50)
		if err != nil {
			t.Fatalf("GetHistoryEntriesInRange(isolation): %v", err)
		}
		for _, e := range isolated {
			if e.Entry == "other-tenant-entry" {
				t.Fatal("another tenant's history leaked into the range query")
			}
		}
	})
}

func entryStrings(rows []HistoryEntry) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Entry
	}
	return out
}

package storage

import (
	"testing"

	"xbot/storage/sqlite"
)

// testTenantStoreContract exercises the TenantStore half of the Storage
// contract. It runs against every backend (see sqlite_contract_test.go and
// pg_contract_test.go) so tenant registry, chat registry, per-session config
// and pending-resume semantics stay identical across implementations.
//
// Isolation: every subtest mints its own identifiers via uniqueChatID (chat
// ids / sender ids / subscription ids) so subtests never collide. Assertions
// that enumerate rows (ListTenants, ListDistinctChannels) only require the
// rows this subtest created to be present — the suite shares one Storage
// instance, so earlier subtests legitimately leave rows behind and an exact
// count would be wrong.
//
// Backend reachability note: setter/getter pairs are always preceded by an
// explicit GetOrCreateTenantID (or CreateChat), never relying on a setter to
// materialize a missing tenant row — the SQLite and PG implementations differ
// there and the contract must not encode either behavior.
func testTenantStoreContract(t *testing.T, factory StorageFactory) {
	s := factory(t)
	defer s.Close()

	ts := s.Tenant()

	// ── P0 ──

	t.Run("GetOrCreateTenantID_Idempotent", func(t *testing.T) {
		chatID := uniqueChatID(t)

		first, err := ts.GetOrCreateTenantID("web", chatID)
		if err != nil {
			t.Fatalf("GetOrCreateTenantID(first): %v", err)
		}
		if first == 0 {
			t.Fatalf("GetOrCreateTenantID returned tenant ID 0, want non-zero")
		}

		second, err := ts.GetOrCreateTenantID("web", chatID)
		if err != nil {
			t.Fatalf("GetOrCreateTenantID(second): %v", err)
		}
		if second != first {
			t.Errorf("GetOrCreateTenantID not idempotent for web/%s: first=%d second=%d", chatID, first, second)
		}
	})

	t.Run("GetTenantIDByChannelChatID", func(t *testing.T) {
		chatID := uniqueChatID(t)

		want, err := ts.GetOrCreateTenantID("web", chatID)
		if err != nil {
			t.Fatalf("GetOrCreateTenantID: %v", err)
		}

		got, err := ts.GetTenantIDByChannelChatID("web", chatID)
		if err != nil {
			t.Fatalf("GetTenantIDByChannelChatID(existing): %v", err)
		}
		if got != want {
			t.Errorf("GetTenantIDByChannelChatID(existing) = %d, want %d", got, want)
		}

		missing, err := ts.GetTenantIDByChannelChatID("web", uniqueChatID(t))
		if err != nil {
			t.Fatalf("GetTenantIDByChannelChatID(missing): %v", err)
		}
		if missing != 0 {
			t.Errorf("GetTenantIDByChannelChatID(missing) = %d, want 0", missing)
		}
	})

	t.Run("GetTenantInfo", func(t *testing.T) {
		chatID := uniqueChatID(t)

		tenantID, err := ts.GetOrCreateTenantID("cli", chatID)
		if err != nil {
			t.Fatalf("GetOrCreateTenantID: %v", err)
		}

		channel, gotChatID, err := ts.GetTenantInfo(tenantID)
		if err != nil {
			t.Fatalf("GetTenantInfo: %v", err)
		}
		if channel != "cli" {
			t.Errorf("GetTenantInfo channel = %q, want %q", channel, "cli")
		}
		if gotChatID != chatID {
			t.Errorf("GetTenantInfo chat_id = %q, want %q", gotChatID, chatID)
		}
	})

	t.Run("CreateChat_ListUserChats_DeleteChat_RenameChat", func(t *testing.T) {
		// A per-subtest owner keeps ListUserChats scoped to this subtest's rows
		// (SQLite also prepends the synthetic default chat chat_id == sender_id).
		senderID := "sender-" + uniqueChatID(t)

		chatID, err := ts.CreateChat("web", senderID, "original label")
		if err != nil {
			t.Fatalf("CreateChat: %v", err)
		}
		if chatID == "" {
			t.Fatalf("CreateChat returned empty chatID")
		}

		// Create: the new chat must be listed, with its label and current marker.
		chats, hasMore, err := ts.ListUserChats("web", senderID, chatID, 0, 100)
		if err != nil {
			t.Fatalf("ListUserChats(after create): %v", err)
		}
		chat := tenantContractFindChat(chats, chatID)
		if chat == nil {
			t.Fatalf("ListUserChats did not return created chat %q (hasMore=%v, rows=%d)", chatID, hasMore, len(chats))
		}
		if chat.Label != "original label" {
			t.Errorf("created chat label = %q, want %q", chat.Label, "original label")
		}
		if !chat.IsCurrent {
			t.Errorf("created chat IsCurrent = false, want true (currentChatID=%q)", chatID)
		}

		// Rename: the listed label must follow.
		if err := ts.RenameChat("web", senderID, chatID, "renamed label"); err != nil {
			t.Fatalf("RenameChat: %v", err)
		}
		chats, _, err = ts.ListUserChats("web", senderID, chatID, 0, 100)
		if err != nil {
			t.Fatalf("ListUserChats(after rename): %v", err)
		}
		chat = tenantContractFindChat(chats, chatID)
		if chat == nil {
			t.Fatalf("ListUserChats after rename did not return chat %q", chatID)
		}
		if chat.Label != "renamed label" {
			t.Errorf("renamed chat label = %q, want %q", chat.Label, "renamed label")
		}

		// Delete: the chat must disappear from the listing.
		if err := ts.DeleteChat("web", senderID, chatID); err != nil {
			t.Fatalf("DeleteChat: %v", err)
		}
		chats, _, err = ts.ListUserChats("web", senderID, chatID, 0, 100)
		if err != nil {
			t.Fatalf("ListUserChats(after delete): %v", err)
		}
		if tenantContractFindChat(chats, chatID) != nil {
			t.Errorf("deleted chat %q is still returned by ListUserChats", chatID)
		}
	})

	t.Run("ListTenants", func(t *testing.T) {
		webID, err := ts.GetOrCreateTenantID("web", uniqueChatID(t))
		if err != nil {
			t.Fatalf("GetOrCreateTenantID(web): %v", err)
		}
		cliID, err := ts.GetOrCreateTenantID("cli", uniqueChatID(t))
		if err != nil {
			t.Fatalf("GetOrCreateTenantID(cli): %v", err)
		}

		tenants, err := ts.ListTenants()
		if err != nil {
			t.Fatalf("ListTenants: %v", err)
		}
		found := make(map[int64]bool, len(tenants))
		for _, tn := range tenants {
			found[tn.ID] = true
		}
		if !found[webID] {
			t.Errorf("ListTenants (%d rows) is missing created tenant web=%d", len(tenants), webID)
		}
		if !found[cliID] {
			t.Errorf("ListTenants (%d rows) is missing created tenant cli=%d", len(tenants), cliID)
		}
	})

	t.Run("GetSenderForChat", func(t *testing.T) {
		senderID := "sender-" + uniqueChatID(t)

		chatID, err := ts.CreateChat("web", senderID, "sender lookup")
		if err != nil {
			t.Fatalf("CreateChat: %v", err)
		}

		got, err := ts.GetSenderForChat("web", chatID)
		if err != nil {
			t.Fatalf("GetSenderForChat: %v", err)
		}
		if got != senderID {
			t.Errorf("GetSenderForChat = %q, want %q", got, senderID)
		}
	})

	t.Run("ListDistinctChannels", func(t *testing.T) {
		if _, err := ts.GetOrCreateTenantID("web", uniqueChatID(t)); err != nil {
			t.Fatalf("GetOrCreateTenantID(web): %v", err)
		}
		if _, err := ts.GetOrCreateTenantID("cli", uniqueChatID(t)); err != nil {
			t.Fatalf("GetOrCreateTenantID(cli): %v", err)
		}

		channels, err := ts.ListDistinctChannels()
		if err != nil {
			t.Fatalf("ListDistinctChannels: %v", err)
		}
		for _, want := range []string{"web", "cli"} {
			if !tenantContractHasChannel(channels, want) {
				t.Errorf("ListDistinctChannels = %v, want it to contain %q", channels, want)
			}
		}
		if tenantContractHasChannel(channels, "_shared") {
			t.Errorf("ListDistinctChannels = %v, must not contain the reserved %q channel", channels, "_shared")
		}
	})

	// ── P1 ──

	t.Run("SetTenantCWD_GetTenantCWD", func(t *testing.T) {
		tenantID := createTestTenant(t, s, uniqueChatID(t))
		want := "/tmp/xbot-contract-cwd"

		if err := ts.SetTenantCWD(tenantID, want); err != nil {
			t.Fatalf("SetTenantCWD: %v", err)
		}
		got, err := ts.GetTenantCWD(tenantID)
		if err != nil {
			t.Fatalf("GetTenantCWD: %v", err)
		}
		if got != want {
			t.Errorf("GetTenantCWD = %q, want %q", got, want)
		}
	})

	t.Run("SetTenantPreview", func(t *testing.T) {
		tenantID := createTestTenant(t, s, uniqueChatID(t))

		if err := ts.SetTenantPreview(tenantID, "latest message preview"); err != nil {
			t.Fatalf("SetTenantPreview: %v", err)
		}
	})

	t.Run("SetTenantSubscription_GetTenantSubscription", func(t *testing.T) {
		chatID := uniqueChatID(t)
		if _, err := ts.GetOrCreateTenantID("web", chatID); err != nil {
			t.Fatalf("GetOrCreateTenantID: %v", err)
		}

		if err := ts.SetTenantSubscription("web", chatID, "sub-contract", "model-contract"); err != nil {
			t.Fatalf("SetTenantSubscription: %v", err)
		}

		subID, model, err := ts.GetTenantSubscription("web", chatID)
		if err != nil {
			t.Fatalf("GetTenantSubscription: %v", err)
		}
		if subID != "sub-contract" {
			t.Errorf("GetTenantSubscription subscription = %q, want %q", subID, "sub-contract")
		}
		if model != "model-contract" {
			t.Errorf("GetTenantSubscription model = %q, want %q", model, "model-contract")
		}
	})

	t.Run("SetTenantRunner_GetTenantRunner", func(t *testing.T) {
		chatID := uniqueChatID(t)
		if _, err := ts.GetOrCreateTenantID("web", chatID); err != nil {
			t.Fatalf("GetOrCreateTenantID: %v", err)
		}

		if err := ts.SetTenantRunner("web", chatID, "runner-contract"); err != nil {
			t.Fatalf("SetTenantRunner: %v", err)
		}

		got, err := ts.GetTenantRunner("web", chatID)
		if err != nil {
			t.Fatalf("GetTenantRunner: %v", err)
		}
		if got != "runner-contract" {
			t.Errorf("GetTenantRunner = %q, want %q", got, "runner-contract")
		}
	})

	t.Run("ClearSubscriptionFromTenants", func(t *testing.T) {
		chatID := uniqueChatID(t)
		if _, err := ts.GetOrCreateTenantID("web", chatID); err != nil {
			t.Fatalf("GetOrCreateTenantID: %v", err)
		}

		// Unique subscription id: the clear is a blanket UPDATE over every
		// tenant pointing at this id, so a shared literal would also wipe rows
		// created by sibling subtests.
		subID := "sub-clear-" + uniqueChatID(t)
		if err := ts.SetTenantSubscription("web", chatID, subID, "model-clear"); err != nil {
			t.Fatalf("SetTenantSubscription: %v", err)
		}

		if err := ts.ClearSubscriptionFromTenants(subID); err != nil {
			t.Fatalf("ClearSubscriptionFromTenants: %v", err)
		}

		gotSub, gotModel, err := ts.GetTenantSubscription("web", chatID)
		if err != nil {
			t.Fatalf("GetTenantSubscription(after clear): %v", err)
		}
		if gotSub != "" || gotModel != "" {
			t.Errorf("GetTenantSubscription after clear = (%q, %q), want empty strings", gotSub, gotModel)
		}
	})

	t.Run("DeleteTenantHistory", func(t *testing.T) {
		tenantID := createTestTenant(t, s, uniqueChatID(t))
		ss := getSessionStore(t, s, tenantID)

		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "hello")); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}

		before, err := ss.GetMessagesCount(tenantID)
		if err != nil {
			t.Fatalf("GetMessagesCount(before): %v", err)
		}
		if before == 0 {
			t.Fatalf("GetMessagesCount before DeleteTenantHistory = 0, want > 0")
		}

		if err := ts.DeleteTenantHistory(tenantID); err != nil {
			t.Fatalf("DeleteTenantHistory: %v", err)
		}

		after, err := ss.GetMessagesCount(tenantID)
		if err != nil {
			t.Fatalf("GetMessagesCount(after): %v", err)
		}
		if after != 0 {
			t.Errorf("GetMessagesCount after DeleteTenantHistory = %d, want 0", after)
		}
	})

	t.Run("UpdateChatSortOrders", func(t *testing.T) {
		senderID := "sender-" + uniqueChatID(t)

		chatID, err := ts.CreateChat("web", senderID, "sort order")
		if err != nil {
			t.Fatalf("CreateChat: %v", err)
		}

		if err := ts.UpdateChatSortOrders("web", senderID, map[string]int{chatID: 1}); err != nil {
			t.Fatalf("UpdateChatSortOrders: %v", err)
		}

		chats, _, err := ts.ListUserChats("web", senderID, chatID, 0, 100)
		if err != nil {
			t.Fatalf("ListUserChats: %v", err)
		}
		chat := tenantContractFindChat(chats, chatID)
		if chat == nil {
			t.Fatalf("ListUserChats did not return chat %q after sort-order update", chatID)
		}
		if chat.SortOrder != 1 {
			t.Errorf("chat sort order = %d, want 1", chat.SortOrder)
		}
	})

	t.Run("PendingResumes_CRUD", func(t *testing.T) {
		chatID := uniqueChatID(t)
		senderID := "sender-" + uniqueChatID(t)

		if err := ts.AddPendingResume("web", chatID, senderID); err != nil {
			t.Fatalf("AddPendingResume: %v", err)
		}
		if !tenantContractHasPendingResume(t, ts, "web", chatID, senderID) {
			t.Fatalf("ListPendingResumes does not contain the added resume web/%s", chatID)
		}

		if err := ts.ClearPendingResume("web", chatID); err != nil {
			t.Fatalf("ClearPendingResume: %v", err)
		}
		if tenantContractHasPendingResume(t, ts, "web", chatID, senderID) {
			t.Errorf("ListPendingResumes still contains the cleared resume web/%s", chatID)
		}
	})

	// ── P1 (v71 per-session-DB registry) ──
	//
	// db_path / migrated are the two registry columns that decide whether a
	// session reads its own DB, and the v72 ordering contract (flag first, then
	// delete the main-DB copy) depends on them being independently writable and
	// readable. Both backends store them as NOT NULL columns on `tenants`
	// (SQLite: TEXT '' / INTEGER 0, PG: TEXT '' / BOOLEAN false) and the getter
	// reads them back by primary key.

	t.Run("GetTenantDBInfo_DefaultsThenSetDBPath", func(t *testing.T) {
		tenantID := createTestTenant(t, s, uniqueChatID(t))

		// A freshly created tenant has no session DB assigned: callers rely on
		// the empty path to mean "derive + persist on first open"
		// (session/sessiondb.go: dbPath == "" ⇒ SessionDBRelPath + SetTenantDBPath).
		info, err := ts.GetTenantDBInfo(tenantID)
		if err != nil {
			t.Fatalf("GetTenantDBInfo(fresh): %v", err)
		}
		if info.DBPath != "" {
			t.Errorf("GetTenantDBInfo(fresh).DBPath = %q, want \"\"", info.DBPath)
		}
		if info.Migrated {
			t.Errorf("GetTenantDBInfo(fresh).Migrated = true, want false")
		}

		dbPath := "sessions/web/bucket/" + uniqueChatID(t) + ".db"
		if err := ts.SetTenantDBPath(tenantID, dbPath); err != nil {
			t.Fatalf("SetTenantDBPath: %v", err)
		}

		info, err = ts.GetTenantDBInfo(tenantID)
		if err != nil {
			t.Fatalf("GetTenantDBInfo(after SetTenantDBPath): %v", err)
		}
		if info.DBPath != dbPath {
			t.Errorf("GetTenantDBInfo.DBPath = %q, want %q", info.DBPath, dbPath)
		}
		// Assigning the path must NOT flip the migration flag: the two columns
		// are independent, and this order (path first, flag only after the copy
		// commits) is what the lazy-migration path writes.
		if info.Migrated {
			t.Errorf("GetTenantDBInfo.Migrated = true after SetTenantDBPath alone, want false")
		}

		// The registry column is the single authority after the first
		// derivation, so a later write replaces the stored path verbatim.
		reassigned := dbPath + ".reassigned"
		if err := ts.SetTenantDBPath(tenantID, reassigned); err != nil {
			t.Fatalf("SetTenantDBPath(reassign): %v", err)
		}
		info, err = ts.GetTenantDBInfo(tenantID)
		if err != nil {
			t.Fatalf("GetTenantDBInfo(after reassign): %v", err)
		}
		if info.DBPath != reassigned {
			t.Errorf("GetTenantDBInfo.DBPath after reassign = %q, want %q", info.DBPath, reassigned)
		}
	})

	t.Run("SetTenantMigrated", func(t *testing.T) {
		tenantID := createTestTenant(t, s, uniqueChatID(t))

		before, err := ts.GetTenantDBInfo(tenantID)
		if err != nil {
			t.Fatalf("GetTenantDBInfo(before): %v", err)
		}
		if before.Migrated {
			t.Fatalf("fresh tenant %d already migrated: %+v", tenantID, before)
		}

		if err := ts.SetTenantMigrated(tenantID); err != nil {
			t.Fatalf("SetTenantMigrated: %v", err)
		}

		after, err := ts.GetTenantDBInfo(tenantID)
		if err != nil {
			t.Fatalf("GetTenantDBInfo(after): %v", err)
		}
		if !after.Migrated {
			t.Errorf("GetTenantDBInfo.Migrated = false after SetTenantMigrated, want true")
		}
		// Marking migrated must not touch the path column.
		if after.DBPath != before.DBPath {
			t.Errorf("SetTenantMigrated changed db_path: before=%q after=%q", before.DBPath, after.DBPath)
		}

		// Idempotent: re-marking is the crash-recovery re-run path and must stay
		// a no-op success that keeps the flag set.
		if err := ts.SetTenantMigrated(tenantID); err != nil {
			t.Fatalf("SetTenantMigrated(second): %v", err)
		}
		again, err := ts.GetTenantDBInfo(tenantID)
		if err != nil {
			t.Fatalf("GetTenantDBInfo(after second mark): %v", err)
		}
		if !again.Migrated {
			t.Errorf("GetTenantDBInfo.Migrated = false after repeated SetTenantMigrated, want true")
		}
	})

	t.Run("GetTenantDBInfo_MissingTenant", func(t *testing.T) {
		// The registry is read by primary key; an unknown tenant has no row, so
		// the error (not a zero value) is what tells callers there is nothing to
		// read — sessiondb's shutdown path only deletes the DB file when the
		// call succeeds with a non-empty path.
		const missingTenantID = int64(1) << 62

		info, err := ts.GetTenantDBInfo(missingTenantID)
		if err == nil {
			t.Errorf("GetTenantDBInfo(missing tenant %d) = %+v, nil error; want an error", missingTenantID, info)
		}
		if info.DBPath != "" || info.Migrated {
			t.Errorf("GetTenantDBInfo(missing tenant %d) returned non-zero value %+v alongside error", missingTenantID, info)
		}
	})

	// ── P1 (sender identity) ──

	t.Run("GetSessionSenderID", func(t *testing.T) {
		senderID := "sender-" + uniqueChatID(t)

		// Sender identity lives in the user_chats registry, not in tenants, so
		// the row must come from CreateChat — the same path the Web channel uses
		// when a chatroom is opened. (GetOrCreateTenantID alone is NOT enough;
		// this subtest pins that distinction below.)
		chatID, err := ts.CreateChat("web", senderID, "sender lookup")
		if err != nil {
			t.Fatalf("CreateChat: %v", err)
		}

		got, err := ts.GetSessionSenderID("web", chatID)
		if err != nil {
			t.Fatalf("GetSessionSenderID(existing): %v", err)
		}
		if got != senderID {
			t.Errorf("GetSessionSenderID(web, %s) = %q, want %q", chatID, got, senderID)
		}

		// A tenants row without a user_chats row has no sender identity: the
		// lookup must report the empty string rather than fabricate one
		// (serverapp treats "" as "no resume identity").
		tenantOnlyChatID := uniqueChatID(t)
		if _, err := ts.GetOrCreateTenantID("web", tenantOnlyChatID); err != nil {
			t.Fatalf("GetOrCreateTenantID: %v", err)
		}
		got, err = ts.GetSessionSenderID("web", tenantOnlyChatID)
		if err != nil {
			t.Fatalf("GetSessionSenderID(tenant without user_chats row): %v", err)
		}
		if got != "" {
			t.Errorf("GetSessionSenderID(tenant-only chat %s) = %q, want \"\"", tenantOnlyChatID, got)
		}

		// Unknown chat: empty string, nil error (never an error — the caller
		// distinguishes "no row" from a real failure by the error alone).
		got, err = ts.GetSessionSenderID("web", uniqueChatID(t))
		if err != nil {
			t.Fatalf("GetSessionSenderID(unknown chat): %v", err)
		}
		if got != "" {
			t.Errorf("GetSessionSenderID(unknown chat) = %q, want \"\"", got)
		}

		// Channel is part of the lookup key: the same chat id on another channel
		// has no user_chats row.
		got, err = ts.GetSessionSenderID("cli", chatID)
		if err != nil {
			t.Fatalf("GetSessionSenderID(other channel): %v", err)
		}
		if got != "" {
			t.Errorf("GetSessionSenderID(cli, web chat %s) = %q, want \"\"", chatID, got)
		}
	})
}

// ─── Helpers (tenant contract) ───

// tenantContractFindChat returns the listing row for chatID, or nil when the
// chat is absent from the listing.
func tenantContractFindChat(chats []sqlite.UserChatWithPreview, chatID string) *sqlite.UserChatWithPreview {
	for i := range chats {
		if chats[i].ChatID == chatID {
			return &chats[i]
		}
	}
	return nil
}

// tenantContractHasChannel reports whether the channel list contains want.
func tenantContractHasChannel(channels []string, want string) bool {
	for _, c := range channels {
		if c == want {
			return true
		}
	}
	return false
}

// tenantContractHasPendingResume reports whether ListPendingResumes contains an
// entry for (channel, chatID), asserting its sender on the way.
func tenantContractHasPendingResume(t *testing.T, ts TenantStore, channel, chatID, senderID string) bool {
	t.Helper()

	resumes, err := ts.ListPendingResumes()
	if err != nil {
		t.Fatalf("ListPendingResumes: %v", err)
	}
	for _, r := range resumes {
		if r.Channel != channel || r.ChatID != chatID {
			continue
		}
		if r.SenderID != senderID {
			t.Errorf("pending resume %s/%s sender = %q, want %q", channel, chatID, r.SenderID, senderID)
		}
		return true
	}
	return false
}

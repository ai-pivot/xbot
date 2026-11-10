package storage

import (
	"fmt"
	"testing"
	"time"

	"xbot/storage/sqlite"
)

// testInteractiveAgentStoreContract pins the interactive SubAgent registry
// semantics on BOTH backends. The agent rebuilds its sub-agent tree from this
// table after a restart, so a backend that silently lacks it (the original PG
// panic) is a hard failure, not a degraded nicety.
func testInteractiveAgentStoreContract(t *testing.T, factory StorageFactory) {
	t.Run("UpsertGetAndListChildren", func(t *testing.T) {
		s := factory(t)
		store := s.InteractiveAgents()
		if store == nil {
			t.Fatal("Storage.InteractiveAgents() returned nil")
		}
		parentID := uniqueChatID(t)
		key := "agent:testrole/" + parentID

		rec := sqlite.InteractiveAgentRecord{
			AgentKey:      key,
			ParentChannel: "web",
			ParentChatID:  parentID,
			RoleName:      "testrole",
			Instance:      parentID,
			Task:          "do work",
			Background:    true,
			Status:        sqlite.InteractiveAgentIdle,
			TurnID:        7,
			ModelName:     "model-x",
			SubID:         "sub-x",
			OriginUserID:  "web-1",
		}
		if err := store.Upsert(rec); err != nil {
			t.Fatalf("Upsert: %v", err)
		}

		got, err := store.Get(key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.AgentKey != key || got.ParentChannel != "web" || got.ParentChatID != parentID {
			t.Fatalf("Get returned wrong identity: %+v", got)
		}
		if got.TurnID != 7 || !got.Background || got.ModelName != "model-x" {
			t.Fatalf("Get lost fields: %+v", got)
		}

		// Upsert must preserve created_at while updating mutable fields.
		created := got.CreatedAt
		rec.Status = sqlite.InteractiveAgentRunning
		rec.TurnID = 8
		rec.LastReply = "done"
		if err := store.Upsert(rec); err != nil {
			t.Fatalf("Upsert update: %v", err)
		}
		got, err = store.Get(key)
		if err != nil {
			t.Fatalf("Get after update: %v", err)
		}
		if got.CreatedAt != created {
			t.Fatalf("Upsert must preserve created_at: %q -> %q", created, got.CreatedAt)
		}
		if got.Status != sqlite.InteractiveAgentRunning || got.TurnID != 8 || got.LastReply != "done" {
			t.Fatalf("Upsert update not applied: %+v", got)
		}
		if got.LastReplyAt == "" {
			t.Fatalf("LastReplyAt must advance when LastReply changes: %+v", got)
		}

		children, err := store.ListChildAgents("web", parentID)
		if err != nil {
			t.Fatalf("ListChildAgents: %v", err)
		}
		if len(children) != 1 || children[0].AgentKey != key {
			t.Fatalf("ListChildAgents = %+v", children)
		}

		// Missing key is a sentinel, not a generic error.
		if _, err := store.Get("agent:missing/none"); err != sqlite.ErrInteractiveAgentNotFound {
			t.Fatalf("missing Get error = %v, want ErrInteractiveAgentNotFound", err)
		}
	})

	t.Run("MarkStatusAndRunningInterrupt", func(t *testing.T) {
		s := factory(t)
		store := s.InteractiveAgents()
		chatID := uniqueChatID(t)
		key := "agent:mark/" + chatID
		if err := store.Upsert(sqlite.InteractiveAgentRecord{
			AgentKey: key, ParentChannel: "web", ParentChatID: chatID,
			RoleName: "mark", Instance: chatID, Status: sqlite.InteractiveAgentRunning, TurnID: 3,
		}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if n, err := store.MarkAllRunningAsInterrupted(); err != nil || n < 1 {
			t.Fatalf("MarkAllRunningAsInterrupted = (%d, %v), want >=1", n, err)
		}
		got, err := store.Get(key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status != sqlite.InteractiveAgentInterrupted {
			t.Fatalf("status = %q, want interrupted", got.Status)
		}
		if err := store.MarkStatus(key, sqlite.InteractiveAgentIdle, 9); err != nil {
			t.Fatalf("MarkStatus: %v", err)
		}
		got, _ = store.Get(key)
		if got.Status != sqlite.InteractiveAgentIdle || got.TurnID != 9 {
			t.Fatalf("MarkStatus not applied: %+v", got)
		}
	})

	t.Run("MarkUnloadedCascade", func(t *testing.T) {
		s := factory(t)
		store := s.InteractiveAgents()
		rootChat := uniqueChatID(t)
		rootKey := "agent:cascade/" + rootChat

		// root -> child (parent is channel=agent, chatID=root role/instance)
		childChat := fmt.Sprintf("cascade/%s", rootChat)
		childKey := "agent:child/" + childChat
		for _, rec := range []sqlite.InteractiveAgentRecord{
			{AgentKey: rootKey, ParentChannel: "web", ParentChatID: rootChat, RoleName: "cascade", Instance: rootChat, Status: sqlite.InteractiveAgentIdle},
			{AgentKey: childKey, ParentChannel: "agent", ParentChatID: "cascade/" + rootChat, RoleName: "child", Instance: childChat, Status: sqlite.InteractiveAgentIdle},
		} {
			if err := store.Upsert(rec); err != nil {
				t.Fatalf("Upsert %s: %v", rec.AgentKey, err)
			}
		}
		if err := store.MarkUnloadedCascade(rootKey); err != nil {
			t.Fatalf("MarkUnloadedCascade: %v", err)
		}
		for _, key := range []string{rootKey, childKey} {
			got, err := store.Get(key)
			if err != nil {
				t.Fatalf("Get %s: %v", key, err)
			}
			if got.Status != sqlite.InteractiveAgentUnloaded {
				t.Fatalf("%s status = %q, want unloaded", key, got.Status)
			}
		}
	})

	t.Run("MarkOrphanedIdleUnloaded", func(t *testing.T) {
		s := factory(t)
		store := s.InteractiveAgents()
		suffix := uniqueChatID(t)

		// alive: its backing tenant exists.
		aliveChat := "alive-" + suffix
		aliveKey := "agent:alive/" + aliveChat
		if err := store.Upsert(sqlite.InteractiveAgentRecord{
			AgentKey: aliveKey, ParentChannel: "web", ParentChatID: aliveChat,
			RoleName: "alive", Instance: aliveChat, Status: sqlite.InteractiveAgentIdle,
		}); err != nil {
			t.Fatalf("Upsert alive: %v", err)
		}
		if _, err := s.Tenant().GetOrCreateTenantID("agent", "web:"+aliveChat+"/alive:"+aliveChat); err != nil {
			t.Fatalf("create backing tenant: %v", err)
		}

		// orphan: no backing tenant.
		orphanChat := "orphan-" + suffix
		orphanKey := "agent:orphan/" + orphanChat
		if err := store.Upsert(sqlite.InteractiveAgentRecord{
			AgentKey: orphanKey, ParentChannel: "web", ParentChatID: orphanChat,
			RoleName: "orphan", Instance: orphanChat, Status: sqlite.InteractiveAgentIdle,
		}); err != nil {
			t.Fatalf("Upsert orphan: %v", err)
		}

		n, err := store.MarkOrphanedIdleUnloaded(nil)
		if err != nil {
			t.Fatalf("MarkOrphanedIdleUnloaded: %v", err)
		}
		if n < 1 {
			t.Fatalf("MarkOrphanedIdleUnloaded changed %d rows, want >=1", n)
		}
		orphan, _ := store.Get(orphanKey)
		if orphan.Status != sqlite.InteractiveAgentUnloaded {
			t.Fatalf("orphan status = %q, want unloaded", orphan.Status)
		}
		alive, _ := store.Get(aliveKey)
		if alive.Status != sqlite.InteractiveAgentIdle {
			t.Fatalf("alive status = %q, want idle", alive.Status)
		}
	})

	t.Run("ListActive", func(t *testing.T) {
		s := factory(t)
		store := s.InteractiveAgents()
		chatID := uniqueChatID(t)
		key := "agent:active/" + chatID
		if err := store.Upsert(sqlite.InteractiveAgentRecord{
			AgentKey: key, ParentChannel: "web", ParentChatID: chatID,
			RoleName: "active", Instance: chatID, Status: sqlite.InteractiveAgentIdle,
		}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		items, err := store.ListActive(time.Now().Add(-time.Hour), false)
		if err != nil {
			t.Fatalf("ListActive: %v", err)
		}
		found := false
		for _, it := range items {
			if it.AgentKey == key {
				found = true
				if it.RoleName != "active" || it.Status != sqlite.InteractiveAgentIdle {
					t.Fatalf("ListActive row = %+v", it)
				}
			}
		}
		if !found {
			t.Fatalf("ListActive did not include %s (items=%d)", key, len(items))
		}

		// includeUnloaded=false must hide unloaded rows.
		if err := store.MarkStatus(key, sqlite.InteractiveAgentUnloaded, 0); err != nil {
			t.Fatalf("MarkStatus: %v", err)
		}
		items, _ = store.ListActive(time.Now().Add(-time.Hour), false)
		for _, it := range items {
			if it.AgentKey == key {
				t.Fatalf("ListActive(includeUnloaded=false) returned unloaded row %s", key)
			}
		}
	})
}

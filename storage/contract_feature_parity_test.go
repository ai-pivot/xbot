package storage

import (
	"context"
	"strconv"
	"testing"
	"time"

	"xbot/storage/sqlite"
)

// testFeatureParityContract pins the centralized-storage features that used to
// be SQLite-only: structured messages, group metadata/messages, activity index
// and the runner registry. Both backends must expose them through Storage /
// SessionStore.
func testFeatureParityContract(t *testing.T, factory StorageFactory) {
	t.Run("StructuredMessages", func(t *testing.T) {
		s := factory(t)
		chatID := uniqueChatID(t)
		tenantID := createTestTenant(t, s, chatID)
		ss := getSessionStore(t, s, tenantID)

		// Idempotent append: same (chat, key) returns the existing row.
		rec := sqlite.StructuredMessage{
			ChatID:         chatID,
			Kind:           sqlite.StructuredKindReport,
			CorrelationID:  "corr-1",
			IdempotencyKey: "idem-1",
			Content:        "report body",
			SenderType:     "agent",
			SenderID:       "agent:x",
			SenderName:     "x",
			Direction:      "out",
		}
		id, created, err := ss.AppendStructuredMessage(tenantID, rec)
		if err != nil {
			t.Fatalf("AppendStructuredMessage: %v", err)
		}
		if !created {
			t.Fatal("first append must report created=true")
		}
		id2, created2, err := ss.AppendStructuredMessage(tenantID, rec)
		if err != nil {
			t.Fatalf("AppendStructuredMessage duplicate: %v", err)
		}
		if created2 || id2 != id {
			t.Fatalf("idempotent append = (%d, created=%v), want (%d,false)", id2, created2, id)
		}
		got, err := ss.GetStructuredMessageByID(tenantID, id)
		if err != nil {
			t.Fatalf("GetStructuredMessageByID: %v", err)
		}
		if got.Kind != sqlite.StructuredKindReport || got.Content != "report body" {
			t.Fatalf("stored row = %+v", got)
		}

		// Decision CAS: first decision succeeds and marks the report answered.
		dec := sqlite.StructuredMessage{
			ChatID:         chatID,
			Kind:           sqlite.StructuredKindDecision,
			ReplyTo:        strconv.FormatInt(id, 10),
			IdempotencyKey: "decision-1",
			Content:        "approved",
			Direction:      "in",
		}
		if _, _, err := ss.AppendDecision(tenantID, dec); err != nil {
			t.Fatalf("AppendDecision: %v", err)
		}
		target, err := ss.GetStructuredMessageByID(tenantID, id)
		if err != nil {
			t.Fatalf("Get report after decision: %v", err)
		}
		if !target.Answered {
			t.Fatal("decision must mark target answered")
		}
		dec.IdempotencyKey = "decision-2"
		if _, _, err := ss.AppendDecision(tenantID, dec); err == nil {
			t.Fatal("second decision with another key must be rejected by CAS")
		}

		// Ask/answer/inbox.
		ask := sqlite.StructuredMessage{
			ChatID:         chatID,
			Kind:           sqlite.StructuredKindAskUser,
			CorrelationID:  "ask-1",
			IdempotencyKey: "ask-idem",
			Content:        "continue?",
			Direction:      "out",
		}
		askID, _, err := ss.AppendStructuredMessage(tenantID, ask)
		if err != nil {
			t.Fatalf("AppendStructuredMessage ask: %v", err)
		}
		if n, err := ss.CountInbox(tenantID); err != nil || n < 1 {
			t.Fatalf("CountInbox before answer = (%d,%v), want >=1", n, err)
		}
		_, qID, created, err := ss.AnswerAskUser(tenantID, chatID, "ask-1", "yes", `{"answers":["yes"]}`, "answer-idem", "web-1", "web", false)
		if err != nil {
			t.Fatalf("AnswerAskUser: %v", err)
		}
		if !created || qID != askID {
			t.Fatalf("AnswerAskUser = (question=%d, created=%v), want (%d,true)", qID, created, askID)
		}
		if n, err := ss.CountInbox(tenantID); err != nil || n != 0 {
			t.Fatalf("CountInbox after answer = (%d,%v), want 0", n, err)
		}
	})

	t.Run("Groups", func(t *testing.T) {
		s := factory(t)
		gs := s.Groups()
		if gs == nil {
			t.Fatal("Storage.Groups() returned nil")
		}
		ctx := context.Background()
		gid := "grp_" + uniqueChatID(t)
		owner := "web-1"
		g, err := gs.CreateGroup(ctx, gid, "Test Group", owner, []string{"web-2", "agent:x/y"})
		if err != nil {
			t.Fatalf("CreateGroup: %v", err)
		}
		if g.ID != gid || g.Owner != owner || g.MemberCount != 3 {
			t.Fatalf("CreateGroup = %+v", g)
		}
		if _, err := gs.CreateGroup(ctx, gid, "Dup", owner, nil); err == nil {
			t.Fatal("duplicate group must fail")
		}
		groups, err := gs.ListGroupsFor(ctx, owner)
		if err != nil {
			t.Fatalf("ListGroupsFor: %v", err)
		}
		found := false
		for _, row := range groups {
			if row.ID == gid {
				found = true
			}
		}
		if !found {
			t.Fatalf("ListGroupsFor did not include %s", gid)
		}
		members, err := gs.ListMembers(ctx, gid)
		if err != nil || len(members) != 3 {
			t.Fatalf("ListMembers = (%d,%v), want 3", len(members), err)
		}
		if ok, err := gs.IsAliveMember(ctx, gid, owner); err != nil || !ok {
			t.Fatalf("IsAliveMember(owner) = (%v,%v)", ok, err)
		}
		if _, err := gs.AddMember(ctx, gid, "web-3"); err != nil {
			t.Fatalf("AddMember: %v", err)
		}
		if ok, _ := gs.IsAliveMember(ctx, gid, "web-3"); !ok {
			t.Fatal("new member must be alive")
		}
	})

	t.Run("ActivityIndex", func(t *testing.T) {
		s := factory(t)
		as := s.Activity()
		if as == nil {
			t.Fatal("Storage.Activity() returned nil")
		}
		chatID := uniqueChatID(t)
		tenantID := createTestTenant(t, s, chatID)
		item := sqlite.ActivityItem{
			TenantID:   tenantID,
			ChatID:     chatID,
			Channel:    "web",
			MessageID:  42,
			Kind:       sqlite.StructuredKindReport,
			SenderID:   "web-1",
			SenderName: "web",
			Summary:    "hello activity",
			CreatedAt:  sqlite.FormatActivityTime(time.Now()),
		}
		if err := as.Insert(item); err != nil {
			t.Fatalf("Insert: %v", err)
		}
		if err := as.Insert(item); err != nil {
			t.Fatalf("duplicate Insert must be idempotent: %v", err)
		}
		items, hasMore, err := as.List(50, time.Hour, nil)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if hasMore {
			t.Fatal("hasMore must be false for a single row")
		}
		found := false
		for _, it := range items {
			if it.ChatID == chatID && it.MessageID == 42 {
				found = true
			}
		}
		if !found {
			t.Fatalf("List did not include inserted item (%d rows)", len(items))
		}
		if n, err := as.Count(); err != nil || n < 1 {
			t.Fatalf("Count = (%d,%v), want >=1", n, err)
		}
	})

	t.Run("RunnerStore", func(t *testing.T) {
		s := factory(t)
		rs := s.Runners()
		if rs == nil {
			t.Fatal("Storage.Runners() returned nil")
		}
		name := "runner-" + uniqueChatID(t)
		token, err := rs.Create(name, sqlite.RunnerModeNative, "", "/tmp/ws", sqlite.RunnerLLMSettings{})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if token == "" {
			t.Fatal("Create returned empty token")
		}
		if got, err := rs.Token(name); err != nil || got != token {
			t.Fatalf("Token = (%q,%v), want %q", got, err, token)
		}
		if ok := rs.Validate(token); !ok {
			t.Fatal("Validate must accept the minted token")
		}
		if got, ok := rs.FindByToken(token); !ok || got != name {
			t.Fatalf("FindByToken = (%q,%v)", got, ok)
		}
		infos, err := rs.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		found := false
		for _, info := range infos {
			if info.Name == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("List did not include %s", name)
		}
		if _, err := rs.Get(name); err != nil {
			t.Fatalf("Get: %v", err)
		}
		newName := name + "-renamed"
		if err := rs.Rename(name, newName); err != nil {
			t.Fatalf("Rename: %v", err)
		}
		if _, err := rs.Get(newName); err != nil {
			t.Fatalf("Get after rename: %v", err)
		}
		if err := rs.Delete(newName); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := rs.Get(newName); err == nil {
			t.Fatal("Get after delete must fail")
		}
	})
}

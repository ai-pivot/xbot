package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"xbot/llm"
)

func TestGroupService_CreateListMembers(t *testing.T) {
	db := openTestDB(t)
	svc := NewGroupService(db.Conn())
	ctx := context.Background()

	g, err := svc.CreateGroup(ctx, "grp_test_1", "团队晨会", "web-1",
		[]string{"agent:hubu/main", "agent:backend/main", "agent:hubu/main"})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if g.MemberCount != 3 { // owner + 两个去重后的 agent
		t.Fatalf("member_count = %d, want 3", g.MemberCount)
	}

	if _, err := svc.CreateGroup(ctx, "grp_test_1", "dup", "web-1", nil); err != ErrGroupExists {
		t.Fatalf("duplicate create error = %v, want ErrGroupExists", err)
	}

	groups, err := svc.ListGroupsFor(ctx, "agent:backend/main")
	if err != nil {
		t.Fatalf("ListGroupsFor: %v", err)
	}
	if len(groups) != 1 || groups[0].ID != "grp_test_1" {
		t.Fatalf("groups = %+v", groups)
	}
	if all, _ := svc.ListGroupsFor(ctx, "agent:libu/main"); len(all) != 0 {
		t.Fatalf("non-member sees %d groups, want 0", len(all))
	}

	members, err := svc.ListMembers(ctx, "grp_test_1")
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("members = %d, want 3", len(members))
	}
	ownerFound := false
	for _, m := range members {
		if m.MemberAddr == "web-1" {
			ownerFound = true
			if m.Status != "alive" || m.Name != "web-1" {
				t.Fatalf("owner member = %+v", m)
			}
		}
	}
	if !ownerFound {
		t.Fatalf("owner web-1 missing from members: %+v", members)
	}
	for _, m := range members {
		if m.MemberAddr == "agent:hubu/main" {
			if m.Role != "hubu" || m.Instance != "main" || m.Name != "hubu/main" {
				t.Fatalf("enrich member = %+v", m)
			}
		}
	}

	ok, err := svc.IsAliveMember(ctx, "grp_test_1", "agent:hubu/main")
	if err != nil || !ok {
		t.Fatalf("IsAliveMember(hubu) = %v, %v", ok, err)
	}
	if ok, _ := svc.IsAliveMember(ctx, "grp_test_1", "agent:libu/main"); ok {
		t.Fatalf("libu should not be a member")
	}
	if _, err := svc.ListMembers(ctx, "grp_missing"); err != ErrGroupNotFound {
		t.Fatalf("missing group error = %v", err)
	}
}

func TestGroupMessages_AppendAndCursor(t *testing.T) {
	db, err := OpenSessionDB(filepath.Join(t.TempDir(), "session.db"))
	if err != nil {
		t.Fatalf("OpenSessionDB: %v", err)
	}
	defer func() { _ = db.Close() }()
	svc := NewSessionService(db)
	const tenantID = int64(7)
	base := time.Now().Add(-2 * time.Hour)

	for i := 0; i < 12; i++ {
		kind := "text"
		switch i % 3 {
		case 0:
			kind = "ask_user"
		case 1:
			kind = "answer"
		}
		_, err := svc.AppendGroupMessage(tenantID, llm.ChatMessage{
			Role:      "assistant",
			Content:   fmt.Sprintf("msg-%02d", i),
			Timestamp: base.Add(time.Duration(i) * time.Minute),
		}, GroupMessageEnvelope{
			GroupID: "grp_test_1", Kind: kind, CorrelationID: fmt.Sprintf("corr_%d", i/3),
			To: []string{"agent:hubu/main"}, Payload: map[string]any{"i": i},
			SenderType: "agent", SenderID: "hubu/main", SenderName: "data",
		})
		if err != nil {
			t.Fatalf("AppendGroupMessage %d: %v", i, err)
		}
	}

	rows, hasMore, next, err := svc.ListGroupMessages(tenantID, "grp_test_1", 0, 6)
	if err != nil {
		t.Fatalf("ListGroupMessages: %v", err)
	}
	if !hasMore {
		t.Fatalf("hasMore = false, want true")
	}
	if len(rows) != 6 || rows[0].Content != "msg-06" || rows[5].Content != "msg-11" {
		t.Fatalf("page 1 = %+v", rows)
	}
	if rows[0].Kind != "ask_user" || rows[1].Kind != "answer" {
		t.Fatalf("kinds = %q %q", rows[0].Kind, rows[1].Kind)
	}
	if rows[0].SenderName != "data" || rows[0].Payload["i"] != float64(6) {
		t.Fatalf("envelope lost: %+v", rows[0])
	}

	rows2, hasMore2, _, err := svc.ListGroupMessages(tenantID, "grp_test_1", next, 6)
	if err != nil {
		t.Fatalf("ListGroupMessages page 2: %v", err)
	}
	if hasMore2 {
		t.Fatalf("page 2 hasMore = true, want false")
	}
	if len(rows2) != 6 || rows2[0].Content != "msg-00" || rows2[5].Content != "msg-05" {
		t.Fatalf("page 2 = %+v", rows2)
	}

	// 不同 tenant 的会话库隔离：同一 group_id 在 tenant 8 里读不到。
	if rows, _, _, _ := svc.ListGroupMessages(8, "grp_test_1", 0, 50); len(rows) != 0 {
		t.Fatalf("cross-tenant read leaked %d rows", len(rows))
	}
	if ok, _ := svc.GroupMessagesExist(tenantID, "grp_test_1"); !ok {
		t.Fatalf("GroupMessagesExist = false")
	}
}

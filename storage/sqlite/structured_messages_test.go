package sqlite

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func newStructuredTestService(t *testing.T) (*SessionService, int64) {
	t.Helper()
	db, err := OpenSessionDB(filepath.Join(t.TempDir(), "session.db"))
	if err != nil {
		t.Fatalf("OpenSessionDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewSessionService(db), 1
}

func askPayloadJSON(t *testing.T, correlation string, multi bool) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"payload": map[string]any{
			"questions": []any{map[string]any{
				"id": "q1", "question": "是否扩容？", "multi_select": multi,
				"allow_other": true, "required": true,
				"options": []any{
					map[string]any{"id": "approve", "label": "准"},
					map[string]any{"id": "reject", "label": "驳"},
				},
			}},
			"allow_cancel": true,
		},
		"correlation_id": correlation,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestAppendStructuredMessageIdempotent(t *testing.T) {
	svc, tenant := newStructuredTestService(t)
	m := StructuredMessage{
		ChatID: "chat-1", Kind: StructuredKindAskUser, CorrelationID: "cor_1",
		IdempotencyKey: "hubu:q1", StructuredJSON: askPayloadJSON(t, "cor_1", false),
		Direction: "in", Content: "是否扩容？", SenderType: "agent", SenderID: "hubu/main",
	}
	id1, created1, err := svc.AppendStructuredMessage(tenant, m)
	if err != nil {
		t.Fatal(err)
	}
	if !created1 {
		t.Fatal("first insert must be created")
	}
	id2, created2, err := svc.AppendStructuredMessage(tenant, m)
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Fatal("second insert with same idempotency key must not create")
	}
	if id1 != id2 {
		t.Fatalf("idempotent insert ids differ: %d vs %d", id1, id2)
	}
	var n int
	if err := svc.db.Conn().QueryRow(
		`SELECT COUNT(1) FROM session_messages WHERE tenant_id=? AND idempotency_key=?`,
		tenant, "hubu:q1").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows for idempotency key = %d, want 1", n)
	}
}

func TestAppendStructuredMessageInvalidKind(t *testing.T) {
	svc, tenant := newStructuredTestService(t)
	if _, _, err := svc.AppendStructuredMessage(tenant, StructuredMessage{ChatID: "c", Kind: "bogus"}); err == nil {
		t.Fatal("invalid kind must be rejected")
	}
}

func TestAnswerAskUserDoubleClickOnlyOnce(t *testing.T) {
	svc, tenant := newStructuredTestService(t)
	_, _, err := svc.AppendStructuredMessage(tenant, StructuredMessage{
		ChatID: "chat-1", Kind: StructuredKindAskUser, CorrelationID: "cor_1",
		StructuredJSON: askPayloadJSON(t, "cor_1", false), Content: "是否扩容？",
	})
	if err != nil {
		t.Fatal(err)
	}
	id1, _, created1, err := svc.AnswerAskUser(tenant, "chat-1", "cor_1", "q1:approve",
		`{"answers":[{"question_id":"q1","option_ids":["approve"]}]}`, "ans-1", "web-1", "web-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if !created1 {
		t.Fatal("first answer must be created")
	}
	// 双击：同一 idempotency_key 只生效一次（返回已有行）。
	id2, _, created2, err := svc.AnswerAskUser(tenant, "chat-1", "cor_1", "q1:approve",
		`{"answers":[{"question_id":"q1","option_ids":["approve"]}]}`, "ans-1", "web-1", "web-1", false)
	if err != nil {
		t.Fatalf("duplicate answer should be idempotent: %v", err)
	}
	if created2 || id1 != id2 {
		t.Fatalf("duplicate answer created=%v id1=%d id2=%d", created2, id1, id2)
	}
	// 不同 key 重复回答 → 拒绝。
	if _, _, _, err := svc.AnswerAskUser(tenant, "chat-1", "cor_1", "again",
		`{}`, "ans-2", "web-1", "web-1", false); !errors.Is(err, ErrAskUserAnswered) {
		t.Fatalf("repeat answer error = %v, want ErrAskUserAnswered", err)
	}
	var answers int
	if err := svc.db.Conn().QueryRow(
		`SELECT COUNT(1) FROM session_messages WHERE tenant_id=? AND kind='answer'`,
		tenant).Scan(&answers); err != nil {
		t.Fatal(err)
	}
	if answers != 1 {
		t.Fatalf("answer rows = %d, want 1", answers)
	}
}

func TestAnswerRearAfterCancelRejected(t *testing.T) {
	svc, tenant := newStructuredTestService(t)
	_, _, err := svc.AppendStructuredMessage(tenant, StructuredMessage{
		ChatID: "chat-1", Kind: StructuredKindAskUser, CorrelationID: "cor_cancel",
		StructuredJSON: askPayloadJSON(t, "cor_cancel", false), Content: "是否扩容？",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.MarkAskUserCancelled(tenant, "chat-1", "cor_cancel", "no", "cancel-1", "web-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.AnswerAskUser(tenant, "chat-1", "cor_cancel", "q1:approve",
		`{}`, "ans-late", "web-1", "web-1", false); !errors.Is(err, ErrAskUserCancelled) {
		t.Fatalf("answer after cancel error = %v, want ErrAskUserCancelled", err)
	}
}

func TestAnswerMissingAskUserNotFound(t *testing.T) {
	svc, tenant := newStructuredTestService(t)
	if _, _, _, err := svc.AnswerAskUser(tenant, "chat-1", "cor_missing", "x", `{}`, "k", "web-1", "web-1", false); !errors.Is(err, ErrStructuredNotFound) {
		t.Fatalf("error = %v, want ErrStructuredNotFound", err)
	}
}

func TestInboxOnlyUnansweredAskUserAndCursorPaging(t *testing.T) {
	svc, tenant := newStructuredTestService(t)
	for i := 0; i < 5; i++ {
		cor := fmt.Sprintf("cor_%d", i)
		if _, _, err := svc.AppendStructuredMessage(tenant, StructuredMessage{
			ChatID: "chat-1", Kind: StructuredKindAskUser, CorrelationID: cor,
			StructuredJSON: askPayloadJSON(t, cor, false), Content: "问题",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 回答一条 → 收件箱不再包含它。
	if _, _, _, err := svc.AnswerAskUser(tenant, "chat-1", "cor_2", "ok", `{}`, "a", "web-1", "web-1", false); err != nil {
		t.Fatal(err)
	}
	page1, more, next, err := svc.ListInbox(tenant, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || !more {
		t.Fatalf("page1 len=%d more=%v, want 2/true", len(page1), more)
	}
	for _, m := range page1 {
		if m.Answered || m.Cancelled {
			t.Fatalf("inbox returned answered/cancelled row: %+v", m)
		}
		if m.CorrelationID == "cor_2" {
			t.Fatal("inbox returned answered correlation cor_2")
		}
	}
	page2, _, _, err := svc.ListInbox(tenant, next, 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, m := range append(page1, page2...) {
		if seen[m.ID] {
			t.Fatalf("cursor paging duplicated id %d", m.ID)
		}
		seen[m.ID] = true
	}
	if len(seen) != 4 {
		t.Fatalf("inbox total = %d, want 4 (5 asked - 1 answered)", len(seen))
	}
	if n, err := svc.CountInbox(tenant); err != nil || n != 4 {
		t.Fatalf("CountInbox = %d, %v; want 4", n, err)
	}
}

func TestStructuredReplyToDanglingAllowed(t *testing.T) {
	svc, tenant := newStructuredTestService(t)
	// reply_to 指向不存在的行：允许（线程允许悬空引用），不 panic。
	id, _, err := svc.AppendStructuredMessage(tenant, StructuredMessage{
		ChatID: "chat-1", Kind: StructuredKindAnswer, CorrelationID: "cor_1",
		ReplyTo: "99999", Content: "答案",
	})
	if err != nil {
		t.Fatalf("dangling reply_to must be allowed: %v", err)
	}
	m, err := svc.GetStructuredMessageByID(tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	if m.ReplyTo != "99999" {
		t.Fatalf("reply_to = %q", m.ReplyTo)
	}
}

func TestListStructuredMessagesByKind(t *testing.T) {
	svc, tenant := newStructuredTestService(t)
	kinds := []string{StructuredKindText, StructuredKindReport, StructuredKindReport, StructuredKindEvent}
	for i, k := range kinds {
		if _, _, err := svc.AppendStructuredMessage(tenant, StructuredMessage{
			ChatID: "chat-1", Kind: k, Content: fmt.Sprintf("m%d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, more, _, err := svc.ListStructuredMessages(tenant, StructuredKindReport, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if more || len(rows) != 2 {
		t.Fatalf("report rows = %d more=%v, want 2/false", len(rows), more)
	}
}

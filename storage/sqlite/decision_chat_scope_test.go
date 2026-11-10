package sqlite

import (
	"errors"
	"strconv"
	"testing"
)

// decision_chat_scope_test.go —— 批复的目标行必须与 decision 落在**同一个会话**。
//
// 2026-10-22 review 发现：AppendDecision 的目标查询与 CAS 都只用
// (tenant_id, id)，而 `session_messages.id` 只在**会话库内**自增、跨会话不唯一。
// 于是 A 会话的批复可以关掉 B 会话的同号 report：decision 写进 A，B 的收件箱
// 却被静默收敛 —— 这正是「批复错位」的另一半根因（P0）。
//
// 修复：查询与 UPDATE 都追加 `chat_id = ?`；目标不在本会话时按「本会话没有这条
// 消息」处理（ErrStructuredNotFound），且不得留副作用。

func TestAppendDecisionRejectsCrossChatTarget(t *testing.T) {
	svc, tenant := newStructuredTestService(t)

	rid, _, err := svc.AppendStructuredMessage(tenant, StructuredMessage{
		ChatID: "chat-1", Kind: StructuredKindReport, CorrelationID: "cross-1",
		Direction: "in", Content: "chat-1 的 report", SenderType: "agent", SenderID: "web-4/team.data",
	})
	if err != nil {
		t.Fatalf("append report: %v", err)
	}
	replyTo := strconv.FormatInt(rid, 10)

	// decision 落在 chat-2，reply_to 指向 chat-1 的行 → 必须失败。
	_, _, err = svc.AppendDecision(tenant, StructuredMessage{
		ChatID: "chat-2", Kind: StructuredKindDecision, ReplyTo: replyTo,
		Direction: "in", Content: "准", SenderType: "user", SenderID: "web-4",
	})
	if !errors.Is(err, ErrStructuredNotFound) {
		t.Fatalf("cross-chat decision: got %v, want ErrStructuredNotFound", err)
	}

	// 失败不得留副作用：chat-1 的 report 必须仍是 answered=0。
	var answered int
	if err := svc.db.Conn().QueryRow(
		`SELECT COALESCE(answered,0) FROM session_messages WHERE id = ?`, rid).Scan(&answered); err != nil {
		t.Fatal(err)
	}
	if answered != 0 {
		t.Fatalf("failed cross-chat decision must not mark the target answered (got %d)", answered)
	}

	// 同会话批复仍然成功（防止修过头）。
	if _, created, err := svc.AppendDecision(tenant, StructuredMessage{
		ChatID: "chat-1", Kind: StructuredKindDecision, ReplyTo: replyTo,
		Direction: "in", Content: "准", SenderType: "user", SenderID: "web-4",
	}); err != nil || !created {
		t.Fatalf("same-chat decision should succeed: created=%v err=%v", created, err)
	}
}

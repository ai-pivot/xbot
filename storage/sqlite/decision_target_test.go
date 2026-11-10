package sqlite

import (
	"errors"
	"testing"

	"xbot/llm"
)

// decision_target_test.go — 回归 2026-10-22 的批复越权漏洞。
//
// 事故：顶层会话用 reply_to=<id> 批复，那条 id 实际是会话里的一条 assistant 空行
// （kind=""）。CAS 只校验 answered/cancelled，于是"批复成功"，把一行普通消息
// 标成了 answered=1 —— 批复看似成功，实际批错了对象，且没有报错。
//
// 修复：目标必须是 report / ask_user（Go 侧校验 + SQL CAS 双保险）。

func TestAppendDecisionRejectsNonDecidableTarget(t *testing.T) {
	svc, tenant := newStructuredTestService(t)

	// 造一条普通对话消息（kind 为空，role=assistant），模拟"空行"。
	plain, err := svc.AppendMessage(tenant, llm.ChatMessage{Role: "assistant", Content: "普通回复"})
	if err != nil {
		t.Fatalf("append plain message: %v", err)
	}

	_, _, err = svc.AppendDecision(tenant, StructuredMessage{
		ChatID: "chat-1", Kind: StructuredKindDecision, ReplyTo: itoa(plain),
		Direction: "in", Content: "准", SenderType: "user", SenderID: "web-4",
	})
	if !errors.Is(err, ErrDecisionTargetNotDecidable) {
		t.Fatalf("decision on a plain message: got %v, want ErrDecisionTargetNotDecidable", err)
	}

	// 校验失败不能留下副作用：目标行必须仍是 answered=0。
	var answered int
	if err := svc.db.Conn().QueryRow(
		`SELECT COALESCE(answered,0) FROM session_messages WHERE id = ?`, plain).Scan(&answered); err != nil {
		t.Fatal(err)
	}
	if answered != 0 {
		t.Fatalf("failed decision must not mark the target answered (got answered=%d)", answered)
	}
}

func TestAppendDecisionAcceptsReport(t *testing.T) {
	svc, tenant := newStructuredTestService(t)

	rid, _, err := svc.AppendStructuredMessage(tenant, StructuredMessage{
		ChatID: "chat-1", Kind: StructuredKindReport, CorrelationID: "hubu-1",
		Direction: "in", Content: "盘库完成", SenderType: "agent", SenderID: "hubu/main",
	})
	if err != nil {
		t.Fatalf("append report: %v", err)
	}

	_, created, err := svc.AppendDecision(tenant, StructuredMessage{
		ChatID: "chat-1", Kind: StructuredKindDecision, ReplyTo: itoa(rid),
		Direction: "in", Content: "准", SenderType: "user", SenderID: "web-4",
	})
	if err != nil || !created {
		t.Fatalf("decision on report: created=%v err=%v", created, err)
	}

	var answered int
	if err := svc.db.Conn().QueryRow(
		`SELECT COALESCE(answered,0) FROM session_messages WHERE id = ?`, rid).Scan(&answered); err != nil {
		t.Fatal(err)
	}
	if answered != 1 {
		t.Fatalf("report should be answered=1, got %d", answered)
	}

	// 重复批复必须被拒（CAS）。
	if _, _, err := svc.AppendDecision(tenant, StructuredMessage{
		ChatID: "chat-1", Kind: StructuredKindDecision, ReplyTo: itoa(rid),
		Direction: "in", Content: "改判", SenderType: "user", SenderID: "web-4",
	}); !errors.Is(err, ErrAskUserAnswered) {
		t.Fatalf("second decision: got %v, want ErrAskUserAnswered", err)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestAppendDecisionRejectsSelfApproval — 安全约束：发送方不能批复自己发出的 report。
// 2026-10-22 安全审计可复现：同租户、目标非"自身会话"，旧实现会放行。
func TestAppendDecisionRejectsSelfApproval(t *testing.T) {
	svc, tenant := newStructuredTestService(t)

	rid, _, err := svc.AppendStructuredMessage(tenant, StructuredMessage{
		ChatID: "chat-1", Kind: StructuredKindReport, CorrelationID: "self-1",
		Direction: "in", Content: "我自己报的", SenderType: "agent", SenderID: "agent:team.data/main",
	})
	if err != nil {
		t.Fatalf("append report: %v", err)
	}

	_, _, err = svc.AppendDecision(tenant, StructuredMessage{
		ChatID: "chat-1", Kind: StructuredKindDecision, ReplyTo: itoa(rid),
		Direction: "in", Content: "准", SenderType: "agent", SenderID: "agent:team.data/main",
	})
	if !errors.Is(err, ErrDecisionSelfApproval) {
		t.Fatalf("self approval: got %v, want ErrDecisionSelfApproval", err)
	}

	// 顶层会话（不同 sender）可以批
	if _, created, err := svc.AppendDecision(tenant, StructuredMessage{
		ChatID: "chat-1", Kind: StructuredKindDecision, ReplyTo: itoa(rid),
		Direction: "in", Content: "准", SenderType: "user", SenderID: "web-4",
	}); err != nil || !created {
		t.Fatalf("supervisor approval should pass: created=%v err=%v", created, err)
	}
}

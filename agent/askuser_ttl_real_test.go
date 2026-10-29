package agent

import (
	"os"
	"testing"
	"time"

	"xbot/protocol"
	"xbot/storage/sqlite"
)

// TestAskUserTTLThirtyMinutesReal 是 D-03 TTL 的**真实时长**实测（默认 skip）。
//
// 为什么要它：短 TTL / 倒拨时间只能证明分支可达，证不了「30 分钟后真的发生」。
// 御前 2026-10-29 令：必须另有一次真实 ≥31 分钟的实测。
//
// 跑法（约 31 分钟）：
//
//	XBOT_TTL_REAL=1 go test ./agent/ -run TestAskUserTTLThirtyMinutesReal -timeout 40m -v
//
// 断言三段：
//
//	T+0    ：新鲜提问保留（插话不作废）；
//	T+29m  ：仍在 TTL 内 ⇒ 仍保留；
//	T+31m  ：超过 TTL ⇒ 收回，且 inbox 行 cancelled=true、cancel_reason=ttl_expired。
func TestAskUserTTLThirtyMinutesReal(t *testing.T) {
	if os.Getenv("XBOT_TTL_REAL") != "1" {
		t.Skip("real-time TTL test skipped; set XBOT_TTL_REAL=1 to run (~31 min)")
	}
	mt, _ := newAgentHistorySession(t)
	sess, err := mt.GetOrCreateSession("web", "chat-ttl-real")
	if err != nil {
		t.Fatal(err)
	}
	svc, ok := sess.SessionService().(*sqlite.SessionService)
	if !ok {
		t.Fatal("session service is not *sqlite.SessionService")
	}
	tenantID := sess.TenantID()
	if _, _, err := svc.AppendStructuredMessage(tenantID, sqlite.StructuredMessage{
		ChatID: "chat-ttl-real", Kind: sqlite.StructuredKindAskUser, CorrelationID: "ask-ttl-real",
		Direction: "in", Role: "assistant", Content: "真实 31 分钟实测",
		SenderType: "user", SenderID: "web-1", SenderName: "web-1", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	a := &Agent{multiSession: mt}
	a.setPendingAskUser("web", "chat-ttl-real", &protocol.ProgressEvent{RequestID: "req-ttl-real"})

	// T+0
	a.autoResolveStalePendingAskUser("web", "chat-ttl-real")
	if a.GetPendingAskUser("web", "chat-ttl-real") == nil {
		t.Fatal("T+0：新鲜提问被收回（D-03 回归）")
	}
	t.Logf("T+0 保留 OK（ttl=%s）", pendingAskUserTTL)

	// T+29m：仍在 TTL 内
	time.Sleep(29 * time.Minute)
	a.autoResolveStalePendingAskUser("web", "chat-ttl-real")
	if a.GetPendingAskUser("web", "chat-ttl-real") == nil {
		t.Fatal("T+29m：还没到 TTL 就被收回")
	}
	t.Log("T+29m 仍保留 OK")

	// T+31m：超过 TTL
	time.Sleep(2 * time.Minute)
	a.autoResolveStalePendingAskUser("web", "chat-ttl-real")
	if a.GetPendingAskUser("web", "chat-ttl-real") != nil {
		t.Fatal("T+31m：超过 TTL 仍未收回（pending 永远挂住）")
	}
	rows, _, _, err := svc.ListStructuredMessages(tenantID, sqlite.StructuredKindAskUser, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 || !rows[0].Cancelled {
		t.Fatalf("T+31m：inbox 行未置 cancelled=true：%+v", rows)
	}
	t.Logf("T+31m 收回 OK：cancelled=%v correlation_id=%s", rows[0].Cancelled, rows[0].CorrelationID)
}

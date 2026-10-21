package agent

import (
	"context"
	"testing"

	"xbot/bus"
	ch "xbot/channel"
	"xbot/session"
	"xbot/tools"
)

// 判据（2026-10-22 用户 P0「cancel 后发新消息 ⇒ 历史变成 user1 user2 + iter1-20 busy」）：
// **cancel ack 是"被取消那一轮"的权威收尾帧，必须带 TurnID。**
//
// 根因：`handleCancelledRun` 直接返回 `&channel.OutboundMsg{}`，绕过了 `sendMessage`
// 的 turn_id 盖章 ⇒ SSE 载荷里 `turn_id` 缺省（`omitempty`）。后果按客户端而异：
//   - web（`reduce.ts`）回落到 `activeTurn` —— 碰巧对上，所以 web 看不出问题；
//   - 原生（模型链路，行源）没有兜底 turn ⇒ 整帧丢弃 ⇒ 该回合永远没有 `final`
//     ⇒ 行恒 `turn-N-live`（`isLive`）⇒ cancel 后发新消息时那条 assistant 被当成
//     "在飞尾项"搬到列表末尾、并一直 busy。
//
// ⛔ mutation 自证：删掉 cancel ack 的 `TurnID:` 行 ⇒ 本文件 ①② 全红。

// newCancelAckAgent 建一个能跑完整 handleCancelledRun 的最小 Agent（真 SQLite 会话）。
func newCancelAckAgent(t *testing.T) (*Agent, *session.TenantSession) {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	multiSession, err := session.NewMultiTenant(dbPath)
	if err != nil {
		t.Fatalf("NewMultiTenant: %v", err)
	}
	t.Cleanup(func() { multiSession.Close() })

	a := &Agent{
		multiSession: multiSession,
		todoManager:  tools.NewTodoManager(),
		channelFinder: func(name string) (ch.Channel, bool) {
			return nil, false
		},
	}
	sess, err := multiSession.GetOrCreateSession("web", "test-chat")
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}
	return a, sess
}

// ① 入站 metadata 里的 turn_id（= RunConfig.TurnID）必须出现在 cancel ack 上。
func TestCancelAckCarriesTurnIDFromMetadata(t *testing.T) {
	a, sess := newCancelAckAgent(t)
	msg := bus.InboundMessage{
		Channel:  "web",
		ChatID:   "test-chat",
		SenderID: "u1",
		Content:  "do something",
		Metadata: map[string]string{"turn_id": "7"},
	}
	out, err := a.handleCancelledRun(context.Background(), msg, &RunOutput{}, sess)
	if err != nil {
		t.Fatalf("handleCancelledRun: %v", err)
	}
	if out.Metadata == nil || out.Metadata["cancelled"] != "true" {
		t.Fatalf("cancel ack 必须仍带 cancelled=true，got %#v", out.Metadata)
	}
	if out.TurnID != 7 {
		t.Fatalf("cancel ack 必须带被取消那一轮的 TurnID（否则前端无法收尾该回合）；got TurnID=%d want 7",
			out.TurnID)
	}
}

// ② metadata 缺 turn_id 时，回落到该会话的 activeTurnID（cancel 请求自身的元数据可能不带）。
func TestCancelAckTurnIDFallsBackToActiveTurn(t *testing.T) {
	a, sess := newCancelAckAgent(t)
	const chatID = "test-chat"
	const want = uint64(9)
	ss := &bgSessionState{}
	ss.activeTurnID.Store(want)
	a.bgSessionStates.Store(qualifyChatID("web", chatID), ss)
	t.Cleanup(func() { a.bgSessionStates.Delete(qualifyChatID("web", chatID)) })

	msg := bus.InboundMessage{Channel: "web", ChatID: chatID, SenderID: "u1", Content: "do something"}
	out, err := a.handleCancelledRun(context.Background(), msg, &RunOutput{}, sess)
	if err != nil {
		t.Fatalf("handleCancelledRun: %v", err)
	}
	if out.TurnID != want {
		t.Fatalf("cancel ack 必须回落到 activeTurnID；got TurnID=%d want %d", out.TurnID, want)
	}
}

// ③ 规则唯一实现 `outboundTurnID` 的优先级（显式 > metadata > active；命令回复恒 0）。
func TestOutboundTurnIDPrecedence(t *testing.T) {
	const chatID = "test-chat"
	const active = uint64(3)
	newAgent := func() *Agent {
		a := &Agent{}
		ss := &bgSessionState{}
		ss.activeTurnID.Store(active)
		a.bgSessionStates.Store(qualifyChatID("web", chatID), ss)
		return a
	}
	cases := []struct {
		name     string
		meta     map[string]string
		explicit uint64
		want     uint64
	}{
		{"显式优先于一切", map[string]string{"turn_id": "5"}, 8, 8},
		{"metadata turn_id", map[string]string{"turn_id": "5"}, 0, 5},
		{"回落 active", nil, 0, active},
		{"metadata 非法 ⇒ 回落 active", map[string]string{"turn_id": "abc"}, 0, active},
		{"命令回复恒 0（不得借用 active）", map[string]string{"command_reply": "true"}, 0, 0},
		{"命令回复即使带 turn_id 也恒 0", map[string]string{"command_reply": "true", "turn_id": "5"}, 0, 0},
	}
	for _, c := range cases {
		got := newAgent().outboundTurnID("web", chatID, c.meta, c.explicit)
		if got != c.want {
			t.Errorf("%s: outboundTurnID = %d, want %d", c.name, got, c.want)
		}
	}
}

package main

// 约束 6 的单测：断线重连必须发 **op=6 Resume**（不是重新 Identify）。
//
// 官方（event-emit.html §恢复登录态 Session）：
// 「断开重连 gateway 后不需要发送重新登录 OpCode 2 Identify 请求，需要发送
//  OpCode 6 Resume」，字段 token / session_id / seq（= 最后收到的 s）。
// 成功后服务端先补发 seq 之后遗漏的事件，最后下发 RESUMED。
//
// 御前指定验收：
//   1. 断线后发 op6，session_id / seq 与断线前一致；
//   2. 断线期间的消息在 Resume 后补发进来（断线期间塞 2 条 → Resume 后都收到）。

import (
	"context"
	"testing"
	"time"
)

// newResumeGateway 构造一个"重连两次就停"的网关：每条连接一个 fakeConn。
func newResumeGateway(t *testing.T, conns []*fakeConn, events chan GatewayEvent) (*Gateway, *recordingTokens, *countingDialer) {
	t.Helper()
	dialer := &countingDialer{make: func(call int) (WSConn, error) {
		if call-1 < len(conns) {
			return conns[call-1], nil
		}
		return nil, context.Canceled
	}}
	tokens := &recordingTokens{}
	gw := NewGateway(tokens, func(context.Context) (string, error) {
		return "wss://example.invalid/ws", nil
	}, dialer, func(_ context.Context, ev GatewayEvent) { events <- ev }, t.Logf)

	// 只放行前 len(conns)-1 次退避，跑完这些连接就让 Run 结束。
	allowed := len(conns) - 1
	var slept int
	gw.cfg = GatewayConfig{
		Reconnect: ReconnectPolicy{Min: time.Millisecond, Max: time.Millisecond},
		Sleep: func(context.Context, time.Duration) bool {
			slept++
			return slept <= allowed
		},
	}
	return gw, tokens, dialer
}

// TestGatewayResumeAfterDisconnect 首次 Identify 建立会话 → 断线 → 重连发 op6
// Resume（session_id/seq 与断线前一致）→ 补发的 2 条事件都进来 → RESUMED。
func TestGatewayResumeAfterDisconnect(t *testing.T) {
	first := newFakeConn()
	second := newFakeConn()
	events := make(chan GatewayEvent, 8)

	gw, _, dialer := newResumeGateway(t, []*fakeConn{first, second}, events)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = gw.Run(ctx) }()

	// ── 第一条连接：Hello → Identify → READY(seq=1) → 一个业务事件(seq=5) ──
	first.inject(t, helloFrame(400))
	if op := int(first.next(t)["op"].(float64)); op != opIdentify {
		t.Fatalf("first connect: op = %d, want identify (no session yet)", op)
	}
	first.inject(t, readyFrame())
	first.inject(t, dispatchFrame(EventC2CMessageCreate, 5, "ev-1",
		`{"id":"m-1","content":"hi","author":{"user_openid":"U1"}}`))
	ev := waitEvent(t, events)
	if ev.Sequence != 5 {
		t.Fatalf("first event seq = %d, want 5", ev.Sequence)
	}
	// 断线（对端关闭）。
	_ = first.Close()

	// ── 第二条连接：Hello → **op6 Resume**（session_id=sess-1, seq=5）──
	second.inject(t, helloFrame(400))
	resume := second.next(t)
	if op := int(resume["op"].(float64)); op != opResume {
		t.Fatalf("reconnect op = %d, want resume (%d) — 重新 Identify 会丢断线期间的消息", op, opResume)
	}
	d := resume["d"].(map[string]any)
	if d["session_id"] != "sess-1" {
		t.Fatalf("resume session_id = %v, want sess-1 (断线前的 session)", d["session_id"])
	}
	if seq := int(d["seq"].(float64)); seq != 5 {
		t.Fatalf("resume seq = %d, want 5 (断线前最后收到的 s)", seq)
	}
	// 普通断线不需要重取 token（只有 op=9 才 Invalidate），沿用 tok-1。
	if d["token"] != "QQBot tok-1" {
		t.Fatalf("resume token = %v, want reused QQBot tok-1", d["token"])
	}

	// ── 服务端补发断线期间的 2 条事件（seq 6、7），再下发 RESUMED ──
	second.inject(t, dispatchFrame(EventC2CMessageCreate, 6, "ev-2",
		`{"id":"m-2","content":"missed-1","author":{"user_openid":"U1"}}`))
	second.inject(t, dispatchFrame(EventC2CMessageCreate, 7, "ev-3",
		`{"id":"m-3","content":"missed-2","author":{"user_openid":"U1"}}`))
	second.inject(t, `{"op":0,"s":7,"t":"RESUMED","d":{}}`)

	got := []GatewayEvent{waitEvent(t, events), waitEvent(t, events)}
	if got[0].ID != "ev-2" || got[1].ID != "ev-3" {
		t.Fatalf("backfilled events = %+v, want ev-2 then ev-3", got)
	}
	if got[0].Sequence != 6 || got[1].Sequence != 7 {
		t.Fatalf("backfilled seq = %d,%d want 6,7", got[0].Sequence, got[1].Sequence)
	}

	// 心跳必须带补发后的最新 seq（7），证明 Resume 后续接在正确位置。
	hb := second.next(t)
	if op := int(hb["op"].(float64)); op != opHeartbeat {
		t.Fatalf("after RESUMED: op = %d, want heartbeat", op)
	}
	if seq, ok := hb["d"].(float64); !ok || int(seq) != 7 {
		t.Fatalf("heartbeat seq = %v, want 7", hb["d"])
	}
	second.inject(t, ackFrame())

	// 清理：取消 ctx，Run 退出。
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("gateway Run did not exit after cancel")
	}
	if dialer.Calls() < 2 {
		t.Fatalf("dial calls = %d, want >= 2", dialer.Calls())
	}
}

// TestGatewayOp9ClearsSessionAndReIdentifies op=9（Invalid Session）后必须清掉会话：
// 下次连接发 Identify，而不是拿作废的 session_id 去 Resume。
func TestGatewayOp9ClearsSessionAndReIdentifies(t *testing.T) {
	first := newFakeConn()
	second := newFakeConn()
	events := make(chan GatewayEvent, 8)

	gw, tokens, _ := newResumeGateway(t, []*fakeConn{first, second}, events)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = gw.Run(ctx) }()

	first.inject(t, helloFrame(400))
	if op := int(first.next(t)["op"].(float64)); op != opIdentify {
		t.Fatalf("first op = %d, want identify", op)
	}
	first.inject(t, readyFrame())
	first.inject(t, dispatchFrame(EventC2CMessageCreate, 3, "ev-1",
		`{"id":"m-1","content":"hi","author":{"user_openid":"U1"}}`))
	waitEvent(t, events)

	// op=9：会话作废。
	first.inject(t, op9Frame())

	// 第二条连接：必须先 Identify（session 已清），且 token 已重取（tok-2）。
	second.inject(t, helloFrame(400))
	frame := second.next(t)
	if op := int(frame["op"].(float64)); op != opIdentify {
		t.Fatalf("after op=9: op = %d, want identify (%d) — 作废的 session 不能用于 Resume", op, opIdentify)
	}
	if d := frame["d"].(map[string]any); d["token"] != "QQBot tok-2" {
		t.Fatalf("after op=9: token = %v, want re-fetched tok-2", d["token"])
	}
	if n := tokens.Invalidations(); n != 1 {
		t.Fatalf("token invalidations = %d, want 1 (op=9 必须丢弃 token)", n)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("gateway Run did not exit after cancel")
	}
}

// TestGatewayServerReconnectKeepsSession op=7（服务端要求重连）不是会话失效：
// 必须保留 session_id，下次连接继续用 Resume。
func TestGatewayServerReconnectKeepsSession(t *testing.T) {
	first := newFakeConn()
	second := newFakeConn()
	events := make(chan GatewayEvent, 8)

	gw, _, _ := newResumeGateway(t, []*fakeConn{first, second}, events)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = gw.Run(ctx) }()

	first.inject(t, helloFrame(400))
	first.next(t)
	first.inject(t, readyFrame())
	first.inject(t, dispatchFrame(EventC2CMessageCreate, 9, "ev-1",
		`{"id":"m-1","content":"hi","author":{"user_openid":"U1"}}`))
	waitEvent(t, events)

	first.inject(t, `{"op":7}`) // server requests reconnect

	second.inject(t, helloFrame(400))
	frame := second.next(t)
	if op := int(frame["op"].(float64)); op != opResume {
		t.Fatalf("after op=7: op = %d, want resume", op)
	}
	if d := frame["d"].(map[string]any); d["session_id"] != "sess-1" {
		t.Fatalf("after op=7: session_id = %v, want sess-1", d["session_id"])
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("gateway Run did not exit after cancel")
	}
}

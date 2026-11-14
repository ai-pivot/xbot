package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 假件
// ---------------------------------------------------------------------------

// recordingTokens 记录每次发出的 token 与 Invalidate 次数。
type recordingTokens struct {
	mu            sync.Mutex
	gen           int
	issued        []string
	invalidations int
}

func (t *recordingTokens) Token(context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tok := fmt.Sprintf("tok-%d", t.gen+1)
	t.issued = append(t.issued, tok)
	return tok, nil
}

func (t *recordingTokens) Invalidate() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gen++
	t.invalidations++
}

func (t *recordingTokens) Issued() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.issued))
	copy(out, t.issued)
	return out
}

func (t *recordingTokens) Invalidations() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.invalidations
}

// fakeConn 是内存 WS 连接：测试往 in 里塞下行帧，从 out 读上行帧。
type fakeConn struct {
	in     chan []byte
	out    chan []byte
	closed chan struct{}
	once   sync.Once
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		in:     make(chan []byte, 16),
		out:    make(chan []byte, 64),
		closed: make(chan struct{}),
	}
}

func (c *fakeConn) ReadMessage() ([]byte, error) {
	select {
	case b := <-c.in:
		return b, nil
	case <-c.closed:
		return nil, io.EOF
	}
}

func (c *fakeConn) WriteMessage(payload []byte) error {
	cp := make([]byte, len(payload))
	copy(cp, payload)
	select {
	case c.out <- cp:
		return nil
	case <-c.closed:
		return io.EOF
	}
}

func (c *fakeConn) SetReadDeadline(time.Time) error { return nil }

func (c *fakeConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// inject 把一行 JSON 塞给网关。
func (c *fakeConn) inject(t *testing.T, raw string) {
	t.Helper()
	select {
	case c.in <- []byte(raw):
	case <-time.After(2 * time.Second):
		t.Fatal("inject timeout: gateway is not reading")
	}
}

// next 读一帧上行 JSON。
func (c *fakeConn) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case raw := <-c.out:
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode upstream frame: %v (raw=%s)", err, raw)
		}
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for upstream frame")
		return nil
	}
}

// countingDialer 按调用次数返回连接或错误。
type countingDialer struct {
	mu    sync.Mutex
	calls int
	make  func(call int) (WSConn, error)
}

func (d *countingDialer) Dial(_ context.Context, _ string, _ http.Header) (WSConn, error) {
	d.mu.Lock()
	d.calls++
	n := d.calls
	d.mu.Unlock()
	return d.make(n)
}

func (d *countingDialer) Calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// ---------------------------------------------------------------------------
// 帧构造
// ---------------------------------------------------------------------------

func helloFrame(intervalMS int) string {
	return fmt.Sprintf(`{"op":10,"d":{"heartbeat_interval":%d}}`, intervalMS)
}

func readyFrame() string {
	return `{"op":0,"s":1,"t":"READY","d":{"session_id":"sess-1","user":{"id":"bot"}}}`
}

func ackFrame() string { return `{"op":11}` }

func op9Frame() string { return `{"op":9,"d":false}` }

func dispatchFrame(t string, seq int64, id string, d string) string {
	return fmt.Sprintf(`{"op":0,"s":%d,"t":%q,"id":%q,"d":%s}`, seq, t, id, d)
}

// waitEvent 等一条业务事件。
func waitEvent(t *testing.T, ch <-chan GatewayEvent) GatewayEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for gateway event")
		return GatewayEvent{}
	}
}

// ---------------------------------------------------------------------------
// 测试
// ---------------------------------------------------------------------------

// TestGatewayLifecycle 覆盖 Hello → Identify → READY → 心跳 → ACK → 事件分发。
func TestGatewayLifecycle(t *testing.T) {
	conn := newFakeConn()
	dialer := &countingDialer{make: func(int) (WSConn, error) { return conn, nil }}
	tokens := &recordingTokens{}
	events := make(chan GatewayEvent, 4)

	gw := NewGateway(tokens, func(context.Context) (string, error) {
		return "wss://example.invalid/ws", nil
	}, dialer, func(_ context.Context, ev GatewayEvent) { events <- ev }, t.Logf)
	gw.cfg = GatewayConfig{
		Reconnect: ReconnectPolicy{Min: time.Millisecond, Max: time.Millisecond, MaxAttempts: 0},
		Sleep:     func(context.Context, time.Duration) bool { return false },
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gw.Run(ctx)

	conn.inject(t, helloFrame(200))

	identify := conn.next(t)
	if op := int(identify["op"].(float64)); op != opIdentify {
		t.Fatalf("op = %d, want identify (%d)", op, opIdentify)
	}
	d := identify["d"].(map[string]any)
	if d["token"] != "QQBot tok-1" {
		t.Fatalf("identify token = %v, want QQBot tok-1", d["token"])
	}
	if int(d["intents"].(float64)) != DefaultIntents {
		t.Fatalf("intents = %v, want %d", d["intents"], DefaultIntents)
	}
	if shard := d["shard"].([]any); len(shard) != 2 || int(shard[0].(float64)) != 0 || int(shard[1].(float64)) != 1 {
		t.Fatalf("shard = %v, want [0,1]", d["shard"])
	}

	conn.inject(t, readyFrame())
	conn.inject(t, dispatchFrame(EventC2CMessageCreate, 5, "ev-1",
		`{"id":"m-1","content":"hi","author":{"user_openid":"U1"}}`))

	ev := waitEvent(t, events)
	if ev.Type != EventC2CMessageCreate || ev.ID != "ev-1" || ev.Sequence != 5 {
		t.Fatalf("event = %+v", ev)
	}

	hb := conn.next(t)
	if op := int(hb["op"].(float64)); op != opHeartbeat {
		t.Fatalf("op = %d, want heartbeat", op)
	}
	if seq, ok := hb["d"].(float64); !ok || int(seq) != 5 {
		t.Fatalf("heartbeat d = %v, want last seq 5", hb["d"])
	}
	conn.inject(t, ackFrame())

	// READY 本身不应当作为业务事件外抛。
	select {
	case extra := <-events:
		t.Fatalf("unexpected event leaked: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestGatewayReconnectsWithExponentialBackoff 连续失败时按 1s/2s/4s 退避，
// 到达 MaxAttempts 后放弃并返回错误。
func TestGatewayReconnectsWithExponentialBackoff(t *testing.T) {
	dialer := &countingDialer{make: func(int) (WSConn, error) {
		return nil, fmt.Errorf("connection refused")
	}}
	tokens := &recordingTokens{}

	var delays []time.Duration
	gw := NewGateway(tokens, func(context.Context) (string, error) {
		return "wss://example.invalid/ws", nil
	}, dialer, nil, nil)
	gw.cfg = GatewayConfig{
		Reconnect: ReconnectPolicy{Min: time.Second, Max: time.Minute, MaxAttempts: 3},
		Sleep: func(_ context.Context, d time.Duration) bool {
			delays = append(delays, d)
			return true
		},
	}

	err := gw.Run(context.Background())
	if err == nil {
		t.Fatal("Run = nil, want error after MaxAttempts")
	}
	// MaxAttempts=3 限制的是重试次数：首次 + 3 次重试 = 4 次连接尝试。
	if got := dialer.Calls(); got != 4 {
		t.Fatalf("dial calls = %d, want 4 (initial + 3 retries)", got)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if len(delays) != len(want) {
		t.Fatalf("delays = %v, want %v", delays, want)
	}
	for i := range want {
		if delays[i] != want[i] {
			t.Fatalf("delays = %v, want %v", delays, want)
		}
	}
}

// TestGatewayOp9RefetchesTokenBeforeReconnect op=9 必须丢 token 重取再重连。
func TestGatewayOp9RefetchesTokenBeforeReconnect(t *testing.T) {
	conn1 := newFakeConn()
	conn2 := newFakeConn()
	dialer := &countingDialer{make: func(call int) (WSConn, error) {
		if call == 1 {
			return conn1, nil
		}
		return conn2, nil
	}}
	tokens := &recordingTokens{}

	gw := NewGateway(tokens, func(context.Context) (string, error) {
		return "wss://example.invalid/ws", nil
	}, dialer, nil, nil)
	gw.cfg = GatewayConfig{
		Reconnect: ReconnectPolicy{Min: time.Millisecond, Max: time.Millisecond, MaxAttempts: 0},
		Sleep:     func(context.Context, time.Duration) bool { return true },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gw.Run(ctx)

	// 第一次连接：Hello → Identify → READY，然后收到 op=9（会话失效）。
	// 放在 READY 之后，专门覆盖主读循环里的 op=9 分支（连上之后才失效）。
	conn1.inject(t, helloFrame(1000))
	if first := conn1.next(t); int(first["op"].(float64)) != opIdentify {
		t.Fatalf("first op = %v, want identify", first["op"])
	}
	conn1.inject(t, readyFrame())
	conn1.inject(t, op9Frame())

	// 第二次连接必须已经换了 token。
	waitFor(t, "second dial", func() bool { return dialer.Calls() >= 2 })
	conn2.inject(t, helloFrame(1000))
	second := conn2.next(t)
	d := second["d"].(map[string]any)
	if d["token"] != "QQBot tok-2" {
		t.Fatalf("second identify token = %v, want QQBot tok-2 (refetched)", d["token"])
	}
	if tokens.Invalidations() != 1 {
		t.Fatalf("invalidations = %d, want 1", tokens.Invalidations())
	}
}

// TestGatewayReconnectsWhenHeartbeatUnacked 心跳没有 ACK 时必须判失联并重连。
func TestGatewayReconnectsWhenHeartbeatUnacked(t *testing.T) {
	conn1 := newFakeConn()
	conn2 := newFakeConn()
	dialer := &countingDialer{make: func(call int) (WSConn, error) {
		if call == 1 {
			return conn1, nil
		}
		return conn2, nil
	}}
	tokens := &recordingTokens{}

	gw := NewGateway(tokens, func(context.Context) (string, error) {
		return "wss://example.invalid/ws", nil
	}, dialer, nil, nil)
	gw.cfg = GatewayConfig{
		Reconnect: ReconnectPolicy{Min: time.Millisecond, Max: time.Millisecond, MaxAttempts: 0},
		Sleep:     func(context.Context, time.Duration) bool { return true },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gw.Run(ctx)

	conn1.inject(t, helloFrame(10)) // 10ms 心跳，故意不回 ACK
	_ = conn1.next(t)               // identify
	conn1.inject(t, readyFrame())

	// 第一个心跳发出后不回 ACK → 下一个 tick 判定失联 → 重连。
	waitFor(t, "reconnect after missed heartbeat", func() bool { return dialer.Calls() >= 2 })
	select {
	case <-conn1.closed:
	default:
		t.Fatal("first connection should be closed after missed heartbeat")
	}
}

// TestGatewaySkipsUnknownOps 未知 op 不应杀死连接。
func TestGatewaySkipsUnknownOps(t *testing.T) {
	conn := newFakeConn()
	dialer := &countingDialer{make: func(int) (WSConn, error) { return conn, nil }}
	tokens := &recordingTokens{}
	events := make(chan GatewayEvent, 4)

	gw := NewGateway(tokens, func(context.Context) (string, error) { return "wss://x/ws", nil },
		dialer, func(_ context.Context, ev GatewayEvent) { events <- ev }, t.Logf)
	gw.cfg = GatewayConfig{Sleep: func(context.Context, time.Duration) bool { return false }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gw.Run(ctx)

	conn.inject(t, helloFrame(1000))
	_ = conn.next(t)
	conn.inject(t, readyFrame())
	conn.inject(t, `{"op":42,"d":{}}`)
	conn.inject(t, dispatchFrame(EventGroupAtMessageCreate, 3, "ev-2",
		`{"id":"m-2","content":"yo","group_openid":"G1","author":{"member_openid":"M1"}}`))

	ev := waitEvent(t, events)
	if ev.Type != EventGroupAtMessageCreate {
		t.Fatalf("event = %+v", ev)
	}
}

// waitFor 轮询直到 cond 为真（测试专用小工具）。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

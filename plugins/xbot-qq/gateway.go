package main

// QQ 官方 WebSocket 网关生命周期。
//
// 协议（docs: 开放平台 → 开发 → WebSocket 连接）：
//
//	1. GET /gateway 拿 wss 地址                  （APIClient.GatewayURL）
//	2. 连上去，收 op=10 Hello（带 heartbeat_interval）
//	3. 发 op=2 Identify（token "QQBot {token}" + intents + shard）
//	4. 收 op=0 t=READY（带 session_id）
//	5. 每 heartbeat_interval 发 op=1 心跳，收 op=11 ACK
//	6. 断线 → 指数退避重连；op=9 Invalid Session → 丢掉 token 重取再重连
//
// 本文件只做生命周期，不解析业务事件（见 events.go 的 ParseEvent）。
// 所有外部依赖都是接口（TokenSource / Dialer / gatewayURL 函数），
// 因此完整生命周期可以在单测里用内存假连接逐步驱动。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// 网关 opcode。
const (
	opDispatch     = 0
	opHeartbeat    = 1
	opIdentify     = 2
	opReconnect    = 7
	opInvalidSess  = 9
	opHello        = 10
	opHeartbeatACK = 11
)

// IntentGroupAndC2C 覆盖 C2C_MESSAGE_CREATE 与 GROUP_AT_MESSAGE_CREATE。
// GROUP_MESSAGE_CREATE（群全量消息）需要在开放平台后台单独申请私域权限，
// 权限下来后事件会走同一个 intent，无需改这里。
const IntentGroupAndC2C = 1 << 25

// DefaultIntents 是默认订阅的 intents。
const DefaultIntents = IntentGroupAndC2C

// GatewayEvent 是一条 op=0 业务事件（READY 由 Gateway 内部消费，不外抛）。
type GatewayEvent struct {
	// Type 是 t 字段：C2C_MESSAGE_CREATE / GROUP_AT_MESSAGE_CREATE / ...
	Type string
	// Data 是 d 原文（解析见 events.go）。
	Data json.RawMessage
	// ID 是 envelope 的 id（事件唯一 id）。
	ID string
	// Sequence 是 envelope 的 s（心跳要回带，用于会话续接）。
	Sequence int64
}

// GatewayConfig 是生命周期参数。
type GatewayConfig struct {
	// Intents 为空时用 DefaultIntents。
	Intents int
	// Reconnect 是断线退避策略；零值用 DefaultReconnectPolicy。
	Reconnect ReconnectPolicy
	// HelloTimeout 是连上后等 op=10 的上限；0 用 15s。
	HelloTimeout time.Duration
	// Sleep 注入等待（单测用它记录退避时长而不真的睡）；nil 用真实定时器。
	Sleep func(ctx context.Context, d time.Duration) bool
}

// Gateway 维护一条到 QQ 网关的长连接，断线自动重连。
//
// Run 阻塞直到 ctx 取消；不返回"正常结束"以外的错误 —— 单次连接错误都被
// 重连逻辑吞掉并记录（渠道进程要长期自愈）。
type Gateway struct {
	tokens     TokenSource
	gatewayURL func(ctx context.Context) (string, error)
	dialer     Dialer
	onEvent    func(ctx context.Context, ev GatewayEvent)
	logf       func(format string, args ...any)

	cfg GatewayConfig
	seq atomic.Int64

	// identify 构造只做一次，便于测试断言。
	shard [2]int
}

// NewGateway 构造网关。
func NewGateway(tokens TokenSource, gatewayURL func(context.Context) (string, error), dialer Dialer, onEvent func(context.Context, GatewayEvent), logf func(string, ...any)) *Gateway {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Gateway{
		tokens:     tokens,
		gatewayURL: gatewayURL,
		dialer:     dialer,
		onEvent:    onEvent,
		logf:       logf,
		shard:      [2]int{0, 1},
	}
}

// Run 连接并维持会话，直到 ctx 取消。
func (g *Gateway) Run(ctx context.Context) error {
	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		ready, err := g.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if ready {
			attempt = 0 // 成功收到 READY = 连接稳定，退避归零
		}
		attempt++

		delay := g.reconnectPolicy().Backoff(attempt)
		if delay <= 0 {
			return fmt.Errorf("qq gateway: giving up after %d attempts: %w", attempt-1, err)
		}
		if err != nil {
			g.logf("gateway disconnected (attempt %d): %v; retrying in %s", attempt, err, delay)
		}
		if !g.sleep(ctx, delay) {
			return nil
		}
	}
}

func (g *Gateway) reconnectPolicy() ReconnectPolicy {
	if g.cfg.Reconnect.Min == 0 && g.cfg.Reconnect.Max == 0 {
		return DefaultReconnectPolicy()
	}
	return g.cfg.Reconnect
}

func (g *Gateway) intents() int {
	if g.cfg.Intents == 0 {
		return DefaultIntents
	}
	return g.cfg.Intents
}

func (g *Gateway) sleep(ctx context.Context, d time.Duration) bool {
	if g.cfg.Sleep != nil {
		return g.cfg.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// session 跑一次完整连接：取 token → /gateway → 连接 → Hello/Identify/READY
// → 心跳 → 直到断开。ready 表示本次是否真的收到了 READY。
func (g *Gateway) session(ctx context.Context) (ready bool, err error) {
	token, err := g.tokens.Token(ctx)
	if err != nil {
		return false, fmt.Errorf("get access token: %w", err)
	}
	wsURL, err := g.gatewayURL(ctx)
	if err != nil {
		return false, fmt.Errorf("get gateway url: %w", err)
	}

	conn, err := g.dialer.Dial(ctx, wsURL, nil)
	if err != nil {
		return false, fmt.Errorf("dial gateway: %w", err)
	}
	defer conn.Close()

	// ctx 取消时关闭连接，把阻塞中的 ReadMessage 叫醒。
	done := make(chan struct{})
	var doneOnce sync.Once
	stop := func() { doneOnce.Do(func() { close(done) }) }
	defer stop()
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	helloTimeout := g.cfg.HelloTimeout
	if helloTimeout <= 0 {
		helloTimeout = 15 * time.Second
	}

	// ── 1. 等 op=10 Hello ──
	if err := conn.SetReadDeadline(time.Now().Add(helloTimeout)); err != nil {
		return false, fmt.Errorf("set read deadline: %w", err)
	}
	env, err := readEnvelope(conn)
	if err != nil {
		return false, fmt.Errorf("wait hello: %w", err)
	}
	if env.Op == opInvalidSess {
		g.tokens.Invalidate()
		return false, errors.New("invalid session before hello")
	}
	if env.Op != opHello {
		return false, fmt.Errorf("expected hello (op=%d), got op=%d", opHello, env.Op)
	}
	var hello struct {
		HeartbeatInterval int64 `json:"heartbeat_interval"`
	}
	if err := json.Unmarshal(env.D, &hello); err != nil {
		return false, fmt.Errorf("parse hello: %w", err)
	}
	interval := time.Duration(hello.HeartbeatInterval) * time.Millisecond
	if interval <= 0 {
		return false, fmt.Errorf("hello carried invalid heartbeat_interval %d", hello.HeartbeatInterval)
	}
	g.logf("gateway hello: heartbeat_interval=%s", interval)

	// ── 2. Identify ──
	identify := map[string]any{
		"op": opIdentify,
		"d": map[string]any{
			"token":   "QQBot " + token,
			"intents": g.intents(),
			"shard":   g.shard,
			"properties": map[string]string{
				"$os":      "linux",
				"$browser": "xbot",
				"$device":  "xbot",
			},
		},
	}
	if err := writeJSONFrame(conn, identify); err != nil {
		return false, fmt.Errorf("send identify: %w", err)
	}

	// ── 3. 等 READY ──
	// READY 之前也可能先来 op=9（凭据/会话失效），必须处理。
	if err := conn.SetReadDeadline(time.Now().Add(helloTimeout)); err != nil {
		return false, fmt.Errorf("set read deadline: %w", err)
	}
	for {
		env, err = readEnvelope(conn)
		if err != nil {
			return false, fmt.Errorf("wait ready: %w", err)
		}
		switch env.Op {
		case opDispatch:
			if env.Type == "" {
				continue
			}
			g.drainEarlyDispatch(ctx, env)
		case opInvalidSess:
			g.tokens.Invalidate()
			return false, errors.New("invalid session on identify (op=9): token discarded")
		case opHeartbeatACK:
			continue
		case opHello:
			continue
		default:
			return false, fmt.Errorf("unexpected op=%d before READY", env.Op)
		}
		if env.Type == "READY" {
			g.logf("gateway READY")
			break
		}
	}

	// ── 4. 心跳 + 读循环 ──
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	ackCh := make(chan struct{}, 1)
	hbErrCh := make(chan error, 1)
	hbDone := make(chan struct{})
	go g.heartbeatLoop(hbCtx, conn, interval, ackCh, hbDone, func(err error) {
		select {
		case hbErrCh <- err:
		default:
		}
		_ = conn.Close() // 把读循环叫醒，由读循环统一返回错误
	})

	readTimeout := 2 * interval
	if readTimeout < 30*time.Second {
		readTimeout = 30 * time.Second
	}

	for {
		if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			hbCancel()
			<-hbDone
			return true, fmt.Errorf("set read deadline: %w", err)
		}
		env, err := readEnvelope(conn)
		if err != nil {
			hbCancel()
			<-hbDone
			select {
			case hbErr := <-hbErrCh:
				return true, hbErr
			default:
			}
			return true, err
		}

		switch env.Op {
		case opDispatch:
			if s := env.Sequence; s > 0 {
				g.seq.Store(s)
			}
			if env.Type == "" || env.Type == "READY" {
				continue
			}
			if g.onEvent != nil {
				g.onEvent(ctx, GatewayEvent{Type: env.Type, Data: env.D, ID: env.ID, Sequence: env.Sequence})
			}
		case opHeartbeatACK:
			select {
			case ackCh <- struct{}{}:
			default:
			}
		case opHeartbeat:
			// 服务端要求立刻心跳（少见），直接发一次。
			_ = writeJSONFrame(conn, map[string]any{"op": opHeartbeat, "d": g.seqOrNil()})
		case opReconnect:
			hbCancel()
			<-hbDone
			return true, errors.New("server requested reconnect (op=7)")
		case opInvalidSess:
			hbCancel()
			<-hbDone
			g.tokens.Invalidate()
			return true, errors.New("invalid session (op=9): token discarded, will reconnect")
		case opHello:
			continue
		default:
			g.logf("gateway: ignoring op=%d", env.Op)
		}
	}
}

// drainEarlyDispatch 处理 READY 之前到达的业务事件（官方保证 READY 先到，
// 但协议没禁止其它 dispatch 抢跑；这里不丢事件）。
func (g *Gateway) drainEarlyDispatch(ctx context.Context, env gwEnvelope) {
	if env.Type == "" || env.Type == "READY" {
		return
	}
	if g.onEvent != nil {
		g.onEvent(ctx, GatewayEvent{Type: env.Type, Data: env.D, ID: env.ID, Sequence: env.Sequence})
	}
}

// heartbeatLoop 按 interval 发 op=1，收不到 ACK 就判失联。
//
// 判定方式：发过一次心跳后，下次 tick 到来前没收到 op=11 → 失联。
// 这样容忍"ACK 晚一个周期"，但连续两个周期没有 ACK 一定重连，不会假活。
func (g *Gateway) heartbeatLoop(ctx context.Context, conn WSConn, interval time.Duration, ackCh <-chan struct{}, done chan<- struct{}, fail func(error)) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	pending := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ackCh:
			pending = false
		case <-ticker.C:
			if pending {
				fail(fmt.Errorf("heartbeat not acked within %s — reconnecting", interval))
				return
			}
			if err := writeJSONFrame(conn, map[string]any{"op": opHeartbeat, "d": g.seqOrNil()}); err != nil {
				fail(fmt.Errorf("send heartbeat: %w", err))
				return
			}
			pending = true
		}
	}
}

// seqOrNil 返回心跳 d（没有 seq 时按协议发 null）。
func (g *Gateway) seqOrNil() any {
	if s := g.seq.Load(); s > 0 {
		return s
	}
	return nil
}

// gwEnvelope 是网关下行帧的公共信封。
type gwEnvelope struct {
	Op       int             `json:"op"`
	D        json.RawMessage `json:"d,omitempty"`
	Sequence int64           `json:"s,omitempty"`
	Type     string          `json:"t,omitempty"`
	ID       string          `json:"id,omitempty"`
}

func readEnvelope(conn WSConn) (gwEnvelope, error) {
	raw, err := conn.ReadMessage()
	if err != nil {
		return gwEnvelope{}, err
	}
	var env gwEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return gwEnvelope{}, fmt.Errorf("decode gateway frame: %w (raw=%s)", err, truncate(string(raw), 300))
	}
	return env, nil
}

func writeJSONFrame(conn WSConn, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.WriteMessage(raw)
}

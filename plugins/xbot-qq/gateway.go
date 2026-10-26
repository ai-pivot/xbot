package main

// QQ 官方 WebSocket 网关生命周期。
//
// 文档依据（QQ 机器人官方文档）：
//
//	《事件订阅与通知》§通用数据结构 Payload / §OpCode 含义 / §WebSocket方式
//	https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/interface-framework/event-emit.html
//	《opcode》
//	https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/interface-framework/opcode.html
//	《获取通用 WSS 接入点》
//	https://bot.q.qq.com/wiki/develop/api-v2/openapi/wss/url_get.html
//
// 协议流程（出处：event-emit.html §WebSocket方式）：
//
//	1. GET /gateway 拿 wss 地址                  （url_get.html）
//	2. 连上去，收 op=10 Hello（d.heartbeat_interval）        （§发起连接到 Gateway）
//	3. 发 op=2 Identify（token "QQBot {token}" + intents + shard）
//	                                         （§登录鉴权获得 Session）
//	4. 收 op=0 t=READY（d.session_id）        （同上，Ready Event 示例）
//	5. 每 heartbeat_interval 发 op=1 心跳（d=最新 s，首次 null），收 op=11 ACK
//	                                         （§发送心跳 Ack）
//	6. 断线 → 指数退避重连；op=9 Invalid Session → 丢 token 重取再重连
//	                                         （opcode.html：9 = identify/resume 参数错）
//
// ⚠️ 已知差异（文档有、未实现）：event-emit.html §恢复登录态 Session 要求重连后
// 发 op=6 Resume（携带 session_id + seq）以补发断线期间遗漏的事件；本实现选择
// 重连后重新 Identify（会漏掉断线窗口内的事件）。属未完成项，待联调排期。
//
// 本文件只做生命周期，不解析业务事件（见 events.go 的 ParseInbound）。
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
//
// 出处：opcode.html 全表；event-emit.html §OpCode 含义。
const (
	opDispatch     = 0  // Dispatch        服务端消息推送
	opHeartbeat    = 1  // Heartbeat       客户端/服务端心跳
	opIdentify     = 2  // Identify        客户端发送鉴权
	opResume       = 6  // Resume          恢复登录态（断线重连用，见 §恢复登录态 Session）
	opReconnect    = 7  // Reconnect       服务端通知客户端重新连接
	opInvalidSess  = 9  // Invalid Session identify/resume 参数有错
	opHello        = 10 // Hello           连接建立后网关下发第一条
	opHeartbeatACK = 11 // Heartbeat ACK   心跳成功后的回执
)

// IntentGroupAndC2C 覆盖 C2C_MESSAGE_CREATE 与 GROUP_AT_MESSAGE_CREATE，
// 同时也覆盖 GROUP_MESSAGE_CREATE（群全量）。
//
// 出处：event-emit.html §事件订阅Intents
//
//	GROUP_AND_C2C_EVENT (1 << 25)
//	  - C2C_MESSAGE_CREATE      // 用户单聊发消息给机器人
//	  - GROUP_AT_MESSAGE_CREATE // 用户在群里@机器人时收到的消息
//	  （GROUP_MESSAGE_CREATE 同页列在群消息事件文档，intent 同为 1<<25）
//
// 注意：event-emit.html §权限 说明「除了 GUILDS、PUBLIC_GUILD_MESSAGES、
// GUILD_MEMBERS 是基础事件……其他特殊事件都需要申请才能使用；如果传递了
// 无权限的 intents，websocket 会报错并直接关闭连接」。GROUP_AND_C2C_EVENT
// 需要在开放平台申请（本渠道默认就用它，属既定前提）。
const IntentGroupAndC2C = 1 << 25

// FULL_INTENTS 对齐官方新 SDK（@tencent-connect/qqbot-nodejs v1.0.5
// src/protocol/gateway/constants.ts）：
//
//	FULL_INTENTS = GUILDS | GUILD_MEMBERS | PUBLIC_GUILD_MESSAGES |
//	               DIRECT_MESSAGE | GROUP_AND_C2C | INTERACTION
//
// 2026-10-26 排障：该 SDK 默认就用 FULL_INTENTS，且它**能**收到群 @ 消息；
// 而我们只订 GROUP_AND_C2C(1<<25) 时，沙箱/正式两种域名下 GROUP_AT_MESSAGE_CREATE
// 都一条没到（C2C 正常）。因此按官方 SDK 的取值逐位对齐后再验证一次。
// 若某些位无权限，握手会被 4914/4915 直接关闭 —— 那说明对齐过度，退回最小集。
const (
	IntentGuilds              = 1 << 0
	IntentGuildMembers        = 1 << 1
	IntentPublicGuildMessages = 1 << 30
	IntentDirectMessage       = 1 << 12
	IntentInteraction         = 1 << 26
)

// DefaultIntents 是最小可用集。
//
// 2026-10-26 实测：用官方 SDK 的 FULL_INTENTS
// （GUILDS|GUILD_MEMBERS|PUBLIC_GUILD_MESSAGES|DIRECT_MESSAGE|GROUP_AND_C2C|INTERACTION）
// 时，连 C2C_MESSAGE_CREATE 都收不到了（`gateway event received` 一次都没触发）。
// 官方文档 §权限 明确："如果拥有的某个特殊事件类型的权限被取消，则在当前连接上
// 不会报错，但是将不会收到对应的事件类型" —— 即多订了没权限的位会**静默**
// 影响事件投递。因此回到已验证可用的最小集：只订 GROUP_AND_C2C(1<<25)，
// 它能收到 C2C_MESSAGE_CREATE。
// 默认同时订阅：
//
//	GROUP_AND_C2C      (1<<25) → 私聊 C2C_MESSAGE_CREATE + QQ群 GROUP_AT_MESSAGE_CREATE
//	PUBLIC_GUILD_MESSAGES (1<<30) → QQ**频道**里 AT_MESSAGE_CREATE
//
// 2026-10-26：此前只订 1<<25，能收私聊、收不到「群」消息。QQ 的「群」有两类——
// 群聊(group)和频道(guild)，后者走 AT_MESSAGE_CREATE + 1<<30，是 1<<25 覆盖不到的。
// 两个位都是消息类基础位，同时订阅即可兼容两种场景。
const DefaultIntents = IntentGroupAndC2C | IntentPublicGuildMessages

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
	// WSURLOverride 非空时**跳过 GET /gateway**，直接连这个 wss 地址。
	//
	// 2026-10-26 实测背景：正式域名下 GET /gateway 会 401（11298 IP 白名单），
	// 但**直连 wss 是通的**（能收到 op=10 Hello）：
	//   wss://api.sgroup.qq.com/websocket/   → Hello
	//   wss://api.bot.qq.com/websocket/      → Hello
	// 因此提供一个"固定地址"旁路，绕过 /gateway 那一步。
	WSURLOverride string
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
	// seq 是最后收到的 s（心跳与 op6 Resume 的 seq 都用它）。
	seq atomic.Int64
	// sessionID 是 READY 事件给出的 session_id；非空时重连走 op=6 Resume
	// （官方：「断开重连 gateway 后不需要发送重新登录 OpCode 2 Identify 请求，
	// 需要发送 OpCode 6 Resume」）。op=9 时清空，退回 Identify。
	sessionMu sync.Mutex
	sessionID string

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
		// 先判"不可重试"，再决定退避：IP 白名单 / 限流 / 鉴权失败重试一万次
		// 也不会变绿，只会烧配额并触发风控（2026-10-26 联调事故）。
		if fatal, hint := FatalGatewayReason(err); fatal {
			g.logf("gateway stopped: 不可重试错误%s: %v", hint, err)
			return fmt.Errorf("qq gateway: fatal: %w%s", err, hint)
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
	var wsURL string
	if ov := g.cfg.WSURLOverride; ov != "" {
		wsURL = ov
		g.logf("gateway ws url (override, skip /gateway): %s", wsURL)
	} else {
		u, err := g.gatewayURL(ctx)
		if err != nil {
			return false, fmt.Errorf("get gateway url: %w", err)
		}
		wsURL = u
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
	// 出处：event-emit.html §发起连接到 Gateway
	// ——「一旦连接成功，就会返回 OpCode 10 Hello 消息。这个消息主要的内容是
	//    心跳周期，单位毫秒(milliseconds)」。
	if err := json.Unmarshal(env.D, &hello); err != nil {
		return false, fmt.Errorf("parse hello: %w", err)
	}
	interval := time.Duration(hello.HeartbeatInterval) * time.Millisecond
	if interval <= 0 {
		return false, fmt.Errorf("hello carried invalid heartbeat_interval %d", hello.HeartbeatInterval)
	}
	g.logf("gateway hello: heartbeat_interval=%s", interval)
	// 把实际订阅的 intents 打进日志：排障时"订了什么"必须是可观测事实，
	// 而不是靠读代码推断（此前多次因为看不到 intents 而误判）。
	if iv := g.intents(); true {
		g.logf("gateway identify: intents=%d (group_c2c=%v guild_messages=%v resume=%v)",
			iv, iv&IntentGroupAndC2C != 0, iv&IntentPublicGuildMessages != 0, g.currentSessionID() != "")
	}

	// ── 2. Resume 或 Identify ──
	//
	// 官方（event-emit.html §恢复登录态 Session）：
	// 「断开重连 gateway 后不需要发送重新登录 Opcode 2 Identify 请求。在连接到
	//  Gateway 之后，需要发送 Opcode 6 Resume 消息」，字段 token / session_id / seq，
	// seq 为最后收到的 s。成功后服务端补发断线期间遗漏的事件，补完再下发 RESUMED。
	//
	// 只有"没有 session"（首次连接、或收到 op=9 被清空）时才走 Identify。
	if sid := g.currentSessionID(); sid != "" {
		resume := map[string]any{
			"op": opResume,
			"d": map[string]any{
				"token":      "QQBot " + token,
				"session_id": sid,
				"seq":        g.seq.Load(),
			},
		}
		if err := writeJSONFrame(conn, resume); err != nil {
			return false, fmt.Errorf("send resume: %w", err)
		}
		g.logf("gateway resume sent: session_id=%s seq=%d", sid, g.seq.Load())
	} else {
		// ── Identify ──
		//
		// 出处：event-emit.html §登录鉴权获得 Session
		//   token     格式为 "QQBot {AccessToken}"
		//   intents   本次连接要接收的事件位
		//   shard     两元素数组 [shard_id, num_shards]
		//   properties 目前无实际作用（同页原文），按示例填 $os/$browser/$device
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
	}

	// ── 3. 等 READY（Identify）或 RESUMED（Resume）──
	// 两者之前也可能先来 op=9（凭据/会话失效），必须处理。
	// 出处：event-emit.html §登录鉴权获得 Session（Ready Event 示例：
	// op=0, t="READY", d.session_id）；opcode.html（9 = Invalid Session）。
	if err := conn.SetReadDeadline(time.Now().Add(helloTimeout)); err != nil {
		return false, fmt.Errorf("set read deadline: %w", err)
	}
waitLoop:
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
			// op=9：identify/resume 参数有错 → 会话作废，重取 token 后重新 Identify。
			g.clearSession()
			g.tokens.Invalidate()
			return false, errors.New("invalid session (op=9) before READY/RESUMED: session cleared, token discarded")
		case opHeartbeatACK:
			continue
		case opHello:
			continue
		default:
			return false, fmt.Errorf("unexpected op=%d before READY", env.Op)
		}
		switch env.Type {
		case "READY":
			g.captureSessionID(env.D)
			g.logf("gateway READY (session_id=%s)", g.currentSessionID())
			break waitLoop
		case "RESUMED":
			// Resume 成功：session_id 保持不变（服务端已确认续接）。
			g.logf("gateway RESUMED (session_id=%s seq=%d)", g.currentSessionID(), g.seq.Load())
			break waitLoop
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
			// 会话作废：清掉 session_id，下次连接只能 Identify（不能 Resume）。
			g.clearSession()
			g.tokens.Invalidate()
			return true, errors.New("invalid session (op=9): session cleared, token discarded, will re-identify")
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
	if s := env.Sequence; s > 0 {
		g.seq.Store(s)
	}
	if env.Type == "" || env.Type == "READY" || env.Type == "RESUMED" {
		return
	}
	if g.onEvent != nil {
		g.onEvent(ctx, GatewayEvent{Type: env.Type, Data: env.D, ID: env.ID, Sequence: env.Sequence})
	}
}

// heartbeatLoop 按 interval 发 op=1，收不到 ACK 就判失联。
//
// 出处：event-emit.html §发送心跳 Ack
//   - 「d 为客户端收到的最新的消息的 s，如果是首次连接，d 为传 null」
//   - 「心跳发送成功之后会收到 OpCode 11 Heartbeat ACK」
//
// 判定方式：发过一次心跳后，下次 tick 到来前没收到 op=11 → 失联。
// 这样容忍"ACK 晚一个周期"，但连续两个周期没有 ACK 一定重连，不会假活。
// （"漏 ACK 就重连"是实现策略：文档只说明 ACK 的存在，没写超时处理。）
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

// currentSessionID 返回当前 session_id（无会话时为空）。
func (g *Gateway) currentSessionID() string {
	g.sessionMu.Lock()
	defer g.sessionMu.Unlock()
	return g.sessionID
}

// clearSession 作废当前会话（op=9），下次连接只能 Identify。
func (g *Gateway) clearSession() {
	g.sessionMu.Lock()
	defer g.sessionMu.Unlock()
	g.sessionID = ""
}

// captureSessionID 从 READY 的 d.session_id 记录会话 id（Resume 的凭据）。
//
// 出处：event-emit.html §登录鉴权获得 Session — Ready Event 示例 d.session_id。
func (g *Gateway) captureSessionID(data json.RawMessage) {
	var d struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		g.logf("gateway: cannot parse READY session_id: %v", err)
		return
	}
	if d.SessionID == "" {
		g.logf("gateway: READY carried no session_id — reconnect will fall back to identify")
		return
	}
	g.sessionMu.Lock()
	g.sessionID = d.SessionID
	g.sessionMu.Unlock()
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

package main

// 渠道运行时：把配置变成"一条到 QQ 网关的长连接 + 一个发送器"。
//
// 生命周期由 app 管理（配置热更新 / deactivate 时 stop 再 start）。
// 单测通过 newRuntime 注入假 TokenSource / 假 Dialer / 假 MessageAPI，
// 因此这里不直接依赖网络。

import (
	"context"
	"sync"
	"time"
)

// runtimeDeps 是运行时的可替换依赖。
type runtimeDeps struct {
	tokens     TokenSource
	gatewayURL func(ctx context.Context) (string, error)
	dialer     Dialer
	api        MessageAPI
	// sleep 注入断线退避等待（单测记录时长用）。
	sleep func(ctx context.Context, d time.Duration) bool
	// reconnect 覆盖默认退避策略（单测用小间隔）。
	reconnect ReconnectPolicy
}

// channelRuntime 是"一次配置生效期"内的连接与会话。
type channelRuntime struct {
	cfg    Config
	gw     *Gateway
	sender *OutboundSender
	logf   func(string, ...any)

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// newDefaultRuntime 用真实网络依赖构造运行时。
func newDefaultRuntime(cfg Config, logf func(string, ...any)) *channelRuntime {
	tokens := NewTokenClient(cfg.AppID, cfg.AppSecret)
	// 环境由配置决定：应用未发布时必须走沙箱域名，否则 /gateway 一律 401。
	api := NewAPIClientEnv(tokens, cfg.Sandbox)
	return newRuntime(cfg, runtimeDeps{
		tokens:     tokens,
		gatewayURL: api.GatewayURL,
		dialer:     stdlibDialer{},
		api:        api,
	}, logf)
}

// newRuntime 用注入依赖构造运行时（单测入口）。
func newRuntime(cfg Config, deps runtimeDeps, logf func(string, ...any)) *channelRuntime {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	gw := NewGateway(deps.tokens, deps.gatewayURL, deps.dialer, nil, logf)
	// Intents 来自 channels.qq.intents（十进制/0x），0 表示用 DefaultIntents。
	// 这样切换「群聊(1<<25) / 频道(1<<30)」只需要改配置 + reload，不必重新编译。
	gw.cfg = GatewayConfig{Reconnect: deps.reconnect, Sleep: deps.sleep, Intents: cfg.Intents, WSURLOverride: cfg.WSURL}
	sender := NewOutboundSender(deps.api)
	// 发送器自己也要能报 ERROR（配额耗尽必须留痕，见 sender.go）。
	sender.logErr = logf
	sender.Markdown = cfg.Markdown
	sender.Stream = cfg.Stream
	// 350ms/片 × 3 片 ≈ 1 秒：客户端能明显看到"逐渐长出来"，又不会拖太久。
	sender.streamDelay = 350 * time.Millisecond
	return &channelRuntime{
		cfg:    cfg,
		gw:     gw,
		sender: sender,
		logf:   logf,
	}
}

// Send 是下行发送入口（app 持有 channelRuntime 时直接调）。
func (r *channelRuntime) Send(ctx context.Context, chatID, content string) (*SentMessage, error) {
	return r.sender.Send(ctx, chatID, content)
}

// SendStreamDelta 转发一段增量文本到私聊流式接口（群聊自动忽略）。
func (r *channelRuntime) SendStreamDelta(ctx context.Context, chatID, delta string, final bool) (bool, error) {
	return r.sender.SendStreamDelta(ctx, chatID, delta, final)
}

// RememberInbound 记录被动回复锚点。
func (r *channelRuntime) RememberInbound(chatID, msgID string) {
	r.sender.RememberInbound(chatID, msgID)
}

// Start 启动网关协程，onEvent 会在网关协程里被调用。
func (r *channelRuntime) Start(parent context.Context, onEvent func(context.Context, GatewayEvent)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return // 已经在跑
	}
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	done := make(chan struct{})
	r.done = done
	r.gw.onEvent = onEvent
	go func() {
		defer close(done)
		if err := r.gw.Run(ctx); err != nil {
			r.logf("gateway stopped: %v", err)
		}
	}()
}

// Stop 取消并等待网关协程退出（幂等）。
func (r *channelRuntime) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	done := r.done
	r.cancel = nil
	r.done = nil
	r.mu.Unlock()

	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done
	}
}

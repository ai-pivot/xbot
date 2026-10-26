// Command qq-plugin implements the xbot QQ channel plugin — **QQ 官方开放平台**
// （bot.q.qq.com / q.qq.com）版本，不使用 OneBot / NapCat。
//
// 形态：xbot 的 stdio 渠道插件（plugin.json 里 runtime="stdio"）—— 由 host
// 用 entry 命令拉起一个独立进程，双方用 JSON lines over stdin/stdout 通信
// （协议与 WebSocket 同一套，见 plugin/protocol/protocol.go）。
//
// 协议要点（host → plugin）：
//
//	{"method":"activate","params":{"pluginId":"xbot.qq"}}
//	{"type":"channel_config","metadata":{"config":"{\"enabled\":\"true\",...}"}}
//	{"type":"text","content":"...","chat_id":"..."}          // 下行回复（真实路径）
//	{"id":"srv-1","method":"channel_send","params":{...}}    // 兼容旧路径
//	{"method":"deactivate"}
//
// 协议要点（plugin → host）：
//
//	{"result":"ok","channel_provider":{"name":"qq","config_schema":[...]}}   // 应答 activate
//	{"type":"channel_prompt","system_parts":{"05_channel_qq":"..."}}          // 渠道提示词
//	{"id":"plugin-1","method":"send_inbound","params":{...}}                  // 入站消息（openid 身份）
//	{"id":"srv-1","result":"..."} | {"id":"srv-1","error":"..."}              // 应答 RPC
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	a := newApp(os.Stderr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.run(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "qq-plugin: fatal: %v\n", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Wire types（只镜像本插件用到的子集，保持零依赖）
// ---------------------------------------------------------------------------

// frame 是 host → plugin 的一帧。RPC 请求有 ID+Method，事件推送有 Type。
type frame struct {
	ID       string            `json:"id,omitempty"`
	Method   string            `json:"method,omitempty"`
	Type     string            `json:"type,omitempty"`
	Params   json.RawMessage   `json:"params,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Content  string            `json:"content,omitempty"`
	ChatID   string            `json:"chat_id,omitempty"`
}

// rpcRequest 是 plugin → host 的 RPC 请求帧（send_inbound 等）。
type rpcRequest struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

// activateResponse 应答 activate：声明本插件提供名为 qq 的渠道。
type activateResponse struct {
	Result          string             `json:"result"`
	ChannelProvider ChannelDeclaration `json:"channel_provider"`
}

// errorResponse 应答一个失败/未实现的 RPC。必须带 ID，否则 host 侧调用方
// 会一直等到超时 —— 未知 method 也要回，不能静默丢弃。
type errorResponse struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// okResponse 应答一个成功但无返回值的 RPC。
type okResponse struct {
	ID     string `json:"id"`
	Result string `json:"result"`
}

// ---------------------------------------------------------------------------
// app — 协议循环与状态
// ---------------------------------------------------------------------------

type app struct {
	mu  sync.RWMutex
	cfg Config
	rt  *channelRuntime

	// rpcSeq 生成 plugin → host 的 RPC id（与 examples/echo-channel 的
	// "plugin-N" 形式一致，便于 host 日志里一眼区分方向）。
	rpcSeq atomic.Uint64

	// outMu 序列化 stdout 写入：协议读循环与网关事件协程都会写帧，
	// json.Encoder 本身不是并发安全的。
	outMu sync.Mutex

	// logw 是诊断输出（stderr）。host 会把它重定向到每插件日志文件，
	// 所以这里可以自由打；绝不能写 stdout（stdout 只走协议帧）。
	logw io.Writer

	// newRuntime 可在单测里替换成注入依赖的运行时构造函数。
	newRuntime func(cfg Config, logf func(string, ...any)) *channelRuntime
}

func newApp(logw io.Writer) *app {
	return &app{logw: logw, newRuntime: newDefaultRuntime}
}

func (a *app) logf(format string, args ...any) {
	if a.logw == nil {
		return
	}
	fmt.Fprintf(a.logw, "qq-plugin: "+format+"\n", args...)
}

// write 序列化写一帧到 stdout。
func (a *app) write(enc *json.Encoder, v any) {
	a.outMu.Lock()
	defer a.outMu.Unlock()
	_ = enc.Encode(v)
}

func (a *app) config() Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}

func (a *app) setConfig(c Config) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg = c
}

func (a *app) runtime() *channelRuntime {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.rt
}

// stopRuntime 停止当前连接（幂等）。
func (a *app) stopRuntime() {
	a.mu.Lock()
	rt := a.rt
	a.rt = nil
	a.mu.Unlock()
	if rt != nil {
		rt.Stop()
	}
}

// swapRuntime 停掉旧运行时并换上新的（nil = 只停不启）。
func (a *app) swapRuntime(rt *channelRuntime) {
	a.stopRuntime()
	a.mu.Lock()
	a.rt = rt
	a.mu.Unlock()
}

// nextRPCID 返回下一个 plugin → host 的 RPC id。
func (a *app) nextRPCID() string {
	return fmt.Sprintf("plugin-%d", a.rpcSeq.Add(1))
}

// run 是协议主循环。拆出来是为了单测能喂内存流（与 plugin/protocol 的
// run(h, stdin, stdout) 同构）。
func (a *app) run(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	defer a.stopRuntime()

	enc := json.NewEncoder(stdout)
	// bufio.Reader, NOT bufio.Scanner: Scanner 的行长上限会让一条大消息
	// 直接掐断整个协议循环；Reader 没有该限制。
	br := bufio.NewReader(stdin)

	for {
		raw, rerr := br.ReadBytes('\n')
		line := strings.TrimSpace(string(raw))
		if line != "" {
			if stop := a.handleLine(ctx, enc, line); stop {
				a.logf("deactivate received, exiting")
				return nil
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil // host 关掉了管道
			}
			return fmt.Errorf("read stdin: %w", rerr)
		}
	}
}

// handleLine 分发一帧。返回 true 表示应当退出进程（deactivate）。
func (a *app) handleLine(ctx context.Context, enc *json.Encoder, line string) (stop bool) {
	var f frame
	if err := json.Unmarshal([]byte(line), &f); err != nil {
		// 坏帧不回 ID（不知道是谁），只记录。协议循环必须活下去。
		a.logf("drop malformed frame: %v", err)
		return false
	}

	switch {
	case f.Method == "activate":
		a.handleActivate(enc)
	case f.Method == "deactivate":
		a.write(enc, okResponse{ID: f.ID, Result: "ok"})
		return true
	case f.Method == "channel_send":
		a.handleChannelSend(enc, f)
	case f.Type == "channel_config":
		a.handleChannelConfig(ctx, enc, f)
	case f.Type == "text":
		a.handleTextEvent(enc, f)
	case f.Method != "":
		// 未知 RPC：必须应答，否则 host 侧等到超时。
		a.write(enc, errorResponse{ID: f.ID, Error: "unsupported method: " + f.Method})
	case f.Type != "":
		// 未识别的异步事件（stream_content / progress_structured / session …）
		// 流式接口留待后续（官方 msg_seq 分片），当前安全忽略。
	default:
		a.logf("drop frame with neither method nor type")
	}
	return false
}

// handleActivate 应答渠道声明。host 在插件激活时只问一次。
func (a *app) handleActivate(enc *json.Encoder) {
	decl := qqChannelDeclaration()
	a.logf("activate: declaring channel %q with %d config keys", decl.Name, len(decl.ConfigSchema))
	a.write(enc, activateResponse{Result: "ok", ChannelProvider: decl})
}

// handleChannelConfig 处理 host 推来的渠道配置（首帧，热更新时会再推）。
//
// 两段式：宽松解析（永不失败）→ 严格校验（失败只记日志，留着上一次可用配置
// 继续跑）。插件进程绝不因为配置写错而退出 —— 那会让 host 陷入重启循环。
//
// 提示词声明放在最前面：它只依赖编译进二进制的文件，与这份配置是否合法无关。
// 渠道进程被创建出来（= 渠道已启用）就该把渠道约束挂上，别等配置校验。
func (a *app) handleChannelConfig(ctx context.Context, enc *json.Encoder, f frame) {
	a.declareChannelPrompt(enc)

	var raw map[string]string
	if s := strings.TrimSpace(f.Metadata["config"]); s != "" {
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			a.logf("channel_config: unparsable config JSON, keeping previous config: %v", err)
			return
		}
	}

	cfg := ParseConfig(raw)
	a.setConfig(cfg)

	if err := cfg.Validate(); err != nil {
		a.logf("channel_config: %v", err)
		a.logf("channel_config: channel stays disconnected until config is fixed")
		a.swapRuntime(nil)
		return
	}
	a.logf("channel_config: %s", cfg.Summary())
	if !cfg.Enabled {
		a.logf("channel_config: disabled — no QQ gateway connection will be made")
		a.swapRuntime(nil)
		return
	}
	if cfg.Allow.Empty() {
		a.logf("channel_config: %s is empty — every inbound message will be rejected "+
			"(set it to \"*\" to process everything)", KeyAllow)
	}

	rt := a.newRuntime(cfg, a.logf)
	// 先换再启：避免新旧两条网关连接短暂并存（官方允许重复连接，但同一
	// 机器人开两条长连接会让事件投递变成随机一半，排查起来很痛）。
	a.swapRuntime(rt)
	rt.Start(ctx, func(eventCtx context.Context, ev GatewayEvent) {
		a.handleGatewayEvent(enc, eventCtx, ev)
	})
	a.logf("qq channel started: app_id=%s allow=%s", cfg.AppID, cfg.Allow)
}

// declareChannelPrompt 把渠道专属提示词声明给 host。
//
// host 侧链路：agent.ChannelPluginTransport.handleChannelPrompt →
// channelPluginPromptProvider.setSystemParts → Agent.AddChannelPromptProvider →
// ChannelPromptMiddleware 在 mc.Channel == "qq" 时把这些 system_parts 注入
// system prompt（key 05_channel_qq，排在 00_base 之后、10_skills 之前）。
func (a *app) declareChannelPrompt(enc *json.Encoder) {
	a.write(enc, newChannelPromptFrame())
	a.logf("channel_prompt declared: key=%s bytes=%d sha256=%s",
		ChannelPromptKey, len(channelPromptMarkdown), ChannelPromptSHA256())
}

// handleGatewayEvent 处理一条网关业务事件：解析 → 白名单 → 身份注入。
//
// 关键纪律：**被拒绝的消息不产生任何外部 API 调用**。这里除了写一条
// send_inbound RPC（进程内管道）之外不碰网络；也不要"礼貌地回一句拒绝"，
// 那会变成一次真实的 OpenAPI 调用，把非白名单用户变成可被打量的对象。
func (a *app) handleGatewayEvent(enc *json.Encoder, _ context.Context, ev GatewayEvent) {
	in, ok, err := ParseInbound(ev.Type, ev.Data)
	if err != nil {
		a.logf("inbound parse failed (%s id=%s): %v", ev.Type, ev.ID, err)
		return
	}
	if !ok {
		return
	}

	cfg := a.config()
	decision := cfg.Policy().Decide(in.SenderOpenID)
	if !decision.Handle {
		a.logf("inbound dropped: %s (%s) not admitted by allow=%s (admin=%t)",
			in.SenderOpenID, in.ChatID(), cfg.Allow, decision.Admin)
		return
	}
	if !in.Valid() {
		a.logf("inbound dropped: message %s from %s incomplete (empty content/id)",
			in.MessageID, in.SenderOpenID)
		return
	}

	// 记录锚点，让随后的回复能作为"被动消息"带上 msg_id（5 分钟窗口）。
	if rt := a.runtime(); rt != nil {
		rt.RememberInbound(in.ChatID(), in.MessageID)
	}

	cand := NewInboundCandidate(in.Identity(), in.Content, in.MessageID, cfg.Policy())
	a.logf("inbound accepted: %s admin=%t msg=%s", in.Identity().Describe(), cand.Admin, in.MessageID)
	a.write(enc, rpcRequest{
		ID:     a.nextRPCID(),
		Method: "send_inbound",
		Params: cand.Params(),
	})
}

// handleChannelSend 应答 host 的 channel_send RPC（旧路径，保留兼容）。
func (a *app) handleChannelSend(enc *json.Encoder, f frame) {
	var p struct {
		ChatID  string `json:"chat_id"`
		Content string `json:"content"`
	}
	if len(f.Params) > 0 {
		_ = json.Unmarshal(f.Params, &p)
	}
	a.sendOutbound(enc, f.ID, p.ChatID, p.Content)
}

// handleTextEvent 处理 host 推来的 text 事件（真实下行路径，见
// agent.ChannelPluginTransport.Send → PushEvent(protocol.MsgTypeText)）。
func (a *app) handleTextEvent(enc *json.Encoder, f frame) {
	a.sendOutbound(enc, f.ID, f.ChatID, f.Content)
}

// sendOutbound 是两条下行路径的公共实现。
//
// 没有可用运行时（未配置 / 配置非法 / 已停用）时必须**诚实报错**：
// 回 ok 假装发出去会让上层永远查不出消息丢了。
func (a *app) sendOutbound(enc *json.Encoder, frameID, chatID, content string) {
	rt := a.runtime()
	if rt == nil {
		a.logf("outbound to %q dropped: qq channel is not connected "+
			"(set channels.qq.app_id/app_secret and enabled=true)", chatID)
		if frameID != "" {
			a.write(enc, errorResponse{ID: frameID, Error: "qq channel is not connected (missing credentials or disabled)"})
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	msg, err := rt.Send(ctx, chatID, content)
	if err != nil {
		a.logf("outbound to %q failed: %v", chatID, err)
		if frameID != "" {
			a.write(enc, errorResponse{ID: frameID, Error: err.Error()})
		}
		return
	}
	id := ""
	if msg != nil {
		id = msg.ID
	}
	a.logf("outbound to %q sent (id=%s, %d chars)", chatID, id, len([]rune(content)))
	if frameID != "" {
		a.write(enc, okResponse{ID: frameID, Result: "ok"})
	}
}

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
	// 流式增量帧字段（protocol/events.go 的 stream_delta / stream_content）：
	//   StreamDelta   = 自上次以来的增量文本
	//   StreamContent = 累计检查点（迭代结束时给出，不能直接当增量发）
	StreamDelta   string `json:"stream_delta,omitempty"`
	StreamContent string `json:"stream_content,omitempty"`
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
	// streamSent 记录本轮是否已经发过流式增量（用于最终帧去重）。
	streamSent bool
	// lastChatID 是最近一条入站消息的会话（流式帧不带 chat_id）。
	lastChatID string
	mu         sync.RWMutex
	cfg        Config
	rt         *channelRuntime

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

	// attachments 下载并落盘多模态附件（约束 3）。nil 时按 $XBOT_HOME 构造。
	attachments *AttachmentStore

	// dedup 挡住网关的重复推送（同一 msg_idx / msg_id 只能进一次 host），
	// 见 dedup.go 与约束 5。
	dedup *inboundDeduper

	// seen 是"见过的发送者"台账：每条入站（含被拒的）都记，用户靠它 + !whoami
	// 拿到自己的 openid（见 seen_senders.go）。
	seen *SeenSenderStore
	// notice 给 !whoami / allow 未配置时的自动应答限流，保护被动回复配额。
	notice *noticeLimiter
	// dropLog 压掉 progress / stream 这类高频 debug 噪音（否则真错误会被埋）。
	dropLog *logThrottle
}

func newApp(logw io.Writer) *app {
	return &app{
		logw:        logw,
		newRuntime:  newDefaultRuntime,
		dedup:       newInboundDeduper(defaultDedupTTL, defaultDedupMax),
		attachments: NewAttachmentStore("", nil),
		seen:        NewSeenSenderStore(DefaultSeenSendersPath(), nil),
		notice:      NewNoticeLimiter(noticeCooldown, time.Now),
		dropLog:     NewLogThrottle(dropLogInterval, time.Now),
	}
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
	case f.Type == "progress_structured":
		// 约束 1：**绝对禁止**把进度/迭代快照/工具调用/思考过程转成 QQ 消息。
		// 官方配额（单聊 60 分钟 4 次、群聊 5 分钟 5 次）根本不容许刷进度；
		// 群聊更是明确不支持流式。内部进度只走 stderr。
		//
		// 但 stderr 也不能刷：这条 debug 曾单次联调刷出 2.3 万行 / 2.1MB，
		// 把 HTTP 401 这种决定性错误埋掉。限流到每分钟最多一行 + 压缩计数。
		if ok, dropped := a.dropLog.Allow("progress"); ok {
			a.logf("debug: drop progress frame ×%d 累计（进度一律不转发到 QQ；只发最终答案）",
				dropped+1)
		}
	case f.Type == "stream_content":
		// 私聊流式（用户要求「个人聊天支持 stream」）：
		//   官方接口 POST /v2/users/{openid}/stream_messages，
		//   input_state 1=生成中 / 10=结束，index 递增，stream_msg_id 首片后携带。
		//   host 每个增量帧带 stream_delta（增量）与 stream_content（检查点累计）。
		// 群聊：官方没有群流式接口，仍然丢弃增量、只发最终答案。
		a.handleStreamContent(f)
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
	a.logf("qq channel started: env=%s base=%s app_id=%s allow=%s",
		EnvName(cfg.Sandbox), APIBaseHost(cfg.Sandbox), cfg.AppID, cfg.Allow)
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

// handleGatewayEvent 处理一条网关业务事件：解析 → 命令门禁 → 白名单 → 身份注入。
//
// 两道闸门，顺序不能反：
//
//  1. 命令门禁（slash.go）：未知命令一律拒绝（连 admin 也拒，否则 host 会把它
//     当普通消息喂给模型）；非 admin 的 `/xxx`、`!xxx` 一律拒绝且**不产生
//     send_inbound**（= host 不会执行），回一条 QQ 拒绝文案；admin 但不在
//     allow 里也拒绝，理由要写"白名单"而不是"你不是管理员"。
//  2. 普通消息准入：不在 allow 里的普通消息仍然**零外部 API 调用**（只记日志）。
//
// 普通消息（非命令）被拒绝时不回任何消息；只有命令拒绝才回文案 —— 那是用户
// 显式发起的动作，静默会让人以为卡住了。
// debugDropReason 说明为什么群全量消息被丢弃（日志与测试共用，防止后人"修好"它）。
const debugDropReason = "只处理 @ 机器人消息；群全量消息一律丢弃"

func (a *app) handleGatewayEvent(enc *json.Encoder, ctx context.Context, ev GatewayEvent) {
	// 2026-10-26 排障：把平台推来的**每一个**事件类型都打出来（含我们不认识的），
	// 这样「平台到底推没推 GROUP_AT_MESSAGE_CREATE」可以被日志直接判定，
	// 不再靠猜。info 级，一行一个事件，不影响消息循环。
	a.logf("gateway event received: t=%s id=%s", ev.Type, ev.ID)

	// ── 约束 2：只处理 @ 消息 ──
	//
	// 官方 intents 只有 GROUP_AND_C2C_EVENT (1<<25) 一个位同时覆盖
	// GROUP_AT_MESSAGE_CREATE 与 GROUP_MESSAGE_CREATE（群全量），**订阅层无法拆分**
	// （出处：event-emit.html §事件订阅Intents，以及群消息事件页「机器人被添加到群且
	// 开启了接收所有消息时都会推送此事件」）。所以拆分只能发生在**分发层**：
	// 群全量消息在这里显式丢弃 —— 只记 debug 级日志，不进消息循环、不产生
	// inbound / LLM 调用 / 出站、不计 metrics。
	// 群全量（GROUP_MESSAGE_CREATE）不再在此丢弃：是否 @ 机器人由 ParseInbound
	// 依据 mentions 判定（2026-10-26 修正 —— 群里开了「接收所有消息」时平台只推
	// 全量事件，此前在这里一律丢弃，表现为「群里 @ 机器人毫无反应」）。

	in, ok, err := ParseInbound(ev.Type, ev.Data)
	if err != nil {
		a.logf("inbound parse failed (%s id=%s): %v", ev.Type, ev.ID, err)
		return
	}
	if !ok {
		return
	}

	// ── 约束 5：入站去重（官方：相同 msg_id 可能重复推送，需按 msg_idx 去重）──
	// 重复推送必须在任何后续动作之前丢弃：不进命令门禁、不产生 send_inbound、
	// 不触发任何出站调用。
	if key := dedupKey(in); a.dedup.Duplicate(key) {
		a.logf("debug: drop duplicate inbound key=%s (msg=%s idx=%d) — 重复推送，已丢弃",
			key, in.MessageID, in.MsgIdx)
		return
	}

	cfg := a.config()
	allow := cfg.Policy().ShouldHandle(in.SenderOpenID)
	admin := cfg.Policy().IsAdmin(in.SenderOpenID)
	content := in.Content
	name := senderNameOf(in)
	chatDesc := chatDescription(in)

	// 每条解析成功的入站先落一条"见过"记录（handled=false），保证即便后面的
	// 分支提前返回，用户的 openid 也已经在台账里 —— 这是他被拒后唯一的自救路径。
	a.recordSeen(in, name, false)

	// ── !whoami：必须在白名单判定之前 ──
	//
	// 被拒的人如果连自己的 openid 都问不到，就永远进不来（先有鸡还是先有蛋）。
	// 只查本地状态 + 一条固定文案，**绝不产生 send_inbound（零 LLM）**，
	// 且同一 openid 每 noticeCooldown 只回一次（保护被动回复配额）。
	if IsWhoamiCommand(in.Content) {
		a.recordSeen(in, name, true)
		a.logf("QQ inbound: sender=%s name=%s chat=%s handled=true (!whoami)",
			in.SenderOpenID, name, chatDesc)
		if a.notice.Allow("whoami:" + in.SenderOpenID) {
			a.replyNotice(in, whoamiText(in.SenderOpenID, name, allow, !cfg.Allow.Empty()))
		} else {
			a.logf("whoami suppressed: %s within %s cooldown (配额保护，不重复回)",
				in.SenderOpenID, noticeCooldown)
		}
		return
	}

	cmd, isCmd := ClassifyCommand(in.Content)
	if isCmd {
		switch {
		case !admin:
			// 第一优先：非 admin 一律拒，且**不泄漏命令列表**
			// （非管理员不该知道有哪些命令可用）。也必须在 Known 之前判 ——
			// 否则非 admin 发 "/rm" 会拿到"未知命令 + 可用命令清单"，既泄清单
			// 又把"权限不足"伪装成"命令写错"。
			a.recordSeen(in, name, true) // 已受理（回了拒绝文案）
			a.logf("QQ inbound: sender=%s name=%s chat=%s handled=true (cmd refused: not admin)",
				in.SenderOpenID, name, chatDesc)
			a.logf("slash rejected: %s (%s) not admin; cmd=%s category=%s dangerous=%t",
				in.SenderOpenID, in.ChatID(), cmd.Name, cmd.Category, cmd.Dangerous)
			a.rejectCommand(in, cmd, RefuseNotAdmin)
			return
		case !cmd.Known && cmd.Raw[0] == '/':
			// 2026-10-26：admin 的「/未知命令」按 bang 执行（等价于 "!xxx"）。
			// 用户明确要求「admin 可以使用 slash 命令，包括危险命令」；
			// host 的 bang 通道本来就是给 admin 直接跑 shell 的，这里只是让
			// "/ls" 与 "!ls" 等价，避免 admin 敲斜杠就被"未知命令"挡回去。
			bang := "!" + strings.TrimSpace(strings.TrimPrefix(cmd.Raw, "/"))
			a.recordSeen(in, name, true)
			a.logf("QQ inbound: sender=%s name=%s chat=%s handled=true (slash→bang: %q)",
				in.SenderOpenID, name, chatDesc, bang)
			// 不 return：把 content 换成 bang 形态后，继续走下面的正常转发流程
			// （附件下载 / 引用块 / send_inbound），与 "!xxx" 完全等价。
			content = bang
		case !cmd.Known:
			// 未知命令一律不转发：host 的 commands.Match 不匹配后会落进普通
			// 消息路径（= 一次模型调用），见 agent/agent.go:3275。
			// 只有 admin 走到这里，才告诉他"命令不存在 + 可用清单"。
			a.recordSeen(in, name, true)
			a.logf("QQ inbound: sender=%s name=%s chat=%s handled=true (cmd refused: unknown)",
				in.SenderOpenID, name, chatDesc)
			a.logf("slash rejected: %s (%s) unknown cmd=%s (not forwarded, would hit LLM)",
				in.SenderOpenID, in.ChatID(), cmd.Name)
			a.rejectCommand(in, cmd, RefuseUnknown)
			return
		case !allow:
			a.recordSeen(in, name, true)
			a.logf("QQ inbound: sender=%s name=%s chat=%s handled=true (cmd refused: not in allow)",
				in.SenderOpenID, name, chatDesc)
			a.logf("slash rejected: admin %s not in allow=%s; cmd=%s",
				in.SenderOpenID, cfg.Allow, cmd.Name)
			a.rejectCommand(in, cmd, RefuseNotAllowlisted)
			return
		default:
			// admin + allow：交给 host 命令注册表执行（含危险命令与压缩）。
			content = cmd.Forward
			a.logf("slash allowed: %s (%s) cmd=%s category=%s dangerous=%t forward=%q",
				in.SenderOpenID, in.ChatID(), cmd.Name, cmd.Category, cmd.Dangerous, content)
		}
	} else if !allow {
		// ── 白名单三态（用户令 2026-10-26，语义不许混）──
		//
		//  a) allow 为空（用户还没填）→ 自动回一条 whoami 指引，零 LLM。
		//     完全没反应会让第一次使用的人不知道下一步干嘛，产品等于死的。
		//  b) allow 已配置但不含本人（含填了不相干 openid）→ **静默拒绝**：
		//     零回复、零 LLM。联调步骤 6 的判据就是"完全无反应"。
		a.recordSeen(in, name, false)
		a.logf("QQ inbound: sender=%s name=%s chat=%s handled=false allow=%s",
			in.SenderOpenID, name, chatDesc, cfg.Allow)
		a.logf("inbound dropped: %s (%s) not admitted by allow=%s (admin=%t)",
			in.SenderOpenID, in.ChatID(), cfg.Allow, admin)
		if cfg.Allow.Empty() {
			if a.notice.Allow("allowempty:" + in.SenderOpenID) {
				a.replyNotice(in, allowEmptyText(in.SenderOpenID, name))
			} else {
				a.logf("allow-empty notice suppressed: %s within %s cooldown (配额保护)",
					in.SenderOpenID, noticeCooldown)
			}
		}
		return
	}

	if !in.Valid() {
		a.logf("inbound dropped: message %s from %s incomplete (empty content/id)",
			in.MessageID, in.SenderOpenID)
		return
	}

	// ── 正文护栏：正文绝不允许是平台令牌 ──
	//
	// 官方事件体的 content 是明文用户文本，id 才是 ROBOT1.0_ 形态（见
	// inbound_log.go 文件头）。如果这里发现正文本身就是令牌，说明报文被
	// 错误地映射（或平台字段变了），此时把它喂给 agent 只会得到乱回复 ——
	// 明确报错并丢弃，不做任何"猜测式解密"。
	if looksLikePlatformToken(in.Content) {
		a.logf("inbound dropped: body is a platform token, not user text (sender=%s chat=%s "+
			"event=%s msg=%s body=%s) — 拒绝把令牌当正文喂给 agent",
			in.SenderOpenID, chatLogTarget(in), in.EventType, in.MessageID, logPreview(in.Content))
		return
	}

	// 记录锚点，让随后的回复能作为"被动消息"带上 msg_id（按场景有效期）。
	if rt := a.runtime(); rt != nil {
		rt.RememberInbound(in.ChatID(), in.MessageID)
		// 流式/进度帧不带 chat_id，记录最近会话供 handleStreamContent 使用。
		a.mu.Lock()
		a.lastChatID = in.ChatID()
		a.mu.Unlock()
	}

	// ── 引用消息：把被引用的一层快照注入上下文（不递归）──
	if in.Reply != nil {
		if block := renderQuoteBlock(*in.Reply); block != "" {
			content = block + "\n" + content
		}
	}

	// ── 只有被准入的消息才下载附件 ──
	//
	// 非白名单消息的纪律是"零外部 API 调用"，下载附件也是外部调用，
	// 所以必须在 allow 判定之后。
	if len(in.Attachments) > 0 {
		actx, cancel := context.WithTimeout(ctx, 60*time.Second)
		block := a.attachments.Ingest(actx, in.Attachments)
		cancel()
		if block != "" {
			content = content + "\n\n" + block
		}
	}

	// ── 身份头：显式告诉模型「谁在说话、是不是管理员」──
	//
	// metadata 里的 is_admin / sender_id 是给宿主的结构化字段，模型未必看得到
	// （取决于宿主如何拼 system/tool 上下文）。这里把身份直接写进正文开头，
	// 保证模型一定能看到，且管理员身份与用户原文分离、不易被注入伪造。
	// 命令（!xxx / admin 的 /xxx）不加身份头：转发给 host 的必须是原样命令，
	// 加头会让 commands.Match 失配、也会污染 bang 的 shell 命令。
	rawContent := content
	if !isCmd {
		content = identityHeader(in, admin) + "\n" + content
	}

	cand := NewInboundCandidate(in.Identity(), content, in.MessageID, cfg.Policy())
	// 结构化一行：sender / chat / chat_type / event / body_len / body 各自成字段。
	// 群聊必须满足 sender != chat 的 openid（chat 带 group: 前缀），否则就是把
	// 回复发到个人私聊去了 —— 这条判据写在日志里，一眼可验（见 inbound_log.go）。
	a.logf("inbound accepted: sender=%s chat=%s chat_type=%s event=%s body_len=%d body=%s admin=%t msg=%s",
		in.SenderOpenID, chatLogTarget(in), chatTypeOf(in), in.EventType,
		bodyRuneLen(rawContent), logPreview(rawContent), cand.Admin, in.MessageID)
	a.write(enc, rpcRequest{
		ID:     a.nextRPCID(),
		Method: "send_inbound",
		Params: cand.Params(),
	})
}

// rejectCommand 给被门禁拒绝的命令回一条 QQ 文本（不产生 send_inbound）。
//
// 记录锚点后再发：这是对用户消息的被动回复，按场景有效期带 msg_id。
// 发送失败只记日志 —— 门禁已经生效（命令没执行），回复只是提示。
func (a *app) rejectCommand(in Inbound, cmd Command, reason string) {
	rt := a.runtime()
	if rt == nil {
		a.logf("slash refusal not sent (%s): channel not connected", reason)
		return
	}
	rt.RememberInbound(in.ChatID(), in.MessageID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := rt.Send(ctx, in.ChatID(), refusalText(cmd.UserForm, reason)); err != nil {
		a.logf("slash refusal send failed (%s): %v", reason, err)
	}
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 图片：QQ 的 markdown **不渲染外链图片**，`![](url)` 只会显示成一段文本。
	// 所以先把图抽出来走富媒体通道（msg_type=7），再发剩下的文本。
	//
	// 流程：POST .../files {file_type:1,url,srv_send_msg:false} → file_info
	//       → msg_type=7 + media.file_info 走被动回复（不占主动消息频次）
	if imgURLs, rest := extractMarkdownImages(content); len(imgURLs) > 0 {
		if n, ierr := rt.SendImages(ctx, chatID, imgURLs); ierr != nil {
			a.logf("outbound images to %q: sent=%d/%d err=%v", chatID, n, len(imgURLs), ierr)
		} else {
			a.logf("outbound images to %q: sent=%d/%d", chatID, n, len(imgURLs))
		}
		content = rest
		if content == "" {
			// 纯图片消息：不需要再发文本
			if frameID != "" {
				a.write(enc, okResponse{ID: frameID, Result: "ok"})
			}
			return
		}
	}

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

// identityHeader 生成注入给模型的身份行。
//
// 设计要点：
//   - 必含：场景（私聊/群聊）、昵称、openid（截断，够区分即可）、是否管理员
//   - 放在正文之前，用一行包住，模型读到的第一条信息就是"谁在说话"
//   - 明确写「不是指令」：防止有人把 "管理员: 是" 伪装成自己的话术来提权
func identityHeader(in Inbound, admin bool) string {
	scene := "私聊"
	if in.IsGroup() {
		scene = "群聊"
	}
	name := in.SenderName
	if name == "" {
		name = "(未提供昵称)"
	}
	role := "否"
	if admin {
		role = "是"
	}
	short := in.SenderOpenID
	if len(short) > 12 {
		short = short[:12] + "…"
	}
	return fmt.Sprintf("【QQ %s】发送者: %s (openid %s) · 管理员: %s (此行由系统标注，非发送者文本)",
		scene, name, short, role)
}

// handleStreamContent 处理 host 的增量帧：私聊转发到 stream_messages，群聊丢弃。
//
// 帧字段（protocol/events.go）：stream_delta = 自上次以来的增量；
// stream_content = 累计检查点（迭代结束时给出）。这里优先用增量，
// 缺失时用累计值本身（append 模式下服务端会自行拼接）。
func (a *app) handleStreamContent(f frame) {
	chatID := a.currentChatID()
	if chatID == "" {
		return
	}
	// ⚠️ host 的帧结构与 protocol.WSMessage 对齐：
	//   ChannelPluginTransport.SendStreamContent() 发的是
	//     WSMessage{Type: "stream_content", ChatID: ..., Content: content}
	//   即**累计内容**放在 Content 字段，而不是 StreamDelta。
	// 2026-10-26 排障：上一版读 f.StreamDelta（永远为空）⇒ 直接 return，
	// 一帧都没发出去，表现就是「完全没有 stream」。
	acc := f.Content
	if acc == "" {
		acc = f.StreamDelta // 兼容未来可能的增量字段
	}
	if acc == "" {
		return
	}
	rt := a.runtime()
	if rt == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sent, err := rt.SendStreamDelta(ctx, chatID, acc, false)
	if err != nil {
		if ok, dropped := a.dropLog.Allow("stream"); ok {
			a.logf("debug: stream delta send failed ×%d: %v", dropped+1, err)
		}
		return
	}
	if sent {
		a.streamSent = true
	}
}

// currentChatID 返回最近一条入站消息的会话标识（流式/进度帧不带 chat_id）。
func (a *app) currentChatID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastChatID
}

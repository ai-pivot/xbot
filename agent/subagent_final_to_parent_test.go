package agent

// interactive 子代理「最终回复投错会话」回归测试。
//
// 生产 bug（2026-11-03，线上 12:47:02Z 日志）：
//
//	{"msg":"sendMessage directSend dispatch","orig_channel":"agent",
//	 "orig_chat_id":"web:web-4/court.shoufu:main","is_final":false}
//	{"error":"unknown channel: agent","msg":"Failed to send final response via sendMessage"}
//
// interactive 子代理的物理会话就是它自己（channel="agent", chatID=子会话 key），
// 于是**它的最终答复投给了它自己**：父会话（用户/上游 agent 正在看的那个会话）
// 永远收不到子代理的答复。同类 bug 在 tools/send_message.go 的 to="user" 上修过
// 一次，当时只补了那一条路径，agent 普通输出路径没修 —— 本次修在投递路由层
// Agent.sendMessage，不再给调用点打补丁。
//
// 这组测试钉住四件事（每条都能单独变异成红）：
//  1. final + interactive 子会话 ⇒ 改投 parent（async，peer_message），且不再投自己；
//  2. 非 final 的进度文本仍投子会话自己，不进 parent；
//  3. 普通顶层会话（cli:/home/smith/x）带 final 元数据时不改投、不 panic；
//  4. final 不把 sessionFinalSent 打在子会话 key 上 —— 否则子代理第二轮变哑巴。

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"xbot/bus"
	ch "xbot/channel"
	"xbot/tools"
)

const (
	f2pChildKey  = "web:web-4/court.shoufu:main"
	f2pParentKey = "web:web-4"
	f2pParentCh  = "web"
	f2pParentID  = "web-4"
)

// f2pSent 记录一次 directSend 投递（fake directSend 的观测点）。
type f2pSent struct {
	Channel string
	ChatID  string
	Content string
}

type f2pRecorder struct {
	mu   sync.Mutex
	sent []f2pSent
}

func (r *f2pRecorder) send(msg ch.OutboundMsg) (string, error) {
	r.mu.Lock()
	r.sent = append(r.sent, f2pSent{Channel: msg.Channel, ChatID: msg.ChatID, Content: msg.Content})
	r.mu.Unlock()
	return "msg-1", nil
}

func (r *f2pRecorder) messages() []f2pSent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]f2pSent, len(r.sent))
	copy(out, r.sent)
	return out
}

func (r *f2pRecorder) findContent(content string) []f2pSent {
	var hits []f2pSent
	for _, m := range r.messages() {
		if m.Content == content {
			hits = append(hits, m)
		}
	}
	return hits
}

// f2pAgent 造一个最小 Agent：真实 BackgroundTaskManager（async 父会话投递通道）
// + fake directSend（观测点）。withChild=true 时注册一条 interactive 子会话。
func f2pAgent(t *testing.T, withChild bool) (*Agent, *tools.BackgroundTaskManager, *f2pRecorder) {
	t.Helper()
	mgr := tools.NewBackgroundTaskManager()
	rec := &f2pRecorder{}
	a := &Agent{bus: bus.NewMessageBus(), directSend: rec.send}
	a.bgTaskMgr.Store(mgr)
	if withChild {
		a.interactiveSubAgents.Store(f2pChildKey, &interactiveAgent{
			roleName:  "court.shoufu",
			instance:  "main",
			parentKey: f2pParentKey,
			lastUsed:  time.Now(),
		})
	}
	return a, mgr, rec
}

// drainAsyncNotifications 取空 NotifyCh 里的 async 通知（SendAsyncMessage 的落点）。
func drainAsyncNotifications(t *testing.T, mgr *tools.BackgroundTaskManager) []*tools.AsyncMessageNotification {
	t.Helper()
	var out []*tools.AsyncMessageNotification
	for {
		select {
		case n := <-mgr.NotifyCh:
			am, ok := n.(*tools.AsyncMessageNotification)
			if !ok {
				t.Fatalf("NotifyCh 上出现非 async 通知: %T", n)
			}
			out = append(out, am)
		default:
			return out
		}
	}
}

// ① 有 parent：最终答复必须**双投** —— 父会话收到（async / peer_message），
// 同时子会话自己的终局帧照旧投给它自己（不因修 bug 而让子会话收不到）。
func TestSubagentFinalToParent_RoutesFinalReplyToParent(t *testing.T) {
	a, mgr, rec := f2pAgent(t, true)

	const finalReply = "最终答复：任务已完成"
	if err := a.sendMessage("agent", f2pChildKey, finalReply,
		map[string]string{ch.MetaFinalReply: "true"}); err != nil {
		t.Fatalf("sendMessage: %v", err)
	}

	notifs := drainAsyncNotifications(t, mgr)
	if len(notifs) != 1 {
		t.Fatalf("parent async 通知数 = %d, want 1 (%+v)", len(notifs), notifs)
	}
	n := notifs[0]
	if n.Key != f2pParentKey {
		t.Errorf("通知 Key = %q, want %q", n.Key, f2pParentKey)
	}
	if n.Source != tools.AsyncSourcePeer {
		t.Errorf("通知 Source = %q, want %q", n.Source, tools.AsyncSourcePeer)
	}
	if n.Content != finalReply {
		t.Errorf("通知 Content = %q, want %q", n.Content, finalReply)
	}

	// 子会话自己这一轮的终局帧照旧送达（= 子会话视图能正常收尾，不是"没收尾但应该没事"）。
	hits := rec.findContent(finalReply)
	if len(hits) != 1 {
		t.Fatalf("子会话终局帧投递次数 = %d, want 1（双投语义：子会话自己也要收到）: %+v", len(hits), hits)
	}
	if hits[0].Channel != "agent" || hits[0].ChatID != f2pChildKey {
		t.Errorf("子会话终局帧投递目标 = %s:%s, want agent:%s", hits[0].Channel, hits[0].ChatID, f2pChildKey)
	}

	// 端到端：parent 空闲时 drain 会把这条答复注入 parent 的入站队列（= 父会话的会话流）。
	a.enqueueBgNotification(n)
	a.drainAndProcessNotifications(f2pParentKey)
	select {
	case in := <-a.bus.Inbound:
		if in.Channel != f2pParentCh || in.ChatID != f2pParentID {
			t.Errorf("parent 入站消息 = %s:%s, want %s:%s", in.Channel, in.ChatID, f2pParentCh, f2pParentID)
		}
		if in.Content != finalReply {
			t.Errorf("parent 入站内容 = %q, want %q（父会话必须真的收到子代理的答复）", in.Content, finalReply)
		}
	default:
		t.Fatal("parent 入站队列为空：异步通知没有被 drain 成父会话消息")
	}
}

// ② 非 final 的进度文本仍投子会话自身，不进 parent。
func TestSubagentFinalToParent_ProgressStaysInSubsession(t *testing.T) {
	a, mgr, rec := f2pAgent(t, true)

	const progress = "进度：正在读 agent.go"
	if err := a.sendMessage("agent", f2pChildKey, progress,
		map[string]string{"progress": "true"}); err != nil {
		t.Fatalf("sendMessage: %v", err)
	}

	hits := rec.findContent(progress)
	if len(hits) != 1 {
		t.Fatalf("进度文本投递次数 = %d, want 1: %+v", len(hits), hits)
	}
	if hits[0].Channel != "agent" || hits[0].ChatID != f2pChildKey {
		t.Errorf("进度文本投递目标 = %s:%s, want agent:%s", hits[0].Channel, hits[0].ChatID, f2pChildKey)
	}
	if notifs := drainAsyncNotifications(t, mgr); len(notifs) != 0 {
		t.Fatalf("进度文本不该进 parent 通道: %+v", notifs)
	}
	// 流式进度块不属于终局 → 不得标记 sessionFinalSent。
	if _, sent := a.sessionFinalSent.Load(qualifyChatID("agent", f2pChildKey)); sent {
		t.Fatal("进度文本错误地设置了 sessionFinalSent")
	}
}

// ③ 普通顶层会话（注册表里没有）带 final 元数据：不改投、不 panic。
// 这条同时是「门禁必须靠 interactiveSubAgents 命中」的钉子：
// parseInteractiveKeyParent("cli:/home/smith/x") 会返回 ("cli", "/home/smith/x") 非空，
// 只看解析结果就会把顶层会话的最终答复扔到别的会话去。
func TestSubagentFinalToParent_TopLevelSessionNotRerouted(t *testing.T) {
	a, mgr, rec := f2pAgent(t, false)

	const topChat = "/home/smith/x"
	const finalReply = "顶层会话的最终答复"
	if err := a.sendMessage("cli", topChat, finalReply,
		map[string]string{ch.MetaFinalReply: "true"}); err != nil {
		t.Fatalf("sendMessage: %v", err)
	}

	hits := rec.findContent(finalReply)
	if len(hits) != 1 {
		t.Fatalf("顶层会话投递次数 = %d, want 1: %+v", len(hits), hits)
	}
	if hits[0].Channel != "cli" || hits[0].ChatID != topChat {
		t.Errorf("顶层会话投递目标 = %s:%s, want cli:%s", hits[0].Channel, hits[0].ChatID, topChat)
	}
	if notifs := drainAsyncNotifications(t, mgr); len(notifs) != 0 {
		t.Fatalf("顶层会话的最终答复被误改投: %+v", notifs)
	}
	// parent 解析得出的 cli:/home/smith 之类目标绝不该出现。
}

// ④ 同一个 interactive 子会话连跑两轮：第一轮 final 之后，第二轮仍然能投递。
//
// 为什么必须钉：sessionFinalSent 的唯一清理点是 resetSessionState
// （agent/agent.go:3008，只在 processMessage 入站路径调用），而 interactive
// 子会话的 Run 不走 processMessage ⇒ 一旦第一轮 final 给它打上标记就永不清除，
// 第二轮起该子会话的所有 sendMessage 都会被「工具已发最终回复 → 跳过」静默吞掉，
// 子代理从第二次对话开始变哑巴（只在第二轮才发作，线上极难发现）。
func TestSubagentFinalToParent_SecondTurnNotSilenced(t *testing.T) {
	a, mgr, rec := f2pAgent(t, true)

	// ── 第一轮：权威最终回复（双投：parent + 子会话自己）──
	if err := a.sendMessage("agent", f2pChildKey, "第一轮最终答复",
		map[string]string{ch.MetaFinalReply: "true"}); err != nil {
		t.Fatalf("第一轮 sendMessage: %v", err)
	}
	if notifs := drainAsyncNotifications(t, mgr); len(notifs) != 1 {
		t.Fatalf("第一轮 parent 通知数 = %d, want 1", len(notifs))
	}
	// ✂ 变异点：把 sendMessage 里 `if isCardFinal` 改回 `if isFinal` ⇒ 这里立刻红。
	if _, sent := a.sessionFinalSent.Load(qualifyChatID("agent", f2pChildKey)); sent {
		t.Fatal("第一轮 final 给子会话 key 打上了 sessionFinalSent：第二轮所有输出会被静默吞掉")
	}

	// ── 第二轮：进度 + 最终答复都必须真的投递出去 ──
	const secondProgress = "第二轮进度：开始核查"
	if err := a.sendMessage("agent", f2pChildKey, secondProgress); err != nil {
		t.Fatalf("第二轮进度 sendMessage: %v", err)
	}
	if hits := rec.findContent(secondProgress); len(hits) != 1 {
		t.Fatalf("第二轮进度投递次数 = %d, want 1（被 sessionFinalSent 拦掉了？）", len(hits))
	}
	// 第二轮的最终回复同样双投：parent 再收一条（不是 0 条，也不是 2 条）。
	if err := a.sendMessage("agent", f2pChildKey, "第二轮最终答复",
		map[string]string{ch.MetaFinalReply: "true"}); err != nil {
		t.Fatalf("第二轮最终 sendMessage: %v", err)
	}
	notifs := drainAsyncNotifications(t, mgr)
	if len(notifs) != 1 || notifs[0].Content != "第二轮最终答复" {
		t.Fatalf("第二轮 parent 通知 = %+v, want 恰好一条「第二轮最终答复」", notifs)
	}
}

// ── 日志级证据（验收 C）──────────────────────────────────────────────────
type f2pLogHook struct {
	mu      sync.Mutex
	entries []*logrus.Entry
}

func (h *f2pLogHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *f2pLogHook) Fire(e *logrus.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	data := make(logrus.Fields, len(e.Data))
	for k, v := range e.Data {
		data[k] = v
	}
	h.entries = append(h.entries, &logrus.Entry{Message: e.Message, Data: data})
	return nil
}

func (h *f2pLogHook) withMessage(msg string) []*logrus.Entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*logrus.Entry
	for _, e := range h.entries {
		if e.Message == msg {
			out = append(out, e)
		}
	}
	return out
}

// TestSubagentFinalToParent_LogEvidence 捕获日志，钉住：
//   - 终局帧的 `sendMessage directSend dispatch` 里 is_final=true（旧实现恒为 false，
//     排查时根本看不出"这是终局帧"）；
//   - 子会话终局帧被改投 parent；
//   - parent busy 路径：async 通知经 Run loop 注入时打出
//     `Injected async message into Run loop` + source=peer_message。
func TestSubagentFinalToParent_LogEvidence(t *testing.T) {
	hook := &f2pLogHook{}
	std := logrus.StandardLogger()
	prev := std.ReplaceHooks(make(logrus.LevelHooks))
	std.AddHook(hook)
	defer func() {
		std.ReplaceHooks(prev)
	}()

	a, mgr, _ := f2pAgent(t, true)

	// (a) 顶层会话的终局帧：directSend 仍然走，日志 is_final=true。
	const topFinal = "顶层终局帧"
	if err := a.sendMessage("cli", "/home/smith/x", topFinal,
		map[string]string{ch.MetaFinalReply: "true"}); err != nil {
		t.Fatalf("sendMessage(top): %v", err)
	}
	dispatch := hook.withMessage("sendMessage directSend dispatch")
	var topEntry *logrus.Entry
	for _, e := range dispatch {
		if e.Data["orig_chat_id"] == "/home/smith/x" {
			topEntry = e
		}
	}
	if topEntry == nil {
		t.Fatalf("没捕到顶层会话的 sendMessage directSend dispatch 日志: %+v", dispatch)
	}
	if topEntry.Data["is_final"] != true {
		t.Fatalf("is_final = %v, want true（终局帧必须被识别）", topEntry.Data["is_final"])
	}

	// (b) 子会话的终局帧：改投 parent，并留下可审计的日志。
	const childFinal = "子代理终局帧"
	if err := a.sendMessage("agent", f2pChildKey, childFinal,
		map[string]string{ch.MetaFinalReply: "true"}); err != nil {
		t.Fatalf("sendMessage(child): %v", err)
	}
	routed := hook.withMessage("sendMessage final reply routed to parent session")
	if len(routed) != 1 {
		t.Fatalf("parent 改投日志条数 = %d, want 1", len(routed))
	}
	if routed[0].Data["parent_channel"] != f2pParentCh || routed[0].Data["parent_chat_id"] != f2pParentID || routed[0].Data["is_final"] != true {
		t.Fatalf("改投日志字段不对: %+v", routed[0].Data)
	}

	// (c) parent busy：同一条 async 通知被 Run loop 作为合成工具结果注入。
	notifs := drainAsyncNotifications(t, mgr)
	if len(notifs) != 1 {
		t.Fatalf("async 通知数 = %d, want 1", len(notifs))
	}
	rs := &runState{cfg: RunConfig{}}
	if err := rs.injectAsyncMessageNotification(context.Background(), 1, notifs[0]); err != nil {
		t.Fatalf("injectAsyncMessageNotification: %v", err)
	}
	injected := hook.withMessage("Injected async message into Run loop")
	if len(injected) != 1 {
		t.Fatalf("Run loop 注入日志条数 = %d, want 1", len(injected))
	}
	if injected[0].Data["source"] != tools.AsyncSourcePeer {
		t.Fatalf("注入日志 source = %v, want %q", injected[0].Data["source"], tools.AsyncSourcePeer)
	}
	t.Logf("日志证据: dispatch(is_final=%v orig_chat_id=%v) / routed(parent_chat_id=%v) / injected(source=%v)",
		topEntry.Data["is_final"], topEntry.Data["orig_chat_id"],
		routed[0].Data["parent_chat_id"], injected[0].Data["source"])
}

// ── ⑥ 兜底链重发同一帧：父会话只能收到 1 条（真·双投 bug 的回归钉子）──────

// f2pFailRecorder 复刻生产投递形态：channel="agent" 没有注册 dispatcher，
// directSend 必然返回 "unknown channel: agent"（这正是触发兜底回包的路径）。
type f2pFailRecorder struct {
	mu       sync.Mutex
	attempts []f2pSent
}

func (r *f2pFailRecorder) send(msg ch.OutboundMsg) (string, error) {
	r.mu.Lock()
	r.attempts = append(r.attempts, f2pSent{Channel: msg.Channel, ChatID: msg.ChatID, Content: msg.Content})
	r.mu.Unlock()
	if msg.Channel == "agent" {
		return "", fmt.Errorf("unknown channel: %s", msg.Channel)
	}
	return "msg-ok", nil
}

func (r *f2pFailRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.attempts)
}

// TestSubagentFinalToParent_FallbackResendNotDoubleInjected 钉住「同一帧被兜底链
// 重发两次 ⇒ 父会话被注入两次」这条真缺陷（2026-11-03 e2e 实证：父会话
// session_messages 里同一内容 2 行、request_id 与 turn_id 相同）。
//
// 生产链路（agent_process.go:773-783 → agent.go:3966）：
//
//	handleRunOutput: a.sendMessage("agent", childKey, final, sendMeta)
//	  → 注入 parent 1 次；directSend("agent") 失败 "unknown channel: agent"
//	  → 返回 &OutboundMsg{…, Metadata: sendMeta}（**同一个 map 对象**）
//	chatProcessLoop: a.sendMessage(msg.Channel, msg.ChatID, content, msg.Metadata)
//	  → 旧实现第二次仍命中路由 ⇒ 父会话 2 条
//
// 修法是一次性标记 metaParentRouted（写在共享 map 上）。变异点：删掉
// sendMessage 判据里的 `msg.Metadata[metaParentRouted] != "true"` ⇒ 本例父
// 通知数变 2、routed 日志变 2 ⇒ 必须红。
func TestSubagentFinalToParent_FallbackResendNotDoubleInjected(t *testing.T) {
	hook := &f2pLogHook{}
	std := logrus.StandardLogger()
	prev := std.ReplaceHooks(make(logrus.LevelHooks))
	std.AddHook(hook)
	defer std.ReplaceHooks(prev)

	mgr := tools.NewBackgroundTaskManager()
	rec := &f2pFailRecorder{}
	a := &Agent{bus: bus.NewMessageBus(), directSend: rec.send}
	a.bgTaskMgr.Store(mgr)
	a.interactiveSubAgents.Store(f2pChildKey, &interactiveAgent{
		roleName:  "court.shoufu",
		instance:  "main",
		parentKey: f2pParentKey,
		lastUsed:  time.Now(),
	})

	const finalReply = "E2E-SUB-OK-2"
	// handleRunOutput 造的 sendMeta：同一个 map 会被兜底回包原样带出来。
	sendMeta := map[string]string{ch.MetaFinalReply: "true", "turn_id": "3"}

	// 第一次：子会话的终局帧（handleRunOutput 路径）。前置条件必须是投递失败。
	if err := a.sendMessage("agent", f2pChildKey, finalReply, sendMeta); err == nil {
		t.Fatalf("前置条件不成立：agent 渠道的 directSend 必须失败（生产 unknown channel: agent）")
	}

	// 兜底回包：handleRunOutput 返回的 OutboundMsg，Metadata 就是同一个 map。
	fallback := ch.OutboundMsg{
		Channel:  "agent",
		ChatID:   f2pChildKey,
		Content:  finalReply,
		Metadata: sendMeta,
	}
	// 第二次：chatProcessLoop 用兜底 OutboundMsg 重发 ⇒ 仍然走一次注定失败的
	// directSend（错误与第一次同源），但**不得**再注入父会话。
	if err := a.sendMessage(fallback.Channel, fallback.ChatID, fallback.Content, fallback.Metadata); err == nil {
		t.Fatalf("第二次 sendMessage 应当仍在 directSend 上失败（保持原投递行为，不 early-return）")
	}

	notifs := drainAsyncNotifications(t, mgr)
	if len(notifs) != 1 {
		t.Fatalf("父会话 async 通知数 = %d, want 1（兜底链重发同一帧不得重复注入）: %+v", len(notifs), notifs)
	}
	if notifs[0].Key != f2pParentKey || notifs[0].Content != finalReply || notifs[0].Source != tools.AsyncSourcePeer {
		t.Fatalf("父会话通知字段不对: %+v", notifs[0])
	}

	// 改投日志也只能有 1 条（第二条被标记拦下，不留"routed"假象）。
	if routed := hook.withMessage("sendMessage final reply routed to parent session"); len(routed) != 1 {
		t.Fatalf("parent 改投日志条数 = %d, want 1: %+v", len(routed), routed)
	}

	// directSend 仍然被调用两次：去重不能以"吞掉原有投递尝试"为代价。
	if got := rec.count(); got != 2 {
		t.Fatalf("directSend 调用次数 = %d, want 2（第二次必须仍然尝试原投递，只是跳过注入）", got)
	}

	// 标记必须落在**共享** map 上（第二次调用拿到的就是这个 map）。
	if sendMeta[metaParentRouted] != "true" {
		t.Fatalf("共享 metadata 上的 %s = %q, want \"true\"（否则兜底重发会再注入一次）",
			metaParentRouted, sendMeta[metaParentRouted])
	}

	// parent busy：这条通知作为合成工具结果进 Run loop，日志恰好 1 条。
	rs := &runState{cfg: RunConfig{}}
	if err := rs.injectAsyncMessageNotification(context.Background(), 1, notifs[0]); err != nil {
		t.Fatalf("injectAsyncMessageNotification: %v", err)
	}
	if injected := hook.withMessage("Injected async message into Run loop"); len(injected) != 1 {
		t.Fatalf("Run loop 注入日志条数 = %d, want 1: %+v", len(injected), injected)
	}
}

// ── ⑦ 真·集成：生产的失败兜底链（handleRunOutput → chatProcessLoop）───────────
//
// ① 与 ⑥ 的区别：⑥ 手工把「同一个 map 连调两次 sendMessage」，只证明**标记**本身；
// ⑦ 走**生产的真实函数**，把缺陷原始路径整条跑一遍：
//
//	handleRunOutput(ctx, msg, out, sess, "")   // agent/agent_process.go:765
//	  ├─ a.sendMessage("agent", 子会话, final, sendMeta)   // 未注册渠道 ⇒ 失败
//	  │    └─ 注入父会话 1 次 + 给 sendMeta 打一次性标记
//	  └─ return &OutboundMsg{…, Metadata: sendMeta}        // :773-783 同一个 map
//	chatProcessLoop（agent/agent.go:3966）
//	  └─ a.sendMessage(fallback.Channel, fallback.ChatID, fallback.Content, fallback.Metadata)
//	       ⇒ 第二次必须**不再**注入父会话（否则父会话同一内容 2 行 = 本缺陷）
//
// 断言是"数条数"，不是"没报错"；父会话通知数与 routed 日志数都必须是 1。
// 变异：删掉 `msg.Metadata[metaParentRouted] = "true"` ⇒ 本例必须红（通知数=2）。
func TestSubagentFinalToParent_RealFallbackResendSingleInjection(t *testing.T) {
	hook := &f2pLogHook{}
	std := logrus.StandardLogger()
	prev := std.ReplaceHooks(make(logrus.LevelHooks))
	std.AddHook(hook)
	defer std.ReplaceHooks(prev)

	mgr := tools.NewBackgroundTaskManager()
	rec := &f2pFailRecorder{}
	a := &Agent{bus: bus.NewMessageBus(), directSend: rec.send}
	a.bgTaskMgr.Store(mgr)
	a.interactiveSubAgents.Store(f2pChildKey, &interactiveAgent{
		roleName:  "court.shoufu",
		instance:  "main",
		parentKey: f2pParentKey,
		lastUsed:  time.Now(),
	})
	_, sess := newAgentHistorySession(t)

	const finalReply = "E2E-REAL-FALLBACK-1"
	msg := bus.InboundMessage{
		Channel:  "agent",
		ChatID:   f2pChildKey,
		Content:  "父会话交给子代理的问题",
		Metadata: map[string]string{"turn_id": "7"},
	}
	out := &RunOutput{OutboundMsg: &ch.OutboundMsg{Content: finalReply}}

	// ① 生产第一步：首投在未注册的 agent 渠道上失败 ⇒ 返回兜底 OutboundMsg。
	fallback, err := a.handleRunOutput(context.Background(), msg, out, sess, "")
	if err != nil {
		t.Fatalf("handleRunOutput err = %v, want nil（投递失败应走兜底回包，不抛错）", err)
	}
	if fallback == nil {
		t.Fatalf("handleRunOutput 兜底回包 = nil, want 非 nil（否则这一轮终局帧直接丢失）")
	}
	if fallback.Channel != "agent" || fallback.ChatID != f2pChildKey || fallback.Content != finalReply {
		t.Fatalf("兜底回包字段不对: %+v", fallback)
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("首投 directSend 次数 = %d, want 1（首投必须真的尝试过投递）", got)
	}

	// ② 生产第二步：chatProcessLoop（agent.go:3966）用兜底 OutboundMsg 再发一次。
	if err := a.sendMessage(fallback.Channel, fallback.ChatID, fallback.Content, fallback.Metadata); err == nil {
		t.Fatalf("第二次投递应当仍在 directSend 上失败（渠道未注册），不得改变原投递行为")
	}

	// ③ 硬断言：父会话**恰好 1 条**通知（整条链路只注入一次）。
	notifs := drainAsyncNotifications(t, mgr)
	if len(notifs) != 1 {
		t.Fatalf("父会话 async 通知数 = %d, want 1（真·兜底链：同一帧被注入 2 次就是本缺陷）: %+v",
			len(notifs), notifs)
	}
	if notifs[0].Key != f2pParentKey || notifs[0].Content != finalReply || notifs[0].Source != tools.AsyncSourcePeer {
		t.Fatalf("父会话通知字段不对: %+v", notifs[0])
	}

	// ④ 第二次**没有**再触发路由：routed 日志恰好 1 条。
	if routed := hook.withMessage("sendMessage final reply routed to parent session"); len(routed) != 1 {
		t.Fatalf("parent 改投日志条数 = %d, want 1（第二次重发不得再走路由）: %+v", len(routed), routed)
	}

	// ⑤ 两次投递尝试都在（去重不能以吞掉原有投递为代价）。
	if got := rec.count(); got != 2 {
		t.Fatalf("directSend 调用次数 = %d, want 2（首投 + 兜底重发，都各自尝试过）", got)
	}

	// ⑥ 机制自证：标记确实写进了**兜底回包带出来的那个 map**。
	if fallback.Metadata[metaParentRouted] != "true" {
		t.Fatalf("兜底回包 Metadata[%s] = %q, want \"true\"（标记没落到共享 map 上，重发必然再注入）",
			metaParentRouted, fallback.Metadata[metaParentRouted])
	}
}

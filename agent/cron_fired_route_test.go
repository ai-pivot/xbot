package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/tools"
)

// cronSubAgentKey 是第一层子代理会话的裸 interactive key（interactiveKey 的输出形态）。
const cronSubAgentKey = "web:web-4/court.gongbu:inst-1"

// cronNestedSubAgentKey 是第二层（嵌套）子代理会话的裸 interactive key：
// 父是 agent 会话 "web:web-4/court.shoufu:main"，所以它自身以 "agent:" 开头。
// 形态与线上 DB 实测一致（工部提供）：
//
//	agent:web:web-4/court.shoufu:main/court.gongbu:inst-2
const cronNestedSubAgentKey = "agent:web:web-4/court.shoufu:main/court.gongbu:inst-2"

// newCronRouteTestAgent 构造一个最小 Agent：只需要 bus + bgTaskMgr + 生命周期
// 信号通道就能跑真实 bgNotifyLoop（与 bg_notify_test.go 的最小构造一致）。
func newCronRouteTestAgent(t *testing.T) (*Agent, *tools.BackgroundTaskManager, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:             bus.NewMessageBus(),
		agentCtx:        ctx,
		lifecycleStopCh: make(chan struct{}),
	}
	a.bgTaskMgr.Store(mgr)
	t.Cleanup(func() {
		close(a.lifecycleStopCh)
		cancel()
	})
	return a, mgr, cancel
}

// TestCronFired_RoutesToSubAgentSession 端到端验证触发路由：
// SendCronFired → bgNotifyLoop → (drain) → bus.Inbound 的落点必须是
// "agent:<interactive key>"（子代理会话），而**不是**父会话 "web:web-4"。
//
// 这是本次改动的核心契约：cronOwnership 把子代理 job 记成
// (channel="agent", chatID=<interactive key>)，scheduler 触发时拼出
// sessionKey = "agent:" + key，事件必须落在这个会话上。
func TestCronFired_RoutesToSubAgentSession(t *testing.T) {
	a, mgr, _ := newCronRouteTestAgent(t)
	go a.bgNotifyLoop()

	mgr.SendCronFired(&tools.CronFired{
		Key:     "agent:" + cronSubAgentKey,
		Sid:     "admin",
		Message: "check cluster health",
	})

	select {
	case msg := <-a.bus.Inbound:
		if msg.Channel != "agent" {
			t.Fatalf("cron notification channel = %q, want \"agent\" (子代理会话)", msg.Channel)
		}
		if msg.ChatID != cronSubAgentKey {
			t.Fatalf("cron notification chatID = %q, want %q", msg.ChatID, cronSubAgentKey)
		}
		gotKey := msg.Channel + ":" + msg.ChatID
		if gotKey != "agent:"+cronSubAgentKey {
			t.Fatalf("cron notification sessionKey = %q, want %q", gotKey, "agent:"+cronSubAgentKey)
		}
		if gotKey == "web:web-4" {
			t.Fatal("cron notification fell back to the PARENT session web:web-4 — 子代理 job 的触发不能落父会话")
		}
		if !strings.Contains(msg.Content, "check cluster health") {
			t.Fatalf("cron notification content lost the job message: %q", msg.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out: cron notification never reached bus.Inbound")
	}
}

// TestCronFired_LiveSubAgentSessionFailureFallsBack 验证**投递失败必有回落**：
//
// 构造一个必然失败的"活"会话（running=true 且 cfg==nil ⇒
// agent/interactive.go:2318-2325 的 "still initializing" 分支），此时
// SendToInteractiveSession 的返回值是 `(*OutboundMsg{Error: err}, nil)` ——
// 第二个返回值是 nil，失败信息只在 OutboundMsg.Error 里。
//
// 判别力要求：deliverCronFiredToSubAgentSession 认领后，bus.Inbound **必须**
// 收到一条 (agent, <裸 key>) 的回落注入，且内容含原始 message。
// 「窗口内什么都没发生」= 事件被静默吞掉 = FAIL（不允许写成"没收到也算过"）。
func TestCronFired_LiveSubAgentSessionFailureFallsBack(t *testing.T) {
	a, _, _ := newCronRouteTestAgent(t)

	a.interactiveSubAgents.Store(cronSubAgentKey, &interactiveAgent{
		roleName: "court.gongbu",
		instance: "inst-1",
		lastUsed: time.Now(),
		running:  true, // 必然进 "still initializing" 分支
		// cfg == nil
	})

	const msg0 = "must-not-be-lost"
	c := &tools.CronFired{Key: "agent:" + cronSubAgentKey, Sid: "admin", Message: msg0}
	// 模拟 bgNotifyLoop 的顺序：先 buffer，再分流。
	a.enqueueBgNotification(c)

	if !a.deliverCronFiredToSubAgentSession(c) {
		t.Fatal("deliverCronFiredToSubAgentSession must claim a live sub-agent session (return true)")
	}

	select {
	case msg := <-a.bus.Inbound:
		if msg.Channel != "agent" || msg.ChatID != cronSubAgentKey {
			t.Fatalf("fallback injection landed on (%q, %q), want (\"agent\", %q)", msg.Channel, msg.ChatID, cronSubAgentKey)
		}
		if !strings.Contains(msg.Content, msg0) {
			t.Fatalf("fallback injection lost the cron message (event swallowed): %q", msg.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("投递失败后事件被静默吞掉：5s 内 bus.Inbound 没有收到回落注入（SendToInteractiveSession 的失败在 OutboundMsg.Error 里，第二个返回值恒为 nil）")
	}
}

// TestCronFired_RoutesToNestedSubAgentSession 覆盖**嵌套子代理**（父是 agent 会话）：
//
//	key = agent:web:web-4/court.shoufu:main/court.gongbu:inst-2
//
// job 归属仍是 ("agent", <该 key>)，scheduler 拼出 "agent:"+key。
// 这里用"必然失败回落"的会话（running=true/cfg=nil）走完整投递路径，
// 断言落点是 (agent, <该 key>)，而不是任何父会话（web:web-4）。
// 同一用例还直接验证解析出的 (channel, chatID, role, instance) 能精确回到 map key
// —— 这是 SendToInteractiveSession 真正寻址的那一步。
func TestCronFired_RoutesToNestedSubAgentSession(t *testing.T) {
	a, _, _ := newCronRouteTestAgent(t)

	a.interactiveSubAgents.Store(cronNestedSubAgentKey, &interactiveAgent{
		roleName: "court.gongbu",
		instance: "inst-2",
		lastUsed: time.Now(),
		running:  true, // 必然失败回落，从而可在 bus.Inbound 上观测落点
	})

	// 解析 → 寻址闭环：解析出的四项必须能解析回同一个 map key。
	channelName, chatID, role, instance, ok := splitCronSubAgentTarget("agent:" + cronNestedSubAgentKey)
	if !ok {
		t.Fatalf("splitCronSubAgentTarget must accept nested key %q", cronNestedSubAgentKey)
	}
	gotKey, err := a.resolveInteractiveSessionKey(channelName, chatID, role, instance)
	if err != nil {
		t.Fatalf("resolveInteractiveSessionKey(%q,%q,%q,%q): %v — 解析出的地址必须能寻址回嵌套会话",
			channelName, chatID, role, instance, err)
	}
	if gotKey != cronNestedSubAgentKey {
		t.Fatalf("resolveInteractiveSessionKey = %q, want %q (nested sub-agent 寻址必须闭环)", gotKey, cronNestedSubAgentKey)
	}

	const msg0 = "nested tick"
	c := &tools.CronFired{Key: "agent:" + cronNestedSubAgentKey, Sid: "admin", Message: msg0}
	a.enqueueBgNotification(c)
	if !a.deliverCronFiredToSubAgentSession(c) {
		t.Fatal("nested sub-agent session must be claimed by deliverCronFiredToSubAgentSession")
	}

	select {
	case msg := <-a.bus.Inbound:
		got := msg.Channel + ":" + msg.ChatID
		if got != "agent:"+cronNestedSubAgentKey {
			t.Fatalf("nested cron fallback landed on %q, want %q", got, "agent:"+cronNestedSubAgentKey)
		}
		if msg.Channel == "web" && msg.ChatID == "web-4" {
			t.Fatal("nested cron event leaked to the root parent session web:web-4")
		}
		if !strings.Contains(msg.Content, msg0) {
			t.Fatalf("nested cron fallback lost the message: %q", msg.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nested: 投递失败后事件被静默吞掉（bus.Inbound 无回落注入）")
	}
}

// TestInteractiveDeliveryFailed 锁住 SendToInteractiveSession 的失败判定：
// 该函数的失败信息在 OutboundMsg.Error 里，第二个返回值恒为 nil——
// 只判 err != nil 会把失败当成功（事件随后被静默吞掉）。
func TestInteractiveDeliveryFailed(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name string
		out  *channel.OutboundMsg
		err  error
		want bool
	}{
		{"err only", &channel.OutboundMsg{}, boom, true},
		{"out.Error only (真实失败形态)", &channel.OutboundMsg{Error: boom, Content: boom.Error()}, nil, true},
		{"no receipt (nil out)", nil, nil, true},
		{"success", &channel.OutboundMsg{Content: "✅ delivered"}, nil, false},
	}
	for _, c := range cases {
		if got := interactiveDeliveryFailed(c.out, c.err); got != c.want {
			t.Fatalf("%s: interactiveDeliveryFailed = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestSplitCronSubAgentTarget 锁住"哪些 key 算子代理会话"的判定：
// 主会话的 job key 绝不能被误判成子代理会话（否则主会话 cron 会被劫持到
// interactive 投递路径，既有行为被破坏）；嵌套子代理的 key 必须正确解出
// (父 channel="agent", 父 chatID=<父 interactive key>, role, instance)。
func TestSplitCronSubAgentTarget(t *testing.T) {
	cases := []struct {
		key          string
		wantOK       bool
		wantChannel  string
		wantChatID   string
		wantRole     string
		wantInstance string
	}{
		{"agent:" + cronSubAgentKey, true, "web", "web-4", "court.gongbu", "inst-1"},
		{"agent:cli:/home/smith/x/court.shoufu:main", true, "cli", "/home/smith/x", "court.shoufu", "main"},
		// 嵌套：父是 agent 会话。
		{"agent:" + cronNestedSubAgentKey, true, "agent", "web:web-4/court.shoufu:main", "court.gongbu", "inst-2"},
		// 主会话 job：不是 "agent" channel ⇒ 必须返回 false。
		{"web:web-4", false, "", "", "", ""},
		{"cli:/home/smith/x", false, "", "", "", ""},
		{"feishu:ou_abc", false, "", "", "", ""},
		// 执行上下文镜像（chatID 不含 "/"）：不是 interactive key ⇒ false，
		// 交给既有通用路径（这是历史行为，不改）。
		{"agent:web:web-4", false, "", "", "", ""},
		{"", false, "", "", "", ""},
	}
	for _, c := range cases {
		channelName, chatID, role, instance, ok := splitCronSubAgentTarget(c.key)
		if ok != c.wantOK {
			t.Fatalf("splitCronSubAgentTarget(%q) ok = %v, want %v", c.key, ok, c.wantOK)
		}
		if !c.wantOK {
			continue
		}
		if channelName != c.wantChannel || chatID != c.wantChatID || role != c.wantRole || instance != c.wantInstance {
			t.Fatalf("splitCronSubAgentTarget(%q) = (%q,%q,%q,%q), want (%q,%q,%q,%q)",
				c.key, channelName, chatID, role, instance,
				c.wantChannel, c.wantChatID, c.wantRole, c.wantInstance)
		}
	}
}

// 正例说明（工部要求"做不到就明确写，不要伪造"）：
// SendToInteractiveSession 的**空闲**成功路径（idle + cfg 可用）会真的启动一轮
// Run（构造 messages、append 到 tenant session、调 a.Run 跑 LLM）。在最小 Agent
// 构造里没有 LLM/multiSession/工具链，无法在不发起真实 LLM 调用的情况下覆盖它。
// 但 **running=true 的成功分支**（`ia.pendingMessages` 入队 → 最多等
// subAgentSendAckWait → 返回"已入队"）**不需要** LLM，是真正的"投递成功"返回值
// （OutboundMsg.Error == nil）—— 下面 TestCronFired_ClaimedEventIsNotReinjectedByDrain
// 就是用它构造成功路径的，不再用"成功路径省略正例"当挡箭牌。

// collectInbound 收集 bus.Inbound 上陆续到达的消息，直到 quiet 时间内没有新消息
// 或到达 max 上限。用于断言"某条内容出现几次"（双投/丢失都是次数问题，只收
// 第一条会把双投漏掉——这正是上一轮 M4 缺口的成因）。
func collectInbound(t *testing.T, a *Agent, quiet, max time.Duration) []bus.InboundMessage {
	t.Helper()
	var got []bus.InboundMessage
	idle := time.NewTimer(quiet)
	defer idle.Stop()
	limit := time.NewTimer(max)
	defer limit.Stop()
	for {
		select {
		case msg := <-a.bus.Inbound:
			got = append(got, msg)
			if !idle.Stop() {
				<-idle.C
			}
			idle.Reset(quiet)
		case <-idle.C:
			return got
		case <-limit.C:
			return got
		}
	}
}

// TestCronFired_FailedDeliveryKeepsSiblingsAndNoDuplicate 锁住两条**同时成立**的不变量：
//
//	① 不丢：同 key 上还有别的 pending（兄弟通知）时，本条投递失败补回**不得**
//	   把兄弟一起吞掉 —— takePendingBgNotifications 取走整条队列再只补回自己，
//	   兄弟就永久消失（2026-11-04 工部验收打回项 1）。
//	② 不双投：本条失败补回后只能出现一次（补回用 ensure 按事件实例去重）。
//
// 判别力：
//   - 把 takeOneBgNotification 换回 takePendingBgNotifications（2743f6be 原样）
//     ⇒ msg1 不再出现 ⇒ 本用例红（"兄弟被吞"）。
//   - 把 take 整个删掉 ⇒ msg0 被 enqueue 两次、drain 注入两份 ⇒ msg0 计数 2 ⇒ 红。
func TestCronFired_FailedDeliveryKeepsSiblingsAndNoDuplicate(t *testing.T) {
	a, _, _ := newCronRouteTestAgent(t)

	// 必然失败的"活"会话（running=true + cfg==nil ⇒ "still initializing"），
	// 投递失败后走 ensure + drain 的补回路径。
	a.interactiveSubAgents.Store(cronSubAgentKey, &interactiveAgent{
		roleName: "court.gongbu",
		instance: "inst-1",
		lastUsed: time.Now(),
		running:  true,
	})

	const msg0 = "sibling-probe-0-exactly-once"
	const msg1 = "sibling-probe-1-must-survive"
	c0 := &tools.CronFired{Key: "agent:" + cronSubAgentKey, Sid: "admin", Message: msg0}
	c1 := &tools.CronFired{Key: "agent:" + cronSubAgentKey, Sid: "admin", Message: msg1}

	// 模拟 bgNotifyLoop 的顺序：同 key 两条事件先 buffer（msg0 是本次要被
	// interactive 路径认领的那条，msg1 是与它同 key 的兄弟事件）。
	a.enqueueBgNotification(c0)
	a.enqueueBgNotification(c1)

	if !a.deliverCronFiredToSubAgentSession(c0) {
		t.Fatal("live sub-agent session must be claimed (return true)")
	}

	got := collectInbound(t, a, 700*time.Millisecond, 5*time.Second)
	if len(got) == 0 {
		t.Fatal("投递失败后 5s 内 bus.Inbound 一条注入都没有：事件被静默吞掉")
	}
	n0, n1 := 0, 0
	for _, m := range got {
		if strings.Contains(m.Content, msg0) {
			n0++
		}
		if strings.Contains(m.Content, msg1) {
			n1++
		}
	}
	if n0 != 1 {
		t.Fatalf("msg0 注入 %d 次（收到 %d 条消息），want 1 —— 双投/或没补回：%v", n0, len(got), contentsOf(got))
	}
	if n1 != 1 {
		t.Fatalf("msg1（同 key 的兄弟 pending）注入 %d 次（收到 %d 条消息），want 1 —— 兄弟通知被 take 整条队列吞掉了：%v",
			n1, len(got), contentsOf(got))
	}
}

// TestCronFired_ClaimedEventIsNotReinjectedByDrain 堵 M4 缺口：**取消 take 调用
// 时旧测试全绿**，说明没有任何用例能发现"通用 drain 路径会再注入一次"。
//
// 构造：活会话 running=true + cfg!=nil ⇒ SendToInteractiveSession 走"入队"
// 成功分支（不需要 LLM，3s ack 窗口后返回成功，OutboundMsg.Error==nil）。
// 认领后本条必须已经从 bgRunPending 消失；此时同会话的 chatWorker 在迭代边界
// 做一次 drain（a.drainAndProcessNotifications）**不得**再注入同一事件。
//
// 判别力：删掉 deliverCronFiredToSubAgentSession 里的 takeOne 调用 ⇒ 本条留在
// bgRunPending ⇒ 下面的 drain 把同一 cron 事件再注入一次（两条路径各注入一次）
// ⇒ 红。这正是"少一次 take"的变异。
func TestCronFired_ClaimedEventIsNotReinjectedByDrain(t *testing.T) {
	a, _, _ := newCronRouteTestAgent(t)

	a.interactiveSubAgents.Store(cronSubAgentKey, &interactiveAgent{
		roleName: "court.gongbu",
		instance: "inst-1",
		lastUsed: time.Now(),
		running:  true,
		cfg:      &RunConfig{}, // 非 nil ⇒ 走"入队"成功分支，而不是 still initializing
	})

	const msg = "claimed-event-must-not-be-injected-twice"
	c := &tools.CronFired{Key: "agent:" + cronSubAgentKey, Sid: "admin", Message: msg}
	a.enqueueBgNotification(c)

	if !a.deliverCronFiredToSubAgentSession(c) {
		t.Fatal("live sub-agent session must be claimed (return true)")
	}

	// ① 同步不变量：认领即出队（本条恰好被移除一次）。
	if rem := a.pendingBgNotifications(c.Key); len(rem) != 0 {
		t.Fatalf("认领后 bgRunPending[%q] 仍有 %d 条 pending，want 0 —— take 没把本条取走，通用 drain 会重复注入", c.Key, len(rem))
	}

	// ② 第二条路径：同会话 chatWorker 在迭代边界 drain（bgNotifyLoop 在
	// bgSessionStates 命中时就是发信号让 chatWorker 这样 drain 的）。
	a.drainAndProcessNotifications(c.Key)

	got := collectInbound(t, a, 500*time.Millisecond, 2*time.Second)
	for _, m := range got {
		if strings.Contains(m.Content, msg) {
			t.Fatalf("同一条 cron 事件被两条路径各注入一次（interactive 认领 + 通用 drain）：%q", m.Content)
		}
	}
}

// TestCronFired_DestroyedSubAgentSessionFallsBackToMainAgent 锁住**诚实的退化语义**
// （2026-11-04 工部验收打回项 2 选 A）：
//
// 子代理会话已不在内存（one-shot 的 Run 结束即 destroyInteractiveSession、
// 或 TTL 回收 / unload / 重启）时，到期事件**不丢**，但执行者是**主 Agent**
// —— 落点仍是 (agent, <interactive key>) 这个 tenant session，身份却是主 Agent。
// 这不是"以子代理身份触发"，别在别处承诺过头。
//
// 本用例不构造会话：正是 destroyInteractiveSession 之后的现场。
func TestCronFired_DestroyedSubAgentSessionFallsBackToMainAgent(t *testing.T) {
	a, mgr, _ := newCronRouteTestAgent(t)
	go a.bgNotifyLoop()

	const msg = "one-shot session already destroyed when cron fired"
	mgr.SendCronFired(&tools.CronFired{Key: "agent:" + cronSubAgentKey, Sid: "admin", Message: msg})

	select {
	case got := <-a.bus.Inbound:
		if got.Channel != "agent" || got.ChatID != cronSubAgentKey {
			t.Fatalf("returnal landing = (%q, %q), want (\"agent\", %q) —— 事件必须留在原 tenant，不许泄漏到父会话", got.Channel, got.ChatID, cronSubAgentKey)
		}
		if !strings.Contains(got.Content, msg) {
			t.Fatalf("回落注入丢了 cron message：%q", got.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("会话已销毁时事件被静默吞掉：bus.Inbound 5s 内没有收到回落注入")
	}
}

func contentsOf(msgs []bus.InboundMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Channel+":"+m.ChatID+" | "+m.Content)
	}
	return out
}

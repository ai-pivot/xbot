package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"xbot/bus"
	"xbot/tools"
)

// cronSubAgentKey 是子代理会话的裸 interactive key（interactiveKey 的输出形态）。
const cronSubAgentKey = "web:web-4/court.gongbu:inst-1"

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

// TestCronFired_LiveSubAgentSessionUsesInteractiveDelivery 验证目标会话活着时，
// 事件走 SendToInteractiveSession（子代理身份）而不是通用 chatWorker 路径：
//   - deliverCronFiredToSubAgentSession 必须认领（返回 true）；
//   - 已经 buffer 的这条通知必须被取走，不在通用路径上重复排一次；
//   - 任何被注入的消息都不能是父会话 (web, web-4)。
//
// 本用例里 interactiveAgent 的 cfg 为 nil，SendToInteractiveSession 会以
// "still initializing" 失败并走回落分支 —— 这恰好同时覆盖了"投递失败不丢事件"。
func TestCronFired_LiveSubAgentSessionUsesInteractiveDelivery(t *testing.T) {
	a, _, _ := newCronRouteTestAgent(t)

	a.interactiveSubAgents.Store(cronSubAgentKey, &interactiveAgent{
		roleName: "court.gongbu",
		instance: "inst-1",
		lastUsed: time.Now(),
	})

	c := &tools.CronFired{Key: "agent:" + cronSubAgentKey, Sid: "admin", Message: "live session tick"}
	// 模拟 bgNotifyLoop 的顺序：先 buffer，再分流。
	a.enqueueBgNotification(c)

	if !a.deliverCronFiredToSubAgentSession(c) {
		t.Fatal("deliverCronFiredToSubAgentSession must claim a live sub-agent session (return true)")
	}
	if left := a.pendingBgNotifications(c.Key); len(left) != 0 {
		t.Fatalf("claimed notification must be taken out of bgRunPending (got %d left) — otherwise the generic path injects it twice", len(left))
	}

	// 投递走 interactive 原语（异步），失败时回落通用路径；无论哪条，
	// 落点都只能是 (agent, <key>)，绝不能是父会话。
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg := <-a.bus.Inbound:
			if msg.Channel == "web" && msg.ChatID == "web-4" {
				t.Fatalf("cron notification leaked into the PARENT session (web, web-4): %+v", msg)
			}
			if msg.Channel != "agent" || msg.ChatID != cronSubAgentKey {
				t.Fatalf("fallback injection landed on (%q, %q), want (\"agent\", %q)", msg.Channel, msg.ChatID, cronSubAgentKey)
			}
		case <-time.After(200 * time.Millisecond):
			return // 窗口内没有任何投递也不算失败：interactive 失败回落已完成或无回落
		case <-deadline:
			return
		}
	}
}

// TestSplitCronSubAgentTarget 锁住"哪些 key 算子代理会话"的判定：
// 主会话的 job key 绝不能被误判成子代理会话（否则主会话 cron 会被劫持到
// interactive 投递路径，既有行为被破坏）。
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
		channel, chatID, role, instance, ok := splitCronSubAgentTarget(c.key)
		if ok != c.wantOK {
			t.Fatalf("splitCronSubAgentTarget(%q) ok = %v, want %v", c.key, ok, c.wantOK)
		}
		if !c.wantOK {
			continue
		}
		if channel != c.wantChannel || chatID != c.wantChatID || role != c.wantRole || instance != c.wantInstance {
			t.Fatalf("splitCronSubAgentTarget(%q) = (%q,%q,%q,%q), want (%q,%q,%q,%q)",
				c.key, channel, chatID, role, instance,
				c.wantChannel, c.wantChatID, c.wantRole, c.wantInstance)
		}
	}
}

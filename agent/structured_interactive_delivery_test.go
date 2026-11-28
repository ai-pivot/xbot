package agent

// structured_interactive_delivery_test.go — 卡片交互回执必须由**发起的子代理**执行。
//
// 用户报告（2026-11-25）：「子代理发出的消息，用户交互后似乎通知会发给主代理」。
// 根因：origin 是子代理 key 时 sessionaddr.Physical 拆成 ("agent", "<childKey>")，
// 而 DeliverStructuredMessage(Persisting) 只查 bgSessionStates（interactive 会话从不
// 注册这张表）⇒ 恒判 idle ⇒ injectInboundWithMetadata ⇒ chatWorker ⇒
// buildMainRunConfig（AgentID="main"）—— 行落在子代理 tenant，执行者却是主 Agent。
//
// 判据（本文件）：
//   - 只有 (agent, 含 '/' 的 childKey) 才走子代理投递；其它形态原样交回通用路径；
//   - 子代理活着：以子代理身份投递（不进 structuredQueue、不进 bus.Inbound）；
//     · 空闲 → 先落库 + **空**注入（历史行即唯一副本）
//     · 运行中 → **内联**内容入 pendingMessages（迭代边界注入），不落库
//   - 子代理已死且复活失败：**当无事发生**（不回落主 Agent），但记录必须落下。

import (
	"strings"
	"testing"
	"time"

	"xbot/bus"
)

func interactiveReceiptItem() *StructuredQueueItem {
	return &StructuredQueueItem{
		Channel:  "agent",
		ChatID:   "web:web-4/court.shoufu:main",
		SenderID: "web-4",
		Content:  "[系统] 用户取消了卡片 #7（kind=ask_user，未作答）。",
		Kind:     "answer", Priority: StructuredPriorityHigh, CorrelationID: "ask-7",
	}
}

func drainBusInbound(bs *bus.MessageBus) []bus.InboundMessage {
	var out []bus.InboundMessage
	for {
		select {
		case m := <-bs.Inbound:
			out = append(out, m)
		default:
			return out
		}
	}
}

// 非子代理目标：不能被这条捷径吞掉。
func TestDeliverStructuredToInteractive_OnlyChildKeysAreIntercepted(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	cases := []*StructuredQueueItem{
		{Channel: "web", ChatID: "web-4"},
		{Channel: "agent", ChatID: "court.shoufu"}, // 逻辑地址：没有 '/'，不是物理子会话 key
		{Channel: "agent", ChatID: "web:web-4"},    // 父会话
	}
	for _, item := range cases {
		persisted := 0
		if handled := a.deliverStructuredToInteractiveSession(item, func() error { persisted++; return nil }); handled {
			t.Fatalf("delivery for %s:%s must NOT be intercepted as an interactive sub-agent", item.Channel, item.ChatID)
		}
		if persisted != 0 {
			t.Fatalf("non-interactive target must not persist via this path (%d)", persisted)
		}
	}
}

// 子代理已死且无法复活：当无事发生 —— 不投递、绝不回落主 Agent；记录仍要落下。
func TestDeliverStructuredToInteractive_DeadSessionDoesNotFallBackToMainAgent(t *testing.T) {
	bs := bus.NewMessageBus()
	a := &Agent{bus: bs} // multiSession=nil ⇒ 复活必然失败（等价于 one-shot 已结束）
	persisted := 0

	if handled := a.DeliverStructuredMessagePersisting(interactiveReceiptItem(), func() error { persisted++; return nil }); !handled {
		t.Fatal("dead interactive target must be handled (并显式丢弃)，不能回落通用路径")
	}
	if persisted != 1 {
		t.Fatalf("persist called %d times, want 1 (记录不能凭空消失)", persisted)
	}
	if got := drainBusInbound(bs); len(got) != 0 {
		t.Fatalf("dead sub-agent receipt leaked into bus.Inbound (%d 条) ⇒ 会由主 Agent 执行", len(got))
	}
	if q := a.peekStructuredQueue(qualifyChatID("agent", "web:web-4/court.shoufu:main")); q != nil && q.Len() != 0 {
		t.Fatalf("dead sub-agent receipt queued for the generic drain (%d)", q.Len())
	}
}

// 子代理活着且空闲：落库 + 空注入（不内联内容，避免同一回执被表示两遍）。
func TestDeliverStructuredToInteractive_IdleSessionPersistsThenEmptyInjects(t *testing.T) {
	bs := bus.NewMessageBus()
	a := &Agent{bus: bs}
	const key = "web:web-4/court.shoufu:main"
	a.interactiveSubAgents.Store(key, &interactiveAgent{}) // 零值：cfg==nil ⇒ send 立即失败，不会真跑 Run
	persisted := 0

	if handled := a.DeliverStructuredMessagePersisting(interactiveReceiptItem(), func() error { persisted++; return nil }); !handled {
		t.Fatal("live interactive target must be handled by the sub-agent delivery path")
	}
	if persisted != 1 {
		t.Fatalf("idle sub-agent path must persist exactly once (行即唯一副本): %d", persisted)
	}
	if got := drainBusInbound(bs); len(got) != 0 {
		t.Fatalf("live sub-agent receipt leaked into bus.Inbound (%d 条) ⇒ 主 Agent 会抢跑", len(got))
	}
	if q := a.peekStructuredQueue(qualifyChatID("agent", key)); q != nil && q.Len() != 0 {
		t.Fatalf("live sub-agent receipt queued for the generic drain (%d)", q.Len())
	}
}

// 子代理活着且正在跑：内容内联进 pendingMessages（迭代边界注入），且不落库。
func TestDeliverStructuredToInteractive_RunningSessionInlinesContent(t *testing.T) {
	bs := bus.NewMessageBus()
	a := &Agent{bus: bs}
	const key = "web:web-4/court.shoufu:main"
	// cfg 必须非空：SendToInteractiveSession 的 running 分支要求已初始化
	// （cfg==nil 会直接返回「still initializing」，入队不会发生）。
	ia := &interactiveAgent{cfg: &RunConfig{}}
	ia.running = true
	a.interactiveSubAgents.Store(key, ia)
	persisted := 0

	if handled := a.DeliverStructuredMessagePersisting(interactiveReceiptItem(), func() error { persisted++; return nil }); !handled {
		t.Fatal("running interactive target must be handled by the sub-agent delivery path")
	}
	if persisted != 0 {
		t.Fatalf("busy path must NOT persist (内容内联，避免两遍表示): %d", persisted)
	}
	if got := drainBusInbound(bs); len(got) != 0 {
		t.Fatalf("running sub-agent receipt leaked into bus.Inbound (%d 条)", len(got))
	}

	deadline := time.Now().Add(testAsyncWait)
	for {
		ia.mu.Lock()
		n := len(ia.pendingMessages)
		var content string
		if n > 0 {
			content = ia.pendingMessages[0].content
		}
		ia.mu.Unlock()
		if n > 0 {
			if !strings.Contains(content, "用户取消了卡片 #7") {
				t.Fatalf("queued receipt content = %q, want the inline receipt", content)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("receipt was never queued into the sub-agent's pendingMessages")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

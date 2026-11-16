package agent

import (
	"strings"
	"testing"

	"xbot/bus"
)

// 用户报的 P0（2026-11-16）：后台通知在会话 idle 时不立刻变成 user 消息，
// 要等下一次 busy 才投递。
//
// 根因：插队队列只在 Run 的 iteration 边界被 drain（wireBgNotificationDrain）。
// 消息在 busy 期入队后，若该 Run 在下一个边界前结束，残留就无人消费 —— 只能等下一次 Run。
// 修法：会话 idle 时由 drainAndProcessNotifications 把残留 flush 成一条普通 user 消息。
func TestFlushStructuredQueueTurnsResidueIntoUserMessage(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	const ch, chat = "web", "chat_idle"
	key := qualifyChatID(ch, chat)

	// 模拟 busy 期入队的两条结构化消息（busy 分支不落库，内容内联在 item 里）。
	st := &bgSessionState{}
	st.busy.Store(true)
	a.bgSessionStates.Store(key, st)
	if ok := a.DeliverStructuredMessagePersisting(&StructuredQueueItem{
		Channel: ch, ChatID: chat, SenderID: "u1", Kind: "decision",
		Content: "用户处理了卡片 #1（kind=decision）：已批准", Priority: StructuredPriorityHigh,
	}, func() error { t.Fatal("busy path must not persist"); return nil }); !ok {
		t.Fatal("delivery failed")
	}
	a.DeliverStructuredMessagePersisting(&StructuredQueueItem{
		Channel: ch, ChatID: chat, SenderID: "u1", Kind: "ask_user",
		Content: "用户处理了卡片 #2（kind=ask_user）：已忽略", Priority: StructuredPriorityHigh,
	}, nil)
	if got := a.StructuredQueueLen(ch, chat); got != 2 {
		t.Fatalf("queue len = %d, want 2", got)
	}

	// Run 结束：busy 落 false，队列残留必须在 idle drain 时变成 user 消息。
	st.busy.Store(false)
	// 走真实链路：Run 结束后 chatWorker 调用的就是这个 idle drain。
	a.drainAndProcessNotifications(key)
	if got := a.StructuredQueueLen(ch, chat); got != 0 {
		t.Fatalf("queue must be empty after flush, got %d", got)
	}

	select {
	case m := <-a.bus.Inbound:
		if m.Content == "" {
			t.Fatal("flushed message must carry inline content (structured_delivery would drop the row)")
		}
		if m.Metadata != nil && m.Metadata["structured_delivery"] == "true" {
			t.Fatalf("flushed message must be a plain user message, got metadata %+v", m.Metadata)
		}
		if !containsAll(m.Content, "#1", "#2") {
			t.Fatalf("both queued cards must be preserved, got %q", m.Content)
		}
	default:
		t.Fatal("no inbound injected — this is exactly the reported bug (waits for next busy)")
	}
}

// idle 时投递必须直接注入（不进队列），这是既有契约，防止本次修改破坏。
func TestDeliverWhenIdleStillInjectsImmediately(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	const ch, chat = "web", "chat_free"
	persisted := 0
	if ok := a.DeliverStructuredMessagePersisting(&StructuredQueueItem{
		Channel: ch, ChatID: chat, SenderID: "u1", Kind: "decision", Content: "批复：已批准",
		Priority: StructuredPriorityHigh,
	}, func() error { persisted++; return nil }); !ok {
		t.Fatal("delivery failed")
	}
	if persisted != 1 {
		t.Fatalf("idle path must persist once, got %d", persisted)
	}
	if got := a.StructuredQueueLen(ch, chat); got != 0 {
		t.Fatalf("idle delivery must not enqueue, got %d", got)
	}
	select {
	case m := <-a.bus.Inbound:
		if m.Metadata["structured_delivery"] != "true" {
			t.Fatalf("idle injection should keep structured_delivery marker, got %+v", m.Metadata)
		}
	default:
		t.Fatal("idle delivery did not inject")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// flush 之后必须幂等：再调一次 idle drain 不能把同一条批复再投一遍。
func TestFlushStructuredQueueIsIdempotent(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	const ch, chat = "web", "chat_idem"
	key := qualifyChatID(ch, chat)
	st := &bgSessionState{}
	st.busy.Store(true)
	a.bgSessionStates.Store(key, st)
	a.DeliverStructuredMessagePersisting(&StructuredQueueItem{
		Channel: ch, ChatID: chat, SenderID: "u1", Kind: "decision",
		Content: "用户处理了卡片 #9：已批准", Priority: StructuredPriorityHigh,
	}, nil)

	st.busy.Store(false)
	a.drainAndProcessNotifications(key)
	select {
	case <-a.bus.Inbound:
	default:
		t.Fatal("first idle drain should deliver")
	}
	// 第二次 idle drain：队列已空，不能再投。
	a.drainAndProcessNotifications(key)
	select {
	case m := <-a.bus.Inbound:
		t.Fatalf("duplicate delivery after flush: %+v", m)
	default:
	}
}

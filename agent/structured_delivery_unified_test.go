package agent

import (
	"errors"
	"testing"

	"xbot/bus"
)

// 用户指出的设计问题（2026-11-15）：既然有统一投递机制（自动按 idle/busy 分支），
// 就不该让调用方自己判断忙闲、自己落库 —— 两个分支条件不同步就会投两次。
// 现在落库作为回调收进 DeliverStructuredMessagePersisting，由它统一决定。
func TestDeliverStructuredMessagePersisting_IdlePersistsThenEmptyInjects(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	item := &StructuredQueueItem{Channel: "web", ChatID: "chat_idle", SenderID: "u1",
		Content: "用户处理了卡片 #1（kind=ask_user）：已忽略", Kind: "decision", Priority: StructuredPriorityHigh, CorrelationID: "ask-1"}

	persisted := 0
	if ok := a.DeliverStructuredMessagePersisting(item, func() error { persisted++; return nil }); !ok {
		t.Fatal("delivery reported failure for an idle session")
	}
	if persisted != 1 {
		t.Fatalf("persist called %d times, want exactly 1 on the idle path", persisted)
	}
	select {
	case m := <-a.bus.Inbound:
		if m.Content != "" {
			t.Fatalf("idle injection must be EMPTY (the persisted row is the only copy), got %q", m.Content)
		}
		if m.Metadata["structured_delivery"] != "true" {
			t.Fatalf("missing structured_delivery metadata: %+v", m.Metadata)
		}
		if m.Metadata["correlation_id"] != "ask-1" {
			t.Fatalf("correlation lost: %+v", m.Metadata)
		}
	default:
		t.Fatal("no inbound injected on the idle path")
	}
}

func TestDeliverStructuredMessagePersisting_BusyQueuesWithoutPersisting(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	key := qualifyChatID("web", "chat_busy")
	st := &bgSessionState{}
	st.busy.Store(true)
	a.bgSessionStates.Store(key, st)

	item := &StructuredQueueItem{Channel: "web", ChatID: "chat_busy", SenderID: "u1",
		Content: "用户处理了卡片 #2（kind=report）：已批准", Kind: "decision", Priority: StructuredPriorityHigh}

	persisted := 0
	if ok := a.DeliverStructuredMessagePersisting(item, func() error { persisted++; return nil }); !ok {
		t.Fatal("delivery reported failure for a busy session")
	}
	if persisted != 0 {
		t.Fatalf("persist called %d times on the busy path — that is exactly the duplicate the user reported", persisted)
	}
	if q := a.structuredQueue(key); q == nil || q.Len() != 1 {
		t.Fatalf("busy path must enqueue exactly one notice, queue=%v", q)
	}
	select {
	case m := <-a.bus.Inbound:
		t.Fatalf("busy path must NOT inject concurrently (would duplicate at the next replay): %+v", m)
	default:
	}
}

// 落库失败时不能丢消息：退化为携带内容投递。
func TestDeliverStructuredMessagePersisting_PersistFailureCarriesContent(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	item := &StructuredQueueItem{Channel: "web", ChatID: "chat_x", SenderID: "u1",
		Content: "用户处理了卡片 #3：已回答", Kind: "decision", Priority: StructuredPriorityHigh}
	if ok := a.DeliverStructuredMessagePersisting(item, func() error { return errors.New("db down") }); !ok {
		t.Fatal("delivery failed")
	}
	select {
	case m := <-a.bus.Inbound:
		if m.Content != item.Content {
			t.Fatalf("persist failed but content was dropped: %q", m.Content)
		}
	default:
		t.Fatal("persist failed and nothing was delivered")
	}
}

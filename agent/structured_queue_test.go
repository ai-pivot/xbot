package agent

import (
	"fmt"
	"testing"

	"xbot/tools"
)

func TestStructuredQueuePriorityOrder(t *testing.T) {
	q := NewStructuredQueue()
	q.Push(&StructuredQueueItem{ID: "bg", Priority: StructuredPriorityBackground, Content: "bg"})
	q.Push(&StructuredQueueItem{ID: "normal", Priority: StructuredPriorityNormal, Content: "n"})
	q.Push(&StructuredQueueItem{ID: "answer", Priority: StructuredPriorityHigh, Content: "a"})
	q.Push(&StructuredQueueItem{ID: "decision", Priority: StructuredPriorityHigh, Content: "d"})

	got := []string{q.Pop().ID, q.Pop().ID, q.Pop().ID, q.Pop().ID}
	want := []string{"answer", "decision", "normal", "bg"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pop order = %v, want %v", got, want)
		}
	}
	if q.Pop() != nil {
		t.Fatal("empty queue must return nil")
	}
}

// 防饿死：连续 8 条高优先级后，第 9 次取最早入队的普通消息。
func TestStructuredQueueStarvationGuard(t *testing.T) {
	q := NewStructuredQueue()
	q.Push(&StructuredQueueItem{ID: "normal-1", Priority: StructuredPriorityNormal})
	for i := 0; i < 20; i++ {
		q.Push(&StructuredQueueItem{ID: fmt.Sprintf("high-%d", i), Priority: StructuredPriorityHigh})
	}
	for i := 0; i < structuredStarvationLimit; i++ {
		it := q.Pop()
		if it == nil || it.Priority != StructuredPriorityHigh {
			t.Fatalf("pop %d = %+v, want high priority", i, it)
		}
	}
	if got := q.Pop(); got == nil || got.ID != "normal-1" {
		t.Fatalf("pop after starvation = %+v, want normal-1", got)
	}
}

func TestStructuredQueueSamePriorityFIFO(t *testing.T) {
	q := NewStructuredQueue()
	for i := 0; i < 5; i++ {
		q.Push(&StructuredQueueItem{ID: fmt.Sprintf("m%d", i), Priority: StructuredPriorityNormal})
	}
	for i := 0; i < 5; i++ {
		if got := q.Pop(); got == nil || got.ID != fmt.Sprintf("m%d", i) {
			t.Fatalf("FIFO broken at %d: %+v", i, got)
		}
	}
}

func BenchmarkStructuredQueuePush(b *testing.B) {
	q := NewStructuredQueue()
	for i := 0; i < b.N; i++ {
		q.Push(&StructuredQueueItem{ID: "x", Priority: StructuredPriorityHigh})
	}
}

func BenchmarkStructuredQueuePop(b *testing.B) {
	q := NewStructuredQueue()
	for i := 0; i < b.N; i++ {
		q.Push(&StructuredQueueItem{ID: "x", Priority: StructuredPriorityHigh})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if q.Pop() == nil {
			b.Fatal("unexpected empty queue")
		}
	}
}

func BenchmarkStructuredQueuePushPop(b *testing.B) {
	q := NewStructuredQueue()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.Push(&StructuredQueueItem{ID: "x", Priority: StructuredPriorityNormal})
		_ = q.Pop()
	}
}

// 真实接线：wireBgNotificationDrain 在 iteration 边界把插队队列转成注入通知，
// 且排在同会话的后台通知之前。
func TestWireBgNotificationDrainDrainsStructuredQueue(t *testing.T) {
	a := &Agent{bgRunPending: map[string][]tools.BgNotification{}}
	key := "web:web-1"
	a.structuredQueue(key).Push(&StructuredQueueItem{
		ID: "a1", Channel: "web", ChatID: "web-1", Kind: "answer",
		Content: "q1:approve", Priority: StructuredPriorityHigh,
	})
	a.bgRunPending[key] = []tools.BgNotification{&tools.AsyncMessageNotification{Key: key, Content: "bg"}}

	notifs := a.wireBgNotificationDrain(key)()
	if len(notifs) != 2 {
		t.Fatalf("drained %d notifications, want 2", len(notifs))
	}
	if _, ok := notifs[0].(*tools.StructuredNotice); !ok {
		t.Fatalf("first notification = %T, want *tools.StructuredNotice (priority lane first)", notifs[0])
	}
	if a.StructuredQueueLen("web", "web-1") != 0 {
		t.Fatal("structured queue not drained")
	}
}

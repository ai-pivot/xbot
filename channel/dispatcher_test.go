package channel

import (
	"testing"
	"time"

	"xbot/bus"
)

// TestDispatcher_ForwardsTurnID 验证 final reply 的 turn_id 从 bus.OutboundMessage
// 端到端传递到 channel.OutboundMsg —— 这是之前 turn_id 丢失（空 DOM / assistant
// 重复）bug 的修复点：bus.OutboundMessage 曾缺 TurnID 字段，Dispatcher 也未传递，
// 导致 WSMessage 顶层 turn_id 被 omitempty 省略。
func TestDispatcher_ForwardsTurnID(t *testing.T) {
	msgBus := bus.NewMessageBus()
	d := NewDispatcher(msgBus)
	mc := NewMockChannel("web", msgBus)
	d.Register(mc)
	go d.Run()
	defer d.Stop()

	msgBus.Outbound <- bus.OutboundMessage{
		Channel: "web",
		ChatID:  "chat-1",
		Content: "hello",
		TurnID:  1366,
	}

	if !mc.WaitForOutbound(time.Second) {
		t.Fatal("no outbound message received")
	}
	out := mc.LastOutbound()
	if out == nil {
		t.Fatal("LastOutbound returned nil")
	}
	if out.TurnID != 1366 {
		t.Fatalf("OutboundMsg.TurnID = %d, want 1366", out.TurnID)
	}
}

// TestDispatcher_ForwardsTurnIDZero 验证无 turn_id（工具中途发送等场景）时
// 传递 0，不伪造。
func TestDispatcher_ForwardsTurnIDZero(t *testing.T) {
	msgBus := bus.NewMessageBus()
	d := NewDispatcher(msgBus)
	mc := NewMockChannel("web", msgBus)
	d.Register(mc)
	go d.Run()
	defer d.Stop()

	msgBus.Outbound <- bus.OutboundMessage{
		Channel: "web",
		ChatID:  "chat-1",
		Content: "tool output",
	}

	if !mc.WaitForOutbound(time.Second) {
		t.Fatal("no outbound message received")
	}
	if out := mc.LastOutbound(); out == nil || out.TurnID != 0 {
		t.Fatalf("OutboundMsg.TurnID = %v, want 0", out)
	}
}

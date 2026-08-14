package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"xbot/bus"
	"xbot/channel"
	"xbot/protocol"
)

// TestStreamContentFullPush_Default 验证 deltaPush 默认关闭时 streamContentFunc
// 总是推送完整累积文本（全量 StreamContent，不发 StreamDelta）——还原全量算法，
// 避免 delta push 的 gap 追赶/分类不一致问题。
func TestStreamContentFullPush_Default(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockCh := &mockProgressChannel2{}
	a := &Agent{
		bus:          bus.NewMessageBus(),
		agentCtx:     ctx,
		deltaPush:    false, // 默认：全量
		channelRange: func(fn func(string, channel.Channel) bool) { fn("mock", mockCh) },
		channelFinder: func(ch string) (channel.Channel, bool) {
			if ch == "mock" {
				return mockCh, true
			}
			return nil, false
		},
	}
	var seq atomic.Uint64
	contentFunc, reasoningFunc, _, _ := a.buildStreamCallbacks("chat-1", "mock", &seq, 1, "mock:chat-1", 0)

	// 两次调用，第二次是第一次的前缀扩展 —— 全量模式下必须都发全量
	contentFunc("Hello")
	contentFunc("Hello World")
	reasoningFunc("think1")
	reasoningFunc("think1 deeper")

	events := mockCh.getEvents()
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}
	// 内容事件：必须是全量（StreamContent 非空），绝不能是 delta
	if events[0].StreamContent != "Hello" || events[0].StreamDelta != "" {
		t.Errorf("event0: StreamContent=%q StreamDelta=%q, want full push", events[0].StreamContent, events[0].StreamDelta)
	}
	if events[1].StreamContent != "Hello World" || events[1].StreamDelta != "" {
		t.Errorf("event1: StreamContent=%q StreamDelta=%q, want full push (prefix expansion must NOT become delta)", events[1].StreamContent, events[1].StreamDelta)
	}
	if events[2].ReasoningStreamContent != "think1" || events[2].ReasoningStreamDelta != "" {
		t.Errorf("event2: ReasoningStreamContent=%q delta=%q, want full push", events[2].ReasoningStreamContent, events[2].ReasoningStreamDelta)
	}
	if events[3].ReasoningStreamContent != "think1 deeper" || events[3].ReasoningStreamDelta != "" {
		t.Errorf("event3: ReasoningStreamContent=%q delta=%q, want full push", events[3].ReasoningStreamContent, events[3].ReasoningStreamDelta)
	}
}

type mockProgressChannel2 struct {
	mu     sync.Mutex
	events []*protocol.ProgressEvent
}

func (m *mockProgressChannel2) SendProgress(_ string, payload *protocol.ProgressEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, payload)
}
func (m *mockProgressChannel2) SendStreamContent(_, _, _ string)         {}
func (m *mockProgressChannel2) Send(channel.OutboundMsg) (string, error) { return "", nil }
func (m *mockProgressChannel2) Name() string                             { return "mock" }
func (m *mockProgressChannel2) Start() error                             { return nil }
func (m *mockProgressChannel2) Stop()                                    {}
func (m *mockProgressChannel2) getEvents() []*protocol.ProgressEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]*protocol.ProgressEvent, len(m.events))
	copy(cp, m.events)
	return cp
}

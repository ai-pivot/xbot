package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"xbot/bus"
	"xbot/channel"
	"xbot/protocol"
)

// mockNonProgressChannel simulates an originating channel that does NOT
// implement channel.ProgressSender (feishu / qq / napcat). Its Send/SendStreamContent
// are not used by buildStreamCallbacks — only the channelFinder resolution matters.
type mockNonProgressChannel struct{}

func (m *mockNonProgressChannel) Send(channel.OutboundMsg) (string, error) { return "", nil }
func (m *mockNonProgressChannel) Name() string                             { return "feishu" }
func (m *mockNonProgressChannel) Start() error                             { return nil }
func (m *mockNonProgressChannel) Stop()                                    {}

// TestStreamCallbacks_FallbackFanout_NonProgressSenderOrigin reproduces the
// channel-session live-stream loss on web: buildStreamCallbacks resolved ONLY
// the originating channel as the stream sender. When the originating channel
// is not a channel.ProgressSender (feishu/qq — they implement PreReplyNotifier
// instead), sender stayed nil and broadcastProgress dropped EVERY stream event
// (content/reasoning/tool/usage) — a web user viewing that channel's session
// saw no typewriter stream, no generating tools, and no live tkps (only the
// 15s heartbeat snapshot + iteration-boundary structured events).
//
// The fix: when the originating channel is not a ProgressSender, fall back to
// the channelRange fan-out over ALL registered ProgressSenders (web/cli/plugins)
// — the same contract as buildProgressEventHandler / emitTurnStarted.
// WebChannel.SendProgress derives its route from payload.ChatID (the qualified
// progressKey "feishu:chatID"), so the event reaches exactly that session's
// subscribers — no duplicate delivery for cli/web-originated turns (those keep
// the single-sender fast path).
func TestStreamCallbacks_FallbackFanout_NonProgressSenderOrigin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	feishuCh := &mockNonProgressChannel{} // originating channel: NOT a ProgressSender
	webCh := &mockProgressChannel2{}      // viewer channel: ProgressSender (web)

	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
		channelRange: func(fn func(string, channel.Channel) bool) {
			// Registered channels: the originating feishu channel + the web
			// channel viewing it (the web user opened the feishu session).
			if !fn("feishu", feishuCh) {
				return
			}
			fn("web", webCh)
		},
		channelFinder: func(ch string) (channel.Channel, bool) {
			if ch == "feishu" {
				return feishuCh, true
			}
			if ch == "web" {
				return webCh, true
			}
			return nil, false
		},
	}

	var seq atomic.Uint64
	contentFunc, reasoningFunc, _, _, _ := a.buildStreamCallbacks("chat-1", "feishu", &seq, 1, "feishu:chat-1", 0)

	contentFunc("Hello")
	reasoningFunc("thinking...")

	events := webCh.getEvents()
	if len(events) != 2 {
		t.Fatalf("expected 2 stream events (content+reasoning) delivered via fallback fan-out, got %d — feishu-session live stream is silently dropped when the originating channel is not a ProgressSender", len(events))
	}
	if events[0].StreamContent != "Hello" {
		t.Errorf("event0 StreamContent = %q, want %q", events[0].StreamContent, "Hello")
	}
	if events[0].ChatID != "feishu:chat-1" {
		t.Errorf("event0 ChatID = %q, want qualified progressKey %q (WebChannel routes on this field)", events[0].ChatID, "feishu:chat-1")
	}
	if events[1].ReasoningStreamContent != "thinking..." {
		t.Errorf("event1 ReasoningStreamContent = %q, want %q", events[1].ReasoningStreamContent, "thinking...")
	}
	for i, ev := range events {
		if ev.TurnID != 1 {
			t.Errorf("event%d TurnID = %d, want 1 (stream events must carry the turn_id)", i, ev.TurnID)
		}
	}
}

// TestStreamCallbacks_SingleSenderFastPath_Unchanged guards the fast path:
// when the originating channel IS a ProgressSender (cli/web), the stream goes
// to that sender ONLY — the fan-out fallback must not introduce duplicate
// delivery for the normal case (historical "Web+TUI double stream" bug class).
func TestStreamCallbacks_SingleSenderFastPath_Unchanged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cliCh := &mockProgressChannel2{} // originating: ProgressSender (cli)
	webCh := &mockProgressChannel2{} // another viewer channel

	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
		channelRange: func(fn func(string, channel.Channel) bool) {
			if !fn("cli", cliCh) {
				return
			}
			fn("web", webCh)
		},
		channelFinder: func(ch string) (channel.Channel, bool) {
			if ch == "cli" {
				return cliCh, true
			}
			if ch == "web" {
				return webCh, true
			}
			return nil, false
		},
	}

	var seq atomic.Uint64
	contentFunc, _, _, _, _ := a.buildStreamCallbacks("chat-1", "cli", &seq, 1, "cli:chat-1", 0)

	contentFunc("Hello")

	if got := len(cliCh.getEvents()); got != 1 {
		t.Errorf("originating cli channel: expected 1 event, got %d", got)
	}
	if got := len(webCh.getEvents()); got != 0 {
		t.Errorf("other channel must NOT receive a duplicate stream event in the single-sender fast path (its own SendProgress Hub broadcast covers it), got %d", got)
	}
}

// compile-time guards: the mocks satisfy the intended interfaces.
var (
	_ channel.Channel        = (*mockNonProgressChannel)(nil)
	_ channel.ProgressSender = (*mockProgressChannel2)(nil)
	_ protocol.ProgressEvent
)

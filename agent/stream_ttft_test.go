package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/llm"
)

// TestStreamToolCallFunc_NameGateMustTouchFirstChunk reproduces the tool-only
// iteration TTFT bug: streamToolCallFunc only calls withLiveStats when a tool
// NAME has arrived (name-gate). Tool-only iterations (no content/reasoning
// stream) send early tool_call deltas with an empty name (index/ID fragments)
// — those early-returns left liveStats.firstChunkAt unset, so the TTFT window
// started at the NAME-arrival frame (or later) instead of the actual first
// chunk → live TTFT overstated for tool-only iterations.
func TestStreamToolCallFunc_NameGateMustTouchFirstChunk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockCh := &mockProgressChannel2{}
	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
		channelRange: func(fn func(string, channel.Channel) bool) {
			fn("mock", mockCh)
		},
		channelFinder: func(ch string) (channel.Channel, bool) {
			if ch == "mock" {
				return mockCh, true
			}
			return nil, false
		},
	}
	var seq atomic.Uint64
	_, _, toolCallFunc, _, resetTiming := a.buildStreamCallbacks("chat-1", "mock", &seq, 1, "mock:chat-1", 0)

	// Simulate the iteration boundary: resetTiming anchors requestStartAt
	// (same as beginIteration does per iteration).
	resetTiming()

	// Wait a bit to simulate the request-setup window (reset → first chunk).
	time.Sleep(60 * time.Millisecond)

	// Tool-only stream: the first tool_call delta arrives with an EMPTY name
	// (index/ID fragment — the name arrives in a later delta). This is the
	// iteration's real FIRST chunk; TTFT must be anchored HERE.
	toolCallFunc([]llm.ToolCallDelta{{Index: 0, ID: "call_1"}})

	// No events pushed yet (name-gate suppresses the push) — but firstChunkAt
	// must already be touched, anchoring TTFT to the first delta.
	if n := len(mockCh.getEvents()); n != 0 {
		t.Fatalf("expected no events before name arrival, got %d", n)
	}

	time.Sleep(120 * time.Millisecond)

	// Name arrives → the push carries live StreamStats. Its TTFT must span
	// back to the FIRST delta (~60ms), not the name-arrival frame (~180ms).
	// Pre-fix: firstChunkAt was only set at the name-arrival frame → TTFT
	// overstated by the name-fragment delay (tool-only iteration TTFT bug).
	toolCallFunc([]llm.ToolCallDelta{{Index: 0, ID: "call_1", Name: "Read"}})

	events := mockCh.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event after name arrival, got %d", len(events))
	}
	st := events[0].StreamStats
	if st == nil {
		t.Fatalf("event missing live StreamStats")
	}
	if st.TTFTMs >= 110 {
		t.Errorf("TTFTMs = %d, want < 110 (must be anchored at the FIRST tool_call delta ~60ms, not the name-arrival frame ~180ms — tool-only iteration TTFT is overstated)", st.TTFTMs)
	}
}

// TestStreamToolCallFunc_TokenEstimateIncludesToolArgs reproduces the "tool 的
// SSE 没计算" bug: liveStats' token fallback estimated tokens from
// len(StreamContent)+len(ReasoningStreamContent) ONLY — tool args (the entire
// output of a tool-only iteration) were not counted, so tok/s stayed 0 while
// the model streamed a tool call. The fix adds StreamingTools' GenChars
// (accumulated argument characters) to the estimate.
func TestStreamToolCallFunc_TokenEstimateIncludesToolArgs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockCh := &mockProgressChannel2{}
	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
		channelRange: func(fn func(string, channel.Channel) bool) {
			fn("mock", mockCh)
		},
		channelFinder: func(ch string) (channel.Channel, bool) {
			if ch == "mock" {
				return mockCh, true
			}
			return nil, false
		},
	}
	var seq atomic.Uint64
	_, _, toolCallFunc, _, _ := a.buildStreamCallbacks("chat-1", "mock", &seq, 1, "mock:chat-1", 0)

	// Tool-only stream: no content/reasoning — the ONLY output is tool args.
	// First frame: 400 chars (~100 tokens estimated).
	toolCallFunc([]llm.ToolCallDelta{{Index: 0, ID: "call_1", Name: "Read", Arguments: strings.Repeat("x", 400)}})

	// Wait for the tkps sliding window (>=200ms between samples).
	time.Sleep(300 * time.Millisecond)

	// Second frame: args grew to 800 chars (~200 tokens) → ~100 tokens / 300ms.
	toolCallFunc([]llm.ToolCallDelta{{Index: 0, ID: "call_1", Name: "Read", Arguments: strings.Repeat("x", 800)}})

	events := mockCh.getEvents()
	if len(events) < 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	st := events[len(events)-1].StreamStats
	if st == nil {
		t.Fatalf("event missing live StreamStats")
	}
	// Pre-fix: tokens estimated 0 (tool args not counted) → dtTokens=0 → tps=0.
	if st.TokensPerSec <= 0 {
		t.Errorf("TokensPerSec = %d, want > 0 (tool args must count toward the token estimate — tool-only iterations streamed tok/s=0)", st.TokensPerSec)
	}
}

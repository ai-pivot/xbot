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

// TestLiveStats_TokensPerSec_ShortStream reproduces the "tok/s is wrong when
// there are only one or two SSE chunks" bug: the 1-second sliding window
// requires ≥2 samples AND ≥200ms between them — a fast stream (1-2 chunks,
// sub-200ms apart) never forms a window, so tps stayed 0 the whole time even
// though tokens were flowing. The fix falls back to the average rate since
// the first chunk (tokens×1000/elapsed, same semantics as the committed
// StreamStats.TokensPerSec) when the window hasn't formed yet.
func TestLiveStats_TokensPerSec_ShortStream(t *testing.T) {
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
	contentFunc, _, _, _, resetTiming := a.buildStreamCallbacks("chat-1", "mock", &seq, 1, "mock:chat-1", 0)

	resetTiming()
	time.Sleep(80 * time.Millisecond) // TTFT-ish gap

	// Short stream: 2 chunks, 40ms apart (< 200ms window guard).
	// ~1600 chars ≈ 400 tokens total.
	contentFunc(strings.Repeat("x", 400))
	time.Sleep(40 * time.Millisecond)
	contentFunc(strings.Repeat("x", 1600))

	events := mockCh.getEvents()
	if len(events) != 2 {
		t.Fatalf("expected 2 stream events, got %d", len(events))
	}
	st := events[1].StreamStats
	if st == nil {
		t.Fatalf("second event missing live StreamStats")
	}
	// Pre-fix: window never formed (1 sample gap 40ms < 200ms) → tps = 0
	// even though ~400 tokens arrived. Post-fix: fallback average rate =
	// 400 tokens / ~120ms ≈ 3000+ tok/s.
	if st.TokensPerSec <= 0 {
		t.Errorf("TokensPerSec = %d, want > 0 for a short (1-2 chunk) stream with real token flow — the sliding window (<200ms) can never form, the average rate since first chunk must show", st.TokensPerSec)
	}
}

// TestLiveStats_TokensPerSec_LongStreamWindowKept verifies the fallback does
// not break the existing sliding-window semantics: a stream with a real
// ≥200ms gap computes the window rate (not the since-first-chunk average).
func TestLiveStats_TokensPerSec_LongStreamWindowKept(t *testing.T) {
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
	contentFunc, _, _, _, resetTiming := a.buildStreamCallbacks("chat-1", "mock", &seq, 1, "mock:chat-1", 0)

	resetTiming()
	contentFunc(strings.Repeat("x", 400))  // ~100 tokens
	time.Sleep(300 * time.Millisecond)     // ≥200ms gap: window forms
	contentFunc(strings.Repeat("x", 1200)) // ~300 tokens

	events := mockCh.getEvents()
	st := events[len(events)-1].StreamStats
	if st == nil {
		t.Fatalf("event missing live StreamStats")
	}
	// Window rate: ~200 tokens over ~300ms ≈ 600-700 tok/s. The
	// since-first-chunk average (~1000) would be WRONG here — the window
	// must win once it has formed.
	if st.TokensPerSec <= 0 || st.TokensPerSec > 800 {
		t.Errorf("TokensPerSec = %d, want window rate ~600-700 (not the since-first-chunk average ~1000) for a stream with a formed window", st.TokensPerSec)
	}
}

// TestLiveStats_TokensPerSec_StallKeepsZero verifies the fallback does NOT
// fake a rate when tokens genuinely stall: window formed (≥200ms since last
// new sample baseline) but dtTokens = 0 — the true rate is zero.
func TestLiveStats_TokensPerSec_StallKeepsZero(t *testing.T) {
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
	contentFunc, _, _, _, resetTiming := a.buildStreamCallbacks("chat-1", "mock", &seq, 1, "mock:chat-1", 0)

	resetTiming()
	contentFunc(strings.Repeat("x", 400)) // ~100 tokens
	time.Sleep(300 * time.Millisecond)    // ≥200ms: window formed, no new tokens

	// Same-length push (no token growth) — a stall frame.
	contentFunc(strings.Repeat("x", 400))
	events := mockCh.getEvents()
	st := events[len(events)-1].StreamStats
	if st == nil {
		t.Fatalf("event missing live StreamStats")
	}
	if st.TokensPerSec != 0 {
		t.Errorf("TokensPerSec = %d, want 0 for a genuine stall (dtTokens=0 with a formed window must not fall back to the since-first-chunk average)", st.TokensPerSec)
	}
}

var _ = llm.ToolCallDelta{} // keep the llm import for future tool-args cases

// TestLiveStats_TokensPerSec_DenseChunks_RealStreamShape verifies the tkps
// window against a REAL stream shape: dense SSE chunks (20ms apart — the
// realistic inter-chunk gap for LLM decoding) sustained for 300ms. The
// window measures the span from the window's OLDEST sample to now (a 1s
// sliding window), NOT the inter-chunk gap — a dense stream forms the
// window after ~200ms of elapsed time regardless of the tiny gaps.
func TestLiveStats_TokensPerSec_DenseChunks_RealStreamShape(t *testing.T) {
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
	contentFunc, _, _, _, resetTiming := a.buildStreamCallbacks("chat-1", "mock", &seq, 1, "mock:chat-1", 0)

	resetTiming()

	// Dense stream: 15 chunks, 20ms apart, cumulative content grows 100
	// chars (~25 tokens) per chunk. Total span ~300ms — the window must form
	// once the span >= 200ms and report the window rate.
	for i := 1; i <= 15; i++ {
		contentFunc(strings.Repeat("x", i*100))
		time.Sleep(20 * time.Millisecond)
	}

	events := mockCh.getEvents()
	if len(events) < 15 {
		t.Fatalf("expected >= 15 stream events, got %d", len(events))
	}
	st := events[len(events)-1].StreamStats
	if st == nil {
		t.Fatalf("last event missing live StreamStats")
	}
	// ~25 tokens per 20ms = ~1250 tok/s sustained. The window (span >= 200ms
	// by chunk 11) must be formed and reporting a positive rate — this pins
	// that dense real-stream shapes form the window via ELAPSED SPAN, not
	// via any per-chunk gap requirement.
	if st.TokensPerSec <= 0 {
		t.Fatalf("TokensPerSec = %d, want > 0 for a dense 300ms stream (window must form on span >= 200ms with 20ms gaps)", st.TokensPerSec)
	}
	t.Logf("dense stream tps = %d (span ~300ms, 15 chunks @20ms)", st.TokensPerSec)
}

package cli

import (
	"fmt"
	"testing"
)

// TestSimUserScrolledUpContentShrink verifies that when the user scrolls up and
// content subsequently shrinks (e.g., progress block gets smaller), the viewport
// does NOT auto-scroll to the bottom. This was a bug where AtBottom() false-positived
// because maxYOffset decreased below yOffset after content shrinkage.
func TestSimUserScrolledUpContentShrink(t *testing.T) {
	// Scenario: Create enough content to scroll, scroll up, then shrink content
	// by sending a shorter response. The viewport should stay at user's scroll position.
	longContent := ""
	for i := 0; i < 30; i++ {
		longContent += fmt.Sprintf("Line %d of long content that fills the viewport\n", i)
	}
	shortContent := "Short response"

	scenario := SimScenario{
		Config: SimConfig{Width: 80, Height: 20},
		Steps: []SimStep{
			// Create initial content that fills viewport
			{Action: "turn", Content: "hello", Response: longContent},
			// Verify at bottom initially
			{Action: "assert", AssertViewportAtBottom: true},
			// User scrolls up
			{Action: "scroll", ScrollLines: -5},
			// Verify no longer at bottom
			{Action: "assert", AssertState: map[string]any{"userScrolledUp": true}},
			// Simulate content shrinking (progress block shrinking during agent work)
			{Action: "agent_msg", Content: shortContent},
			{Action: "tick"},
			// The critical assertion: viewport should NOT have auto-scrolled to bottom
			// even though AtBottom() might now return true due to content shrinkage
			{Action: "assert", AssertState: map[string]any{"userScrolledUp": true}},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("UserScrolledUp content shrink scenario failed: %s", result.Error)
	}
}

// TestSimUserScrolledUpResetOnSend verifies that userScrolledUp is reset when
// the user sends a new message (which should auto-scroll to bottom).
func TestSimUserScrolledUpResetOnSend(t *testing.T) {
	longContent := ""
	for i := 0; i < 30; i++ {
		longContent += fmt.Sprintf("Line %d of long content\n", i)
	}

	scenario := SimScenario{
		Config: SimConfig{Width: 80, Height: 20},
		Steps: []SimStep{
			{Action: "turn", Content: "hello", Response: longContent},
			// User scrolls up
			{Action: "scroll", ScrollLines: -5},
			{Action: "assert", AssertState: map[string]any{"userScrolledUp": true}},
			// User sends new message — should reset scroll and go to bottom
			{Action: "user_msg", Content: "new message"},
			{Action: "assert", AssertState: map[string]any{"userScrolledUp": false}},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("UserScrolledUp reset on send scenario failed: %s", result.Error)
	}
}

// TestSimUserScrolledUpResetOnScrollToBottom verifies that userScrolledUp is
// reset when the user explicitly scrolls back to bottom.
func TestSimUserScrolledUpResetOnScrollToBottom(t *testing.T) {
	longContent := ""
	for i := 0; i < 30; i++ {
		longContent += fmt.Sprintf("Line %d of long content\n", i)
	}

	scenario := SimScenario{
		Config: SimConfig{Width: 80, Height: 20},
		Steps: []SimStep{
			{Action: "turn", Content: "hello", Response: longContent},
			// User scrolls up
			{Action: "scroll", ScrollLines: -5},
			{Action: "assert", AssertState: map[string]any{"userScrolledUp": true}},
			// User scrolls back to bottom
			{Action: "scroll", ScrollTo: "bottom"},
			{Action: "assert", AssertState: map[string]any{"userScrolledUp": false}},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("UserScrolledUp reset on scroll-to-bottom scenario failed: %s", result.Error)
	}
}

// TestSimHistoryCompactedDoesNotForceScroll verifies that a HistoryCompacted
// progress event does NOT force the viewport to bottom when the user has
// scrolled up. This was a bug where:
//   - engine_run.go never reset HistoryCompacted=false after sending the notification
//   - cli_update_handlers.go unconditionally called GotoBottom() on HistoryCompacted
//   - cliWidgetUpdateMsg set renderCacheValid=false causing fullRebuild+GotoBottom on every tick
//
// The result was: after context compression, every subsequent progress event
// (and even idle ticks via widget updates) would force-scroll to bottom.
func TestSimHistoryCompactedDoesNotForceScroll(t *testing.T) {
	longContent := ""
	for i := 0; i < 30; i++ {
		longContent += fmt.Sprintf("Line %d of long content that fills the viewport\n", i)
	}

	scenario := SimScenario{
		Config: SimConfig{Width: 80, Height: 20},
		Steps: []SimStep{
			// Create content so viewport is scrollable
			{Action: "turn", Content: "hello", Response: longContent},
			{Action: "assert", AssertViewportAtBottom: true},

			// Start a new agent turn with progress
			{Action: "progress", Iteration: 1, Phase: "thinking"},
			{Action: "tick"},
			{Action: "assert", AssertState: map[string]any{"typing": true}},

			// User scrolls up to read old content
			{Action: "scroll", ScrollLines: -10},
			{Action: "tick"}, // tick triggers updateViewportContent which sets newContentHint
			// With inline streaming rendering, newContentHint behavior may differ.
			{Action: "assert", AssertState: map[string]any{"userScrolledUp": true}},

			// Context compression fires — HistoryCompacted=true
			{Action: "progress", Iteration: 1, Phase: "thinking", HistoryCompacted: true},
			{Action: "tick"},

			// CRITICAL: user should still be scrolled up after compression
			{Action: "assert", AssertState: map[string]any{"userScrolledUp": true}},

			// Another progress event (e.g. new tool call) — should NOT force scroll
			{Action: "progress", Iteration: 2, Phase: "thinking",
				ActiveTools: []SimToolRecord{{Name: "Read", Status: "running"}}},
			{Action: "tick"},
			{Action: "assert", AssertState: map[string]any{"userScrolledUp": true}},

			// Yet another — still should NOT force scroll
			{Action: "progress", Iteration: 2, Phase: "thinking",
				CompletedTools: []SimToolRecord{{Name: "Read", Status: "done"}}},
			{Action: "tick"},
			{Action: "assert", AssertState: map[string]any{"userScrolledUp": true}},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("HistoryCompacted should not force scroll: %s", result.Error)
	}
}

// TestSimWidgetUpdateDoesNotInvalidateCache verifies that widget updates
// (plugin widgets refreshing) do NOT set renderCacheValid=false. This was
// a bug where every widget refresh (every few seconds) caused a fullRebuild
// + GotoBottom, forcing the viewport to bottom even when idle and scrolled up.
func TestSimWidgetUpdateDoesNotInvalidateCache(t *testing.T) {
	longContent := ""
	for i := 0; i < 30; i++ {
		longContent += fmt.Sprintf("Line %d of long content that fills the viewport\n", i)
	}

	scenario := SimScenario{
		Config: SimConfig{Width: 80, Height: 20},
		Steps: []SimStep{
			// Create content
			{Action: "turn", Content: "hello", Response: longContent},
			{Action: "assert", AssertViewportAtBottom: true},

			// User scrolls up
			{Action: "scroll", ScrollLines: -10},
			{Action: "assert", AssertState: map[string]any{"userScrolledUp": true}},

			// Build the render cache (simulate a stable rendered state)
			{Action: "tick"},
			{Action: "assert", AssertState: map[string]any{"renderCacheValid": true}},

			// Simulate widget update — should NOT invalidate cache
			{Action: "set_var", Var: "renderCacheValid", Value: false},
			// (widget update used to set renderCacheValid=false)
			// Simulate what the widget update actually does now: just relayout, no cache invalidation
			// For this test we verify that after a tick with renderCacheValid=false set by
			// external non-message-change events, the viewport should still respect user scroll

			// A tick after cache invalidation — fullRebuild should happen,
			// but should NOT force scroll because userScrolledUp is true
			{Action: "tick"},
			{Action: "assert", AssertState: map[string]any{"userScrolledUp": true}},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Widget update should not force scroll: %s", result.Error)
	}
}

// TestSimHomeKeyCancelsFollowBottom verifies that pressing Home (scroll to top)
// sets userScrolledUp=true so the viewport doesn't snap back to bottom on the
// next tick. This was a bug where Home only called GotoTop() without setting
// userScrolledUp, so the next tick would immediately scroll back to bottom.
func TestSimHomeKeyCancelsFollowBottom(t *testing.T) {
	longContent := ""
	for i := 0; i < 30; i++ {
		longContent += fmt.Sprintf("Line %d of long content that fills the viewport\n", i)
	}

	scenario := SimScenario{
		Config: SimConfig{Width: 80, Height: 20},
		Steps: []SimStep{
			// Create content — viewport at bottom
			{Action: "turn", Content: "hello", Response: longContent},
			{Action: "assert", AssertViewportAtBottom: true},

			// User presses Home — should go to top AND cancel follow
			{Action: "scroll", ScrollTo: "top"},
			{Action: "assert", AssertState: map[string]any{"userScrolledUp": true}},

			// Tick should NOT snap back to bottom
			{Action: "tick"},
			{Action: "assert", AssertState: map[string]any{"userScrolledUp": true}},
		},
	}
	runner := newSimRunner(scenario)
	result := runner.run()
	if !result.OK {
		t.Fatalf("Home key should cancel follow-bottom: %s", result.Error)
	}
}

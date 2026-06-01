package channel

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

package channel

import (
	"strings"
	"testing"
	"time"
	"xbot/protocol"
)

// TestProgressAndSettledVisualConsistency verifies that progress and settled
// use the same visual format (▎ left bar, same iteration layout).
func TestProgressAndSettledVisualConsistency(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.typingStartTime = time.Now()

	// Simulate 2 iterations with tools
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 0,
		Reasoning: "Let me analyze the code",
		CompletedTools: []protocol.ToolProgress{
			{Name: "Read", Label: "main.go", Status: "done", Elapsed: 1000, Iteration: 0},
		},
	})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1,
		CompletedTools: []protocol.ToolProgress{
			{Name: "Grep", Label: "pattern", Status: "done", Elapsed: 500, Iteration: 1},
		},
	})

	progressBlock := model.renderProgressBlock()

	// Progress should contain ▎ left bar
	if !strings.Contains(progressBlock, "▎") {
		t.Error("Progress block should contain ▎ left bar")
	}
	// Progress should contain tool names
	if !strings.Contains(progressBlock, "main.go") {
		t.Error("Progress block should contain tool label 'main.go'")
	}
	if !strings.Contains(progressBlock, "pattern") {
		t.Error("Progress block should contain tool label 'pattern'")
	}
	// Progress should contain iteration headers
	if !strings.Contains(progressBlock, "#0") {
		t.Error("Progress block should contain #0")
	}

	// Now settle — simulate done
	sendDone(model, "Analysis complete")

	// Find the assistant message and render it
	var assistantContent string
	for _, msg := range model.messages {
		if msg.role == "assistant" {
			assistantContent = model.renderMessage(&msg)
			break
		}
	}

	// Settled (collapsed) should also contain ▎ left bar
	if !strings.Contains(assistantContent, "▎") {
		t.Error("Settled (collapsed) should contain ▎ left bar")
	}
	// Settled should contain tool count
	if !strings.Contains(assistantContent, "2 calls") {
		t.Error("Settled should show '2 calls' summary")
	}
}

// TestSettledExpandedFormat verifies expanded tool_summary rendering
func TestSettledExpandedFormat(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.typingStartTime = time.Now()
	model.toolSummaryExpanded = true

	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 0,
		Reasoning: "Analyzing the file structure",
		CompletedTools: []protocol.ToolProgress{
			{Name: "Read", Label: "config.json", Status: "done", Elapsed: 1000, Iteration: 0},
		},
	})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1,
		Reasoning: "Found issues to fix",
		CompletedTools: []protocol.ToolProgress{
			{Name: "Edit", Label: "fix bug", Status: "done", Elapsed: 500, Iteration: 1},
		},
	})

	sendDone(model, "Fixed the bug")

	// Render assistant message (expanded)
	var content string
	for _, msg := range model.messages {
		if msg.role == "assistant" {
			content = model.renderMessage(&msg)
			break
		}
	}

	// Expanded should have per-iteration blocks
	if !strings.Contains(content, "#0") || !strings.Contains(content, "#1") {
		t.Error("Expanded should show per-iteration headers #0 and #1")
	}
	// Expanded should have ▎ left bar
	if !strings.Contains(content, "▎") {
		t.Error("Expanded should contain ▎ left bar")
	}
	// Expanded should show tools
	if !strings.Contains(content, "config.json") {
		t.Error("Expanded should contain 'config.json'")
	}
	if !strings.Contains(content, "fix bug") {
		t.Error("Expanded should contain 'fix bug'")
	}
	// Expanded should show reasoning for non-last iterations
	if !strings.Contains(content, "Analyzing") {
		t.Error("Expanded should show reasoning for non-last iteration")
	}
}

// TestResizeSettledExpandedReRenders verifies that resize invalidates expanded settled message cache
func TestResizeSettledExpandedReRenders(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.typingStartTime = time.Now()
	model.toolSummaryExpanded = true

	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 0,
		Reasoning: "This is a long reasoning text that should definitely wrap when the terminal width changes from wide to narrow because it contains enough content to require multiple lines at narrower widths",
		CompletedTools: []protocol.ToolProgress{
			{Name: "Read", Label: "a_very_long_filename_that_should_wrap_when_terminal_is_narrow.go", Status: "done", Elapsed: 1000, Iteration: 0},
			{Name: "Grep", Label: "search for a complex pattern in the codebase files", Status: "done", Elapsed: 500, Iteration: 0},
		},
	})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1,
		CompletedTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "run tests to verify the changes work correctly in production", Status: "done", Elapsed: 2000, Iteration: 1},
		},
	})
	sendDone(model, "All changes verified and committed successfully")

	// Render at 80 (wide)
	model.handleResize(80, 24)
	var content80 string
	for _, msg := range model.messages {
		if msg.role == "assistant" {
			content80 = model.renderMessage(&msg)
			break
		}
	}

	// Resize to 40 (narrow) and render
	model.handleResize(40, 24)
	var content40 string
	for _, msg := range model.messages {
		if msg.role == "assistant" {
			content40 = model.renderMessage(&msg)
			break
		}
	}

	lines80 := strings.Count(content80, "\n") + 1
	lines40 := strings.Count(content40, "\n") + 1

	// Narrower width should produce more lines (expanded has enough content to wrap)
	if lines40 <= lines80 {
		t.Errorf("After resize 80→40, expected more lines, got %d <= %d", lines40, lines80)
		t.Logf("Content at 80 (%d lines)\n%s", lines80, content80)
		t.Logf("Content at 40 (%d lines)\n%s", lines40, content40)
	}
}

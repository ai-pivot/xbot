package cli

import (
	"strings"
	"testing"

	"xbot/protocol"
)

// TestLiveIterationBlocks_MultipleStreamingTools verifies that multiple
// generating tools with DIFFERENT names are all rendered.
func TestLiveIterationBlocks_MultipleStreamingTools(t *testing.T) {
	model := newCLIModel()
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 1,
		StreamingTools: []protocol.ToolProgress{
			{Name: "Read", Status: "generating"},
			{Name: "Grep", Status: "generating"},
		},
	}

	blocks := model.liveIterationBlocks(model.progressState.current, 80, "")
	rendered := renderTurnBlocks(blocks)

	// Both tool names must appear in the rendered output
	if !strings.Contains(rendered, "Read") {
		t.Errorf("first generating tool 'Read' missing from render:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Grep") {
		t.Errorf("second generating tool 'Grep' missing from render:\n%s", rendered)
	}

	// Each tool should have its own hint line (skimming… / scanning…)
	hint1 := toolGeneratingHint("Read")
	hint2 := toolGeneratingHint("Grep")
	if !strings.Contains(rendered, hint1) {
		t.Errorf("Read hint %q missing from render:\n%s", hint1, rendered)
	}
	if !strings.Contains(rendered, hint2) {
		t.Errorf("Grep hint %q missing from render:\n%s", hint2, rendered)
	}
}

// TestLiveIterationBlocks_SameNameStreamingTools verifies that two
// generating tools with the SAME name (e.g. two Read calls) are both
// rendered — the dedup fix for generating tools.
func TestLiveIterationBlocks_SameNameStreamingTools(t *testing.T) {
	model := newCLIModel()
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 1,
		StreamingTools: []protocol.ToolProgress{
			{Name: "Read", Status: "generating"},
			{Name: "Read", Status: "generating"},
		},
	}

	blocks := model.liveIterationBlocks(model.progressState.current, 80, "")
	rendered := renderTurnBlocks(blocks)

	// "Read" should appear twice (two separate tool calls)
	readCount := strings.Count(rendered, "Read")
	if readCount < 2 {
		t.Errorf("expected 2 'Read' entries (same-name tools), got %d:\n%s", readCount, rendered)
	}
}

// TestStreamingToolsMerge_SameIter verifies that mergeProgressState
// preserves StreamingTools when a structured event merges within
// the same iteration.
func TestStreamingToolsMerge_SameIter(t *testing.T) {
	model := newCLIModel()

	// Current has StreamingTools set
	model.progressState.current = &protocol.ProgressEvent{
		Phase:          "thinking",
		Iteration:      1,
		StreamingTools: []protocol.ToolProgress{{Name: "Read", Status: "generating"}},
	}

	// Structured event arrives (same iteration, no StreamingTools)
	newPayload := &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 1,
	}
	model.mergeProgressState(newPayload)

	if len(model.progressState.current.StreamingTools) == 0 {
		t.Error("StreamingTools should be preserved on same-iteration merge — expected 1 tool, got 0")
	}
	if model.progressState.current.StreamingTools[0].Name != "Read" {
		t.Errorf("preserved tool name = %q, want Read", model.progressState.current.StreamingTools[0].Name)
	}
}

// TestStreamingToolsMerge_DifferentIter verifies that StreamingTools
// are cleared when mergeProgressState detects an iteration change —
// stream fields belong to the previous iteration.
func TestStreamingToolsMerge_DifferentIter(t *testing.T) {
	model := newCLIModel()

	// Current is iteration 1 with StreamingTools
	model.progressState.current = &protocol.ProgressEvent{
		Phase:          "tool_exec",
		Iteration:      1,
		StreamingTools: []protocol.ToolProgress{{Name: "Read", Status: "generating"}},
	}

	// New event is a different iteration (2 vs 1) — stream fields should be cleared
	newPayload := &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 2,
	}
	model.mergeProgressState(newPayload)

	if len(model.progressState.current.StreamingTools) != 0 {
		t.Errorf("StreamingTools should be cleared on iteration change, got %d tools",
			len(model.progressState.current.StreamingTools))
	}
}

// TestStreamingToolsMerge_FiltersTransitionedToActive verifies that
// mergeProgressState removes StreamingTools that have transitioned to
// ActiveTools — the tool moved from "generating" (LLM streaming args) to
// "running", and keeping the stale "generating" entry causes duplicate
// display (e.g. "Shell preparing…" persists alongside "Shell: running").
func TestStreamingToolsMerge_FiltersTransitionedToActive(t *testing.T) {
	model := newCLIModel()

	// Current: Read and Grep were both generating
	model.progressState.current = &protocol.ProgressEvent{
		Iteration: 1,
		StreamingTools: []protocol.ToolProgress{
			{Name: "Read", Status: "generating"},
			{Name: "Grep", Status: "generating"},
		},
	}

	// New event: Read has transitioned to running (ActiveTools)
	newPayload := &protocol.ProgressEvent{
		Iteration:   1,
		ActiveTools: []protocol.ToolProgress{{Name: "Read", Status: "running"}},
	}
	model.mergeProgressState(newPayload)

	// Only Grep should remain — Read transitioned to ActiveTools
	if len(model.progressState.current.StreamingTools) != 1 {
		t.Fatalf("expected 1 remaining tool (Grep), got %d: %+v",
			len(model.progressState.current.StreamingTools), model.progressState.current.StreamingTools)
	}
	if model.progressState.current.StreamingTools[0].Name != "Grep" {
		t.Errorf("remaining tool name = %q, want Grep", model.progressState.current.StreamingTools[0].Name)
	}
}

// TestStreamingToolsReplaceInStreamOnly verifies that handleProgressMsg
// correctly replaces StreamingTools from stream-only events (snapshot semantics).
func TestStreamingToolsReplaceInStreamOnly(t *testing.T) {
	model := newCLIModel()
	model.typing = true
	model.agentTurnID = 1
	model.channelName = "cli"
	model.chatID = "test"
	chatKey := "cli:test"

	// First StreamingTools event: one tool
	model.handleProgressMsg(cliProgressMsg{
		payload: &protocol.ProgressEvent{
			ChatID:         chatKey,
			Seq:            1,
			StreamingTools: []protocol.ToolProgress{{Name: "Read", Status: "generating"}},
		},
	})

	if model.progressState.current == nil {
		t.Fatal("current is nil after first StreamingTools event")
	}
	if len(model.progressState.current.StreamingTools) != 1 {
		t.Fatalf("expected 1 StreamingTools after first event, got %d", len(model.progressState.current.StreamingTools))
	}

	// Second StreamingTools event: two tools (snapshot includes both)
	model.handleProgressMsg(cliProgressMsg{
		payload: &protocol.ProgressEvent{
			ChatID: chatKey,
			Seq:    2,
			StreamingTools: []protocol.ToolProgress{
				{Name: "Read", Status: "generating"},
				{Name: "Grep", Status: "generating"},
			},
		},
	})

	if model.progressState.current == nil {
		t.Fatal("current is nil after second StreamingTools event")
	}
	if len(model.progressState.current.StreamingTools) != 2 {
		t.Fatalf("expected 2 StreamingTools after second event, got %d", len(model.progressState.current.StreamingTools))
	}

	// Verify both tool names are present
	names := make(map[string]bool)
	for _, tool := range model.progressState.current.StreamingTools {
		names[tool.Name] = true
	}
	if !names["Read"] {
		t.Error("Read missing from StreamingTools after replace")
	}
	if !names["Grep"] {
		t.Error("Grep missing from StreamingTools after replace")
	}
}

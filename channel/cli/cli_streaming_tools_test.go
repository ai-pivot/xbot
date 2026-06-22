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

// TestStreamingToolsCarryForward_SameIter verifies that carryForwardProgressState
// preserves StreamingTools when a structured event replaces current within
// the same iteration.
func TestStreamingToolsCarryForward_SameIter(t *testing.T) {
	model := newCLIModel()

	// Simulate: StreamingTools set on current
	prev := &protocol.ProgressEvent{
		Phase:          "thinking",
		Iteration:      1,
		StreamingTools: []protocol.ToolProgress{{Name: "Read", Status: "generating"}},
	}
	model.progressState.current = prev

	// Simulate: structured event arrives (same iteration, no StreamingTools)
	newPayload := &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 1,
		// No StreamingTools — carry forward should restore from prev
	}

	// Set up for carryForwardProgressState
	model.progressState.current = newPayload
	model.carryForwardProgressState(prev)

	if len(model.progressState.current.StreamingTools) == 0 {
		t.Error("StreamingTools was not carried forward — expected 1 tool, got 0")
	}
	if model.progressState.current.StreamingTools[0].Name != "Read" {
		t.Errorf("carried forward tool name = %q, want Read", model.progressState.current.StreamingTools[0].Name)
	}
}

// TestStreamingToolsCarryForward_DifferentIter verifies that StreamingTools
// are NOT carried forward when the new event belongs to a different iteration.
// This tests the sameIter guard.
func TestStreamingToolsCarryForward_DifferentIter(t *testing.T) {
	model := newCLIModel()

	// Previous iteration had StreamingTools
	prev := &protocol.ProgressEvent{
		Phase:          "tool_exec",
		Iteration:      1,
		StreamingTools: []protocol.ToolProgress{{Name: "Read", Status: "generating"}},
	}

	// New event is a different iteration (2 vs 1) — carry forward should NOT happen
	newPayload := &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 2,
	}

	model.progressState.current = newPayload
	model.carryForwardProgressState(prev)

	if len(model.progressState.current.StreamingTools) != 0 {
		t.Errorf("StreamingTools should NOT carry forward across iterations, got %d tools",
			len(model.progressState.current.StreamingTools))
	}
}

// TestStreamingToolsCarryForward_FiltersStaleActive verifies that carry
// forward does NOT restore a tool that has already transitioned to
// ActiveTools (running/done). This prevents the same tool rendering as
// both "generating" and "running" simultaneously.
func TestStreamingToolsCarryForward_FiltersStaleActive(t *testing.T) {
	model := newCLIModel()

	// Previous: Read was generating
	prev := &protocol.ProgressEvent{
		Iteration: 1,
		StreamingTools: []protocol.ToolProgress{
			{Name: "Read", Status: "generating"},
			{Name: "Grep", Status: "generating"},
		},
	}

	// New event: Read has transitioned to running (ActiveTools),
	// Grep is still generating (not in ActiveTools yet).
	newPayload := &protocol.ProgressEvent{
		Iteration:   1,
		ActiveTools: []protocol.ToolProgress{{Name: "Read", Status: "running"}},
	}

	model.progressState.current = newPayload
	model.carryForwardProgressState(prev)

	// Only Grep should be carried forward — Read is already in ActiveTools
	if len(model.progressState.current.StreamingTools) != 1 {
		t.Fatalf("expected 1 carried-forward tool (Grep), got %d: %+v",
			len(model.progressState.current.StreamingTools), model.progressState.current.StreamingTools)
	}
	if model.progressState.current.StreamingTools[0].Name != "Grep" {
		t.Errorf("expected Grep, got %q", model.progressState.current.StreamingTools[0].Name)
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

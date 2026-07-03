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

	if !strings.Contains(rendered, "Read") {
		t.Errorf("first generating tool 'Read' missing from render:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Grep") {
		t.Errorf("second generating tool 'Grep' missing from render:\n%s", rendered)
	}
}

// TestLiveIterationBlocks_SameNameStreamingTools verifies that two
// generating tools with the SAME name are both rendered.
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

	readCount := strings.Count(rendered, "Read")
	if readCount < 2 {
		t.Errorf("expected 2 'Read' entries, got %d:\n%s", readCount, rendered)
	}
}

// TestStreamingTools_StructuredReplaces verifies that a structured event
// replaces the current state entirely. StreamingTools are ephemeral — they
// come from stream-only events and are cleared when a structured event arrives
// (the tool has finished generating and moved to ActiveTools/CompletedTools).
func TestStreamingTools_StructuredReplaces(t *testing.T) {
	model := newCLIModel()

	model.progressState.current = &protocol.ProgressEvent{
		Phase:          "thinking",
		Iteration:      1,
		StreamingTools: []protocol.ToolProgress{{Name: "Read", Status: "generating"}},
	}

	// Structured event replaces current — StreamingTools are NOT preserved.
	newPayload := &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 1,
		Seq:       1,
	}
	model.applyProgressSnapshot(newPayload)

	if len(model.progressState.current.StreamingTools) != 0 {
		t.Errorf("StreamingTools should be cleared by structured event — got %d tools",
			len(model.progressState.current.StreamingTools))
	}
}

// TestStreamingTools_DifferentIter verifies that StreamingTools are cleared
// when iteration changes.
func TestStreamingTools_DifferentIter(t *testing.T) {
	model := newCLIModel()

	model.progressState.current = &protocol.ProgressEvent{
		Phase:          "tool_exec",
		Iteration:      1,
		StreamingTools: []protocol.ToolProgress{{Name: "Read", Status: "generating"}},
	}

	newPayload := &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 2,
		Seq:       1,
	}
	model.applyProgressSnapshot(newPayload)

	if len(model.progressState.current.StreamingTools) != 0 {
		t.Errorf("StreamingTools should be cleared on iteration change, got %d tools",
			len(model.progressState.current.StreamingTools))
	}
}

// TestStreamingTools_StreamOnlyUpdates verifies that stream-only events
// correctly update StreamingTools on the current state (the low-latency
// display path).
func TestStreamingTools_StreamOnlyUpdates(t *testing.T) {
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

	// Second StreamingTools event: two tools
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
		t.Fatal("current is nil after second event")
	}
	if len(model.progressState.current.StreamingTools) != 2 {
		t.Fatalf("expected 2 StreamingTools, got %d", len(model.progressState.current.StreamingTools))
	}

	names := make(map[string]bool)
	for _, tool := range model.progressState.current.StreamingTools {
		names[tool.Name] = true
	}
	if !names["Read"] || !names["Grep"] {
		t.Error("tool names missing from StreamingTools")
	}
}

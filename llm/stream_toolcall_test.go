package llm

import (
	"context"
	"testing"
)

// TestCollectStreamWithCallback_ToolCallEarlyDetection verifies that the
// onToolCall callback fires when a tool NAME arrives (first chunk), before
// arguments finish streaming. This is the core of "early tool detection".
func TestCollectStreamWithCallback_ToolCallEarlyDetection(t *testing.T) {
	ch := make(chan StreamEvent, 10)
	// Tool name arrives in the first chunk — this is when the UI should
	// immediately show "✦ Read generating…"
	ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCallDelta{Index: 0, ID: "call_1", Name: "Read"}}
	// Arguments stream in subsequent chunks — callback should NOT fire again
	ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCallDelta{Index: 0, Arguments: `{"path":"`}}
	ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCallDelta{Index: 0, Arguments: `test.go"}`}}
	ch <- StreamEvent{Type: EventDone, FinishReason: FinishReasonToolCalls}
	close(ch)

	var snapshots [][]ToolCallDelta
	resp, err := CollectStreamWithCallback(context.Background(), ch, nil, nil, func(toolCalls []ToolCallDelta) {
		snapshots = append(snapshots, toolCalls)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Callback should fire exactly once — only when the name arrives,
	// NOT for argument-only deltas.
	if len(snapshots) != 1 {
		t.Fatalf("onToolCall callback count = %d, want 1 (only fires on name arrival)", len(snapshots))
	}
	snap := snapshots[0]
	if len(snap) != 1 {
		t.Fatalf("snapshot has %d tool calls, want 1", len(snap))
	}
	if snap[0].Name != "Read" {
		t.Errorf("snapshot[0].Name = %q, want %q", snap[0].Name, "Read")
	}
	if snap[0].ID != "call_1" {
		t.Errorf("snapshot[0].ID = %q, want %q", snap[0].ID, "call_1")
	}
	// Final response should have complete tool call with full arguments
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("response has %d tool calls, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Arguments != `{"path":"test.go"}` {
		t.Errorf("response args = %q, want %q", resp.ToolCalls[0].Arguments, `{"path":"test.go"}`)
	}
}

// TestCollectStreamWithCallback_MultiToolEarlyDetection verifies that the
// onToolCall callback fires for each tool name as it arrives, and that
// the snapshot includes all known tools at that point.
func TestCollectStreamWithCallback_MultiToolEarlyDetection(t *testing.T) {
	ch := make(chan StreamEvent, 10)
	// First tool name arrives
	ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCallDelta{Index: 0, ID: "c1", Name: "Read"}}
	// First tool arguments
	ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCallDelta{Index: 0, Arguments: `{"path":"a"}`}}
	// Second tool name arrives
	ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCallDelta{Index: 1, ID: "c2", Name: "Shell"}}
	// Second tool arguments
	ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCallDelta{Index: 1, Arguments: `{"command":"ls"}`}}
	ch <- StreamEvent{Type: EventDone, FinishReason: FinishReasonToolCalls}
	close(ch)

	var snapshots [][]ToolCallDelta
	_, err := CollectStreamWithCallback(context.Background(), ch, nil, nil, func(toolCalls []ToolCallDelta) {
		snapshots = append(snapshots, toolCalls)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Two callbacks: one when "Read" name arrives, one when "Shell" name arrives
	if len(snapshots) != 2 {
		t.Fatalf("onToolCall callback count = %d, want 2", len(snapshots))
	}
	// First snapshot: only Read known
	if len(snapshots[0]) != 1 || snapshots[0][0].Name != "Read" {
		t.Errorf("first snapshot = %+v, want [Read]", snapshots[0])
	}
	// Second snapshot: both Read and Shell known
	if len(snapshots[1]) != 2 {
		t.Fatalf("second snapshot has %d tools, want 2", len(snapshots[1]))
	}
	if snapshots[1][0].Name != "Read" {
		t.Errorf("second snapshot[0].Name = %q, want Read", snapshots[1][0].Name)
	}
	if snapshots[1][1].Name != "Shell" {
		t.Errorf("second snapshot[1].Name = %q, want Shell", snapshots[1][1].Name)
	}
}

// TestCollectStreamWithCallback_NilToolCallCallback verifies that passing nil
// for onToolCall does not panic and tool calls are still collected normally.
func TestCollectStreamWithCallback_NilToolCallCallback(t *testing.T) {
	ch := make(chan StreamEvent, 10)
	ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCallDelta{Index: 0, ID: "c1", Name: "Read", Arguments: `{"path":"x"}`}}
	ch <- StreamEvent{Type: EventDone, FinishReason: FinishReasonToolCalls}
	close(ch)

	resp, err := CollectStreamWithCallback(context.Background(), ch, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "Read" {
		t.Errorf("tool name = %q, want Read", resp.ToolCalls[0].Name)
	}
}

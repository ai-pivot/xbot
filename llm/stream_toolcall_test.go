package llm

import (
	"context"
	"testing"
)

// TestCollectStreamWithCallback_ToolCallEarlyDetection verifies that the
// onToolCall callback fires when a tool NAME arrives (first chunk), before
// arguments finish streaming. This is the core of "early tool detection".
// The callback also fires on argument deltas, enabling real-time progress.
func TestCollectStreamWithCallback_ToolCallEarlyDetection(t *testing.T) {
	ch := make(chan StreamEvent, 10)
	// Tool name arrives in the first chunk — this is when the UI should
	// immediately show "✦ Read generating…"
	ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCallDelta{Index: 0, ID: "call_1", Name: "Read"}}
	// Arguments stream in subsequent chunks — callback fires for each delta
	// so the UI can show progress (e.g. "9 chars", "18 chars").
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
	// Callback fires once per tool call event: 1 name + 2 arg deltas = 3
	if len(snapshots) != 3 {
		t.Fatalf("onToolCall callback count = %d, want 3 (1 name + 2 arg deltas)", len(snapshots))
	}
	// First snapshot: name detected, no args yet (early detection)
	snap := snapshots[0]
	if len(snap) != 1 || snap[0].Name != "Read" || snap[0].ID != "call_1" {
		t.Errorf("first snapshot = %+v, want [Read/call_1]", snap)
	}
	if snap[0].Arguments != "" {
		t.Errorf("first snapshot args = %q, want empty (name only)", snap[0].Arguments)
	}
	// Second snapshot: partial args
	if snapshots[1][0].Arguments != `{"path":"` {
		t.Errorf("second snapshot args = %q, want %q", snapshots[1][0].Arguments, `{"path":"`)
	}
	// Third snapshot: more args accumulated
	if snapshots[2][0].Arguments != `{"path":"test.go"}` {
		t.Errorf("third snapshot args = %q, want %q", snapshots[2][0].Arguments, `{"path":"test.go"}`)
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
// onToolCall callback fires for each tool call event (name arrival + argument
// deltas), and that the snapshot includes all known tools at each point.
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
	// 4 callbacks: Read name, Read args, Shell name, Shell args
	if len(snapshots) != 4 {
		t.Fatalf("onToolCall callback count = %d, want 4 (2 names + 2 arg deltas)", len(snapshots))
	}
	// First snapshot: only Read known, no args
	if len(snapshots[0]) != 1 || snapshots[0][0].Name != "Read" {
		t.Errorf("first snapshot = %+v, want [Read]", snapshots[0])
	}
	if snapshots[0][0].Arguments != "" {
		t.Errorf("first snapshot args = %q, want empty", snapshots[0][0].Arguments)
	}
	// Second snapshot: Read with args
	if snapshots[1][0].Arguments != `{"path":"a"}` {
		t.Errorf("second snapshot Read args = %q, want %q", snapshots[1][0].Arguments, `{"path":"a"}`)
	}
	// Third snapshot: both tools, Shell just named
	if len(snapshots[2]) != 2 {
		t.Fatalf("third snapshot has %d tools, want 2", len(snapshots[2]))
	}
	if snapshots[2][0].Name != "Read" || snapshots[2][1].Name != "Shell" {
		t.Errorf("third snapshot names = [%s, %s], want [Read, Shell]", snapshots[2][0].Name, snapshots[2][1].Name)
	}
	// Fourth snapshot: both tools with args
	if snapshots[3][1].Arguments != `{"command":"ls"}` {
		t.Errorf("fourth snapshot Shell args = %q, want %q", snapshots[3][1].Arguments, `{"command":"ls"}`)
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

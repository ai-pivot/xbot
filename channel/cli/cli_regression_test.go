package cli

import (
	"testing"
	"time"

	"xbot/protocol"
)

// ════════════════════════════════════════════════════════════════════════
// REGRESSION TESTS — each test fixes a specific bug that was found and
// fixed during the TUI pull-model refactor. Tests are named after the
// bug's visual symptom so future regressions are easy to identify.
// ════════════════════════════════════════════════════════════════════════

// ── Bug: content displays truncated during tool execution ──
// Root cause: streamContentFunc is throttled (60ms). The last throttled
// push may be incomplete. When LLM finishes, structured event carries
// authoritative full Content, but applyProgressSnapshot preserved the
// stale incomplete StreamContent. Render prioritizes StreamContent →
// truncated display until tool finishes.
//
// Fix: when snapshot.Content is non-empty, do NOT preserve stale
// StreamContent. Same for Reasoning vs ReasoningStreamContent.
func TestRegression_ContentTruncatedDuringToolExec(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Stream content arrives (throttled, incomplete — missing "对比脚本：")
	sendProgress(model, &protocol.ProgressEvent{
		StreamContent: "先写一个全面的单线程",
	})

	if model.progressState.current.StreamContent != "先写一个全面的单线程" {
		t.Fatalf("stream content not applied: got %q",
			model.progressState.current.StreamContent)
	}

	// Structured event arrives with finalized Content (complete text)
	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:      "cli:/test",
		Seq:         2,
		Phase:       "tool_exec",
		Iteration:   0,
		Content:     "先写一个全面的单线程对比脚本：",
		ActiveTools: []protocol.ToolProgress{{Name: "Shell", Status: "running"}},
	})

	// Render should use Content (complete), not StreamContent (truncated)
	displayContent := model.progressState.current.StreamContent
	if displayContent != "" {
		t.Errorf("stale StreamContent preserved over finalized Content: %q", displayContent)
	}
	if model.progressState.current.Content != "先写一个全面的单线程对比脚本：" {
		t.Errorf("finalized Content not set: got %q",
			model.progressState.current.Content)
	}
}

// ── Bug: phantom "generating" tool line appears alongside "running" ──
// Root cause: progressCh coalescing unconditionally merged old
// StreamingTools into new structured events. When tool transitions
// generating→running, structured event carries ActiveTools but also
// inherits stale StreamingTools → both rendered for one frame.
//
// Fix: only merge StreamingTools when payload.ActiveTools is empty.
func TestRegression_GhostGeneratingToolDuringTransition(t *testing.T) {
	// This tests the progressCh coalescing in SendProgress (cli.go).
	// We simulate two events that would be coalesced:
	// A = stream-only with StreamingTools (generating)
	// B = structured with ActiveTools (running)
	a := cliProgressMsg{payload: &protocol.ProgressEvent{
		Seq:            1,
		StreamingTools: []protocol.ToolProgress{{Name: "Shell", Status: "generating"}},
	}}
	b := cliProgressMsg{payload: &protocol.ProgressEvent{
		Seq:         2,
		Phase:       "tool_exec",
		Iteration:   1,
		ActiveTools: []protocol.ToolProgress{{Name: "Shell", Status: "running"}},
	}}

	merged := coalesceProgress(a, b)

	// Merged event should NOT carry stale StreamingTools — ActiveTools
	// is present, meaning the tool moved past generating.
	if len(merged.payload.StreamingTools) > 0 {
		t.Errorf("stale StreamingTools merged into structured event with ActiveTools: "+
			"got %d generating tools — should be 0", len(merged.payload.StreamingTools))
	}
	if len(merged.payload.ActiveTools) != 1 || merged.payload.ActiveTools[0].Status != "running" {
		t.Errorf("ActiveTools not preserved: %+v", merged.payload.ActiveTools)
	}
}

// ── Bug: coalesceProgress loses StreamContent when next event has ReasoningStreamContent ──
// Root cause: old coalesceProgress took "keep b (newer)" for stream-only,
// losing a's StreamContent when b only carried ReasoningStreamContent.
//
// Fix: merge each stream field independently, taking the longest value.
func TestRegression_CoalesceLosesContentFromDifferentFields(t *testing.T) {
	a := cliProgressMsg{payload: &protocol.ProgressEvent{
		Seq:           1,
		StreamContent: "Hello world content",
	}}
	b := cliProgressMsg{payload: &protocol.ProgressEvent{
		Seq:                    2,
		ReasoningStreamContent: "Let me think about this",
	}}

	merged := coalesceProgress(a, b)

	if merged.payload.StreamContent != "Hello world content" {
		t.Errorf("StreamContent lost during coalesce: got %q, want %q",
			merged.payload.StreamContent, "Hello world content")
	}
	if merged.payload.ReasoningStreamContent != "Let me think about this" {
		t.Errorf("ReasoningStreamContent lost during coalesce: got %q",
			merged.payload.ReasoningStreamContent)
	}
}

// ── Bug: coalesceProgress loses reasoning when next event has content ──
// Same root cause as above, reversed fields.
func TestRegression_CoalesceLosesReasoningFromDifferentFields(t *testing.T) {
	a := cliProgressMsg{payload: &protocol.ProgressEvent{
		Seq:                    1,
		ReasoningStreamContent: "Thinking step 1",
	}}
	b := cliProgressMsg{payload: &protocol.ProgressEvent{
		Seq:           2,
		StreamContent: "Here is the answer",
	}}

	merged := coalesceProgress(a, b)

	if merged.payload.ReasoningStreamContent != "Thinking step 1" {
		t.Errorf("ReasoningStreamContent lost during coalesce: got %q",
			merged.payload.ReasoningStreamContent)
	}
	if merged.payload.StreamContent != "Here is the answer" {
		t.Errorf("StreamContent not applied: got %q", merged.payload.StreamContent)
	}
}

// ── Bug: coalesceProgress takes shorter content over longer ──
// Stream content is cumulative — longer = more complete. Coalesce must
// take the longest, not just "the newer one".
func TestRegression_CoalesceTakesShorterContent(t *testing.T) {
	a := cliProgressMsg{payload: &protocol.ProgressEvent{
		Seq:           1,
		StreamContent: "This is the complete sentence with all words",
	}}
	b := cliProgressMsg{payload: &protocol.ProgressEvent{
		Seq:           2,
		StreamContent: "This is the complete", // throttled, shorter
	}}

	merged := coalesceProgress(a, b)

	if merged.payload.StreamContent != "This is the complete sentence with all words" {
		t.Errorf("coalesce took shorter content: got %q, want longer",
			merged.payload.StreamContent)
	}
}

// ── Bug: tool elapsed timer resets to 0ms on every snapshot ──
// Root cause: applyProgressSnapshot does direct replacement of current,
// discarding ActiveTools[].StartedAt from previous state. Backend sends
// Elapsed (static ms) but not StartedAt → live timer shows "0ms".
//
// Fix: carry over StartedAt from matching running tools in previous state.
func TestRegression_ToolTimerResetsOnSnapshot(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	startTime := time.Now().Add(-5 * time.Second)

	// First snapshot: tool running with StartedAt
	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:      "cli:/test",
		Seq:         1,
		Phase:       "tool_exec",
		Iteration:   0,
		ActiveTools: []protocol.ToolProgress{{Name: "Shell", Status: "running", StartedAt: startTime}},
	})

	// Second snapshot: same tool still running (backend doesn't send StartedAt)
	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:      "cli:/test",
		Seq:         2,
		Phase:       "tool_exec",
		Iteration:   0,
		ActiveTools: []protocol.ToolProgress{{Name: "Shell", Status: "running"}}, // no StartedAt
	})

	if model.progressState.current.ActiveTools[0].StartedAt.IsZero() {
		t.Error("StartedAt was lost across snapshot replacement — timer would reset to 0ms")
	}
	if !model.progressState.current.ActiveTools[0].StartedAt.Equal(startTime) {
		t.Errorf("StartedAt not preserved: got %v, want %v",
			model.progressState.current.ActiveTools[0].StartedAt, startTime)
	}
}

// ── Bug: new tool doesn't get StartedAt ──
// When a brand new running tool appears (not in previous state), it
// should get StartedAt=time.Now() so the timer starts immediately.
func TestRegression_NewToolGetsStartedAt(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// First snapshot: no tools
	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Seq:       1,
		Phase:     "thinking",
		Iteration: 0,
	})

	// Second snapshot: new running tool appears
	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:      "cli:/test",
		Seq:         2,
		Phase:       "tool_exec",
		Iteration:   0,
		ActiveTools: []protocol.ToolProgress{{Name: "Shell", Status: "running"}},
	})

	if model.progressState.current.ActiveTools[0].StartedAt.IsZero() {
		t.Error("new running tool did not get StartedAt — timer would show 0ms")
	}
}

// ── Bug: cancel loses latest iteration ──
// Root cause: (1) ctx wrapper dropped PhaseDone on cancel, (2)
// handleCancelAck conditionally skipped cancelledTurnIterations().
//
// Fix: (1) PhaseDone allowed through ctx wrapper, (2) always call
// cancelledTurnIterations() unconditionally.
func TestRegression_CancelLosesIteration(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Simulate progress: iteration 1 with content + tool
	sendProgress(model, &protocol.ProgressEvent{
		ChatID:        "cli:/test",
		Seq:           1,
		Phase:         "tool_exec",
		Iteration:     1,
		Content:       "Working on something",
		StreamContent: "Working on something",
		ActiveTools:   []protocol.ToolProgress{{Name: "Shell", Status: "running"}},
	})

	// Verify iteration state exists
	if model.progressState.current == nil {
		t.Fatal("no current progress state before cancel")
	}

	// Simulate cancel: should capture the live iteration
	iters := model.cancelledTurnIterations()
	if len(iters) == 0 {
		t.Error("cancelledTurnIterations returned empty — latest iteration lost on cancel")
	}
}

// ── Bug: stale structured event overwrites newer state ──
// Root cause: without Seq monotonic guard, a delayed structured event
// could overwrite a newer snapshot.
//
// Fix: lastAppliedSeq monotonic guard in applyProgressSnapshot.
func TestRegression_StaleStructuredEventOverwrites(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Apply iteration 3
	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Seq:       10,
		Phase:     "thinking",
		Iteration: 3,
	})

	// Stale event (iteration 1, Seq=5) must be discarded
	model.handleProgressMsg(cliProgressMsg{
		payload: &protocol.ProgressEvent{
			ChatID:    "cli:/test",
			Seq:       5,
			Phase:     "tool_exec",
			Iteration: 1,
		},
	})

	if model.progressState.current.Iteration != 3 {
		t.Errorf("stale event overwrote: got Iteration=%d, want 3",
			model.progressState.current.Iteration)
	}
}

// ── Bug: new turn's events blocked by previous turn's Seq ──
// Root cause: lastAppliedSeq not reset on startAgentTurn.
//
// Fix: resetProgressState zeroes lastAppliedSeq.
func TestRegression_NewTurnBlockedByOldSeq(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Seq:       500,
		Phase:     "done",
		Iteration: 1,
	})

	// New turn
	model.startAgentTurn()

	// Turn 2's first event (Seq=1) must be accepted
	model.handleProgressMsg(cliProgressMsg{
		payload: &protocol.ProgressEvent{
			ChatID:    "cli:/test",
			Seq:       1,
			Phase:     "thinking",
			Iteration: 0,
		},
	})

	if model.progressState.current == nil {
		t.Fatal("turn 2 first event blocked by stale lastAppliedSeq from turn 1")
	}
}

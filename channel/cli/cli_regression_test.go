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
// Root cause: progressSlot coalescing unconditionally merged old
// StreamingTools into new structured events. When tool transitions
// generating→running, structured event carries ActiveTools but also
// inherits stale StreamingTools → both rendered for one frame.
//
// Fix: only merge StreamingTools when payload.ActiveTools is empty.
func TestRegression_GhostGeneratingToolDuringTransition(t *testing.T) {
	// This tests the progressSlot coalescing in SendProgress (cli.go).
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

func TestRegression_GhostGeneratingToolAfterError(t *testing.T) {
	a := cliProgressMsg{payload: &protocol.ProgressEvent{
		Seq:            1,
		StreamingTools: []protocol.ToolProgress{{Name: "FileReplace", Status: "generating"}},
	}}
	b := cliProgressMsg{payload: &protocol.ProgressEvent{
		Seq:       2,
		Phase:     "tool_exec",
		Iteration: 1,
		CompletedTools: []protocol.ToolProgress{{
			Name:      "FileReplace",
			Status:    "error",
			Iteration: 1,
		}},
	}}

	merged := coalesceProgress(a, b)

	if len(merged.payload.StreamingTools) > 0 {
		t.Fatalf("stale generating tool survived structured error: %+v", merged.payload.StreamingTools)
	}
	if len(merged.payload.CompletedTools) != 1 || merged.payload.CompletedTools[0].Status != "error" {
		t.Fatalf("structured error tool not preserved: %+v", merged.payload.CompletedTools)
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

// ── Bug: content truncated when tool generating event arrives ──
// Root cause: streamContentFunc was throttled (60ms). When content
// callback was skipped (within throttle window), the full content only
// went to atomic streamState but was NOT pushed. Then unthrottled
// streamToolCallFunc pushed with empty StreamContent → coalescing
// preserved stale incomplete content from the last throttled push.
//
// Fix: removed throttle entirely. catch-up drain in handleAsyncDrain
// prevents backlog. Every callback pushes immediately.
func TestRegression_ContentTruncatedWhenToolGeneratingArrives(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Stream content arrives (complete text)
	sendProgress(model, &protocol.ProgressEvent{
		StreamContent: "Let me update the controllers CMake",
	})

	if model.progressState.current.StreamContent != "Let me update the controllers CMake" {
		t.Fatalf("stream content not applied: got %q",
			model.progressState.current.StreamContent)
	}

	// Tool call event arrives immediately after (no throttle delay)
	// This event does NOT carry StreamContent
	sendProgress(model, &protocol.ProgressEvent{
		StreamingTools: []protocol.ToolProgress{{Name: "FileReplace", Status: "generating"}},
	})

	// StreamContent must be preserved — not lost or truncated
	if model.progressState.current.StreamContent != "Let me update the controllers CMake" {
		t.Errorf("StreamContent lost when tool generating arrived: got %q, want %q",
			model.progressState.current.StreamContent, "Let me update the controllers CMake")
	}
	// StreamingTools must be set
	if len(model.progressState.current.StreamingTools) != 1 {
		t.Errorf("StreamingTools not set: got %d tools", len(model.progressState.current.StreamingTools))
	}
}

// ── Bug: coalesceProgress drops content when tool event has no content ──
// Same root cause in the coalescing layer: stream-only content event
// coalesced with stream-only tool event (no content) must preserve
// content from the first event.
func TestRegression_CoalesceDropsContentOnToolEvent(t *testing.T) {
	a := cliProgressMsg{payload: &protocol.ProgressEvent{
		Seq:           1,
		StreamContent: "Let me update the controllers CMake",
	}}
	b := cliProgressMsg{payload: &protocol.ProgressEvent{
		Seq:            2,
		StreamingTools: []protocol.ToolProgress{{Name: "FileReplace", Status: "generating"}},
	}}

	merged := coalesceProgress(a, b)

	if merged.payload.StreamContent != "Let me update the controllers CMake" {
		t.Errorf("content lost when coalescing with tool event: got %q, want %q",
			merged.payload.StreamContent, "Let me update the controllers CMake")
	}
	if len(merged.payload.StreamingTools) != 1 {
		t.Errorf("StreamingTools lost: got %d tools", len(merged.payload.StreamingTools))
	}
}

// ── Bug: stream content events have unqualified ChatID → TUI discards ──
// Root cause: SendStreamContent implementations had inconsistent ChatID
// qualification. Stream callbacks now go through SendProgress with
// qualified payload.ChatID — this test verifies the TUI accepts them.
func TestRegression_StreamContentChatIDQualified(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Stream content event with QUALIFIED ChatID (what stream callbacks now send)
	sendProgress(model, &protocol.ProgressEvent{
		ChatID:        "cli:/test",
		StreamContent: "content via qualified ChatID",
	})

	if model.progressState.current == nil {
		t.Fatal("stream content event with qualified ChatID was discarded by session filter")
	}
	if model.progressState.current.StreamContent != "content via qualified ChatID" {
		t.Errorf("StreamContent not applied: got %q",
			model.progressState.current.StreamContent)
	}
}

// ── Bug: raw ChatID stream event must be rejected by session filter ──
// This is the negative test — verifies that unqualified ChatID events
// are still filtered out (the original bug scenario).
func TestRegression_RawChatIDStreamEventRejected(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	// initTestModel sets chatID="/test", channelName="cli"
	// currentKey = "cli:/test"

	// Send event with RAW ChatID (the old bug)
	model.handleProgressMsg(cliProgressMsg{
		payload: &protocol.ProgressEvent{
			ChatID:        "/test", // raw, unqualified
			StreamContent: "should be rejected",
		},
	})

	if model.progressState.current != nil &&
		model.progressState.current.StreamContent == "should be rejected" {
		t.Error("raw ChatID event was accepted — session filter broken")
	}
}

// ── Bug: iteration snapshots must come from DB, not local capture ──
// snapshotIterationLocal was removed because it could capture incomplete
// iterations (empty Content/Tools) during streaming. Now iterations ONLY
// come from restoreIterationsFromSnapshot (DB IterationHistory, authoritative)
// or finalizeTurnFromSnapshot (PhaseDone, carries finalized state).
//
// This test verifies that push events alone (without IterationHistory) do
// NOT create local snapshots — the data must come from DB.
func TestRegression_NoLocalSnapshotFromPushEvents(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Iteration 0: tool running (push event, no IterationHistory)
	sendProgress(model, &protocol.ProgressEvent{
		ChatID:      "cli:/test",
		Seq:         1,
		Phase:       "tool_exec",
		Iteration:   0,
		ActiveTools: []protocol.ToolProgress{{Name: "config", Status: "running", Iteration: 0}},
	})

	// Iteration changes to 1 (push event, no IterationHistory)
	sendProgress(model, &protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Seq:       2,
		Phase:     "thinking",
		Iteration: 1,
	})

	// No local snapshot should be created from push events alone.
	if len(model.progressState.iterations) != 0 {
		t.Errorf("expected 0 local snapshots from push events, got %d", len(model.progressState.iterations))
	}
}

func TestRegression_IterationAdvancePushCarriesCompletedHistory(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// C is currently rendered live with reasoning, content, and a generating tool.
	sendProgress(model, &protocol.ProgressEvent{
		ChatID:                 "cli:/test",
		Seq:                    1,
		Phase:                  "tool_exec",
		Iteration:              2,
		StreamContent:          "content C",
		ReasoningStreamContent: "reasoning C",
		StreamingTools:         []protocol.ToolProgress{{Name: "Shell", Status: "generating"}},
	})

	// D arrives as a push event. It must carry C in IterationHistory, otherwise
	// applying D replaces current and C disappears until the next tick pull.
	sendProgress(model, &protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Seq:       2,
		Phase:     "thinking",
		Iteration: 3,
		IterationHistory: []protocol.ProgressEvent{{
			ChatID:    "cli:/test",
			Phase:     "tool_exec",
			Iteration: 2,
			Content:   "content C",
			Reasoning: "reasoning C",
			CompletedTools: []protocol.ToolProgress{{
				Name:      "Shell",
				Status:    "done",
				Iteration: 2,
			}},
		}},
	})

	if len(model.progressState.iterations) != 1 {
		t.Fatalf("expected C restored as completed history on D push, got %d", len(model.progressState.iterations))
	}
	got := model.progressState.iterations[0]
	if got.Iteration != 2 || got.Content != "content C" || got.Reasoning != "reasoning C" {
		t.Fatalf("C history not preserved across iteration advance: %+v", got)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "Shell" {
		t.Fatalf("C tool history not preserved: %+v", got.Tools)
	}
	if model.progressState.current == nil || model.progressState.current.Iteration != 3 {
		t.Fatalf("expected current to advance to D, got %+v", model.progressState.current)
	}
}

func TestRegression_GeneratingToolPreservedUntilStructuredToolState(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	sendProgress(model, &protocol.ProgressEvent{
		ChatID:         "cli:/test",
		Seq:            1,
		Phase:          "tool_exec",
		Iteration:      2,
		StreamingTools: []protocol.ToolProgress{{Name: "Shell", Status: "generating"}},
	})

	// A sparse structured snapshot for the same iteration must not briefly erase
	// the generating tool before the real done/active tool state arrives.
	sendProgress(model, &protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Seq:       2,
		Phase:     "tool_exec",
		Iteration: 2,
	})

	if len(model.progressState.current.StreamingTools) != 1 || model.progressState.current.StreamingTools[0].Name != "Shell" {
		t.Fatalf("generating tool disappeared on sparse same-iteration snapshot: %+v", model.progressState.current)
	}

	// Once a structured tool state arrives, generating must not linger alongside done.
	sendProgress(model, &protocol.ProgressEvent{
		ChatID:         "cli:/test",
		Seq:            3,
		Phase:          "tool_exec",
		Iteration:      2,
		CompletedTools: []protocol.ToolProgress{{Name: "Shell", Status: "done", Iteration: 2}},
	})

	if len(model.progressState.current.StreamingTools) != 0 {
		t.Fatalf("generating tool lingered after structured done arrived: %+v", model.progressState.current.StreamingTools)
	}
	if len(model.progressState.current.CompletedTools) != 1 || model.progressState.current.CompletedTools[0].Name != "Shell" {
		t.Fatalf("done tool state missing after structured snapshot: %+v", model.progressState.current.CompletedTools)
	}
}

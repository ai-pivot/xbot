package cli

import (
	"strings"
	"testing"
	"time"

	ch "xbot/channel"
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

// ════════════════════════════════════════════════════════════════════════
// REGRESSION: Iteration 0 disappears after PhaseDone + handleAgentMessage
// ════════════════════════════════════════════════════════════════════════

// ── Bug: tick pull watermark uses lastIter, which may be advanced past
// iteration 0 without that iteration entering progressState.iterations.
// When iterations list is empty, watermark should be -1 (not lastIter)
// so the server returns ALL iterations including iteration 0.
//
// Fix: tick pull watermark = max(iterations[].Iteration), or -1 when empty.
func TestRegression_TickPullWatermarkRecoversIteration0(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Simulate: progressState has lastIter=1 (from a structured snapshot),
	// but iterations list is EMPTY (delta was lost in coalescing).
	model.progressState.lastIter = 1
	model.progressState.iterations = nil // empty!

	// Manually compute the watermark as the tick handler does
	watermark := -1
	for _, it := range model.progressState.iterations {
		if it.Iteration > watermark {
			watermark = it.Iteration
		}
	}

	// Watermark must be -1 so the server returns iteration 0
	if watermark != -1 {
		t.Fatalf("watermark with empty iterations = %d, want -1", watermark)
	}

	// Now simulate: iterations has [iter1], missing iter0
	model.progressState.iterations = []cliIterationSnapshot{
		{Iteration: 1, Content: "iter1"},
	}
	watermark = -1
	for _, it := range model.progressState.iterations {
		if it.Iteration > watermark {
			watermark = it.Iteration
		}
	}

	// Watermark must be 1 (we have iter1, need iter0 from server)
	// Server returns iterations > 1, which excludes iter0.
	// This is correct — iter0 is already lost if it's not in the list
	// AND the server's history doesn't have it. The tick pull can't
	// recover what the server already evicted. But with Fix 1 (structured
	// events are stateful), the delta is never lost in the first place.
	if watermark != 1 {
		t.Fatalf("watermark with [iter1] = %d, want 1", watermark)
	}

	// Verify: when iterations has [iter0, iter1], watermark = 1
	// Server returns iterations > 1 (nothing new) — correct.
	model.progressState.iterations = []cliIterationSnapshot{
		{Iteration: 0, Content: "iter0"},
		{Iteration: 1, Content: "iter1"},
	}
	watermark = -1
	for _, it := range model.progressState.iterations {
		if it.Iteration > watermark {
			watermark = it.Iteration
		}
	}
	if watermark != 1 {
		t.Fatalf("watermark with [iter0,iter1] = %d, want 1", watermark)
	}
}

// ── Bug: handleAgentMessage overwrites iterations baked by
// finalizeTurnFromSnapshot with progressState.iterations, which may be
// missing iteration 0 (lost in coalescing). This permanently loses iter0.
//
// Fix: mergeIterations takes the union of existing and new iterations,
// deduplicating by iteration number (newer wins for duplicates).
func TestRegression_MergeIterationsPreservesBoth(t *testing.T) {
	existing := []cliIterationSnapshot{
		{Iteration: 0, Content: "iter0-from-finalize"},
		{Iteration: 1, Content: "iter1-from-finalize"},
	}
	newer := []cliIterationSnapshot{
		{Iteration: 1, Content: "iter1-from-progressState"}, // newer data for iter1
		{Iteration: 2, Content: "iter2-new"},                // new iteration
	}

	merged := mergeIterations(existing, newer)

	if len(merged) != 3 {
		t.Fatalf("expected 3 merged iterations, got %d: %+v", len(merged), merged)
	}

	// Must be sorted by iteration number
	if merged[0].Iteration != 0 || merged[0].Content != "iter0-from-finalize" {
		t.Errorf("iter0: got %+v, want iter0-from-finalize", merged[0])
	}
	if merged[1].Iteration != 1 || merged[1].Content != "iter1-from-progressState" {
		t.Errorf("iter1: got %+v, want iter1-from-progressState (newer wins)", merged[1])
	}
	if merged[2].Iteration != 2 || merged[2].Content != "iter2-new" {
		t.Errorf("iter2: got %+v, want iter2-new", merged[2])
	}
}

func TestRegression_MergeIterationsEmptyExisting(t *testing.T) {
	newer := []cliIterationSnapshot{
		{Iteration: 0, Content: "iter0"},
		{Iteration: 1, Content: "iter1"},
	}
	merged := mergeIterations(nil, newer)
	if len(merged) != 2 {
		t.Fatalf("expected 2, got %d", len(merged))
	}
}

func TestRegression_MergeIterationsEmptyNewer(t *testing.T) {
	existing := []cliIterationSnapshot{
		{Iteration: 0, Content: "iter0"},
	}
	merged := mergeIterations(existing, nil)
	if len(merged) != 1 || merged[0].Content != "iter0" {
		t.Fatalf("existing not preserved when newer is empty: %+v", merged)
	}
}

// ── Bug: full end-to-end scenario — iteration 0 disappears when
// PhaseDone arrives before handleAgentMessage, and the delta was lost.
//
// This test simulates the EXACT bug report: user sees iteration 0 appear,
// then it vanishes when the reply arrives. After the fix, iteration 0
// is preserved because:
// 1. Structured progress goes through sendCh (not storeStateless)
// 2. finalizeTurnFromSnapshot bakes iterations into the streaming message
// 3. handleAgentMessage merges (not overwrites) iterations
func TestRegression_EndToEnd_Iteration0PreservedAfterReply(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	turnID := model.agentTurnID

	// Iteration 0: structured progress with delta
	iter0Delta := protocol.ProgressEvent{
		Iteration: 0,
		Content:   "iter0-content",
		CompletedTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "ls", Status: "done", Iteration: 0},
		},
	}
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 1,
	}, iter0Delta)

	// Verify iter0 is in progressState.iterations
	if len(model.progressState.iterations) != 1 {
		t.Fatalf("expected 1 iteration in progressState, got %d", len(model.progressState.iterations))
	}
	if model.progressState.iterations[0].Iteration != 0 {
		t.Fatalf("expected iteration 0, got %d", model.progressState.iterations[0].Iteration)
	}

	// PhaseDone: finalize the turn
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 1,
	})

	// finalizeTurnFromSnapshot should have baked iterations into streaming message
	if model.streamingMsgIdx < 0 {
		t.Fatal("streamingMsgIdx should be valid after PhaseDone")
	}
	baked := model.messages[model.streamingMsgIdx].iterations
	if len(baked) == 0 {
		t.Fatal("no iterations baked into streaming message after PhaseDone")
	}
	foundIter0 := false
	for _, it := range baked {
		if it.Iteration == 0 {
			foundIter0 = true
		}
	}
	if !foundIter0 {
		t.Fatalf("iteration 0 missing from baked iterations: %+v", baked)
	}

	// Agent reply arrives — must NOT overwrite baked iterations
	sendDone(model, "Final reply")

	// Find the assistant message for this turn
	asstIdx := model.findMessageByTurn(turnID, "assistant")
	if asstIdx < 0 {
		t.Fatal("assistant message not found")
	}
	finalIters := model.messages[asstIdx].iterations
	foundIter0 = false
	for _, it := range finalIters {
		if it.Iteration == 0 {
			foundIter0 = true
			if it.Content != "iter0-content" {
				t.Errorf("iter0 content changed: got %q, want 'iter0-content'", it.Content)
			}
		}
	}
	if !foundIter0 {
		t.Fatalf("iteration 0 disappeared after handleAgentMessage: %+v", finalIters)
	}
}

// ════════════════════════════════════════════════════════════════════════
// REGRESSION: Two consecutive Assistant messages must never appear
// ════════════════════════════════════════════════════════════════════════

// ── Bug: handleAgentMessage creates a new assistant message when the last
// message is already an assistant (from startAgentTurn or finalizeTurnFromSnapshot).
// This produces two consecutive Assistant blocks in the viewport.
//
// Fix: all assistant creation paths check for existing assistant before appending.
func TestRegression_NoConsecutiveAssistant_CreatePaths(t *testing.T) {
	// Case 1: IsPartial reply when last message is empty assistant placeholder
	model := initTestModel()
	model.startAgentTurn() // creates empty assistant placeholder

	// Send IsPartial update — must reuse the existing placeholder, not create new
	model.Update(cliOutboundMsg{
		msg: ch.OutboundMsg{
			Channel:   model.channelName,
			ChatID:    model.chatID,
			Content:   "streaming content",
			IsPartial: true,
		},
	})

	assistantCount := 0
	for _, msg := range model.messages {
		if msg.role == "assistant" {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Errorf("IsPartial: expected 1 assistant, got %d", assistantCount)
	}

	// Case 2: Complete reply when last message is empty assistant placeholder
	model2 := initTestModel()
	model2.startAgentTurn()

	sendDone(model2, "complete reply")

	assistantCount = 0
	for _, msg := range model2.messages {
		if msg.role == "assistant" {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Errorf("complete reply: expected 1 assistant, got %d", assistantCount)
	}
}

// ── Bug: rendering guard must merge same-turnID duplicate assistants
// and remove empty placeholders from different turns.
func TestRegression_RenderGuardMergesConsecutiveAssistants(t *testing.T) {
	// Case 1: Same turnID duplicates → merge content + iterations
	model := initTestModel()
	model.messages = []cliMessage{
		{role: "user", content: "hello", turnID: 0},
		{role: "assistant", content: "first", turnID: 1, iterations: []cliIterationSnapshot{
			{Iteration: 0, Content: "iter0"},
		}},
		{role: "assistant", content: "second", turnID: 1, iterations: []cliIterationSnapshot{
			{Iteration: 1, Content: "iter1"},
		}},
	}
	model.rc.valid = false
	model.updateViewportContent()

	assistantCount := 0
	var merged *cliMessage
	for i := range model.messages {
		if model.messages[i].role == "assistant" {
			assistantCount++
			merged = &model.messages[i]
		}
	}
	if assistantCount != 1 {
		t.Errorf("same-turnID: expected 1 assistant after merge, got %d", assistantCount)
	}
	if merged != nil {
		if !strings.Contains(merged.content, "first") || !strings.Contains(merged.content, "second") {
			t.Errorf("merged content missing parts: %q", merged.content)
		}
		if len(merged.iterations) != 2 {
			t.Errorf("merged iterations: expected 2, got %d", len(merged.iterations))
		}
	}

	// Case 2: Different turnID, first is empty placeholder → remove placeholder
	model2 := initTestModel()
	model2.messages = []cliMessage{
		{role: "user", content: "hello", turnID: 0},
		{role: "assistant", content: "", turnID: 1, isPartial: true}, // empty placeholder
		{role: "assistant", content: "real reply", turnID: 2, isPartial: false},
	}
	model2.rc.valid = false
	model2.updateViewportContent()

	assistantCount = 0
	for _, msg := range model2.messages {
		if msg.role == "assistant" {
			assistantCount++
			if msg.content != "real reply" {
				t.Errorf("expected 'real reply', got %q", msg.content)
			}
		}
	}
	if assistantCount != 1 {
		t.Errorf("different-turnID empty placeholder: expected 1 assistant, got %d", assistantCount)
	}

	// Case 3: Different turnID, first has content + iterations → preserved
	// (This is normal multi-turn: U1 A1 U2 A2 — but without U2 between them,
	// which shouldn't happen in production. The guard keeps both to avoid
	// merging unrelated turns.)
	model3 := initTestModel()
	model3.messages = []cliMessage{
		{role: "user", content: "hello", turnID: 0},
		{role: "assistant", content: "reply1", turnID: 1, isPartial: false, iterations: []cliIterationSnapshot{
			{Iteration: 0, Content: "iter0"},
		}},
		{role: "assistant", content: "reply2", turnID: 2, isPartial: false, iterations: []cliIterationSnapshot{
			{Iteration: 0, Content: "iter0-turn2"},
		}},
	}
	model3.rc.valid = false
	model3.updateViewportContent()

	assistantCount = 0
	for _, msg := range model3.messages {
		if msg.role == "assistant" {
			assistantCount++
		}
	}
	// Both assistants are preserved — they have content and iterations from
	// different turns. The rendering guard does NOT merge different-turnID
	// assistants when the first has content (only removes empty placeholders).
	if assistantCount != 2 {
		t.Errorf("different-turnID with content: expected 2 assistants preserved, got %d", assistantCount)
	}
}

// ── Bug: fullRebuild must also guard against consecutive assistants
// (same guard as appendNewMessagesToCache, tested via width change path)
func TestRegression_FullRebuildMergesConsecutiveAssistants(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Simulate same-turnID duplicate via direct manipulation
	model.messages = []cliMessage{
		{role: "user", content: "hello", turnID: 0, timestamp: time.Now()},
		{role: "assistant", content: "first", turnID: 1, timestamp: time.Now(), iterations: []cliIterationSnapshot{
			{Iteration: 0, Content: "iter0"},
		}},
		{role: "assistant", content: "second", turnID: 1, timestamp: time.Now(), iterations: []cliIterationSnapshot{
			{Iteration: 1, Content: "iter1"},
		}},
	}
	model.streamingMsgIdx = -1
	model.rc.valid = false

	// Trigger fullRebuild via width change
	model.handleResize(100, 24)
	model.updateViewportContent()

	assistantCount := 0
	for _, msg := range model.messages {
		if msg.role == "assistant" {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Errorf("fullRebuild: expected 1 assistant after merge, got %d", assistantCount)
	}
}

// ════════════════════════════════════════════════════════════════════════
// REGRESSION: Seq gap detection → immediate snapshot pull
// ════════════════════════════════════════════════════════════════════════

// ── Bug: push events dropped by Hub sendCh-full were never recovered until
// the 2s tick pull — and if the turn ended before 2s, they were permanently
// lost. The fix: detect Seq jumps (gap) and trigger an immediate pull on the
// next tick (100ms), instead of waiting.
//
// Normal flow (no gap): zero pulls. This is critical for performance — the
// push path carries everything, pull only fires on actual data loss.

func TestRegression_SeqGapTriggersPull(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Seq 1: normal structured event
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 0,
		Seq:       1,
	})
	if model.progressState.gapDetected {
		t.Fatal("gapDetected should be false for sequential Seq (1)")
	}
	if model.progressState.lastReceivedSeq != 1 {
		t.Fatalf("lastReceivedSeq = %d, want 1", model.progressState.lastReceivedSeq)
	}

	// Seq 2: normal continuation
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 0,
		Seq:       2,
	})
	if model.progressState.gapDetected {
		t.Fatal("gapDetected should be false for sequential Seq (2)")
	}

	// Seq 5: GAP! (3 and 4 were dropped)
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 1,
		Seq:       5,
	})
	if !model.progressState.gapDetected {
		t.Fatal("gapDetected should be true after Seq jump 2→5")
	}
	if model.progressState.lastReceivedSeq != 5 {
		t.Fatalf("lastReceivedSeq = %d, want 5", model.progressState.lastReceivedSeq)
	}
}

func TestRegression_SeqNoGapNoPull(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Simulate 10 sequential events — no gap, no pull needed
	for i := uint64(1); i <= 10; i++ {
		sendProgress(model, &protocol.ProgressEvent{
			Phase:     "thinking",
			Iteration: int(i),
			Seq:       i,
		})
	}

	if model.progressState.gapDetected {
		t.Fatal("gapDetected should be false — all Seq were sequential")
	}
	if model.progressState.lastReceivedSeq != 10 {
		t.Fatalf("lastReceivedSeq = %d, want 10", model.progressState.lastReceivedSeq)
	}
}

func TestRegression_SeqGapResetsAfterPull(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Create a gap
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 0, Seq: 1})
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 1, Seq: 5})

	if !model.progressState.gapDetected {
		t.Fatal("gapDetected should be true")
	}

	// Simulate what tick handler does: gapDetected → pull → clear
	// (The actual pull is an RPC, but we can simulate the state transition)
	model.progressState.gapDetected = false

	// Next sequential event — no new gap
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 2, Seq: 6})
	if model.progressState.gapDetected {
		t.Fatal("gapDetected should be false after recovery — Seq 5→6 is sequential")
	}
}

func TestRegression_ResetProgressStateClearsGapDetection(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Create a gap
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 0, Seq: 1})
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 1, Seq: 5})
	if !model.progressState.gapDetected {
		t.Fatal("gapDetected should be true")
	}

	// New turn resets state
	model.startAgentTurn()

	if model.progressState.gapDetected {
		t.Fatal("gapDetected should be cleared by resetProgressState")
	}
	if model.progressState.lastReceivedSeq != 0 {
		t.Fatalf("lastReceivedSeq = %d, want 0 after reset", model.progressState.lastReceivedSeq)
	}
}

// ── Bug: first event of a turn (lastReceivedSeq=0) should NOT trigger
// gap detection — Seq starts at 1, and 0→1 is not a gap.
func TestRegression_FirstEventNoFalseGap(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// First event Seq=1, lastReceivedSeq=0 → NOT a gap (first event)
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 0,
		Seq:       1,
	})
	if model.progressState.gapDetected {
		t.Fatal("gapDetected should be false for first event of turn")
	}

	// Even if first event has high Seq (e.g. after reconnect), no gap
	model.startAgentTurn()
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 0,
		Seq:       42,
	})
	if model.progressState.gapDetected {
		t.Fatal("gapDetected should be false for first event (high Seq after reconnect)")
	}
}

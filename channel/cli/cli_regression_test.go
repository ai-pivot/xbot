package cli

// Regression tests for TUI rendering fixes on investigate/tui-turn-flicker branch.
//
// Fixes covered:
// 1. Turn completion flicker: endAgentTurn preserves streamingMsgIdx + progress state
// 2. Duplicate text rendering: renderTurnBody dedup checks all iterations
// 3. Queued tools visible: liveIterationBlocks includes "pending" status
// 4. Reasoning during tool exec: carryForwardProgressState carries ReasoningStreamContent
// 5. Ctrl+C + system notification: handleInjectedUserMsg queues when turnCancelled

import (
	"strings"
	"testing"

	"xbot/channel"
	"xbot/protocol"
)

// ─── Fix 1: endAgentTurn preserves streamingMsgIdx ──────────────────

// TestEndAgentTurn_PreservesStreamingMsgIdx verifies that endAgentTurn
// does NOT clear streamingMsgIdx. Clearing it causes the tick handler
// to fall through to appendNewMessagesToCache (caching incomplete content),
// then handleAgentMessage re-renders → double-flicker.
func TestEndAgentTurn_PreservesStreamingMsgIdx(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	turnID := model.agentTurnID

	// Simulate some progress + tool execution
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Read", Label: "main.go", Status: "running", Iteration: 1},
		},
	})
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		CompletedTools: []protocol.ToolProgress{
			{Name: "Read", Label: "main.go", Status: "done", Elapsed: 100, Iteration: 1},
		},
	})

	// PhaseDone arrives → endAgentTurn is called
	sendProgress(model, &protocol.ProgressEvent{
		Phase:          "done",
		Iteration:      1,
		CompletedTools: []protocol.ToolProgress{{Name: "Read", Label: "main.go", Status: "done", Elapsed: 100, Iteration: 1}},
	})

	// streamingMsgIdx must still be valid (NOT -1)
	if model.streamingMsgIdx < 0 {
		t.Error("streamingMsgIdx should be preserved after endAgentTurn, got -1")
	}
	if model.streamingMsgIdx >= len(model.messages) {
		t.Errorf("streamingMsgIdx %d out of bounds (len=%d)", model.streamingMsgIdx, len(model.messages))
	}
	if model.streamingMsgIdx >= 0 && model.messages[model.streamingMsgIdx].turnID != turnID {
		t.Errorf("streamingMsgIdx points to wrong turnID: got %d, want %d",
			model.messages[model.streamingMsgIdx].turnID, turnID)
	}
}

// TestEndAgentTurn_PreservesProgressState verifies that endAgentTurn
// does NOT clear progressState.iterations or progressState.current.
// These are needed by updateStreamingOnly to render the turn's final
// state between PhaseDone and handleAgentMessage.
func TestEndAgentTurn_PreservesProgressState(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "go test", Status: "running", Iteration: 1},
		},
	})
	sendProgress(model, &protocol.ProgressEvent{
		Phase:          "done",
		Iteration:      1,
		CompletedTools: []protocol.ToolProgress{{Name: "Shell", Label: "go test", Status: "done", Elapsed: 200, Iteration: 1}},
	})

	// After endAgentTurn (called by handleProgressDone), progress state
	// must still be populated for flicker-free rendering.
	if len(model.progressState.iterations) == 0 {
		t.Error("progressState.iterations should be preserved after endAgentTurn")
	}
	if model.progressState.current == nil {
		t.Error("progressState.current should be preserved after endAgentTurn")
	}
	// typing should be false (turn ended)
	if model.typing {
		t.Error("typing should be false after endAgentTurn")
	}
}

// ─── Fix 2: renderTurnBody dedup checks all iterations ──────────────

// TestRenderTurnBody_DedupExactMatchAllIterations verifies that
// fallbackContent is not duplicated when it exactly matches an EARLIER
// iteration's Thinking. The old code only checked the last iteration.
//
// Root cause: iter.Thinking carries the assistant's reply text
// (StructuredProgress.ThinkingContent = response text, NOT reasoning).
// fallbackContent (msg.content) is the same text from a different path.
// Exact match dedup prevents the double render.
func TestRenderTurnBody_DedupExactMatchAllIterations(t *testing.T) {
	model := initTestModel()
	model.ticker.frame = 0

	iterations := []cliIterationSnapshot{
		{
			Iteration: 1,
			Thinking:  "Let me analyze the code",
			Tools: []protocol.ToolProgress{
				{Name: "Read", Label: "main.go", Status: "done", Elapsed: 50, Iteration: 1},
			},
		},
		{
			Iteration: 2,
			// No Thinking in iteration 2 — only tools
			Tools: []protocol.ToolProgress{
				{Name: "Grep", Label: "pattern", Status: "done", Elapsed: 30, Iteration: 2},
			},
		},
	}

	fallback := "Let me analyze the code"
	body := model.renderTurnBody(iterations, nil, 80, fallback)
	clean := stripAnsi(body)

	count := strings.Count(clean, "Let me analyze the code")
	if count != 1 {
		t.Errorf("exact match should dedup to 1 occurrence, got %d:\n%s", count, clean)
	}
}

// TestRenderTurnBody_DifferentTextNotSuppressed verifies that when
// fallbackContent differs from iter.Thinking (e.g. LLM added more text
// after tools), the fallback is rendered normally.
func TestRenderTurnBody_DifferentTextNotSuppressed(t *testing.T) {
	model := initTestModel()
	model.ticker.frame = 0

	iterations := []cliIterationSnapshot{
		{
			Iteration: 1,
			Thinking:  "I will now",
		},
	}

	fallback := "I will now analyze the code and provide a fix"
	body := model.renderTurnBody(iterations, nil, 80, fallback)
	clean := stripAnsi(body)

	// The full fallback must be rendered — short Thinking must not suppress it
	if !strings.Contains(clean, "I will now analyze the code and provide a fix") {
		t.Errorf("fallback should be rendered (different from Thinking):\n%s", clean)
	}
}

// ─── Fix 6: Tools lost due to progressCh coalescing ─────────────────

// TestSnapshotIterationChange_CoalescedToolsNotLost verifies that tools
// are not lost when the "tools done" progress event is dropped by
// progressCh coalescing. The engine guarantees all tools are done before
// starting the next iteration, so ActiveTools with stale "running" status
// must still be captured in the snapshot.
func TestSnapshotIterationChange_CoalescedToolsNotLost(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Iteration 1: tools running (the "done" event will be coalesced away)
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Read", Label: "main.go", Status: "running", Iteration: 1},
			{Name: "Grep", Label: "pattern", Status: "running", Iteration: 1},
		},
	})

	// Iteration 2 arrives WITHOUT a "tools done" event (coalesced away).
	// prev has ActiveTools with "running" status — but the engine has
	// actually completed them (snapshotCompletedIteration ran before callLLM).
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 2,
		Reasoning: "Now I see the issue...",
	})

	// The snapshot should contain BOTH tools despite "running" status.
	if len(model.progressState.iterations) == 0 {
		t.Fatal("should have at least 1 snapshotted iteration")
	}
	snap := model.progressState.iterations[0]
	if len(snap.Tools) != 2 {
		t.Errorf("should have 2 tools in snapshot (coalescing must not lose them), got %d", len(snap.Tools))
	}
	// Verify both tool labels are present
	labels := make(map[string]bool)
	for _, tool := range snap.Tools {
		labels[tool.Label] = true
	}
	if !labels["main.go"] {
		t.Error("tool 'main.go' missing from snapshot")
	}
	if !labels["pattern"] {
		t.Error("tool 'pattern' missing from snapshot")
	}
}

// TestSnapshotIterationChange_PendingToolsCaptured verifies that pending
// (queued) tools are also captured when the iteration changes.
func TestSnapshotIterationChange_PendingToolsCaptured(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Iteration 1: one tool running, one pending
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Read", Label: "f1.go", Status: "running", Iteration: 1},
			{Name: "Grep", Label: "pat", Status: "pending", Iteration: 1},
		},
	})

	// Iteration 2: both tools are done (engine completed them),
	// but the "done" event was coalesced away.
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 2,
	})

	if len(model.progressState.iterations) == 0 {
		t.Fatal("should have 1 snapshotted iteration")
	}
	snap := model.progressState.iterations[0]
	if len(snap.Tools) != 2 {
		t.Errorf("should have 2 tools (running + pending both captured), got %d", len(snap.Tools))
	}
}

// ─── Fix 7: HistoryCompacted handler must return early ─────────────

// TestHistoryCompacted_NoStaleSnapshot verifies that the HistoryCompacted
// handler returns early, preventing snapshotIterationChange from creating
// a stale snapshot from pre-compression prev data.
func TestHistoryCompacted_NoStaleSnapshot(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Build up some iteration history
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Read", Label: "f.go", Status: "running", Iteration: 1},
		},
	})
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 2,
	})

	preCompressCount := len(model.progressState.iterations)
	if preCompressCount == 0 {
		t.Fatal("should have iterations before compression")
	}

	// HistoryCompacted event
	sendProgress(model, &protocol.ProgressEvent{
		Phase:            "thinking",
		Iteration:        2,
		HistoryCompacted: true,
	})

	// After HistoryCompacted, iterations should be cleared (handler sets nil)
	// and NO stale snapshot should be created from prev
	if len(model.progressState.iterations) != 0 {
		t.Errorf("iterations should be cleared after HistoryCompacted, got %d (stale snapshot leaked)",
			len(model.progressState.iterations))
	}
	// lastIter should be 0 (cleared by handler)
	if model.progressState.lastIter != 0 {
		t.Errorf("lastIter should be 0 after HistoryCompacted, got %d", model.progressState.lastIter)
	}
}

// ─── Fix 8: Busy state must use typing flag, not progressState.current ─

// TestTurnComplete_ReturnsToIdle verifies that after endAgentTurn,
// the TUI returns to idle state (typing=false). progressState.current
// is preserved for flicker-free rendering but must NOT keep the TUI
// in busy state. The status bar, tick handler, and update-notice
// suppression must all use m.typing as the sole busy indicator.
func TestTurnComplete_ReturnsToIdle(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Simulate progress
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Read", Label: "f.go", Status: "running", Iteration: 1},
		},
	})

	// Turn is busy
	if !model.typing {
		t.Error("typing should be true during turn")
	}

	// PhaseDone → endAgentTurn
	sendProgress(model, &protocol.ProgressEvent{
		Phase:          "done",
		Iteration:      1,
		CompletedTools: []protocol.ToolProgress{{Name: "Read", Label: "f.go", Status: "done", Elapsed: 100, Iteration: 1}},
	})

	// After endAgentTurn: typing must be false (idle)
	if model.typing {
		t.Error("typing should be false after endAgentTurn (must return to idle)")
	}

	// progressState.current is preserved for rendering (flicker-free),
	// but must NOT affect busy state. The status bar checks typing only.
	// Verify the tick handler's busy calculation:
	// busy := m.typing (NOT m.typing || progressState.current != nil)
	busy := model.typing
	if busy {
		t.Error("tick handler busy must be false when typing=false, even if progressState.current is preserved")
	}
}

// ─── Fix 9: Auto-start + injected message race → two assistants ────

// TestInjectedUserMsg_ClaimsAutoStartedTurn verifies that when a progress
// auto-start creates a streaming message (before the injected user message
// arrives via asyncCh), handleInjectedUserMsg claims the turn by inserting
// the user message before the streaming message — NOT queuing (which would
// produce a second assistant when flushed).
func TestInjectedUserMsg_ClaimsAutoStartedTurn(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn() // simulates auto-start (progress arrived first)
	model.turnAutoStarted = true
	oldTurnID := model.agentTurnID
	oldStreamingIdx := model.streamingMsgIdx

	// Injected user message arrives AFTER auto-start
	model.Update(cliInjectedUserMsg{
		content: "[System] Task done",
		chatID:  model.channelName + ":" + model.chatID,
	})

	// The notification should NOT be queued
	if len(model.messageQueue) != 0 {
		t.Error("notification should NOT be queued when turn was auto-started")
	}

	// turnAutoStarted should be cleared
	if model.turnAutoStarted {
		t.Error("turnAutoStarted should be cleared after claiming turn")
	}

	// agentTurnID should NOT change (same turn)
	if model.agentTurnID != oldTurnID {
		t.Errorf("agentTurnID should not change: got %d, want %d", model.agentTurnID, oldTurnID)
	}

	// streamingMsgIdx should be shifted by 1 (user message inserted before it)
	if model.streamingMsgIdx != oldStreamingIdx+1 {
		t.Errorf("streamingMsgIdx should shift +1: got %d, want %d", model.streamingMsgIdx, oldStreamingIdx+1)
	}

	// User message should be immediately before the streaming message
	if model.streamingMsgIdx < 1 {
		t.Fatal("streamingMsgIdx too small")
	}
	userMsg := model.messages[model.streamingMsgIdx-1]
	if userMsg.role != "user" {
		t.Errorf("message before streaming should be 'user', got %q", userMsg.role)
	}

	// Should have exactly 1 assistant message (the streaming one from auto-start)
	assistantCount := 0
	for _, msg := range model.messages {
		if msg.role == "assistant" {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Errorf("should have exactly 1 assistant, got %d", assistantCount)
	}
}

// TestInjectedUserMsg_QueuesWhenTypingFromRealUser verifies that when
// typing=true was set by a real user message (not auto-start), the
// injected message IS queued (correct behavior).
func TestInjectedUserMsg_QueuesWhenTypingFromRealUser(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.turnAutoStarted = false // explicitly false (real user message turn)

	model.Update(cliInjectedUserMsg{
		content: "[System] Task done",
		chatID:  model.channelName + ":" + model.chatID,
	})

	if len(model.messageQueue) == 0 {
		t.Error("notification should be queued when typing=true from real user message")
	}
}

// ─── Fix 3: Queued (pending) tools visible ──────────────────────────

// TestLiveIterationBlocks_PendingToolsVisible verifies that tools with
// Status="pending" (queued, waiting for serial predecessor) are rendered.
// Without this fix, 3 generating tools → 1 visible when execution starts
// → 2 → 3 (visual oscillation).
func TestLiveIterationBlocks_PendingToolsVisible(t *testing.T) {
	model := initTestModel()
	model.ticker.frame = 0

	// Simulate: tool[0] running, tool[1] and tool[2] pending
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Read", Label: "f1.go", Status: "running", Iteration: 1},
			{Name: "Grep", Label: "pattern", Status: "pending", Iteration: 1},
			{Name: "Shell", Label: "go test", Status: "pending", Iteration: 1},
		},
	}

	blocks := model.liveIterationBlocks(model.progressState.current, 80, "")
	rendered := stripAnsi(renderTurnBlocks(blocks))

	// All three tools must be visible (rendered by label)
	if !strings.Contains(rendered, "f1.go") {
		t.Errorf("running tool 'f1.go' missing from render:\n%s", rendered)
	}
	if !strings.Contains(rendered, "pattern") {
		t.Errorf("pending tool 'pattern' missing from render:\n%s", rendered)
	}
	if !strings.Contains(rendered, "go test") {
		t.Errorf("pending tool 'go test' missing from render:\n%s", rendered)
	}

	// Pending tools should have the "queued" label
	if !strings.Contains(rendered, "queued") {
		t.Errorf("pending tools should show 'queued' label:\n%s", rendered)
	}

	// Pending tools should use the ○ symbol (not ● for running)
	if !strings.Contains(rendered, "○") {
		t.Errorf("pending tools should use ○ symbol:\n%s", rendered)
	}
}

// TestRenderLiveToolTags_PendingStyle verifies the visual distinction
// between running and pending tools.
func TestRenderLiveToolTags_PendingStyle(t *testing.T) {
	model := initTestModel()
	model.ticker.frame = 0

	tools := []protocol.ToolProgress{
		{Name: "Read", Label: "f1.go", Status: "running", Iteration: 1},
		{Name: "Grep", Label: "pattern", Status: "pending", Iteration: 1},
	}

	rendered := model.renderLiveToolTags(tools, 80)

	// Running tool: should have ● (orbit animation frame, but we check the label)
	if !strings.Contains(rendered, "f1.go") {
		t.Errorf("running tool label missing:\n%s", rendered)
	}
	// Pending tool: should have ○ and "queued"
	if !strings.Contains(rendered, "○") {
		t.Errorf("pending tool should use ○ symbol:\n%s", rendered)
	}
	if !strings.Contains(rendered, "queued") {
		t.Errorf("pending tool should show 'queued':\n%s", rendered)
	}
}

// ─── Fix 4: ReasoningStreamContent carried forward during tool_exec ─

// TestCarryForward_ReasoningStreamContent_ToolExec verifies that
// ReasoningStreamContent is carried forward unconditionally during
// tool_exec phase, even when prev.StreamContent is non-empty.
func TestCarryForward_ReasoningStreamContent_ToolExec(t *testing.T) {
	model := initTestModel()

	// Previous progress: both StreamContent and ReasoningStreamContent set
	prev := &protocol.ProgressEvent{
		Phase:                  "thinking",
		Iteration:              1,
		StreamContent:          "Here is my answer",
		ReasoningStreamContent: "I need to think about this carefully",
	}
	model.progressState.current = prev

	// New structured event: tool_exec phase, no stream fields
	newPayload := &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Read", Status: "running", Iteration: 1},
		},
	}
	model.progressState.current = newPayload
	model.carryForwardProgressState(prev)

	if model.progressState.current.ReasoningStreamContent == "" {
		t.Error("ReasoningStreamContent should be carried forward during tool_exec even when prev.StreamContent is non-empty")
	}
	expected := "I need to think about this carefully"
	if model.progressState.current.ReasoningStreamContent != expected {
		t.Errorf("ReasoningStreamContent = %q, want %q",
			model.progressState.current.ReasoningStreamContent, expected)
	}
}

// TestCarryForward_ReasoningStreamContent_StreamingGuard verifies that
// during streaming phase (NOT tool_exec), the original guard is preserved:
// ReasoningStreamContent is NOT carried forward when prev.StreamContent
// is non-empty.
func TestCarryForward_ReasoningStreamContent_StreamingGuard(t *testing.T) {
	model := initTestModel()

	prev := &protocol.ProgressEvent{
		Phase:                  "thinking",
		Iteration:              1,
		StreamContent:          "Here is my answer",
		ReasoningStreamContent: "My reasoning process",
	}
	model.progressState.current = prev

	// New event: still thinking/streaming phase
	newPayload := &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 1,
	}
	model.progressState.current = newPayload
	model.carryForwardProgressState(prev)

	// During streaming (Phase != tool_exec), guard is active:
	// ReasoningStreamContent should NOT be carried forward
	if model.progressState.current.ReasoningStreamContent != "" {
		t.Error("ReasoningStreamContent should NOT be carried forward during streaming when prev.StreamContent is non-empty")
	}
}

// ─── Fix 5: Ctrl+C + system notification → two assistants ───────────

// TestCtrlC_NotificationQueuesDuringCancel verifies that when turnCancelled=true
// and a system notification (injected user message) arrives, it is QUEUED
// rather than starting a new turn. This prevents the double-assistant bug
// where the cancelled turn's streaming message becomes an orphan.
func TestCtrlC_NotificationQueuesDuringCancel(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	oldTurnID := model.agentTurnID
	oldStreamingIdx := model.streamingMsgIdx

	// Simulate some progress
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "cargo build", Status: "running", Iteration: 1},
		},
	})

	// User presses Ctrl+C
	model.cancelTargetTurnID = oldTurnID
	model.turnCancelled = true

	// PhaseDone(cancel) arrives → endAgentTurn runs → typing=false
	sendProgress(model, &protocol.ProgressEvent{
		Phase:          "done",
		Iteration:      1,
		CompletedTools: []protocol.ToolProgress{{Name: "Shell", Label: "cargo build", Status: "done", Elapsed: 100, Iteration: 1}},
	})

	// typing is now false, but turnCancelled is still true
	if model.typing {
		t.Error("typing should be false after PhaseDone")
	}
	if !model.turnCancelled {
		t.Error("turnCancelled should still be true (cancel ack not yet received)")
	}

	// System notification arrives BEFORE cancel ack
	model.Update(cliInjectedUserMsg{
		content: "[System Notification] Background task completed.",
		chatID:  model.channelName + ":" + model.chatID,
	})

	// The notification should be QUEUED, not start a new turn
	if model.agentTurnID != oldTurnID {
		t.Errorf("agentTurnID should NOT increment during cancel window: got %d, want %d",
			model.agentTurnID, oldTurnID)
	}
	if len(model.messageQueue) == 0 {
		t.Error("notification should be queued, but messageQueue is empty")
	}

	// streamingMsgIdx should still point to the cancelled turn's message
	// (not a new turn's message)
	if model.streamingMsgIdx != oldStreamingIdx {
		t.Errorf("streamingMsgIdx should not change: got %d, want %d",
			model.streamingMsgIdx, oldStreamingIdx)
	}

	// Count assistant messages — should be exactly 1 (the cancelled turn's streaming msg)
	assistantCount := 0
	for _, msg := range model.messages {
		if msg.role == "assistant" {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Errorf("should have exactly 1 assistant message, got %d", assistantCount)
	}
}

// TestCtrlC_CancelAckFlushesQueuedNotification verifies that after the
// cancel ack arrives, the queued notification is flushed and starts a
// new turn — producing exactly one new assistant message.
func TestCtrlC_CancelAckFlushesQueuedNotification(t *testing.T) {
	model := initTestModel()
	// Set up a no-op sendInboundFn so sendMessage doesn't early-return
	model.sendInboundFn = func(msg channel.InboundMsg) bool { return true }
	model.startAgentTurn()
	oldTurnID := model.agentTurnID

	// Give the turn some progress so the cancelled streaming message
	// has iterations and is preserved (not removed) by handleCancelAck.
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "cargo build", Status: "running", Iteration: 1},
		},
	})
	sendProgress(model, &protocol.ProgressEvent{
		Phase:          "done",
		Iteration:      1,
		CompletedTools: []protocol.ToolProgress{{Name: "Shell", Label: "cargo build", Status: "done", Elapsed: 200, Iteration: 1}},
	})

	// Ctrl+C
	model.cancelTargetTurnID = oldTurnID
	model.turnCancelled = true
	// Re-send PhaseDone with cancel path (already has iterations from above)
	sendProgress(model, &protocol.ProgressEvent{
		Phase:          "done",
		Iteration:      1,
		CompletedTools: []protocol.ToolProgress{{Name: "Shell", Label: "cargo build", Status: "done", Elapsed: 200, Iteration: 1}},
	})

	// Notification queued during cancel window
	model.Update(cliInjectedUserMsg{
		content: "[System] Task done",
		chatID:  model.channelName + ":" + model.chatID,
	})
	if len(model.messageQueue) == 0 {
		t.Fatal("notification should be queued")
	}

	// Cancel ack arrives
	model.Update(cliOutboundMsg{
		msg: channel.OutboundMsg{
			Content:  "",
			Metadata: map[string]string{"cancelled": "true"},
		},
	})

	// After cancel ack: turnCancelled cleared, typing false
	if model.turnCancelled {
		t.Error("turnCancelled should be cleared after cancel ack")
	}

	// needFlushQueue should be true (tryFlushMessageQueue was called)
	if !model.needFlushQueue {
		t.Error("needFlushQueue should be true after cancel ack with queued messages")
	}

	// Simulate the tick-driven queue flush: mark old turn as reply-received
	// (cancel ack counts as reply), then flush.
	model.setTurnReplyReceived(oldTurnID)
	model.needFlushQueue = false
	model.flushMessageQueue()

	// New turn should have started (agentTurnID incremented by sendMessage → startAgentTurn)
	if model.agentTurnID == oldTurnID {
		t.Error("agentTurnID should increment after queue flush")
	}

	// Should have exactly 2 assistant messages: old (cancelled) + new turn
	assistantCount := 0
	for _, msg := range model.messages {
		if msg.role == "assistant" {
			assistantCount++
		}
	}
	if assistantCount != 2 {
		t.Errorf("should have 2 assistant messages (old cancelled + new turn), got %d", assistantCount)
	}
}

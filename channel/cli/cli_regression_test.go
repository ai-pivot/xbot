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

// TestRenderTurnBody_DedupAllIterations verifies that fallbackContent
// is not duplicated when it matches an EARLIER iteration's Thinking
// (not just the last one). The old code only checked the last iteration,
// causing text duplication when the agent generated text in iteration 1
// then called tools in later iterations.
func TestRenderTurnBody_DedupAllIterations(t *testing.T) {
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

	// fallbackContent matches iteration 1's Thinking exactly
	fallback := "Let me analyze the code"
	body := model.renderTurnBody(iterations, nil, 80, fallback)
	clean := stripAnsi(body)

	// The text should appear exactly ONCE, not twice
	count := strings.Count(clean, "Let me analyze the code")
	if count != 1 {
		t.Errorf("fallbackContent should appear exactly once (dedup), got %d times:\n%s", count, clean)
	}
}

// TestRenderTurnBody_DedupPrefixMatch verifies that prefix matching
// catches cases where fallbackContent has extra trailing text (e.g.
// error messages appended after the original reply).
func TestRenderTurnBody_DedupPrefixMatch(t *testing.T) {
	model := initTestModel()
	model.ticker.frame = 0

	iterations := []cliIterationSnapshot{
		{
			Iteration: 1,
			Thinking:  "Analysis complete",
		},
	}

	// fallbackContent has the iteration text plus extra trailing text
	fallback := "Analysis complete\n\nNote: additional info"
	body := model.renderTurnBody(iterations, nil, 80, fallback)

	// "Analysis complete" should NOT be duplicated
	count := strings.Count(body, "Analysis complete")
	if count > 1 {
		t.Errorf("'Analysis complete' should not be duplicated (prefix match), got %d:\n%s", count, body)
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

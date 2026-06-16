package cli

import (
	"strings"
	"testing"
	"time"
	"xbot/channel"
	"xbot/protocol"
)

// TestCtrlC_UserMsgPreserved verifies that after Ctrl+C, the user message
// remains visible in the viewport. The race condition occurs when:
// 1. PhaseDone arrives after cancel (turnCancelled=true)
// 2. endAgentTurn resets turnCancelled=false
// 3. A stale progress event triggers auto-start turn
// 4. This creates a new agentTurnID, confusing the cancel ack
func TestCtrlC_UserMsgPreserved(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.typingStartTime = time.Now()

	// User sends a message
	userMsg := cliMessage{role: "user", content: "请分析这个 bug", timestamp: time.Now(), dirty: true}
	model.messages = append(model.messages, userMsg)
	model.pendingUserMsg = &userMsg

	// Agent starts working — streaming assistant message
	oldTurnID := model.agentTurnID
	model.streamingMsgIdx = len(model.messages)
	model.messages = append(model.messages, cliMessage{
		role: "assistant", content: "", timestamp: time.Now(),
		isPartial: true, dirty: true, turnID: oldTurnID,
	})

	// Simulate some progress
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 1})
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		CompletedTools: []protocol.ToolProgress{
			{Name: "Read", Label: "Read bug.go", Status: "done", Elapsed: 500, Iteration: 1},
		},
	})

	// User presses Ctrl+C — set cancel state directly
	// (sendCancel would try to send to agent channel which doesn't exist in tests)
	model.cancelTargetTurnID = oldTurnID
	model.turnCancelled = true
	// Add the "已发送取消请求" system message (same as sendCancel does)
	model.appendSystem(model.locale.CancelSent)
	model.updateViewportContent()

	// PhaseDone arrives (engine's progressFinalizer)
	sendProgress(model, &protocol.ProgressEvent{
		Phase:          "done",
		Iteration:      1,
		CompletedTools: []protocol.ToolProgress{{Name: "Read", Label: "Read bug.go", Status: "done", Elapsed: 500, Iteration: 1}},
	})

	// turnCancelled should still be true (preserved by our fix)
	if !model.turnCancelled {
		t.Error("turnCancelled should remain true after PhaseDone in cancel path (prevents auto-start race)")
	}

	// Now simulate the race: a stale progress event arrives after endAgentTurn
	// With the fix, this should NOT trigger auto-start turn
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 2,
	})

	// Verify agentTurnID didn't change (auto-start turn blocked)
	if model.agentTurnID != oldTurnID {
		t.Errorf("auto-start turn should NOT have fired: agentTurnID changed from %d to %d", oldTurnID, model.agentTurnID)
	}

	// Cancel ack arrives
	model.Update(cliOutboundMsg{
		msg: channel.OutboundMsg{
			Content:  "",
			Metadata: map[string]string{"cancelled": "true"},
		},
	})

	// turnCancelled should now be cleared
	if model.turnCancelled {
		t.Error("turnCancelled should be false after cancel ack")
	}

	// Final check: user message must still be present
	hasUserMsg := false
	for _, m := range model.messages {
		if m.role == "user" && strings.Contains(m.content, "请分析这个 bug") {
			hasUserMsg = true
			break
		}
	}
	if !hasUserMsg {
		t.Fatal("User message disappeared after cancel flow")
	}

	// Verify viewport renders the user message
	model.updateViewportContent()
	vpContent := model.viewport.View()
	if !strings.Contains(vpContent, "请分析这个 bug") {
		t.Errorf("User message not visible in viewport after cancel flow.\nViewport:\n%s", vpContent)
	}
}

// TestCtrlC_AutoStartRace specifically tests that endAgentTurn in the cancel
// path does not allow stale progress events to trigger auto-start turn.
func TestCtrlC_AutoStartRace(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.typingStartTime = time.Now()

	userMsg := cliMessage{role: "user", content: "帮我查一下", timestamp: time.Now(), dirty: true}
	model.messages = append(model.messages, userMsg)
	model.pendingUserMsg = &userMsg

	oldTurnID := model.agentTurnID
	model.cancelTargetTurnID = oldTurnID
	model.streamingMsgIdx = len(model.messages)
	model.messages = append(model.messages, cliMessage{
		role: "assistant", content: "", timestamp: time.Now(),
		isPartial: true, dirty: true, turnID: oldTurnID,
	})

	// Ctrl+C
	model.turnCancelled = true

	// PhaseDone → endAgentTurn
	sendProgress(model, &protocol.ProgressEvent{Phase: "done", Iteration: 0})

	// turnCancelled should be preserved (the fix)
	if !model.turnCancelled {
		t.Fatal("turnCancelled must remain true after endAgentTurn in cancel path")
	}
	if model.typing {
		t.Error("typing should be false after endAgentTurn")
	}

	// Stale progress event → should NOT trigger auto-start turn
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 1})

	if model.agentTurnID != oldTurnID {
		t.Fatalf("auto-start turn fired unexpectedly: agentTurnID changed from %d to %d", oldTurnID, model.agentTurnID)
	}

	hasUserMsg := false
	for _, m := range model.messages {
		if m.role == "user" && m.content == "帮我查一下" {
			hasUserMsg = true
			break
		}
	}
	if !hasUserMsg {
		t.Fatal("User message disappeared in auto-start race!")
	}
}

// TestCtrlC_CancelAckPreservesBakedIterations verifies that the cancel ack
// does NOT overwrite iterations that were already baked by handleProgressDone's
// cancel path. This is the root cause of "Ctrl+C loses iterations but restart
// brings them back": handleProgressDone bakes iterationHistory into the
// streaming message, then endAgentTurn clears iterationHistory. When the
// cancel ack arrives, cancelledTurnIterations() returns empty (because
// iterationHistory is gone), and overwrites the baked iterations.
func TestCtrlC_CancelAckPreservesBakedIterations(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn() // increments agentTurnID to 1
	model.typing = true
	model.typingStartTime = time.Now()

	// Set up a turn with iteration history
	oldTurnID := model.agentTurnID
	model.cancelTargetTurnID = oldTurnID
	model.messages = append(model.messages, cliMessage{
		role:      "user",
		content:   "fix this bug",
		timestamp: time.Now(),
		dirty:     true,
	})
	// Replace the empty streaming message created by startAgentTurn with one that has content
	model.messages[model.streamingMsgIdx] = cliMessage{
		role:      "assistant",
		content:   "Here is some streamed content", // Non-empty: triggers the overwrite bug
		timestamp: time.Now(),
		isPartial: true,
		dirty:     true,
		turnID:    oldTurnID,
	}

	// Simulate iterations accumulated during the turn
	model.progressState.iterations = []cliIterationSnapshot{
		{
			Iteration: 1,
			Thinking:  "analyzing the code",
			Tools: []protocol.ToolProgress{
				{Name: "Read", Label: "main.go", Status: "done", Elapsed: 100, Iteration: 1},
			},
		},
		{
			Iteration: 2,
			Thinking:  "found the bug",
			Tools: []protocol.ToolProgress{
				{Name: "Shell", Label: "go test", Status: "done", Elapsed: 200, Iteration: 2},
			},
		},
	}
	model.progressState.lastIter = 2
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 2,
	}

	// PhaseDone arrives with turnCancelled=true (handleProgressDone cancel path)
	model.turnCancelled = true
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 2,
		CompletedTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "go test", Status: "done", Elapsed: 200, Iteration: 2},
		},
	})

	// After handleProgressDone cancel path:
	// - iterations should be baked into streaming message
	// - iterationHistory should be cleared by endAgentTurn
	// - turnCancelled restored to true
	bakedIters := 0
	if model.streamingMsgIdx >= 0 && model.streamingMsgIdx < len(model.messages) {
		bakedIters = len(model.messages[model.streamingMsgIdx].iterations)
	}
	// streamingMsgIdx was set to -1 by endAgentTurn, but the message is still in m.messages
	for i := range model.messages {
		if model.messages[i].role == "assistant" && model.messages[i].turnID == oldTurnID {
			bakedIters = len(model.messages[i].iterations)
			break
		}
	}
	if bakedIters == 0 {
		t.Fatal("handleProgressDone should have baked iterations into streaming message")
	}
	// Verify iterationHistory was cleared by endAgentTurn
	if len(model.progressState.iterations) != 0 {
		t.Fatal("iterationHistory should be cleared after endAgentTurn")
	}

	// Cancel ack arrives — this is where the bug occurs
	model.Update(cliOutboundMsg{
		msg: channel.OutboundMsg{
			Content:  "",
			Metadata: map[string]string{"cancelled": "true"},
		},
	})

	// Find the assistant message and verify iterations are PRESERVED
	var assistantMsg *cliMessage
	for i := range model.messages {
		if model.messages[i].role == "assistant" && model.messages[i].turnID == oldTurnID {
			assistantMsg = &model.messages[i]
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("assistant message should still exist after cancel ack")
	}
	if assistantMsg.isPartial {
		t.Error("assistant message should be finalized (isPartial=false)")
	}
	// CRITICAL: iterations must NOT be overwritten by the cancel ack
	if len(assistantMsg.iterations) != bakedIters {
		t.Errorf("iterations overwritten by cancel ack: got %d, want %d (baked by handleProgressDone)",
			len(assistantMsg.iterations), bakedIters)
	}
}

// TestCtrlC_CancelAckBakesIterationsWhenPhaseDoneNotArrived verifies that
// when cancel ack arrives BEFORE PhaseDone (iterationHistory still available),
// the iterations are correctly baked via cancelledTurnIterations().
func TestCtrlC_CancelAckBakesIterationsWhenPhaseDoneNotArrived(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.typingStartTime = time.Now()

	oldTurnID := model.agentTurnID
	model.cancelTargetTurnID = oldTurnID
	model.streamingMsgIdx = len(model.messages)
	model.messages = append(model.messages, cliMessage{
		role:      "assistant",
		content:   "partial response",
		timestamp: time.Now(),
		isPartial: true,
		dirty:     true,
		turnID:    oldTurnID,
	})

	// Iteration history still available (PhaseDone hasn't arrived yet)
	model.progressState.iterations = []cliIterationSnapshot{
		{
			Iteration: 1,
			Tools: []protocol.ToolProgress{
				{Name: "Read", Label: "file.go", Status: "done", Elapsed: 50, Iteration: 1},
			},
		},
	}
	model.progressState.current = &protocol.ProgressEvent{Iteration: 1}

	// Cancel ack arrives BEFORE PhaseDone
	model.Update(cliOutboundMsg{
		msg: channel.OutboundMsg{
			Content:  "",
			Metadata: map[string]string{"cancelled": "true"},
		},
	})

	var assistantMsg *cliMessage
	for i := range model.messages {
		if model.messages[i].role == "assistant" && model.messages[i].turnID == oldTurnID {
			assistantMsg = &model.messages[i]
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("assistant message should exist after cancel")
	}
	if len(assistantMsg.iterations) == 0 {
		t.Error("iterations should be baked via cancelledTurnIterations() when PhaseDone hasn't arrived")
	}
}

// TestCtrlC_CancelAckDoesNotForceRebuild verifies that the cancel ack does NOT
// call updateViewportContent() — this would trigger a full glamour re-render
// of ALL messages, which can lose the latest iterations' display data.
// The cancel ack should just set m.rc.valid = false and let the next tick
// handle the cosmetic changes (guide color dimming) incrementally.
func TestCtrlC_CancelAckDoesNotForceRebuild(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.typingStartTime = time.Now()

	oldTurnID := model.agentTurnID
	model.cancelTargetTurnID = oldTurnID
	model.streamingMsgIdx = len(model.messages)
	model.messages = append(model.messages, cliMessage{
		role:      "assistant",
		content:   "",
		timestamp: time.Now(),
		isPartial: true,
		dirty:     true,
		turnID:    oldTurnID,
	})

	// Simulate some iterations displayed during streaming
	model.progressState.iterations = []cliIterationSnapshot{
		{Iteration: 1, Tools: []protocol.ToolProgress{{Name: "Read", Label: "file.go", Status: "done"}}},
		{Iteration: 2, Tools: []protocol.ToolProgress{{Name: "Shell", Label: "make", Status: "done"}}},
	}
	model.progressState.lastIter = 2
	model.progressState.current = &protocol.ProgressEvent{Iteration: 2}

	// Build cache by doing an initial render
	model.updateViewportContent()
	// Verify cache is valid after initial render
	if !model.rc.valid {
		t.Fatal("cache should be valid after updateViewportContent")
	}

	// Cancel ack arrives
	model.Update(cliOutboundMsg{
		msg: channel.OutboundMsg{
			Content:  "",
			Metadata: map[string]string{"cancelled": "true"},
		},
	})

	// After cancel ack: cache should be marked invalid (for next tick to pick up)
	// but the critical point is that updateViewportContent was NOT called from
	// the cancel ack handler itself. We verify this by checking that the
	// cached history still contains the old content (not rebuilt).
	// The next tick will handle the rebuild incrementally.
	if model.rc.valid {
		t.Error("rc.valid should be false after cancel ack (pending tick rebuild)")
	}

	// Verify iterations are baked into the streaming message
	var assistantMsg *cliMessage
	for i := range model.messages {
		if model.messages[i].role == "assistant" {
			assistantMsg = &model.messages[i]
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("assistant message should exist after cancel")
	}
	if len(assistantMsg.iterations) != 2 {
		t.Fatalf("expected 2 baked iterations, got %d", len(assistantMsg.iterations))
	}
	// Verify the latest iterations are preserved
	if assistantMsg.iterations[0].Iteration != 1 {
		t.Errorf("expected iteration 1, got %d", assistantMsg.iterations[0].Iteration)
	}
	if assistantMsg.iterations[1].Iteration != 2 {
		t.Errorf("expected iteration 2, got %d", assistantMsg.iterations[1].Iteration)
	}
}

// TestCtrlC_CancelAckSetsInputReadyAndFlushQueue verifies that the cancel ack
// sets inputReady=true and needFlushQueue=true (when queue has messages),
// matching every other turn-end path. Without this fix, Ctrl+C leaves
// inputReady=false: the status bar shows "就绪" (because typing=false) but
// new messages silently queue (📬N) and the queue never flushes.
func TestCtrlC_CancelAckSetsInputReadyAndFlushQueue(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typing = true
	model.typingStartTime = time.Now()
	oldTurnID := model.agentTurnID

	// User queued a message while agent was typing
	model.messageQueue = append(model.messageQueue, queuedMsg{
		content: "follow up question",
		chatID:  model.chatID,
	})

	// Streaming message for the active turn
	model.cancelTargetTurnID = oldTurnID
	model.streamingMsgIdx = len(model.messages)
	model.messages = append(model.messages, cliMessage{
		role: "assistant", content: "partial response", timestamp: time.Now(),
		isPartial: true, dirty: true, turnID: oldTurnID,
	})

	// Cancel ack arrives
	model.Update(cliOutboundMsg{
		msg: channel.OutboundMsg{
			Content:  "",
			Metadata: map[string]string{"cancelled": "true"},
		},
	})

	// typing must be false (status bar shows "就绪")
	if model.typing {
		t.Error("typing should be false after cancel ack")
	}

	// inputReady must be true — user should be able to send directly,
	// not silently queue. This is the core bug: "就绪 but still queuing"
	if !model.inputReady {
		t.Error("inputReady should be true after cancel ack — " +
			"status bar shows ready but messages still queue")
	}

	// needFlushQueue must be true — tick handler should drain the queue
	if !model.needFlushQueue {
		t.Error("needFlushQueue should be true when queue has messages after cancel ack")
	}
}

// TestCtrlC_CancelAckNoQueueNoFlushFlag verifies that needFlushQueue is NOT
// set when there are no queued messages (avoids unnecessary flush attempts).
func TestCtrlC_CancelAckNoQueueNoFlushFlag(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typing = true
	model.typingStartTime = time.Now()
	oldTurnID := model.agentTurnID

	model.cancelTargetTurnID = oldTurnID
	model.streamingMsgIdx = len(model.messages)
	model.messages = append(model.messages, cliMessage{
		role: "assistant", content: "partial", timestamp: time.Now(),
		isPartial: true, dirty: true, turnID: oldTurnID,
	})

	// No messages in queue
	model.Update(cliOutboundMsg{
		msg: channel.OutboundMsg{
			Content:  "",
			Metadata: map[string]string{"cancelled": "true"},
		},
	})

	if !model.inputReady {
		t.Error("inputReady should be true after cancel ack even with empty queue")
	}
	if model.needFlushQueue {
		t.Error("needFlushQueue should remain false when queue is empty")
	}
}

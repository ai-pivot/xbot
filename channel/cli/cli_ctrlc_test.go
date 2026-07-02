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
	// Progress state (iterations) is now preserved after endAgentTurn for
	// flicker-free rendering (updateStreamingOnly uses it between PhaseDone
	// and handleAgentMessage). It is cleared by startAgentTurn/resetProgressState.
	// The baked iterations in the message are independent of progressState.

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
// trigger a fullRebuild of ALL messages. Instead, it uses rerenderCachedMessage
// to re-render only the affected message (O(1)) while keeping rc.valid = true.
// This avoids the flicker caused by O(N) glamour re-render of every message.
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

	// After cancel ack: cache stays valid — rerenderCachedMessage re-rendered
	// only the affected message via appendNewMessagesToCache (O(1)), not a
	// fullRebuild of all messages (O(N) → flicker).
	if !model.rc.valid {
		t.Error("rc.valid should stay true after cancel ack (targeted re-render, no fullRebuild)")
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

// TestCtrlC_CancelPathCapturesStreamContent verifies that when Ctrl+C
// interrupts mid-stream (LLM hasn't finished, structured Thinking/Reasoning
// not yet set), the cancel path in handleProgressDone captures StreamContent
// and ReasoningStreamContent from the live progress (prev) into the snapshot.
// Without this fix, the last reasoning block and content disappear after
// Ctrl+C because they were only available via stream fields.
func TestCtrlC_CancelPathCapturesStreamContent(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn() // increments agentTurnID to 1
	model.typing = true
	model.typingStartTime = time.Now()

	oldTurnID := model.agentTurnID
	model.cancelTargetTurnID = oldTurnID

	// Set up streaming message (empty content — LLM was still streaming)
	model.messages[model.streamingMsgIdx] = cliMessage{
		role:      "assistant",
		content:   "", // empty: content was streamed via StreamContent, not via cliOutboundMsg
		timestamp: time.Now(),
		isPartial: true,
		dirty:     true,
		turnID:    oldTurnID,
	}

	// Previous iteration already snapshotted (structured data available)
	model.progressState.iterations = []cliIterationSnapshot{
		{
			Iteration: 1,
			Reasoning: "previous reasoning",
			Thinking:  "previous content",
			Tools:     []protocol.ToolProgress{{Name: "Read", Label: "file.go", Status: "done", Elapsed: 100}},
		},
	}
	model.progressState.lastIter = 2

	// Current iteration (iteration 2) is being STREAMED.
	// Structured Thinking/Reasoning are EMPTY (recordAssistantMsg not called yet).
	// Stream content is what the user sees on screen.
	model.progressState.current = &protocol.ProgressEvent{
		Phase:                  "thinking",
		Iteration:              2,
		StreamContent:          "I'm still generating this response...",
		ReasoningStreamContent: "Let me think about this problem...",
		// Thinking and Reasoning are EMPTY — LLM hasn't finished
	}

	// PhaseDone arrives with turnCancelled=true (Ctrl+C during streaming)
	model.turnCancelled = true
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 2,
		// PhaseDone's Thinking/Reasoning are also empty (LLM didn't finish)
	})

	// Find the assistant message and verify iterations
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

	// Should have 2 iterations: iter1 (pre-existing snapshot) + iter2 (cancel snapshot)
	if len(assistantMsg.iterations) != 2 {
		t.Fatalf("expected 2 baked iterations, got %d", len(assistantMsg.iterations))
	}

	// iter2 (the one being streamed when Ctrl+C hit) must have captured stream content
	iter2 := assistantMsg.iterations[1]
	if iter2.Iteration != 2 {
		t.Fatalf("expected iteration 2, got %d", iter2.Iteration)
	}
	if iter2.Thinking != "I'm still generating this response..." {
		t.Errorf("iter2 Thinking should capture StreamContent, got %q", iter2.Thinking)
	}
	if iter2.Reasoning != "Let me think about this problem..." {
		t.Errorf("iter2 Reasoning should capture ReasoningStreamContent, got %q", iter2.Reasoning)
	}
}

// TestCtrlC_CancelAckCapturesStreamContent verifies that when cancel ack
// arrives BEFORE PhaseDone, cancelledTurnIterations() captures StreamContent
// and ReasoningStreamContent from the live progress (m.progressState.current).
func TestCtrlC_CancelAckCapturesStreamContent(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.typingStartTime = time.Now()

	oldTurnID := model.agentTurnID
	model.cancelTargetTurnID = oldTurnID
	model.streamingMsgIdx = len(model.messages)
	model.messages = append(model.messages, cliMessage{
		role:      "assistant",
		content:   "", // empty: content was streamed via StreamContent
		timestamp: time.Now(),
		isPartial: true,
		dirty:     true,
		turnID:    oldTurnID,
	})

	// Previous iteration snapshot exists
	model.progressState.iterations = []cliIterationSnapshot{
		{
			Iteration: 1,
			Reasoning: "previous reasoning",
			Tools:     []protocol.ToolProgress{{Name: "Read", Label: "file.go", Status: "done"}},
		},
	}
	model.progressState.lastIter = 2

	// Current iteration (iteration 2) is being STREAMED — no structured data yet
	model.progressState.current = &protocol.ProgressEvent{
		Iteration:              2,
		StreamContent:          "partial streamed output",
		ReasoningStreamContent: "partial streamed reasoning",
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "go test", Status: "running"},
		},
	}

	// Cancel ack arrives BEFORE PhaseDone
	model.Update(cliOutboundMsg{
		msg: channel.OutboundMsg{
			Content:  "",
			Metadata: map[string]string{"cancelled": "true"},
		},
	})

	// Find the assistant message
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

	// Should have 2 iterations: iter1 + iter2 (from cancelledTurnIterations)
	if len(assistantMsg.iterations) != 2 {
		t.Fatalf("expected 2 iterations, got %d", len(assistantMsg.iterations))
	}

	// iter2 must have captured stream content
	iter2 := assistantMsg.iterations[1]
	if iter2.Thinking != "partial streamed output" {
		t.Errorf("iter2 Thinking should capture StreamContent, got %q", iter2.Thinking)
	}
	if iter2.Reasoning != "partial streamed reasoning" {
		t.Errorf("iter2 Reasoning should capture ReasoningStreamContent, got %q", iter2.Reasoning)
	}
}

// TestCtrlC_PhaseDoneBakedIterationsNotOverwrittenByCancelAck verifies the
// double-path safety: PhaseDone cancel path bakes iterations, then cancel ack
// arrives and must NOT overwrite them. This is the most critical race scenario.
func TestCtrlC_PhaseDoneBakedIterationsNotOverwrittenByCancelAck(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typing = true
	model.typingStartTime = time.Now()

	oldTurnID := model.agentTurnID
	model.cancelTargetTurnID = oldTurnID

	// Streaming message with partial content (simulating IsPartial outbound)
	model.messages[model.streamingMsgIdx] = cliMessage{
		role:      "assistant",
		content:   "partial text from outbound",
		timestamp: time.Now(),
		isPartial: true,
		dirty:     true,
		turnID:    oldTurnID,
	}

	// Two iterations: iter1 has structured data, iter2 is being streamed
	model.progressState.iterations = []cliIterationSnapshot{
		{Iteration: 1, Thinking: "iter1 content", Reasoning: "iter1 reasoning",
			Tools: []protocol.ToolProgress{{Name: "Read", Label: "a.go", Status: "done"}}},
	}
	model.progressState.lastIter = 2
	model.progressState.current = &protocol.ProgressEvent{
		Iteration:              2,
		StreamContent:          "streamed iter2 content",
		ReasoningStreamContent: "streamed iter2 reasoning",
	}

	// Step 1: PhaseDone arrives with cancel (Ctrl+C during streaming)
	model.turnCancelled = true
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 2,
	})

	// Verify iterations are baked with stream content
	var assistantMsg *cliMessage
	for i := range model.messages {
		if model.messages[i].role == "assistant" && model.messages[i].turnID == oldTurnID {
			assistantMsg = &model.messages[i]
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("assistant message should exist after PhaseDone cancel")
	}
	bakedCount := len(assistantMsg.iterations)
	if bakedCount != 2 {
		t.Fatalf("expected 2 baked iterations after PhaseDone, got %d", bakedCount)
	}
	// iter2 must have captured stream content
	if assistantMsg.iterations[1].Thinking != "streamed iter2 content" {
		t.Errorf("iter2 Thinking = %q, want streamed content", assistantMsg.iterations[1].Thinking)
	}

	// Step 2: Cancel ack arrives — must NOT overwrite baked iterations
	model.Update(cliOutboundMsg{
		msg: channel.OutboundMsg{
			Content:  "partial text from outbound",
			Metadata: map[string]string{"cancelled": "true"},
		},
	})

	// Find assistant message again (index may have shifted)
	for i := range model.messages {
		if model.messages[i].role == "assistant" && model.messages[i].turnID == oldTurnID {
			assistantMsg = &model.messages[i]
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("assistant message should still exist after cancel ack")
	}
	if len(assistantMsg.iterations) != bakedCount {
		t.Errorf("cancel ack overwrote iterations: got %d, want %d",
			len(assistantMsg.iterations), bakedCount)
	}
	// Stream-captured content must survive cancel ack
	if assistantMsg.iterations[1].Thinking != "streamed iter2 content" {
		t.Errorf("iter2 Thinking corrupted by cancel ack: got %q",
			assistantMsg.iterations[1].Thinking)
	}
}

// TestCtrlC_CancelledTurnIterationsNilSafety verifies that
// cancelledTurnIterations() does NOT panic when progressState.current is nil
// (after endAgentTurn cleared it). It should return whatever iterations are
// available from pendingToolSummary or progressState.iterations.
func TestCtrlC_CancelledTurnIterationsNilSafety(t *testing.T) {
	model := initTestModel()

	// Simulate post-endAgentTurn state: everything cleared
	model.progressState.current = nil
	model.progressState.iterations = nil
	model.pendingToolSummary = nil

	// Should return empty slice, NOT panic
	iters := model.cancelledTurnIterations()
	if len(iters) != 0 {
		t.Errorf("expected empty iterations when all state cleared, got %d", len(iters))
	}

	// Now with pendingToolSummary set (simulating pre-PhaseDone cancel ack)
	model.pendingToolSummary = &cliMessage{
		iterations: []cliIterationSnapshot{
			{Iteration: 1, Thinking: "from PTS"},
		},
	}
	iters = model.cancelledTurnIterations()
	if len(iters) != 1 {
		t.Fatalf("expected 1 iteration from pendingToolSummary, got %d", len(iters))
	}
	if iters[0].Thinking != "from PTS" {
		t.Errorf("expected 'from PTS', got %q", iters[0].Thinking)
	}
}

// TestCtrlC_NormalPhaseDoneDoesNotCaptureStreamContent verifies that the
// NORMAL (non-cancel) PhaseDone path does NOT use stream content as fallback.
// This is intentional: in normal completion, ThinkingContent is set by
// recordAssistantMsg and carries the authoritative content. Using stream
// content in the normal path risks cross-iteration contamination.
func TestCtrlC_NormalPhaseDoneDoesNotCaptureStreamContent(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typing = true
	model.typingStartTime = time.Now()

	turnID := model.agentTurnID
	model.messages[model.streamingMsgIdx] = cliMessage{
		role:      "assistant",
		content:   "",
		timestamp: time.Now(),
		isPartial: true,
		dirty:     true,
		turnID:    turnID,
	}

	// Set up: iteration 2 with EMPTY Thinking but non-empty StreamContent
	model.progressState.iterations = []cliIterationSnapshot{
		{Iteration: 1, Thinking: "iter1", Tools: []protocol.ToolProgress{{Name: "Read", Label: "a.go", Status: "done"}}},
	}
	model.progressState.lastIter = 2
	model.progressState.current = &protocol.ProgressEvent{
		Phase:         "tool_exec",
		Iteration:     2,
		StreamContent: "this should NOT be captured in normal path",
		Thinking:      "", // empty: simulates progressCh coalescing
	}

	// Normal PhaseDone (turnCancelled is FALSE)
	// Include a completed tool so the iter2 snapshot is actually created
	// (empty Thinking + empty Reasoning + no tools → snapshot is skipped)
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 2,
		Thinking:  "", // also empty in payload
		CompletedTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "go test", Status: "done", Elapsed: 100, Iteration: 2},
		},
	})

	// Find the pendingToolSummary iterations (stored for handleAgentMessage)
	if model.pendingToolSummary == nil {
		t.Fatal("pendingToolSummary should be set after normal PhaseDone")
	}
	iters := model.pendingToolSummary.iterations
	if len(iters) < 2 {
		t.Fatalf("expected at least 2 iterations, got %d", len(iters))
	}

	// The last iteration's Thinking should be EMPTY, not stream content.
	// This proves the normal path does NOT capture stream content.
	lastIter := iters[len(iters)-1]
	if lastIter.Thinking == "this should NOT be captured in normal path" {
		t.Error("normal PhaseDone path captured StreamContent — this risks cross-iteration contamination")
	}
}

// TestCtrlC_RenderNoDuplicationWithStreamContent verifies that rendering
// after Ctrl+C does NOT produce duplicate content when both the streaming
// message content and the last iteration's Thinking carry similar text.
// renderTurnBody's dedup (last.Thinking == fallbackContent) must catch this.
func TestCtrlC_RenderNoDuplicationWithStreamContent(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typing = true
	model.typingStartTime = time.Now()

	turnID := model.agentTurnID
	streamText := "Hello world response"

	// Streaming message has content from IsPartial outbound
	model.messages[model.streamingMsgIdx] = cliMessage{
		role:      "assistant",
		content:   streamText,
		timestamp: time.Now(),
		isPartial: true,
		dirty:     true,
		turnID:    turnID,
	}

	// Cancel path captures StreamContent (same text) into snapshot Thinking
	model.progressState.lastIter = 1
	model.progressState.current = &protocol.ProgressEvent{
		Iteration:     1,
		StreamContent: streamText, // same as streaming message content
		Thinking:      "",         // structured empty
	}

	model.cancelTargetTurnID = turnID
	model.turnCancelled = true
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 1,
	})

	// Cancel ack finalizes the message
	model.Update(cliOutboundMsg{
		msg: channel.OutboundMsg{
			Content:  streamText,
			Metadata: map[string]string{"cancelled": "true"},
		},
	})

	// Find the assistant message
	var msg *cliMessage
	for i := range model.messages {
		if model.messages[i].role == "assistant" && model.messages[i].turnID == turnID {
			msg = &model.messages[i]
			break
		}
	}
	if msg == nil {
		t.Fatal("assistant message should exist")
	}

	// Render the message body
	body := model.renderTurnBody(msg.iterations, nil, 76, msg.content)

	// The streamText should appear exactly ONCE in the rendered output.
	// Count occurrences (as rendered text, not raw — glamour may add formatting)
	// We check for the plain text presence without ANSI codes.
	plain := stripANSI(body)
	count := strings.Count(plain, "Hello world response")
	if count != 1 {
		t.Errorf("streamText should appear exactly once in rendered output, got %d times.\nRendered:\n%s", count, plain)
	}
}

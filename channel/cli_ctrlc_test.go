package channel

import (
	"strings"
	"testing"
	"time"
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
		msg: OutboundMsg{
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

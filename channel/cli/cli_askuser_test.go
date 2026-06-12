package cli

import (
	"encoding/json"
	"testing"
	"time"
	"xbot/protocol"
)

// TestAskUserIterationVisibility reproduces the bug:
// When AskUser panel opens, previous iteration records disappear from the viewport.
// Updated: renderProgressBlock always returns empty now (progress is inline in the
// streaming assistant message). The test verifies iterationHistory preservation.
func TestAskUserIterationVisibility(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Simulate 2 iterations with tools
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 1})
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		CompletedTools: []protocol.ToolProgress{
			{Name: "Read", Label: "Read go.mod", Status: "done", Elapsed: 500, Iteration: 1},
		},
	})
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 2})
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 2,
		CompletedTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "echo done", Status: "done", Elapsed: 200, Iteration: 2},
		},
	})

	// Snapshot iteration history count
	iterCountBefore := len(model.iterationHistory)
	if iterCountBefore == 0 {
		t.Fatalf("Expected iterationHistory to have entries, got 0")
	}

	// Verify streaming assistant message exists and has turnID
	if model.streamingMsgIdx < 0 || model.streamingMsgIdx >= len(model.messages) {
		t.Fatal("Expected streaming assistant message to exist after startAgentTurn")
	}
	if model.messages[model.streamingMsgIdx].turnID != model.agentTurnID {
		t.Errorf("Streaming message turnID mismatch: got %d, want %d",
			model.messages[model.streamingMsgIdx].turnID, model.agentTurnID)
	}

	// Simulate the AskUser outbound message from agent
	askQuestions, _ := json.Marshal([]map[string]interface{}{
		{"question": "Can you see the iterations?", "options": []string{"yes", "no"}},
	})
	model.Update(cliOutboundMsg{
		msg: OutboundMsg{
			Content:     "两次迭代完成，现在用 AskUser 提问：",
			WaitingUser: true,
			Metadata: map[string]string{
				"ask_questions": string(askQuestions),
			},
		},
	})

	// After the outbound, the AskUser panel should be open
	if model.panelMode != "askuser" {
		t.Fatalf("Expected panelMode=askuser, got %q", model.panelMode)
	}

	// typing should be false (openAskUserPanel sets it)
	if model.typing {
		t.Error("Expected typing=false after AskUser panel opens")
	}

	// CRITICAL CHECK: iterationHistory should still have entries
	if len(model.iterationHistory) != iterCountBefore {
		t.Errorf("iterationHistory was cleared! Before=%d, After=%d",
			iterCountBefore, len(model.iterationHistory))
	}

	// progress should still be non-nil
	if model.progress == nil {
		t.Error("progress should not be nil while AskUser panel is open")
	}
}

// TestAskUserIterationSurvivesAnswer verifies iteration history survives
// the answer callback (startAgentTurn clears state). Updated: iterations are
// stored in pendingToolSummary (not as tool_summary messages).
func TestAskUserIterationSurvivesAnswer(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.typingStartTime = time.Now()
	_ = model.agentTurnID

	// Simulate 2 iterations with tools
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 0})
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 0,
		CompletedTools: []protocol.ToolProgress{
			{Name: "Read", Label: "Read go.mod", Status: "done", Elapsed: 500, Iteration: 0},
		},
	})
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 1})
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		CompletedTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "echo done", Status: "done", Elapsed: 200, Iteration: 1},
		},
	})

	// Send AskUser outbound
	askQuestions, _ := json.Marshal([]map[string]interface{}{
		{"question": "Can you see the iterations?", "options": []string{"yes", "no"}},
	})
	model.Update(cliOutboundMsg{
		msg: OutboundMsg{
			Content:     "AskUser question",
			WaitingUser: true,
			Metadata: map[string]string{
				"ask_questions": string(askQuestions),
			},
		},
	})

	if model.panelMode != "askuser" {
		t.Fatalf("Expected panelMode=askuser, got %q", model.panelMode)
	}

	// Simulate answer callback
	if model.panelOnAnswer != nil {
		model.panelOnAnswer(map[string]string{"q0": "yes"})
	}

	// After answer: startAgentTurn clears iterationHistory, but
	// the answer callback now stores pre-AskUser iterations in pendingToolSummary.
	if model.pendingToolSummary == nil || len(model.pendingToolSummary.iterations) == 0 {
		t.Error("Expected pendingToolSummary with iterations after answer, got nil or empty")
	} else {
		toolCount := 0
		for _, it := range model.pendingToolSummary.iterations {
			toolCount += len(it.Tools)
		}
		if toolCount < 1 {
			t.Errorf("Expected at least 1 tool in pendingToolSummary, got %d", toolCount)
		}
	}
}

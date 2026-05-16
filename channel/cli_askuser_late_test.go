package channel

import (
	"encoding/json"
	"testing"
	"time"
	"xbot/protocol"
)

// TestAskUserLateProgressClearsState reproduces the real-world bug:
// A late-arriving progress event (still in progressCh from the engine)
// arrives after openAskUserPanel sets m.typing=false.
// handleProgressMsg's auto-start turn logic (line 380) then calls
// startAgentTurn() → resetProgressState(), clearing iterationHistory.
func TestAskUserLateProgressClearsState(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.typingStartTime = time.Now()

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

	iterCountBefore := len(model.iterationHistory)
	if iterCountBefore == 0 {
		t.Fatalf("Expected iterationHistory to have entries, got 0")
	}

	// Send AskUser outbound — opens the panel, sets m.typing = false
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

	// Now simulate a LATE progress event arriving after the panel is open.
	// In real execution, this can happen because progressCh and msgBus
	// are separate async channels with non-deterministic ordering.
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 2, // next iteration that never actually ran
		CompletedTools: []protocol.ToolProgress{
			{Name: "AskUser", Label: "asked question", Status: "done", Elapsed: 100, Iteration: 2},
		},
	})

	// BUG: The auto-start turn logic in handleProgressMsg triggers because
	// m.typing=false (set by openAskUserPanel). This calls startAgentTurn()
	// → resetProgressState() → clears m.iterationHistory!
	if len(model.iterationHistory) == 0 {
		t.Error("BUG REPRODUCED: late progress event cleared iterationHistory after AskUser panel opened")
	}

	if model.progress == nil {
		t.Error("BUG: progress was cleared by late progress event's auto-start turn")
	}

	// Progress block should still render
	block := model.renderProgressBlock()
	if block == "" {
		t.Error("renderProgressBlock returned empty after late progress event")
	}
}

// TestAskUserTickPreservesIterations verifies that tick handler
// doesn't destroy iteration state when AskUser panel is open.
func TestAskUserTickPreservesIterations(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.typingStartTime = time.Now()

	// Simulate 2 iterations
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

	// Open AskUser panel
	askQuestions, _ := json.Marshal([]map[string]interface{}{
		{"question": "Test?", "options": []string{"yes", "no"}},
	})
	model.Update(cliOutboundMsg{
		msg: OutboundMsg{
			Content:     "AskUser",
			WaitingUser: true,
			Metadata:    map[string]string{"ask_questions": string(askQuestions)},
		},
	})

	iterCount := len(model.iterationHistory)

	// Simulate tick
	model.handleTickMsg()

	if len(model.iterationHistory) != iterCount {
		t.Errorf("Tick changed iterationHistory: before=%d after=%d",
			iterCount, len(model.iterationHistory))
	}

	block := model.renderProgressBlock()
	if block == "" {
		t.Error("renderProgressBlock empty after tick with AskUser panel open")
	}
}

package cli

import (
	"encoding/json"
	"testing"
	"time"
	"xbot/channel"
	"xbot/protocol"
)

// TestAskUserLateProgressClearsState reproduces the real-world bug:
// A late-arriving progress event (still in progressSlot from the engine)
// arrives after openAskUserPanel sets m.typing=false.
// handleProgressMsg's auto-start turn logic then calls
// startAgentTurn() → resetProgressState(), clearing iterationHistory.
// Updated: progress is rendered inline in the streaming message.
// The test verifies iterationHistory and progress are preserved.
func TestAskUserLateProgressClearsState(t *testing.T) {
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

	iterCountBefore := len(model.progressState.iterations)
	if iterCountBefore == 0 {
		t.Fatalf("Expected iterationHistory to have entries, got 0")
	}

	// Send AskUser outbound — opens the panel, sets m.typing = false
	askQuestions, _ := json.Marshal([]map[string]interface{}{
		{"question": "Can you see the iterations?", "options": []string{"yes", "no"}},
	})
	model.Update(cliOutboundMsg{
		msg: channel.OutboundMsg{
			Content:     "AskUser question",
			WaitingUser: true,
			Metadata: map[string]string{
				"ask_questions": string(askQuestions),
			},
		},
	})

	if model.panelState.mode != "askuser" {
		t.Fatalf("Expected panelMode=askuser, got %q", model.panelState.mode)
	}

	// Now simulate a LATE progress event arriving after the panel is open.
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 3,
		CompletedTools: []protocol.ToolProgress{
			{Name: "AskUser", Label: "asked question", Status: "done", Elapsed: 100, Iteration: 3},
		},
	})

	// The late progress event should NOT clear iterationHistory.
	if len(model.progressState.iterations) == 0 {
		t.Error("BUG REPRODUCED: late progress event cleared iterationHistory after AskUser panel opened")
	}

	if model.progressState.current == nil {
		t.Error("BUG: progress was cleared by late progress event's auto-start turn")
	}
}

// TestAskUserTickPreservesIterations verifies that tick handler
// doesn't destroy iteration state when AskUser panel is open.
// Updated: progress is rendered inline in the streaming message.
func TestAskUserTickPreservesIterations(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Simulate 2 iterations
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

	// Open AskUser panel
	askQuestions, _ := json.Marshal([]map[string]interface{}{
		{"question": "Test?", "options": []string{"yes", "no"}},
	})
	model.Update(cliOutboundMsg{
		msg: channel.OutboundMsg{
			Content:     "AskUser",
			WaitingUser: true,
			Metadata:    map[string]string{"ask_questions": string(askQuestions)},
		},
	})

	iterCount := len(model.progressState.iterations)

	// Simulate tick
	model.handleTickMsg()

	if len(model.progressState.iterations) != iterCount {
		t.Errorf("Tick changed iterationHistory: before=%d after=%d",
			iterCount, len(model.progressState.iterations))
	}
}

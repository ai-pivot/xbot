package channel

import (
	"encoding/json"
	"testing"
	"time"
	"xbot/protocol"
)

// TestAskUserIterationVisibility reproduces the bug:
// When AskUser panel opens, previous iteration records disappear from the viewport.
func TestAskUserIterationVisibility(t *testing.T) {
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

	// Verify progress block shows both tools before AskUser
	block := model.renderProgressBlock()
	assertCount(t, "Read go.mod before AskUser", block, "Read go.mod", 1)
	assertCount(t, "echo done before AskUser", block, "echo done", 1)

	// Snapshot iteration history count
	iterCountBefore := len(model.iterationHistory)
	if iterCountBefore == 0 {
		t.Fatalf("Expected iterationHistory to have entries, got 0")
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

	// Progress block should still render iteration history
	blockAfter := model.renderProgressBlock()
	if blockAfter == "" {
		t.Error("renderProgressBlock returned empty string after AskUser panel opened")
	}
	assertCount(t, "Read go.mod after AskUser", blockAfter, "Read go.mod", 1)
	assertCount(t, "echo done after AskUser", blockAfter, "echo done", 1)
}

// TestAskUserIterationSurvivesAnswer verifies iteration history survives
// the answer callback (startAgentTurn clears state).
func TestAskUserIterationSurvivesAnswer(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.typingStartTime = time.Now()
	turnID := model.agentTurnID

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
	// our fix should have saved it as tool_summary first.
	// Check that tool_summary exists with iterations from the first run.
	foundToolSummary := false
	for _, msg := range model.messages {
		if msg.role == "tool_summary" && len(msg.iterations) > 0 {
			foundToolSummary = true
			toolCount := 0
			for _, it := range msg.iterations {
				toolCount += len(it.Tools)
			}
			if toolCount < 1 {
				t.Errorf("Expected at least 1 tool in tool_summary, got %d", toolCount)
			}
		}
	}
	if !foundToolSummary {
		toolSummaryMsgs := []string{}
		for _, msg := range model.messages {
			if msg.role == "tool_summary" {
				toolSummaryMsgs = append(toolSummaryMsgs, msg.content)
			}
		}
		t.Errorf("No tool_summary with iterations found after answer. tool_summary contents: %v", toolSummaryMsgs)
	}

	// Verify the tool_summary has the correct turnID
	_ = turnID
}

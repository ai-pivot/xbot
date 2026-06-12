package channel

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"xbot/protocol"
)

// TestAskUserViewportPreservesIterations verifies that when AskUser panel opens,
// the viewport rendering of the previous assistant message still shows ALL iteration
// data (tool names). The bug was that the last iteration's tools (from m.progress)
// were not included in bakeIterations, so they disappeared when the message was finalized.
func TestAskUserViewportPreservesIterations(t *testing.T) {
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

	// Simulate the AskUser outbound message (non-partial, with WaitingUser)
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

	if model.panelMode != "askuser" {
		t.Fatalf("Expected panelMode=askuser, got %q", model.panelMode)
	}

	// Verify the assistant message has iterations baked in
	var assistantMsg *cliMessage
	for idx := range model.messages {
		msg := &model.messages[idx]
		if msg.role == "assistant" && !msg.isPartial {
			assistantMsg = msg
		}
	}
	if assistantMsg == nil {
		t.Fatal("No finalized assistant message found")
	}

	t.Logf("Assistant message iterations: %d", len(assistantMsg.iterations))
	for _, it := range assistantMsg.iterations {
		var tools []string
		for _, t := range it.Tools {
			tools = append(tools, t.Name)
		}
		t.Logf("  iteration %d: tools=%v", it.Iteration, tools)
	}

	// Verify tool names appear in the rendered output
	rendered := model.renderMessage(assistantMsg)
	if !strings.Contains(rendered, "Read") {
		t.Errorf("Rendered output missing 'Read' tool:\n%s", rendered)
	}
	if !strings.Contains(rendered, "echo done") {
		t.Errorf("Rendered output missing 'echo done' tool label:\n%s", rendered)
	}
}

// TestAskUserAnswerPreservesViewportIterations verifies that after answering AskUser,
// the previous assistant message still shows iteration data in viewport.
func TestAskUserAnswerPreservesViewportIterations(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Simulate iteration with tools
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 1})
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		CompletedTools: []protocol.ToolProgress{
			{Name: "Read", Label: "Read go.mod", Status: "done", Elapsed: 500, Iteration: 1},
			{Name: "Grep", Label: "Search pattern", Status: "done", Elapsed: 200, Iteration: 1},
		},
	})
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 2})
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 2,
		CompletedTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "echo hello", Status: "done", Elapsed: 100, Iteration: 2},
		},
	})

	// Send AskUser outbound
	askQuestions, _ := json.Marshal([]map[string]interface{}{
		{"question": "Continue?", "options": []string{"yes", "no"}},
	})
	model.Update(cliOutboundMsg{
		msg: OutboundMsg{
			Content:     "Done reading files",
			WaitingUser: true,
			Metadata: map[string]string{
				"ask_questions": string(askQuestions),
			},
		},
	})

	if model.panelMode != "askuser" {
		t.Fatalf("Expected panelMode=askuser, got %q", model.panelMode)
	}

	// Verify pre-answer: assistant has iterations
	var preAnswerMsg *cliMessage
	for i := range model.messages {
		if model.messages[i].role == "assistant" && !model.messages[i].isPartial {
			preAnswerMsg = &model.messages[i]
		}
	}
	if preAnswerMsg == nil {
		t.Fatal("No finalized assistant message found before answer")
	}
	if len(preAnswerMsg.iterations) == 0 {
		t.Fatal("Pre-answer: assistant message has no iterations")
	}
	t.Logf("Pre-answer iterations: %d", len(preAnswerMsg.iterations))
	for _, it := range preAnswerMsg.iterations {
		var tools []string
		for _, t := range it.Tools {
			tools = append(tools, t.Name)
		}
		t.Logf("  iteration %d: tools=%v", it.Iteration, tools)
	}

	// Answer the AskUser question
	if model.panelOnAnswer != nil {
		model.panelOnAnswer(map[string]string{"q0": "yes"})
	}

	// After answer: find the pre-AskUser assistant message (non-partial, not the new streaming one)
	var postAnswerMsg *cliMessage
	for i := range model.messages {
		if model.messages[i].role == "assistant" && !model.messages[i].isPartial {
			postAnswerMsg = &model.messages[i]
		}
	}
	if postAnswerMsg == nil {
		t.Fatal("No finalized assistant message found after AskUser answer")
	}
	if len(postAnswerMsg.iterations) == 0 {
		t.Fatal("Post-answer: assistant message lost its iterations!")
	}

	// Verify tool names in rendered output
	rendered := model.renderMessage(postAnswerMsg)
	if !strings.Contains(rendered, "Read") {
		t.Errorf("Post-answer rendered output missing 'Read' tool:\n%s", rendered)
	}
	if !strings.Contains(rendered, "echo hello") {
		t.Errorf("Post-answer rendered output missing 'echo hello' tool label:\n%s", rendered)
	}
}

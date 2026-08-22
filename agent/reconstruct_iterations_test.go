package agent

import (
	"testing"

	"xbot/llm"
)

func TestReconstructIterationsFromMessages(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "do something", TurnID: 1},
		{Role: "assistant", Content: "Let me read the file.", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Read", Arguments: `{"path":"f.go"}`}}, TurnID: 1},
		{Role: "tool", ToolCallID: "c1", ToolName: "Read", Content: "file contents", TurnID: 1},
		{Role: "assistant", Content: "Now let me grep.", ToolCalls: []llm.ToolCall{{ID: "c2", Name: "Grep", Arguments: `{"pattern":"test"}`}}, TurnID: 1},
		{Role: "tool", ToolCallID: "c2", ToolName: "Grep", Content: "grep results", TurnID: 1},
	}

	iters := reconstructIterationsFromMessages(msgs)

	if len(iters) != 2 {
		t.Fatalf("expected 2 iterations, got %d", len(iters))
	}

	// Iteration 1: Read
	if iters[0].Iteration != 1 {
		t.Errorf("expected iteration 1, got %d", iters[0].Iteration)
	}
	if len(iters[0].Tools) != 1 || iters[0].Tools[0].Name != "Read" {
		t.Errorf("expected Read tool, got %+v", iters[0].Tools)
	}
	if iters[0].Tools[0].Status != "done" {
		t.Errorf("expected done status, got %s", iters[0].Tools[0].Status)
	}
	if iters[0].Content != "Let me read the file." {
		t.Errorf("expected content 'Let me read the file.', got %q", iters[0].Content)
	}

	// Iteration 2: Grep
	if iters[1].Iteration != 2 {
		t.Errorf("expected iteration 2, got %d", iters[1].Iteration)
	}
	if len(iters[1].Tools) != 1 || iters[1].Tools[0].Name != "Grep" {
		t.Errorf("expected Grep tool, got %+v", iters[1].Tools)
	}
}

func TestReconstructIterationsFromMessages_ErrorStatus(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "test", TurnID: 1},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Shell", Arguments: "{}"}}, TurnID: 1},
		{Role: "tool", ToolCallID: "c1", ToolName: "Shell", Content: "Error: command not found", TurnID: 1},
	}

	iters := reconstructIterationsFromMessages(msgs)

	if len(iters) != 1 {
		t.Fatalf("expected 1 iteration, got %d", len(iters))
	}
	if iters[0].Tools[0].Status != "error" {
		t.Errorf("expected error status, got %s", iters[0].Tools[0].Status)
	}
}

func TestReconstructIterationsFromMessages_Empty(t *testing.T) {
	if iters := reconstructIterationsFromMessages(nil); iters != nil {
		t.Errorf("expected nil for empty messages, got %v", iters)
	}
}

func TestReconstructIterationsFromMessages_ResumeTurnNoUserMessage(t *testing.T) {
	// A resume turn (TurnID 2) has NO user message of its own — it continues
	// a previous turn (TurnID 1, which HAS a user message). The function must
	// rebuild ONLY the current turn's iterations keyed on TurnID; otherwise it
	// bleeds the previous turn's content into the current turn's history.
	msgs := []llm.ChatMessage{
		// Previous turn (TurnID 1) — has a user message.
		{Role: "user", Content: "do the fix", TurnID: 1},
		{Role: "assistant", Content: "backing up the file.", ToolCalls: []llm.ToolCall{{ID: "c_old", Name: "Shell", Arguments: "{}"}}, TurnID: 1},
		{Role: "tool", ToolCallID: "c_old", ToolName: "Shell", Content: "done", TurnID: 1},

		// Resume turn (TurnID 2) — NO user message, only assistant+tool.
		{Role: "assistant", Content: "verifying with a new tool.", ToolCalls: []llm.ToolCall{{ID: "c_new", Name: "Shell", Arguments: "{}"}}, TurnID: 2},
		{Role: "tool", ToolCallID: "c_new", ToolName: "Shell", Content: "ok", TurnID: 2},
	}

	iters := reconstructIterationsFromMessages(msgs)

	if len(iters) != 1 {
		t.Fatalf("expected 1 iteration (current turn only), got %d", len(iters))
	}
	if iters[0].Content != "verifying with a new tool." {
		t.Errorf("expected current-turn content, got %q", iters[0].Content)
	}
	if len(iters[0].Tools) != 1 || iters[0].Tools[0].Name != "Shell" || iters[0].Tools[0].Args != "{}" {
		t.Errorf("expected current-turn Shell tool, got %+v", iters[0].Tools)
	}
}

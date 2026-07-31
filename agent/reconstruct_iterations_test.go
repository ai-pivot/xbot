package agent

import (
	"testing"

	"xbot/llm"
)

func TestReconstructIterationsFromMessages(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "do something"},
		{Role: "assistant", Content: "Let me read the file.", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Read", Arguments: `{"path":"f.go"}`}}},
		{Role: "tool", ToolCallID: "c1", ToolName: "Read", Content: "file contents"},
		{Role: "assistant", Content: "Now let me grep.", ToolCalls: []llm.ToolCall{{ID: "c2", Name: "Grep", Arguments: `{"pattern":"test"}`}}},
		{Role: "tool", ToolCallID: "c2", ToolName: "Grep", Content: "grep results"},
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
		{Role: "user", Content: "test"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Shell", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c1", ToolName: "Shell", Content: "Error: command not found"},
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

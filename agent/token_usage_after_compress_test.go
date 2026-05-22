package agent

import (
	"testing"

	"xbot/llm"
)

// TestTokenUsageAfterCompress verifies that structuredProgress.TokenUsage
// reflects the correct value at every stage after compression:
//
//	Compression → setTokenUsageAfterCompress(20k)
//	  → LLM call returns 30k → updateTokenUsage() → should be 30k
//	  → Tool execution → initToolProgress → should still be 30k
//	  → next iteration beginIteration → should still be 30k
//
// Bug: after compression in a turn, the context bar would show the compressed
// value (20k) during tool execution, then jump to the real value (30k) during
// streaming. This test pinpoints which transition loses the correct value.
func TestTokenUsageAfterCompress(t *testing.T) {
	// Setup: create a runState with structuredProgress
	tracker := NewTokenTracker(170000, 0) // pre-compress token count

	msgs := []llm.ChatMessage{
		llm.NewSystemMessage("system"),
		llm.NewUserMessage("hello"),
	}

	state := &runState{
		cfg: RunConfig{
			MaxOutputTokens: 4096,
		},
		messages:     msgs,
		tokenTracker: tracker,
		structuredProgress: &StructuredProgress{
			Phase:     PhaseThinking,
			Iteration: 0,
		},
		autoNotify: false, // don't require ProgressNotifier
	}

	// Seed initial TokenUsage (simulates initProgress with DB-restored value)
	state.structuredProgress.TokenUsage = &TokenUsageSnapshot{
		PromptTokens:     170000,
		CompletionTokens: 0,
		TotalTokens:      170000,
		MaxOutputTokens:  4096,
	}

	// Step 1: Simulate compression
	// ResetAfterCompress zeros the tracker
	tracker.ResetAfterCompress()

	// setTokenUsageAfterCompress sets the compressed value
	compressedTokens := int64(20000)
	state.setTokenUsageAfterCompress(compressedTokens)

	assertTokenUsage(t, state, "after compress", compressedTokens, "setTokenUsageAfterCompress should set compressed value")

	// Step 2: Simulate LLM call returning real prompt_tokens
	realTokens := int64(30000)
	tracker.RecordLLMCall(realTokens, 500)
	state.updateTokenUsage()

	assertTokenUsage(t, state, "after LLM call", realTokens, "updateTokenUsage should reflect real API value")

	// Step 3: Simulate beginIteration (next iteration in same turn)
	state.beginIteration(1)

	assertTokenUsage(t, state, "after beginIteration", realTokens, "beginIteration must NOT reset TokenUsage")

	// Step 4: Simulate tool execution start (initToolProgress)
	response := &llm.LLMResponse{
		ToolCalls: []llm.ToolCall{
			{Name: "Shell", Arguments: `{"command":"ls"}`},
		},
	}
	batch := state.initToolProgress(response, 1)

	assertTokenUsage(t, state, "after initToolProgress", realTokens, "initToolProgress must NOT reset TokenUsage")
	_ = batch

	// Step 5: Simulate second tool starting (sequential dispatch)
	// beginIteration for iteration 2
	state.beginIteration(2)
	assertTokenUsage(t, state, "iteration 2 beginIteration", realTokens, "TokenUsage must persist across iterations")

	// Step 6: Second LLM call with different token count
	realTokens2 := int64(32000)
	tracker.RecordLLMCall(realTokens2, 600)
	state.updateTokenUsage()

	assertTokenUsage(t, state, "after second LLM call", realTokens2, "second updateTokenUsage should reflect new API value")

	// Step 7: Tool execution after second LLM call
	response2 := &llm.LLMResponse{
		ToolCalls: []llm.ToolCall{
			{Name: "Read", Arguments: `{"path":"main.go"}`},
		},
	}
	state.initToolProgress(response2, 2)

	assertTokenUsage(t, state, "after second initToolProgress", realTokens2, "TokenUsage must persist through second tool execution")
}

func assertTokenUsage(t *testing.T, state *runState, stage string, expected int64, msg string) {
	t.Helper()
	if state.structuredProgress == nil {
		t.Fatalf("%s: structuredProgress is nil", stage)
	}
	if state.structuredProgress.TokenUsage == nil {
		t.Fatalf("%s: TokenUsage is nil — %s", stage, msg)
	}
	got := state.structuredProgress.TokenUsage.PromptTokens
	if got != expected {
		t.Errorf("%s: TokenUsage.PromptTokens = %d, want %d — %s", stage, got, expected, msg)
	}
}

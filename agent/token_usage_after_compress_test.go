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

// TestSetTokenUsageAfterCompress_UpdatesTracker verifies that after compression,
// the token tracker is also updated so that maybeCompress can still evaluate
// the compressed count. Without this fix, ResetAfterCompress zeros the tracker,
// GetPromptTokens returns "no_data", and maybeCompress skips — even when the
// compressed count is still above the threshold (Bug 2).
func TestSetTokenUsageAfterCompress_UpdatesTracker(t *testing.T) {
	tracker := NewTokenTracker(170000, 5000)

	state := &runState{
		cfg: RunConfig{
			MaxOutputTokens: 4096,
		},
		messages: []llm.ChatMessage{
			llm.NewSystemMessage("system"),
			llm.NewUserMessage("hello"),
		},
		tokenTracker:       tracker,
		structuredProgress: &StructuredProgress{},
		autoNotify:         false,
	}

	// Step 1: Compression resets the tracker
	tracker.ResetAfterCompress()

	// Before fix: tracker has 0 tokens, source = "no_data"
	val, source := tracker.GetPromptTokens()
	if val != 0 || source != "no_data" {
		t.Fatalf("after ResetAfterCompress: got (%d, %q), want (0, \"no_data\")", val, source)
	}

	// Step 2: setTokenUsageAfterCompress updates both structuredProgress AND tracker
	compressedTokens := int64(60000)
	state.setTokenUsageAfterCompress(compressedTokens)

	// Tracker should now have the compressed count, source = "restored" (not "api")
	val, source = tracker.GetPromptTokens()
	if val != compressedTokens {
		t.Errorf("tracker.PromptTokens = %d, want %d", val, compressedTokens)
	}
	if source == "no_data" {
		t.Errorf("tracker source = %q, should NOT be \"no_data\" after setTokenUsageAfterCompress", source)
	}
	if source != "restored" {
		t.Errorf("tracker source = %q, want \"restored\" (hadLLMCall must stay false)", source)
	}

	// hadLLMCall must be false so SaveState skips (prevents DB overwrite of
	// the correct compressed value that was already saved by SaveTokenState)
	if tracker.HadLLMCall() {
		t.Error("tracker.HadLLMCall() = true, want false (SaveState must skip)")
	}

	// Step 3: Subsequent LLM call overwrites tracker with real API value
	realTokens := int64(65000)
	tracker.RecordLLMCall(realTokens, 800)
	val, source = tracker.GetPromptTokens()
	if val != realTokens {
		t.Errorf("after RecordLLMCall: tracker.PromptTokens = %d, want %d", val, realTokens)
	}
	if source != "api" {
		t.Errorf("after RecordLLMCall: source = %q, want \"api\"", source)
	}

	// Step 4: SaveState should now write (hadLLMCall=true)
	saved := false
	tracker.SaveState(func(p, c int64) { saved = true })
	if !saved {
		t.Error("SaveState should write after RecordLLMCall")
	}

	// Step 5: But SaveState should have SKIPPED before RecordLLMCall
	// Use SetAfterCompress (the same API the engine uses) to verify
	// SaveState skips when hadLLMCall=false after compression.
	tracker2 := NewTokenTracker(170000, 5000)
	tracker2.ResetAfterCompress()
	tracker2.SetAfterCompress(compressedTokens) // same as setTokenUsageAfterCompress does
	// hadLLMCall is still false
	saved2 := false
	tracker2.SaveState(func(p, c int64) { saved2 = true })
	if saved2 {
		t.Error("SaveState should skip when hadLLMCall=false (after compression, before next LLM call)")
	}
}

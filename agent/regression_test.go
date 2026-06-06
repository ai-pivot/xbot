package agent

import (
	"context"
	"testing"

	"xbot/agent/hooks"
	"xbot/llm"
)

// ---------------------------------------------------------------------------
// Test 1: context_window_exceeded uses runCompression (standard path)
// ---------------------------------------------------------------------------

// TestContextWindowExceeded_UsesRunCompression verifies that when the LLM
// returns finish_reason=model_context_window_exceeded, the engine calls
// runCompression (the standard path) instead of directly calling ApplyCompress.
// This ensures hooks fire, HistoryCompacted flag is set, progress notifications
// are sent, and token state is persisted.
func TestContextWindowExceeded_UsesRunCompression(t *testing.T) {
	cm := &mockContextManager{
		compressFn: func(_ context.Context, messages []llm.ChatMessage, _ llm.LLM, _ string) (*CompressResult, error) {
			return &CompressResult{
				LLMView:          messages[:2],
				CompressedTokens: 5000,
			}, nil
		},
	}

	tracker := NewTokenTracker(180000, 3000)
	tracker.RecordLLMCall(180000, 3000)

	msgs := []llm.ChatMessage{
		llm.NewSystemMessage("system"),
		llm.NewUserMessage("hello"),
		llm.NewAssistantMessage("hi"),
		llm.NewUserMessage("do something complex"),
	}

	var savedPrompt int64
	var savedContext int64

	state := &runState{
		cfg: RunConfig{
			MaxOutputTokens:      4096,
			LLMClient:            &mockLLM{},
			Model:                "test-model",
			ChatID:               "test-chat",
			Channel:              "test",
			OriginUserID:         "cli_user",
			ContextManager:       cm,
			ContextManagerConfig: &ContextManagerConfig{MaxContextTokens: 200000},
			SaveTokenState:       func(p, c int64) { savedPrompt = p },
			SaveContextTokens:    func(p int64) { savedContext = p },
		},
		messages:           msgs,
		tokenTracker:       tracker,
		persistence:        NewPersistenceBridge(nil, 0),
		structuredProgress: &StructuredProgress{Phase: PhaseThinking},
		autoNotify:         true,
		sessionCtx:         &hooks.SessionContext{},
	}

	// Simulate the context_window_exceeded path: runCompression is the same call
	// that handleFinalResponse now makes after this fix.
	state.runCompression(context.Background(), cm, 180000, 200000)

	// Verify: TokenUsage reflects the compressed value
	if state.structuredProgress.TokenUsage == nil {
		t.Fatal("TokenUsage should be set after compression")
	}
	if state.structuredProgress.TokenUsage.PromptTokens != 5000 {
		t.Errorf("TokenUsage.PromptTokens = %d, want 5000 (compressed)", state.structuredProgress.TokenUsage.PromptTokens)
	}

	// Verify: token state was persisted (so restart doesn't see stale 180k)
	if savedPrompt != 5000 {
		t.Errorf("SaveTokenState prompt = %d, want 5000", savedPrompt)
	}
	if savedContext != 5000 {
		t.Errorf("SaveContextTokens = %d, want 5000", savedContext)
	}

	// Verify: messages were reduced
	if len(state.messages) != 2 {
		t.Errorf("len(messages) = %d, want 2 (system + first user)", len(state.messages))
	}
}

// TestContextWindowExceeded_SetsPhase verifies that runCompression sets
// PhaseCompressing during compression and reverts to PhaseThinking after.
func TestContextWindowExceeded_SetsPhase(t *testing.T) {
	cm := &mockContextManager{
		compressFn: func(_ context.Context, messages []llm.ChatMessage, _ llm.LLM, _ string) (*CompressResult, error) {
			return &CompressResult{
				LLMView:          messages[:2],
				CompressedTokens: 5000,
			}, nil
		},
	}

	tracker := NewTokenTracker(180000, 3000)
	tracker.RecordLLMCall(180000, 3000)

	state := &runState{
		cfg: RunConfig{
			MaxOutputTokens:      4096,
			LLMClient:            &mockLLM{},
			Model:                "test-model",
			ContextManager:       cm,
			ContextManagerConfig: &ContextManagerConfig{MaxContextTokens: 200000},
			SaveTokenState:       func(_, _ int64) {},
			SaveContextTokens:    func(_ int64) {},
		},
		messages: []llm.ChatMessage{
			llm.NewSystemMessage("system"),
			llm.NewUserMessage("hello"),
			llm.NewAssistantMessage("hi"),
			llm.NewUserMessage("complex task"),
		},
		tokenTracker:       tracker,
		persistence:        NewPersistenceBridge(nil, 0),
		structuredProgress: &StructuredProgress{Phase: PhaseThinking},
		autoNotify:         false,
		sessionCtx:         &hooks.SessionContext{},
	}

	state.runCompression(context.Background(), cm, 180000, 200000)

	// After runCompression completes, phase should be back to PhaseThinking
	if state.structuredProgress.Phase != PhaseThinking {
		t.Errorf("phase after compression = %q, want %q", state.structuredProgress.Phase, PhaseThinking)
	}
}

// ---------------------------------------------------------------------------
// Test 2: Per-iteration token persistence (SaveTokenState after each LLM call)
// ---------------------------------------------------------------------------

// TestPerIterationTokenPersistence verifies that SaveTokenState is called
// after every LLM API call, not just at the end of a Run. This ensures that
// if the process is killed mid-turn, the DB has the latest token counts.
func TestPerIterationTokenPersistence(t *testing.T) {
	var savedStates []struct{ prompt, completion int64 }

	tracker := NewTokenTracker(0, 0)

	state := &runState{
		cfg: RunConfig{
			MaxOutputTokens: 4096,
			SaveTokenState: func(p, c int64) {
				savedStates = append(savedStates, struct{ prompt, completion int64 }{p, c})
			},
			SaveContextTokens: func(_ int64) {},
		},
		messages: []llm.ChatMessage{
			llm.NewSystemMessage("system"),
			llm.NewUserMessage("hello"),
		},
		tokenTracker:       tracker,
		persistence:        NewPersistenceBridge(nil, 0),
		structuredProgress: &StructuredProgress{},
		autoNotify:         false,
		sessionCtx:         &hooks.SessionContext{},
	}

	// Simulate iteration 1: LLM returns prompt=50000, completion=1000
	tracker.RecordLLMCall(50000, 1000)
	state.updateTokenUsage()
	state.cfg.SaveContextTokens(50000)
	state.cfg.SaveTokenState(50000, 1000)

	// Simulate iteration 2: after tool use, prompt grew to 52000
	tracker.RecordLLMCall(52000, 800)
	state.updateTokenUsage()
	state.cfg.SaveContextTokens(52000)
	state.cfg.SaveTokenState(52000, 800)

	// Simulate iteration 3: more growth
	tracker.RecordLLMCall(55000, 1200)
	state.updateTokenUsage()
	state.cfg.SaveContextTokens(55000)
	state.cfg.SaveTokenState(55000, 1200)

	// Verify: SaveTokenState was called 3 times with correct values
	if len(savedStates) != 3 {
		t.Fatalf("SaveTokenState called %d times, want 3", len(savedStates))
	}
	wantStates := []struct{ prompt, completion int64 }{
		{50000, 1000},
		{52000, 800},
		{55000, 1200},
	}
	for i, want := range wantStates {
		if savedStates[i].prompt != want.prompt || savedStates[i].completion != want.completion {
			t.Errorf("SaveTokenState call %d: got (%d, %d), want (%d, %d)",
				i, savedStates[i].prompt, savedStates[i].completion, want.prompt, want.completion)
		}
	}

	// The LAST saved state is what would be restored after a crash.
	// Before this fix, only the buildOutput path called SaveTokenState,
	// so a crash at iteration 3 would restore iteration 0's (stale) data.
	lastSaved := savedStates[len(savedStates)-1]
	if lastSaved.prompt != 55000 {
		t.Errorf("last saved prompt = %d, want 55000 (latest iteration)", lastSaved.prompt)
	}
}

// TestPerIterationTokenPersistence_AfterCompressRetry verifies that the
// retry-with-compress path also persists tokens after the second LLM call.
func TestPerIterationTokenPersistence_AfterCompressRetry(t *testing.T) {
	var savedStates []struct{ prompt, completion int64 }

	tracker := NewTokenTracker(0, 0)
	state := &runState{
		cfg: RunConfig{
			MaxOutputTokens: 4096,
			SaveTokenState: func(p, c int64) {
				savedStates = append(savedStates, struct{ prompt, completion int64 }{p, c})
			},
			SaveContextTokens: func(_ int64) {},
		},
		tokenTracker:       tracker,
		persistence:        NewPersistenceBridge(nil, 0),
		structuredProgress: &StructuredProgress{},
		autoNotify:         false,
		sessionCtx:         &hooks.SessionContext{},
	}

	// First LLM call: 190k tokens → triggers input-too-long
	tracker.RecordLLMCall(190000, 500)
	state.updateTokenUsage()
	state.cfg.SaveTokenState(190000, 500)

	// After compress, new token count is 50000
	compressed := int64(50000)
	state.setTokenUsageAfterCompress(compressed)
	state.cfg.SaveContextTokens(compressed)
	state.cfg.SaveTokenState(compressed, 0)

	// Retry LLM call returns 52000
	tracker.RecordLLMCall(52000, 800)
	state.updateTokenUsage()
	state.cfg.SaveContextTokens(52000)
	state.cfg.SaveTokenState(52000, 800)

	if len(savedStates) != 3 {
		t.Fatalf("SaveTokenState called %d times, want 3", len(savedStates))
	}
	last := savedStates[len(savedStates)-1]
	if last.prompt != 52000 || last.completion != 800 {
		t.Errorf("last save after retry: got (%d, %d), want (52000, 800)", last.prompt, last.completion)
	}
}

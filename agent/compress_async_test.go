package agent

import (
	"context"
	"testing"
	"time"

	"xbot/agent/hooks"
	"xbot/llm"
	"xbot/memory"
)

// ---------------------------------------------------------------------------
// PR-1: Pre/Post compress hooks must NOT block the synchronous compression
// path (2026-09-02 incident: Pre 2m51s + Post 8m32s blocked the turn for
// 11m23s in total while the compression itself took only 75s).
// ---------------------------------------------------------------------------

// asyncHookMemory is a memory.MemoryProvider + CompressionAware mock whose
// PreCompress BLOCKS on a channel until the test releases it and whose
// PostCompress records its input. Used to prove the memory hooks left the
// synchronous path.
type asyncHookMemory struct {
	preFn  func(ctx context.Context, in memory.PreCompressInput) (*memory.PreCompressResult, error)
	postFn func(ctx context.Context, in memory.PostCompressInput) error
}

func (m *asyncHookMemory) Name() string { return "async-hook" }

func (m *asyncHookMemory) Recall(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

func (m *asyncHookMemory) Memorize(_ context.Context, _ memory.MemorizeInput) (memory.MemorizeResult, error) {
	return memory.MemorizeResult{}, nil
}

func (m *asyncHookMemory) Close() error { return nil }

func (m *asyncHookMemory) PreCompress(ctx context.Context, in memory.PreCompressInput) (*memory.PreCompressResult, error) {
	return m.preFn(ctx, in)
}

func (m *asyncHookMemory) PostCompress(ctx context.Context, in memory.PostCompressInput) error {
	return m.postFn(ctx, in)
}

func (m *asyncHookMemory) CompressContext(_ context.Context) (string, error) {
	return "", nil
}

// TestRunCompression_PrePostCompressAsync — the memory hooks must run async.
//
// RED (old sync implementation): runCompression calls PreCompress synchronously →
// a blocking PreCompress (2m51s in the incident) blocks the whole turn → the
// test deadline fires.
// GREEN: runCompression returns while Pre/Post hooks run in the background;
// PostCompress receives the compressed snapshot + the run's LLM client
// (bd3be203 parameterization) + a non-zero RemovedMessageCount.
func TestRunCompression_PrePostCompressAsync(t *testing.T) {
	preStarted := make(chan struct{})
	preRelease := make(chan struct{})
	postStarted := make(chan struct{})

	// Captured inputs — written by the background hooks, read by the test AFTER
	// the corresponding channel close (happens-before via channel send/close).
	var preInput memory.PreCompressInput
	var postInput *memory.PostCompressInput

	mem := &asyncHookMemory{
		preFn: func(_ context.Context, in memory.PreCompressInput) (*memory.PreCompressResult, error) {
			preInput = in
			close(preStarted)
			<-preRelease // block: simulates the 470k-char extraction LLM call
			return &memory.PreCompressResult{}, nil
		},
		postFn: func(_ context.Context, in memory.PostCompressInput) error {
			inCopy := in
			postInput = &inCopy
			close(postStarted)
			return nil
		},
	}

	cm := &mockContextManager{
		compressFn: func(_ context.Context, messages []llm.ChatMessage, _ llm.LLM, _ string, _ int64) (*CompressResult, error) {
			return &CompressResult{
				LLMView:          messages[:2], // system + first user
				SessionView:      messages[:2],
				CompressedTokens: 7,
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

	llmClient := &mockLLM{}
	state := &runState{
		cfg: RunConfig{
			MaxOutputTokens:      4096,
			LLMClient:            llmClient,
			Model:                "test-model",
			ChatID:               "test-chat",
			Channel:              "test",
			OriginUserID:         "cli_user",
			ContextManager:       cm,
			ContextManagerConfig: &ContextManagerConfig{MaxContextTokens: 200000},
			Memory:               mem,
		},
		messages:           msgs,
		tokenTracker:       tracker,
		persistence:        NewPersistenceBridge(nil, 0),
		structuredProgress: &StructuredProgress{Phase: PhaseThinking},
		autoNotify:         true,
		sessionCtx:         &hooks.SessionContext{},
	}

	done := make(chan error, 1)
	go func() {
		done <- state.runCompression(context.Background(), cm, 180000, 200000)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runCompression returned error: %v", err)
		}
		// Compression completed while PreCompress is STILL blocked → the hook
		// left the synchronous path (green). Old sync implementation blocks
		// inside PreCompress and the deadline below fires (red).
	case <-time.After(5 * time.Second):
		t.Fatal("runCompression blocked on Pre/PostCompress — memory hooks must run async " +
			"(2026-09-02 incident: Pre 2m51s + Post 8m32s on the synchronous path)")
	}

	// Compression applied its result synchronously.
	if len(state.messages) != 2 {
		t.Errorf("len(messages) = %d, want 2 (compression result applied)", len(state.messages))
	}

	// Release the blocked PreCompress; both hooks should complete in background.
	close(preRelease)
	select {
	case <-preStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("PreCompress never ran (background spawn missing)")
	}
	select {
	case <-postStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("PostCompress never ran after compression (background spawn missing)")
	}

	// PreCompress saw the PRE-compression message snapshot (all 4 messages).
	if len(preInput.MessagesToCompress) != 4 {
		t.Errorf("PreCompress got %d messages, want 4 (pre-compression snapshot)", len(preInput.MessagesToCompress))
	}

	// PostCompress got the run's LLM client (bd3be203: no shared-field race)
	// and a non-zero RemovedMessageCount (4 → 2; the OLD code computed
	// removedCount AFTER the swap, always 0).
	if postInput == nil {
		t.Fatal("PostCompress input not captured")
	}
	if postInput.LLMClient == nil {
		t.Error("PostCompress did not receive the run's LLMClient (bd3be203 parameterization)")
	}
	if postInput.RemovedMessageCount != 2 {
		t.Errorf("RemovedMessageCount = %d, want 2 (pre-swap message count 4 - compressed 2; "+
			"the old code computed it after the swap and always got 0)", postInput.RemovedMessageCount)
	}
}

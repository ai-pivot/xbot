package agent

import (
	"testing"
)

// ----------------------------------------------------------------
// NewTokenTracker
// ----------------------------------------------------------------

func TestTokenTracker_New_ZeroValues(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	if tt.PromptTokens() != 0 {
		t.Errorf("expected promptTokens=0, got %d", tt.PromptTokens())
	}
	if tt.CompletionTokens() != 0 {
		t.Errorf("expected completionTokens=0, got %d", tt.CompletionTokens())
	}
	if tt.RestoredFromDB() {
		t.Error("expected restoredFromDB=false for zero values")
	}
	if tt.HadLLMCall() {
		t.Error("expected hadLLMCall=false initially")
	}
}

func TestTokenTracker_New_NonZeroPromptTokens(t *testing.T) {
	tt := NewTokenTracker(500, 200)
	if tt.PromptTokens() != 500 {
		t.Errorf("expected promptTokens=500, got %d", tt.PromptTokens())
	}
	if tt.CompletionTokens() != 200 {
		t.Errorf("expected completionTokens=200, got %d", tt.CompletionTokens())
	}
	if !tt.RestoredFromDB() {
		t.Error("expected restoredFromDB=true when promptTokens > 0")
	}
}

func TestTokenTracker_New_NonZeroCompletionOnly(t *testing.T) {
	// completionTokens > 0 but promptTokens == 0 → restoredFromDB should be false
	tt := NewTokenTracker(0, 300)
	if tt.RestoredFromDB() {
		t.Error("expected restoredFromDB=false when promptTokens=0 even if completionTokens>0")
	}
}

// ----------------------------------------------------------------
// RecordLLMCall
// ----------------------------------------------------------------

func TestTokenTracker_RecordLLMCall(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	tt.RecordLLMCall(1000, 250)

	if tt.PromptTokens() != 1000 {
		t.Errorf("expected promptTokens=1000, got %d", tt.PromptTokens())
	}
	if tt.CompletionTokens() != 250 {
		t.Errorf("expected completionTokens=250, got %d", tt.CompletionTokens())
	}
	if !tt.HadLLMCall() {
		t.Error("expected hadLLMCall=true after RecordLLMCall")
	}
}

func TestTokenTracker_RecordLLMCall_Overwrite(t *testing.T) {
	tt := NewTokenTracker(100, 50)
	tt.RecordLLMCall(2000, 400)

	if tt.PromptTokens() != 2000 {
		t.Errorf("expected promptTokens=2000, got %d", tt.PromptTokens())
	}
	if tt.CompletionTokens() != 400 {
		t.Errorf("expected completionTokens=400, got %d", tt.CompletionTokens())
	}
}

// ----------------------------------------------------------------
// ResetAfterCompress
// ----------------------------------------------------------------

func TestTokenTracker_ResetAfterCompress(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	tt.RecordLLMCall(5000, 800)

	// After compression, all tracking fields should be zeroed.
	// The tracker returns "no_data" until the next LLM API call.
	tt.ResetAfterCompress()

	if tt.PromptTokens() != 0 {
		t.Errorf("expected promptTokens=0 after compress reset, got %d", tt.PromptTokens())
	}
	if tt.CompletionTokens() != 0 {
		t.Errorf("expected completionTokens=0 after compress reset, got %d", tt.CompletionTokens())
	}
	if tt.HadLLMCall() {
		t.Error("expected hadLLMCall=false after compress reset")
	}
	// Verify GetPromptTokens returns no_data
	total, source := tt.GetPromptTokens()
	if total != 0 {
		t.Errorf("expected total=0 after compress reset, got %d", total)
	}
	if source != "no_data" {
		t.Errorf("expected source='no_data' after compress reset, got %q", source)
	}
}

// ----------------------------------------------------------------
// MarkRestoredFromDB
// ----------------------------------------------------------------

func TestTokenTracker_MarkRestoredFromDB(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	if tt.RestoredFromDB() {
		t.Error("expected restoredFromDB=false initially")
	}
	tt.MarkRestoredFromDB()
	if !tt.RestoredFromDB() {
		t.Error("expected restoredFromDB=true after MarkRestoredFromDB")
	}
}

// ----------------------------------------------------------------
// GetPromptTokens
// ----------------------------------------------------------------

func TestTokenTracker_GetPromptTokens_API(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	tt.RecordLLMCall(1000, 200)

	total, source := tt.GetPromptTokens()
	if total != 1000 {
		t.Errorf("expected total=1000, got %d", total)
	}
	if source != "api" {
		t.Errorf("expected source='api', got %q", source)
	}
}

func TestTokenTracker_GetPromptTokens_Restored(t *testing.T) {
	tt := NewTokenTracker(800, 300)
	// No RecordLLMCall → restored from previous Run

	total, source := tt.GetPromptTokens()
	if total != 800 {
		t.Errorf("expected total=800, got %d", total)
	}
	if source != "restored" {
		t.Errorf("expected source='restored', got %q", source)
	}
}

func TestTokenTracker_GetPromptTokens_NoData(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	// No tokens, no LLM call

	total, source := tt.GetPromptTokens()
	if total != 0 {
		t.Errorf("expected total=0, got %d", total)
	}
	if source != "no_data" {
		t.Errorf("expected source='no_data', got %q", source)
	}
}

// ----------------------------------------------------------------
// SaveState
// ----------------------------------------------------------------

func TestTokenTracker_SaveState_NilFn(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	tt.RecordLLMCall(1000, 200)
	// Should not panic with nil saveFn
	tt.SaveState(nil)
}

func TestTokenTracker_SaveState_NoLLMCall(t *testing.T) {
	called := false
	tt := NewTokenTracker(500, 100) // has tokens from DB, but no LLM call in this Run
	tt.SaveState(func(pt, ct int64) {
		called = true
	})
	if called {
		t.Error("expected saveFn NOT to be called when no LLM call in this Run")
	}
}

func TestTokenTracker_SaveState_ZeroPromptTokens(t *testing.T) {
	called := false
	tt := NewTokenTracker(0, 0)
	// Manually set hadLLMCall without promptTokens
	tt.RecordLLMCall(0, 100)
	tt.SaveState(func(pt, ct int64) {
		called = true
	})
	if called {
		t.Error("expected saveFn NOT to be called when promptTokens=0")
	}
}

func TestTokenTracker_SaveState_Called(t *testing.T) {
	var savedPrompt, savedCompletion int64
	tt := NewTokenTracker(0, 0)
	tt.RecordLLMCall(1000, 250)

	tt.SaveState(func(pt, ct int64) {
		savedPrompt = pt
		savedCompletion = ct
	})

	if savedPrompt != 1000 {
		t.Errorf("expected savedPrompt=1000, got %d", savedPrompt)
	}
	if savedCompletion != 250 {
		t.Errorf("expected savedCompletion=250, got %d", savedCompletion)
	}
}

// ----------------------------------------------------------------
// Getters
// ----------------------------------------------------------------

func TestTokenTracker_Getters(t *testing.T) {
	tt := NewTokenTracker(100, 50)

	// Before any LLM call
	if tt.PromptTokens() != 100 {
		t.Errorf("PromptTokens() = %d, want 100", tt.PromptTokens())
	}
	if tt.CompletionTokens() != 50 {
		t.Errorf("CompletionTokens() = %d, want 50", tt.CompletionTokens())
	}
	if tt.HadLLMCall() {
		t.Error("HadLLMCall() = true, want false")
	}
	if !tt.RestoredFromDB() {
		t.Error("RestoredFromDB() = false, want true")
	}

	// After LLM call
	tt.RecordLLMCall(2000, 400)
	if tt.PromptTokens() != 2000 {
		t.Errorf("PromptTokens() = %d, want 2000", tt.PromptTokens())
	}
	if tt.CompletionTokens() != 400 {
		t.Errorf("CompletionTokens() = %d, want 400", tt.CompletionTokens())
	}
	if !tt.HadLLMCall() {
		t.Error("HadLLMCall() = false, want true")
	}
}

package agent

// TokenTracker manages token accounting for a single Run() execution.
// All token counts come from API responses — never from local estimation.
type TokenTracker struct {
	promptTokens     int64
	completionTokens int64
	hadLLMCall       bool
	restoredFromDB   bool // true when initialized from previous Run's persisted token counts
}

// NewTokenTracker creates a TokenTracker, optionally seeded with token counts
// restored from the previous Run (via SaveTokenState / DB).
func NewTokenTracker(lastPromptTokens, lastCompletionTokens int64) *TokenTracker {
	return &TokenTracker{
		promptTokens:     lastPromptTokens,
		completionTokens: lastCompletionTokens,
		restoredFromDB:   lastPromptTokens > 0,
	}
}

// RecordLLMCall records the token counts returned by an LLM API call.
func (t *TokenTracker) RecordLLMCall(prompt, completion int64) {
	t.promptTokens = prompt
	t.completionTokens = completion
	t.hadLLMCall = true
}

// ResetAfterCompress resets token state after context compression.
// All fields are zeroed — the tracker returns "no_data" until the next LLM API call
// provides real token counts. This prevents infinite compression loops caused by
// inaccurate local estimates being treated as authoritative.
func (t *TokenTracker) ResetAfterCompress() {
	t.promptTokens = 0
	t.completionTokens = 0
	t.hadLLMCall = false
	t.restoredFromDB = false
}

// MarkRestoredFromDB marks that token counts were restored from DB/session.
func (t *TokenTracker) MarkRestoredFromDB() {
	t.restoredFromDB = true
}

// GetPromptTokens returns the current prompt token count for compression decisions.
// Returns (value, source) where source indicates the data origin:
//   - "api": from a real LLM API call in this Run
//   - "restored": from previous Run / DB
//   - "no_data": no token data available — should never trigger compression
func (t *TokenTracker) GetPromptTokens() (int64, string) {
	if t.promptTokens <= 0 {
		return 0, "no_data"
	}
	if t.hadLLMCall {
		return t.promptTokens, "api"
	}
	return t.promptTokens, "restored"
}

// SaveState calls saveFn if the tracker has recorded at least one LLM call
// and has positive prompt tokens (i.e. there is meaningful state to save).
func (t *TokenTracker) SaveState(saveFn func(promptTokens, completionTokens int64)) {
	if saveFn == nil || !t.hadLLMCall || t.promptTokens <= 0 {
		return
	}
	saveFn(t.promptTokens, t.completionTokens)
}

// --- Getters ---

func (t *TokenTracker) PromptTokens() int64     { return t.promptTokens }
func (t *TokenTracker) CompletionTokens() int64 { return t.completionTokens }
func (t *TokenTracker) HadLLMCall() bool        { return t.hadLLMCall }
func (t *TokenTracker) RestoredFromDB() bool    { return t.restoredFromDB }

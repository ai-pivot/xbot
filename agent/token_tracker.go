package agent

import (
	"xbot/llm"
)

// TokenTracker manages token accounting for a single Run() execution.
// All token counts come from API responses — never from local estimation.
type TokenTracker struct {
	promptTokens     int64
	completionTokens int64
	msgCountAtCall   int // boundary for delta calculation (len(messages) at last LLM call)
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
// msgCount is len(messages) at the time of the call, used as a boundary
// for delta calculation in EstimateTotal.
func (t *TokenTracker) RecordLLMCall(prompt, completion int64, msgCount int) {
	t.promptTokens = prompt
	t.completionTokens = completion
	t.msgCountAtCall = msgCount
	t.hadLLMCall = true
}

// ResetAfterCompress resets token state after context compression.
func (t *TokenTracker) ResetAfterCompress(newPromptTokens int64, msgCount int) {
	t.promptTokens = newPromptTokens
	t.completionTokens = 0
	t.msgCountAtCall = msgCount
}

// MarkRestoredFromDB marks that token counts were restored from DB/session.
func (t *TokenTracker) MarkRestoredFromDB() {
	t.restoredFromDB = true
}

// EstimateTotal returns the total token count for the given messages
// and a source string describing the data origin.
//
// Token accounting strategy (no estimation, ever):
//  1. "api_prompt+tool_delta" — In-Run, had LLM call + tool messages after it
//     (delta is estimated via tiktoken — acceptable since it's a small additive
//     correction for tool results not yet seen by the API)
//  2. "api_prompt" — In-Run, had LLM call, no new tool messages
//  3. "restored" — No in-Run LLM call yet, but restored from previous Run / DB
//  4. "no_data" — No API data at all. Returns 0 — never triggers compression.
func (t *TokenTracker) EstimateTotal(messages []llm.ChatMessage, model string) (total int64, source string) {
	if t.promptTokens > 0 && t.msgCountAtCall > 0 {
		// In-Run path: we've had at least one LLM call in this Run.
		total = t.promptTokens
		source = "api_prompt"
		if len(messages) > t.msgCountAtCall+1 {
			// Add tokens for tool result messages appended after the last LLM call.
			// This delta is the only place we use local counting — it's a small
			// additive correction for tool results not yet seen by the API.
			toolMsgs := messages[t.msgCountAtCall+1:]
			deltaTokens, deltaErr := llm.CountMessagesTokens(toolMsgs, model)
			if deltaErr != nil {
				source = "api_prompt+tool_delta_err"
			} else {
				total += int64(deltaTokens)
				source = "api_prompt+tool_delta"
			}
		}
		return total, source
	}

	if t.promptTokens > 0 {
		// Restored from previous Run (DB or in-memory) — use exact API prompt_tokens.
		return t.promptTokens, "restored"
	}

	// No API token data available. Return 0 — do NOT estimate.
	// Compression is never triggered without real data.
	return 0, "no_data"
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
func (t *TokenTracker) MsgCountAtCall() int     { return t.msgCountAtCall }
func (t *TokenTracker) HadLLMCall() bool        { return t.hadLLMCall }
func (t *TokenTracker) RestoredFromDB() bool    { return t.restoredFromDB }

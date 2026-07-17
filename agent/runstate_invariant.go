package agent

import (
	"context"
	"fmt"

	log "xbot/logger"
)

// ValidateInvariants checks internal consistency of the runState.
// It is intended for debug-mode validation at key transition points
// (after LLM calls, after compression, after persistence).
// Returns nil if all invariants hold, or an error describing the first violation.
func (s *runState) ValidateInvariants() error {
	// Invariant 1: persistence watermark must not exceed message count
	if pc := s.persistence.LastPersistedCount(); pc > len(s.messages) {
		return fmt.Errorf("invariant violation: LastPersistedCount(%d) > len(messages)(%d)", pc, len(s.messages))
	}

	// Invariant 2: promptTokens consistency.
	// - promptTokens > 0 without any source (LLM call or DB restore) → violation
	// - promptTokens == 0 with restoredFromDB → violation (DB restore should have data)
	// - promptTokens == 0 with hadLLMCall is allowed (API may return 0 for cache hits)
	if s.tokenTracker.PromptTokens() > 0 && !s.tokenTracker.HadLLMCall() && !s.tokenTracker.RestoredFromDB() {
		return fmt.Errorf("invariant violation: promptTokens=%d but hadLLMCall=false restoredFromDB=false",
			s.tokenTracker.PromptTokens())
	}
	if s.tokenTracker.PromptTokens() == 0 && s.tokenTracker.RestoredFromDB() {
		return fmt.Errorf("invariant violation: promptTokens=0 but restoredFromDB=true (should have had data)")
	}

	return nil
}

// validateInvariantsAt logs invariant violations at debug level.
// Intended as a non-intrusive check at key transition points.
func (s *runState) validateInvariantsAt(ctx context.Context, point string) {
	if err := s.ValidateInvariants(); err != nil {
		log.Req(ctx, log.CatAgent).WithField("invariant_check", point).WithError(err).Debug("runState invariant violation")
	}
}

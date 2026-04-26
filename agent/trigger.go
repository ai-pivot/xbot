package agent

// Compression threshold constants.
// These control the tiered context management strategy:
//
//	0.60  maskingThreshold   — observation masking activates (lightweight, no LLM call)
//	0.65  snipThreshold      — free snip layer trims old tool results
//	0.75  compactThreshold   — full LLM-based context compression triggers
const (
	compactThreshold    = 0.75 // Token usage ratio that triggers full compression
	snipThreshold       = 0.65 // Token usage ratio that triggers free snip layer
	maskingThreshold    = 0.60 // Token usage ratio that triggers observation masking
	minReductionRate    = 0.10 // Minimum reduction to consider compression effective
	tokenEstimateMargin = 1.5  // Safety margin for local token estimation fallback
)

// shouldCompact returns true when the token count exceeds the compaction
// threshold (compactThreshold of max). This replaces the previous 3-factor dynamic
// threshold with a simple headroom check.
func shouldCompact(totalTokens, maxTokens int) bool {
	if maxTokens <= 0 {
		return false
	}
	return float64(totalTokens) >= float64(maxTokens)*compactThreshold
}

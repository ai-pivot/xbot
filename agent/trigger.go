package agent

import "xbot/llm"

// shouldCompact returns true when the token count exceeds the compaction
// threshold (default 90% of max, configurable via CompressionThreshold).
// This replaces the previous 3-factor dynamic threshold with a simple headroom check.
func shouldCompact(totalTokens, maxTokens int, threshold float64) bool {
	if maxTokens <= 0 {
		return false
	}
	if threshold <= 0 {
		threshold = 0.9
	}
	return float64(totalTokens) >= float64(maxTokens)*threshold
}

// estimateMessagesTokens estimates the token count of a FULL message slice
// (system prompt + summary + continuation + tail — everything the next LLM
// call will send), using the same chars×2/3 heuristic as compactMessages'
// per-message estimates. Tool-call arguments on assistant messages count too
// (they are serialized into the request prompt).
//
// This is the measurement ruler for the POST-COMPRESSION budget check. It must
// NEVER be used for the compression TRIGGER decision — that uses the exact
// API-returned prompt_tokens exclusively (TokenTracker). The trigger and the
// "did compression actually fit the budget" check are two different questions:
// the first asks "how big is the context right now" (API truth), the second
// asks "how big is the context compression just produced" (estimated, because
// no API call has seen the compressed messages yet).
//
// Root cause of the infinite-compression loop this replaces: the post-compress
// check compared against CompressedTokens — the SUMMARY-only estimate that
// excludes system prompt and tail — so a compressed result of [huge system +
// 150 tail messages] always "passed" the check while the real context stayed
// above the trigger line, and compression re-fired every 5 iterations forever.
func estimateMessagesTokens(messages []llm.ChatMessage) int64 {
	var total int64
	for _, m := range messages {
		total += int64(len([]rune(m.Content)) * 2 / 3)
		for _, tc := range m.ToolCalls {
			total += int64(len([]rune(tc.Arguments)) * 2 / 3)
		}
	}
	return total
}

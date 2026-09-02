package agent

import (
	"testing"

	"xbot/llm"
)

// ---------------------------------------------------------------------------
// PR #336 review defect 2: capTailLength must align to tool_calls pairing
// boundaries. When the tail cap lands BETWEEN an assistant(tool_calls) and
// its tool results (the assistant goes to toCompress, the orphan tool
// messages start the tail), the orphan tool messages get stripped by
// SanitizeMessages in the verbatim request → prefix no longer byte-identical
// (radix cache miss) + that context silently lost.
// ---------------------------------------------------------------------------

// TestCapTailLengthAlignsToToolPairBoundary — a capped tail starting on an
// orphan tool message (its assistant(tool_calls) landed in toCompress) must
// roll back to include the paired assistant, keeping the pair intact.
func TestCapTailLengthAlignsToToolPairBoundary(t *testing.T) {
	// Build 57 messages; maxTailMessages=50 (maxContextTokens=66666 →
	// int(66666*0.15/200)=49 → floor 50) → the raw cap lands at index 7.
	// Layout puts an ORPHAN tool message at index 7 (its paired
	// assistant(tool_calls) at index 6).
	messages := make([]llm.ChatMessage, 0, 57)
	messages = append(messages, llm.NewSystemMessage("sys"))                             // 0
	messages = append(messages, llm.NewUserMessage("q1"))                                // 1
	messages = append(messages, llm.NewAssistantMessage("a1"))                           // 2
	messages = append(messages, llm.NewUserMessage("q2"))                                // 3 — tailStart (last user)
	messages = append(messages, llm.NewUserMessage("f1"), llm.NewAssistantMessage("f2")) // 4,5 filler
	messages = append(messages, llm.ChatMessage{                                         // 6 — assistant(tool_calls)
		Role: "assistant",
		ToolCalls: []llm.ToolCall{{
			ID:        "tc1",
			Name:      "Shell",
			Arguments: `{"command":"ls"}`,
		}},
	})
	messages = append(messages, llm.NewToolMessage("Shell", "tc1", `{"command":"ls"}`, "file1"))  // 7 — ORPHAN tool (raw cap lands here)
	messages = append(messages, llm.NewToolMessage("Shell", "tc1b", `{"command":"ls"}`, "file2")) // 8
	for i := 0; i < 48; i++ {                                                                     // 9..56 filler
		messages = append(messages, llm.NewAssistantMessage("f"))
	}
	if len(messages) != 57 {
		t.Fatalf("test setup: want 57 messages, got %d", len(messages))
	}
	if messages[7].Role != "tool" || messages[6].Role != "assistant" || len(messages[6].ToolCalls) == 0 {
		t.Fatalf("test setup: index 7 must be an orphan tool message with its paired assistant(tool_calls) at 6 (roles: %q/%q)", messages[6].Role, messages[7].Role)
	}

	got := capTailLength(messages, 3, 66666) // raw cap = 57-50 = 7 (the orphan tool)

	if got < 3 || got >= len(messages) {
		t.Fatalf("cap out of range: %d", got)
	}
	if messages[got].Role == "tool" {
		t.Errorf("BUG REPRODUCED: capped tail starts on an orphan tool message (index %d) — "+
			"its assistant(tool_calls) is at index %d, left in toCompress. SanitizeMessages "+
			"strips the orphan tool in the verbatim request → radix cache miss + lost context.",
			got, got-1)
	}
	// After the fix, the tail must start at the paired assistant (index 6)
	// — the [assistant(tool_calls), tool...] pair stays intact.
	if got != 6 {
		t.Errorf("post-fix: tail should roll back to the paired assistant at index 6, got %d (role=%q)", got, messages[got].Role)
	}
}

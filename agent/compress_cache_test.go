package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"xbot/llm"
)

// ---------------------------------------------------------------------------
// PR-2: compaction request cache hit — the compression request must reuse the
// conversation's exact prefix (original system + verbatim history + trailing
// instruction user msg) so the radix cache hits the whole history instead of
// re-prefilling 900k tokens.
//
// Old (red): compactMessages flattened the history into text ([USER] ... lines,
// 2000-rune truncation, omitted old messages) behind a NEW system prompt
// ("context compaction expert") → prefix differs from the running conversation
// → zero radix cache hit.
// New (green): [original system, ...verbatim to-compress messages, instruction]
// → prefix = the conversation the model just served → cache hit.
// ---------------------------------------------------------------------------

// capturingLLM records every request's messages so tests can assert the exact
// prompt structure. Implements only llm.LLM (non-stream) — the compression
// engine.Run falls back to Generate.
type capturingLLM struct {
	mu       sync.Mutex
	calls    [][]llm.ChatMessage
	models   []string
	response string
}

func (c *capturingLLM) Generate(_ context.Context, model string, messages []llm.ChatMessage, _ []llm.ToolDefinition, _ string) (*llm.LLMResponse, error) {
	c.mu.Lock()
	c.calls = append(c.calls, messages)
	c.models = append(c.models, model)
	c.mu.Unlock()
	return &llm.LLMResponse{Content: c.response, FinishReason: llm.FinishReasonStop}, nil
}

func (c *capturingLLM) ListModels() []string { return nil }

// TestCompactMessages_VerbatimHistoryCacheHit — the compaction request must be
// [original system, ...verbatim to-compress, instruction user msg]:
//  1. The system message is byte-identical to the conversation's system.
//  2. The history messages are passed VERBATIM (tool messages keep their tool
//     role + tool_calls structure — no "[USER]"/"[ASSISTANT]" flatten lines,
//     no 2000-rune truncation, no "[N older messages omitted]").
//  3. The LAST message is the compaction instruction (user role, "Output ONLY").
//  4. The result structure is unchanged: [system, [Compacted context], tail].
func TestCompactMessages_VerbatimHistoryCacheHit(t *testing.T) {
	client := &capturingLLM{response: "### Task Summary\nCompacted test summary."}

	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are xbot, an agent working in a repo. TOOLS: Shell, Read, FileReplace."),
		llm.NewUserMessage("please fix the login bug in auth.go"),
		{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:        "tc1",
				Name:      "Read",
				Arguments: `{"path":"/repo/auth.go"}`,
			}},
		},
		// ToolCallID MUST pair with the assistant's tool call — SanitizeMessages
		// strips orphaned tool messages (correct engine behavior; live
		// conversations always carry the pairing).
		llm.NewToolMessage("Read", "tc1", `{"path":"/repo/auth.go"}`, "package auth\nfunc Login() {} // 400 lines of file content"),
		llm.NewAssistantMessage("I see the bug: empty password check is inverted. Fixing."),
		// Tail cut point: last user message — tail = [user, assistant(tool), tool].
		llm.NewUserMessage("continue and run the tests"),
		{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:        "tc2",
				Name:      "Shell",
				Arguments: `{"command":"go test ./..."}`,
			}},
		},
		llm.NewToolMessage("Shell", "tc2", `{"command":"go test ./..."}`, "PASS ok\tfake_test.go"),
	}

	result, err := compactMessages(context.Background(), messages, client, "test-model", 200000)
	if err != nil {
		t.Fatalf("compactMessages failed: %v", err)
	}

	if len(client.calls) != 1 {
		t.Fatalf("expected exactly 1 LLM call, got %d", len(client.calls))
	}
	req := client.calls[0]

	// 1. The system message must be the ORIGINAL system prompt, byte-identical —
	//    radix cache requires an exact prefix match. The old implementation
	//    replaced it with a fresh "context compaction expert" system.
	if len(req) < 2 {
		t.Fatalf("compaction request too short: %d messages", len(req))
	}
	if req[0].Role != "system" || req[0].Content != messages[0].Content {
		t.Errorf("request[0] must be the ORIGINAL system message (byte-identical prefix for radix cache).\n"+
			"got role=%q content[0:80]=%q", req[0].Role, truncateRunes(req[0].Content, 80))
	}

	// 2. The history messages must be VERBATIM — the tool message keeps its
	//    tool role and the assistant message keeps its tool_calls structure.
	//    (toCompress = messages[1..5]: user, assistant(tool), tool, assistant —
	//    the last user msg "continue..." starts the tail.)
	var sawToolMsg, sawToolCall bool
	for _, m := range req[1 : len(req)-1] {
		if m.Role == "tool" && strings.Contains(m.Content, "package auth") {
			sawToolMsg = true
		}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 && m.ToolCalls[0].Name == "Read" {
			sawToolCall = true
		}
	}
	if !sawToolMsg {
		t.Error("compaction request must contain the VERBATIM tool message (role=tool) — " +
			"the flatten path turns it into '[TOOL] ...' text and loses the tool structure")
	}
	if !sawToolCall {
		t.Error("compaction request must preserve the assistant message's tool_calls structure verbatim")
	}
	// No flatten artifacts anywhere.
	for _, m := range req {
		if strings.HasPrefix(m.Content, "[USER]") || strings.HasPrefix(m.Content, "[ASSISTANT]") || strings.HasPrefix(m.Content, "[TOOL]") {
			t.Errorf("flatten artifact found in request: %q", truncateRunes(m.Content, 60))
		}
		if strings.Contains(m.Content, "older messages omitted from compaction") {
			t.Errorf("omission note found in request — verbatim history must not omit messages: %q", truncateRunes(m.Content, 60))
		}
	}

	// 3. The LAST message is the compaction instruction (user role).
	lastMsg := req[len(req)-1]
	if lastMsg.Role != "user" {
		t.Errorf("last request message must be the instruction (user role), got role=%q", lastMsg.Role)
	}
	if !strings.Contains(lastMsg.Content, "Output ONLY") {
		t.Errorf("instruction must demand summary-only output, got: %q", truncateRunes(lastMsg.Content, 120))
	}
	if !strings.Contains(lastMsg.Content, "Recent Work") {
		t.Errorf("instruction must carry the structured-sections requirements (Recent Work etc.), got: %q", truncateRunes(lastMsg.Content, 120))
	}

	// 4. Result structure unchanged: [system, [Compacted context], ..., tail].
	if len(result.LLMView) < 2 || result.LLMView[0].Role != "system" {
		t.Fatalf("LLMView must start with the system message, got %d messages", len(result.LLMView))
	}
	var hasCompacted bool
	for _, m := range result.LLMView {
		if m.Role == "user" && strings.HasPrefix(m.Content, "[Compacted context]") {
			hasCompacted = true
			break
		}
	}
	if !hasCompacted {
		t.Error("LLMView must contain the [Compacted context] summary user message")
	}
	// Tail preserved verbatim (the last user msg + its tool exchange).
	var sawTailUser, sawTailTool bool
	for _, m := range result.LLMView {
		if m.Content == "continue and run the tests" {
			sawTailUser = true
		}
		if m.Role == "tool" && strings.Contains(m.Content, "PASS ok") {
			sawTailTool = true
		}
	}
	if !sawTailUser || !sawTailTool {
		t.Errorf("tail messages must be preserved verbatim in LLMView (user=%v tool=%v)", sawTailUser, sawTailTool)
	}
}

// TestCompactMessages_FlattenFallbackWhenOverBudget — when the verbatim history
// does not fit the compression budget (manual /compress near the limit), the
// flatten path (formatCompactLine) is the fallback: [expert system, flattened
// history]. The request then contains the flatten artifact but still succeeds.
func TestCompactMessages_FlattenFallbackWhenOverBudget(t *testing.T) {
	client := &capturingLLM{response: "### Task Summary\nFallback path summary."}

	messages := []llm.ChatMessage{
		llm.NewSystemMessage("system prompt that is kept"),
		llm.NewUserMessage(strings.Repeat("very long history content ", 40000)), // ~1M chars → over a tiny budget
		llm.NewAssistantMessage("ack"),
		llm.NewUserMessage("latest request"), // tail cut
	}

	result, err := compactMessages(context.Background(), messages, client, "test-model", 20000) // tiny budget → cannot fit verbatim
	if err != nil {
		t.Fatalf("compactMessages failed: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected exactly 1 LLM call, got %d", len(client.calls))
	}
	req := client.calls[0]
	// Fallback path: expert system + single flattened user prompt.
	if len(req) != 2 || req[0].Role != "system" || req[1].Role != "user" {
		t.Fatalf("fallback request must be [expert system, flattened user], got %d messages", len(req))
	}
	if len(result.LLMView) == 0 {
		t.Error("fallback path must still produce a compressed LLMView")
	}
}

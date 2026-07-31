package agent

import (
	"context"
	"strings"
	"testing"

	"xbot/llm"
)

func makeAssistantWithToolCalls(content string, toolCalls ...llm.ToolCall) llm.ChatMessage {
	return llm.ChatMessage{
		Role:      "assistant",
		Content:   content,
		ToolCalls: toolCalls,
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"hello world", 5, "hello...[truncated]"},
		{"你好世界测试", 3, "你好世...[truncated]"},
		{"", 5, ""},
	}
	for _, tt := range tests {
		got := truncateRunes(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

func TestExtractDialogueFromTail_Basic(t *testing.T) {
	tail := []llm.ChatMessage{
		llm.NewUserMessage("hello"),
		llm.NewAssistantMessage("hi there"),
	}
	result := extractDialogueFromTail(tail)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].Role != "user" || result[0].Content != "hello" {
		t.Errorf("expected user message, got %+v", result[0])
	}
	if result[1].Role != "assistant" || result[1].Content != "hi there" {
		t.Errorf("expected assistant message, got %+v", result[1])
	}
}

func TestExtractDialogueFromTail_ToolGroupFolding(t *testing.T) {
	tail := []llm.ChatMessage{
		llm.NewUserMessage("do something"),
		{
			Role:      "assistant",
			Content:   "let me check",
			ToolCalls: []llm.ToolCall{{ID: "1", Name: "Read", Arguments: `{"path":"foo.go"}`}},
		},
		{Role: "tool", Content: "file content here"},
		llm.NewAssistantMessage("done"),
	}
	result := extractDialogueFromTail(tail)

	if len(result) != 3 {
		t.Fatalf("expected 3 messages (user, folded-assistant, assistant), got %d", len(result))
	}
	if result[0].Content != "do something" {
		t.Error("first message should be user")
	}
	if !strings.Contains(result[1].Content, "Read") {
		t.Error("folded message should mention tool name")
	}
	if result[2].Content != "done" {
		t.Error("last message should be final assistant")
	}
}

func TestExtractDialogueFromTail_OffloadStrip(t *testing.T) {
	tail := []llm.ChatMessage{
		{
			Role:      "assistant",
			Content:   "",
			ToolCalls: []llm.ToolCall{{ID: "1", Name: "Read", Arguments: "{}"}},
		},
		{Role: "tool", Content: "📂 [offload:ol_abc123] Read(foo.go)\nfile summary here"},
	}
	result := extractDialogueFromTail(tail)
	if len(result) != 1 {
		t.Fatalf("expected 1 folded message, got %d", len(result))
	}
	if strings.Contains(result[0].Content, "ol_abc123") {
		t.Error("offload ID should be stripped")
	}
	if !strings.Contains(result[0].Content, "foo.go") {
		t.Error("should preserve the tool info after stripping ID")
	}
}

func TestTruncateArgs(t *testing.T) {
	short := "short"
	if truncateArgs(short, 10) != "short" {
		t.Error("short args should not be truncated")
	}
	long := strings.Repeat("x", 200)
	result := truncateArgs(long, 100)
	if len([]rune(result)) > 104 {
		t.Errorf("long args should be truncated, got len=%d", len([]rune(result)))
	}
	if !strings.HasSuffix(result, "...") {
		t.Error("truncated args should end with ...")
	}
}

// TestCompactMessages_PreservesOriginalUserMsg verifies that the original user message
// (the user's actual request) is never compressed away even when tail capping would
// normally truncate it. This is a regression test: before the fix, a long tail of
// tool iterations could push tailStart past the original user message, causing it to
// be summarized by the LLM instead of preserved verbatim.
func TestCompactMessages_PreservesOriginalUserMsg(t *testing.T) {
	// Build messages: [system] + [user: "original request"] + [many tool iterations]
	// With maxContextTokens=100000 → maxTailMessages = 100000*0.15/200 = 75
	// We'll create 200 messages in the tail to trigger capping, which would push
	// tailStart past the original user message without the fix.
	messages := []llm.ChatMessage{
		llm.NewSystemMessage("system prompt"),
	}

	// Add some history (old user/assistant pairs) to simulate pre-existing context
	for i := 0; i < 10; i++ {
		messages = append(messages, llm.NewUserMessage("old question"))
		messages = append(messages, llm.NewAssistantMessage("old answer"))
	}

	// The original user message (this is what must be preserved)
	originalUserContent := "请帮我修复 compress.go 中的 bug，确保原始 user msg 不丢失"
	messages = append(messages, llm.NewUserMessage(originalUserContent))

	// Add many tool iterations (assistant + tool messages) to create a long tail
	for i := 0; i < 200; i++ {
		messages = append(messages, llm.ChatMessage{
			Role:      "assistant",
			ToolCalls: []llm.ToolCall{{ID: "tc" + string(rune(i)), Name: "Shell", Arguments: `{"command":"echo hello"}`}},
		})
		messages = append(messages, llm.ChatMessage{
			Role:    "tool",
			Content: "tool output",
		})
	}

	// Use a mock LLM that returns a valid summary
	mockClient := &mockLLM{
		responses: []llm.LLMResponse{
			{
				Content: "Compacted summary of previous work",
				Usage: llm.TokenUsage{
					PromptTokens:     1000,
					CompletionTokens: 500,
				},
			},
		},
	}

	result, err := compactMessages(context.Background(), messages, mockClient, "test-model", 100000)
	if err != nil {
		t.Fatalf("compactMessages failed: %v", err)
	}

	// The original user message MUST be present in LLMView
	found := false
	for _, msg := range result.LLMView {
		if msg.Role == "user" && msg.Content == originalUserContent {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Original user message not found in LLMView after compression.\n"+
			"LLMView has %d messages. User messages found:", len(result.LLMView))
		for _, msg := range result.LLMView {
			if msg.Role == "user" {
				t.Logf("  user: %q", truncateRunes(msg.Content, 80))
			}
		}
	}

	// The original user message MUST also be present in SessionView
	foundInSession := false
	for _, msg := range result.SessionView {
		if msg.Role == "user" && msg.Content == originalUserContent {
			foundInSession = true
			break
		}
	}
	if !foundInSession {
		t.Errorf("Original user message not found in SessionView after compression.\n"+
			"SessionView has %d messages:", len(result.SessionView))
		for _, msg := range result.SessionView {
			if msg.Role == "user" {
				t.Logf("  user: %q", truncateRunes(msg.Content, 80))
			}
		}
	}

	// CRITICAL: tail MUST be capped — LLMView should NOT contain all 400 tool messages.
	// The old (broken) fix adjusted tailStart to keep the entire tail, defeating compression.
	// With maxContextTokens=100000 → maxTailMessages=75, the tail should be around 75 messages,
	// plus system(1) + summary(1) + continuation(1) + injected user msg(1) ≈ ~80.
	// Allow generous margin but NOT 400+.
	if len(result.LLMView) > 200 {
		t.Errorf("LLMView too large after compression (%d messages) — tail capping may not be working.\n"+
			"Expected ~80 messages (system + summary + continuation + user msg + capped tail), got %d",
			len(result.LLMView), len(result.LLMView))
	}
}

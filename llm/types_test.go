package llm

import (
	"testing"
)

func TestSanitizeMessages_EmptyAssistant(t *testing.T) {
	tests := []struct {
		name    string
		input   []ChatMessage
		wantLen int
		wantLog bool // whether a warning should be logged
	}{
		{
			name: "strips empty assistant (content=\"\" and no tool_calls)",
			input: []ChatMessage{
				NewUserMessage("hello"),
				NewAssistantMessage(""),
				NewAssistantMessage("reply"),
			},
			wantLen: 2, // user + reply, empty stripped
			wantLog: true,
		},
		{
			name: "keeps assistant with content",
			input: []ChatMessage{
				NewUserMessage("hello"),
				NewAssistantMessage("reply"),
			},
			wantLen: 2,
		},
		{
			name: "keeps assistant with tool_calls even if content empty",
			input: []ChatMessage{
				NewUserMessage("hello"),
				{
					Role:    "assistant",
					Content: "",
					ToolCalls: []ToolCall{
						{ID: "call_1", Name: "read", Arguments: "{}"},
					},
				},
				NewToolMessage("read", "call_1", "{}", "result"),
			},
			wantLen: 3,
		},
		{
			name: "strips trailing unpaired tool_calls",
			input: []ChatMessage{
				NewUserMessage("hello"),
				{
					Role: "assistant",
					ToolCalls: []ToolCall{
						{ID: "call_1", Name: "read", Arguments: "{}"},
					},
				},
			},
			wantLen: 1, // only user message remains
		},
		{
			name: "strips empty assistant in middle of list",
			input: []ChatMessage{
				NewUserMessage("hello"),
				NewAssistantMessage(""),
				NewUserMessage("next"),
				NewAssistantMessage("reply"),
			},
			wantLen: 3, // hello + next + reply
			wantLog: true,
		},
		{
			name: "strips multiple empty assistants",
			input: []ChatMessage{
				NewAssistantMessage(""),
				NewUserMessage("hello"),
				NewAssistantMessage(""),
				NewAssistantMessage("reply"),
				NewAssistantMessage(""),
			},
			wantLen: 2, // hello + reply
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeMessages(tt.input)
			if len(got) != tt.wantLen {
				t.Errorf("SanitizeMessages() len = %d, want %d; got messages: %+v", len(got), tt.wantLen, got)
			}
		})
	}
}

// Test that the deprecated alias still works
func TestFixupTrailingToolCalls_Alias(t *testing.T) {
	input := []ChatMessage{
		NewUserMessage("hello"),
		NewAssistantMessage(""),
		NewAssistantMessage("reply"),
	}
	got := FixupTrailingToolCalls(input)
	if len(got) != 2 {
		t.Errorf("FixupTrailingToolCalls() (alias) len = %d, want 2", len(got))
	}
}

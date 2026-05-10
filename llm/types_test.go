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
		{
			name: "Pass 5: strips tool messages orphaned by Pass 2 (invalid JSON tool_call removed)",
			input: []ChatMessage{
				NewUserMessage("hello"),
				{
					Role:    "assistant",
					Content: "",
					ToolCalls: []ToolCall{
						{ID: "call_1", Name: "shell", Arguments: `{"command":"ls`}, // invalid JSON (unclosed string)
						{ID: "call_2", Name: "read", Arguments: "{}"},
					},
				},
				NewToolMessage("shell", "call_1", `{"command":"ls`, "partial result"),
				NewToolMessage("read", "call_2", "{}", "file content"),
				NewAssistantMessage("done"),
			},
			wantLen: 4, // user + assistant(call_2 only) + tool(call_2) + assistant("done")
			wantLog: true,
		},
		{
			name: "Pass 5: strips orphaned tool message with no matching tool_call anywhere",
			input: []ChatMessage{
				NewUserMessage("hello"),
				NewAssistantMessage("thinking..."),
				NewToolMessage("grep", "call_orphan", "{}", "result"),
				NewAssistantMessage("done"),
			},
			wantLen: 3, // user + assistant("thinking") + assistant("done")
			wantLog: true,
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

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
			name: "Pass 2: fixes tool_call with invalid JSON arguments instead of stripping",
			input: []ChatMessage{
				NewUserMessage("hello"),
				{
					Role:    "assistant",
					Content: "",
					ToolCalls: []ToolCall{
						{ID: "call_1", Name: "shell", Arguments: `{"command":"ls`}, // invalid JSON (truncated)
						{ID: "call_2", Name: "read", Arguments: "{}"},
					},
				},
				NewToolMessage("shell", "call_1", `{"command":"ls`, "partial result"),
				NewToolMessage("read", "call_2", "{}", "file content"),
				NewAssistantMessage("done"),
			},
			wantLen: 5, // user + assistant(both tool_calls, call_1 args fixed) + tool(call_1) + tool(call_2) + assistant("done")
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

func TestSanitizeMessages_StripsToolMessageBeforeMatchingAssistant(t *testing.T) {
	input := []ChatMessage{
		NewUserMessage("hello"),
		NewToolMessage("read", "call_late", "{}", "orphan result from corrupted history"),
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "call_late", Name: "read", Arguments: "{}"},
			},
		},
		NewAssistantMessage("done"),
	}

	got := SanitizeMessages(input)
	for _, msg := range got {
		if msg.Role == "tool" {
			t.Fatalf("SanitizeMessages() kept orphan tool message before its assistant: %+v", got)
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			t.Fatalf("SanitizeMessages() kept assistant tool_call without following tool response: %+v", got)
		}
	}
}

func TestSanitizeMessages_KeepsTrailingMultipleToolResults(t *testing.T) {
	input := []ChatMessage{
		NewUserMessage("hello"),
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "call_1", Name: "read", Arguments: "{}"},
				{ID: "call_2", Name: "grep", Arguments: "{}"},
			},
		},
		NewToolMessage("read", "call_1", "{}", "file content"),
		NewToolMessage("grep", "call_2", "{}", "matches"),
	}

	got := SanitizeMessages(input)
	if len(got) != len(input) {
		t.Fatalf("SanitizeMessages() len = %d, want %d; got messages: %+v", len(got), len(input), got)
	}
	if got[1].Role != "assistant" || len(got[1].ToolCalls) != 2 {
		t.Fatalf("SanitizeMessages() assistant tool_calls = %+v, want both calls preserved", got[1])
	}
	if got[2].Role != "tool" || got[2].ToolCallID != "call_1" {
		t.Fatalf("SanitizeMessages() first tool result = %+v, want call_1", got[2])
	}
	if got[3].Role != "tool" || got[3].ToolCallID != "call_2" {
		t.Fatalf("SanitizeMessages() second tool result = %+v, want call_2", got[3])
	}
}

func TestSanitizeMessages_CleanHistoryUsesFastPath(t *testing.T) {
	input := []ChatMessage{
		NewUserMessage("hello"),
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "call_1", Name: "read", Arguments: "{}"},
				{ID: "call_2", Name: "grep", Arguments: "{}"},
			},
		},
		NewToolMessage("read", "call_1", "{}", "file content"),
		NewToolMessage("grep", "call_2", "{}", "matches"),
		NewAssistantMessage("done"),
	}

	got := SanitizeMessages(input)
	if len(got) != len(input) {
		t.Fatalf("SanitizeMessages() len = %d, want %d", len(got), len(input))
	}
	if &got[0] != &input[0] {
		t.Fatal("SanitizeMessages() should reuse the original backing array for clean history")
	}
}

func TestSanitizeMessages_EmptyArguments(t *testing.T) {
	// Empty string "" is not valid JSON. SGLang and other strict backends reject it
	// with 400 Bad Request. SanitizeMessages must fix it to "{}".
	input := []ChatMessage{
		NewUserMessage("hello"),
		{
			Role:    "assistant",
			Content: "A background task has completed.",
			ToolCalls: []ToolCall{
				{ID: "bg_abc", Name: "background_task_result", Arguments: ""},
			},
		},
		NewToolMessage("background_task_result", "bg_abc", "", "task output"),
		NewAssistantMessage("done"),
	}

	got := SanitizeMessages(input)
	if len(got) != 4 {
		t.Fatalf("SanitizeMessages() len = %d, want 4; got: %+v", len(got), got)
	}
	assistant := got[1]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 {
		t.Fatalf("expected assistant with 1 tool_call, got: %+v", assistant)
	}
	if assistant.ToolCalls[0].Arguments != "{}" {
		t.Errorf("empty arguments should be fixed to {}, got %q", assistant.ToolCalls[0].Arguments)
	}
}

func TestSanitizeMessages_EmptyArgumentsInRestoredHistory(t *testing.T) {
	// Simulates restored history from DB where multiple synthetic tool calls
	// have empty arguments (the original bug from SGLang 400).
	input := []ChatMessage{
		NewUserMessage("hello"),
		{
			Role:    "assistant",
			Content: "bg task 1 done",
			ToolCalls: []ToolCall{
				{ID: "bg_1", Name: "background_task_result", Arguments: ""},
			},
		},
		NewToolMessage("background_task_result", "bg_1", "", "output1"),
		{
			Role:    "assistant",
			Content: "bg task 2 done",
			ToolCalls: []ToolCall{
				{ID: "bg_2", Name: "background_task_result", Arguments: ""},
			},
		},
		NewToolMessage("background_task_result", "bg_2", "", "output2"),
		NewAssistantMessage("all done"),
	}

	got := SanitizeMessages(input)
	for _, msg := range got {
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.Arguments == "" {
				t.Errorf("tool_call %s still has empty arguments after sanitization", tc.ID)
			}
			if tc.Arguments != "{}" {
				t.Errorf("tool_call %s arguments = %q, want {}", tc.ID, tc.Arguments)
			}
		}
	}
}

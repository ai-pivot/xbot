package protocol

import (
	"encoding/json"
	"testing"

	"xbot/llm"
)

func TestExportImportRoundTrip(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hello", TurnID: 1},
		{
			Role:             "assistant",
			Content:          "Let me check that.",
			ReasoningContent: "I need to read the file first.",
			ToolCalls:        []llm.ToolCall{{ID: "call_1", Name: "Read", Arguments: `{"path":"a.go"}`}},
			TurnID:           1,
		},
		{Role: "tool", Content: "package a", ToolCallID: "call_1", ToolName: "Read", Detail: "diff shown", TurnID: 1},
		{Role: "user", Content: "Thanks", TurnID: 2},
	}

	// DisplayOnly should be filtered out on export.
	msgs = append(msgs, llm.ChatMessage{Role: "user", Content: "bg notification", DisplayOnly: true, TurnID: 3})

	sess, err := ExportSession("chat-1", "gpt-5.1", msgs)
	if err != nil {
		t.Fatal(err)
	}

	if sess.ID != "chat-1" {
		t.Errorf("ID = %q, want chat-1", sess.ID)
	}
	if sess.Model != "gpt-5.1" {
		t.Errorf("Model = %q, want gpt-5.1", sess.Model)
	}
	if sess.SystemInstructions != "You are a helpful assistant." {
		t.Errorf("SystemInstructions = %q", sess.SystemInstructions)
	}
	// 5 messages, 1 system extracted → 4 in Messages, display_only filtered.
	if len(sess.Messages) != 4 {
		t.Fatalf("Messages len = %d, want 4 (display_only filtered)", len(sess.Messages))
	}

	// Verify assistant tool_calls converted to nested form.
	assistant := sess.Messages[1]
	if assistant.Role != "assistant" {
		t.Fatalf("Messages[1].Role = %q", assistant.Role)
	}
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant ToolCalls len = %d", len(assistant.ToolCalls))
	}
	if assistant.ToolCalls[0].Type != "function" || assistant.ToolCalls[0].Function.Name != "Read" {
		t.Errorf("tool call = %+v", assistant.ToolCalls[0])
	}

	// Round-trip: import back.
	imported := ImportSession(sess)
	// system + 4 messages
	if len(imported) != 5 {
		t.Fatalf("imported len = %d, want 5", len(imported))
	}
	if imported[0].Role != "system" || imported[0].Content != "You are a helpful assistant." {
		t.Errorf("imported[0] = %+v", imported[0])
	}
	if imported[2].Role != "assistant" || imported[2].ReasoningContent != "I need to read the file first." {
		t.Errorf("imported[2] reasoning lost: %+v", imported[2])
	}
	if len(imported[2].ToolCalls) != 1 || imported[2].ToolCalls[0].Name != "Read" {
		t.Errorf("imported[2] tool calls lost: %+v", imported[2].ToolCalls)
	}
	if imported[3].Detail != "diff shown" {
		t.Errorf("imported[3] detail lost: %+v", imported[3])
	}
}

func TestExportSessionStripsLastAssistantDetail(t *testing.T) {
	// The last assistant message's Detail (aggregated iteration history JSON)
	// duplicates content already present in the message stream — exports must
	// strip it while keeping tool-message detail intact.
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task", TurnID: 1},
		{Role: "tool", Content: "file content", ToolCallID: "call_1", ToolName: "Read", Detail: "diff shown", TurnID: 1},
		{Role: "assistant", Content: "final reply", Detail: `[{"iteration":1,"content":"thinking","reasoning":"...","tools":[]}]`, TurnID: 1},
	}
	sess, err := ExportSession("chat-1", "m", msgs)
	if err != nil {
		t.Fatal(err)
	}
	// system extracted → 3 messages: user, tool, assistant
	if len(sess.Messages) != 3 {
		t.Fatalf("Messages len = %d, want 3", len(sess.Messages))
	}
	last := sess.Messages[len(sess.Messages)-1]
	if last.Role != "assistant" {
		t.Fatalf("last role = %q, want assistant", last.Role)
	}
	if last.Detail != "" {
		t.Errorf("last assistant detail should be stripped, got %q", last.Detail)
	}
	// tool-message detail must survive (used for UI rendering on import).
	if sess.Messages[1].Role != "tool" || sess.Messages[1].Detail != "diff shown" {
		t.Errorf("tool detail lost: %+v", sess.Messages[1])
	}
}

func TestExportedMessageContentToString(t *testing.T) {
	// String content
	strContent, _ := json.Marshal("hello")
	m := ExportedMessage{Role: "user", Content: strContent}
	if got := m.ContentToString(); got != "hello" {
		t.Errorf("string content = %q", got)
	}

	// Array content (multimodal) — extract text parts, drop images.
	arrContent, _ := json.Marshal([]ExportedContentPart{
		{Type: "image_url", ImageURL: &ExportedImageURL{URL: "data:image/png;base64,xxx"}},
		{Type: "text", Text: "what's this"},
	})
	m2 := ExportedMessage{Role: "user", Content: arrContent}
	if got := m2.ContentToString(); got != "what's this" {
		t.Errorf("array content = %q, want \"what's this\"", got)
	}
}

func TestImportSessionEmpty(t *testing.T) {
	sess := &ExportedSession{ID: "x", Messages: nil}
	msgs := ImportSession(sess)
	if len(msgs) != 0 {
		t.Errorf("empty import len = %d", len(msgs))
	}

	// System instructions alone prepends a system message.
	sess2 := &ExportedSession{ID: "x", SystemInstructions: "sys", Messages: nil}
	if msgs := ImportSession(sess2); len(msgs) != 1 || msgs[0].Role != "system" {
		t.Errorf("system-only import = %+v", msgs)
	}
}

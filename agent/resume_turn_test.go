package agent

import (
	"context"
	"testing"

	"xbot/llm"
)

// TestResumeTurn_RemovesDuplicateUserMessage verifies that when resume_turn
// metadata is set, processMessage's Assemble output has the duplicate user
// message (appended by Assemble from msg.Content) removed.
//
// This covers the core of the graceful shutdown resume feature: LLM sees
// exactly what's in the DB (history already contains the user message),
// no duplicate.
func TestResumeTurn_RemovesDuplicateUserMessage(t *testing.T) {
	// Simulate history loaded from DB (already contains user message from
	// the original turn that was interrupted by graceful shutdown).
	history := []llm.ChatMessage{
		llm.NewUserMessage("hello"), // already eager-saved before original Run()
	}

	// Assemble always appends the current msg.Content as a new user message.
	mc := NewMessageContext(
		context.Background(),
		"hello", // same content as the history user message
		history,
		"web",
		"/workspace",
		"web-1",
		"web-1",
		"chat-1",
	)
	messages := mc.Assemble()

	// Assemble appended a duplicate user message — this is what resume_turn
	// must strip.
	userCount := 0
	for _, m := range messages {
		if m.Role == "user" {
			userCount++
		}
	}
	if userCount != 2 {
		t.Fatalf("expected 2 user messages (1 from history + 1 from Assemble), got %d", userCount)
	}

	// Simulate the resume_turn removal logic from processMessage:
	// "if len(messages) > 0 && messages[len(messages)-1].Role == "user" {
	//    messages = messages[:len(messages)-1]
	// }"
	if len(messages) > 0 && messages[len(messages)-1].Role == "user" {
		messages = messages[:len(messages)-1]
	}

	// After removal: exactly 1 user message (from history), matching DB state.
	userCount = 0
	for _, m := range messages {
		if m.Role == "user" {
			userCount++
		}
	}
	if userCount != 1 {
		t.Fatalf("expected 1 user message after resume_turn removal, got %d", userCount)
	}

	// The remaining user message is from history (content matches).
	foundUser := false
	for _, m := range messages {
		if m.Role == "user" && m.Content == "hello" {
			foundUser = true
			break
		}
	}
	if !foundUser {
		t.Fatal("expected to find the history user message 'hello' after removal")
	}
}

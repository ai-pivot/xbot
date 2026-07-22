package agent

import (
	"context"
	"testing"

	"xbot/llm"
)

// TestResumeTurn_EmptyUserMessageSkipsAssembleAppend verifies that when
// resume_turn injects an empty message, Assemble does NOT append a user
// message — the history from DB already contains it.
//
// This is the core of the graceful shutdown resume feature: LLM sees
// exactly what's in the DB, no duplicate, no workaround.
func TestResumeTurn_EmptyUserMessageSkipsAssembleAppend(t *testing.T) {
	// Simulate history loaded from DB (already contains user message from
	// the original turn that was interrupted by graceful shutdown).
	history := []llm.ChatMessage{
		llm.NewUserMessage("hello"), // already eager-saved before original Run()
	}

	// Resume turn: InjectInboundResume sends empty content.
	// MessageContext receives empty UserMessage.
	mc := NewMessageContext(
		context.Background(),
		"", // empty — resume_turn, no new user message
		history,
		"web",
		"/workspace",
		"web-1",
		"web-1",
		"chat-1",
	)
	messages := mc.Assemble()

	// Assemble should NOT have appended a user message.
	userCount := 0
	for _, m := range messages {
		if m.Role == "user" {
			userCount++
		}
	}
	if userCount != 1 {
		t.Fatalf("expected 1 user message (from history only), got %d", userCount)
	}

	// The user message is from history (content matches).
	foundUser := false
	for _, m := range messages {
		if m.Role == "user" && m.Content == "hello" {
			foundUser = true
			break
		}
	}
	if !foundUser {
		t.Fatal("expected to find the history user message 'hello'")
	}
}

// TestResumeTurn_PipelineSkipsUserMessageSynthesis verifies that the real
// pipeline (with UserMessageMiddleware) does NOT synthesize a bogus user
// message when ResumeTurn is set. This catches the regression where
// UserMessageMiddleware overwrites UserMessage with timestamp+guide text
// even when UserContent is empty.
func TestResumeTurn_PipelineSkipsUserMessageSynthesis(t *testing.T) {
	loader := NewPromptLoader("")
	pipeline := NewMessagePipeline(
		NewSystemPromptMiddleware(loader, "flat"),
		NewUserMessageMiddleware("flat"),
	)

	mc := &MessageContext{
		Ctx:         context.Background(),
		SystemParts: make(map[string]string),
		UserContent: "", // empty — resume turn
		History: []llm.ChatMessage{
			llm.NewUserMessage("hello"), // from DB
		},
		Channel:    "web",
		WorkDir:    "/workspace",
		SenderName: "web-1",
		Extra:      make(map[string]any),
	}
	mc.ResumeTurn = true

	messages := pipeline.Run(mc)

	// Should be: system + history (1 user) = 2 messages. NO appended user.
	userCount := 0
	for _, m := range messages {
		if m.Role == "user" {
			userCount++
		}
	}
	if userCount != 1 {
		t.Fatalf("expected 1 user message (from history), got %d — UserMessageMiddleware may have synthesized a bogus message", userCount)
	}

	// The single user message must be the real DB content, not synthesized.
	userMsg := messages[len(messages)-1]
	if userMsg.Role != "user" || userMsg.Content != "hello" {
		t.Fatalf("expected last message to be history user 'hello', got %s: %q", userMsg.Role, userMsg.Content)
	}
}

// TestNormalTurn_NonEmptyUserMessageAppended verifies that normal (non-resume)
// turns still append the user message as expected.
func TestNormalTurn_NonEmptyUserMessageAppended(t *testing.T) {
	history := []llm.ChatMessage{
		llm.NewAssistantMessage("previous reply"),
	}

	mc := NewMessageContext(
		context.Background(),
		"new question", // normal turn — user message present
		history,
		"web",
		"/workspace",
		"web-1",
		"web-1",
		"chat-1",
	)
	// Simulate the middleware step that copies UserContent → UserMessage.
	mc.UserMessage = mc.UserContent
	messages := mc.Assemble()

	userCount := 0
	for _, m := range messages {
		if m.Role == "user" {
			userCount++
		}
	}
	if userCount != 1 {
		t.Fatalf("expected 1 user message (from Assemble), got %d", userCount)
	}

	// Last message should be the user message.
	last := messages[len(messages)-1]
	if last.Role != "user" || last.Content != "new question" {
		t.Fatalf("expected last message to be user 'new question', got %s: %q", last.Role, last.Content)
	}
}

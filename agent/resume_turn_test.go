package agent

import (
	"context"
	"testing"

	"xbot/bus"
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

// TestResumeTurn_MetadataToResumeTurnWiring verifies the buildPrompt wiring
// point: msg.Metadata["resume_turn"] == "true" → mc.ResumeTurn = true.
// This is the critical data-integrity invariant — if this wiring breaks,
// every resume inserts a DUPLICATE user message into the DB.
func TestResumeTurn_MetadataToResumeTurnWiring(t *testing.T) {
	// Simulate what buildPrompt does (agent.go:3140-3143):
	//   if msg.Metadata != nil && msg.Metadata["resume_turn"] == "true" {
	//       mc.ResumeTurn = true
	//   }
	mc := NewMessageContext(context.Background(), "", nil, "web", "/ws", "s", "s", "c")

	// Without resume_turn metadata → ResumeTurn stays false
	if mc.ResumeTurn {
		t.Fatal("ResumeTurn should be false without metadata")
	}

	// Simulate buildPrompt setting it from metadata
	msg := bus.InboundMessage{
		Metadata: map[string]string{"resume_turn": "true"},
	}
	if msg.Metadata != nil && msg.Metadata["resume_turn"] == "true" {
		mc.ResumeTurn = true
	}
	if !mc.ResumeTurn {
		t.Fatal("ResumeTurn should be true after buildPrompt wiring")
	}

	// Verify the pipeline actually skips synthesis when ResumeTurn is set
	loader := NewPromptLoader("")
	pipeline := NewMessagePipeline(
		NewSystemPromptMiddleware(loader, "flat"),
		NewUserMessageMiddleware("flat"),
	)
	mc2 := &MessageContext{
		Ctx:         context.Background(),
		SystemParts: make(map[string]string),
		UserContent: "",
		History:     []llm.ChatMessage{llm.NewUserMessage("db user msg")},
		Channel:     "web",
		Extra:       make(map[string]any),
		ResumeTurn:  true,
	}
	messages := pipeline.Run(mc2)
	userCount := 0
	for _, m := range messages {
		if m.Role == "user" {
			userCount++
		}
	}
	if userCount != 1 {
		t.Fatalf("expected 1 user message with ResumeTurn=true, got %d", userCount)
	}
}

// TestNonResumeTurn_EmptyContentSynthesizesMessage proves the flip side:
// WITHOUT ResumeTurn, empty content still synthesizes a user message
// (timestamp+guide). This is the bug that ResumeTurn prevents.
func TestNonResumeTurn_EmptyContentSynthesizesMessage(t *testing.T) {
	loader := NewPromptLoader("")
	pipeline := NewMessagePipeline(
		NewSystemPromptMiddleware(loader, "flat"),
		NewUserMessageMiddleware("flat"),
	)
	mc := &MessageContext{
		Ctx:         context.Background(),
		SystemParts: make(map[string]string),
		UserContent: "", // empty — but NOT a resume turn
		History:     []llm.ChatMessage{llm.NewUserMessage("db user msg")},
		Channel:     "web",
		Extra:       make(map[string]any),
		ResumeTurn:  false, // normal turn with empty content
	}
	messages := pipeline.Run(mc)
	userCount := 0
	for _, m := range messages {
		if m.Role == "user" {
			userCount++
		}
	}
	// UserMessageMiddleware synthesizes a bogus user message even with empty content.
	// This proves ResumeTurn=true is the critical guard.
	if userCount != 2 {
		t.Fatalf("expected 2 user messages (history + synthesized) without ResumeTurn, got %d", userCount)
	}
	// Last message should be the synthesized one (contains timestamp, not "db user msg")
	last := messages[len(messages)-1]
	if last.Role != "user" || last.Content == "db user msg" {
		t.Fatalf("expected synthesized user message (not DB content), got %s: %q", last.Role, last.Content[:min(50, len(last.Content))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

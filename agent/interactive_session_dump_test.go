package agent

import (
	"reflect"
	"sync"
	"testing"

	"xbot/llm"
)

// TestAgentSessionMessagesFormatting verifies the behavior of the THREE
// session-viewer entry points (GetAgentSessionDump, GetAgentSessionDumpByFullKey,
// GetSessionMessages): they must format the same interactive session identically —
// system prompt first (when non-empty), user/assistant messages with content,
// and tool-call-only assistant messages summarized as "[Tool calls: ...]".
//
// This is the behavior contract for the shared formatAgentSessionMessages helper
// (extracted from three duplicated loops in interactive.go). Refactor guard:
// the helper must reproduce the exact pre-refactor behavior of all three.
func TestAgentSessionMessagesFormatting(t *testing.T) {
	a := NewTestAgent()
	key := "cli:/w/reviewer:d1"
	agentProgressKey := "agent:" + key

	toolCallMsg := llm.NewAssistantMessage("")
	toolCallMsg.ToolCalls = []llm.ToolCall{
		{ID: "tc1", Name: "Read", Arguments: `{"path":"a.go"}`},
		{ID: "tc2", Name: "Grep", Arguments: `{"pattern":"TODO"}`},
	}

	ia := &interactiveAgent{
		roleName:     "reviewer",
		instance:     "d1",
		background:   false,
		mu:           sync.Mutex{},
		systemPrompt: llm.NewSystemMessage("You are a reviewer."),
		messages: []llm.ChatMessage{
			llm.NewUserMessage("please review"),
			toolCallMsg, // empty content + tool calls → "[Tool calls: Read, Grep]"
			llm.NewToolMessage("Read", "tc1", "{}", "file a.go read"),
			llm.NewAssistantMessage("review done"),
		},
		iterationHistory: []IterationSnapshot{
			{Iteration: 1, Content: "thinking"},
		},
	}
	a.interactiveSubAgents.Store(key, ia)
	a.lastProgressSnapshot.Delete(agentProgressKey)

	want := []SessionMessage{
		{Role: "system", Content: "You are a reviewer."},
		{Role: "user", Content: "please review"},
		{Role: "assistant", Content: "[Tool calls: Read, Grep]"},
		{Role: "tool", Content: "file a.go read"},
		{Role: "assistant", Content: "review done"},
	}

	// GetSessionMessages
	gotMsgs, ok := a.GetSessionMessages("cli", "/w", "reviewer", "d1")
	if !ok {
		t.Fatal("GetSessionMessages returned ok=false")
	}
	if !reflect.DeepEqual(gotMsgs, want) {
		t.Fatalf("GetSessionMessages mismatch:\n got: %#v\nwant: %#v", gotMsgs, want)
	}

	// GetAgentSessionDump
	dump, ok := a.GetAgentSessionDump("cli", "/w", "reviewer", "d1")
	if !ok {
		t.Fatal("GetAgentSessionDump returned ok=false")
	}
	if !reflect.DeepEqual(dump.Messages, want) {
		t.Fatalf("GetAgentSessionDump.Messages mismatch:\n got: %#v\nwant: %#v", dump.Messages, want)
	}
	if len(dump.IterationHistory) != 1 || dump.IterationHistory[0].Iteration != 1 {
		t.Fatalf("GetAgentSessionDump.IterationHistory mismatch: %#v", dump.IterationHistory)
	}

	// GetAgentSessionDumpByFullKey (same session via the full key)
	dump2, ok := a.GetAgentSessionDumpByFullKey(key)
	if !ok {
		t.Fatal("GetAgentSessionDumpByFullKey returned ok=false")
	}
	if !reflect.DeepEqual(dump2.Messages, want) {
		t.Fatalf("GetAgentSessionDumpByFullKey.Messages mismatch:\n got: %#v\nwant: %#v", dump2.Messages, want)
	}
	if !reflect.DeepEqual(dump2.IterationHistory, dump.IterationHistory) {
		t.Fatalf("ByFullKey/Dump iteration history mismatch: %#v vs %#v", dump2.IterationHistory, dump.IterationHistory)
	}

	// Empty system prompt → no leading system message (boundary of the shared loop).
	emptyPromptIA := &interactiveAgent{
		roleName: "reviewer", instance: "d2", mu: sync.Mutex{},
		systemPrompt: llm.ChatMessage{Role: "system"}, // empty content
		messages:     []llm.ChatMessage{llm.NewUserMessage("hi")},
	}
	a.interactiveSubAgents.Store("cli:/w/reviewer:d2", emptyPromptIA)
	got, ok := a.GetSessionMessages("cli", "/w", "reviewer", "d2")
	if !ok {
		t.Fatal("GetSessionMessages(d2) returned ok=false")
	}
	wantNoSystem := []SessionMessage{{Role: "user", Content: "hi"}}
	if !reflect.DeepEqual(got, wantNoSystem) {
		t.Fatalf("empty system prompt must be skipped:\n got: %#v\nwant: %#v", got, wantNoSystem)
	}
}

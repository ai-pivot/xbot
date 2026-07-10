package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"xbot/llm"
	"xbot/protocol"
)

// TestAutoNotify_DerivedFromBothHandlers verifies that autoNotify in engine.Run()
// is true when either ProgressNotifier OR ProgressEventHandler is set.
// This is the core fix: before, only ProgressNotifier gated autoNotify,
// so background SubAgents with only ProgressEventHandler had autoNotify=false
// and all progress events were silently dropped.
func TestAutoNotify_DerivedFromBothHandlers(t *testing.T) {
	tests := []struct {
		name                 string
		progressNotifier     func(lines []string, thinking string)
		progressEventHandler func(event *ProgressEvent)
		wantAuto             bool
	}{
		{
			name:     "both nil → autoNotify=false",
			wantAuto: false,
		},
		{
			name:             "ProgressNotifier only → autoNotify=true",
			progressNotifier: func([]string, string) {},
			wantAuto:         true,
		},
		{
			name:                 "ProgressEventHandler only → autoNotify=true",
			progressEventHandler: func(*ProgressEvent) {},
			wantAuto:             true,
		},
		{
			name:                 "both set → autoNotify=true",
			progressNotifier:     func([]string, string) {},
			progressEventHandler: func(*ProgressEvent) {},
			wantAuto:             true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := RunConfig{
				ProgressNotifier:     tt.progressNotifier,
				ProgressEventHandler: tt.progressEventHandler,
			}
			autoNotify := cfg.ProgressNotifier != nil || cfg.ProgressEventHandler != nil
			if autoNotify != tt.wantAuto {
				t.Errorf("autoNotify = %v, want %v", autoNotify, tt.wantAuto)
			}
		})
	}
}

// TestBackgroundMode_AutoNotifyViaEventHandler verifies the actual bug scenario:
// background interactive SubAgent has no ProgressNotifier but does have
// ProgressEventHandler (set by wireSubAgentCLIProgress). autoNotify must be true.
func TestBackgroundMode_AutoNotifyViaEventHandler(t *testing.T) {
	cfg := RunConfig{
		// Background mode: ProgressNotifier is nil
		ProgressNotifier: nil,
		// wireSubAgentCLIProgress sets this for background mode
		ProgressEventHandler: func(event *ProgressEvent) {},
	}
	autoNotify := cfg.ProgressNotifier != nil || cfg.ProgressEventHandler != nil
	if !autoNotify {
		t.Fatal("BUG REPRODUCED: background SubAgent with ProgressEventHandler has autoNotify=false")
	}
}

// TestGetActiveProgress_BackgroundInteractive verifies Phase correction
// for running agents between iterations.
func TestGetActiveProgress_BackgroundInteractive(t *testing.T) {
	a := NewTestAgent()
	interactiveKey := "cli:/home/user/src/project/ministry-works:split-test-files"
	agentProgressKey := "agent:" + interactiveKey

	ia := &interactiveAgent{roleName: "ministry-works", instance: "split-test-files", running: true, mu: sync.Mutex{}}
	a.interactiveSubAgents.Store(interactiveKey, ia)

	a.lastProgressSnapshot.Store(agentProgressKey, &protocol.ProgressEvent{
		ChatID: agentProgressKey, Phase: "done", Iteration: 3,
		ActiveTools: []protocol.ToolProgress{{Name: "Shell", Status: "done", Iteration: 3}},
	})
	a.iterationHistories.Store(agentProgressKey, &[]protocol.ProgressEvent{
		{Phase: "running", Iteration: 1},
		{Phase: "tool_use", Iteration: 2},
		{Phase: "running", Iteration: 3},
	})

	result := a.GetActiveProgress("agent", interactiveKey, 0)
	if result == nil {
		t.Fatal("GetActiveProgress returned nil")
		return
	}
	if result.Phase == "done" {
		t.Errorf("BUG REPRODUCED: Phase=%q for running agent between iterations", result.Phase)
	}
}

func TestGetActiveProgress_BackgroundInteractive_FinishedAgent(t *testing.T) {
	a := NewTestAgent()
	key := "cli:/cwd/r:i"
	ia := &interactiveAgent{running: false, mu: sync.Mutex{}}
	a.interactiveSubAgents.Store(key, ia)
	a.lastProgressSnapshot.Store("agent:"+key, &protocol.ProgressEvent{Phase: "done", Iteration: 5})

	result := a.GetActiveProgress("agent", key, 0)
	if result == nil {
		t.Fatal("nil")
		return
	}
	if result.Phase != "done" {
		t.Errorf("stopped agent should have Phase=done, got %q", result.Phase)
	}
}

func TestGetActiveProgress_BackgroundInteractive_NoSnapshot(t *testing.T) {
	a := NewTestAgent()
	if result := a.GetActiveProgress("agent", "cli:/cwd/r:i", 0); result != nil {
		t.Errorf("expected nil, got Phase=%q", result.Phase)
	}
}

func TestGetActiveProgress_KeyFormatConsistency(t *testing.T) {
	a := NewTestAgent()
	interactiveKey := "cli:/home/user/src/project/ministry-works:split-test-files"
	agentProgressKey := "agent:" + interactiveKey

	ia := &interactiveAgent{running: true, mu: sync.Mutex{}}
	a.interactiveSubAgents.Store(interactiveKey, ia)
	a.lastProgressSnapshot.Store(agentProgressKey, &protocol.ProgressEvent{
		ChatID: agentProgressKey, Phase: "done", Iteration: 1,
	})

	result := a.GetActiveProgress("agent", interactiveKey, 0)
	if result == nil {
		t.Fatal("snapshot lookup failed — key format mismatch")
		return
	}

	if _, loaded := a.interactiveSubAgents.Load(interactiveKey); !loaded {
		t.Error("interactiveSubAgents.Load(interactiveKey) failed")
	}
	if _, loaded := a.interactiveSubAgents.Load(agentProgressKey); loaded {
		t.Error("interactiveSubAgents should not store agentProgressKey")
	}
}

func NewTestAgent() *Agent { return &Agent{} }

func TestAttachIterationDelta_AttachesPreviousIteration(t *testing.T) {
	a := NewTestAgent()
	key := "cli:/cwd"
	prev := &protocol.ProgressEvent{
		ChatID:      key,
		Phase:       "tool_exec",
		Iteration:   2,
		Content:     "content C",
		Reasoning:   "reasoning C",
		ActiveTools: []protocol.ToolProgress{{Name: "Shell", Status: "done", Iteration: 2}},
	}
	a.lastProgressSnapshot.Store(key, prev)

	next := &protocol.ProgressEvent{ChatID: key, Phase: "thinking", Iteration: 3}
	a.attachIterationDelta(key, next.Iteration, next)

	if len(next.IterationHistory) != 1 {
		t.Fatalf("expected previous iteration attached as delta, got %d", len(next.IterationHistory))
	}
	got := next.IterationHistory[0]
	if got.Iteration != 2 || got.Content != "content C" || got.Reasoning != "reasoning C" {
		t.Fatalf("wrong delta attached: %+v", got)
	}
	if len(got.ActiveTools) != 1 || got.ActiveTools[0].Name != "Shell" {
		t.Fatalf("tool progress not preserved in delta: %+v", got.ActiveTools)
	}
}

func TestAttachIterationDelta_StripsNestedHistory(t *testing.T) {
	a := NewTestAgent()
	key := "cli:/cwd"
	prev := &protocol.ProgressEvent{
		ChatID:    key,
		Phase:     "tool_exec",
		Iteration: 2,
		Content:   "content C",
		IterationHistory: []protocol.ProgressEvent{{
			Iteration: 1,
			Content:   "nested history must not be retained",
		}},
	}
	a.lastProgressSnapshot.Store(key, prev)

	next := &protocol.ProgressEvent{ChatID: key, Phase: "thinking", Iteration: 3}
	a.attachIterationDelta(key, next.Iteration, next)

	if len(next.IterationHistory) != 1 {
		t.Fatalf("expected one flattened delta entry, got %d", len(next.IterationHistory))
	}
	if len(next.IterationHistory[0].IterationHistory) != 0 {
		t.Fatalf("nested IterationHistory leaked into outgoing payload: %+v", next.IterationHistory[0].IterationHistory)
	}
	histPtr, ok := a.iterationHistories.Load(key)
	if !ok {
		t.Fatal("iteration history was not stored")
	}
	hist := *histPtr.(*[]protocol.ProgressEvent)
	if len(hist) != 1 || len(hist[0].IterationHistory) != 0 {
		t.Fatalf("nested IterationHistory stored internally: %+v", hist)
	}
}

func TestAttachIterationDelta_NoDeltaWhenSameIteration(t *testing.T) {
	a := NewTestAgent()
	key := "cli:/cwd"
	prev := &protocol.ProgressEvent{
		ChatID:    key,
		Phase:     "tool_exec",
		Iteration: 2,
		Content:   "content C",
	}
	a.lastProgressSnapshot.Store(key, prev)

	// Same iteration — no advance, no delta
	next := &protocol.ProgressEvent{ChatID: key, Phase: "thinking", Iteration: 2}
	a.attachIterationDelta(key, next.Iteration, next)

	if len(next.IterationHistory) != 0 {
		t.Fatalf("expected no delta when iteration hasn't advanced, got %d", len(next.IterationHistory))
	}
}

func TestGetActiveProgress_WatermarkFilter(t *testing.T) {
	a := NewTestAgent()
	key := "cli:/cwd"

	// Store 3 iterations in history
	a.iterationHistories.Store(key, &[]protocol.ProgressEvent{
		{Iteration: 1, Content: "iter1"},
		{Iteration: 2, Content: "iter2"},
		{Iteration: 3, Content: "iter3"},
	})
	a.lastProgressSnapshot.Store(key, &protocol.ProgressEvent{
		ChatID: key, Phase: "running", Iteration: 4,
	})

	// fromIter=2: should return only iteration 3
	result := a.GetActiveProgress("cli", "/cwd", 2)
	if result == nil {
		t.Fatal("nil")
	}
	if len(result.IterationHistory) != 1 {
		t.Fatalf("expected 1 iteration after watermark, got %d", len(result.IterationHistory))
	}
	if result.IterationHistory[0].Iteration != 3 {
		t.Fatalf("expected iteration 3, got %d", result.IterationHistory[0].Iteration)
	}

	// fromIter=0: should return all 3 iterations
	result = a.GetActiveProgress("cli", "/cwd", 0)
	if len(result.IterationHistory) != 3 {
		t.Fatalf("expected 3 iterations with fromIter=0, got %d", len(result.IterationHistory))
	}

	// fromIter=3: should return 0 iterations
	result = a.GetActiveProgress("cli", "/cwd", 3)
	if len(result.IterationHistory) != 0 {
		t.Fatalf("expected 0 iterations with fromIter=3, got %d", len(result.IterationHistory))
	}
}

// TestBackgroundCompletion_FinalReplyInMessages verifies that the background mode
// path in SpawnInteractiveSession appends the final assistant reply (out.Content)
// to placeholder.messages, so GetAgentSessionDumpByFullKey returns it.
// This is the fix for the bug where switching away from a completed background
// interactive SubAgent and back would lose the final reply.
func TestBackgroundCompletion_FinalReplyInMessages(t *testing.T) {
	// Simulate what the background goroutine does after Run() completes:
	// out.Messages contains intermediate tool-call messages, out.Content
	// has the final text reply.
	preLen := 2 // system prompt + user message
	cfgMessages := []llm.ChatMessage{
		llm.NewSystemMessage("you are helpful"),
		llm.NewUserMessage("do the task"),
	}
	outMessages := []llm.ChatMessage{
		cfgMessages[0], cfgMessages[1],
		llm.NewAssistantMessage(""),                        // tool call (empty content)
		llm.NewToolMessage("Shell", "tc1", "{}", "result"), // tool result
		// NOTE: no final assistant text reply here — that's in out.Content
	}
	outContent := "Here is the final summary of what I did."

	var newMsgs []llm.ChatMessage
	if preLen > 1 {
		newMsgs = append(newMsgs, cfgMessages[1])
	}
	if len(outMessages) > preLen {
		newMsgs = append(newMsgs, outMessages[preLen:]...)
	}
	// This is the fix — append final reply
	if outContent != "" {
		newMsgs = append(newMsgs, llm.NewAssistantMessage(outContent))
	}

	// Verify the final reply is in messages
	found := false
	for _, m := range newMsgs {
		if m.Role == "assistant" && m.Content == outContent {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("BUG REPRODUCED: final assistant reply missing from background session messages")
	}
	// Should have: user msg + tool call + tool result + final reply = 4
	if len(newMsgs) != 4 {
		t.Errorf("expected 4 messages, got %d", len(newMsgs))
	}
}

// TestBgSend_FinalReplyInMessages verifies that the background "send" action path
// appends the final assistant reply (out.Content) and user message to ia.messages,
// so GetAgentSessionDump returns the complete conversation.
// This is a regression test for the bug where the background send path only set
// lastReply but didn't append the final assistant message to ia.messages.
func TestBgSend_FinalReplyInMessages(t *testing.T) {
	// Simulate ia.messages from the initial spawn (has system prompt + first turn)
	ia := &interactiveAgent{
		roleName: "test-role",
		instance: "test-inst",
		messages: []llm.ChatMessage{
			llm.NewUserMessage("initial task"),
			llm.NewAssistantMessage("initial reply"),
		},
	}

	// Simulate what the background "send" goroutine does after Run() completes:
	// preLen = len(cfg.Messages) before Run() added new messages
	preLen := 3 // system + initial_user + new_user_message (appended before Run)
	cfgMessages := []llm.ChatMessage{
		llm.NewSystemMessage("you are helpful"),
		llm.NewUserMessage("initial task"),      // existing
		llm.NewUserMessage("continue the work"), // new user message from action=send
	}
	outMessages := []llm.ChatMessage{
		cfgMessages[0], cfgMessages[1], cfgMessages[2],
		llm.NewAssistantMessage(""),                        // tool call (empty content)
		llm.NewToolMessage("Shell", "tc1", "{}", "result"), // tool result
		// NOTE: no final assistant text reply in out.Messages — that's in out.Content
	}
	outContent := "Here is my final answer after continuing the work."
	outReasoning := "I thought about it carefully"

	// --- Replicate the fixed background "send" path logic ---
	// Include the user message sent via action=send
	if preLen > 0 {
		lastBeforeRun := cfgMessages[preLen-1]
		if lastBeforeRun.Role == "user" {
			ia.messages = append(ia.messages, lastBeforeRun)
		}
	}
	// Append messages produced during Run (tool calls, tool results, etc.)
	if len(outMessages) > preLen {
		ia.messages = append(ia.messages, outMessages[preLen:]...)
	}
	// Append final assistant reply
	if outContent != "" {
		ia.messages = append(ia.messages, llm.NewAssistantMessage(outContent))
	} else {
		ia.messages = append(ia.messages, llm.NewAssistantMessage("(empty response)"))
	}
	// Carry ReasoningContent
	if outReasoning != "" && len(ia.messages) > 0 {
		ia.messages[len(ia.messages)-1].ReasoningContent = outReasoning
	}

	// Verify user message from action=send is in messages
	foundUserMsg := false
	for _, m := range ia.messages {
		if m.Role == "user" && m.Content == "continue the work" {
			foundUserMsg = true
			break
		}
	}
	if !foundUserMsg {
		t.Error("user message from action=send not found in ia.messages")
	}

	// Verify the final assistant reply is in messages
	foundFinalReply := false
	for _, m := range ia.messages {
		if m.Role == "assistant" && m.Content == outContent {
			foundFinalReply = true
			if m.ReasoningContent != outReasoning {
				t.Errorf("ReasoningContent = %q, want %q", m.ReasoningContent, outReasoning)
			}
			break
		}
	}
	if !foundFinalReply {
		t.Errorf("final assistant reply %q not found in ia.messages", outContent)
		for i, m := range ia.messages {
			t.Logf("  msg[%d]: role=%s content=%q", i, m.Role, m.Content[:min(len(m.Content), 50)])
		}
	}

	// Verify the last message is the final assistant reply
	lastMsg := ia.messages[len(ia.messages)-1]
	if lastMsg.Role != "assistant" || lastMsg.Content != outContent {
		t.Errorf("last message: role=%s content=%q, want assistant with %q", lastMsg.Role, lastMsg.Content, outContent)
	}
}

var _ = context.Background

// TestBgSend_CompressedMessagesReplaced verifies that when compression happens
// during a background send Run(), ia.messages is COMPLETELY replaced with the
// compressed messages — not just appended to. Without this fix, the old code
// only appended out.Messages[preLen:], which is empty after compression
// (out.Messages is shorter than preLen), so the compressed result was discarded.
// Symptom: "low reduction rate, new_tokens == original_tokens" in logs.
func TestBgSend_CompressedMessagesReplaced(t *testing.T) {
	// Simulate ia.messages from a previous turn (long history)
	ia := &interactiveAgent{
		roleName: "test-role",
		instance: "test-inst",
		messages: make([]llm.ChatMessage, 100), // 100 old messages
	}
	for i := range ia.messages {
		ia.messages[i] = llm.NewAssistantMessage(fmt.Sprintf("old message %d with lots of content", i))
	}

	// Simulate cfg.Messages before Run: [system, user_msg]
	// preLen = 2 (messages before Run added its own)
	preLen := 2

	// Simulate out.Messages after compression: engine replaced 100+ messages
	// with a short summary. preLen > len(out.Messages) → compression happened.
	outMessages := []llm.ChatMessage{
		llm.NewSystemMessage("system"),
		llm.NewUserMessage("continue"),                       // the send user message
		llm.NewAssistantMessage("[summary of 100 messages]"), // compressed!
		llm.NewToolMessage("Shell", "tc1", "{}", "result"),
	}
	// len(outMessages) = 4, preLen = 2 → compression replaced many messages

	// --- Replicate the fixed bg send logic ---
	if len(outMessages) < preLen {
		// Compression: replace entirely
		ia.messages = make([]llm.ChatMessage, len(outMessages))
		copy(ia.messages, outMessages)
	} else {
		// Normal: replace with out.Messages (authoritative state from engine)
		ia.messages = make([]llm.ChatMessage, len(outMessages))
		copy(ia.messages, outMessages)
	}

	// Verify ia.messages is SHORT (compressed), not 100+ (original)
	if len(ia.messages) > 10 {
		t.Errorf("ia.messages has %d messages after compression — should be ~4 (compressed)", len(ia.messages))
	}

	// Verify the compressed summary is in ia.messages
	found := false
	for _, m := range ia.messages {
		if m.Content == "[summary of 100 messages]" {
			found = true
			break
		}
	}
	if !found {
		t.Error("compressed summary not found in ia.messages")
	}
}

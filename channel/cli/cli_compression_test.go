package cli

import (
	"testing"
	"time"
	"xbot/channel"
	"xbot/protocol"
)

// TestHistoryCompactedClearsPendingUserMsg verifies that the HistoryCompacted
// handler clears pendingUserMsg. Without this, the reload from DB adds the
// user message (with system guide text), and handleHistoryReload's content
// comparison fails (raw "继续" vs DB version with prepended text), causing
// pendingUserMsg to be appended again → duplicate user message.
func TestHistoryCompactedClearsPendingUserMsg(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.agentTurnID = 5

	// Simulate user message added by sendMessage
	userMsg := cliMessage{
		role:      "user",
		content:   "继续",
		timestamp: time.Now(),
		dirty:     true,
	}
	model.messages = append(model.messages, userMsg)
	model.pendingUserMsg = &userMsg

	// Send HistoryCompacted progress event
	sendProgress(model, &protocol.ProgressEvent{
		Phase:            "thinking",
		Iteration:        5,
		HistoryCompacted: true,
	})

	// pendingUserMsg MUST be cleared — the reload will fetch the
	// authoritative version from DB.
	if model.pendingUserMsg != nil {
		t.Fatal("pendingUserMsg should be nil after HistoryCompacted — " +
			"keeping it causes duplicate user messages when reload completes")
	}
}

// TestHistoryCompactedCreatesStreamingMessage verifies that the HistoryCompacted
// handler creates a streaming message immediately. Without this, streamingMsgIdx
// stays -1 and progress events have nowhere to render — the TUI freezes
// (shows busy status but no live content updates).
func TestHistoryCompactedCreatesStreamingMessage(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.agentTurnID = 5

	// Add some messages
	model.messages = append(model.messages, cliMessage{
		role: "user", content: "hello", timestamp: time.Now(),
	})
	model.messages = append(model.messages, cliMessage{
		role: "assistant", content: "hi there", timestamp: time.Now(),
	})

	// Send HistoryCompacted progress event
	sendProgress(model, &protocol.ProgressEvent{
		Phase:            "thinking",
		Iteration:        5,
		HistoryCompacted: true,
	})

	// Streaming message MUST be created immediately
	if model.streamingMsgIdx < 0 {
		t.Fatal("streamingMsgIdx should be >= 0 after HistoryCompacted — " +
			"without a streaming message, progress events can't render live content")
	}
	if model.streamingMsgIdx >= len(model.messages) {
		t.Fatalf("streamingMsgIdx %d out of range (messages: %d)",
			model.streamingMsgIdx, len(model.messages))
	}
	streamingMsg := model.messages[model.streamingMsgIdx]
	if streamingMsg.role != "assistant" || !streamingMsg.isPartial {
		t.Fatalf("streaming message should be assistant/isPartial, got role=%s isPartial=%v",
			streamingMsg.role, streamingMsg.isPartial)
	}
}

// TestHistoryCompactedPreservesStreamingAfterReload verifies that after
// HistoryCompacted creates a streaming message, a subsequent forceFullRebuild
// reload preserves it. This ensures the streaming message survives the
// compacted history being loaded from DB.
func TestHistoryCompactedPreservesStreamingAfterReload(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.agentTurnID = 5

	// Send HistoryCompacted — creates streaming message
	sendProgress(model, &protocol.ProgressEvent{
		Phase:            "thinking",
		Iteration:        5,
		HistoryCompacted: true,
	})

	if model.streamingMsgIdx < 0 {
		t.Fatal("streaming message should exist after HistoryCompacted")
	}
	originalTurnID := model.messages[model.streamingMsgIdx].turnID

	// Simulate progress event arriving after compression (new iteration)
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 6,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Read", Status: "running"},
		},
	})

	// Now simulate the reload completing with compacted history
	model.handleHistoryReload(cliHistoryReloadMsg{
		channelName:      model.channelName,
		chatID:           model.chatID,
		forceFullRebuild: true,
		history: []channel.HistoryMessage{
			{Role: "assistant", Content: "compacted context summary", Timestamp: time.Now()},
		},
	})

	// Streaming message MUST still exist after reload
	if model.streamingMsgIdx < 0 {
		t.Fatal("streamingMsgIdx should still be >= 0 after forceFullRebuild reload — " +
			"the streaming message must be preserved so progress events continue to render")
	}
	if model.streamingMsgIdx >= len(model.messages) {
		t.Fatalf("streamingMsgIdx %d out of range after reload (messages: %d)",
			model.streamingMsgIdx, len(model.messages))
	}
	// Verify it's the same streaming message (same turnID)
	preservedMsg := model.messages[model.streamingMsgIdx]
	if preservedMsg.turnID != originalTurnID {
		t.Fatalf("streaming message turnID changed: expected %d, got %d",
			originalTurnID, preservedMsg.turnID)
	}
}

// TestPostCompressionProgressUpdatesViewport verifies that after compression,
// subsequent progress events actually update the viewport content. This is
// the core regression test for the "TUI freezes after compression" bug.
func TestPostCompressionProgressUpdatesViewport(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.agentTurnID = 5

	// Compression event
	sendProgress(model, &protocol.ProgressEvent{
		Phase:            "thinking",
		Iteration:        5,
		HistoryCompacted: true,
	})

	// Tick to render
	model.handleTickMsg()

	// Post-compression progress event with tool call
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 6,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Status: "running", Label: "ls -la"},
		},
	})

	model.handleTickMsg()

	// The progress state should reflect the new iteration
	if model.progressState.current == nil {
		t.Fatal("progressState.current should not be nil after progress event")
	}
	if model.progressState.current.Iteration != 6 {
		t.Fatalf("expected iteration 6, got %d", model.progressState.current.Iteration)
	}

	// Streaming message must still be valid for rendering
	if model.streamingMsgIdx < 0 {
		t.Fatal("streamingMsgIdx should be valid — TUI would freeze without it")
	}

	// Verify the viewport actually has content (not empty/frozen)
	vpContent := model.viewport.View()
	if len(vpContent) == 0 {
		t.Fatal("viewport should have content after progress event — TUI is frozen")
	}
}

// TestHistoryCompactedNoDuplicateUserAfterReload verifies the end-to-end
// scenario: compression clears pendingUserMsg, reload loads DB history with
// the user message (with system guide), and no duplicate appears.
func TestHistoryCompactedNoDuplicateUserAfterReload(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.agentTurnID = 5

	// Simulate user message from sendMessage
	userMsg := cliMessage{
		role:      "user",
		content:   "继续",
		timestamp: time.Now(),
		dirty:     true,
	}
	model.messages = append(model.messages, userMsg)
	model.pendingUserMsg = &userMsg

	// HistoryCompacted — should clear pendingUserMsg
	sendProgress(model, &protocol.ProgressEvent{
		Phase:            "thinking",
		Iteration:        5,
		HistoryCompacted: true,
	})

	if model.pendingUserMsg != nil {
		t.Fatal("pendingUserMsg should be cleared by HistoryCompacted")
	}

	// Reload completes with DB history (user message has system guide text)
	model.handleHistoryReload(cliHistoryReloadMsg{
		channelName:      model.channelName,
		chatID:           model.chatID,
		forceFullRebuild: true,
		history: []channel.HistoryMessage{
			{Role: "user", Content: "[2026-06-22 16:44:09 CST] [CLI User]\n继续\n\n[System Guide]\n...", Timestamp: time.Now()},
			{Role: "assistant", Content: "compacted summary", Timestamp: time.Now()},
		},
	})

	// Count user messages — should be exactly 1 (from DB)
	userCount := 0
	for _, msg := range model.messages {
		if msg.role == "user" {
			userCount++
		}
	}
	if userCount != 1 {
		t.Fatalf("expected exactly 1 user message after reload, got %d — "+
			"pendingUserMsg was not cleared, causing duplicate", userCount)
	}
}

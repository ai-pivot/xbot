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

// TestHistoryCompactedDoesNotCreateStreamingMessage verifies that the
// HistoryCompacted handler does NOT create a streaming message. The
// streaming target is restored from DB history by handleHistoryReload.
// Creating a streaming message here would produce duplicate assistants
// (one from here, one from DB) — the root cause of the double-assistant bug.
func TestHistoryCompactedDoesNotCreateStreamingMessage(t *testing.T) {
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

	// streamingMsgIdx MUST be -1 — no streaming message during compReloading.
	// handleHistoryReload will restore it from DB history.
	if model.streamingMsgIdx >= 0 {
		t.Fatal("streamingMsgIdx should be -1 after HistoryCompacted — " +
			"creating a streaming message here causes duplicate assistants")
	}
	// compReloading MUST be true — blocks auto-start during async reload.
	if !model.splashState.compReloading {
		t.Fatal("compReloading should be true after HistoryCompacted")
	}
}

// TestHistoryCompactedRestoresStreamingFromDBAfterReload verifies that after
// HistoryCompacted (which does NOT create a streaming message), the subsequent
// handleHistoryReload finds the DB assistant and marks it as the streaming
// target. This guarantees exactly ONE assistant — no dedup needed.
func TestHistoryCompactedRestoresStreamingFromDBAfterReload(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.agentTurnID = 5

	// Send HistoryCompacted — clears messages, sets compReloading
	sendProgress(model, &protocol.ProgressEvent{
		Phase:            "thinking",
		Iteration:        5,
		HistoryCompacted: true,
	})

	// No streaming message during compReloading
	if model.streamingMsgIdx >= 0 {
		t.Fatal("streamingMsgIdx should be -1 during compReloading")
	}

	// Reload completes with DB history containing an assistant
	model.handleHistoryReload(cliHistoryReloadMsg{
		channelName:      model.channelName,
		chatID:           model.chatID,
		forceFullRebuild: true,
		history: []channel.HistoryMessage{
			{Role: "user", Content: "hello", Timestamp: time.Now()},
			{Role: "assistant", Content: "compacted context summary", Timestamp: time.Now()},
		},
	})

	// compReloading must be cleared
	if model.splashState.compReloading {
		t.Fatal("compReloading should be cleared after reload")
	}
	// Streaming target must be the DB assistant (not a newly created message)
	if model.streamingMsgIdx < 0 {
		t.Fatal("streamingMsgIdx should be >= 0 after reload — DB assistant should be streaming target")
	}
	if model.streamingMsgIdx >= len(model.messages) {
		t.Fatalf("streamingMsgIdx %d out of range (messages: %d)", model.streamingMsgIdx, len(model.messages))
	}
	streaming := model.messages[model.streamingMsgIdx]
	if streaming.role != "assistant" || !streaming.isPartial {
		t.Fatalf("DB assistant should be marked as streaming target: role=%s isPartial=%v",
			streaming.role, streaming.isPartial)
	}
	if streaming.content != "compacted context summary" {
		t.Fatalf("streaming target should be DB assistant with DB content, got %q", streaming.content)
	}

	// Exactly ONE assistant — by design, not by dedup
	assistantCount := 0
	for _, msg := range model.messages {
		if msg.role == "assistant" {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Fatalf("expected exactly 1 assistant (by design), got %d", assistantCount)
	}
}

// TestPostCompressionProgressUpdatesViewport verifies that after compression
// and reload, subsequent progress events render correctly. The streaming
// target is the DB assistant (restored by handleHistoryReload), not a
// separately created streaming message.
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

	// During compReloading, no streaming target
	if model.streamingMsgIdx >= 0 {
		t.Fatal("streamingMsgIdx should be -1 during compReloading")
	}

	// Reload completes with DB history
	model.handleHistoryReload(cliHistoryReloadMsg{
		channelName:      model.channelName,
		chatID:           model.chatID,
		forceFullRebuild: true,
		history: []channel.HistoryMessage{
			{Role: "user", Content: "hello", Timestamp: time.Now()},
			{Role: "assistant", Content: "partial response", Timestamp: time.Now()},
		},
	})

	// After reload, streaming target is the DB assistant
	if model.streamingMsgIdx < 0 {
		t.Fatal("streamingMsgIdx should be valid after reload")
	}

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

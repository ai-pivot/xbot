package cli

import (
	"testing"

	"xbot/channel"
	"xbot/protocol"
)

// TestTurnStarted_NotificationDisplaysAndAdoptsTurnID verifies that a
// turn_started event with trigger="notification" displays the notification
// user message (with isNotification badge) and adopts the backend TurnID.
func TestTurnStarted_NotificationDisplaysAndAdoptsTurnID(t *testing.T) {
	model := initTestModel()

	// Session is idle — a bg notification triggers turn_started
	sendProgress(model, &protocol.ProgressEvent{
		Phase:  "turn_started",
		TurnID: 5,
		TurnStart: &protocol.TurnStartInfo{
			Trigger: "notification",
			Content: "⏰ [定时任务触发] test notification",
		},
	})

	if model.agentTurnID != 5 {
		t.Fatalf("agentTurnID = %d, want 5", model.agentTurnID)
	}
	if !model.typing {
		t.Fatal("typing should be true after turn_started")
	}
	if !model.turnStartedProcessed {
		t.Fatal("turnStartedProcessed should be true")
	}

	// Notification user message should be displayed with isNotification=true
	var notifMsg *cliMessage
	for i := range model.messages {
		if model.messages[i].role == "user" && model.messages[i].isNotification {
			notifMsg = &model.messages[i]
			break
		}
	}
	if notifMsg == nil {
		t.Fatal("notification user message not displayed")
	}
	if notifMsg.turnID != 5 {
		t.Errorf("notification turnID = %d, want 5", notifMsg.turnID)
	}

	// Streaming assistant message should carry the backend TurnID
	if model.streamingMsgIdx < 0 {
		t.Fatal("no streaming message created")
	}
	if model.messages[model.streamingMsgIdx].turnID != 5 {
		t.Errorf("streaming turnID = %d, want 5", model.messages[model.streamingMsgIdx].turnID)
	}
}

// TestTurnStarted_UserTyped_AdoptsBackendTurnID verifies that for user-typed
// messages (where startAgentTurn was already called by sendMessage), the
// turn_started event adopts the backend TurnID without creating a duplicate
// streaming message.
func TestTurnStarted_UserTyped_AdoptsBackendTurnID(t *testing.T) {
	model := initTestModel()

	// User typed a message → sendMessage → startAgentTurn (local turnID)
	model.startAgentTurn()
	localTurnID := model.agentTurnID
	if localTurnID == 0 {
		t.Fatal("startAgentTurn should set a non-zero local turnID")
	}

	// turn_started arrives from the backend with a different TurnID
	sendProgress(model, &protocol.ProgressEvent{
		Phase:  "turn_started",
		TurnID: 42,
		TurnStart: &protocol.TurnStartInfo{
			Trigger:   "user",
			RequestID: "req-1",
		},
	})

	// Backend TurnID should be adopted
	if model.agentTurnID != 42 {
		t.Fatalf("agentTurnID = %d, want 42", model.agentTurnID)
	}

	// No duplicate streaming message — only one assistant message should exist
	assistantCount := 0
	for _, msg := range model.messages {
		if msg.role == "assistant" {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Errorf("expected 1 assistant message, got %d (duplicate created?)", assistantCount)
	}

	// Streaming message should have the backend TurnID
	if model.streamingMsgIdx < 0 {
		t.Fatal("streaming message missing")
	}
	if model.messages[model.streamingMsgIdx].turnID != 42 {
		t.Errorf("streaming turnID = %d, want 42", model.messages[model.streamingMsgIdx].turnID)
	}
}

// TestTurnStarted_CrossGoroutineRace_FinalizesPreviousTurn is the KEY
// reproduction test for the original bug. When turn_started for turn N+1
// arrives BEFORE turn N's reply (cross-goroutine race between handleProgressDrain
// and handleOutbound), the TUI must:
//  1. Finalize turn N as-is (streamed content already visible)
//  2. Start turn N+1
//  3. When turn N's reply arrives late, apply it to turn N's message — NOT
//     turn N+1's streaming message
func TestTurnStarted_CrossGoroutineRace_FinalizesPreviousTurn(t *testing.T) {
	model := initTestModel()

	// Turn 1: user typed → startAgentTurn → turn_started adopts backend TurnID=10
	model.startAgentTurn()
	sendProgress(model, &protocol.ProgressEvent{
		Phase:  "turn_started",
		TurnID: 10,
		TurnStart: &protocol.TurnStartInfo{
			Trigger: "user",
		},
	})
	if model.agentTurnID != 10 {
		t.Fatalf("agentTurnID = %d, want 10", model.agentTurnID)
	}

	// Simulate streaming content for turn 10
	model.progressState.current = &protocol.ProgressEvent{
		StreamContent: "partial reply from turn 10",
	}

	// ── RACE: turn_started for turn 11 arrives BEFORE turn 10's reply ──
	sendProgress(model, &protocol.ProgressEvent{
		Phase:  "turn_started",
		TurnID: 11,
		TurnStart: &protocol.TurnStartInfo{
			Trigger: "notification",
			Content: "bg task done",
		},
	})

	// Turn 11 should now be active
	if model.agentTurnID != 11 {
		t.Fatalf("agentTurnID = %d, want 11", model.agentTurnID)
	}
	if !model.typing {
		t.Fatal("typing should be true for turn 11")
	}

	// Turn 11's streaming message must exist
	if model.streamingMsgIdx < 0 {
		t.Fatal("streaming message for turn 11 missing")
	}
	if model.messages[model.streamingMsgIdx].turnID != 11 {
		t.Errorf("streaming turnID = %d, want 11", model.messages[model.streamingMsgIdx].turnID)
	}

	// ── Turn 10's reply arrives LATE (after turn 11 started) ──
	// Must NOT use sendDone (which sets typing=false) — turn 11 is still active.
	model.Update(cliOutboundMsg{
		msg: channel.OutboundMsg{
			Channel: model.channelName,
			ChatID:  model.chatID,
			Content: "final reply for turn 10",
			TurnID:  10,
		},
	})

	// Turn 11 must still be active — turn 10's late reply must not end it
	if !model.typing {
		t.Fatal("typing should still be true — turn 10's late reply ended turn 11")
	}
	if model.agentTurnID != 11 {
		t.Fatalf("agentTurnID = %d, want 11 (turn 10's reply corrupted turn 11)", model.agentTurnID)
	}

	// Turn 11's streaming message must NOT have turn 10's content
	streaming := model.messages[model.streamingMsgIdx]
	if streaming.content == "final reply for turn 10" {
		t.Error("turn 10's reply leaked into turn 11's streaming message — THIS IS THE BUG")
	}
	if streaming.turnID != 11 {
		t.Errorf("streaming turnID = %d, want 11", streaming.turnID)
	}

	// Turn 10's reply should be applied to turn 10's message (not lost)
	var turn10Msg *cliMessage
	for i := range model.messages {
		if model.messages[i].role == "assistant" && model.messages[i].turnID == 10 {
			turn10Msg = &model.messages[i]
			break
		}
	}
	if turn10Msg == nil {
		t.Fatal("turn 10's assistant message not found — reply was lost")
	}
}

// TestTurnStarted_TurnIDRegression_Detected verifies that the TUI detects
// a TurnID regression (going backwards) and logs a warning. The turn lifecycle
// still proceeds correctly — the assertion is diagnostic, not blocking.
func TestTurnStarted_TurnIDRegression_Detected(t *testing.T) {
	model := initTestModel()

	// First turn: TurnID=5
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "turn_started",
		TurnID:    5,
		TurnStart: &protocol.TurnStartInfo{Trigger: "user"},
	})
	if model.lastReceivedTurnID != 5 {
		t.Fatalf("lastReceivedTurnID = %d, want 5", model.lastReceivedTurnID)
	}

	// Second turn: TurnID=3 (REGRESSION — should be > 5)
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "turn_started",
		TurnID:    3,
		TurnStart: &protocol.TurnStartInfo{Trigger: "user"},
	})

	// The model should still adopt TurnID=3 (diagnostic only, not blocking)
	if model.agentTurnID != 3 {
		t.Errorf("agentTurnID = %d, want 3 (assertion is diagnostic)", model.agentTurnID)
	}
	// lastReceivedTurnID should track the most recent (3 < 5, but still updated)
	if model.lastReceivedTurnID != 3 {
		t.Errorf("lastReceivedTurnID = %d, want 3", model.lastReceivedTurnID)
	}
}

// TestTurnStarted_TurnIDGap_Detected verifies that a TurnID gap (skipping a
// number) is detected and logged as a warning.
func TestTurnStarted_TurnIDGap_Detected(t *testing.T) {
	model := initTestModel()

	// First turn: TurnID=5
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "turn_started",
		TurnID:    5,
		TurnStart: &protocol.TurnStartInfo{Trigger: "user"},
	})

	// Second turn: TurnID=8 (GAP — skipped 6, 7)
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "turn_started",
		TurnID:    8,
		TurnStart: &protocol.TurnStartInfo{Trigger: "notification", Content: "test"},
	})

	// The gap (8 - 5 - 1 = 2) should be accepted but warned about
	if model.agentTurnID != 8 {
		t.Errorf("agentTurnID = %d, want 8", model.agentTurnID)
	}
}

// TestIterationContinuity_GapDetected verifies that the TUI detects an
// iteration gap (e.g., 0 → 2, skipping 1) when processing structured progress.
func TestIterationContinuity_GapDetected(t *testing.T) {
	model := initTestModel()

	// Start turn with TurnID=1
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "turn_started",
		TurnID:    1,
		TurnStart: &protocol.TurnStartInfo{Trigger: "user"},
	})

	// Iteration 0
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 0,
	}, protocol.ProgressEvent{Iteration: 0})

	if model.progressState.lastIter != 0 {
		t.Fatalf("lastIter = %d, want 0", model.progressState.lastIter)
	}

	// Iteration 2 (GAP — skipped 1)
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 2,
	}, protocol.ProgressEvent{Iteration: 2})

	// The gap should be accepted but warned about; lastIter advances to 2
	if model.progressState.lastIter != 2 {
		t.Errorf("lastIter = %d, want 2", model.progressState.lastIter)
	}
}

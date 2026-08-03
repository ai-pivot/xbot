package agent

import (
	"context"
	"testing"

	"xbot/bus"
	"xbot/channel"
	"xbot/llm"
)

// TestHandleRunOutput_PersistsTurnID verifies that handleRunOutput persists
// the assistant message with the correct TurnID (from msg.Metadata["turn_id"]),
// NOT zero. Without this, the DB row has turn_id=0 while the SSE text event
// carries the real turn_id — the mismatch defeats dedupMessages and
// reconcileHistoryWithLiveRows on the frontend, producing two consecutive
// assistant messages (DB-persisted + SSE-live) with the same content.
//
// handleCancelledRun already sets cancelMsg.TurnID; handleRunOutput must too.
func TestHandleRunOutput_PersistsTurnID(t *testing.T) {
	a := &Agent{
		directSend: func(msg channel.OutboundMsg) (string, error) {
			return "", nil
		},
		channelFinder: func(name string) (channel.Channel, bool) { return nil, false },
	}
	_, sess := newAgentHistorySession(t)

	// Simulate a normal (non-cancel) Run output with a final reply.
	out := &RunOutput{
		OutboundMsg: &channel.OutboundMsg{
			Content: "test reply",
		},
	}
	msg := bus.InboundMessage{
		Channel: "web",
		ChatID:  "chat-1",
		Content: "user question",
	}
	// turn_id is set by chatProcessLoop before calling processMessage.
	msg.Metadata = map[string]string{"turn_id": "42"}

	// handleRunOutput calls sendMessage internally. Without a directSend or
	// bus, it returns an error — but the assistant message is persisted
	// BEFORE sendMessage is called. We only need to verify the DB row's
	// turn_id.
	a.handleRunOutput(context.Background(), msg, out, sess, "")

	// Read back the persisted messages and find the assistant reply.
	msgs, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	var assistantMsg *llm.ChatMessage
	for i := range msgs {
		if msgs[i].Role == "assistant" && msgs[i].Content == "test reply" {
			assistantMsg = &msgs[i]
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("assistant message not found in DB")
	}
	if assistantMsg.TurnID != 42 {
		t.Fatalf("assistant message TurnID = %d, want 42 — handleRunOutput must set TurnID from msg.Metadata[\"turn_id\"] so the frontend can dedup DB row against SSE live message", assistantMsg.TurnID)
	}
}

// TestHandleCancelledRun_PersistsTurnID verifies the same invariant for the
// cancel path (already correct — this is a regression guard).
func TestHandleCancelledRun_PersistsTurnID(t *testing.T) {
	a := &Agent{
		directSend: func(msg channel.OutboundMsg) (string, error) {
			return "", nil
		},
		channelFinder: func(name string) (channel.Channel, bool) { return nil, false },
	}
	_, sess := newAgentHistorySession(t)

	msg := bus.InboundMessage{
		Channel: "web",
		ChatID:  "chat-1",
		Content: "user question",
	}
	// turn_id is set by chatProcessLoop before calling processMessage.
	msg.Metadata = map[string]string{"turn_id": "99"}

	a.handleCancelledRun(context.Background(), msg, &RunOutput{}, sess)

	msgs, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	var cancelMsg *llm.ChatMessage
	for i := range msgs {
		if msgs[i].Role == "assistant" && msgs[i].Content == "[interrupted]" {
			cancelMsg = &msgs[i]
			break
		}
	}
	if cancelMsg == nil {
		t.Fatal("[interrupted] message not found in DB")
	}
	if cancelMsg.TurnID != 99 {
		t.Fatalf("cancelled message TurnID = %d, want 99", cancelMsg.TurnID)
	}
}

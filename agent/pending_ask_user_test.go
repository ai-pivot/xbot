package agent

import (
	"context"
	"testing"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/protocol"
	"xbot/tools"
)

func TestHandleRunOutputPreservesRequestIDFromRealAskUserMetadata(t *testing.T) {
	toolResult, err := (&tools.AskUserTool{}).Execute(&tools.ToolContext{}, `{"questions":[{"question":"Continue?","options":["yes","no"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	toolRequestID := toolResult.Metadata["request_id"]
	if toolRequestID == "" {
		t.Fatal("AskUser tool metadata has no request ID")
	}

	a := &Agent{}
	outbound, err := a.handleRunOutput(
		context.Background(),
		bus.InboundMessage{Channel: "web", ChatID: "chat-1"},
		&RunOutput{OutboundMsg: &channel.OutboundMsg{
			WaitingUser: toolResult.WaitingUser,
			Metadata:    toolResult.Metadata,
		}},
		nil,
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	requestID := outbound.Metadata["request_id"]
	if requestID != toolRequestID {
		t.Fatalf("WaitingUser outbound request ID = %q, want %q", requestID, toolRequestID)
	}
	pending := a.GetPendingAskUser("web", "chat-1")
	if pending == nil || pending.RequestID != requestID {
		t.Fatalf("pending AskUser = %#v, want request ID %q", pending, requestID)
	}
	if len(pending.Questions) != 1 || pending.Questions[0].Question != "Continue?" {
		t.Fatalf("pending AskUser questions = %#v", pending.Questions)
	}
}

func TestWithPendingAskUserBlocksConcurrentClearUntilCallbackReturns(t *testing.T) {
	a := &Agent{}
	a.setPendingAskUser("web", "chat-1", &protocol.ProgressEvent{
		RequestID: "request-1",
		Questions: []protocol.AskUserQuestion{{Question: "Continue?"}},
	})

	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	withDone := make(chan bool, 1)
	go func() {
		withDone <- a.WithPendingAskUser("web", "chat-1", func(pending *protocol.ProgressEvent) bool {
			if pending.RequestID != "request-1" {
				t.Errorf("request ID = %q, want request-1", pending.RequestID)
			}
			close(callbackEntered)
			<-releaseCallback
			return true
		})
	}()
	<-callbackEntered

	clearDone := make(chan struct{})
	go func() {
		a.ClearPendingAskUser("web", "chat-1")
		close(clearDone)
	}()
	select {
	case <-clearDone:
		t.Fatal("ClearPendingAskUser returned while callback held the pending snapshot")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseCallback)
	if ok := <-withDone; !ok {
		t.Fatal("WithPendingAskUser returned false for a pending prompt")
	}
	select {
	case <-clearDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ClearPendingAskUser did not return after callback completed")
	}
	if pending := a.GetPendingAskUser("web", "chat-1"); pending != nil {
		t.Fatalf("pending AskUser after clear = %#v", pending)
	}
}

func TestWithPendingAskUserReturnsDetachedSnapshot(t *testing.T) {
	a := &Agent{}
	a.setPendingAskUser("cli", "chat-1", &protocol.ProgressEvent{
		RequestID: "request-1",
		Questions: []protocol.AskUserQuestion{{Question: "Original"}},
	})

	if ok := a.WithPendingAskUser("web", "chat-1", func(pending *protocol.ProgressEvent) bool {
		pending.RequestID = "changed"
		pending.Questions[0].Question = "Changed"
		return true
	}); !ok {
		t.Fatal("WithPendingAskUser did not find chat by channel fallback")
	}

	pending := a.GetPendingAskUser("cli", "chat-1")
	if pending == nil || pending.RequestID != "request-1" || pending.Questions[0].Question != "Original" {
		t.Fatalf("stored pending AskUser was mutated through snapshot: %#v", pending)
	}
}

func TestPendingAskUserCancelPreventsReplayAndNextTurnCancellation(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	key := "web:chat-1"
	a.setPendingAskUser("web", "chat-1", &protocol.ProgressEvent{RequestID: "request-1"})
	// A stale queued cancel must also be removed when the pending prompt wins
	// the cancellation race.
	a.pendingCancel.Store(key, true)

	if !a.acknowledgePendingAskUserCancel(bus.InboundMessage{Channel: "web", ChatID: "chat-1"}) {
		t.Fatal("pending AskUser cancel was not consumed")
	}
	if a.WithPendingAskUser("web", "chat-1", func(*protocol.ProgressEvent) bool {
		t.Fatal("cancelled AskUser remained replayable")
		return true
	}) {
		t.Fatal("cancelled AskUser remained pending")
	}
	if _, pending := a.pendingCancel.LoadAndDelete(key); pending {
		t.Fatal("cancelled AskUser armed pendingCancel for the next turn")
	}

	select {
	case ack := <-a.bus.Outbound:
		if ack.Metadata["cancelled"] != "true" {
			t.Fatalf("cancel ack metadata = %#v", ack.Metadata)
		}
	default:
		t.Fatal("pending AskUser cancel produced no acknowledgement")
	}
}

func TestQueuedAskUserAnswerCancelTargetsQueuedContinuation(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	key := "web:chat-1"
	a.setPendingAskUser("web", "chat-1", &protocol.ProgressEvent{RequestID: "request-1"})
	answer := bus.InboundMessage{
		Channel:  "web",
		ChatID:   "chat-1",
		Content:  "yes",
		Metadata: map[string]string{"ask_user_answered": "true"},
	}
	queue := make(chan bus.InboundMessage, 1)
	queue <- answer
	a.clearPendingAskUserForEnqueuedAnswer(answer)

	if pending := a.GetPendingAskUser("web", "chat-1"); pending != nil {
		t.Fatalf("pending AskUser remained after answer enqueue: %#v", pending)
	}
	a.interceptCancel(bus.InboundMessage{Channel: "web", ChatID: "chat-1", Content: "/cancel"})
	if _, pending := a.pendingCancel.LoadAndDelete(key); !pending {
		t.Fatal("cancel did not target the queued AskUser continuation")
	}
	select {
	case ack := <-a.bus.Outbound:
		t.Fatalf("queued continuation received premature cancel ack: %#v", ack)
	default:
	}
}

func TestActiveAskUserAnswerCancelSignalsActiveContinuation(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	key := "web:chat-1"
	cancelCh := make(chan struct{}, 1)
	a.chatCancelCh.Store(key, cancelCh)
	// Simulate the narrow handoff window where the old prompt is still visible
	// even though its answer continuation has become active.
	a.setPendingAskUser("web", "chat-1", &protocol.ProgressEvent{RequestID: "request-1"})

	a.interceptCancel(bus.InboundMessage{Channel: "web", ChatID: "chat-1", Content: "/cancel"})
	select {
	case <-cancelCh:
	default:
		t.Fatal("active AskUser continuation did not receive cancel signal")
	}
	if pending := a.GetPendingAskUser("web", "chat-1"); pending != nil {
		t.Fatalf("old AskUser prompt remained after active cancel: %#v", pending)
	}
	if _, pending := a.pendingCancel.LoadAndDelete(key); pending {
		t.Fatal("active AskUser cancel armed pendingCancel")
	}
	select {
	case ack := <-a.bus.Outbound:
		t.Fatalf("active continuation received premature cancel ack: %#v", ack)
	default:
	}
}

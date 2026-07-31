package agent

import (
	"context"
	"testing"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/llm"
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
	_, sess := newAgentHistorySession(t)
	// AppendAskQuestion requires a preceding AskUser tool result in history.
	toolMsg := llm.NewToolMessage("AskUser", toolResult.Metadata["request_id"], "", toolResult.Summary)
	if err := sess.AddMessage(toolMsg); err != nil {
		t.Fatal(err)
	}
	outbound, err := a.handleRunOutput(
		context.Background(),
		bus.InboundMessage{Channel: "web", ChatID: "chat-1"},
		&RunOutput{OutboundMsg: &channel.OutboundMsg{
			WaitingUser: toolResult.WaitingUser,
			Metadata:    toolResult.Metadata,
		}},
		sess,
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

func TestWithPendingAskUserDoesNotBlockUnrelatedSessionMutation(t *testing.T) {
	a := &Agent{}
	a.setPendingAskUser("web", "chat-1", &protocol.ProgressEvent{RequestID: "request-1"})
	a.setPendingAskUser("web", "chat-2", &protocol.ProgressEvent{RequestID: "request-2"})

	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	withDone := make(chan struct{})
	go func() {
		defer close(withDone)
		a.WithPendingAskUser("web", "chat-1", func(*protocol.ProgressEvent) bool {
			close(callbackEntered)
			<-releaseCallback
			return true
		})
	}()
	<-callbackEntered

	mutationDone := make(chan struct{})
	go func() {
		a.ClearPendingAskUser("web", "chat-2")
		a.setPendingAskUser("web", "chat-3", &protocol.ProgressEvent{RequestID: "request-3"})
		close(mutationDone)
	}()
	select {
	case <-mutationDone:
	case <-time.After(time.Second):
		t.Fatal("unrelated pending AskUser mutation blocked behind callback")
	}
	if pending := a.GetPendingAskUser("web", "chat-2"); pending != nil {
		t.Fatalf("unrelated pending AskUser was not cleared: %#v", pending)
	}
	if pending := a.GetPendingAskUser("web", "chat-3"); pending == nil || pending.RequestID != "request-3" {
		t.Fatalf("unrelated pending AskUser was not set: %#v", pending)
	}

	close(releaseCallback)
	select {
	case <-withDone:
	case <-time.After(2 * time.Second):
		t.Fatal("pending AskUser callback did not finish")
	}
}

func TestWithPendingAskUserReturnsDetachedSnapshot(t *testing.T) {
	a := &Agent{}
	a.setPendingAskUser("cli", "chat-1", &protocol.ProgressEvent{
		RequestID: "request-1",
		Questions: []protocol.AskUserQuestion{{Question: "Original", Options: []string{"yes"}}},
	})

	if ok := a.WithPendingAskUser("web", "chat-1", func(*protocol.ProgressEvent) bool {
		return true
	}); ok {
		t.Fatal("WithPendingAskUser crossed the qualified channel boundary")
	}
	a.ClearPendingAskUser("web", "chat-1")

	if ok := a.WithPendingAskUser("cli", "chat-1", func(pending *protocol.ProgressEvent) bool {
		pending.RequestID = "changed"
		pending.Questions[0].Question = "Changed"
		pending.Questions[0].Options[0] = "no"
		return true
	}); !ok {
		t.Fatal("WithPendingAskUser did not find the qualified session")
	}

	pending := a.GetPendingAskUser("cli", "chat-1")
	if pending == nil || pending.RequestID != "request-1" || pending.Questions[0].Question != "Original" || pending.Questions[0].Options[0] != "yes" {
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

	a.interceptCancel(bus.InboundMessage{Channel: "web", ChatID: "chat-1", Content: "/cancel"})
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
	nextCtx, nextCancel := context.WithCancel(context.Background())
	defer nextCancel()
	if !a.registerActiveCancelState(key, make(chan struct{}, 1), nextCancel) {
		t.Fatal("queued AskUser continuation did not consume pending cancel")
	}
	if nextCtx.Err() != context.Canceled {
		t.Fatal("queued AskUser continuation was not cancelled before processing")
	}
	a.finishActiveCancelState(key, nextCtx, nextCancel)
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
	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()
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
	if !a.finishActiveCancelState(key, reqCtx, reqCancel) {
		t.Fatal("active teardown did not observe cancel requested before teardown")
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

func TestCancelAfterActiveTeardownTargetsNextQueuedContinuation(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	key := "web:chat-1"
	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()
	a.chatCancelCh.Store(key, make(chan struct{}, 1))

	if a.finishActiveCancelState(key, reqCtx, reqCancel) {
		t.Fatal("normal active teardown reported cancellation")
	}
	a.interceptCancel(bus.InboundMessage{Channel: "web", ChatID: "chat-1", Content: "/cancel"})
	nextCtx, nextCancel := context.WithCancel(context.Background())
	defer nextCancel()
	if !a.registerActiveCancelState(key, make(chan struct{}, 1), nextCancel) {
		t.Fatal("cancel arriving after teardown was not preserved for the next queued continuation")
	}
	if nextCtx.Err() != context.Canceled {
		t.Fatal("post-teardown cancel did not cancel the next queued continuation")
	}
	a.finishActiveCancelState(key, nextCtx, nextCancel)
	select {
	case ack := <-a.bus.Outbound:
		t.Fatalf("post-teardown queued cancel received premature ack: %#v", ack)
	default:
	}
}

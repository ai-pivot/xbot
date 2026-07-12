package agent

import (
	"testing"
	"time"

	"xbot/protocol"
)

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

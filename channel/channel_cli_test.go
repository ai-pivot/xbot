package channel

import (
	"testing"
	"time"

	"xbot/protocol"
)

func TestChannelCLIQueuesDestructiveEventsWhenEventChannelIsFull(t *testing.T) {
	eventCh := make(chan protocol.WSMessage, 1)
	cli := NewChannelCliChannel(eventCh)
	eventCh <- protocol.WSMessage{Type: protocol.MsgTypeRPCResponse, ID: "busy"}
	done := make(chan struct{})
	go func() {
		cli.SendSessionState(protocol.SessionEvent{
			Channel: "cli", ChatID: "chat", Action: "history_rewound", TargetHistoryID: 7,
		})
		cli.sendMsgReliable(protocol.WSMessage{Type: protocol.MsgTypeText, Content: "after gate"})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("destructive event returned before it entered the full event channel")
	case <-time.After(20 * time.Millisecond):
	}
	<-eventCh
	select {
	case msg := <-eventCh:
		if msg.Session == nil || msg.Session.Action != "history_rewound" {
			t.Fatalf("reliable session event = %#v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("history rewind was not delivered after eventCh freed")
	}
	select {
	case msg := <-eventCh:
		if msg.Content != "after gate" {
			t.Fatalf("event after gate = %#v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("event after gate was not queued after history rewind")
	}
	<-done

	eventCh <- protocol.WSMessage{Type: protocol.MsgTypeRPCResponse, ID: "busy-again"}
	done = make(chan struct{})
	go func() {
		defer close(done)
		if _, err := cli.Send(OutboundMsg{
			Channel: "cli", ChatID: "chat", Metadata: map[string]string{"session_reset": "true"},
		}); err != nil {
			t.Errorf("Send session reset: %v", err)
		}
	}()
	select {
	case <-done:
		t.Fatal("session reset returned before it entered the full event channel")
	case <-time.After(20 * time.Millisecond):
	}
	<-eventCh
	select {
	case msg := <-eventCh:
		if msg.Metadata["session_reset"] != "true" {
			t.Fatalf("reliable session reset = %#v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("session reset was not delivered after eventCh freed")
	}
	<-done
	cli.Stop()
}

func TestChannelCLIStopCancelsBlockedDestructiveEvent(t *testing.T) {
	eventCh := make(chan protocol.WSMessage, 1)
	cli := NewChannelCliChannel(eventCh)
	eventCh <- protocol.WSMessage{Type: protocol.MsgTypeText}
	delivered := make(chan struct{})
	go func() {
		cli.SendSessionState(protocol.SessionEvent{Action: "history_rewound"})
		close(delivered)
	}()

	stopped := make(chan struct{})
	go func() {
		cli.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel blocked destructive event")
	}
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("blocked destructive sender did not return after Stop")
	}
}

package channel

import (
	"testing"
	"time"

	"xbot/protocol"
)

// C1: ChannelCliChannel must implement AskUserResolvedSender and deliver the
// invalidation as a {type:"ask_user_resolved"} WSMessage into the event
// channel — this is the transport the in-process CLI client subscribes to.
func TestChannelCLISendAskUserResolved(t *testing.T) {
	var _ AskUserResolvedSender = (*ChannelCliChannel)(nil)

	eventCh := make(chan protocol.WSMessage, 4)
	cli := NewChannelCliChannel(eventCh)

	cli.SendAskUserResolved(protocol.AskUserResolvedEvent{
		Channel: "cli", ChatID: "chat-1", RequestID: "req-1", Reason: "answered",
	})

	select {
	case msg := <-eventCh:
		if msg.Type != protocol.MsgTypeAskUserResolved {
			t.Fatalf("delivered type = %q, want %q", msg.Type, protocol.MsgTypeAskUserResolved)
		}
		if msg.Channel != "cli" || msg.ChatID != "chat-1" {
			t.Fatalf("delivered target = (%q, %q), want (cli, chat-1)", msg.Channel, msg.ChatID)
		}
		if msg.AskUserResolvedRequestID != "req-1" || msg.AskUserResolvedReason != "answered" {
			t.Fatalf("delivered request/reason = (%q, %q), want (req-1, answered)",
				msg.AskUserResolvedRequestID, msg.AskUserResolvedReason)
		}
	case <-time.After(time.Second):
		t.Fatal("ask_user_resolved was not delivered to the event channel")
	}
}

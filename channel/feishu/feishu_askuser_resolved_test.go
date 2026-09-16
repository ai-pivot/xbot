package feishu

import (
	"testing"

	"xbot/channel"
	"xbot/protocol"
)

// C4: SendAskUserResolved must drop the pending text-reply fallback entries of
// the resolved chat (the map key is "chatID:senderID"), leaving other chats
// untouched.
func TestSendAskUserResolvedClearsPendingEntries(t *testing.T) {
	var _ channel.AskUserResolvedSender = (*FeishuChannel)(nil)

	f := newTestChannelWithBus()
	registerPendingAskUser(f, "oc_chat_a", "ou_user_1")
	registerPendingAskUser(f, "oc_chat_a", "ou_user_2")
	registerPendingAskUser(f, "oc_chat_b", "ou_user_3")

	f.SendAskUserResolved(protocol.AskUserResolvedEvent{
		Channel: "feishu", ChatID: "oc_chat_a", RequestID: "req-1", Reason: "answered",
	})

	f.askUserMu.Lock()
	_, a1 := f.askUsers["oc_chat_a:ou_user_1"]
	_, a2 := f.askUsers["oc_chat_a:ou_user_2"]
	_, b := f.askUsers["oc_chat_b:ou_user_3"]
	f.askUserMu.Unlock()

	if a1 || a2 {
		t.Fatalf("resolved chat entries survived: user_1=%v user_2=%v", a1, a2)
	}
	if !b {
		t.Fatal("unrelated chat entry was removed")
	}
}

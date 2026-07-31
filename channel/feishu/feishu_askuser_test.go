package feishu

import (
	"testing"
	"time"

	"xbot/bus"
	"xbot/channel"
)

// newTestChannelWithBus creates a FeishuChannel with a real MessageBus so
// tests can drain Inbound to verify message routing.
func newTestChannelWithBus() *FeishuChannel {
	return NewFeishuChannel(FeishuConfig{}, bus.NewMessageBus())
}

// registerPendingAskUser simulates an AskUser card being sent in a chat.
// For P2P chats, msgChatID is senderID (the session uses open_id as ChatID);
// for group chats, msgChatID is the group's oc_xxx.
func registerPendingAskUser(f *FeishuChannel, msgChatID, senderID string) {
	askKey := msgChatID + ":" + senderID
	f.askUserMu.Lock()
	f.askUsers[askKey] = &feishuPendingAskUser{
		ChatID:    msgChatID,
		SenderID:  senderID,
		Questions: []channel.AskQItem{{Question: "test question"}},
		CreatedAt: time.Now(),
		Answers:   make(map[int]string),
	}
	f.askUserMu.Unlock()
}

func TestFindPendingAskUser_PrimaryMatch(t *testing.T) {
	f := newTestChannelWithBus()
	senderID := "ou_test_user"
	chatID := "oc_group_123"

	registerPendingAskUser(f, chatID, senderID)

	// Exact chatID:senderID match should work for any chatType
	for _, chatType := range []string{"group", "p2p", ""} {
		key, pending := f.findPendingAskUser(chatID, senderID, chatType)
		if pending == nil {
			t.Fatalf("expected pending AskUser for chatType=%q, got nil", chatType)
		}
		if key != chatID+":"+senderID {
			t.Fatalf("expected primary key, got %q", key)
		}
	}
}

func TestFindPendingAskUser_P2PFallback_ForP2PChat(t *testing.T) {
	f := newTestChannelWithBus()
	senderID := "ou_test_user"

	// Simulate P2P AskUser: pending registered with senderID:senderID
	registerPendingAskUser(f, senderID, senderID)

	// P2P text reply: chatID from event is oc_xxx, not senderID
	chatID := "oc_p2p_chat"
	key, pending := f.findPendingAskUser(chatID, senderID, "p2p")
	if pending == nil {
		t.Fatal("expected P2P fallback to find pending AskUser for p2p chat, got nil")
	}
	if key != senderID+":"+senderID {
		t.Fatalf("expected p2p key %q, got %q", senderID+":"+senderID, key)
	}
}

func TestFindPendingAskUser_P2PFallback_ForCardCallback(t *testing.T) {
	f := newTestChannelWithBus()
	senderID := "ou_test_user"

	// Simulate P2P AskUser: pending registered with senderID:senderID
	registerPendingAskUser(f, senderID, senderID)

	// Card callback: chatType unknown ("")
	chatID := "oc_p2p_chat"
	key, pending := f.findPendingAskUser(chatID, senderID, "")
	if pending == nil {
		t.Fatal("expected P2P fallback to find pending AskUser for card callback, got nil")
	}
	if key != senderID+":"+senderID {
		t.Fatalf("expected p2p key %q, got %q", senderID+":"+senderID, key)
	}
}

// TestFindPendingAskUser_GroupMessageNotConsumedByP2PFallback is the core
// regression test for issue #174: a group chat message must NOT be consumed
// by a pending P2P AskUser.
func TestFindPendingAskUser_GroupMessageNotConsumedByP2PFallback(t *testing.T) {
	f := newTestChannelWithBus()
	senderID := "ou_test_user"

	// User has a pending P2P AskUser (registered senderID:senderID)
	registerPendingAskUser(f, senderID, senderID)

	// User sends a message in a GROUP chat
	groupChatID := "oc_group_456"
	_, pending := f.findPendingAskUser(groupChatID, senderID, "group")
	if pending != nil {
		t.Fatal("group message must NOT match a pending P2P AskUser — this is the #174 bug")
	}
}

// TestFindPendingAskUser_NoMatch verifies clean negative case.
func TestFindPendingAskUser_NoMatch(t *testing.T) {
	f := newTestChannelWithBus()
	senderID := "ou_test_user"

	// No pending AskUser at all
	_, pending := f.findPendingAskUser("oc_chat", senderID, "p2p")
	if pending != nil {
		t.Fatal("expected nil when no pending AskUser exists")
	}
}

// TestGroupMessageNotConsumedByP2PAskUser is an integration-level test.
// It verifies the full text-message routing path: when a user has a pending
// P2P AskUser and sends a text in a group, the message must reach the bus
// as a normal group message — not be consumed as an AskUser answer.
func TestGroupMessageNotConsumedByP2PAskUser(t *testing.T) {
	f := newTestChannelWithBus()

	senderID := "ou_test_user"
	groupChatID := "oc_group_chat"

	// User has a pending P2P AskUser
	registerPendingAskUser(f, senderID, senderID)

	// Simulate the text-message routing path (onMessage inner logic):
	// findPendingAskUser with group chatType should return nil.
	_, pending := f.findPendingAskUser(groupChatID, senderID, "group")
	if pending != nil {
		t.Fatal("BUG: group message matched a pending P2P AskUser — message would be consumed")
	}

	// Since pending is nil, tryResolveAskUserByText would not be called,
	// and the message would flow to the bus as a normal group message.
	// Verify no AskUser answer was recorded.
	f.askUserMu.Lock()
	p2pKey := senderID + ":" + senderID
	stillPending, exists := f.askUsers[p2pKey]
	f.askUserMu.Unlock()
	if !exists {
		t.Fatal("pending P2P AskUser should still exist (not consumed)")
	}
	if len(stillPending.Answers) > 0 {
		t.Fatalf("pending AskUser should have no answers, got %d", len(stillPending.Answers))
	}
}

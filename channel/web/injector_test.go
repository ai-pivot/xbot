package web

import (
	"testing"

	"xbot/channel"
	"xbot/protocol"
)

// TestWebChannelImplementsUserMessageInjector verifies that WebChannel
// implements channel.UserMessageInjector. Without this, injectCLIUserMessage
// (called by drainAndProcessNotifications for bg task / cron notifications)
// silently skips the web channel — the notification's user message is never
// pushed to the frontend via SSE/WS.
func TestWebChannelImplementsUserMessageInjector(t *testing.T) {
	var wc *WebChannel
	var _ channel.UserMessageInjector = wc // compile-time interface check
}

// TestWebChannelInjectUserMessage verifies that InjectUserMessage sends an
// inject_user WSMessage with the correct type and qualified chat_id.
func TestWebChannelInjectUserMessage(t *testing.T) {
	wc := &WebChannel{hub: newHub()}
	wc.evtBuf = make(map[string]*eventStream)
	wc.hub.seqFn = wc.stampAndBuffer

	// Register a web SSE client
	c := &Client{
		chatID:   "chat-1",
		connType: clientConnTypeSSE,
		sendCh:   make(chan protocol.WSMessage, 64),
	}
	wc.hub.mu.Lock()
	wc.hub.conns["client-1"] = c
	routeKey := sessionRouteKey("web", "chat-1")
	if wc.hub.subs[routeKey] == nil {
		wc.hub.subs[routeKey] = make(map[string]bool)
	}
	wc.hub.subs[routeKey]["client-1"] = true
	wc.hub.mu.Unlock()

	// Inject a notification user message
	wc.InjectUserMessage("web:chat-1", "⏰ [定时任务触发] 测试通知")

	// Drain delivered messages
	var delivered []protocol.WSMessage
drain:
	for {
		select {
		case msg := <-c.sendCh:
			delivered = append(delivered, msg)
		default:
			break drain
		}
	}

	if len(delivered) != 1 {
		t.Fatalf("expected 1 message, got %d", len(delivered))
	}

	msg := delivered[0]
	if msg.Type != protocol.MsgTypeInjectUser {
		t.Errorf("expected type %q, got %q", protocol.MsgTypeInjectUser, msg.Type)
	}
	if msg.Content != "⏰ [定时任务触发] 测试通知" {
		t.Errorf("expected content to be preserved, got %q", msg.Content)
	}
	if msg.ChatID != "web:chat-1" {
		t.Errorf("expected chat_id 'web:chat-1' (qualified for frontend matching), got %q", msg.ChatID)
	}
}

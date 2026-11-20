package web

import (
	"testing"

	"xbot/protocol"
)

// TestSessionStateFrameSurvivesFullSendCh pins the defect recorded in
// court/evidence/gongbu/web-idle-busy-race/sendch-drop-analysis.md: when a
// client's sendCh (cap 64) is saturated by a progress-frame burst, the sidebar
// session-state frame (busy/idle) was dropped silently, leaving the sidebar
// stuck on a stale state until a manual tree refresh.
//
// Judgement: while sendCh is FULL, a session-state frame must still be
// delivered through the stateless last-wins slot, and only the newest state
// must survive (busy then idle ⇒ idle).
func TestSessionStateFrameSurvivesFullSendCh(t *testing.T) {
	h := newHub()
	c := &Client{
		sendCh:       make(chan protocol.WSMessage, 1),
		done:         make(chan struct{}),
		id:           "sess-drop-client",
		userID:       "owner-1",
		connType:     clientConnTypeWS,
		statelessSig: make(chan struct{}, 1),
	}
	h.addClient(c.id, c)

	// Saturate sendCh so the push path hits its default branch.
	c.sendCh <- protocol.WSMessage{Type: protocol.MsgTypeProgress}

	busy := protocol.WSMessage{
		Type: protocol.MsgTypeSession,
		Session: &protocol.SessionEvent{
			Channel: "web", ChatID: "chat-A", Action: "busy", SenderID: "owner-1",
		},
	}
	h.broadcastSessionStateToWebClients(busy)

	got := c.drainStateless()
	if len(got) != 1 {
		t.Fatalf("session frame dropped when sendCh full: stateless slot has %d messages, want 1 (busy must fall back to last-wins)", len(got))
	}
	if got[0].Session == nil || got[0].Session.Action != "busy" {
		t.Fatalf("stateless slot holds %+v, want session busy", got[0])
	}

	// Last-wins: the newer state must overwrite the older one.
	idle := protocol.WSMessage{
		Type: protocol.MsgTypeSession,
		Session: &protocol.SessionEvent{
			Channel: "web", ChatID: "chat-A", Action: "idle", SenderID: "owner-1",
		},
	}
	h.broadcastSessionStateToWebClients(idle)

	got = c.drainStateless()
	if len(got) != 1 {
		t.Fatalf("last-wins broken: stateless slot has %d messages, want exactly 1 (idle)", len(got))
	}
	if got[0].Session == nil || got[0].Session.Action != "idle" {
		t.Fatalf("stateless slot holds %+v, want session idle (newest wins)", got[0])
	}
}

// TestSessionStateFrameSurvivesFullSendChRouteScoped covers the route-scoped
// delivery path (sendToClient / deliverToSubscribers) — the one that produced
// the historical "Hub.sendToClient: sendCh full, dropping message" warns.
func TestSessionStateFrameSurvivesFullSendChRouteScoped(t *testing.T) {
	h := newHub()
	c := &Client{
		sendCh:       make(chan protocol.WSMessage, 1),
		done:         make(chan struct{}),
		id:           "sess-drop-client-2",
		userID:       "owner-2",
		connType:     clientConnTypeWS,
		statelessSig: make(chan struct{}, 1),
	}
	h.addClient(c.id, c)
	routeKey := sessionRouteKey("web", "chat-B")
	if !h.subscribe(c.id, routeKey) {
		t.Fatal("subscribe failed")
	}
	c.sendCh <- protocol.WSMessage{Type: protocol.MsgTypeProgress} // saturate

	msg := protocol.WSMessage{
		Type:    protocol.MsgTypeSession,
		Channel: "web",
		ChatID:  "chat-B",
		Session: &protocol.SessionEvent{Channel: "web", ChatID: "chat-B", Action: "idle"},
	}
	if ok := h.deliverToSubscribers(routeKey, msg, msg, false); !ok {
		t.Fatal("deliverToSubscribers returned false (frame considered undelivered)")
	}

	got := c.drainStateless()
	if len(got) != 1 || got[0].Session == nil || got[0].Session.Action != "idle" {
		t.Fatalf("route-scoped session frame dropped when sendCh full: got %d stateless messages %+v, want 1 idle", len(got), got)
	}
}

// TestStatelessSlotKeySeparatesSessions guards the slot key: two sessions'
// states must not evict each other (per-source last-wins, not global).
func TestStatelessSlotKeySeparatesSessions(t *testing.T) {
	a := &protocol.WSMessage{Type: protocol.MsgTypeSession, Session: &protocol.SessionEvent{Channel: "web", ChatID: "chat-A", Action: "busy"}}
	b := &protocol.WSMessage{Type: protocol.MsgTypeSession, Session: &protocol.SessionEvent{Channel: "web", ChatID: "chat-B", Action: "busy"}}
	if statelessSlotKey(a) == statelessSlotKey(b) {
		t.Fatalf("slot key collides for distinct sessions: %q", statelessSlotKey(a))
	}
}

package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xbot/bus"
	"xbot/protocol"
)

// ---------------------------------------------------------------------------
// ask_user_resolved (wave B): "pending 已解除" must reach EVERY web client of
// the session (WS + SSE, all tabs/devices), reconnect must reconcile clients
// holding a stale cached panel, and delivery must be repeat-safe.
// ---------------------------------------------------------------------------

func decodeAskUserResolvedMsg(t *testing.T, msg protocol.WSMessage) protocol.AskUserResolvedEvent {
	t.Helper()
	if msg.Type != protocol.MsgTypeAskUserResolved {
		t.Fatalf("message type = %q, want %q (msg=%#v)", msg.Type, protocol.MsgTypeAskUserResolved, msg)
	}
	// The event is carried as flat envelope fields (wire contract shared with
	// the Web front-end) — no nested Content JSON.
	return protocol.AskUserResolvedEvent{
		Channel:   msg.Channel,
		ChatID:    msg.ChatID,
		RequestID: msg.AskUserResolvedRequestID,
		Reason:    msg.AskUserResolvedReason,
	}
}

func waitForRouteEventType(t *testing.T, wc *WebChannel, sel SessionSelector, msgType string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range wc.replaySSEEvents(sel, 0) {
			if ev.Type == msgType {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("event type %q never reached route %s\x00%s", msgType, sel.Channel, sel.ChatID)
}

// W1: a live emission from the backend (wave A) must reach every subscriber
// of THAT session — two WS tabs + one SSE connection — and must not leak to
// a client viewing a different session.
func TestAskUserResolvedBroadcastReachesEveryClientOfSession(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	routeKey := sessionRouteKey("web", "web-1")

	subscribers := map[string]*Client{
		"ws-1":  {id: "ws-1", connType: clientConnTypeWS, sendCh: make(chan protocol.WSMessage, 8), done: make(chan struct{})},
		"ws-2":  {id: "ws-2", connType: clientConnTypeWS, sendCh: make(chan protocol.WSMessage, 8), done: make(chan struct{})},
		"sse-1": {id: "sse-1", connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 8), done: make(chan struct{}), sessionChannel: "web", chatID: "web-1"},
	}
	other := &Client{id: "ws-other", connType: clientConnTypeWS, sendCh: make(chan protocol.WSMessage, 8), done: make(chan struct{})}
	for id, c := range subscribers {
		if !wc.hub.addClient(id, c) || !wc.hub.subscribe(id, routeKey) {
			t.Fatalf("setup failed for %s", id)
		}
	}
	if !wc.hub.addClient(other.id, other) || !wc.hub.subscribe(other.id, sessionRouteKey("web", "web-2")) {
		t.Fatal("setup failed for ws-other")
	}

	// reason "cancelled" covers the busy-auto-cancel case; the transport must
	// not distinguish reasons — every client invalidates on sight.
	wc.SendAskUserResolved(protocol.AskUserResolvedEvent{
		Channel: "web", ChatID: "web-1", RequestID: "req-1", Reason: "cancelled",
	})

	for id, c := range subscribers {
		select {
		case msg := <-c.sendCh:
			ev := decodeAskUserResolvedMsg(t, msg)
			if ev.Channel != "web" || ev.ChatID != "web-1" || ev.RequestID != "req-1" || ev.Reason != "cancelled" {
				t.Fatalf("%s resolved event = %#v", id, ev)
			}
			if msg.Seq == 0 {
				t.Fatalf("%s resolved event is not sequenced: %#v", id, msg)
			}
			if msg.RouteChannel != "web" || msg.RouteChatID != "web-1" {
				t.Fatalf("%s resolved envelope route = %q/%q", id, msg.RouteChannel, msg.RouteChatID)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not receive ask_user_resolved", id)
		}
	}
	select {
	case msg := <-other.sendCh:
		t.Fatalf("client of another session received ask_user_resolved: %#v", msg)
	default:
	}
}

// W4: repeat delivery must be safe — two identical emissions arrive twice
// (no server-side dedup dependency, no panic) and the broadcast must NOT be
// gated on pending state (clients are the idempotency layer).
func TestAskUserResolvedRepeatBroadcastSideEffectFree(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	lookups := 0
	wc.SetCallbacks(WebCallbacks{
		WithPendingAskUser: func(channel, chatID string, fn func(*protocol.ProgressEvent) bool) bool {
			lookups++
			return false
		},
	})
	client := &Client{id: "ws-1", connType: clientConnTypeWS, sendCh: make(chan protocol.WSMessage, 8), done: make(chan struct{})}
	if !wc.hub.addClient(client.id, client) || !wc.hub.subscribe(client.id, sessionRouteKey("web", "web-1")) {
		t.Fatal("setup failed")
	}

	ev := protocol.AskUserResolvedEvent{Channel: "web", ChatID: "web-1", RequestID: "req-1", Reason: "answered"}
	wc.SendAskUserResolved(ev)
	wc.SendAskUserResolved(ev) // duplicate — must not panic or be gated

	for i := 0; i < 2; i++ {
		select {
		case msg := <-client.sendCh:
			if got := decodeAskUserResolvedMsg(t, msg); got.RequestID != "req-1" || got.Reason != "answered" {
				t.Fatalf("delivery %d event = %#v", i, got)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("delivery %d did not arrive", i)
		}
	}
	if lookups != 0 {
		t.Fatalf("broadcast consulted WithPendingAskUser %d times — delivery must be unconditional", lookups)
	}
	if got := wc.getEventStream(sessionRouteKey("web", "web-1")).lastSeq(); got != 2 {
		t.Fatalf("last sequence = %d, want 2 (each delivery is sequenced independently)", got)
	}
}

// W4 (SSE gate): resolved is NOT "consumed" like a resolved ask_user — it must
// be written even when no pending exists, and repeated delivery is safe.
func TestAskUserResolvedPassesSSEDeliveryWithoutPending(t *testing.T) {
	wc := &WebChannel{}
	wc.callbacks.WithPendingAskUser = func(ch, chatID string, fn func(*protocol.ProgressEvent) bool) bool {
		return false // no pending — ask_user would be consumed here; resolved must not be
	}
	recorder := httptest.NewRecorder()
	client := &Client{w: recorder, flusher: recorder, sseEncWriter: recorder}
	for seq := uint64(3); seq <= 4; seq++ {
		msg := protocol.WSMessage{Type: protocol.MsgTypeAskUserResolved, Seq: seq, ChatID: "chat-1", Channel: "web", AskUserResolvedReason: "cleared"}
		if err := wc.writeCurrentSSEEvent(client, msg); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Count(recorder.Body.String(), "event:ask_user_resolved"); got != 2 {
		t.Fatalf("ask_user_resolved frames = %d, want 2 (resolved must never be consumed): %q", got, recorder.Body.String())
	}
	if client.lastSentSeq != 4 {
		t.Fatalf("lastSentSeq = %d, want 4", client.lastSentSeq)
	}
	// Re-delivery of an already-sent seq is a wire-level no-op (no duplicate frame).
	if err := wc.writeCurrentSSEEvent(client, protocol.WSMessage{Type: protocol.MsgTypeAskUserResolved, Seq: 4, ChatID: "chat-1"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(recorder.Body.String(), "event:ask_user_resolved"); got != 2 {
		t.Fatalf("duplicate seq was written again: %d frames", got)
	}

	// Batch path: two resolved events in one batch are both written.
	batchRecorder := httptest.NewRecorder()
	batchClient := &Client{w: batchRecorder, flusher: batchRecorder, sseEncWriter: batchRecorder}
	batch := []protocol.WSMessage{
		{Type: protocol.MsgTypeAskUserResolved, Seq: 7, ChatID: "chat-1"},
		{Type: protocol.MsgTypeAskUserResolved, Seq: 8, ChatID: "chat-1"},
	}
	if err := wc.writeSSEBatch(t.Context(), batchClient, batch); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(batchRecorder.Body.String(), "event:ask_user_resolved"); got != 2 {
		t.Fatalf("batched ask_user_resolved frames = %d, want 2: %q", got, batchRecorder.Body.String())
	}
	if batchClient.lastSentSeq != 8 {
		t.Fatalf("batch lastSentSeq = %d, want 8", batchClient.lastSentSeq)
	}
}

// W4 (WS gate): the WS write gate only intercepts ask_user prompts awaiting a
// pending; ask_user_resolved must pass through untouched (no pending needed).
func TestAskUserResolvedPassesWSWriteGateWithoutPending(t *testing.T) {
	wc := &WebChannel{}
	wc.callbacks.WithPendingAskUser = func(ch, chatID string, fn func(*protocol.ProgressEvent) bool) bool {
		return false
	}
	client := &Client{}
	msg := protocol.WSMessage{Type: protocol.MsgTypeAskUserResolved, ChatID: "chat-1", ChatType: "", AskUserResolvedReason: "cleared"}
	var written *protocol.WSMessage
	ok, err := wc.writeCurrentWSMessage(client, msg, func(m protocol.WSMessage) error {
		copy := m
		written = &copy
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || written == nil {
		t.Fatalf("ask_user_resolved was dropped by the WS write gate (ok=%v written=%v)", ok, written)
	}
	if written.Type != protocol.MsgTypeAskUserResolved || written.AskUserResolvedReason != msg.AskUserResolvedReason {
		t.Fatalf("WS write gate mutated the event: %#v", written)
	}
}

// W2 (WS): reconnect with NO pending prompt must push a reconcile
// ask_user_resolved ("cleared") so a client holding a stale cached panel
// collapses it immediately. The retained ask_user must still NOT be replayed.
func TestWSReconnectWithoutPendingPushesAskUserResolved(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	wc.hub.sendToClient(chatID, protocol.WSMessage{
		Type:     protocol.MsgTypeAskUser,
		Progress: &protocol.ProgressEvent{RequestID: "request-1"},
	})
	wc.SetCallbacks(WebCallbacks{
		WithPendingAskUser: func(channel, gotChatID string, fn func(*protocol.ProgressEvent) bool) bool {
			return false // prompt was answered/cancelled elsewhere
		},
	})
	client := &Client{sendCh: make(chan protocol.WSMessage, 4)}

	runWSReplay(t, wc, client, chatID, 0)

	select {
	case msg := <-client.sendCh:
		ev := decodeAskUserResolvedMsg(t, msg)
		if ev.Channel != "web" || ev.ChatID != chatID || ev.Reason != "cleared" {
			t.Fatalf("reconcile event = %#v, want cleared for web-1", ev)
		}
		if msg.RouteChannel != "web" || msg.RouteChatID != chatID {
			t.Fatalf("reconcile envelope route = %q/%q", msg.RouteChannel, msg.RouteChatID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect without pending must push ask_user_resolved(cleared)")
	}
	select {
	case msg := <-client.sendCh:
		t.Fatalf("unexpected extra reconnect message: %#v", msg)
	default:
	}
}

// W2 guard (WS): a still-pending prompt must ONLY be re-announced — the
// reconcile invalidation must never fire while a pending exists (it would
// dismiss a live panel).
func TestWSReconnectWithPendingDoesNotInvalidate(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	wc.SetCallbacks(WebCallbacks{
		WithPendingAskUser: func(channel, gotChatID string, fn func(*protocol.ProgressEvent) bool) bool {
			return fn(&protocol.ProgressEvent{RequestID: "request-1"})
		},
	})
	client := &Client{sendCh: make(chan protocol.WSMessage, 4)}

	runWSReplay(t, wc, client, chatID, 0)

	select {
	case msg := <-client.sendCh:
		if msg.Type != protocol.MsgTypeAskUser {
			t.Fatalf("expected pending ask_user re-announce, got %#v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending ask_user was not re-announced")
	}
	select {
	case msg := <-client.sendCh:
		t.Fatalf("pending reconnect must not invalidate: %#v", msg)
	default:
	}
}

// W2 (SSE): a reconnect with NO pending prompt publishes a reconcile
// ask_user_resolved into the route stream (delivered to the reconnecting SSE
// client and deduped against the replay window on repeated connects).
func TestSSEConnectWithoutPendingPublishesAskUserResolved(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	wc.SetCallbacks(WebCallbacks{
		WithPendingAskUser: func(channel, gotChatID string, fn func(*protocol.ProgressEvent) bool) bool {
			return false
		},
	})
	client := &Client{id: "sse-1", connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 8), done: make(chan struct{}), sessionChannel: "web", chatID: chatID}
	if !wc.hub.addClient(client.id, client) || !wc.hub.subscribe(client.id, sessionRouteKey("web", chatID)) {
		t.Fatal("setup failed")
	}

	sel := SessionSelector{Channel: "web", ChatID: chatID}
	wc.publishSSEFallbacks(sel, 0)

	events := wc.replaySSEEvents(sel, 0)
	if len(events) != 1 || events[0].Type != protocol.MsgTypeAskUserResolved {
		t.Fatalf("published events = %#v, want one ask_user_resolved", events)
	}
	ev := decodeAskUserResolvedMsg(t, events[0])
	if ev.Channel != "web" || ev.ChatID != chatID || ev.Reason != "cleared" {
		t.Fatalf("reconcile event = %#v", ev)
	}
	select {
	case msg := <-client.sendCh:
		if got := decodeAskUserResolvedMsg(t, msg); got.Reason != "cleared" || got.ChatID != chatID {
			t.Fatalf("SSE client reconcile = %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SSE client did not receive the reconcile event")
	}

	// Second connect: the reconcile is already in the replay window the client
	// is about to receive — publishing another one would be pure noise.
	wc.publishSSEFallbacks(sel, 0)
	if events := wc.replaySSEEvents(sel, 0); len(events) != 1 {
		t.Fatalf("duplicate reconcile published: %#v", events)
	}
}

// W2 guard (SSE): pending exists → the ask_user fallback publishes and NO
// invalidation is added.
func TestSSEConnectWithPendingDoesNotInvalidate(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	wc.SetCallbacks(WebCallbacks{
		WithPendingAskUser: func(channel, gotChatID string, fn func(*protocol.ProgressEvent) bool) bool {
			return fn(&protocol.ProgressEvent{RequestID: "request-1"})
		},
	})

	sel := SessionSelector{Channel: "web", ChatID: chatID}
	wc.publishSSEFallbacks(sel, 0)

	events := wc.replaySSEEvents(sel, 0)
	if len(events) != 1 || events[0].Type != protocol.MsgTypeAskUser {
		t.Fatalf("published events = %#v, want one pending ask_user", events)
	}
}

// W3: the REST cancel entry points must carry the same ask_user_cancel marker
// as the WebSocket ask_user_response cancel path — without it, an external
// API client's stale cancel arms pendingCancel against the user's NEXT message.
func TestRESTCancelCarriesAskUserCancelMarker(t *testing.T) {
	db := newTestDB(t)
	msgBus := bus.NewMessageBus()
	msgBus.EnableDeliveryAcknowledgement()
	wc := NewWebChannel(WebChannelConfig{DB: db}, msgBus)
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	if _, err := db.Exec("INSERT INTO tenants (channel, chat_id, last_active_at) VALUES (?, ?, ?)", "web", "web-1", time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		path    string
		body    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{
			name:    "api_cancel",
			path:    "/api/cancel",
			body:    `{"chat_id":"web-1"}`,
			handler: wc.handleCancel,
		},
		{
			name:    "ask_user_respond_cancelled",
			path:    "/api/ask_user/respond",
			body:    `{"chat_id":"web-1","cancelled":true}`,
			handler: wc.handleAskUserRespond,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			go func() {
				cancel := <-msgBus.Inbound
				if cancel.Content != "/cancel" || cancel.ChatID != "web-1" {
					t.Errorf("unexpected cancel inbound: %#v", cancel)
				}
				if cancel.Metadata["ask_user_cancel"] != "true" {
					t.Errorf("REST cancel metadata missing ask_user_cancel marker: %#v", cancel.Metadata)
				}
				cancel.DeliveryAck <- bus.DeliveryResult{TurnID: 7}
			}()
			tc.handler(recorder, authedAPIRequest(http.MethodPost, tc.path, []byte(tc.body)))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

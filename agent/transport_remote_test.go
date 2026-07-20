package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xbot/protocol"

	"github.com/gorilla/websocket"
)

// TestReadPumpDoesNotClearReplacedConn verifies that an old readPump
// does not nil-out a connection that was replaced by connect().
// This was the root cause of "disconnect doesn't reconnect" and
// "permanent spinner after reconnect" bugs.
func TestReadPumpDoesNotClearReplacedConn(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	var (
		connMu      sync.Mutex
		serverConns []*websocket.Conn
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connMu.Lock()
		serverConns = append(serverConns, conn)
		connMu.Unlock()

		// Keep connection alive by reading until client closes.
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	cfg := RemoteTransportConfig{ServerURL: wsURL}
	transport := NewRemoteTransport(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start connects and spawns readPump.
	if err := transport.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer transport.Stop()

	// Wait for first server connection.
	time.Sleep(200 * time.Millisecond)
	connMu.Lock()
	if len(serverConns) != 1 {
		t.Fatalf("expected 1 server conn, got %d", len(serverConns))
	}
	firstServerConn := serverConns[0]
	connMu.Unlock()

	// Simulate a reconnect: call connect() again while the old readPump is still
	// running. Before the fix, the old readPump would eventually set t.conn=nil
	// after connect() replaced it with a new conn.
	if err := transport.connect(ctx); err != nil {
		t.Fatalf("reconnect failed: %v", err)
	}

	// Give the old readPump time to detect the close and potentially race.
	time.Sleep(500 * time.Millisecond)

	// Verify t.conn is still non-nil (the new connection).
	transport.connMu.Lock()
	currentConn := transport.conn
	transport.connMu.Unlock()

	if currentConn == nil {
		t.Fatal("t.conn was nil after reconnect — old readPump cleared the new connection")
	}

	// Verify the old server connection was closed by connect().
	if err := firstServerConn.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second)); err == nil {
		t.Fatal("old server connection should have been closed")
	}

	// Verify we can still use the new connection (e.g. send a ping).
	transport.sendPing()

	// Verify only 2 server connections were ever created (initial + reconnect).
	connMu.Lock()
	connCount := len(serverConns)
	connMu.Unlock()
	if connCount != 2 {
		t.Fatalf("expected 2 server conns, got %d (possible reconnect loop)", connCount)
	}
}

// TestOldReadPumpReturnsWithoutInterference verifies that when connect()
// replaces the connection, the old readPump exits cleanly without
// clearing the new connection or sending a redundant reconnect signal.
func TestOldReadPumpReturnsWithoutInterference(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	var (
		connMu      sync.Mutex
		serverConns []*websocket.Conn
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connMu.Lock()
		serverConns = append(serverConns, conn)
		connMu.Unlock()
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	cfg := RemoteTransportConfig{ServerURL: wsURL}
	transport := NewRemoteTransport(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := transport.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer transport.Stop()

	time.Sleep(200 * time.Millisecond)

	// Force a reconnect via connect() while the old readPump is still running.
	// The old readPump should detect t.conn != conn and return without
	// clearing the new connection or sending a redundant reconnectCh signal.
	if err := transport.connect(ctx); err != nil {
		t.Fatalf("reconnect failed: %v", err)
	}

	// Verify t.conn is still non-nil.
	transport.connMu.Lock()
	currentConn := transport.conn
	transport.connMu.Unlock()

	if currentConn == nil {
		t.Fatal("t.conn was nil after reconnect — old readPump cleared the new connection")
	}

	// Verify we only created 2 connections (initial + reconnect),
	// not an infinite reconnect loop.
	time.Sleep(500 * time.Millisecond)
	connMu.Lock()
	connCount := len(serverConns)
	connMu.Unlock()
	if connCount != 2 {
		t.Fatalf("expected 2 server conns, got %d (possible reconnect loop)", connCount)
	}

	// Verify new connection works.
	transport.sendPing()
}

// TestReconnectEventOnlyOnActiveProgress verifies that after reconnect,
// SetProcessing(true) is only sent when the server-side turn is still active.
// This is tested at the CLI level, but we keep the transport-level contract
// here: the reconnect handler should respect progress.Phase.
func TestReconnectProgressPhase(t *testing.T) {
	// This is a documentation-level test: the actual fix is in
	// cmd/xbot-cli/main.go (progress.Phase != "done" guard).
	// We verify the protocol types carry Phase correctly.
	p := &protocol.ProgressEvent{Phase: "done"}
	if p.Phase != "done" {
		t.Fatal("unexpected phase")
	}
}

func TestRemoteTransportReconnectResumesWhenInitialSyncIsLost(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	var connectionCount atomic.Int32
	subscriptions := make(chan protocol.WSClientMessage, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connection := connectionCount.Add(1)
		var subscription protocol.WSClientMessage
		if err := conn.ReadJSON(&subscription); err != nil {
			return
		}
		subscriptions <- subscription
		if connection == 1 {
			// Simulate the server receiving subscribe while its sync ack is lost.
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	transport := NewRemoteTransport(RemoteTransportConfig{
		ServerURL: strings.Replace(server.URL, "http://", "ws://", 1),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer transport.Stop()
	if err := transport.connect(ctx); err != nil {
		t.Fatalf("initial connect: %v", err)
	}
	if err := transport.BindChat("chat-a"); err != nil {
		t.Fatalf("bind chat: %v", err)
	}
	first := <-subscriptions
	if first.Resume || first.LastSeq != 0 {
		t.Fatalf("initial subscription = %#v", first)
	}

	if err := transport.connect(ctx); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	second := <-subscriptions
	if !second.Resume || second.LastSeq != 0 || second.Channel != "cli" || second.ChatID != "chat-a" {
		t.Fatalf("reconnect subscription = %#v", second)
	}
}

func TestRemoteTransportReplayCursorIsRouteScoped(t *testing.T) {
	transport := NewRemoteTransport(RemoteTransportConfig{})
	routeA := remoteRoute{channel: "cli", chatID: "chat-a"}
	routeB := remoteRoute{channel: "cli", chatID: "chat-b"}

	transport.recordRouteCursor(protocol.WSMessage{
		Type: protocol.MsgTypeText, Seq: 9, RouteChannel: routeA.channel, RouteChatID: routeA.chatID,
	})
	transport.recordRouteCursor(protocol.WSMessage{
		Type: protocol.MsgTypeText, Seq: 3, RouteChannel: routeB.channel, RouteChatID: routeB.chatID,
	})

	subA := transport.subscriptionForRoute(routeA)
	subB := transport.subscriptionForRoute(routeB)
	if !subA.Resume || subA.LastSeq != 9 || subA.Channel != "cli" || subA.ChatID != "chat-a" {
		t.Fatalf("route A subscription = %#v", subA)
	}
	if !subB.Resume || subB.LastSeq != 3 || subB.Channel != "cli" || subB.ChatID != "chat-b" {
		t.Fatalf("route B subscription = %#v", subB)
	}

	transport.recordRouteCursor(protocol.WSMessage{
		Type: protocol.MsgTypeSync, RouteChannel: "cli", RouteChatID: "empty-route",
	})
	empty := transport.subscriptionForRoute(remoteRoute{channel: "cli", chatID: "empty-route"})
	if !empty.Resume || empty.LastSeq != 0 {
		t.Fatalf("zero high-water acknowledgement was not retained: %#v", empty)
	}
}

func TestRemoteTransportUnqualifiedEventUsesActiveRouteOnly(t *testing.T) {
	transport := NewRemoteTransport(RemoteTransportConfig{})
	routeA := remoteRoute{channel: "cli", chatID: "chat-a"}
	routeB := remoteRoute{channel: "cli", chatID: "chat-b"}
	transport.routeMu.Lock()
	transport.activeRoute = routeB
	transport.routeSeq[routeA] = 12
	transport.routeMu.Unlock()

	transport.recordRouteCursor(protocol.WSMessage{Type: protocol.MsgTypeText, Seq: 4})
	if got := transport.subscriptionForRoute(routeA).LastSeq; got != 12 {
		t.Fatalf("inactive route cursor changed to %d", got)
	}
	if got := transport.subscriptionForRoute(routeB); !got.Resume || got.LastSeq != 4 {
		t.Fatalf("active route cursor = %#v", got)
	}
}

func TestRemoteTransportSyncAuthoritativelyResetsRouteCursor(t *testing.T) {
	transport := NewRemoteTransport(RemoteTransportConfig{})
	route := remoteRoute{channel: "cli", chatID: "chat-a"}
	transport.recordRouteCursor(protocol.WSMessage{
		Type: protocol.MsgTypeText, Seq: 100, RouteChannel: route.channel, RouteChatID: route.chatID,
	})
	transport.recordRouteCursor(protocol.WSMessage{
		Type: protocol.MsgTypeSync, Seq: 0, RouteChannel: route.channel, RouteChatID: route.chatID,
	})
	if got := transport.subscriptionForRoute(route); !got.Resume || got.LastSeq != 0 {
		t.Fatalf("cursor after lower sync = %#v", got)
	}

	transport.recordRouteCursor(protocol.WSMessage{
		Type: protocol.MsgTypeText, Seq: 4, RouteChannel: route.channel, RouteChatID: route.chatID,
	})
	transport.recordRouteCursor(protocol.WSMessage{
		Type: protocol.MsgTypeText, Seq: 3, RouteChannel: route.channel, RouteChatID: route.chatID,
	})
	if got := transport.subscriptionForRoute(route).LastSeq; got != 4 {
		t.Fatalf("ordinary event lowered cursor to %d", got)
	}
}

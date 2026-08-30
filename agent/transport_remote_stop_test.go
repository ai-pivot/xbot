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

	"github.com/gorilla/websocket"
)

// M2: RemoteTransport.Stop() racing a concurrent connect() must not leak the
// freshly dialed connection. connect()'s dial happens OUTSIDE connMu — if
// Stop() runs while the dial is blocked, the dialed connection was published
// to t.conn with nobody left to Close it, and a ghost ReconnectEvent fired
// after Stop (triggering upper-layer RPCs on a dead transport).

func stopRaceUpgrader() websocket.Upgrader {
	return websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
}

// connectAfterStopIsRejected: after Stop() returns, a direct connect() call
// must fail without publishing a new connection.
func TestConnectAfterStopIsRejected(t *testing.T) {
	upgrader := stopRaceUpgrader()
	var connCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connCount.Add(1)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	transport := NewRemoteTransport(RemoteTransportConfig{ServerURL: wsURL})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := transport.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // wait for the first connection
	if got := connCount.Load(); got != 1 {
		t.Fatalf("expected 1 server conn after Start, got %d", got)
	}
	transport.Stop()

	// Post-Stop connect must be rejected: the transport is no longer owned
	// by anyone. Publishing a new t.conn would leak the fd and emit a ghost
	// ReconnectEvent after Stop.
	if err := transport.connect(context.Background()); err == nil {
		t.Fatal("connect after Stop must fail, got nil error (fresh connection published)")
	}
	transport.connMu.Lock()
	leaked := transport.conn
	transport.connMu.Unlock()
	if leaked != nil {
		t.Fatal("t.conn must stay nil after Stop — a published connection leaks the fd")
	}
}

// stopDuringDialDoesNotPublishGhostConnection: Stop() running WHILE connect()
// is blocked inside the dial must close the fresh connection instead of
// publishing it.
func TestStopDuringDialDoesNotPublishGhostConnection(t *testing.T) {
	upgrader := stopRaceUpgrader()
	var (
		connCount      atomic.Int32
		secondConnSeen = make(chan struct{})
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connCount.Add(1)
		if connCount.Load() == 2 {
			// Second connection: delay the upgrade handshake so the client's
			// dial is still blocked when Stop() runs.
			time.Sleep(300 * time.Millisecond)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		if connCount.Load() == 2 {
			close(secondConnSeen)
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	transport := NewRemoteTransport(RemoteTransportConfig{ServerURL: wsURL})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := transport.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // first connection established

	// connect() blocks inside the dial (server sleeps 300ms before upgrading
	// the 2nd connection). Stop() runs while the dial is in flight.
	var (
		connectErr error
		wg         sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		connectErr = transport.connect(context.Background())
	}()
	time.Sleep(150 * time.Millisecond) // connect() is now blocked in dial
	transport.Stop()
	wg.Wait()

	if connectErr == nil {
		t.Fatal("connect racing with Stop must fail, got nil error (ghost connection published)")
	}
	transport.connMu.Lock()
	leaked := transport.conn
	transport.connMu.Unlock()
	if leaked != nil {
		t.Fatal("t.conn must stay nil when Stop races the dial — the connection would leak")
	}
}

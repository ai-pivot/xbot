package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/protocol"
	"xbot/storage/sqlite"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	f, err := os.CreateTemp("", "xbot_web_test_*.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })

	db, err := sqlite.Open(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.Conn()
}

func newTestWebChannel(t *testing.T, db *sql.DB) (*WebChannel, *bus.MessageBus) {
	t.Helper()
	msgBus := bus.NewMessageBus()
	wc := NewWebChannel(WebChannelConfig{
		Host: "127.0.0.1",
		Port: 0, // random port
		DB:   db,
	}, msgBus)

	return wc, msgBus
}

func startTestServer(t *testing.T, wc *WebChannel) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", wc.handleWS)
	mux.HandleFunc("/api/auth/register", wc.handleRegister)
	mux.HandleFunc("/api/auth/login", wc.handleLogin)
	mux.HandleFunc("/api/auth/logout", wc.handleLogout)
	mux.HandleFunc("/api/history", wc.authMiddleware(wc.handleHistory))

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server
}

func makeWSConnection(t *testing.T, serverURL, cookie string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{}
	header := http.Header{}
	if cookie != "" {
		header.Set("Cookie", cookie)
	}
	// Convert http:// to ws:// for WebSocket dial
	wsURL := strings.Replace(serverURL, "http://", "ws://", 1)
	conn, resp, err := dialer.Dial(wsURL+"/ws", header)
	if err != nil {
		status := -1
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("WebSocket dial failed: %v (status: %d)", err, status)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// ---------------------------------------------------------------------------
// Auth tests
// ---------------------------------------------------------------------------

func TestRegisterAndLogin(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	// Register
	regBody := `{"username":"testuser","password":"secret123"}`
	resp, err := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(regBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var regResp authResponse
	json.NewDecoder(resp.Body).Decode(&regResp)
	if !regResp.OK || regResp.UserID == 0 {
		t.Fatal("registration failed")
	}

	// Duplicate registration
	resp2, _ := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(regBody))
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate, got %d", resp2.StatusCode)
	}

	// Login
	loginBody := `{"username":"testuser","password":"secret123"}`
	resp3, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(loginBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()

	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp3.StatusCode)
	}

	// Check cookie is set
	cookies := resp3.Cookies()
	if !slices.ContainsFunc(cookies, func(c *http.Cookie) bool { return c.Name == webSessionCookieName }) {
		t.Error("session cookie not set")
	}
	for _, c := range cookies {
		if c.Name == webSessionCookieName && !c.HttpOnly {
			t.Error("cookie should be HttpOnly")
		}
	}

	// Wrong password
	badLogin := `{"username":"testuser","password":"wrong"}`
	resp4, _ := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(badLogin))
	if resp4.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong password, got %d", resp4.StatusCode)
	}
}

func TestLogout(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	// Register + Login
	http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"u1","password":"p1"}`))
	loginResp, _ := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"u1","password":"p1"}`))

	var cookies []*http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			cookies = append(cookies, c)
		}
	}

	// Logout
	req, _ := http.NewRequest("POST", server.URL+"/api/auth/logout", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Cookie should be cleared
	for _, c := range resp.Cookies() {
		if c.Name == webSessionCookieName && c.MaxAge != -1 {
			t.Error("cookie should be cleared (MaxAge=-1)")
		}
	}
}

func TestAuthMiddleware(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	// No cookie → 401
	resp, _ := http.Get(server.URL + "/api/history")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	// Register + Login
	http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"u2","password":"p2"}`))
	loginResp, _ := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"u2","password":"p2"}`))

	// With cookie → 200 (no history, but authenticated)
	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			sessionCookie = c
		}
	}

	req, _ := http.NewRequest("GET", server.URL+"/api/history", nil)
	req.AddCookie(sessionCookie)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with valid cookie, got %d", resp2.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// WebSocket tests
// ---------------------------------------------------------------------------

func TestWebSocketAuth(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	// No cookie → 401
	dialer := websocket.Dialer{}
	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	_, resp, err := dialer.Dial(wsURL+"/ws", nil)
	if err == nil {
		t.Fatal("expected WebSocket upgrade to fail without auth")
		return
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	// Register
	regResp, err := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"u3","password":"p3"}`))
	if err != nil {
		t.Fatal(err)
	}
	regResp.Body.Close()
	if regResp.StatusCode != http.StatusCreated {
		t.Fatalf("register failed: %d", regResp.StatusCode)
	}

	// Login
	loginResp, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"u3","password":"p3"}`))
	if err != nil {
		t.Fatal(err)
	}
	loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %d", loginResp.StatusCode)
	}

	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie returned from login")
		return
	}

	conn := makeWSConnection(t, server.URL, sessionCookie.Name+"="+sessionCookie.Value)
	time.Sleep(50 * time.Millisecond)

	// Hub routes by chatID — find the connected client and subscribe it.
	// (In production, readPump subscribes when client sends its first message.)
	wc.hub.mu.RLock()
	var clientCID string
	for cid := range wc.hub.conns {
		clientCID = cid
		break
	}
	wc.hub.mu.RUnlock()
	if clientCID != "" {
		wc.hub.subscribe(clientCID, "web-1")
	}
	conn.Close()
}

func TestWebSocketChat(t *testing.T) {
	db := newTestDB(t)
	wc, msgBus := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	// Register + Login
	regResp, err := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"chatuser","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	regResp.Body.Close()

	loginResp, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"chatuser","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	loginResp.Body.Close()

	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			sessionCookie = c
		}
	}

	conn := makeWSConnection(t, server.URL, sessionCookie.Name+"="+sessionCookie.Value)

	// Send message
	if err := conn.WriteJSON(protocol.WSClientMessage{Type: "message", Content: "hello"}); err != nil {
		t.Fatal(err)
	}

	// Verify inbound message on bus
	select {
	case msg := <-msgBus.Inbound:
		if msg.Channel != "web" {
			t.Errorf("expected channel 'web', got '%s'", msg.Channel)
		}
		if msg.Content != "hello" {
			t.Errorf("expected content 'hello', got '%s'", msg.Content)
		}
		if msg.SenderName != "chatuser" {
			t.Errorf("expected sender 'chatuser', got '%s'", msg.SenderName)
		}
		if !strings.HasPrefix(msg.SenderID, "web-") {
			t.Errorf("expected senderID to start with 'web-', got '%s'", msg.SenderID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for inbound message")
	}
}

func TestSendToWebSocket(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	// Register + Login
	regResp, _ := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"recv","password":"pw"}`))
	regResp.Body.Close()
	loginResp, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"recv","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	loginResp.Body.Close()

	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			sessionCookie = c
		}
	}

	conn := makeWSConnection(t, server.URL, sessionCookie.Name+"="+sessionCookie.Value)
	time.Sleep(50 * time.Millisecond)

	// Subscribe client to chatID (in production, readPump subscribes on first message)
	wc.hub.mu.RLock()
	var clientCID string
	for cid := range wc.hub.conns {
		clientCID = cid
		break
	}
	wc.hub.mu.RUnlock()
	if clientCID != "" {
		wc.hub.subscribe(clientCID, "web-1")
	}

	// Send a message to the client
	msgID, err := wc.Send(channel.OutboundMsg{
		Channel: "web",
		ChatID:  "web-1", // matches senderID format
		Content: "Hello from agent!",
	})
	if err != nil {
		t.Fatal(err)
	}
	if msgID == "" {
		t.Error("expected non-empty message ID")
	}

	// Read from WebSocket
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}

	var wsMsg protocol.WSMessage
	json.Unmarshal(raw, &wsMsg)
	if wsMsg.Content != "Hello from agent!" {
		t.Errorf("expected 'Hello from agent!', got '%s'", wsMsg.Content)
	}
	if wsMsg.ID == "" {
		t.Error("expected non-empty WS message ID")
	}
}

// ---------------------------------------------------------------------------
// __FEISHU_CARD__ protocol adaptation test
// ---------------------------------------------------------------------------

func TestConvertFeishuCard(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantLen int // if > 0, check min length
	}{
		{
			name:  "valid card with title",
			input: `__FEISHU_CARD__:card_id:{"header":{"title":{"content":"Test Title"}},"elements":[{"tag":"markdown","content":"Some **bold** text"}]}`,
			want:  "Test Title",
		},
		{
			name:  "card with div text",
			input: `__FEISHU_CARD__:id:{"header":{"title":{"content":"Title"}},"elements":[{"tag":"div","text":"plain text"}]}`,
			want:  "Title",
		},
		{
			name:  "card with div JSON text",
			input: `__FEISHU_CARD__:id:{"header":{"title":{"content":"Title"}},"elements":[{"tag":"div","text":"{\"content\":\"extracted content\"}"}]}`,
			want:  "extracted content",
		},
		{
			name:  "empty card",
			input: `__FEISHU_CARD__:id:{}`,
			want:  "{}",
		},
		{
			name:  "invalid JSON",
			input: `__FEISHU_CARD__:id:not-json`,
			want:  "not-json",
		},
		{
			name:  "plain text (no prefix)",
			input: "Hello world",
			want:  "Hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result string
			if strings.HasPrefix(tt.input, "__FEISHU_CARD__") {
				result = channel.ConvertFeishuCard(tt.input)
			} else {
				result = tt.input
			}
			if !strings.Contains(result, tt.want) {
				t.Errorf("channel.ConvertFeishuCard() = %q, want to contain %q", result, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Ring buffer test
// ---------------------------------------------------------------------------

func TestRingBuffer(t *testing.T) {
	rb := newRingBuffer(3)

	rb.push(protocol.WSMessage{Content: "a"})
	rb.push(protocol.WSMessage{Content: "b"})
	rb.push(protocol.WSMessage{Content: "c"})

	msgs := rb.flush()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].Content != "a" || msgs[1].Content != "b" || msgs[2].Content != "c" {
		t.Error("messages not in order")
	}

	// Overflow: push more than capacity
	rb.push(protocol.WSMessage{Content: "d"})
	rb.push(protocol.WSMessage{Content: "e"})
	rb.push(protocol.WSMessage{Content: "f"})
	rb.push(protocol.WSMessage{Content: "g"}) // should evict "d"

	msgs = rb.flush()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages after overflow, got %d", len(msgs))
	}
	if msgs[0].Content != "e" {
		t.Errorf("expected first msg 'e', got '%s'", msgs[0].Content)
	}
}

// ---------------------------------------------------------------------------
// Hub test
// ---------------------------------------------------------------------------

func TestHubOfflineBuffering(t *testing.T) {
	hub := newHub()

	chatID := "web-1"
	msg := protocol.WSMessage{Content: "offline msg"}

	// Send to unsubscribed chatID → should be buffered
	ok := hub.sendToClient(chatID, msg)
	if ok {
		t.Error("expected sendToClient to return false for offline user")
	}

	// Verify buffered message count
	if hub.offline[chatID] == nil || hub.offline[chatID].count != 1 {
		t.Error("expected 1 buffered message")
	}

	// Add client and subscribe → should flush offline messages
	client := &Client{
		sendCh:       make(chan protocol.WSMessage, 10),
		done:         make(chan struct{}),
		id:           "test-client-1",
		statelessSig: make(chan struct{}, 1),
	}
	hub.addClient(client.id, client)
	hub.subscribe(client.id, chatID)

	// Wait for flush
	time.Sleep(100 * time.Millisecond)

	select {
	case m := <-client.sendCh:
		if m.Content != "offline msg" {
			t.Errorf("expected 'offline msg', got '%s'", m.Content)
		}
	default:
		t.Error("expected offline message to be flushed")
	}
}

func TestHubStatelessSlot(t *testing.T) {
	hub := newHub()
	chatID := "/home/user/test"

	client := &Client{
		sendCh:       make(chan protocol.WSMessage, 64),
		done:         make(chan struct{}),
		id:           "test-stateless-client",
		statelessSig: make(chan struct{}, 1),
	}
	hub.addClient(client.id, client)
	hub.subscribe(client.id, chatID)

	// Send multiple stream_content messages rapidly — only the latest should be stored.
	for i := 0; i < 100; i++ {
		ok := hub.sendToClient(chatID, protocol.WSMessage{
			Type: protocol.MsgTypeStreamContent,
			Progress: &protocol.ProgressEvent{
				ChatID:        "cli:/home/user/test",
				StreamContent: fmt.Sprintf("content-%d", i),
			},
		})
		if !ok {
			t.Fatalf("sendToClient returned false for message %d", i)
		}
	}

	// sendCh should NOT contain any stream_content — they go to statelessMap.
	if len(client.sendCh) != 0 {
		t.Errorf("expected sendCh to be empty, got %d items", len(client.sendCh))
	}

	// Only the latest stream_content should be in the stateless map.
	msgs := client.drainStateless()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 stateless message, got %d", len(msgs))
	}
	if msgs[0].Progress.StreamContent != "content-99" {
		t.Errorf("expected 'content-99', got '%s'", msgs[0].Progress.StreamContent)
	}

	// Signal should have been fired exactly once (cap-1 channel, excess signals dropped).
	select {
	case <-client.statelessSig:
	default:
		t.Error("expected statelessSig to have a pending signal")
	}
}

func TestHubStatelessPerSubAgent(t *testing.T) {
	hub := newHub()
	chatID := "/home/user/test"

	client := &Client{
		sendCh:       make(chan protocol.WSMessage, 64),
		done:         make(chan struct{}),
		id:           "test-multi-subagent",
		statelessSig: make(chan struct{}, 1),
	}
	hub.addClient(client.id, client)
	hub.subscribe(client.id, chatID)

	// Simulate 3 concurrent SubAgents streaming — each should keep its own latest.
	agentA := "cli:/home/user/test:Agent-keen-fox"
	agentB := "cli:/home/user/test:Agent-bold-cat"
	agentC := "cli:/home/user/test:Agent-swift-owl"
	mainSession := "cli:/home/user/test"

	// Rapid stream_content from all 3 agents + main session.
	for i := 0; i < 50; i++ {
		hub.sendToClient(chatID, protocol.WSMessage{
			Type: protocol.MsgTypeStreamContent,
			Progress: &protocol.ProgressEvent{
				ChatID:        agentA,
				StreamContent: fmt.Sprintf("agentA-%d", i),
			},
		})
		hub.sendToClient(chatID, protocol.WSMessage{
			Type: protocol.MsgTypeStreamContent,
			Progress: &protocol.ProgressEvent{
				ChatID:        agentB,
				StreamContent: fmt.Sprintf("agentB-%d", i),
			},
		})
		hub.sendToClient(chatID, protocol.WSMessage{
			Type: protocol.MsgTypeStreamContent,
			Progress: &protocol.ProgressEvent{
				ChatID:        agentC,
				StreamContent: fmt.Sprintf("agentC-%d", i),
			},
		})
		hub.sendToClient(chatID, protocol.WSMessage{
			Type: protocol.MsgTypeStreamContent,
			Progress: &protocol.ProgressEvent{
				ChatID:        mainSession,
				StreamContent: fmt.Sprintf("main-%d", i),
			},
		})
	}

	msgs := client.drainStateless()
	if len(msgs) != 4 {
		t.Fatalf("expected 4 stateless messages (one per source), got %d", len(msgs))
	}

	bySource := make(map[string]string)
	for _, m := range msgs {
		bySource[m.Progress.ChatID] = m.Progress.StreamContent
	}
	if bySource[agentA] != "agentA-49" {
		t.Errorf("agentA: expected 'agentA-49', got '%s'", bySource[agentA])
	}
	if bySource[agentB] != "agentB-49" {
		t.Errorf("agentB: expected 'agentB-49', got '%s'", bySource[agentB])
	}
	if bySource[agentC] != "agentC-49" {
		t.Errorf("agentC: expected 'agentC-49', got '%s'", bySource[agentC])
	}
	if bySource[mainSession] != "main-49" {
		t.Errorf("main: expected 'main-49', got '%s'", bySource[mainSession])
	}
}

func TestHubStatelessMixedTypes(t *testing.T) {
	hub := newHub()
	chatID := "/home/user/test"

	client := &Client{
		sendCh:       make(chan protocol.WSMessage, 64),
		done:         make(chan struct{}),
		id:           "test-mixed-client",
		statelessSig: make(chan struct{}, 1),
	}
	hub.addClient(client.id, client)
	hub.subscribe(client.id, chatID)

	// Send stream_content + progress for the same source — should keep latest of each.
	source := "cli:/home/user/test"
	hub.sendToClient(chatID, protocol.WSMessage{
		Type:     protocol.MsgTypeStreamContent,
		Progress: &protocol.ProgressEvent{ChatID: source, StreamContent: "old-stream"},
	})
	hub.sendToClient(chatID, protocol.WSMessage{
		Type:     protocol.MsgTypeProgress,
		Progress: &protocol.ProgressEvent{ChatID: source, Iteration: 1},
	})
	hub.sendToClient(chatID, protocol.WSMessage{
		Type:     protocol.MsgTypeStreamContent,
		Progress: &protocol.ProgressEvent{ChatID: source, StreamContent: "new-stream"},
	})
	hub.sendToClient(chatID, protocol.WSMessage{
		Type:     protocol.MsgTypeProgress,
		Progress: &protocol.ProgressEvent{ChatID: source, Iteration: 3},
	})

	msgs := client.drainStateless()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 stateless messages (one per type), got %d", len(msgs))
	}

	byType := make(map[string]*protocol.ProgressEvent)
	for _, m := range msgs {
		byType[m.Type] = m.Progress
	}
	if byType[protocol.MsgTypeStreamContent].StreamContent != "new-stream" {
		t.Errorf("stream_content: expected 'new-stream', got '%s'", byType[protocol.MsgTypeStreamContent].StreamContent)
	}
	if byType[protocol.MsgTypeProgress].Iteration != 3 {
		t.Errorf("progress: expected iteration 3, got %d", byType[protocol.MsgTypeProgress].Iteration)
	}
}

func TestHubStatefulStillUsesSendCh(t *testing.T) {
	hub := newHub()
	chatID := "/home/user/test"

	client := &Client{
		sendCh:       make(chan protocol.WSMessage, 2),
		done:         make(chan struct{}),
		id:           "test-stateful-client",
		statelessSig: make(chan struct{}, 1),
	}
	hub.addClient(client.id, client)
	hub.subscribe(client.id, chatID)

	// Stateful messages should still go through sendCh.
	ok := hub.sendToClient(chatID, protocol.WSMessage{Type: "text", Content: "hello"})
	if !ok {
		t.Error("expected stateful message to be sent via sendCh")
	}
	if len(client.sendCh) != 1 {
		t.Errorf("expected sendCh len 1, got %d", len(client.sendCh))
	}
	msgs := client.drainStateless()
	if len(msgs) != 0 {
		t.Errorf("expected no stateless messages, got %d", len(msgs))
	}

	// Fill sendCh with stateful messages and verify warning is still logged.
	client.sendCh <- protocol.WSMessage{Type: "text", Content: "fill1"}
	// sendCh now has 2 items (capacity 2) — next send should fail.
	ok = hub.sendToClient(chatID, protocol.WSMessage{Type: "text", Content: "overflow"})
	if ok {
		t.Error("expected stateful message to be dropped when sendCh full")
	}
}

// ---------------------------------------------------------------------------
// Concurrent test
// ---------------------------------------------------------------------------

func TestConcurrentSends(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	// Register + Login
	regResp, _ := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"concurrent","password":"pw"}`))
	regResp.Body.Close()
	loginResp, _ := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"concurrent","password":"pw"}`))
	loginResp.Body.Close()

	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie")
		return
	}

	conn := makeWSConnection(t, server.URL, sessionCookie.Name+"="+sessionCookie.Value)
	time.Sleep(50 * time.Millisecond)

	// Hub routes by chatID — find the connected client and subscribe it.
	// (In production, readPump subscribes when client sends first message.)
	wc.hub.mu.RLock()
	var clientCID string
	for cid := range wc.hub.conns {
		clientCID = cid
		break
	}
	wc.hub.mu.RUnlock()
	if clientCID == "" {
		t.Fatal("no connected clients in hub")
	}
	wc.hub.subscribe(clientCID, "web-1")

	// Send messages concurrently — must not block (non-blocking design)
	// sendCh has capacity 64, so some may be dropped for rapid sends
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Send must not block — this is the key invariant
			done := make(chan string, 1)
			go func() {
				id, _ := wc.Send(channel.OutboundMsg{
					Channel: "web",
					ChatID:  "web-1",
					Content: "concurrent msg",
				})
				done <- id
			}()
			select {
			case <-done:
				// OK — Send returned (message may or may not be delivered)
			case <-time.After(5 * time.Second):
				t.Errorf("Send blocked for message %d", idx)
			}
		}(i)
	}
	wg.Wait()

	// Read whatever messages arrived
	time.Sleep(200 * time.Millisecond)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	received := 0
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
		received++
	}

	// We expect at least some messages to be delivered (not all dropped)
	// With 100 rapid concurrent sends and 64-capacity buffer, typically ~64 are delivered
	if received == 0 {
		t.Error("expected at least some messages to be delivered")
	}
	if received > 100 {
		t.Errorf("received more messages than sent: %d", received)
	}
	t.Logf("Delivered %d/100 messages (buffer capacity: 64, non-blocking design)", received)
}

// ---------------------------------------------------------------------------
// PushPluginWidgetsPerSession Incremental Tests
// ---------------------------------------------------------------------------

func TestPushPluginWidgetsPerSession_Incremental(t *testing.T) {
	h := newHub()
	// Add a client and subscribe to chatID "chat1"
	cl := &Client{sendCh: make(chan protocol.WSMessage, 16), statelessSig: make(chan struct{}, 1)}
	h.addClient("client1", cl)
	h.subscribe("client1", "/home/user/test")

	rcli := NewRemoteCLIChannel(h)

	callIdx := 0
	renderFn := func(chatID string) map[string]string {
		callIdx++
		if callIdx == 1 {
			return map[string]string{"zone1": "contentA"}
		}
		if callIdx == 2 {
			return map[string]string{"zone1": "contentA"} // same as first
		}
		return map[string]string{"zone1": "contentB"} // different
	}

	// First push — should send (no prior state)
	rcli.PushPluginWidgetsPerSession(renderFn)
	select {
	case msg := <-cl.sendCh:
		if msg.Type != "plugin_widgets" {
			t.Errorf("first push: expected type plugin_widgets, got %s", msg.Type)
		}
	default:
		t.Error("first push should have sent a message")
	}

	// Second push — same content, should be skipped (incremental)
	rcli.PushPluginWidgetsPerSession(renderFn)
	select {
	case <-cl.sendCh:
		t.Error("second push with same content should be skipped")
	default:
		// expected
	}

	// Third push — different content, should send
	rcli.PushPluginWidgetsPerSession(renderFn)
	select {
	case msg := <-cl.sendCh:
		if msg.Type != "plugin_widgets" {
			t.Errorf("third push: expected type plugin_widgets, got %s", msg.Type)
		}
	default:
		t.Error("third push with different content should have sent a message")
	}
}

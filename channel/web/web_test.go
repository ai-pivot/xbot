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
	mux.HandleFunc("/api/settings", wc.authMiddleware(wc.handleSettings))
	mux.HandleFunc("/api/chats", wc.authMiddleware(wc.handleChats))
	mux.HandleFunc("/api/session-tree", wc.authMiddleware(wc.handleSessionTree))
	mux.HandleFunc("/api/chats/{chatID}/switch", wc.authMiddleware(wc.handleChatSwitch))

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server
}

func loginTestAdmin(t *testing.T, serverURL string) *http.Cookie {
	t.Helper()
	http.Post(serverURL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"admin","password":"pw"}`))
	loginResp, err := http.Post(serverURL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer loginResp.Body.Close()
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
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
	resp, _ := http.Get(server.URL + "/api/settings")
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

	req, _ := http.NewRequest("GET", server.URL+"/api/settings", nil)
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

func TestChatsDefaultListAggregatesWebAndCLIForAdmin(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.SetCallbacks(WebCallbacks{
		SessionTree: func(senderID string, current SessionSelector, admin bool) (SessionTreeResult, error) {
			return SessionTreeResult{Sessions: []SessionTreeNode{
				{UserChatWithPreview: UserChatWithPreview{
					ChatID: "web-chat", Channel: "web", Label: "Web Chat", LastActive: time.Now().Format(time.RFC3339),
					Children: []UserChatWithPreview{{
						ChatID: "web:web-chat/review", Channel: "agent", Label: "review", Type: "agent",
					}},
				}},
				{UserChatWithPreview: UserChatWithPreview{
					ChatID: "/repo", Channel: "cli", Label: "/repo", LastActive: time.Now().Format(time.RFC3339),
				}},
			}}, nil
		},
	})
	server := startTestServer(t, wc)

	http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"admin","password":"pw"}`))
	loginResp, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer loginResp.Body.Close()
	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie")
	}

	req, _ := http.NewRequest("GET", server.URL+"/api/chats", nil)
	req.AddCookie(sessionCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out struct {
		OK              bool                  `json:"ok"`
		Chats           []UserChatWithPreview `json:"chats"`
		Sessions        []SessionTreeNode     `json:"sessions"`
		OrphanSubAgents []UserChatWithPreview `json:"orphan_subagents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Chats) != 2 {
		t.Fatalf("expected 2 chats, got %d: %#v", len(out.Chats), out.Chats)
	}
	if out.Chats[0].Channel != "web" || out.Chats[1].Channel != "cli" {
		t.Fatalf("expected web+cli chats, got %#v", out.Chats)
	}
	if len(out.Chats[0].Children) != 1 || out.Chats[0].Children[0].ChatID != "web:web-chat/review" {
		t.Fatalf("/api/chats must preserve SubAgent children, got: %#v", out.Chats[0].Children)
	}
	if len(out.Sessions) != 2 {
		t.Fatalf("/api/chats must expose authoritative tree sessions, got %#v", out.Sessions)
	}
	if len(out.Sessions[0].Children) != 1 || out.Sessions[0].Children[0].ChatID != "web:web-chat/review" {
		t.Fatalf("/api/chats tree sessions must preserve children, got: %#v", out.Sessions[0].Children)
	}
}

func TestChatsRejectsAgentChannelList(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.SetCallbacks(WebCallbacks{
		ChatList: func(senderID, currentChatID, channelName string) ([]UserChatWithPreview, error) {
			t.Fatalf("ChatList should not be called for channel=%q", channelName)
			return nil, nil
		},
	})
	server := startTestServer(t, wc)
	sessionCookie := loginTestAdmin(t, server.URL)

	req, _ := http.NewRequest("GET", server.URL+"/api/chats?channel=agent", nil)
	req.AddCookie(sessionCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestChatsChannelListFiltersSubAgentRows(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.SetCallbacks(WebCallbacks{
		ChatList: func(senderID, currentChatID, channelName string) ([]UserChatWithPreview, error) {
			if channelName != "cli" {
				t.Fatalf("unexpected channel %q", channelName)
			}
			return []UserChatWithPreview{
				{
					ChatID:     "/repo:Agent-main",
					Channel:    "cli",
					Label:      "Agent-main",
					LastActive: time.Now().Format(time.RFC3339),
				},
				{
					ChatID:     "cli:/repo:Agent-main/review:1",
					Channel:    "cli",
					Label:      "review",
					LastActive: time.Now().Format(time.RFC3339),
				},
				{
					ChatID:     "row-id",
					Channel:    "web",
					FullKey:    "agent:cli:/repo:Agent-main/review:1/fix:2",
					Label:      "fix",
					LastActive: time.Now().Format(time.RFC3339),
				},
			}, nil
		},
	})
	server := startTestServer(t, wc)
	sessionCookie := loginTestAdmin(t, server.URL)

	req, _ := http.NewRequest("GET", server.URL+"/api/chats?channel=cli", nil)
	req.AddCookie(sessionCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out struct {
		OK    bool                  `json:"ok"`
		Chats []UserChatWithPreview `json:"chats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Chats) != 1 {
		t.Fatalf("expected only main chat row, got %#v", out.Chats)
	}
	if out.Chats[0].ChatID != "/repo:Agent-main" {
		t.Fatalf("ordinary CLI Agent-named session must remain, got %#v", out.Chats[0])
	}
}

func TestChatsCreateSetsCurrentSession(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.SetCallbacks(WebCallbacks{
		ChatCreate: func(senderID, label string) (string, error) {
			return "created-chat", nil
		},
	})
	server := startTestServer(t, wc)
	sessionCookie := loginTestAdmin(t, server.URL)

	req, _ := http.NewRequest("POST", server.URL+"/api/chats", strings.NewReader(`{"label":"created"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if current := wc.GetCurrentSession("web-1"); current.Channel != "web" || current.ChatID != "created-chat" {
		t.Fatalf("current session not updated: %#v", current)
	}
}

func TestSessionTreeReturnsChildrenForAdmin(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.SetCallbacks(WebCallbacks{
		SessionTree: func(senderID string, current SessionSelector, admin bool) (SessionTreeResult, error) {
			if !admin {
				t.Fatal("expected admin")
			}
			return SessionTreeResult{Sessions: []SessionTreeNode{{
				UserChatWithPreview: UserChatWithPreview{
					ChatID: "parent", Channel: "cli", Label: "parent", LastActive: time.Now().Format(time.RFC3339), Type: "main",
					Children: []UserChatWithPreview{{
						ChatID: "cli:parent/review:1", Channel: "agent", Label: "review: done",
						Type: "agent", Role: "review", Instance: "1", ParentChannel: "cli", ParentChatID: "parent", Historical: true,
					}},
				},
			}}}, nil
		},
	})
	server := startTestServer(t, wc)

	http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"admin","password":"pw"}`))
	loginResp, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer loginResp.Body.Close()
	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie")
	}

	req, _ := http.NewRequest("GET", server.URL+"/api/session-tree", nil)
	req.AddCookie(sessionCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out struct {
		OK              bool                  `json:"ok"`
		Sessions        []SessionTreeNode     `json:"sessions"`
		OrphanSubAgents []UserChatWithPreview `json:"orphan_subagents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sessions) != 1 || len(out.Sessions[0].Children) != 1 {
		t.Fatalf("expected one parent with one child, got %#v", out.Sessions)
	}
	if out.Sessions[0].Children[0].ParentChatID != "parent" {
		t.Fatalf("unexpected child parent: %#v", out.Sessions[0].Children[0])
	}
	if len(out.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", out.OrphanSubAgents)
	}
}

func TestIsAdminIdentityRecognizesFirstWebUser(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	if !wc.IsAdminIdentity("web-1") {
		t.Fatal("web-1 should be treated as admin identity")
	}
	if wc.IsAdminIdentity("web-2") {
		t.Fatal("web-2 should not be treated as admin identity")
	}
	if wc.IsAdminIdentity("cli_user") {
		t.Fatal("plain business sender should not be treated as web admin")
	}
}

func TestWebSocketBrowserMessageCanTargetCLISessionForAdmin(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.Exec(`INSERT INTO tenants (channel, chat_id) VALUES ('cli', '/repo')`); err != nil {
		t.Fatal(err)
	}
	wc, msgBus := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"admin2","password":"pw"}`))
	loginResp, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"admin2","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer loginResp.Body.Close()
	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie")
	}

	conn := makeWSConnection(t, server.URL, sessionCookie.Name+"="+sessionCookie.Value)
	if err := conn.WriteJSON(protocol.WSClientMessage{
		Type:    "message",
		Channel: "cli",
		ChatID:  "/repo",
		Content: "hello cli",
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case msg := <-msgBus.Inbound:
		if msg.Channel != "cli" || msg.ChatID != "/repo" || msg.Content != "hello cli" {
			t.Fatalf("unexpected inbound: %#v", msg)
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

	// Send stream_content + stream-only progress for the same source —
	// both are stateless, should keep latest of each.
	// Structured progress events (with Phase/Iteration/IterationHistory)
	// are stateful and go through sendCh, not storeStateless.
	source := "cli:/home/user/test"
	hub.sendToClient(chatID, protocol.WSMessage{
		Type:     protocol.MsgTypeStreamContent,
		Progress: &protocol.ProgressEvent{ChatID: source, StreamContent: "old-stream"},
	})
	hub.sendToClient(chatID, protocol.WSMessage{
		Type:     protocol.MsgTypeProgress,
		Progress: &protocol.ProgressEvent{ChatID: source, StreamContent: "progress-stream"},
	})
	hub.sendToClient(chatID, protocol.WSMessage{
		Type:     protocol.MsgTypeStreamContent,
		Progress: &protocol.ProgressEvent{ChatID: source, StreamContent: "new-stream"},
	})
	hub.sendToClient(chatID, protocol.WSMessage{
		Type:     protocol.MsgTypeProgress,
		Progress: &protocol.ProgressEvent{ChatID: source, StreamContent: "progress-stream-new"},
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
	if byType[protocol.MsgTypeProgress].StreamContent != "progress-stream-new" {
		t.Errorf("progress: expected 'progress-stream-new', got '%s'", byType[protocol.MsgTypeProgress].StreamContent)
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

// TestRPCNonBlocking verifies that a slow RPC does not block subsequent RPCs.
// Before the fix, readPump processed RPCs serially — a long-running
// refresh_model_entries (up to 8s per subscription) blocked all subsequent
// RPC responses. After the fix, each RPC is dispatched to a goroutine so
// readPump can continue reading messages.
func TestRPCNonBlocking(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	// Custom RPCHandler: "slow_method" sleeps 300ms, "fast_method" returns immediately.
	wc.SetRPCHandler(func(method string, params json.RawMessage, senderID string) (json.RawMessage, error) {
		switch method {
		case "slow_method":
			time.Sleep(300 * time.Millisecond)
			return json.RawMessage(`"slow_done"`), nil
		case "fast_method":
			return json.RawMessage(`"fast_done"`), nil
		default:
			return nil, fmt.Errorf("unknown method: %s", method)
		}
	})

	// Register + Login
	regResp, _ := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"rpcuser","password":"pw"}`))
	regResp.Body.Close()
	loginResp, _ := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"rpcuser","password":"pw"}`))
	loginResp.Body.Close()

	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie")
	}

	conn := makeWSConnection(t, server.URL, sessionCookie.Name+"="+sessionCookie.Value)
	defer conn.Close()
	time.Sleep(50 * time.Millisecond) // let readPump start

	// Send slow RPC first, then fast RPC immediately.
	slowReq, _ := json.Marshal(protocol.WSClientMessage{Type: protocol.MsgTypeRPC, ID: "rpc-slow", Method: "slow_method"})
	fastReq, _ := json.Marshal(protocol.WSClientMessage{Type: protocol.MsgTypeRPC, ID: "rpc-fast", Method: "fast_method"})

	if err := conn.WriteMessage(websocket.TextMessage, slowReq); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, fastReq); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	// The fast RPC response MUST arrive before the slow RPC response.
	// If readPump is still serial (pre-fix), the fast response is delayed
	// by at least 300ms (the slow RPC's sleep).
	var firstResp protocol.WSMessage
	if err := conn.ReadJSON(&firstResp); err != nil {
		t.Fatalf("read first response: %v", err)
	}

	if firstResp.ID != "rpc-fast" {
		t.Errorf("expected fast RPC response first, got ID=%s (slow RPC should still be sleeping)", firstResp.ID)
	}

	var secondResp protocol.WSMessage
	if err := conn.ReadJSON(&secondResp); err != nil {
		t.Fatalf("read second response: %v", err)
	}
	if secondResp.ID != "rpc-slow" {
		t.Errorf("expected slow RPC response second, got ID=%s", secondResp.ID)
	}
}

func TestShouldEagerSaveUserMessageSkipsCommands(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		content string
		want    bool
	}{
		{name: "web normal", channel: "web", content: "hello", want: true},
		{name: "web empty", channel: "web", content: "", want: true},
		{name: "cli normal", channel: "cli", content: "hello", want: false},
		{name: "bang command", channel: "web", content: "!pwd", want: false},
		{name: "slash new", channel: "web", content: "/new", want: false},
		{name: "slash rewind", channel: "web", content: "/rewind", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldEagerSaveUserMessage(tc.channel, tc.content); got != tc.want {
				t.Fatalf("shouldEagerSaveUserMessage(%q, %q) = %v, want %v", tc.channel, tc.content, got, tc.want)
			}
		})
	}
}

// REGRESSION: Structured progress events must not be overwritten by storeStateless.

func TestRegression_StructuredProgressIsStateful(t *testing.T) {
	tests := []struct {
		name string
		msg  protocol.WSMessage
		want bool
	}{
		{name: "phase=thinking, iter=0", msg: protocol.WSMessage{Type: protocol.MsgTypeProgress, Progress: &protocol.ProgressEvent{Phase: "thinking", Iteration: 0}}, want: true},
		{name: "phase=done, iter=1", msg: protocol.WSMessage{Type: protocol.MsgTypeProgress, Progress: &protocol.ProgressEvent{Phase: "done", Iteration: 1}}, want: true},
		{name: "iteration_history present", msg: protocol.WSMessage{Type: protocol.MsgTypeProgress, Progress: &protocol.ProgressEvent{IterationHistory: []protocol.ProgressEvent{{Iteration: 0}}}}, want: true},
		{name: "history_compacted=true", msg: protocol.WSMessage{Type: protocol.MsgTypeProgress, Progress: &protocol.ProgressEvent{HistoryCompacted: true}}, want: true},
		{name: "stream-only (stateless)", msg: protocol.WSMessage{Type: protocol.MsgTypeProgress, Progress: &protocol.ProgressEvent{StreamContent: "streaming text"}}, want: false},
		{name: "MsgTypeStreamContent (stateless)", msg: protocol.WSMessage{Type: protocol.MsgTypeStreamContent, Progress: &protocol.ProgressEvent{StreamContent: "text"}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isStatefulMsg(tt.msg)
			if got != tt.want {
				t.Errorf("isStatefulMsg() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRegression_StructuredProgressNotOverwrittenInStateless(t *testing.T) {
	hub := newHub()
	chatID := "/test"
	source := "cli:/test"
	client := &Client{
		sendCh:       make(chan protocol.WSMessage, 64),
		done:         make(chan struct{}),
		id:           "test-structured-progress",
		statelessSig: make(chan struct{}, 1),
	}
	hub.addClient(client.id, client)
	hub.subscribe(client.id, chatID)
	hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeProgress, Progress: &protocol.ProgressEvent{ChatID: source, Phase: "tool_exec", Iteration: 0}})
	delta := protocol.ProgressEvent{Iteration: 0, Content: "iter0-content"}
	hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeProgress, Progress: &protocol.ProgressEvent{ChatID: source, Phase: "thinking", Iteration: 1, IterationHistory: []protocol.ProgressEvent{delta}}})
	hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeProgress, Progress: &protocol.ProgressEvent{ChatID: source, Phase: "done", Iteration: 1}})
	msgs := client.drainStateless()
	if len(msgs) != 0 {
		t.Fatalf("expected 0 stateless messages, got %d", len(msgs))
	}
	if len(client.sendCh) != 3 {
		t.Fatalf("expected 3 messages in sendCh, got %d", len(client.sendCh))
	}
	for i := 0; i < 3; i++ {
		msg := <-client.sendCh
		if msg.Type != protocol.MsgTypeProgress {
			t.Errorf("sendCh[%d]: expected progress, got %s", i, msg.Type)
		}
	}
}

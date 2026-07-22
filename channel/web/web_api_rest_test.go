package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xbot/bus"
	ch "xbot/channel"
	"xbot/protocol"
	"xbot/tools"
)

type fixedIdentityResolver struct {
	IdentityResolverAPI
	userID int64
	role   string
}

type fixedOSSProvider struct{}

func (fixedOSSProvider) Upload(string, []byte) error { return nil }
func (fixedOSSProvider) GetDownloadURL(string) (string, error) {
	return "https://files.example/test.txt", nil
}
func (fixedOSSProvider) Name() string   { return "fixed" }
func (fixedOSSProvider) Domain() string { return "https://files.example" }

func (r fixedIdentityResolver) Resolve(channel, channelUserID string) (int64, string, error) {
	return r.userID, r.role, nil
}

func authedAPIRequest(method, target string, body []byte) *http.Request {
	return authedAPIRequestFor(method, target, body, "web-1", 1)
}

func authedAPIRequestFor(method, target string, body []byte, senderID string, userID int) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	ctx := contextWithSenderID(contextWithUserID(req.Context(), userID), senderID)
	ctx = context.WithValue(ctx, webSessionKey, sessionInfo{userID: userID, username: "tester"})
	return req.WithContext(ctx)
}

func decodeAPIResponse(t *testing.T, rec *httptest.ResponseRecorder) (testAPIEnvelope, map[string]any) {
	t.Helper()
	var data map[string]any
	envelope := decodeAPIData(t, rec.Body, &data)
	return envelope, data
}

func setTestCurrentSession(wc *WebChannel, sel SessionSelector) {
	setTestCurrentSessionFor(wc, "web-1", sel)
}

func setTestCurrentSessionFor(wc *WebChannel, senderID string, sel SessionSelector) {
	wc.userCurrentSessionMu.Lock()
	defer wc.userCurrentSessionMu.Unlock()
	wc.userCurrentSession[senderID] = sel
}

func TestRESTResponseEnvelope(t *testing.T) {
	success := httptest.NewRecorder()
	writeJSON(success, http.StatusOK, map[string]any{"value": "ok", "ok": true})
	var successRaw map[string]any
	if err := json.NewDecoder(success.Body).Decode(&successRaw); err != nil {
		t.Fatal(err)
	}
	if successRaw["ok"] != true || successRaw["error"] != nil {
		t.Fatalf("unexpected success envelope: %#v", successRaw)
	}
	data, ok := successRaw["data"].(map[string]any)
	if !ok || data["value"] != "ok" {
		t.Fatalf("unexpected success data: %#v", successRaw["data"])
	}
	if _, nested := data["ok"]; nested {
		t.Fatalf("legacy ok field leaked into data: %#v", data)
	}

	failure := httptest.NewRecorder()
	jsonErrorResponse(failure, http.StatusNotFound, "missing")
	var failureRaw map[string]any
	if err := json.NewDecoder(failure.Body).Decode(&failureRaw); err != nil {
		t.Fatal(err)
	}
	if failureRaw["ok"] != false || failureRaw["data"] != nil {
		t.Fatalf("unexpected error envelope: %#v", failureRaw)
	}
	errorBody := failureRaw["error"].(map[string]any)
	if errorBody["code"] != "not_found" || errorBody["message"] != "missing" {
		t.Fatalf("unexpected error object: %#v", errorBody)
	}
}

func TestProductionRoutesUseWebPOSTContract(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	mux := wc.newServeMux()

	for _, path := range []string{
		"/api/auth/config",
		"/api/message",
		"/api/cancel",
		"/api/ask_user/respond",
		"/api/rpc",
		"/api/history",
		"/api/history/rewind",
		"/api/search",
		"/api/settings",
		"/api/llm-config",
		"/api/session/status",
		"/api/runners/list",
		"/api/runners/create",
		"/api/runners/active",
		"/api/runners/runner-a/delete",
		"/api/files/upload",
		"/api/fs/list",
		"/api/fs/read",
		"/api/fs/search",
		"/api/chats/list",
		"/api/chats/create",
		"/api/chats/chat-a/switch",
		"/api/chats/chat-a/rename",
		"/api/chats/chat-a/delete",
		"/api/session-tree",
		"/api/account/link-code",
		"/api/account/link",
		"/api/account/identities/list",
		"/api/account/identities/1/delete",
		"/api/admin/users/list",
		"/api/admin/users/1/set-role",
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s status = %d, want 405", path, recorder.Code)
		}
	}

	for _, path := range []string{
		"/api/cwd",
		"/api/tasks",
		"/api/background-tasks",
		"/api/commands",
		"/api/session-subscription",
		"/api/runner/token",
		"/api/runners",
		"/api/chats",
		"/api/subagents",
		"/api/context-info",
		"/api/channels",
		"/api/account/identities",
		"/api/admin/users",
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("POST %s status = %d, want 404", path, recorder.Code)
		}
	}
}

func TestProductionSessionTreeAcceptsAuthenticatedPOST(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := httptest.NewServer(wc.newServeMux())
	t.Cleanup(server.Close)
	cookie := loginTestAdmin(t, server.URL)

	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/session-tree", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/session-tree status = %d, want 200", response.StatusCode)
	}
	var data struct {
		Sessions []any `json:"sessions"`
	}
	envelope := decodeAPIData(t, response.Body, &data)
	if !envelope.OK || data.Sessions == nil {
		t.Fatalf("unexpected session tree response: ok=%v sessions=%#v", envelope.OK, data.Sessions)
	}
}

func TestSessionTreeAdminFlagHonorsSingleUserMode(t *testing.T) {
	for _, tc := range []struct {
		name       string
		singleUser bool
		wantAdmin  bool
	}{
		{name: "single user", singleUser: true, wantAdmin: true},
		{name: "multi user", singleUser: false, wantAdmin: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			wc, _ := newTestWebChannel(t, db)
			wc.singleUser = tc.singleUser
			wc.SetCallbacks(WebCallbacks{
				SessionTree: func(_ string, _ SessionSelector, admin bool) (SessionTreeResult, error) {
					if admin != tc.wantAdmin {
						t.Fatalf("admin = %v, want %v", admin, tc.wantAdmin)
					}
					return SessionTreeResult{}, nil
				},
			})

			recorder := httptest.NewRecorder()
			request := authedAPIRequestFor(http.MethodGet, "/api/session-tree", nil, "web-2", 2)
			wc.handleSessionTree(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
		})
	}
}

func TestRESTChatCRUDPassesChannelToCallbacks(t *testing.T) {
	db := newTestDB(t)
	wc := NewWebChannel(WebChannelConfig{DB: db}, bus.NewMessageBus())
	cliStream := wc.getEventStream(sessionRouteKey("cli", "shared"))
	webStream := wc.getEventStream(sessionRouteKey("web", "shared"))
	cliStream.nextSeq()
	webStream.nextSeq()
	requestKey := inboundRequestKey{senderID: "web-1", channel: "cli", chatID: "shared", requestID: "request-1"}
	wc.inboundRequests[requestKey] = &inboundRequestState{}
	if _, err := db.Exec(
		"INSERT INTO tenants (channel, chat_id, last_active_at) VALUES (?, ?, ?)",
		"cli", "shared", time.Now().Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}
	var renamed, deleted SessionSelector
	wc.SetCallbacks(WebCallbacks{
		ChatRename: func(senderID, channel, chatID, label string) error {
			if senderID != "web-1" || label != "renamed" {
				t.Fatalf("rename callback args = (%q, %q)", senderID, label)
			}
			renamed = SessionSelector{Channel: channel, ChatID: chatID}
			return nil
		},
		ChatDelete: func(senderID, channel, chatID string) error {
			if senderID != "web-1" {
				t.Fatalf("delete sender = %q", senderID)
			}
			deleted = SessionSelector{Channel: channel, ChatID: chatID}
			return nil
		},
	})

	rename := authedAPIRequest(http.MethodPost, "/api/chats/shared/rename", []byte(`{"channel":"cli","label":"renamed"}`))
	rename.SetPathValue("chatID", "shared")
	renameRecorder := httptest.NewRecorder()
	wc.handleChatRename(renameRecorder, rename)
	if renameRecorder.Code != http.StatusOK {
		t.Fatalf("rename status = %d: %s", renameRecorder.Code, renameRecorder.Body.String())
	}
	if renamed != (SessionSelector{Channel: "cli", ChatID: "shared"}) {
		t.Fatalf("rename selector = %#v", renamed)
	}

	deleteRequest := authedAPIRequest(http.MethodDelete, "/api/chats/shared?channel=cli", nil)
	deleteRequest.SetPathValue("chatID", "shared")
	deleteRecorder := httptest.NewRecorder()
	wc.handleChatDelete(deleteRecorder, deleteRequest)
	if deleteRecorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d: %s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	if deleted != (SessionSelector{Channel: "cli", ChatID: "shared"}) {
		t.Fatalf("delete selector = %#v", deleted)
	}
	if fresh := wc.getEventStream(sessionRouteKey("cli", "shared")); fresh == cliStream || fresh.lastSeq() != 0 {
		t.Fatalf("deleted CLI replay stream was retained: same=%v seq=%d", fresh == cliStream, fresh.lastSeq())
	}
	if wc.getEventStream(sessionRouteKey("web", "shared")) != webStream || webStream.lastSeq() != 1 {
		t.Fatal("deleting CLI session changed same-ID Web replay state")
	}
	if _, ok := wc.inboundRequests[requestKey]; ok {
		t.Fatal("deleted CLI request-dedup state was retained")
	}
}

func TestRESTChatDeleteAllowsAdminVerifiedLocalCLISession(t *testing.T) {
	db := newTestDB(t)
	wc := NewWebChannel(WebChannelConfig{DB: db}, bus.NewMessageBus())
	deleted := false
	wc.SetCallbacks(WebCallbacks{
		LocalSessionExists: func(channel, chatID string) bool {
			return channel == "cli" && chatID == "/repo/project:local-only"
		},
		ChatDelete: func(senderID, channel, chatID string) error {
			deleted = senderID == "web-1" && channel == "cli" && chatID == "/repo/project:local-only"
			return nil
		},
	})
	foreignSwitch := authedAPIRequestFor(
		http.MethodPost,
		"/api/chats/local/switch?channel=cli",
		nil,
		"web-2",
		2,
	)
	foreignSwitch.SetPathValue("chatID", "/repo/project:local-only")
	foreignSwitchRecorder := httptest.NewRecorder()
	wc.handleChatSwitch(foreignSwitchRecorder, foreignSwitch)
	if foreignSwitchRecorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin local CLI switch status=%d", foreignSwitchRecorder.Code)
	}

	adminSwitch := authedAPIRequest(http.MethodPost, "/api/chats/local/switch?channel=cli", nil)
	adminSwitch.SetPathValue("chatID", "/repo/project:local-only")
	adminSwitchRecorder := httptest.NewRecorder()
	wc.handleChatSwitch(adminSwitchRecorder, adminSwitch)
	if adminSwitchRecorder.Code != http.StatusOK {
		t.Fatalf("admin local CLI switch status=%d body=%s", adminSwitchRecorder.Code, adminSwitchRecorder.Body.String())
	}
	if got := wc.GetCurrentSession("web-1"); got != (SessionSelector{Channel: "cli", ChatID: "/repo/project:local-only"}) {
		t.Fatalf("admin local CLI selector=%#v", got)
	}

	foreignRequest := authedAPIRequestFor(
		http.MethodDelete,
		"/api/chats/local?channel=cli",
		nil,
		"web-2",
		2,
	)
	foreignRequest.SetPathValue("chatID", "/repo/project:local-only")
	foreignRecorder := httptest.NewRecorder()
	wc.handleChatDelete(foreignRecorder, foreignRequest)
	if foreignRecorder.Code != http.StatusForbidden || deleted {
		t.Fatalf("non-admin local CLI delete status=%d deleted=%v", foreignRecorder.Code, deleted)
	}

	request := authedAPIRequest(http.MethodDelete, "/api/chats/local?channel=cli", nil)
	request.SetPathValue("chatID", "/repo/project:local-only")
	recorder := httptest.NewRecorder()
	wc.handleChatDelete(recorder, request)

	if recorder.Code != http.StatusOK || !deleted {
		t.Fatalf("local CLI delete status=%d deleted=%v body=%s", recorder.Code, deleted, recorder.Body.String())
	}
}

func TestRESTMessageCancelAndAskUserReuseInboundPath(t *testing.T) {
	db := newTestDB(t)
	msgBus := bus.NewMessageBus()
	wc := NewWebChannel(WebChannelConfig{DB: db}, msgBus)
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	if _, err := db.Exec("INSERT INTO tenants (channel, chat_id, last_active_at) VALUES (?, ?, ?)", "web", "web-1", time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	wc.handleMessage(recorder, authedAPIRequest(http.MethodPost, "/api/message", []byte(`{"content":"hello"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("message status = %d: %s", recorder.Code, recorder.Body.String())
	}
	message := <-msgBus.Inbound
	if message.Channel != "web" || message.ChatID != "web-1" || message.Content != "hello" {
		t.Fatalf("unexpected message inbound: %#v", message)
	}
	if message.Metadata[bus.MetadataReplyPolicy] != bus.ReplyPolicyOptional {
		t.Fatalf("missing reply policy metadata: %#v", message.Metadata)
	}

	recorder = httptest.NewRecorder()
	wc.handleCancel(recorder, authedAPIRequest(http.MethodPost, "/api/cancel", []byte(`{"chat_id":"web-1"}`)))
	cancel := <-msgBus.Inbound
	if recorder.Code != http.StatusOK || cancel.Content != "/cancel" || cancel.ChatID != "web-1" {
		t.Fatalf("unexpected cancel result: status=%d message=%#v", recorder.Code, cancel)
	}

	recorder = httptest.NewRecorder()
	wc.handleAskUserRespond(recorder, authedAPIRequest(http.MethodPost, "/api/ask_user/respond", []byte(`{"chat_id":"web-1","question_id":"q1","answer":"yes"}`)))
	answer := <-msgBus.Inbound
	if recorder.Code != http.StatusOK || answer.Content != "Qq1: yes" || answer.Metadata["ask_user_answered"] != "true" {
		t.Fatalf("unexpected AskUser result: status=%d message=%#v", recorder.Code, answer)
	}
}

func TestRESTMessageRetriesAreIdempotent(t *testing.T) {
	db := newTestDB(t)
	msgBus := bus.NewMessageBus()
	wc := NewWebChannel(WebChannelConfig{DB: db}, msgBus)
	wc.SetOSSProvider(fixedOSSProvider{})
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	client := &Client{
		connType: clientConnTypeSSE,
		sendCh:   make(chan protocol.WSMessage, 4),
		done:     make(chan struct{}),
		id:       "retry-client",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, "web-1")

	for _, tc := range []struct {
		name      string
		requestID string
		content   string
	}{
		{name: "message", requestID: "request-message", content: "hello"},
		{name: "command", requestID: "request-command", content: "/new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"id":          tc.requestID,
				"content":     tc.content,
				"upload_keys": []string{"upload-key"},
				"file_names":  []string{"test.txt"},
			})
			if err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				recorder := httptest.NewRecorder()
				wc.handleMessage(recorder, authedAPIRequest(http.MethodPost, "/api/message", body))
				if recorder.Code != http.StatusOK {
					t.Fatalf("attempt %d status = %d: %s", attempt+1, recorder.Code, recorder.Body.String())
				}
			}

			inbound := <-msgBus.Inbound
			if inbound.RequestID != tc.requestID || !strings.Contains(inbound.Content, "<file") {
				t.Fatalf("inbound = %#v", inbound)
			}
			select {
			case duplicate := <-msgBus.Inbound:
				t.Fatalf("duplicate inbound: %#v", duplicate)
			default:
			}
			echo := <-client.sendCh
			if echo.Type != protocol.MsgTypeUserEcho || echo.ID != tc.requestID || echo.OriginalContent != tc.content {
				t.Fatalf("echo = %#v", echo)
			}
			select {
			case duplicate := <-client.sendCh:
				t.Fatalf("duplicate echo: %#v", duplicate)
			default:
			}
		})
	}
}

func TestRESTMessageEnqueueFailureLeavesNoEchoOrHistory(t *testing.T) {
	db := newTestDB(t)
	wc := NewWebChannel(WebChannelConfig{DB: db}, nil)
	wc.SetOSSProvider(fixedOSSProvider{})
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	client := &Client{
		connType: clientConnTypeSSE,
		sendCh:   make(chan protocol.WSMessage, 1),
		done:     make(chan struct{}),
		id:       "failed-client",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, "web-1")
	body := []byte(`{"id":"failed-request","content":"/new","upload_keys":["upload-key"],"file_names":["test.txt"]}`)

	for attempt := 0; attempt < 2; attempt++ {
		recorder := httptest.NewRecorder()
		wc.handleMessage(recorder, authedAPIRequest(http.MethodPost, "/api/message", body))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("attempt %d status = %d: %s", attempt+1, recorder.Code, recorder.Body.String())
		}
	}
	select {
	case echo := <-client.sendCh:
		t.Fatalf("unexpected echo after failed enqueue: %#v", echo)
	default:
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM session_messages").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("session_messages count = %d, want 0", count)
	}
}

func TestRESTMessageCommitsIdempotencyOnlyAfterAgentAdmission(t *testing.T) {
	msgBus := bus.NewMessageBus()
	msgBus.EnableDeliveryAcknowledgement()
	wc := NewWebChannel(WebChannelConfig{}, msgBus)
	wc.SetOSSProvider(fixedOSSProvider{})
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	client := &Client{
		connType: clientConnTypeSSE,
		sendCh:   make(chan protocol.WSMessage, 1),
		done:     make(chan struct{}),
		id:       "admission-client",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, "web-1")
	body := []byte(`{"id":"admission-request","content":"hello","upload_keys":["upload-key"],"file_names":["test.txt"]}`)

	go func() {
		message := <-msgBus.Inbound
		message.DeliveryAck <- bus.ErrInboundQueueFull
	}()
	failed := httptest.NewRecorder()
	wc.handleMessage(failed, authedAPIRequest(http.MethodPost, "/api/message", body))
	if failed.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed admission status = %d: %s", failed.Code, failed.Body.String())
	}
	select {
	case echo := <-client.sendCh:
		t.Fatalf("failed admission emitted echo: %#v", echo)
	default:
	}

	go func() {
		message := <-msgBus.Inbound
		message.DeliveryAck <- nil
	}()
	accepted := httptest.NewRecorder()
	wc.handleMessage(accepted, authedAPIRequest(http.MethodPost, "/api/message", body))
	if accepted.Code != http.StatusOK {
		t.Fatalf("accepted admission status = %d: %s", accepted.Code, accepted.Body.String())
	}
	echo := <-client.sendCh
	if echo.ID != "admission-request" || echo.Type != protocol.MsgTypeUserEcho {
		t.Fatalf("accepted admission echo = %#v", echo)
	}
}

func TestRESTMessageCancellationAfterHandoffPreservesIdempotency(t *testing.T) {
	msgBus := bus.NewMessageBus()
	msgBus.EnableDeliveryAcknowledgement()
	wc := NewWebChannel(WebChannelConfig{}, msgBus)
	wc.SetOSSProvider(fixedOSSProvider{})
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	client := &Client{
		connType:       clientConnTypeSSE,
		sendCh:         make(chan protocol.WSMessage, 1),
		done:           make(chan struct{}),
		id:             "cancelled-request-client",
		sessionChannel: "web",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, sessionRouteKey("web", "web-1"))
	message := protocol.WSClientMessage{
		ID:         "cancelled-request",
		Type:       protocol.MsgTypeMessage,
		Content:    "hello",
		UploadKeys: []string{"upload-key"},
		FileNames:  []string{"test.txt"},
	}
	identity := inboundIdentity{SenderID: "web-1", SenderName: "tester", WebUserID: 1}
	ctx, cancel := context.WithCancel(context.Background())
	type dispatchResult struct {
		sel SessionSelector
		err error
	}
	resultCh := make(chan dispatchResult, 1)
	go func() {
		sel, err := wc.dispatchUserMessage(ctx, identity, message)
		resultCh <- dispatchResult{sel: sel, err: err}
	}()

	inbound := <-msgBus.Inbound
	cancel()
	inbound.DeliveryAck <- nil
	result := <-resultCh
	if result.err != nil || result.sel.ChatID != "web-1" {
		t.Fatalf("dispatch after handoff cancellation = (%#v, %v)", result.sel, result.err)
	}
	if _, err := wc.dispatchUserMessage(context.Background(), identity, message); err != nil {
		t.Fatalf("same-ID retry after cancelled response: %v", err)
	}
	select {
	case duplicate := <-msgBus.Inbound:
		t.Fatalf("same-ID retry re-enqueued: %#v", duplicate)
	default:
	}
	echo := <-client.sendCh
	if echo.ID != message.ID {
		t.Fatalf("echo ID = %q, want %q", echo.ID, message.ID)
	}
	select {
	case duplicate := <-client.sendCh:
		t.Fatalf("same-ID retry emitted duplicate echo: %#v", duplicate)
	default:
	}
}

func TestRESTRPCDispatchesThroughCallback(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	wc.SetRPCHandler(func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error) {
		if method != "get_settings" || identity.SenderID != "web-2" || string(params) != `{"namespace":"web"}` {
			t.Fatalf("unexpected RPC dispatch: method=%q sender=%q params=%s", method, identity.SenderID, params)
		}
		return json.RawMessage(`{"theme":"dark"}`), nil
	})
	recorder := httptest.NewRecorder()
	wc.handleRPC(recorder, authedAPIRequestFor(http.MethodPost, "/api/rpc", []byte(`{"method":"get_settings","params":{"namespace":"web"}}`), "web-2", 2))
	envelope, data := decodeAPIResponse(t, recorder)
	if recorder.Code != http.StatusOK || !envelope.OK || data["theme"] != "dark" {
		t.Fatalf("unexpected RPC response: %d %#v %#v", recorder.Code, envelope, data)
	}
}

func TestRESTRPCAllowsFrontendRecoveryMethods(t *testing.T) {
	methods := []string{"list_command_names", "set_cwd"}
	for _, wantMethod := range methods {
		t.Run(wantMethod, func(t *testing.T) {
			wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
			wc.SetRPCHandler(func(method string, _ json.RawMessage, _ RPCIdentity) (json.RawMessage, error) {
				if method != wantMethod {
					t.Fatalf("method = %q, want %q", method, wantMethod)
				}
				return json.RawMessage(`{}`), nil
			})
			body := []byte(`{"method":"` + wantMethod + `","params":{}}`)
			recorder := httptest.NewRecorder()
			wc.handleRPC(recorder, authedAPIRequestFor(http.MethodPost, "/api/rpc", body, "web-2", 2))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestRESTRPCAllowsModelManagementMethodsForNonAdmin(t *testing.T) {
	// These methods were previously rejected for non-admin users, causing
	// model configuration to be broken in the web UI. They are now allowed.
	methods := []struct {
		method string
		params string
	}{
		{method: "select_model", params: `{"sub_id":"sub-1","model":"gpt-4","channel":"web","chat_id":"web-2"}`},
		{method: "set_model_enabled", params: `{"sub_id":"sub-1","model":"gpt-4","enabled":true}`},
		{method: "remove_model", params: `{"sub_id":"sub-1","model":"gpt-4"}`},
		{method: "upsert_model", params: `{"sub_id":"sub-1","model":"gpt-4","max_context":0,"max_output":0,"api_type":""}`},
		{method: "set_subscription_enabled", params: `{"sub_id":"sub-1","enabled":true}`},
		{method: "update_subscription", params: `{"id":"sub-1","sub":{"name":"test","provider":"openai","base_url":"","api_key":"","model":""}}`},
		{method: "remove_subscription", params: `{"id":"sub-1"}`},
		{method: "rename_subscription", params: `{"id":"sub-1","name":"test"}`},
		{method: "set_default_subscription", params: `{"id":"sub-1"}`},
		{method: "update_per_model_config", params: `{"id":"sub-1","model":"gpt-4","config":{"max_output_tokens":0,"max_context":0,"api_type":"","enabled":true}}`},
		{method: "set_default_model", params: `{"sub_id":"sub-1","model":"gpt-4"}`},
	}
	for _, tt := range methods {
		t.Run(tt.method, func(t *testing.T) {
			wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
			dispatched := false
			wc.SetRPCHandler(func(method string, _ json.RawMessage, _ RPCIdentity) (json.RawMessage, error) {
				if method != tt.method {
					t.Fatalf("method = %q, want %q", method, tt.method)
				}
				dispatched = true
				return json.RawMessage(`{}`), nil
			})
			body := []byte(`{"method":"` + tt.method + `","params":` + tt.params + `}`)
			recorder := httptest.NewRecorder()
			wc.handleRPC(recorder, authedAPIRequestFor(http.MethodPost, "/api/rpc", body, "web-2", 2))
			if recorder.Code != http.StatusOK || !dispatched {
				t.Fatalf("status=%d dispatched=%v body=%s", recorder.Code, dispatched, recorder.Body.String())
			}
		})
	}
}

func TestRESTRPCGetActiveProgressChecksAgentOwnership(t *testing.T) {
	db := newTestDB(t)
	for _, chat := range []struct {
		senderID string
		chatID   string
	}{
		{senderID: "web-2", chatID: "owned-chat"},
		{senderID: "web-3", chatID: "foreign-chat"},
	} {
		if _, err := db.Exec(
			"INSERT INTO user_chats (channel, sender_id, chat_id, label) VALUES (?, ?, ?, ?)",
			"web", chat.senderID, chat.chatID, chat.chatID,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(
			"INSERT INTO tenants (channel, chat_id, last_active_at) VALUES (?, ?, ?)",
			"agent", "web:"+chat.chatID+"/review:1", time.Now().Format(time.RFC3339),
		); err != nil {
			t.Fatal(err)
		}
	}

	dispatched := 0
	wc := NewWebChannel(WebChannelConfig{DB: db}, bus.NewMessageBus())
	wc.SetRPCHandler(func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error) {
		dispatched++
		return json.RawMessage(`{"phase":"tool"}`), nil
	})

	owned := httptest.NewRecorder()
	wc.handleRPC(owned, authedAPIRequestFor(http.MethodPost, "/api/rpc", []byte(`{"method":"get_active_progress","params":{"channel":"agent","chat_id":"web:owned-chat/review:1"}}`), "web-2", 2))
	if owned.Code != http.StatusOK || dispatched != 1 {
		t.Fatalf("owned status=%d dispatched=%d body=%s", owned.Code, dispatched, owned.Body.String())
	}

	foreign := httptest.NewRecorder()
	wc.handleRPC(foreign, authedAPIRequestFor(http.MethodPost, "/api/rpc", []byte(`{"method":"get_active_progress","params":{"channel":"agent","chat_id":"web:foreign-chat/review:1"}}`), "web-2", 2))
	if foreign.Code != http.StatusForbidden || dispatched != 1 {
		t.Fatalf("foreign status=%d dispatched=%d body=%s", foreign.Code, dispatched, foreign.Body.String())
	}
}

func TestRESTRPCRejectsUnsafeNonAdminMethods(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	dispatched := false
	wc.SetRPCHandler(func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error) {
		dispatched = true
		return json.RawMessage(`{}`), nil
	})

	tests := []struct {
		method string
		params string
	}{
		{method: "send_inbound", params: `{"channel":"web","chat_id":"web-1","sender_id":"web-1","content":"injected"}`},
		{method: "plugin_reload", params: `{"id":"plugin-a"}`},
		{method: "plugin_reload_all", params: `{}`},
		{method: "plugin_install", params: `{"source_dir":"/tmp/plugin"}`},
		{method: "plugin_uninstall", params: `{"id":"plugin-a"}`},
		{method: "get_token_state", params: `{"channel":"cli","chat_id":"/foreign"}`},
		{method: "get_history", params: `{"channel":"agent","chat_id":"web:web-1/private:1"}`},
		{method: "plugin_widgets", params: `{"chat_id":"/another-users-session"}`},
	}
	for _, test := range tests {
		t.Run(test.method, func(t *testing.T) {
			dispatched = false
			body := []byte(`{"method":"` + test.method + `","params":` + test.params + `}`)
			recorder := httptest.NewRecorder()
			wc.handleRPC(recorder, authedAPIRequestFor(http.MethodPost, "/api/rpc", body, "web-2", 2))
			if recorder.Code != http.StatusForbidden || dispatched {
				t.Fatalf("status=%d dispatched=%v body=%s", recorder.Code, dispatched, recorder.Body.String())
			}
		})
	}
}

func TestRESTRPCGetSessionSubscriptionChecksOwnership(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.Exec(
		"INSERT INTO tenants (channel, chat_id, owner_user_id, last_active_at) VALUES (?, ?, ?, ?)",
		"cli", "/owned", 42, time.Now().Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO tenants (channel, chat_id, owner_user_id, last_active_at) VALUES (?, ?, ?, ?)",
		"cli", "/foreign", 7, time.Now().Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}

	dispatched := 0
	wc := NewWebChannel(WebChannelConfig{DB: db}, bus.NewMessageBus())
	wc.SetCallbacks(WebCallbacks{
		IdentityResolver: fixedIdentityResolver{userID: 42, role: "user"},
		RPCHandler: func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error) {
			dispatched++
			return json.RawMessage(`{"subscription_id":"sub-a","model":"model-a"}`), nil
		},
	})

	owned := httptest.NewRecorder()
	wc.handleRPC(owned, authedAPIRequestFor(http.MethodPost, "/api/rpc", []byte(`{"method":"get_session_subscription","params":{"channel":"cli","chat_id":"/owned"}}`), "web-2", 2))
	if owned.Code != http.StatusOK || dispatched != 1 {
		t.Fatalf("owned status=%d dispatched=%d body=%s", owned.Code, dispatched, owned.Body.String())
	}

	foreign := httptest.NewRecorder()
	wc.handleRPC(foreign, authedAPIRequestFor(http.MethodPost, "/api/rpc", []byte(`{"method":"get_session_subscription","params":{"channel":"cli","chat_id":"/foreign"}}`), "web-2", 2))
	if foreign.Code != http.StatusForbidden || dispatched != 1 {
		t.Fatalf("foreign status=%d dispatched=%d body=%s", foreign.Code, dispatched, foreign.Body.String())
	}
}

func TestRESTRPCRejectsMalformedPluginWidgetParamsAsBadRequest(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	dispatched := false
	wc.SetRPCHandler(func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error) {
		dispatched = true
		return json.RawMessage(`{}`), nil
	})
	recorder := httptest.NewRecorder()
	wc.handleRPC(recorder, authedAPIRequestFor(http.MethodPost, "/api/rpc", []byte(`{"method":"plugin_widgets","params":{"chat_id":123}}`), "web-2", 2))
	envelope := decodeAPIData(t, recorder.Body, nil)
	if recorder.Code != http.StatusBadRequest || dispatched || envelope.Error == nil || envelope.Error.Code != "bad_request" {
		t.Fatalf("status=%d dispatched=%v envelope=%#v", recorder.Code, dispatched, envelope)
	}
}

func TestRESTRPCClassifiesDispatchErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "authorization", err: errors.New("access denied"), wantStatus: http.StatusForbidden, wantCode: "forbidden"},
		{name: "invalid request", err: errors.New("unknown RPC method: missing"), wantStatus: http.StatusBadRequest, wantCode: "bad_request"},
		{name: "runtime", err: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
			wc.SetRPCHandler(func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error) {
				return nil, test.err
			})
			recorder := httptest.NewRecorder()
			wc.handleRPC(recorder, authedAPIRequestFor(http.MethodPost, "/api/rpc", []byte(`{"method":"get_settings","params":{}}`), "web-2", 2))
			envelope := decodeAPIData(t, recorder.Body, nil)
			if recorder.Code != test.wantStatus || envelope.Error == nil || envelope.Error.Code != test.wantCode {
				t.Fatalf("status=%d envelope=%#v", recorder.Code, envelope)
			}
		})
	}
}

func TestRESTRPCPreservesAdminDispatch(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	dispatched := false
	wc.SetCallbacks(WebCallbacks{
		IdentityResolver: fixedIdentityResolver{userID: 42, role: "admin"},
		RPCHandler: func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error) {
			dispatched = true
			if identity.SenderID != "web-2" || identity.CanonicalUserID != 42 || identity.CanonicalRole != "admin" {
				t.Fatalf("unexpected RPC identity: %#v", identity)
			}
			return json.RawMessage(`{}`), nil
		},
	})
	recorder := httptest.NewRecorder()
	wc.handleRPC(recorder, authedAPIRequestFor(http.MethodPost, "/api/rpc", []byte(`{"method":"send_inbound","params":{}}`), "web-2", 2))
	if recorder.Code != http.StatusOK || !dispatched {
		t.Fatalf("admin RPC status=%d dispatched=%v body=%s", recorder.Code, dispatched, recorder.Body.String())
	}
}

func TestRESTSessionStatusReturnsTokenUsageAndCWD(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	wc.SetCallbacks(WebCallbacks{
		RPCHandler: func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error) {
			if method != "get_context_usage" {
				t.Fatalf("unexpected RPC method %q", method)
			}
			return json.RawMessage(`{"available":true,"prompt_tokens":250,"completion_tokens":25,"max_context_tokens":1000,"usage_percent":25}`), nil
		},
		GetCWD: func(senderID string, sel SessionSelector) (string, error) {
			return "/home/user", nil
		},
	})
	recorder := httptest.NewRecorder()
	wc.handleSessionStatus(recorder, authedAPIRequest(http.MethodPost, "/api/session/status", []byte(`{"chat_id":"web-1"}`)))
	_, data := decodeAPIResponse(t, recorder)
	usage := data["token_usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(250) || usage["max_tokens"] != float64(1000) || usage["usage_pct"] != float64(25) {
		t.Fatalf("unexpected token usage: %#v", usage)
	}
	if data["cwd"] != "/home/user" {
		t.Fatalf("unexpected cwd: %#v", data["cwd"])
	}
	// tasks and background_tasks are no longer bundled — they have
	// their own endpoints (/api/cron/list, /api/tasks/list).
	if _, ok := data["tasks"]; ok {
		t.Fatalf("status should not include tasks: %#v", data)
	}
	if _, ok := data["background_tasks"]; ok {
		t.Fatalf("status should not include background_tasks: %#v", data)
	}
}

func TestRESTCronListReturnsTasks(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	wc.SetCallbacks(WebCallbacks{
		CronTasks: func(senderID string, sel SessionSelector) (any, error) {
			return []map[string]any{{"id": "task-1"}}, nil
		},
	})
	recorder := httptest.NewRecorder()
	wc.handleCronListPOST(recorder, authedAPIRequest(http.MethodPost, "/api/cron/list", []byte(`{"chat_id":"web-1"}`)))
	_, data := decodeAPIResponse(t, recorder)
	tasks := data["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 cron task, got %#v", tasks)
	}
}

func TestRESTTasksListReturnsBackgroundTasks(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	wc.SetCallbacks(WebCallbacks{
		BackgroundTasks: func(senderID string, sel SessionSelector) (any, error) {
			return []map[string]any{{"id": "bg-1"}}, nil
		},
	})
	recorder := httptest.NewRecorder()
	wc.handleTasksListPOST(recorder, authedAPIRequest(http.MethodPost, "/api/tasks/list", []byte(`{"chat_id":"web-1"}`)))
	_, data := decodeAPIResponse(t, recorder)
	tasks := data["background_tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 background task, got %#v", tasks)
	}
}

func TestRESTHistoryCursorPrecedesInterleavedEvent(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	wc.SetCallbacks(WebCallbacks{
		HistorySnapshot: func(senderID string, sel SessionSelector) (HistorySnapshot, error) {
			wc.hub.sendToClient(sel.ChatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "interleaved"})
			return HistorySnapshot{Messages: []ch.HistoryMessage{}}, nil
		},
	})

	recorder := httptest.NewRecorder()
	wc.handleHistory(recorder, authedAPIRequest(http.MethodGet, "/api/history?channel=web&chat_id=web-1", nil))
	_, data := decodeAPIResponse(t, recorder)
	if recorder.Code != http.StatusOK || data["last_seq"] != float64(0) {
		t.Fatalf("history status=%d data=%#v", recorder.Code, data)
	}
	if got := wc.getEventStream(sessionRouteKey("web", "web-1")).lastSeq(); got != 1 {
		t.Fatalf("event stream last seq=%d, want 1", got)
	}
}

func TestRESTSessionStatusReturnsIdleOwnedSessionCWD(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.Exec(
		"INSERT INTO user_chats (channel, sender_id, chat_id, label) VALUES (?, ?, ?, ?)",
		"web", "web-2", "owned-chat", "Owned",
	); err != nil {
		t.Fatal(err)
	}
	wc := NewWebChannel(WebChannelConfig{DB: db}, bus.NewMessageBus())
	wc.SetCallbacks(WebCallbacks{
		GetCWD: func(senderID string, sel SessionSelector) (string, error) {
			if senderID != "web-2" || sel.Channel != "web" || sel.ChatID != "owned-chat" {
				t.Fatalf("unexpected CWD selector: sender=%q selector=%#v", senderID, sel)
			}
			return "/workspace/idle", nil
		},
	})
	recorder := httptest.NewRecorder()
	wc.handleSessionStatus(recorder, authedAPIRequestFor(http.MethodPost, "/api/session/status", []byte(`{"channel":"web","chat_id":"owned-chat"}`), "web-2", 2))
	_, data := decodeAPIResponse(t, recorder)
	if recorder.Code != http.StatusOK || data["cwd"] != "/workspace/idle" {
		t.Fatalf("status=%d data=%#v", recorder.Code, data)
	}
}

func TestRESTSessionStatusInfersCurrentCLIChannelFromChatID(t *testing.T) {
	db := newTestDB(t)
	wc := NewWebChannel(WebChannelConfig{DB: db}, bus.NewMessageBus())
	setTestCurrentSession(wc, SessionSelector{Channel: "cli", ChatID: "/home/user"})
	if _, err := db.Exec("INSERT INTO tenants (channel, chat_id, last_active_at) VALUES (?, ?, ?)", "cli", "/home/user", time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	wc.SetCallbacks(WebCallbacks{
		RPCHandler: func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error) {
			var session sessionBody
			if err := json.Unmarshal(params, &session); err != nil {
				t.Fatal(err)
			}
			if session.Channel != "cli" || session.ChatID != "/home/user" {
				t.Fatalf("wrong session routed to token RPC: %#v", session)
			}
			return json.RawMessage(`{"prompt_tokens":1}`), nil
		},
	})
	recorder := httptest.NewRecorder()
	wc.handleSessionStatus(recorder, authedAPIRequest(http.MethodPost, "/api/session/status", []byte(`{"chat_id":"/home/user"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("session status = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRESTHistoryInfersCurrentOwnedAgentChannelFromChatID(t *testing.T) {
	db := newTestDB(t)
	wc := NewWebChannel(WebChannelConfig{DB: db}, bus.NewMessageBus())
	chatID := "web:web-2/review:1"
	setTestCurrentSessionFor(wc, "web-2", SessionSelector{Channel: "agent", ChatID: chatID})
	if _, err := db.Exec("INSERT INTO tenants (channel, chat_id, last_active_at) VALUES (?, ?, ?)", "agent", chatID, time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	wc.SetCallbacks(WebCallbacks{
		HistorySnapshot: func(senderID string, sel SessionSelector) (HistorySnapshot, error) {
			if senderID != "web-2" || sel.Channel != "agent" || sel.ChatID != chatID {
				t.Fatalf("wrong history selector: sender=%q selector=%#v", senderID, sel)
			}
			return HistorySnapshot{}, nil
		},
	})
	recorder := httptest.NewRecorder()
	request := authedAPIRequestFor(http.MethodGet, "/api/history?chat_id="+url.QueryEscape(chatID), nil, "web-2", 2)
	wc.handleHistory(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("history status = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRESTRunnersIncludeTokenOnListAndCreate(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	wc.SetCallbacks(WebCallbacks{
		RunnerList: func(senderID string) ([]tools.RunnerInfo, error) {
			return []tools.RunnerInfo{{Name: "runner-a", Token: "secret-token", LLMAPIKey: "llm-secret"}}, nil
		},
		RunnerCreate: func(senderID, name, mode, dockerImage, workspace string, llm tools.RunnerLLMSettings) (string, error) {
			return "xbot-runner --token secret-token", nil
		},
	})

	listRecorder := httptest.NewRecorder()
	wc.handleRunners(listRecorder, authedAPIRequest(http.MethodGet, "/api/runners", nil))
	_, listData := decodeAPIResponse(t, listRecorder)
	runner := listData["runners"].([]any)[0].(map[string]any)
	if runner["token"] != "secret-token" || runner["llm_api_key"] == "llm-secret" {
		t.Fatalf("runner list token/key handling is wrong: %#v", runner)
	}

	createRecorder := httptest.NewRecorder()
	wc.handleRunners(createRecorder, authedAPIRequest(http.MethodPost, "/api/runners", []byte(`{"name":"runner-a","mode":"native"}`)))
	_, createData := decodeAPIResponse(t, createRecorder)
	if createData["token"] != "secret-token" {
		t.Fatalf("runner create did not return token: %#v", createData)
	}
}

func TestRESTLLMModelAndMaxContextEndpoints(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	var selectedModel string
	var maxContext int
	wc.SetCallbacks(WebCallbacks{
		LLMSet: func(senderID, subID, model string) error {
			selectedModel = subID + ":" + model
			return nil
		},
		LLMGetMaxContext: func(senderID, subID, model string) int { return maxContext },
		LLMSetMaxContext: func(senderID, subID, model string, value int) error {
			maxContext = value
			return nil
		},
	})

	modelRecorder := httptest.NewRecorder()
	wc.handleLLMModelSet(modelRecorder, authedAPIRequest(http.MethodPost, "/api/llm-config/model", []byte(`{"sub_id":"sub-a","model":"model-a"}`)))
	if modelRecorder.Code != http.StatusOK || selectedModel != "sub-a:model-a" {
		t.Fatalf("set_model failed: status=%d selected=%q", modelRecorder.Code, selectedModel)
	}

	setRecorder := httptest.NewRecorder()
	wc.handleLLMMaxContext(setRecorder, authedAPIRequest(http.MethodPost, "/api/llm-max-context", []byte(`{"max_context":12345}`)))
	if setRecorder.Code != http.StatusOK || maxContext != 12345 {
		t.Fatalf("set_max_context failed: status=%d value=%d", setRecorder.Code, maxContext)
	}

	getRecorder := httptest.NewRecorder()
	wc.handleLLMMaxContext(getRecorder, authedAPIRequest(http.MethodGet, "/api/llm-max-context", nil))
	_, getData := decodeAPIResponse(t, getRecorder)
	if getData["max_context"] != float64(12345) {
		t.Fatalf("get_max_context failed: %#v", getData)
	}
}

func TestRESTFileEndpointsUseJSONBodyAndMergedBehavior(t *testing.T) {
	dir := t.TempDir()
	textPath := filepath.Join(dir, "hello.txt")
	binaryPath := filepath.Join(dir, "image.bin")
	if err := os.WriteFile(textPath, []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}
	binaryContent := []byte{0, 1, 2, 3}
	if err := os.WriteFile(binaryPath, binaryContent, 0o600); err != nil {
		t.Fatal(err)
	}
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())

	listRecorder := httptest.NewRecorder()
	wc.handleFsList(listRecorder, authedAPIRequest(http.MethodGet, "/api/fs/list?path="+url.QueryEscape(dir), nil))
	_, listData := decodeAPIResponse(t, listRecorder)
	entries := listData["entries"].([]any)
	if len(entries) != 2 || entries[0].(map[string]any)["mode"] == "" {
		t.Fatalf("list response missing stat mode: %#v", entries)
	}

	readRecorder := httptest.NewRecorder()
	wc.handleFsRead(readRecorder, authedAPIRequest(http.MethodGet, "/api/fs/read?path="+url.QueryEscape(binaryPath), nil))
	_, readData := decodeAPIResponse(t, readRecorder)
	if readData["encoding"] != "base64" || readData["content"] != base64.StdEncoding.EncodeToString(binaryContent) {
		t.Fatalf("binary read was not base64 encoded: %#v", readData)
	}

	rawRecorder := httptest.NewRecorder()
	wc.handleFsRaw(rawRecorder, authedAPIRequest(http.MethodGet, "/api/fs/raw?path="+url.QueryEscape(textPath), nil))
	if rawRecorder.Code != http.StatusOK || rawRecorder.Body.String() != "hello" || rawRecorder.Header().Get("Content-Type") == "application/json" {
		t.Fatalf("unexpected raw response: status=%d type=%q body=%q", rawRecorder.Code, rawRecorder.Header().Get("Content-Type"), rawRecorder.Body.String())
	}
}

func TestCanAccessAgentSessionUsesTenantAndWebParentOwnership(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	if _, err := db.Exec("INSERT INTO tenants (channel, chat_id, last_active_at) VALUES (?, ?, ?)", "agent", "web:web-2/review:1", time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO tenants (channel, chat_id, last_active_at) VALUES (?, ?, ?)", "agent", "cli:/repo:Agent-main/review:1", time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	if !wc.canAccessSession(contextWithUserID(context.Background(), 2), 2, "web-2", "agent", "web:web-2/review:1") {
		t.Fatal("web user should access SubAgent under their default web session")
	}
	if wc.canAccessSession(contextWithUserID(context.Background(), 3), 3, "web-3", "agent", "web:web-2/review:1") {
		t.Fatal("different web user must not access another user's SubAgent")
	}
	if !wc.canAccessSession(contextWithUserID(context.Background(), 1), 1, "web-1", "agent", "cli:/repo:Agent-main/review:1") {
		t.Fatal("admin web user should access existing cli-backed SubAgent")
	}
	if wc.canAccessSession(contextWithUserID(context.Background(), 1), 1, "web-1", "agent", "cli:/repo:Agent-main/missing:1") {
		t.Fatal("admin access still requires an existing agent tenant")
	}
}

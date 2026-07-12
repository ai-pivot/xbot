package web

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"xbot/protocol"
)

func TestWriteSSEEventFormat(t *testing.T) {
	recorder := httptest.NewRecorder()
	client := &Client{w: recorder, flusher: recorder}
	msg := protocol.WSMessage{
		Type:    protocol.MsgTypeText,
		Seq:     42,
		Content: "hello",
	}

	if err := writeSSEEvent(client, msg); err != nil {
		t.Fatal(err)
	}
	wantData, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	want := "id:42\nevent:text\ndata:" + string(wantData) + "\n\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("unexpected SSE event:\n got: %q\nwant: %q", got, want)
	}
	if !recorder.Flushed {
		t.Fatal("SSE event was not flushed")
	}
}

func TestWriteSSEHeartbeat(t *testing.T) {
	if sseHeartbeatInterval != 15*time.Second {
		t.Fatalf("heartbeat interval = %s, want 15s", sseHeartbeatInterval)
	}
	recorder := httptest.NewRecorder()
	client := &Client{w: recorder, flusher: recorder}

	if err := writeSSEHeartbeat(client); err != nil {
		t.Fatal(err)
	}
	if got := recorder.Body.String(); got != ":heartbeat\n\n" {
		t.Fatalf("heartbeat = %q, want %q", got, ":heartbeat\n\n")
	}
	if !recorder.Flushed {
		t.Fatal("SSE heartbeat was not flushed")
	}
}

func TestSSEReceivesHubStatefulAndStatelessEvents(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)

	resp := openSSE(t, server.URL, cookie, "web-1", "")
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q", got)
	}
	if got := resp.Header.Get("Connection"); got != "keep-alive" {
		t.Fatalf("Connection = %q", got)
	}

	wc.hub.mu.RLock()
	var connected *Client
	for _, client := range wc.hub.conns {
		connected = client
		break
	}
	wc.hub.mu.RUnlock()
	if connected == nil {
		t.Fatal("SSE client was not registered with Hub")
	}
	if connected.connType != clientConnTypeSSE || connected.w == nil || connected.flusher == nil {
		t.Fatalf("unexpected SSE client transport fields: %#v", connected)
	}

	reader := bufio.NewReader(resp.Body)
	wc.SendProgress("web-1", &protocol.ProgressEvent{Phase: "thinking"})
	stateful := readSSEEvent(t, reader)
	assertSSEMessage(t, stateful, protocol.MsgTypeProgress, 1)

	wc.SendStreamContent("web-1", "partial", "reasoning")
	stateless := readSSEEvent(t, reader)
	assertSSEMessage(t, stateless, protocol.MsgTypeStreamContent, 2)

	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	waitForHubClients(t, wc, 0)
}

func TestSSELastEventIDReplaysMissedEvents(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.hub.sendToClient("web-1", protocol.WSMessage{Type: protocol.MsgTypeText, Content: "seen"})
	wc.hub.sendToClient("web-1", protocol.WSMessage{Type: protocol.MsgTypeText, Content: "missed"})
	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)

	resp := openSSE(t, server.URL, cookie, "web-1", "1")
	defer resp.Body.Close()
	event := readSSEEvent(t, bufio.NewReader(resp.Body))
	msg := assertSSEMessage(t, event, protocol.MsgTypeText, 2)
	if msg.Content != "missed" {
		t.Fatalf("replayed content = %q, want missed", msg.Content)
	}

	wc.hub.sendToClient("web-1", protocol.WSMessage{Type: protocol.MsgTypeText, Content: "live"})
	next := assertSSEMessage(t, readSSEEvent(t, bufio.NewReader(resp.Body)), protocol.MsgTypeText, 3)
	if next.Content != "live" {
		t.Fatalf("next content = %q, want live", next.Content)
	}
}

func TestSSEReconnectSendsActiveProgressWhenReplayHasNone(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.SetCallbacks(WebCallbacks{
		GetActiveProgress: func(channel, chatID string) *protocol.ProgressEvent {
			return &protocol.ProgressEvent{Phase: "tool_exec"}
		},
	})
	wc.stampAndBuffer("web-1", protocol.WSMessage{Type: protocol.MsgTypeText, Content: "seen"})
	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)

	resp := openSSE(t, server.URL, cookie, "web-1", "1")
	defer resp.Body.Close()
	event := readSSEEvent(t, bufio.NewReader(resp.Body))
	msg := assertSSEMessage(t, event, protocol.MsgTypeProgress, 2)
	if msg.Progress == nil || msg.Progress.Phase != "tool_exec" {
		t.Fatalf("unexpected active progress: %#v", msg.Progress)
	}
}

func TestSSEReplaySortsMixedStatefulAndStatelessEvents(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	wc.hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeStreamContent, Content: "stream-1"})
	wc.hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "stateful-2"})
	wc.hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeStreamContent, Content: "stream-3"})

	events := wc.replaySSEEvents(SessionSelector{Channel: "web", ChatID: chatID}, 0)
	if len(events) != 2 {
		t.Fatalf("replay event count = %d, want 2", len(events))
	}
	if events[0].Seq != 2 || events[0].Content != "stateful-2" {
		t.Fatalf("first replay event = %#v", events[0])
	}
	if events[1].Seq != 3 || events[1].Content != "stream-3" {
		t.Fatalf("second replay event = %#v", events[1])
	}
}

func TestSSEReplayHandoffSuppressesStaleFallback(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.hub.sendToClient("web-1", protocol.WSMessage{Type: protocol.MsgTypeText, Content: "seen"})
	progressLookup := make(chan struct{}, 1)
	releaseLookup := make(chan struct{})
	wc.SetCallbacks(WebCallbacks{
		GetActiveProgress: func(channel, chatID string) *protocol.ProgressEvent {
			progressLookup <- struct{}{}
			<-releaseLookup
			return &protocol.ProgressEvent{Phase: "tool_exec"}
		},
	})
	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)

	resp := openSSE(t, server.URL, cookie, "web-1", "1")
	defer resp.Body.Close()
	select {
	case <-progressLookup:
	case <-time.After(2 * time.Second):
		t.Fatal("active progress lookup did not start")
	}
	wc.SendProgress("web-1", &protocol.ProgressEvent{Phase: "thinking"})
	close(releaseLookup)

	reader := bufio.NewReader(resp.Body)
	live := assertSSEMessage(t, readSSEEvent(t, reader), protocol.MsgTypeProgress, 2)
	if live.Progress == nil || live.Progress.Phase != "thinking" {
		t.Fatalf("unexpected live progress: %#v", live.Progress)
	}
	if got := wc.getEventStream("web-1").lastSeq(); got != 2 {
		t.Fatalf("last sequence = %d, want 2; stale fallback was published", got)
	}
}

func TestSSEPendingAskUserReplayDoesNotDuplicate(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	content, err := json.Marshal(protocol.AskUserEvent{ChatID: chatID, RequestID: "request-1"})
	if err != nil {
		t.Fatal(err)
	}
	wc.hub.sendToClient(chatID, protocol.WSMessage{
		Type:    protocol.MsgTypeAskUser,
		ChatID:  chatID,
		Content: string(content),
	})
	wc.SetCallbacks(WebCallbacks{
		GetPendingAskUser: func(channel, gotChatID string) *protocol.ProgressEvent {
			return &protocol.ProgressEvent{RequestID: "request-1"}
		},
	})

	wc.publishSSEFallbacks(SessionSelector{Channel: "web", ChatID: chatID}, 0)

	if got := wc.getEventStream(chatID).lastSeq(); got != 1 {
		t.Fatalf("last sequence = %d, want 1", got)
	}
	events := wc.replaySSEEvents(SessionSelector{Channel: "web", ChatID: chatID}, 0)
	if len(events) != 1 || askUserRequestID(events[0]) != "request-1" {
		t.Fatalf("replayed AskUser events = %#v", events)
	}
}

func TestSSEFallbackIsSharedBySubscribedClients(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	clients := []*Client{
		{connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 4), done: make(chan struct{}), id: "sse-1", chatID: chatID},
		{connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 4), done: make(chan struct{}), id: "sse-2", chatID: chatID},
	}
	for _, client := range clients {
		wc.hub.addClient(client.id, client)
		wc.hub.subscribe(client.id, chatID)
	}
	wc.SetCallbacks(WebCallbacks{
		GetActiveProgress: func(channel, gotChatID string) *protocol.ProgressEvent {
			return &protocol.ProgressEvent{Phase: "tool_exec"}
		},
	})
	sel := SessionSelector{Channel: "web", ChatID: chatID}

	wc.publishSSEFallbacks(sel, 0)
	wc.publishSSEFallbacks(sel, 0)
	for _, client := range clients {
		fallback := <-client.sendCh
		if fallback.Type != protocol.MsgTypeProgress || fallback.Seq != 1 {
			t.Fatalf("client %s fallback = %#v", client.id, fallback)
		}
		select {
		case duplicate := <-client.sendCh:
			t.Fatalf("client %s received duplicate fallback: %#v", client.id, duplicate)
		default:
		}
	}

	wc.hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "next"})
	for _, client := range clients {
		next := <-client.sendCh
		if next.Type != protocol.MsgTypeText || next.Seq != 2 {
			t.Fatalf("client %s next event = %#v", client.id, next)
		}
	}
}

func TestSSECatchesUpAfterSendChannelOverflow(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	progressLookup := make(chan struct{}, 1)
	releaseLookup := make(chan struct{})
	wc.SetCallbacks(WebCallbacks{
		GetActiveProgress: func(channel, chatID string) *protocol.ProgressEvent {
			progressLookup <- struct{}{}
			<-releaseLookup
			return nil
		},
	})
	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)

	resp := openSSE(t, server.URL, cookie, "web-1", "")
	defer resp.Body.Close()
	select {
	case <-progressLookup:
	case <-time.After(2 * time.Second):
		t.Fatal("active progress lookup did not start")
	}

	const overflow = 8
	eventCount := webSendChBufSize + overflow
	for i := 1; i <= eventCount; i++ {
		wc.hub.sendToClient("web-1", protocol.WSMessage{
			Type:    protocol.MsgTypeText,
			Content: "event-" + strconv.Itoa(i),
		})
	}
	close(releaseLookup)

	reader := bufio.NewReader(resp.Body)
	for i := 1; i <= eventCount; i++ {
		msg := assertSSEMessage(t, readSSEEvent(t, reader), protocol.MsgTypeText, uint64(i))
		if want := "event-" + strconv.Itoa(i); msg.Content != want {
			t.Fatalf("event %d content = %q, want %q", i, msg.Content, want)
		}
	}
}

func TestSSEContractEventsReceiveSequences(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	client := &Client{
		connType: clientConnTypeSSE,
		sendCh:   make(chan protocol.WSMessage, 16),
		done:     make(chan struct{}),
		id:       "sse-contract",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, "web-1")

	types := []string{
		protocol.MsgTypeText,
		protocol.MsgTypeProgress,
		protocol.MsgTypeStreamContent,
		protocol.MsgTypeAskUser,
		protocol.MsgTypeCard,
		protocol.MsgTypeUserEcho,
		protocol.MsgTypeInjectUser,
		protocol.MsgTypePluginWidgets,
		protocol.MsgTypeRunnerStatus,
		protocol.MsgTypeSyncProgress,
	}
	for i, msgType := range types {
		if !wc.hub.sendToClient("web-1", protocol.WSMessage{Type: msgType}) {
			t.Fatalf("message %q was not delivered", msgType)
		}
		msg := <-client.sendCh
		wantSeq := uint64(i + 1)
		if msg.Seq != wantSeq {
			t.Fatalf("message %q seq = %d, want %d", msgType, msg.Seq, wantSeq)
		}
	}

	wc.SendSessionState(protocol.SessionEvent{Channel: "web", ChatID: "web-1", Action: "busy"})
	msg := <-client.sendCh
	if msg.Type != protocol.MsgTypeSession || msg.Seq != uint64(len(types)+1) {
		t.Fatalf("session event = %#v", msg)
	}
}

func TestSSESessionBroadcastIsIsolatedBySubscription(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	client1 := &Client{connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "user-1", userID: "web-1"}
	client2 := &Client{connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "user-2", userID: "web-2"}
	wsClient := &Client{connType: clientConnTypeWS, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "legacy-ws"}
	for _, client := range []*Client{client1, client2, wsClient} {
		wc.hub.addClient(client.id, client)
	}
	wc.hub.subscribe(client1.id, "web-1")
	wc.hub.subscribe(client2.id, "web-2")

	wc.SendSessionState(protocol.SessionEvent{Channel: "web", ChatID: "web-1", Action: "busy"})
	select {
	case msg := <-client1.sendCh:
		if msg.Type != protocol.MsgTypeSession || msg.Seq == 0 {
			t.Fatalf("user-1 session event = %#v", msg)
		}
	default:
		t.Fatal("authorized SSE client did not receive session event")
	}
	select {
	case msg := <-client2.sendCh:
		t.Fatalf("foreign SSE client received session event: %#v", msg)
	default:
	}
	select {
	case <-wsClient.sendCh:
	default:
		t.Fatal("legacy WS broadcast behavior changed")
	}
}

func TestHubUsesSequencedCopyWithoutChangingCLIWSMessage(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	sseClient := &Client{connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "sse"}
	webSocketClient := &Client{connType: clientConnTypeWS, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "web-ws"}
	cliClient := &Client{connType: clientConnTypeWS, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "cli-ws", isCLI: true}
	for _, client := range []*Client{sseClient, webSocketClient, cliClient} {
		wc.hub.addClient(client.id, client)
		wc.hub.subscribe(client.id, "shared-chat")
	}

	wc.hub.sendToClient("shared-chat", protocol.WSMessage{Type: protocol.MsgTypeText, Content: "hello"})
	sseMsg := <-sseClient.sendCh
	webSocketMsg := <-webSocketClient.sendCh
	cliMsg := <-cliClient.sendCh
	if sseMsg.Seq != 1 || webSocketMsg.Seq != sseMsg.Seq {
		t.Fatalf("Web transport sequences differ: SSE=%d WS=%d", sseMsg.Seq, webSocketMsg.Seq)
	}
	if cliMsg.Seq != 0 {
		t.Fatalf("CLI WS envelope sequence changed: %d", cliMsg.Seq)
	}
}

func TestSSERejectsInvalidLastEventID(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)

	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/sse?chat_id=web-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookie)
	req.Header.Set("Last-Event-ID", "not-a-sequence")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSSERequiresAuthenticationAndChatID(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)

	resp, err := http.Get(server.URL + "/api/sse?chat_id=web-1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", resp.StatusCode)
	}

	cookie := loginTestAdmin(t, server.URL)
	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing chat_id status = %d, want 400", resp.StatusCode)
	}
}

func openSSE(t *testing.T, serverURL string, cookie *http.Cookie, chatID, lastEventID string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, serverURL+"/api/sse?chat_id="+chatID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookie)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("SSE status = %d: %s", resp.StatusCode, body)
	}
	return resp
}

func readSSEEvent(t *testing.T, reader *bufio.Reader) map[string]string {
	t.Helper()
	fields := make(map[string]string, 3)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE event: %v", err)
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			return fields
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("invalid SSE line %q", line)
		}
		fields[key] = value
	}
}

func assertSSEMessage(t *testing.T, event map[string]string, wantType string, wantSeq uint64) protocol.WSMessage {
	t.Helper()
	if got := event["event"]; got != wantType {
		t.Fatalf("event type = %q, want %q", got, wantType)
	}
	if got := event["id"]; got != strconv.FormatUint(wantSeq, 10) {
		t.Fatalf("event id = %q, want %d", got, wantSeq)
	}
	var msg protocol.WSMessage
	if err := json.Unmarshal([]byte(event["data"]), &msg); err != nil {
		t.Fatalf("decode SSE data: %v", err)
	}
	if msg.Type != wantType || msg.Seq != wantSeq {
		t.Fatalf("SSE data envelope = %#v", msg)
	}
	return msg
}

func waitForHubClients(t *testing.T, wc *WebChannel, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		wc.hub.mu.RLock()
		got := len(wc.hub.conns)
		wc.hub.mu.RUnlock()
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Hub client count did not reach %d", want)
}

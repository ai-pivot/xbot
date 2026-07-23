package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	ch "xbot/channel"
	"xbot/protocol"
	"xbot/tools"
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

func TestWriteSSECursor(t *testing.T) {
	recorder := httptest.NewRecorder()
	client := &Client{w: recorder, flusher: recorder}

	if err := writeSSECursor(client, 42); err != nil {
		t.Fatal(err)
	}
	if got := recorder.Body.String(); got != "id:42\n\n" {
		t.Fatalf("SSE cursor = %q, want %q", got, "id:42\n\n")
	}
	if !recorder.Flushed {
		t.Fatal("SSE cursor was not flushed")
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

func TestSSEIsolatesChannelsWithTheSameChatID(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)

	resp := openSSE(t, server.URL, cookie, "web-1", "0")
	defer resp.Body.Close()
	wc.hub.sendToSession("cli", "web-1", protocol.WSMessage{Type: protocol.MsgTypeText, Content: "foreign cli"})
	wc.hub.sendToSession("web", "web-1", protocol.WSMessage{Type: protocol.MsgTypeText, Content: "web event"})

	msg := assertSSEMessage(t, readSSEEvent(t, bufio.NewReader(resp.Body)), protocol.MsgTypeText, 1)
	if msg.Content != "web event" {
		t.Fatalf("cross-channel SSE event leaked: %#v", msg)
	}
	if got := wc.getEventStream(sessionRouteKey("cli", "web-1")).lastSeq(); got != 1 {
		t.Fatalf("CLI stream seq = %d, want 1", got)
	}
	if got := wc.getEventStream(sessionRouteKey("web", "web-1")).lastSeq(); got != 1 {
		t.Fatalf("Web stream seq = %d, want 1", got)
	}
}

func TestSSEFreshConnectionCursorRecoversEventsPublishedWhileDisconnected(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)

	resp := openSSE(t, server.URL, cookie, "web-1", "")
	cursor := readSSEFrame(t, bufio.NewReader(resp.Body))
	if got := cursor["id"]; got != "0" {
		t.Fatalf("fresh SSE cursor = %q, want 0", got)
	}
	if _, ok := cursor["event"]; ok {
		t.Fatalf("fresh SSE cursor unexpectedly dispatched an event: %#v", cursor)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	waitForHubClients(t, wc, 0)

	wc.hub.sendToClient("web-1", protocol.WSMessage{Type: protocol.MsgTypeText, Content: "while disconnected"})
	reconnected := openSSE(t, server.URL, cookie, "web-1", cursor["id"])
	defer reconnected.Body.Close()
	msg := assertSSEMessage(t, readSSEEvent(t, bufio.NewReader(reconnected.Body)), protocol.MsgTypeText, 1)
	if msg.Content != "while disconnected" {
		t.Fatalf("replayed content = %q, want while disconnected", msg.Content)
	}
}

func TestSSEInitialConnectStartsAtCurrentHighWater(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.hub.sendToClient("web-1", protocol.WSMessage{Type: protocol.MsgTypeText, Content: "old text"})
	wc.hub.sendToClient("web-1", protocol.WSMessage{Type: protocol.MsgTypeAskUser, Content: `{"request_id":"resolved"}`})
	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)

	resp := openSSE(t, server.URL, cookie, "web-1", "")
	defer resp.Body.Close()
	wc.hub.sendToClient("web-1", protocol.WSMessage{Type: protocol.MsgTypeText, Content: "live"})

	msg := assertSSEMessage(t, readSSEEvent(t, bufio.NewReader(resp.Body)), protocol.MsgTypeText, 3)
	if msg.Content != "live" {
		t.Fatalf("initial SSE event content = %q, want live", msg.Content)
	}
}

func TestSSEFreshHighWaterKeepsFullProgressAfterStreamPatch(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	full := &protocol.ProgressEvent{
		Seq:              1,
		Phase:            "tool_exec",
		Iteration:        2,
		ActiveTools:      []protocol.ToolProgress{{Name: "shell", Status: "running"}},
		IterationHistory: []protocol.ProgressEvent{{Iteration: 1, Phase: "done"}},
	}
	wc.SendProgress(chatID, full)
	wc.SendProgress(chatID, &protocol.ProgressEvent{StreamContent: "partial"})
	client := &Client{
		connType:       clientConnTypeSSE,
		sendCh:         make(chan protocol.WSMessage, 1),
		statelessSig:   make(chan struct{}, 1),
		done:           make(chan struct{}),
		chatID:         chatID,
		sessionChannel: "web",
		id:             "sse-full-progress",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, sessionRouteKey("web", chatID))
	wc.SetCallbacks(WebCallbacks{
		GetActiveProgress: func(channel, gotChatID string) *protocol.ProgressEvent {
			copy := *full
			copy.StreamContent = "partial"
			return &copy
		},
	})

	wc.publishSSEFallbacks(SessionSelector{Channel: "web", ChatID: chatID}, 2)

	msg := drainProgress(t, client)
	if len(msg) != 1 || msg[0].Seq != 3 || msg[0].Progress == nil {
		t.Fatalf("full progress fallback = %#v", msg)
	}
	if msg[0].Progress.Phase != "tool_exec" || msg[0].Progress.Iteration != 2 || len(msg[0].Progress.ActiveTools) != 1 || len(msg[0].Progress.IterationHistory) != 1 {
		t.Fatalf("full progress fields were lost: %#v", msg[0].Progress)
	}
	events := wc.replaySSEEvents(SessionSelector{Channel: "web", ChatID: chatID}, 0)
	if len(events) != 3 || events[0].Type != protocol.MsgTypeProgress || events[1].Type != protocol.MsgTypeStreamContent {
		t.Fatalf("normalized event stream = %#v", events)
	}
}

func TestSSEPositiveLastEventIDReplaysStreamAndFullProgress(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	chatID := "web-1"
	full := &protocol.ProgressEvent{
		Seq:              1,
		Phase:            "tool_exec",
		Iteration:        2,
		ActiveTools:      []protocol.ToolProgress{{Name: "shell", Status: "running"}},
		IterationHistory: []protocol.ProgressEvent{{Iteration: 1, Phase: "done"}},
	}
	wc.SendProgress(chatID, full)
	wc.SendProgress(chatID, &protocol.ProgressEvent{StreamContent: "partial"})
	wc.SetCallbacks(WebCallbacks{
		GetActiveProgress: func(channel, gotChatID string) *protocol.ProgressEvent {
			copy := *full
			copy.StreamContent = "partial"
			return &copy
		},
	})
	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)

	resp := openSSE(t, server.URL, cookie, chatID, "1")
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	stream := assertSSEMessage(t, readSSEEvent(t, reader), protocol.MsgTypeStreamContent, 2)
	if stream.Progress == nil || stream.Progress.StreamContent != "partial" {
		t.Fatalf("stream replay = %#v", stream)
	}
	progress := assertSSEMessage(t, readSSEEvent(t, reader), protocol.MsgTypeProgress, 3)
	if progress.Progress == nil || progress.Progress.Phase != "tool_exec" || len(progress.Progress.ActiveTools) != 1 || len(progress.Progress.IterationHistory) != 1 {
		t.Fatalf("full progress replay fallback = %#v", progress.Progress)
	}
}

func TestSSEReconnectMergesIndependentStreamFields(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	chatID := "web-1"
	wc.hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "baseline"})
	wc.SendProgress(chatID, &protocol.ProgressEvent{Phase: "thinking", Iteration: 1})
	wc.SendProgress(chatID, &protocol.ProgressEvent{ChatID: "web:" + chatID, StreamContent: "answer"})
	wc.SendProgress(chatID, &protocol.ProgressEvent{ChatID: "web:" + chatID, ReasoningStreamContent: "reasoning"})
	wc.SendProgress(chatID, &protocol.ProgressEvent{
		ChatID:         "web:" + chatID,
		StreamingTools: []protocol.ToolProgress{{Name: "Read", Status: "generating"}},
	})
	wc.SendProgress(chatID, &protocol.ProgressEvent{ChatID: "web:" + chatID, StreamTokens: 17})

	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)
	resp := openSSE(t, server.URL, cookie, chatID, "1")
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	assertSSEMessage(t, readSSEEvent(t, reader), protocol.MsgTypeProgress, 2)
	stream := assertSSEMessage(t, readSSEEvent(t, reader), protocol.MsgTypeStreamContent, 6)
	if stream.Progress == nil ||
		stream.Progress.StreamContent != "answer" ||
		stream.Progress.ReasoningStreamContent != "reasoning" ||
		len(stream.Progress.StreamingTools) != 1 ||
		stream.Progress.StreamingTools[0].Name != "Read" ||
		stream.Progress.StreamTokens != 17 {
		t.Fatalf("merged stream replay = %#v", stream.Progress)
	}
}

func TestSSEStreamMergeStopsAtStatefulBoundary(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	source := "web:" + chatID
	wc.SendProgress(chatID, &protocol.ProgressEvent{ChatID: source, StreamContent: "old turn"})
	wc.SendProgress(chatID, &protocol.ProgressEvent{ChatID: source, Content: "structured result"})
	wc.SendProgress(chatID, &protocol.ProgressEvent{ChatID: source, StreamTokens: 3})

	events := wc.replaySSEEvents(SessionSelector{Channel: "web", ChatID: chatID}, 0)
	if len(events) != 3 {
		t.Fatalf("replayed events = %#v, want three events across boundary", events)
	}
	latest := events[2]
	if latest.Type != protocol.MsgTypeStreamContent || latest.Progress == nil {
		t.Fatalf("latest stream event = %#v", latest)
	}
	if latest.Progress.StreamContent != "" || latest.Progress.StreamTokens != 3 {
		t.Fatalf("stream fields crossed stateful boundary: %#v", latest.Progress)
	}
}

func TestSSEStreamMergeIgnoresUnrelatedStructuredProgressBoundary(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	sourceA := "web:worker-a"
	sourceB := "web:worker-b"
	wc.SendProgress(chatID, &protocol.ProgressEvent{ChatID: sourceA, StreamContent: "worker A"})
	wc.SendProgress(chatID, &protocol.ProgressEvent{ChatID: sourceB, Content: "worker B result"})
	wc.SendProgress(chatID, &protocol.ProgressEvent{ChatID: sourceA, StreamTokens: 5})

	events := wc.replaySSEEvents(SessionSelector{Channel: "web", ChatID: chatID}, 0)
	if len(events) != 2 {
		t.Fatalf("replayed events = %#v, want structured B plus merged stream A", events)
	}
	stream := events[1]
	if stream.Type != protocol.MsgTypeStreamContent || stream.Progress == nil {
		t.Fatalf("merged worker A stream = %#v", stream)
	}
	if stream.Progress.ChatID != sourceA || stream.Progress.StreamContent != "worker A" || stream.Progress.StreamTokens != 5 {
		t.Fatalf("unrelated structured progress split worker A stream: %#v", stream.Progress)
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

func TestSSEStatelessReplacementStaysNewestWhenEventStreamIsFull(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	wc.hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeRunnerStatus, Content: "old"})
	for seq := 2; seq <= eventStreamSize; seq++ {
		wc.hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: strconv.Itoa(seq)})
	}
	wc.hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeRunnerStatus, Content: "new"})
	wc.hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "after"})

	events := wc.replaySSEEvents(SessionSelector{Channel: "web", ChatID: chatID}, eventStreamSize)
	if len(events) != 2 {
		t.Fatalf("events after full-buffer replacement = %#v, want seq 513 and 514", events)
	}
	if events[0].Seq != eventStreamSize+1 || events[0].Type != protocol.MsgTypeRunnerStatus || events[0].Content != "new" {
		t.Fatalf("replacement event = %#v", events[0])
	}
	if events[1].Seq != eventStreamSize+2 || events[1].Type != protocol.MsgTypeText || events[1].Content != "after" {
		t.Fatalf("event after replacement = %#v", events[1])
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
		WithPendingAskUser: func(channel, gotChatID string, fn func(*protocol.ProgressEvent) bool) bool {
			return fn(&protocol.ProgressEvent{RequestID: "request-1"})
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

func TestWSReconnectSkipsResolvedRetainedAskUser(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	wc.hub.sendToClient(chatID, protocol.WSMessage{
		Type:     protocol.MsgTypeAskUser,
		Progress: &protocol.ProgressEvent{RequestID: "request-1"},
	})
	wc.SetCallbacks(WebCallbacks{
		WithPendingAskUser: func(channel, gotChatID string, fn func(*protocol.ProgressEvent) bool) bool {
			return false
		},
	})
	client := &Client{sendCh: make(chan protocol.WSMessage, 4)}

	runWSReplay(t, wc, client, "web-1", 0)
	select {
	case msg := <-client.sendCh:
		t.Fatalf("resolved AskUser replayed over WS: %#v", msg)
	default:
	}
}

func TestWSReconnectRestoresOnlyMatchingPendingAskUser(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	wc.hub.sendToClient(chatID, protocol.WSMessage{
		Type:     protocol.MsgTypeAskUser,
		Progress: &protocol.ProgressEvent{RequestID: "request-1"},
	})
	wc.SetCallbacks(WebCallbacks{
		WithPendingAskUser: func(channel, gotChatID string, fn func(*protocol.ProgressEvent) bool) bool {
			return fn(&protocol.ProgressEvent{
				RequestID: "request-1",
				Questions: []protocol.AskUserQuestion{{Question: "Continue?"}},
			})
		},
	})
	client := &Client{sendCh: make(chan protocol.WSMessage, 4)}

	runWSReplay(t, wc, client, "web-1", 0)
	select {
	case msg := <-client.sendCh:
		if msg.Type != protocol.MsgTypeAskUser || msg.Progress == nil || msg.Progress.RequestID != "request-1" {
			t.Fatalf("authoritative WS AskUser = %#v", msg)
		}
	default:
		t.Fatal("matching pending AskUser was not restored over WS")
	}
	if len(client.sendCh) != 0 {
		t.Fatalf("WS AskUser restored more than once: %#v", <-client.sendCh)
	}
}

func TestWebSendSkipsAskUserAfterPendingCleared(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	wc.SetCallbacks(WebCallbacks{
		WithPendingAskUser: func(channel, gotChatID string, fn func(*protocol.ProgressEvent) bool) bool {
			return false
		},
	})
	if _, err := wc.Send(ch.OutboundMsg{
		Channel:     "web",
		ChatID:      chatID,
		WaitingUser: true,
		Metadata: map[string]string{
			"request_id":    "request-1",
			"ask_questions": `[{"question":"Continue?"}]`,
		},
	}); err != nil {
		t.Fatal(err)
	}
	for _, event := range wc.replaySSEEvents(SessionSelector{Channel: "web", ChatID: chatID}, 0) {
		if event.Type == protocol.MsgTypeAskUser {
			t.Fatalf("cleared AskUser was published live: %#v", event)
		}
	}
}

func TestRemoteCLISendPublishesStructuredAskUserLive(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "/repo"
	client := &Client{
		connType:       clientConnTypeSSE,
		sendCh:         make(chan protocol.WSMessage, 2),
		done:           make(chan struct{}),
		chatID:         chatID,
		sessionChannel: "cli",
		id:             "remote-cli-ask-user",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, sessionRouteKey("cli", chatID))
	remoteCLI := NewRemoteCLIChannel(wc.hub)

	if _, err := remoteCLI.Send(ch.OutboundMsg{
		Channel:     "cli",
		ChatID:      chatID,
		WaitingUser: true,
		Metadata: map[string]string{
			"request_id":    "request-1",
			"ask_questions": `[{"question":"Continue?","options":["yes","no"]}]`,
		},
	}); err != nil {
		t.Fatal(err)
	}

	<-client.sendCh // ordinary text envelope
	ask := <-client.sendCh
	if ask.Type != protocol.MsgTypeAskUser || ask.Channel != "cli" || ask.ChatID != chatID {
		t.Fatalf("AskUser envelope = %#v", ask)
	}
	if ask.Progress == nil || ask.Progress.RequestID != "request-1" {
		t.Fatalf("AskUser progress = %#v", ask.Progress)
	}
	if len(ask.Progress.Questions) != 1 || ask.Progress.Questions[0].Question != "Continue?" {
		t.Fatalf("AskUser questions = %#v", ask.Progress.Questions)
	}
}

func TestWSWriteBoundarySkipsLiveAskUserClearedAfterEnqueue(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	var pendingMu sync.RWMutex
	pending := &protocol.ProgressEvent{RequestID: "request-1"}
	wc.SetCallbacks(WebCallbacks{
		WithPendingAskUser: func(channel, gotChatID string, fn func(*protocol.ProgressEvent) bool) bool {
			pendingMu.RLock()
			defer pendingMu.RUnlock()
			if pending == nil {
				return false
			}
			copy := *pending
			return fn(&copy)
		},
	})
	client := &Client{
		connType: clientConnTypeWS,
		sendCh:   make(chan protocol.WSMessage, 4),
		done:     make(chan struct{}),
		id:       "browser-ws",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, chatID)
	if _, err := wc.Send(ch.OutboundMsg{
		Channel:     "web",
		ChatID:      chatID,
		WaitingUser: true,
		Metadata:    map[string]string{"request_id": "request-1"},
	}); err != nil {
		t.Fatal(err)
	}
	<-client.sendCh // ordinary text envelope
	ask := <-client.sendCh
	pendingMu.Lock()
	pending = nil
	pendingMu.Unlock()

	writeCalled := false
	written, err := wc.writeCurrentWSMessage(client, ask, func(protocol.WSMessage) error {
		writeCalled = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if written || writeCalled {
		t.Fatal("resolved live AskUser reached the WS writer")
	}
}

func TestWSWriteBoundarySkipsReconnectAskUserClearedAfterEnqueue(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	var pendingMu sync.RWMutex
	pending := &protocol.ProgressEvent{RequestID: "request-1"}
	wc.hub.sendToClient(chatID, protocol.WSMessage{
		Type:     protocol.MsgTypeAskUser,
		Progress: pending,
	})
	wc.SetCallbacks(WebCallbacks{
		WithPendingAskUser: func(channel, gotChatID string, fn func(*protocol.ProgressEvent) bool) bool {
			pendingMu.RLock()
			defer pendingMu.RUnlock()
			if pending == nil {
				return false
			}
			copy := *pending
			return fn(&copy)
		},
	})
	client := &Client{connType: clientConnTypeWS, sendCh: make(chan protocol.WSMessage, 4)}
	runWSReplay(t, wc, client, "web-1", 0)
	ask := <-client.sendCh
	pendingMu.Lock()
	pending = nil
	pendingMu.Unlock()

	writeCalled := false
	written, err := wc.writeCurrentWSMessage(client, ask, func(protocol.WSMessage) error {
		writeCalled = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if written || writeCalled {
		t.Fatal("resolved reconnect AskUser reached the WS writer")
	}
}

func TestWSWriteBoundaryDoesNotHoldPendingLockDuringNetworkWrite(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	var pendingMu sync.RWMutex
	pending := &protocol.ProgressEvent{RequestID: "request-1"}
	wc.SetCallbacks(WebCallbacks{
		WithPendingAskUser: func(channel, chatID string, fn func(*protocol.ProgressEvent) bool) bool {
			pendingMu.RLock()
			defer pendingMu.RUnlock()
			if pending == nil {
				return false
			}
			copy := *pending
			return fn(&copy)
		},
	})
	client := &Client{connType: clientConnTypeWS}
	msg := protocol.WSMessage{
		Type:     protocol.MsgTypeAskUser,
		Channel:  "web",
		ChatID:   "chat-1",
		Progress: &protocol.ProgressEvent{RequestID: "request-1"},
	}

	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		written, err := wc.writeCurrentWSMessage(client, msg, func(protocol.WSMessage) error {
			close(writeStarted)
			<-releaseWrite
			return nil
		})
		if err != nil {
			t.Errorf("write current WS message: %v", err)
		}
		if !written {
			t.Error("current AskUser was not written")
		}
	}()
	<-writeStarted

	clearDone := make(chan struct{})
	go func() {
		pendingMu.Lock()
		pending = nil
		pendingMu.Unlock()
		close(clearDone)
	}()
	select {
	case <-clearDone:
	case <-time.After(time.Second):
		t.Fatal("pending AskUser lock was held during network write")
	}

	close(releaseWrite)
	select {
	case <-writeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("network write did not finish")
	}
}

func runWSReplay(t *testing.T, wc *WebChannel, client *Client, senderID string, lastSeq uint64) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wc.replayMissedEvents(client, senderID)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for client.syncCh.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	syncCh := client.syncCh.Load()
	if syncCh == nil {
		t.Fatal("WS replay did not install sync channel")
	}
	*syncCh <- lastSeq
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WS replay did not finish")
	}
}

func TestSSEDeliversRealAskUserToolMetadata(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	chatID := "web-1"
	var pendingMu sync.RWMutex
	var pending *protocol.ProgressEvent
	wc.SetCallbacks(WebCallbacks{
		WithPendingAskUser: func(channel, gotChatID string, fn func(*protocol.ProgressEvent) bool) bool {
			pendingMu.RLock()
			defer pendingMu.RUnlock()
			if pending == nil {
				return false
			}
			copy := *pending
			copy.Questions = append([]protocol.AskUserQuestion(nil), pending.Questions...)
			return fn(&copy)
		},
	})
	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)
	resp := openSSE(t, server.URL, cookie, chatID, "")
	defer resp.Body.Close()

	toolResult, err := (&tools.AskUserTool{}).Execute(
		&tools.ToolContext{},
		`{"questions":[{"question":"Continue?","options":["yes","no"]}]}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	requestID := toolResult.Metadata["request_id"]
	if requestID == "" {
		t.Fatal("real AskUser metadata has no request ID")
	}
	var questions []protocol.AskUserQuestion
	if err := json.Unmarshal([]byte(toolResult.Metadata["ask_questions"]), &questions); err != nil {
		t.Fatal(err)
	}
	pendingMu.Lock()
	pending = &protocol.ProgressEvent{RequestID: requestID, Questions: questions}
	pendingMu.Unlock()
	if _, err := wc.Send(ch.OutboundMsg{
		Channel:     "web",
		ChatID:      chatID,
		WaitingUser: true,
		Metadata:    toolResult.Metadata,
	}); err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(resp.Body)
	assertSSEMessage(t, readSSEEvent(t, reader), protocol.MsgTypeText, 1)
	ask := assertSSEMessage(t, readSSEEvent(t, reader), protocol.MsgTypeAskUser, 2)
	if ask.Progress == nil || ask.Progress.RequestID != requestID {
		t.Fatalf("AskUser SSE request ID = %#v, want %q", ask.Progress, requestID)
	}
	if len(ask.Progress.Questions) != 1 || ask.Progress.Questions[0].Question != "Continue?" {
		t.Fatalf("AskUser SSE questions = %#v", ask.Progress.Questions)
	}
}

func TestSSEPendingAskUserFallbackPublishesAtomicSnapshot(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	lookupCount := 0
	wc.SetCallbacks(WebCallbacks{
		WithPendingAskUser: func(channel, gotChatID string, fn func(*protocol.ProgressEvent) bool) bool {
			lookupCount++
			return fn(&protocol.ProgressEvent{RequestID: "request-1"})
		},
	})

	wc.publishSSEFallbacks(SessionSelector{Channel: "web", ChatID: chatID}, 0)

	if lookupCount != 1 {
		t.Fatalf("pending AskUser lookup count = %d, want 1", lookupCount)
	}
	events := wc.replaySSEEvents(SessionSelector{Channel: "web", ChatID: chatID}, 0)
	if len(events) != 1 || askUserRequestID(events[0]) != "request-1" {
		t.Fatalf("published AskUser events = %#v", events)
	}
}

func TestSSEPendingAskUserPublicationLinearizesBeforeConcurrentClear(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	var pendingMu sync.RWMutex
	pending := &protocol.ProgressEvent{RequestID: "request-1"}
	withPendingEntered := make(chan struct{})
	wc.SetCallbacks(WebCallbacks{
		WithPendingAskUser: func(channel, gotChatID string, fn func(*protocol.ProgressEvent) bool) bool {
			pendingMu.RLock()
			defer pendingMu.RUnlock()
			if pending == nil {
				return false
			}
			copy := *pending
			close(withPendingEntered)
			return fn(&copy)
		},
	})

	wc.hub.seqMu.Lock()
	publishDone := make(chan struct{})
	go func() {
		wc.publishSSEFallbacks(SessionSelector{Channel: "web", ChatID: chatID}, 0)
		close(publishDone)
	}()
	<-withPendingEntered

	clearDone := make(chan struct{})
	go func() {
		pendingMu.Lock()
		pending = nil
		pendingMu.Unlock()
		close(clearDone)
	}()
	select {
	case <-clearDone:
		t.Fatal("pending AskUser clear overtook an in-flight publication")
	case <-time.After(50 * time.Millisecond):
	}

	wc.hub.seqMu.Unlock()
	select {
	case <-publishDone:
	case <-time.After(2 * time.Second):
		t.Fatal("pending AskUser publication did not finish")
	}
	select {
	case <-clearDone:
	case <-time.After(2 * time.Second):
		t.Fatal("pending AskUser clear did not finish after publication")
	}
	if got := wc.getEventStream(chatID).lastSeq(); got != 1 {
		t.Fatalf("last sequence = %d, want 1 for publication ordered before clear", got)
	}
}

func TestSSEReconnectSkipsResolvedRetainedAskUser(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	chatID := "web-1"
	wc.hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "before"})
	wc.hub.sendToClient(chatID, protocol.WSMessage{
		Type:     protocol.MsgTypeAskUser,
		ChatID:   chatID,
		Progress: &protocol.ProgressEvent{RequestID: "request-1"},
	})
	wc.SetCallbacks(WebCallbacks{
		WithPendingAskUser: func(channel, gotChatID string, fn func(*protocol.ProgressEvent) bool) bool {
			return false
		},
	})

	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)
	resp := openSSE(t, server.URL, cookie, chatID, "1")
	defer resp.Body.Close()
	wc.hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "after"})

	msg := assertSSEMessage(t, readSSEEvent(t, bufio.NewReader(resp.Body)), protocol.MsgTypeText, 3)
	if msg.Content != "after" {
		t.Fatalf("first reconnect event content = %q, want after", msg.Content)
	}
}

func TestSSEReconnectReplaysMatchingPendingAskUser(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	chatID := "web-1"
	wc.hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "before"})
	wc.hub.sendToClient(chatID, protocol.WSMessage{
		Type:     protocol.MsgTypeAskUser,
		ChatID:   chatID,
		Progress: &protocol.ProgressEvent{RequestID: "request-1"},
	})
	wc.SetCallbacks(WebCallbacks{
		WithPendingAskUser: func(channel, gotChatID string, fn func(*protocol.ProgressEvent) bool) bool {
			return fn(&protocol.ProgressEvent{RequestID: "request-1"})
		},
	})

	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)
	resp := openSSE(t, server.URL, cookie, chatID, "1")
	defer resp.Body.Close()

	msg := assertSSEMessage(t, readSSEEvent(t, bufio.NewReader(resp.Body)), protocol.MsgTypeAskUser, 2)
	if askUserRequestID(msg) != "request-1" {
		t.Fatalf("replayed AskUser request ID = %q, want request-1", askUserRequestID(msg))
	}
}

func TestSSEActiveProgressFallbackStopsAtIdleEvent(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	lookupStarted := make(chan struct{})
	releaseLookup := make(chan struct{})
	lookupCount := 0
	wc.SetCallbacks(WebCallbacks{
		GetActiveProgress: func(channel, gotChatID string) *protocol.ProgressEvent {
			lookupCount++
			if lookupCount == 1 {
				close(lookupStarted)
				<-releaseLookup
			}
			return &protocol.ProgressEvent{Phase: "thinking"}
		},
	})

	done := make(chan struct{})
	go func() {
		wc.publishSSEFallbacks(SessionSelector{Channel: "web", ChatID: chatID}, 0)
		close(done)
	}()
	<-lookupStarted
	wc.SendSessionState(protocol.SessionEvent{Channel: "web", ChatID: chatID, Action: "idle"})
	close(releaseLookup)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("active progress fallback did not finish")
	}

	if lookupCount != 2 {
		t.Fatalf("active progress lookup count = %d, want 2 after terminal event", lookupCount)
	}
	events := wc.replaySSEEvents(SessionSelector{Channel: "web", ChatID: chatID}, 0)
	if len(events) != 1 || events[0].Type != protocol.MsgTypeSession || events[0].Session == nil || events[0].Session.Action != "idle" {
		t.Fatalf("events after terminal handoff = %#v", events)
	}
}

func TestSSEActiveProgressFallbackRevalidatesSnapshot(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	lookupCount := 0
	wc.SetCallbacks(WebCallbacks{
		GetActiveProgress: func(channel, chatID string) *protocol.ProgressEvent {
			lookupCount++
			if lookupCount == 1 {
				return &protocol.ProgressEvent{Phase: "thinking"}
			}
			return nil
		},
	})

	wc.publishSSEFallbacks(SessionSelector{Channel: "web", ChatID: "web-1"}, 0)

	if lookupCount != 2 {
		t.Fatalf("active progress lookup count = %d, want 2", lookupCount)
	}
	if got := wc.getEventStream("web-1").lastSeq(); got != 0 {
		t.Fatalf("last sequence = %d, want 0 for stale progress fallback", got)
	}
}

func TestSSEActiveProgressFallbackHonorsIdleAtHighWater(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	wc.SendSessionState(protocol.SessionEvent{Channel: "web", ChatID: chatID, Action: "idle"})
	lookupCount := 0
	wc.SetCallbacks(WebCallbacks{
		GetActiveProgress: func(channel, gotChatID string) *protocol.ProgressEvent {
			lookupCount++
			return &protocol.ProgressEvent{Phase: "thinking"}
		},
	})

	wc.publishSSEFallbacks(SessionSelector{Channel: "web", ChatID: chatID}, 1)

	if lookupCount != 2 {
		t.Fatalf("active progress lookup count = %d, want 2 at idle high-water mark", lookupCount)
	}
	if got := wc.getEventStream(chatID).lastSeq(); got != 1 {
		t.Fatalf("last sequence = %d, want terminal sequence 1", got)
	}
}

//nolint:staticcheck // The empty critical section intentionally verifies lock acquisition without deadlock.
func TestSSEProgressFallbackDoesNotHoldSequenceLockDuringCallback(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	var stateMu sync.Mutex
	stateLocked := make(chan struct{})
	emitIdle := make(chan struct{})
	completionDone := make(chan struct{})
	go func() {
		stateMu.Lock()
		close(stateLocked)
		<-emitIdle
		wc.SendSessionState(protocol.SessionEvent{Channel: "web", ChatID: chatID, Action: "idle"})
		stateMu.Unlock()
		close(completionDone)
	}()
	<-stateLocked

	secondLookup := make(chan struct{})
	lookupCount := 0
	wc.SetCallbacks(WebCallbacks{
		GetActiveProgress: func(channel, gotChatID string) *protocol.ProgressEvent {
			lookupCount++
			if lookupCount == 2 {
				close(secondLookup)
				stateMu.Lock()
				stateMu.Unlock()
			}
			return &protocol.ProgressEvent{Phase: "thinking"}
		},
	})

	publishDone := make(chan struct{})
	go func() {
		wc.publishSSEFallbacks(SessionSelector{Channel: "web", ChatID: chatID}, 0)
		close(publishDone)
	}()
	<-secondLookup
	close(emitIdle)
	for name, done := range map[string]<-chan struct{}{
		"completion": completionDone,
		"fallback":   publishDone,
	} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s path deadlocked", name)
		}
	}
	if got := wc.getEventStream(chatID).lastSeq(); got != 1 {
		t.Fatalf("last sequence = %d, want idle sequence 1", got)
	}
}

func TestSSEProgressFallbackUsesEventPublishedBeforeSnapshotStore(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	wc.SendProgress(chatID, &protocol.ProgressEvent{Seq: 2, Phase: "new"})
	client := &Client{
		connType:       clientConnTypeSSE,
		sendCh:         make(chan protocol.WSMessage, 1),
		statelessSig:   make(chan struct{}, 1),
		done:           make(chan struct{}),
		chatID:         chatID,
		sessionChannel: "web",
		id:             "sse-progress-order",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, sessionRouteKey("web", chatID))
	wc.SetCallbacks(WebCallbacks{
		GetActiveProgress: func(channel, gotChatID string) *protocol.ProgressEvent {
			return &protocol.ProgressEvent{Seq: 1, Phase: "old"}
		},
	})

	wc.publishSSEFallbacks(SessionSelector{Channel: "web", ChatID: chatID}, 1)

	msgs := drainProgress(t, client)
	if len(msgs) != 1 || msgs[0].Seq != 2 || msgs[0].Progress == nil || msgs[0].Progress.Seq != 2 || msgs[0].Progress.Phase != "new" {
		t.Fatalf("progress fallback = %#v", msgs)
	}
}

func TestSSEFallbackIsSharedBySubscribedClients(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	clients := []*Client{
		{connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 4), statelessSig: make(chan struct{}, 1), done: make(chan struct{}), id: "sse-1", chatID: chatID, sessionChannel: "web"},
		{connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 4), statelessSig: make(chan struct{}, 1), done: make(chan struct{}), id: "sse-2", chatID: chatID, sessionChannel: "web"},
	}
	for _, client := range clients {
		wc.hub.addClient(client.id, client)
		wc.hub.subscribe(client.id, sessionRouteKey("web", chatID))
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
		progress := drainProgress(t, client)
		if len(progress) != 1 || progress[0].Type != protocol.MsgTypeProgress || progress[0].Seq != 1 {
			t.Fatalf("client %s fallback = %#v", client.id, progress)
		}
		if extra := drainProgress(t, client); len(extra) > 0 {
			t.Fatalf("client %s received duplicate fallback: %#v", client.id, extra)
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

func TestSSEFallbackCheckAndPublishIsAtomicWithLiveEvent(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	client := &Client{
		connType:     clientConnTypeSSE,
		sendCh:       make(chan protocol.WSMessage, 4),
		statelessSig: make(chan struct{}, 1),
		done:         make(chan struct{}),
		id:           "sse-atomic",
		chatID:       chatID,
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, chatID)

	checkComplete := make(chan struct{})
	releasePublish := make(chan struct{})
	fallbackDone := make(chan struct{})
	go func() {
		wc.hub.sendSSEEventIf(chatID, func() (protocol.WSMessage, bool) {
			shouldPublish := !containsSSEEvent(
				wc.replaySSEEvents(SessionSelector{Channel: "web", ChatID: chatID}, 0),
				protocol.MsgTypeProgress,
				"",
			)
			close(checkComplete)
			<-releasePublish
			return protocol.WSMessage{
				Type:     protocol.MsgTypeProgress,
				Progress: &protocol.ProgressEvent{Phase: "fallback"},
			}, shouldPublish
		})
		close(fallbackDone)
	}()
	<-checkComplete
	if wc.hub.seqMu.TryLock() {
		wc.hub.seqMu.Unlock()
		t.Fatal("sequence lock was released between fallback check and publish")
	}

	liveStarted := make(chan struct{})
	liveDone := make(chan struct{})
	go func() {
		close(liveStarted)
		wc.SendProgress(chatID, &protocol.ProgressEvent{Phase: "live"})
		close(liveDone)
	}()
	<-liveStarted
	close(releasePublish)
	for name, done := range map[string]<-chan struct{}{
		"fallback": fallbackDone,
		"live":     liveDone,
	} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s publish did not finish", name)
		}
	}

	// Progress events are stateless (latest wins in the stateless slot), but
	// both are retained in the eventStream for SSE replay. Verify via replay.
	events := wc.replaySSEEvents(SessionSelector{Channel: "web", ChatID: chatID}, 0)
	var progressEvents []protocol.WSMessage
	for _, e := range events {
		if e.Type == protocol.MsgTypeProgress {
			progressEvents = append(progressEvents, e)
		}
	}
	if len(progressEvents) != 2 {
		t.Fatalf("expected 2 progress events in replay, got %d: %#v", len(progressEvents), progressEvents)
	}
	fallback := progressEvents[0]
	live := progressEvents[1]
	if fallback.Seq != 1 || fallback.Progress == nil || fallback.Progress.Phase != "fallback" {
		t.Fatalf("fallback event = %#v", fallback)
	}
	if live.Seq != 2 || live.Progress == nil || live.Progress.Phase != "live" {
		t.Fatalf("live event = %#v", live)
	}
}

func TestHubOfflineSequencingPreservesWebAndCLIContracts(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	tests := []struct {
		name    string
		chatID  string
		isCLI   bool
		wantSeq uint64
	}{
		{name: "web websocket", chatID: "web-1", wantSeq: 1},
		{name: "CLI websocket", chatID: "/workspace/cli-1", isCLI: true, wantSeq: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wc.hub.sendToClient(tt.chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "offline"})
			client := &Client{
				connType: clientConnTypeWS,
				sendCh:   make(chan protocol.WSMessage, 1),
				done:     make(chan struct{}),
				id:       tt.name,
				isCLI:    tt.isCLI,
			}
			wc.hub.addClient(client.id, client)
			wc.hub.subscribe(client.id, tt.chatID)

			msg := <-client.sendCh
			if msg.Seq != tt.wantSeq {
				t.Fatalf("offline sequence = %d, want %d", msg.Seq, tt.wantSeq)
			}
		})
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
		connType:     clientConnTypeSSE,
		sendCh:       make(chan protocol.WSMessage, 16),
		statelessSig: make(chan struct{}, 1),
		done:         make(chan struct{}),
		id:           "sse-contract",
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
		outbound := protocol.WSMessage{Type: msgType}
		if msgType == protocol.MsgTypeProgress {
			outbound.Progress = &protocol.ProgressEvent{Phase: "thinking"}
		}
		if !wc.hub.sendToClient("web-1", outbound) {
			t.Fatalf("message %q was not delivered", msgType)
		}
		// Progress, stream_content, sync_progress, runner_status are stateless
		// (latest wins) — drain from the stateless slot. Others use sendCh.
		var msg protocol.WSMessage
		if !isStatefulMsg(outbound) || msgType == protocol.MsgTypeProgress {
			msgs := drainStatelessMsgs(t, client)
			if len(msgs) != 1 {
				t.Fatalf("message %q: expected 1 stateless event, got %d", msgType, len(msgs))
			}
			msg = msgs[0]
		} else {
			msg = <-client.sendCh
		}
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

func TestSSESessionBroadcastReachesAllClients(t *testing.T) {
	// Session state events (busy/idle/renamed/deleted) affect the global
	// session sidebar — they must reach ALL connected clients, not just
	// those subscribed to the event's session. This is the fix for the
	// sidebar not showing busy state until manual refresh.
	wc, _ := newTestWebChannel(t, nil)
	client1 := &Client{connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "user-1", userID: "web-1"}
	client2 := &Client{connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "user-2", userID: "web-2"}
	wsClient := &Client{connType: clientConnTypeWS, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "browser-ws"}
	cliClient := &Client{connType: clientConnTypeWS, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "cli-ws", isCLI: true}
	for _, client := range []*Client{client1, client2, wsClient, cliClient} {
		wc.hub.addClient(client.id, client)
	}
	// Only client1 subscribes to "web-1" — client2 does NOT.
	wc.hub.subscribe(client1.id, "web-1")

	wc.SendSessionState(protocol.SessionEvent{Channel: "web", ChatID: "web-1", Action: "busy"})

	// client1 (subscribed) must receive the event
	var sseMsg protocol.WSMessage
	select {
	case sseMsg = <-client1.sendCh:
		if sseMsg.Type != protocol.MsgTypeSession || sseMsg.Seq == 0 {
			t.Fatalf("user-1 session event = %#v", sseMsg)
		}
	default:
		t.Fatal("subscribed SSE client did not receive session event")
	}
	// client2 (NOT subscribed) must ALSO receive the event — this is the fix
	select {
	case msg := <-client2.sendCh:
		if msg.Type != protocol.MsgTypeSession {
			t.Fatalf("user-2 session event type = %#v", msg)
		}
	default:
		t.Fatal("unsubscribed SSE client did not receive session event (sidebar won't update)")
	}
	// WS clients must also receive it
	select {
	case msg := <-wsClient.sendCh:
		if msg.Seq != sseMsg.Seq {
			t.Fatalf("browser WS session seq = %d, SSE seq = %d", msg.Seq, sseMsg.Seq)
		}
	default:
		t.Fatal("browser WS client did not receive session event")
	}
	// CLI clients receive unsequenced messages
	select {
	case msg := <-cliClient.sendCh:
		if msg.Seq != 0 {
			t.Fatalf("CLI WS session seq = %d, want 0", msg.Seq)
		}
	default:
		t.Fatal("CLI WS client did not receive session event")
	}
}

func TestWebChannelStopInterruptsBlockedSSEWrite(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	writer := newDeadlineBlockingResponseWriter()
	defer writer.release()
	client := &Client{
		connType: clientConnTypeSSE,
		w:        writer,
		flusher:  writer,
		sendCh:   make(chan protocol.WSMessage, 1),
		done:     make(chan struct{}),
		chatID:   "web-1",
		id:       "blocked-sse",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, client.chatID)
	wc.hub.sendToClient(client.chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "blocked"})

	wc.wg.Add(1)
	go func() {
		defer wc.wg.Done()
		wc.sseWriteLoop(context.Background(), client)
	}()
	select {
	case <-writer.writeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE write did not block")
	}

	stopped := make(chan struct{})
	go func() {
		wc.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("WebChannel.Stop did not interrupt blocked SSE write")
	}
}

func TestSSEHandlerCannotRegisterAfterStop(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)
	wc.Stop()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/sse?chat_id=web-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookie)
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("stopped SSE handler did not return: %v", err)
	}
	resp.Body.Close()

	wc.hub.mu.RLock()
	connections := len(wc.hub.conns)
	wc.hub.mu.RUnlock()
	if connections != 0 {
		t.Fatalf("Hub connections after Stop = %d, want 0", connections)
	}
}

func TestSSECancelledDeadlineCannotBeRearmed(t *testing.T) {
	writer := newDeadlineBlockingResponseWriter()
	defer writer.release()
	client := &Client{w: writer}
	client.sseWriteCanceled.Store(true)

	armSSEWriteDeadline(client)

	select {
	case <-writer.unblock:
	case <-time.After(time.Second):
		t.Fatal("cancelled SSE write was rearmed with a future deadline")
	}
}

type deadlineBlockingResponseWriter struct {
	header       http.Header
	writeStarted chan struct{}
	unblock      chan struct{}
	startOnce    sync.Once
	unblockOnce  sync.Once
}

func newDeadlineBlockingResponseWriter() *deadlineBlockingResponseWriter {
	return &deadlineBlockingResponseWriter{
		header:       make(http.Header),
		writeStarted: make(chan struct{}),
		unblock:      make(chan struct{}),
	}
}

func (w *deadlineBlockingResponseWriter) Header() http.Header { return w.header }

func (w *deadlineBlockingResponseWriter) Write([]byte) (int, error) {
	w.startOnce.Do(func() { close(w.writeStarted) })
	<-w.unblock
	return 0, context.Canceled
}

func (w *deadlineBlockingResponseWriter) WriteHeader(int) {}

func (w *deadlineBlockingResponseWriter) Flush() {}

func (w *deadlineBlockingResponseWriter) SetWriteDeadline(deadline time.Time) error {
	if !deadline.IsZero() && time.Until(deadline) < sseWriteTimeout/2 {
		w.release()
	}
	return nil
}

func (w *deadlineBlockingResponseWriter) release() {
	w.unblockOnce.Do(func() { close(w.unblock) })
}

func TestHubUsesSequencedCopyWithoutChangingCLIWSMessage(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	sseClient := &Client{connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 1), statelessSig: make(chan struct{}, 1), done: make(chan struct{}), id: "sse"}
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

func TestHubNormalizesSparseProgressOnlyForSSE(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "shared-chat"
	sseClient := &Client{connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "sse"}
	webSocketClient := &Client{connType: clientConnTypeWS, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "web-ws", statelessSig: make(chan struct{}, 1)}
	cliClient := &Client{connType: clientConnTypeWS, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "cli-ws", isCLI: true, statelessSig: make(chan struct{}, 1)}
	for _, client := range []*Client{sseClient, webSocketClient, cliClient} {
		wc.hub.addClient(client.id, client)
		wc.hub.subscribe(client.id, chatID)
	}

	wc.SendProgress(chatID, &protocol.ProgressEvent{StreamContent: "partial"})

	sseMsgs := drainStatelessMsgs(t, sseClient)
	if len(sseMsgs) != 1 || sseMsgs[0].Type != protocol.MsgTypeStreamContent || sseMsgs[0].Seq != 1 {
		t.Fatalf("SSE sparse progress = %#v", sseMsgs)
	}
	webMsgs := webSocketClient.drainStateless()
	if len(webMsgs) != 1 || webMsgs[0].Type != protocol.MsgTypeProgress || webMsgs[0].Seq != 1 {
		t.Fatalf("browser WS sparse progress = %#v", webMsgs)
	}
	cliMsgs := cliClient.drainStateless()
	if len(cliMsgs) != 1 || cliMsgs[0].Type != protocol.MsgTypeProgress || cliMsgs[0].Seq != 0 {
		t.Fatalf("CLI WS sparse progress = %#v", cliMsgs)
	}
}

func TestNormalizeSSEEventOnlyConvertsStreamOnlyProgress(t *testing.T) {
	streamOnly := []struct {
		name     string
		progress *protocol.ProgressEvent
	}{
		{name: "content delta", progress: &protocol.ProgressEvent{StreamContent: "partial"}},
		{name: "reasoning delta", progress: &protocol.ProgressEvent{ReasoningStreamContent: "thinking"}},
		{name: "streaming tools", progress: &protocol.ProgressEvent{StreamingTools: []protocol.ToolProgress{{Name: "Read"}}}},
		{name: "stream tokens", progress: &protocol.ProgressEvent{StreamTokens: 12}},
		{
			name: "stream identifiers are allowed",
			progress: &protocol.ProgressEvent{
				ChatID:        "agent:worker",
				Seq:           7,
				StreamContent: "partial",
				StreamTokens:  12,
			},
		},
	}
	for _, tt := range streamOnly {
		t.Run(tt.name, func(t *testing.T) {
			msg := normalizeSSEEvent(protocol.WSMessage{Type: protocol.MsgTypeProgress, Progress: tt.progress})
			if msg.Type != protocol.MsgTypeStreamContent {
				t.Fatalf("normalized type = %q, want %q", msg.Type, protocol.MsgTypeStreamContent)
			}
		})
	}

	structured := []struct {
		name   string
		mutate func(*protocol.ProgressEvent)
	}{
		{name: "iteration", mutate: func(p *protocol.ProgressEvent) { p.Iteration = 1 }},
		{name: "content", mutate: func(p *protocol.ProgressEvent) { p.Content = "complete" }},
		{name: "reasoning", mutate: func(p *protocol.ProgressEvent) { p.Reasoning = "complete" }},
		{name: "tool calls", mutate: func(p *protocol.ProgressEvent) { p.ToolCalls = []protocol.ToolCallSnapshot{{ID: "tool-1"}} }},
		{name: "elapsed wall", mutate: func(p *protocol.ProgressEvent) { p.ElapsedWall = 1 }},
		{name: "phase", mutate: func(p *protocol.ProgressEvent) { p.Phase = "thinking" }},
		{name: "active tools", mutate: func(p *protocol.ProgressEvent) { p.ActiveTools = []protocol.ToolProgress{{Name: "Read"}} }},
		{name: "completed tools", mutate: func(p *protocol.ProgressEvent) { p.CompletedTools = []protocol.ToolProgress{{Name: "Read"}} }},
		{name: "subagents", mutate: func(p *protocol.ProgressEvent) { p.SubAgents = []protocol.SubAgentInfo{{Role: "reviewer"}} }},
		{name: "todos", mutate: func(p *protocol.ProgressEvent) { p.Todos = []protocol.TodoItem{{ID: 1}} }},
		{name: "token usage", mutate: func(p *protocol.ProgressEvent) { p.TokenUsage = &protocol.TokenUsage{} }},
		{name: "questions", mutate: func(p *protocol.ProgressEvent) { p.Questions = []protocol.AskUserQuestion{{Question: "Continue?"}} }},
		{name: "request id", mutate: func(p *protocol.ProgressEvent) { p.RequestID = "request-1" }},
		{name: "iteration history", mutate: func(p *protocol.ProgressEvent) { p.IterationHistory = []protocol.ProgressEvent{{Iteration: 1}} }},
		{name: "history compacted", mutate: func(p *protocol.ProgressEvent) { p.HistoryCompacted = true }},
		{name: "cwd", mutate: func(p *protocol.ProgressEvent) { p.CWD = "/workspace" }},
	}
	for _, tt := range structured {
		t.Run(tt.name, func(t *testing.T) {
			progress := &protocol.ProgressEvent{StreamContent: "partial"}
			tt.mutate(progress)
			msg := normalizeSSEEvent(protocol.WSMessage{Type: protocol.MsgTypeProgress, Progress: progress})
			if msg.Type != protocol.MsgTypeProgress {
				t.Fatalf("normalized type = %q, want %q", msg.Type, protocol.MsgTypeProgress)
			}
		})
	}

	for _, progress := range []*protocol.ProgressEvent{nil, {}} {
		msg := normalizeSSEEvent(protocol.WSMessage{Type: protocol.MsgTypeProgress, Progress: progress})
		if msg.Type != protocol.MsgTypeProgress {
			t.Fatalf("empty progress normalized type = %q, want %q", msg.Type, protocol.MsgTypeProgress)
		}
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

func TestSSEResumeCursorPrefersHeaderOverQuery(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		header     *string
		wantSeq    uint64
		wantCursor bool
		wantErr    bool
	}{
		{name: "no cursor", target: "/api/sse?chat_id=web-1"},
		{name: "query cursor", target: "/api/sse?chat_id=web-1&last_event_id=7", wantSeq: 7, wantCursor: true},
		{name: "invalid query cursor", target: "/api/sse?chat_id=web-1&last_event_id=bad", wantCursor: true, wantErr: true},
		{name: "header takes priority", target: "/api/sse?chat_id=web-1&last_event_id=7", header: stringPointer("9"), wantSeq: 9, wantCursor: true},
		{name: "empty header still takes priority", target: "/api/sse?chat_id=web-1&last_event_id=7", header: stringPointer(""), wantCursor: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			if tt.header != nil {
				req.Header["Last-Event-Id"] = []string{*tt.header}
			}
			seq, hasCursor, err := sseResumeCursor(req)
			if (err != nil) != tt.wantErr || seq != tt.wantSeq || hasCursor != tt.wantCursor {
				t.Fatalf("sseResumeCursor() = (%d, %v, %v), want (%d, %v, err=%v)", seq, hasCursor, err, tt.wantSeq, tt.wantCursor, tt.wantErr)
			}
		})
	}
}

func TestSSEExplicitChannelOverridesStaleCurrentSession(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	if _, err := db.Exec(
		"INSERT INTO tenants (channel, chat_id, last_active_at) VALUES (?, ?, ?)",
		"cli", "/repo:Agent-main", time.Now().Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := authedAPIRequest(http.MethodGet, "/api/sse?chat_id=%2Frepo%3AAgent-main&channel=cli", nil)
	sel, ok := wc.resolveSSESession(recorder, request, "web-1", "/repo:Agent-main")
	if !ok || sel.Channel != "cli" || sel.ChatID != "/repo:Agent-main" {
		t.Fatalf("resolved selector=%#v ok=%v status=%d body=%s", sel, ok, recorder.Code, recorder.Body.String())
	}
}

func TestSSEAllowsCanonicalOwnerForCLIAndNestedAgentSessions(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.SetCallbacks(WebCallbacks{
		IdentityResolver: fixedIdentityResolver{userID: 42, role: "user"},
	})
	for _, tenant := range []struct {
		channel string
		chatID  string
		owner   int64
	}{
		{channel: "cli", chatID: "owned-cli", owner: 42},
		{channel: "agent", chatID: "cli:owned-cli/review:1", owner: 42},
		{channel: "agent", chatID: "agent:cli:owned-cli/review:1/fix:2", owner: 42},
		{channel: "cli", chatID: "foreign-cli", owner: 99},
		{channel: "agent", chatID: "cli:foreign-cli/review:1", owner: 99},
		{channel: "agent", chatID: "agent:cli:foreign-cli/review:1/fix:2", owner: 99},
	} {
		if _, err := db.Exec(
			"INSERT INTO tenants (channel, chat_id, owner_user_id, last_active_at) VALUES (?, ?, ?, ?)",
			tenant.channel, tenant.chatID, tenant.owner, time.Now().Format(time.RFC3339),
		); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name    string
		channel string
		chatID  string
		want    bool
	}{
		{name: "owned CLI", channel: "cli", chatID: "owned-cli", want: true},
		{name: "owned CLI-rooted agent", channel: "agent", chatID: "cli:owned-cli/review:1", want: true},
		{name: "owned nested CLI-rooted agent", channel: "agent", chatID: "agent:cli:owned-cli/review:1/fix:2", want: true},
		{name: "foreign CLI", channel: "cli", chatID: "foreign-cli"},
		{name: "foreign CLI-rooted agent", channel: "agent", chatID: "cli:foreign-cli/review:1"},
		{name: "foreign nested CLI-rooted agent", channel: "agent", chatID: "agent:cli:foreign-cli/review:1/fix:2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			target := "/api/sse?channel=" + url.QueryEscape(tc.channel) + "&chat_id=" + url.QueryEscape(tc.chatID)
			request := authedAPIRequestFor(http.MethodGet, target, nil, "web-2", 2)
			_, ok := wc.resolveSSESession(recorder, request, "web-2", tc.chatID)
			if ok != tc.want {
				t.Fatalf("resolveSSESession ok=%v, want %v; status=%d body=%s", ok, tc.want, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func stringPointer(value string) *string {
	return &value
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

func TestSSEAllowsAdminToExistingForeignWebSession(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)
	adminCookie := loginTestAdmin(t, server.URL)
	_ = loginTestWebUser(t, server.URL, "member")
	if _, err := db.Exec("INSERT INTO tenants (channel, chat_id) VALUES (?, ?)", "web", "web-2"); err != nil {
		t.Fatal(err)
	}

	resp := openSSE(t, server.URL, adminCookie, "web-2", "")
	defer resp.Body.Close()
	wc.hub.sendToClient("web-2", protocol.WSMessage{Type: protocol.MsgTypeText, Content: "admin view"})
	msg := assertSSEMessage(t, readSSEEvent(t, bufio.NewReader(resp.Body)), protocol.MsgTypeText, 1)
	if msg.Content != "admin view" {
		t.Fatalf("admin SSE content = %q, want admin view", msg.Content)
	}
}

func TestSSEDeniesOrdinaryUserForeignAndAdminMissingWebSessions(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)
	adminCookie := loginTestAdmin(t, server.URL)
	memberCookie := loginTestWebUser(t, server.URL, "member")
	if _, err := db.Exec("INSERT INTO tenants (channel, chat_id) VALUES (?, ?)", "web", "web-1"); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
		chatID string
	}{
		{name: "ordinary user foreign session", cookie: memberCookie, chatID: "web-1"},
		{name: "admin missing session", cookie: adminCookie, chatID: "web-999"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, server.URL+"/api/sse?chat_id="+tc.chatID, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.AddCookie(tc.cookie)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("SSE status = %d, want 403: %s", resp.StatusCode, body)
			}
		})
	}
}

func loginTestWebUser(t *testing.T, serverURL, username string) *http.Cookie {
	t.Helper()
	registerResp, err := http.Post(
		serverURL+"/api/auth/register",
		"application/json",
		strings.NewReader(`{"username":"`+username+`","password":"pw"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	registerResp.Body.Close()
	if registerResp.StatusCode != http.StatusOK {
		t.Fatalf("register %s status = %d, want 200", username, registerResp.StatusCode)
	}

	loginResp, err := http.Post(
		serverURL+"/api/auth/login",
		"application/json",
		strings.NewReader(`{"username":"`+username+`","password":"pw"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("login %s status = %d", username, loginResp.StatusCode)
	}
	for _, cookie := range loginResp.Cookies() {
		if cookie.Name == webSessionCookieName {
			return cookie
		}
	}
	t.Fatalf("login %s returned no session cookie", username)
	return nil
}

// drainStatelessMsgs drains the stateless slot and returns all messages,
// sorted by seq. Use this instead of <-client.sendCh for stateless event
// types (progress, stream_content, sync_progress, runner_status).
func drainStatelessMsgs(t *testing.T, client *Client) []protocol.WSMessage {
	t.Helper()
	msgs := client.drainStateless()
	out := make([]protocol.WSMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// drainProgress drains the stateless slot and returns all progress messages,
// sorted by seq. Use this instead of <-client.sendCh for progress events —
// progress_structured is stateless (latest wins), delivered via statelessMap.
func drainProgress(t *testing.T, client *Client) []protocol.WSMessage {
	t.Helper()
	all := drainStatelessMsgs(t, client)
	var progress []protocol.WSMessage
	for _, m := range all {
		if m.Type == protocol.MsgTypeProgress {
			progress = append(progress, m)
		}
	}
	return progress
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
	for {
		fields := readSSEFrame(t, reader)
		if _, ok := fields["event"]; ok {
			return fields
		}
	}
}

func readSSEFrame(t *testing.T, reader *bufio.Reader) map[string]string {
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

// ---------------------------------------------------------------------------
// SSE packet-drop consistency tests
//
// These tests prove that dropping ANY middle SSE packet still results in
// linearly consistent, correct history with no lost messages. The guarantee
// rests on three mechanisms:
//   1. Monotonic seq — every event gets a unique increasing seq (stampAndBuffer)
//   2. Ring buffer — events are retained (eventStream, 512 slots) for replay
//   3. Dedup on write — writeSSEEvent skips events with seq <= lastSentSeq
//
// If a live event is dropped (sendCh full, network glitch), the ring buffer
// still has it. On reconnect, the client sends Last-Event-ID = last received
// seq, and the server replays all events with seq > lastSeq.
// ---------------------------------------------------------------------------

// publishMixedEvents publishes a deterministic sequence of mixed-type events
// and returns the expected (type, content) pairs in publication order.
func publishMixedEvents(wc *WebChannel, chatID string, source string) []struct {
	seq     uint64
	typ     string
	content string
} {
	var expected []struct {
		seq     uint64
		typ     string
		content string
	}

	publish := func(typ, content string) {
		expected = append(expected, struct {
			seq     uint64
			typ     string
			content string
		}{uint64(len(expected) + 1), typ, content})
	}

	// 1. stream_content (stateless, coalesced in ring buffer)
	wc.SendProgress(chatID, &protocol.ProgressEvent{ChatID: source, StreamContent: "stream-1"})
	publish("stream_content", "stream-1")

	// 2. progress_structured (stateful — merge boundary for stream_content)
	wc.SendProgress(chatID, &protocol.ProgressEvent{ChatID: source, Phase: "thinking", Iteration: 1})
	publish("progress_structured", "thinking")

	// 3. text — final assistant message (stateful)
	wc.hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "answer-1"})
	publish("text", "answer-1")

	// 4. session — busy (stateful)
	wc.SendSessionState(protocol.SessionEvent{Channel: "web", ChatID: chatID, Action: "busy"})
	publish("session", "busy")

	// 5. stream_content again (after stateful boundary — NOT coalesced with #1)
	wc.SendProgress(chatID, &protocol.ProgressEvent{ChatID: source, StreamContent: "stream-2"})
	publish("stream_content", "stream-2")

	// 6. text — second answer
	wc.hub.sendToClient(chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "answer-2"})
	publish("text", "answer-2")

	return expected
}

// eventContent extracts a human-readable content string from a WSMessage for
// assertion purposes.
func eventContent(msg protocol.WSMessage) string {
	switch msg.Type {
	case protocol.MsgTypeText:
		return msg.Content
	case protocol.MsgTypeProgress:
		return msg.Progress.Phase
	case protocol.MsgTypeStreamContent:
		return msg.Progress.StreamContent
	case protocol.MsgTypeSession:
		return msg.Session.Action
	}
	return ""
}

// TestSSEDropAnyMiddleEventReplaysAll verifies that for EVERY possible drop
// position, the ring buffer replay recovers all remaining events in correct
// seq order with correct content.
func TestSSEDropAnyMiddleEventReplaysAll(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-drop"
	source := "web:" + chatID
	expected := publishMixedEvents(wc, chatID, source)
	sel := SessionSelector{Channel: "web", ChatID: chatID}

	for dropAfter := 0; dropAfter < len(expected); dropAfter++ {
		t.Run("drop_after_"+strconv.Itoa(dropAfter), func(t *testing.T) {
			replayed := wc.replaySSEEvents(sel, uint64(dropAfter))
			wantCount := len(expected) - dropAfter
			if len(replayed) != wantCount {
				t.Fatalf("dropAfter=%d: got %d events, want %d", dropAfter, len(replayed), wantCount)
			}

			for i, ev := range replayed {
				wantSeq := uint64(dropAfter + i + 1)
				if ev.Seq != wantSeq {
					t.Fatalf("event %d: seq=%d, want %d (not monotonic)", i, ev.Seq, wantSeq)
				}
				wantType := expected[dropAfter+i].typ
				if ev.Type != wantType {
					t.Fatalf("event %d: type=%q, want %q", i, ev.Type, wantType)
				}
				wantContent := expected[dropAfter+i].content
				if got := eventContent(ev); got != wantContent {
					t.Fatalf("event %d: content=%q, want %q (corrupted)", i, got, wantContent)
				}
			}
		})
	}
}

// TestSSEPacketDropHTTPReconnect is an end-to-end test: the client receives
// some events, the connection drops (simulating packet loss), the client
// reconnects with Last-Event-ID, and the server replays ALL missed events.
func TestSSEPacketDropHTTPReconnect(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	chatID := "web-reconnect"
	// Create a tenant row so the SSE access check passes for admin.
	if _, err := db.Exec("INSERT INTO tenants (channel, chat_id) VALUES ('web', ?)", chatID); err != nil {
		t.Fatal(err)
	}

	// Use GetActiveProgress as a readiness signal — it fires after the SSE
	// handler sets the high-water mark and subscribes, so we know the
	// connection is ready to receive events.
	sseReady := make(chan struct{}, 2)
	wc.SetCallbacks(WebCallbacks{
		GetActiveProgress: func(channel, gotChatID string) *protocol.ProgressEvent {
			select {
			case sseReady <- struct{}{}:
			default:
			}
			return nil
		},
	})

	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)

	// First connection (fresh — starts at high-water mark 0).
	resp1 := openSSE(t, server.URL, cookie, chatID, "")
	select {
	case <-sseReady:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE connection not ready")
	}

	// Publish 5 text events AFTER the connection is live.
	const total = 5
	for i := 1; i <= total; i++ {
		wc.hub.sendToClient(chatID, protocol.WSMessage{
			Type:    protocol.MsgTypeText,
			Content: "msg-" + strconv.Itoa(i),
		})
	}

	// Receive 2 events, then "drop" the connection.
	reader1 := bufio.NewReader(resp1.Body)
	for i := 1; i <= 2; i++ {
		msg := assertSSEMessage(t, readSSEEvent(t, reader1), protocol.MsgTypeText, uint64(i))
		if want := "msg-" + strconv.Itoa(i); msg.Content != want {
			t.Fatalf("event %d content=%q, want %q", i, msg.Content, want)
		}
	}
	resp1.Body.Close() // simulate connection drop after event 2

	// Reconnect with Last-Event-ID = 2 — server must replay events 3-5.
	resp2 := openSSE(t, server.URL, cookie, chatID, "2")
	defer resp2.Body.Close()
	reader2 := bufio.NewReader(resp2.Body)
	for i := 3; i <= total; i++ {
		msg := assertSSEMessage(t, readSSEEvent(t, reader2), protocol.MsgTypeText, uint64(i))
		if want := "msg-" + strconv.Itoa(i); msg.Content != want {
			t.Fatalf("replayed event %d content=%q, want %q", i, msg.Content, want)
		}
	}
}

// TestSSEStreamContentCoalescedNotLost verifies that multiple consecutive
// stream_content events coalesce to the LATEST content in the ring buffer —
// no data is lost, and the final state is always preserved.
func TestSSEStreamContentCoalescedNotLost(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-coalesce"
	source := "web:" + chatID

	// Publish 5 stream_content events with growing content.
	for i := 1; i <= 5; i++ {
		wc.SendProgress(chatID, &protocol.ProgressEvent{
			ChatID:        source,
			StreamContent: "chunk-" + strconv.Itoa(i),
		})
	}

	replayed := wc.replaySSEEvents(SessionSelector{Channel: "web", ChatID: chatID}, 0)
	// All 5 events are coalesced into 1 (latest wins, no intermediate events).
	if len(replayed) != 1 {
		t.Fatalf("expected 1 coalesced stream_content event, got %d: %+v", len(replayed), replayed)
	}
	ev := replayed[0]
	if ev.Type != protocol.MsgTypeStreamContent {
		t.Fatalf("type=%q, want stream_content", ev.Type)
	}
	if ev.Progress == nil || ev.Progress.StreamContent != "chunk-5" {
		t.Fatalf("content=%q, want %q (latest must be preserved)",
			ev.Progress.StreamContent, "chunk-5")
	}
	// The coalesced event carries the LATEST seq (5), not the first (1).
	if ev.Seq != 5 {
		t.Fatalf("seq=%d, want 5 (latest event's seq)", ev.Seq)
	}
}

// TestSSEMixedEventTypesReplayConsistent verifies that interleaved event types
// (stream_content, progress, text, session) are all preserved in the ring
// buffer and replayed in correct seq order with correct types + content.
func TestSSEMixedEventTypesReplayConsistent(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-mixed"
	source := "web:" + chatID
	expected := publishMixedEvents(wc, chatID, source)
	sel := SessionSelector{Channel: "web", ChatID: chatID}

	// Full replay from 0 — all events must be present.
	replayed := wc.replaySSEEvents(sel, 0)
	if len(replayed) != len(expected) {
		t.Fatalf("got %d events, want %d", len(replayed), len(expected))
	}

	// Verify monotonic seq + correct type + correct content.
	for i, ev := range replayed {
		if ev.Seq != expected[i].seq {
			t.Fatalf("event %d: seq=%d, want %d", i, ev.Seq, expected[i].seq)
		}
		if ev.Type != expected[i].typ {
			t.Fatalf("event %d: type=%q, want %q", i, ev.Type, expected[i].typ)
		}
		if got := eventContent(ev); got != expected[i].content {
			t.Fatalf("event %d: content=%q, want %q", i, got, expected[i].content)
		}
	}

	// Verify linear consistency: seq values are strictly increasing.
	for i := 1; i < len(replayed); i++ {
		if replayed[i].Seq <= replayed[i-1].Seq {
			t.Fatalf("seq not monotonic at index %d: %d <= %d", i, replayed[i].Seq, replayed[i-1].Seq)
		}
	}
}

// TestSSESendChOverflowRecoversAllTypes verifies that when sendCh overflows
// (client is slow / network stalled), events of ALL types are dropped from
// the live delivery path but fully recovered via ring buffer replay.
func TestSSESendChOverflowRecoversAllTypes(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-overflow"
	source := "web:" + chatID

	// Create an SSE client with a tiny sendCh (buffer 1) — most events
	// will overflow and be "dropped" from live delivery.
	client := &Client{
		connType:       clientConnTypeSSE,
		sendCh:         make(chan protocol.WSMessage, 1),
		done:           make(chan struct{}),
		chatID:         chatID,
		sessionChannel: "web",
		id:             "sse-overflow",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, sessionRouteKey("web", chatID))

	// Publish many events of mixed types — sendCh can hold only 1.
	publishMixedEvents(wc, chatID, source)

	// Drain whatever made it into sendCh (at most 1).
	drained := 0
drainLoop:
	for {
		select {
		case <-client.sendCh:
			drained++
		default:
			break drainLoop
		}
	}

	// Ring buffer replay recovers ALL events regardless of how many were
	// dropped from the live path.
	replayed := wc.replaySSEEvents(SessionSelector{Channel: "web", ChatID: chatID}, 0)
	if len(replayed) != 6 {
		t.Fatalf("drained=%d live, but replay got %d events, want 6 (all types recovered)",
			drained, len(replayed))
	}

	// Verify all 6 event types are present in the replay.
	seenTypes := make(map[string]bool)
	for _, ev := range replayed {
		seenTypes[ev.Type] = true
	}
	for _, want := range []string{"stream_content", "progress_structured", "text", "session"} {
		if !seenTypes[want] {
			t.Errorf("event type %q missing from replay", want)
		}
	}
}

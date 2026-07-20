package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestSessionRouteKeyNeverInfersChannelFromChatID(t *testing.T) {
	chatID := "web:chat-1/reviewer:1"
	webRoute := sessionRouteKey("web", chatID)
	agentRoute := sessionRouteKey("agent", chatID)
	if webRoute == agentRoute {
		t.Fatalf("explicit Web and Agent routes collided: %q", webRoute)
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

func TestSSEResumeAtHighWaterForcesAuthoritativeResync(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.SendSessionState(protocol.SessionEvent{
		Channel: "web", ChatID: "web-1", Action: "history_rewound", TargetHistoryID: 11,
	})
	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)

	// Cursor 1 may mean either "already consumed seq 1" or "old server epoch
	// also ended at 1". The server cannot distinguish them, so it requires one
	// authoritative client resync instead of silently dropping a new reset.
	resp := openSSE(t, server.URL, cookie, "web-1", "1")
	defer resp.Body.Close()
	control := assertSSEResyncControl(t, readSSEEvent(t, bufio.NewReader(resp.Body)), "web", "web-1")
	if control.Metadata["baseline_seq"] != "1" {
		t.Fatalf("resync baseline = %q, want 1", control.Metadata["baseline_seq"])
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

	msg := <-client.sendCh
	if msg.Type != protocol.MsgTypeProgress || msg.Seq != 3 || msg.Progress == nil {
		t.Fatalf("full progress fallback = %#v", msg)
	}
	if msg.Progress.Phase != "tool_exec" || msg.Progress.Iteration != 2 || len(msg.Progress.ActiveTools) != 1 || len(msg.Progress.IterationHistory) != 1 {
		t.Fatalf("full progress fields were lost: %#v", msg.Progress)
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
	sourceA := "agent:worker-a"
	sourceB := "agent:worker-b"
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
	reader := bufio.NewReader(resp.Body)
	assertSSEResyncControl(t, readSSEEvent(t, reader), "web", "web-1")
	msg := assertSSEMessage(t, readSSEEvent(t, reader), protocol.MsgTypeProgress, 2)
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

func TestSSEReconnectRetainsDestructiveBarrierBeyondRingCapacity(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)
	sel := SessionSelector{Channel: "web", ChatID: "web-1"}
	wc.hub.sendToSession(sel.Channel, sel.ChatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "before reset"})
	wc.SendSessionState(protocol.SessionEvent{
		Channel: sel.Channel, ChatID: sel.ChatID, Action: "history_rewound", TargetHistoryID: 41,
	})
	barrierSeq := wc.getEventStream(sessionRouteKey(sel.Channel, sel.ChatID)).lastSeq()
	for i := 0; i < eventStreamSize+1; i++ {
		wc.hub.sendToSession(sel.Channel, sel.ChatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: strconv.Itoa(i)})
	}

	// Reconnecting from before the reset must receive the barrier even though
	// the bounded suffix now starts with a sequence gap.
	replayed := wc.replaySSEEvents(sel, barrierSeq-1)
	if len(replayed) != eventStreamSize+1 {
		t.Fatalf("replay length = %d, want barrier + %d suffix events", len(replayed), eventStreamSize)
	}
	if replayed[0].Session == nil || replayed[0].Session.Action != "history_rewound" || replayed[0].Seq != barrierSeq {
		t.Fatalf("first reconnect event is not retained barrier: %#v", replayed[0])
	}
	if replayed[1].Seq <= barrierSeq+1 {
		t.Fatalf("test did not create expected post-barrier sequence gap: barrier=%d first_suffix=%d", barrierSeq, replayed[1].Seq)
	}
	resp := openSSE(t, server.URL, cookie, sel.ChatID, strconv.FormatUint(barrierSeq-1, 10))
	reader := bufio.NewReader(resp.Body)
	reset := assertSSEMessage(t, readSSEEvent(t, reader), protocol.MsgTypeSession, barrierSeq)
	if reset.Session == nil || reset.Session.Action != "history_rewound" {
		t.Fatalf("SSE reconnect did not receive reset first: %#v", reset)
	}
	resync := assertSSEMessage(t, readSSEEvent(t, reader), protocol.MsgTypeResyncRequired, barrierSeq+1)
	if resync.Channel != sel.Channel || resync.ChatID != sel.ChatID {
		t.Fatalf("SSE resync route = %#v", resync)
	}
	firstSuffix := assertSSEMessage(t, readSSEEvent(t, reader), protocol.MsgTypeText, replayed[1].Seq)
	if firstSuffix.Seq <= barrierSeq+1 {
		t.Fatalf("SSE reconnect did not expose expected sequence gap: %#v", firstSuffix)
	}
	resp.Body.Close()
	waitForHubClients(t, wc, 0)
	if got := wc.replaySSEEvents(sel, barrierSeq); len(got) != eventStreamSize || got[0].Session != nil {
		t.Fatalf("cursor at barrier replayed barrier again: %#v", got)
	}

	// SessionReset is destructive too and replaces the prior barrier epoch.
	wc.hub.sendToSession(sel.Channel, sel.ChatID, protocol.WSMessage{Type: protocol.MsgTypeText, SessionReset: true})
	latestBarrier := wc.getEventStream(sessionRouteKey(sel.Channel, sel.ChatID)).lastSeq()
	for i := 0; i < eventStreamSize+1; i++ {
		wc.hub.sendToSession(sel.Channel, sel.ChatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "new-" + strconv.Itoa(i)})
	}
	latest := wc.replaySSEEvents(sel, latestBarrier-1)
	if len(latest) != eventStreamSize+1 || !latest[0].SessionReset || latest[0].Seq != latestBarrier {
		t.Fatalf("latest session reset barrier was not retained: %#v", latest)
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
	assertSSEResyncControl(t, readSSEEvent(t, reader), "web", "web-1")
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

func TestWSAgentRouteFallbackUsesCanonicalSession(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "cli:root/reviewer:1"
	var progressChannel, pendingChannel string
	wc.SetCallbacks(WebCallbacks{
		GetActiveProgress: func(channel, gotChatID string) *protocol.ProgressEvent {
			progressChannel = channel
			if gotChatID != chatID {
				t.Fatalf("progress chat ID = %q", gotChatID)
			}
			return &protocol.ProgressEvent{Phase: "thinking"}
		},
		WithPendingAskUser: func(channel, gotChatID string, fn func(*protocol.ProgressEvent) bool) bool {
			pendingChannel = channel
			if gotChatID != chatID {
				t.Fatalf("pending chat ID = %q", gotChatID)
			}
			return fn(&protocol.ProgressEvent{
				RequestID: "request-1",
				Questions: []protocol.AskUserQuestion{{Question: "Continue?"}},
			})
		},
	})
	client := &Client{
		id:          "agent-replay",
		isCLI:       true,
		routeReplay: true,
		sendCh:      make(chan protocol.WSMessage, 4),
		done:        make(chan struct{}),
	}
	wc.hub.addClient(client.id, client)
	route := SessionSelector{Channel: "cli", ChatID: chatID}
	if !wc.subscribeAndReplay(client, route, "agent", 0, false) {
		t.Fatal("Agent replay subscription failed")
	}
	if progressChannel != "agent" || pendingChannel != "agent" {
		t.Fatalf("fallback channels = progress:%q pending:%q", progressChannel, pendingChannel)
	}

	var progress, ask *protocol.WSMessage
	for len(client.sendCh) > 0 {
		msg := <-client.sendCh
		switch msg.Type {
		case protocol.MsgTypeProgress:
			copy := msg
			progress = &copy
		case protocol.MsgTypeAskUser:
			copy := msg
			ask = &copy
		}
	}
	if progress == nil || progress.Channel != "agent" || progress.RouteChannel != "cli" || progress.Progress == nil || progress.Progress.Phase != "thinking" {
		t.Fatalf("Agent progress fallback = %#v", progress)
	}
	if ask == nil || ask.Channel != "agent" || ask.RouteChannel != "cli" || ask.Progress == nil || ask.Progress.RequestID != "request-1" {
		t.Fatalf("Agent AskUser fallback = %#v", ask)
	}
	var event protocol.AskUserEvent
	if err := json.Unmarshal([]byte(ask.Content), &event); err != nil {
		t.Fatalf("decode AskUser content: %v", err)
	}
	if event.Channel != "agent" || event.ChatID != chatID || event.RequestID != "request-1" || event.Questions == "" {
		t.Fatalf("Agent AskUser event = %#v", event)
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
	client.id = "replay-" + senderID
	client.userID = senderID
	client.done = make(chan struct{})
	client.routeReplay = true
	wc.hub.addClient(client.id, client)
	if !wc.subscribeAndReplay(client, SessionSelector{Channel: "web", ChatID: senderID}, "web", lastSeq, true) {
		t.Fatal("WS replay subscription failed")
	}
	select {
	case ack := <-client.sendCh:
		if ack.Type != protocol.MsgTypeSync {
			client.sendCh <- ack
		}
	default:
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

	msg := <-client.sendCh
	if msg.Seq != 2 || msg.Progress == nil || msg.Progress.Seq != 2 || msg.Progress.Phase != "new" {
		t.Fatalf("progress fallback = %#v", msg)
	}
}

func TestSSEFallbackIsSharedBySubscribedClients(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	clients := []*Client{
		{connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 4), done: make(chan struct{}), id: "sse-1", chatID: chatID, sessionChannel: "web"},
		{connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 4), done: make(chan struct{}), id: "sse-2", chatID: chatID, sessionChannel: "web"},
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

func TestSSEFallbackCheckAndPublishIsAtomicWithLiveEvent(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	client := &Client{
		connType: clientConnTypeSSE,
		sendCh:   make(chan protocol.WSMessage, 4),
		done:     make(chan struct{}),
		id:       "sse-atomic",
		chatID:   chatID,
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

	fallback := <-client.sendCh
	live := <-client.sendCh
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
		{name: "CLI websocket", chatID: "/workspace/cli-1", isCLI: true, wantSeq: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channelName := "web"
			if tt.isCLI {
				channelName = "cli"
			}
			wc.hub.sendToSession(channelName, tt.chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "offline"})
			client := &Client{
				connType: clientConnTypeWS,
				sendCh:   make(chan protocol.WSMessage, 1),
				done:     make(chan struct{}),
				id:       tt.name,
				isCLI:    tt.isCLI,
			}
			wc.hub.addClient(client.id, client)
			wc.hub.subscribe(client.id, sessionRouteKey(channelName, tt.chatID))

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
		connType: clientConnTypeSSE,
		sendCh:   make(chan protocol.WSMessage, 16),
		done:     make(chan struct{}),
		id:       "sse-contract",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, sessionRouteKey("web", "web-1"))

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
	wsClient := &Client{connType: clientConnTypeWS, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "browser-ws"}
	cliClient := &Client{connType: clientConnTypeWS, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "cli-ws", isCLI: true}
	for _, client := range []*Client{client1, client2, wsClient, cliClient} {
		wc.hub.addClient(client.id, client)
	}
	wc.hub.subscribe(client1.id, sessionRouteKey("web", "web-1"))
	wc.hub.subscribe(client2.id, sessionRouteKey("web", "web-2"))
	wc.hub.subscribe(wsClient.id, sessionRouteKey("web", "web-1"))
	wc.hub.subscribe(cliClient.id, sessionRouteKey("cli", "web-1"))

	wc.SendSessionState(protocol.SessionEvent{Channel: "web", ChatID: "web-1", Action: "busy"})

	var sseMsg protocol.WSMessage
	select {
	case sseMsg = <-client1.sendCh:
		if sseMsg.Type != protocol.MsgTypeSession || sseMsg.Seq == 0 {
			t.Fatalf("user-1 session event = %#v", sseMsg)
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
	case msg := <-wsClient.sendCh:
		if msg.Seq != sseMsg.Seq {
			t.Fatalf("browser WS session seq = %d, SSE seq = %d", msg.Seq, sseMsg.Seq)
		}
	default:
		t.Fatal("browser WS broadcast behavior changed")
	}
	select {
	case msg := <-cliClient.sendCh:
		t.Fatalf("CLI WS client on a different route received session event: %#v", msg)
	default:
	}
}

func TestAgentProgressUsesCanonicalChildRoute(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	fullKey := "cli:/workspace/review:1"
	client := &Client{connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "agent-view"}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, sessionRouteKey("agent", fullKey))

	wc.SendProgress(fullKey, &protocol.ProgressEvent{ChatID: "agent:" + fullKey, Phase: "thinking"})

	select {
	case msg := <-client.sendCh:
		if msg.Type != protocol.MsgTypeProgress || msg.RouteChannel != "agent" || msg.RouteChatID != fullKey {
			t.Fatalf("agent progress route = %#v", msg)
		}
	default:
		t.Fatal("agent progress did not reach the canonical child route")
	}
	if got := wc.getEventStream(sessionRouteKey("web", fullKey)).lastSeq(); got != 0 {
		t.Fatalf("agent progress leaked into web route: seq=%d", got)
	}
}

func TestCLISubAgentLifecycleAlsoReachesBrowserChildRoute(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	remoteCLI := NewRemoteCLIChannel(wc.Hub())
	fullKey := "cli:/workspace/review:1"
	parentClient := &Client{connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "cli-parent"}
	childClient := &Client{connType: clientConnTypeSSE, sendCh: make(chan protocol.WSMessage, 1), done: make(chan struct{}), id: "agent-child"}
	for _, client := range []*Client{parentClient, childClient} {
		wc.hub.addClient(client.id, client)
	}
	wc.hub.subscribe(parentClient.id, sessionRouteKey("cli", "/workspace"))
	wc.hub.subscribe(childClient.id, sessionRouteKey("agent", fullKey))

	remoteCLI.SendSessionState(protocol.SessionEvent{
		Channel: "cli", ChatID: "/workspace", Action: "subagent_started", SessionKey: fullKey,
	})

	for name, client := range map[string]*Client{"parent": parentClient, "child": childClient} {
		select {
		case msg := <-client.sendCh:
			if msg.Type != protocol.MsgTypeSession || msg.Session == nil || msg.Session.SessionKey != fullKey {
				t.Fatalf("%s lifecycle event = %#v", name, msg)
			}
		default:
			t.Fatalf("%s route did not receive SubAgent lifecycle", name)
		}
	}
}

func TestHistoryRewoundClearsReplayBeforeResetEvent(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	otherChatID := "web-2"

	wc.hub.sendToSession("web", chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "deleted future 1"})
	wc.hub.sendToSession("web", chatID, protocol.WSMessage{Type: protocol.MsgTypeProgress, Progress: &protocol.ProgressEvent{Phase: "thinking"}})
	wc.hub.sendToSession("web", otherChatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "keep me"})
	stream := wc.getEventStream(sessionRouteKey("web", chatID))
	if stream.lastSeq() != 2 {
		t.Fatalf("pre-reset sequence = %d, want 2", stream.lastSeq())
	}
	if wc.hub.offline[sessionRouteKey("web", chatID)] == nil {
		t.Fatal("expected legacy WebSocket replay buffer before reset")
	}

	wc.SendSessionState(protocol.SessionEvent{
		Channel: "web", ChatID: chatID, Action: "history_rewound", TargetHistoryID: 17,
	})

	events := wc.replaySSEEvents(SessionSelector{Channel: "web", ChatID: chatID}, 0)
	if len(events) != 1 || events[0].Type != protocol.MsgTypeSession || events[0].Session == nil {
		t.Fatalf("post-reset replay = %+v", events)
	}
	if events[0].Seq != 3 || events[0].Session.Action != "history_rewound" || events[0].Session.TargetHistoryID != 17 {
		t.Fatalf("reset event = %+v", events[0])
	}
	offlineReset := wc.hub.offline[sessionRouteKey("web", chatID)]
	if offlineReset == nil {
		t.Fatal("offline WebSocket reset barrier was not retained")
	}
	offlineEvents := offlineReset.flush()
	if len(offlineEvents) != 1 || offlineEvents[0].Session == nil || offlineEvents[0].Session.Action != "history_rewound" {
		t.Fatalf("stale WebSocket replay survived history reset: %+v", offlineEvents)
	}
	other := wc.replaySSEEvents(SessionSelector{Channel: "web", ChatID: otherChatID}, 0)
	if len(other) != 1 || other[0].Content != "keep me" {
		t.Fatalf("unrelated replay changed: %+v", other)
	}
}

func TestHistoryRewoundIsRouteScopedWSBarrier(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "shared-chat"
	webRoute := sessionRouteKey("web", chatID)
	cliRoute := sessionRouteKey("cli", chatID)
	webClient := &Client{
		connType:     clientConnTypeWS,
		sendCh:       make(chan protocol.WSMessage, 8),
		done:         make(chan struct{}),
		id:           "web-target",
		statelessSig: make(chan struct{}, 1),
	}
	cliClient := &Client{
		connType:     clientConnTypeWS,
		sendCh:       make(chan protocol.WSMessage, 8),
		done:         make(chan struct{}),
		id:           "cli-other-route",
		isCLI:        true,
		statelessSig: make(chan struct{}, 1),
	}
	for _, client := range []*Client{webClient, cliClient} {
		wc.hub.addClient(client.id, client)
	}
	wc.hub.subscribe(webClient.id, webRoute)
	wc.hub.subscribe(cliClient.id, cliRoute)

	webClient.sendCh <- protocol.WSMessage{Type: protocol.MsgTypeProgress, Progress: &protocol.ProgressEvent{Content: "deleted queued future"}}
	webClient.sendCh <- protocol.WSMessage{Type: protocol.MsgTypeRPCResponse, ID: "preserve-rpc"}
	webClient.storeStateless(&protocol.WSMessage{Type: protocol.MsgTypeStreamContent, Progress: &protocol.ProgressEvent{StreamContent: "deleted stream"}})
	cliClient.sendCh <- protocol.WSMessage{Type: protocol.MsgTypeProgress, Progress: &protocol.ProgressEvent{Content: "other route"}}
	cliClient.storeStateless(&protocol.WSMessage{Type: protocol.MsgTypeStreamContent, Progress: &protocol.ProgressEvent{StreamContent: "other route"}})

	wc.hub.sendToSession("web", chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "deleted web replay"})
	wc.hub.sendToSession("cli", chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "keep cli replay"})
	wc.hub.offMu.Lock()
	wc.hub.offline[webRoute] = newRingBuffer(webOfflineMsgBufSize)
	wc.hub.offline[webRoute].push(protocol.WSMessage{Type: protocol.MsgTypeText, Content: "deleted offline future"})
	wc.hub.offline[cliRoute] = newRingBuffer(webOfflineMsgBufSize)
	wc.hub.offline[cliRoute].push(protocol.WSMessage{Type: protocol.MsgTypeText, Content: "keep offline future"})
	wc.hub.offMu.Unlock()

	wc.SendSessionState(protocol.SessionEvent{
		Channel: "web", ChatID: chatID, Action: "history_rewound", TargetHistoryID: 17,
	})

	first := <-webClient.sendCh
	if first.Type != protocol.MsgTypeRPCResponse || first.ID != "preserve-rpc" {
		t.Fatalf("non-session queue was not preserved: %#v", first)
	}
	reset := <-webClient.sendCh
	if reset.Type != protocol.MsgTypeSession || reset.Session == nil || reset.Session.Action != "history_rewound" {
		t.Fatalf("target reset = %#v", reset)
	}
	if got := webClient.drainStateless(); len(got) != 0 {
		t.Fatalf("target stateless future survived: %#v", got)
	}
	otherQueued := <-cliClient.sendCh
	if otherQueued.Type != protocol.MsgTypeProgress || otherQueued.Progress == nil || otherQueued.Progress.Content != "other route" {
		t.Fatalf("other route queue changed: %#v", otherQueued)
	}
	otherText := <-cliClient.sendCh
	if otherText.Type != protocol.MsgTypeText || otherText.Content != "keep cli replay" {
		t.Fatalf("other route text queue changed: %#v", otherText)
	}
	if got := cliClient.drainStateless(); len(got) != 1 || got[0].Progress.StreamContent != "other route" {
		t.Fatalf("other route stateless state changed: %#v", got)
	}
	select {
	case unexpected := <-cliClient.sendCh:
		t.Fatalf("reset leaked to same chat ID on another channel: %#v", unexpected)
	default:
	}

	webReplay := wc.replaySSEEvents(SessionSelector{Channel: "web", ChatID: chatID}, 0)
	if len(webReplay) != 1 || webReplay[0].Session == nil || webReplay[0].Session.Action != "history_rewound" {
		t.Fatalf("web replay after reset = %#v", webReplay)
	}
	cliReplay := wc.replaySSEEvents(SessionSelector{Channel: "cli", ChatID: chatID}, 0)
	if len(cliReplay) != 1 || cliReplay[0].Content != "keep cli replay" {
		t.Fatalf("CLI replay changed by Web reset = %#v", cliReplay)
	}
	wc.hub.offMu.Lock()
	webOffline := wc.hub.offline[webRoute]
	cliOffline := wc.hub.offline[cliRoute]
	wc.hub.offMu.Unlock()
	if webOffline != nil {
		t.Fatalf("target route offline future survived reset: %#v", webOffline.flush())
	}
	if cliOffline == nil {
		t.Fatal("same chat ID on another channel lost offline state")
	}
	cliOfflineEvents := cliOffline.flush()
	if len(cliOfflineEvents) != 1 || cliOfflineEvents[0].Content != "keep offline future" {
		t.Fatalf("other route offline state changed: %#v", cliOfflineEvents)
	}
}

func TestHistoryRewoundDisconnectsFullWSAndReplaysBarrier(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "cli-chat"
	routeKey := sessionRouteKey("cli", chatID)
	client := &Client{
		connType:     clientConnTypeWS,
		sendCh:       make(chan protocol.WSMessage, 1),
		done:         make(chan struct{}),
		id:           "blocked-cli",
		isCLI:        true,
		statelessSig: make(chan struct{}, 1),
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, routeKey)
	client.sendCh <- protocol.WSMessage{Type: protocol.MsgTypeRPCResponse, ID: "in-flight-rpc"}

	wc.SendSessionState(protocol.SessionEvent{
		Channel: "cli", ChatID: chatID, Action: "history_rewound", TargetHistoryID: 9,
	})
	select {
	case <-client.done:
	default:
		t.Fatal("full reset queue did not disconnect client")
	}
	queued := <-client.sendCh
	if queued.Type != protocol.MsgTypeRPCResponse || queued.ID != "in-flight-rpc" {
		t.Fatalf("full queue was corrupted: %#v", queued)
	}
	replay := wc.replaySSEEvents(SessionSelector{Channel: "cli", ChatID: chatID}, 0)
	if len(replay) != 1 || replay[0].Session == nil || replay[0].Session.Action != "history_rewound" {
		t.Fatalf("reset barrier unavailable for reconnect: %#v", replay)
	}

	reconnected := &Client{
		connType:     clientConnTypeWS,
		sendCh:       make(chan protocol.WSMessage, 1),
		done:         make(chan struct{}),
		id:           "reconnected-cli",
		isCLI:        true,
		statelessSig: make(chan struct{}, 1),
	}
	wc.hub.addClient(reconnected.id, reconnected)
	wc.hub.subscribe(reconnected.id, routeKey)
	reset := <-reconnected.sendCh
	if reset.Session == nil || reset.Session.Action != "history_rewound" || reset.Seq != replay[0].Seq {
		t.Fatalf("reconnected CLI reset = %#v", reset)
	}
}

func TestRemoteCLIControlUsesQualifiedRoute(t *testing.T) {
	hub := newHub()
	remoteCLI := NewRemoteCLIChannel(hub)
	chatID := "/repo/control"
	client := &Client{
		connType:     clientConnTypeWS,
		sendCh:       make(chan protocol.WSMessage, 1),
		done:         make(chan struct{}),
		id:           "control-cli",
		isCLI:        true,
		statelessSig: make(chan struct{}, 1),
	}
	hub.addClient(client.id, client)
	hub.subscribe(client.id, sessionRouteKey("cli", chatID))

	type controlResult struct {
		value map[string]string
		err   error
	}
	resultCh := make(chan controlResult, 1)
	go func() {
		value, err := remoteCLI.SendTUIControlRequest(chatID, "layout", map[string]string{"mode": "wide"})
		resultCh <- controlResult{value: value, err: err}
	}()

	request := <-client.sendCh
	if request.Type != protocol.MsgTypeTUIControlReq || request.TUIControl == nil || request.TUIControl.Action != "layout" {
		t.Fatalf("remote CLI control request = %#v", request)
	}
	remoteCLI.deliverTUIResponse(request.ID, &protocol.TUIControlPayload{Result: map[string]string{"status": "ok"}})
	result := <-resultCh
	if result.err != nil || result.value["status"] != "ok" {
		t.Fatalf("remote CLI control result = (%#v, %v)", result.value, result.err)
	}
}

func TestRemoteCLIAgentHistoryResetUsesCLITransportRoute(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	remoteCLI := NewRemoteCLIChannel(wc.hub)
	fullKey := "cli:/repo/reviewer:one"
	client := &Client{
		connType:     clientConnTypeWS,
		sendCh:       make(chan protocol.WSMessage, 2),
		done:         make(chan struct{}),
		id:           "agent-view-cli",
		isCLI:        true,
		statelessSig: make(chan struct{}, 1),
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, sessionRouteKey("cli", fullKey))
	wc.hub.sendToSession("agent", fullKey, protocol.WSMessage{Type: protocol.MsgTypeText, Content: "web-agent-route"})

	remoteCLI.SendSessionState(protocol.SessionEvent{
		Channel: "agent", ChatID: fullKey, Action: "history_rewound", TargetHistoryID: 23,
	})
	reset := <-client.sendCh
	if reset.Type != protocol.MsgTypeSession || reset.Session == nil || reset.Session.Channel != "agent" || reset.Session.ChatID != fullKey || reset.Session.Action != "history_rewound" {
		t.Fatalf("remote Agent reset = %#v", reset)
	}
	cliReplay := wc.replaySSEEvents(SessionSelector{Channel: "cli", ChatID: fullKey}, 0)
	if len(cliReplay) != 1 || cliReplay[0].Session == nil || cliReplay[0].Session.Channel != "agent" {
		t.Fatalf("CLI transport replay = %#v", cliReplay)
	}
	agentReplay := wc.replaySSEEvents(SessionSelector{Channel: "agent", ChatID: fullKey}, 0)
	if len(agentReplay) != 1 || agentReplay[0].Content != "web-agent-route" {
		t.Fatalf("remote CLI reset changed Web Agent route = %#v", agentReplay)
	}
}

func TestOfflineResetBarrierSurvivesOverflowAndFullSubscribe(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	remoteCLI := NewRemoteCLIChannel(wc.hub)
	chatID := "/repo/offline"
	routeKey := sessionRouteKey("cli", chatID)
	remoteCLI.SendSessionState(protocol.SessionEvent{
		Channel: "cli", ChatID: chatID, Action: "history_rewound", TargetHistoryID: 31,
	})
	for i := 0; i < webOfflineMsgBufSize+10; i++ {
		wc.hub.sendToSession("cli", chatID, protocol.WSMessage{Type: protocol.MsgTypeText, Content: strconv.Itoa(i)})
	}

	blocked := &Client{
		connType:     clientConnTypeWS,
		sendCh:       make(chan protocol.WSMessage, 1),
		done:         make(chan struct{}),
		id:           "blocked-replay-cli",
		isCLI:        true,
		statelessSig: make(chan struct{}, 1),
	}
	blocked.sendCh <- protocol.WSMessage{Type: protocol.MsgTypeRPCResponse, ID: "busy"}
	wc.hub.addClient(blocked.id, blocked)
	if wc.hub.subscribe(blocked.id, routeKey) {
		t.Fatal("full replay subscriber was accepted")
	}
	select {
	case <-blocked.done:
	default:
		t.Fatal("full replay subscriber was not disconnected")
	}

	reconnected := &Client{
		connType:     clientConnTypeWS,
		sendCh:       make(chan protocol.WSMessage, webOfflineMsgBufSize+1),
		done:         make(chan struct{}),
		id:           "reconnected-replay-cli",
		isCLI:        true,
		statelessSig: make(chan struct{}, 1),
	}
	wc.hub.addClient(reconnected.id, reconnected)
	if !wc.hub.subscribe(reconnected.id, routeKey) {
		t.Fatal("reconnected replay subscriber was rejected")
	}
	reset := <-reconnected.sendCh
	if reset.Session == nil || reset.Session.Action != "history_rewound" || reset.Session.TargetHistoryID != 31 {
		t.Fatalf("first replay event is not reset barrier: %#v", reset)
	}
	if got := len(reconnected.sendCh); got != webOfflineMsgBufSize {
		t.Fatalf("offline messages after barrier = %d, want %d", got, webOfflineMsgBufSize)
	}
	wc.hub.offMu.Lock()
	remaining := wc.hub.offline[routeKey]
	wc.hub.offMu.Unlock()
	if remaining != nil {
		t.Fatalf("successful replay left offline state: %#v", remaining.flush())
	}
}

func TestSSEProgressFallbackStopsAtHistoryAndSessionResetBarriers(t *testing.T) {
	snapshot := &protocol.ProgressEvent{Seq: 1, Phase: "thinking", Content: "deleted future"}
	for _, barrier := range []protocol.WSMessage{
		{Type: protocol.MsgTypeSession, Seq: 2, Session: &protocol.SessionEvent{Action: "history_rewound"}},
		{Type: protocol.MsgTypeText, Seq: 2, SessionReset: true},
	} {
		if progress, ok := selectSSEProgressFallback(snapshot, []protocol.WSMessage{
			{Type: protocol.MsgTypeProgress, Seq: 1, Progress: snapshot},
			barrier,
		}); ok || progress != nil {
			t.Fatalf("stale fallback crossed reset barrier %#v: %#v", barrier, progress)
		}
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
	if cliMsg.Seq != sseMsg.Seq {
		t.Fatalf("CLI WS envelope sequence = %d, want %d", cliMsg.Seq, sseMsg.Seq)
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

	sseMsg := <-sseClient.sendCh
	if sseMsg.Type != protocol.MsgTypeStreamContent || sseMsg.Seq != 1 {
		t.Fatalf("SSE sparse progress = %#v", sseMsg)
	}
	webMsgs := webSocketClient.drainStateless()
	if len(webMsgs) != 1 || webMsgs[0].Type != protocol.MsgTypeProgress || webMsgs[0].Seq != 1 {
		t.Fatalf("browser WS sparse progress = %#v", webMsgs)
	}
	cliMsgs := cliClient.drainStateless()
	if len(cliMsgs) != 1 || cliMsgs[0].Type != protocol.MsgTypeProgress || cliMsgs[0].Seq != 1 {
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

func TestCLIClientCanonicalOwnershipAndAgentParentForgery(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	for _, tenant := range []struct {
		channel string
		chatID  string
		owner   int64
	}{
		{channel: "cli", chatID: "owned", owner: 42},
		{channel: "cli", chatID: "foreign", owner: 99},
		{channel: "agent", chatID: "cli:owned/review:1", owner: 42},
		// The key claims an owned parent, but the child tenant belongs to another user.
		{channel: "agent", chatID: "agent:cli:owned/review:1/fix:2", owner: 99},
		{channel: "agent", chatID: "cli:owned/foreign-parent:1", owner: 99},
		// The child owner matches, but its direct parent tenant does not.
		{channel: "agent", chatID: "agent:cli:owned/foreign-parent:1/forged-child:2", owner: 42},
		// A same-name CLI tenant must not shadow the foreign Agent tenant on the
		// shared remote CLI transport route.
		{channel: "agent", chatID: "cli:foreign/review:1", owner: 99},
		{channel: "cli", chatID: "cli:foreign/review:1", owner: 42},
		// Agent-shaped IDs remain reserved even when a legacy CLI tenant exists
		// without an Agent tenant of the same name.
		{channel: "cli", chatID: "cli:owned/legacy-agent-key:1", owner: 42},
	} {
		if _, err := db.Exec(
			"INSERT INTO tenants (channel, chat_id, owner_user_id, last_active_at) VALUES (?, ?, ?, ?)",
			tenant.channel, tenant.chatID, tenant.owner, time.Now().Format(time.RFC3339),
		); err != nil {
			t.Fatal(err)
		}
	}
	client := &Client{isCLI: true, userID: "web-2", canonicalUserID: 42, canonicalRole: "user"}

	if !wc.canAccessClientSession(client, "cli", "owned", false) {
		t.Fatal("runner token owner cannot access its CLI tenant")
	}
	if wc.canAccessClientSession(client, "cli", "foreign", true) {
		t.Fatal("runner token accessed a foreign existing tenant")
	}
	if !wc.canAccessClientSession(client, "cli", "new-session", true) {
		t.Fatal("runner token cannot create a new CLI tenant")
	}
	if !wc.canAccessClientSession(client, "cli", "new-session", false) {
		t.Fatal("claimed CLI tenant was not available to subsequent control requests")
	}
	if wc.canAccessClientSession(client, "cli", "cli:new-session/review:1", true) {
		t.Fatal("runner created an Agent-shaped CLI tenant")
	}
	if wc.canAccessClientSession(client, "cli", "cli:owned/legacy-agent-key:1", false) {
		t.Fatal("runner accessed an existing Agent-shaped CLI tenant")
	}
	if _, err := wc.resolveInboundSession(context.Background(), inboundIdentity{
		SenderID: "web-2", CanonicalUserID: 42, CanonicalRole: "user", IsCLI: true,
	}, "cli", "cli:owned/legacy-agent-key:1"); err == nil {
		t.Fatal("REST inbound accepted an existing Agent-shaped CLI tenant")
	}
	collisionID := "cli:foreign/review:1"
	if accessChannel := wc.clientRouteAccessChannel(client, "cli", collisionID); accessChannel != "agent" {
		t.Fatalf("same-name Agent route resolved as %q, want agent", accessChannel)
	}
	if wc.canAccessClientSession(client, wc.clientRouteAccessChannel(client, "cli", collisionID), collisionID, false) {
		t.Fatal("same-name CLI tenant shadowed foreign Agent ownership")
	}
	if wc.canAccessClientSession(client, "agent", "agent:cli:owned/review:1/fix:2", false) {
		t.Fatal("forged Agent parent key bypassed child tenant ownership")
	}
	if wc.canAccessClientSession(client, "agent", "agent:cli:owned/foreign-parent:1/forged-child:2", false) {
		t.Fatal("foreign direct Agent parent ownership was not checked recursively")
	}
}

func TestCLIClientConcurrentTenantClaimHasSingleOwner(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	clients := []*Client{
		{isCLI: true, userID: "web-42", canonicalUserID: 42, canonicalRole: "user"},
		{isCLI: true, userID: "web-99", canonicalUserID: 99, canonicalRole: "user"},
	}

	start := make(chan struct{})
	results := make(chan bool, len(clients))
	for _, client := range clients {
		client := client
		go func() {
			<-start
			results <- wc.canAccessClientSession(client, "cli", "concurrent-claim", true)
		}()
	}
	close(start)

	allowed := 0
	for range clients {
		if <-results {
			allowed++
		}
	}
	if allowed != 1 {
		t.Fatalf("concurrent claim allowed %d users, want 1", allowed)
	}

	var ownerUserID int64
	if err := db.QueryRow(
		`SELECT COALESCE(owner_user_id, 0) FROM tenants WHERE channel = 'cli' AND chat_id = 'concurrent-claim'`,
	).Scan(&ownerUserID); err != nil {
		t.Fatalf("read claimed tenant owner: %v", err)
	}
	if ownerUserID != 42 && ownerUserID != 99 {
		t.Fatalf("claimed tenant owner = %d, want 42 or 99", ownerUserID)
	}
	for _, client := range clients {
		got := wc.canAccessClientSession(client, "cli", "concurrent-claim", false)
		want := client.canonicalUserID == ownerUserID
		if got != want {
			t.Fatalf("post-claim access for user %d = %v, want %v", client.canonicalUserID, got, want)
		}
	}
}

func TestWSReplayOverflowRequiresAuthoritativeResync(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	sel := SessionSelector{Channel: "cli", ChatID: "chat"}
	for i := 0; i < eventStreamSize+1; i++ {
		wc.hub.sendToSession(sel.Channel, sel.ChatID, protocol.WSMessage{
			Type: protocol.MsgTypeText, Content: strconv.Itoa(i),
		})
	}
	client := &Client{
		connType:    clientConnTypeWS,
		sendCh:      make(chan protocol.WSMessage, webSendChBufSize),
		done:        make(chan struct{}),
		id:          "overflow-client",
		userID:      "web-2",
		routeReplay: true,
	}
	wc.hub.addClient(client.id, client)
	if !wc.subscribeAndReplay(client, sel, sel.Channel, 0, true) {
		t.Fatal("overflow replay subscription failed")
	}
	resync := <-client.sendCh
	if resync.Type != protocol.MsgTypeResyncRequired || resync.Seq == 0 || resync.RouteChannel != sel.Channel || resync.RouteChatID != sel.ChatID {
		t.Fatalf("resync signal = %#v", resync)
	}
	if len(client.sendCh) != 0 {
		t.Fatalf("overflow replay sent stale suffix instead of requiring resync: %d queued", len(client.sendCh))
	}
}

func TestWSAgentReplayOverflowUsesCanonicalResyncChannel(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	sel := SessionSelector{Channel: "cli", ChatID: "cli:root/reviewer:1"}
	wc.SetCallbacks(WebCallbacks{
		GetActiveProgress: func(channel, chatID string) *protocol.ProgressEvent {
			if channel != "agent" || chatID != sel.ChatID {
				t.Fatalf("progress fallback target = (%q, %q)", channel, chatID)
			}
			return &protocol.ProgressEvent{Phase: "thinking", Iteration: 3}
		},
		WithPendingAskUser: func(channel, chatID string, fn func(*protocol.ProgressEvent) bool) bool {
			if channel != "agent" || chatID != sel.ChatID {
				t.Fatalf("AskUser fallback target = (%q, %q)", channel, chatID)
			}
			return fn(&protocol.ProgressEvent{RequestID: "request-overflow", Questions: []protocol.AskUserQuestion{{Question: "Continue?"}}})
		},
	})
	for i := 0; i < eventStreamSize+1; i++ {
		wc.hub.sendToSession(sel.Channel, sel.ChatID, protocol.WSMessage{
			Type: protocol.MsgTypeText, Content: strconv.Itoa(i),
		})
	}
	client := &Client{
		connType:    clientConnTypeWS,
		sendCh:      make(chan protocol.WSMessage, webSendChBufSize),
		done:        make(chan struct{}),
		id:          "agent-overflow-client",
		routeReplay: true,
	}
	wc.hub.addClient(client.id, client)
	if !wc.subscribeAndReplay(client, sel, "agent", 0, true) {
		t.Fatal("Agent overflow replay subscription failed")
	}
	resync := <-client.sendCh
	if resync.Type != protocol.MsgTypeResyncRequired || resync.Channel != "agent" || resync.RouteChannel != "cli" || resync.ChatID != sel.ChatID {
		t.Fatalf("Agent resync signal = %#v", resync)
	}
	progress := <-client.sendCh
	if progress.Type != protocol.MsgTypeProgress || progress.Channel != "agent" || progress.RouteChannel != "cli" || progress.Progress == nil || progress.Progress.Iteration != 3 {
		t.Fatalf("Agent overflow progress fallback = %#v", progress)
	}
	ask := <-client.sendCh
	if ask.Type != protocol.MsgTypeAskUser || ask.Channel != "agent" || ask.RouteChannel != "cli" || ask.Progress == nil || ask.Progress.RequestID != "request-overflow" {
		t.Fatalf("Agent overflow AskUser fallback = %#v", ask)
	}
}

func TestSSEReplayOverflowEmitsResyncEvent(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	sel := SessionSelector{Channel: "web", ChatID: "web-1"}
	for i := 0; i < eventStreamSize+1; i++ {
		wc.hub.sendToSession(sel.Channel, sel.ChatID, protocol.WSMessage{
			Type: protocol.MsgTypeText, Content: strconv.Itoa(i),
		})
	}
	recorder := httptest.NewRecorder()
	client := &Client{
		connType:       clientConnTypeSSE,
		w:              recorder,
		flusher:        recorder,
		sendCh:         make(chan protocol.WSMessage, 1),
		done:           make(chan struct{}),
		chatID:         sel.ChatID,
		sessionChannel: sel.Channel,
	}
	if closed, err := wc.catchUpSSE(context.Background(), client, nil); err != nil || closed {
		t.Fatalf("SSE catch-up = closed %v, err %v", closed, err)
	}
	if !strings.Contains(recorder.Body.String(), "event:"+protocol.MsgTypeResyncRequired) {
		t.Fatalf("SSE overflow response did not contain resync event: %s", recorder.Body.String())
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

func assertSSEResyncControl(t *testing.T, event map[string]string, channel, chatID string) protocol.WSMessage {
	t.Helper()
	if event["event"] != protocol.MsgTypeResyncRequired {
		t.Fatalf("SSE resync control = %#v", event)
	}
	var msg protocol.WSMessage
	if err := json.Unmarshal([]byte(event["data"]), &msg); err != nil {
		t.Fatalf("decode SSE resync control: %v", err)
	}
	if msg.Type != protocol.MsgTypeResyncRequired || msg.Channel != channel || msg.ChatID != chatID || msg.Seq != 0 || msg.Metadata["baseline_seq"] == "" {
		t.Fatalf("SSE resync message = %#v", msg)
	}
	if event["id"] != msg.Metadata["baseline_seq"] {
		t.Fatalf("SSE resync cursor = %q, baseline = %q", event["id"], msg.Metadata["baseline_seq"])
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

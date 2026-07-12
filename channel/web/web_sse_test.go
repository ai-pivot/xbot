package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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
		connType: clientConnTypeSSE,
		sendCh:   make(chan protocol.WSMessage, 1),
		done:     make(chan struct{}),
		chatID:   chatID,
		id:       "sse-full-progress",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, chatID)
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

func TestSSEPendingAskUserFallbackRevalidatesRequest(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	requestIDs := []string{"request-old", "request-new"}
	lookup := 0
	wc.SetCallbacks(WebCallbacks{
		GetPendingAskUser: func(channel, gotChatID string) *protocol.ProgressEvent {
			requestID := requestIDs[lookup]
			lookup++
			return &protocol.ProgressEvent{RequestID: requestID}
		},
	})

	wc.publishSSEFallbacks(SessionSelector{Channel: "web", ChatID: chatID}, 0)

	if lookup != 2 {
		t.Fatalf("pending AskUser lookup count = %d, want 2", lookup)
	}
	if got := wc.getEventStream(chatID).lastSeq(); got != 0 {
		t.Fatalf("last sequence = %d, want 0 for stale AskUser fallback", got)
	}
}

func TestSSEPendingAskUserClearWinsBeforePublication(t *testing.T) {
	wc, _ := newTestWebChannel(t, nil)
	chatID := "web-1"
	var pendingMu sync.Mutex
	pending := &protocol.ProgressEvent{RequestID: "request-1"}
	lookupCount := 0
	lookedUp := make(chan struct{}, 3)
	wc.SetCallbacks(WebCallbacks{
		GetPendingAskUser: func(channel, gotChatID string) *protocol.ProgressEvent {
			pendingMu.Lock()
			lookupCount++
			var result *protocol.ProgressEvent
			if pending != nil {
				copy := *pending
				result = &copy
			}
			pendingMu.Unlock()
			lookedUp <- struct{}{}
			return result
		},
	})

	wc.hub.seqMu.Lock()
	done := make(chan struct{})
	go func() {
		wc.publishSSEFallbacks(SessionSelector{Channel: "web", ChatID: chatID}, 0)
		close(done)
	}()
	<-lookedUp
	<-lookedUp
	pendingMu.Lock()
	pending = nil
	pendingMu.Unlock()
	wc.hub.seqMu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pending AskUser publication did not finish")
	}

	pendingMu.Lock()
	gotLookups := lookupCount
	pendingMu.Unlock()
	if gotLookups != 3 {
		t.Fatalf("pending AskUser lookup count = %d, want 3", gotLookups)
	}
	if got := wc.getEventStream(chatID).lastSeq(); got != 0 {
		t.Fatalf("last sequence = %d, want 0 after pending AskUser clear", got)
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
		connType: clientConnTypeSSE,
		sendCh:   make(chan protocol.WSMessage, 1),
		done:     make(chan struct{}),
		chatID:   chatID,
		id:       "sse-progress-order",
	}
	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, chatID)
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
	wc.hub.subscribe(client1.id, "web-1")
	wc.hub.subscribe(client2.id, "web-2")

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
		if msg.Seq != 0 {
			t.Fatalf("CLI WS session seq = %d, want 0", msg.Seq)
		}
	default:
		t.Fatal("CLI WS broadcast behavior changed")
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

	sseMsg := <-sseClient.sendCh
	if sseMsg.Type != protocol.MsgTypeStreamContent || sseMsg.Seq != 1 {
		t.Fatalf("SSE sparse progress = %#v", sseMsg)
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

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	log "xbot/logger"
	"xbot/protocol"

	"github.com/google/uuid"
)

const (
	sseHeartbeatInterval = 15 * time.Second
	sseWriteTimeout      = 2 * time.Second
)

// handleSSE streams server events for one authenticated Web session.
func (wc *WebChannel) handleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	chatID := strings.TrimSpace(r.URL.Query().Get("chat_id"))
	if chatID == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "chat_id is required")
		return
	}
	sel, ok := wc.resolveSSESession(w, r, senderID, chatID)
	if !ok {
		return
	}
	routeKey := sessionRouteKey(sel.Channel, sel.ChatID)

	lastSeq, hasResumeCursor, err := sseResumeCursor(r)
	if err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid SSE resume cursor")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonErrorResponse(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	client := &Client{
		connType:       clientConnTypeSSE,
		w:              w,
		flusher:        flusher,
		sendCh:         make(chan protocol.WSMessage, webSendChBufSize),
		done:           make(chan struct{}),
		hub:            wc.hub,
		userID:         senderID,
		chatID:         chatID,
		sessionChannel: sel.Channel,
		id:             strings.ReplaceAll(uuid.New().String(), "-", ""),
		lastSentSeq:    lastSeq,
		statelessSig:   make(chan struct{}, 1),
	}

	// Sequence high-water selection and subscription are one transaction: an
	// event is either below the fresh baseline or delivered after subscription.
	wc.hub.seqMu.Lock()
	streamLastSeq := wc.getEventStream(routeKey).lastSeq()
	forceResync := hasResumeCursor && lastSeq > 0 && lastSeq >= streamLastSeq
	if !hasResumeCursor {
		client.lastSentSeq = streamLastSeq
	} else if client.lastSentSeq > streamLastSeq {
		// The server restarted and its in-memory sequence restarted from zero.
		client.lastSentSeq = 0
	}
	registered := wc.hub.addClient(client.id, client)
	subscribed := registered && wc.hub.subscribe(client.id, routeKey)
	wc.hub.seqMu.Unlock()
	if !subscribed {
		if registered {
			wc.hub.removeClient(client.id)
		}
		return
	}
	defer func() {
		client.closeDone()
		wc.hub.removeClient(client.id)
		log.WithFields(log.Fields{
			"sender_id": senderID,
			"chat_id":   chatID,
			"client_id": client.id,
		}).Info("SSE client disconnected")
	}()

	log.WithFields(log.Fields{
		"sender_id": senderID,
		"chat_id":   chatID,
		"client_id": client.id,
	}).Info("SSE client connected")

	stopWriteWatcher := watchSSEWriteCancellation(r.Context(), client)
	defer stopWriteWatcher()
	if sseContextError(r.Context(), client) != nil {
		return
	}

	// A fresh EventSource has no reconnect cursor until it receives an id field.
	// Publish the selected high-water mark even when no business event is ready.
	if forceResync {
		if err := writeSSEResyncRequired(client, sel); err != nil {
			return
		}
	} else if !hasResumeCursor {
		if err := writeSSECursor(client, client.lastSentSeq); err != nil {
			return
		}
	} else if err := flushSSEResponse(client); err != nil {
		return
	}
	wc.publishSSEFallbacks(sel, client.lastSentSeq)
	if sseContextError(r.Context(), client) != nil {
		return
	}
	wc.sseWriteLoopCore(r.Context(), client)
}

func (wc *WebChannel) resolveSSESession(w http.ResponseWriter, r *http.Request, senderID, chatID string) (SessionSelector, bool) {
	channelName := strings.TrimSpace(r.URL.Query().Get("channel"))
	var sel SessionSelector
	if channelName != "" {
		sel = SessionSelector{Channel: channelName, ChatID: chatID}
	} else {
		sel = wc.GetCurrentSession(senderID)
	}
	if sel.ChatID != chatID {
		sel = SessionSelector{Channel: "web", ChatID: chatID}
		if webChatIDLooksLikeSubAgent(chatID) {
			sel.Channel = "agent"
		}
	}
	if !wc.canAccessSession(r.Context(), userIDFromContext(r.Context()), senderID, sel.Channel, sel.ChatID) {
		jsonErrorResponse(w, http.StatusForbidden, "access denied")
		return SessionSelector{}, false
	}
	return sel, true
}

func parseLastEventID(raw string) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	return strconv.ParseUint(raw, 10, 64)
}

func sseResumeCursor(r *http.Request) (uint64, bool, error) {
	if len(r.Header.Values("Last-Event-ID")) > 0 {
		seq, err := parseLastEventID(r.Header.Get("Last-Event-ID"))
		return seq, true, err
	}
	if _, ok := r.URL.Query()["last_event_id"]; ok {
		seq, err := parseLastEventID(r.URL.Query().Get("last_event_id"))
		return seq, true, err
	}
	return 0, false, nil
}

func (wc *WebChannel) replaySSEEvents(sel SessionSelector, lastSeq uint64) []protocol.WSMessage {
	events, _ := wc.replaySSEWindow(sel, lastSeq)
	return events
}

func (wc *WebChannel) replaySSEWindow(sel SessionSelector, lastSeq uint64) ([]protocol.WSMessage, uint64) {
	events, evictedThrough := wc.getEventStream(sessionRouteKey(sel.Channel, sel.ChatID)).replayAfter(lastSeq)
	sort.SliceStable(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	return events, evictedThrough
}

func (wc *WebChannel) publishSSEFallbacks(sel SessionSelector, lastSeq uint64) {
	events := wc.replaySSEEvents(sel, lastSeq)
	if !containsSSEEvent(events, protocol.MsgTypeProgress, "") && wc.callbacks.GetActiveProgress != nil {
		if progress := wc.callbacks.GetActiveProgress(sel.Channel, sel.ChatID); progress != nil {
			if current := wc.callbacks.GetActiveProgress(sel.Channel, sel.ChatID); current != nil {
				wc.publishSSEFallbackIfMissing(sel, lastSeq, protocol.WSMessage{
					Type:     protocol.MsgTypeProgress,
					TS:       time.Now().Unix(),
					Progress: current,
				}, "")
			}
		}
	}

	if wc.callbacks.WithPendingAskUser != nil {
		wc.callbacks.WithPendingAskUser(sel.Channel, sel.ChatID, func(current *protocol.ProgressEvent) bool {
			return wc.publishSSEFallbackIfMissing(sel, lastSeq, protocol.WSMessage{
				Type:     protocol.MsgTypeAskUser,
				TS:       time.Now().Unix(),
				ChatID:   sel.ChatID,
				Progress: current,
			}, current.RequestID)
		})
	}
}

func (wc *WebChannel) publishSSEFallbackIfMissing(sel SessionSelector, lastSeq uint64, msg protocol.WSMessage, requestID string) bool {
	return wc.hub.sendSSEEventIf(sessionRouteKey(sel.Channel, sel.ChatID), func() (protocol.WSMessage, bool) {
		events := wc.replaySSEEvents(sel, lastSeq)
		if containsSSEEvent(events, msg.Type, requestID) {
			return protocol.WSMessage{}, false
		}
		// A destructive reset ends the old replay epoch. Never synthesize a
		// snapshot or pending prompt from the deleted branch across that barrier.
		if containsSSEReplayBarrier(events) {
			return protocol.WSMessage{}, false
		}
		switch msg.Type {
		case protocol.MsgTypeProgress:
			progress, ok := selectSSEProgressFallback(msg.Progress, wc.replaySSEEvents(sel, 0))
			if !ok {
				return protocol.WSMessage{}, false
			}
			msg.Progress = progress
		case protocol.MsgTypeAskUser:
			if msg.Progress == nil || msg.Progress.RequestID != requestID {
				return protocol.WSMessage{}, false
			}
		}
		return msg, true
	})
}

func selectSSEProgressFallback(snapshot *protocol.ProgressEvent, events []protocol.WSMessage) (*protocol.ProgressEvent, bool) {
	if snapshot == nil {
		return nil, false
	}
	state := ""
	var stateSeq uint64
	var latestProgress *protocol.WSMessage
	for _, event := range events {
		if isSSEReplayBarrier(event) {
			state = "reset"
			stateSeq = event.Seq
			latestProgress = nil
			continue
		}
		if event.Type == protocol.MsgTypeProgress && event.Progress != nil {
			eventCopy := event
			latestProgress = &eventCopy
		}
		if event.Type == protocol.MsgTypeSession && event.Session != nil {
			switch event.Session.Action {
			case "busy", "idle":
				state = event.Session.Action
				stateSeq = event.Seq
			}
		}
	}
	if state == "idle" || (state == "busy" || state == "reset") && (latestProgress == nil || latestProgress.Seq < stateSeq) {
		return nil, false
	}
	if latestProgress != nil && snapshot.Seq != latestProgress.Progress.Seq {
		progressCopy := *latestProgress.Progress
		return &progressCopy, true
	}
	return snapshot, true
}

func containsSSEReplayBarrier(events []protocol.WSMessage) bool {
	for _, event := range events {
		if isSSEReplayBarrier(event) {
			return true
		}
	}
	return false
}

func isSSEReplayBarrier(event protocol.WSMessage) bool {
	return event.SessionReset || event.Type == protocol.MsgTypeSession && event.Session != nil && event.Session.Action == "history_rewound"
}

func containsSSEEvent(events []protocol.WSMessage, msgType, requestID string) bool {
	for _, event := range events {
		if event.Type != msgType {
			continue
		}
		if msgType != protocol.MsgTypeAskUser || requestID == "" || askUserRequestID(event) == requestID {
			return true
		}
	}
	return false
}

func askUserRequestID(msg protocol.WSMessage) string {
	if msg.Progress != nil && msg.Progress.RequestID != "" {
		return msg.Progress.RequestID
	}
	var event protocol.AskUserEvent
	if json.Unmarshal([]byte(msg.Content), &event) == nil {
		return event.RequestID
	}
	return ""
}

func (wc *WebChannel) sseWriteLoop(ctx context.Context, client *Client) {
	stopWriteWatcher := watchSSEWriteCancellation(ctx, client)
	defer stopWriteWatcher()
	wc.sseWriteLoopCore(ctx, client)
}

func (wc *WebChannel) sseWriteLoopCore(ctx context.Context, client *Client) {
	ticker := time.NewTicker(sseHeartbeatInterval)
	defer ticker.Stop()

	if closed, err := wc.catchUpSSE(ctx, client, nil); err != nil || closed {
		return
	}

	for {
		select {
		case <-client.statelessSig:
			client.drainStateless()
			if closed, err := wc.catchUpSSE(ctx, client, nil); err != nil || closed {
				return
			}
		case msg, ok := <-client.sendCh:
			if !ok {
				return
			}
			if closed, err := wc.catchUpSSE(ctx, client, []protocol.WSMessage{msg}); err != nil || closed {
				return
			}
		case <-ticker.C:
			if err := writeSSEHeartbeat(client); err != nil {
				return
			}
		case <-ctx.Done():
			return
		case <-client.done:
			return
		}
	}
}

func watchSSEWriteCancellation(ctx context.Context, client *Client) func() {
	stopped := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
		case <-client.done:
		case <-stopped:
			return
		}
		client.sseWriteCanceled.Store(true)
		_ = http.NewResponseController(client.w).SetWriteDeadline(time.Now())
	}()
	return func() {
		close(stopped)
		<-finished
	}
}

func (wc *WebChannel) catchUpSSE(ctx context.Context, client *Client, initial []protocol.WSMessage) (bool, error) {
	pending := initial
	for {
		if err := sseContextError(ctx, client); err != nil {
			return false, err
		}
		sel := SessionSelector{Channel: client.sessionChannel, ChatID: client.chatID}
		events, evictedThrough := wc.replaySSEWindow(sel, client.lastSentSeq)
		if evictedThrough > client.lastSentSeq {
			pending = append(pending, protocol.WSMessage{
				Type:         protocol.MsgTypeResyncRequired,
				Seq:          evictedThrough,
				Channel:      sel.Channel,
				ChatID:       sel.ChatID,
				RouteChannel: sel.Channel,
				RouteChatID:  sel.ChatID,
			})
		}
		pending = append(pending, events...)
		queued, closed := collectSSEBatch(client.sendCh)
		pending = append(pending, queued...)
		if len(pending) == 0 {
			return closed, nil
		}
		if err := wc.writeSSEBatch(ctx, client, pending); err != nil {
			return closed, err
		}
		if closed {
			return true, nil
		}
		pending = nil
	}
}

func sseContextError(ctx context.Context, client *Client) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-client.done:
		return context.Canceled
	default:
		return nil
	}
}

func collectSSEBatch(ch <-chan protocol.WSMessage) ([]protocol.WSMessage, bool) {
	batch := make([]protocol.WSMessage, 0, cap(ch))
	for drained := 0; drained < cap(ch); drained++ {
		select {
		case msg, ok := <-ch:
			if !ok {
				return batch, true
			}
			batch = append(batch, msg)
		default:
			return batch, false
		}
	}
	return batch, false
}

func (wc *WebChannel) writeSSEBatch(ctx context.Context, client *Client, batch []protocol.WSMessage) error {
	sort.SliceStable(batch, func(i, j int) bool { return batch[i].Seq < batch[j].Seq })
	for _, msg := range batch {
		if err := sseContextError(ctx, client); err != nil {
			return err
		}
		if err := wc.writeCurrentSSEEvent(client, msg); err != nil {
			return err
		}
	}
	return nil
}

func (wc *WebChannel) writeCurrentSSEEvent(client *Client, msg protocol.WSMessage) error {
	if msg.Type != protocol.MsgTypeAskUser || msg.Seq == 0 || msg.Seq <= client.lastSentSeq {
		return writeSSEEvent(client, msg)
	}

	requestID := askUserRequestID(msg)
	var currentEvent protocol.WSMessage
	current := requestID != "" && wc.callbacks.WithPendingAskUser != nil &&
		wc.callbacks.WithPendingAskUser(client.sessionChannel, client.chatID, func(pending *protocol.ProgressEvent) bool {
			if pending.RequestID != requestID {
				return false
			}
			msg.Progress = pending
			currentEvent = msg
			return true
		})
	if current {
		return writeSSEEvent(client, currentEvent)
	}
	// A resolved prompt must not remain at the replay cursor forever. Treat
	// it as consumed while omitting it from the response stream.
	client.lastSentSeq = msg.Seq
	return nil
}

func writeSSEEvent(client *Client, msg protocol.WSMessage) error {
	if msg.Seq == 0 {
		return fmt.Errorf("SSE event %q has no sequence", msg.Type)
	}
	if msg.Seq <= client.lastSentSeq {
		return nil
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal SSE event: %w", err)
	}
	armSSEWriteDeadline(client)
	defer clearSSEWriteDeadline(client)
	if _, err := fmt.Fprintf(client.w, "id:%d\nevent:%s\ndata:%s\n\n", msg.Seq, msg.Type, data); err != nil {
		return fmt.Errorf("write SSE event: %w", err)
	}
	if err := flushSSE(client); err != nil {
		return err
	}
	client.lastSentSeq = msg.Seq
	return nil
}

func writeSSEHeartbeat(client *Client) error {
	armSSEWriteDeadline(client)
	defer clearSSEWriteDeadline(client)
	if _, err := io.WriteString(client.w, ":heartbeat\n\n"); err != nil {
		return fmt.Errorf("write SSE heartbeat: %w", err)
	}
	return flushSSE(client)
}

func writeSSECursor(client *Client, seq uint64) error {
	armSSEWriteDeadline(client)
	defer clearSSEWriteDeadline(client)
	if _, err := fmt.Fprintf(client.w, "id:%d\n\n", seq); err != nil {
		return fmt.Errorf("write SSE cursor: %w", err)
	}
	return flushSSE(client)
}

func writeSSEResyncRequired(client *Client, sel SessionSelector) error {
	msg := protocol.WSMessage{
		Type:         protocol.MsgTypeResyncRequired,
		Channel:      sel.Channel,
		ChatID:       sel.ChatID,
		RouteChannel: sel.Channel,
		RouteChatID:  sel.ChatID,
		Metadata: map[string]string{
			"baseline_seq": strconv.FormatUint(client.lastSentSeq, 10),
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal SSE resync control: %w", err)
	}
	armSSEWriteDeadline(client)
	defer clearSSEWriteDeadline(client)
	if _, err := fmt.Fprintf(client.w, "id:%d\nevent:%s\ndata:%s\n\n", client.lastSentSeq, msg.Type, data); err != nil {
		return fmt.Errorf("write SSE resync control: %w", err)
	}
	return flushSSE(client)
}

func flushSSEResponse(client *Client) error {
	armSSEWriteDeadline(client)
	defer clearSSEWriteDeadline(client)
	return flushSSE(client)
}

func flushSSE(client *Client) error {
	if err := http.NewResponseController(client.w).Flush(); err != nil {
		return fmt.Errorf("flush SSE response: %w", err)
	}
	return nil
}

func armSSEWriteDeadline(client *Client) {
	controller := http.NewResponseController(client.w)
	_ = controller.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
	if client.sseWriteCanceled.Load() {
		_ = controller.SetWriteDeadline(time.Now())
	}
}

func clearSSEWriteDeadline(client *Client) {
	controller := http.NewResponseController(client.w)
	if client.sseWriteCanceled.Load() {
		_ = controller.SetWriteDeadline(time.Now())
		return
	}
	_ = controller.SetWriteDeadline(time.Time{})
}

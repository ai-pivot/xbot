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

const sseHeartbeatInterval = 15 * time.Second

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

	lastSeq, err := parseLastEventID(r.Header.Get("Last-Event-ID"))
	if err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid Last-Event-ID")
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
	// SSE responses are intentionally long-lived; keep the server's REST write
	// timeout while clearing it only for this response.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	client := &Client{
		connType:    clientConnTypeSSE,
		w:           w,
		flusher:     flusher,
		sendCh:      make(chan protocol.WSMessage, webSendChBufSize),
		done:        make(chan struct{}),
		hub:         wc.hub,
		userID:      senderID,
		chatID:      chatID,
		id:          strings.ReplaceAll(uuid.New().String(), "-", ""),
		lastSentSeq: lastSeq,
	}
	if streamLastSeq := wc.getEventStream(chatID).lastSeq(); lastSeq > streamLastSeq {
		client.lastSentSeq = 0
	}

	wc.hub.addClient(client.id, client)
	wc.hub.subscribe(client.id, chatID)
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

	// Commit the response headers immediately even when no event is ready yet.
	flusher.Flush()
	// eventStream is also the initial-connect source of truth. Replaying from
	// zero closes the gap between the history request and Hub subscription;
	// clients already holding history deduplicate by the same sequence IDs.
	wc.publishSSEFallbacks(sel, client.lastSentSeq)
	wc.sseWriteLoop(r.Context(), client)
}

func (wc *WebChannel) resolveSSESession(w http.ResponseWriter, r *http.Request, senderID, chatID string) (SessionSelector, bool) {
	sel := wc.GetCurrentSession(senderID)
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

func (wc *WebChannel) replaySSEEvents(sel SessionSelector, lastSeq uint64) []protocol.WSMessage {
	events := wc.getEventStream(sel.ChatID).eventsAfter(lastSeq)
	sort.SliceStable(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	return events
}

func (wc *WebChannel) publishSSEFallbacks(sel SessionSelector, lastSeq uint64) {
	events := wc.replaySSEEvents(sel, lastSeq)
	if !containsSSEEvent(events, protocol.MsgTypeProgress, "") && wc.callbacks.GetActiveProgress != nil {
		if progress := wc.callbacks.GetActiveProgress(sel.Channel, sel.ChatID); progress != nil {
			wc.publishSSEFallbackIfMissing(sel, lastSeq, protocol.WSMessage{
				Type:     protocol.MsgTypeProgress,
				TS:       time.Now().Unix(),
				Progress: progress,
			}, "")
		}
	}

	if wc.callbacks.GetPendingAskUser != nil {
		if progress := wc.callbacks.GetPendingAskUser(sel.Channel, sel.ChatID); progress != nil {
			wc.publishSSEFallbackIfMissing(sel, lastSeq, protocol.WSMessage{
				Type:     protocol.MsgTypeAskUser,
				TS:       time.Now().Unix(),
				ChatID:   sel.ChatID,
				Progress: progress,
			}, progress.RequestID)
		}
	}
}

func (wc *WebChannel) publishSSEFallbackIfMissing(sel SessionSelector, lastSeq uint64, msg protocol.WSMessage, requestID string) {
	// Multiple reconnects can ask for the same fallback concurrently. Serialize
	// the final check and publish so they share one eventStream sequence.
	wc.sseFallbackMu.Lock()
	defer wc.sseFallbackMu.Unlock()
	if containsSSEEvent(wc.replaySSEEvents(sel, lastSeq), msg.Type, requestID) {
		return
	}
	wc.hub.sendToClient(sel.ChatID, msg)
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
	ticker := time.NewTicker(sseHeartbeatInterval)
	defer ticker.Stop()

	if closed, err := wc.catchUpSSE(client, nil); err != nil || closed {
		return
	}

	for {
		select {
		case msg, ok := <-client.sendCh:
			if !ok {
				return
			}
			if closed, err := wc.catchUpSSE(client, []protocol.WSMessage{msg}); err != nil || closed {
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

func (wc *WebChannel) catchUpSSE(client *Client, initial []protocol.WSMessage) (bool, error) {
	pending := initial
	for {
		pending = append(pending, wc.getEventStream(client.chatID).eventsAfter(client.lastSentSeq)...)
		queued, closed := collectSSEBatch(client.sendCh)
		pending = append(pending, queued...)
		if len(pending) == 0 {
			return closed, nil
		}
		if err := writeSSEBatch(client, pending); err != nil {
			return closed, err
		}
		if closed {
			return true, nil
		}
		pending = nil
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

func writeSSEBatch(client *Client, batch []protocol.WSMessage) error {
	sort.SliceStable(batch, func(i, j int) bool { return batch[i].Seq < batch[j].Seq })
	for _, msg := range batch {
		if err := writeSSEEvent(client, msg); err != nil {
			return err
		}
	}
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
	if _, err := fmt.Fprintf(client.w, "id:%d\nevent:%s\ndata:%s\n\n", msg.Seq, msg.Type, data); err != nil {
		return fmt.Errorf("write SSE event: %w", err)
	}
	client.flusher.Flush()
	client.lastSentSeq = msg.Seq
	return nil
}

func writeSSEHeartbeat(client *Client) error {
	if _, err := io.WriteString(client.w, ":heartbeat\n\n"); err != nil {
		return fmt.Errorf("write SSE heartbeat: %w", err)
	}
	client.flusher.Flush()
	return nil
}

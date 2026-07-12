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
	replay := wc.replaySSEEvents(sel, client.lastSentSeq)
	wc.sseWriteLoop(r.Context(), client, replay)
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
	replayedProgress := false
	for _, event := range events {
		if event.Type == protocol.MsgTypeProgress {
			replayedProgress = true
		}
	}

	if !replayedProgress && wc.callbacks.GetActiveProgress != nil {
		if progress := wc.callbacks.GetActiveProgress(sel.Channel, sel.ChatID); progress != nil {
			events = append(events, wc.hub.sequenceEvent(sel.ChatID, protocol.WSMessage{
				Type:     protocol.MsgTypeProgress,
				TS:       time.Now().Unix(),
				Progress: progress,
			}))
		}
	}

	if wc.callbacks.GetPendingAskUser != nil {
		if progress := wc.callbacks.GetPendingAskUser(sel.Channel, sel.ChatID); progress != nil {
			events = append(events, wc.hub.sequenceEvent(sel.ChatID, protocol.WSMessage{
				Type:     protocol.MsgTypeAskUser,
				TS:       time.Now().Unix(),
				ChatID:   sel.ChatID,
				Progress: progress,
			}))
		}
	}
	return events
}

func (wc *WebChannel) sseWriteLoop(ctx context.Context, client *Client, initial []protocol.WSMessage) {
	ticker := time.NewTicker(sseHeartbeatInterval)
	defer ticker.Stop()

	batch, closed := collectSSEBatch(client.sendCh, initial)
	if err := writeSSEBatch(client, batch); err != nil || closed {
		return
	}

	for {
		select {
		case msg, ok := <-client.sendCh:
			if !ok {
				return
			}
			batch, closed := collectSSEBatch(client.sendCh, []protocol.WSMessage{msg})
			if err := writeSSEBatch(client, batch); err != nil || closed {
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

func collectSSEBatch(ch <-chan protocol.WSMessage, initial []protocol.WSMessage) ([]protocol.WSMessage, bool) {
	batch := append([]protocol.WSMessage(nil), initial...)
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

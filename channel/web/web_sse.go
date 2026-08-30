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
	"sync"
	"time"

	log "xbot/logger"
	"xbot/protocol"

	"github.com/google/uuid"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
)

const (
	sseHeartbeatInterval = 15 * time.Second
	sseWriteTimeout      = 2 * time.Second
)

// SSE encoder pools (zstd + gzip). Encoders are expensive to create;
// pool them for reuse across SSE connections.
var sseZstdPool = sync.Pool{
	New: func() interface{} {
		enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
		return enc
	},
}

var sseGzipPool = sync.Pool{
	New: func() interface{} {
		w, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression)
		return w
	},
}

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

	// SSE compression: detect Accept-Encoding and wrap the writer.
	// zstd preferred (20x compression on SSE), gzip fallback (Safari etc.).
	// The encoder wraps client.w — all fmt.Fprintf(client.w, ...) calls
	// go through compression automatically. flushSSE flushes the encoder
	// first (flush compressed block to TCP), then the underlying writer.
	acceptEnc := r.Header.Get("Accept-Encoding")
	var sseWriter io.Writer = w
	var sseEncFlush func() error
	var sseEncClose func()
	if strings.Contains(acceptEnc, "zstd") {
		enc := sseZstdPool.Get().(*zstd.Encoder)
		enc.Reset(w)
		sseWriter = enc
		sseEncFlush = func() error { return enc.Flush() }
		sseEncClose = func() { enc.Close(); sseZstdPool.Put(enc) }
		w.Header().Set("Content-Encoding", "zstd")
		w.Header().Set("Vary", "Accept-Encoding")
	} else if strings.Contains(acceptEnc, "gzip") {
		enc := sseGzipPool.Get().(*gzip.Writer)
		enc.Reset(w)
		sseWriter = enc
		sseEncFlush = func() error { return enc.Flush() }
		sseEncClose = func() { enc.Close(); sseGzipPool.Put(enc) }
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
	}

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
		sseEncWriter:   sseWriter,
		sseEncFlush:    sseEncFlush,
		sseEncClose:    sseEncClose,
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
	// Defer cleanup BEFORE addClient/subscribe — if subscribe fails (early
	// return below), the encoder must still be released to the pool.
	defer func() {
		client.closeDone()
		if client.sseEncClose != nil {
			client.sseEncClose()
		}
		wc.hub.removeClient(client.id)
		log.WithFields(log.Fields{
			"sender_id": senderID,
			"chat_id":   chatID,
			"client_id": client.id,
		}).Info("SSE client disconnected")
	}()
	registered := wc.hub.addClient(client.id, client)
	subscribed := registered && wc.hub.subscribe(client.id, routeKey)
	wc.hub.seqMu.Unlock()
	if !subscribed {
		if registered {
			wc.hub.removeClient(client.id)
		}
		return
	}

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
		// Two deliberate GetActiveProgress calls (NOT a redundant double fetch):
		// the first is the cheap nil gate; the second revalidates the snapshot
		// AFTER the first lookup returned — the active-progress store may have
		// gone terminal (turn ended, snapshot cleared) in between. Publishing a
		// stale thinking snapshot after turn end resurrects a dead turn in the
		// UI. Guarded by TestSSEActiveProgressFallbackRevalidatesSnapshot /
		// TestSSEActiveProgressFallbackStopsAtIdleEvent /
		// TestSSEActiveProgressFallbackHonorsIdleAtHighWater and the lock-order
		// test (the second lookup must NOT run under seqMu).
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
			// drainStateless 取出的 stateless 事件（stream_content / sync_progress /
			// runner_status）必须作为 initial 传给 catchUpSSE 发送 —— 不能丢弃。
			// 否则当 ring buffer replay 因 lastSentSeq 已追上（lastSentSeq == 最新
			// seq）而返回空时，这些 latest-wins 事件永久丢失，客户端只剩 heartbeat。
			drained := client.drainStateless()
			var initial []protocol.WSMessage
			if len(drained) > 0 {
				initial = make([]protocol.WSMessage, 0, len(drained))
				for _, m := range drained {
					if m != nil {
						initial = append(initial, *m)
					}
				}
			}
			if closed, err := wc.catchUpSSE(ctx, client, initial); err != nil || closed {
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
			// Heartbeat 带上 live 信息：SSE 事件（stream_content/progress_structured）
			// 可能因 sendCh 满 / statelessSig 信号丢失 / 连接抖动而中断，前端 stream
			// 停在最后一次事件 → 用户看到"永久卡住"。heartbeat 周期（15s）推送进行中
			// turn 的 live 快照（sync_progress，stateless latest-wins）—— 前端每 15s
			// 至少收到一次 live 状态，stream 恢复显示（不再卡死）。
			wc.sendHeartbeatLive(client)
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

// maxSSEBatchesPerCall 限制 catchUpSSE 单次调用的批次处理量。stream 高频时
// ring buffer replay 会持续返回新事件，若不加限制，catchUpSSE 的 for 循环
// 会一直占用 sseWriteLoopCore（同步调用），饿死 heartbeat ticker —— 客户端
// 只收 heartbeat、收不到新事件。处理 N 批后返回，让 select 重新调度；未发完
// 的 replay 事件会在下一次 sendCh/statelessSig 触发时继续（lastSentSeq 已推进，
// 不会丢）。
const maxSSEBatchesPerCall = 16

func (wc *WebChannel) catchUpSSE(ctx context.Context, client *Client, initial []protocol.WSMessage) (bool, error) {
	pending := initial
	for batch := 0; batch < maxSSEBatchesPerCall; batch++ {
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
	// 批次上限已到：返回让 sseWriteLoopCore 回到 select（处理 heartbeat 和
	// 新事件）。剩余 replay 事件由下次调用继续发送。
	return false, nil
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

// sseEventShouldWrite reports whether msg should be delivered on the SSE
// stream. AskUser events are delivered only while their prompt is pending;
// a resolved prompt must be treated as consumed (cursor advanced without
// emitting, reconnect must not re-announce it). Pending-existence is the
// only signal — there is exactly one pending AskUser per (channel, chatID),
// so no request-ID check is needed (a transient ID mismatch must never
// swallow a LIVE event; the producer already skips re-announcing cleared
// prompts). Shared by the single-event path (writeCurrentSSEEvent) and the
// batched path (writeSSEBatch).
func (wc *WebChannel) sseEventShouldWrite(client *Client, msg protocol.WSMessage) bool {
	if msg.Type != protocol.MsgTypeAskUser {
		return true
	}
	return wc.callbacks.WithPendingAskUser != nil &&
		wc.callbacks.WithPendingAskUser(client.sessionChannel, client.chatID, func(*protocol.ProgressEvent) bool {
			return true
		})
}

// writeSSEBatch writes a seq-sorted batch of events with ONE write deadline
// arm and ONE encoder+TCP flush at batch end. Each event is marshalled and
// Fprintf'd to the writer buffer without flushing (writeSSEEventNoFlush); the
// batch dedups by a local watermark (the same seq can appear twice in a batch
// — ring-buffer replay and the sendCh drain can both carry it, matching
// writeSSEEvent's per-event cursor rule). lastSentSeq advances only after the
// flush succeeds — a failed batch leaves the cursor at the last flushed seq
// so a reconnect replays the unsent events. Resolved AskUser prompts are
// consumed (cursor advanced, nothing written). For a 1-event batch this is
// equivalent to the old per-event writeSSEEvent cycle (one arm, one write,
// one flush).
func (wc *WebChannel) writeSSEBatch(ctx context.Context, client *Client, batch []protocol.WSMessage) error {
	sort.SliceStable(batch, func(i, j int) bool { return batch[i].Seq < batch[j].Seq })
	if len(batch) == 0 {
		return nil
	}
	armSSEWriteDeadline(client)
	defer clearSSEWriteDeadline(client)
	watermark := client.lastSentSeq
	for _, msg := range batch {
		if err := sseContextError(ctx, client); err != nil {
			return err
		}
		if !wc.sseEventShouldWrite(client, msg) {
			// Resolved prompt — treat as consumed, omit from the response
			// stream. The cursor advances via the local watermark ONLY (the
			// batch-end commit below), never here: a mid-batch write must not
			// move lastSentSeq ahead of the last flushed seq. If the batch
			// later fails, the reconnect replay re-derives consumption (the
			// resolved prompt is re-answered by sseEventShouldWrite) and
			// every unflushed event is replayed.
			watermark = msg.Seq
			continue
		}
		// Dedup within the batch: writeSSEEventNoFlush skips already-sent seqs,
		// but the cursor (client.lastSentSeq) only advances at batch end — use a
		// local watermark so a duplicate later in the same batch is skipped too.
		if msg.Seq != 0 && msg.Seq <= watermark {
			continue
		}
		if err := writeSSEEventNoFlush(client, msg); err != nil {
			return err
		}
		if msg.Seq > watermark {
			watermark = msg.Seq
		}
	}
	if err := flushSSE(client); err != nil {
		return err
	}
	// Batch is seq-sorted; commit the cursor to the highest seq processed
	// (Seq==0 control broadcasts never participate — writeSSEEventNoFlush
	// writes them without an id line, they carry no replay cursor).
	client.lastSentSeq = watermark
	return nil
}

// writeSSEEventNoFlush writes one SSE event to the writer buffer WITHOUT
// arming a write deadline, flushing, or advancing lastSentSeq. Batched
// writers (writeSSEBatch) call this per event and flush once at batch end;
// writeSSEEvent wraps it with the per-event arm/flush/clear cycle and cursor
// advance for single-event paths. Events already at/below the replay cursor
// are skipped; Seq==0 control broadcasts (web_plugin_config_changed etc.)
// carry no id: line (they never participate in Last-Event-ID resume).
func writeSSEEventNoFlush(client *Client, msg protocol.WSMessage) error {
	if msg.Seq == 0 {
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("marshal SSE event: %w", err)
		}
		if _, err := fmt.Fprintf(client.sseWriter(), "event:%s\ndata:%s\n\n", msg.Type, data); err != nil {
			return fmt.Errorf("write SSE event: %w", err)
		}
		return nil
	}
	if msg.Seq <= client.lastSentSeq {
		return nil
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal SSE event: %w", err)
	}
	if _, err := fmt.Fprintf(client.sseWriter(), "id:%d\nevent:%s\ndata:%s\n\n", msg.Seq, msg.Type, data); err != nil {
		return fmt.Errorf("write SSE event: %w", err)
	}
	return nil
}

func (wc *WebChannel) writeCurrentSSEEvent(client *Client, msg protocol.WSMessage) error {
	// AskUser: deliver while the prompt is pending; drop as consumed when it
	// has been answered/cancelled (must not remain at the replay cursor, and
	// reconnect must not re-announce a resolved prompt). Pending-existence is
	// the only signal — there is exactly one pending AskUser per
	// (channel, chatID), so no request-ID check is needed (a transient ID
	// mismatch must never swallow a LIVE event; the producer already skips
	// re-announcing cleared prompts).
	if !wc.sseEventShouldWrite(client, msg) {
		// Resolved prompt — treat as consumed, omit from the response stream.
		client.lastSentSeq = msg.Seq
		return nil
	}
	return writeSSEEvent(client, msg)
}

func writeSSEEvent(client *Client, msg protocol.WSMessage) error {
	if msg.Seq == 0 {
		// 控制面广播消息（BroadcastToWeb 的 web_plugin_config_changed /
		// web_plugin_init / web_plugin_deactivate）无 eventStream 序号 ——
		// 按"无序号控制事件"写出：无 id: 行（不参与 Last-Event-ID 续传），
		// 不推进 lastSentSeq。SSE 规范允许无 id 事件。
		// 旧实现直接报错 "has no sequence" → catchUpSSE 返回 err →
		// sseWriteLoopCore 退出并关闭连接 —— 控制广播永远到不了客户端，
		// 且活跃 SSE 连接被随机断开（插件配置热重载失效的根因）。
		armSSEWriteDeadline(client)
		defer clearSSEWriteDeadline(client)
		if err := writeSSEEventNoFlush(client, msg); err != nil {
			return err
		}
		return flushSSE(client)
	}
	if msg.Seq <= client.lastSentSeq {
		return nil
	}
	armSSEWriteDeadline(client)
	defer clearSSEWriteDeadline(client)
	if err := writeSSEEventNoFlush(client, msg); err != nil {
		return err
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
	// Send a REAL heartbeat event (not a comment line). EventSource treats
	// comment lines (":heartbeat") as connection keep-alives but fires NO JS
	// event for them — the frontend cannot detect a half-open connection
	// (server stuck / network cut without TCP reset) where heartbeats simply
	// stop arriving. A real `event: heartbeat` lets the frontend watchdog
	// update its last-activity timestamp and declare the connection dead when
	// heartbeats stop (→ reconnect + "Reconnecting…" banner).
	if _, err := io.WriteString(client.sseWriter(), "event: heartbeat\ndata: {}\n\n"); err != nil {
		return fmt.Errorf("write SSE heartbeat: %w", err)
	}
	return flushSSE(client)
}

// sendHeartbeatLive 在 heartbeat 时推送进行中 turn 的 live 快照（sync_progress）。
//
// 防止 SSE 事件丢失后前端 stream 卡住：stream_content / progress_structured 是
// 增量推送，若 sendCh 满（网络慢）或 statelessSig 信号丢失，前端收不到新事件，
// live 停在最后一次状态 → 用户看 stream 一直卡住。heartbeat 周期（15s）推送
// 当前快照（stateless latest-wins），前端每 15s 至少收到一次 live 状态更新，
// 即使增量事件丢失也能从周期快照恢复显示。
//
// 只推送"进行中"turn（Phase 非空非 done）：空闲会话普通 heartbeat 即可。
// 快照经 sendSSEEventIf 走 ring buffer（seq 分配 + 持久化），断线重连也能恢复。
func (wc *WebChannel) sendHeartbeatLive(client *Client) {
	if wc.callbacks.GetActiveProgress == nil || client.sessionChannel == "" || client.chatID == "" {
		return
	}
	sel := SessionSelector{Channel: client.sessionChannel, ChatID: client.chatID}
	p := wc.callbacks.GetActiveProgress(sel.Channel, sel.ChatID)
	if p == nil || p.Phase == "" || p.Phase == "done" {
		return // 无进行中 turn —— 普通 heartbeat
	}
	wc.hub.sendSSEEventIf(sessionRouteKey(sel.Channel, sel.ChatID), func() (protocol.WSMessage, bool) {
		return protocol.WSMessage{
			Type:         protocol.MsgTypeProgress,
			TS:           time.Now().Unix(),
			Channel:      sel.Channel,
			ChatID:       sel.ChatID,
			RouteChannel: sel.Channel,
			RouteChatID:  sel.ChatID,
			Progress:     p,
		}, true
	})
}

func writeSSECursor(client *Client, seq uint64) error {
	armSSEWriteDeadline(client)
	defer clearSSEWriteDeadline(client)
	if _, err := fmt.Fprintf(client.sseWriter(), "id:%d\n\n", seq); err != nil {
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
	if _, err := fmt.Fprintf(client.sseWriter(), "id:%d\nevent:%s\ndata:%s\n\n", client.lastSentSeq, msg.Type, data); err != nil {
		return fmt.Errorf("write SSE resync control: %w", err)
	}
	return flushSSE(client)
}

func flushSSEResponse(client *Client) error {
	armSSEWriteDeadline(client)
	defer clearSSEWriteDeadline(client)
	return flushSSE(client)
}

// sseWriter returns the SSE event writer (compressed or plain).
// Falls back to client.w if sseEncWriter is nil (tests / no compression).
func (c *Client) sseWriter() io.Writer {
	if c.sseEncWriter != nil {
		return c.sseEncWriter
	}
	return c.w
}

// flushSSE flushes the SSE encoder (if any) then the underlying writer.
func flushSSE(client *Client) error {
	// Flush encoder first (flush compressed block to underlying writer),
	// then flush the underlying writer (flush TCP).
	if client.sseEncFlush != nil {
		if err := client.sseEncFlush(); err != nil {
			return fmt.Errorf("flush SSE encoder: %w", err)
		}
	}
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

package channel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xbot/bus"
	log "xbot/logger"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// Reconnect strategy (shared constants with qq.go)
// ---------------------------------------------------------------------------

var napcatReconnectDelays = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
	60 * time.Second,
}

const napcatMaxReconnectAttempts = 100
const napcatQuickDisconnectWindow = 5 * time.Second
const napcatQuickDisconnectCount = 3
const napcatConnectTimeout = 30 * time.Second    // Max wait for initial WebSocket connection
const napcatReconnectInterval = 60 * time.Second // Pause between reconnect attempts

// ---------------------------------------------------------------------------
// NapCatConfig Configuration
// ---------------------------------------------------------------------------

// NapCatConfig: NapCat (OneBot 11) channel configuration
type NapCatConfig struct {
	Enabled   bool
	WSUrl     string   // NapCat WebSocket URL, e.g. "ws://localhost:3001"
	Token     string   // Auth token (optional)
	AllowFrom []string // Allowed QQ number whitelist (empty = allow all)
}

// ---------------------------------------------------------------------------
// NapCatChannel Implementation
// ---------------------------------------------------------------------------

// NapCatChannel: NapCat (OneBot 11) channel implementation
type NapCatChannel struct {
	WSChannelBase

	config NapCatConfig
	msgBus *bus.MessageBus

	running  atomic.Bool
	stopOnce sync.Once

	// API request-response matching
	pending   map[string]chan json.RawMessage // echo -> response channel
	pendingMu sync.Mutex

	// Bot's own QQ number (obtained from events)
	selfID atomic.Int64

	// Chat type cache (chatID → "group"/"private")
	chatTypeCache sync.Map
}

// NewNapCatChannel Create NapCat channel
func NewNapCatChannel(cfg NapCatConfig, msgBus *bus.MessageBus) *NapCatChannel {
	return &NapCatChannel{
		WSChannelBase: NewWSChannelBase(1000, napcatQuickDisconnectWindow, napcatQuickDisconnectCount),
		config:        cfg,
		msgBus:        msgBus,
		pending:       make(map[string]chan json.RawMessage),
	}
}

func (n *NapCatChannel) Name() string { return "napcat" }

// ---------------------------------------------------------------------------
// Start / Stop
// ---------------------------------------------------------------------------

// Start Start NapCat channel, blocks until Stop is called
func (n *NapCatChannel) Start() error {
	if n.config.WSUrl == "" {
		return fmt.Errorf("napcat: ws_url is required")
	}

	n.running.Store(true)
	log.WithField("ws_url", n.config.WSUrl).Info("NapCat bot starting...")

	attempt := 0
	for n.running.Load() {
		if attempt >= napcatMaxReconnectAttempts {
			return fmt.Errorf("napcat: exceeded max reconnect attempts (%d)", napcatMaxReconnectAttempts)
		}

		connectStart := time.Now()
		err := n.connectAndRun()
		if !n.running.Load() {
			return nil // graceful shutdown
		}
		// Connection lasting over 30s indicates it's not an immediate disconnect, reset counter
		if time.Since(connectStart) > napcatConnectTimeout {
			attempt = 0
		}

		if err != nil {
			log.WithError(err).Warn("NapCat: WebSocket session ended")
		}

		// Quick disconnect detection
		if n.isQuickDisconnectLoop() {
			log.Warn("NapCat: rapid disconnect loop detected, waiting 60s")
			if !n.sleepOrStop(napcatReconnectInterval) {
				return nil
			}
			attempt++
			continue
		}

		delay := napcatReconnectDelays[attempt%len(napcatReconnectDelays)]
		if attempt >= len(napcatReconnectDelays) {
			delay = napcatReconnectDelays[len(napcatReconnectDelays)-1]
		}

		log.WithFields(log.Fields{
			"attempt": attempt + 1,
			"delay":   delay,
		}).Info("NapCat: reconnecting...")

		if !n.sleepOrStop(delay) {
			return nil
		}
		attempt++
	}
	return nil
}

// Stop Stop NapCat channel
func (n *NapCatChannel) Stop() {
	n.stopOnce.Do(func() {
		n.running.Store(false)
		close(n.stopCh)
		n.closeConn()
		n.clearPending()
		log.Info("NapCat bot stopped")
	})
}

// ---------------------------------------------------------------------------
// Connect and run main loop
// ---------------------------------------------------------------------------

// connectAndRun Establish WebSocket connection and run message loop, returns when connection drops
func (n *NapCatChannel) connectAndRun() error {
	header := http.Header{}
	if n.config.Token != "" {
		header.Set("Authorization", "Bearer "+n.config.Token)
	}

	conn, _, err := websocket.DefaultDialer.Dial(n.config.WSUrl, header)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}

	n.connMu.Lock()
	n.conn = conn
	n.connMu.Unlock()

	defer n.closeConn()

	connectTime := time.Now()
	log.WithField("ws_url", n.config.WSUrl).Info("NapCat: WebSocket connected")

	// Read messages
	for n.running.Load() {
		_, data, err := conn.ReadMessage()
		if err != nil {
			n.recordDisconnect(connectTime)
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return fmt.Errorf("ws closed: %w", err)
			}
			return fmt.Errorf("ws read: %w", err)
		}

		if err := n.handleEvent(data); err != nil {
			log.WithError(err).Warn("NapCat: event handling error")
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// OneBot 11 event types
// ---------------------------------------------------------------------------

// obEvent OneBot 11 generic event structure
type obEvent struct {
	PostType      string          `json:"post_type"`
	MessageType   string          `json:"message_type"`
	SubType       string          `json:"sub_type"`
	MetaEventType string          `json:"meta_event_type"`
	SelfID        int64           `json:"self_id"`
	Time          int64           `json:"time"`
	MessageID     int64           `json:"message_id"`
	UserID        int64           `json:"user_id"`
	GroupID       int64           `json:"group_id"`
	RawMessage    string          `json:"raw_message"`
	Message       json.RawMessage `json:"message"`
	Sender        obSender        `json:"sender"`

	// API response fields
	Status  json.RawMessage `json:"status"`
	RetCode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Echo    string          `json:"echo"`
}

// obSender Sender info
type obSender struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Card     string `json:"card"` // Group nickname
}

// obMessageSegment OneBot 11 message segment
type obMessageSegment struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// obTextData Text message segment data
type obTextData struct {
	Text string `json:"text"`
}

// obImageData Image message segment data
type obImageData struct {
	File string `json:"file"`
	URL  string `json:"url"`
}

// obAtData @mention message segment data
type obAtData struct {
	QQ any `json:"qq"`
}

// formatQQ Format obAtData.QQ(any) as string
// NapCat may send QQ number as string or float64 type
func formatQQ(qq any) string {
	switch v := qq.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// obMediaData Generic media message segment data (record/video/file)
type obMediaData struct {
	File string `json:"file"`
	URL  string `json:"url"`
}

// obAPIRequest OneBot 11 API request
type obAPIRequest struct {
	Action string `json:"action"`
	Params any    `json:"params"`
	Echo   string `json:"echo"`
}

// obAPIResponse OneBot 11 API response
type obAPIResponse struct {
	Status  string          `json:"status"`
	RetCode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Echo    string          `json:"echo"`
}

// obSendMsgResponse send_msg response data
type obSendMsgResponse struct {
	MessageID int64 `json:"message_id"`
}

// ---------------------------------------------------------------------------
// Event dispatcher
// ---------------------------------------------------------------------------

// handleEvent Handle events received from WebSocket
func (n *NapCatChannel) handleEvent(data []byte) error {
	var event obEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("parse event: %w", err)
	}

	// Check if it's an API response (has echo field)
	if event.Echo != "" {
		n.handleAPIResponse(event.Echo, data)
		return nil
	}

	// Record self_id
	if event.SelfID != 0 {
		n.selfID.Store(event.SelfID)
	}

	switch event.PostType {
	case "message":
		return n.handleMessage(&event)
	case "meta_event":
		return n.handleMetaEvent(&event)
	case "notice":
		log.WithField("sub_type", event.SubType).Debug("NapCat: notice event (ignored)")
	case "request":
		log.WithField("sub_type", event.SubType).Debug("NapCat: request event (ignored)")
	default:
		// May be a pure API response (status field exists but no post_type)
		if len(event.Status) > 0 {
			// API response without echo, ignore
			return nil
		}
		log.WithField("post_type", event.PostType).Debug("NapCat: unknown event type")
	}

	return nil
}

// handleAPIResponse Handle API response, match pending request
func (n *NapCatChannel) handleAPIResponse(echo string, data []byte) {
	n.pendingMu.Lock()
	ch, ok := n.pending[echo]
	if ok {
		delete(n.pending, echo)
	}
	n.pendingMu.Unlock()

	if ok {
		select {
		case ch <- json.RawMessage(data):
		default:
			// Channel may be full or closed, discard response
		}
	}
}

// handleMetaEvent Handle meta events
func (n *NapCatChannel) handleMetaEvent(event *obEvent) error {
	switch event.MetaEventType {
	case "heartbeat":
		log.Debug("NapCat: heartbeat received")
	case "lifecycle":
		log.WithField("sub_type", event.SubType).Info("NapCat: lifecycle event")
	default:
		log.WithField("meta_event_type", event.MetaEventType).Debug("NapCat: unknown meta event")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Message handler
// ---------------------------------------------------------------------------

// handleMessage: handle message event
func (n *NapCatChannel) handleMessage(event *obEvent) error {
	messageID := fmt.Sprintf("%d", event.MessageID)

	log.WithFields(log.Fields{
		"message_id":   messageID,
		"message_type": event.MessageType,
		"user_id":      event.UserID,
		"group_id":     event.GroupID,
		"raw_message":  truncate(event.RawMessage, 100),
	}).Info("NapCat: message received")

	// Deduplication
	if n.isDuplicate(messageID) {
		log.WithField("message_id", messageID).Debug("NapCat: duplicate message, skipping")
		return nil
	}

	// Whitelist check
	senderID := fmt.Sprintf("%d", event.UserID)
	if !n.isAllowed(n.config.AllowFrom, senderID) {
		log.WithField("sender", senderID).Info("NapCat: access denied")
		return nil
	}

	// Parse message segments
	content, media, mentionedBot := n.parseMessageSegments(event.Message, event.SelfID)

	// Group messages must @bot to be processed, private messages processed directly
	if event.MessageType == "group" && !mentionedBot {
		log.WithField("group_id", event.GroupID).Debug("NapCat: group message without @bot, skipping")
		return nil
	}

	// If the message is empty (possibly all emoji or @bot), skip it
	if content == "" && len(media) == 0 {
		return nil
	}

	// Build inbound message
	senderName := event.Sender.Nickname
	if event.Sender.Card != "" {
		senderName = event.Sender.Card // Group nickname优先
	}

	var chatID string
	var chatType string
	var xbotChatType string

	switch event.MessageType {
	case "private":
		chatID = senderID
		chatType = "private"
		xbotChatType = "p2p"
	case "group":
		chatID = fmt.Sprintf("%d", event.GroupID)
		chatType = "group"
		xbotChatType = "group"
	default:
		chatID = senderID
		chatType = event.MessageType
		xbotChatType = "p2p"
	}

	requestID := log.NewRequestID()

	inbound := bus.InboundMessage{
		From:       bus.NewIMAddress("napcat", senderID),
		To:         bus.NewIMAddress("napcat", chatID),
		Channel:    "napcat",
		SenderID:   senderID,
		SenderName: senderName,
		ChatID:     chatID,
		ChatType:   xbotChatType,
		Content:    content,
		Media:      media,
		Time: func() time.Time {
			if event.Time == 0 {
				return time.Now()
			}
			return time.Unix(event.Time, 0)
		}(),
		RequestID: requestID,
		Metadata: map[string]string{
			"message_id":   messageID,
			"chat_type":    chatType,
			"self_id":      fmt.Sprintf("%d", event.SelfID),
			"reply_policy": "optional", // QQ doesn't support patch, disable ACK and progress notifications
		},
	}

	// Cache chat type for chatID, for use by Send method
	n.chatTypeCache.Store(chatID, chatType)

	n.msgBus.Inbound <- inbound
	return nil
}

// ---------------------------------------------------------------------------
// Message segment parsing
// ---------------------------------------------------------------------------

// parseMessageSegments: parse OneBot 11 message segment array, returns text content, media URL list, and whether @bot
// selfID For filtering @bot message segments in group messages
func (n *NapCatChannel) parseMessageSegments(raw json.RawMessage, selfID int64) (string, []string, bool) {
	if len(raw) == 0 {
		return "", nil, false
	}

	var segments []obMessageSegment
	if err := json.Unmarshal(raw, &segments); err != nil {
		// May be string-format message (messagePostFormat=string), return directly
		var s string
		if err2 := json.Unmarshal(raw, &s); err2 == nil {
			return s, nil, false
		}
		log.WithError(err).Debug("NapCat: failed to parse message segments")
		return "", nil, false
	}

	var textParts []string
	var media []string
	selfIDStr := fmt.Sprintf("%d", selfID)
	mentionedBot := false

	for _, seg := range segments {
		switch seg.Type {
		case "text":
			var data obTextData
			if err := json.Unmarshal(seg.Data, &data); err == nil && data.Text != "" {
				textParts = append(textParts, data.Text)
			}

		case "image":
			var data obImageData
			if err := json.Unmarshal(seg.Data, &data); err == nil {
				url := data.URL
				if url == "" {
					url = data.File
				}
				if url != "" {
					media = append(media, url)
				}
			}

		case "at":
			var data obAtData
			if err := json.Unmarshal(seg.Data, &data); err == nil {
				// Detect @bot self or @all
				qqStr := formatQQ(data.QQ)
				if qqStr == selfIDStr || qqStr == "all" {
					mentionedBot = true
					continue
				}
				textParts = append(textParts, fmt.Sprintf("@%s", qqStr))
			}

		case "reply":
			// Reply message segment, not added to text but can be logged
			// metadata already has message_id, reply's id can be ignored

		case "face":
			// QQ emoji, ignored

		case "record":
			var data obMediaData
			if err := json.Unmarshal(seg.Data, &data); err == nil {
				url := data.URL
				if url == "" {
					url = data.File
				}
				if url != "" {
					media = append(media, url)
				}
			}

		case "video":
			var data obMediaData
			if err := json.Unmarshal(seg.Data, &data); err == nil {
				url := data.URL
				if url == "" {
					url = data.File
				}
				if url != "" {
					media = append(media, url)
				}
			}

		case "file":
			var data obMediaData
			if err := json.Unmarshal(seg.Data, &data); err == nil {
				url := data.URL
				if url == "" {
					url = data.File
				}
				if url != "" {
					media = append(media, url)
				}
			}

		default:
			log.WithField("type", seg.Type).Debug("NapCat: unknown message segment type")
		}
	}

	text := strings.TrimSpace(strings.Join(textParts, ""))
	return text, media, mentionedBot
}

// ---------------------------------------------------------------------------
// Send (outbound)
// ---------------------------------------------------------------------------

// Send Send message to NapCat
func (n *NapCatChannel) Send(msg bus.OutboundMessage) (string, error) {
	if msg.Content == "" && len(msg.Media) == 0 {
		return "", nil
	}

	// QQ doesn't support patch (in-place message update), send new message directly.
	// reply_policy=optional has disabled ACK and progress notifications, only final replies received here.

	chatType := ""
	if msg.Metadata != nil {
		chatType = msg.Metadata["chat_type"]
	}
	// Infer chat type from cache
	if chatType == "" {
		if cached, ok := n.chatTypeCache.Load(msg.ChatID); ok {
			chatType = cached.(string)
		}
	}

	// Build message content (message segment array)
	message := n.buildOutboundMessage(msg.Content, msg.Media)

	// Select API based on chat_type
	switch chatType {
	case "group":
		groupID, err := strconv.ParseInt(msg.ChatID, 10, 64)
		if err != nil {
			return "", fmt.Errorf("napcat: invalid group_id %q: %w", msg.ChatID, err)
		}
		return n.sendGroupMsg(groupID, message)

	case "private":
		userID, err := strconv.ParseInt(msg.ChatID, 10, 64)
		if err != nil {
			return "", fmt.Errorf("napcat: invalid user_id %q: %w", msg.ChatID, err)
		}
		return n.sendPrivateMsg(userID, message)

	default:
		// Unable to determine chat type, default to private chat attempt
		log.WithField("chat_id", msg.ChatID).Warn("NapCat: unknown chat type, defaulting to private")
		id, err := strconv.ParseInt(msg.ChatID, 10, 64)
		if err != nil {
			return "", fmt.Errorf("napcat: invalid chat_id %q: %w", msg.ChatID, err)
		}
		return n.sendPrivateMsg(id, message)
	}
}

// mediaTypeFromURL Infer OneBot media message segment type from URL/path extension
func mediaTypeFromURL(url string) string {
	switch {
	case strings.HasSuffix(strings.ToLower(url), ".mp3"),
		strings.HasSuffix(strings.ToLower(url), ".wav"),
		strings.HasSuffix(strings.ToLower(url), ".silk"),
		strings.HasSuffix(strings.ToLower(url), ".amr"):
		return "record"
	case strings.HasSuffix(strings.ToLower(url), ".mp4"),
		strings.HasSuffix(strings.ToLower(url), ".avi"):
		return "video"
	default:
		return "image"
	}
}

// buildOutboundMessage Build outbound message content
// If text only, return plain string; if media present, return message segment array
func (n *NapCatChannel) buildOutboundMessage(content string, media []string) any {
	if len(media) == 0 {
		return content
	}

	// Build message segment array
	var segments []map[string]any

	// Add text segment
	if content != "" {
		segments = append(segments, map[string]any{
			"type": "text",
			"data": map[string]string{
				"text": content,
			},
		})
	}

	// Add media segment
	for _, url := range media {
		segments = append(segments, map[string]any{
			"type": mediaTypeFromURL(url),
			"data": map[string]string{
				"file": url,
			},
		})
	}

	return segments
}

// sendPrivateMsg Send private chat message
func (n *NapCatChannel) sendPrivateMsg(userID int64, message any) (string, error) {
	resp, err := n.callAPI("send_private_msg", map[string]any{
		"user_id": userID,
		"message": message,
	})
	if err != nil {
		return "", fmt.Errorf("napcat: send_private_msg failed: %w", err)
	}

	var result obSendMsgResponse
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return "", nil // Send succeeded but response parsing failed, no impact
	}
	return fmt.Sprintf("%d", result.MessageID), nil
}

// sendGroupMsg Send group message
func (n *NapCatChannel) sendGroupMsg(groupID int64, message any) (string, error) {
	resp, err := n.callAPI("send_group_msg", map[string]any{
		"group_id": groupID,
		"message":  message,
	})
	if err != nil {
		return "", fmt.Errorf("napcat: send_group_msg failed: %w", err)
	}

	var result obSendMsgResponse
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return "", nil
	}
	return fmt.Sprintf("%d", result.MessageID), nil
}

// ---------------------------------------------------------------------------
// API call with echo matching
// ---------------------------------------------------------------------------

// callAPI Call OneBot 11 API, match response via echo
func (n *NapCatChannel) callAPI(action string, params any) (*obAPIResponse, error) {
	echo := uuid.New().String()

	// Register pending response channel
	ch := make(chan json.RawMessage, 1)
	n.pendingMu.Lock()
	n.pending[echo] = ch
	n.pendingMu.Unlock()

	// Send request
	req := obAPIRequest{
		Action: action,
		Params: params,
		Echo:   echo,
	}

	if err := n.wsSend(req); err != nil {
		n.pendingMu.Lock()
		delete(n.pending, echo)
		n.pendingMu.Unlock()
		return nil, fmt.Errorf("ws send: %w", err)
	}

	// Wait for response (timeout 30s)
	select {
	case data := <-ch:
		if data == nil {
			return nil, fmt.Errorf("napcat: connection closed while waiting for %s response", action)
		}
		var resp obAPIResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, fmt.Errorf("parse api response: %w", err)
		}
		if resp.RetCode != 0 {
			return &resp, fmt.Errorf("api error: status=%s retcode=%d", resp.Status, resp.RetCode)
		}
		return &resp, nil
	case <-time.After(napcatConnectTimeout):
		n.pendingMu.Lock()
		if ch, ok := n.pending[echo]; ok {
			close(ch)
			delete(n.pending, echo)
		}
		n.pendingMu.Unlock()
		return nil, fmt.Errorf("api call %s timed out", action)

	case <-n.stopCh:
		n.pendingMu.Lock()
		if ch, ok := n.pending[echo]; ok {
			close(ch)
			delete(n.pending, echo)
		}
		n.pendingMu.Unlock()
		return nil, fmt.Errorf("channel stopped")
	}
}

// ---------------------------------------------------------------------------
// WebSocket helpers
// ---------------------------------------------------------------------------

// clearPending Clean up all pending requests
func (n *NapCatChannel) clearPending() {
	n.pendingMu.Lock()
	defer n.pendingMu.Unlock()

	for echo, ch := range n.pending {
		close(ch)
		delete(n.pending, echo)
	}
}

// ---------------------------------------------------------------------------
// Deduplication
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Access control
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Quick disconnect detection
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// truncate Truncate string for logging
func truncate(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "..."
}

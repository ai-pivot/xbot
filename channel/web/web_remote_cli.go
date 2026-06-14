package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	log "xbot/logger"
	ch "xbot/channel"
	"xbot/protocol"
)

func (c *RemoteCLIChannel) SendTUIControlRequest(chatID string, action string, params map[string]string) (map[string]string, error) {
	id := ch.CliMsg.GenerateTUIID()
	respCh := make(chan *protocol.TUIControlPayload, 1)

	c.tuiPendingMu.Lock()
	c.tuiPending[id] = respCh
	c.tuiPendingMu.Unlock()

	defer func() {
		c.tuiPendingMu.Lock()
		delete(c.tuiPending, id)
		c.tuiPendingMu.Unlock()
	}()

	wsMsg := ch.CliMsg.BuildTUIControlReqMsg(id, chatID, action, params)
	if !c.hub.sendToClient(chatID, wsMsg) {
		return nil, fmt.Errorf("remote CLI client offline for chat %s", chatID)
	}

	select {
	case resp := <-respCh:
		if resp.Error != "" {
			return nil, fmt.Errorf("%s", resp.Error)
		}
		return resp.Result, nil
	case <-time.After(ch.TuiRespTimeout):
		return nil, fmt.Errorf("tui_control request %s timed out", id)
	}
}

// deliverTUIResponse routes a TUI control response from a remote CLI client
// to the pending request channel.
func (c *RemoteCLIChannel) deliverTUIResponse(id string, payload *protocol.TUIControlPayload) {
	c.tuiPendingMu.Lock()
	respCh, ok := c.tuiPending[id]
	c.tuiPendingMu.Unlock()
	if ok {
		select {
		case respCh <- payload:
		default:
		}
	}
}

// remoteCLIChannel — virtual CLI channel for remote mode (CLI→WS→server)
// ---------------------------------------------------------------------------

// remoteCLIChannel is a virtual Channel implementation registered as "cli"
// in the server's dispatcher. It routes outbound messages to the correct
// WebSocket client via the web channel's hub.
type RemoteCLIChannel struct {
	hub *Hub

	// Per-chatID widget zone cache for incremental updates.
	lastWidgetMu    sync.Mutex
	lastWidgetZones map[string]map[string]string // chatID → zone → content

	// TUI control pending requests (keyed by request ID)
	tuiPendingMu sync.Mutex
	tuiPending   map[string]chan *protocol.TUIControlPayload
}

// NewRemoteCLIChannel creates a virtual CLI channel that shares the given hub.
func NewRemoteCLIChannel(hub *Hub) *RemoteCLIChannel {
	rc := &RemoteCLIChannel{
		hub:        hub,
		tuiPending: make(map[string]chan *protocol.TUIControlPayload),
	}
	hub.tuiRespFn = rc.deliverTUIResponse
	return rc
}

func (c *RemoteCLIChannel) Name() string { return "cli" }

func (c *RemoteCLIChannel) Start() error { return nil }

func (c *RemoteCLIChannel) Stop() {}

// stripChannelPrefix extracts the plain chatID from a "channel:chatID" qualified key.
// If no colon is present, returns the input unchanged.
// Used by RemoteCLIChannel to map qualified keys to Hub subscriber keys.
func stripChannelPrefix(qualifiedID string) string {
	if idx := strings.Index(qualifiedID, ":"); idx >= 0 {
		return qualifiedID[idx+1:]
	}
	return qualifiedID
}

// InjectUserMessage sends an injected user message (e.g. bg task notification)
// to the remote CLI runner via WebSocket.
// chatID may be in "channel:chatID" format (from injectCLIUserMessage) — we strip
// the channel prefix for the Hub subscriber lookup while preserving the full key
// in the WSMessage so the client-side TUI session filter matches correctly.
func (c *RemoteCLIChannel) InjectUserMessage(chatID, content string) {
	hubKey := stripChannelPrefix(chatID)
	wsMsg := ch.CliMsg.BuildInjectUserMsg(chatID, content)
	if !c.hub.sendToClient(hubKey, wsMsg) {
		log.WithField("chat_id", chatID).Debug("Remote CLI client offline, inject_user buffered")
	}
}

// SendProgress sends structured progress to remote CLI clients via the Hub.
func (c *RemoteCLIChannel) SendProgress(chatID string, payload *protocol.ProgressEvent) {
	if msg := ch.CliMsg.BuildProgressMsg(chatID, payload); msg != nil {
		if !c.hub.sendToClient(chatID, *msg) {
			log.WithFields(log.Fields{
				"chat_id": chatID,
				"phase":   payload.Phase,
				"iter":    payload.Iteration,
			}).Debug("Hub SendProgress: no online subscriber, event buffered")
		}
	}
}

// SendSessionState sends a session state change event to remote CLI clients via the Hub.
func (c *RemoteCLIChannel) SendSessionState(ev protocol.SessionEvent) {
	c.hub.broadcastToCLI(ch.CliMsg.BuildSessionStateMsg(ev))
}

// SendStreamContent sends streaming LLM content to remote CLI clients via the Hub.
func (c *RemoteCLIChannel) SendStreamContent(chatID, content, reasoning string) {
	if msg := ch.CliMsg.BuildStreamContentMsg(chatID, content, reasoning); msg != nil {
		_ = c.hub.sendToClient(chatID, *msg) // stream events are ephemeral, safe to drop
	}
}

// PushPluginWidgetsPerSession pushes widget zone content to each connected CLI
// client with per-session rendering. The renderFn callback is called once per
// subscribed chatID to produce session-specific widget content (using the
// session's workDir for correct git branch, etc.).
//
// Performs incremental updates: only sends to chatIDs whose zones actually changed.
func (c *RemoteCLIChannel) PushPluginWidgetsPerSession(renderFn func(chatID string) map[string]string) {
	// Collect subscribed chatIDs
	c.hub.mu.RLock()
	chatIDs := make([]string, 0, len(c.hub.subs))
	for chatID := range c.hub.subs {
		chatIDs = append(chatIDs, chatID)
	}
	c.hub.mu.RUnlock()

	for _, chatID := range chatIDs {
		// Skip non-session chatIDs (e.g. "admin" from web-layer p2p routing).
		// CLI sessions use absolute paths as chatID; web-layer uses userID.
		// Pushing to web chatIDs sends stale content to wrong windows.
		if !strings.HasPrefix(chatID, "/") {
			continue
		}

		zones := renderFn(chatID)

		// Incremental: skip if nothing changed for this chatID
		c.lastWidgetMu.Lock()
		changed := true
		if c.lastWidgetZones != nil {
			if prev, ok := c.lastWidgetZones[chatID]; ok {
				if len(prev) == len(zones) {
					changed = false
					for k, v := range zones {
						if ov, exists := prev[k]; !exists || ov != v {
							changed = true
							break
						}
					}
				}
			}
		}
		if !changed {
			c.lastWidgetMu.Unlock()
			continue
		}
		if c.lastWidgetZones == nil {
			c.lastWidgetZones = make(map[string]map[string]string)
		}
		c.lastWidgetZones[chatID] = zones
		c.lastWidgetMu.Unlock()

		b, _ := json.Marshal(zones)
		wsMsg := protocol.WSMessage{
			Type:    protocol.MsgTypePluginWidgets,
			TS:      time.Now().Unix(),
			ChatID:  chatID, // client uses this to filter cross-session pushes
			Content: string(b),
		}
		_ = c.hub.sendToClient(chatID, wsMsg) // best-effort push
	}
}

func (c *RemoteCLIChannel) Send(msg ch.OutboundMsg) (string, error) {
	msgID := strings.ReplaceAll(uuid.New().String(), "-", "")

	content := msg.Content
	msgType := "text"

	if strings.HasPrefix(content, "__FEISHU_CARD__") {
		msgType = "card"
		content = ch.ConvertFeishuCard(content)
	}

	targetClientID := msg.ChatID

	// Build base text message, then overlay remote-specific fields
	wsMsg := ch.CliMsg.BuildTextMsg(msg)
	wsMsg.ID = msgID
	wsMsg.Type = msgType
	wsMsg.Content = content
	wsMsg.ProgressHistory = msg.Metadata["progress_history"]

	if !c.hub.sendToClient(targetClientID, wsMsg) {
		log.WithFields(log.Fields{"chat_id": msg.ChatID, "target_client_id": targetClientID}).Debug("CLI WS client offline, message buffered")
	}

	// AskUser: reuse shared builder
	if askMsg := ch.CliMsg.BuildAskUserMsg(msg); askMsg != nil {
		askMsg.ID = msgID
		log.WithFields(log.Fields{
			"msg_channel":   msg.Channel,
			"msg_chatid":    msg.ChatID,
			"target_client": targetClientID,
		}).Info("RemoteCLIChannel.Send: dispatching ask_user")
		c.hub.sendToClient(targetClientID, *askMsg)
	}

	return msgID, nil
}

package channel

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"xbot/protocol"

	"github.com/google/uuid"
	log "xbot/logger"
)

func (c *RemoteCLIChannel) SendTUIControlRequest(chatID string, action string, params map[string]string) (map[string]string, error) {
	id := cliMsg.generateTUIID()
	ch := make(chan *protocol.TUIControlPayload, 1)

	c.tuiPendingMu.Lock()
	c.tuiPending[id] = ch
	c.tuiPendingMu.Unlock()

	defer func() {
		c.tuiPendingMu.Lock()
		delete(c.tuiPending, id)
		c.tuiPendingMu.Unlock()
	}()

	wsMsg := cliMsg.buildTUIControlReqMsg(id, chatID, action, params)
	if !c.hub.sendToClient(chatID, wsMsg) {
		return nil, fmt.Errorf("remote CLI client offline for chat %s", chatID)
	}

	select {
	case resp := <-ch:
		if resp.Error != "" {
			return nil, fmt.Errorf("%s", resp.Error)
		}
		return resp.Result, nil
	case <-time.After(tuiRespTimeout):
		return nil, fmt.Errorf("tui_control request %s timed out", id)
	}
}

// deliverTUIResponse routes a TUI control response from a remote CLI client
// to the pending request channel.
func (c *RemoteCLIChannel) deliverTUIResponse(id string, payload *protocol.TUIControlPayload) {
	c.tuiPendingMu.Lock()
	ch, ok := c.tuiPending[id]
	c.tuiPendingMu.Unlock()
	if ok {
		select {
		case ch <- payload:
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

// InjectUserMessage sends an injected user message (e.g. bg task notification)
// to the remote CLI runner via WebSocket.
func (c *RemoteCLIChannel) InjectUserMessage(chatID, content string) {
	wsMsg := cliMsg.buildInjectUserMsg(chatID, content)
	if !c.hub.sendToClient(chatID, wsMsg) {
		log.WithField("chat_id", chatID).Debug("Remote CLI client offline, inject_user buffered")
	}
}

// SendProgress sends structured progress to remote CLI clients via the Hub.
func (c *RemoteCLIChannel) SendProgress(chatID string, payload *protocol.ProgressEvent) {
	if msg := cliMsg.buildProgressMsg(chatID, payload); msg != nil {
		if !c.hub.sendToClient(chatID, *msg) {
			log.WithFields(log.Fields{
				"chat_id": chatID,
				"phase":   payload.Phase,
				"iter":    payload.Iteration,
			}).Info("Hub SendProgress: no online subscriber, event buffered")
		}
	}
}

// SendSessionState sends a session state change event to remote CLI clients via the Hub.
func (c *RemoteCLIChannel) SendSessionState(ev protocol.SessionEvent) {
	c.hub.broadcastToAll(cliMsg.buildSessionStateMsg(ev))
}

// SendStreamContent sends streaming LLM content to remote CLI clients via the Hub.
func (c *RemoteCLIChannel) SendStreamContent(chatID, content, reasoning string) {
	if msg := cliMsg.buildStreamContentMsg(chatID, content, reasoning); msg != nil {
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

func (c *RemoteCLIChannel) Send(msg OutboundMsg) (string, error) {
	msgID := strings.ReplaceAll(uuid.New().String(), "-", "")

	content := msg.Content
	msgType := "text"

	if strings.HasPrefix(content, "__FEISHU_CARD__") {
		msgType = "card"
		content = ConvertFeishuCard(content)
	}

	targetClientID := msg.ChatID

	// Build base text message, then overlay remote-specific fields
	wsMsg := cliMsg.buildTextMsg(msg)
	wsMsg.ID = msgID
	wsMsg.Type = msgType
	wsMsg.Content = content
	wsMsg.ProgressHistory = msg.Metadata["progress_history"]

	if !c.hub.sendToClient(targetClientID, wsMsg) {
		log.WithFields(log.Fields{"chat_id": msg.ChatID, "target_client_id": targetClientID}).Debug("CLI WS client offline, message buffered")
	}

	// AskUser: reuse shared builder
	if askMsg := cliMsg.buildAskUserMsg(msg); askMsg != nil {
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

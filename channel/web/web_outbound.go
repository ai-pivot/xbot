package web

import (
	"encoding/json"
	"strings"
	"time"

	ch "xbot/channel"
	log "xbot/logger"
	"xbot/protocol"

	"github.com/google/uuid"
)

// SendSessionState implements ch.SessionStateSender.
// WebSocket clients retain the legacy broadcast behavior; SSE clients receive
// only events for their authorized chat subscription.
func (wc *WebChannel) SendSessionState(ev protocol.SessionEvent) {
	wc.hub.broadcastSessionState(ev.ChatID, protocol.WSMessage{
		Type:    protocol.MsgTypeSession,
		TS:      time.Now().Unix(),
		Session: &ev,
	})
}

// ---------------------------------------------------------------------------
// Send: non-blocking write to connected Web transports
// ---------------------------------------------------------------------------

// Send 发送消息到 Web 客户端（非阻塞）

func (wc *WebChannel) Send(msg ch.OutboundMsg) (string, error) {
	msgID := strings.ReplaceAll(uuid.New().String(), "-", "")

	content := msg.Content
	msgType := "text"

	// __FEISHU_CARD__ protocol adaptation
	if strings.HasPrefix(content, "__FEISHU_CARD__") {
		msgType = "card"
		content = ch.ConvertFeishuCard(content)
	}

	wsMsg := protocol.WSMessage{
		Type:            msgType,
		ID:              msgID,
		Content:         content,
		TS:              time.Now().Unix(),
		ProgressHistory: msg.Metadata["progress_history"],
		Channel:         msg.Channel,
		ChatID:          msg.ChatID,
		SessionReset:    msg.Metadata != nil && msg.Metadata["session_reset"] == "true",
		// Only forward frontend-relevant metadata keys — avoid leaking internal
		// keys like feishu_user_id, request_id, cancelled, etc.
	}

	targetClientID := msg.ChatID

	// /new resets the conversation boundary. Drop buffered pre-reset events before
	// buffering the reset message itself; otherwise reconnect replay can resurrect
	// a stale in-flight progress event from the previous session.
	if wsMsg.SessionReset {
		wc.getEventStream(targetClientID).clear()
	}

	// The Hub stamps and buffers Web transport events before fan-out while
	// preserving the remote CLI WebSocket envelope unchanged.
	if !wc.hub.sendToClient(targetClientID, wsMsg) {
		log.WithFields(log.Fields{"chat_id": msg.ChatID, "target_client_id": targetClientID}).Debug("Web client offline, message buffered")
	}

	// AskUser: agent needs user input
	if msg.WaitingUser {
		askPayload := &protocol.ProgressEvent{}
		if msg.Metadata != nil {
			askPayload.RequestID = msg.Metadata["request_id"]
			if qJSON := msg.Metadata["ask_questions"]; qJSON != "" {
				var qs []protocol.AskUserQuestion
				if json.Unmarshal([]byte(qJSON), &qs) == nil {
					askPayload.Questions = qs
				}
			}
		}
		askMsg := protocol.WSMessage{
			Type:     protocol.MsgTypeAskUser,
			ID:       msgID,
			TS:       time.Now().Unix(),
			Channel:  msg.Channel,
			ChatID:   msg.ChatID,
			Progress: askPayload,
		}
		if wc.callbacks.WithPendingAskUser != nil {
			wc.callbacks.WithPendingAskUser(msg.Channel, msg.ChatID, func(pending *protocol.ProgressEvent) bool {
				if pending.RequestID != askPayload.RequestID {
					return false
				}
				askMsg.Progress = pending
				wc.hub.sendToClient(targetClientID, askMsg)
				return true
			})
		}
	}

	return msgID, nil
}

// stampAndBuffer assigns a monotonic seq to the message and appends it to the
// per-chatID event stream buffer. Returns the stamped message (ready to send).
func (wc *WebChannel) stampAndBuffer(chatID string, msg protocol.WSMessage) protocol.WSMessage {
	es := wc.getEventStream(chatID)
	msg.Seq = es.nextSeq()
	es.push(msg)
	return msg
}

// SendProgress 发送结构化进度事件到 Web 客户端（非阻塞）。
// 内部通过 hub 的缓冲通道发送，保持调用路径轻量。
func (wc *WebChannel) SendProgress(chatID string, payload *protocol.ProgressEvent) {
	if payload == nil {
		return
	}

	wsMsg := protocol.WSMessage{
		Type:     protocol.MsgTypeProgress,
		TS:       time.Now().Unix(),
		Progress: payload,
	}

	if !wc.hub.sendToClient(chatID, wsMsg) {
		log.WithField("chat_id", chatID).Debug("Web client offline, progress event buffered")
	}
}

// SendStreamContent sends streaming LLM content to a specific client.
// Used by CLI RemoteBackend connections to push token-by-token streaming.
func (wc *WebChannel) SendStreamContent(chatID, content, reasoning string) {
	if content == "" && reasoning == "" {
		return
	}
	wsMsg := protocol.WSMessage{
		Type: protocol.MsgTypeStreamContent,
		TS:   time.Now().Unix(),
		Progress: &protocol.ProgressEvent{
			ChatID:                 "web:" + chatID,
			StreamContent:          content,
			ReasoningStreamContent: reasoning,
		},
	}
	_ = wc.hub.sendToClient(chatID, wsMsg) // stream events are ephemeral, safe to drop
}

// PushRunnerStatus pushes a runner online/offline status change to the Web client.
func (wc *WebChannel) PushRunnerStatus(chatID, runnerName string, online bool) {
	wsMsg := protocol.WSMessage{
		Type: protocol.MsgTypeRunnerStatus,
		TS:   time.Now().Unix(),
		Content: func() string {
			b, _ := json.Marshal(map[string]any{"runner_name": runnerName, "online": online})
			return string(b)
		}(),
	}
	if !wc.hub.sendToClient(chatID, wsMsg) {
		log.WithField("chat_id", chatID).Debug("Web client offline, runner status buffered")
	}
}

// PushSyncProgress pushes a sync progress notification to the Web client.
func (wc *WebChannel) PushSyncProgress(chatID, phase, message string) {
	wsMsg := protocol.WSMessage{
		Type: "sync_progress",
		TS:   time.Now().Unix(),
		Content: func() string {
			b, _ := json.Marshal(map[string]any{"phase": phase, "message": message})
			return string(b)
		}(),
	}
	if !wc.hub.sendToClient(chatID, wsMsg) {
		log.WithField("chat_id", chatID).Debug("Web client offline, sync progress buffered")
	}
}

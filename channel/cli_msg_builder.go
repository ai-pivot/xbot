package channel

import (
	"encoding/json"
	"fmt"
	"time"

	"xbot/protocol"
)

// cliMessageBuilder constructs WSMessage payloads for CLI channel operations.
// Both ChannelCliChannel (local/in-process) and RemoteCLIChannel (WebSocket)
// use these builders so the message format stays identical — only the
// transport layer differs.
type cliMessageBuilder struct{}

var CliMsg = cliMessageBuilder{}

// buildTextMsg creates a text outbound message.
func (cliMessageBuilder) BuildTextMsg(msg OutboundMsg) protocol.WSMessage {
	return protocol.WSMessage{
		Type:     protocol.MsgTypeText,
		TS:       time.Now().Unix(),
		Content:  msg.Content,
		ChatID:   msg.ChatID,
		Channel:  msg.Channel,
		Metadata: msg.Metadata,
	}
}

// buildAskUserMsg creates an ask_user message from an outbound message with
// WaitingUser=true. Returns nil if WaitingUser is false.
func (cliMessageBuilder) BuildAskUserMsg(msg OutboundMsg) *protocol.WSMessage {
	if !msg.WaitingUser {
		return nil
	}
	askEv := protocol.AskUserEvent{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
	}
	progress := &protocol.ProgressEvent{}
	if msg.Metadata != nil {
		askEv.RequestID = msg.Metadata["request_id"]
		askEv.Questions = msg.Metadata["ask_questions"]
		progress.RequestID = askEv.RequestID
		if askEv.Questions != "" {
			_ = json.Unmarshal([]byte(askEv.Questions), &progress.Questions)
		}
	}
	data, _ := json.Marshal(askEv)
	return &protocol.WSMessage{
		Type:     protocol.MsgTypeAskUser,
		TS:       time.Now().Unix(),
		Channel:  msg.Channel,
		ChatID:   msg.ChatID,
		Content:  string(data),
		Progress: progress,
	}
}

// buildProgressMsg creates a progress event message.
// Returns nil if payload is nil.
func (cliMessageBuilder) BuildProgressMsg(chatID string, payload *protocol.ProgressEvent) *protocol.WSMessage {
	if payload == nil {
		return nil
	}
	return &protocol.WSMessage{
		Type:     protocol.MsgTypeProgress,
		TS:       time.Now().Unix(),
		Progress: payload,
		ChatID:   chatID,
	}
}

// buildStreamContentMsg creates a stream content message.
// The Progress.ChatID carries the "cli:" prefix expected by
// handleProgressMsg's session filter. Returns nil if both content and
// reasoning are empty.
func (cliMessageBuilder) BuildStreamContentMsg(chatID, content, reasoning string) *protocol.WSMessage {
	if content == "" && reasoning == "" {
		return nil
	}
	return &protocol.WSMessage{
		Type: protocol.MsgTypeStreamContent,
		TS:   time.Now().Unix(),
		Progress: &protocol.ProgressEvent{
			ChatID:                 "cli:" + chatID,
			StreamContent:          content,
			ReasoningStreamContent: reasoning,
		},
	}
}

// buildSessionStateMsg creates a session state change message.
func (cliMessageBuilder) BuildSessionStateMsg(ev protocol.SessionEvent) protocol.WSMessage {
	return protocol.WSMessage{
		Type:    protocol.MsgTypeSession,
		TS:      time.Now().Unix(),
		Session: &ev,
	}
}

// buildInjectUserMsg creates an inject_user message.
func (cliMessageBuilder) BuildInjectUserMsg(chatID, content string) protocol.WSMessage {
	return protocol.WSMessage{
		Type:    protocol.MsgTypeInjectUser,
		TS:      time.Now().Unix(),
		ChatID:  chatID,
		Content: content,
	}
}

// buildTUIControlReqMsg creates a TUI control request message.
func (cliMessageBuilder) BuildTUIControlReqMsg(id, chatID string, action string, params map[string]string) protocol.WSMessage {
	return protocol.WSMessage{
		Type:   protocol.MsgTypeTUIControlReq,
		ID:     id,
		ChatID: chatID,
		TUIControl: &protocol.TUIControlPayload{
			Action: action,
			Params: params,
		},
	}
}

// generateTUIID creates a unique ID for TUI control requests.
func (cliMessageBuilder) GenerateTUIID() string {
	return fmt.Sprintf("tui-%d", time.Now().UnixNano())
}

// tuiRespTimeout is the timeout for waiting on a TUI control response.
const TuiRespTimeout = 10 * time.Second

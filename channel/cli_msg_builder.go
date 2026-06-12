package channel

import (
	"encoding/json"
	"fmt"
	"time"

	"xbot/protocol"
)

// CliMessageBuilder constructs WSMessage payloads for CLI channel operations.
// Both ChannelCliChannel (local/in-process) and RemoteCLIChannel (WebSocket)
// use these builders so the message format stays identical — only the
// transport layer differs.
type CliMessageBuilder struct{}

var CliMsg = CliMessageBuilder{}

// BuildTextMsg creates a text outbound message.
func (CliMessageBuilder) BuildTextMsg(msg OutboundMsg) protocol.WSMessage {
	return protocol.WSMessage{
		Type:     protocol.MsgTypeText,
		TS:       time.Now().Unix(),
		Content:  msg.Content,
		ChatID:   msg.ChatID,
		Channel:  msg.Channel,
		Metadata: msg.Metadata,
	}
}

// BuildAskUserMsg creates an ask_user message from an outbound message with
// WaitingUser=true. Returns nil if WaitingUser is false.
func (CliMessageBuilder) BuildAskUserMsg(msg OutboundMsg) *protocol.WSMessage {
	if !msg.WaitingUser {
		return nil
	}
	askEv := protocol.AskUserEvent{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
	}
	if msg.Metadata != nil {
		askEv.RequestID = msg.Metadata["request_id"]
		askEv.Questions = msg.Metadata["ask_questions"]
	}
	data, _ := json.Marshal(askEv)
	return &protocol.WSMessage{
		Type:    protocol.MsgTypeAskUser,
		TS:      time.Now().Unix(),
		ChatID:  msg.ChatID,
		Content: string(data),
	}
}

// BuildProgressMsg creates a progress event message.
// Returns nil if payload is nil.
func (CliMessageBuilder) BuildProgressMsg(chatID string, payload *protocol.ProgressEvent) *protocol.WSMessage {
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

// BuildStreamContentMsg creates a stream content message.
// The Progress.ChatID carries the "cli:" prefix expected by
// handleProgressMsg's session filter. Returns nil if both content and
// reasoning are empty.
func (CliMessageBuilder) BuildStreamContentMsg(chatID, content, reasoning string) *protocol.WSMessage {
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

// BuildSessionStateMsg creates a session state change message.
func (CliMessageBuilder) BuildSessionStateMsg(ev protocol.SessionEvent) protocol.WSMessage {
	return protocol.WSMessage{
		Type:    protocol.MsgTypeSession,
		TS:      time.Now().Unix(),
		Session: &ev,
	}
}

// BuildInjectUserMsg creates an inject_user message.
func (CliMessageBuilder) BuildInjectUserMsg(chatID, content string) protocol.WSMessage {
	return protocol.WSMessage{
		Type:    protocol.MsgTypeInjectUser,
		TS:      time.Now().Unix(),
		ChatID:  chatID,
		Content: content,
	}
}

// BuildTUIControlReqMsg creates a TUI control request message.
func (CliMessageBuilder) BuildTUIControlReqMsg(id, chatID string, action string, params map[string]string) protocol.WSMessage {
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

// GenerateTUIID creates a unique ID for TUI control requests.
func (CliMessageBuilder) GenerateTUIID() string {
	return fmt.Sprintf("tui-%d", time.Now().UnixNano())
}

// TuiRespTimeout is the timeout for waiting on a TUI control response.
const TuiRespTimeout = 10 * time.Second

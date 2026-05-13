package channel

import (
	"encoding/json"
	"fmt"
	"time"

	"xbot/bus"
	"xbot/protocol"
)

// ChannelCliChannel is the in-process equivalent of RemoteCLIChannel.
// It converts Agent method calls (SendProgress, SendSessionState, etc.)
// into WSMessage events pushed to an event channel, which ChannelTransport
// reads and dispatches to subscribers.
//
// This replaces the local-mode pattern where Agent directly called
// cliCh.SendProgress() → progressCh → asyncCh → TUI.
// Now: Agent → ChannelCliChannel → eventCh → ChannelTransport → baseTransport → Subscribe handler → cliCh → TUI.
// Same path as remote mode, but with Go channels instead of WebSocket.
type ChannelCliChannel struct {
	eventCh chan<- protocol.WSMessage
}

// NewChannelCliChannel creates a ChannelCliChannel that writes to the given event channel.
func NewChannelCliChannel(eventCh chan<- protocol.WSMessage) *ChannelCliChannel {
	return &ChannelCliChannel{eventCh: eventCh}
}

// Channel interface

func (c *ChannelCliChannel) Name() string                                    { return "cli" }
func (c *ChannelCliChannel) Start() error                                    { return nil }
func (c *ChannelCliChannel) Stop()                                           {}
func (c *ChannelCliChannel) SetChatID(string)                                {}
func (c *ChannelCliChannel) SetSendInboundFn(func(bus.InboundMessage) error) {}

// ProgressSender is implemented by channels that can send progress events
// to remote or in-process clients (RemoteCLIChannel, ChannelCliChannel).
// Used by agent's buildCLIProgressEventHandler for type assertion.
type ProgressSender interface {
	SendProgress(chatID string, payload *protocol.ProgressEvent)
	SendStreamContent(chatID, content, reasoning string)
}

func (c *ChannelCliChannel) Send(msg bus.OutboundMessage) (string, error) {
	wsMsg := protocol.WSMessage{
		Type:    protocol.MsgTypeText,
		TS:      time.Now().Unix(),
		Content: msg.Content,
		ChatID:  msg.ChatID,
		Channel: msg.Channel,
	}
	select {
	case c.eventCh <- wsMsg:
		return "", nil
	default:
		return "", fmt.Errorf("channel cli: event channel full")
	}
}

// SendProgress converts a progress event to WSMessage and pushes to the event channel.
func (c *ChannelCliChannel) SendProgress(chatID string, payload *protocol.ProgressEvent) {
	if payload == nil {
		return
	}
	wsMsg := protocol.WSMessage{
		Type:     protocol.MsgTypeProgress,
		TS:       time.Now().Unix(),
		Progress: payload,
		ChatID:   chatID,
	}
	select {
	case c.eventCh <- wsMsg:
	default:
	}
}

// SendSessionState pushes a session state change event.
func (c *ChannelCliChannel) SendSessionState(ev protocol.SessionEvent) {
	wsMsg := protocol.WSMessage{
		Type:    protocol.MsgTypeSession,
		TS:      time.Now().Unix(),
		Session: &ev,
	}
	select {
	case c.eventCh <- wsMsg:
	default:
	}
}

// SendToast pushes a toast notification event.
func (c *ChannelCliChannel) SendToast(msg string) {
	wsMsg := protocol.WSMessage{
		Type:    protocol.MsgTypeText,
		TS:      time.Now().Unix(),
		Content: msg,
	}
	select {
	case c.eventCh <- wsMsg:
	default:
	}
}

// SendStreamContent pushes a stream content event.
func (c *ChannelCliChannel) SendStreamContent(chatID, content, reasoning string) {
	if content == "" && reasoning == "" {
		return
	}
	wsMsg := protocol.WSMessage{
		Type:   protocol.MsgTypeStreamContent,
		TS:     time.Now().Unix(),
		ChatID: chatID,
		Progress: &protocol.ProgressEvent{
			ChatID:                 chatID,
			StreamContent:          content,
			ReasoningStreamContent: reasoning,
		},
	}
	select {
	case c.eventCh <- wsMsg:
	default:
	}
}

// SetConnState is a no-op for in-process channel — always connected.
func (c *ChannelCliChannel) SetConnState(string) {}

// InjectUserMessage pushes an inject_user event.
func (c *ChannelCliChannel) InjectUserMessage(chatID, content string) {
	wsMsg := protocol.WSMessage{
		Type:    protocol.MsgTypeInjectUser,
		TS:      time.Now().Unix(),
		ChatID:  chatID,
		Content: content,
	}
	select {
	case c.eventCh <- wsMsg:
	default:
	}
}

// SendAskUser pushes an ask_user event.
func (c *ChannelCliChannel) SendAskUser(chatID string, ev protocol.AskUserEvent) {
	data, _ := json.Marshal(ev)
	wsMsg := protocol.WSMessage{
		Type:    protocol.MsgTypeAskUser,
		TS:      time.Now().Unix(),
		ChatID:  chatID,
		Content: string(data),
	}
	select {
	case c.eventCh <- wsMsg:
	default:
	}
}

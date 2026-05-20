package channel

import (
	"fmt"
	"sync"
	"time"

	log "xbot/logger"
	"xbot/protocol"
)

// ChannelCliChannel is the in-process equivalent of RemoteCLIChannel.
// It converts Agent method calls (SendProgress, SendSessionState, etc.)
// into WSMessage events pushed to an event channel, which ChannelTransport
// reads and dispatches to subscribers.
//
// Message construction is delegated to cliMessageBuilder (cli_msg_builder.go)
// so the WSMessage format is identical to RemoteCLIChannel — only the
// transport differs (Go channel vs WebSocket).
type ChannelCliChannel struct {
	eventCh      chan<- protocol.WSMessage
	tuiPendingMu sync.Mutex
	tuiPending   map[string]chan *protocol.TUIControlPayload
}

// NewChannelCliChannel creates a ChannelCliChannel that writes to the given event channel.
func NewChannelCliChannel(eventCh chan<- protocol.WSMessage) *ChannelCliChannel {
	return &ChannelCliChannel{
		eventCh:    eventCh,
		tuiPending: make(map[string]chan *protocol.TUIControlPayload),
	}
}

// Channel interface

func (c *ChannelCliChannel) Name() string                            { return "cli" }
func (c *ChannelCliChannel) Start() error                            { return nil }
func (c *ChannelCliChannel) Stop()                                   {}
func (c *ChannelCliChannel) SetChatID(string)                        {}
func (c *ChannelCliChannel) SetSendInboundFn(func(InboundMsg) error) {}

// ProgressSender is implemented by channels that can send progress events
// to remote or in-process clients (RemoteCLIChannel, ChannelCliChannel).
// Used by agent's buildCLIProgressEventHandler for type assertion.
type ProgressSender interface {
	SendProgress(chatID string, payload *protocol.ProgressEvent)
	SendStreamContent(chatID, content, reasoning string)
}

// UserMessageInjector is implemented by channels that support injecting
// user messages from background sources (cron, bg task notifications).
// Used by agent's injectCLIUserMessage for type assertion.
type UserMessageInjector interface {
	InjectUserMessage(chatID, content string)
}

// SessionStateSender is implemented by channels that can receive session
// state change events (e.g. busy/idle, subagent lifecycle, rename).
// Used by Agent internally to push state without external callbacks.
type SessionStateSender interface {
	SendSessionState(ev protocol.SessionEvent)
}

// sendMsg pushes a WSMessage to the event channel. Returns error if full.
func (c *ChannelCliChannel) sendMsg(msg protocol.WSMessage) error {
	select {
	case c.eventCh <- msg:
		return nil
	default:
		return fmt.Errorf("channel cli: event channel full")
	}
}

// sendMsgBestEffort pushes a WSMessage, logging a warning if full.
func (c *ChannelCliChannel) sendMsgBestEffort(msg protocol.WSMessage) {
	select {
	case c.eventCh <- msg:
	default:
		log.WithFields(log.Fields{"type": msg.Type, "chat_id": msg.ChatID}).Warn("ChannelCliChannel: eventCh full, dropping message")
	}
}

func (c *ChannelCliChannel) Send(msg OutboundMsg) (string, error) {
	if err := c.sendMsg(cliMsg.buildTextMsg(msg)); err != nil {
		return "", err
	}
	if askMsg := cliMsg.buildAskUserMsg(msg); askMsg != nil {
		if err := c.sendMsg(*askMsg); err != nil {
			return "", err
		}
	}
	return "", nil
}

func (c *ChannelCliChannel) SendProgress(chatID string, payload *protocol.ProgressEvent) {
	if msg := cliMsg.buildProgressMsg(chatID, payload); msg != nil {
		c.sendMsgBestEffort(*msg)
	}
}

func (c *ChannelCliChannel) SendSessionState(ev protocol.SessionEvent) {
	c.sendMsgBestEffort(cliMsg.buildSessionStateMsg(ev))
}

func (c *ChannelCliChannel) SendToast(msg string) {
	c.sendMsgBestEffort(protocol.WSMessage{
		Type:    protocol.MsgTypeText,
		TS:      time.Now().Unix(),
		Content: msg,
	})
}

func (c *ChannelCliChannel) SendStreamContent(chatID, content, reasoning string) {
	if msg := cliMsg.buildStreamContentMsg(chatID, content, reasoning); msg != nil {
		c.sendMsgBestEffort(*msg)
	}
}

func (c *ChannelCliChannel) SetConnState(string) {}

func (c *ChannelCliChannel) InjectUserMessage(chatID, content string) {
	c.sendMsgBestEffort(cliMsg.buildInjectUserMsg(chatID, content))
}

func (c *ChannelCliChannel) SendTUIControlRequest(chatID string, action string, params map[string]string) (map[string]string, error) {
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

	if err := c.sendMsg(cliMsg.buildTUIControlReqMsg(id, chatID, action, params)); err != nil {
		return nil, err
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

func (c *ChannelCliChannel) DeliverTUIResponse(id string, payload *protocol.TUIControlPayload) {
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

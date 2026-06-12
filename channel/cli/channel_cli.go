package cli

import (
	"fmt"
	"sync"
	"time"
	"xbot/channel"

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
	if err := c.sendMsg(channel.CliMsg.BuildTextMsg(msg)); err != nil {
		return "", err
	}
	if askMsg := channel.CliMsg.BuildAskUserMsg(msg); askMsg != nil {
		if err := c.sendMsg(*askMsg); err != nil {
			return "", err
		}
	}
	return "", nil
}

func (c *ChannelCliChannel) SendProgress(chatID string, payload *protocol.ProgressEvent) {
	if msg := channel.CliMsg.BuildProgressMsg(chatID, payload); msg != nil {
		c.sendMsgBestEffort(*msg)
	}
}

func (c *ChannelCliChannel) SendSessionState(ev protocol.SessionEvent) {
	c.sendMsgBestEffort(channel.CliMsg.BuildSessionStateMsg(ev))
}

func (c *ChannelCliChannel) SendToast(msg string) {
	c.sendMsgBestEffort(protocol.WSMessage{
		Type:    protocol.MsgTypeText,
		TS:      time.Now().Unix(),
		Content: msg,
	})
}

func (c *ChannelCliChannel) SendStreamContent(chatID, content, reasoning string) {
	if msg := channel.CliMsg.BuildStreamContentMsg(chatID, content, reasoning); msg != nil {
		c.sendMsgBestEffort(*msg)
	}
}

func (c *ChannelCliChannel) SetConnState(string) {}

func (c *ChannelCliChannel) InjectUserMessage(chatID, content string) {
	c.sendMsgBestEffort(channel.CliMsg.BuildInjectUserMsg(chatID, content))
}

func (c *ChannelCliChannel) SendTUIControlRequest(chatID string, action string, params map[string]string) (map[string]string, error) {
	id := channel.CliMsg.GenerateTUIID()
	ch := make(chan *protocol.TUIControlPayload, 1)

	c.tuiPendingMu.Lock()
	c.tuiPending[id] = ch
	c.tuiPendingMu.Unlock()

	defer func() {
		c.tuiPendingMu.Lock()
		delete(c.tuiPending, id)
		c.tuiPendingMu.Unlock()
	}()

	if err := c.sendMsg(channel.CliMsg.BuildTUIControlReqMsg(id, chatID, action, params)); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Error != "" {
			return nil, fmt.Errorf("%s", resp.Error)
		}
		return resp.Result, nil
	case <-time.After(channel.TuiRespTimeout):
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

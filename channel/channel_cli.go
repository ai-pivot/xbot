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
	reliableMu   sync.Mutex
	stopCh       chan struct{}
	stopped      bool
}

// NewChannelCliChannel creates a ChannelCliChannel that writes to the given event channel.
func NewChannelCliChannel(eventCh chan<- protocol.WSMessage) *ChannelCliChannel {
	return &ChannelCliChannel{
		eventCh:    eventCh,
		tuiPending: make(map[string]chan *protocol.TUIControlPayload),
		stopCh:     make(chan struct{}),
	}
}

// Channel interface

func (c *ChannelCliChannel) Name() string { return "cli" }
func (c *ChannelCliChannel) Start() error { return nil }
func (c *ChannelCliChannel) Stop() {
	c.reliableMu.Lock()
	if !c.stopped {
		c.stopped = true
		close(c.stopCh)
	}
	c.reliableMu.Unlock()
}
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

// sendMsgReliable waits until a destructive state event is queued or the
// channel stops. Returning only after enqueue preserves reset ordering across
// the rewind operation gate.
func (c *ChannelCliChannel) sendMsgReliable(msg protocol.WSMessage) bool {
	c.reliableMu.Lock()
	if c.stopped {
		c.reliableMu.Unlock()
		return false
	}
	c.reliableMu.Unlock()
	select {
	case c.eventCh <- msg:
		return true
	case <-c.stopCh:
		return false
	}
}

func isDestructiveCLIMessage(msg protocol.WSMessage) bool {
	if msg.SessionReset || msg.Metadata != nil && msg.Metadata["session_reset"] == "true" {
		return true
	}
	return msg.Type == protocol.MsgTypeSession && msg.Session != nil && msg.Session.Action == "history_rewound"
}

func (c *ChannelCliChannel) Send(msg OutboundMsg) (string, error) {
	textMsg := CliMsg.BuildTextMsg(msg)
	if isDestructiveCLIMessage(textMsg) {
		if !c.sendMsgReliable(textMsg) {
			return "", fmt.Errorf("channel cli: stopped")
		}
	} else if err := c.sendMsg(textMsg); err != nil {
		return "", err
	}
	if askMsg := CliMsg.BuildAskUserMsg(msg); askMsg != nil {
		if err := c.sendMsg(*askMsg); err != nil {
			return "", err
		}
	}
	return "", nil
}

func (c *ChannelCliChannel) SendProgress(chatID string, payload *protocol.ProgressEvent) {
	if msg := CliMsg.BuildProgressMsg(chatID, payload); msg != nil {
		c.sendMsgBestEffort(*msg)
	}
}

func (c *ChannelCliChannel) SendSessionState(ev protocol.SessionEvent) {
	msg := CliMsg.BuildSessionStateMsg(ev)
	if isDestructiveCLIMessage(msg) {
		c.sendMsgReliable(msg)
		return
	}
	c.sendMsgBestEffort(msg)
}

func (c *ChannelCliChannel) SendToast(msg string) {
	c.sendMsgBestEffort(protocol.WSMessage{
		Type:    protocol.MsgTypeText,
		TS:      time.Now().Unix(),
		Content: msg,
	})
}

func (c *ChannelCliChannel) SendStreamContent(chatID, content, reasoning string) {
	if msg := CliMsg.BuildStreamContentMsg(chatID, content, reasoning); msg != nil {
		c.sendMsgBestEffort(*msg)
	}
}

func (c *ChannelCliChannel) SetConnState(string) {}

func (c *ChannelCliChannel) InjectUserMessage(chatID, content string) {
	c.sendMsgBestEffort(CliMsg.BuildInjectUserMsg(chatID, content))
}

func (c *ChannelCliChannel) SendTUIControlRequest(chatID string, action string, params map[string]string) (map[string]string, error) {
	id := CliMsg.GenerateTUIID()
	ch := make(chan *protocol.TUIControlPayload, 1)

	c.tuiPendingMu.Lock()
	c.tuiPending[id] = ch
	c.tuiPendingMu.Unlock()

	defer func() {
		c.tuiPendingMu.Lock()
		delete(c.tuiPending, id)
		c.tuiPendingMu.Unlock()
	}()

	if err := c.sendMsg(CliMsg.BuildTUIControlReqMsg(id, chatID, action, params)); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Error != "" {
			return nil, fmt.Errorf("%s", resp.Error)
		}
		return resp.Result, nil
	case <-time.After(TuiRespTimeout):
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

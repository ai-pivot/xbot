package channel

import (
	"context"
	"fmt"
	"sync"

	"xbot/bus"
	"xbot/clipanic"
	log "xbot/logger"
)

// Dispatcher 出站消息分发器
type Dispatcher struct {
	channels map[string]Channel
	bus      *bus.MessageBus
	done     chan struct{}
	mu       sync.RWMutex
}

// NewDispatcher 创建分发器
func NewDispatcher(msgBus *bus.MessageBus) *Dispatcher {
	return &Dispatcher{
		channels: make(map[string]Channel),
		bus:      msgBus,
		done:     make(chan struct{}),
	}
}

// Register 注册渠道
func (d *Dispatcher) Register(ch Channel) {
	d.mu.Lock()
	d.channels[ch.Name()] = ch
	d.mu.Unlock()
	log.WithField("channel", ch.Name()).Info("Channel registered")
}

// Run 启动出站消息分发循环
func (d *Dispatcher) Run() {
	log.Info("Outbound dispatcher started")
	for {
		select {
		case <-d.done:
			return
		case msg, ok := <-d.bus.Outbound:
			if !ok {
				log.Info("Outbound channel closed, dispatcher exiting")
				return
			}
			outMsg := OutboundMsg{
				Channel:     msg.Channel,
				ChatID:      msg.ChatID,
				Content:     msg.Content,
				Media:       msg.Media,
				Metadata:    msg.Metadata,
				IsPartial:   msg.IsPartial,
				WaitingUser: msg.WaitingUser,
				ToolsUsed:   msg.ToolsUsed,
				Error:       msg.Error,
			}
			d.mu.RLock()
			ch, ok := d.channels[outMsg.Channel]
			d.mu.RUnlock()
			if !ok {
				log.WithField("channel", outMsg.Channel).Warn("Unknown channel, dropping message")
				continue
			}
			if _, err := func() (ret string, err error) {
				defer func() {
					if r := recover(); r != nil {
						clipanic.Report("channel.Dispatcher.Send", outMsg, r)
						log.WithField("channel", outMsg.Channel).Errorf("Channel.Send panic: %v", r)
						err = fmt.Errorf("channel %s panic: %v", outMsg.Channel, r)
					}
				}()
				return ch.Send(outMsg)
			}(); err != nil {
				log.WithError(err).WithField("channel", outMsg.Channel).Error("Failed to send message")
			}
		}
	}
}

// Stop 停止分发器
func (d *Dispatcher) Stop() {
	close(d.done)
	d.mu.RLock()
	for _, ch := range d.channels {
		ch.Stop()
	}
	d.mu.RUnlock()
}

// Unregister removes a channel from the dispatcher and stops it.
// This ensures goroutines are cleaned up when channels are removed.
func (d *Dispatcher) Unregister(name string) {
	d.mu.Lock()
	ch, ok := d.channels[name]
	if ok {
		delete(d.channels, name)
	}
	d.mu.Unlock()
	if ok {
		ch.Stop()
		log.WithField("channel", name).Info("Channel unregistered and stopped")
	} else {
		log.WithField("channel", name).Warn("Channel not found for unregister")
	}
}

// SendMessage implements bus.MessageSender.
func (d *Dispatcher) SendMessage(channelName, chatID, content string) (string, error) {
	return d.SendDirect(OutboundMsg{
		Channel: channelName,
		ChatID:  chatID,
		Content: content,
	})
}

// SendMessageCtx implements bus.MessageSenderCtx.
// Propagates ctx to AgentChannel so pending RPCs can be cancelled by the caller.
func (d *Dispatcher) SendMessageCtx(ctx context.Context, channelName, chatID, content string) (string, error) {
	return d.SendDirect(OutboundMsg{
		Channel: channelName,
		ChatID:  chatID,
		Content: content,
		Ctx:     ctx,
	})
}

// Compile-time interface check
var _ bus.MessageSender = (*Dispatcher)(nil)
var _ bus.MessageSenderCtx = (*Dispatcher)(nil)

// SendDirect 同步发送消息到指定渠道，返回平台消息 ID
func (d *Dispatcher) SendDirect(msg OutboundMsg) (string, error) {
	d.mu.RLock()
	ch, ok := d.channels[msg.Channel]
	d.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("unknown channel: %s", msg.Channel)
	}
	return ch.Send(msg)
}

// GetChannel 获取渠道
func (d *Dispatcher) GetChannel(name string) (Channel, bool) {
	d.mu.RLock()
	ch, ok := d.channels[name]
	d.mu.RUnlock()
	return ch, ok
}

// EnabledChannels 返回已注册的渠道列表
func (d *Dispatcher) EnabledChannels() []string {
	d.mu.RLock()
	names := make([]string, 0, len(d.channels))
	for name := range d.channels {
		names = append(names, name)
	}
	d.mu.RUnlock()
	return names
}

// RangeChannels iterates over all registered channels, calling fn for each.
// If fn returns false, iteration stops. This is the canonical way to
// broadcast to all channels (including plugin channels) without hardcoding
// channel names. The caller should type-assert to the desired interface
// (ProgressSender, SessionStateSender, etc.) to filter.
func (d *Dispatcher) RangeChannels(fn func(name string, ch Channel) bool) {
	d.mu.RLock()
	type entry struct {
		name string
		ch   Channel
	}
	entries := make([]entry, 0, len(d.channels))
	for name, ch := range d.channels {
		entries = append(entries, entry{name, ch})
	}
	d.mu.RUnlock()
	for _, e := range entries {
		if !fn(e.name, e.ch) {
			return
		}
	}
}

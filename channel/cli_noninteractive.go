package channel

import (
	"fmt"
	"strings"
	"sync"
	"xbot/bus"
	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// NonInteractiveChannel (Non-interactive mode, single execution)
// ---------------------------------------------------------------------------

// NonInteractiveChannel Non-interactive mode channel, for pipe/argument mode.
// 收到Complete message后打印到 stdout 并设置退出标志。
type NonInteractiveChannel struct {
	msgBus   *bus.MessageBus
	msgCh    chan bus.OutboundMessage
	done     chan struct{}
	doneOnce sync.Once // ensures close(done) is called exactly once
}

// NewNonInteractiveChannel Create non-interactive mode channel
func NewNonInteractiveChannel(msgBus *bus.MessageBus) *NonInteractiveChannel {
	ch := &NonInteractiveChannel{
		msgBus: msgBus,
		msgCh:  make(chan bus.OutboundMessage, 64),
		done:   make(chan struct{}),
	}
	// Start message receiving goroutine
	go ch.run()
	return ch
}

func (c *NonInteractiveChannel) run() {
	var prevContent string
	for msg := range c.msgCh {
		content := msg.Content
		if strings.HasPrefix(content, "__FEISHU_CARD__") {
			content = ConvertFeishuCard(content)
		}
		if msg.IsPartial {
			// Streaming partial message: only output the delta
			if len(content) > len(prevContent) {
				diff := content[len(prevContent):]
				fmt.Print(diff)
			}
			prevContent = content
		} else {
			// Complete message：输出剩余差异部分，然后换行
			if len(content) > len(prevContent) {
				diff := content[len(prevContent):]
				fmt.Print(diff)
			}
			fmt.Println()
			c.doneOnce.Do(func() { close(c.done) })
			return
		}
	}
}

func (c *NonInteractiveChannel) Name() string { return "cli" }
func (c *NonInteractiveChannel) Start() error { return nil }
func (c *NonInteractiveChannel) Stop()        {}
func (c *NonInteractiveChannel) Send(msg bus.OutboundMessage) (string, error) {
	select {
	case c.msgCh <- msg:
	default:
		log.WithField("channel", "non-interactive").Warn("Message dropped: buffer full")
	}
	return "", nil
}
func (c *NonInteractiveChannel) WaitDone() { <-c.done }

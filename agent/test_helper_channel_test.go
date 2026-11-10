package agent

import (
	"xbot/channel"
	"xbot/protocol"
)

// askUserCancelIdleChannel 是记录 SessionEvent 的测试 channel（原定义在
// askuser_cancel_idle_test.go；AskUser 删除后该用例移除，但 cancel_stale_test 仍用）。
type askUserCancelIdleChannel struct {
	events []protocol.SessionEvent
}

func (c *askUserCancelIdleChannel) Name() string                             { return "web" }
func (c *askUserCancelIdleChannel) Start() error                             { return nil }
func (c *askUserCancelIdleChannel) Stop()                                    {}
func (c *askUserCancelIdleChannel) Send(channel.OutboundMsg) (string, error) { return "", nil }
func (c *askUserCancelIdleChannel) SendSessionState(ev protocol.SessionEvent) {
	c.events = append(c.events, ev)
}

package agent

import (
	"encoding/json"

	"xbot/bus"
	"xbot/channel"
	"xbot/protocol"
)

// Communication groups methods for message passing, events, and transport configuration.
type Communication interface {
	SendInbound(msg bus.InboundMessage) error
	Bus() *bus.MessageBus
	SetDirectSend(fn func(bus.OutboundMessage) (string, error))
	SetChannelFinder(fn func(name string) (channel.Channel, bool))
	SetChannelPromptProviders(providers ...ChannelPromptProvider)
	Subscribe(pattern protocol.EventPattern, handler protocol.EventHandler) (cancel func())
	BindChat(chatID string) error
	CallRPC(method string, params any) (json.RawMessage, error)
}

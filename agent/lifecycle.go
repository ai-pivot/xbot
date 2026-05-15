package agent

import (
	"context"

	"xbot/bus"
	"xbot/channel"
	"xbot/protocol"
)

// AgentRunner manages the Agent's lifecycle (start, run, stop).
type AgentRunner interface {
	Start(ctx context.Context) error
	Stop()
	Run(ctx context.Context) error
}

// EventRouter handles bidirectional message/event routing.
type EventRouter interface {
	SendMessage(msg bus.InboundMessage) error
	BindChat(chatID string) error
	Subscribe(pattern protocol.EventPattern, handler protocol.EventHandler) (cancel func())
	ConnState() string
	IsRemote() bool
	ServerURL() string
}

// CallbackRegistry manages callback injection for Agent ↔ Channel binding.
type CallbackRegistry interface {
	SetTUIControlHandler(cb func(action string, params map[string]string) (map[string]string, error))
	WireCallbacks(
		directSend func(msg bus.OutboundMessage) (string, error),
		channelFinder func(name string) (channel.Channel, bool),
		sessionStateHandler func(ev protocol.SessionEvent),
		messageSender bus.MessageSender,
		registerAgentChannel func(name string, runFn bus.RunFn) error,
		unregisterAgentChannel func(name string),
	)
	SetChatRenameFn(fn func(chatID, newName string) (oldName string, err error))
}

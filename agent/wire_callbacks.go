package agent

import (
	"xbot/bus"
	"xbot/channel"
	"xbot/protocol"
)

// WireCallbacks injects ALL shared callbacks into the agent.
// Both cmd/xbot-cli/main.go and serverapp/server.go MUST call this.
//
// IMPORTANT: All parameters are positional (not a struct). This is intentional —
// adding a new parameter changes the function signature, which causes a COMPILE
// ERROR at BOTH call sites. You cannot forget one side.
//
// Callbacks that differ between local/server (e.g. ChatRenameFn, TUICallbacks)
// should NOT go here — use individual Set* methods for those.
func (a *Agent) WireCallbacks(
	directSend func(msg bus.OutboundMessage) (string, error),
	channelFinder func(name string) (channel.Channel, bool),
	sessionStateHandler func(ev protocol.SessionEvent),
	messageSender bus.MessageSender,
	registerAgentChannel func(name string, runFn bus.RunFn) error,
	unregisterAgentChannel func(name string),
) {
	a.directSend = directSend
	a.channelFinder = channelFinder
	if a.settingsSvc != nil {
		a.settingsSvc.SetChannelFinder(channelFinder)
	}
	a.sessionStateHandler = sessionStateHandler
	a.messageSender = messageSender
	a.registerAgentChannel = registerAgentChannel
	a.unregisterAgentChannel = unregisterAgentChannel
}

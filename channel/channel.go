package channel

import "xbot/bus"

// Channel Chat channel interface
type Channel interface {
	// Name Return channel name
	Name() string
	// Start Start channel, blocks until ctx is cancelled
	Start() error
	// Stop Stop channel
	Stop()
	// Send Send message, return platform message ID (for subsequent updates)
	Send(msg bus.OutboundMessage) (string, error)
}

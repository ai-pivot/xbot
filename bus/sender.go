package bus

import "context"

// MessageSender sends messages to any addressable Channel.
// Implemented by channel.Dispatcher. Defined in bus package to avoid
// circular dependencies between channel and tools packages.
type MessageSender interface {
	// SendMessage sends a message to the specified channel/chatID.
	// Returns the response content (for agent channels, RPC) or empty string (for IM).
	SendMessage(channelName, chatID, content string) (string, error)
}

// MessageSenderCtx sends messages with context for cancellation.
// Implemented by channel.Dispatcher. Extensions of MessageSender that
// support caller-context-aware cancellation.
type MessageSenderCtx interface {
	MessageSender
	// SendMessageCtx sends a message with a caller context for cancellation.
	// When ctx is cancelled (e.g. Ctrl+C, timeout), pending RPCs return errors.
	SendMessageCtx(ctx context.Context, channelName, chatID, content string) (string, error)
}

// RunFn runs a SubAgent task and returns the result.
// Defined in bus to avoid channel→agent circular dependency.
type RunFn func(ctx context.Context, task string) (string, error)

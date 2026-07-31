package channel

import "xbot/protocol"

// ProgressSender is implemented by channels that transport the shared
// protocol.ProgressEvent to remote or in-process clients. The agent's single
// progress producer broadcasts the same immutable snapshot/log event to each
// registered ProgressSender.
type ProgressSender interface {
	SendProgress(chatID string, payload *protocol.ProgressEvent)
	SendStreamContent(chatID, content, reasoning string)
}

// UserMessageInjector is implemented by channels that support injecting
// user messages from background sources (cron, bg task notifications).
// Used by agent's injectCLIUserMessage for type assertion.
type UserMessageInjector interface {
	InjectUserMessage(chatID, content string)
}

// SessionStateSender is implemented by channels that can receive session
// state change events (e.g. busy/idle, subagent lifecycle, rename).
// Used by Agent internally to push state without external callbacks.
type SessionStateSender interface {
	SendSessionState(ev protocol.SessionEvent)
}

// PreReplyNotifier is implemented by channels that require text-based ack
// and progress messages before the final LLM reply. These channels lack
// streaming/structured progress (e.g. Feishu patches the existing message
// with progress content, QQ sends progress as separate messages).
//
// Channels with structured progress (Web, CLI via ProgressSender) do NOT
// implement this — they receive progress through SendProgress events and
// don't need ack messages.
//
// The agent uses this capability to decide whether to send ack messages and
// text-based progress, keeping the core loop channel-agnostic. Individual
// messages can still opt out via ReplyPolicyOptional metadata (e.g. Feishu
// @all mentions, NapCat which doesn't support patching).
type PreReplyNotifier interface {
	PreReplyNotify() bool
}

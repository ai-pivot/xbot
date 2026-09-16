package channel

import (
	"xbot/plugin"
	"xbot/protocol"
)

// MetaFinalReply marks the authoritative final reply of a turn. A channel
// holding an open streaming card must close it when this arrives (even when the
// content is empty, as on cancellation).
//
// Feishu consumes it (channel/feishu/feishu_stream_card.go); other channels
// ignore it.
const MetaFinalReply = "final_reply"

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

// QueueStateSender is implemented by channels that can receive the session
// message-queue snapshot (pending messages admitted but not yet dequeued).
// The agent broadcasts a full snapshot on every queue change (enqueue /
// dequeue / cancel); Web renders it as the Staging Tray (queue_state SSE).
type QueueStateSender interface {
	SendQueueState(channel, chatID string, payload *protocol.QueueStatePayload)
}

// AskUserResolvedSender is implemented by channels that transport the
// ask_user_resolved invalidation to their clients. The agent emits one
// whenever a pending AskUser prompt stops being pending (answered, cancelled,
// rewound, or cleared); every channel holding a client-side copy of the
// pending prompt must deliver it so stale prompts collapse immediately —
// without this, answering in one tab/device/channel leaves the prompt alive
// in the others until their next full refresh.
//
// Delivery is repeat-safe, NOT exactly-once: channels may deliver the same
// event multiple times (live broadcast + reconnect replay + reconnect
// reconcile). Clients MUST treat repeats as no-ops (drop the cached prompt
// for the (channel, chat_id) pair, keyed by request_id when present).
type AskUserResolvedSender interface {
	SendAskUserResolved(ev protocol.AskUserResolvedEvent)
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

// WidgetSubscriber is implemented by channels that receive plugin widget/UI
// updates (WidgetRegistry content changes). The agent's single widget producer
// broadcasts a notification to every WidgetSubscriber channel; each channel
// decides how to render (ANSI for TUI, structured JSON for Web) and which of
// its own clients to push to.
//
// This mirrors the ProgressSender / SessionStateSender pattern: the agent
// stays channel-agnostic and never hard-codes channel-specific push logic.
type WidgetSubscriber interface {
	// SetWidgetRegistry injects the widget rendering registry. Called once at
	// channel registration time (when the plugin system is available).
	SetWidgetRegistry(wr *plugin.WidgetRegistry)
	// NotifyWidgetsUpdated tells the channel that widget content changed.
	// The channel decides which sessions to render and how to deliver the
	// update to its clients.
	NotifyWidgetsUpdated()
}

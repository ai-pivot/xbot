package protocol

import (
	"xbot/bus"
)

// InboundMessage 统一的入站消息（protocol 包内使用）。
type InboundMessage struct {
	bus.MessagePayload

	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// OutboundMessage 统一的出站消息（protocol 包内使用）。
type OutboundMessage struct {
	bus.MessagePayload

	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`

	IsPartial   bool     `json:"is_partial,omitempty"`
	ToolsUsed   []string `json:"tools_used,omitempty"`
	WaitingUser bool     `json:"waiting_user,omitempty"`
	Error       string   `json:"error,omitempty"`
}

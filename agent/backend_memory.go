package agent

import (
	"context"
	"time"

	"xbot/protocol"
)

// MemoryManagement groups methods for memory, history, and token state operations.
type MemoryManagement interface {
	ClearMemory(ctx context.Context, channel, chatID, targetType, senderID string) error
	GetMemoryStats(ctx context.Context, channel, chatID, senderID string) map[string]string
	GetHistory(channel, chatID string) ([]protocol.HistoryMessage, error)
	TrimHistory(channel, chatID string, cutoff time.Time) error
	GetTokenState(channel, chatID string) (promptTokens, completionTokens int64, err error)
	ResetTokenState()
	GetUserTokenUsage(senderID string) (map[string]any, error)
	GetDailyTokenUsage(senderID string, days int) ([]map[string]any, error)
}

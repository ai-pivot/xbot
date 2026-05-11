package agent

import (
	"xbot/protocol"
	"xbot/session"
)

// SessionManagement groups methods for session configuration and introspection.
type SessionManagement interface {
	MultiSession() *session.MultiTenantSession
	SetCWD(ch, chatID, dir string) error
	SetMaxIterations(n int)
	SetMaxConcurrency(n int)
	SetMaxContextTokens(n int)
	SetCompressionThreshold(f float64)
	IsProcessing(ch, chatID string) bool
	GetActiveProgress(ch, chatID string) *protocol.ProgressEvent
	GetTodos(ch, chatID string) []protocol.TodoItem
}

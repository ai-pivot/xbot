package agent

import "context"

// InteractiveManagement groups methods for interactive subagent session management.
type InteractiveManagement interface {
	CountInteractiveSessions(channelName, chatID string) int
	ListInteractiveSessions(channelName, chatID string) []InteractiveSessionInfo
	InspectInteractiveSession(ctx context.Context, roleName, channelName, chatID, instance string, tailCount int) (string, error)
	GetSessionMessages(channelName, chatID, roleName, instance string) ([]SessionMessage, bool)
	GetAgentSessionDump(channelName, chatID, roleName, instance string) (*AgentSessionDump, bool)
	GetAgentSessionDumpByFullKey(fullKey string) (*AgentSessionDump, bool)
}

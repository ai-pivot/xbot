package agent

import (
	"xbot/agent/hooks"
	llm "xbot/llm"
	"xbot/plugin"
	"xbot/tools"
)

// AgentBackend abstracts where the agent loop runs.
// Backend is the single unified implementation that supports both
// local (in-process Agent) and remote (WebSocket Transport) modes.
//
// CLI uses this interface to interact with the agent regardless of location.
// Management methods may return nil for remote mode (where the operation
// runs server-side); callers should nil-check as appropriate.
//
// AgentBackend embeds focused sub-interfaces defined in this package
// for better interface segregation.
type AgentBackend interface {
	Lifecycle
	LLMManagement
	SettingsManagement
	SessionManagement
	MemoryManagement
	SubscriptionManagement
	InteractiveManagement
	BgTaskManagement
	TenantManagement
	ToolManagement
	Communication

	// IsRemote returns true if the backend is remote (server-side agent loop).
	// Callers should use this to guard calls that return nil for RemoteBackend
	// (SettingsService, LLMFactory, BgTaskManager, HookManager, MultiSession).
	IsRemote() bool

	// ConnState returns the current connection state.
	// Local: always "connected". Remote: "connected"/"disconnected"/"reconnecting".
	ConnState() string

	// ServerURL returns the remote server URL.
	// Local: returns empty string.
	ServerURL() string

	// Agent returns the underlying *Agent.
	// Local: returns the agent. Remote: returns nil.
	Agent() *Agent

	// LLMFactory returns the LLM factory for model management.
	LLMFactory() *LLMFactory

	// SettingsService returns the settings service.
	SettingsService() *SettingsService

	// PluginManager returns the plugin manager.
	// LocalBackend delegates to Agent; RemoteBackend returns nil.
	PluginManager() *plugin.PluginManager

	// HookManager returns the tool hook manager.
	HookManager() *hooks.Manager

	// ApprovalState returns the approval state for runtime handler injection.
	// LocalBackend delegates to Agent; RemoteBackend returns nil.
	ApprovalState() *hooks.ApprovalState

	// BgTaskManager returns the background task manager.
	BgTaskManager() *tools.BackgroundTaskManager

	// SetProxyLLM injects a proxy LLM for a specific user.
	SetProxyLLM(senderID string, proxy *llm.ProxyLLM, model string)

	// ClearProxyLLM removes the proxy LLM for a specific user.
	ClearProxyLLM(senderID string)

	// GetChannelConfigs returns channel configurations as flat maps.
	// Keys: "web", "feishu", "qq", "napcat".
	GetChannelConfigs() (map[string]map[string]string, error)

	// SetChannelConfig updates a channel's configuration in config.json.
	// channel: "web", "feishu", "qq", or "napcat".
	SetChannelConfig(channel string, values map[string]string) error

	// SetChannelReconfigureFn sets a callback to restart a channel after config changes.
	// In remote mode, channel restart is handled server-side via RPC; this is a no-op.
	SetChannelReconfigureFn(fn func(channel string))
}

package agent

import (
	"context"
	"encoding/json"
	"time"

	"xbot/agent/hooks"
	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	"xbot/event"
	llm "xbot/llm"
	"xbot/plugin"
	"xbot/protocol"
	"xbot/session"
	"xbot/tools"
)

// AgentBackend abstracts where the agent loop runs.
// Backend is the single unified implementation that supports both
// local (in-process Agent) and remote (WebSocket Transport) modes.
//
// CLI uses this interface to interact with the agent regardless of location.
// Management methods may return nil for remote mode (where the operation
// runs server-side); callers should nil-check as appropriate.
type AgentBackend interface {
	// --- Lifecycle ---
	Start(ctx context.Context) error
	Stop()
	Close() error
	Run(ctx context.Context) error
	IsRemote() bool
	ConnState() string
	ServerURL() string

	// --- Agent access ---
	Agent() *Agent
	LLMFactory() *LLMFactory
	SettingsService() *SettingsService
	PluginManager() *plugin.PluginManager
	HookManager() *hooks.Manager
	ApprovalState() *hooks.ApprovalState
	BgTaskManager() *tools.BackgroundTaskManager

	// --- LLM Management ---
	ListModels() []string
	ListAllModels() []string
	GetDefaultModel() string
	SetUserModel(senderID, model string) error
	SwitchModel(senderID, model, chatID string) error
	GetContextMode() string
	SetContextMode(mode string) error
	SetModelTiers(cfg config.LLMConfig) error
	GetUserMaxContext(senderID string) int
	SetUserMaxContext(senderID string, maxContext int) error
	GetUserMaxOutputTokens(senderID string) int
	SetUserMaxOutputTokens(senderID string, maxTokens int) error
	GetUserThinkingMode(senderID string) string
	SetUserThinkingMode(senderID string, mode string) error
	GetLLMConcurrency(senderID string) int
	SetLLMConcurrency(senderID string, personal int) error
	SetDefaultThinkingMode(mode string) error
	SetProxyLLM(senderID string, proxy *llm.ProxyLLM, model string)
	ClearProxyLLM(senderID string)

	// --- Settings ---
	GetSettings(namespace, senderID string) (map[string]string, error)
	SetSetting(namespace, senderID, key, value string) error
	SetTUICallbacks(
		tuiCtrl func(action string, params map[string]string) (map[string]string, error),
		configGet func(key string) (string, error),
		configSet func(key, value string) (string, error),
	)
	SetTUIControlHandler(callback func(action string, params map[string]string) (map[string]string, error))
	SetChatRenameFn(chatRename func(chatID, newName string) (oldName string, err error))

	// --- Session ---
	MultiSession() *session.MultiTenantSession
	SetCWD(ch, chatID, dir string) error
	SetMaxIterations(n int)
	SetMaxConcurrency(n int)
	SetMaxContextTokens(n int, chatID ...string)
	SetCompressionThreshold(f float64)
	IsProcessing(ch, chatID string) bool
	GetActiveProgress(ch, chatID string) *protocol.ProgressEvent
	GetTodos(ch, chatID string) []protocol.TodoItem

	// --- Memory & History ---
	ClearMemory(ctx context.Context, channel, chatID, targetType, senderID string) error
	GetMemoryStats(ctx context.Context, channel, chatID, senderID string) map[string]string
	GetHistory(channel, chatID string) ([]protocol.HistoryMessage, error)
	TrimHistory(channel, chatID string, cutoff time.Time) error
	GetTokenState(channel, chatID string) (promptTokens, completionTokens int64, err error)
	ResetTokenState()
	GetUserTokenUsage(senderID string) (map[string]any, error)
	GetDailyTokenUsage(senderID string, days int) ([]map[string]any, error)

	// --- Subscriptions ---
	ListSubscriptions(senderID string) ([]protocol.Subscription, error)
	GetDefaultSubscription(senderID string) (*protocol.Subscription, error)
	AddSubscription(senderID string, sub protocol.Subscription) error
	RemoveSubscription(id string) error
	SetDefaultSubscription(id string, chatID string) error
	RenameSubscription(id, name string) error
	UpdateSubscription(id string, sub protocol.Subscription) error
	SetSubscriptionModel(id, model string) error

	// --- Interactive SubAgent ---
	CountInteractiveSessions(channelName, chatID string) int
	ListInteractiveSessions(channelName, chatID string) []InteractiveSessionInfo
	InspectInteractiveSession(ctx context.Context, roleName, channelName, chatID, instance string, tailCount int) (string, error)
	GetSessionMessages(channelName, chatID, roleName, instance string) ([]SessionMessage, bool)
	GetAgentSessionDump(channelName, chatID, roleName, instance string) (*AgentSessionDump, bool)
	GetAgentSessionDumpByFullKey(fullKey string) (*AgentSessionDump, bool)

	// --- Background Tasks ---
	GetBgTaskCount(sessionKey string) int
	ListBgTasks(sessionKey string) ([]BgTaskJSON, error)
	KillBgTask(taskID string) error
	CleanupCompletedBgTasks(sessionKey string)

	// --- Tenants ---
	ListTenants() ([]TenantInfo, error)

	// --- Tools ---
	RegisterCoreTool(tool tools.Tool)
	RegisterTool(tool tools.Tool)
	IndexGlobalTools()
	SetSandbox(sb tools.Sandbox, mode string)
	GetCardBuilder() *tools.CardBuilder
	SetEventRouter(router *event.Router)
	RegistryManager() *RegistryManager

	// --- Communication ---
	SendInbound(msg bus.InboundMessage) error
	Bus() *bus.MessageBus
	SetDirectSend(fn func(bus.OutboundMessage) (string, error))
	SetChannelFinder(fn func(name string) (channel.Channel, bool))
	SetChannelPromptProviders(providers ...ChannelPromptProvider)
	Subscribe(pattern protocol.EventPattern, handler protocol.EventHandler) (cancel func())
	BindChat(chatID string) error
	CallRPC(method string, params any) (json.RawMessage, error)

	// --- Channel Config ---
	GetChannelConfigs() (map[string]map[string]string, error)
	SetChannelConfig(channel string, values map[string]string) error
	SetChannelReconfigureFn(fn func(channel string))
}

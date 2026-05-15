package agent

import (
	"context"
	"encoding/json"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	llm "xbot/llm"
	"xbot/protocol"
	"xbot/tools"
)

// AgentBackend is the client-side interface for interacting with an agent.
// Most methods are RPC calls — the agent may run in-process (via ChannelTransport)
// or on a remote server (via RemoteTransport).
//
// Architecture (post-refactor):
//
//	Backend holds: Transport (Call/Close) + AgentRunner + EventRouter + CallbackRegistry
//	- Lifecycle methods (Start/Stop/Run) → AgentRunner
//	- Communication methods (SendInbound/Subscribe/BindChat) → EventRouter
//	- Callback methods (WireCallbacks/SetTUIControlHandler) → CallbackRegistry
//	- All other methods → Transport.Call() (RPC)
//
// Tool registration methods (RegisterCoreTool, etc.) require a local Agent
// and are no-op over remote transports.
type AgentBackend interface {
	// --- Lifecycle (delegated to AgentRunner) ---
	Start(ctx context.Context) error
	Stop()
	Close() error
	Run(ctx context.Context) error
	IsRemote() bool
	ConnState() string
	ServerURL() string

	// --- LLM Management (all via RPC) ---
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
	SetModelContexts(contexts map[string]int) error
	SetGlobalMaxTokens(maxTokens int) error
	SetRetryConfig(cfg llm.RetryConfig) error
	SetChatLLM(chatID string, provider string, llmCfg config.LLMConfig) error
	ClearProxyLLM(senderID string)

	// --- Settings (via RPC) ---
	GetSettings(namespace, senderID string) (map[string]string, error)
	SetSetting(namespace, senderID, key, value string) error
	SetTUIControlHandler(callback func(action string, params map[string]string) (map[string]string, error))

	// --- Session (via RPC) ---
	SetCWD(ch, chatID, dir string) error
	SetMaxIterations(n int)
	SetMaxConcurrency(n int)
	SetMaxContextTokens(n int, chatID ...string)
	GetEffectiveMaxContext(senderID, chatID string) int
	ClearPerChatMaxContext(chatID string)
	SetCompressionThreshold(f float64)
	IsProcessing(ch, chatID string) bool
	GetActiveProgress(ch, chatID string) *protocol.ProgressEvent
	GetTodos(ch, chatID string) []protocol.TodoItem

	// --- Memory & History (via RPC) ---
	ClearMemory(ctx context.Context, channel, chatID, targetType, senderID string) error
	GetMemoryStats(ctx context.Context, channel, chatID, senderID string) map[string]string
	GetHistory(channel, chatID string) ([]protocol.HistoryMessage, error)
	TrimHistory(channel, chatID string, cutoff time.Time) error
	GetTokenState(channel, chatID string) (promptTokens, completionTokens int64, err error)
	ResetTokenState()
	GetUserTokenUsage(senderID string) (map[string]any, error)
	GetDailyTokenUsage(senderID string, days int) ([]map[string]any, error)

	// --- Subscriptions (via RPC) ---
	ListSubscriptions(senderID string) ([]protocol.Subscription, error)
	GetDefaultSubscription(senderID string) (*protocol.Subscription, error)
	AddSubscription(senderID string, sub protocol.Subscription) error
	RemoveSubscription(id string) error
	SetDefaultSubscription(id string, chatID string) error
	RenameSubscription(id, name string) error
	UpdateSubscription(id string, sub protocol.Subscription) error
	UpdatePerModelConfig(id, model string, pmc protocol.PerModelConfig) error
	SetSubscriptionModel(id, model string) error

	// --- Interactive SubAgent (via RPC) ---
	CountInteractiveSessions(channelName, chatID string) int
	ListInteractiveSessions(channelName, chatID string) []InteractiveSessionInfo
	InspectInteractiveSession(ctx context.Context, roleName, channelName, chatID, instance string, tailCount int) (string, error)
	GetSessionMessages(channelName, chatID, roleName, instance string) ([]SessionMessage, bool)
	GetAgentSessionDump(channelName, chatID, roleName, instance string) (*AgentSessionDump, bool)
	GetAgentSessionDumpByFullKey(fullKey string) (*AgentSessionDump, bool)

	// --- Background Tasks (via RPC) ---
	GetBgTaskCount(sessionKey string) int
	ListBgTasks(sessionKey string) ([]BgTaskJSON, error)
	KillBgTask(taskID string) error
	CleanupCompletedBgTasks(sessionKey string)

	// --- Tenants (via RPC) ---
	ListTenants() ([]TenantInfo, error)

	// --- Tools (local mode only — direct Agent access) ---
	RegisterCoreTool(tool tools.Tool)
	RegisterTool(tool tools.Tool)
	IndexGlobalTools()
	SetSandbox(sb tools.Sandbox, mode string)

	// --- Communication (delegated to EventRouter) ---
	SendInbound(msg bus.InboundMessage) error
	Subscribe(pattern protocol.EventPattern, handler protocol.EventHandler) (cancel func())
	BindChat(chatID string) error
	CallRPC(method string, params any) (json.RawMessage, error)

	// --- Web Users (via RPC) ---
	CreateWebUser(username string) (password string, err error)
	ListWebUsers() ([]map[string]any, error)
	DeleteWebUser(username string) error

	// --- Chat Management (via RPC) ---
	DeleteChat(channel, senderID, chatID string) error
	RenameChat(channel, senderID, chatID, newName string) error

	// --- Channel Config (via RPC) ---
	GetChannelConfigs() (map[string]map[string]string, error)
	SetChannelConfig(channel string, values map[string]string) error

	// --- Callback Injection (delegated to CallbackRegistry) ---
	WireCallbacks(
		directSend func(msg bus.OutboundMessage) (string, error),
		channelFinder func(name string) (channel.Channel, bool),
		sessionStateHandler func(ev protocol.SessionEvent),
		messageSender bus.MessageSender,
		registerAgentChannel func(name string, runFn bus.RunFn) error,
		unregisterAgentChannel func(name string),
	)
	SetChatRenameFn(fn func(chatID, newName string) (oldName string, err error))
}

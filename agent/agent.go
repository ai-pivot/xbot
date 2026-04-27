package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xbot/agent/hooks"
	"xbot/bus"
	"xbot/channel"
	"xbot/clipanic"
	"xbot/cron"
	"xbot/event"
	"xbot/llm"
	log "xbot/logger"
	"xbot/memory"
	"xbot/memory/letta"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// ErrLLMGenerate indicates an LLM generation call failure (network, API 4xx/5xx, etc.)
// cardExpiryDuration is how long to keep expired Feishu cards before cleanup.
const cardExpiryDuration = 24 * time.Hour

// ErrLLMGenerate is returned when LLM generation fails.
var ErrLLMGenerate = errors.New("LLM generate failed")

// assertNoSystemPersist checks that a system message is not being persisted to session.
// Returns error if a system message is detected — callers should skip the message and log.
func assertNoSystemPersist(m llm.ChatMessage) error {
	if m.Role == "system" {
		log.WithField("message", m).Error("ASSERT: must not persist system message to session")
		return fmt.Errorf("must not persist system message to session")
	}
	return nil
}

// copyMessages creates a shallow copy of the messages slice so that
// in-place modifications (e.g. stripSystemReminder) don't mutate the
// original cfg.Messages backing array or session storage.
func copyMessages(msgs []llm.ChatMessage) []llm.ChatMessage {
	cpy := make([]llm.ChatMessage, len(msgs))
	copy(cpy, msgs)
	return cpy
}

// formatErrorForUser formats an error into a user-visible message
func formatErrorForUser(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrLLMGenerate) {
		return fmt.Sprintf("LLM 服务调用失败，请稍后重试或检查Configuration。\n错误详情: %v", err)
	}
	return fmt.Sprintf("处理消息时发生错误: %v", err)
}

func resolveGlobalSkillsDirs(legacySkillsDir string) []string {
	if legacySkillsDir == "" {
		return nil
	}
	abs, err := filepath.Abs(legacySkillsDir)
	if err != nil {
		return nil
	}
	return []string{abs}
}

// metaTools are tools that manage/search other tools — not useful to index.
var metaTools = map[string]bool{
	"search_tools": true,
	"load_tools":   true,
	"manage_tools": true,
}

// IndexGlobalTools indexes all global tools for semantic search:
// built-in registry tools, tool groups, and global MCP servers.
// Call after all tools are registered. Uses full-replace semantics
// so stale entries from removed tools are automatically cleaned up.
func (a *Agent) IndexGlobalTools() {
	registry := a.tools
	multiSession := a.multiSession
	globalMCPConfigPath := filepath.Join(a.xbotHome, "mcp.json")

	ctx := context.Background()
	var toolEntries []memory.ToolIndexEntry
	indexed := make(map[string]bool) // track indexed tool names to avoid duplicates

	// 1. Index built-in tool groups (like Feishu tools)
	toolGroups := registry.GetToolGroups()
	for _, group := range toolGroups {
		for _, toolName := range group.ToolNames {
			tool, ok := registry.Get(toolName)
			desc := fmt.Sprintf("Built-in tool group: %s", group.Name)
			var channels []string
			if ok {
				if toolDesc := tool.Description(); toolDesc != "" {
					desc = fmt.Sprintf("Tool: %s. %s", toolName, toolDesc)
				}
				if cp, ok := tool.(tools.ChannelProvider); ok {
					channels = cp.SupportedChannels()
				}
			}
			if group.Instructions != "" {
				desc = fmt.Sprintf("%s. %s", desc, group.Instructions)
			}
			toolEntries = append(toolEntries, memory.ToolIndexEntry{
				Name:        toolName,
				ServerName:  group.Name,
				Source:      "global",
				Description: desc,
				Channels:    channels,
			})
			indexed[toolName] = true
		}
	}

	// 2. Index all registry tools not already covered by tool groups
	for _, tool := range registry.List() {
		name := tool.Name()
		if indexed[name] || metaTools[name] {
			continue
		}
		var channels []string
		if cp, ok := tool.(tools.ChannelProvider); ok {
			channels = cp.SupportedChannels()
		}
		toolEntries = append(toolEntries, memory.ToolIndexEntry{
			Name:        name,
			ServerName:  "builtin",
			Source:      "global",
			Description: tool.Description(),
			Channels:    channels,
		})
		indexed[name] = true
	}

	// 3. Index global MCP servers (non-blocking: starts background init, re-indexes once on completion)
	//    We do NOT use SetOnChange here because IndexGlobalTools creates a fresh
	//    mcpMgr each call, and onChange would trigger another IndexGlobalTools →
	//    another mcpMgr → infinite goroutine chain. Instead, we fire a single
	//    background re-index that creates its own mcpMgr with sync.Once guard.
	dummySessionKey := "indexing:dummy"
	mcpMgr := tools.NewSessionMCPManager(
		dummySessionKey,
		"system0",
		globalMCPConfigPath,
		"", "", 30*time.Minute,
	)
	if mcpMgr != nil {
		catalog := mcpMgr.GetCatalog() // non-blocking: returns current (may be empty on first call)
		for _, entry := range catalog {
			for _, toolName := range entry.ToolNames {
				fullName := fmt.Sprintf("mcp_%s_%s", entry.Name, toolName)
				desc := fmt.Sprintf("MCP server: %s. Tool: %s", entry.Name, toolName)
				if entry.Instructions != "" {
					desc = fmt.Sprintf("%s. %s", desc, entry.Instructions)
				}
				toolEntries = append(toolEntries, memory.ToolIndexEntry{
					Name:        fullName,
					ServerName:  entry.Name,
					Source:      "global",
					Description: desc,
				})
			}
		}
		mcpMgr.Close()
	}

	if len(toolEntries) == 0 {
		log.Info("No tools to index")
		return
	}

	if err := multiSession.IndexToolsForTenant(ctx, 0, toolEntries); err != nil {
		log.WithError(err).Warn("Failed to index global tools")
		return
	}

	log.WithField("count", len(toolEntries)).Infof("Indexed %d global tools (registry + tool groups + MCP)", len(toolEntries))
}

// Agent core engine
type Agent struct {
	bus              *bus.MessageBus
	multiSession     *session.MultiTenantSession // Multi-tenant session manager
	tools            *tools.Registry
	maxIterations    int
	purgeOldMessages bool

	skills             *SkillStore
	agents             *AgentStore
	chatHistory        *tools.ChatHistoryStore // chat history cache
	cardBuilder        *tools.CardBuilder      // Card Builder MCP
	workDir            string
	promptLoader       *PromptLoader
	pipeline           *MessagePipeline // message build pipeline (holds instance, supports runtime dynamic add/remove of middleware)
	cronPipeline       *MessagePipeline // Cron-specific message build pipeline
	sandboxMode        string           // "none" or "docker"
	sandbox            tools.Sandbox    // Sandbox instance reference (added in V4)
	sandboxIdleTimeout time.Duration    // sandbox idle timeout (0 to disable)
	directWorkspace    string           // when non-empty, workspaceRoot() returns this directly (used in CLI mode, replaces singleUser workspace shortcut)
	maxConcurrency     int              // max concurrent session processing count
	globalSem          chan struct{}    // global concurrency semaphore (dynamically rebuilt by SetMaxConcurrency)
	globalSemMu        sync.Mutex       // protects globalSem replacement
	globalSkillDirs    []string         // global skill directories (host machine paths)
	agentsDir          string
	xbotHome           string // global xbot config dir (e.g. ~/.xbot), used for mcp.json etc.

	// context management configuration
	contextManagerConfig *ContextManagerConfig
	contextManagerMu     sync.RWMutex // protects concurrent read/write of contextManager
	contextManager       ContextManager

	// SubAgent depth control
	maxSubAgentDepth int

	// Cron service and scheduler
	cronSvc *sqlite.CronService
	cronSch *cron.Scheduler

	// Event trigger router
	eventRouter *event.Router

	// User LLM config service and factory
	llmConfigSvc *sqlite.UserLLMConfigService
	llmFactory   *LLMFactory

	// user-level semaphore: users with custom LLM configuration use independent semaphores
	// key: senderID, value: user-independent semaphore (capacity 1)
	userSemaphores sync.Map // map[string]chan struct{}

	commands         *CommandRegistry                          // command registry
	directSend       func(bus.OutboundMessage) (string, error) // synchronous send, bypasses bus to get message_id
	sessionMsgIDs    sync.Map                                  // key: "channel:chatID" -> current session sent message IDs (for Patch updates)
	sessionReplyTo   sync.Map                                  // key: "channel:chatID" -> user inbound message ID (for first reply reply mode)
	sessionFinalSent sync.Map                                  // key: "channel:chatID" -> bool, tool has sent final reply (e.g. card), subsequent sendMessage skipped

	// per-request cancel: used by /cancel to cancel the currently processing request
	// key: "channel:chatID:senderID" -> chan struct{} (buffered, cap=1)
	chatCancelCh sync.Map

	// lastProgressSnapshot stores the latest CLIProgressPayload per active chat,
	// updated by ProgressEventHandler during processing. Used by GetActiveProgress
	// RPC to restore progress state on mid-session reconnect.
	// key: "channel:chatID" -> *channel.CLIProgressPayload
	lastProgressSnapshot sync.Map

	// iterationHistories stores completed iteration snapshots per active chat.
	// key: "channel:chatID" -> *[]channel.CLIProgressPayload (one per completed iteration)
	// On turn end, the entry is deleted.
	iterationHistories sync.Map

	// interactiveSubAgents stores interactive SubAgent sessions
	// key: "channel:chatID/roleName" -> *interactiveAgent
	// sync.Map provides atomic Load/Store/Delete/LoadOrStore, no additional mutex needed
	interactiveSubAgents sync.Map

	// messageSender allows sending messages to any Channel via Dispatcher.
	messageSender bus.MessageSender
	// registerAgentChannel registers an AgentChannel in the Dispatcher.
	registerAgentChannel func(name string, runFn bus.RunFn) error
	// unregisterAgentChannel removes an AgentChannel from the Dispatcher.
	unregisterAgentChannel func(name string)

	// hookManager is the shared tool execution hook manager for this Agent and all SubAgents.
	hookManager *hooks.Manager

	// timingData collects per-tool execution timing statistics.
	timingData *hooks.TimingData

	// approvalState manages approval handling for privileged operations.
	approvalState *hooks.ApprovalState

	// OffloadStore manages large tool result offload to disk
	offloadStore *OffloadStore

	// maskStore manages observation masking storage
	maskStore *ObservationMaskStore

	// contextEditor manages context editing (Context Editing tool)
	contextEditor *ContextEditor

	// todoManager manages the current session's TODO list
	todoManager *tools.TodoManager

	// channelPromptProviders channel-specific prompt provider list (injected externally)
	channelPromptProviders []ChannelPromptProvider

	// RegistryManager for skill/agent sharing and marketplace
	registryManager *RegistryManager

	// SettingsService for per-user settings
	settingsSvc *SettingsService

	// channelFinder looks up a channel instance by name (injected from main.go).
	channelFinder func(name string) (channel.Channel, bool)

	// bgTaskMgr manages background shell tasks (shared across all sessions)
	bgTaskMgr *tools.BackgroundTaskManager

	// bgRunActive is atomically set to 1 when a Run is active and consuming bg notifications,
	// 0 when idle. Used by bgNotifyLoop to decide routing.
	bgRunActive int32

	// bgRunPending buffers bg notifications that arrived during an active Run.
	// The Run loop drains these between iterations.
	bgRunPending   []tools.BgNotification
	bgRunPendingMu sync.Mutex

	// agentCtx is the Agent-level context, set when Run() starts and cancelled when Run() exits.
	// Background interactive subagents derive their context from this (not from per-request ctx)
	// so they survive across multiple requests and only stop when the parent Agent process exits.
	agentCtx    context.Context
	agentCancel context.CancelFunc
}

// SetRegistryManager sets the RegistryManager (for external injection or override).
func (a *Agent) SetRegistryManager(rm *RegistryManager) { a.registryManager = rm }

// SetSettingsService sets the SettingsService (for external injection or override).
func (a *Agent) SetSettingsService(svc *SettingsService) { a.settingsSvc = svc }

// LLMFactory returns the Agent's LLMFactory (for external injection of callbacks).
func (a *Agent) LLMFactory() *LLMFactory { return a.llmFactory }

// BgTaskManager returns the Agent's BackgroundTaskManager.
func (a *Agent) BgTaskManager() *tools.BackgroundTaskManager { return a.bgTaskMgr }

// SetMessageSender sets the Dispatcher reference for unified messaging.
func (a *Agent) SetMessageSender(ms bus.MessageSender) { a.messageSender = ms }

// SetAgentChannelRegistry sets the callbacks for registering/unregistering AgentChannels.
func (a *Agent) SetAgentChannelRegistry(register func(name string, runFn bus.RunFn) error, unregister func(name string)) {
	a.registerAgentChannel = register
	a.unregisterAgentChannel = unregister
}

// RegistryManager returns the Agent's RegistryManager (for external injection of callbacks).
func (a *Agent) RegistryManager() *RegistryManager { return a.registryManager }

// SettingsService returns the Agent's SettingsService (for external injection of callbacks).
func (a *Agent) SettingsService() *SettingsService { return a.settingsSvc }

// MultiSession returns the Agent's MultiTenantSession (for external injection of callbacks).
func (a *Agent) MultiSession() *session.MultiTenantSession { return a.multiSession }

// SetUserModel sets the model for a user's LLM configuration (used by settings card callback).
func (a *Agent) SetUserModel(senderID, model string) error {
	cfg, err := a.llmConfigSvc.GetConfig(senderID)
	if err != nil {
		return fmt.Errorf("get LLM config: %w", err)
	}
	if cfg == nil {
		return fmt.Errorf("user has no custom LLM config; use /set-llm first")
	}
	cfg.Model = model
	if err := a.llmConfigSvc.SetConfig(cfg); err != nil {
		return fmt.Errorf("save model: %w", err)
	}
	a.llmFactory.Invalidate(senderID)
	return nil
}

// SetChannelFinder sets the channel finder callback (for external injection).
// Also propagates to SettingsService so it can resolve channels by name.
func (a *Agent) SetChannelFinder(fn func(name string) (channel.Channel, bool)) {
	a.channelFinder = fn
	if a.settingsSvc != nil {
		a.settingsSvc.SetChannelFinder(fn)
	}
}

// IsProcessing returns true if there is an active Run for the given sender.
func (a *Agent) IsProcessing(senderID string) bool {
	found := false
	a.chatCancelCh.Range(func(key, _ interface{}) bool {
		if k, ok := key.(string); ok && strings.HasSuffix(k, ":"+senderID) {
			found = true
			return false
		}
		return true
	})
	return found
}

// SetProxyLLM injects a ProxyLLM for a user (when their active runner has local LLM).
func (a *Agent) SetProxyLLM(senderID string, proxy *llm.ProxyLLM, model string) {
	a.llmFactory.SetProxyLLM(senderID, proxy, model)
}

// ClearProxyLLM removes a ProxyLLM for a user.
func (a *Agent) ClearProxyLLM(senderID string) {
	a.llmFactory.ClearProxyLLM(senderID)
}

// GetDefaultModel returns the default model name.
func (a *Agent) GetDefaultModel() string {
	return a.llmFactory.GetDefaultModel()
}
func (a *Agent) GetSettingsService() *SettingsService {
	return a.settingsSvc
}

func buildToolMessageContent(result *tools.ToolResult) string {
	if result == nil {
		return ""
	}
	// Combine Summary + Detail + Tips into plain text to avoid JSON serialization escaping newlines.
	// The old approach using json.Marshal(result) caused diff newlines in Detail to be encoded as \n,
	// LLM sees unreadable text blocks instead of formatted diffs.
	var sb strings.Builder
	if result.Summary != "" {
		sb.WriteString(result.Summary)
	}
	if result.Detail != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(result.Detail)
	}
	if result.Tips != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(result.Tips)
	}
	return sb.String()
}

// Config Agent Configuration
type Config struct {
	Bus             *bus.MessageBus
	LLM             llm.LLM
	Model           string
	MaxIterations   int           // max tool call iterations per conversation
	MaxConcurrency  int           // max concurrent session processing count（默认 3）
	DBPath          string        // SQLite database path (uses default path if empty)
	SkillsDir       string        // Skills directory
	AgentsDir       string        // Agents 目录（空则使用 WorkDir/.xbot/agents）
	WorkDir         string        // working directory (all files relative to this directory)
	PromptFile      string        // system prompt template file path (uses built-in default if empty)
	SingleUser      bool          `json:"single_user"` // Deprecated: no longer used, kept for config file compatibility
	DirectWorkspace string        `json:"-"`           // when non-empty, directly used as workspaceRoot (CLI mode)
	SandboxMode     string        // sandbox mode: "none" or "docker" (default "docker")
	Sandbox         tools.Sandbox // Sandbox instance reference (added in V4)

	SandboxIdleTimeout time.Duration // sandbox idle timeout (0 to disable)

	MemoryProvider     string // memory provider: "flat" or "letta"
	EmbeddingProvider  string // embedding provider: "openai" (default) or "ollama"
	EmbeddingBaseURL   string // embedding vector service URL
	EmbeddingAPIKey    string // embedding vector service key
	EmbeddingModel     string // embedding model name
	EmbeddingMaxTokens int    // embedding model max token count

	// XbotHome is the global xbot config directory (e.g. ~/.xbot).
	// Used to locate global config files like mcp.json.
	XbotHome string

	// MCP Session managementConfiguration
	MCPInactivityTimeout time.Duration // MCP inactivity timeout
	MCPCleanupInterval   time.Duration // MCP cleanup scan interval
	SessionCacheTimeout  time.Duration // session cache timeout

	// context management mode
	// priority: ContextMode > EnableAutoCompress legacy field
	// default "", determined by resolveContextMode
	ContextMode ContextMode

	// Persona isolation: each web user has independent persona (no fallback to global)
	PersonaIsolation bool

	// legacy compression config (kept for initializing ContextManagerConfig, backward compatible with main.go params)
	MaxContextTokens     int     // max context token count (default 100000)
	CompressionThreshold float64 // token ratio threshold for triggering compression (default 0.7)
	EnableAutoCompress   bool    // whether to enable auto context compression (default true, legacy field)

	// DynamicMaxTokens dynamically adjusts max_output_tokens based on remaining
	// context space. When enabled, max_output_tokens is reduced when the context
	// is large to prevent context_window_exceeded errors.
	DynamicMaxTokens bool

	// SubAgent depth control
	MaxSubAgentDepth int // SubAgent max nesting depth (default 6)

	// clean up old messages after compression
	PurgeOldMessages bool // auto-delete old messages after compression (default false)

	// OffloadDir: offload file storage directory (default ~/.xbot/offload_store)
	OffloadDir string

	// MaskDir: mask file storage base directory (default ~/.xbot/mask/{tenantID})
	MaskDir string
}

// initStores initializes various stores and registries, returns skillStore, agentStore, chatHistory, registry, cardBuilder.

func initStores(cfg Config) (*SkillStore, *AgentStore, *tools.ChatHistoryStore, *tools.Registry, *tools.CardBuilder) {
	globalSkillDirs := resolveGlobalSkillsDirs(cfg.SkillsDir)

	skillStore := NewSkillStore(cfg.WorkDir, globalSkillDirs, cfg.Sandbox)

	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	agentsDir := cfg.AgentsDir
	if agentsDir == "" {
		agentsDir = filepath.Join(cfg.WorkDir, ".xbot", "agents")
	}
	if err := tools.InitAgentRoles(agentsDir); err != nil {
		log.WithError(err).Warn("Failed to load agent roles, SubAgent will have no predefined roles")
	}
	agentStore := NewAgentStore(cfg.WorkDir, agentsDir, cfg.Sandbox)

	// determine memory mode
	memoryProvider := cfg.MemoryProvider
	if memoryProvider == "" {
		memoryProvider = "flat"
	}

	registry := tools.DefaultRegistry(memoryProvider)

	// create chat history store
	chatHistory := tools.NewChatHistoryStore(200) // keep the latest 200 entries per group
	registry.Register(tools.NewChatHistoryTool(chatHistory))

	// MCP global config: use xbotHome directly (~/.xbot/mcp.json).
	// resolveDataPath would double-nest to ~/.xbot/.xbot/mcp.json.
	xbotHome := cfg.XbotHome
	if xbotHome == "" {
		xbotHome = cfg.WorkDir
	}
	mcpConfigPath := filepath.Join(xbotHome, "mcp.json")

	// register ManageTools tool (requires skillStore and mcpConfigPath)
	registry.RegisterCore(tools.NewManageTools(cfg.WorkDir, mcpConfigPath))

	cardBuilder := tools.NewCardBuilder()
	for _, t := range tools.NewCardTools(cardBuilder) {
		registry.Register(t)
	}

	// Clean up expired waiting cards from previous runs (TTL: 24h)
	if n := cardBuilder.CleanupExpiredWaitingCards(cardExpiryDuration); n > 0 {
		log.WithField("count", n).Info("Cleaned up expired waiting cards")
	}

	return skillStore, agentStore, chatHistory, registry, cardBuilder
}

// initSession initializes multi-tenant session manager.
func initSession(cfg Config) (*session.MultiTenantSession, error) {
	memoryProvider := cfg.MemoryProvider
	if memoryProvider == "" {
		memoryProvider = "flat"
	}
	multiSession, err := session.NewMultiTenant(
		cfg.DBPath,
		session.WithMCPTimeout(cfg.MCPInactivityTimeout),
		session.WithCleanupInterval(cfg.MCPCleanupInterval),
		session.WithSessionCacheTimeout(cfg.SessionCacheTimeout),
		session.WithMemoryProvider(memoryProvider),
		session.WithPersonaIsolation(cfg.PersonaIsolation),
		session.WithEmbeddingConfig(session.EmbeddingConfig{
			Provider:   cfg.EmbeddingProvider,
			BaseURL:    cfg.EmbeddingBaseURL,
			APIKey:     cfg.EmbeddingAPIKey,
			Model:      cfg.EmbeddingModel,
			MaxTokens:  cfg.EmbeddingMaxTokens,
			LLMClient:  cfg.LLM,
			LLMModel:   cfg.Model,
			TokenModel: cfg.Model,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("initialize multi-tenant session: %w", err)
	}
	return multiSession, nil
}

// initServices registers tools, initializes cron/LLM/offload/registry/settings services.
// This method directly modifies the Agent pointer.
func initServices(a *Agent, cfg Config, multiSession *session.MultiTenantSession, registry *tools.Registry) {
	// MCP config must use xbotHome directly (not resolveDataPath which double-nests).
	mcpConfigPath := filepath.Join(a.xbotHome, "mcp.json")
	contextMode := resolveContextMode(cfg)

	memoryProvider := cfg.MemoryProvider
	if memoryProvider == "" {
		memoryProvider = "flat"
	}

	multiSession.SetMCPConfigPath(mcpConfigPath)

	// set callback for session cleanup, synchronously clean up sessionActivated/sessionRound in Registry (C-09)
	registryRef := registry // capture for closure
	multiSession.SetOnSessionEvict(func(sessionKey string) { registryRef.DeactivateSession(sessionKey) })

	// set session MCP manager provider
	registry.SetSessionMCPManagerProvider(multiSession)

	// global tool index is called via IndexGlobalTools() after all tools are registered

	// if using Letta memory mode, register memory tools (core tools, always available)
	if memoryProvider == "letta" {
		for _, tool := range tools.LettaMemoryTools() {
			registry.RegisterCore(tool)
		}
		registry.RegisterCore(&tools.SearchToolsTool{})
		log.Info("Letta memory tools registered (core)")
	}

	// Flat mode: register flat memory tools (memory_read/write/list)
	if memoryProvider == "flat" || memoryProvider == "" {
		for _, tool := range tools.FlatMemoryTools() {
			registry.RegisterCore(tool)
		}
		log.Info("Flat memory tools registered (core)")
	}

	// project memory tools: registered for all providers (provider-agnostic)
	for _, tool := range tools.KnowledgeTools() {
		registry.RegisterCore(tool)
	}
	log.Info("Knowledge tools registered (core)")

	// Initialize command registry
	a.commands = NewCommandRegistry()
	registerBuiltinCommands(a.commands)

	// initialize Cron service and scheduler
	cronSvc := sqlite.NewCronService(multiSession.DB())
	cronSch := cron.NewScheduler(cronSvc)

	// migrate data from legacy JSON files (if needed)
	if err := cronSvc.MigrateFromJSON(cfg.WorkDir); err != nil {
		log.WithError(err).Warn("Failed to migrate cron jobs from JSON")
	}

	// register CronTool (core tool, always available)
	registry.RegisterCore(tools.NewCronTool(cronSvc))

	a.cronSvc = cronSvc
	a.cronSch = cronSch

	// Initialize UserLLMConfigService
	a.llmConfigSvc = sqlite.NewUserLLMConfigService(multiSession.DB())
	a.llmFactory = NewLLMFactory(a.llmConfigSvc, cfg.LLM, cfg.Model)
	a.llmFactory.SetSubscriptionSvc(sqlite.NewLLMSubscriptionService(multiSession.DB()))

	// initialize context manager
	a.contextManagerConfig = &ContextManagerConfig{
		MaxContextTokens:     cfg.MaxContextTokens,
		CompressionThreshold: cfg.CompressionThreshold,
		DefaultMode:          contextMode,
	}
	a.contextManager = NewContextManager(a.contextManagerConfig)

	// initialize OffloadStore (Phase 2: Layer 1 Offload)
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	offloadDir := cfg.OffloadDir
	if offloadDir == "" {
		offloadDir = filepath.Join(cfg.WorkDir, ".xbot", "offload_store")
	}
	a.offloadStore = NewOffloadStore(OffloadConfig{
		StoreDir:        offloadDir,
		MaxResultTokens: 2000,
		MaxResultBytes:  10240,
		CleanupAgeDays:  7,
	})
	go a.offloadStore.CleanStale()

	// Inject sandbox into OffloadStore for remote mode file hash computation
	if a.sandbox != nil {
		a.offloadStore.SetSandbox(a.sandbox)
	}

	// initialize ObservationMaskStore (Phase 3: Observation Masking)
	// disabled by default: enabled via settings enable_masking.
	// always created (needed for tool registration), but controlled at engine layer via RunConfig.MaskStore.
	// Disk storage goes to global ~/.xbot/mask/{tenantID}/, avoiding polluting the current working directory.
	maskDir := cfg.MaskDir
	if maskDir == "" {
		maskDir = filepath.Join(a.xbotHome, "mask")
	}
	a.maskStore = NewObservationMaskStore(200)
	a.maskStore.SetBaseDir(maskDir)
	go a.maskStore.CleanStale(7)

	// register offload_recall tool (requires OffloadStore dependency injection)
	if a.offloadStore != nil {
		recallTool := &tools.OffloadRecallTool{Store: a.offloadStore}
		registry.RegisterCore(recallTool)
	}

	// register recall_masked tool (requires MaskStore dependency injection)
	if a.maskStore != nil {
		registry.RegisterCore(&tools.RecallMaskedTool{Store: a.maskStore})
	}

	// initialize ContextEditor (Context Editing tool — precise context editing)
	editStore := NewContextEditStore(100)
	contextEditor := NewContextEditor(editStore)
	a.contextEditor = contextEditor
	// Wire up persistence callback for context edits (best-effort sync to DB).
	// IMPORTANT: PersistFn is called while ContextEditor.mu is held (write lock).
	// Do NOT acquire ContextEditor.mu inside PersistFn — deadlock!
	sessionSvc := sqlite.NewSessionService(multiSession.DB())
	contextEditor.PersistFn = func(editedIndices []int) {
		tenantID := contextEditor.tenantID
		if tenantID == 0 {
			return
		}
		// messages is safe to read here — caller (applyEdit/deleteTurn) holds the write lock
		msgs := contextEditor.messages
		if msgs == nil {
			return
		}
		for _, idx := range editedIndices {
			if idx < 0 || idx >= len(msgs) {
				continue
			}
			if err := sessionSvc.UpdateMessageContentNonDisplayOnly(tenantID, idx, msgs[idx].Content); err != nil {
				log.WithError(err).WithFields(log.Fields{
					"tenant_id": tenantID,
					"index":     idx,
				}).Warn("Failed to persist context edit to database")
			}
		}
	}
	registry.RegisterCore(&tools.ContextEditTool{Handler: contextEditor})

	// initialize and register TODO management tool
	todoMgr := tools.NewTodoManager()
	a.todoManager = todoMgr
	registry.RegisterCore(&tools.TodoWriteTool{Manager: todoMgr})
	registry.RegisterCore(&tools.TodoListTool{Manager: todoMgr})

	// Initialize SharedSkillRegistry
	sharedRegistry := sqlite.NewSharedSkillRegistry(multiSession.DB())

	// Initialize RegistryManager
	a.registryManager = NewRegistryManager(a.skills, a.agents, sharedRegistry, cfg.WorkDir, cfg.Sandbox)

	// Initialize UserSettingsService and SettingsService
	userSettingsSvc := sqlite.NewUserSettingsService(multiSession.DB())
	a.settingsSvc = NewSettingsService(userSettingsSvc)

	// Initialize LLMSemaphoreManager and inject dependencies
	llmSemMgr := llm.NewLLMSemaphoreManager()
	a.llmFactory.SetLLMSemaphoreManager(llmSemMgr)
	a.llmFactory.SetSettingsService(a.settingsSvc)

	// initialize message build pipeline (must be after settingsSvc, LanguageMiddleware depends on it)
	a.initPipelines(memoryProvider)
}

// New creates Agent
func New(cfg Config) (*Agent, error) {
	// 1. set configuration defaults
	if cfg.MaxIterations == 0 {
		cfg.MaxIterations = 2000
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 3
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = "."
	}
	if cfg.SkillsDir == "" {
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		cfg.SkillsDir = filepath.Join(cfg.WorkDir, ".xbot", "skills")
	}
	if cfg.DBPath == "" {
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		cfg.DBPath = filepath.Join(cfg.WorkDir, ".xbot", "xbot.db")
	}
	if cfg.MCPInactivityTimeout == 0 {
		cfg.MCPInactivityTimeout = 30 * time.Minute
	}
	if cfg.MCPCleanupInterval == 0 {
		cfg.MCPCleanupInterval = 5 * time.Minute
	}
	if cfg.SessionCacheTimeout == 0 {
		cfg.SessionCacheTimeout = 24 * time.Hour
	}
	if cfg.MaxContextTokens == 0 {
		cfg.MaxContextTokens = 100000 // default 100k tokens
	}
	if cfg.CompressionThreshold == 0 {
		cfg.CompressionThreshold = 0.7
	}
	if cfg.MaxSubAgentDepth <= 0 {
		cfg.MaxSubAgentDepth = 6
	}

	// 2. initialize stores and registries
	skillStore, agentStore, chatHistory, registry, cardBuilder := initStores(cfg)

	// 3. initialize session manager
	multiSession, err := initSession(cfg)
	if err != nil {
		return nil, fmt.Errorf("init session: %w", err)
	}

	// 4. build Agent instance
	sandboxMode := cfg.SandboxMode
	if sandboxMode == "" {
		sandboxMode = "docker"
	}

	agent := &Agent{
		bus:              cfg.Bus,
		multiSession:     multiSession,
		tools:            registry,
		maxIterations:    cfg.MaxIterations,
		maxConcurrency:   cfg.MaxConcurrency,
		purgeOldMessages: cfg.PurgeOldMessages,

		skills:             skillStore,
		agents:             agentStore,
		chatHistory:        chatHistory,
		cardBuilder:        cardBuilder,
		workDir:            cfg.WorkDir,
		promptLoader:       NewPromptLoader(cfg.PromptFile),
		sandboxMode:        sandboxMode,
		sandbox:            cfg.Sandbox,
		sandboxIdleTimeout: cfg.SandboxIdleTimeout,
		directWorkspace:    cfg.DirectWorkspace,
		globalSkillDirs:    resolveGlobalSkillsDirs(cfg.SkillsDir),
		maxSubAgentDepth:   cfg.MaxSubAgentDepth,
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		agentsDir: filepath.Join(cfg.WorkDir, ".xbot", "agents"),
		xbotHome:  cfg.XbotHome,
		// timingData and approvalState are created before hookManager so they
		// can be shared: the same instances are registered as builtins and
		// exposed via accessor methods.
		timingData:    hooks.NewTimingData(),
		approvalState: hooks.NewApprovalState(nil), // handler set later by channel when available
		hookManager: func() *hooks.Manager {
			mgr, err := hooks.NewManager(cfg.XbotHome, cfg.WorkDir)
			if err != nil {
				log.WithError(err).Warn("Failed to load hooks config, using empty manager")
				mgr, _ = hooks.NewManager(cfg.XbotHome, cfg.WorkDir)
			}
			return mgr
		}(),
		bgTaskMgr: tools.NewBackgroundTaskManager(),
	}

	// 5. initialize various services (modifies agent pointer)
	initServices(agent, cfg, multiSession, registry)

	// 5b. Register builtin hooks on the shared hookManager.
	// Uses the same timingData/approvalState instances stored on the Agent.
	agent.hookManager.RegisterBuiltin(hooks.LoggingCallback())
	agent.hookManager.RegisterBuiltin(hooks.TimingCallback(agent.timingData))
	agent.hookManager.RegisterBuiltin(hooks.ApprovalCallback(agent.approvalState))

	// 6. start bg task notification routing goroutine
	go agent.bgNotifyLoop()

	return agent, nil
}

// GetContextManager returns the current context manager (read lock protected).
// Used for buildMainRunConfig / buildSubAgentRunConfig / handleCompress etc.
func (a *Agent) GetContextManager() ContextManager {
	a.contextManagerMu.RLock()
	defer a.contextManagerMu.RUnlock()
	return a.contextManager
}

// SetContextManager replaces the current context manager (write lock protected).
// Used for runtime switching via /context mode command.
func (a *Agent) SetContextManager(cm ContextManager) {
	a.contextManagerMu.Lock()
	defer a.contextManagerMu.Unlock()
	a.contextManager = cm
}

// GetContextMode returns the current effective context mode.
func (a *Agent) GetContextMode() string {
	return string(a.contextManagerConfig.EffectiveMode())
}

// SetContextMode changes the runtime context mode and rebuilds the context manager.
// Pass "default" to reset to the default mode.
func (a *Agent) SetContextMode(mode string) error {
	cfg := a.contextManagerConfig
	target := ContextMode(mode)

	if target == "default" {
		cfg.ResetRuntimeMode()
		a.SetContextManager(NewContextManager(cfg))
		return nil
	}

	// "auto" is a user-facing alias for "phase1" (automatic compression)
	if target == "auto" {
		target = ContextModePhase1
	}

	if !IsValidContextMode(target) {
		return fmt.Errorf("invalid mode %q; valid: phase1, auto, none, default", mode)
	}

	cfg.SetRuntimeMode(target)
	a.SetContextManager(NewContextManager(cfg))
	return nil
}

func (a *Agent) SetMaxIterations(n int) {
	a.contextManagerMu.Lock()
	a.maxIterations = n
	a.contextManagerMu.Unlock()
}
func (a *Agent) SetMaxConcurrency(n int) {
	a.contextManagerMu.Lock()
	a.maxConcurrency = n
	a.contextManagerMu.Unlock()
	// Rebuild global semaphore with new capacity
	a.globalSemMu.Lock()
	a.globalSem = make(chan struct{}, n)
	a.globalSemMu.Unlock()
}
func (a *Agent) SetMaxContextTokens(n int) {
	a.contextManagerMu.Lock()
	a.contextManagerConfig.MaxContextTokens = n
	a.contextManagerMu.Unlock()
}

func (a *Agent) getMaxIterations() int {
	a.contextManagerMu.RLock()
	defer a.contextManagerMu.RUnlock()
	return a.maxIterations
}

func (a *Agent) getMaxConcurrency() int {
	a.contextManagerMu.RLock()
	defer a.contextManagerMu.RUnlock()
	if a.maxConcurrency < 1 {
		return 1
	}
	return a.maxConcurrency
}

// getGlobalSem returns the current global semaphore channel.
// Must be called each time a semaphore is needed (not cached) so that
// SetMaxConcurrency rebuilds take effect immediately.
func (a *Agent) getGlobalSem() chan struct{} {
	a.globalSemMu.Lock()
	defer a.globalSemMu.Unlock()
	return a.globalSem
}

// SetSandbox replaces the sandbox instance and mode at runtime (e.g. when user
// switches from docker to none in the settings panel).
func (a *Agent) SetSandbox(sb tools.Sandbox, mode string) {
	a.sandbox = sb
	a.sandboxMode = mode
	if a.offloadStore != nil {
		a.offloadStore.SetSandbox(sb)
	}
}

// GetUserLLMConfig returns the user's LLM config summary (no API key), or nil if none.
func (a *Agent) GetUserLLMConfig(senderID string) (provider, baseURL, model string, ok bool) {
	cfg, err := a.llmConfigSvc.GetConfig(senderID)
	if err != nil || cfg == nil || (cfg.BaseURL == "" && cfg.APIKey == "") {
		return "", "", "", false
	}
	return cfg.Provider, cfg.BaseURL, cfg.Model, true
}

// SetUserLLM creates or replaces a user's full LLM config.
func (a *Agent) SetUserLLM(senderID, provider, baseURL, apiKey, model string) error {
	if provider == "" || baseURL == "" || apiKey == "" {
		return fmt.Errorf("provider, base_url, api_key are required")
	}
	cfg := &sqlite.UserLLMConfig{
		SenderID: senderID,
		Provider: provider,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		Model:    model,
	}
	if err := a.llmConfigSvc.SetConfig(cfg); err != nil {
		return err
	}
	a.llmFactory.Invalidate(senderID)
	a.llmFactory.InvalidateCustomLLMCache(senderID)
	return nil
}

// DeleteUserLLM removes a user's LLM config and reverts to global.
func (a *Agent) DeleteUserLLM(senderID string) error {
	if err := a.llmConfigSvc.DeleteConfig(senderID); err != nil {
		return err
	}
	a.llmFactory.Invalidate(senderID)
	a.llmFactory.InvalidateCustomLLMCache(senderID)
	return nil
}

// GetLLMConcurrency gets the user's personal LLM concurrency limit configuration.
func (a *Agent) GetLLMConcurrency(senderID string) int {
	return a.llmFactory.GetLLMConcurrency(senderID)
}

// SetLLMConcurrency sets the user's personal LLM concurrency limit configuration.
func (a *Agent) SetLLMConcurrency(senderID string, personal int) error {
	return a.llmFactory.SetLLMConcurrency(senderID, personal)
}

// SetDirectSend injects a synchronous send function (bypasses bus, for message update tracking)
func (a *Agent) SetDirectSend(fn func(bus.OutboundMessage) (string, error)) {
	a.directSend = fn
}

// SetEventRouter sets the event trigger router.
// The router's InjectFunc is wired to injectEventMessage when Agent.Run starts.
func (a *Agent) SetEventRouter(r *event.Router) {
	a.eventRouter = r
}

// SetChannelPromptProviders sets channel-specific prompt providers.
// Rebuilds pipeline after call, inserting ChannelPromptMiddleware into the pipeline.
func (a *Agent) SetChannelPromptProviders(providers ...ChannelPromptProvider) {
	a.channelPromptProviders = providers
	a.pipeline.Use(NewChannelPromptMiddleware(providers...))
}

// HookManager returns the Agent's shared hook manager for tool execution.
// Callers can use this to register hooks, emit events, etc.
func (a *Agent) HookManager() *hooks.Manager {
	return a.hookManager
}

// TimingData returns the shared timing statistics collector.
func (a *Agent) TimingData() *hooks.TimingData { return a.timingData }

// ApprovalState returns the shared approval state for privileged operations.
func (a *Agent) ApprovalState() *hooks.ApprovalState { return a.approvalState }

// GetCardBuilder returns the CardBuilder for card callback handling.
func (a *Agent) GetCardBuilder() *tools.CardBuilder {
	return a.cardBuilder
}

// getUserSemaphore gets a user-independent semaphore for users with custom LLM configuration.
// Capacity matches maxConcurrency: allows parallel processing of different sessions from the same user,
// but total concurrency does not exceed the global limit.
// Uses LoadOrStore atomic operation to avoid concurrent creation of multiple semaphores.
func (a *Agent) getUserSemaphore(senderID string) chan struct{} {
	if val, ok := a.userSemaphores.Load(senderID); ok {
		return val.(chan struct{})
	}
	sem, _ := a.userSemaphores.LoadOrStore(senderID, make(chan struct{}, a.getMaxConcurrency()))
	return sem.(chan struct{})
}

// Close closes the Agent and all its resources
func (a *Agent) Close() error {
	// Cancel agent-level context to stop background subagents
	if a.agentCancel != nil {
		a.agentCancel()
	}
	// Stop cron scheduler first to avoid access attempts after database is closed
	if a.cronSch != nil {
		a.cronSch.Stop()
	}
	// Close NotifyCh to unblock bgNotifyLoop goroutine
	if a.bgTaskMgr != nil && a.bgTaskMgr.NotifyCh != nil {
		close(a.bgTaskMgr.NotifyCh)
	}
	// Then close database connections
	if a.multiSession != nil {
		if err := a.multiSession.Close(); err != nil {
			log.WithError(err).Warn("MultiTenantSession close error")
		}
	}
	return nil
}

// NOTE: math/rand is intentionally used here for non-cryptographic random selection
// (picking a casual ack message). Go 1.20+ automatically seeds math/rand on package
// init, so there is no security concern and no explicit seeding is required.
var ackMessages = []string{
	"收到~",
	"好的，让我看看",
	"收到，处理中...",
	"了解，稍等~",
	"好的~",
	"嗯嗯，马上处理",
	"收到，稍等一下~",
	"OK，马上看看",
}

func (a *Agent) sendAck(channel, chatID string) {
	msg := ackMessages[rand.Intn(len(ackMessages))]
	if err := a.sendMessage(channel, chatID, msg); err != nil {
		log.WithError(err).Warn("Failed to send ack")
	}
}

// Run starts the Agent loop, continuously consuming inbound messages.
// Messages are grouped by chat (channel:chatID), processed sequentially within the same chat, in parallel across different chats.
// Global concurrency is controlled by AGENT_MAX_CONCURRENCY (default 3) to avoid excessive LLM concurrency.
// After a user sets their own LLM configuration, that user's requests use an independent semaphore, no longer consuming global resources.
func (a *Agent) Run(ctx context.Context) error {
	log.WithFields(log.Fields{
		"max_concurrency": a.getMaxConcurrency(),
	}).Info("Agent loop started")

	a.multiSession.StartCleanupRoutine()

	a.cronSch.SetInjectFunc(a.injectInbound)
	// cronStartDelay is the delay before starting the cron scheduler after first agent run.
	const cronStartDelay = 3 * time.Second
	a.cronSch.StartDelayed(cronStartDelay)

	if a.eventRouter != nil {
		a.eventRouter.SetInjectFunc(a.injectEventMessage)
	}

	// Set up Agent-level context for background interactive subagents.
	// Bg subagents derive from this ctx (not per-request ctx) so they survive across requests.
	a.agentCtx, a.agentCancel = context.WithCancel(ctx)
	defer func() {
		a.agentCancel() // cancel all bg subagents when Agent exits
		a.cronSch.Stop()
		a.multiSession.StopCleanupRoutine()
	}()

	sem := make(chan struct{}, a.getMaxConcurrency())
	a.globalSemMu.Lock()
	a.globalSem = sem
	a.globalSemMu.Unlock()

	var mu sync.Mutex
	chatQueues := make(map[string]chan bus.InboundMessage)
	var wg sync.WaitGroup

	// getOrCreateQueue creates an independent message queue and worker for each chat
	// Semaphore is dynamically selected on each message processing (supports users setting/canceling custom LLM mid-session)
	getOrCreateQueue := func(key string) chan bus.InboundMessage {
		mu.Lock()
		defer mu.Unlock()
		if q, ok := chatQueues[key]; ok {
			return q
		}
		q := make(chan bus.InboundMessage, 32)
		chatQueues[key] = q

		wg.Go(func() {
			a.chatWorker(ctx, key, q)
			mu.Lock()
			delete(chatQueues, key)
			mu.Unlock()
		})
		return q
	}

	for {
		select {
		case <-ctx.Done():
			log.Info("Agent loop stopping, draining chat workers...")
			mu.Lock()
			for _, q := range chatQueues {
				close(q)
			}
			mu.Unlock()
			wg.Wait()
			log.Info("Agent loop stopped")
			return ctx.Err()
		case msg := <-a.bus.Inbound:

			// /cancel intercept: does not enter chatWorker queue, sends cancel signal directly
			if strings.TrimSpace(strings.ToLower(msg.Content)) == "/cancel" {
				cancelKey := msg.Channel + ":" + msg.ChatID + ":" + msg.SenderID
				log.WithField("cancel_key", cancelKey).Info("Received /cancel request")
				if ch, ok := a.chatCancelCh.Load(cancelKey); ok {
					select {
					case ch.(chan struct{}) <- struct{}{}:
						log.Info("Cancel signal sent to processing goroutine")
						_ = a.sendMessage(msg.Channel, msg.ChatID, "Request cancelled.")
					default:
						// cancel signal already sent
						log.WithField("cancel_key", cancelKey).Warn("Cancel signal already sent (buffer full)")
					}
				} else {
					log.WithField("cancel_key", cancelKey).Warn("No active request found for cancel")
					_ = a.sendMessage(msg.Channel, msg.ChatID, "No active request.")
				}
				continue
			}

			key := msg.Channel + ":" + msg.ChatID
			q := getOrCreateQueue(key)
			select {
			case q <- msg:
			default:
				log.WithFields(log.Fields{"request_id": msg.RequestID, "chat": key}).Warn("Chat queue full, dropping message")
			}
		}
	}
}

// workspaceRoot returns the workspace root for the given sender.
// If DirectWorkspace is set (e.g. CLI mode), returns it directly (no per-user subdirectory).
// Otherwise, returns per-user workspace directory.
func (a *Agent) workspaceRoot(senderID string) string {
	if a.directWorkspace != "" {
		return a.directWorkspace
	}
	return tools.UserWorkspaceRoot(a.workDir, senderID)
}

// isRemoteUser checks whether the given user routes to a remote sandbox.
// Uses SandboxResolver for per-user routing instead of checking Name() on the
// global SandboxRouter (which returns "router", not "remote").
func (a *Agent) isRemoteUser(userID string) bool {
	return a.sandboxNameForUser(userID) == "remote"
}

// sandboxNameForUser resolves the sandbox name for a given user.
func (a *Agent) sandboxNameForUser(userID string) string {
	if a.sandbox == nil {
		return ""
	}
	if resolver, ok := a.sandbox.(tools.SandboxResolver); ok {
		return resolver.SandboxForUser(userID).Name()
	}
	return a.sandbox.Name()
}

// remoteWorkspace returns the remote runner's workspace for the given user.
// Returns "" if the user is not on a remote sandbox or has no active connection.
//
// Deprecated: use sandboxWorkspace instead for all sandbox file operations.
func (a *Agent) remoteWorkspace(userID string) string {
	if a.sandbox == nil {
		return ""
	}
	if resolver, ok := a.sandbox.(tools.SandboxResolver); ok {
		return resolver.SandboxForUser(userID).Workspace(userID)
	}
	if a.sandbox.Name() == "remote" {
		return a.sandbox.Workspace(userID)
	}
	return ""
}

// sandboxWorkspace returns the correct workspace path for sandbox file operations.
// For docker mode: returns "/workspace" (the container-internal mount point).
// For remote mode: returns the runner's registered workspace.
// For none/local mode: returns the host-side user workspace root.
func (a *Agent) sandboxWorkspace(userID string) string {
	if a.sandbox == nil {
		return a.workspaceRoot(userID)
	}
	sb := a.sandbox
	if resolver, ok := sb.(tools.SandboxResolver); ok {
		sb = resolver.SandboxForUser(userID)
	}
	switch sb.Name() {
	case "docker":
		return sb.Workspace(userID) // "/workspace"
	case "remote":
		return sb.Workspace(userID) // runner's workspace
	default:
		return a.workspaceRoot(userID)
	}
}

// ensureWorkspace ensures the workspace directory exists (sandbox-aware).
// Skipped for remote, docker, and denied sandboxes — they manage their own filesystems
// or don't need host-side directories.
func (a *Agent) ensureWorkspace(ctx context.Context, dir, senderID string) error {
	name := a.sandboxNameForUser(senderID)
	if name == "remote" || name == "docker" || name == "denied" || name == "none" {
		return nil
	}
	if a.sandbox != nil {
		return a.sandbox.MkdirAll(ctx, dir, 0o755, senderID)
	}
	return os.MkdirAll(dir, 0o755)
}

// isGroupChat checks if it's a group chat
// Uses the message's ChatType field: p2p is private chat, group is group chat
func (a *Agent) isGroupChat(msg bus.InboundMessage) bool {
	return msg.ChatType == "group"
}

// getSemaphoreForMessage gets the semaphore the message should use
// Private chat: if user has custom LLM, use independent semaphore
// Group chat: always use global semaphore (because a group has multiple people, using independent semaphore would block other people's messages)
func (a *Agent) getSemaphoreForMessage(msg bus.InboundMessage) chan struct{} {
	globalSem := a.getGlobalSem()
	senderID := msg.SenderID
	if senderID == "" {
		return globalSem
	}

	// Group chat uses global semaphore
	if a.isGroupChat(msg) {
		return globalSem
	}

	// Private chat: check if user has custom LLM
	if a.llmFactory.HasCustomLLM(senderID) {
		return a.getUserSemaphore(senderID)
	}

	return globalSem
}

// chatWorker processes a single chat's message queue, guaranteeing sequential processing within the same chat.
// Concurrency controlled via semaphore: processing starts only after acquiring semaphore, released after completion.
// Semaphore is dynamically selected on each message processing to support users setting/canceling custom LLM mid-session.
// chatWorker processes a single chat's message queue.
// Main loop continuously takes messages from ch and dispatches:
//   - Command messages (/version, /help, etc.): executed immediately in independent goroutine, non-blocking
//   - Normal messages: sent to internal msgCh, processed serially by a dedicated goroutine (with semaphore + cancel)
//
// This way, even when a normal message is being processed for a long time (LLM inference), the main loop can still pick up and execute command messages.
func (a *Agent) chatWorker(ctx context.Context, chatKey string, ch <-chan bus.InboundMessage) {
	// Internal normal message queue: written by main loop, consumed by processLoop
	msgCh := make(chan bus.InboundMessage, 32)

	var wg sync.WaitGroup
	wg.Add(1)
	clipanic.Go("agent.chatWorker.processLoop", func() {
		defer wg.Done()
		a.chatProcessLoop(ctx, chatKey, msgCh)
	})

	for msg := range ch {
		if ctx.Err() != nil {
			break
		}

		// Command message dispatch: execution method determined by Concurrent()
		if cmd := a.commands.Match(msg.Content); cmd != nil {
			if cmd.Concurrent() {
				// Stateless commands: processed in independent goroutine, no semaphore, non-blocking
				m := msg
				c := cmd
				clipanic.Go("agent.chatWorker.concurrentCommand", func() {
					// Clear sessionFinalSent: commands don't go through processMessage,
					// need to manually clear otherwise sendMessage will be intercepted
					cmdKey := m.Channel + ":" + m.ChatID
					a.sessionMsgIDs.Delete(cmdKey)
					a.sessionFinalSent.Delete(cmdKey)

					response, err := c.Execute(ctx, a, m)
					if err != nil {
						log.WithFields(log.Fields{"request_id": m.RequestID, "chat": chatKey}).WithError(err).Error("Error processing command")
						content := formatErrorForUser(err)
						if sendErr := a.sendMessage(m.Channel, m.ChatID, content); sendErr != nil {
							a.bus.Outbound <- bus.OutboundMessage{
								Channel: m.Channel,
								ChatID:  m.ChatID,
								Content: content,
							}
						}
						return
					}
					if response != nil {
						if sendErr := a.sendMessage(m.Channel, m.ChatID, response.Content, response.Metadata); sendErr != nil {
							a.bus.Outbound <- *response
						}
					}
				})
			} else {
				// Stateful commands (/new, /compress, /set-llm, etc.): go through serial queue,
				// avoid session data races with currently processing normal messages
				select {
				case msgCh <- msg:
				case <-ctx.Done():
				}
			}
			continue
		}

		// Normal messages: forwarded to internal queue, processed serially by processLoop
		select {
		case msgCh <- msg:
		case <-ctx.Done():
		}
	}

	close(msgCh)
	wg.Wait()
}

// chatProcessLoop processes normal messages (non-command) serially, with semaphore control and per-request cancel support.
func (a *Agent) chatProcessLoop(ctx context.Context, chatKey string, ch <-chan bus.InboundMessage) {
	var idleTimer *time.Timer
	defer func() {
		if idleTimer != nil {
			idleTimer.Stop()
		}
	}()

	var lastSenderID string // record the last active senderID

	for msg := range ch {
		if ctx.Err() != nil {
			return
		}

		// stop the previous idle timer (new message received, reset timer)
		if idleTimer != nil {
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
		}

		sem := a.getSemaphoreForMessage(msg)

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}

		// create per-request cancel context
		var response *bus.OutboundMessage
		var err error
		cancelCh := make(chan struct{}, 1)
		cancelKey := msg.Channel + ":" + msg.ChatID + ":" + msg.SenderID
		a.chatCancelCh.Store(cancelKey, cancelCh)

		reqCtx, reqCancel := context.WithCancel(ctx)

		// listen for cancel signal
		clipanic.Go("agent.chatProcessLoop.cancelListener", func() {
			select {
			case <-cancelCh:
				reqCancel()
			case <-reqCtx.Done():
			}
		})

		// execute message processing, check if cancelled after completion
		// Note: must check before reqCancel() call, otherwise reqCtx.Err() always returns Canceled
		wasCancelled := false
		func() {
			defer func() {
				reqCancel()
				a.chatCancelCh.Delete(cancelKey)
				key := msg.Channel + ":" + msg.ChatID
				a.lastProgressSnapshot.Delete(key)
				a.iterationHistories.Delete(key)
				<-sem // release slot
			}()

			// When sandbox is doing export+import, reject all requests from that user
			sbUID := sandboxUserID(msg)
			if sb := tools.GetSandbox(); sb.IsExporting(sbUID) {
				log.WithFields(log.Fields{"request_id": msg.RequestID, "sender": msg.SenderID, "sandbox_user": sbUID}).Info("Request rejected: sandbox export in progress")
				a.sendMessage(msg.Channel, msg.ChatID, "⏳ 沙箱正在持久化中，请稍后再试...")
				return
			}

			response, err = a.processMessage(reqCtx, msg)
			// Check if cancelled before defer executes (user may /cancel during processMessage)
			if reqCtx.Err() == context.Canceled {
				wasCancelled = true
			}
		}()

		if wasCancelled && ctx.Err() == nil {
			// Request was cancelled by user /cancel (not global ctx close)
			log.WithFields(log.Fields{"request_id": msg.RequestID, "chat": chatKey}).Info("Request cancelled by user")
			// Even when cancelled, send response so CLI can clean up typing/progress state.
			if response != nil {
				_ = a.sendMessage(msg.Channel, msg.ChatID, response.Content, response.Metadata)
			} else {
				// No response generated yet (cancelled mid-tool-call) — send empty
				// message to signal turn end so CLI can clean up typing/progress state.
				_ = a.sendMessage(msg.Channel, msg.ChatID, "")
			}
			continue
		}

		if err != nil {
			log.WithFields(log.Fields{"request_id": msg.RequestID, "chat": chatKey}).WithError(err).Error("Error processing message")
			// Use the same path as normal reply via sendMessage: can Patch sent progress bar with error content, avoiding silent error delivery failure
			content := formatErrorForUser(err)
			if sendErr := a.sendMessage(msg.Channel, msg.ChatID, content); sendErr != nil {
				log.Ctx(ctx).WithError(sendErr).Warn("Failed to send error via sendMessage, fallback to bus")
				a.bus.Outbound <- bus.OutboundMessage{
					Channel: msg.Channel,
					ChatID:  msg.ChatID,
					Content: content,
				}
			}
			continue
		}
		if response != nil {
			if response.WaitingUser {
				// WaitingUser response: send directly with WaitingUser flag set.
				// Bypass sendMessage (which doesn't support WaitingUser) to avoid metadata hack.
				outMsg := bus.OutboundMessage{
					Channel:     msg.Channel,
					ChatID:      msg.ChatID,
					Content:     response.Content,
					WaitingUser: true,
					Metadata:    response.Metadata,
				}
				if outMsg.Metadata == nil {
					outMsg.Metadata = make(map[string]string)
				}
				select {
				case a.bus.Outbound <- outMsg:
				default:
					log.Ctx(ctx).Warn("Message bus outbound channel is full, dropping WaitingUser response")
				}
			} else if err := a.sendMessage(msg.Channel, msg.ChatID, response.Content, response.Metadata); err != nil {
				log.Ctx(ctx).WithError(err).Warn("Failed to dispatch response via sendMessage")
			}
		}

		// update the last active senderID
		lastSenderID = msg.SenderID

		// After processing, if idle timeout is enabled and user has docker sandbox, set timer
		// Remote sandbox connections should be persistent, no idle cleanup
		if a.sandboxIdleTimeout > 0 && lastSenderID != "" {
			// Skip idle cleanup for remote sandbox — the runner connection should be persistent
			if !a.isRemoteUser(lastSenderID) {
				idleTimer = time.AfterFunc(a.sandboxIdleTimeout, func() {
					if err := a.sandbox.CloseForUser(lastSenderID); err != nil {
						log.WithError(err).Warnf("Idle sandbox cleanup failed for user %s", lastSenderID)
					} else {
						log.Infof("Idle sandbox cleaned up for user %s (timeout: %s)", lastSenderID, a.sandboxIdleTimeout)
					}
				})
			}
		}
	}
}

// processMessage processes a single inbound message

func (a *Agent) processMessage(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	// Preprocess: request ID, user context, logging, media attachment
	ctx, msg = a.preprocessMessage(ctx, msg)

	// Cron messages use independent processing flow (no history context, no message update tracking)
	if msg.IsCron {
		return a.processCronMessage(ctx, msg)
	}

	// Initialize session message tracking and get/create tenant session
	tenantSession, err := a.initMessageSession(ctx, msg)
	if err != nil {
		return nil, err
	}

	// Command matching: dispatched uniformly via CommandRegistry
	if cmd := a.commands.Match(msg.Content); cmd != nil {
		log.Ctx(ctx).WithFields(log.Fields{
			"channel": msg.Channel,
			"command": cmd.Name(),
		}).Info("Command matched")
		return cmd.Execute(ctx, a, msg)
	}

	// Handle card responses (button clicks, form submissions)
	if msg.Metadata != nil && msg.Metadata["card_response"] == "true" {
		return a.handleCardResponse(ctx, msg, tenantSession)
	}

	preReplyNotify := bus.ShouldPreReplyNotify(msg.Metadata) && msg.Channel != "cli"
	replyPolicy := bus.InboundReplyPolicy(msg.Metadata)

	// Immediately send a random acknowledgment reply
	if preReplyNotify {
		a.sendAck(msg.Channel, msg.ChatID)
	}

	// Build LLM messages (inject long-term memory, skills)
	messages, err := a.buildPrompt(ctx, msg, tenantSession)
	if err != nil {
		return nil, err
	}

	// Handle AskUser reply: replace tool message content with user's answer
	messages, askUserAnswered := a.handleAskUserReply(ctx, msg, messages, tenantSession)

	// Eager-save user message before Run() so engine messages appear after it
	a.eagerSaveUserMsg(ctx, msg, askUserAnswered, tenantSession)

	// Build run config
	cfg := a.buildMainRunConfig(ctx, msg, messages, tenantSession, preReplyNotify)
	// Restore token count from last Run() to ensure maybeCompress can use accurate values
	if extras := cfg.ToolContextExtras; extras != nil && extras.MemorySvc != nil && extras.TenantID != 0 {
		if pt, ct, err := extras.MemorySvc.GetTokenState(ctx, extras.TenantID); err == nil && pt > 0 {
			cfg.LastPromptTokens = pt
			cfg.LastCompletionTokens = ct
		}
	}

	// Inject running background tasks / interactive agents / groups into last user message
	a.injectActiveContextNotes(ctx, msg, messages)

	// Wire drain callback for bg notifications (session-scoped filtering)
	a.wireBgNotificationDrain(&cfg, msg.Channel+":"+msg.ChatID)

	// Emit session events (start now, defer end)
	emitEnd := a.emitSessionStartEvent(ctx, msg, &cfg)
	defer emitEnd()

	// Mark Run as active so bgNotifyLoop buffers notifications
	atomic.StoreInt32(&a.bgRunActive, 1)
	out := Run(ctx, cfg)
	atomic.StoreInt32(&a.bgRunActive, 0)

	// Drain remaining bg notifications that arrived after Run's last iteration
	a.drainRemainingNotifications()

	// Handle Run errors (cancellation, general errors)
	if handled, outbound, err := a.handleRunError(ctx, msg, out, tenantSession); handled {
		return outbound, err
	}

	// Finalize output: persist assistant message, send reply, add reaction
	return a.finalizeRunOutput(ctx, msg, out, tenantSession, replyPolicy)
}

// preprocessMessage sets up request context (request ID, user ID), logs a preview,
// and appends media file references to the message content.
func (a *Agent) preprocessMessage(ctx context.Context, msg bus.InboundMessage) (context.Context, bus.InboundMessage) {
	// Use the requestID carried by the message (generated when channel receives the message), generate new one if absent
	reqID := msg.RequestID
	if reqID == "" {
		reqID = log.NewRequestID()
	}
	ctx = log.WithRequestID(ctx, reqID)

	// Inject senderID into context, for per-user human block (Letta mode)
	// Recall/Memorize gets userID via letta.GetUserID(ctx)
	ctx = letta.WithUserID(ctx, msg.SenderID)

	preview := msg.Content
	if r := []rune(preview); len(r) > 80 {
		preview = string(r[:80]) + "..."
	}
	log.Ctx(ctx).WithFields(log.Fields{
		"channel": msg.Channel,
		"sender":  msg.SenderID,
	}).Infof("Processing: %s", preview)

	// Attach media file references to message content
	if len(msg.Media) > 0 {
		var ref strings.Builder
		ref.WriteString("\n\n[Attached files]")
		for _, f := range msg.Media {
			ref.WriteString("\n- ")
			ref.WriteString(f)
		}
		msg.Content += ref.String()
	}

	return ctx, msg
}

// initMessageSession initializes session message tracking, creates or retrieves
// the tenant session, configures tenant-scoped stores, and caches the message
// to chat history.
func (a *Agent) initMessageSession(ctx context.Context, msg bus.InboundMessage) (*session.TenantSession, error) {
	// Initialize session message tracking: clear old sent message IDs, record inbound message ID for first reply
	key := msg.Channel + ":" + msg.ChatID
	a.sessionMsgIDs.Delete(key)
	a.sessionFinalSent.Delete(key)
	if msg.Metadata != nil && msg.Metadata["message_id"] != "" {
		a.sessionReplyTo.Store(key, msg.Metadata["message_id"])
	} else {
		a.sessionReplyTo.Delete(key)
	}

	// Get or create tenant session (senderID passed via context, not here)
	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return nil, fmt.Errorf("get/create tenant session: %w", err)
	}

	// Set tenant-scoped stores for this request
	tenantID := tenantSession.TenantID()
	if a.contextEditor != nil {
		a.contextEditor.SetTenantID(tenantID)
	}
	if a.maskStore != nil {
		a.maskStore.SetTenantID(tenantID)
	}

	// Cache message to chat history (for ChatHistory tool queries)
	a.chatHistory.Add(msg.Channel, msg.ChatID, msg.SenderID, msg.Content)
	log.Ctx(ctx).WithFields(log.Fields{
		"channel": msg.Channel,
		"chat_id": msg.ChatID,
		"sender":  msg.SenderID,
	}).Debug("Message cached to chat history")

	return tenantSession, nil
}

// handleAskUserReply processes AskUser answer messages by removing the appended
// user message and replacing the most recent AskUser tool message content with
// the user's answer. Returns the modified messages slice and whether this was
// an AskUser answer.
func (a *Agent) handleAskUserReply(ctx context.Context, msg bus.InboundMessage, messages []llm.ChatMessage, tenantSession *session.TenantSession) ([]llm.ChatMessage, bool) {
	// AskUser reply is not a new user message, but replaces the AskUser tool result.
	askUserAnswered := msg.Metadata != nil && msg.Metadata["ask_user_answered"] == "true"
	if !askUserAnswered {
		return messages, false
	}

	// Remove last user message appended by Assemble
	if len(messages) > 0 && messages[len(messages)-1].Role == "user" {
		messages = messages[:len(messages)-1]
	}
	// Replace the most recent AskUser tool message content with user's answer.
	foundAskUserTool := false
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "tool" {
			continue
		}
		if messages[i].ToolName != "AskUser" {
			continue
		}
		messages[i].Content = msg.Content
		foundAskUserTool = true
		break
	}
	if !foundAskUserTool {
		log.Ctx(ctx).Warn("AskUser answer received but no matching AskUser tool message found in prompt history")
	}
	// Also update the stale tool result in session so future buildPrompt reads correct content.
	if err := tenantSession.ReplaceToolMessage("AskUser", "", msg.Content); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to replace AskUser tool result in session")
	}

	return messages, true
}

// eagerSaveUserMsg saves the user message to the session before Run() executes,
// ensuring incrementally persisted assistant/tool messages appear after it in the DB.
func (a *Agent) eagerSaveUserMsg(ctx context.Context, msg bus.InboundMessage, askUserAnswered bool, tenantSession *session.TenantSession) {
	if askUserAnswered || (msg.Metadata != nil && msg.Metadata["user_msg_eager_saved"] == "true") {
		return
	}
	userMsg := llm.NewUserMessage(msg.Content)
	if !msg.Time.IsZero() {
		userMsg.Timestamp = msg.Time
	}
	if err := tenantSession.AddMessage(userMsg); err != nil {
		log.Ctx(ctx).WithError(err).WithFields(log.Fields{
			"channel": msg.Channel,
			"chat_id": msg.ChatID,
			"sender":  msg.SenderID,
			"content": msg.Content,
		}).Warn("Failed to eager-save user message")
	}
}

// injectActiveContextNotes appends system notes about running background tasks,
// active interactive agents, and active group chats to the last user message
// so the LLM is aware of current activity.
func (a *Agent) injectActiveContextNotes(ctx context.Context, msg bus.InboundMessage, messages []llm.ChatMessage) []llm.ChatMessage {
	var systemNotes []string

	// Background tasks
	if a.bgTaskMgr != nil {
		sessionKey := msg.Channel + ":" + msg.ChatID
		running := a.bgTaskMgr.ListRunning(sessionKey)
		if len(running) > 0 {
			var ids []string
			for _, t := range running {
				ids = append(ids, t.ID)
			}
			systemNotes = append(systemNotes, fmt.Sprintf("Running background tasks: %s", strings.Join(ids, ", ")))
		}
	}

	// Interactive agent sessions
	sessions := a.ListInteractiveSessions(msg.Channel, msg.ChatID)
	if len(sessions) > 0 {
		var agentParts []string
		for _, s := range sessions {
			status := "idle"
			if s.Running {
				status = "running"
			}
			mode := "fg"
			if s.Background {
				mode = "bg"
			}
			agentParts = append(agentParts, fmt.Sprintf("%s/%s(%s,%s)", s.Role, s.Instance, mode, status))
		}
		systemNotes = append(systemNotes, fmt.Sprintf("Active interactive agents: %s", strings.Join(agentParts, ", ")))
	}

	// Active group chats
	groups := tools.ListGroups()
	if len(groups) > 0 {
		var groupParts []string
		for _, g := range groups {
			status := "open"
			if g.Closed {
				status = "closed"
			}
			members := strings.Join(g.Members, ",")
			groupParts = append(groupParts, fmt.Sprintf("%s(%s, %d members: %s)", g.Name, status, len(g.Members), members))
		}
		systemNotes = append(systemNotes, fmt.Sprintf("Groups: %s", strings.Join(groupParts, "; ")))
	}

	if len(systemNotes) > 0 {
		info := "\n[System] " + strings.Join(systemNotes, " | ")
		// Append to a copy of the last user message to avoid mutating session data
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				m := messages[i] // shallow copy
				m.Content += info
				messages[i] = m
				break
			}
		}
	}

	return messages
}

// wireBgNotificationDrain configures the RunConfig's DrainBgNotifications callback
// to filter and return only notifications matching the given session key.
// Other sessions' notifications are put back into the pending list.
func (a *Agent) wireBgNotificationDrain(cfg *RunConfig, sessionKey string) {
	cfg.DrainBgNotifications = func() []tools.BgNotification {
		a.bgRunPendingMu.Lock()
		pending := a.bgRunPending
		a.bgRunPending = nil
		a.bgRunPendingMu.Unlock()
		var mine []tools.BgNotification
		var others []tools.BgNotification
		for _, n := range pending {
			if n.SessionKey() == sessionKey {
				mine = append(mine, n)
			} else {
				others = append(others, n)
			}
		}
		// Put other sessions' notifications back
		if len(others) > 0 {
			a.bgRunPendingMu.Lock()
			a.bgRunPending = append(a.bgRunPending, others...)
			a.bgRunPendingMu.Unlock()
		}
		return mine
	}
}

// emitSessionStartEvent emits the SessionStart hook event and returns a
// cleanup function that emits the SessionEnd event. The caller should
// defer the cleanup function.
func (a *Agent) emitSessionStartEvent(ctx context.Context, msg bus.InboundMessage, cfg *RunConfig) func() {
	if a.hookManager == nil {
		return func() {}
	}
	memoryProvider := ""
	if cfg.Memory != nil {
		memoryProvider = fmt.Sprintf("%T", cfg.Memory)
	}
	a.hookManager.Emit(ctx, &hooks.SessionStartEvent{
		BasePayload: hooks.BasePayload{
			SessionID: msg.ChatID, Channel: msg.Channel,
			SenderID: msg.SenderID, ChatID: msg.ChatID,
		},
		Source:         msg.Channel,
		Model:          cfg.Model,
		MemoryProvider: memoryProvider,
	})
	return func() {
		a.hookManager.Emit(ctx, &hooks.SessionEndEvent{
			BasePayload: hooks.BasePayload{
				SessionID: msg.ChatID, Channel: msg.Channel,
				SenderID: msg.SenderID, ChatID: msg.ChatID,
			},
			Source: msg.Channel,
		})
	}
}

// drainRemainingNotifications processes any background notifications that
// arrived after the Run loop's last iteration. Each notification is dispatched
// through the idle path.
func (a *Agent) drainRemainingNotifications() {
	a.bgRunPendingMu.Lock()
	remaining := a.bgRunPending
	a.bgRunPending = nil
	a.bgRunPendingMu.Unlock()
	for _, notif := range remaining {
		switch n := notif.(type) {
		case *tools.BackgroundTask:
			go a.processBgNotification(n)
		case *tools.SubAgentBgNotify:
			go a.processSubAgentBgNotification(n)
		}
	}
}

// handleRunError handles errors from the Run loop. For cancellation errors,
// it persists un-persisted engine messages and iteration history, then returns
// an outbound message with cancellation metadata. Returns (true, outbound, err)
// if the error was handled and the caller should return. Returns (false, nil, nil)
// if there was no error and the caller should continue processing.
func (a *Agent) handleRunError(ctx context.Context, msg bus.InboundMessage, out *RunOutput, tenantSession *session.TenantSession) (handled bool, outbound *bus.OutboundMessage, err error) {
	if out.Error == nil {
		return false, nil, nil
	}
	// When cancelled, save any un-persisted engine messages from the
	// interrupted iteration. User message and completed iterations are
	// already persisted (eager-save + incremental persistence).
	if errors.Is(out.Error, context.Canceled) {
		for _, em := range out.EngineMessages {
			if err := assertNoSystemPersist(em); err != nil {
				continue
			}
			if err := tenantSession.AddMessage(em); err != nil {
				log.Ctx(ctx).WithError(err).Warn("Failed to save engine message on cancel")
			}
		}
		if len(out.EngineMessages) > 0 {
			log.Ctx(ctx).Infof("Cancelled: persisted %d un-persisted engine messages", len(out.EngineMessages))
		}
		// Save iteration history as an assistant message with detail,
		// so web UI can restore it on page refresh without showing "loading".
		if len(out.IterationHistory) > 0 {
			cancelMsg := llm.NewAssistantMessage("")
			cancelMsg.DisplayOnly = true
			if jsonBytes, err := json.Marshal(out.IterationHistory); err == nil {
				cancelMsg.Detail = string(jsonBytes)
			}
			if err := tenantSession.AddMessage(cancelMsg); err != nil {
				log.Ctx(ctx).WithError(err).Warn("Failed to save cancelled iteration history")
			}
		}
		// Send a minimal outbound so the web channel knows processing ended.
		// Without this, web stays in "loading" state after cancel on refresh.
		meta := map[string]string{"cancelled": "true"}
		if len(out.IterationHistory) > 0 {
			if jsonBytes, err := json.Marshal(out.IterationHistory); err == nil {
				meta["progress_history"] = string(jsonBytes)
			}
		}
		return true, &bus.OutboundMessage{
			Channel:  msg.Channel,
			ChatID:   msg.ChatID,
			Content:  "",
			Metadata: meta,
		}, nil
	}
	return true, nil, out.Error
}

// finalizeRunOutput processes the successful output of the Run loop: handles
// WaitingUser state, empty content scenarios, persists the assistant message,
// sends the final reply, and adds the completion reaction.
func (a *Agent) finalizeRunOutput(ctx context.Context, msg bus.InboundMessage, out *RunOutput, tenantSession *session.TenantSession, replyPolicy string) (*bus.OutboundMessage, error) {
	finalContent := out.Content
	waitingUser := out.WaitingUser

	// If tool is waiting for user response, send WaitingUser outbound to let channel open interaction panel
	if waitingUser {
		log.Ctx(ctx).Info("Tool is waiting for user response, sending WaitingUser outbound")
		// User message and engine messages already persisted (eager-save + incremental).
		// Send the WaitingUser outbound so CLI can open the ask-user panel.
		// Content may be empty (no assistant reply yet), which is fine — the
		// panel reads the question from Metadata["ask_question"].
		meta := map[string]string{}
		for k, v := range out.Metadata {
			meta[k] = v
		}
		return &bus.OutboundMessage{
			Channel:     msg.Channel,
			ChatID:      msg.ChatID,
			Content:     finalContent,
			WaitingUser: true,
			Metadata:    meta,
		}, nil
	}

	// If final content is empty and not Optional reply strategy, send prompt to user
	if finalContent == "" && replyPolicy != bus.ReplyPolicyOptional {
		log.Ctx(ctx).Warn("Run produced empty content without waiting for user input")
		if err := a.sendMessage(msg.Channel, msg.ChatID, "⚠️ 处理完成，但未生成回复内容。请尝试重新描述您的需求。"); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to send empty content notification")
		}
		return nil, nil
	}

	if finalContent == "" && replyPolicy == bus.ReplyPolicyOptional {
		// User message already eager-saved before Run().
		log.Ctx(ctx).WithFields(log.Fields{
			"channel":      msg.Channel,
			"chat_id":      msg.ChatID,
			"reply_policy": replyPolicy,
		}).Info("Optional reply policy: no final response generated, skipping outbound")
		// Send an empty outbound to clear TUI progress state (typing/progress indicator).
		// Without this, TUI gets stuck showing progress with no way for user to interact.
		if ch, ok := a.channelFinder(msg.Channel); ok {
			ch.Send(bus.OutboundMessage{
				Channel: msg.Channel,
				ChatID:  msg.ChatID,
				Content: "",
			})
		}
		return nil, nil
	}

	// User message already eager-saved before Run(). Engine messages already
	// incrementally persisted. Only need to save the final assistant reply.
	assistantMsg := llm.NewAssistantMessage(finalContent)
	assistantMsg.ReasoningContent = out.ReasoningContent
	// Attach iteration history as JSON detail for UI display (not included in LLM context).
	if len(out.IterationHistory) > 0 {
		if jsonBytes, err := json.Marshal(out.IterationHistory); err == nil {
			assistantMsg.Detail = string(jsonBytes)
		}
	}
	if err := tenantSession.AddMessage(assistantMsg); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to save assistant message")
	}

	// Send final reply via sendMessage (reuse session-internal message update tracking)
	sendMeta := map[string]string{}
	if assistantMsg.Detail != "" {
		sendMeta["progress_history"] = assistantMsg.Detail
	}
	if err := a.sendMessage(msg.Channel, msg.ChatID, finalContent, sendMeta); err != nil {
		log.Ctx(ctx).WithError(err).Error("Failed to send final response via sendMessage")
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: finalContent,
		}, nil
	}

	// Add emoji reaction to user's original message to indicate processing complete
	a.addReaction(msg)

	return nil, nil
}

// processCronMessage processes cron-triggered messages (no history context, uses dedicated system prompt)
func (a *Agent) processCronMessage(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	// Inject requestID (if processMessage didn't inject it)
	if log.RequestID(ctx) == "" {
		ctx = log.WithRequestID(ctx, log.NewRequestID())
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"channel":   msg.Channel,
		"chat_id":   msg.ChatID,
		"sender_id": msg.SenderID,
	}).Infof("Processing cron message: %s", tools.Truncate(msg.Content, 80))

	// Clear old session state to ensure cron messages can be sent normally
	key := msg.Channel + ":" + msg.ChatID
	a.sessionMsgIDs.Delete(key)
	a.sessionFinalSent.Delete(key)

	// Use creator's workspace path
	senderID := msg.SenderID
	workspaceRoot := a.workspaceRoot(senderID)
	if err := a.ensureWorkspace(ctx, workspaceRoot, senderID); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to create cron user workspace")
	}

	// Build cron-specific messages (no history context)
	mc := NewCronMessageContext(msg.Content)
	messages := a.cronPipeline.Run(mc)

	// Run Agent loop (unified Run, cron doesn't need auto-compression and progress notifications)
	cronMsg := msg
	cronMsg.SenderID = senderID
	cronCfg := a.buildCronRunConfig(ctx, cronMsg, messages)
	cronOut := Run(ctx, cronCfg)
	if cronOut.Error != nil {
		return nil, cronOut.Error
	}
	finalContent := cronOut.Content

	if finalContent == "" {
		finalContent = "定时任务已执行，但无输出内容。"
	}

	// If tool has already sent final reply (e.g. card), skip subsequent text reply
	if _, sent := a.sessionFinalSent.Load(key); sent {
		log.Ctx(ctx).Info("Cron: tool already sent final reply (card), skipping text reply")
		a.persistCronMessages(ctx, msg, finalContent)
		return nil, nil
	}

	// Persist cron messages to session (visible to web users on next visit)
	a.persistCronMessages(ctx, msg, finalContent)

	// Keep original message ID to support reply mode
	metadata := make(map[string]string)
	if msg.Metadata != nil {
		metadata = msg.Metadata
	}

	return &bus.OutboundMessage{
		Channel:  msg.Channel,
		ChatID:   msg.ChatID,
		Content:  finalContent,
		Metadata: metadata,
	}, nil
}

// persistCronMessages persists cron messages to session, making them visible to web users on next visit.
// For non-web channels (e.g. Feishu), messages are already persisted via IM platform, no need for extra saving.
func (a *Agent) persistCronMessages(ctx context.Context, msg bus.InboundMessage, assistantContent string) {
	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to get session for cron message persistence")
		return
	}

	cronUserMsg := llm.NewUserMessage("[定时任务] " + msg.Content)
	cronUserMsg.Timestamp = msg.Time
	cronUserMsg.DisplayOnly = true
	if err := tenantSession.AddMessage(cronUserMsg); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to persist cron user message")
	}

	if assistantContent != "" {
		cronAssistantMsg := llm.NewAssistantMessage(assistantContent)
		cronAssistantMsg.DisplayOnly = true
		if err := tenantSession.AddMessage(cronAssistantMsg); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to persist cron assistant message")
		}
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"channel": msg.Channel,
		"chat_id": msg.ChatID,
	}).Debug("Cron messages persisted to session")
}

// buildPrompt builds the complete LLM message list (shared logic: called by both processMessage and handlePromptQuery).
// Uses the pipeline instance held by Agent, passing dynamic data via MessageContext.Extra.
func (a *Agent) buildPrompt(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) ([]llm.ChatMessage, error) {
	history, err := tenantSession.GetMessages()
	if err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to get history, using empty history")
		history = nil
	}
	// Fixup: strip trailing unpaired tool_calls left by a cancelled Run.
	// Both Anthropic and OpenAI APIs reject requests with unpaired tool_calls.
	history = llm.FixupTrailingToolCalls(history)
	sbUID := sandboxUserID(msg)
	workspaceRoot := a.workspaceRoot(sbUID)
	if err := a.ensureWorkspace(ctx, workspaceRoot, sbUID); err != nil {
		return nil, fmt.Errorf("create user workspace: %w", err)
	}
	newTools, err := a.multiSession.ConfigureSessionMCP(msg.Channel, msg.ChatID, msg.SenderID, a.workDir)
	if err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to configure session MCP scope")
	}
	if len(newTools) > 0 {
		sessionKey := msg.Channel + ":" + msg.ChatID
		a.tools.ActivateTools(sessionKey, newTools)
		log.Ctx(ctx).WithField("tools", len(newTools)).Info("Auto-activated new personal MCP tools")
	}

	promptWorkDir := a.workDir
	if a.sandboxMode == "docker" {
		promptWorkDir = "/workspace"
	} else if ws := a.remoteWorkspace(msg.SenderID); ws != "" {
		promptWorkDir = ws
	}

	mc := NewMessageContext(
		letta.WithUserID(ctx, msg.SenderID),
		msg.Content,
		history,
		msg.Channel,
		promptWorkDir,
		msg.SenderName,
		msg.SenderID,
		msg.ChatID,
	)

	// Inject current working directory (CWD) into prompt
	// In sandbox mode, CWD is already a sandbox-internal path, defaults to promptWorkDir when no cd
	mc.CWD = tenantSession.GetCurrentDir()
	if mc.CWD == "" {
		log.WithFields(log.Fields{
			"channel":      msg.Channel,
			"chat_id":      msg.ChatID,
			"fallback_dir": promptWorkDir,
		}).Debug("Session CWD empty, using promptWorkDir fallback")
		mc.CWD = promptWorkDir
	}

	mc.SetExtra(ExtraKeySkillsCatalog, a.skills.GetSkillsCatalog(ctx, msg.SenderID))
	mc.SetExtra(ExtraKeyAgentsCatalog, a.agents.GetAgentsCatalog(ctx, msg.SenderID))
	mc.SetExtra(ExtraKeyMemoryProvider, tenantSession.Memory())
	permUsers := a.settingsSvc.GetPermUsers(msg.Channel, msg.SenderID)
	mc.SetExtra(ExtraKeyPermUsers, permUsers)
	mc.Ctx = withPermControlEnabled(mc.Ctx, IsPermControlEnabled(permUsers))

	mc.SetExtra(ExtraKeyTenantID, tenantSession.TenantID())

	return a.pipeline.Run(mc), nil
}

// max returns the larger of a and b.

// summarizeRetryError simplifies LLM errors into user-friendly descriptions.
func summarizeRetryError(err error) string {
	if err == nil {
		return "unknown error"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "TLS handshake timeout"):
		return "network timeout"
	case strings.Contains(msg, "connection refused"):
		return "connection refused"
	case strings.Contains(msg, "429") || strings.Contains(msg, "rate limit"):
		return "rate limited"
	case strings.Contains(msg, "502") || strings.Contains(msg, "503"):
		return "service temporarily unavailable"
	case strings.Contains(msg, "500") || strings.Contains(msg, "504"):
		return "server error"
	default:
		var netErr net.Error
		if errors.As(err, &netErr) {
			if netErr.Timeout() {
				return "network timeout"
			}
			return "network error"
		}
		return "temporary error"
	}
}

// runLoop executes the Agent iteration loop (LLM -> tool call -> LLM ...)
// When autoNotify is true, cumulatively display model intermediate content and tool call status, updating the same message in real-time
// tenantSession is for persisting compression results after auto-compression (can pass nil)

// RegisterTool registers a tool to the agent's tool registry.
// This is useful for dynamically adding tools after agent creation.
func (a *Agent) RegisterTool(tool tools.Tool) {
	a.tools.Register(tool)
	log.WithField("tool", tool.Name()).Info("Tool registered")
}

func (a *Agent) RegisterCoreTool(tool tools.Tool) {
	a.tools.RegisterCore(tool)
	log.WithField("tool", tool.Name()).Info("Tool registered")
}

// First send creates a new message (replies to inbound message_id if present), subsequent sends Patch update the same message.
// When tools send final reply (e.g. Feishu card), also Patch updates, but marks session as "completed", subsequent calls auto-skip.
// sendMessage sends a message to IM channel.
// Via directSend direct connection or bus.Outbound broadcast.
func (a *Agent) sendMessage(channel, chatID, content string, metadata ...map[string]string) error {
	key := channel + ":" + chatID

	// Tool has sent final reply → skip all subsequent messages (progress updates, LLM final reply, etc.)
	if _, sent := a.sessionFinalSent.Load(key); sent {
		return nil
	}

	msg := bus.OutboundMessage{
		Channel: channel,
		ChatID:  chatID,
		Content: content,
	}
	if len(metadata) > 0 && metadata[0] != nil {
		msg.Metadata = metadata[0]
	}
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]string)
	}

	isFinal := strings.HasPrefix(content, "__FEISHU_CARD__:")

	if a.directSend != nil {
		if msg.Metadata == nil {
			msg.Metadata = make(map[string]string)
		}

		// Always include update_message_id for patch support.
		// For cards: feishu.go will attempt patch first; if cross-type conflict occurs,
		// it falls back to creating a new message and deleting the old progress message.
		if existingID, ok := a.sessionMsgIDs.Load(key); ok {
			if id, ok := existingID.(string); ok {
				msg.Metadata["update_message_id"] = id
			}
		}

		if replyTo, ok := a.sessionReplyTo.Load(key); ok {
			if id, ok := replyTo.(string); ok {
				msg.Metadata["message_id"] = id
			}
		}

		log.WithField("send_channel", msg.Channel).
			WithField("send_chat_id", msg.ChatID).
			WithField("orig_channel", channel).
			WithField("orig_chat_id", chatID).
			WithField("is_final", isFinal).
			Info("sendMessage directSend dispatch")
		msgID, err := a.directSend(msg)
		if err != nil {
			return err
		}
		if msgID != "" {
			a.sessionMsgIDs.Store(key, msgID)
		}
		if isFinal {
			a.sessionFinalSent.Store(key, true)
		}
		return nil
	}

	// Fallback: use bus when directSend unavailable (no message update tracking)
	select {
	case a.bus.Outbound <- msg:
		return nil
	default:
		return fmt.Errorf("message bus outbound channel is full")
	}
}

// injectInbound injects a message into the inbound queue, triggering the full Agent processing loop.
// For internal system messages such as cron scheduling and background task notifications.
func (a *Agent) injectInbound(channel, chatID, senderID, content string) {
	msg := bus.InboundMessage{
		Channel:   channel,
		SenderID:  senderID,
		ChatID:    chatID,
		Content:   content,
		Time:      time.Now(),
		IsCron:    false,
		RequestID: log.NewRequestID(),
	}
	select {
	case a.bus.Inbound <- msg:
	case <-a.agentCtx.Done():
	}
}

// injectEventMessage injects an event-triggered message into the inbound queue.
// Event Router routes external events (webhooks, etc.) to agent loop via this function,
// and sets EventSource/EventTrigger metadata.
func (a *Agent) injectEventMessage(msg event.Message) {
	inbound := bus.InboundMessage{
		Channel:      msg.Channel,
		SenderID:     msg.SenderID,
		ChatID:       msg.ChatID,
		Content:      msg.Content,
		Time:         time.Now(),
		IsCron:       false,
		RequestID:    log.NewRequestID(),
		EventSource:  msg.EventSource,
		EventTrigger: msg.EventTrigger,
	}
	select {
	case a.bus.Inbound <- inbound:
	case <-a.agentCtx.Done():
	}
}

// bgNotifyLoop routes background notifications from BgTaskManager.NotifyCh.
// When a Run is active (bgRunActive=1), notifications are buffered in bgRunPending
// for the Run loop to drain between iterations. When idle (bgRunActive=0),
// notifications go directly to the appropriate handler based on type.
func (a *Agent) bgNotifyLoop() {
	for notif := range a.bgTaskMgr.NotifyCh {
		if atomic.LoadInt32(&a.bgRunActive) == 1 {
			// Run is active — buffer for Run loop to drain
			a.bgRunPendingMu.Lock()
			a.bgRunPending = append(a.bgRunPending, notif)
			a.bgRunPendingMu.Unlock()
		} else {
			// Idle — process directly based on notification type
			switch n := notif.(type) {
			case *tools.BackgroundTask:
				go a.processBgNotification(n)
			case *tools.SubAgentBgNotify:
				go a.processSubAgentBgNotification(n)
			}
		}
	}
}

// processBgNotification handles a background task completion when no Run() is active.
// Injects the task result as a user message via injectInbound, triggering the standard
// processMessage → Assemble → Run pipeline. This matches Claude Code's behavior:
// bg task completion = environment notification = user message to the LLM.
func (a *Agent) processBgNotification(task *tools.BackgroundTask) {
	sessionKey := task.SessionKey()
	if sessionKey == "" {
		log.WithField("task_id", task.ID).Warn("Bg task notification: no session key, dropping")
		return
	}

	parts := strings.SplitN(sessionKey, ":", 2)
	if len(parts) != 2 {
		log.WithField("session_key", sessionKey).Warn("Bg task: invalid session key format")
		return
	}
	channelName, chatID := parts[0], parts[1]

	content := tools.FormatBgTaskCompletion(task)
	log.WithFields(log.Fields{
		"task_id": task.ID,
		"channel": channelName,
		"chat_id": chatID,
	}).Info("Bg task notification: injecting as user message")

	// Notify CLI to display the user message in the chat UI
	if a.channelFinder != nil {
		if ch, ok := a.channelFinder(channelName); ok {
			if cliCh, ok := ch.(*channel.CLIChannel); ok {
				cliCh.InjectUserMessage(content)
			}
		}
	}

	a.injectInbound(channelName, chatID, "user", content)
}

// processSubAgentBgNotification handles a bg subagent notification when no Run() is active.
// Only completion notifications trigger a new Run; progress notifications are dropped
// (they're only meaningful during an active Run, where they're injected as tool results).
func (a *Agent) processSubAgentBgNotification(n *tools.SubAgentBgNotify) {
	// During idle, only completion matters — progress would waste an LLM call
	if n.Type != tools.SubAgentBgNotifyCompleted {
		log.WithFields(log.Fields{
			"role":     n.Role,
			"instance": n.Instance,
			"type":     n.Type,
		}).Debug("Dropping bg subagent progress notification (agent idle)")
		return
	}

	parts := strings.SplitN(n.SessionKey(), ":", 2)
	if len(parts) != 2 {
		log.WithField("session_key", n.SessionKey()).Warn("Bg subagent notification: invalid session key")
		return
	}
	channelName, chatID := parts[0], parts[1]
	content := tools.FormatSubAgentBgNotify(n)

	log.WithFields(log.Fields{
		"role":     n.Role,
		"instance": n.Instance,
		"type":     n.Type,
		"channel":  channelName,
	}).Info("Bg subagent notification: injecting as user message")

	if a.channelFinder != nil {
		if ch, ok := a.channelFinder(channelName); ok {
			if cliCh, ok := ch.(*channel.CLIChannel); ok {
				cliCh.InjectUserMessage(content)
			}
		}
	}

	a.injectInbound(channelName, chatID, "user", content)
}

// buildBgNotificationRunConfig is no longer needed — idle bg notifications
// go through injectInbound → processMessage → buildMainRunConfig.

// RunSubAgent implements the tools.SubAgentManager interface
// Creates an independent sub-Agent loop to execute a task; the sub-Agent has its own tool set but cannot create further sub-Agents
// allowedTools is the tool whitelist; when empty, uses all tools (except SubAgent)
func (a *Agent) RunSubAgent(parentCtx *tools.ToolContext, task string, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, roleName, model string) (string, error) {
	cfg := a.buildSubAgentRunConfig(parentCtx.Ctx, parentCtx, task, systemPrompt, allowedTools, caps, roleName, false, model)
	out := Run(parentCtx.Ctx, cfg)
	if out.Error != nil {
		return out.Content, out.Error
	}
	return out.Content, nil
}

// addReaction adds emoji reaction to user message to indicate processing complete
func (a *Agent) addReaction(msg bus.InboundMessage) {
	if a.directSend == nil {
		return
	}
	messageID := ""
	if msg.Metadata != nil {
		messageID = msg.Metadata["message_id"]
	}
	if messageID == "" {
		return
	}

	_, err := a.directSend(bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Metadata: map[string]string{
			"add_reaction":        "DONE",
			"reaction_message_id": messageID,
		},
	})
	if err != nil {
		log.WithError(err).Debug("Failed to add reaction")
	}
}

// ProcessDirect processes a message directly (for CLI mode)
func (a *Agent) ProcessDirect(ctx context.Context, content string) (string, error) {
	msg := bus.InboundMessage{
		Channel:   "cli",
		SenderID:  "cli_user",
		ChatID:    "direct",
		Content:   content,
		Time:      time.Now(),
		RequestID: log.NewRequestID(),
	}
	resp, err := a.processMessage(ctx, msg)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}
	return resp.Content, nil
}

// formatToolProgress generates a human-readable one-line summary of a tool call for progress display.
// It parses the JSON args and extracts the most important parameter(s) based on the tool name.
// Output is concise, max ~80 chars total.
func formatToolProgress(name string, args string) string {
	const maxLen = 80

	// Helper to get a string field from parsed JSON
	get := func(m map[string]interface{}, key string) string {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
			// Handle numeric types (e.g., limit as float64 from JSON)
			return fmt.Sprintf("%v", v)
		}
		return ""
	}

	// Try to parse JSON args
	var m map[string]interface{}
	parsed := json.Unmarshal([]byte(args), &m) == nil

	// Helper to truncate and format the final result (rune-safe for multibyte chars)
	truncate := func(s string, max int) string {
		runes := []rune(s)
		if len(runes) <= max {
			return s
		}
		return string(runes[:max-3]) + "..."
	}

	if !parsed {
		log.WithField("tool", name).WithField("raw_args", truncate(args, 200)).Debug("formatToolProgress: failed to parse tool args as JSON")
	}

	// Letta memory tools
	switch name {
	case "core_memory_append":
		block := get(m, "block")
		return truncate(fmt.Sprintf("core_memory_append: %s", block), maxLen)
	case "core_memory_replace":
		block := get(m, "block")
		return truncate(fmt.Sprintf("core_memory_replace: %s", block), maxLen)
	case "rethink":
		block := get(m, "block")
		return truncate(fmt.Sprintf("rethink: %s", block), maxLen)
	case "archival_memory_insert":
		return "archival_memory_insert"
	case "archival_memory_search":
		query := get(m, "query")
		return truncate(fmt.Sprintf("archival_memory_search: %q", query), maxLen)
	case "recall_memory_search":
		query := get(m, "query")
		startDate := get(m, "start_date")
		endDate := get(m, "end_date")
		parts := []string{}
		if query != "" {
			parts = append(parts, fmt.Sprintf("%q", query))
		}
		if startDate != "" || endDate != "" {
			parts = append(parts, fmt.Sprintf("%s~%s", startDate, endDate))
		}
		return truncate(fmt.Sprintf("recall_memory_search: %s", strings.Join(parts, " ")), maxLen)
	}

	if !parsed {
		// JSON parsing failed: show truncated raw args
		raw := truncate(args, maxLen-len(name)-2)
		if raw == "" {
			return name
		}
		return truncate(fmt.Sprintf("%s: %s", name, raw), maxLen)
	}

	var summary string
	switch name {
	case "Shell":
		summary = fmt.Sprintf("Shell: %s", get(m, "command"))
	case "Read":
		summary = fmt.Sprintf("Read: %s", get(m, "path"))
	case "FileCreate":
		summary = fmt.Sprintf("FileCreate: %s", get(m, "path"))
	case "FileReplace":
		summary = fmt.Sprintf("FileReplace: %s", get(m, "path"))
	case "Grep":
		pattern := get(m, "pattern")
		path := get(m, "path")
		include := get(m, "include")
		target := path
		if include != "" {
			if target != "" {
				target = include + " in " + target
			} else {
				target = include
			}
		}
		if target != "" {
			summary = fmt.Sprintf("Grep: %q in %s", pattern, target)
		} else {
			summary = fmt.Sprintf("Grep: %q", pattern)
		}
	case "Glob":
		summary = fmt.Sprintf("Glob: %s", get(m, "pattern"))
	case "WebSearch":
		summary = fmt.Sprintf("WebSearch: %q", get(m, "query"))
	case "Cron":
		summary = fmt.Sprintf("Cron: %s", get(m, "action"))
	case toolSubAgent:
		role := get(m, "role")
		task := get(m, "task")
		if role != "" {
			summary = truncate(fmt.Sprintf("SubAgent [%s]: %s", role, task), maxLen)
		} else {
			summary = fmt.Sprintf("SubAgent: %s", task)
		}
	case "DownloadFile":
		summary = fmt.Sprintf("DownloadFile: %s", get(m, "output_path"))
	case "ChatHistory":
		limit := get(m, "limit")
		if limit != "" {
			summary = fmt.Sprintf("ChatHistory: limit=%s", limit)
		} else {
			summary = "ChatHistory"
		}
	case "ManageTools":
		action := get(m, "action")
		mName := get(m, "name")
		if mName != "" {
			summary = fmt.Sprintf("ManageTools: %s %s", action, mName)
		} else {
			summary = fmt.Sprintf("ManageTools: %s", action)
		}
	case "card_create":
		title := get(m, "title")
		if title != "" {
			summary = fmt.Sprintf("card_create: %q", title)
		} else {
			summary = "card_create"
		}
	default:
		// Unknown tools (including MCP tools): show first 60 chars of args
		raw := truncate(args, 60)
		summary = fmt.Sprintf("%s: %s", name, raw)
	}

	// Remove newlines to prevent broken quote blocks (tool arguments may contain multi-line content)
	summary = strings.NewReplacer("\n", " ", "\r", "").Replace(summary)
	return truncate(summary, maxLen)
}

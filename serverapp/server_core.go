package serverapp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	llm_pkg "xbot/llm"
	log "xbot/logger"
	"xbot/tools"
)

// ServerCoreOpts holds all parameters for creating a ServerCore.
type ServerCoreOpts struct {
	Config           *config.Config
	LLM              llm_pkg.LLM
	DBPath           string
	WorkDir          string
	XbotHome         string
	PersonaIsolation bool
}

// ServerCore is the shared core for both local and remote server modes.
// It owns the Agent, Bus, Dispatcher, and RPCTable.
type ServerCore struct {
	Agent    *agent.Agent
	Config   *config.Config
	Disp     *channel.Dispatcher
	MsgBus   *bus.MessageBus
	RPCTable RPCTable

	// channelReconfigureFn is called after a channel config change.
	channelReconfigureFn func(channel string)
}

// NewServerCore creates and initializes a ServerCore.
//
// It performs the following steps:
//  1. Creates the Agent from Config
//  2. Creates the Dispatcher
//  3. Creates the RPCTable
//  4. Registers core tools (DownloadFile, WebSearch)
//  5. Configures LLM (tiers, contexts, retry)
//
// It does NOT handle:
//   - HTTP listening, channel registration (Feishu/Web/QQ)
//   - Database migration
//   - OAuth setup
//   - Subscription migration from config.json to DB
//   - Settings sync from DB
//   - Runner token DB
func NewServerCore(opts ServerCoreOpts) (*ServerCore, error) {
	cfg := opts.Config

	// 1. Create MessageBus and Dispatcher.
	msgBus := bus.NewMessageBus()
	disp := channel.NewDispatcher(msgBus)

	// 2. Create Agent.
	// Embedding fallback: use LLM endpoint if embedding not configured.
	embBaseURL := cfg.Embedding.BaseURL
	if embBaseURL == "" {
		embBaseURL = cfg.LLM.BaseURL
	}
	embAPIKey := cfg.Embedding.APIKey
	if embAPIKey == "" {
		embAPIKey = cfg.LLM.APIKey
	}

	offloadDir := filepath.Join(opts.XbotHome, "offload_store")
	maskDir := filepath.Join(opts.XbotHome, "mask")

	ag, err := agent.New(agent.Config{
		Bus:                   msgBus,
		LLM:                   opts.LLM,
		Model:                 cfg.LLM.Model,
		MaxIterations:         cfg.Agent.MaxIterations,
		MaxConcurrency:        cfg.Agent.MaxConcurrency,
		DBPath:                opts.DBPath,
		SkillsDir:             filepath.Join(opts.XbotHome, "skills"),
		AgentsDir:             filepath.Join(opts.XbotHome, "agents"),
		WorkDir:               opts.WorkDir,
		XbotHome:              opts.XbotHome,
		PromptFile:            cfg.Agent.PromptFile,
		SandboxMode:           cfg.Sandbox.Mode,
		Sandbox:               tools.GetSandbox(),
		MemoryProvider:        cfg.Agent.MemoryProvider,
		EmbeddingProvider:     cfg.Embedding.Provider,
		EmbeddingBaseURL:      embBaseURL,
		EmbeddingAPIKey:       embAPIKey,
		EmbeddingModel:        cfg.Embedding.Model,
		EmbeddingMaxTokens:    cfg.Embedding.MaxTokens,
		MCPInactivityTimeout:  time.Duration(cfg.Agent.MCPInactivityTimeout),
		MCPCleanupInterval:    time.Duration(cfg.Agent.MCPCleanupInterval),
		SessionCacheTimeout:   time.Duration(cfg.Agent.SessionCacheTimeout),
		EnableAutoCompress:    cfg.Agent.EffectiveEnableAutoCompress(),
		MaxContextTokens:      cfg.Agent.MaxContextTokens,
		CompressionThreshold:  cfg.Agent.CompressionThreshold,
		ContextMode:           agent.ContextMode(cfg.Agent.ContextMode),
		MaxSubAgentDepth:      cfg.Agent.MaxSubAgentDepth,
		PurgeOldMessages:      cfg.Agent.PurgeOldMessages,
		SandboxIdleTimeout:    time.Duration(cfg.Sandbox.IdleTimeout),
		PersonaIsolation:      opts.PersonaIsolation,
		OffloadDir:            offloadDir,
		MaskDir:               maskDir,
		PluginEnabled:         cfg.Plugins.Enabled,
		PluginDirs:            cfg.Plugins.Dirs,
		PluginDisabledPlugins: cfg.Plugins.DisabledPlugins,
	})
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}

	// 3. Create RPCTable.
	rpcTable := BuildRPCTable(cfg, ag, disp, msgBus)

	// 4. Register core tools.
	ag.RegisterCoreTool(tools.NewDownloadFileTool(cfg.Feishu.AppID, cfg.Feishu.AppSecret))
	ag.RegisterTool(tools.NewDownloadFileTool(cfg.Feishu.AppID, cfg.Feishu.AppSecret))
	ag.RegisterCoreTool(tools.NewWebSearchTool(cfg.TavilyAPIKey))

	// Register Logs tool (admin only).
	if adminChatID := cfg.Admin.ChatID; adminChatID != "" {
		logsTool := tools.NewLogsTool(adminChatID)
		ag.RegisterCoreTool(logsTool)
		log.WithField("admin_chat_id", adminChatID).Info("Logs tool registered (admin only)")
	}

	// Index all registered tools so the agent knows about them.
	ag.IndexGlobalTools()

	// 5. Configure LLM.
	ag.LLMFactory().SetModelTiers(cfg.LLM)
	ag.LLMFactory().SetModelContexts(cfg.Agent.ModelContexts)
	ag.LLMFactory().SetRetryConfig(llm_pkg.RetryConfig{
		Attempts: uint(cfg.Agent.LLMRetryAttempts),
		Delay:    time.Duration(cfg.Agent.LLMRetryDelay),
		MaxDelay: time.Duration(cfg.Agent.LLMRetryMaxDelay),
		Timeout:  time.Duration(cfg.Agent.LLMRetryTimeout),
	})

	// 6. WireCallbacks and SetChatRenameFn are set by the caller
	// (CLI main.go or serverapp/server.go), not here.
	// ServerCore only creates the Agent + RPCTable + Dispatcher.

	return &ServerCore{
		Agent:    ag,
		Config:   cfg,
		Disp:     disp,
		MsgBus:   msgBus,
		RPCTable: rpcTable,
	}, nil
}

// HandleRPC dispatches an RPC request to the RPCTable.
// This is the entry point for in-process Transport (ChannelTransport).
func (sc *ServerCore) HandleRPC(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
	return sc.RPCTable.Dispatch(ctx, method, payload)
}

// SetChannelReconfigureFn sets the callback for dynamic channel reconfiguration.
func (sc *ServerCore) SetChannelReconfigureFn(fn func(string)) {
	sc.channelReconfigureFn = fn
}

// RegisterCoreTool registers a core tool on the agent.
func (sc *ServerCore) RegisterCoreTool(tool tools.Tool) {
	sc.Agent.RegisterCoreTool(tool)
}

// RegisterTool registers a tool on the agent.
func (sc *ServerCore) RegisterTool(tool tools.Tool) {
	sc.Agent.RegisterTool(tool)
}

// IndexGlobalTools indexes all registered tools.
func (sc *ServerCore) IndexGlobalTools() {
	sc.Agent.IndexGlobalTools()
}

// Run starts the agent loop and blocks until the context is done.
func (sc *ServerCore) Run(ctx context.Context) error {
	return sc.Agent.Run(ctx)
}

// Close releases agent resources.
func (sc *ServerCore) Close() error {
	return sc.Agent.Close()
}

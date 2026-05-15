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
	"xbot/protocol"
	"xbot/tools"
)

// InitServer creates the core server components:
// RPCTable, Dispatcher, and MessageBus.
//
// It creates the Agent internally, wires its callbacks,
// and starts the agent loop. The caller never touches the Agent directly.
//
// Both CLI local mode and server remote mode call this.
// The caller is responsible for:
//   - Registering the CLI/WS channel with the returned Dispatcher
//   - Calling SetSessionStateHandler after the channel is created
//   - Calling SetChatRenameFn if needed (local CLI mode)
//   - HTTP/WS listening (server mode only)
func InitServer(cfg *config.Config, llmClient llm_pkg.LLM, dbPath, workDir, xbotHome string, personaIsolation bool, reconfigureFn func(string)) (
	ag *agent.Agent, rpcTable RPCTable, disp *channel.Dispatcher, msgBus *bus.MessageBus, err error) {

	// 1. Create MessageBus and Dispatcher.
	msgBus = bus.NewMessageBus()
	disp = channel.NewDispatcher(msgBus)

	// 2. Create Agent.
	embBaseURL := cfg.Embedding.BaseURL
	if embBaseURL == "" {
		embBaseURL = cfg.LLM.BaseURL
	}
	embAPIKey := cfg.Embedding.APIKey
	if embAPIKey == "" {
		embAPIKey = cfg.LLM.APIKey
	}

	offloadDir := filepath.Join(xbotHome, "offload_store")
	maskDir := filepath.Join(xbotHome, "mask")

	ag, err = agent.New(agent.Config{
		Bus:                   msgBus,
		LLM:                   llmClient,
		Model:                 cfg.LLM.Model,
		MaxIterations:         cfg.Agent.MaxIterations,
		MaxConcurrency:        cfg.Agent.MaxConcurrency,
		DBPath:                dbPath,
		SkillsDir:             filepath.Join(xbotHome, "skills"),
		AgentsDir:             filepath.Join(xbotHome, "agents"),
		WorkDir:               workDir,
		XbotHome:              xbotHome,
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
		PersonaIsolation:      personaIsolation,
		OffloadDir:            offloadDir,
		MaskDir:               maskDir,
		PluginEnabled:         cfg.Plugins.Enabled,
		PluginDirs:            cfg.Plugins.Dirs,
		PluginDisabledPlugins: cfg.Plugins.DisabledPlugins,
	})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("create agent: %w", err)
	}

	// 3. Create RPCTable.
	rpcTable = BuildRPCTable(cfg, ag, disp, msgBus, reconfigureFn)

	// 4. Register core tools.
	ag.RegisterCoreTool(tools.NewDownloadFileTool(cfg.Feishu.AppID, cfg.Feishu.AppSecret))
	ag.RegisterTool(tools.NewDownloadFileTool(cfg.Feishu.AppID, cfg.Feishu.AppSecret))
	ag.RegisterCoreTool(tools.NewWebSearchTool(cfg.TavilyAPIKey))

	if adminChatID := cfg.Admin.ChatID; adminChatID != "" {
		ag.RegisterCoreTool(tools.NewLogsTool(adminChatID))
		log.WithField("admin_chat_id", adminChatID).Info("Logs tool registered (admin only)")
	}

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

	// 6. Wire agent callbacks (sessionStateHandler set later via SetSessionStateHandler).
	ag.WireCallbacks(
		func(msg bus.OutboundMessage) (string, error) { // directSend
			return disp.SendDirect(msg)
		},
		disp.GetChannel, // channelFinder
		nil,             // sessionStateHandler — set later via SetSessionStateHandler
		disp,            // messageSender
		func(name string, runFn bus.RunFn) error { // registerAgentChannel
			ac := channel.NewAgentChannel(name, runFn)
			if err := ac.Start(); err != nil {
				return fmt.Errorf("start AgentChannel %s: %w", name, err)
			}
			disp.Register(ac)
			return nil
		},
		func(name string) { disp.Unregister(name) }, // unregisterAgentChannel
	)

	// 7. Start agent loop.
	// The agent blocks on msgBus.Inbound until a message arrives,
	// so it's safe to start before sessionStateHandler is set —
	// the handler is only called when processing outbound events,
	// which happens after the first user message.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer cancel()
		if err := ag.Run(ctx); err != nil && ctx.Err() == nil {
			log.WithError(err).Error("Agent loop exited with error")
		}
	}()
	log.Info("Agent loop started")

	return ag, rpcTable, disp, msgBus, nil
}

// SetSessionStateHandler is a convenience function that calls
// ag.SetSessionStateHandler. CLI uses this after creating cliCh.
func SetSessionStateHandler(ag *agent.Agent, fn func(ev protocol.SessionEvent)) {
	ag.SetSessionStateHandler(fn)
}

// SetChatRenameFn is a convenience function that calls
// ag.SetChatRenameFn. CLI uses this after creating the DB.
func SetChatRenameFn(ag *agent.Agent, fn func(chatID, newName string) (oldName string, err error)) {
	ag.SetChatRenameFn(fn)
}

// DispatchRPC dispatches an RPC request to the given RPCTable.
// Used by InProcessTransport in local mode.
func DispatchRPC(table RPCTable) func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
	return table.Dispatch
}

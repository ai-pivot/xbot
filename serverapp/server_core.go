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
	"xbot/storage"
	"xbot/tools"
)

// InitServer creates the core server components and starts the agent loop.
// It returns the RPCTable, Dispatcher, and MessageBus.
// The Agent is created and managed internally — callers never access it directly.
//
// Both CLI local mode and server remote mode call this.
// When eventCh is non-nil (local CLI mode), a localEventBridge is created and
// registered with the Dispatcher so that server→CLI events flow through eventCh.
// When eventCh is nil (server mode), no bridge is created; server.go registers
// its own RemoteCLIChannel later.
//
// The Dispatcher's Run() goroutine is started inside InitServer for all modes.
func InitServer(cfg *config.Config, llmClient llm_pkg.LLM, dbPath, workDir, xbotHome string, personaIsolation bool, reconfigureFn func(string), eventCh chan protocol.WSMessage) (
	ag *agent.Agent, rpcTable RPCTable, disp *channel.Dispatcher, msgBus *bus.MessageBus, err error) {

	// 1. Create MessageBus and Dispatcher.
	msgBus = bus.NewMessageBus()
	disp = channel.NewDispatcher(msgBus)

	// 2. Register localEventBridge when eventCh is provided (local CLI mode).
	if eventCh != nil {
		bridge := &localEventBridge{eventCh: eventCh}
		disp.Register(bridge)
	}

	// 1b. Migrate data to SQLite if needed (one-time migration from old formats).
	// Must run BEFORE agent.New (which opens the DB via session.NewMultiTenant).
	if err := storage.MigrateIfNeeded(context.Background(), workDir, dbPath); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("migrate data: %w", err)
	}

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

	// 2c. Migrate flat memory from SQLite tables to MD files (if needed).
	// This is a one-time migration; must run after agent opens the DB (via
	// session.NewMultiTenant) but before any session access.
	storage.MigrateMemoryToFiles(dbPath)

	// 2c. Set runner token DB for per-user runner token persistence.
	// The Agent opens the DB internally via session.NewMultiTenant(cfg.DBPath);
	// we retrieve the *sql.DB from MultiSession to avoid a second sqlite.Open.
	if db := ag.MultiSession().DB(); db != nil {
		tools.SetRunnerTokenDB(db.Conn())
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

	// 6. Wire agent callbacks.
	ag.WireCallbacks(
		func(msg bus.OutboundMessage) (string, error) { // directSend
			return disp.SendDirect(msg)
		},
		disp.GetChannel, // channelFinder
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
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer cancel()
		if err := ag.Run(ctx); err != nil && ctx.Err() == nil {
			log.WithError(err).Error("Agent loop exited with error")
		}
	}()

	// 8. Start dispatcher goroutine (reads from msgBus.Outbound and
	// dispatches to registered channels — including localEventBridge in local mode).
	go disp.Run()

	return ag, rpcTable, disp, msgBus, nil
}

// DispatchRPC dispatches an RPC request to the given RPCTable.
// Used by ChannelTransport in local mode.
func DispatchRPC(table RPCTable) func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
	return table.Dispatch
}

// ---------------------------------------------------------------------------
// localEventBridge — bridges server-side Dispatcher to CLI's eventCh
// ---------------------------------------------------------------------------

// localEventBridge implements channel.Channel. It is registered with the
// Dispatcher so that all outbound messages for the "cli" channel are forwarded
// as protocol.WSMessage to eventCh, which the Client reads in its eventLoop.
//
// This is the critical piece that decouples CLI from server internals:
// the CLI never touches msgBus or disp directly — events flow through eventCh.
type localEventBridge struct {
	eventCh chan protocol.WSMessage
}

func (b *localEventBridge) Name() string { return "cli" }

func (b *localEventBridge) Start() error { return nil }

func (b *localEventBridge) Stop() {}

func (b *localEventBridge) Send(msg bus.OutboundMessage) (string, error) {
	// Convert OutboundMessage → WSMessage and push to eventCh.
	// The Client.eventLoop reads from eventCh and dispatches via dispatchWSMessage.
	wsMsg := protocol.WSMessage{
		Type:    protocol.MsgTypeText,
		Content: msg.Content,
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
	}
	if msg.WaitingUser {
		// ask_user events use a different type so the client can distinguish them.
		wsMsg.Type = protocol.MsgTypeAskUser
		if msg.Metadata != nil {
			if q, ok := msg.Metadata["ask_questions"]; ok {
				wsMsg.Content = q
			}
		}
	}
	select {
	case b.eventCh <- wsMsg:
	default:
		log.WithField("chat_id", msg.ChatID).Warn("localEventBridge: eventCh full, dropping message")
	}
	return "", nil
}

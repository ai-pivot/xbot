package serverapp

import (
	"context"
	"encoding/json"
	"fmt"
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
// It owns the Agent, Bus, Dispatcher, Backend, and RPCTable.
type ServerCore struct {
	Agent    *agent.Agent
	Config   *config.Config
	Disp     *channel.Dispatcher
	MsgBus   *bus.MessageBus
	Backend  *agent.Backend
	RPCTable RPCTable
}

// NewServerCore creates and initializes a ServerCore.
//
// It performs the following steps:
//  1. Creates the Agent from BackendConfig
//  2. Creates the Dispatcher
//  3. Creates the DirectBackend and RPCTable
//  4. Creates the LocalBackend (Transport + Agent)
//  5. Wires callbacks (directSend, channelFinder, sessionStateHandler, etc.)
//  6. Configures LLM (tiers, contexts, retry)
//  7. Registers core tools (DownloadFile, WebSearch, EventTrigger)
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
	bc := agent.BackendConfig{
		Cfg:              cfg,
		LLM:              opts.LLM,
		Bus:              msgBus,
		DBPath:           opts.DBPath,
		WorkDir:          opts.WorkDir,
		XbotHome:         opts.XbotHome,
		PersonaIsolation: opts.PersonaIsolation,
	}
	ag, err := agent.New(bc.AgentConfig())
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}

	// 3. Create RPCTable.
	rpcTable := BuildRPCTable(cfg, ag, disp, msgBus)

	// 4. Create Backend (local mode: ChannelTransport + Agent).
	backend := agent.NewLocalBackend(
		agent.NewChannelTransport(rpcTable.Dispatch),
		ag,
		nil, // runner: caller manages Agent.Run() directly
		nil, // router: caller sets up EventRouter if needed
		nil, // callbacks: caller wires via ag.WireCallbacks or core.Agent.WireCallbacks
	)

	// 5. Register core tools.
	backend.RegisterCoreTool(tools.NewDownloadFileTool(cfg.Feishu.AppID, cfg.Feishu.AppSecret))
	backend.RegisterTool(tools.NewDownloadFileTool(cfg.Feishu.AppID, cfg.Feishu.AppSecret))
	backend.RegisterCoreTool(tools.NewWebSearchTool(cfg.TavilyAPIKey))

	// Register Logs tool (admin only).
	if adminChatID := cfg.Admin.ChatID; adminChatID != "" {
		logsTool := tools.NewLogsTool(adminChatID)
		backend.RegisterCoreTool(logsTool)
		log.WithField("admin_chat_id", adminChatID).Info("Logs tool registered (admin only)")
	}

	// 7. Configure LLM.
	ag.LLMFactory().SetModelTiers(cfg.LLM)
	ag.LLMFactory().SetModelContexts(cfg.Agent.ModelContexts)
	ag.LLMFactory().SetRetryConfig(llm_pkg.RetryConfig{
		Attempts: uint(cfg.Agent.LLMRetryAttempts),
		Delay:    time.Duration(cfg.Agent.LLMRetryDelay),
		MaxDelay: time.Duration(cfg.Agent.LLMRetryMaxDelay),
		Timeout:  time.Duration(cfg.Agent.LLMRetryTimeout),
	})

	// Wire channel reconfiguration callback.
	backend.SetChannelReconfigureFn(func(name string) {
		if disp == nil || msgBus == nil {
			return
		}
		loaded := config.LoadFromFile(config.ConfigFilePath())
		if loaded == nil {
			return
		}
		_, running := disp.GetChannel(name)
		shouldRun := channelShouldRun(loaded, name)
		if shouldRun && !running {
			if ch := createChannelInstance(name, loaded, msgBus); ch != nil {
				disp.Register(ch)
			}
		} else if !shouldRun && running {
			disp.Unregister(name)
		}
	})

	return &ServerCore{
		Agent:    ag,
		Config:   cfg,
		Disp:     disp,
		MsgBus:   msgBus,
		Backend:  backend,
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
	sc.Backend.SetChannelReconfigureFn(fn)
}

// RegisterCoreTool registers a core tool on the backend.
func (sc *ServerCore) RegisterCoreTool(tool tools.Tool) {
	sc.Backend.RegisterCoreTool(tool)
}

// RegisterTool registers a tool on the backend.
func (sc *ServerCore) RegisterTool(tool tools.Tool) {
	sc.Backend.RegisterTool(tool)
}

// IndexGlobalTools indexes all registered tools.
func (sc *ServerCore) IndexGlobalTools() {
	sc.Backend.IndexGlobalTools()
}

// Run starts the agent loop and blocks until the context is done.
func (sc *ServerCore) Run(ctx context.Context) error {
	return sc.Backend.Run(ctx)
}

// Close releases backend resources.
func (sc *ServerCore) Close() error {
	return sc.Backend.Close()
}

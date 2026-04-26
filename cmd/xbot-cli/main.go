// xbot CLI entry point
// Standalone terminal-based chat interface
//
// Usage:
//   xbot-cli               Resume last session (default)
//   xbot-cli --resume      Resume session and show current status
//   xbot-cli --new         Start a new session
//   xbot-cli <prompt>      Execute single prompt in non-interactive mode
//   xbot-cli -p <prompt>   Execute single prompt in non-interactive mode
//   echo "hello" | xbot-cli  Pipe mode

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"xbot/agent"
	"xbot/agent/hooks"
	"xbot/bus"
	"xbot/channel"
	"xbot/clipanic"
	"xbot/config"
	"xbot/llm"
	log "xbot/logger"
	"xbot/serverapp"
	"xbot/storage"
	"xbot/storage/sqlite"
	"xbot/tools"
	"xbot/version"

	"github.com/google/uuid"
	"github.com/mattn/go-isatty"
)

// saveWg tracks in-flight config saves so SIGINT can wait for them.
var saveWg sync.WaitGroup

const cliSenderID = "cli_user"

// saveCLIConfig merges CLI-owned global fields into the latest on-disk config.
// It intentionally preserves unrelated sections like on-disk subscriptions and
// existing remote CLI connection settings unless the caller provides overrides.
// refreshRemoteValuesCache fetches current settings from the remote server
// and updates the local cache. Called from a background goroutine — never from
// the BubbleTea Update loop (which would freeze the TUI on WS disconnect).
func (app *cliApp) refreshRemoteValuesCache() {
	if app.backend == nil {
		return
	}
	vals := make(map[string]string)
	if sv, err := app.backend.GetSettings("cli", "cli_user"); err == nil {
		for k, v := range sv {
			vals[k] = v
		}
	}
	// LLM values come from the active subscription (single source of truth).
	// This replaces the old path where llm_model was read from GetSettings
	// (which stored stale LLM values in user_settings).
	if sub, err := app.backend.GetDefaultSubscription(cliSenderID); err == nil && sub != nil {
		vals["llm_provider"] = sub.Provider
		vals["llm_base_url"] = sub.BaseURL
		vals["llm_model"] = sub.Model
		if sub.APIKey != "" {
			vals["llm_api_key"] = sub.APIKey
		}
		if sub.MaxOutputTokens > 0 {
			vals["max_output_tokens"] = fmt.Sprintf("%d", sub.MaxOutputTokens)
		}
		if sub.ThinkingMode != "" {
			vals["thinking_mode"] = sub.ThinkingMode
		}
	}
	vals["context_mode"] = app.backend.GetContextMode()
	if _, ok := vals["sandbox_mode"]; !ok {
		vals["sandbox_mode"] = "none"
	}
	if _, ok := vals["memory_provider"]; !ok {
		vals["memory_provider"] = "flat"
	}
	if _, ok := vals["max_iterations"]; !ok {
		vals["max_iterations"] = "30"
	}
	if _, ok := vals["max_concurrency"]; !ok {
		vals["max_concurrency"] = "3"
	}
	if _, ok := vals["max_context_tokens"]; !ok {
		vals["max_context_tokens"] = "0"
	}
	app.valuesCacheMu.Lock()
	app.valuesCache = vals
	app.valuesCacheMu.Unlock()
}

func saveCLIConfig(cfg *config.Config) error {
	path := config.ConfigFilePath()
	merged := config.LoadFromFile(path)
	if merged == nil {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			merged = &config.Config{}
		} else {
			log.WithField("path", path).Error("saveCLIConfig: config file exists but cannot parse, refusing to overwrite")
			return fmt.Errorf("config file parse error, not overwriting")
		}
	}
	// CLI only ever modifies these sections:
	merged.LLM = cfg.LLM     // via settings panel / subscription switch
	merged.Agent = cfg.Agent // via settings panel (max_iterations, etc.)
	// CLI remote connection settings: only write if non-empty (e.g. first setup)
	if cfg.CLI.ServerURL != "" || cfg.CLI.Token != "" {
		merged.CLI = cfg.CLI
	}
	return config.SaveToFile(path, merged)
}

func isCLISubscriptionSettingKey(key string) bool {
	switch key {
	case "llm_provider", "llm_api_key", "llm_base_url", "llm_model", "max_output_tokens", "thinking_mode":
		return true
	default:
		return false
	}
}

func localSeedSourceSubscriptions(cfg *config.Config) []config.SubscriptionConfig {
	if len(cfg.Subscriptions) > 0 {
		return cfg.Subscriptions
	}
	if strings.TrimSpace(cfg.LLM.Provider) == "" &&
		strings.TrimSpace(cfg.LLM.BaseURL) == "" &&
		strings.TrimSpace(cfg.LLM.APIKey) == "" &&
		strings.TrimSpace(cfg.LLM.Model) == "" {
		return nil
	}
	name := strings.TrimSpace(cfg.LLM.Provider)
	if name == "" {
		name = "default"
	}
	return []config.SubscriptionConfig{{
		ID:              "default",
		Name:            name,
		Provider:        cfg.LLM.Provider,
		BaseURL:         cfg.LLM.BaseURL,
		APIKey:          cfg.LLM.APIKey,
		Model:           cfg.LLM.Model,
		MaxOutputTokens: cfg.LLM.MaxOutputTokens,
		ThinkingMode:    cfg.LLM.ThinkingMode,
		Active:          true,
	}}
}

func hasActiveSeedSubscription(subs []config.SubscriptionConfig) bool {
	for _, sub := range subs {
		if sub.Active {
			return true
		}
	}
	return false
}

func seedSubscriptionsForSender(svc *sqlite.LLMSubscriptionService, senderID string, subs []config.SubscriptionConfig) error {
	if svc == nil || len(subs) == 0 {
		return nil
	}
	hasActive := hasActiveSeedSubscription(subs)
	for i, sub := range subs {
		if err := svc.Add(&sqlite.LLMSubscription{
			ID:              sub.ID,
			SenderID:        senderID,
			Name:            sub.Name,
			Provider:        sub.Provider,
			BaseURL:         sub.BaseURL,
			APIKey:          sub.APIKey,
			Model:           sub.Model,
			MaxOutputTokens: sub.MaxOutputTokens,
			ThinkingMode:    sub.ThinkingMode,
			IsDefault:       sub.Active || (i == 0 && !hasActive),
		}); err != nil {
			return err
		}
	}
	return nil
}

func seedLocalDBSubscriptionsFromConfig(db *sqlite.DB, cfg *config.Config) error {
	if db == nil {
		return nil
	}
	svc := sqlite.NewLLMSubscriptionService(db)
	sourceSubs := localSeedSourceSubscriptions(cfg)
	if len(sourceSubs) == 0 {
		return nil
	}
	existing, err := svc.List(cliSenderID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	return seedSubscriptionsForSender(svc, cliSenderID, sourceSubs)
}

func loadLLMFromLocalDB(db *sqlite.DB, cfg *config.Config) bool {
	if db == nil {
		return false
	}
	llmCfg, err := sqlite.NewUserLLMConfigService(db).GetConfig(cliSenderID)
	if err != nil || llmCfg == nil {
		return false
	}
	cfg.LLM.Provider = llmCfg.Provider
	cfg.LLM.BaseURL = llmCfg.BaseURL
	cfg.LLM.APIKey = llmCfg.APIKey
	cfg.LLM.Model = llmCfg.Model
	cfg.LLM.MaxOutputTokens = llmCfg.MaxOutputTokens
	cfg.LLM.ThinkingMode = llmCfg.ThinkingMode
	return true
}

func seedLocalDBSubscriptions(backend agent.AgentBackend, cfg *config.Config) error {
	if backend == nil || backend.LLMFactory() == nil {
		return nil
	}
	svc := backend.LLMFactory().GetSubscriptionSvc()
	if svc == nil {
		return nil
	}
	sourceSubs := localSeedSourceSubscriptions(cfg)
	if len(sourceSubs) == 0 {
		return nil
	}
	existing, err := svc.List(cliSenderID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	return seedSubscriptionsForSender(svc, cliSenderID, sourceSubs)
}

func loadLLMFromDBSubscription(backend agent.AgentBackend, cfg *config.Config) bool {
	if backend == nil {
		return false
	}
	sub, err := backend.GetDefaultSubscription(cliSenderID)
	if err != nil || sub == nil {
		return false
	}
	cfg.LLM.Provider = sub.Provider
	cfg.LLM.BaseURL = sub.BaseURL
	cfg.LLM.APIKey = sub.APIKey
	cfg.LLM.Model = sub.Model
	cfg.LLM.MaxOutputTokens = backend.GetUserMaxOutputTokens(cliSenderID)
	cfg.LLM.ThinkingMode = backend.GetUserThinkingMode(cliSenderID)
	return true
}

func currentActiveSubscription(backend agent.AgentBackend, cfg *config.Config) *channel.Subscription {
	if backend != nil {
		if sub, err := backend.GetDefaultSubscription(cliSenderID); err == nil && sub != nil {
			return sub
		}
	}
	sourceSubs := localSeedSourceSubscriptions(cfg)
	for i, sub := range sourceSubs {
		if sub.Active || (i == 0 && !hasActiveSeedSubscription(sourceSubs)) {
			return &channel.Subscription{
				ID:       sub.ID,
				Name:     sub.Name,
				Provider: sub.Provider,
				BaseURL:  sub.BaseURL,
				APIKey:   sub.APIKey,
				Model:    sub.Model,
				Active:   true,
			}
		}
	}
	return nil
}

// updateActiveSubscription updates the current default subscription with LLM field
// changes from the Settings panel. This is the ONLY path for LLM config changes —
// user_llm_subscriptions is the single source of truth.
//
// When only llm_model changes (no provider/key/url), it checks if the target model
// belongs to a different subscription and switches to it instead of overwriting.
func updateActiveSubscription(backend agent.AgentBackend, cfg *config.Config, values map[string]string) error {
	if backend == nil {
		return nil
	}

	// Smart model switch: if only llm_model changed, find a matching subscription.
	if v, ok := values["llm_model"]; ok && strings.TrimSpace(v) != "" {
		targetModel := strings.TrimSpace(v)
		_, providerChanged := values["llm_provider"]
		_, keyChanged := values["llm_api_key"]
		_, urlChanged := values["llm_base_url"]
		if !providerChanged && !keyChanged && !urlChanged {
			if subs, err := backend.ListSubscriptions(cliSenderID); err == nil {
				for _, sub := range subs {
					if sub.Model == targetModel && sub.ID != "" {
						return backend.SetDefaultSubscription(sub.ID, "")
					}
				}
			}
		}
	}

	// Get or create default subscription
	sub, err := backend.GetDefaultSubscription(cliSenderID)
	if err != nil || sub == nil {
		// No subscription exists yet (first-time setup). Create one from the provided values.
		provider := strings.TrimSpace(values["llm_provider"])
		apiKey := strings.TrimSpace(values["llm_api_key"])
		model := strings.TrimSpace(values["llm_model"])
		baseURL := strings.TrimSpace(values["llm_base_url"])
		if provider == "" {
			provider = cfg.LLM.Provider
		}
		if baseURL == "" {
			baseURL = cfg.LLM.BaseURL
		}
		if model == "" {
			model = cfg.LLM.Model
		}
		newSub := channel.Subscription{
			Name:            "default",
			Provider:        provider,
			APIKey:          apiKey,
			Model:           model,
			BaseURL:         baseURL,
			MaxOutputTokens: cfg.LLM.MaxOutputTokens,
			ThinkingMode:    cfg.LLM.ThinkingMode,
			Active:          true,
		}
		if v, ok := values["max_output_tokens"]; ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				newSub.MaxOutputTokens = n
			}
		}
		if v, ok := values["thinking_mode"]; ok {
			newSub.ThinkingMode = v
		}
		if err := backend.AddSubscription(cliSenderID, newSub); err != nil {
			return fmt.Errorf("create subscription: %w", err)
		}
		// Find the newly created subscription and set it as default
		subs, listErr := backend.ListSubscriptions(cliSenderID)
		if listErr != nil {
			return fmt.Errorf("list subscriptions after create: %w", listErr)
		}
		for _, s := range subs {
			if s.Provider == provider && s.Model == model && s.APIKey == apiKey {
				_ = backend.SetDefaultSubscription(s.ID, "")
				break
			}
		}
		return nil
	}

	// Apply changed fields
	if v, ok := values["llm_provider"]; ok && strings.TrimSpace(v) != "" {
		sub.Provider = strings.TrimSpace(v)
	}
	if v, ok := values["llm_api_key"]; ok && strings.TrimSpace(v) != "" {
		key := strings.TrimSpace(v)
		// Never overwrite with a masked key (e.g. "sk-a****") from server RPC.
		// This would destroy the real API key in storage.
		if !strings.HasSuffix(key, "****") || len(key) > 20 {
			sub.APIKey = key
		}
	}
	if v, ok := values["llm_model"]; ok && strings.TrimSpace(v) != "" {
		sub.Model = strings.TrimSpace(v)
	}
	if v, ok := values["llm_base_url"]; ok && strings.TrimSpace(v) != "" {
		sub.BaseURL = strings.TrimSpace(v)
	}
	if v, ok := values["max_output_tokens"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			sub.MaxOutputTokens = n
		}
	}
	if v, ok := values["thinking_mode"]; ok {
		sub.ThinkingMode = v
	}

	return backend.UpdateSubscription(sub.ID, *sub)
}

// cliApp wraps shared CLI initialization logic for both interactive and non-interactive modes.
type cliApp struct {
	cfg       *config.Config
	llmClient llm.LLM
	msgBus    *bus.MessageBus
	db        *sqlite.DB
	backend   agent.AgentBackend
	workDir   string
	xbotHome  string

	// Remote-mode async cache for agent info (avoid RPC from event loop → deadlock)
	agentCacheMu    sync.RWMutex
	agentCacheCount int
	agentCacheList  []channel.AgentPanelEntry

	// Remote-mode async cache for GetCurrentValues (avoid RPC from Update loop → 30s freeze)
	valuesCacheMu sync.RWMutex
	valuesCache   map[string]string

	// Remote-mode background goroutine cancel
	valuesCancel context.CancelFunc
}

// isFirstRun checks if this is the first run (config.json missing or API key not configured)
func isFirstRun() bool {
	configPath := config.ConfigFilePath()
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return true
	}
	cfg := config.LoadFromFile(configPath)
	if cfg == nil {
		return true
	}
	// Check config-level API key
	if cfg.LLM.APIKey != "" {
		return false
	}
	// Check environment variable override
	if os.Getenv("LLM_API_KEY") != "" {
		return false
	}
	// Check config.json subscriptions array (may have active sub with API key)
	for _, sub := range cfg.Subscriptions {
		if sub.Active && sub.APIKey != "" {
			return false
		}
	}
	return true
}

// isLocalServer returns true if the server URL points to a local/loopback address.
func isLocalServer(serverURL string) bool {
	u, err := url.Parse(serverURL)
	if err != nil {
		return false
	}
	h := strings.Split(u.Host, ":")[0] // strip port
	// Fast path: standard loopback addresses
	if h == "127.0.0.1" || h == "localhost" || h == "::1" || h == "" {
		return true
	}
	// Slow path: check if the host is a local network interface IP
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.Equal(ip) {
			return true
		}
	}
	return false
}

// newCLIApp performs shared initialization: loads config, creates Backend.
// If serverURL is non-empty, creates a RemoteBackend (agent runs on server).
// Otherwise creates a LocalBackend (agent runs in-process).
func newCLIApp(serverURL, token string, forceLocal bool) *cliApp {
	cfg := config.Load()

	// If --server was not specified on the command line, fall back to config.
	// --local disables this fallback and forces legacy in-process mode.
	if !forceLocal {
		if serverURL == "" && cfg.CLI.ServerURL != "" {
			serverURL = cfg.CLI.ServerURL
		}
		if token == "" && cfg.CLI.Token != "" {
			token = cfg.CLI.Token
		}
	}
	localMode := serverURL == ""

	workDir := cfg.Agent.WorkDir
	xbotHome := config.XbotHome()
	dbPath := config.DBFilePath()

	if err := setupLogger(cfg.Log, xbotHome); err != nil {
		log.WithError(err).Fatal("Failed to setup logger")
	}

	msgBus := bus.NewMessageBus()

	if err := storage.MigrateIfNeeded(context.Background(), workDir, dbPath); err != nil {
		log.WithError(err).Fatal("Failed to migrate data to SQLite")
	}

	// Migrate flat memory from SQLite tables to MD files (if needed)
	storage.MigrateMemoryToFiles(dbPath)

	db, err := sqlite.Open(dbPath)
	if err != nil {
		log.WithError(err).Warn("Failed to open token database, runner tokens disabled")
	} else {
		tools.SetRunnerTokenDB(db.Conn())
	}

	if localMode {
		if err := seedLocalDBSubscriptionsFromConfig(db, cfg); err != nil {
			log.WithError(err).Warn("Failed to seed local DB subscriptions from config")
		}
		if !loadLLMFromLocalDB(db, cfg) {
			syncLLMFromActiveSub(cfg)
		}
	} else {
		syncLLMFromActiveSub(cfg)
	}

	llmClient, err := createLLM(cfg.LLM, llm.RetryConfig{
		Attempts: uint(cfg.Agent.LLMRetryAttempts),
		Delay:    cfg.Agent.LLMRetryDelay,
		MaxDelay: cfg.Agent.LLMRetryMaxDelay,
		Timeout:  cfg.Agent.LLMRetryTimeout,
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to create LLM client")
	}
	log.WithFields(log.Fields{
		"provider": cfg.LLM.Provider,
		"model":    cfg.LLM.Model,
	}).Info("LLM client created")

	tools.InitSandbox(cfg.Sandbox, workDir)

	var backend agent.AgentBackend
	if serverURL != "" {
		// Remote mode: agent loop runs on the server
		log.WithField("server", serverURL).Info("Using remote backend")
		backend = agent.NewRemoteBackend(agent.RemoteBackendConfig{
			ServerURL: serverURL,
			Token:     token,
		})
	} else {
		// Local mode: agent loop runs in-process
		bc := agent.BackendConfig{
			Cfg:             cfg,
			LLM:             llmClient,
			Bus:             msgBus,
			DBPath:          dbPath,
			WorkDir:         workDir,
			XbotHome:        xbotHome,
			DirectWorkspace: workDir, // CLI: workspace = workDir directly (no per-user subdirectory)
		}
		backend, err = agent.NewLocalBackend(bc.AgentConfig())
		if err != nil {
			log.WithError(err).Fatal("Failed to create local backend")
		}
		backend.RegisterCoreTool(tools.NewWebSearchTool(cfg.TavilyAPIKey))
		backend.IndexGlobalTools()
		backend.LLMFactory().SetModelTiers(cfg.LLM)
		backend.LLMFactory().SetRetryConfig(llm.RetryConfig{
			Attempts: uint(cfg.Agent.LLMRetryAttempts),
			Delay:    cfg.Agent.LLMRetryDelay,
			MaxDelay: cfg.Agent.LLMRetryMaxDelay,
			Timeout:  cfg.Agent.LLMRetryTimeout,
		})
	}

	return &cliApp{
		cfg:       cfg,
		llmClient: llmClient,
		msgBus:    msgBus,
		db:        db,
		backend:   backend,
		workDir:   workDir,
		xbotHome:  xbotHome,
	}
}

// Close releases resources.
func (app *cliApp) Close() {
	if app.valuesCancel != nil {
		app.valuesCancel()
	}
	if app.backend != nil {
		app.backend.Stop()
	}
	if app.db != nil {
		app.db.Close()
	}
	log.Close()
}

// ---------------------------------------------------------------------------
// CLI flags and interactive state
// ---------------------------------------------------------------------------

// cliFlags holds parsed command-line flags for the CLI entry point.
type cliFlags struct {
	prompt     string
	newSession bool
	server     string // --server ws://host:port (RemoteBackend: agent runs on server)
	share      string // --share ws://host:port/ws/userID (Runner mode: tools run locally)
	token      string // --token xxx
	workspace  string // --workspace /path
	local      bool   // --local force legacy in-process mode
	debug      bool   // --debug enable UI capture + key injection
	debugInput string // --debug-input "1,enter,ctrl+c"
	debugCapMs int    // --debug-capture-ms 200
}

// interactiveState holds shared state for the interactive CLI main flow,
// passed between extracted helper functions to avoid deep nesting and
// excessive parameter lists.
type interactiveState struct {
	app             *cliApp
	absWorkDir      string
	isRemote        bool
	flags           cliFlags
	firstRun        bool
	remoteServerURL string

	// Local mode DB services (nil in remote mode)
	tenantSvc  *sqlite.TenantService
	sessionSvc *sqlite.SessionService
	tenantID   int64
}

// printCLIHelp prints usage information for xbot-cli.
func printCLIHelp() {
	fmt.Println("Usage: xbot-cli [options] [prompt]")
	fmt.Println()
	fmt.Println("Modes:")
	fmt.Println("  default             Auto mode: use remote server if cli.server_url is configured")
	fmt.Println("  --local             Force legacy local mode (in-process agent, old behavior)")
	fmt.Println("  --server <ws-url>   Force remote mode and connect to server")
	fmt.Println("  serve               Run server mode in the same binary")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --help, -h          Show this help")
	fmt.Println("  --new               Start a new session")
	fmt.Println("  --resume            Resume last session (default)")
	fmt.Println("  -p <prompt>         Non-interactive single prompt")
	fmt.Println("  --token <token>     Token for remote server")
	fmt.Println("  --workspace <path>  Override workspace")
}

// handleSubcommands processes early-exit sub-commands (install, serve, help).
// Returns true if a sub-command was handled and main should return immediately.
func handleSubcommands() bool {
	if len(os.Args) <= 1 {
		return false
	}
	switch os.Args[1] {
	case "install":
		fmt.Println("The install subcommand is deprecated, please use scripts/install.sh")
		fmt.Println("Example: curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash")
		return true
	case "serve":
		if err := serverapp.Run(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		return true
	case "--help", "-h", "help":
		printCLIHelp()
		return true
	}
	return false
}

// parseCLIFlags parses command-line flags from os.Args and returns the result.
func parseCLIFlags() cliFlags {
	var f cliFlags
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--resume":
			// Kept for compatibility, behaves the same as default
		case "--new":
			f.newSession = true
		case "-p":
			if len(os.Args) > i+1 {
				f.prompt = os.Args[i+1]
			}
		case "--server":
			if len(os.Args) > i+1 {
				f.server = os.Args[i+1]
				i++
			}
		case "--local":
			f.local = true
		case "--debug":
			f.debug = true
		case "--debug-input":
			if len(os.Args) > i+1 {
				f.debugInput = os.Args[i+1]
				i++
				f.debug = true // auto-enable debug mode
			}
		case "--debug-capture-ms":
			if len(os.Args) > i+1 {
				n, err := strconv.Atoi(os.Args[i+1])
				if err == nil && n >= 50 {
					f.debugCapMs = n
				}
				i++
			}
		case "--help", "-h":
			printCLIHelp()
			os.Exit(0)
		case "--share":
			if len(os.Args) > i+1 {
				f.share = os.Args[i+1]
				i++
			}
		case "--token":
			if len(os.Args) > i+1 {
				f.token = os.Args[i+1]
				i++
			}
		case "--workspace":
			if len(os.Args) > i+1 {
				f.workspace = os.Args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(os.Args[i], "-") {
				f.prompt = os.Args[i]
			}
		}
	}
	return f
}

// buildCLIChannelConfig constructs the CLIChannelConfig with all callback functions.
// It writes into the provided cfg pointer so that closures referencing cfg fields
// (e.g., DynamicHistoryLoader) see later mutations from setupHistoryLoaders.
func buildCLIChannelConfig(s *interactiveState, cfg *channel.CLIChannelConfig) {
	cfg.WorkDir = s.app.workDir
	cfg.ChatID = s.absWorkDir
	cfg.RemoteMode = s.isRemote
	cfg.RemoteServerURL = s.remoteServerURL
	cfg.DebugMode = s.flags.debug
	cfg.DebugInput = s.flags.debugInput
	cfg.DebugCaptureMs = s.flags.debugCapMs
	cfg.IsFirstRun = s.firstRun

	app := s.app // local alias for closure captures

	// ── GetCurrentValues: returns current settings for the TUI settings panel ──
	cfg.GetCurrentValues = func() map[string]string {
		// In remote mode, return cached values — never block the BubbleTea Update loop.
		// The cache is refreshed asynchronously by refreshRemoteValuesCache().
		if app.backend != nil && app.backend.IsRemote() {
			app.valuesCacheMu.RLock()
			cache := app.valuesCache
			app.valuesCacheMu.RUnlock()
			return cache
		}
		// Local mode: read directly from config (fast, no RPC).
		activeSub := currentActiveSubscription(app.backend, app.cfg)
		llmProvider := app.cfg.LLM.Provider
		llmAPIKey := app.cfg.LLM.APIKey
		llmModel := app.cfg.LLM.Model
		llmBaseURL := app.cfg.LLM.BaseURL
		if activeSub != nil {
			llmProvider = activeSub.Provider
			llmAPIKey = activeSub.APIKey
			llmModel = activeSub.Model
			llmBaseURL = activeSub.BaseURL
		}
		return map[string]string{
			"llm_provider":   llmProvider,
			"llm_api_key":    llmAPIKey,
			"llm_model":      llmModel,
			"llm_base_url":   llmBaseURL,
			"vanguard_model": app.cfg.LLM.VanguardModel,
			"balance_model":  app.cfg.LLM.BalanceModel,
			"swift_model":    app.cfg.LLM.SwiftModel,
			"sandbox_mode": func() string {
				if app.cfg.Sandbox.Mode != "" {
					return app.cfg.Sandbox.Mode
				}
				return "none"
			}(),
			"memory_provider":    app.cfg.Agent.MemoryProvider,
			"tavily_api_key":     app.cfg.TavilyAPIKey,
			"context_mode":       app.cfg.Agent.ContextMode,
			"max_iterations":     fmt.Sprintf("%d", app.cfg.Agent.MaxIterations),
			"max_concurrency":    fmt.Sprintf("%d", app.cfg.Agent.MaxConcurrency),
			"max_context_tokens": fmt.Sprintf("%d", app.cfg.Agent.MaxContextTokens),
			"max_output_tokens": func() string {
				// Prefer subscription value (single source of truth)
				if activeSub != nil && activeSub.MaxOutputTokens > 0 {
					return fmt.Sprintf("%d", activeSub.MaxOutputTokens)
				}
				if app.cfg.LLM.MaxOutputTokens > 0 {
					return fmt.Sprintf("%d", app.cfg.LLM.MaxOutputTokens)
				}
				return "8192"
			}(),
			"thinking_mode": func() string {
				if activeSub != nil && activeSub.ThinkingMode != "" {
					return activeSub.ThinkingMode
				}
				return app.cfg.LLM.ThinkingMode
			}(),
			"enable_auto_compress": func() string {
				if app.cfg.Agent.EnableAutoCompress == nil || *app.cfg.Agent.EnableAutoCompress {
					return "true"
				}
				return "false"
			}(),
			"theme": func() string {
				// Read persisted theme from settings, default to dark
				if app.backend != nil {
					if ss := app.backend.SettingsService(); ss != nil {
						if vals, err := ss.GetSettings("cli", "cli_user"); err == nil {
							if t, ok := vals["theme"]; ok && t != "" {
								return t
							}
						}
					}
				}
				return "midnight"
			}(),
			"language": func() string {
				if app.backend != nil {
					if ss := app.backend.SettingsService(); ss != nil {
						if vals, err := ss.GetSettings("cli", "cli_user"); err == nil {
							if l, ok := vals["language"]; ok {
								return l
							}
						}
					}
				}
				return ""
			}(),
		}
	}

	// ── ApplySettings: persists setting changes from the TUI settings panel ──
	cfg.ApplySettings = func(values map[string]string) {
		if app.backend == nil {
			return
		}
		_, llmChanged := values["llm_provider"]
		_, keyChanged := values["llm_api_key"]
		_, modelChanged := values["llm_model"]
		_, urlChanged := values["llm_base_url"]
		_, vanguardChanged := values["vanguard_model"]
		_, balanceChanged := values["balance_model"]
		_, swiftChanged := values["swift_model"]
		_, maxOutputChanged := values["max_output_tokens"]
		_, thinkingChanged := values["thinking_mode"]

		llmFieldChanged := llmChanged || keyChanged || modelChanged || urlChanged || maxOutputChanged || thinkingChanged

		// ── Subscription-scoped fields: update via subscription manager ──
		if llmFieldChanged {
			if err := updateActiveSubscription(app.backend, app.cfg, values); err != nil {
				log.Warnf("Failed to update active subscription: %v", err)
			}
		}

		// ── Non-subscription settings: persist and apply runtime ──
		for k, v := range values {
			if isCLISubscriptionSettingKey(k) {
				continue // subscription fields handled above
			}
			if channel.IsGlobalScopedSettingKey(k) {
				continue // global-scoped keys not stored in DB
			}
			_ = app.backend.SetSetting("cli", "cli_user", k, v)
		}
		applyCLISettingsToBackend(app.backend, "cli_user", values)

		// ── Local-mode extras ──
		if !app.backend.IsRemote() {
			applyCLISettingsToConfig(app.cfg, values)
			// Model tiers (needs explicit check since config-only)
			if vanguardChanged || balanceChanged || swiftChanged {
				app.backend.LLMFactory().SetModelTiers(app.cfg.LLM)
			}
			// Sandbox reinit (local-only, needs app.workDir closure)
			if v, ok := values["sandbox_mode"]; ok && v != "" {
				tools.ReinitSandbox(app.cfg.Sandbox, app.workDir)
				app.backend.SetSandbox(tools.GetSandbox(), v)
			}
			applyCLISettingsToBackend(app.backend, cliSenderID, values)
			loadLLMFromDBSubscription(app.backend, app.cfg)
			if err := saveCLIConfig(app.cfg); err != nil {
				log.Warnf("Failed to save config.json: %v", err)
			}
			if theme, ok := values["theme"]; ok && theme != "" {
				if ss := app.backend.SettingsService(); ss != nil {
					_ = ss.SetSetting("cli", "cli_user", "theme", theme)
				}
			}
			if maxOutputChanged || thinkingChanged {
				if newClient, err := createLLM(app.cfg.LLM, llm.DefaultRetryConfig()); err == nil {
					app.llmClient = newClient
					app.backend.LLMFactory().SetDefaults(newClient, app.cfg.LLM.Model)
					app.backend.LLMFactory().SetDefaultThinkingMode(app.cfg.LLM.ThinkingMode)
					app.backend.LLMFactory().SetModelTiers(app.cfg.LLM)
				} else {
					log.Warnf("Failed to rebuild LLM client: %v", err)
				}
			}
		}

		// ── Remote mode: immediately refresh cache so UI shows new values ──
		if app.backend.IsRemote() {
			app.refreshRemoteValuesCache()
		}
	}

	// ── ClearMemory: clears agent memory for the current session ──
	cfg.ClearMemory = func(targetType string) error {
		if app.backend == nil {
			return fmt.Errorf("agent not initialized")
		}
		return app.backend.ClearMemory(context.Background(), "cli", s.absWorkDir, targetType, "cli_user")
	}

	// ── GetMemoryStats: returns memory statistics for the current session ──
	cfg.GetMemoryStats = func() map[string]string {
		if app.backend == nil {
			return map[string]string{}
		}
		return app.backend.GetMemoryStats(context.Background(), "cli", s.absWorkDir, "cli_user")
	}

	// ── SwitchLLM: switches the LLM client for the current session ──
	cfg.SwitchLLM = func(provider, baseURL, apiKey, model string) error {
		llmCfg := config.LLMConfig{
			Provider: provider,
			BaseURL:  baseURL,
			APIKey:   apiKey,
			Model:    model,
		}
		client, err := createLLM(llmCfg, llm.DefaultRetryConfig())
		if err != nil {
			return fmt.Errorf("create LLM: %w", err)
		}
		app.llmClient = client
		if app.backend != nil {
			if factory := app.backend.LLMFactory(); factory != nil {
				// Only cache for this chat — don't affect other CLI windows
				factory.SetChatLLM(cliSenderID, s.absWorkDir, client, model)
				factory.SetModelTiers(app.cfg.LLM)
			}
		}
		return nil
	}

	// ── RefreshValuesCache: triggers a manual refresh of the remote values cache ──
	cfg.RefreshValuesCache = func() {
		app.refreshRemoteValuesCache()
	}

	// ── UsageQuery: returns token usage statistics ──
	cfg.UsageQuery = func(senderID string, days int) (*sqlite.UserTokenUsage, []sqlite.DailyTokenUsage, error) {
		if app.backend == nil {
			return nil, nil, fmt.Errorf("agent not initialized")
		}
		if app.backend.IsRemote() {
			// Remote mode: get data via RPC and convert from map to struct
			cumMap, err := app.backend.GetUserTokenUsage(senderID)
			if err != nil {
				return nil, nil, err
			}
			var cumulative *sqlite.UserTokenUsage
			if cumMap != nil {
				var u sqlite.UserTokenUsage
				if b, _ := json.Marshal(cumMap); len(b) > 0 {
					_ = json.Unmarshal(b, &u)
				}
				cumulative = &u
			}
			dailyMaps, err := app.backend.GetDailyTokenUsage(senderID, days)
			if err != nil {
				return nil, nil, err
			}
			var daily []sqlite.DailyTokenUsage
			for _, dm := range dailyMaps {
				var d sqlite.DailyTokenUsage
				if b, _ := json.Marshal(dm); len(b) > 0 {
					_ = json.Unmarshal(b, &d)
				}
				daily = append(daily, d)
			}
			return cumulative, daily, nil
		}
		ms := app.backend.MultiSession()
		cumulative, err := ms.GetUserTokenUsage(senderID)
		if err != nil {
			return nil, nil, err
		}
		daily, err := ms.GetDailyTokenUsage(senderID, days)
		if err != nil {
			return nil, nil, err
		}
		return cumulative, daily, nil
	}

	// ── AgentCount: returns the number of running agent sessions ──
	cfg.AgentCount = func() int {
		if app.backend == nil {
			return 0
		}
		if app.backend.IsRemote() {
			app.agentCacheMu.RLock()
			defer app.agentCacheMu.RUnlock()
			return app.agentCacheCount
		}
		return app.backend.CountInteractiveSessions("cli", s.absWorkDir)
	}

	// ── AgentList: returns a list of running agent sessions ──
	cfg.AgentList = func() []channel.AgentPanelEntry {
		if app.backend == nil {
			return nil
		}
		if app.backend.IsRemote() {
			app.agentCacheMu.RLock()
			defer app.agentCacheMu.RUnlock()
			return app.agentCacheList
		}
		sessions := app.backend.ListInteractiveSessions("cli", s.absWorkDir)
		entries := make([]channel.AgentPanelEntry, len(sessions))
		for i, sess := range sessions {
			entries[i] = channel.AgentPanelEntry{
				Role:       sess.Role,
				Instance:   sess.Instance,
				Running:    sess.Running,
				Background: sess.Background,
				Task:       sess.Task,
				Preview:    sess.Preview,
			}
		}
		return entries
	}

	// ── AgentInspect: returns detailed information about an agent session ──
	cfg.AgentInspect = func(roleName, instance string, tailCount int) (string, error) {
		if app.backend == nil {
			return "", fmt.Errorf("agent not initialized")
		}
		return app.backend.InspectInteractiveSession(context.Background(), roleName, "cli", s.absWorkDir, instance, tailCount)
	}

	// ── AgentMessages: returns the chat messages of an agent session ──
	cfg.AgentMessages = func(roleName, instance string) []channel.SessionChatMessage {
		if app.backend == nil {
			return nil
		}
		msgs, _ := app.backend.GetSessionMessages("cli", s.absWorkDir, roleName, instance)
		if msgs == nil {
			return nil
		}
		result := make([]channel.SessionChatMessage, len(msgs))
		for i, m := range msgs {
			result[i] = channel.SessionChatMessage{Role: m.Role, Content: m.Content}
		}
		return result
	}

	// ── SessionsList: returns all sessions for the session panel ──
	absWorkDir := s.absWorkDir // local copy for closure
	cfg.SessionsList = func() []channel.SessionPanelEntry {
		var entries []channel.SessionPanelEntry
		tenants, err := app.backend.ListTenants()
		seen := make(map[string]bool) // dedup agent sessions by role:instance
		if err == nil && len(tenants) > 0 {
			// Fetch all interactive sessions across all chatIDs at once
			// (empty chatID = list all for the channel).
			allSessions := app.backend.ListInteractiveSessions("cli", "")

			for _, t := range tenants {
				// Agent tenants (channel="agent") are not real "main" sessions —
				// they're internal bookkeeping for interactive SubAgent persistence.
				// Skip them; agent sessions are listed separately via ListInteractiveSessions.
				if t.Channel == "agent" {
					continue
				}
				isActive := t.ChatID == absWorkDir && t.Channel == "cli"
				label := fmt.Sprintf("[%s] %s", t.Channel, t.ChatID)
				if isActive {
					label = "Main Chat  You ↔ Agent"
				}
				entries = append(entries, channel.SessionPanelEntry{
					ID:      t.ChatID,
					Type:    "main",
					Channel: t.Channel,
					Label:   label,
					Active:  isActive,
				})
				// SubAgent sessions: list agents belonging to this tenant's chatID.
				for _, sess := range allSessions {
					if sess.ChatID != t.ChatID {
						continue
					}
					agentKey := sess.Role + ":" + sess.Instance
					if seen[agentKey] {
						continue
					}
					seen[agentKey] = true
					entries = append(entries, channel.SessionPanelEntry{
						ID:          fmt.Sprintf("agent:%s/%s", sess.Role, sess.Instance),
						Type:        "agent",
						Channel:     t.Channel,
						Role:        sess.Role,
						Instance:    sess.Instance,
						ParentID:    t.ChatID,
						Running:     sess.Running,
						MessageHint: sess.Preview,
					})
				}
			}
		} else {
			// Fallback: no tenants available
			entries = append(entries, channel.SessionPanelEntry{
				ID:      absWorkDir,
				Type:    "main",
				Channel: "cli",
				Label:   "Main Chat  You ↔ Agent",
				Active:  true,
			})
			sessions := app.backend.ListInteractiveSessions("cli", absWorkDir)
			for _, sess := range sessions {
				agentKey := sess.Role + ":" + sess.Instance
				if seen[agentKey] {
					continue
				}
				seen[agentKey] = true
				entries = append(entries, channel.SessionPanelEntry{
					ID:          fmt.Sprintf("agent:%s/%s", sess.Role, sess.Instance),
					Type:        "agent",
					Channel:     "cli",
					Role:        sess.Role,
					Instance:    sess.Instance,
					ParentID:    sess.ChatID,
					Running:     sess.Running,
					MessageHint: sess.Preview,
				})
			}
		}
		// Append group chats
		for _, g := range tools.ListGroups() {
			status := ""
			if g.Closed {
				status = " [closed]"
			}
			entries = append(entries, channel.SessionPanelEntry{
				ID:          g.Name,
				Type:        "group",
				Label:       "💬 " + g.Name + status,
				MessageHint: fmt.Sprintf("%d members", len(g.Members)),
			})
		}
		return entries
	}

	// ── ChannelConfigGetFn: returns all channel configurations ──
	cfg.ChannelConfigGetFn = func() (map[string]map[string]string, error) {
		if app.backend == nil {
			return nil, fmt.Errorf("agent not initialized")
		}
		return app.backend.GetChannelConfigs()
	}

	// ── ChannelConfigSetFn: updates a channel configuration ──
	cfg.ChannelConfigSetFn = func(channelName string, values map[string]string) error {
		if app.backend == nil {
			return fmt.Errorf("agent not initialized")
		}
		return app.backend.SetChannelConfig(channelName, values)
	}

	// ── Web user management callbacks ──
	cfg.CreateWebUserFn = func(username string) (string, error) {
		if app.backend == nil {
			return "", fmt.Errorf("agent not initialized")
		}
		if app.backend.IsRemote() {
			result, err := app.backend.CallRPC("create_web_user", map[string]string{"username": username})
			if err != nil {
				return "", err
			}
			var resp struct {
				Password string `json:"password"`
			}
			if err := json.Unmarshal(result, &resp); err != nil {
				return "", err
			}
			return resp.Password, nil
		}
		db := app.backend.MultiSession().DB().Conn()
		_, password, err := channel.CreateWebUser(db, username)
		return password, err
	}

	cfg.ListWebUsersFn = func() ([]map[string]any, error) {
		if app.backend == nil {
			return nil, fmt.Errorf("agent not initialized")
		}
		if app.backend.IsRemote() {
			result, err := app.backend.CallRPC("list_web_users", nil)
			if err != nil {
				return nil, err
			}
			var users []channel.WebUserInfo
			if err := json.Unmarshal(result, &users); err != nil {
				return nil, err
			}
			out := make([]map[string]any, len(users))
			for i, u := range users {
				out[i] = map[string]any{"id": u.ID, "username": u.Username, "created_at": u.CreatedAt}
			}
			return out, nil
		}
		users, err := channel.ListWebUsers(app.backend.MultiSession().DB().Conn())
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, len(users))
		for i, u := range users {
			out[i] = map[string]any{"id": u.ID, "username": u.Username, "created_at": u.CreatedAt}
		}
		return out, nil
	}

	cfg.DeleteWebUserFn = func(username string) error {
		if app.backend == nil {
			return fmt.Errorf("agent not initialized")
		}
		if app.backend.IsRemote() {
			_, err := app.backend.CallRPC("delete_web_user", map[string]string{"username": username})
			return err
		}
		return channel.DeleteWebUser(app.backend.MultiSession().DB().Conn(), username)
	}

	cfg.IsAdminFn = func() bool {
		return true // standalone mode: CLI user is always admin
	}
}

// setupHistoryLoaders configures history loading callbacks on the channel config.
// It populates s.tenantSvc, s.sessionSvc, and s.tenantID for local mode DB access.
func setupHistoryLoaders(s *interactiveState, cfg *channel.CLIChannelConfig) {
	app := s.app

	// Set history message loader (session resume) — local mode only
	if !app.backend.IsRemote() && app.db != nil {
		s.tenantSvc = sqlite.NewTenantService(app.db)
		s.sessionSvc = sqlite.NewSessionService(app.db)
		tenantID, err := s.tenantSvc.GetOrCreateTenantID("cli", s.absWorkDir)
		if err == nil {
			s.tenantID = tenantID
			svc := s.sessionSvc // capture for closure
			tid := tenantID
			cfg.HistoryLoader = func() ([]channel.HistoryMessage, error) {
				msgs, err := svc.GetAllMessages(tid)
				if err != nil {
					return nil, err
				}
				return channel.ConvertMessagesToHistory(msgs), nil
			}
		}
	}
	// Remote mode: history loaded after backend.Start() via cliCh.LoadHistory()

	// Dynamic history loader: loads target session history by (channelName, chatID)
	if s.tenantSvc != nil && s.sessionSvc != nil {
		// Local mode: load from session DB directly
		ts := s.tenantSvc
		ss := s.sessionSvc
		cfg.DynamicHistoryLoader = func(channelName, chatID string) ([]channel.HistoryMessage, error) {
			if channelName == "" {
				channelName = "cli"
			}
			tid, err := ts.GetOrCreateTenantID(channelName, chatID)
			if err != nil {
				return nil, fmt.Errorf("get tenant: %w", err)
			}
			msgs, err := ss.GetAllMessages(tid)
			if err != nil {
				return nil, err
			}
			return channel.ConvertMessagesToHistory(msgs), nil
		}
	} else if app.backend != nil && app.backend.IsRemote() {
		// Remote mode: load via RPC get_history
		backend := app.backend
		cfg.DynamicHistoryLoader = func(channelName, chatID string) ([]channel.HistoryMessage, error) {
			if channelName == "" {
				channelName = "cli"
			}
			return backend.GetHistory(channelName, chatID)
		}
	}

	// Agent session history: load from in-memory interactiveSubAgents (not DB).
	if app.backend != nil {
		backend := app.backend
		cfg.GetActiveProgressFn = func(channelName, chatID string) *channel.CLIProgressPayload {
			return backend.GetActiveProgress(channelName, chatID)
		}
		cfg.AgentSessionDumpFn = func(chatID string) ([]channel.HistoryMessage, error) {
			// Try in-memory first (running sessions)
			dump, ok := backend.GetAgentSessionDumpByFullKey(chatID)
			if ok && len(dump.Messages) > 0 {
				var msgs []channel.HistoryMessage
				for _, m := range dump.Messages {
					msgs = append(msgs, channel.HistoryMessage{
						Role:    m.Role,
						Content: m.Content,
					})
				}
				if len(dump.IterationHistory) > 0 {
					var iters []channel.HistoryIteration
					for _, snap := range dump.IterationHistory {
						var ttools []channel.CLIToolProgress
						for _, t := range snap.Tools {
							ttools = append(ttools, channel.CLIToolProgress{
								Name:      t.Name,
								Label:     t.Label,
								Status:    t.Status,
								Elapsed:   t.ElapsedMS,
								Iteration: snap.Iteration,
								Summary:   t.Summary,
							})
						}
						iters = append(iters, channel.HistoryIteration{
							Iteration: snap.Iteration,
							Thinking:  snap.Thinking,
							Reasoning: snap.Reasoning,
							Tools:     ttools,
						})
					}
					msgs = append(msgs, channel.HistoryMessage{
						Role:       "tool_summary",
						Iterations: iters,
					})
				}
				return msgs, nil
			}
			// Fallback: load from DB (agent tenants have channel="agent", chatID=interactiveKey)
			if cfg.DynamicHistoryLoader != nil {
				return cfg.DynamicHistoryLoader("agent", chatID)
			}
			return nil, nil
		}
	}
}

// wireRemoteMode sets up remote-backend-specific callbacks on the CLI channel:
// inbound message forwarding, outbound routing, progress streaming, background
// tasks, and history trimming — all via RPC.
func wireRemoteMode(s *interactiveState, cliCh *channel.CLIChannel) {
	app := s.app

	cliCh.SetSettingsService(newRemoteSettingsService(app.backend))
	cliCh.SetModelLister(newRemoteModelLister(app.backend))

	// Forward user messages to server instead of local bus
	cliCh.SetSendInboundFn(func(msg bus.InboundMessage) bool {
		clipanic.Go("main.remote.SendInbound", func() {
			if err := app.backend.SendInbound(msg); err != nil {
				log.WithError(err).Warn("Failed to forward message to remote server")
				// Show a toast so the user knows the message failed to send.
				cliCh.SendToast("Failed to send message: "+err.Error(), "✗")
			}
		})
		return true
	})

	// Forward server responses directly to CLI channel
	app.backend.OnOutbound(func(msg bus.OutboundMessage) {
		defer clipanic.Recover("main.remote.OnOutbound", msg, false)
		cliCh.Send(msg)
	})

	// Register OnProgress callback for streaming progress from server
	app.backend.OnProgress(func(p *channel.CLIProgressPayload) {
		defer clipanic.Recover("main.remote.OnProgress", p, false)
		cliCh.SendProgress("cli:"+s.absWorkDir, p)
	})

	// Inject remote bg task callbacks (BgTaskManager is nil in remote mode)
	bgSessionKey := "cli:" + s.absWorkDir
	cliCh.SetBgTaskRemoteCallbacks(
		bgSessionKey,
		func() int { return app.backend.GetBgTaskCount(bgSessionKey) },
		func() []*tools.BackgroundTask {
			tasks, _ := app.backend.ListBgTasks(bgSessionKey)
			if tasks == nil {
				return nil
			}
			result := make([]*tools.BackgroundTask, len(tasks))
			for i, t := range tasks {
				result[i] = &tools.BackgroundTask{
					ID:       t.ID,
					Command:  t.Command,
					Status:   tools.BgTaskStatus(t.Status),
					Output:   t.Output,
					ExitCode: t.ExitCode,
					Error:    t.Error,
				}
				if sa, err := time.Parse(time.RFC3339, t.StartedAt); err == nil {
					result[i].StartedAt = sa
				}
				if t.FinishedAt != "" {
					if fa, err := time.Parse(time.RFC3339, t.FinishedAt); err == nil {
						result[i].FinishedAt = &fa
					}
				}
			}
			return result
		},
		func(taskID string) error { return app.backend.KillBgTask(taskID) },
		func() { app.backend.CleanupCompletedBgTasks(bgSessionKey) },
	)

	// Inject TrimHistoryFn for Ctrl+K session truncation (RPC-backed)
	cliCh.SetTrimHistoryFn(func(cutoff time.Time) error {
		return app.backend.TrimHistory("cli", s.absWorkDir, cutoff)
	})

	cliCh.SetResetTokenStateFn(func() {
		app.backend.ResetTokenState()
	})
}

// wireLocalMode sets up local-backend-specific services: settings, model lister,
// background task manager, approval state, checkpoint store, and history trimming.
// Returns an optional cleanup function that the caller should defer.
func wireLocalMode(s *interactiveState, cliCh *channel.CLIChannel) func() {
	app := s.app

	// Local mode: use local service objects directly
	if ss := app.backend.SettingsService(); ss != nil {
		cliCh.SetSettingsService(ss)
	}
	cliCh.SetModelLister(&cliModelLister{
		factory:  app.backend.LLMFactory(),
		cfg:      app.cfg,
		senderID: cliSenderID,
	})

	// Inject BgTaskManager for background task display
	bgSessionKey := "cli:" + s.absWorkDir
	cliCh.SetBgTaskManager(app.backend.BgTaskManager(), bgSessionKey)

	// Inject ApprovalState for permission control approval dialog
	if state := app.backend.ApprovalState(); state != nil {
		cliCh.SetApprovalState(state)
	}

	// Inject CheckpointState for Ctrl+K rewind file rollback
	var cleanup func()
	checkpointDir := filepath.Join(os.Getenv("HOME"), ".xbot", "checkpoints", "cli-default")
	if cpStore, err := tools.NewCheckpointStore(checkpointDir); err == nil {
		if mgr := app.backend.HookManager(); mgr != nil {
			cpState := hooks.NewCheckpointState(cpStore)
			mgr.RegisterBuiltin(hooks.CheckpointCallback(cpState))
			cliCh.SetCheckpointState(cpState)
			cStore := cpStore
			cleanup = func() { cStore.Cleanup() }
		}
	} else {
		log.WithError(err).Warn("Failed to create checkpoint store")
	}

	// Inject TrimHistoryFn for Ctrl+K session truncation
	if s.tenantID != 0 && s.sessionSvc != nil {
		svc := s.sessionSvc
		tid := s.tenantID
		cliCh.SetTrimHistoryFn(func(cutoff time.Time) error {
			if cutoff.IsZero() {
				return nil
			}
			_, err := svc.PurgeNewerThanOrEqual(tid, cutoff)
			return err
		})
	} else {
		log.WithFields(log.Fields{"tenantID": s.tenantID, "hasSessionSvc": s.sessionSvc != nil, "hasDB": app.db != nil}).Warn("TrimHistoryFn NOT registered — DB truncation will not work")
	}

	// Reset cached token state after rewind to prevent stale compress trigger
	cliCh.SetResetTokenStateFn(func() {
		app.backend.ResetTokenState()
	})

	return cleanup
}

// createAndWireChannel creates the CLIChannel, registers it with the dispatcher,
// and wires mode-specific services (remote or local).
// Returns the channel and an optional cleanup function to defer.
func createAndWireChannel(s *interactiveState, disp *channel.Dispatcher, cliCfg channel.CLIChannelConfig) (*channel.CLIChannel, func()) {
	app := s.app
	cliCh := channel.NewCLIChannel(cliCfg, app.msgBus)
	disp.Register(cliCh)

	var cleanup func()

	// Inject SettingsService for interactive /settings panel
	if app.backend != nil {
		if app.backend.IsRemote() {
			wireRemoteMode(s, cliCh)
		} else {
			cleanup = wireLocalMode(s, cliCh)
		}
	}

	// Apply saved theme at startup.
	// Local mode can read settings immediately; remote mode must wait until backend.Start()
	// establishes the WS/RPC connection, otherwise theme fetch races and the UI keeps default
	// colors until the user re-saves settings.
	if app.backend != nil && !app.backend.IsRemote() {
		if ss := app.backend.SettingsService(); ss != nil {
			if vals, err := ss.GetSettings("cli", "cli_user"); err == nil {
				if t, ok := vals["theme"]; ok && t != "" {
					channel.ApplyTheme(t)
				}
			}
		}
	}

	// Inject channelFinder to enable structured progress events (tool calls, thinking, etc.)
	app.backend.SetDirectSend(disp.SendDirect)
	app.backend.SetChannelFinder(disp.GetChannel)
	if lb, ok := app.backend.(*agent.LocalBackend); ok {
		lb.Agent().SetMessageSender(disp)
		lb.Agent().SetAgentChannelRegistry(
			func(name string, runFn bus.RunFn) error {
				ac := channel.NewAgentChannel(name, runFn)
				if err := ac.Start(); err != nil {
					return fmt.Errorf("start AgentChannel %s: %w", name, err)
				}
				disp.Register(ac)
				return nil
			},
			func(name string) {
				disp.Unregister(name)
			},
		)
	}

	// Inject CLI channel-specific prompt provider
	app.backend.SetChannelPromptProviders(&channel.CliPromptProvider{})

	return cliCh, cleanup
}

// startBackendWithRetry starts the backend, retrying up to 5 times in remote mode
// with exponential backoff. Returns false if startup failed (caller should return
// from main; the function already calls app.Close()).
func startBackendWithRetry(ctx context.Context, app *cliApp) bool {
	if app.backend.IsRemote() {
		fmt.Fprintf(os.Stderr, "\n  Connecting to remote server %s ...\n", app.cfg.CLI.ServerURL)
		const maxRetries = 5
		var connectErr error
		for attempt := 0; attempt < maxRetries; attempt++ {
			connectErr = app.backend.Start(ctx)
			if connectErr == nil {
				fmt.Fprintln(os.Stderr, "  Connected.")
				break
			}
			delay := time.Duration(1<<uint(attempt)) * time.Second
			if attempt < maxRetries-1 {
				fmt.Fprintf(os.Stderr, "  Connection failed: %v\n  Retrying in %vs (%d/%d)...\n", connectErr, delay, attempt+1, maxRetries)
				select {
				case <-ctx.Done():
					fmt.Fprintln(os.Stderr, "\n  Cancelled.")
					app.Close()
					return false
				case <-time.After(delay):
				}
			}
		}
		if connectErr != nil {
			fmt.Fprintf(os.Stderr, "\n  %s\n  Could not connect to server after %d attempts. Please check:\n    1. Server is running (xbot-cli serve)\n    2. Port matches in config (%s)\n    3. Token is correct\n  %s\n\n",
				red("ERROR: "+connectErr.Error()),
				maxRetries,
				config.ConfigFilePath(),
				red("Exiting."))
			app.Close()
			return false
		}
	} else {
		if err := app.backend.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start backend: %v\n", err)
			app.Close()
			return false
		}
	}
	return true
}

// setupRemotePostConnect performs post-connection setup for remote mode:
// loads history, syncs CWD, restores active progress, and wires reconnect
// and connection-state-change callbacks.
func setupRemotePostConnect(ctx context.Context, s *interactiveState, cliCh *channel.CLIChannel) {
	app := s.app

	// Apply theme from remote settings (now that WS/RPC is connected)
	if vals, err := app.backend.GetSettings("cli", "cli_user"); err == nil {
		if t, ok := vals["theme"]; ok && t != "" {
			channel.ApplyTheme(t)
		}
	}

	remoteChatID := s.absWorkDir

	// Auto-set CWD: if connected to a local server (127.0.0.1/localhost),
	// sync the CLI's actual cwd to the server session so the agent uses
	// the correct directory regardless of where the server was started.
	if isLocalServer(app.cfg.CLI.ServerURL) {
		if cwd, err := os.Getwd(); err == nil {
			if err := app.backend.SetCWD("cli", remoteChatID, cwd); err != nil {
				log.WithError(err).WithField("chat_id", remoteChatID).Warn("Failed to sync CWD to server")
			} else {
				log.WithFields(log.Fields{
					"cwd":     cwd,
					"chat_id": remoteChatID,
				}).Info("Synced CLI CWD to local server")
			}
		}
	}

	// Load remote session history
	if history, err := app.backend.GetHistory("cli", remoteChatID); err != nil {
		log.WithError(err).WithField("chat_id", remoteChatID).Warn("Failed to load remote session history")
	} else {
		log.WithFields(log.Fields{"chat_id": remoteChatID, "count": len(history)}).Info("CLI loaded remote history via RPC")
		if len(history) > 0 {
			cliCh.LoadHistory(history)
		}
	}

	// Subscribe to business chatID so Hub routes server-pushed events
	if rb, ok := app.backend.(*agent.RemoteBackend); ok {
		rb.SubscribeChat(remoteChatID)
	}

	// Check if server has an active agent turn for this chat (mid-session reconnect).
	clipanic.Go("main.remote.RestoreActiveProgress", func() {
		progress := app.backend.GetActiveProgress("cli", remoteChatID)
		if progress != nil {
			log.WithFields(log.Fields{
				"chatID":    remoteChatID,
				"phase":     progress.Phase,
				"iteration": progress.Iteration,
				"active":    len(progress.ActiveTools),
				"completed": len(progress.CompletedTools),
				"histLen":   len(progress.IterationHistory),
			}).Info("RestoreActiveProgress: restoring progress snapshot")
			cliCh.RestoreInitialProgress("cli:"+s.absWorkDir, progress)
		} else {
			log.WithField("chatID", remoteChatID).Info("RestoreActiveProgress: no active progress")
		}
	})

	// Wire reconnect callback to reload history on WS reconnect.
	if rb, ok := app.backend.(interface{ OnReconnect(func()) }); ok {
		rb.OnReconnect(func() {
			defer clipanic.Recover("main.remote.OnReconnect", nil, false)
			// Re-subscribe to business chatID for new WS connection.
			if rb, ok := app.backend.(*agent.RemoteBackend); ok {
				rb.SubscribeChat(remoteChatID)
			}
			// Re-sync CWD on reconnect (server may have restarted, losing in-memory cwd)
			if isLocalServer(app.cfg.CLI.ServerURL) {
				if cwd, err := os.Getwd(); err == nil {
					_ = app.backend.SetCWD("cli", remoteChatID, cwd)
				}
			}
			if history, err := app.backend.GetHistory("cli", remoteChatID); err != nil {
				log.WithError(err).Warn("Failed to reload history after reconnect")
			} else {
				cliCh.LoadHistory(history)
			}
			// Re-check processing state after reconnect.
			if app.backend.IsProcessing("cli", remoteChatID) {
				cliCh.SetProcessing(true)
				// Restore active progress snapshot (iteration history + stream state).
				if progress := app.backend.GetActiveProgress("cli", remoteChatID); progress != nil {
					cliCh.RestoreInitialProgress("cli:"+s.absWorkDir, progress)
				}
			} else {
				cliCh.SetProcessing(false)
			}
		})
	}

	// Wire connection state change callback for header bar indicator.
	if csc, ok := app.backend.(interface{ OnConnStateChange(func(string)) }); ok {
		csc.OnConnStateChange(func(state string) {
			defer clipanic.Recover("main.remote.OnConnStateChange", state, false)
			cliCh.SetConnState(state)
		})
	}

	// Background goroutine: periodically refresh agent count/list cache
	// (RPC calls must not happen from BubbleTea event loop → deadlock)
	clipanic.Go("main.remote.RefreshAgentCache", func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if app.backend == nil {
					return
				}
				count := app.backend.CountInteractiveSessions("cli", s.absWorkDir)
				sessions := app.backend.ListInteractiveSessions("cli", s.absWorkDir)
				entries := make([]channel.AgentPanelEntry, len(sessions))
				for i, sess := range sessions {
					entries[i] = channel.AgentPanelEntry{
						Role:       sess.Role,
						Instance:   sess.Instance,
						Running:    sess.Running,
						Background: sess.Background,
						Task:       sess.Task,
						Preview:    sess.Preview,
					}
				}
				app.agentCacheMu.Lock()
				app.agentCacheCount = count
				app.agentCacheList = entries
				app.agentCacheMu.Unlock()
			}
		}
	})
}

// startRemoteCacheRefreshers starts background goroutines that periodically
// refresh the remote values cache. No-op for local mode.
func startRemoteCacheRefreshers(ctx context.Context, app *cliApp) {
	if app.backend == nil || !app.backend.IsRemote() {
		return
	}
	// Initial seed
	app.refreshRemoteValuesCache()
	valuesCtx, valuesCancel := context.WithCancel(context.Background())
	clipanic.Go("main.remote.RefreshValuesCache", func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				app.refreshRemoteValuesCache()
			case <-valuesCtx.Done():
				return
			}
		}
	})
	app.valuesCancel = valuesCancel
}

// installSignalHandler sets up a goroutine that handles SIGINT/SIGTERM by
// gracefully shutting down the backend, waiting for pending saves, and
// stopping the TUI.
func installSignalHandler(ctx context.Context, cancel context.CancelFunc, app *cliApp, cliCh *channel.CLIChannel) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	clipanic.Go("main.signalHandler", func() {
		<-sigCh
		log.Info("Received shutdown signal, shutting down...")
		// Stop backend first (closes WS, unblocks pending RPCs)
		if app.backend != nil {
			app.backend.Stop()
		}
		// Wait for pending saves with timeout (avoid blocking forever on hung RPC)
		done := make(chan struct{})
		clipanic.Go("main.signalHandler.WaitSaves", func() {
			saveWg.Wait()
			close(done)
		})
		select {
		case <-done:
			log.Info("All saves complete")
		case <-time.After(2 * time.Second):
			log.Warn("Timeout waiting for pending saves, forcing shutdown")
		}
		cancel()
		// Quit BubbleTea program so cliCh.Start() returns
		cliCh.Stop()
	})
}

// setupRunnerAndSubscriptions injects the Runner LLM bridge and configures
// subscription managers (local or remote).
func setupRunnerAndSubscriptions(s *interactiveState, cliCh *channel.CLIChannel) {
	app := s.app

	// Runner Bridge: inject LLM client, model list and provider for runner use
	if !app.backend.IsRemote() {
		cliCh.SetRunnerLLM(app.llmClient, func() []string {
			if app.backend != nil {
				return app.backend.LLMFactory().ListModels()
			}
			return nil
		}(), app.cfg.LLM.Provider)
	}

	// Multi-subscription support
	if app.backend.IsRemote() {
		// Remote mode: use RPC-backed subscription manager
		cliCh.SetSubscriptionManager(newRemoteSubscriptionManager(app.backend))
		cliCh.SetLLMSubscriber(newRemoteLLMSubscriber(app.backend))
	} else {
		if err := seedLocalDBSubscriptions(app.backend, app.cfg); err != nil {
			log.WithError(err).Warn("Failed to seed local DB subscriptions")
		}
		loadLLMFromDBSubscription(app.backend, app.cfg)
		cliCh.SetSubscriptionManager(newLocalSubscriptionManager(app.backend))
		cliCh.SetLLMSubscriber(newLocalLLMSubscriber(app.backend))
	}
}

func main() {
	xbotHome := config.XbotHome()
	clipanic.EnableFileLogging(filepath.Join(xbotHome, "logs", "cli-panic.log"))
	defer clipanic.Recover("main.main", nil, true)
	fmt.Printf("xbot CLI %s\n", version.Version)

	// Early-exit sub-commands (install, serve, help)
	if handleSubcommands() {
		return
	}

	flags := parseCLIFlags()

	// Pipe mode: read prompt from stdin if not a terminal
	if flags.prompt == "" && !isatty.IsTerminal(os.Stdin.Fd()) {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			log.WithError(err).Fatal("Failed to read from stdin")
		}
		flags.prompt = strings.TrimSpace(string(data))
	}

	// First-run detection (interactive mode only, passed to TUI for setup panel)
	firstRun := flags.prompt == "" && isFirstRun()

	// Non-interactive mode
	if flags.prompt != "" {
		executeNonInteractive(flags.prompt)
		return
	}

	if flags.newSession {
		fmt.Println("Mode: new session (--new)")
	} else {
		fmt.Println("Mode: resuming last session (use --new for new session)")
	}
	fmt.Println("Starting...")

	if flags.local {
		flags.server = ""
	}
	app := newCLIApp(flags.server, flags.token, flags.local)
	if flags.local {
		fmt.Println("Backend: legacy local mode (--local)")
	} else if app.backend != nil && app.backend.IsRemote() {
		fmt.Println("Backend: remote server mode")
	} else {
		fmt.Println("Backend: local mode")
	}
	defer app.Close()

	disp := channel.NewDispatcher(app.msgBus)
	absWorkDir, _ := filepath.Abs(app.workDir)

	_, isRemote := app.backend.(*agent.RemoteBackend)
	remoteServerURL := ""
	if rb, ok := app.backend.(*agent.RemoteBackend); ok {
		remoteServerURL = rb.ServerURL()
	}

	s := &interactiveState{
		app:             app,
		absWorkDir:      absWorkDir,
		isRemote:        isRemote,
		flags:           flags,
		firstRun:        firstRun,
		remoteServerURL: remoteServerURL,
	}

	// Build channel config, history loaders, and wire services
	var cliCfg channel.CLIChannelConfig
	buildCLIChannelConfig(s, &cliCfg)
	setupHistoryLoaders(s, &cliCfg)
	cliCh, cleanup := createAndWireChannel(s, disp, cliCfg)
	if cleanup != nil {
		defer cleanup()
	}

	// Start backend (with retry for remote mode)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !startBackendWithRetry(ctx, app) {
		return
	}
	clipanic.Go("main.dispatcher.Run", disp.Run)

	// Remote-mode post-connect setup (history, CWD, reconnect, cache refresh)
	if app.backend.IsRemote() {
		setupRemotePostConnect(ctx, s, cliCh)
	}
	startRemoteCacheRefreshers(ctx, app)

	// Send /new message if starting a new session
	if flags.newSession {
		app.msgBus.Inbound <- bus.InboundMessage{
			Channel:    "cli",
			SenderID:   "cli_user",
			ChatID:     absWorkDir,
			ChatType:   "p2p",
			Content:    "/new",
			SenderName: "CLI User",
			Time:       time.Now(),
			RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
		}
	}

	installSignalHandler(ctx, cancel, app, cliCh)
	setupRunnerAndSubscriptions(s, cliCh)

	// --share flag: auto-connect as runner after TUI starts
	if flags.share != "" {
		shareWorkspace := flags.workspace
		if shareWorkspace == "" {
			shareWorkspace = app.workDir
		}
		cliCh.StartWithRunner(flags.share, flags.token, shareWorkspace)
	} else {
		if err := cliCh.Start(); err != nil {
			log.WithError(err).Error("CLI channel error")
			app.Close()
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Adapters: bridge config/types to CLI interfaces
// ---------------------------------------------------------------------------

// cliModelLister wraps LLMFactory + config to implement channel.ModelLister.
// ListAllModels collects models from default LLM + all config subscriptions.
type cliModelLister struct {
	factory  *agent.LLMFactory
	cfg      *config.Config
	senderID string
}

func (l *cliModelLister) ListModels() []string {
	client, _, _, _ := l.factory.GetLLM(l.senderID)
	return client.ListModels()
}

func (l *cliModelLister) ListAllModels() []string {
	seen := make(map[string]bool)
	var result []string
	for _, m := range l.factory.ListModels() {
		if !seen[m] {
			seen[m] = true
			result = append(result, m)
		}
	}
	if svc := l.factory.GetSubscriptionSvc(); svc != nil && l.senderID != "" {
		if subs, err := svc.List(l.senderID); err == nil {
			for _, sub := range subs {
				if sub.Model != "" && !seen[sub.Model] {
					seen[sub.Model] = true
					result = append(result, sub.Model)
				}
			}
			return result
		}
	}
	for _, sub := range l.cfg.Subscriptions {
		if sub.Model != "" && !seen[sub.Model] {
			seen[sub.Model] = true
			result = append(result, sub.Model)
		}
	}
	return result
}

type localSubscriptionManager struct {
	backend agent.AgentBackend
}

func newLocalSubscriptionManager(backend agent.AgentBackend) *localSubscriptionManager {
	return &localSubscriptionManager{backend: backend}
}

func (m *localSubscriptionManager) List(senderID string) ([]channel.Subscription, error) {
	if senderID == "" {
		senderID = cliSenderID
	}
	return m.backend.ListSubscriptions(senderID)
}

func (m *localSubscriptionManager) GetDefault(senderID string) (*channel.Subscription, error) {
	if senderID == "" {
		senderID = cliSenderID
	}
	return m.backend.GetDefaultSubscription(senderID)
}

func (m *localSubscriptionManager) Add(sub *channel.Subscription) error {
	return m.backend.AddSubscription(cliSenderID, *sub)
}

func (m *localSubscriptionManager) Remove(id string) error {
	return m.backend.RemoveSubscription(id)
}

func (m *localSubscriptionManager) SetDefault(id, chatID string) error {
	return m.backend.SetDefaultSubscription(id, chatID)
}

func (m *localSubscriptionManager) SetModel(id, model string) error {
	return m.backend.SetSubscriptionModel(id, model)
}

func (m *localSubscriptionManager) Rename(id, name string) error {
	return m.backend.RenameSubscription(id, name)
}

func (m *localSubscriptionManager) Update(id string, sub *channel.Subscription) error {
	return m.backend.UpdateSubscription(id, *sub)
}

type localLLMSubscriber struct {
	backend agent.AgentBackend
}

func newLocalLLMSubscriber(backend agent.AgentBackend) *localLLMSubscriber {
	return &localLLMSubscriber{backend: backend}
}

func (s *localLLMSubscriber) SwitchSubscription(senderID string, sub *channel.Subscription, chatID string) error {
	if sub == nil {
		return nil
	}
	return s.backend.SetDefaultSubscription(sub.ID, chatID)
}

func (s *localLLMSubscriber) SwitchModel(senderID, model string) {
	if senderID == "" {
		senderID = cliSenderID
	}
	if err := s.backend.SwitchModel(senderID, model); err != nil {
		log.WithError(err).Warn("localLLMSubscriber: SwitchModel failed")
	}
}

func (s *localLLMSubscriber) GetDefaultModel() string {
	return s.backend.GetDefaultModel()
}

// configSubscriptionManager manages CLI subscriptions in config.json (no database).
type configSubscriptionManager struct {
	cfg      *config.Config
	saveFn   func() error           // persists config to disk
	tierSync func(config.LLMConfig) // called after subscription switch to re-sync tier models
}

func newConfigSubscriptionManager(cfg *config.Config, saveFn func() error, tierSync func(config.LLMConfig)) *configSubscriptionManager {
	return &configSubscriptionManager{cfg: cfg, saveFn: saveFn, tierSync: tierSync}
}

func (m *configSubscriptionManager) List(_ string) ([]channel.Subscription, error) {
	result := make([]channel.Subscription, len(m.cfg.Subscriptions))
	for i, s := range m.cfg.Subscriptions {
		result[i] = channel.Subscription{
			ID:       s.ID,
			Name:     s.Name,
			Provider: s.Provider,
			BaseURL:  s.BaseURL,
			APIKey:   s.APIKey,
			Model:    s.Model,
			Active:   s.Active,
			// MaxOutputTokens/ThinkingMode not available from config seeds
		}
	}
	return result, nil
}

func (m *configSubscriptionManager) GetDefault(_ string) (*channel.Subscription, error) {
	for _, s := range m.cfg.Subscriptions {
		if s.Active {
			return &channel.Subscription{
				ID:       s.ID,
				Name:     s.Name,
				Provider: s.Provider,
				Model:    s.Model,
				Active:   true,
			}, nil
		}
	}
	return nil, nil
}

func (m *configSubscriptionManager) Add(sub *channel.Subscription) error {
	m.cfg.Subscriptions = append(m.cfg.Subscriptions, config.SubscriptionConfig{
		ID:       sub.ID,
		Name:     sub.Name,
		Provider: sub.Provider,
		BaseURL:  sub.BaseURL,
		APIKey:   sub.APIKey,
		Model:    sub.Model,
		Active:   sub.Active,
	})
	return m.saveFn()
}

func (m *configSubscriptionManager) Remove(id string) error {
	filtered := m.cfg.Subscriptions[:0]
	for _, s := range m.cfg.Subscriptions {
		if s.ID != id {
			filtered = append(filtered, s)
		}
	}
	if len(filtered) == len(m.cfg.Subscriptions) {
		return fmt.Errorf("subscription %s not found", id)
	}
	m.cfg.Subscriptions = filtered
	return m.saveFn()
}

func (m *configSubscriptionManager) SetDefault(id, chatID string) error {
	found := false
	for i := range m.cfg.Subscriptions {
		if m.cfg.Subscriptions[i].ID == id {
			m.cfg.Subscriptions[i].Active = true
			found = true
		} else {
			m.cfg.Subscriptions[i].Active = false
		}
	}
	if !found {
		return fmt.Errorf("subscription %s not found", id)
	}
	// Derive cfg.LLM from new active subscription
	syncLLMFromActiveSub(m.cfg)
	// Re-sync model tiers (tier fields are global, not per-subscription)
	if m.tierSync != nil {
		m.tierSync(m.cfg.LLM)
	}
	return m.saveFn()
}

func (m *configSubscriptionManager) SetModel(id, model string) error {
	for i := range m.cfg.Subscriptions {
		if m.cfg.Subscriptions[i].ID == id {
			m.cfg.Subscriptions[i].Model = model
			// If modifying active subscription, sync cfg.LLM
			if m.cfg.Subscriptions[i].Active {
				syncLLMFromActiveSub(m.cfg)
				if m.tierSync != nil {
					m.tierSync(m.cfg.LLM)
				}
			}
			return m.saveFn()
		}
	}
	return fmt.Errorf("subscription %s not found", id)
}

func (m *configSubscriptionManager) Rename(id, name string) error {
	for i := range m.cfg.Subscriptions {
		if m.cfg.Subscriptions[i].ID == id {
			m.cfg.Subscriptions[i].Name = name
			return m.saveFn()
		}
	}
	return fmt.Errorf("subscription %s not found", id)
}

func (m *configSubscriptionManager) Update(id string, sub *channel.Subscription) error {
	for i := range m.cfg.Subscriptions {
		if m.cfg.Subscriptions[i].ID == id {
			m.cfg.Subscriptions[i].Name = sub.Name
			m.cfg.Subscriptions[i].Provider = sub.Provider
			m.cfg.Subscriptions[i].BaseURL = sub.BaseURL
			// Never overwrite with a masked API key from server RPC.
			if !strings.HasSuffix(sub.APIKey, "****") || len(sub.APIKey) > 20 {
				m.cfg.Subscriptions[i].APIKey = sub.APIKey
			}
			m.cfg.Subscriptions[i].Model = sub.Model
			// If modifying active subscription, sync cfg.LLM
			if m.cfg.Subscriptions[i].Active {
				syncLLMFromActiveSub(m.cfg)
				if m.tierSync != nil {
					m.tierSync(m.cfg.LLM)
				}
			}
			return m.saveFn()
		}
	}
	return fmt.Errorf("subscription %s not found", id)
}

// syncLLMFromActiveSub derives cfg.LLM.* from the active config subscription.
// It is still used by legacy config-backed helper paths and migration logic.
func syncLLMFromActiveSub(cfg *config.Config) {
	for _, sc := range cfg.Subscriptions {
		if sc.Active {
			cfg.LLM.Provider = sc.Provider
			cfg.LLM.BaseURL = sc.BaseURL
			cfg.LLM.APIKey = sc.APIKey
			cfg.LLM.Model = sc.Model
			cfg.LLM.MaxOutputTokens = sc.MaxOutputTokens
			cfg.LLM.ThinkingMode = sc.ThinkingMode
			return
		}
	}
}

// red wraps text in ANSI red for terminal error output.
func red(s string) string {
	return "\033[0;31m" + s + "\033[0m"
}

// executeNonInteractive runs non-interactive mode: executes a single prompt and outputs to stdout.
func executeNonInteractive(prompt string) {
	app := newCLIApp("", "", true) // non-interactive always uses local backend
	defer app.Close()

	absWorkDir, _ := filepath.Abs(app.workDir)

	nonIntCh := channel.NewNonInteractiveChannel(app.msgBus)
	disp := channel.NewDispatcher(app.msgBus)
	disp.Register(nonIntCh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = app.backend.Start(ctx)
	go disp.Run()

	app.msgBus.Inbound <- bus.InboundMessage{
		Channel:    "cli",
		SenderID:   "cli_user",
		ChatID:     absWorkDir,
		ChatType:   "p2p",
		Content:    prompt,
		SenderName: "CLI User",
		Time:       time.Now(),
		RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
	}

	nonIntCh.WaitDone()
}

// setupLogger configures the logger (CLI mode: file-only output, no terminal TUI interference).
// Logs are written to the global xbotHome/logs directory.
func setupLogger(cfg config.LogConfig, xbotHome string) error {
	logDir := filepath.Join(xbotHome, "logs")
	return log.Setup(log.SetupConfig{
		Level:    cfg.Level,
		Format:   cfg.Format,
		LogDir:   logDir,
		MaxAge:   7,
		FileOnly: true,
	})
}

// createLLM creates an LLM client with retry, exponential backoff, and jitter.
func createLLM(cfg config.LLMConfig, retryCfg llm.RetryConfig) (llm.LLM, error) {
	var inner llm.LLM
	switch cfg.Provider {
	case "openai":
		inner = llm.NewOpenAILLM(llm.OpenAIConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: cfg.Model,
			MaxTokens:    cfg.MaxOutputTokens,
			OnModelsLoadError: func(err error) {
				select {
				case channel.ModelsLoadErrorCh() <- err:
				default:
				}
			},
		})
	case "anthropic":
		inner = llm.NewAnthropicLLM(llm.AnthropicConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: cfg.Model,
			MaxTokens:    cfg.MaxOutputTokens,
		})
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.Provider)
	}
	return llm.NewRetryLLM(inner, retryCfg), nil
}

// ---------------------------------------------------------------------------
// Remote backend adapters — implement CLI interfaces via RPC
// ---------------------------------------------------------------------------

// remoteSettingsService implements channel.SettingsService via RPC.
type remoteSettingsService struct {
	backend agent.AgentBackend
}

func newRemoteSettingsService(backend agent.AgentBackend) *remoteSettingsService {
	return &remoteSettingsService{backend: backend}
}

func (s *remoteSettingsService) GetSettings(namespace, senderID string) (map[string]string, error) {
	return s.backend.GetSettings(namespace, senderID)
}

func (s *remoteSettingsService) SetSetting(namespace, senderID, key, value string) error {
	return s.backend.SetSetting(namespace, senderID, key, value)
}

// remoteModelLister implements channel.ModelLister via RPC.
type remoteModelLister struct {
	backend agent.AgentBackend
}

func newRemoteModelLister(backend agent.AgentBackend) *remoteModelLister {
	return &remoteModelLister{backend: backend}
}

func (l *remoteModelLister) ListModels() []string {
	return l.backend.ListModels()
}

func (l *remoteModelLister) ListAllModels() []string {
	return l.backend.ListAllModels()
}

// remoteSubscriptionManager implements channel.SubscriptionManager via RPC.
type remoteSubscriptionManager struct {
	backend agent.AgentBackend
}

func newRemoteSubscriptionManager(backend agent.AgentBackend) *remoteSubscriptionManager {
	return &remoteSubscriptionManager{backend: backend}
}

func (m *remoteSubscriptionManager) List(senderID string) ([]channel.Subscription, error) {
	return m.backend.ListSubscriptions(senderID)
}

func (m *remoteSubscriptionManager) GetDefault(senderID string) (*channel.Subscription, error) {
	return m.backend.GetDefaultSubscription(senderID)
}

func (m *remoteSubscriptionManager) Add(sub *channel.Subscription) error {
	return m.backend.AddSubscription("cli_user", *sub)
}

func (m *remoteSubscriptionManager) Remove(id string) error {
	return m.backend.RemoveSubscription(id)
}

func (m *remoteSubscriptionManager) SetDefault(id, chatID string) error {
	return m.backend.SetDefaultSubscription(id, chatID)
}

func (m *remoteSubscriptionManager) SetModel(id, model string) error {
	return m.backend.SetSubscriptionModel(id, model)
}

func (m *remoteSubscriptionManager) Rename(id, name string) error {
	return m.backend.RenameSubscription(id, name)
}

func (m *remoteSubscriptionManager) Update(id string, sub *channel.Subscription) error {
	return m.backend.UpdateSubscription(id, *sub)
}

// remoteLLMSubscriber implements channel.LLMSubscriber via RPC.
type remoteLLMSubscriber struct {
	backend agent.AgentBackend
}

func newRemoteLLMSubscriber(backend agent.AgentBackend) *remoteLLMSubscriber {
	return &remoteLLMSubscriber{backend: backend}
}

func (s *remoteLLMSubscriber) SwitchSubscription(senderID string, sub *channel.Subscription, chatID string) error {
	if sub == nil {
		return nil
	}
	// Server-side set_default_subscription invalidates the LLM cache so
	// the next GetLLM call picks up the new subscription's provider/model/credentials.
	// Do NOT call SetUserModel here — it would create a conflicting LLMConfig
	// that overrides the subscription's model.
	return s.backend.SetDefaultSubscription(sub.ID, chatID)
}

func (s *remoteLLMSubscriber) SwitchModel(senderID, model string) {
	if err := s.backend.SwitchModel(senderID, model); err != nil {
		log.WithError(err).Warn("remoteLLMSubscriber: SwitchModel RPC failed")
	}
}

func (s *remoteLLMSubscriber) GetDefaultModel() string {
	return s.backend.GetDefaultModel()
}

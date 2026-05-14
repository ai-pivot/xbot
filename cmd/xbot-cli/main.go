// xbot CLI entry point
// Standalone terminal-based chat interface
//
// Usage:
//   xbot-cli               恢复上次会话（默认）
//   xbot-cli --resume      恢复会话并显示当前状态
//   xbot-cli --new              开始新会话
//   xbot-cli --new-session      开始新会话（同 --new）
//   xbot-cli --max-context N    指定最大上下文 token 数
//   xbot-cli --max-tokens N     指定最大输出 token 数
//   xbot-cli <prompt>      非交互模式执行单次 prompt
//   xbot-cli -p <prompt>   非交互模式执行单次 prompt
//   echo "hello" | xbot-cli  管道模式

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
	"xbot/bus"
	"xbot/channel"
	"xbot/clipanic"
	"xbot/config"
	"xbot/llm"
	log "xbot/logger"
	"xbot/pprof"
	"xbot/protocol"
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
// refreshRemoteValuesCache fetches current settings from the backend
// and updates the local cache. Called from a background goroutine — never from
// the BubbleTea Update loop (which would freeze the TUI on slow transport).
// configLayoutValue reads a single layout setting from the local config.json.
// Used as fallback when RPC fails on first refreshRemoteValuesCache call.
// saveLayoutToConfig writes layout settings (sidebar_width, theme, etc.)
// directly to config.json. These keys are not in the Config struct and
// are preserved by SaveToFile's deep merge, but we must write them explicitly.
func saveLayoutToConfig(vals map[string]string) {
	path := config.ConfigFilePath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	for k, v := range vals {
		if v != "" {
			m[k] = v
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0644)
}

func configLayoutValue(key string) string {
	raw, err := os.ReadFile(config.ConfigFilePath())
	if err != nil {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		if n, ok := v.(float64); ok {
			return strconv.Itoa(int(n))
		}
	}
	return ""
}

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
		vals["max_output_tokens"] = fmt.Sprintf("%d", sub.MaxOutputTokens)
		log.Debugf("[Settings] refreshRemoteValuesCache: sub=%s max_output_tokens=%d", func() string {
			if sub != nil {
				return sub.ID
			}
			return "<nil>"
		}(), func() int {
			if sub != nil {
				return sub.MaxOutputTokens
			}
			return -1
		}())
		if sub.ThinkingMode != "" {
			vals["thinking_mode"] = sub.ThinkingMode
		}
	}
	vals["context_mode"] = app.backend.GetContextMode()
	// ScopeGlobal keys: always override DB values with config (single source of truth).
	// Old versions may have left stale values in user_settings DB; these must not
	// override the config.json value. See Issue #18.
	vals["sandbox_mode"] = func() string {
		if app.cfg.Sandbox.Mode != "" {
			return app.cfg.Sandbox.Mode
		}
		return "none"
	}()
	vals["memory_provider"] = func() string {
		if app.cfg.Agent.MemoryProvider != "" {
			return app.cfg.Agent.MemoryProvider
		}
		return "flat"
	}()
	vals["compression_threshold"] = func() string {
		if app.cfg.Agent.CompressionThreshold > 0 {
			return fmt.Sprintf("%g", app.cfg.Agent.CompressionThreshold)
		}
		return "0.9"
	}()
	// ScopeUser keys: tavily_api_key (user_settings → config.json fallback)
	if _, ok := vals["tavily_api_key"]; !ok {
		vals["tavily_api_key"] = app.cfg.TavilyAPIKey
	}
	// ScopeUser keys (max_iterations, max_concurrency, max_context_tokens):
	// Primary source is the user_settings DB (written by /set). Only fallback
	// to config.json when DB has no value (first-run or never changed).
	if _, ok := vals["max_iterations"]; !ok {
		vals["max_iterations"] = func() string {
			if app.cfg.Agent.MaxIterations > 0 {
				return fmt.Sprintf("%d", app.cfg.Agent.MaxIterations)
			}
			return "30"
		}()
	}
	if _, ok := vals["max_concurrency"]; !ok {
		vals["max_concurrency"] = func() string {
			if app.cfg.Agent.MaxConcurrency > 0 {
				return fmt.Sprintf("%d", app.cfg.Agent.MaxConcurrency)
			}
			return "3"
		}()
	}
	if _, ok := vals["max_context_tokens"]; !ok {
		vals["max_context_tokens"] = func() string {
			if app.cfg.Agent.MaxContextTokens > 0 {
				return fmt.Sprintf("%d", app.cfg.Agent.MaxContextTokens)
			}
			return "200000"
		}()
	}
	app.valuesCacheMu.Lock()
	app.valuesCache = vals
	app.valuesCacheMu.Unlock()

	// Merge layout keys from local config.json if missing (RPC may fail on first call)
	layoutKeys := []string{"sidebar_width", "sidebar_enabled", "sidebar_position", "chat_max_width", "chat_center", "layout_mode"}
	for _, k := range layoutKeys {
		if _, ok := vals[k]; ok {
			continue
		}
		if v := configLayoutValue(k); v != "" {
			vals[k] = v
		}
	}

	if app.cliCh != nil {
		app.cliCh.SyncLayoutSettings(vals)
	}

	// Sync tier model mappings via RPC so SubAgent model resolution
	// works correctly (tier models are user-scoped, persisted in DB).
	if app.backend != nil {
		llmCfg := app.cfg.LLM // start from current config
		if v, ok := vals["vanguard_model"]; ok {
			llmCfg.VanguardModel = v
		}
		if v, ok := vals["balance_model"]; ok {
			llmCfg.BalanceModel = v
		}
		if v, ok := vals["swift_model"]; ok {
			llmCfg.SwiftModel = v
		}
		app.cfg.LLM = llmCfg
		app.backend.SetModelTiers(llmCfg)
		app.backend.SetModelContexts(app.cfg.Agent.ModelContexts)
	}
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
	// Agent settings: always write back (max_iterations, max_concurrency, etc.)
	merged.Agent = cfg.Agent

	// LLM tier model mappings: always write back (vanguard/balance/swift models).
	// These are global preferences, not subscription credentials.
	merged.LLM.VanguardModel = cfg.LLM.VanguardModel
	merged.LLM.BalanceModel = cfg.LLM.BalanceModel
	merged.LLM.SwiftModel = cfg.LLM.SwiftModel

	// LLM credentials (Provider, BaseURL, APIKey, Model, MaxOutputTokens, ThinkingMode):
	// Single source of truth is user_llm_subscriptions DB, NOT config.json.
	// Only write credentials to config.json if there are no DB subscriptions
	// (first-run path where config.json is the only data source).
	// Guard: only write if credentials are actually present (avoid zero-value overwrite).
	if len(merged.Subscriptions) == 0 && cfg.LLM.Provider != "" {
		merged.LLM.Provider = cfg.LLM.Provider
		merged.LLM.BaseURL = cfg.LLM.BaseURL
		merged.LLM.APIKey = cfg.LLM.APIKey
		merged.LLM.Model = cfg.LLM.Model
		merged.LLM.MaxOutputTokens = cfg.LLM.MaxOutputTokens
		merged.LLM.ThinkingMode = cfg.LLM.ThinkingMode
	}

	// CLI remote connection settings: only write if non-empty (e.g. first setup)
	if cfg.CLI.ServerURL != "" || cfg.CLI.Token != "" {
		merged.CLI = cfg.CLI
	}
	// Persist setup completion flag so isFirstRun() won't re-trigger on restart.
	if cfg.CLISetupCompleted {
		merged.CLISetupCompleted = true
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
	if backend == nil {
		return nil
	}
	sourceSubs := localSeedSourceSubscriptions(cfg)
	if len(sourceSubs) == 0 {
		return nil
	}
	existing, err := backend.ListSubscriptions(cliSenderID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	hasActive := hasActiveSeedSubscription(sourceSubs)
	for i, sc := range sourceSubs {
		if err := backend.AddSubscription(cliSenderID, protocol.Subscription{
			ID:              sc.ID,
			Name:            sc.Name,
			Provider:        sc.Provider,
			BaseURL:         sc.BaseURL,
			APIKey:          sc.APIKey,
			Model:           sc.Model,
			MaxOutputTokens: sc.MaxOutputTokens,
			ThinkingMode:    sc.ThinkingMode,
			Active:          sc.Active || (i == 0 && !hasActive),
		}); err != nil {
			return err
		}
	}
	return nil
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
		subID := ""
		maxTok := -1
		if sub != nil {
			subID = sub.ID
			maxTok = sub.MaxOutputTokens
		}
		log.Warnf("[Settings] GetDefaultSubscription: id=%s max_output_tokens=%d err=%v", subID, maxTok, err)
	} else {
		log.Debugf("[Settings] GetDefaultSubscription: id=%s max_output_tokens=%d base_url=%q", sub.ID, sub.MaxOutputTokens, sub.BaseURL)
	}
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
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
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
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			log.Debugf("[Settings] Setting max_output_tokens: %d (from value %q)", n, v)
			sub.MaxOutputTokens = n
		} else {
			log.Warnf("[Settings] Invalid max_output_tokens value %q: err=%v", v, err)
		}
	}
	if v, ok := values["thinking_mode"]; ok {
		sub.ThinkingMode = v
	}

	// Preserve PerModelConfigs — never overwrite with nil (would destroy per-model overrides
	// written by saveSettings or sub panel). Merge existing values on top.
	if sub.PerModelConfigs == nil {
		sub.PerModelConfigs = make(map[string]channel.PerModelConfig)
	}

	log.Debugf("[Settings] UpdateSubscription: id=%s max_output_tokens=%d thinking_mode=%q", sub.ID, sub.MaxOutputTokens, sub.ThinkingMode)
	return backend.UpdateSubscription(sub.ID, *sub)
}

// cliApp 封装 CLI 的公共初始化逻辑，供交互和非交互模式共享。
type cliApp struct {
	cfg       *config.Config
	llmClient llm.LLM
	msgBus    *bus.MessageBus
	db        *sqlite.DB
	backend   *agent.Backend
	disp      *channel.Dispatcher
	workDir   string
	xbotHome  string

	// Remote-mode async cache for agent info (avoid RPC from event loop → deadlock)
	agentCacheMu      sync.RWMutex
	agentCacheCount   int
	agentCacheList    []channel.AgentPanelEntry
	sessionsCacheList []channel.SessionPanelEntry

	// Remote-mode async cache for GetCurrentValues (avoid RPC from Update loop → 30s freeze)
	valuesCacheMu sync.RWMutex
	valuesCache   map[string]string

	// Remote-mode background goroutine cancel
	valuesCancel context.CancelFunc

	cliCh *channel.CLIChannel // for syncing layout settings after cache refresh
}

// isFirstRun 检测是否是首次运行（config.json 不存在或 API Key 未配置，且未完成 CLI setup）
func isFirstRun() bool {
	configPath := config.ConfigFilePath()
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return true
	}
	cfg := config.LoadFromFile(configPath)
	if cfg == nil {
		return true
	}
	// If setup wizard was already completed, don't show it again.
	// This flag is set when the user saves LLM credentials via the setup/settings panel.
	if cfg.CLISetupCompleted {
		return false
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

// newCLIApp 执行公共初始化：加载配置、创建 Backend。
// If serverURL is non-empty, creates a RemoteBackend (agent runs on server).
// Otherwise creates a LocalBackend (agent runs in-process).
// buildPaletteExternalCommands collects commands from skills, plugins, and user
// custom commands (~/.xbot/commands/*.md). Called each time the palette opens.
func (a *cliApp) buildPaletteExternalCommands() []channel.PaletteExternalCommand {
	var cmds []channel.PaletteExternalCommand
	home, _ := os.UserHomeDir()
	xbotDir := home + "/.xbot"

	// 1. Skills from ~/.xbot/skills/
	if entries, err := os.ReadDir(xbotDir + "/skills"); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || name == "skill-creator" {
				continue
			}
			cmds = append(cmds, channel.PaletteExternalCommand{
				Title:       "Skill: " + name,
				Description: "activate /" + name + " skill",
				Category:    channel.PaletteCategorySkills,
				Content:     "/" + name + " ",
			})
		}
	}

	// 2. Plugin commands from loaded plugins
	// TODO: migrate palette plugin commands to RPC

	// 3. User custom commands from ~/.xbot/commands/*.md (crush-style)
	if entries, err := os.ReadDir(xbotDir + "/commands"); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
			content, err := os.ReadFile(xbotDir + "/commands/" + e.Name())
			if err != nil {
				continue
			}
			cmds = append(cmds, channel.PaletteExternalCommand{
				Title:       name,
				Description: "custom command",
				Category:    channel.PaletteCategoryUser,
				Content:     string(content),
				Send:        true,
			})
		}
	}

	// 4. SubAgent roles from ~/.xbot/agents/
	if entries, err := os.ReadDir(xbotDir + "/agents"); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			cmds = append(cmds, channel.PaletteExternalCommand{
				Title:       "Agent: " + name,
				Description: "spawn " + name + " SubAgent",
				Category:    channel.PaletteCategoryAgents,
				Content:     "/agent " + name + " ",
			})
		}
	}

	return cmds
}

func newCLIApp(serverURL, token string, forceLocal bool, maxContextTokens, maxOutputTokens int) *cliApp {
	cfg := config.Load()

	// If --server was not specified on the command line, fall back to config.
	// --local disables this fallback and forces in-process transport.
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

	// Apply CLI flag overrides (after subscription loading so they take precedence).
	if maxContextTokens > 0 {
		cfg.Agent.MaxContextTokens = maxContextTokens
		log.WithField("max_context_tokens", maxContextTokens).Info("CLI --max-context override applied")
	}
	if maxOutputTokens > 0 {
		cfg.LLM.MaxOutputTokens = maxOutputTokens
		log.WithField("max_output_tokens", maxOutputTokens).Info("CLI --max-tokens override applied")
	}

	llmClient, err := createLLM(cfg.LLM, llm.RetryConfig{
		Attempts: uint(cfg.Agent.LLMRetryAttempts),
		Delay:    time.Duration(cfg.Agent.LLMRetryDelay),
		MaxDelay: time.Duration(cfg.Agent.LLMRetryMaxDelay),
		Timeout:  time.Duration(cfg.Agent.LLMRetryTimeout),
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to create LLM client")
	}
	log.WithFields(log.Fields{
		"provider": cfg.LLM.Provider,
		"model":    cfg.LLM.Model,
	}).Info("LLM client created")

	tools.InitSandbox(cfg.Sandbox, workDir)

	var backend *agent.Backend
	disp := channel.NewDispatcher(msgBus)
	if serverURL != "" {
		// Remote mode: agent loop runs on the server
		log.WithField("server", serverURL).Info("Using remote backend")
		backend = agent.NewRemoteBackend(agent.RemoteTransportConfig{
			ServerURL: serverURL,
			Token:     token,
		})
	} else {
		// Local mode: agent loop runs in-process via Go channel transport.
		// Architecturally identical to remote mode — just uses channels instead of WebSocket.
		bc := agent.BackendConfig{
			Cfg:             cfg,
			LLM:             llmClient,
			Bus:             msgBus,
			DBPath:          dbPath,
			WorkDir:         workDir,
			XbotHome:        xbotHome,
			DirectWorkspace: workDir,
		}
		backend, err = agent.NewChannelBackend(agent.ChannelTransportConfig{
			AgentConfig: bc.AgentConfig(),
			Dispatcher:  disp,
			InitTools:   []tools.Tool{tools.NewWebSearchTool(cfg.TavilyAPIKey)},
			LLMSetup: agent.LLMSetupConfig{
				Tiers:     cfg.LLM,
				Contexts:  cfg.Agent.ModelContexts,
				MaxTokens: maxOutputTokens,
				Retry: llm.RetryConfig{
					Attempts: uint(cfg.Agent.LLMRetryAttempts),
					Delay:    time.Duration(cfg.Agent.LLMRetryDelay),
					MaxDelay: time.Duration(cfg.Agent.LLMRetryMaxDelay),
					Timeout:  time.Duration(cfg.Agent.LLMRetryTimeout),
				},
			},
		})
		if err != nil {
			log.WithError(err).Fatal("Failed to create local backend")
		}
	}

	return &cliApp{
		cfg:       cfg,
		llmClient: llmClient,
		msgBus:    msgBus,
		db:        db,
		backend:   backend,
		disp:      disp,
		workDir:   workDir,
		xbotHome:  xbotHome,
	}
}

// Close 释放资源。
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

// ensureCJKWidth is now a no-op.
//
// Previously, in CJK locales we set RUNEWIDTH_EASTASIAN=1 to align go-runewidth
// with ansi.StringWidth. However, RUNEWIDTH_EASTASIAN=1 makes ambiguous-width
// characters (│─╭▋● etc.) report width=2, while most terminals (foot, gnome-terminal,
// iTerm2, Windows Terminal) render them as width=1 when using non-CJK fonts.
// This width mismatch causes the entire TUI layout to shift and wrap incorrectly.
//
// The original CJK truncation bug (#14) was fixed by switching from go-runewidth
// to ansi.StringWidth in truncateToWidth/hardWrapRunes. lipgloss v2 also uses
// the ansi package internally, so both paths agree on width=1 for ambiguous chars
// without needing RUNEWIDTH_EASTASIAN=1.
//
// Users who actually have CJK fonts that render ambiguous chars as double-width
// can opt in by setting RUNEWIDTH_EASTASIAN=1 in their shell profile.
func ensureCJKWidth() {}

func main() {
	// CJK width: ensureCJKWidth is now a no-op (see comment above).
	// Kept as a call site for forward compatibility if we need to re-enable
	// locale-aware width detection in the future.
	ensureCJKWidth()

	xbotHome := config.XbotHome()
	clipanic.EnableFileLogging(filepath.Join(xbotHome, "logs", "cli-panic.log"))
	defer clipanic.Recover("main.main", nil, true)
	fmt.Printf("xbot CLI %s\n", version.Version)

	// pluginWidgetSyncFn bridges SetCWDFn (inside if app.backend != nil) and
	// cliCh.SyncPluginWidgetChatID (inside if app.backend.IsRemote()).
	// Both are in different scopes, so we use a closure variable at main scope.
	var pluginWidgetSyncFn func(string)

	printHelp := func() {
		fmt.Println("Usage: xbot-cli [options] [prompt]")
		fmt.Println()
		fmt.Println("Modes:")
		fmt.Println("  default             Auto mode: use remote server if cli.server_url is configured")
		fmt.Println("  --local             Force in-process transport (Go channels, no server needed)")
		fmt.Println("  --server <ws-url>   Force remote mode and connect to server")
		fmt.Println("  serve               Run server mode in the same binary")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  --help, -h          Show this help")
		fmt.Println("  --new, --new-session  Start a new isolated session (auto-named)")
		fmt.Println("  --resume            Resume last session (default)")
		fmt.Println("  --max-context N     Override max context tokens (e.g. 128000)")
		fmt.Println("  --max-tokens N      Override max output tokens (e.g. 8192)")
		fmt.Println("  -p <prompt>         Non-interactive single prompt")
		fmt.Println("  --token <token>     Token for remote server")
		fmt.Println("  --workspace <path>  Override workspace")
		fmt.Println("  --sidebar-width N  Set sidebar width (16-40, default 20)")
		fmt.Println("  --no-sidebar       Disable sidebar")
	}

	// Sub-commands: handled before flag parsing.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "install":
			fmt.Println("install 子命令已不再主推，请使用 scripts/install.sh")
			fmt.Println("例如: curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash")
			return
		case "serve":
			if err := serverapp.Run(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			return
		case "--help", "-h", "help":
			printHelp()
			return
		}
	}

	// 解析命令行标志
	prompt := ""
	newSession := false
	var (
		flagServer       string        // --server ws://host:port (RemoteBackend: agent runs on server)
		flagShare        string        // --share ws://host:port/ws/userID (Runner mode: tools run locally)
		flagToken        string        // --token xxx
		flagWorkspace    string        // --workspace /path (overrides config)
		flagLocal        bool          // --local force in-process transport (Go channels)
		flagDebug        bool          // --debug enable UI capture + key injection via SIGUSR1
		flagDebugInput   string        // --debug-input "1,enter,ctrl+c" auto-inject key sequence after startup
		flagDebugCapMs   int           // --debug-capture-ms 200  UI capture interval in ms (default 1000)
		flagPProf        bool          // --pprof enable pprof HTTP server
		flagPProfPort    int           // --pprof-port 6060
		pprofServer      *pprof.Server // initialized if --pprof flag is set
		flagSidebarWidth int           // --sidebar-width 25 (range 16-40)
		flagNoSidebar    bool          // --no-sidebar
		flagMaxContext   int           // --max-context N (override max context tokens)
		flagMaxTokens    int           // --max-tokens N (override max output tokens)
	)
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--resume":
			// 保留兼容性，行为与默认相同
		case "--new", "--new-session":
			newSession = true
		case "-p":
			if len(os.Args) > i+1 {
				prompt = os.Args[i+1]
			}
		case "--server":
			if len(os.Args) > i+1 {
				flagServer = os.Args[i+1]
				i++
			}
		case "--local":
			flagLocal = true
		case "--debug":
			flagDebug = true
		case "--debug-input":
			if len(os.Args) > i+1 {
				flagDebugInput = os.Args[i+1]
				i++
				flagDebug = true // auto-enable debug mode
			}
		case "--debug-capture-ms":
			if len(os.Args) > i+1 {
				n, err := strconv.Atoi(os.Args[i+1])
				if err == nil && n >= 50 {
					flagDebugCapMs = n
				}
				i++
			}
		case "--pprof":
			flagPProf = true
		case "--pprof-port":
			if len(os.Args) > i+1 {
				n, err := strconv.Atoi(os.Args[i+1])
				if err == nil && n > 0 {
					flagPProfPort = n
				}
				i++
			}
		case "--help", "-h":
			printHelp()
			return
		case "--share":
			if len(os.Args) > i+1 {
				flagShare = os.Args[i+1]
				i++
			}
		case "--token":
			if len(os.Args) > i+1 {
				flagToken = os.Args[i+1]
				i++
			}
		case "--workspace":
			if len(os.Args) > i+1 {
				flagWorkspace = os.Args[i+1]
				i++
			}
		case "--sidebar-width":
			if len(os.Args) > i+1 {
				if n, err := strconv.Atoi(os.Args[i+1]); err == nil && n >= 16 && n <= 40 {
					flagSidebarWidth = n
				}
				i++
			}
		case "--no-sidebar":
			flagNoSidebar = true
		case "--max-context":
			if len(os.Args) > i+1 {
				if n, err := strconv.Atoi(os.Args[i+1]); err == nil && n > 0 {
					flagMaxContext = n
				}
				i++
			}
		case "--max-tokens":
			if len(os.Args) > i+1 {
				if n, err := strconv.Atoi(os.Args[i+1]); err == nil && n > 0 {
					flagMaxTokens = n
				}
				i++
			}
		default:
			if !strings.HasPrefix(os.Args[i], "-") {
				prompt = os.Args[i]
			}
		}
	}
	if prompt == "" && !isatty.IsTerminal(os.Stdin.Fd()) {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			log.WithError(err).Fatal("Failed to read from stdin")
		}
		prompt = strings.TrimSpace(string(data))
	}

	// 首次运行检测（仅在交互模式下，传给 TUI 做 setup panel）
	// Refined AFTER newCLIApp so we can also check DB subscriptions, not just config.json.
	firstRun := prompt == "" && isFirstRun()

	// 非交互模式
	if prompt != "" {
		executeNonInteractive(prompt, flagMaxContext, flagMaxTokens)
		return
	}

	if newSession {
		fmt.Println("Mode: new session (--new / --new-session)")
	} else {
		fmt.Println("Mode: resuming last session (use --new or --new-session for new session)")
	}
	fmt.Println("Starting...")

	if flagLocal {
		flagServer = ""
	}
	app := newCLIApp(flagServer, flagToken, flagLocal, flagMaxContext, flagMaxTokens)
	if flagLocal {
		fmt.Println("Backend: in-process (channel transport)")
	} else if app.backend != nil && app.backend.IsRemote() {
		fmt.Printf("Backend: remote (%s)\n", app.cfg.CLI.ServerURL)
	} else {
		fmt.Println("Backend: in-process (channel transport)")
	}
	defer app.Close()

	// Refine firstRun: config.json check passed, but DB may already have a subscription.
	// If a subscription exists in DB but config.json lacks cli_setup_completed,
	// auto-write the marker so the setup panel won't reappear on next startup.
	if firstRun && app.backend != nil {
		if sub, err := app.backend.GetDefaultSubscription(cliSenderID); err == nil && sub != nil && sub.APIKey != "" {
			firstRun = false
			app.cfg.CLISetupCompleted = true
			if err := saveCLIConfig(app.cfg); err != nil {
				log.Warnf("Failed to persist cli_setup_completed after detecting DB subscription: %v", err)
			}
		}
	}

	// Shutdown pprof server on exit
	if pprofServer != nil {
		defer pprofServer.Shutdown(context.Background())
	}

	disp := app.disp

	// 用工作目录绝对路径作为 ChatID，不同目录有不同的会话
	absWorkDir, _ := filepath.Abs(app.workDir)

	// Restore last active session on startup, unless --new/--new-session is used.
	// Both local and remote mode use local sessions.json — it's written by
	// SetLastActiveSession whenever the user switches sessions in the TUI.
	// RPC is not available here (backend not started yet).
	initialChatID := absWorkDir
	if newSession {
		// --new/--new-session: unconditionally create a new isolated session.
		name, chatID, err := channel.NewAutoSession(absWorkDir)
		if err != nil {
			log.WithError(err).Fatal("Failed to create new session")
		}
		initialChatID = chatID
		log.WithFields(log.Fields{"chatID": chatID, "name": name}).Info("Created new session")
	} else if last := channel.GetLastActiveSession(absWorkDir); last != "" {
		initialChatID = last
		log.WithFields(log.Fields{"chatID": initialChatID}).Info("Restoring last active session")
	}

	remoteServerURL := app.backend.ServerURL()
	// Pre-declare tenantSvc so SessionsList closure can capture it.
	// Assigned later after backend checks. Closure reads at invocation time.
	var tenantSvc *sqlite.TenantService

	cliCfg := channel.CLIChannelConfig{
		WorkDir:              absWorkDir,
		ChatID:               initialChatID,
		RemoteMode:           false, // unified: always use remote adapter path
		RemoteServerURL:      remoteServerURL,
		DebugMode:            flagDebug,
		DebugInput:           flagDebugInput,
		DebugCaptureMs:       flagDebugCapMs,
		IsFirstRun:           firstRun,
		SidebarWidthOverride: flagSidebarWidth,
		NoSidebar:            flagNoSidebar,
		GetCurrentValues: func() map[string]string {
			app.valuesCacheMu.RLock()
			cache := app.valuesCache
			app.valuesCacheMu.RUnlock()
			return cache
		},
		ApplySettings: func(values map[string]string, chatID string) {
			if app.backend == nil {
				return
			}
			_, llmChanged := values["llm_provider"]
			_, keyChanged := values["llm_api_key"]
			_, modelChanged := values["llm_model"]
			_, urlChanged := values["llm_base_url"]
			_, maxOutputChanged := values["max_output_tokens"]
			_, thinkingChanged := values["thinking_mode"]

			llmFieldChanged := llmChanged || keyChanged || modelChanged || urlChanged || maxOutputChanged || thinkingChanged

			// ── Subscription-scoped fields: update via subscription manager ──
			if llmFieldChanged {
				if err := updateActiveSubscription(app.backend, app.cfg, values); err != nil {
					log.Warnf("Failed to update active subscription: %v", err)
				}
				// Mark setup as completed so isFirstRun() won't re-trigger on next startup.
				// This is needed because LLM credentials are stored in DB (user_llm_subscriptions),
				// not in config.json, so the config-level API key check won't catch them.
				app.cfg.CLISetupCompleted = true
			}

			// ── Non-subscription settings: persist and apply runtime ──
			for k, v := range values {
				if isCLISubscriptionSettingKey(k) {
					continue // subscription fields handled above
				}
				if channel.IsGlobalScopedSettingKey(k) {
					continue // global-scoped keys not stored in DB
				}
				// Per-session settings: skip global DB write when in a session context
				if channel.IsPerSessionSettingKey(k) && chatID != "" {
					continue
				}
				_ = app.backend.SetSetting("cli", "cli_user", k, v)
			}
			agent.ApplyRuntimeSettings(app.cfg, app.backend, "cli_user", values)
			// Persist non-subscription settings to config.json

			// Update local cache immediately (no waiting for refreshRemoteValuesCache)
			app.valuesCacheMu.Lock()
			for k, v := range values {
				if app.valuesCache == nil {
					app.valuesCache = make(map[string]string)
				}
				// Per-session settings: don't cache globally (other sessions should see their own values)
				if channel.IsPerSessionSettingKey(k) && chatID != "" {
					continue
				}
				app.valuesCache[k] = v
			}
			app.valuesCacheMu.Unlock()

			if app.cliCh != nil {
				app.cliCh.SyncLayoutSettings(values)
			}

			// Always save layout to config.json (keys not in Config struct, must write directly)
			saveLayoutToConfig(values)
			if err := saveCLIConfig(app.cfg); err != nil {
				log.Warnf("Failed to save CLI config: %v", err)
			}

			// ── LLM config changes applied via RPC (unified local/remote path) ──
			if llmFieldChanged {
				app.backend.SetModelTiers(app.cfg.LLM)
				app.backend.SetDefaultThinkingMode(app.cfg.LLM.ThinkingMode)
				app.backend.SetModelContexts(app.cfg.Agent.ModelContexts)
				app.backend.SetGlobalMaxTokens(app.cfg.LLM.MaxOutputTokens)
				app.backend.SetRetryConfig(llm.DefaultRetryConfig())
			}

			// Immediately refresh cache so UI shows new values
			app.refreshRemoteValuesCache()
		},
		ClearMemory: func(targetType string) error {
			if app.backend == nil {
				return fmt.Errorf("agent not initialized")
			}
			return app.backend.ClearMemory(context.Background(), "cli", absWorkDir, targetType, "cli_user")
		},
		GetMemoryStats: func() map[string]string {
			if app.backend == nil {
				return map[string]string{}
			}
			return app.backend.GetMemoryStats(context.Background(), "cli", absWorkDir, "cli_user")
		},
		SwitchLLM: func(provider, baseURL, apiKey, model string) error {
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
				_ = app.backend.SetChatLLM(absWorkDir, app.cfg.LLM.Provider, app.cfg.LLM)
				_ = app.backend.SetModelTiers(app.cfg.LLM)
			}
			return nil
		},
		RefreshValuesCache: func() {
			app.refreshRemoteValuesCache()
		},
		UsageQuery: func(senderID string, days int) (*sqlite.UserTokenUsage, []sqlite.DailyTokenUsage, error) {
			if app.backend == nil {
				return nil, nil, fmt.Errorf("agent not initialized")
			}
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
		},
		AgentCount: func() int {
			if app.backend == nil {
				return 0
			}
			return app.backend.CountInteractiveSessions("cli", absWorkDir)
		},
		AgentList: func() []channel.AgentPanelEntry {
			if app.backend == nil {
				return nil
			}
			sessions := app.backend.ListInteractiveSessions("cli", absWorkDir)
			entries := make([]channel.AgentPanelEntry, len(sessions))
			for i, s := range sessions {
				entries[i] = channel.AgentPanelEntry{
					Role:       s.Role,
					Instance:   s.Instance,
					Running:    s.Running,
					Background: s.Background,
					Task:       s.Task,
					Preview:    s.Preview,
				}
			}
			return entries
		},
		AgentInspect: func(roleName, instance string, tailCount int) (string, error) {
			if app.backend == nil {
				return "", fmt.Errorf("agent not initialized")
			}
			return app.backend.InspectInteractiveSession(context.Background(), roleName, "cli", absWorkDir, instance, tailCount)
		},
		AgentMessages: func(roleName, instance string) []channel.SessionChatMessage {
			if app.backend == nil {
				return nil
			}
			msgs, _ := app.backend.GetSessionMessages("cli", absWorkDir, roleName, instance)
			if msgs == nil {
				return nil
			}
			result := make([]channel.SessionChatMessage, len(msgs))
			for i, m := range msgs {
				result[i] = channel.SessionChatMessage{Role: m.Role, Content: m.Content}
			}
			return result
		},
		SessionsList: func() []channel.SessionPanelEntry {
			// All modes use cache — refreshed by refreshAgentCache() in background.
			app.agentCacheMu.RLock()
			cached := app.sessionsCacheList
			app.agentCacheMu.RUnlock()
			entries := make([]channel.SessionPanelEntry, len(cached))
			copy(entries, cached)
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
		},
		ChannelConfigGetFn: func() (map[string]map[string]string, error) {
			if app.backend == nil {
				return nil, fmt.Errorf("agent not initialized")
			}
			return app.backend.GetChannelConfigs()
		},
		ChannelConfigSetFn: func(channelName string, values map[string]string) error {
			if app.backend == nil {
				return fmt.Errorf("agent not initialized")
			}
			return app.backend.SetChannelConfig(channelName, values)
		},
		CreateWebUserFn: func(username string) (string, error) {
			if app.backend == nil {
				return "", fmt.Errorf("agent not initialized")
			}
			return app.backend.CreateWebUser(username)
		},
		ListWebUsersFn: func() ([]map[string]any, error) {
			if app.backend == nil {
				return nil, fmt.Errorf("agent not initialized")
			}
			return app.backend.ListWebUsers()
		},
		DeleteWebUserFn: func(username string) error {
			if app.backend == nil {
				return fmt.Errorf("agent not initialized")
			}
			return app.backend.DeleteWebUser(username)
		},
		IsAdminFn: func() bool {
			return true // standalone mode: CLI user is always admin
		},
		PaletteContributor: func() []channel.PaletteExternalCommand {
			return app.buildPaletteExternalCommands()
		},
	}

	// 设置历史消息加载器（会话恢复）
	var cliTenantID int64
	var cliSessionSvc *sqlite.SessionService
	if app.backend != nil && app.db != nil {
		tenantSvc = sqlite.NewTenantService(app.db)
		cliSessionSvc = sqlite.NewSessionService(app.db)
		tenantID, err := tenantSvc.GetOrCreateTenantID("cli", initialChatID)
		if err == nil {
			cliTenantID = tenantID
			cliCfg.HistoryLoader = func() ([]channel.HistoryMessage, error) {
				msgs, err := cliSessionSvc.GetAllMessages(cliTenantID)
				if err != nil {
					return nil, err
				}
				return channel.ConvertMessagesToHistory(msgs), nil
			}
			// Restore token state from DB so the context bar shows immediately
			// on startup (not just after the first LLM call of the new session).
			// Restore token state for context bar display, preferring exact
			// per-message context_tokens over tenant_state (which may contain
			// stale estimated values from the old DetectTruncation code).
			cliMemSvc := sqlite.NewMemoryService(app.db)
			cliCfg.TokenStateLoader = func() (promptTokens, completionTokens int64) {
				// Prefer exact context_tokens from last user message
				if cliSessionSvc != nil && cliTenantID != 0 {
					if lastCtx, err := cliSessionSvc.GetLastUserMessageContextTokens(cliTenantID); err == nil && lastCtx > 0 {
						return lastCtx, 0
					}
				}
				// Fallback to tenant_state
				pt, ct, err := cliMemSvc.GetTokenState(context.Background(), cliTenantID)
				if err != nil {
					log.WithError(err).Warn("Failed to load token state")
					return 0, 0
				}
				return pt, ct
			}
		}
	}
	// Remote mode: history loaded via RestoreSession (uses suHistoryLoadMsg path)
	// (HistoryLoader runs during NewCLIChannel, before WS is connected)

	// Dynamic history loader: all modes use backend.GetHistory
	// (local: localTransport → DB, remote: WS RPC → server DB)
	if app.backend != nil {
		backend := app.backend
		cliCfg.DynamicHistoryLoader = func(channelName, chatID string) ([]channel.HistoryMessage, error) {
			if channelName == "" {
				channelName = "cli"
			}
			return backend.GetHistory(channelName, chatID)
		}
		cliCfg.TokenStateLoader = func() (promptTokens, completionTokens int64) {
			pt, ct, err := backend.GetTokenState("cli", initialChatID)
			if err != nil {
				return 0, 0
			}
			return pt, ct
		}
	}

	// Agent session history: load from in-memory interactiveSubAgents (not DB).
	// refreshAgentCache is declared here at function level (not inside an if block)
	// so it's accessible from both the SessionsListRefresh callback and the remote
	// client setup below. Assigned later with = (not :=).
	var refreshAgentCache func()
	if app.backend != nil {
		backend := app.backend
		cliCfg.GetActiveProgressFn = func(channelName, chatID string) *protocol.ProgressEvent {
			return backend.GetActiveProgress(channelName, chatID)
		}
		cliCfg.BindChatFn = func(chatID string) error {
			return backend.BindChat(chatID)
		}
		cliCfg.GetTodosFn = func(channelName, chatID string) []protocol.TodoItem {
			return backend.GetTodos(channelName, chatID)
		}
		cliCfg.GetTokenStateFn = func(channelName, chatID string) (int64, int64) {
			pt, ct, err := backend.GetTokenState(channelName, chatID)
			if err != nil {
				return 0, 0
			}
			return pt, ct
		}
		cliCfg.SessionsDeleteFn = func(channelName, chatID string) error {
			return backend.DeleteChat(channelName, cliSenderID, chatID)
		}
		// sessionsListRefresh will be assigned when refreshAgentCache is defined below.
		// We defer wiring via a pointer so the closure can capture the later-defined func.
		cliCfg.SessionsListRefresh = func() {
			if refreshAgentCache != nil {
				refreshAgentCache()
			}
		}
		cliCfg.TrimHistoryFn = func(channelName, chatID string, cutoff time.Time) error {
			return backend.TrimHistory(channelName, chatID, cutoff)
		}
		cliCfg.SetCWDFn = func(channelName, chatID, dir string) error {
			if err := backend.SetCWD(channelName, chatID, dir); err != nil {
				return err
			}
			if pluginWidgetSyncFn != nil {
				pluginWidgetSyncFn(chatID)
			}
			return nil
		}
		cliCfg.AgentSessionDumpFn = func(chatID string) ([]channel.HistoryMessage, error) {
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
						var tools []protocol.ToolProgress
						for _, t := range snap.Tools {
							tools = append(tools, protocol.ToolProgress{
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
							Tools:     tools,
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
			if cliCfg.DynamicHistoryLoader != nil {
				return cliCfg.DynamicHistoryLoader("agent", chatID)
			}
			return nil, nil
		}
	}

	cliCh := channel.NewCLIChannel(&cliCfg, app.msgBus)
	app.cliCh = cliCh
	disp.Register(cliCh)

	// Start pprof HTTP server if --pprof flag is set
	if flagPProf {
		pprofPort := 6060
		if flagPProfPort > 0 {
			pprofPort = flagPProfPort
		}
		pprofServer = pprof.NewServer(pprof.Config{
			Enable: true,
			Host:   "localhost",
			Port:   pprofPort,
		})
		if err := pprofServer.Start(); err != nil {
			log.WithError(err).Warn("Failed to start pprof server")
		}
	}

	// Inject SettingsService for interactive /settings panel
	if app.backend != nil {
		// Unified: use RPC-backed adapters for both local and remote modes
		cliCh.SetSettingsService(newBackendSettingsService(app.backend))
		cliCh.SetModelLister(newBackendModelLister(app.backend))
		// Forward user messages to backend (unified local/remote path)
		cliCh.SetSendInboundFn(func(msg bus.InboundMessage) bool {
			clipanic.Go("main.SendInbound", func() {
				if err := app.backend.SendInbound(msg); err != nil {
					log.WithError(err).Warn("Failed to send message")
					cliCh.SendToast("Failed to send message: "+err.Error(), "✗")
				}
			})
			return true
		})
		// Subscribe to outbound events (unified local/remote path)
		app.backend.Subscribe(protocol.EventPattern{Type: "outbound"}, func(env protocol.EventEnvelope) {
			var ev protocol.OutboundEvent
			if err := json.Unmarshal(env.Payload, &ev); err != nil {
				return
			}
			cliCh.Send(bus.OutboundMessage{
				Channel: ev.Channel,
				ChatID:  ev.ChatID,
				Content: ev.Content,
			})
		})
		// Handle ask_user events separately (WaitingUser=true, Questions JSON in metadata)
		app.backend.Subscribe(protocol.EventPattern{Type: "ask_user"}, func(env protocol.EventEnvelope) {
			var ev protocol.AskUserEvent
			if err := json.Unmarshal(env.Payload, &ev); err != nil {
				return
			}
			meta := map[string]string{"ask_questions": ev.Questions}
			if ev.RequestID != "" {
				meta["request_id"] = ev.RequestID
			}
			cliCh.Send(bus.OutboundMessage{
				Channel:     ev.Channel,
				ChatID:      ev.ChatID,
				WaitingUser: true,
				Metadata:    meta,
			})
		})
		// Register progress handler via Subscribe for streaming progress
		app.backend.Subscribe(protocol.EventPattern{Type: "progress"}, func(env protocol.EventEnvelope) {
			var p protocol.ProgressEvent
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				return
			}
			cliCh.SendProgress("cli:"+cliCfg.ChatID, &p)
		})
		// Register inject_user handler via Subscribe for bg task notifications
		app.backend.Subscribe(protocol.EventPattern{Type: "inject_user"}, func(env protocol.EventEnvelope) {
			var ev protocol.InjectUserEvent
			if err := json.Unmarshal(env.Payload, &ev); err != nil {
				return
			}
			cliCh.InjectUserMessage(ev.ChatID, ev.Content)
		})
		// Inject bg task callbacks via RPC (unified local/remote path)
		bgSessionKey := "cli:" + cliCfg.ChatID
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
		// Inject TrimHistoryFn for Ctrl+K session truncation (RPC-backed, unified path)
		cliCh.SetTrimHistoryFn(func(cutoff time.Time) error {
			return app.backend.TrimHistory("cli", cliCfg.ChatID, cutoff)
		})
		cliCh.SetResetTokenStateFn(func() {
			app.backend.ResetTokenState()
		})
	}

	// Wire AI-Native TUI callback (both local and remote modes)
	if app.backend != nil {
		tuiCtrl := func(action string, params map[string]string) (map[string]string, error) {
			return cliCh.SendTUIControl(action, params)
		}
		app.backend.SetTUIControlHandler(tuiCtrl)
	}

	// NOTE: Theme/layout is applied after backend.Start() below (lines ~1767).
	// Do NOT call backend.GetSettings before Start() — channelTransport
	// requires serve() goroutine to be running for RPC calls.

	// Channel callbacks are wired through the Backend interface above.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start backend: remote mode has retry with progress display;
	// local mode (channelTransport) connects instantly.
	if err := app.backend.Start(ctx); err != nil {
		if app.backend.IsRemote() {
			// Retry loop for remote connections
			const maxRetries = 5
			fmt.Fprintf(os.Stderr, "\n  Connecting to remote server %s ...\n", app.cfg.CLI.ServerURL)
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
						return
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
				return
			}
		} else {
			fmt.Fprintf(os.Stderr, "Failed to start backend: %v\n", err)
			app.Close()
			return
		}
	}
	clipanic.Go("main.dispatcher.Run", disp.Run)

	// ── Post-Start initialization (unified for all modes) ─────────────
	// Both local and remote modes run the same initialization.
	// Only a few items are remote-specific (reconnect, conn_state).

	// Apply layout from local config.json FIRST (instant, no RPC).
	layoutVals := map[string]string{}
	for _, k := range []string{"sidebar_width", "sidebar_enabled", "sidebar_position", "chat_max_width", "chat_center", "layout_mode"} {
		if v := configLayoutValue(k); v != "" {
			layoutVals[k] = v
		}
	}
	if len(layoutVals) > 0 {
		cliCh.SyncLayoutSettings(layoutVals)
	}
	// Refresh from server when WS is ready (or from local agent immediately)
	if vals, err := app.backend.GetSettings("cli", "cli_user"); err == nil {
		if t, ok := vals["theme"]; ok && t != "" {
			channel.ApplyTheme(t)
		}
		cliCh.SyncLayoutSettings(vals)
	}

	chatID := initialChatID

	// Auto-set CWD: for remote servers, sync the CLI's actual cwd so the
	// agent uses the correct directory. Local mode already has the workspace set.
	if app.backend.IsRemote() && isLocalServer(app.cfg.CLI.ServerURL) {
		if cwd, err := os.Getwd(); err == nil {
			if err := app.backend.SetCWD("cli", chatID, cwd); err != nil {
				log.WithError(err).WithField("chat_id", chatID).Warn("Failed to sync CWD to server")
			} else {
				log.WithFields(log.Fields{
					"cwd":     cwd,
					"chat_id": chatID,
				}).Info("Synced CLI CWD to local server")
			}
		}
	}

	// BindChat: subscribe to events for the initial chatID.
	app.backend.BindChat(chatID)

	// Plugin widgets: subscribe to push events for widget zone content.
	remoteCache := channel.NewRemotePluginCache(chatID, func(method string, params any) (json.RawMessage, error) {
		return app.backend.CallRPC(method, params)
	})
	cliCh.SetRemotePluginCache(remoteCache)
	pluginWidgetSyncFn = cliCh.SyncPluginWidgetChatID
	app.backend.Subscribe(protocol.EventPattern{Type: "plugin_widget"}, func(env protocol.EventEnvelope) {
		var ev protocol.PluginWidgetEvent
		if err := json.Unmarshal(env.Payload, &ev); err != nil {
			return
		}
		pushChatID := strings.TrimPrefix(ev.ChatID, "cli:")
		curChatID := strings.TrimPrefix(cliCh.CurrentChatID(), "cli:")
		if pushChatID != "" && curChatID != "" && pushChatID != curChatID {
			return // ignore pushes for other sessions
		}
		remoteCache.UpdateZones(ev.Zones)
	})
	remoteCache.Refresh()

	// Initial restore: load history + active progress + todos atomically.
	clipanic.Go("main.RestoreActiveProgress", func() {
		progress := app.backend.GetActiveProgress("cli", chatID)
		var todos []protocol.TodoItem
		if progress != nil {
			log.WithFields(log.Fields{
				"chatID":    chatID,
				"phase":     progress.Phase,
				"iteration": progress.Iteration,
				"histLen":   len(progress.IterationHistory),
			}).Info("RestoreActiveProgress: restoring progress snapshot")
		} else {
			log.WithField("chatID", chatID).Info("RestoreActiveProgress: no active progress")
		}
		history, err := app.backend.GetHistory("cli", chatID)
		if err != nil {
			log.WithError(err).Warn("RestoreActiveProgress: failed to load history")
			return
		}
		cliCh.RestoreSession(history, progress, todos)
	})

	// Remote-only: reconnect handler for WS connection drops.
	if app.backend.IsRemote() {
		app.backend.Subscribe(protocol.EventPattern{Type: "reconnect"}, func(env protocol.EventEnvelope) {
			defer clipanic.Recover("main.OnReconnect", nil, false)
			_ = app.backend.BindChat(chatID)
			if isLocalServer(app.cfg.CLI.ServerURL) {
				if cwd, err := os.Getwd(); err == nil {
					_ = app.backend.SetCWD("cli", chatID, cwd)
				}
			}
			clipanic.Go("main.ReconnectRestore", func() {
				progress := app.backend.GetActiveProgress("cli", chatID)
				history, err := app.backend.GetHistory("cli", chatID)
				if err != nil {
					log.WithError(err).Warn("ReconnectRestore: failed to load history")
					return
				}
				cliCh.RestoreSession(history, progress, nil)
				if progress != nil {
					cliCh.SetProcessing(true)
				} else {
					cliCh.SetProcessing(false)
				}
			})
		})
		// Connection state change handler for header bar indicator.
		app.backend.Subscribe(protocol.EventPattern{Type: "conn_state"}, func(env protocol.EventEnvelope) {
			var ev protocol.ConnStateEvent
			if err := json.Unmarshal(env.Payload, &ev); err != nil {
				return
			}
			cliCh.SetConnState(ev.State)
		})
	}

	// Session state handler: busy/idle + SubAgent lifecycle events.
	app.backend.Subscribe(protocol.EventPattern{Type: "session"}, func(env protocol.EventEnvelope) {
		var ev protocol.SessionEvent
		if err := json.Unmarshal(env.Payload, &ev); err != nil {
			return
		}
		cliCh.SendSessionState(ev)
	})

	// ── Session cache (all modes) ───────────────────────────────────
	// refreshAgentCache reads sessions/subagents from Backend and updates the
	// cache used by SessionsList and AgentCount/AgentList.
	refreshAgentCache = func() {
		if app.backend == nil {
			return
		}
		allSubAgents := app.backend.ListInteractiveSessions("cli", "")
		subsByChatID := make(map[string][]agent.InteractiveSessionInfo)
		for _, s := range allSubAgents {
			subsByChatID[s.ChatID] = append(subsByChatID[s.ChatID], s)
		}
		tenantMap := map[string]string{}
		if tenants, err := app.backend.ListTenants(); err == nil {
			for _, t := range tenants {
				if t.Channel == "agent" || t.Label == "" {
					continue
				}
				tenantMap[t.ChatID] = t.Label
			}
		}
		var sessionEntries []channel.SessionPanelEntry
		seen := make(map[string]bool)
		for _, s := range channel.ListLocalDirSessions(absWorkDir) {
			mainBusy := app.backend.IsProcessing("cli", s.ID)
			sessLabel := s.Label
			if sessLabel == "default" {
				sessLabel = "默认会话"
			}
			if dbLabel, ok := tenantMap[s.ID]; ok && dbLabel != "" {
				sessLabel = dbLabel
			}
			sessionEntries = append(sessionEntries, channel.SessionPanelEntry{
				ID: s.ID, Type: "main", Channel: "cli",
				Label: sessLabel, Active: s.ID == absWorkDir, Busy: mainBusy,
			})
			for _, sub := range subsByChatID[s.ID] {
				agentKey := sub.Role + ":" + sub.Instance
				if seen[agentKey] {
					continue
				}
				seen[agentKey] = true
				sessionEntries = append(sessionEntries, channel.SessionPanelEntry{
					ID:   fmt.Sprintf("agent:%s/%s", sub.Role, sub.Instance),
					Type: "agent", Channel: "cli", Role: sub.Role, Instance: sub.Instance,
					ParentID: s.ID, Running: sub.Running, Busy: sub.Running, MessageHint: sub.Preview,
				})
			}
		}
		agentEntries := make([]channel.AgentPanelEntry, 0, len(allSubAgents))
		for _, s := range allSubAgents {
			agentEntries = append(agentEntries, channel.AgentPanelEntry{
				Role: s.Role, Instance: s.Instance, Running: s.Running,
				Background: s.Background, Task: s.Task, Preview: s.Preview,
			})
		}
		app.agentCacheMu.Lock()
		app.agentCacheCount = len(allSubAgents)
		app.agentCacheList = agentEntries
		app.sessionsCacheList = sessionEntries
		app.agentCacheMu.Unlock()
	}
	refreshAgentCache()
	clipanic.Go("main.RefreshAgentCache", func() {
		ticker := time.NewTicker(30 * time.Second) // Safety-net poll; primary path is SessionEvent push
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refreshAgentCache()
			}
		}
	})

	// Background goroutine: periodically refresh values cache.
	// Both local and remote modes use cache for GetCurrentValues.
	if app.backend != nil {
		app.refreshRemoteValuesCache()
		valuesCtx, valuesCancel := context.WithCancel(context.Background())
		clipanic.Go("main.RefreshValuesCache", func() {
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

	if newSession {
		msg := bus.InboundMessage{
			Channel:    "cli",
			SenderID:   "cli_user",
			ChatID:     absWorkDir,
			ChatType:   "p2p",
			Content:    "/new",
			SenderName: "CLI User",
			Time:       time.Now(),
			RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
		}
		if err := app.backend.SendInbound(msg); err != nil {
			log.WithError(err).Warn("Failed to send newSession message")
		}
	}

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

	// Runner Bridge: inject LLM client, model list and provider for runner use (unified path)
	cliCh.SetRunnerLLM(app.llmClient, app.backend.ListModels(), app.cfg.LLM.Provider)

	// Multi-subscription support (unified for both local and remote modes)
	cliCh.SetSubscriptionManager(newBackendSubscriptionManager(app.backend))
	cliCh.SetLLMSubscriber(newBackendLLMSubscriber(app.backend))

	// --share flag: auto-connect as runner after TUI starts
	if flagShare != "" {
		shareURL := flagShare
		shareToken := flagToken
		shareWorkspace := flagWorkspace
		if shareWorkspace == "" {
			shareWorkspace = app.workDir
		}
		cliCh.StartWithRunner(shareURL, shareToken, shareWorkspace)
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

// backendSubscriptionManager / backendLLMSubscriber defined below (~line 2190).

// syncLLMFromActiveSub derives cfg.LLM.* from the active config subscription.
// Used as fallback when no DB subscriptions exist (first-run / config-only path).
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

// executeNonInteractive 非交互模式：单次执行 prompt 并输出到 stdout。
func executeNonInteractive(prompt string, maxContextTokens, maxOutputTokens int) {
	app := newCLIApp("", "", true, maxContextTokens, maxOutputTokens) // non-interactive always uses local backend
	defer app.Close()

	absWorkDir, _ := filepath.Abs(app.workDir)

	nonIntCh := channel.NewNonInteractiveChannel(app.msgBus)
	disp := app.disp
	disp.Register(nonIntCh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = app.backend.Start(ctx)
	go disp.Run()

	// Send message through unified RPC path (same as interactive mode)
	_ = app.backend.SendInbound(bus.InboundMessage{
		Channel:    "cli",
		SenderID:   "cli_user",
		ChatID:     absWorkDir,
		ChatType:   "p2p",
		Content:    prompt,
		SenderName: "CLI User",
		Time:       time.Now(),
		RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
	})

	nonIntCh.WaitDone()
}

// setupLogger 配置日志（CLI 模式：仅文件输出，不干扰终端 TUI）。
// 日志写入全局 xbotHome/logs 目录。
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

// createLLM 根据配置创建 LLM 客户端（带重试、指数退避和随机抖动）。
func createLLM(cfg config.LLMConfig, retryCfg llm.RetryConfig) (llm.LLM, error) {
	modelsLoadErrCb := func(err error) {
		select {
		case channel.ModelsLoadErrorCh() <- err:
		default:
		}
	}
	var inner llm.LLM
	switch cfg.Provider {
	case "openai":
		inner = llm.NewOpenAILLM(llm.OpenAIConfig{
			BaseURL:           cfg.BaseURL,
			APIKey:            cfg.APIKey,
			DefaultModel:      cfg.Model,
			MaxTokens:         cfg.MaxOutputTokens,
			OnModelsLoadError: modelsLoadErrCb,
		})
	case "anthropic":
		inner = llm.NewAnthropicLLM(llm.AnthropicConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: cfg.Model,
			MaxTokens:    cfg.MaxOutputTokens,
		})
	default:
		// All other providers (custom, openrouter, ollama, azure, google, deepseek, etc.)
		// use OpenAI-compatible API — same as LLMFactory.createClient.
		inner = llm.NewOpenAILLM(llm.OpenAIConfig{
			BaseURL:           cfg.BaseURL,
			APIKey:            cfg.APIKey,
			DefaultModel:      cfg.Model,
			MaxTokens:         cfg.MaxOutputTokens,
			OnModelsLoadError: modelsLoadErrCb,
		})
	}
	return llm.NewRetryLLM(inner, retryCfg), nil
}

// ---------------------------------------------------------------------------
// Backend adapters — implement CLI interfaces via Backend RPC
// ---------------------------------------------------------------------------

// backendSettingsService implements channel.SettingsService via Backend RPC.
type backendSettingsService struct {
	backend agent.AgentBackend
}

func newBackendSettingsService(backend agent.AgentBackend) *backendSettingsService {
	return &backendSettingsService{backend: backend}
}

func (s *backendSettingsService) GetSettings(namespace, senderID string) (map[string]string, error) {
	return s.backend.GetSettings(namespace, senderID)
}

func (s *backendSettingsService) SetSetting(namespace, senderID, key, value string) error {
	return s.backend.SetSetting(namespace, senderID, key, value)
}

// backendModelLister implements channel.ModelLister via Backend RPC.
type backendModelLister struct {
	backend agent.AgentBackend
}

func newBackendModelLister(backend agent.AgentBackend) *backendModelLister {
	return &backendModelLister{backend: backend}
}

func (l *backendModelLister) ListModels() []string {
	return l.backend.ListModels()
}

func (l *backendModelLister) EnsureModelsLoaded() {
	// Remote mode: model list is fetched from the server on demand.
	// No-op — the server handles caching and freshness.
}

func (l *backendModelLister) ListAllModels() []string {
	return l.backend.ListAllModels()
}

// backendSubscriptionManager implements channel.SubscriptionManager via Backend interface.
// Works identically for both local (localTransport → DB) and remote (WS RPC → server DB) modes.
type backendSubscriptionManager struct {
	backend agent.AgentBackend
}

func newBackendSubscriptionManager(backend agent.AgentBackend) *backendSubscriptionManager {
	return &backendSubscriptionManager{backend: backend}
}

func (m *backendSubscriptionManager) List(senderID string) ([]channel.Subscription, error) {
	if senderID == "" {
		senderID = cliSenderID
	}
	return m.backend.ListSubscriptions(senderID)
}

func (m *backendSubscriptionManager) GetDefault(senderID string) (*channel.Subscription, error) {
	if senderID == "" {
		senderID = cliSenderID
	}
	return m.backend.GetDefaultSubscription(senderID)
}

func (m *backendSubscriptionManager) Add(sub *channel.Subscription) error {
	return m.backend.AddSubscription(cliSenderID, *sub)
}

func (m *backendSubscriptionManager) Remove(id string) error {
	return m.backend.RemoveSubscription(id)
}

func (m *backendSubscriptionManager) SetDefault(id, chatID string) error {
	return m.backend.SetDefaultSubscription(id, chatID)
}

func (m *backendSubscriptionManager) SetModel(id, model string) error {
	return m.backend.SetSubscriptionModel(id, model)
}

func (m *backendSubscriptionManager) Rename(id, name string) error {
	return m.backend.RenameSubscription(id, name)
}

func (m *backendSubscriptionManager) Update(id string, sub *channel.Subscription) error {
	return m.backend.UpdateSubscription(id, *sub)
}

func (m *backendSubscriptionManager) UpdatePerModelConfig(id, model string, pmc channel.PerModelConfig) error {
	return m.backend.UpdatePerModelConfig(id, model, protocol.PerModelConfig(pmc))
}

// backendLLMSubscriber implements channel.LLMSubscriber via Backend interface.
// Works identically for both local and remote modes.
type backendLLMSubscriber struct {
	backend agent.AgentBackend
}

func newBackendLLMSubscriber(backend agent.AgentBackend) *backendLLMSubscriber {
	return &backendLLMSubscriber{backend: backend}
}

func (s *backendLLMSubscriber) SwitchSubscription(senderID string, sub *channel.Subscription, chatID string) error {
	if sub == nil {
		return nil
	}
	return s.backend.SetDefaultSubscription(sub.ID, chatID)
}

func (s *backendLLMSubscriber) SwitchModel(senderID, model, chatID string) {
	if senderID == "" {
		senderID = cliSenderID
	}
	if err := s.backend.SwitchModel(senderID, model, chatID); err != nil {
		log.WithError(err).Warn("backendLLMSubscriber: SwitchModel failed")
	}
}

func (s *backendLLMSubscriber) GetDefaultModel() string {
	return s.backend.GetDefaultModel()
}

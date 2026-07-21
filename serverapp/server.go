package serverapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel"
	"xbot/channel/feishu"
	"xbot/channel/napcat"
	"xbot/channel/qq"
	"xbot/channel/web"
	"xbot/config"
	"xbot/event"
	llm_pkg "xbot/llm"
	log "xbot/logger"
	"xbot/oauth"
	"xbot/oauth/providers"
	"xbot/plugin"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"xbot/storage"
	"xbot/storage/sqlite"
	"xbot/tools"
	"xbot/tools/feishu_mcp"
	"xbot/version"
)

// injectProxyLLM checks if the user's active runner has local LLM configured,
// and if so, injects a ProxyLLM into the agent's LLM factory.
func injectProxyLLM(userID string, ag *agent.Agent) {
	db := tools.GetRunnerTokenDB()
	if db == nil {
		return
	}
	store := tools.NewRunnerTokenStore(db)
	activeName, err := store.GetActiveRunner(userID)
	if err != nil || activeName == "" {
		return
	}
	runners, err := store.ListRunners(userID)
	if err != nil {
		return
	}
	for _, r := range runners {
		if r.Name == activeName {
			llm := r.LLMSettings()
			if llm.HasLLM() {
				sb := tools.GetSandbox()
				if sb == nil {
					return
				}
				router, ok := sb.(*tools.SandboxRouter)
				if !ok || router.Remote() == nil {
					return
				}
				rs := router.Remote()
				proxy := &llm_pkg.ProxyLLM{
					GenerateFunc: func(ctx context.Context, _, model string, messages []llm_pkg.ChatMessage, tools []llm_pkg.ToolDefinition, thinkingMode string) (*llm_pkg.LLMResponse, error) {
						return rs.LLMGenerate(ctx, userID, model, messages, tools, thinkingMode)
					},
					ListModelsFunc: func() []string {
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						models, err := rs.LLMModels(ctx, userID)
						if err != nil {
							return nil
						}
						return models
					},
				}
				model := llm.Model
				if model == "" {
					model = ag.GetDefaultModel()
				}
				ag.SetProxyLLM(userID, proxy, model)
				log.Infof("ProxyLLM injected for user=%s runner=%s provider=%s", userID, activeName, llm.Provider)
			} else {
				ag.ClearProxyLLM(userID)
			}
			return
		}
	}
}

// setupLogging initializes the logger.
func setupLogging(cfg *config.Config) {
	if err := setupLogger(cfg.Log, cfg.Agent.WorkDir); err != nil {
		log.WithError(err).Fatal("Failed to setup logger")
	}
}

// setupLLM creates the LLM client.
func setupLLM(cfg *config.Config) (llm_pkg.LLM, error) {
	return createLLM(cfg.LLM, llm_pkg.RetryConfig{
		Attempts: uint(cfg.Agent.LLMRetryAttempts),
		Delay:    time.Duration(cfg.Agent.LLMRetryDelay),
		MaxDelay: time.Duration(cfg.Agent.LLMRetryMaxDelay),
		Timeout:  time.Duration(cfg.Agent.LLMRetryTimeout),
	})
}

// setupOAuth creates OAuth server and manager.
func setupOAuth(cfg *config.Config, dbPath string) (*oauth.Server, *oauth.Manager, *providers.FeishuProvider, *sqlite.DB, error) {
	if !cfg.OAuth.Enable {
		return nil, nil, nil, nil, nil
	}

	sharedDB, err := sqlite.Open(dbPath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to open shared database for OAuth: %w", err)
	}
	tokenStorage, err := oauth.NewSQLiteStorage(sharedDB)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to create OAuth token storage: %w", err)
	}

	oauthManager := oauth.NewManager(tokenStorage)
	feishuProvider := providers.NewFeishuProvider(cfg.Feishu.AppID, cfg.Feishu.AppSecret, cfg.OAuth.BaseURL+"/oauth/callback")
	oauthManager.RegisterProvider(feishuProvider)
	oauthServer := oauth.NewServer(oauth.Config{Enable: true, Host: cfg.OAuth.Host, Port: cfg.OAuth.Port, BaseURL: cfg.OAuth.BaseURL}, oauthManager)
	log.WithFields(log.Fields{"port": cfg.OAuth.Port, "baseURL": cfg.OAuth.BaseURL}).Info("OAuth server started")
	return oauthServer, oauthManager, feishuProvider, sharedDB, nil
}

// maskAPIKey masks an API key for safe transport over WS RPC.
// Shows first 4 chars + "****" so users can identify the key.
func maskAPIKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return key[:4] + "****"
}

// createAdminLLM creates a new LLM client from the admin config.
func createAdminLLM(cfg *config.Config) (llm_pkg.LLM, error) {
	switch cfg.LLM.Provider {
	case "openai":
		return llm_pkg.NewOpenAILLM(llm_pkg.OpenAIConfig{
			BaseURL:      cfg.LLM.BaseURL,
			APIKey:       cfg.LLM.APIKey,
			DefaultModel: cfg.LLM.Model,
			MaxTokens:    cfg.LLM.MaxOutputTokens,
		}), nil
	case "anthropic":
		return llm_pkg.NewAnthropicLLM(llm_pkg.AnthropicConfig{
			BaseURL:      cfg.LLM.BaseURL,
			APIKey:       cfg.LLM.APIKey,
			DefaultModel: cfg.LLM.Model,
			MaxTokens:    cfg.LLM.MaxOutputTokens,
		}), nil
	default:
		// All other providers (custom, openrouter, ollama, azure, google, deepseek, etc.)
		// use OpenAI-compatible API — same as LLMFactory.createClient.
		return llm_pkg.NewOpenAILLM(llm_pkg.OpenAIConfig{
			BaseURL:      cfg.LLM.BaseURL,
			APIKey:       cfg.LLM.APIKey,
			DefaultModel: cfg.LLM.Model,
			MaxTokens:    cfg.LLM.MaxOutputTokens,
			OnModelsLoadError: func(err error) {
				log.WithError(err).Warn("Failed to load models list (admin LLM)")
			},
		}), nil
	}
}

// resolveStaticDir returns the frontend static directory.
// Priority: explicit config → binary-relative web/dist → XBOT_HOME/web/dist.
func resolveStaticDir(cfg *config.Config) string {
	if cfg.Web.StaticDir != "" {
		return cfg.Web.StaticDir
	}
	// 1. Binary-relative: <exe_dir>/web/dist/ (Docker image layout)
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "web", "dist")
		if fi, err := os.Stat(filepath.Join(candidate, "index.html")); err == nil && !fi.IsDir() {
			return candidate
		}
	}
	// 2. XBOT_HOME-relative: ~/.xbot/web/dist/ (install script layout)
	if home := config.XbotHome(); home != "" {
		candidate := filepath.Join(home, "web", "dist")
		if fi, err := os.Stat(filepath.Join(candidate, "index.html")); err == nil && !fi.IsDir() {
			return candidate
		}
	}
	return ""
}

// createChannelInstance creates a channel instance by name using current config.
// Returns nil for channels that require complex setup (e.g. web with DB/OSS).
// Used for dynamic channel start/stop without server restart.
func createChannelInstance(name string, cfg *config.Config, msgBus *bus.MessageBus) channel.Channel {
	switch name {
	case "feishu":
		return feishu.NewFeishuChannel(feishu.FeishuConfig{
			AppID:             cfg.Feishu.AppID,
			AppSecret:         cfg.Feishu.AppSecret,
			EncryptKey:        cfg.Feishu.EncryptKey,
			VerificationToken: cfg.Feishu.VerificationToken,
			AllowFrom:         cfg.Feishu.AllowFrom,
		}, msgBus)
	case "qq":
		return qq.NewQQChannel(qq.QQConfig{
			AppID:        cfg.QQ.AppID,
			ClientSecret: cfg.QQ.ClientSecret,
			AllowFrom:    cfg.QQ.AllowFrom,
		}, msgBus)
	case "napcat":
		return napcat.NewNapCatChannel(napcat.NapCatConfig{
			WSUrl:     cfg.NapCat.WSUrl,
			Token:     cfg.NapCat.Token,
			AllowFrom: cfg.NapCat.AllowFrom,
		}, msgBus)
	default:
		// 插件 channel：从 ChannelProviderRegistry 查找
		reg := GetChannelProviderRegistry()
		if reg == nil {
			return nil
		}
		provider, ok := reg.Get(name)
		if !ok {
			return nil
		}
		pluginCfg := GetPluginChannelConfig(cfg, name)
		ch, err := provider.CreateChannel(pluginCfg, msgBus)
		if err != nil {
			log.WithError(err).WithField("channel", name).Error("Plugin channel creation failed")
			return nil
		}
		return ch
	}
}

// channelShouldRun returns true if the given channel should be running
// based on the current config.
func channelShouldRun(cfg *config.Config, name string) bool {
	switch name {
	case "feishu":
		return cfg.Feishu.Enabled
	case "qq":
		return cfg.QQ.Enabled
	case "napcat":
		return cfg.NapCat.Enabled
	default:
		// 插件 channel：从 ChannelProviderRegistry 查找并委托 IsEnabled
		reg := GetChannelProviderRegistry()
		if reg == nil {
			return false
		}
		provider, ok := reg.Get(name)
		if !ok {
			return false
		}
		pluginCfg := GetPluginChannelConfig(cfg, name)
		return provider.IsEnabled(pluginCfg)
	}
}

// GetPluginChannelConfig 从 config.Channels 中读取插件 channel 的配置。
// 如果 config.Channels 为空或 name 不存在，返回空 map。
func GetPluginChannelConfig(cfg *config.Config, name string) map[string]string {
	if cfg == nil || cfg.Channels == nil {
		return nil
	}
	return cfg.Channels[name]
}

// registerChannels creates and registers all channels.
func registerChannels(disp *channel.Dispatcher, cfg *config.Config, msgBus *bus.MessageBus, ag *agent.Agent, webDB *sqlite.DB, workDir string) (*feishu.FeishuChannel, *web.WebChannel, error) {
	var feishuCh *feishu.FeishuChannel
	var webCh *web.WebChannel
	if cfg.Feishu.Enabled {
		feishuCh = feishu.NewFeishuChannel(feishu.FeishuConfig{
			AppID:             cfg.Feishu.AppID,
			AppSecret:         cfg.Feishu.AppSecret,
			EncryptKey:        cfg.Feishu.EncryptKey,
			VerificationToken: cfg.Feishu.VerificationToken,
			AllowFrom:         cfg.Feishu.AllowFrom,
		}, msgBus)
		disp.Register(feishuCh)

	}

	// 注册 QQ 渠道
	if cfg.QQ.Enabled {
		qqCh := qq.NewQQChannel(qq.QQConfig{
			AppID:        cfg.QQ.AppID,
			ClientSecret: cfg.QQ.ClientSecret,
			AllowFrom:    cfg.QQ.AllowFrom,
		}, msgBus)
		disp.Register(qqCh)
	}

	// 注册 NapCat (OneBot 11) 渠道
	if cfg.NapCat.Enabled {
		napcatCh := napcat.NewNapCatChannel(napcat.NapCatConfig{
			WSUrl:     cfg.NapCat.WSUrl,
			Token:     cfg.NapCat.Token,
			AllowFrom: cfg.NapCat.AllowFrom,
		}, msgBus)
		disp.Register(napcatCh)
	}

	if cfg.Web.Enable {
		if webDB != nil {
			webCh = web.NewWebChannel(web.WebChannelConfig{
				Host:       cfg.Web.Host,
				Port:       cfg.Web.Port,
				DB:         webDB.Conn(),
				AdminToken: cfg.Admin.Token,
				InviteOnly: cfg.Web.InviteOnly,
				PublicURL:  cfg.Sandbox.PublicURL,
				SingleUser: cfg.Agent.Experimental.SingleUser,
			}, msgBus)
			// Auto-detect frontend static files if not explicitly configured.
			staticDir := resolveStaticDir(cfg)
			if staticDir != "" {
				webCh.SetStaticDir(staticDir)
				log.WithField("static_dir", staticDir).Info("Frontend static files detected")
			}
			// Web file uploads go through cloud OSS only — no local storage
			webCh.SetWorkDir(workDir)
			// Set OSS provider for file storage
			if cfg.OSS.Provider == "qiniu" {
				ossProvider, err := web.NewOSSProvider(
					cfg.OSS.Provider,
					"",
					web.QiniuConfig{
						AccessKey: cfg.OSS.QiniuAccessKey,
						SecretKey: cfg.OSS.QiniuSecretKey,
						Bucket:    cfg.OSS.QiniuBucket,
						Domain:    cfg.OSS.QiniuDomain,
						Region:    cfg.OSS.QiniuRegion,
					},
				)
				if err != nil {
					log.WithError(err).Error("Failed to create Qiniu OSS provider")
				} else {
					webCh.SetOSSProvider(ossProvider)
					log.Info("OSS provider configured: qiniu")
				}
			}

			webCh.SetCallbacks(buildWebCallbacks(cfg, ag, webDB))
			// Wire admin role check into SandboxRouter — admin web users bypass
			// the DeniedSandbox restriction. Uses WebChannel's IsAdminIdentity
			// (role-based, checks DB for web-1 or "admin" auth identity).
			sb := tools.GetSandbox()
			if sb != nil {
				if router, ok := sb.(*tools.SandboxRouter); ok {
					router.SetIsAdminFn(webCh.IsAdminIdentity)
					if remote := router.Remote(); remote != nil {
						remote.OnRunnerStatusChange = func(userID, runnerName string, online bool) {
							webCh.PushRunnerStatus(userID, runnerName, online)
							// When a runner with local LLM connects/disconnects, update ProxyLLM.
							if online {
								injectProxyLLM(userID, ag)
							} else {
								ag.ClearProxyLLM(userID)
							}
						}
						remote.OnSyncProgress = func(userID, phase, message string) {
							webCh.PushSyncProgress(userID, phase, message)
						}
					}
				}
			}
			disp.Register(webCh)
		} else {
			log.Warn("Web channel enabled but no database available, skipping")
		}
	}

	// 注册插件 channel（从 ChannelProviderRegistry 查找）
	reg := GetChannelProviderRegistry()
	if reg != nil {
		for _, provider := range reg.List() {
			name := provider.Name()
			pluginCfg := GetPluginChannelConfig(cfg, name)
			if !provider.IsEnabled(pluginCfg) {
				continue
			}
			ch, err := provider.CreateChannel(pluginCfg, msgBus)
			if err != nil {
				log.WithError(err).WithField("channel", name).Error("Failed to create plugin channel")
				continue
			}
			disp.Register(ch)

			// If the channel is a ChannelPluginTransport, start its readLoop
			// for bidirectional JSON-RPC communication.
			if transport, ok := ch.(*agent.ChannelPluginTransport); ok {
				if err := transport.Start(); err != nil {
					log.WithError(err).WithField("channel", name).Error("Failed to start plugin transport")
					continue
				}
				go transport.Run(context.Background())
			}

			log.WithField("channel", name).Info("Plugin channel registered")
		}
	}

	return feishuCh, webCh, nil
}

func Run(args []string) error {
	// Parse --config flag before loading config.
	// Usage: xbot --config /path/to/config.json
	var configPath string
	for i := 0; i < len(args); i++ {
		if (args[i] == "--config" || args[i] == "-config") && i+1 < len(args) {
			configPath = args[i+1]
			i++
		} else if len(args[i]) > 9 && args[i][:9] == "--config=" {
			configPath = args[i][9:]
		}
	}

	var cfg *config.Config
	if configPath != "" {
		cfg = config.LoadFromFile(configPath)
		if cfg == nil {
			return fmt.Errorf("load config from %s", configPath)
		}
	} else {
		cfg = config.Load()
	}

	setupLogging(cfg)
	defer log.Close()

	llmClient, err := setupLLM(cfg)
	if err != nil {
		log.WithError(err).Fatal("Failed to create LLM client")
	}
	log.WithFields(log.Fields{"provider": cfg.LLM.Provider, "model": cfg.LLM.Model}).Info("LLM client created")

	workDir := cfg.Agent.WorkDir
	xbotDir := config.XbotHome()
	dbPath := config.DBFilePath()

	if err := storage.MigrateIfNeeded(context.Background(), workDir, dbPath); err != nil {
		log.WithError(err).Fatal("Failed to migrate data to SQLite")
	}

	oauthServer, oauthManager, _, sharedDB, err := setupOAuth(cfg, dbPath)
	if err != nil {
		log.WithError(err).Fatal("Failed to setup OAuth")
	}

	// 初始化沙箱
	tools.InitSandbox(cfg.Sandbox, workDir)

	// 初始化 ChannelProviderRegistry 并注入到 plugin 包。
	// 必须在 InitServer 之前调用，因为插件 Activate 可能在 InitServer 内触发。
	channelProviderReg := NewChannelProviderRegistry()
	SetChannelProviderRegistry(channelProviderReg)
	plugin.SetChannelProviderRegistrar(func(provider any) error {
		cp, ok := provider.(channel.ChannelProvider)
		if !ok {
			return fmt.Errorf("provider does not implement channel.ChannelProvider (got %T)", provider)
		}
		return channelProviderReg.Register(cp)
	})

	// Register the gRPC channel provider factory: creates stdioChannelPluginProvider
	// from ChannelProviderDecl. The provider spawns a dedicated process for the channel
	// and uses bidirectional JSON-RPC over stdin/stdout.
	// rpcTablePtr is set later after InitServer, but the factory is called during
	// InitServer (plugin activation). The dispatch function resolves lazily.
	var rpcTablePtr *RPCTable
	var registryPtr *tools.Registry // resolved after InitServer (same pattern as rpcTablePtr)
	var ag *agent.Agent             // resolved after InitServer; used by channel plugin prompt registration
	plugin.SetChannelProviderFactory(func(decl *plugin.ChannelProviderDecl, _ *plugin.StdioPluginProcess) (any, error) {
		return &stdioChannelPluginProvider{
			decl:     decl,
			xbotHome: xbotDir,
			rpcDisp: func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
				if rpcTablePtr == nil {
					return nil, fmt.Errorf("rpc table not available")
				}
				return rpcTablePtr.Dispatch(ctx, method, payload)
			},
			getRegistry: func() *tools.Registry { return registryPtr },
			agentGetter: func() *agent.Agent { return ag },
		}, nil
	})

	// Pre-declare so the channelReconfigureFn closure can reference them by pointer.
	// The closure is only invoked at runtime (never during InitServer), so the
	// nil→non-nil transition after InitServer returns is safe.
	var (
		rpcTable RPCTable
		disp     *channel.Dispatcher
		msgBus   *bus.MessageBus
	)

	// channelReconfigureFn is called after a channel config change (server-side only).
	channelReconfigureFn := func(name string) {
		if disp == nil || msgBus == nil {
			return
		}
		cfg := config.LoadFromFile(config.ConfigFilePath())
		if cfg == nil {
			return
		}
		_, running := disp.GetChannel(name)
		shouldRun := channelShouldRun(cfg, name)
		if shouldRun && !running {
			if ch := createChannelInstance(name, cfg, msgBus); ch != nil {
				disp.Register(ch)
				go func(n string, c any) {
					defer func() {
						if r := recover(); r != nil {
							log.WithField("channel", n).Error("Dynamic channel start panicked\n" + string(debug.Stack()))
						}
					}()
					if sc, ok := c.(interface{ Start() error }); ok {
						if err := sc.Start(); err != nil {
							log.WithError(err).WithField("channel", n).Error("Dynamic channel failed")
						}
					}
				}(ch.Name(), ch)
			}
		} else if !shouldRun && running {
			disp.Unregister(name)
		}
	}

	ag, rpcTable, disp, msgBus, err = InitServer(cfg, llmClient, dbPath, workDir, xbotDir, cfg.Web.PersonaIsolation, channelReconfigureFn, nil)
	if err != nil {
		log.WithError(err).Fatal("Failed to init server")
	}

	// Initialize canonical user identity resolver.
	// Uses the shared DB if available (OAuth enabled), otherwise opens a direct
	// connection to the DB path — IdentityResolver should always be available
	// in server mode regardless of OAuth config.
	var identityDB *sql.DB
	if sharedDB != nil {
		identityDB = sharedDB.Conn()
	} else {
		var err error
		identityDB, err = sql.Open("sqlite", dbPath)
		if err != nil {
			log.WithError(err).Warn("Failed to open identity DB, identity resolver disabled")
		}
	}
	if identityDB != nil {
		resolver := agent.NewIdentityResolver(identityDB)
		ag.SetIdentityResolver(resolver)
	}

	// Set the rpcTablePtr for the channel provider factory.
	// Plugin activation happens during InitServer, so the factory was already called
	// with the lazy dispatch function. Now the RPCTable is available.
	rpcTablePtr = &rpcTable
	registryPtr = ag.Tools() // resolve lazy registry getter for channel providers

	// Reconcile subscriptions into DB (single source of truth).
	// 1. migrate config.json subscriptions[] into DB user subscriptions (one-time).
	// 2. reconcile the shared system subscription from cfg.LLM/env every boot.
	// 3. set cli_user's default to the system subscription if it has none (first run).
	// 4. build the factory's fallback LLM from the system subscription.
	if subSvc := ag.LLMFactory().GetSubscriptionSvc(); subSvc != nil {
		if err := migrateConfigSubscriptions(cfg, subSvc, cliSenderID); err != nil {
			log.WithError(err).Warn("Failed to migrate config subscriptions to DB")
		}
		if err := reconcileSystemSubscription(cfg, subSvc); err != nil {
			log.WithError(err).Warn("Failed to reconcile system subscription")
		}
		// Seed cli_user's default to the system subscription only if it has no
		// default yet (first run). Existing user defaults are preserved.
		if udm, err := subSvc.GetUserDefaultModel(cliSenderID); err == nil && udm == nil {
			if sys, err := subSvc.GetSystemSubscription(); err == nil && sys != nil {
				_ = subSvc.SetUserDefaultModel(cliSenderID, sys.ID, sys.Model)
			}
		}
		// Build the factory fallback LLM from the resolved default subscription
		// (user default, or system subscription as fallback). This replaces the
		// old "override cfg.LLM → createAdminLLM → SetDefaults" dance: DB is the
		// single source, and the system subscription carries the config/env values.
		if defSub, errDef := subSvc.GetDefault(cliSenderID); errDef != nil {
			log.WithError(errDef).Error("GetDefault failed")
		} else if defSub == nil {
			log.Warn("GetDefault returned nil — no default or system subscription in DB")
		} else {
			log.WithFields(log.Fields{
				"id": defSub.ID, "name": defSub.Name, "model": defSub.Model,
				"provider": defSub.Provider, "is_system": defSub.IsSystem,
			}).Info("Default subscription from DB")
			// Refresh cfg.LLM from the authoritative DB row so tier-model mappings
			// and any cfg.LLM consumers see the resolved values.
			cfg.LLM.Provider = defSub.Provider
			cfg.LLM.BaseURL = defSub.BaseURL
			cfg.LLM.APIKey = defSub.APIKey
			cfg.LLM.Model = ""
			cfg.LLM.MaxOutputTokens = defSub.MaxOutputTokens
			if newClient, err := createAdminLLM(cfg); err == nil {
				ag.LLMFactory().SetSystemLLM(newClient, "")
				// max_output_tokens is per-model in DB now — no cache to seed.
				// thinking_mode is no longer carried on the factory user cache
				// from the subscription; it is a global user setting seeded below.
				log.WithFields(log.Fields{"provider": defSub.Provider, "model": defSub.Model, "max_output_tokens": defSub.MaxOutputTokens}).Info("LLM client synced from DB default subscription")
			}
			// Seed the global thinking_mode user setting once, if unset. Source
			// priority: existing user_settings row → cfg.LLM.ThinkingMode →
			// default-sub ThinkingMode → "" (auto). After seeding, ResolveLLM
			// reads it from the user_settings DB (canonical channel); changing it
			// via Ctrl+M or /settings only needs InvalidateSender. This replaces
			// the old per-subscription thinking.
			if ss := ag.SettingsService(); ss != nil {
				if vals, err := ss.GetSettings(channel.ThinkingModeChannel, cliSenderID); err == nil {
					if _, set := vals["thinking_mode"]; !set {
						seed := strings.TrimSpace(cfg.LLM.ThinkingMode)
						if seed == "" {
							seed = defSub.ThinkingMode
						}
						if err := ss.SetSetting(channel.ThinkingModeChannel, cliSenderID, "thinking_mode", seed); err != nil {
							log.WithError(err).Warn("Failed to seed global thinking_mode")
						} else {
							log.WithField("thinking_mode", seed).Info("Seeded global thinking_mode user setting")
						}
					}
				}
			}
		}
	}

	// Clean up subscription-scoped keys that were migrated from user_settings
	// to user_llm_subscriptions. Stale rows in user_settings can overwrite
	// correct subscription values on startup (e.g. name→provider, max_output_tokens→8192).
	// NOTE: thinking_mode is intentionally NOT in this list — it is now a global
	// user_setting (ScopeUser), so a user_settings row for it is legitimate.
	if ss := ag.SettingsService(); ss != nil {
		cleaned := 0
		for _, key := range []string{
			"llm_provider", "llm_api_key", "llm_model", "llm_base_url",
			"max_output_tokens",
		} {
			if err := ss.DeleteSetting("cli", cliSenderID, key); err == nil {
				cleaned++
			}
		}
		if cleaned > 0 {
			log.WithField("count", cleaned).Info("Cleaned subscription-scoped keys from user_settings")
		}
	}

	// Sync Agent runtime settings from DB (admin user).
	// DB is the source of truth — config.json may be stale after user changes.
	// Exception: sandbox_mode is a server-level config initialized from config.json
	// by InitSandbox above. DB should NOT override it on startup.
	if ss := ag.SettingsService(); ss != nil {
		if vals, err := ss.GetSettings("cli", cliSenderID); err == nil {
			// Preserve config.json sandbox_mode — it was already used by InitSandbox.
			// Remove from vals so applyRuntimeSettings doesn't override it.
			sandboxFromConfig := cfg.Sandbox.Mode
			delete(vals, "sandbox_mode")
			applyRuntimeSettings(cfg, ag, cliSenderID, vals)
			// Ensure sandbox_mode stays as config.json set it.
			if sandboxFromConfig != "" {
				cfg.Sandbox.Mode = sandboxFromConfig
			}
			log.Info("Agent runtime settings synced from DB")
		}
	}

	// 初始化 Feishu MCP（无论 OAuth 是否启用，飞书消息相关工具需要它）
	feishuMCP := feishu_mcp.NewFeishuMCP(oauthManager, cfg.Feishu.AppID, cfg.Feishu.AppSecret)
	feishuMCP.SetLarkClient(lark.NewClient(cfg.Feishu.AppID, cfg.Feishu.AppSecret,
		lark.WithOpenBaseUrl("https://open.feishu.cn")))

	// 消息工具（不需要 OAuth，直接用 Lark client 发消息/文件）
	ag.RegisterToolForChannel("feishu", &feishu_mcp.SendFileTool{MCP: feishuMCP})

	// 注册 OAuth 和 Feishu MCP 文档工具（如果 OAuth 启用）
	if cfg.OAuth.Enable && oauthManager != nil {
		// 注册 OAuth 工具
		oauthTool := &tools.OAuthTool{
			Manager: oauthManager,
			BaseURL: cfg.OAuth.BaseURL,
		}
		ag.RegisterCoreTool(oauthTool)
		ag.RegisterToolForChannel("feishu", &feishu_mcp.ListAllBitablesTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.BitableFieldsTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.BitableRecordTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.BitableListTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.BatchCreateAppTableRecordTool{MCP: feishuMCP})

		// Wiki tools
		ag.RegisterToolForChannel("feishu", &feishu_mcp.WikiListSpacesTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.WikiListNodesTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.WikiGetNodeTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.WikiMoveNodeTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.WikiCreateNodeTool{MCP: feishuMCP})

		// Document tools
		ag.RegisterToolForChannel("feishu", &feishu_mcp.DocxGetContentTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.DocxListBlocksTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.DocxCreateTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.DocxInsertBlockTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.DocxGetBlockTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.DocxDeleteBlocksTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.DocxFindBlockTool{MCP: feishuMCP})

		// Search tools
		ag.RegisterToolForChannel("feishu", &feishu_mcp.SearchWikiTool{MCP: feishuMCP})

		// Drive tools
		ag.RegisterToolForChannel("feishu", &feishu_mcp.UploadFileTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.ListFilesTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.AddPermissionTool{MCP: feishuMCP})

		// Message resource tools
		ag.RegisterToolForChannel("feishu", &feishu_mcp.DownloadFileTool{MCP: feishuMCP})
		ag.RegisterToolForChannel("feishu", &feishu_mcp.SendFileTool{MCP: feishuMCP})

		log.Info("OAuth and Feishu MCP document tools registered")
	}

	// 注册 DownloadFile 工具（支持 Web/OSS 和飞书两种来源）
	ag.RegisterCoreTool(tools.NewDownloadFileTool(cfg.Feishu.AppID, cfg.Feishu.AppSecret))
	ag.RegisterTool(tools.NewDownloadFileTool(cfg.Feishu.AppID, cfg.Feishu.AppSecret))
	if !cfg.DisableWebSearch {
		ag.RegisterCoreTool(tools.NewWebSearchTool(cfg.TavilyAPIKey))
	}

	// 初始化事件触发系统（Event Trigger System）
	triggerSvc := sqlite.NewTriggerService(ag.MultiSession().DB())
	eventRouter := event.NewRouter(triggerSvc)
	ag.SetEventRouter(eventRouter)

	webhookBaseURL := cfg.EventWebhook.BaseURL
	if webhookBaseURL == "" {
		webhookBaseURL = fmt.Sprintf("http://%s:%d", cfg.EventWebhook.Host, cfg.EventWebhook.Port)
	}
	ag.RegisterCoreTool(tools.NewEventTriggerTool(eventRouter, webhookBaseURL))

	var webhookServer *event.WebhookServer
	if cfg.EventWebhook.Enable {
		webhookServer = event.NewWebhookServer(eventRouter, event.WebhookConfig{
			Host:        cfg.EventWebhook.Host,
			Port:        cfg.EventWebhook.Port,
			BaseURL:     webhookBaseURL,
			MaxBodySize: cfg.EventWebhook.MaxBodySize,
			RateLimit:   cfg.EventWebhook.RateLimit,
		})
	}

	// 所有工具注册完成，索引全局工具（用于 search_tools 语义搜索）
	ag.IndexGlobalTools()
	ag.LLMFactory().SetModelContexts(cfg.Agent.ModelContexts)
	ag.LLMFactory().SetRetryConfig(llm_pkg.RetryConfig{
		Attempts: uint(cfg.Agent.LLMRetryAttempts),
		Delay:    time.Duration(cfg.Agent.LLMRetryDelay),
		MaxDelay: time.Duration(cfg.Agent.LLMRetryMaxDelay),
		Timeout:  time.Duration(cfg.Agent.LLMRetryTimeout),
	})

	tokenDB, err := sqlite.Open(dbPath)
	if err != nil {
		log.WithError(err).Warn("Failed to open token database, runner tokens disabled")
	} else {
		tools.SetRunnerTokenDB(tokenDB.Conn())
	}

	var webDB *sqlite.DB
	if tokenDB != nil {
		webDB = tokenDB
	}
	feishuCh, webCh, err := registerChannels(disp, cfg, msgBus, ag, webDB, workDir)
	if err != nil {
		log.WithError(err).Fatal("Failed to register channels")
	}

	// channelReconfigureFn was moved before InitServer call and passed as parameter.

	// Wire RPC handler for CLI RemoteBackend clients (after disp/msgBus are available).
	if webCh != nil {
		webCh.SetRPCHandler(func(method string, params json.RawMessage, identity web.RPCIdentity) (json.RawMessage, error) {
			senderID := identity.SenderID
			userID := identity.CanonicalUserID
			role := identity.CanonicalRole
			if senderID == "admin" || senderID == "cli_user" {
				// CLI users are always admin. Resolve the real user_id via
				// IdentityResolver so subscription/settings queries (which use
				// rpcUserID) hit the correct DB rows.
				if ag.IdentityResolver() != nil {
					if uid, _, err := ag.IdentityResolver().Resolve("cli", senderID); err == nil && uid > 0 {
						userID = uid
					}
				}
				role = "admin"
			} else if role == "" {
				role = "user"
			}
			// bizID is the sender-level identity (for per-session operations).
			// userID is the canonical user identity (for subscription/settings queries).
			// RPC handlers choose which to use: subscription/settings use rpcUserID(ctx),
			// per-session operations use rpcBizID(ctx).
			bizID := senderID
			if senderID == "admin" {
				bizID = cliSenderID
			}
			ctx := WithRPCCtxResolved(context.Background(), senderID, bizID, userID, role)
			return rpcTable.Dispatch(ctx, method, params)
		})
	}

	// Register virtual CLI channel for remote mode (CLI→WS→server).
	// This makes the dispatcher aware of channel=cli so all outbound messages
	// (including raw bus.Outbound calls) route correctly to WS clients.
	if webCh != nil {
		disp.Register(web.NewRemoteCLIChannel(webCh.Hub()))
	}

	// sessionStateHandler and ChatRenameFn are now handled internally by Agent.
	// No external injection needed — Agent uses its own channelFinder + multiSession.DB().

	// 设置飞书渠道的 CardBuilder（用于卡片回调处理）
	if feishuCh != nil {
		feishuCh.SetCardBuilder(ag.GetCardBuilder())
		if state := ag.ApprovalState(); state != nil {
			feishuCh.SetApprovalState(state)
		}

		// 传递 admin chatID 和 web DB（用于 admin 命令如 !webadd）
		adminChatID := cfg.Admin.ChatID
		if adminChatID != "" {
			feishuCh.SetAdminChatID(adminChatID)
		}
		if webDB != nil {
			feishuCh.SetWebDB(webDB.Conn())
		}

		// 注入设置卡片回调（让飞书渠道能访问 Agent 的 LLM/Registry/Settings 功能）
		feishuCh.SetSettingsCallbacks(buildFeishuSettingsCallbacks(cfg, ag))

		// Wire cross-channel account linking for Feishu.
		// When a Feishu user sends "/link <code>", the Feishu channel calls this
		// function to consume the code and link their Feishu identity.
		feishuCh.SetLinkAccountFn(func(code, channel, channelUserID string) (string, error) {
			if ag.IdentityResolver() == nil {
				return "", fmt.Errorf("identity resolver not available")
			}
			// Validate code without consuming (preview-safe)
			targetUserID, err := ag.IdentityResolver().ValidateLinkCode(code)
			if err != nil {
				return "", err
			}
			// Resolve current Feishu user
			currentUserID, _, _ := ag.IdentityResolver().Resolve(channel, channelUserID)
			if currentUserID == targetUserID {
				ag.IdentityResolver().ConsumeLinkCode(code)
				return "已关联，无需重复操作", nil
			}
			// Try simple link
			_, err = ag.IdentityResolver().LinkIdentity(targetUserID, channel, channelUserID)
			if err == nil {
				ag.IdentityResolver().ConsumeLinkCode(code)
				return fmt.Sprintf("关联成功 (user_id=%d)", targetUserID), nil
			}
			// Merge required — log warning for audit trail, then auto-execute.
			// Feishu has no practical two-step confirm mechanism; link code (8-char
			// base32, 5min TTL, single-use) serves as bearer authorization.
			log.WithFields(log.Fields{
				"source_user_id": currentUserID,
				"target_user_id": targetUserID,
				"channel":        channel,
				"channel_user":   channelUserID,
			}).Warn("IdentityResolver: auto-merging users via Feishu /link (irreversible)")
			ag.IdentityResolver().ConsumeLinkCode(code)
			if mergeErr := ag.IdentityResolver().MergeUsers(currentUserID, targetUserID); mergeErr != nil {
				return "", fmt.Errorf("合并失败: %w", mergeErr)
			}
			return fmt.Sprintf("账号合并成功 (user_id=%d)。此操作不可撤销，旧账号的所有资产已迁移到目标账号。", targetUserID), nil
		})

		// 注入飞书渠道特化 prompt 提供者
		ag.SetChannelPromptProviders(&feishuPromptAdapter{ch: feishuCh})
	}

	// 设置优雅退出（提前声明 ctx，供 OAuth Manager cleanup goroutine 使用）
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 设置 OAuth 服务器的回调函数，使其能在授权完成后发送消息
	if oauthServer != nil {
		// 启动 OAuth flow 定期清理 goroutine
		oauthManager.Start(ctx)

		oauthServer.SetSendFunc(func(chName, chatID, content string) error {
			_, err := disp.SendDirect(channel.OutboundMsg{
				Channel: chName,
				ChatID:  chatID,
				Content: content,
			})
			return err
		})
		// 现在启动 OAuth HTTP 服务器
		if err := oauthServer.Start(); err != nil {
			log.WithError(err).Fatal("Failed to start OAuth server")
		}
		log.WithFields(log.Fields{
			"port":    cfg.OAuth.Port,
			"baseURL": cfg.OAuth.BaseURL,
		}).Info("OAuth server started")
	}

	channels := disp.EnabledChannels()
	if len(channels) == 0 {
		log.Warn("No channels enabled. Set FEISHU_ENABLED=true and configure FEISHU_APP_ID/FEISHU_APP_SECRET.")
		log.Info("Starting in agent-only mode (no IM channels)")
	} else {
		log.WithField("channels", channels).Info("Channels enabled")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Dispatcher.Run() is already started by InitServer.

	// 启动所有渠道
	for name, ch := range getChannels(disp) {
		go func(n string, c channel.Channel) {
			defer func() {
				if r := recover(); r != nil {
					log.WithFields(log.Fields{"channel": n, "panic": r}).Error("Channel goroutine panicked\n" + string(debug.Stack()))
				}
			}()
			log.WithField("channel", n).Info("Starting channel...")
			if err := c.Start(); err != nil {
				log.WithError(err).WithField("channel", n).Error("Channel failed")
			}
		}(name, ch)
	}

	// 启动 Webhook 事件服务器
	if webhookServer != nil {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.WithField("panic", r).Error("Webhook server panicked\n" + string(debug.Stack()))
				}
			}()
			if err := webhookServer.Start(); err != nil {
				log.WithError(err).Error("Webhook server failed")
			}
		}()
		log.WithFields(log.Fields{
			"host":     cfg.EventWebhook.Host,
			"port":     cfg.EventWebhook.Port,
			"base_url": webhookBaseURL,
		}).Info("Webhook event server started")
	}

	// Agent loop already started by InitServer.

	log.Info("xbot started successfully")
	fmt.Println("🤖 xbot is running. Press Ctrl+C to stop.")

	// 启动后发送上线通知
	if cfg.StartupNotify.Channel != "" && cfg.StartupNotify.ChatID != "" {
		go sendStartupNotify(disp, cfg)
	}

	// 恢复被 graceful shutdown 中断的 agent loop
	resumePendingTurns(ag, webDB, sigCh)

	// 等待退出信号
	sig := <-sigCh
	log.WithField("signal", sig.String()).Warn("Received shutdown signal")
	fmt.Println("\nShutting down...")

	// 收集正在运行的 agent loop，记录到 pending_resumes 以便重启后恢复。
	// 必须在 cancel() 之前执行：cancel() 会终止所有 agent loop，
	// cancel 之后再遍历 chatCancelCh 就取不到任何 key 了。
	collectPendingResumes(ag, webDB)

	// 先取消 context，让 agent.Run() 退出（其 defer 会清理 cron 和 cleanup routine）
	cancel()

	// 关闭 Webhook 事件服务器
	if webhookServer != nil {
		webhookServer.Stop()
	}

	// 等待 agent loop 退出后再继续关闭
	if ag != nil {
		ag.Close()
	}

	// 关闭沙箱（清理 Docker 容器等资源）
	// export/import 可能耗时较长（大容器数分钟），不设超时，必须等待完成。
	if sandbox := tools.GetSandbox(); sandbox != nil {
		if err := sandbox.Close(); err != nil {
			log.WithError(err).Warn("Sandbox close error")
		}
	}

	// 停止 OAuth 服务器
	if oauthServer != nil {
		if err := oauthServer.Shutdown(context.Background()); err != nil {
			log.WithError(err).Warn("OAuth server shutdown error")
		}
	}
	// 停止 OAuth Manager 的定期清理 goroutine
	if oauthManager != nil {
		oauthManager.Close()
	}

	// 关闭 OAuth 共享数据库连接
	if sharedDB != nil {
		if err := sharedDB.Close(); err != nil {
			log.WithError(err).Warn("OAuth shared DB close error")
		}
	}

	// 关闭 runner token 数据库连接
	if tokenDB != nil {
		if err := tokenDB.Close(); err != nil {
			log.WithError(err).Warn("Token DB close error")
		}
	}

	disp.Stop()
	log.Info("xbot stopped")
	return nil
}

// collectPendingResumes records all active agent loops to pending_resumes table
// before shutdown. Called BEFORE cancel() — after cancel, chatCancelCh is empty.
func collectPendingResumes(ag *agent.Agent, webDB *sqlite.DB) {
	if ag == nil || webDB == nil {
		return
	}
	keys := ag.ActiveSessionKeys()
	if len(keys) == 0 {
		return
	}
	for _, key := range keys {
		// key format: "channel:chatID"
		idx := strings.Index(key, ":")
		if idx < 0 {
			continue
		}
		channel := key[:idx]
		chatID := key[idx+1:]
		content, senderID, err := webDB.GetLastUserMessage(channel, chatID)
		if err != nil {
			log.WithError(err).WithField("session", key).Warn("Failed to get last user message for pending resume")
			continue
		}
		if content == "" {
			log.WithField("session", key).Warn("No user message found for pending resume, skipping")
			continue
		}
		if err := webDB.AddPendingResume(channel, chatID, senderID, content); err != nil {
			log.WithError(err).WithField("session", key).Warn("Failed to record pending resume")
		} else {
			log.WithField("session", key).Info("Recorded pending resume for graceful shutdown recovery")
		}
	}
}

// resumePendingTurns re-triggers agent turns for sessions that were
// interrupted by graceful shutdown. Called after all channels start.
// A short delay lets web clients reconnect before the turn fires.
func resumePendingTurns(ag *agent.Agent, webDB *sqlite.DB, sigCh <-chan os.Signal) {
	if ag == nil || webDB == nil {
		return
	}
	pending, err := webDB.ListPendingResumes()
	if err != nil {
		log.WithError(err).Warn("Failed to list pending resumes")
		return
	}
	if len(pending) == 0 {
		return
	}
	// Delay to let web/feishu clients reconnect so resumed replies reach them.
	// Use select instead of time.Sleep so a SIGTERM during the wait still
	// triggers graceful shutdown instead of Go's default (immediate exit).
	log.Info("Pending resumes found, waiting 3s for clients to reconnect...")
	select {
	case <-time.After(3 * time.Second):
	case <-sigCh:
		log.Info("Shutdown signal received during resume wait, skipping resume")
		return
	}
	for _, pr := range pending {
		// Skip turns that already completed naturally between shutdown
		// collection and cancel() — the turn finished and reply was persisted.
		hasReply, err := webDB.HasAssistantReplyAfterLastUser(pr.Channel, pr.ChatID)
		if err != nil {
			log.WithError(err).WithField("session", pr.Channel+":"+pr.ChatID).Error("Failed to check assistant reply, keeping record for next restart")
			continue
		}
		if hasReply {
			log.WithField("session", pr.Channel+":"+pr.ChatID).Info("Turn already completed, skipping resume")
			webDB.ClearPendingResume(pr.Channel, pr.ChatID)
			continue
		}
		log.WithFields(log.Fields{
			"channel": pr.Channel,
			"chat_id": pr.ChatID,
		}).Info("Resuming interrupted agent turn")
		ag.InjectInboundResume(pr.Channel, pr.ChatID, pr.SenderID, pr.Content)
		if err := webDB.ClearPendingResume(pr.Channel, pr.ChatID); err != nil {
			log.WithError(err).Error("Failed to clear pending resume record after injection — residual record will self-heal on next restart if the turn completes")
		}
	}
}

// createLLM 根据配置创建 LLM 客户端（带重试、指数退避和随机抖动）
func createLLM(cfg config.LLMConfig, retryCfg llm_pkg.RetryConfig) (llm_pkg.LLM, error) {
	var inner llm_pkg.LLM
	switch cfg.Provider {
	case "openai":
		inner = llm_pkg.NewOpenAILLM(llm_pkg.OpenAIConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: cfg.Model,
		})
	case "anthropic":
		inner = llm_pkg.NewAnthropicLLM(llm_pkg.AnthropicConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: cfg.Model,
		})
	default:
		// All other providers (custom, openrouter, ollama, azure, google, deepseek, etc.)
		// use OpenAI-compatible API — same as LLMFactory.createClient.
		inner = llm_pkg.NewOpenAILLM(llm_pkg.OpenAIConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: cfg.Model,
			OnModelsLoadError: func(err error) {
				log.WithError(err).Warn("Failed to load models list")
			},
		})
	}

	return llm_pkg.NewRetryLLM(inner, retryCfg), nil
}

// setupLogger 配置日志
func setupLogger(cfg config.LogConfig, workDir string) error {
	return log.Setup(log.SetupConfig{
		Level:   cfg.Level,
		Format:  cfg.Format,
		WorkDir: workDir,
		MaxAge:  7, // 保留 7 天日志
	})
}

// getChannels 获取分发器中的所有渠道（辅助函数）
func getChannels(disp *channel.Dispatcher) map[string]channel.Channel {
	result := make(map[string]channel.Channel)
	for _, name := range disp.EnabledChannels() {
		if ch, ok := disp.GetChannel(name); ok {
			result[name] = ch
		}
	}
	return result
}

// sendStartupNotify 发送启动上线通知
func sendStartupNotify(disp *channel.Dispatcher, cfg *config.Config) {
	// 等待渠道 WebSocket 连接建立（轮询，最多 10 秒）
	const maxWait = 10 * time.Second
	const pollInterval = 500 * time.Millisecond
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		channels := disp.EnabledChannels()
		if len(channels) > 0 {
			// Give channels a moment to fully initialize
			time.Sleep(1 * time.Second)
			break
		}
		time.Sleep(pollInterval)
	}

	content := fmt.Sprintf("🟢 **xbot 已上线**\n- 版本：%s\n- 时间：%s\n- 模型：%s\n- 沙箱：%s\n- 记忆：%s",
		version.Info(),
		time.Now().Format("2006-01-02 15:04:05 MST"),
		cfg.LLM.Model,
		cfg.Sandbox.Mode,
		cfg.Agent.MemoryProvider,
	)

	for i := 0; i < 3; i++ {
		_, err := disp.SendDirect(channel.OutboundMsg{
			Channel: cfg.StartupNotify.Channel,
			ChatID:  cfg.StartupNotify.ChatID,
			Content: content,
		})
		if err == nil {
			log.WithFields(log.Fields{
				"channel": cfg.StartupNotify.Channel,
				"chat_id": cfg.StartupNotify.ChatID,
			}).Info("Startup notification sent")
			return
		}
		log.WithError(err).Warn("Failed to send startup notification, retrying...")
		time.Sleep(2 * time.Second)
	}
	log.Error("Failed to send startup notification after 3 attempts")
}

// feishuPromptAdapter 将 FeishuChannel 桥接为 agent.ChannelPromptProvider 接口。
// 避免在 agent 包中直接依赖 channel 包。
type feishuPromptAdapter struct {
	ch *feishu.FeishuChannel
}

func (a *feishuPromptAdapter) ChannelPromptName() string {
	return a.ch.Name()
}

func (a *feishuPromptAdapter) ChannelSystemParts(ctx context.Context, chatID, senderID string) map[string]string {
	return a.ch.ChannelSystemParts(ctx, chatID, senderID)
}

// buildRunnerConnectCmd constructs the xbot-runner CLI command from a token entry.
func buildRunnerConnectCmd(cfg *config.Config, entry *tools.RunnerTokenEntry) string {
	pubURL := cfg.PublicWSAddr()
	cmd := fmt.Sprintf("./xbot-runner --server %s/ws/%s --token %s", pubURL, entry.UserID, entry.Token)
	if entry.Settings.Mode == "docker" {
		cmd += " --mode docker"
		if entry.Settings.DockerImage != "" {
			cmd += fmt.Sprintf(" --docker-image %s", entry.Settings.DockerImage)
		}
	}
	if entry.Settings.Workspace != "" && entry.Settings.Workspace != "/workspace" {
		cmd += fmt.Sprintf(" --workspace %s", entry.Settings.Workspace)
	}
	return cmd
}

// saveServerConfig persists only the config sections the server actually modifies.
// It reads the current disk config first, overwrites ONLY the fields the server owns,
// then writes back — all other sections are preserved untouched.
//
// ⚠️ IMPORTANT: Do NOT add more sections here without careful review.
// Every field copied here must be one that the server actually modifies at runtime.
// Copying extra fields (Sandbox, CLI, Admin, Web, etc.) will overwrite user-set
// values with in-memory defaults, which is exactly the class of bug this function prevents.
func saveServerConfig(cfg *config.Config) error {
	path := config.ConfigFilePath()
	merged := config.LoadFromFile(path)
	if merged == nil {
		// Config file doesn't exist or has parse errors.
		// Refuse to overwrite — writing an empty config would destroy
		// all user settings (feishu, qq, web, etc.).
		// Only create a new file if it truly doesn't exist.
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			merged = &config.Config{}
		} else {
			log.WithField("path", path).Error("saveServerConfig: config file exists but cannot parse, refusing to overwrite")
			return fmt.Errorf("config file parse error, not overwriting")
		}
	}
	// Agent settings: write back ONLY fields the server actually modifies at runtime.
	// Do NOT copy the entire Agent struct — MaxContextTokens and MaxOutputTokens
	// are user-configured (per-model) and must not be overwritten by server defaults.
	// Only MaxIterations, MaxConcurrency, CompressionThreshold, and ContextMode
	// are server-owned runtime settings.
	merged.Agent.MaxIterations = cfg.Agent.MaxIterations
	merged.Agent.MaxConcurrency = cfg.Agent.MaxConcurrency
	merged.Agent.CompressionThreshold = cfg.Agent.CompressionThreshold
	merged.Agent.ContextMode = cfg.Agent.ContextMode
	// Auto-compress: only write back if explicitly set at runtime
	if cfg.Agent.EnableAutoCompress != nil {
		merged.Agent.EnableAutoCompress = cfg.Agent.EnableAutoCompress
	}

	// LLM credentials (Provider, BaseURL, APIKey, Model, MaxOutputTokens, ThinkingMode)
	// are NOT written back to config.json. The DB system subscription (reconciled
	// from config/env at boot) is the single source of truth, and cfg.LLM.* may
	// hold decrypted values refreshed from DB — writing them back would leak
	// plaintext keys. config.json keeps whatever credentials it already had
	// (preserved by SaveToFile's deep merge) solely as a boot seed for the system
	// subscription.

	return config.SaveToFile(path, merged)
}

// adminSenderID is the WS auth identity for admin users.
// Used ONLY for role-based access control (isAdmin checks).
// It is NOT a business senderID — never use it as a DB key for
// settings, subscriptions, token usage, or other per-user state.
const adminSenderID = "admin"

// cliSenderID is the fixed business sender ID for CLI channel.
// All CLI messages, settings, subscriptions, and per-user state use this ID.
// Server-side startup code uses this constant when seeding DB data.
const cliSenderID = "cli_user"

// isAdmin checks if the given context has admin privileges.
// Checks canonical user role first; falls back to authSenderID == "admin"
// for backward compat (CLI local mode where IdentityResolver may not be wired).
func isAdmin(ctx context.Context) bool {
	if role := rpcRole(ctx); role != "" {
		return role == "admin"
	}
	return rpcAuthID(ctx) == adminSenderID
}

// sessionKeyOwner extracts the chatID (owner) from a session/full key.
// Key format: "channel:chatID/roleName[:instance]"
// Returns empty string if the format is invalid.
func sessionKeyOwner(key string) string {
	slash := strings.LastIndex(key, "/")
	if slash <= 0 {
		return ""
	}
	parent := key[:slash]
	parts := strings.SplitN(parent, ":", 2)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// migrateConfigSubscriptions seeds config.json subscriptions into the DB for a given user.
// Idempotent — skips if the user already has DB subscriptions.
func migrateConfigSubscriptions(cfg *config.Config, subSvc *sqlite.LLMSubscriptionService, senderID string) error {
	if len(cfg.Subscriptions) == 0 {
		return nil
	}
	// Skip if user already has non-system DB subscriptions. The system subscription
	// (is_system=1) is injected by List and must not count as a user subscription.
	existing, err := subSvc.List(senderID)
	if err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}
	userOwned := 0
	for _, e := range existing {
		if !e.IsSystem {
			userOwned++
		}
	}
	if userOwned > 0 {
		return nil
	}
	for i, s := range cfg.Subscriptions {
		sub := &sqlite.LLMSubscription{
			SenderID:        senderID,
			Name:            s.Name,
			Provider:        s.Provider,
			BaseURL:         s.BaseURL,
			APIKey:          s.APIKey,
			Model:           s.Model,
			MaxOutputTokens: s.MaxOutputTokens,
			ThinkingMode:    s.ThinkingMode,
			IsDefault:       s.Active || (i == 0 && !hasActiveSub(cfg)),
		}
		if s.ID != "" {
			sub.ID = s.ID
		}
		if err := subSvc.Add(sub); err != nil {
			return fmt.Errorf("add subscription %s: %w", s.Name, err)
		}
		log.WithFields(log.Fields{"name": s.Name, "sender_id": senderID}).Info("Migrated config subscription to DB")
	}
	return nil
}

func hasActiveSub(cfg *config.Config) bool {
	for _, s := range cfg.Subscriptions {
		if s.Active {
			return true
		}
	}
	return false
}

// reconcileSystemSubscription upserts the shared system subscription into the DB
// from cfg.LLM (+ env overrides) on every boot. The system subscription is the
// single source for the global/default LLM: read-only, visible to all users, and
// the lowest-priority fallback in ResolveLLM. Env vars take precedence over
// config.json values, matching the original config loading semantics.
func reconcileSystemSubscription(cfg *config.Config, subSvc *sqlite.LLMSubscriptionService) error {
	llmCfg := cfg.LLM
	if llmCfg.Provider == "" && llmCfg.BaseURL == "" && llmCfg.APIKey == "" && llmCfg.Model == "" {
		// No global LLM configured — leave any existing system subscription untouched.
		return nil
	}
	sysSub := &sqlite.LLMSubscription{
		Provider:        llmCfg.Provider,
		BaseURL:         llmCfg.BaseURL,
		APIKey:          llmCfg.APIKey,
		Model:           llmCfg.Model,
		MaxOutputTokens: llmCfg.MaxOutputTokens,
		ThinkingMode:    llmCfg.ThinkingMode,
	}
	if err := subSvc.UpsertSystemSubscription(sysSub); err != nil {
		return fmt.Errorf("upsert system subscription: %w", err)
	}
	log.WithFields(log.Fields{
		"provider": sysSub.Provider, "model": sysSub.Model,
	}).Info("System subscription reconciled from config/env")
	return nil
}

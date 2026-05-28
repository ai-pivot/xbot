package serverapp

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	llm_pkg "xbot/llm"
	log "xbot/logger"
	"xbot/plugin"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// rpcContext holds shared dependencies for RPC handlers.
// It is created once at server startup and reused for every request.
// Per-request identity (authSenderID, bizID) is passed via context.Context,
// NOT stored here — this avoids rebuilding the entire table per request.
// RPCContext holds shared dependencies for RPC handler construction.
// Exported so that CLI local mode can construct it via BuildRPCTable.
type RPCContext struct {
	Cfg    *config.Config
	Ag     *agent.Agent
	Disp   *channel.Dispatcher
	MsgBus *bus.MessageBus

	// reconfigureFn is called after a channel config change (server-side only).
	reconfigureFn func(channel string)

	// pluginWidgetsMu serializes plugin_widgets RPC calls so concurrent
	// sessions don't race on the shared PluginContext.workingDir.
	pluginWidgetsMu sync.Mutex
}

func (h *RPCContext) requireAdmin(next RPCHandler) RPCHandler {
	return func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		if !isAdmin(rpcAuthID(ctx)) {
			return nil, fmt.Errorf("admin only")
		}
		return next(ctx, params)
	}
}

// ownOrAdmin checks that the caller owns the resource or has admin privileges.
// Empty chatID is treated as self-access (defaults to caller's bizID).
func ownOrAdmin(ctx context.Context, chatID string) error {
	if isAdmin(rpcAuthID(ctx)) || chatID == "" || chatID == rpcBizID(ctx) {
		return nil
	}
	return fmt.Errorf("access denied")
}

func (h *RPCContext) requireLLMFactory() error {
	if h.Ag == nil || h.Ag.LLMFactory() == nil {
		return fmt.Errorf("LLM factory not available")
	}
	return nil
}

func (h *RPCContext) requireSubscriptionSvc() (*sqlite.LLMSubscriptionService, error) {
	if err := h.requireLLMFactory(); err != nil {
		return nil, err
	}
	svc := h.Ag.LLMFactory().GetSubscriptionSvc()
	if svc == nil {
		return nil, fmt.Errorf("subscription service not available")
	}
	return svc, nil
}

func (h *RPCContext) requireMultiSession() error {
	if h.Ag == nil || h.Ag.MultiSession() == nil {
		return fmt.Errorf("multi-session not available")
	}
	return nil
}

// resolveChatID checks ownership and defaults empty chatID to the caller's bizID.
func resolveChatID(ctx context.Context, chatID string) (string, error) {
	if err := ownOrAdmin(ctx, chatID); err != nil {
		return "", err
	}
	if chatID == "" {
		return rpcBizID(ctx), nil
	}
	return chatID, nil
}

// BuildRPCTable constructs the complete RPC dispatch table.
// The table is built once at startup and reused for every request;
// per-request identity is injected via context, so no authSenderID/bizID is needed here.
func BuildRPCTable(cfg *config.Config, ag *agent.Agent, disp *channel.Dispatcher, msgBus *bus.MessageBus, reconfigureFn func(string)) RPCTable {
	h := &RPCContext{Cfg: cfg, Ag: ag, Disp: disp, MsgBus: msgBus, reconfigureFn: reconfigureFn}
	t := make(RPCTable, 70)
	registerSettingsHandlers(t, h)
	registerLLMHandlers(t, h)
	registerSubscriptionHandlers(t, h)
	registerSessionHandlers(t, h)
	registerTaskHandlers(t, h)
	registerAdminHandlers(t, h)
	registerPluginHandlers(t, h)
	return t
}

// ── Context / settings / cwd / max-iterations / concurrency / context-tokens ──

func registerSettingsHandlers(t RPCTable, h *RPCContext) {
	// send_inbound routes an inbound message through the message bus.
	// Used by Client.SendInbound in local mode so CLI never touches msgBus directly.
	t["send_inbound"] = rpc1(func(ctx context.Context, p struct {
		Channel    string            `json:"channel"`
		ChatID     string            `json:"chat_id"`
		Content    string            `json:"content"`
		SenderID   string            `json:"sender_id"`
		SenderName string            `json:"sender_name"`
		ChatType   string            `json:"chat_type"`
		RequestID  string            `json:"request_id"`
		Metadata   map[string]string `json:"metadata,omitempty"`
	}) (any, error) {
		msg := bus.InboundMessage{
			Channel:    p.Channel,
			ChatID:     p.ChatID,
			Content:    p.Content,
			SenderID:   p.SenderID,
			SenderName: p.SenderName,
			ChatType:   p.ChatType,
			RequestID:  p.RequestID,
			Metadata:   p.Metadata,
		}
		select {
		case h.MsgBus.Inbound <- msg:
			return nil, nil
		default:
			return nil, fmt.Errorf("inbound channel full")
		}
	})
	t["get_context_mode"] = rpc0(func(ctx context.Context) any {
		return h.Ag.GetContextMode()
	})
	t["set_context_mode"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		Mode string `json:"mode"`
	}) error {
		return h.Ag.SetContextMode(p.Mode)
	}))
	t["set_cwd"] = rpc1void(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
		Dir     string `json:"dir"`
	}) error {
		if err := ownOrAdmin(ctx, p.ChatID); err != nil {
			return err
		}
		// SetCWD internally refreshes plugin workDir with correct tenantID
		return h.Ag.SetCWD(p.Channel, p.ChatID, p.Dir)
	})
	t["get_settings"] = rpc1(func(ctx context.Context, p struct {
		Namespace string `json:"namespace"`
		SenderID  string `json:"sender_id"`
	}) (any, error) {
		bizID := rpcBizID(ctx)
		if err := migrateCLIUserSettingsFromGlobalIfNeeded(h.Cfg, h.Ag, p.Namespace, bizID); err != nil {
			return nil, err
		}
		if h.Ag.SettingsService() == nil {
			return nil, errSettingsUnavailable
		}
		result, err := h.Ag.SettingsService().GetSettings(p.Namespace, bizID)
		if err != nil {
			return nil, err
		}
		for _, k := range []string{"llm_provider", "llm_api_key", "llm_model", "llm_base_url"} {
			delete(result, k)
		}
		// Inject config defaults for keys not present in user_settings.
		// This ensures remote CLI clients see the actual runtime values
		// (e.g. max_context_tokens=200000) even when the user never
		// explicitly saved those settings.
		//
		// ⚠️ MaxContextTokens must no longer be blindly written back to config.json
		// (see saveServerConfig). The value here comes from the user's config file
		// and is the intended default.
		if _, ok := result["max_context_tokens"]; !ok {
			// Derive from current subscription's per-model config.
			// Falls back to config.Agent.MaxContextTokens if no subscription.
			if h.Ag.LLMFactory() != nil {
				if mc := h.Ag.LLMFactory().GetEffectiveMaxContext(bizID, ""); mc > 0 {
					result["max_context_tokens"] = fmt.Sprintf("%d", mc)
				} else {
					result["max_context_tokens"] = fmt.Sprintf("%d", h.Cfg.Agent.MaxContextTokens)
				}
			} else {
				result["max_context_tokens"] = fmt.Sprintf("%d", h.Cfg.Agent.MaxContextTokens)
			}
		}
		if _, ok := result["max_iterations"]; !ok {
			result["max_iterations"] = fmt.Sprintf("%d", h.Cfg.Agent.MaxIterations)
		}
		if _, ok := result["max_concurrency"]; !ok {
			result["max_concurrency"] = fmt.Sprintf("%d", h.Cfg.Agent.MaxConcurrency)
		}
		if _, ok := result["context_mode"]; !ok {
			result["context_mode"] = h.Cfg.Agent.ContextMode
		}
		if _, ok := result["compression_threshold"]; !ok {
			result["compression_threshold"] = fmt.Sprintf("%g", h.Cfg.Agent.CompressionThreshold)
		}
		return result, nil
	})
	t["set_setting"] = rpc1void(func(ctx context.Context, p struct {
		Namespace string `json:"namespace"`
		SenderID  string `json:"sender_id"`
		Key       string `json:"key"`
		Value     string `json:"value"`
	}) error {
		bizID := rpcBizID(ctx)
		if err := migrateCLIUserSettingsFromGlobalIfNeeded(h.Cfg, h.Ag, p.Namespace, bizID); err != nil {
			return err
		}
		switch p.Key {
		case "llm_provider", "llm_api_key", "llm_model", "llm_base_url":
			return nil
		}
		// Global-scoped keys (sandbox_mode) are server-level config, not per-user.
		// Apply runtime effect but don't persist to user_settings DB —
		// the source of truth is config.json.
		if channel.IsGlobalScopedSettingKey(p.Key) {
			if isAdmin(rpcAuthID(ctx)) {
				applyRuntimeSetting(h.Cfg, h.Ag, bizID, p.Key, p.Value)
			}
			return nil
		}
		// Subscription-scoped keys (max_context_tokens) are stored in the
		// subscription's PerModelConfigs, NOT in user_settings DB.
		// The CLI's saveSettings() handles the write via subscriptionMgr.Update().
		if channel.IsSubscriptionScopedSettingKey(p.Key) {
			if isAdmin(rpcAuthID(ctx)) {
				applyRuntimeSetting(h.Cfg, h.Ag, bizID, p.Key, p.Value)
			}
			return nil
		}
		if h.Ag.SettingsService() == nil {
			return errSettingsUnavailable
		}
		if err := h.Ag.SettingsService().SetSetting(p.Namespace, bizID, p.Key, p.Value); err != nil {
			return err
		}
		if isAdmin(rpcAuthID(ctx)) {
			applyRuntimeSetting(h.Cfg, h.Ag, bizID, p.Key, p.Value)
		}
		return nil
	})

	// ── Max iterations / concurrency / context tokens ──
	t["set_max_iterations"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		N int `json:"n"`
	}) error {
		h.Ag.SetMaxIterations(p.N)
		return nil
	}))
	t["set_max_concurrency"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		N int `json:"n"`
	}) error {
		h.Ag.SetMaxConcurrency(p.N)
		return nil
	}))
	t["set_max_context_tokens"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		MaxContext int    `json:"max_context"`
		ChatID     string `json:"chat_id,omitempty"`
	}) error {
		if p.ChatID != "" {
			h.Ag.SetMaxContextTokens(p.MaxContext, p.ChatID)
		} else {
			h.Ag.SetMaxContextTokens(p.MaxContext)
		}
		return nil
	}))
	t["set_compression_threshold"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		Threshold float64 `json:"threshold"`
	}) error {
		h.Ag.SetCompressionThreshold(p.Threshold)
		return nil
	}))
}

// ── LLM / model / tier / proxy handlers ──

func registerLLMHandlers(t RPCTable, h *RPCContext) {
	t["get_default_model"] = rpc0(func(ctx context.Context) string {
		bizID := rpcBizID(ctx)
		model := ""
		if subSvc := h.Ag.LLMFactory().GetSubscriptionSvc(); subSvc != nil {
			if sub, err := subSvc.GetDefault(bizID); err == nil && sub != nil && sub.Model != "" {
				model = sub.Model
			}
		}
		if model == "" {
			_, m, _, _ := h.Ag.LLMFactory().GetLLM(bizID)
			model = m
		}
		log.WithField("sender_id", bizID).WithField("model", model).Debug("RPC get_default_model")
		return model
	})
	t["set_user_model"] = rpc1void(func(ctx context.Context, p struct {
		Model string `json:"model"`
	}) error {
		return h.Ag.SetUserModel(rpcBizID(ctx), p.Model)
	})
	t["switch_model"] = rpc1void(func(ctx context.Context, p struct {
		Model  string `json:"model"`
		ChatID string `json:"chat_id,omitempty"`
	}) error {
		bizID := rpcBizID(ctx)
		log.WithField("sender_id", bizID).WithField("model", p.Model).WithField("chat_id", p.ChatID).Info("RPC switch_model")
		if p.ChatID != "" {
			h.Ag.LLMFactory().SwitchModel(bizID, p.Model, p.ChatID)
		} else {
			h.Ag.LLMFactory().SwitchModel(bizID, p.Model)
		}
		if subSvc := h.Ag.LLMFactory().GetSubscriptionSvc(); subSvc != nil {
			if sub, err := subSvc.GetDefault(bizID); err == nil && sub != nil {
				if err := subSvc.SetModel(sub.ID, p.Model); err != nil {
					log.WithError(err).Warn("RPC switch_model: SetModel failed")
				}
			}
		}
		return nil
	})
	t["get_user_max_context"] = rpc0(func(ctx context.Context) int { return h.Ag.GetUserMaxContext(rpcBizID(ctx)) })
	t["set_user_max_context"] = rpc1void(func(ctx context.Context, p struct {
		MaxContext int `json:"max_context"`
	}) error {
		return h.Ag.SetUserMaxContext(rpcBizID(ctx), p.MaxContext)
	})
	t["get_user_max_output_tokens"] = rpc0(func(ctx context.Context) int { return h.Ag.GetUserMaxOutputTokens(rpcBizID(ctx)) })
	t["set_user_max_output_tokens"] = rpc1void(func(ctx context.Context, p struct {
		MaxTokens int `json:"max_tokens"`
	}) error {
		return h.Ag.SetUserMaxOutputTokens(rpcBizID(ctx), p.MaxTokens)
	})
	t["get_user_thinking_mode"] = rpc0(func(ctx context.Context) string { return h.Ag.GetUserThinkingMode(rpcBizID(ctx)) })
	t["set_user_thinking_mode"] = rpc1void(func(ctx context.Context, p struct {
		Mode string `json:"mode"`
	}) error {
		return h.Ag.SetUserThinkingMode(rpcBizID(ctx), p.Mode)
	})
	t["get_llm_concurrency"] = rpc0(func(ctx context.Context) int { return h.Ag.GetLLMConcurrency(rpcBizID(ctx)) })
	t["set_llm_concurrency"] = rpc1void(func(ctx context.Context, p struct {
		Personal int `json:"personal"`
	}) error {
		return h.Ag.SetLLMConcurrency(rpcBizID(ctx), p.Personal)
	})
	t["set_default_thinking_mode"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		Mode string `json:"mode"`
	}) error {
		if h.Ag.LLMFactory() == nil {
			return fmt.Errorf("LLM factory not available")
		}
		h.Ag.LLMFactory().SetDefaultThinkingMode(p.Mode)
		return nil
	}))
	t["list_models"] = rpc0err(func(ctx context.Context) ([]string, error) {
		if h.Ag.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		client, _, _, _ := h.Ag.LLMFactory().GetLLM(rpcBizID(ctx))
		return client.ListModels(), nil
	})
	t["list_all_models"] = rpc0err(func(ctx context.Context) ([]string, error) {
		if h.Ag.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		models := h.Ag.LLMFactory().ListAllModelsForUser(rpcBizID(ctx))
		log.WithField("count", len(models)).Debug("RPC list_all_models")
		return models, nil
	})
	t["set_model_tiers"] = h.requireAdmin(rpc1void(func(ctx context.Context, p config.LLMConfig) error {
		if h.Ag.LLMFactory() == nil {
			return fmt.Errorf("LLM factory not available")
		}
		h.Ag.LLMFactory().SetModelTiers(p)
		return nil
	}))
	t["set_proxy_llm"] = rpc1void(func(ctx context.Context, p struct {
		Model string `json:"model"`
	}) error {
		if h.Ag.LLMFactory() != nil {
			h.Ag.LLMFactory().SwitchModel(rpcBizID(ctx), p.Model)
		}
		return nil
	})
	t["clear_proxy_llm"] = rpc0void(func(ctx context.Context) error { h.Ag.ClearProxyLLM(rpcBizID(ctx)); return nil })
	t["set_global_max_tokens"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		MaxTokens int `json:"max_tokens"`
	}) error {
		if h.Ag.LLMFactory() == nil {
			return fmt.Errorf("LLM factory not available")
		}
		h.Ag.LLMFactory().SetGlobalMaxTokens(p.MaxTokens)
		return nil
	}))
	t["set_model_contexts"] = h.requireAdmin(rpc1void(func(ctx context.Context, p map[string]int) error {
		if h.Ag.LLMFactory() == nil {
			return fmt.Errorf("LLM factory not available")
		}
		h.Ag.LLMFactory().SetModelContexts(p)
		return nil
	}))
	t["set_retry_config"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		Attempts uint          `json:"attempts"`
		Delay    time.Duration `json:"delay"`
		MaxDelay time.Duration `json:"max_delay"`
		Timeout  time.Duration `json:"timeout"`
	}) error {
		if h.Ag.LLMFactory() == nil {
			return fmt.Errorf("LLM factory not available")
		}
		h.Ag.LLMFactory().SetRetryConfig(llm_pkg.RetryConfig{
			Attempts: p.Attempts,
			Delay:    p.Delay,
			MaxDelay: p.MaxDelay,
			Timeout:  p.Timeout,
		})
		return nil
	}))
	t["apply_runtime_settings"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		Values map[string]string `json:"values"`
	}) error {
		applyRuntimeSettings(h.Cfg, h.Ag, rpcBizID(ctx), p.Values)
		return nil
	}))
}

// ── Subscription CRUD ──

func registerSubscriptionHandlers(t RPCTable, h *RPCContext) {
	t["list_subscriptions"] = rpc0err(h.listSubscriptions)
	t["get_default_subscription"] = rpc0err(h.getDefaultSubscription)
	t["add_subscription"] = rpc1void(func(ctx context.Context, p struct {
		Sub struct {
			Name            string `json:"name"`
			Provider        string `json:"provider"`
			BaseURL         string `json:"base_url"`
			APIKey          string `json:"api_key"`
			Model           string `json:"model"`
			Active          bool   `json:"active"`
			MaxOutputTokens int    `json:"max_output_tokens"`
			ThinkingMode    string `json:"thinking_mode"`
		} `json:"sub"`
	}) error {
		svc, err := h.requireSubscriptionSvc()
		if err != nil {
			return err
		}
		dbSub := &sqlite.LLMSubscription{
			Name:            p.Sub.Name,
			Provider:        p.Sub.Provider,
			BaseURL:         p.Sub.BaseURL,
			APIKey:          p.Sub.APIKey,
			Model:           p.Sub.Model,
			MaxOutputTokens: p.Sub.MaxOutputTokens,
			ThinkingMode:    p.Sub.ThinkingMode,
			IsDefault:       p.Sub.Active,
		}
		dbSub.SenderID = rpcBizID(ctx)
		return svc.Add(dbSub)
	})
	t["update_subscription"] = rpc1void(h.updateSubscription)
	t["update_per_model_config"] = rpc1void(func(ctx context.Context, p struct {
		ID     string                `json:"id"`
		Model  string                `json:"model"`
		Config sqlite.PerModelConfig `json:"config"`
	}) error {
		svc, err := h.requireSubscriptionSvc()
		if err != nil {
			return err
		}
		existing, err := svc.Get(p.ID)
		if err != nil {
			return fmt.Errorf("subscription %s not found: %w", p.ID, err)
		}
		bizID := rpcBizID(ctx)
		if !isAdmin(rpcAuthID(ctx)) && existing.SenderID != bizID {
			return fmt.Errorf("subscription not found")
		}
		if existing.PerModelConfigs == nil {
			existing.PerModelConfigs = make(map[string]sqlite.PerModelConfig)
		}
		existing.PerModelConfigs[p.Model] = p.Config
		if err := svc.Update(existing); err != nil {
			return err
		}
		// Invalidate the user-level LLM cache so GetLLMForChat picks up the
		// new PerModelConfigs.MaxContext on the next buildBaseRunConfig.
		// Without this, the old max_context value persists in the cached llmEntry
		// and maybeCompress uses stale limits.
		h.Ag.LLMFactory().InvalidateSender(bizID)
		return nil
	})
	t["remove_subscription"] = rpc1void(func(ctx context.Context, p struct {
		ID string `json:"id"`
	}) error {
		svc, err := h.requireSubscriptionSvc()
		if err != nil {
			return err
		}
		sub, err := svc.Get(p.ID)
		if err != nil {
			return err
		}
		if !isAdmin(rpcAuthID(ctx)) && sub.SenderID != rpcBizID(ctx) {
			return fmt.Errorf("subscription not found")
		}
		if err := svc.Remove(p.ID); err != nil {
			return err
		}
		h.Ag.LLMFactory().Invalidate(sub.SenderID)
		return nil
	})
	t["set_default_subscription"] = rpc1void(h.setDefaultSubscription)
	t["rename_subscription"] = rpc1void(func(ctx context.Context, p struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}) error {
		svc, err := h.requireSubscriptionSvc()
		if err != nil {
			return err
		}
		sub, err := svc.Get(p.ID)
		if err != nil {
			return err
		}
		if !isAdmin(rpcAuthID(ctx)) && sub.SenderID != rpcBizID(ctx) {
			return fmt.Errorf("subscription not found")
		}
		return svc.Rename(p.ID, p.Name)
	})
	t["set_subscription_model"] = rpc1void(h.setSubscriptionModel)
}

// ── Memory / session / history / status ──

func registerSessionHandlers(t RPCTable, h *RPCContext) {
	t["clear_memory"] = rpc1void(func(ctx context.Context, p struct {
		Channel    string `json:"channel"`
		ChatID     string `json:"chat_id"`
		TargetType string `json:"target_type"`
	}) error {
		if err := h.requireMultiSession(); err != nil {
			return err
		}
		chatID, err := resolveChatID(ctx, p.ChatID)
		if err != nil {
			return err
		}
		return h.Ag.MultiSession().ClearMemory(context.Background(), p.Channel, chatID, p.TargetType, rpcBizID(ctx))
	})
	t["get_memory_stats"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		if err := h.requireMultiSession(); err != nil {
			return nil, err
		}
		chatID, err := resolveChatID(ctx, p.ChatID)
		if err != nil {
			return nil, err
		}
		return h.Ag.MultiSession().GetMemoryStats(context.Background(), p.Channel, chatID, rpcBizID(ctx)), nil
	})
	t["get_user_token_usage"] = rpc0err(func(ctx context.Context) (any, error) {
		if err := h.requireMultiSession(); err != nil {
			return nil, err
		}
		return h.Ag.MultiSession().GetUserTokenUsage(rpcBizID(ctx))
	})
	t["get_daily_token_usage"] = rpc1(func(ctx context.Context, p struct {
		Days     int    `json:"days"`
		SenderID string `json:"sender_id"`
	}) (any, error) {
		if err := h.requireMultiSession(); err != nil {
			return nil, err
		}
		return h.Ag.MultiSession().GetDailyTokenUsage(rpcBizID(ctx), p.Days)
	})

	// ── Sub-agents / sessions ──
	t["count_interactive_sessions"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (int, error) {
		if err := ownOrAdmin(ctx, p.ChatID); err != nil {
			return 0, err
		}
		if !isAdmin(rpcAuthID(ctx)) && p.ChatID == "" {
			p.ChatID = rpcBizID(ctx)
		}
		return h.Ag.CountInteractiveSessions(p.Channel, p.ChatID), nil
	})
	t["list_interactive_sessions"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		if err := ownOrAdmin(ctx, p.ChatID); err != nil {
			return nil, err
		}
		if !isAdmin(rpcAuthID(ctx)) && p.ChatID == "" {
			p.ChatID = rpcBizID(ctx)
		}
		return h.Ag.ListInteractiveSessions(p.Channel, p.ChatID), nil
	})
	t["inspect_interactive_session"] = rpc1(func(ctx context.Context, p struct {
		Role      string `json:"role"`
		Channel   string `json:"channel"`
		ChatID    string `json:"chat_id"`
		Instance  string `json:"instance"`
		TailCount int    `json:"tail_count"`
	}) (string, error) {
		chatID, err := resolveChatID(ctx, p.ChatID)
		if err != nil {
			return "", err
		}
		return h.Ag.InspectInteractiveSession(context.Background(), p.Role, p.Channel, chatID, p.Instance, p.TailCount)
	})
	t["get_session_messages"] = rpc1(func(ctx context.Context, p struct {
		Channel  string `json:"channel"`
		ChatID   string `json:"chat_id"`
		Role     string `json:"role"`
		Instance string `json:"instance"`
	}) (any, error) {
		chatID, err := resolveChatID(ctx, p.ChatID)
		if err != nil {
			return nil, err
		}
		msgs, _ := h.Ag.GetSessionMessages(p.Channel, chatID, p.Role, p.Instance)
		if msgs == nil {
			msgs = []agent.SessionMessage{}
		}
		return msgs, nil
	})
	t["get_agent_session_dump"] = rpc1(func(ctx context.Context, p struct {
		Channel  string `json:"channel"`
		ChatID   string `json:"chat_id"`
		Role     string `json:"role"`
		Instance string `json:"instance"`
	}) (any, error) {
		chatID, err := resolveChatID(ctx, p.ChatID)
		if err != nil {
			return nil, err
		}
		dump, _ := h.Ag.GetAgentSessionDump(p.Channel, chatID, p.Role, p.Instance)
		if dump == nil {
			dump = &agent.AgentSessionDump{}
		}
		return dump, nil
	})
	t["get_agent_session_dump_by_full_key"] = rpc1(func(ctx context.Context, p struct {
		FullKey string `json:"full_key"`
	}) (any, error) {
		if p.FullKey == "" {
			return nil, fmt.Errorf("full_key is required")
		}
		if owner := sessionKeyOwner(p.FullKey); owner != "" {
			if !isAdmin(rpcAuthID(ctx)) && owner != rpcBizID(ctx) {
				return nil, fmt.Errorf("access denied")
			}
		}
		dump, _ := h.Ag.GetAgentSessionDumpByFullKey(p.FullKey)
		if dump == nil {
			dump = &agent.AgentSessionDump{}
		}
		return dump, nil
	})

	// ── History ──
	t["get_history"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		bizID := rpcBizID(ctx)
		if p.Channel == "" {
			p.Channel = "web"
		}
		if p.ChatID == "" {
			p.ChatID = bizID
		}
		if !isAdmin(rpcAuthID(ctx)) && p.ChatID != bizID && p.Channel != "agent" {
			return nil, fmt.Errorf("access denied")
		}
		// Update last_active_at so we can restore the most recent session on restart.
		if db := h.Ag.MultiSession().DB(); db != nil {
			if _, err := sqlite.NewTenantService(db).GetOrCreateTenantID(p.Channel, p.ChatID); err != nil {
				log.WithError(err).Warn("RPC get_history: failed to update last_active_at")
			}
		}
		history, err := func() ([]channel.HistoryMessage, error) {
			ms := h.Ag.MultiSession()
			if ms == nil {
				return nil, fmt.Errorf("multi-session not available")
			}
			sess, err := ms.GetOrCreateSession(p.Channel, p.ChatID)
			if err != nil {
				return nil, err
			}
			msgs, err := sess.GetMessages()
			if err != nil {
				return nil, err
			}
			return channel.ConvertMessagesToHistory(msgs), nil
		}()
		if err != nil {
			return nil, err
		}
		log.WithFields(log.Fields{"channel": p.Channel, "chat_id": p.ChatID, "count": len(history)}).Info("RPC get_history")
		return history, nil
	})
	t["delete_chat"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		bizID := rpcBizID(ctx)
		authID := rpcAuthID(ctx)
		if p.Channel == "" {
			p.Channel = "cli"
		}
		if p.ChatID == "" {
			p.ChatID = bizID
		}
		if !isAdmin(authID) && p.ChatID != bizID {
			return nil, fmt.Errorf("access denied")
		}
		// Use bizID (cliSenderID for admin) as sender_id for DB operations,
		// because ChatRenameFn writes labels with cliSenderID, not the WS auth identity.
		senderID := bizID
		if db := h.Ag.MultiSession().DB(); db != nil {
			cs := sqlite.NewChatService(db.Conn())
			if err := cs.DeleteChat(p.Channel, senderID, p.ChatID); err != nil {
				return nil, fmt.Errorf("delete chat: %w", err)
			}
		} else {
			return nil, fmt.Errorf("database not available")
		}
		// Clean up offload files for the deleted session.
		h.Ag.CleanupSessionFiles(p.Channel, p.ChatID)
		// Clean up worktree registration + physical git worktree.
		sessionKey := p.Channel + ":" + p.ChatID
		tools.GlobalWorktreeRegistry.CleanupSession(sessionKey)
		// Clean up persisted CWD so a future session with the same chatID
		// does not inherit a stale worktree directory.
		session.DeletePersistedCWD(p.Channel, p.ChatID)
		// Destroy tenant session (closes MCP, removes from cache).
		_ = h.Ag.MultiSession().DestroySession(p.Channel, p.ChatID)
		log.WithFields(log.Fields{"channel": p.Channel, "chat_id": p.ChatID}).Info("RPC delete_chat")
		return map[string]string{"status": "ok"}, nil
	})
	t["rename_chat"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
		NewName string `json:"new_name"`
	}) (any, error) {
		bizID := rpcBizID(ctx)
		authID := rpcAuthID(ctx)
		if p.Channel == "" {
			p.Channel = "cli"
		}
		if !isAdmin(authID) && p.ChatID != bizID {
			return nil, fmt.Errorf("access denied")
		}
		// Use bizID (cliSenderID for admin) as sender_id for DB operations,
		// consistent with ChatRenameFn which writes labels with cliSenderID.
		senderID := bizID
		if db := h.Ag.MultiSession().DB(); db != nil {
			cs := sqlite.NewChatService(db.Conn())
			if err := cs.RenameChat(p.Channel, senderID, p.ChatID, p.NewName); err != nil {
				return nil, fmt.Errorf("rename chat: %w", err)
			}
		} else {
			return nil, fmt.Errorf("database not available")
		}
		return map[string]string{"status": "ok"}, nil
	})
	t["get_token_state"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		bizID := rpcBizID(ctx)
		if p.Channel == "" {
			p.Channel = "cli"
		}
		if p.ChatID == "" {
			p.ChatID = bizID
		}
		ms := h.Ag.MultiSession()
		if ms == nil {
			return map[string]int64{"prompt_tokens": 0, "completion_tokens": 0}, nil
		}
		sess, err := ms.GetOrCreateSession(p.Channel, p.ChatID)
		if err != nil {
			return nil, err
		}
		memSvc := sess.MemoryService()
		if memSvc == nil {
			return map[string]int64{"prompt_tokens": 0, "completion_tokens": 0}, nil
		}
		pt, ct, err := memSvc.GetTokenState(ctx, sess.TenantID())
		if err != nil {
			return nil, err
		}
		return map[string]int64{"prompt_tokens": pt, "completion_tokens": ct}, nil
	})
	t["trim_history"] = rpc1void(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
		Cutoff  int64  `json:"cutoff"`
	}) error {
		bizID := rpcBizID(ctx)
		if p.Channel == "" {
			p.Channel = "web"
		}
		if p.ChatID == "" {
			p.ChatID = bizID
		}
		if !isAdmin(rpcAuthID(ctx)) && p.ChatID != bizID {
			return fmt.Errorf("access denied")
		}
		var cutoff time.Time
		if p.Cutoff > 0 {
			cutoff = time.Unix(p.Cutoff, 0)
		}
		return h.Ag.MultiSession().TrimHistory(p.Channel, p.ChatID, cutoff)
	})

	// ── Status ──
	t["is_processing"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (bool, error) {
		if p.Channel == "" {
			p.Channel = "web"
		}
		// is_processing requires explicit chatID or admin.
		// Unlike other handlers, empty chatID does NOT default to self.
		if !isAdmin(rpcAuthID(ctx)) && p.ChatID != rpcBizID(ctx) {
			return false, fmt.Errorf("access denied")
		}
		return h.Ag.IsProcessingByChannel(p.Channel, p.ChatID), nil
	})
	t["get_active_progress"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		bizID := rpcBizID(ctx)
		if p.Channel == "" {
			p.Channel = "web"
		}
		if !isAdmin(rpcAuthID(ctx)) && p.ChatID != bizID && p.Channel != "agent" {
			return nil, fmt.Errorf("access denied")
		}
		return h.Ag.GetActiveProgress(p.Channel, p.ChatID), nil
	})

	t["get_todos"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		bizID := rpcBizID(ctx)
		if p.Channel == "" {
			p.Channel = "web"
		}
		if !isAdmin(rpcAuthID(ctx)) && p.ChatID != bizID && p.Channel != "agent" {
			return nil, fmt.Errorf("access denied")
		}
		return h.Ag.GetTodos(p.Channel, p.ChatID), nil
	})
}

// ── Background tasks / tenants ──

func registerTaskHandlers(t RPCTable, h *RPCContext) {
	t["get_bg_task_count"] = rpc1(func(ctx context.Context, p struct {
		SessionKey string `json:"session_key"`
	}) (int, error) {
		bizID := rpcBizID(ctx)
		if !isAdmin(rpcAuthID(ctx)) && p.SessionKey != "" {
			if owner := sessionKeyOwner(p.SessionKey); owner != "" && owner != bizID {
				return 0, fmt.Errorf("access denied")
			}
		}
		if h.Ag.BgTaskManager() == nil {
			return 0, nil
		}
		return len(h.Ag.BgTaskManager().ListRunning(p.SessionKey)), nil
	})
	t["list_bg_tasks"] = rpc1(func(ctx context.Context, p struct {
		SessionKey string `json:"session_key"`
	}) (any, error) {
		bizID := rpcBizID(ctx)
		if !isAdmin(rpcAuthID(ctx)) && p.SessionKey != "" {
			if owner := sessionKeyOwner(p.SessionKey); owner != "" && owner != bizID {
				return nil, fmt.Errorf("access denied")
			}
		}
		if h.Ag.BgTaskManager() == nil {
			return []struct{}{}, nil
		}
		return marshalBgTasks(h.Ag.BgTaskManager().ListAllForSession(p.SessionKey)), nil
	})
	t["kill_bg_task"] = rpc1void(func(ctx context.Context, p struct {
		TaskID string `json:"task_id"`
	}) error {
		bizID := rpcBizID(ctx)
		if h.Ag.BgTaskManager() == nil {
			return fmt.Errorf("background tasks not available")
		}
		if !isAdmin(rpcAuthID(ctx)) {
			task, err := h.Ag.BgTaskManager().Status(p.TaskID)
			if err != nil {
				return fmt.Errorf("access denied: task not found")
			}
			if owner := sessionKeyOwner(task.SessionKey()); owner != "" && owner != bizID {
				return fmt.Errorf("access denied")
			}
		}
		return h.Ag.BgTaskManager().Kill(p.TaskID)
	})
	t["cleanup_completed_bg_tasks"] = rpc1(func(ctx context.Context, p struct {
		SessionKey string `json:"session_key"`
	}) (bool, error) {
		bizID := rpcBizID(ctx)
		if !isAdmin(rpcAuthID(ctx)) && p.SessionKey != "" {
			if owner := sessionKeyOwner(p.SessionKey); owner != "" && owner != bizID {
				return false, fmt.Errorf("access denied")
			}
		}
		if h.Ag.BgTaskManager() != nil {
			h.Ag.BgTaskManager().RemoveCompletedTasks(p.SessionKey)
		}
		return true, nil
	})

	// ── Tenants ──
	t["list_tenants"] = rpc0err(h.listTenants)
}

// ── Admin-only handlers ──

func registerAdminHandlers(t RPCTable, h *RPCContext) {
	cfg := h.Cfg
	disp := h.Disp
	msgBus := h.MsgBus

	t["reset_token_state"] = h.requireAdmin(rpc0void(func(ctx context.Context) error {
		// no-op in local mode
		return nil
	}))
	t["get_channel_config"] = h.requireAdmin(rpc0err(func(ctx context.Context) (any, error) {
		return getChannelConfigs()
	}))
	t["set_channel_config"] = h.requireAdmin(rpc1(func(ctx context.Context, p struct {
		Channel string            `json:"channel"`
		Values  map[string]string `json:"values"`
	}) (any, error) {
		if err := setChannelConfig(p.Channel, p.Values, h.reconfigureFn); err != nil {
			return nil, err
		}
		enabledVal, ok := p.Values["enabled"]
		if !ok {
			// Fallback: web channel historically used "enable", not "enabled".
			enabledVal, ok = p.Values["enable"]
		}
		if ok && disp != nil && msgBus != nil {
			enabled, _ := strconv.ParseBool(enabledVal)
			_, alreadyRunning := disp.GetChannel(p.Channel)
			if enabled && !alreadyRunning {
				if ch := createChannelInstance(p.Channel, cfg, msgBus); ch != nil {
					disp.Register(ch)
					go func(n string, c channel.Channel) {
						defer func() {
							if r := recover(); r != nil {
								log.WithField("channel", n).Error("Dynamic channel start panicked\n" + string(debug.Stack()))
							}
						}()
						if err := c.Start(); err != nil {
							log.WithError(err).WithField("channel", n).Error("Dynamic channel failed")
						}
					}(ch.Name(), ch)
				}
			} else if !enabled && alreadyRunning {
				disp.Unregister(p.Channel)
			}
		}
		return nil, nil
	}))

	// ── Web user management (admin only) ──
	t["create_web_user"] = h.requireAdmin(rpc1(func(ctx context.Context, p struct {
		Username string `json:"username"`
	}) (any, error) {
		_, password, err := channel.CreateWebUser(h.Ag.MultiSession().DB().Conn(), p.Username)
		if err != nil {
			return nil, err
		}
		return map[string]string{"password": password}, nil
	}))
	t["list_web_users"] = h.requireAdmin(rpc0err(func(ctx context.Context) (any, error) {
		return channel.ListWebUsers(h.Ag.MultiSession().DB().Conn())
	}))
	t["delete_web_user"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		Username string `json:"username"`
	}) error {
		return channel.DeleteWebUser(h.Ag.MultiSession().DB().Conn(), p.Username)
	}))
}

// ── Plugin system RPCs (remote CLI support) ──

func registerPluginHandlers(t RPCTable, h *RPCContext) {

	t["plugin_status"] = rpc0err(func(ctx context.Context) (any, error) {
		pm := h.Ag.PluginManager()
		if pm == nil {
			return nil, fmt.Errorf("plugin system not available")
		}
		entries := pm.ListPlugins()
		type pluginJSON struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Version string `json:"version"`
			State   string `json:"state"`
			Runtime string `json:"runtime"`
		}
		plugins := make([]pluginJSON, len(entries))
		for i, e := range entries {
			plugins[i] = pluginJSON{
				ID:      e.Manifest.ID,
				Name:    e.Manifest.Name,
				Version: e.Manifest.Version,
				State:   string(e.State),
				Runtime: string(e.Manifest.Runtime),
			}
		}
		return map[string]any{
			"plugins": plugins,
			"active":  pm.ActiveCount(),
			"total":   len(entries),
		}, nil
	})

	t["plugin_widgets"] = rpc1(func(ctx context.Context, p struct {
		ChatID string `json:"chat_id"`
	}) (any, error) {
		pm := h.Ag.PluginManager()
		if pm == nil {
			return map[string]any{"zones": map[string]string{}, "infos": []struct{}{}}, nil
		}
		h.pluginWidgetsMu.Lock()
		defer h.pluginWidgetsMu.Unlock()

		// Look up the session CWD using the chat_id sent by the CLI client.
		// Each CLI window has a session keyed by its working directory path.
		getCWD := func(cid string) string {
			cwd := ""
			if cid != "" && h.Ag.MultiSession() != nil {
				if sess, err := h.Ag.MultiSession().GetOrCreateSession("cli", cid); err == nil {
					cwd = sess.GetCurrentDir()
				}
			}
			return cwd
		}
		wr := pm.WidgetRegistry()

		// Render per-workDir using shared function — same logic as WS push path.
		zones := plugin.RenderSessionWidgets(wr, getCWD, p.ChatID)
		return map[string]any{
			"zones": zones,
			"infos": pm.WidgetInfoForWorkDir(getCWD(p.ChatID)),
			"count": wr.Count(),
		}, nil
	})

	t["plugin_reload"] = rpc1(func(ctx context.Context, p struct {
		ID string `json:"id"`
	}) (any, error) {
		pm := h.Ag.PluginManager()
		if pm == nil {
			return nil, fmt.Errorf("plugin system not available")
		}
		if err := pm.Reload(context.Background(), p.ID); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	})

	t["plugin_reload_all"] = rpc0err(func(ctx context.Context) (any, error) {
		pm := h.Ag.PluginManager()
		if pm == nil {
			return nil, fmt.Errorf("plugin system not available")
		}
		if err := pm.ReloadAll(context.Background()); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	})

	t["plugin_install"] = rpc1(func(ctx context.Context, p struct {
		SourceDir string `json:"source_dir"`
	}) (any, error) {
		pm := h.Ag.PluginManager()
		if pm == nil {
			return nil, fmt.Errorf("plugin system not available")
		}
		entry, err := pm.InstallPlugin(context.Background(), p.SourceDir)
		if err != nil {
			return nil, err
		}
		return map[string]string{
			"id":  entry.Manifest.ID,
			"dir": entry.Dir,
		}, nil
	})

	t["plugin_uninstall"] = rpc1(func(ctx context.Context, p struct {
		ID string `json:"id"`
	}) (any, error) {
		pm := h.Ag.PluginManager()
		if pm == nil {
			return nil, fmt.Errorf("plugin system not available")
		}
		if err := pm.UninstallPlugin(context.Background(), p.ID); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	})

	t["plugin_health"] = rpc0err(func(ctx context.Context) (any, error) {
		pm := h.Ag.PluginManager()
		if pm == nil {
			return nil, fmt.Errorf("plugin system not available")
		}
		results := pm.HealthCheck(context.Background())
		out := make(map[string]string, len(results))
		for id, err := range results {
			if err != nil {
				out[id] = err.Error()
			} else {
				out[id] = "ok"
			}
		}
		return out, nil
	})

	t["plugin_metrics"] = rpc0err(func(ctx context.Context) (any, error) {
		pm := h.Ag.PluginManager()
		if pm == nil {
			return nil, fmt.Errorf("plugin system not available")
		}
		return pm.Metrics(), nil
	})
}

// HandleCLIRPC dispatches RPC requests from CLI RemoteBackend clients.
func HandleCLIRPC(table RPCTable, method string, params json.RawMessage, senderID string) (json.RawMessage, error) {
	bizID := senderIDFromParams(params, senderID)
	ctx := WithRPCCtx(context.Background(), senderID, bizID)
	return table.Dispatch(ctx, method, params)
}

// ── Complex subscription handlers (extracted for readability) ──

func (h *RPCContext) listSubscriptions(ctx context.Context) ([]channel.Subscription, error) {
	bizID := rpcBizID(ctx)
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return []channel.Subscription{}, nil
	}
	subs, err := svc.List(bizID)
	if err != nil {
		return nil, err
	}
	result := make([]channel.Subscription, len(subs))
	for i, s := range subs {
		result[i] = subToChannel(s)
	}
	return result, nil
}

func (h *RPCContext) getDefaultSubscription(ctx context.Context) (*channel.Subscription, error) {
	bizID := rpcBizID(ctx)
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return nil, nil
	}
	sub, err := svc.GetDefault(bizID)
	if err != nil {
		return nil, err
	}
	if sub == nil {
		return nil, nil
	}
	ch := subToChannel(sub)
	return &ch, nil
}

func (h *RPCContext) updateSubscription(ctx context.Context, p struct {
	ID  string `json:"id"`
	Sub struct {
		Name            string                           `json:"name"`
		Provider        string                           `json:"provider"`
		BaseURL         string                           `json:"base_url"`
		APIKey          string                           `json:"api_key"`
		Model           string                           `json:"model"`
		Active          bool                             `json:"active"`
		MaxOutputTokens int                              `json:"max_output_tokens"`
		ThinkingMode    string                           `json:"thinking_mode"`
		PerModelConfigs map[string]sqlite.PerModelConfig `json:"per_model_configs"`
	} `json:"sub"`
}) error {
	bizID := rpcBizID(ctx)
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return err
	}
	existing, err := svc.Get(p.ID)
	if err != nil {
		return err
	}
	if !isAdmin(rpcAuthID(ctx)) && existing.SenderID != bizID {
		return fmt.Errorf("subscription not found")
	}
	// Start from existing subscription — client never has unmasked credentials,
	// so we preserve all existing fields and only accept intentional changes.
	dbSub := &sqlite.LLMSubscription{
		ID: existing.ID, SenderID: existing.SenderID,
		Name: existing.Name, Provider: existing.Provider, BaseURL: existing.BaseURL,
		APIKey: existing.APIKey, Model: existing.Model,
		MaxOutputTokens: existing.MaxOutputTokens,
		MaxContext:      existing.MaxContext,
		ThinkingMode:    existing.ThinkingMode, IsDefault: existing.IsDefault,
		PerModelConfigs: existing.PerModelConfigs,
		CreatedAt:       existing.CreatedAt, UpdatedAt: existing.UpdatedAt,
	}
	// --- Accept only fields the client can legitimately change ---
	// PerModelConfigs: always accept (max_context per model)
	if p.Sub.PerModelConfigs != nil {
		dbSub.PerModelConfigs = p.Sub.PerModelConfigs
	}
	// Name: accept if non-empty
	if strings.TrimSpace(p.Sub.Name) != "" {
		dbSub.Name = p.Sub.Name
	}
	// ThinkingMode: always accept
	dbSub.ThinkingMode = p.Sub.ThinkingMode
	// MaxOutputTokens: always accept
	dbSub.MaxOutputTokens = p.Sub.MaxOutputTokens
	// Model: accept if non-empty
	if strings.TrimSpace(p.Sub.Model) != "" {
		dbSub.Model = p.Sub.Model
	}
	// Provider: accept only if non-empty AND non-masked
	if strings.TrimSpace(p.Sub.Provider) != "" && !strings.Contains(p.Sub.Provider, "****") {
		dbSub.Provider = p.Sub.Provider
	}
	// BaseURL: accept only if non-empty AND non-masked
	if strings.TrimSpace(p.Sub.BaseURL) != "" && !strings.Contains(p.Sub.BaseURL, "****") {
		dbSub.BaseURL = p.Sub.BaseURL
	}
	// APIKey: accept only if non-masked (real key from sub panel edit)
	if p.Sub.APIKey != "" && !strings.HasSuffix(p.Sub.APIKey, "****") {
		dbSub.APIKey = p.Sub.APIKey
	}
	if err := svc.Update(dbSub); err != nil {
		return err
	}
	// Use InvalidateSender (user-level only) instead of Invalidate (all sessions).
	// Updating a subscription's fields (name, model, key) should NOT wipe every
	// session's per-session LLM override. Only the user-level default is affected.
	h.Ag.LLMFactory().InvalidateSender(existing.SenderID)
	if existing.IsDefault {
		h.Ag.LLMFactory().SwitchSubscription(bizID, dbSub, "")
	}
	return nil
}

func (h *RPCContext) setDefaultSubscription(ctx context.Context, p struct {
	ID     string `json:"id"`
	ChatID string `json:"chat_id"`
}) error {
	bizID := rpcBizID(ctx)
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return err
	}
	sub, err := svc.Get(p.ID)
	if err != nil {
		return err
	}
	if !isAdmin(rpcAuthID(ctx)) && sub.SenderID != bizID {
		return fmt.Errorf("subscription not found")
	}
	if p.ChatID != "" {
		// Per-session switch: only update per-chat cache, do NOT modify
		// the global default subscription or invalidate other sessions.
		return h.Ag.LLMFactory().SetSessionLLM(bizID, p.ChatID, sub)
	}
	// Global switch: update DB default + invalidate all caches + set per-user LLM
	if err := svc.SetDefault(p.ID); err != nil {
		return err
	}
	h.Ag.LLMFactory().Invalidate(bizID)
	return h.Ag.LLMFactory().SwitchSubscription(bizID, sub, "")
}

func (h *RPCContext) setSubscriptionModel(ctx context.Context, p struct {
	ID    string `json:"id"`
	Model string `json:"model"`
}) error {
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return err
	}
	sub, err := svc.Get(p.ID)
	if err != nil {
		return err
	}
	if !isAdmin(rpcAuthID(ctx)) && sub.SenderID != rpcBizID(ctx) {
		return fmt.Errorf("subscription not found")
	}
	if err := svc.SetModel(p.ID, p.Model); err != nil {
		return err
	}
	updated, err := svc.Get(p.ID)
	if err != nil {
		return err
	}
	if updated != nil {
		if def, _ := svc.GetDefault(updated.SenderID); def != nil && def.ID == updated.ID {
			h.Ag.LLMFactory().Invalidate(updated.SenderID)
			if err := h.Ag.LLMFactory().SwitchSubscription(updated.SenderID, updated, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *RPCContext) listTenants(ctx context.Context) (any, error) {
	bizID := rpcBizID(ctx)
	if h.Ag.MultiSession() == nil {
		return []struct{}{}, nil
	}
	db := h.Ag.MultiSession().DB()
	if db == nil {
		return []struct{}{}, nil
	}
	tenants, err := sqlite.NewTenantService(db).ListTenants()
	if err != nil {
		return nil, err
	}
	if !isAdmin(rpcAuthID(ctx)) {
		var userTenants []sqlite.TenantInfo
		for _, t := range tenants {
			if t.ChatID == bizID {
				userTenants = append(userTenants, t)
			}
		}
		tenants = userTenants
	}
	tenants = slices.DeleteFunc(tenants, func(t sqlite.TenantInfo) bool {
		return t.Channel == "agent"
	})
	type tenantJSON struct {
		ID           int64  `json:"id"`
		Channel      string `json:"channel"`
		ChatID       string `json:"chat_id"`
		Label        string `json:"label,omitempty"`
		CreatedAt    string `json:"created_at"`
		LastActiveAt string `json:"last_active_at"`
	}
	result := make([]tenantJSON, len(tenants))
	for i, t := range tenants {
		result[i] = tenantJSON{t.ID, t.Channel, t.ChatID, t.Label, t.CreatedAt.Format(time.RFC3339), t.LastActiveAt.Format(time.RFC3339)}
	}
	return result, nil
}

// ── Helpers ──

func subToChannel(s *sqlite.LLMSubscription) channel.Subscription {
	return channel.Subscription{
		ID: s.ID, Name: s.Name, Provider: s.Provider,
		BaseURL: s.BaseURL, APIKey: maskAPIKey(s.APIKey),
		Model: s.Model, Active: s.IsDefault,
		MaxOutputTokens: s.MaxOutputTokens, MaxContext: s.MaxContext,
		ThinkingMode:    s.ThinkingMode,
		PerModelConfigs: s.PerModelConfigs,
	}
}

type bgTaskJSON struct {
	ID         string `json:"id"`
	Command    string `json:"command"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Output     string `json:"output"`
	ExitCode   int    `json:"exit_code"`
	Error      string `json:"error,omitempty"`
}

func marshalBgTasks(tasks []*tools.BackgroundTask) []bgTaskJSON {
	result := make([]bgTaskJSON, len(tasks))
	for i, t := range tasks {
		result[i] = bgTaskJSON{t.ID, t.Command, string(t.Status), t.StartedAt.Format(time.RFC3339), "", t.Output, t.ExitCode, t.Error}
		if t.FinishedAt != nil {
			result[i].FinishedAt = t.FinishedAt.Format(time.RFC3339)
		}
	}
	return result
}

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
	"xbot/channel/web"
	"xbot/config"
	llm_pkg "xbot/llm"
	log "xbot/logger"
	"xbot/plugin"
	"xbot/protocol"
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
		if !isAdmin(ctx) {
			return nil, fmt.Errorf("admin only")
		}
		return next(ctx, params)
	}
}

// ownOrAdmin checks that the caller owns the complete channel+chatID resource
// or has admin privileges. Callers must resolve empty defaults before checking.
// Also checks canonical session ownership: if the session (channel+chatID) is
// owned by the same canonical user_id, access is granted even if chatID != bizID.
func (h *RPCContext) ownOrAdmin(ctx context.Context, channel, chatID string) bool {
	if isAdmin(ctx) {
		return true
	}
	if channel == "" || chatID == "" {
		return false
	}
	if channel == "web" && chatID == rpcBizID(ctx) {
		return true
	}
	// A newly created Web chat is recorded in user_chats before its first
	// message or CWD update creates the corresponding tenant row.
	if channel == "web" && h.webChatOwnedBySender(chatID, rpcAuthID(ctx)) {
		return true
	}
	if channel == "agent" && h.agentSessionOwnedBySender(chatID, rpcAuthID(ctx), rpcUserID(ctx)) {
		return true
	}
	// Check canonical session ownership
	if userID := rpcUserID(ctx); userID > 0 {
		return h.sessionOwnedByUser(channel, chatID, userID)
	}
	return false
}

func (h *RPCContext) resolveOwnedSession(ctx context.Context, channel, chatID, defaultChannel string) (string, string, error) {
	if channel == "" {
		channel = defaultChannel
	}
	if chatID == "" {
		chatID = rpcBizID(ctx)
	}
	if !h.ownOrAdmin(ctx, channel, chatID) {
		return "", "", fmt.Errorf("access denied")
	}
	return channel, chatID, nil
}

func (h *RPCContext) agentSessionOwnedBySender(chatID, senderID string, userID int64) bool {
	if senderID == "" || h.Ag == nil || h.Ag.MultiSession() == nil || h.Ag.MultiSession().DB() == nil {
		return false
	}
	if !h.agentTenantExists(chatID) {
		return false
	}
	for depth := 0; depth < 32; depth++ {
		parentChannel, parentChatID := sessionKeyParent(chatID)
		if parentChannel == "" || parentChatID == "" {
			return false
		}
		switch parentChannel {
		case "web":
			return parentChatID == senderID || h.webChatOwnedBySender(parentChatID, senderID)
		case "agent":
			if !h.agentTenantExists(parentChatID) {
				return false
			}
			chatID = parentChatID
		default:
			return userID > 0 && h.sessionOwnedByUser(parentChannel, parentChatID, userID)
		}
	}
	return false
}

func (h *RPCContext) agentTenantExists(chatID string) bool {
	var count int
	err := h.Ag.MultiSession().DB().Conn().QueryRow(
		`SELECT COUNT(*) FROM tenants WHERE channel = 'agent' AND chat_id = ?`, chatID,
	).Scan(&count)
	return err == nil && count > 0
}

func sessionKeyParent(key string) (string, string) {
	slash := strings.LastIndex(key, "/")
	if slash <= 0 {
		return "", ""
	}
	parent := key[:slash]
	parts := strings.SplitN(parent, ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func (h *RPCContext) webChatOwnedBySender(chatID, senderID string) bool {
	if senderID == "" || h.Ag == nil || h.Ag.MultiSession() == nil || h.Ag.MultiSession().DB() == nil {
		return false
	}
	var count int
	err := h.Ag.MultiSession().DB().Conn().QueryRow(
		`SELECT COUNT(*) FROM user_chats WHERE channel = 'web' AND sender_id = ? AND chat_id = ?`,
		senderID, chatID,
	).Scan(&count)
	return err == nil && count > 0
}

// sessionOwnedByUser checks if a tenant (channel+chatID) has owner_user_id == userID.
func (h *RPCContext) sessionOwnedByUser(channel, chatID string, userID int64) bool {
	if h.Ag == nil || h.Ag.MultiSession() == nil || h.Ag.MultiSession().DB() == nil {
		return false
	}
	var ownerUserID int64
	err := h.Ag.MultiSession().DB().Conn().QueryRow(
		`SELECT COALESCE(owner_user_id, 0) FROM tenants WHERE channel = ? AND chat_id = ?`,
		channel, chatID,
	).Scan(&ownerUserID)
	if err != nil {
		return false
	}
	return ownerUserID == userID
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

// contextUsage returns the latest exact context snapshot without creating a
// tenant session. The persisted user-message token count is authoritative; the
// tenant token state is only a fallback for sessions without that value.
func (h *RPCContext) contextUsage(ctx context.Context, senderID, channelName, chatID string) (protocol.ContextUsage, error) {
	var usage protocol.ContextUsage
	defaultMaxContext := 0
	if h.Cfg != nil {
		defaultMaxContext = h.Cfg.Agent.MaxContextTokens
	}
	if h.Ag != nil && h.Ag.LLMFactory() != nil {
		usage.SubscriptionID, usage.SubscriptionName, usage.Model, usage.MaxContextTokens =
			h.Ag.LLMFactory().ResolveContextConfig(senderID, chatID, channelName, defaultMaxContext)
	} else {
		usage.MaxContextTokens = defaultMaxContext
	}

	if h.Ag == nil || h.Ag.MultiSession() == nil || h.Ag.MultiSession().DB() == nil {
		return usage, nil
	}
	db := h.Ag.MultiSession().DB()
	tenantID, err := sqlite.NewTenantService(db).GetTenantIDByChannelChatID(channelName, chatID)
	if err != nil {
		return usage, err
	}
	if tenantID == 0 {
		return usage, nil
	}

	promptTokens, err := sqlite.NewSessionService(db).GetLastUserMessageContextTokens(tenantID)
	if err != nil {
		return usage, err
	}
	statePromptTokens, stateCompletionTokens, err := sqlite.NewMemoryService(db).GetTokenState(ctx, tenantID)
	if err != nil {
		return usage, err
	}
	if promptTokens > 0 {
		usage.PromptTokens = promptTokens
	} else if statePromptTokens > 0 {
		usage.PromptTokens = statePromptTokens
	}
	if statePromptTokens > 0 {
		usage.CompletionTokens = stateCompletionTokens
	}

	usage.Available = usage.PromptTokens > 0
	if usage.Available && usage.MaxContextTokens > 0 {
		percent := float64(usage.PromptTokens) / float64(usage.MaxContextTokens) * 100
		usage.UsagePercent = &percent
	}
	return usage, nil
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
	registerCommandHandlers(t, h)
	registerPluginHandlers(t, h)
	registerRunnerHandlers(t, h)
	return t
}

func registerCommandHandlers(t RPCTable, h *RPCContext) {
	t["list_command_names"] = rpc0(func(ctx context.Context) any {
		if h.Ag == nil {
			return []string{}
		}
		return h.Ag.CommandNames()
	})
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
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "web")
		if err != nil {
			return err
		}
		// SetCWD internally refreshes plugin workDir with correct tenantID
		return h.Ag.SetCWD(channelName, chatID, p.Dir)
	})
	t["get_settings"] = rpc1(func(ctx context.Context, p struct {
		Namespace string `json:"namespace"`
		SenderID  string `json:"sender_id"`
	}) (any, error) {
		userID := rpcUserID(ctx)
		if h.Ag.SettingsService() == nil {
			return nil, errSettingsUnavailable
		}
		result, err := h.Ag.SettingsService().GetByUserID(p.Namespace, userID)
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
				if mc := h.Ag.LLMFactory().GetEffectiveMaxContext(rpcBizID(ctx), ""); mc > 0 {
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
		userID := rpcUserID(ctx)
		switch p.Key {
		case "llm_provider", "llm_api_key", "llm_model", "llm_base_url":
			return nil
		}
		// Global-scoped keys (sandbox_mode) are server-level config, not per-user.
		if channel.IsGlobalScopedSettingKey(p.Key) {
			if isAdmin(ctx) {
				applyRuntimeSetting(h.Cfg, h.Ag, bizID, p.Key, p.Value)
			}
			return nil
		}
		// Subscription-scoped keys (max_context_tokens) are stored in the
		// subscription's PerModelConfigs, NOT in user_settings DB.
		if channel.IsSubscriptionScopedSettingKey(p.Key) {
			if isAdmin(ctx) {
				applyRuntimeSetting(h.Cfg, h.Ag, bizID, p.Key, p.Value)
			}
			return nil
		}
		if h.Ag.SettingsService() == nil {
			return errSettingsUnavailable
		}
		// Write by canonical user_id — all identities linked to the same user
		// share the same settings.
		if err := h.Ag.SettingsService().SetByUserID(p.Namespace, userID, p.Key, p.Value); err != nil {
			return err
		}
		if isAdmin(ctx) {
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
		// Model-first: resolve the user's last-used (sub, model) pair via
		// the model-first chain (sessionMemo → tenants → user_default_model),
		// NOT the subscription's default Model field.
		_, model, err := h.Ag.LLMFactory().ResolveActiveSubModel(bizID, "", "")
		if err != nil || model == "" {
			// Fallback to GetLLM resolution
			_, m, _, _, _ := h.Ag.LLMFactory().GetLLM(bizID)
			model = m
		}
		log.WithField("sender_id", bizID).WithField("model", model).Debug("RPC get_default_model")
		return model
	})
	t["set_user_model"] = rpc1void(func(ctx context.Context, p struct {
		SubID string `json:"sub_id"`
		Model string `json:"model"`
	}) error {
		return h.Ag.SetUserModel(rpcBizID(ctx), p.SubID, p.Model)
	})
	// select_model: model-first per-session selection. Sets (subID, model) for a
	// chat via the ResolveLLM/SelectModel path.
	t["select_model"] = rpc1void(func(ctx context.Context, p struct {
		SubID   string `json:"sub_id"`
		Model   string `json:"model"`
		ChatID  string `json:"chat_id,omitempty"`
		Channel string `json:"channel,omitempty"`
	}) error {
		bizID := rpcBizID(ctx)
		channel := p.Channel
		if channel == "" {
			channel = "cli"
		}
		if p.ChatID == "" {
			return fmt.Errorf("select_model requires a chat_id (use set_default_model for the user-level default)")
		}
		return h.Ag.LLMFactory().SelectModel(bizID, p.ChatID, channel, p.SubID, p.Model)
	})
	// set_default_model: model-first user-level default (subscription, model).
	t["set_default_model"] = rpc1void(func(ctx context.Context, p struct {
		SubID string `json:"sub_id"`
		Model string `json:"model"`
	}) error {
		return h.Ag.LLMFactory().SetUserDefaultModel(rpcBizID(ctx), p.SubID, p.Model)
	})
	// set_model_enabled: toggle a model's enabled flag (model disable feature).
	t["set_model_enabled"] = rpc1void(func(ctx context.Context, p struct {
		SubID   string `json:"sub_id"`
		Model   string `json:"model"`
		Enabled bool   `json:"enabled"`
	}) error {
		return h.Ag.LLMFactory().SetModelEnabled(p.SubID, p.Model, p.Enabled)
	})
	// remove_model: permanently delete a model from subscription_models.
	t["remove_model"] = rpc1void(func(ctx context.Context, p struct {
		SubID string `json:"sub_id"`
		Model string `json:"model"`
	}) error {
		if p.SubID == "" || p.Model == "" {
			return fmt.Errorf("sub_id and model are required")
		}
		svc := h.Ag.LLMFactory().GetSubscriptionSvc()
		if svc == nil {
			return fmt.Errorf("subscription service not available")
		}
		return svc.RemoveModel(p.SubID, p.Model)
	})
	// upsert_model: insert or update a model in subscription_models.
	t["upsert_model"] = rpc1void(func(ctx context.Context, p struct {
		SubID        string `json:"sub_id"`
		Model        string `json:"model"`
		MaxContext   int    `json:"max_context"`
		MaxOutput    int    `json:"max_output"`
		APIType      string `json:"api_type"`
		ThinkingMode string `json:"thinking_mode"`
	}) error {
		if p.SubID == "" || p.Model == "" {
			return fmt.Errorf("sub_id and model are required")
		}
		svc := h.Ag.LLMFactory().GetSubscriptionSvc()
		if svc == nil {
			return fmt.Errorf("subscription service not available")
		}
		return svc.UpsertModel(p.SubID, p.Model, p.MaxContext, p.MaxOutput, p.APIType, p.ThinkingMode)
	})
	// set_subscription_enabled: toggle a subscription's enabled flag (v40). A
	// disabled subscription stops contributing models to the picker.
	t["set_subscription_enabled"] = rpc1void(func(ctx context.Context, p struct {
		SubID   string `json:"sub_id"`
		Enabled bool   `json:"enabled"`
	}) error {
		return h.Ag.LLMFactory().SetSubscriptionEnabled(p.SubID, p.Enabled)
	})
	t["get_user_max_context"] = rpc0(func(ctx context.Context) int { return h.Ag.GetUserMaxContext(rpcBizID(ctx), "", "") })
	t["set_user_max_context"] = rpc1void(func(ctx context.Context, p struct {
		MaxContext int `json:"max_context"`
	}) error {
		return h.Ag.SetUserMaxContext(rpcBizID(ctx), "", "", p.MaxContext)
	})
	t["get_user_max_output_tokens"] = rpc0(func(ctx context.Context) int { return h.Ag.GetUserMaxOutputTokens(rpcBizID(ctx), "", "") })
	t["set_user_max_output_tokens"] = rpc1void(func(ctx context.Context, p struct {
		MaxTokens int `json:"max_tokens"`
	}) error {
		return h.Ag.SetUserMaxOutputTokens(rpcBizID(ctx), "", "", p.MaxTokens)
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
		client, _, _, _, _ := h.Ag.LLMFactory().GetLLM(rpcBizID(ctx))
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
	t["list_all_model_entries"] = rpc0err(func(ctx context.Context) ([]protocol.ModelEntry, error) {
		if h.Ag.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		entries := h.Ag.LLMFactory().ListAllModelEntriesForUserID(rpcUserID(ctx))
		log.WithField("count", len(entries)).Debug("RPC list_all_model_entries")
		return entries, nil
	})
	t["refresh_model_entries"] = rpc0err(func(ctx context.Context) ([]protocol.ModelEntry, error) {
		if h.Ag.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		entries := h.Ag.LLMFactory().RefreshModelEntriesForUser(rpcBizID(ctx))
		log.WithField("count", len(entries)).Info("RPC refresh_model_entries")
		return entries, nil
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
	t["get_session_subscription"] = rpc1(func(ctx context.Context, p struct {
		ChatID  string `json:"chat_id"`
		Channel string `json:"channel,omitempty"`
	}) (any, error) {
		bizID := rpcBizID(ctx)
		channel := p.Channel
		if channel == "" {
			channel = "cli"
		}
		// Read from tenants table (persistent backend source of truth).
		if h.Ag.MultiSession() != nil && h.Ag.MultiSession().DB() != nil {
			subID, model, _ := sqlite.NewTenantService(h.Ag.MultiSession().DB()).GetTenantSubscription(channel, p.ChatID)
			if subID != "" {
				return map[string]string{"subscription_id": subID, "model": model}, nil
			}
		}
		// Fallback: try LLMFactory in-memory cache (survives until server restart).
		if llmClient, model, _, _, _ := h.Ag.LLMFactory().GetLLMForChat(bizID, p.ChatID); llmClient != nil {
			return map[string]string{"model": model}, nil
		}
		return map[string]string{}, nil
	})
	t["add_subscription"] = rpc1void(func(ctx context.Context, p struct {
		Sub struct {
			ID              string                             `json:"id"`
			Name            string                             `json:"name"`
			Provider        string                             `json:"provider"`
			BaseURL         string                             `json:"base_url"`
			APIKey          string                             `json:"api_key"`
			Model           string                             `json:"model"`
			Active          bool                               `json:"active"`
			MaxOutputTokens int                                `json:"max_output_tokens"`
			ThinkingMode    string                             `json:"thinking_mode"`
			PerModelConfigs map[string]protocol.PerModelConfig `json:"per_model_configs"`
		} `json:"sub"`
	}) error {
		svc, err := h.requireSubscriptionSvc()
		if err != nil {
			return err
		}
		dbSub := &sqlite.LLMSubscription{
			ID:              p.Sub.ID,
			Name:            p.Sub.Name,
			Provider:        p.Sub.Provider,
			BaseURL:         p.Sub.BaseURL,
			APIKey:          p.Sub.APIKey,
			Model:           p.Sub.Model,
			MaxOutputTokens: p.Sub.MaxOutputTokens,
			ThinkingMode:    p.Sub.ThinkingMode,
			IsDefault:       p.Sub.Active,
		}
		if len(p.Sub.PerModelConfigs) > 0 {
			dbSub.PerModelConfigs = make(map[string]sqlite.PerModelConfig, len(p.Sub.PerModelConfigs))
			for model, cfg := range p.Sub.PerModelConfigs {
				dbSub.PerModelConfigs[model] = sqlite.PerModelConfig(cfg)
			}
		}
		dbSub.SenderID = rpcBizID(ctx)
		if err := svc.Add(dbSub); err != nil {
			return err
		}
		// Write user_id so ListByUserID can find this subscription.
		// Add() doesn't write user_id (legacy schema), so we patch it here.
		if uid := rpcUserID(ctx); uid > 0 {
			svc.SetSubscriptionUserID(dbSub.ID, uid)
		}
		return nil
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
		if !isAdmin(ctx) && existing.SenderID != bizID {
			return fmt.Errorf("subscription not found")
		}
		if existing.PerModelConfigs == nil {
			existing.PerModelConfigs = make(map[string]sqlite.PerModelConfig)
		}
		existing.PerModelConfigs[p.Model] = p.Config
		if err := svc.Update(existing); err != nil {
			return err
		}
		// Also write to subscription_models table (authoritative source for v35+)
		svc.UpsertModel(existing.ID, p.Model, p.Config.MaxContext, p.Config.MaxOutputTokens, "", p.Config.APIType)
		// Invalidate ALL cached entries for this sender (user-level + per-session).
		// Must use Invalidate() not InvalidateSender() because per-session entries
		// (senderID:chatID keys) hold a cached *LLMSubscription pointer with stale
		// PerModelConfigs. InvalidateSender only clears the user-level entry, so
		// GetLLMForChat hits the per-chat cache and returns the old MaxContext.
		h.Ag.LLMFactory().Invalidate(bizID)
		// Drop the new-path client cache + session memos for this subscription so
		// ResolveLLM picks up the new per-model config.
		h.Ag.LLMFactory().InvalidateSubscription(existing.ID)
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
		if !isAdmin(ctx) && sub.SenderID != rpcBizID(ctx) {
			return fmt.Errorf("subscription not found")
		}
		if err := svc.Remove(p.ID); err != nil {
			return err
		}
		// Drop both the legacy entries map and the new clientCache/sessionMemo
		// for this subscription. Invalidate(senderID) clears legacy entries;
		// InvalidateSubscription(subID) drops the per-subscription client cache
		// used by ResolveLLM.
		h.Ag.LLMFactory().Invalidate(sub.SenderID)
		h.Ag.LLMFactory().InvalidateSubscription(sub.ID)
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
		if !isAdmin(ctx) && sub.SenderID != rpcBizID(ctx) {
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
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "web")
		if err != nil {
			return err
		}
		return h.Ag.MultiSession().ClearMemory(context.Background(), channelName, chatID, p.TargetType, rpcBizID(ctx))
	})
	t["get_memory_stats"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		if err := h.requireMultiSession(); err != nil {
			return nil, err
		}
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "web")
		if err != nil {
			return nil, err
		}
		return h.Ag.MultiSession().GetMemoryStats(context.Background(), channelName, chatID, rpcBizID(ctx)), nil
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
		if !isAdmin(ctx) && p.ChatID == "" {
			p.ChatID = rpcBizID(ctx)
		}
		if p.Channel == "" {
			p.Channel = "web"
		}
		if !h.ownOrAdmin(ctx, p.Channel, p.ChatID) {
			return 0, fmt.Errorf("access denied")
		}
		return h.Ag.CountInteractiveSessions(p.Channel, p.ChatID), nil
	})
	t["list_interactive_sessions"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		if !isAdmin(ctx) && p.ChatID == "" {
			p.ChatID = rpcBizID(ctx)
		}
		if p.Channel == "" {
			p.Channel = "web"
		}
		if !h.ownOrAdmin(ctx, p.Channel, p.ChatID) {
			return nil, fmt.Errorf("access denied")
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
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "web")
		if err != nil {
			return "", err
		}
		return h.Ag.InspectInteractiveSession(context.Background(), p.Role, channelName, chatID, p.Instance, p.TailCount)
	})
	t["get_session_messages"] = rpc1(func(ctx context.Context, p struct {
		Channel  string `json:"channel"`
		ChatID   string `json:"chat_id"`
		Role     string `json:"role"`
		Instance string `json:"instance"`
	}) (any, error) {
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "web")
		if err != nil {
			return nil, err
		}
		msgs, _ := h.Ag.GetSessionMessages(channelName, chatID, p.Role, p.Instance)
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
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "web")
		if err != nil {
			return nil, err
		}
		dump, _ := h.Ag.GetAgentSessionDump(channelName, chatID, p.Role, p.Instance)
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
		if !isAdmin(ctx) && !h.ownOrAdmin(ctx, "agent", p.FullKey) {
			return nil, fmt.Errorf("access denied")
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
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "web")
		if err != nil {
			return nil, err
		}
		p.Channel, p.ChatID = channelName, chatID
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
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "cli")
		if err != nil {
			return nil, err
		}
		p.Channel, p.ChatID = channelName, chatID
		bizID := rpcBizID(ctx)
		// Use bizID (cliSenderID for admin) as sender_id for DB operations,
		// because ChatRenameFn writes labels with cliSenderID, not the WS auth identity.
		senderID := bizID
		if db := h.Ag.MultiSession().DB(); db != nil {
			cs := sqlite.NewChatService(db)
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
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "cli")
		if err != nil {
			return nil, err
		}
		p.Channel, p.ChatID = channelName, chatID
		bizID := rpcBizID(ctx)
		// Use bizID (cliSenderID for admin) as sender_id for DB operations,
		// consistent with ChatRenameFn which writes labels with cliSenderID.
		senderID := bizID
		if db := h.Ag.MultiSession().DB(); db != nil {
			cs := sqlite.NewChatService(db)
			if err := cs.RenameChat(p.Channel, senderID, p.ChatID, p.NewName); err != nil {
				return nil, fmt.Errorf("rename chat: %w", err)
			}
		} else {
			return nil, fmt.Errorf("database not available")
		}
		return map[string]string{"status": "ok"}, nil
	})
	t[agent.MethodGetContextUsage] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		bizID := rpcBizID(ctx)
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "web")
		if err != nil {
			return nil, err
		}
		return h.contextUsage(ctx, bizID, channelName, chatID)
	})
	t[agent.MethodGetTokenState] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "cli")
		if err != nil {
			return nil, err
		}
		usage, err := h.contextUsage(ctx, rpcBizID(ctx), channelName, chatID)
		if err != nil {
			return nil, err
		}
		return map[string]int64{"prompt_tokens": usage.PromptTokens, "completion_tokens": usage.CompletionTokens}, nil
	})
	t["trim_history"] = rpc1void(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
		Cutoff  int64  `json:"cutoff"`
	}) error {
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "web")
		if err != nil {
			return err
		}
		p.Channel, p.ChatID = channelName, chatID
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
		if !isAdmin(ctx) && (p.ChatID == "" || !h.ownOrAdmin(ctx, p.Channel, p.ChatID)) {
			return false, fmt.Errorf("access denied")
		}
		return h.Ag.IsProcessingByChannel(p.Channel, p.ChatID), nil
	})
	t["get_active_progress"] = rpc1(func(ctx context.Context, p struct {
		Channel       string `json:"channel"`
		ChatID        string `json:"chat_id"`
		FromIteration int    `json:"from_iteration"`
	}) (any, error) {
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "web")
		if err != nil {
			return nil, err
		}
		return h.Ag.GetActiveProgress(channelName, chatID, p.FromIteration), nil
	})

	t["get_pending_ask_user"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "web")
		if err != nil {
			return nil, err
		}
		return h.Ag.GetPendingAskUser(channelName, chatID), nil
	})

	t["get_todos"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		channelName, chatID, err := h.resolveOwnedSession(ctx, p.Channel, p.ChatID, "web")
		if err != nil {
			return nil, err
		}
		return h.Ag.GetTodos(channelName, chatID), nil
	})
}

// ── Background tasks / tenants ──

func registerTaskHandlers(t RPCTable, h *RPCContext) {
	t["get_bg_task_count"] = rpc1(func(ctx context.Context, p struct {
		SessionKey string `json:"session_key"`
	}) (int, error) {
		bizID := rpcBizID(ctx)
		if !isAdmin(ctx) && p.SessionKey != "" {
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
		if !isAdmin(ctx) && p.SessionKey != "" {
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
		if !isAdmin(ctx) {
			task, err := h.Ag.BgTaskManager().Status(p.TaskID)
			if err != nil {
				return fmt.Errorf("access denied: task not found")
			}
			owner := task.SenderID()
			if owner == "" {
				owner = sessionKeyOwner(task.SessionKey())
			}
			if owner == "" || owner != bizID {
				return fmt.Errorf("access denied")
			}
		}
		return h.Ag.BgTaskManager().Kill(p.TaskID)
	})
	t["cleanup_completed_bg_tasks"] = rpc1(func(ctx context.Context, p struct {
		SessionKey string `json:"session_key"`
	}) (bool, error) {
		bizID := rpcBizID(ctx)
		if !isAdmin(ctx) && p.SessionKey != "" {
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
		_, password, err := web.CreateWebUser(h.Ag.MultiSession().DB().Conn(), p.Username)
		if err != nil {
			return nil, err
		}
		return map[string]string{"password": password}, nil
	}))
	t["list_web_users"] = h.requireAdmin(rpc0err(func(ctx context.Context) (any, error) {
		return web.ListWebUsers(h.Ag.MultiSession().DB().Conn())
	}))
	t["delete_web_user"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		Username string `json:"username"`
	}) error {
		return web.DeleteWebUser(h.Ag.MultiSession().DB().Conn(), p.Username)
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
		// Run ReloadAll with timeout — typically <5s, 30s max.
		// Since this RPC is dispatched from a concurrent agent command
		// (not readPump), synchronous execution does not cause TCP deadlock.
		resultCh := make(chan error, 1)
		go func() {
			resultCh <- pm.ReloadAll(context.Background())
		}()
		select {
		case err := <-resultCh:
			if err != nil {
				return nil, err
			}
			return map[string]string{"status": "ok"}, nil
		case <-time.After(30 * time.Second):
			return map[string]string{"status": "timeout", "message": "reload still in progress"}, nil
		}
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
	bizID := senderIDFromParams(params)
	if bizID == "" {
		// CLI path: admin is a role, not a business identity. All CLI
		// subscriptions/settings live under cliSenderID.
		if senderID == adminSenderID || senderID == cliSenderID {
			bizID = cliSenderID
		} else {
			bizID = senderID
		}
	}
	// Resolve canonical user identity via IdentityResolver when available.
	// This ensures role and userID are correctly set for all access checks.
	userID := int64(0)
	role := "user"
	if senderID == "admin" || senderID == "cli_user" {
		role = "admin"
		userID = 1
	}
	ctx := WithRPCCtxResolved(context.Background(), senderID, bizID, userID, role)
	return table.Dispatch(ctx, method, params)
}

// ── Complex subscription handlers (extracted for readability) ──

func (h *RPCContext) listSubscriptions(ctx context.Context) ([]channel.Subscription, error) {
	userID := rpcUserID(ctx)
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return []channel.Subscription{}, nil
	}
	subs, err := svc.ListByUserID(userID)
	if err != nil {
		return nil, err
	}
	result := make([]channel.Subscription, len(subs))
	for i, s := range subs {
		// PerModelConfigs is populated from the subscription_models table by the
		// storage layer (loadPerModelConfigs, v42); no merge needed here.
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
		APIType         string                           `json:"api_type"`
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
	if !isAdmin(ctx) && existing.SenderID != bizID {
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
		ThinkingMode:    existing.ThinkingMode, APIType: existing.APIType, IsDefault: existing.IsDefault,
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
	// APIType: always accept
	dbSub.APIType = p.Sub.APIType
	// MaxOutputTokens: always accept
	dbSub.MaxOutputTokens = p.Sub.MaxOutputTokens
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
	// PerModelConfigs are persisted to the subscription_models table (v42 sole source).
	// Update() no longer writes the JSON column, so persist them explicitly when provided.
	if p.Sub.PerModelConfigs != nil {
		if err := svc.UpdatePerModelConfigs(dbSub.ID, p.Sub.PerModelConfigs); err != nil {
			return fmt.Errorf("update per-model configs: %w", err)
		}
	}
	if err := svc.Update(dbSub); err != nil {
		return err
	}
	// Use InvalidateSender (user-level only) instead of Invalidate (all sessions).
	// Updating a subscription's fields (name, model, key) should NOT wipe every
	// session's per-session LLM override. Only the user-level default is affected.
	h.Ag.LLMFactory().InvalidateSender(existing.SenderID)
	// Drop the new-path client cache for this subscription so ResolveLLM rebuilds
	// the client with the updated credentials/base_url/config.
	h.Ag.LLMFactory().InvalidateSubscription(dbSub.ID)
	// If this subscription is the user's default (per user_default_model), re-switch
	// the user-level entry so defaultLLM picks up the updated fields immediately.
	if udm, _ := svc.GetUserDefaultModel(existing.SenderID); udm != nil && udm.SubscriptionID == existing.ID {
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
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("subscription id is required")
	}
	sub, err := svc.Get(p.ID)
	if err != nil {
		return err
	}
	if sub == nil {
		return fmt.Errorf("subscription not found")
	}
	if !isAdmin(ctx) && sub.SenderID != bizID {
		return fmt.Errorf("subscription not found")
	}
	if p.ChatID != "" {
		// Per-session switch: update per-chat cache AND persist to DB
		// so the session→subscription mapping survives server restarts.
		if err := h.Ag.LLMFactory().SetSessionLLM(bizID, p.ChatID, sub); err != nil {
			return err
		}
		// Persist to tenants table (backend source of truth).
		if ms := h.Ag.MultiSession(); ms != nil && ms.DB() != nil {
			if err := sqlite.NewTenantService(ms.DB()).SetTenantSubscription("cli", p.ChatID, sub.ID, sub.Model); err != nil {
				log.WithError(err).Warn("RPC setDefaultSubscription: SetTenantSubscription failed")
			}
		}
		return nil
	}
	// Global switch: update DB default + set per-user LLM.
	// Use InvalidateSender (NOT Invalidate) to preserve per-chat entries —
	// other sessions with their own per-session subscriptions must not be
	// affected by this global switch.
	if err := svc.SetDefault(p.ID); err != nil {
		return err
	}
	// Set user_default_model with empty model — the user picks a model
	// from the panel, which updates user_default_model via SelectModel.
	if err := h.Ag.LLMFactory().SetUserDefaultModel(bizID, sub.ID, ""); err != nil {
		log.WithError(err).Warn("RPC setDefaultSubscription: SetUserDefaultModel failed")
	}
	h.Ag.LLMFactory().InvalidateSender(bizID)
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
	if !isAdmin(ctx) && sub.SenderID != rpcBizID(ctx) {
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
			h.Ag.LLMFactory().InvalidateSender(updated.SenderID)
			// Drop the new-path client cache for this subscription so ResolveLLM
			// picks up the new default model + per-model config.
			h.Ag.LLMFactory().InvalidateSubscription(updated.ID)
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
	if !isAdmin(ctx) {
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
		Model        string `json:"model,omitempty"`
		CreatedAt    string `json:"created_at"`
		LastActiveAt string `json:"last_active_at"`
	}
	result := make([]tenantJSON, len(tenants))
	for i, t := range tenants {
		result[i] = tenantJSON{t.ID, t.Channel, t.ChatID, t.Label, t.Model, t.CreatedAt.Format(time.RFC3339), t.LastActiveAt.Format(time.RFC3339)}
	}
	return result, nil
}

// ── Runner CRUD ──

func registerRunnerHandlers(t RPCTable, h *RPCContext) {
	// runner_create creates a new named runner and returns the token.
	t["runner_create"] = rpc1(func(ctx context.Context, p struct {
		Name        string `json:"name"`
		Mode        string `json:"mode"`
		DockerImage string `json:"docker_image"`
		Workspace   string `json:"workspace"`
		LLMProvider string `json:"llm_provider"`
		LLMAPIKey   string `json:"llm_api_key"`
		LLMModel    string `json:"llm_model"`
		LLMBaseURL  string `json:"llm_base_url"`
	}) (any, error) {
		db := tools.GetRunnerTokenDB()
		if db == nil {
			return nil, fmt.Errorf("runner management not configured")
		}
		store := tools.NewRunnerTokenStore(db)
		bizID := rpcBizID(ctx)
		if p.Mode == "" {
			p.Mode = "native"
		}
		if p.DockerImage == "" {
			p.DockerImage = "ubuntu:22.04"
		}
		llm := tools.RunnerLLMSettings{
			Provider: p.LLMProvider,
			APIKey:   p.LLMAPIKey,
			Model:    p.LLMModel,
			BaseURL:  p.LLMBaseURL,
		}
		token, _, err := store.CreateRunner(bizID, p.Name, p.Mode, p.DockerImage, p.Workspace, llm)
		if err != nil {
			return nil, err
		}
		return map[string]string{
			"name":  p.Name,
			"token": token,
		}, nil
	})

	// runner_list returns all runners for the calling user.
	t["runner_list"] = rpc0err(func(ctx context.Context) (any, error) {
		db := tools.GetRunnerTokenDB()
		if db == nil {
			return nil, fmt.Errorf("runner management not configured")
		}
		bizID := rpcBizID(ctx)
		return tools.NewRunnerTokenStore(db).ListRunners(bizID)
	})

	// runner_delete deletes a runner by name.
	t["runner_delete"] = rpc1void(func(ctx context.Context, p struct {
		Name string `json:"name"`
	}) error {
		db := tools.GetRunnerTokenDB()
		if db == nil {
			return fmt.Errorf("runner management not configured")
		}
		bizID := rpcBizID(ctx)
		return tools.NewRunnerTokenStore(db).DeleteRunner(bizID, p.Name)
	})

	// runner_get_active returns the active runner name.
	t["runner_get_active"] = rpc0err(func(ctx context.Context) (any, error) {
		db := tools.GetRunnerTokenDB()
		if db == nil {
			return nil, fmt.Errorf("runner management not configured")
		}
		bizID := rpcBizID(ctx)
		return tools.NewRunnerTokenStore(db).GetActiveRunner(bizID)
	})

	// runner_set_active sets the active runner.
	t["runner_set_active"] = rpc1void(func(ctx context.Context, p struct {
		Name string `json:"name"`
	}) error {
		db := tools.GetRunnerTokenDB()
		if db == nil {
			return fmt.Errorf("runner management not configured")
		}
		bizID := rpcBizID(ctx)
		return tools.NewRunnerTokenStore(db).SetActiveRunner(bizID, p.Name)
	})

	// runner_rename renames a runner.
	t["runner_rename"] = rpc1void(func(ctx context.Context, p struct {
		OldName string `json:"old_name"`
		NewName string `json:"new_name"`
	}) error {
		db := tools.GetRunnerTokenDB()
		if db == nil {
			return fmt.Errorf("runner management not configured")
		}
		bizID := rpcBizID(ctx)
		return tools.NewRunnerTokenStore(db).RenameRunner(bizID, p.OldName, p.NewName)
	})

	// generate_link_code generates a cross-channel link code for the current user.
	t["generate_link_code"] = rpc0(func(ctx context.Context) any {
		if h.Ag.IdentityResolver() == nil {
			return map[string]any{"error": "identity resolver not available"}
		}
		userID := rpcUserID(ctx)
		if userID == 0 {
			userID = 1 // CLI admin fallback
		}
		code, err := h.Ag.IdentityResolver().GenerateLinkCode(userID)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{"code": code, "expires_in": 300}
	})

	// consume_link_code consumes a link code and links current identity to the target user.
	// Returns one of:
	//   {"action": "linked", "user_id": N}  — simple link, identity wasn't previously bound
	//   {"action": "merge_required", "preview": {...}} — identity is bound to another user, merge needed
	//   {"action": "merged", "user_id": N} — merge executed (confirm=true was passed)
	t["consume_link_code"] = rpc1(func(ctx context.Context, p struct {
		Code    string `json:"code"`
		Confirm bool   `json:"confirm"`
	}) (any, error) {
		if h.Ag.IdentityResolver() == nil {
			return nil, fmt.Errorf("identity resolver not available")
		}
		currentUserID := rpcUserID(ctx)
		if currentUserID == 0 {
			return nil, fmt.Errorf("cannot resolve current user identity")
		}
		// On preview (confirm=false), validate WITHOUT consuming the code
		if !p.Confirm {
			targetUserID, err := h.Ag.IdentityResolver().ValidateLinkCode(p.Code)
			if err != nil {
				return nil, err
			}
			// Try simple link first
			_, err = h.Ag.IdentityResolver().LinkIdentity(targetUserID, "cli", rpcBizID(ctx))
			if err == nil {
				// Simple link succeeded — consume the code now
				h.Ag.IdentityResolver().ConsumeLinkCode(p.Code)
				return map[string]any{"action": "linked", "user_id": targetUserID}, nil
			}
			// Merge required — return preview (code NOT consumed yet)
			preview, err := h.Ag.IdentityResolver().PreviewMerge(currentUserID, targetUserID)
			if err != nil {
				return nil, fmt.Errorf("merge preview failed: %w", err)
			}
			return map[string]any{
				"action":  "merge_required",
				"preview": preview,
				"message": "Re-send with confirm=true to execute merge.",
			}, nil
		}
		// Confirm=true: consume code and execute merge
		targetUserID, err := h.Ag.IdentityResolver().ConsumeLinkCode(p.Code)
		if err != nil {
			return nil, err
		}
		if mergeErr := h.Ag.IdentityResolver().MergeUsers(currentUserID, targetUserID); mergeErr != nil {
			return nil, fmt.Errorf("merge failed: %w", mergeErr)
		}
		return map[string]any{"action": "merged", "user_id": targetUserID}, nil
	})

	// list_identities lists the current user's linked channel identities.
	t["list_identities"] = rpc0(func(ctx context.Context) any {
		if h.Ag.IdentityResolver() == nil {
			return map[string]any{"identities": []any{}}
		}
		userID := rpcUserID(ctx)
		if userID == 0 {
			userID = 1
		}
		identities, err := h.Ag.IdentityResolver().ListIdentities(userID)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{"user_id": userID, "identities": identities}
	})
}

// ── Helpers ──

func subToChannel(s *sqlite.LLMSubscription) channel.Subscription {
	return channel.Subscription{
		ID: s.ID, Name: s.Name, Provider: s.Provider,
		BaseURL: s.BaseURL, APIKey: maskAPIKey(s.APIKey),
		Model: s.Model, Active: s.IsDefault, Enabled: s.Enabled,
		MaxOutputTokens: s.MaxOutputTokens, MaxContext: s.MaxContext,
		ThinkingMode:    s.ThinkingMode,
		APIType:         s.APIType,
		PerModelConfigs: s.PerModelConfigs,
		IsSystem:        s.IsSystem,
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

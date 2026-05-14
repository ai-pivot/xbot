package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	llm "xbot/llm"
	"xbot/protocol"
	"xbot/storage/sqlite"
)

// PerModelConfigs are already protocol.PerModelConfig (type alias), use directly.
// sqliteSubToProtocol converts a sqlite.LLMSubscription to protocol.Subscription.
func sqliteSubToProtocol(s *sqlite.LLMSubscription) protocol.Subscription {
	return protocol.Subscription{
		ID: s.ID, Name: s.Name, Provider: s.Provider,
		BaseURL: s.BaseURL, APIKey: s.APIKey, Model: s.Model, Active: s.IsDefault,
		MaxOutputTokens: s.MaxOutputTokens, ThinkingMode: s.ThinkingMode,
		PerModelConfigs: s.PerModelConfigs,
	}
}

// localTransport is the in-process "server" for local mode.
// Its Call() method dispatches to a handler table that directly operates on *Agent.
// This eliminates all local/remote branching in Backend — every call is a transport.Call().
type localTransport struct {
	baseTransport

	agent         *Agent
	bus           *bus.MessageBus
	reconfigureFn func(channel string)
	handlers      map[string]func(json.RawMessage) (json.RawMessage, error)
}

func newLocalTransport(agent *Agent, bus *bus.MessageBus) *localTransport {
	t := &localTransport{
		baseTransport: newBaseTransport(),
		agent:         agent,
		bus:           bus,
		handlers:      make(map[string]func(json.RawMessage) (json.RawMessage, error), 64),
	}
	t.registerHandlers()
	return t
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func (t *localTransport) Start(ctx context.Context) error {
	go t.agent.Run(ctx)
	return nil
}
func (t *localTransport) Stop()                         { _ = t.agent.Close() }
func (t *localTransport) Close() error                  { return t.agent.Close() }
func (t *localTransport) Run(ctx context.Context) error { return t.agent.Run(ctx) }

// ---------------------------------------------------------------------------
// Communication
// ---------------------------------------------------------------------------

func (t *localTransport) SendMessage(msg protocol.InboundMessage) error {
	select {
	case t.bus.Inbound <- bus.InboundMessage{
		Content: msg.Content, Channel: msg.Channel, ChatID: msg.ChatID,
		SenderID: msg.SenderID, SenderName: msg.SenderName, ChatType: msg.ChatType,
	}:
		return nil
	default:
		return fmt.Errorf("inbound channel full, message dropped")
	}
}

func (t *localTransport) BindChat(string) error { return nil }

// ---------------------------------------------------------------------------
// TUI control (no-op in local mode — agent handles directly)
// ---------------------------------------------------------------------------

func (t *localTransport) SetTUIControlHandler(cb func(action string, params map[string]string) (map[string]string, error)) {
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

func (t *localTransport) ConnState() string { return "connected" }
func (t *localTransport) IsRemote() bool    { return false }
func (t *localTransport) ServerURL() string { return "" }

// ---------------------------------------------------------------------------
// RPC dispatch
// ---------------------------------------------------------------------------

func (t *localTransport) Call(method string, payload json.RawMessage) (json.RawMessage, error) {
	handler, ok := t.handlers[method]
	if !ok {
		return nil, fmt.Errorf("RPC method %q not available in local mode", method)
	}
	return handler(payload)
}

// ---------------------------------------------------------------------------
// Generic handler builders
// ---------------------------------------------------------------------------

// rpc0 handles methods that take no request payload and return (R, error).
func rpc0[R any](fn func() (R, error)) func(json.RawMessage) (json.RawMessage, error) {
	return func(_ json.RawMessage) (json.RawMessage, error) {
		result, err := fn()
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
}

// rpc1 handles methods that unmarshal a request, call fn, and return (R, error).
func rpc1[Req, R any](fn func(Req) (R, error)) func(json.RawMessage) (json.RawMessage, error) {
	return func(raw json.RawMessage) (json.RawMessage, error) {
		var req Req
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, fmt.Errorf("unmarshal: %w", err)
		}
		result, err := fn(req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
}

// rpcVoid handles methods that unmarshal a request and return only error.
func rpcVoid[Req any](fn func(Req) error) func(json.RawMessage) (json.RawMessage, error) {
	return func(raw json.RawMessage) (json.RawMessage, error) {
		var req Req
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, fmt.Errorf("unmarshal: %w", err)
		}
		return nil, fn(req)
	}
}

// rpcVoid0 handles void methods with no request payload.
func rpcVoid0(fn func() error) func(json.RawMessage) (json.RawMessage, error) {
	return func(_ json.RawMessage) (json.RawMessage, error) { return nil, fn() }
}

// ---------------------------------------------------------------------------
// Handler registration
// ---------------------------------------------------------------------------

func (t *localTransport) registerHandlers() {
	h := t.handlers
	a := t.agent

	// ── Settings ──────────────────────────────────────────────────────────

	h[MethodGetSettings] = rpc1(func(r getSettingsReq) (map[string]string, error) {
		if a.settingsSvc == nil {
			return nil, ErrSettingsUnavailable
		}
		return a.settingsSvc.GetSettings(r.Namespace, r.SenderID)
	})

	h[MethodSetSetting] = rpcVoid(func(r setSettingReq) error {
		if a.settingsSvc == nil {
			return ErrSettingsUnavailable
		}
		return a.settingsSvc.SetSetting(r.Namespace, r.SenderID, r.Key, r.Value)
	})

	// ── Model / LLM ───────────────────────────────────────────────────────

	h[MethodGetDefaultModel] = rpc0(func() (string, error) {
		return a.GetDefaultModel(), nil
	})

	h[MethodGetContextMode] = rpc0(func() (string, error) {
		return a.GetContextMode(), nil
	})

	h[MethodListModels] = rpc0(func() ([]string, error) {
		return a.llmFactory.ListModels(), nil
	})

	h[MethodListAllModels] = rpc0(func() ([]string, error) {
		return a.llmFactory.ListAllModelsForUser(""), nil
	})

	h[MethodSetModelTiers] = rpcVoid(func(r config.LLMConfig) error {
		a.llmFactory.SetModelTiers(r)
		return nil
	})

	h[MethodSetDefaultThinkingMode] = rpcVoid(func(r setDefaultThinkingModeReq) error {
		a.llmFactory.SetDefaultThinkingMode(r.Mode)
		return nil
	})

	h[MethodSetModelContexts] = rpcVoid(func(r map[string]int) error {
		a.llmFactory.SetModelContexts(r)
		return nil
	})

	h[MethodSetGlobalMaxTokens] = rpcVoid(func(r setGlobalMaxTokensReq) error {
		a.llmFactory.SetGlobalMaxTokens(r.MaxTokens)
		return nil
	})

	h[MethodSetRetryConfig] = rpcVoid(func(r llm.RetryConfig) error {
		a.llmFactory.SetRetryConfig(r)
		return nil
	})

	h[MethodSetChatLLM] = rpcVoid(func(r setChatLLMReq) error {
		var inner llm.LLM
		switch r.Provider {
		case "anthropic":
			inner = llm.NewAnthropicLLM(llm.AnthropicConfig{
				BaseURL:      r.Config.BaseURL,
				APIKey:       r.Config.APIKey,
				DefaultModel: r.Config.Model,
				MaxTokens:    r.Config.MaxOutputTokens,
			})
		default:
			inner = llm.NewOpenAILLM(llm.OpenAIConfig{
				BaseURL:      r.Config.BaseURL,
				APIKey:       r.Config.APIKey,
				DefaultModel: r.Config.Model,
				MaxTokens:    r.Config.MaxOutputTokens,
			})
		}
		client := llm.NewRetryLLM(inner, llm.DefaultRetryConfig())
		a.llmFactory.SetChatLLM(r.SenderID, r.ChatID, client, r.Config.Model)
		return nil
	})

	h[MethodClearProxyLLM] = rpcVoid(func(r clearProxyLLMReq) error {
		a.ClearProxyLLM(r.SenderID)
		return nil
	})

	// ── Per-user settings ─────────────────────────────────────────────────

	h[MethodGetUserMaxContext] = rpc1(func(r getUserMaxContextReq) (int, error) {
		return a.GetUserMaxContext(r.SenderID), nil
	})

	h[MethodGetUserMaxOutputTokens] = rpc1(func(r getUserMaxOutputTokensReq) (int, error) {
		return a.GetUserMaxOutputTokens(r.SenderID), nil
	})

	h[MethodGetUserThinkingMode] = rpc1(func(r getUserThinkingModeReq) (string, error) {
		return a.GetUserThinkingMode(r.SenderID), nil
	})

	h[MethodGetLLMConcurrency] = rpc1(func(r getLLMConcurrencyReq) (int, error) {
		return a.GetLLMConcurrency(r.SenderID), nil
	})

	h[MethodSetUserModel] = rpcVoid(func(r setUserModelReq) error {
		return a.SetUserModel(r.SenderID, r.Model)
	})

	h[MethodSwitchModel] = rpcVoid(func(r switchModelReq) error {
		if r.ChatID != "" {
			a.llmFactory.SwitchModel(r.SenderID, r.Model, r.ChatID)
		} else {
			a.llmFactory.SwitchModel(r.SenderID, r.Model)
		}
		return nil
	})

	h[MethodSetUserMaxContext] = rpcVoid(func(r setUserMaxContextReq) error {
		return a.SetUserMaxContext(r.SenderID, r.MaxContext)
	})

	h[MethodSetUserMaxOutputTokens] = rpcVoid(func(r setUserMaxOutputTokensReq) error {
		if r.MaxTokens < 0 {
			return fmt.Errorf("max_output_tokens must be >= 0, got %d", r.MaxTokens)
		}
		if err := a.SetUserMaxOutputTokens(r.SenderID, r.MaxTokens); err != nil {
			// Only fallback to factory-level setting when user has no DB config.
			if strings.Contains(err.Error(), "未配置自定义 LLM") {
				a.llmFactory.SetUserMaxOutputTokens(r.SenderID, r.MaxTokens)
				return nil
			}
			return err
		}
		return nil
	})

	h[MethodSetUserThinkingMode] = rpcVoid(func(r setUserThinkingModeReq) error {
		validModes := map[string]bool{"": true, "enabled": true, "disabled": true, "auto": true}
		if !validModes[r.Mode] {
			return fmt.Errorf("invalid thinking_mode: %q", r.Mode)
		}
		if err := a.SetUserThinkingMode(r.SenderID, r.Mode); err != nil {
			// Only fallback to factory-level setting when user has no DB config.
			if strings.Contains(err.Error(), "未配置自定义 LLM") {
				a.llmFactory.SetUserThinkingMode(r.SenderID, r.Mode)
				return nil
			}
			return err
		}
		return nil
	})

	h[MethodSetLLMConcurrency] = rpcVoid(func(r setLLMConcurrencyReq) error {
		return a.SetLLMConcurrency(r.SenderID, r.Personal)
	})

	// ── Runtime config ────────────────────────────────────────────────────

	h[MethodSetMaxIterations] = rpcVoid(func(r int) error {
		a.SetMaxIterations(r)
		return nil
	})

	h[MethodSetMaxConcurrency] = rpcVoid(func(r int) error {
		a.SetMaxConcurrency(r)
		return nil
	})

	h[MethodSetMaxContextTokens] = rpcVoid(func(r struct {
		MaxContext int    `json:"max_context"`
		ChatID     string `json:"chat_id,omitempty"`
	}) error {
		if r.ChatID != "" {
			a.SetMaxContextTokens(r.MaxContext, r.ChatID)
		} else {
			a.SetMaxContextTokens(r.MaxContext)
		}
		return nil
	})

	h[MethodSetCompressionThreshold] = rpcVoid(func(r float64) error {
		a.SetCompressionThreshold(r)
		return nil
	})

	h[MethodSetContextMode] = rpcVoid(func(r setContextModeReq) error {
		return a.SetContextMode(r.Mode)
	})

	h[MethodSetCWD] = rpcVoid(func(r setCWDReq) error {
		if a.sandboxMode != "none" {
			return fmt.Errorf("CWD sync not supported in %s sandbox mode", a.sandboxMode)
		}
		if a.MultiSession() == nil {
			return ErrNoSessionManager
		}
		sess, err := a.MultiSession().GetOrCreateSession(r.Channel, r.ChatID)
		if err != nil {
			return err
		}

		// If session already has a persisted CWD (restored from disk), keep it.
		// Otherwise use the requested directory.
		if sess.GetCurrentDir() == "" {
			sess.SetCurrentDir(r.Dir)
		}

		// Always refresh plugin contexts so script plugins see the correct workDir
		if a.pluginMgr != nil {
			cwd := sess.GetCurrentDir()
			a.pluginMgr.RefreshWorkDir(cwd, r.Channel, r.ChatID, sess.TenantID())
			a.pluginMgr.RefreshTenantID(sess.TenantID())
		}
		return nil
	})

	h[MethodResetTokenState] = rpcVoid0(func() error { return nil })

	// ── Token usage ───────────────────────────────────────────────────────

	h[MethodGetUserTokenUsage] = rpc1(func(r getUserTokenUsageReq) (map[string]any, error) {
		if a.multiSession == nil {
			return nil, nil
		}
		usage, err := a.multiSession.GetUserTokenUsage(r.SenderID)
		if err != nil || usage == nil {
			return nil, err
		}
		return map[string]any{
			"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens,
			"total_tokens": usage.TotalTokens, "cached_tokens": usage.CachedTokens,
			"conversation_count": usage.ConversationCount, "llm_call_count": usage.LLMCallCount,
		}, nil
	})

	h[MethodGetDailyTokenUsage] = rpc1(func(r getDailyTokenUsageReq) ([]map[string]any, error) {
		if a.multiSession == nil {
			return nil, nil
		}
		daily, err := a.multiSession.GetDailyTokenUsage(r.SenderID, r.Days)
		if err != nil || daily == nil {
			return nil, err
		}
		result := make([]map[string]any, len(daily))
		for i, d := range daily {
			result[i] = map[string]any{
				"date": d.Date, "model": d.Model,
				"input_tokens": d.InputTokens, "output_tokens": d.OutputTokens,
				"cached_tokens":      d.CachedTokens,
				"conversation_count": d.ConversationCount, "llm_call_count": d.LLMCallCount,
			}
		}
		return result, nil
	})

	h[MethodGetTokenState] = rpc1(func(r getTokenStateReq) (struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	}, error) {
		ms := a.MultiSession()
		if ms == nil {
			return struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			}{}, nil
		}
		sess, err := ms.GetOrCreateSession(r.Channel, r.ChatID)
		if err != nil {
			return struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			}{}, err
		}
		memSvc := sess.MemoryService()
		if memSvc == nil {
			return struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			}{}, nil
		}
		pt, ct, err := memSvc.GetTokenState(context.Background(), sess.TenantID())
		return struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		}{PromptTokens: pt, CompletionTokens: ct}, err
	})

	// ── Background tasks ──────────────────────────────────────────────────

	h[MethodGetBgTaskCount] = rpc1(func(r getBgTaskCountReq) (int, error) {
		if a.bgTaskMgr == nil {
			return 0, nil
		}
		return len(a.bgTaskMgr.ListRunning(r.SessionKey)), nil
	})

	h[MethodListBgTasks] = rpc1(func(r listBgTasksReq) ([]BgTaskJSON, error) {
		if a.bgTaskMgr == nil {
			return nil, nil
		}
		tasks := a.bgTaskMgr.ListAllForSession(r.SessionKey)
		result := make([]BgTaskJSON, len(tasks))
		for i, t := range tasks {
			result[i] = BgTaskJSON{
				ID: t.ID, Command: t.Command, Status: string(t.Status),
				StartedAt: t.StartedAt.Format(time.RFC3339), ExitCode: t.ExitCode,
				Output: t.Output, Error: t.Error,
			}
			if t.FinishedAt != nil {
				result[i].FinishedAt = t.FinishedAt.Format(time.RFC3339)
			}
		}
		return result, nil
	})

	h[MethodKillBgTask] = rpcVoid(func(r killBgTaskReq) error {
		if a.bgTaskMgr == nil {
			return ErrBgTasksUnavailable
		}
		return a.bgTaskMgr.Kill(r.TaskID)
	})

	h[MethodCleanupCompletedBgTasks] = rpcVoid(func(r cleanupCompletedBgTasksReq) error {
		if a.bgTaskMgr != nil {
			a.bgTaskMgr.RemoveCompletedTasks(r.SessionKey)
		}
		return nil
	})

	// ── Tenants ───────────────────────────────────────────────────────────

	h[MethodListTenants] = rpc0(func() ([]TenantInfo, error) {
		if a.multiSession == nil {
			return nil, nil
		}
		db := a.multiSession.DB()
		if db == nil {
			return nil, nil
		}
		tenantSvc := sqlite.NewTenantService(db)
		tenants, err := tenantSvc.ListTenants()
		if err != nil {
			return nil, err
		}
		result := make([]TenantInfo, len(tenants))
		for i, t := range tenants {
			result[i] = TenantInfo{
				ID: t.ID, Channel: t.Channel, ChatID: t.ChatID, Label: t.Label,
				CreatedAt: t.CreatedAt.Format(time.RFC3339), LastActiveAt: t.LastActiveAt.Format(time.RFC3339),
			}
		}
		return result, nil
	})

	// ── Subscriptions ─────────────────────────────────────────────────────

	h[MethodListSubscriptions] = rpc1(func(r listSubscriptionsReq) ([]protocol.Subscription, error) {
		svc := a.llmFactory.GetSubscriptionSvc()
		if svc == nil {
			return nil, nil
		}
		subs, err := svc.List(r.SenderID)
		if err != nil || subs == nil {
			return nil, err
		}
		result := make([]protocol.Subscription, len(subs))
		for i, s := range subs {
			result[i] = sqliteSubToProtocol(s)
		}
		return result, nil
	})

	h[MethodGetDefaultSubscription] = rpc1(func(r getDefaultSubscriptionReq) (*protocol.Subscription, error) {
		svc := a.llmFactory.GetSubscriptionSvc()
		if svc == nil {
			return nil, nil
		}
		sub, err := svc.GetDefault(r.SenderID)
		if err != nil || sub == nil {
			return nil, err
		}
		p := sqliteSubToProtocol(sub)
		return &p, nil
	})

	h[MethodAddSubscription] = rpcVoid(func(r addSubscriptionReq) error {
		svc := a.llmFactory.GetSubscriptionSvc()
		if svc == nil {
			return ErrSubscriptionsUnavailable
		}
		if err := svc.Add(&sqlite.LLMSubscription{
			ID: r.Sub.ID, SenderID: r.SenderID, Name: r.Sub.Name,
			Provider: r.Sub.Provider, BaseURL: r.Sub.BaseURL, APIKey: r.Sub.APIKey,
			Model: r.Sub.Model, IsDefault: r.Sub.Active,
		}); err != nil {
			return err
		}
		a.llmFactory.Invalidate(r.SenderID)
		return nil
	})

	h[MethodRemoveSubscription] = rpcVoid(func(r removeSubscriptionReq) error {
		svc := a.llmFactory.GetSubscriptionSvc()
		if svc == nil {
			return ErrSubscriptionsUnavailable
		}
		sub, err := svc.Get(r.ID)
		if err != nil {
			return err
		}
		if err := svc.Remove(r.ID); err != nil {
			return err
		}
		if sub != nil {
			a.llmFactory.Invalidate(sub.SenderID)
		}
		return nil
	})

	h[MethodSetDefaultSubscription] = rpcVoid(func(r setDefaultSubscriptionReq) error {
		svc := a.llmFactory.GetSubscriptionSvc()
		if svc == nil {
			return ErrSubscriptionsUnavailable
		}
		sub, err := svc.Get(r.ID)
		if err != nil || sub == nil {
			return fmt.Errorf("subscription %s not found", r.ID)
		}
		if r.ChatID != "" {
			// Per-session switch: only update per-chat cache, do NOT modify
			// the global default subscription or invalidate other sessions.
			return a.llmFactory.SetSessionLLM(sub.SenderID, r.ChatID, sub)
		}
		// Global switch: update DB default + invalidate all caches + set per-user LLM
		if err := svc.SetDefault(r.ID); err != nil {
			return err
		}
		a.llmFactory.Invalidate(sub.SenderID)
		return a.llmFactory.SwitchSubscription(sub.SenderID, sub, "")
	})

	h[MethodUpdateSubscription] = rpcVoid(func(r updateSubscriptionReq) error {
		svc := a.llmFactory.GetSubscriptionSvc()
		if svc == nil {
			return ErrSubscriptionsUnavailable
		}
		existing, err := svc.Get(r.ID)
		if err != nil {
			return err
		}
		if existing == nil {
			return fmt.Errorf("subscription %s not found", r.ID)
		}
		dbSub := &sqlite.LLMSubscription{
			ID: r.ID, SenderID: existing.SenderID,
			Name: r.Sub.Name, Provider: r.Sub.Provider, BaseURL: r.Sub.BaseURL,
			APIKey: r.Sub.APIKey, Model: r.Sub.Model,
			MaxContext: existing.MaxContext, MaxOutputTokens: r.Sub.MaxOutputTokens,
			ThinkingMode: r.Sub.ThinkingMode, IsDefault: r.Sub.Active,
		}
		// PerModelConfigs is now type-aliased — direct assignment.
		dbSub.PerModelConfigs = r.Sub.PerModelConfigs
		// Never overwrite with a masked key from server RPC transport.
		if strings.HasSuffix(dbSub.APIKey, "****") && len(dbSub.APIKey) <= 20 {
			dbSub.APIKey = existing.APIKey
		}
		if err := svc.Update(dbSub); err != nil {
			return err
		}
		a.llmFactory.Invalidate(existing.SenderID)
		return nil
	})

	h[MethodUpdatePerModelConfig] = rpcVoid(func(r updatePerModelConfigReq) error {
		svc := a.llmFactory.GetSubscriptionSvc()
		if svc == nil {
			return ErrSubscriptionsUnavailable
		}
		existing, err := svc.Get(r.ID)
		if err != nil {
			return fmt.Errorf("subscription %s not found: %w", r.ID, err)
		}
		if existing.PerModelConfigs == nil {
			existing.PerModelConfigs = make(map[string]sqlite.PerModelConfig)
		}
		existing.PerModelConfigs[r.Model] = r.Config
		return svc.Update(existing)
	})

	h[MethodSetSubscriptionModel] = rpcVoid(func(r setSubscriptionModelReq) error {
		svc := a.llmFactory.GetSubscriptionSvc()
		if svc == nil {
			return ErrSubscriptionsUnavailable
		}
		sub, err := svc.Get(r.ID)
		if err != nil {
			return err
		}
		if err := svc.SetModel(r.ID, r.Model); err != nil {
			return err
		}
		if sub != nil {
			a.llmFactory.Invalidate(sub.SenderID)
		}
		return nil
	})

	h[MethodRenameSubscription] = rpcVoid(func(r renameSubscriptionReq) error {
		svc := a.llmFactory.GetSubscriptionSvc()
		if svc == nil {
			return ErrSubscriptionsUnavailable
		}
		return svc.Rename(r.ID, r.Name)
	})

	// ── Memory / session / history ────────────────────────────────────────

	h[MethodClearMemory] = rpcVoid(func(r clearMemoryReq) error {
		if a.multiSession == nil {
			return nil
		}
		return a.multiSession.ClearMemory(context.Background(), r.Channel, r.ChatID, r.TargetType, r.SenderID)
	})

	h[MethodGetMemoryStats] = rpc1(func(r getMemoryStatsReq) (map[string]string, error) {
		if a.multiSession == nil {
			return nil, nil
		}
		return a.multiSession.GetMemoryStats(context.Background(), r.Channel, r.ChatID, r.SenderID), nil
	})

	h[MethodGetHistory] = rpc1(func(r getHistoryReq) ([]protocol.HistoryMessage, error) {
		ms := a.MultiSession()
		if ms == nil {
			return nil, fmt.Errorf("multi-session not available")
		}
		sess, err := ms.GetOrCreateSession(r.Channel, r.ChatID)
		if err != nil {
			return nil, err
		}
		msgs, err := sess.GetMessages()
		if err != nil {
			return nil, err
		}
		return channel.ConvertMessagesToHistory(msgs), nil
	})

	h[MethodTrimHistory] = rpcVoid(func(r trimHistoryReq) error {
		ms := a.MultiSession()
		if ms == nil {
			return fmt.Errorf("multi-session not available")
		}
		return ms.TrimHistory(r.Channel, r.ChatID, time.Unix(r.Cutoff, 0))
	})

	// ── Interactive sessions ───────────────────────────────────────────────

	h[MethodCountInteractiveSessions] = rpc1(func(r countInteractiveSessionsReq) (int, error) {
		return a.CountInteractiveSessions(r.ChannelName, r.ChatID), nil
	})

	h[MethodListInteractiveSessions] = rpc1(func(r listInteractiveSessionsReq) ([]InteractiveSessionInfo, error) {
		return a.ListInteractiveSessions(r.ChannelName, r.ChatID), nil
	})

	h[MethodInspectInteractiveSession] = rpc1(func(r inspectInteractiveSessionReq) (string, error) {
		return a.InspectInteractiveSession(context.Background(), r.RoleName, r.ChannelName, r.ChatID, r.Instance, r.TailCount)
	})

	h[MethodGetSessionMessages] = rpc1(func(r getSessionMessagesReq) (struct {
		Messages []SessionMessage `json:"messages"`
		OK       bool             `json:"ok"`
	}, error) {
		msgs, ok := a.GetSessionMessages(r.ChannelName, r.ChatID, r.RoleName, r.Instance)
		return struct {
			Messages []SessionMessage `json:"messages"`
			OK       bool             `json:"ok"`
		}{Messages: msgs, OK: ok}, nil
	})

	h[MethodGetAgentSessionDump] = rpc1(func(r getAgentSessionDumpReq) (struct {
		Dump *AgentSessionDump `json:"dump"`
		OK   bool              `json:"ok"`
	}, error) {
		dump, ok := a.GetAgentSessionDump(r.ChannelName, r.ChatID, r.RoleName, r.Instance)
		return struct {
			Dump *AgentSessionDump `json:"dump"`
			OK   bool              `json:"ok"`
		}{Dump: dump, OK: ok}, nil
	})

	h[MethodGetAgentSessionDumpByFullKey] = rpc1(func(r getAgentSessionDumpByFullKeyReq) (struct {
		Dump *AgentSessionDump `json:"dump"`
		OK   bool              `json:"ok"`
	}, error) {
		dump, ok := a.GetAgentSessionDumpByFullKey(r.FullKey)
		return struct {
			Dump *AgentSessionDump `json:"dump"`
			OK   bool              `json:"ok"`
		}{Dump: dump, OK: ok}, nil
	})

	// ── Processing state ──────────────────────────────────────────────────

	h[MethodIsProcessing] = rpc1(func(r isProcessingReq) (bool, error) {
		// Exact key match — cancelKey is stored as channel:chatID
		// (no trailing colon, no senderID). Prefix matching was broken
		// because a parent dir prefix (e.g. "cli:/home/xbot:") would
		// also match child dir keys (e.g. "cli:/home/xbot/worktree").
		key := r.Channel + ":" + r.ChatID
		_, found := a.chatCancelCh.Load(key)
		return found, nil
	})

	h[MethodGetActiveProgress] = rpc1(func(r getActiveProgressReq) (*protocol.ProgressEvent, error) {
		key := r.Channel + ":" + r.ChatID
		v, ok := a.lastProgressSnapshot.Load(key)
		if !ok {
			return nil, nil
		}
		snapshot := v.(*protocol.ProgressEvent)
		// Shallow copy to avoid data race: agent may update snapshot fields
		// concurrently during json.Marshal.
		result := *snapshot
		if histPtr, ok := a.iterationHistories.Load(key); ok {
			hist := *histPtr.(*[]protocol.ProgressEvent)
			if len(hist) > 0 {
				result.IterationHistory = make([]protocol.ProgressEvent, len(hist))
				copy(result.IterationHistory, hist)
				return &result, nil
			}
		}
		return &result, nil
	})

	h[MethodGetTodos] = rpc1(func(r getTodosReq) ([]protocol.TodoItem, error) {
		key := r.Channel + ":" + r.ChatID
		if a.todoManager == nil {
			return []protocol.TodoItem{}, nil
		}
		items := a.todoManager.GetTodos(key)
		if len(items) == 0 {
			return []protocol.TodoItem{}, nil
		}
		result := make([]protocol.TodoItem, len(items))
		for i, t := range items {
			result[i] = protocol.TodoItem{ID: t.ID, Text: t.Text, Done: t.Done}
		}
		return result, nil
	})

	// ── LLM Factory helpers (via RPC) ───────────────────────────────────

	h[MethodGetEffectiveMaxContext] = rpc1(func(r getEffectiveMaxContextReq) (int, error) {
		return a.llmFactory.GetEffectiveMaxContext(r.SenderID, r.ChatID), nil
	})

	h[MethodClearPerChatMaxContext] = rpcVoid(func(r clearPerChatMaxContextReq) error {
		a.llmFactory.ClearPerChatMaxContext(r.ChatID)
		return nil
	})

	// ── Session configuration ─────────────────────────────────────────────

	h[MethodSetMaxIterations] = rpcVoid(func(r setMaxIterationsReq) error {
		a.SetMaxIterations(r.N)
		return nil
	})

	h[MethodSetMaxConcurrency] = rpcVoid(func(r setMaxConcurrencyReq) error {
		a.SetMaxConcurrency(r.N)
		return nil
	})

	h[MethodSetMaxContextTokens] = rpcVoid(func(r setMaxContextTokensReq) error {
		if r.ChatID != "" {
			a.SetMaxContextTokens(r.MaxContext, r.ChatID)
		} else {
			a.SetMaxContextTokens(r.MaxContext)
		}
		return nil
	})

	h[MethodSetCompressionThreshold] = rpcVoid(func(r setCompressionThresholdReq) error {
		a.SetCompressionThreshold(r.Threshold)
		return nil
	})

	h[MethodGetContextMode] = rpc0(func() (string, error) {
		return a.GetContextMode(), nil
	})

	h[MethodSetContextMode] = rpcVoid(func(r setContextModeReq) error {
		return a.SetContextMode(r.Mode)
	})

	// ── Channel config ────────────────────────────────────────────────────

	h[MethodGetChannelConfig] = rpc0(func() (map[string]map[string]string, error) {
		cfg := config.LoadFromFile(config.ConfigFilePath())
		if cfg == nil {
			return nil, fmt.Errorf("config not found")
		}
		result := make(map[string]map[string]string)
		result["web"] = map[string]string{
			"enabled": strconv.FormatBool(cfg.Web.Enable),
			"host":    cfg.Web.Host,
			"port":    strconv.Itoa(cfg.Web.Port),
		}
		result["feishu"] = map[string]string{
			"enabled":            strconv.FormatBool(cfg.Feishu.Enabled),
			"app_id":             cfg.Feishu.AppID,
			"app_secret":         cfg.Feishu.AppSecret,
			"encrypt_key":        cfg.Feishu.EncryptKey,
			"verification_token": cfg.Feishu.VerificationToken,
			"domain":             cfg.Feishu.Domain,
		}
		result["qq"] = map[string]string{
			"enabled":       strconv.FormatBool(cfg.QQ.Enabled),
			"app_id":        cfg.QQ.AppID,
			"client_secret": cfg.QQ.ClientSecret,
		}
		result["napcat"] = map[string]string{
			"enabled": strconv.FormatBool(cfg.NapCat.Enabled),
			"ws_url":  cfg.NapCat.WSUrl,
			"token":   cfg.NapCat.Token,
		}
		return result, nil
	})

	h[MethodSetChannelConfig] = rpcVoid(func(r setChannelConfigReq) error {
		cfg := config.LoadFromFile(config.ConfigFilePath())
		if cfg == nil {
			cfg = &config.Config{}
		}
		switch r.Channel {
		case "web":
			if v, ok := r.Values["enabled"]; ok {
				cfg.Web.Enable, _ = strconv.ParseBool(v)
			} else if v, ok := r.Values["enable"]; ok {
				cfg.Web.Enable, _ = strconv.ParseBool(v)
			}
			if v, ok := r.Values["host"]; ok {
				cfg.Web.Host = v
			}
			if v, ok := r.Values["port"]; ok {
				cfg.Web.Port, _ = strconv.Atoi(v)
			}
		case "feishu":
			if v, ok := r.Values["enabled"]; ok {
				cfg.Feishu.Enabled, _ = strconv.ParseBool(v)
			}
			if v, ok := r.Values["app_id"]; ok {
				cfg.Feishu.AppID = v
			}
			if v, ok := r.Values["app_secret"]; ok {
				cfg.Feishu.AppSecret = v
			}
			if v, ok := r.Values["encrypt_key"]; ok {
				cfg.Feishu.EncryptKey = v
			}
			if v, ok := r.Values["verification_token"]; ok {
				cfg.Feishu.VerificationToken = v
			}
			if v, ok := r.Values["domain"]; ok {
				cfg.Feishu.Domain = v
			}
		case "qq":
			if v, ok := r.Values["enabled"]; ok {
				cfg.QQ.Enabled, _ = strconv.ParseBool(v)
			}
			if v, ok := r.Values["app_id"]; ok {
				cfg.QQ.AppID = v
			}
			if v, ok := r.Values["client_secret"]; ok {
				cfg.QQ.ClientSecret = v
			}
		case "napcat":
			if v, ok := r.Values["enabled"]; ok {
				cfg.NapCat.Enabled, _ = strconv.ParseBool(v)
			}
			if v, ok := r.Values["ws_url"]; ok {
				cfg.NapCat.WSUrl = v
			}
			if v, ok := r.Values["token"]; ok {
				cfg.NapCat.Token = v
			}
		default:
			return fmt.Errorf("unknown channel: %s", r.Channel)
		}
		err := config.SaveToFile(config.ConfigFilePath(), cfg)
		if err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		if t.reconfigureFn != nil {
			t.reconfigureFn(r.Channel)
		}
		return nil
	})

	// ── Web Users ──────────────────────────────────────────────────────────

	h["create_web_user"] = rpc1(func(r struct {
		Username string `json:"username"`
	}) (map[string]string, error) {
		db := a.MultiSession().DB().Conn()
		_, password, err := channel.CreateWebUser(db, r.Username)
		if err != nil {
			return nil, err
		}
		return map[string]string{"password": password}, nil
	})

	h["list_web_users"] = rpc0(func() (any, error) {
		db := a.MultiSession().DB().Conn()
		return channel.ListWebUsers(db)
	})

	h["delete_web_user"] = rpcVoid(func(r struct {
		Username string `json:"username"`
	}) error {
		db := a.MultiSession().DB().Conn()
		return channel.DeleteWebUser(db, r.Username)
	})

	// ── Chat Management ──────────────────────────────────────────────────

	h["delete_chat"] = rpcVoid(func(r struct {
		Channel  string `json:"channel"`
		SenderID string `json:"senderid"`
		ChatID   string `json:"chat_id"`
	}) error {
		cs := sqlite.NewChatService(a.MultiSession().DB().Conn())
		return cs.DeleteChat(r.Channel, r.SenderID, r.ChatID)
	})

	h["rename_chat"] = rpcVoid(func(r struct {
		Channel  string `json:"channel"`
		SenderID string `json:"senderid"`
		ChatID   string `json:"chat_id"`
		NewName  string `json:"new_name"`
	}) error {
		cs := sqlite.NewChatService(a.MultiSession().DB().Conn())
		return cs.RenameChat(r.Channel, r.SenderID, r.ChatID, r.NewName)
	})

	h["get_history"] = rpc1(func(r struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		if r.Channel == "" {
			r.Channel = "cli"
		}
		ms := a.MultiSession()
		if ms == nil {
			return nil, fmt.Errorf("session service not available")
		}
		db := ms.DB()
		if db == nil {
			return nil, fmt.Errorf("database not available")
		}
		tenantSvc := sqlite.NewTenantService(db)
		sessionSvc := sqlite.NewSessionService(db)
		tid, err := tenantSvc.GetOrCreateTenantID(r.Channel, r.ChatID)
		if err != nil {
			return nil, fmt.Errorf("get tenant: %w", err)
		}
		msgs, err := sessionSvc.GetAllMessages(tid)
		if err != nil {
			return nil, err
		}
		return channel.ConvertMessagesToHistory(msgs), nil
	})

	h["get_token_state"] = rpc1(func(r struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		if r.Channel == "" {
			r.Channel = "cli"
		}
		ms := a.MultiSession()
		if ms == nil {
			return nil, fmt.Errorf("session service not available")
		}
		db := ms.DB()
		if db == nil {
			return nil, fmt.Errorf("database not available")
		}
		tenantSvc := sqlite.NewTenantService(db)
		tid, err := tenantSvc.GetOrCreateTenantID(r.Channel, r.ChatID)
		if err != nil {
			return nil, fmt.Errorf("get tenant: %w", err)
		}
		pt, ct, err := sqlite.NewMemoryService(db).GetTokenState(context.Background(), tid)
		if err != nil {
			return nil, err
		}
		return map[string]int64{"prompt_tokens": pt, "completion_tokens": ct}, nil
	})
}

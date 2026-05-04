package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"xbot/agent/hooks"
	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	"xbot/event"
	llm "xbot/llm"
	"xbot/plugin"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/tools"

	log "xbot/logger"
)

// Sentinel errors for service availability checks.
var (
	ErrSettingsUnavailable      = errors.New("settings service not available")
	ErrBgTasksUnavailable       = errors.New("background tasks not available")
	ErrSubscriptionsUnavailable = errors.New("subscription service not available")
	ErrNoSessionManager         = errors.New("no session manager")
)

// BgTaskJSON is a JSON-serializable background task summary.
type BgTaskJSON struct {
	ID         string `json:"id"`
	Command    string `json:"command"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Output     string `json:"output"`
	ExitCode   int    `json:"exit_code"`
	Error      string `json:"error,omitempty"`
}

// TenantInfo is a JSON-serializable tenant summary.
type TenantInfo struct {
	ID           int64  `json:"id"`
	Channel      string `json:"channel"`
	ChatID       string `json:"chat_id"`
	CreatedAt    string `json:"created_at"`
	LastActiveAt string `json:"last_active_at"`
}

// Backend is the single unified implementation of AgentBackend.
// Local mode: agent is non-nil, transport is nil.
// Remote mode: agent is nil, transport is non-nil.
type Backend struct {
	agent         *Agent
	bus           *bus.MessageBus
	transport     Transport
	reconfigureFn func(channel string)
}

// NewBackend creates a local-mode Backend.
func NewBackend(cfg Config) (*Backend, error) {
	a, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &Backend{agent: a, bus: cfg.Bus}, nil
}

// NewTransportBackend creates a remote-mode Backend connected via Transport.
func NewTransportBackend(t Transport) *Backend {
	return &Backend{transport: t}
}

// NewRemoteBackend creates a remote-mode Backend from RemoteTransportConfig.
// This is the convenience constructor for CLI remote mode.
func NewRemoteBackend(cfg RemoteTransportConfig) *Backend {
	return &Backend{transport: NewRemoteTransport(cfg)}
}

// Agent returns the underlying *Agent (nil in remote mode).
func (b *Backend) Agent() *Agent {
	return b.agent
}

// ---------------------------------------------------------------------------
// Generic dispatch helpers
// ---------------------------------------------------------------------------

// dispatch sends a typed request to either the local agent or remote transport.
func dispatch[Req, Res any](b *Backend, method string, req Req, localFn func(*Agent, Req) (Res, error)) (Res, error) {
	if b.agent != nil {
		return localFn(b.agent, req)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		var zero Res
		return zero, fmt.Errorf("%s: marshal: %w", method, err)
	}
	raw, err := b.transport.Call(method, payload)
	if err != nil {
		var zero Res
		return zero, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		var zero Res
		return zero, nil
	}
	var result Res
	if err := json.Unmarshal(raw, &result); err != nil {
		var zero Res
		return zero, fmt.Errorf("%s: unmarshal: %w", method, err)
	}
	return result, nil
}

// dispatchVoid sends a fire-and-forget request. Errors are logged, not returned.
func dispatchVoid[Req any](b *Backend, method string, req Req, localFn func(*Agent, Req) error) {
	if b.agent != nil {
		_ = localFn(b.agent, req)
		return
	}
	payload, err := json.Marshal(req)
	if err != nil {
		log.WithError(err).WithField("method", method).Warn("Backend: marshal failed")
		return
	}
	if _, err := b.transport.Call(method, payload); err != nil {
		log.WithError(err).WithField("method", method).Warn("Backend: remote call failed")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func (b *Backend) Start(ctx context.Context) error {
	if b.agent != nil {
		go b.agent.Run(ctx)
		return nil
	}
	return b.transport.Start(ctx)
}

func (b *Backend) Stop() {
	if b.agent != nil {
		if err := b.agent.Close(); err != nil {
			_ = err // best effort
		}
		return
	}
	b.transport.Stop()
}

func (b *Backend) Close() error {
	if b.agent != nil {
		return b.agent.Close()
	}
	return b.transport.Close()
}

func (b *Backend) Run(ctx context.Context) error {
	if b.agent != nil {
		return b.agent.Run(ctx)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (b *Backend) SendInbound(msg bus.InboundMessage) error {
	if b.agent != nil {
		select {
		case b.bus.Inbound <- msg:
			return nil
		default:
			return fmt.Errorf("inbound channel full, message dropped")
		}
	}
	return b.transport.SendMessage(Message{
		Content:    msg.Content,
		Channel:    msg.Channel,
		ChatID:     msg.ChatID,
		SenderID:   msg.SenderID,
		SenderName: msg.SenderName,
		ChatType:   msg.ChatType,
		Cancel:     strings.TrimSpace(strings.ToLower(msg.Content)) == "/cancel",
	})
}

// ---------------------------------------------------------------------------
// Callback setters (no-ops for local, delegate to transport for remote)
// ---------------------------------------------------------------------------

func (b *Backend) OnOutbound(cb func(bus.OutboundMessage)) {
	if b.transport != nil {
		b.transport.OnOutbound(cb)
	}
}

func (b *Backend) OnProgress(cb func(*channel.CLIProgressPayload)) {
	if b.transport != nil {
		b.transport.OnProgress(cb)
	}
}

func (b *Backend) OnInjectUserMessage(cb func(content string)) {
	if b.transport != nil {
		b.transport.OnInjectUserMessage(cb)
	}
}

func (b *Backend) OnReconnect(cb func()) {
	if b.transport != nil {
		b.transport.OnReconnect(cb)
	}
}

func (b *Backend) OnConnStateChange(cb func(state string)) {
	if b.transport != nil {
		b.transport.OnConnStateChange(cb)
	}
}

func (b *Backend) OnPluginWidgets(cb func(zones map[string]string, chatID string)) {
	if b.transport != nil {
		b.transport.OnPluginWidgets(cb)
	}
}

func (b *Backend) Subscribe(chatID string) error {
	if b.transport != nil {
		return b.transport.Subscribe(chatID)
	}
	return nil
}

func (b *Backend) ConnState() string {
	if b.transport != nil {
		return b.transport.ConnState()
	}
	return "connected"
}

func (b *Backend) ServerURL() string {
	if b.transport != nil {
		return b.transport.ServerURL()
	}
	return ""
}

// SetChannelReconfigureFn stores the function called after SetChannelConfig.
// In remote mode this is a no-op (channel restart is server-side).
func (b *Backend) SetChannelReconfigureFn(fn func(channel string)) {
	if b.agent != nil {
		b.reconfigureFn = fn
	}
}

// ---------------------------------------------------------------------------
// Return Go objects (nil in remote mode)
// ---------------------------------------------------------------------------

func (b *Backend) LLMFactory() *LLMFactory {
	if b.agent == nil {
		return nil
	}
	return b.agent.LLMFactory()
}

func (b *Backend) SettingsService() *SettingsService {
	if b.agent == nil {
		return nil
	}
	return b.agent.SettingsService()
}

func (b *Backend) MultiSession() *session.MultiTenantSession {
	if b.agent == nil {
		return nil
	}
	return b.agent.MultiSession()
}

func (b *Backend) BgTaskManager() *tools.BackgroundTaskManager {
	if b.agent == nil {
		return nil
	}
	return b.agent.BgTaskManager()
}

func (b *Backend) HookManager() *hooks.Manager {
	if b.agent == nil {
		return nil
	}
	return b.agent.HookManager()
}

func (b *Backend) ApprovalState() *hooks.ApprovalState {
	if b.agent == nil {
		return nil
	}
	return b.agent.ApprovalState()
}

func (b *Backend) PluginManager() *plugin.PluginManager {
	if b.agent == nil {
		return nil
	}
	return b.agent.PluginManager()
}

func (b *Backend) Bus() *bus.MessageBus {
	if b.agent == nil {
		return nil
	}
	return b.bus
}

func (b *Backend) GetCardBuilder() *tools.CardBuilder {
	if b.agent == nil {
		return nil
	}
	return b.agent.GetCardBuilder()
}

func (b *Backend) RegistryManager() *RegistryManager {
	if b.agent == nil {
		return nil
	}
	return b.agent.RegistryManager()
}

// ---------------------------------------------------------------------------
// No-ops (handled by Dispatcher locally; transport handles remotely)
// ---------------------------------------------------------------------------

func (b *Backend) SetDirectSend(fn func(bus.OutboundMessage) (string, error)) {
	if b.agent != nil {
		b.agent.SetDirectSend(fn)
	}
}

func (b *Backend) SetChannelFinder(fn func(name string) (channel.Channel, bool)) {
	if b.agent != nil {
		b.agent.SetChannelFinder(fn)
	}
}

func (b *Backend) SetChannelPromptProviders(providers ...ChannelPromptProvider) {
	if b.agent != nil {
		b.agent.SetChannelPromptProviders(providers...)
	}
}

func (b *Backend) RegisterCoreTool(tool tools.Tool) {
	if b.agent != nil {
		b.agent.RegisterCoreTool(tool)
	}
}

func (b *Backend) IndexGlobalTools() {
	if b.agent != nil {
		b.agent.IndexGlobalTools()
	}
}

func (b *Backend) RegisterTool(tool tools.Tool) {
	if b.agent != nil {
		b.agent.RegisterTool(tool)
	}
}

func (b *Backend) SetEventRouter(router *event.Router) {
	if b.agent != nil {
		b.agent.SetEventRouter(router)
	}
}

// ---------------------------------------------------------------------------
// Local-only identity methods
// ---------------------------------------------------------------------------

func (b *Backend) IsRemote() bool {
	return b.agent == nil
}

// ---------------------------------------------------------------------------
// dispatch[Req, Res] — typed return methods
// ---------------------------------------------------------------------------

func (b *Backend) GetSettings(namespace, senderID string) (map[string]string, error) {
	return dispatch(b, "get_settings",
		getSettingsReq{Namespace: namespace, SenderID: senderID},
		func(a *Agent, r getSettingsReq) (map[string]string, error) {
			if a.settingsSvc == nil {
				return nil, ErrSettingsUnavailable
			}
			return a.settingsSvc.GetSettings(r.Namespace, r.SenderID)
		})
}

func (b *Backend) GetDefaultModel() string {
	result, _ := dispatch(b, "get_default_model",
		struct{}{},
		func(a *Agent, _ struct{}) (string, error) {
			return a.GetDefaultModel(), nil
		})
	return result
}

func (b *Backend) GetUserMaxContext(senderID string) int {
	result, _ := dispatch(b, "get_user_max_context",
		getUserMaxContextReq{SenderID: senderID},
		func(a *Agent, r getUserMaxContextReq) (int, error) {
			return a.GetUserMaxContext(r.SenderID), nil
		})
	return result
}

func (b *Backend) GetUserMaxOutputTokens(senderID string) int {
	result, _ := dispatch(b, "get_user_max_output_tokens",
		getUserMaxOutputTokensReq{SenderID: senderID},
		func(a *Agent, r getUserMaxOutputTokensReq) (int, error) {
			return a.GetUserMaxOutputTokens(r.SenderID), nil
		})
	return result
}

func (b *Backend) GetUserThinkingMode(senderID string) string {
	result, _ := dispatch(b, "get_user_thinking_mode",
		getUserThinkingModeReq{SenderID: senderID},
		func(a *Agent, r getUserThinkingModeReq) (string, error) {
			return a.GetUserThinkingMode(r.SenderID), nil
		})
	return result
}

func (b *Backend) GetLLMConcurrency(senderID string) int {
	result, _ := dispatch(b, "get_llm_concurrency",
		getLLMConcurrencyReq{SenderID: senderID},
		func(a *Agent, r getLLMConcurrencyReq) (int, error) {
			return a.GetLLMConcurrency(r.SenderID), nil
		})
	return result
}

func (b *Backend) GetContextMode() string {
	result, _ := dispatch(b, "get_context_mode",
		struct{}{},
		func(a *Agent, _ struct{}) (string, error) {
			return a.GetContextMode(), nil
		})
	return result
}

func (b *Backend) ListModels() []string {
	result, _ := dispatch(b, "list_models",
		struct{}{},
		func(a *Agent, _ struct{}) ([]string, error) {
			return a.llmFactory.ListModels(), nil
		})
	return result
}

func (b *Backend) ListAllModels() []string {
	result, _ := dispatch(b, "list_all_models",
		struct{}{},
		func(a *Agent, _ struct{}) ([]string, error) {
			return a.llmFactory.ListAllModelsForUser(""), nil
		})
	return result
}

func (b *Backend) GetUserTokenUsage(senderID string) (map[string]any, error) {
	return dispatch(b, "get_user_token_usage",
		getUserTokenUsageReq{SenderID: senderID},
		func(a *Agent, r getUserTokenUsageReq) (map[string]any, error) {
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
}

func (b *Backend) GetDailyTokenUsage(senderID string, days int) ([]map[string]any, error) {
	return dispatch(b, "get_daily_token_usage",
		getDailyTokenUsageReq{SenderID: senderID, Days: days},
		func(a *Agent, r getDailyTokenUsageReq) ([]map[string]any, error) {
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
}

func (b *Backend) GetBgTaskCount(sessionKey string) int {
	result, _ := dispatch(b, "get_bg_task_count",
		getBgTaskCountReq{SessionKey: sessionKey},
		func(a *Agent, r getBgTaskCountReq) (int, error) {
			if a.bgTaskMgr == nil {
				return 0, nil
			}
			return len(a.bgTaskMgr.ListRunning(r.SessionKey)), nil
		})
	return result
}

func (b *Backend) ListBgTasks(sessionKey string) ([]BgTaskJSON, error) {
	return dispatch(b, "list_bg_tasks",
		listBgTasksReq{SessionKey: sessionKey},
		func(a *Agent, r listBgTasksReq) ([]BgTaskJSON, error) {
			if a.bgTaskMgr == nil {
				return nil, nil
			}
			tasks := a.bgTaskMgr.ListAllForSession(r.SessionKey)
			result := make([]BgTaskJSON, len(tasks))
			for i, t := range tasks {
				result[i] = BgTaskJSON{
					ID:        t.ID,
					Command:   t.Command,
					Status:    string(t.Status),
					StartedAt: t.StartedAt.Format(time.RFC3339),
					ExitCode:  t.ExitCode,
					Output:    t.Output,
					Error:     t.Error,
				}
				if t.FinishedAt != nil {
					result[i].FinishedAt = t.FinishedAt.Format(time.RFC3339)
				}
			}
			return result, nil
		})
}

func (b *Backend) ListTenants() ([]TenantInfo, error) {
	return dispatch(b, "list_tenants",
		struct{}{},
		func(a *Agent, _ struct{}) ([]TenantInfo, error) {
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
					ID:           t.ID,
					Channel:      t.Channel,
					ChatID:       t.ChatID,
					CreatedAt:    t.CreatedAt.Format(time.RFC3339),
					LastActiveAt: t.LastActiveAt.Format(time.RFC3339),
				}
			}
			return result, nil
		})
}

func (b *Backend) ListSubscriptions(senderID string) ([]channel.Subscription, error) {
	return dispatch(b, "list_subscriptions",
		listSubscriptionsReq{SenderID: senderID},
		func(a *Agent, r listSubscriptionsReq) ([]channel.Subscription, error) {
			svc := a.llmFactory.GetSubscriptionSvc()
			if svc == nil {
				return nil, nil
			}
			subs, err := svc.List(r.SenderID)
			if err != nil || subs == nil {
				return nil, err
			}
			result := make([]channel.Subscription, len(subs))
			for i, s := range subs {
				result[i] = channel.Subscription{
					ID: s.ID, Name: s.Name, Provider: s.Provider,
					BaseURL: s.BaseURL, APIKey: s.APIKey,
					Model: s.Model, Active: s.IsDefault,
					MaxOutputTokens: s.MaxOutputTokens, ThinkingMode: s.ThinkingMode,
				}
			}
			return result, nil
		})
}

func (b *Backend) GetDefaultSubscription(senderID string) (*channel.Subscription, error) {
	return dispatch(b, "get_default_subscription",
		getDefaultSubscriptionReq{SenderID: senderID},
		func(a *Agent, r getDefaultSubscriptionReq) (*channel.Subscription, error) {
			svc := a.llmFactory.GetSubscriptionSvc()
			if svc == nil {
				return nil, nil
			}
			sub, err := svc.GetDefault(r.SenderID)
			if err != nil || sub == nil {
				return nil, err
			}
			return &channel.Subscription{
				ID: sub.ID, Name: sub.Name, Provider: sub.Provider,
				BaseURL: sub.BaseURL, APIKey: sub.APIKey,
				Model: sub.Model, Active: sub.IsDefault,
				MaxOutputTokens: sub.MaxOutputTokens, ThinkingMode: sub.ThinkingMode,
			}, nil
		})
}

func (b *Backend) GetHistory(channelName, chatID string) ([]channel.HistoryMessage, error) {
	return dispatch(b, "get_history",
		getHistoryReq{Channel: channelName, ChatID: chatID},
		func(a *Agent, r getHistoryReq) ([]channel.HistoryMessage, error) {
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
}

func (b *Backend) GetTokenState(ch, chatID string) (int64, int64, error) {
	if b.agent != nil {
		ms := b.agent.MultiSession()
		if ms == nil {
			return 0, 0, nil
		}
		sess, err := ms.GetOrCreateSession(ch, chatID)
		if err != nil {
			return 0, 0, err
		}
		memSvc := sess.MemoryService()
		if memSvc == nil {
			return 0, 0, nil
		}
		pt, ct, err := memSvc.GetTokenState(context.Background(), sess.TenantID())
		if err != nil {
			return 0, 0, err
		}
		return pt, ct, nil
	}
	raw, err := dispatch(b, "get_token_state",
		getTokenStateReq{Channel: ch, ChatID: chatID},
		func(a *Agent, r getTokenStateReq) (struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		}, error) {
			return struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			}{}, nil
		})
	if err != nil {
		return 0, 0, err
	}
	return raw.PromptTokens, raw.CompletionTokens, nil
}

func (b *Backend) GetChannelConfigs() (map[string]map[string]string, error) {
	return dispatch(b, "get_channel_config",
		struct{}{},
		func(a *Agent, _ struct{}) (map[string]map[string]string, error) {
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
}

func (b *Backend) CountInteractiveSessions(channelName, chatID string) int {
	result, _ := dispatch(b, "count_interactive_sessions",
		countInteractiveSessionsReq{ChannelName: channelName, ChatID: chatID},
		func(a *Agent, r countInteractiveSessionsReq) (int, error) {
			return a.CountInteractiveSessions(r.ChannelName, r.ChatID), nil
		})
	return result
}

func (b *Backend) ListInteractiveSessions(channelName, chatID string) []InteractiveSessionInfo {
	result, _ := dispatch(b, "list_interactive_sessions",
		listInteractiveSessionsReq{ChannelName: channelName, ChatID: chatID},
		func(a *Agent, r listInteractiveSessionsReq) ([]InteractiveSessionInfo, error) {
			return a.ListInteractiveSessions(r.ChannelName, r.ChatID), nil
		})
	return result
}

func (b *Backend) InspectInteractiveSession(ctx context.Context, roleName, channelName, chatID, instance string, tailCount int) (string, error) {
	if b.agent != nil {
		return b.agent.InspectInteractiveSession(ctx, roleName, channelName, chatID, instance, tailCount)
	}
	return dispatch(b, "inspect_interactive_session",
		inspectInteractiveSessionReq{
			RoleName: roleName, ChannelName: channelName,
			ChatID: chatID, Instance: instance, TailCount: tailCount,
		},
		func(a *Agent, r inspectInteractiveSessionReq) (string, error) {
			return a.InspectInteractiveSession(ctx, r.RoleName, r.ChannelName, r.ChatID, r.Instance, r.TailCount)
		})
}

func (b *Backend) GetSessionMessages(channelName, chatID, roleName, instance string) ([]SessionMessage, bool) {
	if b.agent != nil {
		return b.agent.GetSessionMessages(channelName, chatID, roleName, instance)
	}
	result, err := dispatch(b, "get_session_messages",
		getSessionMessagesReq{
			ChannelName: channelName, ChatID: chatID,
			RoleName: roleName, Instance: instance,
		},
		func(a *Agent, r getSessionMessagesReq) ([]SessionMessage, error) {
			msgs, ok := a.GetSessionMessages(r.ChannelName, r.ChatID, r.RoleName, r.Instance)
			if !ok {
				return nil, errors.New("session not found")
			}
			return msgs, nil
		})
	if err != nil {
		return nil, false
	}
	return result, true
}

func (b *Backend) GetAgentSessionDump(channelName, chatID, roleName, instance string) (*AgentSessionDump, bool) {
	if b.agent != nil {
		return b.agent.GetAgentSessionDump(channelName, chatID, roleName, instance)
	}
	result, err := dispatch(b, "get_agent_session_dump",
		getAgentSessionDumpReq{
			ChannelName: channelName, ChatID: chatID,
			RoleName: roleName, Instance: instance,
		},
		func(a *Agent, r getAgentSessionDumpReq) (*AgentSessionDump, error) {
			dump, ok := a.GetAgentSessionDump(r.ChannelName, r.ChatID, r.RoleName, r.Instance)
			if !ok {
				return nil, errors.New("session not found")
			}
			return dump, nil
		})
	if err != nil {
		return nil, false
	}
	return result, true
}

func (b *Backend) GetAgentSessionDumpByFullKey(fullKey string) (*AgentSessionDump, bool) {
	if b.agent != nil {
		return b.agent.GetAgentSessionDumpByFullKey(fullKey)
	}
	result, err := dispatch(b, "get_agent_session_dump_by_full_key",
		getAgentSessionDumpByFullKeyReq{FullKey: fullKey},
		func(a *Agent, r getAgentSessionDumpByFullKeyReq) (*AgentSessionDump, error) {
			dump, ok := a.GetAgentSessionDumpByFullKey(r.FullKey)
			if !ok {
				return nil, errors.New("session not found")
			}
			return dump, nil
		})
	if err != nil {
		return nil, false
	}
	return result, true
}

func (b *Backend) IsProcessing(ch, chatID string) bool {
	result, _ := dispatch(b, "is_processing",
		isProcessingReq{Channel: ch, ChatID: chatID},
		func(a *Agent, r isProcessingReq) (bool, error) {
			prefix := r.Channel + ":" + r.ChatID + ":"
			found := false
			a.chatCancelCh.Range(func(key, _ any) bool {
				if k, ok := key.(string); ok && strings.HasPrefix(k, prefix) {
					found = true
					return false
				}
				return true
			})
			return found, nil
		})
	return result
}

func (b *Backend) GetActiveProgress(ch, chatID string) *channel.CLIProgressPayload {
	if b.agent != nil {
		key := ch + ":" + chatID
		v, ok := b.agent.lastProgressSnapshot.Load(key)
		if !ok {
			return nil
		}
		snapshot := v.(*channel.CLIProgressPayload)
		if histPtr, ok := b.agent.iterationHistories.Load(key); ok {
			hist := *histPtr.(*[]channel.CLIProgressPayload)
			if len(hist) > 0 {
				result := *snapshot
				result.IterationHistory = make([]channel.CLIProgressPayload, len(hist))
				copy(result.IterationHistory, hist)
				return &result
			}
		}
		return snapshot
	}
	result, _ := dispatch(b, "get_active_progress",
		getActiveProgressReq{Channel: ch, ChatID: chatID},
		func(a *Agent, r getActiveProgressReq) (*channel.CLIProgressPayload, error) {
			key := r.Channel + ":" + r.ChatID
			v, ok := a.lastProgressSnapshot.Load(key)
			if !ok {
				return nil, nil
			}
			snapshot := v.(*channel.CLIProgressPayload)
			if histPtr, ok := a.iterationHistories.Load(key); ok {
				hist := *histPtr.(*[]channel.CLIProgressPayload)
				if len(hist) > 0 {
					result := *snapshot
					result.IterationHistory = make([]channel.CLIProgressPayload, len(hist))
					copy(result.IterationHistory, hist)
					return &result, nil
				}
			}
			return snapshot, nil
		})
	return result
}

func (b *Backend) GetMemoryStats(ctx context.Context, ch, chatID, senderID string) map[string]string {
	if b.agent != nil {
		if b.agent.multiSession == nil {
			return nil
		}
		return b.agent.multiSession.GetMemoryStats(ctx, ch, chatID, senderID)
	}
	result, _ := dispatch(b, "get_memory_stats",
		getMemoryStatsReq{Channel: ch, ChatID: chatID, SenderID: senderID},
		func(a *Agent, r getMemoryStatsReq) (map[string]string, error) {
			if a.multiSession == nil {
				return nil, nil
			}
			return a.multiSession.GetMemoryStats(context.Background(), r.Channel, r.ChatID, r.SenderID), nil
		})
	return result
}

func (b *Backend) CallRPC(method string, params any) (json.RawMessage, error) {
	return dispatch(b, method, params,
		func(a *Agent, _ any) (json.RawMessage, error) {
			return nil, fmt.Errorf("RPC not available in standalone mode")
		})
}

// ---------------------------------------------------------------------------
// dispatchVoid — fire-and-forget methods
// ---------------------------------------------------------------------------

func (b *Backend) SetMaxIterations(n int) {
	dispatchVoid(b, "set_max_iterations", n,
		func(a *Agent, v int) error { a.SetMaxIterations(v); return nil })
}

func (b *Backend) SetMaxConcurrency(n int) {
	dispatchVoid(b, "set_max_concurrency", n,
		func(a *Agent, v int) error { a.SetMaxConcurrency(v); return nil })
}

func (b *Backend) SetMaxContextTokens(n int) {
	dispatchVoid(b, "set_max_context_tokens", n,
		func(a *Agent, v int) error { a.SetMaxContextTokens(v); return nil })
}

func (b *Backend) SetCompressionThreshold(f float64) {
	dispatchVoid(b, "set_compression_threshold", f,
		func(a *Agent, v float64) error { a.SetCompressionThreshold(v); return nil })
}

func (b *Backend) SetSandbox(sb tools.Sandbox, mode string) {
	if b.agent != nil {
		b.agent.SetSandbox(sb, mode)
		return
	}
	// Remote mode: sandbox is a no-op (sandbox runs server-side).
	dispatchVoid(b, "set_sandbox", setSandboxReq{Mode: mode},
		func(a *Agent, r setSandboxReq) error { return nil })
}

func (b *Backend) SetProxyLLM(senderID string, proxy *llm.ProxyLLM, model string) {
	if b.agent != nil {
		b.agent.SetProxyLLM(senderID, proxy, model)
		return
	}
	dispatchVoid(b, "set_proxy_llm", setProxyLLMReq{SenderID: senderID, Model: model},
		func(a *Agent, r setProxyLLMReq) error { return nil })
}

func (b *Backend) ClearProxyLLM(senderID string) {
	if b.agent != nil {
		b.agent.ClearProxyLLM(senderID)
		return
	}
	dispatchVoid(b, "clear_proxy_llm", clearProxyLLMReq{SenderID: senderID},
		func(a *Agent, r clearProxyLLMReq) error { return nil })
}

func (b *Backend) CleanupCompletedBgTasks(sessionKey string) {
	if b.agent != nil {
		if b.agent.bgTaskMgr != nil {
			b.agent.bgTaskMgr.RemoveCompletedTasks(sessionKey)
		}
		return
	}
	dispatchVoid(b, "cleanup_completed_bg_tasks", cleanupCompletedBgTasksReq{SessionKey: sessionKey},
		func(a *Agent, r cleanupCompletedBgTasksReq) error { return nil })
}

func (b *Backend) ResetTokenState() {
	if b.agent != nil {
		// No-op: token state is per-tenant in DB, cleared via TrimHistory.
		return
	}
	dispatchVoid(b, "reset_token_state", struct{}{},
		func(a *Agent, _ struct{}) error { return nil })
}

// ---------------------------------------------------------------------------
// Explicit if/else — complex local logic
// ---------------------------------------------------------------------------

func (b *Backend) SetCWD(ch, chatID, dir string) error {
	if b.agent != nil {
		if b.agent.sandboxMode != "none" {
			return fmt.Errorf("CWD sync not supported in %s sandbox mode", b.agent.sandboxMode)
		}
		if b.agent.MultiSession() == nil {
			return ErrNoSessionManager
		}
		sess, err := b.agent.MultiSession().GetOrCreateSession(ch, chatID)
		if err != nil {
			return err
		}
		sess.SetCurrentDir(dir)
		return nil
	}
	_, err := dispatch(b, "set_cwd",
		setCWDReq{Channel: ch, ChatID: chatID, Dir: dir},
		func(a *Agent, r setCWDReq) (struct{}, error) { return struct{}{}, nil })
	return err
}

func (b *Backend) SetContextMode(mode string) error {
	_, err := dispatch(b, "set_context_mode",
		setContextModeReq{Mode: mode},
		func(a *Agent, r setContextModeReq) (struct{}, error) {
			return struct{}{}, a.SetContextMode(r.Mode)
		})
	return err
}

func (b *Backend) SetSetting(namespace, senderID, key, value string) error {
	_, err := dispatch(b, "set_setting",
		setSettingReq{Namespace: namespace, SenderID: senderID, Key: key, Value: value},
		func(a *Agent, r setSettingReq) (struct{}, error) {
			if a.settingsSvc == nil {
				return struct{}{}, ErrSettingsUnavailable
			}
			return struct{}{}, a.settingsSvc.SetSetting(r.Namespace, r.SenderID, r.Key, r.Value)
		})
	return err
}

func (b *Backend) SetModelTiers(cfg config.LLMConfig) error {
	_, err := dispatch(b, "set_model_tiers", cfg,
		func(a *Agent, c config.LLMConfig) (struct{}, error) {
			a.llmFactory.SetModelTiers(c)
			return struct{}{}, nil
		})
	return err
}

func (b *Backend) SetDefaultThinkingMode(mode string) error {
	_, err := dispatch(b, "set_default_thinking_mode",
		setDefaultThinkingModeReq{Mode: mode},
		func(a *Agent, r setDefaultThinkingModeReq) (struct{}, error) {
			a.llmFactory.SetDefaultThinkingMode(r.Mode)
			return struct{}{}, nil
		})
	return err
}

func (b *Backend) ClearMemory(ctx context.Context, channel, chatID, targetType, senderID string) error {
	if b.agent != nil {
		if b.agent.multiSession == nil {
			return nil
		}
		return b.agent.multiSession.ClearMemory(ctx, channel, chatID, targetType, senderID)
	}
	_, err := dispatch(b, "clear_memory",
		clearMemoryReq{Channel: channel, ChatID: chatID, TargetType: targetType, SenderID: senderID},
		func(a *Agent, r clearMemoryReq) (struct{}, error) { return struct{}{}, nil })
	return err
}

func (b *Backend) KillBgTask(taskID string) error {
	_, err := dispatch(b, "kill_bg_task",
		killBgTaskReq{TaskID: taskID},
		func(a *Agent, r killBgTaskReq) (struct{}, error) {
			if a.bgTaskMgr == nil {
				return struct{}{}, ErrBgTasksUnavailable
			}
			return struct{}{}, a.bgTaskMgr.Kill(r.TaskID)
		})
	return err
}

func (b *Backend) TrimHistory(ch, chatID string, cutoff time.Time) error {
	if b.agent != nil {
		ms := b.agent.MultiSession()
		if ms == nil {
			return fmt.Errorf("multi-session not available")
		}
		return ms.TrimHistory(ch, chatID, cutoff)
	}
	_, err := dispatch(b, "trim_history",
		trimHistoryReq{Channel: ch, ChatID: chatID, Cutoff: cutoff.Unix()},
		func(a *Agent, r trimHistoryReq) (struct{}, error) { return struct{}{}, nil })
	return err
}

func (b *Backend) SetSubscriptionModel(id, model string) error {
	_, err := dispatch(b, "set_subscription_model",
		setSubscriptionModelReq{ID: id, Model: model},
		func(a *Agent, r setSubscriptionModelReq) (struct{}, error) {
			svc := a.llmFactory.GetSubscriptionSvc()
			if svc == nil {
				return struct{}{}, ErrSubscriptionsUnavailable
			}
			sub, err := svc.Get(r.ID)
			if err != nil {
				return struct{}{}, err
			}
			if err := svc.SetModel(r.ID, r.Model); err != nil {
				return struct{}{}, err
			}
			if sub != nil {
				a.llmFactory.Invalidate(sub.SenderID)
			}
			return struct{}{}, nil
		})
	return err
}

func (b *Backend) RenameSubscription(id, name string) error {
	_, err := dispatch(b, "rename_subscription",
		renameSubscriptionReq{ID: id, Name: name},
		func(a *Agent, r renameSubscriptionReq) (struct{}, error) {
			svc := a.llmFactory.GetSubscriptionSvc()
			if svc == nil {
				return struct{}{}, ErrSubscriptionsUnavailable
			}
			return struct{}{}, svc.Rename(r.ID, r.Name)
		})
	return err
}

func (b *Backend) SetUserModel(senderID, model string) error {
	if b.agent != nil {
		return b.agent.SetUserModel(senderID, model)
	}
	_, err := dispatch(b, "set_user_model",
		setUserModelReq{SenderID: senderID, Model: model},
		func(a *Agent, r setUserModelReq) (struct{}, error) { return struct{}{}, nil })
	return err
}

func (b *Backend) SwitchModel(senderID, model string) error {
	if b.agent != nil {
		b.agent.llmFactory.SwitchModel(senderID, model)
		return nil
	}
	_, err := dispatch(b, "switch_model",
		switchModelReq{SenderID: senderID, Model: model},
		func(a *Agent, r switchModelReq) (struct{}, error) {
			a.llmFactory.SwitchModel(r.SenderID, r.Model)
			return struct{}{}, nil
		})
	return err
}

func (b *Backend) SetUserMaxContext(senderID string, maxContext int) error {
	if b.agent != nil {
		return b.agent.SetUserMaxContext(senderID, maxContext)
	}
	_, err := dispatch(b, "set_user_max_context",
		setUserMaxContextReq{SenderID: senderID, MaxContext: maxContext},
		func(a *Agent, r setUserMaxContextReq) (struct{}, error) { return struct{}{}, nil })
	return err
}

func (b *Backend) SetUserMaxOutputTokens(senderID string, maxTokens int) error {
	if b.agent != nil {
		if maxTokens < 0 {
			return fmt.Errorf("max_output_tokens must be >= 0, got %d", maxTokens)
		}
		if err := b.agent.SetUserMaxOutputTokens(senderID, maxTokens); err != nil {
			b.agent.llmFactory.SetUserMaxOutputTokens(senderID, maxTokens)
		}
		return nil
	}
	_, err := dispatch(b, "set_user_max_output_tokens",
		setUserMaxOutputTokensReq{SenderID: senderID, MaxTokens: maxTokens},
		func(a *Agent, r setUserMaxOutputTokensReq) (struct{}, error) { return struct{}{}, nil })
	return err
}

func (b *Backend) SetUserThinkingMode(senderID string, mode string) error {
	if b.agent != nil {
		validModes := map[string]bool{"": true, "enabled": true, "disabled": true, "auto": true}
		if !validModes[mode] {
			return fmt.Errorf("invalid thinking_mode: %q", mode)
		}
		if err := b.agent.SetUserThinkingMode(senderID, mode); err != nil {
			b.agent.llmFactory.SetUserThinkingMode(senderID, mode)
		}
		return nil
	}
	_, err := dispatch(b, "set_user_thinking_mode",
		setUserThinkingModeReq{SenderID: senderID, Mode: mode},
		func(a *Agent, r setUserThinkingModeReq) (struct{}, error) { return struct{}{}, nil })
	return err
}

func (b *Backend) SetLLMConcurrency(senderID string, personal int) error {
	if b.agent != nil {
		return b.agent.SetLLMConcurrency(senderID, personal)
	}
	_, err := dispatch(b, "set_llm_concurrency",
		setLLMConcurrencyReq{SenderID: senderID, Personal: personal},
		func(a *Agent, r setLLMConcurrencyReq) (struct{}, error) { return struct{}{}, nil })
	return err
}

func (b *Backend) AddSubscription(senderID string, sub channel.Subscription) error {
	if b.agent != nil {
		svc := b.agent.llmFactory.GetSubscriptionSvc()
		if svc == nil {
			return ErrSubscriptionsUnavailable
		}
		if err := svc.Add(&sqlite.LLMSubscription{
			ID: sub.ID, SenderID: senderID, Name: sub.Name,
			Provider: sub.Provider, BaseURL: sub.BaseURL, APIKey: sub.APIKey,
			Model: sub.Model, IsDefault: sub.Active,
		}); err != nil {
			return err
		}
		b.agent.llmFactory.Invalidate(senderID)
		return nil
	}
	_, err := dispatch(b, "add_subscription",
		addSubscriptionReq{
			SenderID: senderID,
			Sub: channelSubscriptionJSON{
				ID: sub.ID, Name: sub.Name, Provider: sub.Provider,
				BaseURL: sub.BaseURL, APIKey: sub.APIKey,
				Model: sub.Model, Active: sub.Active,
				MaxOutputTokens: sub.MaxOutputTokens, ThinkingMode: sub.ThinkingMode,
			},
		},
		func(a *Agent, r addSubscriptionReq) (struct{}, error) { return struct{}{}, nil })
	return err
}

func (b *Backend) RemoveSubscription(id string) error {
	if b.agent != nil {
		svc := b.agent.llmFactory.GetSubscriptionSvc()
		if svc == nil {
			return ErrSubscriptionsUnavailable
		}
		sub, err := svc.Get(id)
		if err != nil {
			return err
		}
		if err := svc.Remove(id); err != nil {
			return err
		}
		if sub != nil {
			b.agent.llmFactory.Invalidate(sub.SenderID)
		}
		return nil
	}
	_, err := dispatch(b, "remove_subscription",
		removeSubscriptionReq{ID: id},
		func(a *Agent, r removeSubscriptionReq) (struct{}, error) { return struct{}{}, nil })
	return err
}

func (b *Backend) SetDefaultSubscription(id string, chatID string) error {
	if b.agent != nil {
		svc := b.agent.llmFactory.GetSubscriptionSvc()
		if svc == nil {
			return ErrSubscriptionsUnavailable
		}
		if err := svc.SetDefault(id); err != nil {
			return err
		}
		sub, err := svc.Get(id)
		if err == nil && sub != nil {
			b.agent.llmFactory.Invalidate(sub.SenderID)
			if err := b.agent.llmFactory.SwitchSubscription(sub.SenderID, sub, chatID); err != nil {
				return err
			}
		}
		return nil
	}
	_, err := dispatch(b, "set_default_subscription",
		setDefaultSubscriptionReq{ID: id, ChatID: chatID},
		func(a *Agent, r setDefaultSubscriptionReq) (struct{}, error) { return struct{}{}, nil })
	return err
}

func (b *Backend) UpdateSubscription(id string, sub channel.Subscription) error {
	if b.agent != nil {
		svc := b.agent.llmFactory.GetSubscriptionSvc()
		if svc == nil {
			return ErrSubscriptionsUnavailable
		}
		existing, err := svc.Get(id)
		if err != nil {
			return err
		}
		if existing == nil {
			return fmt.Errorf("subscription %s not found", id)
		}
		dbSub := &sqlite.LLMSubscription{
			ID:              id,
			SenderID:        existing.SenderID,
			Name:            sub.Name,
			Provider:        sub.Provider,
			BaseURL:         sub.BaseURL,
			APIKey:          sub.APIKey,
			Model:           sub.Model,
			MaxContext:      existing.MaxContext,
			MaxOutputTokens: sub.MaxOutputTokens,
			ThinkingMode:    sub.ThinkingMode,
			IsDefault:       sub.Active,
		}
		// Never overwrite with a masked key from server RPC transport.
		if strings.HasSuffix(dbSub.APIKey, "****") && len(dbSub.APIKey) <= 20 {
			dbSub.APIKey = existing.APIKey
		}
		if err := svc.Update(dbSub); err != nil {
			return err
		}
		b.agent.llmFactory.Invalidate(existing.SenderID)
		return nil
	}
	_, err := dispatch(b, "update_subscription",
		updateSubscriptionReq{
			ID: id,
			Sub: channelSubscriptionJSON{
				ID: sub.ID, Name: sub.Name, Provider: sub.Provider,
				BaseURL: sub.BaseURL, APIKey: sub.APIKey,
				Model: sub.Model, Active: sub.Active,
				MaxOutputTokens: sub.MaxOutputTokens, ThinkingMode: sub.ThinkingMode,
			},
		},
		func(a *Agent, r updateSubscriptionReq) (struct{}, error) { return struct{}{}, nil })
	return err
}

func (b *Backend) SetChannelConfig(channel string, values map[string]string) error {
	if b.agent != nil {
		cfg := config.LoadFromFile(config.ConfigFilePath())
		if cfg == nil {
			cfg = &config.Config{}
		}
		switch channel {
		case "web":
			if v, ok := values["enabled"]; ok {
				cfg.Web.Enable, _ = strconv.ParseBool(v)
			} else if v, ok := values["enable"]; ok {
				cfg.Web.Enable, _ = strconv.ParseBool(v)
			}
			if v, ok := values["host"]; ok {
				cfg.Web.Host = v
			}
			if v, ok := values["port"]; ok {
				cfg.Web.Port, _ = strconv.Atoi(v)
			}
		case "feishu":
			if v, ok := values["enabled"]; ok {
				cfg.Feishu.Enabled, _ = strconv.ParseBool(v)
			}
			if v, ok := values["app_id"]; ok {
				cfg.Feishu.AppID = v
			}
			if v, ok := values["app_secret"]; ok {
				cfg.Feishu.AppSecret = v
			}
			if v, ok := values["encrypt_key"]; ok {
				cfg.Feishu.EncryptKey = v
			}
			if v, ok := values["verification_token"]; ok {
				cfg.Feishu.VerificationToken = v
			}
			if v, ok := values["domain"]; ok {
				cfg.Feishu.Domain = v
			}
		case "qq":
			if v, ok := values["enabled"]; ok {
				cfg.QQ.Enabled, _ = strconv.ParseBool(v)
			}
			if v, ok := values["app_id"]; ok {
				cfg.QQ.AppID = v
			}
			if v, ok := values["client_secret"]; ok {
				cfg.QQ.ClientSecret = v
			}
		case "napcat":
			if v, ok := values["enabled"]; ok {
				cfg.NapCat.Enabled, _ = strconv.ParseBool(v)
			}
			if v, ok := values["ws_url"]; ok {
				cfg.NapCat.WSUrl = v
			}
			if v, ok := values["token"]; ok {
				cfg.NapCat.Token = v
			}
		default:
			return fmt.Errorf("unknown channel: %s", channel)
		}
		err := config.SaveToFile(config.ConfigFilePath(), cfg)
		if b.reconfigureFn != nil {
			b.reconfigureFn(channel)
		}
		return err
	}
	_, err := dispatch(b, "set_channel_config",
		setChannelConfigReq{Channel: channel, Values: values},
		func(a *Agent, r setChannelConfigReq) (struct{}, error) { return struct{}{}, nil })
	return err
}

// Ensure Backend implements AgentBackend.
var _ AgentBackend = (*Backend)(nil)

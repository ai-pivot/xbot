package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Backend is the unified AgentBackend implementation.
// It is a pure typed RPC client — every method goes through transport.Call().
// The transport decides whether the call executes locally (localTransport)
// or remotely (RemoteTransport). Backend never branches on mode.
type Backend struct {
	agent     *Agent          // nil in remote mode; for local-only accessors
	bus       *bus.MessageBus // nil in remote mode
	transport Transport
}

// NewBackend creates a local-mode Backend with an in-process Agent.
func NewBackend(cfg Config) (*Backend, error) {
	a, err := New(cfg)
	if err != nil {
		return nil, err
	}
	lt := newLocalTransport(a, cfg.Bus)
	return &Backend{agent: a, bus: cfg.Bus, transport: lt}, nil
}

// NewTransportBackend creates a Backend from an existing Transport (remote mode).
func NewTransportBackend(t Transport) *Backend {
	return &Backend{transport: t}
}

// NewRemoteBackend creates a remote-mode Backend from a RemoteTransportConfig.
func NewRemoteBackend(cfg RemoteTransportConfig) *Backend {
	return &Backend{transport: NewRemoteTransport(cfg)}
}

// ---------------------------------------------------------------------------
// Generic RPC helpers — the only two functions Backend ever needs
// ---------------------------------------------------------------------------

// call marshals req, calls transport, unmarshals into result.
// result may be nil for void methods.
func (b *Backend) call(method string, req any, result any) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("%s: marshal: %w", method, err)
	}
	raw, err := b.transport.Call(method, payload)
	if err != nil {
		return err
	}
	if result != nil && len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, result); err != nil {
			return fmt.Errorf("%s: unmarshal: %w", method, err)
		}
	}
	return nil
}

// callVoid is fire-and-forget: errors are logged, not returned.
func (b *Backend) callVoid(method string, req any) {
	if err := b.call(method, req, nil); err != nil {
		log.WithError(err).WithField("method", method).Warn("Backend: call failed")
	}
}

// withAgent returns the result of fn(agent), or zero-value if remote.
func withAgent[T any](b *Backend, fn func(*Agent) T) T {
	if b.agent == nil {
		var zero T
		return zero
	}
	return fn(b.agent)
}

// ---------------------------------------------------------------------------
// Lifecycle — pure transport delegation
// ---------------------------------------------------------------------------

func (b *Backend) Start(ctx context.Context) error { return b.transport.Start(ctx) }
func (b *Backend) Stop()                           { b.transport.Stop() }
func (b *Backend) Close() error                    { return b.transport.Close() }
func (b *Backend) Run(ctx context.Context) error   { return b.transport.Run(ctx) }

// ---------------------------------------------------------------------------
// Communication — pure transport delegation
// ---------------------------------------------------------------------------

func (b *Backend) SendInbound(msg bus.InboundMessage) error {
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
// Callback setters — pure transport delegation
// ---------------------------------------------------------------------------

func (b *Backend) OnOutbound(cb func(bus.OutboundMessage))         { b.transport.OnOutbound(cb) }
func (b *Backend) OnProgress(cb func(*channel.CLIProgressPayload)) { b.transport.OnProgress(cb) }
func (b *Backend) OnInjectUserMessage(cb func(content string))     { b.transport.OnInjectUserMessage(cb) }
func (b *Backend) OnReconnect(cb func())                           { b.transport.OnReconnect(cb) }
func (b *Backend) OnConnStateChange(cb func(state string))         { b.transport.OnConnStateChange(cb) }
func (b *Backend) OnPluginWidgets(cb func(zones map[string]string, chatID string)) {
	b.transport.OnPluginWidgets(cb)
}
func (b *Backend) Subscribe(chatID string) error { return b.transport.Subscribe(chatID) }
func (b *Backend) ConnState() string             { return b.transport.ConnState() }
func (b *Backend) ServerURL() string             { return b.transport.ServerURL() }

// ---------------------------------------------------------------------------
// Local-only identity & accessors (nil in remote mode)
// ---------------------------------------------------------------------------

func (b *Backend) Agent() *Agent                     { return b.agent }
func (b *Backend) IsRemote() bool                    { return b.transport.IsRemote() }
func (b *Backend) Bus() *bus.MessageBus              { return b.bus }
func (b *Backend) LLMFactory() *LLMFactory           { return withAgent(b, (*Agent).LLMFactory) }
func (b *Backend) SettingsService() *SettingsService { return withAgent(b, (*Agent).SettingsService) }
func (b *Backend) MultiSession() *session.MultiTenantSession {
	return withAgent(b, (*Agent).MultiSession)
}
func (b *Backend) BgTaskManager() *tools.BackgroundTaskManager {
	return withAgent(b, (*Agent).BgTaskManager)
}
func (b *Backend) HookManager() *hooks.Manager          { return withAgent(b, (*Agent).HookManager) }
func (b *Backend) ApprovalState() *hooks.ApprovalState  { return withAgent(b, (*Agent).ApprovalState) }
func (b *Backend) PluginManager() *plugin.PluginManager { return withAgent(b, (*Agent).PluginManager) }
func (b *Backend) GetCardBuilder() *tools.CardBuilder   { return withAgent(b, (*Agent).GetCardBuilder) }
func (b *Backend) RegistryManager() *RegistryManager    { return withAgent(b, (*Agent).RegistryManager) }

// ---------------------------------------------------------------------------
// Local-only setters (non-serializable args, no-op in remote mode)
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

func (b *Backend) SetSandbox(sb tools.Sandbox, mode string) {
	if b.agent != nil {
		b.agent.SetSandbox(sb, mode)
	}
}

func (b *Backend) SetProxyLLM(senderID string, proxy *llm.ProxyLLM, model string) {
	if b.agent != nil {
		b.agent.SetProxyLLM(senderID, proxy, model)
	}
}

func (b *Backend) SetChannelReconfigureFn(fn func(channel string)) {
	if lt, ok := b.transport.(*localTransport); ok {
		lt.reconfigureFn = fn
	}
}

// ---------------------------------------------------------------------------
// RPC methods — every method is a single b.call() / b.callVoid()
// ---------------------------------------------------------------------------

// ── Settings ──────────────────────────────────────────────────────────────

func (b *Backend) GetSettings(namespace, senderID string) (map[string]string, error) {
	var r map[string]string
	return r, b.call(MethodGetSettings, getSettingsReq{Namespace: namespace, SenderID: senderID}, &r)
}

func (b *Backend) SetSetting(namespace, senderID, key, value string) error {
	return b.call(MethodSetSetting, setSettingReq{Namespace: namespace, SenderID: senderID, Key: key, Value: value}, nil)
}

// ── Model / LLM ───────────────────────────────────────────────────────────

func (b *Backend) GetDefaultModel() string {
	var r string
	_ = b.call(MethodGetDefaultModel, struct{}{}, &r)
	return r
}

func (b *Backend) GetContextMode() string {
	var r string
	_ = b.call(MethodGetContextMode, struct{}{}, &r)
	return r
}

func (b *Backend) ListModels() []string {
	var r []string
	_ = b.call(MethodListModels, struct{}{}, &r)
	return r
}

func (b *Backend) ListAllModels() []string {
	var r []string
	_ = b.call(MethodListAllModels, struct{}{}, &r)
	return r
}

func (b *Backend) SetModelTiers(cfg config.LLMConfig) error {
	return b.call(MethodSetModelTiers, cfg, nil)
}

func (b *Backend) SetDefaultThinkingMode(mode string) error {
	return b.call(MethodSetDefaultThinkingMode, setDefaultThinkingModeReq{Mode: mode}, nil)
}

func (b *Backend) ClearProxyLLM(senderID string) {
	b.callVoid(MethodClearProxyLLM, clearProxyLLMReq{SenderID: senderID})
}

// ── Per-user settings ─────────────────────────────────────────────────────

func (b *Backend) GetUserMaxContext(senderID string) int {
	var r int
	_ = b.call(MethodGetUserMaxContext, getUserMaxContextReq{SenderID: senderID}, &r)
	return r
}

func (b *Backend) SetUserMaxContext(senderID string, maxContext int) error {
	return b.call(MethodSetUserMaxContext, setUserMaxContextReq{SenderID: senderID, MaxContext: maxContext}, nil)
}

func (b *Backend) GetUserMaxOutputTokens(senderID string) int {
	var r int
	_ = b.call(MethodGetUserMaxOutputTokens, getUserMaxOutputTokensReq{SenderID: senderID}, &r)
	return r
}

func (b *Backend) SetUserMaxOutputTokens(senderID string, maxTokens int) error {
	return b.call(MethodSetUserMaxOutputTokens, setUserMaxOutputTokensReq{SenderID: senderID, MaxTokens: maxTokens}, nil)
}

func (b *Backend) GetUserThinkingMode(senderID string) string {
	var r string
	_ = b.call(MethodGetUserThinkingMode, getUserThinkingModeReq{SenderID: senderID}, &r)
	return r
}

func (b *Backend) SetUserThinkingMode(senderID string, mode string) error {
	return b.call(MethodSetUserThinkingMode, setUserThinkingModeReq{SenderID: senderID, Mode: mode}, nil)
}

func (b *Backend) GetLLMConcurrency(senderID string) int {
	var r int
	_ = b.call(MethodGetLLMConcurrency, getLLMConcurrencyReq{SenderID: senderID}, &r)
	return r
}

func (b *Backend) SetLLMConcurrency(senderID string, personal int) error {
	return b.call(MethodSetLLMConcurrency, setLLMConcurrencyReq{SenderID: senderID, Personal: personal}, nil)
}

func (b *Backend) SetUserModel(senderID, model string) error {
	return b.call(MethodSetUserModel, setUserModelReq{SenderID: senderID, Model: model}, nil)
}

func (b *Backend) SwitchModel(senderID, model string) error {
	return b.call(MethodSwitchModel, switchModelReq{SenderID: senderID, Model: model}, nil)
}

// ── Runtime config ────────────────────────────────────────────────────────

func (b *Backend) SetMaxIterations(n int)            { b.callVoid(MethodSetMaxIterations, n) }
func (b *Backend) SetMaxConcurrency(n int)           { b.callVoid(MethodSetMaxConcurrency, n) }
func (b *Backend) SetMaxContextTokens(n int)         { b.callVoid(MethodSetMaxContextTokens, n) }
func (b *Backend) SetCompressionThreshold(f float64) { b.callVoid(MethodSetCompressionThreshold, f) }
func (b *Backend) ResetTokenState()                  { b.callVoid(MethodResetTokenState, struct{}{}) }
func (b *Backend) CleanupCompletedBgTasks(sessionKey string) {
	b.callVoid(MethodCleanupCompletedBgTasks, cleanupCompletedBgTasksReq{SessionKey: sessionKey})
}

func (b *Backend) SetContextMode(mode string) error {
	return b.call(MethodSetContextMode, setContextModeReq{Mode: mode}, nil)
}

func (b *Backend) SetCWD(ch, chatID, dir string) error {
	return b.call(MethodSetCWD, setCWDReq{Channel: ch, ChatID: chatID, Dir: dir}, nil)
}

// ── Token usage ───────────────────────────────────────────────────────────

func (b *Backend) GetUserTokenUsage(senderID string) (map[string]any, error) {
	var r map[string]any
	return r, b.call(MethodGetUserTokenUsage, getUserTokenUsageReq{SenderID: senderID}, &r)
}

func (b *Backend) GetDailyTokenUsage(senderID string, days int) ([]map[string]any, error) {
	var r []map[string]any
	return r, b.call(MethodGetDailyTokenUsage, getDailyTokenUsageReq{SenderID: senderID, Days: days}, &r)
}

func (b *Backend) GetTokenState(ch, chatID string) (int64, int64, error) {
	var r struct {
		Prompt     int64 `json:"prompt_tokens"`
		Completion int64 `json:"completion_tokens"`
	}
	if err := b.call(MethodGetTokenState, getTokenStateReq{Channel: ch, ChatID: chatID}, &r); err != nil {
		return 0, 0, err
	}
	return r.Prompt, r.Completion, nil
}

// ── Background tasks ──────────────────────────────────────────────────────

func (b *Backend) GetBgTaskCount(sessionKey string) int {
	var r int
	_ = b.call(MethodGetBgTaskCount, getBgTaskCountReq{SessionKey: sessionKey}, &r)
	return r
}

func (b *Backend) ListBgTasks(sessionKey string) ([]BgTaskJSON, error) {
	var r []BgTaskJSON
	return r, b.call(MethodListBgTasks, listBgTasksReq{SessionKey: sessionKey}, &r)
}

func (b *Backend) KillBgTask(taskID string) error {
	return b.call(MethodKillBgTask, killBgTaskReq{TaskID: taskID}, nil)
}

// ── Tenants ───────────────────────────────────────────────────────────────

func (b *Backend) ListTenants() ([]TenantInfo, error) {
	var r []TenantInfo
	return r, b.call(MethodListTenants, struct{}{}, &r)
}

// ── Subscriptions ─────────────────────────────────────────────────────────

func (b *Backend) ListSubscriptions(senderID string) ([]channel.Subscription, error) {
	var r []channel.Subscription
	return r, b.call(MethodListSubscriptions, listSubscriptionsReq{SenderID: senderID}, &r)
}

func (b *Backend) GetDefaultSubscription(senderID string) (*channel.Subscription, error) {
	var r *channel.Subscription
	return r, b.call(MethodGetDefaultSubscription, getDefaultSubscriptionReq{SenderID: senderID}, &r)
}

func (b *Backend) AddSubscription(senderID string, sub channel.Subscription) error {
	return b.call(MethodAddSubscription, addSubscriptionReq{
		SenderID: senderID,
		Sub: channelSubscriptionJSON{
			ID: sub.ID, Name: sub.Name, Provider: sub.Provider,
			BaseURL: sub.BaseURL, APIKey: sub.APIKey,
			Model: sub.Model, Active: sub.Active,
			MaxOutputTokens: sub.MaxOutputTokens, ThinkingMode: sub.ThinkingMode,
		},
	}, nil)
}

func (b *Backend) RemoveSubscription(id string) error {
	return b.call(MethodRemoveSubscription, removeSubscriptionReq{ID: id}, nil)
}

func (b *Backend) SetDefaultSubscription(id string, chatID string) error {
	return b.call(MethodSetDefaultSubscription, setDefaultSubscriptionReq{ID: id, ChatID: chatID}, nil)
}

func (b *Backend) UpdateSubscription(id string, sub channel.Subscription) error {
	return b.call(MethodUpdateSubscription, updateSubscriptionReq{
		ID: id,
		Sub: channelSubscriptionJSON{
			ID: sub.ID, Name: sub.Name, Provider: sub.Provider,
			BaseURL: sub.BaseURL, APIKey: sub.APIKey,
			Model: sub.Model, Active: sub.Active,
			MaxOutputTokens: sub.MaxOutputTokens, ThinkingMode: sub.ThinkingMode,
		},
	}, nil)
}

func (b *Backend) SetSubscriptionModel(id, model string) error {
	return b.call(MethodSetSubscriptionModel, setSubscriptionModelReq{ID: id, Model: model}, nil)
}

func (b *Backend) RenameSubscription(id, name string) error {
	return b.call(MethodRenameSubscription, renameSubscriptionReq{ID: id, Name: name}, nil)
}

// ── Memory / session / history ────────────────────────────────────────────

func (b *Backend) ClearMemory(ctx context.Context, channelName, chatID, targetType, senderID string) error {
	return b.call(MethodClearMemory, clearMemoryReq{
		Channel: channelName, ChatID: chatID, TargetType: targetType, SenderID: senderID,
	}, nil)
}

func (b *Backend) GetMemoryStats(ctx context.Context, ch, chatID, senderID string) map[string]string {
	var r map[string]string
	_ = b.call(MethodGetMemoryStats, getMemoryStatsReq{Channel: ch, ChatID: chatID, SenderID: senderID}, &r)
	return r
}

func (b *Backend) GetHistory(channelName, chatID string) ([]channel.HistoryMessage, error) {
	var r []channel.HistoryMessage
	return r, b.call(MethodGetHistory, getHistoryReq{Channel: channelName, ChatID: chatID}, &r)
}

func (b *Backend) TrimHistory(ch, chatID string, cutoff time.Time) error {
	return b.call(MethodTrimHistory, trimHistoryReq{Channel: ch, ChatID: chatID, Cutoff: cutoff.Unix()}, nil)
}

// ── Interactive sessions ──────────────────────────────────────────────────

func (b *Backend) CountInteractiveSessions(channelName, chatID string) int {
	var r int
	_ = b.call(MethodCountInteractiveSessions, countInteractiveSessionsReq{ChannelName: channelName, ChatID: chatID}, &r)
	return r
}

func (b *Backend) ListInteractiveSessions(channelName, chatID string) []InteractiveSessionInfo {
	var r []InteractiveSessionInfo
	_ = b.call(MethodListInteractiveSessions, listInteractiveSessionsReq{ChannelName: channelName, ChatID: chatID}, &r)
	return r
}

func (b *Backend) InspectInteractiveSession(ctx context.Context, roleName, channelName, chatID, instance string, tailCount int) (string, error) {
	var r string
	return r, b.call(MethodInspectInteractiveSession, inspectInteractiveSessionReq{
		RoleName: roleName, ChannelName: channelName,
		ChatID: chatID, Instance: instance, TailCount: tailCount,
	}, &r)
}

func (b *Backend) GetSessionMessages(channelName, chatID, roleName, instance string) ([]SessionMessage, bool) {
	var r struct {
		Messages []SessionMessage `json:"messages"`
		OK       bool             `json:"ok"`
	}
	if err := b.call(MethodGetSessionMessages, getSessionMessagesReq{
		ChannelName: channelName, ChatID: chatID, RoleName: roleName, Instance: instance,
	}, &r); err != nil {
		return nil, false
	}
	return r.Messages, r.OK
}

func (b *Backend) GetAgentSessionDump(channelName, chatID, roleName, instance string) (*AgentSessionDump, bool) {
	var r struct {
		Dump *AgentSessionDump `json:"dump"`
		OK   bool              `json:"ok"`
	}
	if err := b.call(MethodGetAgentSessionDump, getAgentSessionDumpReq{
		ChannelName: channelName, ChatID: chatID, RoleName: roleName, Instance: instance,
	}, &r); err != nil {
		return nil, false
	}
	return r.Dump, r.OK
}

func (b *Backend) GetAgentSessionDumpByFullKey(fullKey string) (*AgentSessionDump, bool) {
	var r struct {
		Dump *AgentSessionDump `json:"dump"`
		OK   bool              `json:"ok"`
	}
	if err := b.call(MethodGetAgentSessionDumpByFullKey, getAgentSessionDumpByFullKeyReq{FullKey: fullKey}, &r); err != nil {
		return nil, false
	}
	return r.Dump, r.OK
}

// ── Processing state ──────────────────────────────────────────────────────

func (b *Backend) IsProcessing(ch, chatID string) bool {
	var r bool
	_ = b.call(MethodIsProcessing, isProcessingReq{Channel: ch, ChatID: chatID}, &r)
	return r
}

func (b *Backend) GetActiveProgress(ch, chatID string) *channel.CLIProgressPayload {
	var r *channel.CLIProgressPayload
	_ = b.call(MethodGetActiveProgress, getActiveProgressReq{Channel: ch, ChatID: chatID}, &r)
	return r
}

// ── Channel config ────────────────────────────────────────────────────────

func (b *Backend) GetChannelConfigs() (map[string]map[string]string, error) {
	var r map[string]map[string]string
	return r, b.call(MethodGetChannelConfig, struct{}{}, &r)
}

func (b *Backend) SetChannelConfig(channel string, values map[string]string) error {
	return b.call(MethodSetChannelConfig, setChannelConfigReq{Channel: channel, Values: values}, nil)
}

// ── Raw RPC ───────────────────────────────────────────────────────────────

func (b *Backend) CallRPC(method string, params any) (json.RawMessage, error) {
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return b.transport.Call(method, payload)
}

// Ensure Backend implements AgentBackend.
var _ AgentBackend = (*Backend)(nil)

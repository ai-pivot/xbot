package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	llm "xbot/llm"
	"xbot/protocol"

	"github.com/google/uuid"
	log "xbot/logger"
)

// Client is the unified client for both local and remote modes.
// All methods are RPC calls through Transport.
//
// In local mode: Transport = ChannelTransport, events arrive via eventCh.
// In remote mode: Transport = RemoteTransport, events arrive via WebSocket.
type Client struct {
	transport Transport
	eventCh   chan protocol.WSMessage // nil in remote mode

	// Event subscription (shared with baseTransport pattern)
	base baseTransport

	// Lifecycle management
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// NewClient creates a new Client.
//
// Parameters:
//   - transport: the RPC transport (ChannelTransport for local, RemoteTransport for remote)
//   - eventCh: channel for receiving server-pushed events (nil for remote mode)
func NewClient(transport Transport, eventCh chan protocol.WSMessage) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		transport: transport,
		eventCh:   eventCh,
		base:      newBaseTransport(),
		ctx:       ctx,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	// Start eventLoop if eventCh is available (local mode via ChannelTransport)
	// or if the transport provides one via EventCh().
	ch := eventCh
	if ch == nil {
		if evt, ok := transport.(interface {
			EventCh() chan protocol.WSMessage
		}); ok {
			ch = evt.EventCh()
		}
	}
	if ch != nil {
		c.eventCh = ch
		go c.eventLoop()
	}
	return c
}

// ---------------------------------------------------------------------------
// eventLoop — reads from eventCh and dispatches to subscribers
// ---------------------------------------------------------------------------

func (c *Client) eventLoop() {
	defer close(c.done)
	for {
		select {
		case wsMsg, ok := <-c.eventCh:
			if !ok {
				return
			}
			c.dispatchWSMessage(wsMsg)
		case <-c.ctx.Done():
			return
		}
	}
}

// dispatchWSMessage converts a WSMessage to the appropriate event type
// and dispatches to matching subscribers via baseTransport.
func (c *Client) dispatchWSMessage(msg protocol.WSMessage) {
	switch msg.Type {
	case protocol.MsgTypeProgress:
		if msg.Progress != nil {
			c.base.emit(c.ctx, msg.Progress)
		}
	case protocol.MsgTypeText:
		c.base.emit(c.ctx, protocol.OutboundEvent{
			ChatID:   msg.ChatID,
			Channel:  msg.Channel,
			Content:  msg.Content,
			Metadata: msg.Metadata,
		})
	case protocol.MsgTypeStreamContent:
		if msg.Progress != nil {
			c.base.emit(c.ctx, &protocol.ProgressEvent{
				ChatID:                 msg.Progress.ChatID,
				StreamContent:          msg.Progress.StreamContent,
				ReasoningStreamContent: msg.Progress.ReasoningStreamContent,
			})
		}
	case protocol.MsgTypeAskUser:
		var ev protocol.AskUserEvent
		if err := json.Unmarshal([]byte(msg.Content), &ev); err == nil {
			c.base.emit(c.ctx, ev)
		}
	case protocol.MsgTypeInjectUser:
		c.base.emit(c.ctx, protocol.InjectUserEvent{
			ChatID:  msg.ChatID,
			Content: msg.Content,
		})
	case protocol.MsgTypeSession:
		if msg.Session != nil {
			c.base.emit(c.ctx, *msg.Session)
		}
	case protocol.MsgTypeResyncRequired:
		c.base.emit(c.ctx, protocol.SessionEvent{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Action:  "resync_required",
		})
	case protocol.MsgTypePluginWidgets:
		var zones map[string]string
		if err := json.Unmarshal([]byte(msg.Content), &zones); err == nil {
			c.base.emit(c.ctx, protocol.PluginWidgetEvent{
				ChatID: msg.ChatID,
				Zones:  zones,
			})
		}
	case protocol.MsgTypeTUIControlReq:
		if msg.TUIControl != nil {
			c.base.emit(c.ctx, protocol.TUIControlEvent{
				Action: msg.TUIControl.Action,
				Params: msg.TUIControl.Params,
			})
		}
	default:
		log.WithFields(log.Fields{"type": msg.Type, "chat_id": msg.ChatID}).Warn("dispatchWSMessage: unknown message type, dropping")
	}
}

// ---------------------------------------------------------------------------
// Generic RPC helpers — mirrors Backend.call / callVoid
// ---------------------------------------------------------------------------

// call marshals req, calls transport, unmarshals into result.
func (c *Client) call(method string, req any, result any) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("%s: marshal: %w", method, err)
	}
	raw, err := c.transport.Call(method, payload)
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
func (c *Client) callVoid(method string, req any) {
	if err := c.call(method, req, nil); err != nil {
		log.WithError(err).WithField("method", method).Warn("Client: call failed")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start initializes the client. For remote mode, starts the transport.
// For local mode, the eventLoop is already started in NewClient.
func (c *Client) Start(ctx context.Context) error {
	// Share event base with transport so local events (conn_state, reconnect)
	// dispatched by the transport reach Client subscribers.
	if sharer, ok := c.transport.(interface{ ShareEventBase(*baseTransport) }); ok {
		sharer.ShareEventBase(&c.base)
	}
	// For remote mode, if transport implements AgentRunner, start it.
	if runner, ok := c.transport.(AgentRunner); ok {
		return runner.Start(ctx)
	}
	return nil
}

// Stop cancels the client context, stopping the eventLoop.
func (c *Client) Stop() {
	c.cancel()
	if runner, ok := c.transport.(AgentRunner); ok {
		runner.Stop()
	}
}

// Close releases transport resources.
func (c *Client) Close() error {
	return c.transport.Close()
}

// Run blocks until the context is done.
// For remote mode, delegates to the transport's Run method.
// For local mode, waits on the done channel or context cancellation.
func (c *Client) Run(ctx context.Context) error {
	if runner, ok := c.transport.(AgentRunner); ok {
		return runner.Run(ctx)
	}
	// Local mode: block until context done or client stopped.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return nil
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}

// ---------------------------------------------------------------------------
// Transport identity
// ---------------------------------------------------------------------------

func (c *Client) IsRemote() bool {
	if rt, ok := c.transport.(interface{ IsRemote() bool }); ok {
		return rt.IsRemote()
	}
	return false
}

func (c *Client) ConnState() string {
	if rt, ok := c.transport.(interface{ ConnState() string }); ok {
		return rt.ConnState()
	}
	return "connected" // local mode is always connected
}

func (c *Client) ServerURL() string {
	if rt, ok := c.transport.(interface{ ServerURL() string }); ok {
		return rt.ServerURL()
	}
	return "" // local mode has no server URL
}

// ---------------------------------------------------------------------------
// Communication — EventRouter
// ---------------------------------------------------------------------------

// SendInbound sends a user message to the agent.
// Both local and remote mode: routes through RPC.
// Local mode: send_inbound RPC handler writes to msgBus.Inbound.
// Remote mode: uses RemoteTransport.SendMessage if available.
//
// Parameters are flat fields to decouple callers from bus.InboundMessage.
// Time and RequestID are generated internally.
func (c *Client) SendInbound(ch, chatID, content, senderID, senderName, chatType string, metadata map[string]string) error {
	// Remote mode — use WebSocket SendMessage.
	if c.eventCh == nil {
		type messageSender interface {
			SendMessage(msg protocol.InboundMessage) error
		}
		if sender, ok := c.transport.(messageSender); ok {
			return sender.SendMessage(protocol.InboundMessage{
				MessagePayload: bus.MessagePayload{
					Content:    content,
					Channel:    ch,
					ChatID:     chatID,
					SenderID:   senderID,
					SenderName: senderName,
					ChatType:   chatType,
					Metadata:   metadata,
				},
			})
		}
		return fmt.Errorf("Client: no remote sender configured")
	}
	// Local mode — use RPC (send_inbound handler writes to msgBus.Inbound).
	return c.call(MethodSendInbound, struct {
		Channel    string            `json:"channel"`
		ChatID     string            `json:"chat_id"`
		Content    string            `json:"content"`
		SenderID   string            `json:"sender_id"`
		SenderName string            `json:"sender_name"`
		ChatType   string            `json:"chat_type"`
		RequestID  string            `json:"request_id"`
		Metadata   map[string]string `json:"metadata,omitempty"`
	}{
		Channel:    ch,
		ChatID:     chatID,
		Content:    content,
		SenderID:   senderID,
		SenderName: senderName,
		ChatType:   chatType,
		RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
		Metadata:   metadata,
	}, nil)
}

// Subscribe registers an event handler matching the given pattern.
func (c *Client) Subscribe(pattern protocol.EventPattern, handler protocol.EventHandler) (cancel func()) {
	return c.base.Subscribe(pattern, handler)
}

// BindChat registers this connection to receive events for the given chat.
// For remote mode, delegates to RemoteTransport.BindChat.
func (c *Client) BindChat(chatID string) error {
	type chatBinder interface {
		BindChat(chatID string) error
	}
	if binder, ok := c.transport.(chatBinder); ok {
		return binder.BindChat(chatID)
	}
	return nil // local mode: no-op
}

// ---------------------------------------------------------------------------
// Callback injection — forwarded to transport if it supports them
// ---------------------------------------------------------------------------

func (c *Client) SetTUIControlHandler(cb func(action string, params map[string]string) (map[string]string, error)) {
	type tuiSetter interface {
		SetTUIControlHandler(func(action string, params map[string]string) (map[string]string, error))
	}
	if setter, ok := c.transport.(tuiSetter); ok {
		setter.SetTUIControlHandler(cb)
	}
	// Local mode: no-op (handled by ServerCore)
}

func (c *Client) WireCallbacks(
	directSend func(msg channel.OutboundMsg) (string, error),
	channelFinder func(name string) (channel.Channel, bool),
	messageSender bus.MessageSender,
	registerAgentChannel func(name string, runFn bus.RunFn) error,
	unregisterAgentChannel func(name string),
) {
	type wireSetter interface {
		WireCallbacks(
			func(msg channel.OutboundMsg) (string, error),
			func(name string) (channel.Channel, bool),
			bus.MessageSender,
			func(name string, runFn bus.RunFn) error,
			func(name string),
		)
	}
	if setter, ok := c.transport.(wireSetter); ok {
		setter.WireCallbacks(directSend, channelFinder, messageSender, registerAgentChannel, unregisterAgentChannel)
	}
	// Local mode: no-op (handled by ServerCore)
}

// ---------------------------------------------------------------------------
// Settings (via RPC)
// ---------------------------------------------------------------------------

func (c *Client) GetSettings(namespace, senderID string) (map[string]string, error) {
	var r map[string]string
	return r, c.call(MethodGetSettings, getSettingsReq{Namespace: namespace, SenderID: senderID}, &r)
}

func (c *Client) SetSetting(namespace, senderID, key, value string) error {
	return c.call(MethodSetSetting, setSettingReq{Namespace: namespace, SenderID: senderID, Key: key, Value: value}, nil)
}

// ---------------------------------------------------------------------------
// Model / LLM (via RPC)
// ---------------------------------------------------------------------------

func (c *Client) GetDefaultModel() string {
	var r string
	_ = c.call(MethodGetDefaultModel, struct{}{}, &r)
	return r
}

func (c *Client) GetContextMode() string {
	var r string
	_ = c.call(MethodGetContextMode, struct{}{}, &r)
	return r
}

func (c *Client) ListModels() []string {
	var r []string
	_ = c.call(MethodListModels, struct{}{}, &r)
	return r
}

func (c *Client) ListAllModels() []string {
	var r []string
	_ = c.call(MethodListAllModels, struct{}{}, &r)
	return r
}

// ListAllModelEntries returns selectable models paired with their owning
// subscription (SubID/SubName empty for system-default models), for the model
// picker UI. Skips disabled subscriptions and disabled models.
func (c *Client) ListAllModelEntries() []protocol.ModelEntry {
	var r []protocol.ModelEntry
	_ = c.call(MethodListAllModelEntries, struct{}{}, &r)
	return r
}

// RefreshModelEntries live-fetches /models for every enabled subscription,
// persists to CachedModels, and returns the fresh entry list. Use before
// opening the model picker so it reflects providers' true available models.
func (c *Client) RefreshModelEntries() []protocol.ModelEntry {
	var r []protocol.ModelEntry
	_ = c.call(MethodRefreshModelEntries, struct{}{}, &r)
	return r
}

func (c *Client) SetDefaultThinkingMode(mode string) error {
	return c.call(MethodSetDefaultThinkingMode, setDefaultThinkingModeReq{Mode: mode}, nil)
}

func (c *Client) SetModelContexts(contexts map[string]int) error {
	return c.call(MethodSetModelContexts, contexts, nil)
}

func (c *Client) SetGlobalMaxTokens(maxTokens int) error {
	return c.call(MethodSetGlobalMaxTokens, setGlobalMaxTokensReq{MaxTokens: maxTokens}, nil)
}

func (c *Client) SetRetryConfig(cfg llm.RetryConfig) error {
	return c.call(MethodSetRetryConfig, cfg, nil)
}

func (c *Client) SetChatLLM(chatID string, provider string, llmCfg config.LLMConfig) error {
	return c.call(MethodSetChatLLM, setChatLLMReq{
		ChatID:   chatID,
		Provider: provider,
		Config:   llmCfg,
	}, nil)
}

func (c *Client) ClearProxyLLM(senderID string) {
	c.callVoid(MethodClearProxyLLM, clearProxyLLMReq{SenderID: senderID})
}

// ---------------------------------------------------------------------------
// Per-user settings (via RPC)
// ---------------------------------------------------------------------------

func (c *Client) GetUserMaxContext(senderID string) int {
	var r int
	_ = c.call(MethodGetUserMaxContext, getUserMaxContextReq{SenderID: senderID}, &r)
	return r
}

func (c *Client) SetUserMaxContext(senderID string, maxContext int) error {
	return c.call(MethodSetUserMaxContext, setUserMaxContextReq{SenderID: senderID, MaxContext: maxContext}, nil)
}

func (c *Client) GetUserMaxOutputTokens(senderID string) int {
	var r int
	_ = c.call(MethodGetUserMaxOutputTokens, getUserMaxOutputTokensReq{SenderID: senderID}, &r)
	return r
}

func (c *Client) SetUserMaxOutputTokens(senderID string, maxTokens int) error {
	return c.call(MethodSetUserMaxOutputTokens, setUserMaxOutputTokensReq{SenderID: senderID, MaxTokens: maxTokens}, nil)
}

func (c *Client) GetUserThinkingMode(senderID string) string {
	var r string
	_ = c.call(MethodGetUserThinkingMode, getUserThinkingModeReq{SenderID: senderID}, &r)
	return r
}

func (c *Client) SetUserThinkingMode(senderID string, mode string) error {
	return c.call(MethodSetUserThinkingMode, setUserThinkingModeReq{SenderID: senderID, Mode: mode}, nil)
}

func (c *Client) SetUserModel(senderID, subID, model string) error {
	return c.call(MethodSetUserModel, setUserModelReq{SenderID: senderID, SubID: subID, Model: model}, nil)
}

// SelectModel sets the per-session (subscription, model) for a chat.
// Persists to the tenants table via the select_model RPC.
func (c *Client) SelectModel(senderID, channelName, subID, model, chatID string) error {
	return c.call(MethodSelectModel, struct {
		SenderID string `json:"sender_id"`
		Channel  string `json:"channel"`
		SubID    string `json:"sub_id"`
		Model    string `json:"model"`
		ChatID   string `json:"chat_id,omitempty"`
	}{SenderID: senderID, Channel: channelName, SubID: subID, Model: model, ChatID: chatID}, nil)
}

// SetDefaultModel sets the user-level default (subscription, model) for new sessions.
func (c *Client) SetDefaultModel(senderID, subID, model string) error {
	return c.call(MethodSetDefaultModel, struct {
		SenderID string `json:"sender_id"`
		SubID    string `json:"sub_id"`
		Model    string `json:"model"`
	}{SenderID: senderID, SubID: subID, Model: model}, nil)
}

// SetModelEnabled toggles a model's enabled flag (model disable feature).
func (c *Client) SetModelEnabled(subID, model string, enabled bool) error {
	return c.call(MethodSetModelEnabled, struct {
		SubID   string `json:"sub_id"`
		Model   string `json:"model"`
		Enabled bool   `json:"enabled"`
	}{SubID: subID, Model: model, Enabled: enabled}, nil)
}

// RemoveModel permanently deletes a model from subscription_models.
func (c *Client) RemoveModel(subID, model string) error {
	return c.call(MethodRemoveModel, struct {
		SubID string `json:"sub_id"`
		Model string `json:"model"`
	}{SubID: subID, Model: model}, nil)
}

// UpsertModel inserts or updates a model in subscription_models.
func (c *Client) UpsertModel(subID, model string, maxContext, maxOutput int, apiType, thinkingMode string) error {
	return c.call(MethodUpsertModel, struct {
		SubID        string `json:"sub_id"`
		Model        string `json:"model"`
		MaxContext   int    `json:"max_context"`
		MaxOutput    int    `json:"max_output"`
		APIType      string `json:"api_type"`
		ThinkingMode string `json:"thinking_mode"`
	}{SubID: subID, Model: model, MaxContext: maxContext, MaxOutput: maxOutput, APIType: apiType, ThinkingMode: thinkingMode}, nil)
}

// SetSubscriptionEnabled toggles a subscription's enabled flag (v40). A disabled
// subscription stops contributing models to the picker.
func (c *Client) SetSubscriptionEnabled(subID string, enabled bool) error {
	return c.call(MethodSetSubscriptionEnabled, struct {
		SubID   string `json:"sub_id"`
		Enabled bool   `json:"enabled"`
	}{SubID: subID, Enabled: enabled}, nil)
}

// ---------------------------------------------------------------------------
// Runtime config (via RPC)
// ---------------------------------------------------------------------------

func (c *Client) SetMaxIterations(n int) {
	c.callVoid(MethodSetMaxIterations, setMaxIterationsReq{N: n})
}

func (c *Client) SetMaxConcurrency(n int) {
	c.callVoid(MethodSetMaxConcurrency, setMaxConcurrencyReq{N: n})
}

func (c *Client) SetCompressionThreshold(f float64) {
	c.callVoid(MethodSetCompressionThreshold, setCompressionThresholdReq{Threshold: f})
}

// ApplyRuntimeSettings applies a batch of setting changes via RPC.
// The server-side handler calls agent.ApplyRuntimeSettings + saveServerConfig.
func (c *Client) ApplyRuntimeSettings(values map[string]string) {
	c.callVoid(MethodApplyRuntimeSettings, applyRuntimeSettingsReq{Values: values})
}

func (c *Client) SetContextMode(mode string) error {
	return c.call(MethodSetContextMode, setContextModeReq{Mode: mode}, nil)
}

func (c *Client) SetCWD(ch, chatID, dir string) error {
	return c.call(MethodSetCWD, setCWDReq{Channel: ch, ChatID: chatID, Dir: dir}, nil)
}

func (c *Client) ResetTokenState() {
	c.callVoid(MethodResetTokenState, struct{}{})
}

// ---------------------------------------------------------------------------
// Token usage (via RPC)
// ---------------------------------------------------------------------------

func (c *Client) GetUserTokenUsage(senderID string) (map[string]any, error) {
	var r map[string]any
	return r, c.call(MethodGetUserTokenUsage, getUserTokenUsageReq{SenderID: senderID}, &r)
}

func (c *Client) GetDailyTokenUsage(senderID string, days int) ([]map[string]any, error) {
	var r []map[string]any
	return r, c.call(MethodGetDailyTokenUsage, getDailyTokenUsageReq{SenderID: senderID, Days: days}, &r)
}

func (c *Client) GetTokenState(ch, chatID string) (int64, int64, error) {
	var r struct {
		Prompt     int64 `json:"prompt_tokens"`
		Completion int64 `json:"completion_tokens"`
	}
	if err := c.call(MethodGetTokenState, getTokenStateReq{Channel: ch, ChatID: chatID}, &r); err != nil {
		return 0, 0, err
	}
	return r.Prompt, r.Completion, nil
}

func (c *Client) GetContextUsage(ch, chatID string) (protocol.ContextUsage, error) {
	var usage protocol.ContextUsage
	err := c.call(MethodGetContextUsage, getContextUsageReq{Channel: ch, ChatID: chatID}, &usage)
	return usage, err
}

// ---------------------------------------------------------------------------
// Background tasks (via RPC)
// ---------------------------------------------------------------------------

func (c *Client) GetBgTaskCount(sessionKey string) int {
	var r int
	_ = c.call(MethodGetBgTaskCount, getBgTaskCountReq{SessionKey: sessionKey}, &r)
	return r
}

func (c *Client) ListBgTasks(sessionKey string) ([]BgTaskJSON, error) {
	var r []BgTaskJSON
	return r, c.call(MethodListBgTasks, listBgTasksReq{SessionKey: sessionKey}, &r)
}

func (c *Client) KillBgTask(taskID string) error {
	return c.call(MethodKillBgTask, killBgTaskReq{TaskID: taskID}, nil)
}

func (c *Client) CleanupCompletedBgTasks(sessionKey string) {
	c.callVoid(MethodCleanupCompletedBgTasks, cleanupCompletedBgTasksReq{SessionKey: sessionKey})
}

// ---------------------------------------------------------------------------
// Tenants (via RPC)
// ---------------------------------------------------------------------------

func (c *Client) ListTenants() ([]TenantInfo, error) {
	var r []TenantInfo
	return r, c.call(MethodListTenants, struct{}{}, &r)
}

// ---------------------------------------------------------------------------
// Subscriptions (via RPC)
// ---------------------------------------------------------------------------

func (c *Client) ListSubscriptions(senderID string) ([]protocol.Subscription, error) {
	var r []protocol.Subscription
	return r, c.call(MethodListSubscriptions, listSubscriptionsReq{SenderID: senderID}, &r)
}

func (c *Client) GetDefaultSubscription(senderID string) (*protocol.Subscription, error) {
	var r *protocol.Subscription
	return r, c.call(MethodGetDefaultSubscription, getDefaultSubscriptionReq{SenderID: senderID}, &r)
}

func (c *Client) AddSubscription(senderID string, sub protocol.Subscription) error {
	return c.call(MethodAddSubscription, addSubscriptionReq{
		SenderID: senderID,
		Sub: channelSubscriptionJSON{
			ID: sub.ID, Name: sub.Name, Provider: sub.Provider,
			BaseURL: sub.BaseURL, APIKey: sub.APIKey,
			Model: sub.Model, Active: sub.Active,
			MaxOutputTokens: sub.MaxOutputTokens, ThinkingMode: sub.ThinkingMode,
			PerModelConfigs: sub.PerModelConfigs,
		},
	}, nil)
}

func (c *Client) RemoveSubscription(id string) error {
	return c.call(MethodRemoveSubscription, removeSubscriptionReq{ID: id}, nil)
}

func (c *Client) SetDefaultSubscription(id string, chatID string) error {
	return c.call(MethodSetDefaultSubscription, setDefaultSubscriptionReq{ID: id, ChatID: chatID}, nil)
}

// GetSessionSubscription returns the session→subscription mapping from the backend.
// Returns (subscriptionID, model). Empty strings if no mapping exists.
func (c *Client) GetSessionSubscription(senderID, channelName, chatID string) (subscriptionID, model string, err error) {
	var result map[string]string
	if err := c.call(MethodGetSessionSubscription, map[string]string{"channel": channelName, "chat_id": chatID}, &result); err != nil {
		return "", "", err
	}
	return result["subscription_id"], result["model"], nil
}

func (c *Client) UpdateSubscription(id string, sub protocol.Subscription) error {
	return c.call(MethodUpdateSubscription, updateSubscriptionReq{
		ID: id,
		Sub: channelSubscriptionJSON{
			ID: sub.ID, Name: sub.Name, Provider: sub.Provider,
			BaseURL: sub.BaseURL, APIKey: sub.APIKey,
			Model: sub.Model, Active: sub.Active,
			MaxOutputTokens: sub.MaxOutputTokens, ThinkingMode: sub.ThinkingMode,
			PerModelConfigs: sub.PerModelConfigs,
		},
	}, nil)
}

func (c *Client) UpdatePerModelConfig(id, model string, pmc protocol.PerModelConfig) error {
	return c.call(MethodUpdatePerModelConfig, updatePerModelConfigReq{
		ID: id, Model: model, Config: pmc,
	}, nil)
}

func (c *Client) SetSubscriptionModel(id, model string) error {
	return c.call(MethodSetSubscriptionModel, setSubscriptionModelReq{ID: id, Model: model}, nil)
}

func (c *Client) RenameSubscription(id, name string) error {
	return c.call(MethodRenameSubscription, renameSubscriptionReq{ID: id, Name: name}, nil)
}

// ---------------------------------------------------------------------------
// Memory / History (via RPC)
// ---------------------------------------------------------------------------

func (c *Client) ClearMemory(ctx context.Context, channelName, chatID, targetType, senderID string) error {
	return c.call(MethodClearMemory, clearMemoryReq{
		Channel: channelName, ChatID: chatID, TargetType: targetType, SenderID: senderID,
	}, nil)
}

func (c *Client) GetMemoryStats(ctx context.Context, ch, chatID, senderID string) map[string]string {
	var r map[string]string
	_ = c.call(MethodGetMemoryStats, getMemoryStatsReq{Channel: ch, ChatID: chatID, SenderID: senderID}, &r)
	return r
}

func (c *Client) GetHistory(channelName, chatID string) ([]protocol.HistoryMessage, error) {
	var r []protocol.HistoryMessage
	return r, c.call(MethodGetHistory, getHistoryReq{Channel: channelName, ChatID: chatID}, &r)
}

func (c *Client) RewindHistory(ch, chatID string, historyID int64) (protocol.HistoryRewindResult, error) {
	var result protocol.HistoryRewindResult
	err := c.call(MethodRewindHistory, rewindHistoryReq{Channel: ch, ChatID: chatID, HistoryID: historyID}, &result)
	return result, err
}

// ---------------------------------------------------------------------------
// Interactive SubAgent sessions (via RPC)
// ---------------------------------------------------------------------------

func (c *Client) CountInteractiveSessions(channelName, chatID string) int {
	var r int
	_ = c.call(MethodCountInteractiveSessions, countInteractiveSessionsReq{ChannelName: channelName, ChatID: chatID}, &r)
	return r
}

func (c *Client) ListInteractiveSessions(channelName, chatID string) []InteractiveSessionInfo {
	var r []InteractiveSessionInfo
	_ = c.call(MethodListInteractiveSessions, listInteractiveSessionsReq{ChannelName: channelName, ChatID: chatID}, &r)
	return r
}

func (c *Client) InspectInteractiveSession(ctx context.Context, roleName, channelName, chatID, instance string, tailCount int) (string, error) {
	var r string
	return r, c.call(MethodInspectInteractiveSession, inspectInteractiveSessionReq{
		RoleName: roleName, ChannelName: channelName,
		ChatID: chatID, Instance: instance, TailCount: tailCount,
	}, &r)
}

func (c *Client) GetSessionMessages(channelName, chatID, roleName, instance string) ([]SessionMessage, bool) {
	var msgs []SessionMessage
	if err := c.call(MethodGetSessionMessages, getSessionMessagesReq{
		ChannelName: channelName, ChatID: chatID, RoleName: roleName, Instance: instance,
	}, &msgs); err != nil {
		return nil, false
	}
	return msgs, len(msgs) > 0
}

func (c *Client) GetAgentSessionDump(channelName, chatID, roleName, instance string) (*AgentSessionDump, bool) {
	var dump AgentSessionDump
	if err := c.call(MethodGetAgentSessionDump, getAgentSessionDumpReq{
		ChannelName: channelName, ChatID: chatID, RoleName: roleName, Instance: instance,
	}, &dump); err != nil {
		return nil, false
	}
	return &dump, len(dump.Messages) > 0
}

func (c *Client) GetAgentSessionDumpByFullKey(fullKey string) (*AgentSessionDump, bool) {
	var dump AgentSessionDump
	if err := c.call(MethodGetAgentSessionDumpByFullKey, getAgentSessionDumpByFullKeyReq{FullKey: fullKey}, &dump); err != nil {
		return nil, false
	}
	return &dump, len(dump.Messages) > 0
}

// ContinueInteractiveSession submits content to the active interactive SubAgent
// identified by its canonical full key. A nil error means accepted; completion
// is reported later through progress events.
func (c *Client) ContinueInteractiveSession(fullKey, content string) error {
	return c.call(MethodContinueInteractiveSession, continueInteractiveSessionReq{
		FullKey: fullKey,
		Content: content,
	}, nil)
}

// ---------------------------------------------------------------------------
// Processing state (via RPC)
// ---------------------------------------------------------------------------

func (c *Client) IsProcessing(ch, chatID string) bool {
	var r bool
	_ = c.call(MethodIsProcessing, isProcessingReq{Channel: ch, ChatID: chatID}, &r)
	return r
}

func (c *Client) GetActiveProgress(ch, chatID string, fetch protocol.ProgressFetch) *protocol.ProgressEvent {
	var r *protocol.ProgressEvent
	_ = c.call(MethodGetActiveProgress, getActiveProgressReq{Channel: ch, ChatID: chatID, FromIteration: fetch.ToFromIter()}, &r)
	return r
}

func (c *Client) GetPendingAskUser(ch, chatID string) *protocol.ProgressEvent {
	var r *protocol.ProgressEvent
	_ = c.call(MethodGetPendingAskUser, getPendingAskUserReq{Channel: ch, ChatID: chatID}, &r)
	return r
}

func (c *Client) GetTodos(ch, chatID string) []protocol.TodoItem {
	var r []protocol.TodoItem
	_ = c.call(MethodGetTodos, getTodosReq{Channel: ch, ChatID: chatID}, &r)
	return r
}

// ---------------------------------------------------------------------------
// Channel config (via RPC)
// ---------------------------------------------------------------------------

func (c *Client) GetChannelConfigs() (map[string]map[string]string, error) {
	var r map[string]map[string]string
	return r, c.call(MethodGetChannelConfig, struct{}{}, &r)
}

func (c *Client) SetChannelConfig(channel string, values map[string]string) error {
	return c.call(MethodSetChannelConfig, setChannelConfigReq{Channel: channel, Values: values}, nil)
}

// ---------------------------------------------------------------------------
// Raw RPC
// ---------------------------------------------------------------------------

func (c *Client) CallRPC(method string, params any) (json.RawMessage, error) {
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return c.transport.Call(method, payload)
}

// ListCommandNames returns visible commands registered by the backend agent.
func (c *Client) ListCommandNames() ([]string, error) {
	var names []string
	if err := c.call("list_command_names", nil, &names); err != nil {
		return nil, err
	}
	return names, nil
}

// ---------------------------------------------------------------------------
// Web Users (via RPC)
// ---------------------------------------------------------------------------

func (c *Client) CreateWebUser(username string) (string, error) {
	var resp struct {
		Password string `json:"password"`
	}
	err := c.call("create_web_user", map[string]string{"username": username}, &resp)
	return resp.Password, err
}

func (c *Client) ListWebUsers() ([]map[string]any, error) {
	var result []map[string]any
	raw, err := c.CallRPC("list_web_users", nil)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) DeleteWebUser(username string) error {
	_, err := c.CallRPC("delete_web_user", map[string]string{"username": username})
	return err
}

// ---------------------------------------------------------------------------
// Chat Management (via RPC)
// ---------------------------------------------------------------------------

func (c *Client) DeleteChat(ch, senderID, chatID string) error {
	_, err := c.CallRPC("delete_chat", map[string]string{
		"channel":  ch,
		"senderid": senderID,
		"chat_id":  chatID,
	})
	return err
}

// ---------------------------------------------------------------------------
// Runner CRUD (via RPC)
// ---------------------------------------------------------------------------

func (c *Client) RunnerCreate(name, mode, dockerImage, workspace, llmProvider, llmAPIKey, llmModel, llmBaseURL string) (map[string]string, error) {
	var r map[string]string
	err := c.call(MethodRunnerCreate, map[string]string{
		"name":         name,
		"mode":         mode,
		"docker_image": dockerImage,
		"workspace":    workspace,
		"llm_provider": llmProvider,
		"llm_api_key":  llmAPIKey,
		"llm_model":    llmModel,
		"llm_base_url": llmBaseURL,
	}, &r)
	return r, err
}

func (c *Client) RunnerList() ([]map[string]any, error) {
	var r []map[string]any
	raw, err := c.CallRPC(MethodRunnerList, struct{}{})
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return r, nil
}

func (c *Client) RunnerDelete(name string) error {
	_, err := c.CallRPC(MethodRunnerDelete, map[string]string{"name": name})
	return err
}

func (c *Client) RunnerGetActive() (string, error) {
	raw, err := c.CallRPC(MethodRunnerGetActive, struct{}{})
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}

func (c *Client) RunnerSetActive(name string) error {
	_, err := c.CallRPC(MethodRunnerSetActive, map[string]string{"name": name})
	return err
}

func (c *Client) RunnerRename(oldName, newName string) error {
	_, err := c.CallRPC(MethodRunnerRename, map[string]string{"old_name": oldName, "new_name": newName})
	return err
}

func (c *Client) RenameChat(ch, senderID, chatID, newName string) error {
	_, err := c.CallRPC("rename_chat", map[string]string{
		"channel":  ch,
		"senderid": senderID,
		"chat_id":  chatID,
		"new_name": newName,
	})
	return err
}

// ---------------------------------------------------------------------------
// App (via RPC)
// ---------------------------------------------------------------------------

// AppPack packs local items into a .xbot.zip file and returns the output path.
func (c *Client) AppPack(name string, items []AppItem, author string) (string, error) {
	specs := make([]appItemSpec, len(items))
	for i, it := range items {
		specs[i] = appItemSpec(it)
	}
	var r appPackResp
	if err := c.call(MethodAppPack, appPackReq{Name: name, Items: specs, Author: author}, &r); err != nil {
		return "", err
	}
	return r.Path, nil
}

// AppInstallFile installs a .xbot.zip from a local file path.
func (c *Client) AppInstallFile(zipPath, senderID string, force bool) (*AppInstallResult, error) {
	var r appInstallResp
	if err := c.call(MethodAppInstallFile, appInstallFileReq{ZipPath: zipPath, SenderID: senderID, Force: force}, &r); err != nil {
		return nil, err
	}
	return &AppInstallResult{
		Manifest:  AppManifest{Name: r.Name, Version: r.Version},
		Installed: r.Installed,
		Skipped:   r.Skipped,
	}, nil
}

// AppInstallURL downloads a .xbot.zip from a URL and installs it.
func (c *Client) AppInstallURL(url, senderID string, force bool) (*AppInstallResult, error) {
	var r appInstallResp
	if err := c.call(MethodAppInstallURL, appInstallURLReq{URL: url, SenderID: senderID, Force: force}, &r); err != nil {
		return nil, err
	}
	return &AppInstallResult{
		Manifest:  AppManifest{Name: r.Name, Version: r.Version},
		Installed: r.Installed,
		Skipped:   r.Skipped,
	}, nil
}

// AppUninstall removes an installed skill/agent/plugin/app.
func (c *Client) AppUninstall(entryType, name, senderID string) error {
	return c.call(MethodAppUninstall, appUninstallReq{Type: entryType, Name: name, SenderID: senderID}, nil)
}

// AppList returns lists of installed skills, agents, and plugins.
func (c *Client) AppList(senderID string) (skills, agents, plugins []string, err error) {
	var r appListResp
	if err := c.call(MethodAppList, appListReq{SenderID: senderID}, &r); err != nil {
		return nil, nil, nil, err
	}
	return r.Skills, r.Agents, r.Plugins, nil
}

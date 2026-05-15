// Backend implementation — hand-written methods only.
// Simple RPC methods are auto-generated in rpc_backend_gen.go.
// To regenerate: go run ./cmd/rpcgen/

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"xbot/bus"
	"xbot/channel"
	"xbot/protocol"
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
	Label        string `json:"label,omitempty"`
	CreatedAt    string `json:"created_at"`
	LastActiveAt string `json:"last_active_at"`
}

// Backend is the unified RPC client.
// Every method goes through transport.Call() — zero local state.
// Server-side code (serverapp) gets a separate *Agent for direct access.
type Backend struct {
	transport Transport
}

// NewBackend creates a local-mode Backend with an in-process Agent.
// Returns (Backend for RPC, Agent for direct server-side access).
func NewBackend(cfg Config) (*Backend, *Agent, error) {
	a, err := New(cfg)
	if err != nil {
		return nil, nil, err
	}
	lt := newLocalTransport(a, cfg.Bus)
	return &Backend{transport: lt}, a, nil
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
	return b.transport.SendMessage(protocol.InboundMessage{
		MessagePayload: bus.MessagePayload{
			Content:    msg.Content,
			Channel:    msg.Channel,
			ChatID:     msg.ChatID,
			SenderID:   msg.SenderID,
			SenderName: msg.SenderName,
			ChatType:   msg.ChatType,
		},
	})
}

// ---------------------------------------------------------------------------
// Callback setters — pure transport delegation
// ---------------------------------------------------------------------------

func (b *Backend) Subscribe(pattern protocol.EventPattern, handler protocol.EventHandler) (cancel func()) {
	return b.transport.Subscribe(pattern, handler)
}

func (b *Backend) SetTUIControlHandler(cb func(action string, params map[string]string) (map[string]string, error)) {
	b.transport.SetTUIControlHandler(cb)
}

func (b *Backend) WireCallbacks(
	directSend func(msg bus.OutboundMessage) (string, error),
	channelFinder func(name string) (channel.Channel, bool),
	sessionStateHandler func(ev protocol.SessionEvent),
	messageSender bus.MessageSender,
	registerAgentChannel func(name string, runFn bus.RunFn) error,
	unregisterAgentChannel func(name string),
) {
	b.transport.WireCallbacks(directSend, channelFinder, sessionStateHandler, messageSender, registerAgentChannel, unregisterAgentChannel)
}

func (b *Backend) SetChatRenameFn(fn func(chatID, newName string) (oldName string, err error)) {
	b.transport.SetChatRenameFn(fn)
}

func (b *Backend) BindChat(chatID string) error { return b.transport.BindChat(chatID) }
func (b *Backend) ConnState() string            { return b.transport.ConnState() }
func (b *Backend) ServerURL() string            { return b.transport.ServerURL() }
func (b *Backend) IsRemote() bool               { return b.transport.IsRemote() }

// RegisterCoreTool/RegisterTool/IndexGlobalTools delegate to localTransport
// for server-side use. No-op for remote transports.
func (b *Backend) RegisterCoreTool(tool tools.Tool) {
	if lt, ok := b.transport.(*localTransport); ok {
		lt.agent.RegisterCoreTool(tool)
	}
}
func (b *Backend) RegisterTool(tool tools.Tool) {
	if lt, ok := b.transport.(*localTransport); ok {
		lt.agent.RegisterTool(tool)
	}
}
func (b *Backend) IndexGlobalTools() {
	if lt, ok := b.transport.(*localTransport); ok {
		lt.agent.IndexGlobalTools()
	}
}

// SetSandbox delegates to localTransport for server-side use.
func (b *Backend) SetSandbox(sb tools.Sandbox, mode string) {
	if lt, ok := b.transport.(*localTransport); ok {
		lt.agent.SetSandbox(sb, mode)
	}
}

// SetChannelReconfigureFn delegates to localTransport for server-side use.
func (b *Backend) SetChannelReconfigureFn(fn func(channel string)) {
	if lt, ok := b.transport.(*localTransport); ok {
		lt.reconfigureFn = fn
	}
}

// ---------------------------------------------------------------------------
// RPC methods — most are a single b.call() / b.callVoid().
// (CallRPC is the exception, using transport.Call() directly).
// ---------------------------------------------------------------------------

// ── Settings ──────────────────────────────────────────────────────────────

// ── Model / LLM ───────────────────────────────────────────────────────────

// ── Per-user settings ─────────────────────────────────────────────────────

// ── Runtime config ────────────────────────────────────────────────────────

func (b *Backend) SetMaxContextTokens(n int, chatID ...string) {
	chatIDVal := ""
	if len(chatID) > 0 {
		chatIDVal = chatID[0]
	}
	b.callVoid(MethodSetMaxContextTokens, struct {
		MaxContext int    `json:"max_context"`
		ChatID     string `json:"chat_id,omitempty"`
	}{MaxContext: n, ChatID: chatIDVal})
}

// GetEffectiveMaxContext returns the effective max context for a user/session.
// Via RPC — works for both local and remote modes.

// ClearPerChatMaxContext clears the per-session max_context override.
// Via RPC — works for both local and remote modes.

// ── Token usage ───────────────────────────────────────────────────────────

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

// ── Tenants ───────────────────────────────────────────────────────────────

// ── Subscriptions ─────────────────────────────────────────────────────────

func (b *Backend) AddSubscription(senderID string, sub protocol.Subscription) error {
	return b.call(MethodAddSubscription, addSubscriptionReq{
		SenderID: senderID,
		Sub: channelSubscriptionJSON{
			ID: sub.ID, Name: sub.Name, Provider: sub.Provider,
			BaseURL: sub.BaseURL, APIKey: sub.APIKey,
			Model: sub.Model, Active: sub.Active,
			MaxOutputTokens: sub.MaxOutputTokens, ThinkingMode: sub.ThinkingMode,
			// PerModelConfigs is now protocol.PerModelConfig alias — use directly.
			PerModelConfigs: sub.PerModelConfigs,
		},
	}, nil)
}

func (b *Backend) UpdateSubscription(id string, sub protocol.Subscription) error {
	return b.call(MethodUpdateSubscription, updateSubscriptionReq{
		ID: id,
		Sub: channelSubscriptionJSON{
			ID: sub.ID, Name: sub.Name, Provider: sub.Provider,
			BaseURL: sub.BaseURL, APIKey: sub.APIKey,
			Model: sub.Model, Active: sub.Active,
			MaxOutputTokens: sub.MaxOutputTokens, ThinkingMode: sub.ThinkingMode,
			// PerModelConfigs is now protocol.PerModelConfig alias — use directly.
			PerModelConfigs: sub.PerModelConfigs,
		},
	}, nil)
}

// ── Memory / session / history ────────────────────────────────────────────

// ── Interactive sessions ──────────────────────────────────────────────────

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

// ── Channel config ────────────────────────────────────────────────────────

// ── Raw RPC ───────────────────────────────────────────────────────────────

func (b *Backend) CallRPC(method string, params any) (json.RawMessage, error) {
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return b.transport.Call(method, payload)
}

// --- Web Users ---

func (b *Backend) CreateWebUser(username string) (string, error) {
	var resp struct {
		Password string `json:"password"`
	}
	err := b.call("create_web_user", map[string]string{"username": username}, &resp)
	return resp.Password, err
}

func (b *Backend) ListWebUsers() ([]map[string]any, error) {
	var result []map[string]any
	raw, err := b.CallRPC("list_web_users", nil)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (b *Backend) DeleteWebUser(username string) error {
	_, err := b.CallRPC("delete_web_user", map[string]string{"username": username})
	return err
}

// --- Chat Management ---

func (b *Backend) DeleteChat(ch, senderID, chatID string) error {
	_, err := b.CallRPC("delete_chat", map[string]string{
		"channel":  ch,
		"senderid": senderID,
		"chat_id":  chatID,
	})
	return err
}

func (b *Backend) RenameChat(ch, senderID, chatID, newName string) error {
	_, err := b.CallRPC("rename_chat", map[string]string{
		"channel":  ch,
		"senderid": senderID,
		"chat_id":  chatID,
		"new_name": newName,
	})
	return err
}

// Ensure Backend implements AgentBackend.
var _ AgentBackend = (*Backend)(nil)

package agent

import (
	"context"

	"xbot/llm"
	log "xbot/logger"
	"xbot/protocol"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// userSystem holds all user-related service instances. It is the ONLY
// container for LLMFactory / SettingsService / IdentityResolver — these
// are never accessed as direct Agent fields. Request-path code reads
// from UserContext (resolved by ResolveUserContext); infrastructure
// code accesses a.userSys directly.
type userSystem struct {
	llmFactory       *LLMFactory
	settingsSvc      *SettingsService
	identityResolver *IdentityResolver
}

// userContextKey is the context key for UserContext.
type userContextKey struct{}

// WithUserContext stores a UserContext in context.Context.
// Called once at processMessage entry — everything downstream reads from ctx.
func WithUserContext(ctx context.Context, uc *UserContext) context.Context {
	return context.WithValue(ctx, userContextKey{}, uc)
}

// UserContextFromContext extracts the UserContext from context.
// Returns nil if not set (e.g. cron path without processMessage).
func UserContextFromContext(ctx context.Context) *UserContext {
	uc, _ := ctx.Value(userContextKey{}).(*UserContext)
	return uc
}

// UserContext holds all user-related resolved components.
//
// Populated once by Agent.ResolveUserContext at the request entry point and
// carried via context.Context. ALL code after the middleware — agent loop,
// command handlers, config tool — reads from this struct and never touches
// LLMFactory / IdentityResolver / SettingsService directly.
//
// To support single-user mode, only ResolveUserContext needs to change.
type UserContext struct {
	// === Identity ===
	UserID       int64
	Role         string
	SenderID     string
	SenderName   string
	FeishuUserID string

	// === LLM (resolved) ===
	LLMClient        llm.LLM
	Model            string
	ThinkingMode     string
	MaxContextTokens int
	MaxOutputTokens  int
	SubID            string

	// === Settings (resolved read-only snapshot) ===
	Settings  map[string]string
	PermUsers *PermUsersConfig

	// === Sandbox ===
	Sandbox     tools.Sandbox
	SandboxMode string

	// === LLM concurrency ===
	LLMSemAcquire func(context.Context) func()
	SubAgentSem   func(context.Context) func()

	// === Management operations ===
	// These are used by command handlers (/set-llm, /settings, etc.) and the
	// config tool to read/write user-system state. They are set by
	// ResolveUserContext. In single-user mode, they would be swapped for
	// alternative implementations (e.g. file-based).

	// SubSvc provides LLM subscription CRUD operations.
	SubSvc *sqlite.LLMSubscriptionService

	// SettingsSvc provides user settings read/write (for config tool callbacks
	// and /settings command — these need live DB access, not the snapshot).
	SettingsSvc *SettingsService

	// LLM management closures (capture senderID)
	HasCustomLLM     func() bool
	InvalidateLLM    func()
	InvalidateSender func()
	ResolveLLM       func(chatID string) (llm.LLM, string, int, string, int)
	ResolveActiveSub func(chatID string) (*sqlite.LLMSubscription, string, error)
	SelectModel      func(chatID, subID, model string) error
	RefreshModels    func() ([]protocol.ModelEntry, []RefreshResult)

	// factory is the internal bridge to LLMFactory for SubAgent model-specific
	// resolution. Callers use ResolveLLMForModel, never access this directly.
	factory *llmFactoryRef
}

// GetSetting returns a setting value.
func (uc *UserContext) GetSetting(key string) string {
	if uc == nil || uc.Settings == nil {
		return ""
	}
	return uc.Settings[key]
}

// GetSettingBool returns a setting as boolean (default false).
func (uc *UserContext) GetSettingBool(key string) bool {
	return uc.GetSetting(key) == "true"
}

// llmFactoryRef bridges UserContext to LLMFactory for model-specific resolution.
type llmFactoryRef struct {
	getLLMForModel            func(senderID, model string) (llm.LLM, string, int, string, int, bool)
	getLLM                    func(senderID string) (llm.LLM, string, int, string, int)
	resolveSubIDForModel      func(senderID, model string) string
	llmSemAcquireForUser      func(senderID, channel string) func(context.Context) func()
	subAgentSemAcquireForUser func(senderID, channel string) func(context.Context) func()
}

// ResolveLLMForModel resolves an LLM client for a specific model (SubAgent path).
func (uc *UserContext) ResolveLLMForModel(model string) (client llm.LLM, resolvedModel string, maxCtx int, thinkingMode string, maxOut int, subID string) {
	if model == "" || model == uc.Model {
		return uc.LLMClient, uc.Model, uc.MaxContextTokens, uc.ThinkingMode, uc.MaxOutputTokens, uc.SubID
	}
	if uc.factory == nil {
		log.Usr(nil, log.CatAuth, uc.SenderID).WithFields(log.Fields{"model": model, "sender": uc.SenderID}).Warn("UserContext.factory is nil, falling back to main model for SubAgent")
		return uc.LLMClient, uc.Model, uc.MaxContextTokens, uc.ThinkingMode, uc.MaxOutputTokens, uc.SubID
	}
	var ok bool
	client, resolvedModel, maxCtx, thinkingMode, maxOut, ok = uc.factory.getLLMForModel(uc.SenderID, model)
	if !ok {
		log.Usr(nil, log.CatAuth, uc.SenderID).WithFields(log.Fields{"model": model, "sender": uc.SenderID}).Warn("model not found for SubAgent, falling back to main model")
	}
	subID = uc.factory.resolveSubIDForModel(uc.SenderID, resolvedModel)
	return client, resolvedModel, maxCtx, thinkingMode, maxOut, subID
}

// ResolveLLMForModelWithFallback resolves LLM for a model, falling back to
// the inherited LLM when the factory cannot resolve the requested model.
func (uc *UserContext) ResolveLLMForModelWithFallback(model string) (client llm.LLM, resolvedModel string, maxCtx int, thinkingMode string, maxOut int, subID string) {
	if model == "" {
		return uc.LLMClient, uc.Model, uc.MaxContextTokens, uc.ThinkingMode, uc.MaxOutputTokens, uc.SubID
	}
	client, resolvedModel, maxCtx, thinkingMode, maxOut, subID = uc.ResolveLLMForModel(model)
	if client == nil {
		return uc.LLMClient, uc.Model, uc.MaxContextTokens, uc.ThinkingMode, uc.MaxOutputTokens, uc.SubID
	}
	return client, resolvedModel, maxCtx, thinkingMode, maxOut, subID
}

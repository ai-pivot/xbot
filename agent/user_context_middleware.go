package agent

import (
	"strconv"

	"xbot/llm"
	log "xbot/logger"
	"xbot/protocol"
	"xbot/storage/sqlite"
)

// singleUserSenderID is the canonical senderID used in single-user mode.
// All users are mapped to this identity — sharing subscriptions, settings,
// sessions, and workspaces.
const singleUserSenderID = "cli_user"

// ResolveUserContext resolves ALL user-related components for a request.
// Called ONCE at processMessage entry — the result is carried via context
// (WithUserContext) and read everywhere via UserContextFromContext.
//
// This is the SINGLE boundary between the user system and the agent loop.
// No code after this point should access LLMFactory / IdentityResolver /
// SettingsService directly.
//
// Identity resolution priority:
//  1. metadata["user_id"] — injected by the channel entry layer using the
//     PHYSICAL channel (e.g. "web"), which is authoritative. This avoids
//     re-resolving via (msg.Channel, senderID) which uses the session's
//     ORIGIN channel (e.g. "cli") — a pair that may not exist in
//     user_identities for cross-channel browsing scenarios.
//  2. IdentityResolver.Resolve(channel, senderID) — fallback when metadata
//     is absent (standalone CLI, SubAgent, etc.).
//
// In single-user mode (a.singleUser == true), all senderIDs are mapped to
// a single canonical identity. This is the ONLY place that needs to change
// to switch between multi-user and single-user modes.
func (a *Agent) ResolveUserContext(channel, chatID, senderID string, metadata map[string]string) *UserContext {
	if a.userSys == nil || a.userSys.llmFactory == nil {
		log.Warn("ResolveUserContext: userSys or llmFactory is nil, returning nil")
		return nil
	}

	// --- Single-user mode: collapse all senders to one identity ---
	if a.singleUser {
		senderID = singleUserSenderID
	}

	// --- LLM ---
	llmClient, model, maxCtx, thinkingMode, maxOut := a.userSys.llmFactory.ResolveLLM(senderID, chatID, channel)
	llmSemAcquire := a.userSys.llmFactory.LLMSemAcquireForUser(senderID, channel)
	subAgentSem := a.userSys.llmFactory.SubAgentSemAcquireForUser(senderID, channel)

	subID := ""
	if sub, _, err := a.userSys.llmFactory.ResolveActiveSubModel(senderID, chatID, channel); err == nil && sub != nil {
		subID = sub.ID
	}

	// --- Settings ---
	var settings map[string]string
	var permUsers *PermUsersConfig
	if a.userSys.settingsSvc != nil {
		if vals, err := a.userSys.settingsSvc.GetSettings(channel, senderID); err == nil {
			settings = vals
		}
		permUsers = a.userSys.settingsSvc.GetPermUsers(channel, senderID)
	}

	// --- Identity ---
	// Channel entry points inject the pre-resolved canonical identity via
	// metadata. This is authoritative — ResolveUserContext must NOT
	// re-resolve via (channel, senderID) because that pair uses the
	// session's origin channel, not the physical channel the user
	// authenticated through. Re-resolving would create a wrong
	// (channel, channel_user_id) pair for cross-channel browsing.
	userID := int64(0)
	role := ""
	standaloneMode := true
	if uid, r, ok := parseUserIDFromMetadata(metadata); ok {
		userID = uid
		role = r
		standaloneMode = false
	} else if a.userSys.identityResolver != nil && !a.singleUser {
		standaloneMode = false
		uid, r, _ := a.userSys.identityResolver.Resolve(channel, senderID)
		userID = uid
		role = r
	}
	if userID == 0 {
		userID = 1
	}
	if role == "" {
		if standaloneMode || a.singleUser {
			role = "admin"
		} else {
			role = "user"
		}
	}

	// --- Sandbox ---
	sandbox := resolveSandbox(a.sandbox, senderID)

	// --- Factory bridge for SubAgent model resolution ---
	factoryRef := &llmFactoryRef{
		getLLMForModel:            a.userSys.llmFactory.GetLLMForModel,
		getLLM:                    a.userSys.llmFactory.GetLLM,
		resolveSubIDForModel:      a.userSys.llmFactory.ResolveSubIDForModel,
		llmSemAcquireForUser:      a.userSys.llmFactory.LLMSemAcquireForUser,
		subAgentSemAcquireForUser: a.userSys.llmFactory.SubAgentSemAcquireForUser,
	}

	// --- Management closures (capture senderID) ---
	hasCustom := func() bool { return a.userSys.llmFactory.HasCustomLLM(senderID) }
	invalidate := func() { a.userSys.llmFactory.Invalidate(senderID) }
	invalidateSender := func() { a.userSys.llmFactory.InvalidateSender(senderID) }
	resolveLLMFresh := func(chatID string) (llm.LLM, string, int, string, int) {
		return a.userSys.llmFactory.ResolveLLM(senderID, chatID, channel)
	}
	resolveActiveSub := func(chatID string) (*sqlite.LLMSubscription, string, error) {
		return a.userSys.llmFactory.ResolveActiveSubModel(senderID, chatID, channel)
	}
	selectModel := func(chatID, subID, model string) error {
		return a.userSys.llmFactory.SelectModel(senderID, chatID, channel, subID, model)
	}
	refreshModels := func() ([]protocol.ModelEntry, []RefreshResult) {
		return a.userSys.llmFactory.RefreshModelEntriesForUserWithResults(senderID)
	}

	uc := &UserContext{
		UserID:           userID,
		Role:             role,
		SenderID:         senderID,
		LLMClient:        llmClient,
		Model:            model,
		ThinkingMode:     thinkingMode,
		MaxContextTokens: maxCtx,
		MaxOutputTokens:  maxOut,
		SubID:            subID,
		Settings:         settings,
		PermUsers:        permUsers,
		Sandbox:          sandbox,
		SandboxMode:      a.sandboxMode,
		LLMSemAcquire:    llmSemAcquire,
		SubAgentSem:      subAgentSem,
		// Management
		SubSvc:           a.userSys.llmFactory.GetSubscriptionSvc(),
		SettingsSvc:      a.userSys.settingsSvc,
		HasCustomLLM:     hasCustom,
		InvalidateLLM:    invalidate,
		InvalidateSender: invalidateSender,
		ResolveLLM:       resolveLLMFresh,
		ResolveActiveSub: resolveActiveSub,
		SelectModel:      selectModel,
		RefreshModels:    refreshModels,
		factory:          factoryRef,
	}

	log.WithFields(log.Fields{
		"channel":     channel,
		"sender_id":   senderID,
		"user_id":     userID,
		"role":        role,
		"model":       model,
		"sub_id":      subID,
		"single_user": a.singleUser,
	}).Debug("UserContext resolved")

	return uc
}

// parseUserIDFromMetadata extracts user_id and role from message metadata.
// Used by buildMainRunConfig to override middleware-resolved identity
// when the channel entry point already set it (avoids double DB lookup).
func parseUserIDFromMetadata(metadata map[string]string) (int64, string, bool) {
	if metadata == nil {
		return 0, "", false
	}
	uidStr := metadata["user_id"]
	if uidStr == "" {
		return 0, "", false
	}
	uid, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil {
		return 0, "", false
	}
	role := metadata["user_role"]
	return uid, role, true
}

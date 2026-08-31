package agent

import (
	"xbot/llm"
	log "xbot/logger"
	"xbot/protocol"
	"xbot/storage/sqlite"
)

// operatorSenderID is the fixed sender used by ALL channels after the
// multi-user removal. Subscriptions, settings, workspace, and memory resolve
// under this single operator identity — every sender (web-N, feishu ou_xxx,
// cli_user) shares one operator. Admin rights are decided per original
// sender via Agent.isAdminSender BEFORE the collapse.
const operatorSenderID = "cli_user"

// isAdminSender reports whether the given channel+sender holds admin rights.
// Trusted channels are always admin (cli = local operator, web = password
// login); every other channel (feishu/qq/...) requires the senderID to be
// listed in config agent.admins. This replaces the removed users.role /
// IdentityResolver authority after the multi-user removal.
func (a *Agent) isAdminSender(channel, senderID string) bool {
	switch channel {
	case "cli":
		return true
	case "web":
		return true
	}
	if senderID == "admin" {
		return true
	}
	for _, s := range a.admins {
		if s == senderID {
			return true
		}
	}
	return false
}

// ResolveUserContext resolves ALL user-related components for a request.
// Called ONCE at processMessage entry — the result is carried via context
// (WithUserContext) and read everywhere via UserContextFromContext.
//
// This is the SINGLE boundary between the (removed) user system and the
// agent loop. After the multi-user removal there is exactly ONE operator:
// every sender collapses to the fixed operator identity, sharing
// subscriptions, settings, workspace, and memory. The only per-sender
// decision left is ADMIN RIGHTS — decided from the ORIGINAL channel+sender
// via Agent.isAdminSender (cli/web are trusted; feishu/qq require the
// config agent.admins allowlist).
func (a *Agent) ResolveUserContext(channel, chatID, senderID string, metadata map[string]string) *UserContext {
	if a.userSys == nil || a.userSys.llmFactory == nil {
		log.Warn("ResolveUserContext: userSys or llmFactory is nil, returning nil")
		return nil
	}

	// --- Admin decision (BEFORE the operator collapse: needs the original
	// channel+sender for the feishu/qq allowlist match) ---
	role := "user"
	if a.isAdminSender(channel, senderID) {
		role = "admin"
	}

	// --- Operator collapse: one identity for everyone ---
	senderID = operatorSenderID

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

	// --- Identity: single operator (multi-user removed) ---
	// userID is a constant (the one operator); role was decided above from
	// the ORIGINAL sender via isAdminSender.
	userID := int64(1)

	// --- Sandbox ---
	sandbox := resolveSandbox(a.sandbox, senderID)

	// --- Factory bridge for SubAgent model resolution ---
	factoryRef := &llmFactoryRef{
		getLLMForModel:            a.userSys.llmFactory.GetLLMForModel,
		getLLM:                    a.userSys.llmFactory.GetLLM,
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
	listModels := func() []protocol.ModelEntry {
		return a.userSys.llmFactory.ListAllModelEntriesForUser(senderID)
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
		ListModels:       listModels,
		factory:          factoryRef,
	}

	log.WithFields(log.Fields{
		"channel":   channel,
		"sender_id": senderID,
		"user_id":   userID,
		"role":      role,
		"model":     model,
		"sub_id":    subID,
	}).Debug("UserContext resolved")

	return uc
}

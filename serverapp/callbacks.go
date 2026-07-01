package serverapp

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"xbot/protocol"

	"xbot/agent"
	"xbot/channel"
	"xbot/channel/feishu"
	"xbot/channel/web"
	"xbot/config"
	log "xbot/logger"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// runnerCallbacks builds the shared Runner callback closures.
// Used by both WebCallbacks and SettingsCallbacks to avoid duplication.
func runnerCallbacks(cfg *config.Config) channel.RunnerCallbacks {
	return channel.RunnerCallbacks{
		RunnerTokenGet: func(senderID string) string {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return ""
			}
			entry := tools.NewRunnerTokenStore(db).Get(senderID)
			if entry == nil {
				return ""
			}
			return buildRunnerConnectCmd(cfg, entry)
		},
		RunnerTokenGenerate: func(senderID, mode, dockerImage, workspace string) (string, error) {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return "", fmt.Errorf("remote sandbox not configured")
			}
			entry, err := tools.NewRunnerTokenStore(db).Generate(senderID, tools.RunnerTokenSettings{
				Mode:        mode,
				DockerImage: dockerImage,
				Workspace:   workspace,
			})
			if err != nil {
				return "", fmt.Errorf("generate token: %w", err)
			}
			return buildRunnerConnectCmd(cfg, entry), nil
		},
		RunnerTokenRevoke: func(senderID string) error {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return fmt.Errorf("remote sandbox not configured")
			}
			tools.NewRunnerTokenStore(db).Revoke(senderID)
			return nil
		},
		RunnerList: func(senderID string) ([]tools.RunnerInfo, error) {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return nil, fmt.Errorf("runner management not configured")
			}
			store := tools.NewRunnerTokenStore(db)
			runners, err := store.ListRunners(senderID)
			if err != nil {
				return nil, err
			}
			populateRunnerOnlineStatus(runners, senderID)
			runners = injectBuiltinDocker(runners)
			return runners, nil
		},
		RunnerCreate: func(senderID, name, mode, dockerImage, workspace string, llm tools.RunnerLLMSettings) (string, error) {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return "", fmt.Errorf("runner management not configured")
			}
			store := tools.NewRunnerTokenStore(db)
			token, _, err := store.CreateRunner(senderID, name, mode, dockerImage, workspace, llm)
			if err != nil {
				return "", err
			}
			return buildRunnerConnectCmdFromToken(cfg, senderID, token, mode, dockerImage, workspace, llm), nil
		},
		RunnerDelete: func(senderID, name string) error {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return fmt.Errorf("runner management not configured")
			}
			if sb := tools.GetSandbox(); sb != nil {
				if router, ok := sb.(*tools.SandboxRouter); ok {
					router.DisconnectRunner(senderID, name)
				}
			}
			return tools.NewRunnerTokenStore(db).DeleteRunner(senderID, name)
		},
		RunnerGetActive: func(senderID string) (string, error) {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return "", fmt.Errorf("runner management not configured")
			}
			return tools.NewRunnerTokenStore(db).GetActiveRunner(senderID)
		},
		RunnerSetActive: func(senderID, name string) error {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return fmt.Errorf("runner management not configured")
			}
			return tools.NewRunnerTokenStore(db).SetActiveRunner(senderID, name)
		},
	}
}

// registryCallbacks builds the shared Registry callback closures.
func registryCallbacks(ag *agent.Agent) channel.RegistryCallbacks {
	return channel.RegistryCallbacks{
		RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
			return ag.RegistryManager().Browse(entryType, limit, offset)
		},
		RegistryInstall: func(entryType string, id int64, senderID string) error {
			return ag.RegistryManager().Install(entryType, id, senderID)
		},
		RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
			return ag.RegistryManager().ListMy(senderID, entryType)
		},
		RegistryPublish: func(entryType, name, senderID string) error {
			return ag.RegistryManager().Publish(entryType, name, senderID)
		},
		RegistryUnpublish: func(entryType, name, senderID string) error {
			return ag.RegistryManager().Unpublish(entryType, name, senderID)
		},
		RegistryUninstall: func(entryType, name, senderID string) error {
			return ag.RegistryManager().Uninstall(entryType, name, senderID)
		},
	}
}

// llmCallbacks builds the shared LLM callback closures.
func llmCallbacks(ag *agent.Agent) channel.LLMCallbacks {
	return channel.LLMCallbacks{
		LLMList: func(senderID string) ([]protocol.ModelEntry, protocol.ModelEntry) {
			entries := ag.LLMFactory().ListAllModelEntriesForUser(senderID)
			sub, model, err := ag.LLMFactory().ResolveActiveSubModel(senderID, "", "")
			if err != nil || sub == nil {
				return entries, protocol.ModelEntry{Model: model}
			}
			return entries, protocol.ModelEntry{SubID: sub.ID, SubName: sub.Name, Model: model}
		},
		LLMSet: func(senderID, subID, model string) error {
			return ag.SetUserModel(senderID, subID, model)
		},
		// MaxContext / MaxOutputTokens: when (subID, model) are provided
		// (feishu model tab passes them from the model selector), write
		// per-(subID, model) config directly. Empty pair (web, legacy)
		// falls back to session-level resolution.
		LLMGetMaxContext: func(senderID, subID, model string) int {
			if subID != "" && model != "" {
				return ag.GetUserMaxContextForSubModel(senderID, subID, model)
			}
			return ag.GetUserMaxContext(senderID, "", "")
		},
		LLMSetMaxContext: func(senderID, subID, model string, maxContext int) error {
			if subID != "" && model != "" {
				return ag.SetUserMaxContextForSubModel(senderID, subID, model, maxContext)
			}
			return ag.SetUserMaxContext(senderID, "", "", maxContext)
		},
		LLMGetMaxOutputTokens: func(senderID, subID, model string) int {
			if subID != "" && model != "" {
				return ag.GetUserMaxOutputTokensForSubModel(senderID, subID, model)
			}
			return ag.GetUserMaxOutputTokens(senderID, "", "")
		},
		LLMSetMaxOutputTokens: func(senderID, subID, model string, maxTokens int) error {
			if subID != "" && model != "" {
				return ag.SetUserMaxOutputTokensForSubModel(senderID, subID, model, maxTokens)
			}
			return ag.SetUserMaxOutputTokens(senderID, "", "", maxTokens)
		},
		LLMGetThinkingMode: func(senderID string) string {
			return ag.GetUserThinkingMode(senderID)
		},
		LLMSetThinkingMode: func(senderID string, mode string) error {
			return ag.SetUserThinkingMode(senderID, mode)
		},
		LLMGetPersonalConcurrency: func(senderID string) int {
			return ag.GetLLMConcurrency(senderID)
		},
		LLMSetPersonalConcurrency: func(senderID string, personal int) error {
			return ag.SetLLMConcurrency(senderID, personal)
		},
	}
}

// populateRunnerOnlineStatus fills the Online field for each runner.
func populateRunnerOnlineStatus(runners []tools.RunnerInfo, senderID string) {
	if sb := tools.GetSandbox(); sb != nil {
		if router, ok := sb.(*tools.SandboxRouter); ok {
			for i := range runners {
				runners[i].Online = router.IsRunnerOnline(senderID, runners[i].Name)
			}
		}
	}
}

// injectBuiltinDocker prepends the built-in docker sandbox runner if available.
func injectBuiltinDocker(runners []tools.RunnerInfo) []tools.RunnerInfo {
	if sb := tools.GetSandbox(); sb != nil {
		if router, ok := sb.(*tools.SandboxRouter); ok && router.HasDocker() {
			dockerEntry := tools.RunnerInfo{
				Name:        tools.BuiltinDockerRunnerName,
				Mode:        "docker",
				DockerImage: router.DockerImage(),
				Online:      true,
			}
			return append([]tools.RunnerInfo{dockerEntry}, runners...)
		}
	}
	return runners
}

// buildRunnerConnectCmdFromToken builds the xbot-runner CLI command from token + settings.
func buildRunnerConnectCmdFromToken(cfg *config.Config, senderID, token, mode, dockerImage, workspace string, llm tools.RunnerLLMSettings) string {
	pubURL := cfg.PublicWSAddr()
	cmd := fmt.Sprintf("./xbot-runner --server %s/ws/%s --token %s", pubURL, senderID, token)
	if mode == "docker" && dockerImage != "" {
		cmd += fmt.Sprintf(" --mode docker --docker-image %s", dockerImage)
	}
	if workspace != "" {
		cmd += fmt.Sprintf(" --workspace %s", workspace)
	}
	if llm.HasLLM() {
		cmd += fmt.Sprintf(" --llm-provider %s --llm-api-key %s --llm-model %s", llm.Provider, llm.APIKey, llm.Model)
		if llm.BaseURL != "" {
			cmd += fmt.Sprintf(" --llm-base-url %s", llm.BaseURL)
		}
	}
	return cmd
}

// buildWebCallbacks creates WebCallbacks using shared callback builders.
func buildWebCallbacks(cfg *config.Config, ag *agent.Agent, webDB *sql.DB) web.WebCallbacks {
	rc := runnerCallbacks(cfg)
	regc := registryCallbacks(ag)
	llmc := llmCallbacks(ag)

	callbacks := web.WebCallbacks{
		// Runner callbacks
		RunnerTokenGet:      rc.RunnerTokenGet,
		RunnerTokenGenerate: rc.RunnerTokenGenerate,
		RunnerTokenRevoke:   rc.RunnerTokenRevoke,
		RunnerList:          rc.RunnerList,
		RunnerCreate:        rc.RunnerCreate,
		RunnerDelete:        rc.RunnerDelete,
		RunnerGetActive:     rc.RunnerGetActive,
		RunnerSetActive:     rc.RunnerSetActive,

		// Registry callbacks
		RegistryBrowse:    regc.RegistryBrowse,
		RegistryInstall:   regc.RegistryInstall,
		RegistryListMy:    regc.RegistryListMy,
		RegistryPublish:   regc.RegistryPublish,
		RegistryUnpublish: regc.RegistryUnpublish,
		RegistryUninstall: regc.RegistryUninstall,

		// LLM callbacks (Web channel exposes only basic model/max-context via HTTP API;
		// ThinkingMode/MaxOutputTokens/PersonalConcurrency are CLI-only via RPC.)
		LLMList:          llmc.LLMList,
		LLMSet:           llmc.LLMSet,
		LLMGetMaxContext: llmc.LLMGetMaxContext,
		LLMSetMaxContext: llmc.LLMSetMaxContext,

		// SandboxWriteFile — Web-specific
		SandboxWriteFile: func(senderID string, sandboxRelPath string, data []byte, perm os.FileMode) (string, error) {
			sandbox := tools.GetSandbox()
			if sandbox == nil {
				return "", fmt.Errorf("no sandbox available")
			}
			resolver, ok := sandbox.(tools.SandboxResolver)
			if !ok {
				return "", fmt.Errorf("sandbox does not support per-user resolution")
			}
			userSbx := resolver.SandboxForUser(senderID)
			if userSbx == nil || userSbx.Name() == "none" {
				return "", fmt.Errorf("no sandbox available for user %s", senderID)
			}
			ws := userSbx.Workspace(senderID)
			absPath := filepath.Join(ws, sandboxRelPath)
			dir := filepath.Dir(absPath)
			if err := userSbx.MkdirAll(context.Background(), dir, 0755, senderID); err != nil {
				log.WithError(err).Warn("Failed to create directory in sandbox")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := userSbx.WriteFile(ctx, absPath, data, perm, senderID); err != nil {
				return "", err
			}
			return ws, nil
		},
	}

	// Wire IsProcessing
	callbacks.IsProcessing = func(senderID string) bool {
		return ag.IsProcessing(senderID)
	}
	// Wire GetActiveProgress
	callbacks.GetActiveProgress = func(channel, chatID string) *protocol.ProgressEvent {
		return ag.GetActiveProgress(channel, chatID)
	}
	// Wire SessionsList
	callbacks.SessionsList = func(senderID string) []web.SessionInfo {
		sessions := ag.ListInteractiveSessions("web", senderID)
		result := make([]web.SessionInfo, len(sessions))
		for i, s := range sessions {
			result[i] = web.ChatRoom{
				ID:       s.Role + "/" + s.Instance,
				Type:     "subagent",
				Label:    s.Role + "/" + s.Instance,
				Role:     s.Role,
				Instance: s.Instance,
				Running:  s.Running,
				Preview:  s.Preview,
				Members:  "Agent ↔ " + s.Role,
			}
		}
		return result
	}
	// Wire SessionMessages
	callbacks.SessionMessages = func(senderID, roleName, instance string) ([]channel.SessionChatMessage, bool) {
		msgs, ok := ag.GetSessionMessages("web", senderID, roleName, instance)
		if !ok {
			return nil, false
		}
		result := make([]channel.SessionChatMessage, len(msgs))
		for i, m := range msgs {
			result[i] = channel.SessionChatMessage{Role: m.Role, Content: m.Content}
		}
		return result, true
	}
	// Wire Chat CRUD
	callbacks.ChatList = func(senderID, currentChatID, channel string) ([]web.UserChatWithPreview, error) {
		if webDB == nil {
			return nil, nil
		}
		// For web channel: use ChatService.ListUserChats (includes user-created chatrooms).
		if channel == "" || channel == "web" {
			cs := sqlite.NewChatService(webDB)
			chats, err := cs.ListUserChats("web", senderID, currentChatID)
			if err != nil {
				return nil, err
			}
			result := make([]web.UserChatWithPreview, len(chats))
			for i, c := range chats {
				result[i] = web.UserChatWithPreview{
					ChatID:     c.ChatID,
					Channel:    "web",
					Label:      c.Label,
					LastActive: c.LastActive.Format(time.RFC3339),
					Preview:    c.Preview,
					IsCurrent:  c.IsCurrent,
				}
			}
			return result, nil
		}
		// For non-web channels (admin only): list all tenants in that channel.
		return listTenantsByChannel(webDB, channel, currentChatID)
	}
	callbacks.ChatCreate = func(senderID, label string) (string, error) {
		if webDB == nil {
			return "", fmt.Errorf("database not available")
		}
		cs := sqlite.NewChatService(webDB)
		return cs.CreateChat("web", senderID, label)
	}
	callbacks.ChatDelete = func(senderID, chatID string) error {
		if webDB == nil {
			return fmt.Errorf("database not available")
		}
		cs := sqlite.NewChatService(webDB)
		return cs.DeleteChat("web", senderID, chatID)
	}
	callbacks.ChatRename = func(senderID, chatID, label string) error {
		if webDB == nil {
			return fmt.Errorf("database not available")
		}
		cs := sqlite.NewChatService(webDB)
		return cs.RenameChat("web", senderID, chatID, label)
	}
	return callbacks
}

// listTenantsByChannel lists all tenants for a given channel (e.g. "cli", "feishu").
// Used by admin to browse sessions from other channels in the Web UI.
// Returns the last user/assistant message as preview.
func listTenantsByChannel(db *sql.DB, channel, currentChatID string) ([]web.UserChatWithPreview, error) {
	// Single query with correlated subquery for preview — avoids N+1 pattern.
	rows, err := db.Query(`
		SELECT t.id, t.chat_id, t.last_active_at,
		       (SELECT sm.content FROM session_messages sm
		        WHERE sm.tenant_id = t.id AND sm.role IN ('user', 'assistant')
		        ORDER BY sm.id DESC LIMIT 1) AS preview
		FROM tenants t
		WHERE t.channel = ? AND t.chat_id != '_shared'
		ORDER BY t.last_active_at DESC`, channel)
	if err != nil {
		return nil, fmt.Errorf("list tenants by channel: %w", err)
	}
	defer rows.Close()

	var result []web.UserChatWithPreview
	for rows.Next() {
		var tenantID int64
		var chatID, lastActiveStr string
		var preview sql.NullString
		if err := rows.Scan(&tenantID, &chatID, &lastActiveStr, &preview); err != nil {
			log.WithError(err).Warn("Failed to scan tenant row in listTenantsByChannel")
			continue
		}

		previewStr := preview.String
		if runes := []rune(previewStr); len(runes) > 80 {
			previewStr = string(runes[:80])
		}

		// Try RFC3339 first, then SQLite's "2006-01-02 15:04:05" format
		// (DEFAULT CURRENT_TIMESTAMP produces the latter).
		lastActive, err := time.Parse(time.RFC3339, lastActiveStr)
		if err != nil {
			lastActive, err = time.Parse("2006-01-02 15:04:05", lastActiveStr)
			if err != nil {
				log.WithError(err).WithField("raw", lastActiveStr).Warn("Failed to parse last_active_at in listTenantsByChannel")
			}
		}

		result = append(result, web.UserChatWithPreview{
			ChatID:     chatID,
			Channel:    channel,
			Label:      chatID, // For non-web channels, use chatID as label
			LastActive: lastActive.Format(time.RFC3339),
			Preview:    previewStr,
			IsCurrent:  chatID == currentChatID,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenants: %w", err)
	}
	return result, nil
}

// buildFeishuSettingsCallbacks builds SettingsCallbacks for Feishu using shared builders.
func buildFeishuSettingsCallbacks(cfg *config.Config, ag *agent.Agent) feishu.SettingsCallbacks {
	rc := runnerCallbacks(cfg)
	regc := registryCallbacks(ag)
	llmc := llmCallbacks(ag)

	return feishu.SettingsCallbacks{
		// LLM basic callbacks
		LLMList:                   llmc.LLMList,
		LLMSet:                    llmc.LLMSet,
		LLMGetMaxContext:          llmc.LLMGetMaxContext,
		LLMSetMaxContext:          llmc.LLMSetMaxContext,
		LLMGetMaxOutputTokens:     llmc.LLMGetMaxOutputTokens,
		LLMSetMaxOutputTokens:     llmc.LLMSetMaxOutputTokens,
		LLMGetThinkingMode:        llmc.LLMGetThinkingMode,
		LLMSetThinkingMode:        llmc.LLMSetThinkingMode,
		LLMGetPersonalConcurrency: llmc.LLMGetPersonalConcurrency,
		LLMSetPersonalConcurrency: llmc.LLMSetPersonalConcurrency,

		// LLM config (Feishu-specific — uses channel.Subscription directly)
		LLMGetConfig: func(senderID string) (provider, baseURL, model string, ok bool) {
			return "", "", "", false
		},
		LLMSetConfig: func(senderID, provider, baseURL, apiKey, model string, maxOutputTokens int, thinkingMode string) error {
			return fmt.Errorf("not supported in server mode")
		},
		LLMDelete: func(senderID string) error {
			return fmt.Errorf("not supported in server mode")
		},

		// Subscription management
		LLMListSubscriptions: func(senderID string) ([]channel.Subscription, error) {
			subs, err := ag.LLMFactory().GetSubscriptionSvc().List(senderID)
			if err != nil {
				return nil, err
			}
			result := make([]channel.Subscription, len(subs))
			for i, s := range subs {
				result[i] = subToChannel(s)
			}
			return result, nil
		},
		LLMGetDefaultSubscription: func(senderID string) (*channel.Subscription, error) {
			sub, err := ag.LLMFactory().GetSubscriptionSvc().GetDefault(senderID)
			if err != nil || sub == nil {
				return nil, err
			}
			// Return raw APIKey (not masked) — this is used for editing,
			// and matches the original master behavior.
			ch := channel.Subscription{
				ID: sub.ID, Name: sub.Name, Provider: sub.Provider,
				BaseURL: sub.BaseURL, APIKey: sub.APIKey,
				Model: sub.Model, Active: sub.IsDefault, Enabled: sub.Enabled,
				MaxOutputTokens: sub.MaxOutputTokens, ThinkingMode: sub.ThinkingMode,
			}
			return &ch, nil
		},
		LLMAddSubscription: func(senderID string, sub *channel.Subscription) error {
			svc := ag.LLMFactory().GetSubscriptionSvc()
			newSub := &sqlite.LLMSubscription{
				SenderID: senderID,
				Name:     sub.Name,
				Provider: sub.Provider,
				BaseURL:  sub.BaseURL,
				APIKey:   sub.APIKey,
				Model:    sub.Model,
			}
			// If user has no default subscription yet, auto-set the first one.
			existing, _ := svc.List(senderID)
			if len(existing) == 0 {
				newSub.IsDefault = true
			}
			if err := svc.Add(newSub); err != nil {
				return err
			}
			ag.LLMFactory().InvalidateSender(senderID)
			ag.LLMFactory().InvalidateSubscription(newSub.ID)
			return nil
		},
		LLMRemoveSubscription: func(id string) error {
			svc := ag.LLMFactory().GetSubscriptionSvc()
			sub, err := svc.Get(id)
			if err != nil {
				return err
			}
			// Remove cascades: deletes subscription_models + user_default_model
			// (if pointing to this sub) in a single transaction.
			if err := svc.Remove(id); err != nil {
				return err
			}
			// Clear stale tenant entries pointing to the deleted subscription.
			// Without this, every ResolveLLM call wastes cycles looking up a
			// non-existent subscription before falling back to system default.
			if ts := ag.LLMFactory().GetTenantSvc(); ts != nil {
				if err := ts.ClearSubscriptionFromTenants(id); err != nil {
					log.WithError(err).WithField("sub_id", id).Warn("Failed to clear tenant subscription references")
				}
			}
			ag.LLMFactory().InvalidateSender(sub.SenderID)
			ag.LLMFactory().InvalidateSubscription(sub.ID)
			return nil
		},
		LLMSetDefaultSubscription: func(id string) error {
			svc := ag.LLMFactory().GetSubscriptionSvc()
			if err := svc.SetDefault(id); err != nil {
				return err
			}
			sub, err := svc.Get(id)
			if err == nil && sub != nil {
				// Use InvalidateSender to preserve per-session entries.
				// Invalidate() would wipe every session's LLM override.
				ag.LLMFactory().InvalidateSender(sub.SenderID)
				ag.LLMFactory().InvalidateSubscription(sub.ID)
				_ = ag.LLMFactory().SwitchSubscription(sub.SenderID, sub, "")
			}
			return nil
		},
		LLMRenameSubscription: func(id, name string) error {
			return ag.LLMFactory().GetSubscriptionSvc().Rename(id, name)
		},
		LLMSetSubscriptionEnabled: func(id string, enabled bool) error {
			svc := ag.LLMFactory().GetSubscriptionSvc()
			if err := svc.SetSubscriptionEnabled(id, enabled); err != nil {
				return err
			}
			// Invalidate cache so the change takes effect immediately.
			sub, err := svc.Get(id)
			if err == nil && sub != nil {
				ag.LLMFactory().InvalidateSender(sub.SenderID)
				ag.LLMFactory().InvalidateSubscription(sub.ID)
			}
			return nil
		},

		LLMUpdateSubscription: func(id string, sub *channel.Subscription) error {
			svc := ag.LLMFactory().GetSubscriptionSvc()
			existing, err := svc.Get(id)
			if err != nil {
				return err
			}
			existing.Name = sub.Name
			// Masked-key protection: never overwrite credential fields with masked values.
			// This matches the updateSubscription RPC handler in rpc_table.go.
			if sub.Provider != "" && !strings.Contains(sub.Provider, "****") {
				existing.Provider = sub.Provider
			}
			if sub.BaseURL != "" && !strings.Contains(sub.BaseURL, "****") {
				existing.BaseURL = sub.BaseURL
			}
			if sub.APIKey != "" && !strings.HasSuffix(sub.APIKey, "****") {
				existing.APIKey = sub.APIKey
			}
			if sub.Model != "" {
				existing.Model = sub.Model
			}
			if err := svc.Update(existing); err != nil {
				return err
			}
			ag.LLMFactory().InvalidateSender(existing.SenderID)
			ag.LLMFactory().InvalidateSubscription(existing.ID)
			return nil
		},

		// Model tier — per-user, stored in user_settings DB (same pattern as
		// thinking_mode). No global config fallback — tier is purely per-user.
		LLMGetModelTier: func(senderID, tier string) (subID, model string) {
			return ag.GetUserTierModel(senderID, tier)
		},
		LLMSetModelTier: func(senderID, tier, subID, model string) error {
			return ag.SetUserTierModel(senderID, tier, subID, model)
		},
		LLMListAllModels: func(senderID string) []protocol.ModelEntry {
			return ag.LLMFactory().ListAllModelEntriesForUser(senderID)
		},

		// Context mode
		ContextModeGet: func() string {
			return ag.GetContextMode()
		},
		ContextModeSet: func(mode string) error {
			return ag.SetContextMode(mode)
		},

		// Registry
		RegistryBrowse:    regc.RegistryBrowse,
		RegistryInstall:   regc.RegistryInstall,
		RegistryListMy:    regc.RegistryListMy,
		RegistryPublish:   regc.RegistryPublish,
		RegistryUnpublish: regc.RegistryUnpublish,
		RegistryDelete:    regc.RegistryUninstall,

		// Metrics
		MetricsGet: func() string {
			return agent.GlobalMetrics.Snapshot().FormatMarkdown()
		},

		// Sandbox
		SandboxCleanupTrigger: func(senderID string) error {
			sb := tools.GetSandbox()
			if sb == nil {
				return fmt.Errorf("sandbox not initialized")
			}
			return sb.ExportAndImport(senderID)
		},
		SandboxIsExporting: func(senderID string) bool {
			sb := tools.GetSandbox()
			if sb == nil {
				return false
			}
			return sb.IsExporting(senderID)
		},

		// Runner callbacks
		RunnerConnectCmdGet: func(senderID string) string {
			token := cfg.Sandbox.AuthToken
			if token == "" {
				return ""
			}
			pubURL := cfg.PublicWSAddr()
			return fmt.Sprintf("./xbot-runner --server %s/ws/%s --token %s", pubURL, senderID, token)
		},
		RunnerTokenGet:      rc.RunnerTokenGet,
		RunnerTokenGenerate: rc.RunnerTokenGenerate,
		RunnerTokenRevoke:   rc.RunnerTokenRevoke,
		RunnerList:          rc.RunnerList,
		RunnerCreate:        rc.RunnerCreate,
		RunnerDelete:        rc.RunnerDelete,
		RunnerGetActive:     rc.RunnerGetActive,
		RunnerSetActive:     rc.RunnerSetActive,

		// Feishu-Web linking
		FeishuWebLink: func(feishuUserID, username, password string) (string, error) {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return "", fmt.Errorf("web linking not enabled")
			}
			return web.FeishuLinkUser(db, feishuUserID, username, password)
		},
		FeishuWebGetLinked: func(feishuUserID string) (string, bool) {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return "", false
			}
			return web.FeishuGetLinkedUser(db, feishuUserID)
		},
		FeishuWebUnlink: func(feishuUserID string) error {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return fmt.Errorf("web linking not enabled")
			}
			return web.FeishuUnlinkUser(db, feishuUserID)
		},

		// Memory
		MemoryClear: func(senderID, chatID, targetType string) error {
			return ag.MultiSession().ClearMemory(context.Background(), "feishu", chatID, targetType, senderID)
		},
		MemoryGetStats: func(senderID, chatID string) map[string]string {
			return ag.MultiSession().GetMemoryStats(context.Background(), "feishu", chatID, senderID)
		},
	}
}

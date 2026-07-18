package serverapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"xbot/protocol"

	"xbot/agent"
	"xbot/channel"
	cli "xbot/channel/cli"
	"xbot/channel/feishu"
	"xbot/channel/web"
	"xbot/config"
	log "xbot/logger"
	"xbot/session"
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
func buildWebCallbacks(cfg *config.Config, ag *agent.Agent, webDB *sqlite.DB) web.WebCallbacks {
	rc := runnerCallbacks(cfg)
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
		return ag.GetActiveProgress(channel, chatID, 0) // web always requests full history
	}
	callbacks.GetPendingAskUser = func(channel, chatID string) *protocol.ProgressEvent {
		return ag.GetPendingAskUser(channel, chatID)
	}
	callbacks.WithPendingAskUser = ag.WithPendingAskUser
	callbacks.HistorySnapshot = func(senderID string, sel web.SessionSelector) (web.HistorySnapshot, error) {
		if ag.MultiSession() == nil {
			return web.HistorySnapshot{}, fmt.Errorf("multi-session not available")
		}
		if db := ag.MultiSession().DB(); db != nil {
			if _, err := sqlite.NewTenantService(db).GetOrCreateTenantID(sel.Channel, sel.ChatID); err != nil {
				log.WithError(err).Warn("Web history: failed to update last_active_at")
			}
		}
		sess, err := ag.MultiSession().GetOrCreateSession(sel.Channel, sel.ChatID)
		if err != nil {
			return web.HistorySnapshot{}, err
		}
		msgs, err := sess.GetMessages()
		if err != nil {
			return web.HistorySnapshot{}, err
		}
		progress := ag.GetActiveProgress(sel.Channel, sel.ChatID, 0)
		if progress != nil && progress.Phase == "done" {
			progress = nil
		}
		return web.HistorySnapshot{
			Messages:       channel.ConvertMessagesToHistory(msgs),
			Processing:     ag.IsProcessingByChannel(sel.Channel, sel.ChatID),
			ActiveProgress: progress,
			ChatID:         sel.ChatID,
			Channel:        sel.Channel,
		}, nil
	}
	callbacks.RewindHistory = func(senderID string, sel web.SessionSelector, cutoff time.Time) (web.RewindHistoryResult, error) {
		return rewindWebHistory(ag, sel.Channel, sel.ChatID, cutoff)
	}
	callbacks.GetCWD = func(senderID string, sel web.SessionSelector) (string, error) {
		return webSessionCWD(ag, sel.Channel, sel.ChatID), nil
	}
	callbacks.SetCWD = func(senderID string, sel web.SessionSelector, dir string) error {
		return ag.SetCWD(sel.Channel, sel.ChatID, dir)
	}
	callbacks.BackgroundTasks = func(senderID string, sel web.SessionSelector) (any, error) {
		if ag.BgTaskManager() == nil {
			return []webBgTaskJSON{}, nil
		}
		return marshalWebBgTasks(ag.BgTaskManager().ListAllForSession(sel.Channel + ":" + sel.ChatID)), nil
	}
	callbacks.CronTasks = func(senderID string, sel web.SessionSelector) (any, error) {
		if ag.MultiSession() == nil || ag.MultiSession().DB() == nil {
			return []any{}, nil
		}
		if _, err := sqlite.NewTenantService(ag.MultiSession().DB()).GetOrCreateTenantID(sel.Channel, sel.ChatID); err != nil {
			return nil, fmt.Errorf("resolve tenant: %w", err)
		}
		return sqlite.NewCronService(ag.MultiSession().DB()).ListJobsByChannelChatID(sel.Channel, sel.ChatID)
	}
	callbacks.CommandList = func(senderID string) ([]web.CommandInfo, error) {
		byName := make(map[string]web.CommandInfo, len(channel.TUISlashCommands))
		for _, name := range channel.TUISlashCommands {
			byName[name] = web.CommandInfo{Name: name}
		}
		var agentCommands []web.CommandInfo
		if ag.Commands() != nil {
			for _, cmd := range ag.Commands().CommandList() {
				if cmd.Name == "" {
					continue
				}
				info := web.CommandInfo{Name: cmd.Name, Aliases: cmd.Aliases, Description: cmd.Description}
				byName[cmd.Name] = info
				agentCommands = append(agentCommands, info)
			}
		}
		result := make([]web.CommandInfo, 0, len(byName))
		for _, name := range channel.TUISlashCommands {
			if cmd, ok := byName[name]; ok {
				result = append(result, cmd)
				delete(byName, name)
			}
		}
		for _, cmd := range agentCommands {
			if _, ok := byName[cmd.Name]; !ok {
				continue
			}
			result = append(result, cmd)
			delete(byName, cmd.Name)
		}
		return result, nil
	}
	callbacks.SessionSubscription = func(senderID string, sel web.SessionSelector) (map[string]string, error) {
		if ag.MultiSession() != nil && ag.MultiSession().DB() != nil {
			subID, model, _ := sqlite.NewTenantService(ag.MultiSession().DB()).GetTenantSubscription(sel.Channel, sel.ChatID)
			if subID != "" {
				return map[string]string{"subscription_id": subID, "model": model}, nil
			}
		}
		if ag.LLMFactory() != nil {
			if sub, model, err := ag.LLMFactory().ResolveActiveSubModel(senderID, sel.ChatID, sel.Channel); err == nil && sub != nil {
				return map[string]string{"subscription_id": sub.ID, "model": model}, nil
			}
		}
		return map[string]string{}, nil
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
				applyWebRunningStatus(ag, &result[i])
			}
			return result, nil
		}
		// For non-web channels (admin only): list all tenants in that channel.
		if channel == "cli" {
			rows, err := listCLIChatSessions(webDB.Conn(), currentChatID)
			applyWebRunningStatuses(ag, rows)
			return rows, err
		}
		rows, err := listTenantsByChannel(webDB.Conn(), channel, currentChatID)
		applyWebRunningStatuses(ag, rows)
		return rows, err
	}
	callbacks.SubAgentList = func(senderID string, admin bool) ([]web.UserChatWithPreview, error) {
		if webDB == nil {
			return nil, nil
		}
		allowedWebParents := map[string]bool{}
		if !admin {
			var err error
			allowedWebParents, err = listWebChatIDsForSender(webDB.Conn(), senderID)
			if err != nil {
				return nil, err
			}
		}
		var rows []web.UserChatWithPreview
		if admin {
			var err error
			rows, err = listTenantsByChannel(webDB.Conn(), "agent", "")
			if err != nil {
				return nil, err
			}
		} else {
			agentRows, err := listTenantsByChannel(webDB.Conn(), "agent", "")
			if err != nil {
				return nil, err
			}
			for _, row := range agentRows {
				if subAgentRowBelongsToAllowedWebParent(row, allowedWebParents) {
					rows = append(rows, row)
				}
			}
		}
		seen := make(map[string]bool, len(rows))
		for _, row := range rows {
			seen[subAgentRowKey(row)] = true
		}
		for _, s := range ag.ListInteractiveSessions("web", "") {
			if admin || allowedWebParents[s.ChatID] {
				rows = upsertSubAgentRow(rows, seen, "web", s)
			}
		}
		if admin {
			for _, s := range ag.ListInteractiveSessions("cli", "") {
				rows = upsertSubAgentRow(rows, seen, "cli", s)
			}
			for _, s := range ag.ListInteractiveSessions("agent", "") {
				rows = upsertSubAgentRow(rows, seen, "agent", s)
			}
		} else {
			for _, s := range ag.ListInteractiveSessions("agent", "") {
				row := subAgentRowFromInteractiveSession("agent", s)
				if subAgentRowBelongsToAllowedWebParent(row, allowedWebParents) {
					rows = upsertSubAgentRow(rows, seen, "agent", s)
				}
			}
		}
		applyWebRunningStatuses(ag, rows)
		return rows, nil
	}
	callbacks.SessionTree = func(senderID string, current web.SessionSelector, admin bool) (web.SessionTreeResult, error) {
		if webDB == nil {
			return web.SessionTreeResult{}, nil
		}
		var mains []web.UserChatWithPreview
		webCurrent := ""
		if current.Channel == "web" {
			webCurrent = current.ChatID
		}
		cs := sqlite.NewChatService(webDB)
		webChats, err := cs.ListUserChats("web", senderID, webCurrent)
		if err != nil {
			return web.SessionTreeResult{}, err
		}
		for _, c := range webChats {
			mains = append(mains, web.UserChatWithPreview{
				ChatID:     c.ChatID,
				Channel:    "web",
				Label:      c.Label,
				LastActive: c.LastActive.Format(time.RFC3339),
				Preview:    c.Preview,
				IsCurrent:  c.IsCurrent,
			})
			applyWebRunningStatus(ag, &mains[len(mains)-1])
		}
		if admin {
			cliCurrent := ""
			if current.Channel == "cli" {
				cliCurrent = current.ChatID
			}
			cliChats, err := listCLIChatSessions(webDB.Conn(), cliCurrent)
			if err != nil {
				return web.SessionTreeResult{}, err
			}
			applyWebRunningStatuses(ag, cliChats)
			mains = append(mains, cliChats...)
			webTenants, err := listTenantsByChannel(webDB.Conn(), "web", webCurrent)
			if err != nil {
				return web.SessionTreeResult{}, err
			}
			applyWebRunningStatuses(ag, webTenants)
			mains = appendMissingSessionRows(mains, webTenants)
		}

		subagents, err := callbacks.SubAgentList(senderID, admin)
		if err != nil {
			return web.SessionTreeResult{}, err
		}
		applyWebRunningStatuses(ag, subagents)
		return buildSessionTree(mains, subagents), nil
	}
	callbacks.ChatCreate = func(senderID, label string) (string, error) {
		if webDB == nil {
			return "", fmt.Errorf("database not available")
		}
		cs := sqlite.NewChatService(webDB)
		return cs.CreateChat("web", senderID, label)
	}
	callbacks.ChatDelete = func(senderID, channel, chatID string) error {
		if webDB == nil {
			return fmt.Errorf("database not available")
		}
		removedLocal := false
		if channel == "cli" {
			var err error
			removedLocal, err = cli.RemoveStoredSessionByChatID(chatID)
			if err != nil {
				return fmt.Errorf("remove CLI session metadata: %w", err)
			}
		}
		cs := sqlite.NewChatService(webDB)
		if err := cs.DeleteChat(channel, senderID, chatID); err != nil {
			if !removedLocal || !errors.Is(err, sqlite.ErrChatNotFound) {
				return err
			}
		}
		ag.CleanupSessionFiles(channel, chatID)
		tools.GlobalWorktreeRegistry.CleanupSession(channel + ":" + chatID)
		session.DeletePersistedCWD(channel, chatID)
		_ = ag.MultiSession().DestroySession(channel, chatID)
		return nil
	}
	callbacks.ChatRename = func(senderID, channel, chatID, label string) error {
		if webDB == nil {
			return fmt.Errorf("database not available")
		}
		if channel == "cli" {
			if _, err := cli.RenameStoredSessionByChatID(chatID, label); err != nil {
				return fmt.Errorf("rename CLI session metadata: %w", err)
			}
		}
		cs := sqlite.NewChatService(webDB)
		return cs.RenameChat(channel, senderID, chatID, label)
	}
	callbacks.LocalSessionExists = func(channel, chatID string) bool {
		return channel == "cli" && cli.StoredSessionExists(chatID)
	}

	// Identity resolver — wire agent's IdentityResolver to WebChannel
	callbacks.IdentityResolver = ag.IdentityResolver()

	return callbacks
}

func applyWebRunningStatuses(ag *agent.Agent, rows []web.UserChatWithPreview) {
	for i := range rows {
		applyWebRunningStatus(ag, &rows[i])
	}
}

func applyWebRunningStatus(ag *agent.Agent, row *web.UserChatWithPreview) {
	if row == nil {
		return
	}
	ch := row.Channel
	if ch == "" {
		ch = "web"
	}
	chatID := row.ChatID
	if row.FullKey != "" && (ch == "agent" || row.Type == "agent") {
		chatID = row.FullKey
	}
	if ch != "agent" && row.Type != "agent" {
		row.WorkDir = webSessionCWD(ag, ch, chatID)
	}
	if ag != nil {
		row.Running = ag.IsProcessingByChannel(ch, chatID)
		if row.Running {
			row.Status = "running"
		} else if row.Status == "" {
			row.Status = "idle"
		}
	}
	for i := range row.Children {
		applyWebRunningStatus(ag, &row.Children[i])
	}
}

func webSessionCWD(ag *agent.Agent, channelName, chatID string) string {
	dir := session.LoadPersistedCWD(channelName, chatID)
	if dir == "" && channelName == "cli" {
		if workDir, _ := parseCLIChatID(chatID); workDir != "" {
			dir = workDir
		}
	}
	if dir == "" && ag != nil && ag.MultiSession() != nil {
		if sess, ok := ag.MultiSession().GetSession(channelName, chatID); ok && sess != nil {
			dir = sess.GetCurrentDir()
		}
	}
	return dir
}

func rewindWebHistory(ag *agent.Agent, channelName, chatID string, cutoff time.Time) (web.RewindHistoryResult, error) {
	if ag == nil || ag.MultiSession() == nil {
		return web.RewindHistoryResult{}, fmt.Errorf("multi-session not available")
	}
	sess, err := ag.MultiSession().GetOrCreateSession(channelName, chatID)
	if err != nil {
		return web.RewindHistoryResult{}, err
	}
	msgs, err := sess.GetMessages()
	if err != nil {
		return web.RewindHistoryResult{}, err
	}
	history := channel.ConvertMessagesToHistory(msgs)
	compactCutoff := -1
	selectedEligibleOrdinal := 0
	draft := ""
	for i, msg := range history {
		if msg.Role != "user" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(msg.Content), "[Compacted context]") {
			compactCutoff = i
			selectedEligibleOrdinal = 0
		}
		if compactCutoff >= 0 && i < compactCutoff {
			continue
		}
		selectedEligibleOrdinal++
		if msg.Timestamp.Equal(cutoff) || msg.Timestamp.After(cutoff) {
			if !msg.Timestamp.Equal(cutoff) {
				selectedEligibleOrdinal--
				break
			}
			draft = msg.Content
			break
		}
	}
	if draft == "" {
		return web.RewindHistoryResult{}, fmt.Errorf("rewind target not found")
	}
	var checkpoint *protocol.RewindResult
	if result, err := ag.RewindCheckpoint(channelName, chatID, selectedEligibleOrdinal); err != nil {
		log.WithError(err).Warn("rewind checkpoint failed")
	} else {
		checkpoint = result
	}
	if err := ag.MultiSession().TrimHistory(channelName, chatID, cutoff); err != nil {
		return web.RewindHistoryResult{}, err
	}
	return web.RewindHistoryResult{Draft: draft, RewindResult: checkpoint}, nil
}

type webBgTaskJSON struct {
	ID         string `json:"id"`
	Command    string `json:"command"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Output     string `json:"output"`
	ExitCode   int    `json:"exit_code"`
	Error      string `json:"error,omitempty"`
}

func marshalWebBgTasks(tasks []*tools.BackgroundTask) []webBgTaskJSON {
	result := make([]webBgTaskJSON, len(tasks))
	for i, t := range tasks {
		result[i] = webBgTaskJSON{t.ID, t.Command, string(t.Status), t.StartedAt.Format(time.RFC3339), "", t.Output, t.ExitCode, t.Error}
		if t.FinishedAt != nil {
			result[i].FinishedAt = t.FinishedAt.Format(time.RFC3339)
		}
	}
	return result
}

func appendMissingSessionRows(base, extra []web.UserChatWithPreview) []web.UserChatWithPreview {
	seen := make(map[string]bool, len(base))
	for _, row := range base {
		seen[sessionTreeKey(row.Channel, row.ChatID)] = true
	}
	for _, row := range extra {
		key := sessionTreeKey(row.Channel, row.ChatID)
		if seen[key] {
			continue
		}
		seen[key] = true
		base = append(base, row)
	}
	return base
}

func listWebChatIDsForSender(db *sql.DB, senderID string) (map[string]bool, error) {
	result := map[string]bool{senderID: true}
	rows, err := db.Query("SELECT chat_id FROM user_chats WHERE channel = 'web' AND sender_id = ?", senderID)
	if err != nil {
		return nil, fmt.Errorf("list web chat ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var chatID string
		if err := rows.Scan(&chatID); err != nil {
			return nil, fmt.Errorf("scan web chat id: %w", err)
		}
		result[chatID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate web chat ids: %w", err)
	}
	return result, nil
}

// listTenantsByChannel lists all tenants for a given channel (e.g. "cli", "feishu").
// Used by admin to browse sessions from other channels in the Web UI.
// Returns the last user/assistant message as preview.
func listTenantsByChannel(db *sql.DB, channel, currentChatID string) ([]web.UserChatWithPreview, error) {
	// Single query with correlated subquery for preview — avoids N+1 pattern.
	rows, err := db.Query(`
		SELECT t.id, t.chat_id, t.last_active_at,
									COALESCE((SELECT uc.label FROM user_chats uc
										WHERE uc.channel = t.channel AND uc.chat_id = t.chat_id AND uc.label != ''
										ORDER BY uc.rowid DESC LIMIT 1),
										(SELECT uc.label FROM user_chats uc
										WHERE uc.chat_id = t.chat_id AND uc.label != ''
										ORDER BY uc.rowid DESC LIMIT 1), '') AS label,
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
		var chatID, lastActiveStr, label string
		var preview sql.NullString
		if err := rows.Scan(&tenantID, &chatID, &lastActiveStr, &label, &preview); err != nil {
			log.WithError(err).Warn("Failed to scan tenant row in listTenantsByChannel")
			continue
		}
		previewStr := preview.String
		if runes := []rune(previewStr); len(runes) > 80 {
			previewStr = string(runes[:80])
		}

		lastActive := parseTenantTime(lastActiveStr)

		chat := web.UserChatWithPreview{
			ChatID:     chatID,
			Channel:    channel,
			Label:      displayLabelForTenant(channel, chatID, label),
			LastActive: lastActive.Format(time.RFC3339),
			Preview:    previewStr,
			IsCurrent:  chatID == currentChatID,
		}
		if channel == "agent" {
			normalized, ok := normalizeSubAgentRow(chat, previewStr)
			if !ok {
				continue
			}
			chat = normalized
		} else if isInteractiveSubAgentTenant(channel, chatID) {
			continue
		}
		result = append(result, chat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenants: %w", err)
	}
	return result, nil
}

type cliDirSessionsFile struct {
	Dir      string              `json:"dir"`
	Sessions []cliDirSessionFile `json:"sessions"`
}

type cliDirSessionFile struct {
	Name      string    `json:"name"`
	ChatID    string    `json:"chat_id"`
	CreatedAt time.Time `json:"created_at"`
}

func listCLIChatSessions(db *sql.DB, currentChatID string) ([]web.UserChatWithPreview, error) {
	tenantRows, err := listTenantsByChannel(db, "cli", currentChatID)
	if err != nil {
		return nil, err
	}
	tenantByChatID := make(map[string]web.UserChatWithPreview, len(tenantRows))
	for _, row := range tenantRows {
		tenantByChatID[row.ChatID] = row
	}

	localRows, err := listCLISessionsFromLocalStore(currentChatID, tenantByChatID)
	if err != nil {
		log.WithError(err).Warn("Failed to list CLI sessions from local store; falling back to tenant list")
		return tenantRows, nil
	}
	if len(localRows) == 0 {
		return tenantRows, nil
	}
	merged := make([]web.UserChatWithPreview, 0, len(localRows)+len(tenantRows))
	seen := make(map[string]bool, len(localRows))
	for _, row := range localRows {
		merged = append(merged, row)
		seen[row.ChatID] = true
	}
	for _, row := range tenantRows {
		if seen[row.ChatID] || isInteractiveSubAgentTenant("cli", row.ChatID) {
			continue
		}
		merged = append(merged, row)
	}
	sortUserChats(merged)
	return merged, nil
}

func listCLISessionsFromLocalStore(currentChatID string, tenantByChatID map[string]web.UserChatWithPreview) ([]web.UserChatWithPreview, error) {
	paths, err := filepath.Glob(filepath.Join(config.XbotHome(), "sessions", "*.json"))
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var rows []web.UserChatWithPreview
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var file cliDirSessionsFile
		if err := json.Unmarshal(data, &file); err != nil {
			continue
		}
		if shouldSkipWebCLISessionDir(file.Dir) {
			continue
		}
		for _, sess := range file.Sessions {
			if sess.ChatID == "" || seen[sess.ChatID] || isInteractiveSubAgentTenant("cli", sess.ChatID) {
				continue
			}
			seen[sess.ChatID] = true
			row := web.UserChatWithPreview{
				ChatID:     sess.ChatID,
				Channel:    "cli",
				Label:      displayLabelForCLILocalSession(sess, file.Dir),
				LastActive: sess.CreatedAt.Format(time.RFC3339),
				IsCurrent:  sess.ChatID == currentChatID,
			}
			if tenant, ok := tenantByChatID[sess.ChatID]; ok {
				row.LastActive = tenant.LastActive
				row.Preview = tenant.Preview
				row.IsCurrent = tenant.IsCurrent
				if tenant.Label != "" {
					row.Label = tenant.Label
				}
			}
			rows = append(rows, row)
		}
	}
	sortUserChats(rows)
	return rows, nil
}

func shouldSkipWebCLISessionDir(dir string) bool {
	if dir == "" {
		return true
	}
	// Skip Go test temp directories across platforms:
	//   Linux:   /tmp/Test<Name>/001
	//   macOS:   /var/folders/.../T/Test<Name>/001
	// Both contain a path segment starting with "Test" inside the system temp.
	return strings.Contains(dir, "/T/Test") || strings.Contains(dir, "/tmp/Test")
}

func displayLabelForCLILocalSession(sess cliDirSessionFile, dir string) string {
	if sess.Name != "" && sess.Name != "default" {
		return sess.Name
	}
	if base := filepath.Base(strings.TrimRight(dir, string(filepath.Separator))); base != "" && base != "." {
		return base
	}
	if dir != "" {
		return dir
	}
	return sess.ChatID
}

func sortUserChats(rows []web.UserChatWithPreview) {
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].LastActive > rows[j].LastActive
	})
}

func parseTenantTime(raw string) time.Time {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04:05.999999999 -0700 MST",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	if idx := strings.Index(raw, " m="); idx > 0 {
		return parseTenantTime(raw[:idx])
	}
	log.WithField("raw", raw).Warn("Failed to parse last_active_at in listTenantsByChannel")
	return time.Time{}
}

func buildSessionTree(mains, subagents []web.UserChatWithPreview) web.SessionTreeResult {
	builder := newSessionTreeBuilder(len(mains))
	allSubagents := make([]web.UserChatWithPreview, 0, len(subagents))
	allSubagents = append(allSubagents, subagents...)
	for _, main := range mains {
		if rowLooksLikeSubAgent(main) {
			allSubagents = append(allSubagents, main)
			continue
		}
		builder.addMain(main)
	}
	normalized := make([]web.UserChatWithPreview, 0, len(allSubagents))
	for _, sa := range allSubagents {
		row, ok := normalizeSubAgentRow(sa, sa.Preview)
		if !ok {
			continue
		}
		normalized = append(normalized, row)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return agentTreeDepth(normalized[i]) < agentTreeDepth(normalized[j])
	})
	var orphans []web.UserChatWithPreview
	for _, sa := range normalized {
		sa.Channel = "agent"
		sa.Type = "agent"
		if !builder.attachAgent(sa) {
			orphans = append(orphans, sa)
		}
	}
	return web.SessionTreeResult{Sessions: builder.nodes, OrphanSubAgents: orphans}
}

func agentTreeDepth(row web.UserChatWithPreview) int {
	if row.ParentChannel == "agent" && row.ParentChatID != "" {
		parent := row.ParentChatID
		depth := 1
		for depth < 32 {
			info, ok := parseAgentTenantChatID(parent)
			if !ok {
				return depth
			}
			depth++
			if info.parentChannel != "agent" {
				return depth
			}
			parent = info.parentChatID
		}
		return depth
	}
	key := row.FullKey
	if key == "" {
		key = row.ChatID
	}
	depth := 0
	for depth < 32 {
		info, ok := parseAgentTenantChatID(key)
		if !ok {
			return depth
		}
		depth++
		if info.parentChannel != "agent" {
			return depth
		}
		key = info.parentChatID
	}
	return depth
}

func rowLooksLikeSubAgent(row web.UserChatWithPreview) bool {
	if row.Channel == "agent" || row.Type == "agent" || row.Type == "subagent" {
		return true
	}
	if row.ParentChannel != "" || row.ParentChatID != "" || row.Role != "" || row.Instance != "" {
		return true
	}
	fullKey := row.FullKey
	if fullKey == "" {
		fullKey = row.ChatID
	}
	_, ok := parseAgentTenantChatID(fullKey)
	return ok
}

func canSynthesizeSessionTreeParent(sa web.UserChatWithPreview) bool {
	if sa.ParentChannel == "" || sa.ParentChatID == "" {
		return false
	}
	if sa.ParentChannel == "cli" {
		return looksLikeCLIChatID(sa.ParentChatID)
	}
	return sa.ParentChannel == "web"
}

func synthesizedParentRow(sa web.UserChatWithPreview) web.UserChatWithPreview {
	label := displayLabelForTenant(sa.ParentChannel, sa.ParentChatID, "")
	return web.UserChatWithPreview{
		ChatID:     sa.ParentChatID,
		Channel:    sa.ParentChannel,
		Label:      label,
		LastActive: sa.LastActive,
		Preview:    "",
		Type:       "main",
		Synthetic:  true,
	}
}

type sessionTreeNodeRef struct {
	channel   string
	chatID    string
	mainIndex int
	childPath []int
}

type sessionTreeBuilder struct {
	nodes             []web.SessionTreeNode
	aliases           map[string]sessionTreeNodeRef
	ambiguousAliases  map[string]bool
	fallbackAliases   map[string]sessionTreeNodeRef
	ambiguousFallback map[string]bool
}

func newSessionTreeBuilder(capacity int) *sessionTreeBuilder {
	return &sessionTreeBuilder{
		nodes:             make([]web.SessionTreeNode, 0, capacity),
		aliases:           make(map[string]sessionTreeNodeRef, capacity*2),
		ambiguousAliases:  make(map[string]bool),
		fallbackAliases:   make(map[string]sessionTreeNodeRef),
		ambiguousFallback: make(map[string]bool),
	}
}

func (b *sessionTreeBuilder) addMain(main web.UserChatWithPreview) *web.UserChatWithPreview {
	main.Type = "main"
	if main.Channel == "" {
		main.Channel = "web"
	}
	b.nodes = append(b.nodes, web.SessionTreeNode{UserChatWithPreview: main})
	idx := len(b.nodes) - 1
	b.indexMain(idx)
	return &b.nodes[idx].UserChatWithPreview
}

func (b *sessionTreeBuilder) indexMain(idx int) {
	node := &b.nodes[idx].UserChatWithPreview
	ref := sessionTreeNodeRef{channel: node.Channel, chatID: node.ChatID, mainIndex: idx}
	b.setAlias(sessionTreeKey(node.Channel, node.ChatID), ref)
	for _, key := range sessionTreeFallbackKeys(node.Channel, node.ChatID) {
		if existing, ok := b.fallbackAliases[key]; ok && !sameSessionTreeRef(existing, ref) {
			b.ambiguousFallback[key] = true
			continue
		}
		b.fallbackAliases[key] = ref
	}
}

func (b *sessionTreeBuilder) attachAgent(agent web.UserChatWithPreview) bool {
	parent, ok := b.resolveParent(agent)
	if !ok {
		return false
	}
	parentNode := b.nodeForRef(parent)
	if parentNode == nil {
		return false
	}
	agent.ParentChannel = parent.channel
	agent.ParentChatID = parent.chatID
	parentNode.Children = append(parentNode.Children, agent)
	b.indexAgent(parent, len(parentNode.Children)-1)
	return true
}

func (b *sessionTreeBuilder) indexAgent(parent sessionTreeNodeRef, childIndex int) {
	node := b.nodeForRef(parent)
	if node == nil || childIndex < 0 || childIndex >= len(node.Children) {
		return
	}
	child := &node.Children[childIndex]
	path := append([]int(nil), parent.childPath...)
	path = append(path, childIndex)
	ref := sessionTreeNodeRef{channel: "agent", chatID: child.ChatID, mainIndex: parent.mainIndex, childPath: path}
	b.setAlias(sessionTreeKey("agent", child.ChatID), ref)
	if child.FullKey != "" {
		b.setAlias(sessionTreeKey("agent", child.FullKey), ref)
	}
}

func (b *sessionTreeBuilder) setAlias(key string, ref sessionTreeNodeRef) {
	if existing, ok := b.aliases[key]; ok && !sameSessionTreeRef(existing, ref) {
		b.ambiguousAliases[key] = true
		return
	}
	b.aliases[key] = ref
}

func (b *sessionTreeBuilder) resolveParent(agent web.UserChatWithPreview) (sessionTreeNodeRef, bool) {
	if agent.ParentChannel == "agent" {
		return b.resolveAgentParent(agent.ParentChatID, agent.LastActive)
	}
	if parent, ok := b.lookupMainParent(agent.ParentChannel, agent.ParentChatID); ok {
		return parent, true
	}
	if !canSynthesizeSessionTreeParent(agent) {
		return sessionTreeNodeRef{}, false
	}
	parent := b.addMain(synthesizedParentRow(agent))
	return sessionTreeNodeRef{channel: parent.Channel, chatID: parent.ChatID, mainIndex: len(b.nodes) - 1}, true
}

func (b *sessionTreeBuilder) resolveAgentParent(agentChatID, lastActive string) (sessionTreeNodeRef, bool) {
	if parent, ok := b.lookupAgent(agentChatID); ok {
		return parent, true
	}
	parent, ok := normalizeSubAgentRow(web.UserChatWithPreview{
		ChatID:     agentChatID,
		Channel:    "agent",
		LastActive: lastActive,
		Synthetic:  true,
	}, "")
	if !ok {
		return sessionTreeNodeRef{}, false
	}
	parent.Synthetic = true
	if !b.attachAgent(parent) {
		return sessionTreeNodeRef{}, false
	}
	return b.lookupAgent(agentChatID)
}

func (b *sessionTreeBuilder) lookupAgent(chatID string) (sessionTreeNodeRef, bool) {
	key := sessionTreeKey("agent", chatID)
	if b.ambiguousAliases[key] {
		return sessionTreeNodeRef{}, false
	}
	if ref, ok := b.aliases[key]; ok {
		return ref, true
	}
	return sessionTreeNodeRef{}, false
}

func (b *sessionTreeBuilder) lookupMainParent(channel, chatID string) (sessionTreeNodeRef, bool) {
	key := sessionTreeKey(channel, chatID)
	if !b.ambiguousAliases[key] {
		if ref, ok := b.aliases[key]; ok {
			return ref, true
		}
	}
	if b.ambiguousAliases[key] {
		return sessionTreeNodeRef{}, false
	}
	explicitChannel, explicitChatID, hasExplicit := splitQualifiedSessionKey(chatID)
	if hasExplicit {
		explicitKey := sessionTreeKey(explicitChannel, explicitChatID)
		if b.ambiguousAliases[explicitKey] {
			return sessionTreeNodeRef{}, false
		}
		if ref, ok := b.aliases[explicitKey]; ok {
			return ref, true
		}
		channel = explicitChannel
		chatID = explicitChatID
	}
	for _, key := range sessionTreeFallbackKeys(channel, chatID) {
		if b.ambiguousFallback[key] {
			continue
		}
		if ref, ok := b.fallbackAliases[key]; ok {
			return ref, true
		}
	}
	return sessionTreeNodeRef{}, false
}

func (b *sessionTreeBuilder) nodeForRef(ref sessionTreeNodeRef) *web.UserChatWithPreview {
	if ref.mainIndex < 0 || ref.mainIndex >= len(b.nodes) {
		return nil
	}
	node := &b.nodes[ref.mainIndex].UserChatWithPreview
	for _, idx := range ref.childPath {
		if idx < 0 || idx >= len(node.Children) {
			return nil
		}
		node = &node.Children[idx]
	}
	return node
}

func sameSessionTreeRef(a, b sessionTreeNodeRef) bool {
	if a.channel != b.channel || a.chatID != b.chatID || a.mainIndex != b.mainIndex || len(a.childPath) != len(b.childPath) {
		return false
	}
	for i := range a.childPath {
		if a.childPath[i] != b.childPath[i] {
			return false
		}
	}
	return true
}

func looksLikeCLIChatID(chatID string) bool {
	workDir, name := parseCLIChatID(chatID)
	return looksLikeWorkDir(workDir) || name != "default"
}

func sessionTreeFallbackKeys(channel, chatID string) []string {
	if channel == "" {
		channel = "web"
	}
	keys := []string{}
	if channel == "cli" {
		name := cliSessionNameFromChatID(chatID)
		if name != "" && name != "default" {
			keys = append(keys, sessionTreeKey(channel, name))
		}
	}
	return keys
}

func cliSessionNameFromChatID(chatID string) string {
	idx := strings.LastIndex(chatID, ":")
	if idx <= 0 || idx == len(chatID)-1 {
		return chatID
	}
	return chatID[idx+1:]
}

func splitQualifiedSessionKey(value string) (channel, chatID string, ok bool) {
	idx := strings.Index(value, ":")
	if idx <= 0 || idx == len(value)-1 {
		return "", "", false
	}
	channel = value[:idx]
	for _, r := range channel {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return "", "", false
		}
	}
	return channel, value[idx+1:], true
}

func sessionTreeKey(channel, chatID string) string {
	if channel == "" {
		channel = "web"
	}
	return channel + ":" + chatID
}

type agentTenantInfo struct {
	parentChannel string
	parentChatID  string
	role          string
	instance      string
}

func normalizeSubAgentRow(row web.UserChatWithPreview, preview string) (web.UserChatWithPreview, bool) {
	fullKey := row.FullKey
	if fullKey == "" {
		fullKey = row.ChatID
	}
	explicitParentChannel := row.ParentChannel
	explicitParentChatID := row.ParentChatID
	info, ok := parseAgentTenantChatID(fullKey)
	if !ok {
		return web.UserChatWithPreview{}, false
	}
	row.Channel = "agent"
	row.Type = "agent"
	row.ChatID = fullKey
	row.FullKey = fullKey
	row.Role = info.role
	row.Instance = info.instance
	row.ParentChannel = info.parentChannel
	row.ParentChatID = info.parentChatID
	if explicitParentChannel == "agent" && explicitParentChatID != "" {
		row.ParentChannel = explicitParentChannel
		row.ParentChatID = explicitParentChatID
	}
	row.Historical = !row.Running
	row.Label = subAgentDisplayLabel(row.Role, row.Instance, preview)
	return row, true
}

func subAgentRowKey(row web.UserChatWithPreview) string {
	if row.FullKey != "" {
		return row.FullKey
	}
	return row.ChatID
}

func upsertSubAgentRow(rows []web.UserChatWithPreview, seen map[string]bool, parentChannel string, s agent.InteractiveSessionInfo) []web.UserChatWithPreview {
	row := subAgentRowFromInteractiveSession(parentChannel, s)
	key := subAgentRowKey(row)
	if seen[key] {
		for i := range rows {
			if subAgentRowKey(rows[i]) == key {
				lastActive := rows[i].LastActive
				rows[i] = row
				rows[i].LastActive = lastActive
				break
			}
		}
		return rows
	}
	seen[key] = true
	return append(rows, row)
}

func subAgentRowFromInteractiveSession(parentChannel string, s agent.InteractiveSessionInfo) web.UserChatWithPreview {
	chatID := s.Key
	if chatID == "" {
		chatID = fmt.Sprintf("%s:%s/%s", parentChannel, s.ChatID, s.Role)
		if s.Instance != "" {
			chatID += ":" + s.Instance
		}
	}
	row, ok := normalizeSubAgentRow(web.UserChatWithPreview{
		ChatID:     chatID,
		FullKey:    chatID,
		Channel:    "agent",
		Label:      subAgentDisplayLabel(s.Role, s.Instance, s.Preview),
		LastActive: time.Now().Format(time.RFC3339),
		Preview:    s.Preview,
		Running:    s.Running,
	}, s.Preview)
	if !ok {
		return web.UserChatWithPreview{}
	}
	row.Running = s.Running
	row.Historical = false
	if s.ParentChannel != "" && s.ParentChatID != "" {
		row.ParentChannel = s.ParentChannel
		row.ParentChatID = s.ParentChatID
	}
	return row
}

func subAgentRowBelongsToAllowedWebParent(row web.UserChatWithPreview, allowedWebParents map[string]bool) bool {
	parentChannel := row.ParentChannel
	parentChatID := row.ParentChatID
	for depth := 0; depth < 32; depth++ {
		if parentChannel == "web" {
			return allowedWebParents[parentChatID]
		}
		if parentChannel != "agent" {
			return false
		}
		info, ok := parseAgentTenantChatID(parentChatID)
		if !ok {
			return false
		}
		parentChannel = info.parentChannel
		parentChatID = info.parentChatID
	}
	return false
}

func parseAgentTenantChatID(chatID string) (agentTenantInfo, bool) {
	slash := strings.LastIndex(chatID, "/")
	if slash <= 0 || slash == len(chatID)-1 {
		return agentTenantInfo{}, false
	}
	parent := chatID[:slash]
	roleInstance := chatID[slash+1:]
	colon := strings.LastIndex(roleInstance, ":")
	role := roleInstance
	instance := ""
	if colon > 0 && colon < len(roleInstance)-1 {
		role = roleInstance[:colon]
		instance = roleInstance[colon+1:]
	}
	channelSep := strings.Index(parent, ":")
	if channelSep <= 0 || channelSep == len(parent)-1 {
		return agentTenantInfo{}, false
	}
	return agentTenantInfo{
		parentChannel: parent[:channelSep],
		parentChatID:  parent[channelSep+1:],
		role:          role,
		instance:      instance,
	}, true
}

func subAgentDisplayLabel(role, instance, _ string) string {
	base := role
	if base == "" {
		base = instance
	}
	if base == "" {
		base = "SubAgent"
	}
	if instance != "" {
		return base + "/" + instance
	}
	return base
}

func isInteractiveSubAgentTenant(channel, chatID string) bool {
	if channel == "agent" || strings.HasPrefix(chatID, "agent:") {
		return true
	}
	if _, ok := parseAgentTenantChatID(chatID); ok {
		return true
	}
	prefix := channel + ":"
	return strings.HasPrefix(chatID, prefix) && strings.Contains(chatID[len(prefix):], "/")
}

func displayLabelForTenant(channel, chatID, label string) string {
	if label != "" {
		return label
	}
	if channel != "cli" {
		return chatID
	}
	workDir, name := parseCLIChatID(chatID)
	if name == "" || name == "default" {
		if base := filepath.Base(strings.TrimRight(workDir, string(filepath.Separator))); base != "" && base != "." {
			return base
		}
		return workDir
	}
	return name
}

func parseCLIChatID(chatID string) (workDir, sessionName string) {
	idx := strings.LastIndex(chatID, ":")
	if idx <= 0 || idx == len(chatID)-1 {
		return chatID, "default"
	}
	prefix := chatID[:idx]
	suffix := chatID[idx+1:]
	if !looksLikeWorkDir(prefix) {
		return chatID, "default"
	}
	return prefix, suffix
}

func looksLikeWorkDir(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "~") {
		return true
	}
	return len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/')
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

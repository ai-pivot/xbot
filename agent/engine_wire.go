package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xbot/agent/hooks"
	"xbot/bus"
	channelpkg "xbot/channel"
	"xbot/channel/cli"
	"xbot/config"
	"xbot/llm"
	log "xbot/logger"
	"xbot/memory"
	"xbot/memory/letta"
	"xbot/oauth"
	"xbot/protocol"
	"xbot/session"
	"xbot/tools"
)

// todoManagerAdapter wraps tools.TodoManager to implement TodoManagerProvider.
type todoManagerAdapter struct {
	mgr *tools.TodoManager
}

func (a *todoManagerAdapter) GetTodoSummary(sessionKey string) string {
	return a.mgr.GetTodoSummary(sessionKey)
}

func (a *todoManagerAdapter) GetTodoItems(sessionKey string) []TodoProgressItem {
	items := a.mgr.GetTodos(sessionKey)
	result := make([]TodoProgressItem, len(items))
	for i, item := range items {
		result[i] = TodoProgressItem{ID: item.ID, Text: item.Text, Done: item.Done}
	}
	return result
}

func (a *todoManagerAdapter) ClearTodos(sessionKey string) {
	a.mgr.SetTodos(sessionKey, nil)
}

// applyUserMaxContext 如果模型订阅中设置了 MaxContext，
// 创建一个新的 ContextManagerConfig 副本覆盖 MaxContextTokens，
// 避免污染 Agent 级别的原始配置（含 sync.RWMutex）。
// 模型级别的 MaxContext 优先级高于全局 MaxContextTokens：
//   - userMaxCtx > 0 → 直接使用模型配置的值
//   - userMaxCtx == 0 → 回退到全局 base.MaxContextTokens
func applyUserMaxContext(base *ContextManagerConfig, userMaxCtx int) *ContextManagerConfig {
	if base == nil {
		return nil
	}
	effective := base.MaxContextTokens
	if userMaxCtx > 0 {
		effective = userMaxCtx
	}
	if effective <= 0 {
		return base
	}
	if effective == base.MaxContextTokens {
		return base
	}
	return &ContextManagerConfig{
		MaxContextTokens:     effective,
		CompressionThreshold: base.CompressionThreshold,
		DefaultMode:          base.DefaultMode,
	}
}

// buildBaseRunConfig 构建主 Agent（main/cron）共用的基础 RunConfig。
// 包含 LLM、身份、工作区、工具执行器、循环控制、HookManager 等公共字段。
// 返回 (RunConfig, userMaxContext) — userMaxContext 为用户在 Settings 中设置的值，0 表示未设置。
func (a *Agent) buildBaseRunConfig(
	ctx context.Context,
	channel, chatID, senderID string,
	messages []llm.ChatMessage,
	senderName string,
	sandboxUserID string,
) (RunConfig, int) {
	sessionKey := qualifyChatID(channel, chatID)

	// All user-related info is resolved once at processMessage entry and
	// carried via context. No direct LLMFactory/SettingsService access here.
	userCtx := UserContextFromContext(ctx)

	llmClient := userCtx.LLMClient
	model := userCtx.Model
	userMaxCtx := userCtx.MaxContextTokens
	thinkingMode := userCtx.ThinkingMode
	maxOutputTokens := userCtx.MaxOutputTokens

	// LLM 并发限流回调（per-tenant）— resolved by middleware
	llmSemAcquire := userCtx.LLMSemAcquire
	subAgentSem := userCtx.SubAgentSem

	return RunConfig{
		// 必需
		LLMClient:    llmClient,
		Model:        model,
		ThinkingMode: thinkingMode,
		Tools:        a.tools,
		Messages:     messages,

		// 身份
		AgentID: "main",
		Channel: channel,
		ChatID:  chatID,
		SubID:   userCtx.SubID,
		SessionName: func() string {
			_, name := cli.ParseChatID(chatID)
			// Override with DB label if available (e.g. renamed from "Agent-xxx" to a custom name).
			// This ensures the rename reminder doesn't fire for already-renamed sessions.
			if a.multiSession != nil {
				if db := a.multiSession.DB(); db != nil {
					var label string
					if err := db.Conn().QueryRow(
						"SELECT label FROM user_chats WHERE channel = ? AND chat_id = ? AND label != '' LIMIT 1",
						channel, chatID,
					).Scan(&label); err == nil && label != "" {
						name = label
					}
				}
			}
			return name
		}(),
		SenderID:     senderID,      // 直接调用者 = 原始用户（用于消息路由 + settings/usage 存储 key）
		OriginUserID: sandboxUserID, // 沙箱/工作区用户（飞书身份登录 web 时为飞书 ou_xxx）
		SenderName:   senderName,

		// 工作区 & 沙箱
		WorkingDir:       a.workDir,
		WorkspaceRoot:    a.workspaceRoot(sandboxUserID),
		ReadOnlyRoots:    a.globalSkillDirs,
		SkillsDirs:       a.globalSkillDirs,
		AgentsDir:        a.agentsDir,
		MCPConfigPath:    tools.UserMCPConfigPath(a.workDir, sandboxUserID),
		GlobalMCPConfig:  filepath.Join(a.xbotHome, "mcp.json"),
		DataDir:          a.workDir,
		SandboxEnabled:   userCtx.SandboxMode != "none",
		PreferredSandbox: userCtx.SandboxMode,
		Sandbox:          userCtx.Sandbox,
		SandboxMode:      userCtx.SandboxMode,
		InitialCWD:       a.workDir, // absolute-resolved at buildToolContext time

		// 循环控制
		MaxIterations:   a.getMaxIterations(),
		MaxOutputTokens: maxOutputTokens,

		// Auto worktree: read via GetEffectiveSetting — the single correct
		// read path for user-scoped settings. Same source as /settings panel.
		AutoWorktreeEnabled: userCtx.GetSettingBool("auto_worktree"),

		// Session
		SessionKey: sessionKey,

		// 发送
		SendFunc:      a.sendMessage,
		InjectInbound: a.injectInbound,

		// 工具执行
		ToolExecutor: a.buildToolExecutor(ctx, channel, chatID, senderID, senderName, sandboxUserID, ""),

		// 读写分离（主 Agent 始终启用）
		EnableReadWriteSplit: true,

		// SessionFinalSent 回调
		SessionFinalSentCallback: func() bool {
			_, sent := a.sessionFinalSent.Load(sessionKey)
			return sent
		},

		// Letta 记忆字段
		ToolContextExtras: a.buildToolContextExtras(channel, chatID),

		// Memory provider — set so buildOutput populates out.Messages for
		// auto-memorize, and CompressionAware hooks fire during compaction.
		Memory: func() memory.MemoryProvider {
			ts, err := a.multiSession.GetOrCreateSession(channel, chatID)
			if err != nil {
				return nil
			}
			return ts.Memory()
		}(),

		// HookManager — inherit from Agent
		HookManager: a.hookManager,

		// PluginManager — inherit from Agent
		PluginManager: a.pluginMgr,

		// PermUsers — from UserContext (resolved at processMessage entry)
		PermUsers: userCtx.PermUsers,

		// TUI/Config callbacks — inherit from Agent (CLI local mode)
		TUICtrlFn:    a.tuiCtrlFn,
		ConfigGetFn:  a.configGetFn,
		ConfigSetFn:  a.configSetFn,
		ChatRenameFn: a.renameSession,

		// Remote TUI control — detect RemoteCLIChannel and inject WS-based callback
		RemoteTUICtrlFn: a.buildRemoteTUICtrlFn(channel, chatID),

		// Subscription listing — from LLMFactory
		ListLLMSubs: a.listLLMSubsFn(userCtx),

		// Subscription read/write — from LLMFactory (for config tool)
		GetActiveSubFieldFn: a.getActiveSubFieldFn(userCtx, channel, chatID),
		UpdateActiveSubFn:   a.updateActiveSubFn(userCtx, channel),

		// LLM 并发限流回调（per-tenant）
		LLMSemAcquire:             llmSemAcquire,
		EnableConcurrentSubAgents: true,
		SubAgentSem:               subAgentSem,
	}, userMaxCtx
}

// buildMainRunConfig 为主 Agent 构建完整的 RunConfig。
// 从 processMessage / handleCardResponse 调用。
func (a *Agent) buildMainRunConfig(
	ctx context.Context,
	msg bus.InboundMessage,
	messages []llm.ChatMessage,
	tenantSession *session.TenantSession,
	autoNotify bool,
) RunConfig {
	channel, chatID, senderID, senderName := msg.Channel, msg.ChatID, msg.SenderID, msg.SenderName
	sessionKey := qualifyChatID(channel, chatID)

	// 飞书身份登录 web 时，用飞书用户 ID 作为沙箱用户 ID，
	// 确保 web 端与飞书端共用同一个 Docker 容器和工作区。
	feishuUserID := msg.Metadata["feishu_user_id"]
	sandboxUserID := senderID
	if feishuUserID != "" {
		sandboxUserID = feishuUserID
	}

	cfg, userMaxCtx := a.buildBaseRunConfig(ctx, channel, chatID, senderID, messages, senderName, sandboxUserID)

	// TurnID is assigned by chatProcessLoop (per-session monotonic counter) and
	// carried via msg.Metadata. Propagate to RunConfig so every progress event
	// and the final reply carry it for frontend association.
	if raw := msg.Metadata["turn_id"]; raw != "" {
		if tid, err := strconv.ParseUint(raw, 10, 64); err == nil {
			cfg.TurnID = tid
		} else {
			log.WithFields(log.Fields{"raw": raw}).Warn("buildMainRunConfig: failed to parse turn_id from metadata")
		}
	}

	// 可观测性：把 session / user / turn 标识传给 Run，使每次 LLM HTTP 请求
	// 携带 X-Session-Id / X-User-Id / X-Turn-Id（X-Request-Id 在 generateResponse
	// 每次调用生成），对标 Codex / Claude Code，便于在 LLM 提供商侧按会话追踪
	// 请求、定位接口问题。
	cfg.Observability = llm.Observability{
		SessionID: sessionKey,
		UserID:    senderID,
		TurnID:    int64(cfg.TurnID),
	}

	// Track the current iteration per session so stream callbacks can stamp it
	// on stream_content events. The frontend uses the iteration to clear the
	// previous iteration's content/tools when a new iteration begins with only
	// stream events (structured boundary event may be coalesced/lost in SSE).
	//
	// CRITICAL: use the ORIGIN key (qualifyChatID(channel, chatID)), NOT the
	// physical sessionKey (which may be overridden to physicalChannel:chatID
	// at line 267 for web-users-browsing-CLI-sessions). buildStreamCallbacks
	// computes progressKey = qualifyChatID(channel, chatID) and reads
	// activeIteration via getActiveIteration(progressKey) — if OnIterationChange
	// writes to the physical key while getActiveIteration reads the origin key,
	// they mismatch and getActiveIteration returns the stale initial value (1)
	// on every iteration.
	cfg.OnIterationChange = func(iteration int) {
		originKey := qualifyChatID(channel, chatID)
		if state, ok := a.bgSessionStates.Load(originKey); ok {
			state.(*bgSessionState).activeIteration.Store(int64(iteration))
		}
	}

	// physical_channel: the channel the user is actually connected through.
	// When a web user browses a CLI-created session, msg.Channel is "cli"
	// (the session's origin channel), but the user is on "web". Channel-scoped
	// tools (like display_html) must resolve against the physical channel,
	// not the session origin.
	physicalChannel := msg.Metadata["physical_channel"]
	if physicalChannel != "" && physicalChannel != channel {
		// Override SessionKey so AsDefinitionsForSession/GetForSession find
		// channel-scoped tools registered under the physical channel.
		sessionKey = physicalChannel + ":" + chatID
		cfg.SessionKey = sessionKey
		// RootSessionKey must use the CANONICAL session key (origin channel),
		// NOT the physical channel. Offload data is session-scoped — it must
		// be stored and recalled under the same key regardless of whether the
		// session is accessed via web or CLI. Using the physical key would
		// split offload data across web:/cli: directories for the same session.
		cfg.RootSessionKey = qualifyChatID(channel, chatID)
		// Rebuild ToolExecutor with the physical channel so tool EXECUTION
		// also resolves channel-scoped tools correctly.
		cfg.ToolExecutor = a.buildToolExecutor(ctx, channel, chatID, senderID, senderName, sandboxUserID, physicalChannel)
	}

	// Identity is resolved at processMessage entry and carried via ctx.
	// ResolveUserContext already preferred metadata injection (from the
	// channel boundary) over re-resolving via (channel, senderID).
	userCtx := UserContextFromContext(ctx)
	cfg.UserID = userCtx.UserID
	cfg.Role = userCtx.Role
	if cfg.UserID == 0 {
		cfg.UserID = 1 // standalone mode default
	}
	if cfg.Role == "" {
		cfg.Role = "user" // safe default
	}

	// 从 tenant session 获取租户 ID，用于 per-tenant 工具可见性
	cfg.TenantID = tenantSession.TenantID()

	// Use session CWD for InitialCWD (may differ from a.workDir for worktree sessions).
	// AutoDetectAndInit in buildPrompt already set the correct CWD on tenantSession.
	if cwd := tenantSession.GetCurrentDir(); cwd != "" {
		cfg.InitialCWD = cwd
	}

	// Wire peer message injection for inter-session communication.
	cfg.PeerMessageFn = a.injectPeerMessage

	// 保留 FeishuUserID 供 buildToolContext 等处使用
	cfg.FeishuUserID = feishuUserID

	// 主 Agent 特有字段
	cfg.Session = tenantSession

	// Token 状态持久化：Run() 结束后写入 DB，重启后恢复
	if extras := cfg.ToolContextExtras; extras != nil && extras.MemorySvc != nil && extras.TenantID != 0 {
		memSvc := extras.MemorySvc
		tenantID := extras.TenantID
		cfg.SaveTokenState = func(promptTokens, completionTokens int64) {
			if err := memSvc.SetTokenState(context.Background(), tenantID, promptTokens, completionTokens); err != nil {
				log.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to persist token state")
			}
		}
		// Per-message exact token accounting: after each LLM API call,
		// write the API's prompt_tokens to the most recent user message.
		// Rewind reads this value to restore accurate token state without estimation.
		cfg.SaveContextTokens = func(promptTokens int64) {
			if err := tenantSession.SaveContextTokens(promptTokens); err != nil {
				log.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to save context tokens")
			}
		}
	}
	// SaveStreamStats: persist stream timing stats to session-level storage
	// (survives turn end, unlike lastProgressSnapshot which is deleted).
	progressKey := qualifyChatID(channel, chatID)
	cfg.SaveStreamStats = func(stats *protocol.StreamStats) {
		if stats == nil {
			return
		}
		a.lastStreamStats.Store(progressKey, stats)
	}

	// OAuth 处理
	cfg.OAuthHandler = a.buildOAuthHandler(channel, chatID, senderID, sessionKey)

	// Structured progress has one channel-agnostic snapshot/log producer.
	// Every ProgressSender receives the exact same protocol event; channel
	// transports only handle delivery and never derive semantic history.
	// Set up BEFORE ProgressNotifier so we can detect structured availability.
	if handler := a.buildProgressEventHandler(chatID, channel); handler != nil {
		done := ctx.Done()
		cfg.ProgressEventHandler = func(ev *ProgressEvent) {
			select {
			case <-done:
				// PhaseDone is the authoritative final snapshot and must survive
				// cancellation for every channel.
				if ev != nil && ev.Structured != nil && ev.Structured.Phase == PhaseDone {
					handler(ev)
				}
				return
			default:
			}
			handler(ev)
		}
	}

	// ProgressNotifier sends text-based progress as a regular message. It is
	// enabled by CHANNEL CAPABILITY (autoNotify = PreReplyNotifier — Feishu
	// patches the existing message with progress text, QQ sends separate
	// messages), NOT by the absence of a ProgressEventHandler. Every channel
	// now has a ProgressEventHandler (needed for /su viewing + PhaseDone), so
	// keying on `ProgressEventHandler == nil` silently disabled text progress
	// for all channels. CLI/Web have structured progress (ProgressSender) —
	// autoNotify=false there, so no text pollution.
	if autoNotify {
		cfg.ProgressNotifier = func(lines []string, _ string) {
			if len(lines) > 0 {
				if err := a.sendMessage(channel, chatID, lines[0]); err != nil {
					log.Warn("Failed to send progress: ", err)
				}
			}
		}
	} else {
		cfg.ProgressNotifier = func(lines []string, _ string) {}
	}

	// 注入 ContextManager
	cfg.ContextManager = a.GetContextManager()
	cfg.ContextManagerConfig = applyUserMaxContext(a.contextManagerConfig, userMaxCtx)

	// After Cd changes session CWD, refresh all plugin contexts so script plugins
	// (e.g. git-info) re-execute in the new directory.
	if a.pluginMgr != nil {
		cfg.RefreshPluginWorkDir = func(dir, channel, chatID string, tenantID int64) {
			a.pluginMgr.RefreshWorkDir(dir, channel, chatID, tenantID)
		}
	}

	// Per-user token usage tracking (persisted to SQLite)
	cfg.RecordUserTokenUsage = func(senderID, model string, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount int) {
		if err := a.multiSession.RecordUserTokenUsage(senderID, model, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount); err != nil {
			log.WithError(err).WithField("sender_id", senderID).Warn("Failed to record user token usage")
		}
	}

	// SpawnAgent（主 Agent 可以创建 SubAgent）
	// ctx already carries UserContext — SubAgent inherits it via context.
	cfg.SpawnAgent = func(ctx context.Context, inMsg bus.InboundMessage) (*channelpkg.OutboundMsg, error) {
		return a.spawnSubAgent(ctx, inMsg)
	}

	// OffloadStore — Layer 1 offload
	cfg.OffloadStore = a.offloadStore

	// MaskStore — Observation Masking（默认开启，可通过 settings 的 enable_masking 关闭）
	cfg.MaskStore = a.maskStore
	streamDisabled := false
	if userCtx.GetSetting("enable_masking") == "false" {
		cfg.MaskStore = nil
	}
	if userCtx.GetSetting("enable_stream") == "false" {
		streamDisabled = true
	}

	// Stream — default ON for all channels; wire callbacks per channel type.
	if !streamDisabled {
		cfg.Stream = true
		if a.channelFinder != nil {
			var progressSeq atomic.Uint64
			cfg.ProgressSeq = &progressSeq
			cfg.StreamContentFunc, cfg.StreamReasoningFunc, cfg.StreamToolCallFunc, cfg.StreamUsageFunc, cfg.ResetStreamTiming = a.buildStreamCallbacks(chatID, channel, &progressSeq, cfg.TurnID, cfg.SessionKey, cfg.TenantID)
		}
	}

	// ContextEditor — Context Editing（精确编辑上下文）
	if a.contextEditor != nil {
		cfg.ContextEditor = NewContextEditor(a.contextEditor.Store)
	}

	// TodoManager — TODO 状态查询
	if a.todoManager != nil {
		cfg.TodoManager = &todoManagerAdapter{mgr: a.todoManager}
	}

	// InteractiveCallbacks — interactive SubAgent 支持
	cfg.InteractiveCallbacks = &InteractiveCallbacks{
		SpawnFn: func(ctx context.Context, roleName string, msg bus.InboundMessage) (*channelpkg.OutboundMsg, error) {
			return a.SpawnInteractiveSession(ctx, roleName, msg)
		},
		SendFn: a.SendToInteractiveSession,
		UnloadFn: func(ctx context.Context, roleName, instance string) error {
			return a.UnloadInteractiveSession(ctx, roleName, channel, chatID, instance)
		},
		InterruptFn: func(ctx context.Context, roleName, instance string) error {
			return a.InterruptInteractiveSession(ctx, roleName, channel, chatID, instance)
		},
		InspectFn: func(ctx context.Context, roleName, instance string, tail int) (string, error) {
			return a.InspectInteractiveSession(ctx, roleName, channel, chatID, instance, tail)
		},
		ListActiveFn: func(ch, cid string) []SubAgentStatus {
			return interactiveSessionsToStatuses(a.ListInteractiveSessions(ch, cid))
		},
	}

	// Memory tools for compaction — allows the compaction LLM to archive
	// important context into core/archival memory before it gets compacted away.
	// Uses the real tool registry instead of hand-written execution logic.
	if defs, exec := a.buildMemoryToolSetup(channel, chatID); defs != nil {
		cfg.MemoryToolDefs = defs
		cfg.MemoryToolExec = exec
	}

	return cfg
}

// filterSubAgentTools 根据白名单过滤子 Agent 工具集。
// 以下工具永久可用，不受白名单限制：
//   - SubAgent（如果 caps.SpawnAgent=true）
//   - offload_recall、recall_masked（SubAgent 需要访问父 Agent 的 offload/mask 数据）
//   - SendMessage、CreateChat（interactive SubAgent 群聊/agent 间通信必需）
func filterSubAgentTools(subTools *tools.Registry, allowedTools []string, caps tools.SubAgentCapabilities, interactive bool) {
	if len(allowedTools) == 0 {
		return
	}
	allowed := make(map[string]bool, len(allowedTools))
	for _, name := range allowedTools {
		allowed[name] = true
	}
	for _, tool := range subTools.List() {
		toolName := tool.Name()
		// SubAgent 工具：如果 SpawnAgent=true，始终保留
		if toolName == "SubAgent" && caps.SpawnAgent {
			continue
		}
		// offload_recall / recall_masked：SubAgent 始终可用
		if toolName == "offload_recall" || toolName == "recall_masked" {
			continue
		}
		// SendMessage / CreateChat：interactive SubAgent 始终可用（群聊通信）
		if interactive && (toolName == "SendMessage" || toolName == "CreateChat") {
			continue
		}
		if !allowed[toolName] {
			subTools.Unregister(toolName)
		}
	}
}

// resolveSubAgentCWD 解析子 Agent 的当前工作目录。
// 继承父 Agent 的 CWD，无则默认 workDir。同时检测 worktree 隔离。
func resolveSubAgentCWD(parentCtx *tools.ToolContext, workDir string) (cwd string, newWorkDir string, isWorktreeIsolated bool) {
	cwd = parentCtx.CurrentDir
	if cwd == "" {
		cwd = workDir
	}
	isWorktreeIsolated = parentCtx.IsWorktreeIsolated
	if strings.Contains(cwd, ".xbot-worktrees") {
		newWorkDir = cwd
		isWorktreeIsolated = true
	} else {
		newWorkDir = workDir
	}
	return
}

// buildSubAgentRunConfig 为 SubAgent 构建 RunConfig。
// SubAgent 使用独立工具集、无 session、有压缩（独立 ContextManager）、无进度通知。
// Phase 2: SubAgent 通过 RunConfig 继承父 Agent 的工作区配置，
// 使用统一的 defaultToolExecutor + buildToolContext 构建 ToolContext。
func (a *Agent) buildSubAgentRunConfig(
	ctx context.Context,
	parentCtx *tools.ToolContext,
	task string,
	systemPrompt string,
	allowedTools []string,
	caps tools.SubAgentCapabilities,
	roleName string,
	interactive bool,
	instance string,
	model string, // 可选：角色指定的模型，为空时继承主 Agent
) RunConfig {
	parentAgentID := parentCtx.AgentID

	// Extract UserContext from context — set by SpawnAgent callback.
	// This is the SINGLE source of user info for SubAgent construction.
	// No direct access to LLMFactory/SettingsService/IdentityResolver below.
	userCtx := UserContextFromContext(ctx)

	// Interactive SubAgent 默认拥有 send_message 能力（群聊/agent 间通信必需）
	if interactive {
		caps.SendMessage = true
	}

	if systemPrompt == "" {
		systemPrompt = "You are a helpful assistant. Complete the given task using the available tools."
	}

	// 子 Agent 工具集：根据 capabilities 决定是否保留 SubAgent 工具
	subTools := a.tools.Clone()
	if !caps.SpawnAgent {
		subTools.Unregister("SubAgent")
	}
	// AskUser 依赖 channel adapter 渲染交互 UI（TUI 面板/飞书卡片），
	// 但 SubAgent 运行在 "agent" channel 上，不在 AskUser.SupportedChannels 中。
	// 即使工具被执行，WaitingUser 信号也会被 RunSubAgent/SpawnInteractive 丢弃，
	// 导致 SubAgent 静默挂起（空回复 + idle 状态，用户从未看到问题）。
	// 无条件移除，避免静默失效。
	subTools.Unregister("AskUser")

	// 如果指定了工具白名单，只保留白名单中的工具
	filterSubAgentTools(subTools, allowedTools, caps, interactive)

	// 构建 SubAgent 的 system prompt：通用模板 + 角色专有能力描述
	// parentCtx.WorkspaceRoot 在 remote 模式下为空（buildToolContext 清空了宿主机路径），
	// 回退到 a.workDir 确保提示词中始终包含正确的工作目录。
	workDir := parentCtx.WorkspaceRoot
	if workDir == "" {
		workDir = a.workDir
	}
	if parentCtx.Sandbox != nil && parentCtx.Sandbox.Name() != "none" {
		workDir = parentCtx.Sandbox.Workspace(parentCtx.OriginUserID)
	}
	now := time.Now().Format("2006-01-02 15:04:05 MST")

	// CWD 继承父 Agent 的当前目录，无则默认 workDir
	cwd, workDir, isWorktreeIsolated := resolveSubAgentCWD(parentCtx, workDir)
	cwdPart := "\n- 当前目录：" + cwd

	// role.SystemPrompt 作为角色专有能力描述（非通用 prompt）
	rolePrompt := strings.TrimSpace(systemPrompt)
	if rolePrompt == "" {
		rolePrompt = "You are a helpful assistant. Complete the given task using the available tools."
	}

	// 通用模板 + 角色描述（有白名单时使用精简模板）
	var sysPrompt string
	if len(allowedTools) > 0 {
		sysPrompt = fmt.Sprintf(subagentSystemPromptTemplateConcise, workDir, cwdPart, roleName, parentAgentID, now)
	} else {
		sysPrompt = fmt.Sprintf(subagentSystemPromptTemplate, workDir, cwdPart, roleName, parentAgentID, now)
	}
	if interactive {
		sysPrompt += subagentExecutionModeInteractive
	} else {
		sysPrompt += subagentExecutionModeOneShot
	}
	sysPrompt += "\n## 角色描述\n\n" + rolePrompt + "\n"

	// 注入群组信息（当前 agent 是某个虚拟群组的成员）
	if parentCtx.GroupID != "" && len(parentCtx.GroupMembers) > 0 {
		sysPrompt += "\n## 群组协作\n\n"
		sysPrompt += fmt.Sprintf("你是虚拟群组 **%s** 的成员。群组成员：\n", parentCtx.GroupID)
		for _, m := range parentCtx.GroupMembers {
			sysPrompt += fmt.Sprintf("- %s\n", m)
		}
		sysPrompt += "\n你可以使用 **SendMessage** 工具直接向群组中的其他成员发送消息：\n"
		sysPrompt += "- `SendMessage(to=\"agent:角色/实例\", message=\"...\")` → 直接发送消息给该成员\n"
		sysPrompt += "- `SendMessage(to=\"" + parentCtx.GroupID + "\", message=\"...\")` → 广播发给所有成员\n"
		sysPrompt += "- `SendMessage(to=\"" + parentCtx.GroupID + "\", message=\"@agent:角色/实例 ...\")` → @提及特定成员\n"
		sysPrompt += "\n**注意**：你只能向同组成员发消息，不能跨群组通信。群组通信是直接的——消息会进入对方的 session，他们能看到完整的上下文并自行判断如何回应。\n"
	}

	// 注入可用 agent 目录（只在 spawn_agent=true 时注入）
	if caps.SpawnAgent {
		if agentsCatalog := a.agents.GetAgentsCatalog(ctx, parentCtx.SenderID, workDir); agentsCatalog != "" {
			sysPrompt += "\n" + agentsCatalog
		}
	}

	// 注入 skills 目录（SubAgent 可使用 Skill 工具加载 skill）
	originUserID := parentCtx.OriginUserID
	if originUserID == "" {
		originUserID = parentCtx.SenderID
	}
	if skillsCatalog := a.skills.GetSkillsCatalog(ctx, originUserID, workDir); skillsCatalog != "" {
		sysPrompt += "\n" + skillsCatalog
	}

	// Pre-compute parentExtras once (shared between Phase 4 and buildSubAgentMemory)
	parentExtras := a.buildToolContextExtras(parentCtx.Channel, parentCtx.ChatID)

	// Phase 4: Inject project context from AGENTS.md
	// Check resolved workDir first (includes sandbox path), then a.workDir
	// (host path, needed in remote mode where sandbox clears workDir).
	if projectCtx := LoadProjectContextFile(workDir); projectCtx != "" {
		sysPrompt += projectCtx
	} else if projectCtx := LoadProjectContextFile(a.workDir); projectCtx != "" {
		sysPrompt += projectCtx
	} else if projectCtx := LoadProjectContextFile(cwd); projectCtx != "" {
		// Fallback: check CWD if neither workspace root has AGENTS.md
		sysPrompt += projectCtx
	}

	// Phase 5: Inject user language preference into SubAgent prompt.
	// Only inject if not already present in the inherited system prompt
	// (LanguageMiddleware on the main Agent already adds it via SystemParts).
	if lang := userCtx.GetSetting("language"); lang != "" {
		if !strings.Contains(sysPrompt, "## Language") {
			sysPrompt += "\n" + LanguageInstruction(lang)
		}
	}

	messages := []llm.ChatMessage{
		llm.NewSystemMessage(sysPrompt),
		llm.NewUserMessage(task),
	}

	subAgentID := parentAgentID + "/" + roleName

	// SubAgent LLM resolution — via UserContext, no direct LLMFactory access.
	llmClient, subModel, userMaxCtx, thinkingMode, maxOutputTokens, subID := userCtx.ResolveLLMForModelWithFallback(model)

	// Stream — default ON; inherit from parent config unless explicitly disabled.
	stream := userCtx.GetSetting("enable_stream") != "false"

	cfg := RunConfig{
		LLMClient:       llmClient,
		Model:           subModel,
		SubID:           subID,
		ThinkingMode:    thinkingMode,
		Stream:          stream,
		MaxOutputTokens: maxOutputTokens,
		Tools:           subTools,
		Messages:        messages,
		AgentID:         subAgentID,
		Channel:         parentCtx.Channel,
		ChatID:          parentCtx.ChatID,
		SenderID:        parentAgentID, // SubAgent: 直接调用者 = 父 Agent
		OriginUserID:    originUserID,  // SubAgent: 继承原始用户 ID

		// 从父 Agent 继承工作区 & 沙箱配置
		WorkingDir:       parentCtx.WorkingDir,
		WorkspaceRoot:    parentCtx.WorkspaceRoot,
		ReadOnlyRoots:    parentCtx.ReadOnlyRoots,
		SkillsDirs:       parentCtx.SkillsDirs,
		AgentsDir:        parentCtx.AgentsDir,
		MCPConfigPath:    parentCtx.MCPConfigPath,
		GlobalMCPConfig:  parentCtx.GlobalMCPConfigPath,
		DataDir:          parentCtx.DataDir,
		SandboxEnabled:   parentCtx.Sandbox != nil && parentCtx.Sandbox.Name() != "none",
		PreferredSandbox: parentCtx.PreferredSandbox,
		Sandbox:          parentCtx.Sandbox,
		SandboxMode: func() string {
			if parentCtx.Sandbox != nil {
				return parentCtx.Sandbox.Name()
			}
			return "none"
		}(),
		// 继承父 Agent 的 CWD。remote 模式下 parentCtx.CurrentDir 可能为空
		//（buildToolContext 清空了宿主机路径，且 session 未存过 CWD），
		// 回退到 a.workDir 确保子 Agent 有正确的初始目录。
		InitialCWD: func() string {
			if parentCtx.CurrentDir != "" {
				return parentCtx.CurrentDir
			}
			return a.workDir
		}(),
		InitialGroupID:      parentCtx.GroupID,
		InitialGroupMembers: parentCtx.GroupMembers,

		// Worktree isolation: if the parent is in a worktree, rewrite
		// WorkspaceRoot to the worktree path and enable isolation.
		// (Already computed above before system prompt construction.)
		IsWorktreeIsolated: isWorktreeIsolated,

		MaxIterations: a.getMaxIterations(), // 继承主 Agent 配置
		// SubAgent 不设独立超时，直接使用父 context 携带的 deadline

		// LLM 并发限流：继承父 Agent 的 per-tenant 信号量
		LLMSemAcquire: userCtx.LLMSemAcquire,

		// SubAgent 如果能 spawn 子 Agent，也启用并行执行
		EnableConcurrentSubAgents: caps.SpawnAgent,
		SubAgentSem:               userCtx.SubAgentSem,

		// ToolExecutor = nil → 使用 defaultToolExecutor（统一 buildToolContext）
	}

	// If the SubAgent's CWD is inside a worktree directory,
	// rewrite WorkspaceRoot to the worktree path for path isolation.
	// (workDir was already rewritten above before system prompt construction.)
	if isWorktreeIsolated {
		cfg.WorkspaceRoot = workDir
	}

	// Per-user token usage tracking：SubAgent 的 token 消耗归属原始用户
	cfg.RecordUserTokenUsage = func(senderID, model string, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount int) {
		if err := a.multiSession.RecordUserTokenUsage(originUserID, model, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"sender_id":    originUserID,
				"sub_agent_id": subAgentID,
			}).Warn("Failed to record SubAgent token usage")
		}
	}

	// 独立 sessionKey：使用 subAgentID 确保与父 Agent 隔离，
	// 避免工具激活、OffloadStore、MaskStore 等按 sessionKey 索引的数据污染。
	cfg.SessionKey = subAgentID

	// RootSessionKey：记录顶层 Agent（主 Agent）的 session key，
	// 用于 offload_recall 等需要访问父 session 数据的场景（如 SubAgent 回忆父 Agent 的 offload 数据）。
	rootKey := parentCtx.RootSessionKey
	if rootKey == "" {
		rootKey = parentCtx.Channel + ":" + parentCtx.ChatID
	}
	cfg.RootSessionKey = rootKey

	// === Context Mask 统一机制：注入 6 个缺失字段 ===
	// SubAgent 与主 Agent 共享同一 Run() 循环，context mask（offload/mask/context-edit）
	// 依赖这些字段才能正确触发。之前缺失导致 SubAgent 上下文压缩/遮罩永不生效。

	// 1. ContextManager：创建独立实例（不共享父 Agent 的触发器，避免计数交叉）
	//    从 caps.Memory 条件中移出，所有 SubAgent 都需要压缩能力。
	if a.contextManagerConfig != nil {
		cmCfg := applyUserMaxContext(a.contextManagerConfig, userMaxCtx)
		cfg.ContextManager = newPhase1Manager(cmCfg)
		cfg.ContextManagerConfig = cmCfg
	}

	// 2. OffloadStore：共享父 Agent 实例（按 sessionKey 隔离，完全安全）
	cfg.OffloadStore = a.offloadStore

	// 3. MaskStore：共享父 Agent 实例（通过随机 ID 查找，容量共享但 SubAgent 生命周期短影响可忽略）
	cfg.MaskStore = a.maskStore

	// 4. ContextEditor：创建独立实例（每个 Agent 需要自己的 messages 引用和编辑历史）
	cfg.ContextEditor = NewContextEditor(NewContextEditStore(100))

	// Capability: send_message — 允许 SubAgent 向 IM 渠道发送消息
	if caps.SendMessage {
		cfg.SendFunc = a.sendMessage
	}

	// Capability: memory — 创建独立记忆系统
	// SubAgent 的会话 = 与调用者 Agent 的私有聊天。调用者是 "user"，SubAgent 是 "xbot"。
	// 通过 deriveSubAgentTenantID 隔离：每个 (parentTenantID, parentAgentID, roleName) 组合
	// 产生唯一的 tenantID，确保 SubAgent 和父 Agent 读写完全不同的记忆数据。
	if caps.Memory {
		extras, mem := a.buildSubAgentMemory(ctx, parentCtx, parentExtras, parentAgentID, roleName)
		if extras != nil && mem != nil {
			cfg.ToolContextExtras = extras
			cfg.Memory = mem

			// 注入记忆使用指南到 system prompt（根据 provider 类型选择）
			messages[0].Content += a.subagentMemorySection()

			// 注入记忆到 system prompt（SubAgent 不使用 pipeline，需手动调用 Recall）
			memCtx := ctx
			// letta 模式需要 WithUserID
			if _, ok := mem.(*letta.LettaMemory); ok {
				subSenderID := subAgentHumanBlockSenderID(parentAgentID)
				memCtx = letta.WithUserID(ctx, subSenderID)
			}
			if recallText, err := mem.Recall(memCtx, task); err == nil && recallText != "" {
				messages[0].Content += "\n\n" + recallText
			}

		}
	} else {
		// 无 memory 能力时，移除记忆工具，避免 SubAgent 尝试调用后失败
		subTools.Unregister("core_memory_append")
		subTools.Unregister("core_memory_replace")
		subTools.Unregister("rethink")
		subTools.Unregister("archival_memory_insert")
		subTools.Unregister("archival_memory_search")
		subTools.Unregister("recall_memory_search")
		subTools.Unregister("memory_search")
		subTools.Unregister("memory_add")
		subTools.Unregister("memory_manage")
	}

	// Capability: spawn_agent — 允许 SubAgent 创建子 Agent
	if caps.SpawnAgent {
		cfg.SpawnAgent = func(ctx context.Context, msg bus.InboundMessage) (*channelpkg.OutboundMsg, error) {
			return a.spawnSubAgent(ctx, msg)
		}
	}
	// HookManager — SubAgent does NOT inherit the parent Agent's hook manager.
	// Goal continuation (PreTurnEndHook) is a main-Agent-only feature.
	// If SubAgent inherits it, the goal hook fires during SubAgent execution,
	// injecting goal-continuation prompts into SubAgent turns — causing
	// SubAgents to never end naturally and loop on the goal prompt.
	// SubAgent still gets plugin hooks via PluginManager (separate system).
	cfg.HookManager = nil
	cfg.PluginManager = a.pluginMgr

	// SaveTokenState: persist token counts so GetTokenState RPC returns
	// correct values when the TUI switches to a SubAgent session.
	// Without this, the context bar shows 0 (empty) for SubAgent sessions.
	if extras := cfg.ToolContextExtras; extras != nil && extras.MemorySvc != nil && extras.TenantID != 0 {
		memSvc := extras.MemorySvc
		tenantID := extras.TenantID
		cfg.SaveTokenState = func(promptTokens, completionTokens int64) {
			if err := memSvc.SetTokenState(context.Background(), tenantID, promptTokens, completionTokens); err != nil {
				log.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to persist subagent token state")
			}
		}
	}

	// TUI/Config callbacks for tool execution (needed by tui_control/config tools)
	cfg.TUICtrlFn = a.tuiCtrlFn
	cfg.RemoteTUICtrlFn = a.buildRemoteTUICtrlFn(parentCtx.Channel, parentCtx.ChatID)
	cfg.ChatRenameFn = a.renameSession
	cfg.MessageSender = a.messageSender
	cfg.RegisterAgentChannel = a.registerAgentChannel
	cfg.UnregisterAgentChannel = a.unregisterAgentChannel

	// Interactive 回调独立注入，不依赖 SpawnAgent
	cfg.InteractiveCallbacks = &InteractiveCallbacks{
		SpawnFn: a.SpawnInteractiveSession,
		SendFn:  a.SendToInteractiveSession,
		UnloadFn: func(ctx context.Context, roleName, instance string) error {
			return a.UnloadInteractiveSession(ctx, roleName, parentCtx.Channel, parentCtx.ChatID, instance)
		},
		InterruptFn: func(ctx context.Context, roleName, instance string) error {
			return a.InterruptInteractiveSession(ctx, roleName, parentCtx.Channel, parentCtx.ChatID, instance)
		},
		InspectFn: func(ctx context.Context, roleName, instance string, tail int) (string, error) {
			return a.InspectInteractiveSession(ctx, roleName, parentCtx.Channel, parentCtx.ChatID, instance, tail)
		},
		ListActiveFn: func(ch, cid string) []SubAgentStatus {
			return interactiveSessionsToStatuses(a.ListInteractiveSessions(ch, cid))
		},
	}

	return cfg
}

// buildToolExecutor 构建主 Agent 的工具执行器。
// 包含 session MCP 查找、激活检查、工具使用追踪等完整逻辑。
// 这是主 Agent 和 Cron 使用的执行器，SubAgent 使用 defaultToolExecutor。
func (a *Agent) buildToolExecutor(ctx context.Context, channel, chatID, senderID, senderName, sandboxUserID string, physicalChannel string) func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
	userCtx := UserContextFromContext(ctx)
	// If physicalChannel is set (web user browsing CLI session), use it for
	// channel-scoped tool resolution. Otherwise fall back to the session's origin channel.
	sessionKey := qualifyChatID(channel, chatID)
	if physicalChannel != "" && physicalChannel != channel {
		sessionKey = physicalChannel + ":" + chatID
	}

	// Pre-build RunConfig outside closure to avoid reallocating on every tool call.
	// Only ctx (from the caller) changes per-call; all config fields are stable.
	wsRoot := a.workspaceRoot(sandboxUserID)
	isRemote := a.isRemoteUser(sandboxUserID)
	// For remote users, leave WorkspaceRoot/WorkingDir empty — the runner
	// manages its own filesystem. Keep SkillsDirs/AgentsDir as host paths
	// for server-side sync (EnsureSynced reads global skills from host).
	var workspaceRoot, workingDir string
	if !isRemote {
		workspaceRoot = wsRoot
		workingDir = a.workDir
	}
	cfg := &RunConfig{
		AgentID:        "main",
		Channel:        channel,
		ChatID:         chatID,
		SenderID:       senderID,      // 主 Agent: 直接调用者（用于消息路由）
		OriginUserID:   sandboxUserID, // 沙箱/工作区用户（飞书身份登录 web 时为飞书 ou_xxx）
		SenderName:     senderName,
		SendFunc:       a.sendMessage,
		RootSessionKey: qualifyChatID(channel, chatID), // canonical session key for offload_recall
		// SessionKey must carry the physicalChannel override (computed above)
		// so ToolContext.SessionKey matches the runState's s.sessionKey that
		// refreshStructuredTodos reads. Without this, TodoWrite writes to
		// "cli:chatID" (ToolContext fallback) while refreshStructuredTodos reads
		// "web:chatID" (overridden cfg.SessionKey) when a web user browses a
		// CLI-created session → todos never appear in the progress stream.
		SessionKey: sessionKey,

		WorkingDir:             workingDir,
		WorkspaceRoot:          workspaceRoot,
		ReadOnlyRoots:          a.globalSkillDirs,
		SkillsDirs:             a.globalSkillDirs,
		AgentsDir:              a.agentsDir,
		MCPConfigPath:          tools.UserMCPConfigPath(a.workDir, sandboxUserID),
		GlobalMCPConfig:        filepath.Join(a.xbotHome, "mcp.json"),
		DataDir:                a.workDir,
		SandboxEnabled:         a.sandboxMode != "none",
		PreferredSandbox:       a.sandboxMode,
		Sandbox:                resolveSandbox(a.sandbox, sandboxUserID),
		SandboxRouter:          a.sandbox, // raw router for per-tool-call re-resolution
		SandboxMode:            a.sandboxMode,
		InjectInbound:          a.injectInbound,
		Tools:                  a.tools,
		BgTaskManager:          a.bgTaskMgr,
		MessageSender:          a.messageSender,
		RegisterAgentChannel:   a.registerAgentChannel,
		UnregisterAgentChannel: a.unregisterAgentChannel,
	}

	cfg.SpawnAgent = func(spawnCtx context.Context, inMsg bus.InboundMessage) (*channelpkg.OutboundMsg, error) {
		return a.spawnSubAgent(spawnCtx, inMsg)
	}

	cfg.InteractiveCallbacks = &InteractiveCallbacks{
		SpawnFn: a.SpawnInteractiveSession,
		SendFn:  a.SendToInteractiveSession,
		UnloadFn: func(ctx context.Context, roleName, instance string) error {
			return a.UnloadInteractiveSession(ctx, roleName, channel, chatID, instance)
		},
		InterruptFn: func(ctx context.Context, roleName, instance string) error {
			return a.InterruptInteractiveSession(ctx, roleName, channel, chatID, instance)
		},
		InspectFn: func(ctx context.Context, roleName, instance string, tail int) (string, error) {
			return a.InspectInteractiveSession(ctx, roleName, channel, chatID, instance, tail)
		},
		ListActiveFn: func(ch, cid string) []SubAgentStatus {
			return interactiveSessionsToStatuses(a.ListInteractiveSessions(ch, cid))
		},
	}

	// Pre-build Letta memory extras (involves GetOrCreateSession + LettaMemory lookup).
	cfg.ToolContextExtras = a.buildToolContextExtras(channel, chatID)

	// Wire peer message injection for inter-session communication.
	cfg.PeerMessageFn = a.injectPeerMessage

	// Inherit hook manager from Agent.
	cfg.HookManager = a.hookManager
	cfg.PluginManager = a.pluginMgr

	// TUI/Config callbacks for tool execution (needed by tui_control/config tools)
	cfg.TUICtrlFn = a.tuiCtrlFn
	cfg.RemoteTUICtrlFn = a.buildRemoteTUICtrlFn(channel, chatID)
	cfg.ChatRenameFn = a.renameSession
	cfg.ListLLMSubs = a.listLLMSubsFn(userCtx)
	cfg.GetActiveSubFieldFn = a.getActiveSubFieldFn(userCtx, channel, chatID)
	cfg.UpdateActiveSubFn = a.updateActiveSubFn(userCtx, channel)

	var sessionOnce sync.Once

	return func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
		// Lazy-inject session so buildToolContext can persist CWD across tool calls.
		// Without this, Cd stores CWD in a ToolContext that is discarded on next call.
		// Use sync.Once to prevent concurrent goroutines from racing on cfg.Session.
		sessionOnce.Do(func() {
			if cfg.Session == nil {
				if sess, err := a.multiSession.GetOrCreateSession(channel, chatID); err == nil {
					cfg.Session = sess
				}
			}
		})

		// 1. 工具查找：session MCP 优先，然后全局注册表
		var tool tools.Tool
		ok := false

		if mcpMgr := a.multiSession.GetSessionMCPManager(sessionKey); mcpMgr != nil {
			for _, st := range mcpMgr.GetSessionTools() {
				if st.Name() == tc.Name {
					tool = st
					ok = true
					break
				}
			}
		}
		if !ok {
			// Unified lookup: channel-scoped → tenant → global
			tenantID := int64(0)
			if cfg.Session != nil {
				tenantID = cfg.Session.TenantID()
			}
			tool, ok = a.tools.GetForSession(tc.Name, tenantID, sessionKey)
		}
		if !ok {
			return nil, fmt.Errorf("unknown tool: %s", tc.Name)
		}

		// 2. 确保用户工作目录存在（remote 模式跳过，runner 自行管理文件系统）
		if !a.isRemoteUser(senderID) {
			if err := os.MkdirAll(wsRoot, 0o755); err != nil {
				return nil, fmt.Errorf("create user workspace: %w", err)
			}
		}

		// Re-resolve sandbox per tool call — picks up runner switches immediately
		if router, ok := cfg.SandboxRouter.(*tools.SandboxRouter); ok {
			cfg.Sandbox = router.SandboxForSession(
				cfg.Channel+":"+cfg.ChatID,
				cfg.OriginUserID,
			)
		}

		toolExecCtx := withApprovalTarget(ctx, cfg.ChatID, cfg.OriginUserID)
		if cfg.PermUsers != nil {
			toolExecCtx = tools.WithPermUsers(toolExecCtx, cfg.PermUsers.DefaultUser, cfg.PermUsers.PrivilegedUser)
		}

		// 5. 构建 ToolContext（统一路径，只有 ctx 变化）
		toolCtx := buildToolContext(toolExecCtx, cfg)

		// 6-8. Execute with hooks (shared implementation — same as defaultToolExecutor)
		return executeWithHooks(cfg.HookManager, toolExecCtx, toolCtx, tc.Name, tc.Arguments, tool, hooks.BasePayload{
			SessionID: cfg.ChatID,
			Channel:   cfg.Channel,
			SenderID:  cfg.OriginUserID,
			ChatID:    cfg.ChatID,
		})
	}
}

// buildOAuthHandler 构建 OAuth 自动触发处理器。
func (a *Agent) buildOAuthHandler(channel, chatID, senderID, sessionKey string) func(ctx context.Context, tc llm.ToolCall, execErr error) (string, bool) {
	return func(ctx context.Context, tc llm.ToolCall, execErr error) (string, bool) {
		if !oauth.IsTokenNeededError(execErr) {
			return "", false
		}

		// 已触发过则跳过，避免重复 OAuth 状态
		if _, sent := a.sessionFinalSent.Load(sessionKey); sent {
			log.Ctx(ctx).WithFields(log.Fields{
				"tool":   tc.Name,
				"reason": "sessionFinalSent already set, skipping duplicate oauth_authorize",
			}).Info("Skip duplicate OAuth auto-trigger")
			return "OAuth authorization already in progress.", true
		}

		log.Ctx(ctx).WithFields(log.Fields{
			"tool": tc.Name,
		}).Info("OAuth token needed, auto-triggering oauth_authorize tool")

		oauthTool, ok := a.tools.Get("oauth_authorize")
		if !ok {
			return "OAuth authorization required but oauth_authorize tool not found. Please enable OAuth in configuration.", true
		}

		oauthInput := fmt.Sprintf(`{"provider": "feishu", "reason": "needed to access %s"}`, tc.Name)
		oauthCtx := &tools.ToolContext{
			Ctx:      ctx,
			Channel:  channel,
			ChatID:   chatID,
			SenderID: senderID,
			SendFunc: a.sendMessage,
		}
		oauthResult, oauthErr := oauthTool.Execute(oauthCtx, oauthInput)
		if oauthErr == nil && oauthResult != nil {
			a.sessionFinalSent.Store(sessionKey, true)
			return oauthResult.Summary, true
		}

		log.Ctx(ctx).WithError(oauthErr).Error("Failed to execute oauth_authorize tool")
		return "OAuth authorization required. Please configure OAUTH_ENABLE=true and OAUTH_BASE_URL in your environment.", true
	}
}

// buildMemoryToolSetup returns tool definitions and executor for memory tools during compaction.
// Uses the real tool registry instead of hand-written execution logic,
// ensuring tool behavior stays in sync with the main agent loop.
// Returns (nil, nil) if memory tools are not available.
func (a *Agent) buildMemoryToolSetup(channel, chatID string) ([]llm.ToolDefinition, func(ctx context.Context, tc llm.ToolCall) (string, error)) {
	extras := a.buildToolContextExtras(channel, chatID)
	if extras == nil || extras.CoreMemory == nil {
		return nil, nil
	}

	memToolNames := []string{
		"core_memory_append", "core_memory_replace", "rethink",
		"archival_memory_insert", "archival_memory_search",
	}
	var defs []llm.ToolDefinition
	for _, name := range memToolNames {
		if t, ok := a.tools.Get(name); ok {
			defs = append(defs, t)
		}
	}
	if len(defs) == 0 {
		return nil, nil
	}

	// Minimal RunConfig for building ToolContext — memory tools only need ToolContextExtras.
	memCfg := &RunConfig{
		Channel:           channel,
		ChatID:            chatID,
		ToolContextExtras: extras,
	}

	exec := func(ctx context.Context, tc llm.ToolCall) (string, error) {
		tool, ok := a.tools.Get(tc.Name)
		if !ok {
			return "Unknown tool: " + tc.Name, nil
		}
		toolCtx := buildToolContext(ctx, memCfg)
		result, err := tool.Execute(toolCtx, tc.Arguments)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), nil
		}
		return result.Summary, nil
	}

	return defs, exec
}

// buildToolContextExtras 构建 ToolContext 扩展字段。
// 通用字段（TenantID、MemorySvc、MemoryProvider）从 TenantSession 直接获取。
// LettaMemory 专属字段（CoreMemory、ArchivalMemory、ToolIndexer）通过类型断言设置。
// 新增 provider 无需修改此函数——工具通过 ctx.MemoryProvider 类型断言获取特有方法。
func (a *Agent) buildToolContextExtras(channel, chatID string) *ToolContextExtras {
	extras := &ToolContextExtras{
		InvalidateAllSessionMCP: func() { a.multiSession.InvalidateAll() },
	}

	ts, err := a.multiSession.GetOrCreateSession(channel, chatID)
	if err != nil {
		log.WithError(err).WithFields(log.Fields{
			"channel": channel,
			"chat_id": chatID,
		}).Warn("buildToolContextExtras: GetOrCreateSession failed, fields will be empty")
	} else {
		// Tenant-level fields: work for all memory provider types
		extras.TenantID = ts.TenantID()
		extras.MemorySvc = ts.MemoryService()
		extras.RecallTimeRange = a.multiSession.RecallTimeRangeFunc()
		// Generic: store the MemoryProvider instance. Tools type-assert to get
		// provider-specific methods (e.g. *xbotmemory.XbotMemory).
		extras.MemoryProvider = ts.Memory()

		// LettaMemory-specific fields (backward compat for existing letta tools)
		if lm, ok := ts.Memory().(*letta.LettaMemory); ok {
			extras.CoreMemory = lm.CoreService()
			extras.ArchivalMemory = lm.ArchivalService()
			extras.ToolIndexer = lm
		}
	}

	return extras
}

// buildSubAgentMemory 为 SubAgent 构建独立的记忆系统。
//
// 核心设计：SubAgent 的会话 = 与调用者 Agent 的私有聊天。
// 调用者是 "user"，SubAgent 是 "xbot"。这保持了高度一致的 agent 逻辑抽象。
//
// 隔离策略：
//   - tenantID: 通过 deriveSubAgentTenantID(parentTenantID, parentAgentID, roleName) 生成
//   - persona: 完全独立（SubAgent 自己的身份，不从父级继承）
//   - human: 通过 parentAgentID 隔离（记录调用者 agent 的特征，而非原始终端用户）
//   - archival memory / working_context: 通过 tenantID 自动隔离
//
// 返回 (ToolContextExtras, MemoryProvider)。如果创建失败，返回 nil, nil 并记录警告。
func (a *Agent) buildSubAgentMemory(
	ctx context.Context,
	parentCtx *tools.ToolContext,
	parentExtras *ToolContextExtras,
	parentAgentID, roleName string,
) (*ToolContextExtras, memory.MemoryProvider) {
	// 1. 获取父 Agent 的 tenantID（用于推导 SubAgent 的 tenantID）
	if parentExtras.TenantID == 0 {
		log.Ctx(ctx).WithField("parent", parentAgentID).Warn("SubAgent memory: parent tenantID is 0, skipping memory setup")
		return nil, nil
	}

	// 2. 推导 SubAgent 的独立 tenantID
	subTenantID := deriveSubAgentTenantID(parentExtras.TenantID, parentAgentID, roleName)

	// 3. 通过注册表创建 SubAgent 记忆系统（无硬编码 provider 名称）
	//    记忆按 owner user_id 共享（与父 Agent 同一 canonical user），
	//    SubAgent 的记忆归原始终端用户所有，跨会话可见。
	memDir := filepath.Join(config.XbotHome(), "memory", fmt.Sprintf("%d", subTenantID))
	deps := memory.ProviderDeps{
		TenantID: subTenantID,
		UserID:   parentCtx.UserID, // canonical owner — same user across all sessions
		BaseDir:  memDir,
		DB:       a.multiSession.DB().Conn(),
	}

	// Letta-specific deps
	if a.memoryProvider == "letta" || a.memoryProvider == "" {
		coreSvc := a.multiSession.CoreMemoryService()
		archivalSvc := a.multiSession.ArchivalService()
		memorySvc := a.multiSession.MemoryService()
		toolIndexSvc := a.multiSession.ToolIndexService()

		// Initialize SubAgent core memory blocks
		subSenderID := subAgentHumanBlockSenderID(parentAgentID)
		if err := coreSvc.InitBlocks(subTenantID, subSenderID); err != nil {
			log.Ctx(ctx).WithError(err).WithFields(log.Fields{
				"tenant_id":     subTenantID,
				"parent_agent":  parentAgentID,
				"role":          roleName,
				"sub_sender_id": subSenderID,
			}).Warn("SubAgent memory: failed to init core blocks")
			return nil, nil
		}

		deps.LettaDeps = &letta.Deps{
			CoreSvc:      coreSvc,
			ArchivalSvc:  archivalSvc,
			MemorySvc:    memorySvc,
			ToolIndexSvc: toolIndexSvc,
		}
	}

	providerName := a.memoryProvider
	if providerName == "" {
		providerName = "letta" // backward compat
	}
	if providerName == "none" || providerName == "flat" {
		// flat/none: no SubAgent memory
		return nil, nil
	}

	mem := memory.CreateProvider(providerName, deps)
	if mem == nil {
		log.Ctx(ctx).WithField("provider", providerName).Warn("SubAgent memory: provider not registered")
		return nil, nil
	}

	// 4. 构建 ToolContextExtras（供 SubAgent 的工具使用）
	extras := &ToolContextExtras{
		TenantID:                subTenantID,
		MemoryProvider:          mem, // generic: tools type-assert to get provider-specific methods
		InvalidateAllSessionMCP: func() { a.multiSession.InvalidateAll() },
	}

	// LettaMemory-specific fields (backward compat for existing letta tools)
	if lm, ok := mem.(*letta.LettaMemory); ok {
		extras.CoreMemory = lm.CoreService()
		extras.ArchivalMemory = lm.ArchivalService()
		extras.MemorySvc = a.multiSession.MemoryService()
		extras.RecallTimeRange = a.multiSession.RecallTimeRangeFunc()
		extras.ToolIndexer = lm
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"sub_tenant_id": subTenantID,
		"parent_agent":  parentAgentID,
		"role":          roleName,
		"provider":      providerName,
	}).Info("SubAgent memory: created memory system")

	return extras, mem
}

// subagentMemorySection returns the memory guide for SubAgent based on the provider type.
// Uses memory.GetPromptParts registry — no hardcoded provider names.
func (a *Agent) subagentMemorySection() string {
	parts := memory.GetPromptParts(a.memoryProvider)
	if parts.MemoryPrompt != "" {
		return parts.MemoryPrompt
	}
	if a.memoryProvider == "letta" || a.memoryProvider == "" {
		return subagentMemorySection // backward compat: letta's SubAgent guide
	}
	return "" // flat/none: no SubAgent memory guide
}

// subAgentHumanBlockSenderID returns the virtual senderID used for the SubAgent's
// human block. This isolates SubAgent's human block from the parent's by using
// parentAgentID as the key, so each SubAgent role sees a different "user".
func subAgentHumanBlockSenderID(parentAgentID string) string {
	return "agent:" + parentAgentID
}

// consolidateSubAgentMemory runs a lightweight memorize pass after SubAgent exits.
// It extracts key information from the SubAgent's conversation messages and
// persists them to the SubAgent's independent memory via Memorize().
func (a *Agent) consolidateSubAgentMemory(
	ctx context.Context,
	cfg RunConfig,
	messages []llm.ChatMessage,
	task string,
	roleName string,
	parentAgentID string,
) {
	mem := cfg.Memory
	extras := cfg.ToolContextExtras
	if mem == nil || extras == nil {
		return
	}

	// Build memorize input with all conversation messages and LLM client
	memInput := memory.MemorizeInput{
		Messages:  messages,
		LLMClient: cfg.LLMClient,
		Model:     cfg.Model,
	}

	// Call Memorize with the SubAgent's virtual senderID context
	subSenderID := subAgentHumanBlockSenderID(parentAgentID)
	memCtx := letta.WithUserID(ctx, subSenderID)

	if result, err := mem.Memorize(memCtx, memInput); err != nil {
		log.Ctx(ctx).WithError(err).WithFields(log.Fields{
			"role":      roleName,
			"tenant_id": extras.TenantID,
		}).Warn("SubAgent memory consolidation failed")
	} else if result.OK {
		GlobalMetrics.MemoryConsolidations.Add(1)
	}
}

// spawnSubAgent 通过 Run() 创建并运行 SubAgent。
// 这是 SpawnAgent 回调的实现，将 InboundMessage 转换为 RunConfig 并调用 Run()。
func (a *Agent) spawnSubAgent(ctx context.Context, msg bus.InboundMessage) (*channelpkg.OutboundMsg, error) {
	parentAgentID := msg.ParentAgentID
	task := msg.Content
	systemPrompt := msg.SystemPrompt
	allowedTools := msg.AllowedTools
	roleName := msg.RoleName
	instance := ""
	if msg.Metadata != nil {
		instance = msg.Metadata["instance_id"]
	}

	// --- CallChain 深度 & 循环检查 ---
	cc := CallChainFromContext(ctx)
	if roleName != "" {
		if err := cc.CanSpawn(roleName, a.maxSubAgentDepth); err != nil {
			log.Ctx(ctx).WithFields(log.Fields{
				"parent": parentAgentID,
				"role":   roleName,
				"chain":  cc.Chain,
			}).Warn("SubAgent spawn blocked by CallChain")
			return &channelpkg.OutboundMsg{
				Channel: "",
				ChatID:  "",
				Content: err.Error(),
				Error:   err,
			}, nil
		}
	}

	// 构建 parentCtx（从 InboundMessage 恢复）
	originChannel, originChatID, originSender := resolveOriginIDs(msg)
	parentCtx := a.buildParentToolContext(ctx, originChannel, originChatID, originSender, msg)
	oneshotInstance := fmt.Sprintf("oneshot-%s-%d", roleName, time.Now().UnixNano())
	oneshotKey := interactiveKey(originChannel, originChatID, roleName, oneshotInstance)

	log.Ctx(ctx).WithFields(log.Fields{
		"parent": parentAgentID,
		"role":   roleName,
		"task":   tools.Truncate(task, 80),
	}).Info("SubAgent started (via Run)")

	// 从 InboundMessage 恢复 capabilities
	caps := tools.CapabilitiesFromMap(msg.Capabilities)

	// 从 InboundMessage 元数据中获取角色指定的模型
	subModel := ""
	if msg.Metadata != nil {
		subModel = msg.Metadata["model"]
	}

	cfg := a.buildSubAgentRunConfig(ctx, parentCtx, task, systemPrompt, allowedTools, caps, roleName, false, instance, subModel)

	// SubAgent 进度上报：统一走穿透回调模式。
	// 顶层 agent（无 parent callback）创建 root callback，只渲染 depth=1（直接子 agent）的进度。
	// 深层子 agent 的进度通过穿透回调冒泡上来，但 depth>1 不发送到聊天窗口。
	myDepth := cc.Depth() + 1
	myPath := cc.Spawn(roleName).Chain

	// 确定当前层级使用的 parent callback（可能为 nil）
	parentCB, _ := SubAgentProgressFromContext(ctx)

	// 构建 subCtx：传递 CallChain + 穿透回调
	subCtx := WithCallChain(ctx, cc.Spawn(roleName))

	// 注入穿透回调到 subCtx，让更深层 SubAgent 递归上报进度到顶层
	// 穿透回调包装 parentCB，累加 depth 和 path
	subCtx = WithSubAgentProgress(subCtx, func(detail SubAgentProgressDetail) {
		detail.Depth = myDepth + detail.Depth
		if len(detail.Path) == 0 {
			detail.Path = myPath
		}
		if parentCB != nil {
			parentCB(detail)
		}
	})

	// 设置当前层级的 ProgressNotifier
	if parentCB != nil {
		// 非顶层：穿透进度到父 agent（由上面的穿透回调处理）
		cfg.ProgressNotifier = func(lines []string, thinking string) {
			if len(lines) > 0 {
				parentCB(SubAgentProgressDetail{
					Path:       myPath,
					Lines:      lines,
					Depth:      myDepth,
					Instance:   oneshotInstance,
					SessionKey: oneshotKey,
					Content:    thinking,
				})
			}
		}
	} else if originChannel != "" && originChatID != "" && a.wantsPreReplyNotify(originChannel) {
		// Channels without structured progress (Feishu, QQ): send text-based
		// SubAgent progress to the chat window. Channels with structured progress
		// (Web, CLI) handle SubAgent progress via ProgressSender events.
		rn := roleName
		cfg.ProgressNotifier = func(lines []string, _ string) {
			if len(lines) > 0 {
				last := lines[len(lines)-1]
				if idx := strings.LastIndex(last, "\n"); idx >= 0 {
					last = last[idx+1:]
				}
				prefixed := "📋 subagent: [" + rn + "] " + last + "\n"
				if err := a.sendMessage(originChannel, originChatID, prefixed); err != nil {
					log.Warn("Failed to send prefixed output: ", err)
				}
			}
		}
	} else {
		// Channels with structured progress (Web, CLI): set a dummy notifier so
		// autoNotify=true, which enables wireSubAgentProgress's ProgressEventHandler
		// to fire. The dummy itself sends nothing.
		cfg.ProgressNotifier = func(lines []string, _ string) {}
	}

	// Register one-shot subagent in interactiveSubAgents so it's visible
	// in the Ctrl+T panel. Kept after completion for history viewing; TTL cleans it up.
	oneshotIA := &interactiveAgent{
		roleName:   roleName,
		instance:   oneshotInstance,
		lastUsed:   time.Now(),
		running:    true,
		background: false,
		task:       task,
	}
	a.interactiveSubAgents.Store(oneshotKey, oneshotIA)

	// Create TenantSession for message persistence (same as interactive SubAgents).
	agentTenantSession, err := a.multiSession.GetOrCreateSessionWithOwner("agent", oneshotKey, cfg.UserID)
	if err != nil {
		a.interactiveSubAgents.Delete(oneshotKey)
		return nil, fmt.Errorf("create oneshot agent tenant session: %w", err)
	}
	cfg.Session = agentTenantSession
	operationGate := a.sessionOperationGate("agent", oneshotKey)
	if !operationGate.lock(subCtx) {
		a.interactiveSubAgents.Delete(oneshotKey)
		return nil, subCtx.Err()
	}
	defer operationGate.unlock()
	if err := agentTenantSession.Clear(); err != nil {
		a.interactiveSubAgents.Delete(oneshotKey)
		return nil, fmt.Errorf("clear oneshot agent tenant session: %w", err)
	}

	// Eager-save user message so get_history returns it during Run().
	historyID, err := agentTenantSession.AppendMessage(llm.NewUserMessage(task))
	if err != nil {
		a.interactiveSubAgents.Delete(oneshotKey)
		return nil, fmt.Errorf("append oneshot agent user message: %w", err)
	}
	if len(cfg.Messages) > 1 && cfg.Messages[1].Role == "user" {
		cfg.Messages[1].ID = historyID
	}

	// Wire CLI progress + stream callbacks so Ctrl+T shows real-time progress.
	a.wireSubAgentProgress(oneshotKey, originChatID, &cfg)

	// Wire incremental snapshot callback so iteration history is available
	// during Run() for panel preview and inspect — not only after completion.
	// Lock mu to avoid data race with ListInteractiveSessions/summarizeInteractivePreviewLocked.
	cfg.OnIterationSnapshot = func(snap IterationSnapshot) {
		oneshotIA.mu.Lock()
		oneshotIA.iterationHistory = append(oneshotIA.iterationHistory, snap)
		oneshotIA.mu.Unlock()
	}

	// Emit SubAgentStart event (notification, non-blocking)
	if a.hookManager != nil {
		a.hookManager.Emit(ctx, &hooks.SubAgentStartEvent{
			BasePayload: hooks.BasePayload{
				SessionID: originChatID, Channel: originChannel,
				SenderID: originSender, ChatID: originChatID,
			},
			AgentType: roleName,
			Task:      task,
		})
	}

	// Emit subagent_started event for instant sidebar push.
	a.emitSessionState(protocol.SessionEvent{
		Channel:    originChannel,
		ChatID:     originChatID,
		Action:     "subagent_started",
		Role:       roleName,
		Instance:   oneshotInstance,
		SessionKey: oneshotKey,
		ParentID:   originChatID,
	})

	// runOneshot executes the synchronous one-shot Run + result handling.
	// Shared by the foreground path (direct call) and the background path
	// (goroutine, task_wait-able).
	runOneshot := func(runCtx context.Context) (*channelpkg.OutboundMsg, error) {
		out := Run(runCtx, cfg)

		// Emit subagent_stopped event for instant sidebar update.
		a.emitSessionState(protocol.SessionEvent{
			Channel:    originChannel,
			ChatID:     originChatID,
			Action:     "subagent_stopped",
			Role:       roleName,
			Instance:   oneshotInstance,
			SessionKey: oneshotKey,
			ParentID:   originChatID,
		})

		log.Ctx(runCtx).WithFields(log.Fields{
			"role":     roleName,
			"instance": oneshotInstance,
			"out_nil":  out == nil,
			"out_len": func() int {
				if out != nil {
					return len(out.Content)
				}
				return 0
			}(),
			"iterations": len(oneshotIA.iterationHistory),
		}).Info("oneshot subagent Run() returned")

		// Populate iteration history so inspect can show results after completion
		oneshotIA.mu.Lock()
		oneshotIA.running = false
		if out != nil {
			oneshotIA.lastReply = out.Content
			oneshotIA.promptTokens = out.LastPromptTokens
			oneshotIA.completionTokens = out.LastCompletionTokens
			if len(cfg.Messages) > 0 {
				oneshotIA.systemPrompt = cfg.Messages[0]
			}
			if len(out.Messages) > 0 {
				start := 0
				if out.Messages[0].Role == "system" {
					start = 1
				}
				oneshotIA.messages = make([]llm.ChatMessage, len(out.Messages)-start)
				copy(oneshotIA.messages, out.Messages[start:])
			}
			if out.Content != "" {
				oneshotIA.messages = append(oneshotIA.messages, llm.NewAssistantMessage(out.Content))
			}
			if out.Content != "" && out.ReasoningContent != "" && len(oneshotIA.messages) > 0 {
				oneshotIA.messages[len(oneshotIA.messages)-1].ReasoningContent = out.ReasoningContent
			}
			if len(out.IterationHistory) > 0 {
				oneshotIA.iterationHistory = out.IterationHistory
			}
			log.Ctx(runCtx).WithField("iteration_count", len(oneshotIA.iterationHistory)).Info("oneshot subagent completed")
		} else {
			log.Ctx(runCtx).Warn("oneshot subagent returned nil output")
			oneshotIA.mu.Unlock()
			a.destroyInteractiveSession(oneshotKey)
			return &channelpkg.OutboundMsg{}, nil
		}
		oneshotIA.mu.Unlock()
		if agentTenantSession != nil && out.Content != "" {
			assistantMsg := llm.NewAssistantMessage(out.Content)
			assistantMsg.ReasoningContent = out.ReasoningContent
			if len(out.IterationHistory) > 0 {
				if jsonBytes, err := json.Marshal(out.IterationHistory); err == nil {
					assistantMsg.Detail = string(jsonBytes)
				}
			}
			historyID, err := agentTenantSession.AppendMessage(assistantMsg)
			if err != nil {
				a.cancelChildSessions(oneshotKey)
				persistErrText := fmt.Sprintf("append oneshot agent assistant message: %v", err)
				oneshotIA.mu.Lock()
				oneshotIA.running = false
				oneshotIA.lastError = persistErrText
				if len(oneshotIA.messages) > 0 && oneshotIA.messages[len(oneshotIA.messages)-1].Role == "assistant" {
					oneshotIA.messages = oneshotIA.messages[:len(oneshotIA.messages)-1]
				}
				oneshotIA.mu.Unlock()
				return nil, fmt.Errorf("append oneshot agent assistant message: %w", err)
			}
			oneshotIA.mu.Lock()
			if len(oneshotIA.messages) > 0 && oneshotIA.messages[len(oneshotIA.messages)-1].Role == "assistant" {
				oneshotIA.messages[len(oneshotIA.messages)-1].ID = historyID
			}
			oneshotIA.mu.Unlock()
		}
		// Cascade-cancel any bg sessions spawned during this one-shot's Run(),
		// then destroy the one-shot session immediately. Persisted agent tenant
		// history remains available for Web history/session-tree reads.
		a.cancelChildSessions(oneshotKey)
		a.destroyInteractiveSession(oneshotKey)

		log.Ctx(runCtx).WithFields(log.Fields{
			"parent":    parentAgentID,
			"role":      roleName,
			"tools":     out.ToolsUsed,
			"has_error": out.Error != nil,
		}).Info("SubAgent completed (via Run)")

		// Emit SubAgentStop event (notification, non-blocking)
		if a.hookManager != nil {
			a.hookManager.Emit(runCtx, &hooks.SubAgentStopEvent{
				BasePayload: hooks.BasePayload{
					SessionID: originChatID, Channel: originChannel,
					SenderID: originSender, ChatID: originChatID,
				},
				AgentType: roleName,
				Instance:  oneshotInstance,
				Content:   out.Content,
			})
		}

		if out.Error != nil {
			content := out.Content
			if content == "" {
				content = "⚠️ SubAgent 执行失败，未产生任何输出。"
			}
			content += fmt.Sprintf("\n\n> ❌ SubAgent Error: %v", out.Error)
			out.Content = content
		}

		// SubAgent 记忆整合：将本次对话的关键信息写入 SubAgent 的独立记忆
		// 同步执行，确保记忆写入完成后再返回，避免 session 被 unload 导致记忆丢失。
		if cfg.Memory != nil && len(out.Messages) > 0 {
			a.consolidateSubAgentMemory(runCtx, cfg, out.Messages, task, roleName, parentAgentID)
		}

		return out.OutboundMsg, nil
	}

	// Background one-shot (default): run in a goroutine registered with the task
	// manager, return immediately with a task ID the parent can task_wait on.
	// The completion result is injected as a notification.
	if msg.Metadata != nil && msg.Metadata["background"] == "true" {
		var bgBase context.Context
		if ctx.Value(bgSessionCtxKey{}) != nil {
			bgBase = ctx
		} else {
			bgBase = a.agentCtx
		}
		if bgBase == nil {
			bgBase = context.Background() // safety fallback for tests
		}
		bgCtx, bgCancel := context.WithCancel(bgBase)
		bgCtx = context.WithValue(bgCtx, bgSessionCtxKey{}, true)
		bgCtx = context.WithValue(bgCtx, bgParentKey{}, oneshotKey)
		bgCtx = WithCallChain(bgCtx, CallChainFromContext(subCtx))

		oneshotIA.mu.Lock()
		oneshotIA.background = true
		oneshotIA.cancelCurrent = bgCancel
		oneshotIA.mu.Unlock()

		sessionKey := originChannel + ":" + originChatID
		notifyMgr := a.bgTaskMgr
		var bgTask *tools.SubAgentTask
		if notifyMgr != nil {
			bgTask = notifyMgr.RegisterSubAgentTask("", sessionKey, originSender, roleName, oneshotInstance, bgCancel)
		}

		go func() {
			defer bgCancel()
			// Panic recovery: a panic inside runOneshot (Run(), persistence,
			// session destroy) would crash the whole process AND leave the
			// waitable task open (task_wait blocks until timeout). Mirror the
			// interactive background goroutine's recover() — close the task and
			// notify the parent so nothing leaks.
			defer func() {
				if r := recover(); r != nil {
					log.WithFields(log.Fields{
						"role":     roleName,
						"instance": oneshotInstance,
						"panic":    r,
					}).Error("Background one-shot subagent panicked")
					if notifyMgr == nil {
						return
					}
					content := fmt.Sprintf("Sub-agent panicked: %v", r)
					if bgTask != nil {
						notifyMgr.CloseSubAgentTask(bgTask.ID, tools.BgTaskError, content)
					}
					notifyMgr.SendSubAgentNotify(&tools.SubAgentBgNotify{
						Key:      sessionKey,
						Type:     tools.SubAgentBgNotifyCompleted,
						Role:     roleName,
						Instance: oneshotInstance,
						Content:  content,
						Sid:      originSender,
					})
				}
			}()
			outMsg, runErr := runOneshot(bgCtx)
			if notifyMgr == nil {
				return
			}
			content := ""
			if outMsg != nil {
				content = outMsg.Content
			} else if runErr != nil {
				content = fmt.Sprintf("Error: %v", runErr)
			}
			if bgTask != nil {
				status := tools.BgTaskDone
				if runErr != nil {
					status = tools.BgTaskError
				}
				notifyMgr.CloseSubAgentTask(bgTask.ID, status, content)
			}
			notifyMgr.SendSubAgentNotify(&tools.SubAgentBgNotify{
				Key:      sessionKey,
				Type:     tools.SubAgentBgNotifyCompleted,
				Role:     roleName,
				Instance: oneshotInstance,
				Content:  content,
				Sid:      originSender,
			})
		}()

		startedMsg := fmt.Sprintf("Sub-agent %q (instance=%s) started in background.", roleName, oneshotInstance)
		if bgTask != nil {
			startedMsg += fmt.Sprintf("\n\nBackground task ID: %s. Use task_wait (task_id=%q) to wait for completion, or task_status to check progress.", bgTask.ID, bgTask.ID)
		}
		return &channelpkg.OutboundMsg{Content: startedMsg}, nil
	}

	return runOneshot(subCtx)
}

// resolveSubAgents extracts the SubAgent tree from a ProgressEvent.
// It prefers the structured SubAgents field (reliable), falling back to
// text-based ExtractSubAgentTree only if structured data is unavailable.
func resolveSubAgents(event *ProgressEvent) []protocol.SubAgentInfo {
	// Structured data only — no text-based string matching. SubAgents are
	// cleared at iteration boundary in beginIteration (they're one-shot tools
	// that complete synchronously within the iteration). When nil, returns nil
	// — no fallback.
	if event.Structured != nil && len(event.Structured.SubAgents) > 0 {
		return convertCLISubAgentTree(event.Structured.SubAgents)
	}
	return nil
}

// convertCLISubAgentTree 将 agent.SubAgentNode 转换为 protocol.SubAgentInfo 树。
func convertCLISubAgentTree(nodes []SubAgentNode) []protocol.SubAgentInfo {
	if len(nodes) == 0 {
		return nil
	}
	result := make([]protocol.SubAgentInfo, len(nodes))
	for i, n := range nodes {
		result[i] = protocol.SubAgentInfo{
			Role:       n.Role,
			Instance:   n.Instance,
			SessionKey: n.SessionKey,
			Status:     n.Status,
			Desc:       n.Desc,
			Children:   convertCLISubAgentTree(n.Children),
		}
	}
	return result
}

// buildProgressPayload converts one engine progress event into the shared
// protocol consumed by every ProgressSender channel. Snapshot/log identity is
// assigned once before fan-out; transports never derive iteration history.
func buildProgressPayload(progressKey string, event *ProgressEvent) *protocol.ProgressEvent {
	if event == nil || event.Structured == nil {
		return nil
	}
	s := event.Structured
	payload := &protocol.ProgressEvent{
		ChatID: progressKey, Phase: string(s.Phase), Seq: s.Seq,
		Iteration: s.Iteration, Content: s.Content, Reasoning: s.ReasoningContent,
		HistoryCompacted: s.HistoryCompacted, CWD: s.CWD,
		TurnID: s.TurnID,
	}
	for _, t := range s.ActiveTools {
		payload.ActiveTools = append(payload.ActiveTools, protocol.ToolProgress{
			Name: t.Name, Label: t.Label, Status: string(t.Status),
			Elapsed: t.Elapsed.Milliseconds(), Iteration: t.Iteration,
			Summary: t.Summary, Detail: t.Detail, Args: t.Args, ToolHints: t.ToolHints,
		})
	}
	for _, t := range s.CompletedTools {
		payload.CompletedTools = append(payload.CompletedTools, protocol.ToolProgress{
			Name: t.Name, Label: t.Label, Status: string(t.Status),
			Elapsed: t.Elapsed.Milliseconds(), Iteration: t.Iteration,
			Summary: t.Summary, Detail: t.Detail, Args: t.Args, ToolHints: t.ToolHints,
		})
	}
	payload.SubAgents = resolveSubAgents(event)
	payload.Todos = make([]protocol.TodoItem, len(s.Todos))
	for i, td := range s.Todos {
		payload.Todos[i] = protocol.TodoItem{ID: td.ID, Text: td.Text, Done: td.Done}
	}
	if s.TokenUsage != nil {
		payload.TokenUsage = &protocol.TokenUsage{
			PromptTokens: s.TokenUsage.PromptTokens, CompletionTokens: s.TokenUsage.CompletionTokens,
			TotalTokens: s.TokenUsage.TotalTokens, CacheHitTokens: s.TokenUsage.CacheHitTokens,
			MaxOutputTokens: s.TokenUsage.MaxOutputTokens,
		}
	}
	// Attach stream timing stats from the latest LLM call
	if s.StreamStats != nil {
		payload.StreamStats = s.StreamStats
	}
	return payload
}

// buildProgressEventHandler creates the single channel-agnostic structured
// progress pipeline. It derives one semantic snapshot/log event, stores it
// once, then broadcasts that exact immutable payload to every registered
// ProgressSender (CLI, Web, and plugin channels).
func (a *Agent) buildProgressEventHandler(chatID, originatingChannel string) func(*ProgressEvent) {
	if a.channelRange == nil {
		return nil
	}
	var senders []channelpkg.ProgressSender
	a.channelRange(func(_ string, ch channelpkg.Channel) bool {
		if sender, ok := ch.(channelpkg.ProgressSender); ok {
			senders = append(senders, sender)
		}
		return true
	})
	if len(senders) == 0 {
		return nil
	}
	progressKey := qualifyChatID(originatingChannel, chatID)
	return func(event *ProgressEvent) {
		payload := buildProgressPayload(progressKey, event)
		if payload == nil {
			return
		}
		// PhaseDone（turn 结束）：最后迭代没有"推进到下一迭代"的事件，
		// attachIterationDelta（条件 nextIteration > prev.Iteration）不会记录
		// 它 → 前端 progress 事件的 IterationHistory 缺最后迭代 content →
		// text 事件 commit 后内容消失（DB 有 content，刷新恢复，用户报告
		// "刷新前看不到最后的迭代 content，content stream 完之后会消失"）。
		// PhaseDone 时强制记录最后迭代，并把它附加到事件本身。
		if payload.Phase == string(PhaseDone) {
			a.recordFinalIteration(progressKey)
			if hist, ok := a.iterationHistories.Load(progressKey); ok {
				h := *hist.(*[]protocol.ProgressEvent)
				if len(h) > 0 {
					last := h[len(h)-1]
					if last.Iteration == payload.Iteration {
						payload.IterationHistory = []protocol.ProgressEvent{last}
					}
				}
			}
		}
		a.attachIterationDelta(progressKey, payload.Iteration, payload)
		// Iteration checkpoint: push the FULL cumulative stream text as a
		// stream_content event BEFORE clearStreamState wipes it. This realigns
		// the frontend's delta-accumulated streamContent (bandwidth
		// optimization pushes O(n) deltas) — a dropped delta mid-iteration is
		// repaired here, keeping the same strong consistency as the old
		// full-push scheme.
		if prev, ok := a.lastProgressSnapshot.Load(progressKey); ok {
			if pe, ok := prev.(*protocol.ProgressEvent); ok && pe.StreamContent != "" {
				for _, sender := range senders {
					sender.SendProgress(chatID, &protocol.ProgressEvent{
						ChatID:        progressKey,
						TurnID:        pe.TurnID,
						Iteration:     payload.Iteration,
						StreamContent: pe.StreamContent,
					})
				}
			}
		}
		a.lastProgressSnapshot.Store(progressKey, progressSnapshotWithoutHistory(payload))
		a.clearStreamState(progressKey)
		for _, sender := range senders {
			sender.SendProgress(chatID, cloneProgressEvent(payload))
		}
	}
}

// buildStreamCallbacks collects the ProgressSender for the ORIGINATING channel
// and returns stream callbacks that push to it.
//
// Only the originating channel is used — its SendProgress already broadcasts
// to ALL Hub subscribers (including other channels' clients). Broadcasting to
// multiple channels causes duplicate delivery: each channel sends to the same
// Hub, which broadcasts to the same subscribers, so each subscriber receives
// the event N times (where N = number of ProgressSender channels).
//
// RATE LIMITING: content/reasoning push is NOT throttled — every token
// callback pushes immediately. The frontend typewriter (50ms tick) renders
// at its own pace; coalescing of redundant snapshots happens at the SSE
// delivery layer (sendCh batching + ring-buffer mergeStatelessEvent).
// Tool calls and token usage are low-frequency, also not throttled.
// All callbacks also write to atomic streamState for GetActiveProgress reconnect.
func (a *Agent) buildStreamCallbacks(chatID, channel string, progressSeq *atomic.Uint64, turnID uint64, sessionKey string, tenantID int64) (streamContentFunc func(string), streamReasoningFunc func(string), streamToolCallFunc func([]llm.ToolCallDelta), streamUsageFunc func(*llm.TokenUsage), resetTiming func()) {
	// Use ONLY the originating channel — its SendProgress broadcasts to ALL
	// Hub subscribers (including other channels' clients via shared Hub).
	var sender channelpkg.ProgressSender
	if ch, ok := a.channelFinder(channel); ok {
		if ps, ok := ch.(channelpkg.ProgressSender); ok {
			sender = ps
		}
	}

	progressKey := qualifyChatID(channel, chatID)

	// broadcastProgress pushes to the originating channel only.
	// chatID (raw) is the routing key for SendProgress's first parameter.
	// payload.ChatID (qualified progressKey) is the session identity for
	// the TUI's handleProgressMsg filter. These are two different semantics —
	// never mix them.
	broadcastProgress := func(payload *protocol.ProgressEvent) {
		if payload.ChatID == "" {
			payload.ChatID = progressKey
		}
		if sender != nil {
			sender.SendProgress(chatID, payload)
		}
	}

	// Live stream timing: attach a REAL-TIME StreamStats (tkps/ttft/totalMs)
	// to EVERY stream frame, not just the iteration-end event. Previously
	// StreamStats was only attached by buildProgressPayload after callLLM
	// completed, so the frontend tkps indicator only updated once per stream.
	requestStartAt := time.Now()
	var firstChunkAt time.Time
	var mu sync.Mutex
	// Real-time completion-token counter, updated by streamUsageFunc when the
	// provider reports usage mid-stream. NOTE: OpenAI/DeepSeek usually emit the
	// usage event only at stream end, so this is ~0 during generation — liveStats
	// falls back to estimating tokens from cumulative content length (≈4 chars/token).
	var streamTokens int64
	// 1-second sliding window for INSTANT tokens/sec. Keeps (time, cumulativeTokens)
	// samples; tkps = (newest − oldest within the window) / Δt. No cumulative-average
	// fallback — cumulative average decays as elapsed grows (that was the
	// "always decreasing tkps" bug). Reasoning counts as tokens (it is generated
	// output); when the model is reasoning fast, tkps is high; when it pauses for
	// tools, tkps drops — that's the actual instantaneous speed.
	type tokenSample struct {
		at     time.Time
		tokens int64
	}
	var samples []tokenSample
	// Reset per-iteration timing baseline. TTFT must reflect THE CURRENT
	// LLM CALL's first-token latency, not the whole Run's. buildStreamCallbacks
	// is called once per Run (buildMainRunConfig); without resetting at each
	// beginIteration, live frames report the Run-wide TTFT (first chunk of
	// iteration 1) while committed iterations report their own response
	// StreamStats.TTFTMs — the same iteration showed different ttft values
	// between its live phase and its committed row ("迭代内 ttft 变化" bug).
	resetTiming = func() {
		mu.Lock()
		defer mu.Unlock()
		requestStartAt = time.Now()
		firstChunkAt = time.Time{}
		streamTokens = 0
		samples = samples[:0]
	}
	liveStats := func(payload *protocol.ProgressEvent) *protocol.StreamStats {
		now := time.Now()
		mu.Lock()
		if firstChunkAt.IsZero() {
			firstChunkAt = now
		}
		first := firstChunkAt
		tokens := streamTokens
		// Copy requestStartAt while holding the lock — resetTiming writes it
		// under mu.Lock() during beginIteration, so reading it after Unlock()
		// is a data race with the concurrent resetTiming.
		reqStart := requestStartAt
		// Estimate from CUMULATIVE stream state (reasoning + content) — NOT the
		// single-frame payload. Each stream frame carries only ONE field
		// (streamContentFunc sends StreamContent, streamReasoningFunc sends
		// ReasoningStreamContent); reading the frame would make the estimate
		// drop when the model switches reasoning→content (dtTokens < 0 → tkps
		// frozen at the previous value, the "123 tok/s never changes" bug).
		if tokens <= 0 {
			n := 0
			if v, ok := a.streamState.Load(progressKey); ok {
				if ap, ok := v.(*atomic.Pointer[protocol.ProgressEvent]); ok {
					if ss := ap.Load(); ss != nil {
						n = len(ss.StreamContent) + len(ss.ReasoningStreamContent)
					}
				}
			}
			if n > 0 {
				tokens = int64(n) / 4
			}
		}
		// Append sample, drop anything older than 1s, then rate = Δtokens/Δt over
		// the surviving window. Window must be ≥200ms to avoid noise; the very
		// first frames simply report 0 (frontend shows "streaming").
		//
		// CRITICAL: if tokens went BACKWARDS (iteration boundary → clearStreamState
		// wiped streamState → the estimate drops below the previous sample), the old
		// samples in the window carry a LARGER tokens value, so dtTokens < 0 → tps
		// stays 0 until the stale samples age out (≈1s). That made the tkps indicator
		// freeze between iterations then "suddenly start updating" once the old
		// samples slid out. Reset the window on regression so the new iteration
		// starts accumulating from its own baseline.
		if len(samples) > 0 && tokens < samples[len(samples)-1].tokens {
			samples = samples[:0]
		}
		samples = append(samples, tokenSample{at: now, tokens: tokens})
		cutoff := now.Add(-time.Second)
		for len(samples) > 0 && samples[0].at.Before(cutoff) {
			samples = samples[1:]
		}
		tps := int64(0)
		if len(samples) >= 2 {
			oldest := samples[0]
			dtMs := now.Sub(oldest.at).Milliseconds()
			dtTokens := tokens - oldest.tokens
			if dtMs >= 200 && dtTokens > 0 {
				tps = dtTokens * 1000 / dtMs
			}
		}
		mu.Unlock()
		return &protocol.StreamStats{
			TTFTMs:       first.Sub(reqStart).Milliseconds(),
			TokensPerSec: tps,
			TotalMs:      now.Sub(reqStart).Milliseconds(),
		}
	}
	withLiveStats := func(payload *protocol.ProgressEvent) {
		payload.StreamStats = liveStats(payload)
		broadcastProgress(payload)
	}

	// All stream callbacks go through broadcastProgress with a qualified
	// ChatID. This replaces the old SendStreamContent path which had
	// inconsistent ChatID qualification across implementations (CLIChannel
	// used raw, RemoteCLI/Web qualified manually).
	//
	// Bandwidth: delta push（a.deltaPush=true）时 stream pushes carry ONLY the
	// delta (O(n) total per iteration) instead of the full cumulative text on
	// every token (O(n²)). The server still keeps the FULL cumulative text in
	// lastProgressSnapshot so:
	//   - the iteration-end checkpoint (StreamContent set) realigns the frontend
	//   - get_active_progress / restoreActiveProgress can repair a lost delta
	//   - the next delta computation prefixes cleanly.
	// The delta computation falls back to a FULL checkpoint push when the
	// incoming text is not a strict prefix extension (reset/out-of-order).
	//
	// delta push 默认关闭（a.deltaPush=false）：每次推送完整累积文本 —— 简单
	// 可靠，gap 追赶无需特殊处理。delta push 曾引入多个问题（stateless gap
	// 不恢复导致打字机缺字、三层 isStreamOnly 分类不一致、迭代边界前缀判断、
	// 服务器重启拼接），需显式开启（config delta_push: true）。
	streamContentFunc = func(content string) {
		iter := a.getActiveIteration(progressKey)
		if !a.deltaPush {
			a.updateStreamState(progressKey, func(s *protocol.ProgressEvent) {
				s.StreamContent = content
			})
			withLiveStats(&protocol.ProgressEvent{
				ChatID:        progressKey,
				TurnID:        turnID,
				Iteration:     iter,
				StreamContent: content,
			})
			return
		}
		delta := content
		isFull := true
		a.updateStreamState(progressKey, func(s *protocol.ProgressEvent) {
			prev := s.StreamContent
			if len(content) > len(prev) && strings.HasPrefix(content, prev) {
				delta = content[len(prev):]
				isFull = false
			}
			// Keep the full cumulative text server-side (checkpoint + recovery source).
			s.StreamContent = content
		})
		if isFull {
			withLiveStats(&protocol.ProgressEvent{
				ChatID:        progressKey,
				TurnID:        turnID,
				Iteration:     iter,
				StreamContent: content,
			})
		} else {
			withLiveStats(&protocol.ProgressEvent{
				ChatID:      progressKey,
				TurnID:      turnID,
				Iteration:   iter,
				StreamDelta: delta,
			})
		}
	}
	streamReasoningFunc = func(content string) {
		iter := a.getActiveIteration(progressKey)
		if !a.deltaPush {
			a.updateStreamState(progressKey, func(s *protocol.ProgressEvent) {
				s.ReasoningStreamContent = content
			})
			withLiveStats(&protocol.ProgressEvent{
				ChatID:                 progressKey,
				TurnID:                 turnID,
				Iteration:              iter,
				ReasoningStreamContent: content,
			})
			return
		}
		delta := content
		isFull := true
		a.updateStreamState(progressKey, func(s *protocol.ProgressEvent) {
			prev := s.ReasoningStreamContent
			if len(content) > len(prev) && strings.HasPrefix(content, prev) {
				delta = content[len(prev):]
				isFull = false
			}
			s.ReasoningStreamContent = content
		})
		if isFull {
			withLiveStats(&protocol.ProgressEvent{
				ChatID:                 progressKey,
				TurnID:                 turnID,
				Iteration:              iter,
				ReasoningStreamContent: content,
			})
		} else {
			withLiveStats(&protocol.ProgressEvent{
				ChatID:               progressKey,
				TurnID:               turnID,
				Iteration:            iter,
				ReasoningStreamDelta: delta,
			})
		}
	}
	streamToolCallFunc = func(toolCalls []llm.ToolCallDelta) {
		toolProgs := make([]protocol.ToolProgress, 0, len(toolCalls))
		var genuiContent string
		for _, tc := range toolCalls {
			if tc.Name != "" {
				// Look up UI metadata once per tool name — populates both the
				// streaming GenUI extraction and the ToolProgress UIMode/UILibs
				// (frontend renders metadata-driven, never tool-name-driven).
				var ui *tools.UIDecl
				if ui = a.toolUIDecl(sessionKey, tenantID, tc.Name); ui != nil && ui.Mode == "genui" {
					genuiContent = extractPartialParam(tc.Arguments, ui.Param)
				}
				tp := protocol.ToolProgress{
					Name:     tc.Name,
					Status:   "generating",
					GenChars: len(tc.Arguments),
					// Each tool must stamp the current iteration too — the event's
					// Iteration field (below) is NOT copied into the per-tool entries.
					// Without it, a stale generating tool from a completed iteration
					// (surviving via streamState merge into get_active_progress
					// snapshots / catchup gap replay) carries no iteration marker and
					// the frontend cannot filter it from the current iteration's live
					// rendering (user report: "过去的 generating 状态错误的在最新
					// 迭代上渲染，直到最新迭代真正的 tool 出现").
					Iteration: a.getActiveIteration(progressKey),
				}
				if ui != nil {
					tp.UIMode = ui.Mode
					tp.UILibs = ui.Libs
				}
				toolProgs = append(toolProgs, tp)
			}
		}
		if len(toolProgs) == 0 && genuiContent == "" {
			return
		}

		// No server-side throttle for genuiContent — the frontend already
		// throttles compilation to 100ms. Server-side throttle would drop
		// intermediate updates and potentially the final code.
		a.updateStreamState(progressKey, func(s *protocol.ProgressEvent) {
			s.StreamingTools = toolProgs
			if genuiContent != "" {
				s.GenUIContent = genuiContent
			}
		})
		seq := progressSeq.Add(1)
		payload := &protocol.ProgressEvent{
			ChatID: progressKey,
			TurnID: turnID,
			Seq:    seq,
			// MUST stamp the current iteration — without it the event serializes
			// as iteration:0, and the frontend receives a "tool generating"
			// stream_content whose iteration dropped to 0 mid-turn (user report:
			// "iter id 突然变成 0 导致整个 turn 的 DOM 消失"; all repro dumps show
			// the vanishing turn right after an iteration:0 streaming_tools event).
			Iteration:      a.getActiveIteration(progressKey),
			StreamingTools: toolProgs,
		}
		if genuiContent != "" {
			payload.GenUIContent = genuiContent
		}
		withLiveStats(payload)
	}
	streamUsageFunc = func(usage *llm.TokenUsage) {
		if usage == nil || usage.CompletionTokens == 0 {
			return
		}
		mu.Lock()
		streamTokens = usage.CompletionTokens
		mu.Unlock()
		a.updateStreamState(progressKey, func(s *protocol.ProgressEvent) {
			s.StreamTokens = usage.CompletionTokens
		})
		seq := progressSeq.Add(1)
		withLiveStats(&protocol.ProgressEvent{
			ChatID: progressKey,
			TurnID: turnID,
			Seq:    seq,
			// MUST stamp Iteration (same bug class as StreamingTools): a
			// stream_tokens event without it serializes as iteration:0, and the
			// frontend sees iteration drop to 0 mid-turn → it rejects the event
			// (user report: "iter 为 0 导致 turn 消失").
			Iteration:    a.getActiveIteration(progressKey),
			StreamTokens: usage.CompletionTokens,
		})
	}
	return streamContentFunc, streamReasoningFunc, streamToolCallFunc, streamUsageFunc, resetTiming
}

// toolUIDecl looks up the tool's UI declaration (UIDeclProvider) by session
// context. Returns nil if the tool is unknown or has no UI capability.
// This is the single metadata-driven lookup — no hardcoded tool names.
func (a *Agent) toolUIDecl(sessionKey string, tenantID int64, toolName string) *tools.UIDecl {
	if a.tools == nil || toolName == "" {
		return nil
	}
	tool, ok := a.tools.GetForSession(toolName, tenantID, sessionKey)
	if !ok {
		return nil
	}
	if p, ok := tool.(tools.UIDeclProvider); ok {
		return p.UIDecl()
	}
	return nil
}

// extractPartialParam extracts the named field value from a partial JSON
// string like {"code":"<div class='...'>...}. The JSON may be incomplete
// (streaming), so we use a string scan instead of json.Unmarshal.
// Returns "" if the field is absent or not a quoted string.
func extractPartialParam(args, paramName string) string {
	// Find "paramName":" or "paramName": "
	needle := `"` + paramName + `"`
	idx := strings.Index(args, needle)
	if idx == -1 {
		return ""
	}
	// Skip past "paramName"
	rest := args[idx+len(needle):]
	// Skip whitespace and colon
	for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n' || rest[0] == ':') {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return ""
	}
	// Must start with a quote
	quote := rest[0]
	if quote != '"' && quote != '\'' {
		return ""
	}
	rest = rest[1:]
	// Read until matching quote (respecting backslash escapes)
	var sb strings.Builder
	for i := 0; i < len(rest); i++ {
		ch := rest[i]
		if ch == '\\' && i+1 < len(rest) {
			// Handle escape sequences
			next := rest[i+1]
			switch next {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			case '"':
				sb.WriteByte('"')
			case '\\':
				sb.WriteByte('\\')
			case '/':
				sb.WriteByte('/')
			default:
				sb.WriteByte(next)
			}
			i++
			continue
		}
		if ch == quote {
			return sb.String()
		}
		sb.WriteByte(ch)
	}
	// Stream incomplete — return what we have so far
	return sb.String()
}

// interactiveSessionsToStatuses converts InteractiveSessionInfo slice to
// lightweight SubAgentStatus slice for system reminder injection.
func interactiveSessionsToStatuses(sessions []InteractiveSessionInfo) []SubAgentStatus {
	if len(sessions) == 0 {
		return nil
	}
	statuses := make([]SubAgentStatus, len(sessions))
	for i, s := range sessions {
		statuses[i] = SubAgentStatus{
			Role:     s.Role,
			Instance: s.Instance,
			Running:  s.Running,
		}
	}
	return statuses
}

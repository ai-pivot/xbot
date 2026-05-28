package agent

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xbot/agent/hooks"
	"xbot/bus"
	"xbot/channel"
	"xbot/clipanic"
	"xbot/cron"
	"xbot/event"
	"xbot/llm"
	log "xbot/logger"
	"xbot/memory"
	"xbot/memory/letta"
	"xbot/plugin"
	"xbot/protocol"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// ErrLLMGenerate 表示 LLM 生成调用失败（网络、API 4xx/5xx 等）
var ErrLLMGenerate = errors.New("LLM generate failed")

// assertNoSystemPersist checks that a system message is not being persisted to session.
// Returns error if a system message is detected — callers should skip the message and log.
func assertNoSystemPersist(m llm.ChatMessage) error {
	if m.Role == "system" {
		log.WithField("message", m).Error("ASSERT: must not persist system message to session")
		return fmt.Errorf("must not persist system message to session")
	}
	return nil
}

// copyMessages creates a shallow copy of the messages slice so that
// in-place modifications (e.g. stripSystemReminder) don't mutate the
// original cfg.Messages backing array or session storage.
func copyMessages(msgs []llm.ChatMessage) []llm.ChatMessage {
	cpy := make([]llm.ChatMessage, len(msgs))
	copy(cpy, msgs)
	return cpy
}

// formatErrorForUser 将错误格式化为对用户可见的提示
func formatErrorForUser(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrLLMGenerate) {
		return fmt.Sprintf("LLM 服务调用失败，请稍后重试或检查配置。\n错误详情: %v", err)
	}
	return fmt.Sprintf("处理消息时发生错误: %v", err)
}

// resolveMemoryProvider returns the effective memory provider, defaulting to "flat".
func resolveMemoryProvider(cfg string) string {
	if cfg == "" {
		return "flat"
	}
	return cfg
}

func resolveGlobalSkillsDirs(skillsDir string) []string {
	if skillsDir == "" {
		return nil
	}
	abs, err := filepath.Abs(skillsDir)
	if err != nil {
		return nil
	}
	return []string{abs}
}

// metaTools are tools that manage/search other tools — not useful to index.
var metaTools = map[string]bool{
	"search_tools": true,
	"load_tools":   true,
	"manage_tools": true,
}

// IndexGlobalTools indexes all global tools for semantic search:
// built-in registry tools, tool groups, and global MCP servers.
// Call after all tools are registered. Uses full-replace semantics
// so stale entries from removed tools are automatically cleaned up.
func (a *Agent) IndexGlobalTools() {
	registry := a.tools
	multiSession := a.multiSession
	globalMCPConfigPath := filepath.Join(a.xbotHome, "mcp.json")

	ctx := context.Background()
	var toolEntries []memory.ToolIndexEntry
	indexed := make(map[string]bool) // track indexed tool names to avoid duplicates

	// 1. Index built-in tool groups (like Feishu tools)
	toolGroups := registry.GetToolGroups()
	for _, group := range toolGroups {
		for _, toolName := range group.ToolNames {
			tool, ok := registry.Get(toolName)
			desc := fmt.Sprintf("Built-in tool group: %s", group.Name)
			var channels []string
			if ok {
				if toolDesc := tool.Description(); toolDesc != "" {
					desc = fmt.Sprintf("Tool: %s. %s", toolName, toolDesc)
				}
				if cp, ok := tool.(tools.ChannelProvider); ok {
					channels = cp.SupportedChannels()
				}
			}
			if group.Instructions != "" {
				desc = fmt.Sprintf("%s. %s", desc, group.Instructions)
			}
			toolEntries = append(toolEntries, memory.ToolIndexEntry{
				Name:        toolName,
				ServerName:  group.Name,
				Source:      "global",
				Description: desc,
				Channels:    channels,
			})
			indexed[toolName] = true
		}
	}

	// 2. Index all registry tools not already covered by tool groups
	for _, tool := range registry.List() {
		name := tool.Name()
		if indexed[name] || metaTools[name] {
			continue
		}
		var channels []string
		if cp, ok := tool.(tools.ChannelProvider); ok {
			channels = cp.SupportedChannels()
		}
		toolEntries = append(toolEntries, memory.ToolIndexEntry{
			Name:        name,
			ServerName:  "builtin",
			Source:      "global",
			Description: tool.Description(),
			Channels:    channels,
		})
		indexed[name] = true
	}

	// 3. Index global MCP servers (non-blocking: starts background init, re-indexes once on completion)
	//    We do NOT use SetOnChange here because IndexGlobalTools creates a fresh
	//    mcpMgr each call, and onChange would trigger another IndexGlobalTools →
	//    another mcpMgr → infinite goroutine chain. Instead, we fire a single
	//    background re-index that creates its own mcpMgr with sync.Once guard.
	dummySessionKey := "indexing:dummy"
	mcpMgr := tools.NewSessionMCPManager(
		dummySessionKey,
		"system0",
		globalMCPConfigPath,
		"", "", 30*time.Minute,
	)
	if mcpMgr != nil {
		catalog := mcpMgr.GetCatalog() // non-blocking: returns current (may be empty on first call)
		for _, entry := range catalog {
			for _, toolName := range entry.ToolNames {
				fullName := fmt.Sprintf("mcp_%s_%s", entry.Name, toolName)
				desc := fmt.Sprintf("MCP server: %s. Tool: %s", entry.Name, toolName)
				if entry.Instructions != "" {
					desc = fmt.Sprintf("%s. %s", desc, entry.Instructions)
				}
				toolEntries = append(toolEntries, memory.ToolIndexEntry{
					Name:        fullName,
					ServerName:  entry.Name,
					Source:      "global",
					Description: desc,
				})
			}
		}
		mcpMgr.Close()
	}

	if len(toolEntries) == 0 {
		log.Info("No tools to index")
		return
	}

	if err := multiSession.IndexToolsForTenant(ctx, 0, toolEntries); err != nil {
		log.WithError(err).Warn("Failed to index global tools")
		return
	}

	log.WithField("count", len(toolEntries)).Infof("Indexed %d global tools (registry + tool groups + MCP)", len(toolEntries))
}

// bgSessionState tracks per-session state for bg notification delivery.
// Registered in bgSessionStates when a chatWorker starts, deregistered on exit.
//
// Architecture:
//   - bgNotifyLoop ALWAYS buffers to bgRunPending, NEVER processes directly.
//   - After buffering, it signals the session's notifyCh.
//   - chatProcessLoop drains pending notifications after each turn completes
//     (after response is sent), guaranteeing injectCLIUserMessage won't race
//     with the turn's reply on asyncCh.
//   - chatWorker drains pending notifications when idle (chatProcessLoop is
//     waiting on msgCh), checked via busy flag to avoid racing with response sends.
//   - During active Run, wireBgNotificationDrain picks up notifications between
//     iterations as tool results.
type bgSessionState struct {
	notifyCh chan struct{} // buffered(1): signal that bgRunPending has new items
	busy     atomic.Bool   // true while chatProcessLoop is processing a turn
}

// Agent 核心 Agent 引擎
type Agent struct {
	bus              *bus.MessageBus
	multiSession     *session.MultiTenantSession // Multi-tenant session manager
	tools            *tools.Registry
	maxIterations    int
	purgeOldMessages bool

	skills             *SkillStore
	agents             *AgentStore
	chatHistory        *tools.ChatHistoryStore // 聊天历史缓存
	cardBuilder        *tools.CardBuilder      // Card Builder MCP
	workDir            string
	promptLoader       *PromptLoader
	pipeline           *MessagePipeline // 消息构建管道（持有实例，支持运行时动态增删中间件）
	cronPipeline       *MessagePipeline // Cron 专用消息构建管道
	sandboxMode        string           // "none" or "docker"
	sandbox            tools.Sandbox    // Sandbox 实例引用（V4 新增）
	sandboxIdleTimeout time.Duration    // 沙箱空闲超时（0 禁用）
	directWorkspace    string           // 非空时 workspaceRoot() 直接返回此值（CLI 模式使用，取代 singleUser 的 workspace 短路）
	maxConcurrency     int              // 最大并发会话处理数
	globalSem          chan struct{}    // 全局并发信号量（SetMaxConcurrency 动态重建）
	globalSemMu        sync.Mutex       // 保护 globalSem 替换
	globalSkillDirs    []string         // 全局 skill 目录（宿主机路径）
	agentsDir          string
	xbotHome           string // global xbot config dir (e.g. ~/.xbot), used for mcp.json etc.

	// 上下文管理配置
	contextManagerConfig *ContextManagerConfig
	contextManagerMu     sync.RWMutex // 保护 contextManager 的并发读写
	contextManager       ContextManager

	// SubAgent 深度控制
	maxSubAgentDepth int

	// Cron service and scheduler
	cronSvc *sqlite.CronService
	cronSch *cron.Scheduler

	// Event trigger router
	eventRouter *event.Router

	// User LLM config service and factory
	llmConfigSvc *sqlite.UserLLMConfigService
	llmFactory   *LLMFactory

	// 用户级别的信号量：设置了自己的 LLM 配置的用户使用独立信号量
	// key: senderID, value: 用户独立的信号量（容量为1）
	userSemaphores sync.Map // map[string]chan struct{}

	commands         *CommandRegistry                          // 指令注册表
	directSend       func(channel.OutboundMsg) (string, error) // 同步发送，绕过 bus 以获取 message_id
	sessionMsgIDs    sync.Map                                  // key: "channel:chatID" -> 当前 session 已发消息 ID（用于 Patch 更新）
	sessionReplyTo   sync.Map                                  // key: "channel:chatID" -> 用户入站消息 ID（用于首条回复的 reply 模式）
	sessionFinalSent sync.Map                                  // key: "channel:chatID" -> bool, 工具已发送最终回复（如卡片），后续 sendMessage 跳过

	// per-request cancel: 用于 /cancel 取消当前正在处理的请求
	// key: "channel:chatID" -> chan struct{} (buffered, cap=1)
	chatCancelCh sync.Map

	// pendingCancel: 当 /cancel 到达时 cancelCh 尚未注册（消息还在排队或等信号量），
	// 先记录 pending，chatProcessLoop 注册 cancelCh 后立即消费。
	// key: "channel:chatID" -> bool
	pendingCancel sync.Map

	// lastProgressSnapshot stores the latest CLIProgressPayload per active chat,
	// updated by ProgressEventHandler during processing. Used by GetActiveProgress
	// RPC to restore progress state on mid-session reconnect.
	// key: "channel:chatID" -> *protocol.ProgressEvent
	lastProgressSnapshot sync.Map

	// iterationHistories stores completed iteration snapshots per active chat.
	// key: "channel:chatID" -> *[]protocol.ProgressEvent (one per completed iteration)
	// On turn end, the entry is deleted.
	iterationHistories sync.Map

	// builtinProgressSeq stores per-chat atomic seq counters for builtin commands
	// (/compress, /new) that bypass engine.Run but still need progress events.
	// key: "channel:chatID" -> *atomic.Uint64
	builtinProgressSeq sync.Map

	// interactiveSubAgents stores interactive SubAgent sessions
	// key: "channel:chatID/roleName" -> *interactiveAgent
	// sync.Map provides atomic Load/Store/Delete/LoadOrStore, no additional mutex needed
	interactiveSubAgents sync.Map

	// messageSender allows sending messages to any Channel via Dispatcher.
	messageSender bus.MessageSender
	// registerAgentChannel registers an AgentChannel in the Dispatcher.
	registerAgentChannel func(name string, runFn bus.RunFn) error
	// unregisterAgentChannel removes an AgentChannel from the Dispatcher.
	unregisterAgentChannel func(name string)

	// hookManager is the shared tool execution hook manager for this Agent and all SubAgents.
	hookManager *hooks.Manager

	// timingData collects per-tool execution timing statistics.
	timingData *hooks.TimingData

	// approvalState manages approval handling for privileged operations.
	approvalState *hooks.ApprovalState

	// OffloadStore manages large tool result offload to disk
	offloadStore *OffloadStore

	// maskStore manages observation masking storage
	maskStore *ObservationMaskStore

	// cleanupStopCh signals the periodic cleanup goroutine to stop
	cleanupStopCh chan struct{}

	// contextEditor 管理上下文编辑（Context Editing 工具）
	contextEditor *ContextEditor

	// todoManager 管理当前会话的 TODO 列表
	todoManager *tools.TodoManager

	// channelPromptProviders channel 特化 prompt 提供者列表（由外部注入）
	channelPromptProviders []ChannelPromptProvider

	// RegistryManager for skill/agent sharing and marketplace
	registryManager *RegistryManager

	// SettingsService for per-user settings
	settingsSvc *SettingsService

	// TUI control callbacks (set by CLI channel, nil for other channels)
	tuiCtrlFn   func(action string, params map[string]string) (map[string]string, error)
	configGetFn func(key string) (string, error)
	configSetFn func(key, value string) (string, error)

	// channelFinder looks up a channel instance by name (injected from main.go).
	channelFinder func(name string) (channel.Channel, bool)

	// cliSenderID is the sender_id used for CLI channel DB operations.
	cliSenderID string

	// bgTaskMgr manages background shell tasks (shared across all sessions)

	// PluginManager manages the plugin system lifecycle
	pluginMgr *plugin.PluginManager
	bgTaskMgr *tools.BackgroundTaskManager

	// bgRunPending buffers bg notifications that arrived during an active Run.
	// The Run loop drains these between iterations.
	bgRunPending   []tools.BgNotification
	bgRunPendingMu sync.Mutex

	// bgSessionStates maps chatKey → *bgSessionState for per-session notification signaling.
	// bgNotifyLoop always buffers notifications, then signals the session's state channel.
	// chatWorker registers on entry and deregisters on exit.
	bgSessionStates sync.Map

	// agentCtx is the Agent-level context, set when Run() starts and cancelled when Run() exits.
	// Background interactive subagents derive their context from this (not from per-request ctx)
	// so they survive across multiple requests and only stop when the parent Agent process exits.
	agentCtx    context.Context
	agentCancel context.CancelFunc
}

// SetRegistryManager sets the RegistryManager (for external injection or override).
func (a *Agent) SetRegistryManager(rm *RegistryManager) { a.registryManager = rm }

// SetSettingsService sets the SettingsService (for external injection or override).
func (a *Agent) SetSettingsService(svc *SettingsService) { a.settingsSvc = svc }

// SetTUICallbacks sets the TUI control and config callbacks (CLI channel only).
func (a *Agent) SetTUICallbacks(
	tuiCtrl func(action string, params map[string]string) (map[string]string, error),
	configGet func(key string) (string, error),
	configSet func(key, value string) (string, error),
) {
	a.tuiCtrlFn = tuiCtrl
	a.configGetFn = configGet
	a.configSetFn = configSet
}

// buildRemoteTUICtrlFn returns a TUIControl callback for remote CLI mode via WS,
// or nil if no RemoteCLIChannel is registered.
func (a *Agent) buildRemoteTUICtrlFn(chanName, chatID string) func(action string, params map[string]string) (map[string]string, error) {
	if a.channelFinder == nil {
		log.WithField("chan", chanName).Debug("buildRemoteTUICtrlFn: channelFinder is nil")
		return nil
	}
	if chanName != "cli" {
		log.WithField("chan", chanName).Debug("buildRemoteTUICtrlFn: channel is not cli")
		return nil
	}
	ch, ok := a.channelFinder("cli")
	if !ok {
		log.Debug("buildRemoteTUICtrlFn: channelFinder('cli') returned not found")
		return nil
	}
	if rc, ok := ch.(*channel.RemoteCLIChannel); ok {
		log.WithField("chat_id", chatID).Debug("buildRemoteTUICtrlFn: remote TUI control enabled")
		return func(action string, params map[string]string) (map[string]string, error) {
			return rc.SendTUIControlRequest(chatID, action, params)
		}
	}
	if cch, ok := ch.(*channel.ChannelCliChannel); ok {
		log.WithField("chat_id", chatID).Debug("buildRemoteTUICtrlFn: local CLI TUI control enabled")
		return func(action string, params map[string]string) (map[string]string, error) {
			return cch.SendTUIControlRequest(chatID, action, params)
		}
	}
	log.WithField("type", fmt.Sprintf("%T", ch)).Debug("buildRemoteTUICtrlFn: channel is not RemoteCLIChannel or ChannelCliChannel")
	return nil
}

// listLLMSubsFn returns a subscription listing function for the given channel.
func (a *Agent) listLLMSubsFn(channel string) func(ch, senderID string) []tools.SubscriptionInfo {
	if a.llmFactory == nil {
		return nil
	}
	svc := a.llmFactory.GetSubscriptionSvc()
	if svc == nil {
		return nil
	}
	return func(ch, senderID string) []tools.SubscriptionInfo {
		subs, _ := svc.List(senderID)
		result := make([]tools.SubscriptionInfo, 0, len(subs))
		for _, s := range subs {
			result = append(result, tools.SubscriptionInfo{
				ID:        s.ID,
				Name:      s.Name,
				Provider:  s.Provider,
				Model:     s.Model,
				IsDefault: s.IsDefault,
			})
		}
		return result
	}
}

// LLMFactory returns the Agent's LLMFactory (for external injection of callbacks).
func (a *Agent) LLMFactory() *LLMFactory { return a.llmFactory }

// SetLLMFactory sets the LLM factory (used in tests).
func (a *Agent) SetLLMFactory(f *LLMFactory) { a.llmFactory = f }

// BgTaskManager returns the Agent's BackgroundTaskManager.
func (a *Agent) BgTaskManager() *tools.BackgroundTaskManager { return a.bgTaskMgr }

// SetMessageSender sets the Dispatcher reference for unified messaging.
func (a *Agent) SetMessageSender(ms bus.MessageSender) { a.messageSender = ms }

// SetAgentChannelRegistry sets the callbacks for registering/unregistering AgentChannels.
func (a *Agent) SetAgentChannelRegistry(register func(name string, runFn bus.RunFn) error, unregister func(name string)) {
	a.registerAgentChannel = register
	a.unregisterAgentChannel = unregister
}

// RegistryManager returns the Agent's RegistryManager (for external injection of callbacks).
func (a *Agent) RegistryManager() *RegistryManager { return a.registryManager }

// SettingsService returns the Agent's SettingsService (for external injection of callbacks).
func (a *Agent) SettingsService() *SettingsService { return a.settingsSvc }

// MultiSession returns the Agent's MultiTenantSession (for external injection of callbacks).
func (a *Agent) MultiSession() *session.MultiTenantSession { return a.multiSession }

// SetUserModel sets the model for a user's LLM configuration (used by settings card callback).
func (a *Agent) SetUserModel(senderID, model string) error {
	cfg, err := a.llmConfigSvc.GetConfig(senderID)
	if err != nil {
		return fmt.Errorf("get LLM config: %w", err)
	}
	if cfg == nil {
		return fmt.Errorf("user has no custom LLM config; use /set-llm first")
	}
	cfg.Model = model
	if err := a.llmConfigSvc.SetConfig(cfg); err != nil {
		return fmt.Errorf("save model: %w", err)
	}
	a.llmFactory.Invalidate(senderID)
	return nil
}

// SetChannelFinder sets the channel finder callback (for external injection).
// Also propagates to SettingsService so it can resolve channels by name.
func (a *Agent) SetChannelFinder(fn func(name string) (channel.Channel, bool)) {
	a.channelFinder = fn
	if a.settingsSvc != nil {
		a.settingsSvc.SetChannelFinder(fn)
	}
}

// emitSessionState pushes a session state event to CLI and Web channels.
// Uses channelFinder to locate channels and type-asserts to SessionStateSender.
func (a *Agent) emitSessionState(ev protocol.SessionEvent) {
	if a.channelFinder == nil {
		return
	}
	for _, name := range []string{"cli", "web"} {
		ch, ok := a.channelFinder(name)
		if !ok {
			continue
		}
		if sender, ok := ch.(channel.SessionStateSender); ok {
			sender.SendSessionState(ev)
		}
	}
}

// renameSession renames a chat session in DB and pushes the state change.
// Uses multiSession.DB() for DB access and emitSessionState for notification.
func (a *Agent) renameSession(chatID, newName string) (oldName string, err error) {
	if a.multiSession == nil {
		return "", fmt.Errorf("renameSession: no multiSession DB")
	}
	db := a.multiSession.DB()
	if db == nil {
		return "", fmt.Errorf("renameSession: no DB connection")
	}
	conn := db.Conn()

	// Look up channel & sender from DB (works for both CLI and web chats)
	var ch, senderID string
	row := conn.QueryRow(`SELECT channel, sender_id FROM user_chats WHERE chat_id = ? LIMIT 1`, chatID)
	if err := row.Scan(&ch, &senderID); err != nil {
		// Fallback for CLI sessions not yet in user_chats
		ch = "cli"
		senderID = a.cliSenderID
	}

	// Get old name
	row = conn.QueryRow(`SELECT label FROM user_chats WHERE channel = ? AND sender_id = ? AND chat_id = ?`, ch, senderID, chatID)
	_ = row.Scan(&oldName)
	if oldName == "" {
		_, oldName = channel.ParseChatID(chatID)
	}

	// Deduplicate
	finalName := channel.DeduplicateSessionName(newName, chatID, func() []channel.NameEntry {
		rows, err := conn.Query(`SELECT chat_id, label FROM user_chats WHERE channel = ? AND sender_id = ? AND label != ''`, ch, senderID)
		if err != nil {
			return nil
		}
		defer rows.Close()
		var entries []channel.NameEntry
		for rows.Next() {
			var cid, lbl string
			if err := rows.Scan(&cid, &lbl); err == nil {
				entries = append(entries, channel.NameEntry{Name: lbl, ChatID: cid})
			}
		}
		return entries
	})

	// Update DB
	_, err = conn.Exec(`
		INSERT INTO user_chats (channel, sender_id, chat_id, label)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(channel, sender_id, chat_id) DO UPDATE SET label = ?`,
		ch, senderID, chatID, finalName, finalName,
	)
	if err != nil {
		return "", fmt.Errorf("rename chat in DB: %w", err)
	}

	// Push state change
	a.emitSessionState(protocol.SessionEvent{
		Channel: ch,
		ChatID:  chatID,
		Action:  "renamed",
		Label:   finalName,
	})

	return oldName, nil
}

// IsProcessing returns true if there is an active Run for the given sender.
func (a *Agent) IsProcessing(senderID string) bool {
	found := false
	a.chatCancelCh.Range(func(key, _ any) bool {
		if k, ok := key.(string); ok && strings.HasSuffix(k, ":"+senderID) {
			found = true
			return false
		}
		return true
	})
	return found
}

// SetProxyLLM injects a ProxyLLM for a user (when their active runner has local LLM).
func (a *Agent) SetProxyLLM(senderID string, proxy *llm.ProxyLLM, model string) {
	a.llmFactory.SetProxyLLM(senderID, proxy, model)
}

// ClearProxyLLM removes a ProxyLLM for a user.
func (a *Agent) ClearProxyLLM(senderID string) {
	a.llmFactory.ClearProxyLLM(senderID)
}

// GetDefaultModel returns the default model name.
func (a *Agent) GetDefaultModel() string {
	return a.llmFactory.GetDefaultModel()
}
func (a *Agent) GetSettingsService() *SettingsService {
	return a.settingsSvc
}

func buildToolMessageContent(result *tools.ToolResult) string {
	if result == nil {
		return ""
	}
	// 将 Summary + Detail + Tips 组合为纯文本，避免 JSON 序列化转义换行符。
	// 旧方案用 json.Marshal(result) 导致 Detail 中的 diff 换行被编码为 \n，
	// LLM 看到的是不可读的文本块而非格式化的 diff。
	var sb strings.Builder
	if result.Summary != "" {
		sb.WriteString(result.Summary)
	}
	if result.Detail != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(result.Detail)
	}
	if result.Tips != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(result.Tips)
	}
	return sb.String()
}

// Config Agent 配置
type Config struct {
	Bus             *bus.MessageBus
	LLM             llm.LLM
	Model           string
	MaxIterations   int           // 单次对话最大工具调用迭代次数
	MaxConcurrency  int           // 最大并发会话处理数（默认 3）
	DBPath          string        // SQLite 数据库路径（空则使用默认路径）
	SkillsDir       string        // Skills 目录
	AgentsDir       string        // Agents 目录（空则使用 WorkDir/.xbot/agents）
	WorkDir         string        // 工作目录（所有文件相对此目录）
	PromptFile      string        // 系统提示词模板文件路径（空则使用内置默认值）
	DirectWorkspace string        `json:"-"` // 非空时直接作为 workspaceRoot（CLI 模式使用）
	SandboxMode     string        // 沙箱模式: "none" 或 "docker"（默认 "docker"）
	Sandbox         tools.Sandbox // Sandbox 实例引用（V4 新增）

	SandboxIdleTimeout time.Duration // 沙箱空闲超时（0 禁用）

	MemoryProvider     string // 记忆提供者: "flat" 或 "letta"
	EmbeddingProvider  string // 嵌入提供者: "openai"(默认) 或 "ollama"
	EmbeddingBaseURL   string // 嵌入向量服务地址
	EmbeddingAPIKey    string // 嵌入向量服务密钥
	EmbeddingModel     string // 嵌入模型名称
	EmbeddingMaxTokens int    // 嵌入模型最大 token 数

	// XbotHome is the global xbot config directory (e.g. ~/.xbot).
	// Used to locate global config files like mcp.json.
	XbotHome string

	// MCP 会话管理配置
	MCPInactivityTimeout time.Duration // MCP 不活跃超时时间
	MCPCleanupInterval   time.Duration // MCP 清理扫描间隔
	SessionCacheTimeout  time.Duration // 会话缓存超时

	// 上下文管理模式
	// 优先级：ContextMode > EnableAutoCompress 旧字段
	// 默认 ""，由 resolveContextMode 决定
	ContextMode ContextMode

	// Persona isolation: each web user has independent persona (no fallback to global)
	PersonaIsolation bool

	// 旧压缩配置（保留用于初始化 ContextManagerConfig，向后兼容 main.go 传参）
	MaxContextTokens     int     // 最大上下文 token 数（默认 100000）
	CompressionThreshold float64 // 触发压缩的 token 比例阈值（默认 0.7）
	EnableAutoCompress   bool    // 是否启用自动上下文压缩（默认 true，旧字段）

	// DynamicMaxTokens dynamically adjusts max_output_tokens based on remaining
	// context space. When enabled, max_output_tokens is reduced when the context
	// is large to prevent context_window_exceeded errors.
	DynamicMaxTokens bool

	// SubAgent 深度控制
	MaxSubAgentDepth int // SubAgent 最大嵌套深度（默认 6）

	// 压缩后清理旧消息
	PurgeOldMessages bool // 压缩后自动删除旧消息（默认 false）

	// OffloadDir: offload 文件存储目录（默认 ~/.xbot/offload_store）
	OffloadDir string

	// MaskDir: mask 文件存储基目录（默认 ~/.xbot/mask/{tenantID}）
	MaskDir string

	// Plugin system configuration
	PluginEnabled         bool     // Enable plugin system
	PluginDirs            []string // Additional plugin directories
	PluginDisabledPlugins []string // Plugin IDs to disable

	// AutoWorktree enables automatic git worktree creation when multiple
	// sessions share the same repo. Set from config.Agent.Experimental.AutoWorktree.
	AutoWorktree bool

	// CLISenderID is the sender_id used for CLI channel DB operations (default: "cli_user").
	CLISenderID string
}

// initStores 初始化各类存储和注册表，返回 skillStore, agentStore, chatHistory, registry, cardBuilder。

func initStores(cfg Config) (*SkillStore, *AgentStore, *tools.ChatHistoryStore, *tools.Registry, *tools.CardBuilder) {
	globalSkillDirs := resolveGlobalSkillsDirs(cfg.SkillsDir)

	skillStore := NewSkillStore(cfg.WorkDir, globalSkillDirs, cfg.Sandbox)

	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	agentsDir := cfg.AgentsDir
	if agentsDir == "" {
		agentsDir = filepath.Join(cfg.WorkDir, ".xbot", "agents")
	}
	if err := tools.InitAgentRoles(agentsDir); err != nil {
		log.WithError(err).Warn("Failed to load agent roles, SubAgent will have no predefined roles")
	}
	agentStore := NewAgentStore(cfg.WorkDir, agentsDir, cfg.Sandbox)

	// 确定记忆模式
	registry := tools.DefaultRegistry(resolveMemoryProvider(cfg.MemoryProvider))

	// 创建聊天历史存储
	chatHistory := tools.NewChatHistoryStore(200) // 每个群组保留最近 200 条
	registry.Register(tools.NewChatHistoryTool(chatHistory))

	// MCP global config: use xbotHome directly (~/.xbot/mcp.json).
	// resolveDataPath would double-nest to ~/.xbot/.xbot/mcp.json.
	xbotHome := cfg.XbotHome
	if xbotHome == "" {
		xbotHome = cfg.WorkDir
	}
	mcpConfigPath := filepath.Join(xbotHome, "mcp.json")

	// 注册 ManageTools tool（需要 skillStore 和 mcpConfigPath）
	registry.RegisterCore(tools.NewManageTools(cfg.WorkDir, mcpConfigPath))

	cardBuilder := tools.NewCardBuilder()
	for _, t := range tools.NewCardTools(cardBuilder) {
		registry.Register(t)
	}

	// Clean up expired waiting cards from previous runs (TTL: 24h)
	if n := cardBuilder.CleanupExpiredWaitingCards(24 * time.Hour); n > 0 {
		log.WithField("count", n).Info("Cleaned up expired waiting cards")
	}

	return skillStore, agentStore, chatHistory, registry, cardBuilder
}

// initSession 初始化多租户会话管理器。
func initSession(cfg Config) (*session.MultiTenantSession, error) {
	multiSession, err := session.NewMultiTenant(
		cfg.DBPath,
		session.WithMCPTimeout(cfg.MCPInactivityTimeout),
		session.WithCleanupInterval(cfg.MCPCleanupInterval),
		session.WithSessionCacheTimeout(cfg.SessionCacheTimeout),
		session.WithMemoryProvider(resolveMemoryProvider(cfg.MemoryProvider)),
		session.WithPersonaIsolation(cfg.PersonaIsolation),
		session.WithEmbeddingConfig(session.EmbeddingConfig{
			Provider:   cfg.EmbeddingProvider,
			BaseURL:    cfg.EmbeddingBaseURL,
			APIKey:     cfg.EmbeddingAPIKey,
			Model:      cfg.EmbeddingModel,
			MaxTokens:  cfg.EmbeddingMaxTokens,
			LLMClient:  cfg.LLM,
			LLMModel:   cfg.Model,
			TokenModel: cfg.Model,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("initialize multi-tenant session: %w", err)
	}
	return multiSession, nil
}

// initServices 注册工具、初始化 cron/LLM/offload/registry/settings 等服务。
// 此方法直接修改 Agent 指针。
func initServices(a *Agent, cfg Config, multiSession *session.MultiTenantSession, registry *tools.Registry) {
	// MCP config must use xbotHome directly (not resolveDataPath which double-nests).
	mcpConfigPath := filepath.Join(a.xbotHome, "mcp.json")
	contextMode := resolveContextMode(cfg)

	memoryProvider := resolveMemoryProvider(cfg.MemoryProvider)

	multiSession.SetMCPConfigPath(mcpConfigPath)

	// 设置会话被清理时的回调，同步清理 Registry 中的 sessionActivated/sessionRound（C-09）
	registryRef := registry // capture for closure
	multiSession.SetOnSessionEvict(func(sessionKey string) { registryRef.DeactivateSession(sessionKey) })

	// 设置会话 MCP 管理器提供者
	registry.SetSessionMCPManagerProvider(multiSession)

	// 全局工具索引通过 IndexGlobalTools() 在所有工具注册完成后调用

	// 如果使用 Letta 记忆模式，注册记忆工具（核心工具，始终可用）
	if memoryProvider == "letta" {
		for _, tool := range tools.LettaMemoryTools() {
			registry.RegisterCore(tool)
		}
		registry.RegisterCore(&tools.SearchToolsTool{})
		log.Info("Letta memory tools registered (core)")
	}

	// Flat 模式：注册 flat memory tools（memory_read/write/list）
	if memoryProvider == "flat" {
		for _, tool := range tools.FlatMemoryTools() {
			registry.RegisterCore(tool)
		}
		log.Info("Flat memory tools registered (core)")
	}

	log.Info("Knowledge tools removed — project knowledge is managed via AGENTS.md + docs/agent/")

	// 初始化指令注册表
	a.commands = NewCommandRegistry()
	registerBuiltinCommands(a.commands)

	// 初始化 Cron 服务和调度器
	cronSvc := sqlite.NewCronService(multiSession.DB())
	cronSch := cron.NewScheduler(cronSvc)

	// 从旧的 JSON 文件迁移数据（如果需要）
	if err := cronSvc.MigrateFromJSON(cfg.WorkDir); err != nil {
		log.WithError(err).Warn("Failed to migrate cron jobs from JSON")
	}

	// 注册 CronTool（核心工具，始终可用）
	registry.RegisterCore(tools.NewCronTool(cronSvc))

	a.cronSvc = cronSvc
	a.cronSch = cronSch

	// Initialize UserLLMConfigService
	a.llmConfigSvc = sqlite.NewUserLLMConfigService(multiSession.DB())
	a.llmFactory = NewLLMFactory(a.llmConfigSvc, cfg.LLM, cfg.Model)
	a.llmFactory.SetSubscriptionSvc(sqlite.NewLLMSubscriptionService(multiSession.DB()))

	// 初始化上下文管理器
	a.contextManagerConfig = &ContextManagerConfig{
		MaxContextTokens:     cfg.MaxContextTokens,
		CompressionThreshold: cfg.CompressionThreshold,
		DefaultMode:          contextMode,
	}
	a.contextManager = NewContextManager(a.contextManagerConfig)

	// 初始化 OffloadStore（Phase 2: Layer 1 Offload）
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	offloadDir := cfg.OffloadDir
	if offloadDir == "" {
		offloadDir = filepath.Join(cfg.WorkDir, ".xbot", "offload_store")
	}
	a.offloadStore = NewOffloadStore(OffloadConfig{
		StoreDir:        offloadDir,
		MaxResultTokens: 2000,
		MaxResultBytes:  10240,
		CleanupAgeDays:  7,
	})

	// Inject sandbox into OffloadStore for remote mode file hash computation
	if a.sandbox != nil {
		a.offloadStore.SetSandbox(a.sandbox)
	}

	// 初始化 ObservationMaskStore（Phase 3: Observation Masking）
	// 默认关闭：通过 settings 的 enable_masking 开启。
	// 始终创建（工具注册需要），但 engine 层通过 RunConfig.MaskStore 控制。
	// 磁盘落在全局 ~/.xbot/mask/{tenantID}/，避免污染当前工作目录。
	maskDir := cfg.MaskDir
	if maskDir == "" {
		maskDir = filepath.Join(a.xbotHome, "mask")
	}
	a.maskStore = NewObservationMaskStore(200)
	a.maskStore.SetBaseDir(maskDir)

	// Start periodic cleanup for offload and mask data.
	// Runs immediately at startup, then every 6 hours.
	a.cleanupStopCh = make(chan struct{})
	go a.periodicCleanup()

	// 注册 offload_recall 工具（需要 OffloadStore 依赖注入）
	if a.offloadStore != nil {
		recallTool := &tools.OffloadRecallTool{Store: a.offloadStore}
		registry.RegisterCore(recallTool)
	}

	// 注册 recall_masked 工具（需要 MaskStore 依赖注入）
	if a.maskStore != nil {
		registry.RegisterCore(&tools.RecallMaskedTool{Store: a.maskStore})
	}

	// 初始化 ContextEditor（Context Editing 工具 — 精确编辑上下文）
	editStore := NewContextEditStore(100)
	contextEditor := NewContextEditor(editStore)
	a.contextEditor = contextEditor
	// Wire up persistence callback for context edits (best-effort sync to DB).
	// IMPORTANT: PersistFn is called while ContextEditor.mu is held (write lock).
	// Do NOT acquire ContextEditor.mu inside PersistFn — deadlock!
	sessionSvc := sqlite.NewSessionService(multiSession.DB())
	contextEditor.PersistFn = func(editedIndices []int) {
		tenantID := contextEditor.tenantID
		if tenantID == 0 {
			return
		}
		// messages is safe to read here — caller (applyEdit/deleteTurn) holds the write lock
		msgs := contextEditor.messages
		if msgs == nil {
			return
		}
		for _, idx := range editedIndices {
			if idx < 0 || idx >= len(msgs) {
				continue
			}
			if err := sessionSvc.UpdateMessageContentNonDisplayOnly(tenantID, idx, msgs[idx].Content); err != nil {
				log.WithError(err).WithFields(log.Fields{
					"tenant_id": tenantID,
					"index":     idx,
				}).Warn("Failed to persist context edit to database")
			}
		}
	}
	registry.RegisterCore(&tools.ContextEditTool{Handler: contextEditor})

	// 初始化并注册 TODO 管理工具
	todoMgr := tools.NewTodoManager()
	a.todoManager = todoMgr
	registry.RegisterCore(&tools.TodoWriteTool{Manager: todoMgr})
	registry.RegisterCore(&tools.TodoListTool{Manager: todoMgr})

	// Register AI-Native TUI & Config tools as core (always available)
	registry.RegisterCore(&tools.TuiControlTool{})
	registry.RegisterCore(&tools.ConfigTool{})

	// Initialize SharedSkillRegistry
	sharedRegistry := sqlite.NewSharedSkillRegistry(multiSession.DB())

	// Initialize RegistryManager
	a.registryManager = NewRegistryManager(a.skills, a.agents, sharedRegistry, cfg.WorkDir, cfg.Sandbox)

	// Initialize UserSettingsService and SettingsService
	userSettingsSvc := sqlite.NewUserSettingsService(multiSession.DB())
	a.settingsSvc = NewSettingsService(userSettingsSvc)

	// Initialize LLMSemaphoreManager and inject dependencies
	llmSemMgr := llm.NewLLMSemaphoreManager()
	a.llmFactory.SetLLMSemaphoreManager(llmSemMgr)
	a.llmFactory.SetSettingsService(a.settingsSvc)

	// 初始化消息构建管道（必须在 settingsSvc 之后，LanguageMiddleware 依赖它）
	a.initPipelines(memoryProvider)
}

// New 创建 Agent
func New(cfg Config) (*Agent, error) {
	// 1. 设置配置默认值
	if cfg.MaxIterations == 0 {
		cfg.MaxIterations = 2000
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 3
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = "."
	}
	if cfg.SkillsDir == "" {
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		cfg.SkillsDir = filepath.Join(cfg.WorkDir, ".xbot", "skills")
	}
	if cfg.DBPath == "" {
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		cfg.DBPath = filepath.Join(cfg.WorkDir, ".xbot", "xbot.db")
	}
	if cfg.MCPInactivityTimeout == 0 {
		cfg.MCPInactivityTimeout = 30 * time.Minute
	}
	if cfg.MCPCleanupInterval == 0 {
		cfg.MCPCleanupInterval = 5 * time.Minute
	}
	if cfg.SessionCacheTimeout == 0 {
		cfg.SessionCacheTimeout = 24 * time.Hour
	}
	if cfg.MaxContextTokens == 0 {
		cfg.MaxContextTokens = 100000 // 默认 100k token
	}
	if cfg.CompressionThreshold == 0 {
		cfg.CompressionThreshold = 0.9
	}
	if cfg.MaxSubAgentDepth <= 0 {
		cfg.MaxSubAgentDepth = 6
	}
	if cfg.CLISenderID == "" {
		cfg.CLISenderID = "cli_user"
	}

	// 2. 初始化存储和注册表
	skillStore, agentStore, chatHistory, registry, cardBuilder := initStores(cfg)

	// 3. 初始化会话管理器
	multiSession, err := initSession(cfg)
	if err != nil {
		return nil, fmt.Errorf("init session: %w", err)
	}

	// 4. 构建 Agent 实例
	sandboxMode := cfg.SandboxMode
	if sandboxMode == "" {
		sandboxMode = "docker"
	}

	agent := &Agent{
		bus:              cfg.Bus,
		multiSession:     multiSession,
		tools:            registry,
		maxIterations:    cfg.MaxIterations,
		maxConcurrency:   cfg.MaxConcurrency,
		purgeOldMessages: cfg.PurgeOldMessages,

		skills:             skillStore,
		agents:             agentStore,
		chatHistory:        chatHistory,
		cardBuilder:        cardBuilder,
		workDir:            cfg.WorkDir,
		promptLoader:       NewPromptLoader(cfg.PromptFile),
		sandboxMode:        sandboxMode,
		sandbox:            cfg.Sandbox,
		sandboxIdleTimeout: cfg.SandboxIdleTimeout,
		directWorkspace:    cfg.DirectWorkspace,
		globalSkillDirs:    resolveGlobalSkillsDirs(cfg.SkillsDir),
		maxSubAgentDepth:   cfg.MaxSubAgentDepth,
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		agentsDir: filepath.Join(cfg.WorkDir, ".xbot", "agents"),
		xbotHome:  cfg.XbotHome,
		// timingData and approvalState are created before hookManager so they
		// can be shared: the same instances are registered as builtins and
		// exposed via accessor methods.
		timingData:    hooks.NewTimingData(),
		approvalState: hooks.NewApprovalState(nil), // handler set later by channel when available
		hookManager: func() *hooks.Manager {
			mgr, err := hooks.NewManager(cfg.XbotHome, cfg.WorkDir)
			if err != nil {
				log.WithError(err).Warn("Failed to load hooks config, using empty manager")
				mgr, _ = hooks.NewManager(cfg.XbotHome, cfg.WorkDir)
			}
			return mgr
		}(),
		bgTaskMgr:   tools.NewBackgroundTaskManager(),
		cliSenderID: cfg.CLISenderID,
	}

	// 5. 初始化各类服务（修改 agent 指针）
	initServices(agent, cfg, multiSession, registry)

	// 5b. Register builtin hooks on the shared hookManager.
	// Uses the same timingData/approvalState instances stored on the Agent.
	agent.hookManager.RegisterBuiltin(hooks.LoggingCallback())
	agent.hookManager.RegisterBuiltin(hooks.TimingCallback(agent.timingData))
	agent.hookManager.RegisterBuiltin(hooks.ApprovalCallback(agent.approvalState))

	// 5c. Initialize plugin system (if enabled in config)
	if cfg.PluginEnabled {
		agent.pluginMgr = plugin.NewPluginManager(cfg.XbotHome)
		agent.pluginMgr.SetRuntimeFactory(plugin.NewCompositeRuntimeFactory())
		// Set the agent's working directory so script plugins (e.g. git-info)
		// run in the user's workspace, not the plugin install dir.
		agent.pluginMgr.SetWorkDir(agent.workDir)
		// Set default ANSI render so server-side widget rendering (plugin_widgets RPC)
		// produces colored output. The TUI overrides this with lipgloss rendering.
		agent.pluginMgr.WidgetRegistry().SetDefaultRenderFn(plugin.BasicANSIRender)
		// Add extra plugin directories from config
		if len(cfg.PluginDirs) > 0 {
			agent.pluginMgr.AddSearchDirs(cfg.PluginDirs)
		}
		// Disable specific plugins from config
		if len(cfg.PluginDisabledPlugins) > 0 {
			agent.pluginMgr.DisablePlugins(cfg.PluginDisabledPlugins)
		}
		if _, err := agent.pluginMgr.Discover(context.Background()); err != nil {
			log.WithError(err).Warn("Plugin discovery failed")
		}
		if err := agent.pluginMgr.ActivateAll(context.Background()); err != nil {
			log.WithError(err).Warn("Plugin activation failed")
		}
		// Wire plugin capabilities to xbot subsystems
		hookBridge := plugin.NewPluginHookBridge()
		enricherReg := plugin.NewEnricherRegistry()
		if err := plugin.WireAll(agent.pluginMgr, registry, hookBridge, enricherReg); err != nil {
			log.WithError(err).Warn("Plugin wiring failed")
		}
		// Register the hook bridge as a builtin hook handler
		agent.hookManager.RegisterBuiltin(hooks.PluginBridgeCallback(hookBridge))
		// Wire enricher registry into the message pipeline
		agent.pipeline.Use(newPluginEnricherMiddleware(enricherReg))
		// Wire WidgetRegistry.OnUpdated to push widget content to remote CLI clients.
		// Local mode overrides this in CLIChannel.SetWidgetRegistry with asyncCh callback.
		// Remote mode uses this to push via Hub to all connected WebSocket clients.
		pm := agent.pluginMgr
		// Debounce widget push: coalesce rapid updates (e.g. multiple PostToolUse
		// triggers in a single agent iteration) into a single WebSocket message.
		pm.WidgetRegistry().SetDebounce(200 * time.Millisecond)
		pm.WidgetRegistry().OnUpdated(func() {
			if agent.channelFinder == nil {
				return
			}
			ch, ok := agent.channelFinder("cli")
			if !ok {
				return
			}
			rcli, ok := ch.(*channel.RemoteCLIChannel)
			if !ok {
				return // local CLIChannel handles its own OnUpdated
			}
			// Per-session rendering: each chatID may have a different workDir.
			// Uses plugin.RenderSessionWidgets — shared with plugin_widgets RPC handler
			// to ensure consistent rendering across push and pull paths.
			ms := agent.multiSession
			wr := pm.WidgetRegistry()
			rcli.PushPluginWidgetsPerSession(func(chatID string) map[string]string {
				getCWD := func(cid string) string {
					cwd := ""
					if ms != nil && cid != "" {
						if sess, err := ms.GetOrCreateSession("cli", cid); err == nil {
							cwd = sess.GetCurrentDir()
						}
					}
					// Fallback: if session CWD is empty, use persisted WorktreeRegistry entry
					if cwd == "" {
						sessKey := "cli:" + cid
						if entry := tools.GlobalWorktreeRegistry.GetBySession(sessKey); entry != nil && entry.WorktreeDir != "" {
							cwd = entry.WorktreeDir
						}
					}
					return cwd
				}
				zones := plugin.RenderSessionWidgets(wr, getCWD, chatID)
				log.Infof("[widget-push] chatID=%s cwd=%s infoBar=%q footer=%q", chatID, getCWD(chatID), zones["infoBar"], zones["footer"])
				return zones
			})
		})
		log.Infof("Plugin system initialized: %d active plugins", agent.pluginMgr.ActiveCount())
	} else {
		log.Debug("Plugin system disabled in config")
	}

	// 6. 启动 bg task 通知路由 goroutine
	go agent.bgNotifyLoop()

	return agent, nil
}

// GetContextManager 获取当前上下文管理器（读锁保护）。
// 用于 buildMainRunConfig / buildSubAgentRunConfig / handleCompress 等场景。
func (a *Agent) GetContextManager() ContextManager {
	a.contextManagerMu.RLock()
	defer a.contextManagerMu.RUnlock()
	return a.contextManager
}

// SetContextManager 替换当前上下文管理器（写锁保护）。
// 用于 /context mode 命令运行时切换。
func (a *Agent) SetContextManager(cm ContextManager) {
	a.contextManagerMu.Lock()
	defer a.contextManagerMu.Unlock()
	a.contextManager = cm
}

// GetContextMode returns the current effective context mode.
func (a *Agent) GetContextMode() string {
	return string(a.contextManagerConfig.EffectiveMode())
}

// SetContextMode changes the runtime context mode and rebuilds the context manager.
// Pass "default" to reset to the default mode.
func (a *Agent) SetContextMode(mode string) error {
	cfg := a.contextManagerConfig
	target := ContextMode(mode)

	if target == "default" {
		cfg.ResetRuntimeMode()
		a.SetContextManager(NewContextManager(cfg))
		return nil
	}

	// "auto" is a user-facing alias for "phase1" (automatic compression)
	if target == "auto" {
		target = ContextModePhase1
	}

	if !IsValidContextMode(target) {
		return fmt.Errorf("invalid mode %q; valid: phase1, auto, none, default", mode)
	}

	cfg.SetRuntimeMode(target)
	a.SetContextManager(NewContextManager(cfg))
	return nil
}

func (a *Agent) SetMaxIterations(n int) {
	a.contextManagerMu.Lock()
	a.maxIterations = n
	a.contextManagerMu.Unlock()
}
func (a *Agent) SetMaxConcurrency(n int) {
	a.contextManagerMu.Lock()
	a.maxConcurrency = n
	a.contextManagerMu.Unlock()
	// Rebuild global semaphore with new capacity
	a.globalSemMu.Lock()
	a.globalSem = make(chan struct{}, n)
	a.globalSemMu.Unlock()
	// Clear all cached user-level semaphores so they are recreated with the
	// new capacity on the next call to getUserSemaphore. Without this, users
	// with custom LLM keep using the old capacity forever (the cached chan
	// in userSemaphores sync.Map is never replaced by the old code).
	a.userSemaphores.Clear()
}

// SetMaxContextTokens sets the max context token limit.
// When chatID is non-empty, only the per-chat override is updated (session-scoped).
// When chatID is empty, the global agent-level config is updated (backward compatible).
func (a *Agent) SetMaxContextTokens(n int, chatID ...string) {
	if len(chatID) > 0 && chatID[0] != "" {
		// Per-session: store in LLMFactory's per-chat cache
		a.llmFactory.SetPerChatMaxContext(chatID[0], n)
	} else {
		// Global: update agent-level config (backward compatible)
		a.contextManagerMu.Lock()
		a.contextManagerConfig.MaxContextTokens = n
		a.contextManagerMu.Unlock()
	}
}

func (a *Agent) SetCompressionThreshold(f float64) {
	a.contextManagerMu.Lock()
	a.contextManagerConfig.CompressionThreshold = f
	a.contextManagerMu.Unlock()
}

func (a *Agent) getMaxIterations() int {
	a.contextManagerMu.RLock()
	defer a.contextManagerMu.RUnlock()
	return a.maxIterations
}

func (a *Agent) getMaxConcurrency() int {
	a.contextManagerMu.RLock()
	defer a.contextManagerMu.RUnlock()
	if a.maxConcurrency < 1 {
		return 1
	}
	return a.maxConcurrency
}

// getGlobalSem returns the current global semaphore channel.
// Must be called each time a semaphore is needed (not cached) so that
// SetMaxConcurrency rebuilds take effect immediately.
func (a *Agent) getGlobalSem() chan struct{} {
	a.globalSemMu.Lock()
	defer a.globalSemMu.Unlock()
	return a.globalSem
}

// SetSandbox replaces the sandbox instance and mode at runtime (e.g. when user
// switches from docker to none in the settings panel).
func (a *Agent) SetSandbox(sb tools.Sandbox, mode string) {
	a.sandbox = sb
	a.sandboxMode = mode
	if a.offloadStore != nil {
		a.offloadStore.SetSandbox(sb)
	}
}

// GetUserLLMConfig returns the user's LLM config summary (no API key), or nil if none.
func (a *Agent) GetUserLLMConfig(senderID string) (provider, baseURL, model string, ok bool) {
	cfg, err := a.llmConfigSvc.GetConfig(senderID)
	if err != nil || cfg == nil || (cfg.BaseURL == "" && cfg.APIKey == "") {
		return "", "", "", false
	}
	return cfg.Provider, cfg.BaseURL, cfg.Model, true
}

// SetUserLLM creates or replaces a user's full LLM config.
func (a *Agent) SetUserLLM(senderID, provider, baseURL, apiKey, model string) error {
	if provider == "" || baseURL == "" || apiKey == "" {
		return fmt.Errorf("provider, base_url, api_key 必填")
	}
	cfg := &sqlite.UserLLMConfig{
		SenderID: senderID,
		Provider: provider,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		Model:    model,
	}
	if err := a.llmConfigSvc.SetConfig(cfg); err != nil {
		return err
	}
	a.llmFactory.Invalidate(senderID)
	a.llmFactory.InvalidateCustomLLMCache(senderID)
	return nil
}

// DeleteUserLLM removes a user's LLM config and reverts to global.
func (a *Agent) DeleteUserLLM(senderID string) error {
	if err := a.llmConfigSvc.DeleteConfig(senderID); err != nil {
		return err
	}
	a.llmFactory.Invalidate(senderID)
	a.llmFactory.InvalidateCustomLLMCache(senderID)
	return nil
}

// GetLLMConcurrency 获取用户个人 LLM 并发上限配置。
func (a *Agent) GetLLMConcurrency(senderID string) int {
	return a.llmFactory.GetLLMConcurrency(senderID)
}

// SetLLMConcurrency 设置用户个人 LLM 并发上限配置。
func (a *Agent) SetLLMConcurrency(senderID string, personal int) error {
	return a.llmFactory.SetLLMConcurrency(senderID, personal)
}

// SetDirectSend 注入同步发送函数（绕过 bus，用于消息更新跟踪）
func (a *Agent) SetDirectSend(fn func(channel.OutboundMsg) (string, error)) {
	a.directSend = fn
}

// SetEventRouter sets the event trigger router.
// The router's InjectFunc is wired to injectEventMessage when Agent.Run starts.
func (a *Agent) SetEventRouter(r *event.Router) {
	a.eventRouter = r
}

// SetChannelPromptProviders 设置 channel 特化 prompt 提供者。
// 调用后会重建 pipeline，将 ChannelPromptMiddleware 插入到管道中。
func (a *Agent) SetChannelPromptProviders(providers ...ChannelPromptProvider) {
	a.channelPromptProviders = providers
	a.pipeline.Use(NewChannelPromptMiddleware(providers...))
}

// HookManager returns the Agent's shared hook manager for tool execution.
// Callers can use this to register hooks, emit events, etc.
func (a *Agent) HookManager() *hooks.Manager {
	return a.hookManager
}

// TimingData returns the shared timing statistics collector.
func (a *Agent) TimingData() *hooks.TimingData { return a.timingData }

// ApprovalState returns the shared approval state for privileged operations.
func (a *Agent) ApprovalState() *hooks.ApprovalState { return a.approvalState }

// GetCardBuilder returns the CardBuilder for card callback handling.
func (a *Agent) GetCardBuilder() *tools.CardBuilder {
	return a.cardBuilder
}

// getUserSemaphore 获取用户独立的信号量，用于有自定义 LLM 配置的用户。
// 容量与 maxConcurrency 一致：允许同一用户的不同会话并行处理，
// 但总并发不超过全局上限。
// 使用 LoadOrStore 原子操作避免并发创建多个信号量。
func (a *Agent) getUserSemaphore(senderID string) chan struct{} {
	if val, ok := a.userSemaphores.Load(senderID); ok {
		return val.(chan struct{})
	}
	sem, _ := a.userSemaphores.LoadOrStore(senderID, make(chan struct{}, a.getMaxConcurrency()))
	return sem.(chan struct{})
}

// Close 关闭 Agent 及其所有资源
func (a *Agent) Close() error {
	// Cancel agent-level context to stop background subagents
	if a.agentCancel != nil {
		a.agentCancel()
	}
	// Stop periodic cleanup goroutine
	if a.cleanupStopCh != nil {
		close(a.cleanupStopCh)
		a.cleanupStopCh = nil
	}
	// Deactivate all plugins before shutting down subsystems
	if a.pluginMgr != nil {
		a.pluginMgr.DeactivateAll(context.Background())
	}
	// 先停止 cron 调度器，避免在数据库关闭后仍尝试访问
	if a.cronSch != nil {
		a.cronSch.Stop()
	}
	// Close NotifyCh to unblock bgNotifyLoop goroutine
	if a.bgTaskMgr != nil && a.bgTaskMgr.NotifyCh != nil {
		close(a.bgTaskMgr.NotifyCh)
		a.bgTaskMgr.NotifyCh = nil
	}
	// 再关闭数据库连接
	if a.multiSession != nil {
		if err := a.multiSession.Close(); err != nil {
			log.WithError(err).Warn("MultiTenantSession close error")
		}
	}
	return nil
}

// PluginManager returns the plugin manager for this agent.
// Returns nil if the plugin system is not initialized.
func (a *Agent) PluginManager() *plugin.PluginManager {
	return a.pluginMgr
}

// NOTE: math/rand is intentionally used here for non-cryptographic random selection
// (picking a casual ack message). Go 1.20+ automatically seeds math/rand on package
// init, so there is no security concern and no explicit seeding is required.
var ackMessages = []string{
	"收到~",
	"好的，让我看看",
	"收到，处理中...",
	"了解，稍等~",
	"好的~",
	"嗯嗯，马上处理",
	"收到，稍等一下~",
	"OK，马上看看",
}

func (a *Agent) sendAck(channel, chatID string) {
	msg := ackMessages[rand.Intn(len(ackMessages))]
	if err := a.sendMessage(channel, chatID, msg); err != nil {
		log.WithError(err).Warn("Failed to send ack")
	}
}

// resetSessionState clears outbound message tracking state for the given session key.
// Called at the start of each new message to ensure clean state.
func (a *Agent) resetSessionState(key string) {
	a.sessionMsgIDs.Delete(key)
	a.sessionFinalSent.Delete(key)
}

// qualifyChatID combines channel name and chatID into the "channel:chatID" format
// used by TUI session filtering (handleInjectedUserMsg). All inject paths must
// use this helper instead of inline string concatenation.
func qualifyChatID(channel, chatID string) string {
	return channel + ":" + chatID
}

// injectCLIUserMessage sends a user message to the CLI channel if available.
// Used by background notification handlers to display messages in the CLI UI.
// Supports all three CLI channel types via UserMessageInjector interface:
// CLIChannel (local), RemoteCLIChannel (websocket), ChannelCliChannel (in-process server).
func (a *Agent) injectCLIUserMessage(channelName, chatID, content string) {
	if a.channelFinder == nil {
		log.WithFields(log.Fields{"channel": channelName, "chat_id": chatID}).Debug("injectCLIUserMessage: channelFinder is nil, skipping")
		return
	}
	ch, ok := a.channelFinder(channelName)
	if !ok {
		log.WithFields(log.Fields{"channel": channelName, "chat_id": chatID}).Warn("injectCLIUserMessage: channel not found via channelFinder")
		return
	}
	injector, ok := ch.(channel.UserMessageInjector)
	if !ok {
		log.WithFields(log.Fields{"channel": channelName, "chat_id": chatID}).Debug("injectCLIUserMessage: channel does not implement UserMessageInjector")
		return
	}
	injector.InjectUserMessage(qualifyChatID(channelName, chatID), content)
}

// Run 启动 Agent 循环，持续消费入站消息。
// 消息按 chat (channel:chatID) 分组，同一 chat 内顺序处理，不同 chat 并行处理。
// 全局并发数由 AGENT_MAX_CONCURRENCY 控制（默认 3），避免 LLM 并发过高。
// 用户设置了自己的 LLM 配置后，该用户的请求使用独立的信号量，不再占用全局资源。
func (a *Agent) Run(ctx context.Context) error {
	log.WithFields(log.Fields{
		"max_concurrency": a.getMaxConcurrency(),
	}).Info("Agent loop started")

	a.multiSession.StartCleanupRoutine()

	a.cronSch.SetNotifyCronFunc(func(channel, chatID, senderID, message string) {
		sessionKey := channel + ":" + chatID
		a.bgTaskMgr.SendCronFired(&tools.CronFired{
			Key:     sessionKey,
			Sid:     senderID,
			Message: message,
		})
	})
	a.cronSch.StartDelayed(3 * time.Second)

	if a.eventRouter != nil {
		a.eventRouter.SetInjectFunc(a.injectEventMessage)
	}

	// Set up Agent-level context for background interactive subagents.
	// Bg subagents derive from this ctx (not per-request ctx) so they survive across requests.
	a.agentCtx, a.agentCancel = context.WithCancel(ctx)
	defer func() {
		a.agentCancel() // cancel all bg subagents when Agent exits
		a.cronSch.Stop()
		a.multiSession.StopCleanupRoutine()
	}()

	sem := make(chan struct{}, a.getMaxConcurrency())
	a.globalSemMu.Lock()
	a.globalSem = sem
	a.globalSemMu.Unlock()

	var mu sync.Mutex
	chatQueues := make(map[string]chan bus.InboundMessage)
	var wg sync.WaitGroup

	// getOrCreateQueue 为每个 chat 创建独立的消息队列和 worker
	// 信号量在每次处理消息时动态选择（支持用户中途设置/取消自定义 LLM）
	getOrCreateQueue := func(key string) chan bus.InboundMessage {
		mu.Lock()
		defer mu.Unlock()
		if q, ok := chatQueues[key]; ok {
			return q
		}
		q := make(chan bus.InboundMessage, 32)
		chatQueues[key] = q

		wg.Go(func() {
			a.chatWorker(ctx, key, q)
			mu.Lock()
			delete(chatQueues, key)
			mu.Unlock()
		})
		return q
	}

	for {
		select {
		case <-ctx.Done():
			log.Info("Agent loop stopping, draining chat workers...")
			mu.Lock()
			for _, q := range chatQueues {
				close(q)
			}
			mu.Unlock()
			wg.Wait()
			log.Info("Agent loop stopped")
			return ctx.Err()
		case msg := <-a.bus.Inbound:

			// /cancel 拦截：不进入 chatWorker 队列，直接发 cancel 信号
			// cancel key 仅用 channel:chatID（不含 senderID），因为同一个 chat
			// 同时只有一个活跃请求（chatQueue 串行化），且 bg task / cron 等
			// 系统通知的 senderID 与 CLI 用户的 senderID 可能不同。
			if strings.TrimSpace(strings.ToLower(msg.Content)) == "/cancel" {
				cancelKey := msg.Channel + ":" + msg.ChatID
				cancelMeta := map[string]string{"cancelled": "true"}
				log.WithField("cancel_key", cancelKey).Info("Received /cancel request")
				if ch, ok := a.chatCancelCh.Load(cancelKey); ok {
					select {
					case ch.(chan struct{}) <- struct{}{}:
						log.Info("Cancel signal sent to processing goroutine")
						_ = a.sendMessage(msg.Channel, msg.ChatID, "Request cancelled.", cancelMeta)
					default:
						// cancel 信号已发过
						log.WithField("cancel_key", cancelKey).Warn("Cancel signal already sent (buffer full)")
					}
				} else {
					// cancelCh 尚未注册（消息还在排队或等信号量），记录 pending
					a.pendingCancel.Store(cancelKey, true)
					log.WithField("cancel_key", cancelKey).Info("Cancel pending: request not yet active, will cancel when it starts")
					_ = a.sendMessage(msg.Channel, msg.ChatID, "Request queued for cancellation.", cancelMeta)
				}
				continue
			}

			key := msg.Channel + ":" + msg.ChatID
			q := getOrCreateQueue(key)
			select {
			case q <- msg:
			default:
				log.WithFields(log.Fields{"request_id": msg.RequestID, "chat": key}).Warn("Chat queue full, dropping message")
			}
		}
	}
}

// workspaceRoot returns the workspace root for the given sender.
// If DirectWorkspace is set (e.g. CLI mode), returns it directly (no per-user subdirectory).
// Otherwise, returns per-user workspace directory.
func (a *Agent) workspaceRoot(senderID string) string {
	if a.directWorkspace != "" {
		return a.directWorkspace
	}
	return tools.UserWorkspaceRoot(a.workDir, senderID)
}

// isRemoteUser checks whether the given user routes to a remote sandbox.
// Uses SandboxResolver for per-user routing instead of checking Name() on the
// global SandboxRouter (which returns "router", not "remote").
func (a *Agent) isRemoteUser(userID string) bool {
	return a.sandboxNameForUser(userID) == "remote"
}

// sandboxNameForUser resolves the sandbox name for a given user.
func (a *Agent) sandboxNameForUser(userID string) string {
	if a.sandbox == nil {
		return ""
	}
	if resolver, ok := a.sandbox.(tools.SandboxResolver); ok {
		return resolver.SandboxForUser(userID).Name()
	}
	return a.sandbox.Name()
}

// remoteWorkspace returns the remote runner's workspace for the given user.
// Returns "" if the user is not on a remote sandbox or has no active connection.
// Note: sandboxWorkspace covers all sandbox modes (docker/remote/none) but
// this function is kept for the promptWorkDir fallback path where we need
// to distinguish remote-runner from in-process docker sandbox.
func (a *Agent) remoteWorkspace(userID string) string {
	if a.sandbox == nil {
		return ""
	}
	if resolver, ok := a.sandbox.(tools.SandboxResolver); ok {
		return resolver.SandboxForUser(userID).Workspace(userID)
	}
	if a.sandbox.Name() == "remote" {
		return a.sandbox.Workspace(userID)
	}
	return ""
}

// sandboxWorkspace returns the correct workspace path for sandbox file operations.
// For docker mode: returns "/workspace" (the container-internal mount point).
// For remote mode: returns the runner's registered workspace.
// For none/local mode: returns the host-side user workspace root.
func (a *Agent) sandboxWorkspace(userID string) string {
	if a.sandbox == nil {
		return a.workspaceRoot(userID)
	}
	sb := a.sandbox
	if resolver, ok := sb.(tools.SandboxResolver); ok {
		sb = resolver.SandboxForUser(userID)
	}
	switch sb.Name() {
	case "docker":
		return sb.Workspace(userID) // "/workspace"
	case "remote":
		return sb.Workspace(userID) // runner's workspace
	default:
		return a.workspaceRoot(userID)
	}
}

// ensureWorkspace ensures the workspace directory exists (sandbox-aware).
// Skipped for remote, docker, and denied sandboxes — they manage their own filesystems
// or don't need host-side directories.
func (a *Agent) ensureWorkspace(ctx context.Context, dir, senderID string) error {
	name := a.sandboxNameForUser(senderID)
	if name == "remote" || name == "docker" || name == "denied" || name == "none" {
		return nil
	}
	if a.sandbox != nil {
		return a.sandbox.MkdirAll(ctx, dir, 0o755, senderID)
	}
	return os.MkdirAll(dir, 0o755)
}

// isGroupChat 判断是否为群聊
// 使用消息的 ChatType 字段：p2p 为私聊，group 为群聊
func (a *Agent) isGroupChat(msg bus.InboundMessage) bool {
	return msg.ChatType == "group"
}

// getSemaphoreForMessage 获取消息应该使用的信号量
// 私聊：用户有自定义 LLM 则使用独立信号量
// 群聊：始终使用全局信号量（因为群里有多人，使用独立信号量会导致其他人的消息也被阻塞）
func (a *Agent) getSemaphoreForMessage(msg bus.InboundMessage) chan struct{} {
	globalSem := a.getGlobalSem()
	senderID := msg.SenderID
	if senderID == "" {
		return globalSem
	}

	// 群聊使用全局信号量
	if a.isGroupChat(msg) {
		return globalSem
	}

	// 私聊：检查用户是否有自定义 LLM
	if a.llmFactory.HasCustomLLM(senderID) {
		return a.getUserSemaphore(senderID)
	}

	return globalSem
}

// chatWorker 处理单个 chat 的消息队列，保证同一 chat 内顺序处理。
// 通过信号量控制并发：获取信号量后才开始处理，处理完释放。
// 信号量在每次处理消息时动态选择，以支持用户中途设置/取消自定义 LLM。
// chatWorker 处理单个 chat 的消息队列。
// 主循环持续从 ch 取消息并分发：
//   - 指令消息（/version, /help 等）：独立 goroutine 立即执行，不阻塞
//   - 普通消息：发送到内部 msgCh，由专门的 goroutine 串行处理（带信号量 + cancel）
//   - bg通知信号：当chatProcessLoop空闲时，drain并处理pending通知
//
// 这样即使普通消息正在长时间处理（LLM 推理），主循环仍能取出并执行命令消息。
func (a *Agent) chatWorker(ctx context.Context, chatKey string, ch <-chan bus.InboundMessage) {
	// 内部普通消息队列：主循环写入，processLoop 消费
	msgCh := make(chan bus.InboundMessage, 32)

	// Register per-session bg notification state
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(chatKey, ss)
	defer a.bgSessionStates.Delete(chatKey)

	var wg sync.WaitGroup
	wg.Add(1)
	clipanic.Go("agent.chatWorker.processLoop", func() {
		defer wg.Done()
		a.chatProcessLoop(ctx, chatKey, msgCh, ss)
	})

	defer func() {
		close(msgCh)
		wg.Wait()
	}()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if ctx.Err() != nil {
				return
			}

			// 指令消息分发：根据 Concurrent() 决定执行方式
			if cmd := a.commands.Match(msg.Content); cmd != nil {
				if cmd.Concurrent() {
					// 无状态命令：独立 goroutine 处理，不占信号量，不阻塞
					m := msg
					c := cmd
					clipanic.Go("agent.chatWorker.concurrentCommand", func() {
						// 清除 sessionFinalSent：command 不走 processMessage，
						// 需要手动清除否则 sendMessage 会被拦截
						cmdKey := qualifyChatID(m.Channel, m.ChatID)
						a.resetSessionState(cmdKey)

						response, err := c.Execute(ctx, a, m)
						if err != nil {
							log.WithFields(log.Fields{"request_id": m.RequestID, "chat": chatKey}).WithError(err).Error("Error processing command")
							content := formatErrorForUser(err)
							if sendErr := a.sendMessage(m.Channel, m.ChatID, content); sendErr != nil {
								a.bus.Outbound <- bus.OutboundMessage{
									Channel: m.Channel,
									ChatID:  m.ChatID,
									Content: content,
								}
							}
							return
						}
						if response != nil {
							if sendErr := a.sendMessage(m.Channel, m.ChatID, response.Content, response.Metadata); sendErr != nil {
								a.bus.Outbound <- bus.OutboundMessage{
									Channel: response.Channel,
									ChatID:  response.ChatID,
									Content: response.Content,
									Media:   response.Media,
								}
							}
						}
					})
				} else {
					// 有状态命令（/new, /compress, /set-llm 等）：走串行队列，
					// 避免与正在处理的普通消息产生 session 数据竞态
					select {
					case msgCh <- msg:
					case <-ctx.Done():
						return
					}
				}
				continue
			}

			// 普通消息：转发到内部队列，由 processLoop 串行处理
			select {
			case msgCh <- msg:
			case <-ctx.Done():
				return
			}

		case <-ss.notifyCh:
			// bg notification arrived — drain and process ONLY when chatProcessLoop is idle.
			// When busy, notifications stay in bgRunPending for chatProcessLoop's
			// post-turn drain to pick up (guaranteed after response is sent).
			if !ss.busy.Load() {
				a.drainAndProcessNotifications(chatKey)
			}

		case <-ctx.Done():
			return
		}
	}
}

// chatProcessLoop 串行处理普通消息（非命令），带信号量控制和 per-request cancel 支持。
// After each turn completes (response sent), drains pending bg notifications
// at a safe point where injectCLIUserMessage cannot race with the turn's reply.
func (a *Agent) chatProcessLoop(ctx context.Context, chatKey string, ch <-chan bus.InboundMessage, ss *bgSessionState) {
	var idleTimer *time.Timer
	defer func() {
		if idleTimer != nil {
			idleTimer.Stop()
		}
	}()

	var lastSenderID string // 记录最后活跃的 senderID

	for msg := range ch {
		if ctx.Err() != nil {
			return
		}

		// Mark session busy so chatWorker skips notification drain
		ss.busy.Store(true)

		// 停止上一次的 idle timer（收到新消息，重置计时）
		if idleTimer != nil {
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
		}

		sem := a.getSemaphoreForMessage(msg)

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			ss.busy.Store(false)
			return
		}

		// 创建 per-request cancel context
		var response *channel.OutboundMsg
		var err error
		cancelCh := make(chan struct{}, 1)
		// cancelKey 仅用 channel:chatID（不含 senderID），与 /cancel 拦截处保持一致
		cancelKey := msg.Channel + ":" + msg.ChatID
		a.chatCancelCh.Store(cancelKey, cancelCh)

		// Emit session busy event for instant sidebar push.
		a.emitSessionState(protocol.SessionEvent{
			Channel: msg.Channel, ChatID: msg.ChatID, Action: "busy",
		})

		// 消费 pending cancel：如果 /cancel 在消息排队期间已到达，立即发信号。
		// Track whether we consumed one so we can cancel the context synchronously
		// before processMessage starts — relying solely on the goroutine below
		// races with processMessage on the first message after restart.
		hadPending := false
		if _, pending := a.pendingCancel.LoadAndDelete(cancelKey); pending {
			select {
			case cancelCh <- struct{}{}:
				hadPending = true
				log.WithField("cancel_key", cancelKey).Info("Consumed pending cancel signal")
			default:
			}
		}

		reqCtx, reqCancel := context.WithCancel(ctx)

		// Synchronous cancel: if a pending cancel was consumed above, cancel
		// the context NOW before processMessage starts. reqCancel is idempotent
		// (called again in defer) and guarantees processMessage receives an
		// already-canceled context, avoiding the LLM call entirely.
		if hadPending {
			reqCancel()
		}

		// 监听 cancel 信号（处理 processMessage 运行期间到达的 cancel）。
		// Loop to handle multiple cancel requests — the goroutine was previously
		// a single select{}, which exited after the first cancel. If the user
		// pressed Ctrl+C again (because the agent didn't appear to stop), the
		// channel reader was gone and subsequent sends got "buffer full".
		clipanic.Go("agent.chatProcessLoop.cancelListener", func() {
			for {
				select {
				case <-cancelCh:
					reqCancel()
				// reqCancel is idempotent — calling it multiple times is safe.
				// Drain any additional signals that arrived between calls.
				case <-reqCtx.Done():
					return
				}
			}
		})

		// 执行消息处理，完成后检查是否被取消
		// 注意：必须在 reqCancel() 调用前检查，否则 reqCtx.Err() 总是返回 Canceled
		wasCancelled := false
		func() {
			defer func() {
				reqCancel()
				a.chatCancelCh.Delete(cancelKey)
				a.pendingCancel.Delete(cancelKey)

				// Emit session idle event for instant sidebar push.
				a.emitSessionState(protocol.SessionEvent{
					Channel: msg.Channel, ChatID: msg.ChatID, Action: "idle",
				})
				key := qualifyChatID(msg.Channel, msg.ChatID)
				a.lastProgressSnapshot.Delete(key)
				a.iterationHistories.Delete(key)
				<-sem // 释放槽位
			}()

			// 沙箱正在 export+import 时，拒绝该用户所有请求
			sbUID := sandboxUserID(msg)
			if sb := tools.GetSandbox(); sb.IsExporting(sbUID) {
				log.WithFields(log.Fields{"request_id": msg.RequestID, "sender": msg.SenderID, "sandbox_user": sbUID}).Info("Request rejected: sandbox export in progress")
				a.sendMessage(msg.Channel, msg.ChatID, "⏳ 沙箱正在持久化中，请稍后再试...")
				return
			}

			response, err = a.processMessage(reqCtx, msg)
			// 在 defer 执行前检查是否被取消（processMessage 过程中用户可能 /cancel）
			if reqCtx.Err() == context.Canceled {
				wasCancelled = true
			}
		}()

		if wasCancelled && ctx.Err() == nil {
			// 请求被用户 /cancel 取消（而非全局 ctx 关闭）
			log.WithFields(log.Fields{"request_id": msg.RequestID, "chat": chatKey}).Info("Request cancelled by user")
			// 即使取消也要发送 response，让 CLI 清理 typing/progress 状态。
			// Always include cancelled metadata so CLI can distinguish cancel acks
			// from normal replies and avoid ending a subsequently-started turn.
			cancelMeta := map[string]string{"cancelled": "true"}
			if response != nil {
				// Merge cancelled into existing metadata
				if response.Metadata == nil {
					response.Metadata = cancelMeta
				} else {
					response.Metadata["cancelled"] = "true"
				}
				_ = a.sendMessage(msg.Channel, msg.ChatID, response.Content, response.Metadata)
			} else {
				// No response generated yet (cancelled mid-tool-call) — send empty
				// message to signal turn end so CLI can clean up typing/progress state.
				_ = a.sendMessage(msg.Channel, msg.ChatID, "", cancelMeta)
			}
			// Turn done — response sent, safe to drain bg notifications
			ss.busy.Store(false)
			a.drainAndProcessNotifications(chatKey)
			continue
		}

		if err != nil {
			log.WithFields(log.Fields{"request_id": msg.RequestID, "chat": chatKey}).WithError(err).Error("Error processing message")
			// 走 sendMessage 与正常回复同一路径：可 Patch 已发出的进度条为错误内容，避免错误静默不达用户
			content := formatErrorForUser(err)
			if sendErr := a.sendMessage(msg.Channel, msg.ChatID, content); sendErr != nil {
				log.Ctx(ctx).WithError(sendErr).Warn("Failed to send error via sendMessage, fallback to bus")
				a.bus.Outbound <- bus.OutboundMessage{
					Channel: msg.Channel,
					ChatID:  msg.ChatID,
					Content: content,
				}
			}
			// Turn done — error response sent, safe to drain bg notifications
			ss.busy.Store(false)
			a.drainAndProcessNotifications(chatKey)
			continue
		}
		if response != nil {
			if response.WaitingUser {
				// WaitingUser response: send directly with WaitingUser flag set.
				// Bypass sendMessage (which doesn't support WaitingUser) since it applies
				// Patch/Edit logic incompatible with async user interaction.
				busMsg := bus.OutboundMessage{
					Channel:     msg.Channel,
					ChatID:      msg.ChatID,
					Content:     response.Content,
					WaitingUser: true,
					Metadata:    response.Metadata,
				}
				if busMsg.Metadata == nil {
					busMsg.Metadata = make(map[string]string)
				}
				select {
				case a.bus.Outbound <- busMsg:
				default:
					log.Ctx(ctx).Warn("Message bus outbound channel is full, dropping WaitingUser response")
				}
			} else if err := a.sendMessage(msg.Channel, msg.ChatID, response.Content, response.Metadata); err != nil {
				log.Ctx(ctx).WithError(err).Warn("Failed to dispatch response via sendMessage")
			}
		}

		// 更新最后活跃的 senderID
		lastSenderID = msg.SenderID

		// 处理完成后，如果启用了 idle timeout 且用户有 docker 沙箱，设置 timer
		// Remote sandbox 连接应保持常驻，不做 idle 清理
		if a.sandboxIdleTimeout > 0 && lastSenderID != "" {
			// Skip idle cleanup for remote sandbox — the runner connection should be persistent
			if !a.isRemoteUser(lastSenderID) {
				idleTimer = time.AfterFunc(a.sandboxIdleTimeout, func() {
					if err := a.sandbox.CloseForUser(lastSenderID); err != nil {
						log.WithError(err).Warnf("Idle sandbox cleanup failed for user %s", lastSenderID)
					} else {
						log.Infof("Idle sandbox cleaned up for user %s (timeout: %s)", lastSenderID, a.sandboxIdleTimeout)
					}
				})
			}
		}

		// Turn done — response sent, safe to drain bg notifications.
		// This is the CRITICAL ordering: all response sends happen BEFORE this point,
		// so injectCLIUserMessage in drainAndProcessNotifications cannot race with
		// the turn's reply on asyncCh.
		ss.busy.Store(false)
		a.drainAndProcessNotifications(chatKey)
	}
}

// processMessage 处理单条入站消息

func (a *Agent) processMessage(ctx context.Context, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	// 使用消息携带的 requestID（在渠道收到消息时生成），如果没有则生成新的
	reqID := msg.RequestID
	if reqID == "" {
		reqID = log.NewRequestID()
	}
	ctx = log.WithRequestID(ctx, reqID)

	// 注入 senderID 到 context，用于 per-user human block（Letta 模式）
	// Recall/Memorize 会通过 letta.GetUserID(ctx) 获取 userID
	ctx = letta.WithUserID(ctx, msg.SenderID)

	preview := msg.Content
	if r := []rune(preview); len(r) > 80 {
		preview = string(r[:80]) + "..."
	}
	log.Ctx(ctx).WithFields(log.Fields{
		"channel": msg.Channel,
		"sender":  msg.SenderID,
	}).Infof("Processing: %s", preview)

	// 将 Media 文件引用附加到消息内容中
	if len(msg.Media) > 0 {
		var ref strings.Builder
		ref.WriteString("\n\n[Attached files]")
		for _, f := range msg.Media {
			ref.WriteString("\n- ")
			ref.WriteString(f)
		}
		msg.Content += ref.String()
	}

	// 初始化 session 消息跟踪：清除旧的已发消息 ID，记录入站消息 ID 用于首条回复
	key := qualifyChatID(msg.Channel, msg.ChatID)
	a.resetSessionState(key)
	if msg.Metadata != nil && msg.Metadata["message_id"] != "" {
		a.sessionReplyTo.Store(key, msg.Metadata["message_id"])
	} else {
		a.sessionReplyTo.Delete(key)
	}

	// 获取或创建租户会话（senderID 通过 context 传递，不在这里传）
	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return nil, fmt.Errorf("get/create tenant session: %w", err)
	}

	// Set tenant-scoped stores for this request.
	tenantID := tenantSession.TenantID()
	if a.contextEditor != nil {
		a.contextEditor.SetTenantID(tenantID)
	}
	if a.maskStore != nil {
		a.maskStore.SetTenantID(tenantID)
	}
	if a.pluginMgr != nil {
		a.pluginMgr.RefreshTenantID(tenantID)
		// Wire plugin tools for this tenant if not already done
		if !a.pluginMgr.IsTenantWired(tenantID) {
			if err := plugin.WirePluginToolsForTenant(a.pluginMgr, a.tools, tenantID); err != nil {
				log.Ctx(ctx).WithError(err).WithField("tenant_id", tenantID).Warn("Failed to wire plugin tools for tenant")
			} else {
				a.pluginMgr.MarkTenantWired(tenantID)
			}
		}
	}

	// 缓存消息到聊天历史（用于 ChatHistory 工具查询）
	a.chatHistory.Add(msg.Channel, msg.ChatID, msg.SenderID, msg.Content)
	log.Ctx(ctx).WithFields(log.Fields{
		"channel": msg.Channel,
		"chat_id": msg.ChatID,
		"sender":  msg.SenderID,
	}).Debug("Message cached to chat history")

	// 指令匹配：通过 CommandRegistry 统一分发
	if cmd := a.commands.Match(msg.Content); cmd != nil {
		log.Ctx(ctx).WithFields(log.Fields{
			"channel": msg.Channel,
			"command": cmd.Name(),
		}).Info("Command matched")
		return cmd.Execute(ctx, a, msg)
	}

	// 处理卡片响应（按钮点击、表单提交）
	if msg.Metadata != nil && msg.Metadata["card_response"] == "true" {
		return a.handleCardResponse(ctx, msg, tenantSession)
	}

	preReplyNotify := bus.ShouldPreReplyNotify(msg.Metadata) && msg.Channel != "cli"
	replyPolicy := bus.InboundReplyPolicy(msg.Metadata)

	// 立即发送随机确认回复
	if preReplyNotify {
		a.sendAck(msg.Channel, msg.ChatID)
	}

	// 构建 LLM 消息（注入长期记忆、skills）
	messages, err := a.buildPrompt(ctx, msg, tenantSession)
	if err != nil {
		return nil, err
	}

	// AskUser 回答不是新的 user message，而是替换 AskUser 的 tool result。
	// 移除 Assemble 追加的 user message，并精确替换最近的 AskUser tool message。
	askUserAnswered := msg.Metadata != nil && msg.Metadata["ask_user_answered"] == "true"
	if askUserAnswered {
		// Remove last user message appended by Assemble
		if len(messages) > 0 && messages[len(messages)-1].Role == "user" {
			messages = messages[:len(messages)-1]
		}
		// Replace the most recent AskUser tool message content with user's answer.
		foundAskUserTool := false
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role != "tool" {
				continue
			}
			if messages[i].ToolName != "AskUser" {
				continue
			}
			messages[i].Content = msg.Content
			foundAskUserTool = true
			break
		}
		if !foundAskUserTool {
			log.Ctx(ctx).Warn("AskUser answer received but no matching AskUser tool message found in prompt history")
		}
		// Also update the stale tool result in session so future buildPrompt reads correct content.
		if err := tenantSession.ReplaceToolMessage("AskUser", "", msg.Content); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to replace AskUser tool result in session")
		}
	}

	// 运行 Agent 循环（统一 Run）
	// Eager-save user message BEFORE Run() so incrementally persisted assistant/tool
	// messages appear after it in the DB. GetHistory uses user messages as turn boundaries.
	if !askUserAnswered && (msg.Metadata == nil || msg.Metadata["user_msg_eager_saved"] != "true") {
		userMsg := llm.NewUserMessage(msg.Content)
		if !msg.Time.IsZero() {
			userMsg.Timestamp = msg.Time
		}
		if err := tenantSession.AddMessage(userMsg); err != nil {
			log.Ctx(ctx).WithError(err).WithFields(log.Fields{
				"channel": msg.Channel,
				"chat_id": msg.ChatID,
				"sender":  msg.SenderID,
				"content": msg.Content,
			}).Warn("Failed to eager-save user message")
		}
	}

	cfg := a.buildMainRunConfig(ctx, msg, messages, tenantSession, preReplyNotify)
	// 恢复 token 计数，优先从 session_messages.context_tokens 读取精确值。
	// tenant_state 可能被旧版 DetectTruncation 的估算值污染，context_tokens 永远是 API 精确值。
	if extras := cfg.ToolContextExtras; extras != nil && extras.TenantID != 0 {
		if lastCtx, err := tenantSession.GetLastContextTokens(); err == nil && lastCtx > 0 {
			cfg.LastPromptTokens = lastCtx
			cfg.LastCompletionTokens = 0
		} else if extras.MemorySvc != nil {
			if pt, ct, err := extras.MemorySvc.GetTokenState(ctx, extras.TenantID); err == nil && pt > 0 {
				cfg.LastPromptTokens = pt
				cfg.LastCompletionTokens = ct
			}
		}
	}
	// Inject running background task IDs into the last user message so the LLM
	// is aware of active tasks and doesn't try to restart them.
	// injectSystemNotes modifies the messages slice in-place (appends to last user message),
	// so the return value is intentionally discarded.
	_ = a.injectSystemNotes(messages, msg.Channel, msg.ChatID)

	// Wire drain callback so Run loop can inject bg notifications as tool messages.
	// Only return notifications matching THIS session's key. Other sessions' notifications
	// are put back into the pending list to prevent cross-session contamination.
	currentSessionKey := qualifyChatID(msg.Channel, msg.ChatID)
	cfg.DrainBgNotifications = a.wireBgNotificationDrain(currentSessionKey)

	// Emit SessionStart event (notification, non-blocking)
	if a.hookManager != nil {
		memoryProvider := ""
		if cfg.Memory != nil {
			memoryProvider = fmt.Sprintf("%T", cfg.Memory)
		}
		a.hookManager.Emit(ctx, &hooks.SessionStartEvent{
			BasePayload: hooks.BasePayload{
				SessionID: msg.ChatID, Channel: msg.Channel,
				SenderID: msg.SenderID, ChatID: msg.ChatID,
			},
			Source:         msg.Channel,
			Model:          cfg.Model,
			MemoryProvider: memoryProvider,
		})
	}

	// Emit SessionEnd event on processMessage exit (notification, non-blocking)
	if a.hookManager != nil {
		defer func() {
			a.hookManager.Emit(ctx, &hooks.SessionEndEvent{
				BasePayload: hooks.BasePayload{
					SessionID: msg.ChatID, Channel: msg.Channel,
					SenderID: msg.SenderID, ChatID: msg.ChatID,
				},
				Source: msg.Channel,
			})
		}()
	}

	out := Run(ctx, cfg)

	// No bgRunActive management or notification draining here.
	// bgNotifyLoop always buffers (never processes directly).
	// Remaining notifications in bgRunPending are drained by
	// chatProcessLoop's post-turn drain (after response is sent),
	// or by chatWorker's idle notification handler.
	if out.Error != nil {
		if errors.Is(out.Error, context.Canceled) {
			return a.handleCancelledRun(ctx, msg, out, tenantSession)
		}
		return nil, out.Error
	}

	return a.handleRunOutput(ctx, msg, out, tenantSession, replyPolicy)
}

// buildPrompt 构建完整的 LLM 消息列表（共用逻辑：processMessage 和 handlePromptQuery 都调用）。
// 使用 Agent 持有的 pipeline 实例，通过 MessageContext.Extra 传递动态数据。
func (a *Agent) buildPrompt(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) ([]llm.ChatMessage, error) {
	history, err := tenantSession.GetMessages()
	if err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to get history, using empty history")
		history = nil
	}

	// Auto worktree detection: if multiple sessions share the same git repo,
	// automatically create an isolated worktree to prevent file conflicts.
	// Gated behind auto_worktree user setting (default: false).
	sessKey := qualifyChatID(msg.Channel, msg.ChatID)
	sbUID := sandboxUserID(msg)
	workspaceRoot := a.workspaceRoot(sbUID)
	detectDir := tenantSession.GetCurrentDir()
	if detectDir == "" {
		detectDir = workspaceRoot
	}
	// Peer awareness / auto worktree: register this session for collaboration.
	// When auto_worktree is enabled, every session gets its own git worktree (no primary).
	// When disabled, RegisterPeer provides lightweight in-memory session tracking.
	// Uses GetEffectiveSetting — the single correct read path for user-scoped settings.
	// AutoDetectAndInit is idempotent: returns existing entry if session already registered.
	if a.settingsSvc.GetEffectiveSettingBool(msg.Channel, msg.SenderID, "auto_worktree") {
		if tools.GlobalWorktreeRegistry.GetBySession(sessKey) == nil {
			if entry, created := tools.AutoDetectAndInit(detectDir, sessKey); entry != nil && entry.WorktreeDir != "" {
				// Only override CWD for brand new worktrees (first creation).
				// On restart, AutoDetectAndInit returns existing entry with created=false,
				// so the user's last CWD (restored by loadPersistedCWD) is preserved —
				// even if they Cd'd out of the worktree.
				if created {
					tenantSession.SetCurrentDir(entry.WorktreeDir)
				}
			}
		}
	} else {
		tools.GlobalWorktreeRegistry.RegisterPeer(sessKey, detectDir)
	}

	// Fixup: strip trailing unpaired tool_calls left by a cancelled Run.
	// Both Anthropic and OpenAI APIs reject requests with unpaired tool_calls.
	history = llm.SanitizeMessages(history)
	if err := a.ensureWorkspace(ctx, workspaceRoot, sbUID); err != nil {
		return nil, fmt.Errorf("create user workspace: %w", err)
	}
	newTools, err := a.multiSession.ConfigureSessionMCP(msg.Channel, msg.ChatID, msg.SenderID, a.workDir)
	if err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to configure session MCP scope")
	}
	if len(newTools) > 0 {
		sessKey := qualifyChatID(msg.Channel, msg.ChatID)
		a.tools.ActivateTools(sessKey, newTools)
		log.Ctx(ctx).WithField("tools", len(newTools)).Info("Auto-activated new personal MCP tools")
	}

	promptWorkDir := a.workDir
	if a.sandboxMode == "docker" {
		promptWorkDir = "/workspace"
	} else if ws := a.remoteWorkspace(msg.SenderID); ws != "" {
		promptWorkDir = ws
	}

	// For worktree sessions, override promptWorkDir with the worktree path.
	// The system prompt shows promptWorkDir as the main "工作目录", so the
	// agent must see the worktree path here to know where it's working.
	cwd := tenantSession.GetCurrentDir()
	if cwd != "" && strings.Contains(cwd, ".xbot-worktrees") {
		promptWorkDir = cwd
	}

	mc := NewMessageContext(
		letta.WithUserID(ctx, msg.SenderID),
		msg.Content,
		history,
		msg.Channel,
		promptWorkDir,
		msg.SenderName,
		msg.SenderID,
		msg.ChatID,
	)

	// 注入当前工作目录（CWD）到 prompt
	// sandbox 模式下 CWD 已经是 sandbox 内路径，无 cd 时默认为 promptWorkDir
	mc.CWD = cwd
	mc.XbotHome = a.xbotHome
	if mc.CWD == "" {
		log.WithFields(log.Fields{
			"channel":      msg.Channel,
			"chat_id":      msg.ChatID,
			"fallback_dir": promptWorkDir,
		}).Debug("Session CWD empty, using promptWorkDir fallback")
		mc.CWD = promptWorkDir
	}

	// Determine projectDir for project-local skill/agent scanning
	projectDir := cwd // use session CWD as project root
	if projectDir == "" {
		projectDir = promptWorkDir
	}

	mc.SetExtra(ExtraKeySkillsCatalog, a.skills.GetSkillsCatalog(ctx, msg.SenderID, projectDir))
	mc.SetExtra(ExtraKeyAgentsCatalog, a.agents.GetAgentsCatalog(ctx, msg.SenderID, projectDir))
	mc.SetExtra(ExtraKeyMemoryProvider, tenantSession.Memory())
	permUsers := a.settingsSvc.GetPermUsers(msg.Channel, msg.SenderID)
	mc.SetExtra(ExtraKeyPermUsers, permUsers)
	mc.Ctx = withPermControlEnabled(mc.Ctx, IsPermControlEnabled(permUsers))

	mc.SetExtra(ExtraKeyTenantID, tenantSession.TenantID())

	// Session name for rename hint (only injected on first user message)
	_, sessionName := channel.ParseChatID(msg.ChatID)
	if a.multiSession != nil {
		if db := a.multiSession.DB(); db != nil {
			var label string
			if err := db.Conn().QueryRow(
				"SELECT label FROM user_chats WHERE channel = ? AND chat_id = ? AND label != '' LIMIT 1",
				msg.Channel, msg.ChatID,
			).Scan(&label); err == nil && label != "" {
				sessionName = label
			}
		}
	}
	mc.SetExtra(ExtraKeySessionName, sessionName)

	return a.pipeline.Run(mc), nil
}

// summarizeRetryError 将 LLM 错误简化为用户友好的描述。
func summarizeRetryError(err error) string {
	if err == nil {
		return "未知错误"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "TLS handshake timeout"):
		return "网络超时"
	case strings.Contains(msg, "connection refused"):
		return "连接被拒绝"
	case strings.Contains(msg, "429") || strings.Contains(msg, "rate limit"):
		return "请求限流"
	case strings.Contains(msg, "502") || strings.Contains(msg, "503"):
		return "服务暂时不可用"
	case strings.Contains(msg, "500") || strings.Contains(msg, "504"):
		return "服务端错误"
	default:
		var netErr net.Error
		if errors.As(err, &netErr) {
			if netErr.Timeout() {
				return "网络超时"
			}
			return "网络错误"
		}
		return "临时错误"
	}
}

// runLoop 执行 Agent 迭代循环（LLM -> 工具调用 -> LLM ...）
// autoNotify 为 true 时，累积显示模型中间内容和工具调用状态，实时更新同一条消息
// tenantSession 用于自动压缩后持久化压缩结果（可传 nil）

// RegisterTool registers a tool to the agent's tool registry.
// This is useful for dynamically adding tools after agent creation.
func (a *Agent) RegisterTool(tool tools.Tool) {
	a.tools.Register(tool)
	log.WithField("tool", tool.Name()).Info("Tool registered")
}

func (a *Agent) RegisterCoreTool(tool tools.Tool) {
	a.tools.RegisterCore(tool)
	log.WithField("tool", tool.Name()).Info("Tool registered")
}

// emitBuiltinProgress sends a progress event for builtin commands (/compress, /new)
// that bypass engine.Run. It uses the same CLI channel path as buildCLIProgressEventHandler
// so the snapshot is stored for mid-session reconnect.
func (a *Agent) emitBuiltinProgress(chName, chatID string, phase ProgressPhase) {
	progressKey := qualifyChatID(chName, chatID)

	// Get or create per-chat seq counter. Start at 1 so the first event
	// is not discarded by the CLI's seq monotonic check (initial lastProgressSeq=0).
	seqPtr, _ := a.builtinProgressSeq.LoadOrStore(progressKey, &atomic.Uint64{})
	seq := seqPtr.(*atomic.Uint64).Add(1)

	payload := &protocol.ProgressEvent{
		ChatID:    progressKey,
		Phase:     string(phase),
		Seq:       seq,
		Iteration: 0,
	}

	// Send via CLI channel
	if a.channelFinder != nil {
		if ch, ok := a.channelFinder("cli"); ok {
			if cc, ok := ch.(*channel.CLIChannel); ok {
				cc.SendProgress(chatID, payload)
			} else if rc, ok := ch.(channel.ProgressSender); ok {
				rc.SendProgress(chatID, payload)
			}
		}
	}

	// Store snapshot for mid-session reconnect
	a.lastProgressSnapshot.Store(progressKey, payload)
}

// emitBuiltinProgressDone sends a PhaseDone progress event and cleans up the snapshot.
// Must be called in a defer after emitBuiltinProgress to ensure the CLI ends the turn.
// tokenUsage is optional — when provided, it updates the CLI's context indicator bar.
func (a *Agent) emitBuiltinProgressDone(chName, chatID string, tokenUsage *protocol.TokenUsage) {
	progressKey := qualifyChatID(chName, chatID)

	seqPtr, ok := a.builtinProgressSeq.Load(progressKey)
	if !ok {
		return
	}
	seq := seqPtr.(*atomic.Uint64).Add(1)

	payload := &protocol.ProgressEvent{
		ChatID:     progressKey,
		Phase:      string(PhaseDone),
		Seq:        seq,
		TokenUsage: tokenUsage,
	}

	if a.channelFinder != nil {
		if ch, ok := a.channelFinder("cli"); ok {
			if cc, ok := ch.(*channel.CLIChannel); ok {
				cc.SendProgress(chatID, payload)
			} else if rc, ok := ch.(channel.ProgressSender); ok {
				rc.SendProgress(chatID, payload)
			}
		}
	}

	a.lastProgressSnapshot.Delete(progressKey)
	a.builtinProgressSeq.Delete(progressKey)
}

// 首次发送创建新消息（如有入站 message_id 则回复该消息），后续发送 Patch 更新同一条消息。
// 工具发送最终回复（如飞书卡片）时同样 Patch 更新，但标记 session 为"已完成"，后续调用自动跳过。
// sendMessage 向 IM 渠道发送消息。
// 通过 directSend 直连或 bus.Outbound 广播。
func (a *Agent) sendMessage(chName, chatID, content string, metadata ...map[string]string) error {
	key := qualifyChatID(chName, chatID)

	// 工具已发送最终回复 → 跳过后续所有消息（进度更新、LLM 最终回复等）
	if _, sent := a.sessionFinalSent.Load(key); sent {
		return nil
	}

	msg := channel.OutboundMsg{
		Channel: chName,
		ChatID:  chatID,
		Content: content,
	}
	if len(metadata) > 0 && metadata[0] != nil {
		msg.Metadata = metadata[0]
	}
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]string)
	}

	isFinal := strings.HasPrefix(content, "__FEISHU_CARD__:")

	if a.directSend != nil {
		// Always include update_message_id for patch support.
		// For cards: feishu.go will attempt patch first; if cross-type conflict occurs,
		// it falls back to creating a new message and deleting the old progress message.
		if existingID, ok := a.sessionMsgIDs.Load(key); ok {
			if id, ok := existingID.(string); ok {
				msg.Metadata["update_message_id"] = id
			}
		}

		if replyTo, ok := a.sessionReplyTo.Load(key); ok {
			if id, ok := replyTo.(string); ok {
				msg.Metadata["message_id"] = id
			}
		}

		log.WithField("send_channel", msg.Channel).
			WithField("send_chat_id", msg.ChatID).
			WithField("orig_channel", chName).
			WithField("orig_chat_id", chatID).
			WithField("is_final", isFinal).
			Info("sendMessage directSend dispatch")
		msgID, err := a.directSend(msg)
		if err != nil {
			return err
		}
		if msgID != "" {
			a.sessionMsgIDs.Store(key, msgID)
		}
		if isFinal {
			a.sessionFinalSent.Store(key, true)
		}
		return nil
	}

	// 降级：directSend 不可用时走 bus（无消息更新跟踪）
	select {
	case a.bus.Outbound <- bus.OutboundMessage{
		Channel:  msg.Channel,
		ChatID:   msg.ChatID,
		Content:  msg.Content,
		Media:    msg.Media,
		Metadata: msg.Metadata,
	}:
		return nil
	default:
		return fmt.Errorf("message bus outbound channel is full")
	}
}

// injectInbound 向入站队列注入消息，触发 Agent 完整处理循环。
// 用于 cron 调度和后台任务通知等内部系统消息。
func (a *Agent) injectInbound(channel, chatID, senderID, content string) {
	msg := bus.InboundMessage{
		Channel:   channel,
		SenderID:  senderID,
		ChatID:    chatID,
		Content:   content,
		Time:      time.Now(),
		RequestID: log.NewRequestID(),
	}
	select {
	case a.bus.Inbound <- msg:
	case <-a.agentCtx.Done():
		log.WithFields(log.Fields{"channel": channel, "chat_id": chatID}).Warn("injectInbound: agent context done, dropping message")
	}
}

// injectEventMessage 向入站队列注入事件触发的消息。
// Event Router 通过此函数将外部事件（webhook 等）路由到 agent loop，
// 并设置 EventSource/EventTrigger 元数据。
// 同时通过 injectCLIUserMessage 通知 TUI 显示。
func (a *Agent) injectEventMessage(msg event.Message) {
	// Route through unified async message pipeline
	a.injectAsyncMessage(msg.Channel, msg.ChatID, msg.SenderID, msg.Content, tools.AsyncSourceEvent)
}

// bgNotifyLoop routes background notifications from BgTaskManager.NotifyCh.
// ALL notifications are buffered into bgRunPending. The function NEVER processes
// them directly — this eliminates the race between injectCLIUserMessage and the
// agent's reply on asyncCh.
//
// After buffering, the target session's notifyCh is signaled. The session's
// chatWorker or chatProcessLoop picks up the signal and drains notifications
// at a safe point (after the turn's reply is sent, or when idle).
func (a *Agent) bgNotifyLoop() {
	for notif := range a.bgTaskMgr.NotifyCh {
		// Always buffer — never process directly
		a.bgRunPendingMu.Lock()
		a.bgRunPending = append(a.bgRunPending, notif)
		a.bgRunPendingMu.Unlock()

		// Signal the target session's chatWorker
		sessionKey := notif.SessionKey()
		if state, ok := a.bgSessionStates.Load(sessionKey); ok {
			ss := state.(*bgSessionState)
			select {
			case ss.notifyCh <- struct{}{}:
			default:
				// Already signaled — notification will be drained with others
			}
		}
	}
}

// processBgNotification handles a background task completion when no Run() is active.
// Injects the task result as a user message via injectBgUserMessage, triggering the standard
// processMessage → Assemble → Run pipeline. This matches Claude Code's behavior:
// bg task completion = environment notification = user message to the LLM.
func (a *Agent) processBgNotification(task *tools.BackgroundTask) {
	sessionKey := task.SessionKey()
	if sessionKey == "" {
		log.WithField("task_id", task.ID).Warn("Bg task notification: no session key, dropping")
		return
	}

	parts := strings.SplitN(sessionKey, ":", 2)
	if len(parts) != 2 {
		log.WithField("session_key", sessionKey).Warn("Bg task: invalid session key format")
		return
	}
	channelName, chatID := parts[0], parts[1]

	// Offload large task output so the agent can retrieve it via offload_recall.
	// Without this, FormatBgTaskCompletion truncates the output to 2000 chars
	// and says "use offload_recall" without providing an actual offload ID.
	outputOverride := ""
	if a.offloadStore != nil && task.Output != "" {
		offloadCtx := context.Background()
		if offloaded, ok := a.offloadStore.MaybeOffload(offloadCtx, sessionKey,
			"background_task_result", task.Command, task.Output,
			"" /*workspaceRoot*/, "" /*sandboxWorkDir*/, "" /*userID*/); ok {
			outputOverride = offloaded.Summary
			log.WithFields(log.Fields{
				"task_id":    task.ID,
				"offload_id": offloaded.ID,
			}).Info("Bg task output offloaded")
		}
	}

	content := tools.FormatBgTaskCompletion(task, outputOverride)
	log.WithFields(log.Fields{
		"task_id": task.ID,
		"channel": channelName,
		"chat_id": chatID,
	}).Info("Bg task notification: injecting as user message")

	a.injectBgUserMessage(channelName, chatID, task.SenderID(), content)
}

// processCronFiredNotification handles a cron fired notification when no Run() is active.
// It parses the session key and injects the cron message as a user message via injectBgUserMessage.
func (a *Agent) processCronFiredNotification(c *tools.CronFired) {
	parts := strings.SplitN(c.SessionKey(), ":", 2)
	if len(parts) != 2 {
		log.WithField("session_key", c.SessionKey()).Warn("CronFired notification: invalid session key")
		return
	}
	channelName, chatID := parts[0], parts[1]
	content := fmt.Sprintf("⏰ [定时任务触发] %s", c.Message)

	log.WithFields(log.Fields{
		"channel": channelName,
		"chat_id": chatID,
		"message": tools.Truncate(c.Message, 80),
	}).Info("CronFired notification: injecting as user message")

	a.injectBgUserMessage(channelName, chatID, c.SenderID(), content)
}

// processSubAgentBgNotification handles a bg subagent notification when no Run() is active.
// Only completion notifications trigger a new Run; progress notifications are dropped
// (they're only meaningful during an active Run, where they're injected as tool results).
func (a *Agent) processSubAgentBgNotification(n *tools.SubAgentBgNotify) {
	// During idle, only completion matters — progress would waste an LLM call
	if n.Type != tools.SubAgentBgNotifyCompleted {
		log.WithFields(log.Fields{
			"role":     n.Role,
			"instance": n.Instance,
			"type":     n.Type,
		}).Debug("Dropping bg subagent progress notification (agent idle)")
		return
	}

	parts := strings.SplitN(n.SessionKey(), ":", 2)
	if len(parts) != 2 {
		log.WithField("session_key", n.SessionKey()).Warn("Bg subagent notification: invalid session key")
		return
	}
	channelName, chatID := parts[0], parts[1]
	content := tools.FormatSubAgentBgNotify(n)

	log.WithFields(log.Fields{
		"role":     n.Role,
		"instance": n.Instance,
		"type":     n.Type,
		"channel":  channelName,
	}).Info("Bg subagent notification: injecting as user message")

	a.injectBgUserMessage(channelName, chatID, n.SenderID(), content)
}

// processAsyncMessageNotification handles an async message notification when
// the target session is idle (no active Run). Injects as user message with
// TUI notification and correct senderID for LLM subscription resolution.
func (a *Agent) processAsyncMessageNotification(n *tools.AsyncMessageNotification) {
	parts := strings.SplitN(n.SessionKey(), ":", 2)
	if len(parts) != 2 {
		log.WithField("session_key", n.SessionKey()).Warn("Async message notification: invalid session key")
		return
	}
	channelName, chatID := parts[0], parts[1]
	log.WithFields(log.Fields{
		"source":  n.Source,
		"channel": channelName,
		"chat_id": chatID,
	}).Info("Async message notification: injecting as user message (idle)")
	a.injectBgUserMessage(channelName, chatID, n.SenderID(), n.Content)
}

// injectBgUserMessage is the unified entry point for injecting background notification
// content as a user message. It reads senderID from the notification to preserve
// correct sender context (workspace, sandbox, memory, LLM config).
// All bg notification handlers MUST use this function — never call injectInbound directly.
//
// Both TUI notification (injectCLIUserMessage) and agent processing (injectInbound)
// are called together. Without injectCLIUserMessage, the TUI never receives a
// cliInjectedUserMsg, so no user message appears — only the progress auto-start
// fires, which lacks the user message in m.messages.
func (a *Agent) injectBgUserMessage(channelName, chatID, senderID, content string) {
	a.injectCLIUserMessage(channelName, chatID, content)
	a.injectInbound(channelName, chatID, senderID, content)
}

// buildBgNotificationRunConfig is no longer needed — idle bg notifications
// go through injectInbound → processMessage → buildMainRunConfig.

// RunSubAgent 实现 tools.SubAgentManager 接口
// 创建一个独立的子 Agent 循环来执行任务，子 Agent 拥有自己的工具集但不能再创建子 Agent

// injectAsyncMessage is the UNIFIED entry point for all async message injection.
// Used by peer messages, webhook events, and any other external source.
// Routes through bgRunPending → drain pipeline, same as bg task completions.
//
// Busy: injected as synthetic tool call/result pair in Run loop (immediate, non-blocking).
// Idle: injected as user message via injectInbound (triggers new turn).
//
// Always notifies TUI for visibility.
func (a *Agent) injectAsyncMessage(channel, chatID, senderID, content, source string) string {
	sessionKey := channel + ":" + chatID

	// Resolve real senderID if not provided
	if senderID == "" {
		senderID = a.resolveSenderForSession(channel, chatID)
	}

	// Route through the same bgRunPending → drain pipeline as bg tasks.
	// This guarantees:
	// - Busy: injected as tool result on Run loop's goroutine (no data race)
	// - Idle: injected as user message with correct TUI notification
	a.bgTaskMgr.SendAsyncMessage(&tools.AsyncMessageNotification{
		Key:     sessionKey,
		Sid:     senderID,
		Content: content,
		Source:  source,
	})

	return fmt.Sprintf("✅ queued for %s", sessionKey)
}

// resolveSenderForSession looks up the real user ID (senderID) that owns a session.
// This is needed by injectPeerMessage to use the correct LLM subscription when
// injecting a user message into an idle target session.
// Returns "admin" as fallback for CLI sessions when DB lookup fails.
func (a *Agent) resolveSenderForSession(channel, chatID string) string {
	if a.multiSession != nil {
		if db := a.multiSession.DB(); db != nil {
			var senderID string
			err := db.Conn().QueryRow(
				"SELECT sender_id FROM user_chats WHERE channel = ? AND chat_id = ? LIMIT 1",
				channel, chatID,
			).Scan(&senderID)
			if err == nil && senderID != "" {
				return senderID
			}
		}
	}
	// Fallback: for CLI channels, the default senderID is "admin"
	if channel == "cli" {
		return "admin"
	}
	return channel
}

// injectPeerMessage sends a message to another CLI session (peer-to-peer).
// If the target is busy, injects as a fake tool result in the current iteration.
// If idle, pushes as a user message to start a new turn.
// Returns a delivery status message.
func (a *Agent) injectPeerMessage(targetSessionKey, content string) string {
	parts := strings.SplitN(targetSessionKey, ":", 2)
	if len(parts) != 2 {
		return fmt.Sprintf("❌ invalid peer session address: %s", targetSessionKey)
	}
	ch, chatID := parts[0], parts[1]
	return a.injectAsyncMessage(ch, chatID, "", content, tools.AsyncSourcePeer)
}

// allowedTools 为工具白名单，为空时使用所有工具（除 SubAgent）
func (a *Agent) RunSubAgent(parentCtx *tools.ToolContext, task string, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, roleName, instance, model string) (string, error) {
	cfg := a.buildSubAgentRunConfig(parentCtx.Ctx, parentCtx, task, systemPrompt, allowedTools, caps, roleName, false, instance, model)
	out := Run(parentCtx.Ctx, cfg)
	if out.Error != nil {
		return out.Content, out.Error
	}
	return out.Content, nil
}

// addReaction 对用户消息添加表情回复，表示处理完成
func (a *Agent) addReaction(msg bus.InboundMessage) {
	if a.directSend == nil {
		return
	}
	messageID := ""
	if msg.Metadata != nil {
		messageID = msg.Metadata["message_id"]
	}
	if messageID == "" {
		return
	}

	_, err := a.directSend(channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Metadata: map[string]string{
			"add_reaction":        "DONE",
			"reaction_message_id": messageID,
		},
	})
	if err != nil {
		log.WithError(err).Debug("Failed to add reaction")
	}
}

// ProcessDirect 直接处理一条消息（用于 CLI 模式）
func (a *Agent) ProcessDirect(ctx context.Context, content string) (string, error) {
	msg := bus.InboundMessage{
		Channel:   "cli",
		SenderID:  "cli_user",
		ChatID:    "direct",
		Content:   content,
		Time:      time.Now(),
		RequestID: log.NewRequestID(),
	}
	resp, err := a.processMessage(ctx, msg)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}
	return resp.Content, nil
}

// CleanupSessionFiles removes offload data for a session identified by (channel, chatID).
// Called from delete_chat RPC handler and CLI session deletion to ensure disk-stored
// offload data is cleaned when a session is removed from DB.
// Mask data cleanup relies on the periodic CleanStale timer which removes dirs
// older than 7 days (mask dirs are keyed by numeric tenant ID, not session key).
func (a *Agent) CleanupSessionFiles(channel, chatID string) {
	sessionKey := qualifyChatID(channel, chatID)
	if a.offloadStore != nil {
		a.offloadStore.CleanSession(sessionKey)
	}
}

// periodicCleanup runs offload and mask stale cleanup on a 6-hour ticker.
// Runs once immediately at startup, then periodically until cleanupStopCh is closed.
func (a *Agent) periodicCleanup() {
	a.doCleanup()
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-a.cleanupStopCh:
			return
		case <-ticker.C:
			a.doCleanup()
		}
	}
}

// doCleanup runs stale cleanup for offload and mask stores.
func (a *Agent) doCleanup() {
	if a.offloadStore != nil {
		a.offloadStore.CleanStale()
	}
	if a.maskStore != nil {
		a.maskStore.CleanStale(7)
	}
}

// formatToolProgress generates a human-readable one-line summary of a tool call for progress display.
// It parses the JSON args and extracts the most important parameter(s) based on the tool name.
// Output is concise, max ~80 chars total.

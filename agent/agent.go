package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xbot/agent/hooks"
	"xbot/bus"
	"xbot/channel"
	"xbot/channel/cli"
	"xbot/channel/web"
	"xbot/clipanic"
	"xbot/cron"
	"xbot/event"
	"xbot/llm"
	log "xbot/logger"
	"xbot/memory"
	"xbot/memory/letta"
	xbotmemory "xbot/memory/xbot"
	"xbot/plugin"
	"xbot/protocol"
	"xbot/runner"
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
// in-place modifications don't mutate the original cfg.Messages backing
// array or session storage.
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

// evalRealPath resolves a path to its real absolute path, following symlinks.
// Falls back to filepath.Abs on error.
func evalRealPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return real
}

func resolveGlobalSkillsDirs(skillsDir string) []string {
	var dirs []string

	// 1. Add the configured/default xbot skills dir (~/.xbot/skills)
	if skillsDir != "" {
		dirs = append(dirs, evalRealPath(skillsDir))
	}

	// 2. Auto-detect Codex/Cursor-compatible global skills dir: ~/.agents/skills
	//    This allows xbot to automatically pick up skills installed by Codex, Cursor,
	//    or other agents that follow the ~/.agents/ convention, without requiring symlinks.
	//    Only add it if the directory actually exists, and deduplicate by real path
	//    (in case skillsDir is a symlink pointing to ~/.agents/skills or vice versa).
	if home, err := os.UserHomeDir(); err == nil {
		agentsSkillsDir := filepath.Join(home, ".agents", "skills")
		if info, err := os.Stat(agentsSkillsDir); err == nil && info.IsDir() {
			real := evalRealPath(agentsSkillsDir)
			alreadyIncluded := false
			for _, d := range dirs {
				if d == real {
					alreadyIncluded = true
					break
				}
			}
			if !alreadyIncluded {
				dirs = append(dirs, real)
			}
		}
	}

	return dirs
}

// metaTools are tools that manage/search other tools — not useful to index.
var metaTools = map[string]bool{
	"search_tools": true,
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

	// activeTurnID is the TurnID of the currently-processing turn. Set by
	// chatProcessLoop when it generates a new TurnID; read by sendMessage to
	// stamp the TurnID on the reply OutboundMsg. chatProcessLoop is serial per
	// session, so there is no concurrent write — but sendMessage may be called
	// from the Run's goroutine (ProgressNotifier), hence atomic.
	activeTurnID atomic.Uint64
	// activeIteration is the iteration number of the currently-processing
	// iteration. Set by runState.beginIteration via cfg.OnIterationChange so
	// stream callbacks can stamp the iteration on stream_content events —
	// without it the frontend cannot tell that a new iteration started when
	// only reasoning/content streams arrive (no structured event), and it
	// keeps rendering the previous iteration's content/tools (iter2 shows
	// iter1's content1 tool1).
	activeIteration atomic.Int64
	turnIDSeq       atomic.Uint64 // per-session monotonic TurnID counter
	// lastTurnID tracks the most recently assigned TurnID for monotonicity
	// assertions. Must be strictly increasing; a regression or non-increment
	// indicates a turn lifecycle bug.
	lastTurnID atomic.Uint64

	// drainedThisRun tracks notifications consumed by DrainBgNotifications
	// during the current Run. If the Run is cancelled, pending notifications
	// are recorded in the interrupted turn and this tracking is cleared so the
	// same notification is not delivered as a fresh user message.
	// Cleared on normal/error completion (notifications were processed).
	drainedThisRunMu sync.Mutex
	drainedThisRun   []tools.BgNotification
}

// sessionOperationGate serializes a chat turn with destructive session
// operations such as rewind. The channel form supports context-aware waiting
// for turns and non-blocking acquisition for API requests.
type sessionOperationGate struct {
	token chan struct{}
	refs  int // guarded by Agent.sessionOperationGatesMu
}

func newSessionOperationGate() *sessionOperationGate {
	return &sessionOperationGate{token: make(chan struct{}, 1)}
}

type sessionOperationLease struct {
	owner    *Agent
	key      string
	gate     *sessionOperationGate
	released atomic.Bool
}

func (l *sessionOperationLease) lock(ctx context.Context) bool {
	if l == nil || l.released.Load() {
		return false
	}
	select {
	case l.gate.token <- struct{}{}:
		return true
	case <-ctx.Done():
		l.release()
		return false
	}
}

func (l *sessionOperationLease) tryLock() bool {
	if l == nil || l.released.Load() {
		return false
	}
	select {
	case l.gate.token <- struct{}{}:
		return true
	default:
		l.release()
		return false
	}
}

func (l *sessionOperationLease) unlock() {
	if l == nil || l.released.Load() {
		return
	}
	<-l.gate.token
	l.release()
}

func (l *sessionOperationLease) release() {
	if l == nil || !l.released.CompareAndSwap(false, true) {
		return
	}
	l.owner.releaseSessionOperationGate(l.key, l.gate)
}

const bgNotificationMetadataKey = "xbot_internal_bg_notification"

// acknowledgeDrainedThisRun removes notifications only after their synthetic
// tool pairs have been durably persisted.
func (ss *bgSessionState) acknowledgeDrainedThisRun(count int) {
	if count <= 0 {
		return
	}
	ss.drainedThisRunMu.Lock()
	defer ss.drainedThisRunMu.Unlock()
	if count >= len(ss.drainedThisRun) {
		ss.drainedThisRun = nil
		return
	}
	ss.drainedThisRun = append([]tools.BgNotification(nil), ss.drainedThisRun[count:]...)
}

func (ss *bgSessionState) snapshotDrainedThisRun() []tools.BgNotification {
	ss.drainedThisRunMu.Lock()
	defer ss.drainedThisRunMu.Unlock()
	return append([]tools.BgNotification(nil), ss.drainedThisRun...)
}

func (ss *bgSessionState) takeDrainedThisRun() []tools.BgNotification {
	ss.drainedThisRunMu.Lock()
	defer ss.drainedThisRunMu.Unlock()
	drained := ss.drainedThisRun
	ss.drainedThisRun = nil
	return drained
}

// clearDrainedThisRun discards any stale tracking after a successful turn.
func (ss *bgSessionState) clearDrainedThisRun() {
	ss.drainedThisRunMu.Lock()
	ss.drainedThisRun = nil
	ss.drainedThisRunMu.Unlock()
}

// nextTurnID atomically increments and returns the next per-session TurnID.
// Called by chatProcessLoop when dequeuing a message. Thread-safe via atomic.
func (ss *bgSessionState) nextTurnID() uint64 {
	return ss.turnIDSeq.Add(1)
}

// setActiveTurn records the TurnID of the currently-processing turn so that
// sendMessage can stamp it on the reply OutboundMsg.
func (ss *bgSessionState) setActiveTurn(id uint64) {
	ss.activeTurnID.Store(id)
}

// Agent 核心 Agent 引擎
type Agent struct {
	bus           *bus.MessageBus
	multiSession  *session.MultiTenantSession // Multi-tenant session manager
	tools         *tools.Registry
	maxIterations int

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
	runnerManager      *runner.Manager  // Runner 管理器（V5：runner 作为一等公民）
	sandboxIdleTimeout time.Duration    // 沙箱空闲超时（0 禁用）

	// toolProviders are the ordered tool sources for the agent.
	// Priority: agent-core(1) → runner(2) → channel(3) → plugin(4).
	toolProviders   []tools.ToolProvider
	directWorkspace string        // 非空时 workspaceRoot() 直接返回此值（CLI 模式使用，取代 singleUser 的 workspace 短路）
	maxConcurrency  int           // 最大并发会话处理数
	globalSem       chan struct{} // 全局并发信号量（SetMaxConcurrency 动态重建）
	globalSemMu     sync.Mutex    // 保护 globalSem 替换
	globalSkillDirs []string      // 全局 skill 目录（宿主机路径）
	agentsDir       string
	xbotHome        string // global xbot config dir (e.g. ~/.xbot), used for mcp.json etc.
	// deltaPush 启用流式 delta push（增量文本）。默认 false = 每次推送完整
	// 累积文本（streamContentFunc/streamReasoningFunc 用）。见 Config.DeltaPush。
	deltaPush bool

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

	// User system: holds llmFactory, settingsSvc, identityResolver.
	// Accessed by ResolveUserContext (request path) and infrastructure methods.
	// Never accessed directly by agent loop code — use UserContext from ctx.
	userSys *userSystem

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
	cancelStateMu sync.Mutex
	chatCancelCh  sync.Map

	// sessionOperationGates serializes Run/command turns with history rewind.
	// Leases are reference counted under the map mutex so idle entries can be
	// removed without an ABA window that creates two gates for one session.
	sessionOperationGatesMu sync.Mutex
	sessionOperationGates   map[string]*sessionOperationGate

	// pendingCancel: 当 /cancel 到达时 cancelCh 尚未注册（消息还在排队或等信号量），
	// 先记录 pending，chatProcessLoop 注册 cancelCh 后立即消费。
	// key: "channel:chatID" -> bool
	pendingCancel sync.Map

	// lastProgressSnapshot stores the latest channel-agnostic progress snapshot
	// per active chat, updated before broadcasting structured progress. Used by
	// GetActiveProgress to restore any channel after a mid-session reconnect.
	// key: "channel:chatID" -> *protocol.ProgressEvent
	lastProgressSnapshot sync.Map

	// lastStreamStats stores the most recent LLM stream timing stats per session.
	// Unlike lastProgressSnapshot (deleted on turn end), this persists across
	// turns so /info can display TTFT/TPOT even after the turn completes.
	// key: "channel:chatID" -> *protocol.StreamStats
	lastStreamStats sync.Map

	// waitingUserSessions stores pending AskUser prompts per chat.
	// Set when buildWaitingUserOutbound fires; deleted when the answer arrives.
	// Used by GetPendingAskUser to resend ask_user on WS reconnect.
	// key: "channel:chatID" -> *pendingAskUserEntry
	waitingUserSessions sync.Map

	// streamState stores live LLM streaming content per chat, updated by stream
	// callbacks (streamContentFunc/streamReasoningFunc/streamToolCallFunc).
	// GetActiveProgress merges these fields into the returned snapshot.
	// This replaces the old push-based stream event pipeline for local CLI.
	// key: "channel:chatID" -> *atomic.Pointer[protocol.ProgressEvent]
	streamState sync.Map

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

	// approvalState manages approval handling for privileged operations.
	approvalState *hooks.ApprovalState

	// checkpointState manages file checkpoint snapshots for rewind file rollback.
	checkpointState *hooks.CheckpointState
	// checkpointStores caches per-session CheckpointStores (keyed by session key).
	checkpointStores sync.Map // map[string]*tools.CheckpointStore

	// OffloadStore manages large tool result offload to disk
	offloadStore *OffloadStore

	// maskStores caches per-tenant observation masking stores keyed by tenantID
	// (map[int64]*ObservationMaskStore, lazily created by maskStoreFor). The old
	// shared-singleton + SetTenantID-per-message design raced across tenants:
	// tenant A's Mask could persist into tenant B's directory (cross-tenant data
	// leak) and Recall's disk fallback read B's storeDir. Per-tenant instances
	// bind {baseDir}/{tenantID} once and never switch.
	maskStores sync.Map
	// maskBaseDir is the disk base directory for per-tenant mask stores.
	maskBaseDir string

	// lifecycleStopCh and lifecycleWG own the Agent's long-lived goroutines.
	lifecycleStopCh chan struct{}
	lifecycleWG     sync.WaitGroup
	closeOnce       sync.Once

	// contextEditor 管理上下文编辑（Context Editing 工具）
	contextEditor *ContextEditor

	// todoManager 管理当前会话的 TODO 列表
	todoManager *tools.TodoManager

	// goalManager 管理当前会话的 Goal 生命周期
	goalManager *GoalManager

	// channelPromptProviders channel 特化 prompt 提供者列表（由外部注入）
	channelPromptProviders []ChannelPromptProvider

	// channelPromptMiddleware 持有 pipeline 中的 ChannelPromptMiddleware 引用，
	// 用于运行时动态添加 provider（通过 AddChannelPromptProvider）。
	channelPromptMiddleware *ChannelPromptMiddleware

	// RegistryManager for skill/agent sharing and marketplace
	registryManager *RegistryManager

	// SettingsService is accessed via a.userSys.settingsSvc (no direct field).

	// TUI control callbacks (set by CLI channel, nil for other channels)
	tuiCtrlFn   func(action string, params map[string]string) (map[string]string, error)
	configGetFn func(key string) (string, error)
	configSetFn func(key, value string) (string, error)

	// channelFinder looks up a channel instance by name (injected from main.go).
	channelFinder func(name string) (channel.Channel, bool)

	// channelRange iterates over all registered channels (injected from main.go).
	// Used for broadcasting to ALL channels (including plugin channels) without
	// hardcoding channel names. nil in standalone mode.
	channelRange func(fn func(string, channel.Channel) bool)

	// cliSenderID is the sender_id used for CLI channel DB operations.
	cliSenderID string

	// singleUser enables single-user mode: all senders share one identity.
	singleUser bool

	// memoryProvider stores the resolved memory provider type ("flat", "letta", "xbot", "none").
	// Used by SubAgent memory construction to match the parent's provider.
	memoryProvider string

	// identityResolver resolves channel-specific senderID to canonical user_id.
	// IdentityResolver is accessed via a.userSys.identityResolver (no direct field).
	// nil in standalone CLI mode (no multi-user DB).

	// bgTaskMgr manages background shell tasks (shared across all sessions).
	// atomic.Pointer: read by background goroutines (bgNotifyLoop) and the
	// message-processing path, replaced via SetBgTaskManager (tests) after
	// New() has started those goroutines — plain field access is a data race.

	// PluginManager manages the plugin system lifecycle
	pluginMgr *plugin.PluginManager
	// webUIReg stores channel-plugin web UI component declarations (web_ui protocol).
	webUIReg  *plugin.WebUIRegistry
	bgTaskMgr atomic.Pointer[tools.BackgroundTaskManager]

	// bgRunPending buffers bg notifications by session. The Run loop drains the
	// current session between iterations; idle sessions drain their own bucket.
	bgRunPending   map[string][]tools.BgNotification
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

type pendingAskUserEntry struct {
	mu      sync.RWMutex
	pending *protocol.ProgressEvent
}

// SetSettingsService sets the SettingsService (for external injection or override).
func (a *Agent) SetSettingsService(svc *SettingsService) {
	if a.userSys == nil {
		a.userSys = &userSystem{}
	}
	a.userSys.settingsSvc = svc
}

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
	if rc, ok := ch.(*web.RemoteCLIChannel); ok {
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

// listLLMSubsFn returns a subscription listing function backed by UserContext.
func (a *Agent) listLLMSubsFn(uc *UserContext) func(ch, senderID string) []tools.SubscriptionInfo {
	if uc == nil || uc.SubSvc == nil {
		return nil
	}
	svc := uc.SubSvc
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

// getActiveSubFieldFn returns a function that reads a single field from the
// active subscription, backed by UserContext.
func (a *Agent) getActiveSubFieldFn(uc *UserContext, channel, chatID string) func(key string) (string, error) {
	if uc == nil || uc.SubSvc == nil {
		return nil
	}
	svc := uc.SubSvc
	return func(key string) (string, error) {
		senderID := a.cliSenderID
		if senderID == "" {
			senderID = "cli_user"
		}
		if key == "llm_model" {
			_, model, _, _, _ := uc.ResolveLLM(chatID)
			if model != "" {
				return model, nil
			}
		}
		sub, err := svc.GetDefault(senderID)
		if err != nil {
			return "", fmt.Errorf("get default subscription: %w", err)
		}
		if sub == nil {
			return "", nil
		}
		return subFieldValue(sub, key), nil
	}
}

// updateActiveSubFn returns a function that updates a single field in the
// active subscription, backed by UserContext.
func (a *Agent) updateActiveSubFn(uc *UserContext, channel string) func(key, value string) (string, error) {
	if uc == nil || uc.SubSvc == nil {
		return nil
	}
	svc := uc.SubSvc
	return func(key, value string) (string, error) {
		senderID := a.cliSenderID
		if senderID == "" {
			senderID = "cli_user"
		}
		sub, err := svc.GetDefault(senderID)
		if err != nil {
			return "", fmt.Errorf("get default subscription: %w", err)
		}
		if sub == nil {
			return "", fmt.Errorf("no active subscription found")
		}
		oldVal := subFieldValue(sub, key)
		if err := setSubFieldValue(sub, key, value); err != nil {
			return "", err
		}
		if err := svc.Update(sub); err != nil {
			return "", fmt.Errorf("update subscription: %w", err)
		}
		uc.InvalidateLLM()
		return oldVal, nil
	}
}

// subFieldValue reads a single field from an LLMSubscription by config key.
func subFieldValue(sub *sqlite.LLMSubscription, key string) string {
	switch key {
	case "llm_provider":
		return sub.Provider
	case "llm_api_key":
		return sub.APIKey
	case "llm_base_url":
		return sub.BaseURL
	case "llm_model":
		return sub.Model
	case "max_output_tokens":
		if sub.MaxOutputTokens > 0 {
			return strconv.Itoa(sub.MaxOutputTokens)
		}
		return "4096"
	case "api_type":
		return sub.APIType
	}
	return ""
}

// setSubFieldValue sets a single field on an LLMSubscription by config key.
func setSubFieldValue(sub *sqlite.LLMSubscription, key, value string) error {
	switch key {
	case "llm_provider":
		sub.Provider = strings.TrimSpace(value)
	case "llm_api_key":
		sub.APIKey = strings.TrimSpace(value)
	case "llm_base_url":
		sub.BaseURL = strings.TrimSpace(value)
	case "llm_model":
		// Model is user-level — stored in sub.Model temporarily for the caller
		// to upsert to subscription_models. The DB column is preserved but
		// no longer read by any code path.
		sub.Model = strings.TrimSpace(value)
	case "max_output_tokens":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("max_output_tokens must be an integer: %w", err)
		}
		if n < 1 || n > 131072 {
			return fmt.Errorf("max_output_tokens must be between 1 and 131072, got %d", n)
		}
		sub.MaxOutputTokens = n
	case "api_type":
		sub.APIType = strings.TrimSpace(value)
	default:
		return fmt.Errorf("unknown subscription key: %s", key)
	}
	return nil
}

// LLMFactory returns the Agent's LLMFactory (for external injection of callbacks).
func (a *Agent) LLMFactory() *LLMFactory {
	if a.userSys == nil {
		return nil
	}
	return a.userSys.llmFactory
}

// SetLLMFactory sets the LLM factory (used in tests).
func (a *Agent) SetLLMFactory(f *LLMFactory) {
	if a.userSys == nil {
		a.userSys = &userSystem{}
	}
	a.userSys.llmFactory = f
}

// BgTaskManager returns the Agent's BackgroundTaskManager.
func (a *Agent) BgTaskManager() *tools.BackgroundTaskManager { return a.bgTaskMgr.Load() }

// SetBgTaskManager replaces the background task manager (used in tests).
// Safe against the bgNotifyLoop goroutine (started in New) — atomic swap.
func (a *Agent) SetBgTaskManager(manager *tools.BackgroundTaskManager) { a.bgTaskMgr.Store(manager) }

// Commands returns the Agent's CommandRegistry (for external consumers like RPC handlers).
func (a *Agent) Commands() *CommandRegistry { return a.commands }

// SetCommandRegistry sets the command registry (used in tests).
func (a *Agent) SetCommandRegistry(r *CommandRegistry) { a.commands = r }

// SetMessageSender sets the Dispatcher reference for unified messaging.
func (a *Agent) SetMessageSender(ms bus.MessageSender) { a.messageSender = ms }

// RegistryManager returns the Agent's RegistryManager (for external injection of callbacks).
func (a *Agent) RegistryManager() *RegistryManager { return a.registryManager }

// SettingsService returns the Agent's SettingsService (for external injection of callbacks).
func (a *Agent) SettingsService() *SettingsService {
	if a.userSys == nil {
		return nil
	}
	return a.userSys.settingsSvc
}

// MultiSession returns the Agent's MultiTenantSession (for external injection of callbacks).
func (a *Agent) MultiSession() *session.MultiTenantSession { return a.multiSession }

// Skills returns the skill store for management operations (list/toggle/view).
func (a *Agent) Skills() *SkillStore { return a.skills }

// WorkDir returns the Agent's configured working directory. Used as a
// fallback for web sessions that have no persisted CWD.
func (a *Agent) WorkDir() string { return a.workDir }

// SetIdentityResolver injects the canonical user identity resolver.
func (a *Agent) SetIdentityResolver(r *IdentityResolver) {
	if a.userSys == nil {
		a.userSys = &userSystem{}
	}
	a.userSys.identityResolver = r
}

// IdentityResolver returns the agent's identity resolver (may be nil in standalone mode).
func (a *Agent) IdentityResolver() *IdentityResolver {
	if a.userSys == nil {
		return nil
	}
	return a.userSys.identityResolver
}

// RewindCheckpoint restores files for an existing checkpointed session. It
// only uses stores that were already created by the normal CLI run path.
func (a *Agent) RewindCheckpoint(channel, chatID string, turnIdx int) (*protocol.RewindResult, error) {
	if turnIdx < 1 {
		return nil, nil
	}
	key := qualifyChatID(channel, chatID)
	raw, ok := a.checkpointStores.Load(key)
	if !ok {
		return nil, nil
	}
	store, ok := raw.(*tools.CheckpointStore)
	if !ok || store == nil {
		return nil, nil
	}
	result, err := store.Rewind(turnIdx)
	return &result, err
}

func (a *Agent) sessionOperationGate(channel, chatID string) *sessionOperationLease {
	key := qualifyChatID(channel, chatID)
	a.sessionOperationGatesMu.Lock()
	if a.sessionOperationGates == nil {
		a.sessionOperationGates = make(map[string]*sessionOperationGate)
	}
	gate := a.sessionOperationGates[key]
	if gate == nil {
		gate = newSessionOperationGate()
		a.sessionOperationGates[key] = gate
	}
	gate.refs++
	a.sessionOperationGatesMu.Unlock()
	return &sessionOperationLease{owner: a, key: key, gate: gate}
}

func (a *Agent) releaseSessionOperationGate(key string, gate *sessionOperationGate) {
	a.sessionOperationGatesMu.Lock()
	defer a.sessionOperationGatesMu.Unlock()
	if a.sessionOperationGates[key] != gate || gate.refs <= 0 {
		return
	}
	gate.refs--
	if gate.refs == 0 {
		delete(a.sessionOperationGates, key)
	}
}

// RewindHistory commits the DB truncate first, then best-effort restores files.
// A checkpoint error is returned in-band because history has already rewound.
func (a *Agent) RewindHistory(channel, chatID string, historyID int64) (protocol.HistoryRewindResult, error) {
	if a.multiSession == nil {
		return protocol.HistoryRewindResult{}, fmt.Errorf("multi-session not available")
	}
	gate := a.sessionOperationGate(channel, chatID)
	if !gate.tryLock() {
		return protocol.HistoryRewindResult{}, fmt.Errorf("cannot rewind while session is processing")
	}
	defer gate.unlock()
	// Interactive paths that have not yet joined the common gate still publish
	// active cancel state. Fail closed while they are running.
	if a.IsProcessingByChannel(channel, chatID) {
		return protocol.HistoryRewindResult{}, fmt.Errorf("cannot rewind while session is processing")
	}
	target, turnIdx, err := a.multiSession.RewindHistory(channel, chatID, historyID)
	if err != nil {
		return protocol.HistoryRewindResult{}, err
	}
	// The truncate is committed and the operation gate is still held, so no Run
	// can repopulate these snapshots until the reset event has been published.
	progressKey := qualifyChatID(channel, chatID)
	a.lastProgressSnapshot.Delete(progressKey)
	a.iterationHistories.Delete(progressKey)
	a.clearStreamState(progressKey)
	a.ClearPendingAskUser(channel, chatID)
	if channel == "agent" {
		a.syncInteractiveSessionAfterRewind(chatID)
	}
	result := protocol.HistoryRewindResult{
		TargetHistoryID: target.ID,
		Draft:           target.Content,
		HistoryRewound:  true,
		FilesRewound:    true,
	}
	checkpoint, checkpointErr := a.RewindCheckpoint(channel, chatID, turnIdx)
	result.Checkpoint = checkpoint
	result.FilesRewound, result.CheckpointError = checkpointOutcome(checkpoint, checkpointErr)
	a.emitSessionState(protocol.SessionEvent{
		Channel: channel, ChatID: chatID, Action: "history_rewound", TargetHistoryID: target.ID,
	})
	return result, nil
}

func checkpointOutcome(checkpoint *protocol.RewindResult, err error) (bool, string) {
	if err != nil {
		return false, err.Error()
	}
	if checkpoint != nil && len(checkpoint.Errors) > 0 {
		return false, fmt.Sprintf("checkpoint reported %d file errors", len(checkpoint.Errors))
	}
	return true, ""
}

// SetUserModel sets the user's default model via an explicit (subID, model) pair.
// Used by the settings card callback (feishu/web) and the set_user_model RPC.
// When subID is empty, falls back to ResolveSubscriptionForModel (legacy UIs
// that only know the model name). Persists the choice to user_default_model.
func (a *Agent) SetUserModel(senderID, subID, model string) error {
	if model == "" {
		return fmt.Errorf("model is required")
	}
	if subID == "" {
		sub, err := a.userSys.llmFactory.ResolveSubscriptionForModel(senderID, model)
		if err != nil {
			return fmt.Errorf("resolve subscription for model %q: %w", model, err)
		}
		subID = sub.ID
	}
	// Persist the default model under the canonical user_id so linked
	// identities (web/cli/feishu) all see the same selection.
	if uid, ok := a.resolveUserID(senderID); ok {
		if err := a.userSys.llmFactory.SetUserDefaultModelByUserID(uid, subID, model); err != nil {
			return fmt.Errorf("save default model: %w", err)
		}
	} else if err := a.userSys.llmFactory.SetUserDefaultModel(senderID, subID, model); err != nil {
		return fmt.Errorf("save default model: %w", err)
	}
	a.userSys.llmFactory.Invalidate(senderID)
	return nil
}

// SetChannelFinder sets the channel finder callback (for external injection).
// Also propagates to SettingsService so it can resolve channels by name.
func (a *Agent) SetChannelFinder(fn func(name string) (channel.Channel, bool)) {
	a.channelFinder = fn
	if a.userSys != nil && a.userSys.settingsSvc != nil {
		a.userSys.settingsSvc.SetChannelFinder(fn)
	}
}

// SetChannelRange sets the channel range callback for broadcasting to all
// registered channels (including plugin channels). Injected from main.go
// via Dispatcher.RangeChannels.
func (a *Agent) SetChannelRange(fn func(func(string, channel.Channel) bool)) {
	a.channelRange = fn
}

// emitSessionState pushes a session state event to registered channels.
func (a *Agent) emitSessionState(ev protocol.SessionEvent) {
	sharedServerHub := false
	if a.channelFinder != nil {
		if cliChannel, ok := a.channelFinder("cli"); ok {
			_, sharedServerHub = cliChannel.(*web.RemoteCLIChannel)
		}
	}

	publish := func(name string, ch channel.Channel) {
		if sharedServerHub &&
			((ev.Channel == "cli" && name == "web") ||
				(ev.Channel == "web" && name == "cli")) {
			return
		}
		if sender, ok := ch.(channel.SessionStateSender); ok {
			sender.SendSessionState(ev)
		}
	}

	if a.channelRange != nil {
		a.channelRange(func(name string, ch channel.Channel) bool {
			publish(name, ch)
			return true
		})
		return
	}
	if a.channelFinder == nil {
		return
	}
	for _, name := range []string{"cli", "web"} {
		if ch, ok := a.channelFinder(name); ok {
			publish(name, ch)
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
	if err := row.Scan(&oldName); err != nil {
		log.Warn("Failed to scan old name: ", err)
	}
	if oldName == "" {
		_, oldName = cli.ParseChatID(chatID)
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

// ActiveSessionKeys returns all active "channel:chatID" keys from
// chatCancelCh. Used by graceful shutdown to collect sessions whose
// agent loops should be resumed on next startup.
func (a *Agent) ActiveSessionKeys() []string {
	var keys []string
	a.chatCancelCh.Range(func(key, _ any) bool {
		if k, ok := key.(string); ok {
			keys = append(keys, k)
		}
		return true
	})
	return keys
}

// GetPendingAskUser returns the pending AskUser prompt for a chat, or nil.
// Used by the web channel to resend ask_user on WS reconnect so refreshing
// the page doesn't lose the prompt.
func (a *Agent) GetPendingAskUser(ch, chatID string) *protocol.ProgressEvent {
	var result *protocol.ProgressEvent
	a.WithPendingAskUser(ch, chatID, func(pending *protocol.ProgressEvent) bool {
		result = pending
		return true
	})
	return result
}

// WithPendingAskUser invokes fn with a snapshot while preventing the pending
// prompt from being cleared. Callers can use this to make publication or
// delivery admission linearizable with an AskUser answer. fn must stay bounded,
// must not perform network I/O, and must not mutate pending AskUser state.
func (a *Agent) WithPendingAskUser(ch, chatID string, fn func(*protocol.ProgressEvent) bool) bool {
	if fn == nil {
		return false
	}
	for {
		key, entry := a.loadPendingAskUserEntry(ch, chatID)
		if entry == nil {
			return false
		}

		entry.mu.RLock()
		current, ok := a.waitingUserSessions.Load(key)
		if !ok || current != entry || entry.pending == nil {
			entry.mu.RUnlock()
			continue
		}
		snapshot := clonePendingAskUser(entry.pending)
		result := func() bool {
			defer entry.mu.RUnlock()
			return fn(snapshot)
		}()
		return result
	}
}

func (a *Agent) loadPendingAskUserEntry(ch, chatID string) (string, *pendingAskUserEntry) {
	if ch == "" || chatID == "" {
		return "", nil
	}
	key := qualifyChatID(ch, chatID)
	if value, ok := a.waitingUserSessions.Load(key); ok {
		return key, value.(*pendingAskUserEntry)
	}
	if a.multiSession != nil {
		if sess, err := a.multiSession.GetOrCreateSession(ch, chatID); err == nil {
			if replay, err := sess.Replay(); err == nil && replay.PendingAskUser != nil {
				event := &protocol.ProgressEvent{}
				metadata := replay.PendingAskUser.Metadata
				event.RequestID = metadata["request_id"]
				if raw := metadata["ask_questions"]; raw != "" {
					_ = json.Unmarshal([]byte(raw), &event.Questions)
				}
				entry := &pendingAskUserEntry{pending: event}
				actual, _ := a.waitingUserSessions.LoadOrStore(key, entry)
				return key, actual.(*pendingAskUserEntry)
			}
		}
	}
	return key, nil
}

func (a *Agent) setPendingAskUser(ch, chatID string, pending *protocol.ProgressEvent) {
	if ch == "" || chatID == "" {
		return
	}
	if pending == nil {
		a.clearPendingAskUser(ch, chatID)
		return
	}
	key := qualifyChatID(ch, chatID)
	for {
		fresh := &pendingAskUserEntry{pending: clonePendingAskUser(pending)}
		value, loaded := a.waitingUserSessions.LoadOrStore(key, fresh)
		if !loaded {
			return
		}
		entry := value.(*pendingAskUserEntry)
		entry.mu.Lock()
		current, ok := a.waitingUserSessions.Load(key)
		if !ok || current != entry {
			entry.mu.Unlock()
			continue
		}
		entry.pending = clonePendingAskUser(pending)
		entry.mu.Unlock()
		return
	}
}

// ClearPendingAskUser removes the pending AskUser prompt for a chat.
// Called when the user answers or cancels.
func (a *Agent) ClearPendingAskUser(ch, chatID string) {
	a.clearPendingAskUser(ch, chatID)
}

func (a *Agent) clearPendingAskUser(ch, chatID string) bool {
	if ch == "" || chatID == "" {
		return false
	}
	return a.clearPendingAskUserKey(qualifyChatID(ch, chatID))
}

func (a *Agent) clearPendingAskUserKey(key string) bool {
	for {
		value, ok := a.waitingUserSessions.Load(key)
		if !ok {
			return false
		}
		entry := value.(*pendingAskUserEntry)
		entry.mu.Lock()
		current, ok := a.waitingUserSessions.Load(key)
		if !ok || current != entry {
			entry.mu.Unlock()
			continue
		}
		cleared := entry.pending != nil
		entry.pending = nil
		a.waitingUserSessions.CompareAndDelete(key, entry)
		entry.mu.Unlock()
		return cleared
	}
}

func clonePendingAskUser(pending *protocol.ProgressEvent) *protocol.ProgressEvent {
	if pending == nil {
		return nil
	}
	result := *pending
	result.Questions = append([]protocol.AskUserQuestion(nil), pending.Questions...)
	for i := range result.Questions {
		result.Questions[i].Options = append([]string(nil), pending.Questions[i].Options...)
	}
	return &result
}

func (a *Agent) sendPendingAskUserCancelAck(msg bus.InboundMessage) {
	if err := a.sendMessage(msg.Channel, msg.ChatID, "", map[string]string{
		"cancelled": "true",
		"no_patch":  "true",
	}); err != nil {
		log.WithError(err).Warn("Failed to send pending AskUser cancel ack")
	}
}

func (a *Agent) clearPendingAskUserForEnqueuedAnswer(msg bus.InboundMessage) {
	if msg.Metadata != nil && msg.Metadata["ask_user_answered"] == "true" {
		a.ClearPendingAskUser(msg.Channel, msg.ChatID)
	}
}

func (a *Agent) interceptCancel(msg bus.InboundMessage) {
	cancelKey := msg.Channel + ":" + msg.ChatID
	log.WithField("cancel_key", cancelKey).Info("Received /cancel request")
	a.cancelStateMu.Lock()
	if ch, ok := a.chatCancelCh.Load(cancelKey); ok {
		// Record the request synchronously. The cancel listener may not consume
		// the channel before teardown snapshots reqCtx.
		a.pendingCancel.Store(cancelKey, true)
		sent := false
		select {
		case ch.(chan struct{}) <- struct{}{}:
			sent = true
		default:
			log.WithField("cancel_key", cancelKey).Warn("Cancel signal already sent (buffer full)")
		}
		// A prompt may have been stored just before the active Run returned.
		// Clear it, but never replace the active cancellation with an early ack.
		a.clearPendingAskUser(msg.Channel, msg.ChatID)
		// Persist ask_answer to invalidate the pending ask_question record.
		// Without this, Replay() finds an unanswered ask_question on reload
		// and restores the AskUser prompt. The wasCancelled path (line ~2927)
		// can't do this because GetPendingAskUser returns nil after the clear above.
		if a.multiSession != nil {
			if sess, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID); err == nil {
				if _, err := sess.AppendAskAnswer("[cancelled]"); err != nil {
					log.WithError(err).Warn("Failed to append ask_answer for cancelled AskUser (active Run)")
				}
			}
		}
		a.cancelStateMu.Unlock()
		if sent {
			log.Info("Cancel signal sent to processing goroutine")
			if existingID, ok := a.sessionMsgIDs.Load(qualifyChatID(msg.Channel, msg.ChatID)); ok {
				if id, ok := existingID.(string); ok {
					a.addReactionToMessage(msg.Channel, msg.ChatID, id, "CrossMark")
				}
			}
		}
		return
	}
	if a.clearPendingAskUser(msg.Channel, msg.ChatID) {
		// Persist ask_answer to invalidate the pending ask_question record.
		// Without this, Replay() finds an unanswered ask_question on reload
		// and restores the AskUser prompt — the user sees it again after
		// refresh even though they cancelled.
		if a.multiSession != nil {
			if sess, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID); err == nil {
				if _, err := sess.AppendAskAnswer("[cancelled]"); err != nil {
					log.WithError(err).Warn("Failed to append ask_answer for cancelled AskUser")
				}
			}
		}
		a.pendingCancel.Delete(cancelKey)
		a.cancelStateMu.Unlock()
		// AskUser 交互结束（用户 cancel）：解除 WaitingUser 的 busy 状态并发射
		// session(idle)。WaitingUser 时 chatProcessLoop 有意保持 ss.busy=true
		// （防止 chatWorker 在 AskUser panel 显示期间 drain 通知），cancel 若
		// 不清除，前端 session tree 的 running 状态永远保持 → 会话卡 busy、
		// cancel 看似无效、无法交互（用户报告："后台 web 会话用 askuser 取消后
		// 永远卡 busy，cancel 无效，什么事情都做不了"）。
		if state, ok := a.bgSessionStates.Load(cancelKey); ok {
			if ss := state.(*bgSessionState); ss.busy.Load() {
				ss.busy.Store(false)
				tools.GlobalWorktreeRegistry.SetBusy(cancelKey, false)
				a.emitSessionState(protocol.SessionEvent{
					Channel: msg.Channel, ChatID: msg.ChatID, Action: "idle",
				})
			}
		}
		a.sendPendingAskUserCancelAck(msg)
		log.WithField("cancel_key", cancelKey).Info("Cancelled pending AskUser prompt")
		return
	}

	// An AskUser panel cancel (ask_user_response cancelled:true, routed here
	// from web.go with the ask_user_cancel marker) reaching a state with NO
	// active Run and NO pending prompt means the AskUser interaction has
	// already fully resolved (answer processed via this or another client,
	// or the WaitingUser turn already ended). This is a stale/duplicate cancel
	// — arming pendingCancel here would poison the user's NEXT message: its
	// Run starts and registerActiveCancelState immediately cancels it
	// ("cancel 掉 AskUser 后，下一条消息被取消了"). Ignore it entirely.
	if msg.Metadata != nil && msg.Metadata["ask_user_cancel"] == "true" {
		a.cancelStateMu.Unlock()
		log.WithField("cancel_key", cancelKey).Info("AskUser cancel ignored: no active Run and no pending AskUser prompt")
		return
	}

	// The request is queued or waiting for a semaphore. Its worker consumes
	// this marker before processMessage starts and sends the acknowledgement
	// only after the cancellation has completed.
	a.pendingCancel.Store(cancelKey, true)
	a.cancelStateMu.Unlock()
	log.WithField("cancel_key", cancelKey).Info("Cancel pending: request not yet active, will cancel when it starts")
}

func (a *Agent) registerActiveCancelState(cancelKey string, cancelCh chan struct{}, reqCancel context.CancelFunc) bool {
	a.cancelStateMu.Lock()
	defer a.cancelStateMu.Unlock()
	a.chatCancelCh.Store(cancelKey, cancelCh)
	_, pending := a.pendingCancel.LoadAndDelete(cancelKey)
	if pending {
		reqCancel()
	}
	return pending
}

func (a *Agent) finishActiveCancelState(cancelKey string, reqCtx context.Context, reqCancel context.CancelFunc) bool {
	a.cancelStateMu.Lock()
	defer a.cancelStateMu.Unlock()
	if _, pending := a.pendingCancel.LoadAndDelete(cancelKey); pending {
		reqCancel()
	}
	wasCancelled := reqCtx.Err() == context.Canceled
	a.chatCancelCh.Delete(cancelKey)
	reqCancel()
	return wasCancelled
}

// SetProxyLLM injects a ProxyLLM for a user (when their active runner has local LLM).
func (a *Agent) SetProxyLLM(senderID string, proxy *llm.ProxyLLM, model string) {
	a.userSys.llmFactory.SetProxyLLM(senderID, proxy, model)
}

// ClearProxyLLM removes a ProxyLLM for a user.
func (a *Agent) ClearProxyLLM(senderID string) {
	a.userSys.llmFactory.ClearProxyLLM(senderID)
}

// GetDefaultModel returns the default model name.
func (a *Agent) GetDefaultModel() string {
	return a.userSys.llmFactory.GetDefaultModel()
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
	Bus            *bus.MessageBus
	LLM            llm.LLM
	Model          string
	MaxIterations  int    // 单次对话最大工具调用迭代次数
	MaxConcurrency int    // 最大并发会话处理数（默认 3）
	DBPath         string // SQLite 数据库路径（空则使用默认路径）
	SkillsDir      string // Skills 目录
	AgentsDir      string // Agents 目录（空则使用 WorkDir/.xbot/agents）
	// DisabledSkills 全局 skill 黑名单：这些 skill 不会出现在 available_skills
	// catalog 中（LLM 不可见、不可激活）。
	DisabledSkills []string
	// DisabledTools 全局内置 tool 黑名单：这些 tool 跳过注册（不可见、不可执行）。
	DisabledTools   []string
	WorkDir         string // 工作目录（所有文件相对此目录）
	PromptFile      string // 系统提示词模板文件路径（空则使用内置默认值）
	DirectWorkspace string `json:"-"` // 非空时直接作为 workspaceRoot（CLI 模式使用）
	// DeltaPush 启用流式 delta push（增量文本）。默认 false = 每次推送完整
	// 累积文本（简单可靠）。见 config.AgentConfig.DeltaPush。
	DeltaPush   bool
	SandboxMode string        // 沙箱模式: "none" 或 "docker"（默认 "docker"）
	Sandbox     tools.Sandbox // Sandbox 实例引用（V4 新增）

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

	// SubAgent 深度控制
	MaxSubAgentDepth int // SubAgent 最大嵌套深度（默认 6）

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

	// SingleUser enables single-user mode: all senders are treated as one
	// shared identity. Set from config.Agent.Experimental.SingleUser.
	SingleUser bool
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
		registry.RegisterForChannel("feishu", t)
	}

	// GenUI (display_html) 已插件化：由 stdio channel 插件 xbot-genui 声明
	// `display_html` 工具（channels:["web"] + ui 元数据），主仓库不再内置注册。
	// 见 docs/agent/genui-plugin-design.md。

	// Clean up expired waiting cards from previous runs (TTL: 24h)
	if n := cardBuilder.CleanupExpiredWaitingCards(24 * time.Hour); n > 0 {
		log.WithField("count", n).Info("Cleaned up expired waiting cards")
	}

	// 全局黑名单：skill 从 catalog 排除，内置 tool 从 registry 注销。
	// 注意：DownloadFileTool / WebSearchTool 在 agent.New 返回后才注册
	// （server_core.go），需在调用方对它们再做一次 DisableTools。
	skillStore.SetDisabledSkills(cfg.DisabledSkills)
	for _, name := range cfg.DisabledTools {
		if name = strings.TrimSpace(name); name != "" {
			registry.Unregister(name)
			log.WithField("tool", name).Info("Tool disabled by blacklist")
		}
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
	a.memoryProvider = memoryProvider

	multiSession.SetMCPConfigPath(mcpConfigPath)

	// 设置会话 MCP 管理器提供者
	registry.SetSessionMCPManagerProvider(multiSession)

	// 全局工具索引通过 IndexGlobalTools() 在所有工具注册完成后调用

	// 注册记忆工具（通过注册表，无硬编码 provider 名称）
	for _, tool := range tools.GetMemoryTools(memoryProvider) {
		registry.RegisterCore(tool)
	}
	if memoryProvider != "none" && len(tools.GetMemoryTools(memoryProvider)) > 0 {
		log.WithField("provider", memoryProvider).Info("Memory tools registered (core)")
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

	// LLM factory: per-user subscriptions are the single source for custom LLM.
	if a.userSys == nil {
		a.userSys = &userSystem{}
	}
	a.userSys.llmFactory = NewLLMFactory(cfg.LLM, cfg.Model)
	a.userSys.llmFactory.SetSubscriptionSvc(sqlite.NewLLMSubscriptionService(multiSession.DB()))
	a.userSys.llmFactory.SetTenantSvc(sqlite.NewTenantService(multiSession.DB()))

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
	// Per-tenant 惰性实例（maskStoreFor）：多租户 server 下共享单例 +
	// SetTenantID 切换是跨租户数据竞态（A 的 Mask 落进 B 的目录），改为
	// map[tenantID]*ObservationMaskStore 隔离。engine 层通过 RunConfig.MaskStore 控制。
	// 磁盘落在全局 ~/.xbot/mask/{tenantID}/，避免污染当前工作目录。
	maskDir := cfg.MaskDir
	if maskDir == "" {
		maskDir = filepath.Join(a.xbotHome, "mask")
	}
	a.maskBaseDir = maskDir

	// Start periodic cleanup for offload and mask data.
	// Runs immediately at startup, then every 6 hours.
	a.lifecycleWG.Add(1)
	go func() {
		defer a.lifecycleWG.Done()
		a.periodicCleanup()
	}()

	// 注册 offload_recall 工具（需要 OffloadStore 依赖注入）
	if a.offloadStore != nil {
		recallTool := &tools.OffloadRecallTool{Store: a.offloadStore}
		registry.RegisterCore(recallTool)
	}

	// 注册 recall_masked 工具（需要 MaskStore 依赖注入）。
	// tenantMaskRouter 按执行时 ToolContext.TenantID 路由到 per-tenant 实例，
	// 多租户 server 下并发会话互不可见。
	registry.RegisterCore(&tools.RecallMaskedTool{Store: &tenantMaskRouter{a: a}})

	// 初始化 ContextEditor（Context Editing 工具 — 精确编辑上下文）
	editStore := NewContextEditStore(100)
	contextEditor := NewContextEditor(editStore)
	a.contextEditor = contextEditor
	registry.RegisterCore(&tools.ContextEditTool{})

	// 初始化并注册 TODO 管理工具
	todoMgr := tools.NewTodoManager()
	a.todoManager = todoMgr
	registry.RegisterCore(&tools.TodoWriteTool{Manager: todoMgr})
	registry.RegisterCore(&tools.TodoListTool{Manager: todoMgr})

	// 初始化 GoalManager 并注册工具 + PreTurnEnd hook
	a.goalManager = NewGoalManager()
	registry.RegisterCore(&setGoalCompleteTool{manager: a.goalManager})
	a.hookManager.RegisterBuiltin(a.goalManager.PreTurnEndHook())

	// Register AI-Native TUI & Config tools as core (always available)
	registry.RegisterCore(&tools.TuiControlTool{})
	registry.RegisterCore(&tools.ConfigTool{})

	// Initialize RegistryManager
	a.registryManager = NewRegistryManager(a.skills, a.agents, cfg.WorkDir, cfg.XbotHome, cfg.Sandbox)

	// Initialize UserSettingsService and SettingsService
	userSettingsSvc := sqlite.NewUserSettingsService(multiSession.DB())
	a.userSys.settingsSvc = NewSettingsService(userSettingsSvc)

	// Initialize LLMSemaphoreManager and inject dependencies
	llmSemMgr := llm.NewLLMSemaphoreManager()
	a.userSys.llmFactory.SetLLMSemaphoreManager(llmSemMgr)
	a.userSys.llmFactory.SetSettingsService(a.userSys.settingsSvc)

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

	rm := runner.NewManager()
	agent := &Agent{
		bus:            cfg.Bus,
		multiSession:   multiSession,
		tools:          registry,
		maxIterations:  cfg.MaxIterations,
		maxConcurrency: cfg.MaxConcurrency,
		deltaPush:      cfg.DeltaPush,

		skills:             skillStore,
		agents:             agentStore,
		chatHistory:        chatHistory,
		cardBuilder:        cardBuilder,
		workDir:            cfg.WorkDir,
		promptLoader:       NewPromptLoader(cfg.PromptFile),
		sandboxMode:        sandboxMode,
		sandbox:            cfg.Sandbox,
		runnerManager:      rm,
		sandboxIdleTimeout: cfg.SandboxIdleTimeout,
		toolProviders: []tools.ToolProvider{
			newAgentToolProvider(),
			runner.NewToolProvider(rm),
		},
		directWorkspace:  cfg.DirectWorkspace,
		globalSkillDirs:  resolveGlobalSkillsDirs(cfg.SkillsDir),
		maxSubAgentDepth: cfg.MaxSubAgentDepth,
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		agentsDir: filepath.Join(cfg.WorkDir, ".xbot", "agents"),
		xbotHome:  cfg.XbotHome,
		// approvalState is created before hookManager so it can be shared:
		// the same instance is registered as a builtin and exposed via
		// accessor methods.
		approvalState: hooks.NewApprovalState(nil), // handler set later by channel when available
		hookManager: func() *hooks.Manager {
			mgr, err := hooks.NewManager(cfg.XbotHome, cfg.WorkDir)
			if err != nil {
				log.WithError(err).Warn("Failed to load hooks config, using empty manager")
				mgr, _ = hooks.NewManager(cfg.XbotHome, cfg.WorkDir)
			}
			return mgr
		}(),
		cliSenderID:     cfg.CLISenderID,
		singleUser:      cfg.SingleUser,
		lifecycleStopCh: make(chan struct{}),
	}

	// bgTaskMgr via atomic Store (before any background goroutine starts —
	// bgNotifyLoop reads it via Load; SetBgTaskManager may replace it later).
	agent.bgTaskMgr.Store(tools.NewBackgroundTaskManager())

	// 5. 初始化各类服务（修改 agent 指针）
	initServices(agent, cfg, multiSession, registry)

	// 5b. Register builtin hooks on the shared hookManager.
	// Uses the same approvalState instance stored on the Agent.
	agent.hookManager.RegisterBuiltin(hooks.LoggingCallback())
	agent.hookManager.RegisterBuiltin(hooks.ApprovalCallback(agent.approvalState))

	// 5b-2. Create checkpoint state and register checkpoint hook for rewind file rollback.
	// The CheckpointStore is created per-session (in processMessage) and set via SetStore.
	agent.checkpointState = protocol.NewCheckpointState(nil)
	agent.hookManager.RegisterBuiltin(hooks.CheckpointCallback(agent.checkpointState))

	// 5c. Initialize plugin system (if enabled in config)
	if cfg.PluginEnabled {
		agent.pluginMgr = plugin.NewPluginManager(cfg.XbotHome)
		agent.webUIReg = plugin.NewWebUIRegistry()
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
		// Wire plugin activator into RegistryManager so newly installed plugins
		// (via /app install) are activated immediately without manual reload.
		// ReloadAll triggers OnReload callbacks which re-wire hooks/tools/widgets/commands.
		//
		// Note: ReloadAll reloads ALL plugins, not just the installed one.
		// This is intentional — OnReload callbacks (which re-wire hooks/tools/widgets)
		// only fire on full reload, not on single-plugin Activate. The O(n) overhead
		// is acceptable since /app install is a rare manual operation.
		agent.registryManager.SetPluginActivator(func(pluginID string) error {
			return agent.pluginMgr.ReloadAll(context.Background())
		})
		// Wire plugin deactivator so /app uninstall stops the plugin (hooks,
		// widgets, runtime) after removing files.
		// uninstallPlugin deletes files FIRST, then calls this — so ReloadAll
		// won't re-discover the deleted plugin. Same O(n) note as above.
		agent.registryManager.SetPluginDeactivator(func(pluginID string) error {
			return agent.pluginMgr.ReloadAll(context.Background())
		})
		// Wire plugin capabilities to xbot subsystems
		hookBridge := plugin.NewPluginHookBridge()
		enricherReg := plugin.NewEnricherRegistry()
		if err := plugin.WireAll(agent.pluginMgr, registry, hookBridge, enricherReg); err != nil {
			log.WithError(err).Warn("Plugin wiring failed")
		}
		// Wire channel providers registered by plugins to ChannelProviderRegistry.
		plugin.WireChannelProviders(agent.pluginMgr)
		// Wire plugin commands into the agent command registry.
		plugin.WirePluginCommands(agent.pluginMgr, func(name, description string, handler plugin.PluginCommandHandler, pctx plugin.PluginContext) {
			agent.commands.Register(&pluginCmdAdapter{
				name:        name,
				description: description,
				handler:     handler,
				pctx:        pctx,
			}, CommandInfo{Name: name, Usage: name, Description: description})
		})
		// Re-wire commands after every plugin reload
		agent.pluginMgr.OnReload(func() {
			// Remove old plugin commands before re-registering to avoid duplicates
			agent.commands.mu.Lock()
			filtered := agent.commands.commands[:0]
			for _, cmd := range agent.commands.commands {
				if !isPluginCommand(cmd) {
					filtered = append(filtered, cmd)
				}
			}
			agent.commands.commands = filtered
			agent.commands.mu.Unlock()

			plugin.WirePluginCommands(agent.pluginMgr, func(name, description string, handler plugin.PluginCommandHandler, pctx plugin.PluginContext) {
				agent.commands.Register(&pluginCmdAdapter{
					name:        name,
					description: description,
					handler:     handler,
					pctx:        pctx,
				}, CommandInfo{Name: name, Usage: name, Description: description})
			})
			plugin.WirePluginCrons(agent.pluginMgr, agent.cronSvc)
			plugin.WirePluginThemes(agent.pluginMgr, func(id string, data []byte) error {
				themesDir := filepath.Join(agent.xbotHome, "themes")
				os.MkdirAll(themesDir, 0755)
				return os.WriteFile(filepath.Join(themesDir, id+".json"), data, 0644)
			})
		})
		// Wire plugin crons into the cron service.
		plugin.WirePluginCrons(agent.pluginMgr, agent.cronSvc)
		// Wire plugin themes into the local themes directory.
		plugin.WirePluginThemes(agent.pluginMgr, func(id string, data []byte) error {
			themesDir := filepath.Join(agent.xbotHome, "themes")
			if err := os.MkdirAll(themesDir, 0755); err != nil {
				return err
			}
			themePath := filepath.Join(themesDir, id+".json")
			return os.WriteFile(themePath, data, 0644)
		})
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
		// Broadcast widget updates to every channel implementing WidgetSubscriber.
		// Each channel decides its own rendering (CLI → ANSI, Web → structured JSON)
		// and push target. Local CLI mode overrides this via CLIChannel.SetWidgetRegistry
		// (it registers its own OnUpdated with the asyncCh callback).
		pm.WidgetRegistry().OnUpdated(func() {
			if agent.channelRange == nil {
				return
			}
			agent.channelRange(func(_ string, ch channel.Channel) bool {
				if ws, ok := ch.(channel.WidgetSubscriber); ok {
					ws.NotifyWidgetsUpdated()
				}
				return true
			})
		})
		log.Infof("Plugin system initialized: %d active plugins", agent.pluginMgr.ActiveCount())
	} else {
		log.Debug("Plugin system disabled in config")
	}

	// 6. 启动 bg task 通知路由 goroutine
	agent.lifecycleWG.Add(1)
	go func() {
		defer agent.lifecycleWG.Done()
		agent.bgNotifyLoop()
	}()

	// 7. Inject all registered tools into the local runner's tool set.
	// This bridges the gap until tools are migrated to runner/tools/.
	agent.runnerManager.SetLocalTools(registry.List())

	// 8. Populate local runner's skill/agent declarations from the stores.
	// Base scan (embedded + global) — per-user and project-local are
	// merged at buildPrompt time via the existing store calls.
	if skills, err := agent.skills.ListSkills(context.Background(), ""); err == nil {
		entries := make([]runner.SkillEntry, len(skills))
		for i, s := range skills {
			entries[i] = runner.SkillEntry{
				Name: s.Name, Description: s.Description, Dir: s.Path,
			}
		}
		agent.runnerManager.Local().Skills = entries
	}
	if roles, err := tools.LoadAgentRoles(agent.agentsDir); err == nil {
		entries := make([]runner.Entry, 0, len(roles))
		for _, r := range roles {
			entries = append(entries, runner.Entry{
				Name: r.Name, Description: r.Description, Dir: agent.agentsDir,
			})
		}
		agent.runnerManager.Local().Agents = entries
	}

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

// GetLLMConcurrencyForUserID returns the max_concurrency for a canonical user.
// This reads the same "max_concurrency" key (channel "cli") that the CLI uses,
// so the web UI and CLI always show the same value.
func (a *Agent) GetLLMConcurrencyForUserID(userID int64) int {
	if a.userSys == nil || a.userSys.settingsSvc == nil {
		return a.getMaxConcurrency()
	}
	vals, err := a.userSys.settingsSvc.GetByUserID("cli", userID)
	if err != nil || vals == nil {
		return a.getMaxConcurrency()
	}
	s := vals["max_concurrency"]
	if s == "" {
		return a.getMaxConcurrency()
	}
	var v int
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil || v <= 0 {
		return a.getMaxConcurrency()
	}
	return v
}

// SetLLMConcurrencyForUserID sets max_concurrency for a canonical user.
// Writes to the same "max_concurrency" key (channel "cli") as the CLI.
func (a *Agent) SetLLMConcurrencyForUserID(userID int64, personal int) error {
	if a.userSys == nil || a.userSys.settingsSvc == nil {
		return ErrSettingsUnavailable
	}
	return a.userSys.settingsSvc.SetByUserID("cli", userID, "max_concurrency", fmt.Sprintf("%d", personal))
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
	// 移除旧的 middleware，创建新的
	if a.channelPromptMiddleware != nil {
		a.pipeline.Remove("channel_prompt")
	}
	a.channelPromptMiddleware = NewChannelPromptMiddleware(providers...)
	a.pipeline.Use(a.channelPromptMiddleware)
}

// AddChannelPromptProvider 动态添加一个 channel prompt provider（线程安全）。
// 适用于运行时动态注册（如 channel 插件声明专属 prompt）。
// 如果同名 provider 已存在，则覆盖更新。
func (a *Agent) AddChannelPromptProvider(provider ChannelPromptProvider) {
	a.channelPromptProviders = append(a.channelPromptProviders, provider)
	if a.channelPromptMiddleware == nil {
		// 首个 provider：创建 middleware 并插入 pipeline
		a.channelPromptMiddleware = NewChannelPromptMiddleware(provider)
		a.pipeline.Use(a.channelPromptMiddleware)
	} else {
		a.channelPromptMiddleware.AddProvider(provider)
	}
}

// HookManager returns the Agent's shared hook manager for tool execution.
// Callers can use this to register hooks, emit events, etc.
func (a *Agent) HookManager() *hooks.Manager {
	return a.hookManager
}

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
	a.closeOnce.Do(func() {
		// Cancel agent-level context to stop background subagents.
		if a.agentCancel != nil {
			a.agentCancel()
		}
		// Stop producers before stopping the notification consumer.
		if a.pluginMgr != nil {
			a.pluginMgr.DeactivateAll(context.Background())
		}
		if a.cronSch != nil {
			a.cronSch.Stop()
		}
		if a.lifecycleStopCh != nil {
			close(a.lifecycleStopCh)
		}
		a.lifecycleWG.Wait()

		// Close the database only after Agent-owned background work has exited.
		if a.multiSession != nil {
			if err := a.multiSession.Close(); err != nil {
				log.WithError(err).Warn("MultiTenantSession close error")
			}
		}
	})
	return nil
}

// PluginManager returns the plugin manager for this agent.
// Returns nil if the plugin system is not initialized.
func (a *Agent) PluginManager() *plugin.PluginManager {
	return a.pluginMgr
}

// WebUIRegistry returns the web UI component registry (web_ui protocol).
// Returns nil when the plugin system is disabled.
func (a *Agent) WebUIRegistry() *plugin.WebUIRegistry {
	return a.webUIReg
}

// RegisterChannelWebUI stores web UI component declarations from a channel
// plugin (hot-update replaces the channel's previous set).
func (a *Agent) RegisterChannelWebUI(channel string, decls []plugin.WebUIComponent) {
	if a.webUIReg == nil {
		return
	}
	a.webUIReg.SetChannel(channel, decls)
	if a.pluginMgr != nil {
		// Notify web subscribers so the new components render immediately.
		a.pluginMgr.WidgetRegistry().NotifyUpdated()
	}
}

// ChannelPluginCall sends an RPC to a channel plugin transport by channel name.
// Returns an error if the channel is not a ChannelPluginTransport.
func (a *Agent) ChannelPluginCall(channel string, method string, payload json.RawMessage) (json.RawMessage, error) {
	if a.channelFinder == nil {
		return nil, fmt.Errorf("channel finder unavailable")
	}
	ch, ok := a.channelFinder(channel)
	if !ok {
		return nil, fmt.Errorf("channel %q not found", channel)
	}
	exec, ok := ch.(plugin.ChannelToolExecutor)
	if !ok {
		return nil, fmt.Errorf("channel %q does not support RPC calls", channel)
	}
	return exec.Call(method, payload)
}

// CommandNames returns visible slash/bang commands from the registry.
func (a *Agent) CommandNames() []string {
	if a == nil || a.commands == nil {
		return nil
	}
	return a.commands.CommandNames()
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

// wantsPreReplyNotify returns true if the given channel requires text-based ack
// and progress messages (e.g. Feishu, QQ). Channels with structured progress
// (Web, CLI via ProgressSender) return false — they receive progress through
// SendProgress events and don't need ack messages.
//
// This is the single channel-capability check that replaces hardcoded channel
// name comparisons (e.g. `msg.Channel != "cli"`) in the core loop. The core
// code stays channel-agnostic: each channel declares its own behavior by
// implementing (or not) channel.PreReplyNotifier.
func (a *Agent) wantsPreReplyNotify(channelName string) bool {
	ch, ok := a.channelFinder(channelName)
	if !ok {
		return false
	}
	pn, ok := ch.(channel.PreReplyNotifier)
	return ok && pn.PreReplyNotify()
}

// qualifyChatID combines channel name and chatID into the "channel:chatID" format
// used by TUI session filtering (handleInjectedUserMsg). All inject paths must
// use this helper instead of inline string concatenation.
func qualifyChatID(channel, chatID string) string {
	return channel + ":" + chatID
}

// ensureCheckpointStore creates a per-session CheckpointStore if one doesn't
// already exist for this session key, updates the shared CheckpointState to
// point at it, and wires the CheckpointState into the CLI channel.
func (a *Agent) ensureCheckpointStore(ctx context.Context, sessionKey, channel, chatID string) {
	if a.checkpointState == nil {
		return
	}

	// Only CLI sessions need checkpoint tracking (rewind is a CLI feature).
	if channel != "cli" {
		return
	}

	// Check if we already have a store for this session.
	if _, ok := a.checkpointStores.Load(sessionKey); ok {
		// Store exists — just point the shared state at it and ensure CLI has the state.
		if raw, ok := a.checkpointStores.Load(sessionKey); ok {
			a.checkpointState.SetStore(raw.(*tools.CheckpointStore))
		}
		a.wireCheckpointStateToCLI()
		return
	}

	// Create new per-session store.
	baseDir := filepath.Join(a.xbotHome, "checkpoints", sessionKey)
	store, err := tools.NewCheckpointStore(baseDir)
	if err != nil {
		log.Ctx(ctx).WithError(err).WithField("session", sessionKey).Warn("Failed to create checkpoint store for session")
		return
	}

	a.checkpointStores.Store(sessionKey, store)
	a.checkpointState.SetStore(store)
	a.wireCheckpointStateToCLI()

	log.Ctx(ctx).WithField("session", sessionKey).Debug("Created checkpoint store for session")
}

// wireCheckpointStateToCLI passes the shared CheckpointState to the CLI channel
// so that rewind can access it for file rollback.
func (a *Agent) wireCheckpointStateToCLI() {
	if a.channelFinder == nil || a.checkpointState == nil {
		return
	}
	ch, ok := a.channelFinder("cli")
	if !ok {
		return
	}
	// CLIChannel (local mode) — checkpoint store and CLI model share the same process.
	if cliCh, ok := ch.(*cli.CLIChannel); ok {
		cliCh.SetCheckpointState(a.checkpointState)
	}
}

// Run 启动 Agent 循环，持续消费入站消息。
// 消息按 chat (channel:chatID) 分组，同一 chat 内顺序处理，不同 chat 并行处理。
// 全局并发数由 AGENT_MAX_CONCURRENCY 控制（默认 3），避免 LLM 并发过高。
// 用户设置了自己的 LLM 配置后，该用户的请求使用独立的信号量，不再占用全局资源。
func (a *Agent) Run(ctx context.Context) error {
	a.bus.EnableDeliveryAcknowledgement()
	defer a.bus.DisableDeliveryAcknowledgement()
	log.WithFields(log.Fields{
		"max_concurrency": a.getMaxConcurrency(),
	}).Info("Agent loop started")

	a.multiSession.StartCleanupRoutine()

	a.cronSch.SetNotifyCronFunc(func(channel, chatID, senderID, message string) {
		sessionKey := channel + ":" + chatID
		// nil-guard: bare &Agent{} construction (no New() Store) must not
		// panic on a cron trigger — same contract as the interactive.go read
		// sites. Normal chain stores the manager before cron starts.
		if mgr := a.bgTaskMgr.Load(); mgr != nil {
			mgr.SendCronFired(&tools.CronFired{
				Key:     sessionKey,
				Sid:     senderID,
				Message: message,
			})
		}
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
				a.interceptCancel(msg)
				acknowledgeInboundDelivery(msg, bus.DeliveryResult{})
				continue
			}

			key := msg.Channel + ":" + msg.ChatID
			q := getOrCreateQueue(key)
			select {
			case q <- msg:
				// Successfully admitted to the per-chat queue. The ack is
				// DELAYED until the chatWorker pulls the message off the
				// queue: it allocates the per-session TurnID and detects
				// whether the chat is already busy (queued), then acks
				// with {TurnID, Queued} so REST responses can return the
				// turn id directly without waiting for turn_started (which
				// may be lost/coalesced in SSE). If a message somehow never
				// reaches the chatWorker (ctx cancel), the transport's own
				// ctx/timeout will unblock the request.
				a.clearPendingAskUserForEnqueuedAnswer(msg)
			default:
				acknowledgeInboundDelivery(msg, bus.DeliveryResult{Err: bus.ErrInboundQueueFull})
				log.WithFields(log.Fields{"request_id": msg.RequestID, "chat": key}).Warn("Chat queue full, dropping message")
			}
		}
	}
}

func acknowledgeInboundDelivery(msg bus.InboundMessage, res bus.DeliveryResult) {
	if msg.DeliveryAck == nil {
		return
	}
	select {
	case msg.DeliveryAck <- res:
	default:
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
	if a.userSys.llmFactory.HasCustomLLM(senderID) {
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

	// Restore the per-session turn ID counter from DB SYNCHRONOUSLY, BEFORE
	// the processLoop goroutine starts and BEFORE the main loop can admit any
	// message. chatProcessLoop used to restore it asynchronously; a message
	// arriving at admitToMsgCh before the restore ran allocated turn_id from
	// the zeroed counter (turn_id=1), colliding with a pre-restart turn and
	// producing TURN_ID_GAP (prev=1, new=154) + cross-turn iteration pollution.
	a.restoreTurnIDSeq(chatKey, ss)

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
				log.Ctx(ctx).WithFields(log.Fields{
					"channel":     msg.Channel,
					"command":     cmd.Name(),
					"concurrent":  cmd.Concurrent(),
					"content_len": len(msg.Content),
				}).Info("Command matched in chatWorker")
				if cmd.Concurrent() {
					// 无状态命令：独立 goroutine 处理，不占信号量，不阻塞
					m := msg
					c := cmd
					clipanic.Go("agent.chatWorker.concurrentCommand", func() {
						// 并发命令即时处理：无 turn 概念，立即 ack（不预分配
						// turnID，也不产生 turn_started 事件）。
						acknowledgeInboundDelivery(m, bus.DeliveryResult{})
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
					a.admitToMsgCh(ctx, chatKey, msg, ss, msgCh)
				}
				continue
			}

			// 普通消息：转发到内部队列，由 processLoop 串行处理
			a.admitToMsgCh(ctx, chatKey, msg, ss, msgCh)

		case <-ss.notifyCh:
			a.handleBgNotifySignal(chatKey, ss)

		case <-ctx.Done():
			return
		}
	}
}

// admitToMsgCh 在消息进入 msgCh（chatProcessLoop 串行队列）前分配
// per-session TurnID 并立即 ack 给传输层。ack 携带 turn_id —— REST 响应
// 可直接返回它，不再依赖可能被 SSE 合并/丢弃的 turn_started 事件 —— 以及
// 排队状态（chat 已在处理上一条消息 → queued=true，前端显示排队标记）。
//
// AskUser answer 不预分配 turnID：其复用逻辑依赖 activeTurnID（当前正在
// 处理的 turn），若在此提前 setActiveTurn 会被排队消息污染（排队消息会
// 把 activeTurnID 改写成自己的 turn，导致 answer 复用错误的 turn id）。
// answer 的复用由 chatProcessLoop 在真正出队处理时完成。
//
// resume_turn（InjectInboundResume —— 重启恢复 / /continue）同样不预分配：
// 恢复的 Run 必须复用被中断 turn 的 id（最后一条 user 消息的 turn），预分配
// nextTurnID 会把同一逻辑 turn 拆成两个 turn —— 前端渲染成两个 assistant
// 块（用户报告"重启后这个 turn 产生两个大 dom"）。复用由 chatProcessLoop
// 出队时经 resolveResumeTurnID 从 DB 解析。
func (a *Agent) admitToMsgCh(ctx context.Context, chatKey string, msg bus.InboundMessage, ss *bgSessionState, msgCh chan<- bus.InboundMessage) {
	queued := len(msgCh) > 0 || ss.busy.Load()
	var turnID uint64
	if msg.Metadata == nil || (msg.Metadata["ask_user_answered"] != "true" && msg.Metadata["resume_turn"] != "true") {
		turnID = ss.nextTurnID()
		if msg.Metadata == nil {
			msg.Metadata = map[string]string{}
		}
		msg.Metadata["turn_id"] = strconv.FormatUint(turnID, 10)
	}
	acknowledgeInboundDelivery(msg, bus.DeliveryResult{TurnID: turnID, Queued: queued})
	select {
	case msgCh <- msg:
	case <-ctx.Done():
	}
}

// resolveResumeTurnID returns the turn id a restart-resumed Run
// (InjectInboundResume) must CONTINUE: the turn of the last non-display-only
// user message — the owner of the interrupted turn. Reusing it keeps the
// interrupted work and the resumed work in ONE turn (session_messages rows,
// iteration_history records and the frontend's per-turn rendering all merge
// into a single assistant block — identical to an uninterrupted turn).
// Every restart previously allocated a FRESH turn id (admitToMsgCh →
// nextTurnID), splitting the logical turn into user(N)/resume(N+1)/resume(N+2)
// — the frontend rendered each as a separate block ("two big DOMs").
// Returns 0 when no resolvable user turn exists (no user message / legacy rows
// without turn_id) — the caller falls back to allocating a fresh turn id.
func (a *Agent) resolveResumeTurnID(channel, chatID string) uint64 {
	if a.multiSession == nil {
		return 0
	}
	sess, err := a.multiSession.GetOrCreateSession(channel, chatID)
	if err != nil {
		log.WithFields(log.Fields{"channel": channel, "chat_id": chatID}).WithError(err).
			Warn("resolveResumeTurnID: GetOrCreateSession failed, allocating fresh turn id")
		return 0
	}
	tid, err := sess.GetLastUserTurnID()
	if err != nil {
		log.WithFields(log.Fields{"channel": channel, "chat_id": chatID}).WithError(err).
			Warn("resolveResumeTurnID: GetLastUserTurnID failed, allocating fresh turn id")
		return 0
	}
	return tid
}

func (a *Agent) handleBgNotifySignal(chatKey string, ss *bgSessionState) {
	// bg notification arrived — drain and process ONLY when chatProcessLoop is idle.
	// When busy, notifications stay in bgRunPending for chatProcessLoop's
	// post-turn drain to pick up (guaranteed after response is sent).

	if !ss.busy.Load() {
		a.drainAndProcessNotifications(chatKey)
	}
}

// restoreTurnIDSeq restores the per-session turn ID counter from DB so it
// stays globally monotonic across server restarts. Must run SYNCHRONOUSLY in
// chatWorker before any message is admitted (admitToMsgCh allocates turn ids
// via ss.nextTurnID). Running it inside chatProcessLoop (async goroutine)
// raced with the main loop: a message admitted before the restore allocated
// turn_id=1, colliding with a pre-restart turn (TURN_ID_GAP + cross-turn
// iteration pollution).
func (a *Agent) restoreTurnIDSeq(chatKey string, ss *bgSessionState) {
	if a.multiSession != nil {
		parts := strings.SplitN(chatKey, ":", 2)
		if len(parts) == 2 {
			if sess, err := a.multiSession.GetOrCreateSession(parts[0], parts[1]); err == nil {
				if maxTurnID, err := sess.GetMaxTurnID(); err == nil && maxTurnID > 0 {
					ss.turnIDSeq.Store(maxTurnID)
					log.WithFields(log.Fields{
						"chat_key":    chatKey,
						"max_turn_id": maxTurnID,
					}).Info("Restored turn ID counter from DB")
				}
			}
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
		keepRunning := func() bool {
			if ctx.Err() != nil {
				return false
			}
			opGate := a.sessionOperationGate(msg.Channel, msg.ChatID)
			if !opGate.lock(ctx) {
				return false
			}
			defer opGate.unlock()

			// Mark session busy so chatWorker skips notification drain
			ss.busy.Store(true)
			// 同步 worktree registry：该 session 正在迭代中（peer 协作提示依据，
			// busy/idle = 是否在迭代中，而非时间推断）。
			tools.GlobalWorktreeRegistry.SetBusy(qualifyChatID(msg.Channel, msg.ChatID), true)

			// 停止上一次的 idle timer（收到新消息，重置计时）
			if idleTimer != nil {
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
			}

			// TurnID 由 chatWorker 在入队时预分配（普通/有状态命令消息，
			// 见 admitToMsgCh），写入 Metadata["turn_id"] 并随 DeliveryAck 返回，
			// 使 REST 响应能直接返回 turn id（不依赖可能被 SSE 合并/丢弃的
			// turn_started 事件）。
			//
			// AskUser answer 也是独立 turn：分配新 turn_id（nextTurnID），
			// 不复用 activeTurnID。复用会让回答 user 消息与回答前的 assistant
			// 同 turn，前端按 turn 合并迭代时把回答前后的内容（如 pwd 与
			// task_wait）混进同一个 assistant 块。新 turn 保证回答后的消息
			// 与回答前严格分离。
			//
			// resume_turn（InjectInboundResume —— 重启恢复 / /continue）出队时
			// 复用被中断 turn 的 id（最后一条非 display-only user 消息的 turn，
			// resolveResumeTurnID 从 DB 解析）：中断前后的消息/迭代归属同一个
			// turn，前端按 turn 合并渲染为单个 assistant 块（与不重启一致）。
			// 无法解析（无 user 消息 / legacy 无 turn_id）时退回分配新 turn id。
			resumeTurn := msg.Metadata != nil && msg.Metadata["resume_turn"] == "true"
			turnID, _ := strconv.ParseUint(msg.Metadata["turn_id"], 10, 64)
			if turnID == 0 {
				if resumeTurn {
					turnID = a.resolveResumeTurnID(msg.Channel, msg.ChatID)
				}
				if turnID == 0 {
					turnID = ss.nextTurnID()
				}
				if msg.Metadata == nil {
					msg.Metadata = map[string]string{}
				}
				msg.Metadata["turn_id"] = strconv.FormatUint(turnID, 10)
			}
			ss.setActiveTurn(turnID)
			// Consistency check: TurnID must be strictly monotonic per session.
			// A gap or regression indicates a bug in the turn lifecycle.
			// AskUser answer is its OWN turn with a fresh turn_id (nextTurnID,
			// allocated above) — never a reuse of the previous active turn.
			// Only turnID < prev is a real violation.
			// resume_turn 豁免：它复用 DB 中被中断 turn 的 id（非计数器分配），
			// 可能小于 lastTurnID（如中断期间插入过通知 turn）——复用是合法的
			// 续turn，不是生命周期 bug；且它不消耗计数器，跳过检查与基线更新
			// 避免伪告警（gap 由后续正常分配自然对齐）。
			if !resumeTurn {
				if prev := ss.lastTurnID.Load(); prev > 0 {
					if turnID < prev {
						log.WithFields(log.Fields{
							"session_key":  chatKey,
							"prev_turn_id": prev,
							"new_turn_id":  turnID,
							"delta":        int64(turnID) - int64(prev),
						}).Error("TURN_ID_INVARIANT_VIOLATION: TurnID must be strictly increasing — got non-increasing value")
					} else if turnID > prev && turnID != prev+1 {
						log.WithFields(log.Fields{
							"session_key":  chatKey,
							"prev_turn_id": prev,
							"new_turn_id":  turnID,
							"gap":          turnID - prev - 1,
						}).Warn("TURN_ID_GAP: TurnID jumped — intermediate turn(s) may have been lost")
					}
				}
				// resume 不更新 lastTurnID 基线：复用 id 来自 DB（非计数器分配），
				// 可能小于当前基线（如中断期间插入过通知 turn）——存储它会让下一个
				// 正常分配触发伪 TURN_ID_GAP 告警。
				ss.lastTurnID.Store(turnID)
			}
			a.emitTurnStarted(msg, turnID)

			sem := a.getSemaphoreForMessage(msg)

			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				ss.busy.Store(false)
				tools.GlobalWorktreeRegistry.SetBusy(qualifyChatID(msg.Channel, msg.ChatID), false)
				return false
			}

			// 创建 per-request cancel context
			var response *channel.OutboundMsg
			var err error
			cancelCh := make(chan struct{}, 1)
			// cancelKey 仅用 channel:chatID（不含 senderID），与 /cancel 拦截处保持一致
			cancelKey := msg.Channel + ":" + msg.ChatID
			reqCtx, reqCancel := context.WithCancel(ctx)
			hadPending := a.registerActiveCancelState(cancelKey, cancelCh, reqCancel)

			// Emit session busy event for instant sidebar push.
			a.emitSessionState(protocol.SessionEvent{
				Channel: msg.Channel, ChatID: msg.ChatID, Action: "busy",
			})

			if hadPending {
				log.WithField("cancel_key", cancelKey).Info("Consumed pending cancel signal")
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

			// Execute the request, then atomically snapshot cancellation and unregister
			// the active cancel state before another /cancel can target this chat.
			wasCancelled := false
			func() {
				defer func() {
					// Panic 兜底：processMessage panic 时 goroutine 会被 clipanic.Go
					// recover（进程存活）但从此退出 —— 下方 err/正常分支的
					// ss.busy.Store(false) 永不可达，会话永久卡 busy（用户后续消息
					// 永远排队）。recover 后置 err，走统一 err 分支清理（busy 复位 +
					// 用户收到错误提示），会话可继续服务。
					if r := recover(); r != nil {
						log.WithFields(log.Fields{
							"request_id": msg.RequestID,
							"chat":       chatKey,
							"panic":      r,
							"stack":      string(debug.Stack()),
						}).Error("panic recovered in processMessage")
						response = nil
						err = fmt.Errorf("internal error: %v", r)
					}
					wasCancelled = a.finishActiveCancelState(cancelKey, reqCtx, reqCancel)

					// WaitingUser: the turn is PAUSED, not ended. Do NOT emit
					// session(idle) — the frontend's session(idle) handler triggers
					// a defensive finalize that clears iterationHistory, causing all
					// iterations from before the AskUser call to disappear. Also
					// preserve lastProgressSnapshot + iterationHistories so SSE
					// reconnect can recover the in-flight turn.
					isWaitingUser := response != nil && response.WaitingUser
					if !isWaitingUser {
						a.emitSessionState(protocol.SessionEvent{
							Channel: msg.Channel, ChatID: msg.ChatID, Action: "idle",
						})
						key := qualifyChatID(msg.Channel, msg.ChatID)
						a.lastProgressSnapshot.Delete(key)
						a.iterationHistories.Delete(key)
					}
					<-sem // 释放槽位（WaitingUser 也需要释放，让 answer 能获取）
				}()

				// 沙箱正在 export+import 时，拒绝该用户所有请求
				sbUID := sandboxUserID(msg)
				if sb := tools.GetSandbox(); sb.IsExporting(sbUID) {
					log.WithFields(log.Fields{"request_id": msg.RequestID, "sender": msg.SenderID, "sandbox_user": sbUID}).Info("Request rejected: sandbox export in progress")
					a.sendMessage(msg.Channel, msg.ChatID, "⏳ 沙箱正在持久化中，请稍后再试...")
					return
				}

				response, err = a.processMessage(reqCtx, msg)
			}()

			if wasCancelled && ctx.Err() == nil {
				// 请求被用户 /cancel 取消（而非全局 ctx 关闭）
				log.WithFields(log.Fields{"request_id": msg.RequestID, "chat": chatKey}).Info("Request cancelled by user")
				// Persist ask_answer to invalidate the pending ask_question record.
				// Without this, Replay() finds an unanswered ask_question on reload
				// and restores the AskUser prompt — the user sees it again after
				// refresh even though they cancelled.
				if pending := a.GetPendingAskUser(msg.Channel, msg.ChatID); pending != nil {
					if sess, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID); err == nil {
						if _, err := sess.AppendAskAnswer("[cancelled]"); err != nil {
							log.WithError(err).Warn("Failed to append ask_answer for cancelled AskUser")
						}
					}
				}
				a.ClearPendingAskUser(msg.Channel, msg.ChatID)
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
					if err := a.sendMessage(msg.Channel, msg.ChatID, response.Content, response.Metadata); err != nil {
						log.Warn("Failed to send response: ", err)
					}
				} else {
					// No response generated yet (cancelled mid-tool-call) — send empty
					// message to signal turn end so CLI can clean up typing/progress state.
					if err := a.sendMessage(msg.Channel, msg.ChatID, "", cancelMeta); err != nil {
						log.Warn("Failed to send cancel ack: ", err)
					}
				}
				// Do not post-turn drain here: handleCancelledRun records same-session
				// pending bg notifications in the interrupted turn, without starting a
				// fresh bg-notification turn after the cancel ack.
				ss.busy.Store(false)
				tools.GlobalWorktreeRegistry.SetBusy(qualifyChatID(msg.Channel, msg.ChatID), false)
				return true
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
				// Synthetic notification pairs are acknowledged only after their DB
				// append succeeds. Put any unacknowledged items back before retrying.
				a.requeueDrainedBgNotifications(chatKey)
				ss.busy.Store(false)
				tools.GlobalWorktreeRegistry.SetBusy(qualifyChatID(msg.Channel, msg.ChatID), false)
				a.drainAndProcessNotifications(chatKey)
				return true
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
					// WaitingUser 消息不可静默丢弃：AskUser 面板不显示 = turn 永久暂停
					// 等一个不会到达的回答（ss.busy 保持 true，会话卡死）。带超时的
					// 阻塞发送替代立即丢弃（对齐上方 err 分支的直接写语义），仅在
					// shutdown（reqCtx.Done）或 10s 仍满时放弃。
					select {
					case a.bus.Outbound <- busMsg:
					case <-reqCtx.Done():
						log.Ctx(ctx).Warn("Context cancelled, dropping WaitingUser response")
					case <-time.After(10 * time.Second):
						log.Ctx(ctx).Error("Message bus outbound channel full for 10s, dropping WaitingUser response")
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
			//
			// WaitingUser: the turn is PAUSED (waiting for user input), not ended.
			// Keep busy=true so chatWorker doesn't drain notifications while the
			// AskUser panel is showing. The answer message will be dequeued next
			// and processed as a continuation of this turn.
			if response != nil && response.WaitingUser {
				// WaitingUser: turn PAUSED waiting for user input — the session is
				// NOT iterating. Mark the peer idle for collaboration hints
				// (ss.busy stays true for chatWorker notification-drain semantics).
				tools.GlobalWorktreeRegistry.SetBusy(qualifyChatID(msg.Channel, msg.ChatID), false)
				return true
			}
			ss.clearDrainedThisRun()
			ss.busy.Store(false)
			tools.GlobalWorktreeRegistry.SetBusy(qualifyChatID(msg.Channel, msg.ChatID), false)
			a.drainAndProcessNotifications(chatKey)
			return true
		}()
		if !keepRunning {
			return
		}
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

	// Resolve all user-related components ONCE at the entry point.
	// Everything downstream reads UserContext from ctx — no direct access
	// to LLMFactory/IdentityResolver/SettingsService anywhere in the agent loop.
	userCtx := a.ResolveUserContext(msg.Channel, msg.ChatID, msg.SenderID, msg.Metadata)
	ctx = WithUserContext(ctx, userCtx)

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

	// Create the tenant with the identity already resolved by
	// ResolveUserContext. The canonical user_id is authoritative — it was
	// resolved at the channel boundary using the physical channel, and
	// ResolveUserContext already preferred metadata injection over
	// re-resolving via (msg.Channel, senderID).
	tenantOwner := int64(0)
	if msg.Channel == "web" || msg.Channel == "cli" || msg.Channel == "agent" {
		if userCtx != nil {
			tenantOwner = userCtx.UserID
		}
	}

	// Background notifications are internal system messages injected into an
	// existing session. They must NOT trigger owner verification — the senderID
	// (from cron job / bg task) may differ from the session's canonical owner
	// (e.g. CLI path vs user ID), causing ErrTenantOwnerConflict.
	var tenantSession *session.TenantSession
	var err error
	if msg.Metadata != nil && msg.Metadata[bgNotificationMetadataKey] == "true" {
		tenantSession, err = a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	} else {
		tenantSession, err = a.multiSession.GetOrCreateSessionWithOwner(msg.Channel, msg.ChatID, tenantOwner)
	}
	if err != nil {
		return nil, fmt.Errorf("get/create tenant session: %w", err)
	}

	// Ensure the memory provider is scoped to the canonical owner so memories
	// are shared across ALL sessions of this user (not per-tenant).
	// The provider may have been created earlier by buildToolContextExtras via
	// GetOrCreateSession (no owner → userID=0); fix it up now.
	if tenantOwner > 0 {
		if xm, ok := tenantSession.Memory().(*xbotmemory.XbotMemory); ok {
			xm.SetOwnerUserID(tenantOwner)
		}
	}

	// Set tenant-scoped stores for this request.
	// MaskStore 不再在此切换租户：per-tenant 实例由 maskStoreFor(tenantID) 惰性
	// 创建（engine 装配 RunConfig.MaskStore 时解析），运行中租户目录不可变，
	// 消除共享单例 SetTenantID 的跨租户竞态（A 的 Mask 落进 B 的目录）。
	tenantID := tenantSession.TenantID()
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

	// Ensure per-session checkpoint store exists and is wired to CLI channel.
	// File snapshots are persisted to ~/.xbot/checkpoints/{sessionKey}/changes.jsonl
	// and used by /rewind to restore files to their pre-edit state.
	a.ensureCheckpointStore(ctx, key, msg.Channel, msg.ChatID)

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
		out, err := cmd.Execute(ctx, a, msg)
		if err != nil {
			return nil, err
		}
		// goal_start sentinel: /goal command set a goal and wants to
		// fall through to Run() with the objective as the user message.
		if out != nil && out.Metadata != nil && out.Metadata["goal_start"] != "" {
			msg.Content = out.Metadata["goal_start"]
			// Push a progress event carrying the goal so the frontend can
			// display the GoalBanner immediately (before the first Run
			// iteration's refreshStructuredTodos).
			a.emitGoalProgress(msg.Channel, msg.ChatID)
			// fall through to Run
		} else {
			return out, nil
		}
	}

	// 处理卡片响应（按钮点击、表单提交）
	if msg.Metadata != nil && msg.Metadata["card_response"] == "true" {
		return a.handleCardResponse(ctx, msg, tenantSession)
	}

	// Channel-capability check: does this channel need text-based ack/progress?
	// (Feishu/QQ do; Web/CLI have structured progress via SendProgress events.)
	// Per-message opt-out via ReplyPolicyOptional (e.g. Feishu @all, NapCat).
	preReplyNotify := a.wantsPreReplyNotify(msg.Channel) && bus.ShouldPreReplyNotify(msg.Metadata)
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

	// AskUser 回答：记录 Q&A + 清理 pending + 持久化回答为正常 user 消息。
	askUserAnswered := msg.Metadata != nil && msg.Metadata["ask_user_answered"] == "true"
	if askUserAnswered {
		// Append the answer (control record) AND the answer user row in ONE
		// atomic transaction — crash between the two separate writes would
		// leave an ask_answer control without a user anchor (broken history).
		// The user row is a NORMAL user message bound to this turn so the web
		// history has a real "user replied" row (turn anchor for the iterations
		// that follow). Non-display-only: GetHistory/Replay excludes display_only
		// rows, so the frontend would never see it and the order would break.
		answerHistoryID, askErr := func() (int64, error) {
			if tidStr := msg.Metadata["turn_id"]; tidStr != "" {
				if tid, err := strconv.ParseUint(tidStr, 10, 64); err == nil && tid > 0 {
					answerMsg := llm.NewUserMessage(msg.Content)
					answerMsg.TurnID = tid
					return tenantSession.AppendAskAnswerWithUserMessage(msg.Content, answerMsg)
				}
			}
			return tenantSession.AppendAskAnswer(msg.Content)
		}()
		if askErr != nil {
			return nil, fmt.Errorf("append AskUser answer: %w", askErr)
		}
		_ = answerHistoryID
		a.ClearPendingAskUser(msg.Channel, msg.ChatID)
		// Remove last user message appended by Assemble
		if len(messages) > 0 && messages[len(messages)-1].Role == "user" {
			messages = messages[:len(messages)-1]
		}
		// Replace the most recent AskUser tool message content with the user's
		// answer so THIS turn's LLM context contains the answer — the model
		// cannot see the persisted answer user message in the current prompt.
		// (Without this the model keeps seeing "Asked N question(s)" and has
		// no idea the user answered.)
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
	}

	// Resume turn: the user message is already in the DB (eager-saved before
	// the original Run() started). InjectInboundResume sends an empty message
	// with resume_turn metadata — Assemble skips appending a user message
	// entirely (UserMessage is empty), so no duplicate to remove.
	resumeTurn := msg.Metadata != nil && msg.Metadata["resume_turn"] == "true"

	// 运行 Agent 循环（统一 Run）
	// Eager-save user message BEFORE Run() so incrementally persisted assistant/tool
	// messages appear after it in the DB. GetHistory uses user messages as turn boundaries.
	// Skip for resume (already in DB) and AskUser (not a new user message).
	if !askUserAnswered && !resumeTurn && (msg.Metadata == nil || msg.Metadata["user_msg_eager_saved"] != "true") {
		userMsg := llm.NewUserMessage(msg.Content)
		if !msg.Time.IsZero() {
			userMsg.Timestamp = msg.Time
		}
		// INVARIANT: every persisted user message MUST carry a non-zero
		// turn_id. admitToMsgCh allocates it at queue-admission time for all
		// messages that reach processMessage (incl. bg notifications, which
		// route through the same queue). A missing/zero turn_id here is an
		// upstream bug — persisting an unbound user row breaks turn
		// association and made the frontend render replies above the user
		// message. FAIL FAST (panic) instead of writing the row.
		tid, perr := strconv.ParseUint(msg.Metadata["turn_id"], 10, 64)
		if perr != nil || tid == 0 {
			panic(fmt.Sprintf(
				"agent: INVARIANT VIOLATION — user message has no turn_id (channel=%s chat=%s content=%.80q askUserAnswered=%v resumeTurn=%v)",
				msg.Channel, msg.ChatID, msg.Content, askUserAnswered, resumeTurn,
			))
		}
		userMsg.TurnID = tid
		historyID, err := tenantSession.AppendMessage(userMsg)
		if err != nil {
			return nil, fmt.Errorf("eager-save user message: %w", err)
		}
		if len(messages) > 0 && messages[len(messages)-1].Role == "user" {
			messages[len(messages)-1].ID = historyID
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
	cfg.AcknowledgeBgNotifications = a.wireBgNotificationAcknowledge(currentSessionKey)

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

	// Cancel early-exit: if ctx was cancelled during setup (e.g. user pressed
	// Ctrl+C while buildPrompt/buildMainRunConfig was running), bail out now
	// instead of entering Run. This is especially important for the first
	// message in a session, where setup involves DB tenant creation, workspace
	// initialization, and MCP configuration — all synchronous and collectively
	// can take several seconds. Without this check, cancel during first-message
	// setup would silently wait until Run's first iteration to take effect.
	//
	// Delegate to handleCancelledRun so the [interrupted] message is persisted
	// with user_cancelled and progress_history — a bare OutboundMsg would leave
	// the frontend without user_cancelled and without a committed message.
	if ctx.Err() != nil {
		log.Ctx(ctx).Info("processMessage: ctx cancelled during setup, skipping Run")
		return a.handleCancelledRun(ctx, msg, &RunOutput{}, tenantSession)
	}

	out := Run(ctx, cfg)

	// Auto-memorize: lightweight incremental consolidation after each turn.
	// This enables cross-session memory WITHOUT requiring /new, WITHOUT burning
	// 3 LLM calls per turn (the old code called Memorize(ArchiveAll=true) after
	// EVERY turn — the root cause of "记忆整理太频繁" + "记忆膨胀").
	//
	// Providers implementing TurnConsolidator (xbot) get throttled incremental
	// extraction: new messages accumulate until a threshold, then one LLM call
	// extracts atomic memories. Providers without it (flat/letta) fall back to
	// the old full Memorize — unchanged behavior.
	if mem := tenantSession.Memory(); mem != nil && len(out.Messages) > 0 {
		lastConsolidated := tenantSession.LastConsolidated()
		log.Ctx(ctx).WithFields(log.Fields{
			"messages":          len(out.Messages),
			"provider":          mem.Name(),
			"last_consolidated": lastConsolidated,
		}).Info("Auto-memorize: starting incremental consolidation")
		a.lifecycleWG.Add(1)
		go func(mem memory.MemoryProvider, messages []llm.ChatMessage, chatID string, llmClient llm.LLM, model string, lastCons int) {
			defer a.lifecycleWG.Done()

			// Cancel when the Agent is closed (lifecycleStopCh is closed by
			// Close()) so consolidation never touches a closed DB / released
			// LLM client. NOT derived from agentCtx — that is cancelled at the
			// end of every Run(), which would kill the consolidation before it
			// starts. The watch goroutine exits when either side fires.
			memCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if a.lifecycleStopCh != nil {
				go func() {
					select {
					case <-a.lifecycleStopCh:
						cancel()
					case <-memCtx.Done():
					}
				}()
			}

			// Shallow-copy the slice: out.Messages' backing array is shared with
			// the caller, and ConsolidateTurn may mutate elements in place.
			messagesCopy := make([]llm.ChatMessage, len(messages))
			copy(messagesCopy, messages)

			input := memory.MemorizeInput{
				Messages:         messagesCopy,
				LastConsolidated: lastCons,
				LLMClient:        llmClient,
				Model:            model,
				ArchiveAll:       false, // incremental — never full archive per turn
			}

			var result memory.MemorizeResult
			var err error
			if tc, ok := mem.(memory.TurnConsolidator); ok {
				result, err = tc.ConsolidateTurn(memCtx, input)
			} else {
				// Legacy provider: full Memorize (ArchiveAll forced true so
				// flat/letta actually do something — their Memorize no-ops when
				// ArchiveAll=false).
				input.ArchiveAll = true
				result, err = mem.Memorize(memCtx, input)
			}
			if err != nil {
				log.WithError(err).WithField("chat_id", chatID).Warn("Auto-memorize: consolidation failed")
				return
			}
			log.WithFields(log.Fields{
				"chat_id": chatID,
				"ok":      result.OK,
			}).Info("Auto-memorize: consolidation completed")
		}(mem, out.Messages, msg.ChatID, cfg.LLMClient, cfg.Model, lastConsolidated)
	} else if mem != nil && len(out.Messages) == 0 {
		log.Ctx(ctx).WithFields(log.Fields{
			"provider":  mem.Name(),
			"msg_count": len(out.Messages),
		}).Warn("Auto-memorize: skipped — out.Messages is empty (cfg.Memory may not have been set)")
	}

	// Save iteration history on cancellation, even if Run() returned nil error.
	// The context may have been cancelled after Run() finished its last iteration
	// but before it checked ctx.Done(). In that case out.Error is nil but the
	// iteration snapshots are valid and should be persisted.
	cancelled := out.Error != nil && errors.Is(out.Error, context.Canceled)
	if !cancelled && ctx.Err() == context.Canceled {
		cancelled = true
	}
	if cancelled {
		return a.handleCancelledRun(ctx, msg, out, tenantSession)
	}
	if out.Error != nil {
		return nil, out.Error
	}

	return a.handleRunOutput(ctx, msg, out, tenantSession, replyPolicy)
}

// buildPrompt 构建完整的 LLM 消息列表（共用逻辑：processMessage 和 handlePromptQuery 都调用）。
// 使用 Agent 持有的 pipeline 实例，通过 MessageContext.Extra 传递动态数据。
// fillAssistantContentFromIterations 为 content 空的 assistant 消息补充迭代内容
// （LLM 上下文构建用）。v55+ 数据模型：assistant 回复不写 session_messages.content
// （msg 是 iter 组成的集合），回复文本在 iteration_history 的最终迭代。
// 迭代 content 是权威数据源，没有才 fallback 到 msg.content（旧数据不受影响）。
func (a *Agent) fillAssistantContentFromIterations(msgs []llm.ChatMessage, tenantSession *session.TenantSession) {
	var turnIDs []uint64
	for _, m := range msgs {
		if m.Role == "assistant" && m.Content == "" && m.TurnID > 0 {
			turnIDs = append(turnIDs, m.TurnID)
		}
	}
	if len(turnIDs) == 0 {
		return
	}
	recs, err := tenantSession.GetIterationHistoryByTurns(turnIDs)
	if err != nil {
		return // 补充失败：保持 content 空（前端/CLI 从迭代取）
	}
	for i := range msgs {
		m := &msgs[i]
		if m.Role != "assistant" || m.Content != "" || m.TurnID == 0 {
			continue
		}
		iterRecs, ok := recs[m.TurnID]
		if !ok || len(iterRecs) == 0 {
			continue
		}
		// 最终迭代的 content 就是该 turn 的回复文本（最后回复 = 最终 iter）
		last := iterRecs[len(iterRecs)-1]
		if last.Content != "" {
			m.Content = last.Content
		}
	}
}

func (a *Agent) buildPrompt(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) ([]llm.ChatMessage, error) {
	userCtx := UserContextFromContext(ctx)

	history, err := tenantSession.GetMessages()
	if err != nil {
		return nil, fmt.Errorf("replay session history: %w", err)
	}
	// v55+ 数据模型：assistant 回复不再写 session_messages.content（msg 是
	// iter 组成的集合，content 是历史遗留字段）。回复文本在 iteration_history
	// 的最终迭代 —— LLM 上下文构建时从迭代取（迭代 content 是权威数据源，
	// 没有才 fallback 到 msg.content，即旧数据不受影响）。
	a.fillAssistantContentFromIterations(history, tenantSession)
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
	// UserContext already resolved settings in middleware — read from there.
	// AutoDetectAndInit is idempotent: returns existing entry if session already registered.
	if userCtx != nil && userCtx.GetSettingBool("auto_worktree") {
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
		log.Ctx(ctx).WithField("tools", len(newTools)).Info("New personal MCP tools configured")
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
	//
	// SAFETY: Verify the worktree actually belongs to this session.
	// A stale worktree path from a deleted/recreated session must NOT
	// be used — it would put the agent in an orphaned directory.
	cwd := tenantSession.GetCurrentDir()
	if cwd != "" && strings.Contains(cwd, ".xbot-worktrees") {
		// Verify ownership: the worktree must be registered to this session
		if wtEntry := tools.GlobalWorktreeRegistry.GetBySession(sessKey); wtEntry != nil && wtEntry.WorktreeDir != "" {
			// CWD must be inside the registered worktree (or exactly match it)
			if cwd == wtEntry.WorktreeDir || strings.HasPrefix(cwd, wtEntry.WorktreeDir+string(os.PathSeparator)) {
				promptWorkDir = cwd
			} else {
				// CWD points to a DIFFERENT worktree than what's registered.
				// This is a stale state leak — reset to workspace root.
				log.WithFields(log.Fields{
					"session":    sessKey,
					"cwd":        cwd,
					"registered": wtEntry.WorktreeDir,
				}).Warn("CWD points to unowned worktree, resetting to workspace root")
				tenantSession.SetCurrentDir(workspaceRoot)
				cwd = workspaceRoot
			}
		} else {
			// No worktree registered for this session, but CWD is in a worktree.
			// This is a stale state leak from a previous session — reset.
			log.WithFields(log.Fields{
				"session": sessKey,
				"cwd":     cwd,
			}).Warn("Session has worktree CWD but no registry entry, resetting to workspace root")
			tenantSession.SetCurrentDir(workspaceRoot)
			cwd = workspaceRoot
		}
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

	// Resume turn: skip user message synthesis (already in DB history)
	if msg.Metadata != nil && msg.Metadata["resume_turn"] == "true" {
		mc.ResumeTurn = true
	}

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
	permUsers := userCtx.PermUsers
	mc.SetExtra(ExtraKeyPermUsers, permUsers)
	mc.Ctx = withPermControlEnabled(mc.Ctx, IsPermControlEnabled(permUsers))

	mc.SetExtra(ExtraKeyTenantID, tenantSession.TenantID())

	// Session name for rename hint (only injected on first user message)
	_, sessionName := cli.ParseChatID(msg.ChatID)
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
	case strings.Contains(msg, "stream ended without finish_reason") ||
		strings.Contains(msg, "unexpected EOF"):
		return "流式响应被截断"
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

// RegisterToolForChannel registers a channel-scoped tool.
// The tool is only visible in sessions of the specified channel.
func (a *Agent) RegisterToolForChannel(channel string, tool tools.Tool) {
	a.tools.RegisterForChannel(channel, tool)
	log.WithField("tool", tool.Name()).WithField("channel", channel).Info("Channel tool registered")
}

// DisableTools unregisters the given GLOBAL tool blacklist. These tools become
// invisible AND unexecutable (AsDefinitions skips them, so the LLM never sees
// them; GetForSession returns not-found, so they can never run). Empty/unknown
// names are no-ops.
func (a *Agent) DisableTools(names []string) {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		a.tools.Unregister(name)
		log.WithField("tool", name).Info("Tool disabled by blacklist")
	}
}

// Tools returns the agent's tool registry.
func (a *Agent) Tools() *tools.Registry {
	return a.tools
}

// RunnerManager returns the agent's runner manager.
func (a *Agent) RunnerManager() *runner.Manager {
	return a.runnerManager
}

// ToolProviders returns the ordered tool providers.
func (a *Agent) ToolProviders() []tools.ToolProvider {
	return a.toolProviders
}

// ResolveTool looks up a tool by name across all tool providers in priority order.
// Returns nil, false if no provider has the tool.
func (a *Agent) ResolveTool(sessionKey string, tenantID int64, name string) (tools.Tool, bool) {
	for _, p := range a.toolProviders {
		if t, ok := p.GetTool(sessionKey, tenantID, name); ok {
			return t, true
		}
	}
	return nil, false
}

// getActiveTurnID returns the TurnID of the currently-processing turn for the
// given session key, or 0 if no turn is active (e.g. SubAgent, test).
func (a *Agent) getActiveTurnID(sessionKey string) uint64 {
	if state, ok := a.bgSessionStates.Load(sessionKey); ok {
		return state.(*bgSessionState).activeTurnID.Load()
	}
	return 0
}

// getActiveIteration returns the iteration number of the currently-processing
// iteration for the given session key, or 0 if no turn is active. Set by
// runState.beginIteration via cfg.OnIterationChange; read by stream callbacks
// to stamp iteration on stream_content events.
func (a *Agent) getActiveIteration(sessionKey string) int {
	if state, ok := a.bgSessionStates.Load(sessionKey); ok {
		return int(state.(*bgSessionState).activeIteration.Load())
	}
	return 0
}

// emitTurnStarted announces a new agent turn via the unified progress stream.
// This replaces the old InjectUserMessage side-channel: the notification user
// message is now delivered atomically with the TurnID through the same channel
// as all other progress events, eliminating cross-goroutine arrival-order races.
//
// trigger: "user" (user-typed), "notification" (bg task/cron), "resume".
// content: the user message text (non-empty only for notification/resume).
func (a *Agent) emitTurnStarted(msg bus.InboundMessage, turnID uint64) {
	progressKey := qualifyChatID(msg.Channel, msg.ChatID)

	// Clear iteration history from the previous turn. iterationHistories is
	// per-session (not per-turn) — without clearing, GetActiveProgress returns
	// old turn's iterations mixed with the new turn's, causing the frontend
	// to render duplicate iterations across turns (e.g. turn 27's iter 1-2
	// appearing inside turn 28's assistant message).
	// Skip for resume (InjectInboundResume) — it continues the same turn.
	//
	// Concurrency safety: emitTurnStarted is called from chatProcessLoop
	// (line 2901) BEFORE processMessage (line 2951). The previous turn's
	// Run() has already returned (chatProcessLoop is serial — it waits for
	// processMessage to complete before dequeuing the next message). No
	// concurrent snapshotCompletedIteration or attachIterationDelta can be
	// writing to iterationHistories at this point. GetActiveProgress (frontend
	// request) reads iterationHistories via sync.Map Load — it may observe
	// the Delete (empty result) but never a torn state (sync.Map operations
	// are atomic). The turn_started SSE event is emitted AFTER this Delete,
	// so the frontend's reload (triggered by turn_started) sees the clean
	// state.
	if msg.Metadata == nil || msg.Metadata["resume_turn"] != "true" {
		a.iterationHistories.Delete(progressKey)
	}

	trigger := "user"
	content := ""
	if msg.Metadata != nil {
		if msg.Metadata[bgNotificationMetadataKey] == "true" {
			trigger = "notification"
			content = msg.Content
		} else if msg.Metadata["resume_turn"] == "true" {
			trigger = "resume"
		} else if msg.Metadata["ask_user_answered"] == "true" {
			// AskUser answer is a NEW turn (new turnID allocated by chatProcessLoop).
			// trigger="user" so the frontend does commitLiveProgressAndReset
			// (commits old turn's live content, resets store). Using "resume"
			// was wrong — it preserved old iterationHistory (resetStreamingState)
			// and lost the old turn's live content.
			trigger = "user"
		}
	}

	seqPtr, _ := a.builtinProgressSeq.LoadOrStore(progressKey, &atomic.Uint64{})
	seq := seqPtr.(*atomic.Uint64).Add(1)

	payload := &protocol.ProgressEvent{
		ChatID: progressKey,
		Phase:  "turn_started",
		Seq:    seq,
		TurnID: turnID,
		TurnStart: &protocol.TurnStartInfo{
			Trigger:    trigger,
			Content:    content,
			RequestID:  msg.RequestID,
			SenderName: msg.SenderName,
		},
	}

	if a.channelRange != nil {
		a.channelRange(func(_ string, ch channel.Channel) bool {
			if sender, ok := ch.(channel.ProgressSender); ok {
				sender.SendProgress(msg.ChatID, cloneProgressEvent(payload))
			}
			return true
		})
	}

	// Store snapshot for mid-session reconnect.
	a.lastProgressSnapshot.Store(progressKey, payload)
}

// emitBuiltinProgress sends a progress event for builtin commands (/compress, /new)
// that bypass engine.Run. It follows the same channel-agnostic fan-out and
// snapshot contract as buildProgressEventHandler.
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
		TurnID:    a.getActiveTurnID(progressKey),
		Iteration: 0,
	}

	// Builtin commands use the same channel-agnostic fan-out contract as
	// engine progress. Channels are transports only.
	if a.channelRange != nil {
		a.channelRange(func(_ string, ch channel.Channel) bool {
			if sender, ok := ch.(channel.ProgressSender); ok {
				sender.SendProgress(chatID, cloneProgressEvent(payload))
			}
			return true
		})
	}

	// Store snapshot for mid-session reconnect
	a.lastProgressSnapshot.Store(progressKey, progressSnapshotWithoutHistory(payload))
	a.clearStreamState(progressKey)
}

// emitGoalProgress pushes a lightweight progress event carrying the current goal
// state. Called after /goal command or set_goal RPC so the frontend can display
// the GoalBanner immediately — before the first Run iteration's
// refreshStructuredTodos injects goal into normal progress events.
func (a *Agent) emitGoalProgress(chName, chatID string) {
	if a.goalManager == nil {
		return
	}
	progressKey := qualifyChatID(chName, chatID)
	goal := a.goalManager.GoalInfo(progressKey)
	if goal == nil {
		return
	}
	seqPtr, _ := a.builtinProgressSeq.LoadOrStore(progressKey, &atomic.Uint64{})
	seq := seqPtr.(*atomic.Uint64).Add(1)
	payload := &protocol.ProgressEvent{
		ChatID:    progressKey,
		Phase:     "",
		Seq:       seq,
		TurnID:    a.getActiveTurnID(progressKey),
		Iteration: 0,
		Todos:     a.GetTodos(chName, chatID),
		Goal:      goal,
	}
	if a.channelRange != nil {
		a.channelRange(func(_ string, ch channel.Channel) bool {
			if sender, ok := ch.(channel.ProgressSender); ok {
				sender.SendProgress(chatID, cloneProgressEvent(payload))
			}
			return true
		})
	}
	// Update snapshot so GetActiveProgress also returns the goal.
	a.lastProgressSnapshot.Store(progressKey, progressSnapshotWithoutHistory(payload))
}

// emitBuiltinProgressDone sends a PhaseDone progress event and cleans up the snapshot.
// Must be called in a defer after emitBuiltinProgress to ensure the CLI ends the turn.
// tokenUsage is optional — when provided, it updates the CLI's context indicator bar.
// historyCompacted signals the CLI to rebuild messages from session storage after
// compression or session reset (same as the auto-compress path).
func (a *Agent) emitBuiltinProgressDone(chName, chatID string, tokenUsage *protocol.TokenUsage, historyCompacted bool) {
	progressKey := qualifyChatID(chName, chatID)

	seqPtr, ok := a.builtinProgressSeq.Load(progressKey)
	if !ok {
		return
	}
	seq := seqPtr.(*atomic.Uint64).Add(1)

	payload := &protocol.ProgressEvent{
		ChatID:           progressKey,
		Phase:            string(PhaseDone),
		Seq:              seq,
		TurnID:           a.getActiveTurnID(progressKey),
		TokenUsage:       tokenUsage,
		HistoryCompacted: historyCompacted,
	}

	if a.channelRange != nil {
		a.channelRange(func(_ string, ch channel.Channel) bool {
			if sender, ok := ch.(channel.ProgressSender); ok {
				sender.SendProgress(chatID, payload)
			}
			return true
		})
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

	// Stamp the TurnID so the frontend can associate this reply with the correct
	// user message. Prefer the caller-supplied turn_id (authoritative, parsed
	// from RunConfig.TurnID in handleRunOutput); fall back to getActiveTurnID
	// for callers that don't pass one (e.g. tool-initiated sends mid-turn).
	if tidStr := msg.Metadata["turn_id"]; tidStr != "" {
		if tid, err := strconv.ParseUint(tidStr, 10, 64); err == nil && tid > 0 {
			msg.TurnID = tid
		}
	}
	if msg.TurnID == 0 {
		msg.TurnID = a.getActiveTurnID(qualifyChatID(chName, chatID))
	}

	isFinal := strings.HasPrefix(content, "__FEISHU_CARD__:")

	if a.directSend != nil {
		// Skip patch for messages that should not overwrite the streaming
		// message (e.g. cancel confirmations). These are sent as new messages.
		if msg.Metadata["no_patch"] != "true" {
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
		TurnID:   msg.TurnID,
	}:
		return nil
	default:
		return fmt.Errorf("message bus outbound channel is full")
	}
}

// injectInbound 向入站队列注入消息，触发 Agent 完整处理循环。
// 用于 cron 调度和后台任务通知等内部系统消息。
func (a *Agent) injectInbound(channel, chatID, senderID, content string) {
	a.injectInboundWithMetadata(channel, chatID, senderID, content, nil)
}

// InjectInboundResume triggers a resume turn for a session interrupted by
// graceful shutdown or /continue command. It injects an EMPTY message with
// resume_turn metadata — the original user message is already in the DB.
// processMessage detects resume_turn and passes empty UserMessage to
// MessageContext, so Assemble skips appending a user message entirely.
// No duplicate, no workaround.
func (a *Agent) InjectInboundResume(channel, chatID, senderID string) {
	a.injectInboundWithMetadata(channel, chatID, senderID, "", map[string]string{
		"resume_turn": "true",
	})
}

func (a *Agent) injectInboundWithMetadata(channel, chatID, senderID, content string, metadata map[string]string) {
	msg := bus.InboundMessage{
		Channel:   channel,
		SenderID:  senderID,
		ChatID:    chatID,
		Content:   content,
		Metadata:  metadata,
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
// Event Router 通过此函数将外部事件（webhook 等）路由到 agent loop。
// 同时通过 injectCLIUserMessage 通知 TUI 显示。
func (a *Agent) injectEventMessage(msg event.Message) {
	// Route through unified async message pipeline
	a.injectAsyncMessage(msg.Channel, msg.ChatID, msg.SenderID, msg.Content, tools.AsyncSourceEvent)
}

// bgNotifyLoop routes background notifications from BgTaskManager.NotifyCh.
// ALL notifications are buffered into bgRunPending first.
//
// If the target session has an active chatWorker (registered in bgSessionStates),
// its notifyCh is signaled — the chatWorker or chatProcessLoop drains notifications
// at a safe point (after the turn's reply is sent, or when idle). This deferred
// processing eliminates the race between injectCLIUserMessage and the agent's
// reply on asyncCh.
//
// If the target session has NO active chatWorker (e.g. after service restart, before
// the first user message creates a chatWorker), notifications are processed directly.
// This is safe because no Run() is active for the session — there is no concurrent
// reply on asyncCh to race with. Without this fallback, cron triggers and other bg
// notifications would silently accumulate in bgRunPending until the first user message.
func (a *Agent) bgNotifyLoop() {
	for {
		// Resolve the manager per iteration: SetBgTaskManager may replace it at
		// runtime (tests). A bare &Agent{} (no New() Store) yields nil here —
		// poll lightly instead of panicking on nil.NotifyCh (same nil contract
		// as the interactive.go read sites).
		mgr := a.bgTaskMgr.Load()
		if mgr == nil {
			select {
			case <-a.lifecycleStopCh:
				return
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		select {
		case <-a.lifecycleStopCh:
			return
		case notif, ok := <-mgr.NotifyCh:
			if !ok {
				return
			}
			// Always buffer first
			a.enqueueBgNotification(notif)

			sessionKey := notif.SessionKey()
			if state, ok := a.bgSessionStates.Load(sessionKey); ok {
				// Active chatWorker exists — signal it to drain at a safe point
				ss := state.(*bgSessionState)
				select {
				case ss.notifyCh <- struct{}{}:
				default:
					// Already signaled — notification will be drained with others
				}
			} else {
				// No active chatWorker (e.g. after restart). No Run() is in progress
				// for this session, so processing directly is race-free.
				a.drainAndProcessNotifications(sessionKey)
			}
		}
	}
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
	// Display is handled by emitTurnStarted in chatProcessLoop — the notification
	// user message is delivered atomically with the TurnID via the unified progress
	// stream, eliminating the cross-goroutine race between InjectUserMessage
	// (caller's goroutine) and the turn's reply (handleOutbound goroutine).
	a.injectInboundWithMetadata(channelName, chatID, senderID, content, map[string]string{
		bgNotificationMetadataKey: "true",
	})
}

// buildBgNotificationRunConfig is no longer needed — idle bg notifications
// go through injectInbound → processMessage → buildMainRunConfig.

// RunSubAgent 实现 tools.SubAgentManager 接口
// 创建一个独立的子 Agent 循环来执行任务，子 Agent 拥有自己的工具集但不能再创建子 Agent

// InjectAsyncMessage is the exported wrapper for injectAsyncMessage.
// Used by RPC handlers (e.g. genui_action) to inject UI action callbacks
// through the bgnotify pipeline.
func (a *Agent) InjectAsyncMessage(channel, chatID, senderID, content, source string) string {
	return a.injectAsyncMessage(channel, chatID, senderID, content, source)
}

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
	// nil-guard: bare &Agent{} construction (no New() Store) — same
	// contract as the interactive.go read sites.
	if mgr := a.bgTaskMgr.Load(); mgr != nil {
		mgr.SendAsyncMessage(&tools.AsyncMessageNotification{
			Key:     sessionKey,
			Sid:     senderID,
			Content: content,
			Source:  source,
		})
	}

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

// addReactionToMessage 对指定消息添加表情回复
func (a *Agent) addReactionToMessage(chName, chatID, messageID, emojiType string) {
	if a.directSend == nil || messageID == "" {
		return
	}
	_, err := a.directSend(channel.OutboundMsg{
		Channel: chName,
		ChatID:  chatID,
		Metadata: map[string]string{
			"add_reaction":        emojiType,
			"reaction_message_id": messageID,
		},
	})
	if err != nil {
		log.WithError(err).Debug("Failed to add reaction")
	}
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
	a.addReactionToMessage(msg.Channel, msg.ChatID, messageID, "DONE")
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
		case <-a.lifecycleStopCh:
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
	a.cleanAllMaskStores(7)
}

// maskStoreFor returns the per-tenant ObservationMaskStore, creating it on
// first use (LoadOrStore makes concurrent first access idempotent). The
// instance binds {maskBaseDir}/{tenantID} once and never switches tenants —
// this is what makes concurrent tenants safe (the old shared-singleton
// SetTenantID switching leaked tenant A's masks into tenant B's directory).
func (a *Agent) maskStoreFor(tenantID int64) *ObservationMaskStore {
	if v, ok := a.maskStores.Load(tenantID); ok {
		return v.(*ObservationMaskStore)
	}
	v, _ := a.maskStores.LoadOrStore(tenantID, newObservationMaskStoreForTenant(a.maskBaseDir, tenantID))
	return v.(*ObservationMaskStore)
}

// cleanAllMaskStores runs stale cleanup for every tenant mask store: already
// created in-memory instances plus every tenant directory on disk (lazily
// instantiated so their files are cleaned too).
func (a *Agent) cleanAllMaskStores(maxAgeDays int) {
	a.maskStores.Range(func(_, v any) bool {
		v.(*ObservationMaskStore).CleanStale(maxAgeDays)
		return true
	})
	if a.maskBaseDir == "" {
		return
	}
	entries, err := os.ReadDir(a.maskBaseDir)
	if err != nil {
		return // not created yet — nothing on disk
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		tid, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil {
			continue // non-tenant directory
		}
		a.maskStoreFor(tid).CleanStale(maxAgeDays)
	}
}

// formatToolProgress generates a human-readable one-line summary of a tool call for progress display.
// It parses the JSON args and extracts the most important parameter(s) based on the tool name.
// Output is concise, max ~80 chars total.

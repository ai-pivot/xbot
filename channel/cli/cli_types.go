package cli

import (
	"fmt"
	ch "xbot/channel"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	// Markdown rendering for assistant messages
	"strings"
	"sync"
	"time"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"

	"xbot/llm"
	"xbot/plugin"
	"xbot/protocol"
)

// ---------------------------------------------------------------------------
// CLI-local background task types (decoupled from tools package)
// ---------------------------------------------------------------------------

// BgTaskStatus represents the status of a background task.
type BgTaskStatus string

const (
	BgTaskRunning BgTaskStatus = "running"
	BgTaskDone    BgTaskStatus = "done"
	BgTaskError   BgTaskStatus = "error"
	BgTaskKilled  BgTaskStatus = "killed"
)

// BgTask represents a background task for CLI display.
// This is the CLI-local equivalent of tools.BackgroundTask, containing only
// the fields needed for task panel rendering.
type BgTask struct {
	ID         string       `json:"id"`
	Command    string       `json:"command"`
	Status     BgTaskStatus `json:"status"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
	Output     string       `json:"output"`
	ExitCode   int          `json:"exit_code"`
	Error      string       `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// CLI-local metadata constants (decoupled from bus package)
// ---------------------------------------------------------------------------

const (
	// MetadataReplyPolicy controls how Agent should behave before final reply.
	MetadataReplyPolicy = "reply_policy"

	ReplyPolicyOptional = "optional"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

// UserTokenUsage represents a user's cumulative token usage.
// Mirror of sqlite.UserTokenUsage — used in CLIChannelConfig.UsageQuery callback
// so that cmd/xbot-cli does not need to import the sqlite package.
type UserTokenUsage struct {
	SenderID          string `json:"sender_id"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	TotalTokens       int64  `json:"total_tokens"`
	CachedTokens      int64  `json:"cached_tokens"`
	ConversationCount int64  `json:"conversation_count"`
	LLMCallCount      int64  `json:"llm_call_count"`
}

const (
	cliMsgBufSize = 100
)

// syncWriter wraps an *os.File with DEC Synchronized Output (mode 2026).
// Terminals that support this (GNOME Terminal/VTE 0.68+, iTerm2, foot, etc.)
// will batch all writes between the begin/end markers into a single
// atomic frame, eliminating flicker caused by partial repaints.
// Terminals that don't support mode 2026 simply ignore the sequences.

// maxBubbleWidth returns the content width used for message rendering.
// Full width minus small margins for readability.
func maxBubbleWidth(termWidth int) int {
	w := termWidth - 2
	if w < 30 {
		w = 30
	}
	return w
}

// truncateToWidth truncates s so its display width (accounting for wide CJK
// characters) fits within maxWidth columns.  If truncated, "..." is appended.
// This avoids slicing mid-UTF-8-byte which would corrupt terminal rendering.
func truncateToWidth(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= maxWidth {
		return s
	}
	ellipsis := "..."
	target := maxWidth - ansi.StringWidth(ellipsis)
	if target <= 0 {
		return ellipsis[:maxWidth]
	}
	w := 0
	for i, r := range s {
		rw := ansi.StringWidth(string(r))
		if w+rw > target {
			return s[:i] + ellipsis
		}
		w += rw
	}
	return s
}

// hardWrapRunes wraps a line at maxW columns, breaking at character boundaries.
// ANSI escape sequences are preserved across wrapped segments.
// Multi-line input (\n) is split first; each line is wrapped independently.
// Returns the original line if it fits within maxW.
func hardWrapRunes(line string, maxW int) string {
	if maxW <= 0 {
		return line
	}
	inputLines := strings.Split(line, "\n")
	var wrapped []string
	for _, l := range inputLines {
		wrapped = append(wrapped, hardWrapSingleLine(l, maxW))
	}
	return strings.Join(wrapped, "\n")
}

// hardWrapSingleLine wraps a single line to fit within maxW columns.
// It processes by grapheme clusters to preserve multi-rune emoji sequences
// (ZWJ, variation selectors, skin tone). ANSI escapes are preserved.
func hardWrapSingleLine(line string, maxW int) string {
	if maxW <= 0 {
		return line
	}
	if lipgloss.Width(line) <= maxW {
		return line
	}

	var wrapped []string
	var buf strings.Builder
	w := 0

	remaining := line
	var ansiState string
	for len(remaining) > 0 {
		if remaining[0] == '\x1b' {
			i := 1
			for i < len(remaining) {
				c := remaining[i]
				if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
					i++
					break
				}
				i++
			}
			seq := remaining[:i]
			buf.WriteString(seq)
			remaining = remaining[i:]
			if strings.HasSuffix(seq, "[0m") || strings.HasSuffix(seq, "[m") {
				ansiState = ""
			} else if strings.HasSuffix(seq, "m") {
				ansiState = seq
			}
			continue
		}

		cluster, next, _, _ := uniseg.StepString(remaining, 0)
		cw := ansi.StringWidth(cluster)

		if w+cw > maxW && buf.Len() > 0 {
			wrapped = append(wrapped, buf.String())
			buf.Reset()
			w = 0
			if ansiState != "" {
				buf.WriteString(ansiState)
			}
		}
		buf.WriteString(cluster)
		w += cw
		remaining = next
	}
	if buf.Len() > 0 {
		wrapped = append(wrapped, buf.String())
	}
	return strings.Join(wrapped, "\n")
}

// --- Line segment tokenizer for smart wrapping ---

// isCJKRune returns true for runes that allow line breaks on either side.

// tokenizeLine splits a line into typed segments for smart wrapping.

// Document.Margin=0 prevents misalignment inside lipgloss bubbles.
// WordWrap is set to the available width so glamour can calculate proper
// table column widths and wrap cell content within cells.
// Color styles follow currentTheme for visual consistency.
func newGlamourRenderer(wrapWidth int) *glamour.TermRenderer {
	t := currentTheme
	c := func(s string) *string { return &s }

	style := styles.DarkStyleConfig
	zero := uint(0)
	style.Document.Margin = &zero

	// 文档正文
	if t.GDocumentText != "" {
		style.Document.Color = c(t.GDocumentText)
	}
	// 标题 (H1–H6)
	if t.GHeadingText != "" {
		style.Heading.Color = c(t.GHeadingText)
		style.H1.Color = c(t.GHeadingText)
		style.H2.Color = c(t.GHeadingText)
		style.H3.Color = c(t.GHeadingText)
		style.H4.Color = c(t.GHeadingText)
		style.H5.Color = c(t.GHeadingText)
		style.H6.Color = c(t.GHeadingText)
	}
	// 代码块：首选 GCodeBlock，回退到 BGPanel
	codeBg := t.GCodeBlock
	if codeBg == "" {
		codeBg = t.BGPanel
	}
	if codeBg != "" {
		style.CodeBlock.BackgroundColor = c(codeBg)
		if style.CodeBlock.Chroma != nil {
			style.CodeBlock.Chroma.Background.BackgroundColor = c(codeBg)
		}
	}
	if t.GCodeText != "" {
		style.CodeBlock.Color = c(t.GCodeText)
		if style.CodeBlock.Chroma != nil {
			style.CodeBlock.Chroma.Text.Color = c(t.GCodeText)
		}
	}
	// 链接
	if t.GLinkText != "" {
		style.Link.Color = c(t.GLinkText)
		style.LinkText.Color = c(t.GLinkText)
	}
	// 引用 — 使用主题引导线色
	if t.GBlockQuote != "" {
		style.BlockQuote.Color = c(t.GBlockQuote)
	}

	style.BlockQuote.IndentToken = c("│ ")

	// 列表项
	if t.GListItem != "" {
		style.Item.Color = c(t.GListItem)
	}
	// 水平分隔线
	if t.GHorizontalRule != "" {
		style.HorizontalRule.Color = c(t.GHorizontalRule)
	}
	// 强调/加粗文本使用主题强调色
	if t.Accent != "" {
		style.Emph.Color = c(t.Accent)
		style.Strong.Color = c(t.AccentAlt)
	}

	r, _ := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		// WordWrap tells glamour the available width so it can size tables correctly.
		// Glamour's word-wrap breaks inline code (`build:sim-sdk:x86_64` at hyphen
		// boundaries), so we also do post-processing: hardWrapRunes wraps non-table
		// text at exact column boundaries after glamour renders styles, while table
		// lines are truncated (not wrapped) to preserve structure.
		glamour.WithWordWrap(wrapWidth),
	)
	return r
}

// cliCommands 已知命令列表（用于 Tab 补全，§8）
var cliCommands = []string{
	"/cancel", "/channel", "/chat", "/clear", "/commands", "/compress", "/context", "/exit",
	"/help", "/model", "/models", "/new", "/palette", "/plugin", "/quit", "/rewind", "/search",
	"/sessions", "/settings", "/setup", "/ss", "/su", "/tasks", "/update",
	"/usage", "/user",
}

// --- Unified Unicode icons ---
// 避免 emoji/ASCII/Unicode 混用，统一视觉风格。
const (
	IconCheck        = "✓" // 成功/完成
	IconCross        = "✗" // 错误/失败
	IconDot          = "◉" // 进行中/活跃
	IconArrow        = "→" // 方向/进度
	IconBullet       = "•" // 列表项
	IconWarning      = "⚠" // 警告
	IconInfo         = "ℹ" // 信息
	IconSearch       = "◈" // 搜索
	IconRobot        = "◆" // Agent/SubAgent
	IconRunnerOn     = "◉" // Runner 在线
	IconRunnerWait   = "◎" // Runner 连接中
	IconUser         = "▣" // 用户切换
	IconGear         = "⚙" // 设置
	IconCloudOn      = "☁" // 远程已连接
	IconCloudOff     = "⊘" // 远程已断开
	IconCloudWait    = "◌" // 远程重连中
	IconDiamond      = "◈" // 品牌标记（菱形）
	IconDiamondSolid = "◆" // 焦点/活跃
	IconDiamondEmpty = "◇" // 非焦点
	IconGuideActive  = "┊" // 消息引导线（活跃）
	IconGuideDim     = "┆" // 消息引导线（暗淡）
	IconDotLine      = "┈" // 点线分隔符
)

// §19 长消息折叠阈值
const (
	msgFoldThresholdLines = 20
	msgFoldPreviewLines   = 6
)

// ---------------------------------------------------------------------------
// CLI Progress Payload (for structured progress events)
// ---------------------------------------------------------------------------

// cliIterationSnapshot captures a completed iteration for the progress panel.
type cliIterationSnapshot struct {
	Iteration   int
	Thinking    string
	Reasoning   string // model's reasoning/thinking chain (reasoning_content)
	Tools       []protocol.ToolProgress
	ElapsedWall int64 // wall-clock duration of the iteration (ms)
}

// formatElapsed formats milliseconds into a human-friendly duration string.
func formatElapsed(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	if ms < 60000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	mins := ms / 60000
	secs := (ms % 60000) / 1000
	return fmt.Sprintf("%dm%ds", mins, secs)
}

// ---------------------------------------------------------------------------
// CLI ch.Channel Config
// ---------------------------------------------------------------------------

// CLIChannelConfig CLI 渠道配置
type CLIChannelConfig struct {
	WorkDir                string                                                                                                                                       // 工作目录（用于标题栏显示）
	ChatID                 string                                                                                                                                       // 会话 ID（按工作目录区分）
	RemoteMode             bool                                                                                                                                         // 是否为 remote backend 模式（用于标题栏/轻提示）
	RemoteServerURL        string                                                                                                                                       // remote server URL (for header display, e.g. "ws://host:port")
	DebugMode              bool                                                                                                                                         // --debug: UI capture + key injection via SIGUSR1
	DebugInput             string                                                                                                                                       // --debug-input "1,enter,ctrl+c": auto-inject key sequence after startup
	DebugCaptureMs         int                                                                                                                                          // --debug-capture-ms 200: UI capture interval in ms (default 1000)
	HistoryLoader          func() ([]ch.HistoryMessage, error)                                                                                                          // 会话恢复：加载历史消息
	DynamicHistoryLoader   func(channelName, chatID string) ([]ch.HistoryMessage, error)                                                                                // /su 切换用户后加载目标用户历史
	TokenStateLoader       func() (promptTokens, completionTokens int64)                                                                                                // 会话恢复：从 DB 加载上次 Run 的 token 计数
	AgentSessionDumpFn     func(chatID string) ([]ch.HistoryMessage, error)                                                                                             // agent session 切换时从 Agent 内存加载消息
	AgentSessionLLMStateFn func(chatID string) (modelName string, maxContextTokens, maxOutputTokens int64, compressRatio float64, promptTokens, completionTokens int64) // SubAgent LLM 状态（模型名、上下文限制、token用量）
	GetCurrentValues       func() map[string]string                                                                                                                     // 获取当前配置值（用于 settings panel 初始值）
	ApplySettings          func(values map[string]string, chatID string)                                                                                                // 应用设置变更（写 config.json + 更新运行时状态）
	IsFirstRun             bool                                                                                                                                         // 首次运行标志，TUI 启动时自动打开 setup panel
	ClearMemory            func(targetType string) error                                                                                                                // 清空记忆（danger zone）
	GetMemoryStats         func() map[string]string                                                                                                                     // 获取记忆统计（danger zone）
	SwitchLLM              func(provider, baseURL, apiKey, model string) error                                                                                          // 切换活跃 LLM（config + factory + save）
	RefreshValuesCache     func(subscriptionID string)                                                                                                                  // 刷新 GetCurrentValues 缓存（sub 切换后调用，传入激活的订阅 ID）
	UsageQuery             func(senderID string, days int) (cumulative *UserTokenUsage, daily []ch.DailyTokenUsage, err error)                                          // 查询 token 用量
	AgentCount             func() int                                                                                                                                   // 获取活跃的 interactive agent 数量
	AgentList              func() []AgentPanelEntry                                                                                                                     // 列出活跃 interactive agents（用于 panel 展示）
	AgentInspect           func(roleName, instance string, tailCount int) (string, error)                                                                               // 窥探 interactive agent 的最近活动（tail 风格）
	AgentMessages          func(roleName, instance string) []ch.SessionChatMessage                                                                                      // 获取 interactive agent 的对话消息
	ChatCreateFn           func(channelName, senderID, label string) (string, error)                                                                                    // 创建新 ChatRoom（返回 chatID）
	SessionsDeleteFn       func(channelName, chatID string) error                                                                                                       // 删除 session（本地 JSON + 服务端 DB 级联）
	SessionsListRefresh    func()                                                                                                                                       // 侧边栏刷新：session 创建/删除后立即调用，确保 sidebar 不显示过期数据
	SessionsList           func() []SessionPanelEntry                                                                                                                   // 列出所有 session（main + subagent）
	GetActiveProgressFn    func(channelName, chatID string) *protocol.ProgressEvent                                                                                     // 获取目标 session 的活跃进度（session switch 恢复用）
	GetTodosFn             func(channelName, chatID string) []protocol.TodoItem                                                                                         // 获取目标 session 的服务端 TODO 列表（session switch 覆盖本地缓存用）
	GetTokenStateFn        func(channelName, chatID string) (promptTokens, completionTokens int64)                                                                      // 获取目标 session 的最后 token 状态（session switch 恢复 context bar 用）
	TrimHistoryFn          func(channelName, chatID string, cutoff time.Time) error                                                                                     // rewind 回退时删除 DB 消息（channel+chatID 动态传入，支持多 session）
	ChannelConfigGetFn     func() (map[string]map[string]string, error)                                                                                                 // 获取频道配置（用于 /channel 面板）
	ChannelConfigSetFn     func(channel string, values map[string]string) error                                                                                         // 保存频道配置（用于 /channel 面板）
	CreateWebUserFn        func(username string) (password string, err error)                                                                                           // 创建 Web 用户（admin only，返回自动生成的密码）
	ListWebUsersFn         func() ([]map[string]any, error)                                                                                                             // 列出所有 Web 用户
	DeleteWebUserFn        func(username string) error                                                                                                                  // 删除 Web 用户（admin only）
	IsAdminFn              func() bool                                                                                                                                  // 检查当前用户是否 admin
	PaletteContributor     PaletteContributor                                                                                                                           // supplies external commands for command palette
	SidebarWidthOverride   int                                                                                                                                          // --sidebar-width N (0 = use setting/default)
	NoSidebar              bool                                                                                                                                         // --no-sidebar
	TodoManager            *cliTodoManager                                                                                                                              // per-session todo persistence
	SetCWDFn               func(channelName, chatID, dir string) error                                                                                                  // 会话切换时初始化 CWD
	BindChatFn             func(chatID string) error                                                                                                                    // 订阅 Hub 路由，使服务器推送事件（progress/stream/outbound）到达客户端
	Ephemeral              bool                                                                                                                                         // --ephemeral: no sessions.json, no DB persistence, clean slate for benchmarking
}

type AgentPanelEntry struct {
	Role         string
	Instance     string
	Running      bool
	Background   bool
	Task         string // one-shot subagent task (empty for interactive)
	Preview      string // latest progress/last reply summary for panel display
	ParentChatID string // parent session chatID (for session isolation filtering)
}

// SessionPanelEntry represents a session item in the Sessions panel.
type SessionPanelEntry struct {
	ID          string // chatID or "agent:role/instance"
	Type        string // "main" = main chatroom, "agent" = SubAgent session
	Channel     string // channel name (e.g. "cli", "web") for history loading
	Label       string // display label
	Role        string // agent role (for agent type)
	Instance    string // agent instance (for agent type)
	ParentID    string // parent chatID (for agent type)
	Running     bool   // true = currently active
	Active      bool   // true = currently selected (main session only)
	Busy        bool   // true = session is processing (agent thinking/tool_exec, etc.)
	MessageHint string // preview of last message
}

// ---------------------------------------------------------------------------
// CLI ch.Channel (implements ch.Channel interface)
// ---------------------------------------------------------------------------

// CLIChannel CLI 渠道实现
type CLIChannel struct {
	config  *CLIChannelConfig
	msgChan chan ch.OutboundMsg // 接收 agent 回复的通道
	workDir string              // 工作目录

	// Bubble Tea
	program   *tea.Program
	programMu sync.Mutex // protects program field
	model     *cliModel

	// Lifecycle
	stopCh chan struct{}
	wg     sync.WaitGroup

	// Progress coalescing: prevent WS message floods from blocking the
	// Bubble Tea event loop. SendProgress writes to asyncCh (non-blocking);
	// a single drain goroutine calls program.Send. This ensures the WS readPump
	// never blocks on program.Send, and intermediate progress events are
	// dropped when the event loop is behind (the next event will be fresher).
	// PhaseDone ("done") events bypass this and use program.Send directly,
	// since they must never be dropped.
	//
	// Why a single drain goroutine matters: Bubble Tea's p.msgs is unbuffered.
	// Multiple concurrent senders (readLoop for keys, handleProgressDrain,
	// handleOutbound) all compete for the single receiver (eventLoop). With
	// 3+ senders, key events get ~25% scheduling probability. By consolidating
	// ALL non-critical sends through one channel + one goroutine, we reduce
	// concurrent senders to 2 (readLoop + drain), giving keys ~50% chance.
	progressCh chan *protocol.ProgressEvent
	asyncCh    chan tea.Msg // unified async send channel (buffered)

	// Services (injected by Agent or main)
	settingsSvc        SettingsService    // interface for GetSettings/SetSetting
	configMu           sync.RWMutex       // protects runner LLM fields (llmClient, modelList, llmProvider)
	modelLister        ModelLister        // provides available model names for combo
	PaletteContributor PaletteContributor // supplies external commands (plugins/skills/agents/custom)

	// Multi-subscription management
	subscriptionMgr SubscriptionManager // manages LLM subscriptions
	llmSubscriber   LLMSubscriber       // switches active LLM (propagated to model)

	// Background tasks
	bgTaskKill func(taskID string) error // remote mode: RPC-backed kill

	// Runner LLM access
	llmClient    llm.LLM
	modelList    []string
	llmProvider  string
	bgSessionKey string

	runnerAutoConnect *runnerAutoConnectConfig // auto-connect as runner after TUI init

	// Permission control
	approvalState *protocol.ApprovalState // injected to wire CLIApprovalHandler after program creation

	// Pending injections (set before model exists, applied in Start)
	pendingTrimHistoryFn     func(time.Time) error
	pendingResetTokenStateFn func()
	pendingHistory           []ch.HistoryMessage     // remote mode: cached history before model is ready
	pendingProgress          *protocol.ProgressEvent // remote mode: cached progress before model is ready
	pendingCheckpointState   *protocol.CheckpointState
	pendingSendInboundFn     func(ch.InboundMsg) bool
	// Pending remote bg task callbacks (set before model exists in remote mode)
	pendingBgTaskCountFn     func() int
	pendingBgTaskListFn      func() []*BgTask
	pendingBgTaskKillFn      func(taskID string) error // remote mode: forward to server
	pendingBgTaskCleanupFn   func()                    // remote mode: cleanup completed tasks
	pendingPluginMgrFn       func() *plugin.PluginManager
	pendingWidgetRegistry    *plugin.WidgetRegistry // deferred from SetWidgetRegistry before model ready
	pendingRemotePluginCache *remotePluginCache     // deferred from SetRemotePluginCache before model ready
}

// SettingsService is the interface needed by CLIChannel for settings panel.
type SettingsService interface {
	GetSettings(channelName, senderID string) (map[string]string, error)
	SetSetting(channelName, senderID, key, value string) error
}

// ModelLister provides available model names for the settings combo box.
type ModelLister interface {
	ListModels() []string
	// ListAllModels returns models across all subscriptions (for global tier settings).
	ListAllModels() []string
	// EnsureModelsLoaded triggers a synchronous model list fetch if not yet loaded.
	// After this call returns, ListModels() should return the full model list.
	EnsureModelsLoaded()
}

// SubscriptionManager manages user LLM subscriptions.
type SubscriptionManager interface {
	List(senderID string) ([]ch.Subscription, error)
	GetDefault(senderID string) (*ch.Subscription, error)
	Add(sub *ch.Subscription) error
	Remove(id string) error
	SetDefault(id, chatID string) error
	SetModel(id, model string) error
	Rename(id, name string) error
	Update(id string, sub *ch.Subscription) error
	UpdatePerModelConfig(id, model string, pmc ch.PerModelConfig) error
	// GetSessionSubscription queries the backend for the session→subscription mapping.
	// Returns empty strings if no mapping exists (server restart, first-time session, etc.).
	GetSessionSubscription(senderID, chatID string) (subscriptionID, model string, err error)
}

// LLMSubscriber switches the active LLM for a user (called when subscription changes).
type LLMSubscriber interface {
	SwitchSubscription(senderID string, sub *ch.Subscription, chatID string) error
	SwitchModel(senderID, model, chatID string)
	GetDefaultModel() string
}

// SendTUIControl sends a TUI session control message through asyncCh
// (the single channel ALL BubbleTea-bound messages go through).
// handleSessionControlMsg does zero WS RPCs, so no deadlock with the drain.
func (c *CLIChannel) SendTUIControl(action string, params map[string]string) (map[string]string, error) {
	resultCh := make(chan *cliSessionResult, 1)
	msg := cliSessionControlMsg{
		action: action,
		params: params,
		result: resultCh,
	}
	if v, ok := params["chat_id"]; ok {
		msg.chatID = v
	}

	// Must go through asyncCh — handleAsyncDrain is the ONLY goroutine
	// that calls program.Send. Direct prog.Send competes for p.msgs.
	select {
	case c.asyncCh <- msg:
	default:
		return nil, fmt.Errorf("tui_control: asyncCh full")
	}

	select {
	case result := <-resultCh:
		if !result.ok {
			return nil, fmt.Errorf("%s", result.err)
		}
		return map[string]string{"status": "ok"}, nil
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("tui_control: TUI event loop not responding (10s timeout)")
	}
}

// NewCLIChannel 创建 CLI 渠道

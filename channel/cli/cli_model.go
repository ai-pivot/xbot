package cli

import (
	"context"
	"fmt"
	"strings"
	"time"
	ch "xbot/channel"
	"xbot/internal/textarea"
	log "xbot/logger"
	"xbot/plugin"
	"xbot/protocol"
	"xbot/tools"
	"xbot/version"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
)

var (
	// diamondPulseFrames: pulsing diamond sweep — thinking/loading spinner
	diamondPulseFrames = []string{"◇◇◇◇◇", "◇◆◇◇◇", "◇◆◆◇◇", "◇◆◆◆◇", "◇◇◆◆◇", "◇◇◇◆◇"}
	// waveFrames: rotating crescent moon phases — subagent feel
	waveFrames = []string{"◐", "◓", "◑", "◒", "◐", "◓", "◑", "◒", "◐", "◓", "◑", "◒"}
	// orbitFrames: spinning orbit — processing feel
	orbitFrames = []string{"◌", "◔", "◕", "●", "◕", "◔", "◌", "◔", "◕", "●", "◕", "◔"}
	// splashFrames: loading bar animation — 启动画面进度条
	splashFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	// sidebarSpinnerFrames: braille spinner for sidebar busy sessions
	sidebarSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
)

// errorKeywords — system 消息中的错误检测关键词
var errorKeywords = []string{"error", "failed", "失败", "错误", "exception", "denied", "refused"}

type debugCaptureMsg struct{}

type cliToastItem struct {
	text string
	icon string // "✓" | "✗" | "ℹ" 等
}

// cliToastMsg Toast 通知消息（入队显示，自动消失）
type cliToastMsg struct {
	text string
	icon string // "✓" | "✗" | "ℹ" 等
}

// cliToastClearMsg Toast 通知自动清除消息（弹出队列头部）
type cliToastClearMsg struct{}

type cliModel struct {
	// --- Core UI ---
	viewport viewport.Model // 消息显示区
	textarea textarea.Model // 用户输入区

	// §22 输入历史
	inputHistory    []string    // 已发送输入历史（新 → 旧），仅会话内
	inputHistoryIdx int         // -1 = 不在浏览模式, >=0 = 当前浏览索引
	inputDraft      string      // 进入历史浏览前的输入草稿
	ticker          *animTicker // 进度动画 ticker
	width           int         // 终端宽度
	height          int         // 终端高度
	styles          cliStyles
	locale          *ch.UILocale // i18n: current UI locale

	// §23 Placeholder: stored separately from textarea to avoid CJK rendering bug.
	// Textarea's built-in Placeholder causes a view-mode switch (placeholder→normal)
	// that triggers cellbuf incremental diff issues on Windows Terminal with CJK chars.
	placeholderText string // current placeholder string to display in View

	// --- Message state ---
	messages        []cliMessage          // 消息历史
	renderer        *glamour.TermRenderer // Markdown 渲染器
	streamingMsgIdx int                   // 当前流式消息的索引（-1 表示无流式消息）
	newContentHint  bool                  // 有新内容但用户未在底部（显示 ↓ 提示）
	ready           bool                  // 是否已初始化
	viewportYStart  int                   // viewport top Y in terminal coords (set in layout)

	// --- Scroll tracking ---
	// userScrolledUp tracks the user's INTENT to stay scrolled up, independent
	// of viewport geometry. AtBottom() can return false-positives when content
	// shrinks (maxYOffset decreases, clamping yOffset to maxYOffset). This flag
	// is only set by explicit user scroll-up actions and cleared by explicit
	// scroll-to-bottom actions, making it immune to geometric false-positives.
	userScrolledUp bool

	// --- Agent state ---
	agentTurnID       uint64                       // monotonically increasing turn counter
	typing            bool                         // agent 是否正在回复
	replyProcessed    bool                         // true = reply (or cancel ack) has been fully processed for current turn
	typingStartTime   time.Time                    // 本次处理开始时间
	inputReady        bool                         // 输入就绪状态（agent 回复期间禁止发送）
	sendInboundFn     func(ch.InboundMsg) bool     // forward to server via backend.SendInbound
	tempStatus        string                       // 临时状态提示（自动过期）
	pendingCmds       []tea.Cmd                    // commands queued by helpers (auto-drained in Update)
	shouldQuit        bool                         // Smart quit: quit after current operation completes
	trimHistoryFn     func(cutoff time.Time) error // /rewind: delete DB messages at or after cutoff timestamp
	resetTokenStateFn func()                       // /rewind: clear stale prompt/completion token counts

	// --- Message queue (typing 期间排队的消息) ---
	messageQueue   []queuedMsg // 排队等待发送的消息（绑定 chatID 防止跨 session 误投）
	queueEditing   bool        // true = 正在编辑/查看最后一条排队消息
	queueEditBuf   string      // 编辑中的排队消息内容
	needFlushQueue bool        // true = handleAgentMessage 后需要刷新队列
	flushDelay     int         // ticks to wait before flushing (lets pending injected msgs arrive)

	// --- Background tasks ---
	bgTaskCount     int                       // running background tasks (0 = no indicator)
	bgTaskCountFn   func() int                // callback to get current bg task count (set by channel)
	bgTaskListFn    func() []*BgTask          // callback to list running tasks (remote mode)
	bgTaskKillFn    func(taskID string) error // callback to kill a task (remote mode)
	bgTaskCleanupFn func()                    // callback to cleanup completed tasks (remote mode)

	// --- Interactive agents ---
	agentCount      int                                                            // active interactive agent sessions (0 = no indicator)
	agentCountFn    func() int                                                     // callback to get current agent count (set by channel)
	agentListFn     func() []panelAgentEntry                                       // callback to list active agents for panel
	agentInspectFn  func(roleName, instance string, tailCount int) (string, error) // callback to inspect agent activity
	agentMessagesFn func(roleName, instance string) []ch.SessionChatMessage        // callback to get agent conversation messages
	sessionsListFn  func() []SessionPanelEntry                                     // callback to list all sessions for Sessions panel

	// --- Usage query ---
	usageQueryFn func(senderID string, days int) (cumulative *UserTokenUsage, daily []ch.DailyTokenUsage, err error)

	// --- Plugin management ---
	pluginMgrFn       func() *plugin.PluginManager
	widgetRegistry    *plugin.WidgetRegistry // UI widget registry from plugin system
	pluginReloading   bool                   // true when a reload operation is in progress
	remotePluginCache *remotePluginCache     // plugin data cache for remote mode (nil = local mode)

	// --- Web user management (admin only) ---
	createWebUserFn func(username string) (password string, err error)
	listWebUsersFn  func() ([]map[string]any, error)
	deleteWebUserFn func(username string) error
	isAdminFn       func() bool

	// --- Session ---
	workDir                string    // 工作目录（标题栏显示用）
	remoteMode             bool      // 是否连接 remote backend（标题栏提示用）
	remoteServerURL        string    // remote server host for header display (e.g. "host:port")
	connState              string    // WS connection state: "connected"|"disconnected"|"reconnecting"
	reconnectFrame         int       // spinner frame counter for reconnect overlay animation
	debugMode              bool      // --debug: UI capture + key injection via SIGUSR1
	debugCaptureMs         int       // --debug-capture-ms: UI capture interval in ms (0 = default 1000)
	ephemeral              bool      // --ephemeral: no persistence, clean slate for benchmarking
	senderID               string    // 当前身份 ID（默认 "cli_user"，/su 命令可切换）
	channelName            string    // 当前 channel（默认 "cli"，/su 切换时可能变为 "web"）
	defaultChatID          string    // 默认 chatID（/su 切换回来时恢复）
	chatID                 string    // 会话 ID（按工作目录区分）
	sessionName            string    // 当前会话名称（同目录多 session 支持）
	shiftHintUntil         time.Time // 鼠标无 Shift 操作时显示选中提示的截止时间（zero = 隐藏）
	newContentHintRendered string    // rendered "↓ 新内容" string for zone width measurement
	newContentHintXStart   int       // X position of newContentHint in status bar

	// --- §1 增量渲染 (aggregated into renderCache) ---
	rc renderCache

	// --- §2 工具可视化 ---
	lastContent string // 最后一次迭代的 thinking_content，在 progress 清除前捕获

	// --- §8 Tab 补全 ---
	completions    []string // 当前补全候选项
	compIdx        int      // 当前选中的补全索引
	pluginCmdNames []string // 插件注册的命令名（/xxx 格式），合并到 Tab 补全
	commandNamesFn func() []string

	// --- §8b @ 文件引用补全 ---
	fileCompletions []string // @ 文件路径补全候选项
	fileCompIdx     int      // 当前选中的文件补全索引
	fileCompActive  bool     // true = Tab 循环中，阻止重新 glob

	// --- §9 Rewind (/rewind command) ---
	rewindMode      bool                      // true = rewind overlay active
	rewindItems     []rewindItem              // candidate user messages for rewind selection
	rewindCursor    int                       // selected index in rewindItems
	rewindResult    *protocol.RewindResult    // result of the last rewind operation (for display)
	checkpointState *protocol.CheckpointState // file checkpoint state for rewind file rollback (nil = no file tracking)

	// --- §10 TODO 进度条 ---
	todos            []protocol.TodoItem // 从 progress 事件同步的 TODO 列表
	todosDoneCleared bool                // 全完成后已被用户输入清除，阻止 progress 重填

	// --- §11 Tool Summary 折叠 ---
	toolSummaryExpanded bool // Ctrl+O 切换

	// --- §11c Tool detail expand on click ---
	// expandedTools tracks which tool lines are expanded to show detail bodies.
	// Key format: "msgIdx:toolIdx" — identifies a specific tool within a specific message.
	expandedTools map[string]bool

	// --- §13 Update Check ---

	updateNotice   *version.UpdateInfo // nil=nothing, non-nil=show notice
	checkingUpdate bool                // true while /update is in progress

	// --- §15 Unified LLM panel (Ctrl+N) ---
	// One panel for both subscriptions and models. quickSwitchMode is "" or
	// "llm". quickSwitchRows is the flat displayed list (sections + subs +
	// models + action rows); quickSwitchCursor indexes into it. Filtering is
	// toggled by "/" (quickSwitchFiltering) so command letters (e/d/n/s) don't
	// collide with typing.
	quickSwitchMode        string                      // ""=off, "llm"=unified panel open
	quickSwitchRows        []qsRow                     // flat row list the cursor indexes into
	quickSwitchCursor      int                         // selected row index
	quickSwitchFilterInput textinput.Model             // filter input (focused only in filter mode)
	quickSwitchFiltering   bool                        // "/" filter mode active
	quickSwitchShowAll     bool                        // show noise models (image/realtime/…)
	quickSwitchRefreshing  bool                        // /models refresh in flight
	quickSwitchScrollY     int                         // vertical scroll offset for the panel
	expandedSubs           map[string]bool             // expanded subscription IDs (tree fold state)
	llmCache               *asyncRefreshCache[llmData] // cache-aside: sync DB read + async /models refresh
	subscriptionMgr        SubscriptionManager         // injected by CLIChannel
	llmSubscriber          LLMSubscriber               // injected by CLIChannel
	cachedSubName          string                      // owning subscription display name for status bar

	// --- §23 Command Palette (Ctrl+K) ---
	paletteOpen           bool               // true = command palette overlay is active
	paletteInput          textinput.Model    // filter input
	paletteItems          []paletteCommand   // all available commands
	paletteFiltered       []paletteCommand   // commands after fuzzy filter
	paletteCursor         int                // selected index in filtered list
	paletteScrollY        int                // scroll offset for visible items
	paletteActiveCategory PaletteCategory    // active category tab (empty = all)
	paletteContributor    PaletteContributor // external command provider (plugins/skills/agents)

	// --- §16 ch.Subscription generation guard ---
	// subGeneration increments every time the active subscription actually changes.
	// panelSubGeneration captures the generation when the settings panel opens.
	// ApplySettings REFUSES to write per-subscription LLM fields if generations don't match.
	// This is the structural guarantee against stale LLM values overwriting a new subscription.
	subGeneration int

	// --- §19 长消息折叠 ---
	msgLineOffsets []int // 每条消息在 viewport 折行后 content 中的起始行号

	// --- §Session state save/restore ---
	// Per-session saved state so switching sessions doesn't lose in-progress state.
	// Key = "channelName:chatID". Messages are NOT saved here — DB is source of truth.
	savedSessions    map[string]*sessionState
	pendingUserMsg   *cliMessage       // most recent user message sent but not yet confirmed in DB
	pendingSuRestore *suHistoryLoadMsg // pre-start restore data, consumed by Init()
	turnCancelled    bool              // true after Ctrl+C — prevents auto-start on stale progress
	turnAutoStarted  bool              // true when turn was started by progress auto-start (no user message yet).
	// handleInjectedUserMsg checks this to claim the auto-started turn
	// instead of queuing (which would create a second assistant).
	// turnStartedProcessed tracks whether the backend's turn_started event
	// for the current turn has been received. Set false by startAgentTurn,
	// set true by handleTurnStarted. Distinguishes "first turn_started for
	// this turn (adopt TurnID)" from "turn_started for N+1 before N's reply
	// (finalize N, start N+1)".
	turnStartedProcessed bool
	cancelTargetTurnID   uint64 // turnID being cancelled; guards stale cancel ack from modifying wrong message
	cancelAckProcessed   bool   // true after first cancel ack handled; guards stale second cancel ack (Bug #2: async goroutine race)
	idleTickCounter      int    // counts 100ms ticks in idle state; placeholder rotates every 30

	// --- Turn/Iteration consistency tracking ---
	// lastReceivedTurnID tracks the most recent TurnID received via turn_started.
	// Used to assert TurnID is strictly monotonic per session.
	lastReceivedTurnID uint64

	// --- Mouse support ---
	mouseZones mouseZoneBuilder // zone tracker for mouse hit testing (rebuilt each View())

	easterEggState easterEggState

	pluginOverlay struct {
		active   bool
		id       string
		provider plugin.OverlayProvider
	}

	searchState searchState

	toastState toastState

	splashState splashState

	panelState panelState

	layoutConfig layoutConfig

	progressState progressState

	channel             *CLIChannel     // back-reference to owning channel (set during Start)
	cachedModelName     string          // cached model name for View() performance
	modelNameZoneXStart int             // rendered X start of model name in status bar (-1 = not rendered)
	modelNameZoneXEnd   int             // rendered X end of model name in status bar (exclusive)
	cachedThinkingMode  string          // global thinking_mode user setting ("" = auto), for status-bar indicator
	thinkingZoneXStart  int             // rendered X start of thinking indicator in status bar (-1 = not rendered)
	thinkingZoneXEnd    int             // rendered X end of thinking indicator in status bar (exclusive)
	activeSubID         string          // active subscription ID for current session
	hasNoSubCache       bool            // cached result of hasNoSubscription()
	hasNoSubCacheValid  bool            // true when hasNoSubCache is authoritative
	todoManager         *cliTodoManager // per-session todo persistence
	askUserSession      string          // chatID of the session that triggered current AskUser panel (empty = no pending AskUser)
	modelCount          int             // cached model list length for View() performance

	// Context usage display (persisted across turns for ready-status bar)
	lastTokenUsage         *protocol.TokenUsage // last known token usage from progress events
	cachedMaxContextTokens int                  // max context tokens (from settings/config, cached for View())
	cachedMaxOutputTokens  int64                // max output tokens (from progress events, cached for View())
	cachedCompressRatio    float64              // compression threshold ratio, cached for View()

	// === Runner Bridge ===
	runnerBridge *RunnerBridge
}

// cliMessage 单条消息
// queuedMsg is a user message waiting in the queue, bound to a specific chatID.
// When flushed, it is only delivered to the session that was active when queued.
type queuedMsg struct {
	content string
	chatID  string
}

type cliMessage struct {
	role      string
	content   string
	timestamp time.Time
	isPartial bool
	// --- turn identification for deterministic rendering ---
	turnID uint64 // agentTurnID when this message was created (0 = not agent-generated)
	// isNotification marks user messages injected by bg notifications (not
	// typed by the user). Rendered with a 🔔 badge + muted style so the user
	// can distinguish system-injected messages from their own input.
	isNotification bool
	// --- thinking/reasoning content (displayed in a collapsible box) ---
	reasoning string // raw reasoning text (stored when message is finalized)
	// --- §1 增量渲染 ---
	rendered    string // 缓存的渲染结果（ANSI 字符串）
	dirty       bool   // 是否需要重新渲染
	renderWidth int    // 渲染时的终端宽度（用于 resize 失效检测）

	// --- §2 工具可视化 ---
	tools      []protocol.ToolProgress // 扁平化工具列表（兼容旧逻辑）
	iterations []cliIterationSnapshot  // 按迭代分组的快照（优先使用）

	// --- §19 长消息折叠 ---
	renderedLines         int  // 渲染后的总行数（每次 dirty 重算）
	originalRenderedLines int  // fold 前的原始行数（fold 时保存，用于 unfold 判断）
	folded                bool // 是否折叠

	// --- Wrapped lines cache ---
	wrappedLines    []string // pre-wrapped lines for viewport (avoids O(N) re-parse)
	wrappedMaxWidth int      // max visual width among wrappedLines
	wrappedWidth    int      // chatWidth when wrappedLines was computed

	// --- Markdown rendering for system messages ---
	markdown bool // when true, system messages go through glamour renderer (e.g. /usage tables)
	styled   bool // when true, content is pre-rendered with ANSI codes, output as-is in renderMessage
}

// newCLIModel 创建 CLI model
func newCLIModel() *cliModel {
	ta := textarea.New()
	ta.Placeholder = "" // disabled; placeholder rendered in View() to avoid CJK bug
	ta.Focus()
	ta.SetWidth(72)
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	// Enable DynamicHeight so textarea auto-grows/shrinks based on visual lines
	// (including soft wraps from CJK characters). This replaces our manual autoExpandInput.
	ta.DynamicHeight = true
	ta.MinHeight = minTaHeight
	ta.MaxHeight = maxTaHeight
	ta.SetHeight(minTaHeight)
	initStyles := buildStyles(76)
	applyTAStyles(&ta, &initStyles)
	// Disable textarea's virtual (block) cursor. We use BubbleTea's real
	// terminal cursor instead — this avoids a double-cursor and ensures IME
	// candidate windows anchor to the correct position during streaming output.
	ta.SetVirtualCursor(false)

	// Keep textarea's native newline bindings intact.
	// Plain Enter is intercepted by the outer CLI handler and used for send,
	// while modified/newline-intent keys (for example Ctrl+J depending on
	// terminal encoding) are allowed to reach the textarea so its built-in
	// multiline + internal-scroll behavior continues to work at MaxHeight.

	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))

	// 滚动方式：Up/Down（逐行，也响应鼠标滚轮的转义序列）、PgUp/PgDn（翻页）
	// 注意：Up/Down 会同时被 textarea 的光标移动和 viewport 的滚动处理。
	// handleKeyPress 里对 KeyUp/KeyDown 的 input history 逻辑会优先拦截，
	// 但仅在 idle + 输入框为空时才触发，所以滚轮滚动在 typing 时不冲突。
	vp.KeyMap.Up.SetKeys("up")
	vp.KeyMap.Down.SetKeys("down")
	vp.KeyMap.Left.SetKeys()
	vp.KeyMap.Right.SetKeys()
	vp.KeyMap.PageUp.SetKeys("pgup")
	vp.KeyMap.PageDown.SetKeys("pgdown")
	vp.KeyMap.HalfPageUp.SetKeys()
	vp.KeyMap.HalfPageDown.SetKeys()
	vp.SetHorizontalStep(0) // 禁用水平滚动步长

	renderer := newGlamourRenderer(maxBubbleWidth(80) - 2)

	// Ticker
	tk := newAnimTicker(diamondPulseFrames, currentTheme.Warning)

	m := &cliModel{
		viewport:        vp,
		textarea:        ta,
		ticker:          tk,
		placeholderText: ch.GetLocale(ch.CurrentLocaleLang()).IdlePlaceholders[0],
		messages:        make([]cliMessage, 0, cliMsgBufSize),
		styles:          buildStyles(80),
		renderer:        renderer,
		ready:           false,
		typing:          false,
		streamingMsgIdx: -1,
		inputReady:      true,
		replyProcessed:  true,
		locale:          ch.GetLocale(""),
		inputHistory:    make([]string, 0, 100),
		inputHistoryIdx: -1,
		inputDraft:      "",
		senderID:        "cli_user",
		channelName:     "cli",
		expandedTools:   make(map[string]bool),
		// Layout defaults
		layoutConfig: layoutConfig{
			maxWidth:       76,
			center:         true,
			mode:           "auto",
			sidebarEnabled: true,
			sidebarVisible: true,
			sidebarWidth:   30,
			sidebarPos:     "left",
			collapsedSects: make(map[string]bool),
		},
		progressState: progressState{
			unread:     make(map[string]bool),
			busyStates: make(map[string]bool),
			liveStates: make(map[string]*liveSessionState),
		},
		expandedSubs: make(map[string]bool),
	}

	// llmCache: cache-aside for subscriptions + model entries.
	// loadSync reads from DB (fast, ~ms), satisfying the UI instantly.
	// The /models API refresh runs async and applies results via Apply().
	// Initialized here with a closure that captures the model pointer —
	// subscriptionMgr/channel are injected later but available at Get() time.
	m.llmCache = newAsyncRefreshCache(func() llmData {
		return m.llmSource()
	})

	return m
}

type cliOutboundMsg struct {
	msg ch.OutboundMsg
}

// cliProgressMsg 实时进度更新消息（来自 WS eventStream 或本地 Transport）。
// 经过 seq 去重、turnID 守卫等实时处理。
type cliProgressMsg struct {
	payload *protocol.ProgressEvent
}

// cliProcessingMsg sets the typing/processing state externally (remote reconnect).
type cliProcessingMsg struct {
	processing bool
}

// cliConnStateMsg updates the WS connection state for the header bar indicator.
type cliConnStateMsg struct {
	state string // "connected" | "disconnected" | "reconnecting"
}

// cliSessionStateMsg carries a server-pushed session state change event
// (busy/idle, subagent started/stopped) into the BubbleTea Update loop.
type cliSessionStateMsg struct {
	event protocol.SessionEvent
}

// liveSessionState holds event-driven session state received from the server
// via SessionEvent push. This provides instant sidebar updates (<100ms)
// instead of waiting for the safety-net poll. Keyed by session ID:
//   - main sessions: chatID
//   - agent sessions: "agent:role/instance"
type liveSessionState struct {
	busy     bool
	role     string // non-empty for subagent sessions
	instance string // non-empty for subagent sessions
	parentID string // parent chatID for subagent sessions
}

// cliHistoryLoadMsg loads history messages into the model from a goroutine-safe context.
// Data is pre-converted, so the Update handler only appends and rebuilds viewport.
type cliHistoryLoadMsg struct {
	channelName string
	chatID      string
	history     []cliMessage
}

// cliTickMsg 全局 ticker 定时刷新消息（100ms 间隔，由全局 goroutine 驱动）。
// 不再使用 BubbleTea cmd chain，消除了 chain 累积导致 tick 加倍的问题。
type cliTickMsg struct{}

type cliTempStatusClearMsg struct{}

// cliSettingsSavedMsg settings save completed (async callback result)
type cliSettingsSavedMsg struct {
	themeChanged  bool
	theme         string
	langChanged   bool
	lang          string
	layoutChanged bool
	layoutVals    map[string]string // layout-related settings for field update
	feedbackMsg   string
	savedModel    string // model name from saved values (avoids GetDefault RPC timing issues)
}

type cliSwitchLLMDoneMsg struct {
	err       error
	subID     string
	subName   string
	subModel  string
	maxCtx    int // per-model max_context from subscription (for session persistence)
	maxOutTok int // per-model max_output_tokens from subscription
	mgr       SubscriptionManager
}

// cliInjectedUserMsg 通知 CLI 有 user 消息被注入（如 bg task 完成通知）
type cliInjectedUserMsg struct {
	content string
	chatID  string // session key "channel:chatID", empty for legacy (always apply)
}

// cliUpdateCheckMsg 更新检查结果消息
type cliUpdateCheckMsg struct {
	info *version.UpdateInfo
}

type cliPluginReloadResultMsg struct {
	pluginID string
	err      error
}

type cliPluginReloadAllResultMsg struct {
	err error
}

type cliPluginHealthResultMsg struct {
	results map[string]error
}

type cliPluginInstallResultMsg struct {
	pluginID  string
	pluginDir string
	err       error
}

type cliPluginUninstallResultMsg struct {
	pluginID string
	err      error
}

// cliWidgetUpdateMsg signals that a plugin widget's content has been updated
// and the TUI should re-render to show the new content.
type cliWidgetUpdateMsg struct{}

// isCtrlEnter 检测 Ctrl+Enter 按键。
// 终端对 Ctrl+Enter 没有统一标准，常见 raw sequences：
//   - CSI u 协议: \x1b[13;5u   (kitty, Ghostty, Windows Terminal)
//   - 旧格式:     \x1b[27;5;13~ (部分 xterm 变体)
//
// 注意：Bubble Tea 不识别这些序列，会作为 unknownCSISequenceMsg 传递，
// 其 String() 格式为 "?CSI[49 51 59 53 117]?"（%+v 对 []byte 输出字节值数组）。
// 因此需要同时匹配 KeyMsg 和 unknownCSISequenceMsg 的字符串表示。
func isCtrlEnter(msg tea.Msg) bool {
	s := fmt.Sprintf("%v", msg)
	// CSI u 协议: \x1b[13;5u → "?CSI[49 51 59 53 117]?" 或 KeyRunes "\x1b[13;5u"
	// 旧格式:     \x1b[27;5;13~ → "?CSI[50 55 59 53 59 49 51 126]?" 或 KeyRunes "\x1b[27;5;13~"
	return s == "?CSI[49 51 59 53 117]?" || s == "\x1b[13;5u" ||
		s == "?CSI[50 55 59 53 59 49 51 126]?" || s == "\x1b[27;5;13~"
}

// isCtrlO 检测 Ctrl+O 按键（部分终端发送 CSI u 序列，Bubble Tea 无法识别）。
// Ctrl+O = ASCII 15, CSI u 协议: \x1b[15;5u → "?CSI[49 53 59 53 117]?"
func isCtrlO(msg tea.Msg) bool {
	s := fmt.Sprintf("%v", msg)
	return s == "?CSI[49 53 59 53 117]?" || s == "\x1b[15;5u"
}

// isCtrlJ detects Ctrl+J (newline). Ctrl+J = ASCII 10.
// CSI u protocol: \x1b[10;5u → "?CSI[49 48 59 53 117]?"
func isCtrlJ(msg tea.Msg) bool {
	s := fmt.Sprintf("%v", msg)
	return s == "?CSI[49 48 59 53 117]?" || s == "\x1b[10;5u" || s == "ctrl+j"
}

func (m *cliModel) Init() tea.Cmd {
	cmds := []tea.Cmd{textarea.Blink}
	// If RestoreSession cached data before program start, emit it as
	// a tea.Cmd so handleSuHistoryLoad runs inside the event loop.
	if m.pendingSuRestore != nil {
		msg := *m.pendingSuRestore
		m.pendingSuRestore = nil
		cmds = append(cmds, func() tea.Msg { return msg })
	}
	if m.debugMode {
		cmds = append(cmds, m.debugCaptureTick())
	}
	return tea.Batch(cmds...)
}

// debugCaptureTick returns a tea.Cmd that fires periodically to capture UI state.
func (m *cliModel) debugCaptureTick() tea.Cmd {
	interval := time.Duration(m.debugCaptureMs) * time.Millisecond
	if interval < 50*time.Millisecond {
		interval = 1 * time.Second
	}
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return debugCaptureMsg{}
	})
}

// suLoadHistoryCmd 异步加载 /su 目标用户的历史消息
func (m *cliModel) suLoadHistoryCmd() tea.Cmd {
	chatID := m.chatID
	channelName := m.channelName
	progressFn := m.channel.config.GetActiveProgressFn
	todosFn := m.channel.config.GetTodosFn
	tokenFn := m.channel.config.GetTokenStateFn

	// Agent sessions: load from in-memory interactiveSubAgents (not DB).
	if channelName == "agent" {
		dumpFn := m.channel.config.AgentSessionDumpFn
		llmStateFn := m.channel.config.AgentSessionLLMStateFn
		if dumpFn != nil {
			return func() tea.Msg {
				history, err := dumpFn(chatID)
				var activeProgress *protocol.ProgressEvent
				if progressFn != nil {
					// /su switch: request full history (fromIter=-1) — new session needs
					// ALL iterations including iteration 0.
					activeProgress = progressFn(channelName, chatID, protocol.FetchAll())
				}
				var todos []protocol.TodoItem
				if todosFn != nil {
					todos = todosFn(channelName, chatID)
				}
				// Fetch LLM state from agent for SubAgent session status bar
				var modelName, subID string
				var maxCtx, maxOut, tokPrompt, tokComp int64
				var compRatio float64
				if llmStateFn != nil {
					modelName, subID, maxCtx, maxOut, compRatio, tokPrompt, tokComp = llmStateFn(chatID)
				}
				// Fallback token state from DB if agent didn't provide it
				if tokPrompt == 0 && tokenFn != nil {
					tokPrompt, tokComp = tokenFn(channelName, chatID)
				}
				return suHistoryLoadMsg{
					history: history, err: err, channelName: channelName, chatID: chatID,
					activeProgress: activeProgress, todos: todos,
					tokenPrompt: tokPrompt, tokenCompletion: tokComp,
					modelName: modelName, subscriptionID: subID, maxContextTokens: maxCtx,
					maxOutputTokens: maxOut, compressRatio: compRatio,
				}
			}
		}
	}

	loader := m.channel.config.DynamicHistoryLoader
	if loader == nil {
		return func() tea.Msg {
			return suHistoryLoadMsg{err: fmt.Errorf("no dynamic history loader"), channelName: channelName, chatID: chatID}
		}
	}
	return func() tea.Msg {
		history, err := loader(channelName, chatID)
		// Also fetch active progress for seamless session switch recovery.
		// /su switch: request full history (fromIter=-1) — new session needs all
		// iterations including iteration 0.
		var activeProgress *protocol.ProgressEvent
		if progressFn != nil {
			activeProgress = progressFn(channelName, chatID, protocol.FetchAll())
		}
		// Fetch server-side TODO list to overwrite local cache on first switch.
		var todos []protocol.TodoItem
		if todosFn != nil {
			todos = todosFn(channelName, chatID)
		}
		// Fetch last token state so the context bar shows immediately
		// even when the session is idle (no active turn).
		var tokenPrompt, tokenCompletion int64
		if tokenFn != nil {
			tokenPrompt, tokenCompletion = tokenFn(channelName, chatID)
		}
		return suHistoryLoadMsg{
			history: history, err: err,
			channelName:     channelName,
			chatID:          chatID,
			activeProgress:  activeProgress,
			tokenPrompt:     tokenPrompt,
			tokenCompletion: tokenCompletion,
			todos:           todos,
		}
	}
}

func (m *cliModel) toggleSidebarSection(section string) {
	if m.layoutConfig.collapsedSects == nil {
		m.layoutConfig.collapsedSects = make(map[string]bool)
	}
	if m.layoutConfig.collapsedSects[section] {
		delete(m.layoutConfig.collapsedSects, section)
	} else {
		m.layoutConfig.collapsedSects[section] = true
	}
	m.saveSidebarCollapsedPrefs()
}

// saveSidebarCollapsedPrefs persists the current sidebar section collapse state to preferences.json.
func (m *cliModel) saveSidebarCollapsedPrefs() {
	if m.workDir == "" {
		return
	}
	prefs := tools.LoadPreferences(m.workDir, m.senderID)
	prefs.SidebarCollapsed = m.layoutConfig.collapsedSects
	_ = tools.SavePreferences(m.workDir, m.senderID, prefs)
}

// easterEggState groups 10 fields for easteregg.
type easterEggState struct {
	mode           easterEggMode
	customArt      string
	konamiBuf      []string
	matrixCols     int
	matrixRows     int
	matrixDrops    []int
	matrixSpeeds   []int
	matrixTrailLen []int
	matrixBuf      [][]rune
	versionHits    []time.Time
}

// searchState groups related fields extracted from cliModel.
type searchState struct {
	mode    bool
	query   string
	results []int
	idx     int
	editing bool
	ti      textinput.Model
}

// toastState groups related fields extracted from cliModel.
type toastState struct {
	queue       []cliToastItem
	timerActive bool
}

// splashState groups related fields extracted from cliModel.
type splashState struct {
	done             bool
	frame            int
	suLoading        bool
	suPhaseConfirmed bool
	compReloading    bool
}

// settingsPanelState groups fields for the settings panel mode.
type settingsPanelState struct {
	settingsSaving bool
	editing        bool
	editTA         textarea.Model
	combo          bool
	comboIdx       int
	wizardStep     int
	wizardLangSel  int
	wizardProvSel  int
	wizardKeyTI    textinput.Model
	schema         []ch.SettingDefinition
	schemaFull     []ch.SettingDefinition
	isSetup        bool
	values         map[string]string
	prevProvider   string
	onSubmit       func(values map[string]string)
	subGeneration  int
}

// askUserPanelState groups fields for the ask-user panel mode.
type askUserPanelState struct {
	askItems      []askItem
	askTab        int
	askOptSel     map[int]map[int]bool
	askOptCursor  map[int]int
	askAnswerTA   textarea.Model
	askOtherTI    textinput.Model
	askScrollY    int
	askTotalLines int
	onAnswer      func(answers map[string]string)
	onCancel      func()
}

// runnerPanelState groups fields for the runner panel mode.
type runnerPanelState struct {
	runnerServerTI  textinput.Model
	runnerTokenTI   textinput.Model
	runnerWS        textinput.Model
	runnerEditField int
}

// miscPanelState groups fields for danger/bgtasks/approval/channel/session panels.
type miscPanelState struct {
	approvalReq       *protocol.ApprovalRequest
	approvalCh        chan<- protocol.ApprovalResult
	approvalCursor    int
	approvalDenyTA    textinput.Model
	approvalDenyMode  bool
	bgTasks           []*BgTask
	bgAgents          []panelAgentEntry
	bgCursor          int
	bgViewing         bool
	bgLogLines        []string
	bgLogFollow       bool
	sessItems         []SessionPanelEntry
	sessCursor        int
	sessViewing       bool
	sessConfirmDelete bool
	sessConfirmEntry  SessionPanelEntry
	dangerItems       []dangerItem
	dangerCursor      int
	dangerConfirm     bool
	dangerInput       textinput.Model
	dangerOnExec      func(targetType string) error
	channelItems      []string
	channelCursor     int
	channelCfg        map[string]map[string]string
}

// panelState groups panel-related fields extracted from cliModel, organized by mode.
type panelState struct {
	mode    string
	cursor  int
	scrollY int
	stack   []panelStackEntry

	settings settingsPanelState
	askUser  askUserPanelState
	runner   runnerPanelState
	misc     miscPanelState
}

// layoutConfig groups 13 fields extracted from cliModel.
type layoutConfig struct {
	maxWidth       int
	center         bool
	mode           string
	sidebarEnabled bool
	sidebarWidth   int
	sidebarPos     string
	sidebarVisible bool
	collapsedSects map[string]bool
	xShift         int
	cachedSBWidth  int
	cachedSBKey    int
	cachedChatW    int
	cachedChatKey  int
}

// progressState groups fields extracted from cliModel.
type progressState struct {
	current        *protocol.ProgressEvent
	iterations     []cliIterationSnapshot
	lastIter       int
	lastSeq        uint64
	lastAppliedSeq uint64 // highest Seq applied via applyProgressSnapshot (structured events)
	lastStreamSeq  uint64 // highest Seq from stream-only events (separate counter)
	iterStart      time.Time
	busySessions   bool
	unread         map[string]bool
	busyStates     map[string]bool
	liveStates     map[string]*liveSessionState
	twActive       bool
	twVisible      int
	rwVisible      int
	rwCjkSkip      bool
	twCjkSkip      bool

	// Seq gap detection (Raft-style log consistency).
	// lastReceivedSeq tracks the highest Seq seen from push events.
	// When a push arrives with Seq > lastReceivedSeq+1, there's a gap —
	// events were dropped (sendCh full) or lost (WS disconnect).
	// gapDetected triggers an immediate GetActiveProgress pull on the next
	// tick (100ms) — the only pull trigger in normal operation.
	// Normal flow: Seq increments by 1 each event, no gap, zero pulls.
	lastReceivedSeq uint64
	gapDetected     bool
}

// --- Plugin Overlay ---

// refreshPluginCmdNames lazily populates plugin command names for Tab completion
// from the palette contributor. Called on every slash-input keypress; syncs from
// channel.PaletteContributor if needed and caches results.
func (m *cliModel) refreshPluginCmdNames() {
	// Sync palette contributor from channel if not yet set
	if m.paletteContributor == nil && m.channel != nil && m.channel.PaletteContributor != nil {
		m.paletteContributor = m.channel.PaletteContributor
	}
	if m.paletteContributor == nil {
		return
	}
	// Already populated — only refresh if palette was rebuilt
	if len(m.pluginCmdNames) > 0 {
		return
	}
	for _, ext := range m.paletteContributor() {
		if strings.HasPrefix(ext.Content, "/") {
			name := strings.SplitN(ext.Content, " ", 2)[0]
			found := false
			for _, existing := range m.pluginCmdNames {
				if existing == name {
					found = true
					break
				}
			}
			if !found {
				m.pluginCmdNames = append(m.pluginCmdNames, name)
			}
		}
	}
}

// showPluginOverlay activates a full-screen overlay provided by a plugin.
func (m *cliModel) showPluginOverlay(id string, provider plugin.OverlayProvider) {
	m.pluginOverlay.active = true
	m.pluginOverlay.id = id
	m.pluginOverlay.provider = provider
}

// hidePluginOverlay deactivates the current plugin overlay.
func (m *cliModel) hidePluginOverlay() {
	m.pluginOverlay.active = false
	m.pluginOverlay.provider = nil
}

// --- Plugin Event Bus Messages ---

// cliPluginOverlayShowMsg triggers display of a plugin overlay.
type cliPluginOverlayShowMsg struct {
	pluginID  string
	overlayID string
}

// cliPluginOverlayHideMsg triggers hiding of the current plugin overlay.
type cliPluginOverlayHideMsg struct {
	pluginID string
}

// cliPluginNotifyMsg carries a notification from a plugin to be shown as a toast.
type cliPluginNotifyMsg struct {
	pluginID string
	level    string
	title    string
	message  string
}

// cliPluginSoundMsg carries a sound playback request from a plugin.
type cliPluginSoundMsg struct {
	pluginID string
	sound    string
}

// wirePluginEventBus subscribes to plugin event bus topics and routes events
// into the Bubble Tea event loop via program.Send(). This is called once
// during CLI channel startup after the model and program are both available.
func (m *cliModel) wirePluginEventBus(program *tea.Program) {
	if m.pluginMgrFn == nil {
		return
	}
	mgr := m.pluginMgrFn()
	bus := mgr.Bus()
	if bus == nil {
		return
	}

	// Helper that logs subscription failures instead of silently dropping them.
	sub := func(topic string, handler plugin.PluginEventHandler) {
		if err := bus.Subscribe(topic, handler); err != nil {
			log.WithError(err).WithField("topic", topic).Warn("plugin event subscription failed")
		}
	}

	// plugin:overlay:show — display a plugin's full-screen overlay
	sub("plugin:overlay:show", func(ctx context.Context, topic string, data any) error {
		d, ok := data.(map[string]string)
		if !ok {
			return nil
		}
		program.Send(cliPluginOverlayShowMsg{
			pluginID:  d["plugin_id"],
			overlayID: d["overlay_id"],
		})
		return nil
	})

	// plugin:overlay:hide — dismiss the current plugin overlay
	sub("plugin:overlay:hide", func(ctx context.Context, topic string, data any) error {
		d, ok := data.(map[string]string)
		if !ok {
			return nil
		}
		program.Send(cliPluginOverlayHideMsg{
			pluginID: d["plugin_id"],
		})
		return nil
	})

	// plugin:notify — show a plugin notification as a toast
	sub("plugin:notify", func(ctx context.Context, topic string, data any) error {
		d, ok := data.(map[string]string)
		if !ok {
			return nil
		}
		program.Send(cliPluginNotifyMsg{
			pluginID: d["plugin_id"],
			level:    d["level"],
			title:    d["title"],
			message:  d["message"],
		})
		return nil
	})

	// plugin:sound:play — play a sound effect
	sub("plugin:sound:play", func(ctx context.Context, topic string, data any) error {
		d, ok := data.(map[string]string)
		if !ok {
			return nil
		}
		program.Send(cliPluginSoundMsg{
			pluginID: d["plugin_id"],
			sound:    d["sound"],
		})
		return nil
	})
}

package channel

import (
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"fmt"
	"github.com/charmbracelet/glamour"
	"time"
	"xbot/agent/hooks"
	"xbot/bus"
	"xbot/clipanic"
	"xbot/internal/textarea"
	"xbot/storage/sqlite"
	"xbot/tools"
	"xbot/version"
)

func newAnimTicker(frames []string, color string) *animTicker {
	altColor := currentTheme.AccentAlt
	return &animTicker{
		frames:   frames,
		style:    lipgloss.NewStyle().Foreground(lipgloss.Color(color)),
		styleAlt: lipgloss.NewStyle().Foreground(lipgloss.Color(altColor)),
		color:    color,
		colorAlt: altColor,
	}
}

func (t *animTicker) tick() {
	t.ticks++
	// Advance frame only every `speed` ticks (speed=1 → every tick, speed=3 → every 3rd)
	if t.speed <= 1 || t.ticks%int64(t.speed) == 0 {
		t.frame = (t.frame + 1) % len(t.frames)
	}
}

// view Render current frame with dual-color breathing effect (switches between two colors every 10 ticks)
func (t *animTicker) view() string {
	if t.ticks%20 < 10 {
		return t.style.Render(t.frames[t.frame])
	}
	return t.styleAlt.Render(t.frames[t.frame])
}

// viewFrames renders a frame from a given set using the ticker's current frame index.
// speedOverride controls per-call animation speed (0 = use ticker's default speed).
// Also with breathing effect.
func (t *animTicker) viewFrames(frames []string, speedOverride ...int) string {
	speed := t.speed
	if len(speedOverride) > 0 && speedOverride[0] > 0 {
		speed = speedOverride[0]
	}
	// Calculate effective frame based on speed
	effectiveFrame := t.frame
	if speed > 1 {
		// Use a separate counter for this frame set, keyed by speed
		effectiveFrame = int(t.ticks/int64(speed)) % len(frames)
	}
	idx := effectiveFrame % len(frames)
	if t.ticks%20 < 10 {
		return t.style.Render(frames[idx])
	}
	return t.styleAlt.Render(frames[idx])
}

// isCJK reports whether r is likely a wide (double-width) character for the
// typewriter speed penalty heuristic. Uses a conservative lower bound (0x2E80)
// that covers CJK ideographs, Kana, Hangul, and other East Asian scripts.
// This intentionally over-matches (emoji, etc.) to avoid rendering artifacts.
// For precise CJK word-boundary detection, see internal/textarea.isCJK.
func isCJK(r rune) bool {
	return r >= 0x2E80
}

// advanceTypewriter advances both typewriters (stream + reasoning) on each tick.
// Called every typewriterTickMsg (50ms) during streaming.
func (m *cliModel) advanceTypewriter() {
	if m.progress == nil {
		m.twVisible = 0
		m.rwVisible = 0
		return
	}

	// Advance reasoning writer
	if m.progress.ReasoningStreamContent != "" {
		target := len([]rune(m.progress.ReasoningStreamContent))
		m.advanceWriterCJK(&m.rwVisible, target, m.progress.ReasoningStreamContent, &m.rwCjkSkipTick)
	}

	// Advance stream writer
	if m.progress.StreamContent != "" {
		target := len([]rune(m.progress.StreamContent))
		m.advanceWriterCJK(&m.twVisible, target, m.progress.StreamContent, &m.twCjkSkipTick)
	}
}

// typewriterBehind reports whether there is still unread stream or reasoning
// content waiting to be revealed by the typewriter animation.
func (m *cliModel) typewriterBehind() bool {
	if m.progress == nil {
		return false
	}
	if m.progress.StreamContent != "" && m.twVisible < len([]rune(m.progress.StreamContent)) {
		return true
	}
	if m.progress.ReasoningStreamContent != "" && m.rwVisible < len([]rune(m.progress.ReasoningStreamContent)) {
		return true
	}
	return false
}

// contentWidth returns the available content width for message bubbles,
// accounting for border and padding overhead.
func (m *cliModel) contentWidth() int {
	return m.width - bubblePadding
}

// advanceWriterCJK advances the typewriter cursor with CJK-aware speed control: when
// is CJK, it only advances every other tick (effectively half speed).
// skipFlip tracks alternating ticks within a single call chain.
func (m *cliModel) advanceWriterCJK(visible *int, target int, content string, skipFlip *bool) {
	if target == 0 {
		*visible = 0
		return
	}
	gap := target - *visible
	if gap <= 0 {
		return
	}

	// Check if the next rune to reveal is CJK
	runes := []rune(content)
	nextIsCJK := *visible < len(runes) && isCJK(runes[*visible])

	// Gap-based acceleration — smooth catch-up without visible jumps.
	// Max advance per 50ms tick is capped to avoid teleporting when
	// network coalesces multiple stream updates into one big gap.
	advance := 1
	switch {
	case gap > 80:
		advance = 20
	case gap > 40:
		advance = 10
	case gap > 20:
		advance = 3
	}

	// CJK penalty: if next rune is CJK and we're at normal speed, skip every other tick
	if nextIsCJK && advance <= twSpeedSlow && gap <= twGapSlow {
		*skipFlip = !*skipFlip
		if *skipFlip {
			return // skip this tick, revealing nothing
		}
	}

	*visible += advance
	if *visible > target {
		*visible = target
	}
}

// Ticker frame presets
var (
	// dotFrames: braille dot orbit — 8 frames for a smooth clockwise loop
	dotFrames = []string{
		"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",
	}
	// waveFrames: rotating crescent moon phases — subagent feel
	waveFrames = []string{"◐", "◓", "◑", "◒", "◐", "◓", "◑", "◒", "◐", "◓", "◑", "◒"}
	// orbitFrames: spinning orbit — processing feel
	orbitFrames = []string{"◌", "◔", "◕", "●", "◕", "◔", "◌", "◔", "◕", "●", "◕", "◔"}
	// splashFrames: loading bar animation — splash screen progress bar
	splashFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	// pulseFrames: pulsing circle — tool completion pulse
	pulseFrames = []string{"◌", "◎", "◉", "◎", "◌"}
)

// errorKeywords — error detection keywords in system messages
var errorKeywords = []string{"error", "failed", "失败", "Error", "exception", "denied", "refused"}

// Terminal CSI escape sequences for modified keys not recognized by Bubble Tea.
// Some terminals use the CSI u protocol (kitty, Ghostty, Windows Terminal),
// others use the legacy format. These constants are matched against fmt.Sprint(msg)
// since Bubble Tea delivers them as unknown sequences with varying internal types.
const (
	// Ctrl+Enter: CSI u = \x1b[13;5u, legacy = \x1b[27;5;13~
	csiCtrlEnterCSIu      = "?CSI[49 51 59 53 117]?"
	csiCtrlEnterRaw       = "\x1b[13;5u"
	csiCtrlEnterLegacy    = "?CSI[50 55 59 53 59 49 51 126]?"
	csiCtrlEnterLegacyRaw = "\x1b[27;5;13~"
	// Ctrl+O: CSI u = \x1b[15;5u
	csiCtrlOCsiu = "?CSI[49 53 59 53 117]?"
	csiCtrlORaw  = "\x1b[15;5u"
	// Ctrl+J: CSI u = \x1b[10;5u
	csiCtrlJCsiu = "?CSI[49 48 59 53 117]?"
	csiCtrlJRaw  = "\x1b[10;5u"
	csiCtrlJKey  = "ctrl+j"
)

// pickVerb returns a deterministic verb based on tick count (changes every ~2s at 10 FPS).
func (m *cliModel) pickVerb(ticks int64) string {
	verbs := m.locale.ThinkingVerbs
	if len(verbs) == 0 {
		return "Thinking"
	}
	idx := (ticks / 20) % int64(len(verbs))
	return verbs[idx]
}

// pickIdlePlaceholder Return rotating placeholder based on time (switches every 5 seconds)
func (m *cliModel) pickIdlePlaceholder() string {
	placeholders := m.locale.IdlePlaceholders
	if len(placeholders) == 0 {
		return ""
	}
	idx := int(time.Now().Unix()/5) % len(placeholders)
	return placeholders[idx]
}

// updatePlaceholder refreshes the placeholder text based on typing state.
// We store it in m.placeholderText instead of m.textarea.Placeholder to avoid
// CJK rendering bugs caused by textarea's internal placeholder↔normal view switch.
func (m *cliModel) updatePlaceholder() {
	if m.typing {
		m.placeholderText = m.locale.ProcessingPlaceholder
	} else {
		m.placeholderText = m.pickIdlePlaceholder()
	}
}

// cycleModel switches to the next model across all subscriptions.
// Uses ListAllModels() so models from ALL subscriptions are visible (not just the
// current default LLM). Cycles through the model names displayed in the status bar.
// Note: this only changes the cached model name — the actual subscription switch
// happens when a new LLM call is made (or via quick switch panel).
func (m *cliModel) cycleModel() {
	if m.channel == nil {
		return
	}

	// Use ListModels (current subscription only) instead of ListAllModels.
	// Ctrl+N should cycle through the current subscription's models only.
	models := m.channel.modelLister.ListModels()
	if len(models) < 2 {
		m.showTempStatus("Only one model available")
		return
	}

	current := m.cachedModelName
	nextIdx := 0
	for i, name := range models {
		if name == current {
			nextIdx = (i + 1) % len(models)
			break
		}
	}
	nextModel := models[nextIdx]

	m.cachedModelName = nextModel
	m.showTempStatus(fmt.Sprintf("Model: %s", nextModel))

	// Switch model on the current subscription (no need to change subscription
	// since we're already cycling within the current subscription's models).
	if m.llmSubscriber != nil {
		m.llmSubscriber.SwitchModel(m.senderID, nextModel)
	}
	m.updateQuickSwitchModels(nextModel)
}

// tickerTickMsg is the ticker periodic tick message
type tickerTickMsg struct{}

// splashTickMsg Splash screen periodic tick message
type splashTickMsg struct {
	frame int // Current frame index
}

// debugCaptureMsg triggers a UI capture (dump View() to file).
type debugCaptureMsg struct{}

// splashDoneMsg Splash screen end message
type splashDoneMsg struct{}

// suHistoryLoadMsg History load complete message after /su user switch
type suHistoryLoadMsg struct {
	history        []HistoryMessage
	err            error
	channelName    string              // target session at time of request
	chatID         string              // target session at time of request
	activeProgress *CLIProgressPayload // non-nil if target session has an active agent turn
}

// sessionState holds per-session state that should be preserved when switching sessions.
// Messages are NOT stored here — the DB is the source of truth for history.
type sessionState struct {
	progress          *CLIProgressPayload
	typing            bool
	iterationHistory  []cliIterationSnapshot
	lastSeenIteration int
	streamingMsgIdx   int
	typingStartTime   time.Time
	lastReasoning     string
	lastThinking      string
	turnCancelled     bool // true after explicit Ctrl+C cancel — prevents auto-start
}

// sessionKey returns the map key for the current session.
func (m *cliModel) sessionKey() string {
	return m.channelName + ":" + m.chatID
}

// saveCurrentSession saves the current session's live state into the savedSessions map.
func (m *cliModel) saveCurrentSession() {
	key := m.sessionKey()
	if m.savedSessions == nil {
		m.savedSessions = make(map[string]*sessionState)
	}
	m.savedSessions[key] = m.captureSessionState()
}

// restoreSession restores a session's live state from the savedSessions map.
// If the session has saved state, restores it; otherwise resets to idle.
func (m *cliModel) restoreSession() {
	key := m.sessionKey()
	if saved, ok := m.savedSessions[key]; ok {
		m.applySessionState(saved)
		delete(m.savedSessions, key) // clean up
	} else {
		m.resetSessionState()
	}
}

// captureSessionState captures the current streaming/typing state into a snapshot.
func (m *cliModel) captureSessionState() *sessionState {
	return &sessionState{
		progress:          m.progress,
		typing:            m.typing,
		iterationHistory:  m.iterationHistory,
		lastSeenIteration: m.lastSeenIteration,
		streamingMsgIdx:   m.streamingMsgIdx,
		typingStartTime:   m.typingStartTime,
		lastReasoning:     m.lastReasoning,
		lastThinking:      m.lastThinking,
		turnCancelled:     m.turnCancelled,
	}
}

// applySessionState restores streaming/typing state from a snapshot.
func (m *cliModel) applySessionState(s *sessionState) {
	m.progress = s.progress
	m.typing = s.typing
	m.iterationHistory = s.iterationHistory
	m.lastSeenIteration = s.lastSeenIteration
	m.streamingMsgIdx = s.streamingMsgIdx
	m.typingStartTime = s.typingStartTime
	m.lastReasoning = s.lastReasoning
	m.lastThinking = s.lastThinking
	m.turnCancelled = s.turnCancelled
}

// resetSessionState resets streaming/typing state to idle (not cancelled).
func (m *cliModel) resetSessionState() {
	m.progress = nil
	m.typing = false
	m.streamingMsgIdx = -1
	m.iterationHistory = nil
	m.lastSeenIteration = 0
	m.typingStartTime = time.Time{}
	m.lastReasoning = ""
	m.lastThinking = ""
	m.turnCancelled = false
}

// cliHistoryReloadMsg History reload complete message after context compression
type cliHistoryReloadMsg struct {
	history []HistoryMessage
	err     error
}

// cliToastItem Single toast notification data
type cliToastItem struct {
	text string
	icon string // "✓" | "✗" | "ℹ" 等
}

// cliToastMsg Toast notification message (queued for display, auto-dismiss)
type cliToastMsg struct {
	text string
	icon string // "✓" | "✗" | "ℹ" 等
}

// cliToastClearMsg Toast notification auto-clear message (dequeue from head)
type cliToastClearMsg struct{}

// cliModel Bubble Tea state model
type cliModel struct {
	// --- Core UI ---
	viewport viewport.Model // Message display area
	textarea textarea.Model // User input area

	// §22 Input history
	inputHistory    []string    // 已发送Input history（新 → 旧），仅会话内
	inputHistoryIdx int         // -1 = not in browse mode, >=0 = current browse index
	inputDraft      string      // Input draft before entering history browsing
	ticker          *animTicker // Progress animation ticker
	width           int         // Terminal width
	height          int         // Terminal height
	styles          cliStyles
	locale          *UILocale // i18n: current UI locale

	// §23 Placeholder: stored separately from textarea to avoid CJK rendering bug.
	// Textarea's built-in Placeholder causes a view-mode switch (placeholder→normal)
	// that triggers cellbuf incremental diff issues on Windows Terminal with CJK chars.
	placeholderText string // current placeholder string to display in View

	// --- Message state ---
	messages        []cliMessage          // Message history
	renderer        *glamour.TermRenderer // Markdown renderer
	streamingMsgIdx int                   // Index of current streaming message (-1 = no streaming message)
	newContentHint  bool                  // New content but user is not at bottom (show ↓ hint)
	ready           bool                  // Whether initialized

	// --- Agent state ---
	agentTurnID       uint64                        // monotonically increasing turn counter
	typing            bool                          // Whether agent is currently replying
	typingStartTime   time.Time                     // Current processing start time
	inputReady        bool                          // Input ready state (sending disabled during agent reply)
	msgBus            *bus.MessageBus               // Message bus reference
	sendInboundFn     func(bus.InboundMessage) bool // remote mode: forward to server via backend.SendInbound
	tempStatus        string                        // Temporary status hint (auto-expires)
	pendingCmds       []tea.Cmd                     // commands queued by helpers (auto-drained in Update)
	shouldQuit        bool                          // Smart quit: quit after current operation completes
	trimHistoryFn     func(cutoff time.Time) error  // /rewind: delete DB messages at or after cutoff timestamp
	resetTokenStateFn func()                        // /rewind: clear stale prompt/completion token counts

	// --- Message queue (messages queued during typing) ---
	messageQueue   []string // Messages queued waiting to be sent
	queueEditing   bool     // true = editing/viewing last queued message
	queueEditBuf   string   // Queued message content being edited
	needFlushQueue bool     // true = queue flush needed after handleAgentMessage

	// --- Background tasks ---
	bgTaskCount     int                            // running background tasks (0 = no indicator)
	bgTaskCountFn   func() int                     // callback to get current bg task count (set by channel)
	bgTaskListFn    func() []*tools.BackgroundTask // callback to list running tasks (remote mode)
	bgTaskKillFn    func(taskID string) error      // callback to kill a task (remote mode)
	bgTaskCleanupFn func()                         // callback to cleanup completed tasks (remote mode)

	// --- Interactive agents ---
	agentCount      int                                                            // active interactive agent sessions (0 = no indicator)
	agentCountFn    func() int                                                     // callback to get current agent count (set by channel)
	agentListFn     func() []panelAgentEntry                                       // callback to list active agents for panel
	agentInspectFn  func(roleName, instance string, tailCount int) (string, error) // callback to inspect agent activity
	agentMessagesFn func(roleName, instance string) []SessionChatMessage           // callback to get agent conversation messages
	sessionsListFn  func() []SessionPanelEntry                                     // callback to list all sessions for Sessions panel

	// --- Usage query ---
	usageQueryFn func(senderID string, days int) (cumulative *sqlite.UserTokenUsage, daily []sqlite.DailyTokenUsage, err error)

	// --- Web user management (admin only) ---
	createWebUserFn func(username string) (password string, err error)
	listWebUsersFn  func() ([]map[string]any, error)
	deleteWebUserFn func(username string) error
	isAdminFn       func() bool

	// --- Progress ---
	progress             *CLIProgressPayload
	iterationHistory     []cliIterationSnapshot // Completed iteration snapshots
	lastSeenIteration    int                    // Iteration number of last progress event
	iterationStartTime   time.Time              // current iteration wall-clock start time
	fastTickActive       bool                   // true when a fast tick chain (100ms) is running
	typewriterTickActive bool                   // true when typewriter tick chain (50ms) is running
	twVisible            int                    // typewriter: runes currently visible in stream content
	rwVisible            int                    // typewriter: runes currently visible in reasoning stream content
	rwCjkSkipTick        bool                   // alternates each tick to halve CJK speed (reasoning)
	twCjkSkipTick        bool                   // alternates each tick to halve CJK speed (stream)

	// --- Session ---
	workDir         string // Working directory (for title bar display)
	remoteMode      bool   // Whether connected to remote backend (for title bar hint)
	remoteServerURL string // remote server host for header display (e.g. "host:port")
	connState       string // WS connection state: "connected"|"disconnected"|"reconnecting"
	debugMode       bool   // --debug: UI capture + key injection via SIGUSR1
	debugCaptureMs  int    // --debug-capture-ms: UI capture interval in ms (0 = default 1000)
	senderID        string // Current identity ID (default "cli_user", switchable via /su command)
	channelName     string // Current channel (default "cli", may become "web" after /su switch)
	defaultChatID   string // Default chatID (restored when /su switches back)
	chatID          string // Session ID (distinguished by working directory)

	// --- §1 Incremental rendering ---
	renderCacheValid    bool   // Whether global cache is valid (set false after resize)
	cachedHistory       string // Cached rendered history messages (excluding current streaming message)
	cachedMsgCount      int    // messages count when cache was built
	lastViewportContent string // Last raw content of setViewportContent (for dedup)
	lastViewportWidth   int    // Last width of setViewportContent (for dedup)

	// --- §2 Tool visualization ---
	lastCompletedTools []CLIToolProgress // Snapshot at end of each round, independent of m.progress lifecycle
	lastReasoning      string            // Last iteration's reasoning_content, captured before progress is cleared
	lastThinking       string            // Last iteration's thinking_content, captured before progress is cleared

	// --- §8 Tab completion ---
	completions []string // Current completion candidates
	compIdx     int      // Currently selected completion index

	// --- §8b @ file reference completion ---
	fileCompletions []string // @ file path completion candidates
	fileCompIdx     int      // Currently selected file completion index
	fileCompActive  bool     // true = in Tab cycle, prevent re-glob

	// --- §9 Rewind (/rewind command) ---
	rewindMode      bool                   // true = rewind overlay active
	rewindItems     []rewindItem           // candidate user messages for rewind selection
	rewindCursor    int                    // selected index in rewindItems
	rewindResult    *tools.RewindResult    // result of the last rewind operation (for display)
	checkpointState *hooks.CheckpointState // file checkpoint state for rewind file rollback (nil = no file tracking)

	// --- §10 TODO progress bar ---
	todos            []CLITodoItem // TODO list synced from progress events
	todosDoneCleared bool          // Cleared by user input after full completion, prevent progress from refilling

	// --- §11 Tool Summary collapse ---
	toolSummaryExpanded bool // Ctrl+O toggle

	// --- §11b Pending Tool Summary ---
	// PhaseDone may arrive before handleAgentMessage. Store the tool_summary
	// here so handleAgentMessage can insert it at the correct position.
	pendingToolSummary *cliMessage

	// --- §12 Interactive Panel ---
	// panelMode: ""=normal, "settings"=settings panel, "askuser"=ask user panel
	panelMode     string
	panelCursor   int            // settings panel: selected item index
	panelEdit     bool           // settings panel: editing current item
	panelScrollY  int            // Panel scroll offset (manually managed, independent of viewport)
	panelEditTA   textarea.Model // settings panel: inline editor
	panelCombo    bool           // settings panel: combo dropdown open
	panelComboIdx int            // settings panel: combo selected option index
	// --- Panel state backup (for quick switch round-trip) ---
	panelValuesBackup   map[string]string              // saved panelValues before quick switch
	panelCursorBackup   int                            // saved panelCursor before quick switch
	panelOnSubmitBackup func(values map[string]string) // saved onSubmit callback
	// --- AskUser panel ---
	panelItems         []askItem            // askuser panel: question items
	panelTab           int                  // askuser panel: current tab (question index)
	panelOptSel        map[int]map[int]bool // askuser panel: selected option indices per question
	panelOptCursor     map[int]int          // askuser panel: highlighted option index per question
	panelAnswerTA      textarea.Model       // askuser panel: free-input editor (no-options mode)
	panelOtherTI       textinput.Model      // askuser panel: single-line Other input
	askPanelScrollY    int                  // askuser panel: internal scroll offset for long content
	askPanelTotalLines int                  // cached total line count for scroll clamping
	panelSchema        []SettingDefinition  // settings panel: schema copy
	// --- Approval panel ---
	approvalRequest      *tools.ApprovalRequest // pending approval request
	approvalResultCh     chan<- tools.ApprovalResult
	approvalCursor       int                             // 0=approve, 1=deny
	approvalDenyInput    textinput.Model                 // deny reason input
	approvalEnteringDeny bool                            // true when editing deny reason
	panelValues          map[string]string               // settings panel: current values
	panelOnSubmit        func(values map[string]string)  // callback on settings submit
	panelOnAnswer        func(answers map[string]string) // callback on askuser answers (key=index, value=answer)
	panelOnCancel        func()                          // callback on cancel

	// --- Bg Tasks Panel ---
	panelBgTasks   []*tools.BackgroundTask // cached task list
	panelBgAgents  []panelAgentEntry       // cached agent list
	panelBgCursor  int                     // selected item index (tasks first, then agents)
	panelBgViewing bool                    // true = viewing log of selected task

	panelBgLogLines []string // cached log lines for viewing

	// --- Sessions Panel ---
	panelSessionItems   []SessionPanelEntry // cached session list
	panelSessionCursor  int                 // selected item index
	panelSessionViewing bool                // true = viewing session messages

	// --- Danger Zone Panel ---
	panelDangerItems   []dangerItem
	panelDangerCursor  int
	panelDangerConfirm bool // true = showing confirm input
	panelDangerInput   textinput.Model
	panelDangerOnExec  func(targetType string) error // callback to execute clear

	// --- §13 Update Check ---

	// --- Runner Panel ---
	panelRunnerServerTI  textinput.Model     // Server URL input
	panelRunnerTokenTI   textinput.Model     // Token input
	panelRunnerWorkspace textinput.Model     // Workspace input
	panelRunnerEditField int                 // Currently editing field (0=server, 1=token, 2=workspace)
	updateNotice         *version.UpdateInfo // nil=nothing, non-nil=show notice
	checkingUpdate       bool                // true while /update is in progress

	// --- Channel Config Panel ---
	panelChannelItems  []string                     // channel names: ["web", "feishu", "qq", "napcat"]
	panelChannelCursor int                          // selected channel index
	panelChannelCfg    map[string]map[string]string // cached channel configs

	// --- §15 Subscription / Model Quick Switch ---
	quickSwitchMode          string              // ""=off, "subscription"=selecting subscription, "model"=selecting model
	quickSwitchList          []Subscription      // available subscriptions or models
	quickSwitchCursor        int                 // selected index
	quickSwitchReturnToPanel bool                // true = return to settings panel after switch completes
	subscriptionMgr          SubscriptionManager // injected by CLIChannel
	llmSubscriber            LLMSubscriber       // injected by CLIChannel

	// --- §16 Subscription generation guard ---
	// subGeneration increments every time the active subscription actually changes.
	// panelSubGeneration captures the generation when the settings panel opens.
	// ApplySettings REFUSES to write per-subscription LLM fields if generations don't match.
	// This is the structural guarantee against stale LLM values overwriting a new subscription.
	subGeneration      int
	panelSubGeneration int

	// --- §14 Splash screen ---
	splashDone  bool // true = splash animation ended, enter normal UI
	splashFrame int  // Current splash animation frame index
	suLoading   bool // true = loading history after /su user switch, showing loading screen

	// --- §16 Toast notification queue ---
	toasts     []cliToastItem // Toast queue (head = currently displayed)
	toastTimer bool           // true = toast dismiss timer started

	// --- §19 Long message folding ---
	msgLineOffsets []int // Start line number of each message in viewport-wrapped content

	// --- §Session state save/restore ---
	// Per-session saved state so switching sessions doesn't lose in-progress state.
	// Key = "channelName:chatID". Messages are NOT saved here — DB is source of truth.
	savedSessions map[string]*sessionState
	turnCancelled bool // true after Ctrl+C — prevents auto-start on stale progress

	// --- §21 Message search /search ---
	searchMode    bool            // Whether in search mode
	searchQuery   string          // Search query
	searchResults []int           // List of matching message indices
	searchIdx     int             // Currently navigated search result index (-1 = none selected)
	searchEditing bool            // true = editing search query, false = navigating results
	searchTI      textinput.Model // Search input box

	// toolDisplayInfo

	// --- 🥚 Easter Eggs ---
	easterEgg       easterEggMode // Currently active easter egg type ("" = none)
	easterEggCustom string        // Easter egg custom content (version achievement art, etc.)
	konamiBuffer    []string      // Konami Code key buffer
	matrixCols      int           // Matrix rain column count
	matrixRows      int           // Matrix rain row count
	matrixDrops     []int         // Matrix rain head position per column
	matrixSpeeds    []int         // Matrix rain drop speed per column
	matrixTrailLen  []int         // Matrix rain trail length per column
	matrixBuffer    [][]rune      // Matrix rain character buffer
	versionHitTimes []time.Time   // /version command call timestamps (triple-call detection)

	channel         *CLIChannel // back-reference to owning channel (set during Start)
	cachedModelName string      // cached model name for View() performance
	modelCount      int         // cached model list length for View() performance

	// === Runner Bridge ===
	runnerBridge *RunnerBridge
}

// cliMessage Single message
type cliMessage struct {
	role      string
	content   string
	timestamp time.Time
	isPartial bool
	// --- §1 Incremental rendering ---
	rendered    string // Cached render result (ANSI string)
	dirty       bool   // Whether re-render is needed
	renderWidth int    // 渲染时的Terminal width（用于 resize 失效检测）

	// --- §2 Tool visualization ---
	tools      []CLIToolProgress      // Flattened tool list (backward compatible)
	iterations []cliIterationSnapshot // Snapshots grouped by iteration (preferred)

	// --- §19 Long message folding ---
	renderedLines         int  // Total rendered lines (recalculated on each dirty)
	originalRenderedLines int  // Original line count before fold (saved at fold time, used for unfold decision)
	folded                bool // Whether folded

	// --- Markdown rendering for system messages ---
	markdown bool // when true, system messages go through glamour renderer (e.g. /usage tables)
}

// newCLIModel Create CLI model
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

	// Keep textarea's native newline bindings intact.
	// Plain Enter is intercepted by the outer CLI handler and used for send,
	// while modified/newline-intent keys (for example Ctrl+J depending on
	// terminal encoding) are allowed to reach the textarea so its built-in
	// multiline + internal-scroll behavior continues to work at MaxHeight.

	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))

	// Scroll behavior: Up/Down (line by line, also responds to mouse wheel escape sequences), PgUp/PgDn (page)
	// Note: Up/Down is handled by both textarea cursor movement and viewport scrolling.
	// The input history logic for KeyUp/KeyDown in handleKeyPress intercepts first,
	// but only triggers when idle + empty input, so wheel scrolling doesn't conflict during typing.
	vp.KeyMap.Up.SetKeys("up")
	vp.KeyMap.Down.SetKeys("down")
	vp.KeyMap.Left.SetKeys()
	vp.KeyMap.Right.SetKeys()
	vp.KeyMap.PageUp.SetKeys("pgup")
	vp.KeyMap.PageDown.SetKeys("pgdown")
	vp.KeyMap.HalfPageUp.SetKeys()
	vp.KeyMap.HalfPageDown.SetKeys()
	vp.SetHorizontalStep(0) // Disable horizontal scroll step

	renderer := newGlamourRenderer(maxBubbleWidth(80) - 2)

	// Ticker
	tk := newAnimTicker(dotFrames, currentTheme.Warning)

	return &cliModel{
		viewport:        vp,
		textarea:        ta,
		ticker:          tk,
		placeholderText: GetLocale(currentLocaleLang).IdlePlaceholders[0],
		messages:        make([]cliMessage, 0, cliMsgBufSize),
		styles:          buildStyles(80),
		renderer:        renderer,
		ready:           false,
		typing:          false,
		streamingMsgIdx: -1,
		progress:        nil,
		inputReady:      true,
		locale:          GetLocale(""),
		inputHistory:    make([]string, 0, 100),
		inputHistoryIdx: -1,
		inputDraft:      "",
		senderID:        "cli_user",
		channelName:     "cli",
	}
}

// SetMsgBus Set message bus (for sending user messages)
func (m *cliModel) SetMsgBus(msgBus *bus.MessageBus) {
	m.msgBus = msgBus
}

// SetSubscriptionMgr sets the subscription manager for quick switch.
func (m *cliModel) SetSubscriptionMgr(mgr SubscriptionManager) {
	m.subscriptionMgr = mgr
}

// SetLLMSubscriber sets the LLM subscriber for quick switch.
func (m *cliModel) SetLLMSubscriber(sub LLMSubscriber) {
	m.llmSubscriber = sub
}

// ---------------------------------------------------------------------------
// Bubble Tea Messages (internal message types)
// ---------------------------------------------------------------------------

// cliOutboundMsg Message received from agent
type cliOutboundMsg struct {
	msg bus.OutboundMessage
}

// cliProgressMsg Progress update message
type cliProgressMsg struct {
	payload *CLIProgressPayload
}

// cliProcessingMsg sets the typing/processing state externally (remote reconnect).
type cliProcessingMsg struct {
	processing bool
}

// cliConnStateMsg updates the WS connection state for the header bar indicator.
type cliConnStateMsg struct {
	state string // "connected" | "disconnected" | "reconnecting"
}

// cliHistoryLoadMsg loads history messages into the model from a goroutine-safe context.
// Data is pre-converted, so the Update handler only appends and rebuilds viewport.
type cliHistoryLoadMsg struct {
	history []cliMessage
}

// cliTickMsg Periodic refresh (for streaming output animation)
type cliTickMsg struct{}

// typewriterTickMsg Independent typewriter refresh (50ms interval, rune-by-rune output)
type typewriterTickMsg struct{}

// idleTickMsg Low-frequency periodic refresh (for placeholder rotation)
type idleTickMsg struct{}

// cliTempStatusClearMsg Temporary status hint auto-clear
type cliTempStatusClearMsg struct{}

// cliSettingsSavedMsg settings save completed (async callback result)
type cliSettingsSavedMsg struct {
	themeChanged bool
	theme        string
	langChanged  bool
	lang         string
	feedbackMsg  string
}

// cliSwitchLLMDoneMsg is sent when an async subscription switch completes.
type cliSwitchLLMDoneMsg struct {
	err      error
	subID    string
	subName  string
	subModel string
	mgr      SubscriptionManager
}

// cliInjectedUserMsg Notify CLI that a user message was injected (e.g. bg task completion notification)
type cliInjectedUserMsg struct {
	content string
}

// cliUpdateCheckMsg Update check result message
type cliUpdateCheckMsg struct {
	info *version.UpdateInfo
}

// isCtrlEnter Detect Ctrl+Enter key press.
// Terminals have no unified standard for Ctrl+Enter, common raw sequences:
//   - CSI u protocol: \x1b[13;5u   (kitty, Ghostty, Windows Terminal)
//   - Legacy format:     \x1b[27;5;13~ (some xterm variants)
//
// Note: Bubble Tea doesn't recognize these sequences, passes them as unknownCSISequenceMsg,
// Its String() format is "?CSI[49 51 59 53 117]?" (%+v outputs byte value array for []byte).
// so we need to match both KeyMsg and the string representation of unknownCSISequenceMsg.
func isCtrlEnter(msg tea.Msg) bool {
	s := fmt.Sprint(msg)
	return s == csiCtrlEnterCSIu || s == csiCtrlEnterRaw ||
		s == csiCtrlEnterLegacy || s == csiCtrlEnterLegacyRaw
}

// isCtrlO Detect Ctrl+O key press (some terminals send CSI u sequences, Bubble Tea can't recognize them).
// Ctrl+O = ASCII 15, CSI u protocol: \x1b[15;5u → "?CSI[49 53 59 53 117]?"
func isCtrlO(msg tea.Msg) bool {
	s := fmt.Sprint(msg)
	return s == csiCtrlOCsiu || s == csiCtrlORaw
}

// isCtrlJ detects Ctrl+J (newline). Ctrl+J = ASCII 10.
// CSI u protocol: \x1b[10;5u → "?CSI[49 48 59 53 117]?"
func isCtrlJ(msg tea.Msg) bool {
	s := fmt.Sprint(msg)
	return s == csiCtrlJCsiu || s == csiCtrlJRaw || s == csiCtrlJKey
}

// refreshCachedModelName caches the current model name to avoid repeated lookups in View().
// Should be called after channel init, config changes, and settings saves.
func (m *cliModel) refreshCachedModelName() {
	if m.channel == nil {
		return
	}
	// Single source of truth: read from active subscription (not from settings values)
	if m.channel.subscriptionMgr != nil {
		if sub, err := m.channel.subscriptionMgr.GetDefault(m.senderID); err == nil && sub != nil {
			m.cachedModelName = sub.Model
		}
	}
	// Cache model count for View() (avoids ListAllModels RPC per frame)
	if m.channel.modelLister != nil {
		m.modelCount = len(m.channel.modelLister.ListAllModels())
	}
}

// Init Init — start splash screen animation (minimum display 1 second)
func (m *cliModel) Init() tea.Cmd {
	cmds := []tea.Cmd{textarea.Blink, m.splashTick(0)}
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

// splashTick Generate tick command for splash screen animation
func (m *cliModel) splashTick(frame int) tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
		return splashTickMsg{frame: frame + 1}
	})
}

// suLoadHistoryCmd Async load history messages for /su target user
func (m *cliModel) suLoadHistoryCmd() tea.Cmd {
	chatID := m.chatID
	channelName := m.channelName
	progressFn := m.channel.config.GetActiveProgressFn

	// Agent sessions: load from in-memory interactiveSubAgents (not DB).
	if channelName == "agent" {
		dumpFn := m.channel.config.AgentSessionDumpFn
		if dumpFn != nil {
			return func() tea.Msg {
				history, err := dumpFn(chatID)
				// Agent sessions don't have GetActiveProgress, but try anyway
				var activeProgress *CLIProgressPayload
				if progressFn != nil {
					activeProgress = progressFn(channelName, chatID)
				}
				return suHistoryLoadMsg{history: history, err: err, channelName: channelName, chatID: chatID, activeProgress: activeProgress}
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
		var activeProgress *CLIProgressPayload
		if progressFn != nil {
			activeProgress = progressFn(channelName, chatID)
		}
		return suHistoryLoadMsg{history: history, err: err, channelName: channelName, chatID: chatID, activeProgress: activeProgress}
	}
}

// reloadMessagesFromSession triggers async history reload after context compression.
// The engine has replaced its internal message list and persisted to session DB;
// CLI must rebuild m.messages to stay in sync.
func (m *cliModel) reloadMessagesFromSession() {
	loader := m.channel.config.DynamicHistoryLoader
	if loader == nil {
		return
	}
	chatID := m.chatID
	channelName := m.channelName
	clipanic.Go("channel.cliModel.reloadMessagesFromSession", func() {
		history, err := loader(channelName, chatID)
		// Send result via async channel (goroutine-safe)
		if m.channel != nil {
			select {
			case m.channel.asyncCh <- cliHistoryReloadMsg{history: history, err: err}:
			default:
				// channel full, drop — next progress event will retry
			}
		}
	})
}

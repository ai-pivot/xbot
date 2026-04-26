package channel

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"encoding/json"
	"fmt"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/mattn/go-runewidth"
	"strings"
	"sync"
	"time"
	"xbot/agent/hooks"
	"xbot/bus"
	"xbot/llm"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	cliMsgBufSize = 100 // message buffer pre-allocation size

	// Textarea height bounds (auto-grow range)
	minTaHeight = 3  // minimum textarea rows
	maxTaHeight = 10 // maximum textarea rows before internal scrolling

	// Typewriter gap-based acceleration thresholds.
	// When the gap between visible and target runes exceeds these thresholds,
	// the typewriter accelerates to catch up smoothly.
	twGapFast   = 80 // gap > 80: fast-forward (20 runes/tick)
	twGapMedium = 40 // gap > 40: medium catch-up (10 runes/tick)
	twGapSlow   = 20 // gap > 20: gentle catch-up (3 runes/tick)
	twSpeedFast = 20
	twSpeedMed  = 10
	twSpeedSlow = 3

	// File completion hint display limits
	fileCompMaxNameRunes = 20 // max runes for file basename display before truncation
	fileCompTruncateAt   = 18 // runes to keep before adding ellipsis

	// Toast notification limits
	toastMaxQueue      = 5  // max toast items in queue
	toastTrimTo        = 4  // keep this many when queue overflows
	toastDisplaySec    = 3  // seconds each toast is visible
	toastMaxRunes      = 50 // max runes for toast text before truncation
	toastTruncateRunes = 47 // runes to keep before appending "..."

	// Splash animation frame thresholds
	splashMinFrames = 20 // minimum frames before splash can end (~1s at 50ms/frame)
	splashMaxFrames = 40 // hard cap (~2 seconds)

	// Input history limits
	inputHistoryMax = 100 // max stored input history entries

	// Layout: message bubble horizontal overhead (left border + left padding + right padding + right border)
	bubblePadding = 4

	// Panel minimum content width for rendering items (truncate if narrower)
	panelMinContentWidth = 20

	// Message role constants — used throughout the codebase for role comparisons.
	// These match the values stored in HistoryMessage.Role and cliMessage.role.
	roleUser        = "user"
	roleAssistant   = "assistant"
	roleSystem      = "system"
	roleToolSummary = "tool_summary"

	// Panel mode constants — used for panelMode field comparisons.
	panelModeSettings = "settings"
	panelModeDanger   = "danger"
	panelModeAskUser  = "askuser"
	panelModeRunner   = "runner"
	panelModeChannel  = "channel"
	panelModeBgTasks  = "bgtasks"
	panelModeSessions = "sessions"
	panelModeApproval = "approval"
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
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	ellipsis := "..."
	target := maxWidth - runewidth.StringWidth(ellipsis)
	if target <= 0 {
		return ellipsis[:maxWidth]
	}
	w := 0
	for i, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > target {
			return s[:i] + ellipsis
		}
		w += rw
	}
	return s
}

// hardWrapRunes wraps a line at maxW columns, breaking at character boundaries.
// ANSI escape sequences are preserved across wrapped segments.
// Returns the original line if it fits within maxW.
// countLines returns the number of lines in s (always >= 1 for non-empty strings).
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func hardWrapRunes(line string, maxW int) string {
	if maxW <= 0 {
		return line
	}
	if lipgloss.Width(line) <= maxW {
		return line
	}
	var lines []string
	var buf strings.Builder
	w := 0
	inEscape := false
	for _, r := range line {
		if r == '\x1b' {
			inEscape = true
			buf.WriteRune(r)
			continue
		}
		if inEscape {
			buf.WriteRune(r)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
			}
			continue
		}
		rw := runewidth.RuneWidth(r)
		// Safety: if a rune has zero display width (combining chars, control
		// chars, zero-width joiners), still emit it but don't let it block
		// line wrapping.  Without this, a run of zero-width runes causes
		// w to never reach maxW → infinite loop → CPU 100% freeze.
		if rw == 0 {
			buf.WriteRune(r)
			continue
		}
		if w+rw > maxW {
			lines = append(lines, buf.String())
			buf.Reset()
			w = 0
		}
		buf.WriteRune(r)
		w += rw
	}
	if buf.Len() > 0 {
		lines = append(lines, buf.String())
	}
	return strings.Join(lines, "\n")
}

// newGlamourRenderer creates a glamour Markdown renderer.
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

	// Document body text
	if t.GDocumentText != "" {
		style.Document.Color = c(t.GDocumentText)
	}
	// Headings (H1–H4)
	if t.GHeadingText != "" {
		style.Heading.Color = c(t.GHeadingText)
		style.H1.Color = c(t.GHeadingText)
		style.H2.Color = c(t.GHeadingText)
		style.H3.Color = c(t.GHeadingText)
		style.H4.Color = c(t.GHeadingText)
	}
	// Code block background and text
	if t.GCodeBlock != "" {
		style.CodeBlock.BackgroundColor = c(t.GCodeBlock)
		if style.CodeBlock.Chroma != nil {
			style.CodeBlock.Chroma.Background.BackgroundColor = c(t.GCodeBlock)
		}
	}
	if t.GCodeText != "" {
		style.CodeBlock.Color = c(t.GCodeText)
		if style.CodeBlock.Chroma != nil {
			style.CodeBlock.Chroma.Text.Color = c(t.GCodeText)
		}
	}
	// Links
	if t.GLinkText != "" {
		style.Link.Color = c(t.GLinkText)
		style.LinkText.Color = c(t.GLinkText)
	}
	// Block quotes
	if t.GBlockQuote != "" {
		style.BlockQuote.Color = c(t.GBlockQuote)
		style.BlockQuote.IndentToken = c("│ ")
	}
	// List items
	if t.GListItem != "" {
		style.Item.Color = c(t.GListItem)
	}
	// Horizontal rules
	if t.GHorizontalRule != "" {
		style.HorizontalRule.Color = c(t.GHorizontalRule)
	}

	r, _ := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(wrapWidth),
	)
	return r
}

// cliCommands: known command list (for Tab completion, §8)
var cliCommands = []string{
	"/cancel", "/channel", "/chat", "/clear", "/compact", "/context", "/exit",
	"/help", "/model", "/models", "/new", "/quit", "/rewind", "/search",
	"/sessions", "/settings", "/setup", "/ss", "/su", "/tasks", "/update",
	"/usage", "/user",
}

// §19 Long message folding threshold
const (
	msgFoldThresholdLines = 20
	msgFoldPreviewLines   = 6
)

// ---------------------------------------------------------------------------
// CLI Progress Payload (for structured progress events)
// ---------------------------------------------------------------------------

// CLIProgressPayload Structured progress message payload (corresponds to agent.StructuredProgress)。
type CLIProgressPayload struct {
	ChatID                 string // session key for routing — CLI filters by m.chatID
	Phase                  string
	Iteration              int
	ActiveTools            []CLIToolProgress
	CompletedTools         []CLIToolProgress
	Thinking               string
	Reasoning              string // model's reasoning/thinking chain (reasoning_content)
	SubAgents              []CLISubAgent
	Todos                  []CLITodoItem
	TokenUsage             *CLITokenUsage       // Token usage snapshot (real-time updates)
	StreamContent          string               // LLM streaming text content (accumulated, for real-time render)
	ReasoningStreamContent string               // LLM streaming reasoning content (accumulated, for real-time render)
	IterationHistory       []CLIProgressPayload // completed iteration snapshots (for mid-session reconnect restore)
	HistoryCompacted       bool                 // true after context compression — CLI should reload messages from session
}

// CLITokenUsage Token usage (corresponds to agent.TokenUsageSnapshot)
type CLITokenUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CacheHitTokens   int64
}

// CLITodoItem represents a TODO item for CLI display.
type CLITodoItem struct {
	ID   int
	Text string
	Done bool
}

// CLIToolProgress Execution progress of a single tool.
type CLIToolProgress struct {
	Name      string
	Label     string
	Status    string
	Elapsed   int64 // milliseconds (from progress event)
	Iteration int   // Belonging iteration ID
	Summary   string
	StartedAt time.Time // when tool started (for live elapsed timer)
}

// CLISubAgent Structured progress state of a sub-agent.
type CLISubAgent struct {
	Role     string
	Status   string // "running" | "done" | "error"
	Desc     string
	Children []CLISubAgent
}

// cliIterationSnapshot captures a completed iteration for the progress panel.
type cliIterationSnapshot struct {
	Iteration   int
	Thinking    string
	Reasoning   string // model's reasoning/thinking chain (reasoning_content)
	Tools       []CLIToolProgress
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
// CLI Channel Config
// ---------------------------------------------------------------------------

// HistoryIteration History iteration snapshot (for tool_summary rendering during session restore)
type HistoryIteration struct {
	Iteration   int
	Thinking    string
	Reasoning   string
	Tools       []CLIToolProgress
	ElapsedWall int64 // wall-clock duration of the iteration (ms)
}

// HistoryMessage History messages (for session restore)
type HistoryMessage struct {
	Role       string // "user", "assistant", "tool_summary", "system"
	Content    string
	Timestamp  time.Time
	Iterations []HistoryIteration // Only has value when role=="tool_summary", ordered by iteration
}

// iterSnapshot mirrors agent.IterationSnapshot for JSON unmarshaling Detail field.
type iterSnapshot struct {
	Iteration int            `json:"iteration"`
	Thinking  string         `json:"thinking,omitempty"`
	Reasoning string         `json:"reasoning,omitempty"`
	Tools     []iterToolSnap `json:"tools"`
}

type iterToolSnap struct {
	Name      string `json:"name"`
	Label     string `json:"label,omitempty"`
	Status    string `json:"status"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Summary   string `json:"summary,omitempty"`
}

// formatToolLabel generates a short human-readable label from a tool name and its JSON arguments.
// Used when restoring progress from intermediate assistant messages (no Detail snapshot),
// e.g. after server restart. Produces labels like "Shell(tail -100 file.log)" or "Read(path)".
func formatToolLabel(name, argsJSON string) string {
	const maxLen = 60
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return name
	}

	get := func(key string) string {
		if v, ok := args[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
			return fmt.Sprintf("%v", v)
		}
		return ""
	}

	switch name {
	case "Shell":
		cmd := get("command")
		if cmd != "" {
			if len(cmd) > maxLen-len(name)-2 {
				cmd = cmd[:maxLen-len(name)-5] + "..."
			}
			return name + "(" + cmd + ")"
		}
	case "Read":
		p := get("path")
		if p != "" {
			return name + "(" + p + ")"
		}
	case "Grep":
		p := get("pattern")
		if p != "" {
			return name + "(" + p + ")"
		}
	case "Glob":
		p := get("pattern")
		if p != "" {
			return name + "(" + p + ")"
		}
	case "Write", "FileCreate":
		p := get("path")
		if p != "" {
			return name + "(" + p + ")"
		}
	case "Edit", "FileReplace":
		p := get("path")
		if p != "" {
			return name + "(" + p + ")"
		}
	case "WebSearch":
		q := get("query")
		if q != "" {
			return name + "(" + q + ")"
		}
	case "SubAgent":
		r := get("role")
		t := get("task")
		if r != "" {
			if t != "" && len(t) > 30 {
				t = t[:27] + "..."
			}
			if t != "" {
				return name + "(" + r + ": " + t + ")"
			}
			return name + "(" + r + ")"
		}
	default:
		// Generic: show first string parameter
		for _, v := range args {
			if s, ok := v.(string); ok && s != "" {
				if len(s) > maxLen-len(name)-2 {
					s = s[:maxLen-len(name)-5] + "..."
				}
				return name + "(" + s + ")"
			}
		}
	}
	return name
}

// ConvertMessagesToHistory converts raw DB messages into HistoryMessages for CLI display.
// It handles three scenarios:
//  1. Normal completed turn: assistant with Detail → one tool_summary + assistant
//  2. Cancelled/interrupted turn: intermediate assistant(ToolCalls) without Detail → pending tool_summary
//  3. Mixed: some turns completed, last one cancelled
func ConvertMessagesToHistory(msgs []llm.ChatMessage) []HistoryMessage {
	var history []HistoryMessage
	var pendingIters []HistoryIteration
	var curIterTools []CLIToolProgress
	var curIterIdx int
	var curIterThinking string
	var curIterReasoning string

	finishCurIter := func() {
		if len(curIterTools) > 0 || curIterThinking != "" || curIterReasoning != "" {
			pendingIters = append(pendingIters, HistoryIteration{
				Iteration: curIterIdx,
				Thinking:  curIterThinking,
				Reasoning: curIterReasoning,
				Tools:     curIterTools,
			})
		}
		curIterTools = nil
		curIterThinking = ""
		curIterReasoning = ""
	}

	flushPending := func() {
		finishCurIter()
		if len(pendingIters) > 0 {
			history = append(history, HistoryMessage{
				Role:       roleToolSummary,
				Iterations: pendingIters,
			})
			pendingIters = nil
		}
	}

	for _, m := range msgs {
		switch m.Role {
		case "tool":
			continue
		case roleAssistant:
			if m.Detail != "" {
				// Detail has authoritative iteration history. Discard pending iters
				// from intermediate assistant messages — they lack elapsed/label data.
				finishCurIter()
				pendingIters = nil

				var snaps []iterSnapshot
				if jsonErr := json.Unmarshal([]byte(m.Detail), &snaps); jsonErr == nil {
					iters := make([]HistoryIteration, 0, len(snaps))
					for _, snap := range snaps {
						toolList := make([]CLIToolProgress, len(snap.Tools))
						for i, t := range snap.Tools {
							label := t.Label
							if label == "" {
								label = t.Name
							}
							toolList[i] = CLIToolProgress{
								Name:      t.Name,
								Label:     label,
								Status:    t.Status,
								Elapsed:   t.ElapsedMS,
								Iteration: snap.Iteration,
								Summary:   t.Summary,
							}
						}
						iters = append(iters, HistoryIteration{
							Iteration: snap.Iteration,
							Thinking:  snap.Thinking,
							Reasoning: snap.Reasoning,
							Tools:     toolList,
						})
					}
					if len(iters) > 0 {
						history = append(history, HistoryMessage{
							Role:       roleToolSummary,
							Timestamp:  m.Timestamp,
							Iterations: iters,
						})
					}
				}
				if m.Content != "" {
					history = append(history, HistoryMessage{
						Role:      roleAssistant,
						Content:   m.Content,
						Timestamp: m.Timestamp,
					})
				}
			} else if len(m.ToolCalls) > 0 {
				// Intermediate assistant with tool_calls from incremental persistence.
				// Accumulate into pending — don't flush yet.
				finishCurIter()
				curIterIdx++
				curIterThinking = m.Content
				curIterReasoning = m.ReasoningContent
				for _, tc := range m.ToolCalls {
					curIterTools = append(curIterTools, CLIToolProgress{
						Name:      tc.Name,
						Label:     formatToolLabel(tc.Name, tc.Arguments),
						Status:    "done",
						Elapsed:   0,
						Iteration: curIterIdx,
					})
				}
			} else if m.Content != "" {
				flushPending()
				history = append(history, HistoryMessage{
					Role:      roleAssistant,
					Content:   m.Content,
					Timestamp: m.Timestamp,
				})
			}
		default:
			flushPending()
			if m.Content != "" {
				history = append(history, HistoryMessage{
					Role:      m.Role,
					Content:   m.Content,
					Timestamp: m.Timestamp,
				})
			}
		}
	}
	flushPending()
	return history
}

// CLIChannelConfig CLI channel configuration
type CLIChannelConfig struct {
	WorkDir              string                                                                                                         // Working directory (for title bar display)
	ChatID               string                                                                                                         // Session ID (distinguished by working directory)
	RemoteMode           bool                                                                                                           // Whether in remote backend mode (for title bar/hints)
	RemoteServerURL      string                                                                                                         // remote server URL (for header display, e.g. "ws://host:port")
	DebugMode            bool                                                                                                           // --debug: UI capture + key injection via SIGUSR1
	DebugInput           string                                                                                                         // --debug-input "1,enter,ctrl+c": auto-inject key sequence after startup
	DebugCaptureMs       int                                                                                                            // --debug-capture-ms 200: UI capture interval in ms (default 1000)
	HistoryLoader        func() ([]HistoryMessage, error)                                                                               // Session restore: load history messages
	DynamicHistoryLoader func(channelName, chatID string) ([]HistoryMessage, error)                                                     // Load target user history after /su switch
	AgentSessionDumpFn   func(chatID string) ([]HistoryMessage, error)                                                                  // Load messages from Agent memory on agent session switch
	GetCurrentValues     func() map[string]string                                                                                       // Get current config values (for settings panel initial values)
	ApplySettings        func(values map[string]string)                                                                                 // Apply setting changes (write config.json + update runtime state)
	IsFirstRun           bool                                                                                                           // First-run flag, auto-opens setup panel on TUI start
	ClearMemory          func(targetType string) error                                                                                  // Clear memory (danger zone)
	GetMemoryStats       func() map[string]string                                                                                       // Get memory stats (danger zone)
	SwitchLLM            func(provider, baseURL, apiKey, model string) error                                                            // Switch active LLM (config + factory + save)
	RefreshValuesCache   func()                                                                                                         // Refresh GetCurrentValues cache (called after sub switch)
	UsageQuery           func(senderID string, days int) (cumulative *sqlite.UserTokenUsage, daily []sqlite.DailyTokenUsage, err error) // Query token usage
	AgentCount           func() int                                                                                                     // Get count of active interactive agents
	AgentList            func() []AgentPanelEntry                                                                                       // List active interactive agents (for panel display)
	AgentInspect         func(roleName, instance string, tailCount int) (string, error)                                                 // Inspect recent activity of interactive agent (tail style)
	AgentMessages        func(roleName, instance string) []SessionChatMessage                                                           // Get conversation messages of interactive agent
	ChatCreateFn         func(channelName, senderID, label string) (string, error)                                                      // Create new ChatRoom (returns chatID)
	SessionsList         func() []SessionPanelEntry                                                                                     // List all sessions (main + subagent)
	GetActiveProgressFn  func(channelName, chatID string) *CLIProgressPayload                                                           // Get active progress of target session (for session switch restore)
	ChannelConfigGetFn   func() (map[string]map[string]string, error)                                                                   // Get channel config (for /channel panel)
	ChannelConfigSetFn   func(channel string, values map[string]string) error                                                           // Save channel config (for /channel panel)
	CreateWebUserFn      func(username string) (password string, err error)                                                             // Create Web user (admin only, returns auto-generated password)
	ListWebUsersFn       func() ([]map[string]any, error)                                                                               // List all Web users
	DeleteWebUserFn      func(username string) error                                                                                    // Delete Web user (admin only)
	IsAdminFn            func() bool                                                                                                    // Check if current user is admin
}

type AgentPanelEntry struct {
	Role       string
	Instance   string
	Running    bool
	Background bool
	Task       string // one-shot subagent task (empty for interactive)
	Preview    string // latest progress/last reply summary for panel display
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
	MessageHint string // preview of last message
}

// ---------------------------------------------------------------------------
// CLI Channel (implements Channel interface)
// ---------------------------------------------------------------------------

// CLIChannel CLI channel implementation
type CLIChannel struct {
	config  CLIChannelConfig
	msgBus  *bus.MessageBus
	msgChan chan bus.OutboundMessage // Channel for receiving agent replies
	workDir string                   // Working directory

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
	progressCh chan *CLIProgressPayload
	asyncCh    chan tea.Msg // unified async send channel (buffered)

	// Services (injected by Agent or main)
	settingsSvc SettingsService // interface for GetSettings/SetSetting
	configMu    sync.RWMutex    // protects runner LLM fields (llmClient, modelList, llmProvider)
	modelLister ModelLister     // provides available model names for combo

	// Multi-subscription management
	subscriptionMgr SubscriptionManager // manages LLM subscriptions
	llmSubscriber   LLMSubscriber       // switches active LLM (propagated to model)

	// Background tasks
	bgTaskMgr  *tools.BackgroundTaskManager
	bgTaskKill func(taskID string) error // remote mode: RPC-backed kill

	// Runner LLM access
	llmClient    llm.LLM
	modelList    []string
	llmProvider  string
	bgSessionKey string

	runnerAutoConnect *runnerAutoConnectConfig // auto-connect as runner after TUI init

	// Permission control
	approvalState *hooks.ApprovalState // injected to wire CLIApprovalHandler after program creation

	// Pending injections (set before model exists, applied in Start)
	pendingTrimHistoryFn     func(time.Time) error
	pendingResetTokenStateFn func()
	pendingHistory           []HistoryMessage    // remote mode: cached history before model is ready
	pendingProgress          *CLIProgressPayload // remote mode: cached progress before model is ready
	pendingCheckpointState   *hooks.CheckpointState
	pendingSendInboundFn     func(bus.InboundMessage) bool
	// Pending remote bg task callbacks (set before model exists in remote mode)
	pendingBgTaskCountFn   func() int
	pendingBgTaskListFn    func() []*tools.BackgroundTask
	pendingBgTaskKillFn    func(taskID string) error // remote mode: forward to server
	pendingBgTaskCleanupFn func()                    // remote mode: cleanup completed tasks
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
}

// Subscription represents a LLM subscription for display/selection.
type Subscription struct {
	ID              string
	Name            string
	Provider        string
	BaseURL         string
	APIKey          string
	Model           string
	MaxOutputTokens int
	ThinkingMode    string
	Active          bool
}

// SubscriptionManager manages user LLM subscriptions.
type SubscriptionManager interface {
	List(senderID string) ([]Subscription, error)
	GetDefault(senderID string) (*Subscription, error)
	Add(sub *Subscription) error
	Remove(id string) error
	SetDefault(id, chatID string) error
	SetModel(id, model string) error
	Rename(id, name string) error
	Update(id string, sub *Subscription) error
}

// LLMSubscriber switches the active LLM for a user (called when subscription changes).
type LLMSubscriber interface {
	SwitchSubscription(senderID string, sub *Subscription, chatID string) error
	SwitchModel(senderID, model string)
	GetDefaultModel() string
}

// NewCLIChannel Create CLI channel

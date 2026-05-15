package channel

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"encoding/json"
	"fmt"

	// Markdown rendering for assistant messages
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/x/ansi"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"xbot/bus"
	"xbot/llm"
	"xbot/plugin"
	"xbot/protocol"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

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
// Returns the original line if it fits within maxW.
//
// Break rules:
//   - Break after any space (space stays on current line, next line starts clean).
//   - Break after any CJK character (CJK chars can wrap at any boundary).
//   - Otherwise break at the exact column boundary.
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
	lastBreakable := -1 // byte offset in buf of last position where we can break after

	// Track active ANSI state so we can replay it on continuation lines.
	// We record all escape sequences since the last SGR reset (\x1b[0m).
	var ansiState strings.Builder // accumulated SGR sequences since last reset
	haveAnsiState := false

	for _, r := range line {
		if r == '\x1b' {
			inEscape = true
			buf.WriteRune(r)
			ansiState.WriteRune(r)
			continue
		}
		if inEscape {
			buf.WriteRune(r)
			ansiState.WriteRune(r)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
				// Detect SGR reset: \x1b[0m or \x1b[m
				s := ansiState.String()
				if strings.HasSuffix(s, "[0m") || strings.HasSuffix(s, "[m") {
					ansiState.Reset()
					haveAnsiState = false
				} else if strings.HasSuffix(s, "m") {
					haveAnsiState = true
				}
			}
			continue
		}
		rw := ansi.StringWidth(string(r))
		// Safety: zero-width runes don't block wrapping.
		if rw == 0 {
			buf.WriteRune(r)
			continue
		}

		// Mark breakable positions: after space or after CJK character
		if r == ' ' || isCJK(r) {
			lastBreakable = buf.Len() + utf8.RuneLen(r)
		}

		if w+rw > maxW {
			if lastBreakable > 0 && lastBreakable < buf.Len() {
				// Break at last breakable position
				keep := buf.String()[:lastBreakable]
				rest := buf.String()[lastBreakable:]
				lines = append(lines, keep)
				buf.Reset()
				buf.WriteString(rest)
				w = lipgloss.Width(rest)
				lastBreakable = -1
			} else {
				// No breakable position found, hard break here
				lines = append(lines, buf.String())
				buf.Reset()
				w = 0
				lastBreakable = -1
			}
			// Replay active ANSI state on continuation line
			if haveAnsiState {
				buf.WriteString(ansiState.String())
			}
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
		// Glamour handles table column sizing and paragraph wrapping.
		// hardWrapRunes serves as a safety net for lines that still exceed
		// viewport width (e.g. long URLs) but should not be the primary
		// wrapping mechanism — it doesn't understand table structure and
		// would break separator/alignment lines.
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
// CLI Channel Config
// ---------------------------------------------------------------------------

// HistoryIteration 历史迭代快照（用于会话恢复的 tool_summary 渲染）
type HistoryIteration = protocol.HistoryIteration

// HistoryMessage 历史消息（用于会话恢复）
type HistoryMessage = protocol.HistoryMessage

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
	var args map[string]any
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
	var curIterTools []protocol.ToolProgress
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

	// lastAssistantTS tracks the timestamp of the last processed assistant
	// message, used to assign a unique Timestamp to flushPending()-generated
	// tool_summary messages. Without this, multiple interrupted turns produce
	// tool_summary messages with zero timestamps, causing dedup to drop all
	// but the first.
	var lastAssistantTS time.Time
	// syntheticIdx provides monotonically-increasing nanosecond offsets to
	// guarantee unique timestamps for consecutive flushPending() calls when
	// no real assistant timestamp is available (e.g. all turns interrupted).
	var syntheticIdx int

	flushPending := func() {
		finishCurIter()
		if len(pendingIters) > 0 {
			ts := lastAssistantTS
			if ts.IsZero() {
				// No assistant message in this turn — assign a synthetic
				// timestamp so each tool_summary gets a unique dedup key.
				ts = time.Date(2024, 1, 1, 0, 0, 0, syntheticIdx, time.UTC)
				syntheticIdx++
			}
			history = append(history, HistoryMessage{
				Role:       "tool_summary",
				Timestamp:  ts,
				Iterations: pendingIters,
			})
			pendingIters = nil
		}
	}

	for _, m := range msgs {
		switch m.Role {
		case "tool":
			continue
		case "assistant":
			lastAssistantTS = m.Timestamp
			if m.Detail != "" {
				// Detail has authoritative iteration history. Discard pending iters
				// from intermediate assistant messages — they lack elapsed/label data.
				finishCurIter()
				pendingIters = nil

				var snaps []iterSnapshot
				if jsonErr := json.Unmarshal([]byte(m.Detail), &snaps); jsonErr == nil {
					iters := make([]HistoryIteration, 0, len(snaps))
					for _, snap := range snaps {
						toolList := make([]protocol.ToolProgress, len(snap.Tools))
						for i, t := range snap.Tools {
							label := t.Label
							if label == "" {
								label = t.Name
							}
							toolList[i] = protocol.ToolProgress{
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
							Role:       "tool_summary",
							Timestamp:  m.Timestamp,
							Iterations: iters,
						})
					}
				}
				if m.Content != "" {
					history = append(history, HistoryMessage{
						Role:      "assistant",
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
					curIterTools = append(curIterTools, protocol.ToolProgress{
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
					Role:      "assistant",
					Content:   m.Content,
					Timestamp: m.Timestamp,
				})
			}
		default:
			flushPending()
			// Reset lastAssistantTS after flushing: the next tool_summary
			// belongs to a new turn (this default case is typically "user"),
			// so it should use its own synthetic timestamp if that turn
			// is also interrupted (no assistant reply).
			lastAssistantTS = time.Time{}
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

// CLIChannelConfig CLI 渠道配置
type CLIChannelConfig struct {
	WorkDir              string                                                                                                         // 工作目录（用于标题栏显示）
	ChatID               string                                                                                                         // 会话 ID（按工作目录区分）
	RemoteMode           bool                                                                                                           // 是否为 remote backend 模式（用于标题栏/轻提示）
	RemoteServerURL      string                                                                                                         // remote server URL (for header display, e.g. "ws://host:port")
	DebugMode            bool                                                                                                           // --debug: UI capture + key injection via SIGUSR1
	DebugInput           string                                                                                                         // --debug-input "1,enter,ctrl+c": auto-inject key sequence after startup
	DebugCaptureMs       int                                                                                                            // --debug-capture-ms 200: UI capture interval in ms (default 1000)
	HistoryLoader        func() ([]HistoryMessage, error)                                                                               // 会话恢复：加载历史消息
	DynamicHistoryLoader func(channelName, chatID string) ([]HistoryMessage, error)                                                     // /su 切换用户后加载目标用户历史
	TokenStateLoader     func() (promptTokens, completionTokens int64)                                                                  // 会话恢复：从 DB 加载上次 Run 的 token 计数
	AgentSessionDumpFn   func(chatID string) ([]HistoryMessage, error)                                                                  // agent session 切换时从 Agent 内存加载消息
	GetCurrentValues     func() map[string]string                                                                                       // 获取当前配置值（用于 settings panel 初始值）
	ApplySettings        func(values map[string]string, chatID string)                                                                  // 应用设置变更（写 config.json + 更新运行时状态）
	IsFirstRun           bool                                                                                                           // 首次运行标志，TUI 启动时自动打开 setup panel
	ClearMemory          func(targetType string) error                                                                                  // 清空记忆（danger zone）
	GetMemoryStats       func() map[string]string                                                                                       // 获取记忆统计（danger zone）
	SwitchLLM            func(provider, baseURL, apiKey, model string) error                                                            // 切换活跃 LLM（config + factory + save）
	RefreshValuesCache   func()                                                                                                         // 刷新 GetCurrentValues 缓存（sub 切换后调用）
	UsageQuery           func(senderID string, days int) (cumulative *sqlite.UserTokenUsage, daily []sqlite.DailyTokenUsage, err error) // 查询 token 用量
	AgentCount           func() int                                                                                                     // 获取活跃的 interactive agent 数量
	AgentList            func() []AgentPanelEntry                                                                                       // 列出活跃 interactive agents（用于 panel 展示）
	AgentInspect         func(roleName, instance string, tailCount int) (string, error)                                                 // 窥探 interactive agent 的最近活动（tail 风格）
	AgentMessages        func(roleName, instance string) []SessionChatMessage                                                           // 获取 interactive agent 的对话消息
	ChatCreateFn         func(channelName, senderID, label string) (string, error)                                                      // 创建新 ChatRoom（返回 chatID）
	SessionsDeleteFn     func(channelName, chatID string) error                                                                         // 删除 session（本地 JSON + 服务端 DB 级联）
	SessionsListRefresh  func()                                                                                                         // 侧边栏刷新：session 创建/删除后立即调用，确保 sidebar 不显示过期数据
	SessionsList         func() []SessionPanelEntry                                                                                     // 列出所有 session（main + subagent）
	GetActiveProgressFn  func(channelName, chatID string) *protocol.ProgressEvent                                                       // 获取目标 session 的活跃进度（session switch 恢复用）
	GetTodosFn           func(channelName, chatID string) []protocol.TodoItem                                                           // 获取目标 session 的服务端 TODO 列表（session switch 覆盖本地缓存用）
	GetTokenStateFn      func(channelName, chatID string) (promptTokens, completionTokens int64)                                        // 获取目标 session 的最后 token 状态（session switch 恢复 context bar 用）
	TrimHistoryFn        func(channelName, chatID string, cutoff time.Time) error                                                       // rewind 回退时删除 DB 消息（channel+chatID 动态传入，支持多 session）
	ChannelConfigGetFn   func() (map[string]map[string]string, error)                                                                   // 获取频道配置（用于 /channel 面板）
	ChannelConfigSetFn   func(channel string, values map[string]string) error                                                           // 保存频道配置（用于 /channel 面板）
	CreateWebUserFn      func(username string) (password string, err error)                                                             // 创建 Web 用户（admin only，返回自动生成的密码）
	ListWebUsersFn       func() ([]map[string]any, error)                                                                               // 列出所有 Web 用户
	DeleteWebUserFn      func(username string) error                                                                                    // 删除 Web 用户（admin only）
	IsAdminFn            func() bool                                                                                                    // 检查当前用户是否 admin
	PaletteContributor   PaletteContributor                                                                                             // supplies external commands for command palette
	SidebarWidthOverride int                                                                                                            // --sidebar-width N (0 = use setting/default)
	NoSidebar            bool                                                                                                           // --no-sidebar
	TodoManager          *tools.TodoManager                                                                                             // per-session todo persistence
	SetCWDFn             func(channelName, chatID, dir string) error                                                                    // 会话切换时初始化 CWD
	BindChatFn           func(chatID string) error                                                                                      // 订阅 Hub 路由，使服务器推送事件（progress/stream/outbound）到达客户端
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
// CLI Channel (implements Channel interface)
// ---------------------------------------------------------------------------

// CLIChannel CLI 渠道实现
type CLIChannel struct {
	config  *CLIChannelConfig
	msgBus  *bus.MessageBus
	msgChan chan bus.OutboundMessage // 接收 agent 回复的通道
	workDir string                   // 工作目录

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
	bgTaskMgr  *tools.BackgroundTaskManager
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
	pendingHistory           []HistoryMessage        // remote mode: cached history before model is ready
	pendingProgress          *protocol.ProgressEvent // remote mode: cached progress before model is ready
	pendingCheckpointState   *protocol.CheckpointState
	pendingSendInboundFn     func(bus.InboundMessage) bool
	// Pending remote bg task callbacks (set before model exists in remote mode)
	pendingBgTaskCountFn     func() int
	pendingBgTaskListFn      func() []*tools.BackgroundTask
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

// Subscription represents a LLM subscription for display/selection.
type Subscription = protocol.Subscription

// PerModelConfig stores per-model token overrides within a subscription.
type PerModelConfig = protocol.PerModelConfig

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
	UpdatePerModelConfig(id, model string, pmc PerModelConfig) error
}

// LLMSubscriber switches the active LLM for a user (called when subscription changes).
type LLMSubscriber interface {
	SwitchSubscription(senderID string, sub *Subscription, chatID string) error
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

package cli

import (
	"fmt"

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
// CLI-local constants
// ---------------------------------------------------------------------------

const (
	cliMsgBufSize = 100
)

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
const (
	IconCheck        = "✓"
	IconCross        = "✗"
	IconDot          = "◉"
	IconArrow        = "→"
	IconBullet       = "•"
	IconWarning      = "⚠"
	IconInfo         = "ℹ"
	IconSearch       = "◈"
	IconRobot        = "◆"
	IconRunnerOn     = "◉"
	IconRunnerWait   = "◎"
	IconUser         = "▣"
	IconGear         = "⚙"
	IconCloudOn      = "☁"
	IconCloudOff     = "⊘"
	IconCloudWait    = "◌"
	IconDiamond      = "◈"
	IconDiamondSolid = "◆"
	IconDiamondEmpty = "◇"
	IconGuideActive  = "┊"
	IconGuideDim     = "┆"
	IconDotLine      = "┈"
)

// §19 长消息折叠阈值
const (
	msgFoldThresholdLines = 20
	msgFoldPreviewLines   = 6
)

// ---------------------------------------------------------------------------
// CLI Progress Payload
// ---------------------------------------------------------------------------

// cliIterationSnapshot captures a completed iteration for the progress panel.
type cliIterationSnapshot struct {
	Iteration   int
	Thinking    string
	Reasoning   string
	Tools       []protocol.ToolProgress
	ElapsedWall int64
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

// CLIChannelConfig CLI 渠道配置
type CLIChannelConfig struct {
	WorkDir                string
	ChatID                 string
	RemoteMode             bool
	RemoteServerURL        string
	DebugMode              bool
	DebugInput             string
	DebugCaptureMs         int
	HistoryLoader          func() ([]HistoryMessage, error)
	DynamicHistoryLoader   func(channelName, chatID string) ([]HistoryMessage, error)
	TokenStateLoader       func() (promptTokens, completionTokens int64)
	AgentSessionDumpFn     func(chatID string) ([]HistoryMessage, error)
	AgentSessionLLMStateFn func(chatID string) (modelName string, maxContextTokens, maxOutputTokens int64, compressRatio float64, promptTokens, completionTokens int64)
	GetCurrentValues       func() map[string]string
	ApplySettings          func(values map[string]string, chatID string)
	IsFirstRun             bool
	ClearMemory            func(targetType string) error
	GetMemoryStats         func() map[string]string
	SwitchLLM              func(provider, baseURL, apiKey, model string) error
	RefreshValuesCache     func(subscriptionID string)
	UsageQuery             func(senderID string, days int) (cumulative *UserTokenUsage, daily []DailyTokenUsage, err error)
	AgentCount             func() int
	AgentList              func() []AgentPanelEntry
	AgentInspect           func(roleName, instance string, tailCount int) (string, error)
	AgentMessages          func(roleName, instance string) []SessionChatMessage
	ChatCreateFn           func(channelName, senderID, label string) (string, error)
	SessionsDeleteFn       func(channelName, chatID string) error
	SessionsListRefresh    func()
	SessionsList           func() []SessionPanelEntry
	GetActiveProgressFn    func(channelName, chatID string) *protocol.ProgressEvent
	GetTodosFn             func(channelName, chatID string) []protocol.TodoItem
	GetTokenStateFn        func(channelName, chatID string) (promptTokens, completionTokens int64)
	TrimHistoryFn          func(channelName, chatID string, cutoff time.Time) error
	ChannelConfigGetFn     func() (map[string]map[string]string, error)
	ChannelConfigSetFn     func(channel string, values map[string]string) error
	CreateWebUserFn        func(username string) (password string, err error)
	ListWebUsersFn         func() ([]map[string]any, error)
	DeleteWebUserFn        func(username string) error
	IsAdminFn              func() bool
	PaletteContributor     PaletteContributor
	SidebarWidthOverride   int
	NoSidebar              bool
	TodoManager            *cliTodoManager
	SetCWDFn               func(channelName, chatID, dir string) error
	BindChatFn             func(chatID string) error
	Ephemeral              bool
}

// ---------------------------------------------------------------------------
// CLI Channel (implements Channel interface)
// ---------------------------------------------------------------------------

// CLIChannel CLI 渠道实现
type CLIChannel struct {
	config  *CLIChannelConfig
	msgChan chan OutboundMsg
	workDir string

	// Bubble Tea
	program   *tea.Program
	programMu sync.Mutex
	model     *cliModel

	// Lifecycle
	stopCh chan struct{}
	wg     sync.WaitGroup

	progressCh chan *protocol.ProgressEvent
	asyncCh    chan tea.Msg

	// Services
	settingsSvc        SettingsService
	configMu           sync.RWMutex
	modelLister        ModelLister
	PaletteContributor PaletteContributor

	// Multi-subscription management
	subscriptionMgr SubscriptionManager
	llmSubscriber   LLMSubscriber

	// Background tasks
	bgTaskKill func(taskID string) error

	// Runner LLM access
	llmClient    llm.LLM
	modelList    []string
	llmProvider  string
	bgSessionKey string

	runnerAutoConnect *runnerAutoConnectConfig

	// Permission control
	approvalState *protocol.ApprovalState

	// Pending injections
	pendingTrimHistoryFn     func(time.Time) error
	pendingResetTokenStateFn func()
	pendingHistory           []HistoryMessage
	pendingProgress          *protocol.ProgressEvent
	pendingCheckpointState   *protocol.CheckpointState
	pendingSendInboundFn     func(InboundMsg) bool
	pendingBgTaskCountFn     func() int
	pendingBgTaskListFn      func() []*BgTask
	pendingBgTaskKillFn      func(taskID string) error
	pendingBgTaskCleanupFn   func()
	pendingPluginMgrFn       func() *plugin.PluginManager
	pendingWidgetRegistry    *plugin.WidgetRegistry
	pendingRemotePluginCache *remotePluginCache
}

// SettingsService is the interface needed by CLIChannel for settings panel.
type SettingsService interface {
	GetSettings(channelName, senderID string) (map[string]string, error)
	SetSetting(channelName, senderID, key, value string) error
}

// ModelLister provides available model names for the settings combo box.
type ModelLister interface {
	ListModels() []string
	ListAllModels() []string
	EnsureModelsLoaded()
}

// SendTUIControl sends a TUI session control message through asyncCh.
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

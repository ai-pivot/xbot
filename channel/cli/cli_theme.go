package cli

import (
	"encoding/json"
	"path/filepath"

	"fmt"
	"hash/fnv"
	"image/color"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"xbot/config"
	"xbot/internal/textarea"
	"xbot/plugin"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/muesli/termenv"
)

func init() {
	termenv.SetDefaultOutput(termenv.NewOutput(os.Stdout, termenv.WithTTY(false)))
}

// terminalProfile stores the detected terminal color profile.
// Updated when BubbleTea sends ColorProfileMsg. Used by buildStyles
// to ensure muted/dim colors remain visible on low-color terminals
// (e.g. Linux console with ANSI 16-color profile).
var terminalProfile colorprofile.Profile

// Theme system — semantic color palette with foreground/background layering.
// All schemes are designed for dark terminal backgrounds.
type cliTheme struct {
	// Text (3-level foreground hierarchy)
	TextPrimary   string // 主文本色
	TextSecondary string // 次要文本
	TextMuted     string // 弱化文本/占位符
	FGMostSubtle  string // 最弱文本（引导线轨道、超弱分隔符）
	FGGuide       string // 引导线（┊）颜色
	// Semantic
	Success string // 成功/完成
	Warning string // 警告/进行中
	Error   string // 错误
	Info    string // 信息/链接
	// UI
	Accent    string // 强调色（边框、焦点）
	AccentAlt string // 次要强调
	BarFilled string // 进度条填充
	BarEmpty  string // 进度条空
	Border    string // 边框
	TitleText string // 标题栏文字
	// Surface — background layers (darkest → lightest)
	Surface  string // 主背景（最深）
	BGPanel  string // 面板/卡片背景（比 Surface 略亮）
	Gradient string // 渐变辅助色（分隔线、装饰）
	// Semantic backgrounds (for diffs, tool output, status blocks)
	ErrorBg   string // 错误背景（diff 删除行）
	SuccessBg string // 成功背景（diff 插入行）
	WarningBg string // 警告背景
	InfoBg    string // 信息背景
	// Glamour 渲染色（Markdown 内容跟随主题）
	GDocumentText   string // 文档正文色
	GHeadingText    string // 标题色
	GCodeBlock      string // 代码块背景色
	GCodeText       string // 代码文本色
	GLinkText       string // 链接色
	GBlockQuote     string // 引用边框色
	GListItem       string // 列表标记色
	GHorizontalRule string // 水平分隔线色
	// --- 新增：扩展色板 (Phase 2: 5-level foreground + 5-level background + accent gradient) ---
	FGBright     string // 最亮前景（焦点高亮、交互反馈）
	BGHover      string // 悬停/选中行背景
	BGInset      string // 深色内嵌背景（代码块、思考框）
	BGOverlay    string // 覆盖层背景（最深）
	SuccessMuted string // 次要成功色
	WarningMuted string // 次要警告色
	ErrorMuted   string // 次要错误色
	InfoMuted    string // 次要信息色
	AccentStart  string // Accent 渐变起始色
	AccentEnd    string // Accent 渐变终止色
}

var (
	themeMidnight = cliTheme{
		// Inspired by Material Design Indigo — deep, professional, elegant
		TextPrimary:     "#e8eaed",
		TextSecondary:   "#9aa0a6",
		TextMuted:       "#5f6368",
		FGMostSubtle:    "#3c4043",
		FGGuide:         "#667eea",
		Success:         "#81c995",
		Warning:         "#fdd663",
		Error:           "#f28b82",
		Info:            "#8ab4f8",
		Accent:          "#8c9eff",
		AccentAlt:       "#c58af9",
		BarFilled:       "#8c9eff",
		BarEmpty:        "#292a3d",
		Border:          "#3c4043",
		TitleText:       "#e8eaed",
		Surface:         "#1e1f2e",
		BGPanel:         "#252736",
		Gradient:        "#667eea",
		ErrorBg:         "#332020",
		SuccessBg:       "#1a3325",
		WarningBg:       "#332d1a",
		InfoBg:          "#1a2533",
		GDocumentText:   "#e8eaed",
		GHeadingText:    "#8ab4f8",
		GCodeBlock:      "#1e1f2e",
		GCodeText:       "#c9d1d9",
		GLinkText:       "#8ab4f8",
		GBlockQuote:     "#c58af9",
		GListItem:       "#8c9eff",
		GHorizontalRule: "#667eea",
		FGBright:        "#ffffff",
		BGHover:         "#2d2f3e",
		BGInset:         "#171827",
		BGOverlay:       "#0d0e1a",
		SuccessMuted:    "#4a7a5a",
		WarningMuted:    "#8a7a3a",
		ErrorMuted:      "#7a4a42",
		InfoMuted:       "#4a5a7a",
		AccentStart:     "#8c9eff",
		AccentEnd:       "#667eea",
	}
	themeOcean = cliTheme{
		// Deep ocean blues with cyan highlights — calm, focused
		TextPrimary:     "#c3e8f0",
		TextSecondary:   "#6fb3c4",
		TextMuted:       "#3d6b7a",
		FGMostSubtle:    "#1e4976",
		FGGuide:         "#0ea5e9",
		Success:         "#5eead4",
		Warning:         "#fbbf24",
		Error:           "#fb7185",
		Info:            "#7dd3fc",
		Accent:          "#22d3ee",
		AccentAlt:       "#67e8f9",
		BarFilled:       "#22d3ee",
		BarEmpty:        "#0f2b3c",
		Border:          "#1e4976",
		TitleText:       "#ecfeff",
		Surface:         "#0c1929",
		BGPanel:         "#112233",
		Gradient:        "#0ea5e9",
		ErrorBg:         "#2a1a1f",
		SuccessBg:       "#1a2a25",
		WarningBg:       "#2a251a",
		InfoBg:          "#1a2230",
		GDocumentText:   "#c3e8f0",
		GHeadingText:    "#7dd3fc",
		GCodeBlock:      "#0c1929",
		GCodeText:       "#b0d8e8",
		GLinkText:       "#7dd3fc",
		GBlockQuote:     "#67e8f9",
		GListItem:       "#22d3ee",
		GHorizontalRule: "#0ea5e9",
		FGBright:        "#e0f4ff",
		BGHover:         "#1a3a4a",
		BGInset:         "#0a1a2a",
		BGOverlay:       "#050f18",
		SuccessMuted:    "#3a7a6a",
		WarningMuted:    "#8a7a3a",
		ErrorMuted:      "#7a4a42",
		InfoMuted:       "#3a5a7a",
		AccentStart:     "#22d3ee",
		AccentEnd:       "#0ea5e9",
	}
	themeForest = cliTheme{
		// Nordic forest greens — organic, natural, soothing
		TextPrimary:     "#d1e7dd",
		TextSecondary:   "#7dba8a",
		TextMuted:       "#4a6b50",
		FGMostSubtle:    "#1a4d2e",
		FGGuide:         "#059669",
		Success:         "#86efac",
		Warning:         "#fde68a",
		Error:           "#fca5a5",
		Info:            "#93c5fd",
		Accent:          "#4ade80",
		AccentAlt:       "#a3e635",
		BarFilled:       "#4ade80",
		BarEmpty:        "#0f2419",
		Border:          "#1a4d2e",
		TitleText:       "#dcfce7",
		Surface:         "#0a1f14",
		BGPanel:         "#0f2a1c",
		Gradient:        "#059669",
		ErrorBg:         "#2a1a1f",
		SuccessBg:       "#1a2a20",
		WarningBg:       "#2a251a",
		InfoBg:          "#1a2230",
		GDocumentText:   "#d1e7dd",
		GHeadingText:    "#93c5fd",
		GCodeBlock:      "#0a1f14",
		GCodeText:       "#b8d4c0",
		GLinkText:       "#93c5fd",
		GBlockQuote:     "#a3e635",
		GListItem:       "#4ade80",
		GHorizontalRule: "#059669",
		FGBright:        "#e0ffe0",
		BGHover:         "#1a3a2a",
		BGInset:         "#0a1a10",
		BGOverlay:       "#050f08",
		SuccessMuted:    "#3a6a4a",
		WarningMuted:    "#7a6a3a",
		ErrorMuted:      "#6a3a3a",
		InfoMuted:       "#3a5a4a",
		AccentStart:     "#4ade80",
		AccentEnd:       "#22c55e",
	}
	themeSunset = cliTheme{
		// Warm amber/coral palette — energetic, inviting
		TextPrimary:     "#fef3c7",
		TextSecondary:   "#fdba74",
		TextMuted:       "#78716c",
		FGMostSubtle:    "#44403c",
		FGGuide:         "#ea580c",
		Success:         "#fde68a",
		Warning:         "#fdba74",
		Error:           "#fca5a5",
		Info:            "#93c5fd",
		Accent:          "#fb923c",
		AccentAlt:       "#fbbf24",
		BarFilled:       "#fb923c",
		BarEmpty:        "#1c1917",
		Border:          "#44403c",
		TitleText:       "#fffbeb",
		Surface:         "#1c1917",
		BGPanel:         "#24201c",
		Gradient:        "#ea580c",
		ErrorBg:         "#2a1a1f",
		SuccessBg:       "#1d2a18",
		WarningBg:       "#2a221a",
		InfoBg:          "#1a2230",
		GDocumentText:   "#fef3c7",
		GHeadingText:    "#fbbf24",
		GCodeBlock:      "#1c1917",
		GCodeText:       "#fde68a",
		GLinkText:       "#93c5fd",
		GBlockQuote:     "#fbbf24",
		GListItem:       "#fb923c",
		GHorizontalRule: "#ea580c",
		FGBright:        "#fff5e0",
		BGHover:         "#3a2a1a",
		BGInset:         "#1a1008",
		BGOverlay:       "#0f0a05",
		SuccessMuted:    "#5a7a4a",
		WarningMuted:    "#8a7a3a",
		ErrorMuted:      "#7a4a3a",
		InfoMuted:       "#5a5a7a",
		AccentStart:     "#fb923c",
		AccentEnd:       "#ea580c",
	}
	themeRose = cliTheme{
		// Soft pink/magenta — modern, playful, expressive
		TextPrimary:     "#fce7f3",
		TextSecondary:   "#f9a8d4",
		TextMuted:       "#6b4c5e",
		FGMostSubtle:    "#4a2040",
		FGGuide:         "#db2777",
		Success:         "#fbcfe8",
		Warning:         "#fdba74",
		Error:           "#fca5a5",
		Info:            "#c4b5fd",
		Accent:          "#f472b6",
		AccentAlt:       "#c084fc",
		BarFilled:       "#f472b6",
		BarEmpty:        "#1f1522",
		Border:          "#4a2040",
		TitleText:       "#fdf2f8",
		Surface:         "#1a0f1e",
		BGPanel:         "#221525",
		Gradient:        "#db2777",
		ErrorBg:         "#2a1a20",
		SuccessBg:       "#1a2a1d",
		WarningBg:       "#2a221a",
		InfoBg:          "#1d1a30",
		GDocumentText:   "#fce7f3",
		GHeadingText:    "#c4b5fd",
		GCodeBlock:      "#1a0f1e",
		GCodeText:       "#f0b8d8",
		GLinkText:       "#c4b5fd",
		GBlockQuote:     "#c084fc",
		GListItem:       "#f472b6",
		GHorizontalRule: "#db2777",
		FGBright:        "#ffe0f0",
		BGHover:         "#3a1a2a",
		BGInset:         "#1a0a15",
		BGOverlay:       "#0f050a",
		SuccessMuted:    "#4a6a5a",
		WarningMuted:    "#8a6a3a",
		ErrorMuted:      "#7a3a4a",
		InfoMuted:       "#4a4a7a",
		AccentStart:     "#f472b6",
		AccentEnd:       "#db2777",
	}
	themeMono = cliTheme{
		// Clean grayscale with red accent — minimalist, hacker aesthetic
		TextPrimary:     "#c9d1d9",
		TextSecondary:   "#8b949e",
		TextMuted:       "#484f58",
		FGMostSubtle:    "#30363d",
		FGGuide:         "#8b949e",
		Success:         "#7ee787",
		Warning:         "#e3b341",
		Error:           "#ff7b72",
		Info:            "#79c0ff",
		Accent:          "#f0f6fc",
		AccentAlt:       "#8b949e",
		BarFilled:       "#f0f6fc",
		BarEmpty:        "#21262d",
		Border:          "#30363d",
		TitleText:       "#f0f6fc",
		Surface:         "#161b22",
		BGPanel:         "#1c2128",
		Gradient:        "#484f58",
		ErrorBg:         "#2d1a1f",
		SuccessBg:       "#1d2d20",
		WarningBg:       "#2d281a",
		InfoBg:          "#1a2530",
		GDocumentText:   "#c9d1d9",
		GHeadingText:    "#79c0ff",
		GCodeBlock:      "#161b22",
		GCodeText:       "#c9d1d9",
		GLinkText:       "#79c0ff",
		GBlockQuote:     "#8b949e",
		GListItem:       "#f0f6fc",
		GHorizontalRule: "#484f58",
		FGBright:        "#ffffff",
		BGHover:         "#2a2a2a",
		BGInset:         "#111111",
		BGOverlay:       "#080808",
		SuccessMuted:    "#555555",
		WarningMuted:    "#666655",
		ErrorMuted:      "#665555",
		InfoMuted:       "#555566",
		AccentStart:     "#f0f6fc",
		AccentEnd:       "#484f58",
	}
	themeNord = cliTheme{
		// Nord color scheme — arctic, blue-ish, muted elegance
		TextPrimary:     "#d8dee9",
		TextSecondary:   "#81a1c1",
		TextMuted:       "#4c566a",
		FGMostSubtle:    "#3b4252",
		FGGuide:         "#5e81ac",
		Success:         "#a3be8c",
		Warning:         "#ebcb8b",
		Error:           "#bf616a",
		Info:            "#81a1c1",
		Accent:          "#88c0d0",
		AccentAlt:       "#b48ead",
		BarFilled:       "#88c0d0",
		BarEmpty:        "#3b4252",
		Border:          "#434c5e",
		TitleText:       "#eceff4",
		Surface:         "#2e3440",
		BGPanel:         "#353b49",
		Gradient:        "#5e81ac",
		ErrorBg:         "#382525",
		SuccessBg:       "#253825",
		WarningBg:       "#383525",
		InfoBg:          "#202838",
		GDocumentText:   "#d8dee9",
		GHeadingText:    "#88c0d0",
		GCodeBlock:      "#2e3440",
		GCodeText:       "#d8dee9",
		GLinkText:       "#81a1c1",
		GBlockQuote:     "#b48ead",
		GListItem:       "#88c0d0",
		GHorizontalRule: "#5e81ac",
		FGBright:        "#eceff4",
		BGHover:         "#3b4252",
		BGInset:         "#242933",
		BGOverlay:       "#191d24",
		SuccessMuted:    "#4c6a5a",
		WarningMuted:    "#8a7a4a",
		ErrorMuted:      "#6a4a4a",
		InfoMuted:       "#4a5a7a",
		AccentStart:     "#88c0d0",
		AccentEnd:       "#5e81ac",
	}
	themeDracula = cliTheme{
		// Dracula — deep purple theme with vivid contrast
		TextPrimary:     "#f8f8f2",
		TextSecondary:   "#bd93f9",
		TextMuted:       "#6272a4",
		FGMostSubtle:    "#44475a",
		FGGuide:         "#6272a4",
		Success:         "#50fa7b",
		Warning:         "#f1fa8c",
		Error:           "#ff5555",
		Info:            "#8be9fd",
		Accent:          "#bd93f9",
		AccentAlt:       "#ff79c6",
		BarFilled:       "#bd93f9",
		BarEmpty:        "#21222c",
		Border:          "#44475a",
		TitleText:       "#f8f8f2",
		Surface:         "#1e1f29",
		BGPanel:         "#282a36",
		Gradient:        "#6272a4",
		ErrorBg:         "#382525",
		SuccessBg:       "#1d3825",
		WarningBg:       "#383525",
		InfoBg:          "#202538",
		GDocumentText:   "#f8f8f2",
		GHeadingText:    "#bd93f9",
		GCodeBlock:      "#1e1f29",
		GCodeText:       "#f8f8f2",
		GLinkText:       "#8be9fd",
		GBlockQuote:     "#ff79c6",
		GListItem:       "#bd93f9",
		GHorizontalRule: "#6272a4",
		FGBright:        "#f8f8f2",
		BGHover:         "#343746",
		BGInset:         "#1a1b26",
		BGOverlay:       "#0e0f16",
		SuccessMuted:    "#3a6a5a",
		WarningMuted:    "#7a6a3a",
		ErrorMuted:      "#6a3a4a",
		InfoMuted:       "#4a4a7a",
		AccentStart:     "#bd93f9",
		AccentEnd:       "#6272a4",
	}

	themeCatppuccin = cliTheme{
		// Catppuccin Mocha — soft pastel dark theme, community favorite
		TextPrimary:     "#cdd6f4", // Text
		TextSecondary:   "#a6adc8", // Overlay0
		TextMuted:       "#585b70", // Overlay2
		FGMostSubtle:    "#45475a", // Surface1
		FGGuide:         "#89b4fa", // Blue
		Success:         "#a6e3a1", // Green
		Warning:         "#f9e2af", // Yellow
		Error:           "#f38ba8", // Red
		Info:            "#89b4fa", // Blue
		Accent:          "#cba6f7", // Mauve
		AccentAlt:       "#f5c2e7", // Pink
		BarFilled:       "#cba6f7", // Mauve
		BarEmpty:        "#313244", // Surface0
		Border:          "#45475a", // Surface1
		TitleText:       "#cdd6f4", // Text
		Surface:         "#1e1e2e", // Base
		BGPanel:         "#1e1e2e", // same as Surface
		Gradient:        "#89b4fa", // Blue
		ErrorBg:         "#3a2626",
		SuccessBg:       "#263a2a",
		WarningBg:       "#3a3626",
		InfoBg:          "#24283a",
		GDocumentText:   "#cdd6f4", // Text
		GHeadingText:    "#cba6f7", // Mauve
		GCodeBlock:      "#181825", // Mantle
		GCodeText:       "#cdd6f4", // Text
		GLinkText:       "#89b4fa", // Blue
		GBlockQuote:     "#f5c2e7", // Pink
		GListItem:       "#cba6f7", // Mauve
		GHorizontalRule: "#89b4fa", // Blue
		FGBright:        "#cdd6f4",
		BGHover:         "#313244",
		BGInset:         "#181825",
		BGOverlay:       "#11111b",
		SuccessMuted:    "#4a6a5a",
		WarningMuted:    "#7a6a3a",
		ErrorMuted:      "#6a4a4a",
		InfoMuted:       "#4a5a7a",
		AccentStart:     "#cba6f7",
		AccentEnd:       "#89b4fa",
	}

	themeRegistry = map[string]*cliTheme{
		"midnight":   &themeMidnight,
		"ocean":      &themeOcean,
		"forest":     &themeForest,
		"sunset":     &themeSunset,
		"rose":       &themeRose,
		"mono":       &themeMono,
		"nord":       &themeNord,
		"dracula":    &themeDracula,
		"catppuccin": &themeCatppuccin,
	}

	currentTheme = &themeMidnight
)

// ---------------------------------------------------------------------------
// §20 样式缓存系统 — 避免每帧重建 lipgloss.Style（第 7 轮重构）
// ---------------------------------------------------------------------------
// 每个 View() 调用创建 200+ 个 lipgloss.NewStyle() → 改为缓存，只在主题/resize 时重建。

type cliStyles struct {
	TitleBar         lipgloss.Style
	TitleText        lipgloss.Style
	ReadyStatus      lipgloss.Style
	ThinkingSt       lipgloss.Style
	Progress         lipgloss.Style
	Tool             lipgloss.Style
	Separator        lipgloss.Style
	InputBox         lipgloss.Style
	Time             lipgloss.Style
	UserLabel        lipgloss.Style
	AssistLabel      lipgloss.Style
	StreamingLabel   lipgloss.Style
	SystemMsg        lipgloss.Style
	ErrorMsg         lipgloss.Style
	ToolSummary      lipgloss.Style
	ToolHeader       lipgloss.Style
	ToolItem         lipgloss.Style
	ToolErrorItem    lipgloss.Style
	ToolThinking     lipgloss.Style
	ToolHint         lipgloss.Style
	ProgressHeader   lipgloss.Style
	ProgressIter     lipgloss.Style
	ProgressThinking lipgloss.Style
	ProgressDone     lipgloss.Style
	ProgressRunning  lipgloss.Style
	ProgressError    lipgloss.Style
	ProgressElapsed  lipgloss.Style
	ProgressIndent   lipgloss.Style
	ProgressDim      lipgloss.Style
	ProgressBlock    lipgloss.Style
	Accent           lipgloss.Style
	TextMutedSt      lipgloss.Style
	TextSecondarySt  lipgloss.Style
	WarningSt        lipgloss.Style
	InfoSt           lipgloss.Style
	TokenUsage       lipgloss.Style
	Footer           lipgloss.Style
	ToastBg          lipgloss.Style
	ToastText        lipgloss.Style
	TodoLabel        lipgloss.Style
	TodoFilled       lipgloss.Style
	TodoEmpty        lipgloss.Style
	TodoDone         lipgloss.Style
	TodoPending      lipgloss.Style
	PanelBox         lipgloss.Style
	PanelHeader      lipgloss.Style
	PanelCursor      lipgloss.Style
	PanelDesc        lipgloss.Style
	PanelHint        lipgloss.Style
	PanelDivider     lipgloss.Style
	PanelEmpty       lipgloss.Style
	FileCompDir      lipgloss.Style
	FileCompFile     lipgloss.Style
	FileCompSel      lipgloss.Style
	HelpTitle        lipgloss.Style
	HelpCmd          lipgloss.Style
	HelpDesc         lipgloss.Style
	HelpGroup        lipgloss.Style
	HelpKey          lipgloss.Style
	HelpPanel        lipgloss.Style
	// --- completions ---
	CompSelected   lipgloss.Style
	CompItem       lipgloss.Style
	CompHint       lipgloss.Style
	CompHintBorder lipgloss.Style
	// --- view helpers ---
	LineHint      lipgloss.Style
	WarningBold   lipgloss.Style
	PlaceholderSt lipgloss.Style
	// --- splash ---
	VersionSt lipgloss.Style
	// --- toast ---
	ToastIcon lipgloss.Style
	// --- message render ---
	UserDotSep     lipgloss.Style
	UserHeader     lipgloss.Style
	UserContent    lipgloss.Style
	AssistantGuide lipgloss.Style
	StreamCursor   lipgloss.Style
	// --- guide lines (message hierarchy) ---
	GuideSt    lipgloss.Style // 活跃引导线（┊）
	DimGuideSt lipgloss.Style // 暗淡引导线（┆）
	// --- thinking box ---
	ThinkingBox lipgloss.Style // 推理内容折叠面板
	// --- settings panel ---
	SettingsDivider lipgloss.Style
	SettingsCat     lipgloss.Style
	SettingsSelBg   lipgloss.Style
	// --- textarea presets ---
	TACursor         lipgloss.Style
	TABase           lipgloss.Style
	TAPlaceholder    lipgloss.Style
	TACursorLine     lipgloss.Style
	TALineNumber     lipgloss.Style
	TAEndOfBuffer    lipgloss.Style
	TABlurredCursor  lipgloss.Style
	TABlurredLineNum lipgloss.Style
	TABlurredEOB     lipgloss.Style
	TABlurredText    lipgloss.Style
	TIPrompt         lipgloss.Style
	TIText           lipgloss.Style
	TICursor         lipgloss.Style
	TIPlaceholder    lipgloss.Style
	// --- key hints (footer) ---
	KeyLabelSt       lipgloss.Style
	KeyDescSt        lipgloss.Style
	ProgressGradient lipgloss.Style
	ProgressGlow     lipgloss.Style

	// --- search (§21) ---
	SearchBar       lipgloss.Style
	SearchIndicator lipgloss.Style

	// --- plugin state ---
	PluginActive     lipgloss.Style
	PluginError      lipgloss.Style
	PluginDiscovered lipgloss.Style
	PluginInactive   lipgloss.Style
	PluginTransition lipgloss.Style
	// --- diamond signature system ---
	DiamondMark  lipgloss.Style // ◈ 品牌标记
	DiamondFocus lipgloss.Style // ◆ 焦点指示
	DiamondDim   lipgloss.Style // ◇ 非焦点
	DotLine      lipgloss.Style // ┈ 点线分隔
	GuideActive  lipgloss.Style // ┊ 虚线引导线（活跃）
	GuideDim     lipgloss.Style // ┆ 虚线引导线（暗淡）
	// --- panel border variants ---
	PanelBorderSettings lipgloss.Style // Settings: 左粗线风格
	PanelBorderSessions lipgloss.Style // Sessions: 信息色边框
	PanelBorderDanger   lipgloss.Style // Danger/Approval: 错误色边框
	PanelBorderRewind   lipgloss.Style // Rewind: 暗淡边框
	// --- footer hint zones ---
	FooterHintLabel lipgloss.Style // 可点击 hint 的按键标签
	FooterHintHover lipgloss.Style // hint hover 态（下划线）
	// --- sidebar ---
	SidebarBg      lipgloss.Style
	SidebarSection lipgloss.Style
	SidebarItem    lipgloss.Style
	SidebarActive  lipgloss.Style
	SidebarHeader  lipgloss.Style
	SidebarBusy    lipgloss.Style
	SidebarDivider lipgloss.Style
	// --- additional surfaces ---
	BGHoverSt lipgloss.Style // 选中/悬停行背景
	BGInsetSt lipgloss.Style // 深色内嵌背景
	// toolDisplayInfo
}

func buildStyles(width int) cliStyles {
	t := currentTheme

	// On low-color terminals (ANSI 16-color, e.g. Linux console / VT),
	// dark muted colors like "#5f6368" get downgraded to black by
	// colorprofile.Convert16, becoming invisible on dark backgrounds.
	// Override these theme colors with ANSI-safe equivalents that are
	// guaranteed visible.
	if terminalProfile == colorprofile.ANSI {
		t.TextMuted = "7"     // light gray — clearly visible on black bg
		t.TextSecondary = "7" // light gray
		t.FGMostSubtle = "8"  // bright black (dark gray) — faint but visible
		t.FGGuide = "8"       // bright black for guide lines
	}

	c := func(s string) color.Color { return lipgloss.Color(s) }
	cw := width - 4
	if cw < 10 {
		cw = 10
	}
	return cliStyles{
		TitleBar:         lipgloss.NewStyle().Background(c(t.Border)).Foreground(c(t.TitleText)).Bold(true).Width(width),
		TitleText:        lipgloss.NewStyle(),
		ReadyStatus:      lipgloss.NewStyle().Foreground(c(t.Success)).Bold(true).Padding(0, 1),
		ThinkingSt:       lipgloss.NewStyle().Foreground(c(t.Warning)).Padding(0, 1),
		Progress:         lipgloss.NewStyle().Foreground(c(t.Warning)),
		Tool:             lipgloss.NewStyle().Foreground(c(t.Info)),
		Separator:        lipgloss.NewStyle().Foreground(c(t.Gradient)).Background(c(t.Surface)),
		InputBox:         lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Padding(0, 1).Width(width - 4),
		Time:             lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Faint(true),
		UserLabel:        lipgloss.NewStyle().Foreground(c(t.Info)).Bold(true),
		AssistLabel:      lipgloss.NewStyle().Foreground(c(t.Success)).Bold(true),
		StreamingLabel:   lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true),
		SystemMsg:        lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Italic(true).Width(width).Align(lipgloss.Center),
		ErrorMsg:         lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Error)).Foreground(c(t.Error)).Bold(true).Padding(0, 1).Width(cw),
		ToolSummary:      lipgloss.NewStyle().Foreground(c(t.TextPrimary)).Padding(0, 1).Width(cw).Align(lipgloss.Left),
		ToolHeader:       lipgloss.NewStyle().Foreground(c(t.Info)).Bold(true),
		ToolItem:         lipgloss.NewStyle().Foreground(c(t.Success)),
		ToolErrorItem:    lipgloss.NewStyle().Foreground(c(t.Error)),
		ToolThinking:     lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Italic(true),
		ToolHint:         lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		ProgressHeader:   lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
		ProgressIter:     lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Bold(true),
		ProgressThinking: lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Italic(true),
		ProgressDone:     lipgloss.NewStyle().Foreground(c(t.Success)),
		ProgressRunning:  lipgloss.NewStyle().Foreground(c(t.Warning)),
		ProgressError:    lipgloss.NewStyle().Foreground(c(t.Error)),
		ProgressElapsed:  lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Faint(true),
		ProgressIndent:   lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		ProgressDim:      lipgloss.NewStyle().Foreground(c(t.FGMostSubtle)),
		ProgressBlock:    lipgloss.NewStyle().Padding(0, 1).Width(cw),
		Accent:           lipgloss.NewStyle().Foreground(c(t.Accent)),
		TextMutedSt:      lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		TextSecondarySt:  lipgloss.NewStyle().Foreground(c(t.TextSecondary)),
		WarningSt:        lipgloss.NewStyle().Foreground(c(t.Warning)),
		InfoSt:           lipgloss.NewStyle().Foreground(c(t.Info)),
		TokenUsage:       lipgloss.NewStyle().Foreground(c(t.TextMuted)).Faint(true),
		Footer:           lipgloss.NewStyle().Foreground(c(t.TextSecondary)),
		ToastBg:          lipgloss.NewStyle().Width(width).Padding(0, 1),
		ToastText:        lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		TodoLabel:        lipgloss.NewStyle().Foreground(c(t.TextSecondary)),
		TodoFilled:       lipgloss.NewStyle().Foreground(c(t.BarFilled)),
		TodoEmpty:        lipgloss.NewStyle().Foreground(c(t.BarEmpty)),
		TodoDone:         lipgloss.NewStyle().Foreground(c(t.Success)),
		TodoPending:      lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		PanelBox:         lipgloss.NewStyle().Width(width).AlignHorizontal(lipgloss.Left).Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Padding(0, 1),
		PanelHeader:      lipgloss.NewStyle().Foreground(c(t.Info)).Bold(true),
		PanelCursor:      lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true),
		PanelDesc:        lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Faint(true),
		PanelHint:        lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		PanelDivider:     lipgloss.NewStyle().Foreground(c(t.Border)).Faint(true),
		PanelEmpty:       lipgloss.NewStyle().Foreground(c(t.TextMuted)).Faint(true).Width(width - 8).Align(lipgloss.Center),
		FileCompDir:      lipgloss.NewStyle().Foreground(c(t.Info)),
		FileCompFile:     lipgloss.NewStyle().Foreground(c(t.Info)),
		FileCompSel:      lipgloss.NewStyle().Foreground(c(t.Info)).Bold(true).Underline(true),
		HelpTitle:        lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
		HelpCmd:          lipgloss.NewStyle().Foreground(c(t.Info)).Bold(true).Width(12),
		HelpDesc:         lipgloss.NewStyle().Foreground(c(t.TextSecondary)),
		HelpGroup:        lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true),
		HelpKey:          lipgloss.NewStyle().Foreground(c(t.TextPrimary)).Bold(true).Width(14),
		HelpPanel:        lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Padding(0, 1).Width(cw),
		// --- completions ---
		CompSelected:   lipgloss.NewStyle().Bold(true).Underline(true).Foreground(c(t.Success)),
		CompItem:       lipgloss.NewStyle().Foreground(c(t.Success)),
		CompHint:       lipgloss.NewStyle().Padding(0, 1),
		CompHintBorder: lipgloss.NewStyle().Foreground(c(t.Success)).Padding(0, 1),
		// --- view helpers ---
		LineHint:      lipgloss.NewStyle().Foreground(c(t.TextMuted)).Faint(true),
		WarningBold:   lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true).Padding(0, 1),
		PlaceholderSt: lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		// --- splash ---
		VersionSt: lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Italic(true),
		// --- toast ---
		ToastIcon: lipgloss.NewStyle().Foreground(c(t.Success)).Bold(true),
		// --- message render ---
		UserDotSep:     lipgloss.NewStyle().Foreground(c(t.Gradient)),
		UserHeader:     lipgloss.NewStyle().Foreground(c(t.TextSecondary)),
		UserContent:    lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		AssistantGuide: lipgloss.NewStyle().Foreground(c(t.Gradient)),
		StreamCursor:   lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true),
		// --- guide lines (message hierarchy) ---
		GuideSt:    lipgloss.NewStyle().Foreground(c(t.FGGuide)),      // ┊ 虚线引导线（活跃）
		DimGuideSt: lipgloss.NewStyle().Foreground(c(t.FGMostSubtle)), // ┆ 虚线引导线（暗淡）
		// --- thinking box ---
		ThinkingBox: lipgloss.NewStyle().Background(c(t.BGInset)).Padding(0, 1),
		// --- settings panel ---
		SettingsDivider: lipgloss.NewStyle().Foreground(c(t.Border)).Faint(true),
		SettingsCat:     lipgloss.NewStyle().Foreground(c(t.AccentAlt)).Bold(true),
		SettingsSelBg:   lipgloss.NewStyle(),
		// --- textarea presets ---
		TACursor:         lipgloss.NewStyle().Foreground(c(t.Info)),
		TABase:           lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		TAPlaceholder:    lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		TACursorLine:     lipgloss.NewStyle(),
		TALineNumber:     lipgloss.NewStyle(),
		TAEndOfBuffer:    lipgloss.NewStyle(),
		TABlurredCursor:  lipgloss.NewStyle(),
		TABlurredLineNum: lipgloss.NewStyle(),
		TABlurredEOB:     lipgloss.NewStyle(),
		TABlurredText:    lipgloss.NewStyle(),
		TIPrompt:         lipgloss.NewStyle(),
		TIText:           lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		TICursor:         lipgloss.NewStyle().Foreground(c(t.Info)),
		TIPlaceholder:    lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		// --- key hints (footer) ---
		KeyLabelSt:       lipgloss.NewStyle().Foreground(c(t.TextMuted)).Bold(true).Underline(true),
		KeyDescSt:        lipgloss.NewStyle().Foreground(c(t.TextSecondary)),
		ProgressGradient: lipgloss.NewStyle().Foreground(c(t.BarFilled)).Bold(true),
		ProgressGlow:     lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
		// --- search (§21) ---
		SearchBar:       lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Info)).Padding(0, 1).Width(width - 4),
		SearchIndicator: lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true),
		// --- plugin state ---
		PluginActive:     lipgloss.NewStyle().Foreground(c(t.Success)),
		PluginError:      lipgloss.NewStyle().Foreground(c(t.Error)),
		PluginDiscovered: lipgloss.NewStyle().Foreground(c(t.Warning)),
		PluginInactive:   lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		PluginTransition: lipgloss.NewStyle().Foreground(c(t.Warning)).Italic(true),
		// --- diamond signature system ---
		DiamondMark:  lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
		DiamondFocus: lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
		DiamondDim:   lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		DotLine:      lipgloss.NewStyle().Foreground(c(t.FGMostSubtle)),
		GuideActive:  lipgloss.NewStyle().Foreground(c(t.FGGuide)),
		GuideDim:     lipgloss.NewStyle().Foreground(c(t.FGMostSubtle)),
		// --- panel border variants ---
		PanelBorderSettings: lipgloss.NewStyle().Width(width).AlignHorizontal(lipgloss.Left).BorderLeft(true).BorderStyle(lipgloss.Border{Left: "▎"}).BorderForeground(c(t.Accent)).Padding(0, 1),
		PanelBorderSessions: lipgloss.NewStyle().Width(width).AlignHorizontal(lipgloss.Left).Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Info)).Padding(0, 1),
		PanelBorderDanger:   lipgloss.NewStyle().Width(width).AlignHorizontal(lipgloss.Left).Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Error)).Padding(0, 1),
		PanelBorderRewind:   lipgloss.NewStyle().Width(width).AlignHorizontal(lipgloss.Left).Border(lipgloss.RoundedBorder()).BorderForeground(c(t.FGMostSubtle)).Padding(0, 1),
		// --- footer hint zones ---
		FooterHintLabel: lipgloss.NewStyle().Foreground(c(t.TextMuted)).Bold(true).Underline(true),
		FooterHintHover: lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true).Underline(true),
		// --- sidebar ---
		SidebarBg:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Padding(0, 1),
		SidebarSection: lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Bold(true),
		SidebarItem:    lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		SidebarActive:  lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
		SidebarHeader:  lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
		SidebarBusy:    lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true),
		SidebarDivider: lipgloss.NewStyle().Foreground(c(t.Border)),
		// --- additional surfaces ---
		BGHoverSt: lipgloss.NewStyle().Background(c(t.BGHover)),
		BGInsetSt: lipgloss.NewStyle().Background(c(t.BGInset)),
	}
}

// hexToRGB converts a hex color string (e.g. "#ff0044" or "ff0044") to RGB components.
func hexToRGB(hex string) (uint8, uint8, uint8) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 128, 128, 128
	}
	r, _ := strconv.ParseUint(hex[0:2], 16, 8)
	g, _ := strconv.ParseUint(hex[2:4], 16, 8)
	b, _ := strconv.ParseUint(hex[4:6], 16, 8)
	return uint8(r), uint8(g), uint8(b)
}

// applyTAStyles 将缓存样式应用到 textarea 组件
func applyTAStyles(ta *textarea.Model, s *cliStyles) {
	styles := ta.Styles()
	styles.Cursor.Color = s.TACursor.GetForeground()
	styles.Cursor.Blink = false // 禁用光标闪烁：避免 IME 输入时字符因闪烁竞态而视觉消失
	styles.Focused.Base = s.TABase
	styles.Focused.Placeholder = s.TAPlaceholder
	styles.Focused.CursorLine = s.TACursorLine
	styles.Focused.LineNumber = s.TALineNumber
	styles.Focused.EndOfBuffer = s.TAEndOfBuffer
	styles.Blurred.CursorLine = s.TABlurredCursor
	styles.Blurred.LineNumber = s.TABlurredLineNum
	styles.Blurred.EndOfBuffer = s.TABlurredEOB
	styles.Blurred.Text = s.TABlurredText
	ta.SetStyles(styles)
}

// newPanelTextArea creates a configured textarea for panel editing.
func (m *cliModel) newPanelTextArea(value string, width, height int) textarea.Model {
	ta := textarea.New()
	ta.Prompt = "  "
	applyTAStyles(&ta, &m.styles)
	ta.CharLimit = 0
	ta.SetWidth(m.panelWidth(width))
	ta.SetHeight(height)
	ta.SetValue(value)
	ta.CursorEnd()
	ta.Focus()
	return ta
}

// themeChangeCh signals the running model to rebuild styles after a theme change.
var themeChangeCh = make(chan struct{}, 1)

// modelsLoadErrorCh carries model list API load errors from LLM goroutines to the tea Update loop.
var modelsLoadErrorCh = make(chan error, 1)

// ModelsLoadErrorCh returns the channel for model list load errors.
func ModelsLoadErrorCh() chan<- error { return modelsLoadErrorCh }

// currentThemeName tracks the active theme name for themeChangeCh handler.
var currentThemeName string

// currentThemeMu protects currentTheme writes from external goroutines
// (e.g. settings handler calling ApplyTheme during View() rendering).
var currentThemeMu sync.Mutex

// setTheme 更新 currentTheme 但不发 channel 通知。
// 供 applyThemeAndRebuild 等需要同步完成所有工作的调用方使用，
// 避免后续 Update 周期再触发一次冗余的 fullRebuild。
func setTheme(name string) {
	currentThemeMu.Lock()
	defer currentThemeMu.Unlock()
	if t, ok := themeRegistry[name]; ok {
		currentTheme = t
		currentThemeName = name
		return
	}
	// Fallback: try loading external theme from ~/.xbot/themes/<name>.json
	if t := loadExternalTheme(name); t != nil {
		currentTheme = t
		currentThemeName = name
		return
	}
	currentTheme = &themeMidnight
	currentThemeName = "midnight"
}

var externalThemesMu sync.Mutex
var externalThemes = map[string]*cliTheme{}

// loadExternalTheme loads a theme JSON from ~/.xbot/themes/<name>.json.
// Caches loaded themes for subsequent lookups.
func loadExternalTheme(name string) *cliTheme {
	externalThemesMu.Lock()
	if t, ok := externalThemes[name]; ok {
		externalThemesMu.Unlock()
		return t
	}
	externalThemesMu.Unlock()

	path := filepath.Join(filepath.Dir(config.ConfigFilePath()), "themes", name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var ext externalThemeJSON
	if err := json.Unmarshal(data, &ext); err != nil {
		return nil
	}
	t := &cliTheme{
		TextPrimary:     or(ext.TextPrimary, "#e8eaed"),
		TextSecondary:   or(ext.TextSecondary, "#9aa0a6"),
		TextMuted:       or(ext.TextMuted, "#5f6368"),
		FGMostSubtle:    or(ext.FGMostSubtle, "#3c4043"),
		FGGuide:         or(ext.FGGuide, "#667eea"),
		Success:         or(ext.Success, "#81c995"),
		Warning:         or(ext.Warning, "#fdd663"),
		Error:           or(ext.Error, "#f28b82"),
		Info:            or(ext.Info, "#8ab4f8"),
		Accent:          or(ext.Accent, "#8c9eff"),
		AccentAlt:       or(ext.AccentAlt, "#c58af9"),
		BarFilled:       or(ext.BarFilled, "#8c9eff"),
		BarEmpty:        or(ext.BarEmpty, "#292a3d"),
		Border:          or(ext.Border, "#3c4043"),
		TitleText:       or(ext.TitleText, "#e8eaed"),
		Surface:         or(ext.Surface, "#1e1f2e"),
		BGPanel:         or(ext.BGPanel, "#252736"),
		Gradient:        or(ext.Gradient, "#667eea"),
		ErrorBg:         or(ext.ErrorBg, "#332020"),
		SuccessBg:       or(ext.SuccessBg, "#1a3325"),
		WarningBg:       or(ext.WarningBg, "#332d1a"),
		InfoBg:          or(ext.InfoBg, "#1a2533"),
		GDocumentText:   or(ext.GDocumentText, "#e8eaed"),
		GHeadingText:    or(ext.GHeadingText, "#8ab4f8"),
		GCodeBlock:      or(ext.GCodeBlock, "#1e1f2e"),
		GCodeText:       or(ext.GCodeText, "#c9d1d9"),
		GLinkText:       or(ext.GLinkText, "#8ab4f8"),
		GBlockQuote:     or(ext.GBlockQuote, "#8c9eff"),
		GListItem:       or(ext.GListItem, "#8ab4f8"),
		GHorizontalRule: or(ext.GHorizontalRule, "#3c4043"),
		FGBright:        or(ext.FGBright, "#ffffff"),
		BGHover:         or(ext.BGHover, "#2d2f3e"),
		BGInset:         or(ext.BGInset, "#161722"),
		BGOverlay:       or(ext.BGOverlay, "#0d0e1a"),
		SuccessMuted:    or(ext.SuccessMuted, "#4a7d5f"),
		WarningMuted:    or(ext.WarningMuted, "#8a7a3a"),
		ErrorMuted:      or(ext.ErrorMuted, "#8a4d4d"),
		InfoMuted:       or(ext.InfoMuted, "#4a6d8a"),
		AccentStart:     or(ext.AccentStart, ext.Accent),
		AccentEnd:       or(ext.AccentEnd, ext.AccentAlt),
	}

	externalThemesMu.Lock()
	externalThemes[name] = t
	externalThemesMu.Unlock()
	return t
}

var pluginThemesMu sync.Mutex

// LoadPluginTheme parses a theme JSON (same format as external themes) and
// registers it in the theme registry. This is called when plugins contribute
// themes via ContributeTheme. Thread-safe.
func LoadPluginTheme(id string, data []byte) error {
	var ext externalThemeJSON
	if err := json.Unmarshal(data, &ext); err != nil {
		return fmt.Errorf("plugin theme %q: %w", id, err)
	}
	t := &cliTheme{
		TextPrimary:     or(ext.TextPrimary, "#e8eaed"),
		TextSecondary:   or(ext.TextSecondary, "#9aa0a6"),
		TextMuted:       or(ext.TextMuted, "#5f6368"),
		FGMostSubtle:    or(ext.FGMostSubtle, "#3c4043"),
		FGGuide:         or(ext.FGGuide, "#667eea"),
		Success:         or(ext.Success, "#81c995"),
		Warning:         or(ext.Warning, "#fdd663"),
		Error:           or(ext.Error, "#f28b82"),
		Info:            or(ext.Info, "#8ab4f8"),
		Accent:          or(ext.Accent, "#8c9eff"),
		AccentAlt:       or(ext.AccentAlt, "#c58af9"),
		BarFilled:       or(ext.BarFilled, "#8c9eff"),
		BarEmpty:        or(ext.BarEmpty, "#292a3d"),
		Border:          or(ext.Border, "#3c4043"),
		TitleText:       or(ext.TitleText, "#e8eaed"),
		Surface:         or(ext.Surface, "#1e1f2e"),
		BGPanel:         or(ext.BGPanel, "#252736"),
		Gradient:        or(ext.Gradient, "#667eea"),
		ErrorBg:         or(ext.ErrorBg, "#332020"),
		SuccessBg:       or(ext.SuccessBg, "#1a3325"),
		WarningBg:       or(ext.WarningBg, "#332d1a"),
		InfoBg:          or(ext.InfoBg, "#1a2533"),
		GDocumentText:   or(ext.GDocumentText, "#e8eaed"),
		GHeadingText:    or(ext.GHeadingText, "#8ab4f8"),
		GCodeBlock:      or(ext.GCodeBlock, "#1e1f2e"),
		GCodeText:       or(ext.GCodeText, "#c9d1d9"),
		GLinkText:       or(ext.GLinkText, "#8ab4f8"),
		GBlockQuote:     or(ext.GBlockQuote, "#8c9eff"),
		GListItem:       or(ext.GListItem, "#8ab4f8"),
		GHorizontalRule: or(ext.GHorizontalRule, "#3c4043"),
		FGBright:        or(ext.FGBright, "#ffffff"),
		BGHover:         or(ext.BGHover, "#2d2f3e"),
		BGInset:         or(ext.BGInset, "#161722"),
		BGOverlay:       or(ext.BGOverlay, "#0d0e1a"),
		SuccessMuted:    or(ext.SuccessMuted, "#4a7d5f"),
		WarningMuted:    or(ext.WarningMuted, "#8a7a3a"),
		ErrorMuted:      or(ext.ErrorMuted, "#8a4d4d"),
		InfoMuted:       or(ext.InfoMuted, "#4a6d8a"),
		AccentStart:     or(ext.AccentStart, ext.Accent),
		AccentEnd:       or(ext.AccentEnd, ext.AccentAlt),
	}

	pluginThemesMu.Lock()
	themeRegistry[id] = t
	pluginThemesMu.Unlock()
	return nil
}

// externalThemeJSON is the JSON-serializable theme format for external files.
// Users only need to specify colors they want to override; defaults fill the rest.
type externalThemeJSON struct {
	TextPrimary     string `json:"text_primary"`
	TextSecondary   string `json:"text_secondary"`
	TextMuted       string `json:"text_muted"`
	FGMostSubtle    string `json:"fg_most_subtle"`
	FGGuide         string `json:"fg_guide"`
	Success         string `json:"success"`
	Warning         string `json:"warning"`
	Error           string `json:"error"`
	Info            string `json:"info"`
	Accent          string `json:"accent"`
	AccentAlt       string `json:"accent_alt"`
	BarFilled       string `json:"bar_filled"`
	BarEmpty        string `json:"bar_empty"`
	Border          string `json:"border"`
	TitleText       string `json:"title_text"`
	Surface         string `json:"surface"`
	BGPanel         string `json:"bg_panel"`
	Gradient        string `json:"gradient"`
	ErrorBg         string `json:"error_bg"`
	SuccessBg       string `json:"success_bg"`
	WarningBg       string `json:"warning_bg"`
	InfoBg          string `json:"info_bg"`
	GDocumentText   string `json:"gdocument_text"`
	GHeadingText    string `json:"gheading_text"`
	GCodeBlock      string `json:"gcode_block"`
	GCodeText       string `json:"gcode_text"`
	GLinkText       string `json:"glink_text"`
	GBlockQuote     string `json:"gblock_quote"`
	GListItem       string `json:"glist_item"`
	GHorizontalRule string `json:"ghorizontal_rule"`
	FGBright        string `json:"fg_bright"`
	BGHover         string `json:"bg_hover"`
	BGInset         string `json:"bg_inset"`
	BGOverlay       string `json:"bg_overlay"`
	SuccessMuted    string `json:"success_muted"`
	WarningMuted    string `json:"warning_muted"`
	ErrorMuted      string `json:"error_muted"`
	InfoMuted       string `json:"info_muted"`
	AccentStart     string `json:"accent_start"`
	AccentEnd       string `json:"accent_end"`
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func ApplyTheme(name string) {
	setTheme(name)
	// Non-blocking send; if model is already processing a theme change, skip.
	select {
	case themeChangeCh <- struct{}{}:
	default:
	}
}

// ThemeNames returns the list of available theme names (built-in + external).
func ThemeNames() []string {
	names := make([]string, 0, len(themeRegistry))
	for name := range themeRegistry {
		names = append(names, name)
	}
	// Scan external themes directory
	themesDir := filepath.Join(filepath.Dir(config.ConfigFilePath()), "themes")
	if entries, err := os.ReadDir(themesDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".json")
			if name == e.Name() {
				continue // not a .json file
			}
			// Don't duplicate built-in names
			if _, ok := themeRegistry[name]; ok {
				continue
			}
			names = append(names, name)
		}
	}
	return names
}

// RoleColor returns a hex color string for a SubAgent role name.
// It uses a deterministic HSL-based hash so the same role always gets
// the same color, and all roles are visually distinct.
// Colors are tuned for dark terminal backgrounds (high lightness ~72%).
func RoleColor(role string) string {
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(role)))
	hash := h.Sum32()

	// Spread hues evenly across 0-360°
	hue := int(hash % 360)
	const saturation, lightness = 0.75, 0.72 // tuned for dark backgrounds
	return hslToHex(hue, saturation, lightness)
}

// hslToHex converts HSL (hue 0-359, saturation/lightness 0-1) to #RRGGBB.
func hslToHex(h int, s, l float64) string {
	r, g, b := hslToRGB(h, s, l)
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// hslToRGB converts HSL to RGB (0-255 each).
func hslToRGB(h int, s, l float64) (uint8, uint8, uint8) {
	hf := float64(h) / 60.0
	c := (1 - abs(2*l-1)) * s
	x := c * (1 - abs(math.Mod(hf, 2)-1))
	var r1, g1, b1 float64
	switch {
	case hf < 1:
		r1, g1, b1 = c, x, 0
	case hf < 2:
		r1, g1, b1 = x, c, 0
	case hf < 3:
		r1, g1, b1 = 0, c, x
	case hf < 4:
		r1, g1, b1 = 0, x, c
	case hf < 5:
		r1, g1, b1 = x, 0, c
	default:
		r1, g1, b1 = c, 0, x
	}
	m := l - c/2
	r := uint8((r1 + m) * 255)
	g := uint8((g1 + m) * 255)
	b := uint8((b1 + m) * 255)
	return r, g, b
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// ---------------------------------------------------------------------------
// Widget Style Mapping — maps plugin.StyleClass to lipgloss styles
// ---------------------------------------------------------------------------

// buildWidgetRenderFn returns a RenderFunc that applies the current theme to widget spans.
// Passed to plugin.WidgetRegistry.SetDefaultRenderFn at startup.
func buildWidgetRenderFn(st cliStyles) func(spans []plugin.WidgetSpan, width int) string {
	styleMap := map[plugin.StyleClass]lipgloss.Style{
		plugin.StyleNormal:  lipgloss.NewStyle(),
		plugin.StyleDim:     st.ProgressDim,
		plugin.StyleAccent:  st.Accent,
		plugin.StyleSuccess: st.ReadyStatus,
		plugin.StyleWarning: st.WarningSt,
		plugin.StyleError:   st.ErrorMsg,
		plugin.StyleInfo:    st.InfoSt,
		plugin.StyleMuted:   st.TextMutedSt,
	}
	return func(spans []plugin.WidgetSpan, width int) string {
		if len(spans) == 0 {
			return ""
		}
		var result string
		for _, sp := range spans {
			st, ok := styleMap[sp.Style]
			if !ok {
				st = lipgloss.NewStyle()
			}
			result += st.Render(sp.Text)
		}
		return result
	}
}

package channel

import (
	"time"
)

// UILocale holds all UI strings for a given language.
type UILocale struct {
	// --- A. System messages ---
	CancelSent          string // "已发送取消请求"
	QueueCleared        string // "已清空 %d 条排队消息"
	SettingsSaved       string // "✅ 设置已保存"
	NoSettings          string // "当前渠道没有可配置的设置项。"
	CheckingUpdate      string // "正在检查更新..."
	ModelUsage          string // "用法: /set-model <模型名>\n使用 /models 查看可用模型"
	AskCancelled        string // "已取消提问"
	SetupComplete       string // "✅ 初始配置完成，可以开始使用了。随时用 /settings 修改配置，/setup 重新引导。"
	SetupLettaNote      string // "[!] letta memory mode requires embedding service:\n  1. ..."
	SetupTitle          string // Setup wizard title banner (HTML-like markup)
	SetupSubtitle       string // Setup wizard subtitle explaining the 2-step process
	SetupWelcome        string // Welcome message shown after setup completes
	SetupNoLLM          string // Message shown when user tries to chat without LLM config
	WizardProviderTitle string // "选择你的 AI 服务商"
	WizardKeyTitle      string // "获取 %s 的密钥"
	WizardKeyLabel      string // "密钥："
	WizardDoneTitle     string // "🎉 设置完成！"
	WizardStartBtn      string // "开始使用"
	WizardNextBtn       string // "下一步"
	WizardBackBtn       string // "返回"
	WizardNavHint       string // "↑↓ 选择 · Enter 确认"
	UpdateFound         string // "发现新版本: %s → %s\n升级命令: ..."
	UpdateCurrent       string // "当前版本 %s 已是最新"
	UpdateFailed        string // "更新检查失败（网络超时或无法连接 GitHub API）"

	// --- B. Panel text ---
	PanelSettingsTitle   string            // "⚙ Settings"
	PanelNotSet          string            // "(未设置)"
	PanelEditHint        string            // "Enter confirm | Esc cancel"
	PanelComboHint       string            // "Up/Down select | Enter confirm | Type custom | Esc cancel"
	PanelNavHint         string            // "↑↓ 导航 · Enter 编辑/切换 · Ctrl+S 保存 · Esc 关闭"
	PanelEditPlaceholder string            // "输入新值..."
	PanelBtnGetKey       string            // "🔑 点击这里获取密钥"
	PanelBtnSave         string            // "💾 保存设置"
	PanelBtnCancel       string            // "✖ 取消"
	ProviderHints        map[string]string // per-provider API key hint text (keyed by HintKey)
	PanelToggleOn        string            // "● ON"
	PanelToggleOff       string            // "○ OFF"

	PanelOther      string // "Other: "
	PanelSubmit     string // "Submit →"
	PanelAskNav     string // "←→/Tab 切换问题"
	PanelAskToggle  string // "Space/Enter toggle"
	PanelAskOther   string // "v Other input"
	PanelAskSubmit  string // "Enter submit"
	PanelAskNewline string // "Ctrl+J newline"
	PanelAskCancel  string // "Esc cancel"

	BgTasksTitle       string // "Tasks"
	BgTasksHelp        string // "↑↓ navigate  Enter view log  Del kill  Esc close"
	BgTasksEmpty       string // "No background tasks running"
	BgTasksUnsupported string // "Background tasks not supported."
	BgTaskLogTitle     string // "Log: %s — %s"
	BgTaskLogHelp      string // "↑↓ scroll  Esc back"
	BgTaskLogMore      string // "... %d more lines (↑↓ scroll)"

	PanelOmitted          string // "... %d lines omitted — resize terminal for full view ..."
	PanelOtherPlaceholder string // askuser panel Other input placeholder
	EmergencyQuitHint     string // Ctrl+Z emergency quit hint

	// --- C. Status bar ---
	TitleHint             string // "Enter send · Ctrl+J newline · /help"
	ShiftSelectHint       string // "⇧+drag to select text"
	ProcessingPlaceholder string // "[Processing...] (Ctrl+C to cancel)"
	CheckingUpdates       string // "⟳ checking for updates..."
	StatusReady           string // "● ready"
	StatusCompressing     string // "compressing"
	StatusNewing          string // "resetting session"
	StatusRetrying        string // "retrying"
	StatusDone            string // "done"
	NewContentHint        string // "v new content"
	BgTaskRunning         string // "[^ %dt]"
	AgentRunning          string // "[Ctrl+T %da]"
	TabNoMatch            string // "[Tab] no matching files"

	// --- C2. Info Bar (bottom status line below input) ---
	InfoBarTasks   string // "Tasks"
	InfoBarAgents  string // "Agents"
	InfoBarNoTasks string // "No tasks"
	InfoBarNoAgent string // "No agents"

	// --- D. Temp status ---
	WaitingOperation   string // "... waiting for previous operation to complete..."
	NoMessagesToDelete string // "[!] no messages to delete"
	KillFailed         string // "Kill failed: %s"

	// --- E. Help ---
	HelpTitle          string // "xbot Help"
	HelpCommandsTitle  string // " Commands "
	HelpShortcutsTitle string // " Shortcuts "
	HelpCmds           []HelpCmdEntry
	HelpKeys           []HelpKeyEntry

	// --- E2. Fold messages (§19) ---
	MsgTooShortToFold string
	MsgExpanded       string
	MsgCollapsed      string

	// --- E3. Search (§21) ---
	SearchPlaceholder string
	SearchResults     string
	SearchNoResults   string
	SearchNavFormat   string

	// --- F. Rewind ---
	RewindTitle string // "Rewind"
	RewindHint  string // "Select a message to rewind to. Content will be placed in input box."

	// --- G. Splash ---
	SplashDesc     string // "AI-powered terminal agent"
	SplashLoading  string // "  %s  initializing..."
	SplashFirstRun string // Splash description for first-run users

	// --- H. Footer keys ---
	FooterScroll   string // "scroll"
	FooterBack     string // "back"
	FooterNavigate string // "navigate"
	FooterLog      string // "log"
	FooterKill     string // "kill"
	FooterClose    string // "close"
	FooterCancel   string // "cancel"
	FooterPalette  string // "palette"
	FooterCommands string // "commands"
	FooterComplete string // "complete"
	FooterBgTasks  string // "bg tasks"
	FooterNewline  string // "newline"
	FooterSelect   string // "select"
	FooterManage   string // "manage"
	FooterHistory  string // "history"
	FooterSearch   string // "search"
	FooterFold     string // "fold"
	FooterUnfold   string // "unfold"

	// --- K. Runner panel ---
	RunnerPanelTitle           string
	RunnerStatusConnected      string
	RunnerConnecting           string
	RunnerDisconnect           string
	RunnerDisconnectAction     string
	RunnerConnectSuccess       string
	RunnerConnectFailed        string
	RunnerServerLabel          string
	RunnerTokenLabel           string
	RunnerWorkspaceLabel       string
	RunnerServerPlaceholder    string
	RunnerTokenPlaceholder     string
	RunnerWorkspacePlaceholder string
	RunnerServerRequired       string
	RunnerWorkspaceRequired    string
	RunnerPleaseWait           string
	RunnerNavHint              string
	RunnerNotAvailable         string
	RunnerLogLabel             string // "📋 日志"
	RunnerBack                 string // "back"

	// --- L. /su command ---
	SuAlreadyDefault  string // "Already default identity"
	SuSwitched        string // "✅ Switched to: %s"
	SuSwitchedHistory string // "✅ Switched to: %s — loaded %d history messages"
	SuLoadFailed      string // "⚠️ Failed to load history: %v"
	SuSwitching       string // "Switching identity: %s"
	SuLoadingHistory  string // "  %s  loading history..."

	// --- L2. Reconnect overlay (remote mode) ---
	ReconnectTitle  string // "Connection Lost"
	ReconnectingMsg string // "  %s  Reconnecting to server..."
	ReconnectedMsg  string // "  %s  Reconnected!"
	ReconnectHint   string // "Press Ctrl+C to quit"

	// --- M. Danger zone ---
	DangerTitle              string // "⚠ Danger Zone"
	DangerConfirmClear       string // "Confirm clear: %s"
	DangerIrreversible       string // "This action cannot be undone."
	DangerTypeConfirm        string // "Type %s to confirm:"
	DangerNavHint            string // "↑↓ select  Enter confirm  Esc back"
	DangerMismatch           string // "❌ Confirmation text mismatch"
	DangerClearFailed        string // "❌ Clear failed: %v"
	DangerCleared            string // "✅ Cleared: %s"
	DangerConfirmPlaceholder string // "Type confirmation text..."
	DangerSessionHistory     string // "Session History"
	DangerCoreAll            string // "Core Memory: All"
	DangerLongTerm           string // "Long-term Memory"
	DangerEventHistory       string // "Event History"
	DangerArchival           string // "Archival Memory (vector DB)"

	// --- N. Message queue ---
	MessageQueued    string // "⏳ Message queued (%d pending)"
	MessageQueuedUp  string // "⏳ Message queued (%d pending) — ↑ to recall · Esc to cancel"
	QueuePending     string // "📬 %d queued" — persistent status bar indicator
	QueueItemRemoved string // "Removed queued message: %s"

	// --- I. Dynamic arrays ---
	ThinkingVerbs    []string // spinner verbs: Thinking, Reasoning, ...
	IdlePlaceholders []string // rotating hints for empty input

	// --- J. Settings schema ---
	SetupSchema    []SettingDefinition
	SettingsSchema []SettingDefinition
}

// HelpCmdEntry describes a single help command entry.
type HelpCmdEntry struct {
	Cmd  string
	Desc string
}

// HelpKeyEntry describes a single help key entry.
type HelpKeyEntry struct {
	Key  string
	Desc string
}

// ---------------------------------------------------------------------------
// Locales
// ---------------------------------------------------------------------------

var locales map[string]*UILocale

func LocaleZH() *UILocale {
	return &UILocale{
		// --- A. System messages ---
		CancelSent:          "已发送取消请求",
		QueueCleared:        "已清空 %d 条排队消息",
		SettingsSaved:       "✅ 设置已保存",
		NoSettings:          "当前渠道没有可配置的设置项。",
		CheckingUpdate:      "正在检查更新...",
		ModelUsage:          "用法: /set-model <模型名>\n使用 /models 查看可用模型",
		AskCancelled:        "已取消提问",
		SetupComplete:       "✅ 初始配置完成，可以开始使用了。随时用 /settings 修改配置，/setup 重新引导。",
		SetupLettaNote:      "\n\n[!] letta 记忆模式需要嵌入服务:\n  1. 安装 Ollama: https://ollama.ai\n  2. 拉取嵌入模型: `ollama pull nomic-embed-text`\n  3. 在配置或环境变量中设置嵌入端点",
		SetupTitle:          "👋 欢迎使用 xbot！",
		SetupSubtitle:       "要开始使用，只需做两件事：选择 AI 服务商 → 填入密钥。其他选项可以先不填",
		SetupWelcome:        "🎉 设置完成！你可以开始和 AI 对话了。\n\n📝 怎么用：\n• 在底部输入框打字，按 Enter 发送\n• 想换行？按 Ctrl+J\n• 输入 /help 查看更多操作\n\n⌨️ 常用快捷键：\n• Ctrl+K — 命令面板（可以执行各种操作）\n• Ctrl+T — 查看和切换对话\n• Ctrl+N — 切换 AI 模型\n• Ctrl+C — 取消 AI 正在生成的回复\n\n💡 小提示：直接用大白话和 AI 说话就行，比如「帮我写一个 Python 脚本」或「解释一下这段代码」",
		SetupNoLLM:          "⚠️ 还没有配置 AI 服务密钥，暂时无法对话。\n\n按 /setup 重新配置，或按 /settings 打开完整设置。\n\n📖 如果你不确定怎么做，输入 /help 查看帮助。",
		WizardProviderTitle: "选择你的 AI 服务商",
		WizardKeyTitle:      "获取 %s 的密钥",
		WizardKeyLabel:      "密钥（API Key）：",
		WizardDoneTitle:     "🎉 设置完成！",
		WizardStartBtn:      "开始使用",
		WizardNextBtn:       "下一步",
		WizardBackBtn:       "返回",
		WizardNavHint:       "↑↓ 选择 · Enter 确认 · Esc 返回上一步",
		UpdateFound:         "发现新版本: %s → %s (stable)\n升级命令: curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash\n%s",
		UpdateCurrent:       "当前版本 %s (channel: %s) 已是最新",
		UpdateFailed:        "更新检查失败（网络超时或无法连接 GitHub API）",

		// --- B. Panel text ---
		PanelSettingsTitle:   "⚙ 设置",
		PanelNotSet:          "(未设置)",
		PanelEditHint:        "Enter 确认 | Esc 取消",
		PanelComboHint:       "↑↓ 选择 | Enter 确认 | 输入自定义值 | Esc 取消",
		PanelNavHint:         "↑↓ 导航 · Enter 编辑/切换 · Ctrl+S 保存 · Esc 关闭",
		PanelEditPlaceholder: "> 输入新值...",
		PanelBtnGetKey:       "🔑 点击这里获取密钥",
		PanelBtnSave:         "💾 保存设置",
		PanelBtnCancel:       "✖ 取消",
		ProviderHints: map[string]string{
			"openai":       "👉 打开上面的链接 → 登录 → Create new secret key → 复制密钥",
			"anthropic":    "👉 打开上面的链接 → 登录 → Create Key → 复制密钥",
			"openrouter":   "👉 打开上面的链接 → 登录 → Create Key → 复制密钥",
			"google":       "👉 打开上面的链接 → 登录 → Create API Key → 复制密钥",
			"deepseek":     "👉 打开上面的链接 → 登录 → 创建 API Key → 复制密钥",
			"zhipu":        "👉 打开上面的链接 → 登录 → 添加 API Key → 复制密钥",
			"zhipu_coding": "👉 打开上面的链接 → 登录 → 创建 API Key（sk-sp- 开头的是 Coding Plan 专用密钥）",
			"siliconflow":  "👉 打开上面的链接 → 登录 → 添加 API Key → 复制密钥",
			"moonshot":     "👉 打开上面的链接 → 登录 → 创建 API Key → 复制密钥",
			"xiaomi":       "👉 打开上面的链接 → 注册/登录 → 获取 Token Plan 密钥",
			"ollama":       "✅ 不需要密钥！只需先安装 Ollama（ollama.com）并运行模型",
		},
		PanelToggleOn:  "● 开启",
		PanelToggleOff: "○ 关闭",

		PanelOther:      "其他: ",
		PanelSubmit:     "提交 →",
		PanelAskNav:     "←→/Tab 切换问题",
		PanelAskToggle:  "Space/Enter 切换",
		PanelAskOther:   "v 自定义输入",
		PanelAskSubmit:  "Enter 提交",
		PanelAskNewline: "Ctrl+J 换行",
		PanelAskCancel:  "Esc 取消",

		BgTasksTitle:       "📋 Tasks",
		BgTasksHelp:        "↑↓ 导航  Enter 查看日志  Del 终止  Esc 关闭",
		BgTasksEmpty:       "没有正在运行的任务或代理",
		BgTasksUnsupported: "不支持后台任务。",
		BgTaskLogTitle:     "日志: %s — %s",
		BgTaskLogHelp:      "↑↓ 滚动  Esc 返回",
		BgTaskLogMore:      "... 还有 %d 行（↑↓ 滚动）",

		PanelOmitted:          "  ... %d 行已省略（终端过窄，请放大窗口） ...",
		PanelOtherPlaceholder: "在此输入...",
		EmergencyQuitHint:     "🚪 紧急退出 (Ctrl+Z)",

		// --- C. Status bar ---
		TitleHint:             "Enter 发送 · Ctrl+J 换行 · /help",
		ShiftSelectHint:       "⇧+拖拽 选择文本",
		ProcessingPlaceholder: "[处理中...] 输入消息排队 · Ctrl+C 取消",
		CheckingUpdates:       "⟳ 正在检查更新...",
		StatusReady:           "◈ 就绪",
		StatusCompressing:     "压缩中",
		StatusNewing:          "重置中",
		StatusRetrying:        "重试中",
		StatusDone:            "完成",
		NewContentHint:        "↓ 新内容",
		BgTaskRunning:         "[^ %dt]",
		AgentRunning:          "[Ctrl+T %da]",
		TabNoMatch:            "[Tab] 无匹配文件",

		// --- C2. Info Bar (bottom status line below input) ---
		InfoBarTasks:   "任务",
		InfoBarAgents:  "代理",
		InfoBarNoTasks: "无任务",
		InfoBarNoAgent: "无代理",

		// --- D. Temp status ---
		WaitingOperation:   "... 等待上一个操作完成...",
		NoMessagesToDelete: "[!] 没有可删除的消息",
		KillFailed:         "终止失败: %s",

		// --- E. Help commands ---
		HelpTitle:          "xbot 帮助",
		HelpCommandsTitle:  " 命令 ",
		HelpShortcutsTitle: " 快捷键 ",
		HelpCmds: []HelpCmdEntry{
			{Cmd: "/commands", Desc: "打开命令面板 (Ctrl+K)"},
			{Cmd: "/cancel", Desc: "取消当前操作"},
			{Cmd: "/clear", Desc: "清空聊天记录"},
			{Cmd: "/compress", Desc: "压缩上下文"},
			{Cmd: "/set-llm", Desc: "创建/更新个人 LLM 订阅"},
			{Cmd: "/unset-llm", Desc: "删除个人默认订阅"},
			{Cmd: "/llm", Desc: "查看当前订阅与模型"},
			{Cmd: "/models", Desc: "列出可选模型"},
			{Cmd: "/set-model", Desc: "切换模型"},
			{Cmd: "/usage", Desc: "查看 token 用量"},
			{Cmd: "/new", Desc: "开始新会话"},
			{Cmd: "/rename", Desc: "重命名当前会话"},
			{Cmd: "/rewind", Desc: "回退对话"},
			{Cmd: "/settings", Desc: "打开设置面板"},
			{Cmd: "/menu", Desc: "主菜单（设置/市场/我的）"},
			{Cmd: "/browse", Desc: "浏览 Skill/Agent 市场"},
			{Cmd: "/install", Desc: "安装市场 Skill/Agent"},
			{Cmd: "/uninstall", Desc: "卸载 Skill/Agent"},
			{Cmd: "/publish", Desc: "发布 Skill/Agent 到市场"},
			{Cmd: "/unpublish", Desc: "取消发布"},
			{Cmd: "/my", Desc: "查看我发布/安装的 Skills/Agents"},
			{Cmd: "/update", Desc: "检查更新"},
			{Cmd: "/help", Desc: "显示此帮助"},
		},
		HelpKeys: []HelpKeyEntry{
			{Key: "Ctrl+K", Desc: "命令面板（所有操作入口）"},
			{Key: "Ctrl+N", Desc: "LLM 面板（切换模型 + 管理订阅）"},
			{Key: "Ctrl+T", Desc: "会话列表"},
			{Key: "Ctrl+O", Desc: "展开/折叠工具"},
			{Key: "Ctrl+J", Desc: "输入框换行"},
			{Key: "Tab", Desc: "命令/路径补全"},
			{Key: "Shift+↑", Desc: "追回排队消息编辑"},
			{Key: "^", Desc: "后台任务面板"},
			{Key: "Ctrl+C", Desc: "取消操作/删除排队消息"},
		},

		// --- E2. Fold messages (§19) ---
		MsgTooShortToFold: "消息太短，无法折叠（需超过 %d 行）",
		MsgExpanded:       "已展开",
		MsgCollapsed:      "已折叠",

		// --- E3. Search (§21) ---
		SearchPlaceholder: "搜索消息...",
		SearchResults:     "找到 %d 条匹配消息 (n/N 导航, Esc 退出)",
		SearchNoResults:   "未找到匹配消息",
		SearchNavFormat:   "/ %s  [%d/%d]  n next · N prev · Esc",

		// --- F. Confirm dialog ---
		RewindTitle: "Rewind",
		RewindHint:  "选择要回退到的消息，内容将放入输入框",

		// --- G. Splash ---
		SplashDesc:     "AI 驱动的终端助手",
		SplashLoading:  "  %s  初始化中...",
		SplashFirstRun: "👋 欢迎使用 xbot！正在为你准备初次设置...",

		// --- H. Footer keys ---
		FooterScroll:   "滚动",
		FooterBack:     "返回",
		FooterNavigate: "导航",
		FooterLog:      "日志",
		FooterKill:     "终止",
		FooterClose:    "关闭",
		FooterCancel:   "取消",
		FooterPalette:  "命令面板",
		FooterCommands: "命令",
		FooterComplete: "补全",
		FooterBgTasks:  "任务/代理",
		FooterNewline:  "换行",
		FooterSelect:   "选择",
		FooterManage:   "管理",
		FooterHistory:  "历史",
		FooterSearch:   "搜索",
		FooterFold:     "折叠",
		FooterUnfold:   "展开",

		// --- K. Runner panel ---
		RunnerPanelTitle:           "Runner 管理",
		RunnerStatusConnected:      "已连接",
		RunnerConnecting:           "正在连接...",
		RunnerDisconnect:           "断开连接",
		RunnerDisconnectAction:     "断开",
		RunnerConnectSuccess:       "✅ Runner 已连接",
		RunnerConnectFailed:        "❌ Runner 连接失败: %s",
		RunnerServerLabel:          "Server URL",
		RunnerTokenLabel:           "Token",
		RunnerWorkspaceLabel:       "工作目录",
		RunnerServerPlaceholder:    "ws://host:port/ws/userID",
		RunnerTokenPlaceholder:     "认证 Token",
		RunnerWorkspacePlaceholder: "共享的工作目录路径",
		RunnerServerRequired:       "Server URL 不能为空",
		RunnerWorkspaceRequired:    "工作目录不能为空",
		RunnerPleaseWait:           "请稍候...",
		RunnerNavHint:              "↑↓/Tab 切换字段  Enter 连接  Esc 返回",
		RunnerNotAvailable:         "Runner 功能不可用",
		RunnerLogLabel:             "📋 日志",
		RunnerBack:                 "返回",

		// --- L. /su command ---
		SuAlreadyDefault:  "当前已是默认身份",
		SuSwitched:        "✅ 身份已切换为: %s",
		SuSwitchedHistory: "✅ 身份已切换为: %s — 已加载 %d 条历史消息",
		SuLoadFailed:      "⚠️ 加载历史失败: %v",
		SuSwitching:       "切换身份: %s",
		SuLoadingHistory:  "  %s  加载历史中...",

		// --- L2. Reconnect overlay ---
		ReconnectTitle:  "连接已断开",
		ReconnectingMsg: "  %s  正在重新连接服务器...",
		ReconnectedMsg:  "  %s  已重新连接!",
		ReconnectHint:   "按 Ctrl+Z 退出",

		// --- M. Danger zone ---
		DangerTitle:              "⚠ 危险区",
		DangerConfirmClear:       "确认清空：%s",
		DangerIrreversible:       "此操作不可恢复",
		DangerTypeConfirm:        "请输入 %s 确认：",
		DangerNavHint:            "↑↓ 选择  Enter 确认  Esc 返回",
		DangerMismatch:           "❌ 确认文字不匹配",
		DangerClearFailed:        "❌ 清空失败：%v",
		DangerCleared:            "✅ 已清空：%s",
		DangerConfirmPlaceholder: "输入确认文字...",
		DangerSessionHistory:     "会话历史",
		DangerCoreAll:            "Core Memory: 全部",
		DangerLongTerm:           "长期记忆",
		DangerEventHistory:       "事件历史",
		DangerArchival:           "归档记忆（向量数据库）",

		// --- N. Message queue ---
		MessageQueued:    "⏳ 消息已排队（%d 条待发送）",
		MessageQueuedUp:  "⏳ 消息已排队（%d 条待发送）— Shift+↑ 追回编辑 · Esc 撤销",
		QueuePending:     "📬 %d 条排队中",
		QueueItemRemoved: "已移除排队消息：%s",

		// --- I. Dynamic arrays ---
		ThinkingVerbs: []string{"思考中", "推理中", "分析中", "考虑中", "评估中", "反思中", "处理中", "沉思中"},
		IdlePlaceholders: []string{
			"Enter 发送 · Ctrl+J 换行 · /help",
			"试试问：帮我写一个 Python 脚本",
			"Ctrl+K 命令面板",
			"Ctrl+T 会话 · Ctrl+K 命令",
			"@filepath 附加文件",
			"Ctrl+N 切换模型",
			"Ctrl+K → 所有命令",
			"输入 /help 查看所有快捷键",
			"直接用大白话和 AI 说话就行",
		},

		// --- J. Settings schema ---
		SetupSchema: []SettingDefinition{

			{
				Key: "llm_provider", Label: "AI 服务商", Description: "选择你使用的 AI 服务。不知道选什么？国内推荐 DeepSeek",
				Type: SettingTypeCombo, Category: "LLM", DefaultValue: "deepseek",
				Options: []SettingOption{
					{Label: "DeepSeek（深度求索）", Value: "deepseek", Description: "国内直连 · 性能强 · 新用户有免费额度"},
					{Label: "智谱（ChatGLM）", Value: "zhipu", Description: "国内直连 · GLM 系列"},
					{Label: "智谱 Coding Plan（编程套餐）", Value: "zhipu_coding", Description: "编程专用通道 · 针对代码优化 · 需要单独购买"},
					{Label: "硅基流动（SiliconFlow）", Value: "siliconflow", Description: "国内聚合平台 · 多种模型可选"},
					{Label: "Moonshot（Kimi）", Value: "moonshot", Description: "国内直连 · Kimi 系列"},
					{Label: "小米 MiMo（Token Plan）", Value: "xiaomi", Description: "Token 计费 · 月付套餐 · 包含 V2.5 全系列"},
					{Label: "OpenAI（ChatGPT）", Value: "openai", Description: "需要海外网络"},
					{Label: "Anthropic（Claude）", Value: "anthropic", Description: "需要海外网络"},
					{Label: "OpenRouter", Value: "openrouter", Description: "聚合平台 · 可访问多种模型"},
					{Label: "Google AI（Gemini）", Value: "google", Description: "需要海外网络"},
					{Label: "Ollama（本地运行）", Value: "ollama", Description: "无需联网 · 需要先安装 Ollama"},
					{Label: "自定义（兼容 OpenAI 格式）", Value: "custom", Description: "适用于其他 OpenAI 兼容的 AI 服务"},
				},
			},
			{
				Key: "llm_api_key", Label: "密钥（API Key）",
				Description: "使用 AI 服务的通行证。选好服务商后，去它的官网注册并创建密钥",
				Type:        SettingTypePassword, Category: "LLM",
			},
			{
				Key: "llm_model", Label: "AI 模型",
				Description: "一般会自动填好，也可以改成你想用的模型",
				Type:        SettingTypeCombo, Category: "LLM",
			},
			{
				Key: "llm_base_url", Label: "服务器地址",
				Description: "一般不用改，会自动填好。只有使用自定义服务时才需要修改",
				Type:        SettingTypeText, Category: "LLM",
				DependsOnKey:    "llm_provider",
				DependsOnValues: "ollama,custom",
			},
			{
				Key: "theme", Label: "界面风格", Description: "选一个你喜欢的颜色风格",
				Type: SettingTypeSelect, Category: "外观", DefaultValue: "midnight",
				Options: []SettingOption{
					{Label: "Midnight（默认）", Value: "midnight"},
					{Label: "Ocean", Value: "ocean"},
					{Label: "Forest", Value: "forest"},
					{Label: "Sunset", Value: "sunset"},
					{Label: "Rose", Value: "rose"},
					{Label: "Mono", Value: "mono"},
					{Label: "nord:极光", Value: "nord"},
					{Label: "dracula:暗夜", Value: "dracula"},
					{Label: "catppuccin:摩卡", Value: "catppuccin"},
				},
			},
		},
		SettingsSchema: []SettingDefinition{

			// ── 模型与推理（用户级 + 订阅入口）──
			{
				Key: "thinking_mode", Label: "思考模式", Description: "模型推理/思维链模式（默认自动）",
				Type: SettingTypeSelect, Category: "模型与推理", DefaultValue: "",
				Options: []SettingOption{
					{Label: "自动", Value: ""},
					{Label: "开启", Value: "enabled"},
					{Label: "开启（保留历史推理）", Value: `{"type":"enabled","clear_thinking":false}`},
					{Label: "关闭", Value: "disabled"},
					{Label: "DeepSeek: effort=high", Value: `{"thinking":{"type":"enabled"},"reasoning_effort":"high"}`},
					{Label: "DeepSeek: effort=max", Value: `{"thinking":{"type":"enabled"},"reasoning_effort":"max"}`},
				},
			},
			// Subscription management entry (display-only, triggers quick switch)
			{Key: "subscription_manage", Label: "📦 订阅管理", Type: SettingTypeText, Category: "模型与推理"},

			// ── 模型等级（SubAgent 全局设置，跨订阅生效）──
			{Key: "tier_vanguard", Label: "Vanguard（强）", Description: "SubAgent 指定 vanguard 等级时使用的模型，跨所有订阅选择", Type: SettingTypeCombo, Category: "模型等级 (SubAgent)"},
			{Key: "tier_balance", Label: "Balance（中）", Description: "SubAgent 指定 balance 等级时使用的模型，跨所有订阅选择", Type: SettingTypeCombo, Category: "模型等级 (SubAgent)"},
			{Key: "tier_swift", Label: "Swift（弱）", Description: "SubAgent 指定 swift 等级时使用的模型，跨所有订阅选择", Type: SettingTypeCombo, Category: "模型等级 (SubAgent)"},

			// ── Agent 行为（用户级）──
			{
				Key: "enable_stream", Label: "流式输出", Description: "使用流式 API 调用 LLM（默认开启）",
				Type: SettingTypeSelect, Category: "Agent 行为", DefaultValue: "true",
				Options: []SettingOption{
					{Label: "开启", Value: "true"},
					{Label: "关闭", Value: "false"},
				},
			},
			{
				Key: "enable_masking", Label: "工具结果遮蔽", Description: "上下文较大时自动遮蔽旧工具结果以释放空间（默认开启）",
				Type: SettingTypeSelect, Category: "Agent 行为", DefaultValue: "true",
				Options: []SettingOption{
					{Label: "开启", Value: "true"},
					{Label: "关闭", Value: "false"},
				},
			},
			{
				Key: "compression_threshold", Label: "压缩阈值", Description: "上下文压缩触发阈值，占最大上下文的比例（默认 0.9）",
				Type: SettingTypeNumber, Category: "Agent 行为", DefaultValue: "0.9",
			},
			{
				Key: "context_mode", Label: "上下文模式", Description: "控制上下文管理策略",
				Type: SettingTypeSelect, Category: "Agent 行为", DefaultValue: "auto",
				Options: []SettingOption{
					{Label: "自动（默认）", Value: "auto"},
					{Label: "手动压缩", Value: "manual"},
					{Label: "不压缩", Value: "none"},
				},
			},
			{
				Key: "max_iterations", Label: "最大迭代次数", Description: "单次对话最大工具调用迭代次数（默认 2000）",
				Type: SettingTypeNumber, Category: "Agent 行为", DefaultValue: "2000",
			},
			{
				Key: "max_concurrency", Label: "最大并发数", Description: "同时处理的最大请求数（默认 3）",
				Type: SettingTypeNumber, Category: "Agent 行为", DefaultValue: "3",
			},
			{Key: "auto_worktree", Label: "自动 Worktree 隔离", Description: "每个会话自动创建独立的 git worktree，避免多 agent 同时修改同一文件。需要 git 仓库。", Type: SettingTypeToggle, Category: "Agent 行为", DefaultValue: "false"},

			// ── 外观（用户级）──
			{
				Key: "theme", Label: "配色", Description: "CLI 界面配色方案",
				Type: SettingTypeSelect, Category: "外观", DefaultValue: "midnight",
				Options: []SettingOption{
					{Label: "Midnight（默认）", Value: "midnight"},
					{Label: "Ocean", Value: "ocean"},
					{Label: "Forest", Value: "forest"},
					{Label: "Sunset", Value: "sunset"},
					{Label: "Rose", Value: "rose"},
					{Label: "Mono", Value: "mono"},
					{Label: "nord:极光", Value: "nord"},
					{Label: "dracula:暗夜", Value: "dracula"},
					{Label: "catppuccin:摩卡", Value: "catppuccin"},
				},
			},
			{
				Key: "language", Label: "语言", Description: "Agent 回复使用的语言",
				Type: SettingTypeSelect, Category: "外观", DefaultValue: "",
				Options: []SettingOption{
					{Label: "跟随 Prompt（默认）", Value: ""},
					{Label: "English", Value: "en"},
					{Label: "中文", Value: "zh"},
					{Label: "日本語", Value: "ja"},
				},
			},

			// ── 服务与密钥（用户级）──
			{
				Key: "tavily_api_key", Label: "Tavily API Key", Description: "网络搜索服务密钥（个人配置，优先使用；留空则使用全局配置）",
				Type: SettingTypePassword, Category: "服务与密钥",
			},
			{Key: "runner_panel", Label: "🔧 Runner 管理", Type: SettingTypeText, Category: "服务与密钥"},

			// ── 权限（全局级）──
			{Key: "default_user", Label: "默认执行用户", Description: "LLM 可以免审批以此用户执行工具。留空则只能以当前进程用户执行（最安全）。需配置 NOPASSWD sudoers", Type: SettingTypeText, Category: "权限"},
			{Key: "privileged_user", Label: "特权用户", Description: "LLM 以此用户执行时需要人工审批。留空则禁止提权。需配置 NOPASSWD sudoers", Type: SettingTypeText, Category: "权限"},

			// ── 危险（动作）──
			{Key: "danger_zone", Label: "⚠️ 危险区 — 清空记忆", Type: SettingTypeText, Category: "危险"},
		},
	}
}

func localeEN() *UILocale {
	return &UILocale{
		// --- A. System messages ---
		CancelSent:          "Cancel request sent",
		QueueCleared:        "Cleared %d queued messages",
		SettingsSaved:       "✅ Settings saved",
		NoSettings:          "No configurable settings for this channel.",
		CheckingUpdate:      "Checking for updates...",
		ModelUsage:          "Usage: /set-model <model name>\nUse /models to list available models",
		AskCancelled:        "Question cancelled",
		SetupComplete:       "✅ Initial setup complete. Use /settings to configure, /setup to re-run.",
		SetupLettaNote:      "\n\n[!] letta memory mode requires embedding service:\n  1. Install Ollama: https://ollama.ai\n  2. Pull embedding model: `ollama pull nomic-embed-text`\n  3. Set embedding endpoint in config or env",
		SetupTitle:          "👋 Welcome to xbot!",
		SetupSubtitle:       "To get started: choose an AI provider → enter your key. Other options can be left as-is",
		SetupWelcome:        "🎉 Setup complete! You can now chat with AI.\n\n📝 How to use:\n• Type in the input box at the bottom, press Enter to send\n• Want a new line? Press Ctrl+J\n• Type /help for more options\n\n⌨️ Useful shortcuts:\n• Ctrl+K — Command palette (all actions)\n• Ctrl+T — View and switch sessions\n• Ctrl+N — Switch AI model\n• Ctrl+C — Cancel AI response\n\n💡 Tip: Just talk to AI in plain language, e.g. 'help me write a Python script'",
		SetupNoLLM:          "⚠️ No AI service key configured yet.\n\nPress /setup to configure, or /settings for full options.\n\n📖 Not sure what to do? Type /help for guidance.",
		WizardProviderTitle: "Choose your AI provider",
		WizardKeyTitle:      "Get your %s API key",
		WizardKeyLabel:      "API Key:",
		WizardDoneTitle:     "🎉 Setup complete!",
		WizardStartBtn:      "Start using",
		WizardNextBtn:       "Next",
		WizardBackBtn:       "Back",
		WizardNavHint:       "↑↓ Select · Enter confirm · Esc Go back",
		UpdateFound:         "New version available: %s → %s (stable)\nUpdate command: curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash\n%s",
		UpdateCurrent:       "Current version %s (channel: %s) is up to date",
		UpdateFailed:        "Update check failed (network timeout or unable to connect to GitHub API)",

		// --- B. Panel text ---
		PanelSettingsTitle:   "⚙ Settings",
		PanelNotSet:          "(not set)",
		PanelEditHint:        "Enter confirm | Esc cancel",
		PanelComboHint:       "Up/Down select | Enter confirm | Type custom | Esc cancel",
		PanelNavHint:         "↑↓ navigate · Enter edit/toggle · Ctrl+S save · Esc close",
		PanelEditPlaceholder: "Enter new value...",
		PanelBtnGetKey:       "🔑 Click to get API key",
		PanelBtnSave:         "💾 Save",
		PanelBtnCancel:       "✖ Cancel",
		ProviderHints: map[string]string{
			"openai":       "👉 Open the link above → Log in → Create new secret key → Copy",
			"anthropic":    "👉 Open the link above → Log in → Create Key → Copy",
			"openrouter":   "👉 Open the link above → Log in → Create Key → Copy",
			"google":       "👉 Open the link above → Log in → Create API Key → Copy",
			"deepseek":     "👉 Open the link above → Log in → Create API Key → Copy",
			"zhipu":        "👉 Open the link above → Log in → Add API Key → Copy",
			"zhipu_coding": "👉 Open the link above → Log in → Create API Key (sk-sp- prefix is for Coding Plan)",
			"siliconflow":  "👉 Open the link above → Log in → Add API Key → Copy",
			"moonshot":     "👉 Open the link above → Log in → Create API Key → Copy",
			"xiaomi":       "👉 Open the link above → Sign up / Log in → Get Token Plan key",
			"ollama":       "✅ No key needed! Just install Ollama (ollama.com) and run a model",
		},
		PanelToggleOn:  "● ON",
		PanelToggleOff: "○ OFF",

		PanelOther:      "Other: ",
		PanelSubmit:     "Submit →",
		PanelAskNav:     "←→/Tab switch question",
		PanelAskToggle:  "Space/Enter toggle",
		PanelAskOther:   "v Other input",
		PanelAskSubmit:  "Enter submit",
		PanelAskNewline: "Ctrl+J newline",
		PanelAskCancel:  "Esc cancel",

		BgTasksTitle:       "📋 Tasks",
		BgTasksHelp:        "↑↓ navigate  Enter view log  Del kill  Esc close",
		BgTasksEmpty:       "No running tasks or agents",
		BgTasksUnsupported: "Background tasks not supported.",
		BgTaskLogTitle:     "Log: %s — %s",
		BgTaskLogHelp:      "↑↓ scroll  Esc back",
		BgTaskLogMore:      "... %d more lines (↑↓ scroll)",

		PanelOmitted:          "  ... %d lines omitted — resize terminal for full view ...",
		PanelOtherPlaceholder: "Type here...",
		EmergencyQuitHint:     "🚪 Emergency Quit (Ctrl+Z)",

		// --- C. Status bar ---
		TitleHint:         "Enter send · Ctrl+J newline · /help",
		ShiftSelectHint:   "⇧+drag to select text",
		CheckingUpdates:   "⟳ checking for updates...",
		StatusReady:       "◈ ready",
		StatusCompressing: "compressing",
		StatusNewing:      "resetting",
		StatusRetrying:    "retrying",
		StatusDone:        "done",
		NewContentHint:    "v new content",
		BgTaskRunning:     "[^ %dt]",
		AgentRunning:      "[Ctrl+T %da]",
		TabNoMatch:        "[Tab] no matching files",

		// --- C2. Info Bar (bottom status line below input) ---

		// --- D. Temp status ---
		WaitingOperation:   "... waiting for previous operation to complete...",
		NoMessagesToDelete: "[!] no messages to delete",
		KillFailed:         "Kill failed: %s",

		// --- E. Help commands ---
		HelpTitle:          "xbot Help",
		HelpCommandsTitle:  " Commands ",
		HelpShortcutsTitle: " Shortcuts ",
		HelpCmds: []HelpCmdEntry{
			{Cmd: "/commands", Desc: "Open command palette (Ctrl+K)"},
			{Cmd: "/cancel", Desc: "Cancel current operation"},
			{Cmd: "/clear", Desc: "Clear chat history"},
			{Cmd: "/compress", Desc: "Compress context"},
			{Cmd: "/set-llm", Desc: "Create/update personal LLM subscription"},
			{Cmd: "/unset-llm", Desc: "Delete personal default subscription"},
			{Cmd: "/llm", Desc: "Show current subscription and model"},
			{Cmd: "/models", Desc: "List available models"},
			{Cmd: "/set-model", Desc: "Switch model"},
			{Cmd: "/usage", Desc: "View token usage"},
			{Cmd: "/new", Desc: "Start new session"},
			{Cmd: "/rename", Desc: "Rename current session"},
			{Cmd: "/rewind", Desc: "Rewind conversation"},
			{Cmd: "/settings", Desc: "Open settings panel"},
			{Cmd: "/menu", Desc: "Main menu (settings/marketplace/mine)"},
			{Cmd: "/browse", Desc: "Browse Skill/Agent marketplace"},
			{Cmd: "/install", Desc: "Install a marketplace Skill/Agent"},
			{Cmd: "/uninstall", Desc: "Uninstall a Skill/Agent"},
			{Cmd: "/publish", Desc: "Publish a Skill/Agent to the marketplace"},
			{Cmd: "/unpublish", Desc: "Unpublish"},
			{Cmd: "/my", Desc: "List my published/installed Skills/Agents"},
			{Cmd: "/update", Desc: "Check for updates"},
			{Cmd: "/help", Desc: "Show this help"},
		},
		HelpKeys: []HelpKeyEntry{
			{Key: "Ctrl+K", Desc: "Command palette (all actions)"},
			{Key: "Ctrl+N", Desc: "LLM panel (switch model + manage subscriptions)"},
			{Key: "Ctrl+T", Desc: "Sessions list"},
			{Key: "Ctrl+O", Desc: "Expand/collapse tools"},
			{Key: "Ctrl+J", Desc: "Newline in input"},
			{Key: "Tab", Desc: "Command/path completion"},
			{Key: "Shift+↑", Desc: "Recall queued message"},
			{Key: "^", Desc: "Background tasks panel"},
			{Key: "Ctrl+C", Desc: "Cancel / remove queued messages"},
		},

		// --- E2. Fold messages (§19) ---
		MsgTooShortToFold: "Message too short to fold (needs > %d lines)",
		MsgExpanded:       "Expanded",
		MsgCollapsed:      "Collapsed",

		// --- E3. Search (§21) ---
		SearchPlaceholder: "Search messages...",
		SearchResults:     "Found %d matching messages (n/N navigate, Esc to exit)",
		SearchNoResults:   "No matching messages found",
		SearchNavFormat:   "/ %s  [%d/%d]  n next · N prev · Esc",

		// --- F. Confirm dialog ---
		RewindTitle: "Rewind",
		RewindHint:  "Select a message to rewind to. Content will be placed in input box.",

		// --- G. Splash ---
		SplashDesc:     "AI-powered terminal agent",
		SplashLoading:  "  %s  initializing...",
		SplashFirstRun: "👋 Welcome to xbot! Preparing initial setup...",

		// --- H. Footer keys ---
		FooterScroll:   "scroll",
		FooterBack:     "back",
		FooterNavigate: "navigate",
		FooterLog:      "log",
		FooterKill:     "kill",
		FooterClose:    "close",
		FooterCancel:   "cancel",
		FooterPalette:  "palette",
		FooterCommands: "commands",
		FooterComplete: "complete",
		FooterBgTasks:  "tasks/agents",
		FooterNewline:  "newline",
		FooterSelect:   "select",
		FooterManage:   "manage",
		FooterHistory:  "history",
		FooterSearch:   "search",
		FooterFold:     "fold",
		FooterUnfold:   "unfold",

		// --- K. Runner panel ---
		RunnerPanelTitle:           "Runner Manager",
		RunnerStatusConnected:      "Connected",
		RunnerConnecting:           "Connecting...",
		RunnerDisconnect:           "Disconnect",
		RunnerDisconnectAction:     "disconnect",
		RunnerConnectSuccess:       "✅ Runner connected",
		RunnerConnectFailed:        "❌ Runner connection failed: %s",
		RunnerServerLabel:          "Server URL",
		RunnerTokenLabel:           "Token",
		RunnerWorkspaceLabel:       "Workspace",
		RunnerServerPlaceholder:    "ws://host:port/ws/userID",
		RunnerTokenPlaceholder:     "Auth token",
		RunnerWorkspacePlaceholder: "Shared workspace directory",
		RunnerServerRequired:       "Server URL is required",
		RunnerWorkspaceRequired:    "Workspace is required",
		RunnerPleaseWait:           "Please wait...",
		RunnerNavHint:              "↑↓/Tab switch fields  Enter connect  Esc back",
		RunnerNotAvailable:         "Runner not available",
		RunnerLogLabel:             "📋 Log",
		RunnerBack:                 "back",

		// --- L. /su command ---
		SuAlreadyDefault:  "Already default identity",
		SuSwitched:        "✅ Switched to: %s",
		SuSwitchedHistory: "✅ Switched to: %s — loaded %d history messages",
		SuLoadFailed:      "⚠️ Failed to load history: %v",
		SuSwitching:       "Switching identity: %s",
		SuLoadingHistory:  "  %s  loading history...",

		// --- L2. Reconnect overlay ---
		ReconnectTitle:  "Connection Lost",
		ReconnectingMsg: "  %s  Reconnecting to server...",
		ReconnectedMsg:  "  %s  Reconnected!",
		ReconnectHint:   "Press Ctrl+Z to quit",

		// --- M. Danger zone ---
		DangerTitle:              "⚠ Danger Zone",
		DangerConfirmClear:       "Confirm clear: %s",
		DangerIrreversible:       "This action cannot be undone.",
		DangerTypeConfirm:        "Type %s to confirm:",
		DangerNavHint:            "↑↓ select  Enter confirm  Esc back",
		DangerMismatch:           "❌ Confirmation text mismatch",
		DangerClearFailed:        "❌ Clear failed: %v",
		DangerCleared:            "✅ Cleared: %s",
		DangerConfirmPlaceholder: "Type confirmation text...",
		DangerSessionHistory:     "Session History",
		DangerCoreAll:            "Core Memory: All",
		DangerLongTerm:           "Long-term Memory",
		DangerEventHistory:       "Event History",
		DangerArchival:           "Archival Memory (vector DB)",

		// --- N. Message queue ---
		MessageQueued:    "⏳ Message queued (%d pending)",
		MessageQueuedUp:  "⏳ Message queued (%d pending) — Shift+↑ recall · Esc cancel",
		QueuePending:     "📬 %d queued",
		QueueItemRemoved: "Removed queued message: %s",

		// --- I. Dynamic arrays ---
		ThinkingVerbs: []string{"Thinking", "Reasoning", "Analyzing", "Considering", "Evaluating", "Reflecting", "Processing", "Contemplating"},
		IdlePlaceholders: []string{
			"Enter send · Ctrl+J newline · /help",
			"Try asking: help me write a Python script",
			"Ctrl+K command palette",
			"Ctrl+T sessions · Ctrl+K commands",
			"@filepath to attach files",
			"Ctrl+N switch model",
			"Ctrl+K → all commands",
			"Type /help for all shortcuts",
			"Just talk to AI in plain language",
		},

		// --- J. Settings schema ---
		SetupSchema: []SettingDefinition{

			{
				Key: "llm_provider", Label: "AI Provider", Description: "Choose your AI service. Not sure? Try DeepSeek (works in China)",
				Type: SettingTypeCombo, Category: "LLM", DefaultValue: "deepseek",
				Options: []SettingOption{
					{Label: "DeepSeek", Value: "deepseek", Description: "China direct · Strong · Free credits for new users"},
					{Label: "Zhipu (ChatGLM)", Value: "zhipu", Description: "China direct · GLM series"},
					{Label: "Zhipu Coding Plan", Value: "zhipu_coding", Description: "Coding-specific endpoint · Optimized for code · Separate subscription"},
					{Label: "SiliconFlow", Value: "siliconflow", Description: "China aggregation · Multiple models"},
					{Label: "Moonshot (Kimi)", Value: "moonshot", Description: "China direct · Kimi series"},
					{Label: "Xiaomi MiMo (Token Plan)", Value: "xiaomi", Description: "Token billing · Monthly plan · Includes V2.5 series"},
					{Label: "OpenAI (ChatGPT)", Value: "openai", Description: "Requires overseas network"},
					{Label: "Anthropic (Claude)", Value: "anthropic", Description: "Requires overseas network"},
					{Label: "OpenRouter", Value: "openrouter", Description: "Aggregation platform · Access to many models"},
					{Label: "Google AI (Gemini)", Value: "google", Description: "Requires overseas network"},
					{Label: "Ollama (Local)", Value: "ollama", Description: "No internet needed · Requires Ollama installation"},
					{Label: "Custom (OpenAI-compatible)", Value: "custom", Description: "For other OpenAI-compatible AI services"},
				},
			},
			{
				Key: "llm_api_key", Label: "API Key",
				Description: "Your passkey for using the AI service. Register at the provider's website to get one",
				Type:        SettingTypePassword, Category: "LLM",
			},
			{
				Key: "llm_model", Label: "AI Model",
				Description: "Usually auto-filled. Change it if you want a specific model",
				Type:        SettingTypeCombo, Category: "LLM",
			},
			{
				Key: "llm_base_url", Label: "Server URL",
				Description: "Usually auto-filled. Only modify if using a custom service",
				Type:        SettingTypeText, Category: "LLM",
				DependsOnKey:    "llm_provider",
				DependsOnValues: "ollama,custom",
			},
			{
				Key: "theme", Label: "Color Theme", Description: "Choose a color scheme you like",
				Type: SettingTypeSelect, Category: "Appearance", DefaultValue: "midnight",
				Options: []SettingOption{
					{Label: "Midnight (default)", Value: "midnight"},
					{Label: "Ocean", Value: "ocean"},
					{Label: "Forest", Value: "forest"},
					{Label: "Sunset", Value: "sunset"},
					{Label: "Rose", Value: "rose"},
					{Label: "Mono", Value: "mono"},
					{Label: "nord:Aurora", Value: "nord"},
					{Label: "dracula:Dark Night", Value: "dracula"},
					{Label: "catppuccin:Mocha", Value: "catppuccin"},
				},
			},
		},
		SettingsSchema: []SettingDefinition{
			// ── Model & Reasoning (user-level + subscription entry) ──
			{
				Key: "thinking_mode", Label: "Thinking Mode", Description: "Model reasoning/thinking chain mode (default: auto)",
				Type: SettingTypeSelect, Category: "Model & Reasoning", DefaultValue: "",
				Options: []SettingOption{
					{Label: "Auto", Value: ""},
					{Label: "Enabled", Value: "enabled"},
					{Label: "Enabled (Preserved)", Value: `{"type":"enabled","clear_thinking":false}`},
					{Label: "Disabled", Value: "disabled"},
					{Label: "DeepSeek: effort=high", Value: `{"thinking":{"type":"enabled"},"reasoning_effort":"high"}`},
					{Label: "DeepSeek: effort=max", Value: `{"thinking":{"type":"enabled"},"reasoning_effort":"max"}`},
				},
			},
			// Subscription management entry (display-only, triggers quick switch)
			{Key: "subscription_manage", Label: "📦 Subscriptions", Type: SettingTypeText, Category: "Model & Reasoning"},

			// ── Model tiers (SubAgent global, cross-subscription) ──
			{Key: "tier_vanguard", Label: "Vanguard (strong)", Description: "Model used when SubAgent specifies vanguard tier, selected across all subscriptions", Type: SettingTypeCombo, Category: "Model Tiers (SubAgent)"},
			{Key: "tier_balance", Label: "Balance (medium)", Description: "Model used when SubAgent specifies balance tier, selected across all subscriptions", Type: SettingTypeCombo, Category: "Model Tiers (SubAgent)"},
			{Key: "tier_swift", Label: "Swift (weak)", Description: "Model used when SubAgent specifies swift tier, selected across all subscriptions", Type: SettingTypeCombo, Category: "Model Tiers (SubAgent)"},

			// ── Agent behavior (user-level) ──
			{
				Key: "enable_stream", Label: "Stream Output", Description: "Use streaming API for LLM calls (on by default)",
				Type: SettingTypeSelect, Category: "Agent behavior", DefaultValue: "true",
				Options: []SettingOption{
					{Label: "On", Value: "true"},
					{Label: "Off", Value: "false"},
				},
			},
			{
				Key: "enable_masking", Label: "Tool Result Masking", Description: "Automatically mask old tool results to free context space (on by default)",
				Type: SettingTypeSelect, Category: "Agent behavior", DefaultValue: "true",
				Options: []SettingOption{
					{Label: "On", Value: "true"},
					{Label: "Off", Value: "false"},
				},
			},
			{
				Key: "compression_threshold", Label: "Compression Threshold", Description: "Context compression trigger ratio (default 0.9)",
				Type: SettingTypeNumber, Category: "Agent behavior", DefaultValue: "0.9",
			},
			{
				Key: "context_mode", Label: "Context Mode", Description: "Context management strategy",
				Type: SettingTypeSelect, Category: "Agent behavior", DefaultValue: "auto",
				Options: []SettingOption{
					{Label: "Auto (default)", Value: "auto"},
					{Label: "Manual compress", Value: "manual"},
					{Label: "No compress", Value: "none"},
				},
			},
			{
				Key: "max_iterations", Label: "Max Iterations", Description: "Max tool call iterations per conversation (default 2000)",
				Type: SettingTypeNumber, Category: "Agent behavior", DefaultValue: "2000",
			},
			{
				Key: "max_concurrency", Label: "Max Concurrency", Description: "Max concurrent requests (default 3)",
				Type: SettingTypeNumber, Category: "Agent behavior", DefaultValue: "3",
			},
			{Key: "auto_worktree", Label: "Auto Worktree Isolation", Description: "Automatically create an isolated git worktree for each session, preventing multi-agent file conflicts. Requires a git repository.", Type: SettingTypeToggle, Category: "Agent behavior", DefaultValue: "false"},

			// ── Appearance (user-level) ──
			{
				Key: "theme", Label: "Theme", Description: "CLI color scheme",
				Type: SettingTypeSelect, Category: "Appearance", DefaultValue: "midnight",
				Options: []SettingOption{
					{Label: "Midnight (default)", Value: "midnight"},
					{Label: "Ocean", Value: "ocean"},
					{Label: "Forest", Value: "forest"},
					{Label: "Sunset", Value: "sunset"},
					{Label: "Rose", Value: "rose"},
					{Label: "Mono", Value: "mono"},
					{Label: "nord:Aurora", Value: "nord"},
					{Label: "dracula:Dark Night", Value: "dracula"},
					{Label: "catppuccin:Mocha", Value: "catppuccin"},
				},
			},
			{
				Key: "language", Label: "Language", Description: "Language for agent replies",
				Type: SettingTypeSelect, Category: "Appearance", DefaultValue: "",
				Options: []SettingOption{
					{Label: "Follow prompt (default)", Value: ""},
					{Label: "English", Value: "en"},
					{Label: "中文", Value: "zh"},
					{Label: "日本語", Value: "ja"},
				},
			},

			// ── Services & Keys (user-level) ──
			{
				Key: "tavily_api_key", Label: "Tavily API Key", Description: "Web search API key (personal config; falls back to global config if empty)",
				Type: SettingTypePassword, Category: "Services & Keys",
			},
			{Key: "runner_panel", Label: "🔧 Runner Manager", Type: SettingTypeText, Category: "Services & Keys"},

			// ── Permissions (global-level) ──
			{Key: "default_user", Label: "Default User", Description: "OS user for LLM tool execution without approval. Leave empty to restrict to current process user (safest). Requires NOPASSWD sudoers", Type: SettingTypeText, Category: "Permissions"},
			{Key: "privileged_user", Label: "Privileged User", Description: "OS user that require human approval when used by LLM. Leave empty to block privilege escalation. Requires NOPASSWD sudoers", Type: SettingTypeText, Category: "Permissions"},

			// ── Danger (action) ──
			{Key: "danger_zone", Label: "⚠️ Danger Zone — Clear Memory", Type: SettingTypeText, Category: "Danger"},
		},
	}
}

func localeJA() *UILocale {
	return &UILocale{
		// --- A. System messages ---
		CancelSent:          "キャンセルリクエストを送信しました",
		QueueCleared:        "%d 件のキューに入ったメッセージをクリアしました",
		SettingsSaved:       "✅ 設定を保存しました",
		NoSettings:          "このチャンネルには設定項目がありません。",
		CheckingUpdate:      "アップデートを確認中...",
		ModelUsage:          "使い方: /set-model <モデル名>\n/models で利用可能モデルを表示",
		AskCancelled:        "質問をキャンセルしました",
		SetupComplete:       "✅ 初期設定が完了しました。/settings で設定変更、/setup で再設定。",
		SetupLettaNote:      "\n\n[!] letta メモリモードには埋め込みサービスが必要です:\n  1. Ollama をインストール: https://ollama.ai\n  2. 埋め込みモデルを取得: `ollama pull nomic-embed-text`\n  3. 設定または環境変数で埋め込みエンドポイントを設定",
		SetupTitle:          "👋 xbot へようこそ！",
		SetupSubtitle:       "AI プロバイダーを選択 → キーを入力するだけ。その他は後で変更できます",
		SetupWelcome:        "🎉 設定完了！AI とチャットを開始できます。\n\n📝 使い方：\n• 下の入力欄に文字を入力し、Enter で送信\n• 改行は Ctrl+J\n• /help でその他の操作を確認\n\n⌨️ よく使うショートカット：\n• Ctrl+K — コマンドパレット（すべての操作）\n• Ctrl+T — セッションの表示・切替\n• Ctrl+N — AI モデル切替\n• Ctrl+C — AI 応答のキャンセル\n\n💡 ヒント：自然な言葉で AI に話しかけてください",
		SetupNoLLM:          "⚠️ AI サービスのキーが未設定です。\n\n/setup で設定、/settings で詳細設定。\n📖 /help でガイダンスを確認。",
		WizardProviderTitle: "AI プロバイダーを選択",
		WizardKeyTitle:      "%s の API キーを取得",
		WizardKeyLabel:      "API キー：",
		WizardDoneTitle:     "🎉 設定完了！",
		WizardStartBtn:      "利用開始",
		WizardNextBtn:       "次へ",
		WizardBackBtn:       "戻る",
		WizardNavHint:       "↑↓ 選択 · Enter 確認 · Esc 戻る",
		UpdateFound:         "新しいバージョン: %s → %s (stable)\nアップデート: curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash\n%s",
		UpdateCurrent:       "現在のバージョン %s (channel: %s) は最新です",
		UpdateFailed:        "アップデート確認に失敗（ネットワークタイムアウトまたは GitHub API に接続できません）",

		// --- B. Panel text ---
		PanelSettingsTitle:   "⚙ 設定",
		PanelNotSet:          "(未設定)",
		PanelEditHint:        "Enter 確認 | Esc キャンセル",
		PanelComboHint:       "↑↓ 選択 | Enter 確認 | カスタム入力 | Esc キャンセル",
		PanelNavHint:         "↑↓ 移動 · Enter 編集/切替 · Ctrl+S 保存 · Esc 閉じる",
		PanelEditPlaceholder: "> 新しい値を入力...",
		PanelBtnGetKey:       "🔑 クリックしてキーを取得",
		PanelBtnSave:         "💾 保存",
		PanelBtnCancel:       "✖ キャンセル",
		ProviderHints: map[string]string{
			"openai":       "👉 上のリンクを開く → ログイン → Create new secret key → コピー",
			"anthropic":    "👉 上のリンクを開く → ログイン → Create Key → コピー",
			"openrouter":   "👉 上のリンクを開く → ログイン → Create Key → コピー",
			"google":       "👉 上のリンクを開く → ログイン → Create API Key → コピー",
			"deepseek":     "👉 上のリンクを開く → ログイン → API Key を作成 → コピー",
			"zhipu":        "👉 上のリンクを開く → ログイン → API Key を追加 → コピー",
			"zhipu_coding": "👉 上のリンクを開く → ログイン → API Key を作成（sk-sp- は Coding Plan 専用）",
			"siliconflow":  "👉 上のリンクを開く → ログイン → API Key を追加 → コピー",
			"moonshot":     "👉 上のリンクを開く → ログイン → API Key を作成 → コピー",
			"xiaomi":       "👉 上のリンクを開く → 登録/ログイン → Token Plan キーを取得",
			"ollama":       "✅ キー不要！Ollama（ollama.com）をインストールしてモデルを実行",
		},
		PanelToggleOn:  "● オン",
		PanelToggleOff: "○ オフ",

		PanelOther:      "その他: ",
		PanelSubmit:     "送信 →",
		PanelAskNav:     "←→/Tab 質問切替",
		PanelAskToggle:  "Space/Enter 切替",
		PanelAskOther:   "v その他入力",
		PanelAskSubmit:  "Enter 送信",
		PanelAskNewline: "Ctrl+J 改行",
		PanelAskCancel:  "Esc キャンセル",

		BgTasksTitle:       "📋 Tasks",
		BgTasksHelp:        "↑↓ 移動  Enter ログ表示  Del 終了  Esc 閉じる",
		BgTasksEmpty:       "実行中のタスクやエージェントはありません",
		BgTasksUnsupported: "バックグラウンドタスクは未対応です。",
		BgTaskLogTitle:     "ログ: %s — %s",
		BgTaskLogHelp:      "↑↓ スクロール  Esc 戻る",
		BgTaskLogMore:      "... あと %d 行（↑↓ スクロール）",

		PanelOmitted:          "  ... %d 行省略（端末を拡大してください） ...",
		PanelOtherPlaceholder: "ここに入力...",
		EmergencyQuitHint:     "🚪 緊急終了 (Ctrl+Z)",

		// --- C. Status bar ---
		TitleHint:         "Enter 送信 · Ctrl+J 改行 · /help",
		ShiftSelectHint:   "⇧+ドラッグでテキスト選択",
		CheckingUpdates:   "⟳ アップデート確認中...",
		StatusReady:       "◈ 準備完了",
		StatusCompressing: "圧縮中",
		StatusNewing:      "リセット中",
		StatusRetrying:    "リトライ中",
		StatusDone:        "完了",
		NewContentHint:    "↓ 新着",
		BgTaskRunning:     "[^ %dタ]",
		AgentRunning:      "[Ctrl+T %dエ]",
		TabNoMatch:        "[Tab] 一致するファイルなし",

		// --- C2. Info Bar (bottom status line below input) ---

		// --- D. Temp status ---
		WaitingOperation:   "... 前の操作の完了を待機中...",
		NoMessagesToDelete: "[!] 削除するメッセージがありません",
		KillFailed:         "終了に失敗: %s",

		// --- E. Help commands ---
		HelpTitle:          "xbot ヘルプ",
		HelpCommandsTitle:  " コマンド ",
		HelpShortcutsTitle: " ショートカット ",
		HelpCmds: []HelpCmdEntry{
			{Cmd: "/commands", Desc: "コマンドパレットを開く (Ctrl+K)"},
			{Cmd: "/cancel", Desc: "現在の操作をキャンセル"},
			{Cmd: "/clear", Desc: "チャット履歴をクリア"},
			{Cmd: "/compress", Desc: "コンテキストを圧縮"},
			{Cmd: "/set-model", Desc: "モデル切替"},
			{Cmd: "/models", Desc: "利用可能モデル一覧"},
			{Cmd: "/set-llm", Desc: "個人 LLM サブスクリプション作成/更新"},
			{Cmd: "/unset-llm", Desc: "個人デフォルトサブスクリプション削除"},
			{Cmd: "/llm", Desc: "現在のサブスクリプションとモデルを表示"},
			{Cmd: "/usage", Desc: "トークン使用量を表示"},
			{Cmd: "/new", Desc: "新規セッション開始"},
			{Cmd: "/rename", Desc: "現在のセッション名を変更"},
			{Cmd: "/rewind", Desc: "会話を巻き戻す"},
			{Cmd: "/settings", Desc: "設定パネルを開く"},
			{Cmd: "/menu", Desc: "メインメニュー（設定/マーケット/自分）"},
			{Cmd: "/browse", Desc: "Skill/Agent マーケットを閲覧"},
			{Cmd: "/install", Desc: "マーケットから Skill/Agent をインストール"},
			{Cmd: "/uninstall", Desc: "Skill/Agent をアンインストール"},
			{Cmd: "/publish", Desc: "Skill/Agent をマーケットへ公開"},
			{Cmd: "/unpublish", Desc: "公開を取り消す"},
			{Cmd: "/my", Desc: "自分の公開/インストール済み Skills/Agents"},
			{Cmd: "/update", Desc: "アップデート確認"},
			{Cmd: "/help", Desc: "ヘルプ表示"},
		},
		HelpKeys: []HelpKeyEntry{
			{Key: "Ctrl+K", Desc: "コマンドパレット（全操作の入口）"},
			{Key: "Ctrl+N", Desc: "LLM パネル（モデル切替 + サブスクリプション管理）"},
			{Key: "Ctrl+T", Desc: "セッション一覧"},
			{Key: "Ctrl+O", Desc: "ツール展開/折りたたみ"},
			{Key: "Ctrl+J", Desc: "入力欄で改行"},
			{Key: "Tab", Desc: "コマンド/パス補完"},
			{Key: "Shift+↑", Desc: "キューメッセージを編集"},
			{Key: "^", Desc: "バックグラウンドタスクパネル"},
			{Key: "Ctrl+C", Desc: "キャンセル / キュー削除"},
		},

		// --- E2. Fold messages (§19) ---
		MsgTooShortToFold: "メッセージが短すぎます（%d 行を超える必要があります）",
		MsgExpanded:       "展開しました",
		MsgCollapsed:      "折りたたみました",

		// --- E3. Search (§21) ---
		SearchPlaceholder: "メッセージを検索...",
		SearchResults:     "%d 件の一致メッセージが見つかりました (n/N で移動, Esc で終了)",
		SearchNoResults:   "一致するメッセージが見つかりません",
		SearchNavFormat:   "/ %s  [%d/%d]  n 次 · N 前 · Esc",

		// --- F. Confirm dialog ---
		RewindTitle: "Rewind",
		RewindHint:  "巻き戻すメッセージを選択してください。内容が入力欄に配置されます。",

		// --- G. Splash ---
		SplashDesc:     "AI駆動のターミナルエージェント",
		SplashLoading:  "  %s  初期化中...",
		SplashFirstRun: "👋 xbot へようこそ！初期設定を準備しています...",

		// --- H. Footer keys ---
		FooterScroll:   "スクロール",
		FooterBack:     "戻る",
		FooterNavigate: "移動",
		FooterLog:      "ログ",
		FooterKill:     "終了",
		FooterClose:    "閉じる",
		FooterCancel:   "キャンセル",
		FooterPalette:  "パレット",
		FooterCommands: "コマンド",
		FooterComplete: "補完",
		FooterBgTasks:  "タスク/エージェント",
		FooterNewline:  "改行",
		FooterSelect:   "選択",
		FooterManage:   "管理",
		FooterHistory:  "履歴",
		FooterSearch:   "検索",
		FooterFold:     "折りたたみ",
		FooterUnfold:   "展開",

		// --- K. Runner panel ---
		RunnerPanelTitle:           "Runner 管理",
		RunnerStatusConnected:      "接続済み",
		RunnerConnecting:           "接続中...",
		RunnerDisconnect:           "接続解除",
		RunnerDisconnectAction:     "切断",
		RunnerConnectSuccess:       "✅ Runner 接続完了",
		RunnerConnectFailed:        "❌ Runner 接続失敗: %s",
		RunnerServerLabel:          "Server URL",
		RunnerTokenLabel:           "Token",
		RunnerWorkspaceLabel:       "ワークスペース",
		RunnerServerPlaceholder:    "ws://host:port/ws/userID",
		RunnerTokenPlaceholder:     "認証 Token",
		RunnerWorkspacePlaceholder: "共有ワークスペースパス",
		RunnerServerRequired:       "Server URL は必須です",
		RunnerWorkspaceRequired:    "ワークスペースは必須です",
		RunnerPleaseWait:           "お待ちください...",
		RunnerNavHint:              "↑↓/Tab フィールド切替  Enter 接続  Esc 戻る",
		RunnerNotAvailable:         "Runner 機能は利用できません",
		RunnerLogLabel:             "📋 ログ",
		RunnerBack:                 "戻る",

		// --- L. /su command ---
		SuAlreadyDefault:  "既にデフォルト ID です",
		SuSwitched:        "✅ ID を切り替えました: %s",
		SuSwitchedHistory: "✅ ID を切り替えました: %s — %d 件の履歴を読み込みました",
		SuLoadFailed:      "⚠️ 履歴の読み込みに失敗: %v",
		SuSwitching:       "ID 切り替え中: %s",
		SuLoadingHistory:  "  %s  履歴を読み込み中...",

		// --- L2. Reconnect overlay ---
		ReconnectTitle:  "接続が切断されました",
		ReconnectingMsg: "  %s  サーバーに再接続中...",
		ReconnectedMsg:  "  %s  再接続しました!",
		ReconnectHint:   "Ctrl+Z で終了",

		// --- M. Danger zone ---
		DangerTitle:              "⚠ 危険エリア",
		DangerConfirmClear:       "クリア確認: %s",
		DangerIrreversible:       "この操作は取り消せません",
		DangerTypeConfirm:        "%s を入力して確認:",
		DangerNavHint:            "↑↓ 選択  Enter 確認  Esc 戻る",
		DangerMismatch:           "❌ 確認テキストが一致しません",
		DangerClearFailed:        "❌ クリア失敗: %v",
		DangerCleared:            "✅ クリア完了: %s",
		DangerConfirmPlaceholder: "確認テキストを入力...",
		DangerSessionHistory:     "セッション履歴",
		DangerCoreAll:            "Core Memory: 全て",
		DangerLongTerm:           "長期記憶",
		DangerEventHistory:       "イベント履歴",
		DangerArchival:           "アーカイブ記憶（ベクトルDB）",

		// --- N. Message queue ---
		MessageQueued:    "⏳ メッセージをキューに入れました（%d 件保留中）",
		MessageQueuedUp:  "⏳ メッセージをキューに入れました（%d 件保留中）— Shift+↑ 編集 · Esc キャンセル",
		QueuePending:     "📬 %d 件キュー中",
		QueueItemRemoved: "キューのメッセージを削除：%s",

		// --- I. Dynamic arrays ---
		ThinkingVerbs: []string{"思考中", "推論中", "分析中", "検討中", "評価中", "振り返り", "処理中", "熟考中"},
		IdlePlaceholders: []string{
			"Enter 送信 · Ctrl+J 改行 · /help",
			"試してみて：Python スクリプトを書いて",
			"Ctrl+K コマンドパレット",
			"Ctrl+T セッション · Ctrl+K コマンド",
			"@filepath でファイル添付",
			"Ctrl+N モデル切替",
			"Ctrl+K → 全コマンド",
			"/help で全ショートカットを表示",
			"自然な言葉で AI に話しかけてください",
		},

		// --- J. Settings schema ---
		SetupSchema: []SettingDefinition{

			{
				Key: "llm_provider", Label: "AI プロバイダー", Description: "AI サービスを選択してください。迷ったら DeepSeek がおすすめ",
				Type: SettingTypeCombo, Category: "LLM", DefaultValue: "deepseek",
				Options: []SettingOption{
					{Label: "DeepSeek", Value: "deepseek", Description: "中国直結 · 高性能 · 新規無料枠あり"},
					{Label: "Zhipu (ChatGLM)", Value: "zhipu", Description: "中国直結 · GLM シリーズ"},
					{Label: "Zhipu Coding Plan", Value: "zhipu_coding", Description: "コーディング専用 · コード最適化 · 別途購入必要"},
					{Label: "SiliconFlow", Value: "siliconflow", Description: "中国集約プラットフォーム · 複数モデル"},
					{Label: "Moonshot (Kimi)", Value: "moonshot", Description: "中国直結 · Kimi シリーズ"},
					{Label: "Xiaomi MiMo (Token Plan)", Value: "xiaomi", Description: "トークン課金 · 月額プラン · V2.5 シリーズ対応"},
					{Label: "OpenAI (ChatGPT)", Value: "openai", Description: "海外ネットワークが必要"},
					{Label: "Anthropic (Claude)", Value: "anthropic", Description: "海外ネットワークが必要"},
					{Label: "OpenRouter", Value: "openrouter", Description: "集約プラットフォーム · 複数モデル利用可能"},
					{Label: "Google AI (Gemini)", Value: "google", Description: "海外ネットワークが必要"},
					{Label: "Ollama (ローカル)", Value: "ollama", Description: "インターネット不要 · Ollama のインストールが必要"},
					{Label: "カスタム (OpenAI 互換)", Value: "custom", Description: "その他の OpenAI 互換 AI サービス"},
				},
			},
			{
				Key: "llm_api_key", Label: "API キー",
				Description: "AI サービスの認証キー。各プロバイダーのサイトで登録して取得",
				Type:        SettingTypePassword, Category: "LLM",
			},
			{
				Key: "llm_model", Label: "AI モデル",
				Description: "通常は自動入力されます。特定のモデルを使いたい場合に変更",
				Type:        SettingTypeCombo, Category: "LLM",
			},
			{
				Key: "llm_base_url", Label: "サーバー URL",
				Description: "通常は自動入力。カスタムサービスの場合のみ変更",
				Type:        SettingTypeText, Category: "LLM",
				DependsOnKey:    "llm_provider",
				DependsOnValues: "ollama,custom",
			},
			{
				Key: "theme", Label: "カラーテーマ", Description: "お好みのカラースキームを選択",
				Type: SettingTypeSelect, Category: "外観", DefaultValue: "midnight",
				Options: []SettingOption{
					{Label: "Midnight（デフォルト）", Value: "midnight"},
					{Label: "Ocean", Value: "ocean"},
					{Label: "Forest", Value: "forest"},
					{Label: "Sunset", Value: "sunset"},
					{Label: "Rose", Value: "rose"},
					{Label: "Mono", Value: "mono"},
					{Label: "nord:オーロラ", Value: "nord"},
					{Label: "dracula:ダークナイト", Value: "dracula"},
					{Label: "catppuccin:モカ", Value: "catppuccin"},
				},
			},
		},
		SettingsSchema: []SettingDefinition{
			// ── モデルと推論（ユーザーレベル + サブスクリプション入口）──
			{
				Key: "thinking_mode", Label: "思考モード", Description: "モデルの推論/思考チェーンモード（デフォルト: 自動）",
				Type: SettingTypeSelect, Category: "モデルと推論", DefaultValue: "",
				Options: []SettingOption{
					{Label: "自動", Value: ""},
					{Label: "有効", Value: "enabled"},
					{Label: "有効（推論保持）", Value: `{"type":"enabled","clear_thinking":false}`},
					{Label: "無効", Value: "disabled"},
					{Label: "DeepSeek: effort=high", Value: `{"thinking":{"type":"enabled"},"reasoning_effort":"high"}`},
					{Label: "DeepSeek: effort=max", Value: `{"thinking":{"type":"enabled"},"reasoning_effort":"max"}`},
				},
			},
			// Subscription management entry (display-only, triggers quick switch)
			{Key: "subscription_manage", Label: "📦 サブスクリプション管理", Type: SettingTypeText, Category: "モデルと推論"},

			// ── モデルティア（SubAgent グローバル設定、サブスクリプション横断）──
			{Key: "tier_vanguard", Label: "Vanguard（強）", Description: "SubAgent が vanguard ティアを指定した場合に使用するモデル、全サブスクリプションから選択", Type: SettingTypeCombo, Category: "モデルティア (SubAgent)"},
			{Key: "tier_balance", Label: "Balance（中）", Description: "SubAgent が balance ティアを指定した場合に使用するモデル、全サブスクリプションから選択", Type: SettingTypeCombo, Category: "モデルティア (SubAgent)"},
			{Key: "tier_swift", Label: "Swift（弱）", Description: "SubAgent が swift ティアを指定した場合に使用するモデル、全サブスクリプションから選択", Type: SettingTypeCombo, Category: "モデルティア (SubAgent)"},

			// ── Agent 動作（ユーザーレベル）──
			{
				Key: "enable_stream", Label: "ストリーム出力", Description: "ストリーミング API で LLM を呼び出し（デフォルト: オン）",
				Type: SettingTypeSelect, Category: "Agent 動作", DefaultValue: "true",
				Options: []SettingOption{
					{Label: "オン", Value: "true"},
					{Label: "オフ", Value: "false"},
				},
			},
			{
				Key: "enable_masking", Label: "ツール結果マスキング", Description: "コンテキストが大きい場合に古いツール結果を自動マスキング（デフォルト: オン）",
				Type: SettingTypeSelect, Category: "Agent 動作", DefaultValue: "true",
				Options: []SettingOption{
					{Label: "オン", Value: "true"},
					{Label: "オフ", Value: "false"},
				},
			},
			{
				Key: "compression_threshold", Label: "圧縮閾値", Description: "コンテキスト圧縮のトリガー比率（デフォルト 0.9）",
				Type: SettingTypeNumber, Category: "Agent 動作", DefaultValue: "0.9",
			},
			{
				Key: "context_mode", Label: "コンテキストモード", Description: "コンテキスト管理戦略",
				Type: SettingTypeSelect, Category: "Agent 動作", DefaultValue: "auto",
				Options: []SettingOption{
					{Label: "自動（デフォルト）", Value: "auto"},
					{Label: "手動圧縮", Value: "manual"},
					{Label: "圧縮なし", Value: "none"},
				},
			},
			{
				Key: "max_iterations", Label: "最大反復数", Description: "1回の会話の最大ツール呼び出し反復数（デフォルト 2000）",
				Type: SettingTypeNumber, Category: "Agent 動作", DefaultValue: "2000",
			},
			{
				Key: "max_concurrency", Label: "最大同時実行数", Description: "同時に処理する最大リクエスト数（デフォルト 3）",
				Type: SettingTypeNumber, Category: "Agent 動作", DefaultValue: "3",
			},
			{Key: "auto_worktree", Label: "自動 Worktree 分離", Description: "各セッションに独立した git worktree を自動作成し、マルチエージェントのファイル競合を防止します。git リポジトリが必要です。", Type: SettingTypeToggle, Category: "Agent 動作", DefaultValue: "false"},

			// ── 外観（ユーザーレベル）──
			{
				Key: "theme", Label: "テーマ", Description: "CLI カラースキーム",
				Type: SettingTypeSelect, Category: "外観", DefaultValue: "midnight",
				Options: []SettingOption{
					{Label: "Midnight（デフォルト）", Value: "midnight"},
					{Label: "Ocean", Value: "ocean"},
					{Label: "Forest", Value: "forest"},
					{Label: "Sunset", Value: "sunset"},
					{Label: "Rose", Value: "rose"},
					{Label: "Mono", Value: "mono"},
					{Label: "nord:オーロラ", Value: "nord"},
					{Label: "dracula:ダークナイト", Value: "dracula"},
					{Label: "catppuccin:モカ", Value: "catppuccin"},
				},
			},
			{
				Key: "language", Label: "言語", Description: "エージェントの返信言語",
				Type: SettingTypeSelect, Category: "外観", DefaultValue: "",
				Options: []SettingOption{
					{Label: "プロンプトに従う（デフォルト）", Value: ""},
					{Label: "English", Value: "en"},
					{Label: "中文", Value: "zh"},
					{Label: "日本語", Value: "ja"},
				},
			},

			// ── サービスとキー（ユーザーレベル）──
			{
				Key: "tavily_api_key", Label: "Tavily API Key", Description: "Web検索APIキー（個人設定、空の場合はグローバル設定にフォールバック）",
				Type: SettingTypePassword, Category: "サービスとキー",
			},
			{Key: "runner_panel", Label: "🔧 Runner 管理", Type: SettingTypeText, Category: "サービスとキー"},

			// ── 権限（グローバルレベル）──
			{Key: "default_user", Label: "デフォルトユーザー", Description: "LLMが承認なしでツールを実行できるOSユーザー。空の場合は現在のプロセスユーザーに制限（最も安全）。NOPASSWD sudoersが必要", Type: SettingTypeText, Category: "権限"},
			{Key: "privileged_user", Label: "特権ユーザー", Description: "LLMが使用時に人間の承認が必要なOSユーザー。空の場合は権限昇格を禁止。NOPASSWD sudoersが必要", Type: SettingTypeText, Category: "権限"},

			// ── 危険（アクション）──
			{Key: "danger_zone", Label: "⚠️ 危険エリア — 記憶クリア", Type: SettingTypeText, Category: "危険"},
		},
	}
}

func init() {
	locales = map[string]*UILocale{
		"":   localeEN(),
		"zh": LocaleZH(),
		"en": localeEN(),
		"ja": localeJA(),
	}
}

// localeChangeCh is used to notify the running CLI model of locale changes.
// Follows the same pattern as themeChangeCh (buffered channel, non-blocking send).
var localeChangeCh = make(chan struct{}, 1)

// LocaleChangeCh returns the locale change notification channel.
// Sub-packages (cli) listen on this to reload UI after locale changes.
func LocaleChangeCh() chan struct{} { return localeChangeCh }

// currentLocaleLang stores the current locale language code.
var currentLocaleLang string

// setLocale updates currentLocaleLang without sending on localeChangeCh.
// Use when the caller handles the locale change synchronously (e.g. applyLanguageChange),
// to avoid a redundant fullRebuild in the next Update cycle.
func setLocale(lang string) {
	currentLocaleLang = lang
}

// SetLocale switches the current locale and notifies the running model.
func SetLocale(lang string) {
	setLocale(lang)
	select {
	case localeChangeCh <- struct{}{}:
	default:
	}
}

// CurrentLocaleLang returns the current locale language code.
// Exported for use by sub-packages (feishu, etc.) that need locale-aware settings.
func CurrentLocaleLang() string {
	return currentLocaleLang
}

// GetLocale returns the UILocale for the given language code.
// When lang is empty (no language configured, e.g. first run), it detects
// the default language from the system timezone: CST (UTC+8) → Chinese,
// otherwise English. Explicit language codes always take precedence.
func GetLocale(lang string) *UILocale {
	if loc, ok := locales[lang]; ok {
		return loc
	}
	// No language configured — infer from timezone.
	if lang == "" {
		_, offset := time.Now().Zone()
		// UTC+8 zones: CST (China Standard Time), HKT, SGT, etc.
		// Offset is in seconds: UTC+8 = 28800
		if offset >= 25200 && offset <= 32400 { // UTC+7 to UTC+9
			return locales["zh"]
		}
	}
	return locales["en"]
}

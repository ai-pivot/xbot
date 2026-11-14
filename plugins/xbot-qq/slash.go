package main

// slash / bang 命令门禁。
//
// 背景：host 侧确实支持"渠道消息 → 斜杠命令"。链路（只读核对，未改主模块）：
//
//	agent/agent.go:3275   if cmd := a.commands.Match(msg.Content); cmd != nil {
//	agent/agent.go:3286   if commandRequiresAdmin(cmd) && !a.isAdminSender(...) { 拒绝 }
//	agent/command.go:74   CommandRegistry.Match（先 TrimSpace，再逐条 Match）
//	agent/command_builtin.go:569-600 内置命令注册表
//	agent/agent.go:1555   isCancelCommand（/cancel 与别名 /stop，在 Run 入口拦截）
//	config/config.go:430  AgentConfig.IsAdminSender（cli/web 恒 admin；其他渠道看 agent.admins）
//
// 关键事实（决定了本文件为什么必须存在）：
//
//  1. host 只对 AdminOnly 命令做拦截；/compress、/new、bang 等都不是 AdminOnly，
//     所以"非 admin 不能发命令"必须由渠道侧先拦。
//  2. **未知 `/xxx` 会掉进普通消息路径**（Match 返回 nil → 直接进 LLM）。
//     所以未知命令绝不能原样转发，否则 `/echo hi` 就变成了一次模型调用 ——
//     既是资费/延迟问题，也把"命令"偷偷变成了 prompt 注入面。
//
// 门禁规则（准入与权限分离）：
//
//	未知命令              → 拒绝（连 admin 也拒），理由=未知命令；不转发（防落进 LLM）
//	admin  + allow        → 放行（交给 host 命令注册表；危险命令同样放行）
//	admin  + !allow       → 拒绝，理由=不在白名单（不是"你不是管理员"）
//	!admin（无论 allow）  → 拒绝，理由=斜杠命令仅管理员可用
//
// 被拒绝的命令：**不产生 send_inbound**（因此宿主不会执行、不会压缩、不会写状态），
// 只回一条 QQ 文本（这是唯一的对外调用，且只针对用户显式发起的命令）。
//
// 渠道侧别名（host 只认自己注册表里的名字）：
//
//	/compact → /compress   同义（host 只有 /compress）
//	/reset   → /new        重置会话 = 新对话（host 没有 /reset）
//	/quit    → /stop       渠道进程由 host 托管，用户不能"退出"；quit 降级为中断当前 turn

import (
	"sort"
	"strings"
)

// 命令风险分类（仅用于日志与拒绝文案；放行判定只看 unknown/admin/allow）。
const (
	RiskInfo        = "info"
	RiskSession     = "session"
	RiskCompression = "compression"
	RiskConfig      = "config"
	RiskCredentials = "credentials"
	RiskService     = "service"
	RiskDestructive = "destructive"
	RiskUnknown     = "unknown"
)

// commandSpec 描述一个已知命令。
type commandSpec struct {
	Category string
	// Dangerous 表示该命令可能改配置/动凭据/重启服务/删数据，或直接执行 shell。
	Dangerous bool
	// Alias 非空时把该命令改名后再交给 host（host 只认自身注册表里的名字）。
	Alias string
}

// knownCommands 是 host 内置命令的风险画像。
//
// 清单只读核对自 agent/command_builtin.go:569-600 的 registerBuiltinCommands，
// 加上 agent/agent.go:1555 的 isCancelCommand（/cancel 与 /stop）。本插件不改 host。
//
// 不在表里的 `/xxx` 一律按未知处理并被拒绝 —— 这是故意的：host 对未知斜杠
// 命令会走普通消息路径（= 一次模型调用），所以"白名单式"放行是唯一安全解。
var knownCommands = map[string]commandSpec{
	// 会话
	"/new":      {Category: RiskSession},
	"/continue": {Category: RiskSession},
	"/context":  {Category: RiskSession},
	"/export":   {Category: RiskSession},
	"/goal":     {Category: RiskSession},
	"/cancel":   {Category: RiskSession},
	"/stop":     {Category: RiskSession},

	// 只读信息
	"/version": {Category: RiskInfo},
	"/help":    {Category: RiskInfo},
	"/prompt":  {Category: RiskInfo},
	"/info":    {Category: RiskInfo},
	"/models":  {Category: RiskInfo},
	"/usage":   {Category: RiskInfo},

	// 上下文压缩
	"/compress": {Category: RiskCompression},
	"/compact":  {Category: RiskCompression, Alias: "/compress"},

	// 配置 / 凭据 / 插件（host 侧多为 AdminOnly，渠道侧仍需先过 admin 门禁）
	"/set-llm":   {Category: RiskCredentials, Dangerous: true},
	"/unset-llm": {Category: RiskCredentials, Dangerous: true},
	"/llm":       {Category: RiskCredentials, Dangerous: true},
	"/llms":      {Category: RiskCredentials, Dangerous: true},
	"/set-model": {Category: RiskConfig, Dangerous: true},
	"/settings":  {Category: RiskConfig, Dangerous: true},
	"/plugin":    {Category: RiskConfig, Dangerous: true},

	// 渠道侧别名（host 没有这两个命令；不映射的话它们会落进 LLM 路径）
	"/reset": {Category: RiskSession, Alias: "/new"},
	"/quit":  {Category: RiskSession, Alias: "/stop"},
}

// Command 是一条被识别出的斜杠 / bang 命令。
type Command struct {
	// Raw 是原始正文（TrimSpace 后）。
	Raw string
	// Name 是规范化的命令名："/" 前缀 + 第一段小写（含参数不影响），例如
	// "/compress"、"!"（bang 不细分）。
	Name string
	// Category / Dangerous 见 commandSpec。
	Category  string
	Dangerous bool
	// Known 表示该命令在 host 注册表里存在（或已被本插件映射到存在的命令）。
	// Known=false 时**禁止转发**：host 会把它当普通消息喂给模型。
	Known bool
	// UserForm 是**面向用户的**命令形态，一律 "!" 前缀（"!rm"、"/compress"→"!compress"）。
	// 用户可见文案只能用 UserForm —— 不能把 host 内部名（"/compress"）暴露给用户。
	UserForm string
	// Forward 是要交给 host 的正文：默认等于 Raw；别名命令会被改写。
	Forward string
}

// ClassifyCommand 判断一条消息是不是命令。
//
// 前缀约定（与 host 一致）："/" 是斜杠命令，"!" 是 bang 命令（host 的 bangCmd
// 直接在 sandbox 执行 shell —— 同样只允许 admin）。
//
// 边界行为（有测试）：容忍首尾空白；命令名大小写不敏感；命令名与参数之间可
// 有任意个空格（参数内部空格原样保留）；全角斜杠不当作命令。
// 不是命令时返回 ok=false。
func ClassifyCommand(content string) (Command, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return Command{}, false
	}
	var prefix string
	switch trimmed[0] {
	case '/':
		prefix = "/"
	case '!':
		prefix = "!"
	default:
		return Command{}, false
	}

	// 取第一段作为命令名："/set-llm foo api_key=bar" → "/set-llm"。
	fields := strings.Fields(trimmed)
	firstToken := fields[0]

	if prefix == "!" {
		// bang 命令整体当一条 shell 命令看待，不细分，由 host 的 bangCmd 执行。
		return Command{
			Raw:       trimmed,
			Name:      "!",
			UserForm:  userFormOf(firstToken),
			Category:  RiskDestructive,
			Dangerous: true,
			Known:     true,
			Forward:   trimmed,
		}, true
	}

	name := strings.ToLower(firstToken)
	spec, known := knownCommands[name]
	if !known {
		// 未知命令：不猜风险等级，按最危险记日志；Known=false 让调用方拒绝。
		spec = commandSpec{Category: RiskUnknown, Dangerous: true}
	}

	forward := trimmed
	if spec.Alias != "" {
		// 只替换命令名，保留参数（如 "/compact 一下" → "/compress 一下"）。
		// 用原始 token 裁剪而不是重新拼 fields，保留参数内部原始空白。
		forward = spec.Alias + strings.TrimPrefix(trimmed, firstToken)
	}

	return Command{
		Raw:       trimmed,
		Name:      name,
		UserForm:  userFormOf(firstToken),
		Category:  spec.Category,
		Dangerous: spec.Dangerous,
		Known:     known,
		Forward:   forward,
	}, true
}

// userFormOf 把用户输入的命令 token 转成**面向用户的** "!" 形态。
//
// 为什么必须转：用户输入的是 "!rm"，而 host 内部命令名带斜杠（"/compress"）。
// 文案里直接显示 "/rm"、"/cancel" 会和用户看到的东西自相矛盾。
// 显示层统一用 "!" —— 与用户在 QQ 里实际敲的形态一致。
func userFormOf(token string) string {
	name := strings.ToLower(strings.TrimSpace(token))
	name = strings.TrimPrefix(name, "/")
	name = strings.TrimPrefix(name, "!")
	return "!" + name
}

// 拒绝原因（写进日志；也决定用户可见文案）。
const (
	RefuseNotAdmin       = "not_admin"
	RefuseNotAllowlisted = "not_allowlisted"
	RefuseUnknown        = "unknown_command"
)

// allowedCommandNames 返回**面向用户**（"!" 形态）的命令名，排序后用于未知命令提示。
//
// 显示层统一 "!"：用户敲的是 "!rm"，回一句 "/cancel /compact" 等于教用户
// 敲他自己没敲过的形态，文案自相矛盾。
func allowedCommandNames() []string {
	names := make([]string, 0, len(knownCommands))
	for name := range knownCommands {
		names = append(names, userFormOf(name))
	}
	sort.Strings(names)
	return names
}

// refusalText 返回用户可见的拒绝文案。name 必须是 cmd.UserForm（"!" 形态）。
//
// 三种理由必须区分：
//   - 未知命令（仅 admin 可见）：要说清"被拦截了、没有送给模型"，否则用户会以为插件坏了；
//   - 管理员但不在白名单：不能回"你不是管理员"（那会让人以为 admins 配错了）；
//   - 非管理员：只说需要管理员权限，**不列命令清单**（非 admin 不该知道有哪些命令）。
func refusalText(userForm, reason string) string {
	switch reason {
	case RefuseUnknown:
		return "⛔ 未知命令（" + userForm + "）：xbot 未注册该命令，已拦截（不会当作普通消息送给模型）。" +
			"可用命令：" + strings.Join(allowedCommandNames(), " ")
	case RefuseNotAllowlisted:
		return "⛔ 你不在本渠道白名单（allow）里，管理员身份不豁免准入。请让管理员把你的 openid 加入 allow。"
	default:
		return "⛔ 需要管理员权限（" + userForm + "）：本渠道的命令仅对管理员开放。你的 openid 不在本渠道 admins 名单里。"
	}
}

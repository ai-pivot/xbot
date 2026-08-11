package channel

import "xbot/protocol"

// TUISlashCommands is retained for source compatibility with integrations that
// consume or extend the TUI-local completion list.
// Deprecated: use TUICommandList for complete presentation metadata.
var TUISlashCommands = []string{
	"/cancel", "/channel", "/chat", "/clear", "/commands", "/copy", "/exit",
	"/help", "/list-sessions", "/palette", "/plugin",
	"/quit", "/rename", "/rewind", "/search", "/sessions", "/settings", "/setup",
	"/ss", "/su", "/tasks", "/update", "/user",
}

var additionalTUICommands = []string{
	"/compress", "/context", "/debug", "/link-account", "/new", "/unlink-account",
	"/usage", "/version",
}

var tuiCommandMetadata = map[string]protocol.CommandInfo{
	"/cancel":         {Usage: "/cancel", Description: "取消当前操作"},
	"/channel":        {Usage: "/channel", Description: "打开渠道配置面板"},
	"/chat":           {Usage: "/chat new [name] | /chat <id> | /chat ls", Description: "创建、切换或列出会话"},
	"/clear":          {Usage: "/clear", Description: "清空当前 TUI 显示"},
	"/commands":       {Usage: "/commands", Description: "打开命令面板"},
	"/copy":           {Usage: "/copy [last|all]", Description: "复制消息到剪贴板"},
	"/compress":       {Usage: "/compress", Description: "手动触发上下文压缩"},
	"/context":        {Usage: "/context", Description: "查看上下文统计"},
	"/debug":          {Usage: "/debug [stats|mem|goroutines|heap|profile|gc|gc-force]", Description: "查看运行时诊断信息"},
	"/exit":           {Usage: "/exit", Description: "退出 xbot-cli"},
	"/help":           {Usage: "/help", Description: "显示帮助"},
	"/link-account":   {Usage: "/link-account [code]", Description: "生成或消费账号关联码"},
	"/list-sessions":  {Usage: "/list-sessions", Description: "列出后端全部会话（仅管理员）"},
	"/new":            {Usage: "/new", Description: "归档记忆并重置当前对话"},
	"/palette":        {Usage: "/palette", Description: "打开命令面板"},
	"/plugin":         {Usage: "/plugin [subcommand]", Description: "管理插件"},
	"/quit":           {Usage: "/quit", Description: "退出 xbot-cli"},
	"/rename":         {Usage: "/rename <name>", Description: "重命名当前会话"},
	"/rewind":         {Usage: "/rewind", Description: "回退对话"},
	"/search":         {Usage: "/search", Description: "搜索消息历史"},
	"/sessions":       {Usage: "/sessions", Description: "打开会话面板"},
	"/settings":       {Usage: "/settings", Description: "打开设置面板"},
	"/setup":          {Usage: "/setup", Description: "打开初始设置向导"},
	"/ss":             {Usage: "/ss", Description: "打开会话面板"},
	"/su":             {Usage: "/su [user-id|channel:chat-id]", Description: "切换身份或会话"},
	"/tasks":          {Usage: "/tasks", Description: "打开后台任务与 Agent 面板"},
	"/unlink-account": {Usage: "/unlink-account", Description: "查看当前账号关联身份"},
	"/update":         {Usage: "/update", Description: "检查更新"},
	"/usage":          {Usage: "/usage", Description: "查看 token 用量统计"},
	"/user":           {Usage: "/user [subcommand]", Description: "管理 Web 用户"},
	"/version":        {Usage: "/version", Description: "显示版本信息"},
}

// TUICommandList returns the TUI-local command metadata. Agent-level commands
// are supplied by the Agent CommandRegistry and merged by each presentation
// surface without changing either execution path.
func TUICommandList() []protocol.CommandInfo {
	names := append([]string(nil), TUISlashCommands...)
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		seen[name] = struct{}{}
	}
	for _, name := range additionalTUICommands {
		if _, exists := seen[name]; !exists {
			names = append(names, name)
		}
	}

	commands := make([]protocol.CommandInfo, 0, len(names))
	for _, name := range names {
		if info, ok := tuiCommandMetadata[name]; ok {
			info.Name = name
			commands = append(commands, info)
			continue
		}
		commands = append(commands, protocol.CommandInfo{Name: name, Usage: name})
	}
	return commands
}

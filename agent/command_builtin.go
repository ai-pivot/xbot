package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xbot/bus"
	"xbot/channel"
	"xbot/channel/feishu"
	log "xbot/logger"
	"xbot/plugin"
	"xbot/version"
)

// --- /new ---

type newCmd struct{}

func (c *newCmd) Name() string        { return "/new" }
func (c *newCmd) Aliases() []string   { return nil }
func (c *newCmd) Match(s string) bool { return strings.ToLower(s) == "/new" }
func (c *newCmd) Concurrent() bool    { return false } // mutates session

func (c *newCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return nil, err
	}
	return a.handleNewSession(ctx, msg, tenantSession)
}

// --- /version ---

type versionCmd struct{}

func (c *versionCmd) Name() string        { return "/version" }
func (c *versionCmd) Aliases() []string   { return nil }
func (c *versionCmd) Match(s string) bool { return strings.ToLower(s) == "/version" }
func (c *versionCmd) Concurrent() bool    { return true } // stateless

func (c *versionCmd) Execute(_ context.Context, _ *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	info := version.Info()
	if version.Commit != "" {
		info += "\ncommit: " + version.Commit
	}
	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: info,
	}, nil
}

// --- /plugin reload-all (agent-level command for remote CLI) ---

type pluginReloadAllCmd struct{}

func (c *pluginReloadAllCmd) Name() string      { return "/plugin reload-all" }
func (c *pluginReloadAllCmd) Aliases() []string { return nil }
func (c *pluginReloadAllCmd) Concurrent() bool  { return true } // doesn't block message queue
func (c *pluginReloadAllCmd) Match(s string) bool {
	return strings.TrimSpace(s) == "/plugin reload-all"
}
func (c *pluginReloadAllCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	if a.pluginMgr == nil {
		return nil, fmt.Errorf("plugin system not available")
	}
	// Run reload in background — ReloadAll can take a while and must not
	// block the command handler (which blocks message processing).
	go func() {
		if err := a.pluginMgr.ReloadAll(context.Background()); err != nil {
			log.WithError(err).Error("Plugin reload-all failed")
		}
	}()
	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: "🔄 Plugin reload started — widgets will refresh when complete",
	}, nil
}

// --- /help ---

type helpCmd struct{}

func (c *helpCmd) Name() string        { return "/help" }
func (c *helpCmd) Aliases() []string   { return nil }
func (c *helpCmd) Match(s string) bool { return strings.ToLower(s) == "/help" }
func (c *helpCmd) Concurrent() bool    { return true } // stateless

func (c *helpCmd) Execute(_ context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	content := "xbot 命令:\n/help — 显示帮助"
	if a != nil && a.commands != nil {
		content = a.commands.HelpText()
	}
	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: content,
	}, nil
}

// --- /prompt ---

type promptCmd struct{}

func (c *promptCmd) Name() string      { return "/prompt" }
func (c *promptCmd) Aliases() []string { return nil }
func (c *promptCmd) Match(s string) bool {
	lower := strings.ToLower(s)
	return lower == "/prompt" || strings.HasPrefix(lower, "/prompt ")
}
func (c *promptCmd) Concurrent() bool { return true } // read-only snapshot, no real-time requirement

func (c *promptCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return nil, err
	}
	return a.handlePromptQuery(ctx, msg, tenantSession)
}

// --- /set-llm ---

type setLLMCmd struct{}

func (c *setLLMCmd) Name() string      { return "/set-llm" }
func (c *setLLMCmd) Aliases() []string { return nil }
func (c *setLLMCmd) Match(s string) bool {
	lower := strings.ToLower(s)
	return lower == "/set-llm" || strings.HasPrefix(lower, "/set-llm ")
}
func (c *setLLMCmd) Concurrent() bool { return false } // mutates LLM config

func (c *setLLMCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	return a.handleSetLLM(ctx, msg)
}

// --- /llm ---

type getLLMCmd struct{}

func (c *getLLMCmd) Name() string        { return "/llm" }
func (c *getLLMCmd) Aliases() []string   { return nil }
func (c *getLLMCmd) Match(s string) bool { return strings.ToLower(s) == "/llm" }
func (c *getLLMCmd) Concurrent() bool    { return true } // read-only

func (c *getLLMCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	return a.handleGetLLM(ctx, msg)
}

// --- /llms ---

type listLLMsCmd struct{}

func (c *listLLMsCmd) Name() string        { return "/llms" }
func (c *listLLMsCmd) Aliases() []string   { return nil }
func (c *listLLMsCmd) Match(s string) bool { return strings.ToLower(s) == "/llms" }
func (c *listLLMsCmd) Concurrent() bool    { return true } // read-only

func (c *listLLMsCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	return a.handleListLLMs(ctx, msg)
}

// --- /unset-llm ---

type unsetLLMCmd struct{}

func (c *unsetLLMCmd) Name() string      { return "/unset-llm" }
func (c *unsetLLMCmd) Aliases() []string { return nil }
func (c *unsetLLMCmd) Match(s string) bool {
	return strings.HasPrefix(strings.ToLower(s), "/unset-llm")
}
func (c *unsetLLMCmd) Concurrent() bool { return false } // mutates LLM config

func (c *unsetLLMCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	return a.handleUnsetLLM(ctx, msg)
}

// --- /compress ---

type compressCmd struct{}

func (c *compressCmd) Name() string        { return "/compress" }
func (c *compressCmd) Aliases() []string   { return nil }
func (c *compressCmd) Match(s string) bool { return strings.ToLower(s) == "/compress" }
func (c *compressCmd) Concurrent() bool    { return false } // mutates session

func (c *compressCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return nil, err
	}
	return a.handleCompress(ctx, msg, tenantSession)
}

// --- /usage ---

type usageCmd struct{}

func (c *usageCmd) Name() string        { return "/usage" }
func (c *usageCmd) Aliases() []string   { return nil }
func (c *usageCmd) Match(s string) bool { return strings.ToLower(s) == "/usage" }
func (c *usageCmd) Concurrent() bool    { return true } // read-only DB query

func (c *usageCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	return a.handleUsage(ctx, msg)
}

// --- /context info --- (read-only, concurrent)

type contextInfoCmd struct{}

func (c *contextInfoCmd) Name() string      { return "/context" }
func (c *contextInfoCmd) Aliases() []string { return nil }
func (c *contextInfoCmd) Match(s string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(s))
	return trimmed == "/context" || trimmed == "/context info"
}
func (c *contextInfoCmd) Concurrent() bool { return true } // read-only

func (c *contextInfoCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return nil, err
	}
	return a.handleContextInfo(ctx, msg, tenantSession)
}

// --- /context mode --- (stateful, NOT concurrent)

type contextModeCmd struct{}

func (c *contextModeCmd) Name() string      { return "/context mode" }
func (c *contextModeCmd) Aliases() []string { return nil }
func (c *contextModeCmd) Match(s string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(s))
	return strings.HasPrefix(trimmed, "/context mode")
}
func (c *contextModeCmd) Concurrent() bool { return false } // mutates runtime mode

func (c *contextModeCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	content := strings.TrimSpace(msg.Content)
	modeStr := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(content), "/context mode"))
	return a.handleContextMode(ctx, msg, modeStr)
}

// --- /models ---

type modelsCmd struct{}

func (c *modelsCmd) Name() string        { return "/models" }
func (c *modelsCmd) Aliases() []string   { return nil }
func (c *modelsCmd) Match(s string) bool { return strings.ToLower(s) == "/models" }
func (c *modelsCmd) Concurrent() bool    { return true } // read-only

func (c *modelsCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	return a.handleModels(ctx, msg)
}

// --- /set-model ---

type setModelCmd struct{}

func (c *setModelCmd) Name() string      { return "/set-model" }
func (c *setModelCmd) Aliases() []string { return nil }
func (c *setModelCmd) Match(s string) bool {
	lower := strings.ToLower(s)
	return lower == "/set-model" || strings.HasPrefix(lower, "/set-model ")
}
func (c *setModelCmd) Concurrent() bool { return false } // mutates LLM config

func (c *setModelCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	return a.handleSetModel(ctx, msg)
}

// --- ! (bang command) ---

type bangCmd struct{}

func (c *bangCmd) Name() string      { return "!" }
func (c *bangCmd) Aliases() []string { return nil }
func (c *bangCmd) Match(s string) bool {
	_, ok := isBangCommand(s)
	return ok
}
func (c *bangCmd) Concurrent() bool { return true } // runs in sandbox, no session mutation

func (c *bangCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	cmd, _ := isBangCommand(msg.Content)
	return a.handleBangCommand(ctx, msg, cmd)
}

// --- /settings ---

type settingsCmd struct{}

func (c *settingsCmd) Name() string      { return "/settings" }
func (c *settingsCmd) Aliases() []string { return nil }
func (c *settingsCmd) Match(s string) bool {
	lower := strings.ToLower(s)
	return lower == "/settings" || strings.HasPrefix(lower, "/settings ")
}
func (c *settingsCmd) Concurrent() bool { return true }

func (c *settingsCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	if msg.ChatType == "group" {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "⚠️ 设置仅限私聊使用，请私信我发送 /settings"}, nil
	}

	if a.userSys == nil || a.userSys.settingsSvc == nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "SettingsService 未初始化"}, nil
	}
	uc := UserContextFromContext(ctx)

	content := strings.TrimSpace(msg.Content)
	args := strings.TrimPrefix(strings.ToLower(content), "/settings ")
	args = strings.TrimSpace(args)

	// /settings set <key> <value>
	if strings.HasPrefix(args, "set ") {
		setParts := strings.Fields(strings.TrimPrefix(args, "set "))
		if len(setParts) < 2 {
			return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "用法：`/settings set <key> <value>`"}, nil
		}
		key := setParts[0]
		value := strings.Join(setParts[1:], " ")

		// Fix 4: Validate key against schema if channelFinder is available
		schema := uc.SettingsSvc.GetSettingsSchema(msg.Channel)
		if len(schema) > 0 {
			valid := false
			for _, def := range schema {
				if def.Key == key {
					valid = true
					break
				}
			}
			if !valid {
				var validKeys []string
				for _, def := range schema {
					validKeys = append(validKeys, def.Key)
				}
				return &channel.OutboundMsg{
					Channel: msg.Channel, ChatID: msg.ChatID,
					Content: fmt.Sprintf("未知设置项: %q\n可用设置项: %s", key, strings.Join(validKeys, ", ")),
				}, nil
			}
		}

		err := uc.SettingsSvc.SetSetting(msg.Channel, msg.SenderID, key, value)
		if err != nil {
			return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("设置失败：%v", err)}, nil
		}
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("✅ %s = %s", key, value)}, nil
	}

	// /settings (list) — 检测飞书渠道使用交互式卡片，其他渠道使用 markdown
	if a.channelFinder != nil {
		if ch, ok := a.channelFinder(msg.Channel); ok {
			if fc, ok := ch.(*feishu.FeishuChannel); ok {
				card, err := fc.BuildSettingsCard(ctx, msg.SenderID, msg.ChatID, "basic")
				if err != nil {
					return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("构建设置卡片失败：%v", err)}, nil
				}
				cardJSON, err := json.Marshal(card)
				if err != nil {
					return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("序列化设置卡片失败：%v", err)}, nil
				}
				return &channel.OutboundMsg{
					Channel: msg.Channel,
					ChatID:  msg.ChatID,
					Content: "__FEISHU_CARD__::" + string(cardJSON),
				}, nil
			}
		}
	}

	// Fallback: 非 Feishu 渠道使用 markdown UI
	ui, err := uc.SettingsSvc.GetSettingsUI(msg.Channel, msg.SenderID)
	if err != nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("获取设置失败：%v", err)}, nil
	}
	return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: ui}, nil
}

// --- /goal ---

type goalCmd struct{}

func (c *goalCmd) Name() string      { return "/goal" }
func (c *goalCmd) Aliases() []string { return nil }
func (c *goalCmd) Match(s string) bool {
	lower := strings.TrimSpace(strings.ToLower(s))
	if lower == "/goal" {
		return true
	}
	fields := strings.Fields(lower)
	if len(fields) >= 2 && fields[0] == "/goal" {
		sub := fields[1]
		if sub == "status" || sub == "clear" {
			return false // handled by goalStatusCmd / goalClearCmd
		}
		return true
	}
	return false
}
func (c *goalCmd) Concurrent() bool { return false } // mutates goal state + triggers Run

func (c *goalCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	objective := strings.TrimSpace(strings.TrimPrefix(msg.Content, "/goal"))
	objective = strings.TrimSpace(objective)
	if objective == "" || objective == "status" || objective == "clear" {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "用法：`/goal <目标描述>`"}, nil
	}

	sessionKey := qualifyChatID(msg.Channel, msg.ChatID)
	if a.goalManager == nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "⚠️ Goal 系统未初始化"}, nil
	}
	a.goalManager.Set(sessionKey, objective)

	// Return sentinel — processMessage detects goal_start metadata,
	// strips the /goal prefix, and falls through to Run().
	return &channel.OutboundMsg{
		Channel:  msg.Channel,
		ChatID:   msg.ChatID,
		Metadata: map[string]string{"goal_start": objective},
	}, nil
}

// --- /goal status ---

type goalStatusCmd struct{}

func (c *goalStatusCmd) Name() string      { return "/goal status" }
func (c *goalStatusCmd) Aliases() []string { return nil }
func (c *goalStatusCmd) Match(s string) bool {
	return strings.TrimSpace(strings.ToLower(s)) == "/goal status"
}
func (c *goalStatusCmd) Concurrent() bool { return true }

func (c *goalStatusCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	if a.goalManager == nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "⚠️ Goal 系统未初始化"}, nil
	}
	sessionKey := qualifyChatID(msg.Channel, msg.ChatID)
	g := a.goalManager.Get(sessionKey)
	if g == nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "📭 当前没有目标。使用 `/goal <目标描述>` 设定目标。"}, nil
	}

	var status string
	switch g.Status {
	case GoalCompleted:
		status = "✅ 已完成"
	default:
		status = "🔄 进行中"
	}

	content := fmt.Sprintf("🎯 **目标**: %s\n📊 **状态**: %s", g.Objective, status)
	if g.Summary != "" {
		content += fmt.Sprintf("\n📝 **总结**: %s", g.Summary)
	}
	return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: content}, nil
}

// --- /goal clear ---

type goalClearCmd struct{}

func (c *goalClearCmd) Name() string      { return "/goal clear" }
func (c *goalClearCmd) Aliases() []string { return nil }
func (c *goalClearCmd) Match(s string) bool {
	return strings.TrimSpace(strings.ToLower(s)) == "/goal clear"
}
func (c *goalClearCmd) Concurrent() bool { return false }

func (c *goalClearCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	if a.goalManager == nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "⚠️ Goal 系统未初始化"}, nil
	}
	sessionKey := qualifyChatID(msg.Channel, msg.ChatID)
	a.goalManager.Clear(sessionKey)
	return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "✅ 目标已清除。后续 turn 将正常结束，不再自动继续。"}, nil
}

// --- /menu ---

type menuCmd struct{}

func (c *menuCmd) Name() string        { return "/menu" }
func (c *menuCmd) Aliases() []string   { return nil }
func (c *menuCmd) Match(s string) bool { return strings.ToLower(s) == "/menu" }
func (c *menuCmd) Concurrent() bool    { return true }

func (c *menuCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: "## 🏠 主菜单\n\n" +
			"- ⚙️ `/settings` — 个人设置\n" +
			"- 📦 `/app list` — 查看已安装\n" +
			"- 📦 `/app install <file|url>` — 安装应用\n" +
			"- 📦 `/app export <name> --skill <s>` — 打包导出\n" +
			"- 🗑️ `/app uninstall <type> <name>` — 卸载\n",
	}, nil
}

// registerBuiltinCommands registers all built-in commands to the registry.
func registerBuiltinCommands(r *CommandRegistry) {
	r.Register(&newCmd{}, CommandInfo{Usage: "/new", Description: "开始新对话（归档记忆后重置）"})
	r.Register(&versionCmd{}, CommandInfo{Usage: "/version", Description: "显示版本信息"})
	r.Register(&helpCmd{}, CommandInfo{Usage: "/help", Description: "显示帮助"})
	r.Register(&promptCmd{}, CommandInfo{Usage: "/prompt <query>", Description: "预览完整提示词（不调用 LLM）"})
	r.Register(&setLLMCmd{}, CommandInfo{Usage: "/set-llm provider=<p> base_url=<url> api_key=<key> [model=<m>]", Description: "创建/更新个人 LLM 订阅"})
	r.Register(&unsetLLMCmd{}, CommandInfo{Usage: "/unset-llm <订阅名>", Description: "删除指定订阅"})
	r.Register(&getLLMCmd{}, CommandInfo{Usage: "/llm", Description: "查看当前解析到的订阅与模型"})
	r.Register(&listLLMsCmd{}, CommandInfo{Usage: "/llms", Description: "列出所有个人 LLM 订阅"})
	r.Register(&compressCmd{}, CommandInfo{Usage: "/compress", Description: "手动触发上下文压缩"})
	r.Register(&usageCmd{}, CommandInfo{Usage: "/usage", Description: "查看 token 用量统计"})
	r.Register(&contextModeCmd{}, CommandInfo{Usage: "/context mode [phase1|none|default]", Description: "查看/切换压缩模式"}) // 先注册（更精确的匹配优先）
	r.Register(&contextInfoCmd{}, CommandInfo{Usage: "/context", Description: "查看上下文统计"})                              // 后注册（更宽泛的匹配）
	r.Register(&modelsCmd{}, CommandInfo{Usage: "/models", Description: "列出可选模型（带正常/离线/禁用状态）"})
	r.Register(&setModelCmd{}, CommandInfo{Usage: "/set-model <订阅名> <模型名>", Description: "切换当前会话模型"})
	r.Register(&bangCmd{}, CommandInfo{Usage: "!<command>", Description: "快捷执行命令（跳过 LLM，直接在 sandbox 中运行）"})

	// Registry & settings commands
	r.Register(&settingsCmd{}, CommandInfo{Usage: "/settings", Description: "打开个人设置（仅私聊）"})
	r.Register(&menuCmd{}, CommandInfo{Usage: "/menu", Description: "主菜单"})
	r.Register(&pluginReloadAllCmd{}, CommandInfo{Usage: "/plugin reload-all", Description: "重新加载所有插件"})
	r.Register(&appCmd{}, CommandInfo{Usage: "/app", Description: "应用管理（打包、安装、卸载）"})

	// Goal commands
	r.Register(&goalClearCmd{}, CommandInfo{Usage: "/goal clear", Description: "清除当前目标"}) // 先注册（更精确的匹配优先）
	r.Register(&goalStatusCmd{}, CommandInfo{Usage: "/goal status", Description: "查看当前目标状态"})
	r.Register(&goalCmd{}, CommandInfo{Usage: "/goal <目标描述>", Description: "设定长期目标，Agent 自动持续工作直到完成"}) // 后注册（匹配 /goal <任意内容>）
}

// ---------------------------------------------------------------------------
// Plugin Command Adapter — bridges plugin.PluginCommandHandler → agent.Command
// ---------------------------------------------------------------------------

// pluginCmdAdapter wraps a plugin command handler as an agent.Command.
// It avoids circular imports by living in the agent package, receiving the
// handler and PluginContext from plugin.WirePluginCommands.
type pluginCmdAdapter struct {
	name        string
	description string
	handler     plugin.PluginCommandHandler
	pctx        plugin.PluginContext
}

func (a *pluginCmdAdapter) Name() string      { return a.name }
func (a *pluginCmdAdapter) Aliases() []string { return nil }
func (a *pluginCmdAdapter) Concurrent() bool  { return false }
func (a *pluginCmdAdapter) CommandInfo() CommandInfo {
	return CommandInfo{Name: a.name, Usage: a.name, Description: a.description}
}

func isPluginCommand(cmd Command) bool {
	switch c := cmd.(type) {
	case *pluginCmdAdapter:
		return true
	case *commandWithInfo:
		_, ok := c.Command.(*pluginCmdAdapter)
		return ok
	default:
		return false
	}
}

func (a *pluginCmdAdapter) Match(content string) bool {
	trimmed := strings.TrimSpace(content)
	return strings.HasPrefix(trimmed, a.name+" ") || trimmed == a.name
}

func (a *pluginCmdAdapter) Execute(ctx context.Context, ag *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	trimmed := strings.TrimSpace(msg.Content)
	args := ""
	if strings.HasPrefix(trimmed, a.name+" ") {
		args = strings.TrimSpace(strings.TrimPrefix(trimmed, a.name+" "))
	}
	result, err := a.handler(ctx, args, a.pctx)
	if err != nil {
		return nil, err
	}
	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: result,
	}, nil
}

// --- /app (app management) ---

type appCmd struct{}

func (c *appCmd) Name() string      { return "/app" }
func (c *appCmd) Aliases() []string { return nil }
func (c *appCmd) Match(s string) bool {
	lower := strings.ToLower(s)
	return lower == "/app" || strings.HasPrefix(lower, "/app ")
}
func (c *appCmd) Concurrent() bool { return false }

func (c *appCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	content := strings.TrimSpace(msg.Content)
	args := strings.TrimPrefix(strings.ToLower(content), "/app")
	args = strings.TrimSpace(args)
	if args == "" {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: appHelp()}, nil
	}

	parts := strings.Fields(args)
	subCmd := parts[0]
	rest := parts[1:]

	switch subCmd {
	case "export":
		return c.handleExport(a, msg, rest)
	case "install":
		return c.handleInstall(a, msg, rest)
	case "uninstall":
		return c.handleUninstall(a, msg, rest)
	case "list":
		return c.handleList(a, msg, rest)
	default:
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: appHelp()}, nil
	}
}

func (c *appCmd) handleExport(a *Agent, msg bus.InboundMessage, args []string) (*channel.OutboundMsg, error) {
	// Parse: /app export <app-name> --skill <s> --agent <a> --plugin <p> [...]
	if len(args) < 2 {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "用法：`/app export <app-name> --skill <name> --agent <name> --plugin <name>`"}, nil
	}

	appName := args[0]
	var items []AppItem
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--skill":
			if i+1 < len(args) {
				items = append(items, AppItem{Type: "skill", Name: args[i+1]})
				i++
			}
		case "--agent":
			if i+1 < len(args) {
				items = append(items, AppItem{Type: "agent", Name: args[i+1]})
				i++
			}
		case "--plugin":
			if i+1 < len(args) {
				items = append(items, AppItem{Type: "plugin", Name: args[i+1]})
				i++
			}
		}
	}

	if len(items) == 0 {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "至少指定一个 --skill、--agent 或 --plugin"}, nil
	}
	if a.registryManager == nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "RegistryManager 未初始化"}, nil
	}

	outputPath := filepath.Join(os.TempDir(), appName+".xbot.zip")
	if err := a.registryManager.PackApp(items, outputPath, msg.SenderID); err != nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("导出失败：%v", err)}, nil
	}

	var itemNames []string
	for _, it := range items {
		itemNames = append(itemNames, fmt.Sprintf("%s:%s", it.Type, it.Name))
	}
	return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("✅ 应用已导出到 %s\n包含：%s", outputPath, strings.Join(itemNames, ", "))}, nil
}

func (c *appCmd) handleInstall(a *Agent, msg bus.InboundMessage, args []string) (*channel.OutboundMsg, error) {
	if len(args) < 1 {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "用法：`/app install <file-path>` 或 `/app install <url>`"}, nil
	}
	if a.registryManager == nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "RegistryManager 未初始化"}, nil
	}

	target := args[0]
	var result *AppInstallResult
	var err error

	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		// /app install <url> — download and install
		result, err = a.registryManager.InstallAppFromURL(target, msg.SenderID)
	} else {
		// /app install <file-path> — install from local file
		result, err = a.registryManager.InstallAppFromFile(target, msg.SenderID)
	}
	if err != nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("安装失败：%v", err)}, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "✅ 应用 %q 安装完成\n", result.Manifest.Name)
	if result.Manifest.Version != "" {
		fmt.Fprintf(&sb, "版本：%s\n", result.Manifest.Version)
	}
	sb.WriteString("已安装：\n")
	for _, item := range result.Installed {
		fmt.Fprintf(&sb, "  - %s\n", item)
	}
	return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: sb.String()}, nil
}

func (c *appCmd) handleUninstall(a *Agent, msg bus.InboundMessage, args []string) (*channel.OutboundMsg, error) {
	if len(args) < 2 {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "用法：`/app uninstall <type> <name>`"}, nil
	}
	if a.registryManager == nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "RegistryManager 未初始化"}, nil
	}
	entryType := args[0]
	name := args[1]
	if err := a.registryManager.Uninstall(entryType, name, msg.SenderID); err != nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("卸载失败：%v", err)}, nil
	}
	return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("✅ %s %q 已卸载", entryType, name)}, nil
}

func (c *appCmd) handleList(a *Agent, msg bus.InboundMessage, args []string) (*channel.OutboundMsg, error) {
	if a.registryManager == nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "RegistryManager 未初始化"}, nil
	}
	// List installed skills
	skills := a.registryManager.ListInstalledSkills(msg.SenderID)
	// List installed agents
	agents := a.registryManager.ListInstalledAgents(msg.SenderID)
	// List installed plugins
	plugins := a.registryManager.ListInstalledPlugins(msg.SenderID)

	if len(skills) == 0 && len(agents) == 0 && len(plugins) == 0 {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "📦 暂无已安装的 skill、agent 或 plugin"}, nil
	}

	var sb strings.Builder
	sb.WriteString("## 📦 已安装\n\n")
	if len(skills) > 0 {
		sb.WriteString("**Skills:**\n")
		for _, s := range skills {
			fmt.Fprintf(&sb, "- 📦 %s\n", s)
		}
		sb.WriteString("\n")
	}
	if len(agents) > 0 {
		sb.WriteString("**Agents:**\n")
		for _, a := range agents {
			fmt.Fprintf(&sb, "- 🤖 %s\n", a)
		}
		sb.WriteString("\n")
	}
	if len(plugins) > 0 {
		sb.WriteString("**Plugins:**\n")
		for _, p := range plugins {
			fmt.Fprintf(&sb, "- 🧩 %s\n", p)
		}
	}
	return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: sb.String()}, nil
}

func appHelp() string {
	return "## 📦 /app — 应用管理\n\n" +
		"**子命令：**\n\n" +
		"- `/app list` — 查看已安装\n" +
		"- `/app install <file-path>` — 从 .xbot.zip 文件安装\n" +
		"- `/app install <url>` — 从 URL 下载并安装\n" +
		"- `/app uninstall <type> <name>` — 卸载（skill/agent/plugin/app）\n" +
		"- `/app export <name> --skill <s> --agent <a> --plugin <p>` — 打包导出\n\n" +
		"**示例：**\n\n" +
		"```\n" +
		"/app list\n" +
		"/app install /tmp/my-app.xbot.zip\n" +
		"/app install https://example.com/my-app.xbot.zip\n" +
		"/app uninstall skill debug\n" +
		"/app uninstall plugin my-plugin\n" +
		"/app uninstall app my-app\n" +
		"/app export my-app --skill debug --agent explore --plugin git-widget\n" +
		"```\n"
}

package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
func (c *promptCmd) Concurrent() bool { return false } // needs UserContext (only available in processMessage path)

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
func (c *getLLMCmd) Concurrent() bool {
	return false // needs UserContext (only available in processMessage path)
	// Queuing semantics: Concurrent()=false routes this command through the
	// per-chat serial msgCh (admitToMsgCh) — during a busy turn it QUEUES and
	// executes only after the current turn completes, it does NOT run in parallel.
}

func (c *getLLMCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	return a.handleGetLLM(ctx, msg)
}

// --- /llms ---

type listLLMsCmd struct{}

func (c *listLLMsCmd) Name() string        { return "/llms" }
func (c *listLLMsCmd) Aliases() []string   { return nil }
func (c *listLLMsCmd) Match(s string) bool { return strings.ToLower(s) == "/llms" }
func (c *listLLMsCmd) Concurrent() bool {
	return false // needs UserContext (only available in processMessage path)
	// Queuing semantics: Concurrent()=false routes this command through the
	// per-chat serial msgCh (admitToMsgCh) — during a busy turn it QUEUES and
	// executes only after the current turn completes, it does NOT run in parallel.
}

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

// --- /continue ---

type continueCmd struct{}

func (c *continueCmd) Name() string        { return "/continue" }
func (c *continueCmd) Aliases() []string   { return nil }
func (c *continueCmd) Match(s string) bool { return strings.ToLower(s) == "/continue" }
func (c *continueCmd) Concurrent() bool    { return false } // serializes with the enqueued resume turn

func (c *continueCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	if a.multiSession == nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "⚠️ 数据库未连接"}, nil
	}

	// Since v71 (one session, one DB) messages live in the per-session DB —
	// the main DB's session_messages is empty, so resume checks must go
	// through the TenantSession.
	tenant, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("⚠️ 会话未连接: %v", err)}, nil
	}

	// Check if the last user message already has an assistant reply.
	// If it does, the turn completed — nothing to continue.
	hasReply, err := tenant.HasAssistantReplyAfterLastUser()
	if err != nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("⚠️ 检查失败: %v", err)}, nil
	}
	if hasReply {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "💡 上一轮对话已完成，无需继续。"}, nil
	}

	// Verify there IS a last user message to resume from.
	content, err := tenant.GetLastUserMessageContent()
	if err != nil {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("⚠️ 获取历史失败: %v", err)}, nil
	}
	if content == "" {
		return &channel.OutboundMsg{Channel: msg.Channel, ChatID: msg.ChatID, Content: "💡 没有找到可继续的对话。"}, nil
	}

	// Inject resume turn with empty content — the user message is already
	// in DB. resume_turn metadata tells processMessage to skip eager-save,
	// and Assemble skips appending a user message (UserMessage is empty).
	// LLM sees exactly the DB state — no duplicate, no workaround.
	log.Ctx(ctx).WithFields(log.Fields{
		"channel": msg.Channel,
		"chat_id": msg.ChatID,
	}).Info("User triggered /continue — resuming interrupted turn")
	a.InjectInboundResume(msg.Channel, msg.ChatID, msg.SenderID)
	return nil, nil // no immediate reply — the resumed turn will produce one
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
func (c *contextInfoCmd) Concurrent() bool {
	return false // needs UserContext (only available in processMessage path)
	// Queuing semantics: Concurrent()=false routes this command through the
	// per-chat serial msgCh (admitToMsgCh) — during a busy turn it QUEUES and
	// executes only after the current turn completes, it does NOT run in parallel.
}

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
func (c *modelsCmd) Concurrent() bool {
	return false // needs UserContext (only available in processMessage path)
	// Queuing semantics: Concurrent()=false routes this command through the
	// per-chat serial msgCh (admitToMsgCh) — during a busy turn it QUEUES and
	// executes only after the current turn completes, it does NOT run in parallel.
}

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
func (c *settingsCmd) Concurrent() bool {
	return false // needs UserContext (only available in processMessage path)
	// Queuing semantics: Concurrent()=false routes this command through the
	// per-chat serial msgCh (admitToMsgCh) — during a busy turn it QUEUES and
	// executes only after the current turn completes, it does NOT run in parallel.
}

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

// registerBuiltinCommands registers all built-in commands to the registry.
func registerBuiltinCommands(r *CommandRegistry) {
	r.Register(&newCmd{}, CommandInfo{Usage: "/new", Description: "开始新对话（归档记忆后重置）"})
	r.Register(&versionCmd{}, CommandInfo{Usage: "/version", Description: "显示版本信息"})
	r.Register(&helpCmd{}, CommandInfo{Usage: "/help", Description: "显示帮助"})
	r.Register(&promptCmd{}, CommandInfo{Usage: "/prompt [query]", Description: "预览完整提示词（不调用 LLM）"})
	// Admin-only management commands (multi-user removal: subscriptions,
	// settings and models are GLOBAL — non-admin channel users such as Feishu
	// group members must not read or mutate the operator's configuration).
	r.Register(&setLLMCmd{}, CommandInfo{Usage: "/set-llm <name> provider=<p> base_url=<url> api_key=<key>", Description: "创建/更新个人 LLM 订阅", AdminOnly: true})
	r.Register(&unsetLLMCmd{}, CommandInfo{Usage: "/unset-llm <name>", Description: "删除指定订阅", AdminOnly: true})
	r.Register(&getLLMCmd{}, CommandInfo{Usage: "/llm", Description: "查看当前解析到的订阅与模型", AdminOnly: true})
	r.Register(&listLLMsCmd{}, CommandInfo{Usage: "/llms", Description: "列出所有个人 LLM 订阅", AdminOnly: true})
	r.Register(&compressCmd{}, CommandInfo{Usage: "/compress", Description: "手动触发上下文压缩"})
	r.Register(&continueCmd{}, CommandInfo{Usage: "/continue", Description: "继续上一轮被中断的对话（基于 DB 断点恢复）"})
	r.Register(&usageCmd{}, CommandInfo{Usage: "/usage", Description: "查看 token 用量统计", AdminOnly: true})
	r.Register(&contextModeCmd{}, CommandInfo{Usage: "/context mode [phase1|none|default]", Description: "查看/切换压缩模式"}) // 先注册（更精确的匹配优先）
	r.Register(&contextInfoCmd{}, CommandInfo{Usage: "/context", Description: "查看上下文统计"})                              // 后注册（更宽泛的匹配）
	r.Register(&modelsCmd{}, CommandInfo{Usage: "/models", Description: "列出可选模型（带正常/离线/禁用状态）", AdminOnly: true})
	r.Register(&setModelCmd{}, CommandInfo{Usage: "/set-model <subscription> <model>", Description: "切换当前会话模型", AdminOnly: true})
	r.Register(&bangCmd{}, CommandInfo{Usage: "!<command>", Description: "快捷执行命令（跳过 LLM，直接在 sandbox 中运行）"})

	// Registry & settings commands
	r.Register(&settingsCmd{}, CommandInfo{Usage: "/settings", Description: "打开个人设置（仅私聊）", AdminOnly: true})
	r.Register(&pluginReloadAllCmd{}, CommandInfo{Usage: "/plugin reload-all", Description: "重新加载所有插件", AdminOnly: true})

	// Goal commands
	r.Register(&goalClearCmd{}, CommandInfo{Usage: "/goal clear", Description: "清除当前目标"}) // 先注册（更精确的匹配优先）
	r.Register(&goalStatusCmd{}, CommandInfo{Usage: "/goal status", Description: "查看当前目标状态"})
	r.Register(&goalCmd{}, CommandInfo{Usage: "/goal <description>", Description: "设定长期目标，Agent 自动持续工作直到完成"}) // 后注册（匹配 /goal <任意内容>）

	// Session info & export
	r.Register(&infoCmd{}, CommandInfo{Usage: "/info", Description: "查看当前会话信息"})
	r.Register(&exportCmd{}, CommandInfo{Usage: "/export [native|openai|codex]", Description: "导出当前会话"})
}

// --- /info ---

type infoCmd struct{}

func (c *infoCmd) Name() string        { return "/info" }
func (c *infoCmd) Aliases() []string   { return nil }
func (c *infoCmd) Match(s string) bool { return strings.ToLower(strings.TrimSpace(s)) == "/info" }
func (c *infoCmd) Concurrent() bool    { return false } // needs UserContext (only available in processMessage path)

func (c *infoCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	return a.handleSessionInfo(ctx, msg)
}

// --- /export ---

type exportCmd struct{}

func (c *exportCmd) Name() string      { return "/export" }
func (c *exportCmd) Aliases() []string { return nil }
func (c *exportCmd) Match(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	return lower == "/export" || strings.HasPrefix(lower, "/export ")
}
func (c *exportCmd) Concurrent() bool { return false } // needs UserContext (only available in processMessage path)

func (c *exportCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	return a.handleExportSession(ctx, msg)
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

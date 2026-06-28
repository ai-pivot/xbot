package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"xbot/bus"
	"xbot/channel"
	"xbot/storage/sqlite"
)

const setLLMUsage = `设置/更新个人 LLM 订阅（作为你的默认订阅）

用法: /set-llm provider=<provider> base_url=<url> api_key=<key> [model=<model>] [max_context=<tokens>] [max_output_tokens=<tokens>] [thinking_mode=<mode>]

说明:
  - 首次调用会创建一个个人订阅并设为默认；再次调用会更新该默认订阅。
  - 订阅是模型的来源；用 /set-model <model> 切换模型，用 /models 查看可选模型。

必填参数:
  provider      - LLM 提供商: anthropic 或 openai/deepseek/zhipu 等 OpenAI 兼容服务
  base_url      - API 基础地址
  api_key       - API 密钥

可选参数:
  model         - 默认模型名称（不填则由订阅自动选取）
  max_context   - 最大上下文 token 数（可选，0 表示不限制）
  max_output_tokens - 最大输出 token 数（可选）
  thinking_mode - 思考模式（可选，各厂商格式不同）:
                  DeepSeek/OpenAI reasoning:
                    - enabled: 强制开启
                    - disabled: 强制关闭
                    - {"thinking":{"type":"enabled"},"reasoning_effort":"high"}: 指定思考强度 (high/max)
                  智谱 GLM:
                    - {"type":"enabled","clear_thinking":false}: 保留式思考（多轮推理连贯）
                  Anthropic Claude:
                    - enabled: 手动模式（需配合 budget_tokens）
                    - adaptive: 自适应模式（Opus 4.6/Sonnet 4.6）
                    - {"type":"enabled","budget_tokens":10000}
                    - {"type":"adaptive","effort":"high"}  (low/medium/high)

示例:
  # OpenAI 格式（适用于 OpenAI、DeepSeek、SiliconFlow 等）
  /set-llm provider=openai base_url=https://api.openai.com/v1 api_key=sk-xxx model=gpt-4
  /set-llm provider=deepseek base_url=https://api.deepseek.com/v1 api_key=sk-xxx model=deepseek-chat

  # DeepSeek R1 (Thinking Mode)
  /set-llm provider=deepseek base_url=https://api.deepseek.com/v1 api_key=sk-xxx model=deepseek-reasoner thinking_mode=enabled

  # 智谱 GLM-5/GLM-4.7 (深度思考)
  /set-llm provider=openai base_url=https://open.bigmodel.cn/api/paas/v4 api_key=xxx model=glm-5 thinking_mode=enabled

  # Anthropic Claude
  /set-llm provider=anthropic base_url=https://api.anthropic.com api_key=sk-ant-xxx model=claude-3-5-sonnet-20241022

  # Anthropic Claude Adaptive Thinking (Opus 4.6/Sonnet 4.6)
  /set-llm provider=anthropic base_url=https://api.anthropic.com api_key=sk-ant-xxx model=claude-sonnet-4-20250514 thinking_mode=adaptive

  # 限制上下文大小
  /set-llm provider=openai base_url=https://api.openai.com/v1 api_key=sk-xxx model=gpt-4 max_context=8000

注意: API Key 会被加密存储，查询时只显示前4位。请在私聊中使用，避免在群聊暴露密钥。`

// handleSetLLM handles /set-llm to create/update the user's default personal
// LLM subscription.
// parseSetLLMArgs splits args by spaces but respects JSON brace nesting and quoted strings.
// e.g. `provider=openai thinking_mode={"type": "enabled", "budget_tokens": 10000}` correctly
// produces ["provider=openai", `thinking_mode={"type": "enabled", "budget_tokens": 10000}`].
func parseSetLLMArgs(args string) []string {
	var parts []string
	var current strings.Builder
	depth := 0
	inQuote := false
	for i := 0; i < len(args); i++ {
		ch := args[i]
		if ch == '"' && (i == 0 || args[i-1] != '\\') {
			inQuote = !inQuote
		}
		if !inQuote {
			if ch == '{' {
				depth++
			}
			if ch == '}' && depth > 0 {
				depth--
			}
			if ch == ' ' && depth == 0 {
				if current.Len() > 0 {
					parts = append(parts, current.String())
					current.Reset()
				}
				continue
			}
		}
		current.WriteByte(ch)
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

func (a *Agent) handleSetLLM(ctx context.Context, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	// Security: warn in group chat to avoid exposing API key
	if msg.ChatType == "group" {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "⚠️ 安全提醒：此命令涉及 API Key 等敏感信息，请通过私聊发送 /set-llm，避免在群聊中暴露密钥。",
		}, nil
	}

	// Parse command arguments
	trimmed := strings.TrimSpace(msg.Content)
	args := strings.TrimSpace(trimmed[len("/set-llm"):])

	if args == "" {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: setLLMUsage,
		}, nil
	}

	// Parse key=value pairs
	cfg := &sqlite.UserLLMConfig{
		SenderID: msg.SenderID,
	}

	parts := parseSetLLMArgs(args)
	parseErrors := false
	for _, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			parseErrors = true
			continue
		}
		key := strings.ToLower(kv[0])
		value := kv[1]

		switch key {
		case "provider":
			cfg.Provider = value
		case "base_url":
			cfg.BaseURL = value
		case "api_key":
			cfg.APIKey = value
		case "model":
			cfg.Model = value
		case "max_context":
			var maxCtx int
			if _, err := fmt.Sscanf(value, "%d", &maxCtx); err == nil {
				cfg.MaxContext = maxCtx
			} else {
				parseErrors = true
			}
		case "max_output_tokens":
			var maxOut int
			if _, err := fmt.Sscanf(value, "%d", &maxOut); err == nil {
				cfg.MaxOutputTokens = maxOut
			} else {
				parseErrors = true
			}
		case "thinking_mode":
			// 支持: enabled, disabled, adaptive, 自定义 JSON 字符串
			if value == "enabled" || value == "disabled" || value == "adaptive" {
				cfg.ThinkingMode = value
			} else if len(value) > 0 && value[0] == '{' {
				// 校验 JSON 合法性
				var js json.RawMessage
				if json.Unmarshal([]byte(value), &js) == nil {
					cfg.ThinkingMode = value
				} else {
					parseErrors = true
				}
			} else {
				cfg.ThinkingMode = "" // 空/无效值表示不发送参数
			}
		}
	}

	// Validate required fields
	if cfg.Provider == "" || cfg.BaseURL == "" || cfg.APIKey == "" {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("错误: 必须提供 provider, base_url 和 api_key 参数。\n\n%s", setLLMUsage),
		}, nil
	}

	// Warn about parse errors
	var warning string
	if parseErrors {
		warning = "\n⚠️ 注意: 部分参数格式不正确，已被忽略。"
	}

	svc := a.llmFactory.GetSubscriptionSvc()
	if svc == nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "订阅服务未初始化，无法保存配置。",
		}, nil
	}

	// Derive a display name for an auto-created subscription.
	name := cfg.Provider
	if name == "" {
		name = "My LLM"
	}

	// Update the user's default subscription if one exists; otherwise create one.
	existing, gerr := svc.GetDefault(msg.SenderID)
	if gerr == nil && existing != nil {
		existing.Provider = cfg.Provider
		existing.BaseURL = cfg.BaseURL
		existing.APIKey = cfg.APIKey
		if cfg.Model != "" {
			existing.Model = cfg.Model
		}
		existing.MaxContext = cfg.MaxContext
		existing.MaxOutputTokens = cfg.MaxOutputTokens
		existing.ThinkingMode = cfg.ThinkingMode
		if err := svc.Update(existing); err != nil {
			return &channel.OutboundMsg{
				Channel: msg.Channel, ChatID: msg.ChatID,
				Content: fmt.Sprintf("更新订阅失败: %v", err),
			}, nil
		}
	} else {
		sub := &sqlite.LLMSubscription{
			SenderID:        msg.SenderID,
			Name:            name,
			Provider:        cfg.Provider,
			BaseURL:         cfg.BaseURL,
			APIKey:          cfg.APIKey,
			Model:           cfg.Model,
			MaxContext:      cfg.MaxContext,
			MaxOutputTokens: cfg.MaxOutputTokens,
			ThinkingMode:    cfg.ThinkingMode,
			IsDefault:       true,
			Enabled:         true,
		}
		if err := svc.Add(sub); err != nil {
			return &channel.OutboundMsg{
				Channel: msg.Channel, ChatID: msg.ChatID,
				Content: fmt.Sprintf("创建订阅失败: %v", err),
			}, nil
		}
	}

	// Invalidate cached LLM client and HasCustomLLM cache
	a.llmFactory.Invalidate(msg.SenderID)
	a.llmFactory.invalidateUserMemos(msg.SenderID)
	a.llmFactory.InvalidateCustomLLMCache(msg.SenderID)

	// Mask API key for display
	maskedKey := maskAPIKey(cfg.APIKey)

	var maxContextStr string
	if cfg.MaxContext > 0 {
		maxContextStr = fmt.Sprintf("\n- Max Context: %d", cfg.MaxContext)
	}

	var thinkingModeStr string
	if cfg.ThinkingMode != "" {
		thinkingModeStr = fmt.Sprintf("\n- Thinking Mode: %s", cfg.ThinkingMode)
	} else {
		thinkingModeStr = "\n- Thinking Mode: auto"
	}

	modelDisplay := cfg.Model
	if modelDisplay == "" {
		modelDisplay = "(由订阅自动选取)"
	}

	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("个人订阅已保存（设为默认）:\n- Provider: %s\n- Base URL: %s\n- API Key: %s\n- Model: %s%s%s%s\n\n用 /models 查看可选模型，/set-model <model> 切换。",
			cfg.Provider, cfg.BaseURL, maskedKey, modelDisplay, maxContextStr, thinkingModeStr, warning),
	}, nil
}

// handleGetLLM handles /llm command to show the currently resolved LLM
// (subscription + model), via the same resolution path the agent loop uses.
func (a *Agent) handleGetLLM(ctx context.Context, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	_, model, maxCtx, thinkingMode, maxOut := a.llmFactory.ResolveLLM(msg.SenderID, msg.ChatID, msg.Channel)

	if model == "" {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "当前未解析到 LLM。使用 /set-llm 设置个人订阅。",
		}, nil
	}

	// Find the subscription that owns this model.
	sub, _ := a.llmFactory.ResolveSubscriptionForModel(msg.SenderID, model)

	var sb strings.Builder
	if sub != nil {
		maskedKey := maskAPIKey(sub.APIKey)
		fmt.Fprintf(&sb, "当前 LLM:\n- 订阅: %s\n- Provider: %s\n- Base URL: %s\n- API Key: %s\n- 模型: %s",
			sub.Name, sub.Provider, sub.BaseURL, maskedKey, model)
	} else {
		fmt.Fprintf(&sb, "当前使用系统默认 LLM:\n- 模型: %s", model)
		fmt.Fprintf(&sb, "\n\n（未匹配到个人订阅；可用 /set-llm 设置个人订阅。）")
	}
	if maxCtx > 0 {
		fmt.Fprintf(&sb, "\n- Max Context: %d", maxCtx)
	}
	if maxOut > 0 {
		fmt.Fprintf(&sb, "\n- Max Output Tokens: %d", maxOut)
	}
	if thinkingMode != "" {
		fmt.Fprintf(&sb, "\n- Thinking Mode: %s", thinkingMode)
	} else {
		fmt.Fprintf(&sb, "\n- Thinking Mode: auto")
	}
	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: sb.String(),
	}, nil
}

// userDefaultSubModel resolves the user's default subscription and the model to
// apply per-model settings to. Returns nil if the user has no default subscription.
func (a *Agent) userDefaultSubModel(senderID string) (*sqlite.LLMSubscription, string, error) {
	svc := a.llmFactory.GetSubscriptionSvc()
	if svc == nil {
		return nil, "", fmt.Errorf("订阅服务未初始化")
	}
	sub, err := svc.GetDefault(senderID)
	if err != nil {
		return nil, "", fmt.Errorf("get default subscription: %w", err)
	}
	if sub == nil {
		return nil, "", fmt.Errorf("当前未配置个人 LLM 订阅，请先通过 /set-llm 设置")
	}
	model := sub.Model
	if model == "" {
		model = a.llmFactory.PickDefaultModelForSub(sub)
	}
	return sub, model, nil
}

// GetUserMaxContext returns the user's max_context setting (0 = use default).
// Reads the per-model override for the default subscription's current model,
// falling back to the subscription-level value.
func (a *Agent) GetUserMaxContext(senderID string) int {
	sub, model, err := a.userDefaultSubModel(senderID)
	if err != nil || sub == nil {
		return 0
	}
	if v := sub.GetPerModelMaxContext(model); v > 0 {
		return v
	}
	return sub.MaxContext
}

// SetUserMaxContext updates the per-model max_context for the user's default
// subscription's current model and invalidates the cached LLM client.
func (a *Agent) SetUserMaxContext(senderID string, maxContext int) error {
	if maxContext < 1000 || maxContext > 2000000 {
		return fmt.Errorf("max_context must be between 1000 and 2000000, got %d", maxContext)
	}
	sub, model, err := a.userDefaultSubModel(senderID)
	if err != nil {
		return err
	}
	svc := a.llmFactory.GetSubscriptionSvc()
	existing, _ := svc.GetModel(sub.ID, model)
	var maxOut int
	var thinking, apiType string
	if existing != nil {
		maxOut = existing.MaxOutputTokens
		thinking = existing.ThinkingMode
		apiType = existing.APIType
	}
	if err := svc.UpsertModel(sub.ID, model, maxContext, maxOut, thinking, apiType); err != nil {
		return fmt.Errorf("save per-model max_context: %w", err)
	}
	a.llmFactory.Invalidate(senderID)
	return nil
}

// GetUserMaxOutputTokens returns the user's max_output_tokens setting (0 = use default).
func (a *Agent) GetUserMaxOutputTokens(senderID string) int {
	sub, model, err := a.userDefaultSubModel(senderID)
	if err != nil || sub == nil {
		return 0
	}
	if v := sub.GetPerModelMaxTokens(model); v > 0 {
		return v
	}
	return sub.MaxOutputTokens
}

// SetUserMaxOutputTokens updates the per-model max_output_tokens for the user's
// default subscription's current model and invalidates the cached LLM client.
func (a *Agent) SetUserMaxOutputTokens(senderID string, maxTokens int) error {
	if maxTokens < 0 || maxTokens > 2000000 {
		return fmt.Errorf("max_output_tokens must be between 0 and 2000000, got %d", maxTokens)
	}
	sub, model, err := a.userDefaultSubModel(senderID)
	if err != nil {
		return err
	}
	svc := a.llmFactory.GetSubscriptionSvc()
	existing, _ := svc.GetModel(sub.ID, model)
	var maxCtx int
	var thinking, apiType string
	if existing != nil {
		maxCtx = existing.MaxContext
		thinking = existing.ThinkingMode
		apiType = existing.APIType
	}
	if err := svc.UpsertModel(sub.ID, model, maxCtx, maxTokens, thinking, apiType); err != nil {
		return fmt.Errorf("save per-model max_output_tokens: %w", err)
	}
	a.llmFactory.Invalidate(senderID)
	return nil
}

// GetUserThinkingMode returns the user's global thinking_mode setting
// ("" = auto). Thinking is a global per-user setting stored under the canonical
// channel (see LLMFactory.thinkingModeChannel), no longer subscription-scoped.
func (a *Agent) GetUserThinkingMode(senderID string) string {
	if a.llmFactory == nil || a.settingsSvc == nil {
		return ""
	}
	vals, err := a.settingsSvc.GetSettings(thinkingModeChannel, senderID)
	if err != nil || vals == nil {
		return ""
	}
	return vals["thinking_mode"]
}

// SetUserThinkingMode updates the global thinking_mode user setting (canonical
// channel) and invalidates the cached LLM client. It no longer touches
// subscription rows — thinking is global, not per-subscription.
func (a *Agent) SetUserThinkingMode(senderID string, mode string) error {
	if mode == "auto" {
		mode = ""
	}
	if a.settingsSvc == nil {
		return ErrSettingsUnavailable
	}
	if err := a.settingsSvc.SetSetting(thinkingModeChannel, senderID, "thinking_mode", mode); err != nil {
		return fmt.Errorf("save thinking_mode: %w", err)
	}
	if a.llmFactory != nil {
		// Drop cached resolved thinking for every session so the next call
		// re-reads the new global value from user_settings.
		a.llmFactory.invalidateUserMemos(senderID)
		a.llmFactory.InvalidateSender(senderID)
	}
	return nil
}

// maskAPIKey masks API key, showing only first 4 characters
func maskAPIKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return key[:4] + "****"
}

// handleUnsetLLM handles /unset-llm to remove the user's default personal
// subscription and clear the user-level default model.
// personal subscription and clear the user-level default model.
func (a *Agent) handleUnsetLLM(ctx context.Context, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	svc := a.llmFactory.GetSubscriptionSvc()
	if svc == nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "订阅服务未初始化。",
		}, nil
	}

	sub, err := svc.GetDefault(msg.SenderID)
	if err != nil || sub == nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "当前没有个人默认订阅，无需清除。",
		}, nil
	}

	name := sub.Name
	if err := svc.Remove(sub.ID); err != nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("删除订阅失败: %v", err),
		}, nil
	}
	_ = svc.ClearUserDefaultModel(msg.SenderID)

	// Invalidate cached LLM client and HasCustomLLM cache
	a.llmFactory.Invalidate(msg.SenderID)
	a.llmFactory.invalidateUserMemos(msg.SenderID)
	a.llmFactory.InvalidateCustomLLMCache(msg.SenderID)

	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("已删除个人订阅 %q，将使用系统默认配置。", name),
	}, nil
}

// handleModels handles /models command to list all selectable models for the
// user (DB-driven, same source as the TUI picker), with status tags.
func (a *Agent) handleModels(ctx context.Context, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	entries := a.llmFactory.ListAllModelEntriesForUser(msg.SenderID)
	if len(entries) == 0 {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "暂无可用模型。请先用 /set-llm 配置个人 LLM 订阅。",
		}, nil
	}

	_, currentModel, _, _, _ := a.llmFactory.ResolveLLM(msg.SenderID, msg.ChatID, msg.Channel)

	var sb strings.Builder
	sb.WriteString("可用模型列表:\n")
	normal, offline, disabled := 0, 0, 0
	for _, e := range entries {
		var icon, tag string
		switch e.Status {
		case "normal":
			icon = "✓"
			normal++
		case "offline":
			icon = "○"
			offline++
		case "disabled":
			icon = "✗"
			disabled++
		default:
			icon = "✓"
			normal++
		}
		tag = fmt.Sprintf("[%s%s]", icon, e.Status)
		mark := ""
		if e.Model == currentModel {
			mark = " (当前)"
		}
		if e.SubName != "" {
			fmt.Fprintf(&sb, "%s %s · %s%s\n", tag, e.SubName, e.Model, mark)
		} else {
			fmt.Fprintf(&sb, "%s %s%s\n", tag, e.Model, mark)
		}
	}

	fmt.Fprintf(&sb, "\n共 %d 个模型（✓正常 %d · ○离线 %d · ✗禁用 %d）。\n", len(entries), normal, offline, disabled)
	sb.WriteString("使用 /set-model <model> 切换（✓/○ 可选，✗ 已禁用）。")

	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: sb.String(),
	}, nil
}

// handleSetModel handles /set-model <model> to switch the current model across
// subscriptions. Resolves the owning subscription and persists the user-level
// default model.
func (a *Agent) handleSetModel(ctx context.Context, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	// Parse command arguments
	trimmed := strings.TrimSpace(msg.Content)
	// Strip the leading command token (handles both "/set-model" and any alias).
	cmdName := "/set-model"
	if strings.HasPrefix(strings.ToLower(trimmed), cmdName) {
		trimmed = strings.TrimSpace(trimmed[len(cmdName):])
	}
	args := trimmed

	if args == "" {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "用法: /set-model <model>\n\n示例:\n  /set-model gpt-4\n  /set-model deepseek-chat\n  /set-model claude-3-5-sonnet-20241022\n\n使用 /models 查看可用模型列表。",
		}, nil
	}

	model := strings.TrimSpace(args)
	sub, err := a.llmFactory.ResolveSubscriptionForModel(msg.SenderID, model)
	if err != nil || sub == nil {
		// Help the user pick from available (non-disabled) models.
		entries := a.llmFactory.ListAllModelEntriesForUser(msg.SenderID)
		var avail []string
		for _, e := range entries {
			if e.Status != "disabled" {
				avail = append(avail, e.Model)
			}
		}
		if len(avail) == 0 {
			return &channel.OutboundMsg{
				Channel: msg.Channel, ChatID: msg.ChatID,
				Content: fmt.Sprintf("未找到提供模型 %q 的订阅，且当前无可选模型。\n\n请先用 /set-llm 创建个人 LLM 订阅。", model),
			}, nil
		}
		return &channel.OutboundMsg{
			Channel: msg.Channel, ChatID: msg.ChatID,
			Content: fmt.Sprintf("未找到提供模型 %q 的订阅。\n\n可选模型: %s\n\n使用 /set-model <model> 切换，或 /set-llm 添加新订阅。", model, strings.Join(avail, ", ")),
		}, nil
	}

	// Persist user-level default (subscription, model).
	if err := a.llmFactory.SetUserDefaultModel(msg.SenderID, sub.ID, model); err != nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel, ChatID: msg.ChatID,
			Content: fmt.Sprintf("切换模型失败: %v", err),
		}, nil
	}
	// Clear cached client so the next call rebuilds with the new model.
	a.llmFactory.Invalidate(msg.SenderID)

	name := sub.Name
	if name == "" {
		name = sub.ID
	}
	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("模型已切换为: %s（订阅: %s）", model, name),
	}, nil
}

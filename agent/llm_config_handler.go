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

const setLLMUsage = `设置/更新个人 LLM 订阅

用法: /set-llm <订阅名> provider=<provider> base_url=<url> api_key=<key> [model=<model>] [max_context=<tokens>] [max_output_tokens=<tokens>] [thinking_mode=<mode>]

说明:
  - <订阅名> 必须作为第一个参数，是位置参数（不是 key=value 形式）。
  - 同名订阅存在则按提供的字段更新，不存在则创建。
  - 更新时不传的字段保持不变；只有创建时 provider/base_url/api_key 才必填。
  - 订阅是模型的来源；用 /set-model <订阅名> <模型名> 切换模型，用 /models 查看可选模型。

必填参数（仅创建时；更新时可省略）:
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
  # 创建名为 openai-pro 的订阅
  /set-llm openai-pro provider=openai base_url=https://api.openai.com/v1 api_key=sk-xxx model=gpt-4

  # 更新已有的 openai-pro 订阅（只改 api_key，其它字段保持不变）
  /set-llm openai-pro api_key=sk-new

  # DeepSeek R1 (Thinking Mode)
  /set-llm deepseek-r1 provider=deepseek base_url=https://api.deepseek.com/v1 api_key=sk-xxx model=deepseek-reasoner thinking_mode=enabled

  # 智谱 GLM-5/GLM-4.7 (深度思考)
  /set-llm glm provider=openai base_url=https://open.bigmodel.cn/api/paas/v4 api_key=xxx model=glm-5 thinking_mode=enabled

  # Anthropic Claude
  /set-llm claude provider=anthropic base_url=https://api.anthropic.com api_key=sk-ant-xxx model=claude-3-5-sonnet-20241022

  # 限制上下文大小
  /set-llm local provider=openai base_url=http://localhost:8080/v1 api_key=sk-xxx model=gpt-4 max_context=8000

注意: API Key 会被加密存储，查询时只显示前4位。请在私聊中使用，避免在群聊暴露密钥。`

// handleSetLLM handles /set-llm to create/update a personal LLM subscription
// identified by name (positional argument). The name is mandatory; there is
// no "default subscription" fallback — every invocation targets the named
// subscription explicitly.
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

	// Parse a positional <订阅名> followed by key=value pairs.
	parts := parseSetLLMArgs(args)
	parseErrors := false
	seenKeys := make(map[string]bool)
	var subName string
	nameFound := false
	cfg := &sqlite.UserLLMConfig{
		SenderID: msg.SenderID,
	}
	for _, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 1 {
			// Positional argument: subscription name. Only the first positional
			// token is accepted; extra positional tokens are parse errors so
			// typos surface early instead of being silently dropped.
			if !nameFound {
				subName = strings.TrimSpace(kv[0])
				nameFound = true
			} else {
				parseErrors = true
			}
			continue
		}
		if len(kv) != 2 {
			parseErrors = true
			continue
		}
		key := strings.ToLower(kv[0])
		value := kv[1]
		seenKeys[key] = true

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

	// <订阅名> 必须作为第一个位置参数，没有"默认订阅"回退。
	if !nameFound || subName == "" {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("错误: 必须将 <订阅名> 作为第一个参数。\n\n%s", setLLMUsage),
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

	// Find the existing non-system subscription by name (case-insensitive).
	// System subscriptions are read-only and never matched.
	subs, _ := svc.List(msg.SenderID)
	var existing *sqlite.LLMSubscription
	for _, s := range subs {
		if s.IsSystem {
			continue
		}
		if existing == nil && strings.EqualFold(s.Name, subName) {
			existing = s
		}
	}

	created := false
	if existing != nil {
		// Update: only fields explicitly provided in the command are overwritten.
		// Without the seenKeys guards, omitting provider/base_url/api_key would
		// zero them out — a regression that silently broke working subscriptions.
		if seenKeys["provider"] {
			existing.Provider = cfg.Provider
		}
		if seenKeys["base_url"] {
			existing.BaseURL = cfg.BaseURL
		}
		if seenKeys["api_key"] {
			existing.APIKey = cfg.APIKey
		}
		if seenKeys["model"] {
			existing.Model = cfg.Model
		}
		if seenKeys["max_context"] {
			existing.MaxContext = cfg.MaxContext
		}
		if seenKeys["max_output_tokens"] {
			existing.MaxOutputTokens = cfg.MaxOutputTokens
		}
		if seenKeys["thinking_mode"] {
			existing.ThinkingMode = cfg.ThinkingMode
		}
		if err := svc.Update(existing); err != nil {
			return &channel.OutboundMsg{
				Channel: msg.Channel, ChatID: msg.ChatID,
				Content: fmt.Sprintf("更新订阅失败: %v", err),
			}, nil
		}
	} else {
		// Create: provider/base_url/api_key are mandatory for new subscriptions.
		if cfg.Provider == "" || cfg.BaseURL == "" || cfg.APIKey == "" {
			return &channel.OutboundMsg{
				Channel: msg.Channel, ChatID: msg.ChatID,
				Content: fmt.Sprintf("错误: 新建订阅必须提供 provider, base_url 和 api_key 参数。\n\n%s", setLLMUsage),
			}, nil
		}
		// No "default subscription" concept — subscriptions are model sources.
		// IsDefault is always false; the first subscription does NOT become default.
		sub := &sqlite.LLMSubscription{
			SenderID:        msg.SenderID,
			Name:            subName,
			Provider:        cfg.Provider,
			BaseURL:         cfg.BaseURL,
			APIKey:          cfg.APIKey,
			Model:           cfg.Model,
			MaxContext:      cfg.MaxContext,
			MaxOutputTokens: cfg.MaxOutputTokens,
			ThinkingMode:    cfg.ThinkingMode,
			Enabled:         true,
		}
		if err := svc.Add(sub); err != nil {
			return &channel.OutboundMsg{
				Channel: msg.Channel, ChatID: msg.ChatID,
				Content: fmt.Sprintf("创建订阅失败: %v", err),
			}, nil
		}
		created = true
		existing = sub
	}

	// Invalidate cached LLM client and HasCustomLLM cache
	a.llmFactory.Invalidate(msg.SenderID)

	// Display the persisted state (post update/create), not the raw input,
	// so the user sees what actually got saved.
	maskedKey := maskAPIKey(existing.APIKey)

	var maxContextStr string
	if existing.MaxContext > 0 {
		maxContextStr = fmt.Sprintf("\n- Max Context: %d", existing.MaxContext)
	}

	var thinkingModeStr string
	if existing.ThinkingMode != "" {
		thinkingModeStr = fmt.Sprintf("\n- Thinking Mode: %s", existing.ThinkingMode)
	} else {
		thinkingModeStr = "\n- Thinking Mode: auto"
	}

	modelDisplay := existing.Model
	if modelDisplay == "" {
		modelDisplay = "(由订阅自动选取)"
	}

	verb := "已更新"
	if created {
		verb = "已创建"
	}

	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("订阅 %s:\n- Name: %s\n- Provider: %s\n- Base URL: %s\n- API Key: %s\n- Model: %s%s%s%s\n\n用 /models 查看可选模型，/set-model <订阅名> <模型名> 切换。",
			verb, existing.Name, existing.Provider, existing.BaseURL, maskedKey, modelDisplay, maxContextStr, thinkingModeStr, warning),
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

	// Resolve the active subscription for this session (not model-name guess).
	sub, _, _ := a.llmFactory.ResolveActiveSubModel(msg.SenderID, msg.ChatID, msg.Channel)

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

// handleListLLMs handles /llms command to list all subscriptions for the
// current user (including the system subscription). Shows name, provider,
// base URL, model, and enabled status.
func (a *Agent) handleListLLMs(ctx context.Context, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	svc := a.llmFactory.GetSubscriptionSvc()
	if svc == nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "订阅服务未初始化。",
		}, nil
	}

	subs, err := svc.List(msg.SenderID)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}

	if len(subs) == 0 {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "暂无订阅。使用 /set-llm 创建个人 LLM 订阅。",
		}, nil
	}

	var sb strings.Builder
	sb.WriteString("你的 LLM 订阅:\n")
	for _, sub := range subs {
		status := "✓"
		if !sub.Enabled {
			status = "✗"
		}
		label := sub.Name
		if sub.IsSystem {
			label = "system (系统默认)"
		}
		model := sub.Model
		if model == "" {
			model = "(未设置)"
		}
		fmt.Fprintf(&sb, "\n%s %s\n  Provider: %s\n  Base URL: %s\n  API Key: %s\n  Model: %s\n",
			status, label, sub.Provider, sub.BaseURL, maskAPIKey(sub.APIKey), model)
	}
	sb.WriteString("\n✓ 启用  ✗ 禁用\n")
	sb.WriteString("使用 /set-llm 添加/更新订阅，/unset-llm <名称> 删除订阅。")
	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: sb.String(),
	}, nil
}

// activeSubModel resolves the subscription and model that the given session is
// currently using, via the model-first ResolveActiveSubModel chain. This
// replaces the old "default subscription" (GetDefault) lookup so that
// per-model config (max_context / max_output_tokens) targets the (sub, model)
// the user is actually interacting with in this session, not the user's
// default subscription's default model. A model is identified by the
// (subscription, model) pair, not by a "default subscription" concept.
func (a *Agent) activeSubModel(senderID, chatID, channel string) (*sqlite.LLMSubscription, string, error) {
	if a.llmFactory == nil {
		return nil, "", fmt.Errorf("LLM 工厂未初始化")
	}
	return a.llmFactory.ResolveActiveSubModel(senderID, chatID, channel)
}

// GetUserMaxContext returns the user's max_context setting (0 = use default).
// Reads the per-model override for the (sub, model) the session is currently
// using (resolved via the model-first ResolveActiveSubModel chain), falling
// back to the subscription-level value.
func (a *Agent) GetUserMaxContext(senderID, chatID, channel string) int {
	sub, model, err := a.activeSubModel(senderID, chatID, channel)
	if err != nil || sub == nil {
		return 0
	}
	if v := sub.GetPerModelMaxContext(model); v > 0 {
		return v
	}
	return sub.MaxContext
}

// SetUserMaxContext updates the per-model max_context for the (sub, model) the
// session is currently using (resolved via the model-first chain) and
// invalidates the cached LLM client.
func (a *Agent) SetUserMaxContext(senderID, chatID, channel string, maxContext int) error {
	if maxContext < 1000 || maxContext > 2000000 {
		return fmt.Errorf("max_context must be between 1000 and 2000000, got %d", maxContext)
	}
	sub, model, err := a.activeSubModel(senderID, chatID, channel)
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
// Reads the per-model override for the (sub, model) the session is currently
// using (resolved via the model-first ResolveActiveSubModel chain).
func (a *Agent) GetUserMaxOutputTokens(senderID, chatID, channel string) int {
	sub, model, err := a.activeSubModel(senderID, chatID, channel)
	if err != nil || sub == nil {
		return 0
	}
	if v := sub.GetPerModelMaxTokens(model); v > 0 {
		return v
	}
	return sub.MaxOutputTokens
}

// SetUserMaxOutputTokens updates the per-model max_output_tokens for the (sub,
// model) the session is currently using (resolved via the model-first chain)
// and invalidates the cached LLM client.
func (a *Agent) SetUserMaxOutputTokens(senderID, chatID, channel string, maxTokens int) error {
	if maxTokens < 0 || maxTokens > 2000000 {
		return fmt.Errorf("max_output_tokens must be between 0 and 2000000, got %d", maxTokens)
	}
	sub, model, err := a.activeSubModel(senderID, chatID, channel)
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

// GetUserMaxContextForSubModel returns the per-(subID, model) max_context,
// bypassing session resolution. Used by channel UIs (feishu) that already
// know the explicit (subID, model) pair from the model selector — they MUST
// NOT resolve subscription from model name alone (per project policy).
// Falls back to subscription-level MaxContext when no per-model override.
func (a *Agent) GetUserMaxContextForSubModel(senderID, subID, model string) int {
	if a.llmFactory == nil || subID == "" || model == "" {
		return 0
	}
	svc := a.llmFactory.GetSubscriptionSvc()
	sub, err := svc.Get(subID)
	if err != nil || sub == nil {
		return 0
	}
	if v := sub.GetPerModelMaxContext(model); v > 0 {
		return v
	}
	return sub.MaxContext
}

// SetUserMaxContextForSubModel updates the per-(subID, model) max_context
// directly, bypassing session resolution. Preserves existing MaxOutputTokens /
// ThinkingMode / APIType on the PerModelConfig row. Invalidates the cached
// LLM client for the sender.
func (a *Agent) SetUserMaxContextForSubModel(senderID, subID, model string, maxContext int) error {
	if maxContext < 1000 || maxContext > 2000000 {
		return fmt.Errorf("max_context must be between 1000 and 2000000, got %d", maxContext)
	}
	if a.llmFactory == nil || subID == "" || model == "" {
		return fmt.Errorf("subID and model are required for per-model max_context")
	}
	svc := a.llmFactory.GetSubscriptionSvc()
	existing, _ := svc.GetModel(subID, model)
	var maxOut int
	var thinking, apiType string
	if existing != nil {
		maxOut = existing.MaxOutputTokens
		thinking = existing.ThinkingMode
		apiType = existing.APIType
	}
	if err := svc.UpsertModel(subID, model, maxContext, maxOut, thinking, apiType); err != nil {
		return fmt.Errorf("save per-model max_context: %w", err)
	}
	a.llmFactory.Invalidate(senderID)
	return nil
}

// GetUserMaxOutputTokensForSubModel returns the per-(subID, model)
// max_output_tokens, bypassing session resolution. Used by channel UIs that
// already know the explicit (subID, model) pair.
func (a *Agent) GetUserMaxOutputTokensForSubModel(senderID, subID, model string) int {
	if a.llmFactory == nil || subID == "" || model == "" {
		return 0
	}
	svc := a.llmFactory.GetSubscriptionSvc()
	sub, err := svc.Get(subID)
	if err != nil || sub == nil {
		return 0
	}
	if v := sub.GetPerModelMaxTokens(model); v > 0 {
		return v
	}
	return sub.MaxOutputTokens
}

// SetUserMaxOutputTokensForSubModel updates the per-(subID, model)
// max_output_tokens directly, bypassing session resolution. Preserves existing
// MaxContext / ThinkingMode / APIType on the PerModelConfig row.
func (a *Agent) SetUserMaxOutputTokensForSubModel(senderID, subID, model string, maxTokens int) error {
	if maxTokens < 0 || maxTokens > 2000000 {
		return fmt.Errorf("max_output_tokens must be between 0 and 2000000, got %d", maxTokens)
	}
	if a.llmFactory == nil || subID == "" || model == "" {
		return fmt.Errorf("subID and model are required for per-model max_output_tokens")
	}
	svc := a.llmFactory.GetSubscriptionSvc()
	existing, _ := svc.GetModel(subID, model)
	var maxCtx int
	var thinking, apiType string
	if existing != nil {
		maxCtx = existing.MaxContext
		thinking = existing.ThinkingMode
		apiType = existing.APIType
	}
	if err := svc.UpsertModel(subID, model, maxCtx, maxTokens, thinking, apiType); err != nil {
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
		a.llmFactory.InvalidateSender(senderID)
	}
	return nil
}

// GetUserTierModel returns the per-user tier model setting from user_settings DB.
// Returns (subID, model). Both may be empty when unset (falls back to global config
// in resolveTierModel). Uses the same canonical channel as thinking_mode so tier
// settings are shared across all channels (CLI, Feishu, Web) per user.
func (a *Agent) GetUserTierModel(senderID, tier string) (subID, model string) {
	if a.llmFactory == nil || a.settingsSvc == nil {
		return "", ""
	}
	vals, err := a.settingsSvc.GetSettings(thinkingModeChannel, senderID)
	if err != nil || vals == nil {
		return "", ""
	}
	raw := vals["tier_"+tier]
	if raw == "" {
		return "", ""
	}
	return parseTierValue(raw)
}

// SetUserTierModel updates the per-user tier model setting in user_settings DB.
// Value is stored as "subID|model" (or plain "model" when subID is empty).
// Invalidates the cached LLM client for the sender.
func (a *Agent) SetUserTierModel(senderID, tier, subID, model string) error {
	if a.settingsSvc == nil {
		return ErrSettingsUnavailable
	}
	val := model
	if subID != "" {
		val = subID + "|" + model
	}
	if err := a.settingsSvc.SetSetting(thinkingModeChannel, senderID, "tier_"+tier, val); err != nil {
		return fmt.Errorf("save tier_%s: %w", tier, err)
	}
	if a.llmFactory != nil {
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

// handleUnsetLLM handles /unset-llm <订阅名> to delete a personal subscription
// by name. If the deleted subscription was the user's last-used model, clears
// the last-used record so new sessions fall back to the system subscription.
func (a *Agent) handleUnsetLLM(ctx context.Context, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	svc := a.llmFactory.GetSubscriptionSvc()
	if svc == nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "订阅服务未初始化。",
		}, nil
	}

	// Parse subscription name from command args.
	trimmed := strings.TrimSpace(msg.Content)
	cmdName := "/unset-llm"
	if strings.HasPrefix(strings.ToLower(trimmed), cmdName) {
		trimmed = strings.TrimSpace(trimmed[len(cmdName):])
	}
	subName := trimmed

	if subName == "" {
		// No name given — list available personal subscriptions.
		subs, _ := svc.List(msg.SenderID)
		var names []string
		for _, s := range subs {
			if s.IsSystem {
				continue
			}
			names = append(names, s.Name)
		}
		if len(names) == 0 {
			return &channel.OutboundMsg{
				Channel: msg.Channel, ChatID: msg.ChatID,
				Content: "你没有个人订阅。使用 /set-llm 创建。",
			}, nil
		}
		return &channel.OutboundMsg{
			Channel: msg.Channel, ChatID: msg.ChatID,
			Content: fmt.Sprintf("用法: /unset-llm <订阅名>\n\n你的订阅:\n  %s", strings.Join(names, "\n  ")),
		}, nil
	}

	// Find subscription by name (case-insensitive, skip system).
	subs, _ := svc.List(msg.SenderID)
	var target *sqlite.LLMSubscription
	for _, s := range subs {
		if s.IsSystem {
			continue
		}
		if strings.EqualFold(s.Name, subName) {
			target = s
			break
		}
	}
	if target == nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel, ChatID: msg.ChatID,
			Content: fmt.Sprintf("未找到名为 %q 的个人订阅。", subName),
		}, nil
	}

	if err := svc.Remove(target.ID); err != nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel, ChatID: msg.ChatID,
			Content: fmt.Sprintf("删除订阅失败: %v", err),
		}, nil
	}

	// Clear last-used model if it pointed to the deleted subscription.
	if udm, _ := svc.GetUserDefaultModel(msg.SenderID); udm != nil && udm.SubscriptionID == target.ID {
		_ = svc.ClearUserDefaultModel(msg.SenderID)
	}

	// Invalidate cached LLM client and HasCustomLLM cache
	a.llmFactory.Invalidate(msg.SenderID)

	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("已删除订阅 %q。", target.Name),
	}, nil
}

// handleModels handles /models command to list all selectable models for the
// user. It first triggers a live refresh of /models for every enabled
// subscription (so the list reflects each provider's true available models),
// then renders the fresh entries with status tags. The refresh is a direct
// call to the same reusable method used by the TUI picker's manual refresh.
// Per-subscription refresh outcomes are appended so the user can see which
// subscriptions refreshed OK and which failed (and why) — previously a failed
// /models fetch was Warn-level log only, invisible in chat, leaving the user
// with a silently incomplete model list and no explanation.
func (a *Agent) handleModels(ctx context.Context, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	entries, results := a.llmFactory.RefreshModelEntriesForUserWithResults(msg.SenderID)
	if len(entries) == 0 && !hasRefreshableSubs(results) {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "暂无可用模型。请先用 /set-llm 配置个人 LLM 订阅。",
		}, nil
	}

	_, currentModel, _, _, _ := a.llmFactory.ResolveLLM(msg.SenderID, msg.ChatID, msg.Channel)

	var sb strings.Builder
	sb.WriteString("可用模型列表（已刷新）:\n")
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
	sb.WriteString("使用 /set-model <订阅名> <模型名> 切换（✓/○ 可选，✗ 已禁用）。")

	// Append refresh summary so the user can see which subscriptions fetched OK
	// and which failed — without this, a failed /models fetch looks like the
	// subscription simply has no models, with no clue to the real cause.
	if summary := formatRefreshSummary(results); summary != "" {
		sb.WriteString("\n\n")
		sb.WriteString(summary)
	}

	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: sb.String(),
	}, nil
}

// hasRefreshableSubs reports whether any subscription was actually refreshed
// (i.e. not all were skipped/empty). When false and entries is also empty, the
// /models command shows the "no models" empty-state instead of a bare list.
func hasRefreshableSubs(results []RefreshResult) bool {
	for _, r := range results {
		if r.Status == "ok" || r.Status == "fail" || r.Status == "noloader" || r.Status == "noclient" || r.Status == "pending" {
			return true
		}
	}
	return false
}

// formatRefreshSummary renders per-subscription refresh outcomes for the /models
// footer. Only non-OK results are shown in detail; successful refreshes are
// summarized as a count to keep the output concise. Returns "" when there are
// no subscriptions to report (e.g. subscriptionSvc was nil).
func formatRefreshSummary(results []RefreshResult) string {
	if len(results) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("⚙️ 模型刷新结果:")
	okCount := 0
	failCount := 0
	for _, r := range results {
		switch r.Status {
		case "ok":
			okCount++
		case "fail", "noclient":
			failCount++
			name := r.SubName
			if name == "" {
				name = "system"
			}
			detail := r.Error
			if detail == "" {
				detail = "未知错误"
			}
			fmt.Fprintf(&sb, "\n  ❌ %s: %s", name, detail)
		case "skipped":
			// Only show skipped when it's meaningful (has an error reason).
			if r.Error != "" {
				name := r.SubName
				if name == "" {
					name = "system"
				}
				fmt.Fprintf(&sb, "\n  ⏭️ %s: %s", name, r.Error)
			}
		case "noloader":
			// Non-OpenAI providers (Anthropic) don't expose /models — not an error,
			// silently omitted from the summary.
		case "pending":
			// Should not reach here after wg.Wait(); treat as fail for visibility.
			failCount++
			fmt.Fprintf(&sb, "\n  ❌ %s: 刷新超时", r.SubName)
		}
	}
	if okCount > 0 {
		fmt.Fprintf(&sb, "\n  ✅ %d 个订阅刷新成功", okCount)
	}
	if failCount == 0 && okCount > 0 {
		// All good — don't append the summary, keep output clean.
		return ""
	}
	return sb.String()
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
			Content: "用法: /set-model <订阅名> <模型名>\n\n示例:\n  /set-model glm-h20 /data/models/skyai/GLM-5.2-W4AFP8\n  /set-model openai gpt-4o\n\n使用 /models 查看可用模型列表（含订阅名和模型名）。",
		}, nil
	}

	// Must specify both subscription name and model name.
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "用法: /set-model <订阅名> <模型名>\n\n示例: /set-model glm-h20 /data/models/skyai/GLM-5.2-W4AFP8\n\n使用 /models 查看可用模型列表。",
		}, nil
	}
	subName := parts[0]
	model := strings.Join(parts[1:], " ")

	// Look up the subscription by name for this user.
	subs, err := a.llmFactory.GetSubscriptionSvc().List(msg.SenderID)
	if err != nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel, ChatID: msg.ChatID,
			Content: fmt.Sprintf("查询订阅失败: %v", err),
		}, nil
	}

	var targetSub *sqlite.LLMSubscription
	for _, s := range subs {
		if s.Name == subName {
			targetSub = s
			break
		}
	}
	if targetSub == nil {
		// Refresh model lists and show available sub/model pairs.
		entries, _ := a.llmFactory.RefreshModelEntriesForUserWithResults(msg.SenderID)
		var avail []string
		for _, e := range entries {
			if e.Status != "disabled" {
				avail = append(avail, fmt.Sprintf("%s %s", e.SubName, e.Model))
			}
		}
		content := fmt.Sprintf("未找到名为 %q 的订阅。", subName)
		if len(avail) > 0 {
			content += "\n\n可用的订阅与模型:\n  " + strings.Join(avail, "\n  ")
		} else {
			content += "\n\n使用 /set-llm 创建个人 LLM 订阅。"
		}
		return &channel.OutboundMsg{
			Channel: msg.Channel, ChatID: msg.ChatID,
			Content: content,
		}, nil
	}

	// Per-session switch: write tenants table for THIS session, and update
	// last-used-model (via SelectModel).
	if !targetSub.Enabled {
		return &channel.OutboundMsg{
			Channel: msg.Channel, ChatID: msg.ChatID,
			Content: fmt.Sprintf("订阅 %q 已禁用，请先启用。", subName),
		}, nil
	}

	if err := a.llmFactory.SelectModel(msg.SenderID, msg.ChatID, msg.Channel, targetSub.ID, model); err != nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel, ChatID: msg.ChatID,
			Content: fmt.Sprintf("切换模型失败: %v", err),
		}, nil
	}

	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("模型已切换: %s（订阅: %s）", model, subName),
	}, nil
}

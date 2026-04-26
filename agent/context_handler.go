package agent

import (
	"context"
	"fmt"
	"strconv"

	"xbot/bus"
	"xbot/llm"
	"xbot/session"
)

// formatTokenCount formats a token count for display (e.g. 1234567 → "1.2M").
func formatTokenCount(n int64) string {
	if n >= 1_000_000_000 {
		return fmt.Sprintf("%.1fB", float64(n)/1_000_000_000)
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return strconv.FormatInt(n, 10)
}

// handleContextInfo handles /context info command: displays current token count and composition
func (a *Agent) handleContextInfo(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	_, model, _, _ := a.llmFactory.GetLLM(msg.SenderID)

	// Use buildPrompt to get full context (including system, skills, memory, etc.)
	messages, err := a.buildPrompt(ctx, msg, tenantSession)
	if err != nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "获取上下文失败，请重试。",
		}, nil
	}

	// Get tool definitions and calculate tokens
	sessionKey := msg.Channel + ":" + msg.ChatID
	toolDefs := visibleToolDefs(a.tools.AsDefinitionsForSession(sessionKey), a.settingsSvc, msg.Channel, msg.SenderID)
	toolDefsTokens, _ := llm.CountToolsTokens(toolDefs, model)

	// Prefer API-returned prompt_tokens (authoritative) over local estimation.
	// Read from current tenant's DB — Agent-level lastPromptTokens is shared across
	// all chats and would show wrong values for other sessions.
	var apiTokens int64
	if tenantSession != nil {
		if memSvc := tenantSession.MemoryService(); memSvc != nil {
			if pt, _, err := memSvc.GetTokenState(ctx, tenantSession.TenantID()); err == nil {
				apiTokens = pt
			}
		}
	}
	cm := a.GetContextManager()
	stats := cm.ContextInfo(messages, model, toolDefsTokens)

	// Override total with API value if available
	tokenSource := "Estimated"
	if apiTokens > 0 {
		stats.TotalTokens = int(apiTokens)
		tokenSource = "API"
	}

	content := fmt.Sprintf(`📊 上下文 Token 统计 (来源: %s)

| 角色 | Token | 占比 |
|------|-------|------|
| System | %d | %.1f%% |
| User | %d | %.1f%% |
| Assistant | %d | %.1f%% |
| Tool (消息) | %d | %.1f%% |
| Tool (定义) | %d | %.1f%% |
| **总计** | **%d** | 100%% |

⚙️ Configuration:
- 最大上下文: %d tokens
- 压缩阈值: %d tokens (%.0f%%)
- 当前模式: %s`,
		tokenSource,
		stats.SystemTokens, float64(stats.SystemTokens)*100/float64(max(stats.TotalTokens, 1)),
		stats.UserTokens, float64(stats.UserTokens)*100/float64(max(stats.TotalTokens, 1)),
		stats.AssistantTokens, float64(stats.AssistantTokens)*100/float64(max(stats.TotalTokens, 1)),
		stats.ToolMsgTokens, float64(stats.ToolMsgTokens)*100/float64(max(stats.TotalTokens, 1)),
		stats.ToolDefTokens, float64(stats.ToolDefTokens)*100/float64(max(stats.TotalTokens, 1)),
		stats.TotalTokens,
		stats.MaxTokens,
		stats.Threshold,
		a.contextManagerConfig.CompressionThreshold*100,
		stats.Mode,
	)

	// Runtime override info
	if stats.IsRuntimeOverride {
		content += fmt.Sprintf("（Runtime override, default is %s）", stats.DefaultMode)
	}

	// Per-user cumulative token usage
	if a.multiSession != nil {
		usage, err := a.multiSession.GetUserTokenUsage(msg.SenderID)
		if err == nil && usage.TotalTokens > 0 {
			content += fmt.Sprintf(`

👤 用户累计用量 (%s):
- 总 Token: %s
  (输入 %s · 输出 %s)
- 对话轮次: %d
- LLM 调用: %d`,
				usage.SenderID,
				formatTokenCount(usage.TotalTokens),
				formatTokenCount(usage.InputTokens),
				formatTokenCount(usage.OutputTokens),
				usage.ConversationCount,
				usage.LLMCallCount,
			)
		}
	}

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: content,
	}, nil
}

// handleContextMode handles /context mode sub-command
func (a *Agent) handleContextMode(ctx context.Context, msg bus.InboundMessage, modeStr string) (*bus.OutboundMessage, error) {
	cfg := a.contextManagerConfig

	if modeStr == "" {
		// Query current mode only
		stats := a.GetContextManager().ContextInfo(nil, "", 0)
		overrideInfo := ""
		if stats.IsRuntimeOverride {
			overrideInfo = fmt.Sprintf("（Runtime override, default is %s）", stats.DefaultMode)
		}
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("当前上下文模式: %s %s", cfg.EffectiveMode(), overrideInfo),
		}, nil
	}

	target := ContextMode(modeStr)
	if target == "default" {
		cfg.ResetRuntimeMode()
		a.SetContextManager(NewContextManager(cfg))
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("Restored default context mode: %s", cfg.DefaultMode),
		}, nil
	}

	if !IsValidContextMode(target) {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "Invalid mode. Options:: phase1, none, default",
		}, nil
	}

	// Set configuration first, then replace manager
	cfg.SetRuntimeMode(target)
	a.SetContextManager(NewContextManager(cfg))

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("Switched context mode: %s", target),
	}, nil
}

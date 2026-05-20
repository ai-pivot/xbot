package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/llm"
	"xbot/session"
	"xbot/storage/sqlite"
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
	return fmt.Sprintf("%d", n)
}

// handleContextInfo 处理 /context info 命令：显示当前 token 数和组成
func (a *Agent) handleContextInfo(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*channel.OutboundMsg, error) {
	_, model, _, _ := a.llmFactory.GetLLM(msg.SenderID)

	// 使用 buildPrompt 获取完整上下文（包含 system、skills、memory 等）
	messages, err := a.buildPrompt(ctx, msg, tenantSession)
	if err != nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "获取上下文失败，请重试。",
		}, nil
	}

	// 获取工具定义并计算 token
	sessionKey := msg.Channel + ":" + msg.ChatID
	toolDefs := visibleToolDefs(a.tools.AsDefinitionsForSession(sessionKey, tenantSession.TenantID()), a.settingsSvc, msg.Channel, msg.SenderID)
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
	tokenSource := "估算"
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

⚙️ 配置:
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

	// 运行时覆盖信息
	if stats.IsRuntimeOverride {
		content += fmt.Sprintf("（运行时覆盖，默认为 %s）", stats.DefaultMode)
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

	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: content,
	}, nil
}

// handleContextMode 处理 /context mode 子命令
func (a *Agent) handleContextMode(ctx context.Context, msg bus.InboundMessage, modeStr string) (*channel.OutboundMsg, error) {
	cfg := a.contextManagerConfig

	if modeStr == "" {
		// 仅查询当前模式
		stats := a.GetContextManager().ContextInfo(nil, "", 0)
		overrideInfo := ""
		if stats.IsRuntimeOverride {
			overrideInfo = fmt.Sprintf("（运行时覆盖，默认为 %s）", stats.DefaultMode)
		}
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("当前上下文模式: %s %s", cfg.EffectiveMode(), overrideInfo),
		}, nil
	}

	target := ContextMode(modeStr)
	if target == "default" {
		cfg.ResetRuntimeMode()
		a.SetContextManager(NewContextManager(cfg))
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("已恢复默认上下文模式: %s", cfg.DefaultMode),
		}, nil
	}

	if !IsValidContextMode(target) {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "无效模式。可选: phase1, none, default",
		}, nil
	}

	// 先设置配置，再替换 manager
	cfg.SetRuntimeMode(target)
	a.SetContextManager(NewContextManager(cfg))

	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("已切换上下文模式: %s", target),
	}, nil
}

// handleUsage handles /usage command: shows token usage statistics.
// Uses multiSession directly (no RPC), so it's safe to call from agent goroutine.
func (a *Agent) handleUsage(ctx context.Context, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	if a.multiSession == nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "Usage tracking not available",
		}, nil
	}

	cumulative, err := a.multiSession.GetUserTokenUsage(msg.SenderID)
	if err != nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("Failed to query usage: %v", err),
		}, nil
	}

	daily, _ := a.multiSession.GetDailyTokenUsage(msg.SenderID, 30)

	var sb strings.Builder
	sb.WriteString("# Token Usage\n\n")

	if cumulative != nil && cumulative.TotalTokens > 0 {
		usageDays := 0
		if len(daily) > 0 {
			earliest := daily[len(daily)-1].Date
			if first, err := time.Parse("2006-01-02", earliest); err == nil {
				usageDays = int(time.Since(first).Hours()/24) + 1
			}
		}

		sb.WriteString("## Summary\n\n")
		sb.WriteString("| | |\n|---|---|\n")
		fmt.Fprintf(&sb, "| **Total tokens** | **%s** |\n", formatTokenCount(cumulative.TotalTokens))
		fmt.Fprintf(&sb, "| Input | %s |\n", formatTokenCount(cumulative.InputTokens))
		fmt.Fprintf(&sb, "| Output | %s |\n", formatTokenCount(cumulative.OutputTokens))
		fmt.Fprintf(&sb, "| Cached | %s |\n", formatTokenCount(cumulative.CachedTokens))
		fmt.Fprintf(&sb, "| Conversations | %d |\n", cumulative.ConversationCount)
		fmt.Fprintf(&sb, "| LLM calls | %d |\n", cumulative.LLMCallCount)
		if usageDays > 0 {
			fmt.Fprintf(&sb, "| **Usage duration** | **%d days** |\n", usageDays)
			avgDaily := cumulative.TotalTokens / int64(usageDays)
			fmt.Fprintf(&sb, "| Avg daily tokens | %s |\n", formatTokenCount(avgDaily))
		}

		sb.WriteString("\n### Analysis\n\n")
		sb.WriteString("| | |\n|---|---|\n")
		if cumulative.InputTokens > 0 {
			cacheRate := float64(cumulative.CachedTokens) / float64(cumulative.InputTokens) * 100
			fmt.Fprintf(&sb, "| **Cache hit rate** | **%.1f%%** |\n", cacheRate)
			nonCachedInput := cumulative.InputTokens - cumulative.CachedTokens
			fmt.Fprintf(&sb, "| Actual input (non-cached) | %s |\n", formatTokenCount(nonCachedInput))
		}
		if cumulative.LLMCallCount > 0 {
			avgIn := cumulative.InputTokens / cumulative.LLMCallCount
			avgOut := cumulative.OutputTokens / cumulative.LLMCallCount
			fmt.Fprintf(&sb, "| Avg input/call | %s |\n", formatTokenCount(avgIn))
			fmt.Fprintf(&sb, "| Avg output/call | %s |\n", formatTokenCount(avgOut))
		}
		if cumulative.ConversationCount > 0 {
			avgCalls := float64(cumulative.LLMCallCount) / float64(cumulative.ConversationCount)
			fmt.Fprintf(&sb, "| Avg calls/conversation | %.1f |\n", avgCalls)
		}
	} else {
		sb.WriteString("No usage data recorded yet.\n")
	}

	// Today's usage by model
	today := time.Now().Format("2006-01-02")
	var todayEntries []sqlite.DailyTokenUsage
	for _, d := range daily {
		if d.Date == today {
			todayEntries = append(todayEntries, d)
		}
	}
	if len(todayEntries) > 0 {
		sb.WriteString("\n## Today's Usage by Model\n\n")
		sb.WriteString("| Model | Input | Output | Cached | Cache% | Calls |\n")
		sb.WriteString("|-------|-------|--------|--------|--------|-------|\n")
		slices.SortFunc(todayEntries, func(a, b sqlite.DailyTokenUsage) int {
			return int((b.InputTokens + b.OutputTokens) - (a.InputTokens + a.OutputTokens))
		})
		for _, d := range todayEntries {
			model := d.Model
			if model == "" {
				model = "(unknown)"
			}
			cacheRate := ""
			if d.InputTokens > 0 {
				cacheRate = fmt.Sprintf("%.0f%%", float64(d.CachedTokens)/float64(d.InputTokens)*100)
			}
			fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | %d |\n",
				model,
				formatTokenCount(d.InputTokens),
				formatTokenCount(d.OutputTokens),
				formatTokenCount(d.CachedTokens),
				cacheRate,
				d.LLMCallCount,
			)
		}
	}

	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: sb.String(),
	}, nil
}

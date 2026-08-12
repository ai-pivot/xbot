package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/protocol"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/tools"
	"xbot/version"
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
	userCtx := UserContextFromContext(ctx)
	toolDefs := visibleToolDefs(a.tools.AsDefinitionsForSession(sessionKey, tenantSession.TenantID()), userCtx.PermUsers, msg.Channel)
	toolDefsTokens := len(toolDefs) * 200 // rough estimate

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
	stats := cm.ContextInfo(messages, userCtx.Model, toolDefsTokens)

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

// handleSessionInfo handles /info command: shows current session info.
func (a *Agent) handleSessionInfo(ctx context.Context, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	uc := UserContextFromContext(ctx)
	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("获取会话失败: %v", err),
		}, nil
	}

	var sb strings.Builder
	sb.WriteString("📋 会话信息\n\n")

	// Session identity
	fmt.Fprintf(&sb, "| 项目 | 值 |\n|---|---|\n")
	// Session ID = X-Session-Id 的值（channel:chatID），可直接用于日志 grep
	// （session_id=...）或 LLM 提供商 dashboard 核对（X-Session-Id header）。
	sessionID := qualifyChatID(msg.Channel, msg.ChatID)
	fmt.Fprintf(&sb, "| Session ID | `%s` |\n", sessionID)
	fmt.Fprintf(&sb, "| Channel | %s |\n", msg.Channel)
	fmt.Fprintf(&sb, "| Chat ID | %s |\n", msg.ChatID)
	// User ID = X-User-Id 的值（LLM 请求 header）。
	if msg.SenderID != "" {
		fmt.Fprintf(&sb, "| User ID | %s |\n", msg.SenderID)
	}
	// Turn ID = X-Turn-Id 的值（当前轮次）。
	if raw := msg.Metadata["turn_id"]; raw != "" {
		fmt.Fprintf(&sb, "| Turn ID | %s |\n", raw)
	}
	// User Role（admin/user，来自 UserContext）
	if uc != nil && uc.Role != "" {
		fmt.Fprintf(&sb, "| User Role | %s |\n", uc.Role)
	}
	// Request ID 格式说明：X-Request-Id = <session>-t<turn>-<n>，每次 LLM 调用
	// 唯一（重试复用）。grep 日志用 request_id=<session>-t<turn>- 前缀即可
	// 找到该 turn 的全部 LLM 调用。
	turnTag := ""
	if raw := msg.Metadata["turn_id"]; raw != "" {
		turnTag = "t" + raw
	}
	fmt.Fprintf(&sb, "| Request ID 前缀 | `%s-%s-N` |\n", sessionID, turnTag)
	fmt.Fprintf(&sb, "| Tenant ID | %d |\n", tenantSession.TenantID())

	// 会话状态（最后活跃 / 最大 Turn / 是否迭代中 / worktree）
	if la := tenantSession.LastActive(); !la.IsZero() {
		fmt.Fprintf(&sb, "| 最后活跃 | %s |\n", la.Format("2006-01-02 15:04:05"))
	}
	if maxTurn, err := tenantSession.GetMaxTurnID(); err == nil && maxTurn > 0 {
		fmt.Fprintf(&sb, "| 最大 Turn | %d |\n", maxTurn)
	}
	if v, ok := a.bgSessionStates.Load(sessionID); ok {
		if ss, ok := v.(*bgSessionState); ok {
			if ss.busy.Load() {
				fmt.Fprintf(&sb, "| 状态 | 🔄 迭代中 |\n")
			} else {
				fmt.Fprintf(&sb, "| 状态 | 空闲 |\n")
			}
		}
	}
	if entry := tools.GlobalWorktreeRegistry.GetBySession(sessionID); entry != nil && entry.WorktreeDir != "" {
		fmt.Fprintf(&sb, "| Worktree | `%s` |\n", entry.WorktreeDir)
		if entry.Branch != "" {
			fmt.Fprintf(&sb, "| 分支 | %s |\n", entry.Branch)
		}
	}

	// CWD
	if cwd := tenantSession.GetCurrentDir(); cwd != "" {
		fmt.Fprintf(&sb, "| 工作目录 | `%s` |\n", cwd)
	}

	// LLM info
	_, model, maxCtx, thinkingMode, maxOut := uc.ResolveLLM(msg.ChatID)
	if model != "" {
		fmt.Fprintf(&sb, "| 模型 | %s |\n", model)
	}
	sub, _, _ := uc.ResolveActiveSub(msg.ChatID)
	if sub != nil {
		fmt.Fprintf(&sb, "| 订阅 | %s (`%s`) |\n", sub.Name, sub.ID)
	}
	if maxCtx > 0 {
		fmt.Fprintf(&sb, "| Max Context | %d |\n", maxCtx)
	}
	if maxOut > 0 {
		fmt.Fprintf(&sb, "| Max Output | %d |\n", maxOut)
	}
	if thinkingMode != "" {
		fmt.Fprintf(&sb, "| Thinking Mode | %s |\n", thinkingMode)
	}

	// Message count
	msgs, err := tenantSession.GetMessages()
	if err == nil {
		userCount := 0
		assistantCount := 0
		toolCount := 0
		for _, m := range msgs {
			switch m.Role {
			case "user":
				userCount++
			case "assistant":
				assistantCount++
			case "tool":
				toolCount++
			}
		}
		fmt.Fprintf(&sb, "| 消息总数 | %d |\n", len(msgs))
		fmt.Fprintf(&sb, "| 用户消息 | %d |\n", userCount)
		fmt.Fprintf(&sb, "| 助手消息 | %d |\n", assistantCount)
		fmt.Fprintf(&sb, "| 工具消息 | %d |\n", toolCount)
	}

	// Token usage + stream timing stats
	if tenantSession != nil {
		if memSvc := tenantSession.MemoryService(); memSvc != nil {
			if pt, ct, err := memSvc.GetTokenState(ctx, tenantSession.TenantID()); err == nil && pt > 0 {
				fmt.Fprintf(&sb, "| Prompt Tokens | %s |\n", formatTokenCount(pt))
				fmt.Fprintf(&sb, "| Completion Tokens | %s |\n", formatTokenCount(ct))
			}
		}
	}
	// Stream timing stats from the most recent LLM call (persists across turns)
	progressKey := msg.Channel + ":" + msg.ChatID
	if v, ok := a.lastStreamStats.Load(progressKey); ok {
		if stats, ok := v.(*protocol.StreamStats); ok && stats != nil {
			fmt.Fprintf(&sb, "| TTFT | %d ms |\n", stats.TTFTMs)
			if stats.TPOTMs > 0 {
				fmt.Fprintf(&sb, "| TPOT | %d ms |\n", stats.TPOTMs)
			}
			if stats.TokensPerSec > 0 {
				fmt.Fprintf(&sb, "| Gen Speed | %d tok/s |\n", stats.TokensPerSec)
			}
			if stats.SSEIntervalMs > 0 {
				fmt.Fprintf(&sb, "| SSE Interval | %d ms |\n", stats.SSEIntervalMs)
			}
			fmt.Fprintf(&sb, "| Stream Duration | %d ms |\n", stats.TotalMs)
			fmt.Fprintf(&sb, "| Output Chunks | %d |\n", stats.Chunks)
		}
	}

	// Sandbox mode
	if a.sandboxMode != "" {
		fmt.Fprintf(&sb, "| Sandbox | %s |\n", a.sandboxMode)
	}
	// 运行环境
	fmt.Fprintf(&sb, "| 版本 | %s |\n", version.Version)

	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: sb.String(),
	}, nil
}

// handleExportSession handles /export command: exports current session in the specified format.
// Formats: native (default), openai, codex
func (a *Agent) handleExportSession(ctx context.Context, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	content := strings.TrimSpace(msg.Content)
	format := "native"
	if strings.HasPrefix(strings.ToLower(content), "/export ") {
		format = strings.TrimSpace(strings.TrimPrefix(content, "/export "))
		format = strings.ToLower(format)
	}
	switch format {
	case "native", "openai", "codex":
		// valid
	case "":
		format = "native"
	default:
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "无效的导出格式。可选: native, openai, codex\n\n用法:\n- `/export` — xbot 原生格式 (JSON)\n- `/export openai` — OpenAI Chat Completions 请求格式\n- `/export codex` — Codex JSONL 格式",
		}, nil
	}

	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("获取会话失败: %v", err),
		}, nil
	}

	msgs, err := tenantSession.GetMessages()
	if err != nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("获取消息失败: %v", err),
		}, nil
	}

	// Resolve model
	uc := UserContextFromContext(ctx)
	_, model, _, _, _ := uc.ResolveLLM(msg.ChatID)

	session, err := protocol.ExportSession(msg.ChatID, model, msgs)
	if err != nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("导出失败: %v", err),
		}, nil
	}

	// Complete append-only history → Records
	if a.multiSession != nil {
		if db := a.multiSession.DB(); db != nil {
			if tenantID := tenantSession.TenantID(); tenantID > 0 {
				if records, err := sqlite.NewSessionService(db).GetFullHistory(tenantID); err == nil {
					session.Records = make([]protocol.ExportedRecord, 0, len(records))
					for _, r := range records {
						session.Records = append(session.Records, historyRecordToExported(r))
					}
				}
			}
		}
	}

	var exported []byte
	switch format {
	case "openai":
		// Construct an OpenAI Chat Completions request body
		var messages []map[string]interface{}
		if session.SystemInstructions != "" {
			messages = append(messages, map[string]interface{}{
				"role":    "system",
				"content": session.SystemInstructions,
			})
		}
		for _, m := range session.Messages {
			entry := map[string]interface{}{
				"role":    m.Role,
				"content": m.ContentToString(),
			}
			if len(m.ToolCalls) > 0 {
				calls := make([]map[string]interface{}, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					calls = append(calls, map[string]interface{}{
						"id":   tc.ID,
						"type": tc.Type,
						"function": map[string]interface{}{
							"name":      tc.Function.Name,
							"arguments": tc.Function.Arguments,
						},
					})
				}
				entry["tool_calls"] = calls
			}
			if m.ToolCallID != "" {
				entry["tool_call_id"] = m.ToolCallID
			}
			if m.Name != "" {
				entry["name"] = m.Name
			}
			messages = append(messages, entry)
		}
		body := map[string]interface{}{
			"model":    model,
			"messages": messages,
		}
		exported, _ = json.MarshalIndent(body, "", "  ")

	case "codex":
		// Codex JSONL: one JSON object per line
		var lines []string
		if session.SystemInstructions != "" {
			line, _ := json.Marshal(map[string]interface{}{
				"type": "message",
				"role": "system",
				"content": []map[string]interface{}{
					{"type": "input_text", "text": session.SystemInstructions},
				},
			})
			lines = append(lines, string(line))
		}
		for _, m := range session.Messages {
			text := m.ContentToString()
			contentType := "input_text"
			if m.Role == "assistant" {
				contentType = "output_text"
			}
			entry := map[string]interface{}{
				"type": "message",
				"role": m.Role,
				"content": []map[string]interface{}{
					{"type": contentType, "text": text},
				},
			}
			if m.Reasoning != "" {
				entry["reasoning"] = m.Reasoning
			}
			if len(m.ToolCalls) > 0 {
				entry["tool_calls"] = m.ToolCalls
			}
			if m.ToolCallID != "" {
				entry["tool_call_id"] = m.ToolCallID
			}
			if m.Name != "" {
				entry["name"] = m.Name
			}
			line, _ := json.Marshal(entry)
			lines = append(lines, string(line))
		}
		exported = []byte(strings.Join(lines, "\n"))

	default:
		// native: full xbot portable JSON
		exported, _ = json.MarshalIndent(session, "", "  ")
	}

	// IM channels (feishu, qq) have message length limits (~30KB for feishu).
	// Web channel has no limit (renders in browser). Truncate for IM channels.
	exportStr := string(exported)
	if msg.Channel != "web" && msg.Channel != "cli" && len(exportStr) > 28000 {
		exportStr = exportStr[:28000] + "\n\n... (内容过长已截断，请使用 Web 渠道或 /export 命令获取完整导出)"
	}

	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("```json\n%s\n```", exportStr),
		Metadata: map[string]string{
			"export_format": format,
		},
	}, nil
}

// historyRecordToExported converts a raw append-only history record
// (session_messages row) to the portable export format.
func historyRecordToExported(r sqlite.HistoryRecord) protocol.ExportedRecord {
	rec := protocol.ExportedRecord{
		HistoryID:       r.HistoryID,
		RecordType:      string(r.Type),
		TargetHistoryID: r.TargetHistoryID,
		RecordData:      r.Data,
		CreatedAt:       r.CreatedAt,
	}
	if r.Type == sqlite.HistoryRecordMessage {
		m := r.Message
		rec.Role = m.Role
		rec.Content = m.Content
		rec.ToolCallID = m.ToolCallID
		rec.ToolName = m.ToolName
		rec.ToolArguments = m.ToolArguments
		rec.Detail = m.Detail
		rec.Reasoning = m.ReasoningContent
		rec.DisplayOnly = m.DisplayOnly
		rec.TurnID = m.TurnID
		if len(m.ToolCalls) > 0 {
			if data, err := json.Marshal(m.ToolCalls); err == nil {
				rec.ToolCalls = data
			}
		}
	}
	return rec
}

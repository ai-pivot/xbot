package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	log "xbot/logger"
	"xbot/memory"
	"xbot/protocol"
	"xbot/session"
)

// handlePromptQuery 构建完整提示词并写入文件发送给用户（dryrun，不调用 LLM）
func (a *Agent) handlePromptQuery(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*channel.OutboundMsg, error) {
	// /prompt 是 concurrent 命令，ctx 未经过 processMessage 的 ResolveUserContext。
	// 此处必须自行解析，否则 UserContextFromContext(ctx) 返回 nil，
	// 下方 userCtx.PermUsers 空指针 panic → goroutine 崩溃 → 无回复。
	if UserContextFromContext(ctx) == nil {
		userCtx := a.ResolveUserContext(msg.Channel, msg.ChatID, msg.SenderID, msg.Metadata)
		ctx = WithUserContext(ctx, userCtx)
	}

	// 提取 /prompt 之后的 query 内容（先 trim 再截取，与 cmd 解析对齐）
	trimmed := strings.TrimSpace(msg.Content)
	query := strings.TrimSpace(trimmed[len("/prompt"):])
	if query == "" {
		query = "(empty query)"
	}

	// 替换 msg.Content 为 query，复用 buildPrompt
	dryMsg := msg
	dryMsg.Content = query
	messages, err := a.buildPrompt(ctx, dryMsg, tenantSession)
	if err != nil {
		return nil, err
	}

	// 获取工具定义
	sessionKey := msg.Channel + ":" + msg.ChatID
	userCtx := UserContextFromContext(ctx)
	toolDefs := visibleToolDefs(a.tools.AsDefinitionsForSession(sessionKey, tenantSession.TenantID()), userCtx.PermUsers, msg.Channel)

	// 格式化输出
	var buf strings.Builder
	buf.WriteString("=== Prompt Dry Run ===\n\n")
	for i, m := range messages {
		fmt.Fprintf(&buf, "--- [%d] role: %s ---\n", i, m.Role)
		buf.WriteString(m.Content)
		buf.WriteString("\n\n")
	}

	fmt.Fprintf(&buf, "--- Tools (%d) ---\n", len(toolDefs))
	for _, td := range toolDefs {
		fmt.Fprintf(&buf, "- %s: %s\n", td.Name(), td.Description())
		for _, p := range td.Parameters() {
			req := ""
			if p.Required {
				req = " (required)"
			}
			fmt.Fprintf(&buf, "    %s (%s)%s: %s\n", p.Name, p.Type, req, p.Description)
		}
	}

	fmt.Fprintf(&buf, "\n--- Total messages: %d ---\n", len(messages))

	// 写入宿主机可访问的绝对路径（~/.xbot/prompt-dryrun/<channel>-<chatid>.md）。
	// 不使用 sandbox workspace —— remote/docker 沙箱的 workspace 是沙箱内部路径，
	// 用户（尤其 web/CLI 会话）无法直接访问，导致"文件没生成"的错觉。
	// 使用 config.XbotHome()（$XBOT_HOME 或 ~/.xbot）而非 a.xbotHome：
	// a.xbotHome 可能为空字符串（config.json 未设置 XbotHome 时），
	// filepath.Join("", "prompt-dryrun") 会退化为相对路径写到 CWD，用户找不到。
	dryRunDir := filepath.Join(config.XbotHome(), "prompt-dryrun")
	if err := os.MkdirAll(dryRunDir, 0o755); err != nil {
		return nil, fmt.Errorf("create prompt dryrun dir: %w", err)
	}
	chatID := msg.ChatID
	if chatID == "" {
		chatID = "default"
	}
	promptFile := filepath.Join(dryRunDir, fmt.Sprintf("%s-%s.md", msg.Channel, strings.ReplaceAll(chatID, "/", "_")))

	// 尝试写入沙箱（如果有且可用），失败则回退宿主机写入。
	// 无论哪种方式，promptFile 都是宿主机绝对路径，确保用户能访问。
	writeErr := os.WriteFile(promptFile, []byte(buf.String()), 0o644)
	if writeErr != nil {
		// sandbox-aware 写入（docker/remote 场景），仍然写入宿主机路径目录
		if a.sandbox != nil {
			writeErr = a.sandbox.WriteFile(ctx, promptFile, []byte(buf.String()), 0o644, sandboxUserID(msg))
		}
	}
	if writeErr != nil {
		return nil, fmt.Errorf("write prompt file: %w", writeErr)
	}

	// 回复中直接附上工具清单摘要（方便确认工具是否注册，无需打开文件）
	var toolNames []string
	for _, td := range toolDefs {
		toolNames = append(toolNames, td.Name())
	}

	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf(
			"Prompt dry run 已写入: `%s`\n\n工具数量: %d\n工具列表: %s",
			promptFile, len(toolDefs), strings.Join(toolNames, ", "),
		),
	}, nil
}

// handleNewSession 处理 /new 命令：先归档记忆，再清空会话
func (a *Agent) handleNewSession(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*channel.OutboundMsg, error) {
	a.emitBuiltinProgress(msg.Channel, msg.ChatID, PhaseNewing)
	// Pass zero TokenUsage so the TUI context bar resets to empty.
	defer a.emitBuiltinProgressDone(msg.Channel, msg.ChatID, &protocol.TokenUsage{}, false)

	userCtx := UserContextFromContext(ctx)

	messages, err := tenantSession.GetMessages()
	if err != nil {
		return &channel.OutboundMsg{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "获取会话消息失败，请重试。",
		}, nil
	}
	lastConsolidated := tenantSession.LastConsolidated()
	mem := tenantSession.Memory()

	// 取尚未合并的消息进行归档
	snapshot := messages
	if lastConsolidated < len(messages) {
		snapshot = messages[lastConsolidated:]
	}

	if mem != nil && len(snapshot) > 0 {
		log.Ctx(ctx).WithField("tenant", tenantSession.String()).Infof("/new: archiving %d unconsolidated messages", len(snapshot))
		result, _ := mem.Memorize(ctx, memory.MemorizeInput{
			Messages:         snapshot,
			LastConsolidated: 0,
			LLMClient:        userCtx.LLMClient,
			Model:            userCtx.Model,
			ArchiveAll:       true,
		})
		if result.OK {
			GlobalMetrics.MemoryConsolidations.Add(1)
		}
		if !result.OK {
			return &channel.OutboundMsg{
				Channel: msg.Channel,
				ChatID:  msg.ChatID,
				Content: "记忆归档失败，会话未重置，请重试。",
			}, nil
		}
	}

	if err := tenantSession.Clear(); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to clear tenant session")
	}
	if err := tenantSession.SetLastConsolidated(0); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to reset last consolidated")
	}

	// 清除记忆整理状态，取消正在进行的整理任务（多路径协调）
	tenantKey := msg.Channel + ":" + msg.ChatID

	// 清理 offload 数据
	if a.offloadStore != nil {
		a.offloadStore.CleanSession(tenantKey)
	}
	// 清理 mask 数据
	if a.maskStore != nil {
		a.maskStore.Clear()
	}

	// Clear token state so the context usage bar resets on /new.
	// Without this, the next Run() would restore stale token counts from DB
	// and the CLI progress bar would show the old session's usage.
	if memSvc := tenantSession.MemoryService(); memSvc != nil {
		if err := memSvc.SetTokenState(ctx, tenantSession.TenantID(), 0, 0); err != nil {
			log.Ctx(ctx).WithError(err).WithField("tenant_id", tenantSession.TenantID()).Warn("Failed to clear token state on /new")
		}
	}

	return &channel.OutboundMsg{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: "会话已重置，记忆已归档。",
		Metadata: map[string]string{
			"session_reset": "true",
		},
	}, nil
}

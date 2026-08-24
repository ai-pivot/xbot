package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"xbot/llm"
	"xbot/tools"
)

// resolveAbsolutePath expands ~ and resolves . / .. to an absolute path.
func resolveAbsolutePath(path string) string {
	if path == "" {
		return ""
	}
	// Expand ~/...
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	} else if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			path = home
		}
	}
	// Resolve . and .. to absolute path
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return path
}

// systemReminderRe is pre-compiled for stripSystemReminder (called in hot loops).
var systemReminderRe = regexp.MustCompile(`\n?\n?<system-reminder>[\s\S]*?</system-reminder>`)

// BuildSystemReminder builds a system reminder appended to the last tool message.
// agentID "main" = main Agent, otherwise SubAgent.
// roundToolCalls is the current round's tool calls (used to detect git commit).
// sessionKey is the unique session identifier (used for worktree peer lookup).
// sessionName is the current session display name (used to detect auto-generated names needing rename).
func BuildSystemReminder(messages []llm.ChatMessage, roundToolCalls []llm.ToolCall, todoSummary string, agentID string, cwd string, sessionKey string, sessionName string, activeSubAgents []SubAgentStatus) string {
	if len(messages) == 0 {
		return ""
	}

	isSubAgent := agentID != "main"

	// 1. 提取任务目标：最后一条 user message（去掉时间戳和引导文本）
	//   - 主 Agent：用户最新需求
	//   - SubAgent：父 Agent 分配的任务命令
	// 同时记录该 user message 的位置，用于计算 toolsSinceUser。
	var taskGoal string
	lastUserIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == "user" && msg.Content != "" {
			taskGoal = extractUserGoal(msg.Content)
			if taskGoal != "" {
				lastUserIdx = i
				break
			}
		}
	}

	// 2b. 统计用户消息之后的 tool 调用数（用于区分新旧消息）
	toolsSinceUser := 0
	if lastUserIdx >= 0 {
		for i := lastUserIdx + 1; i < len(messages); i++ {
			if messages[i].Role == "tool" {
				toolsSinceUser++
			}
		}
	}

	// 4. 构建提醒 —— 严格 XML 结构化，不自然语言。
	// 注入格式全程用标签 + 值，状态用属性表达（kind/status），
	// 避免自然语言句子占上下文。
	var sb strings.Builder
	sb.WriteString("<system-reminder>")

	if taskGoal != "" {
		kind := "user_processing" // 历史需求正在处理中
		if toolsSinceUser == 0 {
			kind = "user_latest" // 用户最新需求
		}
		if isSubAgent {
			kind = "subagent"
		}
		sb.WriteString("<task>")
		fmt.Fprintf(&sb, "<kind>%s</kind>", kind)
		fmt.Fprintf(&sb, "<content>%s</content>", taskGoal)
		sb.WriteString("</task>")
	}

	if cwd != "" {
		cwd = resolveAbsolutePath(cwd)
		fmt.Fprintf(&sb, "<working-dir>%s</working-dir>", cwd)
	}

	if todoSummary != "" {
		fmt.Fprintf(&sb, "<todo>%s</todo>", todoSummary)
	}

	// Peer awareness: show who else is working in the same repo.（仅活跃 worktree + busy）
	// Only show peers with actual worktrees (physical isolation) — lightweight
	// peer-awareness registrations without worktrees do not indicate collaboration.
	// This prevents injecting misleading "3 peers collaborating" when the user
	// simply has multiple independent sessions in the same git repo.
	if !isSubAgent && sessionKey != "" {
		repoPath := ""
		if entry := tools.GlobalWorktreeRegistry.GetBySession(sessionKey); entry != nil {
			repoPath = entry.RepoPath
		}
		peers := tools.GlobalWorktreeRegistry.GetPeers(repoPath, sessionKey)
		// Filter: only show peers with actual worktrees (real collaboration)
		// AND currently BUSY — Busy means the session's chatProcessLoop is
		// processing a turn (an iteration is running), set via
		// WorktreeRegistry.SetBusy alongside ss.busy.Store (agent.go
		// chatProcessLoop). A registered session lingers in the registry for the
		// process lifetime; without the live iterating signal every peer (even
		// one idle for hours) is reported as "collaborating", falsely implying
		// concurrent work and distracting the agent (user report: "peer 已 idle
		// 仍被提示协作中"). busy/idle = 是否在迭代中 — not time-based.
		var activePeers []*tools.WorktreeEntry
		for _, p := range peers {
			if p.WorktreeDir != "" && p.Busy {
				activePeers = append(activePeers, p)
			}
		}
		if len(activePeers) > 0 {
			sb.WriteString("<peers>")
			for _, p := range activePeers {
				fmt.Fprintf(&sb, "<peer role=%q branch=%q>%s</peer>", p.Role, p.Branch, shortenPeerName(p.SessionKey))
			}
			sb.WriteString("</peers>")
		}
	}

	// Active SubAgents（当前执行中 vs 空闲）
	if !isSubAgent && len(activeSubAgents) > 0 {
		sb.WriteString("<subagents>")
		for _, sa := range activeSubAgents {
			status := "idle"
			if sa.Running {
				status = "running"
			}
			label := sa.Role
			if sa.Instance != "" {
				label += "/" + sa.Instance
			}
			fmt.Fprintf(&sb, "<subagent status=%q>%s</subagent>", status, label)
		}
		sb.WriteString("</subagents>")
	}

	// 行为准则：精简固定 3 条（去"优先编辑已有文件"；git commit 并入常驻准则）。
	sb.WriteString("<guidelines>")
	sb.WriteString("<guideline>修改后运行测试验证</guideline>")
	sb.WriteString("<guideline>错误时先分析根因再修改</guideline>")
	sb.WriteString("<guideline>主动维护知识文档和代码质量</guideline>")
	sb.WriteString("</guidelines>")

	sb.WriteString("</system-reminder>")
	return sb.String()
}

// stripSystemReminder removes the <system-reminder>...</system-reminder> block
// and any preceding blank line from a message's content.
func stripSystemReminder(content string) string {
	return systemReminderRe.ReplaceAllString(content, "")
}

// extractUserGoal 从 user message 中提取实际用户需求（去掉时间戳和系统引导文本）。
func extractUserGoal(content string) string {
	lines := strings.Split(content, "\n")
	var goalLines []string
	inGuide := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// 跳过时间戳行 [2026-03-21 23:08:51 CST]
		if len(trimmed) > 0 && trimmed[0] == '[' && strings.Contains(trimmed, "CST") {
			continue
		}
		// 跳过 [用户名] 标记行
		if len(trimmed) > 0 && trimmed[0] == '[' && strings.HasSuffix(trimmed, "]") && len(trimmed) < 50 {
			continue
		}
		// 跳过系统引导文本块
		if strings.Contains(trimmed, "[系统引导]") || strings.Contains(trimmed, "search_tools") || strings.Contains(trimmed, "WebSearch") || strings.Contains(trimmed, "Fetch") || strings.Contains(trimmed, "Skill") || strings.Contains(trimmed, "现在时间") {
			inGuide = true
			continue
		}
		// 跳过 <context> 元数据块（用户消息前注入的 context/time/sender 标签）
		// —— 否则被当作用户需求塞进 taskGoal，变成 "<context>\n<time>...</time>\n<sender>..." 乱码。
		if strings.HasPrefix(trimmed, "<context>") || strings.HasPrefix(trimmed, "</context>") ||
			strings.HasPrefix(trimmed, "<time>") || strings.HasPrefix(trimmed, "</time>") ||
			strings.HasPrefix(trimmed, "<sender>") || strings.HasPrefix(trimmed, "</sender>") {
			continue
		}
		// Skip auto-naming rename hint (injected by UserMessageMiddleware)
		if strings.Contains(trimmed, "⚠️ 当前会话名") || strings.Contains(trimmed, "config(action=\"set\", key=\"session_name\"") {
			inGuide = true
			continue
		}
		if inGuide && trimmed == "" {
			inGuide = false
			continue
		}
		if inGuide {
			continue
		}
		goalLines = append(goalLines, line)
	}
	goal := strings.TrimSpace(strings.Join(goalLines, "\n"))
	runes := []rune(goal)
	if len(runes) > 500 {
		goal = string(runes[:500]) + "..."
	}
	return goal
}

// shortenPeerName shortens a session key for display in peer list.
func shortenPeerName(sessionKey string) string {
	if idx := strings.LastIndex(sessionKey, ":"); idx > 0 {
		return sessionKey[idx+1:]
	}
	return sessionKey
}

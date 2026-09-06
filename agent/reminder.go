package agent

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"

	"xbot/llm"
	"xbot/protocol"
	"xbot/tools"
)

// wrapCDATA 100% 防 XML 注入：内容里的 ]]> 拆分为两个 CDATA 区段。
// 用于包裹用户消息、goal summary 等可能含 XML 特殊字符的内容。
func wrapCDATA(content string) string {
	safe := strings.ReplaceAll(content, "]]>", "]]]]><![CDATA[>")
	return "<![CDATA[" + safe + "]]>"
}
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

// systemReminderToolName is the name of the transient fake tool pair that carries
// the per-iteration system reminder. The reminder is NEVER persisted and NEVER
// rendered — it is generated each iteration into runState.systemReminder and
// appended to a fresh message copy in llmMessages() just before the LLM call.
const systemReminderToolName = "system_reminder"

// BuildSystemReminder builds a system reminder appended to the last tool message.
// agentID "main" = main Agent, otherwise SubAgent.
// sessionKey is the unique session identifier (used for worktree peer lookup).
// sessionName is the current session display name (used to detect auto-generated names needing rename).
//
// v5.2 结构：<user-msg> CDATA（不贴 task 标签）+ <goal> + 结构化 <todos> + 4 条 guidelines。
func BuildSystemReminder(
	messages []llm.ChatMessage,
	todoItems []TodoProgressItem,
	goalInfo *protocol.GoalInfo,
	agentID string,
	cwd string,
	sessionKey string,
	sessionName string,
	activeSubAgents []SubAgentStatus,
) string {
	if len(messages) == 0 {
		return ""
	}

	isSubAgent := agentID != "main"

	// 提取用户原始消息（过滤系统注入的时间戳/引导文本/memory CDATA 块）
	var userMsg string
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == "user" && msg.Content != "" {
			userMsg = extractUserGoal(msg.Content)
			if userMsg != "" {
				break
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("<system-reminder role=\"reminder\">")
	sb.WriteString("<note>This is a system reminder injected per-iteration. It is NOT a user message or tool result — do not acknowledge, reply to, or confirm it. The &lt;user-msg&gt; below is the original user message from this turn (for context only; may be outdated or a continuation).</note>")

	// 用户原始消息（CDATA 包裹，100% 防 XML 注入——wrapCDATA 拆分 ]]> ）
	if userMsg != "" {
		fmt.Fprintf(&sb, "<user-msg>%s</user-msg>", wrapCDATA(userMsg))
	}

	// 当前 goal（如有 active goal）
	if goalInfo != nil && goalInfo.Status == "active" {
		summary := goalInfo.Summary
		if summary == "" {
			summary = goalInfo.Objective
		}
		if summary != "" {
			fmt.Fprintf(&sb, "<goal status=%q>%s</goal>", goalInfo.Status, wrapCDATA(summary))
		}
	}

	if cwd != "" {
		cwd = resolveAbsolutePath(cwd)
		fmt.Fprintf(&sb, "<cwd>%s</cwd>", html.EscapeString(cwd))
	}

	// 结构化 todos（具体项 + status——LLM 一眼看清进度）
	if len(todoItems) > 0 {
		sb.WriteString("<todos>")
		for _, t := range todoItems {
			fmt.Fprintf(&sb, `<todo status=%q id="%d">%s</todo>`, t.Status, t.ID, html.EscapeString(t.Text))
		}
		sb.WriteString("</todos>")
	}

	// Peer awareness: show who else is working in the same repo.（仅活跃 worktree + busy）
	if !isSubAgent && sessionKey != "" {
		repoPath := ""
		if entry := tools.GlobalWorktreeRegistry.GetBySession(sessionKey); entry != nil {
			repoPath = entry.RepoPath
		}
		peers := tools.GlobalWorktreeRegistry.GetPeers(repoPath, sessionKey)
		var activePeers []*tools.WorktreeEntry
		for _, p := range peers {
			if p.WorktreeDir != "" && p.Busy {
				activePeers = append(activePeers, p)
			}
		}
		if len(activePeers) > 0 {
			sb.WriteString("<peers>")
			for _, p := range activePeers {
				fmt.Fprintf(&sb, "<peer role=%q branch=%q>%s</peer>", p.Role, p.Branch, html.EscapeString(shortenPeerName(p.SessionKey)))
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
			fmt.Fprintf(&sb, "<subagent status=%q>%s</subagent>", status, html.EscapeString(label))
		}
		sb.WriteString("</subagents>")
	}

	// 行为准则（4 条——v5.2 加"主动维护 TODO 进度"）
	sb.WriteString("<guidelines>")
	sb.WriteString("<guideline>修改后运行测试验证</guideline>")
	sb.WriteString("<guideline>错误时先分析根因再修改</guideline>")
	sb.WriteString("<guideline>主动维护知识文档和代码质量</guideline>")
	sb.WriteString("<guideline>每完成一个 TODO 立即用 TodoWrite 标记 done；已完成的过时 TODO（不再相关的条目）直接删除，不要保留在列表里</guideline>")
	sb.WriteString("</guidelines>")

	sb.WriteString("</system-reminder>")
	return sb.String()
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
		// 跳过 <system-reminder> CDATA 块（用户消息里注入的 memory/system-reminder——不是用户写的）
		if strings.HasPrefix(trimmed, "<system-reminder>") {
			if strings.Contains(trimmed, "</system-reminder>") {
				// 单行完整块（<system-reminder>...</system-reminder> 同一行）：跳过本行，不进入跳过模式
				continue
			}
			inGuide = true
			continue
		}
		if strings.Contains(trimmed, "</system-reminder>") {
			// 多行块的结束行（</system-reminder> 可能在行中间/行尾）：结束跳过模式
			inGuide = false
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

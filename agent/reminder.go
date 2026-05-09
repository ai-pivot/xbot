package agent

import (
	"fmt"
	"regexp"
	"strings"

	"xbot/llm"
	"xbot/tools"
)

// systemReminderRe is pre-compiled for stripSystemReminder (called in hot loops).
var systemReminderRe = regexp.MustCompile(`\n?\n?<system-reminder>[\s\S]*?</system-reminder>`)

// BuildSystemReminder builds a system reminder appended to the last tool message.
// agentID "main" = main Agent, otherwise SubAgent.
// roundToolCalls is the current round's tool calls (used to detect git commit).
// sessionKey is the unique session identifier (used for worktree peer lookup).
func BuildSystemReminder(messages []llm.ChatMessage, roundToolCalls []llm.ToolCall, todoSummary string, agentID string, cwd string, sessionKey string) string {
	if len(messages) == 0 {
		return ""
	}

	isSubAgent := agentID != "main"

	// 1. 提取任务目标：最后一条 user message（去掉时间戳和引导文本）
	//   - 主 Agent：用户最新需求
	//   - SubAgent：父 Agent 分配的任务命令
	var taskGoal string
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == "user" && msg.Content != "" {
			taskGoal = extractUserGoal(msg.Content)
			if taskGoal != "" {
				break
			}
		}
	}

	// 2. 统计 tool message 总数作为进度指标
	toolCount := 0
	for _, msg := range messages {
		if msg.Role == "tool" {
			toolCount++
		}
	}

	// 3. Collect round tool names for display
	var roundToolNames []string
	for _, tc := range roundToolCalls {
		roundToolNames = append(roundToolNames, tc.Name)
	}

	// 4. 构建提醒
	var parts []string

	if taskGoal != "" {
		if isSubAgent {
			parts = append(parts, fmt.Sprintf("执行任务: %s", taskGoal))
		} else {
			parts = append(parts, fmt.Sprintf("用户需求: %s", taskGoal))
		}
	}

	if cwd != "" {
		parts = append(parts, fmt.Sprintf("📂 默认工作目录: %s（你的 Shell 命令默认在此目录执行，Cd 后生效）", cwd))
	}

	parts = append(parts, fmt.Sprintf("已完成 %d 次工具调用", toolCount))
	parts = append(parts, fmt.Sprintf("本轮使用: %s", strings.Join(roundToolNames, ", ")))

	if todoSummary != "" {
		parts = append(parts, fmt.Sprintf("TODO: %s", todoSummary))
	}

	// Worktree/peer awareness: always show peer list and collaboration rules
	// when other agents are working in the same repo (even without worktree).
	if !isSubAgent && sessionKey != "" {
		// Find repo path from session key
		repoPath := ""
		if entry := tools.GlobalWorktreeRegistry.GetBySession(sessionKey); entry != nil {
			repoPath = entry.RepoPath
			if entry.WorktreeDir != "" {
				parts = append(parts, "")
				parts = append(parts, fmt.Sprintf("⚠️ Worktree 隔离模式 (分支: %s)", entry.Branch))
				parts = append(parts, fmt.Sprintf("   工作区: %s", entry.WorktreeDir))
				parts = append(parts, "   你的改动与主工作区隔离，其他 agent 看不到")
				parts = append(parts, fmt.Sprintf("   完成后请主动询问用户：合并回主工作区（%s）还是继续留在 worktree", entry.RepoPath))
			}
		}

		// Always show peers if any (including shared sessions without worktree)
		peers := tools.GlobalWorktreeRegistry.GetPeers(repoPath, sessionKey)
		if len(peers) > 0 {
			parts = append(parts, "")
			parts = append(parts, fmt.Sprintf("👥 协作中: %d 个同伴在此仓库工作", len(peers)))
			for _, p := range peers {
				parts = append(parts, fmt.Sprintf("   - %s (角色: %s, 分支: %s)", shortenPeerName(p.SessionKey), p.Role, p.Branch))
			}
			parts = append(parts, "协作规则: 尊重同伴的修改，改动冲突时优先通过 SendMessage 协商。")
			parts = append(parts, "如果因同伴正在修改相关代码而无法验证，可 SendMessage 将验证任务委托给同伴。")
		}
	}

	parts = append(parts, "行为提醒:")
	parts = append(parts, "- 优先编辑已有文件，避免创建新文件")
	parts = append(parts, "- 修改后运行测试验证")
	parts = append(parts, "- 错误时先分析根因再修改")

	// Detect git commit in Shell tool calls — remind agent to activate post-dev skill
	gitCommitDetected := false
	for _, tc := range roundToolCalls {
		if tc.Name == "Shell" && strings.Contains(tc.Arguments, "git commit") {
			gitCommitDetected = true
			break
		}
	}
	if gitCommitDetected {
		parts = append(parts, "- 检测到 git commit，立即激活 post-dev skill 更新项目文档")
	}

	return "<system-reminder>\n" + strings.Join(parts, "\n") + "\n</system-reminder>"
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

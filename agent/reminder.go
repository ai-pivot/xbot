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
func BuildSystemReminder(messages []llm.ChatMessage, roundToolCalls []llm.ToolCall, todoSummary string, agentID string, cwd string, sessionKey string, sessionName string) string {
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

	// 2. 统计 tool message 总数作为进度指标
	toolCount := 0
	for _, msg := range messages {
		if msg.Role == "tool" {
			toolCount++
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
		} else if toolsSinceUser == 0 {
			// 用户刚说的——这是当前轮的第一个工具调用
			parts = append(parts, fmt.Sprintf("用户最新需求: %s", taskGoal))
		} else {
			// 用户之前说的——明确标注这不是新消息
			parts = append(parts, fmt.Sprintf("用户原始需求（正在处理中，已执行 %d 次工具调用）: %s", toolsSinceUser, taskGoal))
		}
	}

	if cwd != "" {
		// Always resolve to absolute path — never show ~ or . in cwd.
		cwd = resolveAbsolutePath(cwd)
		parts = append(parts, fmt.Sprintf("📂 默认工作目录: %s（你的 Shell 命令默认在此目录执行，Cd 后生效）", cwd))
	}

	parts = append(parts, fmt.Sprintf("已完成 %d 次工具调用", toolCount))
	parts = append(parts, fmt.Sprintf("本轮使用: %s", strings.Join(roundToolNames, ", ")))

	if todoSummary != "" {
		parts = append(parts, fmt.Sprintf("TODO: %s", todoSummary))
	}

	// Peer awareness: show who else is working in the same repo.
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
		// Filter: only show peers with actual worktrees (real collaboration).
		var activePeers []*tools.WorktreeEntry
		for _, p := range peers {
			if p.WorktreeDir != "" {
				activePeers = append(activePeers, p)
			}
		}
		if len(activePeers) > 0 {
			parts = append(parts, "")
			parts = append(parts, fmt.Sprintf("👥 协作中: %d 个同伴在此仓库工作", len(activePeers)))
			for _, p := range activePeers {
				parts = append(parts, fmt.Sprintf("   - %s (角色: %s, 分支: %s)", shortenPeerName(p.SessionKey), p.Role, p.Branch))
			}
			parts = append(parts, "协作规则: 尊重同伴的修改，改动冲突时优先通过 SendMessage 协商。")
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

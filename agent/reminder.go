package agent

import (
	"fmt"
	"regexp"
	"strings"
	"xbot/llm"
)

// systemReminderRe is pre-compiled for stripSystemReminder (called in hot loops).
var systemReminderRe = regexp.MustCompile(`\n?\n?<system-reminder>[\s\S]*?</system-reminder>`)

// BuildSystemReminder builds a system reminder appended to the last tool message.
// agentID "main" = main Agent, otherwise SubAgent.
// roundToolCalls is the current round's tool calls (used to detect git commit).
func BuildSystemReminder(messages []llm.ChatMessage, roundToolCalls []llm.ToolCall, todoSummary string, agentID string, cwd string) string {
	if len(messages) == 0 {
		return ""
	}

	isSubAgent := agentID != "main"

	// 1. Extract task goal: last user message (remove timestamps and guide text)
	//   - Main Agent: user's latest requirement
	//   - SubAgent: task command assigned by parent Agent
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

	// 2. Count total tool messages as progress indicator
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

	// 4. Build reminder
	var parts []string

	if taskGoal != "" {
		if isSubAgent {
			parts = append(parts, fmt.Sprintf("Executing task:: %s", taskGoal))
		} else {
			parts = append(parts, fmt.Sprintf("User requirement:: %s", taskGoal))
		}
	}

	if cwd != "" {
		parts = append(parts, fmt.Sprintf("Current directory:: %s", cwd))
	}

	parts = append(parts, fmt.Sprintf("Completed %d tool calls", toolCount))
	parts = append(parts, fmt.Sprintf("This round used:: %s", strings.Join(roundToolNames, ", ")))

	if todoSummary != "" {
		parts = append(parts, fmt.Sprintf("TODO: %s", todoSummary))
	}

	parts = append(parts, "Behavior reminders:")
	parts = append(parts, "- Prefer editing existing files, avoid creating new files")
	parts = append(parts, "- Run tests to verify after modifications")
	parts = append(parts, "- Analyze root cause first when encountering errors")

	// Detect git commit in Shell tool calls — remind agent to activate post-dev skill
	gitCommitDetected := false
	for _, tc := range roundToolCalls {
		if tc.Name == "Shell" && strings.Contains(tc.Arguments, "git commit") {
			gitCommitDetected = true
			break
		}
	}
	if gitCommitDetected {
		parts = append(parts, "- Detected git commit, immediately activate post-dev skill to update project docs")
	}

	return "<system-reminder>\n" + strings.Join(parts, "\n") + "\n</system-reminder>"
}

// stripSystemReminder removes the <system-reminder>...</system-reminder> block
// and any preceding blank line from a message's content.
func stripSystemReminder(content string) string {
	return systemReminderRe.ReplaceAllString(content, "")
}

// extractUserGoal extracts the actual user requirement from the user message (strips timestamps and system guide text).
func extractUserGoal(content string) string {
	lines := strings.Split(content, "\n")
	var goalLines []string
	inGuide := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Skip timestamp line [2026-03-21 23:08:51 CST]
		if len(trimmed) > 0 && trimmed[0] == '[' && strings.Contains(trimmed, "CST") {
			continue
		}
		// Skip [username] marker lines
		if len(trimmed) > 0 && trimmed[0] == '[' && strings.HasSuffix(trimmed, "]") && len(trimmed) < 50 {
			continue
		}
		// Skip system guide text blocks
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

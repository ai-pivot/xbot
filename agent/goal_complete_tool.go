package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"xbot/llm"
	"xbot/tools"
)

// setGoalCompleteTool lets the agent mark its current goal as complete.
// Injected as a core tool, it is only available when the session has an
// active goal — the agent calls it with a summary, and the PreTurnEnd
// handler sees the completed status and stops injecting continuation prompts.
type setGoalCompleteTool struct {
	manager *GoalManager
}

func (t *setGoalCompleteTool) Name() string { return "set_goal_complete" }

func (t *setGoalCompleteTool) Description() string {
	return `Mark the current goal as completed. Call this tool when you have finished all work
for the active goal. Provide a summary of what was accomplished.

The goal system uses this signal to stop auto-continuation. Without calling this tool,
the agent loop will keep requesting continuation even after the work is done.

Parameters (JSON):
  - summary: string (required) — what was accomplished`
}

func (t *setGoalCompleteTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "summary",
			Type:        "string",
			Description: "Summary of what was accomplished for the goal",
			Required:    true,
		},
	}
}

type setGoalCompleteArgs struct {
	Summary string `json:"summary"`
}

func (t *setGoalCompleteTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var a setGoalCompleteArgs
	if err := json.Unmarshal([]byte(input), &a); err != nil {
		return nil, fmt.Errorf("set_goal_complete: %w", err)
	}

	if strings.TrimSpace(a.Summary) == "" {
		return nil, fmt.Errorf("set_goal_complete: summary must not be empty")
	}

	sessionKey := ctx.Channel + ":" + ctx.ChatID
	t.manager.Complete(sessionKey, a.Summary)

	return tools.NewResultWithTips(
		fmt.Sprintf("✅ 目标已完成: %s", a.Summary),
		"目标已标记为完成。你现在可以给用户提供最终总结。",
	), nil
}

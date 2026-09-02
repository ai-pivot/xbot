package tools

import (
	"fmt"

	"xbot/llm"
)

// CompactContextTool lets the agent trigger a context compression itself
// (agent-initiated compaction — Codex CLI parity where the model observes
// "Context automatically compacted" in-session). Registered ONLY when config
// agent.allow_self_compact is enabled (default off: compaction is otherwise
// threshold-driven via maybeCompress).
//
// The tool does not compress inline — it sets ToolResult.CompactRequested and
// the engine performs the compression before the next LLM call (same
// runCompression path as the threshold trigger: verbatim cache-hit request,
// async Pre/Post memory hooks, all safety checks). This keeps the tool
// execution instant and the compression at the canonical iteration boundary
// (the tool result and the compaction summary both land in the same
// iteration's context window, so the model sees its own request resolved).
type CompactContextTool struct{}

func (t *CompactContextTool) Name() string { return "compact_context" }

func (t *CompactContextTool) Description() string {
	return `Trigger a context compression NOW. The conversation history (except the most recent messages and the system prompt) is replaced by a structured handover summary — a continuation document written so that you can seamlessly continue working from it.

This tool does NOT directly clear, reset, or otherwise affect environment state (files, processes, session data) — it only requests the compression, which runs before your next action. Your running tools and work products are untouched.

When to call this:
- The context is getting large (you notice long accumulated tool outputs, many completed subtasks, or you can feel earlier details becoming stale)
- A major task phase just completed and the earlier context is no longer needed in full
- You are about to start a long new subtask and want a clean, focused context

When NOT to call:
- The user is mid-question or a tool just returned something you still need
- You need full-fidelity history (e.g. re-verifying an earlier exact output) — offload markers in the summary can recall original data
- The context is still small (compression discards detail for brevity)

After the compression you will see the handover summary plus the most recent messages. Call this at most once per task phase.`
}

func (t *CompactContextTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "reason",
			Type:        "string",
			Description: "Briefly why you are compressing now (e.g. 'phase 1 complete, starting long refactor'). Recorded in the progress log.",
		},
	}
}

func (t *CompactContextTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	args, err := parseToolArgs[struct {
		Reason string `json:"reason"`
	}](input)
	if err != nil {
		return nil, fmt.Errorf("parse compact_context args: %w", err)
	}

	summary := "Context compression requested. The compression runs before the next model call; you will see a [Compacted context] handover summary plus the most recent messages. Continue working from it — do not re-ask the user anything covered by the summary."
	if args.Reason != "" {
		summary = fmt.Sprintf("Context compression requested (reason: %s). The compression runs before the next model call; you will see a [Compacted context] handover summary plus the most recent messages. Continue working from it — do not re-ask the user anything covered by the summary.", args.Reason)
	}
	return &ToolResult{
		Summary:          summary,
		CompactRequested: true,
	}, nil
}

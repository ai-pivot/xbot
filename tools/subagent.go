package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"xbot/llm"
)

// InteractiveSubAgentManager extends SubAgentManager with interactive mode support.
// the Agent type in the agent package implements this interface (nil means interactive not supported).
type InteractiveSubAgentManager interface {
	SubAgentManager
	// SpawnInteractive creates/reuses an interactive SubAgent session and executes a task.
	// When instance is empty, behavior is consistent with the old version; setting instance allows the same role to create multiple independent sessions.
	// model is an optional model override; when empty, inherits the main Agent's model。
	SpawnInteractive(ctx *ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps SubAgentCapabilities, instance, model string) (string, error)
	// SendInteractive sends a message to an existing interactive session.
	SendInteractive(ctx *ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps SubAgentCapabilities, instance, model string) (string, error)
	// UnloadInteractive unloads an interactive session (consolidates memory + cleanup).
	UnloadInteractive(ctx *ToolContext, roleName, instance string) error
	// InspectInteractive returns a recent activity summary for an interactive session (tail style).
	InspectInteractive(ctx *ToolContext, roleName, instance string, tailCount int) (string, error)
	// InterruptInteractive interrupts the current iteration of an interactive session.
	InterruptInteractive(ctx *ToolContext, roleName, instance string) error
}

type SubAgentTool struct{}

func (t *SubAgentTool) Name() string {
	return "SubAgent"
}

func (t *SubAgentTool) Description() string {
	return `Delegate work to a sub-agent with a predefined role.
The sub-agent runs independently with its own tool set and context, specialized for that role.

IMPORTANT:
- instance is REQUIRED for every SubAgent call, including one-shot mode.
- Always provide a stable, explicit instance string such as "review-1", "planner-main", or "fix-login-bug".
- If you omit instance, the tool call will fail.

## Model Tier

SubAgents default to the "balance" model tier. Use model_tier to override:
- "vanguard" — strongest model, for complex reasoning tasks
- "swift" — fast/small model, for simple exploration or formatting tasks
- "balance" (default) — balanced model for general tasks

The agent role definition may also specify a model via frontmatter (model: vanguard/swift/balance).
model_tier parameter takes priority over the role's model setting. If neither is set, defaults to "balance".

## One-shot mode (default)
SubAgent(task, role, instance="...") — runs once in the foreground and returns the final result.

## Interactive mode
Persistent multi-turn session. Create once, send multiple messages, unload when done.

| Call | Behavior |
|------|----------|
| SubAgent(task, role, instance="...", interactive=true) | Create or reuse an interactive session |
| SubAgent(task, role, instance="...", action="send") | Send a new user message to an existing interactive session |
| SubAgent(task, role, instance="...", action="unload") | End the interactive session and consolidate memory |
| SubAgent(task, role, instance="...", interactive=true, background=true) | Start an interactive sub-agent in background mode |
| SubAgent(task, role, instance="...", action="inspect") | Inspect recent progress/state of a sub-agent |
| SubAgent(task, role, instance="...", action="interrupt") | Interrupt the current iteration of an interactive sub-agent |

## Background rule
Only interactive sub-agents may run in background mode.
Prefer foreground execution by default. Use background=true only when the sub-agent truly needs to keep running while the caller does other work; using background=true and then immediately waiting/sleeping for the result is effectively the same as foreground mode, just with more complexity.

Parameters (JSON):
  - task: string (required except some control actions), the task or message for the sub-agent
  - role: string (required), predefined role name
  - instance: string (REQUIRED on every call), unique instance ID used to identify the session/run
  - interactive: boolean (optional), create or reuse an interactive session
  - background: boolean (optional), only valid when interactive=true; prefer false unless there is a concrete need to let the caller continue doing other work before checking back later
  - action: string (optional), one of "send", "unload", "inspect", "interrupt"
  - model_tier: string (optional), model tier for this call: "vanguard", "swift", or "balance" (default). Overrides the role's model setting.

Available roles are listed in the <available_agents> section of the system prompt.`
}

func (t *SubAgentTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "task", Type: "string", Description: "Task or message for the sub-agent. Required for normal execution and action=\"send\"."},
		{Name: "role", Type: "string", Description: "Predefined role name (for example: code-reviewer)", Required: true},
		{Name: "instance", Type: "string", Description: `REQUIRED on every call. Stable unique ID for this sub-agent run/session. Never omit it. Examples: "review-1", "planner-main", "bugfix-login".`, Required: true},
		{Name: "interactive", Type: "boolean", Description: "Create or reuse an interactive session for multi-turn conversation"},
		{Name: "background", Type: "boolean", Description: "Run the interactive sub-agent in background mode. Only valid when interactive=true. Prefer foreground by default; use this only when the caller genuinely needs to continue other work and check back later."},
		{Name: "action", Type: "string", Description: `Optional control action: "send", "unload", "inspect", or "interrupt".`},
		{Name: "tail", Type: "integer", Description: "For action=\"inspect\": number of recent iterations to show (default: 5)."},
		{Name: "model_tier", Type: "string", Description: `Model tier for this call: "vanguard" (strongest), "swift" (fastest), or "balance" (default). Overrides the role's model setting. Use when you need a different model than the role's default for a specific task.`},
	}
}

func (t *SubAgentTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var params struct {
		Task        string `json:"task"`
		Role        string `json:"role"`
		Interactive bool   `json:"interactive"`
		Background  bool   `json:"background"`
		Action      string `json:"action"`
		Instance    string `json:"instance"`
		Tail        int    `json:"tail"`
		ModelTier   string `json:"model_tier"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	requiresTask := params.Action == "" || params.Action == "send"
	if requiresTask && params.Task == "" {
		return nil, fmt.Errorf("task is required")
	}

	const maxTaskLength = 50 * 1024 // 50KB
	if len(params.Task) > maxTaskLength {
		return nil, fmt.Errorf("task parameter exceeds maximum allowed size (%d bytes)", maxTaskLength)
	}

	if params.Role == "" {
		return nil, fmt.Errorf("role is required, see <available_agents> in system prompt")
	}

	if params.Instance == "" {
		return nil, fmt.Errorf("instance is required — provide a unique ID (e.g. \"task-1\") to identify this session. Use different instance values to run multiple sub-agents of the same role in parallel")
	}

	// Check if ctx is nil to avoid panic on subsequent access
	if ctx == nil {
		return nil, fmt.Errorf("tool context is required")
	}

	// Ensure global agents are synced to workspace
	EnsureSynced(ctx)

	originUserID := ctx.OriginUserID
	if originUserID == "" {
		originUserID = ctx.SenderID // fallback：compatible with old data
	}

	var userAgentDirs []string
	var roleSb Sandbox
	var roleUserID string
	if shouldUseSandbox(ctx) {
		roleSb = ctx.Sandbox
		roleUserID = ctx.OriginUserID
		if roleUserID == "" {
			roleUserID = ctx.SenderID
		}
		// Remote sandbox: agents were synced to runner's workspace/agents/ by syncToRunner.
		// Use runner workspace paths instead of server-local paths.
		if sbDir := sandboxBaseDir(ctx); sbDir != "" {
			userAgentDirs = append(userAgentDirs, filepath.Join(sbDir, "agents"))
		}
	} else {
		// Local / docker mode: use server-local paths
		if originUserID != "" && ctx.WorkingDir != "" {
			userAgentDirs = append(userAgentDirs, UserAgentsRoot(ctx.WorkingDir, originUserID))
		}
		if ctx.WorkspaceRoot != "" {
			userAgentDirs = append(userAgentDirs, filepath.Join(ctx.WorkspaceRoot, ".agents"))
		}
	}
	role, ok := GetSubAgentRoleSandbox(ctx.Ctx, params.Role, roleSb, roleUserID, userAgentDirs...)
	if !ok {
		return nil, fmt.Errorf("unknown role: %s, see <available_agents> in system prompt", params.Role)
	}

	// Resolve model: model_tier param > role.Model > "balance" (default)
	effectiveModel := role.Model
	if params.ModelTier != "" {
		effectiveModel = params.ModelTier
	}
	if effectiveModel == "" {
		effectiveModel = "balance"
	}

	if ctx.Manager == nil {
		return nil, fmt.Errorf("sub-agent capability not available")
	}

	// Interactive mode handling
	if params.Interactive || params.Action != "" {
		im, ok := ctx.Manager.(InteractiveSubAgentManager)
		if !ok {
			return nil, fmt.Errorf("interactive mode not supported by current agent")
		}

		switch params.Action {
		case "unload":
			if err := im.UnloadInteractive(ctx, params.Role, params.Instance); err != nil {
				return nil, err
			}
			// Unregister AgentChannel from Dispatcher
			agentChName := "agent:" + params.Role + "/" + params.Instance
			if ctx.UnregisterAgentChannel != nil {
				ctx.UnregisterAgentChannel(agentChName)
			}
			return NewResult(fmt.Sprintf("Interactive session for role %q unloaded successfully.", params.Role)), nil

		case "send":
			if params.Task == "" {
				return nil, fmt.Errorf("task is required for action=\"send\"")
			}
			result, err := im.SendInteractive(ctx, params.Task, params.Role, role.SystemPrompt, role.AllowedTools, role.Capabilities, params.Instance, effectiveModel)
			if err != nil {
				return nil, fmt.Errorf("interactive send failed: %w", err)
			}
			return NewResult(result), nil

		case "inspect":
			tailCount := params.Tail
			if tailCount <= 0 {
				tailCount = 5
			}
			result, err := im.InspectInteractive(ctx, params.Role, params.Instance, tailCount)
			if err != nil {
				return nil, fmt.Errorf("inspect failed: %w", err)
			}
			return NewResult(result), nil

		case "interrupt":
			if err := im.InterruptInteractive(ctx, params.Role, params.Instance); err != nil {
				return nil, err
			}
			return NewResult(fmt.Sprintf("Interactive session for role %q (instance=%q) interrupted.", params.Role, params.Instance)), nil

		default:
			// Propagate background flag via ToolContext metadata
			if params.Background {
				if ctx.Metadata == nil {
					ctx.Metadata = make(map[string]string)
				}
				ctx.Metadata["background"] = "true"
			}
			// action="" + interactive=true → spawn/reuse
			result, err := im.SpawnInteractive(ctx, params.Task, params.Role, role.SystemPrompt, role.AllowedTools, role.Capabilities, params.Instance, effectiveModel)
			if err != nil {
				return nil, fmt.Errorf("interactive spawn failed: %w", err)
			}
			// Register AgentChannel in Dispatcher so SendMessage(agent://) can route to it
			agentChName := "agent:" + params.Role + "/" + params.Instance
			if ctx.RegisterAgentChannel != nil {
				sendFn := func(sendCtx context.Context, task string) (string, error) {
					// Replace ctx.Ctx with the AgentChannel's long-lived context.
					// The original ctx.Ctx is cancelled when the tool returns,
					// but sendFn may be called much later via SendMessage.
					oldCtx := ctx.Ctx
					ctx.Ctx = sendCtx
					defer func() { ctx.Ctx = oldCtx }()
					return im.SendInteractive(ctx, task, params.Role, role.SystemPrompt, role.AllowedTools, role.Capabilities, params.Instance, effectiveModel)
				}
				if regErr := ctx.RegisterAgentChannel(agentChName, sendFn); regErr != nil {
					// Non-fatal: SubAgent works, but SendMessage routing won't work
					result += fmt.Sprintf("\n\nWarning: AgentChannel registration failed: %v", regErr)
				}
			}
			return NewResult(result), nil
		}
	}

	if params.Background {
		return nil, fmt.Errorf("background mode is only supported for interactive sub-agents")
	}

	// Default: one-shot mode
	result, err := ctx.Manager.RunSubAgent(ctx, params.Task, role.SystemPrompt, role.AllowedTools, role.Capabilities, params.Role, effectiveModel)
	if err != nil {
		return nil, fmt.Errorf("sub-agent failed: %w", err)
	}

	return NewResult(result), nil
}

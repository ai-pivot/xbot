package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"xbot/llm"
)

// InteractiveSubAgentManager 扩展 SubAgentManager，支持 interactive mode。
// agent 包的 Agent 实现此接口（如果是 nil 则不支持 interactive）。
type InteractiveSubAgentManager interface {
	SubAgentManager
	// SpawnInteractive 创建/复用 interactive SubAgent session 并执行任务。
	// instance 为空时行为与旧版一致；设置 instance 后同一 role 可创建多个独立 session。
	// model 为可选的模型覆盖，为空时继承主 Agent 模型。
	SpawnInteractive(ctx *ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps SubAgentCapabilities, instance, model string) (string, error)
	// SendInteractive 向已有的 interactive session 发送消息。
	SendInteractive(ctx *ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps SubAgentCapabilities, instance, model string) (string, error)
	// UnloadInteractive 结束 interactive session（巩固记忆 + 清理）。
	UnloadInteractive(ctx *ToolContext, roleName, instance string) error
	// InspectInteractive 返回 interactive session 的最近活动摘要（tail 风格）。
	InspectInteractive(ctx *ToolContext, roleName, instance string, tailCount int) (string, error)
	// InterruptInteractive 中断 interactive session 当前正在执行的迭代。
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

## Background by default

Sub-agents run in BACKGROUND by default: spawn returns immediately with a task ID,
the sub-agent works asynchronously, and the result is injected into your conversation
when it finishes. Use task_wait(task_id=...) to block until it completes, task_status
to check progress, or action="inspect" to see its latest activity. Set background=false
only when you must block synchronously and get the final reply directly.

## One-shot mode (default)
SubAgent(task, role, instance="...") — runs once (background by default; set background=false to block for the result).

## Interactive mode
Persistent multi-turn session. Create once, send multiple messages, unload when done.

| Call | Behavior |
|------|----------|
| SubAgent(task, role, instance="...", interactive=true) | Create or reuse an interactive session (background by default) |
| SubAgent(task, role, instance="...", action="send") | Send a new user message to an existing interactive session |
| SubAgent(task, role, instance="...", action="unload") | End the interactive session and consolidate memory |
| SubAgent(task, role, instance="...", action="inspect") | Inspect recent progress/state of a sub-agent |
| SubAgent(task, role, instance="...", action="interrupt") | Interrupt the current iteration of an interactive sub-agent |

## Background rule
Background sub-agents report progress automatically; when one finishes, the result is injected into your
conversation and you can also await it with task_wait(task_id=...). Use action="inspect"
to check progress, action="send" to send messages, action="interrupt" to stop,
action="unload" to terminate.

Parameters (JSON):
  - task: string (required except some control actions), the task or message for the sub-agent
  - role: string (required), predefined role name
  - instance: string (REQUIRED on every call), unique instance ID used to identify the session/run
  - interactive: boolean (optional), create or reuse an interactive session
  - background: boolean (optional), defaults to true — spawn returns immediately and the result is injected when done (await with task_wait). Set false to block for the final reply synchronously.
  - action: string (optional), one of "send", "unload", "inspect", "interrupt"
  - model_tier: string (optional), model tier for this call: "vanguard", "swift", or "balance" (default). Overrides the role's model setting.

Available roles are listed in the <available_agents> section of the system prompt.

For TUI sidebar session management and layout adjustments, use search_tools to load tui_control. For configuration changes, load config.`
}

func (t *SubAgentTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "task", Type: "string", Description: "Task or message for the sub-agent. Required for normal execution and action=\"send\"."},
		{Name: "role", Type: "string", Description: "Predefined role name (for example: code-reviewer)", Required: true},
		{Name: "instance", Type: "string", Description: `REQUIRED on every call. Stable unique ID for this sub-agent run/session. Never omit it. Examples: "review-1", "planner-main", "bugfix-login".`, Required: true},
		{Name: "interactive", Type: "boolean", Description: "Create or reuse an interactive session for multi-turn conversation"},
		{Name: "background", Type: "boolean", Description: "Run the sub-agent in background mode (default: true — spawn returns immediately, completion is injected as a notification; await with task_wait). Set false to block synchronously for the final reply."},
		{Name: "action", Type: "string", Description: `Optional control action: "send", "unload", "inspect", or "interrupt".`},
		{Name: "tail", Type: "integer", Description: "For action=\"inspect\": number of recent iterations to show (default: 5)."},
		{Name: "model_tier", Type: "string", Description: `Model tier for this call: "vanguard" (strongest), "swift" (fastest), or "balance" (default). Overrides the role's model setting. Use when you need a different model than the role's default for a specific task.`},
	}
}

func (t *SubAgentTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Task        string `json:"task"`
		Role        string `json:"role"`
		Interactive bool   `json:"interactive"`
		// *bool so "background" defaults to TRUE (background) unless the LLM
		// explicitly passes background=false to block synchronously.
		Background *bool  `json:"background"`
		Action     string `json:"action"`
		Instance   string `json:"instance"`
		Tail       int    `json:"tail"`
		ModelTier  string `json:"model_tier"`
	}](input)
	if err != nil {
		return nil, err
	}

	// Default: BACKGROUND. Sub-agents run async by default — spawn returns
	// immediately with a task ID, completion is injected as a notification and
	// can be awaited via task_wait. Explicit background=false blocks the parent
	// turn until the final reply arrives.
	background := true
	if params.Background != nil {
		background = *params.Background
	}
	_ = background

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

	// 检查 ctx 是否为 nil，避免后续访问 panic
	if ctx == nil {
		return nil, fmt.Errorf("tool context is required")
	}

	// Ensure global agents are synced to workspace
	EnsureSynced(ctx)

	originUserID := ctx.OriginUserID
	if originUserID == "" {
		originUserID = ctx.SenderID // fallback：兼容旧数据
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

	// Resolve model: model_tier param > role.Model > "balance" (default tier)
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
			// Propagate background flag via ToolContext metadata.
			// Default is background=true; explicit false blocks synchronously.
			if ctx.Metadata == nil {
				ctx.Metadata = make(map[string]string)
			}
			if background {
				ctx.Metadata["background"] = "true"
			} else {
				ctx.Metadata["background"] = "false"
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

	if ctx.Metadata == nil {
		ctx.Metadata = make(map[string]string)
	}
	if background {
		ctx.Metadata["background"] = "true"
	} else {
		ctx.Metadata["background"] = "false"
	}

	// One-shot mode. Background (default): the Run executes in a goroutine
	// registered with the task manager — spawn returns immediately with a task
	// ID the parent can task_wait on; the completion result is injected as a
	// notification. Explicit background=false blocks until the final reply.
	result, err := ctx.Manager.RunSubAgent(ctx, params.Task, role.SystemPrompt, role.AllowedTools, role.Capabilities, params.Role, params.Instance, effectiveModel)
	if err != nil {
		return nil, fmt.Errorf("sub-agent failed: %w", err)
	}

	return NewResult(result), nil
}

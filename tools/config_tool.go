package tools

import (
	"encoding/json"
	"fmt"

	llm "xbot/llm"
	log "xbot/logger"
)

// ConfigTool allows AI to read and modify xbot configuration.
// Sensitive values (api_key) are masked on read, but can be set by the user.
type ConfigTool struct{}

func (t *ConfigTool) Name() string { return "config" }

func (t *ConfigTool) Description() string {
	return "Read, list, and modify any xbot configuration setting. " +
		"This is the PRIMARY tool for all configuration management — subscriptions, models, settings, plugins, hooks, and runners. " +
		"Use this whenever the user wants to see available configs, check a setting, or change a setting " +
		"like max_iterations, context_mode, llm_model, llm_provider, or any other config key. " +
		"For theme switching and TUI layout (sidebar_width, sidebar_position), use tui_control. " +
		"Actions: list (see all configs with descriptions), get (key), set (key, value), " +
		"subscriptions (list all LLM subscriptions), reload_plugins, reload_hooks, " +
		"runner (manage remote runners: sub=create|list|delete|switch). " +
		"To view token usage, tell the user to run /usage."
}

func (t *ConfigTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "action", Type: "string", Description: "Action: list, get, set, subscriptions, reload_plugins, reload_hooks, runner", Required: true},
		{Name: "key", Type: "string", Description: "Configuration key (e.g. theme, max_iterations, context_mode, llm_model). For runner action, use 'runner'.", Required: true},
		{Name: "value", Type: "string", Description: "New value (for set action)", Required: false},
		{Name: "sub", Type: "string", Description: "Runner sub-action: create, list, delete, switch, rename (only for action=runner)", Required: false},
		{Name: "name", Type: "string", Description: "Runner name (for runner create/delete/switch/rename)", Required: false},
		{Name: "new_name", Type: "string", Description: "New runner name (for runner rename only)", Required: false},
		{Name: "mode", Type: "string", Description: "Runner mode: native or docker (for runner create, default: native)", Required: false},
		{Name: "docker_image", Type: "string", Description: "Docker image name (for runner create with mode=docker)", Required: false},
		{Name: "workspace", Type: "string", Description: "Workspace directory on runner (for runner create)", Required: false},
		{Name: "llm_provider", Type: "string", Description: "LLM provider for runner (for runner create, optional)", Required: false},
		{Name: "llm_api_key", Type: "string", Description: "LLM API key for runner (for runner create, optional, masked)", Required: false},
		{Name: "llm_model", Type: "string", Description: "LLM model for runner (for runner create, optional)", Required: false},
		{Name: "llm_base_url", Type: "string", Description: "LLM base URL for runner (for runner create, optional)", Required: false},
	}
}

// isConfigKeyAllowed checks whether a key can be accessed via the config tool.
// Action-scoped keys are excluded — they are UI triggers, not config values.
func isConfigKeyAllowed(ctx *ToolContext, key string) bool {
	if ctx.ConfigList == nil {
		return true // can't check, allow (defensive)
	}
	for _, item := range ctx.ConfigList() {
		if item.Key == key {
			return true
		}
	}
	return false
}

// maskKeys are masked on read — value is replaced with "***" when returned via get.
var maskKeys = map[string]bool{
	"llm_api_key":    true,
	"runner_token":   true,
	"tavily_api_key": true,
}

func (t *ConfigTool) Execute(ctx *ToolContext, raw string) (*ToolResult, error) {
	var params struct {
		Action      string `json:"action"`
		Key         string `json:"key"`
		Value       string `json:"value"`
		Sub         string `json:"sub"`
		Name        string `json:"name"`
		NewName     string `json:"new_name"`
		Mode        string `json:"mode"`
		DockerImage string `json:"docker_image"`
		Workspace   string `json:"workspace"`
		LLMProvider string `json:"llm_provider"`
		LLMAPIKey   string `json:"llm_api_key"`
		LLMModel    string `json:"llm_model"`
		LLMBaseURL  string `json:"llm_base_url"`
	}
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, fmt.Errorf("config: invalid params: %w", err)
	}

	log.Req(ctx.Ctx, log.CatConfig).WithFields(log.Fields{"action": params.Action, "key": params.Key}).Debug("config tool called")

	switch params.Action {
	case "list":
		if ctx.ConfigList == nil {
			return nil, fmt.Errorf("config: config list not available")
		}
		items := ctx.ConfigList()
		b, _ := json.MarshalIndent(items, "", "  ")
		return NewResult(string(b)), nil

	case "subscriptions":
		if ctx.ListSubscriptions == nil {
			return nil, fmt.Errorf("config: subscription listing not available")
		}
		subs := ctx.ListSubscriptions()
		b, _ := json.MarshalIndent(subs, "", "  ")
		return NewResult(string(b)), nil

	case "get":
		if ctx.ConfigGet == nil {
			return nil, fmt.Errorf("config: config service not available")
		}
		if !isConfigKeyAllowed(ctx, params.Key) {
			return nil, fmt.Errorf("config: %q is not a user config key (LLM settings use /set-llm, subscription settings use /subscription)", params.Key)
		}
		val, err := ctx.ConfigGet(params.Key)
		if err != nil {
			return nil, fmt.Errorf("config: get %q failed: %w", params.Key, err)
		}
		if maskKeys[params.Key] && len(val) > 4 {
			val = val[:4] + "***"
		}
		return NewResult(fmt.Sprintf("%s = %s", params.Key, val)), nil

	case "set":
		if ctx.ConfigSet == nil {
			return nil, fmt.Errorf("config: config service not available")
		}
		if params.Value == "" {
			return nil, fmt.Errorf("config: value required for set action")
		}
		if !isConfigKeyAllowed(ctx, params.Key) {
			return nil, fmt.Errorf("config: %q is not a user config key (LLM settings use /set-llm, subscription settings use /subscription)", params.Key)
		}

		// Special handling for session_name: rename the chat session
		if params.Key == "session_name" {
			if ctx.ChatRename == nil {
				return nil, fmt.Errorf("config: session rename not available")
			}
			oldName, err := ctx.ChatRename(params.Value)
			if err != nil {
				return nil, fmt.Errorf("config: rename session failed: %w", err)
			}
			return NewResult(fmt.Sprintf("会话已从 %s 重命名为 %s", oldName, params.Value)), nil
		}

		// Global-scoped settings require admin privileges
		if ctx.IsGlobalKey != nil && ctx.IsGlobalKey(params.Key) && !ctx.OriginUserIsAdmin {
			return nil, fmt.Errorf("config: %q is a global setting and can only be modified by an admin", params.Key)
		}
		prev, err := ctx.ConfigSet(params.Key, params.Value)
		if err != nil {
			return nil, fmt.Errorf("config: set %q failed: %w", params.Key, err)
		}
		// Notify TUI to reload settings-dependent caches (context bar, model name, etc.).
		// Without this, changes like max_context_tokens don't reflect in the TUI until restart.
		if ctx.TUIControl != nil {
			if _, tuiErr := ctx.TUIControl("reload_settings", map[string]string{"key": params.Key}); tuiErr != nil {
				log.Req(ctx.Ctx, log.CatConfig).WithError(tuiErr).WithField("key", params.Key).Debug("config: TUI reload_settings notification failed (non-fatal)")
			}
		}
		return NewResult(fmt.Sprintf("Updated %s from %s to %s", params.Key, prev, params.Value)), nil

	case "reload_plugins":
		if ctx.PluginReloader == nil {
			return nil, fmt.Errorf("config: plugin reload is not available (plugin system not enabled)")
		}
		if err := ctx.PluginReloader(); err != nil {
			return nil, fmt.Errorf("config: reload_plugins failed: %w", err)
		}
		return NewResult("All plugins reloaded successfully"), nil

	case "reload_hooks":
		if ctx.HooksReloader == nil {
			return nil, fmt.Errorf("config: hooks reload is not available")
		}
		if err := ctx.HooksReloader(); err != nil {
			return nil, fmt.Errorf("config: reload_hooks failed: %w", err)
		}
		return NewResult("Hooks configuration reloaded successfully"), nil

	case "runner":
		return t.runnerAction(ctx, params.Sub, params.Name, params.NewName, params.Mode, params.DockerImage, params.Workspace,
			params.LLMProvider, params.LLMAPIKey, params.LLMModel, params.LLMBaseURL)

	default:
		return nil, fmt.Errorf("config: unknown action: %s (valid: list, get, set, subscriptions, reload_plugins, reload_hooks, runner)", params.Action)
	}
}

// runnerAction handles runner CRUD sub-actions for the config tool.
func (t *ConfigTool) runnerAction(ctx *ToolContext, sub, name, newName, mode, dockerImage, workspace, llmProvider, llmAPIKey, llmModel, llmBaseURL string) (*ToolResult, error) {
	switch sub {
	case "create":
		if name == "" {
			return nil, fmt.Errorf("config runner create: name is required")
		}
		if mode == "" {
			mode = "native"
		}
		if dockerImage == "" {
			dockerImage = "ubuntu:22.04"
		}
		if ctx.RunnerCreate == nil {
			return nil, fmt.Errorf("config runner create: runner management not configured (no runner DB)")
		}
		token, err := ctx.RunnerCreate(name, mode, dockerImage, workspace, llmProvider, llmAPIKey, llmModel, llmBaseURL)
		if err != nil {
			return nil, fmt.Errorf("config runner create: %w", err)
		}
		// Mask the token in output
		masked := token
		if len(token) > 8 {
			masked = token[:4] + "..." + token[len(token)-4:]
		}
		return NewResult(fmt.Sprintf("Runner %q created successfully.\nToken: %s\n\nConnect command:\n  xbot-runner --server <server-url> --token %s", name, masked, token)), nil

	case "list":
		if ctx.RunnerList == nil {
			return nil, fmt.Errorf("config runner list: runner management not configured (no runner DB)")
		}
		runners, err := ctx.RunnerList()
		if err != nil {
			return nil, fmt.Errorf("config runner list: %w", err)
		}
		if len(runners) == 0 {
			return NewResult("No runners found. Use 'config action=runner sub=create name=...' to create one."), nil
		}
		b, _ := json.MarshalIndent(runners, "", "  ")
		return NewResult(string(b)), nil

	case "delete":
		if name == "" {
			return nil, fmt.Errorf("config runner delete: name is required")
		}
		if ctx.RunnerDelete == nil {
			return nil, fmt.Errorf("config runner delete: runner management not configured (no runner DB)")
		}
		if err := ctx.RunnerDelete(name); err != nil {
			return nil, fmt.Errorf("config runner delete: %w", err)
		}
		return NewResult(fmt.Sprintf("Runner %q deleted successfully.", name)), nil

	case "switch":
		if name == "" {
			return nil, fmt.Errorf("config runner switch: name is required")
		}
		// Session-level binding: one session switching runner doesn't affect others
		sessionKey := ctx.Channel + ":" + ctx.ChatID
		if sb := GetSandbox(); sb != nil {
			if router, ok := sb.(*SandboxRouter); ok {
				router.SetSessionRunner(sessionKey, name)
				// Immediately update current sandbox for this turn
				ctx.Sandbox = router.SandboxForSession(sessionKey, ctx.OriginUserID)

				// Reset CWD to the runner's LIVE workspace (from connected runner)
				if router.Remote() != nil {
					ws, _ := router.Remote().GetConnectionInfo(ctx.OriginUserID, name)
					if ws == "" {
						log.Req(ctx.Ctx, log.CatConfig).WithField("runner", name).Debug("Runner connected but workspace not reported yet, keeping current CWD")
					} else if ctx.SetCurrentDir != nil {
						ctx.SetCurrentDir(ws)
						ctx.CurrentDir = ws
						ctx.WorkingDir = ws
					}
				}
			}
		}
		return NewResult(fmt.Sprintf("Switched active runner to %q.", name)), nil

	case "rename":
		if name == "" {
			return nil, fmt.Errorf("config runner rename: old name (name parameter) is required")
		}
		if newName == "" {
			return nil, fmt.Errorf("config runner rename: new name (new_name parameter) is required")
		}
		if ctx.RunnerRename == nil {
			return nil, fmt.Errorf("config runner rename: runner management not configured (no runner DB)")
		}
		if err := ctx.RunnerRename(name, newName); err != nil {
			return nil, fmt.Errorf("config runner rename: %w", err)
		}
		return NewResult(fmt.Sprintf("Runner %q renamed to %q.", name, newName)), nil

	default:
		// If no sub specified, show current active runner
		if sub == "" {
			if ctx.RunnerGetActive == nil {
				return nil, fmt.Errorf("config runner: runner management not configured (no runner DB)")
			}
			active, err := ctx.RunnerGetActive()
			if err != nil {
				return nil, fmt.Errorf("config runner: %w", err)
			}
			if active == "" {
				return NewResult("No active runner set. Use 'config action=runner sub=list' to see available runners, then 'config action=runner sub=switch name=...' to activate one."), nil
			}
			return NewResult(fmt.Sprintf("Active runner: %s", active)), nil
		}
		return nil, fmt.Errorf("config runner: unknown sub-action: %s (valid: create, list, delete, switch, rename)", sub)
	}
}

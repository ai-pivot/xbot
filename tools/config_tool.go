package tools

import (
	"encoding/json"
	"fmt"
	"strconv"

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
		"Actions: list, get, set, subscriptions, model, subscription, reload_plugins, reload_hooks, runner. " +
		"To view token usage, tell the user to run /usage."
}

// configParams holds all parameters for the config tool. Fields are shared
// across actions — only relevant ones are used per action.
type configParams struct {
	Action     string `json:"action"`
	Key        string `json:"key"`
	Value      string `json:"value"`
	Sub        string `json:"sub"`
	SubID      string `json:"sub_id"`
	Model      string `json:"model"`
	MaxContext string `json:"max_context"`
	MaxOutput  string `json:"max_output"`
	Name       string `json:"name"`
	Provider   string `json:"provider"`
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key"`
	APIType    string `json:"api_type"`
	IsDefault  bool   `json:"is_default"`
	// Runner params (unchanged from original)
	NewName     string `json:"new_name"`
	Mode        string `json:"mode"`
	DockerImage string `json:"docker_image"`
	Workspace   string `json:"workspace"`
	LLMProvider string `json:"llm_provider"`
	LLMAPIKey   string `json:"llm_api_key"`
	LLMModel    string `json:"llm_model"`
	LLMBaseURL  string `json:"llm_base_url"`
}

func (t *ConfigTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "action", Type: "string", Description: "Action: list, get, set, subscriptions, model, subscription, reload_plugins, reload_hooks, runner", Required: true},
		{Name: "key", Type: "string", Description: "Config key (for get/set). For runner action, use 'runner'.", Required: true},
		{Name: "value", Type: "string", Description: "New value (for set) or boolean string (for subscription set_enabled)", Required: false},
		{Name: "sub", Type: "string", Description: "Sub-action for model/subscription/runner. Model: list|switch|set_context|set_output|enable|disable|add|remove|refresh|active. Subscription: list|add|remove|update|set_default|set_enabled|rename. Runner: create|list|delete|switch|rename", Required: false},
		{Name: "sub_id", Type: "string", Description: "Subscription ID (for model/subscription actions)", Required: false},
		{Name: "model", Type: "string", Description: "Model name (for model actions)", Required: false},
		{Name: "max_context", Type: "integer", Description: "Max context tokens (for model set_context/add)", Required: false},
		{Name: "max_output", Type: "integer", Description: "Max output tokens (for model set_output/add, subscription add/update)", Required: false},
		{Name: "name", Type: "string", Description: "Name (for subscription add/rename, runner create/delete/switch/rename)", Required: false},
		{Name: "provider", Type: "string", Description: "LLM provider: openai|anthropic (for subscription add/update)", Required: false},
		{Name: "base_url", Type: "string", Description: "API base URL (for subscription add/update)", Required: false},
		{Name: "api_key", Type: "string", Description: "API key (for subscription add/update, masked)", Required: false},
		{Name: "api_type", Type: "string", Description: "API type: chat_completions or responses (for model add)", Required: false},
		{Name: "is_default", Type: "boolean", Description: "Set as default subscription (for subscription add)", Required: false},
		{Name: "new_name", Type: "string", Description: "New name (for runner rename only)", Required: false},
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
	var params configParams
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, fmt.Errorf("config: invalid params: %w", err)
	}

	log.WithFields(log.Fields{"action": params.Action, "key": params.Key}).Debug("config tool called")

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
			return nil, fmt.Errorf("config: %q is not a user config key (use model/subscription actions for LLM settings)", params.Key)
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
			return nil, fmt.Errorf("config: %q is not a user config key (use model/subscription actions for LLM settings)", params.Key)
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
		// Notify TUI to reload settings-dependent caches.
		if ctx.TUIControl != nil {
			if _, tuiErr := ctx.TUIControl("reload_settings", map[string]string{"key": params.Key}); tuiErr != nil {
				log.WithError(tuiErr).WithField("key", params.Key).Debug("config: TUI reload_settings notification failed (non-fatal)")
			}
		}
		return NewResult(fmt.Sprintf("Updated %s from %s to %s", params.Key, prev, params.Value)), nil

	case "model":
		return t.modelAction(ctx, params)

	case "subscription":
		return t.subscriptionAction(ctx, params)

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
		return nil, fmt.Errorf("config: unknown action: %s (valid: list, get, set, subscriptions, model, subscription, reload_plugins, reload_hooks, runner)", params.Action)
	}
}

// modelAction handles LLM model management sub-actions.
func (t *ConfigTool) modelAction(ctx *ToolContext, p configParams) (*ToolResult, error) {
	switch p.Sub {
	case "list":
		if ctx.ListModelsFn == nil {
			return nil, fmt.Errorf("config: model listing not available")
		}
		models := ctx.ListModelsFn()
		b, _ := json.MarshalIndent(models, "", "  ")
		return NewResult(string(b)), nil

	case "active":
		if ctx.GetActiveModelFn == nil {
			return nil, fmt.Errorf("config: active model query not available")
		}
		subID, model, err := ctx.GetActiveModelFn()
		if err != nil {
			return nil, err
		}
		return NewResult(fmt.Sprintf("Active: sub_id=%s, model=%s", subID, model)), nil

	case "switch":
		if ctx.SelectModelFn == nil {
			return nil, fmt.Errorf("config: model switch not available")
		}
		if p.SubID == "" || p.Model == "" {
			return nil, fmt.Errorf("config model switch: sub_id and model are required")
		}
		if err := ctx.SelectModelFn(p.SubID, p.Model); err != nil {
			return nil, fmt.Errorf("config model switch: %w", err)
		}
		// Notify TUI to refresh status bar
		if ctx.TUIControl != nil {
			ctx.TUIControl("reload_settings", map[string]string{"key": "llm_model"})
		}
		return NewResult(fmt.Sprintf("Switched session model to %s (sub: %s)", p.Model, p.SubID)), nil

	case "set_context":
		if ctx.SetModelContextFn == nil {
			return nil, fmt.Errorf("config: set_model_context not available")
		}
		if p.SubID == "" || p.Model == "" {
			return nil, fmt.Errorf("config model set_context: sub_id and model are required")
		}
		maxCtx, err := strconv.Atoi(p.Value)
		if err != nil {
			// Try max_context parameter as fallback
			if p.MaxContext != "" {
				maxCtx, err = strconv.Atoi(p.MaxContext)
			}
			if err != nil {
				return nil, fmt.Errorf("config model set_context: max_context must be an integer, got %q", p.Value)
			}
		}
		if err := ctx.SetModelContextFn(p.SubID, p.Model, maxCtx); err != nil {
			return nil, fmt.Errorf("config model set_context: %w", err)
		}
		return NewResult(fmt.Sprintf("Set max_context=%d for model %s (sub: %s)", maxCtx, p.Model, p.SubID)), nil

	case "set_output":
		if ctx.SetModelOutputFn == nil {
			return nil, fmt.Errorf("config: set_model_output not available")
		}
		if p.SubID == "" || p.Model == "" {
			return nil, fmt.Errorf("config model set_output: sub_id and model are required")
		}
		maxOut, err := strconv.Atoi(p.Value)
		if err != nil {
			if p.MaxOutput != "" {
				maxOut, err = strconv.Atoi(p.MaxOutput)
			}
			if err != nil {
				return nil, fmt.Errorf("config model set_output: max_output must be an integer, got %q", p.Value)
			}
		}
		if err := ctx.SetModelOutputFn(p.SubID, p.Model, maxOut); err != nil {
			return nil, fmt.Errorf("config model set_output: %w", err)
		}
		return NewResult(fmt.Sprintf("Set max_output=%d for model %s (sub: %s)", maxOut, p.Model, p.SubID)), nil

	case "enable":
		if ctx.SetModelEnabledFn == nil {
			return nil, fmt.Errorf("config: model enable not available")
		}
		if p.SubID == "" || p.Model == "" {
			return nil, fmt.Errorf("config model enable: sub_id and model are required")
		}
		if err := ctx.SetModelEnabledFn(p.SubID, p.Model, true); err != nil {
			return nil, fmt.Errorf("config model enable: %w", err)
		}
		return NewResult(fmt.Sprintf("Enabled model %s (sub: %s)", p.Model, p.SubID)), nil

	case "disable":
		if ctx.SetModelEnabledFn == nil {
			return nil, fmt.Errorf("config: model disable not available")
		}
		if p.SubID == "" || p.Model == "" {
			return nil, fmt.Errorf("config model disable: sub_id and model are required")
		}
		if err := ctx.SetModelEnabledFn(p.SubID, p.Model, false); err != nil {
			return nil, fmt.Errorf("config model disable: %w", err)
		}
		return NewResult(fmt.Sprintf("Disabled model %s (sub: %s)", p.Model, p.SubID)), nil

	case "add":
		if ctx.UpsertModelFn == nil {
			return nil, fmt.Errorf("config: model add not available")
		}
		if p.SubID == "" || p.Model == "" {
			return nil, fmt.Errorf("config model add: sub_id and model are required")
		}
		maxCtx, _ := strconv.Atoi(p.MaxContext)
		maxOut, _ := strconv.Atoi(p.MaxOutput)
		if err := ctx.UpsertModelFn(p.SubID, p.Model, maxCtx, maxOut, p.APIType); err != nil {
			return nil, fmt.Errorf("config model add: %w", err)
		}
		return NewResult(fmt.Sprintf("Added/updated model %s on subscription %s", p.Model, p.SubID)), nil

	case "remove":
		if ctx.RemoveModelFn == nil {
			return nil, fmt.Errorf("config: model remove not available")
		}
		if p.SubID == "" || p.Model == "" {
			return nil, fmt.Errorf("config model remove: sub_id and model are required")
		}
		if err := ctx.RemoveModelFn(p.SubID, p.Model); err != nil {
			return nil, fmt.Errorf("config model remove: %w", err)
		}
		return NewResult(fmt.Sprintf("Removed model %s from subscription %s", p.Model, p.SubID)), nil

	case "refresh":
		if ctx.RefreshModelsFn == nil {
			return nil, fmt.Errorf("config: model refresh not available")
		}
		models := ctx.RefreshModelsFn()
		b, _ := json.MarshalIndent(models, "", "  ")
		return NewResult(fmt.Sprintf("Refreshed %d models from providers:\n%s", len(models), string(b))), nil

	default:
		return nil, fmt.Errorf("config model: unknown sub-action: %s (valid: list, active, switch, set_context, set_output, enable, disable, add, remove, refresh)", p.Sub)
	}
}

// subscriptionAction handles LLM subscription CRUD sub-actions.
func (t *ConfigTool) subscriptionAction(ctx *ToolContext, p configParams) (*ToolResult, error) {
	switch p.Sub {
	case "list":
		// Reuse the existing ListSubscriptions closure
		if ctx.ListSubscriptions == nil {
			return nil, fmt.Errorf("config: subscription listing not available")
		}
		subs := ctx.ListSubscriptions()
		b, _ := json.MarshalIndent(subs, "", "  ")
		return NewResult(string(b)), nil

	case "add":
		if ctx.AddSubscriptionFn == nil {
			return nil, fmt.Errorf("config: subscription add not available")
		}
		if p.Name == "" || p.Provider == "" {
			return nil, fmt.Errorf("config subscription add: name and provider are required")
		}
		maxOut, _ := strconv.Atoi(p.MaxOutput)
		id, err := ctx.AddSubscriptionFn(SubscriptionCreateParams{
			Name:            p.Name,
			Provider:        p.Provider,
			BaseURL:         p.BaseURL,
			APIKey:          p.APIKey,
			Model:           p.Model,
			MaxOutputTokens: maxOut,
			IsDefault:       p.IsDefault,
		})
		if err != nil {
			return nil, fmt.Errorf("config subscription add: %w", err)
		}
		return NewResult(fmt.Sprintf("Created subscription %q (id: %s)", p.Name, id)), nil

	case "remove":
		if ctx.RemoveSubscriptionFn == nil {
			return nil, fmt.Errorf("config: subscription remove not available")
		}
		if p.SubID == "" {
			return nil, fmt.Errorf("config subscription remove: sub_id is required")
		}
		if err := ctx.RemoveSubscriptionFn(p.SubID); err != nil {
			return nil, fmt.Errorf("config subscription remove: %w", err)
		}
		return NewResult(fmt.Sprintf("Removed subscription %s", p.SubID)), nil

	case "update":
		if ctx.UpdateSubscriptionFieldsFn == nil {
			return nil, fmt.Errorf("config: subscription update not available")
		}
		if p.SubID == "" {
			return nil, fmt.Errorf("config subscription update: sub_id is required")
		}
		maxOut, _ := strconv.Atoi(p.MaxOutput)
		if err := ctx.UpdateSubscriptionFieldsFn(p.SubID, SubscriptionUpdateParams{
			Name:            p.Name,
			Provider:        p.Provider,
			BaseURL:         p.BaseURL,
			APIKey:          p.APIKey,
			Model:           p.Model,
			MaxOutputTokens: maxOut,
		}); err != nil {
			return nil, fmt.Errorf("config subscription update: %w", err)
		}
		return NewResult(fmt.Sprintf("Updated subscription %s", p.SubID)), nil

	case "set_default":
		if ctx.SetDefaultSubscriptionFn == nil {
			return nil, fmt.Errorf("config: set_default not available")
		}
		if p.SubID == "" {
			return nil, fmt.Errorf("config subscription set_default: sub_id is required")
		}
		if err := ctx.SetDefaultSubscriptionFn(p.SubID); err != nil {
			return nil, fmt.Errorf("config subscription set_default: %w", err)
		}
		return NewResult(fmt.Sprintf("Set default subscription to %s", p.SubID)), nil

	case "set_enabled":
		if ctx.SetSubscriptionEnabledFn == nil {
			return nil, fmt.Errorf("config: set_enabled not available")
		}
		if p.SubID == "" {
			return nil, fmt.Errorf("config subscription set_enabled: sub_id is required")
		}
		enabled := p.Value == "true" || p.Value == "1"
		if err := ctx.SetSubscriptionEnabledFn(p.SubID, enabled); err != nil {
			return nil, fmt.Errorf("config subscription set_enabled: %w", err)
		}
		state := "disabled"
		if enabled {
			state = "enabled"
		}
		return NewResult(fmt.Sprintf("Subscription %s %s", p.SubID, state)), nil

	case "rename":
		if ctx.RenameSubscriptionFn == nil {
			return nil, fmt.Errorf("config: rename not available")
		}
		if p.SubID == "" || p.Name == "" {
			return nil, fmt.Errorf("config subscription rename: sub_id and name are required")
		}
		if err := ctx.RenameSubscriptionFn(p.SubID, p.Name); err != nil {
			return nil, fmt.Errorf("config subscription rename: %w", err)
		}
		return NewResult(fmt.Sprintf("Renamed subscription %s to %q", p.SubID, p.Name)), nil

	default:
		return nil, fmt.Errorf("config subscription: unknown sub-action: %s (valid: list, add, remove, update, set_default, set_enabled, rename)", p.Sub)
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
		sessionKey := ctx.Channel + ":" + ctx.ChatID
		if sb := GetSandbox(); sb != nil {
			if router, ok := sb.(*SandboxRouter); ok {
				router.SetSessionRunner(sessionKey, name)
				ctx.Sandbox = router.SandboxForSession(sessionKey, ctx.OriginUserID)
				if router.Remote() != nil {
					ws, _ := router.Remote().GetConnectionInfo(ctx.OriginUserID, name)
					if ws == "" {
						log.WithField("runner", name).Debug("Runner connected but workspace not reported yet, keeping current CWD")
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

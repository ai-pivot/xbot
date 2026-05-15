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
		"Use this whenever the user wants to see available configs, check a setting, or change a setting " +
		"like max_iterations, context_mode, api_key, provider, theme (prefer tui_control for theme switching), " +
		"sidebar_width (prefer tui_control), or any other config key. " +
		"Actions: list (see all configs with descriptions), get (key), set (key, value), " +
		"subscriptions (list all LLM subscriptions)."
}

func (t *ConfigTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "action", Type: "string", Description: "get or set", Required: true},
		{Name: "key", Type: "string", Description: "Configuration key (e.g. theme, max_iterations, context_mode)", Required: true},
		{Name: "value", Type: "string", Description: "New value (for set action)", Required: false},
	}
}

// isConfigKeyAllowed checks whether a key can be accessed via the config tool.
// Subscription-scoped (LLM keys) and action-scoped keys are excluded — they have
// dedicated management paths (/set-llm, subscription management, palette).
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
		Action string `json:"action"`
		Key    string `json:"key"`
		Value  string `json:"value"`
	}
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
		return NewResult(fmt.Sprintf("Updated %s from %s to %s", params.Key, prev, params.Value)), nil

	default:
		return nil, fmt.Errorf("config: unknown action: %s (valid: list, get, set, subscriptions)", params.Action)
	}
}

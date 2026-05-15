package tools

import (
	"encoding/json"
	"fmt"

	llm "xbot/llm"
	log "xbot/logger"
)

// TuiControlParams defines the input for tui_control tool.
type TuiControlParams struct {
	Action string            `json:"action"` // "switch_session" | "close_session" | "set_layout" | "set_theme"
	ChatID string            `json:"chat_id,omitempty"`
	Key    string            `json:"key,omitempty"`    // for set_layout
	Value  string            `json:"value,omitempty"`  // for set_layout / set_theme
	Theme  string            `json:"theme,omitempty"`  // for set_theme
	Params map[string]string `json:"params,omitempty"` // extra params (e.g. confirm)
}

// TuiControlTool allows AI to operate the TUI sidebar and layout.
// It is registered as a non-core tool (load_tools activated).
type TuiControlTool struct{}

func (t *TuiControlTool) Name() string { return "tui_control" }

func (t *TuiControlTool) Description() string {
	return "Operate the TUI directly: switch sidebar sessions, close sessions, resize sidebar, change themes. " +
		"Use this whenever the user asks to switch to a different session, close a session, adjust the sidebar width, " +
		"or change the theme. This is the PRIMARY way to navigate between sessions in the TUI. " +
		"To CREATE a new session, use CreateChat (type=agent, role=explore, instance=\"name\") instead. " +
		"To create a custom theme, use FileCreate to write a JSON file to ~/.xbot/themes/<name>.json then switch via set_theme. Activate the ai-config skill for the JSON format template. " +
		"Use send_slash to execute slash commands in the input box (/set-llm, /model, /palette, /context, etc.). " +
		"Actions: switch_session(chat_id), close_session(chat_id, params.confirm=true), " +
		"set_layout(key=\"sidebar_width\"|..., value), set_theme(theme_name), send_slash(command=\"/set-llm ...\"), " +
		"reload_plugins(), reload_hooks(). " +
		"Use reload_plugins after creating or modifying plugin files to hot-reload all plugins. " +
		"Use reload_hooks after modifying hooks.json to reload hook configuration without restart. " +
		"To find available sessions to switch to, look at the sessions listed in the sidebar."
}

func (t *TuiControlTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "action", Type: "string", Description: "Action: switch_session, close_session, set_layout, set_theme, send_slash, reload_plugins, reload_hooks", Required: true},
		{Name: "chat_id", Type: "string", Description: "Target session chatID (for switch/close)", Required: false},
		{Name: "key", Type: "string", Description: "Layout key: sidebar_width, sidebar_enabled, etc.", Required: false},
		{Name: "value", Type: "string", Description: "New value for layout key or theme name", Required: false},
		{Name: "theme", Type: "string", Description: "Theme name to switch to", Required: false},
		{Name: "params", Type: "object", Description: "Extra parameters (e.g. {\"confirm\":\"true\"} for close, {\"command\":\"/set-llm ...\"} for send_slash)", Required: false},
	}
}

func (t *TuiControlTool) Execute(ctx *ToolContext, raw string) (*ToolResult, error) {
	if ctx.TUIControl == nil {
		return nil, fmt.Errorf("tui_control: TUI session control is only available in local CLI mode (use /su <session> or sidebar to switch manually)")
	}

	var params TuiControlParams
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, fmt.Errorf("tui_control: invalid params: %w", err)
	}

	log.WithField("action", params.Action).Debug("tui_control called")

	switch params.Action {
	case "switch_session":
		if params.ChatID == "" {
			return nil, fmt.Errorf("tui_control: chat_id required for switch_session")
		}
		res, err := ctx.TUIControl("switch", map[string]string{"chat_id": params.ChatID})
		if err != nil {
			return nil, fmt.Errorf("tui_control: switch_session failed: %w", err)
		}
		return NewResult(fmt.Sprintf("Switched to session %s", res["chat_id"])), nil

	case "close_session":
		if params.ChatID == "" {
			return nil, fmt.Errorf("tui_control: chat_id required for close_session")
		}
		ctrlParams := map[string]string{"chat_id": params.ChatID}
		if params.Params != nil {
			if confirm, ok := params.Params["confirm"]; ok {
				ctrlParams["confirm"] = confirm
			}
		}
		res, err := ctx.TUIControl("close", ctrlParams)
		if err != nil {
			// Check if it's a confirmation request
			if err.Error()[:len("confirmation_required")] == "confirmation_required" {
				return &ToolResult{
					Summary: "Confirmation required to close this session. Call again with params: {\"confirm\":\"true\"}",
					Detail:  err.Error(),
				}, nil
			}
			return nil, fmt.Errorf("tui_control: close_session failed: %w", err)
		}
		_ = res
		return NewResult("Session closed"), nil

	case "set_layout":
		if params.Key == "" || params.Value == "" {
			return nil, fmt.Errorf("tui_control: key and value required for set_layout")
		}
		res, err := ctx.TUIControl("layout", map[string]string{"key": params.Key, "value": params.Value})
		if err != nil {
			return nil, fmt.Errorf("tui_control: set_layout failed: %w", err)
		}
		prev := res["previous"]
		return NewResult(fmt.Sprintf("Layout updated: %s changed from %s to %s", params.Key, prev, params.Value)), nil

	case "set_theme":
		if params.Theme == "" {
			return nil, fmt.Errorf("tui_control: theme required for set_theme")
		}
		res, err := ctx.TUIControl("theme", map[string]string{"theme": params.Theme})
		if err != nil {
			return nil, fmt.Errorf("tui_control: set_theme failed: %w", err)
		}
		prev := res["previous"]
		return NewResult(fmt.Sprintf("Theme changed from %s to %s", prev, params.Theme)), nil

	case "send_slash":
		cmd := ""
		if params.Params != nil {
			cmd = params.Params["command"]
		}
		if cmd == "" {
			return nil, fmt.Errorf("tui_control: params.command required for send_slash (e.g. {\"command\":\"/set-llm provider=anthropic\"})")
		}
		res, err := ctx.TUIControl("send_slash", map[string]string{"command": cmd})
		if err != nil {
			return nil, fmt.Errorf("tui_control: send_slash failed: %w", err)
		}
		_ = res
		return NewResult(fmt.Sprintf("Slash command sent: %s", cmd)), nil

	case "reload_plugins":
		if ctx.PluginReloader == nil {
			return nil, fmt.Errorf("tui_control: plugin reload is not available (plugin system not enabled)")
		}
		if err := ctx.PluginReloader(); err != nil {
			return nil, fmt.Errorf("tui_control: reload_plugins failed: %w", err)
		}
		return NewResult("All plugins reloaded successfully"), nil

	case "reload_hooks":
		if ctx.HooksReloader == nil {
			return nil, fmt.Errorf("tui_control: hooks reload is not available")
		}
		if err := ctx.HooksReloader(); err != nil {
			return nil, fmt.Errorf("tui_control: reload_hooks failed: %w", err)
		}
		return NewResult("Hooks configuration reloaded successfully"), nil

	default:
		if params.Action == "new_session" || params.Action == "create_session" {
			return nil, fmt.Errorf("tui_control: to create a new session, use the CreateChat tool instead (type=agent, role=explore, instance=\"debug\"). tui_control only manages existing sessions")
		}
		return nil, fmt.Errorf("tui_control: unknown action: %s (valid: switch_session, close_session, set_layout, set_theme)", params.Action)
	}
}

package hooks

import (
	"context"
	"encoding/json"

	"xbot/plugin"
)

// PluginBridgeCallback returns a CallbackHook that dispatches hook events
// to the plugin system's PluginHookBridge. This is the integration point
// between xbot's hooks system and the plugin system.
//
// Register this as a builtin callback via:
//
//	hookManager.RegisterBuiltin(hooks.PluginBridgeCallback(bridge))
func PluginBridgeCallback(bridge *plugin.PluginHookBridge) *CallbackHook {
	return &CallbackHook{
		Name: "plugin_bridge",
		Fn: func(ctx context.Context, event Event) (*Result, error) {
			// Convert hooks.Event → plugin.HookPayload
			payload := &plugin.HookPayload{
				Event:    plugin.HookEvent(event.EventName()),
				ToolName: event.ToolName(),
			}

			// Serialize tool input for plugin consumption
			if input := event.ToolInput(); input != nil {
				data, err := json.Marshal(input)
				if err == nil {
					payload.ToolInput = string(data)
				}
			}

			// Extract session metadata from payload
			if p := event.Payload(); p != nil {
				if sid, ok := p["session_id"].(string); ok {
					payload.SessionID = sid
				}
				if ch, ok := p["channel"].(string); ok {
					payload.Channel = ch
				}
				if cid, ok := p["chat_id"].(string); ok {
					payload.ChatID = cid
				}
				if uid, ok := p["sender_id"].(string); ok {
					payload.UserID = uid
				}
				// Extract tenant ID for multi-tenancy
				if tid, ok := p["tenant_id"].(int64); ok {
					payload.TenantID = tid
				}
				// Pass tool output to plugins (e.g. for diff plugins)
				if out, ok := p["tool_output"].(string); ok {
					payload.ToolOutput = out
				}
				// Pass elapsed time for performance-aware plugins
				if ms, ok := p["tool_elapsed_ms"].(int64); ok {
					payload.ToolElapsedMs = ms
				}
			}

			// Extract session context (model, tokens) from context.
			// Injected by the engine via WithSessionContext.
			if sc := SessionContextFromContext(ctx); sc != nil {
				extra := make(map[string]any)
				if sc.Model != "" {
					extra["model"] = sc.Model
				}
				if sc.MaxContext > 0 {
					extra["max_context"] = sc.MaxContext
				}
				if sc.PromptTokens > 0 {
					extra["prompt_tokens"] = sc.PromptTokens
				}
				if sc.CompTokens > 0 {
					extra["comp_tokens"] = sc.CompTokens
				}
				if len(extra) > 0 {
					payload.Extra = extra
				}
			}

			// Dispatch to all registered plugin hooks
			result := bridge.Dispatch(ctx, payload)

			// Convert plugin.HookResult → hooks.Result
			return &Result{
				Decision: string(result.Decision),
				Reason:   result.Message,
			}, nil
		},
	}
}

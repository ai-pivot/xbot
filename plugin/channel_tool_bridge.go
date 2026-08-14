package plugin

import (
	"encoding/json"
	"fmt"

	"xbot/llm"
	"xbot/tools"
)

// ChannelToolExecutor sends tool execution requests to the channel plugin process.
// Implemented by *agent.ChannelPluginTransport (which already has a Call method).
type ChannelToolExecutor interface {
	Call(method string, payload json.RawMessage) (json.RawMessage, error)
}

// ChannelToolDecl is a tool declaration from the channel process.
// The channel process sends these via the "channel_tools" protocol message.
type ChannelToolDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  []llm.ToolParam `json:"parameters"`
	// Channels lists the channels this tool is registered to. Empty = the
	// plugin's own channel only. Allows a channel plugin to expose tools to
	// other channels (e.g. genui plugin registers display_html to "web").
	Channels []string `json:"channels,omitempty"`
	// UI declares the tool's UI capability (mode/param/libs). When set, the
	// engine's streaming extractor picks up ui.Param from tool call arguments
	// and the frontend renders the result via the fancy GenUI runtime.
	// Generic — any tool can declare UI, not just display_html.
	UI *tools.UIDecl `json:"ui,omitempty"`
}

// ChannelToolBridge adapts a channel-declared tool to the tools.Tool interface.
// The actual execution logic lives in the channel process — this bridge proxies
// tool calls via RPC (Call("execute_tool", ...)).
type ChannelToolBridge struct {
	decl     ChannelToolDecl
	executor ChannelToolExecutor
}

// NewChannelToolBridge creates a bridge for a single channel tool.
func NewChannelToolBridge(decl ChannelToolDecl, executor ChannelToolExecutor) *ChannelToolBridge {
	return &ChannelToolBridge{decl: decl, executor: executor}
}

// Name returns the tool name.
func (b *ChannelToolBridge) Name() string { return b.decl.Name }

// Description returns the tool description.
func (b *ChannelToolBridge) Description() string { return b.decl.Description }

// Parameters returns the tool parameters schema.
func (b *ChannelToolBridge) Parameters() []llm.ToolParam { return b.decl.Parameters }

// UIDecl implements tools.UIDeclProvider — returns the tool's UI capability
// declaration (nil if the tool has no UI). Consumers (engine_wire streaming
// extractor, frontend) read this metadata instead of hardcoding tool names.
func (b *ChannelToolBridge) UIDecl() *tools.UIDecl { return b.decl.UI }

// Channels returns the channels this tool is registered to.
// Empty = the bridge was registered to the plugin's own channel only.
func (b *ChannelToolBridge) Channels() []string { return b.decl.Channels }

// Execute proxies the tool call to the channel process via RPC.
func (b *ChannelToolBridge) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	params, _ := json.Marshal(struct {
		Name  string `json:"name"`
		Input string `json:"input"`
	}{
		Name:  b.decl.Name,
		Input: input,
	})

	resultRaw, err := b.executor.Call("execute_tool", params)
	if err != nil {
		return &tools.ToolResult{Summary: fmt.Sprintf("Channel tool %q error: %v", b.decl.Name, err), IsError: true}, nil
	}

	var result struct {
		Content string `json:"content"`
		IsError bool   `json:"is_error"`
		// UICode carries the complete UI source (e.g. TSX) produced by the tool.
		// When non-empty, the bridge pushes it to the channel via ctx.SendFunc
		// (same path as the built-in genui) and stores it in Detail so the
		// committed message can rebuild the UI from history.
		UICode string `json:"ui_code,omitempty"`
	}
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		// If we can't parse the result, return raw content
		return &tools.ToolResult{Summary: string(resultRaw)}, nil
	}

	// Push UI code to the frontend via the standard genui path (if available).
	if result.UICode != "" && ctx != nil && ctx.SendFunc != nil {
		meta := map[string]string{
			"genui":   "true",
			"channel": ctx.Channel,
			"chat_id": ctx.ChatID,
		}
		_ = ctx.SendFunc(ctx.Channel, ctx.ChatID, result.UICode, meta)
	}

	// Store the full UI code in Detail so it survives in iteration history —
	// the frontend rebuilds the UI from Detail on committed/history rendering.
	// ⚠️ Detail must be the PURE TSX source (no Summary prefix): the frontend
	// extracts the code from tool.detail and feeds it to sucrase — a prefix like
	// "🎨 UI rendered (5929 chars)\n" makes compilation fail → blank iframe.
	detail := result.UICode
	return &tools.ToolResult{Summary: result.Content, Detail: detail, IsError: result.IsError}, nil
}

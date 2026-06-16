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
	}
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		// If we can't parse the result, return raw content
		return &tools.ToolResult{Summary: string(resultRaw)}, nil
	}
	return &tools.ToolResult{Summary: result.Content, IsError: result.IsError}, nil
}

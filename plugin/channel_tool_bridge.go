package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"xbot/llm"
	"xbot/tools"
)

// ChannelToolExecutor sends tool execution requests to the channel plugin process.
// Implemented by *agent.ChannelPluginTransport (which already has a Call method).
type ChannelToolExecutor interface {
	Call(method string, payload json.RawMessage) (json.RawMessage, error)
}

// ChannelToolDecl is a tool declaration from the channel process.
type ChannelToolDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  []llm.ToolParam `json:"parameters"`
	Channels    []string        `json:"channels,omitempty"`
	UI          *tools.UIDecl   `json:"ui,omitempty"`
}

// ChannelToolBridge adapts a channel-declared tool to the tools.Tool interface.
type ChannelToolBridge struct {
	decl     ChannelToolDecl
	executor ChannelToolExecutor
}

// NewChannelToolBridge creates a bridge for a single channel tool.
func NewChannelToolBridge(decl ChannelToolDecl, executor ChannelToolExecutor) *ChannelToolBridge {
	return &ChannelToolBridge{decl: decl, executor: executor}
}

func (b *ChannelToolBridge) Name() string                { return b.decl.Name }
func (b *ChannelToolBridge) Description() string         { return b.decl.Description }
func (b *ChannelToolBridge) Parameters() []llm.ToolParam { return b.decl.Parameters }
func (b *ChannelToolBridge) UIDecl() *tools.UIDecl       { return b.decl.UI }
func (b *ChannelToolBridge) Channels() []string          { return b.decl.Channels }

// Execute proxies the tool call to the channel process via RPC.
// If the result contains render_check=true, sends the code to the frontend
// via SendFunc and blocks until the frontend confirms compilation success/failure.
// This is generic — any plugin can use render_check=true in its tool result.
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
		Content     string `json:"content"`
		IsError     bool   `json:"is_error"`
		UICode      string `json:"ui_code,omitempty"`
		RenderCheck bool   `json:"render_check,omitempty"`
	}
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		return &tools.ToolResult{Summary: string(resultRaw)}, nil
	}

	// If the plugin requests a render check AND we have SendFunc (web channel),
	// send the code to the frontend for compilation validation and block until
	// the frontend responds. This is generic — any plugin can use render_check.
	if result.RenderCheck && result.UICode != "" && ctx != nil && ctx.SendFunc != nil {
		checkID := genCheckID()
		// Send code to frontend with render_check metadata.
		// The frontend compiles it (sucrase) and calls render_check_result RPC.
		meta := map[string]string{
			"genui":        "true",
			"render_check": "true",
			"check_id":     checkID,
			"channel":      ctx.Channel,
			"chat_id":      ctx.ChatID,
		}
		_ = ctx.SendFunc(ctx.Channel, ctx.ChatID, result.UICode, meta)
		// Block until frontend responds or timeout (15s).
		success, errMsg := WaitRenderCheck(checkID, DefaultRenderCheckTimeout)
		if !success {
			return &tools.ToolResult{
				Summary: fmt.Sprintf("⚠️ UI render error: %s\n\n请修复 TSX 代码后重试。", errMsg),
				IsError: true,
			}, nil
		}
	}

	// Push UI code to the frontend via the standard genui path.
	if result.UICode != "" && ctx != nil && ctx.SendFunc != nil {
		meta := map[string]string{
			"genui":   "true",
			"channel": ctx.Channel,
			"chat_id": ctx.ChatID,
		}
		_ = ctx.SendFunc(ctx.Channel, ctx.ChatID, result.UICode, meta)
	}

	detail := result.UICode
	return &tools.ToolResult{Summary: result.Content, Detail: detail, IsError: result.IsError}, nil
}

// genCheckID generates a random hex ID for render check tracking.
func genCheckID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// DefaultRenderCheckTimeout is the max time to wait for frontend compilation.
const DefaultRenderCheckTimeout = 15 * time.Second

// WaitRenderCheck registers a check_id, waits for the frontend to resolve it
// (via ResolveRenderCheck), and returns the result. On timeout, returns success
// (don't block the agent — the UI error is still shown in SandboxedUI).
func WaitRenderCheck(checkID string, timeout time.Duration) (bool, string) {
	ch := RegisterRenderCheck(checkID)
	defer func() {
		renderCheckRegistry.Delete(checkID)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case result := <-ch:
		return result.Success, result.Error
	case <-ctx.Done():
		return true, "" // timeout = assume success
	}
}

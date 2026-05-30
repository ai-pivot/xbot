// Package protocol defines the xbot plugin stdio wire format types.
//
// Plugins using the "stdio" runtime (or "grpc" for backward compat) communicate
// with xbot via newline-delimited JSON (NDJSON) over stdin/stdout. This package
// exports the canonical Go types for that protocol, so that plugin authors can
// import a single package and get correct, documented structs.
//
// # Quick Start (tool-only plugin)
//
// Implement [Handler] and pass it to [Run]:
//
//	h := &protocol.Handler{
//	    Activate: func(req *protocol.ActivateParams) (*protocol.ActivateResult, error) {
//	        return &protocol.ActivateResult{
//	            Tools: []protocol.ToolDef{
//	                {Name: "greet", Description: "Greet someone",
//	                    InputSchema: map[string]any{"type": "object",
//	                        "properties": map[string]any{"name": map[string]any{"type": "string"}}}},
//	            },
//	        }, nil
//	    },
//	    ExecuteTool: func(params *protocol.ExecuteToolParams) (*protocol.ExecuteToolResult, error) {
//	        return &protocol.ExecuteToolResult{Result: `{"msg": "Hello!"}`}, nil
//	    },
//	}
//	protocol.Run(h)
package protocol

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// ---------------------------------------------------------------------------
// Request / Response — the two top-level frame types on the wire
// ---------------------------------------------------------------------------

// Request is a JSON line sent from xbot to the plugin.
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON line sent from the plugin to xbot.
// Populate only the fields relevant to the method being responded to.
type Response struct {
	Result     string        `json:"result,omitempty"`
	Error      string        `json:"error,omitempty"`
	Tools      []ToolDef     `json:"tools,omitempty"`
	Hooks      []HookReg     `json:"hooks,omitempty"`
	HookResult *HookResult   `json:"hook_result,omitempty"`
	Enrichers  []EnricherReg `json:"enrichers,omitempty"`

	// ChannelProvider is returned by activate when the plugin provides a custom channel.
	ChannelProvider *ChannelProviderDecl `json:"channel_provider,omitempty"`
}

// Inbound represents an asynchronous message pushed by the plugin to xbot.
// Has "method" but no "result"/"error".
type Inbound struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// ---------------------------------------------------------------------------
// Typed request params (unmarshaled from Request.Params)
// ---------------------------------------------------------------------------

// ActivateParams is the params object for the "activate" method.
type ActivateParams struct {
	PluginID string `json:"pluginId"`
}

// ExecuteToolParams is the params object for the "execute_tool" method.
type ExecuteToolParams struct {
	ToolName string `json:"toolName"`
	Input    string `json:"input"`
}

// HookParams is the params object for the "hook" method.
type HookParams struct {
	Event     string `json:"event"`
	ToolName  string `json:"toolName,omitempty"`
	ToolInput string `json:"toolInput,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Channel   string `json:"channel,omitempty"`
	ChatID    string `json:"chatId,omitempty"`
}

// EnrichParams is the params object for the "enrich" method.
type EnrichParams struct {
	EnricherName string `json:"enricherName"`
}

// DeactivateParams is the params object for the "deactivate" method (empty).
type DeactivateParams struct{}

// ---------------------------------------------------------------------------
// Typed result structs (returned from handler methods)
// ---------------------------------------------------------------------------

// ActivateResult is the result of handling "activate".
type ActivateResult = Response

// ExecuteToolResult is the result of handling "execute_tool".
type ExecuteToolResult struct {
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// HookResult is the result of handling "hook".
type HookResult struct {
	Decision string          `json:"decision"` // allow, deny, ask, defer
	Message  string          `json:"message,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// EnrichResult is the result of handling "enrich".
type EnrichResult struct {
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// Declaration types (returned in activate response)
// ---------------------------------------------------------------------------

// ToolDef describes a tool registered by the plugin.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  []ToolParam     `json:"parameters,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// ToolParam describes a single parameter of a tool.
type ToolParam struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Required    bool            `json:"required"`
	Items       json.RawMessage `json:"items,omitempty"`
}

// HookReg registers a hook on a lifecycle event.
type HookReg struct {
	Event   string `json:"event"`
	Matcher string `json:"matcher"`
}

// EnricherReg registers a context enricher.
type EnricherReg struct {
	Name string `json:"name"`
}

// ChannelProviderDecl declares that the plugin provides a custom channel.
type ChannelProviderDecl struct {
	Name         string          `json:"name"`
	ConfigSchema json.RawMessage `json:"config_schema,omitempty"`
}

// ---------------------------------------------------------------------------
// Handler — plugin implementers fill this struct
// ---------------------------------------------------------------------------

// Handler is the set of callbacks a plugin must implement.
// Only set the fields your plugin needs; unset fields are no-ops.
type Handler struct {
	// Activate is called once after the plugin process starts.
	// Return tools, hooks, enrichers, and/or channel_provider.
	Activate func(params *ActivateParams) (*ActivateResult, error)

	// ExecuteTool is called when the LLM invokes one of the plugin's tools.
	ExecuteTool func(params *ExecuteToolParams) (*ExecuteToolResult, error)

	// Hook is called on lifecycle events that match registered hooks.
	Hook func(params *HookParams) (*HookResult, error)

	// Enrich is called to collect dynamic content for the system prompt.
	Enrich func(params *EnrichParams) (*EnrichResult, error)

	// Deactivate is called before the process is killed.
	Deactivate func()
}

// ---------------------------------------------------------------------------
// Run — the stdio event loop
// ---------------------------------------------------------------------------

// Run starts the NDJSON event loop on stdin/stdout and blocks until EOF or error.
// Plugin authors typically call this as their main():
//
//	func main() { protocol.Run(myHandler) }
func Run(h *Handler) {
	run(h, os.Stdin, os.Stdout)
}

// run is the internal loop, split out for testing.
func run(h *Handler, stdin io.Reader, stdout io.Writer) {
	enc := json.NewEncoder(stdout)
	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 64*1024), maxLineSize)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			writeErr(enc, "invalid JSON: "+err.Error())
			continue
		}

		dispatch(h, enc, req.Method, req.Params)
	}
}

func dispatch(h *Handler, enc *json.Encoder, method string, params json.RawMessage) {
	switch method {
	case "activate":
		if h.Activate == nil {
			writeErr(enc, "activate not implemented")
			return
		}
		p := &ActivateParams{PluginID: ""}
		if params != nil {
			json.Unmarshal(params, p)
		}
		resp, err := h.Activate(p)
		if err != nil {
			writeErr(enc, err.Error())
			return
		}
		writeMsg(enc, resp)

	case "deactivate":
		if h.Deactivate != nil {
			h.Deactivate()
		}
		// Response is ignored by xbot.
		writeMsg(enc, &Response{})

	case "execute_tool":
		if h.ExecuteTool == nil {
			writeErr(enc, "execute_tool not implemented")
			return
		}
		var p ExecuteToolParams
		if params != nil {
			json.Unmarshal(params, &p)
		}
		resp, err := h.ExecuteTool(&p)
		if err != nil {
			writeErr(enc, err.Error())
			return
		}
		writeMsg(enc, resp)

	case "hook":
		if h.Hook == nil {
			writeMsg(enc, &Response{HookResult: &HookResult{Decision: "allow"}})
			return
		}
		var p HookParams
		if params != nil {
			json.Unmarshal(params, &p)
		}
		resp, err := h.Hook(&p)
		if err != nil {
			writeErr(enc, err.Error())
			return
		}
		writeMsg(enc, &Response{HookResult: resp})

	case "enrich":
		if h.Enrich == nil {
			writeErr(enc, "enrich not implemented")
			return
		}
		var p EnrichParams
		if params != nil {
			json.Unmarshal(params, &p)
		}
		resp, err := h.Enrich(&p)
		if err != nil {
			writeErr(enc, err.Error())
			return
		}
		writeMsg(enc, resp)

	default:
		writeErr(enc, fmt.Sprintf("unknown method: %s", method))
	}
}

func writeMsg(enc *json.Encoder, msg any) {
	_ = enc.Encode(msg) // flush is implicit for os.Stdout
}

func writeErr(enc *json.Encoder, errMsg string) {
	writeMsg(enc, &Response{Error: errMsg})
}

// ---------------------------------------------------------------------------
// Call — for callers that need to drive the protocol from the host side
// ---------------------------------------------------------------------------

// Call writes a request to w and reads the response from r.
// This is the host-side (xbot) helper; plugins use Run instead.
func Call(ctx context.Context, w io.Writer, r io.Reader, method string, params any) (*Response, error) {
	req := Request{Method: method}
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal request params: %w", err)
		}
		req.Params = data
	}
	if err := writeJSONLine(w, req); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxLineSize)
	if !sc.Scan() {
		return nil, fmt.Errorf("read response: EOF")
	}
	var resp Response
	if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &resp, nil
}

func writeJSONLine(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

// ---------------------------------------------------------------------------
// Context enricher helper
// ---------------------------------------------------------------------------

// HookResultAllow is a convenience constant.
var HookResultAllow = &HookResult{Decision: "allow"}

// HookResultDeny returns a deny HookResult with the given message.
func HookResultDeny(msg string) *HookResult {
	return &HookResult{Decision: "deny", Message: msg}
}

const maxLineSize = 1 * 1024 * 1024 // 1MB, matches plugin/json.go

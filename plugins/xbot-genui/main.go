// Command genui-plugin implements the xbot GenUI channel plugin.
//
// It declares the `display_html` tool for the `web` channel (with UI metadata)
// via the channel_tools protocol, and executes it: validating LLM-generated TSX
// and returning the code so xbot's ChannelToolBridge pushes it to the frontend
// (ui_code → genui message) and stores it in iteration history (Detail).
//
// Protocol (JSON lines over stdio, see plugin/examples/web-ui-demo):
//
//	xbot → plugin: {"method":"activate","params":{...}}
//	xbot → plugin: {"type":"channel_config","metadata":{...}}
//	xbot → plugin: {"id":"srv-N","method":"execute_tool","params":{...}}
//	xbot → plugin: {"id":"srv-N","method":"web_ui_action","params":{...}}
//	plugin → xbot: {"type":"channel_tools","tools":[...]}   (on channel_config)
//	plugin → xbot: {"id":"srv-N","result":{...}}            (RPC responses)
//
// The plugin is intentionally ZERO-dependency (stdlib only): the channel_tools
// protocol is plain JSON, so the binary builds standalone with `go build`
// and never imports the main xbot module (no version drift).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ─── Wire types (mirror the subset of plugin/protocol we need) ────────────

// toolDecl is a channel_tools declaration entry.
type toolDecl struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  []toolParam `json:"parameters"`
	Channels    []string    `json:"channels,omitempty"`
	UI          *uiDecl     `json:"ui,omitempty"`
}

type toolParam struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// uiDecl mirrors tools.UIDecl.
type uiDecl struct {
	Mode    string       `json:"mode,omitempty"`
	Param   string       `json:"param,omitempty"`
	Libs    []string     `json:"libs,omitempty"`
	Surface *surfaceDecl `json:"surface,omitempty"`
}

// surfaceDecl mirrors tools.UISurface — declares the UI result as a top-level
// panel (fancy header + collapsible + fullscreen), default-open.
type surfaceDecl struct {
	Kind        string `json:"kind,omitempty"`
	Title       string `json:"title,omitempty"`
	Collapsible bool   `json:"collapsible,omitempty"`
	Fullscreen  bool   `json:"fullscreen,omitempty"`
	DefaultOpen bool   `json:"default_open,omitempty"`
}

// rpcRequest is an inbound RPC from xbot ({"id","method","params"}).
type rpcRequest struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// typeMessage is a type-based (async) message from xbot (e.g. channel_config).
type typeMessage struct {
	Type     string          `json:"type"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// ─── display_html tool description (fancy GenUI) ───────────────────────────

const displayHTMLDesc = `Render an interactive UI for the user. The UI renders live in the chat as a streaming preview.

You write a single TSX module. React hooks (useState, useEffect, useMemo, useRef, useCallback, useContext, useReducer, useLayoutEffect, useId, useSyncExternalStore) are available. Standard HTML elements and Tailwind CSS classes work (use ` + "`dark:`" + ` variants for dark mode — the background adapts automatically). You may also use inline style={{...}} and <style>...</style> blocks.

CRITICAL RULES:
- Write one self-contained TSX module; no imports — React is available globally.
- Prefer standard HTML elements + Tailwind for styling; text must be legible in both light and dark mode.
- Avoid global side effects on document/window; keep the component self-contained.
- Reach visible markup early so the preview streams in progressively.
- Do NOT use export/import/module syntax. Write "function App() { ... }" directly. The runtime wraps your code automatically.

INTERACTION — two ways:
1. Agent callback: add data-action="..." (plus any data-* attributes you want passed back) to an element; a click routes the action to the agent (the agent sees 🖱️ [UI Action] <action> State: {<your data-*>}).
2. Local state: React useState/useEffect for pure client-side interactivity.

Example:
  function App() {
    const [n, setN] = useState(0)
    return (
      <div className="p-4">
        <button className="rounded bg-indigo-500 px-3 py-1 text-white" onClick={() => setN(n+1)}>count: {n}</button>
      </div>
    )
  }`

// ─── Main loop ─────────────────────────────────────────────────────────────

func main() {
	enc := json.NewEncoder(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var peek struct {
			ID     string `json:"id"`
			Method string `json:"method"`
			Type   string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &peek); err != nil {
			continue
		}

		switch {
		case peek.Method == "activate":
			handleActivate(enc)
		case peek.Method == "execute_tool":
			var req rpcRequest
			json.Unmarshal([]byte(line), &req)
			handleExecuteTool(enc, req)
		case peek.Method == "web_ui_action":
			// GenUI interactions are routed back to the agent loop by default
			// (genui_action → InjectAsyncMessage). We respond with a no-op so
			// xbot's routing falls through to the agent fallback.
			var req rpcRequest
			json.Unmarshal([]byte(line), &req)
			writeResult(enc, req.ID, map[string]any{"result": ""})
		case peek.Type == "channel_config":
			// Channel is live — declare our tools for the web channel.
			declareTools(enc)
		}
	}
}

func handleActivate(enc *json.Encoder) {
	writeJSON(enc, map[string]any{
		"result": "ok",
		"channel_provider": map[string]any{
			"name": "genui",
			"config_schema": []map[string]any{
				{"key": "enabled", "label": "Enable", "type": "toggle", "default_value": "true"},
			},
		},
	})
}

// declareTools sends the channel_tools declaration — display_html registered to
// the "web" channel with UI metadata (generic, metadata-driven per design doc §9).
func declareTools(enc *json.Encoder) {
	writeJSON(enc, map[string]any{
		"type": "channel_tools",
		"tools": []toolDecl{
			{
				Name:        "display_html",
				Description: displayHTMLDesc,
				Parameters: []toolParam{
					{Name: "code", Type: "string", Description: "Self-contained TSX module with default export App component. React hooks are available (no imports, no component library).", Required: true},
				},
				Channels: []string{"web"},
				UI: &uiDecl{
					Mode:  "genui",
					Param: "code",
					Libs:  []string{"echarts", "three", "motion"},
					// Surface: render the UI as a top-level panel (fancy header +
					// collapse + fullscreen, default-open) instead of being folded
					// into the normal tool list.
					Surface: &surfaceDecl{
						Kind:        "panel",
						Collapsible: true,
						Fullscreen:  true,
						DefaultOpen: true,
					},
				},
			},
		},
	})
}

// handleExecuteTool validates the LLM-generated TSX and returns it as ui_code
// so xbot's ChannelToolBridge pushes it to the frontend + stores it in history.
func handleExecuteTool(enc *json.Encoder, req rpcRequest) {
	var params struct {
		Name  string `json:"name"`
		Input string `json:"input"`
	}
	if len(req.Params) > 0 {
		json.Unmarshal(req.Params, &params)
	}
	if params.Name != "display_html" {
		writeResult(enc, req.ID, map[string]any{"content": fmt.Sprintf("unknown tool: %s", params.Name), "is_error": true})
		return
	}

	var args struct {
		Code string `json:"code"`
	}
	if params.Input != "" && params.Input != "{}" {
		if err := json.Unmarshal([]byte(params.Input), &args); err != nil {
			writeResult(enc, req.ID, map[string]any{"content": fmt.Sprintf("parse arguments: %v", err), "is_error": true})
			return
		}
	}
	if args.Code == "" {
		writeResult(enc, req.ID, map[string]any{"content": "code is required", "is_error": true})
		return
	}

	code := strings.TrimSpace(args.Code)
	code = stripMarkdownFences(code)

	// Validate: must contain an App component.
	if !strings.Contains(code, "App") {
		writeResult(enc, req.ID, map[string]any{"content": "code must define an App component (e.g. `export default function App()` or `function App()`)", "is_error": true})
		return
	}
	// Basic syntax validation (brace/paren balance).
	if err := validateSyntax(code); err != nil {
		writeResult(enc, req.ID, map[string]any{"content": fmt.Sprintf("syntax error: %v. Please fix and retry.", err), "is_error": true})
		return
	}
	// Empty render guard.
	if isEmptyRender(code) {
		writeResult(enc, req.ID, map[string]any{"content": "the App component renders nothing (returns null or an empty fragment). It must return visible JSX content.", "is_error": true})
		return
	}

	writeResult(enc, req.ID, map[string]any{
		"content":  fmt.Sprintf("🎨 UI rendered (%d chars)", len(code)),
		"is_error": false,
		"ui_code":  code,
	})
}

// ─── Validation helpers (migrated from the removed tools/display_html.go) ──

func stripMarkdownFences(code string) string {
	s := code
	if strings.HasPrefix(strings.TrimSpace(s), "```") {
		lines := strings.SplitN(s, "\n", 2)
		if len(lines) > 1 {
			s = lines[1]
		}
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}

func validateSyntax(code string) error {
	depth := 0
	inString := byte(0)
	inTemplate := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(code); i++ {
		ch := code[i]

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if ch == '*' && i+1 < len(code) && code[i+1] == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if inString != 0 {
			if ch == '\\' {
				i++
				continue
			}
			if ch == inString {
				inString = 0
			}
			continue
		}
		if inTemplate {
			if ch == '\\' {
				i++
				continue
			}
			if ch == '`' {
				inTemplate = false
			}
			continue
		}

		if ch == '/' && i+1 < len(code) {
			if code[i+1] == '/' {
				inLineComment = true
				i++
				continue
			}
			if code[i+1] == '*' {
				inBlockComment = true
				i++
				continue
			}
		}
		if ch == '"' || ch == '\'' {
			inString = ch
			continue
		}
		if ch == '`' {
			inTemplate = true
			continue
		}

		switch ch {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth < 0 {
				return fmt.Errorf("unexpected closing bracket '%c' at position %d", ch, i)
			}
		}
	}

	if depth != 0 {
		return fmt.Errorf("unclosed brackets (depth=%d) — check for missing ) ] }", depth)
	}
	return nil
}

func isEmptyRender(code string) bool {
	lines := strings.Split(code, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "return ") {
			ret := strings.TrimSpace(strings.TrimPrefix(trimmed, "return "))
			ret = strings.TrimRight(ret, ");")
			ret = strings.TrimSpace(ret)
			if ret == "null" || ret == "undefined" || ret == "false" || ret == "<></>" {
				return true
			}
		}
	}
	return false
}

// ─── stdout helpers ─────────────────────────────────────────────────────────

func writeJSON(enc *json.Encoder, v any) {
	_ = enc.Encode(v)
}

func writeResult(enc *json.Encoder, id string, result any) {
	if id == "" {
		writeJSON(enc, result)
		return
	}
	writeJSON(enc, map[string]any{"id": id, "result": result})
}

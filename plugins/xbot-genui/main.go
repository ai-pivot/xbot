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
	Mode  string   `json:"mode,omitempty"`
	Param string   `json:"param,omitempty"`
	Libs  []string `json:"libs,omitempty"`
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

const displayHTMLDesc = `Render an interactive React UI for the user. The UI renders live in the chat as a streaming preview.

You write a single TSX module with ` + "`export default function App()`" + ` as the default export. React hooks are available. Tailwind classes for styling (use ` + "`dark:`" + ` variants for dark mode — the background adapts automatically).

GLOBAL COMPONENT LIBRARY (window.XBOT_UI — use directly, no import needed):
- <XBOT_UI.Button variant="primary|ghost|outline|danger|success" onClick={...}>...</XBOT_UI.Button>
- <XBOT_UI.Card title="..." subtitle="..." actions={...}>...</XBOT_UI.Card>
- <XBOT_UI.Stat label="..." value={...} delta={0.12} trend="up|down|flat" icon={...}/>
- <XBOT_UI.Sparkline data={[1,5,3,8]} color="#22c55e"/>
- <XBOT_UI.Progress value={0.7} label="Training" color="bg-emerald-500"/>
- <XBOT_UI.Badge text="NEW" color="green|red|blue|amber|indigo|gray" dot/>
- <XBOT_UI.Table data={[{...}]} columns={[{key,label,render?}]} maxHeight={300}/>
- <XBOT_UI.Tabs tabs={[{key,label,content}]} defaultKey="..."/>
- <XBOT_UI.Modal open={...} onClose={...} title="..." width={480}>...</XBOT_UI.Modal>
- <XBOT_UI.Form fields={[{name,label,type,options?}]} onSubmit={(values)=>{...}} submitLabel="Save"/>
- <XBOT_UI.Toast show={...} text="Saved" kind="success|error|info"/>

CHARTS (declarative ECharts — pass an ECharts option object):
<XBOT_UI.Chart option={{ tooltip:{}, xAxis:{type:'category',data:[...]}, yAxis:{type:'value'}, series:[{type:'line'|'bar'|'pie', data:[...], smooth:true, areaStyle:{}}] }} height={280} />

3D (three.js scene — imperative inside useEffect):
const ref = XBOT_UI.useThreeScene((scene, THREE) => { scene.add(new THREE.Mesh(...)) })

ANIMATION (framer-motion):
<XBOT_UI.motion.div initial={{opacity:0,y:20}} animate={{opacity:1,y:0}} transition={{duration:0.5}}>...</XBOT_UI.motion.div>

ICONS (lucide subset): <XBOT_UI.Icon name="check" size={16}/>

INTERACTION — two ways:
1. Agent callback: data-action="save" data-* attributes → click is routed to the agent (agent sees 🖱️ [UI Action] save State: {...}).
2. Local state: React useState/useEffect for pure client-side interactivity.

RULES:
- Write a single TSX module with ` + "`export default function App()`" + `.
- Use Tailwind CSS classes for all styling. Background is white in light mode, slate-950 in dark. Text must be dark in light (text-gray-900) and light in dark (text-slate-100). Avoid hardcoded colors.
- No imports needed — React and XBOT_UI are available globally.
- Keep it self-contained; reach visible markup early so the preview streams in progressively.
- Example:
  export default function App() {
    const [n, setN] = useState(0)
    return (
      <div className="p-4">
        <XBOT_UI.Stat label="Clicks" value={n} />
        <XBOT_UI.Button variant="primary" onClick={() => setN(n+1)}>+1</XBOT_UI.Button>
        <XBOT_UI.Button variant="ghost" data-action="reset">Reset</XBOT_UI.Button>
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
					{Name: "code", Type: "string", Description: "TSX module with default export App component. Uses React hooks and the XBOT_UI component library.", Required: true},
				},
				Channels: []string{"web"},
				UI: &uiDecl{
					Mode:  "genui",
					Param: "code",
					Libs:  []string{"echarts", "three", "motion"},
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

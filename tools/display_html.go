package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xbot/llm"
)

// DisplayHTMLTool lets the agent render interactive HTML UI in the web channel.
// The HTML uses Tailwind CSS classes and {variable} interpolation.
// User interactions with data-action elements are routed back to the agent
// via the bgnotify pipeline (genui_action RPC → injectAsyncMessage).
type DisplayHTMLTool struct{}

func NewDisplayHTMLTool() *DisplayHTMLTool { return &DisplayHTMLTool{} }

func (t *DisplayHTMLTool) Name() string { return "display_html" }

func (t *DisplayHTMLTool) Description() string {
	return `Render an interactive React UI for the user. The UI renders live in the chat as a streaming preview.

You write a TSX module — a single React component with a default export. The component can use React hooks (useState, useEffect, useMemo, etc.) for state and interactivity.

Rules:
- Write a single TSX module with ` + "`export default function App()`" + ` as the default export.
- Use Tailwind CSS classes for all styling.
- Use React hooks for state: ` + "`const [count, setCount] = useState(0)`" + `
- For agent callbacks, use data-action="action_name" on any element. When clicked, the agent receives the action name and data-* attributes.
- No imports needed — React is available globally. Just write the component.
- Keep it self-contained: no external fetch, no imports other than React.
- Reach visible markup early so the preview streams in progressively.

Example:
export default function App() {
  const [count, setCount] = useState(0)
  return (
    <div className="max-w-sm mx-auto bg-white rounded-2xl shadow-lg p-6">
      <h2 className="text-xl font-bold text-gray-900">Counter</h2>
      <p className="text-3xl font-bold text-purple-600 mt-4">{count}</p>
      <div className="flex gap-2 mt-4">
       <button onClick={() => setCount(count - 1)} className="px-4 py-2 bg-gray-100 rounded-lg">-</button>
       <button onClick={() => setCount(count + 1)} className="px-4 py-2 bg-purple-100 rounded-lg">+</button>
       <button data-action="save" data-value={count} className="px-4 py-2 bg-blue-500 text-white rounded-lg">Save</button>
      </div>
    </div>
  )
}`
}

func (t *DisplayHTMLTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "code", Type: "string", Description: "TSX module with a default export App component. Uses React hooks and Tailwind CSS.", Required: true},
	}
}

func (t *DisplayHTMLTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args struct {
		Code string `json:"code"`
	}
	if input != "" && input != "{}" {
		if err := json.Unmarshal([]byte(input), &args); err != nil {
			return nil, fmt.Errorf("parse arguments: %w", err)
		}
	}
	if args.Code == "" {
		return NewErrorResult("code is required"), nil
	}

	code := strings.TrimSpace(args.Code)
	// Strip markdown fences if present
	code = stripMarkdownFences(code)

	// Validate: must contain a React component (function App or const App)
	if !strings.Contains(code, "App") {
		return NewErrorResult("code must define an App component (e.g. `export default function App()` or `function App()`)"), nil
	}

	// Basic syntax validation: check brace/paren balance
	if err := validateSyntax(code); err != nil {
		return NewErrorResult(fmt.Sprintf("syntax error: %v. Please fix and retry.", err)), nil
	}

	// Send the HTML to the web channel via SendFunc.
	// The frontend picks this up as a "genui" message type.
	if ctx.SendFunc != nil {
		meta := map[string]string{
			"genui":   "true",
			"channel": ctx.Channel,
			"chat_id": ctx.ChatID,
		}
		_ = ctx.SendFunc(ctx.Channel, ctx.ChatID, code, meta)
	}

	// Large HTML offload: write to file so agent callbacks can reference it.
	var sourceRef string
	if len(code) > 4096 {
		genuiDir := filepath.Join(ctx.WorkingDir, ".xbot", "genui")
		if err := os.MkdirAll(genuiDir, 0o755); err == nil {
			filename := fmt.Sprintf("genui_%s.html", generateID())
			fp := filepath.Join(genuiDir, filename)
			if err := os.WriteFile(fp, []byte(code), 0o644); err == nil {
				sourceRef = filepath.Join(".xbot", "genui", filename)
			}
		}
	}

	summary := fmt.Sprintf("🎨 UI rendered (%d chars)", len(code))
	if sourceRef != "" {
		summary += fmt.Sprintf(". Source: %s", sourceRef)
	}
	return NewResult(summary), nil
}

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

// validateSyntax checks basic brace/paren/bracket balance in the code.
// It skips strings, template literals, and comments to avoid false positives.
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
				i++ // skip next char
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

		// Check for comments
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

		// Check for strings/templates
		if ch == '"' || ch == '\'' {
			inString = ch
			continue
		}
		if ch == '`' {
			inTemplate = true
			continue
		}

		// Track brackets
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

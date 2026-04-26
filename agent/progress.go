package agent

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ProgressEvent Structured progress event for upper-layer consumption (e.g. Feishu card rendering).
type ProgressEvent struct {
	Lines      []string
	Structured *StructuredProgress
	Timestamp  time.Time
}

// FullText returns all progress lines joined into a single string.
// Consumers should use this instead of only accessing Lines[0].
func (e *ProgressEvent) FullText() string {
	if len(e.Lines) == 0 {
		return ""
	}
	return strings.Join(e.Lines, "\n")
}

// StructuredProgress holds structured progress info describing the Agent's current state.
type StructuredProgress struct {
	Phase            ProgressPhase
	Iteration        int
	ActiveTools      []ToolProgress
	CompletedTools   []ToolProgress
	ThinkingContent  string // assistant's text output (streaming, for display)
	ReasoningContent string // model's reasoning/thinking chain (reasoning_content field)
	TokenUsage       *TokenUsageSnapshot
	Todos            []TodoProgressItem

	// HistoryCompacted is set to true after context compression completes.
	// CLI uses this to rebuild its message list from session storage.
	HistoryCompacted bool
}

// ProgressPhase represents an Agent run phase.
type ProgressPhase string

const (
	quotePrefix                    = "> " // Markdown blockquote prefix
	PhaseThinking    ProgressPhase = "thinking"
	PhaseToolExec    ProgressPhase = "tool_exec"
	PhaseCompressing ProgressPhase = "compressing"
	PhaseRetrying    ProgressPhase = "retrying"
	PhaseDone        ProgressPhase = "done"
)

// ToolProgress holds the execution progress of a single tool.
type ToolProgress struct {
	Name      string
	Label     string
	Status    ToolStatus
	Elapsed   time.Duration
	Iteration int
	Summary   string
}

// ToolStatus represents tool execution status.
type ToolStatus string

const (
	ToolPending ToolStatus = "pending"
	ToolRunning ToolStatus = "running"
	ToolDone    ToolStatus = "done"
	ToolError   ToolStatus = "error"
)

// TodoProgressItem represents a single TODO item for progress display.
type TodoProgressItem struct {
	ID   int
	Text string
	Done bool
}

// TokenUsageSnapshot is a token usage snapshot.
type TokenUsageSnapshot struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CacheHitTokens   int64
}

// SubAgentProgressDetail carries SubAgent progress callback params with hierarchy info.
// Used in recursive SubAgent scenarios to propagate deep sub-agent progress to the top level.
type SubAgentProgressDetail struct {
	Path  []string // call chain: ["Gongbu", "ministry-works/audit"]
	Lines []string // progress content (all lines, newlines cleaned)
	Depth int      // nesting depth (0 = direct sub-agent)
}

// --- Helper functions ---

// flattenLines flattens Lines into actual lines (split by \n).
// Because notifyProgress joins progressLines into a single string as Lines[0],
// each Lines element may contain \n and must be split for correct processing.
func flattenLines(lines []string) []string {
	var result []string
	for _, line := range lines {
		if line == "" {
			continue
		}
		result = append(result, strings.Split(line, "\n")...)
	}
	return result
}

// progressTruncate truncates a string to max rune count; excess replaced with "…" (compact version).
// Automatically closes Markdown inline syntax markers at the truncation point (`、**、*、[text](、~~).
func progressTruncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes <= 1 {
		return "…"
	}
	truncated := string(runes[:maxRunes-1])
	return truncated + "…" + closeMarkdown(truncated)
}

// closeMarkdown scans for Markdown inline syntax and returns the closing suffix to append.
// Uses a simple state machine to track unclosed markers: backtick, **, *, ~~, [.
func closeMarkdown(s string) string {
	var (
		inCode     bool // inside inline code (`...`)
		boldOpen   bool // ** unclosed
		italicOpen bool // * unclosed
		strikeOpen bool // ~~ unclosed
		linkOpen   bool // [ unclosed
	)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if inCode {
			if r == '`' {
				inCode = false
			}
			continue
		}
		switch r {
		case '`':
			inCode = true
		case '*':
			// Look ahead: two consecutive * is bold
			if i+1 < len(runes) && runes[i+1] == '*' {
				boldOpen = !boldOpen
				i++ // Skip second *
			} else {
				italicOpen = !italicOpen
			}
		case '~':
			if i+1 < len(runes) && runes[i+1] == '~' {
				strikeOpen = !strikeOpen
				i++ // Skip second ~
			}
		case '[':
			linkOpen = true
		case ']':
			linkOpen = false
		}
	}
	var buf strings.Builder
	if inCode {
		buf.WriteByte('`')
	}
	if boldOpen {
		buf.WriteString("**")
	}
	if italicOpen {
		buf.WriteByte('*')
	}
	if linkOpen {
		buf.WriteString("](…)")
	}
	if strikeOpen {
		buf.WriteString("~~")
	}
	return buf.String()
}

// extractRoleName extracts the role name from the end of a path (strips / segments).
func extractRoleName(path []string) string {
	if len(path) == 0 {
		return ""
	}
	last := path[len(path)-1]
	if idx := strings.LastIndexByte(last, '/'); idx >= 0 {
		return last[idx+1:]
	}
	return last
}

// --- Indentation measurement and tree building ---

// countFullWidthIndent calculates the full-width space indentation level of a line (after removing "> " prefix).
// For rebuilding Sub-agent hierarchy tree from flat text lines.
func countFullWidthIndent(line string) int {
	for strings.HasPrefix(line, quotePrefix) {
		line = strings.TrimPrefix(line, quotePrefix)
	}
	count := 0
	for _, r := range line {
		switch r {
		case '　':
			count++
		case ' ', '\t', '│', '├', '└', '─':
			continue
		default:
			return count
		}
	}
	return count
}

// indexedChild childAgentStatus with indentation depth, for buildChildTree.
type indexedChild struct {
	depth int
	child childAgentStatus
}

// buildChildTree rebuilds a flat list into a nested tree based on indentation depth.
// Items at minimum indentation level are direct child nodes; deeper items recursively belong to the previous shallower node.
func buildChildTree(items []indexedChild) []childAgentStatus {
	if len(items) == 0 {
		return nil
	}

	minDepth := items[0].depth
	for _, it := range items[1:] {
		if it.depth < minDepth {
			minDepth = it.depth
		}
	}

	var result []childAgentStatus
	for i := 0; i < len(items); {
		if items[i].depth == minDepth {
			child := items[i].child
			j := i + 1
			var sub []indexedChild
			for j < len(items) && items[j].depth > minDepth {
				sub = append(sub, items[j])
				j++
			}
			if len(sub) > 0 {
				child.Children = buildChildTree(sub)
			}
			result = append(result, child)
			i = j
		} else {
			i++
		}
	}
	return result
}

// --- Sub-agent line identification and parsing ---

// childAgentStatus represents the state parsed from a Sub-agent line.
type childAgentStatus struct {
	Role     string             // Role name
	Status   string             // "🔄" / "✅" / "❌" / "⏳"
	Desc     string             // Short description
	Children []childAgentStatus // Nested Sub-agents (built by buildChildTree)
}

// isSubAgentLine checks if a line is a Sub-agent progress line.
// Supports three formats:
//  1. Tree format (test/passthrough scenarios):  "├─ 🔄 role: desc" / "└─ ✅ role:"
//  2. Quote format (actual runtime Sub-agent passthrough formatted lines): "> 🔄 role: desc" / "> 　✅ role"
//  3. Placeholder line format (Sub-agent initial placeholder): "> ⏳ SubAgent [role]..."
func isSubAgentLine(line string) bool {
	// Clean up reference prefix
	for strings.HasPrefix(line, quotePrefix) {
		line = strings.TrimPrefix(line, quotePrefix)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}

	// Tree format: starts with ├─ / └─ / │
	if strings.HasPrefix(line, "├─") || strings.HasPrefix(line, "└─") || strings.HasPrefix(line, "│") {
		return true
	}

	// Placeholder line format: ⏳ SubAgent [...]...
	if strings.HasPrefix(line, "⏳ SubAgent") {
		return true
	}

	// Reference format: starts with status emoji + text + colon
	line = strings.TrimLeft(line, "　 \t")
	return isStatusEmojiLine(line)
}

// isStatusEmojiLine checks if a line starts with a status emoji and contains a colon (characteristic of sub-agent formatted output).
// Note: ⏳ is not in this list — ⏳ is only used for tool placeholder lines (> ⏳ ToolName: args),
// Tool placeholder lines are handled by other branches of isSubAgentLine (dedicated ⏳ SubAgent [...] matching).
//
// To avoid misidentifying Tool results quoted in LLM output (e.g. "❌ Shell: cmd") as Sub-agent lines,
// the name before the colon needs to be checked by isPlausibleAgentRole.
func isStatusEmojiLine(line string) bool {
	for _, prefix := range []string{"🔄 ", "✅ ", "❌ "} {
		if strings.HasPrefix(line, prefix) {
			rest := line[len(prefix):]
			// Handle format with colon: "🔄 role: desc" or "✅ role:"
			if idx := strings.Index(rest, ":"); idx > 0 {
				candidate := strings.TrimSpace(rest[:idx])
				if isPlausibleAgentRole(candidate) {
					return true
				}
			}
			// Handle no-colon completion format: "✅ role" (legacy, no description)
			if strings.HasPrefix(line, "✅ ") && isPlausibleAgentRole(strings.TrimSpace(rest)) {
				return true
			}
		}
	}
	return false
}

// isPlausibleAgentRole checks if a name looks like a SubAgent role name rather than a tool name.
// SubAgent role name characteristics:
//   - All lowercase English + hyphens/underscores, e.g. "crown-prince", "ministry-works", "explore"
//   - Chinese role names, e.g. "刑部", "工部", "中书省"
//
// Tool name characteristics: capitalized English, e.g. "Shell", "Read", "FileCreate", "SubAgent".
// Also excludes paths (containing /) and other non-role-name patterns.
func isPlausibleAgentRole(name string) bool {
	if name == "" {
		return false
	}
	// Contains path separator → not a role name
	if strings.Contains(name, "/") {
		return false
	}
	runes := []rune(name)
	firstRune := runes[0]
	// First char is uppercase ASCII → tool name (Shell, Read, FileCreate, SubAgent, Grep, ...)
	if firstRune >= 'A' && firstRune <= 'Z' {
		return false
	}
	// First character is non-ASCII (Chinese, etc.) → role name
	if firstRune > 127 {
		return true
	}
	// Starts with ASCII lowercase → role name (e.g. "explore", "crown-prince")
	// But exclude sentences containing spaces
	if strings.Contains(name, " ") {
		return false
	}
	return true
}

// parseSubAgentLine parses a sub-agent progress line, extracting role name and status.
// Supports three input formats:
//  1. Tree format: "├─ 🔄 ministry-works: ⏳ Shell(ls) ..."
//  2. Quote format: "🔄 ministry-works: ⏳ Shell(ls) ..." or "　🔄 ministry-works: ⏳ Shell(ls)"
//  3. Placeholder line format: "⏳ SubAgent [ministry-works]..."
func parseSubAgentLine(line string) (childAgentStatus, bool) {
	// Clean up reference prefix
	for strings.HasPrefix(line, quotePrefix) {
		line = strings.TrimPrefix(line, quotePrefix)
	}

	// Clean up tree lines and full-width indentation
	line = strings.TrimLeft(line, "　 \t│├└─")
	line = strings.TrimSpace(line)
	if line == "" {
		return childAgentStatus{}, false
	}

	// Extract emoji status prefix
	status := "🔄"
	for _, s := range []string{"✅", "❌", "🔄"} {
		if strings.HasPrefix(line, s) {
			status = s
			line = strings.TrimPrefix(line, s)
			break
		}
	}
	line = strings.TrimSpace(line)

	// ⏳ may also be a status prefix (placeholder line "⏳ SubAgent [role]: task")
	if status == "🔄" && strings.HasPrefix(line, "⏳ ") {
		line = strings.TrimPrefix(line, "⏳ ")
		status = "⏳"
	}

	// Extract role name (part before the first colon)
	colonIdx := strings.Index(line, ":")
	if colonIdx <= 0 {
		// No colon: handle legacy "✅ role" format (completion without description).
		// Only accept ✅ with a plausible agent role name.
		if status == "✅" {
			role := strings.TrimSpace(line)
			if role != "" && isPlausibleAgentRole(role) {
				return childAgentStatus{Role: role, Status: status, Desc: ""}, true
			}
		}
		return childAgentStatus{}, false
	}

	role := strings.TrimSpace(line[:colonIdx])
	desc := strings.TrimSpace(line[colonIdx+1:])

	if role == "" {
		return childAgentStatus{}, false
	}

	// Clean up role name: if format is "SubAgent [actual-role]", extract the actual role name
	if strings.HasPrefix(role, "SubAgent [") && strings.HasSuffix(role, "]") {
		role = role[10 : len(role)-1]
	}

	// Defense: reject tool names (PascalCase) parsed as agent roles.
	// This prevents LLM content like "❌ Shell: cmd" from being misidentified.
	if !isPlausibleAgentRole(role) {
		return childAgentStatus{}, false
	}

	return childAgentStatus{Role: role, Status: status, Desc: desc}, true
}

// formatChildAgentsSummary formats multiple Sub-agent states into a compact single-line summary.
// Goal: clearly show how many Agents, their states, and concurrency relationships.
//
// Output examples:
//
//	"🔄 Gongbu(⏳ go version) · ✅ Xingbu · 🔄 Libu(💭)"
//	"✅ Gongbu · ✅ Xingbu · ✅ Libu"
//	"🔄×3 ⏳×2" (when exceeding 6, only show status summary)
func formatChildAgentsSummary(children []childAgentStatus, maxTotalRunes int) string {
	if len(children) == 0 {
		return ""
	}

	const (
		sep        = " · "
		descMax    = 20 // Max description length per Agent
		totalLimit = 6  // Only show status statistics when exceeding this count
	)

	if len(children) > totalLimit {
		// Too many, just count statuses
		running, done, failed, pending := 0, 0, 0, 0
		for _, c := range children {
			// Progress status lines without a role name are counted as running
			if c.Role == "" {
				running++
				continue
			}
			switch c.Status {
			case "✅":
				done++
			case "❌":
				failed++
			case "⏳":
				pending++
			default:
				running++
			}
		}
		parts := []string{}
		if running > 0 {
			parts = append(parts, fmt.Sprintf("🔄×%d", running))
		}
		if pending > 0 {
			parts = append(parts, fmt.Sprintf("⏳×%d", pending))
		}
		if done > 0 {
			parts = append(parts, fmt.Sprintf("✅×%d", done))
		}
		if failed > 0 {
			parts = append(parts, fmt.Sprintf("❌×%d", failed))
		}
		return strings.Join(parts, sep)
	}

	var parts []string
	for _, c := range children {
		if c.Role == "" {
			// Progress status lines without a role name (e.g. "💭 Thinking...")
			parts = append(parts, c.Desc)
			continue
		}
		if c.Desc != "" {
			shortDesc := progressTruncate(c.Desc, descMax)
			parts = append(parts, fmt.Sprintf("%s %s(%s)", c.Status, c.Role, shortDesc))
		} else {
			parts = append(parts, fmt.Sprintf("%s %s", c.Status, c.Role))
		}
	}

	result := strings.Join(parts, sep)
	return progressTruncate(result, maxTotalRunes)
}

// ExtractSubAgentTree parses Sub-agent hierarchy tree from ProgressEvent.Lines.
// Returns a flat list (each element with optional Children), for upper layer (e.g. web channel) to serialize as JSON.
// If Lines has no Sub-agent progress lines, returns nil.
func ExtractSubAgentTree(lines []string) []SubAgentNode {
	flat := flattenLines(lines)
	_, children := extractOwnAndChildProgress(flat)
	if len(children) == 0 {
		return nil
	}
	return convertChildTree(children)
}

// SubAgentNode serializable Sub-agent state node (for channel layer use).
type SubAgentNode struct {
	Role     string         `json:"role"`
	Status   string         `json:"status"` // "running" | "done" | "error" | "pending"
	Desc     string         `json:"desc,omitempty"`
	Children []SubAgentNode `json:"children,omitempty"`
}

// convertChildTree converts internal childAgentStatus to serializable SubAgentNode.
func convertChildTree(children []childAgentStatus) []SubAgentNode {
	if len(children) == 0 {
		return nil
	}
	result := make([]SubAgentNode, 0, len(children))
	for _, c := range children {
		node := SubAgentNode{
			Role:   c.Role,
			Status: emojiToStatus(c.Status),
			Desc:   c.Desc,
		}
		if len(c.Children) > 0 {
			node.Children = convertChildTree(c.Children)
		}
		result = append(result, node)
	}
	return result
}

func emojiToStatus(emoji string) string {
	switch emoji {
	case "✅":
		return "done"
	case "❌":
		return "error"
	case "⏳":
		return "pending"
	default:
		return "running"
	}
}

// extractOwnAndChildProgress separates current Agent's own progress from Sub-agent progress in flattened lines.
// Returns (ownLastLine, childStatuses).
//
// Sub-agent lines are rebuilt into nested tree (Children field) by full-width space indentation depth,
// thus preserving hierarchy when rendering at parent level.
//
// Separation rules:
//   - "> " prefix + status emoji + colon → Sub-agent passthrough formatted output (parsed as childAgentStatus)
//   - ├─ / └─ tree lines → Sub-agent passthrough tree lines (parse as childAgentStatus)
//   - "> ⏳ SubAgent [...]" placeholder line → Sub-agent initial state (parse as childAgentStatus)
//   - Other "> " prefix lines (e.g. "> 💭 Thinking...", tool result passthrough, etc.) → filtered out
//   - Other non-empty prefix lines → current Agent's own progress
//
// isToolCompletionLine checks if it's a tool completion line (e.g. "✅ Shell: go version (508ms)").
// Difference from Sub-agent completion line (e.g. "✅ ministry-works: done") is: tool completion line ends with elapsed time.
func isToolCompletionLine(line string) bool {
	// Clean up reference prefix
	for strings.HasPrefix(line, quotePrefix) {
		line = strings.TrimPrefix(line, quotePrefix)
	}
	line = strings.TrimLeft(line, "　 \t")
	// Tool completion line characteristic: ends with ) (elapsed time format like (508ms), (1.2s))
	if !strings.HasSuffix(line, ")") {
		return false
	}
	// Check for (number time_unit) pattern
	if idx := strings.LastIndex(line, "("); idx > 0 {
		suffix := line[idx:]
		if reToolDuration.MatchString(suffix) {
			return true
		}
	}
	return false
}

var reToolDuration = regexp.MustCompile(`^\(\d+(?:\.\d+)?(?:ms|s)\)$`)

func extractOwnAndChildProgress(flat []string) (string, []childAgentStatus) {
	var ownLines []string
	var indexed []indexedChild

	for _, line := range flat {
		if isSubAgentLine(line) {
			depth := countFullWidthIndent(line)
			if depth == 0 {
				// Lines without indentation need differentiation:
				// 1. Current Agent's tool completion line (e.g. "✅ Shell: cmd (508ms)") → skip
				// 2. Sub-agent placeholder line (e.g. "⏳ SubAgent [role]...") → keep
				if isToolCompletionLine(line) {
					continue
				}
			}
			if child, ok := parseSubAgentLine(line); ok {
				indexed = append(indexed, indexedChild{depth: depth, child: child})
			}
			continue
		}
		if strings.HasPrefix(line, quotePrefix) {
			continue
		}
		cleaned := strings.TrimSpace(line)
		if cleaned != "" {
			ownLines = append(ownLines, cleaned)
		}
	}

	ownLast := ""
	if len(ownLines) > 0 {
		ownLast = ownLines[len(ownLines)-1]
	}

	return ownLast, buildChildTree(indexed)
}

// --- Tree rendering ---

const (
	treeChildDescMax  = 40 // Tree child nodes description max rune count
	treeStatsLimit    = 6  // Sub-agents exceeding this count degrade to statistical summary
	treeInlineSummMax = 60 // Inline summary max rune count
	treeMaxDepth      = 2  // Tree rendering max recursion depth
)

// renderChildrenTree renders Sub-agent list as multi-line indented text.
// Each line starts with "> " (Feishu quote block compatible), uses full-width space "　" for levels (wrap-safe).
// Don't use tree line characters (├─/└─/│), to avoid broken visual structure after Feishu auto-wrapping.
//
// Output example (baseIndent="", currentDepth=0):
//
//	> 　🔄 Zhongshu: 💭 Thinking
//	> 　🔄 Shangshu: Dispatching to two ministries
//	> 　　🔄 Gongbu: ⚡ Shell(ls)
//	> 　　✅ Xingbu:
func renderChildrenTree(children []childAgentStatus, baseIndent string, currentDepth int) []string {
	if len(children) == 0 {
		return nil
	}

	childIndent := baseIndent + "　"

	// Too many Sub-agents → single-line statistical summary
	if len(children) > treeStatsLimit {
		summary := formatChildAgentsSummary(children, treeInlineSummMax)
		return []string{fmt.Sprintf("> %s%s", childIndent, summary)}
	}

	var lines []string
	for _, c := range children {
		if len(c.Children) > 0 && currentDepth < treeMaxDepth {
			// Has child nodes and within depth limit: recursive expansion
			if c.Desc != "" {
				lines = append(lines, fmt.Sprintf("> %s%s %s: %s", childIndent, c.Status, c.Role, progressTruncate(c.Desc, 30)))
			} else {
				lines = append(lines, fmt.Sprintf("> %s%s %s:", childIndent, c.Status, c.Role))
			}
			lines = append(lines, renderChildrenTree(c.Children, childIndent, currentDepth+1)...)
		} else if len(c.Children) > 0 {
			// Exceeds depth limit: child nodes inline summary
			summary := formatChildAgentsSummary(c.Children, treeInlineSummMax)
			if c.Desc != "" {
				lines = append(lines, fmt.Sprintf("> %s%s %s: %s %s", childIndent, c.Status, c.Role, progressTruncate(c.Desc, 20), summary))
			} else {
				lines = append(lines, fmt.Sprintf("> %s%s %s: %s", childIndent, c.Status, c.Role, summary))
			}
		} else {
			// Leaf child nodes
			if c.Desc != "" {
				lines = append(lines, fmt.Sprintf("> %s%s %s: %s", childIndent, c.Status, c.Role, progressTruncate(c.Desc, treeChildDescMax)))
			} else {
				lines = append(lines, fmt.Sprintf("> %s%s %s:", childIndent, c.Status, c.Role))
			}
		}
	}
	return lines
}

// --- Main formatting function ---

// formatSubAgentProgress formats SubAgent progress as text.
// Each SubAgent occupies one slot in the parent Agent's progressLines,
// outputs single line when no Sub-agents, multi-line indented tree when present (wrap-safe).
//
// Design goals:
//   - Users can clearly understand: how many Agents, nesting depth, what they're doing, which are concurrent
//   - All lines start with "> " (Feishu quote block format)
//   - Use full-width spaces for indentation levels (Feishu doesn't collapse, no vertical alignment dependency)
//
// Output format examples:
//
//	> 🔄 crown-prince: 💭 Thinking...                    (direct child agent, no indent)
//	> 🔄 crown-prince: ⏳ Shell(go test) ...           （Tool execution）
//	> ✅ crown-prince                                   (completed)
//	> 🔄 crown-prince: Dispatching                           (has child agents, multi-line)
//	> 　🔄 Shangshu: Dispatching to two ministries                                ├── 子Agent（depth=3）
//	> 　　🔄 Gongbu: ⚡ Shell(ls)                          │   └── 孙Agent（depth=4）
func formatSubAgentProgress(detail SubAgentProgressDetail) string {
	const maxContentRunes = 50

	flat := flattenLines(detail.Lines)
	ownLine, children := extractOwnAndChildProgress(flat)
	roleName := extractRoleName(detail.Path)
	// depth=2 means direct Sub-agent (no indentation needed), each deeper level adds one full-width space
	indentDepth := detail.Depth - 2
	if indentDepth < 0 {
		indentDepth = 0
	}
	indent := strings.Repeat("　", indentDepth)

	// 1. Running but no output: keep running state
	//    Agent may be in this state during LLM call gaps, when tools haven't produced output, or just after starting.
	//    Cannot mark as ✅ (done), otherwise CLI renderer will filter it out.
	if ownLine == "" && len(children) == 0 {
		return fmt.Sprintf("> %s🔄 %s:", indent, roleName)
	}

	// 2. Has sub-agents → multi-line indented tree
	if len(children) > 0 {
		var rootLine string
		if ownLine != "" {
			rootLine = fmt.Sprintf("> %s🔄 %s: %s", indent, roleName, progressTruncate(ownLine, maxContentRunes))
		} else {
			rootLine = fmt.Sprintf("> %s🔄 %s:", indent, roleName)
		}
		childLines := renderChildrenTree(children, indent, 0)
		return strings.Join(append([]string{rootLine}, childLines...), "\n")
	}

	// 3. Leaf node (no sub-agents) → single line
	ownLine = progressTruncate(ownLine, maxContentRunes)
	return fmt.Sprintf("> %s🔄 %s: %s", indent, roleName, ownLine)
}

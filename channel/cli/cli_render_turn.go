package cli

import (
	"fmt"
	"strings"
	"time"

	"xbot/protocol"

	"charm.land/lipgloss/v2"
)

// toolGeneratingHints maps tool names to human-friendly hint text shown
// when the LLM is streaming a tool call (before arguments finish).
var toolGeneratingHints = map[string]string{
	"Read":           "skimming…",
	"Grep":           "scanning…",
	"Glob":           "browsing…",
	"Shell":          "preparing…",
	"WebSearch":      "searching…",
	"Fetch":          "fetching…",
	"FileCreate":     "drafting…",
	"FileReplace":    "rewriting…",
	"SubAgent":       "deciding…",
	"SendMessage":    "wording…",
	"Cd":             "navigating…",
	"Worktree":       "branching…",
	"config":         "tuning…",
	"Cron":           "scheduling…",
	"TodoWrite":      "organizing…",
	"TodoList":       "checking…",
	"AskUser":        "asking…",
	"ChatHistory":    "recalling…",
	"DownloadFile":   "downloading…",
	"EventTrigger":   "watching…",
	"ManageTools":    "connecting…",
	"Skill":          "loading…",
	"context_edit":   "trimming…",
	"Edit":           "editing…",
	"Write":          "writing…",
	"offload_recall": "recalling…",
	"recall_masked":  "recalling…",
	"memory_write":   "remembering…",
	"memory_list":    "listing…",
	"task_read":      "reading…",
	"task_status":    "checking…",
	"task_kill":      "stopping…",
	"Logs":           "digging…",
}

// fallbackHints is randomly sampled (deterministically by tool name len mod)
// when a tool has no explicit entry in toolGeneratingHints.
var fallbackHints = []string{"proposing…", "planning…", "figuring out…", "deciding…"}

func toolGeneratingHint(name string) string {
	if hint, ok := toolGeneratingHints[name]; ok {
		return hint
	}
	return fallbackHints[len(name)%len(fallbackHints)]
}

// ---------------------------------------------------------------------------
// Unified Turn Renderer
// ---------------------------------------------------------------------------

type turnBlockKind int

const (
	turnBlockReasoning turnBlockKind = iota
	turnBlockContent
	turnBlockTools
	turnBlockPulse
)

type turnBlock struct {
	kind turnBlockKind
	text string
}

// renderTurnBody renders all iteration content for an assistant message.
func (m *cliModel) renderTurnBody(
	iterations []cliIterationSnapshot,
	liveProgress *protocol.ProgressEvent,
	contentWidth int,
	fallbackContent string,
) string {
	s := &m.styles
	var sb strings.Builder
	var lastKind turnBlockKind
	hasBlock := false

	for i := range iterations {
		iter := &iterations[i]

		if iter.Reasoning != "" {
			appendTurnBlock(&sb, &lastKind, &hasBlock, turnBlock{
				kind: turnBlockReasoning,
				text: m.renderReasoningBox(iter.Reasoning, contentWidth, s),
			})
		}

		if iter.Content != "" {
			appendTurnBlock(&sb, &lastKind, &hasBlock, turnBlock{
				kind: turnBlockContent,
				text: m.renderTurnContent(iter.Content, contentWidth),
			})
		}

		if len(iter.Tools) > 0 {
			appendTurnBlock(&sb, &lastKind, &hasBlock, turnBlock{
				kind: turnBlockTools,
				text: m.renderToolTags(iter.Tools, contentWidth, s),
			})
		}
	}

	if liveProgress != nil {
		for _, block := range m.liveIterationBlocks(liveProgress, contentWidth, fallbackContent) {
			appendTurnBlock(&sb, &lastKind, &hasBlock, block)
		}
	} else if fallbackContent != "" {
		// Idle state: render the final assistant content after iterations.
		// Dedup: if any iteration already rendered the same text (via
		// Content → iter.Content), skip the fallback to avoid
		// duplication. iter.Content carries the assistant's reply text
		// (StructuredProgress.Content, which is the actual response
		// text, NOT reasoning — the field name is historical).
		// Exact match only: fallbackContent (msg.content) and iter.Content
		// originate from the same LLM response, so they should be identical.
		alreadyRendered := false
		for i := range iterations {
			if strings.TrimSpace(iterations[i].Content) == strings.TrimSpace(fallbackContent) {
				alreadyRendered = true
				break
			}
		}
		if !alreadyRendered {
			appendTurnBlock(&sb, &lastKind, &hasBlock, turnBlock{
				kind: turnBlockContent,
				text: m.renderTurnContent(fallbackContent, contentWidth),
			})
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

func appendTurnBlock(sb *strings.Builder, lastKind *turnBlockKind, hasBlock *bool, block turnBlock) {
	text := cleanTurnBlockText(block.text)
	if text == "" {
		return
	}

	if !*hasBlock {
		// First block starts immediately; renderReasoningBox includes a leading
		// newline for historical callers, so normalize text before appending.
	} else if block.kind == turnBlockPulse || *lastKind == block.kind {
		sb.WriteString("\n")
	} else {
		sb.WriteString("\n\n")
	}
	sb.WriteString(text)
	*lastKind = block.kind
	*hasBlock = true
}

func cleanTurnBlockText(text string) string {
	return strings.TrimRight(strings.TrimLeft(text, "\n"), "\n")
}

// renderTurnContent renders markdown through glamour.
func (m *cliModel) renderTurnContent(text string, width int) string {
	if width < 20 {
		width = 20
	}
	preprocessed := renderMermaidBlocks(text, width)
	preprocessed = renderMathBlocks(preprocessed, width)
	rendered, err := m.renderer.Render(preprocessed)
	if err != nil {
		return text
	}
	return strings.TrimSpace(rendered)
}

// renderToolTags renders compact dot-separated tool badges with full labels.
//
//	· Shell: cd /home/user/... ✓  · Read ✓
func (m *cliModel) renderToolTags(tools []protocol.ToolProgress, width int, s *cliStyles) string {
	maxLabelW := width * 2 / 3
	if maxLabelW < 20 {
		maxLabelW = 20
	}
	var lines []string
	for _, tool := range tools {
		label := oneLineToolLabel(tool.Label)
		if label == "" {
			label = oneLineToolLabel(tool.Name)
		}
		label = truncateToWidth(label, maxLabelW)
		var tag string
		switch tool.Status {
		case "generating":
			frame := splashFrames[m.ticker.frame%len(splashFrames)]
			hint := toolGeneratingHint(tool.Name)
			if tool.GenChars > 0 {
				hint += " " + s.ProgressElapsed.Render(formatCharCount(tool.GenChars))
			}
			tag = s.ProgressRunning.Render(frame+" "+label) + " " + s.ProgressRunning.Render(hint)
		case "error":
			tag = s.ProgressError.Render("✗ " + label)
			if tool.Elapsed > 0 {
				tag += " " + s.ProgressElapsed.Render(formatElapsed(tool.Elapsed))
			}
		case "done":
			tag = s.ProgressDone.Render("✓ " + label)
			if tool.Elapsed > 0 {
				tag += " " + s.ProgressElapsed.Render(formatElapsed(tool.Elapsed))
			}
		default:
			tag = s.ProgressRunning.Render("● " + label)
		}
		lines = append(lines, "  "+s.ProgressDim.Render("·")+" "+tag)
	}
	return strings.Join(lines, "\n")
}

// renderReasoningBox renders reasoning in an always-expanded box:
//
//	╭ Reasoning ──────────────────────────────╮
//	│ reasoning text line 1                   │
//	│ reasoning text line 2                   │
//	╰─────────────────────────────────────────╯
func (m *cliModel) renderReasoningBox(
	reasoning string,
	width int,
	s *cliStyles,
) string {
	if reasoning == "" {
		return ""
	}

	lines := strings.Split(strings.TrimSpace(reasoning), "\n")
	innerW := width - 4 // "│ " + " │"
	if innerW < 20 {
		innerW = 20
	}

	var sb strings.Builder
	label := " Reasoning "
	labelW := lipgloss.Width(label)
	dashCount := innerW - labelW
	if dashCount < 3 {
		dashCount = 3
	}
	sb.WriteString("\n")
	sb.WriteString(s.ProgressDim.Render("╭"))
	sb.WriteString(s.TextSecondarySt.Render(label))
	sb.WriteString(s.ProgressDim.Render(strings.Repeat("─", dashCount) + "╮"))
	sb.WriteString("\n")

	for _, line := range lines {
		wrapped := hardWrapRunes(line, innerW-2)
		for _, wl := range strings.Split(wrapped, "\n") {
			visW := lipgloss.Width(wl)
			pad := innerW - 2 - visW
			if pad < 0 {
				pad = 0
			}
			sb.WriteString(s.ProgressDim.Render("│ "))
			sb.WriteString(s.TextMutedSt.Render(wl))
			sb.WriteString(strings.Repeat(" ", pad))
			sb.WriteString(s.ProgressDim.Render(" │"))
			sb.WriteString("\n")
		}
	}

	sb.WriteString(s.ProgressDim.Render("╰" + strings.Repeat("─", innerW) + "╯"))
	return sb.String()
}

// renderLiveIteration renders the in-progress iteration.
func (m *cliModel) renderLiveIteration(p *protocol.ProgressEvent, width int, fallbackContent string) string {
	return renderTurnBlocks(m.liveIterationBlocks(p, width, fallbackContent))
}

func renderTurnBlocks(blocks []turnBlock) string {
	var sb strings.Builder
	var lastKind turnBlockKind
	hasBlock := false
	for _, block := range blocks {
		appendTurnBlock(&sb, &lastKind, &hasBlock, block)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func firstTurnBlockKind(blocks []turnBlock) (turnBlockKind, bool) {
	for _, block := range blocks {
		if cleanTurnBlockText(block.text) != "" {
			return block.kind, true
		}
	}
	return 0, false
}

func lastIterationBlockKind(iterations []cliIterationSnapshot) (turnBlockKind, bool) {
	for i := len(iterations) - 1; i >= 0; i-- {
		iter := iterations[i]
		if len(iter.Tools) > 0 {
			return turnBlockTools, true
		}
		if iter.Content != "" {
			return turnBlockContent, true
		}
		if iter.Reasoning != "" {
			return turnBlockReasoning, true
		}
	}
	return 0, false
}

// firstIterationBlockKind returns the kind of the first non-empty block
// across a slice of iterations, scanning in forward order.
// Priority order is intentionally reversed from lastIterationBlockKind:
// last cares what an iteration ENDED with (Tools→Thinking→Reasoning, heaviest last),
// first cares what an iteration STARTED with (Reasoning→Thinking→Tools, lightest first).
// This asymmetry ensures correct separator insertion at the boundary between groups.
func firstIterationBlockKind(iterations []cliIterationSnapshot) (turnBlockKind, bool) {
	for _, iter := range iterations {
		if iter.Reasoning != "" {
			return turnBlockReasoning, true
		}
		if iter.Content != "" {
			return turnBlockContent, true
		}
		if len(iter.Tools) > 0 {
			return turnBlockTools, true
		}
	}
	return 0, false
}

func needsTurnBlockSeparator(prev, next turnBlockKind) bool {
	return next != turnBlockPulse && prev != next
}

func (m *cliModel) liveIterationBlocks(p *protocol.ProgressEvent, width int, fallbackContent string) []turnBlock {
	s := &m.styles
	var blocks []turnBlock
	hasSpinner := false

	if p.Phase == "compressing" {
		hasSpinner = true
		frame := diamondPulseFrames[m.ticker.frame%len(diamondPulseFrames)]
		blocks = append(blocks, turnBlock{
			kind: turnBlockPulse,
			text: "  " + s.ProgressRunning.Render(frame) + " " + s.ProgressRunning.Render(m.locale.StatusCompressing),
		})
	}

	// Prefer ReasoningStreamContent (streaming, real-time) but fall back to
	// structured Reasoning (final, set by recordAssistantMsg). Structured
	// progress events sent during tool execution do NOT carry
	// ReasoningStreamContent — only Reasoning. Without this fallback, the
	// reasoning box flickers: visible during streaming, disappears when the
	// first structured event replaces m.progressState.current (losing
	// ReasoningStreamContent), then reappears when carryForwardProgressState
	// restores it or when the iteration is snapshotted.
	reasoningContent := p.ReasoningStreamContent
	if reasoningContent == "" {
		reasoningContent = p.Reasoning
	}
	if reasoningContent != "" {
		hasSpinner = true
		blocks = append(blocks, turnBlock{
			kind: turnBlockReasoning,
			text: m.renderReasoningBox(reasoningContent, width, s),
		})
	}

	streamContent := p.StreamContent
	if streamContent != "" || fallbackContent != "" {
		hasSpinner = true
	}
	displayContent := streamContent
	if displayContent == "" {
		displayContent = p.Content
	}
	if displayContent == "" {
		displayContent = fallbackContent
	}
	if displayContent != "" {
		blocks = append(blocks, turnBlock{
			kind: turnBlockContent,
			text: m.renderTurnContent(displayContent, width),
		})
	}

	// Combine StreamingTools (generating), ActiveTools (active/done/error), and CompletedTools.
	// Deduplicate by Name+Label to prevent the same tool appearing twice
	// when it transitions across phases (generating → active → done → completed).
	var tools []protocol.ToolProgress
	seen := make(map[string]bool)
	addTool := func(t protocol.ToolProgress) {
		// Generating tools may have the same Name+Label (args still streaming,
		// no label yet). Don't deduplicate them — each is a distinct tool call.
		if t.Status == "generating" {
			tools = append(tools, t)
			return
		}
		key := t.Name + "\x00" + t.Label
		if seen[key] {
			return
		}
		seen[key] = true
		tools = append(tools, t)
	}
	// StreamingTools: LLM is still generating tool call arguments. These appear
	// before any structured ActiveTools — show them first with a distinct spinner.
	for _, tool := range p.StreamingTools {
		addTool(tool)
		hasSpinner = true
	}
	for _, tool := range p.ActiveTools {
		// "pending" = tool is queued (waiting for a serial predecessor to finish).
		// Without showing pending tools, the CLI oscillates between showing
		// 3 (generating) → 1 (only running) → 2 → 3 as serial execution progresses.
		if tool.Status == "running" || tool.Status == "active" || tool.Status == "done" || tool.Status == "error" || tool.Status == "pending" {
			addTool(tool)
		}
		if tool.Status == "running" || tool.Status == "active" {
			hasSpinner = true
		}
	}
	for _, tool := range p.CompletedTools {
		addTool(tool)
	}

	if len(tools) > 0 {
		blocks = append(blocks, turnBlock{
			kind: turnBlockTools,
			text: m.renderLiveToolTags(tools, width),
		})
	}

	if len(p.SubAgents) > 0 {
		var treeSb strings.Builder
		m.renderSubAgentTree(&treeSb, p.SubAgents, "", width)
		if tree := strings.TrimRight(treeSb.String(), "\n"); tree != "" {
			hasSpinner = true
			blocks = append(blocks, turnBlock{kind: turnBlockTools, text: tree})
		}
	}

	if !hasSpinner {
		frame := diamondPulseFrames[m.ticker.frame%len(diamondPulseFrames)]
		blocks = append(blocks, turnBlock{kind: turnBlockPulse, text: "  " + s.ProgressRunning.Render(frame)})
	}

	return blocks
}

func (m *cliModel) renderLiveToolTags(tools []protocol.ToolProgress, width int) string {
	s := &m.styles
	maxLabelW := width * 2 / 3
	if maxLabelW < 20 {
		maxLabelW = 20
	}

	var sb strings.Builder
	for _, tool := range tools {
		label := oneLineToolLabel(tool.Label)
		if label == "" {
			label = oneLineToolLabel(tool.Name)
		}
		label = truncateToWidth(label, maxLabelW)
		switch tool.Status {
		case "generating":
			frame := splashFrames[m.ticker.frame%len(splashFrames)]
			hint := toolGeneratingHint(tool.Name)
			if tool.GenChars > 0 {
				hint = hint + " " + s.ProgressElapsed.Render(formatCharCount(tool.GenChars))
			}
			fmt.Fprintf(&sb, "  %s %s %s %s\n",
				s.ProgressDim.Render("·"),
				s.ProgressRunning.Render(frame),
				s.ProgressRunning.Render(label),
				s.ProgressRunning.Render(hint))
		case "error":
			sb.WriteString("  ")
			sb.WriteString(s.ProgressDim.Render("·"))
			sb.WriteString(" ")
			sb.WriteString(s.ProgressError.Render("✗ " + label))
			if tool.Elapsed > 0 {
				sb.WriteString(" ")
				sb.WriteString(s.ProgressElapsed.Render(formatElapsed(tool.Elapsed)))
			}
			sb.WriteString("\n")
		case "pending":
			// Queued tool: waiting for a serial predecessor to finish.
			// Dimmed with ○ to distinguish from actively running (●).
			fmt.Fprintf(&sb, "  %s %s %s\n",
				s.ProgressDim.Render("·"),
				s.TextMutedSt.Render("○ "+label),
				s.TextMutedSt.Render("queued"))
		case "done":
			sb.WriteString("  ")
			sb.WriteString(s.ProgressDim.Render("·"))
			sb.WriteString(" ")
			sb.WriteString(s.ProgressDone.Render("✓ " + label))
			if tool.Elapsed > 0 {
				sb.WriteString(" ")
				sb.WriteString(s.ProgressElapsed.Render(formatElapsed(tool.Elapsed)))
			}
			sb.WriteString("\n")
		default: // running/active
			var elapsedMs int64
			if !tool.StartedAt.IsZero() {
				elapsedMs = time.Since(tool.StartedAt).Milliseconds()
			} else {
				elapsedMs = tool.Elapsed
			}
			elapsed := formatElapsed(elapsedMs)
			frame := orbitFrames[m.ticker.frame%len(orbitFrames)]
			fmt.Fprintf(&sb, "  %s %s %s %s\n",
				s.ProgressDim.Render("·"),
				s.ProgressRunning.Render(frame),
				s.ProgressRunning.Render(label),
				s.ProgressElapsed.Render(elapsed))
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

func oneLineToolLabel(label string) string {
	return strings.Join(strings.Fields(label), " ")
}

// renderProgressBlock is a no-op: all progress rendering is now handled
// inline by renderTurnBody / renderLiveIteration in the streaming message.
func (m *cliModel) renderProgressBlock() string {
	return ""
}

// renderSubAgentTree renders nested sub-agents with indentation.
// Only renders running/pending agents — completed or errored ones are already
// captured in the tool summary and shouldn't linger in the progress panel.
//
// Uses a prefix-based approach instead of depth-based: each level appends
// "┊   " or "    " to the prefix depending on whether the parent was the last
// sibling. This avoids spurious vertical lines after a └── branch.
func (m *cliModel) renderSubAgentTree(sb *strings.Builder, agents []protocol.SubAgentInfo, prefix string, maxWidth int) {
	for i, sa := range agents {
		if sa.Status == "done" || sa.Status == "error" {
			continue
		}
		isLast := i == len(agents)-1
		connector := "└── "
		if !isLast {
			connector = "├── "
		}
		icon := m.ticker.viewFrames(waveFrames)
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(RoleColor(sa.Role)))
		switch sa.Status {
		case "error":
			icon = "✗"
			style = m.styles.ProgressError
		}
		roleText := sa.Role
		if sa.Instance != "" {
			roleText = sa.Role + " [" + sa.Instance + "]"
		}
		line := fmt.Sprintf("%s%s%s %s", prefix, connector, icon, roleText)
		if sa.Desc != "" {
			// Only add description if there's room — never exceed maxWidth.
			overhead := lipgloss.Width(line) + 2 // +2 for ": "
			descW := maxWidth - overhead
			if descW > 0 {
				line += ": " + truncateToWidth(strings.ReplaceAll(strings.ReplaceAll(sa.Desc, "\n", " "), "\r", ""), descW)
			}
		}
		sb.WriteString(style.Render(line))
		sb.WriteString("\n")
		if len(sa.Children) > 0 {
			childPrefix := prefix
			if isLast {
				childPrefix += "    "
			} else {
				childPrefix += "┊   "
			}
			m.renderSubAgentTree(sb, sa.Children, childPrefix, maxWidth)
		}
	}
}

// renderHelpPanel 渲染格式化的帮助面板（第 4 轮）。
func (m *cliModel) renderHelpPanel() string {
	contentWidth := m.chatWidth() - 4
	if contentWidth < 40 {
		contentWidth = 40
	}

	// §20 使用缓存样式
	s := &m.styles
	titleStyle := s.HelpTitle
	cmdStyle := s.HelpCmd
	descStyle := s.HelpDesc
	groupStyle := s.HelpGroup
	keyStyle := s.HelpKey
	panelStyle := s.HelpPanel.Width(contentWidth)

	var sb strings.Builder
	sb.WriteString(titleStyle.Render(m.locale.HelpTitle))
	sb.WriteString("\n")

	sb.WriteString(groupStyle.Render(m.locale.HelpCommandsTitle))
	sb.WriteString("\n")
	for _, c := range m.locale.HelpCmds {
		sb.WriteString("  " + cmdStyle.Render(c.Cmd) + " " + descStyle.Render(c.Desc))
		sb.WriteString("\n")
	}

	sb.WriteString(groupStyle.Render(m.locale.HelpShortcutsTitle))
	sb.WriteString("\n")
	for _, k := range m.locale.HelpKeys {
		sb.WriteString("  " + keyStyle.Render(k.Key) + " " + descStyle.Render(k.Desc))
		sb.WriteString("\n")
	}

	return panelStyle.Render(sb.String())
}

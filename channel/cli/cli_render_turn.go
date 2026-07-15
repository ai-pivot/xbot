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
		blocks := m.liveIterationBlocks(liveProgress, contentWidth, fallbackContent)
		// Check whether any previous iteration had tools. When the live
		// iteration is completely empty (no tools, content, reasoning)
		// but a previous iteration had completed tools, we are in a
		// transition gap between iterations — not a "thinking" gap.
		// In that case suppress the idle pulse spinner.
		prevHadTools := false
		for i := range iterations {
			if len(iterations[i].Tools) > 0 {
				prevHadTools = true
				break
			}
		}
		if prevHadTools && len(blocks) == 1 && blocks[0].kind == turnBlockPulse {
			// The only block is a pulse — drop it.
		} else {
			for _, block := range blocks {
				appendTurnBlock(&sb, &lastKind, &hasBlock, block)
			}
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
		frame := splashFrames[m.ticker.frame%len(splashFrames)]
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
		// Typewriter optimization: cache renderReasoningBox output. Only
		// re-render when the raw content or width changes. On unchanged
		// Deferred rendering: same logic as content — only re-render when
		// typewriter catches up to the previously rendered range.
		var renderedReasoning string
		totalRwRunes := len([]rune(reasoningContent))

		needRender := false
		if m.rc.liveReasoningRendered == "" {
			needRender = true
		} else if reasoningContent == m.rc.liveReasoningKey && width == m.rc.liveReasoningWidth {
			needRender = false
		} else if m.progressState.rwVisible == 0 ||
			m.progressState.rwVisible >= m.rc.liveReasoningTotal {
			needRender = true
		}

		if needRender {
			renderedReasoning = m.renderReasoningBox(reasoningContent, width, s)
			m.rc.liveReasoningRendered = renderedReasoning
			m.rc.liveReasoningKey = reasoningContent
			m.rc.liveReasoningWidth = width
			m.rc.liveReasoningVisN = 0
			m.rc.liveReasoningTotal = totalRwRunes
		} else {
			renderedReasoning = m.rc.liveReasoningRendered
		}

		// Apply typewriter truncation for reasoning.
		// Only apply when showing STREAMING reasoning (ReasoningStreamContent).
		if p.ReasoningStreamContent != "" && totalRwRunes > 0 &&
			m.progressState.rwVisible > 0 && m.progressState.rwVisible < totalRwRunes {
			if m.rc.liveReasoningVisN == 0 {
				m.rc.liveReasoningVisN = countANSIVisibleRunes(renderedReasoning)
			}
			if m.rc.liveReasoningVisN > 0 && m.rc.liveReasoningTotal > 0 {
				targetVis := m.progressState.rwVisible * m.rc.liveReasoningVisN / m.rc.liveReasoningTotal
				if targetVis < m.rc.liveReasoningVisN {
					renderedReasoning = truncateANSIVisible(renderedReasoning, targetVis)
				}
			}
		}
		blocks = append(blocks, turnBlock{
			kind: turnBlockReasoning,
			text: renderedReasoning,
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
		// Deferred glamour rendering: only re-render when the typewriter
		// has caught up to (or beyond) the content that was last rendered.
		//
		// Stream arrivals change StreamContent, but if twVisible hasn't
		// reached the end of the previously rendered content, the old
		// glamour output still covers everything the typewriter needs to
		// show. We just truncate the old output — zero glamour cost.
		//
		// This decouples glamour frequency from stream frequency:
		//   - Fast API (20ms/chunk): glamour runs at typewriter rate (50ms),
		//     not stream rate (20ms) — 2.5x fewer glamour calls.
		//   - Slow API (500ms/chunk): typewriter always catches up between
		//     chunks, so glamour runs once per chunk — same as before.
		var renderedContent string
		totalTwRunes := len([]rune(streamContent))

		needRender := false
		if m.rc.liveContentRendered == "" {
			needRender = true
		} else if displayContent == m.rc.liveContentKey && width == m.rc.liveContentWidth {
			// Content unchanged — pure cache hit.
			needRender = false
		} else if m.progressState.twVisible == 0 ||
			m.progressState.twVisible >= m.rc.liveContentTotal {
			// twVisible=0: typewriter not active, must render to show full content.
			// twVisible >= liveContentTotal: typewriter caught up, need new content.
			needRender = true
		}
		// else: content changed but typewriter is still within the old
		// rendered range. Old glamour output covers everything we need to
		// show. No re-render needed — just truncate differently.

		if needRender {
			renderedContent = m.renderTurnContent(displayContent, width)
			m.rc.liveContentRendered = renderedContent
			m.rc.liveContentKey = displayContent
			m.rc.liveContentWidth = width
			m.rc.liveContentVisN = 0 // lazy: computed on first truncation
			m.rc.liveContentTotal = totalTwRunes
		} else {
			renderedContent = m.rc.liveContentRendered
		}

		// Apply typewriter truncation.
		// Proportional mapping uses liveContentTotal (the rune count of the
		// content that was actually rendered), NOT the current StreamContent
		// length — the glamour output corresponds to liveContentTotal.
		if totalTwRunes > 0 && m.progressState.twVisible > 0 &&
			m.progressState.twVisible < totalTwRunes {
			if m.rc.liveContentVisN == 0 {
				m.rc.liveContentVisN = countANSIVisibleRunes(renderedContent)
			}
			if m.rc.liveContentVisN > 0 && m.rc.liveContentTotal > 0 {
				targetVis := m.progressState.twVisible * m.rc.liveContentVisN / m.rc.liveContentTotal
				if targetVis < m.rc.liveContentVisN {
					renderedContent = truncateANSIVisible(renderedContent, targetVis)
				}
			}
		}
		blocks = append(blocks, turnBlock{
			kind: turnBlockContent,
			text: renderedContent,
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

	// Suppress pulse when any tools are present. The pulse spinner is an
	// "idle indicator" for the thinking phase (no tools, no content). When
	// tools exist in ANY status (pending, running, done, error, completed),
	// the iteration is actively working — showing the pulse adds a spurious
	// line that appears/disappears as tools transition between states.
	//
	// Without this, sequential tool execution causes height jitter:
	//   Tool A running, B pending → hasSpinner=true (running) → no pulse → N lines
	//   Tool A done, B pending    → hasSpinner=false          → pulse    → N+1 lines
	//   Tool A done, B running   → hasSpinner=true (running) → no pulse → N lines
	//   Tool A done, B done      → hasSpinner=false          → pulse    → N+1 lines
	// This H→H+1→H oscillation is the visible jitter.
	if len(tools) > 0 {
		hasSpinner = true
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

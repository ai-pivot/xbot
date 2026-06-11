package channel

import (
	"fmt"
	"strings"

	"xbot/protocol"

	"charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// Unified Turn Renderer
// ---------------------------------------------------------------------------
//
// One assistant message per turn. All iterations render inline with
// consistent styling for both busy and idle states.
//
// Visual hierarchy (per iteration):
//
//   ┊  Content text rendered as markdown...
//   ┊  ┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄
//   ┊  · Shell ✓  · Read ✓  · FileReplace ✓
//   ┊
//   ┊  ▸ Reasoning (8 lines) ─────────────────
//   ┊
//   ┊  ────────────────────────────────────────   ← iteration divider
//   ┊
//   ┊  Next iteration content...
//   ┊  🔄 Grep ● 2.1s                           ← live tool
//   ┊    └── explore [mem-1]: searching...       ← SubAgent tree
// ---------------------------------------------------------------------------

// renderTurnBody renders all iteration content for an assistant message.
// busy: uses liveProgress + iterationHistory
// idle: uses baked iterations from the message
//
// Output does NOT include guide prefix — caller adds "┊ " per line.
func (m *cliModel) renderTurnBody(
	iterations []cliIterationSnapshot,
	liveProgress *protocol.ProgressEvent,
	contentWidth int,
) string {
	s := &m.styles
	var sb strings.Builder

	// Render each completed iteration.
	for i := range iterations {
		iter := &iterations[i]
		if i > 0 {
			sb.WriteString("\n")
			sb.WriteString(s.ProgressDim.Render(strings.Repeat("─", contentWidth)))
			sb.WriteString("\n\n")
		}

		// 1. Iteration content (Thinking = text reply).
		if iter.Thinking != "" {
			rendered := m.renderTurnContent(iter.Thinking, contentWidth)
			sb.WriteString(rendered)
			sb.WriteString("\n")
		}

		// 2. Reasoning (collapsible, above tool tags for better reading flow).
		if iter.Reasoning != "" {
			sb.WriteString("\n")
			sb.WriteString(m.renderReasoningBox(iter.Reasoning, contentWidth, s, false))
			sb.WriteString("\n")
		}

		// 3. Tool tags — subtle pill row.
		if len(iter.Tools) > 0 {
			sb.WriteString(m.renderToolTags(iter.Tools, contentWidth, s))
			sb.WriteString("\n")
		}
	}

	// Render the current live iteration (if any).
	if liveProgress != nil {
		if len(iterations) > 0 {
			sb.WriteString("\n")
			sb.WriteString(s.ProgressDim.Render(strings.Repeat("─", contentWidth)))
			sb.WriteString("\n\n")
		}
		sb.WriteString(m.renderLiveIteration(liveProgress, contentWidth))
	}

	return strings.TrimRight(sb.String(), "\n")
}

// renderTurnContent renders markdown text through glamour.
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

// renderToolTags renders a row of compact tool badges.
//
//	· Shell ✓   · Read ✓   · FileReplace ✓
//
// Done tools are muted. Running tools use warning color. Errors use error color.
func (m *cliModel) renderToolTags(tools []protocol.ToolProgress, width int, s *cliStyles) string {
	var tags []string
	for _, tool := range tools {
		label := tool.Label
		if label == "" {
			label = tool.Name
		}
		switch tool.Status {
		case "error":
			tags = append(tags, s.ProgressError.Render("✗")+" "+s.ProgressError.Render(label))
		case "done":
			tags = append(tags, s.ProgressDone.Render("✓")+" "+s.TextMutedSt.Render(label))
		default:
			tags = append(tags, s.ProgressRunning.Render("●")+" "+s.ProgressRunning.Render(label))
		}
	}
	// Join with dim dot separator
	sep := " " + s.ProgressDim.Render("·") + " "
	return s.ProgressDim.Render("·") + " " + strings.Join(tags, sep)
}

// renderReasoningBox renders a collapsible reasoning section.
//
// Collapsed (default):
//
//	▸ Reasoning (12 lines) ──────────────────────
//
// Expanded:
//
//	╭ Reasoning ──────────────────────────────╮
//	│ The error occurs because the token is   │
//	│ not properly validated in the handler.  │
//	╰─────────────────────────────────────────╯
func (m *cliModel) renderReasoningBox(
	reasoning string,
	width int,
	s *cliStyles,
	expanded bool,
) string {
	if reasoning == "" {
		return ""
	}

	lines := strings.Split(strings.TrimSpace(reasoning), "\n")

	if !expanded {
		// Collapsed: one-line indicator
		count := len(lines)
		summary := fmt.Sprintf("▸ Reasoning (%d lines)", count)
		padW := width - lipgloss.Width(summary) - 2
		if padW > 0 {
			summary += " " + s.ProgressDim.Render(strings.Repeat("─", padW))
		}
		return s.TextSecondarySt.Render(summary)
	}

	// Expanded: bordered box
	innerW := width - 4 // "│ " + " │"
	if innerW < 20 {
		innerW = 20
	}
	var sb strings.Builder

	// Top border with label
	label := " Reasoning "
	labelW := lipgloss.Width(label)
	dashCount := innerW - labelW
	if dashCount < 3 {
		dashCount = 3
	}
	sb.WriteString(s.ProgressDim.Render("╭"))
	sb.WriteString(s.TextSecondarySt.Render(label))
	sb.WriteString(s.ProgressDim.Render(strings.Repeat("─", dashCount) + "╮"))
	sb.WriteString("\n")

	// Content lines
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

	// Bottom border
	sb.WriteString(s.ProgressDim.Render("╰" + strings.Repeat("─", innerW) + "╯"))

	return sb.String()
}

// renderLiveIteration renders the in-progress iteration:
//   - Streaming content (glamour md)
//   - Active tools with elapsed time
//   - SubAgent tree
func (m *cliModel) renderLiveIteration(p *protocol.ProgressEvent, width int) string {
	s := &m.styles
	var sb strings.Builder

	// 1. Streaming content (md rendered)
	streamContent := p.StreamContent
	if streamContent == "" {
		streamContent = p.ReasoningStreamContent
	}
	if streamContent != "" {
		rendered := m.renderTurnContent(streamContent, width)
		sb.WriteString(rendered)
		sb.WriteString("\n")
	}

	// 2. Active tools with elapsed
	if len(p.ActiveTools) > 0 {
		if streamContent != "" {
			sb.WriteString("\n") // breathe between content and tools
		}
		for _, tool := range p.ActiveTools {
			if tool.Status == "running" || tool.Status == "active" {
				elapsed := formatElapsed(tool.Elapsed)
				icon := s.ProgressRunning.Render("🔄")
				label := tool.Label
				if label == "" {
					label = tool.Name
				}
				fmt.Fprintf(&sb, "%s %s %s", icon, s.ProgressRunning.Render(label), s.ProgressElapsed.Render(elapsed))
				sb.WriteString("\n")
			}
		}
	}

	// 3. SubAgent tree
	if len(p.SubAgents) > 0 {
		var treeSb strings.Builder
		m.renderSubAgentTree(&treeSb, p.SubAgents, "", width)
		treeStr := strings.TrimRight(treeSb.String(), "\n")
		if treeStr != "" {
			sb.WriteString(treeStr)
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

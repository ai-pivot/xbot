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
// Design principles:
//
//   • One assistant message per turn, containing all iterations inline.
//   • Each completed iteration shows: content (glamour md) + tool tags.
//   • The current (live) iteration shows: streaming md + active tools.
//   • Reasoning is rendered in a collapsible box (default: collapsed).
//   • Tools are inline tags: "✓ Shell  ✓ Read", expandable via Ctrl+O.
//   • SubAgent tree is preserved.
//
// Layout for a single completed iteration:
//
//   ┊  I found the issue in the auth module.
//   ┊  [Shell ✓ Read ✓]
//   ┊  ╭ Reasoning ──────────────────────╮
//   ┊  │ The error occurs because the     │  ← only when expanded
//   ┊  │ token is not properly validated. │
//   ┊  ╰──────────────────────────────────╯
//   ┊
//
// Layout for the current (live) iteration:
//
//   ┊  Let me check the config file...
//   ┊  🔄 Grep ● 2.1s
//   ┊    └── explore [mem-1]: searching...
// ---------------------------------------------------------------------------

// renderTurnBody renders the full body of an assistant message for a turn.
// During busy state: uses liveProgress + iterationHistory.
// During idle state: uses msg.iterations (baked from iterationHistory).
//
// The output is raw ANSI lines WITHOUT the guide prefix — the caller
// (renderMessage assistant branch) adds the "┊ " prefix per line.
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
			sb.WriteString("\n") // blank line between iterations
		}

		// 1. Iteration content (Thinking field = final text reply of this iter).
		if iter.Thinking != "" {
			rendered := m.renderTurnContent(iter.Thinking, contentWidth)
			sb.WriteString(rendered)
			sb.WriteString("\n")
		}

		// 2. Tool tags.
		if len(iter.Tools) > 0 {
			sb.WriteString(m.renderToolTags(iter.Tools, contentWidth, s))
			sb.WriteString("\n")
		}

		// 3. Reasoning (collapsible box, default collapsed).
		if iter.Reasoning != "" {
			sb.WriteString(m.renderReasoningBox(iter.Reasoning, contentWidth, s, false))
			sb.WriteString("\n")
		}
	}

	// Render the current live iteration (if any).
	if liveProgress != nil {
		if len(iterations) > 0 {
			sb.WriteString("\n") // blank line before live iteration
		}
		sb.WriteString(m.renderLiveIteration(liveProgress, contentWidth))
	}

	return strings.TrimRight(sb.String(), "\n")
}

// renderTurnContent renders markdown text through glamour for turn content.
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

// renderToolTags renders inline tool tags: "[✓ Shell  ✓ Read  ✗ Grep]"
func (m *cliModel) renderToolTags(tools []protocol.ToolProgress, width int, s *cliStyles) string {
	var sb strings.Builder
	// Compact inline tag format.
	// Use different icons for status.
	for i, tool := range tools {
		if i > 0 {
			sb.WriteString("  ") // double space between tags
		}
		label := tool.Label
		if label == "" {
			label = tool.Name
		}
		switch tool.Status {
		case "error":
			sb.WriteString(s.ProgressError.Render("✗"))
			sb.WriteString(" ")
			sb.WriteString(s.ProgressError.Render(label))
		case "done":
			sb.WriteString(s.ProgressDone.Render("✓"))
			sb.WriteString(" ")
			sb.WriteString(s.TextMutedSt.Render(label))
		default:
			sb.WriteString(s.ProgressRunning.Render("●"))
			sb.WriteString(" ")
			sb.WriteString(s.ProgressRunning.Render(label))
		}
	}
	// Wrap in subtle brackets
	tags := sb.String()
	return s.TextMutedSt.Render("[") + tags + s.TextMutedSt.Render("]")
}

// renderReasoningBox renders a collapsible reasoning section.
// When collapsed, shows a one-line summary. When expanded, shows full content
// in a bordered box with "Reasoning" in the top-left corner.
//
// Collapsed:
//
//	┊  ▸ Reasoning (12 lines) ─────────────
//
// Expanded:
//
//	┊  ╭ Reasoning ──────────────────────╮
//	┊  │ The error occurs because the     │
//	┊  │ token is not properly validated. │
//	┊  ╰──────────────────────────────────╯
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
	innerW := width - 4 // space for "│ " prefix and " │" suffix
	if innerW < 20 {
		innerW = 20
	}

	if !expanded {
		// Collapsed: single line summary
		summary := fmt.Sprintf("▸ Reasoning (%d lines)", len(lines))
		// Pad with dim dashes
		padW := width - lipgloss.Width(summary) - 2
		if padW > 0 {
			summary += " " + s.ProgressDim.Render(strings.Repeat("─", padW))
		}
		return s.TextSecondarySt.Render(summary)
	}

	// Expanded: bordered box with "Reasoning" label
	var sb strings.Builder

	// Top border: ╭ Reasoning ──────╮
	label := " Reasoning "
	labelLen := lipgloss.Width(label)
	dashCount := innerW - labelLen
	if dashCount < 3 {
		dashCount = 3
	}
	topBorder := s.ProgressDim.Render("╭") + s.TextSecondarySt.Render(label) +
		s.ProgressDim.Render(strings.Repeat("─", dashCount)+"╮")
	sb.WriteString(topBorder)
	sb.WriteString("\n")

	// Content lines
	for _, line := range lines {
		wrapped := hardWrapRunes(line, innerW-2)
		for _, wl := range strings.Split(wrapped, "\n") {
			// Pad line to innerW
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

	// Bottom border: ╰───────────────╯
	sb.WriteString(s.ProgressDim.Render("╰" + strings.Repeat("─", innerW) + "╯"))

	return sb.String()
}

// renderLiveIteration renders the current in-progress iteration:
//   - Streaming content (glamour md)
//   - Active tools with spinner
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
		for _, tool := range p.ActiveTools {
			if tool.Status == "running" {
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

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

// renderTurnBody renders all iteration content for an assistant message.
func (m *cliModel) renderTurnBody(
	iterations []cliIterationSnapshot,
	liveProgress *protocol.ProgressEvent,
	contentWidth int,
	fallbackContent string,
	reasoningExpanded bool,
) string {
	s := &m.styles
	var sb strings.Builder

	for i := range iterations {
		iter := &iterations[i]
		if i > 0 {
			sb.WriteString("\n")
		}

		// Reasoning (collapsed box above content)
		if iter.Reasoning != "" {
			sb.WriteString(m.renderReasoningBox(iter.Reasoning, contentWidth, s, reasoningExpanded))
		}

		// Content (markdown)
		if iter.Thinking != "" {
			if iter.Reasoning != "" {
				sb.WriteString("\n")
			}
			rendered := m.renderTurnContent(iter.Thinking, contentWidth)
			sb.WriteString(rendered)
		}

		// Tool tags
		if len(iter.Tools) > 0 {
			if iter.Thinking != "" || iter.Reasoning != "" {
				sb.WriteString("\n")
			}
			sb.WriteString(m.renderToolTags(iter.Tools, s))
		}
	}

	if liveProgress != nil {
		if len(iterations) > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(m.renderLiveIteration(liveProgress, contentWidth, fallbackContent))
	}

	return strings.TrimRight(sb.String(), "\n")
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
func (m *cliModel) renderToolTags(tools []protocol.ToolProgress, s *cliStyles) string {
	var tags []string
	for _, tool := range tools {
		label := tool.Label
		if label == "" {
			label = tool.Name
		}
		switch tool.Status {
		case "error":
			tags = append(tags, s.ProgressError.Render("✗ "+label))
		case "done":
			tags = append(tags, s.ProgressDone.Render("✓")+" "+s.TextMutedSt.Render(label))
		default:
			tags = append(tags, s.ProgressRunning.Render("● "+label))
		}
	}
	sep := " " + s.ProgressDim.Render("·") + " "
	return s.ProgressDim.Render("·") + " " + strings.Join(tags, sep)
}

// renderReasoningBox renders a collapsible reasoning section.
//
// Collapsed:
//
//	╭ Reasoning (38 lines) ──────────────────────╮
//
// Expanded:
//
//	╭ Reasoning ──────────────────────────────╮
//	│ reasoning text line 1                   │
//	│ reasoning text line 2                   │
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
	innerW := width - 4 // "│ " + " │"
	if innerW < 20 {
		innerW = 20
	}

	if !expanded {
		label := fmt.Sprintf(" Reasoning (%d lines) ", len(lines))
		labelW := lipgloss.Width(label)
		dashCount := innerW - labelW
		if dashCount < 0 {
			dashCount = 0
		}
		return s.ProgressDim.Render("╭") +
			s.TextSecondarySt.Render(label) +
			s.ProgressDim.Render(strings.Repeat("─", dashCount)+"╮")
	}

	var sb strings.Builder
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
	s := &m.styles
	var sb strings.Builder

	// 1. Reasoning box (if streaming reasoning)
	if p.ReasoningStreamContent != "" {
		sb.WriteString(m.renderReasoningBox(p.ReasoningStreamContent, width, s, true))
		sb.WriteString("\n")
	}

	// 2. Content: prefer live stream → accumulated msg text
	streamContent := p.StreamContent
	displayContent := streamContent
	if displayContent == "" {
		displayContent = fallbackContent
	}
	if displayContent != "" {
		rendered := m.renderTurnContent(displayContent, width)
		sb.WriteString(rendered)
		sb.WriteString("\n")
	}

	// 3. Active tools with spinner
	if len(p.ActiveTools) > 0 {
		for _, tool := range p.ActiveTools {
			if tool.Status == "running" || tool.Status == "active" {
				elapsed := formatElapsed(tool.Elapsed)
				frame := orbitFrames[m.ticker.frame%len(orbitFrames)]
				label := tool.Label
				if label == "" {
					label = tool.Name
				}
				fmt.Fprintf(&sb, "%s %s %s",
					s.ProgressRunning.Render(frame),
					s.ProgressRunning.Render(label),
					s.ProgressElapsed.Render(elapsed))
				sb.WriteString("\n")
			}
		}
	} else if displayContent == "" && p.ReasoningStreamContent == "" {
		frame := diamondPulseFrames[m.ticker.frame%len(diamondPulseFrames)]
		sb.WriteString(s.ProgressRunning.Render(frame))
		sb.WriteString("\n")
	}

	// 4. SubAgent tree
	if len(p.SubAgents) > 0 {
		var treeSb strings.Builder
		m.renderSubAgentTree(&treeSb, p.SubAgents, "", width)
		if tree := strings.TrimRight(treeSb.String(), "\n"); tree != "" {
			sb.WriteString(tree)
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

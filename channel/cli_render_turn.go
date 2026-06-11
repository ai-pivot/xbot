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
// Layout per completed iteration:
//
//   ┊  Content text rendered as markdown...
//   ┊  · Shell ✓  · Read ✓  · FileReplace ✓
//   ┊  ▸ Reasoning (8 lines) ─────────────────
//
// Layout for the live iteration:
//
//   ┊  Next iteration content...
//   ┊  ◕ Grep ● 2.1s                           ← animated spinner + elapsed
//   ┊    └── explore [mem-1]: searching...       ← SubAgent tree
// ---------------------------------------------------------------------------

// renderTurnBody renders all iteration content for an assistant message.
// busy: uses liveProgress + iterationHistory + fallbackContent
// idle: uses baked iterations from the message
//
// fallbackContent is the streaming message's accumulated text, used when
// liveProgress has no StreamContent (e.g. during tool execution the LLM
// has stopped streaming but tools are running).
//
// Output does NOT include guide prefix — caller adds "┊ " per line.
func (m *cliModel) renderTurnBody(
	iterations []cliIterationSnapshot,
	liveProgress *protocol.ProgressEvent,
	contentWidth int,
	fallbackContent string,
) string {
	s := &m.styles
	var sb strings.Builder

	// Render each completed iteration.
	for i := range iterations {
		iter := &iterations[i]
		if i > 0 {
			sb.WriteString("\n")
		}

		// 1. Iteration content (Thinking = text reply).
		if iter.Thinking != "" {
			rendered := m.renderTurnContent(iter.Thinking, contentWidth)
			sb.WriteString(rendered)
			sb.WriteString("\n")
		}

		// 2. Reasoning (collapsible, above tool tags for better reading flow).
		if iter.Reasoning != "" {
			sb.WriteString(m.renderReasoningBox(iter.Reasoning, contentWidth, s, false))
			sb.WriteString("\n")
		}

		// 3. Tool tags — subtle dot-separated row.
		if len(iter.Tools) > 0 {
			sb.WriteString(m.renderToolTags(iter.Tools, contentWidth, s))
			sb.WriteString("\n")
		}
	}

	// Render the current live iteration (if any).
	if liveProgress != nil {
		if len(iterations) > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(m.renderLiveIteration(liveProgress, contentWidth, fallbackContent))
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

// renderToolTags renders a compact dot-separated row of tool badges.
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
	sep := " " + s.ProgressDim.Render("·") + " "
	return s.ProgressDim.Render("·") + " " + strings.Join(tags, sep)
}

// renderReasoningBox renders a collapsible reasoning section.
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
		summary := fmt.Sprintf("▸ Reasoning (%d lines)", len(lines))
		padW := width - lipgloss.Width(summary) - 2
		if padW > 0 {
			summary += " " + s.ProgressDim.Render(strings.Repeat("─", padW))
		}
		return s.TextSecondarySt.Render(summary)
	}

	innerW := width - 4
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
// fallbackContent is the streaming message's accumulated text, used when
// liveProgress has no StreamContent (tool execution phase).
func (m *cliModel) renderLiveIteration(p *protocol.ProgressEvent, width int, fallbackContent string) string {
	s := &m.styles
	var sb strings.Builder

	// 1. Content — prefer live stream, fall back to accumulated message text.
	// During tool execution, StreamContent is empty but the LLM's prior
	// text is in fallbackContent (streaming message's accumulated content).
	streamContent := p.StreamContent
	if streamContent == "" {
		streamContent = p.ReasoningStreamContent
	}
	displayContent := streamContent
	if displayContent == "" {
		displayContent = fallbackContent
	}
	if displayContent != "" {
		rendered := m.renderTurnContent(displayContent, width)
		sb.WriteString(rendered)
		sb.WriteString("\n")
	}

	// 2. Active tools with animated spinner + elapsed.
	// If no content yet and no active tools, show a standalone spinner
	// (waiting for LLM first response).
	if len(p.ActiveTools) > 0 {
		for _, tool := range p.ActiveTools {
			if tool.Status == "running" || tool.Status == "active" {
				elapsed := formatElapsed(tool.Elapsed)
				frame := orbitFrames[m.ticker.frame%len(orbitFrames)]
				icon := s.ProgressRunning.Render(frame)
				label := tool.Label
				if label == "" {
					label = tool.Name
				}
				fmt.Fprintf(&sb, "%s %s %s", icon, s.ProgressRunning.Render(label), s.ProgressElapsed.Render(elapsed))
				sb.WriteString("\n")
			}
		}
	} else if displayContent == "" {
		// No content and no tools — LLM is thinking. Show spinner.
		frame := diamondPulseFrames[m.ticker.frame%len(diamondPulseFrames)]
		sb.WriteString(s.ProgressRunning.Render(frame))
		sb.WriteString("\n")
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

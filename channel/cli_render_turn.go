package channel

import (
	"fmt"
	"strings"
	"time"

	"xbot/protocol"

	"charm.land/lipgloss/v2"
)

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

		if iter.Thinking != "" {
			appendTurnBlock(&sb, &lastKind, &hasBlock, turnBlock{
				kind: turnBlockContent,
				text: m.renderTurnContent(iter.Thinking, contentWidth),
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
		// Avoid duplication: skip if the last iteration already contains it.
		alreadyRendered := false
		if len(iterations) > 0 {
			last := iterations[len(iterations)-1]
			if last.Thinking == fallbackContent {
				alreadyRendered = true
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
		case "error":
			tag = s.ProgressError.Render("✗ " + label)
		case "done":
			tag = s.ProgressDone.Render("✓ " + label)
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
		if iter.Thinking != "" {
			return turnBlockContent, true
		}
		if iter.Reasoning != "" {
			return turnBlockReasoning, true
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

	if p.ReasoningStreamContent != "" {
		hasSpinner = true
		blocks = append(blocks, turnBlock{
			kind: turnBlockReasoning,
			text: m.renderReasoningBox(p.ReasoningStreamContent, width, s),
		})
	}

	streamContent := p.StreamContent
	if streamContent != "" || fallbackContent != "" {
		hasSpinner = true
	}
	displayContent := streamContent
	if displayContent == "" {
		displayContent = p.Thinking
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

	// Combine ActiveTools (active/done/error) and CompletedTools.
	// Deduplicate by Name+Label to prevent the same tool appearing twice
	// when it transitions from ActiveTools(done) to CompletedTools across frames.
	var tools []protocol.ToolProgress
	seen := make(map[string]bool)
	addTool := func(t protocol.ToolProgress) {
		key := t.Name + "\x00" + t.Label
		if seen[key] {
			return
		}
		seen[key] = true
		tools = append(tools, t)
	}
	for _, tool := range p.ActiveTools {
		if tool.Status == "running" || tool.Status == "active" || tool.Status == "done" || tool.Status == "error" {
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
		case "error":
			sb.WriteString("  ")
			sb.WriteString(s.ProgressDim.Render("·"))
			sb.WriteString(" ")
			sb.WriteString(s.ProgressError.Render("✗ " + label))
			sb.WriteString("\n")
		case "done":
			sb.WriteString("  ")
			sb.WriteString(s.ProgressDim.Render("·"))
			sb.WriteString(" ")
			sb.WriteString(s.ProgressDone.Render("✓ " + label))
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

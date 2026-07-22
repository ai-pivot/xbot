package cli

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"xbot/protocol"

	"charm.land/lipgloss/v2"
)

func (m *cliModel) renderMessage(msg *cliMessage) string {
	if msg.hidden {
		return ""
	}
	// §20 使用缓存样式
	s := &m.styles
	var sb strings.Builder
	contentWidth := m.chatWidth() - 4
	// chatUsableWidth: unified right boundary for user msg content.
	// Aligns with assistant body right edge (guide(2) + content(cw-4) = cw-2),
	// leaving 2 cols right padding so content doesn't touch the viewport edge.
	chatUsableWidth := m.chatWidth() - 2
	cw := m.chatWidth()
	timeStyle := s.Time
	userLabelStyle := s.UserLabel
	streamingLabelStyle := s.StreamingLabel
	// Override style widths to chatWidth (sidebar may be open, reducing available space)
	systemMsgStyle := s.SystemMsg.Width(cw)
	errorMsgStyle := s.ErrorMsg.Width(cw - 4)

	// 渲染 Markdown（assistant 消息 + 带 markdown 标记的 system 消息）
	var rendered string
	if msg.role == "assistant" || (msg.role == "system" && msg.markdown && !msg.styled) {
		// Pre-process: render mermaid code blocks to ASCII art
		// Truncate to glamour wrap width to prevent wrapping.
		preprocessed := msg.content
		if msg.role == "assistant" {
			preprocessed = renderMermaidBlocks(msg.content, m.chatWidth()-4)
			preprocessed = renderMathBlocks(preprocessed, m.chatWidth()-4)
		}
		var err error
		rendered, err = m.renderer.Render(preprocessed)
		if err != nil {
			rendered = msg.content
		}
		rendered = strings.TrimSpace(rendered)
	} else {
		rendered = msg.content
	}

	timeStr := timeStyle.Render(msg.timestamp.Format("15:04:05"))

	switch msg.role {
	case "tool_summary":
		// Removed: tool data is now rendered inline in the assistant message
		// via renderTurnBody(). No separate tool_summary rendering needed.
	case "system":
		if msg.styled {
			// Pre-styled content: output as-is, no wrapping
			sb.WriteString(msg.content)
		} else if msg.markdown {
			// Markdown system messages (e.g. /usage tables): use glamour-rendered output directly
			sb.WriteString(rendered)
		} else if isErrorContent(msg.content) {
			sb.WriteString(errorMsgStyle.Render("⚠ " + msg.content))
		} else {
			sb.WriteString(systemMsgStyle.Render(msg.content))
		}
	case "user":
		// 用户消息上方：右侧柔和光点分隔，与 assistant 的左侧竖线形成对称
		dotSep := s.UserDotSep.Width(chatUsableWidth).Align(lipgloss.Right).Render("···")
		sb.WriteString(dotSep)
		sb.WriteString("\n")
		label := userLabelStyle.Render("You")
		header := s.UserHeader.Width(chatUsableWidth).Align(lipgloss.Right).Render(fmt.Sprintf("%s %s", timeStr, label))
		sb.WriteString(header)
		sb.WriteString("\n")
		// 用户消息：右对齐气泡效果
		// 计算内容最大行宽，整块右对齐而非每行拉伸
		lines := strings.Split(rendered, "\n")
		maxWidth := 0
		for _, line := range lines {
			w := lipgloss.Width(line)
			if w > maxWidth {
				maxWidth = w
			}
		}
		maxBubble := chatUsableWidth * 3 / 4
		userStyle := s.UserContent
		if maxWidth <= maxBubble {
			// 内容够窄，左填充实现气泡靠右
			userStyle = s.UserContent.PaddingLeft(chatUsableWidth - maxWidth)
		}
		// CRITICAL: lipgloss Render pads ALL lines to maxWidth including trailing
		// spaces. When contentWidth is close to terminal width, the padded lines
		// can overflow after being processed by hardWrapRunes. Fix: Render first,
		// then right-trim each line — the padding is visual-only (left-aligned by
		// PaddingLeft) and trailing spaces serve no purpose.
		renderedUser := userStyle.Render(rendered)
		userLines := strings.Split(renderedUser, "\n")
		for i, rl := range userLines {
			// Strip trailing whitespace (lipgloss padding) from ALL lines.
			// The left-padding is preserved because TrimRight only removes right side.
			userLines[i] = strings.TrimRight(rl, " \t")
		}
		sb.WriteString(strings.Join(userLines, "\n"))
	case "tool":
		toolName := ""
		if len(msg.tools) > 0 {
			toolName = oneLineToolLabel(msg.tools[0].Name)
		}
		label := "Tool result"
		if toolName != "" {
			label += " · " + toolName
		}
		fmt.Fprintf(&sb, "%s %s", timeStr, s.ToolHeader.Render(label))
		if body := strings.TrimSpace(rendered); body != "" {
			bodyStyle := s.ToolHint
			if isErrorContent(body) {
				bodyStyle = s.ProgressError
			}
			for _, line := range strings.Split(body, "\n") {
				sb.WriteString("\n  ")
				sb.WriteString(bodyStyle.Render(line))
			}
		}
	default:
		// assistant 消息 — crush 风格：先构建内容体，再逐行加 guide 前缀
		// Streaming: bright guide; Completed: dim guide
		var guideSt lipgloss.Style
		guideSym := "┊ "
		if msg.isPartial {
			guideSt = s.GuideSt
		} else {
			guideSt = s.DimGuideSt
		}

		// Build header line
		label := streamingLabelStyle.Render("Assistant")
		if !msg.isPartial {
			label = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextSecondary)).Render("Assistant")
		}
		headerLine := fmt.Sprintf("%s %s", guideSt.Render(guideSym)+timeStr, label)
		if msg.isPartial {
			headerLine += " ..."
		}
		sb.WriteString(headerLine)
		sb.WriteString("\n")

		// Build body lines (thinking box + content + cursor)
		var bodyLines []string

		// Unified turn rendering: if this assistant message has iteration data,
		// use renderTurnBody instead of separate thinking box + content.
		// For streaming (isPartial) messages during busy state, also include
		// live iteration data from m.progressState.iterations + m.progressState.current.
		hasIterData := len(msg.iterations) > 0 || len(msg.tools) > 0
		isLiveTurn := msg.isPartial && m.typing && (len(m.progressState.iterations) > 0 || m.progressState.current != nil)
		if hasIterData || isLiveTurn {
			var iterations []cliIterationSnapshot
			var liveProgress *protocol.ProgressEvent
			var fallbackContent string // content from streaming message when liveProgress has no stream text
			if isLiveTurn {
				iterations = m.progressState.iterations
				liveProgress = m.progressState.current
				fallbackContent = msg.content
			} else {
				iterations = msg.iterations
				if len(msg.tools) > 0 {
					hasIterationTools := false
					for _, iteration := range iterations {
						if len(iteration.Tools) > 0 {
							hasIterationTools = true
							break
						}
					}
					if !hasIterationTools {
						iterations = append(append([]cliIterationSnapshot(nil), iterations...), cliIterationSnapshot{Tools: msg.tools})
					}
				}
				fallbackContent = msg.content
			}
			bodyContent := m.renderTurnBody(iterations, liveProgress, contentWidth, fallbackContent)
			if bodyContent != "" {
				bodyLines = append(bodyLines, strings.Split(bodyContent, "\n")...)
			}
		} else {
			// Thinking Box
			if !msg.isPartial && msg.reasoning != "" {
				thinkingLines := strings.Split(strings.TrimSpace(msg.reasoning), "\n")
				const maxTL = 10
				if len(thinkingLines) > 0 {
					var display []string
					truncated := len(thinkingLines) > maxTL
					if truncated {
						display = thinkingLines[len(thinkingLines)-maxTL:]
					} else {
						display = thinkingLines
					}
					body := strings.Join(display, "\n")
					if truncated {
						body = s.TextMutedSt.Render(fmt.Sprintf("… (%d lines hidden)", len(thinkingLines)-maxTL)) + "\n" + body
					}
					boxW := contentWidth - 4
					if boxW < 20 {
						boxW = 20
					}
					thinkingBox := s.ThinkingBox
					for _, l := range strings.Split(thinkingBox.Width(boxW).Render(body), "\n") {
						bodyLines = append(bodyLines, "  "+l)
					}
					bodyLines = append(bodyLines, "") // blank after box
				}
			}

			// §19 长消息折叠
			displayContent := rendered
			if msg.folded && !msg.isPartial {
				origLines := msg.originalRenderedLines
				if origLines == 0 {
					origLines = msg.renderedLines
				}
				if origLines > msgFoldThresholdLines {
					renderedLinesList := strings.Split(rendered, "\n")
					if len(renderedLinesList) > msgFoldPreviewLines {
						displayContent = strings.Join(renderedLinesList[:msgFoldPreviewLines], "\n")
						displayContent += "\n" + m.styles.TextMutedSt.Render(
							fmt.Sprintf("  ... %s (%d lines) ...", m.locale.MsgCollapsed, origLines))
					}
				}
			}

			// Main content — trim trailing newlines so cursor stays inline.
			trimmed := strings.TrimRight(displayContent, "\n")
			if trimmed != "" {
				bodyLines = append(bodyLines, strings.Split(trimmed, "\n")...)
			}

			// Streaming cursor
			if msg.isPartial && trimmed != "" {
				cursorVisible := (m.ticker.ticks/5)%2 == 0
				if cursorVisible {
					bodyLines = append(bodyLines, s.StreamCursor.Render("▋"))
				}
			}
		}

		// Render all body lines with guide prefix.
		// ANSI reset before guide: clears inline code background color inherited
		// from the previous body line (hardWrapRunes doesn't add trailing reset
		// when breaking inside an inline code span, so bg color leaks forward).
		// ANSI reset after guide: prevents guide foreground from mixing with
		// body line ANSI styles.
		// Right-align: body content width = contentWidth (same as user msg "You"),
		// so guide(2) + body(cw-4) leaves 2 cols right padding.
		const ansiReset = "\x1b[0m"
		renderedGuide := guideSt.Render(guideSym)
		if len(bodyLines) > 0 {
			sb.WriteString(ansiReset)
			sb.WriteString(renderedGuide)
			sb.WriteString(ansiReset)
			sb.WriteString("\n")
		}
		for _, l := range bodyLines {
			sb.WriteString(ansiReset)
			sb.WriteString(renderedGuide)
			sb.WriteString(ansiReset)
			sb.WriteString(l)
			sb.WriteString("\n")
		}
	}

	sb.WriteString("\n\n")

	// §19 计算渲染后行数（每次 dirty 重算）
	// Sanitize rendered output: strip \r carriage-return overwrites per line.
	// This is the final rendering-layer safety net — ensures progress bar
	// output (tqdm, curl etc.) from any source (old offload, history, network)
	// never corrupts the TUI layout.
	raw := sb.String()
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		if idx := strings.LastIndex(line, "\r"); idx >= 0 {
			lines[i] = line[idx+1:]
		}
	}
	cleaned := strings.Join(lines, "\n")
	msg.renderedLines = strings.Count(cleaned, "\n") + 1

	return cleaned
}

// wrapPreservingGuide handles viewport-level line wrapping.
// Guide lines (starting with "┊ ") were already word-wrapped by glamour (with CJK-aware
// reflow), so we skip hardWrapRunes to avoid breaking CJK text at character boundaries.
// Non-guide lines may need hardWrapRunes as a safety net for long ANSI-styled content.
func wrapPreservingGuide(line string, cw int) []string {
	prefix, rest, pw := splitGuidePrefix(line)
	if pw == 0 || rest == "" {
		// Non-guide line: apply hard wrap as safety net
		return strings.Split(hardWrapRunes(line, cw), "\n")
	}
	// Guide line: glamour already wrapped content to (cw - 4) width.
	// Guide prefix is 2 cols, so total = 2 + (cw-4) = cw-2, which fits.
	// Return as-is to preserve glamour's CJK-aware wrapping.
	// Only truncate if a line is somehow still too wide (defensive).
	if lipgloss.Width(line) <= cw {
		return []string{line}
	}
	// Defensive: if still too wide, truncate (don't hard-wrap — that breaks CJK)
	contentW := cw - pw
	if contentW <= 0 {
		return []string{line}
	}
	truncated := truncateToWidth(rest, contentW)
	return []string{prefix + truncated}
}

// splitGuidePrefix splits a rendered line into its guide prefix and the rest.
// Guide lines start with optional spaces then "┊ " (possibly ANSI-colored).
// Returns (prefix, rest, prefixDisplayWidth). If no guide, returns ("", line, 0).
func splitGuidePrefix(line string) (prefix, rest string, prefixW int) {
	i := 0
	n := len(line)
	inEscape := false
	foundPipe := false

	for i < n {
		b := line[i]
		if b == '\x1b' {
			inEscape = true
			i++
			continue
		}
		if inEscape {
			i++
			if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') {
				inEscape = false
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if !foundPipe {
			if r == '┊' {
				foundPipe = true
			} else if r != ' ' {
				// Not a guide line
				return "", line, 0
			}
		} else {
			// After ┊, expect a space
			if r == ' ' {
				end := i + size
				prefix = line[:end]
				rest = line[end:]
				prefixW = lipgloss.Width(prefix)
				return
			}
			// ┊ not followed by space — not a guide prefix
			return "", line, 0
		}
		i += size
	}
	return "", line, 0
}

// wrapDynamicPart wraps a dynamic content string (e.g. rewind block) into
// pre-wrapped lines for direct viewport assembly. This is the same logic as
// setViewportContent's dynamic-part wrapping, but returns []string directly.
func wrapDynamicPart(content string, cw int) []string {
	if content == "" {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed != line {
			visualW := lipgloss.Width(line)
			trimmedW := lipgloss.Width(trimmed)
			if visualW == trimmedW {
				line = trimmed
			}
		}
		wrapped := wrapPreservingGuide(line, cw)
		lines = append(lines, wrapped...)
	}
	return lines
}

func (m *cliModel) renderRewindResultBlock() string {
	if m.rewindResult == nil {
		return ""
	}
	r := m.rewindResult

	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(m.styles.ProgressDone.Bold(true).Render("  Rewind complete"))
	sb.WriteString("\n")

	if len(r.Restored) > 0 {
		fmt.Fprintf(&sb, "  Files restored: %d\n", len(r.Restored))
		for _, f := range r.Restored {
			sb.WriteString(m.styles.TextMutedSt.Render(fmt.Sprintf("    %s\n", f)))
		}
	}
	if len(r.CreatedDel) > 0 {
		fmt.Fprintf(&sb, "  Files deleted: %d\n", len(r.CreatedDel))
		for _, f := range r.CreatedDel {
			sb.WriteString(m.styles.TextMutedSt.Render(fmt.Sprintf("    %s\n", f)))
		}
	}
	if len(r.Errors) > 0 {
		for _, e := range r.Errors {
			sb.WriteString(m.styles.ProgressError.Render(fmt.Sprintf("  Error: %s\n", e)))
		}
	}

	return sb.String()
}

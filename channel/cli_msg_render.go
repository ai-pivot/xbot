package channel

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"xbot/protocol"

	"charm.land/lipgloss/v2"
)

func (m *cliModel) renderToolContentBelow(tool *protocol.ToolProgress, guide string, bodyW int, dimmed bool, maxLines int) string {
	var sb strings.Builder
	guideFn := func(s string) string { return s }
	if dimmed {
		dimSt := m.styles.ProgressDim
		guideFn = func(s string) string { return dimSt.Render(s) }
	}

	// 1. ToolHints (diff from plugin or built-in) — render with guide prefix,
	// same as per-tool body, so diff appears inside the guide tree.
	if tool.ToolHints != "" {
		g := guideFn(guide)
		hintW := bodyW - lipgloss.Width(g)
		if hintW < 1 {
			hintW = 1
		}
		if r, err := m.renderToolHint(tool.ToolHints, hintW, maxLines); err == nil && r != "" {
			for _, line := range strings.Split(r, "\n") {
				sb.WriteString(g)
				sb.WriteString(line)
				sb.WriteString("\n")
			}
		}
	}

	// 2. Per-tool body (Read code, Shell output, Grep matches, etc.)
	if tool.ToolHints == "" { // Don't show body if diff hints are already displayed
		g := guideFn(guide)
		bodyContentW := bodyW - lipgloss.Width(g)
		if bodyContentW < 1 {
			bodyContentW = 1
		}
		if body := m.renderToolBody(*tool, bodyContentW); body != "" {
			guideW := lipgloss.Width(g)
			for _, line := range strings.Split(body, "\n") {
				// Final safety net: ensure guide + rendered line fits within bodyW.
				// Tool body renderers (renderShellBody etc.) wrap to bodyContentW,
				// but lipgloss.Style.Render() may change effective width. Use hardWrapRunes
				// so overflow lines wrap (preserving content) instead of truncating.
				if visW := lipgloss.Width(line); guideW+visW > bodyW {
					wrapped := hardWrapRunes(line, bodyW-guideW)
					for _, wl := range strings.Split(wrapped, "\n") {
						sb.WriteString(g)
						sb.WriteString(wl)
						sb.WriteString("\n")
					}
					continue
				}
				sb.WriteString(g)
				sb.WriteString(line)
				sb.WriteString("\n")
			}
		}
	}

	result := strings.TrimRight(sb.String(), "\n")
	return result
}

func toolDisplayInfo(tool protocol.ToolProgress, okStyle, errStyle lipgloss.Style) (label, icon string, sty lipgloss.Style) {
	if tool.Label == "" {
		label = tool.Name
	} else {
		label = tool.Label
	}
	icon = "✓"
	sty = okStyle
	if tool.Status == "error" {
		icon = "✗"
		sty = errStyle
	}
	return
}

// toolLine formats a tool progress line guaranteed to fit within maxWidth cells.
// icon and label are plain text; elapsed may be pre-styled with ANSI codes.
// Returns the formatted string — caller wraps with style.Render().
func toolLine(icon, label string, elapsedStyled string, maxWidth int) string {
	prefix := fmt.Sprintf("  ┊ %s ", icon)
	prefixW := lipgloss.Width(prefix)

	elapsedW := lipgloss.Width(elapsedStyled) // strips ANSI, measures visual width

	minPad := 0
	if elapsedW > 0 {
		minPad = 1
	}

	maxLabelW := maxWidth - prefixW - elapsedW - minPad
	if maxLabelW < 0 {
		maxLabelW = 0
	}
	label = truncateToWidth(label, maxLabelW)
	labelW := lipgloss.Width(label)

	var sb strings.Builder
	sb.WriteString(prefix)
	sb.WriteString(label)
	if elapsedW > 0 {
		pad := maxWidth - prefixW - labelW - elapsedW
		if pad < minPad {
			pad = minPad
		}
		sb.WriteString(strings.Repeat(" ", pad))
		sb.WriteString(elapsedStyled)
	}
	return sb.String()
}

func (m *cliModel) renderMessage(msg *cliMessage) string {
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
		// §20 使用缓存样式（override width to chatWidth for sidebar compat）
		toolSummaryStyle := s.ToolSummary.Width(cw - 4)
		toolHeaderStyle := s.ToolHeader
		toolItemStyle := s.ToolItem
		toolErrorItemStyle := s.ToolErrorItem
		thinkingStyle := s.ProgressThinking
		reasoningStyle := s.TextMutedSt
		reasoningGuide := s.ProgressDim
		thinkingGuide := s.ProgressIndent
		hintStyle := s.ToolHint

		// 统计总工具数和总耗时
		allTools, iterCount := msg.iterToolsFlat()
		totalTools := len(allTools)
		totalMs := int64(0)
		for _, it := range msg.iterations {
			totalMs += it.ElapsedWall
		}

		var toolSb strings.Builder

		// Box internal width: ToolSummary has Border(2) + Padding(0,1 → 2) = 4 cols overhead
		boxInnerW := contentWidth - 4

		if m.toolSummaryExpanded {
			// 展开模式：完整渲染
			if iterCount > 0 {
				toolSb.WriteString(toolHeaderStyle.Render(fmt.Sprintf("Tools (%d iterations, %d calls)", iterCount, totalTools)))
				toolSb.WriteString("\n")
				guideW := lipgloss.Width(s.ProgressIndent.Render("  ┊ "))
				textW := boxInnerW - guideW
				for ii := range msg.iterations {
					it := &msg.iterations[ii]
					iterLabel := fmt.Sprintf("#%d", it.Iteration)
					if it.ElapsedWall > 0 {
						iterLabel += " " + reasoningStyle.Render(formatElapsed(it.ElapsedWall))
					}
					toolSb.WriteString(s.ProgressIter.Render(iterLabel))
					toolSb.WriteString("\n")
					if it.Reasoning != "" {
						for _, line := range strings.Split(it.Reasoning, "\n") {
							line = strings.TrimRight(line, " \t\r")
							if line == "" {
								continue
							}
							for _, wl := range strings.Split(hardWrapRunes(line, textW), "\n") {
								toolSb.WriteString(reasoningGuide.Render("  ┊ ") + reasoningStyle.Render(wl))
								toolSb.WriteString("\n")
							}
						}
					}
					if it.Thinking != "" {
						for _, line := range strings.Split(it.Thinking, "\n") {
							line = strings.TrimRight(line, " \t\r")
							if line == "" {
								continue
							}
							for _, wl := range strings.Split(hardWrapRunes(line, textW), "\n") {
								toolSb.WriteString(thinkingGuide.Render("  ┊ ") + thinkingStyle.Render(wl))
								toolSb.WriteString("\n")
							}
						}
					}
					for k := range it.Tools {
						tool := &it.Tools[k]
						label, icon, sty := toolDisplayInfo(*tool, toolItemStyle, toolErrorItemStyle)
						elapsed := ""
						if tool.Elapsed > 0 {
							elapsed = fmt.Sprintf(" (%s)", formatElapsed(tool.Elapsed))
						}
						toolSb.WriteString(sty.Render(fmt.Sprintf("    %s %s%s", icon, label, elapsed)))
						toolSb.WriteString("\n")
						// Render tool body (diff hints or per-tool output)
						if content := m.renderToolContentBelow(tool, reasoningGuide.Render("  ┊ "), textW, false, 0); content != "" {
							toolSb.WriteString(content)
							toolSb.WriteString("\n")
						}
					}
				}
			} else {
				toolSb.WriteString(toolHeaderStyle.Render(fmt.Sprintf("Tools (%d)", totalTools)))
				toolSb.WriteString("\n")
				for i := range msg.tools {
					tool := &msg.tools[i]
					label, icon, sty := toolDisplayInfo(*tool, toolItemStyle, toolErrorItemStyle)
					elapsed := ""
					if tool.Elapsed > 0 {
						elapsed = fmt.Sprintf(" (%s)", formatElapsed(tool.Elapsed))
					}
					toolSb.WriteString(sty.Render(fmt.Sprintf("  %s %s%s", icon, label, elapsed)))
					toolSb.WriteString("\n")
					// Render tool body for flat tool list too
					if content := m.renderToolContentBelow(tool, reasoningGuide.Render("  ┊ "), boxInnerW, false, 0); content != "" {
						toolSb.WriteString(content)
						toolSb.WriteString("\n")
					}
				}
			}
		} else {
			// 折叠模式升级（第 4 轮）：统计摘要 + 成功/失败状态图标
			elapsedStr := formatElapsed(totalMs)
			// 统计成功/失败工具数
			successCount, errorCount := 0, 0
			for _, tool := range allTools {
				if tool.Status == "error" {
					errorCount++
				} else {
					successCount++
				}
			}
			var statusIcons string
			if errorCount > 0 {
				statusIcons = s.ProgressError.Render("✗") +
					s.TextMutedSt.Render(fmt.Sprintf("%d", errorCount))
			}
			if successCount > 0 && errorCount > 0 {
				statusIcons += " "
			}
			if successCount > 0 {
				statusIcons += s.ProgressDone.Render("✓") +
					s.TextMutedSt.Render(fmt.Sprintf("%d", successCount))
			}
			toolSb.WriteString(toolHeaderStyle.Render(fmt.Sprintf("Tools %d calls · %s", totalTools, elapsedStr)))
			if statusIcons != "" {
				toolSb.WriteString("  ")
				toolSb.WriteString(statusIcons)
			}
			toolSb.WriteString("  ")
			toolSb.WriteString(hintStyle.Render("[Ctrl+O]"))
		}
		sb.WriteString(toolSummaryStyle.Render(toolSb.String()))
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
		if len(msg.iterations) > 0 {
			bodyContent := m.renderTurnBody(
				msg.iterations,
				nil, // idle: no liveProgress
				contentWidth,
			)
			if bodyContent != "" {
				bodyLines = append(bodyLines, strings.Split(bodyContent, "\n")...)
			}
		} else {
			// Thinking Box
			if !msg.isPartial && msg.thinking != "" {
				thinkingLines := strings.Split(strings.TrimSpace(msg.thinking), "\n")
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
		for _, l := range bodyLines {
			sb.WriteString(ansiReset + guideSt.Render(guideSym) + ansiReset)
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

// setViewportContent sets viewport content while preserving scroll position.
// If the user was at the bottom before the update, keep them at the bottom.

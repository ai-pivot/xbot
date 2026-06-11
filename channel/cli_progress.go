package channel

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"xbot/protocol"

	"charm.land/lipgloss/v2"
)

func (m *cliModel) renderHistoryRange(
	buf *strings.Builder,
	snaps []cliIterationSnapshot,
	innerWidth, reasoningW, thinkingW int,
	reasoningGuide, reasoningStyle, thinkingGuide, thinkingStyle,
	iterStyle, toolDoneStyle, toolErrorStyle, elapsedStyle, dimStyle lipgloss.Style,
	s *cliStyles,
) {
	// Pre-render guide prefixes once (they're the same for all lines).
	// Without this, each iteration re-renders "  ┊ " with Style.Render,
	// which internally walks the string with ANSI parser — O(chars) per call.
	reasoningGuidePrefix := reasoningGuide.Render("  ┊ ")
	thinkingGuidePrefix := thinkingGuide.Render("  ┊ ")

	for j := range snaps {
		snap := &snaps[j]
		buf.WriteString(dimStyle.Render(iterStyle.Render(fmt.Sprintf("#%d", snap.Iteration))))
		buf.WriteString("\n")
		if snap.Reasoning != "" {
			for _, line := range strings.Split(snap.Reasoning, "\n") {
				line = strings.TrimRight(line, " \t\r")
				if line == "" {
					continue
				}
				for _, wl := range strings.Split(hardWrapRunes(line, innerWidth-reasoningW), "\n") {
					buf.WriteString(dimStyle.Render(reasoningGuidePrefix + reasoningStyle.Render(wl)))
					buf.WriteString("\n")
				}
			}
		}
		if snap.Thinking != "" {
			for _, line := range strings.Split(snap.Thinking, "\n") {
				line = strings.TrimRight(line, " \t\r")
				if line == "" {
					continue
				}
				for _, wl := range strings.Split(hardWrapRunes(line, innerWidth-thinkingW), "\n") {
					buf.WriteString(dimStyle.Render(thinkingGuidePrefix + thinkingStyle.Render(wl)))
					buf.WriteString("\n")
				}
			}
		}
		for k := range snap.Tools {
			tool := &snap.Tools[k]
			label, icon, sty := toolDisplayInfo(*tool, toolDoneStyle, toolErrorStyle)
			var elapsedStyled string
			if tool.Elapsed > 0 {
				elapsedStyled = elapsedStyle.Render(formatElapsed(tool.Elapsed))
			}
			buf.WriteString(dimStyle.Render(sty.Render(toolLine(icon, label, elapsedStyled, innerWidth))))
			buf.WriteString("\n")
			if content := m.renderToolContentBelow(tool, reasoningGuidePrefix, innerWidth, true, 0); content != "" {
				buf.WriteString(content)
				buf.WriteString("\n")
			}
		}
	}
}

// renderProgressBlock renders the full progress panel showing completed
// iterations (dimmed) and the current iteration. An internal cache ensures it
// only runs when the content actually changes. Elapsed is quantized to whole
// seconds so the cache stays stable across 100ms ticks.
func (m *cliModel) renderProgressBlock() string {
	if !m.typing && m.progress == nil {
		m.cachedProgressBlockOut = ""
		m.cachedProgressBlockFP = 0
		m.cachedProgressBlockLines = nil
		return ""
	}
	// Cross-session guard: if progress payload carries a ChatID that doesn't match
	// the currently viewed session, it's stale — discard it entirely. This prevents
	// phantom progress blocks when switching sessions while another session's agent
	// is still processing.
	if m.progress != nil && m.progress.ChatID != "" {
		currentKey := qualifyChatID(m.channelName, m.chatID)
		if m.progress.ChatID != currentKey {
			m.progress = nil
			m.typing = false
			m.cachedProgressBlockOut = ""
			m.cachedProgressBlockFP = 0
			m.cachedProgressBlockLines = nil
			return ""
		}
	}

	bubbleWidth := m.chatWidth() - 4
	if bubbleWidth < 10 {
		bubbleWidth = 10
	}
	innerWidth := bubbleWidth - 2 // padding(2)
	if innerWidth < 1 {
		innerWidth = 1
	}

	// §20 使用缓存样式
	s := &m.styles
	iterStyle := s.ProgressIter
	thinkingStyle := s.ProgressThinking
	reasoningStyle := s.TextMutedSt // dimmed style for reasoning chain
	toolDoneStyle := s.ProgressDone
	toolRunningStyle := s.ProgressRunning
	toolErrorStyle := s.ProgressError
	elapsedStyle := s.ProgressElapsed
	indentGuide := s.ProgressIndent
	reasoningGuide := s.ProgressDim // dimmer ┊ for reasoning
	thinkingGuide := indentGuide    // normal ┊ for thinking
	reasoningW := lipgloss.Width(reasoningGuide.Render("  ┊ "))
	thinkingW := lipgloss.Width(thinkingGuide.Render("  ┊ "))
	dimStyle := s.ProgressDim

	// Render completed iterations (dimmed) — use cache to avoid re-running
	// chroma/lipgloss on every 100ms tick (major CPU saver for long sessions).
	// Incremental rebuild: only render newly added iterations and append to cache.
	// Uses padded lines cache (cachedProgressHistoryLines) so that padProgressLines
	// only needs to process the newly added lines instead of the entire history.
	var historyLines []string
	if m.cachedProgressHistoryLen == len(m.iterationHistory) && m.cachedProgressHistoryWidth == bubbleWidth && m.cachedProgressHistory != "" {
		historyLines = m.cachedProgressHistoryLines
	} else if m.cachedProgressHistoryLen > 0 && m.cachedProgressHistoryWidth == bubbleWidth && m.cachedProgressHistoryLen < len(m.iterationHistory) {
		// Incremental: only render new iterations [cachedProgressHistoryLen:]
		var histBuf strings.Builder
		m.renderHistoryRange(&histBuf, m.iterationHistory[m.cachedProgressHistoryLen:], innerWidth, reasoningW, thinkingW, reasoningGuide, reasoningStyle, thinkingGuide, thinkingStyle, iterStyle, toolDoneStyle, toolErrorStyle, elapsedStyle, dimStyle, s)
		newHistory := histBuf.String()
		m.cachedProgressHistory += newHistory
		m.cachedProgressHistoryLen = len(m.iterationHistory)
		m.cachedProgressHistoryFP = fnvHash64(m.cachedProgressHistory)
		// Incremental padded lines: only pad the new portion
		newLines := padLinesFromContent(newHistory)
		m.cachedProgressHistoryLines = append(m.cachedProgressHistoryLines, newLines...)
		historyLines = m.cachedProgressHistoryLines
	} else {
		// Full rebuild (width changed or cache invalidation)
		var histBuf strings.Builder
		m.renderHistoryRange(&histBuf, m.iterationHistory, innerWidth, reasoningW, thinkingW, reasoningGuide, reasoningStyle, thinkingGuide, thinkingStyle, iterStyle, toolDoneStyle, toolErrorStyle, elapsedStyle, dimStyle, s)
		m.cachedProgressHistory = histBuf.String()
		m.cachedProgressHistoryLen = len(m.iterationHistory)
		m.cachedProgressHistoryWidth = bubbleWidth
		m.cachedProgressHistoryFP = fnvHash64(m.cachedProgressHistory)
		// Full padded lines rebuild
		m.cachedProgressHistoryLines = padLinesFromContent(m.cachedProgressHistory)
		historyLines = m.cachedProgressHistoryLines
	}

	// Render current iteration into a separate buffer so we can
	// pad only the dynamic parts while reusing the pre-padded
	// history lines cache.
	var currentBuf strings.Builder

	if m.progress != nil {
		currentBuf.WriteString(iterStyle.Render(fmt.Sprintf("#%d", m.progress.Iteration)))
		currentBuf.WriteString("\n")

		// Render all current-iteration content with correct linear order.
		// Static cache (completed tools + content) is inserted mid-stream.
		m.renderCurrentIteration(&currentBuf, s, innerWidth, reasoningW, thinkingW, reasoningGuide, reasoningStyle, thinkingGuide, thinkingStyle, toolDoneStyle, toolErrorStyle, toolRunningStyle, elapsedStyle, dimStyle, iterStyle)
	} else if m.typing {
		currentBuf.WriteString("  ")
		currentBuf.WriteString(m.ticker.viewFrames(orbitFrames))
		currentBuf.WriteString(thinkingStyle.Render(" " + m.pickVerb(m.ticker.ticks) + "..."))
		currentBuf.WriteString("\n")
	}

	currentContent := strings.TrimRight(currentBuf.String(), "\n")
	if currentContent == "" && len(historyLines) == 0 {
		return ""
	}

	// Total elapsed — quantize to whole seconds so the fingerprint stays stable
	// across 100ms ticks. Without this, formatElapsed changes every 100ms
	// (e.g. "1.2s" → "1.3s"), preventing the cache from ever hitting.
	var elapsedSec int64
	if !m.typingStartTime.IsZero() {
		elapsedSec = time.Since(m.typingStartTime).Milliseconds() / 1000
	}

	// --- Full progress block output cache ---
	// Uses O(1) composite fingerprint (sub-block FPs + active tool state)
	// instead of O(N) hash of the entire pre-render string.
	fp := m.progressBlockCompositeFP(elapsedSec, bubbleWidth)
	if fp == m.cachedProgressBlockFP && bubbleWidth == m.cachedProgressBlockWidth && m.cachedProgressBlockOut != "" {
		return m.cachedProgressBlockOut
	}

	// Header
	headerStyle := s.ProgressHeader
	elapsed := ""
	if !m.typingStartTime.IsZero() {
		elapsed = " " + elapsedStyle.Render(formatElapsed(elapsedSec*1000))
	}
	header := headerStyle.Render("Progress") + elapsed

	// Assemble padded lines incrementally:
	// 1. Header line (padded)
	// 2. History lines (already padded from cache — O(1) reuse)
	// 3. Current iteration lines (padded — always short, O(1))
	// This avoids O(N) padProgressLines on the entire content.
	totalCap := 1 + 1 + len(historyLines) + 20 // header + divider + history + ~20 lines for current
	allPaddedLines := make([]string, 0, totalCap)
	allPaddedLines = append(allPaddedLines, " "+header+" ")

	// Divider at the very start of the progress block
	allPaddedLines = append(allPaddedLines, " "+s.DimGuideSt.Render(strings.Repeat("─", innerWidth))+" ")

	// History lines are already padded — directly append
	allPaddedLines = append(allPaddedLines, historyLines...)

	// Pad only the current iteration content (divider + current iter — always short)
	if currentContent != "" {
		currentLines := padLinesFromContent(currentContent)
		allPaddedLines = append(allPaddedLines, currentLines...)
	}

	// Build the final string from padded lines
	var resultBuf strings.Builder
	resultBuf.Grow(len(allPaddedLines) * (bubbleWidth + 2))
	for _, line := range allPaddedLines {
		resultBuf.WriteString(line)
		resultBuf.WriteString("\n")
	}
	result := resultBuf.String()
	// Add trailing empty line for the bottom border
	allPaddedLines = append(allPaddedLines, "")

	m.cachedProgressBlockOut = result
	m.cachedProgressBlockFP = fp
	m.cachedProgressBlockWidth = bubbleWidth
	m.cachedProgressBlockLines = allPaddedLines

	return result
}

// padLinesFromContent splits content by newlines and pads each line with
// " " prefix and " " suffix (equivalent to Padding(0,1)). Returns only
// the []string of padded lines — used for incremental line assembly
func padLinesFromContent(content string) []string {
	if content == "" {
		return nil
	}
	nlCount := strings.Count(content, "\n")
	lines := make([]string, 0, nlCount+1)
	for {
		idx := strings.IndexByte(content, '\n')
		if idx < 0 {
			if content != "" {
				lines = append(lines, " "+content+" ")
			}
			break
		}
		if idx > 0 { // skip empty lines
			lines = append(lines, " "+content[:idx]+" ")
		}
		content = content[idx+1:]
	}
	return lines
}

// progressBlockCompositeFP computes an O(1) composite fingerprint by combining
// pre-computed sub-block FPs with active tool state. This replaces the old
func (m *cliModel) progressBlockCompositeFP(elapsedSec int64, bubbleWidth int) uint64 {
	h := fnv.New64a()
	var eb [8]byte
	binary.LittleEndian.PutUint64(eb[:], uint64(elapsedSec))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(bubbleWidth))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], m.cachedProgressHistoryFP)
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], m.cachedCurrentStaticFP)
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], m.cachedReasoningBlockFP)
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], m.cachedThinkingBlockFP)
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], m.cachedStreamBlockFP)
	h.Write(eb[:])
	// Spinner tick count — ensures pulse/phase animations update every 100ms.
	// Without this, the cache stays stable for a whole second (elapsedSec is
	// quantized) and the spinner freezes.
	binary.LittleEndian.PutUint64(eb[:], uint64(m.ticker.ticks))
	h.Write(eb[:])
	// Active tools (dynamic — changes every tick during tool execution)
	if m.progress != nil {
		for _, t := range m.progress.ActiveTools {
			if t.Status != "done" && t.Status != "error" {
				h.Write([]byte(t.Name))
				h.Write([]byte(t.Status))
				binary.LittleEndian.PutUint64(eb[:], uint64(t.Elapsed))
				h.Write(eb[:])
			}
		}
	}
	return h.Sum64()
}

// progressStaticFP computes a fingerprint for the static parts of the current
// iteration's progress. When the fingerprint doesn't change between ticks,
// we can skip re-rendering reasoning/thinking/completed-tools/SubAgent tree.
func (m *cliModel) progressStaticFP() uint64 {
	if m.progress == nil {
		return 0
	}
	p := m.progress
	h := fnv.New64a()
	h.Write([]byte(p.Reasoning))
	h.Write([]byte(p.ReasoningStreamContent))
	h.Write([]byte(p.Thinking))
	h.Write([]byte(p.Phase))
	// Completed tools: count + status + label (elapsed is static after completion)
	for _, t := range p.CompletedTools {
		if t.Iteration == p.Iteration {
			h.Write([]byte(t.Name))
			h.Write([]byte(t.Status))
			var eb [8]byte
			binary.LittleEndian.PutUint64(eb[:], uint64(t.Elapsed))
			h.Write(eb[:])
		}
	}
	// Done/error active tools (also static once finished)
	for _, t := range p.ActiveTools {
		if t.Status == "done" || t.Status == "error" {
			h.Write([]byte(t.Name))
			h.Write([]byte(t.Status))
		}
	}
	// SubAgent tree structure
	for _, sa := range p.SubAgents {
		h.Write([]byte(sa.Role))
		h.Write([]byte(sa.Instance))
		h.Write([]byte(sa.Status))
	}
	return h.Sum64()
}

// reasoningBlockFP computes a fingerprint for the reasoning block cache.
// Includes cursor blink state since it affects the rendered output.
func (m *cliModel) reasoningBlockFP(innerWidth, reasoningW, cursorState int) uint64 {
	if m.progress == nil {
		return 0
	}
	h := fnv.New64a()
	reasoningText := m.progress.Reasoning
	if reasoningText == "" {
		reasoningText = m.progress.ReasoningStreamContent
	}
	h.Write([]byte(reasoningText))
	var eb [8]byte
	binary.LittleEndian.PutUint64(eb[:], uint64(m.rwVisible))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(innerWidth))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(reasoningW))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(cursorState))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(len([]rune(m.progress.ReasoningStreamContent))))
	h.Write(eb[:])
	return h.Sum64()
}

// streamBlockFP computes a fingerprint for the stream block cache.
// Includes cursor blink state since it affects the rendered output.
func (m *cliModel) streamBlockFP(innerWidth, thinkingW, cursorState int) uint64 {
	if m.progress == nil {
		return 0
	}
	h := fnv.New64a()
	h.Write([]byte(m.progress.StreamContent))
	var eb [8]byte
	binary.LittleEndian.PutUint64(eb[:], uint64(m.twVisible))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(innerWidth))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(thinkingW))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(cursorState))
	h.Write(eb[:])
	return h.Sum64()
}

// thinkingBlockFP computes a fingerprint for the thinking block cache.
func (m *cliModel) thinkingBlockFP(innerWidth, thinkingW int) uint64 {
	if m.progress == nil {
		return 0
	}
	h := fnv.New64a()
	h.Write([]byte(m.progress.Thinking))
	var eb [8]byte
	binary.LittleEndian.PutUint64(eb[:], uint64(innerWidth))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(thinkingW))
	h.Write(eb[:])
	return h.Sum64()
}

// getCurrentStaticCache returns the cached rendering of static parts for the
// current iteration (completed tools, tool content). These are truly static
// once a tool finishes — no typewriter, no cursor, no elapsed timer.
// Reasoning, thinking, stream content, active tools, and SubAgent tree are
// always rendered dynamically to maintain correct linear order.
func (m *cliModel) getCurrentStaticCache(
	bubbleWidth, innerWidth int,
	s *cliStyles,
	reasoningGuide, reasoningStyle, thinkingGuide, thinkingStyle,
	toolDoneStyle, toolErrorStyle, elapsedStyle, dimStyle, iterStyle lipgloss.Style,
) string {
	if m.progress == nil {
		m.cachedCurrentStatic = ""
		return ""
	}

	fp := m.progressStaticFP()
	if m.cachedCurrentStatic != "" &&
		m.cachedCurrentStaticWidth == bubbleWidth &&
		m.cachedCurrentIter == m.progress.Iteration &&
		m.cachedCurrentStaticFP == fp {
		return m.cachedCurrentStatic
	}

	var sb strings.Builder

	// Completed tools in current iteration
	for _, tool := range m.progress.CompletedTools {
		if tool.Iteration != m.progress.Iteration {
			continue
		}
		label, icon, sty := toolDisplayInfo(tool, toolDoneStyle, toolErrorStyle)
		var elapsedStyled string
		if tool.Elapsed > 0 {
			elapsedStyled = elapsedStyle.Render(formatElapsed(tool.Elapsed))
		}
		sb.WriteString(sty.Render(toolLine(icon, label, elapsedStyled, innerWidth)))
		sb.WriteString("\n")
	}

	// Done/error active tools (static once finished)
	for _, tool := range m.progress.ActiveTools {
		if tool.Status != "done" && tool.Status != "error" {
			continue
		}
		label, icon, sty := toolDisplayInfo(tool, toolDoneStyle, toolErrorStyle)
		var elapsedStyled string
		if tool.Elapsed > 0 {
			elapsedStyled = elapsedStyle.Render(formatElapsed(tool.Elapsed))
		}
		sb.WriteString(sty.Render(toolLine(icon, label, elapsedStyled, innerWidth)))
		sb.WriteString("\n")
	}

	// Tool content (completed + done/error active)
	guide := reasoningGuide.Render("  ┊ ")
	for i := range m.progress.CompletedTools {
		tool := &m.progress.CompletedTools[i]
		if content := m.renderToolContentBelow(tool, guide, innerWidth, false, 0); content != "" {
			sb.WriteString(content)
			sb.WriteString("\n")
		}
	}
	for i := range m.progress.ActiveTools {
		tool := &m.progress.ActiveTools[i]
		if tool.Status != "done" && tool.Status != "error" {
			continue
		}
		if content := m.renderToolContentBelow(tool, guide, innerWidth, false, 0); content != "" {
			sb.WriteString(content)
			sb.WriteString("\n")
		}
	}

	m.cachedCurrentStatic = sb.String()
	m.cachedCurrentStaticWidth = bubbleWidth
	m.cachedCurrentIter = m.progress.Iteration
	m.cachedCurrentStaticFP = fp
	return m.cachedCurrentStatic
}

// renderCurrentIteration renders the current iteration with correct linear order:
//
//  1. Reasoning (with typewriter when streaming) — cached per cursorState
//  2. Thinking — cached
//  3. Completed tools + content (from static cache)
//  4. Stream content (assistant text output) — cached per cursorState
//  5. Active tools (live elapsed)
//  6. Phase spinner (only when no content at all)
//  7. SubAgent tree
//
// Reasoning, thinking, and stream blocks are cached to avoid per-line Style.Render,
func (m *cliModel) renderCurrentIteration(
	sb *strings.Builder,
	s *cliStyles,
	innerWidth, reasoningW, thinkingW int,
	reasoningGuide, reasoningStyle, thinkingGuide, thinkingStyle,
	toolDoneStyle, toolErrorStyle, toolRunningStyle, elapsedStyle, dimStyle, iterStyle lipgloss.Style,
) {
	if m.progress == nil {
		return
	}

	cursorState := int((m.ticker.ticks / 5) % 2) // 0 or 1, changes every ~500ms

	// --- 1. Reasoning (cached) ---
	isReasoningStreaming := m.progress.ReasoningStreamContent != "" && m.progress.StreamContent == ""
	reasoningText := m.progress.Reasoning
	if reasoningText == "" {
		reasoningText = m.progress.ReasoningStreamContent
	}
	if reasoningText != "" {
		fp := m.reasoningBlockFP(innerWidth, reasoningW, cursorState)
		if m.cachedReasoningBlock != "" && m.cachedReasoningBlockFP == fp && m.cachedReasoningBlockWidth == innerWidth {
			sb.WriteString(m.cachedReasoningBlock)
		} else {
			var blockBuf strings.Builder
			// Typewriter effect for reasoning streaming
			if isReasoningStreaming {
				totalRunes := len([]rune(m.progress.ReasoningStreamContent))
				runes := []rune(m.progress.ReasoningStreamContent)
				if m.rwVisible > 0 && m.rwVisible < totalRunes {
					runes = runes[:m.rwVisible]
				}
				reasoningText = string(runes)
			}
			lines := strings.Split(reasoningText, "\n")
			reasoningTyping := isReasoningStreaming && m.rwVisible < len([]rune(m.progress.ReasoningStreamContent))
			cursorVisible := reasoningTyping || cursorState == 0
			for i, line := range lines {
				line = strings.TrimRight(line, " \t\r")
				if line == "" {
					continue
				}
				isLastLine := i == len(lines)-1
				wrappedLines := strings.Split(hardWrapRunes(line, innerWidth-reasoningW), "\n")
				for j, wl := range wrappedLines {
					isLast := isLastLine && j == len(wrappedLines)-1
					guide := reasoningGuide.Render("  ┊ ")
					if isLast && isReasoningStreaming {
						cursorStr := s.StreamCursor.Render("▋")
						cursorOverflow := reasoningW+lipgloss.Width(wl)+lipgloss.Width("▋") > innerWidth
						if cursorOverflow {
							blockBuf.WriteString(guide + reasoningStyle.Render(wl))
							blockBuf.WriteString("\n")
							if cursorVisible {
								blockBuf.WriteString(guide + cursorStr)
							} else {
								blockBuf.WriteString(guide)
							}
						} else if cursorVisible {
							blockBuf.WriteString(guide + reasoningStyle.Render(wl) + cursorStr)
						} else {
							blockBuf.WriteString(guide + reasoningStyle.Render(wl))
						}
					} else {
						blockBuf.WriteString(guide + reasoningStyle.Render(wl))
					}
					blockBuf.WriteString("\n")
				}
			}
			m.cachedReasoningBlock = blockBuf.String()
			m.cachedReasoningBlockFP = fp
			m.cachedReasoningBlockWidth = innerWidth
			sb.WriteString(m.cachedReasoningBlock)
		}
	}

	// --- 2. Thinking (cached) ---
	if m.progress.Thinking != "" {
		fp := m.thinkingBlockFP(innerWidth, thinkingW)
		if m.cachedThinkingBlock != "" && m.cachedThinkingBlockFP == fp && m.cachedThinkingBlockWidth == innerWidth {
			sb.WriteString(m.cachedThinkingBlock)
		} else {
			var blockBuf strings.Builder
			for _, line := range strings.Split(m.progress.Thinking, "\n") {
				line = strings.TrimRight(line, " \t\r")
				if line == "" {
					continue
				}
				for _, wl := range strings.Split(hardWrapRunes(line, innerWidth-thinkingW), "\n") {
					blockBuf.WriteString(thinkingGuide.Render("  ┊ ") + thinkingStyle.Render(wl))
					blockBuf.WriteString("\n")
				}
			}
			m.cachedThinkingBlock = blockBuf.String()
			m.cachedThinkingBlockFP = fp
			m.cachedThinkingBlockWidth = innerWidth
			sb.WriteString(m.cachedThinkingBlock)
		}
	}

	// --- 3. Completed tools + tool content (static cache) ---
	bubbleWidth := innerWidth + 4 // match the padding used elsewhere
	static := m.getCurrentStaticCache(bubbleWidth, innerWidth, s, reasoningGuide, reasoningStyle, thinkingGuide, thinkingStyle, toolDoneStyle, toolErrorStyle, elapsedStyle, dimStyle, iterStyle)
	if static != "" {
		sb.WriteString(static)
	}

	// --- 4. Stream content (assistant text output, cached) ---
	hasTools := len(m.progress.ActiveTools) > 0 || len(m.progress.CompletedTools) > 0

	if m.progress.StreamContent != "" {
		fp := m.streamBlockFP(innerWidth, thinkingW, cursorState)
		if m.cachedStreamBlock != "" && m.cachedStreamBlockFP == fp && m.cachedStreamBlockWidth == innerWidth {
			sb.WriteString(m.cachedStreamBlock)
		} else {
			var blockBuf strings.Builder
			totalRunes := len([]rune(m.progress.StreamContent))
			runes := []rune(m.progress.StreamContent)
			if m.twVisible > 0 && m.twVisible < totalRunes {
				runes = runes[:m.twVisible]
			}
			streamText := string(runes)
			lines := strings.Split(streamText, "\n")
			typing := m.twVisible < totalRunes
			cursorVisible := typing || cursorState == 0
			for i, line := range lines {
				line = strings.TrimRight(line, " \t\r")
				if line == "" {
					continue
				}
				isLastLine := i == len(lines)-1
				wrappedLines := strings.Split(hardWrapRunes(line, innerWidth-thinkingW), "\n")
				for j, wl := range wrappedLines {
					isLast := isLastLine && j == len(wrappedLines)-1
					guide := thinkingGuide.Render("  ┊ ")
					if isLast {
						cursorStr := s.StreamCursor.Render("▋")
						cursorOverflow := thinkingW+lipgloss.Width(wl)+lipgloss.Width("▋") > innerWidth
						if cursorOverflow {
							blockBuf.WriteString(guide + thinkingStyle.Render(wl))
							blockBuf.WriteString("\n")
							if cursorVisible {
								blockBuf.WriteString(guide + cursorStr)
							} else {
								blockBuf.WriteString(guide)
							}
						} else if cursorVisible {
							blockBuf.WriteString(guide + thinkingStyle.Render(wl) + cursorStr)
						} else {
							blockBuf.WriteString(guide + thinkingStyle.Render(wl))
						}
					} else {
						blockBuf.WriteString(guide + thinkingStyle.Render(wl))
					}
					blockBuf.WriteString("\n")
				}
			}
			m.cachedStreamBlock = blockBuf.String()
			m.cachedStreamBlockFP = fp
			m.cachedStreamBlockWidth = innerWidth
			sb.WriteString(m.cachedStreamBlock)
		}
	} else if !hasTools {
		// Phase spinner only when no content at all
		hasReasoning := m.progress.Reasoning != "" || m.progress.ReasoningStreamContent != ""
		hasThinking := m.progress.Thinking != ""
		if !hasReasoning && !hasThinking {
			switch m.progress.Phase {
			case "thinking":
				sb.WriteString("  ")
				sb.WriteString(m.ticker.view())
				sb.WriteString(thinkingStyle.Render(" " + m.pickVerb(m.ticker.ticks) + "..."))
				sb.WriteString("\n")
			case "compressing":
				sb.WriteString("  ")
				sb.WriteString(m.ticker.viewFrames(orbitFrames))
				sb.WriteString(thinkingStyle.Render(" compressing..."))
				sb.WriteString("\n")
			case "newing":
				sb.WriteString("  ")
				sb.WriteString(m.ticker.viewFrames(orbitFrames))
				sb.WriteString(thinkingStyle.Render(" resetting session..."))
				sb.WriteString("\n")
			case "retrying":
				sb.WriteString("  ")
				sb.WriteString(m.ticker.viewFrames(orbitFrames))
				sb.WriteString(thinkingStyle.Render(" retrying..."))
				sb.WriteString("\n")
			}
		}
	}

	// --- 5. Active tools (live elapsed + pulse) ---
	for _, tool := range m.progress.ActiveTools {
		if tool.Status == "done" || tool.Status == "error" {
			continue
		}
		label, _, _ := toolDisplayInfo(tool, toolRunningStyle, lipgloss.Style{})
		pulseIcon := m.ticker.viewFrames(pulseFrames)
		var elapsedMs int64
		if !tool.StartedAt.IsZero() {
			elapsedMs = time.Since(tool.StartedAt).Milliseconds()
		} else {
			elapsedMs = tool.Elapsed
		}
		elapsedStyled := elapsedStyle.Render(formatElapsed(elapsedMs))
		sb.WriteString(toolRunningStyle.Render(toolLine(pulseIcon, label, elapsedStyled, innerWidth)))
		sb.WriteString("\n")
	}

	// --- 6. SubAgent tree ---
	if len(m.progress.SubAgents) > 0 {
		var treeSB strings.Builder
		// Use "  ┊ " as the top-level prefix so child agent lines align
		// with the parent tool line's guide prefix from toolLine().
		m.renderSubAgentTree(&treeSB, m.progress.SubAgents, "  ┊ ", innerWidth)
		if treeSB.Len() > 0 {
			sb.WriteString("\n")
			sb.WriteString(treeSB.String())
		}
	}
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

// renderMessage 渲染单条消息为 ANSI 字符串（§1 增量渲染：自包含方法）
// toolDisplayInfo 从工具进度条目中提取显示用的 label、状态图标和样式。

// renderToolContentBelow renders tool body content below a tool line.
// Shows ToolHints (diff) first, then per-tool body (Read/Shell/Grep/Glob output).
// guide is the prefix for each line (e.g. "  ┊ ").
// dimmed controls whether the content is dimmed (for history iterations).
// maxLines caps diff rendering (0 = unlimited). Passed through to renderToolHint.

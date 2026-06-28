package cli

import (
	"fmt"
	"reflect"
	"strings"
	"unsafe"

	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
)

func (m *cliModel) setViewportContent(content string) {
	// Deduplicate: skip if content and width haven't changed.
	// During resize storms or high-frequency ticks (busy state), this prevents
	// O(N*W) hardWrapRunes from running every 100ms on the same content.
	cw := m.chatWidth()
	if content == m.rc.vpContent && cw == m.rc.vpWidth && m.ready {
		return
	}
	m.rc.vpContent = content
	m.rc.vpWidth = cw

	// Track actual max width across all wrapped lines.
	// The performance optimization bypasses viewport's internal maxLineWidth()
	// scan (which was ~49% CPU), but we must pass the REAL max width — not cw —
	// to viewportSetLinesBypassMaxWidth. Otherwise viewport's visibleLines()
	// trusts longestLineWidth <= maxWidth and skips truncation. Any line wider
	// than maxWidth slips through, lipgloss.Wrap in viewport.View() re-wraps it,
	// producing extra rendered lines that overflow the viewport height and push
	// the input box off screen.
	maxW := 0
	var lines []string // pre-split lines to avoid viewport's strings.Split
	if cw > 0 {
		// Two-tier wrap: find the cachedHistory boundary in content.
		// The history portion is stable (doesn't change between ticks) — reuse
		// its wrapped version to avoid O(N*W) hardWrapRunes on the growing history.
		historyEnd := 0
		if len(m.rc.history) > 0 && strings.HasPrefix(content, m.rc.history) {
			historyEnd = len(m.rc.history)
		}

		if historyEnd > 0 && m.rc.wrapRaw == m.rc.history && m.rc.wrapWidth == cw {
			// Fast path: reuse cached history lines (avoids O(N) strings.Split every tick).
			// cachedHistoryLines is pre-split; only the dynamic suffix needs wrap.
			if len(m.rc.histLines) > 0 {
				lines = make([]string, len(m.rc.histLines))
				copy(lines, m.rc.histLines)
				maxW = m.rc.histMaxW
			}
			dynamicPart := content[historyEnd:]
			if dynamicPart != "" {
				for _, line := range strings.Split(dynamicPart, "\n") {
					trimmed := strings.TrimRight(line, " \t")
					if trimmed != line {
						visualW := lipgloss.Width(line)
						trimmedW := lipgloss.Width(trimmed)
						if visualW == trimmedW {
							line = trimmed
						}
					}
					wrapped := wrapPreservingGuide(line, cw)
					for _, wl := range wrapped {
						if w := lipgloss.Width(wl); w > maxW {
							maxW = w
						}
					}
					lines = append(lines, wrapped...)
				}
			}
		} else {
			// Slow path: wrap everything and cache the history portion
			rawLines := strings.Split(content, "\n")
			historyLineCount := 0
			if historyEnd > 0 {
				historyLineCount = strings.Count(m.rc.history, "\n")
				if len(m.rc.history) > 0 && m.rc.history[len(m.rc.history)-1] != '\n' {
					historyLineCount++
				}
			}
			var wrappedHistoryParts []string
			for i, line := range rawLines {
				trimmed := strings.TrimRight(line, " \t")
				if trimmed != line {
					visualW := lipgloss.Width(line)
					trimmedW := lipgloss.Width(trimmed)
					if visualW == trimmedW {
						line = trimmed
					}
				}
				wrapped := wrapPreservingGuide(line, cw)
				for _, wl := range wrapped {
					if w := lipgloss.Width(wl); w > maxW {
						maxW = w
					}
				}
				if i < historyLineCount {
					wrappedHistoryParts = append(wrappedHistoryParts, wrapped...)
				}
				lines = append(lines, wrapped...)
			}
			// Cache the wrapped history portion (both string and []string) for next tick
			if historyEnd > 0 && len(wrappedHistoryParts) > 0 {
				m.rc.wrapHistory = strings.Join(wrappedHistoryParts, "\n") + "\n"
				m.rc.histLines = wrappedHistoryParts
				m.rc.bumpHistGen() // invalidate allLines cache — histLines rebuilt
				m.rc.wrapRaw = m.rc.history
				m.rc.wrapWidth = cw
				m.rc.histMaxW = maxW
			}
		}
	} else {
		lines = strings.Split(content, "\n")
	}

	// Use userScrolledUp to determine follow-bottom behavior.
	// AtBottom() alone can false-positive when content shrinks (maxYOffset
	// decreases below current yOffset). userScrolledUp is only set by
	// explicit user scroll-up actions, making it reliable.
	shouldFollowBottom := !m.userScrolledUp
	prevYOffset := m.viewport.YOffset()
	// Use SetContentLines with pre-split lines to avoid viewport's internal
	// strings.Split. We also bypass the expensive maxLineWidth scan inside
	// SetContentLines by directly setting the internal lines and width.
	viewportSetLinesBypassMaxWidth(&m.viewport, lines, maxW)
	if shouldFollowBottom {
		m.viewport.GotoBottom()
		m.newContentHint = false
	} else {
		// Defensive: if the viewport height was changed by autoExpandInput
		// or relayoutViewport, the yOffset may now exceed maxYOffset.
		// Restore user's previous scroll position, clamped to valid range.
		m.viewport.SetYOffset(prevYOffset)
		m.newContentHint = true
	}
}

// trimToolSummaryPayload releases large fields (ToolHints, Detail, Args) from
// tool_summary messages after rendering. The rendered output is cached in
// msg.rendered, so these multi-KB strings are no longer needed. Without this,
// N iterations × M tools × avg_diff_size causes O(N*M) GC pressure — the GC
// must scan all surviving strings every collection cycle, consuming CPU
func wrappedLineCount(content string, width int) int {
	if content == "" {
		return 0
	}
	if width <= 0 {
		return strings.Count(content, "\n")
	}
	count := 0
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, " \t")
		count += strings.Count(hardWrapRunes(line, width), "\n") + 1
	}
	return count
}

// visibleTurnIndices 返回每个"对话轮次"的起始 slice 索引。
// 每个 turn 以 user 消息开头，包含之后所有的 assistant/tool_summary 消息
// 直到下一个 user 消息为止。tool_summary 自动归属其前面最近的 user 所在的 turn。
//
// 例如: [user(0), assistant(1), tool_summary(2), user(3), assistant(4)]
// turns: [0, 3] — 按"1"删最后 1 轮即 cutIdx=3，保留 [user(0), assistant(1), tool_summary(2)]
func visibleTurnIndices(messages []cliMessage) []int {
	var turns []int
	for i, msg := range messages {
		if msg.role == "user" {
			turns = append(turns, i)
		}
	}
	// 如果没有 user 消息但有其他消息，回退到旧逻辑（保留兼容）
	if len(turns) == 0 && len(messages) > 0 {
		turns = append(turns, 0)
	}
	return turns
}

// visibleMsgGroupIndices 是 visibleTurnIndices 的别名，保留向后兼容。
func visibleMsgGroupIndices(messages []cliMessage) []int {
	return visibleTurnIndices(messages)
}

// updateViewportContent 更新 viewport 显示内容（§1 增量渲染）
func (m *cliModel) updateViewportContent() {
	// 快速路径：流式消息 + 缓存有效
	// dedupMessagesGuard 不需要在此路径运行：流式更新只改内容不增消息。
	if m.streamingMsgIdx >= 0 && m.rc.valid {
		if m.streamingMsgIdx >= len(m.messages) {
			m.streamingMsgIdx = -1
			m.rc.invalidateProgress()
		} else {
			m.updateStreamingOnly()
			return
		}
	}

	// 快速路径：缓存有效 + 无流式消息 + 消息数未变，只刷新 rewind block（tick 场景）
	if m.rc.valid && m.streamingMsgIdx < 0 && m.rc.msgCount == len(m.messages) {
		// progress block is now a no-op (rendered inline in streaming message).
		// Only check rewind block for changes.
		rewindBlock := m.renderRewindResultBlock()
		rewindFP := fnvHash64(rewindBlock)
		cachedHistoryLen := len(m.rc.history)
		if cachedHistoryLen == m.rc.lastTickHistLen &&
			m.rc.lastTickRewFP == rewindFP {
			return
		}
		m.rc.lastTickHistLen = cachedHistoryLen
		m.rc.lastTickRewFP = rewindFP

		// --- Direct lines assembly with cached slice ---
		cw := m.chatWidth()
		if len(m.rc.histLines) > 0 && cw > 0 {
			// Rewind block lines: cache wrapped version
			var rewindLines []string
			if rewindBlock == m.rc.dynamicRaw && cw == m.rc.dynamicWidth {
				rewindLines = m.rc.dynamicLines
			} else {
				rewindLines = wrapDynamicPart(rewindBlock, cw)
				m.rc.dynamicRaw = rewindBlock
				m.rc.dynamicLines = rewindLines
				m.rc.dynamicWidth = cw
			}

			// Reuse cached allLines when history section is unchanged.
			// Generation counter guarantees: histGen==allLinesGen means allLines
			// was built from the SAME histLines content — no stale reuse possible
			// even when line count coincidentally matches.
			histLen := len(m.rc.histLines)
			rl := len(rewindLines)
			totalLines := histLen + rl
			genMatch := m.rc.histGen == m.rc.allLinesGen

			if genMatch && histLen == m.rc.allLinesHistLen && totalLines <= cap(m.rc.allLines) {
				// History unchanged (same generation + same length) — in-place update rewind only
				m.rc.allLines = m.rc.allLines[:totalLines]
				copy(m.rc.allLines[histLen:], rewindLines)
			} else if genMatch && histLen > m.rc.allLinesHistLen && cap(m.rc.allLines) >= totalLines {
				// History grew (new iteration appended, same generation) — extend in-place.
				newHistLines := m.rc.histLines[m.rc.allLinesHistLen:]
				m.rc.allLines = m.rc.allLines[:m.rc.allLinesHistLen]
				m.rc.allLines = append(m.rc.allLines, newHistLines...)
				m.rc.allLines = append(m.rc.allLines, rewindLines...)
				m.rc.allLinesHistLen = histLen
			} else if genMatch && histLen > m.rc.allLinesHistLen {
				// History grew but slice too small — grow with append
				m.rc.allLines = m.rc.allLines[:m.rc.allLinesHistLen]
				m.rc.allLines = append(m.rc.allLines, m.rc.histLines[m.rc.allLinesHistLen:]...)
				m.rc.allLines = append(m.rc.allLines, rewindLines...)
				m.rc.allLinesHistLen = histLen
			} else {
				// Content changed (gen mismatch), history shrank, or first run — rebuild
				m.rc.allLines = make([]string, totalLines)
				copy(m.rc.allLines, m.rc.histLines)
				copy(m.rc.allLines[histLen:], rewindLines)
				m.rc.allLinesHistLen = histLen
				m.rc.allLinesGen = m.rc.histGen
			}

			shouldFollowBottom := !m.userScrolledUp
			viewportSetLinesBypassMaxWidth(&m.viewport, m.rc.allLines, cw)
			if shouldFollowBottom {
				m.viewport.GotoBottom()
				m.newContentHint = false
			} else {
				m.newContentHint = true
			}

			m.rc.vpWidth = cw
			return
		}

		// Fallback: string path (first tick before cachedHistoryLines is populated)
		var sb strings.Builder
		sb.WriteString(m.rc.history)
		sb.WriteString(rewindBlock)
		m.setViewportContent(sb.String())
		return
	}

	// 快速路径：缓存有效 + 仅追加了新消息（无流式、无搜索）
	// 只渲染新增的 dirty 消息并追加到 cachedHistory，跳过全量重建。
	if m.rc.valid && m.streamingMsgIdx < 0 && !m.searchState.mode &&
		len(m.messages) > m.rc.msgCount {
		m.appendNewMessagesToCache()
		return
	}

	// ── dedupGuard: algorithmic guarantee against duplicate rendering ──
	// Only runs on the SLOW path (cache invalid or msgCount changed).
	// Fast paths above guarantee: when rc.valid==true and msgCount unchanged,
	// no new messages were added since last dedup check → skip is safe.
	// Enforces the invariant that no two messages share the same (turnID, role)
	// identity. Uses O(n) map-based identity check, NOT string matching.
	m.dedupMessagesGuard()

	// 慢速路径：全量重建
	m.fullRebuild()
	if m.streamingMsgIdx >= 0 {
		m.updateStreamingOnly()
		return
	}
	// fullRebuild only rebuilds internal caches (cachedHistoryLines etc.)
	// — it does NOT set viewport lines. Do that now so the viewport
	// refreshes immediately, not on the next tick.
	cw := m.chatWidth()
	if cw > 0 && len(m.rc.histLines) > 0 {
		rewindBlock := m.renderRewindResultBlock()
		var rewindLines []string
		if rewindBlock != "" {
			rewindLines = wrapDynamicPart(rewindBlock, cw)
		}
		totalLines := len(m.rc.histLines) + len(rewindLines)
		m.rc.allLines = make([]string, totalLines)
		copy(m.rc.allLines, m.rc.histLines)
		copy(m.rc.allLines[len(m.rc.histLines):], rewindLines)
		m.rc.allLinesHistLen = len(m.rc.histLines)
		m.rc.allLinesGen = m.rc.histGen // sync generation so tick fast path can reuse
		viewportSetLinesBypassMaxWidth(&m.viewport, m.rc.allLines, cw)
		m.viewport.GotoBottom()
		m.newContentHint = false
	}
}

// updateStreamingOnly 只重新渲染当前流式消息（快速路径）
// Uses incremental rendering: completed iterations are cached as line arrays,
// only the live (in-progress) iteration is re-rendered per tick.
// Assembles lines directly into viewport, bypassing the string-based path.
func (m *cliModel) updateStreamingOnly() {
	cw := m.chatWidth()
	contentWidth := cw - 4
	if contentWidth < 10 {
		contentWidth = 10
	}
	msg := &m.messages[m.streamingMsgIdx]
	s := &m.styles

	// When there's no iteration data yet (turn just started, no progress arrived),
	// fall back to the full renderMessage path. This is a brief transitional state
	// that resolves within the first progress event.
	hasIterData := len(m.progressState.iterations) > 0 || m.progressState.current != nil
	if !hasIterData && len(msg.iterations) == 0 {
		var sb strings.Builder
		sb.WriteString(m.rc.history)
		msg.dirty = true
		sb.WriteString(m.renderMessage(msg))
		sb.WriteString(m.renderRewindResultBlock())
		m.setViewportContent(sb.String())
		return
	}

	// --- Determine guide style (streaming = bright) ---
	guideSt := s.GuideSt
	guideSym := "┊ "
	const ansiReset = "\x1b[0m"
	guidePrefix := ansiReset + guideSt.Render(guideSym) + ansiReset

	// --- Build or reuse header line ---
	if m.rc.streamHeaderWidth != contentWidth {
		timeStr := s.Time.Render(msg.timestamp.Format("15:04:05"))
		label := s.StreamingLabel.Render("Assistant")
		m.rc.streamHeaderLine = fmt.Sprintf("%s %s ...", guideSt.Render(guideSym)+timeStr, label)
		m.rc.streamHeaderWidth = contentWidth
	}

	// --- Render completed iterations (cached) ---
	// Uses renderTurnBody(iterations, nil, ...) to get the exact same output
	// as the full render path. Incremental: when only the count increases and width
	// is unchanged, renders only the NEW iterations and appends to cached lines.
	// Full rebuild only on width change or count reset.
	numCompleted := len(m.progressState.iterations)
	var completedLines []string
	completedMaxW := 0

	if numCompleted == m.rc.streamCompletedCount && contentWidth == m.rc.streamCompletedWidth && len(m.rc.streamCompletedLines) > 0 {
		// Cache hit: reuse pre-rendered lines for completed iterations
		completedLines = m.rc.streamCompletedLines
		completedMaxW = m.rc.streamMaxW
	} else if m.rc.streamCompletedCount > 0 && numCompleted > m.rc.streamCompletedCount && contentWidth == m.rc.streamCompletedWidth {
		// Incremental path: render only NEW iterations since last cache update.
		// This is O(1) per iteration instead of O(N) re-rendering all completed.
		completedLines = m.rc.streamCompletedLines
		completedMaxW = m.rc.streamMaxW

		oldCount := m.rc.streamCompletedCount
		newIters := m.progressState.iterations[oldCount:]
		bodyContent := m.renderTurnBody(newIters, nil, contentWidth, "")
		bodyContent = strings.TrimRight(bodyContent, "\n")
		if bodyContent != "" {
			// Handle separator between old last iteration and new first iteration.
			// renderTurnBody on newIters alone resets the internal lastKind, so the
			// first new block has no leading separator. We prepend the correct one.
			//
			// NOTE: This depends on renderTurnBody's internal implementation —
			// specifically that it uses an internal lastKind accumulator that resets
			// per call. If renderTurnBody's separator strategy changes, the
			// lastIterationBlockKind / firstIterationBlockKind logic here must be
			// updated accordingly.
			prevKind, hasPrev := lastIterationBlockKind(m.progressState.iterations[:oldCount])
			nextKind, hasNext := firstIterationBlockKind(newIters)
			if hasPrev && hasNext && needsTurnBlockSeparator(prevKind, nextKind) {
				// \n\n = different kinds, \n = same kind; splitting produces
				// one blank guide line between old and new iteration groups.
				if prevKind == nextKind || nextKind == turnBlockPulse {
					bodyContent = "\n" + bodyContent
				} else {
					bodyContent = "\n\n" + bodyContent
				}
			}
			for _, l := range strings.Split(bodyContent, "\n") {
				guideLine := guidePrefix + l
				if w := lipgloss.Width(guideLine); w > completedMaxW {
					completedMaxW = w
				}
				completedLines = append(completedLines, guideLine)
			}
		}
		// Update cache state (width unchanged)
		m.rc.streamCompletedLines = completedLines
		m.rc.streamCompletedCount = numCompleted
		m.rc.streamMaxW = completedMaxW
	} else {
		// Full rebuild: width changed, count went backwards, or first render.
		if numCompleted > 0 {
			bodyContent := m.renderTurnBody(m.progressState.iterations, nil, contentWidth, "")
			bodyContent = strings.TrimRight(bodyContent, "\n")
			if bodyContent != "" {
				for _, l := range strings.Split(bodyContent, "\n") {
					guideLine := guidePrefix + l
					if w := lipgloss.Width(guideLine); w > completedMaxW {
						completedMaxW = w
					}
					completedLines = append(completedLines, guideLine)
				}
			}
		}
		// Cache for next tick
		m.rc.streamCompletedLines = completedLines
		m.rc.streamCompletedCount = numCompleted
		m.rc.streamCompletedWidth = contentWidth
		m.rc.streamMaxW = completedMaxW
	}

	// --- Render live iteration (every tick — this is the dynamic part) ---
	// Uses renderLiveIteration directly, then combines with completed.
	// Separator logic matches renderTurnBody: no blank line when both sides
	// only have tools (running tools should be continuous with completed tools).
	var liveLines []string
	liveMaxW := 0
	if m.progressState.current != nil {
		liveBlocks := m.liveIterationBlocks(m.progressState.current, contentWidth, msg.content)
		liveContent := renderTurnBlocks(liveBlocks)
		liveContent = strings.TrimRight(liveContent, "\n")
		if liveContent != "" {
			if len(completedLines) > 0 {
				prevKind, hasPrev := lastIterationBlockKind(m.progressState.iterations)
				nextKind, hasNext := firstTurnBlockKind(liveBlocks)
				if hasPrev && hasNext && needsTurnBlockSeparator(prevKind, nextKind) {
					liveLines = append(liveLines, guidePrefix) // blank guide line as separator
				}
			}
			for _, l := range strings.Split(liveContent, "\n") {
				guideLine := guidePrefix + l
				if w := lipgloss.Width(guideLine); w > liveMaxW {
					liveMaxW = w
				}
				liveLines = append(liveLines, guideLine)
			}
		}
	}

	// --- Assemble all lines: history + header + completed + live + footer ---
	histLines := m.rc.histLines
	if len(histLines) == 0 {
		// histLines not populated yet — fall back to string path
		var sb strings.Builder
		sb.WriteString(m.rc.history)
		msg.dirty = true
		sb.WriteString(m.renderMessage(msg))
		sb.WriteString(m.renderRewindResultBlock())
		m.setViewportContent(sb.String())
		return
	}

	allLines := make([]string, 0, len(histLines)+1+len(completedLines)+len(liveLines)+2)
	allLines = append(allLines, histLines...)
	allLines = append(allLines, m.rc.streamHeaderLine) // header line
	if len(completedLines) > 0 || len(liveLines) > 0 {
		allLines = append(allLines, guidePrefix)
		if w := lipgloss.Width(guidePrefix); w > liveMaxW {
			liveMaxW = w
		}
	}
	allLines = append(allLines, completedLines...)
	allLines = append(allLines, liveLines...)
	allLines = append(allLines, "") // trailing \n\n from renderMessage

	// Compute max width
	maxW := m.rc.histMaxW
	if completedMaxW > maxW {
		maxW = completedMaxW
	}
	if liveMaxW > maxW {
		maxW = liveMaxW
	}

	shouldFollowBottom := !m.userScrolledUp
	viewportSetLinesBypassMaxWidth(&m.viewport, allLines, maxW)
	if shouldFollowBottom {
		m.viewport.GotoBottom()
		m.newContentHint = false
	} else {
		m.newContentHint = true
	}
	m.rc.vpWidth = cw
	m.rc.vpContent = "" // invalidate string dedup since we bypassed it
}

// since cachedMsgCount, updating cachedHistory and msgLineOffsets without rebuilding

var (
	viewportLinesOffset            uintptr
	viewportLongestLineWidthOffset uintptr
)

func init() {
	// Compute field offsets via reflect at init time.
	// This avoids hardcoding offsets that would break if viewport.Model changes.
	t := reflect.TypeOf(viewport.Model{})
	if f, ok := t.FieldByName("lines"); ok {
		viewportLinesOffset = f.Offset
	}
	if f, ok := t.FieldByName("longestLineWidth"); ok {
		viewportLongestLineWidthOffset = f.Offset
	}
}

// viewportSetLinesBypassMaxWidth sets viewport lines and longestLineWidth
// directly, bypassing the expensive maxLineWidth() call inside
// viewport.Model.SetContentLines. That function calls ansi.StringWidth on
// every line to find the widest — with ~9MB of content this accounts for
// ~49% of CPU during 100ms ticks (pprof 2026-05-23). Since we already wrap
// lines to chatWidth, we know the max width from our own wrap pass.
//
// The caller guarantees lines contain no embedded \r or \n (they've already
// been split and wrapped), so we skip the ContainsAny scan that was eating
// another ~13% CPU.
func viewportSetLinesBypassMaxWidth(vp *viewport.Model, lines []string, maxW int) {
	// Normalize empty content
	if len(lines) == 1 && len(lines[0]) == 0 {
		setViewportLines(vp, nil)
		setViewportLongestLineWidth(vp, 0)
		vp.ClearHighlights()
		return
	}

	// Caller guarantees no embedded \r\n — skip the scan entirely.
	setViewportLines(vp, lines)
	setViewportLongestLineWidth(vp, maxW)
	vp.ClearHighlights()
}

// setViewportLines sets the unexported 'lines' field of viewport.Model.
func setViewportLines(vp *viewport.Model, lines []string) {
	ptr := (*[]string)(unsafe.Pointer(uintptr(unsafe.Pointer(vp)) + viewportLinesOffset))
	*ptr = lines
}

// setViewportLongestLineWidth sets the unexported 'longestLineWidth' field.
func setViewportLongestLineWidth(vp *viewport.Model, w int) {
	ptr := (*int)(unsafe.Pointer(uintptr(unsafe.Pointer(vp)) + viewportLongestLineWidthOffset))
	*ptr = w
}

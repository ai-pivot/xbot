package channel

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

func (m *cliModel) setViewportContent(content string) {
	// Deduplicate: skip if content and width haven't changed.
	// During resize storms or high-frequency ticks (busy state), this prevents
	// O(N*W) hardWrapRunes from running every 100ms on the same content.
	cw := m.chatWidth()
	if content == m.lastViewportContent && cw == m.lastViewportWidth && m.ready {
		return
	}
	m.lastViewportContent = content
	m.lastViewportWidth = cw

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
		if len(m.cachedHistory) > 0 && strings.HasPrefix(content, m.cachedHistory) {
			historyEnd = len(m.cachedHistory)
		}

		if historyEnd > 0 && m.cachedWrappedHistoryRaw == m.cachedHistory && m.cachedWrappedHistoryWidth == cw {
			// Fast path: reuse cached history lines (avoids O(N) strings.Split every tick).
			// cachedHistoryLines is pre-split; only the dynamic suffix needs wrap.
			if len(m.cachedHistoryLines) > 0 {
				lines = make([]string, len(m.cachedHistoryLines))
				copy(lines, m.cachedHistoryLines)
				maxW = m.cachedHistoryMaxWidth
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
				historyLineCount = strings.Count(m.cachedHistory, "\n")
				if len(m.cachedHistory) > 0 && m.cachedHistory[len(m.cachedHistory)-1] != '\n' {
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
				m.cachedWrappedHistory = strings.Join(wrappedHistoryParts, "\n") + "\n"
				m.cachedHistoryLines = wrappedHistoryParts
				m.cachedWrappedHistoryRaw = m.cachedHistory
				m.cachedWrappedHistoryWidth = cw
				m.cachedHistoryMaxWidth = maxW
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
	if m.streamingMsgIdx >= 0 && m.renderCacheValid {
		m.updateStreamingOnly()
		return
	}

	// 快速路径：缓存有效 + 无流式消息 + 消息数未变，只刷新 progress block（tick 场景）
	if m.renderCacheValid && m.streamingMsgIdx < 0 && m.cachedMsgCount == len(m.messages) {
		// O(1) pre-check: compute composite FP without calling renderProgressBlock.
		// This avoids the O(N) renderProgressBlock call on every tick when nothing
		// changed (the dominant case during streaming within a single iteration).
		var elapsedSec int64
		if !m.typingStartTime.IsZero() {
			elapsedSec = time.Since(m.typingStartTime).Milliseconds() / 1000
		}
		bubbleWidth := m.chatWidth() - 4
		if bubbleWidth < 10 {
			bubbleWidth = 10
		}
		progressFP := m.progressBlockCompositeFP(elapsedSec, bubbleWidth)
		rewindBlock := m.renderRewindResultBlock()
		rewindFP := fnvHash64(rewindBlock)
		cachedHistoryLen := len(m.cachedHistory)
		if cachedHistoryLen == m.lastTickHistoryLen &&
			m.lastTickProgressFP == progressFP &&
			m.lastTickRewindFP == rewindFP {
			return
		}
		m.lastTickHistoryLen = cachedHistoryLen
		m.lastTickProgressFP = progressFP
		m.lastTickRewindFP = rewindFP

		// FP changed → call renderProgressBlock (it will also hit its own internal
		// cache via progressBlockCompositeFP, but we need the actual output string).
		progressBlock := m.renderProgressBlock()

		// --- Direct lines assembly with cached slice ---
		// Reuse the cached allLines slice across ticks to avoid O(N)
		// allocation + copy of cachedHistoryLines every 100ms.
		// Only progress/rewind sections are updated in-place.
		cw := m.chatWidth()
		if len(m.cachedHistoryLines) > 0 && cw > 0 {
			// Progress block lines: already pre-split by renderProgressBlock
			progressLines := m.cachedProgressBlockLines

			// Rewind block lines: cache wrapped version
			var rewindLines []string
			if rewindBlock == m.cachedDynamicRaw && cw == m.cachedDynamicWidth {
				rewindLines = m.cachedDynamicLines
			} else {
				rewindLines = wrapDynamicPart(rewindBlock, cw)
				m.cachedDynamicRaw = rewindBlock
				m.cachedDynamicLines = rewindLines
				m.cachedDynamicWidth = cw
			}

			// Reuse cached allLines when history section unchanged
			histLen := len(m.cachedHistoryLines)
			pl := len(progressLines)
			rl := len(rewindLines)
			totalLines := histLen + pl + rl

			if histLen == m.cachedAllLinesHistoryLen && totalLines <= cap(m.cachedAllLines) {
				// History unchanged — in-place update progress + rewind only
				m.cachedAllLines = m.cachedAllLines[:totalLines]
				copy(m.cachedAllLines[histLen:histLen+pl], progressLines)
				copy(m.cachedAllLines[histLen+pl:], rewindLines)
			} else if histLen > m.cachedAllLinesHistoryLen && cap(m.cachedAllLines) >= totalLines {
				// History grew (new iteration appended) — extend in-place.
				// Old history lines are unchanged, only append the new tail.
				newHistLines := m.cachedHistoryLines[m.cachedAllLinesHistoryLen:]
				m.cachedAllLines = m.cachedAllLines[:m.cachedAllLinesHistoryLen]
				m.cachedAllLines = append(m.cachedAllLines, newHistLines...)
				m.cachedAllLines = append(m.cachedAllLines, progressLines...)
				m.cachedAllLines = append(m.cachedAllLines, rewindLines...)
				m.cachedAllLinesHistoryLen = histLen
			} else if histLen > m.cachedAllLinesHistoryLen {
				// History grew but slice too small — grow with append
				m.cachedAllLines = m.cachedAllLines[:m.cachedAllLinesHistoryLen]
				m.cachedAllLines = append(m.cachedAllLines, m.cachedHistoryLines[m.cachedAllLinesHistoryLen:]...)
				m.cachedAllLines = append(m.cachedAllLines, progressLines...)
				m.cachedAllLines = append(m.cachedAllLines, rewindLines...)
				m.cachedAllLinesHistoryLen = histLen
			} else {
				// History shrank (rewind/compression) or first run — rebuild
				m.cachedAllLines = make([]string, totalLines)
				copy(m.cachedAllLines, m.cachedHistoryLines)
				copy(m.cachedAllLines[histLen:histLen+pl], progressLines)
				copy(m.cachedAllLines[histLen+pl:], rewindLines)
				m.cachedAllLinesHistoryLen = histLen
			}

			shouldFollowBottom := !m.userScrolledUp
			viewportSetLinesBypassMaxWidth(&m.viewport, m.cachedAllLines, cw)
			if shouldFollowBottom {
				m.viewport.GotoBottom()
				m.newContentHint = false
			} else {
				m.newContentHint = true
			}

			m.lastViewportWidth = cw
			return
		}

		// Fallback: string path (first tick before cachedHistoryLines is populated)
		var sb strings.Builder
		sb.WriteString(m.cachedHistory)
		sb.WriteString(progressBlock)
		sb.WriteString(rewindBlock)
		m.setViewportContent(sb.String())
		return
	}

	// 快速路径：缓存有效 + 仅追加了新消息（无流式、无搜索）
	// 只渲染新增的 dirty 消息并追加到 cachedHistory，跳过全量重建。
	if m.renderCacheValid && m.streamingMsgIdx < 0 && !m.searchMode &&
		len(m.messages) > m.cachedMsgCount {
		m.appendNewMessagesToCache()
		return
	}

	// 慢速路径：全量重建
	m.fullRebuild()
	// fullRebuild only rebuilds internal caches (cachedHistoryLines etc.)
	// — it does NOT set viewport lines. Do that now so the viewport
	// refreshes immediately, not on the next tick.
	cw := m.chatWidth()
	if cw > 0 && len(m.cachedHistoryLines) > 0 {
		progressLines := m.cachedProgressBlockLines
		rewindBlock := m.renderRewindResultBlock()
		var rewindLines []string
		if rewindBlock != "" {
			rewindLines = wrapDynamicPart(rewindBlock, cw)
		}
		totalLines := len(m.cachedHistoryLines) + len(progressLines) + len(rewindLines)
		allLines := make([]string, totalLines)
		copy(allLines, m.cachedHistoryLines)
		copy(allLines[len(m.cachedHistoryLines):], progressLines)
		copy(allLines[len(m.cachedHistoryLines)+len(progressLines):], rewindLines)
		viewportSetLinesBypassMaxWidth(&m.viewport, allLines, cw)
		m.viewport.GotoBottom()
		m.newContentHint = false
	}
}

// updateStreamingOnly 只重新渲染当前流式消息（快速路径）
func (m *cliModel) updateStreamingOnly() {
	var sb strings.Builder
	sb.WriteString(m.cachedHistory)

	// 只渲染当前流式消息
	msg := &m.messages[m.streamingMsgIdx]
	msg.dirty = true
	sb.WriteString(m.renderMessage(msg))

	// Append progress block
	sb.WriteString(m.renderProgressBlock())

	// Append rewind result block
	sb.WriteString(m.renderRewindResultBlock())

	m.setViewportContent(sb.String())
}

// since cachedMsgCount, updating cachedHistory and msgLineOffsets without rebuilding

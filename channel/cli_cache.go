package channel

import (
	"hash/fnv"
	"strings"
	"time"

	"xbot/protocol"

	"charm.land/lipgloss/v2"
)

func (m *cliModel) mergeMessagesPreservingCache(newMessages []cliMessage) bool {
	cw := m.chatWidth()
	// Build a fast lookup from existing messages: content-based key → index.
	// Only use the first occurrence of each key to handle dedup.
	existing := make(map[string]int, len(m.messages))
	for i := range m.messages {
		key := m.messages[i].role + ":" + m.messages[i].content
		if _, exists := existing[key]; !exists {
			existing[key] = i
		}
	}

	allMatched := true
	for i := range newMessages {
		key := newMessages[i].role + ":" + newMessages[i].content
		if oldIdx, found := existing[key]; found {
			old := &m.messages[oldIdx]
			nw := &newMessages[i]
			// Inherit cache if the old message was rendered at the same width
			if old.rendered != "" && old.renderWidth == cw && !old.dirty {
				nw.rendered = old.rendered
				nw.renderWidth = old.renderWidth
				nw.dirty = false
				nw.renderedLines = old.renderedLines
				nw.wrappedLines = old.wrappedLines
				nw.wrappedMaxWidth = old.wrappedMaxWidth
				nw.wrappedWidth = old.wrappedWidth
				if len(old.iterations) > 0 && len(nw.iterations) == 0 {
					nw.iterations = old.iterations
				}
				// Compute renderedLines from cached rendered output if missing
				if nw.renderedLines == 0 && nw.rendered != "" {
					nw.renderedLines = strings.Count(nw.rendered, "\n") + 1
				}
			} else {
				allMatched = false
			}
			// Remove from lookup to avoid double-matching
			delete(existing, key)
		} else {
			allMatched = false
		}
	}
	m.messages = newMessages
	return allMatched
}

// invalidateProgressHistoryCache clears the cached rendered output of completed
// iterations so it is rebuilt on the next renderProgressBlock call.
func (m *cliModel) invalidateProgressHistoryCache() {
	m.cachedProgressHistory = ""
	m.cachedProgressHistoryLines = nil
	m.cachedProgressHistoryLen = 0
	m.cachedProgressHistoryWidth = 0
	m.cachedCurrentStatic = ""
	m.cachedCurrentStaticFP = 0
	m.cachedCurrentStaticWidth = 0
	m.cachedCurrentIter = 0
	// Invalidate reasoning/stream/thinking block caches
	m.cachedReasoningBlock = ""
	m.cachedReasoningBlockFP = 0
	m.cachedReasoningBlockWidth = 0
	m.cachedStreamBlock = ""
	m.cachedStreamBlockFP = 0
	m.cachedStreamBlockWidth = 0
	m.cachedThinkingBlock = ""
	m.cachedThinkingBlockFP = 0
	m.cachedThinkingBlockWidth = 0
	// Full progress block output cache
	m.cachedProgressBlockOut = ""
	m.cachedProgressBlockFP = 0
	m.cachedProgressBlockWidth = 0
	m.cachedProgressBlockLines = nil
	m.cachedProgressHistoryFP = 0
}

// resetProgressState resets iteration tracking for a new agent turn.
func (m *cliModel) resetProgressState() {
	m.iterationHistory = nil
	m.lastSeenIteration = 0
	m.lastProgressSeq = 0
	m.lastReasoning = ""
	m.reasoningByIter = nil
	m.progress = nil
	m.iterationStartTime = time.Now() // wall-clock start for iteration 0
	m.typingStartTime = time.Now()
	m.invalidateProgressHistoryCache()
}

// collectAllTools gathers all tools from iteration history into a flat slice.
func (m *cliModel) collectAllTools() []protocol.ToolProgress {
	var all []protocol.ToolProgress
	for _, snap := range m.iterationHistory {
		all = append(all, snap.Tools...)
	}
	return all
}

func (m *cliModel) trimToolSummaryPayload(msg *cliMessage) {
	if msg.role != "tool_summary" || msg.dirty {
		return
	}
	for i := range msg.iterations {
		for k := range msg.iterations[i].Tools {
			msg.iterations[i].Tools[k].ToolHints = ""
			msg.iterations[i].Tools[k].Detail = ""
			msg.iterations[i].Tools[k].Args = ""
		}
	}
}

// wrappedLineCount returns the number of viewport display lines after hard-wrapping.
// The logic mirrors setViewportContent exactly so that msgLineOffsets (computed via
func (m *cliModel) appendNewMessagesToCache() {
	var sb strings.Builder
	sb.WriteString(m.cachedHistory)

	// Calculate starting line offset for new messages
	cw := m.chatWidth()
	runningLines := 0
	if len(m.msgLineOffsets) > 0 {
		// Approximate: use the line count of cachedHistory at current width.
		// This is an estimate but sufficient for msgLineOffsets (used for Ctrl+E folding).
		runningLines = wrappedLineCount(m.cachedHistory, cw)
	}

	startIdx := m.cachedMsgCount
	for i := startIdx; i < len(m.messages); i++ {
		msg := &m.messages[i]
		m.msgLineOffsets = append(m.msgLineOffsets, runningLines)
		rendered := m.renderMessage(msg)
		msg.rendered = rendered
		msg.dirty = false
		msg.renderWidth = cw
		// Release large fields from tool_summary iterations after rendering.
		m.trimToolSummaryPayload(msg)
		sb.WriteString(rendered)
		runningLines += wrappedLineCount(rendered, cw)
	}

	m.cachedHistory = sb.String()
	m.renderCacheValid = true
	m.cachedMsgCount = len(m.messages)

	// Invalidate cachedHistoryLines so setViewportContent's slow path
	// re-wraps and re-caches the lines. Without this, cachedHistoryLines
	// is stale (missing the new messages) and the tick fast path renders
	// incomplete history, causing visual duplication or missing content.
	m.cachedHistoryLines = nil
	m.cachedWrappedHistoryRaw = ""

	// Set viewport with new content + progress block
	var vp strings.Builder
	vp.WriteString(m.cachedHistory)
	vp.WriteString(m.renderProgressBlock())
	vp.WriteString(m.renderRewindResultBlock())
	m.setViewportContent(vp.String())
}

// fullRebuild 全量重建渲染缓存（慢速路径）
func (m *cliModel) fullRebuild() {
	// splitIdx 确保当前流式消息不进入 cachedHistory
	splitIdx := len(m.messages)
	if m.streamingMsgIdx >= 0 {
		splitIdx = m.streamingMsgIdx
	}

	// Fast path: if all messages are already cached at current width,
	// no re-rendering or re-wrapping is needed. Just rebuild cachedHistory
	// from per-message wrapped lines and rebuild msgLineOffsets.
	cw := m.chatWidth()
	allCached := splitIdx > 0
	for i := range m.messages[:splitIdx] {
		if m.messages[i].dirty || m.messages[i].renderWidth != cw || len(m.messages[i].wrappedLines) == 0 || m.messages[i].wrappedWidth != cw {
			allCached = false
			break
		}
	}
	if allCached {
		m.msgLineOffsets = m.msgLineOffsets[:0]
		runningLines := 0
		hmax := 0
		var allWrappedLines []string
		for i := range m.messages[:splitIdx] {
			m.msgLineOffsets = append(m.msgLineOffsets, runningLines)
			wl := m.messages[i].wrappedLines
			if m.messages[i].wrappedMaxWidth > hmax {
				hmax = m.messages[i].wrappedMaxWidth
			}
			runningLines += len(wl)
			allWrappedLines = append(allWrappedLines, wl...)
		}
		var histBuf strings.Builder
		for _, line := range allWrappedLines {
			histBuf.WriteString(line)
			histBuf.WriteString("\n")
		}
		m.cachedHistory = histBuf.String()
		m.cachedHistoryLines = allWrappedLines
		m.cachedWrappedHistory = m.cachedHistory
		m.cachedWrappedHistoryRaw = m.cachedHistory
		m.cachedWrappedHistoryWidth = cw
		m.cachedHistoryMaxWidth = hmax
		m.renderCacheValid = true
		m.cachedMsgCount = len(m.messages)
		return
	}

	// §19 重置消息行号偏移（基于折行后的 viewport 行号）
	m.msgLineOffsets = m.msgLineOffsets[:0]
	runningLines := 0
	// cw already declared in fast path above

	// Collect wrapped lines incrementally to avoid the O(N) strings.Split +
	// lipgloss.Width + wrapPreservingGuide on the entire cachedHistory.
	// Each message contributes its own wrapped lines; cached messages reuse
	// their pre-computed wrapped lines (no re-parsing needed).
	var allWrappedLines []string
	hmax := 0
	for i := range m.messages[:splitIdx] {
		// §19 记录消息在 viewport 折行后内容中的起始行号
		m.msgLineOffsets = append(m.msgLineOffsets, runningLines)
		needsRender := m.messages[i].dirty || m.messages[i].renderWidth != cw
		var rendered string
		if needsRender {
			rendered = m.renderMessage(&m.messages[i])
			m.messages[i].rendered = rendered
			m.messages[i].dirty = false
			m.messages[i].renderWidth = cw
			// Release large fields from tool_summary iterations after rendering.
			// The rendered output is cached in msg.rendered — ToolHints (diff),
			// Detail (tool output), and Args (raw JSON) are no longer needed.
			// Keeping them alive causes O(iterations × tool_size) GC pressure.
			m.trimToolSummaryPayload(&m.messages[i])
		} else {
			rendered = m.messages[i].rendered
		}
		// Wrap lines for this message only and collect into allWrappedLines.
		// For cached messages, pre-compute wrappedLines if not already set.
		msgWrapped := m.messages[i].wrappedLines
		msgMaxW := m.messages[i].wrappedMaxWidth
		if len(msgWrapped) == 0 || m.messages[i].wrappedWidth != cw {
			// Need to (re-)compute wrapped lines for this message
			rawLines := strings.Split(rendered, "\n")
			msgWrapped = make([]string, 0, len(rawLines))
			msgMaxW = 0
			for _, line := range rawLines {
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
					if w := lipgloss.Width(wl); w > msgMaxW {
						msgMaxW = w
					}
				}
				msgWrapped = append(msgWrapped, wrapped...)
			}
			m.messages[i].wrappedLines = msgWrapped
			m.messages[i].wrappedMaxWidth = msgMaxW
			m.messages[i].wrappedWidth = cw
		}
		if msgMaxW > hmax {
			hmax = msgMaxW
		}
		// §21 搜索高亮：匹配消息前插入指示条
		if m.searchMode && m.isSearchMatch(i) {
			indicator := m.styles.SearchIndicator.Render("▸ ")
			allWrappedLines = append(allWrappedLines, indicator)
			runningLines++
			if w := lipgloss.Width(indicator); w > hmax {
				hmax = w
			}
		}
		runningLines += len(msgWrapped)
		allWrappedLines = append(allWrappedLines, msgWrapped...)
	}

	// Rebuild cachedHistory from allWrappedLines for setViewportContent.
	// This is O(total_lines) string join — unavoidable but much cheaper than
	// the previous O(total_content_chars) approach of re-parsing ANSI codes.
	var histBuf strings.Builder
	for _, line := range allWrappedLines {
		histBuf.WriteString(line)
		histBuf.WriteString("\n")
	}
	m.cachedHistory = histBuf.String()
	m.renderCacheValid = true
	m.cachedMsgCount = len(m.messages)

	// All wrapped lines are already computed per-message above.
	// Set the cache directly — no need to re-split + re-wrap the entire
	// cachedHistory string (that was the O(N) bottleneck).
	m.cachedHistoryLines = allWrappedLines
	m.cachedWrappedHistory = m.cachedHistory
	m.cachedWrappedHistoryRaw = m.cachedHistory
	m.cachedWrappedHistoryWidth = cw
	m.cachedHistoryMaxWidth = hmax

	// 拼接最终内容：历史 + 当前流式消息（如有） + progress block + rewind result
	var sb strings.Builder
	sb.WriteString(m.cachedHistory)
	if m.streamingMsgIdx >= 0 {
		sb.WriteString(m.renderMessage(&m.messages[m.streamingMsgIdx]))
	}
	sb.WriteString(m.renderProgressBlock())
	sb.WriteString(m.renderRewindResultBlock())

	m.setViewportContent(sb.String())
}

// fnvHash64 returns a fast FNV-1a hash of s for O(1) dirty detection.
func fnvHash64(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

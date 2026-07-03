package cli

import (
	"hash/fnv"
	"strings"
	"time"

	"xbot/protocol"

	log "xbot/logger"

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

// resetProgressState resets iteration tracking for a new agent turn.
func (m *cliModel) resetProgressState() {
	m.progressState.iterations = nil
	m.progressState.lastIter = 0
	m.progressState.lastSeq = 0
	// LINEAR CONSISTENCY: reset lastAppliedSeq on new turn.
	// Backend's progressSeq is a local atomic.Uint64 in buildMainRunConfig,
	// reset to 0 on every Run(). If we don't reset, events from the new turn
	// (Seq=1,2,3...) would be blocked by the previous turn's high Seq.
	m.progressState.lastAppliedSeq = 0
	m.progressState.lastStreamSeq = 0
	m.progressState.current = nil
	m.progressState.iterStart = time.Now() // wall-clock start for iteration 0
	m.typingStartTime = time.Now()
	m.rc.invalidateProgress()
}

// collectAllTools gathers all tools from iteration history into a flat slice.
func (m *cliModel) collectAllTools() []protocol.ToolProgress {
	var all []protocol.ToolProgress
	for _, snap := range m.progressState.iterations {
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

// rerenderCachedMessage truncates the render cache back to before msgIdx,
// then re-renders from msgIdx onwards via appendNewMessagesToCache.
// This is O(1 message) instead of O(N messages) fullRebuild — used when
// a single message's content changed (e.g. streaming → finalized) and we
// need to update its cached rendering without flickering.
//
// The history string rebuild is O(total_lines) string join, but this is
// orders of magnitude cheaper than O(N × glamour_render) in fullRebuild.
func (m *cliModel) rerenderCachedMessage(msgIdx int) {
	if msgIdx < 0 || msgIdx >= len(m.messages) {
		log.Debug("rerenderCachedMessage: msgIdx out of bounds, skipping",
			"msgIdx", msgIdx, "lenMessages", len(m.messages))
		return
	}
	// If the message hasn't been cached yet (e.g. cancel ack before
	// endAgentTurn cached it), just call appendNewMessagesToCache which
	// renders from rc.msgCount onwards — picks up this message naturally.
	if msgIdx >= m.rc.msgCount {
		log.Debug("rerenderCachedMessage: message not yet cached, using appendNewMessagesToCache",
			"msgIdx", msgIdx, "msgCount", m.rc.msgCount)
		m.appendNewMessagesToCache()
		return
	}
	// Truncate histLines back to before this message.
	if msgIdx < len(m.msgLineOffsets) {
		lineStart := m.msgLineOffsets[msgIdx]
		m.rc.histLines = m.rc.histLines[:lineStart]
	}
	// Truncate msgLineOffsets. Guard against invariant deviation:
	// msgIdx should always be ≤ len(msgLineOffsets) because msgIdx < rc.msgCount
	// (checked above) and rc.msgCount ≤ len(msgLineOffsets) (cache invariant).
	// If the invariant is broken, fall back to fullRebuild instead of panicking.
	if msgIdx <= len(m.msgLineOffsets) {
		m.msgLineOffsets = m.msgLineOffsets[:msgIdx]
	} else {
		log.Warn("rerenderCachedMessage: msgLineOffsets invariant broken, falling back to fullRebuild",
			"msgIdx", msgIdx, "lenMsgLineOffsets", len(m.msgLineOffsets), "msgCount", m.rc.msgCount)
		m.rc.valid = false
		return
	}
	// Rebuild history string from truncated histLines.
	var sb strings.Builder
	for _, line := range m.rc.histLines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	m.rc.history = sb.String()
	// Set msgCount so appendNewMessagesToCache re-renders from msgIdx.
	m.rc.msgCount = msgIdx
	// Keep rc.valid = true — appendNewMessagesToCache is the fast path.
	m.appendNewMessagesToCache()
}

// wrappedLineCount returns the number of viewport display lines after hard-wrapping.
// The logic mirrors setViewportContent exactly so that msgLineOffsets (computed via
func (m *cliModel) appendNewMessagesToCache() {
	cw := m.chatWidth()
	var sb strings.Builder
	sb.WriteString(m.rc.history)

	// Calculate starting line offset for new messages
	runningLines := 0
	if len(m.rc.histLines) > 0 {
		runningLines = len(m.rc.histLines)
	} else if len(m.msgLineOffsets) > 0 {
		runningLines = wrappedLineCount(m.rc.history, cw)
	}

	startIdx := m.rc.msgCount
	for i := startIdx; i < len(m.messages); i++ {
		msg := &m.messages[i]
		m.msgLineOffsets = append(m.msgLineOffsets, runningLines)
		rendered := m.renderMessage(msg)
		msg.rendered = rendered
		msg.dirty = false
		msg.renderWidth = cw
		m.trimToolSummaryPayload(msg)
		sb.WriteString(rendered)
		// Wrap rendered lines and append to histLines so the tick fast path
		// can assemble viewport content without setViewportContent's re-wrap.
		for _, line := range strings.Split(rendered, "\n") {
			for _, wl := range wrapPreservingGuide(line, cw) {
				if w := lipgloss.Width(wl); w > m.rc.histMaxW {
					m.rc.histMaxW = w
				}
				m.rc.histLines = append(m.rc.histLines, wl)
				runningLines++
			}
		}
	}

	m.rc.history = sb.String()
	m.rc.valid = true
	m.rc.msgCount = len(m.messages)

	// Sync wrap cache so setViewportContent's fast path can reuse histLines
	// if it is ever called later (e.g. on terminal resize).
	m.rc.wrapHistory = m.rc.history
	m.rc.wrapRaw = m.rc.history
	m.rc.wrapWidth = cw
	m.rc.bumpHistGen() // invalidate allLines cache — histLines changed

	// Set viewport lines directly using the same efficient path as the tick
	// fast path (viewportSetLinesBypassMaxWidth). This avoids the expensive
	// re-wrap inside setViewportContent while still updating the viewport.
	// In the streaming flow, the viewport already has the correct content
	// from updateStreamingOnly, so this is a no-op visually (no flicker).
	// In non-streaming flows (tests, session restore), this ensures the
	// viewport is actually populated.
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
	m.rc.allLinesGen = m.rc.histGen
	viewportSetLinesBypassMaxWidth(&m.viewport, m.rc.allLines, m.rc.histMaxW)

	// Prime tick fast-path dedup keys so the next tick skips redundant work.
	m.rc.lastTickHistLen = len(m.rc.history)
	m.rc.lastTickRewFP = fnvHash64(rewindBlock)
}

// fullRebuild 全量重建渲染缓存（慢速路径）
func (m *cliModel) fullRebuild() {
	// splitIdx 确保当前流式消息不进入 cachedHistory
	splitIdx := len(m.messages)
	if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
		splitIdx = m.streamingMsgIdx
	}
	if m.streamingMsgIdx >= 0 && m.streamingMsgIdx >= len(m.messages) {
		// Session switch or reset: streamingMsgIdx is stale, clear it
		m.streamingMsgIdx = -1
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
		m.rc.history = histBuf.String()
		m.rc.histLines = allWrappedLines
		m.rc.bumpHistGen() // invalidate allLines cache — content changed
		m.rc.wrapHistory = m.rc.history
		m.rc.wrapRaw = m.rc.history
		m.rc.wrapWidth = cw
		m.rc.histMaxW = hmax
		m.rc.valid = true
		m.rc.msgCount = splitIdx
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
		if m.searchState.mode && m.isSearchMatch(i) {
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
	m.rc.history = histBuf.String()
	m.rc.valid = true
	m.rc.msgCount = splitIdx

	// All wrapped lines are already computed per-message above.
	// Set the cache directly — no need to re-split + re-wrap the entire
	// cachedHistory string (that was the O(N) bottleneck).
	m.rc.histLines = allWrappedLines
	m.rc.bumpHistGen() // invalidate allLines cache — content changed
	m.rc.wrapHistory = m.rc.history
	m.rc.wrapRaw = m.rc.history
	m.rc.wrapWidth = cw
	m.rc.histMaxW = hmax

	// 拼接最终内容：历史 + 当前流式消息（如有） + progress block + rewind result
	var sb strings.Builder
	sb.WriteString(m.rc.history)
	if m.streamingMsgIdx >= 0 {
		sb.WriteString(m.renderMessage(&m.messages[m.streamingMsgIdx]))
	}
	sb.WriteString(m.renderRewindResultBlock())

	m.setViewportContent(sb.String())
}

// fnvHash64 returns a fast FNV-1a hash of s for O(1) dirty detection.
func fnvHash64(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

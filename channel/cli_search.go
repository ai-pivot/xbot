package channel

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

func (m *cliModel) isSearchMatch(idx int) bool {
	for _, si := range m.searchResults {
		if si == idx {
			return true
		}
	}
	return false
}

// toggleMessageFold 批量切换所有 assistant 消息的折叠状态（§19）
// 如果当前有任一长消息未折叠 → 全部折叠；否则 → 全部展开。
func (m *cliModel) toggleMessageFold() {
	if len(m.messages) == 0 {
		return
	}
	// 决定目标状态：如果存在任何未折叠的长消息，则全部折叠
	anyUnfolded := false
	for i := range m.messages {
		msg := &m.messages[i]
		if msg.role == "assistant" && !msg.isPartial && !msg.folded {
			lines := msg.originalRenderedLines
			if lines == 0 {
				lines = msg.renderedLines
			}
			if lines > msgFoldThresholdLines {
				anyUnfolded = true
				break
			}
		}
	}
	targetFold := anyUnfolded

	changed := false
	for i := range m.messages {
		msg := &m.messages[i]
		if msg.role != "assistant" || msg.isPartial {
			continue
		}
		if !targetFold {
			// Unfolding: skip threshold — renderedLines reflects folded preview,
			// not original length. Only unfold messages that are currently folded.
			if !msg.folded {
				continue
			}
			msg.folded = false
			msg.dirty = true
			changed = true
			continue
		}
		// Folding: check threshold using original line count
		lines := msg.originalRenderedLines
		if lines == 0 {
			lines = msg.renderedLines
		}
		if lines <= msgFoldThresholdLines {
			continue
		}
		if !msg.folded {
			msg.folded = true
			msg.originalRenderedLines = msg.renderedLines
			msg.dirty = true
			changed = true
		}
	}
	if changed {
		m.renderCacheValid = false
		m.updateViewportContent()
	}
}

// enterSearchMode 进入搜索模式（§21）
func (m *cliModel) enterSearchMode() {
	ti := textinput.New()
	ti.Placeholder = m.locale.SearchPlaceholder
	ti.Prompt = "/"
	ti.CharLimit = 100
	ti.Focus()
	w := m.chatWidth() - 20
	if w < 20 {
		w = 20
	}
	ti.SetWidth(w)
	m.searchTI = ti
	m.searchMode = true
	m.searchEditing = true
	m.searchQuery = ""
	m.searchResults = nil
	m.searchIdx = -1
	m.renderCacheValid = false
	m.updateViewportContent()
}

// executeSearch 执行搜索（§21）
func (m *cliModel) executeSearch() {
	query := strings.TrimSpace(m.searchTI.Value())
	if query == "" {
		m.exitSearch()
		return
	}
	m.searchQuery = query
	lower := strings.ToLower(query)
	m.searchResults = nil
	for i, msg := range m.messages {
		if msg.role == "system" {
			continue
		}
		if strings.Contains(strings.ToLower(msg.content), lower) {
			m.searchResults = append(m.searchResults, i)
		}
	}
	m.searchIdx = -1
	m.searchEditing = false
	if len(m.searchResults) == 0 {
		m.showSystemMsg(m.locale.SearchNoResults, feedbackInfo)
	} else {
		m.showSystemMsg(fmt.Sprintf(m.locale.SearchResults, len(m.searchResults)), feedbackInfo)
		m.jumpToSearchResult(0)
	}
	m.renderCacheValid = false
	m.updateViewportContent()
}

// exitSearch 退出搜索模式（§21）
func (m *cliModel) exitSearch() {
	m.searchMode = false
	m.searchQuery = ""
	m.searchResults = nil
	m.searchIdx = -1
	m.searchEditing = false
	m.renderCacheValid = false
	m.updateViewportContent()
}

// jumpToSearchResult 跳转到指定搜索结果（§21）
func (m *cliModel) jumpToSearchResult(idx int) {
	if idx < 0 || idx >= len(m.searchResults) {
		return
	}
	m.searchIdx = idx
	msgIdx := m.searchResults[idx]
	if msgIdx < len(m.msgLineOffsets) {
		m.viewport.SetYOffset(m.msgLineOffsets[msgIdx])
	}
}

// typewriterTickCmd returns a command that advances the typewriter by 1 rune every 50ms.
// Runs independently from the main tick to give the typewriter its own update frequency.
func typewriterTickCmd() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
		return typewriterTickMsg{}
	})
}

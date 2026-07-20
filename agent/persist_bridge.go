package agent

import (
	"fmt"
	"regexp"
	"strings"

	"xbot/llm"
	log "xbot/logger"
	"xbot/session"
	"xbot/storage/sqlite"
)

// dynamicContextRe matches <dynamic-context>...</dynamic-context> blocks for stripping
// before persistence (same pattern as systemReminderRe in reminder.go).
var dynamicContextRe = regexp.MustCompile(`\n?\n?<dynamic-context>[\s\S]*?</dynamic-context>`)

// PersistenceBridge manages incremental session persistence for a Run() execution.
// It replaces the scattered lastPersistedCount field and inline persistence logic
// that was previously on runState.
type PersistenceBridge struct {
	session            *session.TenantSession
	lastPersistedCount int
}

// NewPersistenceBridge creates a PersistenceBridge.
// session may be nil (pure in-memory mode, no persistence).
// initialCount is the number of messages already persisted (len(initialMessages)).
func NewPersistenceBridge(sess *session.TenantSession, initialCount int) *PersistenceBridge {
	return &PersistenceBridge{
		session:            sess,
		lastPersistedCount: initialCount,
	}
}

// IncrementalPersist persists new messages (those after lastPersistedCount) to the session.
// Skips system messages. Strips <system-reminder> tags before persisting.
// Updates lastPersistedCount on success.
// Returns nil if session is nil or all messages already persisted.
func (b *PersistenceBridge) IncrementalPersist(messages []llm.ChatMessage) error {
	if b.session == nil || len(messages) <= b.lastPersistedCount {
		return nil
	}
	pending, indices := b.pendingMessages(messages)
	historyIDs, err := b.session.AppendMessages(pending)
	if err != nil {
		log.WithError(err).Error("Failed to persist messages to session")
		return fmt.Errorf("persist message batch: %w", err)
	}
	b.commitPending(messages, indices, historyIDs)
	return nil
}

// IncrementalPersistAndAskQuestion persists the new AskUser tool exchange and
// pending control in one transaction. The watermark and in-memory history IDs
// advance only after the compound commit succeeds.
func (b *PersistenceBridge) IncrementalPersistAndAskQuestion(messages []llm.ChatMessage, metadata map[string]string) error {
	if b.session == nil {
		return nil
	}
	pending, indices := b.pendingMessages(messages)
	if len(pending) == 0 {
		return fmt.Errorf("persist AskUser question: no pending messages")
	}
	historyIDs, _, err := b.session.AppendMessagesAndAskQuestion(pending, metadata)
	if err != nil {
		log.WithError(err).Error("Failed to persist AskUser exchange")
		return fmt.Errorf("persist AskUser exchange: %w", err)
	}
	b.commitPending(messages, indices, historyIDs)
	return nil
}

func (b *PersistenceBridge) pendingMessages(messages []llm.ChatMessage) ([]llm.ChatMessage, []int) {
	if len(messages) <= b.lastPersistedCount {
		return nil, nil
	}
	pending := make([]llm.ChatMessage, 0, len(messages)-b.lastPersistedCount)
	indices := make([]int, 0, len(messages)-b.lastPersistedCount)
	for idx := b.lastPersistedCount; idx < len(messages); idx++ {
		msg := messages[idx]
		if msg.Role == "system" {
			continue
		}
		persistMsg := msg
		if strings.Contains(persistMsg.Content, "<system-reminder>") {
			persistMsg.Content = stripSystemReminder(persistMsg.Content)
		}
		if strings.Contains(persistMsg.Content, "<dynamic-context>") {
			persistMsg.Content = dynamicContextRe.ReplaceAllString(persistMsg.Content, "")
		}
		pending = append(pending, persistMsg)
		indices = append(indices, idx)
	}
	return pending, indices
}

func (b *PersistenceBridge) commitPending(messages []llm.ChatMessage, indices []int, historyIDs []int64) {
	for i, historyID := range historyIDs {
		messages[indices[i]].HistoryID = historyID
	}
	b.lastPersistedCount = len(messages)
}

// RewriteAfterCompress appends a compression control record containing the new
// active context. Original history rows are never rewritten or deleted.
// Strips <system-reminder> and <dynamic-context> blocks before writing to prevent
// transient injection artifacts from being persisted.
// Updates lastPersistedCount to totalMsgCount on success.
// Returns the compression history ID. A zero ID means persistence is disabled.
func (b *PersistenceBridge) RewriteAfterCompress(sessionView []llm.ChatMessage, totalMsgCount int) (int64, error) {
	if b.session == nil {
		return 0, nil
	}
	clean := make([]llm.ChatMessage, 0, len(sessionView))
	for _, msg := range sessionView {
		if err := assertNoSystemPersist(msg); err != nil {
			continue
		}
		// Strip transient injection artifacts before persisting
		persistMsg := msg
		if strings.Contains(persistMsg.Content, "<system-reminder>") {
			persistMsg.Content = stripSystemReminder(persistMsg.Content)
		}
		if strings.Contains(persistMsg.Content, "<dynamic-context>") {
			persistMsg.Content = dynamicContextRe.ReplaceAllString(persistMsg.Content, "")
		}
		clean = append(clean, persistMsg)
	}
	historyID, err := b.session.AppendContextSnapshot(sqlite.HistoryRecordCompress, clean)
	if err != nil {
		log.WithError(err).Error("Failed to append compression history record")
		return 0, err
	}
	b.lastPersistedCount = totalMsgCount
	return historyID, nil
}

// AppendPrune records an aggressive context truncation without deleting history.
func (b *PersistenceBridge) AppendPrune(sessionView []llm.ChatMessage, totalMsgCount int) error {
	if b.session == nil {
		return nil
	}
	if _, err := b.session.AppendContextSnapshot(sqlite.HistoryRecordPrune, sessionView); err != nil {
		return fmt.Errorf("append prune history: %w", err)
	}
	b.lastPersistedCount = totalMsgCount
	return nil
}

// MarkAllPersisted updates the watermark to the given count without writing anything.
// Used after injecting synthetic messages (bg tasks, bg subagent notifications)
// that were already individually persisted.
func (b *PersistenceBridge) MarkAllPersisted(count int) {
	b.lastPersistedCount = count
}

// LastPersistedCount returns the current persistence watermark.
func (b *PersistenceBridge) LastPersistedCount() int {
	return b.lastPersistedCount
}

// ComputeEngineMessages returns messages that were produced during this Run
// (those after lastPersistedCount), for inclusion in RunOutput.EngineMessages.
// Returns nil if no new messages.
func (b *PersistenceBridge) ComputeEngineMessages(messages []llm.ChatMessage) []llm.ChatMessage {
	if len(messages) <= b.lastPersistedCount {
		return nil
	}
	engineMsgs := make([]llm.ChatMessage, len(messages)-b.lastPersistedCount)
	copy(engineMsgs, messages[b.lastPersistedCount:])
	return engineMsgs
}

// IsPersisted checks whether a message at the given index has already been persisted.
// Used by observation masking to decide if in-place updates should also be persisted.
func (b *PersistenceBridge) IsPersisted(messageIndex int) bool {
	return messageIndex < b.lastPersistedCount
}

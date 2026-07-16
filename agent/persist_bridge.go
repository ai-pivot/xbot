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
		historyID, err := b.session.AppendMessage(persistMsg)
		if err != nil {
			log.WithError(err).Error("Failed to persist message to session")
			return fmt.Errorf("persist message %d: %w", idx, err)
		}
		messages[idx].HistoryID = historyID
	}
	b.lastPersistedCount = len(messages)
	return nil
}

// RewriteAfterCompress appends a compression control record containing the new
// active context. Original history rows are never rewritten or deleted.
// Strips <system-reminder> and <dynamic-context> blocks before writing to prevent
// transient injection artifacts from being persisted.
// Updates lastPersistedCount to totalMsgCount on success.
// Returns (true, nil) on success, (false, err) on partial/total failure.
func (b *PersistenceBridge) RewriteAfterCompress(sessionView []llm.ChatMessage, totalMsgCount int) (bool, error) {
	if b.session == nil {
		return true, nil
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
	if _, err := b.session.AppendContextSnapshot(sqlite.HistoryRecordCompress, clean); err != nil {
		log.WithError(err).Error("Failed to append compression history record")
		return false, err
	}
	b.lastPersistedCount = totalMsgCount
	return true, nil
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

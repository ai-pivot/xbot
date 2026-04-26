package agent

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"xbot/llm"
	log "xbot/logger"
)

// ContextEditAction defines the operation types for the context_edit tool.
type ContextEditAction string

const (
	ContextEditDelete     ContextEditAction = "delete"
	ContextEditDeleteTurn ContextEditAction = "delete_turn"
	ContextEditTruncate   ContextEditAction = "truncate"
	ContextEditReplace    ContextEditAction = "replace"
	ContextEditList       ContextEditAction = "list"
)

// ContextEditRequest is the request parameters for the context_edit tool.
type ContextEditRequest struct {
	Action     ContextEditAction `json:"action"`
	MessageIdx int               `json:"message_idx"`
	MaxChars   int               `json:"max_chars"`
	OldText    string            `json:"old_text"`
	NewText    string            `json:"new_text"`
	Reason     string            `json:"reason"`
}

// ContextEditResult is the execution result of context_edit.
type ContextEditResult struct {
	Action     ContextEditAction `json:"action"`
	MessageIdx int               `json:"message_idx"`
	Role       string            `json:"role"`
	Reason     string            `json:"reason"`
	Before     string            `json:"before_chars"`
	After      string            `json:"after_chars"`
	EditedAt   time.Time         `json:"edited_at"`
}

const (
	contextEditDefaultMaxSize  = 100 // Default max entries for context edit store
	contextEditDefaultMaxChars = 200 // Default max characters for context edit
)

// ContextEditStore manages context editing history.
type ContextEditStore struct {
	mu      sync.RWMutex
	history []ContextEditResult
	maxSize int
}

// NewContextEditStore creates ContextEditStore.
func NewContextEditStore(maxSize int) *ContextEditStore {
	if maxSize <= 0 {
		maxSize = contextEditDefaultMaxSize
	}
	return &ContextEditStore{maxSize: maxSize}
}

// Record records one context edit operation.
func (s *ContextEditStore) Record(result ContextEditResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.history) >= s.maxSize {
		s.history = s.history[1:]
	}
	s.history = append(s.history, result)
}

// History returns edit history (most recent first).
func (s *ContextEditStore) History() []ContextEditResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]ContextEditResult, len(s.history))
	copy(result, s.history)
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// ContextEditor executes context editing operations.
// It holds a pointer to the messages slice, set by engine.go at the start of each Run.
type ContextEditor struct {
	Store     *ContextEditStore
	messages  []llm.ChatMessage         // Current conversation messages, set by engine during Run
	mu        sync.RWMutex              // Protect messages reference
	PersistFn func(editedIndices []int) // persistence callback for syncing edits to DB (best-effort)
	tenantID  int64                     // current tenant ID for persistence (set per-request)
}

// NewContextEditor creates ContextEditor.
func NewContextEditor(store *ContextEditStore) *ContextEditor {
	return &ContextEditor{Store: store}
}

// SetMessages sets the current messages slice reference (called by engine at the start of each Run).
func (e *ContextEditor) SetMessages(messages []llm.ChatMessage) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.messages = messages
}

// SetTenantID sets the current tenant ID for persistence callbacks.
// Called per-request before the engine run that may trigger context edits.
func (e *ContextEditor) SetTenantID(tenantID int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tenantID = tenantID
}

// HandleRequest handles context_edit request, directly modifying messages slice.
func (e *ContextEditor) HandleRequest(action string, params map[string]interface{}) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	msgs := e.messages

	if msgs == nil {
		return "", fmt.Errorf("messages not available (editor not initialized)")
	}

	// Audit log for context edits
	log.WithFields(log.Fields{
		"action": action,
		"params": params,
	}).Info("Context edit request")

	switch ContextEditAction(action) {
	case ContextEditList:
		return listMessagesByTurn(msgs), nil
	case ContextEditDeleteTurn:
		return e.deleteTurn(msgs, params)
	case ContextEditDelete, ContextEditTruncate, ContextEditReplace:
		return e.applyEdit(msgs, action, params)
	default:
		return "", fmt.Errorf("unknown action: %s (valid: list, delete, delete_turn, truncate, replace)", action)
	}
}

// applyEdit executes the edit operation and modifies messages slice.
func (e *ContextEditor) applyEdit(messages []llm.ChatMessage, action string, params map[string]interface{}) (string, error) {
	req := ContextEditRequest{
		Action: ContextEditAction(action),
	}

	if v, ok := params["message_idx"].(float64); ok {
		req.MessageIdx = int(v)
	} else {
		return "", fmt.Errorf("message_idx is required for %s action", action)
	}

	if v, ok := params["max_chars"].(float64); ok {
		req.MaxChars = int(v)
	}
	if v, ok := params["old_text"].(string); ok {
		req.OldText = v
	}
	if v, ok := params["new_text"].(string); ok {
		req.NewText = v
	}
	if v, ok := params["reason"].(string); ok {
		req.Reason = v
	}
	if req.Reason == "" {
		req.Reason = "not specified"
	}

	// Map user-visible index to messages slice index
	actualIdx := userVisibleIndex(messages, req.MessageIdx)
	if actualIdx < 0 || actualIdx >= len(messages) {
		return "", fmt.Errorf("message index %d out of range (valid: 0-%d)", req.MessageIdx, countUserVisible(messages)-1)
	}

	msg := messages[actualIdx]

	// Safety check: editing system messages is not allowed
	if msg.Role == "system" {
		return "", fmt.Errorf("cannot edit system messages")
	}

	// Safety check: editing the most recent 3 messages is not allowed
	visibleCount := countUserVisible(messages)
	if req.MessageIdx >= visibleCount-3 {
		return "", fmt.Errorf("cannot edit recent messages (last 3 messages are protected)")
	}

	beforeChars := fmt.Sprintf("%d chars", len([]rune(msg.Content)))
	var afterChars string
	editedIndices := []int{actualIdx} // track which slice indices were modified for persistence

	switch req.Action {
	case ContextEditDelete:
		placeholder := fmt.Sprintf("[context edited: %s — deleted %s at %s]", req.Reason, beforeChars, time.Now().Format("15:04:05"))
		messages[actualIdx].Content = placeholder
		// If the deleted message is an assistant with tool calls, also clean up
		// the subsequent tool messages to maintain tool_use/tool_result pairing.
		if len(messages[actualIdx].ToolCalls) > 0 {
			tcIDs := make(map[string]bool, len(messages[actualIdx].ToolCalls))
			for _, tc := range messages[actualIdx].ToolCalls {
				tcIDs[tc.ID] = true
			}
			// Scan remaining messages (not just consecutive tool segment)
			// to find all tool results paired with this assistant's tool calls.
			for j := actualIdx + 1; j < len(messages); j++ {
				if messages[j].Role == "tool" && tcIDs[messages[j].ToolCallID] {
					messages[j].Content = placeholder
					messages[j].ToolCallID = ""
					messages[j].ToolName = ""
					messages[j].ToolArguments = ""
					editedIndices = append(editedIndices, j)
				}
			}
		}
		messages[actualIdx].ToolCalls = nil
		afterChars = "0 chars"

	case ContextEditTruncate:
		if req.MaxChars <= 0 {
			req.MaxChars = contextEditDefaultMaxChars
		}
		runes := []rune(msg.Content)
		if len(runes) <= req.MaxChars {
			return "", fmt.Errorf("message content (%d chars) is already within limit (%d chars)", len(runes), req.MaxChars)
		}
		truncated := string(runes[:req.MaxChars])
		messages[actualIdx].Content = truncated + fmt.Sprintf("\n\n[context edited: truncated from %s to %d chars — %s]", beforeChars, req.MaxChars, req.Reason)
		afterChars = fmt.Sprintf("%d chars", req.MaxChars)

	case ContextEditReplace:
		if req.OldText == "" {
			return "", fmt.Errorf("old_text is required for replace action")
		}
		var newContent string
		var matched bool
		if strings.HasPrefix(req.OldText, "regex:") {
			pattern := strings.TrimPrefix(req.OldText, "regex:")
			re, err := regexp.Compile(pattern)
			if err != nil {
				return "", fmt.Errorf("invalid regex pattern: %w", err)
			}
			newContent = re.ReplaceAllString(msg.Content, req.NewText)
			matched = newContent != msg.Content
		} else {
			if !strings.Contains(msg.Content, req.OldText) {
				return "", fmt.Errorf("old_text not found in message content")
			}
			newContent = strings.ReplaceAll(msg.Content, req.OldText, req.NewText)
			matched = true
		}
		if !matched {
			return "", fmt.Errorf("old_text pattern did not match any content")
		}
		messages[actualIdx].Content = newContent + fmt.Sprintf("\n\n[context edited: replaced text — %s]", req.Reason)
		afterChars = fmt.Sprintf("%d chars", len([]rune(newContent)))
	}

	result := ContextEditResult{
		Action:     req.Action,
		MessageIdx: req.MessageIdx,
		Role:       msg.Role,
		Reason:     req.Reason,
		Before:     beforeChars,
		After:      afterChars,
		EditedAt:   time.Now(),
	}

	if e.Store != nil {
		e.Store.Record(result)
	}

	log.WithFields(map[string]interface{}{
		"action":      req.Action,
		"message_idx": req.MessageIdx,
		"role":        msg.Role,
		"before":      beforeChars,
		"after":       afterChars,
		"reason":      req.Reason,
	}).Info("Context edit applied")

	// Persist edited message(s) to database (best-effort).
	if e.PersistFn != nil {
		e.PersistFn(editedIndices)
	}

	return fmt.Sprintf("✅ %s message #%d [%s]: %s → %s — %s", req.Action, req.MessageIdx, msg.Role, beforeChars, afterChars, req.Reason), nil
}

// countUserVisible counts non-system messages.
func countUserVisible(messages []llm.ChatMessage) int {
	count := 0
	for _, m := range messages {
		if m.Role != "system" {
			count++
		}
	}
	return count
}

// userVisibleIndex converts user-visible message index to actual messages slice index.
func userVisibleIndex(messages []llm.ChatMessage, visibleIdx int) int {
	visibleCount := 0
	for i, m := range messages {
		if m.Role != "system" {
			if visibleCount == visibleIdx {
				return i
			}
			visibleCount++
		}
	}
	return -1
}

// conversationTurn represents one conversation turn (one user message + all associated assistant/tool messages).
type conversationTurn struct {
	TurnIdx       int    // Turn number (0-based, user-visible)
	StartSliceIdx int    // Start index in messages slice
	EndSliceIdx   int    // End index in messages slice (inclusive)
	UserSliceIdx  int    // User message index in slice
	MsgCount      int    // Message count in this turn
	ToolCount     int    // Tool message count
	TotalChars    int    // Total character count
	UserPreview   string // User message preview
}

// identifyTurns groups messages slice by conversation turns.
// A turn starts from one user message and ends before the next user message.
// System messages don't belong to any turn.
func identifyTurns(messages []llm.ChatMessage) []conversationTurn {
	var turns []conversationTurn
	currentTurn := -1 // Current turn index

	for i, m := range messages {
		if m.Role == "system" {
			continue
		}

		if m.Role == "user" {
			// End previous turn
			if currentTurn >= 0 {
				t := &turns[currentTurn]
				t.EndSliceIdx = i - 1
				t.MsgCount = i - t.StartSliceIdx
			}
			// Start new turn
			currentTurn = len(turns)
			preview := m.Content
			if len([]rune(preview)) > 80 {
				preview = string([]rune(preview)[:80]) + "..."
			}
			turns = append(turns, conversationTurn{
				TurnIdx:       currentTurn,
				StartSliceIdx: i,
				UserSliceIdx:  i,
				UserPreview:   preview,
			})
		}

		// Accumulate current turn statistics
		if currentTurn >= 0 {
			t := &turns[currentTurn]
			t.TotalChars += len([]rune(m.Content))
			if m.Role == "tool" {
				t.ToolCount++
			}
		}
	}

	// End last turn
	if currentTurn >= 0 && len(turns) > 0 {
		t := &turns[currentTurn]
		t.EndSliceIdx = len(messages) - 1
		t.MsgCount = t.EndSliceIdx - t.StartSliceIdx + 1
	}

	return turns
}

// listMessagesByTurn generates message list summary by conversation turns.
func listMessagesByTurn(messages []llm.ChatMessage) string {
	turns := identifyTurns(messages)

	var sb strings.Builder
	sb.WriteString("📋 Conversation Turns:\n\n")

	if len(turns) == 0 {
		sb.WriteString("(no conversation turns found)\n")
		return sb.String()
	}

	totalMsgs := 0
	for _, t := range turns {
		totalMsgs += t.MsgCount

		fmt.Fprintf(&sb, "Turn %d: 👤 user (%d chars)", t.TurnIdx, len([]rune(messages[t.UserSliceIdx].Content)))
		if t.UserPreview != "" {
			fmt.Fprintf(&sb, " \"%s\"", t.UserPreview)
		}
		sb.WriteString("\n")

		// Count assistant messages (iteration rounds with tool calls)
		assistantCount := 0
		for i := t.StartSliceIdx; i <= t.EndSliceIdx; i++ {
			if messages[i].Role == "assistant" {
				assistantCount++
			}
		}

		fmt.Fprintf(&sb, "  └─ %d messages: %d iterations, %d tool calls, %d total chars\n",
			t.MsgCount, assistantCount, t.ToolCount, t.TotalChars)
	}

	fmt.Fprintf(&sb, "\nTotal: %d turns, %d messages. Use delete_turn to remove entire turns, or use message-level actions (delete/truncate/replace) for fine-grained edits.", len(turns), totalMsgs)
	return sb.String()
}

// deleteTurn deletes an entire conversation turn (user message + all associated assistant/tool messages).
func (e *ContextEditor) deleteTurn(messages []llm.ChatMessage, params map[string]interface{}) (string, error) {
	turnIdx, ok := params["turn_idx"].(float64)
	if !ok {
		return "", fmt.Errorf("turn_idx is required for delete_turn action")
	}

	turns := identifyTurns(messages)
	idx := int(turnIdx)
	if idx < 0 || idx >= len(turns) {
		return "", fmt.Errorf("turn index %d out of range (valid: 0-%d)", idx, len(turns)-1)
	}

	// Safety check: deleting the last turn (current conversation) is not allowed
	if idx == len(turns)-1 {
		return "", fmt.Errorf("cannot delete the current (last) turn — it is protected")
	}

	t := turns[idx]
	reason := "not specified"
	if v, ok := params["reason"].(string); ok && v != "" {
		reason = v
	}

	// Replace all messages' content in this turn with placeholder
	placeholder := fmt.Sprintf("[context edited: deleted turn %d (%d messages, %d tool calls) — %s — %s]",
		idx, t.MsgCount, t.ToolCount, reason, time.Now().Format("15:04:05"))

	deletedMsgCount := 0
	for i := t.StartSliceIdx; i <= t.EndSliceIdx; i++ {
		messages[i].Content = placeholder
		messages[i].ToolCalls = nil
		messages[i].ToolCallID = ""
		messages[i].ToolName = ""
		messages[i].ToolArguments = ""
		deletedMsgCount++
	}

	if e.Store != nil {
		e.Store.Record(ContextEditResult{
			Action:     ContextEditDeleteTurn,
			MessageIdx: idx,
			Role:       "turn",
			Reason:     reason,
			Before:     fmt.Sprintf("%d msgs", t.MsgCount),
			After:      "0 chars",
			EditedAt:   time.Now(),
		})
	}

	log.WithFields(map[string]interface{}{
		"action":     "delete_turn",
		"turn_idx":   idx,
		"msg_count":  deletedMsgCount,
		"tool_count": t.ToolCount,
		"reason":     reason,
	}).Info("Context edit: deleted turn")

	// Persist deleted turn messages to database (best-effort).
	if e.PersistFn != nil {
		turnIndices := make([]int, 0, t.EndSliceIdx-t.StartSliceIdx+1)
		for i := t.StartSliceIdx; i <= t.EndSliceIdx; i++ {
			turnIndices = append(turnIndices, i)
		}
		e.PersistFn(turnIndices)
	}

	return fmt.Sprintf("✅ Deleted turn %d (%d messages, %d tool calls, %d total chars) — %s",
		idx, deletedMsgCount, t.ToolCount, t.TotalChars, reason), nil
}

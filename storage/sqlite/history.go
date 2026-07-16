package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"xbot/llm"
	"xbot/storage/internal"
)

type HistoryRecordType string

const (
	HistoryRecordMessage     HistoryRecordType = "message"
	HistoryRecordCompress    HistoryRecordType = "compress"
	HistoryRecordPrune       HistoryRecordType = "prune"
	HistoryRecordContextEdit HistoryRecordType = "context_edit"
	HistoryRecordAskQuestion HistoryRecordType = "ask_question"
	HistoryRecordAskAnswer   HistoryRecordType = "ask_answer"
	HistoryRecordMask        HistoryRecordType = "mask"
)

type HistoryRecord struct {
	HistoryID       int64
	Type            HistoryRecordType
	TargetHistoryID int64
	Message         llm.ChatMessage
	Data            json.RawMessage
	CreatedAt       time.Time
	CompactedBy     int64
	Compression     *CompressionRange
}

type CompressionRange struct {
	StartHistoryID   int64
	EndHistoryID     int64
	SourceHistoryIDs []int64
}

type ContextSnapshot struct {
	Messages   []llm.ChatMessage `json:"messages"`
	HistoryIDs []int64           `json:"history_ids"`
}

type MessageMutation struct {
	TargetHistoryID  int64           `json:"target_history_id"`
	TargetOccurrence int             `json:"target_occurrence,omitempty"`
	Message          llm.ChatMessage `json:"message"`
}

type MessageMutations struct {
	Mutations []MessageMutation `json:"mutations"`
}

type MaskMutation struct {
	TargetHistoryID  int64  `json:"target_history_id"`
	TargetOccurrence int    `json:"target_occurrence,omitempty"`
	Content          string `json:"content"`
}

type MaskMutations struct {
	Mutations []MaskMutation `json:"mutations"`
}

type AskQuestionRecord struct {
	Metadata      map[string]string `json:"metadata"`
	ToolHistoryID int64             `json:"tool_history_id"`
}

type AskAnswerRecord struct {
	Answer        string `json:"answer"`
	ToolHistoryID int64  `json:"tool_history_id"`
}

type PendingAskUser struct {
	HistoryID     int64
	ToolHistoryID int64
	Metadata      map[string]string
}

type ReplayResult struct {
	Messages       []llm.ChatMessage
	PendingAskUser *PendingAskUser
}

func isControlRecordType(recordType HistoryRecordType) bool {
	switch recordType {
	case HistoryRecordCompress, HistoryRecordPrune, HistoryRecordContextEdit,
		HistoryRecordAskQuestion, HistoryRecordAskAnswer, HistoryRecordMask:
		return true
	default:
		return false
	}
}

type messageExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func appendMessageWith(execer messageExecer, tenantID int64, msg llm.ChatMessage) (int64, error) {
	var toolCallsJSON sql.NullString
	if len(msg.ToolCalls) > 0 {
		data, err := json.Marshal(msg.ToolCalls)
		if err != nil {
			return 0, fmt.Errorf("marshal tool_calls: %w", err)
		}
		toolCallsJSON = sql.NullString{String: string(data), Valid: true}
	}
	ts := msg.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	displayOnly := 0
	if msg.DisplayOnly {
		displayOnly = 1
	}
	result, err := execer.Exec(`
		INSERT INTO session_messages
		(tenant_id, role, content, tool_call_id, tool_name, tool_arguments, tool_calls,
		 detail, display_only, reasoning_content, record_type, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'message', ?)
	`, tenantID, msg.Role, msg.Content, msg.ToolCallID, msg.ToolName, msg.ToolArguments,
		toolCallsJSON, msg.Detail, displayOnly, msg.ReasoningContent, ts.Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("insert session message: %w", err)
	}
	historyID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read inserted history id: %w", err)
	}
	return historyID, nil
}

func (s *SessionService) appendMessage(tenantID int64, msg llm.ChatMessage) (int64, error) {
	conn, err := s.conn()
	if err != nil {
		return 0, err
	}
	return appendMessageWith(conn, tenantID, msg)
}

func (s *SessionService) AppendMessage(tenantID int64, msg llm.ChatMessage) (int64, error) {
	s.db.historyMu.Lock()
	defer s.db.historyMu.Unlock()
	return s.appendMessage(tenantID, msg)
}

// AppendMessages atomically appends a related message batch.
func (s *SessionService) AppendMessages(tenantID int64, messages []llm.ChatMessage) ([]int64, error) {
	s.db.historyMu.Lock()
	defer s.db.historyMu.Unlock()
	conn, err := s.conn()
	if err != nil {
		return nil, err
	}
	tx, err := conn.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin message batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	ids := make([]int64, len(messages))
	for i, msg := range messages {
		ids[i], err = appendMessageWith(tx, tenantID, msg)
		if err != nil {
			return nil, fmt.Errorf("append message batch item %d: %w", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit message batch: %w", err)
	}
	return ids, nil
}

func (s *SessionService) AppendControl(tenantID int64, recordType HistoryRecordType, targetHistoryID int64, data any) (int64, error) {
	s.db.historyMu.Lock()
	defer s.db.historyMu.Unlock()
	return s.appendControlLocked(tenantID, recordType, targetHistoryID, data)
}

func (s *SessionService) appendControlLocked(tenantID int64, recordType HistoryRecordType, targetHistoryID int64, data any) (int64, error) {
	if !isControlRecordType(recordType) {
		return 0, fmt.Errorf("unknown history record type %q", recordType)
	}
	if recordType == HistoryRecordCompress || recordType == HistoryRecordPrune ||
		recordType == HistoryRecordAskQuestion || recordType == HistoryRecordAskAnswer || recordType == HistoryRecordMask {
		return 0, fmt.Errorf("history record type %q requires its atomic append method", recordType)
	}
	conn, err := s.conn()
	if err != nil {
		return 0, err
	}
	if targetHistoryID != 0 {
		var exists int
		if err := conn.QueryRow(`SELECT 1 FROM session_messages WHERE tenant_id = ? AND id = ?`, tenantID, targetHistoryID).Scan(&exists); err != nil {
			if err == sql.ErrNoRows {
				return 0, fmt.Errorf("target history_id %d does not exist for tenant %d", targetHistoryID, tenantID)
			}
			return 0, fmt.Errorf("validate target history_id %d: %w", targetHistoryID, err)
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return 0, fmt.Errorf("marshal %s history data: %w", recordType, err)
	}
	if recordType == HistoryRecordContextEdit {
		var mutations MessageMutations
		if err := json.Unmarshal(raw, &mutations); err != nil {
			return 0, fmt.Errorf("decode context edit targets: %w", err)
		}
		if len(mutations.Mutations) == 0 {
			return 0, fmt.Errorf("context edit has no mutations")
		}
		replay, err := s.replayLocked(tenantID)
		if err != nil {
			return 0, err
		}
		for _, mutation := range mutations.Mutations {
			var exists int
			if mutation.TargetHistoryID == 0 {
				return 0, fmt.Errorf("context edit target has no history_id")
			}
			if err := conn.QueryRow(`SELECT 1 FROM session_messages WHERE tenant_id = ? AND id = ?`, tenantID, mutation.TargetHistoryID).Scan(&exists); err != nil {
				if err == sql.ErrNoRows {
					return 0, fmt.Errorf("context edit target history_id %d does not exist for tenant %d", mutation.TargetHistoryID, tenantID)
				}
				return 0, fmt.Errorf("validate context edit target %d: %w", mutation.TargetHistoryID, err)
			}
			if activeMessageIndexOccurrence(replay.Messages, mutation.TargetHistoryID, mutation.TargetOccurrence) < 0 {
				return 0, fmt.Errorf("context edit target history_id %d is not active", mutation.TargetHistoryID)
			}
		}
	}
	result, err := conn.Exec(`
		INSERT INTO session_messages
		(tenant_id, role, content, display_only, record_type, target_history_id, record_data, created_at)
		VALUES (?, 'control', '', 1, ?, NULLIF(?, 0), ?, ?)
	`, tenantID, recordType, targetHistoryID, string(raw), time.Now().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("append %s history record: %w", recordType, err)
	}
	historyID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read inserted history id: %w", err)
	}
	return historyID, nil
}

func (s *SessionService) AppendContextSnapshot(tenantID int64, recordType HistoryRecordType, messages []llm.ChatMessage) (int64, error) {
	s.db.historyMu.Lock()
	defer s.db.historyMu.Unlock()
	if recordType != HistoryRecordCompress && recordType != HistoryRecordPrune {
		return 0, fmt.Errorf("record type %q does not accept a context snapshot", recordType)
	}
	replay, err := s.replayLocked(tenantID)
	if err != nil {
		return 0, err
	}
	active := make([]llm.ChatMessage, 0, len(messages))
	historyIDs := make([]int64, 0, len(messages))
	occurrences := make(map[int64]int)
	for _, msg := range messages {
		if msg.Role != "system" && !msg.DisplayOnly {
			if msg.HistoryID != 0 {
				occurrence := occurrences[msg.HistoryID]
				if activeMessageIndexOccurrence(replay.Messages, msg.HistoryID, occurrence) < 0 {
					return 0, fmt.Errorf("%s snapshot history_id %d occurrence %d is not active", recordType, msg.HistoryID, occurrence)
				}
				occurrences[msg.HistoryID] = occurrence + 1
			}
			active = append(active, msg)
			historyIDs = append(historyIDs, msg.HistoryID)
		}
	}
	if replay.PendingAskUser != nil && activeMessageIndex(active, replay.PendingAskUser.ToolHistoryID) < 0 {
		return 0, fmt.Errorf("%s snapshot removes pending AskUser tool target %d", recordType, replay.PendingAskUser.ToolHistoryID)
	}
	return s.appendSnapshotLocked(tenantID, recordType, ContextSnapshot{Messages: active, HistoryIDs: historyIDs})
}

func (s *SessionService) appendSnapshotLocked(tenantID int64, recordType HistoryRecordType, snapshot ContextSnapshot) (int64, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return 0, fmt.Errorf("marshal %s history data: %w", recordType, err)
	}
	conn, err := s.conn()
	if err != nil {
		return 0, err
	}
	result, err := conn.Exec(`
		INSERT INTO session_messages
		(tenant_id, role, content, display_only, record_type, record_data, created_at)
		VALUES (?, 'control', '', 1, ?, ?, ?)
	`, tenantID, recordType, string(raw), time.Now().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("append %s history record: %w", recordType, err)
	}
	historyID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read inserted history id: %w", err)
	}
	return historyID, nil
}

func (s *SessionService) AppendAskQuestion(tenantID int64, metadata map[string]string) (int64, error) {
	s.db.historyMu.Lock()
	defer s.db.historyMu.Unlock()
	return s.appendAskQuestionLocked(tenantID, metadata)
}

func (s *SessionService) appendAskQuestionLocked(tenantID int64, metadata map[string]string) (int64, error) {
	replay, err := s.replayLocked(tenantID)
	if err != nil {
		return 0, err
	}
	if replay.PendingAskUser != nil {
		return 0, fmt.Errorf("AskUser question %d is still pending", replay.PendingAskUser.HistoryID)
	}
	var toolHistoryID int64
	for i := len(replay.Messages) - 1; i >= 0; i-- {
		if replay.Messages[i].Role == "tool" && replay.Messages[i].ToolName == "AskUser" {
			toolHistoryID = replay.Messages[i].HistoryID
			break
		}
	}
	if toolHistoryID == 0 {
		return 0, fmt.Errorf("append AskUser question: matching active tool result not found")
	}
	payload, err := json.Marshal(AskQuestionRecord{Metadata: metadata, ToolHistoryID: toolHistoryID})
	if err != nil {
		return 0, fmt.Errorf("marshal AskUser question: %w", err)
	}
	conn, err := s.conn()
	if err != nil {
		return 0, err
	}
	result, err := conn.Exec(`
		INSERT INTO session_messages
		(tenant_id, role, content, display_only, record_type, target_history_id, record_data, created_at)
		SELECT ?, 'control', '', 1, 'ask_question', ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM session_messages WHERE tenant_id = ? AND id = ?)
		AND NOT EXISTS (
			SELECT 1 FROM session_messages q
			WHERE q.tenant_id = ? AND q.record_type = 'ask_question'
			AND NOT EXISTS (
				SELECT 1 FROM session_messages a
				WHERE a.tenant_id = q.tenant_id AND a.record_type = 'ask_answer' AND a.target_history_id = q.id
			)
		)
	`, tenantID, toolHistoryID, string(payload), time.Now().Format(time.RFC3339Nano), tenantID, toolHistoryID, tenantID)
	if err != nil {
		return 0, fmt.Errorf("append AskUser question: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if rows != 1 {
		return 0, fmt.Errorf("AskUser question is no longer valid or another question is pending")
	}
	historyID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read AskUser question history id: %w", err)
	}
	return historyID, nil
}

func (s *SessionService) AppendMasks(tenantID int64, mutations []MaskMutation) error {
	s.db.historyMu.Lock()
	defer s.db.historyMu.Unlock()
	if len(mutations) == 0 {
		return nil
	}
	conn, err := s.conn()
	if err != nil {
		return err
	}
	for _, mutation := range mutations {
		if mutation.TargetHistoryID == 0 {
			return fmt.Errorf("mask target has no history_id")
		}
	}
	replay, err := s.replayLocked(tenantID)
	if err != nil {
		return err
	}
	for _, mutation := range mutations {
		if activeMessageIndexOccurrence(replay.Messages, mutation.TargetHistoryID, mutation.TargetOccurrence) < 0 {
			return fmt.Errorf("mask target history_id %d is not active", mutation.TargetHistoryID)
		}
	}
	raw, err := json.Marshal(MaskMutations{Mutations: mutations})
	if err != nil {
		return fmt.Errorf("marshal mask history data: %w", err)
	}
	result, err := conn.Exec(`
		INSERT INTO session_messages
		(tenant_id, role, content, display_only, record_type, record_data, created_at)
		SELECT ?, 'control', '', 1, 'mask', ?, ?
		WHERE NOT EXISTS (
			SELECT 1 FROM json_each(json_extract(?, '$.mutations')) m
			WHERE NOT EXISTS (
				SELECT 1 FROM session_messages sm
				WHERE sm.tenant_id = ? AND sm.id = json_extract(m.value, '$.target_history_id')
			)
		)
	`, tenantID, string(raw), time.Now().Format(time.RFC3339Nano), string(raw), tenantID)
	if err != nil {
		return fmt.Errorf("append mask history record: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read mask append result: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("append mask history record: one or more targets do not exist")
	}
	return nil
}

func (s *SessionService) AppendAskAnswer(tenantID int64, answer string) (int64, error) {
	s.db.historyMu.Lock()
	defer s.db.historyMu.Unlock()
	return s.appendAskAnswerLocked(tenantID, answer)
}

func (s *SessionService) appendAskAnswerLocked(tenantID int64, answer string) (int64, error) {
	replay, err := s.replayLocked(tenantID)
	if err != nil {
		return 0, err
	}
	if replay.PendingAskUser == nil {
		return 0, fmt.Errorf("AskUser question is no longer pending")
	}
	records, err := s.getFullHistoryLocked(tenantID)
	if err != nil {
		return 0, err
	}
	var toolHistoryID int64
	for _, record := range records {
		if record.HistoryID != replay.PendingAskUser.HistoryID {
			continue
		}
		var question AskQuestionRecord
		if err := json.Unmarshal(record.Data, &question); err != nil {
			return 0, fmt.Errorf("history_id %d: decode AskUser question: %w", record.HistoryID, err)
		}
		toolHistoryID = question.ToolHistoryID
		break
	}
	if toolHistoryID == 0 {
		return 0, fmt.Errorf("history_id %d: AskUser question has no tool target", replay.PendingAskUser.HistoryID)
	}
	payload, err := json.Marshal(AskAnswerRecord{Answer: answer, ToolHistoryID: toolHistoryID})
	if err != nil {
		return 0, fmt.Errorf("marshal AskUser answer: %w", err)
	}
	conn, err := s.conn()
	if err != nil {
		return 0, err
	}
	result, err := conn.Exec(`
		INSERT INTO session_messages
		(tenant_id, role, content, display_only, record_type, target_history_id, record_data, created_at)
		SELECT ?, 'control', '', 1, 'ask_answer', ?, ?, ?
		WHERE NOT EXISTS (
			SELECT 1 FROM session_messages
			WHERE tenant_id = ? AND record_type = 'ask_answer' AND target_history_id = ?
		)
		AND EXISTS (
			SELECT 1 FROM session_messages q
			WHERE q.tenant_id = ? AND q.id = ? AND q.record_type = 'ask_question'
		)
		AND EXISTS (
			SELECT 1 FROM session_messages tool
			WHERE tool.tenant_id = ? AND tool.id = ?
		)
	`, tenantID, replay.PendingAskUser.HistoryID, string(payload), time.Now().Format(time.RFC3339Nano),
		tenantID, replay.PendingAskUser.HistoryID, tenantID, replay.PendingAskUser.HistoryID, tenantID, toolHistoryID)
	if err != nil {
		return 0, fmt.Errorf("append AskUser answer: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if rows != 1 {
		return 0, fmt.Errorf("AskUser question is no longer pending")
	}
	historyID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read AskUser answer history id: %w", err)
	}
	return historyID, nil
}

func (s *SessionService) GetFullHistory(tenantID int64) ([]HistoryRecord, error) {
	s.db.historyMu.Lock()
	defer s.db.historyMu.Unlock()
	return s.getFullHistoryLocked(tenantID)
}

func (s *SessionService) getFullHistoryLocked(tenantID int64) ([]HistoryRecord, error) {
	conn, err := s.conn()
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(`
		SELECT id, record_type, COALESCE(target_history_id, 0), COALESCE(record_data, ''),
		       role, content, tool_call_id, tool_name, tool_arguments, tool_calls, detail,
		       reasoning_content, display_only, created_at
		FROM session_messages WHERE tenant_id = ? ORDER BY id ASC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query full session history: %w", err)
	}
	defer rows.Close()
	var records []HistoryRecord
	for rows.Next() {
		var record HistoryRecord
		var rawData, role, content, createdAt string
		var toolCallID, toolName, toolArguments, toolCallsJSON, detail, reasoning sql.NullString
		var displayOnly int
		if err := rows.Scan(&record.HistoryID, &record.Type, &record.TargetHistoryID, &rawData,
			&role, &content, &toolCallID, &toolName, &toolArguments, &toolCallsJSON, &detail,
			&reasoning, &displayOnly, &createdAt); err != nil {
			return nil, fmt.Errorf("scan history record: %w", err)
		}
		record.CreatedAt = internal.ParseTimestamp(createdAt)
		record.Data = json.RawMessage(rawData)
		if record.Type == HistoryRecordMessage {
			record.Message = llm.ChatMessage{HistoryID: record.HistoryID, Role: role, Content: content,
				DisplayOnly: displayOnly != 0, Timestamp: record.CreatedAt}
			if toolCallID.Valid {
				record.Message.ToolCallID = toolCallID.String
			}
			if toolName.Valid {
				record.Message.ToolName = toolName.String
			}
			if toolArguments.Valid {
				record.Message.ToolArguments = toolArguments.String
			}
			if detail.Valid {
				record.Message.Detail = detail.String
			}
			if reasoning.Valid {
				record.Message.ReasoningContent = reasoning.String
			}
			if toolCallsJSON.Valid && toolCallsJSON.String != "" {
				if err := json.Unmarshal([]byte(toolCallsJSON.String), &record.Message.ToolCalls); err != nil {
					return nil, fmt.Errorf("history_id %d: decode tool_calls: %w", record.HistoryID, err)
				}
			}
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate full session history: %w", err)
	}
	decorateCompressionRanges(records)
	return records, nil
}

func decorateCompressionRanges(records []HistoryRecord) {
	active := make([]int64, 0, len(records))
	byID := make(map[int64]int, len(records))
	for i := range records {
		record := &records[i]
		byID[record.HistoryID] = i
		switch record.Type {
		case HistoryRecordMessage:
			if !record.Message.DisplayOnly {
				active = append(active, record.HistoryID)
			}
		case HistoryRecordCompress, HistoryRecordPrune:
			var snapshot ContextSnapshot
			if err := json.Unmarshal(record.Data, &snapshot); err != nil || len(snapshot.HistoryIDs) != len(snapshot.Messages) {
				continue
			}
			kept := make(map[int64]struct{}, len(snapshot.HistoryIDs))
			next := make([]int64, 0, len(snapshot.HistoryIDs))
			for _, id := range snapshot.HistoryIDs {
				if id == 0 {
					id = record.HistoryID
				}
				kept[id] = struct{}{}
				next = append(next, id)
			}
			if record.Type == HistoryRecordCompress {
				source := make([]int64, 0, len(active))
				for _, id := range active {
					if _, ok := kept[id]; ok {
						continue
					}
					source = append(source, id)
					if idx, ok := byID[id]; ok && records[idx].CompactedBy == 0 {
						records[idx].CompactedBy = record.HistoryID
					}
				}
				if len(source) > 0 {
					record.Compression = &CompressionRange{
						StartHistoryID: source[0], EndHistoryID: source[len(source)-1], SourceHistoryIDs: source,
					}
				}
			}
			active = next
		}
	}
}

// RewindToHistoryID validates a user node and atomically truncates that node
// plus every later record for the same tenant. The selected content is returned
// for the caller's existing edit/resend flow.
func (s *SessionService) RewindToHistoryID(tenantID, historyID int64) (llm.ChatMessage, int, error) {
	if historyID <= 0 {
		return llm.ChatMessage{}, 0, fmt.Errorf("history_id is required")
	}
	s.db.historyMu.Lock()
	defer s.db.historyMu.Unlock()
	conn, err := s.conn()
	if err != nil {
		return llm.ChatMessage{}, 0, err
	}
	var role, recordType, content, createdAt string
	var displayOnly int
	if err := conn.QueryRow(`
		SELECT role, record_type, content, created_at, display_only
		FROM session_messages WHERE tenant_id = ? AND id = ?
	`, tenantID, historyID).Scan(&role, &recordType, &content, &createdAt, &displayOnly); err != nil {
		if err == sql.ErrNoRows {
			return llm.ChatMessage{}, 0, fmt.Errorf("rewind target history_id %d not found", historyID)
		}
		return llm.ChatMessage{}, 0, fmt.Errorf("load rewind target: %w", err)
	}
	if displayOnly != 0 || role != "user" || HistoryRecordType(recordType) != HistoryRecordMessage {
		return llm.ChatMessage{}, 0, fmt.Errorf("history_id %d is not a rewindable user message", historyID)
	}
	var turnIdx int
	if err := conn.QueryRow(`
		SELECT COUNT(*) FROM session_messages
		WHERE tenant_id = ? AND id <= ? AND record_type = 'message' AND role = 'user' AND display_only = 0
	`, tenantID, historyID).Scan(&turnIdx); err != nil {
		return llm.ChatMessage{}, 0, fmt.Errorf("resolve rewind turn: %w", err)
	}
	result, err := conn.Exec(`DELETE FROM session_messages WHERE tenant_id = ? AND id >= ?`, tenantID, historyID)
	if err != nil {
		return llm.ChatMessage{}, 0, fmt.Errorf("truncate history at history_id %d: %w", historyID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows == 0 {
		return llm.ChatMessage{}, 0, fmt.Errorf("truncate history at history_id %d changed no records", historyID)
	}
	return llm.ChatMessage{HistoryID: historyID, Role: role, Content: content, Timestamp: internal.ParseTimestamp(createdAt)}, turnIdx, nil
}

// ResolveRewindTimestamp keeps old timestamp clients compatible only when the
// selected second identifies exactly one user history node.
func (s *SessionService) ResolveRewindTimestamp(tenantID int64, cutoff time.Time) (int64, error) {
	if cutoff.IsZero() {
		return 0, fmt.Errorf("history_id is required")
	}
	s.db.historyMu.Lock()
	defer s.db.historyMu.Unlock()
	conn, err := s.conn()
	if err != nil {
		return 0, err
	}
	rows, err := conn.Query(`
		SELECT id FROM session_messages
		WHERE tenant_id = ? AND record_type = 'message' AND role = 'user' AND display_only = 0
		  AND unixepoch(created_at) = ?
		ORDER BY id
	`, tenantID, cutoff.Unix())
	if err != nil {
		return 0, fmt.Errorf("resolve rewind timestamp: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, fmt.Errorf("rewind timestamp does not match a user message")
	}
	if len(ids) != 1 {
		return 0, fmt.Errorf("rewind timestamp is ambiguous: %d user messages match", len(ids))
	}
	return ids[0], nil
}

func (s *SessionService) Replay(tenantID int64) (*ReplayResult, error) {
	s.db.historyMu.Lock()
	defer s.db.historyMu.Unlock()
	return s.replayLocked(tenantID)
}

func (s *SessionService) replayLocked(tenantID int64) (*ReplayResult, error) {
	records, err := s.getFullHistoryLocked(tenantID)
	if err != nil {
		return nil, err
	}
	result := &ReplayResult{}
	known := make(map[int64]struct{}, len(records))
	for _, record := range records {
		switch record.Type {
		case HistoryRecordMessage:
			if !record.Message.DisplayOnly {
				result.Messages = append(result.Messages, record.Message)
			}
		case HistoryRecordCompress, HistoryRecordPrune:
			var snapshot ContextSnapshot
			if err := decodeHistoryData(record, &snapshot); err != nil {
				return nil, err
			}
			if snapshot.Messages == nil || snapshot.HistoryIDs == nil || len(snapshot.HistoryIDs) != len(snapshot.Messages) {
				return nil, fmt.Errorf("history_id %d: invalid %s snapshot shape", record.HistoryID, record.Type)
			}
			previousMessages := result.Messages
			result.Messages = append([]llm.ChatMessage(nil), snapshot.Messages...)
			occurrences := make(map[int64]int)
			for i := range result.Messages {
				result.Messages[i].HistoryID = snapshot.HistoryIDs[i]
				if result.Messages[i].HistoryID == 0 {
					result.Messages[i].HistoryID = record.HistoryID
				} else if _, ok := known[result.Messages[i].HistoryID]; !ok {
					return nil, fmt.Errorf("history_id %d: snapshot references unknown history_id %d", record.HistoryID, result.Messages[i].HistoryID)
				} else {
					occurrence := occurrences[result.Messages[i].HistoryID]
					if activeMessageIndexOccurrence(previousMessages, result.Messages[i].HistoryID, occurrence) < 0 {
						return nil, fmt.Errorf("history_id %d: snapshot references inactive history_id %d occurrence %d", record.HistoryID, result.Messages[i].HistoryID, occurrence)
					}
					occurrences[result.Messages[i].HistoryID] = occurrence + 1
				}
			}
			if result.PendingAskUser != nil && activeMessageIndex(result.Messages, result.PendingAskUser.ToolHistoryID) < 0 {
				return nil, fmt.Errorf("history_id %d: snapshot removes pending AskUser tool target %d", record.HistoryID, result.PendingAskUser.ToolHistoryID)
			}
		case HistoryRecordContextEdit:
			var mutations MessageMutations
			if err := decodeHistoryData(record, &mutations); err != nil {
				return nil, err
			}
			if len(mutations.Mutations) == 0 {
				return nil, fmt.Errorf("history_id %d: context edit has no mutations", record.HistoryID)
			}
			if record.TargetHistoryID == 0 || record.TargetHistoryID != mutations.Mutations[0].TargetHistoryID {
				return nil, fmt.Errorf("history_id %d: context edit target metadata mismatch", record.HistoryID)
			}
			for _, mutation := range mutations.Mutations {
				if _, ok := known[mutation.TargetHistoryID]; !ok {
					return nil, fmt.Errorf("history_id %d: context edit targets unknown history_id %d", record.HistoryID, mutation.TargetHistoryID)
				}
				idx := activeMessageIndexOccurrence(result.Messages, mutation.TargetHistoryID, mutation.TargetOccurrence)
				if idx < 0 {
					return nil, fmt.Errorf("history_id %d: context edit target %d is not active", record.HistoryID, mutation.TargetHistoryID)
				}
				mutation.Message.HistoryID = mutation.TargetHistoryID
				result.Messages[idx] = mutation.Message
			}
		case HistoryRecordMask:
			var mutations MaskMutations
			if err := decodeHistoryData(record, &mutations); err != nil {
				return nil, err
			}
			if len(mutations.Mutations) == 0 {
				return nil, fmt.Errorf("history_id %d: mask has no mutations", record.HistoryID)
			}
			for _, mutation := range mutations.Mutations {
				idx := activeMessageIndexOccurrence(result.Messages, mutation.TargetHistoryID, mutation.TargetOccurrence)
				if idx < 0 {
					return nil, fmt.Errorf("history_id %d: mask target %d is not active", record.HistoryID, mutation.TargetHistoryID)
				}
				result.Messages[idx].Content = mutation.Content
			}
		case HistoryRecordAskQuestion:
			var question AskQuestionRecord
			if err := decodeHistoryData(record, &question); err != nil {
				return nil, err
			}
			if result.PendingAskUser != nil {
				return nil, fmt.Errorf("history_id %d: another AskUser question %d is still pending", record.HistoryID, result.PendingAskUser.HistoryID)
			}
			if record.TargetHistoryID == 0 || record.TargetHistoryID != question.ToolHistoryID {
				return nil, fmt.Errorf("history_id %d: AskUser question target metadata mismatch", record.HistoryID)
			}
			idx := activeMessageIndex(result.Messages, question.ToolHistoryID)
			if idx < 0 {
				return nil, fmt.Errorf("history_id %d: AskUser tool target %d is not active", record.HistoryID, question.ToolHistoryID)
			}
			if result.Messages[idx].Role != "tool" || result.Messages[idx].ToolName != "AskUser" {
				return nil, fmt.Errorf("history_id %d: AskUser target %d is not an AskUser tool result", record.HistoryID, question.ToolHistoryID)
			}
			result.PendingAskUser = &PendingAskUser{HistoryID: record.HistoryID, ToolHistoryID: question.ToolHistoryID, Metadata: question.Metadata}
		case HistoryRecordAskAnswer:
			var answer AskAnswerRecord
			if err := decodeHistoryData(record, &answer); err != nil {
				return nil, err
			}
			if answer.ToolHistoryID == 0 {
				return nil, fmt.Errorf("history_id %d: AskUser answer has no tool target", record.HistoryID)
			}
			if result.PendingAskUser == nil || result.PendingAskUser.HistoryID != record.TargetHistoryID {
				return nil, fmt.Errorf("history_id %d: AskUser answer targets non-pending question %d", record.HistoryID, record.TargetHistoryID)
			}
			idx := activeMessageIndex(result.Messages, answer.ToolHistoryID)
			if idx < 0 {
				return nil, fmt.Errorf("history_id %d: AskUser answer tool target %d is not active", record.HistoryID, answer.ToolHistoryID)
			}
			result.Messages[idx].Content = answer.Answer
			result.PendingAskUser = nil
		default:
			return nil, fmt.Errorf("history_id %d: unknown history record type %q", record.HistoryID, record.Type)
		}
		known[record.HistoryID] = struct{}{}
	}
	return result, nil
}

func decodeHistoryData(record HistoryRecord, dst any) error {
	if len(record.Data) == 0 || !json.Valid(record.Data) {
		return fmt.Errorf("history_id %d: invalid %s control data", record.HistoryID, record.Type)
	}
	if err := json.Unmarshal(record.Data, dst); err != nil {
		return fmt.Errorf("history_id %d: decode %s control data: %w", record.HistoryID, record.Type, err)
	}
	return nil
}

func activeMessageIndex(messages []llm.ChatMessage, historyID int64) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].HistoryID == historyID {
			return i
		}
	}
	return -1
}

func activeMessageIndexOccurrence(messages []llm.ChatMessage, historyID int64, occurrence int) int {
	seen := 0
	for i := range messages {
		if messages[i].HistoryID != historyID {
			continue
		}
		if seen == occurrence {
			return i
		}
		seen++
	}
	return -1
}

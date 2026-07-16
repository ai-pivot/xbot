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
	HistoryID int64
	Metadata  map[string]string
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

func (s *SessionService) appendMessage(tenantID int64, msg llm.ChatMessage) (int64, error) {
	conn, err := s.conn()
	if err != nil {
		return 0, err
	}
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
	result, err := conn.Exec(`
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

func (s *SessionService) AppendMessage(tenantID int64, msg llm.ChatMessage) (int64, error) {
	return s.appendMessage(tenantID, msg)
}

func (s *SessionService) AppendControl(tenantID int64, recordType HistoryRecordType, targetHistoryID int64, data any) (int64, error) {
	if !isControlRecordType(recordType) {
		return 0, fmt.Errorf("unknown history record type %q", recordType)
	}
	if recordType == HistoryRecordAskQuestion || recordType == HistoryRecordAskAnswer || recordType == HistoryRecordMask {
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
		replay, err := s.Replay(tenantID)
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
	if recordType != HistoryRecordCompress && recordType != HistoryRecordPrune {
		return 0, fmt.Errorf("record type %q does not accept a context snapshot", recordType)
	}
	active := make([]llm.ChatMessage, 0, len(messages))
	historyIDs := make([]int64, 0, len(messages))
	for _, msg := range messages {
		if msg.Role != "system" && !msg.DisplayOnly {
			active = append(active, msg)
			historyIDs = append(historyIDs, msg.HistoryID)
		}
	}
	return s.AppendControl(tenantID, recordType, 0, ContextSnapshot{Messages: active, HistoryIDs: historyIDs})
}

func (s *SessionService) AppendAskQuestion(tenantID int64, metadata map[string]string) (int64, error) {
	replay, err := s.Replay(tenantID)
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
	replay, err := s.Replay(tenantID)
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
	replay, err := s.Replay(tenantID)
	if err != nil {
		return 0, err
	}
	if replay.PendingAskUser == nil {
		return 0, fmt.Errorf("AskUser question is no longer pending")
	}
	records, err := s.GetFullHistory(tenantID)
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
	return records, nil
}

func (s *SessionService) Replay(tenantID int64) (*ReplayResult, error) {
	records, err := s.GetFullHistory(tenantID)
	if err != nil {
		return nil, err
	}
	result := &ReplayResult{}
	known := make(map[int64]struct{}, len(records))
	for _, record := range records {
		known[record.HistoryID] = struct{}{}
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
			result.Messages = append([]llm.ChatMessage(nil), snapshot.Messages...)
			for i := range result.Messages {
				if len(snapshot.HistoryIDs) != 0 {
					if len(snapshot.HistoryIDs) != len(result.Messages) {
						return nil, fmt.Errorf("history_id %d: snapshot history_ids length mismatch", record.HistoryID)
					}
					result.Messages[i].HistoryID = snapshot.HistoryIDs[i]
				}
				if result.Messages[i].HistoryID == 0 {
					result.Messages[i].HistoryID = record.HistoryID
				} else if _, ok := known[result.Messages[i].HistoryID]; !ok {
					return nil, fmt.Errorf("history_id %d: snapshot references unknown history_id %d", record.HistoryID, result.Messages[i].HistoryID)
				}
			}
		case HistoryRecordContextEdit:
			var mutations MessageMutations
			if err := decodeHistoryData(record, &mutations); err != nil {
				return nil, err
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
			if activeMessageIndex(result.Messages, question.ToolHistoryID) < 0 {
				return nil, fmt.Errorf("history_id %d: AskUser tool target %d is not active", record.HistoryID, question.ToolHistoryID)
			}
			result.PendingAskUser = &PendingAskUser{HistoryID: record.HistoryID, Metadata: question.Metadata}
		case HistoryRecordAskAnswer:
			var answer AskAnswerRecord
			if err := decodeHistoryData(record, &answer); err != nil {
				return nil, err
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

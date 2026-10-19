package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"xbot/llm"
	"xbot/storage/sqlite"
)

type pgxRows interface {
	Next() bool
	Scan(dest ...any) error
	Close()
}

type pgSessionStore struct {
	pool *pgxpool.Pool
}

func newPGSessionStore(pool *pgxpool.Pool) SessionStore {
	return &pgSessionStore{pool: pool}
}

// ── Messages ──

func (s *pgSessionStore) AddMessage(tenantID int64, msg llm.ChatMessage) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, s.insertMessageSQL(), s.messageArgs(tenantID, msg)...)
	return err
}

func (s *pgSessionStore) AddMessageWithID(tenantID int64, msg llm.ChatMessage) (int64, error) {
	ctx := context.Background()
	var id int64
	err := s.pool.QueryRow(ctx, s.insertMessageSQL()+" RETURNING id", s.messageArgs(tenantID, msg)...).Scan(&id)
	return id, err
}

func (s *pgSessionStore) insertMessageSQL() string {
	return `INSERT INTO session_messages
		(tenant_id, role, content, tool_call_id, tool_name, tool_arguments,
		 tool_calls, detail, reasoning_content, reasoning_items,
		 display_only, internal_only, context_tokens, turn_id, iteration,
		 record_type, target_history_id, record_data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`
}

// messageArgs builds the INSERT argument vector. Every free-text column goes
// through pgText: PostgreSQL rejects the whole statement on an invalid byte
// sequence (SQLSTATE 22021) where SQLite stored it silently, so a single torn
// character upstream must not be able to cost a conversation turn. The JSON
// columns are safe already — encoding/json replaces invalid bytes with U+FFFD.
func (s *pgSessionStore) messageArgs(tenantID int64, msg llm.ChatMessage) []any {
	toolCalls, _ := json.Marshal(msg.ToolCalls)
	if string(toolCalls) == "null" {
		toolCalls = []byte("[]")
	}
	reasoningItems, _ := json.Marshal(msg.ReasoningItems)
	if string(reasoningItems) == "null" {
		reasoningItems = []byte("[]")
	}
	return []any{
		tenantID, pgText(msg.Role), pgText(msg.Content), pgText(msg.ToolCallID), pgText(msg.ToolName),
		pgText(msg.ToolArguments), string(toolCalls), pgText(msg.Detail), pgText(msg.ReasoningContent),
		string(reasoningItems), msg.DisplayOnly, msg.Internal,
		0, msg.TurnID, msg.Iteration,
		"message", 0, "", // record_type, target_history_id, record_data (set by caller)
	}
}

func (s *pgSessionStore) ReplaceToolMessage(tenantID int64, toolName, toolCallID, content string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`UPDATE session_messages SET content = $1
		 WHERE id = (
		   SELECT id FROM session_messages
		   WHERE tenant_id = $2 AND tool_name = $3 AND tool_call_id = $4
		   ORDER BY id DESC LIMIT 1
		 )`, pgText(content), tenantID, pgText(toolName), pgText(toolCallID))
	return err
}

func (s *pgSessionStore) GetHistory(tenantID int64, limit int) ([]llm.ChatMessage, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx, s.selectMessageSQL()+` WHERE tenant_id = $1 ORDER BY id ASC LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *pgSessionStore) GetHistoryBefore(tenantID int64, beforeID int64, limit int) ([]llm.ChatMessage, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx, s.selectMessageSQL()+` WHERE tenant_id = $1 AND id < $2 ORDER BY id DESC LIMIT $3`, tenantID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

func (s *pgSessionStore) GetAllMessages(tenantID int64) ([]llm.ChatMessage, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx, s.selectMessageSQL()+` WHERE tenant_id = $1 ORDER BY id ASC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *pgSessionStore) GetMessagesCount(tenantID int64) (int, error) {
	ctx := context.Background()
	var count int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM session_messages WHERE tenant_id = $1`, tenantID).Scan(&count)
	return count, err
}

func (s *pgSessionStore) Clear(tenantID int64) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `DELETE FROM session_messages WHERE tenant_id = $1`, tenantID)
	return err
}

func (s *pgSessionStore) UpdateMessageContent(tenantID int64, messageIndex int, content string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`UPDATE session_messages SET content = $1
		 WHERE id = (SELECT id FROM session_messages WHERE tenant_id = $2 ORDER BY id ASC LIMIT 1 OFFSET $3)`,
		pgText(content), tenantID, messageIndex)
	return err
}

func (s *pgSessionStore) UpdateUserMessageContextTokens(tenantID int64, promptTokens int64) error {
	ctx := context.Background()
	// COALESCE(display_only, false) = false matches the SQLite contract: command rows
	// (`!cmd`, role='user' + display_only=1) must never receive the watermark —
	// they are display projections and carry no LLM context, so stamping them
	// would make the rewind/resume token restore read 0.
	_, err := s.pool.Exec(ctx,
		`UPDATE session_messages SET context_tokens = $1
		 WHERE id = (SELECT id FROM session_messages WHERE tenant_id = $2 AND role = 'user' AND COALESCE(display_only, false) = false ORDER BY id DESC LIMIT 1)`,
		promptTokens, tenantID)
	return err
}

func (s *pgSessionStore) GetLastUserMessageContent(tenantID int64) (string, error) {
	ctx := context.Background()
	var content string
	err := s.pool.QueryRow(ctx,
		`SELECT content FROM session_messages
		 WHERE tenant_id = $1 AND role = 'user' AND display_only = false AND internal_only = false
		 ORDER BY id DESC LIMIT 1`, tenantID).Scan(&content)
	return content, err
}

func (s *pgSessionStore) HasAssistantReplyAfterLastUser(tenantID int64) (bool, error) {
	ctx := context.Background()
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM session_messages sm
		 WHERE sm.tenant_id = $1 AND sm.role = 'assistant' AND sm.display_only = false
		 AND (sm.tool_calls IS NULL OR sm.tool_calls = '' OR sm.tool_calls = '[]' OR sm.tool_calls = 'null')
		 AND sm.id > (SELECT COALESCE(MAX(id), 0) FROM session_messages
		 WHERE tenant_id = $1 AND role = 'user' AND display_only = false AND internal_only = false)`,
		tenantID).Scan(&count)
	return count > 0, err
}

// ── Turns ──

func (s *pgSessionStore) GetMaxTurnID(tenantID int64) (uint64, error) {
	ctx := context.Background()
	var turnID uint64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(turn_id), 0) FROM session_messages WHERE tenant_id = $1`, tenantID).Scan(&turnID)
	return turnID, err
}

func (s *pgSessionStore) GetLastUserTurnID(tenantID int64) (uint64, error) {
	ctx := context.Background()
	var turnID uint64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(turn_id, 0) FROM session_messages WHERE tenant_id = $1 AND role = 'user' AND display_only = false ORDER BY id DESC LIMIT 1`,
		tenantID).Scan(&turnID)
	return turnID, err
}

func (s *pgSessionStore) GetMaxIterationForTurn(tenantID int64, turnID uint64) (int, error) {
	ctx := context.Background()
	var iter int
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(iteration), 0) FROM iteration_history WHERE tenant_id = $1 AND turn_id = $2`, tenantID, turnID).Scan(&iter)
	return iter, err
}

// ── Iteration history ──

func (s *pgSessionStore) AppendIterationHistory(tenantID int64, msgID int64, turnID uint64, rec sqlite.IterationRecord) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO iteration_history
		 (message_id, tenant_id, turn_id, iteration, content, reasoning, tools, tokens, ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens, model, subscription_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		msgID, tenantID, turnID, rec.Iteration, pgText(rec.Content), pgText(rec.Reasoning), pgText(rec.Tools),
		rec.Tokens, rec.TTFTMs, rec.TokensPerSec, rec.TotalMs, rec.TPOTMs,
		rec.InputTokens, rec.CachedTokens, rec.Model, rec.SubscriptionID)
	return err
}

func (s *pgSessionStore) AppendIterationTool(tenantID int64, turnID uint64, iteration int, toolJSON string) (bool, error) {
	ctx := context.Background()
	tag, err := s.pool.Exec(ctx, `UPDATE iteration_history SET tools = $1 WHERE tenant_id = $2 AND turn_id = $3 AND iteration = $4`, pgText(toolJSON), tenantID, turnID, iteration)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *pgSessionStore) GetIterationHistoryByTurn(tenantID int64, turnID uint64) ([]sqlite.IterationRecord, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT iteration, content, reasoning, tools, tokens, ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens, model, subscription_id
		 FROM iteration_history WHERE tenant_id = $1 AND turn_id = $2 ORDER BY iteration ASC`, tenantID, turnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIterations(rows)
}

func (s *pgSessionStore) GetIterationHistoryByTurns(tenantID int64, turnIDs []uint64) (map[uint64][]sqlite.IterationRecord, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT turn_id, iteration, content, reasoning, tools, tokens, ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens, model, subscription_id
		 FROM iteration_history WHERE tenant_id = $1 AND turn_id = ANY($2) ORDER BY turn_id, iteration ASC`, tenantID, turnIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[uint64][]sqlite.IterationRecord)
	for rows.Next() {
		var tid uint64
		var rec sqlite.IterationRecord
		if err := rows.Scan(&tid, &rec.Iteration, &rec.Content, &rec.Reasoning, &rec.Tools, &rec.Tokens, &rec.TTFTMs, &rec.TokensPerSec, &rec.TotalMs, &rec.TPOTMs, &rec.InputTokens, &rec.CachedTokens, &rec.Model, &rec.SubscriptionID); err != nil {
			return nil, err
		}
		result[tid] = append(result[tid], rec)
	}
	return result, nil
}

// GetIterationHistoryBeforeRange returns the iterations of a turn that precede
// beforeIter, ascending — the windowing endpoint's segment query. Same ordering
// and shape as the SQLite implementation.
func (s *pgSessionStore) GetIterationHistoryBeforeRange(tenantID int64, turnID uint64, beforeIter int) ([]sqlite.IterationRecord, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT iteration, content, reasoning, tools, tokens, ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens, model, subscription_id
		 FROM iteration_history WHERE tenant_id = $1 AND turn_id = $2 AND iteration < $3
		 ORDER BY iteration ASC`, tenantID, turnID, beforeIter)
	if err != nil {
		return nil, fmt.Errorf("get iteration_history before range: %w", err)
	}
	defer rows.Close()
	return scanIterations(rows)
}

// GetIterationHistoryByNumber locates ONE iteration by (turn, iteration).
// Not-found is a normal business result: (zero, false, nil) — the handler turns it
// into a 404. ORDER BY id ASC LIMIT 1 keeps the same tie-break as SQLite when
// duplicate rows exist.
func (s *pgSessionStore) GetIterationHistoryByNumber(tenantID int64, turnID uint64, iteration int) (sqlite.IterationRecord, bool, error) {
	ctx := context.Background()
	var rec sqlite.IterationRecord
	var createdAt time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT iteration, content, reasoning, tools, tokens, ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens, model, subscription_id, created_at
		 FROM iteration_history WHERE tenant_id = $1 AND turn_id = $2 AND iteration = $3
		 ORDER BY id ASC LIMIT 1`, tenantID, turnID, iteration).Scan(
		&rec.Iteration, &rec.Content, &rec.Reasoning, &rec.Tools, &rec.Tokens,
		&rec.TTFTMs, &rec.TokensPerSec, &rec.TotalMs, &rec.TPOTMs,
		&rec.InputTokens, &rec.CachedTokens, &rec.Model, &rec.SubscriptionID, &createdAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlite.IterationRecord{}, false, nil
	}
	if err != nil {
		return sqlite.IterationRecord{}, false, fmt.Errorf("get iteration_history by number: %w", err)
	}
	rec.TurnID = turnID
	rec.CreatedAt = createdAt
	return rec, true, nil
}

// GetSyntheticPairRowsByTurn returns the turn's tool-pair candidate rows
// (assistant rows carrying tool_calls + tool results, in id order) used to repair
// histories written before synthetic tool pairs were persisted. The synthetic-name
// judgement stays in the channel layer — SQL only narrows to candidate rows.
func (s *pgSessionStore) GetSyntheticPairRowsByTurn(tenantID int64, turnID uint64) ([]llm.ChatMessage, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT id, role, content, COALESCE(tool_call_id, ''), COALESCE(tool_name, ''),
		        COALESCE(tool_calls, ''), turn_id, created_at, iteration
		 FROM session_messages
		 WHERE tenant_id = $1 AND turn_id = $2 AND record_type = 'message'
		   AND display_only = false AND internal_only = false
		   AND ((role = 'assistant' AND COALESCE(tool_calls, '') NOT IN ('', '[]', 'null'))
		     OR role = 'tool')
		 ORDER BY id ASC`, tenantID, turnID)
	if err != nil {
		return nil, fmt.Errorf("get synthetic pair rows by turn: %w", err)
	}
	defer rows.Close()
	var msgs []llm.ChatMessage
	for rows.Next() {
		var m llm.ChatMessage
		var toolCalls string
		var createdAt time.Time
		var turnIDCol int64
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.ToolCallID, &m.ToolName,
			&toolCalls, &turnIDCol, &createdAt, &m.Iteration); err != nil {
			continue // a single corrupt row must not block the whole segment
		}
		m.TurnID = uint64(turnIDCol)
		m.Timestamp = createdAt
		if toolCalls != "" && toolCalls != "null" {
			if err := json.Unmarshal([]byte(toolCalls), &m.ToolCalls); err != nil {
				continue
			}
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// ── v71 lifecycle ──

func (s *pgSessionStore) HasLegacyRows(tenantID int64) (bool, error) {
	ctx := context.Background()
	var count int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM session_messages WHERE tenant_id = $1`, tenantID).Scan(&count)
	return count > 0, err
}

func (s *pgSessionStore) LatestPreview(tenantID int64) (string, error) {
	ctx := context.Background()
	var preview string
	err := s.pool.QueryRow(ctx, `SELECT content FROM session_messages WHERE tenant_id = $1 AND role IN ('user','assistant') AND display_only = false ORDER BY id DESC LIMIT 1`, tenantID).Scan(&preview)
	return preview, err
}

// ── Append-style writes (return the inserted row's stable history id) ───────
//
// PG simplifications (this backend exists for backend parity / testing — see
// NewMultiTenantPG): no per-tenant history lock (the shared pool serialises at
// statement level) and no semantic validation of control records (mask /
// context-edit target liveness, single-pending-AskUser enforcement). The *row
// shapes* written here are identical to SQLite's (role='control',
// display_only=1, record_type, target_history_id, record_data) so every shared
// read path (GetFullHistory / Replay / GetHistoryBeforeForDisplay / usage)
// observes the same data.

// AppendMessage appends one message and returns its stable history id
// (same row shape as AddMessageWithID — both route through messageArgs).
func (s *pgSessionStore) AppendMessage(tenantID int64, msg llm.ChatMessage) (int64, error) {
	return s.AddMessageWithID(tenantID, msg)
}

// AppendMessages appends a related message batch in one transaction.
func (s *pgSessionStore) AppendMessages(tenantID int64, messages []llm.ChatMessage) ([]int64, error) {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ids := make([]int64, 0, len(messages))
	for i, msg := range messages {
		var id int64
		if err := tx.QueryRow(ctx, s.insertMessageSQL()+" RETURNING id", s.messageArgs(tenantID, msg)...).Scan(&id); err != nil {
			return nil, fmt.Errorf("append message batch item %d: %w", i, err)
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ids, nil
}

// AppendMessagesAndAskQuestion atomically persists an AskUser tool exchange and
// its pending-question control record (target_history_id = the AskUser tool
// row's id, exactly like SQLite's appendAskQuestionWith).
func (s *pgSessionStore) AppendMessagesAndAskQuestion(tenantID int64, messages []llm.ChatMessage, metadata map[string]string) ([]int64, int64, error) {
	toolIndex := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "tool" && messages[i].ToolName == "AskUser" {
			toolIndex = i
			break
		}
	}
	if toolIndex < 0 {
		return nil, 0, fmt.Errorf("append AskUser question: message batch has no AskUser tool result")
	}
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ids := make([]int64, 0, len(messages))
	for i, msg := range messages {
		var id int64
		if err := tx.QueryRow(ctx, s.insertMessageSQL()+" RETURNING id", s.messageArgs(tenantID, msg)...).Scan(&id); err != nil {
			return nil, 0, fmt.Errorf("append AskUser message batch item %d: %w", i, err)
		}
		ids = append(ids, id)
	}
	question := sqlite.AskQuestionRecord{Metadata: metadata, ToolHistoryID: ids[toolIndex]}
	payload, err := json.Marshal(question)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal AskUser question: %w", err)
	}
	var questionID int64
	if err := tx.QueryRow(ctx, s.insertControlSQL()+" RETURNING id",
		tenantID, string(sqlite.HistoryRecordAskQuestion), ids[toolIndex], string(payload)).Scan(&questionID); err != nil {
		return nil, 0, fmt.Errorf("append AskUser question: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return ids, questionID, nil
}

// AppendCommandMessage persists a `!cmd` / slash input-or-output row: display
// projection only (display_only=1 + record_type='command'), no turn, never part
// of the LLM context — so it survives a page refresh (mirrors SQLite).
func (s *pgSessionStore) AppendCommandMessage(tenantID int64, role, content string) (int64, error) {
	ctx := context.Background()
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO session_messages
		(tenant_id, role, content, display_only, record_type, turn_id)
		VALUES ($1, $2, $3, true, 'command', 0) RETURNING id`, tenantID, pgText(role), pgText(content)).Scan(&id)
	return id, err
}

// AppendControl appends a control record. Same guard as SQLite: the control
// types that own an atomic append path (compress / prune / ask_question /
// ask_answer / mask) are rejected here — only context_edit travels this way.
func (s *pgSessionStore) AppendControl(tenantID int64, recordType sqlite.HistoryRecordType, targetHistoryID int64, data any) (int64, error) {
	if recordType != sqlite.HistoryRecordContextEdit {
		return 0, fmt.Errorf("append control: record type %q requires its atomic append method", recordType)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return 0, fmt.Errorf("marshal %s history data: %w", recordType, err)
	}
	ctx := context.Background()
	var id int64
	if err := s.pool.QueryRow(ctx, s.insertControlSQL()+" RETURNING id",
		tenantID, string(recordType), targetHistoryID, string(raw)).Scan(&id); err != nil {
		return 0, fmt.Errorf("append %s history record: %w", recordType, err)
	}
	return id, nil
}

// AppendContextSnapshot appends a compression/prune snapshot control record.
// The active-message filter matches SQLite (system messages and display-only
// rows are dropped); snapshot liveness validation is not replicated.
func (s *pgSessionStore) AppendContextSnapshot(tenantID int64, recordType sqlite.HistoryRecordType, messages []llm.ChatMessage) (int64, error) {
	if recordType != sqlite.HistoryRecordCompress && recordType != sqlite.HistoryRecordPrune {
		return 0, fmt.Errorf("record type %q does not accept a context snapshot", recordType)
	}
	active := make([]llm.ChatMessage, 0, len(messages))
	historyIDs := make([]int64, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "system" || msg.DisplayOnly {
			continue
		}
		active = append(active, msg)
		historyIDs = append(historyIDs, msg.ID)
	}
	// Version 1 == sqlite.contextSnapshotVersion (unexported there; the replay
	// decoder treats 0 as unset and rejects any other value).
	raw, err := json.Marshal(sqlite.ContextSnapshot{Version: 1, Messages: active, HistoryIDs: historyIDs})
	if err != nil {
		return 0, fmt.Errorf("marshal %s history data: %w", recordType, err)
	}
	ctx := context.Background()
	var id int64
	if err := s.pool.QueryRow(ctx, s.insertControlSQL()+" RETURNING id",
		tenantID, string(recordType), 0, string(raw)).Scan(&id); err != nil {
		return 0, fmt.Errorf("append %s history record: %w", recordType, err)
	}
	return id, nil
}

// AppendAskQuestion appends the pending-question control record. Same contract
// as SQLite: refuse when a question is already pending, and target the newest
// AskUser tool result that is still ACTIVE in the replay (a tool row hidden by a
// later snapshot/compression must not be re-asked against).
func (s *pgSessionStore) AppendAskQuestion(tenantID int64, metadata map[string]string) (int64, error) {
	ctx := context.Background()
	replay, err := s.Replay(tenantID)
	if err != nil {
		return 0, fmt.Errorf("append AskUser question: %w", err)
	}
	if replay.PendingAskUser != nil {
		return 0, fmt.Errorf("AskUser question %d is still pending", replay.PendingAskUser.HistoryID)
	}
	var toolHistoryID int64
	for i := len(replay.Messages) - 1; i >= 0; i-- {
		if replay.Messages[i].Role == "tool" && replay.Messages[i].ToolName == "AskUser" {
			toolHistoryID = replay.Messages[i].ID
			break
		}
	}
	if toolHistoryID == 0 {
		return 0, fmt.Errorf("append AskUser question: matching active tool result not found")
	}
	raw, err := json.Marshal(sqlite.AskQuestionRecord{Metadata: metadata, ToolHistoryID: toolHistoryID})
	if err != nil {
		return 0, fmt.Errorf("marshal AskUser question: %w", err)
	}
	var id int64
	if err := s.pool.QueryRow(ctx, s.insertControlSQL()+" RETURNING id",
		tenantID, string(sqlite.HistoryRecordAskQuestion), toolHistoryID, string(raw)).Scan(&id); err != nil {
		return 0, fmt.Errorf("append AskUser question: %w", err)
	}
	return id, nil
}

// AppendAskAnswer appends the answer control record for the newest pending
// question (target_history_id = the ask_question row's id, same association as
// SQLite). The "still pending" check is a read of the newest ask_question —
// PG has no per-tenant history lock, so a concurrent double answer is not
// serialised away here (documented simplification).
func (s *pgSessionStore) AppendAskAnswer(tenantID int64, answer string) (int64, error) {
	ctx := context.Background()
	questionID, toolHistoryID, err := s.pendingAskQuestion(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	if questionID == 0 {
		return 0, fmt.Errorf("AskUser question is no longer pending")
	}
	raw, err := json.Marshal(sqlite.AskAnswerRecord{Answer: answer, ToolHistoryID: toolHistoryID})
	if err != nil {
		return 0, fmt.Errorf("marshal AskUser answer: %w", err)
	}
	var id int64
	if err := s.pool.QueryRow(ctx, s.insertControlSQL()+" RETURNING id",
		tenantID, string(sqlite.HistoryRecordAskAnswer), questionID, string(raw)).Scan(&id); err != nil {
		return 0, fmt.Errorf("append AskUser answer: %w", err)
	}
	return id, nil
}

// AppendAskAnswerWithUserMessage atomically appends the ask_answer control
// record AND the answer user message (crash consistency — same contract as
// SQLite: never an answer control without its user anchor).
func (s *pgSessionStore) AppendAskAnswerWithUserMessage(tenantID int64, answer string, answerMsg llm.ChatMessage) (int64, error) {
	ctx := context.Background()
	questionID, toolHistoryID, err := s.pendingAskQuestion(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	if questionID == 0 {
		return 0, fmt.Errorf("AskUser question is no longer pending")
	}
	raw, err := json.Marshal(sqlite.AskAnswerRecord{Answer: answer, ToolHistoryID: toolHistoryID})
	if err != nil {
		return 0, fmt.Errorf("marshal AskUser answer: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var historyID int64
	if err := tx.QueryRow(ctx, s.insertControlSQL()+" RETURNING id",
		tenantID, string(sqlite.HistoryRecordAskAnswer), questionID, string(raw)).Scan(&historyID); err != nil {
		return 0, fmt.Errorf("append AskUser answer: %w", err)
	}
	if _, err := tx.Exec(ctx, s.insertMessageSQL(), s.messageArgs(tenantID, answerMsg)...); err != nil {
		return 0, fmt.Errorf("append AskUser answer user message: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return historyID, nil
}

// pendingAskQuestion resolves the newest ask_question record that has no
// ask_answer yet, plus the tool row it targets. Returns (0, 0, nil) when there
// is no pending question.
func (s *pgSessionStore) pendingAskQuestion(ctx context.Context, tenantID int64) (questionID, toolHistoryID int64, err error) {
	var rawData string
	err = s.pool.QueryRow(ctx, `SELECT q.id, COALESCE(q.record_data, '')
		FROM session_messages q
		WHERE q.tenant_id = $1 AND q.record_type = 'ask_question'
		  AND NOT EXISTS (
			SELECT 1 FROM session_messages a
			WHERE a.tenant_id = q.tenant_id AND a.record_type = 'ask_answer'
			  AND a.target_history_id = q.id
		  )
		ORDER BY q.id DESC LIMIT 1`, tenantID).Scan(&questionID, &rawData)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("resolve pending AskUser question: %w", err)
	}
	var question sqlite.AskQuestionRecord
	if err := json.Unmarshal([]byte(rawData), &question); err != nil {
		return 0, 0, fmt.Errorf("decode AskUser question %d: %w", questionID, err)
	}
	return questionID, question.ToolHistoryID, nil
}

// AppendMasks records an observation-masking control record. Same contract as
// SQLite: an empty mutation list is a no-op, every mutation must carry a history
// id, and every target must still be ACTIVE in the replay — a mask aimed at a row
// that a snapshot/compression removed is rejected rather than silently recorded.
// Replay applies the record, so masked content is what the model sees.
func (s *pgSessionStore) AppendMasks(tenantID int64, mutations []sqlite.MaskMutation) error {
	if len(mutations) == 0 {
		return nil
	}
	for _, mutation := range mutations {
		if mutation.TargetHistoryID == 0 {
			return fmt.Errorf("mask target has no history_id")
		}
	}
	raw, err := json.Marshal(sqlite.MaskMutations{Mutations: mutations})
	if err != nil {
		return fmt.Errorf("marshal mask history data: %w", err)
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
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_messages
		(tenant_id, role, content, display_only, record_type, record_data)
		VALUES ($1, 'control', '', true, $2, $3)`,
		tenantID, string(sqlite.HistoryRecordMask), string(raw)); err != nil {
		return fmt.Errorf("append mask history record: %w", err)
	}
	return nil
}

// insertControlSQL inserts a control record (role='control', display-only,
// never LLM context). $1 tenant, $2 record_type, $3 target_history_id (0 NULL),
// $4 record_data.
func (s *pgSessionStore) insertControlSQL() string {
	return `INSERT INTO session_messages
		(tenant_id, role, content, display_only, record_type, target_history_id, record_data)
		VALUES ($1, 'control', '', true, $2, NULLIF($3::bigint, 0), $4)`
}

// ── History reads ──────────────────────────────────────────────────────────

// GetAllIterationHistory returns every iteration record for the tenant, ordered
// by (turn_id, iteration) — the ordering session fork relies on.
func (s *pgSessionStore) GetAllIterationHistory(tenantID int64) ([]sqlite.IterationRecord, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT turn_id, message_id, iteration, content, reasoning, tools, tokens, ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens, model, subscription_id, created_at
		 FROM iteration_history WHERE tenant_id = $1 ORDER BY turn_id ASC, iteration ASC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get all iteration_history: %w", err)
	}
	defer rows.Close()
	var result []sqlite.IterationRecord
	for rows.Next() {
		var rec sqlite.IterationRecord
		var createdAt time.Time
		if err := rows.Scan(&rec.TurnID, &rec.MessageID, &rec.Iteration, &rec.Content, &rec.Reasoning,
			&rec.Tools, &rec.Tokens, &rec.TTFTMs, &rec.TokensPerSec, &rec.TotalMs, &rec.TPOTMs,
			&rec.InputTokens, &rec.CachedTokens, &rec.Model, &rec.SubscriptionID, &createdAt); err != nil {
			return nil, err
		}
		rec.CreatedAt = createdAt
		result = append(result, rec)
	}
	return result, rows.Err()
}

// GetFullHistory returns every raw history row (messages, command rows and
// control records) ordered by id ASC — the append-only record stream.
func (s *pgSessionStore) GetFullHistory(tenantID int64) ([]sqlite.HistoryRecord, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT id, COALESCE(record_type, 'message'), COALESCE(target_history_id, 0), COALESCE(record_data, ''),
		        role, content, COALESCE(tool_call_id, ''), COALESCE(tool_name, ''), COALESCE(tool_arguments, ''),
		        COALESCE(tool_calls, ''), COALESCE(detail, ''), COALESCE(reasoning_content, ''), COALESCE(reasoning_items, ''),
		        COALESCE(display_only, false), COALESCE(internal_only, false), COALESCE(turn_id, 0), COALESCE(iteration, 0), created_at
		 FROM session_messages WHERE tenant_id = $1 ORDER BY id ASC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query full session history: %w", err)
	}
	defer rows.Close()
	var records []sqlite.HistoryRecord
	for rows.Next() {
		var rec sqlite.HistoryRecord
		var recordType, rawData, role, content string
		var toolCallID, toolName, toolArguments, toolCallsStr, detail, reasoning, reasoningItems string
		var displayOnly, internalOnly bool
		var iteration int
		var turnID uint64
		var createdAt time.Time
		if err := rows.Scan(&rec.HistoryID, &recordType, &rec.TargetHistoryID, &rawData,
			&role, &content, &toolCallID, &toolName, &toolArguments, &toolCallsStr, &detail,
			&reasoning, &reasoningItems, &displayOnly, &internalOnly, &turnID, &iteration, &createdAt); err != nil {
			return nil, fmt.Errorf("scan history record: %w", err)
		}
		rec.Type = sqlite.HistoryRecordType(recordType)
		rec.CreatedAt = createdAt
		rec.Data = json.RawMessage(rawData)
		if rec.Type == sqlite.HistoryRecordMessage || rec.Type == sqlite.HistoryRecordCommand {
			msg := llm.ChatMessage{
				ID: rec.HistoryID, Role: role, Content: content,
				DisplayOnly: displayOnly, Internal: internalOnly,
				CommandRow: rec.Type == sqlite.HistoryRecordCommand,
				Timestamp:  createdAt, TurnID: turnID, Iteration: iteration,
				ToolCallID: toolCallID, ToolName: toolName, ToolArguments: toolArguments,
				Detail: detail, ReasoningContent: reasoning,
			}
			if toolCallsStr != "" && toolCallsStr != "null" {
				_ = json.Unmarshal([]byte(toolCallsStr), &msg.ToolCalls)
			}
			if reasoningItems != "" && reasoningItems != "null" {
				_ = json.Unmarshal([]byte(reasoningItems), &msg.ReasoningItems)
			}
			rec.Message = msg
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

// GetHistoryBeforeForDisplay returns the messages before beforeID plus the total
// number of history rows before beforeID (so has_more converges at the first
// page), mirroring the SQLite contract.
func (s *pgSessionStore) GetHistoryBeforeForDisplay(tenantID int64, beforeID int64, limit int) ([]llm.ChatMessage, int, error) {
	if limit <= 0 {
		return nil, 0, nil
	}
	// Normalize beforeID: 0 (unbounded) becomes MaxInt64 so every id matches
	// (mirrors SQLite's replayForDisplayWindow normalization).
	if beforeID <= 0 {
		beforeID = math.MaxInt64
	}
	ctx := context.Background()
	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM session_messages WHERE tenant_id = $1 AND id < $2`, tenantID, beforeID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count display rows before %d: %w", beforeID, err)
	}
	if total == 0 {
		return nil, 0, nil
	}
	msgs, err := s.GetHistoryBefore(tenantID, beforeID, limit)
	if err != nil {
		return nil, 0, err
	}
	return msgs, total, nil
}

// Replay reconstructs the LLM-visible message stream by folding the append-only
// history through sqlite.ApplyHistoryRecords — the same implementation the
// SQLite backend uses, so compression snapshots, masks, context edits and
// AskUser control records can never drift between backends.
func (s *pgSessionStore) Replay(tenantID int64) (*sqlite.ReplayResult, error) {
	ctx := context.Background()
	checkpointID, hasCheckpoint, err := s.pgLatestCheckpoint(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	fromHistoryID := int64(0)
	if hasCheckpoint {
		fromHistoryID = checkpointID
	}
	records, err := s.pgHistoryRecords(ctx, tenantID, fromHistoryID)
	if err != nil {
		return nil, err
	}
	return sqlite.ApplyHistoryRecords(records, hasCheckpoint)
}

// pgLatestCheckpoint returns the newest compress/prune snapshot that
// sqlite.ValidCheckpointSnapshot accepts (mirrors SQLite's latestCheckpointWith).
func (s *pgSessionStore) pgLatestCheckpoint(ctx context.Context, tenantID int64) (int64, bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, COALESCE(record_data, '') FROM session_messages
		WHERE tenant_id = $1 AND record_type IN ($2, $3) ORDER BY id DESC`,
		tenantID, string(sqlite.HistoryRecordCompress), string(sqlite.HistoryRecordPrune))
	if err != nil {
		return 0, false, fmt.Errorf("query replay checkpoints: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return 0, false, fmt.Errorf("scan replay checkpoint: %w", err)
		}
		var snapshot sqlite.ContextSnapshot
		if json.Unmarshal([]byte(raw), &snapshot) != nil || !sqlite.ValidCheckpointSnapshot(id, snapshot) {
			continue
		}
		return id, true, nil
	}
	if err := rows.Err(); err != nil {
		return 0, false, fmt.Errorf("iterate replay checkpoints: %w", err)
	}
	return 0, false, nil
}

// pgHistoryRecords reads the append-only history rows in id order, building the
// same HistoryRecord shapes getHistoryFromWith produces for SQLite: control rows
// carry Type/TargetHistoryID/Data, message and command rows also carry the
// decoded Message (the fold keys everything off those fields).
func (s *pgSessionStore) pgHistoryRecords(ctx context.Context, tenantID, fromHistoryID int64) ([]sqlite.HistoryRecord, error) {
	query := `SELECT id, record_type, COALESCE(target_history_id, 0), COALESCE(record_data, ''),
		       role, content, COALESCE(tool_call_id, ''), COALESCE(tool_name, ''), COALESCE(tool_arguments, ''),
		       COALESCE(tool_calls, ''), COALESCE(detail, ''), reasoning_content, reasoning_items,
		       display_only, internal_only, created_at, turn_id, iteration
		FROM session_messages WHERE tenant_id = $1`
	args := []any{tenantID}
	if fromHistoryID > 0 {
		query += ` AND id >= $2`
		args = append(args, fromHistoryID)
	}
	query += ` ORDER BY id ASC`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query full session history: %w", err)
	}
	defer rows.Close()
	var records []sqlite.HistoryRecord
	for rows.Next() {
		var record sqlite.HistoryRecord
		var rawData, role, content string
		var toolCallID, toolName, toolArguments, toolCallsJSON, detail, reasoning, reasoningItems string
		var displayOnly, internalOnly bool
		var createdAt time.Time
		var turnID, iteration int64
		if err := rows.Scan(&record.HistoryID, &record.Type, &record.TargetHistoryID, &rawData,
			&role, &content, &toolCallID, &toolName, &toolArguments, &toolCallsJSON, &detail,
			&reasoning, &reasoningItems, &displayOnly, &internalOnly, &createdAt, &turnID, &iteration); err != nil {
			return nil, fmt.Errorf("scan history record: %w", err)
		}
		record.CreatedAt = createdAt
		record.Data = json.RawMessage(rawData)
		if record.Type == sqlite.HistoryRecordMessage || record.Type == sqlite.HistoryRecordCommand {
			msg := llm.ChatMessage{
				ID: record.HistoryID, Role: role, Content: content,
				DisplayOnly: displayOnly, Internal: internalOnly,
				CommandRow:       record.Type == sqlite.HistoryRecordCommand,
				Timestamp:        createdAt,
				Iteration:        int(iteration),
				TurnID:           uint64(turnID),
				ToolCallID:       toolCallID,
				ToolName:         toolName,
				ToolArguments:    toolArguments,
				Detail:           detail,
				ReasoningContent: reasoning,
			}
			if toolCallsJSON != "" && toolCallsJSON != "null" {
				if err := json.Unmarshal([]byte(toolCallsJSON), &msg.ToolCalls); err != nil {
					return nil, fmt.Errorf("history_id %d: decode tool_calls: %w", record.HistoryID, err)
				}
			}
			if reasoningItems != "" && reasoningItems != "null" {
				if err := json.Unmarshal([]byte(reasoningItems), &msg.ReasoningItems); err != nil {
					return nil, fmt.Errorf("history_id %d: decode reasoning_items: %w", record.HistoryID, err)
				}
			}
			record.Message = msg
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate full session history: %w", err)
	}
	return records, nil
}

// activeMessageIndexOccurrence returns the index of the Nth (0-based) occurrence
// of historyID in the replayed messages, or -1 when it is not active. Same
// contract as sqlite's activeMessageIndexOccurrence: control-record targets are
// validated against the folded message list, not the raw table.
func activeMessageIndexOccurrence(messages []llm.ChatMessage, historyID int64, occurrence int) int {
	if occurrence < 0 {
		return -1
	}
	seen := 0
	for i, msg := range messages {
		if msg.ID != historyID {
			continue
		}
		if seen == occurrence {
			return i
		}
		seen++
	}
	return -1
}

// RewindToHistoryID truncates the tenant's history at the given user message
// (iteration_history first, then session_messages) and returns the target
// message, its 1-based turn index and the restored prompt-token watermark.
func (s *pgSessionStore) RewindToHistoryID(tenantID, historyID int64) (llm.ChatMessage, int, int64, error) {
	if historyID <= 0 {
		return llm.ChatMessage{}, 0, 0, fmt.Errorf("history_id is required")
	}
	ctx := context.Background()
	var role, recordType, content string
	var displayOnly bool
	var createdAt time.Time
	err := s.pool.QueryRow(ctx, `SELECT role, COALESCE(record_type, 'message'), content, COALESCE(display_only, false), created_at
		FROM session_messages WHERE tenant_id = $1 AND id = $2`, tenantID, historyID).
		Scan(&role, &recordType, &content, &displayOnly, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return llm.ChatMessage{}, 0, 0, fmt.Errorf("rewind target history_id %d not found", historyID)
	}
	if err != nil {
		return llm.ChatMessage{}, 0, 0, fmt.Errorf("load rewind target: %w", err)
	}
	if displayOnly || role != "user" || sqlite.HistoryRecordType(recordType) != sqlite.HistoryRecordMessage {
		return llm.ChatMessage{}, 0, 0, fmt.Errorf("history_id %d is not a rewindable user message", historyID)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return llm.ChatMessage{}, 0, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var turnIdx int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM session_messages
		WHERE tenant_id = $1 AND id <= $2 AND COALESCE(record_type, 'message') = 'message'
		  AND role = 'user' AND COALESCE(display_only, false) = false`, tenantID, historyID).Scan(&turnIdx); err != nil {
		return llm.ChatMessage{}, 0, 0, fmt.Errorf("resolve rewind turn: %w", err)
	}
	// iteration_history first (no FK cascade): by turn_id (authoritative
	// association) with the message_id fallback for legacy rows carrying a real
	// message id — same order and predicates as SQLite.
	if _, err := tx.Exec(ctx, `DELETE FROM iteration_history WHERE tenant_id = $1 AND (
		turn_id IN (SELECT turn_id FROM session_messages WHERE tenant_id = $1 AND id >= $2 AND turn_id > 0)
		OR message_id IN (SELECT id FROM session_messages WHERE tenant_id = $1 AND id >= $2)
	)`, tenantID, historyID); err != nil {
		return llm.ChatMessage{}, 0, 0, fmt.Errorf("truncate iteration history at history_id %d: %w", historyID, err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM session_messages WHERE tenant_id = $1 AND id >= $2`, tenantID, historyID)
	if err != nil {
		return llm.ChatMessage{}, 0, 0, fmt.Errorf("truncate history at history_id %d: %w", historyID, err)
	}
	if tag.RowsAffected() == 0 {
		return llm.ChatMessage{}, 0, 0, fmt.Errorf("truncate history at history_id %d changed no records", historyID)
	}
	var promptTokens int64
	err = tx.QueryRow(ctx, `SELECT COALESCE(context_tokens, 0) FROM session_messages
		WHERE tenant_id = $1 AND COALESCE(record_type, 'message') = 'message'
		  AND role = 'user' AND COALESCE(display_only, false) = false
		ORDER BY id DESC LIMIT 1`, tenantID).Scan(&promptTokens)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return llm.ChatMessage{}, 0, 0, fmt.Errorf("restore rewind token state: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return llm.ChatMessage{}, 0, 0, err
	}
	return llm.ChatMessage{ID: historyID, Role: role, Content: content, Timestamp: createdAt}, turnIdx, promptTokens, nil
}

// ── Usage / counts ─────────────────────────────────────────────────────────

// GetUserMessageCount counts conversation turns (user rows, display-only
// excluded).
func (s *pgSessionStore) GetUserMessageCount(tenantID int64) (int, error) {
	ctx := context.Background()
	var count int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM session_messages
		WHERE tenant_id = $1 AND role = 'user' AND COALESCE(display_only, false) = false`, tenantID).Scan(&count)
	return count, err
}

// GetLastUserMessageContextTokens returns the context_tokens watermark of the
// newest non-display-only user row (0 when absent) — the rewind token restore.
func (s *pgSessionStore) GetLastUserMessageContextTokens(tenantID int64) (int64, error) {
	ctx := context.Background()
	var tokens int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(context_tokens, 0) FROM session_messages
		WHERE tenant_id = $1 AND role = 'user' AND COALESCE(display_only, false) = false
		ORDER BY id DESC LIMIT 1`, tenantID).Scan(&tokens)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get last user message context_tokens: %w", err)
	}
	return tokens, nil
}

// UpdateMessageContentNonDisplayOnly appends a context_edit control record that
// rewrites the content of the Nth LLM-visible message. Same contract as SQLite:
// the index is validated (out of range is an error, never a silent no-op), and
// only the replayed LLM context changes — the raw rows and the display
// projection keep the original text.
func (s *pgSessionStore) UpdateMessageContentNonDisplayOnly(tenantID int64, messageIndex int, content string) error {
	replay, err := s.Replay(tenantID)
	if err != nil {
		return err
	}
	if messageIndex < 0 || messageIndex >= len(replay.Messages) {
		return fmt.Errorf("no non-display-only message found at index %d for tenant %d", messageIndex, tenantID)
	}
	msg := replay.Messages[messageIndex]
	msg.Content = content
	occurrence := 0
	for i := 0; i < messageIndex; i++ {
		if replay.Messages[i].ID == msg.ID {
			occurrence++
		}
	}
	_, err = s.AppendControl(tenantID, sqlite.HistoryRecordContextEdit, msg.ID,
		sqlite.MessageMutations{Mutations: []sqlite.MessageMutation{{
			TargetHistoryID:  msg.ID,
			TargetOccurrence: occurrence,
			Message:          msg,
		}}})
	return err
}

// GetTenantUsageStats aggregates the session's usage from iteration_history —
// the same source, clamps and ordering as SQLite: main aggregate + per-
// (subscription, model) breakdown (model=” rows excluded from the split but kept
// in the aggregate) + recent iteration rows (recentLimit caps the detail list,
// then the rows are reversed to chronological order). The tenants/tenant_state
// watermark fields are filled by the caller — after the v71 split those live in
// the main DB while this store is bound to the session data.
func (s *pgSessionStore) GetTenantUsageStats(tenantID int64, recentLimit int) (*sqlite.TenantUsageStats, error) {
	ctx := context.Background()
	stats := &sqlite.TenantUsageStats{}
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT turn_id),
		       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(tokens), 0), COALESCE(SUM(cached_tokens), 0),
		       COALESCE(SUM(total_ms), 0),
		       COALESCE(AVG(NULLIF(ttft_ms, 0)), 0), COALESCE(AVG(NULLIF(tpot_ms, 0)), 0), COALESCE(AVG(NULLIF(tokens_per_sec, 0)), 0),
		       COALESCE(MIN(created_at)::text, ''), COALESCE(MAX(created_at)::text, '')
		FROM iteration_history WHERE tenant_id = $1`, tenantID).Scan(
		&stats.IterationCount, &stats.TurnCount,
		&stats.InputTokens, &stats.OutputTokens, &stats.CachedTokens,
		&stats.LLMTotalMs,
		&stats.AvgTTFTMs, &stats.AvgTPOTMs, &stats.AvgTokensPerSec,
		&stats.FirstIterationAt, &stats.LastIterationAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get tenant usage stats: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(subscription_id, ''), COALESCE(model, ''), COUNT(*), COUNT(DISTINCT turn_id),
		       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(tokens), 0), COALESCE(SUM(cached_tokens), 0),
		       COALESCE(AVG(NULLIF(ttft_ms, 0)), 0), COALESCE(AVG(NULLIF(tpot_ms, 0)), 0)
		FROM iteration_history WHERE tenant_id = $1 AND model != ''
		GROUP BY subscription_id, model ORDER BY SUM(input_tokens) + SUM(tokens) DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get tenant usage by model: %w", err)
	}
	for rows.Next() {
		var r sqlite.UsageModelRow
		if err := rows.Scan(&r.SubscriptionID, &r.Model, &r.Iterations, &r.Turns, &r.InputTokens,
			&r.OutputTokens, &r.CachedTokens, &r.AvgTTFTMs, &r.AvgTPOTMs); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan tenant usage by model: %w", err)
		}
		stats.ByModel = append(stats.ByModel, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenant usage by model: %w", err)
	}

	if recentLimit != 0 {
		if recentLimit < 0 || recentLimit > 500 {
			recentLimit = 500
		} else if recentLimit < 1 {
			recentLimit = 20
		}
		rows, err := s.pool.Query(ctx, `
			SELECT turn_id, iteration, input_tokens, tokens, cached_tokens, ttft_ms, tpot_ms, tokens_per_sec, total_ms,
			       COALESCE(model, ''), COALESCE(created_at::text, ''), COALESCE(subscription_id, '')
			FROM iteration_history WHERE tenant_id = $1
			ORDER BY id DESC LIMIT $2`, tenantID, recentLimit)
		if err != nil {
			return nil, fmt.Errorf("get recent iterations: %w", err)
		}
		for rows.Next() {
			var r sqlite.UsageIterationRow
			var turnID int64
			if err := rows.Scan(&turnID, &r.Iteration, &r.InputTokens, &r.OutputTokens, &r.CachedTokens,
				&r.TTFTMs, &r.TPOTMs, &r.TokensPerSec, &r.TotalMs, &r.Model, &r.CreatedAt, &r.SubscriptionID); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan recent iteration: %w", err)
			}
			r.TurnID = uint64(turnID)
			stats.RecentIterations = append(stats.RecentIterations, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate recent iterations: %w", err)
		}
		// Reverse to chronological order (oldest → newest).
		for i, j := 0, len(stats.RecentIterations)-1; i < j; i, j = i+1, j-1 {
			stats.RecentIterations[i], stats.RecentIterations[j] = stats.RecentIterations[j], stats.RecentIterations[i]
		}
	}
	return stats, nil
}

// ── SQL helpers ──

func (s *pgSessionStore) selectMessageSQL() string {
	// COALESCE on the nullable columns: control rows (role='control') and command
	// rows are inserted WITHOUT tool_call_id / tool_name / tool_arguments /
	// tool_calls / detail, so those are NULL in PG. scanMessages scans them into
	// plain strings (same projection as the SQLite reader, which uses
	// sql.NullString) — without COALESCE every read path that can see a control
	// row fails with "cannot scan NULL into *string".
	return `SELECT id, role, content, COALESCE(tool_call_id, ''), COALESCE(tool_name, ''), COALESCE(tool_arguments, ''), COALESCE(tool_calls, ''), COALESCE(detail, ''), reasoning_content, reasoning_items, display_only, internal_only, context_tokens, turn_id, iteration FROM session_messages`
}

func scanMessages(rows pgx.Rows) ([]llm.ChatMessage, error) {
	var result []llm.ChatMessage
	for rows.Next() {
		var msg llm.ChatMessage
		var toolCallsStr, reasoningItemsStr string
		var ctxTokens int
		if err := rows.Scan(&msg.ID, &msg.Role, &msg.Content, &msg.ToolCallID, &msg.ToolName, &msg.ToolArguments, &toolCallsStr, &msg.Detail, &msg.ReasoningContent, &reasoningItemsStr, &msg.DisplayOnly, &msg.Internal, &ctxTokens, &msg.TurnID, &msg.Iteration); err != nil {
			return nil, err
		}
		if toolCallsStr != "" && toolCallsStr != "null" {
			json.Unmarshal([]byte(toolCallsStr), &msg.ToolCalls)
		}
		if reasoningItemsStr != "" && reasoningItemsStr != "null" {
			json.Unmarshal([]byte(reasoningItemsStr), &msg.ReasoningItems)
		}
		result = append(result, msg)
	}
	return result, nil
}

func scanIterations(rows pgx.Rows) ([]sqlite.IterationRecord, error) {
	var result []sqlite.IterationRecord
	for rows.Next() {
		var rec sqlite.IterationRecord
		if err := rows.Scan(&rec.Iteration, &rec.Content, &rec.Reasoning, &rec.Tools, &rec.Tokens, &rec.TTFTMs, &rec.TokensPerSec, &rec.TotalMs, &rec.TPOTMs, &rec.InputTokens, &rec.CachedTokens, &rec.Model, &rec.SubscriptionID); err != nil {
			return nil, err
		}
		result = append(result, rec)
	}
	return result, nil
}

// GetIterationAnchorsByTurn returns (iteration, created_at) anchors for every
// iteration of a turn — the only basis for mapping a synthetic tool pair's
// injection time onto an iteration number. Only those two fields are populated.
func (s *pgSessionStore) GetIterationAnchorsByTurn(tenantID int64, turnID uint64) ([]sqlite.IterationRecord, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT iteration, created_at FROM iteration_history
		 WHERE tenant_id = $1 AND turn_id = $2 ORDER BY iteration ASC`, tenantID, turnID)
	if err != nil {
		return nil, fmt.Errorf("get iteration anchors by turn: %w", err)
	}
	defer rows.Close()
	var anchors []sqlite.IterationRecord
	for rows.Next() {
		var rec sqlite.IterationRecord
		var createdAt time.Time
		if err := rows.Scan(&rec.Iteration, &createdAt); err != nil {
			continue
		}
		rec.CreatedAt = createdAt
		anchors = append(anchors, rec)
	}
	return anchors, rows.Err()
}

// LatestAskControlRecord returns the newest ask_question / ask_answer control
// record, or (0, "", nil) when the session has none — the authority for "is a
// prompt still pending?" after a restart.
func (s *pgSessionStore) LatestAskControlRecord(tenantID int64) (int64, sqlite.HistoryRecordType, error) {
	ctx := context.Background()
	var (
		id         int64
		recordType string
	)
	err := s.pool.QueryRow(ctx, `SELECT id, record_type FROM session_messages
		WHERE tenant_id = $1 AND record_type IN ($2, $3)
		ORDER BY id DESC LIMIT 1`,
		tenantID, string(sqlite.HistoryRecordAskQuestion), string(sqlite.HistoryRecordAskAnswer)).
		Scan(&id, &recordType)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("query latest ask control record: %w", err)
	}
	return id, sqlite.HistoryRecordType(recordType), nil
}

// GetTenantUsageBuckets returns time-bucketed usage for the dashboard. The bucket
// maths mirrors SQLite (epoch seconds shifted into the caller's timezone, floored
// to the bucket width) so both backends produce identical series.
func (s *pgSessionStore) GetTenantUsageBuckets(tenantID int64, bucketSeconds int64, count int, tzOffsetMinutes int) ([]sqlite.UsageBucket, error) {
	switch bucketSeconds {
	case sqlite.UsageBucketMinuteSeconds, sqlite.UsageBucketHourSeconds, sqlite.UsageBucketDaySeconds:
	default:
		return nil, fmt.Errorf("unsupported bucket width %ds (want 60, 3600 or 86400)", bucketSeconds)
	}
	if count < 1 {
		count = 1
	} else if count > 1000 {
		count = 1000
	}
	// Real offsets live in [-12h, +14h]; clamp so a hostile value cannot shift the
	// series off the calendar.
	if tzOffsetMinutes < -14*60 {
		tzOffsetMinutes = -14 * 60
	} else if tzOffsetMinutes > 14*60 {
		tzOffsetMinutes = 14 * 60
	}

	ctx := context.Background()
	shift := int64(tzOffsetMinutes) * 60 // seconds east of UTC
	windowStart := sqlite.FloorEpochBucket(time.Now().Unix(), bucketSeconds, shift) -
		(int64(count)-1)*bucketSeconds

	// floor((epoch + shift) / width) * width - shift, expressed in SQL.
	rows, err := s.pool.Query(ctx, `
		SELECT (floor((EXTRACT(EPOCH FROM created_at) + $1) / $2) * $2 - $1)::bigint AS bucket_start,
		       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(cached_tokens), 0),
		       COALESCE(SUM(tokens), 0), COUNT(*),
		       COALESCE(SUM(ttft_ms), 0), COALESCE(SUM(total_ms), 0)
		FROM iteration_history
		WHERE tenant_id = $3 AND created_at IS NOT NULL
		GROUP BY bucket_start
		HAVING (floor((EXTRACT(EPOCH FROM MIN(created_at)) + $1) / $2) * $2 - $1) >= $4
		ORDER BY bucket_start`, shift, bucketSeconds, tenantID, windowStart)
	if err != nil {
		return nil, fmt.Errorf("get tenant usage buckets: %w", err)
	}
	defer rows.Close()

	buckets := []sqlite.UsageBucket{}
	for rows.Next() {
		var b sqlite.UsageBucket
		if err := rows.Scan(&b.BucketStart, &b.InputTokens, &b.CachedTokens, &b.OutputTokens,
			&b.Calls, &b.TTFTMsSum, &b.TotalMsSum); err != nil {
			return nil, fmt.Errorf("scan usage bucket: %w", err)
		}
		buckets = append(buckets, b)
	}
	return buckets, rows.Err()
}

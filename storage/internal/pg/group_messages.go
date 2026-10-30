package pg

// group_messages.go — PostgreSQL port of storage/sqlite/group_messages.go.
//
// A group message is a normal session_messages row carrying the group envelope
// in dedicated columns (group_id / sender_type / sender_id / sender_name) plus
// the envelope JSON in record_data. The write path is therefore the same as any
// other message, so history replay, preview and the display projection keep
// working untouched.
//
// Backend differences, same semantics: INSERT ... RETURNING id (no
// LastInsertId), created_at is TIMESTAMPTZ (rendered back as the same RFC3339Nano
// string SQLite stores, in local time), keyset paging is unchanged.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"xbot/llm"

	common "xbot/storage/common"
)

// AppendGroupMessage writes one group message (group_id / sender_* columns +
// record_data envelope) and returns the new row id.
func (s *sessionStore) AppendGroupMessage(tenantID int64, msg llm.ChatMessage, env common.GroupMessageEnvelope) (int64, error) {
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	msg.GroupID = env.GroupID
	msg.SenderType = env.SenderType
	msg.SenderID = env.SenderID
	msg.SenderName = env.SenderName
	msg.RecordData = env.JSON()

	toolCalls, _ := json.Marshal(msg.ToolCalls)
	if string(toolCalls) == "null" {
		toolCalls = []byte("[]")
	}
	reasoningItems, _ := json.Marshal(msg.ReasoningItems)
	if string(reasoningItems) == "null" {
		reasoningItems = []byte("[]")
	}

	ctx := context.Background()
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO session_messages
		(tenant_id, role, content, tool_call_id, tool_name, tool_arguments, tool_calls,
		 detail, display_only, internal_only, reasoning_content, reasoning_items,
		 record_type, record_data, group_id, sender_type, sender_id, sender_name,
		 created_at, turn_id, iteration)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
		        'message', $13, $14, $15, $16, $17, $18, $19, $20)
		RETURNING id`,
		tenantID, sanitizeText(msg.Role), sanitizeText(msg.Content), sanitizeText(msg.ToolCallID),
		sanitizeText(msg.ToolName), sanitizeText(msg.ToolArguments), string(toolCalls),
		sanitizeText(msg.Detail), msg.DisplayOnly, msg.Internal, sanitizeText(msg.ReasoningContent),
		string(reasoningItems), sanitizeText(msg.RecordData), sanitizeText(msg.GroupID),
		sanitizeText(msg.SenderType), sanitizeText(msg.SenderID), sanitizeText(msg.SenderName),
		msg.Timestamp, msg.TurnID, msg.Iteration).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert group message: %w", err)
	}
	return id, nil
}

// ListGroupMessages reads the group messages of the current session with keyset
// paging.
//
// Ordering: one page is fetched by id DESC (newest first) and then reversed to
// id ASC for the caller, so next_cursor is always the oldest id of the page and
// the next page continues backwards — "newest on first screen, load history
// upwards", as the timeline expects.
func (s *sessionStore) ListGroupMessages(tenantID int64, groupID string, beforeID int64, limit int) ([]common.GroupMessageRow, bool, int64, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	q := `SELECT id, COALESCE(group_id, ''), COALESCE(sender_type, ''), COALESCE(sender_id, ''),
	             COALESCE(sender_name, ''), role, content, COALESCE(record_data, ''), created_at
	        FROM session_messages
	       WHERE tenant_id = $1 AND group_id != '' AND group_id = $2`
	args := []any{tenantID, sanitizeText(groupID)}
	if beforeID > 0 {
		args = append(args, beforeID)
		q += fmt.Sprintf(` AND id < $%d`, len(args))
	}
	args = append(args, limit+1)
	q += fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, len(args))

	rows, err := s.pool.Query(context.Background(), q, args...)
	if err != nil {
		return nil, false, 0, fmt.Errorf("query group messages: %w", err)
	}
	defer rows.Close()

	out := make([]common.GroupMessageRow, 0, limit)
	for rows.Next() {
		var r common.GroupMessageRow
		var raw string
		var created time.Time
		if err := rows.Scan(&r.ID, &r.GroupID, &r.SenderType, &r.SenderID, &r.SenderName,
			&r.Role, &r.Content, &raw, &created); err != nil {
			return nil, false, 0, err
		}
		r.CreatedAt = created.Local().Format(time.RFC3339Nano)
		r.Kind = common.StructuredKindText
		if raw != "" {
			var env struct {
				Kind          string         `json:"kind"`
				CorrelationID string         `json:"correlation_id"`
				To            []string       `json:"to"`
				Payload       map[string]any `json:"payload"`
			}
			if json.Unmarshal([]byte(raw), &env) == nil {
				if env.Kind != "" {
					r.Kind = env.Kind
				}
				r.CorrelationID = env.CorrelationID
				r.To = env.To
				r.Payload = env.Payload
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, 0, err
	}

	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	nextCursor := int64(0)
	if len(out) > 0 {
		nextCursor = out[len(out)-1].ID
	}
	// DESC -> ASC: render the page in chronological order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, hasMore, nextCursor, nil
}

// GroupMessagesExist reports whether the current session already has messages
// for this group (front-end / seed self-check).
func (s *sessionStore) GroupMessagesExist(tenantID int64, groupID string) (bool, error) {
	var n int64
	if err := s.pool.QueryRow(context.Background(),
		`SELECT COUNT(1) FROM session_messages WHERE tenant_id = $1 AND group_id = $2`,
		tenantID, sanitizeText(groupID)).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

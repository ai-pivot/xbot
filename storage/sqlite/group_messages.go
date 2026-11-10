package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"xbot/llm"
)

// AppendGroupMessage 把一条群消息写入会话库（带 group_id / sender_* 列 +
// record_data 信封），返回消息 id。写入路径与普通消息一致，因此历史、回放、
// preview 等既有逻辑全部照常工作。
func (s *SessionService) AppendGroupMessage(tenantID int64, msg llm.ChatMessage, env GroupMessageEnvelope) (int64, error) {
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	msg.GroupID = env.GroupID
	msg.SenderType = env.SenderType
	msg.SenderID = env.SenderID
	msg.SenderName = env.SenderName
	msg.RecordData = env.JSON()
	lock := s.db.historyLock(tenantID)
	lock.Lock()
	defer lock.Unlock()
	return s.appendMessage(tenantID, msg)
}

// ListGroupMessages 按 group_id + keyset 游标读取当前会话库中的群消息。
//
// 返回顺序：按 id DESC 取一页（最新的在前），再翻转为 id ASC 交给调用方 ——
// 这样 next_cursor 恒为「本页最旧 id」，下一页继续向前翻，符合时间轴
// 「首屏看最新、向上加载历史」的交互。
func (s *SessionService) ListGroupMessages(tenantID int64, groupID string, beforeID int64, limit int) ([]GroupMessageRow, bool, int64, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	conn, err := s.conn()
	if err != nil {
		return nil, false, 0, err
	}
	q := `SELECT id, COALESCE(group_id,''), COALESCE(sender_type,''), COALESCE(sender_id,''),
	             COALESCE(sender_name,''), role, content, COALESCE(record_data,''), created_at
	        FROM session_messages
	       WHERE tenant_id = ? AND group_id != '' AND group_id = ?`
	args := []any{tenantID, groupID}
	if beforeID > 0 {
		q += ` AND id < ?`
		args = append(args, beforeID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := conn.QueryContext(context.Background(), q, args...)
	if err != nil {
		return nil, false, 0, fmt.Errorf("query group messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]GroupMessageRow, 0, limit)
	for rows.Next() {
		var r GroupMessageRow
		var raw string
		var created sql.NullString
		if err := rows.Scan(&r.ID, &r.GroupID, &r.SenderType, &r.SenderID, &r.SenderName,
			&r.Role, &r.Content, &raw, &created); err != nil {
			return nil, false, 0, err
		}
		r.CreatedAt = created.String
		r.Kind = "text"
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
	// DESC → ASC：页面内部按时间正序渲染。
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, hasMore, nextCursor, nil
}

// GroupMessagesExist 返回当前会话库是否已有该群的消息（前端/seed 自检用）。
func (s *SessionService) GroupMessagesExist(tenantID int64, groupID string) (bool, error) {
	conn, err := s.conn()
	if err != nil {
		return false, err
	}
	var n int
	err = conn.QueryRow(`SELECT COUNT(1) FROM session_messages WHERE tenant_id=? AND group_id=?`,
		tenantID, groupID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

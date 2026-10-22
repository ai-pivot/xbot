package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// structured_messages.go — v5 结构化消息存储层（sendmessage-structured 设计 §7）。
//
// 铁律：消息只落会话分库（本文件操作的 session_messages 属于某个会话库）。
// 每条结构化消息就是一行：kind / correlation_id / reply_to / idempotency_key /
// structured_json / direction / answered / cancelled 列 + 既有 role/content/sender_*。
//
// 可见性：record_type 保持 'message'，因此既有历史回放/展示路径天然包含这些行；
// LLM 回放通过 role/content 看到 answer/decision（真回调），ask_user/report 则以
// assistant 文本进入上下文。

const (
	// StructuredKindText 普通文本（等价旧 message）。
	StructuredKindText = "text"
	// StructuredKindAskUser 异步提问卡片。
	StructuredKindAskUser = "ask_user"
	// StructuredKindAnswer 对 ask_user 的回答。
	StructuredKindAnswer = "answer"
	// StructuredKindReport 奏折/汇报。
	StructuredKindReport = "report"
	// StructuredKindDecision 批红（准/驳/改）。
	StructuredKindDecision = "decision"
	// StructuredKindCommand 控制指令。
	StructuredKindCommand = "command"
	// StructuredKindEvent 状态事件。
	StructuredKindEvent = "event"
)

// StructuredMessageKinds 是 kind 白名单（API 校验与文档共用）。
var StructuredMessageKinds = map[string]bool{
	StructuredKindText:     true,
	StructuredKindAskUser:  true,
	StructuredKindAnswer:   true,
	StructuredKindReport:   true,
	StructuredKindDecision: true,
	StructuredKindCommand:  true,
	StructuredKindEvent:    true,
}

// 闭环失败的可判别错误（handler 映射为 HTTP 状态码）。
//
// 命名沿用最早只有 ask_user 时的 ErrAskUser*，但语义已泛化为「目标行」：
// answer 闭环的目标是 ask_user 行，decision 闭环的目标是 report 行，
// 两者共用同一套 CAS（AnswerAskUser / AppendDecision）。
var (
	ErrStructuredNotFound = errors.New("structured message not found")
	ErrAskUserAnswered    = errors.New("target already answered")
	ErrAskUserCancelled   = errors.New("target already cancelled")
	// ErrDecisionTargetNotDecidable：批红目标不是可批对象。
	// 2026-10-22 实测漏洞：reply_to 指向一条 assistant 空行（kind=""）也能"批红成功"，
	// 因为 CAS 只校验 answered/cancelled，从不校验 kind。批红必须落在 report/ask_user 上。
	ErrDecisionTargetNotDecidable = errors.New("decision target is not a report or ask_user")
	ErrIdempotencyConflict        = errors.New("idempotency key conflict")
)

// StructuredMessage 是一条结构化消息行的完整形态。
type StructuredMessage struct {
	ID             int64
	ChatID         string
	Kind           string
	CorrelationID  string
	ReplyTo        string
	IdempotencyKey string
	StructuredJSON string
	Direction      string // in | out
	Answered       bool
	Cancelled      bool

	Role       string
	Content    string
	SenderType string
	SenderID   string
	SenderName string

	CreatedAt time.Time
}

// structuredRole 按 kind 决定行的 role：提问/汇报是 assistant 产出；回答/批红是
// 用户输入；command/event/text 由调用方显式指定（缺省 user）。
func structuredRole(m StructuredMessage) string {
	if m.Role != "" {
		return m.Role
	}
	switch m.Kind {
	case StructuredKindAskUser, StructuredKindReport:
		return "assistant"
	case StructuredKindAnswer, StructuredKindDecision:
		return "user"
	default:
		return "user"
	}
}

// AppendStructuredMessage 幂等写入一条结构化消息。
//
// 幂等语义：同一 (chat_id, idempotency_key) 重复写入返回**已有行 id**，
// created=false，不产生第二行。空 key 不参与幂等（每次都写入）。
func (s *SessionService) AppendStructuredMessage(tenantID int64, m StructuredMessage) (id int64, created bool, err error) {
	if m.Kind == "" {
		m.Kind = StructuredKindText
	}
	if !StructuredMessageKinds[m.Kind] {
		return 0, false, fmt.Errorf("invalid structured message kind %q", m.Kind)
	}
	if m.Direction == "" {
		m.Direction = "in"
	}
	if m.Direction != "in" && m.Direction != "out" {
		return 0, false, fmt.Errorf("invalid direction %q", m.Direction)
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	lock := s.db.historyLock(tenantID)
	lock.Lock()
	defer lock.Unlock()

	err = s.withImmediateHistoryWrite(func(store historyQueryExecer) error {
		if m.IdempotencyKey != "" {
			var existing int64
			qerr := store.QueryRow(`
				SELECT id FROM session_messages
				 WHERE tenant_id = ? AND chat_id = ? AND idempotency_key = ?
				 LIMIT 1`, tenantID, m.ChatID, m.IdempotencyKey).Scan(&existing)
			if qerr == nil {
				id = existing
				created = false
				return nil
			}
			if !errors.Is(qerr, sql.ErrNoRows) {
				return fmt.Errorf("lookup idempotency key: %w", qerr)
			}
		}
		res, ierr := store.Exec(`
			INSERT INTO session_messages
			(tenant_id, role, content, display_only, record_type, created_at, turn_id,
			 chat_id, kind, correlation_id, reply_to, idempotency_key, structured_json,
			 direction, answered, cancelled, sender_type, sender_id, sender_name)
			VALUES (?, ?, ?, 0, 'message', ?, 0,
			        ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			tenantID, structuredRole(m), m.Content, canonicalTimestamp(m.CreatedAt),
			m.ChatID, m.Kind, m.CorrelationID, m.ReplyTo, m.IdempotencyKey, m.StructuredJSON,
			m.Direction, boolInt(m.Answered), boolInt(m.Cancelled),
			m.SenderType, m.SenderID, m.SenderName)
		if ierr != nil {
			// UNIQUE(chat_id, idempotency_key) 并发冲突：回读已有行，保持幂等。
			if m.IdempotencyKey != "" && strings.Contains(strings.ToLower(ierr.Error()), "unique") {
				var existing int64
				if qerr := store.QueryRow(`
					SELECT id FROM session_messages
					 WHERE tenant_id = ? AND chat_id = ? AND idempotency_key = ?
					 LIMIT 1`, tenantID, m.ChatID, m.IdempotencyKey).Scan(&existing); qerr == nil {
					id, created = existing, false
					return nil
				}
			}
			return fmt.Errorf("insert structured message: %w", ierr)
		}
		id, ierr = res.LastInsertId()
		if ierr != nil {
			return fmt.Errorf("read structured message id: %w", ierr)
		}
		created = true
		return nil
	})
	return id, created, err
}

// AppendDecision 原子地写入一条批红（kind=decision）并把目标行置 answered。
//
// 与 AnswerAskUser 共用同一套 CAS 闭环，语义逐条对齐：
//   - 目标行（m.ReplyTo 指向的 message_id）不存在 → ErrStructuredNotFound（HTTP 404）
//   - 目标行已 answered → ErrAskUserAnswered（HTTP 409；**不同 idempotency_key 也必须拒绝**，
//     不能只靠幂等键去重）
//   - 目标行已 cancelled → ErrAskUserCancelled（HTTP 409）
//   - 同一 (chat_id, idempotency_key) 重复提交 → 返回已有行，created=false（HTTP 200 duplicate）
//
// 「先查幂等、再 CAS」的顺序很关键：双击同键要返回原 decision 行（200），
// 而不是因为目标已被自己置 answered 而报 409。
func (s *SessionService) AppendDecision(tenantID int64, m StructuredMessage) (id int64, created bool, err error) {
	if m.Kind == "" {
		m.Kind = StructuredKindDecision
	}
	if m.Kind != StructuredKindDecision {
		return 0, false, fmt.Errorf("invalid decision kind %q", m.Kind)
	}
	targetID, perr := strconv.ParseInt(strings.TrimSpace(m.ReplyTo), 10, 64)
	if perr != nil || targetID <= 0 {
		return 0, false, fmt.Errorf("invalid decision target %q", m.ReplyTo)
	}
	if m.Direction == "" {
		m.Direction = "in"
	}
	if m.Direction != "in" && m.Direction != "out" {
		return 0, false, fmt.Errorf("invalid direction %q", m.Direction)
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	lock := s.db.historyLock(tenantID)
	lock.Lock()
	defer lock.Unlock()

	err = s.withImmediateHistoryWrite(func(store historyQueryExecer) error {
		// 1. 幂等：同 key 重复提交直接返回已有行，不再动目标状态。
		if m.IdempotencyKey != "" {
			var existing int64
			qerr := store.QueryRow(`
				SELECT id FROM session_messages
				 WHERE tenant_id = ? AND chat_id = ? AND idempotency_key = ?
				 LIMIT 1`, tenantID, m.ChatID, m.IdempotencyKey).Scan(&existing)
			if qerr == nil {
				id, created = existing, false
				return nil
			}
			if !errors.Is(qerr, sql.ErrNoRows) {
				return fmt.Errorf("lookup decision idempotency key: %w", qerr)
			}
		}

		// 2. 目标必须存在且仍然开着。
		var answered, cancelledCol int
		var targetKind string
		qerr := store.QueryRow(`
			SELECT COALESCE(answered, 0), COALESCE(cancelled, 0), COALESCE(kind, '')
			  FROM session_messages WHERE tenant_id = ? AND id = ?`,
			tenantID, targetID).Scan(&answered, &cancelledCol, &targetKind)
		if errors.Is(qerr, sql.ErrNoRows) {
			return ErrStructuredNotFound
		}
		if qerr != nil {
			return fmt.Errorf("lookup decision target: %w", qerr)
		}
		if targetKind != StructuredKindReport && targetKind != StructuredKindAskUser {
			return fmt.Errorf("%w (target %d has kind=%q)", ErrDecisionTargetNotDecidable, targetID, targetKind)
		}
		if cancelledCol != 0 {
			return ErrAskUserCancelled
		}
		if answered != 0 {
			return ErrAskUserAnswered
		}

		// 3. CAS：只有未批红未取消才推进（并发批红只有一次成功）。
		res, uerr := store.Exec(`
			UPDATE session_messages SET answered = 1
			 WHERE tenant_id = ? AND id = ? AND answered = 0 AND cancelled = 0
			   AND kind IN (?, ?)`,
			tenantID, targetID, StructuredKindReport, StructuredKindAskUser)
		if uerr != nil {
			return fmt.Errorf("mark decision target answered: %w", uerr)
		}
		affected, rerr := res.RowsAffected()
		if rerr != nil {
			return fmt.Errorf("read decision target update rows: %w", rerr)
		}
		if affected == 0 {
			return ErrAskUserAnswered
		}

		// 4. 写 decision 行（reply_to 指向被批目标，线程可回溯）。
		ires, ierr := store.Exec(`
			INSERT INTO session_messages
			(tenant_id, role, content, display_only, record_type, created_at, turn_id,
			 chat_id, kind, correlation_id, reply_to, idempotency_key, structured_json,
			 direction, answered, cancelled, sender_type, sender_id, sender_name)
			VALUES (?, ?, ?, 0, 'message', ?, 0,
			        ?, 'decision', ?, ?, ?, ?, ?, 0, 0, ?, ?, ?)`,
			tenantID, structuredRole(m), m.Content, canonicalTimestamp(m.CreatedAt),
			m.ChatID, m.CorrelationID, m.ReplyTo, m.IdempotencyKey, m.StructuredJSON,
			m.Direction, m.SenderType, m.SenderID, m.SenderName)
		if ierr != nil {
			// UNIQUE(chat_id, idempotency_key) 并发冲突：回读已有行，保持幂等。
			if m.IdempotencyKey != "" && strings.Contains(strings.ToLower(ierr.Error()), "unique") {
				var existing int64
				if qerr := store.QueryRow(`
					SELECT id FROM session_messages
					 WHERE tenant_id = ? AND chat_id = ? AND idempotency_key = ?
					 LIMIT 1`, tenantID, m.ChatID, m.IdempotencyKey).Scan(&existing); qerr == nil {
					id, created = existing, false
					return nil
				}
			}
			return fmt.Errorf("insert decision: %w", ierr)
		}
		id, ierr = ires.LastInsertId()
		if ierr != nil {
			return fmt.Errorf("read decision id: %w", ierr)
		}
		created = true
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return id, created, nil
}

// AnswerAskUser 原子地把某条 ask_user 标记为已回答/已取消，并写入一行
// kind=answer 的记录。返回 (answerID, questionID, created, error)。
//
// 幂等（双击/重复提交只生效一次）：
//   - 同一 correlation 已有 answer 行：
//     · idempotency_key 相同 → 返回已有 answer（created=false），不重复写；
//     · 不同/为空 → ErrAskUserAnswered。
//   - 已取消 → ErrAskUserCancelled（取消请求本身幂等：同 key 返回已有行）。
func (s *SessionService) AnswerAskUser(tenantID int64, chatID, correlationID, answerText, structuredJSON, idempotencyKey, senderID, senderName string, cancelled bool) (answerID, questionID int64, created bool, err error) {
	if strings.TrimSpace(correlationID) == "" {
		return 0, 0, false, ErrStructuredNotFound
	}
	lock := s.db.historyLock(tenantID)
	lock.Lock()
	defer lock.Unlock()

	err = s.withImmediateHistoryWrite(func(store historyQueryExecer) error {
		// 1. 定位问题行（同一 correlation 可能被重复投递，取最新一条）。
		var answered, cancelledCol int
		var qChat string
		qerr := store.QueryRow(`
			SELECT id, COALESCE(chat_id, ''), COALESCE(answered, 0), COALESCE(cancelled, 0)
			  FROM session_messages
			 WHERE tenant_id = ? AND kind = 'ask_user' AND correlation_id = ?
			 ORDER BY id DESC LIMIT 1`, tenantID, correlationID).
			Scan(&questionID, &qChat, &answered, &cancelledCol)
		if errors.Is(qerr, sql.ErrNoRows) {
			return ErrStructuredNotFound
		}
		if qerr != nil {
			return fmt.Errorf("lookup ask_user: %w", qerr)
		}
		if chatID == "" {
			chatID = qChat
		}

		// 2. 已有 answer 行 → 幂等或拒绝。
		var existingAnswerID int64
		var existingKey string
		var existingCancelled int
		aerr := store.QueryRow(`
			SELECT id, COALESCE(idempotency_key, ''), COALESCE(cancelled, 0) FROM session_messages
			 WHERE tenant_id = ? AND kind = 'answer' AND correlation_id = ?
			 ORDER BY id DESC LIMIT 1`, tenantID, correlationID).
			Scan(&existingAnswerID, &existingKey, &existingCancelled)
		if aerr == nil {
			if idempotencyKey != "" && existingKey == idempotencyKey {
				answerID, created = existingAnswerID, false
				return nil
			}
			if existingCancelled != 0 {
				return ErrAskUserCancelled
			}
			return ErrAskUserAnswered
		}
		if !errors.Is(aerr, sql.ErrNoRows) {
			return fmt.Errorf("lookup answer: %w", aerr)
		}

		// 3. 已取消 → 拒绝回答；取消请求本身幂等（返回原问题行）。
		if cancelledCol != 0 {
			if cancelled {
				answerID, created = questionID, false
				return nil
			}
			return ErrAskUserCancelled
		}

		// 4. CAS：只有未答未取消才推进状态（并发双击只有一次成功）。
		flagCol := "answered"
		if cancelled {
			flagCol = "cancelled"
		}
		res, uerr := store.Exec(fmt.Sprintf(`
			UPDATE session_messages SET %s = 1
			 WHERE tenant_id = ? AND id = ? AND answered = 0 AND cancelled = 0`, flagCol),
			tenantID, questionID)
		if uerr != nil {
			return fmt.Errorf("mark ask_user %s: %w", flagCol, uerr)
		}
		affected, rerr := res.RowsAffected()
		if rerr != nil {
			return fmt.Errorf("read ask_user update rows: %w", rerr)
		}
		if affected == 0 {
			return ErrAskUserAnswered
		}

		// 5. 写 answer 行（已存在则幂等返回）。
		if senderName == "" {
			senderName = senderID
		}
		ares, ierr := store.Exec(`
			INSERT INTO session_messages
			(tenant_id, role, content, display_only, record_type, created_at, turn_id,
			 chat_id, kind, correlation_id, reply_to, idempotency_key, structured_json,
			 direction, answered, cancelled, sender_type, sender_id, sender_name)
			VALUES (?, 'user', ?, 0, 'message', ?, 0,
			        ?, 'answer', ?, ?, ?, ?, 'in', 0, ?, 'user', ?, ?)`,
			tenantID, answerText, canonicalTimestamp(time.Now()),
			chatID, correlationID, fmt.Sprintf("%d", questionID), idempotencyKey, structuredJSON,
			boolInt(cancelled), senderID, senderName)
		if ierr != nil {
			if idempotencyKey != "" && strings.Contains(strings.ToLower(ierr.Error()), "unique") {
				if qerr := store.QueryRow(`
					SELECT id FROM session_messages
					 WHERE tenant_id = ? AND chat_id = ? AND idempotency_key = ? LIMIT 1`,
					tenantID, chatID, idempotencyKey).Scan(&existingAnswerID); qerr == nil {
					answerID, created = existingAnswerID, false
					return nil
				}
			}
			return fmt.Errorf("insert answer: %w", ierr)
		}
		answerID, ierr = ares.LastInsertId()
		if ierr != nil {
			return fmt.Errorf("read answer id: %w", ierr)
		}
		created = true
		return nil
	})
	if err != nil {
		return 0, 0, false, err
	}
	return answerID, questionID, created, nil
}

// MarkAskUserCancelled 取消一条未回答的 ask_user（写 kind=answer, cancelled=1）。
func (s *SessionService) MarkAskUserCancelled(tenantID int64, chatID, correlationID, reason, idempotencyKey, senderID string) (int64, int64, bool, error) {
	payload, _ := json.Marshal(map[string]any{
		"answers":     []any{},
		"cancelled":   true,
		"answered_by": senderID,
		"reason":      reason,
	})
	return s.AnswerAskUser(tenantID, chatID, correlationID, "已取消", string(payload), idempotencyKey, senderID, senderID, true)
}

// ListStructuredMessages 按 kind（空 = 全部结构化消息）做 keyset 分页。
// cursor 是上一页最后一条的 id；返回按 id DESC（最新在前）。
func (s *SessionService) ListStructuredMessages(tenantID int64, kind string, cursor int64, limit int) ([]StructuredMessage, bool, int64, error) {
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
	q := `SELECT id, COALESCE(chat_id,''), COALESCE(kind,''), COALESCE(correlation_id,''),
	             COALESCE(reply_to,''), COALESCE(idempotency_key,''), COALESCE(structured_json,''),
	             COALESCE(direction,''), COALESCE(answered,0), COALESCE(cancelled,0),
	             role, content, COALESCE(sender_type,''), COALESCE(sender_id,''),
	             COALESCE(sender_name,''), created_at
	        FROM session_messages
	       WHERE tenant_id = ? AND kind <> ''`
	args := []any{tenantID}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	if cursor > 0 {
		q += ` AND id < ?`
		args = append(args, cursor)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := conn.QueryContext(context.Background(), q, args...)
	if err != nil {
		return nil, false, 0, fmt.Errorf("query structured messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]StructuredMessage, 0, limit)
	for rows.Next() {
		m, err := scanStructuredRow(rows)
		if err != nil {
			return nil, false, 0, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, 0, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	next := int64(0)
	if len(out) > 0 {
		next = out[len(out)-1].ID
	}
	return out, hasMore, next, nil
}

// ListStructuredMessagesAfter 返回 id > afterID 的结构化消息，按 id 升序。
//
// 供 SSE 断线重连做增量补齐：客户端带上已加载的最大 id，精确拉取缺口。
// 「只拉最新一页」在深历史下会丢掉中间的 answer/decision 行，而派生状态
// （answered 由消息流投影得出）依赖这些行 —— 丢一条 decision 就会让已批红
// 的卡片在重连后回退成「待批红」，破坏跨端一致性。所以这里按 id 单调补齐。
func (s *SessionService) ListStructuredMessagesAfter(tenantID int64, kind string, afterID int64, limit int) ([]StructuredMessage, bool, int64, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 200 {
		limit = 200
	}
	conn, err := s.conn()
	if err != nil {
		return nil, false, 0, err
	}
	q := `SELECT id, COALESCE(chat_id,''), COALESCE(kind,''), COALESCE(correlation_id,''),
	             COALESCE(reply_to,''), COALESCE(idempotency_key,''), COALESCE(structured_json,''),
	             COALESCE(direction,''), COALESCE(answered,0), COALESCE(cancelled,0),
	             role, content, COALESCE(sender_type,''), COALESCE(sender_id,''),
	             COALESCE(sender_name,''), created_at
	        FROM session_messages
	       WHERE tenant_id = ? AND kind <> '' AND id > ?`
	args := []any{tenantID, afterID}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY id ASC LIMIT ?`
	args = append(args, limit+1)

	rows, err := conn.QueryContext(context.Background(), q, args...)
	if err != nil {
		return nil, false, 0, fmt.Errorf("query structured messages after %d: %w", afterID, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]StructuredMessage, 0, limit)
	for rows.Next() {
		m, err := scanStructuredRow(rows)
		if err != nil {
			return nil, false, 0, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, 0, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	next := afterID
	if len(out) > 0 {
		next = out[len(out)-1].ID
	}
	return out, hasMore, next, nil
}

// ListInbox 返回当前会话未回答、未取消的 ask_user（收件箱）。
// 按 id DESC（最新在前），cursor = 上一页最后一条 id。
func (s *SessionService) ListInbox(tenantID int64, cursor int64, limit int) ([]StructuredMessage, bool, int64, error) {
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
	q := `SELECT id, COALESCE(chat_id,''), COALESCE(kind,''), COALESCE(correlation_id,''),
	             COALESCE(reply_to,''), COALESCE(idempotency_key,''), COALESCE(structured_json,''),
	             COALESCE(direction,''), COALESCE(answered,0), COALESCE(cancelled,0),
	             role, content, COALESCE(sender_type,''), COALESCE(sender_id,''),
	             COALESCE(sender_name,''), created_at
	        FROM session_messages
	       WHERE tenant_id = ? AND kind = 'ask_user' AND answered = 0 AND cancelled = 0`
	args := []any{tenantID}
	if cursor > 0 {
		q += ` AND id < ?`
		args = append(args, cursor)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := conn.QueryContext(context.Background(), q, args...)
	if err != nil {
		return nil, false, 0, fmt.Errorf("query inbox: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]StructuredMessage, 0, limit)
	for rows.Next() {
		m, err := scanStructuredRow(rows)
		if err != nil {
			return nil, false, 0, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, 0, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	next := int64(0)
	if len(out) > 0 {
		next = out[len(out)-1].ID
	}
	return out, hasMore, next, nil
}

// CountInbox 返回未回答 ask_user 数量（未读徽标）。
func (s *SessionService) CountInbox(tenantID int64) (int, error) {
	conn, err := s.conn()
	if err != nil {
		return 0, err
	}
	var n int
	if err := conn.QueryRow(`
		SELECT COUNT(1) FROM session_messages
		 WHERE tenant_id = ? AND kind = 'ask_user' AND answered = 0 AND cancelled = 0`,
		tenantID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count inbox: %w", err)
	}
	return n, nil
}

// GetStructuredMessageByID 读取一行结构化消息（测试/线程用）。
func (s *SessionService) GetStructuredMessageByID(tenantID, id int64) (*StructuredMessage, error) {
	conn, err := s.conn()
	if err != nil {
		return nil, err
	}
	row := conn.QueryRow(`
		SELECT id, COALESCE(chat_id,''), COALESCE(kind,''), COALESCE(correlation_id,''),
		       COALESCE(reply_to,''), COALESCE(idempotency_key,''), COALESCE(structured_json,''),
		       COALESCE(direction,''), COALESCE(answered,0), COALESCE(cancelled,0),
		       role, content, COALESCE(sender_type,''), COALESCE(sender_id,''),
		       COALESCE(sender_name,''), created_at
		  FROM session_messages WHERE tenant_id = ? AND id = ?`, tenantID, id)
	m, err := scanStructuredRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrStructuredNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// structuredScanner 同时被 *sql.Rows 与 *sql.Row 满足。
type structuredScanner interface {
	Scan(dest ...any) error
}

func scanStructuredRow(sc structuredScanner) (StructuredMessage, error) {
	var m StructuredMessage
	var answered, cancelled int
	var created string
	if err := sc.Scan(&m.ID, &m.ChatID, &m.Kind, &m.CorrelationID, &m.ReplyTo, &m.IdempotencyKey,
		&m.StructuredJSON, &m.Direction, &answered, &cancelled,
		&m.Role, &m.Content, &m.SenderType, &m.SenderID, &m.SenderName, &created); err != nil {
		return StructuredMessage{}, err
	}
	m.Answered = answered != 0
	m.Cancelled = cancelled != 0
	m.CreatedAt = parseStructuredTimestamp(created)
	return m, nil
}

// parseStructuredTimestamp 宽松解析 SQLite 时间列（与 internal.ParseTimestamp 同口径，
// 但避免跨包依赖造成循环；失败时返回零值，由调用方回落到当前时间）。
func parseStructuredTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02T15:04:05Z",
	} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts
		}
	}
	return time.Time{}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

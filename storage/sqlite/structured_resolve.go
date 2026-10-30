package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// structured_resolve.go — 用户看板卡片的"用户处理"闭环（to="user" 路由的落地端）。
//
// 存储复用 session_messages（结构化行），**不新造第二套存储**：
//   - 卡片本身就是一行 kind=report/ask_user 的结构化消息，落在用户可见的根会话里；
//   - 用户处理后：CAS 把该行置 answered（approve/reject）或 cancelled（dismiss），
//     再写一行 kind=decision（准/驳）或 kind=answer（忽略）作为处理记录。
//
// 幂等是硬要求（同一张卡重复点、刷新后重点都不得重复回流）：
//   1. 同一 (chat_id, idempotency_key) 重复提交 → 返回已有行，Created=false；
//   2. 目标行已 answered/cancelled 且换了新 idempotency_key → 409 语义错误；
//   3. CAS 的 UPDATE ... WHERE answered=0 AND cancelled=0 是最终仲裁，
//      并发双点只有一次 RowsAffected()==1。
// 调用方只在 Created==true 时投递回流（见 serverapp 的 resolve 回调）。

// 用户看板卡片支持的四个动作（与派发单 §2 的 approve/reject/answer/dismiss 对齐）。
const (
	NoticeActionApprove = "approve"
	NoticeActionReject  = "reject"
	NoticeActionAnswer  = "answer"
	NoticeActionDismiss = "dismiss"
)

// NoticeStatusOfAction 把动作映射成卡片状态（approved / rejected / answered / dismissed）。
func NoticeStatusOfAction(action string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case NoticeActionApprove:
		return "approved", true
	case NoticeActionReject:
		return "rejected", true
	case NoticeActionAnswer:
		return "answered", true
	case NoticeActionDismiss:
		return "dismissed", true
	default:
		return "", false
	}
}

// NoticeResolution 是一次"用户处理卡片"的结果。
type NoticeResolution struct {
	// WrittenID 是写入的 decision/answer 行 id（重复提交时指向首次那一行）。
	WrittenID int64
	// Created 为 false 表示幂等命中：状态早已 resolved，**不得再次回流**。
	Created bool
	// Written 是写入行的完整形态（用于广播）。
	Written *StructuredMessage
	// Target 是被处理卡片行的完整形态（CAS 之后的状态）。
	Target *StructuredMessage
}

// ResolveNotice 原子地把一张用户看板卡片推进到 resolved，并留下处理记录行。
//
// 返回可判别的错误（与既有闭环一致）：目标不存在 → ErrStructuredNotFound；
// 已处理 → ErrAskUserAnswered / ErrAskUserCancelled。
func (s *SessionService) ResolveNotice(tenantID int64, chatID string, targetID int64, action, content, resolutionJSON, idempotencyKey, senderID, senderName string) (*NoticeResolution, error) {
	status, ok := NoticeStatusOfAction(action)
	if !ok {
		return nil, fmt.Errorf("invalid notice action %q (want approve|reject|answer|dismiss)", action)
	}
	if targetID <= 0 {
		return nil, fmt.Errorf("invalid notice message_id %d", targetID)
	}
	if senderName == "" {
		senderName = senderID
	}
	kind := StructuredKindDecision
	if action == NoticeActionDismiss || action == NoticeActionAnswer {
		kind = StructuredKindAnswer
	}
	if strings.TrimSpace(content) == "" {
		content = status
	}

	lock := s.db.historyLock(tenantID)
	lock.Lock()
	defer lock.Unlock()

	var writtenID int64
	var created bool
	err := s.withImmediateHistoryWrite(func(store historyQueryExecer) error {
		// 1) 幂等：同 key 重复提交直接返回已有行，不再动目标状态。
		if idempotencyKey != "" {
			var existing int64
			qerr := store.QueryRow(`
				SELECT id FROM session_messages
				 WHERE tenant_id = ? AND chat_id = ? AND idempotency_key = ?
				 LIMIT 1`, tenantID, chatID, idempotencyKey).Scan(&existing)
			if qerr == nil {
				writtenID, created = existing, false
				return nil
			}
			if !errors.Is(qerr, sql.ErrNoRows) {
				return fmt.Errorf("lookup notice idempotency key: %w", qerr)
			}
		}

		// 2) 目标必须存在、是本会话的看板项、且仍然开着。
		var answered, cancelled int
		var targetKind, targetChat string
		qerr := store.QueryRow(`
			SELECT COALESCE(answered, 0), COALESCE(cancelled, 0), COALESCE(kind, ''), COALESCE(chat_id, '')
			  FROM session_messages WHERE tenant_id = ? AND id = ?`,
			tenantID, targetID).Scan(&answered, &cancelled, &targetKind, &targetChat)
		if errors.Is(qerr, sql.ErrNoRows) {
			return ErrStructuredNotFound
		}
		if qerr != nil {
			return fmt.Errorf("lookup notice target: %w", qerr)
		}
		if targetKind != StructuredKindReport && targetKind != StructuredKindAskUser {
			return fmt.Errorf("%w (target %d has kind=%q)", ErrDecisionTargetNotDecidable, targetID, targetKind)
		}
		// 跨会话保护：id 只在会话库内自增，必须与本次操作的会话一致。
		if targetChat != "" && chatID != "" && targetChat != chatID {
			return ErrStructuredNotFound
		}
		if cancelled != 0 {
			return ErrAskUserCancelled
		}
		if answered != 0 {
			return ErrAskUserAnswered
		}

		// 3) CAS：只有未处理才推进（并发双击只有一次成功）。
		flagCol := "answered"
		if action == NoticeActionDismiss {
			flagCol = "cancelled"
		}
		res, uerr := store.Exec(fmt.Sprintf(`
			UPDATE session_messages SET %s = 1
			 WHERE tenant_id = ? AND id = ? AND answered = 0 AND cancelled = 0
			   AND kind IN (?, ?)`, flagCol),
			tenantID, targetID, StructuredKindReport, StructuredKindAskUser)
		if uerr != nil {
			return fmt.Errorf("mark notice %s: %w", flagCol, uerr)
		}
		affected, rerr := res.RowsAffected()
		if rerr != nil {
			return fmt.Errorf("read notice update rows: %w", rerr)
		}
		if affected == 0 {
			return ErrAskUserAnswered
		}

		// 4) 写处理记录行（reply_to 指回卡片，线程可回溯）。
		ires, ierr := store.Exec(`
			INSERT INTO session_messages
			(tenant_id, role, content, display_only, record_type, created_at, turn_id,
			 chat_id, kind, correlation_id, reply_to, idempotency_key, structured_json,
			 direction, answered, cancelled, sender_type, sender_id, sender_name)
			VALUES (?, 'user', ?, 0, 'message', ?, 0,
			        ?, ?, '', ?, ?, ?, 'in', 0, ?, 'user', ?, ?)`,
			tenantID, content, canonicalTimestamp(time.Now()),
			chatID, kind, strconv.FormatInt(targetID, 10), idempotencyKey, resolutionJSON,
			boolInt(action == NoticeActionDismiss), senderID, senderName)
		if ierr != nil {
			// UNIQUE(chat_id, idempotency_key) 并发冲突：回读已有行，保持幂等。
			if idempotencyKey != "" && strings.Contains(strings.ToLower(ierr.Error()), "unique") {
				var existing int64
				if qerr := store.QueryRow(`
					SELECT id FROM session_messages
					 WHERE tenant_id = ? AND chat_id = ? AND idempotency_key = ?
					 LIMIT 1`, tenantID, chatID, idempotencyKey).Scan(&existing); qerr == nil {
					writtenID, created = existing, false
					return nil
				}
			}
			return fmt.Errorf("insert notice resolution: %w", ierr)
		}
		writtenID, ierr = ires.LastInsertId()
		if ierr != nil {
			return fmt.Errorf("read notice resolution id: %w", ierr)
		}
		created = true
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := &NoticeResolution{WrittenID: writtenID, Created: created}
	if written, gerr := s.GetStructuredMessageByID(tenantID, writtenID); gerr == nil {
		out.Written = written
	}
	if target, gerr := s.GetStructuredMessageByID(tenantID, targetID); gerr == nil {
		out.Target = target
	}
	return out, nil
}

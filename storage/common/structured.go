package common

import (
	"errors"
	"strings"
	"time"
)

// ─── Structured messages (report / ask_user / answer / decision / inbox) ───

const (
	StructuredKindText     = "text"
	StructuredKindAskUser  = "ask_user"
	StructuredKindAnswer   = "answer"
	StructuredKindReport   = "report"
	StructuredKindDecision = "decision"
	StructuredKindCommand  = "command"
	StructuredKindEvent    = "event"
)

// StructuredMessageKinds is the kind whitelist (API validation + docs).
var StructuredMessageKinds = map[string]bool{
	StructuredKindText:     true,
	StructuredKindAskUser:  true,
	StructuredKindAnswer:   true,
	StructuredKindReport:   true,
	StructuredKindDecision: true,
	StructuredKindCommand:  true,
	StructuredKindEvent:    true,
}

// Closed-loop domain errors (handlers map them to HTTP status codes).
var (
	ErrStructuredNotFound         = errors.New("structured message not found")
	ErrAskUserAnswered            = errors.New("target already answered")
	ErrAskUserCancelled           = errors.New("target already cancelled")
	ErrDecisionTargetNotDecidable = errors.New("decision target is not a report or ask_user")
	ErrDecisionSelfApproval       = errors.New("a department must not approve its own report")
	ErrIdempotencyConflict        = errors.New("idempotency key conflict")
)

// StructuredMessage is the full shape of one structured message row.
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

	// RecordType 决定这行属于"消息"还是"控制记录"（见 HistoryRecordType）。
	// 空串 = 'message'（默认，参与历史回放）；'notice' = 用户看板卡片（出流，
	// 两套回放都跳过）。
	RecordType HistoryRecordType

	CreatedAt time.Time
}

// StructuredRole decides the row role by kind: ask/report are assistant
// output; answer/decision are user input; the rest default to user unless the
// caller sets Role explicitly.
func StructuredRole(m StructuredMessage) string {
	if m.Role != "" {
		return m.Role
	}
	switch m.Kind {
	case StructuredKindAskUser, StructuredKindReport:
		return "assistant"
	default:
		return "user"
	}
}

// ─── 用户看板卡片（to="user"）的用户处理闭环 ───
//
// 这些定义放在 common 而不是 sqlite：handler 层（serverapp）不该为了一个纯函数
// 或结果 DTO 去 import 具体后端（#463 之后 serverapp 禁止直连 storage/sqlite）。

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

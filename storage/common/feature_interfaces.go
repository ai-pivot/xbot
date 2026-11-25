package common

import (
	"context"
	"time"

	"xbot/llm"
)

// StructuredStore is the session-scoped structured-message capability
// (report / ask_user / answer / decision / inbox). It is embedded into
// SessionStore so callers never have to type-assert a backend implementation.
type StructuredStore interface {
	AppendStructuredMessage(tenantID int64, m StructuredMessage) (id int64, created bool, err error)
	AppendDecision(tenantID int64, m StructuredMessage) (id int64, created bool, err error)
	AnswerAskUser(tenantID int64, chatID, correlationID, answerText, structuredJSON, idempotencyKey, senderID, senderName string, cancelled bool) (answerID, questionID int64, created bool, err error)
	MarkAskUserCancelled(tenantID int64, chatID, correlationID, reason, idempotencyKey, senderID string) (int64, int64, bool, error)
	ListStructuredMessages(tenantID int64, kind string, cursor int64, limit int) ([]StructuredMessage, bool, int64, error)
	ListStructuredMessagesAfter(tenantID int64, kind string, afterID int64, limit int) ([]StructuredMessage, bool, int64, error)
	ListInbox(tenantID int64, cursor int64, limit int) ([]StructuredMessage, bool, int64, error)
	CountInbox(tenantID int64) (int, error)
	GetStructuredMessageByID(tenantID, id int64) (*StructuredMessage, error)

	// GetAskUserByCorrelationID 按「回答钥匙」精确定位提问行（与 AnswerAskUser
	// 的定位判据逐字一致：kind='ask_user' AND correlation_id，多行取最新）。
	//
	// 为什么必须有它（不要再扫 ListStructuredMessages(…,200)）：
	//   - 交互回执要把**卡片原文/问题/选项**写进正文，答/取消两条路径都需要提问行；
	//   - 旧实现用「最近 200 条」窗口近似定位，超窗即静默丢上下文，实测把
	//     option id（`smoke:yes`）当正文写进历史（2026-11-11 用户反馈）。
	// 未命中返回 ErrStructuredNotFound（与 GetStructuredMessageByID 同语义）。
	GetAskUserByCorrelationID(tenantID int64, correlationID string) (*StructuredMessage, error)

	// DeleteStructuredMessages 按 id 列表删除本 tenant 的结构化消息行，
	// 返回实际删除行数。用于收件箱的「单条删除 / 一键删除」。
	//
	// 只删 kind 非空的结构化行（ask_user/report/decision/answer/event/command），
	// 普通聊天消息不可经此路径删除 —— 避免把清收件箱做成"删聊天记录"。
	DeleteStructuredMessages(tenantID int64, ids []int64) (int, error)
}

// GroupMessageStore is the session-scoped group-message capability.
type GroupMessageStore interface {
	AppendGroupMessage(tenantID int64, msg llm.ChatMessage, env GroupMessageEnvelope) (int64, error)
	ListGroupMessages(tenantID int64, groupID string, beforeID int64, limit int) ([]GroupMessageRow, bool, int64, error)
	GroupMessagesExist(tenantID int64, groupID string) (bool, error)
}

// GroupStore manages group metadata (groups / group_members).
type GroupStore interface {
	CreateGroup(ctx context.Context, id, name, owner string, members []string) (*GroupRow, error)
	AddMember(ctx context.Context, groupID, addr string) (*GroupMemberRow, error)
	GetGroup(ctx context.Context, id string) (*GroupRow, error)
	ListGroupsFor(ctx context.Context, addr string) ([]GroupRow, error)
	ListMembers(ctx context.Context, groupID string) ([]GroupMemberRow, error)
	IsAliveMember(ctx context.Context, groupID, addr string) (bool, error)
}

// ActivityStore is the cross-session activity feed index.
type ActivityStore interface {
	Insert(rec ActivityItem) error
	List(limit int, within time.Duration, kinds []string) ([]ActivityItem, bool, error)
	Count() (int64, error)
}

// RunnerStore is the managed-runner registry (the old tools.RunnerStore SQL).
type RunnerStore interface {
	Create(name, mode, dockerImage, workspace string, llm RunnerLLMSettings) (string, error)
	RotateToken(name string) (string, error)
	Token(name string) (string, error)
	List() ([]RunnerInfo, error)
	Get(name string) (*RunnerInfo, error)
	Delete(name string) error
	Rename(oldName, newName string) error
	UpdateLLM(name string, llm RunnerLLMSettings) error
	FindByToken(token string) (string, bool)
	Validate(token string) bool
}

package main

// 业务事件解析：把网关 op=0 的 d 报文映射成渠道内部的 Inbound。
//
// 官方事件（docs: 开放平台 → 开发 → 事件订阅）：
//
//	C2C_MESSAGE_CREATE       私聊消息
//	GROUP_AT_MESSAGE_CREATE  群里 @机器人 的消息
//	GROUP_MESSAGE_CREATE     群全量消息（需在后台开通私域权限）
//
// 身份字段**只有 openid**：
//
//	私聊：d.author.user_openid
//	群聊：d.author.member_openid + d.group_openid
//
// d.author.id 是平台内部 id，不是 openid，也不是 QQ 号，**禁止**拿它做身份
// 或白名单判断（换 app 就变，且与用户 QQ 号无对应关系）。

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 支持的官方事件类型。
const (
	EventC2CMessageCreate     = "C2C_MESSAGE_CREATE"
	EventGroupAtMessageCreate = "GROUP_AT_MESSAGE_CREATE"
	EventGroupMessageCreate   = "GROUP_MESSAGE_CREATE"
)

// Inbound 是一条解析完成的官方入站消息。
type Inbound struct {
	// EventType 是原始事件名（C2C_... / GROUP_AT_...）。
	EventType string
	// MessageID 是 d.id，被动回复时作为 msg_id 回传。
	MessageID string
	// Content 是消息正文（已去掉首尾空白；群 @ 消息带的前导空格会被清掉）。
	Content string
	// SenderOpenID 是发送者 openid（私聊 user_openid / 群聊 member_openid）。
	SenderOpenID string
	// SenderName 是展示名。官方 C2C/群事件不给昵称，通常为空。
	SenderName string
	// GroupOpenID 是群 openid，仅群聊非空。
	GroupOpenID string
	// Timestamp 是消息时间（解析失败为零值）。
	Timestamp time.Time
	// Raw 保留原文，便于排查平台字段变化。
	Raw json.RawMessage
}

// IsGroup 报告是否群聊消息。
func (m Inbound) IsGroup() bool { return m.GroupOpenID != "" }

// ChatID 返回 host 会话标识：私聊 = 发送者 openid，群聊 = "group:<群 openid>"。
func (m Inbound) ChatID() string {
	if m.IsGroup() {
		return GroupChatID(m.GroupOpenID)
	}
	return PrivateChatID(m.SenderOpenID)
}

// Identity 转成身份注入用的 Identity。
func (m Inbound) Identity() Identity {
	return Identity{
		SenderID:   m.SenderOpenID,
		SenderName: m.SenderName,
		GroupID:    m.GroupOpenID,
	}.Normalize()
}

// Valid 报告这条消息是否齐全到可以进 agent。
func (m Inbound) Valid() bool {
	return m.MessageID != "" && m.SenderOpenID != "" && strings.TrimSpace(m.Content) != ""
}

// eventEnvelope 是业务事件的 d 结构（官方字段名）。
type eventEnvelope struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	GroupOpenID string `json:"group_openid"`
	Timestamp   string `json:"timestamp"`
	Author      struct {
		ID           string `json:"id"`
		UserOpenID   string `json:"user_openid"`
		MemberOpenID string `json:"member_openid"`
		UnionOpenID  string `json:"union_openid"`
	} `json:"author"`
}

// SupportedEvent 报告该事件是否由本渠道处理。
func SupportedEvent(eventType string) bool {
	switch eventType {
	case EventC2CMessageCreate, EventGroupAtMessageCreate, EventGroupMessageCreate:
		return true
	default:
		return false
	}
}

// ParseInbound 解析一条事件报文。
//
// 返回 ok=false 表示"不是本渠道关心的事件"（心跳之外的其它 dispatch，
// 例如 GUILD_CREATE），调用方应静默跳过；返回 error 表示事件类型对但报文
// 缺关键字段 —— 这种必须记日志，不能静默丢（否则线上表现为"机器人不理人"）。
func ParseInbound(eventType string, data json.RawMessage) (Inbound, bool, error) {
	if !SupportedEvent(eventType) {
		return Inbound{}, false, nil
	}
	var d eventEnvelope
	if err := json.Unmarshal(data, &d); err != nil {
		return Inbound{}, true, fmt.Errorf("parse %s payload: %w", eventType, err)
	}

	in := Inbound{
		EventType:   eventType,
		MessageID:   d.ID,
		Content:     strings.TrimSpace(d.Content),
		SenderName:  "",
		GroupOpenID: d.GroupOpenID,
		Raw:         data,
	}
	if ts, err := time.Parse(time.RFC3339, d.Timestamp); err == nil {
		in.Timestamp = ts
	}

	// ── 身份：只认 openid ──
	switch eventType {
	case EventC2CMessageCreate:
		in.SenderOpenID = d.Author.UserOpenID
	case EventGroupAtMessageCreate, EventGroupMessageCreate:
		in.SenderOpenID = d.Author.MemberOpenID
	}
	if in.SenderOpenID == "" {
		return in, true, fmt.Errorf("%s payload has no sender openid "+
			"(user_openid/member_openid); author.id=%q must NOT be used as identity", eventType, d.Author.ID)
	}
	// 群事件必须带 group_openid；私聊事件不该带。
	isGroupEvent := eventType != EventC2CMessageCreate
	if isGroupEvent && in.GroupOpenID == "" {
		return in, true, fmt.Errorf("%s payload has no group_openid", eventType)
	}
	if !isGroupEvent && in.GroupOpenID != "" {
		return in, true, fmt.Errorf("%s payload unexpectedly carries group_openid=%q", eventType, in.GroupOpenID)
	}
	if in.MessageID == "" {
		return in, true, fmt.Errorf("%s payload has no message id (d.id required for passive reply)", eventType)
	}
	return in, true, nil
}

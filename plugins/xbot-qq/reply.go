package main

// 约束 4（御前令）：引用（回复）消息。
//
// 官方字段：
//   - 事件体 message_type == 103 表示引用消息
//   - msg_elements []MsgElement {msg_idx, author{...}, message_type, content, attachments}
//     （autogen/event/c2c_message_create.html 的 MsgElement 表）
//   - message_scene.ext 是 **[]string 的 "k=v" 串**（含 msg_idx=、ref_msg_idx=、auth_token=），
//     **不是 map** —— 解析见 dedup.go#parseSceneExt
//
// 发送侧（可选能力）：message_reference.message_id。本轮不实现发送侧引用，
// 只做接收侧解析（验收要求的是"注入含 A 原话"）。
//
// **只取一层**：msg_elements 里就是被引用消息的快照，元素内部不会再嵌
// msg_elements；即便文本里伪造了第二层"引用"，也只当普通文本注入，
// 不做任何递归解析。

import (
	"fmt"
	"strconv"
	"strings"
)

// MsgElement 是官方 msg_elements[] 的一个元素（被引用消息的快照）。
// flexInt 兼容"数字或字符串"的字段。
//
// 2026-10-27 真机故障：带 reply（引用）的群消息里，`msg_elements[].msg_idx`
// 是**字符串**（如 "REF-123"），而我们声明成 int ⇒ 整条事件解析失败：
//
//	json: cannot unmarshal string into Go struct field MsgElement.msg_elements.msg_idx of type int
//
// 结果：所有引用消息被整类丢弃（表现为"带 reply 的消息无法处理"）。
// flexInt 同时接受 123 / "123" / 非数字串（后者记 0，不影响其它字段解析）。
type flexInt int

// UnmarshalJSON 实现宽松解析：数字、引号数字、其它字符串都不报错。
func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if s[0] == '"' {
		s = strings.Trim(s, "\"")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		*f = 0 // 非数字串（如引用消息的 msg_idx）按 0 处理，不能让整条消息失败
		return nil
	}
	*f = flexInt(n)
	return nil
}

// MarshalJSON 序列化回数字（便于测试断言与日志）。
func (f flexInt) MarshalJSON() ([]byte, error) { return []byte(strconv.Itoa(int(f))), nil }

type MsgElement struct {
	MsgIdx      flexInt             `json:"msg_idx"`
	MessageType int                 `json:"message_type"`
	Content     string              `json:"content"`
	Attachments []MessageAttachment `json:"attachments"`
	Author      struct {
		ID           string `json:"id"`
		UserOpenID   string `json:"user_openid"`
		MemberOpenID string `json:"member_openid"`
		UnionOpenID  string `json:"union_openid"`
		Username     string `json:"username"`
	} `json:"author"`
}

// AuthorID 返回元素的发送者标识（私聊 user_openid / 群聊 member_openid）。
func (e MsgElement) AuthorID() string {
	if e.Author.MemberOpenID != "" {
		return e.Author.MemberOpenID
	}
	return e.Author.UserOpenID
}

// QuotedMessage 是被引用消息的解析结果（只一层）。
type QuotedMessage struct {
	// MsgIdx 是被引用消息的序号（来自 msg_elements[].msg_idx，缺失为 0）。
	MsgIdx int
	// AuthorOpenID 是被引用消息的发送者。
	AuthorOpenID string
	// Content 是原话。
	Content string
	// Attachments 是被引用消息的附件。
	Attachments []MessageAttachment
}

// isQuoteMessage 报告该事件是否是引用消息（官方 message_type == 103）。
func isQuoteMessage(messageType int, elements []MsgElement) bool {
	return messageType == 103 || len(elements) > 0
}

// pickQuoted 从 msg_elements 里挑出被引用的那一条。
//
// 优先按 message_scene.ext 的 ref_msg_idx 精确匹配；匹配不到时退回
// **第一个有内容的元素**（平台只给一个元素时就是它）。只挑一条，不递归。
func pickQuoted(elements []MsgElement, sceneExt map[string]string) (QuotedMessage, bool) {
	if len(elements) == 0 {
		return QuotedMessage{}, false
	}
	if refIdx := sceneExtInt(sceneExt, "ref_msg_idx"); refIdx != 0 {
		for _, e := range elements {
			if int(e.MsgIdx) == refIdx {
				return toQuoted(e), true
			}
		}
	}
	for _, e := range elements {
		if strings.TrimSpace(e.Content) != "" || len(e.Attachments) > 0 {
			return toQuoted(e), true
		}
	}
	// 全部元素都空：仍然把第一条当引用（至少保留作者/序号，便于排查）。
	return toQuoted(elements[0]), true
}

func toQuoted(e MsgElement) QuotedMessage {
	return QuotedMessage{
		MsgIdx:       int(e.MsgIdx),
		AuthorOpenID: e.AuthorID(),
		Content:      strings.TrimSpace(e.Content),
		Attachments:  e.Attachments,
	}
}

// renderQuoteBlock 生成注入到上下文里的引用块。
//
// 形状与渠道注入的其他结构标签一致（属性用双引号、内容按 XML 转义），
// 让模型能明确区分"用户这轮说的话"和"他引用的旧消息"。
func renderQuoteBlock(q QuotedMessage) string {
	if q.Content == "" && len(q.Attachments) == 0 {
		return ""
	}
	attrs := make([]string, 0, 2)
	if q.MsgIdx != 0 {
		attrs = append(attrs, fmt.Sprintf(`msg_idx="%d"`, q.MsgIdx))
	}
	if q.AuthorOpenID != "" {
		attrs = append(attrs, fmt.Sprintf(`author="%s"`, xmlEscape(q.AuthorOpenID)))
	}
	open := "<reply"
	if len(attrs) > 0 {
		open += " " + strings.Join(attrs, " ")
	}
	open += ">"
	body := xmlEscape(q.Content)
	if len(q.Attachments) > 0 {
		body += fmt.Sprintf("\n（被引用消息含 %d 个附件）", len(q.Attachments))
	}
	return open + "\n" + body + "\n</reply>"
}
